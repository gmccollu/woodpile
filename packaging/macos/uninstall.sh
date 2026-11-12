#!/bin/sh
# Uninstalls Woodshed from macOS. Installed as /usr/local/share/woodshed/uninstall.sh.
#
#   sudo /usr/local/share/woodshed/uninstall.sh                 # keeps the logs and config
#   sudo /usr/local/share/woodshed/uninstall.sh --delete-logs   # also deletes them and the _woodshed account
set -e

LABEL=com.woodshed.daemon
PLIST=/Library/LaunchDaemons/$LABEL.plist
PKG_ID=com.woodshed.pkg
SVC_USER=_woodshed
SVC_GROUP=_woodshed
DATA_DIR="/Library/Application Support/Woodshed"
LOG_DIR=/Library/Logs/Woodshed

delete_logs=0
for arg in "$@"; do
	case "$arg" in
		--delete-logs) delete_logs=1 ;;
		-h | --help)
			sed -n '2,6p' "$0" | sed 's/^# \{0,1\}//'
			exit 0
			;;
		*)
			echo "uninstall.sh: unknown option $arg (use --delete-logs or --help)" >&2
			exit 2
			;;
	esac
done

if [ "$(id -u)" -ne 0 ]; then
	echo "uninstall.sh: run it with sudo" >&2
	exit 1
fi

if launchctl print "system/$LABEL" >/dev/null 2>&1; then
	echo "Stopping the Woodshed daemon"
	launchctl bootout "system/$LABEL" 2>/dev/null || true
fi

echo "Removing the program files"
rm -f "$PLIST" \
	/usr/local/bin/woodshed \
	/etc/newsyslog.d/com.woodshed.daemon.conf
rm -rf /usr/local/share/woodshed /Applications/Woodshed.app
pkgutil --forget "$PKG_ID" >/dev/null 2>&1 || true

if [ "$delete_logs" -eq 1 ]; then
	echo "Deleting $DATA_DIR and $LOG_DIR"
	rm -rf "$DATA_DIR" "$LOG_DIR"
	if dscl . -read "/Users/$SVC_USER" >/dev/null 2>&1; then
		dscl . -delete "/Users/$SVC_USER"
	fi
	if dscl . -read "/Groups/$SVC_GROUP" >/dev/null 2>&1; then
		dscl . -delete "/Groups/$SVC_GROUP"
	fi
	echo "Woodshed is uninstalled, and its logs and config are deleted."
else
	echo "Woodshed is uninstalled. Logs and config are kept in:"
	echo "  $DATA_DIR"
	echo "  $LOG_DIR"
	echo "To delete them later: sudo rm -rf \"$DATA_DIR\" \"$LOG_DIR\""
fi
