// Package system is the whole PlayStation machine for
// the go-ps1 core, in the same family shape as the
// other iTaK cores.
//
// PS1-2 wiring: the R3000A CPU (chips/r3000a) runs on
// the memory bus (bus) with the GTE register file as
// CP2 (math is PS1-3). The bus register page is
// routed to a peripheral multiplexer (mux.go) serving
// the GPU (gpu), the root counters (timers), the DMA
// controller (dma), the CD controller subset (cd) and
// the SPU RAM transfer port (spu), plus the real
// interrupt controller registers I_STAT and I_MASK.
//
// Execution model (stated in the core README):
// peripherals advance at instruction boundaries in
// Step. The display timing model derives the beam
// position from accumulated video clock cycles at the
// documented rates (NTSC 53.693175 MHz video clock,
// 263 lines of 3413 cycles; PAL 53.203425 MHz, 314
// lines of 3406 cycles, selected by the GPU display
// mode), so hblank/vblank windows, dotclock ticks and
// timer sync behavior are computed from the machine's
// true position, not from free-running approximations.
// Interrupt sources (VBlank, GPU, CDROM, DMA,
// TMR0/1/2) latch bits in I_STAT; the CPU external
// interrupt line follows (I_STAT AND I_MASK) through
// the CPU's CP0 IP2 path.
//
// Frame budget: the NTSC CPU frame remains the
// PS1-1 exact rational (33,868,800 x 100 / 5994, so
// frames alternate 565,045 and 565,046 cycles). The
// display frame at 263 lines is a few hundred cycles
// longer than the CPU frame budget; both models are
// exact in themselves and the README notes the
// relationship.
package system

import (
	"fmt"

	"github.com/davidpatton/itak-gaming/cores/go-ps1/bus"
	"github.com/davidpatton/itak-gaming/cores/go-ps1/cd"
	"github.com/davidpatton/itak-gaming/cores/go-ps1/chips/gte"
	"github.com/davidpatton/itak-gaming/cores/go-ps1/chips/r3000a"
	"github.com/davidpatton/itak-gaming/cores/go-ps1/dma"
	"github.com/davidpatton/itak-gaming/cores/go-ps1/gpu"
	"github.com/davidpatton/itak-gaming/cores/go-ps1/spu"
	"github.com/davidpatton/itak-gaming/cores/go-ps1/timers"
)

// Maximum framebuffer dimensions across display
// modes. The actual frame is the GPU's native
// resolution; see DisplayWidth and DisplayHeight.
const (
	Width  = gpu.MaxWidth
	Height = gpu.MaxHeight
)

// Digital pad buttons, stored by SetButtons as a
// bitmask in PlayStation pad order. The pad hardware
// itself lands in a later slice; the I_STAT pad bit
// exists with no source yet.
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

// Interrupt controller bits in I_STAT and I_MASK.
const (
	IRQVBlank = 0
	IRQGPU    = 1
	IRQCDROM  = 2
	IRQDMA    = 3
	IRQTMR0   = 4
	IRQTMR1   = 5
	IRQTMR2   = 6
	IRQPAD    = 7
	IRQSPU    = 9
)

const (
	cpuHz        = 33_868_800
	frameRateHz  = 5994 // 59.94 Hz, scaled by 100
	stateVersion = 2
	miscSize     = 96
)

// Video timing constants per video standard. The
// video clock runs at these rates against the CPU
// clock; positions derive from their exact ratio.
type videoMode struct {
	lines    int64
	dotsLine int64
	clockNum int64 // video clock Hz numerator over cpuHz
}

var (
	videoNTSC = videoMode{lines: 263, dotsLine: 3413, clockNum: 53_693_175}
	videoPAL  = videoMode{lines: 314, dotsLine: 3406, clockNum: 53_203_425}
)

// Machine is one powered-on PlayStation.
type Machine struct {
	CPU  *r3000a.CPU
	Bus  *bus.Bus
	Disc cd.Reader

	GPU    *gpu.GPU
	Timers *timers.Timers
	DMA    *dma.Controller
	SPU    *spu.SPU
	CD     *cd.Controller

	istat uint16
	imask uint16

	buttons   uint32
	frame     uint64
	budgetRem int64

	// Display timing state: absolute video cycles
	// since reset and the derived-event counters used
	// for exact per-interval deltas.
	absVC         int64
	vcRem         int64
	displayFrames uint64
	prevDotTicks  int64
	prevHBlank    int64
	prevVBlank    int64
	gpuIRQPrev    bool

	mdecCount uint32
	mdecSum   uint32
}

// New returns a machine in reset state with no disc.
func New() *Machine {
	m := &Machine{}
	m.Bus = bus.New()
	m.GPU = gpu.New()
	m.Timers = timers.New()
	m.DMA = dma.New()
	m.SPU = spu.New()
	m.CD = cd.NewController()
	m.DMA.Mem = m.Bus
	m.DMA.Dev = m
	m.DMA.OnIRQ = func() {
		m.istat |= 1 << IRQDMA
		m.updateIRQ()
	}
	m.CD.OnIRQ = func() {
		m.istat |= 1 << IRQCDROM
		m.updateIRQ()
	}
	m.Bus.SetIODevice(m)
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
	m.CD.SetDisc(r)
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
	disc := m.Disc
	m.Bus.Reset()
	m.CPU.Reset()
	m.CPU.CP2 = gte.New()
	m.GPU.Reset()
	m.Timers.Reset()
	m.DMA.Reset()
	m.SPU.Reset()
	m.CD.Reset()
	m.CD.SetDisc(disc)
	m.istat = 0
	m.imask = 0
	m.frame = 0
	m.budgetRem = 0
	m.absVC = 0
	m.vcRem = 0
	m.displayFrames = 0
	m.prevDotTicks = 0
	m.prevHBlank = 0
	m.prevVBlank = 0
	m.gpuIRQPrev = false
	m.mdecCount = 0
	m.mdecSum = 0
	m.CPU.SetInterrupt(false)
}

// video returns the active standard's constants.
func (m *Machine) video() videoMode {
	if m.GPU.PAL() {
		return videoPAL
	}
	return videoNTSC
}

// hBlankStartsTo counts hblank pulse starts at
// absolute video positions strictly before t: one
// start per line, at the X2 boundary (or at the line
// start when only the X1 segment exists), and none
// when the programmed range leaves no hblank at all.
func (m *Machine) hBlankStartsTo(t int64) int64 {
	vm := m.video()
	x1, x2 := m.GPU.HDisplayRange()
	if int64(x1) == 0 && int64(x2) >= vm.dotsLine {
		return 0 // the range covers the whole line: no hblank
	}
	start := int64(x2)
	if start >= vm.dotsLine {
		start = 0 // only the [0, X1) segment exists, starting at the line start
	}
	return pulseStartsTo(t, vm.dotsLine, start, true)
}

// vBlankStartsTo counts vblank starts strictly
// before t: one per frame at the Y2 line start, or
// at the frame start when Y2 is beyond the frame but
// a [0, Y1) segment still exists. With Y1 = 0 and Y2
// beyond the frame there is no vblank at all (the
// documented configuration that never interrupts).
func (m *Machine) vBlankStartsTo(t int64) int64 {
	vm := m.video()
	y1, y2 := m.GPU.VDisplayRange()
	if int64(y2) >= vm.lines && int64(y1) == 0 {
		return 0
	}
	start := int64(y2) * vm.dotsLine
	if int64(y2) >= vm.lines {
		start = 0
	}
	return pulseStartsTo(t, vm.lines*vm.dotsLine, start, true)
}

// pulseStartsTo counts pulse starts at positions
// k*period + startOff (k = 0, 1, ...) that are
// strictly less than t. When startOff is 0 the first
// start is at t=0 itself.
func pulseStartsTo(t, period, startOff int64, enabled bool) int64 {
	if !enabled || t <= 0 {
		return 0
	}
	if startOff >= period {
		return 0
	}
	if startOff == 0 {
		return (t + period - 1) / period
	}
	if t <= startOff {
		return 0
	}
	return (t-1-startOff)/period + 1
}

// dotTicksTo counts GPU dotclock ticks strictly
// before absolute video position t, honoring the
// documented per-line floor of fractional dots.
func (m *Machine) dotTicksTo(t int64) int64 {
	vm := m.video()
	div := int64(m.GPU.DotDivider())
	perLine := vm.dotsLine / div
	lines := t / vm.dotsLine
	dot := t % vm.dotsLine
	return lines*perLine + dot/div
}

// beamAt resolves the line and dot position for an
// absolute video position.
func (m *Machine) beamAt(t int64) (line, dot int64) {
	vm := m.video()
	frame := vm.lines * vm.dotsLine
	pos := t % frame
	return pos / vm.dotsLine, pos % vm.dotsLine
}

// inHBlankAt reports hblank for a position from the
// GPU's horizontal display range.
func (m *Machine) inHBlankAt(t int64) bool {
	x1, x2 := m.GPU.HDisplayRange()
	_, dot := m.beamAt(t)
	return dot < int64(x1) || dot >= int64(x2)
}

// inVBlankAt reports vblank for a position from the
// GPU's vertical display range.
func (m *Machine) inVBlankAt(t int64) bool {
	y1, y2 := m.GPU.VDisplayRange()
	line, _ := m.beamAt(t)
	return line < int64(y1) || line >= int64(y2)
}

// InHBlank reports the current beam blanking state.
func (m *Machine) InHBlank() bool { return m.inHBlankAt(m.absVC) }

// InVBlank reports the current beam blanking state.
func (m *Machine) InVBlank() bool { return m.inVBlankAt(m.absVC) }

// DisplayFrames returns the number of display frame
// wraps since reset (263-line or 314-line frames per
// the video standard, distinct from RunFrame calls).
func (m *Machine) DisplayFrames() uint64 { return m.displayFrames }

// IStat returns the interrupt controller pending bits.
func (m *Machine) IStat() uint16 { return m.istat }

// IMask returns the interrupt controller mask.
func (m *Machine) IMask() uint16 { return m.imask }

// updateIRQ drives the CPU external interrupt line
// from the controller state.
func (m *Machine) updateIRQ() {
	m.CPU.SetInterrupt(m.istat&m.imask != 0)
}

// advance moves the peripheral model forward by the
// cycles one CPU instruction consumed: beam position,
// timers (with real dot/hblank/vblank feeds), the
// VBlank and GPU interrupt sources.
func (m *Machine) advance(cycles int) {
	if cycles <= 0 {
		return
	}
	vm := m.video()
	inH := m.inHBlankAt(m.absVC)
	inV := m.inVBlankAt(m.absVC)
	m.vcRem += int64(cycles) * vm.clockNum
	delta := m.vcRem / cpuHz
	m.vcRem -= delta * cpuHz
	oldVC := m.absVC
	m.absVC += delta

	// Display frame wraps.
	frame := vm.lines * vm.dotsLine
	if m.absVC/frame != oldVC/frame {
		m.displayFrames += uint64(m.absVC/frame - oldVC/frame)
	}

	hbStarts := m.hBlankStartsTo(m.absVC)
	vbStarts := m.vBlankStartsTo(m.absVC)
	dotTicks := m.dotTicksTo(m.absVC)
	in := timers.Inputs{
		SysCycles:   cycles,
		DotTicks:    int(dotTicks - m.prevDotTicks),
		HBlankPulse: int(hbStarts - m.prevHBlank),
		VBlankPulse: int(vbStarts - m.prevVBlank),
		InHBlank:    inH,
		InVBlank:    inV,
	}
	if in.DotTicks < 0 {
		in.DotTicks = 0
	}
	m.prevDotTicks = dotTicks
	m.prevHBlank = hbStarts
	m.prevVBlank = vbStarts

	if in.VBlankPulse > 0 {
		m.istat |= 1 << IRQVBlank
	}
	asserted := m.Timers.Advance(in)
	for i := 0; i < timers.Count; i++ {
		if asserted&(1<<uint(i)) != 0 {
			m.istat |= 1 << uint(IRQTMR0+i)
		}
	}
	// GPU interrupt source is edge-latched from the
	// GPUSTAT request bit (GP0(1F) sets it, GP1(02)
	// clears it).
	irq := m.GPU.IRQRequested()
	if irq && !m.gpuIRQPrev {
		m.istat |= 1 << IRQGPU
	}
	m.gpuIRQPrev = irq

	line, _ := m.beamAt(m.absVC)
	m.GPU.LineOdd = line&1 == 1
	m.GPU.Field = int(m.displayFrames & 1)
	m.GPU.OddFrame = m.displayFrames&1 == 1

	m.updateIRQ()
}

// Step executes one CPU instruction, advances the
// peripheral model by its cycles, and returns the
// cycles consumed.
func (m *Machine) Step() int {
	c := m.CPU.Step()
	m.advance(c)
	return c
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

// Framebuffer returns the GPU display area as
// XRGB8888 pixels at its native resolution (see
// DisplayWidth and DisplayHeight for the dimensions
// of the returned slice).
func (m *Machine) Framebuffer() []uint32 {
	fb, _, _ := m.GPU.Framebuffer()
	return fb
}

// DisplayWidth returns the framebuffer width in
// pixels for the current display mode.
func (m *Machine) DisplayWidth() int { return m.GPU.DisplayWidth() }

// DisplayHeight returns the framebuffer height in
// pixels for the current display mode.
func (m *Machine) DisplayHeight() int { return m.GPU.DisplayHeight() }

// SetButtons stores the digital pad state for port 1.
func (m *Machine) SetButtons(mask uint32) { m.buttons = mask }

// Buttons returns the stored pad state.
func (m *Machine) Buttons() uint32 { return m.buttons }

// StateSize returns the exact serialized state size
// for version 2: version byte, CPU, RAM, scratchpad,
// GPU, timers, DMA, interrupt controller, SPU, CD
// controller and the machine misc block.
func (m *Machine) StateSize() int {
	return 1 + r3000a.StateSize + bus.RAMSize + bus.ScratchpadSize +
		gpu.StateSize + timers.StateSize() + dma.StateSize + 4 +
		spu.StateSize + cd.CtrlStateSize + miscSize
}

// Serialize returns the machine state. The disc image
// is not part of the state (it is the loaded media,
// re-supplied by the caller on restore, like the
// other cores' cartridges).
func (m *Machine) Serialize() []byte {
	out := make([]byte, m.StateSize())
	out[0] = stateVersion
	off := 1
	put := func(b []byte) {
		copy(out[off:off+len(b)], b)
		off += len(b)
	}
	put(m.CPU.Serialize())
	put(m.Bus.RAM())
	put(m.Bus.Scratchpad())
	put(m.GPU.Serialize())
	put(m.Timers.Serialize())
	put(m.DMA.Serialize())
	{
		b := make([]byte, 4)
		b[0] = byte(m.istat)
		b[1] = byte(m.istat >> 8)
		b[2] = byte(m.imask)
		b[3] = byte(m.imask >> 8)
		put(b)
	}
	put(m.SPU.Serialize())
	put(m.CD.Serialize())
	{
		b := make([]byte, miscSize)
		put64(b[0:], uint64(m.frame))
		put64(b[8:], uint64(m.budgetRem))
		put32(b[16:], m.buttons)
		put64(b[24:], uint64(m.absVC))
		put64(b[32:], uint64(m.vcRem))
		put64(b[40:], m.displayFrames)
		if m.gpuIRQPrev {
			b[48] = 1
		}
		put32(b[52:], m.mdecCount)
		put32(b[56:], m.mdecSum)
		put64(b[64:], uint64(m.prevDotTicks))
		put64(b[72:], uint64(m.prevHBlank))
		put64(b[80:], uint64(m.prevVBlank))
		put(b)
	}
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
	take := func(n int) []byte {
		b := in[off : off+n]
		off += n
		return b
	}
	if err := m.CPU.Deserialize(take(r3000a.StateSize)); err != nil {
		return err
	}
	copy(m.Bus.RAM(), take(bus.RAMSize))
	copy(m.Bus.Scratchpad(), take(bus.ScratchpadSize))
	m.GPU.Deserialize(take(gpu.StateSize))
	m.Timers.Deserialize(take(timers.StateSize()))
	m.DMA.Deserialize(take(dma.StateSize))
	{
		b := take(4)
		m.istat = uint16(b[0]) | uint16(b[1])<<8
		m.imask = uint16(b[2]) | uint16(b[3])<<8
	}
	m.SPU.Deserialize(take(spu.StateSize))
	if err := m.CD.Deserialize(take(cd.CtrlStateSize)); err != nil {
		return err
	}
	{
		b := take(miscSize)
		m.frame = get64(b[0:])
		m.budgetRem = int64(get64(b[8:]))
		m.buttons = get32(b[16:])
		m.absVC = int64(get64(b[24:]))
		m.vcRem = int64(get64(b[32:]))
		m.displayFrames = get64(b[40:])
		m.gpuIRQPrev = b[48] != 0
		m.mdecCount = get32(b[52:])
		m.mdecSum = get32(b[56:])
		m.prevDotTicks = int64(get64(b[64:]))
		m.prevHBlank = int64(get64(b[72:]))
		m.prevVBlank = int64(get64(b[80:]))
	}
	m.updateIRQ()
	return nil
}

func put32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}
func get32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
func put64(b []byte, v uint64) {
	put32(b, uint32(v))
	put32(b[4:], uint32(v>>32))
}
func get64(b []byte) uint64 {
	return uint64(get32(b)) | uint64(get32(b[4:]))<<32
}
