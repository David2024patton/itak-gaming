# go-snes

A from-scratch Super Nintendo Entertainment System core in Go for
iTaK Gaming, built the same way as the go-genesis, go-gameboy, and
go-nes cores in this repository: every chip is our own code, written
from published hardware documentation, proven against public
conformance corpora kept as local evidence only, and wrapped in a
libretro-compatible shell for the app. No third-party Go
dependencies.

## Status

**SNES-4 (the final slice): shipped. This core is
COMPLETE.**

- `libretro`: the cgo c-shared shell over the `system`
  package (library name "iTaK GoSNES", extensions
  sfc|smc, 256x224 XRGB8888, 60.0988 fps, 32000 Hz
  stereo, serialize passthrough to the machine state,
  SAVE_RAM and SYSTEM_RAM memory interfaces, joypad
  bits mapped 1:1). The desktop shell was driven through
  the iTaK engine bridge against Super Mario World as
  the ship gate: non-black pixel counts of 57,288 /
  53,601 / 53,534 / 53,259 at frames 120 / 400 / 600 /
  1200 (logo, title, and the full attract demo stay
  visible), audio non-silent from the title onward
  (34,084 non-silent samples in frames 301-400 alone),
  a 267,085-byte save state taken at a demo frame whose
  restore is byte-identical and whose 120-frame
  continuation matches the uninterrupted run in frame
  hash and in every audio sample.
- The road here caught one real bug worth recording:
  the SNES-4a black screen. SMW booted, showed its
  title, then faded to permanent black with the music
  still playing. Root cause was a PPU register decode
  bug, not NMI, joypad, DMA, or APU (all cleared by
  traces): `ppu/render.go` read the CGWSEL ($2130)
  fields from the wrong bit positions, so SMW's $12
  was misread as clip-main-inside-window and its
  full-width HDMA window color-mathed every pixel to
  black. The fix follows the documented $2130 layout
  (clip bits 7-6, sub transparent bits 5-4, addend
  bit 1, direct color bit 0; force-blacked pixels still
  get color math; halving divides before clamp and is
  suppressed on blacked and fallback pixels).
  Permanent gates live in `system/smw_gate_test.go`
  (they skip when the ROM is absent): the attract demo
  stays above 50,000 non-black pixels at frames 120 /
  320 / 450 / 600 / 1200 with two-run determinism, B
  exits the demo to file select, A and Start reach the
  overworld, and a save at the overworld restores with
  a byte-identical 30-frame continuation in frame hash
  and audio. Post-fix demo counts run 57,288 / 56,065 /
  53,601 / 53,534 / 53,259 against the reference core's
  53,300-53,700 band.
- Shipped as iTaK Gaming build 52 (version 1.0.52,
  published 2026-10-13): the app's SNES slot now loads
  `libgo_snes.so` (armeabi-v7a, Android 24, NDK r26b,
  the same target as the other Go cores) and Snes9x is
  retired from the SNES slot and no longer bundled.
  This is the fourth system running 100 percent on our
  own from-scratch cores, after Genesis (build 42),
  Game Boy and Game Boy Color (build 50), and NES
  (build 51). Host desktop frame cost for SMW measured
  13.055 ms per frame in the SNES-4 stage runs, inside
  the 16.68 ms NTSC budget; on-device play is David's
  TV check, as with builds 50 and 51.
- Known minor items (observed against the reference
  core during SNES-4a, none blocking): the title iris
  entrance phase timing differs slightly; some scenes
  show small sampled color deltas versus the reference;
  and Start pressed during the attract demo does not
  exit the demo on this core. That last one is
  consistent with SMW's own ROM code, whose demo-exit
  button mask excludes Start (Start works normally in
  the title, file select, and overworld flow); the
  reference core answers Start during the demo anyway,
  which remains unexplained.

**SNES-3: the audio unit: SPC700, SMP, and
S-DSP.**

- `chips/spc700`: the complete SPC700 audio CPU: all 256
  opcodes with published cycle counts, the direct-page
  base from the P flag, the page-1 stack, MOVW word
  operations, the bit operations (SET1/CLR1, TSET1/TCLR1,
  BBS/BBC, MOV1, AND1/OR1/EOR1/NOT1), CBNE/DBNZ loops,
  MUL and the documented DIV behavior (overflow flag,
  half-carry rule, divide by zero), DAA/DAS, TCALL, PCALL,
  BRK, STOP/SLEEP, and full serialization. Bus access
  order is modeled (dummy fetches, interleaved
  read-modify-write, the always-performed bit-op read)
  because the conformance corpus traces it. Conformance:
  the Tom Harte SPC700 single-step corpus (256 files,
  1,000 tests each, 256,000 tests) passes 100%: every
  register including PSW, every listed final RAM cell,
  the exact cycle count, and the data-carrying bus access
  sequence per test. The corpus is local evidence only,
  under `testroms/tomharte-spc700/`.
- `apu`: the SMP and the S-DSP. 64 KB of APU RAM, the
  documented 64-byte IPL ROM running the real boot
  handshake (0xAA/0xBB on the ports) and the block
  transfer protocol, the $00F0-$00FF IO window (CONTROL
  with timer enables, port clear strobes, and the IPL
  ROM enable; the DSP address/data latch with the bit-7
  write lockout; both communication port directions;
  RAM-backed aux ports; the three timers with 8000 Hz
  and 64000 Hz stage rates and read-clear 4-bit output
  counters), and the DSP stepped once per output sample
  at 32000 Hz: BRR sample decoding (all four filters,
  the range 13-15 sign collapse, end and loop handling
  with ENDX), Gaussian interpolation from the published
  512-entry table, ADSR and GAIN envelopes from the
  published 32-entry rate table, pitch modulation from
  the previous voice's output, the 15-bit noise LFSR,
  and the echo ring with its 8-tap FIR filter. Measured
  proofs, all derived from the documentation rather than
  another emulator: a looping BRR sine at pitch 0x385
  measures 439.00 Hz by zero crossings against 440.06
  expected; timers count exactly 62 (T0, target 128) and
  640 (T2, target 100) increments in one simulated
  second; an echo impulse returns at exactly 512
  samples for EDL 1; and a hand-assembled tone program
  uploaded through the real IPL protocol posts its
  magic on the ports and plays.
- `system`: the APU is wired into the machine.
  $2140-$2143 read and write the real ports, the APU
  advances by exact ratio accumulation against the
  master clock (see the SNES-3 notes), and
  `AudioBuffer()` drains interleaved stereo samples at
  32000 Hz (about 534 per frame). Save states are
  version 2 and carry the full APU (RAM, SPC state,
  SMP timers and latches, DSP registers and per-voice
  state, echo position, and the clock ratio remainder):
  273,229 bytes for a cartridge with 8 KB of SRAM. The
  machine gate drives the IPL upload from a synthetic
  65816 program through the actual ports and continues
  31,920 audio values bit-identically across a
  mid-frame save and restore.

**SNES-2: PPU, DMA/HDMA, and the real machine.**

- `ppu`: the full PPU register file ($2100-$213F) and renderer.
  VRAM (64K words) with the VMAIN increment and rotate
  translations, the per-half prefetch read buffer (the first read
  after an address change returns the stale half; a $2139 read
  refills only the low half, a $213A read only the high half),
  CGRAM (256 colors, pair latch, writes landing in the active
  display area dropped), OAM (544 bytes, buffered pair writes),
  the shared-latch scroll registers, the Mode 7 registers with
  the $2134-$2136 multiply results, the $2137 counter latch, and
  $213E/$213F status. Rendering covers background modes 0-7
  with 8/16-pixel tiles, 2/4/8 bpp planar decode, per-layer
  tilemap sizes, offset-per-tile (modes 2, 4, 6; horizontal and
  vertical for mode 2, mode 4's per-column H-or-V entries, mode
  6 horizontal and vertical), Mode 7 affine with the documented
  26-bit product wrap and all four screen-over behaviors plus
  EXTBG, sprites in all eight size pairs with the 32-per-line
  and 34-tile limits and range/time over flags, OAM priority
  rotation, mosaic, both window channels with per-layer masks
  and the color window, color math (add/subtract, halved
  addend, sub screen or fixed color, clip-to-black and
  prevent-math window modes), direct color for 8bpp layers,
  brightness and forced blank, and interlace/overscan/
  pseudo-hires flags. Output is always 256x224 XRGB8888.
- `system`: the real frame-clocked machine. Master-clock line
  timing (1364 clocks per line, 262 lines, pre-render line 0,
  vblank from line 225, the short line 0 on alternating frames,
  interlace 262/263 line alternation), the CPU-side registers
  ($4200-$421F: NMI flag, TIMEUP, HVBJOY, the H/V IRQ timer,
  the multiply/divide unit with its 8/16-cycle result latency,
  $420D MEMSEL gating the map's FastROM speed class), the WRAM
  port, 8 channels of general DMA (all 8 transfer modes, the
  documented B-register patterns, live address/count readback),
  HDMA (per-line table walking, direct and indirect modes,
  repeat lines, the $00 terminator, mid-frame enable starting
  at the next display line), joypads (auto-read at vblank with
  the busy window, manual serial ports, both pads), and the APU
  mailboxes as plain latches in this slice (SNES-3 makes them
  the real ports).
  Save states cover the whole machine (CPU, WRAM, SRAM, PPU
  memory and registers, DMA/HDMA channel state, position and
  frame counters): 207,237 bytes for a cartridge with 8 KB of
  SRAM.
- Proofs for SNES-2: hand-derived synthetic render probes per
  mode (exact pixel values, not emulator comparisons), DMA and
  HDMA unit suites (mode 1 into VRAM, mode 3's paired-register
  pattern into CGRAM, decrement and fixed addressing, HDMA
  direct/indirect/repeat/terminator), and a whole-machine
  determinism gate: a synthetic ROM that DMA-uploads tiles, a
  tilemap, and a palette, then runs an HDMA brightness-band
  effect under an NMI frame counter; state and framebuffer are
  bit-identical across a save/restore boundary taken
  mid-frame. The Peter Lemon (Krom) test ROMs run headlessly
  as report-only evidence (they expose no CPU-readable
  verdicts): all five fetched ROMs (8bpp tilemap, Mode 3
  mosaic, Mode 7 rotate/zoom, HDMA window, HDMA color
  gradient) render non-blank frames. The Krom gradient ROM is
  what pinned the DMA mode 3 B-pattern to (0,0,1,1).

**SNES-1: CPU, cartridge, and test rig.**

- `chips/w65c816`: a complete W65C816S: all 256 opcodes, emulation
  and native modes, the M/X width flags with their register and
  stack consequences, 24-bit banked addressing, the full interrupt
  model (RESET, NMI, IRQ, ABORT at the documented level, BRK/COP
  with signature byte), WAI/STP, MVN/MVP block moves with the
  hardware's interrupt-suspension shape, and save-state
  serialization in the family style. Cycle accounting runs through
  a bus interface that reports a memory speed (fast, slow, XSlow)
  per access, because SNES instruction timing depends on ROM
  speed; internal cycles are counted separately so the DMA and
  HDMA engines of later slices can interleave against them.
- Conformance: the Tom Harte SingleStepTests 65816 corpus (the
  public v1 JSON set: 512 files, 256 opcodes in emulation and
  native modes, 10,000 randomized tests each, 5,120,000 tests)
  passes 100% on this machine. The runner checks every register,
  the final contents of every touched RAM cell, the exact cycle
  count, and the full sequence of valid bus accesses (address,
  direction, and data) per test. The corpus is local evidence
  only: it lives under `testroms/` and is never committed.
- `cart`: SNES cartridge header parsing with the standard scoring
  heuristic (checksum and complement pairing, map-mode agreement,
  reset vector sanity, title plausibility) across the LoROM
  (0x7FC0), HiROM (0xFFC0), and ExHiROM (0x40FFC0) candidate
  locations; title decoding in the documented JIS X 0201 encoding;
  ROM and RAM size exponents; the LoROM, HiROM, ExLoROM, and
  ExHiROM layouts; and the console memory map (Work RAM and its
  low mirror, ROM windows per layout, the LoROM and HiROM SRAM
  windows, per-region access speeds). Games whose headers name a
  coprocessor this core does not implement (SA-1, SuperFX, the
  DSP family, S-DD1, SPC7110, Cx4, and the rest) are refused at
  load with an error naming the chip; they are never silently
  misrun. The IO region (0x2000-0x5FFF in the system banks) is a
  documented placeholder in this slice: reads return an open-bus
  byte, writes are dropped, and the PPU, APU, and DMA slices
  replace it without changing the map's shape.
- `system` (SNES-1 form): CPU plus cartridge map, `RunCycles`,
  and a hand-assembled LoROM smoke program (mode switch, wide
  registers, long stores, a block move, and a long jump across
  banks) proving the parts integrate. SNES-2 replaced this rig
  with the real machine above, keeping its shape (exported
  CPU/Cart/Map, `New`, `LoadGame`, `Title`).
- `libretro`: a documented placeholder; the shell lands in
  SNES-4.

## Roadmap

- **SNES-1**: W65C816 CPU, cartridge and memory map, test rig.
  (Done.)
- **SNES-2**: the PPU (backgrounds, sprites, Mode 7), the console
  timing model, DMA and HDMA, controller IO, and the real IO
  region. (Done.)
- **SNES-3**: the SPC700 audio CPU and the S-DSP: the SNES sound
  engine, plus the APU IO handshake. (Done.)
- **SNES-4** (the last SNES slice): the libretro shell, a
  real-game engine test, and the app wiring swap (the app keeps
  shipping the incumbent core until this one passes the same
  proof, per the project rule). (Done: shipped as app
  build 52, Snes9x retired from the SNES slot.)

## SNES-2 rendering and timing model (the honesty notes)

- **Render model: per-scanline evaluation.** When the clock
  crosses into a new line, the machine renders the display line
  that just ended from the register and memory state as it
  stands then. Mid-line register changes (a scroll or palette
  write partway through a line) take effect from the following
  line, not partway across it. Full dot-level rendering is not
  modeled; raster effects that rewrite registers at a specific
  dot within a line will be off by up to one line's phase. This
  is the documented SNES-2 simplification.
- **Hi-res:** modes 5 and 6 and pseudo-hires (SETINI bit 4)
  render at 512 wide internally, then each output pixel is the
  average of its two sub-pixels after color math. The hardware's
  main/sub column relationship in hi-res (adjacent columns are
  main and sub screen, not two main samples) is not modeled.
- **Overscan:** the framebuffer stays 256x224. In 239-line
  mode the first 224 display lines land in the buffer and the
  remaining 15 are timed but not stored.
- **Interlace:** line counts alternate 263/262 and the field
  flag flips per frame; the half-line vertical offset of the
  second field is not applied to sampling.
- **Sprites:** sprite Y is the 0-based display line the sprite
  starts on. The 34-tile fetch budget counts tiles spanned by
  on-screen sprites; a sprite whose fetch would start past the
  budget is dropped whole.
- **Mode 4 and 6:** BG2 is the offset-per-tile source and is
  not itself displayed (its pixels exist only as the offset
  table), matching the common reading of the mode.
- **Color math halving** applies to the addend (sub screen or
  fixed color), per the CGADSUB bit 6 description followed
  here.
- **DMA timing:** a flat 8 clocks per channel activation plus
  8 per byte, charged when the triggering instruction
  completes; the transfer itself is instantaneous between
  instructions, so a DMA that crosses a line boundary delivers
  all of its bytes before that line's HDMA runs. Per-byte bus
  speed classes during DMA are not modeled.
- **HDMA timing:** transfers are charged 8 + 8 per unit byte
  and run at the line boundary; HDMA for a line executes even
  if a general DMA spanned that boundary (hardware suppresses
  it). HDMA register writes made mid-line apply at the next
  line boundary.
- **Multiply/divide:** results are computed at the write and
  published after 48 (multiply) or 96 (divide) master clocks;
  reads before that return the previously completed results,
  where the hardware exposes intermediate shift state.
- **Counters:** $2137 latches the H/V counters at the dot and
  line the machine has reached at instruction granularity, not
  at the exact bus cycle of the read.
- **NMI:** modeled as the $4210 flag ANDed with the $4200
  enable, presented as a level to the CPU's edge-latched NMI
  input, so enabling NMI during vblank with the flag set fires
  an NMI, as on hardware.
- **APU ports:** live since SNES-3; see the SNES-3 audio
  model notes below.

## SNES-3 audio model (the honesty notes)

- **Clock ratio:** the SPC700 runs at exactly 1.024 MHz
  against the 21.477272 MHz NTSC master clock, accumulated
  as master clocks x 1024000 / 21477272 with the remainder
  carried between calls and stored in save states, so the
  long-term rate and the restore phase are exact. Real SPC
  oscillators sit near, not on, 1.024 MHz; the documented
  fixed rate is used.
- **Sample-stepped DSP:** the DSP evaluates one output
  sample per 32 SPC cycles. SPC writes to DSP registers
  take effect at sample granularity, not at the exact SPC
  cycle of the write. Everything inside a sample (BRR
  decode, envelopes, pitch modulation, noise, echo) is
  modeled; sub-sample register timing is not.
- **KON delay:** a keyed-on voice runs its sample position
  and envelope from the key-on sample but outputs silence
  for the first 5 samples, the documented delay figure.
- **Gaussian rounding:** the four interpolation taps are
  summed and shifted once (>> 11). The fullsnes text
  applies a per-tap double shift instead, so interpolated
  samples can differ by per-tap rounding (a few LSBs at
  most).
- **GAIN ramp steps:** linear decrease subtracts 8 per
  step, linear increase adds 0x20 per step, and bent
  increase adds 0x20 below 0x600 and 0x10 at or above it.
  The fullsnes GAIN text gives different step sizes (a
  linear decrease of 32 per step among them). The values
  here follow this slice's build specification; the
  divergence is flagged for a later accuracy pass.
- **TEST register ($00F0):** writes are stored and reads
  return 0, but no bit is acted on. The documented effects
  (timer disables, RAM write protection, a CPU crash bit,
  added waitstates) are all ignored, matching the
  documentation's own guidance that software leaves this
  register at its 0x0A default.
- **Timers:** a CONTROL write that changes a timer's
  enable bit reloads that timer's stage divider; disabling
  a timer also clears its output counter.
- **ENDX:** a voice's bit is set when decoding of an end
  block starts, per the fullsnes note; any write to $7C
  clears all bits, and a looping end block leaves its bit
  set until then.
- **Echo at EDL 0** uses a one-frame ring: the hardware's
  zero-length buffer reads and writes a single address,
  and a one-frame ring reproduces that addressing while
  still advancing.
- **DSP register latches:** writes to DSP addresses with
  bit 7 set are ignored, as documented; ENDX, ENVX, and
  OUTX read back live state.

Coprocessor chips (SA-1, SuperFX, DSP-1 and family, and the rest)
are out of scope until the base machine is proven end to end.
Games needing them are detected from the header and named
unsupported at load.

## Conformance notes (sharp edges the corpus pinned down)

The corpus is generated from a reference implementation, and a
few of its behaviors are more specific than the datasheet
prose. This core matches the corpus; the notable cases:

- Emulation-mode stack: the pointer is a plain counter folded to
  0x0100|(low byte) whenever a state is installed or emulation is
  entered, and the exported S always reads 0x0100|(low byte).
  Plain pushes and 16-bit pulls move it without masking (a PHD
  from a low byte of 0x00 writes at 0x0100 then 0x00FF; a PLD
  from 0xFE reads at 0x01FF then 0x0200), while single-byte
  pulls (PLA, PLP, PLX, PLY, PLB), RTS, and RTI wrap the
  increment inside page 1, and interrupt entry (BRK, COP, NMI,
  IRQ, ABORT) and JSR address the stack in its page-1 form on
  every access. Real software never runs its emulation stack
  across the page-1 boundary, so the split is observable only
  here.
- The emulation status register always reads with the M and X
  bits set.
- Indexed read fixup (dummy) cycles: charged on a page change,
  and always when the index registers are 16-bit. Dummy cycles
  make no bus access in this core's accounting, matching the
  corpus's pin traces; the same applies to the dead cycles in
  indexed stores, read-modify-write fixups, the (SR,S),Y
  addition (which is unconditional and spent at the pointer's
  high-byte address), and WDM's skipped second byte.
- Indexed data addressing is a true 24-bit addition against
  the data bank: a 16-bit overflow carries into DBR+1, and a
  16-bit data access at 0xFFFF takes its high byte from the
  next bank. The same access in bank 0 (direct page, stack
  relative) wraps its high byte inside bank 0 instead.
- Direct page pointers: the pointer's base addition follows
  the emulation page-wrap rules; the following bytes are plain
  increments from the base, except the [dp],Y bank byte, whose
  fetch wraps with the offset (the two [dp] families differ at
  exactly this byte in the corpus's sparse boundary cases),
  and PEI, whose pointer's high byte wraps with the offset
  too.
- JMP (abs,X) and JSR (abs,X) read their pointer tables from
  the program bank, where JMP [abs] reads its pointer from
  bank 0. JSR (abs,X) pushes only the 16-bit return address
  (the high operand byte's address), interleaved between the
  two operand fetches.
- MVN and MVP run as identical seven-cycle iterations that
  re-fetch the opcode and both operands, copy one byte, and
  spend two dead cycles at the write address; DBR takes the
  destination bank as the move runs. The corpus records at
  most 100 cycles of a move and stops mid-iteration, so its
  final states are truncations; the gate cuts the CPU at
  exactly the recorded cycle (a harness-only cycle budget)
  to compare them.
- REP and SEP cost three cycles, not two. STP, like WAI, is
  modeled as the fetch plus three internal cycles. TDC flags
  its result exactly like TSC does.
- Read-modify-write write-back: in 8-bit mode the original
  value is driven back out first (modeled as a dummy write:
  the byte changes, but the access is not a counted data
  transfer), then the modified value; in 16-bit mode the
  modify costs an internal cycle and the new value is written
  high byte first.
- Decimal ADC and SBC cost no extra cycle. Decimal ADC's
  overflow flag comes from the intermediate sum after the low
  nibble's adjust and before the high nibble's, the 65C02
  family behavior, and the nibble carry is a plain boolean:
  0x5F + 0x2C in decimal is 0x81 with no carry.
- Branches pay the page-cross cycle only in emulation mode.
- TSC sets N and Z from the transferred value; TCS, TDC, and
  TXS set no flags.
- JMP (abs) does not wrap its pointer fetch at a page
  boundary on this core.
- ABORT is recognized at an instruction boundary, pushing the
  address of the instruction that was about to execute (the
  datasheet's precise abort point is mid-instruction and the
  SNES never asserts the pin); the approximation is documented
  in the CPU package.
- WAI is modeled as the corpus models it: the instruction
  itself is the fetch plus three internal cycles, and the wait
  proper continues one internal cycle per step until a wake
  input asserts.

## Layout

```text
chips/w65c816/   the CPU, its conformance gate, and its tests
chips/spc700/    the SPC700 audio CPU, its gate, and its tests
cart/            header parsing and the console memory map
ppu/             the PPU: registers, memory, and the renderer
apu/             the SMP (IO, timers, IPL) and the S-DSP
system/          the whole machine: timing, DMA/HDMA, IO
libretro/        the cgo libretro shell (the shipped core)
testroms/        local-only conformance corpora (gitignored)
```

## Building and testing

```sh
GOTOOLCHAIN=auto GOPROXY=off PATH=$HOME/goroot/go/bin:$PATH go test ./...
```

The conformance gates skip themselves when a corpus is
absent. To fetch the corpora, clone TomHarte/ProcessorTests
and copy `65816/v1/*.json` into `testroms/tomharte/` (about
3 GB) and `spc700/v1/*.json` into `testroms/tomharte-spc700/`
(about 81 MB).
