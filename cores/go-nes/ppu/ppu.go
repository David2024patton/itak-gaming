// Package ppu is an original implementation of the NES's
// Ricoh 2C02 picture processing unit, written from the behavior
// published on the NESdev wiki: the dot-level rendering timing,
// the loopy v/t/x scrolling registers, sprite evaluation with
// its overflow bug, and the open-bus behavior of the register
// file.
//
// The PPU advances one dot at a time (341 dots per scanline,
// 262 scanlines per NTSC frame, three dots per CPU cycle). The
// machine drives it in three-dot ticks from its CPU bus
// accessors, so every CPU-visible event (the vblank flag, sprite
// 0 hits, the MMC3's A12 clocks) lands on the same dot it does
// on hardware. Rendering state that software cannot observe
// directly (the exact byte inside a two-dot fetch, the order of
// secondary OAM writes inside the clear window) is handled at
// the granularity the published timing pins down, and the
// simplifications are documented where they live.
package ppu

import "github.com/davidpatton/itak-gaming/cores/go-nes/cart"

// Width and Height are the framebuffer dimensions in pixels.
const (
	Width  = 256
	Height = 240
)

// Timing constants for the NTSC 2C02.
const (
	dotsPerLine   = 341
	linesPerFrame = 262
	dotsPerFrame  = dotsPerLine * linesPerFrame // 89342, even frames

	vblSetLine = 241 // vblank flag sets at dot 1 of this line
	preLine    = 261 // pre-render line: flags clear at dot 1
)

// Memory is the cartridge side of the PPU bus: pattern memory
// and the mirroring mode, plus the A12 hook the MMC3's IRQ
// counter hangs off.
type Memory interface {
	ReadCHR(addr uint16) uint8
	WriteCHR(addr uint16, val uint8)
	MirrorMode() cart.Mirror
	ClockA12()
}

// a12MinLowDots is the glitch filter on the cartridge's A12
// line: the MMC3 only clocks its IRQ counter on a rising edge
// of A12 when the line has been low for at least this many
// consecutive dots. During rendering, A12 (bit 12 of the fetch
// address) toggles with four-dot low runs inside each tile and
// sprite fetch group, while the one edge that matters per
// scanline, the transition between the background and sprite
// fetch phases in the standard pattern-table arrangements, is
// preceded by a low run tens of dots long. This threshold sits
// between those two regimes; the Blargg mmc3_irq suite (which
// counts exactly 241 clocks per rendered frame) pins it down.
const a12MinLowDots = 9

// decayLifetimeDots is how long each bit of the IO latch holds
// a stored 1 before it decays to 0, per bit position. On
// hardware the latch is a small dynamic store whose bits fade
// at slightly different rates on a scale of somewhat under a
// second; Blargg's ppu_open_bus requires every bit gone by one
// second and intact after tens of milliseconds. One second is
// about 5.37 million dots at the NTSC dot rate.
var decayLifetimeDots = [8]uint32{
	3_600_000, 3_750_000, 3_900_000, 4_050_000,
	4_200_000, 4_350_000, 4_500_000, 4_650_000,
}

// SerializeSize is the fixed save-state size in bytes: version
// byte, CIRAM, OAM, palette, then the registers and pipeline
// state in this package's private layout.
const SerializeSize = 1 + 4096 + 256 + 32 + 192

const serializeVersion = 1

// PPU is a single 2C02 instance.
type PPU struct {
	mem Memory

	// Register file and scrolling state.
	ctrl    uint8 // $2000
	mask    uint8 // $2001
	oamAddr uint8 // $2003
	v       uint16
	t       uint16
	x       uint8 // fine X scroll, 0-7
	w       bool  // $2005/$2006 write toggle

	readBuf uint8 // $2007 read buffer

	// IO latch (open bus) with per-bit decay.
	ioLatch uint8
	ioDecay [8]uint32

	// Status flags.
	vblFlag    bool
	spr0Hit    bool
	sprOvrFlag bool
	suppress   bool // a $2002 read has vetoed the next vblank set

	// SetInLastTick reports where the vblank flag was set
	// relative to the most recent Tick: -1 when the set dot was
	// not in the tick, otherwise the number of dots of the tick
	// that came after the set dot (0, 1, or 2 with three-dot
	// ticks). The machine uses it to resolve the $2002 read and
	// $2000 write races against the set dot.
	SetInLastTick int

	// Timing position: the next dot to execute.
	scanline int // 0-239 visible, 240 post, 241-260 vblank, 261 pre-render
	dot      int // 0-340
	frames   uint64
	oddFrame bool // the frame in progress is an odd frame

	// Memory.
	ciram   [4096]uint8 // only 2048 used unless four-screen
	palette [32]uint8
	oam     [256]uint8

	// Background pipeline.
	bgShiftLo, bgShiftHi uint16
	atShiftLo, atShiftHi uint16
	ntLatch              uint8
	atLatch              uint8
	patLoLatch           uint8
	patHiLatch           uint8

	// Sprite pipeline for the line being rendered: shift
	// registers and counters loaded during the previous line's
	// fetch window.
	sprShLo   [8]uint8
	sprShHi   [8]uint8
	sprAttr   [8]uint8
	sprXCnt   [8]uint8
	sprLineIx [8]int // OAM index each slot was evaluated from

	// Sprite evaluation for the next line.
	secOAM    [32]uint8
	secIndex  [8]int // OAM index per secondary slot
	evalN     int    // OAM sprite index, 0-63
	evalM     int    // byte index within the sprite, 0-3
	evalCount int    // sprites copied to secondary OAM
	evalState int    // see the evaluation constants in tick.go
	evalByte  uint8  // the byte most recently read by evaluation

	// A12 edge tracking.
	prevA12 bool
	a12Low  int // consecutive dots A12 has been low

	framebuffer [Width * Height]uint32
}

// eval states for the sprite evaluation machine.
const (
	evalCopyY    = iota // reading a sprite's Y byte, deciding range
	evalCopyRest        // copying bytes 1-3 of an in-range sprite
	evalBug             // secondary OAM full: the diagonal overflow search
	evalDone
)

// New returns a PPU in its power-on state: registers cleared,
// timing parked at the end of the pre-render line so the
// first full frame is a complete one (the test suites
// self-synchronize to vblank, so the absolute power-on phase
// is not observable to them). From power-on the real chip
// needs about a frame before its output is stable; software
// is documented to ignore it for roughly two vblanks, which
// is what games and test ROMs do.
func New(mem Memory) *PPU {
	p := &PPU{mem: mem, SetInLastTick: -1, scanline: preLine, dot: dotsPerLine - 2, oddFrame: true}
	for i := range p.secIndex {
		p.secIndex[i] = -1
		p.sprLineIx[i] = -1
	}
	return p
}

// Frames returns how many frames have completed (the count
// increments as the vblank period begins).
func (p *PPU) Frames() uint64 { return p.frames }

// Position returns the current beam position (diagnostic).
func (p *PPU) Position() (line, dot int) { return p.scanline, p.dot }

// Framebuffer returns the current frame in XRGB8888, row
// major, 256 by 240. Its contents are complete once Frames has
// advanced past the frame that drew them.
func (p *PPU) Framebuffer() []uint32 { return p.framebuffer[:] }

// NMIOutput is the level of the PPU's NMI pin: the vblank flag
// ANDed with the $2000 NMI enable. The machine feeds it to the
// CPU's edge-triggered NMI input.
func (p *PPU) NMIOutput() bool { return p.vblFlag && p.ctrl&0x80 != 0 }

// VBlank reports the vblank flag, for the machine's race
// handling around the set dot.
func (p *PPU) VBlank() bool { return p.vblFlag }

// rendering reports whether either background or sprite
// rendering is enabled ($2001 bits 3 and 4). Timing treats the
// two together: fetches, evaluation, and the odd-frame skip all
// key off rendering as a whole.
func (p *PPU) rendering() bool { return p.mask&0x18 != 0 }

func (p *PPU) bgEnabled() bool  { return p.mask&0x08 != 0 }
func (p *PPU) sprEnabled() bool { return p.mask&0x10 != 0 }
func (p *PPU) spriteHeight() int {
	if p.ctrl&0x20 != 0 {
		return 16
	}
	return 8
}

// --- Cartridge and internal memory ---

// ntOffset maps a nametable address ($2000-$2FFF, already
// folded from $3000-$3EFF) to a CIRAM offset under the
// cartridge's current mirroring.
func (p *PPU) ntOffset(addr uint16) int {
	page := (addr >> 10) & 3
	off := int(addr & 0x3FF)
	switch p.mem.MirrorMode() {
	case cart.MirrorHorizontal:
		page = page >> 1 // [0,0,1,1]
	case cart.MirrorVertical:
		page = page & 1 // [0,1,0,1]
	case cart.MirrorSingle0:
		page = 0
	case cart.MirrorSingle1:
		page = 1
	case cart.MirrorFourScreen:
		// page as addressed, in the full 4 KB
	}
	return int(page)*1024 + off
}

// paletteIndex maps a palette address ($3F00-$3FFF) to the
// palette RAM index, applying the backdrop mirrors: $3F10,
// $3F14, $3F18, $3F1C alias $3F00, $3F04, $3F08, $3F0C.
func paletteIndex(addr uint16) int {
	i := int(addr & 0x1F)
	if i&0x13 == 0x10 {
		i &^= 0x10
	}
	return i
}

// readMem reads the PPU address space at a 14-bit address.
func (p *PPU) readMem(addr uint16) uint8 {
	addr &= 0x3FFF
	switch {
	case addr < 0x2000:
		return p.mem.ReadCHR(addr)
	case addr < 0x3F00:
		if addr >= 0x3000 {
			addr -= 0x1000
		}
		return p.ciram[p.ntOffset(addr)]
	default:
		return p.palette[paletteIndex(addr)]
	}
}

// writeMem writes the PPU address space at a 14-bit address.
func (p *PPU) writeMem(addr uint16, val uint8) {
	addr &= 0x3FFF
	switch {
	case addr < 0x2000:
		p.mem.WriteCHR(addr, val)
	case addr < 0x3F00:
		if addr >= 0x3000 {
			addr -= 0x1000
		}
		p.ciram[p.ntOffset(addr)] = val
	default:
		p.palette[paletteIndex(addr)] = val & 0x3F
	}
}

// PeekMem reads the PPU address space without side effects,
// for debuggers and test harnesses.
func (p *PPU) PeekMem(addr uint16) uint8 { return p.readMem(addr) }

// --- IO latch (open bus) ---

// latchValue returns the IO latch with decay applied. Decay is
// accounted incrementally in Tick; this reads the result.
func (p *PPU) latchValue() uint8 { return p.ioLatch }

// setLatch drives the full latch (a write, or a read that
// drives all eight bits), refreshing every set bit's decay.
func (p *PPU) setLatch(v uint8) {
	p.ioLatch = v
	for i := 0; i < 8; i++ {
		if v&(1<<uint(i)) != 0 {
			p.ioDecay[i] = decayLifetimeDots[i]
		}
	}
}

// setLatchLow refreshes only the low six bits of the latch (a
// palette read through $2007): the high two bits are driven
// from the latch itself, and bits driven by the latch do not
// refresh their own decay.
func (p *PPU) setLatchLow(v uint8) {
	p.ioLatch = (p.ioLatch & 0xC0) | (v & 0x3F)
	for i := 0; i < 6; i++ {
		if v&(1<<uint(i)) != 0 {
			p.ioDecay[i] = decayLifetimeDots[i]
		}
	}
}

// decayTick advances the latch decay by one dot.
func (p *PPU) decayTick() {
	for i := 0; i < 8; i++ {
		if p.ioLatch&(1<<uint(i)) != 0 {
			if p.ioDecay[i] > 0 {
				p.ioDecay[i]--
			}
			if p.ioDecay[i] == 0 {
				p.ioLatch &^= 1 << uint(i)
			}
		}
	}
}
