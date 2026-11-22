#!/bin/sh
# matrix.sh [MB] [PEP MB]: every connected path alone, then all paths twice,
# first with PEP off, then on; puts PEP and the path directions back at the end.
# Data: (paths + 2) x (MB + PEP MB); default 15 / 30 MB per run.
set -eu
here=$(dirname "$0")
mb=${1:-15}
pepmb=${2:-$((mb * 2))}
pep0=$(xs-hubctl settings | jq -r .pep)
restore() {
	sh "$here/only.sh" all >/dev/null 2>&1 || true
	xs-hubctl settings -pep "$pep0" >/dev/null 2>&1 || true
}
trap restore EXIT INT TERM
st=$(xs-hubctl status)
paths=$(printf '%s' "$st" | jq -r '(.relays[]? | select(.connected) | .name), (.uplinks[]? | select(.enabled) | .id)')
n=$(printf '%s\n' "$paths" | grep -c . || true)
[ "$n" -ge 1 ] || { echo "matrix.sh: no connected path" >&2; exit 2; }
echo "# $(date '+%F %T') build $(xs-hubctl version 2>/dev/null | head -n1) paths: $(printf '%s' "$paths" | tr '\n' ' ')"
echo "# expected data: $(((n + 2) * (mb + pepmb))) MB"
out=$(mktemp)
for pep in off on; do
	size=$mb
	[ "$pep" = on ] && size=$pepmb
	xs-hubctl settings -pep "$pep" >/dev/null
	printf '%s\n' "$paths" | while IFS= read -r p; do
		[ -n "$p" ] || continue
		sh "$here/only.sh" "$p"
		sh "$here/dl.sh" "pep=$pep $p" "$size" | tee -a "$out"
	done
	sh "$here/only.sh" all
	for i in 1 2; do sh "$here/dl.sh" "pep=$pep bonded #$i" "$size" | tee -a "$out"; done
done
LC_ALL=C awk '{ for (i = 1; i < NF; i++) if ($(i + 1) == "MB") s += $i } END { printf "# total %.0f MB\n", s }' "$out"
rm -f "$out"
