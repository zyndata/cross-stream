#!/bin/sh
# Entry point of the CrossStream Server app: maps /data/options.json (the
# app's options in the HA UI) to XS_SERVER_* and starts xs-server (env only,
# no server.yaml). POSIX sh + jq, no bashio.
set -eu

opts=/data/options.json
[ -r "$opts" ] || { echo "crossstream: $opts not found (run by the HA Supervisor?)" >&2; exit 1; }
jq -e 'type == "object"' "$opts" >/dev/null 2>&1 || { echo "crossstream: $opts is not a JSON object" >&2; exit 1; }
opt() { jq -r "$1" "$opts"; }

XS_SERVER_PUBLIC_ENDPOINTS=$(opt '(.public_endpoints // []) | join(",")')
XS_SERVER_PEP=$(opt 'if .pep == null then true else .pep end')
XS_SERVER_LOG_LEVEL=$(opt '.log_level // "info"')
XS_SERVER_MIN_HUB_VERSION=$(opt '.min_hub_version // ""')
XS_SERVER_EGRESS_ALLOW=$(opt '(.egress_allow // []) | join(",")')
export XS_SERVER_PUBLIC_ENDPOINTS XS_SERVER_PEP XS_SERVER_LOG_LEVEL XS_SERVER_MIN_HUB_VERSION XS_SERVER_EGRESS_ALLOW
# The port hubs dial is the host port mapped to 51900 in the Network section (the
# only place it is kept): xs-server reads it from the Supervisor at start, and the
# panel changes it there. Inside, the app always listens on 51900.
export XS_SERVER_PORT_SOURCE=supervisor
# 443 (UDP + WSS) only when the owner mapped it in the Network section: enrollment
# codes list every listening port, and an unmapped one is a dead fallback for hubs.
if [ "$(opt '.port_443 // false')" != true ]; then
	export XS_SERVER_LISTEN_PORTS=51900 XS_SERVER_WSS_PORTS=none
fi

export XS_SERVER_DATA_DIR=/data
export XS_SERVER_WEB_LISTEN=off # the panel is reached through HA ingress only
export XS_SERVER_WEB_INGRESS_LISTEN=0.0.0.0:8099
# /metrics must bind a private address (not 0.0.0.0): the container's own.
addr=$(hostname -i 2>/dev/null | awk '{print $1}')
case "$addr" in
10.* | 172.1[6-9].* | 172.2[0-9].* | 172.3[01].* | 192.168.*) export XS_SERVER_METRICS_LISTEN="$addr:9100" ;;
*) echo "crossstream: no private container address ('$addr'): /metrics off" >&2 ;;
esac

if [ "$(cat /proc/sys/net/ipv4/ip_forward 2>/dev/null || echo 0)" != 1 ]; then
	echo "crossstream: net.ipv4.ip_forward is 0 in the app container, hub traffic cannot be" >&2
	echo "crossstream: forwarded. Enable IP forwarding on the host (Docker does it by default)." >&2
	exit 1
fi

exec xs-server -config ""
