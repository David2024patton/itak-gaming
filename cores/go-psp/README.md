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

## Status: PSP-3 (HLE kernel, thread scheduler, ELF/PRX loader, pspautotests conformance)

PSP-1 delivered the CPU, memory, media foundations and the machine
skeleton (kept below). PSP-2 added the VFPU and the GE (kept below,
with corrections in the PSP-3 section). PSP-3 adds the software
environment homebrew actually runs against:

- `load`: the ELF32/PRX module loader (audited, see below).
- `kernel`: the HLE kernel: UID objects, memory partitions, a
  cooperative thread scheduler over one shared Allegrex CPU, sync
  objects, delays and vblank, and the syscall registry the loader's
  stubs dispatch into.
- `system` kernel mode: `LoadExecutable(data)` boots an ELF or PRX
  into the kernel; states version 3 carry the kernel in kernel mode.

### Loader audit outcome (PSP-3)

The pre-existing loader was audited against the uofw PRX
documentation and the corpus binaries. Two real bugs were found and
fixed: the stub table pointer names the first stub directly (stub j
is the 8 bytes at funcTable + 8j, a JR $ra word plus the syscall
word the kernel patches at StubAddr+4; the old code misread the
table), and the probe extent for ET_EXEC was computed wrong (now the
max segment end minus the min vaddr). Relocation r_info carries
three fields (type bits 0..7, OFS_BASE bits 8..15, ADDR_BASE bits
16..23) and all three are honored; the supported types are
R_MIPS_32, R_MIPS_26, R_MIPS_HI16/LO16 (with the documented AHL
addend combination), and R_MIPS_GPREL16 (validated and skipped: GP
relative entries are invariant under a uniform base shift). Any
other type is a load error naming the type. The loader was then
exonerated against a real corpus PRX by an independent Python ELF
parse: all 11 relocation sections of cpu_alu.prx use only the
modeled forms, and loaded memory matches the file word for word.
Unknown NIDs load fine; calling one returns SCE_KERNEL_ERROR_ERROR
(0x80020001, the model value) and is recorded in
`Kernel.UnknownCalls()` with module and NID. Loader tests are
synthetic ET_EXEC and PRX generators (including the HI16/LO16
sign-carry case) in `load/load_test.go`.

### Kernel model (PSP-3)

- UIDs: a sequential counter with a kind tag in the low byte
  (thread 0x01, sema 0x02, mutex 0x03, eventflag 0x04, memblock
  0x05, callback 0x06, lwmutex 0x07; the first object is 0x101).
- Partitions: user partition 2 at 0x08800000, 24 MB, first-fit with
  256-byte rounding; module images load at partition base + 0x4000
  (the load reserve the corpus binaries' layout requires).
- Threads: one Allegrex CPU time-shared across threads; the full
  CPU state (including the VFPU) is swapped per dispatch. New
  threads start with Status = CU1 | 0x11 and FCSR 0x00000E00.
  Cooperative scheduler: switches on blocking HLE calls and yields,
  reschedules on a higher-priority wakeup, documented
  10,000-instruction timeslice. When every runnable thread blocks,
  time fast-forwards to the next delay deadline or the frame end.
  `RunFrame` consumes its cycle budget across all threads, then
  delivers vblank (waiters wake in priority order, frame counter
  per frame).
- Sync objects: semaphores (microsecond timeouts converted at
  222 MHz), mutexes (recursive per attribute), event flags (AND/OR
  wait modes and clear modes), and the LwMutex family, whose lock
  state lives in a 32-byte guest workarea in the documented layout.
- Delays are microseconds (sceKernelDelayThread) and SysClock counts
  (sceKernelDelaySysClockThread; the SysClock unit is the
  microsecond count read from the cycle counter). The RTC tick is
  62135596800000000 plus the SysClock microseconds, so the clock
  boots at the Unix epoch.
- Boot: the main thread is "root", priority 0x20, 256 KB stack;
  a0 = 5 (the length of "umd0:"), a1 = the boot path string in the
  partition, $gp from the module info, $ra = 0 (the exit
  trampoline). Model values: devkit version 0x06060010 (firmware
  6.60), total memory 32 MB, sceGeEdramGetAddr returns 0x04000000,
  CPU clock 222 MHz.
- Console and IO: sceKernelPrintf (format subset %d %i %u %x %X %c
  %s %p %% with width and zero padding) and fd 1/2 writes go to the
  kernel console buffer. The virtual FS routes umd0:/disc0:
  read-only to the mounted ISO, ms0: and host0: to an in-memory
  store (`system.SetMemoryStickFiles`; host0: is the psplink device
  the corpus framework writes its output file to). The Stdio fd
  getters return 0/1/2 under their legacy (pre-SHA-1) NIDs.

### HLE coverage (PSP-3)

Registered (module, NID) handlers: ThreadManForUser thread set
(Create, Start, Exit, ExitDelete, Delete, GetThreadId,
ReferThreadStatus subset, RotateThreadReadyQueue), delays,
semaphores, mutexes, event flags, callbacks (registered, never
dispatched), LwMutex under both Kernel_Library and
ThreadManForUser; SysMemForUser partition allocation set and
memory queries; the time/RTC/power queries; LoadExecForUser exit
(sceKernelExitGame, and sceKernelSelfStopUnloadModule registered as
exit in the documented single-module approximation); StdioForUser
printf and the fd getters; IoFileMgrForUser (Open, Read, Write,
Close, Lseek, Getstat over the virtual FS; sceIoDevctl returns
invalid argument); sceCtrl (buffer reads/peeks fill SceCtrlData
from the host input state; sampling mode stored); sceDisplay
(SetMode stored, SetFrameBuf routed to the system display,
WaitVblankStart, GetVcount, IsVblank); sceGE (ListEnQueue executes
through the system GE synchronously, ListSync, DrawSync, Break,
EdramGetAddr); scePower (222 MHz, PowerTick no-op); sceRtc tick
queries; sceUtilityGetSystemParamInt (writes 0, returns 0); cache
writeback/invalidate calls are no-op successes. Anything else takes
the unknown-NID path (logged, error returned).

### States (PSP-3)

Version 3 = the version 2 body plus the kernel blob (objects,
partitions, thread contexts, console, sysclock), written only in
kernel mode. A fresh machine serializes to 35,674,041 bytes in
non-kernel mode (version 2) and 35,677,402 bytes just after a
kernel boot (version 3). Version 2 states still load in non-kernel
mode; a version 2 state loaded into a kernel-mode machine is
rejected, version 1 is rejected everywhere. Kernel state
serialization is deterministic: the round trip is byte-identical,
pinned by test.

### Synthetic gates (PSP-3, all pass)

Generator-built ET_EXEC and PRX binaries (hand-assembled MIPS)
cover: (a) hello: printf plus IoWrite emit exactly
"vfpu=1073741824,1077936128,0,0\nhello io\n"; (b) threads: two
priorities sharing a semaphore-guarded counter, console exactly
"start\ncount=2000 sum=95 len=10\n" with the trace order asserted;
(c) a 1,000 microsecond delay and a vblank waiter waking in the
asserted order across frames; (d) a synthetic ISO whose DATA.BIN
checksum the guest prints and matches; (e) a relocatable PRX
(HI16/LO16/R_MIPS_32) loading at 0x08804000, printing through a
resolved stub; (f) determinism: save mid-run, restore, byte
identical console and state; (g) an unknown NID returning the
logged error with the end marker printed; (h) mutex/eventflag/
partition semantics printing exactly
"mx 0 0 800201c5\nev 800201a8 800201af 0\npat 0 res 3\n".

### pspautotests corpus (PSP-3)

The open pspautotests suite is present locally (gitignored, never
committed) and wired through a presence-gated harness. Results
(byte compare after line-ending normalization):

- cpu_alu: 4/4 PASS (the gating group).
- fpu: 6/7. fpu_branch_hazard fails on one line: a bc1f in the
  delay slot of a compare reads the new FCC here; hardware reads
  the stale one. Documented pipeline hazard gap.
- icache: 0/1 on the tail byte only: the content lines match; the
  expected file ends without a trailing newline, a capture
  artifact of the original recording (the same artifact appears
  in lsu). Separately, the machine is cache coherent by
  construction, so cache-maintenance measurements cannot match.
- lsu: 1/3 (llsc passes). lsu's content matches to the same
  trailing-newline artifact; cacheop measures the cache model
  above.
- vfpu: 11/26 pass outright (callout, colors, convert,
  convert_scaled, minmax, minmax_tie, minmax_zero, overlap,
  prefix_branch, prefix_ctrl, vbranch). The other 15 fail only in
  documented classes:
  - The hardware approximation class: the VFPU function units
    (vrcp/vrsq/vsqrt/vlog2/vexp2 family, vsin/vcos/vasin, vrot)
    are low-bit approximations on hardware; this core evaluates
    the true functions. Results differ by 1-3 ULP with signs,
    zeros, NaNs and structure exact. Covers exact (from line 17),
    prefix_sat, specials (vrot rows only), vrot, vector's vasin
    rows, and gum's checkGlRotate sine residual sign (the
    residual magnitude matches exactly; the rest of gum passes,
    including all sceGum matrix ops).
  - prefix_consume: all prefix readbacks and all follow-up
    (then=) values are exact; 30 of 85 op result lines differ in
    per-op prefix roles that are not modeled (which operands
    some ops read raw, scalar prefix routing, product zero
    signs, vcst/vidt writing more than lane 0).
  - prefix_unpack: 11 of 34 lines differ by the prefix unit's
    stale-latch replay, whose update rule the test's own header
    records as not understood (zero is substituted, as the
    author suggests).
  - The operand layout paradox class: vregs Upgrade/Combine,
    the prefixes R-name prints, and matrix's vmidt section
    (which loads four quads from one address) are cases where
    the corpus's own witnesses disagree under any single
    field-based operand walk; the rule satisfying the majority
    of witnesses is kept and these sections differ.
  - overlap_vcrsp: the test's own header documents that the op
    does not read-before-write on hardware (an in-place hazard)
    and uses a size form assemblers refuse; not modeled.
  - vbranch_hazard: an mtvc CC write is visible to a branch 6
    instructions later on hardware, stale at +0/+1/+2; this core
    commits immediately. The plain vbranch test passes fully,
    including vcmp in the delay slot.
  - vavg: lanes 1-3 of the expected file are the recording
    machine's uninitialized register state; the controlled
    witness (specials) shows the ops write lane 0 only.
  - vrnd: seed expansion and the RCX readback path are exact;
    the draw recurrence is modeled, not solved.
  - vector: the guest prints 3309 of 5330 lines (the produced
    region differs only in the vasin approximation rows), then
    its PC loops in an SDK string routine. The tail sections
    (including checkConstants) are unverified; see the VFPU
    flag status.

### PSP-3 corrections to the PSP-2 VFPU

The corpus refuted the PSP-2 build's VFPU operand layout, which
was self-consistent but invented. PSP-3 replaces it, from corpus
witnesses: the arithmetic operand layout (matrix from bits [4:2],
singles and vectors as documented in the source header), the
load/store field layout with LVL.Q/LVR.Q window semantics derived
from the corpus unaligned rows (primary opcode 0x35 is LVL.Q/
LVR.Q; the LDC1 reading PSP-2 assumed does not exist on
Allegrex), MFV/MTV versus MFVC/MTVC split by word bit 7 with the
control index in the low byte, BC2's imm3 CC-bit select, the
planar prefix layout and constant table {0, 1, 2, 1/2, 3, 1/3,
1/4, 1/6}, prefix power-on 0xE4, the prefix consumption reset
(every compute op resets all three prefix registers; an out of
range swizzle lane poisons the op's result to zero), the VFPU
NaN pattern (sign over 0x7F800001, with per-op sign rules the
exact test pins), the hardware half-float truncation/flush
algorithms, the sort/butterfly block with its equal-value tie
collapse, the pack/unpack family with its size-bit and
destination placement rules, VCST's own 19-entry constant
table, vscmp, vsbn/vwbn, vsbz/vlgb, vrot, the reduction ops
(vfad/vavg write lane 0 only), and the matrix operand decode:
vtfm/vhtfm dot the matrix columns with the vector and write a
strided row; vmmul decodes each slot's anchor from its own bits
(no slot transposes; see matrixOriginVmmul) and stores the
product transposed; vmscl reads its matrix with bit 5 cleared.
Scalar VV operands read at the scalar position the dot family
uses (offset = low field bits + high field bits), which the
minmax_tie, vbranch, overlap and prefix_ctrl witnesses jointly
pin. Also corrected in the CPU: the branch-likely family,
Allegrex MAX/MIN, multiply-accumulate and CLZ/CLO placement
(SPECIAL, not SPECIAL2; the SPECIAL2 forms are kept as a
documented superset), the DIVU by-zero rule, WSBW completing the
BSHFL map, the LWL/SWL shift forms, the LL/SC reservation
lifetime (a syscall or thread switch breaks it, a successful SC
does not), the FPU compare predicate formula, the BC1 tf/nd bit
layout, thread Status modeling, CVT.W.S saturation and NaN
rules, the FS-bit denormal model, scalar NaN propagation, exact
add/sub via a residual-checked rational path, FPU
cause/flags maintenance, and the fcr31 writable mask
0x0181FFFF. Primary opcode 0x3F executes as a no-op: the word
0xFFFF0000 appears inside the SDK printf float path linked into
the corpus binaries, and neither published opcode map assigns
0x3F; the encoding stays flagged as an open question in the
source.

VFPU cross-check flag status: the RCX range 136-143 is
corroborated and closed (seeding, form, and mfvc readback all
match the corpus). VCST is implemented from the hardware
constant list and exercised by the corpus's vector tests, but
its dedicated checkConstants section sits in vector's
unproduced tail, so it stays exercised, not adjudicated. The
fill-group encodings are confirmed from the corpus matrix
binary's own words (f 3 vmidt, 6 vmzero, 7 vmone). VREPL/VSPLT
stay flagged: no corpus binary uses them, so the PSP-2 slots
and semantics remain unverified. The VRND draw recurrence and
the RCX shift-window dynamics stay modeled, not solved.

## PSP-2 (VFPU, GE display list processor, system integration)

PSP-2 adds the two coprocessor-class units (the VFPU text below
is the PSP-2 build's; PSP-3 corrected its operand encodings
against the corpus, see the PSP-3 corrections above):

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
- PSP-3 (done): HLE kernel: threads and scheduling, syscall dispatch,
  the ELF/PRX module loader for homebrew, controller and display HLE,
  with pspautotests as the conformance gate (the tests/cpu set,
  including the vfpu group as the executable cross-check for the VFPU
  encodings and math; results in the PSP-3 section above).
- PSP-4: audio (sceAudio plus a Media Engine decision), the libretro
  shell, a homebrew enginetest through the shell, and app wiring for
  the android-64bit build.

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
- Matrix operations (VMMUL, VMSCL, VTFM/VHTFM, matrix moves) read
  their operands raw (no source prefix) in this core; the prefix
  consumption reset still applies to them as compute ops. The
  documented operand overlap restrictions are not enforced, except
  that ordinary VV ops read all inputs before writing (the corpus
  overlap test passes; the vcrsp in-place hazard is the documented
  exception). VCMOV with selector 7 is unspecified in the
  documentation; this core treats it as the per-lane inverted form.
- Two encodings could not be confirmed in the published documents:
  VREPL and VSPLT appear in neither reference text, so they sit at
  free function values in the unary group (0x08 and 0x09) with the
  semantics the instruction names imply, flagged in the source; no
  pspautotests binary uses them either (PSP-3 cross-check, still
  open). VCST uses the hardware 19-entry constant list from the
  published documentation (PSP-3; the corpus checkConstants
  adjudication is pending only because the vector test stalls
  before that section, see the PSP-3 corpus results). The random
  control registers are mapped at 136 to 143 (8 words), the range
  the corpus corroborates.
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
