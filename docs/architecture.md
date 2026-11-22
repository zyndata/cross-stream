# Architecture

## Topology

```
 Client laptop (nothing installed)
        │ Wi-Fi (WPA2, 5 GHz preferred)
        ▼
 ┌──────────────────── Hub laptop (Linux) ─────────────────────┐
 │ hotspot wlan0 (AP, 10.42.0.1/24)                            │
 │ client subnet ──policy route──► TUN xs0 ──► xs-hubd            │
 │ xs-hubd: bonding scheduler / fallback netstack / DNS / IPC     │
 └───────────────┬───────────────┬───────────────┬─────────────┘
       same Wi-Fi│ (UDP data + TCP control)      │
        ┌────────▼───┐   ┌───────▼────┐   ┌──────▼─────┐
        │ Relay A    │   │ Relay B    │   │ Relay C    │  Android phones
        │ cellular   │   │ cellular   │   │ cellular   │  (friends)
        └────────┬───┘   └───────┬────┘   └──────┬─────┘
                 └──────── internet (3 carrier NAT IPs) ┘
                                 ▼
                ┌──────── Server (Docker) ───────┐
                │ xs-server: sessions, reorder,    │
                │ TUN xss0 → NAT → internet      │
                └────────────────────────────────┘
```

The hub laptop has no internet of its own; its only uplinks are the relays.
Relays join the hub's hotspot as Wi-Fi clients. The phone's Wi-Fi is a
local-only network (no internet), so the phone's default network stays cellular.

## Components

### xs-server (server, `cmd/xs-server`)
- Runs in Docker (Linux host or Docker Desktop) or as a Home Assistant app (`ha/crossstream_server`,
  bridge network, options → `XS_SERVER_*` in `run.sh`, state in `/data` = HA backups); there the
  web panel is served over plain HTTP to HA's ingress proxy only (`web_ingress_listen`,
  `web_ingress_allow`), without its own login: HA authenticates, the HA user name is the audit actor.
- Listens on several UDP ports (default 51900 and 443, configurable list) — some carriers
  block or throttle unusual UDP ports. Handles Noise handshakes from enrolled hubs.
- One session per hub. A session has N paths; a path = (path_id, current source addr:port).
  Source address may change (carrier NAT rebinding): accept after valid AEAD, update path.
- Uplink: decrypt → dedup/replay check → reorder buffer → write IP packet to TUN `xss0`.
- Downlink: read from TUN → look up session by inner dst (hub tunnel IP) → scheduler
  picks path → encrypt → send to that path's addr.
- NAT: nftables masquerade on the egress interface for `100.64.0.0/16`; the same
  `crossstream` table holds the egress filter (phase 14: `forward` chain on `xss0`
  dropping private/link-local destinations, TCP 25/445, UDP 67/68/445, the Docker API;
  `egress_allow` exceptions), also applied to PEP origin dials.
- Limits (phase 14): 2 INIT/s per source IP (burst 10) before any DH, INIT `ts` replay
  check per hub, `min_hub_version`, 64 paths per session, 64 TCP/WSS connections per
  source IP, first packet within 10 s; sysctls checked at start; optional Prometheus
  `/metrics` on a loopback/private address (`metrics_listen`).
- Each hub gets a fixed tunnel IP `100.64.0.N/32` assigned at enrollment.
- State: `/data/state.json` (enrolled hubs, tokens), config `/data/server.yaml`.
- CLI `xs-ctl` (same image): `enroll <name>`, `list`, `revoke <name>`, `rename <name> <new>`,
  `sessions [-watch]`, `forward allow|list`, `info`, `audit`, `admin add|passwd|del|list|totp`.
  Talks to xs-server over a Unix socket `/run/xs-server.sock`.
- Docker: one container (`NET_ADMIN`, `/dev/net/tun`, volume `/data`, no Docker socket).
  Bridge mode with published ports (Docker Desktop, tests) or `network_mode: host`
  (Linux production, no Docker NAT in the data path). Every config key has an
  `XS_SERVER_*` env override; `XS_SERVER_PUBLIC_ENDPOINTS` sets the addresses put into
  enrollment codes when auto-detection is wrong (NAT, Docker Desktop).
- Web panel (phase 15, `internal/webui` + `webui/` Svelte, embedded with `go:embed`):
  HTTPS on `web.listen` (default `127.0.0.1:8443`; remote access via SSH tunnel; public bind
  only with an explicit config flag). Same operations as `xs-ctl` through one shared
  admin service (`internal/server/admin`), so CLI and web never diverge:
  dashboard (hubs online, total and per-hub throughput, live via SSE), hubs (paths with
  observed IP, transport, RTT/loss; enroll → shows the XS1 code once with copy button;
  revoke; rename), port-forward allowances and active forwards, server info (ports,
  endpoints, version, `min_hub_version`), audit log (logins, enroll, revoke), recent log
  lines (redacted).
  Auth: admin users via `xs-ctl admin add|passwd|del <user>` (argon2id), optional TOTP
  (`xs-ctl admin totp <user>`), session cookie `Secure; HttpOnly; SameSite=Strict`,
  12 h TTL, CSRF token on writes, login rate limit + lockout, strict CSP. TLS: self-signed
  cert in `/data` (fingerprint printed in logs) or a user-provided cert/key.

### xs-hubd (hub daemon, `cmd/xs-hubd`, runs as root via systemd)
- Hotspot: NetworkManager connection `crossstream-ap` (mode ap, band a/bg fallback,
  WPA2-PSK, `ipv4.method shared`, `ipv6.method disabled`, fixed `cloned-mac-address`
  so the BSSID is stable — Android skips the approval dialog only for a specific SSID+BSSID).
- TUN `xs0` with the hub tunnel IP. Policy routing: `from 10.42.0.0/24 lookup 100`,
  table 100 `default dev xs0`. nft masquerade `10.42.0.0/24 → xs0`. TCP MSS clamp.
- DNS: NM dnsmasq DNS disabled via `/etc/NetworkManager/dnsmasq-shared.d/crossstream.conf`
  (`port=0`, `dhcp-option=option:dns-server,0.0.0.0` = the port address); xs-hubd serves DNS on `10.42.0.1:53` and forwards
  through the active mode (tunnel in BONDED, a relay in DIRECT).
- Relay registry: paired relays (static pubkey, name, priority, limits) in
  `/var/lib/crossstream/hub.json`. Enrollment with the server stored in the same file
  (server endpoint, server pubkey, hub private key — file mode 0600, or sealed in TPM 2.0
  when available, phase 14: `hub.key.tpm`, `xs-hubctl key migrate-tpm`).
- Log: slog text to stderr (journald) plus an in-memory ring of the last 2000 lines;
  IPC `logs.export` returns it redacted (GUI "Eksportuj logi", `xs-hubctl logs`).
  Level at run time (phase 19): a `slog.LevelVar` (start value from `-v`); IPC
  `logs.level {level, for_secs}` sets debug/info, with `for_secs` it reverts by itself
  (a new call replaces the timer); status `log_level {level, start, until}`;
  `xs-hubctl loglevel [debug|info] [-for 15m]`, GUI "Szczegółowe logi (15 min)".
- Control server: TCP `10.42.0.1:51901` (Noise, see protocol.md).
- Data socket: UDP `10.42.0.1:51902` exchanging outer packets with relays.
- IPC for the GUI: JSON-RPC over Unix socket `/run/crossstream/xs-hubd.sock` (group `crossstream`).
- Mode state machine (below).

### xs-hubd on Windows (phase 16, `CrossStreamHub` service, LocalSystem; Windows 11)
- Hotspot: Windows Mobile Hotspot through the WinRT tethering API (PowerShell script embedded
  in `netcfg/winps/`). Mobile Hotspot shares one connection profile chosen at start and does DHCP and
  NAT for it on 192.168.137.0/24 (hub address 192.168.137.1). Before enrollment it starts on any
  profile (relays only need the Wi-Fi); once the TUN `xs0` exists the hub restarts it on the TUN's
  profile, so clients are NATed to the tunnel IP (relays reconnect within seconds).
- Windows has no source policy routing: while the TUN exists, `0.0.0.0/1` + `128.0.0.0/1` go into
  it (the hub's own traffic too); relays are on-link and local uplinks use `IP_UNICAST_IF` sockets.
  The watchdog re-adds missing routes: every 30 s and, debounced by 1 s, on IP Helper change
  notifications (`NotifyIpInterfaceChange`, `NotifyUnicastIpAddressChange`, `NotifyRouteChange2`,
  phase 19); the local uplink scan uses the same events (a USB-tethered phone appears or goes
  within ~1 s).
- MSS clamp on the TUN packets in xs-hubd (`tun.ClampMSS`); no port forwarding, loop guard or
  uplink TTL rule on Windows.
- Wintun adapter with a fixed GUID per name (`wintun.dll` next to the exe); address, MTU,
  metric 5 (automatic metric off), the TUN routes, forwarding flags and captive portal host
  routes through the IP Helper API (`internal/iphlp`, active store only, phase 19); no `netsh`.
  xs-hubd runs at ABOVE_NORMAL priority (`-normal-priority` = off). DNS on the hotspot address :53 (ICS's own DNS proxy
  binds the wildcard address only). Sleep: `SetThreadExecutionState` while clients or relays are present.
- State in `C:\ProgramData\CrossStream` (protected DACL: SYSTEM, Administrators); IPC is the same
  JSON-RPC over an AF_UNIX socket `C:\ProgramData\CrossStream\xs-hubd.sock` whose ACL adds the local
  group `crossstream`; the group may also start/stop the service (the GUI uses `sc.exe`).
  Installer: `deploy/hub-windows/install.ps1`; binaries: `make hub-windows`.

### Hub GUI (`gui/`, Wails v2 + Svelte, unprivileged)
Main window: QR code for relay pairing; relays list (name, carrier, state,
up/down rate, RTT/jitter/loss, data used, priority normal/secondary/backup, speed limit,
data limit, weak Wi-Fi hint);
total throughput graph; mode badge (Bonded / Direct / Offline); connected clients count.
Settings: hotspot SSID/password/band (channel 0 = least loaded after a scan; the chosen
channel and why are shown), server enrollment (paste code), revoke relay, log export.

### Relay core (`internal/relay`, Go, compiled to AAR via gomobile)
- Control client to hub (TCP + Noise IK, hub static key from QR).
- Data relay: UDP from hub → cellular socket → server; server → hub. Forwards
  outer packets verbatim (cannot decrypt). Accepts hub packets only from the paired
  hub's address learned on the control channel; forwards only to the server endpoint
  received from the hub; accepts return traffic only from that endpoint.
- Direct-mode proxy role (fallback): dials targets over cellular on behalf of the hub.
- Counts bytes, enforces data limit, reports stats.
- Socket binding inside Go (common pattern): Kotlin only reports networks via
  `core.SetNetwork(kind, handle int64)` (`Network.getNetworkHandle()`, `0` = lost).
  A Go `Binder` binds each socket in `net.Dialer/ListenConfig.Control`:
  Android build → cgo `android_setsocknetwork(handle, fd)` (`<android/multinetwork.h>`,
  `-landroid`, API 23+); Linux build (`cmd/xs-relaysim`) → `SO_BINDTODEVICE`.
  No per-socket JNI callbacks, no fd passing through gomobile.

### Android app (`android/`, Kotlin, Compose)
- Screens: Home (big toggle, total shared, list of hubs with state), Add hub (QR scan),
  Hub detail (forget), Settings (data limit or unlimited, battery threshold default 20 %).
- Foreground service type `specialUse` (not `dataSync`: Android 15+ limits dataSync
  services to 6 h per 24 h). Persistent notification: hub name, shared MB, Stop action.
- Networks: `requestNetwork(TRANSPORT_CELLULAR + INTERNET)` for relaying;
  `WifiNetworkSpecifier` (exact SSID + BSSID + PSK) for the hub's Wi-Fi. Approval is
  remembered by Android for that exact network, enabling silent auto-reconnect.
- Auto-reconnect: when the toggle is ON, scan for saved hubs' BSSIDs, request the
  strongest visible one; retry with backoff.
- Data limit has a period: per session / per day / per month (reset day). Over the
  limit: stop (default) or throttle to N kb/s.
- Cellular health from `NetworkCapabilities`: not `VALIDATED` or `CAPTIVE_PORTAL`
  (e.g. prepaid data exhausted) → `leaving` with reason `no_data`.
- Wi-Fi RSSI of the hub link reported in stats (`< -67 dBm` = weak, hub GUI hints
  "move phone closer").
- Starts on boot (`RECEIVE_BOOT_COMPLETED`) if the toggle was ON.
- Rolling log file (redacted) + "Share logs" button (FileProvider) for support.
- Stops relaying on: data limit reached, battery below threshold, no cellular data, user Stop.

## Data path in BONDED mode
1. Client packet → wlan0 → policy route → `xs0` (masqueraded to hub tunnel IP).
2. xs-hubd assigns a session sequence number, encrypts, scheduler picks a path,
   sends the outer packet to that relay's data port.
3. Relay forwards the datagram over cellular to xs-server.
4. xs-server decrypts, dedups, reorders by sequence, writes to `xss0`, NAT out.
5. Downlink mirrors this: xs-server schedules over the session's paths.

## Bonding engine (`internal/bond`, used by both hub and server)
- **Path state**: RTT (EWMA from PING/PONG every 200 ms), jitter (rttvar), loss,
  delivery rate, in-flight bytes, priority (normal/secondary/backup), rate limit
  (token bucket), data cap, path MTU.
  States: ALIVE, SUSPECT, DEAD, SLEEPING.
- **Per-path congestion control (BBR-lite)**: windowed max delivery rate (10 RTT) =
  `btlbw`, windowed min RTT (10 s) = `min_rtt`. Pace at `btlbw × gain` (gain 1.25 for one
  RTT every 8 RTTs, else 1.0); in-flight cap `2 × btlbw × min_rtt`. Keeps cellular
  queues short so RTTs stay low and the reorder buffer small. Loss response as in
  BBRv2/v3: a round losing > 2 % ends STARTUP and sets `inflight_hi` (cut ×0.7 on later
  lossy rounds, regrown while probing); STARTUP also ends on a flat estimate after 2 s
  even when rounds are app-limited; only first transmissions give rate samples.
- **Scheduler**: earliest-expected-delivery. For each sendable normal path:
  `eta = srtt/2 + (inflight + len) / rate`; pick min. **Secondary** paths are added only
  when every normal path is at its in-flight cap or rate limit (overflow) — saves the
  friend's data. Optional hub setting `overflow_mbps` (0 = off): secondary paths are not
  used while normal paths already deliver ≥ that rate.
  **Backup** paths only when no normal/secondary path is alive.
  **Direction mode** per path and direction (upload / download): `on`, `backup_only`
  (used in that direction only when no other path is), `off` — e.g. a phone with weak
  upload used for download only.
  Respect token bucket and data cap.
- **SLEEPING**: backup paths ping every 2 s instead of 200 ms (battery + data on the
  phone); woken (200 ms pings, 3 PONGs) when they are about to be needed.
- **Reorder buffer** (receiver): release in sequence order; hold gaps up to
  `min(300 ms, 2 × (max_srtt − min_srtt) + 20 ms)`; then skip the gap
  (inner TCP recovers).
- **ACK**: receiver sends ACK frames (largest seq + 64-bit bitmap) every 20 ms or
  32 packets, piggybacked when possible, over the lowest-RTT path.
- **Fast retransmit**: sender infers loss from ACK gaps (a seq is lost when ≥ 3 later seqs
  sent on the same path are acked, or it is older than `srtt + 4×rttvar`); re-sends it
  on the lowest-ETA alive path if still inside the receiver's hold window, at most 2
  attempts per packet (a common default). Avoids
  skipped gaps, which make inner TCP halve its window.
- **Failover**: path is SUSPECT when its oldest unacked packet is older than
  `max(3 × srtt, 300 ms)` with no ACK progress — scheduler stops giving it new packets.
  DEAD after 3 missed PONGs (~600 ms) or 1 s without any packet. Sender keeps unacked
  packets per path for 1 s; on DEAD, re-sends that path's unacked packets over alive
  paths. Receiver dedups by sequence (replay window).
- **Path recovery**: DEAD path probed every 1 s; ALIVE after 3 consecutive PONGs.
- **Path identity**: the server reports each path's observed public address (PATH_INFO).
  Paths sharing one public IP use the same uplink (no gain) → status/GUI warning.
- **Path MTU**: on ALIVE, probe with padded PINGs (1452/1400/1340/1280 outer bytes, DF).
  Tunnel MTU and MSS clamp follow the smallest path MTU (floor 1280 − overhead).

## Modes (xs-hubd state machine)
```
OFFLINE ──(≥1 path alive)───► CONNECTING ──(handshake ok)──► BONDED
   ▲                              │ (no handshake for 15 s)       │ (no valid packet 10 s
   │                              ▼                              ▼   + retries fail)
   └────(0 paths alive)───────  DIRECT ◄──────────────────────────┘
                                  │ probe server every 30 s; success → BONDED
```
- **DIRECT (serverless fallback)**: xs-hubd terminates client TCP/UDP flows in a
  userspace netstack (gVisor, as used by tun2socks) attached to `xs0`, and forwards each
  flow through one relay's proxy role. Flow → relay assignment: weighted by relay rate,
  sticky per destination IP (keeps sessions/logins on one public IP). Backup relays only if
  no normal relay is alive. A dead relay's flows are reset (cannot be migrated).
- **DIRECT → BONDED**: new flows go to the tunnel; existing direct flows are drained
  (kept on netstack via a flow table) for up to 5 min, then reset.

## Extra features (phases 10–13, common bonding patterns — see `docs/prior-art-notes.md`)

### Server transports (phase 10)
- Endpoints are URIs: `udp://host:51900`, `udp://host:443`, `tcp://host:51900`,
  `wss://host:443/xs`. Only the relay↔server hop changes; hub↔relay stays UDP on the LAN.
- TCP/WSS framing: `len u16 | outer packet`. WSS = WebSocket binary frames over TLS
  (server self-signed cert, relay does not verify it: Noise already authenticates the
  server; TLS only makes traffic look like HTTPS).
- Auto order per path: UDP endpoints → TCP → WSS. Hub switches with the `endpoint`
  message after 5 s without PONG on that path while other paths work.
- One TCP/WSS connection = one path address on the server (same roaming rules as UDP).
  Phase 20: a `tcp://` path is a set of 2 TCP connections (4 while its TCP retransmission
  ratio is high; the hub decides from the relay's `TCP_INFO` counters in `stats`), packets
  round-robin, replies on the connection of the newest packet; a failed connection is
  redialed while the others carry the path (protocol.md "TCP/WSS framing").
- TCP-over-TCP: on TCP/WSS paths the bond skips fast retransmit (transport is reliable;
  re-send only on DEAD), adds a 20 ms ETA penalty so UDP paths are preferred, and the
  server sets `TCP_NODELAY` + `TCP_CONGESTION=bbr` when available.

### Redundancy (phase 10)
- Hub setting `redundancy`: `off` / `auto` (default) / `on`, sent to the server in
  SESSION_CONFIG so both directions behave the same.
- `on`: every IP frame is sent on the 2 lowest-ETA paths (second copy may go on a
  secondary path, never on backup). `auto`: while any normal path shows loss > 2 %
  or jitter > 50 ms (and 30 s after), real-time packets (UDP/ICMP, ≤ 512 B) get the second copy;
  TCP is left alone (decisions.md phase 10). `min_latency` mode: small (≤ 256 B) and
  real-time packets always get it.
- Always (even `off`): duplicate tiny critical packets — DNS (UDP/53), TCP
  SYN/SYN-ACK/FIN/RST, handshake and control frames.
- Duplicates count against path rate limits and data caps. Receiver dedup = replay window.

### Packet aggregation (phase 10)
- Sender batches IP frames ≤ 256 B for at most 1 ms per path into one DATA packet
  (up to path MTU). Saves per-packet overhead (ACKs, small TCP segments, VoIP).

### Speed test (phase 10)
- `xs-hubctl speedtest [--path <relay>|--all] [--dir down|up|both] [--secs 10]`,
  also a GUI button. Uses TEST_* frames: filler pinned to one path (or spread on all).
- Not inner TCP — measures raw path capacity. Hard cap `max_mb` per test (default 20 MB
  per path); the GUI shows the data cost and asks for confirmation (friends' data).
- Sender ramp (phase 18): 4 Mb/s, doubling every 0.4 s up to 1 Gb/s, until the budget or the
  duration is used up; the receiver reports the best 0.5 s window. The schedule is fixed, so
  the hub knows how long the sender ran above a measured rate: under one window → the result
  is a lower bound (`at_least`, shown as "≥"). 20 MB measures up to ~130 Mb/s exactly.
- Results (per path + bonded Mb/s, RTT, loss) kept in the usage store and exported for
  the field-test table (phase 18).

### Bypass list (phase 11)
- Hub setting: list of domains (suffix match) and CIDRs that skip the server and go out
  directly through one relay (carrier IP), while the mode is BONDED. Use case: banks /
  services that block datacenter IPs, or the owner's choice.
- The hub DNS forwarder records answers for bypass domains → IP set with TTL (min 5 min).
  xs-hubd dispatches packets read from `xs0` by destination: IP in set → `internal/direct`
  netstack, else → bonding engine. No kernel routing changes.
- Relay proxy role is enabled in BONDED when the bypass list is non-empty (`mode`
  message `proxy:true`). Relay choice = DIRECT rules (sticky per destination IP).
- If no relay can proxy → traffic falls back to the tunnel (logged once).
- Canary answers (phase 19): while the list is not empty the DNS forwarder answers
  NXDOMAIN itself, any query type, for exactly `use-application-dns.net`, `mask.icloud.com`
  and `mask-h2.icloud.com`: Firefox drops its default DoH and Apple devices turn off
  Private Relay on the hotspot, so their lookups reach the forwarder (and the bypass set).
  An empty list forwards them as usual.
- GUI shows the same privacy warning as DIRECT for bypassed domains.

### Usage history (phase 11)
- Hub store `/var/lib/crossstream/usage.json`: per relay per day `{up, down}` bytes,
  failovers survived ("seamless failovers" counter shown in the GUI),
  speed-test results; keep 400 days, atomic writes every 60 s and on shutdown.
- IPC `usage.get(relay?, from, to)`; GUI "Statystyki" view (was "Podziękowania"): per friend this month / all time,
  top sharers, chart per day.

### Friend-side priority cap (phase 11)
- Relay app setting "How can this hub use my data": `normal` / `only when needed`
  (secondary) / `only as backup`. Sent as `limits.max_priority` in `hello` and in live
  `limits` updates.
- Effective priority = more restrictive of hub setting and friend cap; GUI shows a lock
  icon and does not offer higher levels.

### Port forwarding (phase 11)
- Server: `xs-ctl forward allow <hub> <n>` sets how many public ports a hub may request
  (default 0); restricted ports (server listen ports, 22) are refused; ports are unique
  across hubs.
- Hub requests ports with FWD_REQ; server DNATs `public:port → hub tunnel IP:port`
  (nft) and answers FWD_ACK. Hub DNATs `tunnel IP:port → client` chosen by MAC (current
  DHCP lease from the hotspot; re-resolved on lease change). BONDED mode only.
- IPC `forwards.list/set`, GUI editor; `xs-ctl sessions` shows forwards.

### Desktop notifications (phase 11)
- xs-hubd emits typed events on `events.subscribe`: relay joined/left (+ reason),
  cap 80 % / 100 %, relay battery < 25 %, mode change, weak Wi-Fi, speed test done.
- GUI shows them as OS notifications (`github.com/gen2brain/beeep`), each type toggleable
  in Settings; at most 1 notification per type per relay per 5 min.

### Local uplinks on the hub (phase 12)
- Any hub interface with internet other than the hotspot and `xs0` can be a path:
  phone via USB tethering (Android `usb0`/`enx*` via RNDIS/NCM, iPhone `eth*`/`apple0`
  via `ipheth` + `usbmuxd`), Ethernet, USB LTE modem (`wwan*`), a second Wi-Fi card.
  No app on the phone; USB also charges it.
- Detection (`internal/hub/uplink`): netlink link/route events (debounced 1 s) plus a 10 s
  scan; an interface qualifies when it is up, has an IPv4 address and a default route of its
  own. New interfaces show up in the GUI as "local links", disabled until the owner enables
  them once (remembered by MAC address in `uplinks.json`; path ids 200–254). Type from name,
  driver and DEVTYPE: USB tether, Bluetooth PAN (backup by default), modem, Ethernet, Wi-Fi.
- Per link, as in the relay app: on/off, name, priority, directions, rate limit, data cap per
  session/day/month (reset day) with stop or 256 kb/s throttle, counter reset — the cap is
  counted by the hub (`internal/datacap`). Modems: signal and access tech read from
  ModemManager (`mmcli`, read only) feed the GUI weak-cellular hint.
- Data path: `LocalPath` in xs-hubd sends outer packets on a socket bound to the
  interface (`SO_BINDTODEVICE`) straight to the server — no relay hop. Same bond path
  state, priorities, direction modes, limits, transports (phase 10) and usage history as
  relay paths. The hub-side path abstraction is `PathConn` with two impls:
  `relayPath` (via relay data channel) and `localPath` (its own server connection, same
  endpoint fallback as relays). Enrollment works through a local link alone.
- DIRECT mode: a local uplink can also carry direct flows (dial bound to the interface).
- System setup per uplink (cleanup on stop): `rp_filter=2` (loose) and
  optional nft `ip ttl set 65` on egress for tethered phones (carriers detect tethering by
  TTL 63; toggle per link, default on for USB tethers). Route metrics are left to
  NetworkManager: bound sockets use the interface's own default route whatever its metric.
- Captive portals (phase 19, `uplink.ProbeCaptive`): an HTTP GET of a `generate_204` URL
  (`-captive-url`, default `connectivitycheck.gstatic.com`, `off` = never) on a socket bound
  to the link, no redirects followed, name resolved by the system resolver. 204 (or 200
  empty) = `open`; a redirect or 200 with a body = `captive` (redirect URL kept); anything
  else = `no_internet` (shown only). Probed when a link's path starts and when its path has
  not been ALIVE for 15 s (once until it was alive again) — never while healthy. A captive
  link has state `captive` and its path is stopped; rechecked every 30 s for 10 min, then
  only on the next change (other interface or address) or a manual check. IPC
  `uplinks.captive {uplink, open}` probes now; with `open` it adds a host route for the
  portal's addresses through the link for 10 min (`ip route replace` on Linux,
  `CreateIpForwardEntry2` on Windows) and the caller opens the browser (`xs-hubctl uplinks captive -open`,
  GUI "Zaloguj"). When the route fails the hint is "Zaloguj się w tej sieci na telefonie
  lub wyłącz hub na chwilę".
- The GUI starts `xs-hubd` (`systemctl start`, polkit rule for group `crossstream`) when its
  socket is missing and asks on window close whether to keep the hub running or stop it.
- Mode state machine counts local uplinks as paths ("≥ 1 path alive").

### Performance Enhancing Proxy — PEP (phase 13)
- Problem: client TCP runs end to end over a bonded, lossy, mixed-RTT path; reordering,
  skipped gaps and RTT spread keep its window small. PEP splits TCP: the client's TCP ends
  at the hub (LAN RTT, no loss), the origin's TCP ends at the server (server RTT, BBR),
  and only byte streams cross the bond, carried reliably.
- Hub: when PEP is on, new client TCP flows on `xs0` (except excluded ports/CIDRs and
  bypass) are accepted by the gVisor netstack (shared with DIRECT through its
  `Intercept` hook; PEP flows do not count as DIRECT flows). Each flow = a stream:
  STREAM_OPEN → server dials the origin (`TCP_CONGESTION=bbr`; the origin sees the
  server address, like masqueraded packet-mode traffic); STREAM_DATA carries bytes with offsets; STREAM_WINDOW is per-stream flow
  control (default 4 MB window, session memory cap 256 MB); STREAM_CLOSE / STREAM_RESET.
- Reliability: stream frames are a "reliable" class in the bond — retransmitted until
  acked (never skipped by the reorder buffer); reordering is per stream by offset, so one
  lost packet does not block other streams. They share the IP seq space (protocol.md)
  and their own send queue takes turns with the IP queue; a 256 KB backlog limit makes
  the PEP pump wait instead of queueing whole windows.
- Server announces PEP support in the handshake RESP (`"pep":true`); otherwise the hub
  keeps packet mode. Existing flows stay in their mode when PEP is toggled.
- Default: off until the phase 13 measurement shows a clear gain, then on.
- Limits: max 4096 streams per hub, idle stream timeout 2 h (TCP keepalive on origin side).

## Security model
- Hub↔server: Noise IK with enrolled hub static keys. Revocation removes the key and
  kills the session.
- Hub↔relay: Noise IK, hub static key from QR. First pairing carries a one-time
  token; afterwards the relay's static key is the credential. Hub can forget a relay.
- Relays see only encrypted outer packets and target addresses in DIRECT mode
  (DIRECT mode is not end-to-end hidden from relays: they see destinations,
  and plaintext for non-TLS flows — shown as a warning in the GUI).
- LAN: WPA2-PSK hotspot; PSK is in the QR.
- Bypass traffic has the same exposure to the relay as DIRECT mode.
- Port forwarding exposes client services to the internet; off by default on both server
  (allowance 0) and hub.
