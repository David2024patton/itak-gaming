<!--
INSTRUCTIONS FOR THIS FILE (user and AI: read before editing. Keep this header.)

WHAT THIS FILE IS: the whole plan for this project, step by step. ENDGOAL.md
is the destination; this file is the route; TODO.md is the work list. When
the plan changes, this file changes FIRST, then TODO.md follows.

EXACT STRUCTURE:
- "## S1: What it is" (the product in plain words, who it serves).
- "## S2: Architecture" (the grid: modules, what each owns, the seams).
- Build steps as numbered sections "## S<n>: <step name> (TODO Phase <n>)",
  one section per build step, each naming the shelf repos it uses (from
  ~/workspace/google-org-harvest/USE-IT.md) and the hosting or delivery
  shape where that matters.
- A final "## S<last>: Open questions" section (owner named per question).

RULES:
- Section numbers are stable once published; TODO items point at them with
  (PLAN S<n>). Renumber only in a deliberate pass that updates both files.
- Facts only: what is decided is stated, what is open is an open question.
- Before designing a step, check the shelf: take or retrofit what already
  exists under a usable license instead of rebuilding it.
- No secrets, passwords, or tokens in this file, ever.
-->
# PLAN: iTaK Gaming

The whole plan for iTaK Gaming, step by step. Sections are numbered S1, S2, ... and each build step names the TODO.md phase it maps to; every TODO item points back with (PLAN S<n>). When the plan changes, this file changes first, then TODO.md follows. Facts only: what is decided is stated, what is open is named as an open question.

## S1: What it is

iTaK Gaming is a family arcade for the living room. A Flutter TV frontend and a Go emulator host let each player pick a system, pick a game, and play with separate profiles and save states. Builds are made per platform instead of carrying every system in every package.

The project is open source under MIT. The repo ships no ROMs and no core binaries. Players supply their own legally obtained games and any required system firmware. Online play uses public lobbies and local execution on both sides; the server never serves or streams a game.

## S2: Architecture

| Module | What it owns |
| :--- | :--- |
| `flutter_ui` | TV-first screens: home, system select, game grid, players, online lobbies, leaderboard, settings, logs, update offers, avatars, and the TV keyboard |
| `go_backend` | Emulator host, libretro bridge, native Go core links, video, audio, input, save states, local API, and headless test tools |
| `cores/go-*` | Clean-room Go emulator cores, one module per system, with chip, video, audio, cartridge or disc, system, and test packages |
| `netplay` | Public lobby rendezvous, WebSocket signaling, lockstep relay rules, spectators, friends, play stats, leaderboards, and app distribution metadata |
| Platform builds | Android 32-bit for Chromecast HD, Android 64-bit for stronger TV boxes, Windows 64-bit, and Linux 64-bit; each build carries only the systems that fit it |
| Update feed | Version metadata and APK delivery with SHA-256 verification through the app update flow |

## S3: Hold the legal and repo foundation (TODO Phase 0)

MIT code, no ROMs or core binaries in Git, no server-side game delivery, and a per-build license manifest that says exactly which cores a build carries and whether that build may carry revenue.

## S4: Finish the TV shell (TODO Phase 1)

Separate feature modules for home, game grid, players, online, leaderboard, settings, logs, updates, avatars, and TV keyboard, then the weather home scene, screensaver, and token-free crash reporting. App models use built_value.dart (google/built_value.dart, BSD-3-Clause) for immutable value types and generated serialization of catalog and save data. Any in-app machine learning comes from flutter-mediapipe (google/flutter-mediapipe, Apache-2.0) pub packages instead of hand-rolled platform FFI.

## S5: Keep replacing and proving emulator cores (TODO Phase 2)

Clean-room Go modules, one per system, each with a standard proof pack and an honest retail-game status. Homebrew proof and retail proof are recorded separately; no core is called retail-proven on homebrew alone.

## S6: Ship per-platform builds only after real-device proof (TODO Phase 3)

Profile-driven speed work stays ahead of new features on the weak stick. Device QA runs on artemis (google/artemis, Apache-2.0), which drives real Android phones and TVs from natural-language test flows with Logcat and screenshots, and on cloud-android-orchestration (google/cloud-android-orchestration, Apache-2.0) as a Cuttlefish device farm for automated APK testing. bundletool (google/bundletool, Apache-2.0) builds the device-specific APK sets from one bundle and validates what will actually install.

## S7: Complete the lobby as the family online room (TODO Phase 4)

Two-player lockstep first, spectators and friends around it, then the real Kentucky to Missouri acceptance game. Game state never lives on the server; both sides run the same core and ROM locally and exchange inputs.

## S8: Make family deployment boring (TODO Phase 5)

Self-updates, the Missouri kit, recovery cards, and an in-person setup check in the house where the box will live.

## S9: Gate every release (TODO Phase 6)

Tests, lint, secret scanning, leak checks, save compatibility, netplay determinism, and the exact core license manifest. Banner ads stay out until the manifest proves the build allows them; pop-up ads never ship.

## S10: Standing boundaries

- No ROMs, BIOS files, or core binaries are committed to this repo.
- No server-side game delivery or game streaming to other people, ever.
- A build is not ready because it compiles. It is ready after the shipped app runs on the target device and a game launches.
- Own-core speed is measured on the actual Chromecast HD. The hardware is not blamed when a profile points at the core.
- Banner ads are allowed only for a build whose bundled core licenses allow that revenue model. Pop-up ads are not allowed.

## S11: Open questions

- Which home screen editing model should ship: auto-hide, full edit mode, or both? (owner: David)
- Should voice chat move from research into a build, starting with a USB microphone? (owner: David)
- What is the ship order for retail-game proof on the 64-bit cores? (owner: David and the build schedule)
- When will the Kentucky to Missouri acceptance game be played? (owner: David and his brother)
