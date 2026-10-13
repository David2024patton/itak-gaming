# go-nes: a Nintendo Entertainment System core written in Go

Clean-room NES emulator core for iTaK Gaming, written in Go and
exposed through the libretro API so the iTaK engine can load it
exactly like any other core.

Why this exists: the FCEUmm core bundled today is fine license-wise,
but David's standing rule for iTaK Gaming is that every system gets
its own from-scratch Go core, 100 percent our own code (MIT, same as
the rest of iTaK Gaming), written from published hardware
documentation: the MOS 6502 datasheet behavior as the NES's Ricoh
2A03 variant runs it (no decimal arithmetic), the documented
unofficial opcode behavior collected on the NESdev wiki, the
documented iNES and NES 2.0 header formats, and the documented
mapper (MMC1, MMC3, and friends) behavior.

Conformance is proven the standard way: Kevin Horton's nestest
(automated mode at $C000, result bytes at $0002/$0003 plus a
trace comparison against the golden log), and Blargg's test ROM
suites (the $6000 status and text protocol, the older
screen-report shells, and the 2005 RAM-code shell), run headless
against the system package in this module. The test ROMs are
public conformance suites, kept locally under `testroms/` and
never committed to the repository.

## Status

- NES-1: module skeleton, the 6502 CPU (`chips/cpu6502`,
  official plus documented unofficial opcodes, cycle counts from
  bus accesses), cartridge header parsing and mappers 0, 1, 2,
  3, 4, 7, 11, 66 (`cart`). Gates: nestest full pass, all 18
  Blargg instr_test-v5 ROMs pass.
- NES-2 (this slice): the PPU (`ppu/`, dot-stepped 341x262 NTSC
  timing with the odd-frame dot skip, the full register file
  with the loopy scroll machinery, background and sprite
  rendering with sprite 0 hit and the buggy overflow behavior,
  the published 64-color NTSC palette with emphasis, open-bus
  latch decay, and palette RAM with its mirrors) and the real
  machine in `system/`: the CPU steps cycle by cycle at 3 PPU
  dots per cycle with the access interleaved two dots in, NMI
  driven by the PPU's derived output line, OAM DMA at 513/514
  cycles by CPU parity, both controller ports, and the MMC3
  IRQ clocked from the PPU's A12 line through the documented
  glitch filter. Whole-machine save states serialize the CPU,
  cartridge and mapper state, the full PPU (registers, internal
  latches, pipeline and evaluation state, frame parity), RAM,
  the open bus byte, and the controller state; a determinism
  test proves a frame hash is identical across a
  serialize/restore boundary. The APU register space stays
  inert until NES-3.
- NES-3 (planned): the APU (the 2A03's five channels), the
  libretro shell mirroring go-gameboy's, and the app wiring
  that swaps FCEUmm for this core once the proof bar is met.

## NES-2 conformance results

Hard gates (all passing): ppu_open_bus; the 2005 ppu tests
palette_ram, sprite_ram, vram_access, vbl_clear_time (through
the shell's RAM code); sprite_hit_tests 01-08 plus the
timing-grade 09-11 logged passing; sprite_overflow_tests 1 and
2 plus 3-5 logged passing; mmc3_irq_tests 1.Clocking,
3.A12_clocking, 6.MMC3_rev_B; ppu_vbl_nmi singles 01, 04, 05,
07, 08, 09, 10; vbl_nmi_timing 1, 6, 7. The older mmc3_test
set logs 1, 2, 3, 5 passing.

Report-only, with the reason on the test in
`system/conformance_test.go`: ppu_vbl_nmi 02, 03, 06 and the
combined ROM, and vbl_nmi_timing 2, 3, 4, 5 (a one-dot sync
residue tradeoff: the value family and the NMI-timing family
provably want the sync_vbl exit residue one dot apart under
any single $2002 read rule; the shipped rule is the
documented one, a read whose capture races the set dot
reports the flag clear). mmc3_irq_tests 2.Details case 7 and
4.Scanline_timing case 2, and mmc3_test 4-scanline_timing
case 2 (the pre-render line's first background fetch rise,
which follows the vblank-long A12 low, is counted by this
filter model: 242 clocks in the measured frame against the
expected 241, so the IRQ lands one clock early; every other
line and pattern-table arrangement counts exactly).
power_up_palette (palette RAM powers up cleared here rather
than with the measured hardware contents). cpu_timing_test
produces no verdict even given 900 million cycles (about
30,000 frames): it never reads $4015, its NMI handler is
entered and its timing counters advance, but its measurement
sequence never completes here; the precise verdict is on
TestCPUTimingReport. instr_timing 1/2 stay NES-3 as planned
(they time against the APU length counters).

## Layout

- `chips/cpu6502/` the Ricoh 2A03 CPU core (a MOS 6502 with the
  decimal flag wired off), pure Go, with exact flag behavior,
  interrupt polling at the documented poll point (sampled at
  every bus access, latched at the second-to-last), the NMI
  hijack of BRK, cycle counting by bus access, an RDY stall
  hook for DMA, and save-state serialization
- `cart/` iNES and NES 2.0 header parsing plus the memory
  mappers: NROM, MMC1, UxROM, CNROM, MMC3, AxROM, Color Dreams,
  GxROM
- `ppu/` the 2C02 picture processing unit: dot-stepped timing
  and rendering, the register file, and save-state
  serialization
- `system/` the assembled machine: CPU, PPU, cartridge, RAM,
  controllers, DMA, and whole-machine save states, in the
  go-gameboy core's shape (RunFrame, Framebuffer,
  Serialize/Deserialize, SetButtons)
- `libretro/` the libretro C ABI shell (arrives in NES-3)
