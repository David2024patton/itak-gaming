// Package system assembles the GameCube machine: the
// Gekko CPU (chips/gekko), 24 MB of MEM1 main memory, the
// hardware register pages, and a disc image staged through
// the disc package.
//
// Slice GCN-1 scope: the CPU runs integer code from a
// staged DOL; the DSP and the EXI/SI/DI device register
// pages are stored stubs (writes keep their values,
// nothing acts on them); the IPL ROM window reads as
// zeros because this project ships no Nintendo IPL
// bytes and HLE boot is a later slice.
//
// Slice GCN-2 wires the GX renderer (package gpu) into
// the machine: the CP register window and the GX FIFO
// write port on the 0x0C hardware page route to the GPU,
// Framebuffer presents the GPU's embedded frame buffer,
// and save states grow to version 2 with the full GPU
// state included.
package system

import (
	"encoding/binary"
	"errors"

	"github.com/davidpatton/itak-gaming/cores/go-gamecube/chips/gekko"
	"github.com/davidpatton/itak-gaming/cores/go-gamecube/disc"
	"github.com/davidpatton/itak-gaming/cores/go-gamecube/gpu"
)

// Framebuffer dimensions the GX slice will render at.
const (
	Width  = 640
	Height = 480
)

// MEM1Size is the GameCube main memory size: 24 MB.
const MEM1Size = 24 << 20

const (
	lockedCacheSize = 256 << 10 // the 0xE0000000 locked cache window
	hwPageSize      = 64 << 10  // one 0x0C / 0x0D register page
)

// CyclesPerFrame is the frame budget at the Gekko clock
// of 486 MHz and 60 frames per second.
const CyclesPerFrame = 486_000_000 / 60

const serializeVersion = 2

// StateSize is the whole-machine save state size.
const StateSize = 1 + 4 + gekko.SerializeSize + gpu.SerializeSize + MEM1Size +
	lockedCacheSize + 2*hwPageSize + stateTailSize

const stateTailSize = 16 // pad state, started flag, entry point

// GamePad button bits, in the standard controller report
// order used by the SI polling format.
const (
	PadLeft  uint16 = 0x0001
	PadRight uint16 = 0x0002
	PadDown  uint16 = 0x0004
	PadUp    uint16 = 0x0008
	PadZ     uint16 = 0x0010
	PadR     uint16 = 0x0020
	PadL     uint16 = 0x0040
	PadA     uint16 = 0x0100
	PadB     uint16 = 0x0200
	PadX     uint16 = 0x0400
	PadY     uint16 = 0x0800
	PadStart uint16 = 0x1000
)

// PadState is one controller's state: two button bytes
// (in Buttons) and the analog channels.
type PadState struct {
	Buttons  uint16
	TriggerL uint8
	TriggerR uint8
	StickX   uint8
	StickY   uint8
	CStickX  uint8
	CStickY  uint8
}

// Machine is the assembled GameCube.
type Machine struct {
	cpu *gekko.CPU
	gpu *gpu.GPU

	mem1   []byte
	locked []byte
	hw0C   []byte
	hw0D   []byte

	fb      []uint32
	pad     PadState
	started bool
	entry   uint32

	// CP MMIO registers are 16-bit, but the bus moves
	// bytes; a write to the even byte of a register is
	// latched here until its odd byte arrives in the same
	// bus transaction.
	mmioLatch   uint8
	mmioLatchOK bool
}

// gpuMem adapts MEM1 to the gpu.Memory interface. The GPU
// reads physical, zero-based MEM1 addresses; anything
// outside MEM1 reads as zero.
type gpuMem struct{ m *Machine }

func (a gpuMem) Read8(addr uint32) uint8 {
	if addr < MEM1Size {
		return a.m.mem1[addr]
	}
	return 0
}

func (a gpuMem) Read16(addr uint32) uint16 {
	return uint16(a.Read8(addr))<<8 | uint16(a.Read8(addr+1))
}

func (a gpuMem) Read32(addr uint32) uint32 {
	return uint32(a.Read8(addr))<<24 | uint32(a.Read8(addr+1))<<16 |
		uint32(a.Read8(addr+2))<<8 | uint32(a.Read8(addr+3))
}

// New returns a powered-off machine with no disc loaded.
func New() *Machine {
	m := &Machine{
		mem1:   make([]byte, MEM1Size),
		locked: make([]byte, lockedCacheSize),
		hw0C:   make([]byte, hwPageSize),
		hw0D:   make([]byte, hwPageSize),
		fb:     make([]uint32, Width*Height),
	}
	m.gpu = gpu.New(gpuMem{m})
	m.cpu = gekko.New(m)
	m.cpu.Halted = true
	return m
}

// CPU exposes the processor for test harnesses and tools.
func (m *Machine) CPU() *gekko.CPU { return m.cpu }

// GPU exposes the GX renderer for test harnesses and
// tools.
func (m *Machine) GPU() *gpu.GPU { return m.gpu }

// LoadDisc stages a parsed disc image: the DOL sections
// are copied to their load addresses, the BSS is zeroed,
// and the CPU is reset to the DOL entry point, halted.
// Call Start to run it.
func (m *Machine) LoadDisc(img *disc.Image) error {
	dol, err := img.DOL()
	if err != nil {
		return err
	}
	for i := range m.mem1 {
		m.mem1[i] = 0
	}
	for i := range m.locked {
		m.locked[i] = 0
	}
	for _, s := range dol.Sections {
		for i, b := range s.Data {
			m.Write8(s.Address+uint32(i), b)
		}
	}
	for i := uint32(0); i < dol.BSSSize; i++ {
		m.Write8(dol.BSSAddress+i, 0)
	}
	m.cpu.Reset()
	m.cpu.PC = dol.Entry
	m.cpu.Halted = true
	// A staged disc boots against a clean GX: register
	// files, TMEM, and the EFB all return to reset state.
	m.gpu.Reset()
	m.started = false
	m.entry = dol.Entry
	return nil
}

// Start unhalts the CPU at the staged entry point.
func (m *Machine) Start() {
	m.started = true
	m.cpu.Halted = false
}

// Started reports whether the CPU has been started.
func (m *Machine) Started() bool { return m.started }

// Entry returns the staged DOL entry point.
func (m *Machine) Entry() uint32 { return m.entry }

// RunCycles steps the CPU for up to n cycles (fewer if it
// is halted).
func (m *Machine) RunCycles(n int) {
	for n > 0 {
		s := m.cpu.Step()
		if s == 0 {
			return
		}
		n -= s
	}
}

// RunFrame consumes one frame of CPU cycles.
func (m *Machine) RunFrame() { m.RunCycles(CyclesPerFrame) }

// Framebuffer returns the current frame: the GPU's EFB
// presented as a 640x480 XRGB8888 buffer (nearest-fit
// copy of the BP copy source rectangle, no deflicker).
func (m *Machine) Framebuffer() []uint32 {
	m.gpu.Present(m.fb)
	return m.fb
}

// SetButtons records the controller state.
func (m *Machine) SetButtons(p PadState) { m.pad = p }

// Buttons returns the recorded controller state.
func (m *Machine) Buttons() PadState { return m.pad }

// decode maps a physical address to its backing store.
// The standard GameCube windows are decoded: MEM1 at its
// physical base and at the cached (0x80000000) and
// uncached (0xC0000000) aliases, the 0x0C and 0x0D
// hardware pages, and the locked cache window. The IPL
// window at 0xFFF00000 and anything unmapped read as the
// stub value zero and ignore writes.
func (m *Machine) decode(addr uint32) ([]byte, uint32, bool) {
	switch {
	case addr < MEM1Size:
		return m.mem1, addr, true
	case addr >= 0x80000000 && addr < 0x80000000+MEM1Size:
		return m.mem1, addr - 0x80000000, true
	case addr >= 0xC0000000 && addr < 0xC0000000+MEM1Size:
		return m.mem1, addr - 0xC0000000, true
	case addr >= 0x0C000000 && addr < 0x0C000000+hwPageSize:
		return m.hw0C, addr - 0x0C000000, true
	case addr >= 0x0D000000 && addr < 0x0D000000+hwPageSize:
		return m.hw0D, addr - 0x0D000000, true
	case addr >= 0xE0000000 && addr < 0xE0000000+lockedCacheSize:
		return m.locked, addr - 0xE0000000, true
	}
	return nil, 0, false
}

// gxWindow maps an address in the Flipper GX register
// page to its page offset. The page is decoded at its
// physical base (0x0C000000) and at the cached
// (0x8C000000) and uncached (0xCC000000) aliases, which
// is where GameCube software actually addresses the CP
// registers and the GX FIFO write port.
func (m *Machine) gxWindow(addr uint32) (uint32, bool) {
	for _, base := range []uint32{0x0C000000, 0x8C000000, 0xCC000000} {
		if addr >= base && addr < base+hwPageSize {
			return addr - base, true
		}
	}
	return 0, false
}

// Offsets inside the GX page: the CP MMIO register window
// occupies 0x0000-0x007F (16-bit registers) and the GX
// FIFO write port occupies 0x8000-0x80FF. Every other
// offset (the PE register area at 0x1000 and the rest)
// stays plain stored state in the hw0C page until its
// own slice models it.
const (
	gxFIFOBase = 0x8000
	gxFIFOEnd  = 0x8100
)

// Read8 implements gekko.Bus.
func (m *Machine) Read8(addr uint32) uint8 {
	if off, ok := m.gxWindow(addr); ok {
		switch {
		case off < 0x80:
			// CP MMIO window: 16-bit registers, big-endian
			// byte order across the pair.
			v := m.gpu.ReadMMIO(off &^ 1)
			if off&1 == 0 {
				return uint8(v >> 8)
			}
			return uint8(v)
		case off >= gxFIFOBase && off < gxFIFOEnd:
			return 0 // the FIFO port is write-only
		default:
			return m.hw0C[off]
		}
	}
	if s, off, ok := m.decode(addr); ok {
		return s[off]
	}
	return 0
}

// Read16 implements gekko.Bus (big-endian assembly).
func (m *Machine) Read16(addr uint32) uint16 {
	return uint16(m.Read8(addr))<<8 | uint16(m.Read8(addr+1))
}

// Read32 implements gekko.Bus (big-endian assembly).
func (m *Machine) Read32(addr uint32) uint32 {
	return uint32(m.Read8(addr))<<24 | uint32(m.Read8(addr+1))<<16 |
		uint32(m.Read8(addr+2))<<8 | uint32(m.Read8(addr+3))
}

// Write8 implements gekko.Bus.
func (m *Machine) Write8(addr uint32, v uint8) {
	if off, ok := m.gxWindow(addr); ok {
		switch {
		case off < 0x80:
			// CP MMIO window: assemble the 16-bit register
			// from its two bytes. The latch only ever lives
			// inside one bus write transaction; save states
			// are taken between instructions, never here.
			if off&1 == 0 {
				m.mmioLatch = v
				m.mmioLatchOK = true
			} else if m.mmioLatchOK {
				m.gpu.WriteMMIO(off-1, uint16(m.mmioLatch)<<8|uint16(v))
				m.mmioLatchOK = false
			}
		case off >= gxFIFOBase && off < gxFIFOEnd:
			// Every byte stored anywhere in the FIFO port
			// window pushes one byte into the GX FIFO, in
			// bus order.
			m.gpu.WriteFIFO([]byte{v})
		default:
			m.hw0C[off] = v
		}
		return
	}
	if s, off, ok := m.decode(addr); ok {
		s[off] = v
	}
}

// Write16 implements gekko.Bus.
func (m *Machine) Write16(addr uint32, v uint16) {
	m.Write8(addr, uint8(v>>8))
	m.Write8(addr+1, uint8(v))
}

// Write32 implements gekko.Bus.
func (m *Machine) Write32(addr uint32, v uint32) {
	m.Write8(addr, uint8(v>>24))
	m.Write8(addr+1, uint8(v>>16))
	m.Write8(addr+2, uint8(v>>8))
	m.Write8(addr+3, uint8(v))
}

// Serialize writes the whole-machine state: version byte,
// the CPU state, the GPU state, MEM1, the locked cache
// window, both hardware pages, and the machine tail
// (pad, started, entry).
func (m *Machine) Serialize() []byte {
	out := make([]byte, StateSize)
	out[0] = serializeVersion
	cpuState := m.cpu.Serialize()
	binary.BigEndian.PutUint32(out[1:], uint32(len(cpuState)))
	off := 5
	off += copy(out[off:], cpuState)
	off += copy(out[off:], m.gpu.Serialize())
	off += copy(out[off:], m.mem1)
	off += copy(out[off:], m.locked)
	off += copy(out[off:], m.hw0C)
	off += copy(out[off:], m.hw0D)
	binary.BigEndian.PutUint16(out[off:], m.pad.Buttons)
	out[off+2] = m.pad.TriggerL
	out[off+3] = m.pad.TriggerR
	out[off+4] = m.pad.StickX
	out[off+5] = m.pad.StickY
	out[off+6] = m.pad.CStickX
	out[off+7] = m.pad.CStickY
	if m.started {
		out[off+8] = 1
	}
	binary.BigEndian.PutUint32(out[off+12:], m.entry)
	return out
}

// ErrBadState reports a state block that does not match
// this machine's layout or version.
var ErrBadState = errors.New("system: bad save state")

// Deserialize restores a state written by Serialize.
func (m *Machine) Deserialize(in []byte) error {
	if len(in) != StateSize || in[0] != serializeVersion {
		return ErrBadState
	}
	cpuLen := int(binary.BigEndian.Uint32(in[1:]))
	if cpuLen != gekko.SerializeSize {
		return ErrBadState
	}
	off := 5
	if err := m.cpu.Deserialize(in[off : off+cpuLen]); err != nil {
		return err
	}
	off += cpuLen
	if err := m.gpu.Deserialize(in[off : off+gpu.SerializeSize]); err != nil {
		return ErrBadState
	}
	off += gpu.SerializeSize
	off += copy(m.mem1, in[off:])
	off += copy(m.locked, in[off:])
	off += copy(m.hw0C, in[off:])
	off += copy(m.hw0D, in[off:])
	m.pad.Buttons = binary.BigEndian.Uint16(in[off:])
	m.pad.TriggerL = in[off+2]
	m.pad.TriggerR = in[off+3]
	m.pad.StickX = in[off+4]
	m.pad.StickY = in[off+5]
	m.pad.CStickX = in[off+6]
	m.pad.CStickY = in[off+7]
	m.started = in[off+8] != 0
	m.entry = binary.BigEndian.Uint32(in[off+12:])
	m.cpu.Halted = !m.started
	return nil
}
