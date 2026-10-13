// Package system is the Nintendo DS machine: the two CPUs on
// the dual bus, the 2D video engines, the 3D geometry and
// rendering engine, the sound unit, the peripherals, full
// direct boot, scanline-stepped execution, and save states.
//
// Boot approach (deliberately no BIOS): no DS BIOS or firmware
// image exists in this module and none is required. LoadROM
// performs the documented direct-boot initialization: it
// stages both binaries at their header load addresses, writes
// the boot block games read in main RAM, installs the entry
// register state (stacks per the direct-boot table, CPSR =
// system mode, r12/r13/r14 at the entry contract), and leaves
// the CPUs halted until Start. The firmware the ARM7 can read
// over SPI is a generated minimal image (periph package), not
// a dump.
package system

import (
	"github.com/davidpatton/itak-gaming/cores/go-nds/audio"
	"github.com/davidpatton/itak-gaming/cores/go-nds/bus"
	"github.com/davidpatton/itak-gaming/cores/go-nds/cart"
	"github.com/davidpatton/itak-gaming/cores/go-nds/chips/arm7"
	"github.com/davidpatton/itak-gaming/cores/go-nds/chips/arm9"
	"github.com/davidpatton/itak-gaming/cores/go-nds/chips/armcore"
	"github.com/davidpatton/itak-gaming/cores/go-nds/gx"
	"github.com/davidpatton/itak-gaming/cores/go-nds/periph"
	"github.com/davidpatton/itak-gaming/cores/go-nds/video"
)

// Frame budgets in CPU cycles. The ARM9 runs at 33.5 MHz and
// the ARM7 at half that; one 60 Hz frame is 560,190 ARM9 cycles
// and 280,095 ARM7 cycles. The line split (video package)
// reproduces these totals exactly over 263 lines.
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
	Bus    *bus.Bus
	CPU9   *armcore.CPU
	CPU7   *armcore.CPU
	Periph *periph.Set
	Video  *video.Video
	GX     *gx.GX
	Audio  *audio.Sound

	header *cart.Header
	rom    []byte

	started9 bool
	started7 bool

	buttons uint32

	touchX    uint16
	touchY    uint16
	touchDown bool

	micSamples []int16
	micIdx     int
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
	m.Video = video.New(b)
	m.Periph = periph.New(b,
		func() []byte { return m.rom },
		func() uint32 { return m.buttons },
		func() (uint16, uint16, bool) { return m.touchX, m.touchY, m.touchDown })
	m.GX = gx.New(b)
	m.Audio = audio.New(audioMem{v: b.ARM7()})
	m.Video.Line3D = m.GX.Line3D
	m.Periph.SetMicrophoneSource(m.micNext)
	b.SetIOHook(m)
	return m
}

// audioMem adapts the ARM7 bus view to the sound unit's
// memory interface.
type audioMem struct{ v bus.View }

func (a audioMem) Read8(addr uint32) uint8     { return uint8(a.v.Read(addr, 1)) }
func (a audioMem) Read16(addr uint32) uint16   { return uint16(a.v.Read(addr, 2)) }
func (a audioMem) Write8(addr uint32, v uint8) { a.v.Write(addr, 1, uint32(v)) }
func (a audioMem) Write16(addr uint32, v uint16) {
	a.v.Write(addr, 2, uint32(v))
}

// LoadROM parses the cartridge header and performs the full
// direct-boot initialization (see boot.go), leaving both CPUs
// halted with their entry state installed. Call Start (or
// Start9/Start7) to run them.
func (m *Machine) LoadROM(rom []byte) error {
	h, err := cart.Parse(rom)
	if err != nil {
		return err
	}
	m.header = h
	m.rom = rom
	m.started9 = false
	m.started7 = false
	m.directBoot()
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

// RunFrame advances the machine by one frame, stepped at
// scanline granularity: 263 lines, each running the visible
// cycle chunk, the hblank point (HBlank flags, DMA triggers),
// and the hblank cycle chunk. Video renders per line from the
// register state at line start. VBlank begins at line 192,
// which also latches the affine reference points and fires the
// VBlank DMA triggers and interrupt sources.
func (m *Machine) RunFrame() {
	m.Periph.TriggerDMA(periph.TrigGX)
	m.Video.BeginFrame()
	for line := 0; line < video.LinesTotal; line++ {
		m.Video.SetLine(line)
		if line == m.Video.LYC() && m.Video.IRQEnabled(2) {
			m.Periph.Raise(true, periph.IRQVCount)
			m.Periph.Raise(false, periph.IRQVCount)
		}
		if line == video.Height {
			if m.Video.IRQEnabled(0) {
				m.Periph.Raise(true, periph.IRQVBlank)
				m.Periph.Raise(false, periph.IRQVBlank)
			}
			// The geometry engine's deferred swap and render
			// happen at the VBlank edge.
			m.GX.OnVBlank()
			m.Periph.TriggerDMA(periph.TrigVBlank)
			m.Video.LatchAffine()
		}
		if line < video.Height {
			m.Periph.TriggerDMA(periph.TrigDisplayLine)
			m.Video.RenderLine(line)
			m.Video.CaptureLine(line)
		}
		m.runChunk(video.CyclesLineA9, video.CyclesLineA7)
		m.Video.SetHBlank(true)
		if line < video.Height && m.Video.IRQEnabled(1) {
			m.Periph.Raise(true, periph.IRQHBlank)
			m.Periph.Raise(false, periph.IRQHBlank)
		}
		if line < video.Height {
			m.Periph.TriggerDMA(periph.TrigHBlank)
		}
		m.runChunk(video.CyclesLineB9, video.CyclesLineB7)
		m.Video.SetHBlank(false)
		if line < video.Height {
			m.Video.AdvanceAffine()
		}
	}
	m.Periph.FrameTick()
}

// runChunk steps each started CPU for up to its chunk budget,
// then advances its timers by the cycles actually executed
// and evaluates the keypad interrupt. Each CPU's IRQ line is
// resampled from the interrupt controller after every step,
// so a handler that acknowledges its interrupt source
// deasserts the line at the next instruction boundary instead
// of re-entering until the chunk ends.
// irqGXFIFO is the ARM9 geometry FIFO interrupt bit (21); it
// is level-triggered by the GXSTAT FIFO IRQ mode, so the
// system re-asserts it whenever the condition holds.
const irqGXFIFO = 21

func (m *Machine) runChunk(n9, n7 int) {
	if m.GX.FIFOIRQCondition() {
		m.Periph.Raise(true, irqGXFIFO)
	}
	if m.started9 {
		m.CPU9.IRQLine = m.Periph.PendingIRQ(true)
		n := 0
		for n < n9 {
			n += m.CPU9.Step()
			m.CPU9.IRQLine = m.Periph.PendingIRQ(true)
		}
		m.Periph.AdvanceTimers(true, n)
		m.evalKeyIRQ(true, 0x132)
	}
	if m.started7 {
		m.CPU7.IRQLine = m.Periph.PendingIRQ(false)
		n := 0
		for n < n7 {
			n += m.CPU7.Step()
			m.CPU7.IRQLine = m.Periph.PendingIRQ(false)
		}
		m.Periph.AdvanceTimers(false, n)
		m.Audio.Advance(uint64(n))
		m.evalKeyIRQ(false, 0x132)
	} else {
		// The sound unit is free-running even while the
		// ARM7 is halted.
		m.Audio.Advance(uint64(n7))
	}
}

// evalKeyIRQ evaluates a side's KEYCNT condition against the
// button state and raises the keypad interrupt on a match.
func (m *Machine) evalKeyIRQ(nine bool, off int) {
	io := m.Bus.IO7
	if nine {
		io = m.Bus.IO9
	}
	kc := uint32(io[off]) | uint32(io[off+1])<<8
	if kc&(1<<14) == 0 {
		return
	}
	sel := kc & 0x3FF
	pressed := m.buttons & 0x3FF
	match := false
	if kc&(1<<15) != 0 {
		match = sel != 0 && pressed&sel == sel
	} else {
		match = pressed&sel != 0
	}
	if match {
		m.Periph.Raise(nine, periph.IRQKeys)
	}
}

// Framebuffer returns the top and bottom screen buffers in
// POWCNT1 assignment order: with the swap bit clear engine A
// is the top screen; set, engine A is the bottom screen.
func (m *Machine) Framebuffer() (top, bottom []uint32) {
	if m.Video.Swapped() {
		return m.Video.B, m.Video.A
	}
	return m.Video.A, m.Video.B
}

// SetButtons stores the DS button state: bits 0-9 are A, B,
// Select, Start, Right, Left, Up, Down, R, L in the KEYINPUT
// order, bits 10-11 are X and Y.
func (m *Machine) SetButtons(mask uint32) { m.buttons = mask }

// Buttons returns the stored button state.
func (m *Machine) Buttons() uint32 { return m.buttons }

// SetTouch stores the touchscreen state (pixel coordinates and
// pen-down), feeding both the SPI touchscreen device and the
// ARM7 pen-down interrupt.
func (m *Machine) SetTouch(x, y uint16, down bool) {
	m.touchX, m.touchY, m.touchDown = x, y, down
	m.Periph.SetPenDown(down)
}

// Touch returns the stored touchscreen state.
func (m *Machine) Touch() (x, y uint16, down bool) {
	return m.touchX, m.touchY, m.touchDown
}

// SaveRAM returns the cartridge save memory (the AUX SPI
// EEPROM backing, 512 bytes in this slice).
func (m *Machine) SaveRAM() []byte { return m.Periph.SaveRAM() }

// GXFIFOCount returns the number of 32-bit words written to
// the geometry FIFO port (the DS-3 geometry engine drains
// them for real; see the gx package).
func (m *Machine) GXFIFOCount() uint32 { return m.GX.FIFOWords }

// AudioSamples drains the sound unit's stereo output buffer
// (interleaved L,R int16 at 32,768 Hz).
func (m *Machine) AudioSamples() []int16 { return m.Audio.Drain() }

// SetMicrophone installs the microphone sample stream. The
// ARM7 reads samples through the SPI touchscreen ADC aux
// channel, one sample per read; with no stream installed (or
// once it is exhausted) the ADC returns its idle center
// value.
func (m *Machine) SetMicrophone(samples []int16) {
	m.micSamples = samples
	m.micIdx = 0
}

// micNext consumes one microphone sample for the SPI path.
func (m *Machine) micNext() (int16, bool) {
	if m.micIdx >= len(m.micSamples) {
		return 0, false
	}
	v := m.micSamples[m.micIdx]
	m.micIdx++
	return v, true
}

// stateVersion is the save-state format version. Version 3
// adds the geometry engine and sound unit blocks; version 2
// states are rejected.
const stateVersion = 3

// trailerSize is the fixed machine-state block after the
// device states.
const trailerSize = 32

// StateSize is the exact serialized size of a machine state.
const StateSize = 1 + bus.StateSize + 2*armcore.StateSize +
	periph.StateSize + video.StateSize + gx.StateSize +
	audio.StateSize + trailerSize

// Serialize writes the full machine state. The cartridge
// image itself is not part of the state; Deserialize expects
// a machine that already loaded the same ROM.
func (m *Machine) Serialize() []byte {
	out := make([]byte, StateSize)
	out[0] = stateVersion
	n := 1
	n += m.Bus.Serialize(out[n:])
	n += m.CPU9.Serialize(out[n:])
	n += m.CPU7.Serialize(out[n:])
	n += m.Periph.Serialize(out[n:])
	n += m.Video.Serialize(out[n:])
	n += m.GX.Serialize(out[n:])
	n += m.Audio.Serialize(out[n:])
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
	put32(16, uint32(m.micIdx))
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
	n += m.Periph.Deserialize(src[n:])
	n += m.Video.Deserialize(src[n:])
	n += m.GX.Deserialize(src[n:])
	n += m.Audio.Deserialize(src[n:])
	t := src[n:]
	m.started9 = t[0]&1 != 0
	m.started7 = t[0]&2 != 0
	m.buttons = uint32(t[4]) | uint32(t[5])<<8 | uint32(t[6])<<16 | uint32(t[7])<<24
	m.touchX = uint16(t[8]) | uint16(t[9])<<8
	m.touchY = uint16(t[10]) | uint16(t[11])<<8
	m.touchDown = t[12] != 0
	m.micIdx = int(uint32(t[16]) | uint32(t[17])<<8 | uint32(t[18])<<16 | uint32(t[19])<<24)
	return true
}
