# Hub on a Linux laptop (xs-hubd)

`xs-hubd` runs as root under systemd. It turns the laptop's Wi-Fi card into the
CrossStream hotspot (NetworkManager profile `crossstream-ap`), serves DNS on
`10.42.0.1:53`, routes hotspot clients into the bonded tunnel and is controlled with
`xs-hubctl` (or the GUI, phase 09) over `/run/crossstream/xs-hubd.sock`.

Requirements: NetworkManager (`apt install network-manager` on server installs), `iw`,
`nft`, `ip`, systemd (logind for the sleep inhibitor), a Wi-Fi card with AP mode.

## Install

```sh
make build
sudo sh deploy/hub/install.sh          # binaries, group crossstream, unit, /etc/default/xs-hubd,
                                       # polkit rule; asks about iPhone USB tethering packages
sudo systemctl start xs-hubd           # the Wi-Fi card becomes the hotspot
xs-hubctl hotspot                      # SSID, password, band/channel in use, warnings
xs-hubctl pair-qr                      # scan with the relay app / xs-relaysim
```

Flags (Wi-Fi/Ethernet interface, DNS upstreams) go into `/etc/default/xs-hubd`.
Hotspot name, password, band and country: `xs-hubctl hotspot -ssid … -psk … -band a|bg -country PL`;
when the Wi-Fi card is connected to a network: `-client-wifi share|disconnect|ask`
(restarts the AP; relays need a new pairing code after an SSID or password change).
Settings live in `/var/lib/crossstream/hotspot.json`.

Uninstall: `sudo sh deploy/hub/install.sh --uninstall` (keeps `/var/lib/crossstream`).

From a GitHub Release (no build): `tar -xzf crossstream-hub-vX.Y.Z-linux-amd64.tar.gz`,
`cd crossstream-hub-vX.Y.Z-linux-amd64`, `sudo sh install.sh` (binaries from `bin/`), and
for the desktop app `sudo sh gui/install.sh bin/xs-hub-gui`. The `linux-arm64` archive has
no GUI. Check the archive against its `.sha256` file first (`sha256sum -c`).

### Local links (phase 12)

Any other interface with internet is a bond path too: a phone on USB tethering (no app; iPhone
best effort, needs `usbmuxd`), Bluetooth PAN, Ethernet, a USB modem or the laptop's own SIM
(ModemManager/NetworkManager bring it up; APN and PIN are set in the OS). A new link shows up
off: `xs-hubctl uplinks` lists them, `xs-hubctl uplinks set u1 enabled=true` turns one on
(also `name=`, `priority=`, `up=`/`down=`, `rate_kbps=`, `cap_mb=` + `period=session|day|month`,
`reset_day=`, `over_cap=stop|throttle`, `ttl=`, `reset=1`). Saved in
`/var/lib/crossstream/uplinks.json`. An Android phone must have Wi-Fi off, or it tethers its Wi-Fi.

### Desktop app (optional)

```sh
make gui                                # needs GTK/WebKitGTK dev packages, docs/DEVELOPMENT.md
sudo sh gui/packaging/install.sh bin/xs-hub-gui   # /usr/local/bin + menu entry "CrossStream Hub"
sudo usermod -aG crossstream $USER      # once; log out and in again
```

The app starts `xs-hubd` itself when it is not running (polkit rule from `install.sh`, no password) and
on close asks whether to leave the hub running or stop it (the choice can be remembered; Settings).
The app runs as the desktop user and only talks to `xs-hubd` over its socket: pairing QR, relays
(priority, directions, rate limit, data cap, forget), overflow threshold, hotspot settings and
start/stop, server enrollment. The hub-wide overflow threshold set there is saved in
`/var/lib/crossstream/settings.json` and wins over `-overflow-mbps`.

## What xs-hubd changes, and removes on stop

| While running | Removed by `systemctl stop xs-hubd` |
|---|---|
| NM keyfiles `/etc/NetworkManager/system-connections/crossstream-ap.nmconnection` (and `crossstream-eth` with `-eth-iface`) | `nmcli connection delete` + files |
| `/etc/NetworkManager/dnsmasq-shared.d/crossstream.conf` (DHCP only, DNS → hub address) | file |
| `ip rule … from 10.42.0.0/24 iif <wifi> lookup 100` (+ 10.42.1.0/24 for Ethernet) | rules |
| table 100: port subnets, `unreachable default`, `default dev xs0` | `ip route flush table 100` |
| nft table `ip crossstream_hub` (masquerade, MSS clamp, loop guard) | table |
| logind inhibitor lock (sleep, lid switch) while clients or relays are present | lock |

`net.ipv4.ip_forward` stays on (NetworkManager shared mode needs it anyway).

## Preflight errors

| Message | Fix |
|---|---|
| soft-blocked (rfkill) | `rfkill unblock wifi` |
| connected to Wi-Fi … as a client | xs-hubd waits for a choice: the GUI asks, or `xs-hubctl hotspot -client-wifi share` (AP on `xsap0` next to the client, on its channel; cards allowing AP + two clients, not Intel AX201) / `-client-wifi disconnect`. Saved; `-client-wifi ask` asks again. Or a second (USB) Wi-Fi card |
| no AP mode | another Wi-Fi card |
| no 5 GHz channel allows an access point (warning) | the hotspot runs on 2.4 GHz; set the right country (`-country`) |

## Acceptance check (phase 05, on the laptop)

```sh
sudo nft list ruleset > /tmp/nft.before; ip rule > /tmp/rule.before; ip route show table all > /tmp/route.before
sudo systemctl start xs-hubd && journalctl -u xs-hubd -f      # "hotspot up", "dns listening"
# phone/laptop joins the hotspot: gets 10.42.0.x, DNS 10.42.0.1; with >=1 relay paired and
# the hub enrolled (xs-hubctl enroll <XS1 code>), it browses the web
xs-hubctl status | jq '{mode, clients, sleep_inhibited}'
sudo systemctl stop xs-hubd
sudo nft list ruleset | diff /tmp/nft.before -; ip rule | diff /tmp/rule.before -
ip route show table all | diff /tmp/route.before -    # only lines of the Wi-Fi client connection NM restores
```
