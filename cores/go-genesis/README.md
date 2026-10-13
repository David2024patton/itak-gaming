# go-genesis: a Sega Genesis core written in Go

Clean-room Sega Genesis / Mega Drive emulator core for iTaK Gaming, written
in Go and exposed through the libretro API so the iTaK engine can load it
exactly like any other core.

Why this exists: the Genesis Plus GX core bundled today carries a
non-commercial license, which blocks any future monetization of the app.
This core is 100 percent our own code (MIT, same as the rest of iTaK
Gaming), written from published hardware documentation: the Motorola
M68000 Programmer's Reference Manual, the Zilog Z80 manual, and the TI
SN76489 datasheet plus Sega variant notes.

Milestone 1 ran on three MIT-licensed Go chip libraries by John
Schember (go-chip-m68k, go-chip-z80, go-chip-sn76489) while the rest of
the machine was built. Those chips have since been replaced by our own
implementations in `chips/`, each differential-tested against the
library it replaced (used only as a black-box test oracle): the Z80
passed 100 seeds of 60,000 instructions in lockstep with zero
divergence, and the 68000 passed a generated-program sweep of 40 seeds
of 100,000 instructions with zero divergence outside documented
undefined-flag cases. The module now has no external dependencies.

## Layout

- `chips/` our own CPU and sound chips: `m68k/` (Motorola 68000),
  `z80/` (Zilog Z80), `psg/` (TI SN76489)
- `cart/` ROM image + header parsing
- `bus/` the 68k and Z80 address maps, work RAM, I/O ports, Z80 control
- `vdp/` the VDP video chip: registers, VRAM/CRAM/VSRAM, DMA, rendering
- `sound/` YM2612 FM + SN76489 PSG, mixed to 44.1kHz stereo
- `system/` the machine: clocks, scanline loop, interrupts, save states
- `libretro/` cgo c-shared exports of the retro_* API (the actual core .so)

## Build (desktop test core)

```
GOTOOLCHAIN=auto CGO_ENABLED=1 go build -buildmode=c-shared \
  -o go_genesis_libretro.so ./libretro
```

## Status

Milestone 1 DONE (2026-10-12), verified against the engine test rig and
direct frame dumps with Sonic The Hedgehog:

- The game boots (SEGA logo, team card, title screen), Start enters
  Green Hill Zone Act 1 and gameplay runs: planes, sprites, scrolling,
  HUD, DMA, HBlank/VBlank interrupts, the Z80 sound driver executes.
- Save states are byte-exact: serialize, restore, re-serialize produces
  an identical image, and play after a restore is frame-for-frame
  deterministic against the unrestored run.
- Sound: the PSG path is live and unit-tested. The YM2612 FM chip is a
  register-accurate shell (games write it constantly; it produces no
  audio yet). Sonic 1's soundtrack is almost entirely FM, so the game
  is effectively silent until milestone 2 builds the FM operator
  engine. That is the next piece of work.
- Known cosmetic issues: the title screen's water ripple (per-line
  scroll effect) renders noisier than hardware.

Chip swap DONE (2026-10-13): the core now runs entirely on our own
chips. Verified end to end again with Sonic The Hedgehog: SEGA logo,
title screen and Green Hill Zone gameplay render correctly, the save
state round trip is byte-exact (0 bytes differ, state size unchanged
at 206,757 bytes), and play after a restore is deterministic. The
engine test rig reports one remaining failure, audio silence in the
opening window, which is the milestone 2 YM2612 gap above.

Not yet wired into the app: the TV still bundles Genesis Plus GX. This
core replaces it once the FM engine lands and it passes a TV playtest.
