// Package system is the whole Game Boy Advance machine
// for GBA-1: the ARM7TDMI CPU from chips/arm7tdmi, the
// memory map from the bus package, and a parsed cartridge
// from the cart package.
//
// The CPU is the only active component in this slice. The
// PPU, timers, DMA, interrupts and the keypad IO register
// arrive in GBA-2, audio in GBA-3. The framebuffer is
// therefore a blank 240x160 XRGB8888 buffer (every pixel
// 0xFF000000) and RunFrame simply spends the documented
// per-frame cycle budget on CPU steps: 280,896 cycles,
// which is 1232 cycles per line across 228 lines
// (160 visible, 68 of vertical blank) at the GBA's
// 16.78 MHz clock.
package system

import (
	"fmt"

	"github.com/davidpatton/itak-gaming/cores/go-gba/bus"
	"github.com/davidpatton/itak-gaming/cores/go-gba/cart"
	"github.com/davidpatton/itak-gaming/cores/go-gba/chips/arm7tdmi"
)

// Width and Height are the framebuffer dimensions, in the
// family shape the sibling cores use.
const (
	Width  = 240
	Height = 160
)

// CyclesPerFrame is the RunFrame budget.
const CyclesPerFrame = 280896

// Buttons, in GBATEK KEYINPUT bit order (bits 0 to 9).
// A set bit in the mask handed to SetButtons means the
// button is pressed; the active low register wiring is
// GBA-2 work.
const (
	ButtonA      = 1 << 0
	ButtonB      = 1 << 1
	ButtonSelect = 1 << 2
	ButtonStart  = 1 << 3
	ButtonRight  = 1 << 4
	ButtonLeft   = 1 << 5
	ButtonUp     = 1 << 6
	ButtonDown   = 1 << 7
	ButtonR      = 1 << 8
	ButtonL      = 1 << 9

	buttonsMask = 0x3FF // the low 10 bits
)

// stateVersion is the save state format version. Version
// 1 covers the CPU state and the bus stores listed in
// Serialize.
const stateVersion = 1

// Machine is one powered-on GBA.
type Machine struct {
	cpu  *arm7tdmi.CPU
	bus  *bus.Bus
	cart *cart.Cart

	buttons uint32
	frame   []uint32
}

// New builds an empty machine (no cartridge) in its
// power-on state.
func New() *Machine {
	m := &Machine{}
	m.bus = bus.New()
	m.cpu = arm7tdmi.New(m.bus)
	m.frame = blankFrame()
	return m
}

// blankFrame returns a fresh all black framebuffer.
func blankFrame() []uint32 {
	f := make([]uint32, Width*Height)
	for i := range f {
		f[i] = 0xFF000000
	}
	return f
}

// LoadCart parses a cartridge image, attaches it to the
// bus ROM windows, clears every machine store back to
// power-on state, and resets the CPU. A parse error
// leaves the previous cartridge (if any) untouched.
func (m *Machine) LoadCart(rom []byte) error {
	c, err := cart.Parse(rom)
	if err != nil {
		return err
	}
	m.cart = c
	m.bus = bus.New()
	m.bus.SetROM(c.ROM)
	m.cpu = arm7tdmi.New(m.bus)
	m.buttons = 0
	m.frame = blankFrame()
	return nil
}

// Reset returns the machine to power-on state with the
// current cartridge kept loaded.
func (m *Machine) Reset() {
	rom := []byte(nil)
	if m.cart != nil {
		rom = m.cart.ROM
	}
	m.bus = bus.New()
	m.bus.SetROM(rom)
	m.cpu = arm7tdmi.New(m.bus)
	m.buttons = 0
	m.frame = blankFrame()
}

// Cart returns the loaded cartridge, or nil.
func (m *Machine) Cart() *cart.Cart { return m.cart }

// Bus returns the system bus, for tools and tests.
func (m *Machine) Bus() *bus.Bus { return m.bus }

// CPU returns the processor, for tools and tests.
func (m *Machine) CPU() *arm7tdmi.CPU { return m.cpu }

// Title returns the loaded game's title, or empty.
func (m *Machine) Title() string {
	if m.cart == nil {
		return ""
	}
	return m.cart.Title
}

// RunFrame advances the machine by one frame: it steps
// the CPU until the 280,896 cycle budget is spent. The
// CPU is the only active component in GBA-1, so nothing
// else advances.
func (m *Machine) RunFrame() {
	spent := 0
	for spent < CyclesPerFrame {
		spent += m.cpu.Step()
	}
}

// Framebuffer returns the machine frame as Width*Height
// XRGB8888 pixels. In GBA-1 it is the blank buffer
// allocated once at power-on (all pixels 0xFF000000);
// the slice is owned by the machine.
func (m *Machine) Framebuffer() []uint32 {
	if m.frame == nil {
		m.frame = blankFrame()
	}
	return m.frame
}

// SetButtons stores the controller mask (see the Button
// constants). Only the low 10 bits are meaningful. The
// mask is stored raw in pressed-set form; wiring it into
// the keypad IO register is GBA-2 work.
func (m *Machine) SetButtons(mask uint32) { m.buttons = mask & buttonsMask }

// Buttons returns the stored controller mask.
func (m *Machine) Buttons() uint32 { return m.buttons }

// Save states.

// StateSize returns the exact byte size of a serialized
// state. It is the same for every cartridge, because the
// cartridge SRAM store is serialized at its full 64 KB
// whether or not the game uses it:
//
//	1 version byte + CPU state + 256 KB EWRAM + 32 KB
//	IWRAM + 64 KB SRAM + 1 KB palette + 96 KB VRAM +
//	1 KB OAM + 1 KB IO = 462,013 bytes with the current
//	188 byte CPU state block.
func (m *Machine) StateSize() int {
	return 1 + arm7tdmi.StateSize +
		bus.EWRAMSize + bus.IWRAMSize + bus.SRAMSize +
		bus.PaletteSize + bus.VRAMSize + bus.OAMSize + bus.IOSize
}

// Serialize captures the machine: the CPU state (through
// the CPU's own serializer) and the EWRAM, IWRAM, SRAM,
// palette, VRAM, OAM and IO stores, in that order, behind
// a version byte. The BIOS store is not part of the
// state (it is a zero filled, read only window in this
// core) and neither is the ROM image.
func (m *Machine) Serialize() []byte {
	out := make([]byte, m.StateSize())
	out[0] = stateVersion
	off := 1
	off += m.cpu.Serialize(out[off:])
	off += copy(out[off:], m.bus.EWRAM[:])
	off += copy(out[off:], m.bus.IWRAM[:])
	off += copy(out[off:], m.bus.SRAM[:])
	off += copy(out[off:], m.bus.Palette[:])
	off += copy(out[off:], m.bus.VRAM[:])
	off += copy(out[off:], m.bus.OAM[:])
	off += copy(out[off:], m.bus.IO[:])
	return out
}

// Deserialize restores a state produced by Serialize.
// The input must be exactly StateSize bytes and carry
// the current version. The memory stores are restored
// before the CPU, because the CPU's Deserialize
// re-fetches its executing instruction through the bus.
func (m *Machine) Deserialize(in []byte) error {
	if len(in) != m.StateSize() {
		return fmt.Errorf("system: state size %d, want %d", len(in), m.StateSize())
	}
	if in[0] != stateVersion {
		return fmt.Errorf("system: state version %d, want %d", in[0], stateVersion)
	}
	off := 1 + arm7tdmi.StateSize
	off += copy(m.bus.EWRAM[:], in[off:])
	off += copy(m.bus.IWRAM[:], in[off:])
	off += copy(m.bus.SRAM[:], in[off:])
	off += copy(m.bus.Palette[:], in[off:])
	off += copy(m.bus.VRAM[:], in[off:])
	off += copy(m.bus.OAM[:], in[off:])
	off += copy(m.bus.IO[:], in[off:])
	if !m.cpu.Deserialize(in[1 : 1+arm7tdmi.StateSize]) {
		return fmt.Errorf("system: CPU state block too short")
	}
	return nil
}
