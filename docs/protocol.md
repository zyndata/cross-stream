# Protocol

All integers big-endian. Ports are defaults (configurable).

## 1. Hub ↔ Server (UDP 51900 and 443, through relays)

### Outer packet
```
+------+------------+---------+-----------+-------------------------+
| type | session_id | path_id | counter   | payload                 |
| u8   | u32        | u8      | u64       | (AEAD ciphertext + tag) |
+------+------------+---------+-----------+-------------------------+
header = 14 bytes, used as AEAD associated data
```
| type | Meaning | payload |
|---|---|---|
| 0x01 | HANDSHAKE_INIT (hub→srv) | Noise IK msg 1; session_id=0 |
| 0x02 | HANDSHAKE_RESP (srv→hub) | Noise IK msg 2; carries new session_id |
| 0x03 | DATA | ChaCha20-Poly1305(frames) |

- Noise pattern: `Noise_IK_25519_ChaChaPoly_BLAKE2s` (library: `github.com/flynn/noise`).
  Hub = initiator, knows server static key from the enrollment code.
- After handshake: two keys (hub→srv, srv→hub). Nonce = `counter` (per direction, starts at 0,
  never reused). Rekey (new handshake) every 10 min or 2^32 packets, old keys kept 30 s.
- Replay window: 8192-entry sliding bitmap (multipath causes large reordering).
- A handshake can be sent over any path; the response goes back on the same path.
- Server accepts a new path (new path_id or new source addr) after a valid AEAD packet.

### Handshake payloads (inside Noise, JSON)
- INIT: `{"v":1,"sw":"<hub version>","hub_name":"...","enroll_token":"<base64>|null","paths":[ids],"ts":N}`
  (server rejects `sw` below its `min_hub_version`, logged; no response to the hub).
  `ts` (phase 14, required): send time in Unix ns, strictly increasing per hub; the server
  drops an INIT whose `ts` is not newer than the last accepted one from that hub (replay).
- Server limits (phase 14): 2 INIT/s per source IP (burst 10, checked before the DH),
  64 paths per session, 64 TCP/WSS connections per source IP (32 before phase 20), first packet on a stream
  connection within 10 s.
- RESP: `{"v":1,"session_id":N,"tunnel_ip":"100.64.0.N","mtu":1360,"pep":true}` (`pep` from phase 13)
- Unknown hub key and no valid enroll token → silently dropped (no response).

### Frames (inside DATA, concatenated TLV: `type u8, len u16, value`)
| Frame | Value |
|---|---|
| 0x10 IP | `seq u32` + IPv4 packet |
| 0x11 ACK | `largest u32` + `bitmap u64` (bit i = largest−1−i received); one frame per 64-seq window, so `largest` is the window top, not always the highest seq |
| 0x12 PING | `ts u64` (sender monotonic µs) + optional zero padding (path MTU probe) |
| 0x13 PONG | echoed `ts u64` + `probe_len u16` (size of the PING datagram received; no padding) |
| 0x14 PATH_CONFIG | `path_id u8, priority u8 (0 normal, 1 backup, 2 secondary), rate_kbps u32 (0=∞), cap_mb u32 (0=∞), dir_up u8, dir_down u8 (0 on, 1 backup_only, 2 off)` (hub→srv) |
| 0x15 PATH_GONE | `path_id u8` (hub→srv, relay left) |
| 0x16 CLOSE | reason u8 (0 normal, 1 revoked, 2 server shutdown) |
| 0x17 SESSION_CONFIG | `redundancy u8` (0 off, 1 auto, 2 on), `overflow_mbps u16` (0 = off; secondary paths unused while normal paths send ≥ this), `mode u8` (0 max_throughput, 1 min_latency) (hub→srv) |
| 0x18 TEST_REQ | `test_id u16, path_id u8 (0xFF = all), dir u8 (0 down, 1 up), duration_ms u16, max_mb u16` (hub→srv) |
| 0x19 TEST_DATA | `test_id u16` + filler (receiver counts and drops) |
| 0x1A TEST_RESULT | `test_id u16, path_id u8, bytes u64, duration_us u32` (receiver→sender, one per path) |

Speed test: the hub sends TEST_REQ twice (the server ignores a known `test_id`). Down: the
server sends filler on the path(s) at `max_mb / duration` per path; the hub counts. Up: the hub
sends, the server counts and answers TEST_RESULT per path (twice) `duration + 1 s` after the
request. `bytes` = received outer bytes, `duration_us` = first to last packet. Both ends keep
the tested paths ALIVE for the test (the filler fills the path's queue on purpose).
| 0x1D PATH_INFO | `path_id u8, observed_ipv4 u32, observed_port u16` (srv→hub, on path ALIVE and on address change) |
| 0x1E PATH_NAME | `path_id u8` + name (UTF-8, printable, 0–32 bytes; the relay or local link name for the server web panel) (hub→srv, with the first PATH_CONFIG, on change and every 10 s with the full config) |
| 0x1B FWD_REQ | `count u8` + `count × (proto u8 (6 TCP, 17 UDP), port u16)` — full desired list (hub→srv) |
| 0x1C FWD_ACK | same layout — accepted subset (srv→hub), then, only when there are any, `count u8` (≥ 1) + `count × (proto u8, port u16, reason u8)` refused entries (1 over allowance, 2 restricted port, 3 outside 1024–49151, 4 used by another hub). The hub repeats FWD_REQ every 5 s until an ACK covers its list; the server answers every FWD_REQ |

| 0x20 STREAM_OPEN | `stream_id u32, dst_ipv4 u32, dst_port u16` (hub→srv, PEP) |
| 0x21 STREAM_OPEN_RESULT | `stream_id u32, code u8` (0 ok, 1 refused, 2 timeout, 3 unreachable) (srv→hub) |
| 0x22 STREAM_DATA | `stream_id u32, offset u64` + bytes (both directions) |
| 0x23 STREAM_WINDOW | `stream_id u32, max_offset u64` (receiver may accept bytes up to here) |
| 0x24 STREAM_CLOSE | `stream_id u32, final_offset u64` (half-close, like FIN) |
| 0x25 STREAM_RESET | `stream_id u32, code u8` (abort, like RST: 0 abort, 1 unknown stream, 2 flow control, 3 idle, 4 shutdown, 5 limit) |

Frame 0x1D is added in phase 04, 0x17–0x1C in phases 10–11, 0x1E in phase 15, 0x20–0x25 in phase 13; unknown frame types
are ignored (forward compatibility). Stream frames (0x20–0x25) are reliable: the
sender keeps them until ACKed and re-sends on loss; the reorder buffer delivers them
immediately (per-stream ordering by offset happens in the PEP layer).

Stream frames travel inside the IP frame (0x10) as a "reliable payload": `seq u32` +
one or more stream frame TLVs (first byte 0x20–0x2F, which no IPv4 packet starts with).
They share the IP seq space, ACKs and loss detection; the sender re-sends them without
an attempt or age limit, the receiver hands them over at once (also after their gap
was skipped). Repeated frames are harmless: data is placed by offset, WINDOW only
raises, CLOSE/RESET are idempotent, a repeated OPEN for a live or recently removed id
is ignored.

PEP stream rules: the hub opens streams (ids from a random start, never 0); every
stream starts with a 0-byte window in both directions — OPEN goes together with the
hub's first WINDOW, OPEN_RESULT ok together with the server's. A receiver re-grants
`read offset + 4 MB` once half its window is used, within a 256 MB cap per session.
Data beyond the granted window or the final offset resets the stream (code 2). A frame
for an unknown id is answered with STREAM_RESET code 1 (never a RESET); removed ids are
remembered 2 minutes. At most 4096 streams per hub (OPEN_RESULT refused), idle streams
end after 2 h (code 3).

MTU: tunnel MTU 1360 by default; lowered to fit the smallest probed path MTU
(architecture.md "Path MTU"); xs-hubd clamps TCP MSS to tunnel MTU − 40.

A DATA payload may carry several frames (e.g. ACK + PING + several small IP frames);
senders may batch small IP packets for ≤ 1 ms up to the path MTU (aggregation).

## 2. Server enrollment (paste code)

`xs-ctl enroll <name>` prints:
```
XS1.<base64url(JSON)>
JSON: {"v":1,"ep":["udp://host:51900","udp://host:443","tcp://host:51900","wss://host:443/xs"],"spk":"<server static pubkey b64>","tok":"<16 B b64>","name":"<name>"}
```
- Token single-use, TTL 24 h (configurable), stored hashed in `state.json`.
- Hub generates its own X25519 static keypair, sends INIT with `enroll_token`.
  Server verifies token, stores `{name, hub_pubkey, tunnel_ip}`, deletes token.
- `xs-ctl revoke <name>`: deletes key, closes session immediately.

## 3. Hub ↔ Relay

### Pairing QR (shown in hub GUI)
```
XSR1.<base64url(JSON)>
JSON: {"v":1,"hub":"<hub name>","ssid":"...","bssid":"aa:bb:..","psk":"...",
       "ip":"10.42.0.1","cport":51901,"dport":51902,
       "hpk":"<hub relay-channel static pubkey b64>","tok":"<16 B b64, TTL 10 min>"}
```
QR is regenerated every 10 min and after each successful pairing.

### Control channel (TCP 51901)
- Noise IK, relay = initiator (knows `hpk`). Relay has its own static key (stored on phone).
- Messages: 4-byte length + Noise transport message containing JSON `{"t":"<type>",...}`.

| t | Dir | Fields |
|---|---|---|
| hello | R→H | `relay_name, carrier, pair_token (first time only), limits{max_priority (normal/secondary/backup), cap_mb, cap_period (session/day/month), reset_day, used_mb (in current period), over_cap (stop/throttle), throttle_kbps, battery_min}` |
| welcome | H→R | `relay_id, path_id, server_ep, data_port`, `tcp_conns` (phase 20, optional: TCP connections for a `tcp://` server_ep, 0 = 1, max 8) |
| reject | H→R | `reason` (bad token, forgotten, hub full) |
| stats | R→H every 1 s | `bytes_up, bytes_down, battery, signal_dbm, net_type (lte/nr/...), cellular_ok (VALIDATED), wifi_rssi`; phase 20, optional: `tcp_conns` (live TCP connections), `tcp_retrans`, `tcp_sent` (cumulative segments over the connection set) |
| leaving | R→H | `reason` (limit, battery, user, no_cellular, no_data = not validated/captive) |
| endpoint | H→R | `server_ep` URI (switch this path to another server endpoint / transport), `tcp_conns` (phase 20, optional). The endpoint in use with another `tcp_conns` only resizes the relay's connection set; anything else (including the same endpoint again) redials |

Endpoint choice: the hub orders the code's `ep` by transport (UDP, TCP, WSS; code order
within one); a path starts on the first; if it does not work for 5 s while another path is
ALIVE (15 s while none is and it is the only relay), the hub sends `endpoint` with the next one (then wrap). A relay
that reconnects starts on the endpoint its path last worked on.
TCP/WSS framing between relay and server: `len u16 | outer packet` (WSS: one packet per
binary message).
| mode | H→R | `mode: bonded/direct`, `proxy: bool` (true in DIRECT or when the bypass list is non-empty), `proxy_token` (32 B b64, when proxy) |
| limits | R→H | same object as `hello.limits`; live update without reconnect |
| forget | H→R | relay must delete this hub |

A `tcp://` path may use several TCP connections to the same endpoint (phase 20, no wire
change): each carries the same framing, packets go out round-robin, the server answers on
the connection of the newest packet from that path (a new source port from the same IP on a
stream path is not an address change: no PATH_INFO), a failed connection is redialed while
the others carry the path. The hub picks the number: 2, 4 while the TCP retransmission ratio
over 10 s is ≥ 3 % (back to 2 after 30 s under 1 %).

Control loss for 5 s → hub marks relay DEAD for both modes.

### Data channel (UDP 51902 on hub, relay uses one Wi-Fi-bound and one cellular-bound socket)
- Hub → relay: `[0xB1][path_id u8][outer packet]`. Relay strips 2 bytes, sends outer
  packet over cellular to `server_ep`.
- Relay → hub: `[0xB2][path_id u8][outer packet]` for each datagram received from `server_ep`.
- Relay drops anything not from the hub address of its control session or not from `server_ep`.
- Relay sends a 2-byte keepalive `[0xB3][path_id]` to hub every 5 s.

### Proxy role (relay TCP 51903, enabled when `mode.proxy` is true: DIRECT mode or bypass)
- Hub opens one TCP connection per client TCP flow:
  `session_token (32 B, from control 'mode' message) | atyp u8 (1=IPv4) | addr | port u16`,
  relay replies `0x00` ok / error code, then raw bidirectional stream (relay dials over cellular).
  Error codes: 01 failure, 02 not allowed (token/destination), 04 unreachable, 05 refused,
  08 address type, 09 cap reached. Only connections from the hub's control IP are served.
- The hub keeps `proxy: true` (mode `bonded`) while it drains direct flows after DIRECT,
  and sends a new token for each DIRECT episode.
- UDP flows (incl. DNS): datagrams on the data channel with prefix
  `[0xB4][flow_id u32][ipv4 u32][port u16][payload]`; replies `[0xB5][flow_id u32][payload]`.
  Relay closes idle UDP flows after 60 s.
