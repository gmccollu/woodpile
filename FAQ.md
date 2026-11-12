# Woodshed FAQ

New to Woodshed? Start with the [user guide](docs/user-guide.md). Installer details are in
[docs/install.md](docs/install.md).

- [Why port 514, and why UDP?](#why-port-514-and-why-udp)
- [Devices can't reach Woodshed](#devices-cant-reach-woodshed)
- [What does the installer change in the firewall?](#what-does-the-installer-change-in-the-firewall)
- [Windows or macOS warns me about the download](#windows-or-macos-warns-me-about-the-download)
- [Where are the logs?](#where-are-the-logs)
- [How much disk does it use, and how long does it keep logs?](#how-much-disk-does-it-use-and-how-long-does-it-keep-logs)
- [Can I open the UI from another computer?](#can-i-open-the-ui-from-another-computer)
- [I forgot the UI password](#i-forgot-the-ui-password)
- [How do I uninstall it?](#how-do-i-uninstall-it)
- [Does Woodshed connect to the internet?](#does-woodshed-connect-to-the-internet)

## Why port 514, and why UDP?

514 is the standard syslog port, and it's what almost every device sends to by default, so Woodshed works without
changing anything on the device. Most devices send syslog over UDP: it's simple and doesn't slow the device down,
but a message lost on the network isn't resent. Woodshed also listens on TCP 514 for devices that offer it
(more reliable, in order). Both are on by default; you can change the ports in **Settings → Listeners**.

If something else already uses port 514 (often the computer's own syslog service), Woodshed still starts and the UI
shows a red banner saying which program has the port. Stop that program, or move Woodshed to another port, then press
**Retry**.

## Devices can't reach Woodshed

1. Send a test from the Woodshed computer itself: `woodshed send --message "hello"`. If that shows up, Woodshed works
   and the problem is between the device and this computer.
2. Check the address: the device must use one of the addresses on the **Setup** page, port 514, UDP or TCP as set on
   the device.
3. Check the firewall (next question). On Linux the packages don't open any port: run `sudo ufw allow 514` (Ubuntu)
   or `sudo firewall-cmd --permanent --add-port=514/udp --add-port=514/tcp && sudo firewall-cmd --reload`.
4. Check **Settings → Listeners → Sender allowlist**: if it lists addresses, other senders are refused (and counted
   under "Refused senders").

## What does the installer change in the firewall?

- **Windows:** the MSI (and `woodshed install`) adds two inbound rules for `woodshed.exe`, **Woodshed syslog (UDP
  514)** and **Woodshed syslog (TCP 514)**, and nothing else. The UI port (8514) is never opened.
  The rules apply to all network profiles, including Public: Windows often calls a network "Public" just because
  it hasn't been told otherwise, and devices on such a network would otherwise be silently blocked. To allow syslog
  only on Domain and Private networks, run this in an administrator PowerShell:

  ```powershell
  Get-NetFirewallRule -DisplayName "Woodshed syslog (*)" | Set-NetFirewallRule -Profile Domain,Private
  ```

  An upgrade or repair of the MSI re-creates the rules for all profiles, so run it again after one.
- **macOS:** nothing. If the macOS firewall is on, allow `/usr/local/bin/woodshed` in **System Settings → Network →
  Firewall → Options**.
- **Linux:** nothing (see the commands above).
- **Portable mode:** nothing is installed. On Windows, the first run asks whether to allow Woodshed through the
  firewall; choose **Allow**.

## Windows or macOS warns me about the download

Woodshed's downloads aren't code-signed yet, so the first time you open one:

- **Windows (SmartScreen):** your browser may say the file "isn't commonly downloaded"; choose **Keep**. When you open
  it, "Windows protected your PC" appears: click **More info**, then **Run anyway**.
- **macOS (Gatekeeper):** it says the file "can't be opened" because Apple can't check it. Click **Done**, open
  **System Settings → Privacy & Security**, scroll down to the message about Woodshed and click **Open Anyway**, then
  confirm. (Control-click → Open no longer skips this on current macOS.)

You only need to do this once per download. To check a download hasn't been changed, compare it with `checksums.txt`
on the same release page: `shasum -a 256 FILE` (macOS), `sha256sum FILE` (Linux) or `Get-FileHash FILE` (Windows).

## Where are the logs?

As plain text files, one folder per device and one file per day (dates in UTC), with older days compressed:

| | Installed | Portable |
|---|---|---|
| Windows | `C:\ProgramData\Woodshed\logs\` | `woodshed-data\logs\` next to `woodshed.exe` |
| macOS | `/Library/Application Support/Woodshed/logs/` | `woodshed-data/logs/` next to `woodshed` |
| Linux | `/var/lib/woodshed/logs/` | `woodshed-data/logs/` next to `woodshed` |

The installed folders are readable only by the service and administrators: open them as an administrator, or with
`sudo`. Each line is the time Woodshed received the message, the sender's address and the message exactly as it
arrived. Any text tool can read them; `.log.gz` files open with any unzip tool. To hand logs to someone, use
**Export** in the UI rather than copying files: the zip also says when Woodshed wasn't running.

Woodshed's own log (start-up errors and the like): Windows Event Viewer → Windows Logs → Application, source
`Woodshed`; macOS `/Library/Logs/Woodshed/woodshed.log`; Linux `journalctl -u woodshed`; portable mode prints to its
window.

## How much disk does it use, and how long does it keep logs?

By default it keeps 30 days or 20 GB of logs, whichever comes first, deleting the oldest days first. Change both
in **Settings → Storage**. It also never fills the disk: it keeps 5% of the disk free (at least 1 GB, at most 10 GB),
deleting the oldest logs if it has to, and if there's nothing left to delete it pauses writing and shows a red
banner, counting what it couldn't store. Every deletion is listed in **Settings → Storage history**. The dashboard
shows how much disk the logs use and how many days that covers.

## Can I open the UI from another computer?

Not by default: the UI answers only on the computer Woodshed runs on (`http://localhost:8514/`). To open it from
other computers, turn on **LAN mode** in **Settings → UI access**:

1. Set a password first (in the same section).
2. Turn on **LAN mode** and save. The UI moves to HTTPS at `https://<this computer's address>:8514/`, and every
   visitor has to log in.
3. Your browser warns about the certificate the first time, because Woodshed makes its own; you can load your own
   certificate under **Advanced** instead.
4. On Windows, the UI port also needs a firewall rule; Settings shows the exact command to run as an administrator.

Anyone who can reach that address sees the login page, and with the password, all of your logs, so use a strong one.
After 10 wrong passwords, logins are refused for a while.

## I forgot the UI password

On the Woodshed computer, as an administrator, run:

```sh
sudo woodshed reset-password            # macOS, Linux
woodshed reset-password                 # Windows, in an administrator prompt
```

It removes the password, turns LAN mode off and restarts the service, so the UI is back on
`http://localhost:8514/` on that computer. Set a new password in Settings to turn LAN mode on again. There's no way to
do this over the network, on purpose.

## How do I uninstall it?

Uninstalling removes the program and the service but keeps your logs and settings, unless you ask for them to be
deleted.

- **Windows:** Settings → Apps → Woodshed → Uninstall (keeps the logs). To delete them too:
  `msiexec /x woodshed_X.Y.Z_windows_amd64.msi DELETE_LOGS=1`, or delete `C:\ProgramData\Woodshed` afterwards.
- **macOS:** `sudo /usr/local/share/woodshed/uninstall.sh` (keeps the logs) or add `--delete-logs`.
- **Linux:** `sudo apt remove woodshed` (keeps the logs) or `sudo apt purge woodshed` (deletes them);
  `sudo dnf remove woodshed` keeps them (delete `/var/lib/woodshed` and `/etc/woodshed` yourself).
- **Installed with `woodshed install`:** `woodshed uninstall`, or `woodshed uninstall --delete-logs`, as an
  administrator.
- **Portable:** close it and delete its folder (the logs are in `woodshed-data`).

## Does Woodshed connect to the internet?

No. It makes no outbound connections at all: no telemetry, no update checks. The version is on the **About** page,
with a link to the releases page if you want to check for a newer one yourself.
