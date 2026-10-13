# go-nds: a from-scratch Go Nintendo DS core

Part of iTaK Gaming. This module is a clean-room Nintendo DS
emulator core written in Go from the published hardware
documentation (the ARM architecture manuals and GBATEK). It
bundles no BIOS, no firmware, and no game data, and it has zero
third-party Go dependencies.

DS-1 (this slice) delivers both CPUs, the dual bus, cartridge
header foundations, and the system skeleton. Rendering, audio,
input devices, and wireless are later slices (roadmap below).

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
  0x03800000, per-side IO pages, VRAM banks A to I as LCDC
  backing stores, palette and OAM stores, the GBA slot SRAM
  window, and the cartridge window gated by the EXMEMCNT
  ownership bit.
- `cart`: DS ROM header parsing and validation (title, game
  code, maker code, unit code, encryption seed select byte,
  capacity, both binaries' ROM offsets, entry and load
  addresses, icon offset, secure area CRC field, header CRC
  reported as a validity flag rather than a hard failure).
- `system`: the machine. `LoadROM` stages both binaries at
  their header load addresses and parks the CPUs; `Start`,
  `Start9`, and `Start7` jump them to the header entry points;
  `RunFrame` spends the frame budget (560,190 ARM9 cycles and
  280,095 ARM7 cycles); `Framebuffer` returns the two 256x192
  XRGB8888 screens (blank in this slice); `SetButtons` and
  `SetTouch` store input state; `Serialize` and `Deserialize`
  implement versioned save states of 5,122,481 bytes (each CPU
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

## Boot approach: no BIOS, ever

No DS BIOS or firmware image is shipped, needed, or accepted
by this module. Boot is the standard direct-boot path:
`LoadROM` copies the two binaries to their header load
addresses and `Start` enters them at the header entry points.
In slice 1 the CPUs start halted after staging so tests drive
them explicitly; DS-2 adds the full direct-boot register state
(including the system registers retail code expects to find
initialized). Reads of the hardware BIOS windows (ARM9 at
0xFFFF0000, ARM7 at 0x00000000) return the side's open-bus
value, the documented approximation: there are no dumped BIOS
bytes in this design, by policy.

## Encryption scope

Slice 1 parses header fields only. It performs no decryption,
embeds no keys, and reads the secure area CRC field without
interpreting the secure area. Retail boot uses the encrypted
secure area; if a later slice needs it for direct boot of
retail images, the documented KEY1/KEY2 scheme from the
manuals is the path, and it will be stated there. Nothing about
that changes the no-keys-in-slice-1 fact.

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
- VRAM bank mapping registers (VRAMCNT) are IO storage in this
  slice. The banks exist at their LCDC addresses; the 2D
  engines that consume the assignments arrive in DS-2. The IO
  pages are plain storage (DISPCNT and friends) except WRAMCNT
  and EXMEMCNT, which are live.
- Data aborts are raised through the CPU API (and by BKPT for
  prefetch aborts); the slice 1 bus never faults, so the FSR
  values written on abort are simple status codes, to be
  refined when a faulting bus device exists.
- Framebuffer output is blank, buttons and touchscreen are
  stored state only, and there are no timers, DMA, interrupts
  beyond the CPU exception model, audio, 2D, 3D, or wifi yet.

## Roadmap

- DS-2: 2D engines A and B, timers, DMA, interrupt controller
  wiring, full direct boot.
- DS-3: 3D geometry and rendering, audio engine on the ARM7.
- DS-4: touchscreen and wifi stubs, libretro-style shell for
  the standalone core build, real-game engine test, and app
  wiring for the android-64bit build.

## Running the gates

```
go vet ./...
go test ./...
NDS_TH_SUBSET=50000 go test ./chips/armcore/ -run TestSingleStep -v
```
