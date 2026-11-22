#!/bin/sh
# Phase 02 acceptance in Docker: enroll, connect, ping and curl through the
# tunnel, code reuse fails, revoke drops the session. Needs Docker with compose v2.
set -eu
cd "$(dirname "$0")"
export MSYS_NO_PATHCONV=1 # Git Bash on Windows: keep /tmp/... container paths as they are

dc() { docker compose "$@"; }
pass() { echo "PASS  $*"; }
fail() {
	echo "FAIL  $*"
	dc logs --no-color --tail 40 server >&2 || true
	dc exec -T hub cat /tmp/connect.log >&2 2>/dev/null || true
	exit 1
}
cleanup() { [ "${KEEP:-0}" = 1 ] || dc down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

dc up -d --build >/dev/null 2>&1 || { dc up -d --build; exit 1; }
for _ in $(seq 30); do
	dc exec -T server xs-ctl list >/dev/null 2>&1 && break
	sleep 1
done
dc exec -T server xs-ctl list >/dev/null 2>&1 || fail "xs-server control socket"

code=$(dc exec -T server xs-ctl enroll t1 2>/dev/null | tr -d '\r')
case "$code" in XS1.*) pass "xs-ctl enroll t1" ;; *) fail "xs-ctl enroll t1: $code" ;; esac

origin=$(dc exec -T origin hostname -i | tr -d '\r' | awk '{print $1}')
dc exec -T hub xs-hubctl -dir /tmp/hub enroll "$code" >/dev/null 2>&1 || fail "xs-hubctl enroll"
pass "xs-hubctl enroll <code>"

dc exec -d hub sh -c 'xs-hubctl -dir /tmp/hub connect -via eth0 >/tmp/connect.log 2>&1'
for _ in $(seq 20); do
	dc exec -T hub ip link show xs0 >/dev/null 2>&1 && break
	sleep 0.5
done
dc exec -T hub ping -c 3 -W 2 -I xs0 "$origin" >/dev/null 2>&1 || fail "ping -I xs0 $origin"
pass "xs-hubctl connect + ping -I xs0 origin ($origin)"

dc exec -T hub curl -sS --max-time 20 --interface xs0 -o /tmp/file.bin "http://$origin/file.bin" || fail "curl via xs0"
want=$(dc exec -T origin sha256sum /www/file.bin | awk '{print $1}')
got=$(dc exec -T hub sha256sum /tmp/file.bin | awk '{print $1}')
[ -n "$want" ] && [ "$want" = "$got" ] || fail "downloaded file differs"
pass "curl downloads 1 MiB from origin through the tunnel (sha256 match)"

dc exec -T server xs-ctl sessions | grep -q '^t1 ' || fail "xs-ctl sessions lists t1"
pass "xs-ctl sessions lists t1"

if dc exec -T hub xs-hubctl -dir /tmp/hub2 enroll "$code" >/dev/null 2>&1; then
	fail "reusing the code succeeded"
fi
pass "reusing the same code fails"

dc exec -T server xs-ctl revoke t1 >/dev/null || fail "xs-ctl revoke t1"
sleep 1
dc exec -T server xs-ctl sessions | grep -q '^t1 ' && fail "session still listed 1 s after revoke"
dc exec -T hub grep -q "revoked" /tmp/connect.log || fail "hub not told about the revoke within 1 s"
if dc exec -T hub ping -c 1 -W 1 -I xs0 "$origin" >/dev/null 2>&1; then
	fail "ping still works after revoke"
fi
pass "xs-ctl revoke t1 drops the session within 1 s"
echo "smoke OK"
