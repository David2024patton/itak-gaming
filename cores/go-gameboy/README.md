# go-gameboy: a Nintendo Game Boy core written in Go

Clean-room Game Boy (DMG) and Game Boy Color emulator core for iTaK
Gaming, written in Go and exposed through the libretro API so the iTaK
engine can load it exactly like any other core.

Why this exists: the Gambatte core bundled today is fine license-wise,
but David's standing rule for iTaK Gaming is that every system gets its
own from-scratch Go core, 100 percent our own code (MIT, same as the
rest of iTaK Gaming), written from published hardware documentation:
the Sharp SM83 instruction set documentation as collected in the
Game Boy Pan Docs and the published opcode references, plus the
documented cartridge header and memory bank controller (MBC) behavior.

Conformance is proven the standard way: Blargg's cpu_instrs test ROMs
(all eleven individual tests plus the combined ROM) and the mooneye-gb
acceptance suite, run headless against the system test rig in this
module. The test ROMs are public conformance suites, kept locally
under `testroms/` and never committed to the repository.

## Roadmap

- GB-1 (this slice): module skeleton, the SM83 CPU (`chips/sm83`),
  cartridge header and MBC0/MBC1/MBC3/MBC5 mapping (`cart`), and a
  minimal CPU test rig (`system`) that passes Blargg cpu_instrs.
- GB-2: the PPU (background, window, sprites, STAT/LY timing), the APU,
  and the real timing-accurate system that replaces the test rig.
- GB-3: Game Boy Color extensions (double speed, palettes, VRAM and
  WRAM banking, HDMA), the libretro shell, and wiring the core into
  the app in place of Gambatte once it passes the same proofs.

## Layout

- `chips/sm83/` the Sharp SM83 CPU (the Game Boy processor), pure Go,
  with exact flag behavior, interrupt dispatch, the EI delay, the HALT
  bug, and full save-state serialization
- `cart/` ROM image parsing: header facts, header checksum, and the
  MBC0, MBC1, MBC3 (with RTC state), and MBC5 memory bank controllers
- `system/` GB-1 test rig: a flat bus with cartridge, work RAM, a
  simple timer and divider, and a serial output capture used by the
  conformance harness
- `libretro/` placeholder for the libretro shell (arrives with GB-2/GB-3)
