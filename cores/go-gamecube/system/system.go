// Package system assembles the GameCube machine: the
// Gekko CPU (chips/gekko), 24 MB of MEM1 main memory, the
// hardware register pages, and a disc image staged through
// the disc package.
//
// Slice GCN-1 scope: the CPU runs integer code from a
// staged DOL; the IPL ROM window reads as zeros because
// this project ships no Nintendo IPL bytes.
//
// Slice GCN-2 wires the GX renderer (package gpu) into
// the machine: the CP register window and the GX FIFO
// write port on the 0x0C hardware page route to the GPU,
// Framebuffer presents the GPU's embedded frame buffer,
// and save states grow to version 2 with the full GPU
// state included.
//
// Slice GCN-3 wires the remaining Flipper devices into
// the machine: the DSP (chips/dsp), the audio interface
// (ai), the disc interface (di), the external interface
// (exi), the serial interface (si) and the video
// interface (vi), plus the processor interface (PI)
// interrupt aggregate handled here, and an HLE boot path
// (BootDisc) that reconstructs the documented IPL boot
// state without executing or embedding any IPL code.
// Save states grow to version 3 with every device block
// included.
package system

import (
	"encoding/binary"
	"errors"

	"github.com/davidpatton/itak-gaming/cores/go-gamecube/ai"
	"github.com/davidpatton/itak-gaming/cores/go-gamecube/chips/dsp"
	"github.com/davidpatton/itak-gaming/cores/go-gamecube/chips/gekko"
	"github.com/davidpatton/itak-gaming/cores/go-gamecube/di"
	"github.com/davidpatton/itak-gaming/cores/go-gamecube/disc"
	"github.com/davidpatton/itak-gaming/cores/go-gamecube/exi"
	"github.com/davidpatton/itak-gaming/cores/go-gamecube/gpu"
	"github.com/davidpatton/itak-gaming/cores/go-gamecube/si"
	"github.com/davidpatton/itak-gaming/cores/go-gamecube/vi"
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

const serializeVersion = 3

// StateSize is the whole-machine save state size.
const StateSize = 1 + 4 + gekko.SerializeSize + gpu.SerializeSize + MEM1Size +
	lockedCacheSize + 2*hwPageSize +
	dsp.SerializeSize + ai.SerializeSize + di.SerializeSize +
	exi.SerializeSize + si.SerializeSize + vi.SerializeSize +
	stateTailSize

const stateTailSize = 16 + 8 + 4 + 4 + 1 // pad, started, entry, plus GCN-3: total cycles, DSP remainder, PI INTMSK, booted

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

// PI interrupt cause bits in PI_INTSR and PI_INTMSK.
const (
	piCauseDSP   uint32 = 0x00000001
	piCauseAI    uint32 = 0x00000002
	piCauseEXI   uint32 = 0x00000004
	piCauseSI    uint32 = 0x00000008
	piCauseDI    uint32 = 0x00000010
	piCauseVI    uint32 = 0x00000020
	piCauseGX    uint32 = 0x00000040 // reserved: the GPU exposes no PE interrupt output in this slice, so this bit is never raised
	piCauseError uint32 = 0x00000080 // never raised in this slice
)

// MMIO window identifiers on the 0x0C hardware page.
const (
	winCP = iota
	winVI
	winPI
	winDSP
	winDI
	winSI
	winEXI
	winAI
	winCount
)

// mmioLatch is the per-window byte latch used to assemble
// a 16-bit device register from its two bus bytes. The
// latch only ever lives inside one bus write transaction;
// save states are taken between instructions, never here,
// so latch state is not serialized.
type mmioLatch struct {
	val uint8
	ok  bool
}

// Machine is the assembled GameCube.
type Machine struct {
	cpu *gekko.CPU
	gpu *gpu.GPU

	dsp *dsp.DSP
	ai  *ai.AI
	di  *di.DI
	exi *exi.EXI
	si  *si.SI
	vi  *vi.VI

	mem1   []byte
	locked []byte
	hw0C   []byte
	hw0D   []byte

	fb      []uint32
	pad     PadState
	started bool
	booted  bool
	entry   uint32

	loaded *disc.Image

	totalCycles uint64
	dspRem      uint32 // DSP cycle remainder accumulator, 0..5
	piINTMSK    uint32
	piRaw       [16]uint32 // stored PI words other than INTSR and INTMSK

	latches [winCount]mmioLatch
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

// physMem adapts MEM1 to the byte interface the DSP, AI,
// DI and EXI devices use for DMA. Addresses are physical,
// zero-based MEM1 addresses; anything outside MEM1 reads
// as zero and ignores writes.
type physMem struct{ m *Machine }

func (a physMem) Read8(addr uint32) uint8 {
	if addr < MEM1Size {
		return a.m.mem1[addr]
	}
	return 0
}

func (a physMem) Write8(addr uint32, v uint8) {
	if addr < MEM1Size {
		a.m.mem1[addr] = v
	}
}

// diSource adapts the loaded disc image to the DI Source
// interface. It resolves the image at call time, so a disc
// loaded after the DI device was constructed is the disc
// the drive reads.
type diSource struct{ m *Machine }

func (a diSource) Read(off uint32, dst []byte) int {
	if a.m.loaded == nil {
		return 0
	}
	return a.m.loaded.ReadAt(off, dst)
}

func (a diSource) Header() (sysID byte, game, company string, discNo, version byte) {
	img := a.m.loaded
	if img == nil {
		return 0, "", "", 0, 0
	}
	return img.SystemID, img.GameCode, img.CompanyCode, img.DiscNumber, img.Version
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
	mem := physMem{m}
	m.dsp = dsp.New(mem)
	m.ai = ai.New(mem)
	m.ai.OnAIDInt = m.dsp.SetAIDInt
	m.di = di.New(diSource{m}, mem)
	m.exi = exi.New(mem)
	m.si = si.New(m.siPads)
	m.si.Connected = [4]bool{true, false, false, false}
	m.vi = vi.New()
	return m
}

// siPads is the SI pad provider: channel 0 reports the
// machine pad state, channels 1 to 3 report zero pads.
func (m *Machine) siPads() [4]si.Pad {
	var out [4]si.Pad
	out[0] = si.Pad{
		Buttons:  m.pad.Buttons,
		TriggerL: m.pad.TriggerL,
		TriggerR: m.pad.TriggerR,
		StickX:   m.pad.StickX,
		StickY:   m.pad.StickY,
		CStickX:  m.pad.CStickX,
		CStickY:  m.pad.CStickY,
	}
	return out
}

// CPU exposes the processor for test harnesses and tools.
func (m *Machine) CPU() *gekko.CPU { return m.cpu }

// GPU exposes the GX renderer for test harnesses and
// tools.
func (m *Machine) GPU() *gpu.GPU { return m.gpu }

// DSP exposes the DSP for test harnesses and tools (for
// example to upload IRAM microcode before a boot).
func (m *Machine) DSP() *dsp.DSP { return m.dsp }

// AudioBuffer drains the AI output buffer: interleaved
// stereo samples at AudioRate.
func (m *Machine) AudioBuffer() []int16 { return m.ai.Drain() }

// AudioRate returns the AI sample rate in Hz.
func (m *Machine) AudioRate() int { return m.ai.Rate() }

// CardImage returns the live backing image of an EXI
// memory card slot (0 is slot A, 1 is slot B). Card images
// are persistent media and are not part of save states.
func (m *Machine) CardImage(slot int) []byte { return m.exi.CardImage(slot) }

// resetDevices resets every GCN-3 device and the cycle
// accounting, deterministically, as part of LoadDisc.
func (m *Machine) resetDevices() {
	m.dsp.Reset()
	m.ai.Reset()
	m.ai.OnAIDInt = m.dsp.SetAIDInt
	m.di.Reset()
	m.exi.Reset()
	m.si.Reset()
	m.si.Connected = [4]bool{true, false, false, false}
	m.vi.Reset()
	m.totalCycles = 0
	m.dspRem = 0
	m.piINTMSK = 0
	m.piRaw = [16]uint32{}
	m.latches = [winCount]mmioLatch{}
	m.booted = false
}

// LoadDisc stages a parsed disc image: the DOL sections
// are copied to their load addresses, the BSS is zeroed,
// and the CPU is reset to the DOL entry point, halted.
// Call Start to run it, or BootDisc for the HLE boot path.
func (m *Machine) LoadDisc(img *disc.Image) error {
	dol, err := img.DOL()
	if err != nil {
		m.loaded = nil
		return err
	}
	m.loaded = img
	for i := range m.mem1 {
		m.mem1[i] = 0
	}
	for i := range m.locked {
		m.locked[i] = 0
	}
	m.resetDevices()
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
	m.booted = false
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

// intSR computes the PI_INTSR aggregate from the device
// interrupt outputs.
func (m *Machine) intSR() uint32 {
	var sr uint32
	if m.dsp != nil && m.dsp.IRQAsserted() {
		sr |= piCauseDSP
	}
	if m.ai != nil && m.ai.IRQAsserted() {
		sr |= piCauseAI
	}
	if m.exi != nil && m.exi.IRQAsserted() {
		sr |= piCauseEXI
	}
	if m.si != nil && m.si.IRQAsserted() {
		sr |= piCauseSI
	}
	if m.di != nil && m.di.IRQAsserted() {
		sr |= piCauseDI
	}
	if m.vi != nil && m.vi.Pending() {
		sr |= piCauseVI
	}
	// piCauseGX is reserved and never raised in this slice:
	// the GPU exposes no PE interrupt output. piCauseError
	// is never raised either.
	return sr
}

// refreshIRQ drives the CPU external interrupt line from
// the PI interrupt aggregate and mask.
func (m *Machine) refreshIRQ() {
	if m.cpu == nil {
		return
	}
	m.cpu.SetIRQ(m.intSR()&m.piINTMSK != 0)
}

// piRead16 reads one 16-bit lane of a PI register. PI
// registers are 32-bit: the lane at the even offset is
// the high half, the next lane the low half.
func (m *Machine) piRead16(off uint32) uint16 {
	wordOff := off &^ 2
	var v uint32
	switch wordOff {
	case 0x00: // PI_INTSR: computed, read-only
		v = m.intSR()
	case 0x04: // PI_INTMSK
		v = m.piINTMSK
	default:
		if wordOff/4 < uint32(len(m.piRaw)) {
			v = m.piRaw[wordOff/4]
		}
	}
	if off&2 == 0 {
		return uint16(v >> 16)
	}
	return uint16(v)
}

// piWrite16 writes one 16-bit lane of a PI register.
// Writes to PI_INTSR are ignored.
func (m *Machine) piWrite16(off uint32, v uint16) {
	wordOff := off &^ 2
	low := off&2 != 0
	merge := func(dst *uint32) {
		if low {
			*dst = *dst&0xFFFF0000 | uint32(v)
		} else {
			*dst = *dst&0x0000FFFF | uint32(v)<<16
		}
	}
	switch wordOff {
	case 0x00: // PI_INTSR is read-only
	case 0x04:
		merge(&m.piINTMSK)
	default:
		if wordOff/4 < uint32(len(m.piRaw)) {
			merge(&m.piRaw[wordOff/4])
		}
	}
}

// RunCycles steps the CPU for up to n cycles (fewer if it
// is halted). The DSP, AI, EXI and VI devices advance with
// the CPU: the DSP at its 81 MHz rate (one DSP cycle per
// six CPU cycles, with the remainder carried), the others
// per CPU cycle. If the CPU is halted the devices do not
// advance.
func (m *Machine) RunCycles(n int) {
	for n > 0 {
		s := m.cpu.Step()
		if s == 0 {
			return
		}
		n -= s
		m.totalCycles += uint64(s)
		acc := int(m.dspRem) + s
		m.dsp.Step(acc / 6)
		m.dspRem = uint32(acc % 6)
		m.ai.Advance(s)
		m.exi.Advance(s)
		m.vi.Advance(s)
	}
	m.refreshIRQ()
}

// RunFrame consumes one frame of CPU cycles and polls the
// SI controllers once at the end of the frame.
func (m *Machine) RunFrame() {
	m.RunCycles(CyclesPerFrame)
	m.si.PollFrame()
}

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

// gxWindow maps an address in the Flipper register page
// to its page offset. The page is decoded at its physical
// base (0x0C000000) and at the cached (0x8C000000) and
// uncached (0xCC000000) aliases, which is where GameCube
// software actually addresses the device registers and
// the GX FIFO write port.
func (m *Machine) gxWindow(addr uint32) (uint32, bool) {
	for _, base := range []uint32{0x0C000000, 0x8C000000, 0xCC000000} {
		if addr >= base && addr < base+hwPageSize {
			return addr - base, true
		}
	}
	return 0, false
}

// Offsets inside the Flipper page: the CP MMIO register
// window occupies 0x0000-0x007F (16-bit registers) and the
// GX FIFO write port occupies 0x8000-0x80FF. The GCN-3
// device windows are VI 0x2000, PI 0x3000, DSP 0x5000,
// DI 0x6000, SI 0x6400, EXI 0x6800 and AI 0x6C00. Every
// other offset stays plain stored state in the hw0C page.
const (
	gxFIFOBase = 0x8000
	gxFIFOEnd  = 0x8100
)

// mmioWindowFor maps a Flipper page offset to its device
// window, the window base, and whether the offset is in a
// device window at all.
func mmioWindowFor(off uint32) (win int, base uint32, ok bool) {
	switch {
	case off >= 0x2000 && off < 0x2060:
		return winVI, 0x2000, true
	case off >= 0x3000 && off < 0x3040:
		return winPI, 0x3000, true
	case off >= 0x5000 && off < 0x5030:
		return winDSP, 0x5000, true
	case off >= 0x6000 && off < 0x6024:
		return winDI, 0x6000, true
	case off >= 0x6400 && off < 0x64C0:
		return winSI, 0x6400, true
	case off >= 0x6800 && off < 0x683C:
		return winEXI, 0x6800, true
	case off >= 0x6C00 && off < 0x6C40:
		return winAI, 0x6C00, true
	}
	return 0, 0, false
}

// devRead16 reads one 16-bit device register lane in the
// given window at a window-relative lane offset.
func (m *Machine) devRead16(win int, rel uint32) uint16 {
	switch win {
	case winVI:
		return m.vi.Read16(rel)
	case winPI:
		return m.piRead16(rel)
	case winDSP:
		return m.dsp.Read16(rel)
	case winDI:
		return m.di.Read16(rel)
	case winSI:
		return m.si.Read16(rel)
	case winEXI:
		return m.exi.Read16(rel)
	case winAI:
		return m.ai.Read16(rel)
	}
	return 0
}

// devWrite16 writes one 16-bit device register lane in
// the given window at a window-relative lane offset.
func (m *Machine) devWrite16(win int, rel uint32, v uint16) {
	switch win {
	case winVI:
		m.vi.Write16(rel, v)
	case winPI:
		m.piWrite16(rel, v)
	case winDSP:
		m.dsp.Write16(rel, v)
	case winDI:
		m.di.Write16(rel, v)
	case winSI:
		m.si.Write16(rel, v)
	case winEXI:
		m.exi.Write16(rel, v)
	case winAI:
		m.ai.Write16(rel, v)
	}
}

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
			if win, base, isDev := mmioWindowFor(off); isDev {
				rel := off - base
				v := m.devRead16(win, rel&^1)
				m.refreshIRQ()
				if off&1 == 0 {
					return uint8(v >> 8)
				}
				return uint8(v)
			}
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
			// from its two bytes, using the CP latch.
			lt := &m.latches[winCP]
			if off&1 == 0 {
				lt.val = v
				lt.ok = true
			} else if lt.ok {
				m.gpu.WriteMMIO(off-1, uint16(lt.val)<<8|uint16(v))
				lt.ok = false
			}
		case off >= gxFIFOBase && off < gxFIFOEnd:
			// Every byte stored anywhere in the FIFO port
			// window pushes one byte into the GX FIFO, in
			// bus order.
			m.gpu.WriteFIFO([]byte{v})
		default:
			if win, base, isDev := mmioWindowFor(off); isDev {
				// Device window: assemble the 16-bit
				// register from its two bytes, using this
				// window's own latch so alternating windows
				// cannot corrupt a pair, and call the
				// device on the odd byte.
				rel := off - base
				lt := &m.latches[win]
				if off&1 == 0 {
					lt.val = v
					lt.ok = true
				} else if lt.ok {
					m.devWrite16(win, rel-1, uint16(lt.val)<<8|uint16(v))
					lt.ok = false
				}
				m.refreshIRQ()
				return
			}
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

// ErrNoDisc reports a BootDisc call with no GameCube disc
// loaded.
var ErrNoDisc = errors.New("system: no disc loaded")

// physOf converts a DOL load address to its MEM1 physical
// address through the standard windows.
func physOf(ea uint32) uint32 {
	switch {
	case ea >= 0x80000000 && ea < 0x80000000+MEM1Size:
		return ea - 0x80000000
	case ea >= 0xC0000000 && ea < 0xC0000000+MEM1Size:
		return ea - 0xC0000000
	default:
		return ea
	}
}

func align32(v uint32) uint32 { return (v + 31) &^ 31 }

// BootDisc performs the HLE boot for the disc loaded by
// LoadDisc. LoadDisc must be called first: it stages the
// DOL and resets the machine, and BootDisc then installs
// the boot state the IPL would have left behind (the FST
// in MEM1, the boot info block, exception vectors holding
// a branch to self, BATs and segment registers covering
// MEM1, translation and the FPU on) and starts the CPU at
// the DOL entry point. No IPL or apploader code is
// executed or embedded; the layout follows published
// boot info facts only.
func (m *Machine) BootDisc() error {
	if m.loaded == nil {
		return ErrNoDisc
	}
	img := m.loaded
	dol, err := img.DOL()
	if err != nil {
		return err
	}

	// The FST goes immediately after the highest DOL
	// section end or BSS end, aligned up to 32 bytes.
	var top uint32
	for _, s := range dol.Sections {
		if end := physOf(s.Address) + s.Size; end > top {
			top = end
		}
	}
	if end := physOf(dol.BSSAddress) + dol.BSSSize; end > top {
		top = end
	}
	fstPhys := align32(top)
	fstEff := 0x80000000 | fstPhys
	fst := make([]byte, img.FSTSize)
	img.ReadAt(img.FSTOffset, fst)
	for i, b := range fst {
		if fstPhys+uint32(i) < MEM1Size {
			m.mem1[fstPhys+uint32(i)] = b
		}
	}
	arenaLo := 0x80000000 | align32(fstPhys+img.FSTSize)
	const arenaHi uint32 = 0x81800000
	const stackPtr uint32 = 0x817FFFE0

	// Boot info block at physical MEM1 offsets.
	m.Write32(0x00, 1)          // console type: retail GameCube
	m.Write32(0x20, 0x0D15EA5E) // BS2 executed magic
	m.Write32(0x28, MEM1Size)   // simulated memory size
	m.Write32(0x2C, fstPhys)    // FST start, physical
	m.Write32(0x30, arenaLo)    // OS arena low, effective cached
	m.Write32(0x34, arenaHi)    // OS arena high
	m.Write32(0x38, img.FSTSize)

	// Exception vectors: a branch to self in every slot
	// from 0x100 through 0x1F00.
	for off := uint32(0x100); off <= 0x1F00; off += 0x100 {
		m.Write32(off, 0x48000000)
	}
	// The return trampoline LR points at.
	m.Write32(0x2000, 0x48000000)

	cpu := m.cpu
	for i := range cpu.SR {
		cpu.SR[i] = 0x80000000
	}
	cpu.SetSPR(gekko.SPRIBAT0U+0, 0x800003FF) // IBAT0U
	cpu.SetSPR(gekko.SPRIBAT0U+1, 0x00000002) // IBAT0L
	cpu.SetSPR(gekko.SPRIBAT0U+2, 0x000003FF) // IBAT1U
	cpu.SetSPR(gekko.SPRIBAT0U+3, 0x00000002) // IBAT1L
	cpu.SetSPR(gekko.SPRDBAT0U+0, 0x800003FF) // DBAT0U
	cpu.SetSPR(gekko.SPRDBAT0U+1, 0x00000002) // DBAT0L
	cpu.SetSPR(gekko.SPRDBAT0U+2, 0x000003FF) // DBAT1U
	cpu.SetSPR(gekko.SPRDBAT0U+3, 0x00000002) // DBAT1L
	cpu.MSR = gekko.MSRFP | gekko.MSRIR | gekko.MSRDR
	cpu.GPR[1] = stackPtr
	cpu.GPR[3] = fstEff
	cpu.GPR[4] = img.FSTSize
	cpu.LR = 0x80002000
	cpu.PC = dol.Entry
	cpu.Halted = false
	m.started = true
	m.booted = true
	m.entry = dol.Entry
	return nil
}

// Booted reports whether BootDisc has run.
func (m *Machine) Booted() bool { return m.booted }

// Serialize writes the whole-machine state: version byte,
// the CPU state, the GPU state, MEM1, the locked cache
// window, both hardware pages, the GCN-3 device blocks
// (DSP, AI, DI, EXI, SI, VI), and the machine tail (pad,
// started, entry, and the GCN-3 extension: total cycles,
// DSP remainder, PI INTMSK, booted flag).
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
	off += copy(out[off:], m.dsp.Serialize())
	off += copy(out[off:], m.ai.Serialize())
	off += copy(out[off:], m.di.Serialize())
	off += copy(out[off:], m.exi.Serialize())
	off += copy(out[off:], m.si.Serialize())
	off += copy(out[off:], m.vi.Serialize())
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
	tail := off + 16
	binary.BigEndian.PutUint64(out[tail:], m.totalCycles)
	binary.BigEndian.PutUint32(out[tail+8:], m.dspRem)
	binary.BigEndian.PutUint32(out[tail+12:], m.piINTMSK)
	if m.booted {
		out[tail+16] = 1
	}
	if tail+17 != StateSize {
		panic("system: serialize size mismatch")
	}
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
	blocks := []struct {
		size int
		load func([]byte) error
	}{
		{dsp.SerializeSize, m.dsp.Deserialize},
		{ai.SerializeSize, m.ai.Deserialize},
		{di.SerializeSize, m.di.Deserialize},
		{exi.SerializeSize, m.exi.Deserialize},
		{si.SerializeSize, m.si.Deserialize},
		{vi.SerializeSize, m.vi.Deserialize},
	}
	for _, b := range blocks {
		if err := b.load(in[off : off+b.size]); err != nil {
			return ErrBadState
		}
		off += b.size
	}
	// The AI Deserialize may report an AIINT change through
	// OnAIDInt; make sure the wiring still points at the DSP.
	m.ai.OnAIDInt = m.dsp.SetAIDInt
	m.pad.Buttons = binary.BigEndian.Uint16(in[off:])
	m.pad.TriggerL = in[off+2]
	m.pad.TriggerR = in[off+3]
	m.pad.StickX = in[off+4]
	m.pad.StickY = in[off+5]
	m.pad.CStickX = in[off+6]
	m.pad.CStickY = in[off+7]
	m.started = in[off+8] != 0
	m.entry = binary.BigEndian.Uint32(in[off+12:])
	tail := off + 16
	m.totalCycles = binary.BigEndian.Uint64(in[tail:])
	m.dspRem = binary.BigEndian.Uint32(in[tail+8:])
	m.piINTMSK = binary.BigEndian.Uint32(in[tail+12:])
	m.booted = in[tail+16] != 0
	m.cpu.Halted = !m.started
	return nil
}
