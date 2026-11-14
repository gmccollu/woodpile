# Woodshed user guide

Woodshed collects the log messages (syslog) that your devices send, keeps them as plain files on this computer, and
shows them in a web page where you can watch, search and export them. This page gets you from download to a support
zip. For details, see the [install guide](install.md) and the [FAQ](../FAQ.md).

Downloads are on the [releases page](https://github.com/gmccollu/woodshed/releases).

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

## 5. Events: browse and filter everything stored

**Events** shows every stored message, not just the live tail: all devices, all days, older compressed days
included. It opens on the last hour, newest first. Leave the text box empty to browse, or narrow it down:

- **Text**: lines containing it. The switch beside the box says how it's read (see below); **Aa Case** matches upper
  and lower case exactly.
- **Program/tag**: the program or tag in the message header, such as `sshd` or `kernel`. Separate several with
  commas. End one with `*` to match the start (`ssh*`, `%FW-6-*`); the leading `%` of a tag is optional.
- **Host**: the hostname in the message header, such as `web-01`. To pick by sender address, use the device menu.
- **Exclude**: leave out lines matching this. The Text, Terms or Regex switch and Case apply here too.
- **Devices**, **time range**, **severities** and **oldest or newest first**.

Press **Search** (or Enter) to apply. Click a value in the **Host** or **Program/tag** column to add it to that
filter. Click a line to see the lines around it.

### Text, Terms or Regex

- **Text** (the default): the box is one piece of text, found anywhere in the message. `deny tcp` finds exactly
  that phrase.
- **Terms**: words and `"quoted phrases"`, each found anywhere in the message, joined by `AND`, `OR` and `NOT`.
  Two words side by side mean AND. `NOT` applies to the term right after it, then AND, then OR, so
  `a OR b c` means "a, or both b and c". Use `( )` to group: `deny AND (tcp OR udp) NOT 10.0.0.1`.
  - Only upper-case `AND`, `OR` and `NOT` are operators; `and`, `or` and `not` are ordinary words. To search for the
    word AND itself, quote it: `"AND"`.
  - Quote a term that has spaces or brackets in it: `"(pid 312)"`. Inside quotes, write `\"` for a quote and
    `\\` for a backslash. A quote inside a word is ordinary, so `user="bob"` is one word.
  - `NOT x` on its own finds every line without x.
  - Up to 32 words and phrases and 8 levels of brackets. A mistake is shown under the box with its position, for
    example `Missing ")" for the "(" at character 6.`
  - Only the words and phrases that aren't under a NOT are highlighted.
- **Regex**: a regular expression (RE2 syntax), such as `deny (tcp|udp)`.

### Fields

The **Fields** panel beside the results counts, for the current search and time range, the values of each field:
device, severity, facility, host and program/tag. Each field shows its top 10 values (severity and facility show
every value that appears), then how many others there are.

- Click a value to show only lines with it. Values of one field add up (this host or that one); different fields
  narrow each other (this host and this program).
- **Alt-click** (Option-click on a Mac) a value, or press its **−** button, to leave it out instead. The **+** button
  does the same as a click, for touch screens and the keyboard.
- The filters you've added are shown as chips at the top of the panel. Click one to remove it.
- **(none)** counts lines with no hostname or program/tag in their header. It can't be clicked: no filter picks
  those lines yet.

The counts are exact, never estimated. Woodshed reads the matching lines newest first and says how much it has
counted so far: "Counted 14:02–15:00 of 12:00–15:00" means every line from 14:02 on is counted. It keeps counting
for up to a minute of scanning; on a long range over busy devices it then stops and offers **Count the rest**. A
narrower range, or fewer devices, makes it faster.

### Histogram

Above the results, a histogram shows the matching lines over time. The bar size depends on the range: 1-minute bars
for the last hour, 15-minute bars for 24 hours, 2-hour bars for 7 days. Hover over a bar to see its time and count.

- **Drag across the bars** (or click one) to search just that stretch of time. Your browser's **Back** button zooms out
  again.
- Without text, program/tag, host, facility or severity filters, the histogram comes straight from the index and is
  complete at once ("from the index"). Bars that are estimated from the index say "≈" when you hover over them.
- With filters, the histogram fills in as the lines are counted, newest first. Bars that aren't counted yet are
  hatched.

### Saved searches

The **Saved** menu keeps searches on the server, so everyone who can open this Woodshed sees the same list.

- **Save as…** names the current search (up to 80 characters; names are unique, ignoring case). It keeps the text and
  how it's read, the devices, filters, severities, order and time range. A quick pick like "1 h" stays relative: it
  means the last hour whenever you open it. The raw view and line wrapping aren't saved.
- Click a saved search to open it. The form shows its name, and **changed** once you edit something; **Save** then
  updates it.
- Each saved search has buttons to copy a link to it, rename it and delete it.
- A saved search never changes behind your back. If a device it names is no longer known (forgotten, say), it's shown
  struck through as "no longer known" and the search runs with the rest. A device that sends again is found again.
- They're kept in Woodshed's index, which `woodshed reindex` keeps. Up to 200 can be saved.

The search, including the time range you dragged to and the saved search you opened, is part of the page's address,
so you can bookmark a view or send the link to someone. Links to the old **Search** page (`#/search`) still work.

From the Dashboard, **Open in Events** on the live tail opens Events with the same devices, severities and text,
over the last hour.

## 6. Export logs for support

To send logs to someone (a colleague, a vendor's support team):

1. In **Events**, click **Export these results** (the zip holds what the page shows, filters included); or open
   **Export** and pick the devices, the time range and any filters.
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
