// Package system wires the Dreamcast parts into a running machine
// for DC-1: the SH-4 CPU from chips/sh4, the physical bus, and a
// GD-ROM image from the gd package.
//
// Timing model for this slice is deliberately flat: the CPU charges
// one cycle per instruction and a frame is a fixed budget of
// FrameCycles (200 MHz divided by 60). The framebuffer stays blank
// (all zero) until the PowerVR2 slice (DC-2) renders into it, and
// the pad state is stored but not yet delivered to a game: the
// Maple bus that carries it arrives in DC-3.
package system

import (
	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/bus"
	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/chips/sh4"
	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/gd"
)

// Display geometry and the flat frame budget.
const (
	Width       = 640
	Height      = 480
	FrameCycles = 3333333 // 200 MHz / 60, slice 1 flat model
)

// Pad button bits for PadState.Mask, in the order documented at
// the top of this file's pad section: face buttons first, then
// Start, then the digital cross, then the digital triggers.
const (
	BtnA     uint16 = 1 << 0
	BtnB     uint16 = 1 << 1
	BtnX     uint16 = 1 << 2
	BtnY     uint16 = 1 << 3
	BtnStart uint16 = 1 << 4
	BtnUp    uint16 = 1 << 5
	BtnDown  uint16 = 1 << 6
	BtnLeft  uint16 = 1 << 7
	BtnRight uint16 = 1 << 8
	BtnLT    uint16 = 1 << 9  // left trigger, digital bit
	BtnRT    uint16 = 1 << 10 // right trigger, digital bit
)

// PadState is one Dreamcast controller report: a button mask, the
// two analog triggers (0 to 255) and the analog stick, where 128
// is centred on each axis.
type PadState struct {
	Mask     uint16
	TriggerL uint8
	TriggerR uint8
	JoyX     uint8
	JoyY     uint8
}

// stateVersion is byte 0 of a serialized machine state.
const stateVersion = 0x01

// Machine is one powered-on Dreamcast.
type Machine struct {
	CPU  *sh4.CPU
	Bus  *bus.Bus
	Disc *gd.Image

	framebuffer []uint32
	pad         PadState
	discPath    string
}

// New builds a machine (bus plus CPU) and resets the CPU.
func New() *Machine {
	b := bus.New()
	m := &Machine{
		Bus:         b,
		CPU:         sh4.New(b),
		framebuffer: make([]uint32, Width*Height),
		pad:         PadState{JoyX: 128, JoyY: 128},
	}
	m.CPU.Reset()
	return m
}

// LoadDisc opens a GD-ROM image (.gdi, .iso or .bin) and holds it
// as the machine's disc. The open error, if any, propagates to the
// caller and any previously loaded disc stays in place.
func (m *Machine) LoadDisc(path string) error {
	img, err := gd.Open(path)
	if err != nil {
		return err
	}
	if m.Disc != nil {
		_ = m.Disc.Close()
	}
	m.Disc = img
	m.discPath = path
	return nil
}

// DiscPath returns the path the current disc was loaded from, or
// the empty string when no disc is loaded.
func (m *Machine) DiscPath() string { return m.discPath }

// LoadProgram is a test and tooling helper: it writes code bytes
// into the bus at the physical address pa and points the CPU at
// the matching P1 cached alias, so a program placed in main RAM
// (pa 0x0C000000) starts executing at virtual 0x8C000000.
func (m *Machine) LoadProgram(pa uint32, code []byte) {
	for i, c := range code {
		m.Bus.Write8(pa+uint32(i), c)
	}
	m.CPU.PC = 0x80000000 | pa
}

// SetPC sets the CPU program counter to the virtual address va.
func (m *Machine) SetPC(va uint32) { m.CPU.PC = va }

// RunFrame runs one frame of CPU time under the flat cycle model.
func (m *Machine) RunFrame() { m.CPU.Run(FrameCycles) }

// Framebuffer returns the 640 by 480 XRGB8888 frame. This slice
// never draws, so the buffer is blank (all zero); DC-2 renders
// into it. The returned slice is the machine's own buffer.
func (m *Machine) Framebuffer() []uint32 { return m.framebuffer }

// SetButtons records the current controller state. DC-3 delivers
// it to games over the Maple bus; this slice only stores it.
func (m *Machine) SetButtons(p PadState) { m.pad = p }

// Buttons returns the last controller state set with SetButtons.
func (m *Machine) Buttons() PadState { return m.pad }

// StateSize is the exact byte size of a serialized machine state:
// one version byte, the CPU state, then the RAM, VRAM, flash and
// AICA RAM stores in that order, each at its live length.
func (m *Machine) StateSize() int {
	return 1 + sh4.SerializeSize +
		len(m.Bus.RAM) + len(m.Bus.VRAM) + len(m.Bus.Flash) + len(m.Bus.AICARAM)
}

// Serialize captures the whole machine state. Layout:
//
//	byte 0        version (0x01)
//	bytes 1..     CPU state (sh4.SerializeSize bytes)
//	then          main RAM   (len(Bus.RAM) bytes)
//	then          VRAM       (len(Bus.VRAM) bytes)
//	then          flash      (len(Bus.Flash) bytes)
//	then          AICA RAM   (len(Bus.AICARAM) bytes)
//
// The register stub storage is not part of the state this slice:
// stub registers have no behaviour yet, so nothing a game could
// observe through them is lost.
func (m *Machine) Serialize() []byte {
	out := make([]byte, m.StateSize())
	out[0] = stateVersion
	off := 1
	copy(out[off:], m.CPU.Serialize())
	off += sh4.SerializeSize
	copy(out[off:], m.Bus.RAM)
	off += len(m.Bus.RAM)
	copy(out[off:], m.Bus.VRAM)
	off += len(m.Bus.VRAM)
	copy(out[off:], m.Bus.Flash)
	off += len(m.Bus.Flash)
	copy(out[off:], m.Bus.AICARAM)
	return out
}

// Deserialize restores a state produced by Serialize. It returns
// false, leaving the live machine alone as far as the checks can
// guarantee, when the length is not exactly StateSize, the
// version byte differs, or the CPU rejects its block.
func (m *Machine) Deserialize(b []byte) bool {
	if len(b) != m.StateSize() || b[0] != stateVersion {
		return false
	}
	off := 1
	if !m.CPU.Deserialize(b[off : off+sh4.SerializeSize]) {
		return false
	}
	off += sh4.SerializeSize
	copy(m.Bus.RAM, b[off:])
	off += len(m.Bus.RAM)
	copy(m.Bus.VRAM, b[off:])
	off += len(m.Bus.VRAM)
	copy(m.Bus.Flash, b[off:])
	off += len(m.Bus.Flash)
	copy(m.Bus.AICARAM, b[off:])
	return true
}
