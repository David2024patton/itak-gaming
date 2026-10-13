// Package system wires the Dreamcast parts into a running machine:
// the SH-4 CPU from chips/sh4, the physical bus, a GD-ROM image
// from the gd package, the Holly interrupt controller, and the
// PowerVR2 from the pvr package (DC-2).
//
// Timing model: the CPU charges one cycle per instruction and a
// frame is a fixed budget of FrameCycles (200 MHz divided by 60).
// RunFrame advances the CPU in scanline-sized chunks and steps
// the SPG between chunks, so vblank and hblank interrupts are
// delivered at line accuracy inside the frame. The framebuffer
// is the PVR display output (FB_R_SOF1 presented per FB_R_CTRL),
// composed at the end of each RunFrame. The pad state is stored
// but not yet delivered to a game: the Maple bus that carries it
// arrives in DC-3.
package system

import (
	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/bus"
	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/chips/sh4"
	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/gd"
	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/holly"
	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/pvr"
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

// stateVersion is byte 0 of a serialized machine state. Version
// 2 (DC-2) appends the Holly controller state and the variable
// length PVR state after the DC-1 sections.
const stateVersion = 0x02

// Machine is one powered-on Dreamcast.
type Machine struct {
	CPU   *sh4.CPU
	Bus   *bus.Bus
	Disc  *gd.Image
	PVR   *pvr.PVR
	Holly *holly.Controller

	framebuffer []uint32
	pad         PadState
	discPath    string
}

// New builds a machine (bus plus CPU) and resets the CPU. The
// Holly controller presents interrupts to the CPU; the PVR owns
// its register window, its two FIFO ports, and the shared VRAM.
func New() *Machine {
	b := bus.New()
	cpu := sh4.New(b)
	h := holly.New(cpu)
	p := pvr.New(b.VRAM, h)
	b.Attach(0x005F6900, 0x005F6940, h, false)
	b.Attach(0x005F8000, 0x005FA000, p, false)
	b.Attach(0x00500000, 0x00600000, p.TAPort(), true)
	b.Attach(0x00600000, 0x00601000, p.YUVPort(), true)
	m := &Machine{
		Bus:         b,
		CPU:         cpu,
		PVR:         p,
		Holly:       h,
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

// RunFrame runs one frame of CPU time. The CPU advances in
// scanline-sized chunks with the SPG stepped between chunks, so
// SPG interrupts pend and are accepted inside the frame at line
// accuracy. At the end of the frame the PVR display output is
// composed into the framebuffer.
func (m *Machine) RunFrame() {
	remaining := FrameCycles
	for remaining > 0 {
		chunk := m.PVR.CyclesPerLine()
		if chunk <= 0 || chunk > remaining {
			chunk = remaining
		}
		m.CPU.Run(chunk)
		m.PVR.Advance(chunk)
		remaining -= chunk
	}
	m.PVR.Present(m.framebuffer, Width, Height)
}

// Framebuffer returns the 640 by 480 XRGB8888 frame, composed
// from the PVR display framebuffer at the end of RunFrame. Before
// the first RunFrame it is all zero. The returned slice is the
// machine's own buffer.
func (m *Machine) Framebuffer() []uint32 { return m.framebuffer }

// SetButtons records the current controller state. DC-3 delivers
// it to games over the Maple bus; this slice only stores it.
func (m *Machine) SetButtons(p PadState) { m.pad = p }

// Buttons returns the last controller state set with SetButtons.
func (m *Machine) Buttons() PadState { return m.pad }

// fixedStateSize is the byte size of every section of a machine
// state except the trailing PVR section, whose length depends on
// the TA scene it holds.
func (m *Machine) fixedStateSize() int {
	return 1 + sh4.SerializeSize +
		len(m.Bus.RAM) + len(m.Bus.VRAM) + len(m.Bus.Flash) + len(m.Bus.AICARAM) +
		holly.SerializeSize
}

// StateSize is the exact byte size of a serialized machine state
// for the machine's current state.
func (m *Machine) StateSize() int {
	return m.fixedStateSize() + m.PVR.SerializeSize()
}

// Serialize captures the whole machine state. Layout:
//
//	byte 0        version (0x02)
//	bytes 1..     CPU state (sh4.SerializeSize bytes)
//	then          main RAM   (len(Bus.RAM) bytes)
//	then          VRAM       (len(Bus.VRAM) bytes)
//	then          flash      (len(Bus.Flash) bytes)
//	then          AICA RAM   (len(Bus.AICARAM) bytes)
//	then          Holly state (holly.SerializeSize bytes)
//	then          PVR state (variable length, see pvr.Serialize)
//
// The register stub storage of the remaining stub ranges is not
// part of the state: those registers have no behaviour yet, so
// nothing a game could observe through them is lost.
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
	off += len(m.Bus.AICARAM)
	copy(out[off:], m.Holly.Serialize())
	off += holly.SerializeSize
	copy(out[off:], m.PVR.Serialize())
	return out
}

// Deserialize restores a state produced by Serialize. It returns
// false, leaving the live machine alone as far as the checks can
// guarantee, when the length does not match the sections the
// state itself describes, the version byte differs, or a section
// rejects its block.
func (m *Machine) Deserialize(b []byte) bool {
	fixed := m.fixedStateSize()
	if len(b) < fixed || b[0] != stateVersion {
		return false
	}
	tail := b[fixed:]
	if pvr.SerializedSize(tail) != len(tail) {
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
	off += len(m.Bus.AICARAM)
	if !m.Holly.Deserialize(b[off : off+holly.SerializeSize]) {
		return false
	}
	off += holly.SerializeSize
	return m.PVR.Deserialize(b[off:])
}
