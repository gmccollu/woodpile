# Woodpile HTTP API (v1 contract)

The UI talks only to these routes. All responses are JSON unless noted. Times are RFC 3339 UTC strings.
Byte counts are integers. The frontend builds against this contract with mock data; the backend implements it.
Change this file first if the contract changes.

## Status and stats

`GET /api/status`
```json
{
  "version": "0.1.0", "build": "abc1234", "os": "darwin/arm64",
  "data_dir": "/Library/Application Support/Woodpile",
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
               "action": "retry_listeners"}]   // action: "" | "retry_listeners"; kinds: listener_bind, writes_paused, low_disk
}
```

`POST /api/listeners/retry` → `204`, then re-read `/api/status`.

`GET /api/stats`
```json
{
  "msgs_per_sec": 1284.2,
  "sparkline": [1100, 1180, 1250],            // msgs/sec averaged per minute, oldest first, last 60 minutes
  "devices": {"total": 14, "online": 12, "quiet": 1, "offline": 1},
  "disk": {"used_bytes": 3435973836, "cap_bytes": 21474836480, "free_bytes": 400000000000, "days_kept": 30},
  "dropped_24h": 0,                           // drops Woodpile itself saw (disk guard, write errors)
  "kernel_dropped_24h": null,                 // integer on Linux, null where the OS doesn't expose it
  "truncated_24h": 0,
  "rejected_24h": 0                           // refused messages and TCP connections (allowlist, per-source limit)
}
```
`days_kept` is the number of distinct UTC days that still have data on disk (today counts).
`devices` counts exclude hidden devices.

`GET /api/rejected` → `[{"ip": "198.51.100.7", "count_24h": 120, "last_seen": "...", "reason": "allowlist"}]` (refused
senders). `reason` is the latest refusal's reason: `allowlist` or `per_source` (over the TCP connections-per-source limit).
Sorted by `count_24h` descending, at most 200 entries. The server tracks at most 10,000 refused IPs and evicts the
least recently seen.
Banner kind `device_limit` (warn): new senders are being refused because the active-device cap
(`listeners.max_devices`, default 10,000 devices that sent in the last 10 minutes) or the new-device rate
(`listeners.new_device_burst` 5,000, then `listeners.new_devices_per_minute` 200) was reached; their messages count
as dropped. Senders that already have logs on disk are exempt from the rate. All three are editable in Settings.
`banners` in `/api/status` are sent in priority order: errors first, then warnings.

## Devices

`GET /api/devices?limit=200&offset=0&sort=rate|name|last_heard|first_seen&q=text&group=HQ&ungrouped=true&hidden=include|only|exclude`
→ `{"devices": [...], "total": 1234}`. Default `limit` 200, max 1,000; default `sort=rate`. Directions are fixed:
`rate` busiest first, `name` A–Z (devices without a name sort by IP), `last_heard` and `first_seen` newest first. `q` matches
id, IP or name (case-insensitive substring); `hidden` defaults to `include`. Each device:
```json
[{"id": "10.1.0.1", "ip": "10.1.0.1", "name": "edge-fw-01", "group": "HQ", "hidden": false,
  "first_seen": "...", "last_heard": "...", "status": "online",   // online | quiet | offline
  "msgs_per_min": 36720, "total_msgs": 9123456, "bytes_on_disk": 123456789}]
```

Hidden devices are included, with `"hidden": true`.

`GET /api/groups` → `[{"name": "HQ", "devices": 12}]`, sorted by name, with the number of non-hidden devices in each
(ungrouped devices aren't listed).

`PATCH /api/devices/{id}` body `{"name": "...", "group": "...", "hidden": true}` (any subset) → the updated device.

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

`GET /api/search?q=text&regex=false&case=false&devices=a,b&group=HQ&ungrouped=true&from=...&to=...&severities=...&order=desc&limit=500&cursor=...`
```json
{"results": [{"received": "...", "device_id": "10.1.0.1", "severity": 6, "raw": "...", "ref": "opaque"}],
 "next_cursor": "opaque-or-empty", "scanned_bytes": 123456, "scanned_files": 12, "elapsed_ms": 840,
 "partial": false}
```
- `q`: substring by default (case-insensitive unless `case=true`). With `regex=true` it's an RE2 regular expression;
  an invalid one is a `400` with the parse error. `case` applies to both. Empty `q` matches every line, so it's a
  filtered browse.
- `q` is matched against the raw message as received (unescaped), not the stored line prefix.
- `from` / `to`: RFC 3339, on the received time: `from` ≤ received < `to`. Default `to` = now and `from` = to − 24 h.
  `from` must be before `to`.
- `devices`, `group` / `ungrouped`: as for `/api/live`. Hidden devices are searched only when named in `devices`.
- `order`: `desc` (newest first, the default) or `asc`.
- `limit`: default 500, max 5,000.
- `cursor`: from the previous page's `next_cursor`; the other parameters must be the same as on the first page, or
  the server returns `400`. `limit` is the exception: it may change from page to page.
- Each request has a time budget of 10 s. If the budget runs out before `limit` results (or the page reaches 8 MiB of
  messages), the response has what it found, `partial: true` and a `next_cursor` that resumes where the scan stopped. The UI keeps asking while
  `partial` is true and the user hasn't cancelled.
- `next_cursor` is empty when the range is exhausted.
- `scanned_bytes`, `scanned_files` and `elapsed_ms` are for this request only; the UI adds them up across pages.
- `ref` identifies one line for `/api/context`. It stays valid until the file is deleted by retention.

`GET /api/context?ref=opaque&before=50&after=50` → `{"lines": [...same shape as results...], "hit_index": 50,
"at_start": false, "at_end": false}`
- `at_start` / `at_end` are true when there are no older / newer stored lines for that device.
- Lines come from the same device, in file order, and can cross into the neighbouring part or day file.
- `before` / `after` max 500 each. A `ref` whose file is gone is a `404`.

## Exports

`POST /api/exports` body `{"devices": ["10.1.0.1"], "group": "", "ungrouped": false, "from": "...", "to": "...",
"severities": [0, 1, "unknown"], "q": "", "regex": false, "case": false}` → `202 {"id": "exp_..."}`
- `from` and `to` are required. Pick devices with `devices`, `group`, `ungrouped`, or `"all_devices": true` (every non-hidden device); at least one must select a device. `severities`, `q`, `regex`
  and `case` are optional filters with exactly the same meaning as search, so "Export these results" exports what
  the search showed. Without them every line in range is exported.
- One export builds at a time; a second `POST` while one is building is queued.

`POST /api/exports/estimate` (same body) → `{"lines": 123456, "bytes": 45678901, "files": 12}`: an upper bound
from the index, without scanning, so the UI can show the size before building.

`GET /api/exports` → `[{"id", "created", "devices", "from", "to", "state": "queued|building|ready|failed|expired",
"progress": 0.42, "lines": 0, "size_bytes": 0, "error": "", "ready_at": null, "expires_at": null,
"filters": {"group": "", "ungrouped": false, "severities": [], "q": "", "regex": false, "case": false}}]`, newest
first. `ready_at` / `expires_at` are set once the export is ready; `filters` echoes the request.

`GET /api/exports/{id}` → one entry, same shape.

`GET /api/exports/{id}/download` → the zip (`Content-Disposition: attachment; filename="woodpile-<from>-<to>.zip"`).
When login is required, a browser download can't send the `X-Woodpile-Session` header, so the UI first calls
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
- `manifest.json`: Woodpile version, export time, the request (devices with names and groups, range, filters), and
  per-file line count and SHA-256. Also `gaps`: periods inside the range when Woodpile wasn't running or a listener
  was down, from the event log.
- `woodpile-events.log`: the service's own events in the range (start, stop, bind errors, rotations, deletions,
  drops, pauses).
- `README.txt`: a short note on what each file is.

## Settings

`GET /api/settings` / `PUT /api/settings` (whole object; the server validates and returns the saved object, or `400` with
`{"errors": {"listeners.udp.port": "message", "listeners.allowlist[2]": "message"}}`). Error keys are dotted JSON paths,
with `[i]` for list items. `[i]` indexes the list exactly as the client sent it: the server validates before any
normalising (dedupe, sort). `listeners.allowlist` entries may be a bare IP or a CIDR. `storage.data_dir` is read-only in v1: a PUT that changes it gets a `400` on that key.
```json
{
  "listeners": {"udp": {"enabled": true, "addr": "0.0.0.0", "port": 514},
                "tcp": {"enabled": true, "addr": "0.0.0.0", "port": 514},
                "allowlist": [],
                "max_devices": 10000, "new_device_burst": 5000, "new_devices_per_minute": 200},
  "storage": {"data_dir": "...", "retention_days": 30, "retention_bytes": 21474836480, "part_bytes": 104857600},
  "ui": {"lan_mode": false, "port": 8514, "cert_file": "", "key_file": "", "require_login_local": false},
  "quiet": {"quiet_after_seconds": 300, "offline_after_seconds": 3600}
}
```

`GET /api/storage/history` → `[{"time", "file", "device_id", "bytes", "reason": "age|cap|low_disk"}]`

## Auth

Two modes, set by `ui.lan_mode` in `/api/settings`:
- **Localhost (default):** plain HTTP on `127.0.0.1:<ui.port>`, no login. The server still refuses a `Host` header
  other than `localhost`, `127.0.0.1` or `[::1]` with the UI port (`403`), so a web page can't reach it by
  rebinding its own name to 127.0.0.1.
- **LAN:** HTTPS on every interface, with a password. Turning it on needs a password first: a `PUT /api/settings`
  with `"lan_mode": true` and no password set gets `400 {"errors": {"ui.lan_mode": "..."}}`. The certificate is
  self-signed and generated on the machine (or one the user configured). Plain `http://` on the same port gets a
  `301` to `https://`. Changing `lan_mode` or `port` restarts the UI server just after the PUT's response, which
  also closes open live tails; the UI has to reload at the new address (`https://` after turning LAN mode on,
  `http://localhost:<port>/` after turning it off).

In both modes, a state-changing request (anything but GET/HEAD/OPTIONS) from another origin is refused with `403`
(`Sec-Fetch-Site` or `Origin` vs `Host`). In LAN mode every `/api/` route needs a session (`401 {"error": ...}`
otherwise) except `/api/session`, `/api/login`, `/api/logout` and `GET /healthz`; the static UI files are public.

`GET /api/session`
```json
{"mode": "lan", "auth_required": true, "authenticated": true, "password_set": true}
```
In localhost mode: `{"mode": "localhost", "auth_required": false, "authenticated": true, "password_set": false}`.
`authenticated` is true only when the request carries both the session cookie and the matching `X-Woodpile-Session`
header (below). `/api/session` never returns the session secret.
`password_set` reports the real state in both modes (the LAN checkbox depends on it).

`ui.require_login_local` (default false; **turned on automatically when a password is first set**, and can be
turned off): when true and a password is set, localhost mode also requires login
(the same session rules as LAN mode). It can't be turned on without a password.

`PUT /api/password` requires `current` whenever a password is already set, in both modes. `woodpile
reset-password` on the machine is the only way to replace a password without knowing it.

**Forgotten password:** on the Woodpile machine, an administrator runs `woodpile reset-password`. It deletes the
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
(`__Host-woodpile_session`, `Secure; HttpOnly; SameSite=Strict; Path=/`; with `require_login_local` on plain
localhost, `woodpile_session`, the same without `Secure`). `401` for a wrong password, `400` in localhost mode
without `require_login_local`. Limits (`429` with `Retry-After`): each IP (each /64 for IPv6) gets 5 quick attempts,
then one a second; from the 5th failure within 15 minutes, each address (the full address, zone included) waits
1 s, 2 s, 4 s … up to 15 minutes before its next attempt, after which the right password works; and over 200
failures a minute across all clients spaces every attempt 5 s apart.

`POST /api/logout` → `204`, ends the session and clears the cookie.

`PUT /api/password` body `{"current": "...", "new": "..."}` → `204`. `new` is 8–1024 bytes; a bad one gets
`400 {"error": ..., "errors": {"new": "..."}}`. Once a password is set, `current` is required in every mode (`403` if
wrong; it counts toward the login limits). With a session it also ends every other session. The password is stored only as an Argon2id hash in
`<data>/ui-password.hash` (mode 0600), never in the TOML.

**Session secret (replaces the CSRF token):** whenever login is required (LAN mode, or `require_login_local`),
every `/api/` request except `/api/session`, `/api/login`, `/api/logout` and `/healthz` must carry **both** the
session cookie **and** an `X-Woodpile-Session: <session_secret>` header; either one alone gets `401`. The cookie
isn't port-scoped, so another server on the same host could receive it; the secret lives only in the page's
`localStorage`, which is scoped to the exact origin (scheme, host and port), and a custom header can't be sent
cross-site, so it also does the CSRF token's job. The live tail therefore uses `fetch()` with a streaming reader
instead of `EventSource` (which can't send headers); the stream format is unchanged.

`GET /healthz` → `{"ok": true}` (no login in either mode).

## Errors

Any non-2xx returns `{"error": "human-readable message"}`.
