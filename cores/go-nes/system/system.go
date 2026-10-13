// Package system assembles the NES machine: the 6502 CPU, the
// cartridge, and the address space around them.
//
// In NES-1 this is a CPU test rig, not the real machine: the PPU
// register block is a stub whose $2002 status register reports a
// vblank flag that toggles on a frame cadence (341 by 262 PPU
// cycles per frame, three PPU cycles per CPU cycle, vblank from
// scanline 241 to 261), the APU registers are inert, and the
// controller ports are simple shift registers fed by SetButtons.
// That is exactly enough for the CPU conformance ROMs, most of
// which poll $2002 while they run their instruction tests and
// report through cartridge RAM. ROMs that need real PPU behavior
// (timing measured in dots, sprite evaluation, rendering) belong
// to NES-2, when this package grows the real PPU in the
// go-genesis shape; the bus below is laid out so the PPU and DMA
// drop in without moving the CPU or the cartridge.
package system

import (
	"github.com/davidpatton/itak-gaming/cores/go-nes/cart"
	"github.com/davidpatton/itak-gaming/cores/go-nes/chips/cpu6502"
)

// PPU timing constants for the stub's frame cadence.
const (
	ppuCyclesPerFrame = 341 * 262
	ppuVblankStart    = 241 * 341
	ppuVblankEnd      = 261 * 341
)

// System is the assembled machine (NES-1: the test rig).
type System struct {
	cpu  *cpu6502.CPU
	cart *cart.Cart

	ram     [2048]uint8
	openBus uint8 // the last byte seen on the data bus

	// PPU stub state.
	ppuCtrl     uint8 // $2000 as written; only the NMI enable bit is used
	vblSuppress int64 // frame index whose vblank a $2002 read has cleared

	// Controller stub state: button latches and shift registers.
	buttons [2]uint8
	shift   [2]uint8
	strobe  bool

	oam [256]uint8 // OAM DMA destination stub
}

// New builds a machine around a cartridge image and runs the
// reset sequence.
func New(image []byte) (*System, error) {
	c, err := cart.New(image)
	if err != nil {
		return nil, err
	}
	s := &System{cart: c, vblSuppress: -1}
	s.cpu = cpu6502.New()
	s.cpu.Attach(s)
	s.cpu.Reset()
	return s, nil
}

// CPU exposes the processor for the conformance harness.
func (s *System) CPU() *cpu6502.CPU { return s.cpu }

// Cart exposes the cartridge.
func (s *System) Cart() *cart.Cart { return s.cart }

// Cycles returns total CPU cycles since power-on.
func (s *System) Cycles() uint64 { return s.cpu.Cycles() }

// Step executes one CPU step and lets the stub devices react
// (vblank edges raise the NMI line when enabled, a pending
// cartridge IRQ drives the IRQ line).
func (s *System) Step() int {
	n := s.cpu.Step()
	s.poll()
	return n
}

// RunCycles executes until at least n more CPU cycles have
// elapsed.
func (s *System) RunCycles(n int) {
	target := s.cpu.Cycles() + uint64(n)
	for s.cpu.Cycles() < target {
		s.Step()
	}
}

func (s *System) poll() {
	s.cpu.SetNMILine(s.vblankActive() && s.ppuCtrl&0x80 != 0)
	s.cpu.SetIRQLine(s.cart.IRQPending())
}

// frameIndex and vblankActive describe the stub's frame cadence
// at the current cycle.
func (s *System) framePhase() int64 {
	return int64(s.cpu.Cycles()*3) % ppuCyclesPerFrame
}

func (s *System) vblankRaw() bool {
	ph := s.framePhase()
	return ph >= ppuVblankStart && ph < ppuVblankEnd
}

// vblankActive is the $2002 vblank flag as software observes it:
// set by the cadence, cleared for the rest of the frame window
// once a $2002 read has consumed it.
func (s *System) vblankActive() bool {
	if !s.vblankRaw() {
		return false
	}
	frame := int64(s.cpu.Cycles()*3) / ppuCyclesPerFrame
	return frame != s.vblSuppress
}

func (s *System) readStatus() uint8 {
	var v uint8
	if s.vblankActive() {
		v |= 0x80
		s.vblSuppress = int64(s.cpu.Cycles()*3) / ppuCyclesPerFrame
	}
	return v
}

func (s *System) statusByte() uint8 {
	if s.vblankActive() {
		return 0x80
	}
	return 0
}

func (s *System) readController(port int) uint8 {
	if s.strobe {
		return s.buttons[port] & 1
	}
	v := s.shift[port] & 1
	s.shift[port] = (s.shift[port] >> 1) | 0x80
	return v
}

// SetButtons sets a controller's button state, in read order:
// bit 0 A, 1 B, 2 Select, 3 Start, 4 Up, 5 Down, 6 Left, 7 Right.
func (s *System) SetButtons(port int, mask uint8) {
	if port < 0 || port > 1 {
		return
	}
	s.buttons[port] = mask
	if s.strobe {
		s.shift[port] = mask
	}
}

func (s *System) writeStrobe(v uint8) {
	s.strobe = v&1 != 0
	if s.strobe {
		s.shift[0] = s.buttons[0]
		s.shift[1] = s.buttons[1]
	}
}

// oamDMA is the $4014 stub: the page copy itself is real (games
// and test ROMs can read the buffer back only through the CPU on
// hardware, so here it simply lands in the stub's OAM array),
// while the bus seizure is charged to the CPU through its stall
// hook at the documented 513 or 514 cycles depending on the
// parity of the cycle the write landed on.
func (s *System) oamDMA(page uint8) {
	base := uint16(page) << 8
	for i := 0; i < 256; i++ {
		s.oam[i] = s.Peek(base + uint16(i))
	}
	stall := 513
	if s.cpu.Cycles()&1 != 0 {
		stall = 514
	}
	s.cpu.Stall(stall)
}

// Read is the CPU bus read. Test code that needs side-effect-free
// inspection uses Peek.
func (s *System) Read(addr uint16) uint8 {
	var v uint8
	switch {
	case addr < 0x2000:
		v = s.ram[addr&0x07FF]
	case addr < 0x4000:
		if addr&7 == 2 {
			v = s.readStatus()
		} else {
			v = s.openBus
		}
	case addr == 0x4016:
		v = s.readController(0)
	case addr == 0x4017:
		v = s.readController(1)
	case addr < 0x6000:
		// APU registers (inert in the rig), $4014, and the
		// $4020-$5FFF expansion area read as open bus.
		v = s.openBus
	default:
		s.cart.SetCycle(int64(s.cpu.Cycles()))
		v = s.cart.ReadPRG(addr)
	}
	s.openBus = v
	return v
}

// Write is the CPU bus write.
func (s *System) Write(addr uint16, val uint8) {
	s.openBus = val
	switch {
	case addr < 0x2000:
		s.ram[addr&0x07FF] = val
	case addr < 0x4000:
		if addr&7 == 0 {
			s.ppuCtrl = val
		}
		// Other PPU writes are accepted and ignored by the stub.
	case addr == 0x4014:
		s.oamDMA(val)
	case addr == 0x4016:
		s.writeStrobe(val)
	case addr < 0x4020:
		// APU writes and the $4017 frame counter write are
		// accepted and ignored by the stub.
	case addr < 0x6000:
		// Expansion writes are ignored by the rig.
	default:
		s.cart.SetCycle(int64(s.cpu.Cycles()))
		s.cart.WritePRG(addr, val)
	}
}

// Peek reads an address without side effects: $2002 reports its
// flag without clearing it, and the controller shift registers do
// not advance. Cartridge reads are side-effect free in this
// core's mappers, so they pass through.
func (s *System) Peek(addr uint16) uint8 {
	switch {
	case addr < 0x2000:
		return s.ram[addr&0x07FF]
	case addr < 0x4000:
		if addr&7 == 2 {
			return s.statusByte()
		}
		return s.openBus
	case addr < 0x6000:
		return s.openBus
	default:
		return s.cart.ReadPRG(addr)
	}
}

// BlarggResult inspects the standard Blargg test protocol in
// cartridge RAM: while a test runs, $6000 holds $80; when it
// finishes, $6000 holds the result code (0 for pass) with the
// signature $DE $B0 $61 at $6001-$6003 and a NUL-terminated
// report text at $6004. ok reports whether a finished result is
// present; passed and text are meaningful only then.
func (s *System) BlarggResult() (ok bool, passed bool, text string) {
	if s.Peek(0x6001) != 0xDE || s.Peek(0x6002) != 0xB0 || s.Peek(0x6003) != 0x61 {
		return false, false, ""
	}
	status := s.Peek(0x6000)
	if status == 0x80 {
		return false, false, ""
	}
	var buf []byte
	for addr := uint16(0x6004); addr < 0x8000; addr++ {
		b := s.Peek(addr)
		if b == 0 {
			break
		}
		buf = append(buf, b)
	}
	return true, status == 0, string(buf)
}
