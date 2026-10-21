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

	// IO latch (open bus) with per-bit decay. Decay itself is
	// accounted lazily: decayDebt counts the dots elapsed
	// since the countdowns were last brought up to date, and
	// applyDecay folds the bulk elapsed time into ioDecay and
	// ioLatch whenever the latch is observed or refreshed.
	// Counting down per dot cost an eight-step loop on every
	// one of the 89 thousand dots in a frame for a value that
	// only register reads can observe.
	ioLatch   uint8
	ioDecay   [8]uint32
	decayDebt int

	// sprLiveMask has a bit per sprite slot whose loaded
	// shifters hold any pattern data for the line being drawn.
	// outputPixel scans only those slots for the winning
	// sprite pixel; slots outside the mask can never produce
	// a pixel or a sprite 0 hit on this line, so the scan
	// result is unchanged. The mask is rebuilt by loadSprite
	// as each line's sprites are fetched.
	sprLiveMask uint8

	// a12Watched is false when the cartridge's mapper never
	// clocks an IRQ counter from A12 edges (every mapper here
	// except MMC3): the per-dot A12 sampling and its address
	// reconstruction are then skipped whole.
	a12Watched bool

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

	// Lazy advance: the machine owes the PPU dots it has not
	// rendered yet. Advance records owed dots (and the segment
	// boundary the SetInLastTick accounting needs); CatchUp and
	// CatchUpTo render them, oldest first, so every observer
	// (register accesses, the interrupt poll, save states)
	// syncs the chip to its exact dot before looking. executed
	// counts the dots rendered since the last epoch reset
	// (power-on or a state load); targets and marks are named
	// in the same count.
	pending      int
	executed     int64
	segEnds      []int64 // absolute end dot of each unrendered Advance segment
	segHead      int
	segPrev      int64 // end dot of the most recently completed segment
	bulkBackdrop uint32
	bulkOff      bool
	lastSet      int64 // executed count just after the vblank set dot last fired, -1 if none

	// Dot marks: snapshots of the vblank flag at the bus sync
	// points the machine records (one per bus access, at the
	// dot the access's first two dots complete). The flag half
	// is filled in as rendering crosses the mark's dot; the
	// ctrl half is taken when the mark is recorded, because the
	// access's own $2000 write lands before its sync point and
	// the level there uses the new value.
	marks    [32]dotMark
	markFill int // sequence number of the oldest unfilled mark, -1 if none
	markSeq  int // sequence number the next Mark call expects

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

	// Pattern address caches (see bgPatternAddress and
	// sprPatternAddress in tick.go): the last background
	// pattern address with the inputs it was computed from,
	// and the eight sprite pattern addresses of one line
	// with the inputs they were computed from. A key of -1
	// means no valid entry.
	bgPatKey   int32
	bgPatAddr  uint16
	sprPatKey  int64
	sprPatAddr [8]uint16

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
	p := &PPU{mem: mem, SetInLastTick: -1, scanline: preLine, dot: dotsPerLine - 2, oddFrame: true, a12Watched: true, lastSet: -1, markFill: -1, bgPatKey: -1, sprPatKey: -1}
	for i := range p.marks {
		p.marks[i].seq = -1
	}
	if w, ok := mem.(interface{ UsesA12IRQ() bool }); ok {
		p.a12Watched = w.UsesA12IRQ()
	}
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

// latchValue returns the IO latch with decay applied.
func (p *PPU) latchValue() uint8 {
	p.applyDecay()
	return p.ioLatch
}

// applyDecay brings the per-bit decay countdowns up to date
// for the dots accumulated in decayDebt. The countdowns are
// linear (one per dot, clearing the bit on the dot the count
// reaches zero), so folding a bulk elapsed count in gives
// exactly the state the per-dot loop used to produce.
func (p *PPU) applyDecay() {
	d := p.decayDebt
	if d == 0 {
		return
	}
	p.decayDebt = 0
	for i := 0; i < 8; i++ {
		if p.ioLatch&(1<<uint(i)) == 0 {
			continue
		}
		if uint32(d) >= p.ioDecay[i] {
			p.ioLatch &^= 1 << uint(i)
			p.ioDecay[i] = 0
		} else {
			p.ioDecay[i] -= uint32(d)
		}
	}
}

// setLatch drives the full latch (a write, or a read that
// drives all eight bits), refreshing every set bit's decay.
func (p *PPU) setLatch(v uint8) {
	p.applyDecay()
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
	p.applyDecay()
	p.ioLatch = (p.ioLatch & 0xC0) | (v & 0x3F)
	for i := 0; i < 6; i++ {
		if v&(1<<uint(i)) != 0 {
			p.ioDecay[i] = decayLifetimeDots[i]
		}
	}
}

// decayTick records one more elapsed dot against the latch
// decay. The countdowns themselves are folded in lazily by
// applyDecay.
func (p *PPU) decayTick() {
	p.decayDebt++
}

// dotMark is one recorded bus sync point: the dot it names,
// the $2000 value in force there, and (once rendering has
// crossed the dot) the vblank flag as it stood there.
type dotMark struct {
	seq    int
	dot    int64
	ctrl   uint8
	vbl    bool
	filled bool
}

// Advance records n more owed dots without rendering them.
// The segment boundary is kept because SetInLastTick is
// defined relative to the most recent advance call, exactly
// as it was relative to the most recent Tick call when Tick
// rendered eagerly.
func (p *PPU) Advance(n int) {
	if n <= 0 {
		return
	}
	p.pending += n
	// The segment queue is consumed from segHead while new
	// ends append at the tail, and the two interleave: in
	// continuous play the queue rarely drains fully (the
	// render frontier trails the newest segment by at least
	// the in-flight access), so without compaction the
	// backing array grows by every segment ever recorded
	// and is recopied at each doubling for the whole
	// session. Dropping the consumed prefix once it passes
	// a threshold keeps the array at the size of the
	// genuine render lag instead.
	if p.segHead >= 1024 || (p.segHead > 0 && p.segHead == len(p.segEnds)) {
		copy(p.segEnds, p.segEnds[p.segHead:])
		p.segEnds = p.segEnds[:len(p.segEnds)-p.segHead]
		p.segHead = 0
	}
	p.segEnds = append(p.segEnds, p.executed+int64(p.pending))
}

// CatchUp renders every owed dot.
func (p *PPU) CatchUp() { p.renderTo(p.executed + int64(p.pending)) }

// CatchUpTo renders up to the given absolute dot count (a
// no-op when rendering is already past it).
func (p *PPU) CatchUpTo(d int64) {
	if d > p.executed {
		p.renderTo(d)
	}
}

// Target returns the absolute dot count rendering will reach
// when every owed dot is rendered.
func (p *PPU) Target() int64 { return p.executed + int64(p.pending) }

// renderTo executes dots until executed reaches d. Bulk
// spans (see bulkRoom) advance in closed form; live spans
// step dot by dot to the end of the current scanline before
// bulk eligibility is checked again, so the check costs
// nothing per dot. The segment and mark bookkeeping rides
// the step loop as two compares per dot against the next
// segment end and the oldest unfilled mark.
func (p *PPU) renderTo(d int64) {
	for p.executed < d {
		if n := p.bulkRoom(d); n > 0 {
			p.advanceBulk(n)
			continue
		}
		line := p.scanline
		for p.executed < d && p.scanline == line {
			// Burst to the next bookkeeping stop: the
			// next segment end or the oldest unfilled
			// mark, whichever comes first. Between stops
			// no bookkeeping can come due, so the checks
			// below run once per burst instead of once
			// per dot. The stops are the same dots the
			// per-dot checks used to fire on, so the
			// accounting lands identically.
			stop := d
			if p.segHead < len(p.segEnds) && p.segEnds[p.segHead] < stop {
				stop = p.segEnds[p.segHead]
			}
			for mf := p.markFill; mf >= 0; {
				m := &p.marks[mf&31]
				if m.seq != mf {
					break
				}
				if !m.filled {
					if m.dot < stop {
						stop = m.dot
					}
					break
				}
				mf++
				if mf > p.markSeq {
					break
				}
			}
			for p.executed < stop && p.scanline == line {
				p.step()
				p.executed++
				p.pending--
			}
			// A segment end completes the SetInLastTick
			// accounting for the segment that just
			// finished: the value counts the dots of
			// that segment that followed the vblank set
			// dot, or stays -1 when the set dot was not
			// in it.
			if p.segHead < len(p.segEnds) && p.segEnds[p.segHead] == p.executed {
				end := p.segEnds[p.segHead]
				if p.lastSet > p.segPrev && p.lastSet <= end {
					p.SetInLastTick = int(end - p.lastSet)
				} else {
					p.SetInLastTick = -1
				}
				p.segPrev = end
				p.segHead++
				if p.segHead == len(p.segEnds) {
					p.segEnds = p.segEnds[:0]
					p.segHead = 0
				}
			}
			// Marks fill in as their dots render. A mark
			// the record-time snapshot already filled is
			// only stepped over, never refilled: its
			// value is the flag at its dot, not the flag
			// now.
			for p.markFill >= 0 {
				m := &p.marks[p.markFill&31]
				if m.seq != p.markFill {
					break
				}
				if !m.filled {
					if m.dot > p.executed {
						break
					}
					m.vbl = p.vblFlag
					m.filled = true
				}
				p.markFill++
				if p.markFill > p.markSeq {
					p.markFill = -1
				}
			}
		}
	}
}

// bulkRoom returns how many of the dots owed toward d can
// be bulk-advanced from the current position: dots on which
// the per-dot machinery provably does nothing but advance
// the position, run the open-bus decay counter, and present
// a constant address to the cartridge. That is the dead
// lines (240-260) with rendering on, and every line with
// rendering off (fetches, evaluation, and scroll work are
// all gated on rendering; the visible lines owe only the
// backdrop fill). The vblank set dot and the pre-render
// clear dot are stepped singly, never bulked over: both
// carry flag edges other logic races against. The room is
// also capped at the next segment end and the oldest
// unfilled mark, the two bookkeeping stops.
func (p *PPU) bulkRoom(d int64) int64 {
	off := !p.rendering()
	if !off && (p.scanline < 240 || p.scanline == preLine) {
		return 0
	}
	if p.dot == 1 && (p.scanline == vblSetLine || p.scanline == preLine) {
		return 0
	}
	room := d - p.executed
	pos := int64(p.scanline*dotsPerLine + p.dot)
	if !off {
		// The pre-render line runs fetches with rendering
		// on, so the dead region ends where it starts.
		if r := int64(preLine*dotsPerLine) - pos; r < room {
			room = r
		}
	}
	// The timing event dots are single-stepped: cap the
	// room at the distance to each, wrapping the frame
	// boundary when rendering is off (the only mode whose
	// spans can cross it, since its frames have no
	// odd-frame skip).
	frame := int64(linesPerFrame * dotsPerLine)
	for _, ev := range []int64{vblSetLine*dotsPerLine + 1, preLine*dotsPerLine + 1} {
		dist := ev - pos
		if dist < 0 {
			if !off {
				continue
			}
			dist += frame
		}
		if dist < room {
			room = dist
		}
	}
	return room
}

// advanceBulk executes n dots of a bulk-eligible span in
// closed form: the constant bus address's A12 effect, the
// decay counter, the backdrop fill on visible lines when
// rendering is off (the exact value outputPixel would
// store, palette-space v included), and the position
// advance with its frame counter and odd-frame rules.
func (p *PPU) advanceBulk(n int64) {
	off := !p.rendering()
	p.bulkOff = off
	if off {
		idx := p.palette[0]
		if a := p.v & 0x3FFF; a >= 0x3F00 {
			idx = p.palette[paletteIndex(a)]
		}
		if p.mask&0x01 != 0 {
			idx &= 0x30
		}
		p.bulkBackdrop = p.colorRGB(idx)
	}
	if p.a12Watched {
		if p.v&0x1000 != 0 {
			if !p.prevA12 && p.a12Low >= a12MinLowDots {
				p.mem.ClockA12()
			}
			p.prevA12 = true
			p.a12Low = 0
		} else {
			p.prevA12 = false
			p.a12Low += int(n)
		}
	}
	p.decayDebt += int(n)
	for n > 0 {
		chunk := n
		if p.segHead < len(p.segEnds) {
			if r := p.segEnds[p.segHead] - p.executed; r < chunk {
				chunk = r
			}
		}
		if p.markFill >= 0 {
			if m := &p.marks[p.markFill&31]; m.seq == p.markFill && !m.filled {
				if r := m.dot - p.executed; r < chunk {
					chunk = r
				}
			}
		}
		p.bulkDots(chunk)
		n -= chunk
		// The chunk ended on a segment end or a mark dot
		// (or the span's end): complete the bookkeeping
		// for what it crossed.
		if p.segHead < len(p.segEnds) && p.segEnds[p.segHead] == p.executed {
			end := p.segEnds[p.segHead]
			if p.lastSet > p.segPrev && p.lastSet <= end {
				p.SetInLastTick = int(end - p.lastSet)
			} else {
				p.SetInLastTick = -1
			}
			p.segPrev = end
			p.segHead++
			if p.segHead == len(p.segEnds) {
				p.segEnds = p.segEnds[:0]
				p.segHead = 0
			}
		}
		for p.markFill >= 0 {
			m := &p.marks[p.markFill&31]
			if m.seq != p.markFill {
				break
			}
			if !m.filled {
				if m.dot > p.executed {
					break
				}
				m.vbl = p.vblFlag
				m.filled = true
			}
			p.markFill++
			if p.markFill > p.markSeq {
				p.markFill = -1
			}
		}
	}
}

// bulkDots advances the position (and the visible-line
// backdrop fill when rendering is off) by n dots of a bulk
// span whose backdrop value and A12 effect the caller has
// already applied for the whole span.
func (p *PPU) bulkDots(n int64) {
	rem := int(n)
	for rem > 0 {
		take := dotsPerLine - p.dot
		if take > rem {
			take = rem
		}
		if p.bulkOff && p.scanline < 240 {
			lo := p.dot
			if lo < 1 {
				lo = 1
			}
			hi := p.dot + take
			if hi > 257 {
				hi = 257
			}
			row := p.framebuffer[p.scanline*Width:]
			for pos := lo; pos < hi; pos++ {
				row[pos-1] = p.bulkBackdrop
			}
		}
		p.dot += take
		rem -= take
		if p.dot == dotsPerLine {
			p.dot = 0
			p.scanline++
			if p.scanline == linesPerFrame {
				p.scanline = 0
				p.oddFrame = !p.oddFrame
			}
			if p.scanline == vblSetLine {
				p.frames++
			}
		}
	}
	p.executed += n
	p.pending -= int(n)
}

// Mark records a bus sync point at the current target dot,
// under the machine's per-access sequence number. Marks must
// be recorded in sequence order.
func (p *PPU) Mark(seq int) {
	m := &p.marks[seq&31]
	m.seq = seq
	m.dot = p.Target()
	m.ctrl = p.ctrl
	m.vbl = false
	m.filled = false
	if m.dot <= p.executed {
		// The access that records this mark caught the
		// chip up inside its own dispatch (register
		// accesses do), so the mark's dot has already
		// rendered and rendering will never cross it
		// again. The flag as it stands is the flag at
		// the dot: nothing has rendered since, and the
		// dispatch itself (a $2002 read's flag clear,
		// above all) is part of the point's state,
		// exactly as the eager sync read it after the
		// access.
		m.vbl = p.vblFlag
		m.filled = true
	}
	if p.markFill < 0 {
		p.markFill = seq
	}
	p.markSeq = seq
}

// MarkDot returns the absolute dot recorded under seq, for
// a caller that wants to render to a run of marks once and
// then read the marks back without rendering per mark. ok
// is false when the mark is no longer on record.
func (p *PPU) MarkDot(seq int) (int64, bool) {
	m := &p.marks[seq&31]
	if m.seq != seq {
		return 0, false
	}
	return m.dot, true
}

// MarkNMI returns the NMI output level (vblank flag AND $2000
// enable) as it stood at the sync point recorded under seq,
// rendering up to that dot first if rendering has not reached
// it. ok is false when the mark is no longer on record.
func (p *PPU) MarkNMI(seq int) (level bool, ok bool) {
	m := &p.marks[seq&31]
	if m.seq != seq {
		return false, false
	}
	if !m.filled {
		p.CatchUpTo(m.dot)
	}
	if !m.filled {
		return false, false
	}
	return m.vbl && m.ctrl&0x80 != 0, true
}

// resetLazy clears the advance bookkeeping after a state
// load: the restored position is the new epoch, fully caught
// up by definition.
func (p *PPU) resetLazy() {
	p.pending = 0
	p.sprPatKey = -1 // a load can replace secondary OAM under a matching key
	p.executed = 0
	p.segEnds = p.segEnds[:0]
	p.segHead = 0
	p.segPrev = 0
	p.lastSet = -1
	p.markFill = -1
	p.markSeq = 0
	for i := range p.marks {
		p.marks[i].seq = -1
		p.marks[i].filled = false
	}
}
