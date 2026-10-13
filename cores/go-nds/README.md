# go-nds: a from-scratch Go Nintendo DS core

Part of iTaK Gaming. This module is a clean-room Nintendo DS
emulator core written in Go from the published hardware
documentation (the ARM architecture manuals and GBATEK). It
bundles no BIOS, no firmware, and no game data, and it has zero
third-party Go dependencies.

DS-1 delivered both CPUs, the dual bus, cartridge header
foundations, and the system skeleton. DS-2 added both 2D
video engines with VRAM bank assignment and display capture,
timers, both DMA engines, IPC, per-CPU interrupt controllers,
the game card command model, the documented minimal stubs
(touchscreen, RTC, AUX SPI save memory, and a generated
firmware image), and the full direct boot. DS-3 added the
3D geometry and rendering engine feeding engine A's BG0,
the 16-channel sound unit with capture, and the
microphone path through the touchscreen ADC. DS-4 (this
slice) completes the core: the libretro shell, the
full-stack enginetest through guest ARM9 and ARM7
programs and through the built shell library in the
shared harness, and the android-64bit app wiring
preparation. Wireless stays a documented stub (below).

## Package layout

- `chips/armcore`: one configurable ARM execution core. The two
  DS processors share an architecture lineage, so a single
  implementation is configured as ARMv4T or ARMv5TE.
- `chips/arm7`: the ARM7TDMI (ARMv4T) as used by the DS for
  sound, touchscreen, and wireless work.
- `chips/arm9`: the ARM946E-S (ARMv5TE, "ARM9") that runs the
  main game code: CLZ, the saturating Q arithmetic group, the
  signed halfword multiplies, LDRD/STRD, BLX, BKPT, PLD as a
  no-op, and the CP15 system control coprocessor with register
  storage, high exception vectors, and fault registers (FSR and
  FAR) set on aborts.
- `bus`: the DS memory system as two CPU views over shared
  backing stores. Main RAM 4 MB at 0x02000000 (mirrored), ITCM
  32 KB and DTCM with CP15-configured base and size honored,
  shared WRAM 32 KB under the WRAMCNT ownership mapping
  (enforced per bank, per CPU), ARM7 private WRAM 64 KB at
  0x03800000, per-side IO pages (the ARM9 page is 8 KB so the
  engine B page at 0x04001000 is mapped), VRAM banks A to I as
  LCDC backing stores, the palette store (2 KB: both engines'
  BG and OBJ palettes) and OAM store (2 KB: 1 KB per engine),
  the GBA slot SRAM window, and the cartridge window gated by
  the EXMEMCNT ownership bit. Banks C and D assigned as ARM7
  WRAM (VRAMCNT MST 2) are decoded in the ARM7 view at
  0x06000000. When the system installs its `IOHook`, IO page
  accesses and the 0x04100000 window (IPC FIFO receive port,
  card data port) are offered to the DS-2 devices first and
  fall back to storage for offsets they do not claim; a bare
  bus behaves exactly as slice 1.
- `cart`: DS ROM header parsing and validation (title, game
  code, maker code, unit code, encryption seed select byte,
  capacity, both binaries' ROM offsets, entry and load
  addresses, icon offset, secure area CRC field, header CRC
  reported as a validity flag rather than a hard failure).
- `periph`: the DS-2 device registers behind the bus hook.
  Per-CPU interrupt controllers (IME/IE/IF with
  write-1-to-acknowledge IF), four GBA-style timers per CPU
  (prescalers, cascade, IRQ), ARM9 DMA (immediate, VBlank,
  HBlank, display-start, main-memory-display, card, GBA cart,
  and geometry FIFO timing; the FIFO port itself is claimed
  by the system hook for the gx package since DS-3), ARM7 DMA (GBA modes; the special
  timing has no source in this module and completes cleanly),
  IPCSYNC and the 16-word IPC FIFOs with their IRQ
  conditions, the game card command unit (see the card note
  under Honesty), an AUX SPI 512-byte EEPROM save memory
  exposed as `Machine.SaveRAM()`, the ARM7 SPI bus (power
  stub, generated firmware flash, touchscreen ADC whose aux
  channel 6 is the microphone input since DS-3), and the RTC
  stub.
- `gx`: the 3D geometry and rendering engine (DS-3). The
  packed GXFIFO port and the direct command ports feed a
  256-entry FIFO plus 4-entry pipe drained by an executor
  implementing the full documented command set: the matrix
  unit (four current matrices, projection and texture stacks
  of 1, position/vector stacks of 31 plus the documented
  entry-31 overflow behavior), polygon and vertex assembly
  (2048 polygons, 6144 vertices, with the GXSTAT and
  RAM_COUNT overflow reporting), the 4-light lighting model
  evaluated per vertex at NORMAL time, all eight texture
  formats including 4x4 compressed, and the test commands
  with their result readbacks. SWAP_BUFFERS defers to the
  next VBlank, where the rendering engine draws the swapped
  list into a 256x192 layer (clipping, viewport, per-span
  perspective texture interpolation, 24-bit depth, the
  documented translucent blending and fog) that the video
  package consumes as engine A's BG0 through the `Line3D`
  provider.
- `audio`: the DS sound unit (DS-3) on the ARM7 IO page: 16
  channels (PCM8, PCM16, IMA ADPCM with the documented step
  and index tables, PSG squares on channels 8 to 13, noise
  on 14 and 15), the stereo mixer at 32,768 Hz with master
  volume, per-side source selects, and SOUNDBIAS, and the
  two sound capture units writing the mix back to memory.
- `video`: both 2D engines and display capture. Engines fetch
  through the VRAMCNT bank assignment layer (BG A/B, OBJ A/B,
  extended palette spaces, and the texture spaces, all built
  from the VRAMCNT bytes). Text, affine, affine-extended, and
  bitmap backgrounds including the mode 6 large bitmap, OBJ
  with bitmap sprites and extended palettes, windows, alpha
  blending and brightness, per-engine master brightness, the
  display modes (off, graphics, VRAM display, main RAM
  display), POWCNT1 display swap, DISPSTAT/VCOUNT, and
  DISPCAPCNT capture into a VRAM bank. Timing: 263 lines per
  frame (VCOUNT 0-262), 192 visible, rendered per scanline;
  the ARM9 line budget splits 1536 visible plus 594 hblank
  cycles and the ARM7 budget 768 plus 297, which reproduces
  the frame budgets of 560,190 and 280,095 cycles exactly.
- `system`: the machine. `LoadROM` performs the full direct
  boot (below); `Start`, `Start9`, and `Start7` jump the CPUs
  to the header entry points; `RunFrame` steps the machine at
  scanline granularity, raising the LCD interrupt sources and
  DMA triggers at the documented points; `Framebuffer`
  returns the two 256x192 XRGB8888 screens in POWCNT1 order
  (top, bottom); `SetButtons` and `SetTouch` drive the
  KEYINPUT/EXTKEYIN registers, the keypad interrupt, the
  touchscreen ADC, and the pen-down interrupt; `Serialize`
  and `Deserialize` implement version 3 save states of
  6,071,376 bytes (version 2 states are rejected; each CPU
  state is 8,388 bytes); `SaveRAM` and `SetSaveRAM` expose
  the AUX SPI save backing.
- `libretro`: the shell (DS-4). Core name "iTaK GoDS",
  extension nds, in the family C ABI shape. Both screens
  are presented in one 256x384 XRGB8888 frame, top screen
  in rows 0-191 and bottom screen in rows 192-383, in
  `Framebuffer` order (which already honors the POWCNT1
  display swap); the app crops or scales as it likes.
  The declared rate is the machine's own quotient,
  33,513,982 Hz over 560,190 ARM9 cycles per frame =
  59.8261 fps, and the audio rate is the sound unit's
  32,768 Hz. The libretro pointer device drives the
  touchscreen: coordinates span the presented frame, a
  press in the bottom half becomes `SetTouch` pixel
  coordinates in the bottom screen, and a press in the
  top half (or no press) lifts the pen. The microphone
  is not wired to a frontend source; `SetMicrophone`
  remains available to in-process hosts. SAVE_RAM is the
  512-byte AUX SPI EEPROM backing and SYSTEM_RAM is
  main RAM. A reset re-runs the direct boot from the
  loaded image. Load accepts a path or content.

## Conformance gates

The SingleStepTests ARM7TDMI corpus (generated from
NanoBoyAdvance) is the CPU gate. There is no ARM9 corpus: the
SingleStepTests/ProcessorTests tree has no ARM directory at all
(checked 2026-10-13), and the ARM7TDMI set lives in its own
repository. The corpus files are local test evidence only,
under `testroms/` (gitignored, never committed).

- ARM7 configuration: 2,250,000 of 2,250,000 tests pass (45
  files, full size). The default `go test` subset is 2,000 per
  file (90,000 tests), also 100 percent; set
  `NDS_TH_SUBSET=50000` for the full run and `NDS_TH_SEED` to
  change the seed. The harness compares registers, CPSR, all
  SPSRs, banked storage, the pipeline model, and every data
  read and write in order, by kind, size, address, and data.
  The corpus does not publish a trustworthy total cycle count,
  so cycle counts follow the published model and are not
  corpus-compared.
- ARM9 configuration: 66,000 of 66,000 gated tests pass (33
  files at the default subset). One file (`arm_mcr_mrc`) is
  excluded because CP15 exists on the ARM9, so those encodings
  are register transfers instead of the undefined trap the ARM7
  corpus expects. Eleven files are run in report-only mode
  (20,479 of 22,000 pass): every divergence in them is the
  documented ARMv5 interworking change (a value written to the
  PC selects the instruction state from bit 0, and the Thumb
  BLX register form writes LR), which the ARMv4T generator does
  not model. The counts stay visible in the test log rather
  than being hidden.
- The ARMv5TE additions are pinned by hand-derived unit vectors
  from the architecture documentation (`armcore_test.go`): CLZ
  edges (0, all-ones top bit, mid-word), QADD/QSUB saturation
  with a sticky Q flag, QDADD/QDSUB with first-stage doubling
  saturation, SMUL/SMLA half selection and sign, SMLA wrap with
  Q set and no clamping, SMULW/SMLAW shifted products, SMLALxy
  64-bit accumulation, LDRD/STRD round trip, MCR/MRC round
  trips including DTCM configuration and the high-vectors bit,
  BKPT prefetch abort with FSR/FAR, and serialize round trips.
- System gate: a synthetic cartridge whose ARM9 program writes
  a magic word into shared WRAM and whose ARM7 program reads,
  increments, and returns it under WRAMCNT ownership changes.
  A mid-run save state restores to a byte-identical
  continuation (shared WRAM and full state compared).
- DS-2 gates (all synthetic, no game data): per-engine text
  BG pixel probes (engine A red/green tiles, an independent
  engine B scene), affine and extended bitmap BG probes,
  bitmap OBJ probe, master brightness up and down to the
  exact 6-bit values, alpha blend to the exact blended value,
  VRAM display mode reading a bank directly, capture writing
  a blended copy into the target bank (verified by reading
  the bank back: 0xBC0F for red blended with blue at
  EVA=EVB=8), a VRAMCNT bank moved between assignments
  changing extended palette rendering, timer counts exact
  over long advances plus cascade, ARM9 DMA immediate and
  GX FIFO sink counts, IPC FIFO loopback in both directions
  with IRQ flags, IPCSYNC nibble with IRQ, card chip ID and
  card DMA reads from the loaded image, AUX SPI EEPROM
  write/read round trip, touchscreen ADC values derived from
  SetTouch (X=100 reads 0x67D through the documented map),
  firmware settings block CRC, and an RTC that ticks one
  second per 60 frames. System gates: the direct-boot gate
  (ARM9 program verifies the boot flag at 0x027FFFFA and its
  installed stack pointer; ARM7 verifies its entry state and
  answers through the IPC FIFO), an ARM9 IRQ handler in ITCM
  counting exactly one VBlank per frame, ARM7 timer IRQ
  asserted while the ARM9 controller stays masked, display
  swap exchanging the Framebuffer order, and a determinism
  gate: direct boot, a two-engine scene with a capture pass,
  a mid-run save, and a restored continuation with identical
  dual framebuffers and identical state bytes.

- DS-3 gates (all synthetic): identity clip matrix readback
  bit-exact against the loaded matrix, POS_TEST matching a
  hand-computed transform, matrix stack pointer and overflow
  behavior across 32 pushes, packed GXFIFO command decode,
  FIFO IRQ mode conditions, RAM_COUNT and the swap pause, a
  red triangle covering the screen center and not a corner,
  textured quads sampling exact texels (256-color palette,
  A3I5 alpha blended over the clear color to the documented
  values, a 4x4 compressed block rendering its four
  hand-computed colors), nearer-quad depth wins, the
  equal-depth flag drawing a second polygon the plain case
  rejects, wireframe edges with a transparent interior, toon
  table shading, fog at full density, the clear image bitmap
  rear plane, a PCM16 loop tone measured within 2 percent,
  ADPCM decode of an encoded sine with bounded error, a PSG
  square pitch measured within 2 percent, pan full-left
  zeroing the right channel, capture RAM matching the drained
  output sample for sample, microphone ADC values through
  the SPI path including the idle center, and the DS-3
  determinism gate: two quads (flat translucent near,
  textured far) with fog on, a PCM loop tone plus a PSG
  square, a mid-run save, and a restored continuation with
  identical dual framebuffers, identical next 2,048 stereo
  audio samples (4,382 int16 values compared), and identical
  state bytes.

## Boot approach: no BIOS, ever

No DS BIOS or firmware image is shipped, needed, or accepted
by this module. Boot is the documented direct-boot path:
`LoadROM` stages the ARM9 binary with WRAMCNT = 3 and the
ARM7 binary with WRAMCNT = 0 (leaving WRAMCNT = 0: at boot
the shared WRAM belongs to the ARM7 and games reprogram it),
copies the 0x200-byte header to 0x027FFE00, writes the chip
ID to 0x027FF800 and 0x027FF804, the header bytes 0x20-0x3F
to 0x027FFC00, and the boot flag halfword 0x0001 to
0x027FFFFA, sets POWCNT1 to 0x020F, and installs each CPU's
entry register state: CPSR = system mode, SVC stack
0x03007FE0 and IRQ stack 0x03007FA0 on the ARM9 (0x0380FDC0
and 0x0380FD80 on the ARM7), user stack at the end of the
ARM9 binary's load region (0x0380FE00 on the ARM7), and
r12 = r14 = the header entry point. The CPUs stay halted
until `Start`. The chip ID is a synthetic constant of this
module (0x00001FC2, in the documented format); real chip IDs
belong to manufactured cartridges. Reads of the hardware
BIOS windows (ARM9 at 0xFFFF0000, ARM7 at 0x00000000) return
the side's open-bus value, the documented approximation:
there are no dumped BIOS bytes in this design, by policy.
One consequence: ARM9 interrupt delivery is fully testable
(its vectors fetch from ITCM), while the ARM7's vectors fetch
from its BIOS window, so ARM7 interrupt correctness is gated
at the IF/IRQ-line level in this slice; the real BIOS
dispatch through the RAM handler pointer is a DS-4 shell
concern.

## Encryption scope

The header parser performs no decryption, embeds no keys,
and reads the secure area CRC field without interpreting the
secure area. DS-2's card command unit is always in the plain
(unencrypted command) state: our boot path never runs the
KEY1/KEY2 encryption setup, and real games only encrypt
after talking to a real BIOS, which this module does not
have. Commands are parsed plain and that is stated here
rather than hidden: the documented data read commands
(0x00 and 0xB7) read from the loaded image, the chip ID
command (0xB8) answers the module's synthetic chip ID, the
KEY1 activation marker (0x3C) is accepted without changing
state, and any other command returns a zero-filled block.

## Honesty notes and approximations

- Caches are NOT simulated. CP15 cache and MPU configuration
  registers are stored, but accesses go straight to backing
  memory and no memory protection is enforced (protection
  enforced: none). The TCM windows are real: the bus decodes
  them from the live CP15 registers. The physical DTCM backing
  store is 32 KB; a configured region larger than the store
  wraps within it (the DS part is 16 KB, so real software
  stays inside).
- Cycle model is flat. Both CPUs use the ARM7TDMI S/N/I cycle
  counts; the ARM9's improved multiplier timing and cache
  timing are not modeled. Timing is budgeted per frame.
- The C flag left by a flag-setting multiply follows the
  ARM7TDMI Booth carry model on both CPUs (the published
  carry-save adder model, as used by the corpus generator).
  The ARM9 architecture leaves that flag unaffected; the value
  is unpredictable on ARMv4, so conforming programs cannot
  observe the difference.
- Architecturally unpredictable forms (multiply or swap into
  the PC, and friends) follow the corpus reference behavior.
  The Thumb condition-14 branch encoding, architecturally
  undefined, evaluates as always, matching the reference.
- The cartridge image is mapped linearly in the 0x08000000
  window for foundation work. A real DS game card streams
  through a serial command interface; direct boot here works
  from the parsed image and never executes from that window.
- Data aborts are raised through the CPU API (and by BKPT for
  prefetch aborts); the bus never faults, so the FSR values
  written on abort are simple status codes, to be refined
  when a faulting bus device exists.
- DS-2 video simplifications, all deliberate: display mode 3
  (main RAM display) reads 15bpp pixels from main RAM at the
  address latched by the DISP_MMEM_FIFO register; the display
  FIFO itself is not modeled. Capture source A is engine A's
  composed line before master brightness (the hardware's
  3D+2D mix; since DS-3 the 3D layer takes part through the
  same BG0 composition); source B is read from a VRAM bank
  at the same coordinates the write offset addresses;
  capture completes at the end of the last captured line. On
  engine A a BG0 selected as the 3D layer (DISPCNT bit 3) is
  filled by the gx package through the `Video.Line3D`
  provider, and the condition stays exposed as
  `Video.BG0Is3D`.
  Mode 6 on engine B behaves as mode 5, and the mode 6 large
  bitmap reads from BG space offset 0. Mosaic quantizes the
  fetch coordinates (a simplification of the hardware's
  sample-and-hold). LCDC CPU access to the physical VRAM
  banks stays mapped regardless of the engine assignments.
  The published VRAMCNT tables for banks F and G carry
  overlapping mode encodings; the resolution used here is
  documented in `video/vram.go`.
- Correction found in DS-3: DS-2 decoded the VRAMCNT byte
  layout shifted by one bit (its enable was bit 0 and its
  MST bits 1-2, with fixtures written in that same private
  convention, so its gates passed self-consistently). The
  published GBATEK bank table puts MST at bits 0-2, OFS at
  bits 3-4, and the enable at bit 7 (a BG A texture bank is
  0x81, not 0x03). DS-3 rewrote the decode and the fixtures
  to the published layout, including bank C MST 4 (BG B),
  bank D MST 4 (OBJ B), the F/G extended-palette slot pairs,
  banks A/B MST 2 (OBJ A only, at the OFS-selected 128 KB),
  and bank I MST 2 (OBJ B at offset 0). Published
  documentation wins over the earlier code, per this
  module's standing rule.
- DS-3 geometry and rendering model choices: the geometry
  engine executes commands eagerly as they are written (the
  hardware's execution timing is not modeled); the
  rendering engine draws the whole display list at the
  VBlank swap rather than per scanline, so mid-frame GX
  register changes apply from the next frame. Clipping is
  standard homogeneous clipping against the six view-volume
  planes (the hardware's near-plane behavior simplified, as
  briefed); a polygon crossing the far plane with
  POLYGON_ATTR bit 12 clear is dropped whole, and clipped
  strip remainders stay part of their polygon. RAM_COUNT
  counts the buffer being built (the counts reset at the
  swap, as documented). TEXIMAGE_PARAM and PLTT_BASE latch
  into each polygon at BEGIN_VTXS. Lighting evaluates the
  documented equations in float64 with 0..1 components and
  rounds to 5-bit (the hardware's fixed-point rounding may
  differ by one LSB); with the shininess table disabled the
  raw squared specular level is used, which GBATEK
  describes as equivalent to the table holding linearly
  increasing values. Shadow shading mode falls back to
  modulation. EDGE_COLOR is stored only (edge marking is
  not drawn); DISP_1DOT_DEPTH drives the documented 1-dot
  polygon rule (a failing 1-dot polygon is skipped whole;
  the hardware nuance of rendering it with its first
  vertex's attributes is not implemented). The equal-depth
  tolerance is GBATEK's fixed plus/minus 0x200 of the 24-bit
  depth (it is not derived from DISP_1DOT_DEPTH; the DS-3
  brief's guess there was wrong and the document wins).
  SWAP_BUFFERS bit 1 follows GBATEK (0 selects Z depth, 1
  selects W depth), the reverse of the brief's reading; the
  Z depth is ((z+w) times 0xFFFFFF)/(2w) and the W depth is
  w shifted left 8 (W = 1.0 maps to 0x100000), both clamped
  to 24 bits. Translucent Y-sorting uses the polygon's
  maximum screen Y ascending, stable for ties. The span
  rules follow GBATEK's polygon-size notes: opaque polygons
  without edge marking or anti-aliasing exclude their lower
  and right edges; translucent, edge-marked, and
  anti-aliased polygons render full size; anti-aliasing
  itself is not computed (it changes only the size rule).
  GXSTAT bit 15 written set clears the stack error and
  resets the projection and texture stack pointers (GBATEK
  says the projection pointer, probably also texture).
  SHININESS (command 0x34) is implemented: GBATEK's
  invalid-command list omits it while its own command table
  documents it, and the table wins.
- DS-3 audio model choices: SAD, TMR, PNT, and LEN latch at
  channel start (volume, pan, duty, format, and repeat are
  read live). The channel sample value is held between
  steps with no interpolation. Channel rates use GBATEK's
  formula 16,756,991/(65536 minus TMR) exactly with a
  rational step accumulator (the brief's halved formula was
  wrong). The mixer produces exactly 32,768 samples per
  16,756,991 cycles (GBATEK's nominal figure; the raw clock
  division gives 32,728.5, a 0.12 percent discrepancy inside
  GBATEK's rounded numbers, stated rather than hidden).
  SOUNDBIAS carries only the bias on the DS (the amplitude
  bits the brief mentioned are a GBA feature). The output
  source selects name channels 1 and 3 (the brief said 1
  and 2; the document wins), and capture pairs are channel
  0 (capture 0) and channel 2 (capture 1) with the
  associated channel added in. Capture runs at the mixer
  rate (GBATEK times it from the channel 1 and 3 timers;
  that sharing is not modeled), captures the final post-bias
  output for the mixer source (so captured samples equal the
  drained output exactly under this model), clamps instead
  of emulating the two documented hardware capture bugs,
  and stores PCM8 captures as byte(value shifted right 8
  plus 128). Noise polarity: positive when the LFSR output
  bit is 0. The output buffer is capped at 65,536 stereo
  samples and drops new samples on overflow. The sound unit
  raises no interrupts (there is no per-channel IRQ). The
  microphone consumes one host sample per SPI aux-channel
  read; with no stream (or an exhausted one) the ADC reads
  the idle center value 0x800.
- DS-2 device simplifications: immediate DMA transfers run
  synchronously inside the register write. The ARM7 DMA
  special timing (sound FIFO and card/wifi triggers) has no
  source in this module and completes cleanly; the ARM9
  GBA-cart timing runs immediately against the open-bus
  window. Writing a timer's CNT_L loads its counter
  immediately. Reading an empty IPC FIFO returns 0 (hardware
  returns the last received word). The wifi interrupt bit
  exists with no source.
- The touchscreen, RTC, and firmware are documented minimal
  stubs. The touchscreen ADC uses a linear map consistent
  with the generated calibration (edge 0x100, far edge
  0xF00), and the pen-up ADC reads 0. The RTC is a simplified
  byte model (the real chip is bit-serial) ticking from the
  fixed epoch 2000-01-01 00:00:00 at one second per 60
  frames. The firmware flash image is generated in code in
  the documented format, never dumped: erased 0xFF fill with
  two user settings blocks (empty nickname, default
  calibration, settings CRC16 at offset 0x70 over bytes
  [0:0x70], this module's internally consistent placement);
  SPI flash write commands are ignored. The AUX SPI save
  memory models the 512-byte EEPROM class.

## Roadmap

- DS-2: done.
- DS-3: done.
- DS-4: done (this slice). The libretro shell is built
  as `libgo_ds.so` (see the `libretro` package notes
  above for the presentation, pointer, and rate
  contract). The enginetest gate
  (`system.TestDS4EngineGate`) runs synthetic guest
  programs on both CPUs: the ARM9 boots through the
  real direct boot (milestone 1), uploads VRAM data
  and programs both 2D engines plus a GX scene (a
  textured far quad under a flat near quad, with a 2D
  text tile composited over the 3D layer) and swaps
  buffers (2), starts a PCM loop tone on the ARM7 via
  the IPC FIFO (3), and has the ARM7 poll the
  touchscreen over SPI (4). The milestone log reads
  [1 2 3 4] in order; the touch ADC values are exactly
  0x67D and 0x565 for a pen at (100, 60); stacked
  frame probes are exact (2D tile magenta over the 3D
  layer, textured texels red and green at their
  documented positions, near quad green, engine B tile
  white over a black backdrop); the tone measures
  442.37 Hz against the 442.28 Hz the documented
  channel formula predicts; a mid-run save restores to
  byte-identical framebuffers, continuation audio, and
  state bytes. The same ROM then went through the
  shared harness (`go_backend/cmd/enginetest`) against
  the built shell library: ALL TESTS PASSED (256x384
  non-blank video, non-silent audio, save state of
  exactly 6,071,376 bytes, restore hash equal to the
  save-point hash). App wiring preparation (nothing
  pushed): the DS slot in
  `go_backend/internal/core/types.go` and the DS entry
  in `flutter_ui/lib/main.dart` now name iTaK GoDS and
  `libgo_ds.so`; `tools/autobuild-server.sh` has no
  64-bit branch or core list yet, so the script side
  waits on the parent's pipeline branch. The core is
  complete. Remaining honest gaps: wifi is a documented
  stub with no source behind its interrupt bit; the
  retail encrypted card protocol (KEY1/KEY2 secure
  area) is out of scope and the card model stays in
  the plain command state; the GX executes commands
  eagerly and draws the whole list at the swap rather
  than per scanline (DS-3 model); lighting is computed
  in float64 and rounded to 5-bit color (possible
  1-LSB differences from hardware fixed point); the
  ARM9 CPU cannot write the 0x06000000 BG VRAM window
  (CPU VRAM writes go through the LCDC window, a DS-1
  bus simplification the DS-4 guest is written
  against). The DS-2 boot gate failure DS-3 observed
  once at `system/ds2_test.go:151` has not reproduced:
  DS-4 ran the system package at `-count=10` (ten
  consecutive full passes including the boot gate),
  on top of DS-3's and the parent's earlier green
  runs.

## Running the gates

```
go vet ./...
go test ./...
NDS_TH_SUBSET=50000 go test ./chips/armcore/ -run TestSingleStep -v
```
