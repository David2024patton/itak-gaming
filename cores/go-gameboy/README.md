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
  - OAM DMA: 160 bytes, one per M-cycle after a 2 M-cycle
    startup, with the CPU restricted to HRAM while the transfer
    runs, and the queued-restart behavior. (GB-3 made the pacing
    M-cycle based so double speed halves the dot count; see the
    GB-3 notes for the bug that proved it matters.)
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
    sample. (GB-3 changed the framebuffer to XRGB8888 and grew
    the layout; current sizes are in the GB-3 notes.)
  - The CPU gained M-cycle access slots: each bus access is
    preceded by the dots the hardware spends reaching it, which
    is what makes the timer, DMA, PPU, and ie_push timings land
    on the right dot.
- GB-3 (done): Game Boy Color support, the libretro shell, and the
  app wiring that swaps Gambatte for this core (staged, ships with
  the next build).
  - CGB mode is chosen by the cartridge header (flag 0x80 or 0xC0);
    plain DMG cartridges keep the GB-2 machine. The CGB adds:
    BG and OBJ palette RAM (64 bytes each) behind BCPS/OCPS with
    auto-increment and the mode 3 access block, VRAM bank 1 behind
    VBK carrying tile data and the attribute map (palette, bank,
    flips, priority), WRAM banks 2-7 behind SVBK in the D000
    window, double-speed mode (KEY1 arm plus STOP switch), HDMA
    (general-purpose and HBlank modes), the infrared register as a
    documented stub, and the CGB boot register set (A=0x11).
  - Timing model: the frame stays 70224 device dots. An M-cycle
    is 4 device dots at normal speed and 2 in double speed; the
    timer divider advances at CPU rate, the APU frame sequencer
    is clocked from a divider twin that always advances at the
    device rate so music tempo does not change with CPU speed,
    and the serial bit period halves in double speed (the CGB
    fast clock is 16 dots). OAM DMA is M-cycle paced in both
    speeds (one byte per M-cycle after a 2 M-cycle startup); a
    real GBC homebrew caught why this matters: with the DMA
    clocked in raw dots it outlasted the game's HRAM wait loop in
    double speed and the CPU executed open-bus bytes into a reset
    loop.
  - DMG compatibility mode (a DMG cartridge on CGB hardware) is
    modeled as a register lock surface only: KEY1, HDMA, the
    palette data ports, OPRI, SVBK, and FF74 read 0xFF while the
    index registers and FF72/FF73/FF75 keep working, and the boot
    registers are the compatibility set. Compatibility coloring
    of DMG games is out of scope; DMG cartridges in the app run
    on the DMG model, so compatibility mode is exercised by the
    CGB conformance ROMs (which are DMG images written for CGB
    hardware) rather than by shipped games.
  - The framebuffer is XRGB8888 end to end (the four DMG shades
    go through the DMG palette registers into the same format),
    the shell negotiates pixel format 1, 160x144 at 59.7275 fps
    with a 10/9 aspect, audio batch at 44100 Hz, and the memory
    interface exposes cartridge RAM and work RAM banks 0-1.
  - Save states grew to layout version 2 (the CGB blocks join the
    trailer; version 1 states are rejected cleanly). A CGB state
    is 273657 bytes for µCity (a 128 KB RAM cartridge) and a DMG
    state is 150777 bytes for Tobu Tobu Girl.
  - Proofs on the built core through the iTaK bridge: Tobu Tobu
    Girl (DMG) passes the engine test end to end (ALL TESTS
    PASSED). µCity (GBC) loads, renders, plays non-silent audio,
    and round-trips the per-player save manager; its one
    enginetest flag is the restore check, whose premise (screen
    unchanged two frames after the save point) the game's
    blinking menu violates on its own timeline. The stronger
    proof passes exactly: state bytes re-saved after a restore
    are identical (0 of 273657 differ), and restored continuation
    frames and audio match the original continuation hash for
    hash over 300 frames. No commercial ROMs were available
    locally for these proofs; both games are homebrew (see
    `homebrew/SOURCES.md`).

## Conformance notes

Mooneye acceptance: 43 of 75 ROMs report a pass (the rest target
other boot ROMs or models, the GameShark environment, CGB/SGB
behavior, or intra-instruction access order inside instruction
families). The GB-2 gating sets all pass: the timer suite entire,
the OAM DMA suite, the PPU timing and STAT set, and
interrupts/ie_push. One PPU test is excluded from the gate with
its reason recorded in the test: intr_2_mode0_timing_sprites
measures the pixel FIFO's internal fetch phase between sprites at
different X positions, which a line-wise renderer does not
simulate.

CGB conformance (GB-3): cgb-acid2 renders pixel-exact against its
reference image, and the gated mooneye CGB misc ROMs pass
(boot_regs-cgb, bits/unused_hwio-C). Excluded with reasons
recorded in the test: the boot_div-cgb tests and boot_hwio-C
(boot ROM execution state this machine does not model; it models
the documented register hand-off), and ppu/vblank_stat_intr-C
(interrupt latency measured against a mid-frame DIV reset with
one 256-dot period of tolerance, balanced on the boot ROM's
frame phase). The Blargg DMG suites (cpu_instrs 11/11 plus
combined, dmg_sound 12/12) and dmg-acid2 stay green.

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
- `libretro/` the libretro C ABI shell (c-shared build): 24
  retro_* exports mirroring the go-genesis shell's structure,
  XRGB8888 video, 44100 Hz audio batch, serialize via the system
  state, and the standard joypad bit mapping (A/X to the console
  A, B/Y to the console B)
