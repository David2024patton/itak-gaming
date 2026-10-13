// Package system is the Nintendo DS machine skeleton: the two
// CPUs on the dual bus, cartridge staging, the frame budget, and
// save states.
//
// Boot approach (deliberately no BIOS): no DS BIOS or firmware
// image exists in this module and none is required. LoadROM
// stages the cartridge's ARM9 and ARM7 binaries at their header
// load addresses (the direct-boot layout), and Start jumps each
// CPU to its header entry point. Slice 1 leaves the CPUs halted
// after staging so tests drive them explicitly; DS-2 lands the
// full direct-boot register state, timers, DMA and interrupts.
package system

import (
	"github.com/davidpatton/itak-gaming/cores/go-nds/bus"
	"github.com/davidpatton/itak-gaming/cores/go-nds/cart"
	"github.com/davidpatton/itak-gaming/cores/go-nds/chips/arm7"
	"github.com/davidpatton/itak-gaming/cores/go-nds/chips/arm9"
	"github.com/davidpatton/itak-gaming/cores/go-nds/chips/armcore"
)

// Frame budgets in CPU cycles. The ARM9 runs at 33.5 MHz and
// the ARM7 at half that; one 60 Hz frame is 560,190 ARM9 cycles
// and 280,095 ARM7 cycles.
const (
	CyclesPerFrame9 = 560190
	CyclesPerFrame7 = 280095
)

// Screen geometry of one DS display.
const (
	ScreenWidth  = 256
	ScreenHeight = 192
)

// Machine is one DS console.
type Machine struct {
	Bus  *bus.Bus
	CPU9 *armcore.CPU
	CPU7 *armcore.CPU

	header *cart.Header
	rom    []byte

	started9 bool
	started7 bool

	buttons uint32

	touchX    uint16
	touchY    uint16
	touchDown bool

	top    []uint32
	bottom []uint32
}

// New returns a machine with both CPUs halted and no cartridge.
func New() *Machine {
	b := bus.New()
	m := &Machine{Bus: b}
	m.CPU7 = arm7.New(b.ARM7())
	m.CPU9 = arm9.New(b.ARM9())
	b.AttachCPU9(m.CPU9)
	m.CPU9.Halted = true
	m.CPU7.Halted = true
	m.top = make([]uint32, ScreenWidth*ScreenHeight)
	m.bottom = make([]uint32, ScreenWidth*ScreenHeight)
	return m
}

// LoadROM parses the cartridge header and stages the ARM9 and
// ARM7 binaries at their load addresses through the owning CPU's
// bus view. The CPUs stay halted; call Start (or Start9/Start7)
// to run them.
func (m *Machine) LoadROM(rom []byte) error {
	h, err := cart.Parse(rom)
	if err != nil {
		return err
	}
	m.header = h
	m.rom = rom
	m.Bus.SetROM(rom)
	if h.ARM9Size > 0 {
		m.Bus.LoadBytes(true, h.ARM9Load, h.ARM9Binary(rom))
	}
	if h.ARM7Size > 0 {
		m.Bus.LoadBytes(false, h.ARM7Load, h.ARM7Binary(rom))
	}
	return nil
}

// Header returns the parsed cartridge header, or nil before
// LoadROM.
func (m *Machine) Header() *cart.Header { return m.header }

// Start9 starts the ARM9 at its header entry point.
func (m *Machine) Start9() {
	if m.header == nil {
		return
	}
	m.CPU9.Jump(m.header.ARM9Entry)
	m.started9 = true
}

// Start7 starts the ARM7 at its header entry point.
func (m *Machine) Start7() {
	if m.header == nil {
		return
	}
	m.CPU7.Jump(m.header.ARM7Entry)
	m.started7 = true
}

// Start starts both CPUs at their header entry points.
func (m *Machine) Start() {
	m.Start9()
	m.Start7()
}

// RunFrame advances the machine by one frame budget: the ARM9
// runs up to CyclesPerFrame9 cycles and the ARM7 up to
// CyclesPerFrame7, each only when started. Halted CPUs idle.
func (m *Machine) RunFrame() {
	if m.started9 {
		n := 0
		for n < CyclesPerFrame9 {
			n += m.CPU9.Step()
		}
	}
	if m.started7 {
		n := 0
		for n < CyclesPerFrame7 {
			n += m.CPU7.Step()
		}
	}
}

// Framebuffer returns the top and bottom screen buffers, each
// 256x192 XRGB8888. Slice 1 has no video engines, so both are
// blank; DS-2 fills them from the 2D engines.
func (m *Machine) Framebuffer() (top, bottom []uint32) {
	return m.top, m.bottom
}

// SetButtons stores the DS button state: bits 0-9 are A, B,
// Select, Start, Right, Left, Up, Down, R, L in the KEYINPUT
// order, bits 10-11 are X and Y. Slice 1 stores the state only;
// DS-2 wires it to the IO page.
func (m *Machine) SetButtons(mask uint32) { m.buttons = mask }

// Buttons returns the stored button state.
func (m *Machine) Buttons() uint32 { return m.buttons }

// SetTouch stores the touchscreen placeholder state (pixel
// coordinates and pen-down). The touchscreen device itself
// arrives in DS-4.
func (m *Machine) SetTouch(x, y uint16, down bool) {
	m.touchX, m.touchY, m.touchDown = x, y, down
}

// Touch returns the stored touchscreen placeholder state.
func (m *Machine) Touch() (x, y uint16, down bool) {
	return m.touchX, m.touchY, m.touchDown
}

// stateVersion is the save-state format version.
const stateVersion = 1

// trailerSize is the fixed machine-state block after the CPU and
// bus states.
const trailerSize = 32

// StateSize is the exact serialized size of a machine state.
const StateSize = 1 + bus.StateSize + 2*armcore.StateSize + trailerSize

// Serialize writes the full machine state (version byte, bus,
// both CPUs, machine trailer). The cartridge image itself is not
// part of the state; Deserialize expects a machine that already
// loaded the same ROM.
func (m *Machine) Serialize() []byte {
	out := make([]byte, StateSize)
	out[0] = stateVersion
	n := 1
	n += m.Bus.Serialize(out[n:])
	n += m.CPU9.Serialize(out[n:])
	n += m.CPU7.Serialize(out[n:])
	t := out[n:]
	if m.started9 {
		t[0] |= 1
	}
	if m.started7 {
		t[0] |= 2
	}
	put32 := func(at int, v uint32) {
		t[at] = byte(v)
		t[at+1] = byte(v >> 8)
		t[at+2] = byte(v >> 16)
		t[at+3] = byte(v >> 24)
	}
	put32(4, m.buttons)
	t[8] = byte(m.touchX)
	t[9] = byte(m.touchX >> 8)
	t[10] = byte(m.touchY)
	t[11] = byte(m.touchY >> 8)
	if m.touchDown {
		t[12] = 1
	}
	return out
}

// Deserialize restores a state written by Serialize into this
// machine, which must already have loaded the same cartridge.
func (m *Machine) Deserialize(src []byte) bool {
	if len(src) < StateSize || src[0] != stateVersion {
		return false
	}
	n := 1
	if !m.Bus.Deserialize(src[n:]) {
		return false
	}
	n += bus.StateSize
	if !m.CPU9.Deserialize(src[n:]) {
		return false
	}
	n += armcore.StateSize
	if !m.CPU7.Deserialize(src[n:]) {
		return false
	}
	n += armcore.StateSize
	t := src[n:]
	m.started9 = t[0]&1 != 0
	m.started7 = t[0]&2 != 0
	m.buttons = uint32(t[4]) | uint32(t[5])<<8 | uint32(t[6])<<16 | uint32(t[7])<<24
	m.touchX = uint16(t[8]) | uint16(t[9])<<8
	m.touchY = uint16(t[10]) | uint16(t[11])<<8
	m.touchDown = t[12] != 0
	return true
}
