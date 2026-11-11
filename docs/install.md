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

**Tarball (no package manager):** unpack it, then either run `./woodpile run` in a terminal (as root, or grant
the capability with `sudo setcap cap_net_bind_service=+ep ./woodpile`), or copy `woodpile` to `/usr/bin` and set up
the user, folders and `woodpile.service` from `packaging/linux/` in the source tree the way the package does.

## Checking that it works

1. Open http://127.0.0.1:8514/ on the machine. The welcome page shows the addresses and ports Woodpile is
   listening on, and a test command.
2. Send a test message with `woodpile send` (see `woodpile send --help`), or point a device at the machine's
   address, port 514.
3. If the welcome page says port 514 is in use, another syslog server is already running; stop it, then press
   **Retry**.
