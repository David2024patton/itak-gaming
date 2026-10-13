# go-psp: a from-scratch Go PSP core

A PlayStation Portable emulator core written in Go from published
documentation (the MIPS32 architecture manuals and the PSP hardware
documentation written by the homebrew community). No Sony code, firmware,
BIOS or module dumps are used anywhere in this project, and no game data
is decrypted: PGD-encrypted content is out of scope.

PSP software is an operating environment, not a bare ROM. The eventual
game path for this core is HLE of the PSP kernel and syscalls (slice
PSP-3), the same approach the homebrew toolchain targets, not a
low-level boot of Sony firmware.

## Status: PSP-1 (CPU, memory, media foundations, machine skeleton)

- `chips/allegrex`: the Allegrex integer CPU. Full MIPS I/II/III integer
  set plus the Allegrex/MIPS32 additions: LWL/LWR/SWL/SWR with the exact
  little-endian byte composition rules, LL/SC, MOVZ/MOVN, MUL and the
  MADD/MSUB family accumulating into HI/LO, CLZ/CLO, EXT/INS, ROTR/ROTV,
  SEB/SEH/WSBH/BITREV, and the Allegrex specials MFIC/MTIC (interrupt
  controller register file) and HALT (the exact word 0x70000000). Delay
  slots are modeled with a PC/NPC pipeline pair. There is NO load delay:
  the Allegrex interlocks, so a load writes its register during the load
  instruction and back-to-back load/use sees the new value. CP0 follows
  the PSP layout (PRId reads the Allegrex constant 0x00006542, EBase and
  the Config family are stored, Count advances at half the CPU rate and
  Compare raises the timer line, exception vectors honor BEV and the
  Cause IV bit). FPU (CP1) is implemented for single precision: the core
  arithmetic, CVT/ROUND/TRUNC/CEIL/FLOOR conversions, all 16 C.cond.S
  conditions driving the FCC and BC1F/BC1T branches. Double precision is
  NOT on the Allegrex FPU: double encodings raise Reserved Instruction.
- `bus`: the physical memory map. 32 MB main RAM at 0x08000000 with the
  KSEG0/KSEG1 aliases folded onto the same backing store, 16 KB
  scratchpad at 0x00010000 plus its physical-zero alias, 2 MB VRAM at
  0x04000000 mirrored across its 8 MB window, and the hardware register
  pages kept as stored stub cells. Unmapped reads return zero.
- `umd`: ISO9660 reader (PVD validation, directory walk, file read) and a
  PARAM.SFO parser for the documented PSF key/value format, so the
  PSP_GAME directory convention and disc metadata (title, category, disc
  id) are readable from a user-supplied image.
- `system`: the machine skeleton in the family shape. LoadISO mounts and
  inspects an image, RunFrame consumes a 3,700,000 cycle budget
  (222 MHz / 60), Framebuffer returns a blank 480x272 XRGB8888 buffer,
  SetButtons/SetAnalog store the controller state, and
  Serialize/Deserialize captures the whole machine (CPU + RAM + VRAM +
  scratchpad + input + cycle count) behind a version byte. State size:
  35,669,577 bytes, of which the CPU state is 1,588 bytes.

## Gates (PSP-1)

No MIPS set exists in the Tom Harte ProcessorTests tree (checked
2026-10-13; the tree covers 8088, 68000, 65816, 65c02, SPC700 and others,
no MIPS), so the CPU gate is hand-derived unit vectors from the
MIPS32/Allegrex documentation: 44 tests across the four packages, all
passing, `go vet` clean, `gofmt` clean. The vectors pin the sharp edges:
EXT/INS field boundaries, ROTR by 0 and by register values above 31,
SEB/SEH sign extension, MADD carry into HI, LL/SC link behavior including
the ERET clear, delay slot execution exactly once, exception entry from
inside a delay slot (BD bit), the documented divide-by-zero results, and
FPU rounding-mode vectors (RN/RZ/RP/RM plus trunc/floor/ceil/round
forms), denormal flush and canonical NaN results. A synthetic MIPS
program runs on the machine, and a save state taken mid-run restores
into a byte-identical continuation.

The open-source pspautotests suite (hrydgard/pspautotests) ships 643
prebuilt .elf/.prx binaries, 47 of them under tests/cpu. They need the
PSP-3 HLE loader to run, so PSP-1 only inventories them as later gates:
tests/cpu/cpu_alu (cpu_alu, cpu_branch, cpu_branch2, cpu_div),
tests/cpu/fpu (fpu, rounding, roundmode, fcr, fpu_nan), tests/cpu/lsu
(lsu, llsc, cacheop), tests/cpu/icache (icache), and the tests/cpu/vfpu
set for PSP-2. Nothing from the suite is vendored into this repository.

## Roadmap

- PSP-2: the VFPU in full, and the GE display list processor basics
  (framebuffer presentation, textured primitives) so homebrew can draw.
- PSP-3: HLE kernel: threads and scheduling, syscall dispatch, ELF/PBP
  module loader for homebrew, controller and display HLE, with
  pspautotests as the conformance gate.
- PSP-4: audio (the PSP sound hardware plus AAC/ATRAC stubs as needed),
  the libretro shell, a homebrew enginetest, and app wiring for the
  android-64bit build.

Commercial game support depends on HLE coverage growth slice by slice
and is NOT promised by any of the above.

## Honesty notes (approximations in this slice)

- VFPU is deferred to PSP-2 even though most PSP software uses it. COP2
  encodings (and LWC2/SWC2/LDC2/SDC2) raise Coprocessor Unusable with
  CE=2 and are counted in the CPU's VFPUFaultCount so the system can see
  the demand.
- No TLB. KUSEG is direct-mapped, TLB instructions are no-ops, and the
  CP0 Random register is cosmetic without a TLB behind it.
- No GE/GPU: the framebuffer is blank until PSP-2.
- The bus exposes a 32-bit view of the 64-bit external bus. Cached and
  uncached aliases share one backing store, so the machine is cache
  coherent by construction; the CACHE instruction is a no-op and there
  is no cache to flush.
- Cycle counts are a deterministic approximation (most instructions 1
  cycle, multiply family 2, divide 37, FPU divide/sqrt 12), not a
  pipeline simulation.
- FPU arithmetic is computed in float64 and rounded once through a
  helper that honors the FCSR rounding mode with a one-ulp directed
  adjustment. Denormal inputs and results are flushed to signed zero
  (the Allegrex FPU has no denormal support), NaN results are
  canonicalized to 0x7FC00000, and FPU exceptions are always masked.
  The FPU implementation register (FCR0) reads 0.
- ERET/DERET and the debug exception path are register-level
  simplifications (documented in the CPU source). An exception taken
  while Status.ERL is set transfers control but leaves EPC/Cause
  untouched.
- The ISO reader does not follow multi-extent files; PSP metadata files
  are single-extent, and the disc structures this slice reads always
  are.
- The frame budget assumes 60 Hz; the real panel refresh is about
  59.94 Hz.
