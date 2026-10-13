# go-snes

A from-scratch Super Nintendo Entertainment System core in Go for
iTaK Gaming, built the same way as the go-genesis, go-gameboy, and
go-nes cores in this repository: every chip is our own code, written
from published hardware documentation, proven against public
conformance corpora kept as local evidence only, and wrapped in a
libretro-compatible shell for the app. No third-party Go
dependencies.

## Status

**SNES-1 (this slice): CPU, cartridge, and test rig.**

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
- `system`: the SNES-1 test rig only: CPU plus cartridge map,
  `RunCycles`, and a hand-assembled LoROM smoke program (mode
  switch, wide registers, long stores, a block move, and a long
  jump across banks) proving the parts integrate.
- `libretro`: a documented placeholder; the shell lands in
  SNES-4.

## Roadmap

- **SNES-1**: W65C816 CPU, cartridge and memory map, test rig.
  (Done.)
- **SNES-2**: the PPU (backgrounds, sprites, Mode 7), the console
  timing model, DMA and HDMA, controller IO, and the real IO
  region.
- **SNES-3**: the SPC700 audio CPU and the S-DSP: the SNES sound
  engine, plus the APU IO handshake.
- **SNES-4**: the libretro shell, the Android build, and the app
  wiring swap (the app keeps shipping the incumbent core until
  this one passes the same proof, per the project rule).

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
system/          the test rig tying CPU and cart together
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
