# Changelog

All notable changes to this project are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
## [0.11.2] - 2026-11-27

### Added

- **bond:** Count late arrivals after a reorder skip

### Changed

- **bond:** Stream flag for TCP/WSS paths in BBR-lite; RTT-rise restart tried and dropped

### Fixed

- **bond:** STARTUP again for a path whose rate was never measured
- **bond:** No STARTUP restart on TCP/WSS paths
- **hub:** A path on a TCP/WSS fallback tries UDP again
- **transport:** TCP_NOTSENT_LOWAT and a shorter send queue on stream links
- **bond:** A lossy first round ends STARTUP only after a real delivery
- **transport:** Stream connections chosen by send queue
- **transport:** Stream send queue back to 1024; tcploss compares within a run

## [0.11.1] - 2026-11-26

### Fixed

- **bond:** In-flight cap from the data RTT min; xs-hubctl paths diagnostics
- **relay-app:** Unknown scan goes back to the last used hub
- **bond:** Keep the bandwidth estimate through cellular delivery stalls
- **bond:** ACK aggregation allowance while probing (extra_acked)
- **bond:** No ACK aggregation allowance on TCP/WSS paths

## [0.11.0] - 2026-11-26

### Added

- **phase-21:** Hub laptop's own traffic through CrossStream
- **phase-22:** English and Polish in the hub GUI and the server panel
- **relay:** Rename saved hubs on the phone

### Fixed

- **server:** A new code from an enrolled hub renames it
- **hub:** Own traffic keeps NetworkManager's per-device check working
- **server:** A stalled TCP connection no longer drops the download
- Terminal tools English only (captive hint)
- **pairing:** A re-paired relay keeps its id and usage history
- **relay:** Join the chosen hub when it comes into range; "Switch now"
- **hotspot:** Linux reconnects the Wi-Fi network the hotspot took

## [0.10.0] - 2026-11-22

### Added

- **relay:** Pull down on the home screen to refresh the hubs' status

### Fixed

- **hub:** Re-enrollment replaces the server even when its answer is lost
- **webui:** Copy the enrollment code inside the Home Assistant panel

## [0.9.0] - 2026-11-22

### Added

- **phase-00:** Repo bootstrap with build, lint and CI skeleton
- **phase-01:** Wire format, Noise IK crypto and enrollment codes
- **phase-02:** Server MVP with enrollment, single-path tunnel and Docker
- **phase-03:** Relay core, headless hub and Docker e2e env (1 path)
- **phase-04:** Multipath bonding, failover and per-path limits
- **phase-04:** Multipath bonding, failover and per-path limits
- **phase-05:** Hub hotspot, DNS forwarder, IPC events and systemd service [e2e short]
- **phase-05:** Hub Linux system integration (hotspot, DNS, IPC, systemd)
- **phase-06:** DIRECT fallback mode through relay proxies [e2e short]
- **phase-07:** Android relay app with the Go relay core (gomobile) [e2e short]
- **phase-07:** Android relay app verified on a real phone
- **phase-07b:** BBR-lite loss response and clean rate samples for cellular bonding
- **phase-07b:** Download bonding on real cellular links verified
- **phase-08:** Android relay UX, limits, auto-reconnect [e2e short]
- **phase-08:** Data limit switches relaying off, phase done
- **phase-09:** Hub GUI (Wails + Svelte) and IPC for overflow, hotspot power, leave reasons
- **phase-09:** GUI confirm dialogs, debounced hints, plan: auto channel and relay theme
- **phase-09:** Hub GUI done, owner backup test passed
- **phase-10:** Transports, redundancy, aggregation, speed test, min_latency mode
- **phase-10:** Server transports, redundancy, aggregation, speed test
- **phase-11:** Bypass list, usage history, friend priority cap, port forwarding, notifications
- **phase-12:** Local uplinks on the hub, GUI controls xs-hubd
- **phase-13:** Performance enhancing proxy (split TCP)
- **phase-14:** Server hardening, control fuzzing, TPM-sealed hub key
- **phase-14:** Xs-ctl sessions -watch, redacted hub log export, automatic hotspot channel
- **phase-14:** Hardening, observability, automatic hotspot channel
- **phase-15:** Admin service, admin users, web panel backend
- **phase-15:** PATH_NAME frame, relay names on the server
- **phase-15:** Server web panel UI, docker node stage, webui e2e scenario, docs
- **phase-15:** Server web panel
- **phase-16:** Windows 11 hub (Wintun, Mobile Hotspot on the tunnel, service, installer)
- Relay app Refresh button for hubs in range; hub GUI "Statystyki" view
- **phase-17:** Release pipeline, SSH deploy with health check, native Linux server docs
- **phase-17:** Xs-hubctl probe -via, outside enrollment and panel tunnel checked on the VM
- **phase-17:** Test server VM and release pipeline (no release)
- **phase-17b:** Home Assistant ingress listener for the web panel
- **phase-17b:** Home Assistant app folder, local bundle and Docker smoke test
- **phase-17b:** Release builds the HA app image and pushes the public export
- **phase-17b:** Make ha-upload unpacks the bundle into /addons over the HA SSH app
- **phase-17b:** CrossStream server as a Home Assistant app
- **phase-18:** Re-enroll an enrolled hub from the GUI or xs-hubctl
- **phase-18:** Windows 10 hub NATs hotspot clients in xs-hubd
- **phase-18:** Relay app — choose the hub to join, optional location to see hubs in range, help for usage modes
- **phase-18:** Speed test measures a fast path again with a bigger budget, only when needed
- **phase-18:** Hub asks what to do when its Wi-Fi card is connected to a network
- **phase-18:** PEP on by default after three home runs (bonded ~18 vs ~62 Mb/s)
- **phase-18:** Update notifications in the relay app, hub GUI and xs-hubctl
- **phase-18:** Tunnel port setting with restart from the web panel
- **phase-19:** Run-time log level (logs.level IPC, xs-hubctl loglevel, GUI switch)
- **phase-19:** DoH / Private Relay canary names get NXDOMAIN while the bypass list is set
- **phase-19:** Captive portal detection on hub local uplinks; TCP multi-connection spike prototype
- **phase-19:** Windows network change events, IP Helper API instead of netsh, above-normal priority
- **phase-19:** Uplink gone/back log lines, xs-hubctl state dir on Windows, docs for Windows events and IP Helper API
- **phase-19:** Enrollment wizard (phone first, then code), enroll through the computer's own connection, clearer no-answer error
- **phase-19:** Relay joins a returning hub faster (scan results wake the search, short cooldown when the hub was not found); enrolled Windows hub starts the hotspot on the tunnel at once; phase 20 plan
- **phase-19:** Close phase 19 (native Windows networking and diagnostics)
- **phase-20:** Several TCP connections per tcp:// path, chosen by the hub (2, 4 under loss)
- **phase-20:** Several TCP connections per tcp:// path

### Changed

- **phase-18:** Tunnel start/stop and enrollment move out of daemon.go

### Fixed

- **phase-04:** Faster ACKs, latency baseline, stable e2e loss check
- **phase-04:** Failover stall and loss throughput in the bond engine
- **phase-07:** Make gradlew executable; phase 07 handoff [e2e short]
- **phase-07:** Keep the first stop reason and clear network flags after stop
- **phase-07:** Local network permission on Android 17, 1232-byte MTU floor for 464XLAT
- **phase-10:** Woken backup path, auto redundancy size, fallback during server outage, e2e fixes
- **phase-10:** Small packets join an open aggregation batch without a pacing slot
- **phase-10:** Lint in the aggregation path choice
- **phase-11:** Whole radio rows selectable in the relay app; bypass e2e reads the access log only
- **phase-12:** Testenv LAN on 10.43.0.0/24, off the hub hotspot subnet
- **wire:** Reject an empty FWD_ACK refused section
- **phase-13:** Phase 12 app-limited rule in the bond; fallback waits for relay stats
- **phase-14:** Redundant e2e check < 3 % and below off
- Review findings in the web panel, server admin ops and shared-IP warning
- **bond:** Guard reference, stale-packet roaming, PEP window floor and stream edge cases
- **hub:** Handshake path rotation, nft apply ordering, local uplink tick, shutdown order
- **server:** Host services closed to hubs, forward port and handshake races, speed test limits
- **relay:** Re-pair after forget, leaving before close, proxy lifecycle, error loops
- **android,gui:** Limit field saved on confirm, service resume, settings race, control revert
- Whole-project code review findings (#1) [e2e short]
- **relay:** Test relays bind to 127.0.0.1 (no Windows firewall prompt per test binary) [e2e short]
- **hub:** Windows hotspot address never link-local; GUI error hints for Windows
- Test VM script tolerates the ejected ISO drive, -Finish resumes after install
- **phase-17:** Deploy.yml apk job checks apksigner's exit code, not its output format
- **phase-18:** Bond estimate follows a path whose capacity fell (S23 path collapse)
- **phase-18:** Path loss figure decays when idle and is capped at 100 %
- **phase-18:** Xs-ctl enroll -h prints help; hub names may not start with '-'
- **phase-18:** Windows hub keeps the PC online while no path works
- **phase-18:** HA app advertises 443 endpoints only with the port_443 option
- **phase-18:** Lint — rename lossRoundMin
- **gui:** Render the Linux webview without GPU compositing
- **phase-18:** GUI turns WebKitGTK's DMA-BUF renderer off on Linux [e2e short]
- **phase-18:** App icons from one master — Android launcher icon, Windows GUI icon, sharp PNGs [e2e short]
- **phase-18:** GUI confirm dialogs open above the table, not inside a dimmed row [e2e short]
- **phase-18:** Relay joins its hub when Android hides Wi-Fi scan results; GUI QR enlarges on click [e2e short]
- **phase-18:** Relay app — large on/off switch, no false 'away' when the phone cannot scan, 'connecting' state
- **phase-18:** GUI says 'starting' while it starts xs-hubd, keeps the commands until it runs, reports a stopped hub
- **phase-18:** Relay app — online hubs first with a radio to pick the active one, offline last; live range from cached scans
- **phase-18:** Relay app — a hub switched off leaves 'in range' at the next scan; refused refresh is reported
- **phase-18:** Speed test measures fast paths instead of reporting its own filler rate
- **phase-18:** Hub waits for the upload test result until the test's planned end
- **phase-18:** Linux hotspot asks for an 80 MHz channel on 5 GHz (VHT cards), falls back to the automatic width
- **phase-18:** Detect VHT in iw phy info (its line ends like a section header)
- **phase-18:** No 80 MHz warning when 5 GHz fails altogether
- **phase-18:** Linux hotspot uses 40 MHz on 5 GHz (80 MHz fails on Intel AX201 after ~25 s)
- **phase-18:** Relay waits for Wi-Fi instead of pausing the hub; background scan without results counts as unknown
- **phase-18:** Bond takes cwnd-full samples only while a queue builds (secondary paths stayed in use)
- **phase-18:** Bond app-limited period covers the packet just sent again (secondary paths carried traffic)
- **phase-18:** Virtual AP interface gets the hotspot's own MAC; removed when NetworkManager cannot take it
- **phase-18:** Offer the Wi-Fi share only on cards that allow two clients
- **phase-18:** Wi-Fi choice log names only the choices the card allows
- **phase-18:** Hub stops asking about a Wi-Fi network the laptop has left
- **phase-18:** Field only.sh no longer stops when the last relay is not the chosen one
- **phase-18:** Speed test notification shows the upload result (said 0 Mb/s for an upload test)
- **phase-18:** Hub repeats a changed PATH_CONFIG so a lost datagram cannot unblock a backup-capped relay
- **phase-18:** Hub handshakes again at once after waking from sleep
- **phase-18:** Update edge cases on the hub (stall, lock, copy, pkexec)
- **phase-18:** Relay app update edge cases (reuse, running, stalled, portal)
- **phase-19:** Windows hotspot clients get DNS (xs0 DNS servers via SetInterfaceDnsSettings), weak Wi-Fi hint below -75 dBm, README enrollment steps
- **phase-19:** Windows hotspot restarts once when it started sharing while the tunnel was offline, so clients get DNS
- **phase-19:** Hotspot DNS restart only while a local path keeps the tunnel online, in the background; relay-only DNS gap recorded
- **phase-19:** Restart Internet Connection Sharing before the hotspot shares the tunnel so clients always get DNS; drop the restart-once logic
- Windows hub starts in ~10 s and starts again right after a stop

### Security

- README as the public user guide; every release updates it before the tag


