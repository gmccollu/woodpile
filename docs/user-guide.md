# Woodshed user guide

Woodshed collects the log messages (syslog) that your devices send, keeps them as plain files on this computer, and
shows them in a web page where you can watch, search and export them. This page gets you from download to a support
zip. For details, see the [install guide](install.md) and the [FAQ](../FAQ.md).

Downloads are on the [releases page](https://github.com/gmccollu/woodshed/releases/latest).

## 1. Start Woodshed

Pick one: the installer keeps Woodshed running in the background, including after a restart; portable runs it only
while its window is open, with nothing installed (good for a quick look or a laptop on site).

| | Installer (runs as a service) | Portable (nothing installed) |
|---|---|---|
| **Windows** | Download `woodshed_X.Y.Z_windows_amd64.msi` (`_arm64` for ARM PCs) and double-click it. | Download `woodshed_X.Y.Z_windows_amd64.zip`, right-click it → **Extract All**, then double-click `woodshed.exe` in the extracted folder. |
| **macOS** | Download `woodshed_X.Y.Z_macos_universal.pkg` and double-click it. | Download `woodshed_X.Y.Z_darwin_universal.tar.gz`, double-click it to unpack, then double-click `woodshed`. |
| **Linux** | `sudo apt install ./woodshed_X.Y.Z_amd64.deb` (Debian, Ubuntu) or `sudo dnf install ./woodshed-X.Y.Z-1.x86_64.rpm` (Fedora, RHEL). | `tar xzf woodshed_X.Y.Z_linux_amd64.tar.gz`, then `cd` into the folder and run `sudo ./woodshed`. |

The first time, your computer may warn you because Woodshed isn't signed yet:

- **Windows:** "Windows protected your PC" → **More info** → **Run anyway**. If asked whether to allow Woodshed
  through the firewall, choose **Allow** (portable mode only; the installer sets the firewall up itself).
- **macOS:** if it says it "can't be opened", open **System Settings → Privacy & Security**, scroll down and click
  **Open Anyway**.

Then open <http://localhost:8514/> in a browser on the same computer. (Portable mode opens it for you.) Later, the installer's
launcher gets you back there: **Woodshed** in the Start menu (Windows), in Applications or Launchpad (macOS), or in
your desktop's app menu (Linux). The
**Setup** page shows the addresses your devices should send to and whether Woodshed is listening.

## 2. Point a device at it

In each device's logging or syslog settings, set the syslog server to one of the addresses on the Setup page,
port 514, UDP (or TCP if the device offers it). Save, and make the device log something (log in to it, or change
a setting). The Setup page says when the first message arrives.

No device handy? Send a test message from the Woodshed computer:

```sh
woodshed send --message "hello"          # in the portable folder: ./woodshed send ... (woodshed.exe on Windows)
```

If nothing arrives, see [Devices can't reach it](../FAQ.md#devices-cant-reach-woodshed) in the FAQ.

## 3. Watch

The **Dashboard** shows each device as a tile with a coloured dot (green: heard from in the last 5 minutes, amber:
quiet, red: not heard from in an hour), the message rate and disk use, and a **live tail** of the newest messages.
Click a device to follow only that one (click it again to see all), or type in the box at the top to filter
the devices and messages. On the **Devices** page you can give a device a friendly name and a group.

## 4. Find the log files

Every message is kept, exactly as it arrived, in plain text: one folder per device and one file per day (UTC).
Older days are compressed (`.log.gz`).

| | Installer | Portable |
|---|---|---|
| Windows | `C:\ProgramData\Woodshed\logs\` (open it as an administrator) | `woodshed-data\logs\` next to `woodshed.exe` |
| macOS | `/Library/Application Support/Woodshed/logs/` (needs `sudo`) | `woodshed-data/logs/` next to `woodshed` |
| Linux | `/var/lib/woodshed/logs/` (needs `sudo`) | `woodshed-data/logs/` next to `woodshed` |

By default Woodshed keeps 30 days or 20 GB, whichever comes first, and never fills the disk. Change it in
**Settings → Storage**.

## 5. Search

Open **Search**, enter some text, pick the time range and, if you like, the
devices and severities, then press **Search**. Results stream in, newest first. Click **Context** on a result to see
the lines around it. Turn on **.\* Regex** for regular expressions and **Aa Case** to match upper and lower case
exactly.

## 6. Export logs for support

To send logs to someone (a colleague, a vendor's support team):

1. From a search, click **Export these results**; or open **Export**, pick the devices and the time range.
2. Click **Build the zip**, wait until it's ready, then click **Download**.
3. Attach the zip to your email or ticket.

The zip holds the log lines (plain text, one file per device per day), a manifest with line counts and checksums,
Woodshed's own events for that time (so the reader can tell "the device sent nothing" from "Woodshed wasn't
running"), and a short README. Exports are deleted after 24 hours.

## 7. Stop or remove it

- **Portable:** close its window (or press Ctrl-C). Your logs stay in `woodshed-data`.
- **Installer:** see [Uninstall](../FAQ.md#how-do-i-uninstall-it) in the FAQ. Uninstalling keeps your logs unless
  you ask for them to be deleted.

Other machines can't open this page unless you turn on **LAN mode** (Settings → UI access), which needs a password
and uses HTTPS. See the [FAQ](../FAQ.md#can-i-open-the-ui-from-another-computer).
