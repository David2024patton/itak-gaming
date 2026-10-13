# go-saturn

A from-scratch Sega Saturn emulator core in Go, written from
published hardware documentation (the SH-2 and SH7604 manuals,
the Saturn hardware and CD block references). Zero third-party
dependencies. No BIOS bytes are used or shipped anywhere in
this module; the eventual boot path is HLE (load the game's
initial program and jump to its entry).

## What exists in SAT-1

### chips/sh2: dual SH-2 CPU

The complete SH-2 instruction set as listed in the SH-2
hardware manual: the MOV family in every addressing mode,
MAC.W and MAC.L, MUL.L, DMULS.L and DMULU.L, the full
arithmetic, logic, shift and rotate sets, system control
(LDC, STC, LDS, STS for GBR, VBR, SR, PR, MACH, MACL),
branches with exact delay slots, TRAPA, RTE, SLEEP, and
the exception model (general illegal vector 4, slot
illegal vector 6, CPU address error vector 7, TRAPA base
vector 32 plus the trap number). The CPU is instantiated
twice, master and slave, with fully independent state and
a shared bus interface. NMI and maskable interrupt inputs
are exposed for the system, vectoring through VBR with the
documented stack frame. Each CPU serializes to exactly
104 bytes.

SH-2 versus SH-4 boundary, decided from the SH-2 manual:
DMULS.L and DMULU.L ARE SH-2 instructions and are
implemented; MUL.L and MAC.L are SH-2. SH-4-only forms are
illegal here: register banks (LDC Rn,Rn_BANK and friends),
the FPU register set (STS and LDS of FPUL and FPSCR), and
MOVUA. The SH-2 has no banked general registers and no
FPU, so those encodings raise the general illegal
instruction exception. The 0xF group is entirely illegal
on SH-2.

Gate: hand-derived unit vectors from the manual, 85 test
nodes and about 240 assertions, covering every instruction
group, delay slot legality including the slot illegal
exception, MAC.W and MAC.L accumulation, software division
through the DIV0S, DIV0U and DIV1 sequence matching Go
division for signed and unsigned cases, interrupt masking
and RTE return, NMI, misaligned access traps, and a
mid-program serialize round trip. No Tom Harte SH-2 corpus
exists to run; the gate is the manual vectors.

### bus: the Saturn memory map

Both SH-2 CPUs share one bus. The slave starts held; the
system API releases it. Bus arbitration between the two
CPUs is round robin and flat; no cycle-accurate contention
is modeled in this slice.

Map: BIOS window 512 KiB at 0x00000000 (reads return 0, no
Sega bytes), SMPC register page at 0x00100000 (stored
bytes), backup RAM 32 KiB at 0x00180000 with the documented
odd-byte addressing (data lives on odd byte offsets; even
offsets read 0xFF), Work RAM-L 1 MiB at 0x00200000, the
MINIT and SINIT inter-CPU windows at 0x01000000 and
0x01800000 (a 16-bit write raises the other CPU's FRT
input capture flag), A-bus CS0 and CS1 stub windows, the
CD block register block at CS2 0x05800000 (CR1 to CR4,
HIRQ and mask stored), sound CPU work RAM 512 KiB at
0x05A00000, SCSP registers, VDP1 VRAM 512 KiB, VDP1
framebuffer 256 KiB, VDP1 registers, VDP2 VRAM 512 KiB,
color RAM 4 KiB, VDP2 registers, SCU registers, and Work
RAM-H 1 MiB at 0x06000000 (mirrored, also reachable
cache-through at 0x26000000). Addresses are masked with
0x1FFFFFFF, so the cache and cache-through aliases land on
the same storage.

The SH7604 on-chip register window lives at 0xFFFFFE00
and up, selected per CPU by the bus: FRT, WDT, cache
control (CCR is stored; the cache is not simulated), INTC
interrupt controller fields, and the hardware divide unit
(DVSR, DVDNT, DVCR, VCRDIV, DVDNTH, DVDNTL at 0xFFFFFF00).
A 32-bit write to DVDNT runs a signed 32 by 32 division;
a write to DVDNTL runs a signed 64 by 32 division. Results
appear in DVDNT (quotient) and DVDNTH (remainder) with the
documented overflow flag behavior in DVCR: divide by zero
and quotient overflow set OVF and saturate. Division is
instant in this slice; the hardware latency is not
modeled.

Gate: 17 test functions, 120 assertions over the map, the
backup RAM byte rules, the inter-CPU capture flags, and
divide unit math for 32/32 and 64/32 including overflow,
divide by zero, and the INT_MIN edge cases.

### cd: disc images and Saturn metadata

User-supplied image loading only. The .cue and .bin
reader parses single-FILE cues (AUDIO, MODE1/2048,
MODE1/2352, MODE2/2352, MODE2/2336, MODE2/2324 tracks,
PREGAP and INDEX 00/01), exposes sector access by LBA and
MSF, and returns user data per track mode (2048 bytes for
Mode 1 and Mode 2 Form 1, 2324 for Mode 2 Form 2). The
.iso reader synthesizes raw sectors for Mode 1 images.
ISO9660: primary volume descriptor check, root directory
walk, and single-extent file reads. The Saturn IP header
parse validates the exact 16-byte SEGA SEGASATURN
signature and reads maker ID, product number, version,
release date, device info, area symbols, peripheral list,
title, and the master and slave stack addresses at 0xF0
and 0xF8.

Gate: 11 test functions, 121 assertions over cue parsing,
sector math, pregap handling, ISO9660 on a synthetic
image built inside the tests, and header parsing. All test
data is synthetic.

### system: deterministic machine skeleton

LoadDisc (.cue or .iso by extension) and LoadDiscImage
validate the Saturn header before accepting a disc.
RunFrame spends a 477,273 cycle budget per CPU (28.6364
MHz at 60 frames per second, truncated) stepping the
master, and the slave too once released. Framebuffer()
returns a blank 320 by 224 XRGB8888 buffer in this slice.
SetButtons stores the standard digital pad bitmask
(peripheral ID 0x02 convention). Serialize and
Deserialize use a versioned 2,130,132 byte state covering
both CPUs, both work RAMs, and the backup RAM; disc
contents are not part of the state.

Gate: synthetic SH-2 programs on master and slave
communicating through Work RAM-H, a mid-run save state
round trip with byte-identical continuation, header
validation, and bad-state rejection.

## Slice roadmap

- SAT-2: VDP2 backgrounds plus the VDP1 sprite and
  framebuffer pipeline.
- SAT-3: SCU DSP, SCSP audio with its 68EC000, full SMPC
  command handling, and real CD block command processing.
- SAT-4: BIOS HLE boot, libretro shell, real-game engine
  test, and app wiring for the android-64bit build.

## Honesty notes

- No BIOS bytes anywhere; HLE boot arrives in SAT-4.
- The SH-2 caches are not simulated; CCR is stored only.
- Bus arbitration is round robin and flat.
- The divide unit answers instantly; its hardware
  latency is not modeled.
- The CD block register block is mirrored across its
  window in this slice; real hardware places it at a
  fixed offset inside CS2.
- SMPC, SCSP, VDP1, VDP2, and SCU register pages are
  storage only in this slice; behavior lands in SAT-2
  and SAT-3.
- The SH-2 instruction set is implemented strictly per
  the SH-2 manual, not the wider SH-4 set.
