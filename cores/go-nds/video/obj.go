package video

// OBJ (sprite) rendering per engine, GBA entry format with the
// DS additions: OBJ mode 3 bitmap OBJs (direct color from the
// OBJ VRAM space at (attr2&0x3FF)*128 bytes per the documented
// 128-byte boundary, alpha bit 15), OBJ extended palettes for
// 8bpp OBJs, affine parameter sets with the double-size flag,
// and mosaic. The lowest OAM index wins a pixel.

var objSizes = [4][4][2]int{
	// shape 0: square
	{{8, 8}, {16, 16}, {32, 32}, {64, 64}},
	// shape 1: wide
	{{16, 8}, {32, 8}, {32, 16}, {64, 32}},
	// shape 2: tall
	{{8, 16}, {8, 32}, {16, 32}, {32, 64}},
	// shape 3: prohibited, treated as square
	{{8, 8}, {16, 16}, {32, 32}, {64, 64}},
}

func (v *Video) renderOBJ(lc *lineCtx) {
	sp := v.objSpace(lc.e)
	oam := v.bus.OAM[lc.e*0x400:]
	oneD := lc.dispcnt&0x10 != 0
	mosH, _ := lc.mosaic(true)
	for i := 127; i >= 0; i-- {
		attr0 := uint16(oam[i*8]) | uint16(oam[i*8+1])<<8
		attr1 := uint16(oam[i*8+2]) | uint16(oam[i*8+3])<<8
		attr2 := uint16(oam[i*8+4]) | uint16(oam[i*8+5])<<8
		affine := attr0&0x100 != 0
		if !affine && attr0&0x200 != 0 {
			continue // disabled
		}
		w, h := objSizes[attr0>>14][attr1>>14][0], objSizes[attr0>>14][attr1>>14][1]
		dw, dh := w, h
		if attr0&0x200 != 0 { // double-size (affine only)
			dw, dh = 2*w, 2*h
		}
		x0 := int(attr1 & 0x1FF)
		if x0 >= 256 {
			x0 -= 512
		}
		dy := (lc.y - int(attr0&0xFF)) & 0xFF
		if dy >= dh {
			continue
		}
		mode := (attr0 >> 10) & 3
		prio := uint8(attr2 >> 10 & 3)
		// Affine parameters (also fetched for the
		// non-affine path only when needed).
		var pa, pb, pc, pd int32
		if affine {
			g := int(attr1>>9) & 0x1F
			rd := func(k int) int32 {
				off := 6 + g*32 + k*8
				return int32(int16(uint16(oam[off]) | uint16(oam[off+1])<<8))
			}
			pa, pb, pc, pd = rd(0), rd(1), rd(2), rd(3)
		}
		for dx := 0; dx < dw; dx++ {
			sx := x0 + dx
			if sx < 0 || sx >= Width {
				continue
			}
			fdx := dx
			if attr0&0x1000 != 0 && mosH > 1 {
				fdx = dx - dx%mosH
			}
			var ox, oy int
			if affine {
				rx, ry := fdx-dw/2, dy-dh/2
				ox = int((pa*int32(rx)+pb*int32(ry))>>8) + w/2
				oy = int((pc*int32(rx)+pd*int32(ry))>>8) + h/2
				if ox < 0 || oy < 0 || ox >= w || oy >= h {
					continue
				}
			} else {
				ox, oy = fdx, dy
				if attr1&0x1000 != 0 {
					ox = w - 1 - ox
				}
				if attr1&0x2000 != 0 {
					oy = h - 1 - oy
				}
			}
			c, idx, ok := v.objPixel(lc, sp, attr0, attr2, ox, oy, w, oneD)
			if !ok || idx == 0 && mode != 3 {
				continue
			}
			if mode == 2 {
				lc.objWin[sx] = true
				continue
			}
			lc.obj[sx] = objPx{c: c, ok: true, semi: mode == 1, prio: prio}
		}
	}
}

// objPixel fetches one OBJ source pixel. It returns the rgb6
// color, the raw color index (for the OBJ window test), and
// whether the pixel is present at all.
func (v *Video) objPixel(lc *lineCtx, sp *space, attr0, attr2 uint16, ox, oy, w int, oneD bool) (uint32, int, bool) {
	tile := uint32(attr2 & 0x3FF)
	palNum := int(attr2 >> 12)
	if (attr0>>10)&3 == 3 {
		// Bitmap OBJ: direct color, 128 bytes per tile index
		// unit (the documented boundary simplification).
		addr := tile*128 + uint32(oy*w+ox)*2
		w16 := sp.get16(addr)
		if w16&0x8000 == 0 {
			return 0, 0, false
		}
		return rgb6From15(w16), 1, true
	}
	charsWide, rowChars := 1, 32
	if attr0&0x2000 != 0 {
		charsWide = 2
	}
	if oneD {
		rowChars = (w >> 3) * charsWide
	}
	charIdx := tile + uint32((oy>>3)*rowChars+(ox>>3)*charsWide)
	if attr0&0x2000 != 0 { // 8bpp
		idx := int(sp.get(charIdx*32 + uint32((oy&7)*8+(ox&7))))
		if idx == 0 {
			return 0, 0, false
		}
		if lc.dispcnt&(1<<31) != 0 {
			return rgb6From15(v.extOBJ[lc.e].get16(uint32(palNum*0x200 + idx*2))), idx, true
		}
		return v.stdPal(lc.e, true, idx), idx, true
	}
	b := sp.get(charIdx*32 + uint32((oy&7)*4+(ox&7)/2))
	nib := int(b & 15)
	if ox&1 != 0 {
		nib = int(b >> 4)
	}
	if nib == 0 {
		return 0, 0, false
	}
	return v.stdPal(lc.e, true, palNum*16+nib), nib, true
}
