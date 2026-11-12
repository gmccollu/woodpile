#!/bin/bash
# Run as root on the sender host from the unpacked bundle (wl setup-sender copies it to
# ~/woodpile-lab-bundle). Adds only /opt/woodpile-lab, one systemd unit and one sudoers
# drop-in; it changes no existing service, netplan or package.
#   sudo bash install-sender.sh SENDER_USER
set -euo pipefail
u=${1:?the sender login that may run wl-netns/wl-send without a password}
here=$(cd "$(dirname "$0")" && pwd)
L=/opt/woodpile-lab
install -d -m 0755 -o root -g root $L $L/bin /var/tmp/woodpile-lab
install -m 0755 -o root -g root "$here/woodshed" "$here/wl-netns" "$here/wl-send" $L/bin/
install -m 0644 -o root -g root "$here/senders.conf" $L/senders.conf
# optional: the only target addresses wl-send / wl-netns may aim at
[ -f "$here/targets.allow" ] && install -m 0644 -o root -g root "$here/targets.allow" $L/targets.allow
install -m 0644 -o root -g root "$here/woodpile-lab-senders.service" /etc/systemd/system/woodpile-lab-senders.service
tmp=$(mktemp)
echo "$u ALL=(root) NOPASSWD: $L/bin/wl-netns, $L/bin/wl-send" >"$tmp"
visudo -cf "$tmp" >/dev/null
install -m 0440 -o root -g root "$tmp" /etc/sudoers.d/woodpile-lab
rm -f "$tmp"
systemctl daemon-reload
systemctl enable woodpile-lab-senders.service >/dev/null 2>&1
systemctl restart woodpile-lab-senders.service
$L/bin/wl-netns status
$L/bin/woodshed version
echo "install-sender: ok"
