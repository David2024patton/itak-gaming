# go-gba

A from-scratch Game Boy Advance core in Go for iTaK Gaming,
built the same way as the go-genesis, go-gameboy, go-nes and
go-snes cores in this repository: every chip is our own code,
written from published hardware documentation (GBATEK and the
ARM7TDMI technical reference manual), proven against public
conformance corpora kept as local evidence only, and wrapped
in a libretro-compatible shell for the app when it is ready.
No third-party Go dependencies.

## Status

**GBA-2 is complete: on top of the GBA-1 CPU, bus and
cartridge layers, the core now has the full PPU (modes 0
to 5, windows, blending, mosaic, sprites), the interrupt
controller, four timers, four DMA channels, the keypad
registers, and a BIOS layer made of our own hand-assembled
dispatcher and boot stub plus HLE implementations of the
documented SWI calls. The machine boots a cartridge through
the BIOS boot stub exactly like hardware hands control to
the game. Audio is GBA-3 and the libretro shell is GBA-4,
so games are not claimed to run end to end yet.**

- `cart`: the GBA cartridge header, parsed exactly as GBATEK
  lays it out. Minimum size check, the fixed byte at 0xB2
  (must be 0x96), title at 0xA0 (12 bytes, trailing zeros and
  spaces trimmed), game code at 0xAC, maker code at 0xB0,
  version at 0xBC, the header checksum at 0xBD verified as
  `-(0x19 + sum of bytes 0xA0..0xBC)` and reported as a flag
  (a bad checksum does not refuse the image), the entry word
  at 0x00, ROMSize as the smallest power of two that holds the
  image capped at 32 MB, and the save type found by scanning
  the image for the ASCII signatures games embed (SRAM_V,
  FLASH_V, FLASH512_V, FLASH1M_V, EEPROM_V, each reported
  with its version digits; the longer FLASH512_V and
  FLASH1M_V signatures are checked before the FLASH_V stem
  they share). The 156 byte logo bitmap at 0x04 is never
  embedded or checked: it is Nintendo data, not ours.
- `bus`: the full GBATEK memory map. BIOS (16 KB window,
  backed by a writable 16 KB store that the bios package
  installs its own code into), EWRAM (256 KB, mirrored over
  its 16 MB slot), IWRAM (32 KB, mirrored), IO (a plain 1 KB
  register store plus optional IORead and IOWrite hooks: when
  a hook claims an access it serves it, and whatever it does
  not claim falls back to the plain store, which is how
  WAITCNT and the other behaviorless registers still work),
  palette RAM (1 KB, mirrored), VRAM (96 KB in the 128 KB
  window whose offset masks with 0x17FFF, so the top 32 KB
  of the window mirrors the 32 KB below it), OAM (1 KB,
  mirrored), the three GamePak ROM windows at 0x08, 0x0A and
  0x0C with mirroring of undersized images, and GamePak SRAM
  (64 KB, byte wide: wide reads replicate the addressed byte
  across all lanes, wide writes store only the low byte).
  Unmapped reads return the open bus value and unmapped
  writes are ignored. The bus returns raw aligned units with
  no rotation: Read(addr, 4) is the aligned word at addr&^3,
  Read(addr, 2) is the aligned halfword at addr&^1 zero
  extended, and Read(addr, 1) is the byte at addr; writes
  force the address to the access alignment (SRAM writes
  excepted, low byte only). The GBATEK misaligned load
  rotation lives in the CPU, which is also what the
  conformance corpus expects, since its recorded bus
  transactions carry the raw units. Backing stores are
  exported for serialization and tools.
- `gpu`: the PPU at scanline granularity. All register
  behavior for DISPCNT, DISPSTAT, VCOUNT, the four BG
  control and scroll registers, the affine reference points
  and parameters, windows, blending (BLDCNT, BLDALPHA,
  BLDY) and MOSAIC, including the documented read and write
  quirks (scroll registers read as zero, VCOUNT writes are
  ignored, affine reference writes take effect at once).
  Rendering covers text backgrounds in modes 0, 1 and 2
  (4bpp and 8bpp, all screen sizes with wrapping, palette
  banks), affine backgrounds in modes 1 and 2 (8bpp, the
  internal reference point accumulates the per-line dx and
  dy terms across the frame exactly as documented), the
  bitmap modes 3, 4 and 5 with page selection from DISPCNT
  bit 4, and all 128 OBJ entries (every shape and size,
  1D and 2D character mapping, affine sprites with the 32
  parameter sets and the double-size flag, semi-transparent
  flag into the blender, OBJ window, mosaic). Composition
  follows the documented priority rules (lower priority
  number wins, sprites win ties against backgrounds of the
  same priority, earlier OAM entries win sprite ties), then
  windows (WIN0, WIN1, WINOBJ with WININ and WINOUT masks),
  then color effects (alpha blending with the documented
  clamp when the coefficients sum past 16, brightness up
  and down, all against the backdrop as the bottom layer).
  Output is the 240x160 XRGB8888 framebuffer. Hand-derived
  probes pin exact pixels for text tiles in both depths,
  palette banks, priority, affine identity and half-scale
  sampling, mode 3 and mode 4 pixels, the blend clamp,
  brightness extremes, window clipping, mosaic block size,
  and semi-transparent sprite blending.
- `periph`: the interrupt controller, timers, DMA and
  keypad, in one package because they share the register
  dispatch pattern. The interrupt controller holds IE, IF
  and IME at their documented offsets, implements the
  write-one-to-acknowledge IF semantics and the pending
  rule (an IRQ is deliverable when a source is enabled,
  latched and IME is set). The four timers have the
  documented prescalers (1, 64, 256, 1024), reload on
  overflow, cascade counting, and overflow IRQs; the
  internal divider survives save states so a restored
  timer stays in phase. The four DMA channels implement
  source and destination adjust modes (including
  destination reload), repeat, 16 and 32 bit units,
  immediate, VBlank, HBlank and special FIFO start
  timings, completion IRQs, and channel priority in
  registration order (DMA0 first). FIFO bursts move
  exactly four words to a sink interface the system
  provides; in GBA-2 the sink counts samples, and GBA-3
  turns it into audio. The keypad exposes KEYINPUT
  (active low, in SetButtons bit order) and KEYCNT with
  the OR and AND interrupt conditions, evaluated when the
  buttons change.
- `bios`: our own BIOS, in two halves. The first half is
  hand-assembled ARM code installed into the BIOS store at
  power-on: a boot stub that sets up the documented stack
  tops (supervisor 0x03007FE0, IRQ 0x03007FA0, user and
  system 0x03007F00), zeroes the interrupt check word at
  0x03007FF8 and enters the cartridge, and an IRQ
  dispatcher at the 0x18 vector that saves R0-R3, R12 and
  LR, acknowledges the raised sources in IF, ORs them
  into the check word, calls the user handler pointer at
  0x03007FFC with LR set for the return, restores, and
  returns with SUBS PC, LR, #4, exactly the documented
  convention. It is our code implementing the published
  interface, not Nintendo code; no BIOS bytes are dumped,
  copied or embedded. The second half is HLE for the SWI
  calls, reached through a hook in the CPU exception path:
  SoftReset (0x00), RegisterRamReset (0x01), Halt (0x02),
  Stop (0x03), IntrWait (0x04), VBlankIntrWait (0x05), Div
  and DivArm (0x06, 0x07, with the documented divide by
  zero results), Sqrt (0x08), ArcTan and ArcTan2 (0x09,
  0x0A), CpuSet and CpuFastSet (0x0B, 0x0C, with the fill
  and 32 bit count rounding semantics), GetBiosChecksum
  (0x0D, see the honesty notes), BgAffineSet and
  ObjAffineSet (0x0E, 0x0F), BitUnpack (0x10), the LZ77,
  Huffman and run-length decompressors for WRAM and VRAM
  (0x11 to 0x15), and the Diff8 and Diff16 unfilter calls
  (0x16 to 0x18). Every call has hand-derived tests: divide
  signs and the zero case, sqrt values, the arctangent
  scale, a Huffman vector built by hand, LZ77 data
  compressed inside the test in the documented format and
  decompressed back, CpuSet fill and copy control words,
  and affine parameter outputs. IntrWait and
  VBlankIntrWait are tested end to end through the real
  dispatcher: the CPU halts, the VBlank IRQ runs the
  dispatcher bytes on the CPU, the check word gains the
  flag, and the waiter wakes.
- `system`: the machine in family shape. `LoadCart`
  parses an image, installs the BIOS code, wires the ROM
  into the bus windows and resets; the CPU then boots
  through the BIOS stub into the cartridge entry like
  hardware. `RunFrame` advances 228 lines of 1232 cycles
  (160 visible, 68 vertical blank, 280,896 cycles total):
  per line the CPU runs, the line renders at HBlank start
  with the scroll values latched at line start, HBlank
  and VCount interrupts and HBlank DMA fire, and at line
  160 the VBlank flag, interrupt and VBlank DMA fire in
  that order. `Framebuffer` returns the PPU output.
  `SetButtons` feeds the keypad registers. `Serialize`
  and `Deserialize` are version 2 save states (version 1
  states are refused by the version byte) covering the
  CPU, every memory store, the GPU including its
  framebuffer, the interrupt controller, timers, DMA,
  keypad, the BIOS wait state, and the machine bookkeeping
  (buttons, current line, FIFO sample counters): 615,939
  bytes for any cartridge. System gates include a
  synthetic ROM whose VBlank handler increments a counter
  and which must count exactly one interrupt per frame
  over three frames through the real dispatcher, and a
  determinism gate that renders a tile and sprite scene
  with an HBlank DMA palette cycle, saves mid-frame,
  restores into a fresh machine, and requires the
  continuation framebuffer and state bytes to be
  identical to the uninterrupted run.
- `chips/arm7tdmi`: the complete ARM7TDMI CPU (ARM and
  Thumb states, all processor modes and banked registers,
  exceptions, and the published pipeline read quirks). Its
  gate is the SingleStepTests ARM7TDMI corpus (generated
  from NanoBoyAdvance), kept local-only under
  `testroms/tomharte-arm7tdmi/`: 45 files of 50,000 tests
  each, and the full 2,250,000 tests pass, comparing the
  complete register and status state, the pipeline, and
  every data read and write in order. GBA-2 added two
  system-facing hooks without changing execution
  semantics: an IRQ input line the machine drives (a
  pending IRQ is taken at an instruction boundary when
  the CPSR I bit is clear, with the documented LR offset
  per state, and an IRQ also ends a halt), and an SWI hook
  the BIOS HLE registers (unhandled numbers fall through
  to the real supervisor exception). The full corpus was
  re-run after the change and still passes 100 percent.
  The harness is presence-gated and runs a seeded random
  subset by default (2,000 per file; `GBA_TH_SUBSET` and
  `GBA_TH_SEED` override).

## Roadmap

- **GBA-1**: ARM7TDMI CPU with its conformance gate,
  cartridge parsing, the system bus, and the machine
  shell with save states. (Done: the full corpus passes
  and the system gates are green.)
- **GBA-2**: the PPU (modes 0 to 5, windows, blending,
  mosaic, sprites), the interrupt controller, timers,
  DMA, the keypad registers, and BIOS support (our own
  dispatcher and boot stub in the BIOS window, plus SWI
  HLE). (Done: render probes, the exact-count VBlank IRQ
  gate, and the mid-frame determinism gate are green.)
- **GBA-3**: audio: the four PSG channels (two square,
  one wave, one noise) plus the two DMA FIFO direct-sound
  channels, which land on the FIFO sink GBA-2 already
  routes and counts.
- **GBA-4**: the libretro shell, a real-game engine test,
  and the app wiring swap off mGBA (the app keeps shipping
  the incumbent core until this one passes the same proof,
  per the project rule).

## Honesty notes

- **No dumped BIOS ever ships in this core.** The BIOS
  window holds our own hand-assembled boot stub and IRQ
  dispatcher, written for this core from the documented
  calling convention, plus stubs that return from the
  unused vectors. There is no Nintendo BIOS image in this
  repository and there never will be.
- **GetBiosChecksum returns a published constant.** The
  documented checksum algorithm runs over the real BIOS
  bytes, which we do not have and will not ship, so a
  computed value is impossible. The HLE returns the
  commonly documented value for the standard BIOS,
  0xBAAE187F, as a named constant, because that value is
  what games compare against. It is a reported constant,
  not a measurement of anything in this core.
- **SoftReset flag semantics.** GBATEK describes the
  return through the byte at 0x03007FFA and the word at
  0x03007FFC. As implemented here: when the byte is zero
  the machine re-enters through the boot stub (full stack
  setup and cartridge entry); when it is nonzero the CPU
  jumps to the word at 0x03007FFC with the Thumb bit
  masked off. Both paths are covered by tests.
- **Stop behaves as Halt.** Stop (SWI 0x03) is modeled
  exactly as Halt, with the documented wake sources
  simplified to any pending interrupt flag. Real Stop
  also powers down the display and needs a key or
  cartridge event to wake; games effectively never use
  it, and the simplification is recorded here rather than
  hidden.
- **The render model is scanline granularity**, the same
  documented model the SNES core in this repository uses:
  each visible line renders whole at HBlank start, from
  register values and scroll offsets latched at line
  start (the documented text-mode latch behavior for
  mid-frame writes). Per-pixel mid-line register effects
  and the OAM and VRAM access penalties during display
  are not modeled, and neither are the HBlank access
  restrictions; a register write mid-line takes effect on
  the next line. Mosaic is coordinate snapping at the
  register block size, applied to text backgrounds and
  sprites, not affine backgrounds.
- **DMA timing is trigger-boundary.** A channel armed
  for VBlank or HBlank runs its whole transfer at the
  trigger, inside the line step; transfers do not steal
  individual cycles from the CPU mid-line, and enabling
  or disabling a channel mid-transfer is observed at the
  next trigger, not mid-burst. DMA register reads return
  the stored register values where hardware returns open
  bus garbage (the count register is the known case).
- **Timer readback is scanline-model consistent.** The
  counters advance with the cycles the CPU actually
  executed, so a read sees the value for the current
  point in the line; there is no sub-instruction
  prefetch skew.
- **The gba-suite homebrew test ROMs were not run.** The
  suite (by endrift, the test ROMs used against mGBA) is
  hosted on GitHub, and anonymous git access from this
  build environment is refused with a credential prompt,
  so the ROMs could not be fetched. The PPU, timer and
  IRQ coverage therefore rests on the hand-derived probes
  and gates in this tree. If the suite becomes fetchable,
  a presence-gated harness belongs in GBA-4.
- **Wait-state timing is still flat.** WAITCNT is stored
  and read back but not enacted: ROM and SRAM accesses
  cost no extra cycles yet, and the machine advances on
  the documented line and frame budget.
- **Step cycle counts are not corpus-verified.** The
  corpus publishes no per-test cycle totals (its binary
  records carry no total, and the per-transaction cycle
  markers are flagged experimental by its README and are
  not monotonic in practice), so the gate compares state
  and transaction order, not cycles. Step returns cycle
  counts in the GBATEK model (S/N/I classes with GBATEK
  multiply early termination); treat them as modeled,
  not proven. The SWI HLE cycle counts are likewise
  plausible work-based values, not measured hardware
  timings.
- **The multiply C flag is implementation-defined on
  real ARM7TDMI silicon** (the architecture leaves it
  undefined). This CPU reproduces the published radix-4
  Booth carry-save model that the corpus generator
  implements, which is what makes the corpus multiply
  files pass exactly; a different silicon revision may
  legitimately differ in that one flag.

## Layout

```text
chips/arm7tdmi/  the CPU, its conformance gate, and its tests
cart/            cartridge header parsing and save detection
bus/             the GBATEK memory map and the open bus model
gpu/             the PPU: registers and scanline rendering
periph/          interrupts, timers, DMA and the keypad
bios/            our own BIOS code and the SWI HLE layer
system/          the whole machine: LoadCart, RunFrame, states
testroms/        local-only conformance corpora (gitignored)
```

## Building and testing

```sh
GOTOOLCHAIN=auto GOPROXY=off PATH=$HOME/goroot/go/bin:$PATH go test ./...
```

The conformance gates skip themselves when a corpus is
absent.
