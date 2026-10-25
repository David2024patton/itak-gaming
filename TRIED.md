# Everything Tried: What Worked, What Did Not

Organized by area. "Worked" means proven with evidence (device,
gate, or byte comparison), not believed. Dates are 2026 unless
noted. Deep reports live in ~/itak-speed/ on the server (REFS.md).

## Core speed

### Worked
- **GBA halt fast-forward** (1.70): Anguna's 142 ms frames traced
  to the HALT path paying full step costs while halted. Halt fast
  path plus WAITCNT caching and APU fast paths: wait scene
  10.59 -> 1.05 ms server; stick proved full speed at 9.4 ms.
- **NES deferred rendering** (round 1): render at drive points
  for non-A12 cartridges, exact path kept for MMC3. Zelda
  ~2.0 -> ~1.12 ms server.
- **NES2 micro-optimizations** (1.75): profile first (ptrace RIP
  sampler: rendering ~50%, drive/sync ~22%, bus/CPU ~25%), then
  seven behavior-identical changes (bulk projection, packed drive
  bitmask, winner-plane sprite merge, fused bus sync, more).
  Zelda -> 0.72 to 0.82 ms server. Every gate re-green.
- **SNES brightness-table borrow fix** (round 1): a 32,768-entry
  table was being copied by value per rendered line (~25% of the
  frame). Field-split borrow plus zipped iteration, APU pump
  divide replaced by repeated subtraction, single bus region
  classification. SMW 1.91 -> 1.33 ms server.
- **SNES page-resolution cache** (round 2): 2,048-entry memo on
  the console bus. SMW -> 1.35, Zelda -> 1.27 ms. Cumulative
  about minus 29% from baseline.
- **Adapter profile parity** (1.70, 1.72): SNES and PS1 adapters
  had no release profile (no LTO, 16 codegen units) while the
  other four shipped thin LTO + codegen-units 1. Matching them
  was free speed.
- **PS1 zero-size rectangle fix** (1.72): the GPU promoted
  zero-height rects to full height for RENDERING (the rule only
  applies to memory transfers), so a per-frame black rect painted
  over every page. Deleting two promotions fixed PS1's picture.

### Did not work / dead ends (do not retry blindly)
- **Genesis micro-optimizations:** batched flag updates, timestamp
  sharing, branch-condition hoisting kept (proven identical) but
  total gain ~0 to 2%, inside noise. The 68k interpreter plus the
  bus-stream fidelity model IS the cost; there is no hot spot
  left above ~20%. A VDP field restructure REGRESSED 0.226 ->
  0.346 ms and was reverted.
- **Decoded-block caching for the 6502:** runtime decode measured
  at 0.3% of frame cost. Dead idea, killed by profiling.
- **Fat LTO and target-cpu=native:** neither beat thin LTO on the
  shipped adapters. Tested, measured, dropped.
- **Borrowing jgenesis's Genesis speed:** its 68k is instruction
  level with no bus-stream model, a lower fidelity contract than
  ours. The technique is the contract; cannot take one without
  the other. (David's call if that ever changes.)

## Input

### Worked
- **The SNES joypad byte swap** (found 2026-10-24, fixed in 1.79):
  the auto-read bytes were stored swapped ($4218/$4219), so Start
  and the D-pad landed in the byte games mask off. Start was dead,
  the D-pad was dead, A posed as B. Found as a side discovery of
  the audio repro rig; proven by a per-button truth table read
  back through save states; cross-checked against hardware
  documentation and three reference cores.
- **Attribution method for gate regeneration:** the test gates'
  reference frames encoded the bug. Feeding the FIXED core the
  OLD game-visible byte stream reproduced every old reference
  byte for byte, proving the only change was the mapping. Gates
  were then regenerated from the fixed core legitimately.
- **Long-press adb keyevents** for driving games in tests: short
  synthetic presses (~50-100 ms) are unreliable through the app's
  key-state polling; --longpress registers every time.

### Did not work
- **Trusting the unit tests on input:** both the Rust and Go
  joypad tests ASSERTED the swapped layout. The gate ratified the
  bug for months. Lesson: tests copied from the implementation
  prove nothing about hardware; the truth table came from
  hardware docs plus reference cores.

## Audio

### Worked
- **Verdict B method:** before touching the app, a server rig
  captured the core's raw audio through the production adapter
  and analyzed it in 100 ms buckets. The core was CLEAN; the tear
  was device pacing. Never fix a core for a pipeline problem.
- **Simulation-first cushion design:** the whole audio pipeline
  (gate, buffers, DAC) was simulated against measured device
  frame times before any code shipped. Predicted starved samples
  matched the device complaint; the shipped fix scored zero
  starved in sim before it scored zero on hardware.
- **The 27-check integration matrix** (1.81): when three engines
  proposed three different retunes, all candidates ran against
  all traces with hard pass/fail (purity AND coverage). The
  winner was picked on numbers, and two equally scoring schemes
  lost on stated preference (less machinery, no added latency).
- **Semitone pricing for stretch limits:** every clamp proposal
  is priced in semitones of pitch bend (1.06 = 1.01, 1.08 = 1.33,
  Genesis's needed 1.31x = 4.73). The Genesis clamp raise was
  rejected on that price, in the open.

### Did not work
- **A bigger fixed buffer:** the file-select deficit is a RATE
  problem (~95.5% production for ~21 s). Any fixed bank only
  delays the tear, and banking ~800 ms would delay ALL sound by
  that much. Rejected on math before building.
- **SNES-tuned constants at 44.1 kHz:** correct at 32 kHz, wrong
  geometry at 44.1 (band bottom inside the gate sawtooth trough).
  The constants were not rate-robust; that is what 1.81 fixed.

## App, display, and updates

### Worked
- **Core refresh by hash** (1.73): cores were extracted once and
  never updated (exists-check), so updated installs ran
  first-install cores forever. SHA-256 compare on every launch,
  extract to a temp name, verify, rename over. This one fix
  explained a month of confusing device numbers.
- **Dark dialog theme** (1.78): stock Android dialogs borrowed
  the device skin and rendered white-on-white on David's TV.
  A pinned app-owned dark theme on all 13 dialog sites ended it.
- **Theme-level white flash fix** (1.77): light launch themes
  plus a white launch background painted a white field on open
  and close. Black themes and black backgrounds, proven frame
  by frame on the TV.
- **Intro fade** (1.77) and **intro verdict in Kotlin process
  memory** (1.74).
- **NES tile matte cut** (1.76): ISNet segmentation plus a
  bounded flood-fill punch for the enclosed white wedge.
- **Storage grant by appops** (TV, by hand): a fresh TV install
  showed "No games" with the files verified present; the app
  lacked MANAGE_EXTERNAL_STORAGE. `cmd appops set --uid ...
  MANAGE_EXTERNAL_STORAGE allow` plus the read grant fixed it;
  counts matched the stick exactly.

### Did not work
- **Intro verdict in Dart statics** (1.73 attempt): the Flutter
  engine is recreated on return from the emulator activity,
  wiping Dart statics while the process lives. The intro
  replayed after every game. Moved to Kotlin process memory.
- **rembg u2net for the NES tile:** ate the controller out of
  the photo. Rejected on sight; ISNet chain shipped instead.
- **screenrecord on the Philips TV:** records pure black
  (MediaTek quirk). Screencaps are the only evidence path there.
- **Wikimedia fetch for console art:** 403; the shipped photo's
  own pixels were the source instead. Not retried.

## Saves and safety

### Worked
- **Backup-and-hold** (1.66): on the first Rust-core session, the
  app copies the whole saves tree aside (no code path writes it
  again) and holds auto-save until the player's first manual
  save. Proven on the Missouri stick: 11 files, 6.8 MB, before
  any Rust core ran a game there.

## Test rigs and devices

### Worked
- **The hardware hierarchy:** Missouri stick (ground truth) >
  David's TV (calibrated early-warning bench, about 10% off
  stick muscle) > server repro rigs (core truth) > guests
  (function only, never speed).
- **AVD itak-stick-tv34** on the server (older 32-bit Android TV
  x86 image with ARM translation): installs and runs the app,
  proves function. Guest pathologies (focus ANRs) documented;
  never used for speed claims.
- **Cuttlefish install with no reboot** (2026-10-23): host
  packages, hand-modprobed modules, runtime-only apparmor
  relaxation. Boots in 20 s. See the wall below.
- **The debug chute** (1.69): every install pings home every
  15 minutes and can pull a full report on request. This is now
  the main no-touch evidence path for the Missouri stick.

### Did not work
- **Cuttlefish for the 1.x line:** the guest's abilist is
  x86_64/arm64 with no 32-bit ARM; the v7a-only APK fails with
  INSTALL_FAILED_NO_MATCHING_ABIS. Cuttlefish serves the future
  64-bit (2.0) checks only.
- **TiVo Stream 4K as a lab stick:** great specs on paper (same
  GPU as the stick); the unit boot-looped with flickering HDMI,
  suspected under-power. David closed it: "Forget the TiVo."
- **Chromecast 2nd generation (round puck):** cast-only, cannot
  run apps at all. Hard no as a test device at any price.
- **Judging builds on server numbers alone:** the frozen-core
  bug made every stick measurement between 1.66 and 1.73 a
  measurement of the 1.66 cores. Device evidence or it did not
  happen.

## Process

### Worked
- **One feature, one module, one commit, one push**; pubspec
  pushed last (it triggers the build); ship files derived from
  current remote files plus only the intended change (local
  trees carry paused-branch work and must never be pushed blind).
- **APK dissection on every ship:** the diff against the previous
  build must contain only what the release notes claim.
- **Fan-out with evidence, parent re-verifies:** parallel agents
  bring back artifacts and numbers; lulu re-runs the decisive
  checks herself before anything is called done. File inspection
  alone never proves a dead agent's work functioned.
- **The stand rule (David, 2026-10-23):** a proven defect with a
  clear fix gets built in full and proven on hardware. That is
  not a question to put back in front of him.

### Did not work
- **Server-side edits to the pipeline clone:** the autobuild
  rsyncs the clone over the build tree WITH DELETE every cycle.
  Unpushed server-local changes are reverted silently. Every
  durable change goes through GitHub.
