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

## Sega CD support (SCD-1, foundations)

What landed in this slice:

- A second 68000 sub CPU running at 12.5 MHz with its own bus view: boot window, 512 KB PRG-RAM, 256 KB Word RAM, the gate array, and PCM register and wave RAM storage.
- Word RAM 2M and 1M modes with the DMNA/RET ownership handshake between the two CPUs.
- The gate array at 0xA12000 on the main side, mirrored at 0xFF8000 for the sub CPU: reset and bus-request control, comm flags, 8 command words and 8 status words, and the H-INT interval and vector registers.
- H-INT delivered to the main CPU as a level 2 interrupt at the programmed line cadence.
- A CD block layer opening .cue/.bin and .iso images, with cue parsing, MSF/LBA conversion, and Mode 1/Mode 2 data extraction.
- A simplified CDC model: latched LBA, count and destination, DMA-style transfer into PRG-RAM, Word RAM or PCM wave RAM, or a polled data port. Its busy flag clears after two status reads. The CDD is held at a status-response model.

Boot honesty: this core contains NO Sega BIOS code. Both BIOS roles are filled by small hand-written 68k stubs in the scd package. The sub stub performs the documented boot flow: read sector 0, verify the SEGADISCSYSTEM signature, load the initial program from the header fields into PRG-RAM, and jump to it. A main-side stub program releases the sub CPU and runs the comm handshake for CD-only boots. Real Sega CD BIOS call services (the BIOS jump tables games call) are NOT implemented yet.

Save states: cartridge-only states are byte-identical to before and still load. With a disc attached, the state grows by the sub CPU registers, the full CD RAM and register state, and the sub cycle counter: the state size in CD mode is 1,061,509 bytes (cartridge mode is 207,149 bytes). A CD state needs the CD attached to load.

Simplifications, stated plainly:

- The main CPU's own Word RAM window in cartridge space (0x200000) is not routed yet, so cartridge space is completely untouched in this slice. This also keeps SRAM cartridges safe. The ownership model is enforced and tested at the unit level.
- CDC transfers complete instantly apart from the two-read busy window.
- No wait states are charged on sub CPU memory accesses.
- PRG-RAM banking and protection, and the 1M dual-port second window, are not modeled.
- PCM registers and wave RAM are storage only: there is no PCM or CD audio yet.

Remaining slices at the end of SCD-1: SCD-2 (CD-DA and RF5C164 PCM audio, full CDD command set), SCD-3 (graphics scaler/rotator ASIC, real-game engine test with Sonic CD from a user-supplied image), SCD-4 (app wiring so the Sega CD slot swaps off Genesis Plus GX). SCD-2 has since landed; see the next section.

## Sega CD support (SCD-2, CDD + CD-DA + PCM + banking)

What landed in this slice:

- The full CDD host interface at its documented sub CPU offsets: fader word at 0xFF8034, control byte (HOCK handshake) written at 0xFF8037, status nibbles RS0-RS9 read at 0xFF8038-0xFF8041, command nibbles TC0-TC9 written at 0xFF8042-0xFF804B. A command executes when TC9 lands and its checksum verifies (the sum of all ten nibbles is 0xF mod 16; a bad checksum is ignored). The response packet becomes visible at the next 75 Hz status tick (an exact accumulator off sub CPU cycles) and raises an interrupt. Commands: get status, stop, TOC info (absolute and relative position, current track, total length, first/last track, per-track start with the data-track flag), read/play, seek, pause, resume, fast forward/reverse (8x position rate, audio muted), open and close tray, plus error rejection of out-of-range and non-BCD targets with no state change. The documented-but-unnamed commands 0x05, 0x0A, 0x0B, 0x0E and 0x0F answer with a plain status packet.
- CD-DA playback: audio tracks stream from the cue/bin image through Disc.ReadAudio (raw 2352 byte sectors), position tracked in absolute samples (588 stereo frames per sector) with a sub-sector counter, play/pause/seek/stop from the CDD, track end advancing into the next audio track, stop at a data track boundary or at disc end. The fader register scales samples by FD/1024 in integer math (FD = 1024 is unity).
- The RF5C164 PCM chip in full: 8 channels programmed through the register page at 0xFF0000 (ENV gain, PAN with 4 bit left/right gains, FD phase increment, loop start, start address with write-retrigger, CTRL with chip on/off plus the MOD channel/wave-bank select, per-channel ON/OFF), wave RAM window banked in 4 KB pages at 0xFF1000. Output mixes with the documented integer math and resamples to the machine rate.
- Interrupts on the sub CPU: a delivered CDD response raises level 4 and a completed CDC transfer raises level 5, each gated by a mask bit in gate array register 0x03 (bit1 for CDD, bit2 for CDC). Per the published CDD documentation and the sub BIOS interrupt layout, CDD is level 4 and CDC is level 5.
- PRG-RAM banking and protection: the main CPU reaches PRG-RAM through a 128 KB window at 0x420000, banked by memory mode register bits 7:6, with a write protect field (bits 4:2 of the main-written register byte) protecting the first N x 64 KB of physical PRG-RAM against main window writes. The main CPU Word RAM windows are routed on the Genesis bus: 0x200000-0x23FFFF and its 2M decode at 0x600000-0x63FFFF, both enforcing the 2M/1M ownership model, all inert when no CD is attached. Bus request has its acknowledge readback at 0xA12000 bit1.
- Register coexistence: the CDD interface overlaps the SCD-1 CDC latch addresses. Writes to 0xFF8038-0xFF8041 still land in the CDC latches while reads there return CDD status nibbles; the 0xFF8037 read stays the CDC status register while its write is the CDD control byte; the 0xFF8048 read stays the CDC data port while its write is command nibble TC6.
- Save states: the SCD-2 state (CDD machine and positions, PCM channels and control, bank and protection registers, interrupt latches) fits inside the previously reserved tail bytes of the CD block, so the CD mode state size is unchanged at 1,061,509 bytes and cartridge mode is unchanged at 207,149 bytes. A determinism gate saves mid-playback (PCM channel plus CD-DA track running) and proves sample-identical continuation audio and byte-identical state after restore.

Measured evidence from the gates (synthetic fixtures, zero crossings unless noted): PCM sine at FD 0x0800 over a 256 byte period measured 127.00 Hz against a 127.16 Hz target; sign-magnitude decode peaks exactly at the documented mix value 15060; CD-DA sines measured 439 Hz and 879 Hz against 440 Hz and 880 Hz targets; one second of playback advanced the position exactly 44,100 samples (75 sectors); fast forward advanced at exactly 8x over the measured window; fader FD 512 versus FD 1024 peak ratio measured exactly 0.5.

Simplifications and honesty notes, stated plainly:

- Fader curve: the manual publishes the fade data as an 11 bit field with 1024 at unity; we apply it as a straight linear gain FD/1024. Hardware fades toward the target over time; we apply the target instantly, so the EFDT bit always reads complete. If the published per-step dB table is located later, the gain function changes in one place.
- PCM rate constant: the RF5C164 steps at 12.5 MHz / 384 = 32,552.08 Hz per the Ricoh documentation of the chip family and the Mega CD manual PCM chapter (not the 31,250 Hz sometimes quoted). Sample format is sign-magnitude with 0xFF as the loop marker byte, per the same sources.
- PCM mixing: ENV is a constant per-channel gain (the chip has no envelope generator, and no separate VOL register exists on it). PAN high nibble is the left gain, low nibble the right gain. The datasheet restriction on CPU wave RAM access while the chip is sounding is not modeled; CPU access is always allowed, which sample-streaming software relies on.
- Wait states: published figures for sub CPU accesses to Word RAM and PRG-RAM are not stated consistently in the material we have, so no wait states are charged; the sub CPU runs at a flat 12.5 MHz, as in SCD-1.
- Play modes: the CDD register level has no documented repeat control (TC8 is documented as unused), so there is none: track end advances to the next audio track and the disc end stops. PLAY against a data track runs the documented data read path, streaming one sector per 75 Hz tick into the CDC buffer.
- The scan rate multiple (8x), the WP field decode (bits 4:2, 64 KB boundary units), and the register 0x03 interrupt mask bits are documented model choices where the published material available to us does not pin the exact bits; each lives in exactly one place in the code.

Remaining slices: SCD-3 (graphics scaler/rotator ASIC, real-game engine test with Sonic CD from a user-supplied image), SCD-4 (app wiring so the Sega CD slot swaps off Genesis Plus GX for this core).
