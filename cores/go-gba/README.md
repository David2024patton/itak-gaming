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

**The GBA core is complete (GBA-1 through GBA-4). On top
of the CPU, bus, PPU, peripherals, BIOS and sound of the
first three slices, GBA-4 adds the cartridge save
devices (battery SRAM, the 64 KB and 128 KB flash parts
with their full command protocol, and the serial
EEPROMs), the libretro shell, and the real-game gate:
Anguna, a complete open-source homebrew GBA game, boots,
plays through its title, menus and first animated scene
for 3,600 gated frames, and a state saved mid-flow
restores with a byte-identical 120 frame continuation.
The core passed the shared engine harness end to end
except the audio line, which the harness marks silent
because this game itself makes no sound in the tested
stretch (traced exhaustively; see the honesty notes).
The app swap off mGBA is prepared (build 53 bindings)
and ships once the parent verifies, per the project
rule.**

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
  window: offsets 0x18000 and up fold back onto
  0x10000..0x17FFF, the documented mirror), OAM (1 KB,
  mirrored), the three GamePak ROM windows at 0x08, 0x0A and
  0x0C with mirroring of undersized images, and GamePak SRAM
  (64 KB store behind a byte wide window: wide reads
  replicate the addressed byte across all lanes, wide writes
  store only the low byte; the system narrows the window
  index to 32 KB for battery SRAM cartridges, so the upper
  half mirrors the lower). GBA-4 added SaveRead and
  SaveWrite bus hooks, consulted before the SRAM fast path
  and the backing stores, which let the save devices claim
  their windows (see `save`).
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
- `save`: the cartridge save devices with a bus protocol,
  driven by the `cart` save-type detection and installed
  behind the bus save hooks. Flash (64 KB, and 128 KB in
  two switchable banks) implements the documented command
  protocol end to end: the unlock pair (0xAA to 0x5555,
  0x55 to 0x2AAA), ID mode (0x90) returning the chosen
  documented part IDs (Macronix 0xC2/0x1C for the 64 KB
  parts, Sanyo LE26FV10N1TS 0x62/0x13 for the 128 KB
  part), exit (0xF0), byte program (0xA0) with AND-only
  cell semantics (a program can only clear bits), erase
  setup (0x80) with 4 KB sector erase (0x30) and chip
  erase (0x10), and bank switch (0xB0) on the 128 KB part;
  programming and erasing complete at the triggering
  write, so the documented data-polling reads see the
  final byte at once (stated simplification). EEPROM
  (512 byte and 8 KB) implements the serial protocol on
  the 0x0D window, one bit per bus access: read requests
  (start, 1, address, stop, then 4 dummy bits and 64 data
  bits, most significant first) and write requests
  (start, 0, address, 64 data bits, stop; the block
  commits at the stop bit and the ready poll reads 1
  immediately, the programming interval collapsed and
  stated). The cartridge signature does not name the
  EEPROM size, so the device learns it from the first
  transfer's address width (6 or 14 bits); the window
  follows the documented ROM-size rule (the whole 0x0D
  window for ROMs up to 16 MB, only offsets
  0xFFFF00..0xFFFFFF above that). Battery SRAM stays in
  the bus store, narrowed to its 32 KB by the system.
  Gates: package-level protocol tests (IDs, AND program,
  sector and chip erase, bank 1, EEPROM write-then-read
  at nonzero addresses for both sizes, the window rule)
  plus system-level gates behind synthetic SRAM_V,
  FLASH_V, FLASH512_V, FLASH1M_V and EEPROM_V headers.
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
- `apu`: the complete sound unit. The four PSG channels
  follow GBATEK's GBA sound section and share their
  design with the Game Boy APU in this repository's
  go-gameboy core (the same documented design, and its
  Blargg-proven behaviors are ported: envelope timing in
  1/64 s steps, the sweep unit with its double
  calculation overflow check, the length counters with
  the length-enable edge quirk and the trigger reload
  quirk). Channel 1 is a square wave with sweep, channel
  2 a plain square (duty cycles 12.5, 25, 50 and 75
  percent; frequency 131072/(2048-n) Hz), channel 3 plays
  32 4-bit digits from one of two 16-byte wave RAM banks
  (the bank bit selects the playing bank, CPU accesses at
  0x04000090 hit the other bank; the dimension bit plays
  both banks as 64 digits; volume codes 100, 50 and 25
  percent plus the force-75 percent bit), and channel 4
  is noise from the documented polynomial counter (a
  Galois-form LFSR, 15-bit and 7-bit widths, seeded
  0x4000 or 0x40 at (re)start, clocked at 524288/r/2^(s+1)
  Hz). The two Direct Sound channels hold signed 8-bit
  samples from 32-byte FIFOs, popped once per overflow of
  the timer each channel selects in SOUNDCNT_H and held
  between overflows; when a pop leaves a FIFO at or below
  16 bytes the unit requests a refill, and the system
  answers with one 4-word FIFO DMA burst (DMA1 feeds
  FIFO A, DMA2 feeds FIFO B), the documented half-empty
  rule. Mixing follows the documented output model: each
  PSG channel spans one quarter of the output range and
  each FIFO the full range in a 10-bit domain, the sums
  are scaled by the SOUNDCNT_L side volumes and enables
  and the SOUNDCNT_H PSG ratio (25, 50 or 100 percent),
  the SOUNDBIAS value is added, the result is clipped to
  the 10-bit range, and the stream is emitted as int16
  stereo at the rate the SOUNDBIAS resolution bits
  select (32768, 65536, 131072 or 262144 Hz), box
  filtered from the per-cycle mix by an exact cycle
  accumulator. The frame sequencer (512 Hz: length,
  sweep and envelope clocks) derives from the 16.78 MHz
  machine clock exactly. SOUNDCNT_X bit 7 is the master
  enable: clearing it zeroes the PSG registers
  0x60-0x81, stops all channels and silences the output,
  while SOUNDCNT_H, SOUNDBIAS, wave RAM and the FIFO
  contents survive, exactly as documented. Gates are
  measured, not vibes: channel 1 playing A4 (frequency
  register 0x6D6, the documented value for 440 Hz) counts
  exactly 440 positive zero crossings in one second of
  output; a rising sweep steps 256 to 384, 576, 864, 1296
  and 1944 at the documented sweep clock cycles and shuts
  down on the overflow the double calculation predicts;
  an envelope from 15 at step 1 reaches volume 0 at
  exactly 15/64 s with the output amplitude falling from
  16384 peak-to-peak to a constant; a length-32 counter
  stops its channel at cycle 2064384 on the nose; the
  wave channel reproduces a loaded ramp digit for digit
  at one output sample per digit; the LFSR sequences
  match an independent implementation of the documented
  rule for both widths; and a save state taken
  mid-playback restores with the next 4096 output samples
  bit-identical.
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
  timer stays in phase; GBA-3 added an overflow callback
  the system wires to the sound unit, which is how the
  timers clock Direct Sound. The four DMA channels implement
  source and destination adjust modes (including
  destination reload), repeat, 16 and 32 bit units,
  immediate, VBlank, HBlank and special FIFO start
  timings, completion IRQs, and channel priority in
  registration order (DMA0 first). FIFO bursts move
  exactly four words to a sink interface the system
  provides; since GBA-3 the sink is the sound unit, which
  counts the words and pushes them into its FIFOs. The keypad exposes KEYINPUT
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
  `AudioBuffer` drains the sound unit's interleaved
  stereo int16 output and `AudioSampleRate` reports its
  current rate (32768 Hz unless a game reprograms
  SOUNDBIAS). `SetButtons` feeds the keypad registers. `SaveRAM` and
  `SetSaveRAM` expose the cartridge save contents sized
  by the detected kind (32 KB SRAM, 64 or 128 KB flash,
  512 byte or 8 KB EEPROM); a cartridge with no save
  signature gets the fallback plain 32 KB SRAM window,
  which is what signature-less homebrew expects to find
  (Anguna is such a build: it carries no signature string
  and an unfixed header). `Serialize`
  and `Deserialize` are version 4 save states (version 3
  and older states are refused by the version byte) covering the
  CPU, every memory store, the GPU including its
  framebuffer, the interrupt controller, timers, DMA,
  keypad, the BIOS wait state, the sound unit, the save
  device contents and protocol state, and the machine bookkeeping
  (buttons, current line, FIFO sample counters): 755,548
  bytes for any cartridge. System gates include a
  synthetic ROM whose VBlank handler increments a counter
  and which must count exactly one interrupt per frame
  over three frames through the real dispatcher, and a
  determinism gate that renders a tile and sprite scene
  with an HBlank DMA palette cycle, saves mid-frame,
  restores into a fresh machine, and requires the
  continuation framebuffer and state bytes to be
  identical to the uninterrupted run. GBA-3 added the
  audio gates at this level: timer 0 at 8192 Hz and a
  FIFO DMA sine table measure 255.42 Hz at the output
  (256 Hz target, peak exactly the documented full-scale
  25600) with the DMA word count matching consumption,
  and a mid-playback save during combined PSG and FIFO
  sound restores with bit-identical continuation audio.
  GBA-4 added the save device gates and the real-game
  gate (see the `save` bullet and the honesty notes for
  the Anguna story and its numbers).
- `libretro`: the shell, in the family pattern of the
  SNES and Genesis shells. Core name "iTaK GoGBA",
  extension gba, geometry 240x160 at the hardware frame
  rate (16777216/280896, 59.7275 fps), XRGB8888, the
  joypad mapped onto the KEYINPUT bits, serialize through
  the system states, load by content or by path, and
  RETRO_MEMORY_SAVE_RAM wired to the save contents
  through a shell-owned cache the frontend can read and
  write (game writes are pulled in after each frame;
  frontend edits are hash-detected and pushed before the
  next). The declared audio rate is the machine's rate at
  load (32768 Hz unless the game already reprogrammed
  SOUNDBIAS); if a game changes the rate mid-run, the
  shell resamples to the declared rate with a small
  linear interpolator carried across frames (a documented
  simplicity choice). The desktop build lands in
  `builds/libgo_gba.so` (gitignored).
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
  routes and counts. (Done: pitch, sweep, envelope,
  length, wave, noise, FIFO and determinism gates are all
  measured green; see the `apu` notes above.)
- **GBA-4**: the cartridge save devices (SRAM, flash,
  EEPROM), the libretro shell, the real-game gate on the
  Anguna homebrew, and the app wiring swap prep off mGBA.
  (Done: device gates green, the Anguna gate green, the
  engine harness run recorded in the honesty notes, and
  the build 53 app bindings prepared locally for the
  parent to verify and ship.)

## Honesty notes

- **The Anguna gate found two real core bugs, both fixed
  with regression gates.** Bringing up the real-game gate
  started from a probe that showed a completely dead game
  (black screen, program counter parked at the IRQ
  vector). Debugging found, first, that a CPU halted in a
  BIOS wait took the IRQ exception but never cleared its
  halted flag, so the BIOS dispatcher that completes
  VBlankIntrWait could never run; the fix (an accepted
  interrupt ends a halt, as the CPU's own step comment
  already claimed) is gated by a CPU-level IRQ-from-halt
  test and a system VBlankIntrWait progression test,
  including a save taken mid-wait. Second, once the game
  ran, it rendered nothing because the bus decoded the
  whole VRAM window through a 0x17FFF mask, which drops
  address bit 15: every access at window offsets
  0x8000..0xFFFF landed 32 KB low, scrambling tilemaps
  and tiles in the upper half of VRAM (Anguna's title
  tilemap at 0xE000 landed at 0x6000). The decode now
  folds only offsets at 0x18000 and above, gated by a
  bus test that pins the middle band. Both bugs were
  invisible to the earlier slices' synthetic scenes,
  which never waited on the dispatcher across a halt nor
  placed graphics above 0x8000; that is exactly what a
  real-game gate is for.
- **Anguna makes no sound, and the gate pins that.**
  Measured on this core across 4,500 frames of title,
  menus and gameplay: the game writes the sound registers
  exactly three times at boot (SOUNDCNT_H once,
  SOUNDCNT_X twice), never writes a channel register,
  never arms a FIFO DMA, and never unmasks a sound
  interrupt. Its output is therefore digital silence
  (RMS 0.0), which the gate asserts as this title's
  traced profile, alongside the APU's own measured gates
  from GBA-3. The shared engine harness reports the same
  result its own way (core load, game load, AV info and
  non-blank video pass; per-player save round trip and
  state restore pass at 755,548 bytes; the audio line
  fails as silent). Whether the game's later areas use
  sound is untested; the opening stretch does not.
- **Flash choices.** The ID pairs are the documented
  ones for the parts named in the `save` bullet
  (Macronix 64 KB, Sanyo 128 KB); other vendors' parts
  exist with different IDs and are not emulated.
  Programming and erasing complete instantly rather than
  over the documented milliseconds, so data-polling
  reads return the final byte immediately; no game can
  observe the busy interval. A bare 0xF0 write to 0x5555
  also leaves ID mode, a small leniency several real
  drivers rely on.
- **EEPROM size is learned, with one stated residue.**
  The signature never names the size; the device infers
  6 versus 14 bit addressing from the first transfer (or
  from a loaded save image's length). If the very first
  transfer a game ever makes is a write to the 8 KB part
  whose data puts a 0 then a 1 exactly across the small
  part's stop position, the stream is indistinguishable
  from a small-part write followed by a new transfer,
  and is read the second way; any earlier read pins the
  size and the case never arises. The write programming
  interval is collapsed: the first ready poll sees 1.
  A read whose host omits the stop bit is accepted.
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
- **The audio model is documented, not cycle-exact.**
  The PSG channels are evaluated once per machine cycle
  into a box filter at the SOUNDBIAS rate; within a
  cycle, the mix is taken before that cycle's channel
  timer and frame sequencer clocks land, and a timer
  overflow (a FIFO pop and its refill) is applied at
  instruction granularity: the pop lands before the
  samples of the instruction chunk containing the
  overflow are mixed, not at the exact cycle inside it.
  Direct Sound is sample-and-hold between timer
  overflows, and an underrun holds the last sample, as
  on hardware. One DAC code step maps to 256/15 in the
  documented 10-bit output domain, which gives each PSG
  channel exactly the documented quarter range and each
  FIFO sample (times 4) the full range.
- **The FIFO refill is request-per-overflow.** Hardware
  holds the DMA request open while a FIFO sits at or
  below half empty and a burst lands as soon as the DMA
  can run; this core requests exactly one 4-word burst
  per selected-timer overflow whose pop leaves the FIFO
  at or below 16 bytes. With a DMA channel that can
  deliver, the audible result is the same; with a
  starved or disabled channel both models simply hold
  the last sample. Which DMA channel feeds which FIFO
  is by channel number (DMA1 to A, DMA2 to B); the
  destination address is not decoded, the same
  simplification the GBA-2 DMA shipped with.
- **Wave RAM is a position counter, not a shift
  register.** Hardware shifts the whole 128-bit pattern
  as it plays; this core indexes it. The documented bank
  rule (the CPU only ever touches the bank that is not
  playing) makes the difference unobservable to
  software: playback output is identical, and no read
  can catch the pattern mid-rotation.
- **Small documented choices in the sound unit.** The
  prohibited SOUNDCNT_H PSG ratio code 3 is treated as
  100 percent. The wave force-75 percent volume computes
  (digit times 3) shifted right by 2. Toggling the
  SOUNDCNT_X master enable, or changing the SOUNDBIAS
  resolution, resets the output sample accumulator, so
  no partially averaged sample straddles the switch.
  Pending (not yet drained) output samples are not part
  of save states, the same rule as the Game Boy core
  here: drain `AudioBuffer` before `Serialize` when a
  bit-exact audio continuation matters. The frame
  sequencer quirks (length-enable edge clock, trigger
  length reload decrement, sweep double calculation) are
  ported from the Game Boy core, where Blargg's suite
  proves them; no GBA-specific suite gates them yet
  (see the gba-suite note below).
- **Timer readback is scanline-model consistent.** The
  counters advance with the cycles the CPU actually
  executed, so a read sees the value for the current
  point in the line; there is no sub-instruction
  prefetch skew.
- **The gba-suite homebrew test ROMs were not run.** The
  suite (by endrift, the test ROMs used against mGBA) is
  hosted on GitHub, and anonymous git access from this
  build environment is refused with a credential prompt,
  so the ROMs could not be fetched; that was still true
  when GBA-4 shipped. The PPU, timer and IRQ coverage
  therefore rests on the hand-derived probes and gates in
  this tree, plus the Anguna real-game gate.
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
apu/             the sound unit: PSG channels, Direct Sound, mixing
bios/            our own BIOS code and the SWI HLE layer
save/            the cartridge save devices (flash, EEPROM, states)
system/          the whole machine: LoadCart, RunFrame, states
libretro/        the libretro shell (builds/libgo_gba.so)
testroms/        local-only conformance corpora (gitignored)
```

## Building and testing

```sh
GOTOOLCHAIN=auto GOPROXY=off PATH=$HOME/goroot/go/bin:$PATH go test ./...
```

The conformance gates skip themselves when a corpus is
absent.
