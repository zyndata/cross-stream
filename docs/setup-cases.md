# Setup cases and pitfalls

Practical knowledge for real deployments, collected from public documentation of bonding
products and our own tests (2026-11-10 onwards).
Each item says which phase uses it. Keep entries short; add our own field findings here.

## 1. Hub hotspot on Linux (phase 05)
- AP mode needs NetworkManager (Ubuntu Server lacks it: `apt install network-manager`).
  A hostapd + dnsmasq fallback without NM is possible; we require NM (decision: simpler).
- `rfkill list` — a soft-blocked Wi-Fi card silently prevents the AP. Detect and report
  (`rfkill unblock wifi` only after ASK).
- Regulatory domain: set the country (`iw reg set PL`, NM `802-11-wireless` + `wifi.country`
  equivalent). Without it 5 GHz channels are often `no-IR` and the AP will not start →
  fall back to 2.4 GHz (channel 1/6/11) and say so in the GUI.
- A card that is connected as a Wi-Fi client usually cannot also run an AP. Check
  `iw list` → "valid interface combinations" (`AP` + `managed` on the same radio).
  If not supported: disconnect the client connection (ASK) or use a second USB Wi-Fi card.
  Phase 18: xs-hubd asks instead of exiting (GUI dialog, `xs-hubctl hotspot -client-wifi`):
  `share` = AP on a virtual `xsap0` next to the client, on the client's channel (one radio;
  offered only when a combination also allows two managed interfaces — not on Intel AX201);
  `disconnect` = `nmcli device disconnect`. The choice is saved in `hotspot.json`.
  Windows: Mobile Hotspot runs next to a Wi-Fi client by itself when the driver allows it.
- Channel 0 = automatic (phase 14): before the AP starts (start and settings change only,
  never while it runs) xs-hubd stops its AP, runs `iw dev <iface> scan` and takes the usable
  channel with the least load (neighbours weighted by signal, overlapping 2.4 GHz channels
  counted); order: configured channel → 5 GHz → 2.4 GHz. Status, log ("hotspot up … why=…
  scan=…") and GUI show the channel and why. Paired phones keep working (pairing has SSID +
  BSSID, no channel).
- Saved Wi-Fi networks can grab the radio and hide the hotspot → NM connection
  `crossstream-ap` gets `autoconnect-priority` higher than any client profile.
- Default-route repair: NM or DHCP renewals can rewrite routes; xs-hubd re-checks its
  rules/routes on netlink events and every 30 s.
- Ethernet sharing: a client laptop can also plug into the hub's Ethernet port
  (NM `ipv4.method shared` on that port, subnet `10.42.1.0/24`) — useful when Wi-Fi is
  crowded. Same policy routing as the hotspot.

## 2. Local uplinks on the hub (phase 12)
| Uplink | Linux interface | Notes |
|---|---|---|
| Android USB tethering | `usb0` / `enx*` (RNDIS/NCM) | Works out of the box (Android 9+). **Phone Wi-Fi must be OFF**: a phone on Wi-Fi tethers its Wi-Fi, not cellular. If that Wi-Fi is our own hotspot → loop (see below). |
| iPhone USB tethering | `eth*` / `apple0` (`ipheth`) | Needs `usbmuxd` (+ `ipheth-utils` on Ubuntu) and "Trust" on the phone. Unreliable on Linux since iOS 14/15 → best effort only. |
| Bluetooth PAN tethering | `bnep0` | 1–2 Mb/s (BT 4/5). Last-resort backup path only. |
| USB 4G/5G modem | `wwan*` / `usb*` / `enx*` | ModemManager/NM brings it up; some need vendor drivers. |
| Built-in laptop SIM (WWAN card) | `wwan0` / `wwp*` | Same as a USB modem: ModemManager + NM bring it up (APN, PIN set in the OS). Often FCC-locked on Lenovo/Dell until the vendor unlock tool runs. |
| Second Wi-Fi card as client | `wlan1` | Campsite/hotel Wi-Fi, or a friend's phone hotspot (iPhone works well this way). |
| Ethernet / Starlink | `eth*` / `enp*` | Starlink router in bypass mode = plain Ethernet uplink. |
- Each uplink must be a separate network with its own default route and a unique
  route metric (OpenWrt users must un-bridge WANs for the same reason).
- Two uplinks behind the same router/public IP give no gain ("same network twice").
  The server reports the observed public IP per path (PATH_INFO); the GUI warns.
- Loop guard: hotspot clients never need to reach the server endpoints directly →
  nft drops `iifname <hotspot> ip daddr <server IPs>`; a USB-tethered phone that is also
  on our hotspot then fails cleanly instead of looping through the tunnel.
- Carriers can detect USB/Wi-Fi tethering by TTL; optional `ip ttl set 65` per uplink.

## 3. Friend's phone (phases 07–08, 11)
- One phone = one cellular path. Dual-SIM phones use only one data SIM at a time
  (shared radio); a second SIM needs a second phone.
- Our relay app traffic leaves the phone from the app's own sockets (TTL 64), so Android
  does not count it as hotspot data. The carrier's terms may still treat relaying as
  tethering — friends should know this. (Commercial bonding products state that data shared
  from phones counts as tethering data on the plan.)
- Android 13+: request `POST_NOTIFICATIONS` at onboarding, or the foreground-service
  notification (with the Stop button) is hidden.
- Keep relaying when the app is swiped away from Recents (`stopWithTask="false"`; test on
  Samsung/Xiaomi).
- Screen-overlay apps (night mode, blue-light filters, e.g. Twilight, Lux) can block taps
  on system dialogs — also the Wi-Fi approval dialog of `WifiNetworkSpecifier`.
  Troubleshooting text in the app: "close screen filter apps and retry".
- Data limit defaults that work for friends (common defaults): monthly 2 GB, daily =
  10 % of monthly; over-limit throttle keeps the link useful for failover.
- Plan guard (phase 11): limits count only relayed data; the phone's own usage also eats
  the plan. With the usage-access permission the app reads total cellular usage for the
  billing cycle (`NetworkStatsManager`) and stops relaying when the plan's remaining data
  drops below a reserve the friend sets.

## 4. Server in production (phases 02, 14)
Adapted from common VPN server hardening practice.
- Implemented in phase 14: egress filter (nft `forward` chain in the `crossstream` table +
  the PEP dial check; exceptions `XS_SERVER_EGRESS_ALLOW`), sysctl check at start
  (`deploy/server/90-crossstream.conf` for the host), compose hardening, per-IP handshake
  rate limit and stream-connection cap, INIT replay check, `min_hub_version`, optional
  `/metrics` (`XS_SERVER_METRICS_LISTEN`, loopback/private only). Firewall, SSH and DNSBL
  stay host tasks (phase 17).
- Firewall: default-deny inbound; allow only our UDP/TCP ports and SSH from trusted IPs.
- Egress filter for tunnel traffic (nft, `iifname xss0`): drop TCP 25 (spam), TCP/UDP 445
  (SMB), UDP 67/68, `169.254.169.254` (cloud metadata), RFC 1918 ranges, Docker API
  `172.17.0.0/16:2375`. Allow-list exceptions per hub in config if ever needed.
- sysctl: `net.ipv4.conf.{all,default}.rp_filter=2`, no ICMP redirects
  (`accept_redirects=0`, `secure_redirects=0`), `net.core.default_qdisc=fq`,
  `net.ipv4.tcp_congestion_control=bbr` (PEP/TCP transport sockets), `fs.suid_dumpable=0`.
- Docker: log size limit (`max-size: 50m`), `ulimits: core 0` (no key material in core
  dumps), read-only root FS where possible, never mount the Docker socket.
- SSH: keys only, no root login, `AllowUsers`.
- Keep logs bounded; no per-flow logs (privacy). Optional Prometheus `/metrics` on the
  Unix socket or Docker network only — never public.
- Check the server IP against blocklists (DNSBL) after deploy; a listed IP breaks sites
  for every hub.
- Port forwarding: allow only ports 1024–49151, max 20 per hub, unique per server.
- Version gate: hubs send their software version in INIT; server config
  `min_hub_version` rejects too-old hubs with a clear log line.

## 5. Windows hub (phase 16)
- Windows 10 1809+ with a Wi-Fi adapter that supports Mobile Hotspot (Settings → Network →
  Mobile hotspot must exist). Install from an elevated PowerShell in `bin/windows` (built by
  `make hub-windows`): `powershell -ExecutionPolicy Bypass -File install.ps1`; sign out and in once
  (group `crossstream`), then start the hub from the GUI or with `Start-Service CrossStreamHub`.
- Unlike tools that need a routed-sharing add-on (the Mobile Hotspot
  screen does not list VPN adapters), the hub starts Mobile Hotspot through its API on the Wintun
  adapter's own connection profile (Windows 11; spike results in `docs/decisions.md`). Before the
  hub is enrolled the hotspot shares any connection; after enrollment it restarts once on the
  tunnel. Do not change "Share my Internet connection from" in Settings while the hub runs.
- The hotspot subnet is Windows' 192.168.137.0/24 (hub 192.168.137.1); pairing codes carry it.
- Port forwarding is not available on a Windows hub.
- While the tunnel is up all of the laptop's own traffic goes through it too (no source routing on
  Windows). Closing the lid follows the power plan: set "When I close the lid" to "Do nothing" on AC.
- Windows names adapters "Ethernet 3" etc.; the GUI shows the adapter alias, i.e. what the user
  renamed it to (Settings → Advanced network settings → Rename).
- Android USB tethering appears as a wired adapter ("Remote NDIS"); Bluetooth PAN via "Bluetooth
  Network Connection → Connect using → Access point". Both show under "Łącza lokalne".
- Fallback if Mobile Hotspot refuses on some adapter: a travel router in AP mode on Ethernet with the
  hub headless on that port (as on Linux, `-headless -lan-iface`).

## 5b. Hub laptop's own traffic (phase 21)

- **Linux:** *Ustawienia → Ruch tego laptopa przez CrossStream* / `xs-hubctl settings
  -own-traffic on`. Check from the laptop: `curl https://ifconfig.me` shows the server's address
  in BONDED, a relay's carrier address in DIRECT; `ip rule` lists 101/102; `resolvectl dns xs0`
  shows the tunnel address. Open connections restart once when it switches.
- With no carrying path (OFFLINE/CONNECTING) the rules go away and the laptop uses its own
  links; with no own link it is offline, as without the switch.
- No `systemd-resolved` (plain `/etc/resolv.conf`): DNS stays as configured; a server on the
  home LAN (router) is still asked outside the tunnel. The GUI says so.
- Docker on the laptop: containers follow the laptop (forwarded `docker0` traffic uses table
  100; Docker's own masquerade covers `xs0`).
- NetworkManager's connectivity check binds to each device: rule 99 (`oif <link> lookup main`,
  one per link with a default route) keeps it working, so NM does not mark the Ethernet
  "limited" and push its route behind a metered modem (field test 2026-11-25).
- `xs-hubctl enroll -via` / `probe -via` without root: the socket gets no mark, so with the
  switch on it goes into the tunnel; turn the switch off for such tests.
- Captive portal while on: the browser's portal page lookup goes to the tunnel resolver;
  log in through the local link with *Log in* (host route), or turn the switch off for the
  login.
- **Windows:** always on. `netsh wfp show filters` lists the CrossStream sublayer's filters
  while a path carries traffic; after a hub stop or `taskkill /f` of xs-hubd none is left
  (dynamic WFP session). DirectAccess / corporate VPNs with their own DNS policy rules (NRPT)
  are not supported on a hub laptop.

## 6. Hub hardware options
- OpenWrt routers (ARM/x86, no MIPS) and Raspberry Pi 4/5 can host a hub — a small
  dedicated box instead of a laptop. Notes if we ever do it: un-bridge WANs, unique
  gateway metrics, disable vendor flow offload/"network acceleration" (breaks PEP on
  GL.iNet), OS upgrades wipe installed packages, write to `/tmp` to spare SD cards.
- Raspberry Pi 4 can act as a USB Ethernet gadget (`dtoverlay=dwc2`, `g_ether`): plug the
  Pi into a laptop and the laptop gets the bonded connection without Wi-Fi.

## 7. Home server behind a NAT router (example: MikroTik)
A server at home works like a VPS once the router forwards the tunnel ports to it (Docker
Desktop on a PC, a Linux VM, or the Home Assistant app); only the forwarding target changes
between them.
- First rule out CGNAT: the WAN address in `/ip address print` must equal the address
  seen from outside (e.g. ifconfig.me). 100.64.0.0/10 or a private address = CGNAT →
  port forwarding cannot work, use a VPS instead.
- Cloudflare Tunnel does not fit: public hostnames carry HTTP(S) only, raw UDP needs
  Spectrum (paid) or WARP on every phone, and a proxy hides per-path carrier addresses.
  It may later front the WSS fallback (`wss://host:443/xs`, phase 10).
- Dynamic public IP: `/ip cloud set ddns-enabled=yes` → `xxxx.sn.mynetname.net`
  (`/ip cloud print`); optionally a CNAME from an own domain in Cloudflare, DNS only
  (grey cloud). Hub and relay resolve the name when they connect; an IP change drops the
  session until reconnect.
- MikroTik: static DHCP lease for the PC, then
  `/ip firewall nat add chain=dstnat in-interface-list=WAN protocol=udp dst-port=51900,443 action=dst-nat to-addresses=<PC LAN IP>`
  (the default config accepts dst-nat'ed traffic, no extra filter rule).
- Windows: `deploy/server/.env` `XS_SERVER_PUBLIC_ENDPOINTS=<DDNS name>`,
  `docker compose up -d --build`, firewall rule from `deploy/server/README.md`,
  `xs-ctl enroll`. Disable sleep on the PC during tests.
- Pitfalls: test through the phones' cellular data, not home Wi-Fi (that needs hairpin
  NAT); Docker Desktop's UDP proxy shows the Docker gateway as every path's source
  (PATH_INFO useless, check idle drops in multipath tests); downloads through the tunnel
  leave over the home upload link; until the phase 14 egress filter, an enrolled hub can
  reach the home LAN.
