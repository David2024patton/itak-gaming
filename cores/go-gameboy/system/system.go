// Package system holds the GB-1 Game Boy test rig: a flat 64 KB bus
// wiring the SM83 CPU to a cartridge, work RAM, a simple divider and
// timer, and a serial port capture.
//
// This is deliberately NOT the final machine. It exists so the CPU can
// be proven against the public conformance ROMs (Blargg cpu_instrs
// and mooneye-gb acceptance) before the PPU and APU exist. Stubbed
// regions are honest stubs, documented where they live: video RAM and
// OAM are plain storage, LY always reads 0, and the timer counts with
// the right periods but without the falling-edge quirks the real
// divider circuit has. GB-2 replaces this rig with the timed system.
package system

import (
	"errors"

	"github.com/davidpatton/itak-gaming/cores/go-gameboy/cart"
	"github.com/davidpatton/itak-gaming/cores/go-gameboy/chips/sm83"
)

// IO register addresses used by the rig.
const (
	regSB   = 0xFF01 // serial data
	regSC   = 0xFF02 // serial control
	regDIV  = 0xFF04 // divider
	regTIMA = 0xFF05 // timer counter
	regTMA  = 0xFF06 // timer modulo
	regTAC  = 0xFF07 // timer control
	regIF   = 0xFF0F // interrupt flags
	regLY   = 0xFF44 // LCD Y (stub: always 0 in GB-1)
	regDMA  = 0xFF46 // OAM DMA trigger
	regIE   = 0xFFFF // interrupt enable
)

// timerPeriods maps the TAC clock select to the TIMA increment
// period in T-cycles: 1024, 16, 64, 256.
var timerPeriods = [4]int{1024, 16, 64, 256}

// System is the GB-1 test rig.
type System struct {
	CPU  *sm83.CPU
	Cart *cart.Cart

	vram [0x2000]byte
	wram [0x2000]byte
	oam  [0xA0]byte
	hram [0x7F]byte
	io   [0x80]byte // 0xFF00-0xFF7F stored values (few are live)
	ie   uint8

	divider  int // T-cycle counter behind DIV (reads as divider >> 8)
	timaAcc  int // T-cycles accumulated toward the next TIMA tick
	serial   []byte
	cyclesTC int64 // total T-cycles run

	// LYStub is the value LY reads return. The default 0 matches a
	// parked beam. The mooneye harness sets it to 0xFF, the value
	// the suite's own documentation prescribes for headless runs:
	// test frameworks see a beam past the last line and skip their
	// VBlank wait and drawing setup entirely.
	LYStub uint8
}

// New builds a rig around a cartridge image: the cartridge is parsed,
// the CPU is reset to the DMG post-boot state (the values the boot
// ROM leaves behind at hand-off to 0x0100), and the divider starts at
// its post-boot phase.
func New(image []byte) (*System, error) {
	ct, err := cart.Load(image)
	if err != nil {
		return nil, err
	}
	if ct.Kind == cart.MBC2 || ct.Kind == cart.MBCUnsupported {
		return nil, errors.New("system: mapper not implemented in the GB-1 rig")
	}
	s := &System{CPU: sm83.New(), Cart: ct}
	s.Reset()
	return s, nil
}

// Reset restores the rig to the DMG post-boot state.
func (s *System) Reset() {
	s.CPU.Reset()
	s.CPU.SetRegs(sm83.Registers{
		A: 0x01, F: 0xB0, B: 0x00, C: 0x13,
		D: 0x00, E: 0xD8, H: 0x01, L: 0x4D,
		SP: 0xFFFE, PC: 0x0100,
	})
	for i := range s.wram {
		s.wram[i] = 0
	}
	for i := range s.io {
		s.io[i] = 0
	}
	s.ie = 0
	// DMG hand-off: the divider's high byte reads 0xAB at 0x0100.
	s.divider = 0xAB << 8
	s.timaAcc = 0
	s.serial = s.serial[:0]
	s.cyclesTC = 0
}

// Serial returns everything the ROM has written to the serial port,
// captured the way a link cable logger would see it.
func (s *System) Serial() string { return string(s.serial) }

// Step runs the CPU for one step (one instruction, one dispatch, or
// one idle cycle) and advances the timer by the elapsed T-cycles.
func (s *System) Step() int {
	m := s.CPU.Step(s)
	s.tick(m * 4)
	return m
}

// RunMCycles runs until at least n M-cycles have elapsed.
func (s *System) RunMCycles(n int) {
	for n > 0 {
		n -= s.Step()
	}
}

// tick advances the divider and timer by t T-cycles.
func (s *System) tick(t int) {
	s.cyclesTC += int64(t)
	s.divider += t
	tac := s.io[regTAC-0xFF00]
	if tac&0x04 == 0 {
		return // timer disabled
	}
	s.timaAcc += t
	period := timerPeriods[tac&0x03]
	for s.timaAcc >= period {
		s.timaAcc -= period
		tima := s.io[regTIMA-0xFF00] + 1
		if tima == 0 {
			tima = s.io[regTMA-0xFF00]
			s.io[regIF-0xFF00] |= 0x04 // timer interrupt request
		}
		s.io[regTIMA-0xFF00] = tima
	}
}

// Read implements the bus map for the CPU.
func (s *System) Read(addr uint16) uint8 {
	switch {
	case addr < 0x8000:
		return s.Cart.ReadROM(addr)
	case addr < 0xA000:
		return s.vram[addr-0x8000]
	case addr < 0xC000:
		return s.Cart.ReadRAM(addr)
	case addr < 0xE000:
		return s.wram[addr-0xC000]
	case addr < 0xFE00:
		return s.wram[addr-0xE000] // echo RAM mirrors 0xC000-0xDDFF
	case addr < 0xFEA0:
		return s.oam[addr-0xFE00]
	case addr < 0xFF00:
		return 0xFF // unusable area
	case addr == regLY:
		return s.LYStub // stub: no PPU in GB-1
	case addr == regDIV:
		return uint8(s.divider >> 8)
	case addr == regIF:
		return s.io[regIF-0xFF00] | 0xE0 // unused bits read as 1
	case addr == regIE:
		return s.ie
	case addr >= 0xFF80:
		return s.hram[addr-0xFF80]
	default:
		return s.io[addr-0xFF00]
	}
}

// Write implements the bus map for the CPU.
func (s *System) Write(addr uint16, val uint8) {
	switch {
	case addr < 0x8000:
		s.Cart.Write(addr, val)
	case addr < 0xA000:
		s.vram[addr-0x8000] = val
	case addr < 0xC000:
		s.Cart.Write(addr, val)
	case addr < 0xE000:
		s.wram[addr-0xC000] = val
	case addr < 0xFE00:
		s.wram[addr-0xE000] = val
	case addr < 0xFEA0:
		s.oam[addr-0xFE00] = val
	case addr < 0xFF00:
		// unusable area: writes are ignored
	case addr == regSC:
		s.io[regSC-0xFF00] = val
		if val&0x81 == 0x81 {
			// Internal clock transfer: the byte leaves on the
			// (imaginary) link cable immediately and the transfer
			// bit clears, which is what polling ROM code waits for.
			s.serial = append(s.serial, s.io[regSB-0xFF00])
			s.io[regSC-0xFF00] = val &^ 0x80
		}
	case addr == regDIV:
		s.divider = 0
		s.timaAcc = 0
	case addr == regDMA:
		s.io[regDMA-0xFF00] = val
		src := uint16(val) << 8
		for i := 0; i < len(s.oam); i++ {
			s.oam[i] = s.Read(src + uint16(i))
		}
	case addr == regIE:
		s.ie = val
	case addr >= 0xFF80:
		s.hram[addr-0xFF80] = val
	default:
		s.io[addr-0xFF00] = val
	}
}
