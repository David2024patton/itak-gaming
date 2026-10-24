# Still to do

Status and blockers as of 2026-10-24, about 01:48 EDT.

## 1. SNES file-select sound tear — FIXED in 1.80, device proof owed

- Resolved by the adaptive audio cushion; full design and simulation
  numbers are in DONE.md ("SNES audio cushion"). Still owed: the
  on-TV proof. Install 1.80 on David's Philips TV in a window he
  calls, enter Zelda file select, confirm by ear, and read the
  logcat cushion lines (engage, release, peak stretch near 1.05,
  zero underruns). If a screen ever overruns harder than about a
  5.7 percent deficit for a long stretch, some tearing can remain;
  the deep cure for that is item 2.

## 2. SNES speed headroom (the deep fix for the same screens)

- The title and file-select screens are the heavy ones on both
  devices. Trimming is mined out (remaining cost is diffuse, no hot
  spot above about 20 percent). Paths not started: multithreading
  picture or sound at exact boundaries, NEON vectorization of pixel
  composition (result-identical, rides along with any path), or a
  pre-read execution engine (value unproven for the 65816; profile
  first). Needs David's pick of direction if pursued beyond item 1.

## 3. Owed stick timings on current cores

- Super Mario World and Genesis Sonic have never been re-timed on the
  Missouri stick on the post-1.73 cores (last numbers are the frozen
  cores': 35.89 ms and 43.09 ms). Path: debug chute timing lines when
  Terry plays them, or a TV window David calls. Never blind-navigate
  the stick.

## 4. NES verdict on the real stick (passive)

- NES2 (1.75) is server-proven and TV-proven at 15.2-15.7 ms. The
  on-stick timing proof waits for Terry's first NES Zelda session on
  1.75 or later; the numbers ride the chute home (key ca4ee57efe88).
  Predicted 13.4-16.2 ms. Do not touch the Missouri TV to force it.

## 5. SNES Zelda genuine in-game completion

- Unblocked by 1.79: file entry, name registration, and real gameplay
  (Link's house and beyond) were effectively impossible with a dead
  D-pad and Start. Provable on David's TV in a called window.

## 6. Go defect ledger, items 1 to 15 (David's call, none authorized)

- Found by the Rust program, deliberately replicated in Rust, not to be
  patched without David's go-ahead. Item 16 (the SNES joypad swap) is
  fixed; see DONE.md. The open fifteen: go-nes crossed-store address
  byte; go-nds LDRD/STRD Rd=15 panic (a one-line guard exists in the
  dev copy); go-gameboy MBC2 rejection; go-gba mulCarryLo infinite
  loop domain; go-saturn SR Q/M swap and MOVEP opmode swap; go-psp
  fpu branch hazard, vus2i/vs2i panics, execRegimm link ordering;
  go-gamecube andis. precedence; go-n64 missing LD/SDR, MTC0 width,
  RSP LFV/SFV lane panics; go-nes branch dummy read addresses;
  go-genesis Z80 RETI/YX/R details (Rust diverges deliberately).
  Full ledger lives in lulu's memory notes for 2026-10-21.

## 7. Genesis full-speed direction (needs David's words)

- Genesis sits at about 1.70 ms server against a roughly 0.62 ms
  target for stick full speed. The reference core's speed comes from a
  lower fidelity contract than ours. Options presented Oct 23:
  (1) pre-read/decoded execution engine, (2) multithreading at exact
  boundaries, (3) a switchable faster, less exact mode. David tapped
  option 3 on a widget Oct 23 and retracted it in words seconds later;
  that tap does not count. Fidelity contract stands until he decides
  in words.

## 8. Fresh-TV storage prompt (UX gap, small)

- On a fresh TV the app never asks for the file-access grant, so the
  library silently shows empty even with games on the device (proven
  on David's TV Oct 23, fixed by hand with appops). The app should
  request the grant itself. Awaiting David's priority call.

## 9. One-press auto update (proposed, not authorized)

- Proposed Oct 23: the app checks on its own, downloads in the
  background, and at the next natural moment goes straight to the
  system installer with Update pre-focused (one press), then
  relaunches home. A sideloaded app cannot silently self-update on
  stock Android TV; this is the buildable floor. Not built.

## 10. Decisions parked with David

- Where the twelve Rust core sources live in this repo (expected
  cores/rust-<slug>); the cores ship as prebuilt libraries today.
- Whether to build an in-app restore for the save backup-and-hold
  backups.
- NES save-state format has no CHR RAM (identical wart in both cores);
  extending it is a both-cores, same-release decision.
- Monetization (banner ads, no pop-ups was the proposal).
- Voice chat is blocked on a Bluetooth microphone David does not own.
- Living weather home screen plus idle screensaver (requested Oct 15):
  city-level location from the connection, Open-Meteo, animated layer
  behind the home grid. Not built.
- Home screen editing (requested Oct 15): auto-hide locked and empty
  systems, or a full edit mode. David has not picked.

## 11. The 64-bit edition (2.0) — paused by David Oct 13

- Intended launch version for the 64-bit build. Local unpushed work
  exists (appdist variant routing, device-capability feed selection,
  MainActivity deviceInfo). Cuttlefish is the test bench when this
  resumes. Then Windows app stage 1 (Flutter SDK on David's PC,
  installer), also parked by his sequencing.
