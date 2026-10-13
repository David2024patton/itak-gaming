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
trace comparison against the golden log), and Blargg's
instr_test-v5 instruction ROMs (the $6000 status and text
protocol), run headless against the system package in this module.
The test ROMs are public conformance suites, kept locally under
`testroms/` and never committed to the repository.

## Status

- NES-1 (this slice): module skeleton, the 6502 CPU
  (`chips/cpu6502`, official plus documented unofficial opcodes,
  cycle counts from bus accesses), cartridge header parsing and
  mappers 0, 1, 2, 3, 4, 7, 11, 66 (`cart`), and a CPU test rig
  (`system`) that runs the conformance suites. The rig stubs the
  PPU status register with a frame-cadence vblank flag so ROMs
  that poll $2002 can run; anything needing real PPU behavior is
  NES-2 work and is logged as such.
- NES-2 (planned): the PPU (dot-stepped, in the go-gameboy core's
  shape) with the full system timing model, controller ports, OAM
  DMA, and the MMC3 IRQ clocked from PPU address line A12.
- NES-3 (planned): the APU (the 2A03's five channels), the
  libretro shell mirroring go-gameboy's, and the app wiring that
  swaps FCEUmm for this core once the proof bar is met.

## Layout

- `chips/cpu6502/` the Ricoh 2A03 CPU core (a MOS 6502 with the
  decimal flag wired off), pure Go, with exact flag behavior,
  interrupt polling and the NMI hijack of BRK, cycle counting by
  bus access, an RDY stall hook for DMA, and save-state
  serialization
- `cart/` iNES and NES 2.0 header parsing plus the memory
  mappers: NROM, MMC1, UxROM, CNROM, MMC3, AxROM, Color Dreams,
  GxROM
- `system/` the assembled machine. In NES-1 this is a CPU test
  rig (2 KB RAM with mirrors, stub PPU/APU register space, simple
  controller shift registers); NES-2 grows it into the real
  machine in the go-genesis shape
- `libretro/` the libretro C ABI shell (arrives in NES-3)
