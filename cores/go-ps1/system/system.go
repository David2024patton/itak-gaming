// Package system is the whole PlayStation machine for
// the go-ps1 core, in the same family shape as the
// other iTaK cores.
//
// PS1-1 wires exactly three things together: the
// R3000A CPU (chips/r3000a), the memory bus (bus), and
// a loaded disc image (cd) that nothing reads from yet
// beyond being held. The GTE register file (chips/gte)
// is attached to the CPU; its math is PS1-3. There is
// no GPU, no SPU, no DMA execution and no interrupt
// controller in this slice: the frame is a CPU cycle
// budget and the framebuffer is blank.
//
// Frame budget: the NTSC PS1 runs its CPU at
// 33.8688 MHz and its frame at 59.94 Hz, so a frame is
// 33,868,800 / 59.94 cycles. RunFrame accumulates the
// exact rational (x100 / 5994) so the long-term rate
// is exact; individual frames alternate 565,045 and
// 565,046 cycles.
package system

import (
	"fmt"

	"github.com/davidpatton/itak-gaming/cores/go-ps1/bus"
	"github.com/davidpatton/itak-gaming/cores/go-ps1/cd"
	"github.com/davidpatton/itak-gaming/cores/go-ps1/chips/gte"
	"github.com/davidpatton/itak-gaming/cores/go-ps1/chips/r3000a"
)

// Framebuffer dimensions for this slice. Real PS1
// display modes vary up to 640x480; the skeleton
// reports the maximum and renders nothing.
const (
	Width  = 640
	Height = 480
)

// Digital pad buttons, stored by SetButtons as a
// bitmask in PlayStation pad order.
const (
	ButtonSelect = 1 << 0
	ButtonL3     = 1 << 1
	ButtonR3     = 1 << 2
	ButtonStart  = 1 << 3
	ButtonUp     = 1 << 4
	ButtonRight  = 1 << 5
	ButtonDown   = 1 << 6
	ButtonLeft   = 1 << 7
	ButtonL2     = 1 << 8
	ButtonR2     = 1 << 9
	ButtonL1     = 1 << 10
	ButtonR1     = 1 << 11
	ButtonTri    = 1 << 12
	ButtonCircle = 1 << 13
	ButtonCross  = 1 << 14
	ButtonSquare = 1 << 15
)

const (
	cpuHz        = 33_868_800
	frameRateHz  = 5994 // 59.94 Hz, scaled by 100
	stateVersion = 1
)

// Machine is one powered-on PlayStation.
type Machine struct {
	CPU  *r3000a.CPU
	Bus  *bus.Bus
	Disc cd.Reader

	fb        []uint32
	buttons   uint32
	frame     uint64
	budgetRem int64
}

// New returns a machine in reset state with no disc.
func New() *Machine {
	m := &Machine{fb: make([]uint32, Width*Height)}
	m.Bus = bus.New()
	m.CPU = r3000a.New(m.Bus)
	m.CPU.CP2 = gte.New()
	m.Reset()
	return m
}

// LoadDisc inserts a disc image and resets the
// machine, matching how the other cores treat a game
// load.
func (m *Machine) LoadDisc(r cd.Reader) error {
	if r == nil {
		return fmt.Errorf("system: nil disc reader")
	}
	m.Disc = r
	m.Reset()
	return nil
}

// OpenDisc opens a disc image by path (see cd.Open)
// and loads it.
func (m *Machine) OpenDisc(path string) error {
	img, err := cd.Open(path)
	if err != nil {
		return err
	}
	return m.LoadDisc(img)
}

// Reset returns the machine to its power-on state.
func (m *Machine) Reset() {
	m.Bus.Reset()
	m.CPU.Reset()
	m.CPU.CP2 = gte.New()
	m.frame = 0
	m.budgetRem = 0
	for i := range m.fb {
		m.fb[i] = 0xFF000000
	}
}

// Step executes one CPU instruction and returns the
// cycles it consumed.
func (m *Machine) Step() int {
	return m.CPU.Step()
}

// RunCycles executes CPU instructions until at least
// n cycles have been consumed.
func (m *Machine) RunCycles(n int) {
	done := 0
	for done < n {
		done += m.Step()
	}
}

// RunFrame runs one NTSC frame of CPU time.
func (m *Machine) RunFrame() {
	m.budgetRem += cpuHz * 100
	budget := m.budgetRem / frameRateHz
	m.budgetRem -= budget * frameRateHz
	m.RunCycles(int(budget))
	m.frame++
}

// Frames returns the number of RunFrame calls since
// reset.
func (m *Machine) Frames() uint64 { return m.frame }

// Framebuffer returns the current frame as XRGB8888
// pixels, Width by Height. Blank in this slice.
func (m *Machine) Framebuffer() []uint32 { return m.fb }

// SetButtons stores the digital pad state for port 1.
func (m *Machine) SetButtons(mask uint32) { m.buttons = mask }

// Buttons returns the stored pad state.
func (m *Machine) Buttons() uint32 { return m.buttons }

// StateSize returns the exact serialized state size:
// a version byte, the CPU state, main RAM and the
// scratchpad.
func (m *Machine) StateSize() int {
	return 1 + r3000a.StateSize + bus.RAMSize + bus.ScratchpadSize
}

// Serialize returns the machine state. The disc image
// is not part of the state (it is the loaded media,
// re-supplied by the caller on restore, like the
// other cores' cartridges).
func (m *Machine) Serialize() []byte {
	out := make([]byte, m.StateSize())
	out[0] = stateVersion
	off := 1
	copy(out[off:off+r3000a.StateSize], m.CPU.Serialize())
	off += r3000a.StateSize
	copy(out[off:off+bus.RAMSize], m.Bus.RAM())
	off += bus.RAMSize
	copy(out[off:off+bus.ScratchpadSize], m.Bus.Scratchpad())
	return out
}

// Deserialize restores a state produced by Serialize.
func (m *Machine) Deserialize(in []byte) error {
	if len(in) != m.StateSize() {
		return fmt.Errorf("system: state size %d, want %d", len(in), m.StateSize())
	}
	if in[0] != stateVersion {
		return fmt.Errorf("system: state version %d, want %d", in[0], stateVersion)
	}
	off := 1
	if err := m.CPU.Deserialize(in[off : off+r3000a.StateSize]); err != nil {
		return err
	}
	off += r3000a.StateSize
	copy(m.Bus.RAM(), in[off:off+bus.RAMSize])
	off += bus.RAMSize
	copy(m.Bus.Scratchpad(), in[off:off+bus.ScratchpadSize])
	return nil
}
