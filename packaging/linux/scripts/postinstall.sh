#!/bin/sh
# Woodpile deb/rpm post-install script (nfpm).
#
# Called as:
#   deb: postinst configure <old-version>   (old-version is empty on a first install)
#        postinst abort-upgrade|abort-remove|abort-deconfigure ...
#   rpm: %post <count>                        (1 = first install, 2+ = upgrade)
#
# A first install creates the user and directories, applies the sysctl drop-in,
# and enables and starts the service. An upgrade only restarts a running service.
set -e

SERVICE=woodpile.service
USER_NAME=woodpile
DATA_DIR=/var/lib/woodpile
CONF_DIR=/etc/woodpile
SYSCTL_FILE=/etc/sysctl.d/60-woodpile.conf
UI_URL=http://127.0.0.1:8514/

action=install
case "$1" in
	configure)
		if [ -n "$2" ]; then action=upgrade; fi
		;;
	abort-upgrade | abort-remove | abort-deconfigure)
		action=upgrade
		;;
	*)
		if [ "$1" -ge 2 ] 2>/dev/null; then action=upgrade; fi
		;;
esac

# The system user and group (no home, no login shell).
if ! getent group "$USER_NAME" >/dev/null 2>&1; then
	groupadd --system "$USER_NAME"
fi
if ! getent passwd "$USER_NAME" >/dev/null 2>&1; then
	nologin=/usr/sbin/nologin
	[ -x "$nologin" ] || nologin=/sbin/nologin
	[ -x "$nologin" ] || nologin=/bin/false
	useradd --system --gid "$USER_NAME" --home-dir "$DATA_DIR" --no-create-home \
		--shell "$nologin" --comment "Woodpile syslog server" "$USER_NAME"
fi

# Data and config directories. Existing ones are left as the admin set them.
for dir in "$DATA_DIR" "$CONF_DIR"; do
	if [ ! -d "$dir" ]; then
		install -d -m 0750 -o "$USER_NAME" -g "$USER_NAME" "$dir"
	fi
done

# Raise net.core.rmem_max now; the drop-in keeps it across reboots.
if [ -f "$SYSCTL_FILE" ] && command -v sysctl >/dev/null 2>&1; then
	sysctl -q -p "$SYSCTL_FILE" >/dev/null 2>&1 ||
		echo "woodpile: could not apply $SYSCTL_FILE now (read-only /proc/sys?); it applies at next boot." >&2
fi

if [ ! -d /run/systemd/system ]; then
	# Containers and chroots: no running systemd to talk to.
	if command -v systemctl >/dev/null 2>&1; then
		systemctl enable "$SERVICE" >/dev/null 2>&1 || true
	fi
	echo "woodpile: systemd is not running; start it with: woodpile run --config $CONF_DIR/woodpile.toml"
	exit 0
fi

systemctl daemon-reload >/dev/null 2>&1 || true

if [ "$action" = upgrade ]; then
	# Keep the admin's choice: restart only if it was running.
	systemctl try-restart "$SERVICE" >/dev/null 2>&1 || true
	echo "woodpile: upgraded; a running service was restarted."
	exit 0
fi

systemctl enable "$SERVICE" >/dev/null 2>&1 || true
if systemctl start "$SERVICE"; then
	echo ""
	echo "Woodpile is running and listening for syslog on UDP and TCP port 514."
	echo "Open $UI_URL in a browser on this machine."
	echo "Config: $CONF_DIR/woodpile.toml   Logs: $DATA_DIR/logs   Service log: journalctl -u woodpile"
	echo ""
else
	echo "woodpile: the service did not start; see: journalctl -u woodpile" >&2
fi
exit 0
