#!/bin/bash
# Portable-mode smoke test for Linux and macOS, run by the install-smoke workflow on a
# disposable CI machine (it binds the real port 514, and on Linux uses sudo).
#
#   scripts/smoke/portable.sh path/to/woodpile
#
# 1. `woodpile` with no arguments, from a "download" folder, no terminal (as a file
#    manager starts it): data in woodpile-data next to it, UDP and TCP 514, the UI on
#    8514, 10,000 messages on disk exactly once, a new-device line, Ctrl-C flushes all.
#    Linux first runs it without rights and checks the banner gives the fix, then grants
#    CAP_NET_BIND_SERVICE with setcap as the banner says; macOS needs nothing.
# 2. From a terminal (a pty): the one-line note; a read-only folder falls back to ~/Woodpile.
# VERIFYSEQ points at scripts/verifyseq; SMOKE_LOGS collects the console logs.
set -euo pipefail

src=$(realpath "${1:?woodpile binary}")
VERIFYSEQ=$(realpath "${VERIFYSEQ:-dist/verifyseq}")
LOGS=${SMOKE_LOGS:-/tmp/smoke-logs}
mkdir -p "$LOGS"
os=$(uname -s)
work=$(mktemp -d "${RUNNER_TEMP:-/tmp}/portable.XXXXXX")
dl=$work/Downloads
mkdir -p "$dl"
cp "$src" "$dl/woodpile"
chmod 0755 "$dl/woodpile"
passes=0
pid=""

ok() { passes=$((passes + 1)); printf 'PASS %s\n' "$*"; }
die() {
	printf 'FAIL %s\n' "$*" >&2
	cp "$work"/*.out "$LOGS/" 2>/dev/null || true
	[ -n "$pid" ] && kill "$pid" 2>/dev/null
	exit 1
}
wait_for() { # wait_for <file> <text> <seconds>
	for _ in $(seq "$3"); do grep -qF -- "$2" "$1" 2>/dev/null && return 0; sleep 1; done
	cat "$1" >&2 || true
	die "no '$2' in $1 within $3 s"
}
stop() { # stop <pid> <console file>: Ctrl-C, and a clean, quick exit
	local t0 rc=0
	t0=$(date +%s)
	kill -INT "$1"
	wait "$1" || rc=$?
	[ "$rc" -eq 0 ] || die "portable mode exited with $rc after Ctrl-C"
	grep -q "Woodpile stopped; everything is saved" "$2" || die "no clean-stop line"
	ok "Ctrl-C stopped it cleanly in $(($(date +%s) - t0)) s"
}

if [ "$os" = Linux ]; then
	echo "==> no rights for port 514 yet: the banner explains the fix"
	(cd "$dl" && exec ./woodpile portable --no-browser --dir "$work/norights" </dev/null >"$work/norights.out" 2>&1) &
	pid=$!
	wait_for "$work/norights.out" "Press Ctrl-C" 30
	cat "$work/norights.out"
	grep -q "NOT listening on UDP 514" "$work/norights.out" || die "the banner doesn't say UDP 514 failed"
	grep -q "sudo setcap cap_net_bind_service=+ep ./woodpile" "$work/norights.out" || die "the banner doesn't give the setcap fix"
	grep -q "sudo ./woodpile" "$work/norights.out" || die "the banner doesn't give the sudo fix"
	ok "without rights the UI still starts and the banner gives both fixes"
	stop "$pid" "$work/norights.out"
	pid=""
	sudo setcap cap_net_bind_service=+ep "$dl/woodpile"
	ok "granted CAP_NET_BIND_SERVICE as the banner says"
fi

echo "==> woodpile, no arguments, started without a terminal (a double-click)"
(cd "$dl" && exec ./woodpile </dev/null >"$work/main.out" 2>&1) &
pid=$!
wait_for "$work/main.out" "Press Ctrl-C" 30
cat "$work/main.out"
grep -q "Starting in portable mode" "$work/main.out" && die "the terminal note was printed for a double-click launch"
grep -qF "Data folder:  $dl/woodpile-data" "$work/main.out" || die "the data folder isn't woodpile-data next to the binary"
grep -q "Listening on UDP 514 and TCP 514$" "$work/main.out" || die "not listening on UDP and TCP 514"
grep -q "Web UI:       http://localhost:8514/" "$work/main.out" || die "UI not on 8514"
ok "banner: data next to the binary, UDP and TCP 514, the UI on 8514"
curl -fsS http://127.0.0.1:8514/healthz | grep -q '"ok":true' || die "the UI doesn't answer"
[ "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8514/)" = 200 ] || die "the UI's / isn't 200"
ok "the UI answers"
user=$(ps -o user= -p "$pid" | tr -d ' ')
ok "running as $user (no root)"

run=$(openssl rand -hex 4)
"$dl/woodpile" send --port 514 --proto udp --count 5000 --rate 2000 --run "$run" --first-seq 0 >/dev/null
"$dl/woodpile" send --port 514 --proto tcp --count 5000 --rate 5000 --run "$run" --first-seq 5000 >/dev/null
"$VERIFYSEQ" -data "$dl/woodpile-data" -run "$run" -n 10000 -wait 30s || die "messages missing"
ok "10,000 of 10,000 on disk"
wait_for "$work/main.out" "New device 127.0.0.1 (first message" 20
[ "$(grep -c "New device" "$work/main.out")" = 1 ] || die "more than one new-device line"
ok "one new-device line"
stop "$pid" "$work/main.out"
pid=""
"$VERIFYSEQ" -data "$dl/woodpile-data" -run "$run" -n 10000 || die "messages lost at the stop"
ok "10,000 of 10,000 still on disk after the stop"

echo "==> no arguments in a terminal (a pty), from a read-only folder: the note and the ~/Woodpile fallback"
ro=$work/readonly
mkdir -p "$ro" "$work/home"
cp "$dl/woodpile" "$ro/woodpile"
chmod 0555 "$ro"
inner="cd '$ro' && echo \$\$ >'$work/tty.pid' && HOME='$work/home' exec ./woodpile"
if [ "$os" = Darwin ]; then
	script -q "$work/tty.out" /bin/sh -c "$inner" </dev/null >/dev/null 2>&1 &
else
	script -qfec "$inner" "$work/tty.out" </dev/null >/dev/null 2>&1 &
fi
spid=$!
wait_for "$work/tty.out" "Press Ctrl-C" 30
cat "$work/tty.out"
grep -qF 'Starting in portable mode; `woodpile help` lists commands.' "$work/tty.out" || die "no one-line note in a terminal"
ok "a no-argument start in a terminal prints the one-line note"
grep -qF "$work/home/Woodpile" "$work/tty.out" || die "no fallback to ~/Woodpile"
grep -q "can't be written" "$work/tty.out" || die "the banner doesn't say why it fell back"
ok "a read-only folder falls back to ~/Woodpile, and says why"
pid=$(cat "$work/tty.pid")
kill -INT "$pid"
wait "$spid" || true
pid=""
grep -q "Woodpile stopped; everything is saved" "$work/tty.out" || die "no clean stop in the terminal"
ok "Ctrl-C in the terminal stopped it cleanly"
chmod 0755 "$ro"
cp "$work"/*.out "$LOGS/" 2>/dev/null || true
printf '\nportable smoke test (%s): %d checks passed\n' "$os" "$passes"
