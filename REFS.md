# Reference Repos, License Law, and the Evidence Index

## The reference-repo mining rule (David, 2026-10-23)

The harvested Rust emulator repos are a standing solution mine:
when one of our cores has a defect or a stuck problem, check how
the reference projects solved it and implement the fix in our own
code. Every speed or defect brief names the relevant reference
repos and requires a written technique/fix inventory.

## License law (absolute)

- jsgroth/jgenesis and ioncodes/sly are **GPL-3.0**. This repo is
  MIT. Techniques, architecture, and algorithms learned by
  reading are the mine; **code text is never copied or
  line-by-line transliterated** (that would relicense this
  project GPL). The same write-our-own practice applies to the
  permissively licensed references, for consistency.

## The mine (clones on the server at ~/itak-speed/refs/)

- **jsgroth/jgenesis** (GPL-3.0): Genesis, Sega CD, SNES. The
  primary technique mine. Its joypad register layout agreed with
  hardware in the 1.79 investigation; its Genesis speed comes
  from a lower fidelity contract (see TRIED.md).
- **ioncodes/sly** (GPL-3.0): GBA and SNES cores only. (An early
  claim that it held a PlayStation core was wrong and was
  corrected the same day.)
- **nat-rix/rsnes** (MIT): SNES, work in progress since 2021;
  README lists Mode 7, sprites, and color math unimplemented.
  Light structural reference only.
- NES round: **potatis** (MIT, targets Android/WASM), pinky,
  sprocketnes, plastic, nestur.
- GBA: rustboyadvance-ng. N64: gopher64. SNES APU: emu-rs/snes-apu
  (BSD-2).

## Where the evidence lives (server, ~/itak-speed/)

- BASELINE.md — shipped-library build audit and adapter baselines.
- GBA.md, NES.md, NES2.md, SNES.md, SNES2.md, SNES-REFS.md,
  GENESIS.md, PS1FIX.md — the per-core speed and fix reports.
- CORESRC.md — how a Rust core reaches the CPU on device, and the
  frozen-core root cause.
- REBUILD-170.md, REBUILD-172.md — library rebuild records.
- snes-audio-repro/REPORT.md — verdict B (the core was clean).
- snes-input-repro/REPORT.md — the joypad swap mechanism and fix.
- snes-audio-cushion/ — the cushion simulation and model.
- audio-cushion-{snes,nes,gameboy,genesis,gba,ps1}/ — the six
  per-engine verification passes for 1.81.
- audio-cushion-integration/ — the 27-check matrix and the
  winning tuning.
- Screenshots: ~/workspace/itak-stick-live/ in lulu's workspace
  (v172/v173/v174 stick series, tv* series for David's TV).
- Debug chute data: ~/itak-debug/ on the server, keyed by the
  first 12 hex of sha256(install code); labels in labels.json.
