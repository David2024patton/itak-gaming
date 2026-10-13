# go-dreamcast

A from-scratch Sega Dreamcast core in pure Go, part of iTaK Gaming.
MIT licensed. Users supply their own game images; this repository
contains no game data, no Dreamcast BIOS or firmware bytes, and no
dumped code of any kind.

## DC-1: what is in this slice

* `chips/sh4`: the SH-4 CPU. Integer pipeline, the FPU (single and
  double precision, FPU register banks, FPSCR modes), and a
  functional MMU: UTLB translation with page sizes, protection
  checks and the P1, P2, P3, P4 and store queue areas. With the
  MMU off, addresses are masked to their 29-bit physical form.
* `bus`: the physical memory map (below). Multi-byte accesses are
  composed little-endian from bytes, so unaligned accesses work at
  the bus level.
* `gd`: GD-ROM image reading for `.gdi`, `.iso` and `.bin` files,
  with ISO9660 directory walking and IP.BIN parsing (the bootstrap
  header every Dreamcast disc carries: title, product number,
  region, peripherals and the boot file name).
* `system`: the machine skeleton. It wires CPU, bus and disc
  together, runs frames on a flat cycle budget, holds controller
  state, and implements save states.

## DC-2: what is in this slice

Real interrupt delivery, the SPG display timing generator, and
the PowerVR2 tile renderer.

* `chips/sh4` interrupt model: `RaiseInterrupt` now latches a
  pending request (highest level wins) instead of vectoring
  immediately. Acceptance at the instruction boundary honors
  SR.BL and SR.IMASK (the level must exceed the mask), wakes a
  halted CPU, sets INTEVT at acceptance time, and can be
  withdrawn before acceptance. Exceptions keep their immediate
  behavior.
* `holly`: the Holly system interrupt block at 0x005F6900.
  SB_ISTNRM (write-1-to-clear), SB_ISTEXT (device-clear only),
  SB_ISTERR (write-1-to-clear), and the IML2LVL, IML4LVL,
  IML6LVL mask rows (per bank). Reads of ISTNRM carry bit 30
  when any external source is pending and bit 31 when any
  error source is pending. The highest masked level wins and
  is presented to the CPU with INTEVT 0x320 at level 6, 0x360
  at level 4, 0x3A0 at level 2, following the published ASIC
  event table: event ids are the ISTNRM bit indices (render
  done video 0, ISP 1, TSP 2, vblank in 3, vblank out 4,
  hblank 5, YUV done 6, opaque list done 7, opaque modifier
  8, translucent 9, translucent modifier 10, PVR DMA done 19,
  punchthrough done 21), with the error bank starting at bit
  0 of ISTERR (ISP out of memory, strip halt, parameter out
  of memory, OPB out of memory, TA input error, TA input
  overflow). DC-3 corrected this block's register order and
  summary bits against the official documentation; see the
  DC-3 section.
* `pvr`: the PowerVR2 (CLX2). Register file (ID, REVISION,
  SOFTRESET, STARTRENDER, PARAM_BASE, REGION_BASE, VO_BORDER_COL,
  FB_R_CTRL, FB_W_CTRL, FB_W_LINESTRIDE, FB_R_SOF1/2, FB_R_SIZE,
  FB_W_SOF1/2, FB_X_CLIP, FB_Y_CLIP, ISP_BACKGND_T/S/D,
  ISP_FEED_CFG, FPU_SHAD_SCALE, SDRAM_CFG, FPU_CULL_MODE, fog
  registers, the SPG registers, TEXT_CONTROL, VO_CONTROL,
  VO_STARTX/Y, SCALER_CTL, PAL_RAM_CTRL, FPU_PARAM_CFG,
  HALF_OFFSET, FPU_PERP_VAL, SPG_STATUS, TA_ITP_CURRENT,
  TA_OL_BASE, TA_ISP_BASE, TA_OL_LIMIT, TA_ISP_LIMIT,
  TA_GLOB_TILE_CLIP, TA_ALLOC_CTRL, TA_LIST_INIT, TA_LIST_CONT,
  TA_YUV_TEX_BASE/CTRL/CNT, TA_NEXT_OPB, TA_NEXT_OPB_INIT),
  palette RAM (4 KB, PAL_RAM_CTRL formats 1555, 565, 4444, 8888),
  the fog table (stored; see the fog note below), the TA
  parameter parser, and the tile renderer.
* SPG: the beam advances by exact fractions (pixel clock 27 MHz,
  or 13.5 MHz when FB_R_CTRL selects the divided clock, against
  the 200 MHz cycle stream; no floating point drift). Vblank in
  and out fire on the lines SPG_VBLANK_INT names, hblank fires
  per SPG_HBLANK_INT, and SPG_STATUS reports scanline, hblank
  window, vblank, and field live. Power-on defaults are the
  documented NTSC totals (858 by 263).
* TA: parameter streams arrive through the TA FIFO window at
  0x00500000 (and the YUV FIFO at 0x00600000). The parser handles
  end of list, user tile clip, and control parameters, polygon
  and modifier volume headers, sprite headers, and vertex
  parameters (type 1 packed, with and without UV16, plus
  two-volume and sprite forms), building the five display lists
  (opaque, opaque modifier, translucent, translucent modifier,
  punchthrough). Completed primitives are also written into
  VRAM as ISP parameter blocks at TA_ISP_BASE with per-tile OPB
  chain entries at TA_OL_BASE under the TA_ALLOC_CTRL budgets;
  overflows raise the documented Holly error bits. The TA YUV
  converter turns 384-byte macroblocks (BT.601) into twiddled
  RGB565 texture in VRAM and raises the YUV done event.
* Renderer (on STARTRENDER): region array walk with per-tile
  list disables, background plane first (color from
  ISP_BACKGND_S, depth from ISP_BACKGND_D), then the lists in
  hardware order (opaque, opaque modifier, punchthrough,
  translucent modifier, translucent), all eight ISP depth modes,
  FPU culling modes, perspective-correct interpolation of
  inverse depth, UV, and color, modifier volumes as a per-pixel
  counter (inside parity selects the second parameter set of
  two-volume primitives and the shading of translucent modifier
  lists), texturing in 1555, 565, 4444, 8888, YUV422, paletted 4
  and 8 bit, and VQ compressed forms, twiddled, linear, and
  stride layouts, mipmaps, point and bilinear filtering, the
  replace, modulate, decal, and shadow texture instructions,
  punchthrough alpha test against ALPHA_REF, and full src/dst
  blend factor blending for the translucent lists. Output goes
  into VRAM at FB_W_SOF1 in the FB_W_CTRL format (0555, 0565,
  0888, 8888) with the hardware Bayer 4x4 ordered dither when
  enabled, FB_X_CLIP/FB_Y_CLIP scissoring, and
  FB_W_LINESTRIDE strides. Render done raises the ISP then TSP
  Holly events. `Machine.Framebuffer` presents the read
  framebuffer at FB_R_SOF1 converted to XRGB8888 per FB_R_CTRL
  and FB_R_SIZE.

## DC-3: what is in this slice

Sound (the AICA with its own ARM7TDMI), the Maple controller
bus, G2 DMA, and the fog unit deferred from DC-2.

* Correction to DC-2: the Holly register order and the ISTNRM
  summary bits shipped in DC-2 contradicted the official
  documentation. The documented map is SB_ISTNRM at
  0x005F6900, SB_ISTEXT at +0x04, SB_ISTERR at +0x08 (masks
  IML2NRM/EXT/ERR at 0x6910/14/18, IML4 at 0x6920/24/28, IML6
  at 0x6930/34/38), and in ISTNRM bit 31 is the error summary
  and bit 30 the external summary. DC-2 had ISTERR and ISTEXT
  swapped and the summary bits exchanged; both are fixed,
  with the bank indices and the serialized layout unchanged.
  One known deviation remains: the event id for PVR DMA done
  is still the DC-2 value 19, while the official table puts
  PVR DMA end at bit 11 and channel 2 DMA end at bit 19;
  nothing raises the event yet, so the discrepancy is inert.
* `chips/arm7`: the AICA's ARM7TDMI, this module's own
  implementation (ARM and Thumb states, mode banking,
  IRQ/FIQ/undefined/SWI/abort exceptions, the GBATEK cycle
  model). Gated by the Tom Harte ARM7TDMI corpus, copied
  local-only into `testroms/` like the other cores (never
  committed): the default harness subset passes 90,000 of
  90,000 (2,000 per file across all 45 files), and the full
  corpus at `DC_TH_SUBSET=50000` passes 2,250,000 of
  2,250,000. Coprocessor instructions raise the undefined
  exception, matching the corpus states; data aborts cannot
  occur because the bus interface cannot fault. One corpus
  quirk is pinned in the Thumb unit: a POP with an empty
  register list transfers only the program counter, and the
  loaded value keeps its bit 0 visible in R15, while a
  listed POP into the program counter masks it; the
  reference states distinguish the two paths.
* `aica`: the sound block. 64 channels with the documented
  register file: start address with format select (16-bit
  PCM, 8-bit PCM, 4-bit Yamaha ADPCM with the published step
  table and per-channel predictor and step state), loop
  start/end, key on/off through the common KYONEX write,
  OCT/FNS pitch at the 44,100 Hz base, the AICA envelope
  generator (attack, decay 1, decay 2, release with the
  documented rate tables and sustain level), pitch and
  amplitude LFO, DISDL/DIPAN direct sends, ISEL/IMXL effect
  sends, Q with a one-pole lowpass filter, and the position
  and envelope monitor readback. Samples step at exactly
  512 master cycles (22.5792 MHz master, 44,100 Hz). The
  ARM7 runs at half the master clock, 11.2896 MHz, the rate
  the DC documentation gives for the sound CPU. The DSP
  effect unit executes the documented 128-step microprogram
  (COEF, MADRS, MPRO loaded through registers; TEMP, MEMS,
  MIXS, EFREG data paths with the documented multiply, add,
  shift, pack and unpack operations), gated on a hand built
  gain plus feedback delay program against hand computed
  outputs. Common registers cover MVOL, the three timers
  (sample-clocked interrupt sources), the internal register
  DMA, the RTC, the MIDI stub port, the ARM/SH-4 shared
  registers, the interrupt controller both ways (level
  presentation with end of interrupt to the ARM as FIQ; the
  SH-4 path raises the Holly external AICA bit), and the
  ARMRST control that holds and releases the ARM7.
* `maple`: the controller bus. SB_MDSTAR points at a command
  table in main RAM; SB_MDST (with SB_MDEN) runs it
  synchronously, or SB_MDTSEL defers it to vblank out. Frames
  follow the documented format (command, recipient, sender,
  word count, payload). Two devices per port: the standard
  controller (Device Info with the documented function code
  and function data, Get Condition fed by `SetButtons`:
  buttons in the documented wire bit layout, active low,
  triggers and stick axes) and a VMU-like storage device on
  the sub address (MemoryCard function, 128 KB image, 512
  byte blocks, Get Block and Write Block). Unknown commands
  answer 0xFE, known commands with an unsupported function
  answer 0xFC with the function word, bad block requests
  answer 0xFB, and an empty sub address receives the single
  word 0xFFFFFFFF. Completion raises the Holly Maple DMA
  done event (ISTNRM bit 12, verified against the published
  table).
* `g2dma`: the G2 DMA controller at 0x005F7800. The AICA
  channel copies synchronously in both directions between
  system memory and AICA space (the documented length is the
  byte count), raising ISTNRM bit 15 and the AICA-side DMA
  end interrupt. The Ext1, Ext2, and Dev channels model
  absent devices: started transfers set their busy bit and
  never complete, which is the documented behavior of a DMA
  to a device that is not there.
* Fog (closes the DC-2 deferral): all four documented TSP
  fog modes are applied in the shading stage after the
  source color clamp: table fog through the 128 entry table
  at window +0x200 with FOG_COL_RAM and the FOG_DENSITY
  mantissa and exponent format, per-vertex fog with
  FOG_COL_VERT keyed on the interpolated offset alpha (and
  the Offset bit), no fog, and Look Up Table Mode 2. The
  gate mixes a known depth against a hand filled table to
  the exact presented pixel.
* `system`: RunFrame steps the AICA in scanline chunks by
  the exact rational ratio (SH-4 cycles times 225792 over
  2000000), `AudioBuffer()` drains stereo at 44,100 Hz, and
  `SetButtons` now feeds the Maple controller on port 0.
  The whole-machine gate runs one program through all of
  it: the SH-4 copies and releases an ARM sound driver, the
  driver keys a looping PCM channel and takes timer FIQs,
  a Maple condition poll lands and vectors its handler, the
  fog scene renders, and a mid-run save restores to a byte
  identical continuation including audio.

### DC-3 simplifications and boundaries

* The DSP coefficient for the filter Q is a stated model:
  the documentation gives the Q table in dB (0.75 dB per
  step, minus 3 dB) but no coefficient formula for the
  one-pole section, so the resonant gain follows that table
  directly and Q = 4 with FLV at maximum is exactly
  transparent, the documented pass-through setting.
* Noise generation (SSCTL) is a per-channel xorshift LFSR;
  the hardware noise algorithm is not in the published
  documentation used here. The position monitor returns the
  low 16 bits of the sample position. MIDI is a stub (the
  out interrupt fires on a buffer write, input never
  arrives). The DSP external inputs EXTS are silent (no
  external source exists in this machine), and DSP TABLE
  addressing is absolute into wave RAM rather than ring
  relative. The RTC write model uses a simplified enable
  latch. The ARM/SH-4 wave RAM write-protect bit (RP) is
  stored, not enforced.
* Maple models the controller and the storage sub-device
  only; other documented devices (keyboard, mouse, gun,
  microphone) are not implemented, and their addresses
  answer as empty. All Status returns the Device Info
  payload plus the condition words, a documented layout
  choice. SB_MDAPRO is stored without checking the
  documented security code. Function codes are carried in
  the RAM representation used by the public headers
  (controller 0x01000000, storage 0x02000000); the Dev.Box
  worked example prints the controller code byte-reversed,
  which this core treats as a documentation artifact.
* G2 interrupt-triggered DMA (TSEL other than 0) is not
  modeled; the trigger mask registers sit outside the
  Holly window. The G2 read counters report start plus
  length and a zero remaining length after the synchronous
  AICA copy. SB_G2ID reads 0.
* Interrupts from the AICA to the SH-4 are level presented
  from the shared pending word; because the machine steps
  the two CPUs in chunks, an AICA source the ARM
  acknowledges inside the same chunk (its own timer FIQ)
  never becomes visible to the SH-4. The DC-3 gate
  therefore counts timer B on the SH-4 side, which the ARM
  never acknowledges. On hardware the two CPUs run
  concurrently and both would see the line.



| Range | Contents |
| :--- | :--- |
| 0x00000000 to 0x001FFFFF | BIOS window, HLE NOP sled plus the handler stub page at 0x800 (see the DC-4 section) |
| 0x00200000 to 0x002FFFFF | Flash ROM, 256 KB mirrored across the window |
| 0x00500000 to 0x005FFFFF | TA FIFO (polygon parameter stream) |
| 0x005F6900 to 0x005F693F | Holly interrupt block (real behavior) |
| 0x005F6C00 to 0x005F6CFF | Maple controller bus (real behavior) |
| 0x005F7000 to 0x005F70FF | GD-ROM registers (stub storage) |
| 0x005F7800 to 0x005F78FF | G2 DMA controller (real behavior) |
| 0x005F8000 to 0x005F9FFF | PowerVR registers, palette RAM, fog table (real behavior) |
| 0x00600000 to 0x00600FFF | YUV FIFO (macroblock stream) |
| 0x00601000 to 0x006FFFFF | Modem and expansion area (stub storage, idle) |
| 0x00700000 to 0x00707FFF | AICA registers (real behavior) |
| 0x00710000 to 0x0071000B | AICA RTC (real behavior) |
| 0x00800000 to 0x009FFFFF | AICA RAM, 2 MB |
| 0x04000000 to 0x04FFFFFF | VRAM 32-bit view, 8 MB mirrored |
| 0x05000000 to 0x05FFFFFF | VRAM 64-bit view, 8 MB mirrored, bank interleaved (below) |
| 0x0C000000 to 0x0FFFFFFF | Main RAM, 16 MB mirrored |

Register stubs are plain stored words: a read returns the last
32-bit value written to that exact aligned address (default 0),
and byte or half writes merge into the stored word.

The VRAM 64-bit window interleaves the two 4 MB VRAM banks the
way the hardware does: an 8-byte unit in the 64-bit window takes
its low word from bank 0 and its high word from bank 1 at the
same row offset (backing + 0x400000). Twiddled textures uploaded
through this window therefore land exactly where the renderer's
twiddle addressing expects them, which the test suite proves
end to end with a textured quad submitted over the bus.

Interlace presentation: when SPG_CONTROL selects interlace, the
presented frame composes both fields: even rows come from
FB_R_SOF1 and odd rows from FB_R_SOF2, each field holding half
the lines at the same stride, while the SPG field bit flips per
field. Progressive output reads FB_R_SOF1 only.

## DC-4: what is in this slice

The BIOS HLE boot, the libretro shell, the enginetest gate, and
the app wiring preparation. With this slice the core is
complete: a disc image boots end to end through our own BIOS
high-level emulation, plays through the shell, and passes the
shared engine test.

### The boot model

`Machine.BootDisc` performs the machine side of the documented
Dreamcast boot sequence:

* The disc's IP.BIN (the first 16 sectors of the data area,
  32 KB) is read through the `gd` layer and validated with the
  DC-1 parser (`gd.ParseIPBin`); an absent or malformed IP.BIN
  fails the boot with a clean error before the CPU is touched.
* The IP image is copied to its documented load address
  0x8C008000. The BIOS variables page (0x8C000000 to
  0x8C001000) is cleared, then the documented syscall vector
  table is written: sysinfo at 0x8C0000B0, ROM font at
  0x8C0000B4, flashrom at 0x8C0000B8, GD-ROM at 0x8C0000BC and
  0x8C0000C0, system boot at 0x8C0000E0, and the GD-ROM direct
  entry pointer at 0x8C0010F0. The SYSINFO id block at
  0x8C000068 stays zeroed: the real BIOS copies it from the
  flash sysinfo partition, which this core does not model.
* The CPU enters the IP at 0x8C008300 with SP 0x8C00F000 (the
  boot stack region the public boot documentation places just
  below the 0x8C00F000 vector and scratch block) and PR 0. SR
  keeps its power-on value; a real IP sets its own SR, VBR,
  and stack as its first act, and the fixture IP does exactly
  that.

No BIOS bytes are embedded or executed. The vector slots point
at handler stubs in the BIOS window: one page at physical
0x800, written by us, holding eight stubs of eight bytes each
(RTS, NOP, NOP, NOP). The machine intercepts execution when
the program counter reaches a stub, services the call in Go,
and returns through PR; the stub's own RTS is a safety net for
execution arriving by any other path.

### The syscall subset

The implemented subset is what a boot IP needs, at the
documented vectors, with the documented GD request model
(REQ_CMD returns a request id or 0 while busy, EXEC_SERVER
executes, GET_CMD_STAT reports busy, complete, or error with
the result words):

* GD-ROM (vector 0x8C0000BC and 0x8C0000C0, R6 = 0): REQ_CMD,
  GET_CMD_STAT, EXEC_SERVER, INIT_SYSTEM, GET_DRV_STAT, and
  RESET. Commands: PIOREAD and DMAREAD (FAD addressed sector
  reads through the `gd` layer; result word 0 is bytes
  transferred), GETTOC2 (the session and track table),
  INIT, GET_VERSION (this HLE's own banner, never a Sega
  string), GETSCD (a zeroed subcode block), and PLAY, PLAY2,
  PAUSE, RELEASE completing at once (no CD audio model).
* Misc (same vector, R6 = -1): INIT.
* SYSINFO (0x8C0000B0): init, and the id block read (zeroed,
  see above).
* System boot (0x8C0000E0): function 0, the exit to the BIOS
  menu. There is no menu in this HLE, so the CPU halts and
  the machine idles on its last frame.

Everything else (the font and flashrom vectors in full, the
remaining GD functions and commands, further sysinfo and
system functions) is a logged stub: it returns the
documented error shape (0xFFFFFFFF, or the GD error status)
and is recorded in `Machine.UnimplementedBIOSCalls`, with a
summary (`Machine.BIOSDiagnostics`) the shell prints through
the frontend log at unload, the PS1 core's pattern. Handlers
preserve every register except R0. The requests execute
synchronously at EXEC_SERVER, so a poll loop sees busy only
when no request was queued. One read is bounded to 8192
sectors.

Two boundaries of the boot model, stated plainly: the boot
file is served as stored, so the retail descramble step is
not implemented and unscrambled (homebrew layout) images are
the supported form; and retail discs' own IP.BIN programs are
Sega code this repository does not contain, so a retail image
boots only as far as its IP runs on this syscall subset (see
the honesty notes). No retail game has been booted on this
core yet.

### The enginetest gate

`system/dc4_test.go` builds a synthetic disc (.gdi and .iso
forms) whose IP.BIN is a hand-assembled SH-4 program (a small
two-pass assembler in the test file; every encoding is the
documented one) that does the real boot work through the BIOS
calls: GD init, GETTOC2, a PVD read, an ISO9660 root walk
that finds the boot file named in its own header, and sector
by sector PIOREAD loads of the payload to 0x8C010000. The
payload keys the DC-3 ARM7 sound driver tone, polls Maple
port 0 through a DMA table, renders the DC-2 textured quad
scene, arms the vblank interrupt, and idles on SLEEP while
its handler counts vblanks. Milestones land in order at
0x8C300000, all inside the first frame.

Gate results (2026-10-13): all five milestones observed in
frame 0, in order; framebuffer probes exact (0xF800, 0x07E0,
0x8400, 0x001F, 0xFFFF raw, and the presented red texel
0xFFFF0000 and bilinear midpoint 0xFF848200); the recorded
Maple condition words match the host pad state exactly
(0x07002003, 0x01000000, 0xFFF300C8, 0x8080C81E for A and
Start pressed, trigger 200, stick (30, 200)); the tone's
period is found verbatim in the drained audio and measures
exactly 64.00 samples (689 Hz at 44100 Hz) over 46 rising
edges; the boot used zero unimplemented BIOS calls; a
mid-run save restores byte-identical continuation (state,
framebuffer, and audio). The same disc through the built
desktop core (`builds/libgo_dc_desktop.so`) in the shared
harness (`go_backend/cmd/enginetest`): ALL TESTS PASSED
(640x480 non-blank video, non-silent audio, save state
28,162,901 bytes with the scene loaded, restore frame hash
equal). Debugging the gate caught three real fixture bugs
worth naming: an LDS encoding mixup (PR vs MACH), and the
two SH-4 delay slot traps (a conditional branch's delay slot
runs only when taken; a call's delay slot reads pre-call
registers).

### The shell

`libretro` follows the family pattern: core name
"iTaK GoDC", extensions `gdi|iso|bin` (the `gd` layer has no
.cue parser, so cue is not advertised), `need_fullpath` set
(a .gdi resolves its track files relative to its own
directory), geometry fixed at the PVR Present output
640x480, XRGB8888. The declared fps is the quotient of the
machine's own documented constants, 200,000,000 Hz over the
3,333,333 cycle frame budget = 60.000006, not the 59.94
NTSC figure: the core's flat model runs at 60, and the shell
says so. Audio is the AICA's native 44,100 Hz stereo. The
pad maps the libretro standard layout to the DC pad (A, B,
X, Y, Start, dpad), the analog triggers from the libretro
analog button axes (L2 and R2) with a digital full-press
fallback from L and R, and the main stick from the left
analog axes scaled to 0..255 with 128 centered; through the
iTaK Go bridge (digital joypad only) the stick reads
centered and the triggers read through L and R.
RETRO_MEMORY_SYSTEM_RAM is the 16 MB main RAM;
RETRO_MEMORY_SAVE_RAM is port 0's live 128 KB VMU image.
Serialize goes through the system save states; reset
reboots the loaded disc.

### Fixes and wiring state

* The Holly PVR DMA done event constant is corrected to the
  documented table value 11 (DC-2 and DC-3 carried 19, the
  channel-2 DMA end value). A module-wide grep before the
  fix found the declaration as the constant's only use;
  nothing raised or tested it, so the fix is free. There is
  still no PVR DMA to raise it.
* App wiring preparation is done locally and NOT pushed:
  `go_backend/internal/core/types.go` (the Cores64BitOnly
  Dreamcast entry is now "iTaK GoDC" / libgo_dc.so with
  extensions gdi, iso, bin) and `flutter_ui/lib/main.dart`
  (the 'dc' entry uses libgo_dc.so with the same extensions;
  `requires64: true` unchanged). `tools/autobuild-server.sh`
  is untouched: it has no 64-bit branch, which is the
  parent's pipeline task.

## Save states

`Machine.Serialize` writes, in order:

1. One version byte, currently 0x04.
2. The CPU state (`sh4.SerializeSize` bytes, which now includes
   the latched pending interrupt).
3. Main RAM, then VRAM, then flash, then AICA RAM, each at its
   live length.
4. The Holly block (48 bytes: three status words, nine mask
   words).
5. The PVR state: registers, palette RAM, fog table, the SPG
   beam position (exact fractional accumulator included), and
   the TA scene: parser state, YUV converter progress, and the
   display lists not held in VRAM. The section size varies with
   the scene, and `StateSize` reports the current exact total.
6. The AICA state: the raw register file, the DSP memories,
   every channel's runtime state, the interrupt controller,
   the output ring, and the ARM7 CPU state.
7. The Maple state: registers, the four pad states, and the
   four 128 KB VMU images.
8. The G2 DMA registers and channel state, then a 16 byte
   machine tail: the AICA clock accumulator remainder and
   the Maple vblank trigger edge flag.
9. The BIOS HLE block (192 bytes, DC-4): the booted flag,
   the GD request queue, and the unimplemented-call
   diagnostics.

A fresh machine measures:

```text
StateSize = 28,162,665 bytes (version 4, empty TA scene)
```

`Machine.Deserialize` checks the version byte and the PVR
section length, and restores every store and device from the
buffer; a restored machine continues deterministically from
the saved point. A mid-frame state saved after TA submission
restores and renders byte-identical output, and the DC-3
gate proves the same for a mid-run state with sound playing
(audio included), which the test suite proves.

## Roadmap

* DC-2: done. PowerVR2 renderer, SPG timing, real interrupt
  delivery through the Holly block.
* DC-3: done (this slice). The AICA with its ARM7 sound CPU,
  the DSP effect unit, G2 DMA, the Maple bus delivering
  controller state to games, and the fog unit deferred from
  DC-2.
* DC-4: done (this slice). The BIOS HLE boot and syscall
  subset, the libretro shell, the enginetest gate in process
  and through the shared harness, and the android-64bit app
  wiring preparation. The core is complete; what remains is
  the honest gaps list below, which future slices can close
  as real software demands them.

## Honesty notes

* No Dreamcast BIOS or firmware bytes are included in this core
  or ever downloaded for it. The BIOS window reads back an HLE
  stub: an endless sled of SH-4 NOP instructions (the 16-bit
  value 0x0009) plus the handler stub page at 0x800, so
  execution that lands outside a stub slides instead of
  running dumped code. Real BIOS calls get the high-level
  emulation described in the DC-4 section.
* BIOS syscall coverage stops at the boot subset (see the
  DC-4 section). Calls outside it are logged stubs returning
  a documented error, never silent successes: the ROM font
  and flashrom interfaces are unimplemented, the GD command
  set has no session or mode commands (REQ_SES, REQ_MODE,
  SET_MODE) and no sector-mode switching (the sector size
  and data type are fixed at the image's own layout), CD
  audio commands complete without playing anything, and the
  boot file descramble step is not implemented, so
  unscrambled images are the supported form. No retail game
  has been booted on this core yet; real games will exercise
  syscall paths and hardware corners the fixtures do not,
  and the unimplemented-call diagnostics exist so those
  first failures name what is missing.
* Fog arrived in DC-3 (see the DC-3 section and its
  boundaries): all four TSP fog modes are implemented. The
  fog table entry contributes its low 8 bits as the fog
  alpha, a stated choice; the FOG_CLAMP_MIN/MAX registers
  are stored, but the per-polygon color clamp they feed is
  not implemented (the renderer's source clamp still runs
  before fog, as documented), Look Up Table Mode 2 collapses
  to an identity in this single color buffer renderer (the
  documented substitution makes the fog color the polygon
  color itself), and the background plane is not fogged,
  matching its treatment by the other ISP shading features
  in this renderer.
* Deferred TA formats and simplifications: floating point and
  intensity vertex color formats are read as packed colors; the
  background plane is color only (its texture bit is ignored);
  VQ textures are not mipmapped; trilinear filtering samples a
  single mip level with bilinear filtering at a computed LOD
  (integer MipMapD bias); modifier volumes never write depth;
  OPB chains in VRAM are a simplified per-tile chain while the
  renderer consumes the in-core display lists; SCALER_CTL is
  honored only as a half-width sampling bit in presentation;
  SPG_VBLANK and SPG_WIDTH are stored but do not shape the beam
  (the beam and status follow SPG_VBLANK_INT as documented).
* SPG timing runs off the flat instruction cycle stream, so
  beam positions are exact against the cycle model below, not
  against wall clock time.
* Exceptions vector immediately even when SR.BL is set. The
  manual warns a general exception under BL can deadlock the
  hardware; boot code never raises one there, so interrupts
  (which do honor BL and IMASK) carry the correctness gates.
* The SH-4 cache is not simulated. The CCR register is stored by
  the CPU package if present, and the P2 cache-through area
  behaves as plain memory, exactly like every other area.
* The instruction TLB is modeled as shared UTLB lookups; there is
  no separate ITLB array.
* The cycle model is flat: one cycle per instruction. The real
  SH-4 is a dual-issue superscalar part, so timings, and any
  software that measures time by counting instructions, will
  drift from hardware.
* Flash writes are plain stores. Real flash command cycles (the
  unlock and program sequences) are not emulated.
* Test data is synthetic only: hand assembled instruction words
  and generated disc images, kept in `testroms/` and never
  committed.
