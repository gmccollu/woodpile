#!/bin/sh
# Woodpile deb/rpm pre-remove script (nfpm).
#
# Called as:
#   deb: prerm remove | upgrade <new-version> | deconfigure ... | failed-upgrade ...
#   rpm: %preun <count>    (0 = removal, 1+ = upgrade)
#
# Only a real removal stops and disables the service. On upgrade the service keeps
# running and the new package's postinstall restarts it.
set -e

SERVICE=woodpile.service

case "$1" in
	remove | deconfigure | 0) ;;
	*) exit 0 ;;
esac

if [ -d /run/systemd/system ]; then
	systemctl stop "$SERVICE" >/dev/null 2>&1 || true
fi
if command -v systemctl >/dev/null 2>&1; then
	systemctl disable "$SERVICE" >/dev/null 2>&1 || true
fi
exit 0
