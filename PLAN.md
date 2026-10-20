# PLAN: iTaK Gaming

The whole plan for iTaK Gaming, step by step. Each step names the TODO.md phase it maps to. When the plan changes, this file changes first, then TODO.md follows. Facts only: what is decided is stated, what is open is named as an open question.

## What it is

iTaK Gaming is a family arcade for the living room. A Flutter TV frontend and a Go emulator host let each player pick a system, pick a game, and play with separate profiles and save states. Builds are made per platform instead of carrying every system in every package.

The project is open source under MIT. The repo ships no ROMs and no core binaries. Players supply their own legally obtained games and any required system firmware. Online play uses public lobbies and local execution on both sides; the server never serves or streams a game.

## Architecture

| Module | What it owns |
| :--- | :--- |
| `flutter_ui` | TV-first screens: home, system select, game grid, players, online lobbies, leaderboard, settings, logs, update offers, avatars, and the TV keyboard |
| `go_backend` | Emulator host, libretro bridge, native Go core links, video, audio, input, save states, local API, and headless test tools |
| `cores/go-*` | Clean-room Go emulator cores, one module per system, with chip, video, audio, cartridge or disc, system, and test packages |
| `netplay` | Public lobby rendezvous, WebSocket signaling, lockstep relay rules, spectators, friends, play stats, leaderboards, and app distribution metadata |
| Platform builds | Android 32-bit for Chromecast HD, Android 64-bit for stronger TV boxes, Windows 64-bit, and Linux 64-bit; each build carries only the systems that fit it |
| Update feed | Version metadata and APK delivery with SHA-256 verification through the app update flow |

## Build order

1. Hold the legal and repo foundation: MIT code, no ROMs or core binaries in Git, no server-side game delivery, and a per-build license manifest. (TODO Phase 0)
2. Finish the TV shell as separate feature modules, then add the weather home scene, screensaver, and token-free crash reporting. (TODO Phase 1)
3. Keep replacing and proving emulator cores as clean-room Go modules, with a standard proof pack and honest retail-game status for each one. (TODO Phase 2)
4. Ship per-platform builds only after real-device launch proof, and keep profile-driven speed work ahead of new features on the weak stick. (TODO Phase 3)
5. Complete the lobby as the family online room: two-player lockstep first, spectators and friends around it, then the real Kentucky to Missouri acceptance game. (TODO Phase 4)
6. Make family deployment boring: self-updates, the Missouri kit, recovery cards, and an in-person setup check. (TODO Phase 5)
7. Gate every release on tests, lint, secret scanning, leak checks, save compatibility, netplay determinism, and the exact core license manifest. (TODO Phase 6)

## Standing boundaries

- No ROMs, BIOS files, or core binaries are committed to this repo.
- No server-side game delivery or game streaming to other people, ever.
- A build is not ready because it compiles. It is ready after the shipped app runs on the target device and a game launches.
- Own-core speed is measured on the actual Chromecast HD. The hardware is not blamed when a profile points at the core.
- Banner ads are allowed only for a build whose bundled core licenses allow that revenue model. Pop-up ads are not allowed.

## Open questions

- Which home screen editing model should ship: auto-hide, full edit mode, or both? (owner: David)
- Should voice chat move from research into a build, starting with a USB microphone? (owner: David)
- What is the ship order for retail-game proof on the 64-bit cores? (owner: David and the build schedule)
- When will the Kentucky to Missouri acceptance game be played? (owner: David and his brother)
