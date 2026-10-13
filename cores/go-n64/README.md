# go-n64

A from-scratch Nintendo 64 core in Go for iTaK Gaming, built
the same way as the go-genesis, go-gameboy, go-nes, and
go-snes cores in this repository: every chip is our own
code, written from published hardware documentation (the
MIPS64 architecture manuals, the VR4300 hardware manual,
and the public N64 hardware documentation), and wrapped in
a libretro-compatible shell for the app in a later slice.
No third-party Go dependencies.

## Status

**N64-1: R4300 CPU + memory map + cartridge foundations
+ system skeleton.** The processor runs real MIPS code
with full address translation.

**N64-2 (this slice): the RSP.** The signal processor is
a full second computer: its own scalar MIPS I core, its
SP DMA engine, and the complete vector unit, all wired
into the bus and the system interleave. Nothing renders
yet, and games cannot boot until the PIF/CIC slice
lands.

- `chips/r4300`: the VR4300 (MIPS III + 64-bit
  extensions). Full integer set including the 64-bit loads
  and stores, DADD/DSUB with overflow traps, DMULT/DDIV,
  the unaligned LWL/LWR/SWL/SWR and LDL/LDR/SDL/SDR
  families (big-endian semantics), LL/SC with the link
  bit, branch-likely nullification, traps, and delay slots
  tracked exactly (an exception in a delay slot reports
  the branch address in EPC with Cause BD set). CP0 has
  the full register set: a 32-entry TLB with TLBWR, TLBWI,
  TLBR, and TLBP, refill/invalid/modified exceptions with
  the documented vector selection (TLB refill at offset 0,
  XTLB refill at 0x80 in 64-bit mode, general at 0x180,
  the special interrupt vector at 0x200 when Cause IV is
  set), Count/Compare timer interrupt on IP7, ERET with
  EPC/ErrorEPC and EXL/ERL handling. CP1 implements the
  register file under both FR=0 (even/odd pairs) and FR=1
  mappings, MFC1/MTC1/DMFC1/DMTC1/CFC1/CTC1, and the core
  arithmetic (ADD, SUB, MUL, DIV, SQRT, ABS, MOV, NEG) in
  single and double, all conversions, the ROUND/TRUNC/
  CEIL/FLOOR family, C.cond into all 8 condition bits,
  and BC1T/BC1F/BC1TL/BC1FL.
- `chips/rsp`: the Reality Signal Processor. DMEM and
  IMEM (4 KB each) with 12-bit wrapped addressing, owned
  by the package and visible through the SP window. The
  scalar core is the documented MIPS I subset: the
  integer ops minus the 64-bit forms and the TLB/CP0
  machinery, with LWL/LWR/SWL/SWR, MULT/DIV into HI/LO,
  delay slots, JAL/JALR links, and BREAK (sets the broke
  state, halts, and raises the MI SP interrupt when
  interrupt-on-break is enabled). No exceptions besides
  BREAK; reserved encodings are NOPs, and ADD/ADDI do not
  trap on overflow, per the RSP documentation. Its CP0
  is the SP register set: SP_MEM_ADDR (with the
  DMEM/IMEM select bit), SP_DRAM_ADDR, SP_RD_LEN and
  SP_WR_LEN with the full length/count/skip multi-row
  DMA format, SP_STATUS with the documented read state
  bits and write set/clear bit pairs (halt, broke,
  interrupt, interrupt-on-break, signals 0-7), the
  test-and-set SP_SEMAPHORE (read sets, write clears),
  and SP_PC, writable only while halted. The vector unit
  implements the full documented COP2 set: 32 vector
  registers of 8 lanes, the 48-bit accumulator with VSAR
  slice read-out, VCC/VCO/VCE control registers with
  CFC2/CTC2, element broadcast (scalar, quarter, half,
  whole) applied per the manual, the multiply family
  (VMULF, VMULU, VMULQ, VMUDL/M/N/H, VMACF, VMACU,
  VMACQ, VMADL/M/N/H, VRNDN, VRNDP) with the documented
  accumulator clamp rules, VADD/VSUB/VADDC/VSUBC with the
  VCO carry chain, VABS, the compares (VLT, VEQ, VNE,
  VGE; VGT and VLE are the same functs with operands
  exchanged and have no encoding of their own), the clip
  family VCH/VCL/VCR implemented step for step from the
  manual algorithms, the logicals, VMOV, VMRG, VSAR, and
  VRCP/VRCPL/VRCPH/VRSQ/VRSQL/VRSQH with the latched
  double-precision protocol. Every documented vector
  load and store is implemented: LBV, LSV, LLV, LDV,
  LQV and LRV with the unaligned merge rule, the packed
  LPV, LUV, LHV, LFV, the transpose LTV/STV with the
  documented start-element rotation, and SWV. Microcode
  is cart-supplied data that this processor executes;
  none is embedded.
- `bus`: the physical map. RDRAM (4 MB, or 8 MB with the
  Expansion Pak option) with mirroring, RDRAM register
  page, the SP memory window, SP registers, and SP PC
  routed to the real RSP (its interrupt output drives
  the MI SP bit), DP/VI/AI/RI/SI
  register storage, MI with the documented paired-bit
  mask semantics and the interrupt line wired to the CPU,
  and the PI with real DMA between the cartridge window
  and RDRAM (length +1 semantics, the odd-length round-up,
  completion interrupt). PIF RAM is addressable storage;
  the PIF itself is slice N64-4.
- `cart`: header parsing for .z64/.v64/.n64 dumps with
  detection and normalization to the canonical big-endian
  layout, and CIC family detection computed from the
  ROM's own boot-area CRC32 against the documented
  checksum values. No boot code or CIC seed data is
  embedded anywhere in this core.
- `system`: the machine. LoadROM, RunFrame with a
  93.75 MHz / 60 frame budget (1,562,500 cycles), a blank
  320x240 XRGB8888 framebuffer, SetButtons in the
  standard controller bit layout, and versioned
  Serialize/Deserialize. Version 2 states add the full
  RSP block, the MI interrupt and mask registers, and
  the interleave remainder (4,205,131 bytes with 4 MB;
  8,399,435 with 8 MB). While the RSP is not halted it
  executes interleaved with the CPU: 2 RSP cycles per 3
  CPU cycles (62.5 MHz against 93.75 MHz), credited
  exactly after every CPU step at system step
  granularity.

## Verification

There is no Tom Harte single-step corpus for MIPS (the
ProcessorTests tree was checked 2026-10-13: 6502 family,
65816, 680x0, 8088, SPC700 only), so the CPU is gated on
41 unit tests derived from the manuals: worked 64-bit
arithmetic and multiply/divide vectors, divide-by-zero
results, overflow traps, delay-slot execution and
nullification, exception-in-delay-slot EPC/BD, misaligned
and user-mode segment faults, the Count/Compare interrupt
through the special vector, LL/SC, big-endian partial
loads and stores, TLB map/invalidate/modify/probe with
4 KB and 16 KB pages, ERET flow, FPU register mapping
under both FR settings, FCSR rounding-mode conversions
(all four modes, including exact halfway cases for
ADD.S), compare-and-branch, and divide-by-zero flags.
Bus, cart, and system tests cover PI DMA (including the
odd-length round-up and the PI interrupt), MI mask
semantics, endianness normalization of all three dump
layouts, and a synthetic MIPS program that installs a TLB
mapping, stores through it, DMAs 64 bytes from the cart,
and checksums them: a save state taken mid-run restores
into a fresh machine and produces byte-identical RDRAM,
registers, and results.

The RSP is gated on hand-derived numeric vectors from
the RSP programmer's documentation (19 test functions in
the package, plus 3 system gates): scalar arithmetic with the LWL/LWR merge, branch links
and delay slots, the SP_STATUS write bit pairs, the
semaphore test-and-set, multi-row DMA with skip in both
directions (register API and RSP-driven MTC0), BREAK
interrupt behavior, and a serialization round trip.
Vector gates pin the VMULF 0x8000 x 0x8000 corner
(result 0x7FFF, accumulator 0x80008000 read back through
VSAR), a three-op VMAC chain that accumulates and
saturates, the VADDC/VSUBC carry chain with VCO read
back through CFC2, quarter/half/whole element
broadcasts, the combined VSUBC-then-VEQ double-precision
compare, VCH/VCL on the equal, clamp-high, clamp-low,
and double-precision refinement cases, VRCP/VRSQ values
(0.5 gives exactly 2.0, the double-precision 1.0 gives
exactly 0x8000), the LQV+LRV unaligned pair, the
transpose store with its rotation, and the packed
loads. The end-to-end gate runs through the system: the
CPU DMAs a hand-assembled RSP program into IMEM, starts
it, and the RSP computes an 8-lane dot product
(0x7800 in S1.15) into DMEM and BREAKs; the CPU takes
the MI SP interrupt, and a state saved while the RSP
was mid-program restores and finishes byte-identically.

The open-source n64-systemtest homebrew ROM would be a
useful extra gate, but its official GitHub has no
releases and no committed binary (it is a Rust project
that needs a MIPS toolchain to build), and it normally
boots through the PIF process that lands in N64-4. A
report-only, presence-gated harness
(`system/homebrew_test.go`) boots it in raw CPU mode if a
legally built copy is ever placed in `testroms/`; it is
not a gate today.

## Roadmap

- **N64-3:** RDP software rasterizer (its own command
  processor), VI output, AI audio.
- **N64-4:** PIF/CIC boot (the CIC seed exchange
  implemented from documentation, no dumped boot code),
  the libretro shell, a real-game enginetest, and app
  wiring for the android-64bit build.

## Honesty notes

- Boot requires the PIF/CIC process, which lands in
  N64-4. In this slice the CPU starts at the reset vector
  and test harnesses seed PC and memory directly, the
  same way the sibling cores began.
- Cycle counting is a flat model: 1 cycle for simple
  instructions, 2 for loads and stores, and flat class
  totals for multiply (5), 64-bit multiply (8), divide
  (37), 64-bit divide (74), TLB operations (2), ERET (3),
  and floating point (4, 5 for multiply, 23 for divide and
  square root). Count advances at half rate. Random
  decrements once per instruction, not per cycle.
  Cache effects, pipeline stalls, and bus contention are
  not modeled, and PI DMA completes instantly instead of
  streaming at bus speed.
- Floating point: single-precision arithmetic is computed
  in float64 and rounded with the FCSR mode applied, so
  all four rounding modes are honored for .s operations;
  conversions honor the mode everywhere. Double arithmetic
  uses the host round-to-nearest result. FCSR flags and
  causes are recorded (including divide-by-zero and
  invalid), but an enabled-flag FPE trap is not raised in
  this slice.
- 64-bit operations are always available in kernel mode
  (matching how the N64 kernel runs with KX clear);
  SX/UX gate them in supervisor and user modes. 64-bit
  addressing (xkphys, xkseg, xkuseg) requires the mode's
  KX/SX/UX bit. TLB key comparison uses full-width
  shifted VPN2 keys, which is exact for the 32-bit
  addressing the N64 uses; exotic 64-bit VPN2 encodings
  beyond that are untested.
- CACHE and PREF are no-ops (caches are not modeled).
  Cache Error exceptions are not raised. Watchpoints are
  stored but never fire. The Config register reports the
  documented power-on value; its cache geometry fields are
  informational only.
- Divide by zero follows the common MIPS convention
  (quotient all-ones or 1 by dividend sign, remainder is
  the dividend); the architecture leaves it undefined.
- RSP DMA executes atomically on the length-register
  write: the whole multi-row transfer lands at once, so
  the DMA busy and full flags always read 0 and a CPU
  write that sets halt stops the RSP at the next system
  step boundary rather than mid-instruction. The DRAM
  address is taken modulo 2^24 and then masked to the
  installed RDRAM size; the SP address wraps inside its
  4 KB bank; the length field contributes (length &
  0xFF8) + 8 bytes per row, per the manual.
- The RSP executes one instruction per cycle in the
  interleave accounting. Pipeline behavior is not
  modeled: there are no load-use delay slots (results
  are available to the next instruction immediately),
  and scalar and vector accesses see DMEM only, the
  12-bit space the instruction pseudocode documents.
- Reciprocals: VRCP and VRSQ are computed exactly in
  the documented formats (single-precision S1.15 input
  to S16.15 output, double-precision S15.16 input) by
  integer and float64 math. The hardware instead uses a
  lookup table with roughly 10 bits of precision, so the
  low bits of a result can differ from hardware even
  though ours are the correctly rounded values of the
  documented function; the examples the documentation
  works (0.5 to 2.0, 1.0) match exactly.
- Two places where the RSP documentation disagrees with
  itself are resolved for its worked algorithms: VADDC
  writes the not-equal flag byte as 0 (its Operation
  section, against one architecture sentence), and the
  VCH/VCL clip flags sit in VCC with the less-or-equal
  bits low and the greater-or-equal bits high (the
  register-format figure and the Operation sections,
  against one chapter sentence).
