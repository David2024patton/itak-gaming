# go-ps1

A from-scratch PlayStation (PS1) core in Go for iTaK
Gaming, built the same way as the go-genesis,
go-gameboy, go-nes and go-snes cores in this
repository: every chip is our own code, written from
published hardware documentation (the MIPS I manuals
and the psx-spx hardware notes), proven against test
evidence kept as local files only, and wrapped in a
libretro-compatible shell for the app when it is
ready. No third-party Go dependencies. No Sony BIOS
and no game images are ever part of this repository.

## Status

**PS1-1: the CPU, the memory map, the CD image
foundations and the machine skeleton are built. No
GPU rendering, no SPU audio and no disc boot yet.**

- `chips/r3000a`: the MIPS I R3000A CPU. All integer
  instructions: ALU and shift families, the
  multiply/divide HI/LO unit, loads and stores
  including the unaligned LWL/LWR/SWL/SWR merge
  semantics (little-endian shift tables), branches
  and jumps, SYSCALL and BREAK, and the CP0 register
  set this chip exposes (SR, Cause, EPC, BadVAddr,
  PRId reading the documented 0x00000002). Exceptions
  cover reset, interrupt, syscall, breakpoint,
  overflow, reserved instruction, coprocessor
  unusable (COP1 and COP3 are absent on the PSX and
  raise it, with the CE field set), and address error
  on fetch, load and store with BadVAddr and the
  EPC/BD rules. The pipeline behaviors that define
  the chip are modeled and pinned by tests:
  - Load delay: a load lands after the following
    instruction executes. A delay-slot instruction
    that writes the load's destination with an
    ordinary write (an ALU result, a link address)
    cancels the pending load, per the documented
    R3000 rule; a second load to the same register
    does not cancel it (the first lands on schedule
    and the second takes its place), and LWL/LWR
    merges in the delay slot read the pending value,
    which is how unaligned pairs compose.
  - Branch delay: the delay slot always executes;
    a fault there reports EPC at the branch with
    Cause.BD set. BLTZAL and BGEZAL write the link
    register whether or not the branch is taken,
    as the R3000A does.
  - Multiply/divide: results are computed at issue;
    the HI/LO unit stays busy for the documented
    counts (12 cycles multiply, 36 divide) and an
    early MFHI/MFLO/MTHI/MTLO stalls for the
    remainder. Divide by zero returns the documented
    R3000A results (unsigned: quotient all-ones,
    remainder the dividend; signed: quotient -1 or
    +1 by dividend sign), and INT_MIN / -1 returns
    the dividend with remainder zero, no trap.
  - Cache control: with SR.Isc set, data accesses
    go to an internal isolation store and never
    reach the bus; with SR.SwC also set, fetches
    come from that store. Cache lines, tags and
    writeback are not modeled (see below).
- `chips/gte`: the GTE (coprocessor 2) register file
  only: 32 data and 32 control registers with plain
  storage, plus the documented command dispatch
  table. Every command returns a not-implemented
  error and changes no state; no GTE math is faked.
  The fixed-point implementation is PS1-3.
- `bus`: the memory map. 2 MB of RAM mirrored across
  its 8 MB window in KUSEG, KSEG0 and KSEG1; the
  1 KB scratchpad at 0x1F800000; the hardware
  register page 0x1F801000-0x1F802FFF as stored stubs
  (peripherals, timers, DMA channel registers, the
  CD controller, GPU and SPU windows; no register
  has behavior yet, and DMA transfers are not
  executed); expansion space reading all-ones open
  bus; the cache control register at 0xFFFE0130
  stored; and RAM_SIZE (0x1F801060) resetting to its
  documented 2 MB value.
- `cd`: user-supplied disc images, parsed and read,
  never stored here. `.cue`/`.bin` with the usual
  track modes (Mode 1 and Mode 2, raw and cooked,
  audio), stored pregaps (INDEX 00) and generated
  pregaps (PREGAP, read as silence), and flat
  `.iso`/`.img` files. Sectors are served raw (2352)
  or as user data (2048) with MSF/LBA conversion.
  A minimal ISO9660 reader (primary volume
  descriptor, directory walk, file read) sits on top
  and is proven against a synthetic ISO built inside
  its test.
- `system`: the machine in family shape. `LoadDisc`
  takes a `cd.Reader` (or `OpenDisc` a path), resets,
  and holds the image. `RunFrame` spends the NTSC
  frame budget of 33,868,800 / 59.94 cycles on the
  CPU, accumulated as an exact rational so frames
  alternate 565,045 and 565,046 cycles. `Framebuffer`
  returns a blank 640x480 XRGB8888 buffer (the PS1's
  maximum mode; real modes arrive with the GPU).
  `SetButtons` stores the digital pad bitmask.
  Save states are version 1: the CPU state, main
  RAM and the scratchpad, 2,102,453 bytes, proven by
  a synthetic MIPS checksum program that survives a
  mid-run save with a byte-identical continuation.

## The BIOS approach

There is no Sony BIOS in this core and there never
will be. The shipped iTaK app already boots PS1
discs through an HLE BIOS path, and this core
follows the same rule: the BIOS window exists in the
memory map, its reads return zero words in this
slice (a NOP sled, so the reset vector at
0xBFC00000 executes harmlessly), and our own HLE
implementation of the BIOS calls arrives in a later
slice. User-supplied BIOS files are not read by
this core.

## Slice roadmap

- PS1-2: the GPU command processor and a software
  rasterizer, the three timers, DMA execution, and
  the interrupt controller.
- PS1-3: the SPU (24 voices, ADSR, reverb), the full
  GTE fixed-point implementation, the CD controller,
  BIOS HLE, and the real-game enginetest.
- PS1-4: the libretro shell, the Android build, and
  the app swap off PCSX ReARMed, gated on the same
  proof as the other cores (real games boot and
  play, save states byte-exact).

## Honesty notes for this slice

- Conformance: the TomHarte/ProcessorTests tree was
  checked for a MIPS or PSX single-step set; it has
  none (it carries 6502, 65816, 680x0, 8088, spc700
  and 65C02 variants). The presence-gated harness in
  `chips/r3000a/conformance_test.go` records that and
  skips. The CPU gate for PS1-1 is the unit suite in
  `chips/r3000a`: every instruction group, hand-worked
  unaligned merge vectors, explicit load-delay and
  cancellation cases, branch-delay and link cases,
  divide-by-zero and overflow vectors, and exception
  entry state.
- Cycle counting is instruction-level, not
  cycle-exact: most instructions cost 1, loads and
  stores 2, and the HI/LO busy model above. Cache
  hits, memory wait states and the instruction cache
  are not simulated.
- DMA, timers, interrupts from peripherals, the CD
  controller and the pad hardware are register
  storage only; the CPU interrupt line exists
  (`SetInterrupt`) and is unit-tested.
- The GTE register file is not part of the PS1-1
  save state; it joins the state with its math in
  PS1-3.
