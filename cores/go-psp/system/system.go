// Package system wires the Allegrex CPU, the memory bus and the UMD reader
// into the machine skeleton the rest of the project builds on.
//
// Slice PSP-1 state: the CPU is the only active component. There is no GPU,
// no VFPU and no kernel HLE yet, so LoadISO only mounts and inspects the
// disc image; nothing boots from it in this slice. PSP software is an
// operating environment, and the eventual game path is HLE of the PSP
// kernel in PSP-3, not low-level boot of Sony code.
package system

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/davidpatton/itak-gaming/cores/go-psp/bus"
	"github.com/davidpatton/itak-gaming/cores/go-psp/chips/allegrex"
	"github.com/davidpatton/itak-gaming/cores/go-psp/umd"
)

// Display geometry of the PSP LCD.
const (
	Width  = 480
	Height = 272
)

// FrameCycles is the frame budget at the Allegrex default clock:
// 222 MHz / 60 frames = 3,700,000 cycles per frame. The real refresh is
// about 59.94 Hz; 60 is the family convention for this skeleton.
const FrameCycles = 3700000

// stateVersion tags the serialized layout. It must change whenever the
// layout changes.
const stateVersion = 1

// PSP controller button bits (sceCtrl masks).
const (
	ButtonSelect   uint32 = 0x00000001
	ButtonStart    uint32 = 0x00000008
	ButtonUp       uint32 = 0x00000010
	ButtonRight    uint32 = 0x00000020
	ButtonDown     uint32 = 0x00000040
	ButtonLeft     uint32 = 0x00000080
	ButtonL        uint32 = 0x00000100
	ButtonR        uint32 = 0x00000200
	ButtonTriangle uint32 = 0x00001000
	ButtonCircle   uint32 = 0x00002000
	ButtonCross    uint32 = 0x00004000
	ButtonSquare   uint32 = 0x00008000
)

// defaultEntry is where New leaves the CPU parked: the start of main RAM.
// No code lives there until a loader (PSP-3) or a test puts some there.
const defaultEntry = 0x08000000

// Machine is the PSP system skeleton.
type Machine struct {
	CPU *allegrex.CPU
	Bus *bus.Bus

	iso *umd.ISO
	sfo *umd.SFO

	framebuffer []uint32
	buttons     uint32
	analogX     byte
	analogY     byte
	cycles      uint64
}

// New returns a machine with zeroed memory and the CPU parked at the
// default entry point.
func New() *Machine {
	b := bus.New()
	m := &Machine{
		Bus:         b,
		framebuffer: make([]uint32, Width*Height),
		analogX:     0x80,
		analogY:     0x80,
	}
	for i := range m.framebuffer {
		m.framebuffer[i] = 0xFF000000 // opaque black until the GE exists
	}
	m.CPU = allegrex.New(b)
	m.Boot(defaultEntry)
	return m
}

// Boot resets the CPU at the given entry point. The PSP-3 module loader
// will call this with the entry of the loaded executable.
func (m *Machine) Boot(entry uint32) {
	m.CPU.Reset(entry)
}

// LoadISO mounts a user-supplied disc image and reads its PARAM.SFO
// metadata when present. The image is not decrypted and nothing is
// executed from it in this slice.
func (m *Machine) LoadISO(path string) error {
	iso, err := umd.Open(path)
	if err != nil {
		return err
	}
	if m.iso != nil {
		m.iso.Close()
	}
	m.iso = iso
	m.sfo = nil
	if sfo, err := iso.ParamSFO(); err == nil {
		m.sfo = sfo
	}
	return nil
}

// ISO returns the mounted disc image, or nil.
func (m *Machine) ISO() *umd.ISO {
	return m.iso
}

// GameTitle returns the TITLE field of the mounted disc's PARAM.SFO,
// falling back to the ISO volume id, then to the empty string.
func (m *Machine) GameTitle() string {
	if m.sfo != nil {
		if title, ok := m.sfo.String("TITLE"); ok {
			return title
		}
	}
	if m.iso != nil {
		return m.iso.VolumeID()
	}
	return ""
}

// LoadBytes copies data into memory through the bus. Loaders and tests use
// it to place code; it is not a disc operation.
func (m *Machine) LoadBytes(vaddr uint32, data []byte) {
	for i, v := range data {
		m.Bus.Write8(vaddr+uint32(i), v)
	}
}

// Framebuffer returns the 480x272 XRGB8888 frame. In this slice it stays
// blank; the GE arrives in PSP-2.
func (m *Machine) Framebuffer() []uint32 {
	return m.framebuffer
}

// SetButtons stores the current controller button bitset.
func (m *Machine) SetButtons(mask uint32) {
	m.buttons = mask
}

// Buttons returns the stored button bitset.
func (m *Machine) Buttons() uint32 {
	return m.buttons
}

// SetAnalog stores the analog stick position (0..255 per axis, 0x80 is
// centered). The stick is a stub in this slice: the value is kept and
// serialized, and the HLE input layer in PSP-3 will serve it to games.
func (m *Machine) SetAnalog(x, y byte) {
	m.analogX = x
	m.analogY = y
}

// Analog returns the stored analog stick position.
func (m *Machine) Analog() (x, y byte) {
	return m.analogX, m.analogY
}

// RunCycles advances the machine until at least n CPU cycles have been
// consumed.
func (m *Machine) RunCycles(n int) {
	spent := 0
	for spent < n {
		c := m.CPU.Step()
		if c < 1 {
			c = 1
		}
		spent += c
		m.cycles += uint64(c)
	}
}

// RunFrame advances one frame budget.
func (m *Machine) RunFrame() {
	m.RunCycles(FrameCycles)
}

// TotalCycles returns the total cycles executed since New. It is part of
// the serialized state, so a restored machine continues the count.
func (m *Machine) TotalCycles() uint64 {
	return m.cycles
}

// StateSize returns the exact serialized size in bytes for the current
// format.
func (m *Machine) StateSize() int {
	cpuLen := len(m.CPU.Serialize())
	return 1 + 4 + cpuLen + bus.RAMSize + bus.VRAMSize + bus.ScratchpadSize + 4 + 2 + 2 + 8
}

// Serialize captures the full machine state: CPU, RAM, VRAM, scratchpad,
// input and the cycle counter. The layout is:
//
//	[0]      state version byte
//	[1:5]    uint32 LE CPU state length
//	[5:...]  CPU state, then RAM, VRAM, scratchpad,
//	         buttons uint32, analog X, analog Y, 2 pad bytes,
//	         total cycles uint64
func (m *Machine) Serialize() []byte {
	cpuState := m.CPU.Serialize()
	out := make([]byte, 0, m.StateSize())
	out = append(out, stateVersion)
	var tmp [8]byte
	binary.LittleEndian.PutUint32(tmp[:4], uint32(len(cpuState)))
	out = append(out, tmp[:4]...)
	out = append(out, cpuState...)
	out = append(out, m.Bus.RAM...)
	out = append(out, m.Bus.VRAM...)
	out = append(out, m.Bus.Scratchpad...)
	binary.LittleEndian.PutUint32(tmp[:4], m.buttons)
	out = append(out, tmp[:4]...)
	out = append(out, m.analogX, m.analogY, 0, 0)
	binary.LittleEndian.PutUint64(tmp[:8], m.cycles)
	out = append(out, tmp[:8]...)
	return out
}

// Deserialize restores a state produced by Serialize. The bytes must match
// the current format exactly; anything else is an error and leaves the
// machine untouched.
func (m *Machine) Deserialize(data []byte) error {
	if len(data) != m.StateSize() {
		return fmt.Errorf("system: state size %d, want %d", len(data), m.StateSize())
	}
	if data[0] != stateVersion {
		return fmt.Errorf("system: state version %d, want %d", data[0], stateVersion)
	}
	off := 1
	cpuLen := int(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	if cpuLen != len(m.CPU.Serialize()) {
		return errors.New("system: CPU state length mismatch")
	}
	cpuState := data[off : off+cpuLen]
	off += cpuLen
	ram := data[off : off+bus.RAMSize]
	off += bus.RAMSize
	vram := data[off : off+bus.VRAMSize]
	off += bus.VRAMSize
	scratch := data[off : off+bus.ScratchpadSize]
	off += bus.ScratchpadSize
	buttons := binary.LittleEndian.Uint32(data[off:])
	off += 4
	analogX, analogY := data[off], data[off+1]
	off += 4 // analog pair plus 2 pad bytes
	cycles := binary.LittleEndian.Uint64(data[off:])

	if err := m.CPU.Deserialize(cpuState); err != nil {
		return err
	}
	copy(m.Bus.RAM, ram)
	copy(m.Bus.VRAM, vram)
	copy(m.Bus.Scratchpad, scratch)
	m.buttons = buttons
	m.analogX = analogX
	m.analogY = analogY
	m.cycles = cycles
	return nil
}
