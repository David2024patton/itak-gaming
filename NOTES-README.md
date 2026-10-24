# iTaK Gaming work notes

This branch is the continuity record for iTaK Gaming. It exists so the work
can be picked up exactly where it left off, from any machine or session.

Snapshot: 2026-10-24, about 01:48 EDT, updated after the 1.80 ship.

Read in this order:

1. `notes/DONE.md` — what is finished, with the proof for each item.
2. `notes/TODO.md` — what is still open, what each item needs, and what it
   is waiting on.
3. `notes/REFS.md` — the reference repos we mine, the license law for using
   them, and where the evidence reports live.

## Where things stand right now

- Latest shipped build: **1.81** (versionCode 82), published 2026-10-24
  06:45 UTC to https://arcade.itak.live/app/version.json
  APK sha256 299476dc0d864e2bb2d8c73ff6885c9c0eddb61c99fb45e1b0839fe5a5058092,
  36,664,973 bytes. It carries the integrated all-engine audio cushion
  retune (LEAD_TARGET 1800, LEAD_BAND 300, STRETCH_MAX 1.08; see
  DONE.md). 1.80 (code 81) introduced the cushion; 1.79 (code 80)
  fixed the SNES controller byte swap.
- Owed: the on-device proof of 1.81. It is already installed on
  David's TV (silent install, dumpsys 1.81/82); the listen test
  (Zelda file select by ear plus logcat cushion lines, and no
  cushion engagement during healthy NES/Game Boy play) waits for a
  window he calls.
- David's Philips TV (the home bench): 1.79 was the last build fully
  proven on device; 1.80 and 1.81 are installed there.
  The Missouri stick (Mary and Terry): still on 1.75; it takes updates from
  the same feed when used.

## Environment pointers (names only, no secrets)

- Build server: ubuntu-server, reached over Tailscale SSH alias `ts-server`.
  That server hosts live business websites and must NEVER be rebooted.
- Pipeline: clone at ~/itak-repo, build tree at ~/itak-gaming, staged Rust
  core libraries at ~/itak-rust-libs/armeabi-v7a/, autobuild log at
  ~/itak-autobuild.log. Pushing a pubspec version bump to main is what
  triggers a build; push pubspec LAST, one file per commit.
- Evidence reports: ~/itak-speed/ on the server (index in REFS.md).
  Screenshots: ~/workspace/itak-stick-live/ on the sandbox.
- Update feed: https://arcade.itak.live/app/version.json and
  https://arcade.itak.live/app/latest.apk

## Working rules that govern this project

- Modular everything: one feature, one module, one commit, one push.
- Nothing is called done without proof on the real hardware or a named
  gate run. Rust core claims name their cargo profile; plain dev profile
  `cargo test` is the binding run.
- TV etiquette: before touching a TV's screen, take a screenshot first.
  If the picture is black or someone is watching, hands off.
- The Missouri stick is never rebooted (it kills wireless debugging) and
  its TV is not driven blind; its evidence rides the in-app debug chute.
- No commercial ROMs are committed, downloaded into the repo, or
  distributed. Test ROMs are local copies only.
