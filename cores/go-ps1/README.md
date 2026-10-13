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

**PS1-2: the GPU with a software rasterizer, display
timing, the three root counters, DMA execution, and
the interrupt controller are built and gated. PS1-1
(the CPU, memory map, CD image foundations and the
machine skeleton) is unchanged underneath and its
gates all still pass.**

- `chips/r3000a`: the MIPS I R3000A CPU, as in PS1-1.
  All integer instructions, the HI/LO unit, unaligned
  merge semantics, branch and load delay behavior
  (including the cancellation rule: only a non-load
  write cancels a pending load), CP0 with PRId reading
  0x00000002, and the exception model. Its external
  interrupt input (CP0 IP2) is driven by the PS1-2
  interrupt controller.
- `chips/gte`: the GTE register file only, as in
  PS1-1: plain storage, commands return
  not-implemented, no math faked. Full GTE is PS1-3.
- `bus`: the memory map from PS1-1 (2 MB RAM mirrored
  across its 8 MB window, 1 KB scratchpad, expansion
  open-bus reads, BIOS window returning zero words)
  plus one addition: an `IODevice` hook on the
  register page. With no device attached the page is
  the PS1-1 stored-stub page (the bus tests pin
  that). The system attaches its multiplexer, which
  claims the GPU, timer, DMA, CD, SPU-transfer and
  interrupt windows at their real access sizes;
  everything else in the page stays stored storage.
- `gpu`: the PSX GPU. The full GP0/GP1 command set
  per psx-spx: environment commands E1 to E6 (draw
  mode with texture page, semi-transparency mode,
  dither flag, draw-to-display, texture disable and
  the rectangle flip bits; texture window; drawing
  area; drawing offset; mask set/check), GP1 display
  control (reset, display enable, DMA direction,
  display area start, horizontal and vertical
  display ranges, display mode with horizontal
  resolutions 256/320/368/512/640, vertical 240/480,
  interlace, NTSC/PAL and 15/24-bit depth), and the
  GP1(10) internal register readback. Rendering is
  immediate per command into the 1024x512 16-bit
  VRAM: monochrome, gouraud and textured triangles
  and quads (quads split as triangles 1-2-3 and
  2-3-4, per the hardware rule), monochrome and
  gouraud lines and polylines with the documented
  0x50005000 terminator, rectangles (variable, 1x1,
  8x8, 16x16; raw and textured, with flips), the
  fill rectangle with its documented parameter
  masking, VRAM to VRAM copies, CPU to VRAM uploads
  and VRAM to CPU readback through GPUREAD, and the
  GP0(1F) interrupt request command. Textures sample
  4-bit CLUT, 8-bit CLUT and 15-bit direct formats
  with the texture page and window applied; texel
  0x0000 is transparent; modulation reproduces the
  v2 behavior in 8-bit space with 0x80 neutral.
  Semi-transparency implements all four modes with
  the documented integer arithmetic in 5-bit space,
  and for textured primitives blends only texels
  with the STP bit set (the STP bit is written into
  the drawn pixel's mask bit). Dithering applies the
  documented 4x4 matrix to gouraud and modulated
  output and to all lines, never to monochrome
  polygons or rectangles. GPUSTAT is maintained
  including the ready bits for the immediate model.
  `Framebuffer` converts the display area to
  XRGB8888 at native resolution. Pixel probes in
  `gpu/gpu_test.go` pin the scan-conversion edge
  rule (lower-right edges excluded; an 8x8 right
  triangle covers exactly 36 pixels), exact gouraud
  interpolation at a probed pixel, all four blending
  modes over a known backdrop, CLUT and direct
  sampling, the texture window transform, mask
  behavior, the dither matrix pattern, line and
  polyline endpoints, transfer readback, and the
  15-bit, 24-bit and interlaced-480 display paths.
- `timers`: root counters 0, 1 and 2 with the
  documented mode register (clock sources: timer 0
  system or dotclock, timer 1 system or hblank,
  timer 2 system or system/8; reset at target or at
  0xFFFF; IRQ at target and at 0xFFFF with one-shot
  versus repeat and pulse versus toggle behavior,
  the inverted INTF bit, the reached flags that
  clear on mode read, and the counter reset on mode
  write) and the synchronization modes (pause in
  blanking, reset at the pulse, reset and count only
  in blanking, wait for the first pulse; timer 2
  stop modes). Counters are fed from machine
  position (see `system`), so readback is exact:
  the unit gates pin free-run values, a 10-period
  target-reset run, divider arithmetic and each
  sync mode.
- `dma`: the DMA controller. DPCR (reset value
  0x07654321) and DICR with the documented rule that
  completion flags set only when the channel IRQ is
  enabled, the master flag on bit 31, and flag
  acknowledgement by writing ones. Channels 0 to 6
  with sync modes 0 (all words on start), 1 (blocks
  on the trigger bit) and 2 (GPU linked lists ending
  at next-address 0xFFFFFF; OTC building the
  documented reverse table, verified word for word
  against the psx-spx example), both directions,
  address step plus or minus 4, and chopping honored
  structurally (bursts of the programmed window
  size; the transfer still completes inside the
  trigger call). Wiring: channel 2 feeds GP0 and
  reads GPUREAD, channel 3 pulls words from the CD
  data FIFO, channel 4 moves words through the SPU
  transfer port, channels 0 and 1 reach the MDEC
  stub sink/source (the codec is PS1-3), channel 5
  (PIO) sinks and sources zeros, channel 6 builds
  its table in RAM.
- `spu`: the SPU as far as this slice needs it: the
  512 KB sound RAM and the data transfer port
  (address register at 0x1F801DA4 counting 4-byte
  units in this implementation; the data port at
  0x1F801DA6 advancing per halfword). Voices, ADSR
  and reverb are PS1-3; this package makes no sound.
- `cd`: the PS1-1 image layer (cue/bin and iso/img,
  ISO9660) plus the PS1-2 controller subset at
  0x1F801800-0x1F801803: the documented status,
  response and data FIFOs and IRQ enable/flag
  registers, and the command subset the DMA gate
  needs (Getstat 0x01, Setloc 0x02 with BCD
  parameters, ReadN 0x06 and ReadS 0x1B streaming
  sectors into the data FIFO with the data-ready
  IRQ, Pause 0x09, Setmode 0x0E; unknown commands
  answer with the error IRQ). The full controller
  is PS1-3.
- `system`: the wired machine. The register page is
  a real peripheral multiplexer (`mux.go`). The
  interrupt controller is real: I_STAT and I_MASK
  at 0x1F801070 and 0x1F801074 with
  write-to-acknowledge semantics (writing 0 clears
  a bit); sources are VBlank, GPU (the GP0(1F)
  command and GP1(02) acknowledge), CDROM, DMA and
  timers 0 to 2; the PAD bit exists with no source
  yet. Assertion reaches the CPU through CP0 IP2.
  A synthetic program counts exactly 3 VBlank
  interrupts across 3 frames in its handler, then
  confirms masking stops delivery. Display timing
  is computed from position: video clock at the
  documented NTSC rate (53.693175 MHz, 263 lines of
  3413 cycles) or PAL rate (53.203425 MHz, 314 lines
  of 3406 cycles, selected by the display mode), so
  hblank and vblank windows come from the GPU's
  display range registers, the timer 0 dotclock
  honors the per-line dot floor, and timer 1 clocked
  by hblank counts exactly 263 pulses over one
  display frame (314 in PAL). `Framebuffer` now
  returns the GPU display at native resolution.
  Save states are version 2 (version 1 states are
  rejected): CPU, RAM, scratchpad, GPU (VRAM,
  registers, command and transfer state), timers,
  DMA, I_STAT and I_MASK, SPU RAM, CD controller
  and the timing state, 3,680,305 bytes. The
  determinism gate fills a textured quad through a
  DMA linked list, spins a program on timer IRQs,
  saves mid-run, and proves the restored
  continuation is byte-identical in state and
  framebuffer.

## Timing and raster model notes (PS1-2)

- Rendering is immediate per GP0 command. There is
  no command FIFO delay, polygon queue or GPU cycle
  timing in this slice; GPUSTAT ready bits follow
  the immediate model (ready unless a VRAM to CPU
  readback is pending).
- Peripherals advance at CPU instruction boundaries
  in `Step` order; DMA transfers and timer events
  complete inside the triggering step, so their
  register-visible results are exact at instruction
  granularity.
- The frame models are each exact but distinct:
  `RunFrame` spends the PS1-1 CPU budget (an exact
  rational averaging 565,045 cycles, 59.94 Hz),
  while the display frame is 263 lines of video
  clock (about 566,078 CPU cycles, the documented
  non-interlaced rate). VBlank interrupts therefore
  track display frames, not RunFrame calls.
- Line drawing walks a 16.16 fixed-point DDA with
  both endpoints drawn. psx-spx documents the
  endpoint and shading behavior of lines but does
  not pin the hardware walker's subpixel phase;
  mid-line pixel choice on shallow diagonals is the
  one under-specified step in the rasterizer and is
  flagged here rather than hidden.
- In interlaced 480 mode the framebuffer returns
  the full 480 lines from the display area offset
  by the current field parity (the simplification
  for this slice: no field-line interleave merging).
- The draw-to-display prohibition (GP0 E1 bit 10)
  is applied while the display is enabled; with the
  display off there is no scanout to protect.
- DMA chopping splits transfers into bursts of the
  programmed DMA window size but completes within
  the trigger call; the burst count is observable
  in tests, the moved data is identical.
- When the CD data FIFO runs dry mid-transfer the
  CDROM channel pads zeros (hardware would stall the
  channel); the full CD controller in PS1-3 owns
  refill timing.
- GPUSTAT carries no hblank or vblank bits on the
  real chip; blanking state is exposed through the
  machine's `InHBlank` and `InVBlank` accessors,
  through the timers, and through the VBlank
  interrupt.
- One PS1-1 test expectation changed with the new
  contract: `TestMachineBasics` now checks the
  framebuffer against the machine's native
  display size (256x240 at reset) rather than the
  old fixed 640x480 blank. The test itself, and
  every other PS1-1 gate, is otherwise untouched
  and passing.

## The BIOS approach

There is no Sony BIOS in this core and there never
will be. The shipped iTaK app already boots PS1
discs through an HLE BIOS path, and this core
follows the same rule: the BIOS window exists in the
memory map, its reads return zero words (a NOP sled,
so the reset vector at 0xBFC00000 executes
harmlessly), and our own HLE implementation of the
BIOS calls arrives in PS1-3. User-supplied BIOS
files are not read by this core.

## Slice roadmap

- PS1-3: the SPU (24 voices, ADSR, reverb), the full
  GTE fixed-point implementation, the full CD
  controller, MDEC, BIOS HLE, and a real-game
  enginetest with a homebrew disc.
- PS1-4: the libretro shell, the Android build, and
  the app swap off PCSX ReARMed, gated on the same
  proof as the other cores (real games boot and
  play, save states byte-exact).

## Honesty notes

- Conformance: the TomHarte/ProcessorTests tree was
  checked for a MIPS or PSX single-step set; it has
  none. The CPU gate is the `chips/r3000a` unit
  suite; PS1-2's gates are the documentation-derived
  suites in `gpu`, `timers`, `dma`, `cd` and
  `system` described above. All fixtures are
  synthetic; no Sony data, BIOS bytes or game images
  appear anywhere in this module.
- Cycle counting is instruction-level, not
  cycle-exact: most instructions cost 1, loads and
  stores 2, and the HI/LO busy model applies. Cache
  hits, memory wait states and the instruction
  cache are not simulated.
- The GTE register file is still not part of the
  save state; it joins with its math in PS1-3.
- The MDEC channels move data verbatim to and from
  stubs; the SPU makes no sound; the pad registers
  remain storage until their slices.
