// Package system wires the Allegrex CPU (with its VFPU), the GE display
// list processor, the memory bus and the UMD reader into the machine the
// rest of the project builds on.
//
// Slice PSP-3 state: LoadExecutable boots an ELF or PRX through the HLE
// kernel in the kernel package (threads, sync objects, the published
// HLE library subset, a virtual file system over the UMD image and an
// in-memory Memory Stick). The PSP-1/2 direct path stays available
// when no executable is loaded: display lists submitted through
// SubmitDisplayList execute synchronously through the GE. Save states
// are version 2 without a kernel and version 3 with one; the two do
// not mix (see Deserialize).
package system

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/davidpatton/itak-gaming/cores/go-psp/bus"
	"github.com/davidpatton/itak-gaming/cores/go-psp/chips/allegrex"
	"github.com/davidpatton/itak-gaming/cores/go-psp/ge"
	"github.com/davidpatton/itak-gaming/cores/go-psp/kernel"
	"github.com/davidpatton/itak-gaming/cores/go-psp/load"
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

// stateVersion tags the serialized layout of a machine without a
// kernel. Version 2 (PSP-2) appends the GE state block and the
// GE-active flag, and the CPU block grew to include the VFPU state.
// Version 3 (PSP-3) is the kernel mode layout: the version 2 body
// followed by the serialized kernel blob. A version 2 state loads
// only into a machine that has not booted an executable, and a
// version 3 state only makes sense with its kernel; both rules are
// enforced in Deserialize.
const stateVersion = 2
const stateVersionKernel = 3

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

// Machine is the PSP system.
type Machine struct {
	CPU *allegrex.CPU
	Bus *bus.Bus
	GE  *ge.GE

	iso *umd.ISO
	sfo *umd.SFO

	framebuffer []uint32
	buttons     uint32
	analogX     byte
	analogY     byte
	cycles      uint64
	geActive    bool // a display list was submitted; Framebuffer presents the GE

	kern       *kernel.Kernel // non-nil in kernel mode (after LoadExecutable)
	kernelMode bool
	msFiles    map[string][]byte
}

// New returns a machine with zeroed memory and the CPU parked at the
// default entry point.
func New() *Machine {
	b := bus.New()
	m := &Machine{
		Bus:         b,
		GE:          ge.New(b),
		framebuffer: make([]uint32, Width*Height),
		analogX:     0x80,
		analogY:     0x80,
	}
	for i := range m.framebuffer {
		m.framebuffer[i] = 0xFF000000 // opaque black until the GE draws
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

// --- Kernel mode (PSP-3) ---

// LoadExecutable boots an ELF (ET_EXEC / ET_SCE_EXEC) or PRX
// (ET_SCE_REL) image through the HLE kernel: the machine resets,
// the module loads into the user partition, and its main thread is
// created ready to run on the next RunFrame. Loading an executable
// replaces any previously running program.
func (m *Machine) LoadExecutable(data []byte) error {
	m.Bus = bus.New()
	m.GE = ge.New(m.Bus)
	m.CPU = allegrex.New(m.Bus)
	m.cycles = 0
	m.geActive = false
	for i := range m.framebuffer {
		m.framebuffer[i] = 0xFF000000
	}
	k := kernel.New(m)
	if err := k.LoadModule(data); err != nil {
		return err
	}
	m.kern = k
	m.kernelMode = true
	return nil
}

// KernelMode reports whether an executable has been booted.
func (m *Machine) KernelMode() bool { return m.kernelMode }

// Kernel returns the HLE kernel, or nil outside kernel mode.
func (m *Machine) Kernel() *kernel.Kernel { return m.kern }

// Exited reports whether the guest program has exited (through
// sceKernelExitGame or by every thread dying).
func (m *Machine) Exited() bool {
	return m.kernelMode && m.kern.Exited()
}

// ConsoleOutput returns everything the guest has written to the
// kernel console so far (empty outside kernel mode).
func (m *Machine) ConsoleOutput() string {
	if !m.kernelMode {
		return ""
	}
	return m.kern.ConsoleText()
}

// SetMemoryStickFiles installs the in-memory ms0: store the
// kernel's file system serves. Keys are full guest paths
// ("ms0:/SAVEDATA/x.bin"); writes through sceIo go to the same map.
func (m *Machine) SetMemoryStickFiles(files map[string][]byte) {
	m.msFiles = files
}

// MemoryStickFiles returns the ms0: store (kernel.Host).
func (m *Machine) MemoryStickFiles() map[string][]byte { return m.msFiles }

// The kernel.Host implementation.

var _ kernel.Host = (*Machine)(nil)

// Memory returns the bus as the loader's memory (kernel.Host).
func (m *Machine) Memory() load.Memory { return m.Bus }

// SharedCPU returns the shared Allegrex (kernel.Host).
func (m *Machine) SharedCPU() *allegrex.CPU { return m.CPU }

// PresentFrameBuffer routes a guest framebuffer selection to the
// GE display buffer and marks the GE as the presented source
// (kernel.Host; format codes are the GE codes 0=5650, 1=5551,
// 2=4444, 3=8888).
func (m *Machine) PresentFrameBuffer(addr uint32, width, format int) {
	m.GE.SetDisplayBuffer(addr, width, format)
	m.geActive = true
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

// Framebuffer returns the 480x272 XRGB8888 frame. Before any display
// list is submitted it is the opaque black buffer from boot; afterwards
// it presents the GE display buffer (the current draw buffer, or the
// buffer chosen with SetDisplayBuffer) converted from VRAM.
func (m *Machine) Framebuffer() []uint32 {
	if !m.geActive {
		return m.framebuffer
	}
	rgba := m.GE.ConvertDisplay(Width, Height)
	for i := 0; i < Width*Height; i++ {
		r := uint32(rgba[i*4])
		g := uint32(rgba[i*4+1])
		b := uint32(rgba[i*4+2])
		a := uint32(rgba[i*4+3])
		m.framebuffer[i] = a<<24 | r<<16 | g<<8 | b
	}
	return m.framebuffer
}

// SubmitDisplayList executes the display list at the given bus address
// synchronously through the GE and returns the resulting list state.
// Execution stops at END (StateCompleted) or at a SIGNAL
// (StateStalled); ContinueDisplayList resumes a stalled list. The HLE
// sceGu path that games use arrives in PSP-3; this direct API is how
// slices and tests drive the GE until then.
func (m *Machine) SubmitDisplayList(addr uint32) ge.ListState {
	st := m.GE.Submit(addr)
	m.geActive = true
	return st
}

// ContinueDisplayList resumes a display list that stalled on a SIGNAL.
func (m *Machine) ContinueDisplayList() ge.ListState {
	st := m.GE.Continue()
	m.geActive = true
	return st
}

// DisplayListState returns the GE list state: ready, stalled on a
// signal, or completed.
func (m *Machine) DisplayListState() ge.ListState {
	return m.GE.State()
}

// SetDisplayBuffer selects which VRAM buffer Framebuffer presents, in
// the GE pixel format codes (0=5650, 1=5551, 2=4444, 3=8888). Until a
// list or a game sets one, the GE draw buffer is presented.
func (m *Machine) SetDisplayBuffer(addr uint32, width, format int) {
	m.GE.SetDisplayBuffer(addr, width, format)
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
// consumed. In kernel mode the cycles go through the scheduler.
func (m *Machine) RunCycles(n int) {
	if m.kernelMode {
		m.kern.Run(uint64(n))
		return
	}
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

// RunFrame advances one frame budget. In kernel mode the budget is
// consumed across threads by the scheduler, then the vblank is
// notified (waking sceDisplayWaitVblankStart waiters for the next
// frame). Once the guest has exited, RunFrame is a no-op.
func (m *Machine) RunFrame() {
	if m.kernelMode {
		if m.kern.Exited() {
			return
		}
		m.kern.Run(FrameCycles)
		m.kern.NotifyVblank()
		return
	}
	m.RunCycles(FrameCycles)
}

// TotalCycles returns the total cycles executed since New. It is part of
// the serialized state, so a restored machine continues the count.
func (m *Machine) TotalCycles() uint64 {
	if m.kernelMode {
		return m.kern.Cycles()
	}
	return m.cycles
}

// StateSize returns the exact serialized size in bytes for the current
// format and the machine's current GE state (the GE block is variable
// length because the stencil plane is optional). Kernel mode states
// (version 3) add the length prefixed kernel blob.
func (m *Machine) StateSize() int {
	cpuLen := len(m.CPU.Serialize())
	geLen := len(m.GE.Serialize())
	n := 1 + 4 + cpuLen + bus.RAMSize + bus.VRAMSize + bus.ScratchpadSize + 4 + 2 + 2 + 8 + 4 + 4 + geLen
	if m.kernelMode {
		n += 4 + len(m.kern.Serialize())
	}
	return n
}

// Serialize captures the full machine state: CPU (including the VFPU),
// RAM, VRAM, scratchpad, input, the cycle counter, and the GE. The
// layout is:
//
//	[0]      state version byte (2, or 3 in kernel mode)
//	[1:5]    uint32 LE CPU state length
//	[5:...]  CPU state, then RAM, VRAM, scratchpad,
//	         buttons uint32, analog X, analog Y, 2 pad bytes,
//	         total cycles uint64, GE-active uint32,
//	         uint32 LE GE state length, GE state
//	         (version 3 only: uint32 LE kernel length, kernel state)
func (m *Machine) Serialize() []byte {
	cpuState := m.CPU.Serialize()
	geState := m.GE.Serialize()
	out := make([]byte, 0, m.StateSize())
	version := byte(stateVersion)
	if m.kernelMode {
		version = stateVersionKernel
	}
	out = append(out, version)
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
	// In kernel mode the authoritative cycle count is the kernel's
	// (m.cycles is not advanced there); TotalCycles reports it in
	// both modes, and Deserialize adopts it back the same way.
	binary.LittleEndian.PutUint64(tmp[:8], m.TotalCycles())
	out = append(out, tmp[:8]...)
	geActive := uint32(0)
	if m.geActive {
		geActive = 1
	}
	binary.LittleEndian.PutUint32(tmp[:4], geActive)
	out = append(out, tmp[:4]...)
	binary.LittleEndian.PutUint32(tmp[:4], uint32(len(geState)))
	out = append(out, tmp[:4]...)
	out = append(out, geState...)
	if m.kernelMode {
		kernelState := m.kern.Serialize()
		binary.LittleEndian.PutUint32(tmp[:4], uint32(len(kernelState)))
		out = append(out, tmp[:4]...)
		out = append(out, kernelState...)
	}
	return out
}

// Deserialize restores a state produced by Serialize. The bytes are
// parsed field by field and must be consumed exactly; anything else is
// an error and leaves the machine untouched. Version rules: a
// version 2 state carries no kernel and loads only into a machine
// that has not booted an executable; loading one into a kernel mode
// machine is an error (the kernel's threads and objects cannot be
// reconstructed from it). A version 3 state restores its kernel and
// puts the machine in kernel mode. Version 1 is rejected.
func (m *Machine) Deserialize(data []byte) error {
	if len(data) < 1+4 {
		return errors.New("system: state too short")
	}
	version := data[0]
	if version != stateVersion && version != stateVersionKernel {
		return fmt.Errorf("system: state version %d, want %d or %d", version, stateVersion, stateVersionKernel)
	}
	if version == stateVersion && m.kernelMode {
		return errors.New("system: version 2 state has no kernel state; refusing to load into a kernel mode machine")
	}
	kernelMode := version == stateVersionKernel
	off := 1
	cpuLen := int(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	if cpuLen != len(m.CPU.Serialize()) {
		return errors.New("system: CPU state length mismatch")
	}
	tailLen := cpuLen + bus.RAMSize + bus.VRAMSize + bus.ScratchpadSize + 4 + 4 + 8 + 4 + 4
	if len(data) < off+tailLen {
		return fmt.Errorf("system: state size %d too short for layout", len(data))
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
	off += 8
	geActive := binary.LittleEndian.Uint32(data[off:]) != 0
	off += 4
	geLen := int(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	if len(data) < off+geLen {
		return fmt.Errorf("system: state size %d too short for GE block", len(data))
	}
	geState := data[off : off+geLen]
	off += geLen
	var kernelState []byte
	if kernelMode {
		if len(data) < off+4 {
			return errors.New("system: version 3 state missing kernel block")
		}
		kernelLen := int(binary.LittleEndian.Uint32(data[off:]))
		off += 4
		if len(data) != off+kernelLen {
			return fmt.Errorf("system: state size %d, want %d", len(data), off+kernelLen)
		}
		kernelState = data[off : off+kernelLen]
	} else if len(data) != off {
		return fmt.Errorf("system: state size %d, want %d", len(data), off)
	}

	if err := m.CPU.Deserialize(cpuState); err != nil {
		return err
	}
	if err := m.GE.Deserialize(geState); err != nil {
		return err
	}
	copy(m.Bus.RAM, ram)
	copy(m.Bus.VRAM, vram)
	copy(m.Bus.Scratchpad, scratch)
	m.buttons = buttons
	m.analogX = analogX
	m.analogY = analogY
	m.cycles = cycles
	m.geActive = geActive
	if kernelMode {
		k := kernel.New(m)
		if err := k.Deserialize(kernelState); err != nil {
			return err
		}
		m.kern = k
		m.kernelMode = true
		m.cycles = k.Cycles()
	}
	return nil
}
