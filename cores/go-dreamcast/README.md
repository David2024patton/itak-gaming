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
  ISTNRM, ISTERR (both write-1-to-clear), ISTEXT (device-clear
  only), and the IML2LVL, IML4LVL, IML6LVL mask rows (per bank).
  Reads of ISTNRM carry bit 31 when any external source is
  pending and bit 30 when any error source is pending. The
  highest masked level wins and is presented to the CPU with
  INTEVT 0x320 at level 6, 0x360 at level 4, 0x3A0 at level 2,
  following the published ASIC event table: event ids are the
  ISTNRM bit indices (render done video 0, ISP 1, TSP 2, vblank
  in 3, vblank out 4, hblank 5, YUV done 6, opaque list done 7,
  opaque modifier 8, translucent 9, translucent modifier 10,
  PVR DMA done 19, punchthrough done 21), with the error bank
  starting at bit 0 of ISTERR (ISP out of memory, strip halt,
  parameter out of memory, OPB out of memory, TA input error,
  TA input overflow).
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

### Physical memory map

| Range | Contents |
| :--- | :--- |
| 0x00000000 to 0x001FFFFF | BIOS window, HLE NOP stub (see honesty notes) |
| 0x00200000 to 0x002FFFFF | Flash ROM, 256 KB mirrored across the window |
| 0x00500000 to 0x005FFFFF | TA FIFO (polygon parameter stream) |
| 0x005F6900 to 0x005F693F | Holly interrupt block (real behavior) |
| 0x005F7000 to 0x005F70FF | GD-ROM registers (stub storage) |
| 0x005F8000 to 0x005F9FFF | PowerVR registers, palette RAM, fog table (real behavior) |
| 0x00600000 to 0x00600FFF | YUV FIFO (macroblock stream) |
| 0x00601000 to 0x006FFFFF | Modem and expansion area (stub storage, idle) |
| 0x00700000 to 0x00703FFF | AICA registers (stub storage) |
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

## Save states

`Machine.Serialize` writes, in order:

1. One version byte, currently 0x02.
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

A fresh machine measures:

```text
StateSize = 27,532,261 bytes (version 2, empty TA scene)
```

`Machine.Deserialize` checks the version byte and the PVR
section length, and restores every store and device from the
buffer; a restored machine continues deterministically from the
saved point. A mid-frame state saved after TA submission
restores and renders byte-identical output, which the test
suite proves.

## Roadmap

* DC-2: done (this slice). PowerVR2 renderer, SPG timing, real
  interrupt delivery through the Holly block.
* DC-3: Sound and input. The AICA with its ARM7 sound CPU, G2
  DMA (and the deferred fog unit below), and the Maple bus,
  which finally delivers controller state to games.
* DC-4: BIOS HLE and shipping shape. High-level emulation of the
  BIOS services real games call, a libretro style shell, a
  real-game engine test, and app wiring for the android-64bit
  build of iTaK Gaming.

## Honesty notes

* No Dreamcast BIOS or firmware bytes are included in this core
  or ever downloaded for it. The BIOS window reads back an HLE
  stub: an endless sled of SH-4 NOP instructions (the 16-bit
  value 0x0009), so execution that lands there slides instead of
  running dumped code. Real BIOS calls get high-level emulation
  in DC-4.
* Fog is deferred to DC-3. The fog registers and the fog table
  are stored and serialized, and the TSP fog control field is
  parsed, but fog is not applied to rendered pixels yet.
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
