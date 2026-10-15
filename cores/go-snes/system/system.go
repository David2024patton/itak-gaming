// Package system is the whole SNES machine: the W65C816 CPU
// and the cartridge map from SNES-1, plus the PPU, the DMA
// and HDMA engines, the CPU-side registers ($4200-$421F),
// joypads, the WRAM port, the APU (the SPC700/SMP/S-DSP
// audio unit from SNES-3, reached through the mailboxes at
// $2140-$2143), and the master-clock line timing that ties
// them together.
//
// The APU runs on its own 1.024 MHz oscillator, independent
// of the 21.477272 MHz NTSC master clock. The machine
// advances it by exact ratio accumulation: every master
// clock delta is multiplied by 1024000 and divided by
// 21477272, with the remainder carried in apuRem, so the
// long-term SPC rate is exact and save states resume the
// phase bit-exactly (apuRem is part of the system block).
//
// Timing model: the CPU reports consumed master clocks
// (21477272 per second on NTSC). The machine advances in
// whole instructions; after each instruction the elapsed
// clocks are pushed through the line machine. One line is
// 1364 master clocks (341 dots of 4 clocks); a frame is
// 262 lines numbered 0 to 261, where line 0 is the
// pre-render line (the last line of vertical blank), lines
// 1 to 224 (or 239 in overscan mode) are the picture, and
// vertical blank starts on entering line 225 (240). On odd
// frames while the display is enabled, line 0 is four
// clocks shorter, the documented dot-340 quirk. Interlace
// (SETINI bit 0) alternates the frame between 263 and 262
// lines with the extra line in vblank, and the field flag
// toggles per frame.
//
// Events are processed at line granularity: when the clock
// crosses into a new line, the machine renders the display
// line that just finished, runs HDMA for the line being
// entered, and raises the vblank and NMI flags at the
// vblank boundary. Position-based events inside a line
// (H-IRQ crossing, hblank) are evaluated from the dot
// position at instruction granularity, which is the
// accuracy limit of this slice and is documented in the
// README.
package system

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"

	"github.com/davidpatton/itak-gaming/cores/go-snes/apu"
	"github.com/davidpatton/itak-gaming/cores/go-snes/cart"
	"github.com/davidpatton/itak-gaming/cores/go-snes/chips/w65c816"
	"github.com/davidpatton/itak-gaming/cores/go-snes/ppu"
)

// Buttons are reported to SetButtons as a bitmask in
// serial-read order: bit 0 B, 1 Y, 2 Select, 3 Start,
// 4 Up, 5 Down, 6 Left, 7 Right, 8 A, 9 X, 10 L, 11 R.
const (
	ButtonB      = 1 << 0
	ButtonY      = 1 << 1
	ButtonSelect = 1 << 2
	ButtonStart  = 1 << 3
	ButtonUp     = 1 << 4
	ButtonDown   = 1 << 5
	ButtonLeft   = 1 << 6
	ButtonRight  = 1 << 7
	ButtonA      = 1 << 8
	ButtonX      = 1 << 9
	ButtonL      = 1 << 10
	ButtonR      = 1 << 11
)

// Width and Height are the framebuffer dimensions.
const (
	Width  = ppu.Width
	Height = ppu.Height
)

const (
	clocksPerLine = 1364
)

// dmaChannel holds one DMA/HDMA channel's register file
// and HDMA runtime state.
type dmaChannel struct {
	dmap uint8 // $43n0 transfer parameters
	bbad uint8 // $43n1 B-bus address offset
	a1tl uint8 // $43n2 A address low
	a1th uint8 // $43n3 A address high
	a1b  uint8 // $43n4 A bank
	dasl uint8 // $43n5 count/indirect low
	dash uint8 // $43n6 count/indirect high
	dasb uint8 // $43n7 indirect bank
	a2al uint8 // $43n8 HDMA table address low
	a2ah uint8 // $43n9 HDMA table address high
	ntlr uint8 // $43nA line counter readback
	mirr uint8 // $43nB/$43nF last byte readback

	// HDMA runtime state.
	hdmaInit   bool
	hdmaOff    bool
	hdmaLeft   int
	hdmaRepeat bool
	hdmaData   [4]byte
}

// System is one powered-on SNES.
type System struct {
	CPU  *w65c816.CPU
	Cart *cart.Cart
	Map  *cart.Map

	ppu *ppu.PPU
	apu *apu.APU

	// apuRem is the carried remainder of the master to
	// SPC clock ratio accumulation (see pumpAPU).
	apuRem int64

	// Clock and position.
	now        int64
	lineStart  int64
	line       int
	frame      uint64
	vblCount   uint64
	prevMaster uint64

	openBus uint8

	// CPU registers and units.
	nmitimen uint8 // $4200
	wrio     uint8 // $4201
	wrmpya   uint8 // $4202
	wrmpyb   uint8 // $4203 as written
	wrdiv    uint16
	wrdivb   uint8  // $4206 as written
	rddiv    uint16 // last completed quotient
	rdmpy    uint16 // last completed product or remainder
	mulPend  uint16
	divPendQ uint16
	divPendR uint16
	mulReady int64
	divReady int64
	mulBusy  bool
	divBusy  bool
	htime    uint16 // $4207/$4208, 9 bits
	vtime    uint16 // $4209/$420A, 9 bits
	mdmaen   uint8  // pending general DMA trigger ($420B)
	hdmaen   uint8  // $420C
	memsel   uint8  // $420D

	nmiFlag bool
	timeup  bool
	vblank  bool

	// WRAM port ($2180-$2183).
	wmadd  uint32 // 17 bits
	wmbank uint8

	// The APU mailboxes ($2140-$2143) are the APU's own
	// port latches: reads return apu.ReadPort, writes go
	// to apu.WritePort. No shadow copies live here.

	// DMA/HDMA.
	dma [8]dmaChannel

	// Joypads.
	buttons  [2]uint16 // SetButtons masks
	joyData  [8]uint8  // $4218-$421F
	joyBusy  bool
	joyDone  int  // line on which the auto read completes
	strobe   bool // $4016 write bit 0
	shiftVal [2]uint16
	shiftIdx [2]int
	joy4017w uint8 // $4017 write: port output bits

	// IRQ position tracking.
	irqLine int
	irqDot  int
}

// New builds a System from a complete ROM image, powers it
// on, and resets it. An error is returned for ROMs this
// core does not support (including coprocessor games,
// which SNES-1 refuses at load).
func New(rom []byte) (*System, error) {
	s := &System{ppu: ppu.New()}
	s.CPU = w65c816.New()
	if err := s.LoadGame(rom); err != nil {
		return nil, err
	}
	return s, nil
}

// LoadGame loads a cartridge image into the machine and
// resets it.
func (s *System) LoadGame(rom []byte) error {
	c, err := cart.Load(rom)
	if err != nil {
		return err
	}
	s.Cart = c
	s.Map = cart.NewMap(c)
	s.CPU.Attach(s)
	s.Reset()
	return nil
}

// Reset performs a console reset: the CPU takes its reset
// vector and all machine state returns to power-on values.
func (s *System) Reset() {
	s.ppu = ppu.New()
	s.apu = apu.New()
	s.apuRem = 0
	s.dma = [8]dmaChannel{}
	s.now, s.lineStart, s.line = 0, 0, 0
	s.frame, s.vblCount = 0, 0
	s.nmitimen, s.wrio = 0, 0
	s.wrmpya, s.wrmpyb, s.wrdiv, s.wrdivb = 0, 0, 0, 0
	s.rddiv, s.rdmpy = 0, 0
	s.mulBusy, s.divBusy = false, false
	s.htime, s.vtime = 0, 0
	s.mdmaen, s.hdmaen, s.memsel = 0, 0, 0
	s.nmiFlag, s.timeup = false, false
	s.wmadd, s.wmbank = 0, 0
	s.joyData = [8]uint8{}
	s.joyBusy, s.strobe = false, false
	s.shiftVal = [2]uint16{}
	s.shiftIdx = [2]int{}
	s.openBus = 0
	// Line 0 is the pre-render line; the machine starts in
	// vblank (vblank covers lines 225..261 and line 0).
	s.vblank = true
	s.CPU.Reset()
	s.prevMaster = s.CPU.MasterCycles()
}

// PPU returns the picture processing unit.
func (s *System) PPU() *ppu.PPU { return s.ppu }

// APU returns the audio processing unit (SPC700, SMP,
// and S-DSP), for tools and tests.
func (s *System) APU() *apu.APU { return s.apu }

// AudioBuffer drains the APU's pending stereo samples:
// interleaved L, R int16 at 32000 Hz. A frame of NTSC
// runtime produces about 534 samples (1068 values). The
// buffer is empty until the machine has run.
func (s *System) AudioBuffer() []int16 { return s.apu.DrainAudio() }

// Framebuffer returns the current frame as Width*Height
// XRGB8888 pixels. The slice is owned by the PPU and is
// rewritten line by line as the machine runs.
func (s *System) Framebuffer() []uint32 { return s.ppu.Framebuffer() }

// Frames returns the number of vertical blanks begun since
// power-on.
func (s *System) Frames() uint64 { return s.vblCount }

// SetButtons sets the button mask for a controller port
// (0 or 1). See the Button constants for the bit layout.
func (s *System) SetButtons(port int, mask uint16) {
	if port < 0 || port > 1 {
		return
	}
	s.buttons[port] = mask
}

// Title returns the game title from the cartridge header.
func (s *System) Title() string { return s.Cart.Header.Title }

// SystemRAM returns the 128 KB of console work RAM.
func (s *System) SystemRAM() []byte { return s.Map.WRAM() }

// SaveRAM returns the cartridge save RAM (empty if none).
func (s *System) SaveRAM() []byte { return s.Map.SRAM() }

// --- timing -------------------------------------------------------------

func (s *System) displayLines() int {
	if s.ppu.Overscan() {
		return 239
	}
	return 224
}

func (s *System) totalLines() int {
	if s.ppu.Interlace() {
		if s.ppu.Field() {
			return 263
		}
		return 262
	}
	return 262
}

func (s *System) lineLength() int {
	// The short line: line 0 is four clocks shorter on
	// alternating frames while the display is enabled.
	if s.line == 0 && (s.frame%2 == 1) && !s.ppu.ForcedBlank() {
		return clocksPerLine - 4
	}
	return clocksPerLine
}

func (s *System) dot() int {
	return int(s.now-s.lineStart) / 4
}

// Step executes one CPU instruction, then runs machine
// bookkeeping (line events, pending DMA).
func (s *System) Step() int {
	n := s.CPU.Step()
	s.syncTime()
	if s.mdmaen != 0 {
		mask := s.mdmaen
		s.mdmaen = 0
		s.runDMA(mask)
	}
	return n
}

// RunCycles advances the machine by roughly the given
// number of CPU cycles.
func (s *System) RunCycles(cycles int) {
	start := s.CPU.Cycles() + uint64(cycles)
	for s.CPU.Cycles() < start {
		s.Step()
	}
}

// RunFrame runs until the next vertical blank begins.
func (s *System) RunFrame() {
	target := s.vblCount + 1
	for s.vblCount < target {
		s.Step()
	}
}

func (s *System) syncTime() {
	mc := s.CPU.MasterCycles()
	delta := int64(mc - s.prevMaster)
	s.now += delta
	s.prevMaster = mc
	s.pumpAPU(delta)
	s.advance(0)
}

// pumpAPU advances the APU by the SPC cycles that the
// given number of master clocks corresponds to, at the
// exact 1024000/21477272 ratio with the remainder
// carried in apuRem. DMA and HDMA costs arrive through
// advance; CPU instruction clocks arrive through
// syncTime. The two never double count: instruction
// clocks are reported only by the CPU counter, transfer
// costs only by the advance argument.
func (s *System) pumpAPU(masterDelta int64) {
	if masterDelta <= 0 {
		return
	}
	total := masterDelta*1024000 + s.apuRem
	steps := total / 21477272
	s.apuRem = total % 21477272
	if steps > 0 {
		s.apu.Step(int(steps))
	}
}

// advance moves machine time forward by the given master
// clocks, processing line events, then re-evaluates IRQ
// and the CPU interrupt lines.
func (s *System) advance(clocks int64) {
	s.now += clocks
	s.pumpAPU(clocks)
	for s.now-s.lineStart >= int64(s.lineLength()) {
		s.lineStart += int64(s.lineLength())
		s.enterLine(s.line + 1)
	}
	s.checkIRQ()
	s.syncLines()
}

func (s *System) enterLine(n int) {
	if n >= s.totalLines() {
		n = 0
		s.frame++
		if s.ppu.Interlace() {
			s.ppu.SetField(!s.ppu.Field())
		}
	}
	s.line = n
	d := s.displayLines()
	switch {
	case n == 0:
		// Pre-render line: HDMA re-initializes, sprite
		// overflow flags reset for the new frame.
		s.hdmaInitAll()
		s.ppu.ClearSpriteFlags()
	case n == 1:
		s.vblank = false
	case n == d+1:
		// The last display line just finished, and
		// vblank starts now.
		s.vblank = true
		s.nmiFlag = true
		s.vblCount++
		if s.nmitimen&0x01 != 0 && !s.joyBusy {
			s.joyBusy = true
			s.joyDone = n + 3
		}
	}
	// Render the display line that just ended: display
	// lines are 1..d, so entering line n in 2..d+1 ends
	// display line n-1 (0-based index n-2).
	if n >= 2 && n <= d+1 {
		s.ppu.RenderLine(n - 2)
	}
	// HDMA transfers belong to the display line being
	// entered.
	if n >= 1 && n <= d {
		s.hdmaLine()
	}
	if s.joyBusy && n == s.joyDone {
		s.sampleJoypads()
		s.joyBusy = false
	}
}

func (s *System) syncLines() {
	s.CPU.SetNMILine(s.nmiFlag && s.nmitimen&0x80 != 0)
	s.CPU.SetIRQLine(s.timeup)
	// The PPU drops CGRAM writes that land during active
	// display outside hblank; tell it where the beam is.
	d := s.displayLines()
	active := s.line >= 1 && s.line <= d && s.dot() < 274
	s.ppu.SetDisplayActive(active && !s.ppu.ForcedBlank())
}

// checkIRQ evaluates the H/V timer against the position
// movement since the last check, at instruction
// granularity.
func (s *System) checkIRQ() {
	mode := (s.nmitimen >> 4) & 3
	line, dot := s.line, s.dot()
	prevLine, prevDot := s.irqLine, s.irqDot
	s.irqLine, s.irqDot = line, dot
	if s.timeup || mode == 0 {
		return
	}
	h := int(s.htime)
	v := int(s.vtime)
	fire := false
	sameFrame := line >= prevLine
	switch mode {
	case 1: // H only
		if h > 340 {
			break
		}
		if sameFrame && line == prevLine {
			fire = prevDot < h && dot >= h
		} else if sameFrame {
			// A line boundary was crossed: the H point
			// was passed at the end of the previous
			// line or earlier in this one.
			fire = true
		}
	case 2: // V only
		fire = sameFrame && prevLine < v && line >= v
	case 3: // H and V
		if h > 340 {
			break
		}
		if line == v && prevLine == v {
			fire = prevDot < h && dot >= h
		} else if sameFrame && prevLine < v && line >= v {
			fire = dot >= h || line > v
		}
	}
	if fire {
		s.timeup = true
	}
}

// --- bus ----------------------------------------------------------------

func isIO(bank uint8, addr uint16) bool {
	return (bank < 0x40 || (bank >= 0x80 && bank < 0xC0)) && addr >= 0x2000 && addr < 0x6000
}

// Read implements w65c816.Bus.
func (s *System) Read(bank uint8, addr uint16) uint8 {
	if isIO(bank, addr) {
		v := s.readIO(addr)
		s.openBus = v
		return v
	}
	s.Map.OpenBus = s.openBus
	v := s.Map.Read(bank, addr)
	s.openBus = v
	return v
}

// Write implements w65c816.Bus.
func (s *System) Write(bank uint8, addr uint16, val uint8) {
	s.openBus = val
	if isIO(bank, addr) {
		s.writeIO(addr, val)
		return
	}
	s.Map.OpenBus = s.openBus
	s.Map.Write(bank, addr, val)
}

// WriteDummy routes the CPU's read-modify-write dummy
// writes as real writes (the bus is driven on hardware).
func (s *System) WriteDummy(bank uint8, addr uint16, val uint8) {
	s.Write(bank, addr, val)
}

// Speed implements w65c816.Bus: the cartridge map's speed
// classes, with the $420D MEMSEL bit gating fast ROM
// access. When MEMSEL is clear, fast-ROM regions run at
// the slow class.
func (s *System) Speed(bank uint8, addr uint16) w65c816.Speed {
	sp := s.Map.Speed(bank, addr)
	if sp == w65c816.SpeedFast && s.memsel&1 == 0 {
		return w65c816.SpeedSlow
	}
	return sp
}

// Peek reads without side effects, for tools and tests.
func (s *System) Peek(bank uint8, addr uint16) uint8 {
	if isIO(bank, addr) {
		return s.openBus
	}
	m := *s.Map
	m.OpenBus = s.openBus
	return m.Read(bank, addr)
}

func (s *System) readIO(addr uint16) uint8 {
	switch {
	case addr < 0x2100:
		return s.openBus
	case addr < 0x2140:
		if addr == 0x2137 { // SLHV: latch the counters
			s.ppu.LatchCounters(uint16(s.dot()), uint16(s.line))
			return s.openBus
		}
		if addr >= 0x2134 {
			return s.ppu.ReadReg(addr, s.openBus)
		}
		return s.openBus // write-only PPU registers
	case addr < 0x2180:
		return s.apu.ReadPort(int(addr & 3))
	case addr == 0x2180:
		v := s.Map.WRAM()[s.wmadd&0x1FFFF]
		s.wmadd = (s.wmadd + 1) & 0x1FFFF
		return v
	case addr < 0x4000:
		return s.openBus
	case addr == 0x4016:
		return s.joyRead(0)
	case addr == 0x4017:
		return s.joyRead(1)
	case addr < 0x4200:
		return s.openBus
	case addr < 0x4210:
		return s.openBus // write-only CPU registers
	case addr == 0x4210: // RDNMI
		v := uint8(0x02)
		if s.nmiFlag {
			v |= 0x80
		}
		s.nmiFlag = false
		s.syncLines()
		return v
	case addr == 0x4211: // TIMEUP
		v := uint8(0)
		if s.timeup {
			v = 0x80
		}
		s.timeup = false
		s.syncLines()
		return v
	case addr == 0x4212: // HVBJOY
		var v uint8
		if s.vblank {
			v |= 0x80
		}
		if s.dot() >= 274 {
			v |= 0x40
		}
		if s.joyBusy {
			v |= 0x01
		}
		return v
	case addr == 0x4213: // RDIO
		return s.wrio & 0x80
	case addr == 0x4214:
		s.settleMath()
		return uint8(s.rddiv)
	case addr == 0x4215:
		s.settleMath()
		return uint8(s.rddiv >> 8)
	case addr == 0x4216:
		s.settleMath()
		return uint8(s.rdmpy)
	case addr == 0x4217:
		s.settleMath()
		return uint8(s.rdmpy >> 8)
	case addr >= 0x4218 && addr < 0x4220:
		return s.joyData[addr-0x4218]
	case addr < 0x4300:
		return s.openBus
	case addr < 0x4380:
		return s.readDMA(addr)
	}
	return s.openBus
}

func (s *System) writeIO(addr uint16, val uint8) {
	switch {
	case addr < 0x2100:
		// Expansion area: no registers.
	case addr < 0x2140:
		s.ppu.WriteReg(addr, val)
	case addr < 0x2180:
		s.apu.WritePort(int(addr&3), val)
	case addr == 0x2180:
		s.Map.WRAM()[s.wmadd&0x1FFFF] = val
		s.wmadd = (s.wmadd + 1) & 0x1FFFF
	case addr == 0x2181:
		s.wmadd = (s.wmadd &^ 0xFF) | uint32(val)
	case addr == 0x2182:
		s.wmadd = (s.wmadd &^ 0xFF00) | uint32(val)<<8
	case addr == 0x2183:
		s.wmbank = val & 1
		if val&1 != 0 {
			s.wmadd |= 0x10000
		} else {
			s.wmadd &^= 0x10000
		}
	case addr < 0x4000:
		// Unmapped.
	case addr == 0x4016:
		s.joyWriteStrobe(val)
	case addr == 0x4017:
		s.joy4017w = val & 0x07
	case addr < 0x4200:
		// Unmapped.
	case addr == 0x4200:
		s.nmitimen = val
		s.syncLines()
	case addr == 0x4201:
		s.wrio = val
	case addr == 0x4202:
		s.wrmpya = val
	case addr == 0x4203:
		s.wrmpyb = val
		s.mulPend = uint16(s.wrmpya) * uint16(val)
		s.mulReady = s.now + 48
		s.mulBusy = true
	case addr == 0x4204:
		s.wrdiv = (s.wrdiv & 0xFF00) | uint16(val)
	case addr == 0x4205:
		s.wrdiv = (s.wrdiv & 0x00FF) | uint16(val)<<8
	case addr == 0x4206:
		s.wrdivb = val
		if val == 0 {
			s.divPendQ = 0xFFFF
			s.divPendR = s.wrdiv
		} else {
			s.divPendQ = s.wrdiv / uint16(val)
			s.divPendR = s.wrdiv % uint16(val)
		}
		s.divReady = s.now + 96
		s.divBusy = true
	case addr == 0x4207:
		s.htime = (s.htime & 0xFF00) | uint16(val)
	case addr == 0x4208:
		s.htime = (s.htime & 0x00FF) | (uint16(val&1) << 8)
	case addr == 0x4209:
		s.vtime = (s.vtime & 0xFF00) | uint16(val)
	case addr == 0x420A:
		s.vtime = (s.vtime & 0x00FF) | (uint16(val&1) << 8)
	case addr == 0x420B:
		s.mdmaen = val
	case addr == 0x420C:
		rising := val &^ s.hdmaen
		s.hdmaen = val
		for ch := 0; ch < 8; ch++ {
			if rising&(1<<uint(ch)) != 0 {
				// Enabling a channel mid-frame: it
				// initializes at the next display line.
				c := &s.dma[ch]
				c.hdmaInit = true
				c.hdmaOff = false
				c.a2al, c.a2ah = c.a1tl, c.a1th
				c.hdmaLeft = 0
			}
		}
	case addr == 0x420D:
		s.memsel = val
	case addr < 0x4300:
		// Write-only and unused CPU registers.
	case addr < 0x4380:
		s.writeDMA(addr, val)
	}
}

// settleMath publishes pending multiply/divide results
// whose latency has elapsed.
func (s *System) settleMath() {
	if s.mulBusy && s.now >= s.mulReady {
		s.rdmpy = s.mulPend
		s.mulBusy = false
	}
	if s.divBusy && s.now >= s.divReady {
		s.rddiv = s.divPendQ
		s.rdmpy = s.divPendR
		s.divBusy = false
	}
}

// --- joypads ------------------------------------------------------------

func (s *System) joyWriteStrobe(val uint8) {
	s.strobe = val&1 != 0
	if s.strobe {
		for i := 0; i < 2; i++ {
			s.shiftVal[i] = s.buttons[i]
			s.shiftIdx[i] = 0
		}
	}
}

func (s *System) joyRead(port int) uint8 {
	bit := 0
	switch {
	case s.strobe || s.shiftIdx[port] < 12:
		if s.shiftVal[port]>>uint(s.shiftIdx[port])&1 != 0 {
			bit = 1
		}
	case s.shiftIdx[port] >= 16:
		bit = 1 // the line idles high past the 16-bit report
	default:
		// Bits 12 to 15 of a standard pad's report are 0.
	}
	if !s.strobe {
		s.shiftIdx[port]++
	}
	v := s.openBus & 0xFC
	if port == 1 {
		v |= 0x20
	}
	return v | 0x02 | uint8(bit)
}

// sampleJoypads copies the button masks into the auto-read
// data registers in the hardware layout.
func (s *System) sampleJoypads() {
	for port := 0; port < 2; port++ {
		m := s.buttons[port]
		lo := uint8(0)
		for i := 0; i < 8; i++ {
			if m>>uint(i)&1 != 0 {
				lo |= 1 << uint(7-i)
			}
		}
		hi := uint8(0)
		for i := 0; i < 4; i++ {
			if m>>uint(8+i)&1 != 0 {
				hi |= 1 << uint(7-i)
			}
		}
		s.joyData[port*4] = lo
		s.joyData[port*4+1] = hi
		s.joyData[port*4+2] = 0
		s.joyData[port*4+3] = 0
	}
}

// --- DMA and HDMA -------------------------------------------------------

// dmaPatterns lists the B-address offsets applied on each
// step of each transfer mode, per the documented mode
// table: mode 3 writes each of two registers twice in a
// row (the CGADD/CGDATA gradient style), mode 5 alternates
// two registers, and modes 2 and 6 (likewise 3 and 7)
// produce identical byte streams.
var dmaPatterns = [8][]uint8{
	{0},
	{0, 1},
	{0, 0},
	{0, 0, 1, 1},
	{0, 1, 2, 3},
	{0, 1, 0, 1},
	{0, 0},
	{0, 0, 1, 1},
}

func bAddr(bbad uint8, off uint8) uint16 {
	return 0x2100 + uint16(bbad+off)
}

func (s *System) readDMA(addr uint16) uint8 {
	ch := &s.dma[(addr>>4)&7]
	switch addr & 0x0F {
	case 0x0:
		return ch.dmap
	case 0x1:
		return ch.bbad
	case 0x2:
		return ch.a1tl
	case 0x3:
		return ch.a1th
	case 0x4:
		return ch.a1b
	case 0x5:
		return ch.dasl
	case 0x6:
		return ch.dash
	case 0x7:
		return ch.dasb
	case 0x8:
		return ch.a2al
	case 0x9:
		return ch.a2ah
	case 0xA:
		return ch.ntlr
	case 0xB, 0xF:
		return ch.mirr
	}
	return s.openBus
}

func (s *System) writeDMA(addr uint16, val uint8) {
	ch := &s.dma[(addr>>4)&7]
	switch addr & 0x0F {
	case 0x0:
		ch.dmap = val
	case 0x1:
		ch.bbad = val
	case 0x2:
		ch.a1tl = val
	case 0x3:
		ch.a1th = val
	case 0x4:
		ch.a1b = val
	case 0x5:
		ch.dasl = val
	case 0x6:
		ch.dash = val
	case 0x7:
		ch.dasb = val
	case 0x8:
		ch.a2al = val
	case 0x9:
		ch.a2ah = val
	case 0xA:
		ch.ntlr = val
	case 0xB, 0xF:
		ch.mirr = val
	}
}

// runDMA executes a general DMA for the channels in mask.
// The transfer runs between CPU instructions; its clock
// cost (8 per channel activation plus 8 per byte) is
// charged to machine time afterwards, so line events the
// transfer spans still fire in order.
func (s *System) runDMA(mask uint8) {
	var cost int64
	for n := 0; n < 8; n++ {
		if mask&(1<<uint(n)) == 0 {
			continue
		}
		c := &s.dma[n]
		count := int(c.dasl) | int(c.dash)<<8
		if count == 0 {
			count = 0x10000
		}
		pat := dmaPatterns[c.dmap&7]
		toB := c.dmap&0x80 == 0
		adj := int32(0)
		switch (c.dmap >> 3) & 3 {
		case 0: // increment
			adj = 1
		case 2: // decrement
			adj = -1
		}
		// Adjust modes 1 and 3 are fixed: the A-bus
		// address does not move.
		a := uint32(c.a1b)<<16 | uint32(c.a1th)<<8 | uint32(c.a1tl)
		cost += 8
		for i := 0; i < count; i++ {
			ba := bAddr(c.bbad, pat[i%len(pat)])
			if toB {
				v := s.Read(uint8(a>>16), uint16(a))
				c.mirr = v
				s.Write(0, ba, v)
			} else {
				v := s.Read(0, ba)
				c.mirr = v
				s.Write(uint8(a>>16), uint16(a), v)
			}
			a = uint32(int32(a)+adj) & 0xFFFFFF
			rem := count - i - 1
			c.a1tl = uint8(a)
			c.a1th = uint8(a >> 8)
			c.a1b = uint8(a >> 16)
			c.dasl = uint8(rem)
			c.dash = uint8(rem >> 8)
			cost += 8
		}
	}
	if cost > 0 {
		s.advance(cost)
	}
}

// hdmaInitAll runs at the pre-render line: every enabled
// channel reloads its table pointer from $43n2/$43n3.
func (s *System) hdmaInitAll() {
	for n := 0; n < 8; n++ {
		c := &s.dma[n]
		c.hdmaOff = false
		if s.hdmaen&(1<<uint(n)) != 0 {
			c.hdmaInit = true
			c.a2al, c.a2ah = c.a1tl, c.a1th
			c.hdmaLeft = 0
			c.ntlr = 0
		} else {
			c.hdmaInit = false
		}
	}
}

// hdmaLine runs one display line's HDMA transfers for all
// active channels.
func (s *System) hdmaLine() {
	var cost int64
	for n := 0; n < 8; n++ {
		if s.hdmaen&(1<<uint(n)) == 0 {
			continue
		}
		c := &s.dma[n]
		if !c.hdmaInit || c.hdmaOff {
			continue
		}
		cost += s.hdmaChannel(c)
	}
	if cost > 0 {
		s.advance(cost)
	}
}

// hdmaChannel performs one line of one channel's HDMA:
// reloading the table entry when the line counter empties,
// then transferring one unit of bytes.

func (s *System) hdmaChannel(c *dmaChannel) int64 {
	if c.hdmaLeft == 0 {
		table := uint32(c.a1b)<<16 | uint32(c.a2ah)<<8 | uint32(c.a2al)
		countByte := s.Read(uint8(table>>16), uint16(table))
		table++
		if countByte == 0 {
			c.hdmaOff = true
			c.ntlr = 0
			return 8
		}
		lines := int(countByte & 0x7F)
		if lines == 0 {
			lines = 128
		}
		c.hdmaLeft = lines
		c.hdmaRepeat = countByte&0x80 != 0
		c.ntlr = countByte
		unit := len(dmaPatterns[c.dmap&7])
		if c.dmap&0x40 != 0 {
			// Indirect: the next two table bytes are the
			// data pointer; the data bank is DASB, loaded
			// by software.
			c.dasl = s.Read(uint8(table>>16), uint16(table))
			c.dash = s.Read(uint8(table>>16), uint16(table+1))
			table += 2
		} else {
			// Direct: the unit's data follows in the
			// table itself.
			for i := 0; i < unit; i++ {
				c.hdmaData[i] = s.Read(uint8(table>>16), uint16(table+uint32(i)))
			}
			table += uint32(unit)
		}
		c.a2al = uint8(table)
		c.a2ah = uint8(table >> 8)
	}
	pat := dmaPatterns[c.dmap&7]
	unit := len(pat)
	if c.dmap&0x40 != 0 {
		// Indirect: data comes from the DAS pointer in
		// bank DASB, advancing per byte within this
		// transfer.
		dataAddr := uint32(c.dash)<<8 | uint32(c.dasl)
		for i := 0; i < unit; i++ {
			v := s.Read(c.dasb, uint16(dataAddr))
			c.mirr = v
			s.Write(0, bAddr(c.bbad, pat[i]), v)
			dataAddr++
		}
		if !c.hdmaRepeat {
			c.dasl = uint8(dataAddr)
			c.dash = uint8(dataAddr >> 8)
		}
	} else {
		for i := 0; i < unit; i++ {
			v := c.hdmaData[i]
			c.mirr = v
			s.Write(0, bAddr(c.bbad, pat[i]), v)
		}
	}
	c.hdmaLeft--
	if c.hdmaLeft == 0 {
		c.ntlr = 0
	} else {
		c.ntlr = uint8(c.hdmaLeft) | boolBit(c.hdmaRepeat)<<7
	}
	return int64(8 + 8*unit)
}

func boolBit(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// --- save states ---------------------------------------------------------

const (
	// Version 2 added the APU block (SNES-3). Version 1
	// states have no APU block and are rejected.
	stateVersion = 2
	sysBlockSize = 1024
	// stateHead = version + CPU + WRAM + SRAM length word.
	stateHead = 1 + 96 + 131072 + 4
)

// StateSize returns the exact byte size of a serialized
// state for this cartridge.
func (s *System) StateSize() int {
	return stateHead + len(s.Map.SRAM()) + ppu.SerializeSize + apu.SerializeSize + sysBlockSize
}

// Hash is a quick content digest of the full state (used
// by the determinism tests).
func (s *System) Hash() uint64 {
	h := fnv.New64a()
	_, _ = h.Write(s.Serialize())
	return h.Sum64()
}

// Serialize captures the whole machine: CPU, WRAM, SRAM,
// the PPU block, the APU block, and the system block
// (registers, DMA channel state, position counters,
// joypad state). Pending undrained audio is not part of
// the state; call AudioBuffer first if it matters.
func (s *System) Serialize() []byte {
	out := make([]byte, s.StateSize())
	out[0] = stateVersion
	copy(out[1:97], s.CPU.Serialize())
	copy(out[97:97+131072], s.Map.WRAM())
	binary.LittleEndian.PutUint32(out[97+131072:], uint32(len(s.Map.SRAM())))
	soff := stateHead
	copy(out[soff:], s.Map.SRAM())
	poff := soff + len(s.Map.SRAM())
	s.ppu.Serialize(out[poff : poff+ppu.SerializeSize])
	aoff := poff + ppu.SerializeSize
	copy(out[aoff:aoff+apu.SerializeSize], s.apu.Serialize())
	s.serializeSys(out[aoff+apu.SerializeSize:])
	return out
}

func (s *System) serializeSys(b []byte) {
	b[0] = s.nmitimen
	b[1] = s.wrio
	b[2] = s.wrmpya
	b[3] = s.wrmpyb
	binary.LittleEndian.PutUint16(b[4:], s.wrdiv)
	b[6] = s.wrdivb
	binary.LittleEndian.PutUint16(b[7:], s.rddiv)
	binary.LittleEndian.PutUint16(b[9:], s.rdmpy)
	binary.LittleEndian.PutUint16(b[11:], s.mulPend)
	binary.LittleEndian.PutUint16(b[13:], s.divPendQ)
	binary.LittleEndian.PutUint16(b[15:], s.divPendR)
	// Math-unit deadlines are stored relative to now so a
	// restored state (whose clock is rebased) keeps the
	// same remaining latency.
	binary.LittleEndian.PutUint64(b[17:], uint64(s.mulReady-s.now))
	binary.LittleEndian.PutUint64(b[25:], uint64(s.divReady-s.now))
	b[33] = boolBit(s.mulBusy)
	b[34] = boolBit(s.divBusy)
	binary.LittleEndian.PutUint16(b[35:], s.htime)
	binary.LittleEndian.PutUint16(b[37:], s.vtime)
	b[39] = s.mdmaen
	b[40] = s.hdmaen
	b[41] = s.memsel
	b[42] = boolBit(s.nmiFlag)
	b[43] = boolBit(s.timeup)
	b[44] = boolBit(s.vblank)
	binary.LittleEndian.PutUint32(b[45:], s.wmadd)
	b[49] = s.wmbank
	// Bytes 50..57 held the SNES-2 shadow APU port
	// latches; in version 2 they carry the APU clock
	// ratio remainder, so a restored machine keeps the
	// exact master to SPC phase it was saved with.
	binary.LittleEndian.PutUint64(b[50:], uint64(s.apuRem))
	copy(b[58:66], s.joyData[:])
	b[66] = boolBit(s.joyBusy)
	binary.LittleEndian.PutUint16(b[67:], uint16(s.joyDone))
	b[69] = boolBit(s.strobe)
	binary.LittleEndian.PutUint16(b[70:], s.shiftVal[0])
	binary.LittleEndian.PutUint16(b[72:], s.shiftVal[1])
	b[74] = uint8(s.shiftIdx[0])
	b[75] = uint8(s.shiftIdx[1])
	b[76] = s.joy4017w
	b[77] = s.openBus
	binary.LittleEndian.PutUint16(b[78:], s.buttons[0])
	binary.LittleEndian.PutUint16(b[80:], s.buttons[1])
	// Position: line, intra-line phase, frame counters.
	binary.LittleEndian.PutUint16(b[82:], uint16(s.line))
	binary.LittleEndian.PutUint32(b[84:], uint32(s.now-s.lineStart))
	binary.LittleEndian.PutUint64(b[88:], s.frame)
	binary.LittleEndian.PutUint64(b[96:], s.vblCount)
	binary.LittleEndian.PutUint16(b[104:], uint16(s.irqLine))
	binary.LittleEndian.PutUint16(b[106:], uint16(s.irqDot))
	off := 128
	for i := 0; i < 8; i++ {
		c := &s.dma[i]
		b[off+0] = c.dmap
		b[off+1] = c.bbad
		b[off+2] = c.a1tl
		b[off+3] = c.a1th
		b[off+4] = c.a1b
		b[off+5] = c.dasl
		b[off+6] = c.dash
		b[off+7] = c.dasb
		b[off+8] = c.a2al
		b[off+9] = c.a2ah
		b[off+10] = c.ntlr
		b[off+11] = c.mirr
		b[off+12] = boolBit(c.hdmaInit)
		b[off+13] = boolBit(c.hdmaOff)
		b[off+14] = uint8(c.hdmaLeft)
		b[off+15] = boolBit(c.hdmaRepeat)
		copy(b[off+16:off+20], c.hdmaData[:])
		off += 32
	}
}

// Deserialize restores a state produced by Serialize.
func (s *System) Deserialize(in []byte) error {
	if len(in) != s.StateSize() {
		return fmt.Errorf("system: state size %d, want %d", len(in), s.StateSize())
	}
	if in[0] != stateVersion {
		return fmt.Errorf("system: state version %d", in[0])
	}
	sramLen := int(binary.LittleEndian.Uint32(in[97+131072:]))
	if sramLen != len(s.Map.SRAM()) {
		return fmt.Errorf("system: sram length %d in state, cartridge has %d", sramLen, len(s.Map.SRAM()))
	}
	if err := s.CPU.Deserialize(in[1:97]); err != nil {
		return err
	}
	copy(s.Map.WRAM(), in[97:97+131072])
	soff := stateHead
	copy(s.Map.SRAM(), in[soff:soff+sramLen])
	poff := soff + sramLen
	if err := s.ppu.Deserialize(in[poff : poff+ppu.SerializeSize]); err != nil {
		return err
	}
	aoff := poff + ppu.SerializeSize
	if err := s.apu.Deserialize(in[aoff : aoff+apu.SerializeSize]); err != nil {
		return err
	}
	sb := in[aoff+apu.SerializeSize:]
	s.deserializeSys(sb)
	// Time position reconstruction: the clock is rebased
	// so the current line started at 0; the saved phase is
	// where inside the line the machine sits. Pending
	// math deadlines were stored relative to the clock
	// and are re-anchored here.
	s.lineStart = 0
	s.now = int64(binary.LittleEndian.Uint32(sb[84:]))
	s.mulReady += s.now
	s.divReady += s.now
	s.prevMaster = s.CPU.MasterCycles()
	s.syncLines()
	return nil
}

func (s *System) deserializeSys(b []byte) {
	s.nmitimen = b[0]
	s.wrio = b[1]
	s.wrmpya = b[2]
	s.wrmpyb = b[3]
	s.wrdiv = binary.LittleEndian.Uint16(b[4:])
	s.wrdivb = b[6]
	s.rddiv = binary.LittleEndian.Uint16(b[7:])
	s.rdmpy = binary.LittleEndian.Uint16(b[9:])
	s.mulPend = binary.LittleEndian.Uint16(b[11:])
	s.divPendQ = binary.LittleEndian.Uint16(b[13:])
	s.divPendR = binary.LittleEndian.Uint16(b[15:])
	s.mulReady = int64(binary.LittleEndian.Uint64(b[17:]))
	s.divReady = int64(binary.LittleEndian.Uint64(b[25:]))
	s.mulBusy = b[33] != 0
	s.divBusy = b[34] != 0
	s.htime = binary.LittleEndian.Uint16(b[35:])
	s.vtime = binary.LittleEndian.Uint16(b[37:])
	s.mdmaen = b[39]
	s.hdmaen = b[40]
	s.memsel = b[41]
	s.nmiFlag = b[42] != 0
	s.timeup = b[43] != 0
	s.vblank = b[44] != 0
	s.wmadd = binary.LittleEndian.Uint32(b[45:])
	s.wmbank = b[49]
	s.apuRem = int64(binary.LittleEndian.Uint64(b[50:]))
	copy(s.joyData[:], b[58:66])
	s.joyBusy = b[66] != 0
	s.joyDone = int(binary.LittleEndian.Uint16(b[67:]))
	s.strobe = b[69] != 0
	s.shiftVal[0] = binary.LittleEndian.Uint16(b[70:])
	s.shiftVal[1] = binary.LittleEndian.Uint16(b[72:])
	s.shiftIdx[0] = int(b[74])
	s.shiftIdx[1] = int(b[75])
	s.joy4017w = b[76]
	s.openBus = b[77]
	s.buttons[0] = binary.LittleEndian.Uint16(b[78:])
	s.buttons[1] = binary.LittleEndian.Uint16(b[80:])
	s.line = int(binary.LittleEndian.Uint16(b[82:]))
	s.frame = binary.LittleEndian.Uint64(b[88:])
	s.vblCount = binary.LittleEndian.Uint64(b[96:])
	s.irqLine = int(binary.LittleEndian.Uint16(b[104:]))
	s.irqDot = int(binary.LittleEndian.Uint16(b[106:]))
	off := 128
	for i := 0; i < 8; i++ {
		c := &s.dma[i]
		c.dmap = b[off+0]
		c.bbad = b[off+1]
		c.a1tl = b[off+2]
		c.a1th = b[off+3]
		c.a1b = b[off+4]
		c.dasl = b[off+5]
		c.dash = b[off+6]
		c.dasb = b[off+7]
		c.a2al = b[off+8]
		c.a2ah = b[off+9]
		c.ntlr = b[off+10]
		c.mirr = b[off+11]
		c.hdmaInit = b[off+12] != 0
		c.hdmaOff = b[off+13] != 0
		c.hdmaLeft = int(b[off+14])
		c.hdmaRepeat = b[off+15] != 0
		copy(c.hdmaData[:], b[off+16:off+20])
		off += 32
	}
}
