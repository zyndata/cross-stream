# CrossStream Server

The server end of CrossStream: hubs connect to it over the tunnel ports and
their traffic leaves to the internet from here.

## 1. Router: forward the tunnel port

Forward **UDP 51900** (and **TCP 51900** for networks that block UDP) from your
router's WAN to the Home Assistant host (another port: see [Tunnel port](#tunnel-port)).
Optional: 443 UDP/TCP if nothing else uses 443 on that host (map the ports in the app's Network section, then turn on the
`port_443` option: only then do enrollment codes list the 443 fallbacks).

Behind carrier-grade NAT (your router's WAN address differs from what
`https://ifconfig.me` shows, or it is in 100.64.0.0/10) port forwarding cannot work.

Test from outside your home network (phone on mobile data, not home Wi-Fi).

## 2. Options

- **Public endpoints**: your public IP or DNS name (a dynamic DNS name if the IP
  changes), e.g. `vpn.example.org`. Hubs use what is written into the enrollment code.
- **Performance Enhancing Proxy**: keep on unless a problem points at it.
- **Allowed private networks**: by default hubs reach the internet only, never your
  home network. List prefixes here only when a hub must reach a home device.
- **Minimum hub version**: refuse older hubs after a protocol change.

## 3. Start

Turn on **Start on boot** and **Watchdog**, then start the app. The log should show
`listening`. Open **CrossStream** in the sidebar: the panel needs no extra login
(Home Assistant administrators only).

## 4. Enroll a hub

Panel (Polish UI) → **Huby** → **Dodaj hub**, type a name, copy the one-time code. On the hub:
`xs-hubctl enroll <code>` (or paste it in the hub app). The hub appears online with
its paths within seconds.

## Tunnel port

Hubs connect to port **51900** by default. The port lives in one place only: the app's
**Network** section (`51900/udp` and `51900/tcp`, keep both the same). Change it there, or in
the panel (**Serwer** → **Port tunelu** → **Zmień port**), which writes the same Network
mapping. Either way:

- It takes effect after a restart. The panel shows **Wymagany restart** (restart required) on
  every page with a button that restarts the app; the HA Network section asks by itself.
- Forward the new port (UDP and TCP) on your router to the Home Assistant host.
- Best chosen before the first hub enrolls. **Hubs keep the port from their enrollment
  code**: after a change each one enrolled before must enroll again. The panel's **Huby**
  list marks them "zarejestruj ponownie" (the log names them too). For each: **Usuń**
  (remove), **Dodaj hub** with the same name, paste the new code in the hub. Relay phones
  need nothing.

## Backups

Home Assistant backups include this app's data: the server key (identity), enrolled
hubs and the audit log. Treat backups as secret. Restoring a backup restores the
identity, so hubs keep working; losing it means every hub must enroll again.

## Metrics

Enable port 9100/tcp in the Network section to let a Prometheus scraper read
`http://<home-assistant>:9100/metrics` from your LAN. Never forward it on the router.

## Troubleshooting

- `ip_forward is 0` in the log: the host does not forward IPv4; on Home Assistant OS
  this should not happen, report it.
- Hub cannot connect: check the port forward with the hub's `xs-hubctl probe` from a
  phone's mobile data, and that **Public endpoints** names your current public address.
