package ppu

import "errors"

// Save-state support: the whole PPU, memories and pipeline
// latches and timing position included, so a restored machine
// continues dot-identically. The framebuffer is not stored (it
// is redrawn every frame). The cartridge is saved by its own
// package; the PPU state covers only what lives in the chip.
//
// Layout: version byte, CIRAM (4096), OAM (256), palette (32),
// then a fixed register block of 192 bytes.

func put16(b []byte, v uint16) {
	b[0] = uint8(v)
	b[1] = uint8(v >> 8)
}

func get16(b []byte) uint16 {
	return uint16(b[0]) | uint16(b[1])<<8
}

func put32(b []byte, v uint32) {
	b[0] = uint8(v)
	b[1] = uint8(v >> 8)
	b[2] = uint8(v >> 16)
	b[3] = uint8(v >> 24)
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

// Serialize writes the PPU state into a fresh slice of
// SerializeSize bytes.
func (p *PPU) Serialize() []byte {
	out := make([]byte, SerializeSize)
	out[0] = serializeVersion
	copy(out[1:], p.ciram[:])
	copy(out[1+4096:], p.oam[:])
	copy(out[1+4096+256:], p.palette[:])
	r := out[1+4096+256+32:]
	i := 0
	r[i] = p.ctrl
	i++
	r[i] = p.mask
	i++
	r[i] = p.oamAddr
	i++
	put16(r[i:], p.v)
	i += 2
	put16(r[i:], p.t)
	i += 2
	r[i] = p.x
	i++
	if p.w {
		r[i] = 1
	}
	i++
	r[i] = p.readBuf
	i++
	r[i] = p.ioLatch
	i++
	for b := 0; b < 8; b++ {
		put32(r[i:], p.ioDecay[b])
		i += 4
	}
	var flags uint8
	if p.vblFlag {
		flags |= 1
	}
	if p.spr0Hit {
		flags |= 2
	}
	if p.sprOvrFlag {
		flags |= 4
	}
	if p.suppress {
		flags |= 8
	}
	if p.oddFrame {
		flags |= 16
	}
	if p.prevA12 {
		flags |= 32
	}

	r[i] = flags
	i++
	put32(r[i:], uint32(int32(p.SetInLastTick)))
	i += 4
	put16(r[i:], uint16(p.scanline))
	i += 2
	put16(r[i:], uint16(p.dot))
	i += 2
	put64(r[i:], p.frames)
	i += 8
	put16(r[i:], p.bgShiftLo)
	i += 2
	put16(r[i:], p.bgShiftHi)
	i += 2
	put16(r[i:], p.atShiftLo)
	i += 2
	put16(r[i:], p.atShiftHi)
	i += 2
	r[i] = p.ntLatch
	i++
	r[i] = p.atLatch
	i++
	r[i] = p.patLoLatch
	i++
	r[i] = p.patHiLatch
	i++
	for s := 0; s < 8; s++ {
		r[i] = p.sprShLo[s]
		i++
		r[i] = p.sprShHi[s]
		i++
		r[i] = p.sprAttr[s]
		i++
		r[i] = p.sprXCnt[s]
		i++
		r[i] = uint8(int8(p.sprLineIx[s]))
		i++
	}
	copy(r[i:], p.secOAM[:])
	i += 32
	for s := 0; s < 8; s++ {
		r[i] = uint8(int8(p.secIndex[s]))
		i++
	}
	r[i] = uint8(p.evalN)
	i++
	r[i] = uint8(p.evalM)
	i++
	r[i] = uint8(p.evalCount)
	i++
	r[i] = uint8(p.evalState)
	i++
	r[i] = p.evalByte
	i++
	put32(r[i:], uint32(int32(p.a12Low)))
	i += 4
	return out
}

// Deserialize restores a state produced by Serialize.
func (p *PPU) Deserialize(in []byte) error {
	if len(in) != SerializeSize {
		return errors.New("ppu: bad state size")
	}
	if in[0] != serializeVersion {
		return errors.New("ppu: bad state version")
	}
	copy(p.ciram[:], in[1:])
	copy(p.oam[:], in[1+4096:])
	copy(p.palette[:], in[1+4096+256:])
	r := in[1+4096+256+32:]
	i := 0
	p.ctrl = r[i]
	i++
	p.mask = r[i]
	i++
	p.oamAddr = r[i]
	i++
	p.v = get16(r[i:])
	i += 2
	p.t = get16(r[i:])
	i += 2
	p.x = r[i]
	i++
	p.w = r[i] != 0
	i++
	p.readBuf = r[i]
	i++
	p.ioLatch = r[i]
	i++
	for b := 0; b < 8; b++ {
		p.ioDecay[b] = get32(r[i:])
		i += 4
	}
	flags := r[i]
	i++
	p.vblFlag = flags&1 != 0
	p.spr0Hit = flags&2 != 0
	p.sprOvrFlag = flags&4 != 0
	p.suppress = flags&8 != 0
	p.oddFrame = flags&16 != 0
	p.prevA12 = flags&32 != 0
	p.SetInLastTick = int(int32(get32(r[i:])))
	i += 4
	p.scanline = int(get16(r[i:]))
	i += 2
	p.dot = int(get16(r[i:]))
	i += 2
	p.frames = get64(r[i:])
	i += 8
	p.bgShiftLo = get16(r[i:])
	i += 2
	p.bgShiftHi = get16(r[i:])
	i += 2
	p.atShiftLo = get16(r[i:])
	i += 2
	p.atShiftHi = get16(r[i:])
	i += 2
	p.ntLatch = r[i]
	i++
	p.atLatch = r[i]
	i++
	p.patLoLatch = r[i]
	i++
	p.patHiLatch = r[i]
	i++
	for s := 0; s < 8; s++ {
		p.sprShLo[s] = r[i]
		i++
		p.sprShHi[s] = r[i]
		i++
		p.sprAttr[s] = r[i]
		i++
		p.sprXCnt[s] = r[i]
		i++
		p.sprLineIx[s] = int(int8(r[i]))
		i++
	}
	copy(p.secOAM[:], r[i:])
	i += 32
	for s := 0; s < 8; s++ {
		p.secIndex[s] = int(int8(r[i]))
		i++
	}
	p.evalN = int(r[i])
	i++
	p.evalM = int(r[i])
	i++
	p.evalCount = int(r[i])
	i++
	p.evalState = int(r[i])
	i++
	p.evalByte = r[i]
	i++
	p.a12Low = int(int32(get32(r[i:])))
	return nil
}
