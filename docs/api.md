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
  "rejected_24h": 0                           // messages from senders outside the allowlist
}
```
`days_kept` is the number of distinct UTC days that still have data on disk (today counts).
`devices` counts exclude hidden devices.

`GET /api/rejected` → `[{"ip": "198.51.100.7", "count_24h": 120, "last_seen": "..."}]` (senders refused by the allowlist)
Sorted by `count_24h` descending, at most 200 entries. The server tracks at most 10,000 refused IPs and evicts the
least recently seen.
`banners` in `/api/status` are sent in priority order: errors first, then warnings.

## Devices

`GET /api/devices`
```json
[{"id": "10.1.0.1", "ip": "10.1.0.1", "name": "edge-fw-01", "group": "HQ", "hidden": false,
  "first_seen": "...", "last_heard": "...", "status": "online",   // online | quiet | offline
  "msgs_per_min": 36720, "total_msgs": 9123456, "bytes_on_disk": 123456789}]
```

Hidden devices are included, with `"hidden": true`.

`PATCH /api/devices/{id}` body `{"name": "...", "group": "...", "hidden": true}` (any subset) → the updated device.

## Live tail

`GET /api/live?devices=10.1.0.1,10.1.0.2&group=HQ&ungrouped=true&min_severity=4&q=text` → `text/event-stream`.
- `event: message` `data: {"received": "...", "device_id": "10.1.0.1", "severity": 6, "raw": "<134>..."}`
- `event: sampled` `data: {"rate": 1284.2, "shown": 50}`: sent once a second while the server is sampling.
- `group` and `ungrouped=true` are alternatives; `ungrouped=true` matches devices with no group.
- `severity` is an integer 0–7, or `null` when the message had no parseable PRI.
- `min_severity` is the numeric syslog severity ceiling (0 emerg … 7 debug); messages with an unknown severity
  are always included.
- On connect, the server first sends up to the last 200 matching messages from the ring buffer.

## Search (M3)

`GET /api/search?q=text&regex=false&devices=a,b&group=HQ&from=...&to=...&min_severity=7&limit=500&cursor=...`
```json
{"results": [{"received": "...", "device_id": "10.1.0.1", "severity": 6, "raw": "...", "ref": "opaque"}],
 "next_cursor": "opaque-or-empty", "scanned_bytes": 123456, "truncated": false}
```
`GET /api/context?ref=opaque&before=50&after=50` → `{"lines": [...same shape as results...], "hit_index": 50}`

## Exports (M3)

`POST /api/exports` body `{"devices": ["10.1.0.1"], "from": "...", "to": "..."}` → `{"id": "exp_..."}`
`GET /api/exports` → `[{"id", "created", "devices", "from", "to", "state": "building|ready|failed", "size_bytes", "error"}]`
`GET /api/exports/{id}/download` → the zip.

## Settings

`GET /api/settings` / `PUT /api/settings` (whole object; the server validates and returns the saved object, or `400` with
`{"errors": {"listeners.udp.port": "message", "listeners.allowlist[2]": "message"}}`). Error keys are dotted JSON paths,
with `[i]` for list items. `[i]` indexes the list exactly as the client sent it: the server validates before any
normalising (dedupe, sort). `listeners.allowlist` entries may be a bare IP or a CIDR. `storage.data_dir` is read-only in v1: a PUT that changes it gets a `400` on that key.
```json
{
  "listeners": {"udp": {"enabled": true, "addr": "0.0.0.0", "port": 514},
                "tcp": {"enabled": true, "addr": "0.0.0.0", "port": 514},
                "allowlist": []},
  "storage": {"data_dir": "...", "retention_days": 30, "retention_bytes": 21474836480, "part_bytes": 104857600},
  "ui": {"lan_mode": false, "port": 8514, "cert_file": "", "key_file": ""},   // empty = self-signed
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
{"mode": "lan", "auth_required": true, "authenticated": true, "password_set": true,
 "csrf_token": "opaque"}                    // "" when not logged in, and always "" in localhost mode
```
In localhost mode: `{"mode": "localhost", "auth_required": false, "authenticated": true, "password_set": false, "csrf_token": ""}`.
The UI calls this first: in LAN mode with `authenticated: false` it shows the login form.

`POST /api/login` body `{"password": "..."}` → `200` with the session object above, and a session cookie
(`__Host-woodpile_session`, `Secure; HttpOnly; SameSite=Strict; Path=/`). `401` for a wrong password, `400` in
localhost mode. Each IP (each /64 for IPv6) gets 5 quick attempts, then one a second (`429`, `Retry-After`), and
10 failures within 15 minutes lock it out for 15 minutes (`429` with `Retry-After`, even for the right password).

`POST /api/logout` → `204`, ends the session and clears the cookie.

`PUT /api/password` body `{"current": "...", "new": "..."}` → `204`. `new` is 8–1024 bytes; a bad one gets
`400 {"error": ..., "errors": {"new": "..."}}`. In localhost mode `current` is ignored (whoever can reach the UI there
is trusted, as for every other setting). In LAN mode it needs a session and `current` (`403` if wrong; it counts
toward the lockout), and it ends every other session. The password is stored only as an Argon2id hash in
`<data>/ui-password.hash` (mode 0600), never in the TOML.

**CSRF:** in LAN mode every state-changing request with a session must send the session's `csrf_token` in an
`X-CSRF-Token` header, or it gets `403`. Login is exempt (it has no session yet). `EventSource` needs no token: the
live tail is a GET.

`GET /healthz` → `{"ok": true}` (no login in either mode).

## Errors

Any non-2xx returns `{"error": "human-readable message"}`.
