#!/bin/bash
# Install smoke test for Linux and macOS (SPEC §7), run by .github/workflows/install-smoke.yml
# on a disposable CI machine. It installs Woodpile for real, as root: never run it on a
# machine you care about.
#
#   scripts/smoke/unix.sh deb    dist/woodpile_X_amd64.deb   # apt install / restart / remove / purge
#   scripts/smoke/unix.sh rpm    dist/woodpile-X.x86_64.rpm  # dnf install / restart / remove
#   scripts/smoke/unix.sh pkg    dist/macos/woodpile_X.pkg   # installer / kickstart / uninstall.sh
#   scripts/smoke/unix.sh manual path/to/woodpile            # woodpile install / uninstall
#
# Each mode checks: the service runs as the unprivileged account, holds UDP and TCP 514,
# the UI answers on 8514, 10,000 messages sent with `woodpile send` are all on disk
# exactly once (scripts/verifyseq), `woodpile status` works, a restart keeps the data,
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
	DATA="/Library/Application Support/Woodpile"
	ACCOUNT=_woodpile
	LABEL=com.woodpile.daemon
else
	DATA=/var/lib/woodpile
	ACCOUNT=woodpile
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
		$SUDO cp /Library/Logs/Woodpile/*.log "$LOGS/" 2>/dev/null
		$SUDO launchctl print "system/$LABEL" >"$LOGS/launchctl.txt" 2>&1
		$SUDO log show --last 10m --predicate 'process == "woodpile"' >"$LOGS/unified.log" 2>&1
	else
		$SUDO journalctl -u woodpile --no-pager -n 500 >"$LOGS/journal-$mode.log" 2>&1
		$SUDO systemctl status woodpile --no-pager >"$LOGS/status-$mode.txt" 2>&1
	fi
	$SUDO ls -laR "$DATA" >"$LOGS/data-$mode.txt" 2>&1
	$SUDO chmod -R a+r "$LOGS" 2>/dev/null
	set -e
}

service_pid() {
	if [ "$os" = Darwin ]; then
		$SUDO launchctl print "system/$LABEL" 2>/dev/null | awk '$1 == "pid" && $2 == "=" {print $3}'
	else
		systemctl show -p MainPID --value woodpile.service 2>/dev/null | grep -v '^0$' || true
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
	woodpile_cmd send --port 514 --proto udp --count 5000 --rate 2000 --run "$run" --first-seq 0 >/dev/null
	woodpile_cmd send --port 514 --proto tcp --count 5000 --rate 5000 --run "$run" --first-seq 5000 >/dev/null
	$SUDO "$VERIFYSEQ" -data "$DATA" -run "$run" -n 10000 -wait 30s || die "messages missing on disk"
	ok "10,000 of 10,000 on disk, no duplicates"
}

still_there() { # still_there <run id> <what>
	$SUDO "$VERIFYSEQ" -data "$DATA" -run "$1" -n 10000 || die "run $1's messages aren't all on disk after $2"
	ok "run $1 still complete after $2"
}

woodpile_cmd() {
	if command -v woodpile >/dev/null 2>&1; then woodpile "$@"; else "$BIN" "$@"; fi
}

check_status_cmd() {
	say "woodpile status (as $(id -un))"
	local out
	out=$(woodpile_cmd status) || die "woodpile status failed: $out"
	echo "$out"
	echo "$out" | grep -q "Service: *running" || die "status doesn't say the service is running"
	echo "$out" | grep -q "UDP 0.0.0.0:514: listening" || die "status doesn't show UDP listening"
	ok "woodpile status shows the service and listeners"
}

restart_service() {
	say "restart"
	local before
	before=$(service_pid)
	if [ "$os" = Darwin ]; then $SUDO launchctl kickstart -k "system/$LABEL"; else $SUDO systemctl restart woodpile.service; fi
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
		systemctl is-active --quiet woodpile.service && die "the service is still active"
	fi
	curl -fsS --max-time 2 "$UI/healthz" >/dev/null 2>&1 && die "the UI still answers"
	ok "the service is stopped and gone"
}

data_kept() { $SUDO test -d "$DATA/logs" || die "$DATA/logs was deleted"; ok "$DATA kept"; }
data_gone() { $SUDO test ! -e "$DATA" || die "$DATA still exists"; ok "$DATA deleted"; }

run_id() { openssl rand -hex 4; }

case "$mode" in
deb | rpm)
	pm=apt-get
	[ "$mode" = rpm ] && pm=dnf
	say "$pm install $artifact"
	if [ "$mode" = deb ]; then $SUDO apt-get install -y "$(realpath "$artifact")"; else $SUDO dnf install -y "$(realpath "$artifact")"; fi
	check_running
	r1=$(run_id)
	traffic "$r1"
	check_status_cmd
	curl -fsS "$UI/api/stats" | python3 -c 'import json,sys; d=json.load(sys.stdin); v=d["kernel_dropped_24h"]; print("kernel_dropped_24h", v); sys.exit(0 if isinstance(v, int) else 1)' ||
		die "kernel_dropped_24h isn't a number on Linux"
	ok "kernel UDP drops are visible on Linux"
	refuses "package already installed" $SUDO "$(command -v woodpile)" install
	refuses "installed by the package" $SUDO "$(command -v woodpile)" uninstall
	restart_service
	still_there "$r1" "a restart"
	if [ "$mode" = deb ]; then
		say "apt-get remove keeps the data"
		$SUDO apt-get remove -y woodpile
	else
		say "dnf remove keeps the data"
		$SUDO dnf remove -y woodpile
	fi
	gone
	data_kept
	$SUDO test -f /etc/woodpile/woodpile.toml || die "/etc/woodpile/woodpile.toml was deleted"
	still_there "$r1" "the package removal"
	if [ "$mode" = deb ]; then
		say "apt-get purge deletes it"
		$SUDO apt-get purge -y woodpile
		data_gone
		$SUDO test ! -e /etc/woodpile || die "/etc/woodpile still exists after purge"
		ok "/etc/woodpile deleted"
	fi
	;;
pkg)
	say "installer -pkg $artifact"
	$SUDO installer -pkg "$artifact" -target /
	check_running
	r1=$(run_id)
	traffic "$r1"
	check_status_cmd
	fw=$(/usr/libexec/ApplicationFirewall/socketfilterfw --getglobalstate 2>&1 || true)
	echo "application firewall: $fw" | tee "$LOGS/firewall.txt"
	ok "application firewall state recorded ($fw); a headless install can't show a prompt, and traffic arrived"
	refuses "package already installed" $SUDO "$(command -v woodpile)" install
	refuses "installed by the package" $SUDO "$(command -v woodpile)" uninstall
	restart_service
	still_there "$r1" "a restart"
	say "uninstall.sh keeps the data"
	$SUDO /usr/local/share/woodpile/uninstall.sh
	gone
	data_kept
	still_there "$r1" "uninstall.sh"
	say "reinstall over the kept data, then uninstall.sh --delete-logs"
	$SUDO installer -pkg "$artifact" -target /
	wait_up
	still_there "$r1" "a reinstall"
	$SUDO /usr/local/share/woodpile/uninstall.sh --delete-logs
	gone
	data_gone
	dscl . -read /Users/_woodpile >/dev/null 2>&1 && die "the _woodpile user is still there"
	ok "the _woodpile account is deleted"
	;;
manual)
	BIN=$(realpath "$artifact")
	say "woodpile install (no package)"
	$SUDO "$BIN" install
	if [ "$os" = Darwin ]; then
		grep -q "<string>_woodpile</string>" /Library/LaunchDaemons/com.woodpile.daemon.plist || die "the plist doesn't run as _woodpile"
		ok "the plist runs as _woodpile"
	else
		for l in "User=woodpile" "AmbientCapabilities=CAP_NET_BIND_SERVICE" "ProtectSystem=strict"; do
			grep -qx "$l" /etc/systemd/system/woodpile.service || die "the unit lacks $l"
		done
		ok "the unit is the packaged one (User=woodpile, CAP_NET_BIND_SERVICE, ProtectSystem=strict)"
		systemd-analyze verify /etc/systemd/system/woodpile.service && ok "systemd-analyze verify"
	fi
	check_running
	r1=$(run_id)
	traffic "$r1"
	check_status_cmd
	refuses "already installed" $SUDO "$BIN" install
	restart_service
	still_there "$r1" "a restart"
	say "woodpile uninstall keeps the data"
	$SUDO /usr/local/bin/woodpile uninstall
	gone
	data_kept
	[ ! -e /usr/local/bin/woodpile ] || die "/usr/local/bin/woodpile is still there"
	ok "the installed binary is removed"
	still_there "$r1" "woodpile uninstall"
	say "install again over the kept data, then uninstall --delete-logs"
	$SUDO "$BIN" install
	wait_up
	still_there "$r1" "a reinstall"
	$SUDO /usr/local/bin/woodpile uninstall --delete-logs
	gone
	data_gone
	if [ "$os" = Darwin ]; then
		dscl . -read /Users/_woodpile >/dev/null 2>&1 && die "the _woodpile user is still there"
		ok "the _woodpile account is deleted"
	fi
	;;
*)
	echo "unknown mode $mode" >&2
	exit 2
	;;
esac
collect_logs
printf '\n%s smoke test (%s): %d checks passed\n' "$mode" "$os" "$passes"
