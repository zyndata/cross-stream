#!/bin/sh
# Installs (or with --uninstall removes) the CrossStream hub on a Linux laptop:
# binaries in /usr/local/bin, group crossstream, /var/lib/crossstream, the
# systemd unit, /etc/default/xs-hubd and a polkit rule that lets group
# crossstream start/stop/restart xs-hubd.service (the GUI does that). It does
# not start the service.
# Optional: iPhone USB tethering packages (usbmuxd, libimobiledevice-utils),
# asked interactively; XS_IPHONE=yes|no answers without asking.
# Usage: sudo sh deploy/hub/install.sh [--uninstall] [bin dir, default ./bin]
set -eu

here=$(cd "$(dirname "$0")" && pwd)
rules=/etc/polkit-1/rules.d/50-crossstream.rules
if [ "$(id -u)" -ne 0 ]; then
	echo "run as root: sudo sh $0 $*" >&2
	exit 1
fi

if [ "${1:-}" = "--uninstall" ]; then
	systemctl disable --now xs-hubd.service 2>/dev/null || true
	# Leftovers of a crashed xs-hubd (a clean stop removes them itself): the
	# own-traffic rules would keep sending the laptop's traffic to table 100.
	ip rule del lookup main suppress_prefixlength 0 priority 101 2>/dev/null || true
	ip rule del not fwmark 0x5853 lookup 100 priority 102 2>/dev/null || true
	for dev in $(ip rule show priority 99 2>/dev/null | sed -n 's/.* oif \([^ ]*\) .*lookup main.*/\1/p'); do
		ip rule del oif "$dev" lookup main priority 99 2>/dev/null || true
	done
	nft delete table ip6 crossstream_hub 2>/dev/null || true
	nft delete table ip crossstream_hub 2>/dev/null || true
	rm -f /etc/systemd/system/xs-hubd.service /usr/local/bin/xs-hubd /usr/local/bin/xs-hubctl "$rules"
	systemctl daemon-reload
	echo "removed xs-hubd (kept /var/lib/crossstream, /etc/default/xs-hubd and group crossstream)"
	exit 0
fi

bin=${1:-./bin}
# unpacked release tarball: bin/ next to this script
[ -n "${1:-}" ] || [ -x "$bin/xs-hubd" ] || bin=$here/bin
for f in xs-hubd xs-hubctl; do
	[ -x "$bin/$f" ] || { echo "$bin/$f not found: run make build first" >&2; exit 1; }
done
for tool in nmcli iw nft ip systemd-inhibit; do
	command -v "$tool" >/dev/null 2>&1 || echo "warning: $tool not found (needed by xs-hubd; NetworkManager: apt install network-manager)" >&2
done
command -v mmcli >/dev/null 2>&1 || echo "note: mmcli not found: modem signal is not shown for USB modems / laptop SIM (apt install modemmanager)" >&2

getent group crossstream >/dev/null || groupadd --system crossstream
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ]; then
	usermod -aG crossstream "$SUDO_USER"
	echo "added $SUDO_USER to group crossstream (log in again to use xs-hubctl and the GUI without sudo)"
fi
install -m 0755 "$bin/xs-hubd" "$bin/xs-hubctl" /usr/local/bin/
install -d -m 0700 /var/lib/crossstream
install -m 0644 "$here/xs-hubd.service" /etc/systemd/system/xs-hubd.service
[ -e /etc/default/xs-hubd ] || install -m 0644 "$here/xs-hubd.default" /etc/default/xs-hubd
if [ -d /etc/polkit-1/rules.d ]; then
	install -m 0644 "$here/50-crossstream.rules" "$rules"
	echo "polkit: group crossstream may start/stop/restart xs-hubd.service without a password"
else
	echo "warning: /etc/polkit-1/rules.d missing: the GUI cannot start or stop xs-hubd (apt install polkitd)" >&2
fi
systemctl daemon-reload

# iPhone USB tethering (best effort on Linux): usbmuxd pairs the phone, the
# kernel module ipheth makes the network interface.
iphone=${XS_IPHONE:-}
if [ -z "$iphone" ] && [ -t 0 ] && command -v apt-get >/dev/null 2>&1; then
	printf 'Install iPhone USB tethering support (usbmuxd, libimobiledevice-utils)? [y/N] '
	read -r ans || ans=
	case "$ans" in [yY]*) iphone=yes ;; *) iphone=no ;; esac
fi
if [ "$iphone" = yes ]; then
	apt-get install -y usbmuxd libimobiledevice-utils
fi
if ! modinfo ipheth >/dev/null 2>&1; then
	echo "note: kernel module ipheth not found: iPhone USB tethering will not work on this kernel" >&2
fi

echo "installed. Start: sudo systemctl start xs-hubd (at boot: sudo systemctl enable xs-hubd)"
echo "The Wi-Fi card becomes the hotspot. If it is connected to a Wi-Fi network, xs-hubd asks (GUI or xs-hubctl hotspot -client-wifi share|disconnect)."
echo "Local links (USB tethering, Ethernet, modem) show in the GUI, off until you enable them."
