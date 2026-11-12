# Woodshed

A simple, reliable syslog server for Windows, macOS and Linux, with a clean web UI.
Install it, point your devices at it, and it keeps every message as plain files within a disk budget.

**[Download Woodshed](https://github.com/gmccollu/woodshed/releases/latest)**: an installer for Windows (MSI), macOS
(pkg) or Linux (deb, rpm), or a portable zip/tarball that runs with nothing installed.
Then follow the **[user guide](docs/user-guide.md)**: install or run portable, point a device at it, find the logs,
search, and export a zip for support. Questions: [FAQ](FAQ.md). What's new: [CHANGELOG.md](CHANGELOG.md).

More: [docs/install.md](docs/install.md) (every installer in detail), [SPEC.md](SPEC.md) (the design),
[docs/api.md](docs/api.md) (the UI's API), [CONTRIBUTING.md](CONTRIBUTING.md), [SECURITY.md](SECURITY.md).

## Features

- **Listens straight away:** UDP and TCP on port 514 with no setup. TCP framing (octet counting or newline) is
  detected per message. RFC 5424, RFC 3164 and the usual non-compliant variants are parsed leniently; the raw
  message is always stored exactly as received.
- **Plain files:** one folder per device and one file per day (`logs/<ip>/<day>.log`), gzipped once closed.
  Read them with grep, an editor or any log tool.
- **Never fills the disk:** retention by age and total size (30 days / 20 GiB by default), plus a low-disk guard.
  Every deletion is recorded and shown in Settings.
- **Honest counters:** dropped, truncated and refused messages are counted and shown. Where the OS hides kernel
  UDP drops (Windows, macOS), the UI says so instead of showing a reassuring 0.
- **Web UI:** a dashboard with the message rate, device tiles and a live tail with server-side filters; a devices
  page to rename, group or hide senders; settings that apply without a restart; light and dark modes.
- **Safe by default:** the UI listens on `127.0.0.1:8514` only. LAN mode serves HTTPS with a password. No
  outbound connections, no telemetry, no update checks.
- **Clear errors:** if port 514 is taken, the UI still starts and shows which program holds the port, with a
  Retry button.
- **One binary, one service:** the UI is embedded. Installers for Windows (MSI), macOS (pkg) and Linux (deb,
  rpm) set it up as a system service.

## Quick start

**Portable, nothing installed:** download the archive for your system, unpack it and double-click `woodshed` (or run
`./woodshed`). It listens on UDP and TCP 514, keeps its logs in `woodshed-data/` next to it, and opens the UI. On Linux,
port 514 needs `sudo ./woodshed` (or `sudo setcap cap_net_bind_service=+ep ./woodshed` once). See
[Portable mode](docs/install.md#portable-mode-no-install).

**As a service:** use the installer for your system ([docs/install.md](docs/install.md)).

## Building from source

You need Go (see `go.mod`) and Node.js with npm for the UI.

```sh
# Build the UI and the binary (into dist/woodshed)
make build
```

Run it in the foreground with its own folder. Port 514 needs administrator rights on Linux and is often taken by
the OS's own syslog daemon, so this example uses a high port; the default config listens on 514.

```sh
mkdir -p ./mysyslog
cat > ./mysyslog/woodshed.toml <<'EOF'
[listeners.udp]
enabled = true
addr = "127.0.0.1"
port = 15514

[listeners.tcp]
enabled = true
addr = "127.0.0.1"
port = 15514
EOF
./dist/woodshed run --config ./mysyslog/woodshed.toml
```

In another terminal, send a test message and check the listeners:

```sh
./dist/woodshed send --port 15514 --message "hello from woodshed"
./dist/woodshed status --config ./mysyslog/woodshed.toml
```

Then open <http://localhost:8514/>. The message is also on disk, in
`./mysyslog/woodshed-data/logs/127.0.0.1/<date>.log` (files are named by the UTC date).

Settings missing from the file take their defaults, and a config file that doesn't exist yet is created with
them; either way the data folder is `woodshed-data` next to the config. Stop the server with Ctrl-C: it writes
everything to disk first.

## Commands

| Command | What it does |
|---|---|
| `woodshed` / `woodshed portable [--dir DIR]` | Portable mode: run here with nothing installed, data in `woodshed-data/` |
| `woodshed run [--config FILE]` | Run the server in the foreground |
| `woodshed install` / `uninstall` | Install or remove the system service where no installer package was used (needs administrator rights) |
| `woodshed status` | The service's state, the listeners and the UI address |
| `woodshed reindex` | Rebuild the index from the log files (with the server stopped) |
| `woodshed reset-password` | Forgot the UI password: remove it and turn LAN mode off (run as administrator) |
| `woodshed send` | Send numbered test messages to any syslog server |
| `woodshed bench` | Measure sustained throughput on a temporary in-process server |
| `woodshed version` | Print the version |

`woodshed <command> --help` shows each command's flags.

## Development

```sh
make test              # go vet + go test -race, including the end-to-end test of the real binary
go test -short ./...   # everything except the end-to-end test
cd web && npm run dev:mock   # the UI against mock data
```

## License

MIT. See [LICENSE](LICENSE).
