# go-n64

A from-scratch Nintendo 64 core in Go for iTaK Gaming, built
the same way as the go-genesis, go-gameboy, go-nes, and
go-snes cores in this repository: every chip is our own
code, written from published hardware documentation (the
MIPS64 architecture manuals, the VR4300 hardware manual,
and the public N64 hardware documentation), and wrapped in
a libretro-compatible shell for the app.
No third-party Go dependencies.

## Status

**N64-1: R4300 CPU + memory map + cartridge foundations
+ system skeleton.** The processor runs real MIPS code
with full address translation.

**N64-2: the RSP.** The signal processor is a full
second computer: its own scalar MIPS I core, its SP DMA
engine, and the complete vector unit, all wired into
the bus and the system interleave.

**N64-3: the RDP, VI, and AI.** The display
processor consumes display lists from RDRAM or RSP DMEM
and rasterizes into RDRAM; the VI presents the
programmed color image as the system framebuffer; the
AI streams stereo audio from RDRAM at the programmed
DAC rate.

**N64-4 (this slice): the PIF, the HLE boot, save
devices, and the libretro shell. The core is complete.**
The PIF processes Joybus frames for four controllers
and the cartridge channel; the SI performs real DMA
between RDRAM and PIF RAM; the boot is an HLE
reimplementation of the documented PIF/IPL process
(no dumped boot code anywhere); EEPROM, SRAM, and
FlashRAM save devices work through the PIF and the PI
window; and the libretro shell runs the core in the
app's engine, gated by a synthetic ROM that boots
through the real chain (PIF state, a hand-written
IPL3 in DMEM, then the game).

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
- `rdp`: the Reality Display Processor. A command
  processor executes 64-bit display-list words from
  RDRAM (DP_START/DP_END), or from RSP DMEM when the
  XBUS/DMEM bit in DP_STATUS is set; the DP registers
  are reachable both on the bus page and through the
  RSP's CP0 window (registers 8..15), and execution is
  synchronous, completing inside the DP_END write.
  FULLSYNC raises the DP interrupt through MI; the
  pipe, tile, and load syncs are ordering no-ops in this
  model. The full state command set is implemented
  (image and tile registers, all color registers,
  scissor, primitive depth, convert and key registers,
  other modes and combine mode as the single merged
  register the RDP receives). TMEM is 4 KB with eight
  tile descriptors; LOADBLOCK (with the DxT source-line
  walk), LOADTILE, and LOADTLUT (with the documented
  palette quadrication) feed it, and Load Block and
  Load Tile produce identical TMEM for identical
  content. Texture sampling covers RGBA 16/32, YUV 16
  (the documented two-stage conversion through the
  programmed K coefficients), CI 4/8 through the TLUT
  (RGBA or IA palette type), IA 3/4/8/16, and I 4/8,
  with wrap, mirror, and clamp addressing per axis and
  the tile shift fields. The rasterizer walks
  triangles in the documented coefficient format (edge
  slopes 16.16, Y in 11.2 subpixel units), evaluates
  shade, texture, and z with the exact plane equations,
  applies perspective division through the W
  coefficient, and runs the documented per-pixel
  pipeline: z compare in the four z modes over an
  18-bit internal depth, chroma key, the two-cycle
  color combiner with the full documented mux tables,
  and the blender (the documented (P*A + M*B) / (A + B)
  equation over the documented input selects) with
  coverage algebra per the cvg_dest mode. Texture
  rectangles (both orientations) and fill rectangles
  follow the documented inclusive/exclusive corner
  rules per cycle mode; the fill color uses the
  documented multi-pixel packing for 8 and 16-bit
  images.
- `vi`: the video interface. The full register set
  (control, origin, width, interrupt line, burst and
  sync timing, video windows, and the 2.10 fixed-point
  X/Y scales) with an exact rational half-line counter:
  a field lasts exactly the 1,562,500-cycle system
  frame spread over the programmed half-line total,
  the VI interrupt fires once per field at VI_INTR
  (writing VI_V_CURRENT clears it), and the field bit
  toggles in interlaced totals. Present() scales the
  programmed RGBA5551 or RGBA8888 color image to the
  video window as XRGB8888, with the documented square
  root gamma table applied when gamma is enabled;
  the resampling filters, dither, and divot are not
  applied (their registers are stored).
- `ai`: the audio interface. DRAM address, length,
  control, status, DAC rate, and bit rate registers
  with the documented read mirrors (write-only
  registers read back the current remaining length)
  and the two-deep transfer queue. Sample rate is the
  NTSC video clock over (DACRATE + 1); consumption is
  exact rational accumulation from the CPU clock.
  Following the hardware-test documentation, the AI
  interrupt fires when a transfer STARTS (so software
  can queue the next buffer early) and is acknowledged
  by writing STATUS. Samples are big-endian signed
  16-bit stereo read from RDRAM at consumption time.
- `pif`: the Peripheral Interface. It owns the 64-byte
  PIF RAM that the SI exchanges with the CPU. Writing
  the command byte (or an SI DMA write) parses the
  documented Joybus frame format: per-channel TX/RX
  counts, skip and reset escapes, and the frame-end
  code; an SI DMA read executes the parsed handshakes
  in reverse channel order, channels 0-3 being the
  controllers and channel 4 the cartridge (CIC and save
  chip). Commands implemented: info (0x00/0xFF, the
  documented 3-byte signatures: controller 0x0500 with
  pak status, EEPROM 0x0080/0x00C0 on the cart channel),
  controller state read (0x01), pak read/write (0x02/
  0x03, 32-byte blocks at the documented address
  encoding whose low 5 bits are the published
  positional XOR checksum, data protected by the
  documented CRC-8, polynomial 0x85), EEPROM block
  read/write (0x04/0x05), and the RTC commands
  (0x06/0x07/0x08) answered as no RTC fitted, which is
  what the documentation prescribes (info returns three
  zero bytes). Memory Pak and Rumble Pak are modeled
  per port (a freshly inserted pak behaves absent until
  an Info reports status 3, per the docs). Error
  behavior follows the documentation too: an absent
  device sets the RX no-device bit, an unanswered
  command sets the timeout bit, and a bad pak address
  checksum gets silence plus the checksum-error bit on
  the next Info.
- `bus`: the physical map. RDRAM (4 MB, or 8 MB with the
  Expansion Pak option) with mirroring, RDRAM register
  page, the SP memory window, SP registers, and SP PC
  routed to the real RSP (its interrupt output drives
  the MI SP bit), the DP page routed to the RDP, the
  VI and AI pages routed to their devices (each device
  interrupt drives its MI bit), the SI with real
  64-byte DMA both directions between RDRAM and PIF RAM
  (a read first executes the PIF's parsed frame; either
  direction raises the MI SI interrupt on completion),
  MI with the documented paired-bit mask semantics and
  the interrupt line wired to the CPU, and the PI with
  real DMA between the cartridge window and RDRAM
  (length +1 semantics, the odd-length round-up,
  completion interrupt). PI domain 2 at 0x08000000 is
  the save window: SRAM as a direct 32 KB store and
  FlashRAM behind its documented command protocol.
  The PIF RAM window routes to the PIF device.
- `cart`: header parsing for .z64/.v64/.n64 dumps with
  detection and normalization to the canonical big-endian
  layout, and CIC family detection computed from the
  ROM's own boot-area CRC32 against the documented
  checksum values. No boot code or CIC seed data is
  embedded anywhere in this core; the CIC seed values
  used by the boot are the published algorithm
  constants. The package also carries the save devices
  (EEPROM 4K/16K with the documented block wrap, SRAM,
  and the FlashRAM command state machine with its
  documented status words) and the published IPL3
  checksum algorithm, computed over the ROM's own
  bytes (0x1000..0x101000) with the per-CIC seed and
  magic constants.
- `system`: the machine. LoadROM loads a cartridge;
  Start performs the HLE boot (below) and
  RunFrame advances a 93.75 MHz / 60 frame budget
  (1,562,500 cycles). The framebuffer presented by the
  VI (blank 320x240 when the VI is off), AudioBuffer
  draining the AI at the programmed rate, per-port
  input (SetButtonsPort and SetStick for all four
  controllers), per-port pak insertion, the save API
  (SetSaveType, SaveRAM, SetSaveRAM), and versioned
  Serialize/Deserialize. Version 4 states add the SI
  registers, PIF RAM, the full PIF device state, and
  the save device contents (6,604,438 bytes with 4 MB;
  12,895,894 with 8 MB).
  The VI and AI advance on the cycles each RunCycles
  call actually consumed. While the RSP is not halted it
  executes interleaved with the CPU: 2 RSP cycles per 3
  CPU cycles (62.5 MHz against 93.75 MHz), credited
  exactly after every CPU step at system step
  granularity.

## The boot model

Start() reimplements the documented boot process in
HLE, the same approach other emulators use; no byte of
the PIF boot ROM or of any CIC boot code is embedded
or executed from a dump:

1. The CIC family comes from the cart package's CRC32
   detection of the ROM's own boot area. When it does
   not match (a synthetic or homebrew ROM with original
   boot code cannot reproduce a genuine boot area's
   CRC32, so detection is impossible for it), the boot
   uses an explicit system.SetCIC family if one was
   set, and otherwise defaults to CIC-6102, the most
   common family. This limitation is inherent to the
   documented detection method and is stated openly.
2. The documented initial state is set up: PIF RAM
   carries the post-boot word (region NTSC and both CIC
   seeds) with the command byte's 0x80 bit set; the
   cartridge's first 0x1000 bytes (header plus IPL3)
   are copied to RSP DMEM exactly as IPL2 leaves them;
   CP0 Status and Config get their documented IPL1
   values (0x34000000, 0x0006E463); the stack pointer
   is 0xA4001FF0; the CIC seed byte reaches the code in
   $s6 and the TV type (NTSC, 1) in $s4, as IPL2
   arranges; and the CPU starts at 0xA4000040, the
   IPL3 entry in DMEM.
3. The IPL3 checksum (the published algorithm over ROM
   bytes 0x1000..0x101000 with the family's seed and
   magic) is computed and compared with the header
   words. A mismatch is reported through
   BootChecksumOK() and never stops the boot, matching
   common emulator behavior.
4. From there the ROM's own IPL3 runs on our CPU: it
   initializes what it needs and copies the game image
   from cart offset 0x1000 to the header entry address,
   then jumps. Genuine IPL3 code would run the same
   way; the PIF security challenge is the one piece
   modeled rather than reproduced (the cart channel
   reports ready; see the honesty notes).

LoadROM alone does not boot: low-level tests load a
cartridge and seed the CPU themselves, and the shell's
retro_reset reboots through Start().

## Save devices

N64 cartridges do not declare their save device in the
ROM header, and this core ships no game database by
design. The host chooses: system.SetSaveType (None by
default, EEPROM 4K, EEPROM 16K, SRAM, or FlashRAM),
which the libretro shell exposes as the core option
gon64_save_type. Controller Paks are per-port and
independent of the cart device (port 0 gets a Memory
Pak by default at load). Save contents are exposed as
RETRO_MEMORY_SAVE_RAM and join save states.

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

The RDP, VI, and AI are gated at two levels. Package
tests pin the DP register and freeze behavior, the
FULLSYNC interrupt, fill packing for 8, 16, and 32-bit
images, Load Block versus Load Tile TMEM equality,
combiner arithmetic (including the documented 255/256
scaling), the blender equation against worked values,
scissor clipping, CI4 TLUT lookup, IA and I expansion,
YUV conversion of the classic red sample to exactly
(255, 0, 0), the three-texel filter's diagonal weights,
the primitive-depth path, and state round trips. The
VI tests pin the NTSC 240p interrupt (exactly once per
field), V_CURRENT behavior, and presentation probes;
the AI tests pin the start-of-transfer interrupt, the
length mirror reads, and a 32,006 Hz sine measured from
drained samples. System gates run a hand-built display
list through the DP registers on the real CPU: a
filled background, a shaded z-tested triangle (probe
0xC88D at (60,50) with the gradient reading exactly
20), a closer magenta triangle winning the overlap at
(100,80), a farther green one losing it, a
LOADBLOCK-fed textured rect (texel probes 0x29E1 and
0xFFE1), a DP interrupt counted by the CPU's handler,
and VI framebuffer probes matching the color image.
The same style of list runs out of RSP DMEM through
the RSP's CP0 DP window. The AI system gate plays a
1 kHz sine through two queued transfers: 997.69 Hz
measured against the 1000.19 Hz ideal, exactly two
interrupts (one per transfer start), and a decreasing
LENGTH readback. A determinism gate executes the scene
with audio playing, saves mid-run, restores into a
fresh machine, and requires byte-identical state,
framebuffer, and continuation audio.

The PIF, SI, saves, and boot are gated at three
levels. Unit tests pin the Joybus CRC-8 against
hand-computed values (32 zero bytes give 0x00, [0x01]
gives 0x85, [0xFF] gives 0x8D), the documented pak
address-checksum example (address 0x0020 encodes
0x15), the pak fresh/inserted lifecycle, the
bad-checksum timeout plus the Info error bit, skip
channels, the command byte services, the FlashRAM
command protocol (including the status readback
0x1111800100C2001D), the EEPROM 4K block wrap, an
SI Joybus round trip through the bus with the MI SI
line observed, and the IPL3 checksum initial values
against the published table for all four CIC
families (6101/6102 0xF8CA4DDC, 6103 0xA3886759,
6105 0xDF26F436, 6106 0x1FEA617A).

The swap-class gate (system/system_n64_4_test.go) is a
synthetic ROM with an original hand-written IPL3: it
boots only through LoadROM + Start and the payload
then sets six milestone flags in order (payload entry
and PIF boot termination, display list executed with
the DP interrupt counted, AI sine started with its
interrupt counted, controller poll plus channel-4 info
signature verified, Controller Pak write/read/CRC
round trip verified by the guest, EEPROM block round
trip verified by the guest). Assertions: milestones
in order 1 through 6, the CIC seed recorded from $s6
is 0x3F, the boot checksum reports OK (and a
corrupted-checksum variant still boots, reported
not-OK), framebuffer probes match the N64-3 scene
(0xFFFF00FF at (100,80) over 0xFF214284), the polled
buttons are exactly the A and Start mask the host
set, the drained sine measures 999.59 Hz against the
1000.19 Hz ideal, and a state saved mid-spin restores
into byte-identical continuation (state, framebuffer,
and audio). The same ROM through the desktop
libretro build and go_backend/cmd/enginetest reports
ALL TESTS PASSED (320x240 non-blank video,
non-silent audio, per-player save states written,
restore frame hash equal to the save point).

The open-source n64-systemtest homebrew ROM would be a
useful extra gate, but its official GitHub has no
releases and no committed binary (it is a Rust project
that needs a MIPS toolchain to build). A report-only,
presence-gated harness (`system/homebrew_test.go`)
boots it in raw CPU mode if a legally built copy is
ever placed in `testroms/`; it is not a gate today.

## Roadmap

- **N64-1 through N64-4: done. The core is complete:**
  R4300 + RSP, RDP/VI/AI, PIF/SI with controllers,
  paks, and save devices, the HLE boot, the libretro
  shell, and the app wiring for the android-64bit
  build. Remaining work is accuracy polish from the
  honesty notes below, driven by real-game reports.

## Honesty notes

- Boot: the PIF security challenge/response math is
  modeled as passed (the command byte's challenge bit
  is accepted and cleared, the response bytes are not
  computed), and the CIC-6105 challenge command is a
  documented no-op. Games that verify the challenge
  response contents would notice; the documented boot
  flow and state are otherwise reproduced. The
  post-boot PIF RAM word carries the documented
  contents (region and both seeds); its exact byte
  packing beyond those contents is this core's
  arrangement.
- CIC detection reads the ROM's own boot area, so a
  ROM whose boot code is original (synthetic test ROMs,
  some homebrew) cannot be family-detected: the boot
  then uses system.SetCIC or the 6102 default. The
  shell does not guess beyond that.
- Joybus timing is instantaneous: handshakes complete
  inside the SI DMA that triggers them, and the
  frame's 0x04/0x08 timing bits are accepted without
  effect. The PIF clock divider and per-bit wire
  timing are not modeled.
- FlashRAM erase granularity is the latched 128-byte
  page, per the published command table in the
  hardware notes used here; some documentation
  describes larger physical erase blocks underneath
  the same command interface.
- The RTC commands answer as no RTC fitted (three zero
  bytes on info, silence otherwise), per the
  documentation for a cartridge without a clock. No
  64DD devices are modeled.
- The shell declares 60.0 fps and 44,100 Hz in its AV
  info: the NTSC field rate is simplified to 60, and
  the AI stream (at whatever rate the game programs)
  is resampled to 44,100 with a linear interpolator.
  The core's internal timing stays exact.
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
- RDP execution is synchronous: a list runs to
  completion inside the DP_END write (or inside the
  status write that clears freeze on a pending list),
  so the DP busy bits always read clear, DP_CURRENT
  reads START before and END after, and DP_CLOCK is an
  activity counter (total command words processed)
  rather than a wall clock. Flush is stored and
  reported but has no separate effect.
- Rasterizer coverage is computed from horizontal
  fractional coverage in eighths; vertical coverage is
  treated as full because the model snaps edges to
  scanlines. Attribute interpolation evaluates the
  exact plane equations (Dx and Dy derivatives anchored
  at the top vertex) instead of stepping the De
  derivatives along the major edge as hardware does;
  the planes are the same for well-formed coefficient
  sets.
- The texture filter in 2x2 sample mode is the
  documented three-texel triangle blend, not a
  four-sample bilinear average. The mid-texel mode
  applies its half-texel offset only (the stored
  register behavior beyond that is not modeled).
  Sharpen and detail texture modes fall back to the
  LOD path. LOD level selection uses derivative
  magnitudes at the top vertex and the primitive's LOD
  fraction, clamped by the Set Prim Color minimum
  level; texel1 in 2-cycle mode is the next tile
  descriptor.
- Depth: the stored RDRAM word keeps (z18 >> 2) in the
  high 14 bits and a 2-bit dz code decoded through the
  table {16, 64, 256, 1024} in z18 units, so dz is
  exact only at those four magnitudes. Interpenetrating
  and decal z modes share the (z <= stored + dz) test
  in this model.
- Coverage for 16-bit color images lives in a side
  store inside the RDP (serialized with its state)
  rather than in RDRAM, so RDRAM pixels stay pure
  RGBA5551; hardware hides three coverage bits in the
  pixel's low bits. For 32-bit images the coverage
  rides the stored alpha's low bits under the clamp,
  wrap, and zap cvg_dest modes, as on hardware.
- The documented TMEM odd-line 32-bit half interleave
  is not reproduced: loads write the logical texel
  layout and fetches read it back, which keeps Load
  Block and Load Tile in exact agreement and sampling
  exact for normal images; only software that pokes
  TMEM and expects the interleave pattern back would
  notice.
- The combiner NOISE input and the noise dither modes
  use a deterministic per-pixel hash in place of the
  hardware's free-running noise counter, so save and
  restore stay bit-identical. RGB dither applies the
  standard Bayer and magic-square matrices as a
  threshold offset before 5-bit quantization, an
  approximation of the hardware dither.
- VI presentation samples with the programmed scales
  but applies no resampling filter: the dedither,
  divot, and gamma-dither stages are stored, not
  applied; gamma uses a square root table (the
  documented curve is approximately gamma 2.0). The
  interlaced odd-field one-line-early interrupt quirk
  is not modeled.
- AI: the documented delayed-carry quirk (a transfer
  ending exactly on an 8 KiB page boundary bumps the
  next transfer's address) is not modeled. A LENGTH
  write while both queue slots are full replaces the
  pending transfer (hardware ignores it). The STATUS
  COUNT field reads a DAC counter approximation. The
  undrained output buffer is not part of the save
  state; drain before saving.
