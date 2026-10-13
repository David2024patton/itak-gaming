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

**GBA-1 is complete: the ARM7TDMI CPU passes the entire
SingleStepTests ARM7TDMI conformance corpus (45 files,
2,250,000 tests, 100 percent: registers, banked registers,
CPSR/SPSRs, pipeline state, and every data memory
transaction in order), and the cartridge parser, the
system bus and the machine shell are built and tested.
The remaining slices below are not built yet.**

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
  zero filled store), EWRAM (256 KB, mirrored over its
  16 MB slot), IWRAM (32 KB, mirrored), IO (a plain 1 KB
  register store; no register has behavior yet, WAITCNT at
  0x204 included), palette RAM (1 KB, mirrored), VRAM
  (96 KB in the 128 KB window whose offset masks with
  0x17FFF, so the top 32 KB of the window mirrors the 32 KB
  below it), OAM (1 KB, mirrored), the three GamePak ROM
  windows at 0x08, 0x0A and 0x0C with mirroring of
  undersized images, and GamePak SRAM (64 KB, byte wide:
  wide reads replicate the addressed byte across all lanes,
  wide writes store only the low byte). Unmapped reads
  return the open bus value and unmapped writes are ignored.
  The bus returns raw aligned units with no rotation:
  Read(addr, 4) is the aligned word at addr&^3,
  Read(addr, 2) is the aligned halfword at addr&^1 zero
  extended, and Read(addr, 1) is the byte at addr; writes
  force the address to the access alignment (SRAM writes
  excepted, low byte only). The GBATEK misaligned load
  rotation lives in the CPU, which is also what the
  conformance corpus expects, since its recorded bus
  transactions carry the raw units. Backing stores are
  exported for serialization and tools.
- `system`: the machine in family shape. `LoadCart` parses
  an image, wires it into the bus ROM windows and resets
  the machine; `RunFrame` steps the CPU against the
  documented frame budget of 280,896 cycles (the CPU is
  the only active component in this slice); `Framebuffer`
  returns the blank 240x160 XRGB8888 buffer (all pixels
  0xFF000000); `SetButtons` and `Buttons` store the
  controller mask in GBATEK KEYINPUT bit order (A, B,
  Select, Start, Right, Left, Up, Down, R, L in bits 0 to
  9, stored only, since the keypad IO register is GBA-2);
  `Serialize` and `Deserialize` are version 1 save states
  covering the CPU state plus the EWRAM, IWRAM, SRAM,
  palette, VRAM, OAM and IO stores, 462,013 bytes for any
  cartridge (the size is fixed because the SRAM store
  serializes at its full 64 KB). Tests cover the round
  trip (serialize after a frame, restore into a fresh
  machine, serialize again, byte identical), the header
  path through LoadCart, and a hand-assembled synthetic
  program (entry branch 0xEA00002E to code at 0xC0) that
  fills 64 EWRAM words and then runs a checksum loop over
  them forever, storing the checksum and an iteration
  counter in IWRAM and the low checksum byte in SRAM; a
  second test saves the machine mid-run and proves the
  restored continuation is byte-identical to the
  uninterrupted run. The execution tests perform the jump
  to the cartridge entry point themselves, because on
  hardware the BIOS does that jump and this core ships
  no BIOS.
- `chips/arm7tdmi`: the complete ARM7TDMI CPU (ARM and
  Thumb states, all processor modes and banked registers,
  exceptions, and the published pipeline read quirks). Its
  gate is the SingleStepTests ARM7TDMI corpus (generated
  from NanoBoyAdvance), kept local-only under
  `testroms/tomharte-arm7tdmi/`: 45 files of 50,000 tests
  each, and the full 2,250,000 tests pass, comparing the
  complete register and status state, the pipeline, and
  every data read and write in order. The harness is
  presence-gated and runs a seeded random subset by
  default (2,000 per file; `GBA_TH_SUBSET` and
  `GBA_TH_SEED` override). Nothing in this README claims
  games run yet, because they do not: there is no PPU
  until GBA-2 and no BIOS SWI HLE until a later slice.

## Roadmap

- **GBA-1**: ARM7TDMI CPU with its conformance gate,
  cartridge parsing, the system bus, and the machine
  shell with save states. (Done: the full corpus passes
  and the system gates are green.)
- **GBA-2**: the PPU (backgrounds, sprites, windows,
  blending, Mode 3/4/5 bitmaps), timers, DMA, the
  interrupt controller, and the keypad IO register wiring
  for SetButtons.
- **GBA-3**: audio: the four PSG channels (two square,
  one wave, one noise) plus the two DMA FIFO direct-sound
  channels.
- **GBA-4**: the libretro shell, a real-game engine test,
  and the app wiring swap off mGBA (the app keeps shipping
  the incumbent core until this one passes the same proof,
  per the project rule). GBA-4 gates on working BIOS SWI
  HLE; see the honesty notes.

## Honesty notes

- **No dumped BIOS ever ships in this core.** There is no
  Nintendo BIOS image in this repository and there never
  will be. The BIOS window reads from a zero filled store
  in GBA-1, which is the open bus approximation below, not
  BIOS code.
- **BIOS reads are an approximation in GBA-1.** Unmapped
  and BIOS area reads return the open bus value, modeled
  as the last value a read or fetch drove onto the bus
  (tracked inside the bus package). Real hardware returns
  whatever the bus last held, dominated by the prefetched
  instruction word; last-value tracking is the documented
  stand-in until SWI HLE exists.
- **BIOS SWI HLE arrives in a later slice, and most games
  need it.** Real games call BIOS routines (interrupt
  waits, decompression, math) through SWI instructions
  from the first seconds of boot. Until the HLE layer
  implements those calls, most games cannot run, whatever
  the CPU and PPU do; that is why GBA-4 explicitly gates
  on working SWI HLE.
- **Wait-state timing is flat in GBA-1.** WAITCNT is
  stored in the IO store like every other register and is
  not enacted: ROM and SRAM accesses cost no extra cycles
  yet, and the machine advances on the flat frame budget.
- **Step cycle counts are not corpus-verified.** The
  corpus publishes no per-test cycle totals (its binary
  records carry no total, and the per-transaction cycle
  markers are flagged experimental by its README and are
  not monotonic in practice), so the gate compares state
  and transaction order, not cycles. Step returns cycle
  counts in the GBATEK model (S/N/I classes with GBATEK
  multiply early termination); treat them as modeled,
  not proven.
- **The multiply C flag is implementation-defined on
  real ARM7TDMI silicon** (the architecture leaves it
  undefined). This CPU reproduces the published radix-4
  Booth carry-save model that the corpus generator
  implements, which is what makes the corpus multiply
  files pass exactly; a different silicon revision may
  legitimately differ in that one flag.
- **The framebuffer is blank in GBA-1** on purpose. There
  is no PPU until GBA-2, and a blank documented buffer is
  better than a fake picture.

## Layout

```text
chips/arm7tdmi/  the CPU, its conformance gate, and its tests
cart/            cartridge header parsing and save detection
bus/             the GBATEK memory map and the open bus model
system/          the whole machine: LoadCart, RunFrame, states
testroms/        local-only conformance corpora (gitignored)
```

## Building and testing

```sh
GOTOOLCHAIN=auto GOPROXY=off PATH=$HOME/goroot/go/bin:$PATH go test ./...
```

The conformance gates skip themselves when a corpus is
absent.
