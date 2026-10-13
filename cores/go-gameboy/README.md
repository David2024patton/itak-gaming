# go-gameboy: a Nintendo Game Boy core written in Go

Clean-room Game Boy (DMG) and Game Boy Color emulator core for iTaK
Gaming, written in Go and exposed through the libretro API so the iTaK
engine can load it exactly like any other core.

Why this exists: the Gambatte core bundled today is fine license-wise,
but David's standing rule for iTaK Gaming is that every system gets its
own from-scratch Go core, 100 percent our own code (MIT, same as the
rest of iTaK Gaming), written from published hardware documentation:
the Sharp SM83 instruction set documentation as collected in the
Game Boy Pan Docs and the published opcode references, the documented
PPU, timer, DMA, and APU behavior, plus the documented cartridge
header and memory bank controller (MBC) behavior.

Conformance is proven the standard way: Blargg's cpu_instrs test ROMs
(all eleven individual tests plus the combined ROM), Blargg's
dmg_sound suite (all twelve singles), the dmg-acid2 PPU rendering
test (pixel-exact against the reference image), and the mooneye-gb
acceptance suite, run headless against the system package in this
module. The test ROMs are public conformance suites, kept locally
under `testroms/` and never committed to the repository.

## Status

- GB-1 (done): module skeleton, the SM83 CPU (`chips/sm83`),
  cartridge header and MBC0/MBC1/MBC3/MBC5 mapping (`cart`), and the
  Blargg cpu_instrs gate.
- GB-2 (done): the real machine. `system/` is a dot-clocked system in
  the go-genesis shape: `RunFrame` produces the 160x144 framebuffer
  plus interleaved int16 audio at 44100 Hz, `SetButtons` feeds the
  joypad, and `Serialize`/`Deserialize` cover the whole machine.
  - `ppu/`: dot-stepped PPU (456 dots per line, 154 lines, a frame
    is exactly 70224 dots), modes 2/3/0/1 with the mode 3 fetch
    penalties, LY/LYC compare with the stored coincidence flag (the
    comparison clock stops while the LCD is off), STAT interrupt
    sources and blocking, background, window (own line counter),
    and sprites with DMG priority, the 10-per-line limit, and
    mode-based VRAM/OAM access blocking. LCD turn-on has the
    warmup delay before the first OAM scan.
  - Timers: the falling-edge TIMA model with the DIV-write and
    TAC-write glitches, the 4-dot overflow reload delay, and the
    reload-cycle TIMA/TMA write quirks.
  - OAM DMA: 160 bytes over 640 dots with the CPU restricted to
    HRAM while the transfer runs, and the queued-restart behavior.
  - Joypad matrix with the press-edge interrupt, serial transfers
    on the internal clock (the test capture hook remains), and
    interrupt dispatch with the mid-push cancellation behavior.
  - `sound/`: the full APU, four channels (square with sweep,
    square, wave with its RAM and the DMG access quirks, noise
    LFSR), the 512 Hz frame sequencer, length/sweep/envelope
    clocks with the trigger quirks, NR52 power behavior, NR51
    panning and NR50 volume mixing.
  - Save states are whole-machine and verified with a real
    homebrew game: serialize mid-play, continue, restore, and
    the continuation matches frame for frame and sample for
    sample. A state is 71801 bytes for a RAM-less cartridge and
    grows with cartridge RAM.
  - The CPU gained M-cycle access slots: each bus access is
    preceded by the dots the hardware spends reaching it, which
    is what makes the timer, DMA, PPU, and ie_push timings land
    on the right dot.
- GB-3: Game Boy Color extensions (double speed, palettes, VRAM and
  WRAM banking, HDMA), the libretro shell, and wiring the core into
  the app in place of Gambatte once it passes the same proofs.

## Conformance notes

Mooneye acceptance: 44 of 75 ROMs report a pass (the rest target
other boot ROMs or models, the GameShark environment, CGB/SGB
behavior, or intra-instruction access order inside instruction
families). The GB-2 gating sets all pass: the timer suite entire,
the OAM DMA suite, the PPU timing and STAT set, and
interrupts/ie_push. One PPU test is excluded from the gate with
its reason recorded in the test: intr_2_mode0_timing_sprites
measures the pixel FIFO's internal fetch phase between sprites at
different X positions, which a line-wise renderer does not
simulate.

## Layout

- `chips/sm83/` the Sharp SM83 CPU (the Game Boy processor), pure Go,
  with exact flag behavior, interrupt dispatch, the EI delay, the HALT
  bug, M-cycle access slots, and full save-state serialization
- `cart/` ROM image parsing: header facts, header checksum, and the
  MBC0, MBC1, MBC3 (with RTC state), and MBC5 memory bank controllers
- `ppu/` the dot-stepped picture processing unit
- `sound/` the audio processing unit and its resampler
- `system/` the assembled machine: bus, timers, DMA, joypad, serial,
  the frame runner, and whole-machine save states, plus the
  conformance harness
- `libretro/` placeholder for the libretro shell (arrives with GB-3)
