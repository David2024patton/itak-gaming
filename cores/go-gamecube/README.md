# go-gamecube

A from-scratch GameCube core in Go for iTaK Gaming, built
the same way as the go-genesis, go-gameboy, go-nes, and
go-snes cores in this repository: every chip is our own
code, written from published hardware documentation, proven
against hand-derived test vectors kept in the tree, and
eventually wrapped in a libretro-compatible shell for the
app's android-64bit build. One module serves both the
GameCube and the Wii: the Wii is a superset machine (more
memory, an ARM coprocessor for I/O), and its additions land
as their own later slices once the GameCube core proves
out. No third-party Go dependencies.

## Status

**GCN-4 (this slice): the GameCube core is complete.**
The libretro shell, a homebrew enginetest that boots a
fully synthetic disc through the real HLE boot and
drives every subsystem end to end, the android-64bit
app wiring preparation, and a disc parser robustness
pass close the GameCube track. The GCN-3 status text
follows.

- `chips/dsp`: the GameCube DSP as a cycle-stepped
  core running IRAM microcode, with its mailbox link
  to the CPU, ARAM and the AR DMA engine, the audio
  interrupt mirror from the AI, and full save state
  support.
- `ai`: the audio interface: control, volume, sample
  counter and interrupt timing registers, and the
  audio DMA engine streaming big-endian stereo samples
  out of MEM1 at 48000 Hz or 32000 Hz, volume scaled,
  drained to the host through the system's
  `AudioBuffer` and `AudioRate`.
- `di`: the disc interface: status, cover, command
  buffer, DMA and control registers, and the
  synchronous command set (DMA and non-DMA reads,
  identify, stop motor, the audio configuration
  family) served from the loaded disc image.
- `exi`: the external interface: three channels, the
  SRAM and RTC chip (the RTC advances with CPU
  cycles), memory cards in slots A and B, the AD16
  chip, and the IPL ROM window reading as zeros (this
  project ships no IPL bytes). Card images are
  persistent media held outside save states; SRAM is
  inside save states.
- `si`: the serial interface: four controller
  channels polled into the input buffers (a POLL
  write polls at once, and the system re-polls once
  per frame), the command transfer engine, rumble
  motor state, and the I/O buffer window. Channel 0
  reports the machine pad state.
- `vi`: the video interface register window with a
  simplified even-field timing model and the four
  display interrupt registers; the frame's halfline
  counter advances with CPU cycles and a display
  interrupt fires once per frame for an enabled
  target.
- `system`: the processor interface (PI) interrupt
  aggregate at 0x0C003000: PI_INTSR is a computed
  read-only word over the DSP, AI, EXI, SI, DI and VI
  interrupt outputs, PI_INTMSK is stored, and the CPU
  external interrupt line is the masked aggregate,
  refreshed after device MMIO accesses and at the end
  of every run. `BootDisc` performs the HLE boot: the
  FST copied into MEM1 after the DOL, the documented
  boot info block, exception vectors holding a branch
  to self, BATs and segment registers covering MEM1,
  translation and the FPU on, and the CPU started at
  the DOL entry with r3 and r4 pointing at the FST.
  No IPL or apploader code is executed or embedded.
- `system` save states are version 3: the version 2
  blocks, then the DSP, AI, DI, EXI, SI and VI device
  blocks, and a machine tail extended with the total
  CPU cycle count, the DSP cycle remainder, PI_INTMSK
  and the booted flag, at 54,768,032 bytes. Version 2
  states are rejected.

**GCN-2: the floating point unit, paired singles, and
the GX GPU as a software renderer, wired into the
system.**

- `chips/gekko` FPU: the complete PowerPC floating point
  set of the 750. Scalar arithmetic (fadd/fadds,
  fsub(s), fmul(s), fdiv(s), fsqrt(s)) and the fused
  multiply-add family (fmadd(s), fmsub(s), fnmadd(s),
  fnmsub(s)) are computed on exact rationals
  (`math/big`, standard library) and rounded once under
  the FPSCR rounding mode, so fused results never
  double-round. fsel, frsp, the moves
  (fmr/fneg/fabs/fnabs), compares (fcmpu/fcmpo writing
  the CR field and the FPSCR FPCC), conversions
  (fctiw/fctiwz with the documented saturation: above
  range gives 0x7fffffff, below range, either infinity
  beyond range, and NaN give the 0x80000000 low word;
  the result is stored sign-extended across the full
  FPR), the full FPSCR (sticky exception and invalid
  detail bits as far as operations set them, FR/FI/FPRF
  per operation, FEX recomputed from the enables, the
  four rounding modes honored, exception enables stored;
  exceptions set bits and do not trap, as on the 750),
  mffs, mtfsf with the FM field mask, mtfsfi,
  mtfsb0/mtfsb1, mcrfs, and the lfd/lfs/stfd/stfs load
  and store families including indexed and update
  forms. Every FP and paired-single instruction raises
  the floating point unavailable exception when
  MSR[FP] is clear. Denormals follow IEEE rules; with
  FPSCR[NI] set, denormal inputs and results flush to
  signed zero. There is no fcfid: it is not a 750
  instruction and its encoding stays illegal.
- `chips/gekko` paired singles: the Gekko extension.
  FPRs read as packed single pairs (PS0 in the high
  word, PS1 in the low word). The full primary opcode 4
  set (ps_add/sub/mul/div, ps_madd/msub/nmadd/nmsub,
  ps_sum0/sum1, ps_muls0/muls1, ps_madds0/madds1,
  ps_merge00/01/10/11, ps_neg/abs/nabs/mr, ps_cmpu0/
  cmpo0/cmpu1/cmpo1, ps_sel, ps_res, ps_rsqrte), the
  quantized loads and stores (psq_l 56, psq_lu 57,
  psq_st 60, psq_stu 61, plus the indexed forms)
  driven by GQR0-GQR7 (SPR 896-903, also reachable at
  912-919), dequantizing and quantizing per the GQR
  type (float, u8, u16, s8, s16) and signed scale
  fields, stores rounding toward zero and clamping per
  the manual, and dcbz_l zeroing a 32-byte block.
- `gpu`: the Flipper GX as a software renderer fed by
  a FIFO byte stream. CP, BP, and XF command decoding
  (BP loads, CP loads with the documented register map:
  matrix indices 0x30/0x40, VCD 0x50/0x60, VAT groups
  at 0x70/0x80/0x90 indexed by the low bits, array
  bases 0xA0, strides 0xB0; XF direct and indexed
  loads, the indexed groups reading CP arrays 12-15),
  VCD/VAT vertex decoding for every attribute width
  and format, direct and indexed, all seven primitive
  types, modelview and projection transform,
  Sutherland-Hodgman clipping against the canonical
  clip volume for triangles and quads, an edge-equation
  rasterizer with the top-left fill rule and per-pixel
  perspective-correct color and UV interpolation,
  stage 0 texture sampling straight from MEM1 (I4, I8,
  IA4, IA8, RGB565, RGB5A3, RGBA32, CI4, CI8 with the
  TLUT in TMEM, and CMP blocks), nearest and linear
  filtering at mip level 0, the BP pixel pipeline
  (alpha compare, depth modes with update control,
  blending with the documented factors, subtract and
  logic modes, color and alpha update masks), the
  640x528 EFB in the PE pixel formats (RGB565, RGB8,
  RGBA8, and RGBA6 with exact 6-bit channel storage)
  with a 24-bit depth buffer, the BP-triggered EFB
  clear, and display list calls. The system routes
  the CP MMIO window and the GX FIFO write port
  (0x0C008000, also at the 0x8C and 0xCC aliases) to
  the GPU, and `Framebuffer` presents the EFB through
  the BP copy source rectangle, nearest fit, no
  deflicker.
- `system` save states were version 2 in that slice:
  the CPU state (including FPSCR and the GQRs), the
  complete GPU state, MEM1, the locked cache, and
  both hardware pages, at 37,711,354 bytes. Version 1
  states were rejected then; GCN-3 superseded the
  format with version 3, above.

**GCN-1: the integer CPU, the memory map, disc image
foundations, and the system skeleton.**

- `chips/gekko`: the PowerPC 750 (Gekko) integer CPU:
  the complete integer instruction set of the 32-bit
  PowerPC UISA as the 750 implements it. Arithmetic with
  the carry and overflow forms, compare, logical, rotate
  and shift (rlwinm/rlwnm/rlwimi with wrapping masks,
  slw/srw/sraw/srawi with the documented carry rule),
  loads and stores in every form (update forms, multiple,
  string with the XER byte count, byte-reversed), lwarx
  and stwcx. with the reservation, synchronization and
  cache instructions (dcbz really zeroes its block, the
  rest are ordering no-ops), condition register logical
  operations, all branch forms with the full BO/BI decode,
  traps, system call, moves to and from every modeled SPR
  (LR, CTR, XER, DEC, the time base through mftb, SRR0/1,
  DAR/DSISR, SDR1, SPRGs, segment register storage, the
  BAT registers), and the exception machinery: reset,
  DSI/ISI with DSISR and DAR set from the fault, alignment,
  program with illegal, privileged, and trap cause bits,
  decrementer, external interrupt, and system call.
- Address translation: BAT translation is implemented
  functionally, the documented block sizes from 128 KB to
  256 MB, PP protection qualified by the segment register
  keys (Ks/Kp), supervisor and user valid bits. There is
  **no page table walk in this slice**: with translation
  on, a BAT miss raises the same translation exception
  the hardware raises when its walk finds no PTE. With
  MSR IR/DR clear the map is identity, which is the boot
  state. GameCube software and the SDK runtime program
  BATs for the address space they use, so the boot path
  is covered exactly; anything that enables translation
  and relies on hashed page tables will fault until a
  later slice adds the walk.
- `disc`: GameCube disc image parsing of the user's own
  image: the boot header (system ID, game and company
  codes, disc number, version, name, the 0xC2339F3D magic
  check), bi2 fields, the apploader header, the DOL
  structure walk (text and data sections with load
  addresses, sizes, BSS, entry point, exposed as a
  loadable image), and the FST walk (string table,
  recursive directory entries, file extraction by path),
  proven on a fully synthetic disc image built inside the
  test. Wii discs are detected by their magic
  (0x5D1C9EA3) and the partition table is reported as
  metadata only: Wii partitions are encrypted with keys
  this project does not have and does not want, and no
  decryption is attempted anywhere in this module.
- `system`: the machine skeleton in the family shape:
  24 MB MEM1 decoded at its physical base and at the
  cached (0x80000000) and uncached (0xC0000000) aliases,
  the 0x0C and 0x0D hardware register pages (the GX
  parts of the 0x0C page became live in GCN-2, above),
  the locked cache window as RAM backing, a
  `LoadDisc` that stages a DOL and leaves the CPU halted
  at its entry point, `Start`, `RunFrame` consuming the
  486 MHz frame budget of 8,100,000 cycles, a 640x480
  XRGB8888 `Framebuffer`, `SetButtons` storing
  the standard GameCube pad state (buttons plus analog
  bytes), and whole-machine save states. The gate
  test loads a synthetic DOL whose hand-assembled program
  checksums memory, arms the decrementer, and takes the
  decrementer exception into a staged handler; a save
  taken mid-run restores into a fresh machine and the
  continuation is byte-identical to the uninterrupted
  run, state bytes included. The GCN-2 gate adds a
  second synthetic DOL that runs paired-single math
  and copies a GX FIFO stream out of MEM1 byte by byte;
  its save is taken mid-copy, with a partial draw
  command sitting in the GPU's FIFO buffer, and the
  restored continuation matches the uninterrupted run
  byte for byte, EFB pixels included.

GCN-4 additions:

- `libretro`: the shell, core name "iTaK GoGCN",
  extensions iso and gcm, 640x480 XRGB8888 video,
  60.0 fps (the core frame is 8,100,000 cycles at
  486 MHz, exactly 60 per second), and audio declared
  at 48000 Hz: the AI rate is game-programmed (48000
  or 32000 per the AICR rate select), and the shell
  resamples to the declared rate with a linear
  interpolator whenever the active rate differs.
  Input maps the libretro joypad onto the GameCube
  pad: A, B, X, Y, Start and the D-pad by name,
  libretro R2 onto the GC Z button (Z is the lone
  right shoulder button on the GC pad), L and R onto
  the GC L and R with the analog trigger bytes taken
  from the libretro analog L2 and R2 values when the
  frontend reports them and a digital full press
  otherwise, the main stick on the left analog and
  the C-stick on the right analog. MEM1 is exposed as
  RETRO_MEMORY_SYSTEM_RAM; there is no SAVE_RAM
  device, because GameCube saves live on memory
  cards. Card A persists as the file
  "iTaK GoGCN.cardA.raw" in the frontend save
  directory when the environment provides one
  (RETRO_ENVIRONMENT_GET_SAVE_DIRECTORY), loaded at
  game load and written back at unload and reset;
  card B is not file-backed. Loading a disc boots
  its DOL immediately through the HLE boot. Disc
  change is not supported. A reset rebuilds the
  machine from the loaded disc.
- The GCN-4 enginetest gate
  (`system/system_gcn4_test.go`): a fixture generator
  builds a fully synthetic disc (header, apploader
  header stub, DOL, an FST with one file
  TESTDATA.BIN) whose hand-assembled DOL, entered
  through BootDisc with no seeding, walks the FST,
  programs DBAT2 for the hardware page through
  mtspr (the boot BATs cover MEM1 only), DI-DMAs the
  file into MEM1 and verifies its byte checksum,
  streams a 440 Hz sine through AI DMA and polls
  AIINT, polls the SI pad, round-trips a 512 byte
  pattern through memory card A over EXI and
  byte-compares the readback, pushes a GX FIFO
  stream that clears the EFB to a programmed color
  and draws a flat-shaded quad, records five
  milestone words in order, and spins on the VI
  halfline counter. The gate asserts every milestone
  and log word, the exact quad and clear pixels, the
  sine's zero crossings (8 to 9 in 960 frames at
  440 Hz), the exact DI bytes, the card pattern in
  the card media itself, and the SI report against
  the staged pad. A second gate takes a save
  mid-spin and requires byte-identical continuation
  in state, framebuffer, and audio. The same disc
  runs through the built desktop shell under the
  shared `go_backend/cmd/enginetest` harness: all
  harness checks pass (core load, game load, AV
  info, non-blank video, non-silent audio, save
  state capture, per-player save round trip, and
  restore frame equality).
- Two core bugs the enginetest caught and GCN-4
  fixed. First, the X-form register logical
  instructions (and, or, xor and family) wrote their
  result into the f1 field register; the architected
  layout, the same one andi. and ori use in D form
  and the shifts use in X form, carries the source
  in f1 (rS) and the destination in f2 (rA). No
  earlier gate had used a register logical with
  distinct source and destination registers. The fix
  is in `chips/gekko/exec.go`, pinned by
  `chips/gekko/logical_test.go` with hand-derived
  vectors for all eight logical forms plus the mr
  shape. Second, the disc parser's length checks
  used 32 bit arithmetic that wild offsets and entry
  counts could wrap past, and the bi2 read ran past
  the end of images between 0x440 and 0x460 bytes;
  the checks are 64 bit now with an explicit bi2
  floor, pinned by `disc/disc_robust_test.go`
  (truncated headers, wild DOL and FST offsets, a
  wrapping FST entry count, a wrapping file extent,
  overlapping DOL sections), all returning errors
  with no panics and no recover.
- App wiring preparation (nothing shipped from this
  slice; the parent assigns the build): `go_backend`
  gained a SystemGameCube entry ("Go GameCube",
  libgo_gamecube.so, .iso and .gcm, 64-bit) in
  Cores64BitOnly, and the Flutter system grid's
  GameCube entry now names libgo_gamecube.so with
  extensions .iso and .gcm. The Kotlin activities
  take the core name from Flutter, so they need no
  change. What remains at ship time: the
  android-64bit pipeline itself does not exist yet.
  tools/autobuild-server.sh builds only the 32-bit
  APK (android-arm target, arm backend AAR), carries
  no core build list, and has no arm64 branch; the
  64-bit build path (arm64 core builds, the arm64
  AAR, an android-arm64 APK target and its publish
  feed) is a separate pipeline job for the parent.

## Conformance

There is no Tom Harte PowerPC corpus: the ProcessorTests
repository was checked (it carries 6502, 65816, 680x0,
8088, and spc700 sets) and has no PowerPC set. The CPU is
gated on hand-derived vectors from the PowerPC programming
environments manual, the 750 user manual, and the Gekko
supplement: 36 test functions in `chips/gekko`. The 16
integer functions cover rotate and mask edge
cases including wrapping bands, the carry and overflow
truth table for the add and subtract forms (including
chained adde/addze), multiply and divide, string
instruction register windows including the r31-to-r0
wrap, lwarx/stwcx. reservation pass, fail, and
address-mismatch cases, BAT translation scenarios (block
sizes, PP and key denials, user versus supervisor), DSI
entry state on a protected load, decrementer timing (DEC
of 100 raises at exactly step 405, TB at 101), an
eleven-case branch BO decoding table, exception entry for
sc, tw, illegal, FP unavailable, and privileged
instructions, alignment faults for lmw and stwcx. against
working unaligned scalar accesses, little-endian access
behavior, and a full serialize round trip. The 20 GCN-2
functions add the rounding mode truth table on a value
straddling representable singles, fctiw/fctiwz at both
infinities, NaN, and out-of-range magnitudes, an fmadd
case that separates fused from double-rounded results,
fcmpu with NaN, mtfsb1 then mffs readback, frsp of a
double needing rounding, FP-unavailable with MSR[FP]
clear, scalar load/store round trips, quantize and
dequantize round trips for every GQR type at scales 0
and 5 including negative s16, ps_muls0 lane selection,
ps_sum0 cross-lane results, merge patterns, and a
paired-single matrix-vector multiply checked against a
float64 reference. `gpu` adds 13 tests (solid quad pixel
probes, both VAT register groups, an indexed draw
through the CP arrays, depth ordering in both draw
orders, RGB565 and CI8/TLUT texture sampling, additive
blending, CMP decompression, the EFB clear, and a FIFO
stream delivered one byte at a time rendering
identically). `disc` adds 5 tests and `system` adds 5,
including the synthetic-disc DOL, FST, and save-state
determinism gates above.
GCN-4 adds one `chips/gekko` test (the X-form logical
field layout, eight forms plus mr), seven `disc`
robustness tests, and three `system` tests (the
homebrew enginetest, its save-state determinism gate,
and the disc fixture writer the shared harness run
uses). The state format is unchanged in GCN-4:
version 3, 54,768,032 bytes.
`go vet ./...` is clean and `go test ./...` is green.

## Roadmap

- GCN-2 (done): the floating point unit and paired
  singles (the GameCube's signature extension), plus
  the GX GPU as a software rasterizer.
- GCN-3 (done): the DSP audio unit, the DI disc drive,
  EXI and SI devices, the AI and VI, the PI interrupt
  aggregate, and IPL HLE boot (this project never ships
  the Nintendo IPL; games boot through the DOL staging
  path and an HLE BIOS layer).
- GCN-4 (done): the libretro shell, the homebrew
  enginetest through the real boot, the disc parser
  robustness pass, and the android-64bit app wiring
  preparation. The GameCube track is complete.
- Wii track (future work, only when called for): MEM2
  (the extra 64 MB), the Starlet ARM coprocessor, and IOS
  HLE, as their own slices in this same module.

## Honesty notes

- **The four estimate instructions are correctly
  rounded, not bit-exact.** fres, frsqrte, ps_res, and
  ps_rsqrte return the correctly rounded value of the
  exact reciprocal or reciprocal square root. The
  hardware returns a table approximation with a
  documented error bound, so results differ from real
  hardware in the last bits. Every other FP and paired
  operation in this core is exact.
- **Paired-single enable bits are not modeled.** The
  manual gates psq instructions on HID2[PSE] and
  HID2[LSQE]; this core does not model HID2, so the
  instructions run whenever MSR[FP] is set. The SDK
  runtime enables both bits before any game code runs.
  GQR type codes 1-3 (reserved on hardware) are treated
  as float pass-through. Single-precision scalar
  instructions convert their FPR operand to the single
  value through the same exact rounding path as frsp;
  that conversion records no FPSCR status of its own.
- **GX scope limits, stated plainly.** The texture
  cache is bypassed: stage 0 samples image data
  directly from MEM1, TLUT data comes from TMEM, and
  only mip level 0 is sampled. Lighting is the simple
  documented case only: one directional light (light
  0), a vertex normal, and the channel ambient and
  material registers; every other configuration takes
  the flat vertex color path. The TEV is a single
  stage: texel modulated by vertex color, or vertex
  color alone. Lines and points use trivial
  accept/reject clipping (triangles and quads get full
  Sutherland-Hodgman), lines are one pixel wide, and
  points render at size 1. The dither flag and the
  constant alpha register are stored and ignored.
  Stored as state with no rendering effect: BP
  scissor offset, field mask, draw done and token
  registers, the EFB copy Y-scale and high-water area,
  texture SETMODE1 and SETIMAGE1/SETIMAGE2 per group,
  all TEV stage registers except the alpha compare,
  the PE z format and z-comp-loc bits, indirect and
  remaining BPMEM areas, and XF vertex specs and tex
  matrix info beyond the stage 0 post-matrix path.
- **GX register layout choices.** The PE pixel format
  field uses this core's numbering for the four
  implemented formats (0 RGB565, 1 RGB8, 2 RGBA8,
  3 RGBA6_Z24), which does not match the SDK GX_PF
  numbering; a program's format value selects the
  format of the same position in that list. XF
  transform memory is a flat 1024-word file with
  masked addressing and 12-word matrix slots (the
  documented regions land at position matrices 0x000,
  post matrices 0x500, lights 0x600); matrix index 0,
  which the gates exercise, is exact under any
  reading, and normals transform by the modelview
  upper 3x3 rather than a separate normal matrix. GX
  scissor registers are treated as direct EFB pixel
  coordinates. The GPU save state serializes its
  partial-command FIFO buffer at a fixed 8 MiB cap
  (the largest possible single draw command), so most
  of the GPU block in a typical state is zeros.
- **No IPL bytes, ever.** The IPL window at 0xFFF00000
  reads as zeros. The reset vector points there, which is
  why the system stages DOLs directly and starts the CPU
  at the DOL entry instead.
- **Translation scope** is exactly what the Status
  section says: identity with IR/DR off, BATs with them
  on, no hashed page table walk yet.
- **Cycle model is flat**: one instruction is one cycle.
  The real 750 is superscalar with dispatch, completion,
  and cache timing this core does not model. The time
  base and decrementer tick at the documented CPU/4 bus
  ratio, so timer-driven software sees correct rates.
- **Unaligned scalar accesses are performed** as single
  bus transactions, matching that the Gekko handles most
  of them in hardware; a cross-page unaligned access does
  not take a fault for its second page here. Alignment
  exceptions are raised for the documented cases:
  lmw/stmw and lwarx/stwcx. at non-word addresses.
- **Small documented choices where the architecture
  leaves behavior undefined**: the tail bytes of a final
  partial string-instruction register are zero-filled;
  divide by zero yields a zero quotient (and sets OV in
  the OE forms); SRR1 saves the low half of MSR plus the
  cause bits, and rfi restores exactly the architected
  field mask from it; MSR[LE] is honored on data and
  instruction accesses (little-endian mode works, it is
  simply not what GameCube software uses).
- The remaining unmodeled hardware register areas
  (the PE area of the 0x0C page outside the GX blocks,
  and the rest of the 0x0D page) are stored stubs:
  values written are values read back, and nothing
  acts on them. The CP window and the GX FIFO port on
  the 0x0C page are live since GCN-2, and the DSP, DI,
  SI, EXI, AI, VI and PI windows are live since GCN-3.
  The locked cache window is plain RAM backing; the
  lock control registers are not modeled.
- **DSP scope limits, stated plainly.** The DSP IROM
  and DROM read as zeros: this project ships no DSP
  ROM dump, so microcode that depends on ROM
  coefficients gets zeros. The DSP reset starts at
  IRAM 0 in this HLE model. DSP memory sizes follow
  the published GameCube DSP sizes: IRAM 8 KB, IROM
  8 KB, DRAM 8 KB, DROM 4 KB. AR DMA is atomic on the
  count-low write, and the DSP stacks wrap on
  overflow.
- **EXI scope limits, stated plainly.** Transfers
  complete immediately, and the memory card unlock
  sequence is simplified to an ID-read wake. Card
  images are persistent media, not part of save
  states; SRAM is part of save states.
- **DI scope limits, stated plainly.** Commands
  complete synchronously, and non-DMA reads are
  simplified (games use DMA reads).
- **SI scope limits, stated plainly.** POLL writes
  poll immediately, in addition to the per-frame
  polling the system performs.
- **VI scope limits, stated plainly.** The timing
  model is a simplified even-field model. The XFB
  frame buffer base registers are stored only;
  presentation stays with the GX embedded frame
  buffer.
- **PI scope limit, stated plainly.** The GX cause
  bit (0x00000040) is reserved and not raised: the
  GPU exposes no PE interrupt output in this slice.
  The error bit is never raised either.
- **Boot honesty.** The boot info is an HLE
  reconstruction from published layout facts. No IPL
  or apploader code is executed or embedded anywhere
  in this module.
- **No retail game has been booted yet.** Every gate
  in this module runs on synthetic or hand-assembled
  fixtures, the GCN-4 enginetest included. The core is
  complete against its documented model and its
  gates; commercial-game compatibility is unproven
  and will surface real bugs (the X-form logical fix
  in GCN-4 is the shape of what to expect).
- **Shell scope limits, stated plainly.** Disc change
  is not supported. Only card A is file-backed; card
  B exists in the machine but is not persisted by
  the shell. The 32000 Hz to 48000 Hz audio resample
  is a linear interpolator, not a polyphase filter.
  A reset rebuilds the whole machine from the disc
  rather than pulsing the hardware reset line.
