# iTaK Gaming work notes

This branch is the continuity record for iTaK Gaming. It exists so the work
can be picked up exactly where it left off, from any machine or session.

Snapshot: 2026-10-24, about 01:25 EDT, written by lulu during the 1.80 work.

Read in this order:

1. `notes/DONE.md` — what is finished, with the proof for each item.
2. `notes/TODO.md` — what is still open, what each item needs, and what it
   is waiting on.
3. `notes/REFS.md` — the reference repos we mine, the license law for using
   them, and where the evidence reports live.

## Where things stand right now

- Latest shipped build: **1.79** (versionCode 80), published 2026-10-24
  04:50 UTC to https://arcade.itak.live/app/version.json
  APK sha256 0363ce6dbd63885decc8a4d4e1645a15426d4c591f6b4f7186d96dd3b36a51df,
  36,664,973 bytes. It fixes the SNES controller byte swap (see DONE.md).
- In flight: the **1.80 audio cushion** for the SNES file-select sound tear
  (TODO.md item 1). David confirmed the symptom is the sound and ordered
  the build at 01:22 EDT.
- David's Philips TV (the home bench): 1.79 installed and proven on device.
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
