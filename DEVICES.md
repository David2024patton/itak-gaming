# Devices and Machines

Access procedures live in two skills (in lulu's custom skills):
**itak-tv-access** (David's TV) and **itak-stick-access** (the
Missouri stick). This file is the inventory; the skills are the
how-to.

## David's Philips TV (the home bench)

- Philips 4K TV, model "Philips 4K A1", device queenstown, Funai,
  MediaTek m7332 board. Android 11, armeabi-v7a only.
- Quad Cortex-A53 at 1.55 GHz, Mali-G52 GPU, about 1.7 GB RAM.
  Roughly 10% different muscle from the stick: good early-warning
  bench, NOT the stopwatch for stick verdicts.
- On the home LAN at 192.168.0.122:5555 (DHCP, may change).
  Reached by adb from ubuntu-server.
- App state: 1.81 installed 2026-10-24 (silent install). Game
  library copied from the stick 2026-10-23: NES 5, SNES 8,
  Genesis 7, Game Boy 2, GBA 1, PS1 2 (the 240p Test Suite).
- Debug chute key 2797855e36f2, label "David home TV".
- Quirks: screenrecord is pure black (use screencaps); screencap
  fails transiently now and then (retry once); stay-awake is OFF
  (0) on purpose; TV volume was set to 6 of 100 at David's word
  on 2026-10-24 and stays there until he says otherwise.

## The Missouri stick: "Death Star" (Mary and Terry's)

- Chromecast with Google TV (HD), 2022, device "boreal".
  Amlogic S805X2, quad Cortex-A35 at 1.8 GHz, Mali-G31 MP2,
  about 1.4 GB RAM, Android 14, armeabi-v7a.
- Lives in Missouri on Mary and Terry's TV. Tailscale
  100.113.6.86; adb is driven from ubuntu-server.
- App state: **1.81** (updated itself from the feed; confirmed by
  the chute 2026-10-25). Storage is tight: 452 MB free.
- Debug chute key ca4ee57efe88, label "Mary". Pings every
  15 minutes; reports land in ~/itak-debug/ on the server.
- Stay-awake is ON (7) deliberately, set 2026-10-23. Never reboot
  this stick: rebooting kills wireless debugging.
- Latest play evidence: 2026-10-24 evening, SNES, ten 600-frame
  windows (heavy 16.9-17.8 ms, settling 12.2-12.6 ms).
- Game library: NES 5 (Zelda USA is Terry's game), SNES 8,
  Genesis 7, Game Boy 2, GBA 1 (Anguna), PS1 2 (240p suite),
  Sega CD 0.
- Saves live in the app's private storage and are NOT readable
  over adb on a release build. The in-app backup-and-hold is
  their protection.

## ubuntu-server (the shop)

- Tailscale 100.69.46.95, user `server`. SSH:
  `ssh -F /home/hatch/.ssh/ts/config ts-server`.
- **NEVER REBOOT. It serves live business websites.**
- Hosts: the build pipeline (~/itak-repo clone, ~/itak-gaming
  build tree, ~/itak-autobuild.sh, ~5 minute cycle), the public
  update feed and lobby (~/itak-netplay/), the debug chute store
  (~/itak-debug/), the Rust core sources (~/itak-rust-*), the
  staged core libraries (~/itak-rust-libs/armeabi-v7a/), the
  evidence archive (~/itak-speed/), and the local test ROMs
  (~/roms/, ~/gen-test/, ~/itak-dev/homebrew/).
- GPU: AMD Radeon RX 7900 XTX (the CUDA stack does not run on
  it as-is; irrelevant to this project, noted so nobody plans
  CUDA work here).

## David's Windows PC

- Hostname N30002-T, Tailscale 100.124.11.80, user David.
  SSH alias ts-pc (same config file). Windows 11 Pro, Visual
  Studio and Git, no Flutter SDK.
- ROM staging folders: C:\ROMs\NES, SNES, Genesis, Dreamcast.
- Site of the 2026-10-13 machine-to-machine netplay proof
  (SMW USA, 2,400 frames, zero desyncs).

## Guest rigs (function checks only)

- **AVD itak-stick-tv34** on ubuntu-server: 32-bit Android TV
  image with ARM translation. Runs the app; proves function,
  never speed. Runbook: the itak-stick-emulator-check skill.
- **Cuttlefish** on ubuntu-server: Android 17 guest, boots in
  20 s, adb at 127.0.0.1:6520. Cannot run the 32-bit line (no
  32-bit ARM in its abilist). Reserved for the 64-bit (2.0)
  checks. Runbook: ~/workspace/itak-cuttlefish/RUNBOOK.md.
  Remember the apparmor runtime flag before launches.

## Drawer devices (tried or considered, all closed)

- TiVo Stream 4K: boot loop, closed by David 2026-10-23.
- Fire TV Stick 4K (2023) and Fire TV Stick gen 2 (2016): run
  the app natively but flatter performance; the 2016 stick is
  below minSdk. Spares only.
- Chromecast 2nd gen (round puck): cannot run apps. Not a bench.
- CTL Chromebox CBx2: x86, same ABI wall as Cuttlefish for this
  app; a failover design for David's websites exists and is
  parked awaiting his decision (not an emulator topic).
