package video

// StateSize is the serialized size of the video unit's own
// state (VRAM, palette, and OAM contents live in the bus
// stores and serialize there; the framebuffers are rebuilt by
// rendering).
const StateSize = 80

// Serialize writes the video state into dst.
func (v *Video) Serialize(dst []byte) int {
	n := 0
	put16 := func(x uint16) { dst[n] = byte(x); dst[n+1] = byte(x >> 8); n += 2 }
	put32 := func(x uint32) { put16(uint16(x)); put16(uint16(x >> 16)) }
	put16(v.lyc)
	dst[n] = v.dsEn
	n++
	dst[n] = 0
	n++
	put16(uint16(v.vcount))
	if v.hblank {
		dst[n] = 1
	}
	n++
	dst[n] = 0
	n++
	for e := 0; e < 2; e++ {
		for bg := 0; bg < 4; bg++ {
			put32(uint32(v.refX[e][bg]))
			put32(uint32(v.refY[e][bg]))
		}
	}
	if v.capOn {
		dst[n] = 1
	}
	n++
	put16(uint16(v.capLine))
	dst[n] = 0
	n++
	put32(v.mainAdr)
	return n
}

// Deserialize restores the video state and marks the VRAM
// assignment maps for rebuild.
func (v *Video) Deserialize(src []byte) int {
	n := 0
	get16 := func() uint16 { x := uint16(src[n]) | uint16(src[n+1])<<8; n += 2; return x }
	get32 := func() uint32 { return uint32(get16()) | uint32(get16())<<16 }
	v.lyc = get16()
	v.dsEn = src[n]
	n += 2
	v.vcount = int(get16())
	v.hblank = src[n] != 0
	n += 2
	for e := 0; e < 2; e++ {
		for bg := 0; bg < 4; bg++ {
			v.refX[e][bg] = int32(get32())
			v.refY[e][bg] = int32(get32())
		}
	}
	v.capOn = src[n] != 0
	n++
	v.capLine = int(get16())
	n++
	v.mainAdr = get32()
	v.mapsDirty = true
	return n
}
