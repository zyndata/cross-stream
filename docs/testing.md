# Testing

## Levels
1. **Unit** (`go test ./internal/...`): wire, crypto, bond scheduler/reorder with a fake clock.
2. **E2E in Docker** (`make e2e`): whole system without phones, on simulated cellular links.
3. **Real devices**: Android phones + Linux hub (phases 07+), server on Docker Desktop (Windows PC).
4. **Field test** (phase 18): weak-coverage location, different carriers.

Windows-only elevated tests (phase 19: `internal/tun` adapter setup + timing, `internal/hub/netcfg`
change events, TUN routes/forwarding, host route; `cmd/xs-hubd` priority) create test Wintun
adapters `xstest*` and skip unless the process is elevated and `wintun.dll` sits next to the test
binary: `GOOS=windows go test -c ./internal/tun` (and `./internal/hub/netcfg` with its `testdata`
folder), copy to the hub laptop next to `wintun.dll`, run from an elevated shell (an admin SSH
session is elevated). The routes test takes the PC's internet away for about a second.

## E2E environment (`testenv/docker-compose.yml`)
```
 net "internet": origin (nginx serving /big.bin 200 MB), iperf (iperf3 -s, test image), server (xs-server)
 net "cell1..3": relay1..3 each also on its own cellX net; a "carrierX" router container
                 per cell net NATs into "internet" and applies netem
 net "lan":      hub (xs-hubd headless, no hotspot: lan iface plays wlan0), client, relay1..3
 net "cell4":    hub + carrier4: the hub's own local uplink (phase 12; Docker's default route
                 replaced by carrier4 at metric 500, the link stays off until "localpath")
```
- netem per carrier (defaults, override via env): `rate 8mbit delay 60ms 20ms loss 0.5%`.
- relays = `cmd/xs-relaysim` (same relay core as Android).
- client = test image (curl, iperf3, jq); default route via hub.
- xs-hubd runs with `--headless --lan-iface eth0` (skips NetworkManager hotspot).
- Setup in `run.sh`: `xs-ctl enroll hub1` → `xs-hubctl pair-qr` → relays start with that
  XSR1 code → `xs-hubctl enroll <code>` through the relays (the local uplink on cell4 is still off).
- Networks are plain bridges, not `internal` (Docker drops off-subnet bridged packets on
  internal networks, which breaks the carrier routers); `run.sh` checks that the client and the
  hub's LAN side cannot reach the origin without the tunnel (the hub itself can, through
  carrier4). Fixed addresses: internet 172.30.0.0/24
  (server .2, origin .10, carrierN .1N, iperf .30), cellN 172.31.N.0/24 (carrier .254,
  relay .1N), lan 10.43.0.0/24 (hub .1, relayN .1N, client .20; not 10.42.0.0/24, the hub hotspot default: the e2e runner is the real hub). cell4 172.31.4.0/24 (carrier4 .254, hub .4).
- `run.sh` env: `NETEM=auto|1|0`, `NETEM_ARGS`, `DL_MB` (download size per relay, default 20),
  `IPERF_S`, `KEEP=1`. Every scenario starts a fresh environment; `all` runs them one by one
  (~20–40 min) and keeps the logs of a failed one in `testenv/out.<name>/`; `short` runs
  `ping failover limits` (~6 min). CI runs `short` when the head commit message contains
  `[e2e short]` (or a manual run picks it), `all` otherwise; the owner chooses before each push.
- Scripts read state with `xs-hubctl status | jq` (hub side, upload direction) and
  `xs-ctl sessions -json` (server side, download direction).

## Scenarios (`testenv/run.sh <name>`, each prints one PASS/FAIL line + key numbers)
| Name | Check |
|---|---|
| ping | client pings origin through the tunnel |
| download | 1 link (relay2/3 directions off, `DL_MB` MB) vs 3 links (200 MB); 3 links ≥ 2.4× |
| forget | `xs-hubctl forget relay1`: relay is told to forget the hub; reconnecting with the old code is rejected (`forgotten`) |
| iperf | iperf3 TCP `IPERF_S` s down and up, report Mb/s |
| failover | kill -9 relay2 at 10 s of a 60 MB download; max stall (file not growing, 0.1 s ticks) < 1 s, download completes |
| rejoin | kill relay2, start it again during a download; path ALIVE and carrying traffic within 5 s |
| backup | relay3 = backup; 0 data bytes while 1–2 normal relays alive, takes over when both die, download completes |
| secondary | relay3 = secondary; UDP 6 Mb/s: ≤ 1 % on relay3; UDP 25 Mb/s: ≥ 10 % on relay3 |
| loss | 2 % loss on all links: iperf3 TCP, 4 flows, 2 alternating rounds of 20 s: 3 links ≥ 2× 1 link (fast retransmit works) |
| latency | Hub mode `LAT_MODE` (default `min_latency`; `max_throughput` = phase 04 baseline). ICMP echo 90/s (game-like) during a TCP download, relay2 killed at 15 s, then again while the client saturates the upload (iperf3 4 flows, bufferbloat): p50/p99/max RTT, loss, longest gap for both; `min_latency` needs p99 under the download < 490 ms (baseline ~700) |
| udpblock | carrier2 drops all UDP to the server (nft): relay2 falls back udp:51900 → udp:443 → tcp (→ wss) within 60 s; a 60 MB download stays bonded (relay2 ≥ 10 %, ≥ 8.5 Mb/s, i.e. more than one link) |
| redundant | 8 % loss on every link; ping 50/s with redundancy off, then on: on needs < 3 % loss (expected ~1.3 % with 2 copies, runs 0.6–2.2 %) and less than off; the server's session shows redundancy `on` (SESSION_CONFIG) |
| speedtest | `xs-hubctl speedtest` per relay (5 s each way, 10 MB cap): down and up within ±15 % of the 8 Mb/s netem rate; then all relays at once; 4 results stored |
| aggregation | iperf3 UDP upload, 200-byte packets, 4 Mb/s, 10 s, with `-aggregation=false` then `true`: outer datagrams (hub `tx_pkts`) ≥ 30 % fewer with aggregation |
| bypass | the client first asks the DoH canary `use-application-dns.net` (dnsmasq answers .42): forwarded while the list is empty, NXDOMAIN from the hub once it is set; then `xs-hubctl bypass set bypass.test`; the client resolves names through the hub DNS (`-dns -dns-upstream 172.30.0.53`, dnsmasq in the `dns` container: bypass.test → .41, tunnel.test → .42); the origin nginx log shows a carrier IP (172.30.0.11–13) for bypass.test and the server 172.30.0.2 for tunnel.test |
| portfwd | `xs-ctl forward allow hub1 1`; busybox httpd on the client :8080; `xs-hubctl forwards set tcp/8080=10.43.0.20 tcp/8081=…`: 8080 active, 8081 refused `not_allowed`; an "internet host" 192.0.2.31 (extra address on the iperf container, server route via it) fetches the page through 172.30.0.2:8080; after `xs-ctl revoke hub1` the server DNAT rule is gone. (Docker drops the client's replies on the lan bridge to another Docker subnet such as 172.30.0.0/24, so the origin cannot be the requester.) |
| prio-cap | relay3 restarted with `xs-relaysim -max-priority backup`, hub sets it `normal`: effective priority `backup`, 0 data bytes on relay3 during a 30 MB download |
| localpath | the hub's cell4 interface is listed off, enabled with `xs-hubctl uplinks set`, bonds with the 3 relays (carried data), `ip link set down` during a 60 MB download: max stall < 1 s, link shown `absent`; link up + route back: alive again |
| pep | links 8/8/2 Mb/s, RTT 40/60/300 ms, 1 % loss each way: curl `PEP_MB` (200 MB) and one iperf3 flow `IPERF_S` s down and up with PEP off, then on; prints both and the gain; needs ≥ 1 PEP flow while on, none while off. `PEP=on` runs any scenario with PEP on (`all` too) |
| limits | relay1 alone at 2 Mb/s limit, UDP 4 Mb/s offered: server sends 2000 kbit/s ±5 %; then cap 50 MB during a 200 MB download: stops at 50–52.5 MB |
| fallback | `docker compose stop server`: hub enters DIRECT ≤ 20 s; 3 parallel downloads from 3 origin IPs (`FB_MB`, default 15 MB) each carried by a different relay; a 60 MB download in DIRECT, then `start server`: BONDED with that flow draining, new flows (ping) through the tunnel, download completes, proxy role off. Server state is on a volume, origin also listens on .41/.42 |
| mixed | links 8/8/2 Mb/s, RTT 40/60/120 ms, 0.5 % loss: 100 MB download ≥ 14 Mb/s |
| revoke | `xs-ctl revoke hub1`; session dies, re-handshake rejected |
| udpblock | carrier2 drops UDP to the server; relay2 moves to TCP/WSS, download stays bonded (phase 10) |
| redundant | 8 % loss on all links, redundancy `on`: ping loss < 3 % and below `off` (phase 10, widened phase 14) |
| speedtest | per-path results within ±15 % of netem rates (phase 10) |
| bypass | bypass domain exits via a relay's carrier IP, rest via server IP (phase 11) |
| portfwd | an internet host reaches a web server on the client via the server's forwarded port (phase 11) |
| prio-cap | relay3 caps itself to backup; carries 0 bytes while others alive (phase 11) |
| localpath | 3 relays + 1 hub local uplink bonded; uplink down → failover < 1 s (phase 12) |
| pep | mixed RTT 40/60/300 ms, 1 % loss: throughput PEP off vs on (phase 13) |
| webui | panel over HTTPS from the server container: CSP header and built index, login cookie flags, live paths with relay names + SSE ticks, CSRF-less write rejected, rename (hub reconnects) / forward / revoke via API audited, hub re-enrolls with a code from the API, tunnel clients get 403, 5 bad passwords → 429 lockout (phase 15) |
| update | in the hub container (root, no systemd: a stub `systemctl`): `xs-hubctl update -apply` with a release-shaped `v9.9.9` archive refuses a wrong SHA-256 and an archive of the other architecture, then runs the real `install.sh` + `gui/install.sh` — xs-hubd and the GUI replaced, unit, defaults, group, 0700 state dir, `daemon-reload` then `try-restart xs-hubd.service` (phase 18) |
| sleep | `docker compose pause relay2` for 20 s (a phone in Doze): its path goes dead, the tunnel keeps working, and the path is alive again after the wake. Then `pause hub` for `SLEEP_S` (200 s, longer than the server's 3-min idle timeout; the server must expire the session): the tunnel is back within 10 s of `unpause` (wake detection, not 30 s of silence) and all 3 paths are alive (phase 18) |
| captive | carrier4 plays a hotel network (nft: TCP 80 from cell4 → origin :8080, which answers 302 to `/login`; UDP/TCP to the server dropped); xs-hubd probes `-captive-url http://172.30.0.10/generate_204` (origin answers 204). Enabling the local link: state `captive` with the login URL, path held down; `xs-hubctl uplinks captive <id> -open` shows the page and adds a host route to it via cell4 (the page loads through it); rules removed ("login"): path alive within 60 s (phase 19) |
| tcploss | relay1 alone forced to TCP (UDP to the server dropped on carrier1), netem `TCPL_RATE` (20 Mb/s) 60±10 ms; `TCPL_MB` (20 MB) download with one TCP connection (`xs-relaysim -tcp-conns 1`) and with the hub's choice (2, 4 on heavy loss) at 0 % and 2 % loss: 2 % and 0 % loss ≥ 0.85× one connection (phase 20; the 1.3× bar went with the 2026-11-26 bond fixes, the bar against the 0 % result on 2026-11-27) |
| tcpconns | on demand, not in `all` (phase 19 G spike): relay1 alone forced to TCP (UDP to the server dropped on carrier1), `xs-relaysim -tcp-conns 1/2/4`, netem `TCPC_RATE` (20 Mb/s) 60±10 ms, loss 0/1/2/5 %: `TCPC_MB` (20 MB) download per cell, SPIKE lines with Mb/s |
| capdrop | links 8 Mb/s; carrier1 cut to 0.8 Mb/s after a 40 MB download: first 30 MB on 3 paths ≥ 0.4 ×, the next ≥ 0.75 × relay2+3 alone — a no-collapse smoke check; the regression test is `TestSimCapacityDrop` (phase 18) |

## Notes
- Requires `/dev/net/tun`, `NET_ADMIN`, kernel module `sch_netem`.
  Docker Desktop (WSL2) may lack netem: `run.sh` first tries `tc qdisc add ... netem` on
  carrier1; if that fails it stops with a hint — re-run with `NETEM=0` (no link emulation)
  or use the Linux laptop. Docker Desktop 29 on the Windows PC has netem (checked 2026-11-11).
- CI: e2e needs `/dev/net/tun`, `NET_ADMIN` and `sch_netem`, and costs 10–20 min per run —
  it runs on the owner's self-hosted Linux runner (free minutes), not on GitHub-hosted
  runners. Without that runner, run `make e2e` locally before `/phase-done` and before a
  release. Details: `docs/DEVELOPMENT.md` "CI budget".
- `RACE=1 ./run.sh <scenario>` (phase 14) builds every binary with `-race` (cgo on musl) and
  sets `GORACE=log_path=/tmp/race`; any report in server, hub, relays, carriers or client
  fails the scenario ("race" line, reports in `testenv/out/race.txt`). Slower: thresholds
  that measure throughput may need a re-run.
- The server's egress filter drops private destinations; testenv and the smoke test allow
  their private "internet" with `XS_SERVER_EGRESS_ALLOW`.
- Output discipline: scripts print summaries only; full logs to `testenv/out/*.log`
  (git-ignored, denied for reading unless debugging a failure — then use `rg`/`tail`).
