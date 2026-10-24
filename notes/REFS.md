# References: the repos we mine, and where the evidence lives

## License law (David, 2026-10-23)

The harvested reference repos are a standing solution mine: when one of
our cores has a defect or a solved-elsewhere problem, we read how they
solved it and implement the fix in our own code. Techniques,
architecture, and algorithms are the mine. Code text is never copied or
transliterated: jgenesis and sly are GPL-3.0 and this repo is MIT, and
the same write-our-own practice is used for the permissive references.

## Primary references

- **jgenesis** — https://github.com/jsgroth/jgenesis (GPL-3.0)
  Genesis, Sega CD, and SNES emulator in Rust. Primary technique mine.
  Paid off twice: SNES round 2 structure comparison, and the joypad
  byte-order cross-check (it agreed with the SNES hardware spec and
  with sly and rsnes, which is part of how the 1.79 swap was proven).
  Its Genesis speed comes from an instruction-level 68k with a single
  prefetch word and no bus stream model: a lower fidelity contract
  than ours, which is why we did not chase its number.
- **sly** — https://github.com/ioncodes/sly (GPL-3.0)
  Contains GBA and SNES cores only. It has no PlayStation core (an
  early claim otherwise was wrong and was corrected the same day).
  Joypad cross-check source.
- **rsnes** — https://github.com/nat-rix/rsnes (MIT)
  Work-in-progress SNES core (Mode 7, sprites, color math listed
  unimplemented in its README). Light structural reference and the
  third joypad vote.

## The wider harvest (2026-10-21, on the server at ~/itak-speed/refs/)

- NES: potatis (MIT, targets Android and WASM), pinky, sprocketnes,
  plastic, nestur.
- GBA: rustboyadvance-ng.
- N64: gopher64.
- SNES APU: emu-rs/snes-apu (BSD-2).
- Clones on the server: jgenesis, sly, rsnes under ~/itak-speed/refs/,
  plus the NES round-2 set (potatis, pinky, plastic, sprocketnes,
  nestur).

## Our own evidence reports (server, ~/itak-speed/)

- BASELINE.md — build and profile audit of the shipped libraries.
- GBA.md, NES.md, NES2.md — the GBA halt fix and the two NES rounds.
- SNES.md, SNES2.md, SNES-REFS.md — SNES rounds 1 and 2 and the
  reference mining inventory behind round 2.
- GENESIS.md — the Genesis pass and the fidelity verdict.
- PS1FIX.md — the rectangle promotion defect and fix.
- CORESRC.md — how a Rust core reaches the CPU on device (asset
  extraction path), and the frozen-bundle root cause.
- snes-audio-repro/REPORT.md — the file-select sound investigation,
  verdict B, with the capture and analysis rig (rerunnable).
- snes-input-repro/REPORT.md — the joypad swap diagnosis rig, the
  per-button byte-truth proof, and the attribution runs.
- prev-libs/ — previous staged core libraries (including
  libitak_snes-178.so, the pre-fix SNES library).
- prev-gates-178/ — the buggy-era gate references, preserved before
  the 1.79 regeneration.

## Sandbox-side records

- Ship staging and dissection evidence: ~/workspace/ship-166/ through
  ~/workspace/ship-179/.
- Screenshots from the stick and David's TV: ~/workspace/itak-stick-live/
  (the v172 to v174 stick series, the tv178 and tv179 proof series).
- Cuttlefish runbook: ~/workspace/itak-cuttlefish/RUNBOOK.md.
- Push tooling: ~/workspace/skills/github/bin/github.py for text files
  (one file per commit; it has no branch support and writes the default
  branch), ~/workspace/ship-167/push_binary.py for binary files.

## Devices

- Missouri stick ("Death Star"): Chromecast with Google TV HD, device
  boreal, AMLS805X2 (quad Cortex-A35 at 1.8 GHz), Android 14, 32-bit,
  about 1.4 GB RAM, Mali-G31 MP2. Mary and Terry's TV. Evidence arrives
  through the debug chute (key ca4ee57efe88); do not reboot it, do not
  drive it blind.
- David's TV: Philips 4K, Android 11, 32-bit userspace on 64-bit
  silicon, quad Cortex-A53 at 1.55 GHz, Mali-G52, about 1.7 GB RAM.
  The home bench. Chute key 2797855e36f2.
