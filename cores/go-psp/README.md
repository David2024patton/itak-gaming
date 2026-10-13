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

## Status: PSP-2 (VFPU complete, GE display list processor, system integration)

PSP-1 delivered the CPU, memory, media foundations and the machine
skeleton (kept below). PSP-2 adds the two coprocessor-class units:

- `chips/allegrex` (VFPU): the full VFPU lives in the CPU package. The
  register file is 128 singles organized as 8 matrices of 4x4 with the
  documented row/column operand decode for S/P/T/Q sizes. Encodings
  follow the published VFPU instruction tables (the uofw PSP hardware
  documentation and the davidgfnet VFPU instruction set documentation):
  VV arithmetic under primary opcodes 0x18/0x19/0x1B, the unary and
  conversion families under 0x34, matrix operations under 0x3C, prefix
  instructions under 0x37, loads/stores on the COP2 load/store opcodes,
  and COP2 (0x12) itself holding only the register moves and the
  condition branch. Implemented families: VADD/VSUB/VMUL/VDIV,
  VMIN/VMAX, VDOT/VHDP/VCRS/VDET, the scalar helpers (VSCL, VSBN), the
  unary set (VMOV, VABS, VNEG, VSGN, VSAT0/VSAT1, VZERO/VONE, VIDT),
  the float function set (VSQRT, VRCP, VRSQ, VNRCP, VEXP2, VREXP2,
  VLOG2, VSIN/VCOS/VNSIN, VASIN), conversions (VF2H/VH2F half float,
  VF2IN/VF2IZ/VF2IU/VF2ID with the published scale field, VI2F, VI2UC/
  VI2S packs, VCST constants), matrix ops (VMMUL, VMSCL, VTFM/VHTFM
  2/3/4, VMZERO/VMIDT matrix forms), VCMP with the full 16-entry
  condition table writing the VFPU condition code (per-lane bits plus
  any/all aggregates), VCMOVT/VCMOVF (carved out of the VI2F encoding
  space by the published marker values), VREPL/VSPLT replication, the
  VPFXS/VPFXT/VPFXD prefix instructions with full prefix semantics
  (source swizzle/abs/negate/constant substitution from the documented
  constant table, destination saturation and write masks, one-shot
  consumption), random support (VRNDS seed, VRNDI integer output and
  the float forms), loads/stores LV.S/SV.S and LV.Q/SV.Q with the
  LVL.Q/LVR.Q/SVL.Q/SVR.Q unaligned merge semantics, and the control
  register moves MFC2/MTC2/CFC2/CTC2/MFVC/MTVC (index 131 is the
  condition code, 135 the read-only revision, 136 to 143 the random
  state). VFPU float math follows the same model as the scalar FPU:
  operands pass through the denormal-flushing input helper, arithmetic
  runs in float64, results are rounded once to single. Genuinely
  unimplemented encodings still raise Reserved Instruction and are
  counted in VFPUFaultCount; every documented family above executes.
  VDET (the pair determinant) is implemented in the product group.
- `ge`: the GE display list processor. Lists are submitted by address
  and executed synchronously: SIGNAL stalls the list (the signal value
  and behavior are recorded; Continue resumes past it), FINISH is
  recorded, END completes the list. Every command word is stored to
  its register slot and counted per opcode, including commands that do
  not draw (BEZIER, SPLINE, BBOX are counted and flagged no-draw).
  State covers the framebuffer and depth buffer pointers and widths,
  viewport and offset, region and scissor, clear mode, texture state
  (addresses, sizes, formats T565/T5551/T4444/T8888 and CLUT4/8/16/32
  with CLUT load and the documented texture swizzle layout), blend
  equation and factors, alpha/stencil/color tests, logical ops, and
  the write masks. Vertex decoding handles the weights/UV/color/
  normal/position widths in through and indexed forms. PRIM types 0
  to 6 (points, lines, strips, triangles, strips, fans, sprites) are
  rasterized in software with per-pixel barycentric interpolation
  (no perspective correction, see honesty notes), the full fragment
  test chain, and drawing into VRAM in the active pixel format
  (5650/5551/4444/8888). The system presents the display buffer (or
  the draw buffer until one is set) as the 480x272 frame.

## PSP-1 (CPU, memory, media foundations, machine skeleton)

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
- `system`: the machine in the family shape. LoadISO mounts and
  inspects an image, RunFrame consumes a 3,700,000 cycle budget
  (222 MHz / 60), Framebuffer returns the blank 480x272 XRGB8888 buffer
  until the first display list is submitted and the GE presentation
  after it, SubmitDisplayList/ContinueDisplayList drive the GE
  synchronously (the sceGu HLE path is PSP-3), SetButtons/SetAnalog
  store the controller state, and Serialize/Deserialize captures the
  whole machine behind a version byte (version 2: CPU including the
  VFPU + RAM + VRAM + scratchpad + input + cycle count + GE state).
  State size: 35,674,041 bytes on a fresh machine, of which the CPU
  state is 2,864 bytes (PSP-1: 35,669,577 total, 1,588 CPU).

## Gates (PSP-2)

90 tests across the five packages, all passing, `go vet` clean,
`gofmt` clean. New in PSP-2:

- VFPU: 29 test functions with hand-derived vectors per family:
  arithmetic in all four sizes, min/max NaN rules, saturation forms,
  the float function set, trig at documented points (tolerance based,
  see honesty notes), half float round trips including denormal and
  NaN cases, the int conversion family with its scale field, dot/
  cross/homogeneous products, matrix multiply and transforms against
  hand-computed products, prefix constant/swizzle/saturation cases,
  destination prefix masks, replication, compare and branch on
  condition, conditional moves, LVL/LVR merge cases, random seeding,
  control register moves, and a serialization round trip. A dedicated
  regression set pins the published encodings themselves: every VV
  arithmetic op runs with VT in matrix 0 and matrix 1, and raw anchor
  words from the published tables (vadd.q base 0x60008080) execute as
  arithmetic, not as COP2 moves.
- GE: 13 test functions over synthetic word-array display lists: clear
  fills exact pixels including the raw VRAM word, a through-mode
  triangle covers its center and misses outside points, a textured
  sprite samples a 2x2 texture correctly from both unswizzled and
  swizzled storage, the depth test rejects a farther triangle over a
  nearer one, a sprite fills exactly its rectangle, SIGNAL stalls and
  Continue reaches FINISH/END with the counters asserting the flow,
  CLUT8 textures resolve through the loaded palette, alpha blending
  and alpha test behave, JUMP/CALL/RET control flow is honored, and
  the full command table decodes with per-opcode counters.
- System: a synthetic program runs VFPU math on the CPU (vadd/vdot/
  vmul with hand-computed results) and a display list through the GE
  (clear plus a sprite, pixel-probed), then a state saved mid-run
  restores into a byte-identical continuation of both state bytes and
  framebuffer pixels. Version 1 states are rejected by the version 2
  loader.

## Gates (PSP-1)

No MIPS set exists in the Tom Harte ProcessorTests tree (checked
2026-10-13; the tree covers 8088, 68000, 65816, 65c02, SPC700 and others,
no MIPS), so the CPU gate is hand-derived unit vectors from the
MIPS32/Allegrex documentation. The vectors pin the sharp edges:
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

- PSP-2 (done): the VFPU in full, and the GE display list processor
  (framebuffer presentation, textured primitives) so homebrew can draw.
- PSP-3: HLE kernel: threads and scheduling, syscall dispatch, ELF/PBP
  module loader for homebrew, controller and display HLE, with
  pspautotests as the conformance gate (including the tests/cpu/vfpu
  set as the executable cross-check for the VFPU encodings and math).
- PSP-4: audio (the PSP sound hardware plus AAC/ATRAC stubs as needed),
  the libretro shell, a homebrew enginetest, and app wiring for the
  android-64bit build.

Commercial game support depends on HLE coverage growth slice by slice
and is NOT promised by any of the above.

## Honesty notes (approximations in this slice)

- VFPU precision model: arithmetic is computed in float64 from
  denormal-flushed inputs and rounded once to single, the same model
  as the scalar FPU. The hardware transcendental units (VSIN/VCOS/
  VEXP2/VLOG2/VRCP/VRSQ family) use polynomial approximations on real
  hardware; this core evaluates the host math functions in the
  documented units (trig in units of pi/2) and the tests assert a
  stated tolerance against the true values, not bit equality with the
  hardware polynomials. Bit-exact polynomial reproduction is a later
  accuracy pass, with pspautotests (PSP-3) as the cross-check.
- VFPU random: the generator behind VRNDI/VRNDS and the float random
  forms is an xorshift32 over the documented seed/state registers.
  The exact hardware generator is not fully published; the interface
  (seed register, state readout through MFVC) is implemented as
  documented and the algorithm is best-known, not verified.
- Matrix operations (VMMUL, VMSCL, VTFM/VHTFM, matrix moves) ignore
  source and destination prefixes in this slice; the documented
  operand overlap restrictions are not enforced. VCMOV with selector
  7 is unspecified in the documentation; this core treats it as the
  per-lane inverted form.
- Two encodings could not be confirmed in the published documents:
  VREPL and VSPLT appear in neither reference text, so they sit at
  free function values in the unary group (0x08 and 0x09) with the
  semantics the instruction names imply, flagged in the source.
  VCST indexes the 8-entry prefix constant table here; one reference
  defines a longer constant list of its own, so VCST values past the
  shared entries may differ from hardware. The random control
  registers are mapped at 136 to 143 (8 words); one reference lists
  the range as 136 to 147. VIDT keeps the full identity-vector write
  from the first build; one reference describes a per-vector
  identity init instead. All of these are PSP-3 cross-check items
  for the pspautotests vfpu set.
- GE rasterization interpolates per pixel with barycentric weights
  and NO perspective correction: texture coordinates and colors are
  affine across the triangle. Most PSP test scenes and simple
  homebrew are orthographic, where this matches; perspective-correct
  interpolation is a known PSP-2 simplification.
- GE execution is synchronous on submission: a list runs to END or to
  its SIGNAL stall point immediately, there is no queue, no parallel
  GE/CPU timing, and display list interrupts are not modeled. The
  SIGNAL model records the value and behavior word and stalls until
  Continue, which is the documented simplified form.
- BEZIER and SPLINE primitives are decoded, counted and flagged but
  do not draw (no tessellation in this slice); BBOX is counted only.
  CULLMODE is stored but backface culling is not applied. Dithering
  is off (the DITHER state is stored only). The GE command counters
  are diagnostics and are not part of the serialized state; a
  restored GE restarts its counters at zero.
- No TLB. KUSEG is direct-mapped, TLB instructions are no-ops, and the
  CP0 Random register is cosmetic without a TLB behind it.
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
