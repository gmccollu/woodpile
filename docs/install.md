# Installing Woodpile

Woodpile is one program that runs as a background service. Every installer sets up the service, starts it, and
leaves it listening for syslog on **UDP and TCP port 514**. The web UI is at **http://127.0.0.1:8514/** on the same
machine (localhost only until you turn on LAN mode in Settings).

Download the installer for your system from the project's releases page. Replace `X.Y.Z` below with the version.

| System | File |
|---|---|
| Windows 10/11, Server 2016+ (x64, ARM64) | `woodpile_X.Y.Z_windows_amd64.msi` / `..._arm64.msi` |
| macOS 13 or later (Apple silicon and Intel) | `woodpile_X.Y.Z_macos_universal.pkg` |
| Debian, Ubuntu | `woodpile_X.Y.Z_amd64.deb` / `..._arm64.deb` |
| Fedora, RHEL, Rocky, Alma, openSUSE | `woodpile-X.Y.Z-1.x86_64.rpm` / `...aarch64.rpm` |
| Any Linux, or no service | `woodpile_X.Y.Z_linux_amd64.tar.gz` (and `_arm64`, `_darwin_universal`, `_windows_*.zip`) |

To check a download, compare it with `checksums.txt` from the same release:
`sha256sum -c --ignore-missing checksums.txt` (Linux), `shasum -a 256 -c --ignore-missing checksums.txt` (macOS), or
`Get-FileHash woodpile_X.Y.Z_windows_amd64.msi` (PowerShell).

## Where things live

| | Windows | macOS | Linux |
|---|---|---|---|
| Program | `C:\Program Files\Woodpile\woodpile.exe` (added to `PATH`) | `/usr/local/bin/woodpile` | `/usr/bin/woodpile` |
| Config | `C:\ProgramData\Woodpile\woodpile.toml` | `/Library/Application Support/Woodpile/woodpile.toml` | `/etc/woodpile/woodpile.toml` |
| Logs received | `C:\ProgramData\Woodpile\logs\` | `/Library/Application Support/Woodpile/logs/` | `/var/lib/woodpile/logs/` |
| Woodpile's own log | Event Viewer → Windows Logs → Application | `/Library/Logs/Woodpile/woodpile.log` | `journalctl -u woodpile` |
| Service | `Woodpile` (runs as `NT SERVICE\Woodpile`) | launchd `com.woodpile.daemon` (runs as `_woodpile`) | systemd `woodpile.service` (runs as `woodpile`) |

The config file is created with defaults the first time the service starts. Received logs are plain text, one
folder per device and one file per day (older days are gzipped). The data folder is readable only by the service
account and administrators, so open it as an administrator (Windows) or with `sudo` (macOS, Linux).

Upgrades keep the config and data. Uninstalling keeps them too unless you ask for them to be deleted (below).

## Windows

**Install:** double-click the MSI and approve the administrator prompt. Or, silently from an elevated prompt:

```powershell
msiexec /i woodpile_X.Y.Z_windows_amd64.msi /qn
```

The installer:
- installs the `Woodpile` service (automatic start, restarted if it fails) running as the virtual account
  `NT SERVICE\Woodpile`, which can write only to `C:\ProgramData\Woodpile`;
- adds Windows Firewall rules for woodpile.exe on UDP 514 and TCP 514 only. The UI port isn't opened;
- adds a Start menu shortcut, **Woodpile**, that opens the UI.

**Upgrade:** run the newer MSI. It replaces the old version and restarts the service; the config and logs stay.

**Uninstall:** Settings → Apps → Woodpile → Uninstall. This removes the program, service and firewall rules and
**keeps** `C:\ProgramData\Woodpile`. To delete the logs and config as well:

```powershell
msiexec /x woodpile_X.Y.Z_windows_amd64.msi DELETE_LOGS=1
```

or delete `C:\ProgramData\Woodpile` yourself after uninstalling.

**SmartScreen (while installers are unsigned):** the browser may say the file "isn't commonly downloaded"; choose
**Keep**. When you open it, Windows may show "Windows protected your PC"; choose **More info → Run anyway**. Signed
installers will remove both prompts.

**Scoop** (program only, no service): `scoop bucket add woodpile https://github.com/OWNER/scoop-bucket`, then
`scoop install woodpile`, and run `woodpile run` in a terminal.

## macOS

**Install:** double-click the `.pkg` and follow the installer (it needs an administrator password). When it
finishes, the welcome page opens in your browser. From a terminal instead:

```sh
sudo installer -pkg woodpile_X.Y.Z_macos_universal.pkg -target /
```

The installer:
- creates a hidden system user and group, `_woodpile`, that the service runs as;
- installs `/usr/local/bin/woodpile` (one binary for Apple silicon and Intel) and the launchd daemon
  `/Library/LaunchDaemons/com.woodpile.daemon.plist`, which starts at boot and is restarted if it stops;
- creates `/Library/Application Support/Woodpile` and `/Library/Logs/Woodpile`, owned by `_woodpile`.

**Upgrade:** install the newer `.pkg`. The daemon is stopped, replaced and started again; the config and logs stay.

**Uninstall:**

```sh
sudo /usr/local/share/woodpile/uninstall.sh                # keeps the logs and config
sudo /usr/local/share/woodpile/uninstall.sh --delete-logs  # also deletes them and the _woodpile user
```

**Gatekeeper (while the installer is unsigned and not notarized):** macOS says the package "can't be opened"
because Apple can't check it. Click **Done**, open **System Settings → Privacy & Security**, scroll to the message
about the Woodpile package and click **Open Anyway**, then confirm. (On current macOS, Control-click → Open no longer
skips this.) The `sudo installer` command above doesn't show the prompt.

**macOS firewall:** if the application firewall is on (System Settings → Network → Firewall), allow incoming
connections for `/usr/local/bin/woodpile` there (Options… → +) if devices can't reach port 514.

**Homebrew** (program only, no service): `brew install --cask OWNER/tap/woodpile`, then `woodpile run`.

## Linux (deb and rpm)

**Install:**

```sh
sudo apt install ./woodpile_X.Y.Z_amd64.deb        # Debian, Ubuntu
sudo dnf install ./woodpile-X.Y.Z-1.x86_64.rpm     # Fedora, RHEL and derivatives
sudo zypper install ./woodpile-X.Y.Z-1.x86_64.rpm  # openSUSE
```

The package:
- creates the `woodpile` system user and group (no login, no home);
- installs `woodpile.service`, which runs as `woodpile` with only `CAP_NET_BIND_SERVICE` (for port 514), a
  read-only view of the system, and write access to `/var/lib/woodpile` and `/etc/woodpile` only;
- installs `/etc/sysctl.d/60-woodpile.conf`, raising `net.core.rmem_max` to 32 MiB so Woodpile can get the UDP
  receive buffer it asks for, and applies it straight away;
- enables and starts the service, and prints the UI address.

The package doesn't change the host firewall. If one is on, open port 514:

```sh
sudo ufw allow 514                                                  # Ubuntu (ufw)
sudo firewall-cmd --permanent --add-port=514/udp --add-port=514/tcp && sudo firewall-cmd --reload  # firewalld
```

**Upgrade:** install the newer package the same way. The service is restarted (if it was running) and the
config and logs stay.

**Uninstall:**

```sh
sudo apt remove woodpile    # keeps /var/lib/woodpile and /etc/woodpile
sudo apt purge woodpile     # also deletes them
sudo dnf remove woodpile    # keeps them; delete the two folders by hand if you want them gone
```

Removing the package stops and disables the service and deletes the sysctl drop-in. The raised `rmem_max` stays
in effect until the next reboot. The `woodpile` user is left in place, as distribution policy recommends; remove
it with `sudo userdel woodpile` if you want.

**Tarball (no package manager):** unpack it and either run `./woodpile run` in a terminal, or install it as a
service with `sudo ./woodpile install` (next section).

## Without a package: `woodpile install`

For machines where you don't use the installer package (a tarball, a build from source), `woodpile install` sets up
the service the way the package does, and `woodpile uninstall` removes it again. Both need administrator rights
(`sudo`, or an administrator prompt on Windows).

```sh
sudo ./woodpile install              # Linux, macOS
sudo woodpile uninstall              # keeps the logs and config
sudo woodpile uninstall --delete-logs
```

```powershell
.\woodpile.exe install              # elevated PowerShell
woodpile uninstall [--delete-logs]
```

| | Linux | macOS | Windows |
|---|---|---|---|
| Program | copied to `/usr/local/bin/woodpile` | copied to `/usr/local/bin/woodpile` | copied to `C:\Program Files\Woodpile\woodpile.exe` |
| Service | the package's unit, as `/etc/systemd/system/woodpile.service` | the package's plist, `/Library/LaunchDaemons/com.woodpile.daemon.plist` | `Woodpile`, automatic start, restarted on failure |
| Account | `woodpile` system user (created if missing) with only `CAP_NET_BIND_SERVICE` | `_woodpile` user and group, created by the package's own script | `NT SERVICE\Woodpile` |
| Data folder | `/var/lib/woodpile`, owned by `woodpile`, mode 0750 | `/Library/Application Support/Woodpile`, owned by `_woodpile`, mode 0750 | `C:\ProgramData\Woodpile` with the MSI's ACL (below) |
| Also | `/etc/sysctl.d/60-woodpile.conf` (32 MiB UDP buffer) | `/etc/newsyslog.d/com.woodpile.daemon.conf` | the `Woodpile` event source; firewall rules for UDP and TCP 514 |

`--config FILE` points the service at another config; its data folder is the config's `storage.data_dir` (or, for a
new config, the config's own folder).

`install` refuses when the package already installed the service (the unit in `/usr/lib/systemd/system`, the pkg
receipt, or the MSI's `HKLM\Software\Woodpile` key), and `uninstall` refuses to remove a package's service: use the
package to remove it. So a machine never has two Woodpile services. Remove a `woodpile install` service before
installing the package.

`uninstall --delete-logs` deletes the data folder and the config; on macOS it also deletes the `_woodpile` account,
like the pkg's `uninstall.sh --delete-logs`. On Linux the `woodpile` user is kept, as with the package.

**The Windows data-folder ACL** (the same as the MSI's): owner Administrators, inheritance from ProgramData turned
off, full control for SYSTEM and Administrators, and modify (read, write, delete; no permission changes) for
`NT SERVICE\Woodpile`. Ordinary users get no access. As SDDL:

```
O:BAG:SYD:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1301bf;;;S-1-5-80-276366627-2944175572-1807786847-528126453-1705305312)
```

`woodpile install` sets it with `SetNamedSecurityInfo`. To set it by hand (an elevated prompt):

```powershell
$acl = Get-Acl C:\ProgramData\Woodpile
$acl.SetSecurityDescriptorSddlForm("O:BAG:SYD:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1301bf;;;S-1-5-80-276366627-2944175572-1807786847-528126453-1705305312)")
Set-Acl C:\ProgramData\Woodpile $acl
```

Or with icacls, which gives the same DACL (`M` is 0x1301bf) but leaves the owner as it is:
`icacls C:\ProgramData\Woodpile /inheritance:r /grant:r "*S-1-5-18:(OI)(CI)F" "*S-1-5-32-544:(OI)(CI)F" "NT SERVICE\Woodpile:(OI)(CI)M"`.

## Files in the data folder

| File | What it is |
|---|---|
| `logs/<device>/<day>.log`, `.log.gz` | the received messages; plain text, one line each |
| `index.db` (+ `-wal`, `-shm`) | the SQLite index; `woodpile reindex` rebuilds it from the logs |
| `woodpile.toml` | the config (Linux: `/etc/woodpile/woodpile.toml` instead) |
| `woodpile.lock` | locked while a Woodpile process uses the folder, so two servers (or a server and `reindex`) never share it. The lock is the OS's and goes away with the process, even after a crash; the file itself stays and holds the last PID |
| `woodpile.state.json` | written by the running server (PID, version, UI address and any UI error) for `woodpile status`; deleted at a clean stop |
| `ui-password.hash`, `ui-cert.pem`, `ui-key.pem` | LAN mode's password hash and self-signed certificate (mode 0600) |
| `exports/` | export zips, deleted after 24 hours |

## Checking that it works

1. Open http://127.0.0.1:8514/ on the machine. The welcome page shows the addresses and ports Woodpile is
   listening on, and a test command.
2. Send a test message with `woodpile send --message "hello"` (see `woodpile send --help`), or point a device at the
   machine's address, port 514.
3. `woodpile status` shows the service's state, both listeners and the UI address.

## Troubleshooting

**Port 514 is in use.** Another syslog server (often the OS's own: rsyslog, syslog-ng) is listening. The UI still
starts and shows a red banner naming the port, the error and, where the OS says, the program holding it; `woodpile
status` shows the same. Find it yourself with `sudo ss -lunp 'sport = :514'` and `sudo ss -ltnp 'sport = :514'`
(Linux), `sudo lsof -nP -i :514` (macOS) or `Get-NetUDPEndpoint -LocalPort 514; Get-NetTCPConnection -LocalPort 514`
(PowerShell). Stop that program, or move Woodpile to another port in Settings, then press **Retry** in the banner.
No restart is needed.

**Port 8514 (the UI) is in use.** The service keeps running and retries; `woodpile status` says the UI isn't
answering and why. Change `ui.port` in the config and restart the service.

**Devices can't reach it.** Check the host firewall: the MSI and `woodpile install` on Windows open UDP and TCP 514
only; the Linux packages change no firewall (`sudo ufw allow 514`, or the firewalld command above); on macOS allow
`/usr/local/bin/woodpile` in the application firewall if it's on. The UI port is never opened: it's localhost-only
unless you turn on LAN mode (on Windows the UI then shows the admin command that opens it).

**"another Woodpile process is using this data folder".** A server already runs on that folder (`woodpile status`
says which); stop it first. `woodpile reindex` refuses for the same reason while the service runs.

**Gatekeeper or SmartScreen blocks the installer.** The installers aren't signed yet: see the macOS and Windows
sections above for the one-time prompts.

**Forgot the UI password.** `sudo woodpile reset-password` (an administrator prompt on Windows) removes it and turns
LAN mode off; the UI is back on http://localhost:8514/.

**Where's Woodpile's own log?** Windows: Event Viewer → Windows Logs → Application, source `Woodpile`. macOS:
`/Library/Logs/Woodpile/woodpile.log`. Linux: `journalctl -u woodpile`.

## How the installers are tested

`.github/workflows/install-smoke.yml` installs every package for real on fresh GitHub-hosted machines
(scripts in `scripts/smoke/`): the deb on Ubuntu, the rpm in a Fedora container running systemd, the MSI on Windows
Server and the pkg on macOS, then `woodpile install` on each. Every run checks that the service runs as its
unprivileged account and holds UDP and TCP 514 (on macOS as `_woodpile`, on Linux with only `CAP_NET_BIND_SERVICE`),
that the UI answers on 8514, that 10,000 messages sent with `woodpile send` are all on disk exactly once, that
`woodpile status` works, and that a restart, an upgrade (Windows, 0.0.1 → 0.0.2) and an uninstall keep the data
while the delete-logs paths (`apt purge`, `DELETE_LOGS=1`, `uninstall.sh --delete-logs`, `woodpile uninstall
--delete-logs`) remove it. On Windows it also checks the data-folder ACL, the firewall rules and the event log.
