#!/bin/sh
# E2E scenarios in Docker (docs/testing.md). Phase 04: three relays.
#   ./run.sh <scenario>... | all | short
#   scenarios: ping download forget iperf failover rejoin backup secondary limits loss mixed
#              latency fallback udpblock redundant speedtest aggregation bypass portfwd prio-cap localpath pep webui
#              capdrop update sleep captive tcploss owntraffic
# short = ping failover limits (~6 min); CI runs it when the commit message has "[e2e short]".
# On demand only (not in "all"): tcpconns (phase 19 G spike, ~15 min).
# Each scenario starts a fresh environment ("all" runs them one after another).
# Env:
#   NETEM=auto|1|0   link emulation on the carriers (auto: on, stop with a hint if sch_netem is missing)
#   NETEM_ARGS       netem parameters per carrier interface (default below)
#   DL_MB=20         single-link download size for "download" (MB = 10^6 bytes; 3 links: 200 MB)
#   IPERF_S=30       iperf3 duration per direction
#   KEEP=1           leave the containers running afterwards (single scenario only)
#   LAT_MODE=min_latency  hub mode for "latency" (max_throughput = the phase 04 baseline)
#   PEP=on        every scenario runs with PEP (split TCP) on (default: the hub's default)
#   PEP_MB=200    curl download size for "pep" (per run: PEP off, then on)
#   RACE=1        build every binary with -race; a race report fails the scenario (slower)
#   SLEEP_S=200   how long "sleep" keeps the hub frozen (> the server's 3 min idle timeout)
# Prints one PASS/FAIL line per check; full logs go to testenv/out/.
set -eu
cd "$(dirname "$0")"
export MSYS_NO_PATHCONV=1 # Git Bash on Windows: keep container paths as they are
export LC_ALL=C          # awk: decimal point, not the host locale's comma

NETEM=${NETEM:-auto}
NETEM_ARGS=${NETEM_ARGS:-rate 8mbit delay 60ms 20ms loss 0.5%}
DL_MB=${DL_MB:-20}
IPERF_S=${IPERF_S:-30}
ORIGIN=172.30.0.10
ORIGIN2=172.30.0.41 # same nginx, other addresses (DIRECT is sticky per destination IP)
ORIGIN3=172.30.0.42
IPERF=172.30.0.30
NET_HOST=192.0.2.31 # "internet" address outside the Docker subnets (portfwd)
OUT=out
SCENARIOS="ping download forget iperf failover rejoin backup secondary limits loss mixed latency fallback udpblock redundant speedtest aggregation bypass portfwd prio-cap localpath pep webui capdrop update sleep captive tcploss owntraffic"
ON_DEMAND="tcpconns"
LAT_MODE=${LAT_MODE:-min_latency}
SHORT="ping failover limits" # ~6 min: tunnel, bonding failover, per-path limits
mkdir -p "$OUT"

dc() { docker compose "$@"; }
x() { dc exec -T "$@"; }

collect_logs() {
	dc logs --no-color >"$OUT/compose.log" 2>&1 || true
	x hub cat /var/log/xs-hubd.log >"$OUT/hub.log" 2>&1 || true
	x hub xs-hubctl status >"$OUT/status.json" 2>&1 || true
	x server xs-ctl sessions >"$OUT/sessions.txt" 2>&1 || true
	for n in 1 2 3; do x "relay$n" cat /var/log/relay.log >"$OUT/relay$n.log" 2>&1 || true; done
}
pass() { echo "PASS  $*"; }
fail() {
	echo "FAIL  $*"
	collect_logs
	echo "      logs: testenv/$OUT/ (hub.log, relayN.log, status.json, sessions.txt, compose.log)"
	exit 1
}
cleanup() {
	collect_logs
	[ "${KEEP:-0}" = 1 ] || dc down -v --remove-orphans >/dev/null 2>&1 || true
}

# wait_for <seconds> <command...>: retry every 0.5 s until the command succeeds.
wait_for() {
	_wait=$(($1 * 2))
	shift
	while [ "$_wait" -gt 0 ]; do
		"$@" >/dev/null 2>&1 && return 0
		sleep 0.5
		_wait=$((_wait - 1))
	done
	return 1
}

# netem_set <n> <args...>: emulate carrier n's link (both directions).
netem_set() {
	c=carrier$1
	shift
	for ifc in $(x "$c" xs-iface 172.31.) $(x "$c" xs-iface 172.30.0.); do
		x "$c" tc qdisc replace dev "$ifc" root netem "$@" || fail "netem: $c tc qdisc replace dev $ifc root netem $*"
	done
}

netem_setup() {
	if [ "$NETEM" = 0 ]; then
		echo "NOTE  NETEM=0: links are not emulated (no rate/delay/loss)"
		return
	fi
	cell=$(x carrier1 xs-iface 172.31.1.)
	if ! x carrier1 tc qdisc add dev "$cell" root netem delay 1ms >"$OUT/netem-check.log" 2>&1; then
		echo "FAIL  netem: kernel module sch_netem is not available in this Docker host"
		echo "      (Docker Desktop / WSL2 kernels often lack it). Either re-run with NETEM=0"
		echo "      (no link emulation) or run the full scenarios on the Linux laptop."
		exit 2
	fi
	for n in 1 2 3 4; do
		# shellcheck disable=SC2086 # NETEM_ARGS is a word list on purpose
		netem_set "$n" $NETEM_ARGS
	done
	echo "NOTE  netem on carrier1-4 (each direction): $NETEM_ARGS"
}

# start_relay <n> <XSR1 code> [extra flags]: run xs-relaysim in the background in relay n.
start_relay() {
	dc exec -d -T -e QR="$2" -e N="$1" -e EXTRA="${3:-}" "relay$1" sh -c 'xs-relaysim -qr "$QR" -wifi-iface $(xs-iface 10.43.0.) \
		-cell-iface $(xs-iface 172.31.$N.) -name relay$N -carrier carrier$N -dir /var/lib/xs-relay $EXTRA \
		>>/var/log/relay.log 2>&1; echo "exit $?" >>/var/log/relay.log'
}
kill_relay() { x "relay$1" pkill -9 xs-relaysim || true; }

# hubq <jq filter>: query xs-hubctl status inside the hub.
hubq() { dc exec -T -e F="$1" hub sh -c 'xs-hubctl status | jq -r "$F"' | tr -d '\r'; }
# pv <n> <bond field>: one bond metric of relay n's path (0 if unknown).
pv() { hubq ".relays[] | select(.name==\"relay$1\") | .bond.$2 // 0"; }
connected() { [ "$(hubq '[.relays[] | select(.data_addr != null)] | length')" -ge "$1" ]; }
alive() { [ "$(hubq "[.relays[] | select(.bond.state==\"alive\")] | length")" -ge "$1" ]; }
state_is() { [ "$(pv "$1" state)" = "$2" ]; }
tunnel_up() { hubq .tunnel_up | grep -q true; }
relay_log_has() { x relay1 grep -q "$1" /var/log/relay.log; }
setpath() { r=$1; shift; x hub xs-hubctl path "$r" "$@" >/dev/null || fail "xs-hubctl path $r $*"; }

up() {
	dc build -q >"$OUT/build.log" 2>&1 || fail "docker compose build (see $OUT/build.log)"
	dc down -v --remove-orphans >/dev/null 2>&1 || true # fresh state (a KEEP=1 run may be left over)
	dc up -d >"$OUT/up.log" 2>&1 || fail "docker compose up (see $OUT/up.log)"
	trap cleanup EXIT
	netem_setup
	# The hub has internet of its own through carrier4 (its local uplink,
	# off until "localpath" enables it); its LAN side and the client do not.
	if x hub sh -c "ping -c 1 -W 2 -I \$(xs-iface 10.43.0.) $ORIGIN" >/dev/null 2>&1 ||
		x client ping -c 1 -W 2 "$ORIGIN" >/dev/null 2>&1; then
		fail "isolation: the LAN (hub LAN side or client) reaches the origin without the tunnel"
	fi
	# Retried: one ping right after start-up can be lost (netem 0.5 %, carrier4 NAT not ready).
	hub_own_net() { x hub ping -c 1 -W 2 "$ORIGIN" >/dev/null 2>&1; }
	wait_for 10 hub_own_net || fail "hub reaches the origin through carrier4 (its local uplink)"
	wait_for 30 x server xs-ctl list || fail "xs-server control socket"
	code=$(x server xs-ctl enroll hub1 2>/dev/null | tr -d '\r')
	case "$code" in XS1.*) ;; *) fail "xs-ctl enroll hub1: $code" ;; esac
	wait_for 30 x hub xs-hubctl status || fail "xs-hubd IPC socket"
	for n in 1 2 3; do
		qr=$(x hub xs-hubctl pair-qr -no-qr | tr -d '\r')
		case "$qr" in XSR1.*) ;; *) fail "xs-hubctl pair-qr: $qr" ;; esac
		eval "QR$n=\$qr"
		start_relay "$n" "$qr"
		wait_for 20 connected "$n" || fail "relay$n paired and connected to the hub"
		if [ "$n" = 1 ]; then
			x hub xs-hubctl enroll "$code" >"$OUT/enroll.log" 2>&1 || fail "xs-hubctl enroll through relay1 ($(cat "$OUT/enroll.log"))"
			wait_for 20 tunnel_up || fail "tunnel up through relay1"
		fi
	done
	wait_for 20 alive 3 || fail "3 paths alive (got $(hubq '[.relays[].bond.state] | join(",")'))"
	if [ -n "${PEP:-}" ]; then
		pep_set "$PEP"
		echo "NOTE  PEP $PEP (xs-hubctl settings -pep $PEP)"
	fi
	pass "setup: server enroll code, 3 relays paired, hub enrolled through relay1, 3 paths alive"
}

# pep_set off|on: PEP setting of the hub; on needs the server's support.
pep_set() {
	x hub xs-hubctl settings -pep "$1" >/dev/null || fail "xs-hubctl settings -pep $1"
	[ "$1" = off ] || [ "$(hubq .pep.available)" = true ] || fail "PEP on: the server does not accept streams"
}

mode_is() { [ "$(hubq .mode)" = "$1" ]; }

# dl <MB> [curl args...]: download from the origin; sets DL_BYTES DL_MBPS DL_S.
dl() {
	mb=$1
	shift
	bytes=$((mb * 1000000))
	res=$(x client curl -sS --max-time 900 "$@" -r "0-$((bytes - 1))" -o /dev/null \
		-w '%{size_download} %{speed_download} %{time_total}' "http://$ORIGIN/big.bin" 2>&1) ||
		fail "download: curl $mb MB ($res)"
	set -- $res
	[ "$1" = "$bytes" ] || fail "download: got $1 bytes, want $bytes"
	DL_BYTES=$1
	DL_MBPS=$(awk -v s="$2" 'BEGIN { printf "%.2f", s * 8 / 1e6 }')
	DL_S=$3
}

# single_link on|off: data on relay2/3 off (relay1 alone) or back on.
single_link() {
	for n in 2 3; do
		if [ "$1" = on ]; then setpath "relay$n" -up off -down off; else setpath "relay$n" -up on -down on; fi
	done
	sleep 1
}

# ratio_ok <a> <b> <min>: a/b >= min
ratio_ok() { awk -v a="$1" -v b="$2" -v m="$3" 'BEGIN { exit !(b > 0 && a / b >= m) }'; }

sc_ping() {
	out=$(x client ping -c 5 -W 3 -q "$ORIGIN" 2>&1) || fail "ping: client -> origin through the tunnel ($out)"
	avg=$(echo "$out" | awk -F/ '/^rtt|^round-trip/ { print $5 }')
	pass "ping: client -> hub -> relays -> server -> origin, 5/5, avg ${avg} ms"
}

# throughput_vs_single <min ratio> <label> <MB on 3 links> [rounds]: single
# link (DL_MB) vs 3 links with curl, alternated <rounds> times (default 1),
# compared as total bytes / total time.
throughput_vs_single() {
	rounds=${4:-1}
	b1=0 s1=0 b3=0 s3=0 _round=0
	while [ "$_round" -lt "$rounds" ]; do
		single_link on
		dl "$DL_MB"
		b1=$((b1 + DL_BYTES)) s1=$(awk -v a="$s1" -v b="$DL_S" 'BEGIN { print a + b }')
		single_link off
		dl "$3"
		b3=$((b3 + DL_BYTES)) s3=$(awk -v a="$s3" -v b="$DL_S" 'BEGIN { print a + b }')
		_round=$((_round + 1))
	done
	one=$(awk -v b="$b1" -v s="$s1" 'BEGIN { printf "%.2f", b * 8 / s / 1e6 }')
	three=$(awk -v b="$b3" -v s="$s3" 'BEGIN { printf "%.2f", b * 8 / s / 1e6 }')
	ratio=$(awk -v a="$three" -v b="$one" 'BEGIN { printf "%.2f", a / b }')
	msg="$2: 1 link ${one} Mb/s, 3 links ${three} Mb/s ($rounds x ($DL_MB MB vs $3 MB)), ${ratio}x"
	ratio_ok "$three" "$one" "$1" || fail "$msg, want >= ${1}x"
	pass "$msg"
}

sc_download() { throughput_vs_single 2.4 download 200; }

# iperf_down: TCP download with 4 parallel flows for 20 s; prints Mb/s.
iperf_down() {
	x client sh -c "iperf3 -c $IPERF -t 20 -P 4 -R -J | jq '.end.sum_received.bits_per_second / 1e6'" | tr -d '\r' ||
		fail "iperf3 download"
}

# sc_loss: 2 % loss on every link; 1 link vs 3 links, 4 TCP flows each, two
# alternating rounds (one TCP flow at 2 % loss is too noisy to compare).
sc_loss() {
	[ "$NETEM" = 0 ] && fail "loss: needs netem"
	for n in 1 2 3; do netem_set "$n" rate 8mbit delay 60ms 20ms loss 2%; done
	echo "NOTE  netem on carrier1-3: rate 8mbit delay 60ms 20ms loss 2%"
	one=0 three=0 _round=0
	while [ "$_round" -lt 2 ]; do
		single_link on
		one=$(awk -v a="$one" -v b="$(iperf_down)" 'BEGIN { print a + b }')
		single_link off
		three=$(awk -v a="$three" -v b="$(iperf_down)" 'BEGIN { print a + b }')
		_round=$((_round + 1))
	done
	one=$(awk -v a="$one" 'BEGIN { printf "%.2f", a / 2 }')
	three=$(awk -v a="$three" 'BEGIN { printf "%.2f", a / 2 }')
	ratio=$(awk -v a="$three" -v b="$one" 'BEGIN { printf "%.2f", a / b }')
	msg="loss (2% on every link, iperf3 4 flows, 2 x 20 s each): 1 link ${one} Mb/s, 3 links ${three} Mb/s, ${ratio}x"
	ratio_ok "$three" "$one" 2 || fail "$msg, want >= 2x"
	pass "$msg; hub re-sent $(hubq '[.relays[].bond.retransmits] | add') packets, skipped $(hubq .bond.skipped)"
}

sc_mixed() {
	[ "$NETEM" = 0 ] && fail "mixed: needs netem"
	netem_set 1 rate 8mbit delay 20ms 2ms loss 0.5%
	netem_set 2 rate 8mbit delay 30ms 2ms loss 0.5%
	netem_set 3 rate 2mbit delay 60ms 2ms loss 0.5%
	echo "NOTE  links 8/8/2 Mb/s, RTT 40/60/120 ms, 0.5% loss"
	sleep 3
	dl 100
	share="relay1 $(pv 1 rx_data) relay2 $(pv 2 rx_data) relay3 $(pv 3 rx_data) bytes"
	msg="mixed: 100 MB in ${DL_S} s, ${DL_MBPS} Mb/s ($share)"
	ratio_ok "$DL_MBPS" 1 14 || fail "$msg, want >= 14 Mb/s"
	pass "$msg"
}

# sc_update: hub self-update (phase 18, internal/update). xs-hubctl update -apply runs
# the real deploy/hub/install.sh and gui/packaging/install.sh from a release-shaped
# archive, as root, like the GUI does through pkexec. The container has no systemd:
# /etc/systemd/system and /etc/default (Debian/Ubuntu have them) are created and
# systemctl is a stub that logs its calls. A wrong checksum and an archive for
# another architecture must change nothing.
sc_update() {
	arch=$(x hub uname -m)
	case "$arch" in x86_64) arch=amd64 other=arm64 ;; aarch64) arch=arm64 other=amd64 ;; *) fail "update: arch $arch" ;; esac
	v=v9.9.9
	d=crossstream-hub-$v-linux-$arch
	dc cp ../deploy/hub hub:/tmp/rel-hub >/dev/null 2>&1 || fail "update: copy deploy/hub into the hub"
	dc cp ../gui/packaging hub:/tmp/rel-gui >/dev/null 2>&1 || fail "update: copy gui/packaging into the hub"
	# Release layout as deploy.yml packs it: install files, bin/, gui/. xs-hubd is a marker
	# script (the old binary keeps running), xs-hubctl the real one.
	x hub sh -ec "
		mkdir -p /tmp/rel/$d/bin /tmp/rel/$d/gui /tmp/stub /etc/systemd/system /etc/default
		cp /tmp/rel-hub/* /tmp/rel/$d/
		cp /tmp/rel-gui/* /tmp/rel/$d/gui/
		cp /usr/local/bin/xs-hubctl /tmp/rel/$d/bin/
		printf '#!/bin/sh\necho xs-hubd $v\n' >/tmp/rel/$d/bin/xs-hubd
		printf '#!/bin/sh\necho xs-hub-gui $v\n' >/tmp/rel/$d/bin/xs-hub-gui
		chmod 755 /tmp/rel/$d/bin/*
		tar -czf /tmp/rel/$d.tar.gz -C /tmp/rel $d
		o=crossstream-hub-$v-linux-$other
		cp -r /tmp/rel/$d /tmp/rel/\$o && tar -czf /tmp/rel/\$o.tar.gz -C /tmp/rel \$o
		printf '#!/bin/sh\necho \"\$*\" >>/tmp/systemctl.log\n' >/tmp/stub/systemctl
		chmod 755 /tmp/stub/systemctl
	" || fail "update: build the release archive"
	sum=$(x hub sh -c "sha256sum /tmp/rel/$d.tar.gz | cut -d' ' -f1")
	osum=$(x hub sh -c "sha256sum /tmp/rel/crossstream-hub-$v-linux-$other.tar.gz | cut -d' ' -f1")
	# -log takes a new file only (it runs as root): a fresh one per run, as the GUI does.
	apply() { x hub rm -f /tmp/update.log; x hub env PATH=/tmp/stub:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
		xs-hubctl update -apply "$1" -sha256 "$2" -gui-prefix /tmp/guiprefix -log /tmp/update.log >>"$OUT/update.txt" 2>&1; }
	replaced() { [ "$(x hub head -c 2 /usr/local/bin/xs-hubd)" = '#!' ]; }

	apply "/tmp/rel/$d.tar.gz" "$(printf '0%.0s' $(seq 64))" && fail "update: a wrong checksum was installed"
	replaced && fail "update: a wrong checksum replaced xs-hubd"
	apply "/tmp/rel/crossstream-hub-$v-linux-$other.tar.gz" "$osum" && fail "update: a $other archive was installed on $arch"
	replaced && fail "update: the $other archive replaced xs-hubd"

	apply "/tmp/rel/$d.tar.gz" "$sum" || fail "update: xs-hubctl update -apply ($(x hub tail -n 3 /tmp/update.log | tr '\n' ' '))"
	replaced || fail "update: /usr/local/bin/xs-hubd not replaced"
	[ "$(x hub /usr/local/bin/xs-hubd)" = "xs-hubd $v" ] || fail "update: new xs-hubd does not run"
	[ "$(x hub /tmp/guiprefix/bin/xs-hub-gui)" = "xs-hub-gui $v" ] || fail "update: GUI not installed under -gui-prefix"
	x hub grep -q '^Exec=/tmp/guiprefix/bin/xs-hub-gui$' /tmp/guiprefix/share/applications/crossstream-hub.desktop ||
		fail "update: .desktop Exec does not point at the installed GUI"
	x hub test -f /etc/systemd/system/xs-hubd.service || fail "update: systemd unit not installed"
	x hub test -f /etc/default/xs-hubd || fail "update: /etc/default/xs-hubd not installed"
	x hub getent group crossstream >/dev/null || fail "update: group crossstream not created"
	[ "$(x hub stat -c %a /var/lib/crossstream)" = 700 ] || fail "update: /var/lib/crossstream is not 0700"
	calls=$(x hub cat /tmp/systemctl.log | tr '\n' '|')
	[ "$calls" = "daemon-reload|try-restart xs-hubd.service|" ] || fail "update: systemctl calls $calls"
	x hub grep -q '^installed$' /tmp/update.log || fail "update: -log lacks the result"
	left=$(x hub sh -c 'ls -d /tmp/crossstream-install-* 2>/dev/null | wc -l')
	[ "$left" = 0 ] || fail "update: $left unpacked install directories left in /tmp"
	pass "update: wrong checksum and $other archive refused; -apply ran install.sh + GUI installer, xs-hubd replaced, try-restart asked"
}

# sc_capdrop: relay1's link falls 10x after the paths learned their rates
# (phase 18: the server kept its old estimate for the path, with TCP traffic
# every rate sample looked app-limited, and the bond fell below the other
# paths alone). Smoke check only: no collapse while or after re-learning.
# On the 2-core CI VM the old code also reaches ~0.87x here, so the
# discriminating regression test is internal/bond TestSimCapacityDrop.
# sc_sleep: a relay phone frozen (Doze) for 20 s, then the hub laptop asleep
# (every hub process frozen) for longer than the server's idle timeout
# (3 min): the server has dropped the session, the relays saw nothing for
# minutes. After the wake the tunnel comes back on its own.
sc_sleep() {
	tun_ping() { x client ping -c 1 -W 2 "$ORIGIN"; }
	wait_for 10 tun_ping || fail "sleep: client ping before the sleep"
	dc pause relay2 >/dev/null || fail "sleep: docker compose pause relay2"
	wait_for 10 state_is 2 dead || fail "sleep: relay2 path not dead while relay2 is frozen ($(pv 2 state))"
	wait_for 5 tun_ping || fail "sleep: client ping with relay2 frozen"
	sleep 10
	dc unpause relay2 >/dev/null || fail "sleep: docker compose unpause relay2"
	wait_for 15 state_is 2 alive || fail "sleep: relay2 path not alive 15 s after it woke ($(pv 2 state))"
	pass "sleep: relay2 frozen 20 s, path dead and the tunnel kept working, alive again after the wake"
	dc pause hub >/dev/null || fail "sleep: docker compose pause hub"
	sleep "${SLEEP_S:-200}"
	expired() { dc logs --no-color server 2>&1 | grep -q "session expired (idle)"; }
	wait_for 10 expired || fail "sleep: the server did not drop the hub's idle session (SLEEP_S=${SLEEP_S:-200})"
	dc unpause hub >/dev/null || fail "sleep: docker compose unpause hub"
	t0=$(date +%s)
	wait_for 60 tun_ping || fail "sleep: no client ping through the tunnel 60 s after the hub woke"
	took=$(($(date +%s) - t0))
	[ "$took" -le 10 ] || fail "sleep: tunnel back ${took} s after the wake, want <= 10 s (the hub must not wait for 30 s of server silence)"
	wait_for 30 alive 3 || fail "sleep: 3 paths alive after the wake (got $(hubq '[.relays[].bond.state] | join(",")'))"
	out=$(x client ping -c 5 -W 3 -q "$ORIGIN" 2>&1) || fail "sleep: ping after the wake ($out)"
	pass "sleep: hub frozen ${SLEEP_S:-200} s (server session expired), tunnel back ${took} s after the wake, 3 paths alive"
}

sc_capdrop() {
	[ "$NETEM" = 0 ] && fail "capdrop: needs netem"
	for n in 1 2 3; do netem_set "$n" rate 8mbit delay 30ms 2ms; done
	sleep 3
	dl 40
	netem_set 1 rate 800kbit delay 30ms 2ms limit 100
	echo "NOTE  links 8 Mb/s, 60 ms; carrier1 then cut to 0.8 Mb/s (netem limit 100 packets)"
	dl 30
	first=$DL_MBPS
	dl 30
	three=$DL_MBPS
	setpath relay1 -up off -down off
	sleep 1
	dl 30
	two=$DL_MBPS
	msg="capdrop: after the cut, 3 paths ${first} Mb/s (first 30 MB), then ${three} Mb/s; relay2+3 alone ${two} Mb/s"
	ratio_ok "$first" "$two" 0.4 || fail "$msg, want the first >= 0.4x (no collapse while re-learning)"
	ratio_ok "$three" "$two" 0.75 || fail "$msg, want then >= 0.75x"
	pass "$msg"
}

sc_iperf() {
	# jq runs in the container (the Windows host has none)
	down=$(x client sh -c "iperf3 -c $IPERF -t $IPERF_S -R -J | jq '.end.sum_received.bits_per_second / 1e6'" 2>&1 | tr -d '\r') ||
		fail "iperf: download ($down)"
	sleep 2 # iperf3 -s answers "busy" right after a test
	upl=$(x client sh -c "iperf3 -c $IPERF -t $IPERF_S -J | jq '.end.sum_received.bits_per_second / 1e6'" 2>&1 | tr -d '\r') ||
		fail "iperf: upload ($upl)"
	pass "iperf: ${IPERF_S} s each way, down $(printf '%.2f' "$down") Mb/s, up $(printf '%.2f' "$upl") Mb/s"
}

# monitored_dl <MB>: start a download in the client that logs the longest
# time (in 0.1 s ticks) the file did not grow; result in /tmp/dl.res.
monitored_dl() {
	dc exec -d -T -e B=$(($1 * 1000000)) -e O="$ORIGIN" client sh -c '
		rm -f /tmp/dl /tmp/dl.res
		curl -sS --max-time 900 -r "0-$((B - 1))" -o /tmp/dl "http://$O/big.bin" 2>/tmp/dl.err & pid=$!
		last=0; stall=0; max=0; at=0; started=0; t0=$(awk "{ printf \"%d\", \$1 * 10 }" /proc/uptime)
		while kill -0 $pid 2>/dev/null; do
			s=$(stat -c %s /tmp/dl 2>/dev/null || echo 0)
			if [ "$s" = "$last" ]; then stall=$((stall + 1)); else stall=0; [ "$s" -gt 0 ] && started=1; fi
			if [ "$started" = 1 ] && [ $stall -gt $max ]; then max=$stall; at=$(($(awk "{ printf \"%d\", \$1 * 10 }" /proc/uptime) - t0)); fi
			last=$s; sleep 0.1
		done
		wait $pid; rc=$?
		echo "rc=$rc size=$(stat -c %s /tmp/dl 2>/dev/null || echo 0) max_stall=$max stall_end=$at" >/tmp/dl.res'
}
dl_done() { x client test -f /tmp/dl.res; }
dl_res() { x client cat /tmp/dl.res | tr -d '\r'; }

# ping_probe <count> <interval>: game-like ICMP probe from the client in the
# background; ping_stats <count> <interval> sets PSTAT (p50/p99/max, loss,
# longest gap as text), P99 and GAP (ms) and LOSS (%).
ping_probe() {
	dc exec -d -T -e O="$ORIGIN" -e C="$1" -e I="$2" client sh -c \
		'rm -f /tmp/ping.txt; ping -i "$I" -c "$C" -W 2 "$O" >/tmp/ping.txt 2>&1; echo done >>/tmp/ping.txt'
}
ping_done() { x client grep -q '^done' /tmp/ping.txt; }
ping_stats() {
	_res=$(x client cat /tmp/ping.txt | tr -d '\r' | awk -v c="$1" -v iv="$2" '
		/icmp_seq=/ {
			for (i = 1; i <= NF; i++) {
				if ($i ~ /^icmp_seq=/) { split($i, a, "="); seq = a[2] }
				if ($i ~ /^time=/) { split($i, b, "="); t[n++] = b[2] + 0 }
			}
			if (!(seq in seen)) got++
			seen[seq] = 1
		}
		END {
			# longest run of echo requests that never got a reply (replies may
			# arrive out of order: min_latency does not hold real-time packets)
			for (s = 1; s <= c; s++) { if (s in seen) run = 0; else if (++run > gap) gap = run }
			for (i = 0; i < n; i++) for (j = i + 1; j < n; j++) if (t[j] < t[i]) { x = t[i]; t[i] = t[j]; t[j] = x }
			printf "%.0f %.0f %.0f %.2f %.0f\n", t[int(n * 0.5)], t[int(n * 0.99)], t[n - 1], (c - got) * 100 / c, (gap + 1) * iv * 1000
		}')
	set -- $_res
	P99=$2 LOSS=$4 GAP=$5
	PSTAT="p50 $1 ms, p99 $2 ms, max $3 ms, loss $4%, longest gap $5 ms"
}

# sc_latency: game-like probe (ICMP echo 90/s) under a TCP download, relay2
# killed at 15 s; then the same probe while the client saturates the upload
# (bufferbloat). Hub mode LAT_MODE (default min_latency); the phase 04
# baseline (max_throughput) was p99 ~700 ms under the download.
sc_latency() {
	x hub xs-hubctl settings -mode "$LAT_MODE" >/dev/null || fail "latency: xs-hubctl settings -mode $LAT_MODE"
	monitored_dl 150
	sleep 3
	ping_probe 2700 0.011
	sleep 15
	kill_relay 2
	wait_for 40 ping_done || fail "latency: ping did not finish"
	ping_stats 2700 0.011
	dl_res=$PSTAT p99_dl=$P99
	start_relay 2 "$QR2"
	wait_for 30 alive 3 || fail "latency: relay2 did not come back"
	x client pkill curl >/dev/null 2>&1 || true
	dc exec -d -T -e I="$IPERF" client sh -c 'iperf3 -c "$I" -t 40 -P 4 >/tmp/up.txt 2>&1'
	sleep 5
	ping_probe 2000 0.011
	wait_for 40 ping_done || fail "latency: ping under upload did not finish"
	ping_stats 2000 0.011
	up_res=$PSTAT
	msg="latency ($LAT_MODE): ICMP 90/s under a download, relay2 killed at 15 s: $dl_res; under a 4-flow upload: $up_res"
	if [ "$LAT_MODE" = min_latency ]; then
		awk -v p="$p99_dl" 'BEGIN { exit !(p < 490) }' || fail "$msg; want p99 under the download < 490 ms (baseline ~700)"
	fi
	pass "$msg"
}

sc_failover() {
	monitored_dl 60
	sleep 10
	kill_relay 2
	echo "NOTE  relay2 killed (kill -9) at 10 s"
	wait_for 600 dl_done || fail "failover: download did not finish"
	res=$(dl_res)
	eval "$res"
	stall_s=$(awk -v t="$max_stall" 'BEGIN { printf "%.1f", t / 10 }')
	end_s=$(awk -v t="$stall_end" 'BEGIN { printf "%.1f", t / 10 }')
	msg="failover: 60 MB, relay2 killed at 10 s, max stall ${stall_s} s (ended at ${end_s} s), relay2 path $(pv 2 state)"
	[ "$rc" = 0 ] && [ "$size" = 60000000 ] || fail "$msg (curl rc=$rc size=$size)"
	[ "$max_stall" -lt 10 ] || fail "$msg, want < 1 s"
	pass "$msg"
}

sc_rejoin() {
	kill_relay 2
	wait_for 10 state_is 2 dead || fail "rejoin: relay2 path not dead after kill"
	monitored_dl 40
	sleep 3
	before=$(pv 2 rx_data)
	start_relay 2 "$QR2"
	t0=$(date +%s)
	wait_for 5 state_is 2 alive || fail "rejoin: relay2 path not alive within 5 s"
	carried() { [ "$(pv 2 rx_data)" -gt "$before" ]; }
	wait_for 5 carried || fail "rejoin: relay2 path alive but carries no traffic"
	took=$(($(date +%s) - t0))
	wait_for 600 dl_done || fail "rejoin: download did not finish"
	pass "rejoin: relay2 restarted, path alive and carrying traffic after ~${took} s ($(pv 2 rx_data) bytes)"
}

sc_backup() {
	setpath relay3 -priority backup
	sleep 2
	monitored_dl 100
	sleep 3
	b3=$(pv 3 rx_data)
	sleep 5
	d3=$(($(pv 3 rx_data) - b3))
	[ "$d3" = 0 ] || fail "backup: relay3 carried $d3 data bytes with relay1+2 alive"
	kill_relay 1
	sleep 5
	d3=$(($(pv 3 rx_data) - b3))
	[ "$d3" = 0 ] || fail "backup: relay3 carried $d3 data bytes with relay2 alive"
	state3=$(pv 3 state)
	kill_relay 2
	takes() { [ "$(pv 3 rx_data)" -gt "$b3" ]; }
	wait_for 5 takes || fail "backup: relay3 did not take over when relay1+2 died"
	wait_for 600 dl_done || fail "backup: download did not finish"
	eval "$(dl_res)"
	[ "$rc" = 0 ] || fail "backup: download failed after the takeover (rc=$rc)"
	pass "backup: relay3 ($state3) carried 0 bytes with 1-2 normal paths, took over ($(pv 3 rx_data) bytes), max stall $(awk -v t="$max_stall" 'BEGIN { printf "%.1f", t / 10 }') s"
}

# udp_down <Mb/s> <seconds>: constant-rate UDP download (iperf3), in the background.
udp_down() { dc exec -d -T client iperf3 -c "$IPERF" -u -b "$1M" -l 1200 -t "$2" -R; }

sc_secondary() {
	setpath relay3 -priority secondary
	sleep 2
	udp_down 6 14
	sleep 3
	b1=$(pv 1 rx_data) b2=$(pv 2 rx_data) b3=$(pv 3 rx_data)
	sleep 10
	low3=$(($(pv 3 rx_data) - b3))
	low12=$(($(pv 1 rx_data) - b1 + $(pv 2 rx_data) - b2))
	ratio_ok "$low12" 1 1 || fail "secondary: no traffic on relay1+2"
	awk -v a="$low3" -v b="$low12" 'BEGIN { exit !(a <= 0.01 * b) }' ||
		fail "secondary: relay3 carried $low3 of $((low3 + low12)) bytes at 6 Mb/s (want ~0)"
	sleep 3
	udp_down 25 14
	sleep 4
	b1=$(pv 1 rx_data) b2=$(pv 2 rx_data) b3=$(pv 3 rx_data)
	sleep 8
	high3=$(($(pv 3 rx_data) - b3))
	high12=$(($(pv 1 rx_data) - b1 + $(pv 2 rx_data) - b2))
	awk -v a="$high3" -v b="$high12" 'BEGIN { exit !(a >= 0.1 * (a + b)) }' ||
		fail "secondary: relay3 carried $high3 of $((high3 + high12)) bytes at 25 Mb/s offered (want >= 10%)"
	pass "secondary: relay3 carried $low3 of $((low3 + low12)) bytes at 6 Mb/s, $high3 of $((high3 + high12)) at 25 Mb/s offered"
}

# sv <n> <bond field>: one bond metric of path n on the server (download side).
sv() {
	dc exec -T -e F=".[0].paths[] | select(.id==$1) | .stats.$2 // 0" server sh -c 'xs-ctl sessions -json | jq -r "$F"' | tr -d '\r'
}

sc_limits() {
	setpath relay1 -rate-kbps 2000
	single_link on # relay1 alone, offered 4 Mb/s of UDP: the server's limiter must hold it at 2
	udp_down 4 16
	sleep 5
	r0=$(sv 1 tx_bytes)
	t0=$(date +%s.%N)
	sleep 10
	r1=$(sv 1 tx_bytes)
	t1=$(date +%s.%N)
	rate=$(awk -v a="$r0" -v b="$r1" -v t0="$t0" -v t1="$t1" 'BEGIN { printf "%.0f", (b - a) * 8 / (t1 - t0) / 1000 }')
	awk -v r="$rate" 'BEGIN { exit !(r >= 1900 && r <= 2100) }' || fail "limits: relay1 at ${rate} kbit/s, want 2000 ±5%"
	sleep 2
	single_link off
	setpath relay1 -rate-kbps 0 -cap-mb 50
	used0=$(pv 1 used)
	dl 200
	used=$(pv 1 used)
	capped=$(pv 1 capped)
	# The server enforces the cap on downloads; the hub's count trails it by
	# the packets lost on the way down (with PEP there are no client TCP ACKs
	# going up to make up for them), so either side's cap state counts.
	sused=$(sv 1 used)
	scapped=$(sv 1 capped)
	msg="limits: relay1 rate ${rate} kbit/s (limit 2000), cap 50 MB: hub used $used bytes (was $used0) capped=$capped, server used $sused capped=$scapped"
	[ "$capped" = true ] || [ "$scapped" = true ] || fail "$msg"
	awk -v u="$used" -v s="$sused" 'BEGIN { m = u > s ? u : s; exit !(m >= 50e6 && m <= 52.5e6) }' || fail "$msg, want 50 MB +5%"
	pass "$msg"
}

# bg_dl <MB> <ip>: background download from origin address ip; result in /tmp/bg_<ip>.res.
bg_dl() {
	dc exec -d -T -e B=$(($1 * 1000000)) -e O="$2" client sh -c '
		curl -sS --max-time 600 -r "0-$((B - 1))" -o /dev/null -w "%{size_download} %{speed_download}" \
			"http://$O/big.bin" >/tmp/bg_$O.out 2>&1; echo "rc=$? $(cat /tmp/bg_$O.out)" >/tmp/bg_$O.res'
}
bg_done() { x client test -f "/tmp/bg_$1.res"; }
# relay_down <n>: bytes relay n received from the internet (relay stats).
relay_down() { hubq ".relays[] | select(.name==\"relay$1\") | .stats.bytes_down // 0"; }

# sc_fallback: server stopped -> DIRECT (flows through the relays' proxy role,
# spread per destination IP); server started -> BONDED while an ongoing direct
# download drains.
sc_fallback() {
	FB_MB=${FB_MB:-15}
	dc stop -t 2 server >"$OUT/server-stop.log" 2>&1 || fail "fallback: docker compose stop server"
	t0=$(date +%s)
	wait_for 30 mode_is DIRECT || fail "fallback: no DIRECT 30 s after the server stopped (mode $(hubq .mode))"
	took=$(($(date +%s) - t0))
	[ "$took" -le 20 ] || fail "fallback: DIRECT after ${took} s, want <= 20 s"
	pass "fallback: server stopped -> DIRECT after ${took} s"

	d1=$(relay_down 1) d2=$(relay_down 2) d3=$(relay_down 3)
	for ip in $ORIGIN $ORIGIN2 $ORIGIN3; do bg_dl "$FB_MB" "$ip"; done
	spread() { [ "$(hubq '[.relays[] | select(.direct_flows > 0)] | length')" -ge 3 ]; }
	wait_for 10 spread || fail "fallback: 3 downloads not on 3 relays (flows $(hubq '[.relays[].direct_flows] | join(",")'))"
	for ip in $ORIGIN $ORIGIN2 $ORIGIN3; do
		wait_for 300 bg_done "$ip" || fail "fallback: download from $ip did not finish"
		res=$(x client cat "/tmp/bg_$ip.res" | tr -d '\r')
		case "$res" in "rc=0 $((FB_MB * 1000000)) "*) ;; *) fail "fallback: download from $ip: $res" ;; esac
	done
	share="" min=$((FB_MB * 1000000))
	# relay stats arrive periodically: give the last report a few seconds
	carried() { [ $(($(relay_down "$1") - $2)) -ge "$min" ]; }
	for n in 1 2 3; do
		case $n in 1) d=$d1 ;; 2) d=$d2 ;; *) d=$d3 ;; esac
		wait_for 5 carried "$n" "$d" || true
		b=$(($(relay_down "$n") - d))
		share="$share relay$n $((b / 1000000)) MB"
		[ "$b" -ge "$min" ] || fail "fallback: relay$n carried $b bytes, want >= $min (3 parallel downloads:$share)"
	done
	pass "fallback: 3 parallel $FB_MB MB downloads (3 origin IPs) through 3 relays:$share"

	monitored_dl 60
	started() { x client sh -c '[ "$(stat -c %s /tmp/dl 2>/dev/null || echo 0)" -gt 1000000 ]'; }
	wait_for 20 started || fail "fallback: direct download did not start"
	dc start server >"$OUT/server-start.log" 2>&1 || fail "fallback: docker compose start server"
	t0=$(date +%s)
	wait_for 75 mode_is BONDED || fail "fallback: no BONDED 75 s after the server started (mode $(hubq .mode))"
	took=$(($(date +%s) - t0))
	dl_done && fail "fallback: the direct download finished before BONDED (nothing drained; make it longer)"
	[ "$(hubq .direct.draining)" = true ] || fail "fallback: BONDED with a direct download running, but not draining"
	pass "fallback: server started -> BONDED after ${took} s, draining $(hubq .direct.tcp) direct TCP flow(s)"
	out=$(x client ping -c 3 -W 3 -q "$ORIGIN2" 2>&1) || fail "fallback: new flow (ping) through the tunnel while draining ($out)"
	wait_for 300 dl_done || fail "fallback: drained download did not finish"
	eval "$(dl_res)"
	[ "$rc" = 0 ] && [ "$size" = 60000000 ] || fail "fallback: drained download rc=$rc size=$size"
	proxy_off() { [ "$(hubq .direct.proxy)" = false ]; }
	wait_for 15 proxy_off || fail "fallback: proxy role still on after the drained download ended"
	pass "fallback: drain: direct download finished in BONDED (60 MB, max stall $(awk -v t="$max_stall" 'BEGIN { printf "%.1f", t / 10 }') s), new flows on the tunnel, proxy role off"
}

# sc_udpblock: carrier2 drops all UDP to the server; relay2 falls back to
# TCP or WSS (udp:51900 -> udp:443 -> tcp -> wss) and the download stays bonded.
sc_udpblock() {
	x carrier2 sh -c 'printf "%s
" "table ip block {" "chain forward {" 		"type filter hook forward priority 0; policy accept;" 		"ip daddr 172.30.0.2 meta l4proto udp drop" "}" "}" | nft -f -' || fail "udpblock: nft rule on carrier2"
	t0=$(date +%s)
	stream_alive() {
		tr=$(hubq '.relays[] | select(.name=="relay2") | .transport // ""')
		{ [ "$tr" = tcp ] || [ "$tr" = wss ]; } && state_is 2 alive
	}
	wait_for 60 stream_alive || fail "udpblock: relay2 not alive over tcp/wss (transport $(hubq '.relays[] | select(.name=="relay2") | .transport'), state $(pv 2 state))"
	took=$(($(date +%s) - t0))
	tr=$(hubq '.relays[] | select(.name=="relay2") | .transport')
	before=$(pv 2 rx_data)
	dl 60
	got=$(($(pv 2 rx_data) - before))
	share=$(awk -v g="$got" 'BEGIN { printf "%.0f", g * 100 / 60e6 }')
	msg="udpblock: relay2 on $tr after ${took} s; 60 MB in ${DL_S} s, ${DL_MBPS} Mb/s, relay2 carried ${share}%"
	[ "$share" -ge 10 ] || fail "$msg, want relay2 >= 10%"
	# More than one 8 Mb/s link: still bonded with relay2 on a stream transport
	# (was >= 10; the CI runner sits at 9.4-10.6, decisions.md phase 14).
	ratio_ok "$DL_MBPS" 1 8.5 || fail "$msg, want >= 8.5 Mb/s"
	pass "$msg"
}

# sc_redundant: 8 % loss on every link; redundancy on (2 copies) keeps ping
# loss < 3 % and below off (off varies 2.8-5 %: fast re-send). Expected ~1.3 %: each direction is
# lost only when both copies are (0.08^2); runs measured 0.6-2.2 % (burstier than binomial).
sc_redundant() {
	[ "$NETEM" = 0 ] && fail "redundant: needs netem"
	for n in 1 2 3; do netem_set "$n" rate 8mbit delay 60ms 20ms loss 8%; done
	echo "NOTE  netem on carrier1-3: rate 8mbit delay 60ms 20ms loss 8%"
	for r in off on; do
		x hub xs-hubctl settings -redundancy "$r" >/dev/null || fail "redundant: xs-hubctl settings -redundancy $r"
		sleep 2
		ping_probe 1000 0.02
		wait_for 60 ping_done || fail "redundant: ping did not finish"
		ping_stats 1000 0.02
		eval "res_$r=\$PSTAT loss_$r=\$LOSS"
	done
	srv=$(x server sh -c 'xs-ctl sessions -json | jq -r ".[0].bond.redundancy"' | tr -d '\r')
	msg="redundant (8% loss, ICMP 50/s): off: $res_off; on: $res_on (server redundancy $srv)"
	[ "$srv" = on ] || fail "$msg; server did not get SESSION_CONFIG"
	awk -v l="$loss_on" -v o="$loss_off" 'BEGIN { exit !(l < 3 && l < o) }' ||
		fail "$msg; want loss < 3% and below off with redundancy on"
	pass "$msg"
}

# sc_speedtest: per-relay results within 15 % of the 8 Mb/s netem rate, then
# all relays at once.
sc_speedtest() {
	[ "$NETEM" = 0 ] && fail "speedtest: needs netem"
	summary=""
	for n in 1 2 3; do
		out=$(dc exec -T -e R="relay$n" hub sh -c \
			'xs-hubctl speedtest -path "$R" -dir both -secs 5 -max-mb 10 2>/dev/null | jq -r ".down_mbps, .up_mbps"' |
			tr -d '\r') || fail "speedtest: xs-hubctl speedtest -path relay$n"
		set -- $out
		down=${1:-0} upl=${2:-0}
		summary="$summary relay$n ${down}/${upl}"
		for v in $down $upl; do
			awk -v v="$v" 'BEGIN { exit !(v >= 6.8 && v <= 9.2) }' ||
				fail "speedtest: relay$n down/up $down/$upl Mb/s, want 8 +-15%"
		done
	done
	all=$(x hub sh -c 'xs-hubctl speedtest -all -dir both -secs 10 -max-mb 10 2>/dev/null | jq -r ".down_mbps, .up_mbps"' |
		tr -d '\r' | tr '\n' ' ') || fail "speedtest: -all"
	n=$(x hub sh -c 'xs-hubctl speedtest -results | jq length' | tr -d '\r')
	[ "$n" = 4 ] || fail "speedtest: $n results stored, want 4"
	pass "speedtest (down/up Mb/s, netem 8):$summary; all relays $all; $n results stored"
}

# sc_aggregation: 200-byte UDP upload (iperf3); outer datagrams with and
# without aggregation.
sc_aggregation() {
	pkts() { hubq '[.relays[].bond.tx_pkts] | add'; }
	for a in false true; do
		x hub xs-hubctl settings -aggregation="$a" >/dev/null || fail "aggregation: settings -aggregation=$a"
		sleep 1
		b=$(pkts)
		x client iperf3 -c "$IPERF" -u -l 200 -b 4M -t 10 >/dev/null 2>&1 || fail "aggregation: iperf3 -u"
		eval "n_$a=$(($(pkts) - b))"
	done
	red=$(awk -v a="$n_true" -v b="$n_false" 'BEGIN { printf "%.0f", (1 - a / b) * 100 }')
	msg="aggregation (iperf3 UDP 200 B, 4 Mb/s, 10 s): $n_false datagrams without, $n_true with, ${red}% fewer"
	[ "$red" -ge 30 ] || fail "$msg, want >= 30%"
	pass "$msg"
}

sc_forget() {
	x hub xs-hubctl forget relay1 >/dev/null || fail "forget: xs-hubctl forget relay1"
	wait_for 5 relay_log_has "exit 3" || fail "forget: relay1 was not told to forget the hub"
	start_relay 1 "$QR1"
	wait_for 10 relay_log_has "exit 2" || fail "forget: reconnect with the old code was not rejected"
	relay_log_has "rejected by hub: forgotten" || fail "forget: reject reason is not 'forgotten'"
	pass "forget: xs-hubctl forget relay1 -> relay told to forget; reconnect rejected (forgotten)"
}

# origin_peer <tag>: the client address the origin nginx logged for a request with tag.
origin_peer() {
	dc logs --no-log-prefix origin 2>/dev/null | grep -E "^[0-9.]+ .*\"GET /$1 " | tail -n 1 | awk '{ print $1 }' | tr -d '\r'
}

# resolve <name>: the address the hub DNS forwarder gives the client for name.
resolve() {
	x client nslookup "$1" 10.43.0.1 2>/dev/null | awk '/^Address/ { a = $NF } END { print a }' | tr -d '\r'
}

# canary_nx: the hub DNS forwarder answers the Firefox DoH canary NXDOMAIN.
canary_nx() { x client nslookup use-application-dns.net 10.43.0.1 2>&1 | grep -q NXDOMAIN; }

# sc_bypass: a domain on the bypass list exits through a relay's carrier IP,
# other traffic through the server. The client asks the DoH canary first
# (as Firefox does): forwarded while the list is empty, NXDOMAIN once it is set.
sc_bypass() {
	x hub xs-hubctl bypass clear >/dev/null || fail "bypass: xs-hubctl bypass clear"
	cip=$(resolve use-application-dns.net)
	[ "$cip" = "$ORIGIN3" ] || fail "bypass: canary resolved to '$cip' with an empty list, want $ORIGIN3 (forwarded)"
	x hub xs-hubctl bypass set bypass.test >/dev/null || fail "bypass: xs-hubctl bypass set"
	canary_nx || fail "bypass: use-application-dns.net not NXDOMAIN while the bypass list is set"
	proxy_on() { hubq .direct.proxy | grep -q true; }
	wait_for 10 proxy_on || fail "bypass: relays' proxy role not on in BONDED"
	ip=$(resolve bypass.test)
	[ "$ip" = "$ORIGIN2" ] || fail "bypass: bypass.test resolved to '$ip' through the hub DNS, want $ORIGIN2"
	tag="bp$$"
	x client curl -s -o /dev/null --max-time 20 "http://$ip/$tag" || fail "bypass: curl http://$ip"
	peer=$(origin_peer "$tag")
	case "$peer" in 172.30.0.11 | 172.30.0.12 | 172.30.0.13) ;;
	*) fail "bypass: the origin saw '$peer' for bypass.test, want a carrier IP (172.30.0.11-13)" ;;
	esac
	ip2=$(resolve tunnel.test)
	[ "$ip2" = "$ORIGIN3" ] || fail "bypass: tunnel.test resolved to '$ip2', want $ORIGIN3"
	tag2="tn$$"
	x client curl -s -o /dev/null --max-time 20 "http://$ip2/$tag2" || fail "bypass: curl http://$ip2"
	peer2=$(origin_peer "$tag2")
	[ "$peer2" = 172.30.0.2 ] || fail "bypass: the origin saw '$peer2' for tunnel.test, want the server 172.30.0.2"
	learned=$(dc exec -T hub sh -c 'xs-hubctl bypass | jq -r .learned' | tr -d '\r')
	pass "bypass: canary forwarded with an empty list, NXDOMAIN with bypass.test; bypass.test -> $ip left via carrier $peer, tunnel.test -> $ip2 via the server $peer2 (learned $learned)"
}

# hub_peer <tag> [curl args]: curl from the hub itself (its own traffic),
# then the address the origin saw for it ("" = the request failed).
hub_peer() {
	tag=$1
	shift
	x hub curl -s -o /dev/null --max-time 10 "$@" "http://$ORIGIN/$tag" || { echo ""; return; }
	origin_peer "$tag"
}
own_rules() { x hub ip rule show | grep -c -E "^(99|10[12]):" | tr -d '\r'; }
# hub_uplink: the hub's own uplink (device of its main default route).
hub_uplink() { x hub ip -4 route show default | awk '{ for (i = 1; i < NF; i++) if ($i == "dev") { print $(i + 1); exit } }' | tr -d '\r'; }

# sc_owntraffic (phase 21): with own traffic on, the hub's own requests leave
# through the server in BONDED and through a relay's carrier in DIRECT; off
# (and with no path, the fallback) they use the hub's own uplink (carrier4,
# 172.30.0.14) and no own-traffic rule or IPv6 table is left.
sc_owntraffic() {
	p=$(hub_peer "own0$$")
	[ "$p" = 172.30.0.14 ] || fail "owntraffic: off: the origin saw '$p' for the hub, want its own uplink 172.30.0.14"
	x hub xs-hubctl settings -own-traffic on >/dev/null || fail "owntraffic: xs-hubctl settings -own-traffic on"
	[ "$(hubq .own_traffic)" = true ] || fail "owntraffic: status own_traffic not true"
	on() { [ "$(own_rules)" -ge 3 ]; } # 101, 102 and one bound rule (99) per uplink
	wait_for 10 on || fail "owntraffic: own-traffic rules not installed ($(x hub ip rule show | tr '\n' ';'))"
	x hub nft list table ip6 crossstream_hub >/dev/null 2>&1 || fail "owntraffic: IPv6 reject table missing"
	p=$(hub_peer "own1$$")
	[ "$p" = 172.30.0.2 ] || fail "owntraffic: BONDED: the origin saw '$p' for the hub, want the server 172.30.0.2"
	tip=$(hubq .tunnel_ip)
	a=$(x hub nslookup tunnel.test "$tip" 2>/dev/null | awk '/^Address/ { a = $NF } END { print a }' | tr -d '\r')
	[ "$a" = "$ORIGIN3" ] || fail "owntraffic: hub DNS on the tunnel address $tip answered '$a' for tunnel.test, want $ORIGIN3"
	pass "owntraffic: on, BONDED: the hub's own request left via the server ($p); hub DNS answers on $tip; IPv6 reject table present"
	# A socket bound to the uplink (NetworkManager's connectivity check) keeps it.
	up=$(hub_uplink)
	x hub ip rule show | grep -q -E "^99:.*oif $up lookup main" || fail "owntraffic: no bound rule for the uplink '$up'"
	p=$(hub_peer "own1b$$" --interface "$up")
	[ "$p" = 172.30.0.14 ] || fail "owntraffic: curl --interface $up: the origin saw '$p', want the hub's own uplink 172.30.0.14"
	pass "owntraffic: on: a socket bound to $up leaves through it ($p), as NetworkManager's check needs"

	dc stop -t 2 server >"$OUT/server-stop.log" 2>&1 || fail "owntraffic: docker compose stop server"
	wait_for 30 mode_is DIRECT || fail "owntraffic: no DIRECT 30 s after the server stopped (mode $(hubq .mode))"
	p=$(hub_peer "own2$$")
	case "$p" in 172.30.0.11 | 172.30.0.12 | 172.30.0.13) ;;
	*) fail "owntraffic: DIRECT: the origin saw '$p' for the hub, want a relay carrier (172.30.0.11-13)" ;;
	esac
	pass "owntraffic: on, DIRECT: the hub's own request left via relay carrier $p"

	for n in 1 2 3; do kill_relay "$n"; done
	offline() { mode_is OFFLINE; }
	wait_for 30 offline || fail "owntraffic: not OFFLINE after the relays stopped (mode $(hubq .mode))"
	gone() { [ "$(own_rules)" = 0 ]; }
	wait_for 10 gone || fail "owntraffic: OFFLINE: own-traffic rules still there (fallback wanted)"
	p=$(hub_peer "own3$$")
	[ "$p" = 172.30.0.14 ] || fail "owntraffic: OFFLINE: the origin saw '$p', want the hub's own uplink 172.30.0.14 (fallback)"
	pass "owntraffic: on, OFFLINE: rules removed, the hub's own request used its own uplink (fallback)"

	x hub xs-hubctl settings -own-traffic off >/dev/null || fail "owntraffic: xs-hubctl settings -own-traffic off"
	[ "$(own_rules)" = 0 ] || fail "owntraffic: off: own-traffic rules left"
	x hub nft list table ip6 crossstream_hub >/dev/null 2>&1 && fail "owntraffic: off: IPv6 reject table left"
	r=$(x hub ip route get "$ORIGIN" | tr -d '\r')
	case "$r" in *xs0*) fail "owntraffic: off: the hub routes $ORIGIN through xs0 ($r)" ;; esac
	pass "owntraffic: off: no own-traffic rule, no IPv6 table, $ORIGIN not routed through xs0"
}

# fq <jq filter>: query xs-hubctl forwards inside the hub.
fq() { dc exec -T -e F="$1" hub sh -c 'xs-hubctl forwards | jq -r "$F"' | tr -d '\r'; }

# sc_portfwd: an internet host reaches a web server on the client through
# the server's public port; a port over the allowance is refused; the DNAT
# goes with the session. The "internet host" is 192.0.2.31 on the iperf
# container: Docker drops the client's replies on the lan bridge when they are
# addressed to another Docker network's subnet (172.30.0.0/24), which a real
# internet address never is.
sc_portfwd() {
	x iperf ip addr add "$NET_HOST/32" dev eth0 || fail "portfwd: address $NET_HOST on iperf"
	x server ip route replace "$NET_HOST/32" via "$IPERF" || fail "portfwd: server route to $NET_HOST"
	x server xs-ctl forward allow hub1 1 >/dev/null || fail "portfwd: xs-ctl forward allow hub1 1"
	dc exec -d -T client sh -c 'mkdir -p /tmp/www && echo fwd-ok >/tmp/www/index.html && exec busybox-extras httpd -f -p 8080 -h /tmp/www'
	x hub xs-hubctl forwards set tcp/8080=10.43.0.20 tcp/8081=10.43.0.20 >/dev/null || fail "portfwd: xs-hubctl forwards set"
	fwd_active() { [ "$(fq '.forwards[0].state')" = active ]; }
	wait_for 15 fwd_active || fail "portfwd: tcp/8080 is '$(fq '.forwards[0].state')', want active"
	r2=$(fq '.forwards[1].state + "/" + .forwards[1].reason')
	[ "$r2" = refused/not_allowed ] || fail "portfwd: tcp/8081 is $r2, want refused/not_allowed (allowance 1)"
	body=$(x iperf curl -sS --max-time 10 --interface "$NET_HOST" http://172.30.0.2:8080/ 2>&1 | tr -d '\r')
	[ "$body" = fwd-ok ] || fail "portfwd: $NET_HOST -> 172.30.0.2:8080 gave '$body', want fwd-ok"
	x server xs-ctl forward list | grep -q "tcp/8080" || fail "portfwd: xs-ctl forward list lacks tcp/8080"
	x server xs-ctl revoke hub1 >/dev/null || fail "portfwd: xs-ctl revoke hub1"
	dnat_gone() { ! x server nft list chain ip crossstream prerouting | grep -q dnat; }
	wait_for 5 dnat_gone || fail "portfwd: server DNAT rule left after the session ended"
	pass "portfwd: internet host $NET_HOST fetched '$body' from the client via server :8080; tcp/8081 over the allowance refused; DNAT removed with the session"
}

# sc_prio_cap: relay3's owner allows only backup use: it carries nothing
# while the others are alive, even when the hub asks for normal.
sc_prio_cap() {
	kill_relay 3
	start_relay 3 "$QR3" "-max-priority backup"
	prio3() { [ "$(hubq '.relays[] | select(.name=="relay3") | .priority')" = backup ]; }
	wait_for 20 prio3 || fail "prio-cap: relay3 effective priority is not backup"
	wait_for 20 connected 3 || fail "prio-cap: relay3 not reconnected"
	setpath relay3 -priority normal
	sleep 2
	prio3 || fail "prio-cap: hub setting normal overrode the relay's cap"
	b3=$(pv 3 rx_data)
	dl 30
	d3=$(($(pv 3 rx_data) - b3))
	[ "$d3" = 0 ] || fail "prio-cap: relay3 carried $d3 data bytes with relay1+2 alive"
	pass "prio-cap: relay3 cap backup, hub set normal -> effective $(hubq '.relays[] | select(.name=="relay3") | .priority'), 0 bytes of a 30 MB download ($DL_MBPS Mb/s)"
}

# uq <jq filter>: query the hub's local link on cell4 (xs-hubctl uplinks).
uq() { dc exec -T -e F="$1" hub sh -c 'xs-hubctl uplinks | jq -r "[.[] | select(.addr // \"\" | startswith(\"172.31.4.\"))][0] | $F"' | tr -d '\r'; }
local_alive() { [ "$(uq '.bond.state // ""')" = alive ]; }
local_gone() { [ "$(hubq '[.uplinks[] | select(.state == "absent")] | length')" -ge 1 ]; }

# sc_localpath: the hub's own interface on cell4 (carrier4) is found
# disabled, bonded with the 3 relays once enabled, then unplugged (link
# down) during a download: no stall over 1 s; plugged back it returns.
sc_localpath() {
	id=$(uq .id)
	[ -n "$id" ] && [ "$id" != null ] || fail "localpath: cell4 interface not listed by xs-hubctl uplinks"
	[ "$(uq .state)" = off ] || fail "localpath: new local link not off by default (state $(uq .state))"
	ifc=$(uq .iface)
	x hub xs-hubctl uplinks set "$id" enabled=true >/dev/null || fail "localpath: enable $id"
	wait_for 20 local_alive || fail "localpath: local path not alive (state $(uq .state), bond $(uq '.bond.state'))"
	alive 3 || fail "localpath: relays not alive next to the local path"
	monitored_dl 60
	sleep 10
	b=$(uq '.bond.rx_data // 0')
	x hub ip link set "$ifc" down || fail "localpath: ip link set $ifc down"
	echo "NOTE  $ifc (local link $id) down at 10 s, it had carried $b data bytes"
	wait_for 600 dl_done || fail "localpath: download did not finish"
	res=$(dl_res)
	eval "$res"
	stall_s=$(awk -v t="$max_stall" 'BEGIN { printf "%.1f", t / 10 }')
	msg="localpath: 3 relays + local link $id ($ifc) bonded, 60 MB, link down at 10 s, max stall ${stall_s} s"
	[ "$rc" = 0 ] && [ "$size" = 60000000 ] || fail "$msg (curl rc=$rc size=$size)"
	[ "$b" -gt 0 ] || fail "$msg; the local path carried no data before the unplug"
	[ "$max_stall" -lt 10 ] || fail "$msg, want < 1 s"
	wait_for 20 local_gone || fail "$msg; link not reported absent"
	x hub sh -c "ip link set $ifc up && ip route replace default via 172.31.4.254 dev $ifc metric 500" || fail "localpath: re-plug $ifc"
	wait_for 30 local_alive || fail "$msg; local path not back after the re-plug (state $(uq .state))"
	pass "$msg; absent after unplug, alive again after re-plug"
}

# portal_on / portal_off: carrier4 plays a hotel network before and after
# the login: HTTP from the hub's local uplink goes to the "portal" (origin
# :8080 answers 302 to its login page) and the server is unreachable.
portal_on() {
	x carrier4 sh -c 'i=$(xs-iface 172.31.4.) && printf "%s\n" "table ip portal {" \
		"chain redirect80 {" "type nat hook prerouting priority -110; policy accept;" \
		"iifname \"$i\" tcp dport 80 dnat to 172.30.0.42:8080" "}" \
		"chain blockserver {" "type filter hook forward priority -10; policy accept;" \
		"iifname \"$i\" ip daddr 172.30.0.2 drop" "}" "}" | nft -f -'
}
portal_off() { x carrier4 nft delete table ip portal; }

# sc_captive: the hub's local uplink is behind a login page: it is found
# captive when enabled and held down, xs-hubctl shows the page and routes
# it through the link (-open), and the path comes up by itself after the
# login (recheck every 30 s).
sc_captive() {
	id=$(uq .id)
	ifc=$(uq .iface)
	[ -n "$id" ] && [ "$id" != null ] || fail "captive: cell4 interface not listed by xs-hubctl uplinks"
	portal_on || fail "captive: portal rules on carrier4"
	x hub xs-hubctl uplinks set "$id" enabled=true >/dev/null || fail "captive: enable $id"
	is_captive() { [ "$(uq .state)" = captive ]; }
	wait_for 20 is_captive || fail "captive: $id not captive (state $(uq .state), probe $(uq '.captive.state // ""'))"
	url=$(uq .captive.url)
	[ "$url" = "http://172.30.0.42:8080/login" ] || fail "captive: login page '$url', want http://172.30.0.42:8080/login"
	[ "$(uq '.bond.state // ""')" = "" ] || fail "captive: path not held down ($(uq .bond.state))"
	out=$(x hub xs-hubctl uplinks captive "$id" -open 2>&1 || true) # no browser in the container
	case "$out" in *"login page http://172.30.0.42:8080/login"*"routed through this link"*) ;;
	*) fail "captive: xs-hubctl uplinks captive $id -open said: $out" ;;
	esac
	x hub ip route show 172.30.0.42/32 | grep -q "dev $ifc" || fail "captive: no host route to the portal via $ifc"
	page=$(x hub curl -s --max-time 5 "$url" | tr -d '\r')
	[ "$page" = hotel-login ] || fail "captive: login page through the route gave '$page'"
	portal_off || fail "captive: remove the portal rules (login)"
	wait_for 60 local_alive || fail "captive: path not alive 60 s after the login (state $(uq .state), probe $(uq '.captive.state // ""'))"
	alive 3 || fail "captive: relays not alive next to the local path"
	pass "captive: $id ($ifc) found behind $url and held down, portal routed through it (-open), path alive after the login"
}

# sc_tcploss (phase 20): relay1 alone and forced to TCP (UDP to the server
# dropped on carrier1), netem TCPL_RATE (20 Mb/s) 60±10 ms. TCPL_MB download
# with one TCP connection (relay override -tcp-conns 1) and with the hub's
# choice (2, or 4 on heavy loss) at 0 % and 2 % loss. Bars, both against one
# connection in the same run: 2 % loss ≥ 0.85× (the hub's connections must not
# stall below one alone: before 2026-11-27 they gave ~0.75×; now 0.91–1.28×,
# one connection alone being good), 0 % loss ≥ 0.85×; each
# cell is the median of three downloads. A bar against the 0 % result
# (≥ 0.75×) depended on the machine (CI VM 0.58×, Dell 0.75–0.78×, local
# 0.74–0.95× on the same code) and went with the owner's OK.
# The old 2 % bar (≥ 1.3× one connection) stopped making sense once one
# connection reached 13 Mb/s of a ~17 Mb/s ceiling (2026-11-26 bond fixes:
# one connection 4.95 → 12.3–12.7, hub's choice 13.11 → 15.4–16.1 Mb/s locally).
sc_tcploss() {
	[ "$NETEM" = 0 ] && fail "tcploss: needs netem"
	kill_relay 2
	kill_relay 3
	x carrier1 sh -c 'printf "%s\n" "table ip block {" "chain forward {" \
		"type filter hook forward priority 0; policy accept;" "ip daddr 172.30.0.2 meta l4proto udp drop" "}" "}" | nft -f -' ||
		fail "tcploss: nft rule on carrier1"
	on_tcp() { [ "$(hubq '.relays[] | select(.name=="relay1") | .transport // ""')" = tcp ] && state_is 1 alive && tunnel_up; }
	conns_now() { hubq '.relays[] | select(.name=="relay1") | .stats.tcp_conns // 0'; }
	wait_for 90 on_tcp || fail "tcploss: relay1 not alive on tcp (transport $(hubq '.relays[] | select(.name=="relay1") | .transport'))"
	rate=${TCPL_RATE:-20mbit}
	for mode in one hub; do
		kill_relay 1
		x relay1 sh -c ': >/var/log/relay.log'
		if [ "$mode" = one ]; then start_relay 1 "$QR1" "-tcp-conns 1"; else start_relay 1 "$QR1"; fi
		wait_for 90 on_tcp || fail "tcploss: relay1 ($mode) not alive on tcp"
		if [ "$mode" = one ]; then want_conns() { [ "$(conns_now)" = 1 ]; }; else want_conns() { [ "$(conns_now)" -ge 2 ]; }; fi
		wait_for 15 want_conns || fail "tcploss: relay1 ($mode) has $(conns_now) tcp connections"
		for l in 0 2; do
			netem_set 1 rate "$rate" delay 60ms 10ms loss "$l%"
			sleep 2
			runs=""
			for _ in 1 2 3; do
				dl "${TCPL_MB:-20}"
				runs="$runs $DL_MBPS"
			done
			# median of three: one connection at 2 % loss alone ranged 8-12 Mb/s
			med=$(printf '%s\n' $runs | sort -n | sed -n 2p)
			eval "M_${mode}_$l=\$med"
			echo "      tcploss $mode loss=$l%: median $med Mb/s of$runs, $(conns_now) tcp connections"
		done
	done
	ratio_ok "$M_hub_2" "$M_one_2" 0.85 || fail "tcploss: 2 % loss ${M_hub_2} Mb/s with the hub's choice vs ${M_one_2} with one connection (< 0.85×)"
	ratio_ok "$M_hub_0" "$M_one_0" 0.85 || fail "tcploss: 0 % loss ${M_hub_0} Mb/s vs ${M_one_0} with one connection (< 0.85×)"
	pass "tcploss: 2 % loss ${M_one_2} → ${M_hub_2} Mb/s, 0 % ${M_one_0} → ${M_hub_0} Mb/s (1 connection → hub's choice)"
}

# sc_tcpconns (phase 19 G spike, on demand): relay1 alone and forced to TCP
# (UDP to the server dropped on carrier1), download through the tunnel with
# 1, 2 and 4 TCP connections per path at netem loss 0/1/2/5 %. Prints the
# numbers (SPIKE lines) for docs/decisions.md; no pass bar.
sc_tcpconns() {
	[ "$NETEM" = 0 ] && fail "tcpconns: needs netem"
	kill_relay 2
	kill_relay 3
	x carrier1 sh -c 'printf "%s\n" "table ip block {" "chain forward {" \
		"type filter hook forward priority 0; policy accept;" "ip daddr 172.30.0.2 meta l4proto udp drop" "}" "}" | nft -f -' ||
		fail "tcpconns: nft rule on carrier1"
	on_tcp() { [ "$(hubq '.relays[] | select(.name=="relay1") | .transport // ""')" = tcp ] && state_is 1 alive && tunnel_up; }
	wait_for 90 on_tcp || fail "tcpconns: relay1 not alive on tcp (transport $(hubq '.relays[] | select(.name=="relay1") | .transport'))"
	rate=${TCPC_RATE:-20mbit}
	table=""
	for conns in 1 2 4; do # not "c": netem_set sets it
		kill_relay 1
		x relay1 sh -c ': >/var/log/relay.log'
		start_relay 1 "$QR1" "-tcp-conns $conns"
		wait_for 90 on_tcp || fail "tcpconns: relay1 with $conns connections not alive on tcp"
		for l in 0 1 2 5; do
			netem_set 1 rate "$rate" delay 60ms 10ms loss "$l%"
			sleep 2
			dl "${TCPC_MB:-20}"
			echo "SPIKE tcp-conns=$conns loss=$l% rate=$rate: ${DL_MBPS} Mb/s (${DL_S} s)"
			table="$table c$conns/l$l=${DL_MBPS}"
		done
	done
	pass "tcpconns:$table"
}

# iperf_bg <secs> [-R]: one iperf3 TCP flow in the background; iperf_mbps
# waits for it and prints Mb/s.
iperf_bg() {
	dc exec -d -T -e T="$1" -e R="${2:-}" -e S="$IPERF" client sh -c \
		'rm -f /tmp/ip.json /tmp/ip.done; iperf3 -c "$S" -t "$T" $R -J >/tmp/ip.json 2>&1; touch /tmp/ip.done'
}
iperf_done() { x client test -f /tmp/ip.done; }
iperf_mbps() {
	wait_for $(($1 + 30)) iperf_done || fail "pep: iperf3 did not finish"
	x client sh -c "jq '.end.sum_received.bits_per_second / 1e6' /tmp/ip.json" | tr -d '\r' || fail "pep: iperf3 result"
}

# sc_pep: lossy mixed-RTT links; curl PEP_MB and iperf3 (one flow, both
# directions) with PEP off, then on. Prints both numbers and the gain; the
# phase 13 default is on only if the gain is >= 20 % (decisions.md).
sc_pep() {
	[ "$NETEM" = 0 ] && fail "pep: needs netem"
	netem_set 1 rate 8mbit delay 20ms loss 1%
	netem_set 2 rate 8mbit delay 30ms loss 1%
	netem_set 3 rate 2mbit delay 150ms loss 1%
	echo "NOTE  links 8/8/2 Mb/s, RTT 40/60/300 ms, 1% loss each way"
	mb=${PEP_MB:-200}
	sleep 3
	for m in off on; do
		pep_set "$m"
		sleep 1
		dl "$mb"
		eval "curl_$m=\$DL_MBPS"
		sleep 2
		iperf_bg "$IPERF_S" -R
		sleep 5
		flows=$(hubq '.pep.flows // 0')
		down=$(iperf_mbps "$IPERF_S")
		sleep 2
		iperf_bg "$IPERF_S"
		up=$(iperf_mbps "$IPERF_S")
		eval "down_$m=\$down up_$m=\$up flows_$m=\$flows"
		sleep 2
	done
	[ "$flows_off" = 0 ] || fail "pep: $flows_off PEP flows while PEP was off"
	[ "$flows_on" -ge 1 ] || fail "pep: no PEP flow during iperf3 with PEP on"
	g() { awk -v a="$1" -v b="$2" 'BEGIN { printf "%+.0f%%", (a / b - 1) * 100 }'; }
	pass "pep off: curl $mb MB ${curl_off} Mb/s, iperf3 down $(printf '%.2f' "$down_off") up $(printf '%.2f' "$up_off") Mb/s"
	pass "pep on:  curl $mb MB ${curl_on} Mb/s, iperf3 down $(printf '%.2f' "$down_on") up $(printf '%.2f' "$up_on") Mb/s ($flows_on PEP flow)"
	pass "pep gain: curl $(g "$curl_on" "$curl_off"), iperf3 down $(g "$down_on" "$down_off"), up $(g "$up_on" "$up_off")"
}

# Web panel (phase 15): curl inside the server container against
# https://127.0.0.1:8443 (the server listens on 0.0.0.0:8443 with
# web_allow_public, like bridge-mode compose), cookie jar /tmp/wc.
WEB=https://127.0.0.1:8443
wc() { x server curl -sk --max-time 10 -b /tmp/wc -c /tmp/wc "$@"; }
wpost() { # wpost <path> <json> [csrf]: POST, prints body then the HTTP code on its own line
	wc -X POST -H 'Content-Type: application/json' -H "X-CSRF-Token: ${3:-}" -d "$2" -w '\n%{http_code}' "$WEB/api/$1"
}
jqs() { dc exec -T server jq "$@" | tr -d '\r'; } # jq runs in the server container (none on the host)
wget_() { wc -w '\n%{http_code}' "$WEB/api/$1"; } # body, then the HTTP code
code_of() { printf '%s\n' "$1" | tail -n 1 | tr -d '\r'; }
body_of() { printf '%s\n' "$1" | sed '$d'; }
web_paths() { wc "$WEB/api/sessions" | jqs -r --arg h "$1" '[.[] | select(.hub == $h) | .paths[]] | length'; }
web_names() { wc "$WEB/api/sessions" | jqs -r --arg h "$1" '[.[] | select(.hub == $h) | .paths[].name] | sort | join(",")'; }

sc_webui() {
	echo "testpass1" | dc exec -T server xs-ctl admin add owner -password-stdin >/dev/null || fail "webui: xs-ctl admin add owner"
	x server xs-ctl admin list | grep -q '^owner' || fail "webui: xs-ctl admin list lacks owner"
	hdr=$(x server curl -sk --max-time 10 -D - -o /tmp/index.html "$WEB/" | tr -d '\r')
	echo "$hdr" | grep -qi "^content-security-policy: default-src 'self'" || fail "webui: no CSP header on /"
	echo "$hdr" | grep -qi "unsafe-" && fail "webui: CSP allows unsafe-*"
	x server grep -q 'src="./assets/' /tmp/index.html || fail "webui: index.html is not the built panel with relative URLs (node stage)"
	x server grep -qiE '<script>|<style|style=' /tmp/index.html && fail "webui: inline script or style in index.html"
	# Phase 22: the built bundle carries both languages and the switch (no browser here: the
	# English rendering itself is checked by hand); its CSP header is the page's.
	js=$(x server sed -n 's|.*src="\./\(assets/[^"]*\.js\)".*|\1|p' /tmp/index.html | head -n 1 | tr -d '\r')
	[ -n "$js" ] || fail "webui: no script in index.html"
	jshdr=$(x server curl -sk --max-time 10 -D - -o /tmp/panel.js "$WEB/$js" | tr -d '\r')
	[ "$(echo "$jshdr" | grep -i '^content-security-policy:')" = "$(echo "$hdr" | grep -i '^content-security-policy:')" ] ||
		fail "webui: CSP header of $js differs from the page's"
	for s in 'Server panel' 'Panel serwera' 'Log out' 'xs-lang'; do
		x server grep -qF "$s" /tmp/panel.js || fail "webui: panel bundle lacks '$s' (English/Polish dictionaries, language switch)"
	done
	r=$(wget_ hubs)
	[ "$(code_of "$r")" = 401 ] || fail "webui: /api/hubs without login gave $(code_of "$r"), want 401"
	login=$(x server curl -sk --max-time 10 -D /tmp/lh -c /tmp/wc -H 'Content-Type: application/json' \
		-d '{"user":"owner","password":"testpass1"}' "$WEB/api/login")
	csrf=$(printf '%s' "$login" | jqs -r .csrf)
	[ -n "$csrf" ] && [ "$csrf" != null ] || fail "webui: login failed: $login"
	ck=$(x server grep -i '^set-cookie' /tmp/lh | tr -d '\r')
	for f in "__Host-xs_session=" "Secure" "HttpOnly" "SameSite=Strict" "Max-Age=43200"; do
		case "$ck" in *"$f"*) ;; *) fail "webui: session cookie lacks $f ($ck)" ;; esac
	done
	# Live sessions with relay names (PATH_NAME) and the SSE stream.
	[ "$(web_paths hub1)" = 3 ] || fail "webui: /api/sessions shows $(web_paths hub1) paths for hub1, want 3"
	[ "$(web_names hub1)" = relay1,relay2,relay3 ] || fail "webui: path names '$(web_names hub1)', want relay1,relay2,relay3"
	sse=$(wc -N --max-time 3 "$WEB/api/stream" 2>/dev/null || true)
	echo "$sse" | grep -q '^event: history' || fail "webui: SSE stream sent no history event"
	echo "$sse" | grep -q '^event: tick' || fail "webui: SSE stream sent no tick within 3 s"
	# Writes need the CSRF token.
	r=$(wpost hubs/enroll '{"name":"hub2"}')
	[ "$(code_of "$r")" = 403 ] || fail "webui: enroll without CSRF token gave $(code_of "$r"), want 403"
	r=$(wpost hubs/enroll '{"name":"hub2"}' "$csrf")
	[ "$(code_of "$r")" = 200 ] || fail "webui: enroll hub2: $r"
	body_of "$r" | jqs -r .code | grep -q '^XS1\.' || fail "webui: enroll hub2 returned no XS1 code"
	# Rename: the hub reconnects under the new name with its 3 paths.
	r=$(wpost hubs/rename '{"name":"hub1","new_name":"hub1b"}' "$csrf")
	[ "$(code_of "$r")" = 200 ] || fail "webui: rename hub1 -> hub1b: $r"
	renamed() { [ "$(web_paths hub1b)" = 3 ]; }
	wait_for 30 renamed || fail "webui: hub1b back with 3 paths after rename (got $(web_paths hub1b))"
	wait_for 20 tunnel_up || fail "webui: tunnel up again after rename"
	r=$(wpost hubs/forward '{"name":"hub1b","n":2}' "$csrf")
	[ "$(code_of "$r")" = 200 ] || fail "webui: forward allowance: $r"
	# Hubs and the networks behind them never reach the panel: the packet filter drops TCP to
	# the server host (000 = no connection); the panel's own 403 for tunnel sources is the second line.
	tc=$(x client curl -sk --max-time 10 -o /dev/null -w '%{http_code}' https://100.64.0.1:8443/ 2>/dev/null | tr -d '\r' || true)
	[ "$tc" = 000 ] || [ "$tc" = 403 ] || fail "webui: client through the tunnel got HTTP '$tc' from the panel, want no connection (packet filter) or 403"
	# Audit log and redacted logs.
	audit=$(wc "$WEB/api/audit?n=50")
	for a in enroll rename forward_allow login; do
		ok=$(printf '%s' "$audit" | jqs -r --arg a "$a" 'any(.[]; .action == $a and .actor == "web:owner@127.0.0.1" and .ok)')
		[ "$ok" = true ] || fail "webui: audit log lacks an ok '$a' by web:owner@127.0.0.1"
	done
	logs=$(wc "$WEB/api/logs")
	[ "$(printf '%s' "$logs" | jqs length)" -gt 0 ] || fail "webui: /api/logs is empty"
	printf '%s' "$logs" | grep -q 'XS1\.[A-Za-z0-9]' && fail "webui: an enrollment code is visible in /api/logs"
	x server xs-ctl audit | grep -q 'web:owner@127.0.0.1' || fail "webui: xs-ctl audit lacks the web actions"
	# Revoke closes the session at once.
	r=$(wpost hubs/revoke '{"name":"hub1b"}' "$csrf")
	[ "$(code_of "$r")" = 200 ] || fail "webui: revoke hub1b: $r"
	gone() { [ "$(wc "$WEB/api/hubs" | jqs -r '[.[] | select(.name == "hub1b")] | length')" = 0 ] && ! tunnel_up; }
	wait_for 15 gone || fail "webui: hub1b still listed or tunnel still up after revoke"
	# A code from the panel enrolls the hub again.
	r=$(wpost hubs/enroll '{"name":"hub3"}' "$csrf")
	code=$(body_of "$r" | jqs -r .code)
	# A revoked hub enrolls again with a new code as it is (phase 18): xs-hubd
	# replaces server.json and restarts itself, the relays reconnect.
	x hub xs-hubctl enroll "$code" >"$OUT/enroll3.log" 2>&1 || fail "webui: xs-hubctl enroll with the panel's code ($(cat "$OUT/enroll3.log"))"
	sleep 2
	wait_for 30 x hub xs-hubctl status || fail "webui: xs-hubd back after its restart"
	wait_for 40 connected 3 || fail "webui: 3 relays reconnected to the restarted hub"
	wait_for 30 tunnel_up || fail "webui: tunnel up after enrolling again with the panel's code"
	back() { [ "$(web_paths hub3)" = 3 ]; }
	wait_for 20 back || fail "webui: hub3 shows $(web_paths hub3) paths, want 3"
	r=$(wpost logout '{}' "$csrf")
	r=$(wget_ hubs)
	[ "$(code_of "$r")" = 401 ] || fail "webui: /api/hubs after logout gave $(code_of "$r"), want 401"
	# 5 wrong passwords lock the address out.
	for i in 1 2 3 4 5; do wpost login '{"user":"owner","password":"wrong-pass"}' >/dev/null; done
	r=$(wpost login '{"user":"owner","password":"testpass1"}')
	[ "$(code_of "$r")" = 429 ] || fail "webui: login after 5 failures gave $(code_of "$r"), want 429 (lockout)"
	pass "webui: login (cookie flags, CSP), hub1 paths relay1-3 live (SSE), CSRF enforced, rename/forward/revoke audited, hub re-enrolled with a panel code, tunnel clients refused, lockout"
}

run_one() {
	case " $SCENARIOS $ON_DEMAND " in *" $1 "*) ;; *) usage ;; esac
	(
		up
		"sc_$(echo "$1" | tr - _)" # prio-cap -> sc_prio_cap
		race_check
	)
}

# race_check (RACE=1): any race report written by a -race binary (GORACE
# log_path=/tmp/race in the image) fails the scenario.
race_check() {
	[ "${RACE:-0}" = 1 ] || return 0
	found=""
	rm -f "$OUT/race.txt"
	for c in server hub relay1 relay2 relay3 carrier1 carrier2 carrier3 client; do
		r=$(x "$c" sh -c 'cat /tmp/race.* 2>/dev/null' 2>/dev/null || true)
		[ -n "$r" ] || continue
		found="$found $c"
		printf '== %s\n%s\n' "$c" "$r" >>"$OUT/race.txt"
	done
	[ -z "$found" ] || fail "race: data race reported in$found (testenv/$OUT/race.txt)"
	pass "race: no data race reported (-race binaries)"
}

usage() {
	echo "usage: $0 $(echo "$SCENARIOS" | tr ' ' '|')|all|short" >&2
	exit 2
}

case "${1:-}" in
all | short)
	rc=0
	list=$SCENARIOS
	[ "$1" = short ] && list=$SHORT
	for s in $list; do
		echo "----  $s"
		KEEP=0 sh "./$(basename "$0")" "$s" || { rc=1; cp -r "$OUT" "$OUT.$s" 2>/dev/null || true; }
	done
	exit $rc
	;;
"") usage ;;
*)
	[ $# -eq 1 ] && { run_one "$1"; exit; }
	rc=0
	for s in "$@"; do
		echo "----  $s"
		KEEP=0 sh "./$(basename "$0")" "$s" || { rc=1; cp -r "$OUT" "$OUT.$s" 2>/dev/null || true; }
	done
	exit $rc
	;;
esac
