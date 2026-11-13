# Changelog

All notable changes to Woodshed. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versions follow [Semantic Versioning](https://semver.org/). Release notes on GitHub are taken from this file
(`scripts/release-notes.sh`), so set a section's date before tagging it.

## [1.0.0] - unreleased

The first release: a syslog server you can install on Windows, macOS or Linux in a minute, that keeps every message
as plain files within a disk budget and has a web UI for watching, searching and exporting logs.

### Renamed from Woodpile
- The release candidates were called Woodpile; from 1.0.0 it's Woodshed. Everything carries the new name: the
  program (`woodshed`), the services (`woodshed.service`, `com.woodshed.daemon`, `Woodshed` running as
  `NT SERVICE\Woodshed`), the accounts, folders, packages and download names, and the UI's cookies and header (so
  a browser logs in again once).
- Installing Woodshed over a Woodpile release candidate replaces it: the MSI upgrades over the Woodpile MSI (whose
  uninstall removes the `Woodpile` service), the deb and rpm remove the `woodpile` package (Conflicts/Replaces,
  Obsoletes), and the pkg stops the `com.woodpile.daemon` daemon and removes its plist, program and app.
- The deb now says "stopped and disabled the old Woodpile service" when it replaces a running Woodpile, as the rpm
  already did.
- The old data isn't moved or deleted. Woodshed starts with its own data folder and default settings; the Woodpile
  one (`/var/lib/woodpile` and `/etc/woodpile`, `/Library/Application Support/Woodpile`, `C:\ProgramData\Woodpile`)
  stays where it is. The installer and `woodshed status` say where, and you can delete it when you no longer need it
  ([docs/install.md](docs/install.md#moving-from-woodpile)).
- `woodshed install` refuses while a Woodpile service is still installed, and prints the command that removes it.

### Known issues (release candidates; fixed before 1.0.0)
- Under a sustained flood spread across many spoofed source addresses, size-based retention can delete the
  busiest real device's history, including its newest day. Use the sender allowlist on untrusted networks.
- A flood that cycles through more than 10,000 previously seen source addresses can slow ingest to about 12,000
  messages a second, and the kernel may then drop UDP messages for every device.
- Windows, `woodshed run` (not the installers): a data folder that another local user created beforehand is accepted
  if its permissions name individual users. A race exists while the installer replaces a folder another user created.
- Hidden devices with no name or group can reappear unhidden after 24 hours without logs.

### Receiving
- Listens on UDP and TCP port 514 straight away, on IPv4 and IPv6, with no setup.
- TCP framing (octet counting or newline) is detected per message. Messages up to 64 KiB; longer ones are
  truncated and counted, never dropped.
- Lenient parsing of RFC 5424, RFC 3164 and the usual non-compliant variants (missing PRI, odd timestamps, BOMs).
  The raw message is always stored exactly as received.
- Optional sender allowlist (IPs or CIDRs); refused senders are counted and shown.
- If port 514 is taken, the UI still starts and shows which port, the error and, where the OS tells, the program
  holding it, with a **Retry** button.
- Asks for a 32 MiB UDP receive buffer; the Linux packages raise `net.core.rmem_max` so it gets it.

### Storage
- Plain text files, one folder per device and one file per day (UTC): `logs/<device>/<day>.log`, gzipped once
  closed. Each line is the received time, the sender and the raw message.
- Files close at 100 MiB or midnight UTC, whichever comes first.
- Retention by age and total size (30 days / 20 GiB by default), plus a low-disk guard that keeps 5% of the volume
  free (at least 1 GiB, at most 10 GiB) and pauses writing, counting what it drops, rather than fill the disk. Every deletion is recorded and shown in Settings → Storage history.
- A SQLite index of files and time ranges, rebuildable from the files with `woodshed reindex`.

### Web UI
- Welcome page: this machine's addresses, the listener status and a test command; it notices the first message.
- Dashboard: message rate with a sparkline, devices online and quiet, disk use against the cap, dropped messages,
  device tiles with a last-heard dot, group tabs and a live tail with server-side filters.
- Devices: rename, group or hide senders; first and last seen and totals.
- Events (called Search in the release candidates; old `#/search` links still work): browse or filter every stored
  message, compressed days included. Filters: text or regular expression, case on or off, program/tag (with `*`
  for a prefix, such as `%FW-6-*`), header hostname, exclude, devices or groups, time range and severities. It opens
  on the last hour with no text, so it browses everything. Host and Program/tag columns; click a value to filter by
  it. Results stream in, with ±50 lines of context around a hit.
- The live tail has **Open in Events**: the same devices, severities and text over the last hour.
- Export: a zip of the raw lines for chosen devices and a time range (or exactly what Events showed, with the same
  filters), with a manifest (line counts, SHA-256, the filters, gaps when Woodshed wasn't running), Woodshed's own
  events and a README that states the filters.
- Settings that apply without a restart: listeners, allowlist, retention, file size, quiet thresholds, LAN mode.
- Light and dark modes.

### Security
- The UI listens on `127.0.0.1:8514` only by default.
- LAN mode: HTTPS with a self-signed certificate (or your own) and a password (Argon2id). Sessions need both a
  cookie and a per-session secret header, so a stolen cookie alone is useless. Failed logins back off per address
  and per network, and a browser that logged in before stays remembered. `woodshed reset-password` recovers a
  forgotten password.
- Setting a password also turns on "require login on this machine", so other local users can't change settings;
  it can be turned off.
- No outbound connections, no telemetry and no update checks; a test asserts that no code path dials out.
- Runs with the least privilege each OS allows: a `woodshed` system user with only `CAP_NET_BIND_SERVICE` (Linux),
  the virtual account `NT SERVICE\Woodshed` (Windows), a hidden `_woodshed` user (macOS).

### Installing
- Installers: MSI for Windows (x64 and ARM64), a universal `.pkg` for macOS, `.deb` and `.rpm` for Linux (amd64 and
  arm64). Each sets up the service, starts it and keeps the logs on upgrade and uninstall unless you ask to delete
  them.
- Portable archives (`.zip`, `.tar.gz`) for every OS: unpack, double-click `woodshed`, and the data goes in
  `woodshed-data` next to it.
- `woodshed install` / `uninstall` set up the same service without a package.
- `woodshed status` run without root (or administrator rights on Windows) says plainly that it can't read the
  config and gives the command that can (`sudo woodshed status`), and still shows the service's state and the UI.
- Command line: `run`, `install`, `uninstall`, `status`, `reindex`, `reset-password`, `send` (numbered test
  messages), `bench` (sustained throughput) and `version`.

### Known limitations
- The installers and binaries aren't signed yet, so Windows SmartScreen and macOS Gatekeeper ask once before running
  them (see the [FAQ](https://github.com/gmccollu/woodshed/blob/main/FAQ.md)).
- On Windows and macOS the OS doesn't expose kernel UDP drops; the UI says so instead of showing 0.
- No TLS syslog, alerts or forwarding yet (planned after 1.0).
