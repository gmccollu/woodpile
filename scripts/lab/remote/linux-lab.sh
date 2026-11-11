#!/bin/bash
# Woodpile lab, Linux VM side. Copied to /opt/woodpile-lab on the VM by `wl stage`; called by
# the driver over ssh as the lab login, which needs passwordless sudo (a disposable test VM).
#
#   linux-lab.sh install PKG               apt/dnf install the package
#   linux-lab.sh svc                       JSON: service state, account, caps, sockets, UI, woodpile status
#   linux-lab.sh fw-state                  JSON: which host firewall is active and whether 514 is open
#   linux-lab.sh fw-enable                 turn the firewall on if it's off (keeping SSH open)
#   linux-lab.sh fw-open | fw-close        open / close UDP+TCP 514 exactly as docs/install.md says
#   linux-lab.sh verify RUN N [MISSING_OK] [WAIT]   verifyseq -json against the service's data dir
#   linux-lab.sh stats                     GET /api/stats (localhost UI)
#   linux-lab.sh devices                   the device folders under logs/
#   linux-lab.sh sample OUT INTERVAL SECS [PID]     CSV of RSS, CPU seconds, fds, threads, UDP drops
#   linux-lab.sh soak-sampler start|stop|status SECS   the sampler as a transient systemd unit
#   linux-lab.sh verify-detached RUN N OUTJSON      verifyseq as a transient unit (long runs)
#   linux-lab.sh portable start|stop|status PORT    the tarball binary by hand, unprivileged, on PORT
#   linux-lab.sh reboot                    reboot in 2 s (the ssh session returns first)
#   linux-lab.sh collect OUT.tgz           journal, unit, config, status, data listing, samples
set -euo pipefail
L=/opt/woodpile-lab
DATA=/var/lib/woodpile
UI=http://127.0.0.1:8514
S="sudo -n"
act=${1:?action}
shift

svc_pid() { systemctl show -p MainPID --value woodpile.service 2>/dev/null | grep -v '^0$' || true; }

fw_kind() {
	if command -v ufw >/dev/null && $S ufw status 2>/dev/null | grep -q "^Status: active"; then echo ufw
	elif command -v firewall-cmd >/dev/null && $S firewall-cmd --state >/dev/null 2>&1; then echo firewalld
	elif command -v ufw >/dev/null; then echo ufw-inactive
	elif command -v firewall-cmd >/dev/null; then echo firewalld-inactive
	else echo none; fi
}

case "$act" in
install)
	pkg=${1:?package path}
	case "$pkg" in
	*.deb) $S env DEBIAN_FRONTEND=noninteractive apt-get install -y "$(realpath "$pkg")" ;;
	*.rpm) $S dnf install -y "$(realpath "$pkg")" ;;
	*) echo "install: unknown package type $pkg" >&2; exit 2 ;;
	esac
	;;
svc)
	pid=$(svc_pid)
	user="" caps="" udp="" tcp=""
	if [ -n "$pid" ]; then
		user=$(ps -o user= -p "$pid" | tr -d ' ')
		caps=$(grep '^CapEff:' /proc/"$pid"/status | awk '{print $2}')
		udp=$($S ss -H -lnup 'sport = :514' | grep -c "pid=$pid," || true)
		tcp=$($S ss -H -lntp 'sport = :514' | grep -c "pid=$pid," || true)
	fi
	active=$(systemctl is-active woodpile.service || true)
	enabled=$(systemctl is-enabled woodpile.service || true)
	health=$(curl -fsS --max-time 3 "$UI/healthz" 2>/dev/null || echo "")
	status=$(woodpile status 2>&1 | head -20 || true)
	rmem=$(sysctl -n net.core.rmem_max)
	python3 - "$active" "$enabled" "$pid" "$user" "$caps" "$udp" "$tcp" "$health" "$status" "$rmem" <<'PY'
import json, sys
a = sys.argv[1:]
print(json.dumps({"active": a[0], "enabled": a[1], "pid": a[2], "user": a[3], "cap_eff": a[4],
                  "udp514_sockets": int(a[5] or 0), "tcp514_listen": int(a[6] or 0), "healthz": a[7],
                  "woodpile_status": a[8], "rmem_max": a[9]}))
PY
	;;
fw-state)
	k=$(fw_kind)
	open=unknown
	case "$k" in
	ufw) $S ufw status | grep -Eq '^514(/(tcp|udp))? +ALLOW' && open=yes || open=no ;;
	firewalld) ports=$($S firewall-cmd --list-ports); { echo "$ports" | grep -q '514/udp' && echo "$ports" | grep -q '514/tcp'; } && open=yes || open=no ;;
	*) open=n/a ;;
	esac
	echo "{\"firewall\": \"$k\", \"port514_open\": \"$open\"}"
	;;
fw-enable)
	case "$(fw_kind)" in
	ufw-inactive) $S ufw allow 22/tcp >/dev/null && $S ufw --force enable ;;
	firewalld-inactive) $S systemctl enable --now firewalld && $S firewall-cmd --permanent --add-service=ssh >/dev/null && $S firewall-cmd --reload >/dev/null ;;
	ufw | firewalld) echo "already active" ;;
	none) echo "no host firewall installed" >&2; exit 3 ;;
	esac
	fw_kind
	;;
fw-open)
	case "$(fw_kind)" in
	ufw) $S ufw allow 514 ;;                                                     # docs/install.md, Ubuntu
	firewalld) $S firewall-cmd --permanent --add-port=514/udp --add-port=514/tcp && $S firewall-cmd --reload ;; # docs, firewalld
	*) echo "fw-open: no active firewall" >&2; exit 3 ;;
	esac
	;;
fw-close)
	case "$(fw_kind)" in
	ufw) $S ufw delete allow 514 || true ;;
	firewalld) $S firewall-cmd --permanent --remove-port=514/udp --remove-port=514/tcp && $S firewall-cmd --reload ;;
	*) echo "fw-close: no active firewall" >&2; exit 3 ;;
	esac
	;;
verify)
	run=${1:?run} n=${2:?n} miss=${3:-0} wait=${4:-30s}
	$S "$L/verifyseq" -data "${WL_DATA:-$DATA}" -run "$run" -n "$n" -missing-ok "$miss" -wait "$wait" -json || true
	;;
verify-detached)
	run=${1:?run} n=${2:?n} out=${3:?out json}
	$S systemd-run --unit="woodpile-lab-verify-$run" --collect --quiet --nice=10 \
		/bin/sh -c "$L/verifyseq -data $DATA -run $run -n $n -json > $out.tmp 2>&1; mv $out.tmp $out"
	echo "started woodpile-lab-verify-$run -> $out"
	;;
stats) curl -fsS --max-time 5 "$UI/api/stats" ;;
devices) $S ls -1 "${WL_DATA:-$DATA}/logs" ;;
sample)
	out=${1:?out} iv=${2:?interval} secs=${3:?seconds} pid=${4:-}
	[ -n "$pid" ] || pid=$(svc_pid)
	tck=$(getconf CLK_TCK)
	echo "time,elapsed_s,pid,rss_kb,cpu_s,fds,threads,udp_drops" >"$out"
	t0=$(date +%s)
	end=$((t0 + secs))
	while [ "$(date +%s)" -lt "$end" ]; do
		[ -n "$pid" ] && [ -d "/proc/$pid" ] || { pid=$(svc_pid); sleep "$iv"; continue; }
		read -r ut st <<<"$(awk '{print $14, $15}' /proc/"$pid"/stat)"
		rss=$(awk '/^VmRSS:/ {print $2}' /proc/"$pid"/status)
		thr=$(awk '/^Threads:/ {print $2}' /proc/"$pid"/status)
		fds=$($S ls /proc/"$pid"/fd 2>/dev/null | wc -l)
		# kernel drops on the :514 UDP sockets (port 0202 hex), the last column of /proc/net/udp{,6}
		drops=$(awk '$2 ~ /:0202$/ {d += $NF} END {print d + 0}' /proc/net/udp /proc/net/udp6)
		cpu=$(python3 -c "print(round(($ut + $st) / $tck, 2))")
		echo "$(date -u +%H:%M:%S),$(($(date +%s) - t0)),$pid,$rss,$cpu,$fds,$thr,$drops" >>"$out"
		sleep "$iv"
	done
	;;
soak-sampler)
	op=${1:?start|stop|status}
	case "$op" in
	start)
		secs=${2:?seconds}
		$S mkdir -p /var/tmp/woodpile-lab
		$S systemd-run --unit=woodpile-lab-soak-sampler --collect --quiet --nice=5 \
			"$L/linux-lab.sh" sample /var/tmp/woodpile-lab/soak-samples.csv 60 "$secs"
		echo "sampler started for $secs s -> /var/tmp/woodpile-lab/soak-samples.csv"
		;;
	stop) $S systemctl stop woodpile-lab-soak-sampler || true ;;
	status) systemctl is-active woodpile-lab-soak-sampler || true; $S tail -n 3 /var/tmp/woodpile-lab/soak-samples.csv 2>/dev/null || true ;;
	esac
	;;
portable)
	op=${1:?start|stop|status} port=${2:-15514}
	P=$HOME/woodpile-portable
	case "$op" in
	start)
		mkdir -p "$P/data"
		cat >"$P/woodpile.toml" <<EOF
[listeners.udp]
enabled = true
port = $port

[listeners.tcp]
enabled = true
port = $port

[storage]
data_dir = "$P/data"

[ui]
port = 18514
EOF
		nohup "$P/woodpile" run --config "$P/woodpile.toml" >"$P/console.out" 2>&1 &
		echo $! >"$P/pid"
		for _ in $(seq 30); do curl -fsS --max-time 1 http://127.0.0.1:18514/healthz >/dev/null 2>&1 && break; sleep 1; done
		echo "portable pid $(cat "$P/pid") as $(id -un) on UDP/TCP $port, data $P/data"
		# a hand-run binary on another port needs its own firewall opening (removed by stop)
		case "$(fw_kind)" in
		ufw) $S ufw allow "$port" >/dev/null && echo "ufw: opened $port for the portable run" ;;
		firewalld) $S firewall-cmd --add-port="$port/udp" --add-port="$port/tcp" >/dev/null && echo "firewalld: opened $port (runtime only)" ;;
		esac
		;;
	stop)
		pid=$(cat "$P/pid")
		kill -TERM "$pid"
		for _ in $(seq 20); do kill -0 "$pid" 2>/dev/null || break; sleep 0.5; done
		kill -0 "$pid" 2>/dev/null && { echo "portable didn't stop" >&2; exit 1; }
		case "$(fw_kind)" in
		ufw) $S ufw delete allow "$port" >/dev/null || true ;;
		firewalld) $S firewall-cmd --remove-port="$port/udp" --remove-port="$port/tcp" >/dev/null || true ;;
		esac
		tail -n 5 "$P/console.out"
		;;
	status) cat "$P/pid" 2>/dev/null && tail -n 5 "$P/console.out" ;;
	esac
	;;
reboot) $S systemd-run --on-active=2 --quiet /bin/systemctl reboot && echo "rebooting in 2 s" ;;
collect)
	out=${1:?out.tgz}
	d=$(mktemp -d)
	$S journalctl -u woodpile --no-pager -n 5000 >"$d/journal.log" 2>&1 || true
	systemctl status woodpile --no-pager >"$d/systemctl-status.txt" 2>&1 || true
	systemctl cat woodpile >"$d/unit.txt" 2>&1 || true
	$S cat /etc/woodpile/woodpile.toml >"$d/woodpile.toml" 2>/dev/null || true
	woodpile status >"$d/woodpile-status.txt" 2>&1 || true
	$S ls -laR "$DATA" >"$d/data-listing.txt" 2>&1 || true
	$S cp /var/tmp/woodpile-lab/*.csv /var/tmp/woodpile-lab/*.json "$d/" 2>/dev/null || true
	$S ss -lunpt >"$d/sockets.txt" 2>&1 || true
	uname -a >"$d/uname.txt"; cat /etc/os-release >>"$d/uname.txt"
	$S chown -R "$(id -un)" "$d"
	tar -C "$d" -czf "$out" .
	rm -rf "$d"
	echo "$out"
	;;
*) echo "linux-lab.sh: unknown action $act" >&2; exit 2 ;;
esac
