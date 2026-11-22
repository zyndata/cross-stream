#!/bin/sh
# Phase 17b: the HA app image run the way the Supervisor runs it (NET_ADMIN,
# /dev/net/tun, /data with options.json), without Home Assistant. Checks: the
# options reach xs-server, the server listens, xs-ctl enroll works, the ingress
# panel answers the allowed client (page, session, API write with the CSRF
# token, audit user) and refuses another container; /metrics answers; the
# tunnel port comes from the app's Network mapping (a fake Supervisor maps 51901).
# Needs the image crossstream/ha-app:smoke (make ha-smoke builds it).
set -eu
export MSYS_NO_PATHCONV=1 # Git Bash on Windows: keep container paths as they are

img=${HA_IMAGE:-crossstream/ha-app:smoke}
p=xs-ha-smoke
net=$p-net
app=172.31.250.2
client=172.31.250.10
foreign=172.31.250.11
curl_img=curlimages/curl:8.11.1

pass() { echo "PASS  $*"; }
fail() {
	echo "FAIL  $*"
	docker logs --tail 40 $p-app >&2 2>&1 || true
	exit 1
}
cleanup() {
	[ "${KEEP:-0}" = 1 ] && return
	docker rm -f $p-app $p-client $p-foreign $p-supervisor >/dev/null 2>&1 || true
	docker network rm $net >/dev/null 2>&1 || true
	docker volume rm $p-data >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup

docker network create --subnet 172.31.250.0/24 $net >/dev/null
docker volume create $p-data >/dev/null
# What the Supervisor writes from the app's options (pep off checks jq's false).
docker run --rm -i -v $p-data:/data alpine:3.22 sh -c 'cat >/data/options.json' <<'EOF'
{"public_endpoints":["vpn.example.org","203.0.113.5:51900"],"pep":false,"log_level":"info","min_hub_version":"","egress_allow":["192.168.88.0/24"]}
EOF

# A fake Supervisor (GET only): the app's Network section maps 51900 to host port 51901.
docker run -d --name $p-supervisor --network $net --ip 172.31.250.3 --network-alias supervisor alpine:3.22 sh -c "$(cat <<'SH'
apk add -q --no-cache busybox-extras >/dev/null
mkdir -p /www/addons/self
echo '{"result":"ok","data":{"network":{"51900/udp":51901,"51900/tcp":51901,"443/udp":null}}}' >/www/addons/self/info
exec busybox-extras httpd -f -p 80 -h /www
SH
)" >/dev/null
for _ in $(seq 60); do
	docker exec $p-supervisor wget -qO- http://127.0.0.1/addons/self/info >/dev/null 2>&1 && break
	sleep 1
done

# The Supervisor runs apps with --init. No sysctl here: a bridge container
# inherits the host's ip_forward=1 (Docker sets it), as on an HA host.
docker run -d --name $p-app --init --network $net --ip $app --cap-add NET_ADMIN --device /dev/net/tun \
	-v $p-data:/data -e XS_SERVER_WEB_INGRESS_ALLOW=$client -e SUPERVISOR_TOKEN=smoke "$img" >/dev/null
for _ in $(seq 30); do
	docker logs $p-app 2>&1 | grep -q 'ingress panel listening' && break
	sleep 1
done
logs=$(docker logs $p-app 2>&1)
echo "$logs" | grep -q 'ingress panel listening' || fail "app did not start"
echo "$logs" | grep -q 'listening' || fail "server log has no 'listening'"
pass "app started (server + ingress panel listening)"

env=$(docker exec $p-app sh -c 'tr "\0" "\n" </proc/$(pidof xs-server)/environ')
for want in XS_SERVER_PUBLIC_ENDPOINTS=vpn.example.org,203.0.113.5:51900 XS_SERVER_PEP=false \
	XS_SERVER_EGRESS_ALLOW=192.168.88.0/24 XS_SERVER_PORT_SOURCE=supervisor XS_SERVER_WEB_LISTEN=off XS_SERVER_METRICS_LISTEN=$app:9100; do
	echo "$env" | grep -qx "$want" || fail "options: $want not in the server environment"
done
pass "options.json -> XS_SERVER_* (lists joined, pep=false kept)"

info=$(docker exec $p-app xs-ctl info 2>&1 | tr -d '\r')
echo "$info" | grep -q 'udp://vpn.example.org:51901' && echo "$info" | grep -q 'tcp://vpn.example.org:51901' &&
	echo "$info" | grep -q 'udp://203.0.113.5:51900' && echo "$info" | grep -q 'in Home Assistant' ||
	fail "tunnel port 51901 from the Network mapping not in the code endpoints: $info"
pass "tunnel port from the Supervisor (51901) in the codes, an explicit host:port is kept"

code=$(docker exec $p-app xs-ctl enroll smoke1 2>/dev/null | tr -d '\r')
case "$code" in XS1.*) pass "xs-ctl enroll smoke1" ;; *) fail "xs-ctl enroll: $code" ;; esac

docker pull -q $curl_img >/dev/null
docker run -d --name $p-client --network $net --ip $client --entrypoint sleep $curl_img 300 >/dev/null
docker run -d --name $p-foreign --network $net --ip $foreign --entrypoint sleep $curl_img 300 >/dev/null
c() { docker exec $p-client curl -sS --max-time 5 "$@"; }

page=$(c "http://$app:8099/") || fail "panel page from the allowed client"
echo "$page" | grep -q 'src="./assets/' || fail "panel page has no relative assets: $page"
pass "panel page (relative asset URLs)"

sess=$(c -H 'X-Remote-User-Name: smoke' "http://$app:8099/api/session") || fail "session"
csrf=$(echo "$sess" | sed -n 's/.*"csrf":"\([^"]*\)".*/\1/p')
echo "$sess" | grep -q '"user":"smoke"' && echo "$sess" | grep -q '"ingress":true' && [ -n "$csrf" ] ||
	fail "session: $sess"
pass "session without login (user smoke, ingress)"

st=$(c -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
	-d '{"name":"smoke2"}' "http://$app:8099/api/hubs/enroll")
[ "$st" = 403 ] || fail "write without CSRF token: $st, want 403"
res=$(c -X POST -H 'Content-Type: application/json' -H "X-CSRF-Token: $csrf" -H 'X-Remote-User-Name: smoke' \
	-d '{"name":"smoke2"}' "http://$app:8099/api/hubs/enroll") || fail "enroll through the panel"
echo "$res" | grep -q '"code":"XS1\.' || fail "enroll through the panel: $res"
c "http://$app:8099/api/audit" | grep -q '"ha:smoke"' || fail "audit entry without user ha:smoke"
pass "API write: 403 without the token, enroll with it, audit user ha:smoke"

st=$(c -o /dev/null -w '%{http_code}' -X POST -d '{}' "http://$app:8099/api/login")
[ "$st" = 404 ] || fail "login under ingress: $st, want 404"
pass "/api/login is 404 under ingress"

if out=$(docker exec $p-foreign curl -sS --max-time 5 "http://$app:8099/" 2>&1); then
	fail "foreign container got an answer: $out"
fi
pass "foreign container refused ($(echo "$out" | tail -n 1))"

c "http://$app:9100/metrics" | grep -q '^xs_' || fail "/metrics"
pass "/metrics on the container address"
echo "ha-smoke OK"
