package ppu

// This file is the CPU-facing register file: $2000-$2007 as the
// CPU sees them through $2000-$3FFF. Register accesses capture
// two dots into the accessing CPU cycle (the machine runs two
// of the cycle's dots, dispatches the access, syncs the
// interrupt lines on the result, and runs the third dot last),
// so race behavior against the vblank set dot resolves here,
// in terms of where the set dot fell relative to the capture
// (see SetInLastTick): when the set dot is the next dot to
// execute, the read lands one dot before it, reports the flag
// clear, and vetoes the set; when the set dot was the last
// dot before the capture, the read and the set race and the
// read reports the flag clear, per the documented behavior;
// otherwise the flag reads as it stands. Either way the read
// clears the flag.
func (p *PPU) ReadReg(r int) (val uint8) {
	switch r {
	case 2: // $2002 status
		v := p.latchValue() & 0x1F
		showVbl := p.vblFlag
		switch {
		case p.scanline == vblSetLine && p.dot == 1:
			// The set dot is the next dot to execute:
			// this read samples one dot before it. The
			// flag reads clear and the set is vetoed.
			p.suppress = true
			showVbl = false
		case p.SetInLastTick == 0:
			// The set dot was the last dot this access
			// ran before capturing: the read and the
			// set race, the read reports the flag
			// clear, and the set does not survive.
			showVbl = false
		}
		if showVbl {
			v |= 0x80
		}
		if p.spr0Hit {
			v |= 0x40
		}
		if p.sprOvrFlag {
			v |= 0x20
		}
		p.vblFlag = false
		p.w = false
		// A status read does not refresh the latch.
		return v

	case 4: // $2004 OAM data
		v := p.oamByteAt(p.oamAddr)
		if p.rendering() && p.scanline < 240 {
			// During rendering the read port is wired to
			// the sprite machinery: on visible lines it
			// returns the byte evaluation is examining,
			// when evaluation is running. Reads do not
			// advance the address.
			if p.dot >= 65 && p.dot <= 256 {
				v = p.evalByte
			}
		}
		p.setLatch(v)
		return v

	case 7: // $2007 data
		addr := p.v & 0x3FFF
		var v uint8
		if addr >= 0x3F00 {
			// Palette reads are immediate, and the read
			// buffer fills from the nametable byte under
			// the palette address ("shadow" VRAM).
			v = p.palette[paletteIndex(addr)] | (p.latchValue() & 0xC0)
			p.readBuf = p.readMem(addr - 0x1000)
			p.setLatchLow(v)
		} else {
			v = p.readBuf
			p.readBuf = p.readMem(addr)
			p.setLatch(v)
		}
		p.incVOnAccess()
		return v

	default: // write-only registers read back the latch
		return p.latchValue()
	}
}

// PeekReg reads register r without side effects, for the
// machine's Peek path: $2002 reports its flags without clearing
// anything, $2007 reports the buffer, the rest report the
// latch.
func (p *PPU) PeekReg(r int) uint8 {
	switch r {
	case 2:
		v := p.latchValue() & 0x1F
		if p.vblFlag {
			v |= 0x80
		}
		if p.spr0Hit {
			v |= 0x40
		}
		if p.sprOvrFlag {
			v |= 0x20
		}
		return v
	case 4:
		return p.oamByteAt(p.oamAddr)
	case 7:
		if p.v&0x3FFF >= 0x3F00 {
			return p.palette[paletteIndex(p.v)]
		}
		return p.readBuf
	default:
		return p.latchValue()
	}
}

// oamByteAt returns an OAM byte as reads report it: the third
// byte of each sprite (its attributes) has three unused bits
// that read back as zero.
func (p *PPU) oamByteAt(addr uint8) uint8 {
	v := p.oam[addr]
	if addr&3 == 2 {
		v &= 0xE3
	}
	return v
}

// WriteReg writes register r (0-7). Writes land two dots into
// the accessing CPU cycle, like reads. A $2000 write that
// enables NMI while the flag is set raises the NMI output the
// machine samples at the access boundary (an "immediate" NMI,
// serviced after one more instruction per the CPU's poll
// point); writes near the vblank set dot race the same way
// reads do, through the sampled line levels.
func (p *PPU) WriteReg(r int, val uint8) {
	p.setLatch(val)
	switch r {
	case 0: // $2000 control
		p.ctrl = val
		// Nametable select goes to t bits 10-11.
		p.t = (p.t & 0xF3FF) | (uint16(val&3) << 10)
	case 1: // $2001 mask
		p.mask = val
	case 2: // $2002 is read-only; the write only sets the latch
	case 3: // $2003 OAM address
		p.oamAddr = val
	case 4: // $2004 OAM data
		p.oam[p.oamAddr] = val
		p.oamAddr++
	case 5: // $2005 scroll
		if !p.w {
			p.t = (p.t & 0xFFE0) | (uint16(val) >> 3)
			p.x = val & 7
			p.w = true
		} else {
			p.t = (p.t & 0x0C1F) |
				(uint16(val&7) << 12) | (uint16(val>>3) << 5)
			p.w = false
		}
	case 6: // $2006 address
		if !p.w {
			p.t = (p.t & 0x00FF) | (uint16(val&0x3F) << 8)
			p.w = true
		} else {
			p.t = (p.t & 0xFF00) | uint16(val)
			p.v = p.t
			p.w = false
		}
	case 7: // $2007 data
		p.writeMem(p.v&0x3FFF, val)
		p.incVOnAccess()
	}
}

// incVOnAccess advances v after a $2007 access. Outside
// rendering it is the documented +1 or +32 step (by $2000 bit
// 2). During rendering the access instead makes the PPU
// perform the increments its pipeline performs at the ends of
// fetch groups and at dot 256: coarse X and Y both advance,
// the "glitchy" increment games must avoid mid-frame.
func (p *PPU) incVOnAccess() {
	if p.rendering() && (p.scanline < 240 || p.scanline == preLine) {
		p.incCoarseX()
		p.incY()
		return
	}
	if p.ctrl&0x04 != 0 {
		p.v += 32
	} else {
		p.v++
	}
	p.v &= 0x7FFF
}

// incCoarseX advances v's coarse X, switching the horizontal
// nametable at the edge.
func (p *PPU) incCoarseX() {
	if p.v&0x001F == 31 {
		p.v &^= 0x001F
		p.v ^= 0x0400
	} else {
		p.v++
	}
}

// incY advances v's fine and coarse Y, switching the vertical
// nametable at the bottom edge.
func (p *PPU) incY() {
	if p.v&0x7000 != 0x7000 {
		p.v += 0x1000
		return
	}
	p.v &^= 0x7000
	y := (p.v >> 5) & 0x1F
	switch {
	case y == 29:
		y = 0
		p.v ^= 0x0800
	case y == 31:
		y = 0
	default:
		y++
	}
	p.v = (p.v & 0xFC1F) | (y << 5)
}

// DMAWrite lands one OAM DMA byte through the $2004 port: it
// stores at the current OAM address, which advances with
// wraparound. A full 256-byte DMA therefore leaves the address
// where it started.
func (p *PPU) DMAWrite(val uint8) {
	p.oam[p.oamAddr] = val
	p.oamAddr++
}
