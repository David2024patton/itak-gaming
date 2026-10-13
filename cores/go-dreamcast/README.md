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
  state, exposes a blank 640 by 480 XRGB8888 framebuffer, and
  implements save states.

### Physical memory map

| Range | Contents |
| :--- | :--- |
| 0x00000000 to 0x001FFFFF | BIOS window, HLE NOP stub (see honesty notes) |
| 0x00200000 to 0x002FFFFF | Flash ROM, 256 KB mirrored across the window |
| 0x005F7000 to 0x005F70FF | GD-ROM registers (stub storage) |
| 0x005F8000 to 0x005F83FF | PowerVR core registers (stub storage) |
| 0x005F8400 to 0x005F87FF | PowerVR TA registers (stub storage) |
| 0x005F9000 to 0x005FAFFF | PowerVR RAM and ISP stubs (stub storage) |
| 0x00600000 to 0x006FFFFF | Modem and expansion area (stub storage, idle) |
| 0x00700000 to 0x00703FFF | AICA registers (stub storage) |
| 0x00800000 to 0x009FFFFF | AICA RAM, 2 MB |
| 0x04000000 to 0x04FFFFFF | VRAM 32-bit view, 8 MB mirrored |
| 0x05000000 to 0x05FFFFFF | VRAM 64-bit view, 8 MB mirrored |
| 0x0C000000 to 0x0FFFFFFF | Main RAM, 16 MB mirrored |

Register stubs are plain stored words this slice: a read returns
the last 32-bit value written to that exact aligned address
(default 0), and byte or half writes merge into the stored word.
No stub register has behaviour yet.

## Save states

`Machine.Serialize` writes, in order:

1. One version byte, currently 0x01.
2. The CPU state (`sh4.SerializeSize` bytes).
3. Main RAM, then VRAM, then flash, then AICA RAM, each at its
   live length.

The exact size is therefore:

```text
StateSize = 1 + sh4.SerializeSize + len(RAM) + len(VRAM)
          + len(Flash) + len(AICARAM)
          = 27,525,121 + sh4.SerializeSize   (bytes, with stock sizes)
```

`Machine.Deserialize` accepts only a buffer of exactly that
length with a matching version byte, and restores every store
from it; a restored machine continues deterministically from the
saved point. The register stub storage is not serialized: stub
registers have no behaviour this slice, so nothing observable is
lost.

## Roadmap

* DC-2: PowerVR2. The tile accelerator (TA) gets real behaviour,
  plus a tile-based renderer that draws into the framebuffer.
* DC-3: Sound and input. The AICA with its ARM7 sound CPU, G2
  DMA, and the Maple bus, which finally delivers controller state
  to games.
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
* The SH-4 cache is not simulated. The CCR register is stored by
  the CPU package if present, and the P2 cache-through area
  behaves as plain memory, exactly like every other area.
* The instruction TLB is modeled as shared UTLB lookups; there is
  no separate ITLB array.
* The VRAM 64-bit path is simplified to the same backing store as
  the 32-bit path, with normal width accesses; the hardware
  interleaving of the 64-bit view is not reproduced.
* The cycle model is flat: one cycle per instruction. The real
  SH-4 is a dual-issue superscalar part, so timings, and any
  software that measures time by counting instructions, will
  drift from hardware.
* Flash writes are plain stores. Real flash command cycles (the
  unlock and program sequences) are not emulated.
* Test data is synthetic only: hand assembled instruction words
  and generated disc images, kept in `testroms/` and never
  committed.
