#!/bin/bash
# Soak test (SPEC §7, shortened): runs the real woodshed binary on high loopback ports
# for SOAK_HOURS at a steady SOAK_RATE msg/s of mixed UDP and TCP from four senders,
# with disruptions along the way, then checks every message on disk.
#
#   scripts/soak.sh                     # 2 h at 3,000 msg/s, results in tmp/soak/<timestamp>/
#   SOAK_HOURS=0.25 scripts/soak.sh     # a 15-minute smoke run
#
# The run is a series of rounds (SOAK_ROUND seconds of sending each). Disruptions:
#   round 1        low disk: a filler file pushes the data volume under the guard's
#                  threshold, writes pause and messages are dropped (and counted), then
#                  the filler goes and writes resume. On macOS the data dir is on a small
#                  disk image (hdiutil, no sudo); elsewhere this needs SOAK_LOWDISK_DIR, a
#                  small filesystem you mounted, or it's skipped.
#   after round 1  part_bytes drops to 1 MiB (a settings PUT), so files rotate and gzip
#                  every few seconds from then on.
#   after round 2  port clash: the UDP listener is moved to a port another process holds,
#                  Retry fails, the holder exits, Retry succeeds, the port is moved back.
#   after round 3  SIGTERM and restart; again halfway through the run.
# A midnight crossing isn't forced: the binary has no fake clock (a run that spans
# 00:00 UTC crosses it for real).
#
# At the end: every sequence number must be on disk exactly once, except the messages
# the server counted as dropped (the peak dropped_24h seen), which must match the missing count
# exactly. RSS and open file descriptors are sampled every 30 s (samples.csv).
#
# Exit status 0 when every check passes. Never needs sudo; binds only 127.0.0.1/::1.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
HOURS=${SOAK_HOURS:-2}
RATE=${SOAK_RATE:-3000}
ROUND=${SOAK_ROUND:-300}
SIZE=${SOAK_SIZE:-160}
PORT=${SOAK_PORT:-16514}
CLASH_PORT=${SOAK_CLASH_PORT:-16515}
UI_PORT=${SOAK_UI_PORT:-18614}
OUT=${SOAK_DIR:-$repo/tmp/soak/$(date -u +%Y%m%dT%H%M%SZ)}
IMAGE_SIZE=${SOAK_IMAGE_SIZE:-6g}
NICE=${SOAK_NICE:-10}

mkdir -p "$OUT/bin"
OUT=$(cd "$OUT" && pwd)
log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" | tee -a "$OUT/soak.log"; }
fail_count=0
check() { # check <description> <command...>
	local what=$1
	shift
	if "$@"; then log "PASS $what"; else log "FAIL $what"; fail_count=$((fail_count + 1)); fi
}

log "soak: ${HOURS} h at ${RATE} msg/s, ${ROUND} s rounds, results in $OUT"
go -C "$repo" build -o "$OUT/bin/woodshed" ./cmd/woodshed
go -C "$repo" build -o "$OUT/bin/verifyseq" ./scripts/verifyseq
WP=$OUT/bin/woodshed
BASE=http://127.0.0.1:$UI_PORT

# ---- data volume ----
mounted=""
cleanup() {
	set +e
	[ -n "${server_pid:-}" ] && kill -0 "$server_pid" 2>/dev/null && kill -TERM "$server_pid" && wait "$server_pid"
	[ -n "${sampler_pid:-}" ] && kill "$sampler_pid" 2>/dev/null
	[ -n "${holder_pid:-}" ] && kill "$holder_pid" 2>/dev/null
	rm -f "${filler:-/nonexistent}"
	if [ -n "$mounted" ]; then
		hdiutil detach "$mounted" -quiet || hdiutil detach "$mounted" -force -quiet
		rm -f "$OUT/soak.sparseimage"
		log "disk image detached and deleted"
	fi
}
trap cleanup EXIT

if [ -n "${SOAK_LOWDISK_DIR:-}" ]; then
	VOL=$SOAK_LOWDISK_DIR
elif command -v hdiutil >/dev/null 2>&1; then
	hdiutil create -quiet -size "$IMAGE_SIZE" -type SPARSE -fs APFS -volname wpsoak "$OUT/soak.sparseimage"
	mkdir -p "$OUT/mnt"
	hdiutil attach -quiet -nobrowse -mountpoint "$OUT/mnt" "$OUT/soak.sparseimage"
	mounted=$OUT/mnt
	VOL=$OUT/mnt
	log "data volume: $IMAGE_SIZE disk image at $VOL"
else
	VOL=$OUT/vol
	mkdir -p "$VOL"
	log "no disk image tool and no SOAK_LOWDISK_DIR: the low-disk phase is skipped"
fi
DATA=$VOL/data
mkdir -p "$DATA"

tcp_addr="::1"
python3 -c "import socket; s=socket.socket(socket.AF_INET6); s.bind(('::1', 0))" 2>/dev/null || tcp_addr="127.0.0.1"
cat >"$OUT/woodshed.toml" <<EOF
[listeners.udp]
enabled = true
addr = "127.0.0.1"
port = $PORT

[listeners.tcp]
enabled = true
addr = "$tcp_addr"
port = $PORT

[storage]
data_dir = "$DATA"
part_bytes = 1073741824

[ui]
port = $UI_PORT
EOF

# ---- the server and the sampler ----
start_server() {
	nice -n "$NICE" "$WP" run --config "$OUT/woodshed.toml" >>"$OUT/server.log" 2>&1 &
	server_pid=$!
	echo "$server_pid" >"$OUT/server.pid"
	for _ in $(seq 100); do
		curl -fsS -o /dev/null --max-time 1 "$BASE/healthz" 2>/dev/null && { log "server up (pid $server_pid)"; return 0; }
		sleep 0.2
	done
	log "server didn't start"
	return 1
}
stop_server() {
	local lines t0
	lines=$(wc -l <"$OUT/server.log")
	t0=$(date +%s)
	kill -TERM "$server_pid"
	wait "$server_pid" || true
	if tail -n +"$((lines + 1))" "$OUT/server.log" | grep -q "stopped cleanly"; then
		log "server stopped cleanly in $(($(date +%s) - t0)) s"
	else
		log "FAIL server didn't stop cleanly"
		fail_count=$((fail_count + 1))
	fi
}
api() { curl -fsS --max-time 10 "$@"; }
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }

sample() {
	echo "time,elapsed_s,pid,rss_kb,fds,msgs_per_sec,dropped_24h,files,used_bytes" >"$OUT/samples.csv"
	local t0
	t0=$(date +%s)
	while sleep 30; do
		local pid rss fds stats
		pid=$(cat "$OUT/server.pid")
		rss=$(ps -o rss= -p "$pid" 2>/dev/null | tr -d ' ' || true)
		[ -n "$rss" ] || continue
		fds=$(lsof -n -P -p "$pid" 2>/dev/null | tail -n +2 | wc -l | tr -d ' ')
		stats=$(api "$BASE/api/stats" 2>/dev/null | json '",".join(str(x) for x in (d["msgs_per_sec"], d["dropped_24h"], d["disk"]["used_bytes"]))' 2>/dev/null || echo ",,")
		files=$(find "$DATA/logs" -type f 2>/dev/null | wc -l | tr -d ' ')
		IFS=, read -r rate dropped used <<<"$stats"
		echo "$(date -u +%H:%M:%S),$(($(date +%s) - t0)),$pid,$rss,$fds,$rate,$dropped,$files,$used" >>"$OUT/samples.csv"
	done
}

# ---- sending ----
RUN=$(openssl rand -hex 4)
next_seq=0
shortfall=0
per_sender_rate=$((RATE / 4))
send_round() { # send_round <seconds>
	local secs=$1
	local per=$((per_sender_rate * secs)) i pids=() outs=()
	local protos=("udp lf 127.0.0.1" "udp lf 127.0.0.1" "tcp lf $tcp_addr" "tcp octet $tcp_addr")
	for i in 0 1 2 3; do
		read -r proto framing host <<<"${protos[$i]}"
		outs[i]=$OUT/send-$i.out
		"$WP" send --host "$host" --port "$PORT" --proto "$proto" --framing "$framing" --size "$SIZE" \
			--run "$RUN" --first-seq "$((next_seq + i * per))" --count "$per" --rate "$per_sender_rate" \
			>"${outs[i]}" 2>&1 &
		pids[i]=$!
	done
	for i in 0 1 2 3; do
		wait "${pids[i]}" || log "sender $i: $(tail -1 "${outs[i]}")"
		local sent
		sent=$(sed -n 's/^Sent \([0-9]*\) message.*/\1/p' "${outs[i]}" | head -1)
		sent=${sent:-0}
		if [ "$sent" -ne "$per" ]; then
			log "sender $i sent $sent of $per"
			shortfall=$((shortfall + per - sent))
		fi
	done
	next_seq=$((next_seq + 4 * per))
	sleep 3 # let the listener drain the socket buffers before any disruption
}

wait_banner() { # wait_banner <kind> <present: 1|0> <seconds>
	for _ in $(seq "$3"); do
		local has
		has=$(api "$BASE/api/status" 2>/dev/null | json "int(any(b['kind'] == '$1' for b in d['banners']))" 2>/dev/null || echo x)
		[ "$has" = "$2" ] && return 0
		sleep 1
	done
	return 1
}

low_disk_phase() {
	# Starts a quarter into round 1 and holds the pause for a quarter round (at most 60 s each).
	local quarter=$((ROUND / 4 < 60 ? ROUND / 4 : 60))
	sleep "$quarter"
	local free_kb target_kb
	free_kb=$(df -k "$VOL" | awk 'NR==2 {print $4}')
	# The guard keeps max(1 GiB, 5%) free; leave 700 MiB.
	target_kb=$((free_kb - 700 * 1024))
	filler=$VOL/filler.bin
	log "low disk: writing a $((target_kb / 1024)) MiB filler"
	dd if=/dev/urandom of="$filler" bs=1048576 count=$((target_kb / 1024)) 2>/dev/null
	if wait_banner writes_paused 1 30; then log "PASS writes paused, banner shown"; else log "FAIL no writes_paused banner"; echo fail >"$OUT/lowdisk.fail"; fi
	sleep "$quarter"
	rm -f "$filler"
	if wait_banner writes_paused 0 30; then log "PASS writes resumed, banner gone"; else log "FAIL writes didn't resume"; echo fail >"$OUT/lowdisk.fail"; fi
}

settings_put() { # settings_put <python expression editing d>
	api "$BASE/api/settings" | python3 -c "import json,sys; d=json.load(sys.stdin); $1; print(json.dumps(d))" >"$OUT/settings.json"
	api -X PUT -H 'Content-Type: application/json' --data @"$OUT/settings.json" "$BASE/api/settings" >/dev/null
}

port_clash() {
	python3 -c "import socket,time; s=socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.bind(('127.0.0.1', $CLASH_PORT)); time.sleep(3600)" &
	holder_pid=$!
	sleep 1
	settings_put "d['listeners']['udp']['port'] = $CLASH_PORT"
	check "port clash: UDP down with a listener_bind banner" wait_banner listener_bind 1 10
	api -X POST "$BASE/api/listeners/retry" >/dev/null
	check "port clash: Retry while held stays down" wait_banner listener_bind 1 3
	kill "$holder_pid"
	wait "$holder_pid" 2>/dev/null || true
	holder_pid=""
	local ok=0
	for _ in $(seq 20); do
		api -X POST "$BASE/api/listeners/retry" >/dev/null
		if wait_banner listener_bind 0 1; then ok=1; break; fi
	done
	check "port clash: Retry after the holder exits brings UDP up" test "$ok" = 1
	settings_put "d['listeners']['udp']['port'] = $PORT"
	check "port clash: UDP back on $PORT" wait_banner listener_bind 0 10
}

# ---- the run ----
start_server
sample &
sampler_pid=$!
end=$(($(date +%s) + $(python3 -c "print(int($HOURS * 3600))")))
rounds=$(python3 -c "print(max(4, int($HOURS * 3600 / $ROUND)))")
restart2=$((rounds / 2 + 1))
r=0
while [ "$r" -lt "$rounds" ] && { [ "$r" -lt 4 ] || [ "$(date +%s)" -lt "$end" ]; }; do
	r=$((r + 1))
	log "round $r/$rounds: seq $next_seq.., $((per_sender_rate * 4)) msg/s for $ROUND s"
	lowdisk_pid=""
	if [ "$r" = 1 ] && { [ -n "$mounted" ] || [ -n "${SOAK_LOWDISK_DIR:-}" ]; }; then
		low_disk_phase &
		lowdisk_pid=$!
	fi
	send_round "$ROUND"
	[ -n "$lowdisk_pid" ] && wait "$lowdisk_pid"
	case "$r" in
		1)
			settings_put "d['storage']['part_bytes'] = 1048576"
			log "part_bytes is now 1 MiB"
			;;
		2) port_clash ;;
		3 | "$restart2")
			stop_server
			start_server
			;;
	esac
done
[ -f "$OUT/lowdisk.fail" ] && fail_count=$((fail_count + 1))

# ---- the checks ----
sleep 3
stats=$(api "$BASE/api/stats")
dropped=$(echo "$stats" | json 'd["dropped_24h"]')
# dropped_24h is a rolling 24 h count, so on a 24 h run the low-disk drops from round 1
# have aged out by now. Use the highest value the sampler saw (the only drops are the
# low-disk phase's).
peak=$(awk -F, 'NR > 1 && $7 ~ /^[0-9]+$/ && $7 > m { m = $7 } END { print m + 0 }' "$OUT/samples.csv")
[ "$peak" -gt "$dropped" ] && dropped=$peak
echo "$stats" >"$OUT/final-stats.json"
api "$BASE/api/devices" >"$OUT/final-devices.json"
kill "$sampler_pid" 2>/dev/null || true
sampler_pid=""
stop_server
server_pid=""

expected_missing=$((dropped + shortfall))
log "sent $next_seq numbered messages in $r rounds; server dropped $dropped (low disk), senders fell short by $shortfall"
check "every message on disk exactly once, except the $dropped dropped" \
	"$OUT/bin/verifyseq" -data "$DATA" -run "$RUN" -n "$next_seq" -missing-ok "$expected_missing" -json
if [ -n "$mounted" ] || [ -n "${SOAK_LOWDISK_DIR:-}" ]; then
	check "the low-disk phase dropped some messages" test "$dropped" -gt 0
fi

python3 - "$OUT/samples.csv" <<'PY' | tee -a "$OUT/soak.log"
import csv, statistics, sys
rows = [r for r in csv.DictReader(open(sys.argv[1])) if r["rss_kb"]]
if len(rows) < 10:
    print("too few samples for the RSS check"); sys.exit(0)
rss = [int(r["rss_kb"]) for r in rows]
fds = [int(r["fds"]) for r in rows]
k = max(3, len(rows) // 10)
# Compare the second tenth (after warm-up) with the last tenth.
early, late = statistics.median(rss[k:2 * k]), statistics.median(rss[-k:])
print(f"RSS: median {early / 1024:.1f} MiB early, {late / 1024:.1f} MiB late, max {max(rss) / 1024:.1f} MiB; "
      f"open fds: min {min(fds)}, max {max(fds)}, last {fds[-1]} ({len(rows)} samples)")
print("RSS_FLAT" if late <= early * 1.5 + 20 * 1024 else "RSS_GREW")
print("FDS_BOUNDED" if max(fds) <= 200 else "FDS_HIGH")
PY
grep -q RSS_FLAT "$OUT/soak.log" || { log "FAIL RSS grew"; fail_count=$((fail_count + 1)); }
grep -q FDS_BOUNDED "$OUT/soak.log" || { log "FAIL too many open files"; fail_count=$((fail_count + 1)); }
du -sh "$DATA" | awk '{print "data on disk: " $1}' | tee -a "$OUT/soak.log"
find "$DATA/logs" -name '*.log.gz' | wc -l | awk '{print "compressed files: " $1}' | tee -a "$OUT/soak.log"

if [ "$fail_count" -eq 0 ]; then log "SOAK PASS"; else log "SOAK FAIL ($fail_count checks)"; fi
[ "$fail_count" -eq 0 ]
