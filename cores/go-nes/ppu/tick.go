package ppu

import "math/bits"

// This file is the dot machine: Tick advances the PPU, and
// each dot runs the pixel output, the fetch pipeline, sprite
// evaluation and fetches, the timing events (vblank set, flag
// clear, the odd-frame skip), and the A12 bookkeeping the
// cartridge watches.
//
// Within a dot the order is: pixel out (from the shift
// registers as the previous dot left them), background
// shifters advance, fetch phase actions (latch reads, shifter
// loads, scroll increments), sprite evaluation, the scroll
// reloads, the timing events, and finally the A12 sample for
// the dot.

// Tick advances n dots and renders them immediately. The
// machine no longer calls it per bus access (it records owed
// dots with Advance and syncs with CatchUp at the points that
// observe the chip), but Tick keeps its exact eager semantics
// for the unit tests and any other direct driver: the segment
// machinery in renderTo reproduces the SetInLastTick value
// this call has always produced.
func (p *PPU) Tick(n int) {
	p.Advance(n)
	p.CatchUp()
}

// step executes the dot at the current position, then advances
// the position.
func (p *PPU) step() {
	line, dot := p.scanline, p.dot
	visible := line < 240
	prerender := line == preLine
	fetchLine := visible || prerender

	// Pixel output for the dot, from the shift registers as
	// they stand.
	if visible && dot >= 1 && dot <= 256 {
		p.outputPixel(line, dot-1)
	}

	if fetchLine && p.rendering() {
		// Background shifters advance on every fetch dot of
		// the tile groups.
		if (dot >= 1 && dot <= 256) || (dot >= 321 && dot <= 336) {
			p.shiftBG()
		}
		if dot >= 1 && dot <= 340 {
			p.fetchDot(line, dot)
		}
		if dot == 1 || (dot >= 65 && dot <= 256) {
			p.evalDot(line, dot)
		}
		// Scroll reloads.
		if dot == 257 {
			p.v = (p.v & 0x7BE0) | (p.t & 0x041F)
		}
		if prerender && dot >= 280 && dot <= 304 {
			p.v = (p.v & 0x041F) | (p.t & 0x7BE0)
		}
	}

	// Timing events.
	switch {
	case line == vblSetLine && dot == 1:
		if p.suppress {
			p.suppress = false
		} else {
			p.vblFlag = true
			// The set dot is the (executed+1)-th dot
			// rendered; renderTo's segment accounting
			// measures SetInLastTick from here.
			p.lastSet = p.executed + 1
		}
	case line == preLine && dot == 1:
		p.vblFlag = false
		p.spr0Hit = false
		p.sprOvrFlag = false
	}

	if p.a12Watched {
		p.trackA12()
	}
	p.decayTick()

	// Advance the position. On odd frames with rendering
	// enabled, the pre-render line loses its last dot.
	p.dot++
	if p.dot == dotsPerLine || (line == preLine && p.dot == dotsPerLine-1 && p.oddFrame && p.rendering()) {
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

// fetchDot runs the fetch phase actions for one dot: the
// background tile groups and the sprite fetch slots. Each
// memory access spans two dots (address phase, then the byte
// is latched); the group of eight dots per tile ends with the
// shifter load and a scroll increment.
func (p *PPU) fetchDot(line, dot int) {
	if (dot >= 1 && dot <= 256) || (dot >= 321 && dot <= 336) {
		switch (dot - 1) % 8 {
		case 1: // nametable byte latched
			p.ntLatch = p.readMem(p.ntAddress())
		case 3: // attribute byte latched
			p.atLatch = p.readMem(p.atAddress())
		case 5: // pattern low byte latched
			p.patLoLatch = p.mem.ReadCHR(p.bgPatternAddress())
		case 7: // pattern high byte latched; reload shifters
			p.patHiLatch = p.mem.ReadCHR(p.bgPatternAddress() + 8)
			p.loadBGShifters()
			if dot == 256 {
				p.incY()
			} else {
				p.incCoarseX()
			}
		}
	}
	// Sprite fetches for the next line: eight slots of eight
	// dots, each reading a garbage nametable byte twice (the
	// latches are discarded) and then the sprite's pattern
	// bytes.
	if dot >= 257 && dot <= 320 {
		slot := (dot - 257) / 8
		switch (dot - 257) % 8 {
		case 5:
			p.patLoLatch = p.mem.ReadCHR(p.sprPatternAddress(slot))
		case 7:
			p.patHiLatch = p.mem.ReadCHR(p.sprPatternAddress(slot) + 8)
			p.loadSprite(slot)
		}
	}
}

// shiftBG advances the background shift registers by one
// pixel.
func (p *PPU) shiftBG() {
	p.bgShiftLo <<= 1
	p.bgShiftHi <<= 1
	p.atShiftLo <<= 1
	p.atShiftHi <<= 1
}

// loadBGShifters loads the fetched tile into the low halves of
// the background shifters and the attribute bits (selected for
// the tile's quadrant of v) into the attribute shifters' low
// halves.
func (p *PPU) loadBGShifters() {
	p.bgShiftLo = (p.bgShiftLo & 0xFF00) | uint16(p.patLoLatch)
	p.bgShiftHi = (p.bgShiftHi & 0xFF00) | uint16(p.patHiLatch)
	bits := (p.atLatch >> ((p.v>>4)&4 | (p.v>>2)&2)) & 3
	var lo, hi uint16
	if bits&1 != 0 {
		lo = 0xFF
	}
	if bits&2 != 0 {
		hi = 0xFF
	}
	p.atShiftLo = (p.atShiftLo & 0xFF00) | lo
	p.atShiftHi = (p.atShiftHi & 0xFF00) | hi
}

// ntAddress is the fetch address of the current nametable
// byte.
func (p *PPU) ntAddress() uint16 { return 0x2000 | (p.v & 0x0FFF) }

// atAddress is the fetch address of the current attribute byte.
func (p *PPU) atAddress() uint16 {
	return 0x23C0 | (p.v & 0x0C00) | ((p.v >> 4) & 0x38) | ((p.v >> 2) & 0x07)
}

// bgPatternAddress is the fetch address of the current
// background tile's low pattern byte. The background pattern
// table select is PPUCTRL bit 4 (the sprite table select is
// bit 3; the two were swapped until 2026-10-15, which fetched
// every background tile from the sprite half of CHR and
// garbled the backgrounds of real games while the CPU and
// register test suites still passed).
func (p *PPU) bgPatternAddress() uint16 {
	base := uint16(0)
	if p.ctrl&0x10 != 0 {
		base = 0x1000
	}
	return base | (uint16(p.ntLatch) << 4) | ((p.v >> 12) & 7)
}

// sprPatternAddress is the fetch address of the low pattern
// byte for the sprite in the given secondary slot, with the
// vertical flip and the 8x16 tile pairing applied. The row is
// computed against the line the fetch prepares (the next
// line, or line 0 from the pre-render line).
func (p *PPU) sprPatternAddress(slot int) uint16 {
	target := p.scanline + 1
	if p.scanline == preLine {
		target = 0
	}
	if slot >= p.evalCount {
		// Empty slots fetch tile $FF from the sprite table
		// and the result is discarded. The sprite pattern
		// table select is PPUCTRL bit 3.
		base := uint16(0)
		if p.ctrl&0x08 != 0 {
			base = 0x1000
		}
		return base | (0xFF << 4)
	}
	y := int(p.secOAM[slot*4])
	tile := p.secOAM[slot*4+1]
	attr := p.secOAM[slot*4+2]
	row := target - (y + 1)
	if p.ctrl&0x20 != 0 { // 8x16 sprites
		if attr&0x80 != 0 {
			row = 15 - row
		}
		base := uint16(tile&1) << 12
		t := uint16(tile & 0xFE)
		if row >= 8 {
			t++
			row -= 8
		}
		return base | (t << 4) | uint16(row)
	}
	if attr&0x80 != 0 {
		row = 7 - row
	}
	base := uint16(0)
	if p.ctrl&0x08 != 0 {
		base = 0x1000
	}
	return base | (uint16(tile) << 4) | uint16(row)
}

// loadSprite finishes a sprite fetch: the pattern bytes land
// in the slot's shift registers (bit-reversed when the sprite
// is flipped horizontally, so output is always MSB first), and
// the X counter and attributes are latched.
func (p *PPU) loadSprite(slot int) {
	if slot == 0 {
		p.sprLiveMask = 0
	}
	if slot >= p.evalCount {
		p.sprShLo[slot] = 0
		p.sprShHi[slot] = 0
		p.sprXCnt[slot] = 0xFF
		p.sprAttr[slot] = 0
		p.sprLineIx[slot] = -1
		return
	}
	lo, hi := p.patLoLatch, p.patHiLatch
	attr := p.secOAM[slot*4+2]
	if attr&0x40 != 0 { // horizontal flip
		lo = reverseBits(lo)
		hi = reverseBits(hi)
	}
	p.sprShLo[slot] = lo
	p.sprShHi[slot] = hi
	p.sprAttr[slot] = attr
	p.sprXCnt[slot] = p.secOAM[slot*4+3]
	p.sprLineIx[slot] = p.secIndex[slot]
	if lo|hi != 0 {
		p.sprLiveMask |= 1 << uint(slot)
	}
}

func reverseBits(b uint8) uint8 {
	b = (b&0x55)<<1 | (b&0xAA)>>1
	b = (b&0x33)<<2 | (b&0xCC)>>2
	b = b<<4 | b>>4
	return b
}

// evalDot runs sprite evaluation for the line being prepared:
// secondary OAM clears at dot 1 (in hardware the clear writes
// run across dots 1-64; nothing can observe them mid-clear, so
// they land together), and the evaluation machine reads one
// OAM byte every two dots on dots 65-256. It runs on visible
// lines (preparing the next line) and on the pre-render line
// (preparing line 0).
func (p *PPU) evalDot(line, dot int) {
	switch {
	case dot == 1:
		for i := range p.secOAM {
			p.secOAM[i] = 0xFF
		}
		for i := range p.secIndex {
			p.secIndex[i] = -1
		}
		p.evalN = 0
		p.evalM = 0
		p.evalCount = 0
		p.evalState = evalCopyY
	case dot >= 65 && dot <= 256 && dot%2 == 0 && p.evalState != evalDone:
		target := line + 1
		if line == preLine {
			target = 0
		}
		p.evalRead(target)
	}
}

// evalRead performs one evaluation memory read (one byte) for
// the line being prepared.
func (p *PPU) evalRead(target int) {
	height := p.spriteHeight()
	switch p.evalState {
	case evalCopyY:
		b := p.oam[p.evalN*4]
		p.evalByte = b
		row := target - (int(b) + 1)
		if row >= 0 && row < height {
			// In range: this sprite is copied into
			// secondary OAM, starting with its Y byte.
			p.secOAM[p.evalCount*4] = b
			p.secIndex[p.evalCount] = p.evalN
			p.evalM = 1
			p.evalState = evalCopyRest
		} else {
			p.evalN++
			if p.evalN == 64 {
				p.evalState = evalDone
			}
		}
	case evalCopyRest:
		b := p.oam[p.evalN*4+p.evalM]
		p.evalByte = b
		p.secOAM[p.evalCount*4+p.evalM] = b
		p.evalM++
		if p.evalM == 4 {
			p.evalM = 0
			p.evalCount++
			p.evalN++
			switch {
			case p.evalN == 64:
				p.evalState = evalDone
			case p.evalCount == 8:
				p.evalState = evalBug
			default:
				p.evalState = evalCopyY
			}
		}
	case evalBug:
		// Secondary OAM is full. The hardware's check for a
		// ninth in-range sprite reads OAM diagonally: the
		// byte index is not reset between sprites, so each
		// read examines byte evalM of the next sprite with
		// both counters advancing together, and any byte
		// that looks like an in-range Y coordinate sets
		// the overflow flag.
		b := p.oam[p.evalN*4+p.evalM]
		p.evalByte = b
		row := target - (int(b) + 1)
		if row >= 0 && row < height {
			p.sprOvrFlag = true
		}
		p.evalM = (p.evalM + 1) & 3
		p.evalN++
		if p.evalN == 64 {
			p.evalState = evalDone
		}
	}
}

// outputPixel composes and stores the pixel at (line, x).
func (p *PPU) outputPixel(line, x int) {
	if !p.rendering() {
		// Rendering off: the whole screen is the backdrop
		// color, or the palette entry the current VRAM
		// address points at when v is in palette space.
		idx := p.palette[0]
		if a := p.v & 0x3FFF; a >= 0x3F00 {
			idx = p.palette[paletteIndex(a)]
		}
		if p.mask&0x01 != 0 {
			idx &= 0x30
		}
		p.framebuffer[line*Width+x] = p.colorRGB(idx)
		return
	}

	// Background pixel from the shift registers.
	bgBits := uint8(0)
	bgPal := uint8(0)
	if p.bgEnabled() {
		bit := uint(15 - p.x)
		bgBits = uint8((p.bgShiftLo>>bit)&1) | uint8((p.bgShiftHi>>bit)&1)<<1
		attr := uint8((p.atShiftLo>>bit)&1) | uint8((p.atShiftHi>>bit)&1)<<1
		bgPal = attr<<2 | bgBits
		if x < 8 && p.mask&0x02 == 0 {
			bgBits = 0
		}
	}

	// Sprite pixel: the first slot (lowest index has
	// priority) with an opaque pixel whose X counter has
	// run out.
	sprBits := uint8(0)
	sprPal := uint8(0)
	sprBehind := false
	sprIsZero := false
	if p.sprEnabled() {
		for m := p.sprLiveMask; m != 0; m &= m - 1 {
			i := bits.TrailingZeros8(m)
			if p.sprXCnt[i] != 0 {
				continue
			}
			b := (p.sprShLo[i] >> 7) & 1
			b |= ((p.sprShHi[i] >> 7) & 1) << 1
			if b != 0 {
				sprBits = b
				sprPal = (p.sprAttr[i]&3)<<2 | b
				sprBehind = p.sprAttr[i]&0x20 != 0
				sprIsZero = p.sprLineIx[i] == 0
				break
			}
		}
		if x < 8 && p.mask&0x04 == 0 {
			sprBits = 0
		}
	}
	// Sprite shift registers and X counters run on every
	// output dot, whether or not a pixel was found. Only
	// the live slots run: a dead slot's shifters hold zero
	// pattern (that is what the live mask records), so
	// shifting them is a no-op and their counters gate a
	// pixel that can never be opaque.
	for m := p.sprLiveMask; m != 0; m &= m - 1 {
		i := bits.TrailingZeros8(m)
		if p.sprXCnt[i] == 0 {
			p.sprShLo[i] <<= 1
			p.sprShHi[i] <<= 1
		} else {
			p.sprXCnt[i]--
		}
	}

	// Sprite 0 hit: sprite 0's opaque pixel overlaps an
	// opaque background pixel with both layers rendered,
	// outside the clipped left column, and not at x = 255.
	if sprIsZero && sprBits != 0 && bgBits != 0 &&
		!(x < 8 && (p.mask&0x02 == 0 || p.mask&0x04 == 0)) && x != 255 {
		p.spr0Hit = true
	}

	var palIdx int
	switch {
	case bgBits != 0 && sprBits != 0:
		if sprBehind {
			palIdx = int(bgPal)
		} else {
			palIdx = 0x10 + int(sprPal)
		}
	case sprBits != 0:
		palIdx = 0x10 + int(sprPal)
	case bgBits != 0:
		palIdx = int(bgPal)
	default:
		palIdx = 0
	}
	idx := p.palette[palIdx]
	if palIdx&3 == 0 {
		idx = p.palette[0]
	}
	if p.mask&0x01 != 0 { // grayscale
		idx &= 0x30
	}
	p.framebuffer[line*Width+x] = p.colorRGB(idx)
}

// trackA12 observes the address the PPU drove for the dot
// just executed and clocks the cartridge's A12 hook on
// filtered rising edges.
func (p *PPU) trackA12() {
	a12 := p.busAddress()&0x1000 != 0
	if a12 {
		if !p.prevA12 && p.a12Low >= a12MinLowDots {
			p.mem.ClockA12()
		}
		p.a12Low = 0
	} else {
		p.a12Low++
	}
	p.prevA12 = a12
}

// busAddress is the address on the PPU's address bus for the
// dot just executed (the position still names that dot when
// this runs): the fetch address during fetch windows when
// rendering, v at all other times.
func (p *PPU) busAddress() uint16 {
	line, dot := p.scanline, p.dot
	if p.rendering() && (line < 240 || line == preLine) {
		switch {
		case dot == 0:
			// The idle dot drives no fetch; the bus holds
			// the nametable address of the tile the line is
			// about to fetch. (Presenting v here would leak
			// fine Y's low bit, bit 12 of v, onto A12.)
			return p.ntAddress()
		case (dot >= 1 && dot <= 256) || (dot >= 321 && dot <= 336):
			switch (dot - 1) % 8 {
			case 0, 1:
				return p.ntAddress()
			case 2, 3:
				return p.atAddress()
			case 4, 5:
				return p.bgPatternAddress()
			default:
				return p.bgPatternAddress() + 8
			}
		case dot >= 257 && dot <= 320:
			slot := (dot - 257) / 8
			switch (dot - 257) % 8 {
			case 0, 1, 2, 3:
				return p.ntAddress()
			case 4, 5:
				return p.sprPatternAddress(slot)
			default:
				return p.sprPatternAddress(slot) + 8
			}
		case dot >= 337:
			return p.ntAddress()
		}
	}
	return p.v & 0x3FFF
}
