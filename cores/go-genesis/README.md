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
- Sound: the PSG path is live and unit-tested. The YM2612 FM chip
  started as a register-accurate shell; milestone 2 below replaced it
  with the full FM operator engine.
- Known cosmetic issues: the title screen's water ripple (per-line
  scroll effect) renders noisier than hardware.

Chip swap DONE (2026-10-13): the core now runs entirely on our own
chips. Verified end to end again with Sonic The Hedgehog: SEGA logo,
title screen and Green Hill Zone gameplay render correctly, the save
state round trip is byte-exact (0 bytes differ, state size unchanged
at 206,757 bytes), and play after a restore is deterministic.

Milestone 2 DONE (2026-10-13): the YM2612 FM operator engine is built
in-house (6 channels, 4 operators per channel, envelopes with SSG-EG,
feedback, LFO, channel 3 special mode, DAC channel, hardware timers)
and unit-tested, including a 440.00 Hz frequency check on a keyed
channel. Two wirings completed it: the timers advance from the system
loop at scanline precision (a polling program sees the flag mid-frame;
261 timer services land inside one frame), and register writes are
timestamped, so the sound up to each write's emulated moment renders
before the write lands. A DAC sample stream (the SEGA jingle, about
275 sample writes per frame) now plays at full rate: measured
1,200-1,400 waveform-active samples per frame in the jingle window,
where frame-end rendering managed about 2. The pan registers power on
routed to both sides, as hardware behaves (the jingle never writes
pan first). In the Sonic save test the music window produces 484,644
non-zero samples (was 33,002, PSG only), save states stay byte-exact
at the grown size of 207,149 bytes, and the engine test rig now
reports ALL TESTS PASSED.

Not yet wired into the app: the TV still bundles Genesis Plus GX.
Wiring this core in for Genesis (Sega CD stays on Genesis Plus GX
until our own Sega CD support lands) is the next job, gated on a TV
playtest.
