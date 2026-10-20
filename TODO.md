<!--
INSTRUCTIONS FOR THIS FILE (user and AI: read before editing. Keep this header.)

WHAT THIS FILE IS: the super-detailed task list for this project. It is the
only place work items live. PLAN.md says why and in what order; ENDGOAL.md
says what finished looks like; this file says exactly what remains to do.

EXACT STRUCTURE:
- Phases in order: "## Phase <n>: <name> (PLAN S<m>)".
- Parked work in its own phase at the end: "## Parked".
- Every item is one checkbox line: "- [ ] T-<phase>.<n> <task> (proof: <named
  proof>) (PLAN S<m>)". Parked items use T-P.<n> and name what blocks them.

RULES:
- A box is checked ONLY when its named proof exists (a test, a log line, a
  bench note, a live check). Never check a box on intention or partial work.
- Emulation-proven and bench-proven are different claims; write which one.
- Item IDs and (PLAN S<n>) tags are stable once published; never renumber
  casually. A deliberate renumber updates PLAN.md in the same pass.
- New work enters as new items at the end of their phase. Completed items
  stay, checked, as the record.
- No secrets, passwords, or tokens in this file, ever.
-->
# TODO: iTaK Gaming

The detailed task list for iTaK Gaming. Rules: a box is checked only when its proof exists and is named (a test, a log line, a device note, a published build record, or a live check). Device-proven and emulation-proven are different claims and are marked differently. Plan steps live in PLAN.md; each phase here names its PLAN section.

## Phase 0: Foundations, rights, and repo standard (PLAN S3)
- [x] T-0.1 Publish the project as open source under MIT (proof: `LICENSE` on main). (PLAN S3)
- [x] T-0.2 Keep ROMs and core binaries out of the Git repository (proof: README rule and `.gitignore`; built apps obtain or bundle cores outside the source tree). (PLAN S3)
- [x] T-0.3 Keep server-side game delivery and streaming of games to other people out of the product (proof: standing decision recorded in PLAN.md; netplay relays inputs and signals only). (PLAN S3)
- [x] T-0.4 Add the repo standard files (proof: `TODO.md`, `IDEA.md`, `PLAN.md`, and `docs/RULES.md` are present after this rollout). (PLAN S3)
- [ ] T-0.5 Add a per-build core manifest that lists every bundled core, its source, its license, and whether that exact build may carry ads or other revenue (proof: manifest generated for the next Android 32-bit and 64-bit builds). (PLAN S3)
- [ ] T-0.6 Run a tree leak check before every public push for ROMs, BIOS files, core binaries, private logs, and credentials (proof: script output stored with the release notes). (PLAN S3)
- [ ] T-0.7 Decide the home screen editing model David left open: auto-hide locked and empty systems, a full edit mode, or both as separate settings (blocked on: David). (PLAN S3)

## Phase 1: TV shell and player experience (PLAN S4)
- [x] T-1.1 Build the Flutter TV frontend with system select, game grid, player profiles, online play, leaderboard, settings, logs, and self-update flows (proof: `flutter_ui/lib/main.dart` and the published app builds). (PLAN S4)
- [x] T-1.2 Support TV remote, Bluetooth remote, and keyboard navigation (proof: shipped TV builds and the navigation model in the Flutter UI). (PLAN S4)
- [x] T-1.3 Keep per-player save states separate, with 10 slots per player per game (proof: save manager and README save-state contract). (PLAN S4)
- [x] T-1.4 Add the self-update offer, SHA-256 check, download, and installer handoff (proof: update flow in `flutter_ui`, server contract in `netplay/DEPLOY.md`, and published feed records). (PLAN S4)
- [ ] T-1.5 Split `flutter_ui/lib/main.dart` into feature modules for home, game grid, players, online, leaderboard, settings, logs, updates, avatars, and TV keyboard, one module per move with behavior tests (proof: no feature owns another feature's state and widget tests pass after each move). (PLAN S4)
- [ ] T-1.6 Add a phone, tablet, and TV screenshot walkthrough for the home, game grid, online, settings, and log screens (proof: dated screenshot set with focus visible in every shot). (PLAN S4)
- [ ] T-1.7 Finish the weather-reactive home scene and idle screensaver from the 2026-10-15 spec (proof: Open-Meteo data shown, animation frame budget measured on the Chromecast HD, screensaver clock wakes cleanly). (PLAN S4)
- [ ] T-1.8 Add crash reporting with no GitHub token in the app: app posts the crash and log to the service, the service files the GitHub issue (proof: forced test crash creates one deduplicated issue and no credential is present in the APK). (PLAN S4)
- [ ] T-1.9 Add a controller and keyboard matrix for every supported build, including pairing loss, reconnect, player assignment, and two-player input (proof: device test sheet signed off per build). (PLAN S4)

## Phase 2: Go backend and clean-room cores (PLAN S5)
- [x] T-2.1 Build the Go emulator host around libretro-style cores with core-negotiated video formats, aspect handling, audio capture, input, and save states (proof: `go_backend` and the headless coretest harness). (PLAN S5)
- [x] T-2.2 Maintain clean-room Go core modules for NES, SNES, Game Boy and Game Boy Color, Game Boy Advance, Genesis and Sega CD, PlayStation, Nintendo 64, Nintendo DS, PSP, Dreamcast, Saturn, and GameCube (proof: modules under `cores/` with package tests). (PLAN S5)
- [x] T-2.3 Prove the Game Boy Advance core against the open-source homebrew game Anguna (proof: release note in `release-notes.txt`). (PLAN S5)
- [ ] T-2.4 Reconcile the backend HTTP surface with the TODO in `go_backend/internal/server/api.go`: implement or retire `/api/games`, `/api/launch`, `/api/save`, `/api/load`, and `/api/players` so no placeholder route remains ambiguous (proof: route tests or a removal commit with the client path documented). (PLAN S5)
- [ ] T-2.5 Give every core a standard proof pack: CPU vectors, video vectors, audio checks, save-state round trip, battery save round trip, and one legally usable homebrew or test program (proof: one report per core, with commercial game checks kept as local evidence only). (PLAN S5)
- [ ] T-2.6 Record retail-game proof separately from homebrew proof for the 64-bit cores (proof: device or desktop run log per core; no core is called retail-proven on homebrew alone). (PLAN S5)
- [ ] T-2.7 Finish the remaining core gaps named in the core READMEs, including Dreamcast descramble and Saturn FH-1 limits, or mark each as an explicit unsupported feature in the UI (proof: core issue closed by test or UI copy names the limit). (PLAN S5)
- [ ] T-2.8 Benchmark every core hot path on the Chromecast HD and publish frame-time results beside the build (proof: benchmark table for each 32-bit system). (PLAN S5)

## Phase 3: Platform builds and performance (PLAN S6)
- [x] T-3.1 Publish iTaK Gaming 1.62, build 63, to the update feed and download page (proof: public APK hash matched the published build on 2026-10-17). (PLAN S6)
- [x] T-3.2 Mail the family stick with build 1.61 aboard and leave build 1.62 available on the update feed (proof: project record after the 2026-10-15 ship decision and the 2026-10-17 feed check). (PLAN S6)
- [ ] T-3.3 Never clear a build for family use until the shipped app has been run on the real target device and at least one game has launched without returning to the home screen (proof: device note naming build, device, system, and game). (PLAN S6)
- [ ] T-3.4 Close the remaining speed work on the 32-bit stick: profile first, optimize the measured hot path, and compare against the known full-speed result on the same chip (proof: before and after frame-time table; hardware is not blamed without profile evidence). (PLAN S6)
- [ ] T-3.5 Add the missing arm64 branch to `tools/autobuild-server.sh` or replace it with the current build pipeline (proof: one clean 64-bit build produced from a fresh checkout). (PLAN S6)
- [ ] T-3.6 Produce and verify separate Android 32-bit, Android 64-bit, Windows 64-bit, and Linux 64-bit builds from the same source revision (proof: build matrix with hashes and launch checks). (PLAN S6)
- [ ] T-3.7 Test the update path on a device that starts two versions behind, including a failed download and a resumed launch (proof: device log and final version readout). (PLAN S6)

## Phase 4: Netplay lobby and social play (PLAN S7)
- [x] T-4.1 Build the public lobby server with open and password lobbies, host and guest tokens, signaling relay, health endpoint, rate limits, lobby caps, and reaping (proof: `netplay/README.md` and `netplay/PROTOCOL.md`). (PLAN S7)
- [x] T-4.2 Keep game state off the server: both clients run the same core and ROM locally and exchange lockstep inputs (proof: protocol design in `netplay/PROTOCOL.md`). (PLAN S7)
- [x] T-4.3 Add spectators, friends, global play stats, and leaderboard endpoints to the netplay service (proof: netplay README endpoint and persistence notes). (PLAN S7)
- [x] T-4.4 Ship spectator watching in the app (proof: build 47 record: Watch on lobbies, live match view, chat, and host snapshots). (PLAN S7)
- [ ] T-4.5 Complete the real Kentucky to Missouri game between David and his brother as the public lobby acceptance test (proof: dated session note with build, game, connection path, desync count, and result). (PLAN S7)
- [ ] T-4.6 Finish or explicitly retire the remaining seat-control and tournament scoreboard work described in the design notes (proof: live build walkthrough or a design note marked retired by David). (PLAN S7)
- [ ] T-4.7 Add protocol fuzz tests for JSON envelopes, WebSocket frames, lobby names, SDP and ICE caps, and roster changes (proof: fuzz run in the netplay gate with no unhandled panic). (PLAN S7)
- [ ] T-4.8 Put the public lobby behind TLS in production and verify password and token traffic never travel over plain HTTP (proof: deployment check and redirect test). (PLAN S7)
- [ ] T-4.9 Decide whether voice chat leaves research and enters a build, using the USB microphone first scope in `VOICE_CHAT_RESEARCH.md` (blocked on: David). (PLAN S7)

## Phase 5: Updates and family field kits (PLAN S8)
- [x] T-5.1 Publish app updates from the arcade update feed with version metadata and SHA-256 verification (proof: update flow in the app and feed records). (PLAN S8)
- [x] T-5.2 Write the Missouri setup kit for the family box, ROM folders, BIOS expectations, controller assignment, online play, updates, and log recovery (proof: `MISSOURI-KIT.md`). (PLAN S8)
- [ ] T-5.3 Complete the in-person Missouri setup and confirm the box, controller, game library, profiles, and update path in the house where it will live (blocked on: travel and hands at the destination). (PLAN S8)
- [ ] T-5.4 Add a one-page recovery card for a failed update, a lost controller pairing, and a box that opens to the wrong home screen (proof: card tested by a family user without developer help). (PLAN S8)

## Phase 6: Quality, security, and release gates (PLAN S9)
- [x] T-6.1 Keep Go tests in the backend, netplay service, and core modules (proof: `_test.go` files across `go_backend`, `netplay`, and `cores`). (PLAN S9)
- [ ] T-6.2 Make one release gate run Dart analysis, Flutter tests, Go vet, Go tests for every module, lint, secret scan, and the ROM and binary leak check (proof: clean gate log tied to the release commit). (PLAN S9)
- [ ] T-6.3 Add deterministic replay tests for netplay lockstep and state-hash recovery (proof: recorded input stream reaches the same state hash on two clients). (PLAN S9)
- [ ] T-6.4 Add save-state compatibility tests across one app upgrade for every bundled core (proof: old save loads after upgrade or the app shows a clear incompatibility message). (PLAN S9)
- [ ] T-6.5 Review the lobby server for token storage, password hashing, rate limits, request caps, and log redaction before any wider public launch (proof: security checklist with code references). (PLAN S9)
- [ ] T-6.6 Keep banner ads out until the exact build core manifest proves every bundled core allows the revenue model; no pop-up ads in any build (proof: manifest review in the release gate). (PLAN S9)

## Parked (waiting on a decision, hardware, or hands)
- T-P.1 Home screen editing model (blocked on: David's pick). (PLAN S4)
- T-P.2 Voice chat build decision (blocked on: David). (PLAN S7)
- T-P.3 Real Kentucky to Missouri acceptance game (blocked on: a scheduled session with David's brother). (PLAN S7)
- T-P.4 Missouri in-person setup (blocked on: travel). (PLAN S8)
- T-P.5 Banner ads (blocked on: exact per-build license manifest and David's go). (PLAN S9)
