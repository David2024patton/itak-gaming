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
framebuffer (two 256 KiB banks since SAT-2; the window
maps the current draw bank), VDP1 registers, VDP2 VRAM 512 KiB,
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

## What exists in SAT-2

### vdp2: the background and composition engine

Built from the VDP2 user's manual (ST-58). The package
renders per scanline into the system framebuffer as
XRGB8888 and owns no memory of its own: VRAM (512 KiB),
CRAM (4 KiB), and the 512 byte register page are shared
slices handed in by the bus, so CPU writes take effect
immediately.

Coverage: TVMD display control with every resolution
mode decoded to its pixel dimensions (widths 320, 352,
640, 704; heights 224, 240, 256; interlace doubling to
448, 480, 512; the exclusive monitor modes at their
fixed 480 line height and 525 or 561 total lines).
TVSTAT readback (VBLANK, HBLANK, ODD field) and VCNT are
maintained by the system timing calls. All five scroll
layers (NBG0 to NBG3, RBG0, RBG1) in cell and bitmap
modes: character color modes 16, 256, 2048 palette plus
RGB 5.5.5 and RGB 8.8.8, 1x1 and 2x2 characters, 1 word
and 2 word pattern name data with every character
number supplement mode, plane sizes, and the full map
register set. Scrolling: integer and fractional screen
scroll, per line scroll tables for X, Y, and zoom at
every documented line density, vertical cell scroll,
zoom with the ZMCTL reductions (1/2, 1/4, and the
quarter step modes), and mosaic. Rotation: both
parameter sets read from VRAM with the full documented
parameter set (screen start, per line and per dot
increments, display point, center point, view point,
coefficients kx and ky from registers or the
coefficient table in VRAM or CRAM), RPMD parameter
switching modes, and all four screen over modes.
Priorities: per layer registers, sprite priorities
PRISA to PRISD for all 16 sprite types with the field
positions decoded per type, and special priority and
special color calculation from pattern supplementary
bits or per dot code matches (SFSEL and SFCODE). Color
calculation: per layer enable and 5 bit ratios in 1/32
steps, both ratio selection modes, addition mode,
extended color calculation blending the third image,
and the back screen ratio path. Color offset registers
A and B applied after color calculation. Windows: W0
and W1 rectangles, the line window table, inside and
outside areas, per layer logic, the sprite window, and
the rotation parameter window. Back screen in single
color and per line table forms, the line color screen
with per layer insertion enables, and shadow sprites
halving the scroll color under them. CRAM reads honor
all four RAMCTL modes, including mode 2 and 3 where
RGB888 longs are stored split across word pairs.

Gate: 15 test functions of hand-derived pixel probes:
NBG0 cell mode with scroll offset in both pattern name
formats, line scroll shifting one line by exactly the
table value, vertical cell scroll, RBG0 identity and
half coefficient sampling, frame buffer rotation
coordinates, register versus special priority at one
pixel, an exact half ratio color blend (127, 0, 127),
window outside area, the CRAM mode 2 round trip, back
screen versus line color across a boundary line, sprite
composition including the priority 0 transparency rule,
zoom and ZMHF, and the display size and total line
tables.

### vdp1: the sprite and polygon engine

Built from the VDP1 user's manual (ST-013). The package
owns the command processor and draws into one of two
256 KiB frame buffer banks; the system CPU window maps
only the current draw bank. Command tables are read
from VRAM at 32 bytes per command and walked until an
END command, honoring all eight jump modes (next,
assign, call, return, and the four skip forms) with a
call stack. Commands: system clipping, user clipping,
local coordinate set, normal sprite, scaled sprite with
all documented zoom point positions, distorted sprite,
polygon, polyline, and line. Drawing attributes per the
manual: texture color modes 0 to 5 (16, CLUT 16, 64,
128, and 256 color palette banks with the documented
bit stream fetches and end codes, plus direct RGB),
gouraud shading from the VRAM table interpolated per
pixel, mesh checkerboard skip by frame buffer
coordinate parity, half transparent blending, shadow
writes, color calculation modes, ECD and SPD bits, user
clipping with inside and outside modes, and pre
clipping. Frame change follows FBCR at each frame
boundary: 1-cycle mode swaps banks, erases the new draw
bank through EWDR, EWLR, and EWRR, and auto starts
drawing when PTMR selects it; manual erase and manual
change modes behave per the manual, including the FCT
bit clearing itself. PTMR mode 1 draws on a 0 to 1
trigger transition polled once per line. TVMR selects
the 16 bit and 8 bit frame buffer formats and the frame
buffer rotation read path, which asks VDP2 for rotated
coordinates. LOPR, COPR, and MODR readback are written
into the register page. The finished bank is exposed to
VDP2 as the sprite source: each pixel supplies its
color word plus, through the VDP2 sprite decode, its
priority and color calculation fields.

Gate: 18 test functions over synthetic command lists:
clipping and local coordinates with a scaled sprite,
distorted sprite corner sampling at a hand-computed
probe, polygon span fill, the gouraud midpoint value,
mesh parity, half transparent blending against previous
frame buffer content, user clipping inside and outside,
both frame change modes with erase fill values, jump
assign and call with return, sprite flips, CLUT lookup,
end codes with ECD on and off, and status readback.

### scu: interrupt status and mask

A thin view over the SCU register page (no shadow copy)
with real IMS and IST words. Wired sources: VBlank-IN,
VBlank-OUT, HBlank-IN, and the VDP1 sprite draw end,
each with its documented vector (0x40 up through 0x4D).
Delivery picks the lowest set unmasked status bit,
which is the documented SCU priority order, and the
system offers one delivery per scanline to the master
SH-2 at level 6, the documented default level for
these sources. Timer, DSP end, sound request, SMPC, pad,
and DMA end sources remain status storage only until
SAT-3.

Gate: unit tests for raise and pending, mask blocking
delivery while status still records, priority order
delivery with software clearing, and the vector table;
system tests prove an SH-2 handler runs through the
bus on delivery and stays silent when masked.

### system: scanline timing and version 2 states

RunFrame now advances scanline by scanline: VDP1 frame
change at the top, then per line the VDP2 timing state,
the SCU raises (VBlank-OUT at line 0, HBlank-IN every
line, VBlank-IN at the first blanking line, which is
line 224 in the default mode), one interrupt delivery,
the CPU slice, the VDP1 draw trigger poll, and the
scanline render. The 477,273 cycle per CPU budget is
split by lines: every line gets CyclesPerFrame divided
by TotalLines, and the first remainder lines of the
frame get one extra cycle (at 263 lines: base 1814,
191 lines get 1815). States are version 2 and
3,708,004 bytes, adding VDP2 VRAM, CRAM, and registers,
VDP1 VRAM, both frame buffer banks, and registers, and
the SCU register page.

Gate: the SAT-1 gates plus a dual CPU determinism gate
where the master programs a VDP2 layer and a VDP1
command list through the bus, a mid-run save is
restored into a fresh machine, and the continuation
state bytes and framebuffer pixels are identical;
pixel probes check the NBG0 pattern colors, sprite
over background priority, and background beside the
sprite.

## Slice roadmap

- SAT-2: done. VDP2 backgrounds plus the VDP1 sprite
  and polygon engine, composition, and SCU interrupt
  wiring.
- SAT-3: SCU including its DSP, SCSP audio with its
  68EC000, real CD block command processing, and SMPC
  behavior.
- SAT-4: BIOS HLE boot, libretro shell, real-game
  engine test, and app wiring for the android-64bit
  build.

## Honesty notes

- No BIOS bytes anywhere; HLE boot arrives in SAT-4.
- The SH-2 caches are not simulated; CCR is stored only.
- Bus arbitration is round robin and flat.
- The divide unit answers instantly; its hardware
  latency is not modeled.
- The CD block register block is mirrored across its
  window in this slice; real hardware places it at a
  fixed offset inside CS2.
- SMPC and SCSP register pages are still storage only;
  behavior lands in SAT-3. The SCU page has real
  interrupt status and mask only; its DSP and DMA
  engines land in SAT-3.
- The SH-2 instruction set is implemented strictly per
  the SH-2 manual, not the wider SH-4 set.

### SAT-2 approximations, each documented in code

- Hi-res and exclusive monitor modes are decoded to
  their pixel dimensions only; the doubled pixel clock
  timing of those modes is not modeled (vdp2 DisplaySize
  and TotalLines).
- VDP1 drawing is instantaneous at the frame change or
  trigger poll, not spread across the frame; EDSR draw
  end is reported at the poll, not at a cycle offset.
- The SCU delivers at most one interrupt source per
  scanline, to the master only, always at level 6.
- Pre-clipping: when a part exceeds the system clip
  with Pclp clear, the effective clip shrinks by one
  dot on the right and bottom only (vdp1 commands.go).
- Gouraud shading does not apply to palette bank sprite
  data or to CLUT entries with bit 15 set (vdp1
  commands.go).
- Color calculation mode 3, prohibited on hardware, is
  treated as replace (vdp1 commands.go).
- The HSS bit is a no-op, and the distorted sprite
  samples its corner texture coordinates clamped to the
  pattern edge (vdp1 commands.go).
- Line drawing steps by the longer axis with integer
  interpolation instead of hardware edge walking (vdp1
  commands.go). Span texture interpolation uses 16 bit
  fraction fixed point (vdp1 commands.go).
- MODR readback is a composed value from TVMR, FBCR,
  and PTMR rather than the hardware status mix (vdp1
  vdp1.go).
- In 8 bit frame buffer mode, color calculation runs on
  the 16 bit value and only the low byte is stored
  (vdp1 vdp1.go).
- Mosaic evaluates the whole layer fetch at the snapped
  sample coordinates, including that position's scroll
  state (vdp2 layers.go and rotation.go).
- Rotation bitmap sizes use the single BMSZ bit in
  CHCTLB, so sizes 0 and 1 only (vdp2 rotation.go).
- RPMD mode 2 probes the coefficient entry of set A at
  dot 0 of the line to pick the line's set (vdp2
  rotation.go). The over pattern reuses the single
  OVPN word as both words of the pattern entry (vdp2
  rotation.go). Screen over mode 3 is treated as
  transparent like mode 2 (vdp2 rotation.go).
- The coefficient KLCE line color path is not
  implemented (vdp2 rotation.go).
- Special priority and special CC mode 3, prohibited on
  hardware, are treated as mode 0 (vdp2 vdp2.go).
- Sprite color calculation permission (SPCCCS) is
  resolved by direct comparison of the sprite priority
  number with the CC number (vdp2 sprite.go). 8 bit
  sprite words are treated as palette dots even when
  SPCLMD selects RGB (vdp2 sprite.go).
- RBG0 and RBG1 share the single CHCTLB and PNCR
  character control block and one window control byte,
  as the register file provides only one (vdp2
  rotation.go and windows.go).
- With the ZMCTL reduction bits clear, the zoom
  increment comes from the ZM registers exactly as
  programmed; an increment of 0 freezes sampling, as
  the manual's register definition implies (vdp2
  layers.go).
- Extended background (EXBG) input is not implemented;
  NBG0 and NBG1 render independently. Gradation
  calculation is not implemented. MSB shadow is not
  implemented (vdp2 vdp2.go).
- The VDP1 register page returns stored values on read;
  hardware returns real values only for LOPR, COPR, and
  MODR and garbage elsewhere.
