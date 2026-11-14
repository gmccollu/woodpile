# Woodshed HTTP API (v1 contract)

The UI talks only to these routes. All responses are JSON unless noted. Times are RFC 3339 UTC strings.
Byte counts are integers. The frontend builds against this contract with mock data; the backend implements it.
Change this file first if the contract changes.

## Status and stats

`GET /api/status`
```json
{
  "version": "0.1.0", "build": "abc1234", "os": "darwin/arm64",
  "data_dir": "/Library/Application Support/Woodshed",
  "started_at": "2026-11-10T20:00:00Z",
  "first_message_at": null,                   // null until the first message is received (drives the Welcome page)
  "udp_rcvbuf_bytes": 33554432,               // what the OS actually granted
  "udp_rcvbuf_requested_bytes": 33554432,
  "releases_url": "",                         // build-time constant; empty until the project has a public home
  "kernel_drops_visible": false,
  "listeners": [
    {"proto": "udp", "addr": "0.0.0.0", "port": 514, "ok": true,  "error": ""},
    {"proto": "tcp", "addr": "0.0.0.0", "port": 514, "ok": false, "error": "bind: address already in use", "holder": "syslogd (pid 312)"}
  ],
  "host_ips": ["10.1.0.50", "fd00::50"],
  "ui_cert_fingerprint": "",                  // LAN mode: SHA-256 of the UI's certificate, "AB:CD:…" (64 hex pairs); "" on localhost
  "banners": [{"kind": "listener_bind", "level": "error", "text": "TCP 514 is in use by syslogd (pid 312).",
               "action": "retry_listeners"}]   // action: "" | "retry_listeners"; kinds: listener_bind, writes_paused, low_disk, ui_port_busy, udp_buffer_small
                                               // from 1.1 (planned, see "1.1 (planned)"): kinds update_prompt and update_available, actions
                                               // "update_choice" and "update_dismiss", and an optional "url" on any banner
}
```
A listener that failed to bind may also carry, when known, `holder_kind` (`"woodshed_service"`: the installed Woodshed
service; `"woodshed"`: another running copy of Woodshed; omitted otherwise), `holder_pid` (integer) and `holder_ui_url`
(that Woodshed's UI address). Another user's process (the service runs as its own account) is invisible to `lsof` and
`/proc` for an ordinary user, so when the holder can't be seen and an installed Woodshed service is running (or the
default data folder's run-state file names another live Woodshed), that one is reported as the likely holder.
Banner kind `ui_port_busy` (warn): another Woodshed (usually the installed service) is running but can't open its UI
because this copy holds the UI port.

`POST /api/listeners/retry` → `204`, then re-read `/api/status`.

`GET /api/stats`
```json
{
  "msgs_per_sec": 1284.2,
  "sparkline": [1100, 1180, 1250],            // msgs/sec averaged per minute, oldest first, last 60 minutes
  "devices": {"total": 14, "online": 12, "quiet": 1, "offline": 1},
  "disk": {"used_bytes": 3435973836, "cap_bytes": 21474836480, "free_bytes": 400000000000, "days_kept": 30,
           "bytes_per_day": 2361393152, "compression_ratio": 9.2, "estimated_days": 9.1, "limited_by": "cap"},
  "dropped_24h": 0,                           // drops Woodshed itself saw (disk guard, write errors)
  "kernel_dropped_24h": null,                 // integer on Linux, null where the OS doesn't expose it
  "truncated_24h": 0,
  "rejected_24h": 0                           // refused messages and TCP connections (allowlist, per-source limit)
}
```
Banner `udp_buffer_small` (warn, Linux): the OS granted a UDP receive buffer below 8 MiB (`udp_rcvbuf_bytes` <
`udp_rcvbuf_requested_bytes`). Text names both sizes, says bursts may be dropped before Woodshed sees them, and gives
the fix (`net.core.rmem_max` on the host for containers; the package's sysctl file otherwise). Logged at WARN at start.

`days_kept` is the number of distinct UTC days that still have data on disk (today counts).
`bytes_per_day` is the on-disk (compressed-equivalent) bytes written over the last 24 h: raw bytes divided by
`compression_ratio`, the ratio measured on files this server has closed and gzipped (a default of 8 until it has
closed a file; `null` until it has 1 h of data). `estimated_days` = `cap_bytes` / `bytes_per_day`, or `null`.
`limited_by` is `"cap"` when `estimated_days` < `retention_days` (the size cap, not the age limit, decides how long
logs are kept), `"age"` otherwise, or `null` when unknown. While it's `"cap"`, `/api/status` carries a warn banner
of kind `retention_cap` ("At the current rate the 20 GiB cap keeps about 9 days, less than the 30-day setting").

`GET /api/storage/estimate?retention_bytes=N` → `{"estimated_days": 4.5, "bytes_per_day": …, "compression_ratio": …}`:
the same estimate for a cap the user is typing in Settings, before saving.
`devices` counts exclude hidden devices.

`GET /api/rejected` → `[{"ip": "198.51.100.7", "count_24h": 120, "last_seen": "...", "reason": "allowlist"}]` (refused
senders). `reason` is the latest refusal's reason: `allowlist`, `blocked` (on the blocklist) or `per_source` (over the TCP
connections-per-source limit).
Sorted by `count_24h` descending, at most 200 entries. The server tracks at most 10,000 refused IPs and evicts the
least recently seen.
Banner kind `device_limit` (warn): new senders are being refused because the active-device cap
(`listeners.max_devices`, default 10,000 devices that sent in the last 10 minutes) or the new-device rate
(`listeners.new_device_burst` 5,000, then `listeners.new_devices_per_minute` 200) was reached; their messages count
as dropped. Senders that already have logs on disk are exempt from the rate. All three are editable in Settings.
`banners` in `/api/status` are sent in priority order: errors first, then warnings.

## Devices

`GET /api/devices?limit=200&offset=0&sort=rate|name|last_heard|first_seen&q=text&group=HQ&ungrouped=true&hidden=include|only|exclude&blocked=include|only|exclude`
→ `{"devices": [...], "total": 1234}`. Default `limit` 200, max 1,000; default `sort=rate`. Directions are fixed:
`rate` busiest first, `name` A–Z (devices without a name sort by IP), `last_heard` and `first_seen` newest first. `q` matches
id, IP or name (case-insensitive substring); `hidden` and `blocked` default to `include`. Each device:
```json
[{"id": "10.1.0.1", "ip": "10.1.0.1", "name": "edge-fw-01", "group": "HQ", "hidden": false,
  "first_seen": "...", "last_heard": "...", "status": "online", "blocked": false,   // status: online | quiet | offline | blocked
  "msgs_per_min": 36720, "total_msgs": 9123456, "bytes_on_disk": 123456789}]
```

Hidden devices are included, with `"hidden": true`.

`GET /api/groups` → `[{"name": "HQ", "devices": 12}]`, sorted by name, with the number of non-hidden devices in each
(ungrouped devices aren't listed).

`PATCH /api/devices/{id}` body `{"name": "...", "group": "...", "hidden": true, "blocked": true}` (any subset) → the
updated device. `blocked: true` adds the device's exact address (`/32` or `/128`) to `listeners.blocklist`;
`blocked: false` removes that exact entry (a broader CIDR that also covers it stays, and the device stays blocked:
the response says so with `"blocked_by": "198.51.100.0/24"`).

**Blocklist** (`listeners.blocklist`, IPs or CIDRs, editable in Settings): messages and TCP connections from a
matching sender are refused at the listener, before parsing or storage, and counted as refusals with reason
`blocked`. The blocklist is checked before the allowlist and wins over it. A blocked device's existing logs, search
and export are unaffected; its `status` is `blocked`, and it isn't counted as online, quiet or offline in
`/api/stats`. Validation errors use `listeners.blocklist[i]` like the allowlist.

## Live tail

`GET /api/live?devices=10.1.0.1,10.1.0.2&group=HQ&ungrouped=true&severities=3,4,unknown&q=text` → `text/event-stream`.
- `event: message` `data: {"received": "...", "device_id": "10.1.0.1", "severity": 6, "raw": "<134>..."}`
- `event: sampled` `data: {"rate": 1284.2, "shown": 50}`: sent once a second while the server is sampling.
- `group` and `ungrouped=true` are alternatives; `ungrouped=true` matches devices with no group.
- Hidden devices are left out of the live tail unless they're named in `devices`, the same rule as search.
- `severity` is an integer 0–7, or `null` when the message had no parseable PRI.
- `min_severity` is the numeric syslog severity ceiling (0 emerg … 7 debug); messages with an unknown severity
  are always included.
- On connect, the server first sends up to the last 200 matching messages from the ring buffer.
- A heartbeat comment (`: ping`) is sent every 15 s, so a client can treat 45 s of silence as a dead stream.

## Severity filters (live tail and search)

Both `/api/live` and `/api/search` take either:
- `severities=0,3,7,unknown`: any set of syslog severities (0 emerg … 7 debug) plus `unknown` (no parseable PRI).
  Only those are returned. An empty or missing value means all; `severities=none` means nothing (the live tail
  sends no messages, search returns no results). `none` can't be combined with other values.
- `min_severity=N`: the older ceiling form (0…N, plus unknown). If both are given, `severities` wins.

An invalid value (not 0–7 or `unknown`) is a `400 {"error": ...}`.

## Search

`GET /api/search?q=text&regex=false&case=false&devices=a,b&group=HQ&ungrouped=true&from=...&to=...&severities=...&app=sshd,%25FW-6-*&host=web-01&exclude=text&order=desc&limit=500&cursor=...`
```json
{"results": [{"received": "...", "device_id": "10.1.0.1", "severity": 6, "raw": "...", "ref": "opaque",
              "app": "sshd", "host": "web-01"}],
 "next_cursor": "opaque-or-empty", "scanned_bytes": 123456, "scanned_files": 12, "elapsed_ms": 840,
 "partial": false}
```
- `q`: substring by default (case-insensitive unless `case=true`). With `regex=true` it's an RE2 regular expression;
  an invalid one is a `400` with the parse error. `case` applies to both. Empty `q` matches every line, so it's a
  filtered browse.
- `q` is matched against the raw message as received (unescaped), not the stored line prefix.
- `exclude`: lines whose raw message matches it are left out. It's matched like `q`: a substring by default, an RE2
  regular expression with `regex=true`, and `case` applies. An invalid one is a `400` (the message starts
  `exclude: `). Empty means nothing is excluded.
- `app`: comma-separated programs/tags. Only lines whose header has one of them are returned, ignoring case. A value
  ending in `*` is a prefix (`ssh*`, `%FW-6-*`). A leading `%` is ignored on both sides, so `FW-6-302013` and
  `%FW-6-302013` both match the tag `%FW-6-302013` (in a URL, `%` is written `%25`). `*` alone matches any
  line that has a program/tag.
- `host`: comma-separated hostnames from the message header (not the sender's IP; use `devices` for that), exact
  match ignoring case.
- `app` and `host` read the header the same way the server parses messages (RFC 5424, RFC 3164 and common variants).
  A line whose header has no program/tag or hostname has `""` there and never matches an `app` or `host` filter. The
  header is parsed only for lines that passed the other filters.
- Each result's `app` and `host` are the parsed program/tag (with a `%` tag's `%`) and header hostname, or `""` when
  the header has none. Context lines carry them too.
- `from` / `to`: RFC 3339, on the received time: `from` ≤ received < `to`. Default `to` = now and `from` = to − 24 h.
  `from` must be before `to`.
- `devices`, `group` / `ungrouped`: as for `/api/live`. Hidden devices are searched only when named in `devices`.
- `order`: `desc` (newest first, the default) or `asc`.
- `limit`: default 500, max 5,000.
- `cursor`: from the previous page's `next_cursor`; the other parameters (`app`, `host` and `exclude` included) must
  be the same as on the first page, or the server returns `400`. The order of the values in `app`, `host` and `devices`
  doesn't matter. `limit` is the exception: it may change from page to page.
- Each request has a time budget of 10 s. If the budget runs out before `limit` results (or the page reaches 8 MiB of
  messages), the response has what it found, `partial: true` and a `next_cursor` that resumes where the scan stopped. The UI keeps asking while
  `partial` is true and the user hasn't cancelled.
- `next_cursor` is empty when the range is exhausted.
- `scanned_bytes`, `scanned_files` and `elapsed_ms` are for this request only; the UI adds them up across pages.
- `ref` identifies one line for `/api/context`. It stays valid until the file is deleted by retention.
- 1.1 (planned) adds `type`, `syntax`, `facilities`, `exclude_devices`, `exclude_app` and `exclude_host`, and the
  summary, schema and saved-search routes: see "1.1 (planned)" at the end.

`GET /api/context?ref=opaque&before=50&after=50` → `{"lines": [...same shape as results...], "hit_index": 50,
"at_start": false, "at_end": false}`
- Context isn't filtered: it's every stored line around the hit, so it takes no filter parameters.
- `at_start` / `at_end` are true when there are no older / newer stored lines for that device.
- Lines come from the same device, in file order, and can cross into the neighbouring part or day file.
- `before` / `after` max 500 each. A `ref` whose file is gone is a `404`.

## Exports

`POST /api/exports` body `{"devices": ["10.1.0.1"], "group": "", "ungrouped": false, "from": "...", "to": "...",
"severities": [0, 1, "unknown"], "q": "", "regex": false, "case": false, "app": ["sshd", "%FW-6-*"], "host": ["web-01"],
"exclude": ""}` → `202 {"id": "exp_..."}`
- `from` and `to` are required. Pick devices with `devices`, `group`, `ungrouped`, or `"all_devices": true` (every non-hidden device); at least one must select a device. `severities`, `q`, `regex`,
  `case`, `app`, `host` and `exclude` are optional filters with exactly the same meaning as search, so "Export these
  results" exports what the search showed. `app` and `host` are JSON lists here (search takes them comma-separated);
  blank items are dropped. Without filters every line in range is exported.
- One export builds at a time; a second `POST` while one is building is queued.

`POST /api/exports/estimate` (same body) → `{"lines": 123456, "bytes": 45678901, "files": 12}`: an upper bound
from the index, without scanning, so the UI can show the size before building.

`GET /api/exports` → `[{"id", "created", "devices", "from", "to", "state": "queued|building|ready|failed|expired",
"progress": 0.42, "lines": 0, "size_bytes": 0, "error": "", "ready_at": null, "expires_at": null,
"filters": {"group": "", "ungrouped": false, "severities": [], "q": "", "regex": false, "case": false, "app": [], "host": [],
"exclude": ""}}]`, newest
first. `ready_at` / `expires_at` are set once the export is ready; `filters` echoes the request.

`GET /api/exports/{id}` → one entry, same shape.

`GET /api/exports/{id}/download` → the zip (`Content-Disposition: attachment; filename="woodshed-<from>-<to>.zip"`).
When login is required, a browser download can't send the `X-Woodshed-Session` header, so the UI first calls
`POST /api/exports/{id}/ticket` (normal auth) → `{"url": "/api/exports/{id}/download?ticket=<opaque>"}` and navigates
to that URL. A ticket is single-use, valid for 60 s, bound to the session that asked for it and to that export, and
stored only hashed; the download route accepts it in place of the header (the cookie is still required). Without
login required, the ticket step is optional.
`409` if it isn't ready, `404` if unknown or expired.

`DELETE /api/exports/{id}` → `204`. It cancels a building export or deletes a ready one.

Exports are deleted 24 h after they're ready. Their size counts against free disk (the low-disk guard can delete
them first), but not against the log retention cap.

**Zip contents** (SPEC §4.1):
- `logs/<device-id>/<YYYY-MM-DD>.log`: the matching lines as stored, one message per line, in received order: each
  line is `<received> <sender> <message>`, with CR, LF and backslash in the message escaped as `\r`, `\n` and `\\`
  (`README.txt` shows how to unescape). Search and context results still return the raw message unescaped.
- `manifest.json`: Woodshed version, export time, the request (devices with names and groups, range, filters:
  `severities`, `q`, `regex`, `case`, `app`, `host` and `exclude`), and per-file line count and SHA-256. Also `gaps`: periods inside the range when Woodshed wasn't running or a listener
  was down, from the event log.
- `woodshed-events.log`: the service's own events in the range (start, stop, bind errors, rotations, deletions,
  drops, pauses).
- `README.txt`: a short note on what each file is, with the range and the filters in plain words ("Filters: none"
  when there are none).

## Settings

`GET /api/settings` returns an `ETag`. `PUT /api/settings` should send it as `If-Match`; a stale one gets `409
{"error": "settings changed since you loaded them"}` (a PUT without `If-Match` is accepted, for scripts).
`GET /api/settings` / `PUT /api/settings` (whole object; the server validates and returns the saved object, or `400` with
`{"errors": {"listeners.udp.port": "message", "listeners.allowlist[2]": "message"}}`). Error keys are dotted JSON paths,
with `[i]` for list items. `[i]` indexes the list exactly as the client sent it: the server validates before any
normalising (dedupe, sort). `listeners.allowlist` entries may be a bare IP or a CIDR. `storage.data_dir` is read-only in v1: a PUT that changes it gets a `400` on that key.
```json
{
  "listeners": {"udp": {"enabled": true, "addr": "0.0.0.0", "port": 514},
                "tcp": {"enabled": true, "addr": "0.0.0.0", "port": 514},
                "allowlist": [], "blocklist": [],
                "max_devices": 10000, "new_device_burst": 5000, "new_devices_per_minute": 200},
  "storage": {"data_dir": "...", "retention_days": 30, "retention_bytes": 21474836480, "part_bytes": 104857600},
  "ui": {"lan_mode": false, "port": 8514, "cert_file": "", "key_file": "", "require_login_local": false,
         "extra_hostnames": [], "trusted_hosts": []},
  "quiet": {"quiet_after_seconds": 300, "offline_after_seconds": 3600}
}
```

1.1 (planned) adds `"updates": {"check": "ask"}` to this object: see "1.1 (planned)" at the end.

`GET /api/storage/history` → `[{"time", "file", "device_id", "bytes", "reason": "age|cap|low_disk"}]`

## Auth

Two modes, set by `ui.lan_mode` in `/api/settings`:
- **Localhost (default):** plain HTTP on `127.0.0.1:<ui.port>`, no login. The server still refuses a `Host` header
  other than `localhost`, `127.0.0.1` or `[::1]` with the UI port (`403`), so a web page can't reach it by
  rebinding its own name to 127.0.0.1. Names in `ui.trusted_hosts` (exact host or host:port, e.g. a reverse proxy's
  public name) are also accepted. The `403` body names the rejected Host and points at `ui.trusted_hosts`.
- **LAN:** HTTPS on every interface, with a password. Turning it on needs a password first: a `PUT /api/settings`
  with `"lan_mode": true` and no password set gets `400 {"errors": {"ui.lan_mode": "..."}}`. The certificate is
  self-signed and generated on the machine (or one the user configured). Plain `http://` on the same port gets a
  `301` to `https://`. Changing `lan_mode` or `port` restarts the UI server just after the PUT's response, which
  also closes open live tails; the UI has to reload at the new address (`https://` after turning LAN mode on,
  `http://localhost:<port>/` after turning it off). The PUT response then carries `"restarting": true`.
- **Certificate names:** the self-signed certificate covers localhost, the hostname, `<hostname>.local`, the
  hostname qualified with each DNS search domain (`/etc/resolv.conf` on Linux and macOS, the primary DNS suffix on
  Windows), every stable address, and every name in `ui.extra_hostnames`. It's regenerated (same key) when that set
  gains a name or address.

In both modes, a state-changing request (anything but GET/HEAD/OPTIONS) from another origin is refused with `403`
(`Sec-Fetch-Site` or `Origin` vs `Host`). In LAN mode every `/api/` route needs a session (`401 {"error": ...}`
otherwise) except `/api/session`, `/api/login`, `/api/logout` and `GET /healthz`; the static UI files are public.

`GET /api/session`
```json
{"mode": "lan", "auth_required": true, "authenticated": true, "password_set": true}
```
In localhost mode: `{"mode": "localhost", "auth_required": false, "authenticated": true, "password_set": false}`.
`authenticated` is true only when the request carries both the session cookie and the matching `X-Woodshed-Session`
header (below). `/api/session` never returns the session secret.
`password_set` reports the real state in both modes (the LAN checkbox depends on it).

`ui.require_login_local` (default false; **turned on automatically when a password is first set**, and can be
turned off): when true and a password is set, localhost mode also requires login
(the same session rules as LAN mode). It can't be turned on without a password.

`PUT /api/password` requires `current` whenever a password is already set, in both modes. `woodshed
reset-password` on the machine is the only way to replace a password without knowing it.

**Forgotten password:** on the Woodshed machine, an administrator runs `woodshed reset-password`. It deletes the
password hash, turns LAN mode off in the config, and restarts the UI on `http://localhost:<port>/`. There's no
recovery over the network by design.

**After a `ui.lan_mode` or `ui.port` change** the server restarts after sending the response. The new address is
`https://<host the browser used>:<port>/` with LAN mode on, or `http://localhost:<port>/` with it off. There's no
"restarted" signal (the new address is a different origin, so the UI can't poll it under the CSP); the UI shows the
link straight away.

**`ui.cert_file` / `ui.key_file`:** both empty (self-signed) or both set, absolute paths to readable files. A
mismatched or unreadable pair is a 400 on `ui.cert_file` and/or `ui.key_file`.
The UI calls this first: in LAN mode with `authenticated: false` it shows the login form.

`POST /api/login` body `{"password": "..."}` → `200` with the session object above plus `"session_secret": "opaque"`
(returned only here, once per login), and a session cookie
(`__Host-woodshed_session`, `Secure; HttpOnly; SameSite=Strict; Path=/`; with `require_login_local` on plain
localhost, `woodshed_session`, the same without `Secure`). `401` for a wrong password, `400` in localhost mode
without `require_login_local`. Limits (`429` with `Retry-After`): each IP (each /64 for IPv6) gets 5 quick attempts,
then one a second; from the 5th failure (the count resets after 15 minutes without one), each address (the full
address, zone included) and each IPv4 address or IPv6 /64 waits 1 s, 2 s, 4 s … up to 15 minutes before its next
attempt, after which the right password works; and over 50 failures an hour across all clients spaces every attempt
60 s apart. A successful login also sets `woodshed_known_client` (`HttpOnly; SameSite=Strict`, `Secure` in LAN
mode, 90 days; stored hashed): attempts that carry it skip the per-network and global limits, though not their
own address's backoff.

`POST /api/logout` → `204`, ends the session and clears the cookie.

`PUT /api/password` body `{"current": "...", "new": "..."}` → `204`. `new` is 8–1024 bytes; a bad one gets
`400 {"error": ..., "errors": {"new": "..."}}`. Once a password is set, `current` is required in every mode (`403` if
wrong; it counts toward the login limits). With a session it also ends every other session. The password is stored only as an Argon2id hash in
`<data>/ui-password.hash` (mode 0600), never in the TOML.

**Session secret (replaces the CSRF token):** whenever login is required (LAN mode, or `require_login_local`),
every `/api/` request except `/api/session`, `/api/login`, `/api/logout` and `/healthz` must carry **both** the
session cookie **and** an `X-Woodshed-Session: <session_secret>` header; either one alone gets `401`. The cookie
isn't port-scoped, so another server on the same host could receive it; the secret lives only in the page's
`localStorage`, which is scoped to the exact origin (scheme, host and port), and a custom header can't be sent
cross-site, so it also does the CSRF token's job. The live tail therefore uses `fetch()` with a streaming reader
instead of `EventSource` (which can't send headers); the stream format is unchanged.

`GET /healthz` → `{"ok": true}` (no login in either mode).

## Run-state file

Not HTTP: a running server keeps `woodshed.state.json` in its data folder (mode 0640, rewritten within a second of
any change, deleted at a clean stop) for `woodshed status`, which reads it when the API needs a login or can't
answer. It only means something while the data folder's lock is held (a crash leaves it behind).
```json
{"pid": 4242, "version": "0.9.0", "started_at": "...", "config": "/etc/woodshed/woodshed.toml", "data_dir": "/var/lib/woodshed",
 "ui": {"listening": true, "url": "https://host:8514/", "lan_mode": true, "cert_fingerprint": "AB:CD:…"},
 "listeners": [{"proto": "udp", "addr": "0.0.0.0", "port": 514, "ok": true, "error": ""}],
 "udp_rcvbuf_bytes": 4194304, "udp_rcvbuf_requested_bytes": 33554432,
 "banners": [{"kind": "udp_buffer_small", "level": "warn", "text": "The UDP receive buffer is 4 MiB; …", "action": ""}]}
```
`listeners` and `banners` are exactly `/api/status`'s (banners sorted errors first); `listeners` is `[]` before the
listeners start. `ui.error` and `ui.cert_fingerprint` are omitted when empty.

## Errors

Any non-2xx returns `{"error": "human-readable message"}`.

## 1.1 (planned)

Not in 1.0 builds: nothing in this section is served yet. It's the 1.1 contract (SPEC §10). Routes here sit in a `###`
heading or after a label, never at the start of a line in the usual "`GET /api/...`" form, so `routes_test.go`
doesn't request them. When one ships, move it into its section above in the usual form, where the test checks it
against the server.

### Changed: search, export and saved-search filters

New parameters on `/api/search` and `/api/search/summary`. The same names, as JSON, go in the `POST /api/exports`
body (and its estimate) and in a saved search's `query`.
- `type`: the data type, `syslog`, which is the default and the only value in 1.1. Any other value is a
  `400 {"error": "type: unknown data type \"x\"."}`.
- `syntax`: `text` (the default: `q` and `exclude` mean exactly what they mean in 1.0) or `terms` (below).
- `facilities=0,4,16,unknown`: syslog facilities 0–23 (from the PRI) plus `unknown` (no parseable PRI). It works like
  `severities`: empty or missing means all, and `none` means nothing. An invalid value is a `400`.
- `exclude_devices=a,b`: leave these devices out, after `devices`, `group` and `ungrouped` have picked theirs. A
  device that's both named and excluded is excluded.
- `exclude_app`, `exclude_host`: comma-separated, each matched exactly like `app` and `host`. A line whose
  program/tag (or hostname) matches any of them is left out. A line without a program/tag (or hostname) is never
  left out by them.
- Cursors are bound to all of these too: changing one between pages is a `400`, as now.
- Export: `"type": "syslog", "syntax": "terms", "facilities": [4, "unknown"], "exclude_devices": ["10.1.0.9"],
  "exclude_app": ["kernel"], "exclude_host": ["lab-01"]`, all optional. `GET /api/exports`' `filters`, the zip's
  `manifest.json` and `README.txt` include them.

**`syntax=terms`** (grammar in SPEC §10.1.3): words and `"quoted phrases"` are substrings of the raw message, matched
as `q` is (`case` applies to all of them). They're joined by `AND` (or just a space), `OR` and `NOT`, upper case only,
with `( )` for grouping. NOT binds tightest, then AND, then OR. At most 32 words and phrases, 8 levels of parentheses
and 4 KiB of `q`.
- `regex=true` with `syntax=terms` is a `400 {"error": "Terms and regex can't be combined: pick one."}`.
- A parse error is a `400` naming a 1-based character position, for example
  `{"error": "Unclosed quote starting at character 12."}`. For `exclude`, the message starts with `exclude: `. The
  full list is in SPEC §10.1.3.

### `GET /api/search/schema`

The data types Events can show, and the fields of each one's field panel. The client builds the panel from this list;
it has no field list of its own.
```json
{"types": [
  {"type": "syslog", "label": "Syslog", "default": true,
   "fields": [
     {"key": "device",   "label": "Device",      "param": "devices",    "exclude_param": "exclude_devices", "domain": null},
     {"key": "severity", "label": "Severity",    "param": "severities", "exclude_param": "",
      "domain": [{"value": "0", "label": "emerg"}, {"value": "1", "label": "alert"}, {"value": "2", "label": "crit"},
                 {"value": "3", "label": "err"}, {"value": "4", "label": "warning"}, {"value": "5", "label": "notice"},
                 {"value": "6", "label": "info"}, {"value": "7", "label": "debug"}, {"value": "unknown", "label": "unknown"}]},
     {"key": "facility", "label": "Facility",    "param": "facilities", "exclude_param": "",
      "domain": [{"value": "0", "label": "kern"}, {"value": "1", "label": "user"}, {"value": "4", "label": "auth"},
                 {"value": "16", "label": "local0"}, {"value": "unknown", "label": "unknown"}]},   // all 24 and unknown; see below
     {"key": "host",     "label": "Host",        "param": "host",       "exclude_param": "exclude_host", "domain": null},
     {"key": "app",      "label": "Program/tag", "param": "app",        "exclude_param": "exclude_app",  "domain": null}
   ]}
]}
```
- `param` is the filter parameter a value is added to; a value goes in exactly as its `value`.
- `exclude_param` is the parameter that excludes a value. It's `""` for a field with a `domain`: to exclude a value
  there, send every other value of the domain in `param`.
- `domain` is the complete value set of a closed field (severity, facility), or `null`. The facility labels are kern,
  user, mail, daemon, auth, syslog, lpr, news, uucp, cron, authpriv, ftp, ntp, audit, alert, clock and local0–local7
  (0–23), then unknown.
- The panel shows the fields in this order. 1.1 has one type; a later type is another entry in `types`.

### `GET /api/search/summary`

The field panel and the histogram for a query, from one scan.

Parameters: every `/api/search` filter parameter (not `order` or `limit`), plus:
- `fields`: comma-separated field keys from the schema; all of them by default, or `none` for the histogram alone.
- `top`: values per field, 10 by default, at most 50. Fields with a `domain` return every value that has a count.
- `histogram`: `false` to leave it out.
- `bucket`: the bucket size in seconds, one of 1, 5, 15, 60, 300, 900, 3600, 7200, 21600, 86400 and 604800, giving
  at most 1,000 buckets. By default it's picked from the range (SPEC §10.1.2). Any other value is a `400`.
- `budget_ms`: this request's time budget, 2,000 by default, at most 10,000.
- `cursor`: from the previous response's `next_cursor`. The other parameters must be the same, or it's a `400`.

```json
{"type": "syslog", "from": "2026-11-13T14:00:00Z", "to": "2026-11-13T15:00:00Z",
 "matched": 48210,                           // matching lines counted so far, in [counted_from, to)
 "counted_from": "2026-11-13T14:00:00Z",     // equals from once the count is complete
 "histogram": {"bucket_seconds": 60, "source": "index",          // "index" | "scan"
               "buckets": [{"start": "2026-11-13T14:00:00Z", "count": 812, "approx": false}]},   // count: null = not counted yet
 "fields": [{"key": "host", "label": "Host",
             "values": [{"value": "web-01", "label": "web-01", "count": 20311, "filterable": true}],
             "other": 1204, "none": 37, "distinct": 14, "distinct_capped": false}],
 "next_cursor": "", "partial": false, "scanned_bytes": 0, "scanned_files": 0, "elapsed_ms": 4}
```
- **Counts are exact** for the matching lines in `[counted_from, to)`. The scan reads newest first, so a partial
  response has counted the newest part of the range. Each response carries the running totals, not a delta, so the
  client just replaces what it shows. `partial` and `next_cursor` work as in search; `next_cursor` is `""` once the
  count is complete.
- `histogram.source`:
  - `"index"` when the query has no `q`, `exclude`, `app`, `host`, `exclude_app`, `exclude_host`, facility or severity
    filter, and the bucket is at least 60 s. Devices, group, ungrouped and `exclude_devices` are allowed. It's read
    from the stats and checkpoints without opening a log file, and it's complete in the first response whatever
    `counted_from` says. `approx: true` marks a bucket spread from checkpoints (SPEC §10.1.2).
  - `"scan"` otherwise. The buckets come from the same scan as `fields`: those wholly before `counted_from` are
    `null`, and the one that straddles it has the count so far.
- Buckets start at multiples of `bucket_seconds` since the Unix epoch, in UTC (7-day buckets on Mondays, 00:00 UTC).
  The first bucket may start before `from` and the last end after `to`; both count only lines in `[from, to)`.
- `fields[].values` are sorted by count, highest first, ties by `value`. `label` is what to show: a device's name, or
  its IP if it has none, and the domain label for severity and facility.
  - `other` counts the lines with a value that isn't listed.
  - `none` counts the lines without a value (no hostname or no program/tag; always 0 for device, severity and
    facility).
  - `distinct` is the number of different values seen. At most 10,000 per field are tracked, and past that
    `distinct_capped` is true and new values count under `other`.
  - `filterable` is false for a value that `param` can't express, such as one containing a comma.
- `matched` is the sum of the histogram when only the index histogram was asked for (`fields=none`).
- The running counts are kept on the server for 10 minutes after the last request, for at most 8 summaries (least
  recently used out first). A cursor whose counts are gone is a
  `400 {"error": "This summary expired; start it again."}`.
- At most 2 summary scans run at once; a third waits within its own budget and returns what it has. Searches never
  wait for summaries. A cancelled request stops its scan.

### Saved searches

**List:** `GET /api/saved-searches?type=syslog` → every saved search of that type (default `syslog`), sorted by name ignoring
case:
```json
[{"id": "ss_8f2c1a9d", "name": "Edge denies", "type": "syslog",
  "created": "2026-11-13T14:02:11Z", "updated": "2026-11-13T14:02:11Z",
  "query": {"q": "deny NOT 10.0.0.1", "syntax": "terms", "regex": false, "case": false, "exclude": "",
            "devices": ["10.1.0.1"], "group": "", "ungrouped": false, "exclude_devices": [],
            "severities": [], "facilities": [], "app": ["%FW-6-*"], "exclude_app": [], "host": [], "exclude_host": [],
            "order": "desc", "range": {"last": "1h"}},
  "missing_devices": [], "missing_group": false}]
```
- `query` takes every Events parameter, with the same meaning and validation as `/api/search` (lists as JSON lists,
  severities and facilities as in the export body). Missing keys take their defaults.
- `range` is either `{"last": "15m"}`, a whole number of `m`, `h` or `d` from 1 minute to 400 days, resolved against
  now each time it runs, or `{"from": "…", "to": "…"}` in RFC 3339.
- `missing_devices` lists the IDs in `devices` that the server doesn't know (any more), and `missing_group` is true
  when `group` has no devices. Both are worked out on every read and never stored. A saved search is never changed
  because a device went away: device IDs are addresses, so a device that sends again matches again. If every named
  device is missing, the search matches nothing; it never widens to all devices.

**Read:** `GET /api/saved-searches/{id}` → one saved search, with an `ETag`. `404` if it's unknown.

**Create:** `POST /api/saved-searches` body `{"name": "Edge denies", "type": "syslog", "query": {...}}` (`type` optional) →
`201` with the saved search.

**Update:** `PUT /api/saved-searches/{id}` (the same body) → `200` with the saved search. Send `If-Match` with its `ETag`, as for
settings. A stale one gets `409 {"error": "This saved search changed since you loaded it."}`. A PUT without `If-Match`
is accepted.

**Delete:** `DELETE /api/saved-searches/{id}` → `204`, or `404` if it's unknown.

Limits and errors:
- At most 200 saved searches: one more is a `409 {"error": "There are already 200 saved searches; delete one first."}`.
- `name` is 1–80 characters after trimming, unique ignoring case. A clash is a
  `409 {"error": "A saved search named \"Edge denies\" already exists."}`.
- Each list in `query` holds at most 1,000 items, and the body is at most 64 KiB.
- Validation errors are a `400` with dotted keys, as for settings:
  `{"errors": {"name": "…", "query.q": "Unclosed quote starting at character 12.", "query.range.last": "…"}}`.
- They're stored in `index.db` (SPEC §10.1.4), which `woodshed reindex` keeps.

### Update check: `GET /api/updates`

```json
{"check": "on",                                 // "ask" | "on" | "off", from updates.check
 "available": true,                             // false in a build that can't check (not a release version, or no releases_url)
 "url": "https://gmccollu.github.io/woodshed/releases.json",
 "current": "1.1.0",
 "last_check": "2026-11-13T09:12:44Z",          // null until the first check
 "last_result": "ok",                           // "" (never checked) | "ok" | "error"
 "last_error": "", "last_error_kind": "",       // kinds: dns, connect, proxy, tls, timeout, http_status, redirect, too_large, bad_json, no_release
 "next_check": "2026-11-14T08:40:02Z",          // null unless check is "on"
 "latest": {"version": "1.2.0", "prerelease": false,
            "url": "https://github.com/gmccollu/woodshed/releases/tag/v1.2.0"},   // null until a check succeeds, and after turning it off
 "update_available": true,
 "dismissed_version": ""}
```
- `latest` is the newest release this build would offer: the stable one, or for a prerelease build the newer of
  stable and prerelease. `update_available` is true when it's newer than `current`. `latest.url` is always
  `releases_url + "/tag/" + <the tag name>`, whatever the file's `html_url` says.

**Change:** `PUT /api/updates` body `{"check": "on"}` or `{"check": "off"}` → `200` with the object above. It writes
`updates.check` to the config like a Settings save (the settings ETag changes too). The banner's **Turn on** and
**No thanks** use it. `"ask"` isn't accepted here (`400`); Settings can set it.

**Check now:** `POST /api/updates/check` → `202`: check now. `409` unless `check` is `"on"`; `429` with `Retry-After` within 60 s of
the last one started this way.

**Dismiss:** `POST /api/updates/dismiss` body `{"version": "1.2.0"}` → `204`: hide the `update_available` banner for that version,
for everyone. A newer version shows it again.

**Banners** (in `/api/status`, after errors and warnings):
- `update_prompt` (info, action `update_choice`): shown while `updates.check` is `"ask"`, in a build that can check.
  The UI shows **Turn on** and **No thanks**, which send `PUT /api/updates`. Text: "Check for new Woodshed versions
  once a day? It fetches one public file from gmccollu.github.io and sends nothing about this server."
- `update_available` (info, action `update_dismiss`): "Woodshed 1.2.0 is available (this is 1.1.0)." It has a `url`
  with the release page. `url` is a new optional field on every banner, omitted when empty.

**Settings:** `/api/settings` gains `"updates": {"check": "ask"}`. `updates.check` must be `"ask"`, `"on"` or `"off"`
(a `400` on `updates.check` otherwise). In the TOML:
```toml
[updates]
check = "ask"   # "ask" (the default: ask once in the UI), "on" or "off"
```
A config without `[updates]` (a fresh install, an upgrade from 1.0, portable mode) means `"ask"`. Setting `"on"` or
`"off"` before the first start skips the banner, and so does installing with `WOODSHED_UPDATE_CHECK=on|off` (the MSI
property, or the environment variable for pkg, deb and rpm), which runs `woodshed update-check <value>`. The request itself, the schedule and the state file
(`<data>/update-check.json`) are in SPEC §10.2.
