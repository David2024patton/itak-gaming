package vdp2

// Window rectangles come from the WPS and WPE register pairs, or
// from a line window table for the X range when that window has
// its line window enable bit set. Ranges are inclusive.

// windowInside reports whether screen dot (x, y) is inside the
// window numbered w, which is 0 or 1.
func (v *VDP2) windowInside(w, x, y int) bool {
	sxReg := 0x0C0 + w*8
	exReg := 0x0C4 + w*8
	sx := int(v.reg16(sxReg) & 0x3FF)
	ex := int(v.reg16(exReg) & 0x3FF)
	upOff := 0x0D8 + w*4
	up := v.reg16(upOff)
	if up&0x8000 != 0 {
		table := (int(up&0x7) << 16) | int(v.reg16(upOff+2)&0xFFFE)
		addr := table + y*4
		sx = int(v.vramWord(addr) & 0x3FF)
		ex = int(v.vramWord(addr+2) & 0x3FF)
	}
	sy := int(v.reg16(0x0C2+w*8) & 0x1FF)
	ey := int(v.reg16(0x0C6+w*8) & 0x1FF)
	return x >= sx && x <= ex && y >= sy && y <= ey
}

// windowPass evaluates one per layer window control byte at a dot.
// The byte layout is LOG at bit 7, SWE at bit 5, SWA at bit 4,
// W1E at bit 3, W1A at bit 2, W0E at bit 1, and W0A at bit 0. When
// SWE is set, the sprite window signal replaces the rectangle
// test for the window slot selected by SWA.
func (v *VDP2) windowPass(b uint16, x, y int, sprSignal bool) bool {
	bb := byte(b)
	results := make([]bool, 0, 2)
	for w := 0; w < 2; w++ {
		enBit := uint(1 + w*2)
		areaBit := uint(0 + w*2)
		if bb&(1<<enBit) == 0 {
			continue
		}
		win := v.windowInside(w, x, y)
		if bb&0x20 != 0 && int((bb>>4)&0x1) == w {
			win = sprSignal
		}
		if bb&(1<<areaBit) != 0 {
			win = !win
		}
		results = append(results, win)
	}
	if len(results) == 0 {
		return true
	}
	if len(results) == 1 {
		return results[0]
	}
	if bb&0x80 != 0 {
		return results[0] || results[1]
	}
	return results[0] && results[1]
}

// layerWindowByte returns the window control byte for a layer.
// RBG0 and RBG1 share the RBG0 byte of WCTLC.
func (v *VDP2) layerWindowByte(layer int) uint16 {
	switch layer {
	case layerNBG0:
		return v.reg16(0x0D0) & 0xFF
	case layerNBG1:
		return v.reg16(0x0D0) >> 8
	case layerNBG2:
		return v.reg16(0x0D2) & 0xFF
	case layerNBG3:
		return v.reg16(0x0D2) >> 8
	case layerRBG0, layerRBG1:
		return v.reg16(0x0D4) & 0xFF
	case layerSprite:
		return v.reg16(0x0D4) >> 8
	}
	return 0
}

func (v *VDP2) spriteWindowByte() uint16 {
	return v.reg16(0x0D4) >> 8
}

// insideRPWindow evaluates the rotation parameter window from the
// low byte of WCTLD. Its bits are RPLOG at bit 7, RPW1E at bit 3,
// RPW1A at bit 2, RPW0E at bit 1, and RPW0A at bit 0. There is no
// sprite window input for this window.
func (v *VDP2) insideRPWindow(x, y int) bool {
	bb := byte(v.reg16(0x0D6) & 0xFF)
	results := make([]bool, 0, 2)
	for w := 0; w < 2; w++ {
		enBit := uint(1 + w*2)
		areaBit := uint(0 + w*2)
		if bb&(1<<enBit) == 0 {
			continue
		}
		win := v.windowInside(w, x, y)
		if bb&(1<<areaBit) != 0 {
			win = !win
		}
		results = append(results, win)
	}
	if len(results) == 0 {
		return true
	}
	if len(results) == 1 {
		return results[0]
	}
	if bb&0x80 != 0 {
		return results[0] || results[1]
	}
	return results[0] && results[1]
}
