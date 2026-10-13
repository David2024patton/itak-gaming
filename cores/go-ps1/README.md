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

**PS1-3: the SPU with all 24 voices, the full GTE,
the full CD controller, the MDEC codec, pads and
memory card, PS-X EXE loading and the HLE BIOS are
built and gated. PS1-1 (CPU, memory map, CD image
foundations, machine skeleton) and PS1-2 (GPU
rasterizer, timers, DMA, interrupt controller) are
underneath and their gates still pass, with the
reconciliations listed at the bottom.**

- `chips/r3000a`: the MIPS I R3000A CPU, as in
  PS1-1/PS1-2, plus the PS1-3 coprocessor stall
  model: GTE commands charge their documented cycle
  counts (from the psx-spx opcode summary) into the
  instruction cost at instruction granularity, and
  an exported CommitPendingLoad lets the BIOS
  interception return without stranding a delay-slot
  load.
- `chips/gte`: the full GTE. All 22 commands in the
  psx-spx dispatch table are implemented with the
  documented fixed-point semantics: RTPS (0x01),
  NCLIP (0x06), OP (0x0C), DPCS (0x10), INTPL (0x11),
  MVMVA (0x12), NCDS (0x13), CDP (0x14), NCDT (0x16),
  NCCS (0x1B), CC (0x1C), NCS (0x1E), NCT (0x20),
  SQR (0x28), DCPL (0x29), DPCT (0x2A), AVSZ3 (0x2D),
  AVSZ4 (0x2E), RTPT (0x30), GPF (0x3D), GPL (0x3E),
  NCCT (0x3F). (The psx-spx table is the checklist;
  names that appear in secondary sources but not in
  it, such as RTIR and NCAS, are not GTE opcodes and
  return the unknown-command error, pinned by a
  test.) Division inside RTPS/RTPT uses the
  documented UNR reciprocal algorithm; the gate
  measures its error against exact division over
  20,000 deterministic pseudo-random pairs (15,003
  in the non-overflow, non-saturating range): the
  measured maximum error is 2.703 quotient units
  and the gate bound is 3. The SXY FIFO (SXY0/1/2 plus the SXYP push
  register), IRGB/ORGB conversions and LZCS/LZCR
  leading-zero counts follow the docs. FLAG bit 31
  is the sticky OR of the error bits (bits 30-24 and
  18-13); the MAC0 overflow bits 23/22 are reported
  but, per the doc, are not part of the bit-31 error
  set. Saturation of IR1-3 honors the lm flag per
  command. Command cycle counts are charged to the
  CPU coprocessor stall model.
- `bus`: as in PS1-2 (2 MB RAM mirrored, 1 KB
  scratchpad, the IODevice hook on the register
  page).
- `gpu`, `timers`, `dma`: as in PS1-2 (full GP0/GP1
  rasterizer with pixel probes, exact root counters
  from machine position, 7-channel DMA with GPU
  linked lists and the psx-spx OTC worked example).
  DMA channels 0 and 1 now reach the real MDEC codec
  instead of the PS1-2 stubs.
- `spu`: the full SPU per the psx-spx SPU chapter.
  24 voices: volume L/R with the documented sweep
  modes, pitch with the documented clamp at 0x4000
  and the pitch-modulation (FM) formula driven by
  the previous voice's post-envelope output when its
  PMON bit is set (including the documented sign
  glitch for pitch values above 0x7FFF), ADPCM
  decode with the four documented filter pairs (the
  XA chapter table the SPU chapter defers to) and
  the published 4-tap gaussian interpolation table
  from psx-spx (each tap summed with its own
  arithmetic shift by 15, per the doc formula),
  ADSR with the documented rate tables and
  exponential curves, noise mode per voice, reverb
  send per voice, KON/KOFF with ENDX status, and
  the loop-address IRQ (IRQA) raising the SPU
  interrupt. All SPU addresses (SSA, LSAX, IRQA,
  ESA, TSA) are in 8-byte units. The reverb network
  implements the documented equations (input gains,
  the comb and allpass stages at the documented tap
  offsets scaled by the work area, output gains)
  with its work buffer inside SPU RAM at ESA. CD
  audio mixes at CD volume; the four 1 KB capture
  buffers in the first 4 KB of SPU RAM are written
  per the documented map (CD left at 0x000, CD right
  at 0x400, voice 1 at 0x800, voice 3 at 0xC00).
  Output is interleaved stereo int16 at 44,100 Hz
  through an exact ratio accumulator (768 CPU cycles
  per sample, exactly). Measured gates: an ADPCM
  tone built in the test from the documented encoder
  math measures exactly 440.0 Hz at pitch 0x1000
  (220 crossings in 0.5 s) and 882.0 Hz at pitch
  0x2000; ADSR attack and release hit
  their computed checkpoints; a loop sets ENDX and
  fires the IRQA interrupt (count 1 in the gate);
  FM raises the carrier's crossing count from 220
  to 328 (the documented factor of about 1.49 for
  the gate's modulator level), with 22,048 of 22,050
  samples differing; a reverb impulse returns its
  first echo at sample 142 (computed window about
  140) at peak 17,212 after a silent gap; master
  volume at half measures peaks 5,977 against
  11,954 at full, a ratio of exactly 0.500.
- `cd`: the PS1-1 image layer plus the full
  controller. The documented command set is
  implemented: Getstat (0x01), Setloc (0x02), Play
  (0x03), Forward (0x04), Backward (0x05), ReadN
  (0x06), MotorOn (0x07), Stop (0x08), Pause (0x09),
  Init (0x0A), Mute (0x0B), Demute (0x0C), Setfilter
  (0x0D), Setmode (0x0E), Getparam (0x0F), GetlocL
  (0x10), GetlocP (0x11), SetSession (0x12), GetTN
  (0x13), GetTD (0x14), SeekL (0x15), SeekP (0x16),
  Test (0x19, including the published version
  response), GetID (0x1A, with its delayed second
  response), ReadS (0x1B), Reset (0x1C), GetQ
  (0x1D, subchannel Q generated per the documented
  format with the CRC-16 computed by the documented
  polynomial), ReadTOC (0x1E). Timing model: with
  the exception of status reads, commands complete
  over modeled cycle delays driven by the machine
  advance: a seek costs a 120,000 cycle base (or a
  3,000,000 cycle settle when the drive starts from
  a stopped state) plus 900 cycles per sector of
  distance, that term capped at 6,000,000 cycles,
  and sectors then arrive one per sector period
  (451,584 cycles at 1x speed, 225,792 at 2x).
  Completion interrupts (seek INT2, data INT1) fire
  only through that advance. CD-DA playback streams
  samples to the SPU CD input through PullCDSample;
  Mode 2 Form 2 XA-ADPCM sectors decode with the XA
  filter table (different from the SPU table) and
  route to the same input when Setmode XA is on;
  Setfilter file/channel filtering applies to data
  reads. Error responses carry the documented bytes
  (INT5 with 0x10 for bad parameters, 0x80 for no
  disc).
- `mdec`: the macroblock codec per psx-spx: VLC
  bitstream parsing with the published DC and AC
  tables and escape codes, dequantization with the
  stream's quant tables, the fixed-point IDCT with
  the published coefficient set, YUV to RGB per the
  documented formula, output in 24-bit, 15-bit (STP
  bit from the control word) and 4-bit/8-bit
  monochrome modes, and DMA0-in/DMA1-out wiring in
  the documented word order. Gates: a DC-only
  luminance block decodes to the exact uniform
  value, a 4:2:0 macroblock in 15-bit mode produces
  the expected solid color at probed pixels, one AC
  coefficient lands within plus/minus 2 of an
  independent float IDCT reference (the DC path is
  exact), and a quant table change alters output as
  documented. State size 198,912 bytes. The
  package also carries a BS Huffman layer (the
  software format of STR movies and BS picture
  files) as an extension, selected by an otherwise
  unused command bit; real software performs that
  decode on the CPU, never through the MDEC ports.
  Its v3 DC reconstruction multiplies the decoded
  category value (base + index) by 4 per the
  transcription in bs.go; that scaling has not
  been verified against a real BS v3 stream and is
  flagged in the test that covers it.
- `pad`: the SIO0 joypad and memory card controller
  per psx-spx, with immediate byte completion (the
  stated timing simplification): JOY_DATA/STAT/MODE/
  CTRL/BAUD at their documented sizes, IRQ on the
  documented acknowledge conditions. Digital pad
  (ID 0x41) poll 0x42 returns active-low button
  bytes from SetButtons; config mode (0x43), mode
  set (0x44) enables the analog response (ID 0x73)
  with stick bytes from SetAnalog; the info commands
  0x45 to 0x48 and 0x4C answer the documented
  constants; rumble mapping (0x4D) is stored and the
  mapped poll bytes drive Motors(). Memory card
  behind address 0x81: read (0x52), write (0x57) and
  get ID (0x53; 0x59 is accepted identically, the
  two published sources disagree) with 128-byte
  frames, the XOR checksum over address plus data,
  the documented end bytes (0x47 good, 0x4E bad
  checksum, 0xFF bad sector; a bad-checksum write is
  not stored) and the FLAG byte semantics (starts
  0x08, bit 3 clears on a completed write, bit 2
  surfaces after a failed write). The card image is
  a raw 128 KB store; its directory is game-managed,
  we implement the raw protocol only. An absent card
  answers all 0xFF with no acknowledge. State size
  131,584 bytes including the card image.
- `system`: the wired machine, state version 3.
  PS-X EXE loading (LoadEXE) per the documented
  header: text copied to t_addr, PC = pc0, GP = gp0,
  SP and FP = s_addr + s_size when s_addr is
  nonzero, else the documented default stack top
  0x801FFF00; saved-register header fields are
  ignored. The HLE BIOS (bios.go) intercepts the
  call vectors at 0xA0/0xB0/0xC0 and the
  critical-section syscalls. Implemented calls:
  A(44h) FlushCache, A(48h) SendGP1Command, A(49h)
  GPU_cw, A(4Ah) GPU_cwp, A(4Dh) GetGPUStatus;
  B(02h) init_timer, B(03h) get_timer, B(04h)
  enable_timer_irq, B(05h) disable_timer_irq,
  B(06h) restart_timer, B(07h) DeliverEvent, B(08h)
  OpenEvent, B(09h) CloseEvent, B(0Ah) WaitEvent,
  B(0Bh) TestEvent, B(0Ch) EnableEvent, B(0Dh)
  DisableEvent, B(12h) InitPAD2, B(13h) StartPAD2,
  B(14h) StopPAD2, B(20h) UnDeliverEvent; C(04h)
  get_free_EvCB_slot, C(08h) SysInitMemory, C(09h)
  SysInitKernelVariables, C(0Ah) ChangeClearRCnt,
  C(19h) SetRCnt, C(1Ah) GetRCnt, C(1Bh) ResetRCnt
  (the SDK root-counter names, at the slots the
  PS1-3 brief named; psx-spx defines the root
  counter API as the B(02h) to B(06h) set above, and
  both are implemented); SYS(01h)
  EnterCriticalSection and SYS(02h)
  ExitCriticalSection through the syscall
  instruction. Any other call is logged (the machine
  exposes UnimplementedBIOSCalls and its count) and
  returns $v0 = 0xFFFFFFFF; it never crashes.
  Events: 16 slots, handles 0xF1000000 and up, open
  disabled; mode 0x2000 marks ready for TestEvent,
  mode 0x1000 queues a callback invoked at a Step
  boundary through a synthetic frame with the full
  context saved and restored. InitPAD2 buffers are
  filled on VBlank once StartPAD2 has run. The
  slice gate is the enginetest in
  system/system_ps3_test.go: a generator in the test
  builds a PS-X EXE from hand-assembled MIPS that
  opens and enables the VBlank event through the
  BIOS and counts three deliveries, initializes the
  GPU through GPU_cw, DMA-uploads a texture and
  draws a textured quad, DMA-loads an ADPCM tone
  into SPU RAM and plays it, issues Setloc and ReadN
  and DMA-checksums the sector, runs RTPS through
  the GTE, and writes a PASS magic last. The test
  asserts the magic and all six stage markers, the
  framebuffer probes (the four texture quadrants
  render red, green, blue and white at (48,48),
  (80,48), (48,80) and (80,80)), the tone in the
  audio output (exactly 440.0 Hz, 220 crossings in
  0.5 s), the sector checksum (0xFFFFFF00 for the
  synthetic disc), the GTE results (SXYP (160,120)
  exact for the centered point, (226,86) for the
  offset point, FLAG bit 31 clear), and a mid-run
  save/restore continuation that is byte-identical
  in state, framebuffer (76,800 pixels), result
  area and the next 5,208 audio samples. Save states are version 3 (version
  2 states are rejected): CPU, RAM, scratchpad, GPU,
  GTE, timers, DMA, interrupt controller, SPU, CD
  controller, MDEC, pad and card, the BIOS HLE block
  and the machine misc block: 4,115,149 bytes.

## Timing and model notes (PS1-3)

- The two PS1-2 frame models still coexist exactly:
  RunFrame spends the rational CPU budget (565,045
  cycles average) while the display frame is about
  566,078 CPU cycles, so VBlank tracks display
  frames.
- WaitEvent is non-blocking in the HLE (it returns
  the current readiness): HLE time advances only
  while the CPU executes, so a blocking wait could
  never be satisfied. This is the one deliberate
  BIOS semantic deviation.
- BIOS event delivery follows the I_STAT latch, not
  I_MASK: on hardware a masked interrupt never
  reaches the kernel handler.
- The SPU KON delay is a fixed 5 output samples.
  The reverb path replaces the documented 39-tap
  resampling FIR with plain decimation at half rate
  (the reverb itself runs every second output
  sample, as documented); the feedback equations
  are the documented ones.
- Reading the MDEC register at 0x1F801820 returns a
  pending output word when status bit 31 (data-out
  request) is set, and the status register
  otherwise; the two share the address on hardware
  in the same way, disambiguated by that bit here.
- CD model constants are stated with the cd package
  above. CDROM DMA pads zeros if the data FIFO runs
  dry (hardware would stall the channel).
- GTE command timing is charged per command at
  instruction granularity; there is no cycle-exact
  coprocessor pipeline model.
- Pad and card transfers complete immediately on
  the JOY_DATA write; JOY_STAT TX-ready bits read 1
  and the baud timer bits read zero.

## PS1-2 gate reconciliations in PS1-3

- The cd package tests and the system CD gate now
  advance the machine (or the controller) past the
  modeled seek/settle delay and one sector period
  before asserting data; every content assertion is
  unchanged. The PS1-2 controller delivered sectors
  immediately; the PS1-3 timing model is the
  documented behavior the slice required.
- The SPU transfer port moved to the documented
  register map: PS1-2 carried the address register
  at 0x1F801DA4 in 4-byte units and the data port at
  0x1F801DA6; the documented map is IRQA at
  0x1F801DA4, TSA at 0x1F801DA6 in 8-byte units, and
  the data port at 0x1F801DA8. The PS1-2 transfer
  tests were updated to the documented addresses
  and units with their content assertions unchanged.

## The BIOS approach

There is no Sony BIOS in this core and there never
will be. The BIOS window exists in the memory map
and its reads return zero words (a NOP sled, so the
reset vector at 0xBFC00000 executes harmlessly), and
the documented call vectors are serviced by our own
HLE implementation (see the system section above).
User-supplied BIOS files are not read by this core.

## Slice roadmap

- PS1-4: the libretro shell, the Android build, and
  the app swap off PCSX ReARMed, gated on the same
  proof as the other cores (real games boot and
  play, save states byte-exact).

## Honesty notes

- Conformance: the TomHarte/ProcessorTests tree has
  no MIPS or PSX single-step set. The gates are the
  documentation-derived suites in each package plus
  the enginetest; all fixtures are synthetic and
  built inside the tests. No Sony data, BIOS bytes
  or game images appear anywhere in this module.
- Cycle counting is instruction-level, not
  cycle-exact: most instructions cost 1, loads and
  stores 2, the HI/LO busy model applies, and GTE
  commands charge their documented counts. Cache
  hits, memory wait states and the instruction
  cache are not simulated.
- The CD controller, SPU, GTE, MDEC and pad/card
  approximations are stated in their sections
  above, not hidden.
