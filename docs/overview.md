# Overview

## Problem
In areas with weak mobile coverage a single phone gives ~1 MB/s (~8 Mb/s).
Downloading e.g. 5 GB takes ~85 min. Several phones (friends, different
carriers) together can give a multiple of that, if their links are bonded.

## Product
A self-hosted equivalent of a cellular-bonding service, with three parts:

| Part | Runs on | Role |
|---|---|---|
| Relay app | Friends' Android phones | Forward encrypted packets over the phone's cellular link |
| Hub | Linux laptop (v1), Windows laptop (later) | Wi-Fi hotspot + bonding client + fallback load balancer |
| Server | Docker on a Linux server | Bonding endpoint: reassembles traffic, NATs to the internet |

Any laptop (e.g. a client's machine) joins the hub's Wi-Fi and gets internet.
Nothing is installed or configured on that laptop.

## Requirements (stated by the owner)
- Works anywhere, any time (e.g. night, remote location), no manual steps on the server at join time.
- Friends install an APK (sideloaded, not Google Play) and join by scanning a QR code shown by the hub.
- Relay app UX modelled on MikroTik Back To Home: one big on/off toggle, list of hubs, setup by QR.
- Relay app remembers paired hubs and reconnects automatically; supports several hubs.
- Friend can set a data limit (e.g. 2 GB) or "unlimited"; relaying stops at the limit.
- Friend can set a battery threshold (default 20 %) below which relaying stops.
- Live counter of shared data; persistent notification with a Stop action.
- Hub is a GUI application (Linux first, Windows second variant).
- Hub GUI shows: QR code, connected phones with per-phone speed, total throughput,
  current mode (bonded via server / direct fallback / offline).
- Per-link priority: normal or backup-only. Per-link speed limit and data limit.
- Auto-reconnect of paired devices. Fast failover when a phone drops
  (lost in-flight packets are resent over other phones; transfer must not stall).
- End-to-end encryption hub↔server; relays (friends) cannot read traffic.
- Serverless fallback mode in v1: if the server is unreachable, per-connection
  load balancing across phones.
- Server: Docker, managed from the terminal. Only enrolled hubs (per-device key) may
  connect; keys are revocable. Enrolling a hub = server prints a paste-in code (no QR).
- Expected hubs: owner's Linux laptop, owner's Windows laptop, a company laptop.
  Client laptops only join a hub's Wi-Fi and are never registered on the server.
- Tests: server on Docker Desktop, from phase 17 on a Linux VM. Production: the Home
  Assistant app (phase 17b) on a small home server behind a NAT router
  (bonded download is capped by the home upload).
- Additional features (owner, 2026-11-10, common in bonding products — see `docs/prior-art-notes.md`):
  bypass list (domains out via a phone's carrier IP), redundant mode for calls,
  built-in speed test, per-friend usage history, friend-side priority cap
  ("use me only when needed / as backup"), TCP/WebSocket transport fallback,
  port forwarding on the server's public IP, desktop notifications in the hub GUI,
  local uplinks on the hub (USB-tethered phones without the app, Ethernet, modem),
  Performance Enhancing Proxy (split TCP).
- Server is also managed from a web panel (owner, 2026-11-10): same operations as `xs-ctl`,
  login required, bound to localhost by default (reach it via SSH tunnel).
- Code in a private GitHub repository.
- Server hosting (owner, 2026-11-16): besides plain Docker, the server must be installable
  as a Home Assistant app (former "add-on") from an app repository — phase 17b; later also
  as an OpenWrt package (backlog, after v1).
- Primary use (owner, 2026-11-11): a stable link for online multiplayer VR simulations at a
  client site, rendered locally (only game state crosses the link: a few Mb/s, mostly small
  UDP packets). Latency, jitter and no gaps on a phone drop matter more than throughput.
  If both cannot be had at once, the hub has a mode switch: **max throughput** (bonding,
  reorder buffer, fast re-send — phase 04) or **min latency** (redundancy, no long holds of
  real-time packets — phase 10).

## Fixed decisions
- Language: Go for server, hub core and the relay core (shared module).
  Android app: Kotlin (Android Studio) shell + Go relay core compiled with gomobile (AAR).
- Hub GUI: Wails v2 (Go + Svelte frontend) talking to a privileged hub daemon over local IPC.
- Crypto: Noise protocol (`Noise_IK_25519_ChaChaPoly_BLAKE2s`) + ChaCha20-Poly1305 data packets.
- Transport: UDP for data, TCP for relay control channel.
- Tunnel carries IPv4 only in v1. IPv6 is disabled on the hotspot to avoid leaks.
- No video/streaming priority mode (explicitly dropped). Redundant mode (phase 10) is
  a manual/loss-driven duplication switch, not traffic classification.
- iOS relay app: deferred (later phase, not in v1). The gomobile core keeps the door open.

## Why relays and not USB/Wi-Fi tethering
Traffic leaves the phone from the relay app's own sockets (TTL 64), so Android does not
account it as hotspot data and TTL-based tethering detection does not trigger. Other bonding
products need a "tethered TTL" setting for this. Carrier terms may still classify relaying as
tethering (commercial bonding products say data shared from phones counts as tethering) — tell friends.
Practical deployment notes: `docs/setup-cases.md`.

## Out of scope for v1
iOS app, Google Play distribution, multi-tenant server (accounts/billing),
IPv6 in the tunnel, QoS/traffic classes.

## Key risks (validated early, see phases)
| Risk | Validated in |
|---|---|
| Android relay keeps cellular sending while joined to a no-internet local Wi-Fi | Phase 07 (first real-device task) |
| Wi-Fi reconnect without repeated approval dialog (needs stable SSID+BSSID) | Phase 07 |
| Background killing on Xiaomi/Samsung/Huawei | Phase 08 |
| Same cell tower → less than linear gain | Phase 18 field test |
| Docker Desktop (WSL2) kernel lacks `sch_netem` for the test env | Phase 03 (fallback: run test env on the Linux laptop) |
| Windows hotspot sharing a Wintun-based connection | Phase 16 spike (go/no-go) |
| Carrier blocks/throttles UDP on port 51900 | Phase 07 (fallback: UDP 443, then TCP/WSS in phase 10) |
| Small cellular MTU (464XLAT, IPv6-only carriers) | Phase 04 PMTU probe, checked in phase 07 |
| Prepaid friend runs out of data (cellular up, no internet) | Phase 08 (`VALIDATED`/captive check) |

## Naming
Project: **CrossStream** (Ghostbusters reference: "don't cross the streams" — here crossing
the streams of several phones is the point).

| Component | Code / binary | UI / marketing name |
|---|---|---|
| Server | `xs-server`, CLI `xs-ctl` | Containment |
| Hub daemon + GUI | `xs-hubd`, `xs-hubctl`, `gui/` | ProtonPack |
| Android relay app | `android/`, core `xscore.aar` | Neutrona |
| Test relay | `xs-relaysim` | — |

Code, logs, file paths and protocol identifiers (`XS1.`, `XSR1.`, TUN `xs0`/`xss0`)
use descriptive names only; themed names appear in UI strings and README.
