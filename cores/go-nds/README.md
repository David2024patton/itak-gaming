# go-nds: a from-scratch Go Nintendo DS core

Part of iTaK Gaming. This module is a clean-room Nintendo DS
emulator core written in Go from the published hardware
documentation (the ARM architecture manuals and GBATEK). It
bundles no BIOS, no firmware, and no game data, and it has zero
third-party Go dependencies.

DS-1 delivered both CPUs, the dual bus, cartridge header
foundations, and the system skeleton. DS-2 (this slice) adds
both 2D video engines with VRAM bank assignment and display
capture, timers, both DMA engines, IPC, per-CPU interrupt
controllers, the game card command model, the documented
minimal stubs (touchscreen, RTC, AUX SPI save memory, and a
generated firmware image), and the full direct boot. Audio,
the 3D engine, and wireless are later slices (roadmap below).

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
  and geometry FIFO timing; GX FIFO words land in a counting
  sink exposed for DS-3), ARM7 DMA (GBA modes; the special
  timing has no source in this module and completes cleanly),
  IPCSYNC and the 16-word IPC FIFOs with their IRQ
  conditions, the game card command unit (see the card note
  under Honesty), an AUX SPI 512-byte EEPROM save memory
  exposed as `Machine.SaveRAM()`, the ARM7 SPI bus (power
  stub, generated firmware flash, touchscreen ADC), and the
  RTC stub.
- `video`: both 2D engines and display capture. Engines fetch
  through the VRAMCNT bank assignment layer (BG A/B, OBJ A/B,
  and extended palette spaces built from the VRAMCNT bytes;
  texture slots are DS-3). Text, affine, affine-extended, and
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
  and `Deserialize` implement version 2 save states of
  5,129,781 bytes (version 1 states are rejected; each CPU
  state is 8,388 bytes).

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
  3D+2D mix, here 2D only until DS-3); source B is read from
  a VRAM bank at the same coordinates the write offset
  addresses; capture completes at the end of the last
  captured line. On engine A a BG0 selected as the 3D layer
  (DISPCNT bit 3) renders transparent and the condition is
  exposed as `Video.BG0Is3D` until the DS-3 engine lands.
  Mode 6 on engine B behaves as mode 5, and the mode 6 large
  bitmap reads from BG space offset 0. Mosaic quantizes the
  fetch coordinates (a simplification of the hardware's
  sample-and-hold). LCDC CPU access to the physical VRAM
  banks stays mapped regardless of the engine assignments.
  The published VRAMCNT tables for banks F and G carry
  overlapping mode encodings; the resolution used here is
  documented in `video/vram.go`.
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

- DS-2: done (this slice).
- DS-3: 3D engine (geometry FIFO drained for real, rasterizer
  feeding engine A's BG0) plus audio (PSG and PCM channels)
  and a microphone stub.
- DS-4: libretro shell, enginetest, and app wiring for the
  android-64bit build. Wifi stays a documented stub.

## Running the gates

```
go vet ./...
go test ./...
NDS_TH_SUBSET=50000 go test ./chips/armcore/ -run TestSingleStep -v
```
