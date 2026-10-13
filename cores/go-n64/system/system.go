// Package system wires the VR4300 CPU, the physical memory
// bus, and a parsed cartridge into the machine the libretro
// shell (a later slice) will drive. Slice N64-1 runs the CPU
// only: there is no RSP, RDP, video, or audio output yet, so
// the framebuffer is blank and games cannot boot (boot needs
// the PIF/CIC path, which lands in N64-4). Bare code loaded
// directly into memory runs today, which is what the test
// harness uses.
package system

import (
	"errors"
	"fmt"

	"github.com/davidpatton/itak-gaming/cores/go-n64/bus"
	"github.com/davidpatton/itak-gaming/cores/go-n64/cart"
	"github.com/davidpatton/itak-gaming/cores/go-n64/chips/r4300"
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
	Mem  *bus.Memory
	Cart *cart.Cart

	expansion bool
	buttons   uint16
	fb        []uint32
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

// RunCycles executes the CPU for at least the given number
// of flat cycles.
func (s *System) RunCycles(cycles int) {
	n := 0
	for n < cycles {
		n += s.CPU.Step()
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

const stateVersion = 1

// SerializeSize reports the byte size of a serialized
// system for this machine's RDRAM configuration.
func (s *System) SerializeSize() int {
	return 2 + r4300.SerializeSize + len(s.Mem.RDRAM)
}

// Serialize writes the full machine state: version byte,
// option flags, the CPU block, and RDRAM.
func (s *System) Serialize() []byte {
	out := make([]byte, s.SerializeSize())
	out[0] = stateVersion
	if s.expansion {
		out[1] = 1
	}
	n := s.CPU.Serialize(out[2:])
	copy(out[2+n:], s.Mem.RDRAM)
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
	copy(s.Mem.RDRAM, in[2+r4300.SerializeSize:])
	s.CPU.SetInterruptLine(s.Mem.MIInterrupt())
	return nil
}
