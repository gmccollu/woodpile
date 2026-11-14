# Woodshed — v1 spec (draft)

Woodshed is a syslog server you can install on any Windows, macOS or Linux machine in a minute. It stores
every message it receives as plain files, keeps them within a disk budget, and has a clean web UI for
watching, searching and exporting logs.

Status: draft for review. Decisions marked **(decided)** came from the project owner. Those marked
**decided (coordinator, date, owner-delegated)** are calls the coordinating agent made under the owner's delegation;
the owner can still overrule them. Everything else is a proposal and can change.

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
- No outbound network connections of any kind **(decided)**. 1.1 adds one exception, the opt-in update check (§10.2)
  **(decided, 2026-11-13)**.

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
| Telemetry / updates | None. No outbound connections; the version and a releases link are shown in the UI **(decided)**. From 1.1: still no telemetry, plus an opt-in update check that asks once, makes one plain HTTPS GET of the public releases file a day when on, and never downloads or installs anything (§10.2) **(decided, 2026-11-13)** |
| Update check details (1.1) | `updates.check` = ask / on / off, `ask` on fresh installs, upgrades and portable; the URL is the existing Pages file `releases.json`; User-Agent `Woodshed` with no version; no query, cookies or conditional headers; proxy from the environment; no redirects; only `internal/updatecheck` may dial (§10.2) **decided (coordinator, 2026-11-13, owner-delegated)** |
| TLS syslog (1.1) | Off by default, one Settings switch, TCP 6514 by default (port configurable) **(decided, 2026-11-13)**. TLS 1.2 and 1.3 (1.0/1.1 only behind `allow_legacy_tls`); no SNI, ALPN or client certificate needed; Woodshed's own RSA-2048 CA and leaf (until 2037-12-31, the leaf 10 y at most), changed only on an explicit renew; the same framing and limits as TCP; the MSI ships its firewall rule disabled, and `woodshed tls on` enables it (§10.3); the details are **decided (coordinator, 2026-11-13, owner-delegated)** |
| Events v2 (1.1) | Field panel and histogram over the same files and index. Field counts are exact over a stated span (newest first), never sampled. The histogram comes from the stats and checkpoints when the query is unfiltered, from the same scan otherwise. Boolean terms only with `syntax=terms`, so 1.0 links keep their meaning. Saved searches live in `index.db`. Every endpoint takes `type` (only `syslog` in 1.1) (§10.1) **decided (coordinator, 2026-11-13, owner-delegated)** |
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
  out. From 1.1 the one exception is the opt-in update check (§10.2): tests assert that nothing else dials out, and
  that it doesn't either unless it's on.

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

### 1.0.1
- **TCP idle timeout from 10 min to 24 h** (to build on main). Today `DefaultIdleTimeout = 10 * time.Minute`
  (`internal/listen/listen.go:31`) closes a TCP connection that hasn't completed a message for 10 min. Some senders
  send no keepalives and notice a closed connection only when a write fails. They lose that message, and some
  firewalls block traffic at that moment, because a failed write to a TCP log host counts as "host down".
  - Raise the default to 24 h, configurable as `listeners.tcp_idle_timeout` (file-only like the other TCP limits;
    1 min to 7 d). The same value applies to the TLS listener (§10.3).
  - Slots are still freed under pressure: when `MaxConns` are open, the existing eviction closes the connection idle
    longest (idle over `EvictIdle`, 30 s).
  - A longer timeout lets stale connections fill a sender's own per-source limit (32) when it reboots without closing
    them. So at that limit, the newcomer evicts that sender's own connection idle longest (over `EvictIdle`) instead
    of being refused. Server-side TCP keepalives (Go's default on accepted connections) stay on, so a half-open
    connection to a rebooted sender still closes within minutes.
  - Tests: a quiet connection survives 23 h on a fake clock; eviction still works when `MaxConns` are full; a source at
    its limit with stale connections is let in.

### 1.1
- Events v2 (field panel, histogram, boolean terms, saved searches), the opt-in update check, and TLS syslog
  (RFC 5425, TCP 6514), designed in §10.

### 1.2
- **Pluggable data types:** generalise the pipeline (listener → per-device store → index → search → export) so
  each data type adds its own listener, record format, UI tab and export section, with syslog as the first.
  Devices stay keyed by source IP across types. Events v2 already takes a `type` (§10.1.6).
- **SNMP trap receiver** (UDP 162; v1, v2c, and v3 with user-configured credentials stored like the UI password).
  Traps are stored decoded as text lines, with varbinds and the raw PDU, in the same per-device, per-day files. No
  MIB compiler in the first version; numeric OIDs, plus an optional folder of MIB files for names.

### 1.3
- **SNMP poller ("OID watcher")**, moved up from 2.0 (owner, 2026-11-13). It collects evidence, so it's small and
  fixed:
  - It polls a small fixed set every 30–60 s: sysUpTime; IF-MIB in/out octets (the HC counters), errors and
    discards; CPU and memory through HOST-RESOURCES-MIB where the device supports it.
  - Each device can add a list of custom OIDs. The presets stay vendor-neutral.
  - Counters are stored as rates in a small per-device time-series store, with the same retention as the logs.
  - They're shown as graphs on the Events timeline, for the same window.
  - A drop in uptime is detected as a reboot, and marked on the timeline and in the export.
  - Poll data is included in the export zip.
- Security:
  - It polls only addresses the user configured.
  - v2c communities and v3 auth/priv keys are encrypted at rest with the OS key store where there is one (DPAPI on
    Windows, the Keychain on macOS), and otherwise kept in a file only the service account can read.
  - SECURITY.md's outbound rule gains "SNMP polls to devices you configure".
- Out of scope: discovery, topology, polling hundreds of OIDs, threshold dashboards, a MIB browser.
- It shares the SNMP library and the v3 credential handling with the 1.2 trap receiver, which is why it comes after
  it.

### Later 1.x (not yet ordered)
- **UDP receive off the write path:** today the UDP reader writes each message itself, so a slow disk (fsync,
  rotation) backs up into the OS socket buffer, where loss isn't counted on Windows or macOS. Hand messages to a
  bounded queue and count drops when it's full, so every loss Woodshed causes shows in `dropped_24h`. A 2026-11-12 lab
  soak on Windows lost ~20 per million UDP in bursts near the top of the hour; the cause wasn't found in Woodshed.
- Alerts: device quiet, error-rate spike, disk filling, drops (email and webhook). Simple thresholds only.
- Device identity by hostname as an option, for relayed traffic.
- Forwarding to another collector; RELP.
- Signed installers.

### 2.0
- **NetFlow v5/v9 and IPFIX collector** (UDP 2055/4739, configurable): template handling per exporter and
  observation domain, compact columnar storage per device per hour, retention and the disk guard shared with
  syslog. Search by time, IPs, ports and protocol; simple top-talker and top-port views for a time window;
  windowed export (CSV or IPFIX file). No long-term flow analytics, baselining or anomaly detection.
- **The export zip covers every data type**: syslog, traps, polls and flows for the chosen devices and window, with
  one manifest.
- Device setup helper (generated logging, trap and flow-export config per vendor).

### Out of scope
- SIEM features: correlation rules, threat detection, compliance reporting, case management.
- Full network monitoring: discovery, topology, general-purpose SNMP polling (beyond the 1.3 poller's small set),
  dashboards-as-code.

## 10. 1.1 design (draft, 2026-11-13)

Three features: Events v2 (§10.1), the opt-in update check (§10.2) and the TLS syslog listener (§10.3). Owner decisions are marked **(decided)**; the delegated calls are
listed in §10.4. The API contract is in `docs/api.md` → "1.1 (planned)".

### 10.1 Events v2

Everything here reads the same stored files and the existing index (file rows, checkpoints, minute and hourly
stats). There's no new index type and no query language beyond the boolean terms in §10.1.3 (the §9 scope rule).

**New filters**, on search, the summary, export and saved searches (the live tail is unchanged in 1.1):
- `facilities=0,4,16,unknown`: like `severities` (0–23, `unknown`, `none`), read from the PRI.
- `exclude_devices`, `exclude_app`, `exclude_host`: the opposites of `devices`, `app` and `host`, with the same
  matching rules. When a value is both included and excluded, the exclusion wins.
- Severity and facility are excluded by sending the complement set, so they need no exclude parameter.
- `syntax=text|terms` (§10.1.3) and `type=syslog` (§10.1.6).

#### 10.1.1 Field panel
A panel beside the results lists, for the current query and range, each field's top values and their counts:
device, severity, facility, host (the header hostname) and program/tag. The server lists the fields and how each maps
to a filter (`GET /api/search/schema`); the client has no field list of its own, so a new data type brings its own
fields (§10.1.6).
- Clicking a value adds it as a filter. Values of one field OR together, and different fields AND together.
  Alt/Option-click, or the row's "−" button, excludes the value. Each row also has a "+" button, for touch and
  keyboard. Clicking an active filter's chip removes it. Clicking a value twice doesn't cycle through states: a
  click that does something different each time is easy to get wrong.
- Lines with no hostname or program/tag are counted as "(none)". That row isn't clickable, because no filter can
  express it yet.
- Each field shows up to 10 values (`top`, at most 50), then "N others" and the number of distinct values. Severity
  and facility show every value that has a count.

**Counts are exact over a stated span; they're never sampled and never shown with "≈".** One scan, using the search
engine, reads newest first and counts every matching line. Each response covers `[counted_from, to)` exactly, and the
panel says so ("Counted 14:02–15:00 of 12:00–15:00"). The UI keeps asking with the cursor, as search does, for up to
60 s of scanning per query, then offers **Count the rest**. Why exact:
- The counts are evidence. "web-01 sent 1,204 of these" has to match what Export puts in the zip, and a scaled
  sample can't be checked against it.
- Sampling fails on the query troubleshooting runs most: a rare string in a busy day. A fixed sample finds 0–3 hits
  and scales them into noise. The scan's cost is the bytes it reads, not the number of matches, so a sample saves
  little exactly where it's needed.
- Reading newest first makes a partial answer useful: the last hour of a 24 h range is counted first, and its counts
  are exact.
- The cost is that a busy server takes many budgets over a long range. The panel states its span instead of hiding
  it, the fix is a narrower range or fewer devices, and the histogram is instant for unfiltered queries.

The summary tracks at most 10,000 distinct values per field. After that, new values count under "others"
(`distinct_capped`).

#### 10.1.2 Histogram
A histogram above the results shows the matches per time bucket for the current query. Dragging across it selects a
range, snapped to bucket edges, and runs the search on that range. The range goes into the URL, so Back zooms out.

The bucket size depends on the range (`to − from`), giving at most 120 buckets:

| Range up to | Bucket | | Range up to | Bucket |
|---|---|---|---|---|
| 2 min | 1 s | | 30 h | 15 min |
| 10 min | 5 s | | 5 d | 1 h |
| 30 min | 15 s | | 10 d | 2 h |
| 2 h | 1 min | | 30 d | 6 h |
| 10 h | 5 min | | 120 d | 1 d |
| | | | longer | 7 d |

So the presets get: 15 min → 15 s buckets (60 bars), 1 h → 1 min (60), 24 h → 15 min (96), 7 d → 2 h (84). The
default 1 h view is on whole minutes, so it can use the stats. Buckets start at multiples of their size since the
Unix epoch, in UTC (7 d buckets start on Monday 00:00 UTC). The first and last buckets count only lines inside
`[from, to)`. `bucket=` overrides the size with one from the table, up to 1,000 buckets.

The histogram has two sources:
- **The index** (`source: "index"`), when the query is unfiltered and the bucket is at least 1 min. Unfiltered means
  no `q`, `exclude`, `app`, `host`, their exclude forms, or facility or severity filter. A device selection (devices,
  group, ungrouped, `exclude_devices`) still counts as unfiltered, because the stats are per device. No log file is
  read, so it answers in milliseconds and is complete in the first response.
  - Buckets in the last 24 h come from the per-minute stats. Buckets of 1 h or more in the last 7 d come from the
    hourly stats.
  - Anything older comes from the files' checkpoints (stats are kept for 7 d, logs for 30 d by default), and so does a
    minute-sized bucket older than 24 h. The lines between two checkpoints (and between the last one and the file's
    last received time) are spread evenly over that time. With 1 d buckets this is exact, because files never cross
    midnight UTC and each file's row has its line count; only the first and last buckets, where `from` and `to` cut a
    day, can be approximate. An approximate bucket carries `approx: true`, and the UI shows "≈" in its tooltip.
  - Stats outlive the files that retention deletes. For each device, stats from before its oldest remaining file are
    left out (search would find nothing there), and the bucket that straddles that point is taken from the
    checkpoints.
  - Hidden devices follow search's device selection, and the `_refused` counter is never included.
- **A scan** (`source: "scan"`) otherwise. It's the same scan as the field panel's (one pass feeds both), with the
  same budget, cursor and `counted_from`. Buckets older than `counted_from` are `null` and are drawn hatched, as "not
  counted yet".

#### 10.1.3 Boolean terms
`syntax=terms` turns `q` (and `exclude`) into terms joined by AND, OR and NOT, with parentheses and quoted phrases.
The default stays `syntax=text`, the 1.0 meaning: `q` is one substring, so `deny tcp` still matches only that phrase.
Every existing link, bookmark, export and "Open in Events" keeps its meaning. The UI's Regex toggle becomes a
three-way switch, **Text | Terms | Regex** (`re=1` or `syn=terms` in the page URL).

Why a parameter, rather than switching it on when the query contains operators: syslog text is full of the words and
characters such a rule would key on ("NOT FOUND", `"GET /index.html"`, `(pid 312)`), so a 1.0 link could silently
change meaning. A parameter is explicit, is kept in saved searches, and costs one toggle.

Grammar (whitespace separates tokens):
```
expr   = or
or     = and { "OR" and }
and    = unary { [ "AND" ] unary }        (two terms side by side mean AND)
unary  = "NOT" unary | atom
atom   = word | phrase | "(" expr ")"
phrase = '"' { any character except '"' and '\' | '\"' | '\\' } '"'
word   = a run of characters other than whitespace, '(' and ')', not starting with '"'
```
- Precedence: NOT, then AND, then OR, each left to right. `a OR b c` means `a OR (b AND c)`.
- Only the upper-case words `AND`, `OR` and `NOT` are operators; `and`, `or` and `not` are ordinary terms. To search
  for the word AND, quote it: `"AND"`.
- Each word and phrase is a substring of the raw message, matched exactly as text mode matches `q`:
  case-insensitive unless `case=true` (which applies to every term), with the same Unicode folding. A phrase is one
  substring, spaces included: `"deny tcp"` in terms mode equals `deny tcp` in text mode.
- Outside quotes, `(` and `)` always group. Quote a term that contains them (`"(pid 312)"`). A `"` inside a word is
  an ordinary character, so `user="bob"` is one word.
- `NOT x` on its own is allowed and matches every line without x. An empty `q` matches everything, as now.
- `regex=true` together with `syntax=terms` is a 400. RE2 already has alternation, and a regex inside the term
  grammar would need quoting rules of its own (regex terms like `/…/` aren't in 1.1, §10.4).
- Limits: 32 words and phrases, parentheses nested up to 8 deep, and 4 KiB of `q`.
- Errors are 400s that give a 1-based character position. For `exclude`, the message starts with `exclude: `.
  - `Unclosed quote starting at character 12.`
  - `Unmatched ")" at character 20.` / `Missing ")" for the "(" at character 3.`
  - `"OR" at character 9 needs a term on both sides.` (the same for AND)
  - `"NOT" at character 30 needs a term after it.`
  - `Empty phrase at character 5.` / `Empty parentheses at character 5.`
  - `Too many terms (40); the limit is 32.` / `Parentheses nested deeper than 8.` / `The query is longer than 4 KiB.`
  - `Terms and regex can't be combined: pick one.`
- Highlighting marks the words and phrases that aren't under a NOT.
- Implementation note: the escaped-bytes prefilter uses the longest term that every match must contain (a positive
  term on the top-level AND chain), when there is one.

#### 10.1.4 Saved searches
Saved searches are named and stored on the server, so everyone who can open the UI shares them (there are no
accounts).
- A saved search holds every Events parameter:
  - `type`, `q`, `syntax`, `regex`, `case` and `exclude`;
  - the device selection (`devices`, `group`, `ungrouped`, `exclude_devices`);
  - `severities`, `facilities`, `app`, `exclude_app`, `host`, `exclude_host` and `order`;
  - the range: relative (`{"last": "1h"}`, from 1 min to 400 d) or absolute (`from`, `to`). Saving a preset range
    saves it as relative.

  Display settings (the raw view) aren't saved.
- Limits: 200 saved searches. A name is 1–80 characters after trimming, unique ignoring case. A saved search is
  validated like `/api/search`, so a bad regex can't be saved.
- They're stored in `index.db`, in a new `saved_searches` table (a schema migration on start), not in the TOML.
  - Device names, groups and hidden flags already live in `index.db`, and `reindex` keeps them. Saved searches are the
    same kind of user data. `reindex` rebuilds only the files and checkpoints tables.
  - Each save is one row in one transaction. In the TOML, every save would rewrite the user's config file and change
    the settings ETag, so someone with Settings open would get a 409 because someone else saved a search. On Linux the
    TOML is in `/etc/woodshed`, which config-management tools often overwrite.
  - The cost: deleting `index.db` loses them, as it loses device names. Each saved search has **Copy link**, and the
    API returns plain JSON for a backup.
- **Deleted devices:** a saved search keeps the device IDs it was saved with, and Woodshed never edits it behind the
  user's back. Device IDs are source IPs, so a forgotten device that sends again matches again.
  - Until then, the API lists the IDs it can't find (`missing_devices`), the UI shows them struck through as "no
    longer known", and the search runs with the rest.
  - If every named device is gone, the selection matches nothing, as `/api/search` does today. It never widens to all
    devices. A group with no devices left behaves the same way (`missing_group: true`).
- UI: a **Saved** menu on Events (open, save, save as, rename, delete). Opening a saved search fills the form and the
  URL with its parameters, plus `saved=<id>`. **Save** updates it, and the form shows "changed" once it differs.

#### 10.1.5 Load
- A summary request has its own budget (`budget_ms`, 2 s by default, at most the 10 s search budget), so the panel
  updates every couple of seconds.
- The running counts stay on the server for 10 min after the last request, for at most 8 summaries, least recently
  used out first. A cursor whose counts are gone gets a 400, and the UI starts again.
- At most 2 summary scans run at once. The others wait within their own budget and return what they have. Result
  searches never wait for summaries.
- A summary stops when its HTTP request is cancelled. The UI cancels it on every new query.

#### 10.1.6 Other data types
1.2 adds SNMP traps, and 2.0 adds flows, "under the same hood" (§9). Events v2 is shaped so that they plug in without
breaking the API.
- **Shared:**
  - the time range and the received-time order;
  - device selection by source IP;
  - the budget, cursor and partial rules;
  - the histogram (counts per bucket), with its index and scan sources;
  - the summary response's shape and the field descriptors;
  - saved searches (`type` plus `query`).
- **Per type:**
  - the record format and its parser;
  - what `q` matches (for a trap, its decoded text line; flows may have no free text);
  - its fields and their filter parameters, listed by `GET /api/search/schema` (for example trap OID and SNMP
    version, or protocol and port);
  - its stats, for the index histogram;
  - how its records show in the results.
- Every endpoint takes `type`, defaulting to `syslog`. 1.1 accepts only `syslog`; anything else is
  `400 type: unknown data type "x"`. A saved search's `query` is validated by the rules of its type. The first step
  in 1.2 is a second entry in the schema's list.
- 1.3's poll graphs aren't a searchable type: they sit on the histogram's time axis, for the same window and with
  the same bucket table.

### 10.2 Opt-in update check

**(decided, 2026-11-13: off until the user agrees; it asks once; a plain HTTPS GET of the public releases list; it
never downloads or installs anything.)** When it's on, Woodshed fetches the project's public releases file once a day.
If there's a newer version, it shows a banner that links to that version's release page.

**Asking.** `updates.check` is `"ask"`, `"on"` or `"off"`.
- The default is `"ask"`: for fresh installs, for upgrades from 1.0 (their config has no `[updates]`), and in portable
  mode.
- While it's `"ask"`, nothing is fetched, and `/api/status` carries the info banner `update_prompt`: "Check for new
  Woodshed versions once a day? It fetches one public file from gmccollu.github.io and sends nothing about this
  server." It has two buttons, **Turn on** and **No thanks** (action `update_choice`). Either answer is written to the
  config, and the banner doesn't come back. Settings → Updates has the switch either way.
- Portable mode asks too. A portable copy on a laptop is the one most likely to stay on an old version, and the answer
  is saved in its own folder.
- To decide for many machines, set `[updates] check = "off"` (or `"on"`) in `woodshed.toml` before the first start.
  There's no banner then.
- Silent fleet installs set it at install time with `WOODSHED_UPDATE_CHECK=on|off|ask` (**decided (coordinator, 2026-11-13, owner-delegated)**):
  - MSI: a public property, `msiexec /i woodshed_X.Y.Z_windows_amd64.msi /qn WOODSHED_UPDATE_CHECK=off`.
  - pkg, deb and rpm: an environment variable the install script reads, as in
    `sudo WOODSHED_UPDATE_CHECK=off apt install ./woodshed_X.Y.Z_amd64.deb`.
  - When it's set, the installer runs `woodshed update-check <value> --config <path>` (a new admin command, like
    `lan-mode`, which also serves headless servers), on a fresh install and on an upgrade alike: an explicit choice
    wins. When it isn't set, the config is left alone. An invalid value is ignored with a warning in the install log;
    it never fails the install, because a half-configured deb is worse than a missing setting.
  - The install smoke tests check each one. Whether macOS `installer` passes the variable through to the pkg's
    scripts, and `sudo` and dpkg to the deb's, is an acceptance item for the 1.1 build (§10.4); if `installer`
    doesn't, the pkg documents pre-seeding the TOML instead.
- A build whose version isn't a release (`0.0.0-dev` from `make build`), or that has no `releases_url`, never checks
  and never asks. Settings says "Not available in this build".

**The request.**
- The URL is `https://gmccollu.github.io/woodshed/releases.json`, a constant in `internal/updatecheck` that can't be
  configured. Why this file and not the GitHub API:
  - It already exists. `pages.yml` writes it from the releases API on every release change and daily, for the
    website's download section. `site/tools/releases.mjs` gives it the shape `{generated_at, stable, prerelease}`, each
    a release with `tag_name`, `html_url`, `published_at` and its assets, or `null`. The checker reads only `stable`
    and `prerelease`, and from each only `tag_name` and `html_url`.
  - It's static, about 8 KB, cached by a CDN, and has no API rate limit. The anonymous API allows 60 requests an hour
    per IP, so a shared company egress address with a few installs and browsers would run out, and the check would
    fail. This is the same reason `pages.yml` gives for writing the file at all.
  - Its schema is ours. `releases.mjs`'s output becomes a contract: those keys stay, and a test in `site/test` pins
    them.
  - The trade-off: a Pages outage or a failed deploy makes checks fail or lag by a day. Both are visible in Settings,
    and both are harmless.
- It's a plain `GET`: no query string, no cookies (no cookie jar), no `Authorization`, and no `If-None-Match` or
  `If-Modified-Since` (an ETag can work as an identifier). The only headers are `Host`, `User-Agent: Woodshed`,
  `Accept: application/json` and `Accept-Encoding: gzip`.
- **The User-Agent is `Woodshed`, with no version.** The comparison is local, so the server never needs the version. A
  version in every daily request would let the host count installs per version and see who's behind on fixes; a bare
  name doesn't. Go's default `Go-http-client/1.1` would be just as anonymous, but it hides what's calling; naming it
  is more honest to whoever reads the proxy logs.
- Proxy: it honours `HTTPS_PROXY` / `https_proxy` and `NO_PROXY` (Go's `ProxyFromEnvironment`). The service sees only
  variables set in its own environment (systemd `Environment=`, the launchd plist, the Windows service's environment).
  The OS's system proxy settings aren't read, and the FAQ says so.
- TLS uses the system's roots, version 1.2 or later, and there's no option to skip verification.
- Redirects aren't followed; a redirect is an error. The URL is a contract and must not move: a custom domain for the
  site would have to keep this path answering.
- Limits: a 10 s timeout for the whole request and a 1 MiB response, JSON only.
- It has its own `http.Transport` and `net.Dialer` in `internal/updatecheck`, the only package allowed to connect to a
  host that Woodshed picks itself.

**Schedule.**
- The first check runs 2–10 min (at random) after start-up. If one ran in the last 24 h, the next is due 24 h after
  it instead. The last check's time is kept in `<data>/update-check.json` (mode 0640).
- After that, checks run every 24 h ± 1 h, picked at random each time. After a failure it retries once 1–2 h later,
  then waits for the next daily check.
- Turning it on checks within a minute. **Check now** in Settings runs a check (at most once a minute). Turning it off
  cancels the next check and clears the banner.
- Each check is logged in the service log (time and result), not in the events log.

**The result.**
- Versions are compared as SemVer, without the `v`. An identifier such as `rc10` compares its letters, then its
  number, so `rc10` is newer than `rc9`.
- A release build looks at `stable`. A prerelease build also looks at `prerelease`. If either is newer than the
  running version, the newer of the two counts as available.
- When one is available, an info banner `update_available` appears: "Woodshed 1.2.0 is available (this is 1.1.0)." It
  can be dismissed and has a link (`url`) to the release page.
  - The link must be `releases_url + "/tag/" + tag_name`. If `html_url` says anything else, it's ignored, and the
    banner links to `releases_url`.
  - Woodshed writes the text from the parsed version alone, and nothing else from the file is shown, so a tampered
    file can't put words or links in the UI.
- Dismissing the banner hides it for that version, for everyone (kept in `update-check.json`). A newer version shows
  it again.
- Settings → Updates shows the switch, the URL, the last check (its time and result: "OK, 1.1.0 is the latest", or the
  error), the next check, and **Check now**. A failure shows only there, as in "Last check failed 3 h ago: TLS
  certificate not trusted (a proxy that inspects HTTPS?)". The error kinds are `dns`, `connect`, `proxy`, `tls`,
  `timeout`, `http_status`, `redirect`, `too_large`, `bad_json` and `no_release`.

**Tests.**
- `internal/api`'s `TestNoClientNetworkAPIs` and `TestNoHandlerDialsOut` stay as they are, on the default config
  (now `"ask"`).
- A new module-wide static check: only an allowlist of packages may use the client-side network and process APIs that
  `TestNoClientNetworkAPIs` forbids, namely `internal/updatecheck` plus the packages that use them today
  (`internal/loadgen` for `woodshed send` and `bench`, `internal/peer` for loopback to the local Woodshed, and the OS
  helpers in `internal/listen`, `internal/service` and `cmd/woodshed`). Adding a package to it is a reviewed change.
  The releases host appears in no other package.
- A new runtime test, `TestOnlyUpdateCheckDials` in `internal/server`. It runs the real server with a fake clock, the
  default transport and resolver hooked as in `TestNoHandlerDialsOut`, and the checker's dialer pointed at a local TLS
  server.
  - With `"ask"` and with `"off"`: 3 simulated days with every route exercised (the new ones too) make 0 dials and 0
    lookups.
  - With `"on"`: exactly one request inside the start-up window, then one every 24 h ± 1 h. Each is
    `GET /woodshed/releases.json` with an empty query and exactly the headers above, and nothing else dials or looks
    anything up.
  - Turning it off while it waits: no further request. A restart within 24 h of a check: no start-up check.
  - With `HTTPS_PROXY` set, the one connection is a `CONNECT gmccollu.github.io:443` to the proxy. With a matching
    `NO_PROXY`, it goes direct.
  - Bad responses (a redirect, 1 MiB + 1 byte, not JSON, an off-site `html_url`, no release) give an error in
    Settings, no banner, and no retry beyond the one.

**Doc wording** (applied in the PR that ships the checker, build 2026-11-13). Apply this in the PR that ships the checker, not before. While 1.0 is the latest release, its docs
have to keep saying that it makes no outbound connections, and SECURITY.md's policy covers the latest release.
Editing them on main now would make them wrong for 1.0 users, so the exact wording is here instead.

- **README.md**, Features, replacing "No outbound connections, no telemetry, no update checks.":
  > No telemetry. The only outbound connection is an optional daily update check, off until you turn it on: it
  > fetches one public file (`https://gmccollu.github.io/woodshed/releases.json`), sends nothing about your
  > installation, and never downloads or installs anything.
- **SECURITY.md**, "What counts", replacing the last item:
  > - Woodshed making any outbound network connection other than the opt-in update check: one HTTPS `GET` of
  >   `https://gmccollu.github.io/woodshed/releases.json` a day, made only after an administrator turns it on. A check
  >   while it's off or still asking, a request that sends more than that (a query string, a cookie, an identifier, the
  >   version), a connection anywhere else, or an update banner that links outside this repository's releases all
  >   count.
- **FAQ.md**, "Does Woodshed connect to the internet?":
  > Only if you turn on the update check. The first time you open the UI, Woodshed asks once whether to check for new
  > versions once a day. Until you say yes, and after **No thanks**, it makes no outbound connections at all. You can
  > change your answer any time in **Settings → Updates**.
  >
  > When it's on, Woodshed fetches one public file once a day, `https://gmccollu.github.io/woodshed/releases.json`,
  > with a plain HTTPS GET: no query string, no cookies, and a User-Agent of just `Woodshed` (no version). Nothing
  > about your installation, devices or logs is sent. As with any web request, your DNS server and the site's host
  > see your public IP address and the time. It uses `HTTPS_PROXY` and `NO_PROXY` if they're set in the service's
  > environment; it doesn't read the system's proxy settings. If a newer version is out, the UI shows a banner with a
  > link to its release page. Woodshed never downloads or installs anything itself. Settings shows when it last
  > checked and whether that worked.
  >
  > There's no telemetry. The version is on the **About** page, with a link to the releases page if you'd rather check
  > yourself. To decide for many machines at once, set `check = "off"` (or `"on"`) under `[updates]` in
  > `woodshed.toml` before the first start. Versions before 1.1 have no update check and make no outbound connections.
- **docs/user-guide.md**, a new section before "Stop or remove it":
  > **Updates.** The first time you open the UI, a banner asks whether Woodshed may check for new versions once a day.
  > **Turn on** lets it fetch one public file a day (nothing about your installation is sent); **No thanks** keeps it
  > off and it won't ask again. You can change this in **Settings → Updates**, which also shows the last check. When a
  > newer version is out, a banner links to its release page. Woodshed never updates itself: to upgrade, download the
  > new installer from the releases page and run it, and your settings and logs are kept.
- **CONTRIBUTING.md**, Rules:
  > **No outbound connections, except the update check.** Woodshed never dials out on its own: no telemetry, fonts or
  > CDNs. The one exception is the opt-in update check in `internal/updatecheck` (SPEC §10.2), the only package allowed
  > to reach the internet. Tests enforce both; don't work around them.
- **CHANGELOG.md** (1.1.0): "Opt-in update check: asks once, off unless you turn it on, one GET of the public releases
  file a day, never installs anything." Earlier entries stay as they are.

### 10.3 TLS syslog listener

**(decided, 2026-11-13: off by default; one switch in Settings; TCP 6514 by default, and the port can be changed.)**
A third listener takes syslog over TLS (RFC 5425) for devices that offer it. Everything after the handshake is the
TCP listener: the same framing, limits, parsing, storage and device identity (the source IP). It's off until someone
turns it on, so a 1.0 upgrade changes nothing and creates no key files.

This design follows a source-level study of one widely deployed firewall family's TLS syslog client (2026-11-13),
written up generically here. Its findings shape the defaults:
- They offer TLS 1.2 and 1.3 by default, but on unlicensed or evaluation systems they can offer only TLS 1.0.
- They send no SNI, no ALPN and no client certificate, and never read anything back from the server.
- They trust only a CA that an administrator imported by hand into the device's own trust store, not a public root
  list.
- They insist on EKU serverAuth in a CA-issued server certificate.
- When a name check is configured, they match the certificate's SAN DNS names (CN only when there are none) and
  never its IP SANs. Without one, they check only the chain.
- Messages are LF-framed with no octet count, at most 4 KiB each, over up to 4 parallel connections per host. Order
  across those connections isn't kept.
- They set no keepalives, reconnect every 10 s while the server is unreachable, and drop (don't queue) messages in
  the meantime.
- A failed TLS handshake (a bad certificate, a version mismatch) counts as "log host down". By default that **blocks
  new connections through the firewall** until it's fixed, unless a "permit when the log host is down" option is set.

That last point is why this listener must be conservative: a certificate that changes or expires without warning
doesn't just lose logs, it can stop a site's traffic.

#### 10.3.1 Settings and config
```toml
[listeners.tls]
enabled = false          # the one switch (Settings → Listeners → "Syslog over TLS")
addr = "0.0.0.0"
port = 6514              # any free port; some device docs use 1470 for TCP syslog
allow_legacy_tls = false # also accept TLS 1.0 and 1.1, for older senders (warn banner while on)
cert_file = ""           # your own certificate chain (PEM) and key; both empty means Woodshed's own CA
key_file = ""
```
- Ports below 1024 need the same privileges as 514; 6514 needs none.
- `cert_file` and `key_file` are both empty or both set, and must be absolute paths to readable files, validated
  like `ui.cert_file`. A mismatched pair is a 400 on both keys.
- The listener appears in `/api/status.listeners` as `proto: "tls"`. A bind failure gets the usual `listener_bind`
  banner and Retry.

#### 10.3.2 TLS parameters
- **Versions:** TLS 1.2 and 1.3. `allow_legacy_tls` adds 1.0 and 1.1, and while it's on there's a warn banner
  `tls_legacy`: "TLS 1.0 and 1.1 are allowed for older senders. Turn this off once they're updated."
- **Ciphers:**
  - TLS 1.2: ECDHE with AES-GCM or ChaCha20-Poly1305 only. With an RSA key that's `ECDHE-RSA-AES128-GCM-SHA256`,
    `ECDHE-RSA-AES256-GCM-SHA384` and `ECDHE-RSA-CHACHA20-POLY1305`, and the ECDSA equivalents with a user's ECDSA key.
  - TLS 1.3: the standard suites (Go's fixed set).
  - With `allow_legacy_tls`, 1.0 and 1.1 can't use AEAD suites, so it adds `ECDHE-*-AES128-CBC-SHA` and
    `ECDHE-*-AES256-CBC-SHA`.
  - Never offered: static RSA key exchange, 3DES or RC4.
  - Curves: X25519, P-256 and P-384, plus Go's default hybrid post-quantum group for 1.3.
- **No SNI, ALPN or client certificate is required.** The certificate is the same whatever SNI says, offered ALPN
  values are ignored, and no client certificate is requested.
- **Session resumption:** off (no tickets). The senders studied never resume, and it keeps no ticket key to rotate.
- **Client certificate verification** (an optional `client_ca`) is a "maybe later" for senders that support mutual
  TLS. It isn't in 1.1.

#### 10.3.3 Certificate: Woodshed's own small CA
When TLS is first turned on and no `cert_file` is set, Woodshed creates a CA and a server ("leaf") certificate signed
by it. Devices import the **CA**, so a renewed leaf needs no change on any device.
- **Files** in the data folder, mode 0600 like the UI certificate:
  - `tls-ca.pem` and `tls-ca-key.pem`;
  - `tls-cert.pem` (the leaf, then the CA: the chain that's sent) and `tls-key.pem`.
- **Key type: RSA-2048 for both, signed with SHA-256** (**decided (coordinator, 2026-11-13, owner-delegated)**). The study couldn't confirm which server key
  types older senders accept, while RSA-2048 with SHA-256 is accepted by every TLS 1.0–1.3 client in use, including
  TLS 1.0-only stacks and those limited to 2048-bit keys. ECDSA P-256 would make handshakes cheaper, but a sender that
  can't use it fails closed, and on some firewalls that blocks traffic.
  - The handshake cost of RSA-2048 is about a millisecond per connection, which is nothing even for a reconnect every
    10 s per sender.
  - The trade-off is margin: guidance retires 2048-bit RSA around 2030, inside the CA's lifetime. Replacing the CA is
    an explicit action (below), and a user's own certificate can be any type.
- **The CA:** subject `CN=Woodshed CA <hostname> <yyyy-mm-dd>`; `basicConstraints CA:TRUE, pathlen:0`; KU
  keyCertSign + cRLSign; valid until **2037-12-31T23:59:59Z** (about 11 years from 1.1).
- **The leaf:**
  - `basicConstraints CA:FALSE`;
  - KU digitalSignature + keyEncipherment, and EKU serverAuth;
  - SAN DNS names from the UI certificate's logic (`identityFrom`): the hostname, `<hostname>.local`, the hostname
    in each DNS search domain, `localhost` and every `ui.extra_hostnames` name;
  - IP SANs for every stable address and each `ui.extra_hostnames` IP. Some senders ignore IP SANs, which is why the
    DNS names matter;
  - CN set to the primary DNS name: the first search-domain name if there is one, otherwise the hostname;
  - valid for **10 years** or until the same 2037-12-31T23:59:59Z cap, whichever is earlier.
- **Why such long lives:** these aren't browser certificates.
  - The 398-day limits apply to publicly trusted certificates. A private CA that devices import by hand is under no
    such rule, and the device stacks studied enforce no maximum.
  - An expired certificate stops TLS logging on every device at once, and on some firewalls it blocks traffic. A
    renewal often needs someone other than the Woodshed admin to touch every device.
  - The risk of a long life is a stolen key. Devices rarely check revocation, so a stolen leaf key stays trusted until
    the CA is replaced. The leaf key and the CA key sit in the same 0600 folder, so whoever can take one can take
    both; the answer to a compromise is **Replace CA**, whatever the lifetimes.
  - **Why the 2037 cap** (**decided (coordinator, 2026-11-13, owner-delegated)**): some old 32-bit device stacks keep
    time as a signed 32-bit count of seconds and can't represent anything after 2038-01-19. Such a stack may refuse a
    later date at import, or wrap it into the past and treat the certificate as already expired. An expiry before 2038
    parses everywhere. Its cost is a renewal inside the working life of old gear, but that renewal is planned: the
    60-day banner announces it, and renewing the leaf needs nothing on the devices. Both certificates therefore end
    on 2037-12-31T23:59:59Z, which makes the CA about 11 years.
- **Never changed silently.** Unlike the UI certificate, which regenerates itself, the TLS certificate changes only
  on an explicit action.
  - **Renew certificate** (Settings, or `woodshed tls renew`) makes a new leaf with a new key and the current host
    names, signed by the same CA. Devices keep working, and new connections get the new leaf; open connections keep
    theirs.
  - **Replace CA** (Settings, behind a confirmation that says every device must import the new CA, or
    `woodshed tls renew --new-ca`) makes a new CA and leaf.
  - Every renewal is recorded in the events log and the export's `woodshed-events.log`, with both fingerprints.
- **Warn banners, with the action `tls_renew` that opens Settings; neither ever renews on its own:**
  - `tls_cert_expiring`: from 60 days before the leaf or the CA expires: "The TLS syslog certificate expires on
    2036-11-13. Renew it, then nothing changes on your devices." For the CA, it says devices must import the new one.
  - `tls_cert_names`: when the host's DNS names (hostname, search domains, `ui.extra_hostnames`) include one the leaf
    doesn't cover, for example after a rename: "The TLS certificate doesn't name edge-logs.example.net. Devices that
    check the server name will refuse it. Renew it to add the name." Address changes don't raise it, because the
    senders that check names check DNS names.
- **A user's own certificate** (`cert_file`, `key_file`): sent as given (the leaf and any chain in the file), and
  re-read on Retry and on every Settings save, so a replaced file takes effect without a restart. Woodshed shows its fingerprint,
  subject, issuer and expiry, raises the same expiry banner, and offers no CA download (`404`: get the CA from
  whoever issued the certificate). It's checked when loaded: it must have EKU serverAuth (or no EKU) and a key the
  allowed suites can use, or Settings gets a 400 saying what's missing.

#### 10.3.4 Downloads, fingerprints and status
- **The CA certificate** is a download in Settings, and `GET /api/tls/ca.pem` and `GET /api/tls/ca.der`. The guide says
  which to use: PEM for devices where you paste the certificate as text into a terminal or a web form, DER (`.cer`)
  for devices that want an uploaded binary file, and for the Windows certificate store.
- **Fingerprints:** the SHA-256 of the CA and of the leaf, plus the CA's SHA-1 for devices that show only that. They're
  shown in Settings, in `GET /api/tls`, and in `woodshed status`. The guide says to compare the device's view of the
  CA with Settings before trusting it.
- **Per-sender TLS stats:** so "my device can't connect" can be diagnosed, the listener counts per sender address:
  - TLS connections, and the version and cipher of the last one;
  - failed handshakes in the last 24 h (hourly buckets, like refusals), with the reason and time of the last one.

  Reasons:

  | Reason | Meaning |
  |---|---|
  | `cert_rejected` | the sender sent an alert about the certificate (`unknown_ca`, `bad_certificate`, `certificate_unknown`, `certificate_expired`; the alert is shown). Usually the CA isn't imported, or a name check failed |
  | `version` | no TLS version in common (often a TLS 1.0-only sender while `allow_legacy_tls` is off) |
  | `cipher` | no cipher suite in common |
  | `not_tls` | the first bytes weren't a TLS handshake, usually plain syslog sent to the TLS port |
  | `timeout` | no complete handshake within 10 s |
  | `closed` | the sender closed the connection during the handshake, without an alert |
  | `other` | anything else (the error text is kept) |

  Devices with logs carry this on `/api/devices` as `tls`. Senders that failed before they ever got a message through
  are listed by `GET /api/tls/failures` (at most 10,000 addresses tracked, least recently seen evicted, like
  `/api/rejected`). A sender that's only failing appears there and nowhere else, so Settings → TLS links to it.

#### 10.3.5 Framing and connections
- **Framing** is exactly the TCP listener's: octet counting or LF per message, auto-detected; CRLF and NUL tolerated,
  and a missing final LF at close kept as a message; `max_message_bytes` and `tcp_frame_timeout` as for TCP.
- **Connections** share the TCP listener's pool and limits: `MaxConns`, `tcp_max_conns_per_source` (default 32; at
  least 4 parallel connections per sender are needed) and `tcp_max_conns_per_prefix` count TCP and TLS together, and
  eviction works the same way. Order across a sender's connections isn't assumed.
- **Handshakes don't take slots.** A connection joins the pool only once its handshake completes. Handshakes in
  progress have their own limits: at most 256 at once and 8 per sender address. Past those, a new connection is closed
  at once and counted (reason `busy`). Each handshake has 10 s, then it's closed (`timeout`). A sender that
  reconnects every 10 s while misconfigured therefore costs one short handshake each time and can't crowd out working
  senders.
- After a TLS listener closes a connection, nothing is written back: these senders never read. A close without
  `close_notify`, or a plain FIN or RST, is normal.
- The idle timeout is the TCP listener's: 24 h from 1.0.1 (§9).

#### 10.3.6 Firewalls and installers
The service account can't change the host firewall, and shouldn't be able to. So:
- **Windows:**
  - The MSI ships a rule "Woodshed syslog (TLS 6514)", TCP 6514 for `woodshed.exe` on all profiles, present but
    **disabled**. `woodshed install` creates the same rule, disabled. Upgrades keep its state.
  - `woodshed tls on` (as an administrator) turns the listener on in the config and enables the rule, setting its port
    to `listeners.tls.port`. `woodshed tls off` turns both off.
  - The UI switch turns the listener on without touching the rule. While the rule exists but is disabled, or its port
    differs, there's a warn banner `tls_firewall` with the exact command to run in an administrator prompt
    (`woodshed tls on`). The rule's state is read through the OS without administrator rights (an acceptance item, §10.4). The check
    lives outside `internal/api`, which may not run programs.
  - Portable mode has no rule. Windows' first-run "allow" dialog covers the program on every port, so 6514 works if
    Allow was chosen.
- **macOS:** the application firewall allows or blocks the program, not ports. If Woodshed already receives on 514,
  6514 works too. `tls on` and `off` change only the listener. There's no banner.
- **Linux:** the deb and rpm add nothing, as for 514.
  - `woodshed tls on` (as root) also opens the port where it finds an active, known firewall: `ufw allow 6514/tcp`
    when ufw is active, or `firewall-cmd --permanent --add-port=6514/tcp` and a reload when firewalld is running (the
    default zone). It records what it added in `<data>/tls-firewall.json`.
  - `woodshed tls off` removes only what it added. With neither firewall active (plain nftables or iptables rules,
    say), it changes nothing and prints the commands to run.
  - The service user can't read ufw or firewalld state reliably, so Linux gets no banner. Settings → TLS shows the
    commands under "If devices can't connect".
- `woodshed tls on|off|renew [--new-ca]|status` follow `lan-mode`: they need administrator rights, edit the config,
  and ask a running service to apply it (or say it isn't running). `status` prints the listener, the fingerprints,
  the expiry and the last failures.

#### 10.3.7 API (docs/api.md → "1.1 (planned)")
`GET /api/tls` (state, fingerprints, expiry, the firewall hint), `POST /api/tls/renew` (`{"new_ca": false}`),
`GET /api/tls/ca.pem` and `ca.der`, `GET /api/tls/failures`, a `tls` object on each device in `/api/devices`,
`listeners.tls` in `/api/settings`, the listener in `/api/status`, and the banners `tls_legacy`,
`tls_cert_expiring`, `tls_cert_names` and `tls_firewall`.

#### 10.3.8 Tests
- Handshakes with Go clients over every allowed version and suite. Refused: 1.0/1.1 with the flag off, static-RSA
  suites, a client that offers only CBC with 1.2. With the flag on, 1.0 and 1.1 with the CBC suites.
- A client with no SNI, with a wrong SNI and with ALPN connects. A client that verifies the chain against the
  downloaded CA and checks a DNS name from the SANs connects, and one checking a name that isn't there fails with
  `cert_rejected` counted.
- Certificate profile: CA and leaf fields exactly as in §10.3.3. With a fake clock in 2030, the leaf ends at the
  2037 cap, not 10 years on. A Go `x509.Verify` with the CA as the only root, for
  each SAN name. The chain sent is leaf + CA.
- Never silent: a fake clock past the 60-day mark and a changed hostname raise the banners and leave the files
  byte-identical. Renew keeps `tls-ca.pem` byte-identical.
- Framing parity: the TCP listener's framing table run over TLS.
- Handshake limits: 300 stalled handshakes from many addresses leave established senders working; one address
  can't hold more than 8. A stalled handshake is closed at 10 s and doesn't count against `MaxConns`.
- Failure reasons: plain syslog to the TLS port (`not_tls`), an unknown-CA alert (`cert_rejected`), a version
  mismatch (`version`).
- The install smoke tests: the MSI's rule exists and is disabled after install and after an upgrade, `woodshed tls on`
  enables it and a TLS message arrives from another process, and `off` disables it. On Linux with ufw active, `tls on`
  then `off` leaves the rules as they were.
- The default config: the existing no-dial tests still pass, and no TLS key files exist until TLS is turned on.

#### 10.3.9 Doc wording
Apply this with the implementation, as for §10.2.
- **docs/user-guide.md**, a new section after "Point a device at it":
  > **Sending syslog over TLS.** Use this when a device should encrypt its logs on the way. It's off until you turn
  > it on.
  > 1. In **Settings → Listeners**, turn on **Syslog over TLS** (TCP port 6514). On Windows, also run
  >    `woodshed tls on` once in an administrator prompt, so the firewall lets devices in; Settings shows the command
  >    if it's needed.
  > 2. Download Woodshed's **CA certificate** from the same section: **PEM** if the device wants the certificate as
  >    text to paste, **DER (.cer)** if it wants a file to upload.
  > 3. On the device, import it as a **trusted CA certificate** (often called a trust anchor or a CA
  >    certificate). Compare the fingerprint the device shows with the one in Settings.
  > 4. Point the device's syslog at this computer, **TCP port 6514 with TLS** (the setting may be called secure,
  >    encrypted or TLS syslog).
  > 5. If the device can check the server's name, give it one of the DNS names Settings lists for the certificate,
  >    not an IP address: many devices check names only. Then check on the Dashboard that messages arrive.
  >
  > **Before you turn on TCP or TLS logging on a firewall:** some firewalls block traffic through them when a TCP or
  > TLS syslog server can't be reached, or its certificate fails, unless a "permit traffic when the log host is down"
  > option is set. Check that option first.
  >
  > If a device can't connect, **Settings → TLS** shows each sender's failed handshakes and why: the CA isn't trusted,
  > there's no TLS version in common, or plain syslog was sent to the TLS port.
- **docs/install.md**, a section "TLS syslog: firewall and certificate" with §10.3.6 per OS, `woodshed tls
  on|off|renew|status`, the files in the data folder, renewing (the CA stays, so devices need nothing) and replacing
  the CA (every device must import the new one).
- **FAQ.md**, "Why port 514, and why UDP?" gains: "For encrypted syslog, turn on **Syslog over TLS** (TCP 6514); see
  the user guide."
- **README.md**: the "What it isn't" paragraph drops TLS syslog from "planned after 1.0", and Features gains "Optional
  syslog over TLS (TCP 6514) with its own CA, so devices import one certificate that renewals don't change."
- Device-specific commands stay out of the repo.

### 10.4 Decisions for 1.1

**Owner** **(decided, 2026-11-13)**: the update check is opt-in, asks once, makes a plain HTTPS GET of the public
releases list and never installs anything (§10.2); the TLS listener is off by default, has one Settings switch, and
listens on TCP 6514 by default, with the port configurable (§10.3); the roadmap order in §9.

Everything below is **decided (coordinator, 2026-11-13, owner-delegated)**.

Events v2 and the update check:
1. Field counts are exact over a stated span, newest first, never sampled (§10.1.1).
2. One summary endpoint feeds both the field panel and the histogram; `fields=none` gives the histogram alone
   (§10.1.2).
3. Excluding a value is Alt-click or the "−" button, not a second click (§10.1.1).
4. "(none)" rows can't be clicked (§10.1.1).
5. Boolean terms need `syntax=terms`; they never switch on because operators appear, and only upper-case operators
   count (§10.1.3).
6. `syntax=terms` with `regex=true` is a 400 (§10.1.3).
7. Saved searches live in `index.db`, not the TOML (§10.1.4).
8. A saved search keeps the IDs of missing devices and is never edited automatically (§10.1.4).
9. The update check reads the existing Pages file `releases.json`, not the GitHub API (§10.2).
10. The User-Agent is `Woodshed`, with no version (§10.2).
11. Redirects aren't followed (§10.2).
12. Portable mode asks too (§10.2).
13. Development builds never check or ask (§10.2).
14. No `If-None-Match` or `If-Modified-Since` (§10.2).
15. The public docs change only when the checker ships; the wording waits in §10.2 (§10.2).
16. At most 2 summary scans at once, 2 s per summary request by default, running counts kept 10 min for at most 8
    summaries (§10.1.5).

TLS:
1. RSA-2048 with SHA-256 for the CA and the leaf (§10.3.3).
2. Long lives: the CA until the 2037 cap, the leaf 10 years or the cap, whichever is earlier (§10.3.3; the cap is
   TLS answer 1 below).
3. TCP and TLS share one connection pool and its per-source limits (§10.3.5).
4. Handshakes stay out of the connection slots, with their own limits of 256 at once and 8 per address (§10.3.5).
5. Session tickets are off (§10.3.2).
6. `tls_cert_names` warns about DNS names only, not addresses (§10.3.3).
7. With a user's own certificate, the CA download is a 404 (§10.3.4).
8. `WOODSHED_UPDATE_CHECK` applies on upgrades too, and an invalid value is ignored with a warning (§10.2).
9. The 1.0.1 change: at its per-source limit, a sender's newcomer evicts that sender's own longest-idle connection
   (§9). It's being built in 1.0.1.

Answers to the 1.1 questions:
1. No regex terms inside `syntax=terms` in 1.1; mixing the two stays a 400.
2. The live tail gets neither `syntax=terms` nor the new exclude filters in 1.1.
3. No filter for "(none)" (lines with no hostname or program/tag) yet; the panel row stays unclickable.
4. Silent fleet installs get a switch for the update check: the MSI property and the pkg/deb/rpm environment
   variable `WOODSHED_UPDATE_CHECK=on|off|ask` (§10.2).
5. No redirects: the update check follows none, and the site keeps the URL answering.
6. The field panel's 60 s auto-continue limit stays.
7. The other 1.x roadmap items stay unordered.
8. The 1.3 SNMP poller will be a second package that connects out on its own; §10.2's allowlist test and its "only
   `internal/updatecheck`" wording get amended then.

Answers to the TLS questions:
1. Both certificates end at 2037-12-31T23:59:59Z, for 32-bit device stacks (§10.3.3 says why).
2. No static-RSA suites in 1.1. Add them later only if a real sender needs them.
3. Firewall rule details: an acceptance item, below.
4. On Linux the Settings text is enough; there's no firewall banner.
5. No RSA-3072 or ECDSA CA option in 1.1.
6. Client-certificate verification (`client_ca`) comes later, if asked for.
7. The installers' `WOODSHED_UPDATE_CHECK` pass-through: an acceptance item, below.

Update check, **decided (build, 2026-11-13)** (the smallest choice where §10.2 or docs/api.md left it open):
1. A release build, which may check and ask: a SemVer version that isn't 0.0.0, with no build metadata, and a
   prerelease part, if any, like a real tag's (`rc1`, `rc.1`, `beta2`, `alpha`). A `git describe` version
   (`1.0.0-5-gabc1234-dirty`), a CI snapshot (`1.0.1-snapshot.abc1234`) and `-dev`, `-ci` or `-dirty` builds never
   check or ask.
2. `main.releasesURL` is now `https://github.com/gmccollu/woodshed/releases` (it was empty "until the project has a
   public home"), so release builds have a `releases_url`; the About page links to it too.
3. `html_url`: exactly `releases_url + "/tag/" + tag_name` gives that link; empty or anything else under
   `releases_url` gives `releases_url`; anything outside `releases_url` (another host, or a look-alike such as
   `…/releases.evil.example/…`) is the "off-site `html_url`" of the tests: a `bad_json` error and no banner.
4. A considered entry whose `tag_name` isn't a version, or a `stable` entry with a prerelease version (a
   `prerelease` one without), is `bad_json`. A response whose `Content-Type` isn't `application/json` is `bad_json`.
5. Turning it on checks 5–30 s later. **Check now** while a check is running counts that one.
6. After a restart within 24 h of a check, the next is exactly 24 h after it, even when that check failed (a
   pending retry isn't kept across a restart).
7. `latest` is `null` whenever `check` isn't `"on"` (still asking, too), not only after turning it off.
8. `GET /api/settings` always has `updates`; a `PUT` without it keeps the setting (1.0 clients and scripts).
9. `POST /api/updates/check` answers `202` with no body, and `409` in a build that can't check as well.
   `POST /api/updates/dismiss` with a `version` that isn't one is a `400`.
10. The request is HTTP/1.1 and its connection is closed after each check, without Go's `DisableKeepAlives`,
    which would add a fifth header (`Connection: close`).
11. Error messages are Woodshed's own; an HTTP status shows its number and Go's text, never the server's.
12. `woodshed update-check` accepts any case and surrounding spaces; an invalid value exits 2 and changes nothing.
    With no config yet it writes one with the defaults a server would use there, owned like its folder (the folder
    must exist). `--no-restart` is for the installers, which run it before they start or restart the service.
13. The update banners are in the run-state file too, so `woodshed status` lists them; it also has an `Updates`
    line (the setting and the last check).
14. The MSI runs it through WixQuietExec (its output goes to the install log) with `Return="ignore"`, before
    `StartServices`. The pkg's and the deb/rpm scripts print the warning and carry on.

**Acceptance items for the 1.1 build** (the install smoke tests confirm each, or the design changes before release):
- The WiX firewall extension in use can ship the TLS 6514 rule present but disabled, and an upgrade keeps its state.
- The virtual service account (`NT SERVICE\Woodshed`) can read that rule's state, for the `tls_firewall` banner.
- `WOODSHED_UPDATE_CHECK` reaches the install scripts: the MSI property; macOS `installer` to the pkg's scripts;
  `sudo`, apt/dpkg and dnf/rpm to the deb's and rpm's. Where it doesn't, that installer documents pre-seeding the
  TOML instead.

## 11. Open questions
1. Where the repo lives. The answer given was the internal GHE, which conflicts with "open source on
   github.com" in §2 (see the reply of 2026-11-10).
2. Retention fairness: decided 2026-11-11 (fair shares, §3.4). An optional per-device cap or minimum
   days is still open.
3. macOS launchd daemon as `_woodshed` and the application-firewall prompt: test in M0 (see §5).
