# Service contract (what the packages expect from the binary)

The installers in `packaging/` depend on these. Change both sides together.

1. Services start `woodpile run --service --config <path>`. If the config file is missing, the binary creates
   it with defaults. Config and data paths:
   - Linux: config `/etc/woodpile/woodpile.toml`, data `/var/lib/woodpile`.
   - macOS: `/Library/Application Support/Woodpile/` for both.
   - Windows: `C:\ProgramData\Woodpile\` for both.
2. The service names are fixed. Linux: `woodpile.service`. macOS: `com.woodpile.daemon`. Windows SCM:
   `Woodpile` exactly (the data-folder ACL uses the service SID, which is derived from that name). On Windows the
   MSI creates `C:\ProgramData\Woodpile`; the service can't.
3. Service logs:
   - Windows: the Application event log, source `Woodpile` (registered by the MSI).
   - macOS: stdout/stderr go to `/Library/Logs/Woodpile/woodpile.log`, rotated by newsyslog.
   - Linux: journald.
4. `woodpile install` / `uninstall` must use the same names, and must refuse when a package-installed service
   already exists, so a machine never ends up with two services.
5. Windows LAN mode: the MSI opens only 514 UDP/TCP, and the virtual service account can't add firewall rules.
   When LAN mode is turned on, the UI shows the exact admin command that opens the UI port (decided: no extra
   installer feature in v1).
6. `web/package-lock.json` stays committed (CI uses `npm ci`).
7. M4 still has to wire `woodpile bench` and the install smoke tests into CI.
