// Package system assembles the NES machine: the 6502 CPU, the
// dot-clocked PPU, the cartridge, and the address space around
// them.
//
// Timing: the CPU runs cycle by cycle, and every cycle moves
// the PPU three dots, interleaved with the access itself
// (first dot, interrupt line sample, second dot, the access,
// third dot), which is the alignment the hardware's
// documented races (the $2002 read on the vblank dot, the
// $2000 NMI races) are measured against.
// OAM DMA seizes the bus through the CPU's stall hook while its
// dummy reads keep the PPU running, and the cartridge's MMC3
// IRQ is clocked by the PPU's real A12 edges, not a counter.
//
// The APU register block is inert in this slice (NES-3 grows
// the audio): writes are accepted, reads return open bus.
package system

import (
	"encoding/binary"
	"errors"

	"github.com/davidpatton/itak-gaming/cores/go-nes/cart"
	"github.com/davidpatton/itak-gaming/cores/go-nes/chips/cpu6502"
	"github.com/davidpatton/itak-gaming/cores/go-nes/ppu"
)

// Width and Height mirror the PPU's framebuffer dimensions.
const (
	Width  = ppu.Width
	Height = ppu.Height
)

const serializeVersion = 1

// stateSize is the whole-machine save state size: version
// byte, the CPU state, a length-prefixed cartridge state, the
// PPU state, RAM, and the controller and bus latches. The
// cartridge state is variable-length (PRG RAM size differs per
// game), hence the length prefix.
const stateFixedTail = 2048 + 6 // RAM, open bus, two button latches, two shift registers, strobe

// System is the assembled machine.
type System struct {
	cpu  *cpu6502.CPU
	cart *cart.Cart
	ppu  *ppu.PPU

	ram     [2048]uint8
	openBus uint8 // the last byte driven on the CPU data bus

	// Controller ports: button latches and shift registers,
	// one shared strobe.
	buttons [2]uint8
	shift   [2]uint8
	strobe  bool
}

// New builds a machine around a cartridge image and runs the
// reset sequence.
func New(image []byte) (*System, error) {
	c, err := cart.New(image)
	if err != nil {
		return nil, err
	}
	s := &System{cart: c}
	s.ppu = ppu.New(c)
	s.cpu = cpu6502.New()
	s.cpu.Attach(s)
	s.cpu.Reset()
	return s, nil
}

// CPU exposes the processor for the conformance harness.
func (s *System) CPU() *cpu6502.CPU { return s.cpu }

// Cart exposes the cartridge.
func (s *System) Cart() *cart.Cart { return s.cart }

// PPU exposes the picture unit for debuggers and tests.
func (s *System) PPU() *ppu.PPU { return s.ppu }

// Cycles returns total CPU cycles since power-on.
func (s *System) Cycles() uint64 { return s.cpu.Cycles() }

// Frames returns how many frames the PPU has completed.
func (s *System) Frames() uint64 { return s.ppu.Frames() }

// Framebuffer returns the PPU's current frame, XRGB8888,
// 256 by 240.
func (s *System) Framebuffer() []uint32 { return s.ppu.Framebuffer() }

// Step executes one CPU step (an instruction, an interrupt
// sequence, or a stall cycle).
func (s *System) Step() int { return s.cpu.Step() }

// RunCycles executes until at least n more CPU cycles have
// elapsed.
func (s *System) RunCycles(n int) {
	target := s.cpu.Cycles() + uint64(n)
	for s.cpu.Cycles() < target {
		s.Step()
	}
}

// RunFrame executes until the PPU completes the current frame
// (the next vblank begins), then returns. This is the
// frontend's frame loop.
func (s *System) RunFrame() {
	target := s.ppu.Frames() + 1
	for s.ppu.Frames() < target {
		s.Step()
	}
}

// syncLines feeds the interrupt pins from the machine's
// current state: the NMI line is the PPU's derived output
// (vblank flag AND enable) and the IRQ line is the
// cartridge's. It runs once per bus access, after the access
// itself, so the CPU's poll samples at the following accesses
// see each pulse that an access boundary falls inside, and
// miss the ones that live and die between two boundaries
// (the $2002 suppression case). No access forces an edge by
// hand.
func (s *System) syncLines() {
	s.cpu.SetNMILine(s.ppu.NMIOutput())
	s.cpu.SetIRQLine(s.cart.IRQPending())
}

// Read is the CPU bus read. The PPU runs two of the cycle's
// dots, the access captures against that state, the
// interrupt lines sync on the result, and the third dot
// runs. That split is the phase the hardware's $2002 and
// $2000 race behavior pins down.
func (s *System) Read(addr uint16) uint8 {
	s.ppu.Tick(2)
	v := s.readAt(addr)
	s.openBus = v
	s.syncLines()
	s.ppu.Tick(1)
	return v
}

// readAt dispatches a read.
func (s *System) readAt(addr uint16) uint8 {
	switch {
	case addr < 0x2000:
		return s.ram[addr&0x07FF]
	case addr < 0x4000:
		return s.ppu.ReadReg(int(addr & 7))
	case addr == 0x4016:
		return s.readController(0)
	case addr == 0x4017:
		return s.readController(1)
	case addr < 0x6000:
		// APU registers (inert until NES-3), the $4014
		// write-only register, and the $4020-$5FFF
		// expansion area read as open bus.
		return s.openBus
	default:
		s.cart.SetCycle(int64(s.cpu.Cycles()))
		return s.cart.ReadPRG(addr)
	}
}

// Write is the CPU bus write, with the same two-dots-in
// split as Read.
func (s *System) Write(addr uint16, val uint8) {
	s.ppu.Tick(2)
	s.openBus = val
	s.writeAt(addr, val)
	s.syncLines()
	s.ppu.Tick(1)
}

// writeAt dispatches a write.
func (s *System) writeAt(addr uint16, val uint8) {
	switch {
	case addr < 0x2000:
		s.ram[addr&0x07FF] = val
	case addr < 0x4000:
		s.ppu.WriteReg(int(addr&7), val)
	case addr == 0x4014:
		s.oamDMA(val)
	case addr == 0x4016:
		s.writeStrobe(val)
	case addr < 0x4020:
		// APU writes and the $4017 frame counter write are
		// accepted and ignored until NES-3.
	case addr < 0x6000:
		// Expansion writes are ignored.
	default:
		s.cart.SetCycle(int64(s.cpu.Cycles()))
		s.cart.WritePRG(addr, val)
	}
}

// Peek reads an address without side effects and without
// advancing time: $2002 reports its flags without clearing
// them, the controller shift registers do not advance, and
// cartridge reads are side-effect free in this core's mappers,
// so they pass through.
func (s *System) Peek(addr uint16) uint8 {
	switch {
	case addr < 0x2000:
		return s.ram[addr&0x07FF]
	case addr < 0x4000:
		return s.ppu.PeekReg(int(addr & 7))
	case addr < 0x6000:
		return s.openBus
	default:
		return s.cart.ReadPRG(addr)
	}
}

// --- Controllers ---

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

// --- OAM DMA ---

// oamDMA runs the $4014 DMA: 256 bytes copied from the given
// CPU page into PPU OAM through the PPU's DMA port (starting
// at the current OAM address, wrapping), while the CPU stalls
// for the documented 513 cycles, plus one when the write
// landed on an odd CPU cycle. The stall burns as dummy reads
// through the bus, so the PPU keeps running through the copy.
func (s *System) oamDMA(page uint8) {
	base := uint16(page) << 8
	for i := 0; i < 256; i++ {
		s.ppu.DMAWrite(s.Peek(base + uint16(i)))
	}
	stall := 513
	if s.cpu.Cycles()&1 != 0 {
		stall = 514
	}
	s.cpu.Stall(stall)
}

// --- Save states ---

// Serialize writes the whole-machine state: version byte, CPU
// state, length-prefixed cartridge state, PPU state, RAM, and
// the controller and bus latches.
func (s *System) Serialize() []byte {
	cartState := s.cart.Serialize()
	out := make([]byte, 0, 1+cpu6502.SerializeSize+2+len(cartState)+ppu.SerializeSize+stateFixedTail)
	out = append(out, serializeVersion)
	out = append(out, s.cpu.Serialize()...)
	var lb [2]byte
	binary.LittleEndian.PutUint16(lb[:], uint16(len(cartState)))
	out = append(out, lb[:]...)
	out = append(out, cartState...)
	out = append(out, s.ppu.Serialize()...)
	out = append(out, s.ram[:]...)
	out = append(out, s.openBus, s.buttons[0], s.buttons[1], s.shift[0])
	out = append(out, s.shift[1])
	if s.strobe {
		out = append(out, 1)
	} else {
		out = append(out, 0)
	}
	return out
}

// StateSize returns the size Serialize will produce for the
// loaded cartridge.
func (s *System) StateSize() int {
	return 1 + cpu6502.SerializeSize + 2 + len(s.cart.Serialize()) + ppu.SerializeSize + stateFixedTail
}

// Deserialize restores a state produced by Serialize for the
// same cartridge.
func (s *System) Deserialize(in []byte) error {
	if len(in) < 1+cpu6502.SerializeSize+2 {
		return errors.New("system: state too small")
	}
	if in[0] != serializeVersion {
		return errors.New("system: unsupported state version")
	}
	off := 1
	if err := s.cpu.Deserialize(in[off : off+cpu6502.SerializeSize]); err != nil {
		return err
	}
	off += cpu6502.SerializeSize
	cl := int(binary.LittleEndian.Uint16(in[off:]))
	off += 2
	if len(in) != off+cl+ppu.SerializeSize+stateFixedTail {
		return errors.New("system: state size mismatch")
	}
	if err := s.cart.Deserialize(in[off : off+cl]); err != nil {
		return err
	}
	off += cl
	if err := s.ppu.Deserialize(in[off : off+ppu.SerializeSize]); err != nil {
		return err
	}
	off += ppu.SerializeSize
	copy(s.ram[:], in[off:off+2048])
	off += 2048
	s.openBus = in[off]
	s.buttons[0], s.buttons[1] = in[off+1], in[off+2]
	s.shift[0], s.shift[1] = in[off+3], in[off+4]
	s.strobe = in[off+5] != 0
	return nil
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
