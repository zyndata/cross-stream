#!/bin/sh
# dl.sh <label> [MB]: one bounded download through the tunnel from the Linux hub
# itself (phase 18 field test, docs/field-test.md). A range request is the size
# limit (metered SIMs: never an unbounded generator); --max-time stops a stuck run.
# Prints: <label> <MB> MB <Mb/s> Mb/s <seconds> s
# Env: XS_TUN (xs0), XS_DL_MAX_S (120), XS_DL_URL (override the file).
set -eu
label=${1:?usage: dl.sh <label> [MB]}
mb=${2:-15}
case $mb in '' | *[!0-9]*) echo "dl.sh: MB must be a whole number" >&2; exit 2 ;; esac
if [ "$mb" -le 100 ]; then file=100MB.bin; elif [ "$mb" -le 1000 ]; then file=1GB.bin; else file=10GB.bin; fi
url=${XS_DL_URL:-https://fsn1-speed.hetzner.com/$file}
res=$(curl -s -o /dev/null --interface "${XS_TUN:-xs0}" -r "0-$((mb * 1000000 - 1))" \
	--max-time "${XS_DL_MAX_S:-120}" -w '%{size_download} %{speed_download} %{time_total}' "$url") || true
# shellcheck disable=SC2086 # three numbers
set -- $res
LC_ALL=C awk -v l="$label" -v b="${1:-0}" -v s="${2:-0}" -v t="${3:-0}" \
	'BEGIN { printf "%-28s %8.1f MB %8.2f Mb/s %7.1f s\n", l, b / 1e6, s * 8 / 1e6, t }'
