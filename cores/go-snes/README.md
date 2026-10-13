# go-snes

A from-scratch Super Nintendo Entertainment System core in Go for
iTaK Gaming, built the same way as the go-genesis, go-gameboy, and
go-nes cores in this repository: every chip is our own code, written
from published hardware documentation, proven against public
conformance corpora kept as local evidence only, and wrapped in a
libretro-compatible shell for the app. No third-party Go
dependencies.

## Status

**SNES-2 (this slice): PPU, DMA/HDMA, and the real machine.**

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
  mailboxes as plain latches (the SPC700 itself is SNES-3).
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
  engine, plus the APU IO handshake.
- **SNES-4**: the libretro shell, the Android build, and the app
  wiring swap (the app keeps shipping the incumbent core until
  this one passes the same proof, per the project rule).

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
- **APU ports:** $2140-$2143 reads return the power-on zero
  state (no SPC700 until SNES-3); games polling for the IPL
  handshake (0xAA/0xBB) will wait, which is expected this
  slice.

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
cart/            header parsing and the console memory map
ppu/             the PPU: registers, memory, and the renderer
system/          the whole machine: timing, DMA/HDMA, IO
libretro/        placeholder for the SNES-4 shell
testroms/        local-only conformance corpora (gitignored)
```

## Building and testing

```sh
GOTOOLCHAIN=auto GOPROXY=off PATH=$HOME/goroot/go/bin:$PATH go test ./...
```

The conformance gate skips itself when the corpus is absent.
To fetch the corpus, clone TomHarte/ProcessorTests and copy
`65816/v1/*.json` into `testroms/tomharte/` (about 3 GB).
