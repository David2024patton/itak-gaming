// Package system is the whole Game Boy Advance machine:
// the ARM7TDMI CPU from chips/arm7tdmi, the memory map
// from the bus package, a parsed cartridge from the cart
// package, the PPU from the gpu package, the timers, DMA,
// interrupt controller and keypad from the periph package,
// and our own BIOS code and SWI HLE from the bios package.
//
// Execution model: the frame runs line by line (228
// lines of 1232 cycles: 160 visible, 68 of vertical
// blank). Within a line the CPU runs for the visible
// portion first, the line is rendered by the PPU at the
// start of horizontal blank, line-triggered DMA and
// interrupts fire, and the CPU runs out the remainder of
// the line. Peripherals advance on the cycle counts the
// CPU reports, so timer and DMA behavior is consistent
// with the instruction stream at line granularity.
package system

import (
	"encoding/binary"
	"fmt"

	"github.com/davidpatton/itak-gaming/cores/go-gba/bios"
	"github.com/davidpatton/itak-gaming/cores/go-gba/bus"
	"github.com/davidpatton/itak-gaming/cores/go-gba/cart"
	"github.com/davidpatton/itak-gaming/cores/go-gba/chips/arm7tdmi"
	"github.com/davidpatton/itak-gaming/cores/go-gba/gpu"
	"github.com/davidpatton/itak-gaming/cores/go-gba/periph"
)

// Width and Height are the framebuffer dimensions, in the
// family shape the sibling cores use.
const (
	Width  = 240
	Height = 160
)

// CyclesPerFrame is the RunFrame budget.
const CyclesPerFrame = 280896

// CyclesPerLine is the documented line period.
const CyclesPerLine = 1232

// cyclesVisible is the part of a line before horizontal
// blank begins (cycles 0 to 1005); the rest of the line
// is the horizontal blank window.
const cyclesVisible = 1006

// linesPerFrame is 160 visible lines plus 68 of blank.
const linesPerFrame = 228

// Buttons, in GBATEK KEYINPUT bit order (bits 0 to 9).
// A set bit in the mask handed to SetButtons means the
// button is pressed; the keypad register reports the
// active low form.
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
// 2 covers the CPU state, the bus stores, and the GBA-2
// component states (PPU, interrupt controller, timers,
// DMA, keypad, BIOS wait state) behind the machine
// trailer.
const stateVersion = 2

// machineTrailerSize is the fixed trailer behind the
// component blocks: buttons, current line, and the FIFO
// sink counters and last values for DMA channels 1 and 2.
const machineTrailerSize = 4 + 4 + 2*8 + 2*4

// Machine is one powered-on GBA.
type Machine struct {
	cpu  *arm7tdmi.CPU
	bus  *bus.Bus
	cart *cart.Cart

	gpu    *gpu.GPU
	irq    *periph.IRQ
	timers *periph.Timers
	dma    *periph.DMA
	keys   *periph.Keypad
	biosH  *bios.Handler

	buttons uint32
	line    int

	// The FIFO sink the GBA-2 DMA feeds until GBA-3 turns
	// it into audio: it counts the samples per channel
	// and keeps the most recent value.
	fifoCount [2]uint64
	fifoLast  [2]uint32
}

// New builds an empty machine (no cartridge) in its
// power-on state.
func New() *Machine {
	m := &Machine{}
	m.power()
	return m
}

// power rebuilds the bus, installs our BIOS code into the
// BIOS store, recreates the CPU and every component, and
// wires the dispatch hooks. The cartridge (if any) stays
// attached. This is the shared body of construction,
// LoadCart and Reset.
func (m *Machine) power() {
	rom := []byte(nil)
	if m.cart != nil {
		rom = m.cart.ROM
	}
	m.bus = bus.New()
	m.bus.SetROM(rom)
	bios.Install(m.bus.BIOS[:])
	m.cpu = arm7tdmi.New(m.bus)
	m.gpu = gpu.New(m.bus.Palette[:], m.bus.VRAM[:], m.bus.OAM[:])
	m.irq = periph.NewIRQ()
	m.timers = periph.NewTimers()
	m.dma = periph.NewDMA(m.bus, m.irq, m)
	m.keys = periph.NewKeypad()
	m.biosH = bios.New(m.bus)
	m.cpu.SWIHook = m.biosH.Hook
	m.bus.IORead = m.ioRead
	m.bus.IOWrite = m.ioWrite
	// The stack pointers the BIOS boot stub also sets,
	// preset here so a CPU primed directly at the
	// cartridge entry (as tools and tests do) still sees
	// the documented stacks.
	m.cpu.R[13] = bios.SVCStackTop
	m.cpu.RSVC[0] = bios.SVCStackTop
	m.cpu.RIRQ[0] = bios.IRQStackTop
	m.cpu.RUSR[0] = bios.UserStackTop
	m.buttons = 0
	m.line = 0
	m.fifoCount = [2]uint64{}
	m.fifoLast = [2]uint32{}
}

// LoadCart parses a cartridge image, attaches it to the
// bus ROM windows, and resets the machine. A parse error
// leaves the previous cartridge (if any) untouched.
func (m *Machine) LoadCart(rom []byte) error {
	c, err := cart.Parse(rom)
	if err != nil {
		return err
	}
	m.cart = c
	m.power()
	return nil
}

// Reset returns the machine to power-on state with the
// current cartridge kept loaded.
func (m *Machine) Reset() { m.power() }

// Cart returns the loaded cartridge, or nil.
func (m *Machine) Cart() *cart.Cart { return m.cart }

// Bus returns the system bus, for tools and tests.
func (m *Machine) Bus() *bus.Bus { return m.bus }

// CPU returns the processor, for tools and tests.
func (m *Machine) CPU() *arm7tdmi.CPU { return m.cpu }

// GPU returns the picture processor, for tools and tests.
func (m *Machine) GPU() *gpu.GPU { return m.gpu }

// Title returns the loaded game's title, or empty.
func (m *Machine) Title() string {
	if m.cart == nil {
		return ""
	}
	return m.cart.Title
}

// FIFOSample is the DMA FIFO sink: until GBA-3 builds
// the audio path, samples delivered by DMA channels 1
// and 2 are counted and the latest value kept, so the
// FIFO trigger path is observable and testable.
func (m *Machine) FIFOSample(channel int, value uint32) {
	if channel < 1 || channel > 2 {
		return
	}
	m.fifoCount[channel-1]++
	m.fifoLast[channel-1] = value
}

// FIFOSamples reports how many FIFO samples a DMA
// channel (1 or 2) has delivered since power-on.
func (m *Machine) FIFOSamples(channel int) uint64 {
	if channel < 1 || channel > 2 {
		return 0
	}
	return m.fifoCount[channel-1]
}

// IO dispatch.

// ioRead routes an IO window read to the component that
// owns the register. Behavior-less registers fall
// through to the bus's plain IO store.
func (m *Machine) ioRead(off uint32, size int) (uint32, bool) {
	switch {
	case off < 0x60:
		return m.gpu.ReadReg(off, size), true
	case off >= 0xB0 && off < 0xE0:
		return m.dma.ReadReg(off, size), true
	case off >= 0x100 && off < 0x110:
		return m.timers.ReadReg(off, size), true
	case off >= 0x130 && off < 0x134:
		return m.keys.ReadReg(off, size), true
	case off >= 0x200 && off < 0x204, off >= 0x208 && off < 0x20C:
		return m.irq.ReadReg(off, size), true
	}
	return 0, false
}

// ioWrite routes an IO window write to the component
// that owns the register.
func (m *Machine) ioWrite(off uint32, size int, val uint32) bool {
	switch {
	case off < 0x60:
		m.gpu.WriteReg(off, size, val)
	case off >= 0xB0 && off < 0xE0:
		m.dma.WriteReg(off, size, val)
	case off >= 0x100 && off < 0x110:
		m.timers.WriteReg(off, size, val)
	case off >= 0x130 && off < 0x134:
		m.keys.WriteReg(off, size, val)
	case off >= 0x200 && off < 0x204, off >= 0x208 && off < 0x20C:
		m.irq.WriteReg(off, size, val)
	default:
		return false
	}
	return true
}

// Execution.

// runCPU steps the CPU for the given cycle budget,
// advancing the timers by the cycles each step reports
// and keeping the CPU's IRQ input level current. A CPU
// halted in a BIOS wait is polled so the wait can end as
// soon as its wake condition lands.
func (m *Machine) runCPU(cycles int) {
	spent := 0
	for spent < cycles {
		if m.cpu.Halted {
			m.biosH.Poll(m.cpu)
		}
		m.cpu.IRQLine = m.irq.Asserted()
		n := m.cpu.Step()
		m.timers.Advance(n, m.irq)
		spent += n
	}
}

// runLines advances the machine by n scanlines. Each
// line: vblank and vcount events are evaluated at the
// line start, the PPU latches its scroll registers, the
// CPU runs the visible portion, the line renders as
// horizontal blank begins, HBlank events fire, and the
// CPU runs the rest of the line.
func (m *Machine) runLines(n int) {
	for i := 0; i < n; i++ {
		line := m.line
		m.gpu.SetLine(line)
		dispstat := m.gpu.ReadReg(0x04, 2)
		if line == 160 {
			m.gpu.SetVBlank(true)
			if dispstat&0x0008 != 0 {
				m.irq.Raise(periph.IRQVBlank)
			}
			m.dma.OnVBlank()
		}
		if line == 0 {
			m.gpu.SetVBlank(false)
		}
		if m.gpu.VCountMatch() && dispstat&0x0020 != 0 {
			m.irq.Raise(periph.IRQVCount)
		}
		m.gpu.BeginLine()
		m.runCPU(cyclesVisible)
		if line < Height {
			m.gpu.RenderLine()
		}
		m.gpu.SetHBlank(true)
		if dispstat&0x0010 != 0 {
			m.irq.Raise(periph.IRQHBlank)
		}
		if line < Height {
			m.dma.OnHBlank()
		}
		m.runCPU(CyclesPerLine - cyclesVisible)
		m.gpu.SetHBlank(false)
		m.line++
		if m.line >= linesPerFrame {
			m.line = 0
		}
	}
}

// RunFrame advances the machine by one frame: 228 lines
// against the documented per-frame cycle budget.
func (m *Machine) RunFrame() { m.runLines(linesPerFrame) }

// Framebuffer returns the machine frame as Width*Height
// XRGB8888 pixels, owned by the PPU.
func (m *Machine) Framebuffer() []uint32 { return m.gpu.Framebuffer() }

// SetButtons stores the controller mask (see the Button
// constants) and presents it to the keypad register,
// which evaluates its interrupt condition.
func (m *Machine) SetButtons(mask uint32) {
	m.buttons = mask & buttonsMask
	m.keys.SetButtons(m.buttons, m.irq)
}

// Buttons returns the stored controller mask.
func (m *Machine) Buttons() uint32 { return m.buttons }

// Save states.

// StateSize returns the exact byte size of a serialized
// state: the version byte, the CPU block, the seven bus
// stores, the five GBA-2 component blocks, and the
// machine trailer.
func (m *Machine) StateSize() int {
	return 1 + arm7tdmi.StateSize +
		bus.EWRAMSize + bus.IWRAMSize + bus.SRAMSize +
		bus.PaletteSize + bus.VRAMSize + bus.OAMSize + bus.IOSize +
		gpu.StateSize + periph.IRQStateSize + periph.TimersStateSize +
		periph.DMAStateSize + periph.KeypadStateSize + bios.HandlerStateSize +
		machineTrailerSize
}

// Serialize captures the machine in the layout StateSize
// describes. The BIOS store is not part of the state (it
// holds our own code, reinstalled at power-on) and
// neither is the ROM image.
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
	off += m.gpu.Serialize(out[off:])
	off += m.irq.Serialize(out[off:])
	off += m.timers.Serialize(out[off:])
	off += m.dma.Serialize(out[off:])
	off += m.keys.Serialize(out[off:])
	off += m.biosH.Serialize(out[off:])
	binary.LittleEndian.PutUint32(out[off:], m.buttons)
	off += 4
	binary.LittleEndian.PutUint32(out[off:], uint32(m.line))
	off += 4
	for i := 0; i < 2; i++ {
		binary.LittleEndian.PutUint64(out[off:], m.fifoCount[i])
		off += 8
	}
	for i := 0; i < 2; i++ {
		binary.LittleEndian.PutUint32(out[off:], m.fifoLast[i])
		off += 4
	}
	return out
}

// Deserialize restores a state produced by Serialize.
// The input must be exactly StateSize bytes and carry
// the current version; version 1 states are rejected by
// that check, as in the sibling cores. The memory stores
// and components are restored before the CPU, because
// the CPU's Deserialize re-fetches its executing
// instruction through the bus.
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
	blocks := []struct {
		size int
		load func([]byte) bool
	}{
		{gpu.StateSize, m.gpu.Deserialize},
		{periph.IRQStateSize, m.irq.Deserialize},
		{periph.TimersStateSize, m.timers.Deserialize},
		{periph.DMAStateSize, m.dma.Deserialize},
		{periph.KeypadStateSize, m.keys.Deserialize},
		{bios.HandlerStateSize, m.biosH.Deserialize},
	}
	for _, b := range blocks {
		if !b.load(in[off : off+b.size]) {
			return fmt.Errorf("system: component state block at %d rejected", off)
		}
		off += b.size
	}
	m.buttons = binary.LittleEndian.Uint32(in[off:])
	off += 4
	m.line = int(binary.LittleEndian.Uint32(in[off:]))
	off += 4
	for i := 0; i < 2; i++ {
		m.fifoCount[i] = binary.LittleEndian.Uint64(in[off:])
		off += 8
	}
	for i := 0; i < 2; i++ {
		m.fifoLast[i] = binary.LittleEndian.Uint32(in[off:])
		off += 4
	}
	m.keys.SetButtons(m.buttons, m.irq)
	if !m.cpu.Deserialize(in[1 : 1+arm7tdmi.StateSize]) {
		return fmt.Errorf("system: CPU state block too short")
	}
	return nil
}
