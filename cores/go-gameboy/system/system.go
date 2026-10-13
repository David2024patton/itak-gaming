// Package system is the Game Boy machine: the SM83 CPU, the
// cartridge, the PPU, the APU, and the small devices on the bus
// (timer and divider, OAM DMA, joypad, serial port) wired together
// and clocked dot by dot. One frame is 70,224 dots (456 per line,
// 154 lines) at the 4,194,304 Hz master clock.
//
// A cartridge flagged CGB (header 0x143 bit 7) runs the machine as
// a Game Boy Color: color PPU, VRAM and WRAM banking, double-speed
// mode, HDMA, and the CGB registers. Timing model for speed: the
// frame is paced in device dots (the 4.194 MHz domain the PPU,
// APU, and RTC live in). The CPU consumes M-cycles; one M-cycle
// spans 4 device dots at normal speed and 2 in double speed, the
// timer/divider advances once per CPU dot (so TIMA runs at CPU
// rate), the APU frame sequencer is clocked from a divider twin
// that always advances at the device rate (music tempo does not
// change with CPU speed), and the serial bit clock halves its
// period in double speed, per the published double-speed rules.
//
// The frame shape mirrors the Genesis core in this repository:
// RunFrame advances one video frame, Framebuffer returns the
// finished picture, AudioBuffer drains the frame's stereo samples,
// and Serialize/Deserialize snapshot the whole machine.
//
// Timing model: the CPU executes in M-cycle steps (4 dots each);
// after every step the machine ticks all devices by the elapsed
// dots. Device state changes (timer edges, PPU modes, DMA bytes)
// are computed per dot inside the tick, so interrupt requests land
// on their exact dots; the CPU samples them at instruction
// boundaries, as the hardware does.
package system

import (
	"encoding/binary"
	"errors"

	"github.com/davidpatton/itak-gaming/cores/go-gameboy/cart"
	"github.com/davidpatton/itak-gaming/cores/go-gameboy/chips/sm83"
	"github.com/davidpatton/itak-gaming/cores/go-gameboy/ppu"
	"github.com/davidpatton/itak-gaming/cores/go-gameboy/sound"
)

const (
	masterHz     = 4194304
	dotsPerFrame = ppu.DotsPerFrame
	sampleRate   = sound.SampleRate
)

// Button bits for SetButtons, following the order the joypad
// register presents them: directions first, then actions.
const (
	ButtonRight  uint16 = 1 << 0
	ButtonLeft   uint16 = 1 << 1
	ButtonUp     uint16 = 1 << 2
	ButtonDown   uint16 = 1 << 3
	ButtonA      uint16 = 1 << 4
	ButtonB      uint16 = 1 << 5
	ButtonSelect uint16 = 1 << 6
	ButtonStart  uint16 = 1 << 7
)

// IO register addresses.
const (
	regP1   = 0xFF00
	regSB   = 0xFF01
	regSC   = 0xFF02
	regDIV  = 0xFF04
	regTIMA = 0xFF05
	regTMA  = 0xFF06
	regTAC  = 0xFF07
	regIF   = 0xFF0F
	regKEY1 = 0xFF4D // CGB speed switch
	regDMA  = 0xFF46
	regVBK  = 0xFF4F // CGB VRAM bank (PPU)
	regRP   = 0xFF56 // CGB infrared port (stub)
	regSVBK = 0xFF70 // CGB WRAM bank
	regIE   = 0xFFFF
)

// timerBitPos maps the TAC clock select to the divider bit whose
// falling edge increments TIMA: bits 9, 3, 5, 7 for periods 1024,
// 16, 64, 256 dots.
var timerBitPos = [4]uint{9, 3, 5, 7}

// System is one Game Boy console.
type System struct {
	CPU  *sm83.CPU
	Cart *cart.Cart

	ppu *ppu.PPU
	apu *sound.APU

	// CGB model state. cgb is decided by the cartridge (or forced
	// by NewCGB for test ROMs written for CGB hardware). speed is
	// the CPU speed multiplier, 1 or 2; the timer divider
	// advances speed times per device dot while seqDiv, the frame
	// sequencer's divider twin, advances once.
	cgb      bool
	forceCGB bool
	compat   bool // DMG cart on CGB hardware: CGB feature registers lock
	speed    int
	key1Arm  bool
	seqDiv   uint16
	svbk     uint8 // SVBK register as written (bank in bits 0-2)
	rpReg    uint8 // infrared port writes, stored; reads are the stub
	undocCGB [4]uint8

	// HDMA (CGB VRAM DMA). Source and destination registers are
	// kept as written; the running transfer tracks the current
	// addresses and remaining block count. A general-purpose
	// transfer copies immediately on the HDMA5 write and stalls
	// the CPU for its transfer time via hdmaStallMC.
	hdmaSrcLo, hdmaSrcHi uint8
	hdmaDstLo, hdmaDstHi uint8
	hdmaActive           bool
	hdmaBlocks           int
	hdmaSrc              uint16
	hdmaDst              uint16 // VRAM offset 0x0000-0x1FF0
	hdmaStallMC          int
	hdmaCancelled        bool

	wram  [0x8000]byte // 8 banks of 4 KB; the DMG uses banks 0 and 1
	hram  [0x7F]byte
	ie    uint8
	ieReg uint8 // IE as written: reads return the full stored byte
	ifr   uint8 // interrupt flags (0xFF0F low 5 bits are live)

	// Timer and divider.
	divider   uint16 // full 16-bit divider counter
	tima      uint8
	tma       uint8
	tac       uint8
	timerBit  bool  // current level of the timer's AND output
	overflow  bool  // a TIMA reload is pending
	reloadIn  int   // dots until the pending reload fires
	timaBlock bool  // legacy layout field, unused by the GB-2 timer
	timaMark  int64 // dot of the last accepted TIMA write
	ovflDot   int64 // dot of the pending overflow's edge
	reloadDot int64 // dot the most recent reload landed on

	// OAM DMA. A write starts a schedule: 8 dots (2 M-cycles)
	// pass before the first byte moves, then one byte every 4
	// dots, and the bus restriction lifts 648 dots after the
	// write. dmaClock counts dots since the write (-1 = idle).
	// A write during a transfer queues a restart that takes over
	// 8 dots later while the old transfer keeps running.
	dmaReg     uint8 // last value written; reads return it
	dmaSrc     uint16
	dmaClock   int
	dmaPendSrc uint16
	dmaPend    int // dots until a queued restart takes over; -1 = none

	// Joypad.
	buttons uint16 // SetButtons state, 1 = pressed
	p1Sel   uint8  // P1 select lines as written (bits 4-5)
	p1Prev  uint8  // last computed P1 low nibble, for the press edge

	// Serial.
	serialSB     uint8
	serialSC     uint8
	serialActive bool
	serialBits   int // bits shifted so far in this transfer
	serialClock  int // dots toward the next bit
	serial       []byte

	audio  []int16 // samples produced since the last AudioBuffer call
	dot    uint64  // total dots run since power-on
	frame  uint64  // frames completed
	frac   int     // dots into the current frame
	rtcAcc uint64  // dots accumulated toward the next RTC second

	loaded bool
}

// New builds a machine around a cartridge image: the cartridge is
// parsed, the CPU is reset to the post-boot state (the values
// the boot ROM leaves behind at hand-off to 0x0100), and the
// divider starts at its post-boot phase. A CGB-flagged cartridge
// powers the machine on as a Game Boy Color.
func New(image []byte) (*System, error) {
	s := &System{}
	if err := s.LoadGame(image); err != nil {
		return nil, err
	}
	return s, nil
}

// NewCGB builds a machine that runs as a Game Boy Color even for
// a cartridge without the CGB flag. Real CGB hardware does
// exactly this for the mooneye CGB test ROMs, which are built as
// plain images but written for CGB hardware; it exists for the
// conformance harness.
func NewCGB(image []byte) (*System, error) {
	s := &System{forceCGB: true}
	if err := s.LoadGame(image); err != nil {
		return nil, err
	}
	return s, nil
}

// CGB reports whether the machine is running as a Game Boy Color.
func (s *System) CGB() bool { return s.cgb }

// Speed returns the current CPU speed multiplier (1 or 2).
func (s *System) Speed() int {
	if s.speed == 2 {
		return 2
	}
	return 1
}

// mcycleDots is the number of device dots one CPU M-cycle spans
// at the current speed: 4 at normal speed, 2 in double speed.
func (s *System) mcycleDots() int {
	if s.speed == 2 {
		return 2
	}
	return 4
}

// LoadGame builds the machine around a cartridge image and powers
// it on.
func (s *System) LoadGame(image []byte) error {
	ct, err := cart.Load(image)
	if err != nil {
		return err
	}
	if ct.Kind == cart.MBC2 || ct.Kind == cart.MBCUnsupported {
		return errors.New("system: mapper not implemented")
	}
	s.cgb = ct.CGBFlag&0x80 != 0 || s.forceCGB
	// A DMG cartridge on CGB hardware boots into DMG
	// compatibility mode: the color hardware is present, but
	// the feature registers a game could use to leave DMG mode
	// (KEY1, HDMA, the palette data ports, OPRI, SVBK, FF74) lock
	// to 0xFF, as the hardware locks them. New() with a DMG cart
	// never takes this path (it models DMG hardware, where those
	// registers are simply absent); only CGB hardware, real or
	// forced, shows the locks.
	s.compat = s.cgb && ct.CGBFlag&0x80 == 0
	s.CPU = sm83.New()
	s.CPU.OnCycle = func() { s.tick(s.mcycleDots()) }
	s.CPU.OnStopSwitch = func() bool {
		if !s.cgb || s.compat || !s.key1Arm {
			return false
		}
		s.key1Arm = false
		if s.speed == 2 {
			s.speed = 1
		} else {
			s.speed = 2
		}
		return true
	}
	s.Cart = ct
	if s.cgb {
		s.ppu = ppu.NewCGB()
		if s.compat {
			s.ppu.SetCompat()
		}
	} else {
		s.ppu = ppu.New()
	}
	s.ppu.OnHBlank = func() { s.hdmaHBlank() }
	s.apu = sound.New()
	s.audio = nil
	s.loaded = true
	s.Reset()
	return nil
}

// Loaded reports whether a game is in the machine.
func (s *System) Loaded() bool { return s.loaded }

// Reset restores the machine to the post-boot state: the DMG
// hand-off values, or the CGB hand-off values (A=0x11 marks CGB
// hardware to the game) when the machine runs as a Color.
// Cartridge RAM survives, as battery-backed RAM does on hardware.
func (s *System) Reset() {
	if !s.loaded {
		return
	}
	s.CPU.Reset()
	if s.compat {
		// DMG cartridge on CGB hardware: the compatibility boot
		// path leaves its own register set (hardware-verified by
		// mooneye boot_regs-cgb).
		s.CPU.SetRegs(sm83.Registers{
			A: 0x11, F: 0x80, B: 0x00, C: 0x00,
			D: 0x00, E: 0x08, H: 0x00, L: 0x7C,
			SP: 0xFFFE, PC: 0x0100,
		})
	} else if s.cgb {
		s.CPU.SetRegs(sm83.Registers{
			A: 0x11, F: 0x80, B: 0x00, C: 0x00,
			D: 0xFF, E: 0x56, H: 0x00, L: 0x0D,
			SP: 0xFFFE, PC: 0x0100,
		})
	} else {
		s.CPU.SetRegs(sm83.Registers{
			A: 0x01, F: 0xB0, B: 0x00, C: 0x13,
			D: 0x00, E: 0xD8, H: 0x01, L: 0x4D,
			SP: 0xFFFE, PC: 0x0100,
		})
	}
	s.speed = 1
	s.key1Arm = false
	s.svbk = 1
	s.rpReg = 0
	s.undocCGB = [4]uint8{}
	s.hdmaSrcLo, s.hdmaSrcHi, s.hdmaDstLo, s.hdmaDstHi = 0, 0, 0, 0
	s.hdmaActive = false
	s.hdmaBlocks = 0
	s.hdmaSrc, s.hdmaDst = 0, 0
	s.hdmaStallMC = 0
	s.hdmaCancelled = false
	s.ppu.Reset()
	*s.apu = *sound.New()
	for i := range s.wram {
		s.wram[i] = 0
	}
	for i := range s.hram {
		s.hram[i] = 0
	}
	s.ie = 0
	s.ifr = 0
	// DMG hand-off: the divider's high byte reads 0xAB at 0x0100.
	// The frame sequencer's divider twin starts in lockstep; the
	// two only diverge once double speed engages.
	s.divider = 0xAB << 8
	s.seqDiv = 0xAB << 8
	s.tima, s.tma, s.tac = 0, 0, 0
	s.timerBit = false
	s.overflow = false
	s.reloadIn = 0
	s.timaBlock = false
	s.dmaReg = 0
	s.dmaClock = -1
	s.dmaPend = -1
	s.timaMark = -1 << 40
	s.ovflDot = -1 << 40
	s.reloadDot = -1 << 40
	s.p1Sel = 0x30
	s.p1Prev = 0x0F
	s.serialSB, s.serialSC = 0, 0
	s.serialActive = false
	s.serialBits = 0
	s.serialClock = 0
	s.serial = s.serial[:0]
	s.audio = nil
	s.dot = 0
	s.frame = 0
	s.frac = 0
	s.rtcAcc = 0
}

// Title returns the cartridge title, for logs and UIs.
func (s *System) Title() string {
	if s.Cart == nil {
		return ""
	}
	return s.Cart.Title
}

// SetButtons records pad state. The mask uses the Button* bits;
// the player argument exists for parity with the other cores and
// is ignored (the Game Boy has one pad).
func (s *System) SetButtons(player int, mask uint16) {
	s.buttons = mask
	if mask != 0 && s.CPU != nil && s.CPU.Stopped() {
		s.CPU.Wake()
	}
	s.updateJoypadEdge()
}

// Framebuffer returns the PPU's XRGB8888 frame (160 by 144).
func (s *System) Framebuffer() []uint32 {
	if !s.loaded {
		return nil
	}
	return s.ppu.Framebuffer()
}

// Width is the display width in pixels.
func (s *System) Width() int { return ppu.Width }

// Height is the display height in pixels.
func (s *System) Height() int { return ppu.Height }

// SystemRAM exposes work RAM banks 0 and 1 (the fixed bank and
// the default switchable bank, the 8 KB a DMG program sees) for
// the libretro memory interface.
func (s *System) SystemRAM() []uint8 {
	if !s.loaded {
		return nil
	}
	return s.wram[:0x2000]
}

// wramBank resolves the SVBK-selected bank for the D000 window:
// bank 0 in the register means bank 1, and the DMG always sees
// bank 1.
func (s *System) wramBank() int {
	if !s.cgb {
		return 1
	}
	b := int(s.svbk & 0x07)
	if b == 0 {
		b = 1
	}
	return b
}

// readWRAM serves the C000-DFFF work RAM window with banking.
func (s *System) readWRAM(addr uint16) uint8 {
	off := int(addr - 0xC000)
	if off < 0x1000 {
		return s.wram[off]
	}
	return s.wram[s.wramBank()*0x1000+off-0x1000]
}

// writeWRAM writes the C000-DFFF work RAM window with banking.
func (s *System) writeWRAM(addr uint16, val uint8) {
	off := int(addr - 0xC000)
	if off < 0x1000 {
		s.wram[off] = val
		return
	}
	s.wram[s.wramBank()*0x1000+off-0x1000] = val
}

// SaveRAM exposes the cartridge save RAM, if the cartridge has any.
func (s *System) SaveRAM() []uint8 {
	if !s.loaded || s.Cart == nil {
		return nil
	}
	return s.Cart.RAM
}

// AudioBuffer drains the samples produced by the last RunFrame.
func (s *System) AudioBuffer() []int16 {
	out := s.audio
	s.audio = nil
	return out
}

// Serial returns everything the ROM has written to the serial
// port, captured the way a link cable logger would see it.
func (s *System) Serial() string { return string(s.serial) }

// Cycles returns total dots run since power-on.
func (s *System) Cycles() uint64 { return s.dot }

// requestIRQ sets a bit in the IF register.
func (s *System) requestIRQ(bit uint8) { s.ifr |= 1 << bit }

// ppuIRQ is the PPU's interrupt callback.
func (s *System) ppuIRQ(bit int) { s.requestIRQ(uint8(bit)) }

// Step runs one CPU step (one instruction, one dispatch, or one
// idle M-cycle) and advances every device by the elapsed dots.
// The CPU's OnCycle hook ticks one M-cycle before each bus access
// after the first in the step, so accesses observe the machine on
// their hardware cycle; Step then ticks the step's remaining
// cycles (purely internal ones and the tail of the last access
// cycle).
func (s *System) Step() int {
	if s.hdmaStallMC > 0 {
		// A general-purpose HDMA transfer is running: the CPU
		// is halted while the machine clocks on.
		s.hdmaStallMC--
		s.tick(s.mcycleDots())
		return 1
	}
	m := s.CPU.Step(s)
	ticked := 0
	if used := s.CPU.SlotsUsed(); used > 0 {
		ticked = used - 1
	}
	s.tick((m - ticked) * s.mcycleDots())
	return m
}

// RunMCycles runs until at least n M-cycles have elapsed.
func (s *System) RunMCycles(n int) {
	for n > 0 {
		n -= s.Step()
	}
}

// RunFrame runs one whole video frame (70,224 dots) and collects
// the frame's audio into the AudioBuffer.
func (s *System) RunFrame() {
	if !s.loaded {
		return
	}
	target := dotsPerFrame - s.frac
	run := 0
	for run < target {
		m := s.Step()
		run += m * s.mcycleDots()
	}
	s.frac = run - target // overshoot carries into the next frame
	s.frame++
	s.audio = append(s.audio, s.apu.Produce(-1)...)
}

// tick advances all devices by the given number of device dots.
// The timer divider advances once per CPU dot (speed steps per
// device dot), so TIMA runs at CPU rate in double speed; the
// frame sequencer's divider twin advances once per device dot,
// keeping the APU sequencer at 512 Hz of device time at either
// CPU speed.
func (s *System) tick(dots int) {
	steps := s.speed
	if steps < 1 {
		steps = 1
	}
	for i := 0; i < dots; i++ {
		s.dot++
		for k := 0; k < steps; k++ {
			s.tickTimerStep()
		}
		s.tickSeqDot()
		s.ppu.Tick(1, s.ppuIRQ)
		s.tickDMADot()
		s.tickSerialDot()
		s.apu.Tick(1)
		s.tickRTCDot()
	}
}

// tickTimerStep advances the divider by one CPU dot and applies
// the timer's falling-edge rule: TIMA increments when the AND of
// the TAC enable and the selected divider bit goes from 1 to 0.
// Overflow reload bookkeeping runs before the edge check: a
// reload scheduled for this step lands first.
func (s *System) tickTimerStep() {
	s.divider++
	if s.overflow {
		s.reloadIn--
		if s.reloadIn <= 0 {
			s.overflow = false
			if !s.timaBlock {
				s.tima = s.tma
				s.reloadDot = int64(s.dot)
				s.requestIRQ(2)
			}
			s.timaBlock = false
		}
	}
	bit := s.timerSignal()
	if s.timerBit && !bit {
		s.incTIMA()
	}
	s.timerBit = bit
}

// tickSeqDot advances the frame sequencer's divider twin by one
// device dot; a falling edge of its bit 12 clocks the APU's frame
// sequencer, the same rule GB-2 applied to the main divider (the
// two are identical until double speed engages).
func (s *System) tickSeqDot() {
	prevBit12 := s.seqDiv>>12&1 == 1
	s.seqDiv++
	if prevBit12 && s.seqDiv>>12&1 == 0 {
		s.apu.StepFrameSequencer()
	}
}

// timerSignal computes the timer circuit's current output: the TAC
// enable ANDed with the selected divider bit.
func (s *System) timerSignal() bool {
	if s.tac&0x04 == 0 {
		return false
	}
	return s.divider>>timerBitPos[s.tac&0x03]&1 == 1
}

// incTIMA increments TIMA with the overflow sequence: on overflow
// TIMA reads 0 for one M-cycle, then the TMA value is loaded and
// the timer interrupt is requested. An increment edge that lands
// in the same M-cycle as an accepted TIMA write is discarded
// whole: the counter keeps the written value and no overflow is
// signaled. Writes sit at dots that are multiples of 4 (access
// slots), so the write's cycle is the span [mark, mark+4) and an
// edge strictly inside it is the same-cycle case the mooneye
// tima_write_reloading rounds pin down.
func (s *System) incTIMA() {
	if d := int64(s.dot) - s.timaMark; d >= 0 && d < int64(s.mcycleDots()) {
		return
	}
	s.timaMark = -1 << 40
	if s.tima == 0xFF {
		s.tima = 0
		s.overflow = true
		s.reloadIn = 4
		s.ovflDot = int64(s.dot)
		s.timaBlock = false
	} else {
		s.tima++
	}
}

// timerGlitchCheck runs after DIV or TAC writes: if the write made
// the timer signal fall, TIMA increments immediately.
func (s *System) timerGlitchCheck() {
	bit := s.timerSignal()
	if s.timerBit && !bit {
		s.incTIMA()
	}
	s.timerBit = bit
}

// tickDMADot advances the OAM DMA schedule by one dot. A queued
// restart counts down first; when it fires, the new transfer
// takes over (its byte 0 copies on that dot) and the old one is
// abandoned. Otherwise the running transfer copies byte idx at
// dot 8+4*idx after its write and ends at dot 648.
// tickDMADot advances the OAM DMA by one device dot. The
// transfer itself is M-cycle paced (a 2 M-cycle startup, then
// one byte per M-cycle), so its dot length halves in double
// speed: 648 dots at normal speed, 324 in double speed. A
// transfer that outlasts the game's calibrated HRAM wait loop
// would feed the CPU open-bus bytes mid-instruction-stream, so
// this pacing matters for correctness, not just timing purity.
func (s *System) tickDMADot() {
	mc := s.mcycleDots()
	if s.dmaPend >= 0 {
		s.dmaPend--
		if s.dmaPend == 0 {
			s.dmaPend = -1
			s.dmaSrc = s.dmaPendSrc
			s.dmaClock = 2 * mc
			s.ppu.WriteOAMRaw(0, s.readRaw(s.dmaSrc))
			return
		}
	}
	if s.dmaClock < 0 {
		return
	}
	s.dmaClock++
	if s.dmaClock >= 2*mc && (s.dmaClock-2*mc)%mc == 0 {
		if idx := (s.dmaClock - 2*mc) / mc; idx < 160 {
			s.ppu.WriteOAMRaw(idx, s.readRaw(s.dmaSrc+uint16(idx)))
		}
	}
	if s.dmaClock >= 2*mc+160*mc {
		s.dmaClock = -1
	}
}

// tickSerialDot advances an internal-clock serial transfer. The
// bit period is 512 device dots (8192 Hz) at normal clock, or 16
// dots (262144 Hz) with the CGB fast clock (SC bit 1); double
// speed halves either period, as the published double-speed
// rules describe.
func (s *System) tickSerialDot() {
	if !s.serialActive {
		return
	}
	period := 512
	if s.cgb && s.serialSC&0x02 != 0 {
		period = 16
	}
	period /= s.Speed()
	s.serialClock++
	if s.serialClock >= period {
		s.serialClock = 0
		s.serialBits++
		if s.serialBits == 8 {
			s.serialActive = false
			s.serialSC &^= 0x80
			s.serial = append(s.serial, s.serialSB)
			s.requestIRQ(3)
		}
	}
}

// tickRTCDot advances the MBC3 real-time clock from emulated time:
// one second of dots per RTC second, when the cart has a timer and
// it is not halted.
func (s *System) tickRTCDot() {
	if s.Cart == nil || !s.Cart.HasTimer {
		return
	}
	if s.Cart.RTC.DayHigh&0x40 != 0 {
		return // halted
	}
	s.rtcAcc++
	if s.rtcAcc < masterHz {
		return
	}
	s.rtcAcc -= masterHz
	r := &s.Cart.RTC
	r.Seconds++
	if r.Seconds >= 60 {
		r.Seconds = 0
		r.Minutes++
		if r.Minutes >= 60 {
			r.Minutes = 0
			r.Hours++
			if r.Hours >= 24 {
				r.Hours = 0
				day := uint16(r.DayLow) | uint16(r.DayHigh&1)<<8
				day++
				if day >= 512 {
					day = 0
					r.DayHigh |= 0x80 // day carry
				}
				r.DayLow = uint8(day)
				r.DayHigh = r.DayHigh&^1 | uint8(day>>8)&1
			}
		}
	}
}

// p1Read computes the joypad register's current value from the
// select lines and button state.
func (s *System) p1Read() uint8 {
	low := uint8(0x0F)
	if s.p1Sel&0x10 == 0 { // actions selected
		if s.buttons&ButtonA != 0 {
			low &^= 0x01
		}
		if s.buttons&ButtonB != 0 {
			low &^= 0x02
		}
		if s.buttons&ButtonSelect != 0 {
			low &^= 0x04
		}
		if s.buttons&ButtonStart != 0 {
			low &^= 0x08
		}
	}
	if s.p1Sel&0x20 == 0 { // directions selected
		if s.buttons&ButtonRight != 0 {
			low &^= 0x01
		}
		if s.buttons&ButtonLeft != 0 {
			low &^= 0x02
		}
		if s.buttons&ButtonUp != 0 {
			low &^= 0x04
		}
		if s.buttons&ButtonDown != 0 {
			low &^= 0x08
		}
	}
	return 0xC0 | s.p1Sel | low
}

// updateJoypadEdge requests the joypad interrupt when a selected
// button line falls (a press), the hardware's edge rule.
func (s *System) updateJoypadEdge() {
	low := s.p1Read() & 0x0F
	if s.p1Prev&^low != 0 {
		s.requestIRQ(4)
	}
	s.p1Prev = low
}

// dmaRestricts reports whether the CPU's bus access is currently
// restricted to HRAM by an OAM DMA transfer. The IF and IE
// registers stay reachable: the CPU's interrupt sampling reads
// them internally, not over the shared bus.
func (s *System) dmaRestricts(addr uint16) bool {
	mc := s.mcycleDots()
	if s.dmaClock < 2*mc || s.dmaClock >= 2*mc+160*mc {
		return false
	}
	if addr == regIF || addr == regIE || addr == regDMA {
		return false
	}
	return addr < 0xFF80
}

// Read implements the bus map for the CPU.
func (s *System) Read(addr uint16) uint8 {
	if s.dmaRestricts(addr) {
		return 0xFF
	}
	return s.readRaw(addr)
}

// readRaw is the unrestricted bus read, used by the DMA unit whose
// transfers are not subject to PPU mode blocking.
func (s *System) readRaw(addr uint16) uint8 {
	switch {
	case addr < 0x8000:
		return s.Cart.ReadROM(addr)
	case addr < 0xA000:
		return s.ppu.Read(addr)
	case addr < 0xC000:
		return s.Cart.ReadRAM(addr)
	case addr < 0xE000:
		return s.readWRAM(addr)
	case addr < 0xFE00:
		return s.readWRAM(addr - 0x2000) // echo RAM mirrors 0xC000-0xDDFF
	case addr < 0xFEA0:
		return s.ppu.Read(addr)
	case addr < 0xFF00:
		return 0xFF // unusable area
	}
	switch {
	case addr == regP1:
		return s.p1Read()
	case addr == regSB:
		return s.serialSB
	case addr == regSC:
		// Bits 1-6 read as 1 on both models (mooneye-verified:
		// on CGB hardware the clock select bit also reads 1 even
		// written 0; the stored bit still drives the transfer).
		return s.serialSC | 0x7E
	case addr == regDIV:
		return uint8(s.divider >> 8)
	case addr == regTIMA:
		return s.tima
	case addr == regTMA:
		return s.tma
	case addr == regTAC:
		return s.tac | 0xF8
	case addr == regIF:
		return s.ifr | 0xE0
	case addr >= 0xFF10 && addr < 0xFF40:
		return s.apu.Read(addr)
	case addr == regKEY1:
		if !s.cgb || s.compat {
			return 0xFF
		}
		v := uint8(0x7E)
		if s.speed == 2 {
			v |= 0x80
		}
		if s.key1Arm {
			v |= 0x01
		}
		return v
	case addr == regVBK || (addr >= 0xFF68 && addr <= 0xFF6C):
		return s.ppu.Read(addr)
	case addr >= 0xFF51 && addr <= 0xFF55:
		return s.hdmaRead(addr)
	case addr == regRP:
		// Infrared port stub: no signal received, the
		// documented idle read.
		return 0xFF
	case addr == regSVBK:
		if !s.cgb || s.compat {
			return 0xFF
		}
		return 0xF8 | s.svbk&0x07
	case addr >= 0xFF72 && addr <= 0xFF75:
		if !s.cgb {
			return 0xFF
		}
		if addr == 0xFF74 && s.compat {
			return 0xFF // FF74 exists only in full CGB mode
		}
		if addr == 0xFF75 {
			return 0x8F | s.undocCGB[3]&0x70
		}
		return s.undocCGB[addr-0xFF72]
	case addr >= 0xFF76 && addr <= 0xFF77:
		// PCM12/PCM34: the APU's live channel level outputs.
		// The APU does not expose per-channel DAC levels, so
		// the registers read as the idle value 0; on DMG
		// hardware they are absent entirely.
		if s.cgb {
			return 0x00
		}
		return 0xFF
	case addr >= 0xFF40 && addr < 0xFF4C:
		if addr == regDMA {
			return s.dmaReg
		}
		return s.ppu.Read(addr)
	case addr == regIE:
		return s.ieReg
	case addr >= 0xFF80:
		return s.hram[addr-0xFF80]
	}
	return 0xFF // unimplemented IO reads as open bus
}

// Write implements the bus map for the CPU.
func (s *System) Write(addr uint16, val uint8) {
	if s.dmaRestricts(addr) {
		return
	}
	switch {
	case addr < 0x8000:
		s.Cart.Write(addr, val)
		return
	case addr < 0xA000:
		s.ppu.Write(addr, val, s.ppuIRQ)
		return
	case addr < 0xC000:
		s.Cart.Write(addr, val)
		return
	case addr < 0xE000:
		s.writeWRAM(addr, val)
		return
	case addr < 0xFE00:
		s.writeWRAM(addr-0x2000, val)
		return
	case addr < 0xFEA0:
		s.ppu.Write(addr, val, s.ppuIRQ)
		return
	case addr < 0xFF00:
		return // unusable area: writes are ignored
	}
	switch {
	case addr == regP1:
		s.p1Sel = val & 0x30
		s.updateJoypadEdge()
	case addr == regSB:
		s.serialSB = val
	case addr == regSC:
		if s.cgb {
			s.serialSC = val & 0x83
		} else {
			s.serialSC = val & 0x81
		}
		if val&0x81 == 0x81 {
			// Internal clock: the transfer runs for eight bit
			// periods, then completes and interrupts.
			s.serialActive = true
			s.serialBits = 0
			s.serialClock = 0
		} else {
			s.serialActive = false
		}
	case addr == regDIV:
		prevBit12 := s.seqDiv>>12&1 == 1
		s.divider = 0
		s.seqDiv = 0
		s.timerGlitchCheck()
		if prevBit12 {
			// The write dropped bit 12: the frame sequencer
			// clocks, the same quirk the hardware has.
			s.apu.StepFrameSequencer()
		}
	case addr == regTIMA:
		// The reload interactions, as pinned by the mooneye
		// write-during-reload rounds: a write landing on the
		// reload's own dot loses to the reload and is dropped;
		// a write landing while the overflow is fresh (same
		// M-cycle span as the edge, reload still pending)
		// cancels the reload and its interrupt entirely.
		if int64(s.dot) == s.reloadDot {
			// dropped: the reload owns this dot
		} else if s.overflow && int64(s.dot)-s.ovflDot < int64(s.mcycleDots()) {
			s.overflow = false
			s.tima = val
			s.timaMark = int64(s.dot)
		} else if !s.overflow {
			s.tima = val
			s.timaMark = int64(s.dot)
		}
		// A write mid-window (past the edge's cycle but before
		// the reload dot) is dropped as well: the counter is
		// committed to the reload.

	case addr == regTMA:
		s.tma = val
		// A TMA write landing on the reload's own dot still
		// feeds the reload: the load samples TMA at the end of
		// its cycle. (mooneye tma_write_reloading.)
		if int64(s.dot) == s.reloadDot {
			s.tima = val
		}
	case addr == regTAC:
		s.tac = val & 0x07
		s.timerGlitchCheck()
	case addr == regIF:
		s.ifr = val & 0x1F
	case addr >= 0xFF10 && addr < 0xFF40:
		s.apu.Write(addr, val)
	case addr == regKEY1:
		if s.cgb && !s.compat {
			s.key1Arm = val&0x01 != 0
		}
	case addr == regVBK || (addr >= 0xFF68 && addr <= 0xFF6C):
		s.ppu.Write(addr, val, s.ppuIRQ)
	case addr >= 0xFF51 && addr <= 0xFF55:
		s.hdmaWrite(addr, val)
	case addr == regRP:
		s.rpReg = val // stored, no emitter attached (stub)
	case addr == regSVBK:
		if s.cgb && !s.compat {
			s.svbk = val & 0x07
		}
	case addr >= 0xFF72 && addr <= 0xFF75:
		if s.cgb {
			if addr == 0xFF74 && s.compat {
				// locked in compatibility mode
			} else if addr == 0xFF75 {
				s.undocCGB[3] = val & 0x70
			} else {
				s.undocCGB[addr-0xFF72] = val
			}
		}
	case addr >= 0xFF40 && addr < 0xFF4C:
		if addr == regDMA {
			s.startDMA(val)
		} else {
			s.ppu.Write(addr, val, s.ppuIRQ)
		}
	case addr == regIE:
		s.ie = val
		s.ieReg = val
	case addr >= 0xFF80:
		s.hram[addr-0xFF80] = val
	}
}

// startDMA records a write to the DMA register. With no transfer
// running, the schedule starts now; mid-transfer, the new source
// queues a restart that takes over 8 dots later.
func (s *System) startDMA(val uint8) {
	s.dmaReg = val
	if s.dmaClock >= 0 {
		s.dmaPendSrc = uint16(val) << 8
		s.dmaPend = 2 * s.mcycleDots()
		return
	}
	s.dmaSrc = uint16(val) << 8
	s.dmaClock = 0
}

// --- HDMA (CGB VRAM DMA) -------------------------------------------------

// hdmaRead serves FF51-FF55. The source and destination registers
// read back as written; HDMA5 reads the transfer state: bit 7
// clear with the remaining block count minus one while an HBlank
// transfer runs, the same value with bit 7 set after a
// cancellation, and 0xFF once no transfer state remains. In DMG
// compatibility mode the whole register block locks to 0xFF.
func (s *System) hdmaRead(addr uint16) uint8 {
	if s.compat {
		return 0xFF
	}
	switch addr {
	case 0xFF51:
		return s.hdmaSrcHi
	case 0xFF52:
		return s.hdmaSrcLo
	case 0xFF53:
		return s.hdmaDstHi
	case 0xFF54:
		return s.hdmaDstLo
	case 0xFF55:
		if !s.cgb {
			return 0xFF
		}
		if s.hdmaActive {
			return uint8(s.hdmaBlocks - 1)
		}
		if s.hdmaCancelled {
			return 0x80 | uint8(s.hdmaBlocks-1)
		}
		return 0xFF
	}
	return 0xFF
}

// hdmaWrite serves FF51-FF55. FF51-FF54 store the source and
// destination (masked to 16-byte alignment, the destination to
// the VRAM window). An FF55 write with bit 7 clear starts a
// general-purpose transfer: every block copies at once, into the
// VBK-selected VRAM bank, and the CPU stalls for the documented
// transfer time (8 dots per 16-byte block at normal speed, 16 in
// double speed). With bit 7 set it starts an HBlank transfer of
// one block per HBlank; the same write with bit 7 clear while an
// HBlank transfer runs cancels it instead.
func (s *System) hdmaWrite(addr uint16, val uint8) {
	if s.compat {
		return // locked in compatibility mode
	}
	switch addr {
	case 0xFF51:
		s.hdmaSrcHi = val
	case 0xFF52:
		s.hdmaSrcLo = val & 0xF0
	case 0xFF53:
		s.hdmaDstHi = val & 0x1F
	case 0xFF54:
		s.hdmaDstLo = val & 0xF0
	case 0xFF55:
		if !s.cgb {
			return
		}
		if s.hdmaActive {
			if val&0x80 == 0 {
				s.hdmaActive = false
				s.hdmaCancelled = true
			}
			return
		}
		s.hdmaSrc = uint16(s.hdmaSrcHi)<<8 | uint16(s.hdmaSrcLo)
		s.hdmaDst = uint16(s.hdmaDstHi&0x1F)<<8 | uint16(s.hdmaDstLo)
		s.hdmaBlocks = int(val&0x7F) + 1
		s.hdmaCancelled = false
		if val&0x80 == 0 {
			blocks := s.hdmaBlocks
			for i := 0; i < blocks; i++ {
				s.hdmaCopyBlock()
			}
			s.hdmaBlocks = 0
			s.hdmaStallMC += blocks * (8 * s.Speed()) / s.mcycleDots()
		} else {
			s.hdmaActive = true
		}
	}
}

// hdmaCopyBlock moves one 16-byte block from the current source
// to the current VRAM destination offset and advances both.
func (s *System) hdmaCopyBlock() {
	for i := 0; i < 16; i++ {
		s.ppu.WriteVRAMRaw(int(s.hdmaDst)+i, s.readRaw(s.hdmaSrc+uint16(i)))
	}
	s.hdmaSrc += 16
	s.hdmaDst = (s.hdmaDst + 16) & 0x1FF0
}

// hdmaHBlank is the PPU's HBlank hook: an active HBlank HDMA
// transfer moves one block per HBlank on a visible line. With
// the LCD off no HBlanks occur and the transfer waits.
func (s *System) hdmaHBlank() {
	if !s.hdmaActive || !s.ppu.LCDOn() {
		return
	}
	s.hdmaCopyBlock()
	s.hdmaBlocks--
	if s.hdmaBlocks <= 0 {
		s.hdmaBlocks = 0
		s.hdmaActive = false
	}
}

// --- save states ---------------------------------------------------------

// trailerSize holds the machine-level bookkeeping appended after
// the component states: total dots (u64), frame counter (u64),
// dots into the frame (u32), RTC accumulator (u64), and the audio
// slice length is not stored (the buffer is drained at frame
// boundaries, where states are taken).
const trailerSize = 32

// SerializeSize is the total save-state size in bytes. The
// cartridge RAM length varies by cartridge, so the size does too.
func (s *System) SerializeSize() int {
	if !s.loaded {
		return 0
	}
	return sm83.SerializeSize + cart.SerializeSize + len(s.Cart.RAM) +
		len(s.wram) + len(s.hram) + ppu.SerializeSize + sound.SerializeSize +
		128 + trailerSize
}

// Serialize writes the full machine state into buf.
func (s *System) Serialize(buf []byte) error {
	if !s.loaded {
		return errors.New("system: no game loaded")
	}
	if len(buf) < s.SerializeSize() {
		return errors.New("system: serialize buffer too small")
	}
	off := 0
	if err := s.CPU.Serialize(buf[off:]); err != nil {
		return err
	}
	off += sm83.SerializeSize
	if err := s.Cart.Serialize(buf[off:]); err != nil {
		return err
	}
	off += cart.SerializeSize
	copy(buf[off:], s.Cart.RAM)
	off += len(s.Cart.RAM)
	copy(buf[off:], s.wram[:])
	off += len(s.wram)
	copy(buf[off:], s.hram[:])
	off += len(s.hram)
	if err := s.ppu.Serialize(buf[off:]); err != nil {
		return err
	}
	off += ppu.SerializeSize
	if err := s.apu.Serialize(buf[off:]); err != nil {
		return err
	}
	off += sound.SerializeSize

	b := buf[off : off+128]
	for i := range b {
		b[i] = 0
	}
	// Layout version 2 (GB-3): adds the CGB machine state in the
	// block's upper half. Version 1 states are rejected cleanly.
	b[0] = 2 // layout version
	b[1] = s.ie
	b[2] = s.ifr
	binary.LittleEndian.PutUint16(b[3:], s.divider)
	b[5] = s.tima
	b[6] = s.tma
	b[7] = s.tac
	if s.timerBit {
		b[8] = 1
	}
	if s.overflow {
		b[9] = 1
	}
	binary.LittleEndian.PutUint16(b[10:], uint16(s.reloadIn))
	if s.timaBlock {
		b[12] = 1
	}
	b[13] = s.dmaReg
	binary.LittleEndian.PutUint16(b[14:], s.dmaSrc)
	binary.LittleEndian.PutUint16(b[16:], uint16(s.dmaClock+1))
	binary.LittleEndian.PutUint16(b[18:], s.dmaPendSrc)
	binary.LittleEndian.PutUint16(b[30:], uint16(s.dmaPend+1))
	binary.LittleEndian.PutUint64(b[32:], uint64(s.timaMark))
	binary.LittleEndian.PutUint64(b[40:], uint64(s.ovflDot))
	binary.LittleEndian.PutUint64(b[48:], uint64(s.reloadDot))
	binary.LittleEndian.PutUint16(b[20:], s.buttons)
	b[22] = s.p1Sel
	b[23] = s.p1Prev
	b[24] = s.serialSB
	b[25] = s.serialSC
	if s.serialActive {
		b[26] = 1
	}
	b[27] = uint8(s.serialBits)
	binary.LittleEndian.PutUint16(b[28:], uint16(s.serialClock))
	b[64] = uint8(s.Speed())
	if s.key1Arm {
		b[65] = 1
	}
	binary.LittleEndian.PutUint16(b[66:], s.seqDiv)
	b[68] = s.svbk
	b[69] = s.hdmaSrcHi
	b[70] = s.hdmaSrcLo
	b[71] = s.hdmaDstHi
	b[72] = s.hdmaDstLo
	binary.LittleEndian.PutUint16(b[73:], s.hdmaSrc)
	binary.LittleEndian.PutUint16(b[75:], s.hdmaDst)
	binary.LittleEndian.PutUint16(b[77:], uint16(s.hdmaBlocks))
	if s.hdmaActive {
		b[79] |= 1
	}
	if s.hdmaCancelled {
		b[79] |= 2
	}
	binary.LittleEndian.PutUint32(b[80:], uint32(s.hdmaStallMC))
	b[84] = s.rpReg
	copy(b[85:89], s.undocCGB[:])
	off += 128

	binary.LittleEndian.PutUint64(buf[off:], s.dot)
	binary.LittleEndian.PutUint64(buf[off+8:], s.frame)
	binary.LittleEndian.PutUint32(buf[off+16:], uint32(s.frac))
	binary.LittleEndian.PutUint64(buf[off+24:], s.rtcAcc)
	return nil
}

// Deserialize restores a state written by Serialize.
func (s *System) Deserialize(buf []byte) error {
	if !s.loaded {
		return errors.New("system: no game loaded")
	}
	if len(buf) < s.SerializeSize() {
		return errors.New("system: state too small")
	}
	off := 0
	if err := s.CPU.Deserialize(buf[off:]); err != nil {
		return err
	}
	off += sm83.SerializeSize
	if err := s.Cart.Deserialize(buf[off:]); err != nil {
		return err
	}
	off += cart.SerializeSize
	copy(s.Cart.RAM, buf[off:])
	off += len(s.Cart.RAM)
	copy(s.wram[:], buf[off:])
	off += len(s.wram)
	copy(s.hram[:], buf[off:])
	off += len(s.hram)
	if err := s.ppu.Deserialize(buf[off:]); err != nil {
		return err
	}
	off += ppu.SerializeSize
	if err := s.apu.Deserialize(buf[off:]); err != nil {
		return err
	}
	off += sound.SerializeSize

	b := buf[off : off+128]
	if b[0] != 2 {
		return errors.New("system: unsupported state layout version")
	}
	s.ie = b[1]
	s.ieReg = b[1]
	s.ifr = b[2]
	s.divider = binary.LittleEndian.Uint16(b[3:])
	s.tima = b[5]
	s.tma = b[6]
	s.tac = b[7]
	s.timerBit = b[8] != 0
	s.overflow = b[9] != 0
	s.reloadIn = int(binary.LittleEndian.Uint16(b[10:]))
	s.timaBlock = b[12] != 0
	s.dmaReg = b[13]
	s.dmaSrc = binary.LittleEndian.Uint16(b[14:])
	s.dmaClock = int(binary.LittleEndian.Uint16(b[16:])) - 1
	s.dmaPendSrc = binary.LittleEndian.Uint16(b[18:])
	s.dmaPend = int(binary.LittleEndian.Uint16(b[30:])) - 1
	s.timaMark = int64(binary.LittleEndian.Uint64(b[32:]))
	s.ovflDot = int64(binary.LittleEndian.Uint64(b[40:]))
	s.reloadDot = int64(binary.LittleEndian.Uint64(b[48:]))
	s.buttons = binary.LittleEndian.Uint16(b[20:])
	s.p1Sel = b[22]
	s.p1Prev = b[23]
	s.serialSB = b[24]
	s.serialSC = b[25]
	s.serialActive = b[26] != 0
	s.serialBits = int(b[27])
	s.serialClock = int(binary.LittleEndian.Uint16(b[28:]))
	if b[64] == 2 {
		s.speed = 2
	} else {
		s.speed = 1
	}
	s.key1Arm = b[65] != 0
	s.seqDiv = binary.LittleEndian.Uint16(b[66:])
	s.svbk = b[68]
	s.hdmaSrcHi = b[69]
	s.hdmaSrcLo = b[70]
	s.hdmaDstHi = b[71]
	s.hdmaDstLo = b[72]
	s.hdmaSrc = binary.LittleEndian.Uint16(b[73:])
	s.hdmaDst = binary.LittleEndian.Uint16(b[75:])
	s.hdmaBlocks = int(binary.LittleEndian.Uint16(b[77:]))
	s.hdmaActive = b[79]&1 != 0
	s.hdmaCancelled = b[79]&2 != 0
	s.hdmaStallMC = int(binary.LittleEndian.Uint32(b[80:]))
	s.rpReg = b[84]
	copy(s.undocCGB[:], b[85:89])
	off += 128

	s.dot = binary.LittleEndian.Uint64(buf[off:])
	s.frame = binary.LittleEndian.Uint64(buf[off+8:])
	s.frac = int(binary.LittleEndian.Uint32(buf[off+16:]))
	s.rtcAcc = binary.LittleEndian.Uint64(buf[off+24:])
	// The serial log is a diagnostic capture, not machine state;
	// the pending audio buffer is empty at frame boundaries.
	s.audio = nil
	return nil
}
