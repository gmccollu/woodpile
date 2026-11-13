# Woodshed — v1 spec (draft)

Woodshed is a syslog server you can install on any Windows, macOS or Linux machine in a minute. It stores
every message it receives as plain files, keeps them within a disk budget, and has a clean web UI for
watching, searching and exporting logs.

Status: draft for review. Decisions marked **(decided)** came from the project owner; everything else is a
proposal and can change.

## 1. Goals and non-goals

**Goals**
- Simple install: one installer per OS, it runs as a service, and it listens straight away with no setup.
- Reliable: it never silently loses data it can see, never fills the disk, and survives reboots.
- Portable logs: plain text files that any tool (grep, an editor, a SIEM import) can read, and a one-click
  export for sending to someone else.
- A UI that's pleasant to look at and fast to read.
- A codebase that's easy to extend (TLS, alerts and forwarding are next).

**Non-goals for v1**
- Not a SIEM: no correlation, dashboards-as-code or long-term analytics.
- No multi-user accounts or roles.
- No forwarding or relaying to other systems.
- No outbound network connections of any kind **(decided)**.

## 2. Decisions so far

| Topic | Decision |
|---|---|
| Platforms | Windows, macOS and Linux at equal polish in v1 **(decided)** |
| UI access | Bound to localhost by default. Optional LAN mode, which requires a password and HTTPS **(decided)** |
| Storage | Plain files per device per day, plus a small SQLite index for time ranges **(decided)** |
| v1 scope | Core server, dashboard and live tail, search and export zip **(decided)** |
| Throughput | 10,000 msg/s sustained on 4 cores / 8 GB / SSD with no drops **(decided)** |
| Device identity | Source IP, with a friendly name and group set in the UI **(decided)** |
| Repo / license | Personal github.com account, MIT **(decided)** |
| Device config helper | Not in v1 **(decided)** |
| Telemetry / updates | None. No outbound connections; the version and a releases link are shown in the UI **(decided)** |
| First run | Zero-config: listens immediately, and the UI opens on a welcome page **(decided)** |
| Port 514 in use | The listener fails with a clear error; the UI still starts and explains the clash **(decided)** |
| UI port | 8514 on localhost (§5) **(decided)** |
| Linux UDP buffer | The deb/rpm raises `net.core.rmem_max` to 32 MiB through a sysctl drop-in (§3.1) **(decided)** |
| Rotation | Close at 100 MiB or midnight UTC, gzip, keep 30 days / 20 GB (§3.4) **(decided)** |
| Sizes | "GB" in this spec means GiB (1 GiB guard floor, 20 GiB cap) |
| Low-disk reserve | max(1 GiB, min(5% of the volume, 10 GiB)): 1 GiB on a 10 GB volume, 5 GiB on 100 GB, 10 GiB on 460 GB and 4 TB (§3.4) **(decided, 2026-11-11)** |
| Retention fairness | Over the size cap or under the low-disk reserve, delete first from devices first seen in the last 24 h, then from established devices over their share, then oldest-first. Shares split the cap by each device's average bytes a day over the last 7 days (1 KiB floor; no share below a tenth of an equal split). Every device established for over 24 h keeps its last min(retention days, 2) calendar days; the size cap never takes them, the low-disk guard only once every newer device's data is gone. A device sending over half of the last 24 h's bytes gets a warn event, at most hourly (§3.4) **(decided, 2026-11-11, revised for rc2)** |
| Devices cleanup | A device with no files left, no name, no group and not hidden, quiet for 24 h, is forgotten: index row, folder, and its exemption from the new-sender rate **(decided, 2026-11-11)** |
| `reindex` as root | Refused when the data folder belongs to another user; run it as that user (`sudo -u woodshed woodshed reindex`). Index files that are links or not regular files are refused **(decided, 2026-11-11)** |
| New-sender limits | At most 10,000 *active* senders (`max_devices`): a sender quiet for 10 min has its file closed early (its next message appends to the same file) and its memory freed, so the cap counts senders heard in about the last 10 min. Senders with no logs yet also need a token, first from their network's bucket (a /24, or a /48 for IPv6: burst 256, then 20 a minute), then from the global one (burst 5,000, then 200 a minute), so one network can't keep everyone else out and a new install on a 5,000-device network admits everyone at once; random sources spread over many networks can still drain the global bucket, and the sender allowlist is the answer to that. A sender with logs on disk is always admitted: at the cap it takes the least recently heard sender's slot (that sender's file is parked, not lost). While the cap is full, known senders that aren't in memory are readmitted at up to 500 a second (each evicts the least recently heard sender, whose file is parked); beyond that they're refused (reason `churn`); a sender already in memory is never refused. The cap and the global bucket are live settings. Refused messages count as dropped and raise the `device_limit` banner **(decided, 2026-11-11)** |
| Checkpoints | Every 1 MiB, or every minute once 64 KiB has been written since the last one |
| Durability | Plain `fsync` on every OS (on macOS that survives process and OS crashes, not necessarily power loss) |
| Late messages | A message for a day that's already closed goes into the current file with its own timestamp; files never reopen |
| Hidden devices | Stay hidden if they send again; the Devices page's hidden list shows their last-heard time |
| Stats | Per-minute buckets for 24 h, hourly buckets for 7 days |
| File modes | Log files 0640, directories 0750 |
| UDP buffer on macOS | macOS refuses (rather than caps) requests above `kern.ipc.maxsockbuf` (8 MiB), so the listener steps down to the largest size accepted. 10k msg/s passes with 8 MiB |
| Rejected/drop counters | Hourly buckets; "24 h" is the current hour plus the 23 before it. A refused TCP connection counts once |
| Live-tail sampling | Token bucket, 50 lines/s per subscriber; `sampled.rate` is that subscriber's matching rate |
| Measured (M1, Apple M4 Pro) | 10k msg/s for 30 s over UDP, TCP-LF and TCP-octet: 300,000 sent, 300,000 on disk, 0 missing each. Zero loss up to 60k msg/s (UDP) and 100k (TCP) |

## 3. Architecture

A single Go binary with the web UI embedded (`embed.FS`). There's no runtime, database server or separate
frontend install.

```
 UDP :514 ─┐
           ├─> parse (PRI, timestamps; raw always kept) ─> per-device router ─> buffered writer ─> logs/<device>/<day>.log
 TCP :514 ─┘                                   │                                   │
                                               ├─> live hub (ring buffer) ─> SSE ─> UI live tail
                                               └─> stats (per-device counters)      └─> index (SQLite: file, time ranges, offsets)
 background: rotator ─> gzip ─> retention (age + disk cap + low-disk guard)
 HTTP :8514 ─> REST API + embedded UI
```

**Proposed libraries** (pure Go, no cgo, so every target cross-compiles from one runner):
- `modernc.org/sqlite`: SQLite with no cgo.
- `github.com/kardianos/service`: one API for Windows services, systemd and launchd.
- The standard library for networking, HTTP, gzip and zip.
- Our own small syslog parser: it's lenient by design, and existing libraries reject too much.
- Frontend: Svelte 5 + Vite + Tailwind, built into `web/dist` and embedded. The bundle stays small and there's
  no virtual-DOM cost for the live tail.

### 3.1 Listeners
- UDP and TCP on 514 by default. Both ports and bind addresses can be changed in config.
- TCP framing: auto-detect octet-counting (RFC 6587 §3.4.1) or newline-delimited (LF, CRLF or NUL), per
  message. A frame counts as octet-counted when it starts with up to 10 digits, a space and `<`. After a
  connection's first octet frame, the `<` check is dropped for that connection. This keeps a newline-framed line
  starting with "123 " from being read as a count.
- Max message size is 64 KiB. Anything longer is truncated and counted (`truncated` counter), never dropped.
- UDP socket receive buffer: ask for 32 MiB. On Linux the kernel caps this at `net.core.rmem_max`, so the deb/rpm
  installs `/etc/sysctl.d/60-woodshed.conf` raising it to 32 MiB, and removes it on uninstall **(decided)**. On
  every OS, log the buffer size we actually got at startup and show it on the About page.
- Bind address: wildcard (`0.0.0.0` and `::`) by default. On macOS a non-root process may bind ports below 1024
  only on the wildcard address, not on a specific IP (tested on macOS 27.2, see §5). So on macOS a specific bind
  address with a port below 1024 is refused with a clear error; use the sender allowlist to restrict senders.
- **Port clash:** if a bind fails, that listener doesn't start. The service keeps running so the UI can show a
  red banner: which port and protocol, the OS error, the process holding it where the OS lets us find out, and
  how to fix it. A "Retry" button rebinds without restarting the service.
- Optional sender allowlist (CIDRs). It's empty by default, meaning everyone is accepted. Rejected senders are
  counted per IP and shown in the UI.

### 3.2 Parsing
- Parsing only adds metadata: facility, severity, device timestamp, hostname, app. **The raw message is always
  stored exactly as received.**
- Supports RFC 5424, RFC 3164 and common non-compliant variants (missing PRI, odd timestamps, BOMs). If parsing
  fails, the message is stored raw with severity "unknown".
- A fuzz test (`go test -fuzz`) runs on the parser in CI.

### 3.3 Storage layout
```
<data>/
  woodshed.toml            config (Linux: /etc/woodshed/woodshed.toml instead)
  index.db                 SQLite: devices, files, time-range checkpoints, stats
  logs/
    10.1.0.1/
      2026-11-10.log       today's file (open)
      2026-11-09.log.gz    rotated and compressed
      2026-11-08.001.log.gz  size-split parts (each closed at 100 MiB)
    fd00_1__5/             IPv6 addresses with ':' replaced by '_' (Windows-safe)
  exports/                 generated zips, deleted after 24 h
```
- Default data dirs: Windows `C:\ProgramData\Woodshed`, macOS `/Library/Application Support/Woodshed`, Linux
  `/var/lib/woodshed`.
- **Line format:** `<received UTC, RFC 3339 with ms> <sender IP> <raw message>`, one line per message. Embedded
  CR/LF in a message is escaped as `\r` / `\n` so one message is always one line. Example:
  `2026-11-10T20:41:07.112Z 10.1.0.1 <134>Nov 10 20:41:07 edge-fw-01 conn opened tcp ...`
- Files use the received time in UTC, because device clocks are often wrong or in local time. The device's own
  timestamp stays inside the raw message.
- Writes are buffered per device and flushed every 1 s or 1 MiB, whichever comes first; fsync every 5 s. If the
  process crashes we lose at most about 5 s of data, and that is documented.

### 3.4 Rotation, compression, retention
- Each device writes one plain active file. It's closed when it reaches **100 MiB** (configurable) **or** at
  00:00 UTC, whichever comes first. The midnight close keeps each file inside one day, so "logs from Tuesday"
  is a file-name lookup.
- A background worker gzips each closed file at low priority, then verifies the gzip before deleting the
  original. Only the active file per device is uncompressed.
- Retention applies to the compressed files. Defaults: **30 days or 20 GB total on disk, whichever comes first**
  **(decided)**. Age: every compressed file older than 30 days goes. Size: while all logs exceed the cap, delete
  in **fair-share** order.
  1. Devices first seen in the last 24 h, oldest file first: a flood of new senders pays first, however it's spread.
  2. Established devices over their share, the most over first, oldest file first. Shares split the cap by each
     device's average bytes a day over the last 7 days (from the stats, which outlive the files; 1 KiB floor, and no
     share below a tenth of an equal split), so a long-established busy device gets a large share and junk senders
     little.
  3. Everyone, oldest file first.

  Every device established for over 24 h keeps its last min(retention days, 2) calendar days (today included): the
  size cap never takes them, so it can be exceeded by them; the low-disk guard takes them only once every newer
  device's data is gone. Active (and parked) files are never deleted by the size cap. A device that sent more than
  half of the last 24 h's bytes is reported with a warn event, at most once an hour.
- Rotated files use gzip (`.log.gz`) rather than `.zip`. gzip streams, so search reads it without unpacking, and
  it's the standard for rotated logs. Exports are `.zip` (§4.1) because that's what people open by
  double-clicking.
- Low-disk guard: if free space on the data volume falls below the reserve, max(1 GB, min(5% of the volume,
  10 GB)), delete closed files until it's back above, in the same fair-share order. If that isn't enough, close the
  largest active files as parts and delete those too, rather than pausing until midnight (only when that can
  reach the reserve). The 10 GB cap keeps a large disk from reserving tens of
  GB it never needs (on a 460 GB disk, 5% alone paused writes with 24 GB still free). If nothing is left to
  delete, pause writing, count the dropped messages, and show a red banner that states the reserve. Never fill
  the disk.
- Every deletion is recorded in `index.db` (file, reason, time) and shown in Settings → Storage history.

### 3.5 Index
- One row per file: device, path, first and last received time, size, line count, compressed or not.
- Checkpoints every 1 MiB or 1 minute: (time, byte offset, uncompressed). Search and export use them to jump
  to a time range instead of scanning from the start.
- The index can be rebuilt from the files (`woodshed reindex`), so the files are always the source of truth.

### 3.6 Live hub and stats
- An in-memory ring buffer holds the last 5,000 messages. The UI live tail uses SSE with server-side filters
  (device, group, severity, text).
- Above about 50 lines/s the server sends a sample and an "N msg/s, showing a sample" notice, so the browser
  never falls behind. Pause freezes the view; the server keeps receiving.
- Per-device counters (messages, bytes, truncated, last heard) go into `index.db` in 1-minute buckets and are
  kept for 7 days for the sparklines.
- **Drop counting, honestly:**
  - We count every message we drop ourselves (disk guard, write errors).
  - On Linux we also read the kernel's UDP drop counter for our socket (`/proc/net/udp`).
  - On Windows and macOS kernel UDP drops aren't visible to us. The UI says so instead of showing a reassuring 0.

## 4. Web UI

The visual style follows the mockup (`design/mock.html`): soft glass cards, rounded tiles, light and dark
modes. **Log text is never on glass:** the live tail, search results and file viewer use a solid background,
monospace text, high contrast and severity colors.

| Page | Contents |
|---|---|
| Welcome (first run) | "Woodshed is listening": this machine's IPs, ports and protocols, listener status, a test command to send a message from this machine, and a link to the dashboard once the first message arrives |
| Dashboard | Stat cards (msg/s with a 1 h sparkline, devices online/quiet, disk used against the cap with days kept, dropped messages); device tiles (name, IP, a green/amber/red last-heard dot, msg/min); group tabs; the live tail |
| Devices | Table of all senders: rename, set group, show first/last seen and totals, "forget" (hides the device; its logs are kept until retention deletes them) |
| Search | Text (case-insensitive substring; a toggle for regex), devices/groups, time range, severity. Results stream in, capped at 10,000 with "load more". A context view shows ±50 lines around a hit |
| Export | Pick devices and a time range, then build a zip (see 4.1). Lists recent exports for download |
| Settings | Listeners (ports, bind address, allowlist), storage (folder, retention, part size, storage history), UI access (LAN mode, password, certificate), quiet thresholds |
| About | Version, build, OS, data folder, socket buffer achieved, a link to the releases page |

Quiet status defaults: green when heard within 5 min, amber up to 1 h, red after that. All three are
configurable.

### 4.1 Export zip
- Only the raw log lines in the chosen range, one file per device per day. They're decompressed so whoever
  receives them can read them straight away.
- `manifest.json`: Woodshed version, export time, range, devices (IP, name, group), per-file line count and
  SHA-256, and any gaps in range (for example "listener down 14:02–14:09", from the service's own event log).
- `woodshed-events.log`: the service's own events in the range (restarts, rotations, deletions, drops, bind
  errors). This way the recipient can tell "the device sent nothing" apart from "Woodshed wasn't running".

## 5. Security
- The UI is on `127.0.0.1:8514` by default **(decided)**. 8514 is unassigned in the IANA registry (checked
  2026-11-10), isn't on the browsers' blocked-port list (Chrome loaded a page on it), and was free on the test
  Mac. If it's taken at start-up, the service logs the error and `woodshed status` prints it.
- **LAN mode** (off by default):
  - Turning it on requires setting a password first (hashed with Argon2id).
  - The UI moves to HTTPS with a self-signed certificate generated on the machine. A user certificate can be
    loaded instead.
  - Sessions use secure, HttpOnly, SameSite=Strict cookies, with CSRF tokens on every change.
  - Login is rate-limited and locks out for a while after 10 failures.
- The REST API and UI share the same authentication. There are no unauthenticated routes in LAN mode except
  login and a static health check.
- Export and file routes accept only IDs from the index, never paths, so path traversal isn't possible.
- The service runs with the least privilege each OS allows:
  - Linux: a `woodshed` system user with `CAP_NET_BIND_SERVICE` for port 514.
  - Windows: a virtual service account (`NT SERVICE\Woodshed`) with write access only to its data folder.
  - macOS: a launchd daemon running as a dedicated `_woodshed` user. Tested 2026-11-10 on macOS 27.2 as an
    ordinary user: binding 514 on `0.0.0.0` and `::` succeeded over both UDP and TCP; binding 514 on `127.0.0.1`
    or the en0 address failed with "Permission denied". So the dedicated user works with the default wildcard
    bind. Confirmed in CI (2026-11-11, macos-latest): the launchd daemon as `_woodshed` binds UDP and TCP 514 on the
    wildcard. With the application firewall on, it reports the binary as permitted; no prompt appears headless.
    A prompt for a logged-in user on an unsigned binary is still unverified.
- No outbound connections, no telemetry, no update checks. The CI test suite asserts that no code path dials
  out.

## 6. Packaging and install

| OS | Package | Service | Notes |
|---|---|---|---|
| Windows | MSI (WiX v5) | Windows service, auto-start, restart on failure | Adds firewall rules for 514 UDP/TCP only; the UI port isn't opened unless LAN mode is turned on. Also published to winget and Scoop |
| macOS | `.pkg` (pkgbuild/productbuild) | launchd daemon | Universal binary (arm64 + x86_64). Also published as a Homebrew tap |
| Linux | `.deb`, `.rpm`, tarball | systemd unit | `amd64` and `arm64` |

- Builds run on GitHub Actions from a tag. GoReleaser OSS handles the binaries, nfpm (deb/rpm), Homebrew,
  Scoop and checksums. The MSI and `.pkg` are built in separate jobs on Windows and macOS runners (GoReleaser's
  MSI support is a paid feature).
- Signing: none at first. Add an Apple Developer ID (notarization) and Windows signing (SignPath's free OSS
  program, or Azure Trusted Signing) before promoting it widely. Until then the README documents the
  Gatekeeper and SmartScreen prompts.
- Uninstall stops and removes the service but **keeps the logs** unless the user ticks "also delete logs".
- Upgrade in place: the config and data folder are kept, and the index schema migrates on start.
- CLI on the same binary: `woodshed run|install|uninstall|status|reindex|send|bench|version`.

## 7. Quality bar
- Unit tests for the parser (plus fuzzing), framing, rotation and retention (with a fake clock and fake disk
  space), the index and export.
- Integration test: send 1M messages over UDP and TCP from the built-in `woodshed send` tool and check that the
  counts on disk match exactly, including across a forced rotation and a service restart.
- Benchmark: `woodshed bench` sustains 10k msg/s for 10 minutes with zero drops on the reference spec. It runs
  in CI on every release tag, and the results are recorded in the release notes.
- CI runs on Linux, Windows and macOS runners. The installers are installed, started, sent messages and
  uninstalled on each runner (an install smoke test).
- A 24 h soak test before v1.0: steady load, a forced low disk, a listener port clash, and a reboot.

## 8. Milestones
1. **M0 Skeleton:** repo, CI matrix, the binary serves the embedded "hello" UI, `woodshed run`.
2. **M1 Core server:** listeners, parser, per-device files, rotation, gzip, retention, disk guard, index,
   events log, `send` and `bench`.
3. **M2 UI:** welcome page, dashboard, devices, live tail, settings, light and dark.
4. **M3 Search and export.**
5. **M4 Installers:** MSI, pkg, deb/rpm, service install, upgrade and uninstall, install smoke tests in CI.
6. **v1.0:** soak test, docs (README, install per OS, FAQ), first tagged release.

### 8.1 M4 status (2026-11-11)
Proven in CI on GitHub-hosted runners (`.github/workflows/install-smoke.yml`; on each: least-privilege account,
UDP/TCP 514, the UI, 10,000 messages on disk exactly once, restart, uninstall keeping data, delete-logs removing it):
- deb (Ubuntu, systemd, `CAP_NET_BIND_SERVICE` only), rpm (Fedora 41 container with systemd as PID 1), MSI
  (`NT SERVICE\Woodshed`, protected ACL, 514-only firewall rules, event log, v0.0.1→v0.0.2 major upgrade), pkg
  (launchd as `_woodshed`), and `woodshed install` / `uninstall` on all three OSes, which now matches the packages.
- UI shutdown with an idle connection: 5.0 s → 0.14 s.
- 2 h soak on macOS at 3,000 msg/s: 21.6M sent, every message on disk except the 183,000 counted as dropped during
  a forced low-disk pause (exactly equal), 0 duplicates; RSS 31→37 MiB (peak 45), 19–23 open files.

Still unproven:
- Inbound traffic from another host through the OS firewalls (CI used loopback).
- A real reboot; the 24 h soak (§7); a midnight crossing on the real binary (no fake clock in the binary).
- arm64 packages; the macOS firewall prompt for a logged-in user; `reset-password` restarting a real service.

## 9. Roadmap (after v1)

**Scope rule:** Woodshed is an *evidence collector for troubleshooting*: it collects, keeps, searches and exports what
network devices send, so it can be handed to whoever is helping. It isn't a monitoring system, a flow-analytics
product or a SIEM. Each new data type has to fit that rule (decided 2026-11-11).

### 1.x
- **Pluggable data types:** generalise the pipeline (listener → per-device store → index → search → export) so
  each data type adds its own listener, record format, UI tab and export section, with syslog as the first.
  Devices stay keyed by source IP across types.
- **SNMP trap receiver** (UDP 162; v1, v2c, and v3 with user-configured credentials stored like the UI password).
  Traps are stored decoded as text lines, with varbinds and the raw PDU, in the same per-device, per-day files. No
  MIB compiler in the first version; numeric OIDs, plus an optional folder of MIB files for names.
- **Events viewer, next steps** (after the 1.0 Events page: program/tag, host and exclude filters, clickable columns):
  a field panel with each field's top values (device, severity, host, program/tag, facility) where a click adds a filter;
  a histogram of matches over time where dragging picks the range; AND / OR / NOT between terms; saved searches. All
  over the same stored files, no new index type. A query language and dashboards stay out (see the scope rule).
- **UDP receive off the write path:** today the UDP reader writes each message itself, so a slow disk (fsync,
  rotation) backs up into the OS socket buffer, where loss isn't counted on Windows or macOS. Hand messages to a
  bounded queue and count drops when it's full, so every loss Woodshed causes shows in `dropped_24h`. A 2026-11-12 lab
  soak on Windows lost ~20 per million UDP in bursts near the top of the hour; the cause wasn't found in Woodshed.
- TLS syslog (RFC 5425, port 6514).
- Alerts: device quiet, error-rate spike, disk filling, drops (email and webhook). Simple thresholds only.
- Device identity by hostname as an option, for relayed traffic.
- Forwarding to another collector; RELP.
- Signed installers.
- An opt-in update check (off by default).

### 2.0
- **NetFlow v5/v9 and IPFIX collector** (UDP 2055/4739, configurable): template handling per exporter and
  observation domain, compact columnar storage per device per hour, retention and the disk guard shared with
  syslog. Search by time, IPs, ports and protocol; simple top-talker and top-port views for a time window;
  windowed export (CSV or IPFIX file). No long-term flow analytics, baselining or anomaly detection.
- **The export zip covers every data type**: syslog, traps and flows for the chosen devices and window, with one
  manifest.
- A small **OID watcher** (maybe): poll a handful of OIDs (CPU, memory, interface counters) on chosen devices at a
  fixed interval and show them over time. Not a general SNMP poller.
- Device setup helper (generated logging, trap and flow-export config per vendor).

### Out of scope
- SIEM features: correlation rules, threat detection, compliance reporting, case management.
- Full network monitoring: discovery, topology, general SNMP polling, dashboards-as-code.

## 10. Open questions
1. Where the repo lives. The answer given was the internal GHE, which conflicts with "open source on
   github.com" in §2 (see the reply of 2026-11-10).
2. Retention fairness: decided 2026-11-11 (fair shares, §3.4). An optional per-device cap or minimum
   days is still open.
3. macOS launchd daemon as `_woodshed` and the application-firewall prompt: test in M0 (see §5).
