#!/bin/sh
# Woodshed deb/rpm post-remove script (nfpm).
#
# Called as:
#   deb: postrm remove | purge | upgrade <new-version> | disappear | abort-* ...
#   rpm: %postun <count>   (0 = removal, 1+ = upgrade)
#
# Removal drops the sysctl drop-in and keeps the logs and config.
# Only `apt purge` (deb) deletes /var/lib/woodshed and /etc/woodshed.
# The woodshed system user is kept, as Debian and Fedora policy recommend.
set -e

SYSCTL_FILE=/etc/sysctl.d/60-woodshed.conf

case "$1" in
	remove | purge | 0) ;;
	*) exit 0 ;;
esac

# The package manager has normally deleted this already; make sure.
rm -f "$SYSCTL_FILE"
if command -v sysctl >/dev/null 2>&1; then
	# Re-apply the remaining drop-ins. The running kernel keeps the raised
	# rmem_max until reboot unless another file sets it.
	sysctl -q --system >/dev/null 2>&1 || true
fi

if [ "$1" = purge ]; then
	rm -rf /var/lib/woodshed /etc/woodshed
fi

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || true
	systemctl reset-failed woodshed.service >/dev/null 2>&1 || true
fi
exit 0
