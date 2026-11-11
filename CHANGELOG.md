# Changelog

All notable changes to Woodpile. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versions follow [Semantic Versioning](https://semver.org/). Release notes on GitHub are taken from this file
(`scripts/release-notes.sh`), so set a section's date before tagging it.

## [1.0.0] - unreleased

The first release: a syslog server you can install on Windows, macOS or Linux in a minute, that keeps every message
as plain files within a disk budget and has a web UI for watching, searching and exporting logs.

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
- A SQLite index of files and time ranges, rebuildable from the files with `woodpile reindex`.

### Web UI
- Welcome page: this machine's addresses, the listener status and a test command; it notices the first message.
- Dashboard: message rate with a sparkline, devices online and quiet, disk use against the cap, dropped messages,
  device tiles with a last-heard dot, group tabs and a live tail with server-side filters.
- Devices: rename, group or hide senders; first and last seen and totals.
- Search: text or regular expression, case on or off, devices or groups, time range and severities; results stream
  in, with ±50 lines of context around a hit.
- Export: a zip of the raw lines for chosen devices and a time range (or exactly what a search showed), with a
  manifest (line counts, SHA-256, gaps when Woodpile wasn't running), Woodpile's own events and a README.
- Settings that apply without a restart: listeners, allowlist, retention, file size, quiet thresholds, LAN mode.
- Light and dark modes.

### Security
- The UI listens on `127.0.0.1:8514` only by default.
- LAN mode: HTTPS with a self-signed certificate (or your own), a password (Argon2id), secure session cookies,
  CSRF tokens and a login lockout. `woodpile reset-password` recovers a forgotten password.
- No outbound connections, no telemetry and no update checks; a test asserts that no code path dials out.
- Runs with the least privilege each OS allows: a `woodpile` system user with only `CAP_NET_BIND_SERVICE` (Linux),
  the virtual account `NT SERVICE\Woodpile` (Windows), a hidden `_woodpile` user (macOS).

### Installing
- Installers: MSI for Windows (x64 and ARM64), a universal `.pkg` for macOS, `.deb` and `.rpm` for Linux (amd64 and
  arm64). Each sets up the service, starts it and keeps the logs on upgrade and uninstall unless you ask to delete
  them.
- Portable archives (`.zip`, `.tar.gz`) for every OS: unpack, double-click `woodpile`, and the data goes in
  `woodpile-data` next to it.
- `woodpile install` / `uninstall` set up the same service without a package.
- Command line: `run`, `install`, `uninstall`, `status`, `reindex`, `reset-password`, `send` (numbered test
  messages), `bench` (sustained throughput) and `version`.

### Known limitations
- The installers and binaries aren't signed yet, so Windows SmartScreen and macOS Gatekeeper ask once before running
  them (see the [FAQ](https://github.com/gmccollu/woodpile/blob/main/FAQ.md)).
- On Windows and macOS the OS doesn't expose kernel UDP drops; the UI says so instead of showing 0.
- No TLS syslog, alerts or forwarding yet (planned after 1.0).
