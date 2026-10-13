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

**GCN-1 (this slice): the integer CPU, the memory map,
disc image foundations, and the system skeleton.**

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
  the 0x0C and 0x0D hardware register pages as stored
  stubs, the locked cache window as RAM backing, a
  `LoadDisc` that stages a DOL and leaves the CPU halted
  at its entry point, `Start`, `RunFrame` consuming the
  486 MHz frame budget of 8,100,000 cycles, a blank
  640x480 XRGB8888 `Framebuffer`, `SetButtons` storing
  the standard GameCube pad state (buttons plus analog
  bytes), and whole-machine save states (version byte,
  CPU, MEM1, locked cache, both hardware pages) at
  25,563,749 bytes, dominated by MEM1 itself. The gate
  test loads a synthetic DOL whose hand-assembled program
  checksums memory, arms the decrementer, and takes the
  decrementer exception into a staged handler; a save
  taken mid-run restores into a fresh machine and the
  continuation is byte-identical to the uninterrupted
  run, state bytes included.

## Conformance

There is no Tom Harte PowerPC corpus: the ProcessorTests
repository was checked (it carries 6502, 65816, 680x0,
8088, and spc700 sets) and has no PowerPC set. The CPU is
gated on hand-derived vectors from the PowerPC programming
environments manual and the 750 user manual: 16 test
functions in `chips/gekko` covering rotate and mask edge
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
behavior, and a full serialize round trip. `disc` adds 5
tests and `system` adds 4, including the synthetic-disc
DOL, FST, and save-state determinism gates above.
`go vet ./...` is clean and `go test ./...` is green.

## Roadmap

- GCN-2: the floating point unit and paired singles
  (the GameCube's signature extension), plus the GX GPU
  as a software rasterizer.
- GCN-3: the DSP audio unit, the DI disc drive, EXI and
  SI devices, and IPL HLE boot (this project never ships
  the Nintendo IPL; games boot through the DOL staging
  path and an HLE BIOS layer).
- GCN-4: the libretro shell, a real-game enginetest
  against a user-supplied game image, and the app wiring
  for the android-64bit build.
- Wii track (after the GameCube core proves out): MEM2
  (the extra 64 MB), the Starlet ARM coprocessor, and IOS
  HLE, as their own slices in this same module.

## Honesty notes

- **No FPU yet, on purpose.** FPRs exist as storage, but
  every floating point and paired-single instruction
  raises the floating point unavailable exception in this
  slice, even with MSR[FP] set. Half an FPU would pass
  demos and corrupt games; it lands complete in GCN-2.
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
- The hardware register pages (including the GPU's CP/PE
  pages and the DSP, DI, SI, EXI, and AI blocks) are
  stored stubs in this slice: values written are values
  read back, and nothing acts on them until their slices
  land. The locked cache window is plain RAM backing;
  the lock control registers are not modeled.
