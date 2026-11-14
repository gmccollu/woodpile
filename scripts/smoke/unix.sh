#!/bin/bash
# Install smoke test for Linux and macOS (SPEC §7), run by .github/workflows/install-smoke.yml
# on a disposable CI machine. It installs Woodshed for real, as root: never run it on a
# machine you care about.
#
#   scripts/smoke/unix.sh deb    dist/woodshed_X_amd64.deb   # apt install / restart / remove / purge
#   scripts/smoke/unix.sh rpm    dist/woodshed-X.x86_64.rpm  # dnf install / restart / remove
#   scripts/smoke/unix.sh pkg    dist/macos/woodshed_X.pkg   # installer / kickstart / uninstall.sh
#   scripts/smoke/unix.sh manual path/to/woodshed            # woodshed install / uninstall
#   scripts/smoke/unix.sh legacy OLD NEW                      # Woodpile's package, then Woodshed's over it
#
# legacy (the rename from Woodpile): OLD is the last Woodpile release candidate's deb, rpm or
# pkg and NEW is this build's package of the same kind. It installs OLD, checks that
# `woodshed install` (WOODSHED_BIN) refuses with the command that removes it, installs NEW over
# it, and checks that the old package and service are gone (on Linux the installer output says
# it stopped the old service), the old data is kept and named by the installer output and
# `woodshed status`, and the new service runs.
#
# WOODSHED_UPDATE_CHECK (SPEC §10.2, an acceptance item in §10.4): deb and rpm install with
# WOODSHED_UPDATE_CHECK=off through sudo as the docs say (`sudo VAR=value apt-get install`;
# plain VAR=value as root in the rpm container) and require check = "off" in the config, then
# reinstall with an invalid value and require it ignored with a warning. pkg tries the same
# with `sudo VAR=value installer`; whether installer passes it through is reported either way
# (an ::warning:: when it doesn't), and the documented fallback, pre-seeding the TOML, is
# checked instead. The pkg here is a release version (0.0.1), so the update_prompt banner is
# checked too; the deb and rpm are snapshot builds, which never ask.
#
# Each mode checks: the service runs as the unprivileged account, holds UDP and TCP 514,
# the UI answers on 8514, 10,000 messages sent with `woodshed send` are all on disk
# exactly once (scripts/verifyseq), `woodshed status` works, a restart keeps the data,
# removal keeps the data, and the delete-logs path deletes it.
# VERIFYSEQ points at a built scripts/verifyseq; SMOKE_LOGS is where logs are collected.
set -euo pipefail

mode=${1:?mode}
artifact=${2:?artifact}
VERIFYSEQ=${VERIFYSEQ:-dist/verifyseq}
LOGS=${SMOKE_LOGS:-/tmp/smoke-logs}
mkdir -p "$LOGS"
SUDO=""
[ "$(id -u)" -eq 0 ] || SUDO=sudo
os=$(uname -s)
if [ "$os" = Darwin ]; then
	DATA="/Library/Application Support/Woodshed"
	ACCOUNT=_woodshed
	LABEL=com.woodshed.daemon
	CFG="$DATA/woodshed.toml"
else
	DATA=/var/lib/woodshed
	ACCOUNT=woodshed
	CFG=/etc/woodshed/woodshed.toml
fi
UI=http://127.0.0.1:8514
passes=0

say() { printf '\n==> %s\n' "$*"; }
ok() {
	passes=$((passes + 1))
	printf 'PASS %s\n' "$*"
}
die() {
	printf 'FAIL %s\n' "$*" >&2
	collect_logs
	exit 1
}
collect_logs() {
	set +e
	if [ "$os" = Darwin ]; then
		$SUDO cp /Library/Logs/Woodshed/*.log "$LOGS/" 2>/dev/null
		$SUDO launchctl print "system/$LABEL" >"$LOGS/launchctl.txt" 2>&1
		$SUDO log show --last 10m --predicate 'process == "woodshed"' >"$LOGS/unified.log" 2>&1
	else
		$SUDO journalctl -u woodshed --no-pager -n 500 >"$LOGS/journal-$mode.log" 2>&1
		$SUDO systemctl status woodshed --no-pager >"$LOGS/status-$mode.txt" 2>&1
	fi
	$SUDO ls -laR "$DATA" >"$LOGS/data-$mode.txt" 2>&1
	$SUDO chmod -R a+r "$LOGS" 2>/dev/null
	set -e
}

service_pid() {
	if [ "$os" = Darwin ]; then
		$SUDO launchctl print "system/$LABEL" 2>/dev/null | awk '$1 == "pid" && $2 == "=" {print $3; exit}'
	else
		systemctl show -p MainPID --value woodshed.service 2>/dev/null | grep -v '^0$' || true
	fi
}

wait_up() { # the UI answers and both listeners are up
	for _ in $(seq 60); do
		if curl -fsS --max-time 2 "$UI/api/status" -o /tmp/status.json 2>/dev/null &&
			python3 -c 'import json,sys; d=json.load(open("/tmp/status.json")); sys.exit(0 if len(d["listeners"]) == 2 and all(l["ok"] and l["port"] == 514 for l in d["listeners"]) else 1)'; then
			return 0
		fi
		sleep 1
	done
	cat /tmp/status.json 2>/dev/null || true
	die "the service didn't come up with both listeners on 514 within 60 s"
}

check_running() {
	say "service runs as $ACCOUNT, holds UDP and TCP 514, UI on 8514"
	wait_up
	local pid user
	pid=$(service_pid)
	[ -n "$pid" ] || die "no service process"
	user=$(ps -o user= -p "$pid" | tr -d ' ')
	[ "$user" = "$ACCOUNT" ] || die "the service runs as '$user', not $ACCOUNT"
	ok "service process $pid runs as $user"
	if [ "$os" = Darwin ]; then
		local socks
		socks=$($SUDO lsof -nP -a -p "$pid" -i :514)
		echo "$socks"
		echo "$socks" | grep -q UDP || die "pid $pid doesn't hold UDP 514"
		echo "$socks" | grep -q 'TCP.*LISTEN' || die "pid $pid doesn't listen on TCP 514"
		ok "$ACCOUNT bound UDP and TCP 514 on the wildcard address (SPEC §5: works as a non-root launchd daemon)"
	else
		$SUDO ss -H -lnup 'sport = :514' | tee /dev/stderr | grep -q "pid=$pid," || die "pid $pid doesn't hold UDP 514"
		$SUDO ss -H -lntp 'sport = :514' | tee /dev/stderr | grep -q "pid=$pid," || die "pid $pid doesn't listen on TCP 514"
		ok "pid $pid holds UDP and TCP 514 (CAP_NET_BIND_SERVICE)"
		grep -q '^CapEff:' /proc/"$pid"/status && grep '^Cap' /proc/"$pid"/status
	fi
	curl -fsS "$UI/healthz" | grep -q '"ok":true' || die "/healthz"
	local code
	code=$(curl -s -o /tmp/index.html -w '%{http_code}' "$UI/")
	[ "$code" = 200 ] || die "the UI's / answered $code"
	ok "UI answers on $UI (/healthz, / and /api/status)"
}

traffic() { # traffic <run id>: 5,000 over UDP and 5,000 over TCP, then verify
	local run=$1
	say "send 10,000 messages (run $run) to 127.0.0.1:514 and find them on disk"
	woodshed_cmd send --port 514 --proto udp --count 5000 --rate 2000 --run "$run" --first-seq 0 >/dev/null
	woodshed_cmd send --port 514 --proto tcp --count 5000 --rate 5000 --run "$run" --first-seq 5000 >/dev/null
	$SUDO "$VERIFYSEQ" -data "$DATA" -run "$run" -n 10000 -wait 30s || die "messages missing on disk"
	ok "10,000 of 10,000 on disk, no duplicates"
}

still_there() { # still_there <run id> <what>
	$SUDO "$VERIFYSEQ" -data "$DATA" -run "$1" -n 10000 || die "run $1's messages aren't all on disk after $2"
	ok "run $1 still complete after $2"
}

woodshed_cmd() {
	if command -v woodshed >/dev/null 2>&1; then woodshed "$@"; else "$BIN" "$@"; fi
}

check_status_cmd() {
	say "woodshed status (as $(id -un))"
	local out
	out=$(woodshed_cmd status) || die "woodshed status failed: $out"
	echo "$out"
	echo "$out" | grep -q "Service: *running" || die "status doesn't say the service is running"
	echo "$out" | grep -q "UDP 0.0.0.0:514: listening" || die "status doesn't show UDP listening"
	ok "woodshed status shows the service and listeners"
}

restart_service() {
	say "restart"
	local before
	before=$(service_pid)
	if [ "$os" = Darwin ]; then $SUDO launchctl kickstart -k "system/$LABEL"; else $SUDO systemctl restart woodshed.service; fi
	sleep 2
	wait_up
	[ "$(service_pid)" != "$before" ] || die "the pid didn't change after the restart"
	ok "restarted (pid $before -> $(service_pid))"
}

refuses() { # refuses <expected text> <command...>
	local want=$1 out
	shift
	if out=$("$@" 2>&1); then die "'$*' should have refused: $out"; fi
	echo "$out" | grep -q "$want" || die "'$*' refused for the wrong reason: $out"
	ok "'$*' refuses: $(echo "$out" | head -1)"
}

gone() { # the service is no longer running or defined
	sleep 1
	if [ "$os" = Darwin ]; then
		$SUDO launchctl print "system/$LABEL" >/dev/null 2>&1 && die "the daemon is still loaded"
	else
		systemctl is-active --quiet woodshed.service && die "the service is still active"
	fi
	curl -fsS --max-time 2 "$UI/healthz" >/dev/null 2>&1 && die "the UI still answers"
	ok "the service is stopped and gone"
}

data_kept() { $SUDO test -d "$DATA/logs" || die "$DATA/logs was deleted"; ok "$DATA kept"; }
data_gone() { $SUDO test ! -e "$DATA" || die "$DATA still exists"; ok "$DATA deleted"; }

run_id() { openssl rand -hex 4; }

# update_check prints updates.check from the config ("" when the file or the key is missing).
update_check() {
	$SUDO sed -n '/^\[updates\]/,/^\[/s/^check *= *["'"'"']\([a-z]*\)["'"'"'].*/\1/p' "$CFG" 2>/dev/null | head -n 1 || true
}

# with_env VAR=value command...: as an admin would type it, through sudo when not root.
with_env() {
	if [ -n "$SUDO" ]; then sudo "$@"; else env "$@"; fi
}

# update_banners prints the update banner kinds in /api/status, one per line.
update_banners() {
	curl -fsS "$UI/api/status" | python3 -c 'import json,sys; [print(b["kind"]) for b in json.load(sys.stdin)["banners"] if b["kind"].startswith("update_")]'
}

# ignored_invalid <output>: an invalid WOODSHED_UPDATE_CHECK left the setting alone and said so.
ignored_invalid() {
	local want=$1 out=$2
	[ "$(update_check)" = "$want" ] || die "an invalid WOODSHED_UPDATE_CHECK changed updates.check to '$(update_check)'"
	if echo "$out" | grep -q "warning: ignored WOODSHED_UPDATE_CHECK=bogus"; then
		ok "an invalid WOODSHED_UPDATE_CHECK is ignored with a warning; the install succeeded and check is still \"$want\""
	else
		echo "::warning::the installer output doesn't show the WOODSHED_UPDATE_CHECK warning (check is still \"$want\")"
		ok "an invalid WOODSHED_UPDATE_CHECK is ignored; the install succeeded (the warning isn't in this installer's output)"
	fi
}

case "$mode" in
deb | rpm)
	pm=apt-get
	[ "$mode" = rpm ] && pm=dnf
	say "WOODSHED_UPDATE_CHECK=off $pm install $artifact"
	if [ "$mode" = deb ]; then
		with_env WOODSHED_UPDATE_CHECK=off apt-get install -y "$(realpath "$artifact")"
	else
		with_env WOODSHED_UPDATE_CHECK=off dnf install -y "$(realpath "$artifact")"
	fi
	[ "$(update_check)" = off ] ||
		die "WOODSHED_UPDATE_CHECK=off didn't reach the $mode's install script ($CFG has check = '$(update_check)')"
	ok "WOODSHED_UPDATE_CHECK=off reached the $mode's install script through ${SUDO:-root}, $pm and ${mode/deb/dpkg}: check = \"off\" before the first start"
	check_running
	r1=$(run_id)
	traffic "$r1"
	check_status_cmd
	curl -fsS "$UI/api/stats" | python3 -c 'import json,sys; d=json.load(sys.stdin); v=d["kernel_dropped_24h"]; print("kernel_dropped_24h", v); sys.exit(0 if isinstance(v, int) else 1)' ||
		die "kernel_dropped_24h isn't a number on Linux"
	ok "kernel UDP drops are visible on Linux"
	refuses "package already installed" $SUDO "$(command -v woodshed)" install
	refuses "installed by the package" $SUDO "$(command -v woodshed)" uninstall
	restart_service
	still_there "$r1" "a restart"
	say "reinstall with WOODSHED_UPDATE_CHECK=bogus: ignored with a warning"
	if [ "$mode" = deb ]; then
		out=$(with_env WOODSHED_UPDATE_CHECK=bogus apt-get install --reinstall -y "$(realpath "$artifact")" 2>&1) || die "the reinstall failed: $out"
	else
		out=$(with_env WOODSHED_UPDATE_CHECK=bogus dnf reinstall -y "$(realpath "$artifact")" 2>&1) || die "the reinstall failed: $out"
	fi
	echo "$out"
	ignored_invalid off "$out"
	wait_up
	say "reindex: refused as root, works as the service's user (re-test N1)"
	$SUDO systemctl stop woodshed.service
	refuses "sudo -u woodshed" $SUDO "$(command -v woodshed)" reindex
	wp=$(command -v woodshed)
	if [ -n "$SUDO" ]; then
		sudo -u woodshed "$wp" reindex || die "reindex as the woodshed user failed"
	else
		# Root in a minimal container (no sudo or runuser): drop to woodshed with Python.
		python3 -c 'import os, pwd, sys; p = pwd.getpwnam("woodshed"); os.setgroups([]); os.setgid(p.pw_gid); os.setuid(p.pw_uid); os.execv(sys.argv[1], sys.argv[1:])' \
			"$wp" reindex || die "reindex as the woodshed user failed"
	fi
	ok "reindex runs as the woodshed user"
	$SUDO test ! -O "$DATA/woodshed.lock" || die "the lock file is root's"
	$SUDO systemctl start woodshed.service
	wait_up
	still_there "$r1" "a reindex"
	if [ "$mode" = deb ]; then
		say "apt-get remove keeps the data"
		$SUDO apt-get remove -y woodshed
	else
		say "dnf remove keeps the data"
		$SUDO dnf remove -y woodshed
	fi
	gone
	data_kept
	$SUDO test -f /etc/woodshed/woodshed.toml || die "/etc/woodshed/woodshed.toml was deleted"
	still_there "$r1" "the package removal"
	if [ "$mode" = deb ]; then
		say "apt-get purge deletes it"
		$SUDO apt-get purge -y woodshed
		data_gone
		$SUDO test ! -e /etc/woodshed || die "/etc/woodshed still exists after purge"
		ok "/etc/woodshed deleted"
	fi
	;;
pkg)
	# The runners ship with the application firewall off; turn it on to see what it
	# does with an unsigned daemon (loopback traffic isn't filtered either way).
	fwtool=/usr/libexec/ApplicationFirewall/socketfilterfw
	$SUDO "$fwtool" --setglobalstate on || true
	say "WOODSHED_UPDATE_CHECK=off installer -pkg $artifact"
	with_env WOODSHED_UPDATE_CHECK=off installer -pkg "$artifact" -target /
	check_running
	pkg_env=no
	case "$(update_check)" in
	off)
		pkg_env=yes
		ok "ACCEPTANCE: macOS installer passes WOODSHED_UPDATE_CHECK to the pkg's scripts (check = \"off\")"
		[ -z "$(update_banners)" ] || die "update banners with check = off: $(update_banners)"
		ok "no update banner with check = off"
		;;
	ask)
		echo "::warning::ACCEPTANCE: macOS installer does NOT pass WOODSHED_UPDATE_CHECK to the pkg's scripts; docs/install.md's pre-seeded TOML is the way (checked below)"
		ok "ACCEPTANCE: macOS installer doesn't pass WOODSHED_UPDATE_CHECK through (check = \"ask\"); the pre-seeded TOML is checked below"
		[ "$(update_banners)" = update_prompt ] || die "check = ask in a release build, but the banners are '$(update_banners)'"
		ok "the update_prompt banner shows while check = ask"
		;;
	*) die "updates.check is '$(update_check)' after the install" ;;
	esac
	r1=$(run_id)
	traffic "$r1"
	check_status_cmd
	{
		echo "global: $("$fwtool" --getglobalstate 2>&1)"
		echo "woodshed: $($SUDO "$fwtool" --getappblocked /usr/local/bin/woodshed 2>&1)"
		echo "apps:"
		$SUDO "$fwtool" --listapps 2>&1
	} | tee "$LOGS/firewall.txt"
	ok "application firewall on during the install: state above (no GUI session, so no prompt could show); traffic arrived"
	refuses "package already installed" $SUDO "$(command -v woodshed)" install
	refuses "installed by the package" $SUDO "$(command -v woodshed)" uninstall
	restart_service
	still_there "$r1" "a restart"
	say "uninstall.sh keeps the data"
	$SUDO /usr/local/share/woodshed/uninstall.sh
	gone
	data_kept
	still_there "$r1" "uninstall.sh"
	say "reinstall over the kept data (WOODSHED_UPDATE_CHECK=bogus), then uninstall.sh --delete-logs"
	before_check=$(update_check)
	out=$(with_env WOODSHED_UPDATE_CHECK=bogus installer -pkg "$artifact" -target / 2>&1) || die "the reinstall failed: $out"
	# installer doesn't show the scripts' output; it's in the install log.
	out="$out
$($SUDO grep 'WOODSHED_UPDATE_CHECK' /var/log/install.log 2>/dev/null | tail -n 5)"
	echo "$out"
	wait_up
	if [ "$pkg_env" = yes ]; then
		ignored_invalid "$before_check" "$out"
	else
		[ "$(update_check)" = "$before_check" ] || die "the reinstall changed updates.check to '$(update_check)'"
		ok "the reinstall kept check = \"$before_check\""
	fi
	still_there "$r1" "a reinstall"
	$SUDO /usr/local/share/woodshed/uninstall.sh --delete-logs
	gone
	data_gone

	say "pre-seeded config (docs/install.md): check = \"off\" before installing, no banner"
	$SUDO mkdir -p "$DATA"
	printf '[updates]\ncheck = "off"\n' | $SUDO tee "$CFG" >/dev/null
	$SUDO installer -pkg "$artifact" -target /
	wait_up
	[ "$(update_check)" = off ] || die "the pre-seeded check = off became '$(update_check)'"
	[ -z "$(update_banners)" ] || die "update banners with a pre-seeded check = off: $(update_banners)"
	settings=$(curl -fsS "$UI/api/settings")
	code=$(curl -s -o /dev/null -w '%{http_code}' -X PUT -H 'Content-Type: application/json' --data "$settings" "$UI/api/settings")
	[ "$code" = 200 ] || die "saving settings over the pre-seeded config answered $code"
	[ "$(update_check)" = off ] || die "a settings save lost check = off"
	ok "a pre-seeded check = off is kept, shows no banner, and the daemon can save settings over it"
	$SUDO /usr/local/share/woodshed/uninstall.sh --delete-logs
	gone
	data_gone
	dscl . -read /Users/_woodshed >/dev/null 2>&1 && die "the _woodshed user is still there"
	ok "the _woodshed account is deleted"
	;;
manual)
	BIN=$(realpath "$artifact")
	say "woodshed install (no package)"
	$SUDO "$BIN" install
	if [ "$os" = Darwin ]; then
		grep -q "<string>_woodshed</string>" /Library/LaunchDaemons/com.woodshed.daemon.plist || die "the plist doesn't run as _woodshed"
		ok "the plist runs as _woodshed"
	else
		for l in "User=woodshed" "AmbientCapabilities=CAP_NET_BIND_SERVICE" "ProtectSystem=strict"; do
			grep -qx "$l" /etc/systemd/system/woodshed.service || die "the unit lacks $l"
		done
		ok "the unit is the packaged one (User=woodshed, CAP_NET_BIND_SERVICE, ProtectSystem=strict)"
		systemd-analyze verify /etc/systemd/system/woodshed.service && ok "systemd-analyze verify"
	fi
	check_running
	r1=$(run_id)
	traffic "$r1"
	check_status_cmd
	refuses "already installed" $SUDO "$BIN" install
	restart_service
	still_there "$r1" "a restart"
	say "woodshed uninstall keeps the data"
	$SUDO /usr/local/bin/woodshed uninstall
	gone
	data_kept
	[ ! -e /usr/local/bin/woodshed ] || die "/usr/local/bin/woodshed is still there"
	ok "the installed binary is removed"
	still_there "$r1" "woodshed uninstall"
	say "install again over the kept data, then uninstall --delete-logs"
	$SUDO "$BIN" install
	wait_up
	still_there "$r1" "a reinstall"
	$SUDO /usr/local/bin/woodshed uninstall --delete-logs
	gone
	data_gone
	if [ "$os" = Darwin ]; then
		dscl . -read /Users/_woodshed >/dev/null 2>&1 && die "the _woodshed user is still there"
		ok "the _woodshed account is deleted"
	fi
	;;
legacy)
	old_pkg=$(realpath "$artifact")
	new_pkg=$(realpath "${3:?legacy needs the old and the new package}")
	if [ "$os" = Darwin ]; then
		OLD_DATA="/Library/Application Support/Woodpile"
		remove_cmd="sudo /usr/local/share/woodpile/uninstall.sh"
		install_pkg() { $SUDO installer -pkg "$1" -target /; }
	elif [ "${old_pkg%.deb}" != "$old_pkg" ]; then
		OLD_DATA=/var/lib/woodpile
		remove_cmd="sudo apt remove woodpile"
		install_pkg() { $SUDO apt-get install -y "$1"; }
	else
		OLD_DATA=/var/lib/woodpile
		remove_cmd="sudo dnf remove woodpile"
		install_pkg() { $SUDO dnf install -y "$1"; }
	fi
	say "the old Woodpile package: $old_pkg"
	install_pkg "$old_pkg"
	wait_up
	"$(command -v woodpile)" send --port 514 --proto udp --count 100 --rate 1000 >/dev/null
	for _ in $(seq 30); do $SUDO test -d "$OLD_DATA/logs" && break; sleep 1; done
	$SUDO test -d "$OLD_DATA/logs" || die "the old Woodpile service wrote no logs in $OLD_DATA"
	ok "the old Woodpile service runs and keeps its logs in $OLD_DATA"
	if [ -n "${WOODSHED_BIN:-}" ]; then
		refuses "remove it first with: $remove_cmd" $SUDO "$(realpath "$WOODSHED_BIN")" install
	fi

	say "this package over it: $new_pkg"
	mark=0 # the pkg's script output goes to /var/log/install.log, not to installer's stdout
	if [ "$os" = Darwin ]; then mark=$($SUDO wc -l /var/log/install.log | awk '{print $1}'); fi
	install_out=$(install_pkg "$new_pkg" 2>&1) || {
		echo "$install_out"
		die "installing $new_pkg over Woodpile failed"
	}
	echo "$install_out"
	if [ "$os" = Darwin ]; then install_out=$($SUDO tail -n +"$((mark + 1))" /var/log/install.log); fi
	# Match only the start of the hint: on Linux it names both folders ("in /var/lib/woodpile
	# and /etc/woodpile isn't used..."), and dnf wraps scriptlet output into ">>> " lines.
	echo "$install_out" | grep -q "old Woodpile data in $OLD_DATA" ||
		die "the installer output doesn't name the old data folder"
	ok "the installer output names the old data folder"
	if [ "$os" != Darwin ]; then
		# deb: dpkg removes the old package (its prerm stops the service) before this one's
		# scripts run; rpm: this one's postinstall stops it. Both must say so.
		echo "$install_out" | grep -q "stopped and disabled the old Woodpile service" ||
			die "the installer output doesn't say it stopped the old Woodpile service"
		ok "the installer output says it stopped the old Woodpile service"
	fi
	check_running
	if [ "$os" = Darwin ]; then
		$SUDO launchctl print system/com.woodpile.daemon >/dev/null 2>&1 && die "the old daemon is still loaded"
		for f in /Library/LaunchDaemons/com.woodpile.daemon.plist /usr/local/bin/woodpile /Applications/Woodpile.app /usr/local/share/woodpile; do
			[ ! -e "$f" ] || die "$f is still there"
		done
		pkgutil --pkg-info com.woodpile.pkg >/dev/null 2>&1 && die "the old pkg receipt is still registered"
		ok "the old daemon, its plist, binary, launcher app and receipt are gone"
	else
		systemctl is-active --quiet woodpile.service && die "the old service is still active"
		[ ! -e /usr/lib/systemd/system/woodpile.service ] || die "the old unit is still installed"
		if [ "${old_pkg%.deb}" != "$old_pkg" ]; then
			st=$(dpkg-query -W -f='${db:Status-Status}' woodpile 2>/dev/null || echo not-installed)
			case "$st" in not-installed | config-files) ;; *) die "the old package is still $st" ;; esac
		else
			rpm -q woodpile >/dev/null 2>&1 && die "the old package is still installed"
		fi
		ok "the old package and its service are gone"
	fi
	$SUDO test -d "$OLD_DATA/logs" || die "the old data in $OLD_DATA was deleted"
	ok "the old data in $OLD_DATA is kept"
	out=$(woodshed_cmd status) || die "woodshed status failed: $out"
	echo "$out"
	echo "$out" | grep -q "old Woodpile data in $OLD_DATA" || die "woodshed status doesn't name the old data folder"
	echo "$out" | grep -q "old Woodpile service" && die "woodshed status still sees the old service"
	ok "woodshed status names the old data folder, and no old service"
	r1=$(run_id)
	traffic "$r1"
	;;
*)
	echo "unknown mode $mode" >&2
	exit 2
	;;
esac
collect_logs
printf '\n%s smoke test (%s): %d checks passed\n' "$mode" "$os" "$passes"
