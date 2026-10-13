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
  serialize/restore boundary. The APU register space stayed
  inert through this slice and comes alive in NES-3.
- NES-3 (this slice, completing the core): the APU in
  `sound/` (two pulse channels with duty, sweep with the
  per-channel negate quirk, envelopes and length counters;
  the triangle with its linear counter; noise with both
  LFSR modes; the DMC with DMA sample fetches that stall the
  CPU through the bus, the output level unit, loop mode and
  its IRQ; the frame sequencer at APU rate with the 3-or-4
  cycle $4017 reset delay and the triple frame IRQ set at
  the sequence end; the $4015 status read that clears only
  the frame IRQ flag; and the published non-linear pulse/tnd
  mixer formulas rendered at 44100 Hz, mono duplicated to
  stereo), the libretro shell in `libretro/` mirroring
  go-gameboy's (24 retro_* exports, XRGB8888 256x240, 60.0988
  fps, 4/3 aspect), and the app wiring that points the NES
  row at libgo_nes.so. Save states grew a version-2 block
  carrying the full APU state; a state saved by this core
  restores byte-exact, proven on a real MMC3 game through
  the engine bridge (below).

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
TestCPUTimingReport. instr_timing 1/2 waited for NES-3
(they time against the APU length counters); both are hard
gates now, see the NES-3 results below.

## NES-3 conformance results

Hard gates (all passing): Blargg's apu_test singles, all
eight: 1-len_ctr, 2-len_table, 3-irq_flag, 4-jitter,
5-len_timing, 6-irq_flag_timing, 7-dmc_basics, 8-dmc_rates.
The last three are the cycle-grade ones: they pin the frame
sequencer's APU-rate stepping, the $4017 reset delay of 3
or 4 cycles by divider phase, and the frame IRQ flag's
triple set on the last three CPU cycles of the 4-step
sequence. Blargg's instr_timing 1 and 2, deferred from
NES-1 because they time against the APU length counters,
are hard gates now and pass. Unit gates in `sound/` cover
the sweep target overflow mute, the period-below-8 mute,
the envelope decay schedule, length halt and $4015 disable
behavior, DMC playback, loop restart and IRQ, and a
byte-exact APU serialize round trip whose restored copy
produces identical samples. A pulse channel playing A4
(period 253) measures 441.00 Hz, 3.9 cents sharp of 440
(the period's true pitch is 440.29 Hz; the reading is the
zero-crossing count over one second).

Report-only: Blargg's apu_mixer set (square, triangle,
noise, dmc). Each ROM runs its full tone and cancellation
sequence to the shell's done state here, but the verdict is
a human listening for near-silence between the beeps, so
there is no CPU-visible result to gate on; the test logs
the final status and the audio activity for the record.

Real games, through the iTaK engine bridge and the
from-scratch core (enginetest): Super Mario Bros. 3 (USA)
(MMC3) loads, renders non-blank 256x240 video, produces
non-silent audio, and writes a 15,063-byte save state; its
one harness flag (the frame 2 frames after a restore
differs from the save-point frame) is the harness's
static-screen premise, adjudicated the same way as the
Game Boy core's: the state re-saved immediately after the
restore differs from the saved state in 0 of 15,063 bytes,
and a 300-frame continuation from the restored state lands
byte-identical to the original continuation. Super Mario
Bros. (Japan, USA) (NROM) passes the full harness with no
flags: ALL TESTS PASSED, 15,050-byte states, the restored
frame matching the save point exactly.

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
- `sound/` the 2A03 APU: pulse, triangle, noise and DMC
  channels, the frame sequencer, the non-linear mixer, and
  save-state serialization
- `system/` the assembled machine: CPU, PPU, cartridge, RAM,
  controllers, DMA, and whole-machine save states, in the
  go-gameboy core's shape (RunFrame, Framebuffer,
  Serialize/Deserialize, SetButtons)
- `libretro/` the libretro C ABI shell (24 retro_* exports),
  mirroring the go-gameboy core's shell
