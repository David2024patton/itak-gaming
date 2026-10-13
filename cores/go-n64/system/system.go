// Package system wires the VR4300 CPU, the RSP, the
// physical memory bus, and a parsed cartridge into the
// machine the libretro shell (a later slice) will drive.
// Slice N64-2 adds the RSP: it executes from IMEM under
// the interleave model described on RunCycles. There is
// no RDP, video, or audio output yet, so the framebuffer
// is blank and games cannot boot (boot needs the PIF/CIC
// path, which lands in N64-4). Bare code loaded directly
// into memory runs today, which is what the test harness
// uses.
package system

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/davidpatton/itak-gaming/cores/go-n64/bus"
	"github.com/davidpatton/itak-gaming/cores/go-n64/cart"
	"github.com/davidpatton/itak-gaming/cores/go-n64/chips/r4300"
	"github.com/davidpatton/itak-gaming/cores/go-n64/chips/rsp"
)

// Frame geometry and timing (NTSC): the VR4300 runs at
// 93.75 MHz; a 60 Hz frame budget is 1,562,500 cycles.
const (
	Width          = 320
	Height         = 240
	CyclesPerFrame = 93750000 / 60
)

// Controller button bits in the standard N64 controller
// data layout (the order the PIF reports them).
const (
	ButtonA      = 1 << 15
	ButtonB      = 1 << 14
	ButtonZ      = 1 << 13
	ButtonStart  = 1 << 12
	ButtonDUp    = 1 << 11
	ButtonDDown  = 1 << 10
	ButtonDLeft  = 1 << 9
	ButtonDRight = 1 << 8
	ButtonL      = 1 << 5
	ButtonR      = 1 << 4
	ButtonCUp    = 1 << 3
	ButtonCDown  = 1 << 2
	ButtonCLeft  = 1 << 1
	ButtonCRight = 1 << 0
)

// System is the N64 machine.
type System struct {
	CPU  *r4300.CPU
	RSP  *rsp.RSP
	Mem  *bus.Memory
	Cart *cart.Cart

	expansion bool
	buttons   uint16
	fb        []uint32
	rspFrac   int // leftover RSP budget numerator (of 3 per CPU cycle)
}

// New builds a machine around a raw cartridge dump.
func New(rom []byte) (*System, error) {
	return NewWithOptions(rom, false)
}

// NewWithOptions builds a machine; expansion selects the
// 8 MB Expansion Pak memory size instead of the stock 4 MB.
func NewWithOptions(rom []byte, expansion bool) (*System, error) {
	s := &System{expansion: expansion, fb: make([]uint32, Width*Height)}
	for i := range s.fb {
		s.fb[i] = 0xFF000000 // opaque black
	}
	if err := s.LoadROM(rom); err != nil {
		return nil, err
	}
	return s, nil
}

// LoadROM parses a cartridge dump and resets the machine
// around it.
func (s *System) LoadROM(rom []byte) error {
	c, err := cart.Parse(rom)
	if err != nil {
		return err
	}
	s.Cart = c
	s.Mem = bus.New(c.ROM, s.expansion)
	s.RSP = s.Mem.RSP
	s.CPU = r4300.New(s.Mem)
	s.Mem.OnMIChange = func(active bool) {
		s.CPU.SetInterruptLine(active)
	}
	s.Reset()
	return nil
}

// Reset returns the CPU to its power-on state. Real boot
// (PIF executing the cartridge boot code with the CIC seed)
// lands in N64-4; after Reset the CPU sits at the reset
// vector, and test code is expected to seed PC and memory
// directly.
func (s *System) Reset() {
	s.CPU.Reset()
}

// RunCycles executes the CPU for at least the given
// number of flat cycles, interleaving the RSP. The RSP
// runs at 62.5 MHz against the CPU's 93.75 MHz, a 2:3
// ratio: after every CPU step the RSP is credited 2
// cycles per 3 CPU cycles (the fractional remainder is
// carried exactly) and runs that many instructions,
// stopping early when halted. A CPU write that sets the
// halt flag therefore stops the RSP at the next system
// step boundary.
func (s *System) RunCycles(cycles int) {
	n := 0
	for n < cycles {
		c := s.CPU.Step()
		n += c
		s.rspFrac += 2 * c
		steps := s.rspFrac / 3
		s.rspFrac -= steps * 3
		if steps > 0 {
			s.RSP.Run(steps)
		}
	}
}

// RunFrame executes one 60 Hz frame budget of CPU cycles.
func (s *System) RunFrame() {
	s.RunCycles(CyclesPerFrame)
}

// Framebuffer returns the current video output: a blank
// 320x240 XRGB8888 frame until the RDP/VI slice lands.
func (s *System) Framebuffer() []uint32 { return s.fb }

// SetButtons records the standard controller state. Input
// reaches games through the PIF in a later slice.
func (s *System) SetButtons(mask uint16) { s.buttons = mask }

// Buttons returns the recorded controller state.
func (s *System) Buttons() uint16 { return s.buttons }

// Title returns the cartridge name from its header.
func (s *System) Title() string {
	if s.Cart == nil {
		return ""
	}
	return s.Cart.Name
}

const stateVersion = 2

// SerializeSize reports the byte size of a serialized
// system for this machine's RDRAM configuration.
func (s *System) SerializeSize() int {
	return 2 + r4300.SerializeSize + len(s.Mem.RDRAM) + rsp.SerializeSize + 8 + 8
}

// Serialize writes the full machine state: version byte,
// option flags, the CPU block, RDRAM, the RSP block, the
// MI interrupt and mask registers, and the RSP
// interleave remainder.
func (s *System) Serialize() []byte {
	out := make([]byte, s.SerializeSize())
	out[0] = stateVersion
	if s.expansion {
		out[1] = 1
	}
	n := s.CPU.Serialize(out[2:])
	off := 2 + n
	copy(out[off:], s.Mem.RDRAM)
	off += len(s.Mem.RDRAM)
	off += s.RSP.Serialize(out[off:])
	intr, mask := s.Mem.MIState()
	binary.BigEndian.PutUint32(out[off:], intr)
	binary.BigEndian.PutUint32(out[off+4:], mask)
	off += 8
	binary.BigEndian.PutUint64(out[off:], uint64(int64(s.rspFrac)))
	return out
}

// Deserialize restores a state written by Serialize.
func (s *System) Deserialize(in []byte) error {
	if len(in) != s.SerializeSize() {
		return fmt.Errorf("system: state size %d, want %d", len(in), s.SerializeSize())
	}
	if in[0] != stateVersion {
		return fmt.Errorf("system: state version %d", in[0])
	}
	if (in[1] == 1) != s.expansion {
		return errors.New("system: Expansion Pak configuration mismatch")
	}
	if err := s.CPU.Deserialize(in[2:]); err != nil {
		return err
	}
	off := 2 + r4300.SerializeSize
	copy(s.Mem.RDRAM, in[off:])
	off += len(s.Mem.RDRAM)
	if err := s.RSP.Deserialize(in[off:]); err != nil {
		return err
	}
	off += rsp.SerializeSize
	intr := binary.BigEndian.Uint32(in[off:])
	mask := binary.BigEndian.Uint32(in[off+4:])
	s.Mem.RestoreMI(intr, mask)
	off += 8
	s.rspFrac = int(int64(binary.BigEndian.Uint64(in[off:])))
	s.CPU.SetInterruptLine(s.Mem.MIInterrupt())
	return nil
}
