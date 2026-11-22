#!/bin/sh
# only.sh <path|all>: let exactly one path carry data (relay name or local link
# id), or all of them again. Waits XS_SETTLE_S (4) seconds afterwards.
set -eu
target=${1:?usage: only.sh <relay name|uplink id|all>}
st=$(xs-hubctl status)
relays=$(printf '%s' "$st" | jq -r '.relays[]? | select(.connected) | .name')
links=$(printf '%s' "$st" | jq -r '.uplinks[]? | select(.enabled) | .id')
found=0
[ "$target" = all ] && found=1
set_relay() { xs-hubctl path "$1" -up "$2" -down "$2" >/dev/null; }
set_link() { xs-hubctl uplinks set "$1" "up=$2" "down=$2" >/dev/null; }
# Turn the chosen path on first, so the tunnel never has no path at all.
# Loop bodies use if, not &&: a false last test would end the loop with 1
# and set -e would stop the script (matrix.sh then measured nothing).
printf '%s\n' "$relays" | while IFS= read -r r; do
	if [ -n "$r" ] && { [ "$target" = all ] || [ "$target" = "$r" ]; }; then set_relay "$r" on; fi
done
printf '%s\n' "$links" | while IFS= read -r u; do
	if [ -n "$u" ] && { [ "$target" = all ] || [ "$target" = "$u" ]; }; then set_link "$u" on; fi
done
if [ "$target" != all ]; then
	printf '%s\n%s\n' "$relays" "$links" | grep -qxF -- "$target" && found=1
	[ "$found" = 1 ] || { echo "only.sh: no connected relay or enabled local link '$target'" >&2; exit 2; }
	printf '%s\n' "$relays" | while IFS= read -r r; do
		if [ -n "$r" ] && [ "$r" != "$target" ]; then set_relay "$r" off; fi
	done
	printf '%s\n' "$links" | while IFS= read -r u; do
		if [ -n "$u" ] && [ "$u" != "$target" ]; then set_link "$u" off; fi
	done
fi
sleep "${XS_SETTLE_S:-4}"
