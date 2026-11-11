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
  "udp_rcvbuf_bytes": 33554432,
  "kernel_drops_visible": false,
  "listeners": [
    {"proto": "udp", "addr": "0.0.0.0", "port": 514, "ok": true,  "error": ""},
    {"proto": "tcp", "addr": "0.0.0.0", "port": 514, "ok": false, "error": "bind: address already in use", "holder": "syslogd (pid 312)"}
  ],
  "host_ips": ["10.1.0.50", "fd00::50"],
  "banners": [{"level": "error", "text": "TCP 514 is in use by syslogd (pid 312)."}]
}
```

`POST /api/listeners/retry` → `204`, then re-read `/api/status`.

`GET /api/stats`
```json
{
  "msgs_per_sec": 1284.2,
  "sparkline": [1100, 1180, 1250],            // msgs/sec averaged per minute, oldest first, last 60 minutes
  "devices": {"total": 14, "online": 12, "quiet": 1, "offline": 1},
  "disk": {"used_bytes": 3435973836, "cap_bytes": 21474836480, "free_bytes": 400000000000, "days_kept": 31},
  "dropped_24h": 0,                           // drops Woodpile itself saw (disk guard, write errors)
  "kernel_dropped_24h": null,                 // integer on Linux, null where the OS doesn't expose it
  "truncated_24h": 0
}
```

## Devices

`GET /api/devices`
```json
[{"id": "10.1.0.1", "ip": "10.1.0.1", "name": "edge-fw-01", "group": "HQ", "hidden": false,
  "first_seen": "...", "last_heard": "...", "status": "online",   // online | quiet | offline
  "msgs_per_min": 36720, "total_msgs": 9123456, "bytes_on_disk": 123456789}]
```

`PATCH /api/devices/{id}` body `{"name": "...", "group": "...", "hidden": true}` (any subset) → the updated device.

## Live tail

`GET /api/live?devices=10.1.0.1,10.1.0.2&group=HQ&min_severity=4&q=text` → `text/event-stream`.
- `event: message` `data: {"received": "...", "device_id": "10.1.0.1", "severity": 6, "raw": "<134>..."}`
- `event: sampled` `data: {"rate": 1284.2, "shown": 50}`: sent once a second while the server is sampling.
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
`{"errors": {"field": "message"}}`).
```json
{
  "listeners": {"udp": {"enabled": true, "addr": "0.0.0.0", "port": 514},
                "tcp": {"enabled": true, "addr": "0.0.0.0", "port": 514},
                "allowlist": []},
  "storage": {"data_dir": "...", "retention_days": 30, "retention_bytes": 21474836480, "part_bytes": 104857600},
  "ui": {"lan_mode": false, "port": 8514},
  "quiet": {"quiet_after_seconds": 300, "offline_after_seconds": 3600}
}
```

`GET /api/storage/history` → `[{"time", "file", "device_id", "bytes", "reason": "age|cap|low_disk"}]`

## Errors

Any non-2xx returns `{"error": "human-readable message"}`.
