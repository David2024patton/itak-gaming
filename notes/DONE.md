# Done, with proof

Newest first within each area. Dates are 2026 unless noted.

## Shipped builds (32-bit line, feed: arcade.itak.live/app/version.json)

- **1.79** (Oct 24, code 80) — SNES controller byte swap fix, libraries
  only. The whole story is under "SNES controller" below. APK sha256
  0363ce6d..., dissection vs 1.78: only the manifest version and both
  libitak_snes.so copies differ.
- **1.78** (Oct 24, code 79) — dark theme for the in-game menus and popups
  (ItakDialogTheme on all 13 dialog sites). Proven on David's TV: dark
  pause menu and exit confirm over Zelda.
- **1.77** (Oct 24, code 78) — white flash on app open and close fixed
  (black launch and normal themes, black launch background), intro video
  now fades out (750 ms natural end, 250 ms other exits). The fade was
  caught mid-flight in a TV screenshot.
- **1.76** (Oct 24, code 77) — NES console tile re-cut with transparency;
  the white box around it on the home screen is gone. Asset only.
- **1.75** (Oct 23, code 76) — the NES full-speed core (NES2, below).
- **1.74** (Oct 23, code 75) — intro plays once per process (verdict moved
  to native memory; the Dart static was wiped by engine recreation).
- **1.73** (Oct 23, code 74) — the frozen core bundle fix (below).
- **1.72** (Oct 23, code 73) — PS1 picture fix (below), SNES page cache,
  PS1 adapter release profile parity.
- **1.71** (Oct 23, code 72) — David's intro video with the IntroGate
  (skip on any key, watchdogs, cannot trap the app).
- **1.70** (Oct 23, code 71) — libraries only speed update: GBA halt fix,
  NES deferred rendering, SNES round 1, Genesis micro wins, SNES adapter
  release profile parity.
- **1.69** (Oct 23, code 70) — the debug chute (below).
- **1.68** (Oct 23, code 69) — space background on the home shell with a
  Settings toggle, default on.
- **1.67** (Oct 22, code 68) — console photos for the 13 system tiles,
  box art auto-fetch with a Settings toggle.
- **1.66** (Oct 22, code 67) — the Rust switch: all six 32-bit systems run
  the from-scratch Rust cores behind the Go bridge seam, plus the save
  backup-and-hold (first Rust session backs up the whole saves tree and
  holds auto-save; proven on the Missouri stick Oct 23).

## SNES controller byte swap (the 1.79 fix)

- Defect: `sample_joypads` stored the two auto-read bytes swapped. The core
  put the B Y Select Start + D-pad byte in $4219 and the A X L R byte in
  $4218; hardware is the opposite ($4218 = A X L R, $4219 = B Y Select
  Start + D-pad). Within-byte bit order was always right.
- Effect: in standard auto-read games the D-pad and Start were dead, and
  the face buttons crossed (A acted as B, Start as R, and so on). Proven
  per button by save-state byte reads, and a single R press reproduced
  the A press's exact frame CRC on the Zelda title.
- Origin: inherited verbatim from the Go reference core; the Rust and Go
  unit tests both asserted the wrong layout, which is why the gates were
  green on a broken controller.
- Evidence that the fix is exactly the swap and nothing else: feeding the
  fixed core the old byte stream reproduces all 7 reference frames and
  both Go state blobs byte for byte (attribution proof, reports in
  ~/itak-speed/snes-input-repro/). Byte-truth table after the fix: 12/12
  buttons match hardware; one Start press now enters Zelda file select
  with the exact CRC the A press used to produce.
- Gates after regenerating references from the fixed Go bridge: frames
  7/7, state 16/16, adapter crossproof 16/16, conformance 49/49, cargo
  dev and release 111/111, corpora green. Old artifacts preserved at
  ~/itak-speed/prev-gates-178/, old library at
  ~/itak-speed/prev-libs/libitak_snes-178.so.
- GitHub: cores/go-snes/system/system.go commit eb2a69f,
  machine_test.go commit 2c0af02 (pushed after the 1.79 feed verified).
- On device: installed on David's Philips TV Oct 24; log line 01:09:08
  shows the fixed library refreshed in; a Start press reached the PLAYER
  SELECT screen, photographed. Note: quick synthetic remote taps from a
  computer are sometimes too short for the app's key polling; a normal
  controller press is fine.

## Frozen core bundle (fixed in 1.73)

- Root cause: the app extracted a bundled core library to its private
  folder only if the file was missing, so updated installs kept running
  the first-extracted cores forever. Speed rounds and the PS1 fix never
  reached updated devices until 1.73 made extraction hash-checked on
  every launch (extract to a refresh file, verify, rename over).
- Lesson baked in: every on-stick measurement between 1.66 and 1.73 was
  the 1.66 cores talking, regardless of the app version shown.

## PS1 picture (fixed in 1.72)

- GPU rectangle rendering promoted zero width or height to maximum; the
  guest's per-frame black rect at (0,0) sized (640,0) painted black over
  every page. The size-0-to-max rule applies only to memory-transfer
  commands (psx-spx). The two promotions were deleted; gates 205/205 dev
  and release, conformance 69/69, and the 240p Test Suite menu
  photographed on the Missouri stick.

## Speed program (server numbers, i7-12700K; stick budget 16.7 ms/frame)

- GBA: Anguna's 142 ms traced to the HALT path paying full step cost.
  Halt fast-forward plus wait-state caching took the wait scene from
  10.59 ms to 1.049 ms per frame. On the stick after 1.73: 9.4-9.5 ms,
  full speed. Report ~/itak-speed/GBA.md.
- NES round 1 plus NES2: deferred rendering for non-MMC3 carts, then
  seven behavior-identical core changes (bulk projection, packed drive
  bitmask, winner-plane sprite merge, fused bus sync). Zelda went from
  about 2.0 ms to 0.72-0.82 ms per frame server-side. On David's TV the
  NES2 core runs NES Zelda at 15.2-15.7 ms over twelve straight windows:
  full speed, picture verified pixel-perfect. Report NES.md, NES2.md.
- SNES rounds 1 and 2: brightness table copied by value per line removed
  (about 25 percent of frame cost), APU and bus trims, then a page
  resolution cache mined from the reference cores. Cumulative about
  minus 29 percent (SMW 1.912 to 1.350 ms server). Devices measure
  kinder: Zelda title and select screens 16.7-17.4 ms on the TV, later
  screens 14.4-15.1 ms. Reports SNES.md, SNES2.md, SNES-REFS.md.
- Genesis: honest result about 0 to 2 percent, inside noise. The 68k run
  is 59 percent of the frame inside faithful execute() internals. The
  reference core jgenesis is faster because it runs a lower fidelity
  contract (single prefetch word, no bus stream model). A big Genesis
  win needs a fidelity decision from David; see TODO item 7.
- Adapter release profiles: SNES and PS1 adapters had no LTO settings
  while the other four shipped thin LTO plus codegen-units 1; parity
  landed in 1.70 (SNES) and 1.72 (PS1).

## SNES file-select sound: diagnosis (verdict B, Oct 24)

- David (Oct 23, 23:44, on 1.76): entering file select in SNES Zelda
  "does an awful sound and it's horrible."
- Server repro through the production adapter: 1,989 frames, 1,058,981
  stereo samples at 32 kHz, zero screech or dropout buckets. The core's
  audio at that transition is clean.
- The game jumps about 11 dB louder at the transition, exactly where the
  TV runs 17.0-17.3 ms frames against the 16.698 budget. The tear is
  device-side pacing: over-budget frames starve the sound stream.
  Report: ~/itak-speed/snes-audio-repro/REPORT.md. The fix is TODO item 1.

## Debug chute (shipped in 1.69, operating)

- The app reports to our server with a per-install code; check-ins every
  15 minutes by WorkManager, full reports on demand from the server
  side, Settings toggle default on. Receiver lives in the lobby server
  (itak-lobby image v7); storage ~/itak-debug/<key>/ on the server.
- Mary's stick key ca4ee57efe88, label Mary. David's TV key 2797855e36f2,
  label "David home TV". This is the no-touch evidence path for stick
  frame times.

## David's TV bench and the games install (Oct 23)

- The home bench is David's Philips 4K TV itself (Android 11, 32-bit
  userspace, quad Cortex-A53 at 1.55 GHz, Mali-G52, about 1.7 GB RAM).
  It is within roughly 10 percent of the stick's muscle: a good early
  warning bench, not the stopwatch for Mary's stick.
- His full game library was copied stick to TV (44 files, verified).
  The app showed "No games" until the storage grants were set
  (MANAGE_EXTERNAL_STORAGE appops plus READ_EXTERNAL_STORAGE); counts
  then matched the stick exactly. The app not asking for that grant
  itself is TODO item 8.
- His filmed "jittery, words missing" NES Zelda clip was diagnosed as
  the display chain (phone camera at half the screen rate plus TV
  picture processing), with the emulator output proven pixel-perfect
  from the TV's own framebuffer. Advice given: Game picture style,
  dynamic contrast, motion smoothing, noise reduction, and MPEG
  artifact reduction off for the app.

## Test environments

- AVD itak-stick-tv34 on the server (older 32-bit Android TV image):
  proves function, never speed.
- Cuttlefish (installed Oct 23, no server reboot): boots in about 20 s
  but the published app cannot install there; the guest has no 32-bit
  ARM ABI. It is the future bench for the 64-bit 2.0 line. Runbook:
  ~/workspace/itak-cuttlefish/RUNBOOK.md.
