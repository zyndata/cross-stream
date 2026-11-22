#!/bin/sh
# srvdl.sh [MB]: download from the server itself through the tunnel — takes the
# server's internet egress out of the measurement. Needs, inside the server
# container (HA: docker exec from the Advanced SSH app, protection mode off) and
# XS_SERVER_EGRESS_ALLOW=100.64.0.1/32 on the server for the test:
#   n=$((MB*1000000)); (printf 'HTTP/1.0 200 OK\r\nContent-Length: %d\r\n\r\n' $n; head -c $n /dev/zero) | nc -l -p 5001
# Then run this on the hub. Remove the egress allowance afterwards.
set -eu
mb=${1:-15}
XS_DL_URL=${XS_SRV_URL:-http://100.64.0.1:5001/} exec sh "$(dirname "$0")/dl.sh" "server $mb MB" "$mb"
