# CrossStream server (Docker, Home Assistant)

One container runs `xs-server` (and `xs-ctl` for admin). It needs `NET_ADMIN`
and `/dev/net/tun` to create the tunnel interface `xss0` and its nftables NAT
rule (own table `crossstream`, removed on stop). State lives in `./data`
(`server.key`, `state.json`); back up that directory, never commit it.

Configuration: optional `data/server.yaml` (see `server.example.yaml`). Every key
can be set with an environment variable `XS_SERVER_<KEY>`; put them in a `.env`
file next to `docker-compose.yml` (git-ignored). The important one:

- `XS_SERVER_PUBLIC_ENDPOINTS` — the address hubs use to reach the server, written
  into enrollment codes. Comma-separated; each entry is a bare host (one
  `udp://host:<port>` per listen port), `host:port`, or a full URL. Without it the
  server uses its own outbound address, which is wrong behind NAT and on Docker
  Desktop.
- `XS_SERVER_LISTEN_PORTS` — UDP ports (default `51900,443`).
- Tunnel port (`public_port` in `data/server.yaml`): the port hubs dial when the router maps
  another public port to the server's 51900, e.g. router `51901` → server `51900`. Default:
  the listen port. Change it with `xs-ctl tunnel-port 51901` or in the panel (**Server** →
  **Tunnel port**): both write `public_port` into `data/server.yaml`, the only place it is
  kept, and the panel shows "Server restart required" until `xs-ctl restart` (or its button): the
  server exits and the compose `restart: unless-stopped` policy starts it again.
  `XS_SERVER_PUBLIC_PORT` overrides the file (then the panel shows it read-only).
  Enrollment codes carry the port: after a change, hubs enrolled before must enroll again
  (the log, `xs-ctl list` and the panel name them; revoke, then a new code). Without port
  translation on the router, change the published ports in `docker-compose.yml` (bridge) or
  `XS_SERVER_LISTEN_PORTS` / `XS_SERVER_TCP_PORTS` (host networking) instead.
- `XS_SERVER_EGRESS_ALLOW` — private prefixes hubs may still reach. By default the
  tunnel cannot reach 10/8, 172.16/12, 192.168/16, 169.254/16 (cloud metadata), the
  Docker API, or ports TCP 25/445 and UDP 67/68/445 anywhere (phase 14).
- `XS_SERVER_MIN_HUB_VERSION` — refuse hubs older than `vX.Y.Z` (logged; empty = any).
- `XS_SERVER_METRICS_LISTEN` — Prometheus `/metrics` on a loopback or private
  `ip:port` (e.g. `127.0.0.1:9100` in host mode); never a public address.

Hardening (phase 14): the compose file runs the container with a read-only root
FS, no core dumps, bounded logs and `no-new-privileges`, and sets the namespaced
sysctls. Host-wide sysctls (fq, BBR, `suid_dumpable`; in host mode all of them)
come from `90-crossstream.conf`:

```sh
sudo cp 90-crossstream.conf /etc/sysctl.d/ && sudo sysctl --system
```

xs-server logs `sysctl not applied` at start for each one still missing.

## Admin

```sh
docker compose exec server xs-ctl enroll <hub-name>   # prints the XS1 code (single use, 24 h)
docker compose exec server xs-ctl list                # enrolled hubs
docker compose exec server xs-ctl sessions            # hub, paths, rx/tx, rate, last seen
docker compose exec server xs-ctl sessions -watch     # the same, refreshed every second
docker compose exec server xs-ctl revoke <hub-name>   # deletes the key, drops the session at once
docker compose exec server xs-ctl rename <hub> <new>  # an online hub reconnects under the new name
docker compose exec server xs-ctl info                # ports, endpoints in codes, version
docker compose exec server xs-ctl audit               # who did what (CLI and web panel)
```

On the hub: `xs-hubctl enroll <code>` or the hub GUI. A revoked or moved hub enrolls
again the same way with a new code (xs-hubd replaces `server.json` and restarts itself).

## Web panel (phase 15)

The same operations in a browser: dashboard (hubs online, 5-minute throughput),
hubs with their paths live (relay name, observed address, transport, RTT, jitter,
loss, rates), enroll (the XS1 code is shown once), rename, revoke, port-forward
allowances, server info, audit log and the last 200 log lines (redacted).

1. Create an admin (password asked twice, never echoed; `-password-stdin` for scripts):
   ```sh
   docker compose exec server xs-ctl admin add <user>
   docker compose exec server xs-ctl admin totp <user>   # optional 2FA: scan the QR code
   ```
   Also `admin passwd|del|list`; `admin totp <user> -off` turns 2FA off.
2. Open `https://localhost:8443` on the server machine. The certificate is
   self-signed (`/data/web.crt`); compare its SHA-256 with the `web panel listening
   … cert_sha256=` log line (or set `XS_SERVER_WEB_CERT`/`XS_SERVER_WEB_KEY` to your
   own PEM pair).
3. From another machine use an SSH tunnel, never a public port:
   ```sh
   ssh -N -L 8443:localhost:8443 <server>
   ```
   then open `https://localhost:8443` locally. `-N` only forwards (no shell); the
   window stays open and silent while you use the panel, Ctrl+C closes the tunnel.
   If port 8443 is taken on your machine (e.g. a Docker Desktop server runs there
   too), pick another local port: `-L 9443:localhost:8443` → `https://localhost:9443`.

The panel listens on `127.0.0.1:8443` only: bridge mode publishes `127.0.0.1:8443`
on the host (inside the container it binds all addresses with
`XS_SERVER_WEB_ALLOW_PUBLIC=true`), host mode binds loopback directly. Hubs and
the devices behind them get 403. `XS_SERVER_WEB_LISTEN=off` turns it off.
Security: login with a 5-per-minute limit per address and a 10-minute lockout after
5 failures, optional TOTP, a 12 h `Secure; HttpOnly; SameSite=Strict` cookie, a
CSRF token on every change, a strict CSP, and every change in the audit log
(`/data/audit.jsonl`).

## Windows PC with Docker Desktop

Bridge mode with published UDP ports (the default `docker-compose.yml`).

1. In `deploy/server`, create `.env` with your router's public address or DNS name:
   `XS_SERVER_PUBLIC_ENDPOINTS=203.0.113.5` (your address).
2. `docker compose up -d --build`, then `docker compose logs server` should show
   `xs-server listening`.
3. Windows firewall (PowerShell as administrator):
   `New-NetFirewallRule -DisplayName "CrossStream UDP" -Direction Inbound -Protocol UDP -LocalPort 51900,443 -Action Allow`
4. Router: forward UDP 51900 (and 443 if used) to this PC's LAN address. A dynamic
   public IP changes the address in codes: use a DynDNS name in step 1.
5. `docker compose exec server xs-ctl enroll test` and enroll a hub with the code.

Docker Desktop relays published UDP through its own proxy, so `xs-ctl sessions`
shows a Docker gateway address instead of the hub's real address. That is expected;
use a Linux server with host networking for real traffic.

Docker Desktop also carries the container's outgoing TCP through a user-space proxy.
Downloads forwarded for hubs stay at a few Mb/s per connection there, however fast the
links are (measured 2026-11-16: 3 Mb/s forwarded, 40 Mb/s for data sent by the server
itself over the same two links). The hub's TCP accelerator avoids that proxy path:
`xs-hubctl settings -pep on` (GUI: "Przyspieszanie TCP") gave 34–73 Mb/s on the same setup.

## Linux host (VM, VPS, home server): host networking, recommended

Host mode keeps Docker NAT and the userland proxy out of the data path. The nft
rules then live in the host namespace; xs-server only touches its own
`crossstream` table. Tested on Ubuntu Server 24.04 with Docker Engine 29,
phase 17. The host needs Docker Engine (not Desktop) with the compose plugin; the user
that deploys is in the `docker` group.

### 1. Host setup (once)

```sh
# Docker must not set the iptables FORWARD policy to DROP (see the pitfall below)
echo '{"ip-forward-no-drop": true}' | sudo tee /etc/docker/daemon.json
sudo systemctl restart docker && sudo iptables -P FORWARD ACCEPT
mkdir -p ~/crossstream && echo "XS_SERVER_PUBLIC_ENDPOINTS=<your DDNS name>" > ~/crossstream/.env
```

After the first deploy (it copies `90-crossstream.conf` to `~/crossstream`):
`sudo cp ~/crossstream/90-crossstream.conf /etc/sysctl.d/ && sudo sysctl --system`
(forwarding, rp_filter, no redirects, fq + BBR, `suid_dumpable=0`; xs-server logs
`sysctl not applied` for any that is still missing).

**Pitfall (checked on the VM):** when Docker enables IP forwarding it sets the
iptables `FORWARD` policy to `DROP`. In host mode that drops all hub traffic between
`xss0` and the uplink: hubs connect, the handshake works, but nothing reaches the
internet. Our accept in the nft `crossstream` table cannot override a drop in
another table. `ip-forward-no-drop` (Docker 28+) stops Docker from doing that; it
does not reset a policy that is already `DROP`, hence the `iptables -P` once (a
reboot also resets it). Check: `sudo iptables -L FORWARD -n | head -1` shows
`policy ACCEPT`. Older Docker instead: allow `xss0` in `DOCKER-USER` after every
Docker start (e.g. a systemd unit `After=docker.service`):

```sh
sudo iptables -I DOCKER-USER -i xss0 -j ACCEPT
sudo iptables -I DOCKER-USER -o xss0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
```

Bridge mode on a Linux host instead: also set `"userland-proxy": false` (published
ports through iptables, not a user-space proxy). Do not set it in host mode: it makes
Docker load `br_netfilter`, so bridged traffic passes iptables, and Docker 28's
per-container drop rules (`raw PREROUTING -d <container ip> ! -i <its bridge> -j DROP`)
then break anything that routes through a container, e.g. the e2e testenv on the same
host (seen on the VM runner, phase 17). Removing it needs a reboot (or
`sudo sysctl net.bridge.bridge-nf-call-iptables=0`) to take effect.

### 2. Deploy, upgrade, rollback (from your machine over SSH)

`remote-deploy.sh` copies the compose files and itself to `~/crossstream`, loads the
image tarball (`docker load`, no registry), starts that version and runs the health
check: `xs-ctl info` reports the version and `xs-hubctl probe` completes a handshake
on `127.0.0.1` with the probe hub `healthcheck` (enrolled on first use; it shows in
`xs-ctl list`, do not revoke it). A failed check switches back to the version that
ran before. `.env` on the host keeps `XS_IMAGE_TAG` and `XS_PREVIOUS_IMAGE_TAG`;
other image tags are removed.

```sh
make server-image VERSION=v1.2.3                 # bin/xs-server-image-v1.2.3-amd64.tar.gz (or the Release asset)
export XS_DEPLOY_HOST=xs@<host>            # XS_DEPLOY_DIR, XS_DEPLOY_MODE=host|bridge, XS_DEPLOY_SSH
sh deploy/server/remote-deploy.sh deploy v1.2.3 bin/xs-server-image-v1.2.3-amd64.tar.gz
sh deploy/server/remote-deploy.sh rollback       # back to XS_PREVIOUS_IMAGE_TAG (health-checked)
sh deploy/server/remote-deploy.sh status         # versions, container, xs-ctl info
sh deploy/server/remote-deploy.sh probe          # health check only
```

Online hubs lose the session for a few seconds during the switch and reconnect by
themselves. Before deploying a server that changes `docs/protocol.md`, see the
compatibility rule in `.claude/skills/release/SKILL.md`. By hand on the host:
`cd ~/crossstream && XS_IMAGE_TAG=<version> docker compose -f docker-compose.yml -f docker-compose.host.yml up -d`
(the same works for an emergency rollback; `docker image ls crossstream/xs-server`
lists what is there). Admin commands on the host:
`alias xsc='docker compose -f docker-compose.yml -f docker-compose.host.yml'`, then
`xsc exec server xs-ctl …` as in [Admin](#admin).

### 3. Firewall

Default-deny inbound; allow the tunnel ports from anywhere and SSH only from the
LAN (or trusted addresses). The web panel listens on loopback and needs no rule.
In host mode Docker publishes no ports, so ufw rules apply as written. ufw's own
forward policy is also `DROP`: allow forwarding out of the tunnel interface.

```sh
sudo ufw default deny incoming && sudo ufw default allow outgoing
sudo ufw allow from 192.168.88.0/24 to any port 22 proto tcp   # your LAN
sudo ufw allow 51900 && sudo ufw allow 443          # udp + tcp each
sudo ufw route allow in on xss0                      # hub traffic to the internet
sudo ufw enable
```

Then check from a hub that the internet still works through the tunnel. Router:
forward only 51900 and 443 (udp + tcp) to the host; never SSH or 8443 from the WAN.

### 4. Web panel

Only through an SSH tunnel: `ssh -L 8443:localhost:8443 xs@<host>`, then
`https://localhost:8443` (admin: `xsc exec server xs-ctl admin add <user>`, TOTP
recommended). See [Web panel](#web-panel-phase-15).

### 5. Backups of `/data`

`~/crossstream/data` holds `server.key` (the server identity: lose it and every hub
must enroll again), `state.json` (hubs, tunnel IPs, forwards, panel admins),
`audit.jsonl`, `web.crt`/`web.key` and `healthcheck/`. It contains private keys:
store copies encrypted, off the host.

```sh
sudo tar -czf ~/xs-data-$(date +%F).tar.gz -C ~/crossstream data   # safe while running (atomic writes)
scp xs@<host>:xs-data-*.tar.gz .                              # then delete it on the host
```

Restore: `xsc down`, unpack into `~/crossstream`, `xsc up -d` (or deploy again).
Take a backup before every upgrade.

### 6. Server key rotation

Only when `server.key` may have leaked (host or backup compromised): every hub pins
the server key from its enrollment code, so all hubs must enroll again.

1. Back up `data/`, then `xsc down` and `sudo mv data/server.key data/server.key.old`.
2. `xsc up -d`: xs-server logs `generated server key` with the new public key.
3. For each hub: `xsc exec server xs-ctl revoke <hub>`, `xsc exec server xs-ctl enroll <hub>`;
   on the hub enroll with the new code (GUI or `xs-hubctl enroll`).
4. `sudo rm -rf data/healthcheck` (the next `remote-deploy.sh probe` enrolls it again),
   then delete `server.key.old` and the old backups.

### 7. Host hardening checklist (from `docs/setup-cases.md` §4)

- [ ] SSH: keys only, no root login, `AllowUsers xs`: put `PasswordAuthentication no`,
      `PermitRootLogin no`, `AllowUsers xs` in `/etc/ssh/sshd_config.d/10-crossstream.conf`
      (sshd keeps the first value it reads: the name must sort before `50-cloud-init.conf`),
      `sudo sshd -t && sudo systemctl reload ssh`, then test a new login before closing the
      old one. (Done on the VM 2026-11-18; the VM script already sets keys only.)
- [ ] Firewall as in step 3; router forwards only 51900 and 443.
- [ ] `90-crossstream.conf` in `/etc/sysctl.d`; no `sysctl not applied` in the log.
- [ ] Docker `daemon.json` from step 1; never mount the Docker socket; keep the
      shipped compose hardening (read-only root FS, no core dumps, bounded logs).
- [ ] Security updates: `unattended-upgrades` enabled (Ubuntu Server default).
- [ ] `/data` backed up, encrypted, off the host.
- [ ] Web panel only through the SSH tunnel; panel admins with TOTP.
- [ ] `/metrics` off, or on loopback only.
- [ ] Public IP not on a blocklist (DNSBL, below).

### 8. DNSBL check of the public IP

A blocklisted address breaks sites for every hub. From the host (or check the
address on mxtoolbox.com/blacklists.aspx):

```sh
ip=$(curl -s https://ifconfig.me); rev=$(echo "$ip" | awk -F. '{print $4"."$3"."$2"."$1}')
for bl in zen.spamhaus.org bl.spamcop.net b.barracudacentral.org dnsbl-1.uceprotect.net psbl.surriel.com; do
	a=$(dig +short "$rev.$bl" A | head -n 1); echo "${a:-not listed}  $bl"; done
```

Spamhaus answers `127.0.0.10`/`127.0.0.11` (PBL) for most home connections: that is
a policy list of dynamic addresses that should not send mail directly (port 25 is
blocked for hubs anyway), not a listing for abuse. `127.255.255.x` means the query
was refused (public resolver), not a listing. `127.0.0.2`–`127.0.0.9` (SBL/XBL/CSS)
or any hit on the other lists is a real problem: find the cause before going live.

## Home Assistant

The server also runs as a Home Assistant app (Home Assistant OS or Supervised, amd64 or
aarch64; folder `ha/crossstream_server`, user guide in its `DOCS.md`).

- **From the app repository** (updates through HA's "Update" button): Settings → Apps →
  App store → ⋮ → Repositories → `https://github.com/zyndata/cross-stream` → install
  "CrossStream Server". The image comes from `ghcr.io/zyndata/crossstream-server`.
- **Local bundle** (no registry; every update is a manual re-upload): `make ha-bundle` (or
  the release asset `crossstream-ha-app-<v>.tar.gz`), unpack it into `/addons/` on the HA
  box (Samba or "Terminal & SSH" app: `/addons/crossstream_server/config.yaml` must exist),
  App store → ⋮ → Check for updates → "Local apps" → install. The Supervisor builds the
  image there from the shipped binaries.

Then: options (public endpoints, PEP, egress exceptions, minimum hub version), "Start on
boot" + "Watchdog", start. The panel opens from the sidebar ("CrossStream") through
ingress: no admin users, no login, no certificate — HA authenticates and its user name
lands in the audit log (`ha:<user>`); only HA administrators see the panel. Router: forward
UDP and TCP 51900 to the HA host (443 only after enabling those ports in the app's
Network section). The app uses a bridge network: its nftables table stays inside the
container and the host firewall is never touched. Its `/data` (server key, hubs, audit
log) is in HA backups, so backups are secret. A new install has a new server key: hubs
enrolled to another server enroll again.

Without HA: `make ha-smoke` builds the app image and runs it the way the Supervisor does
(options, ingress source filter, CSRF, enroll, metrics).

## Smoke test

`make server-smoke` (or `sh deploy/server/smoke/run.sh`) builds the image and runs
hub, server and origin containers on internal networks: enroll, connect, ping and
curl through the tunnel, code reuse fails, revoke drops the session. `KEEP=1`
leaves the containers running for debugging.
