package rdp

// State command handlers. Field layouts follow the
// published RDP command summary; each handler decodes
// its 64-bit word into the registers it owns.

// dispatchState decodes the single-word state and setup
// commands. w is the full command word.
func (r *RDP) dispatchState(op int, w uint64) {
	switch op {
	case CmdSetColorImg:
		r.colorImg = decodeImage(w)
	case CmdSetTexImage:
		r.texImg = decodeImage(w)
	case CmdSetZImage:
		r.zAddr = uint32(w & 0x3FFFFFF)
	case CmdSetTile:
		idx := int((w >> 24) & 7)
		t := &r.tiles[idx]
		t.Format = int((w >> 53) & 7)
		t.Size = int((w >> 51) & 3)
		t.Line = int((w >> 41) & 0x1FF)
		t.TMem = int((w >> 32) & 0x1FF)
		t.Palette = int((w >> 20) & 0xF)
		t.ClampT = (w>>19)&1 != 0
		t.MirrorT = (w>>18)&1 != 0
		t.MaskT = int((w >> 14) & 0xF)
		t.ShiftT = int((w >> 10) & 0xF)
		t.ClampS = (w>>9)&1 != 0
		t.MirrorS = (w>>8)&1 != 0
		t.MaskS = int((w >> 4) & 0xF)
		t.ShiftS = int(w & 0xF)
	case CmdSetTileSize, CmdLoadTile, CmdLoadTLUT:
		sl := int((w >> 44) & 0xFFF)
		tl := int((w >> 32) & 0xFFF)
		idx := int((w >> 24) & 7)
		sh := int((w >> 12) & 0xFFF)
		th := int(w & 0xFFF)
		t := &r.tiles[idx]
		t.SL, t.TL, t.SH, t.TH = sl, tl, sh, th
		switch op {
		case CmdLoadTile:
			r.loadTile(idx)
		case CmdLoadTLUT:
			r.loadTLUT(idx)
		}
	case CmdLoadBlock:
		idx := int((w >> 24) & 7)
		sl := int((w >> 44) & 0xFFF)
		tl := int((w >> 32) & 0xFFF)
		sh := int((w >> 12) & 0xFFF)
		dxt := int(w & 0xFFF)
		t := &r.tiles[idx]
		t.SL, t.TL, t.SH = sl, tl, sh
		r.loadBlock(idx, dxt)
	case CmdSetOtherMode:
		r.otherModes = w & 0x00FFFFFFFFFFFFFF
	case CmdSetCombine:
		r.combineReg = w & 0x00FFFFFFFFFFFFFF
	case CmdSetPrimColor:
		r.primMinLvl = int((w >> 40) & 0x1F)
		r.primLvlFrc = int((w >> 32) & 0xFF)
		r.primColor = decodeColor(w)
	case CmdSetEnvColor:
		r.envColor = decodeColor(w)
	case CmdSetBlendCol:
		r.blendColor = decodeColor(w)
	case CmdSetFogColor:
		r.fogColor = decodeColor(w)
	case CmdSetFillColor:
		r.fillColor = uint32(w)
	case CmdSetPrimDepth:
		r.primZ = uint16((w >> 16) & 0xFFFF)
		r.primDZ = uint16(w & 0xFFFF)
	case CmdSetScissor:
		r.sciX0 = int((w >> 44) & 0xFFF)
		r.sciY0 = int((w >> 32) & 0xFFF)
		r.sciField = (w>>25)&1 != 0
		r.sciOdd = (w>>24)&1 != 0
		r.sciX1 = int((w >> 12) & 0xFFF)
		r.sciY1 = int(w & 0xFFF)
	case CmdSetConvert:
		for i := 0; i < 6; i++ {
			v := int32((w >> uint(45-9*i)) & 0x1FF)
			if v&0x100 != 0 {
				v -= 0x200
			}
			r.conv[i] = v
		}
		// Power-on typical values if a list never sets
		// them are NOT assumed here; New leaves them
		// zero and games always program them. Tests set
		// the documented typical set explicitly.
	case CmdSetKeyR:
		r.keyR = keyChan{
			Width:  int((w >> 16) & 0xFFF),
			Center: int((w >> 8) & 0xFF),
			Scale:  int(w & 0xFF),
		}
	case CmdSetKeyGB:
		r.keyGB = keyGBRegs{
			WidthG:  int((w >> 44) & 0xFFF),
			WidthB:  int((w >> 32) & 0xFFF),
			CenterG: int((w >> 24) & 0xFF),
			ScaleG:  int((w >> 16) & 0xFF),
			CenterB: int((w >> 8) & 0xFF),
			ScaleB:  int(w & 0xFF),
		}
	case CmdFillRect:
		r.fillRect(w)
	}
}

// decodeImage decodes a Set Color/Texture Image word.
func decodeImage(w uint64) Image {
	return Image{
		Format: int((w >> 53) & 7),
		Size:   int((w >> 51) & 3),
		Width:  int((w>>32)&0x3FF) + 1,
		Addr:   uint32(w & 0x3FFFFFF),
	}
}

// decodeColor pulls the RGBA byte set out of the low 32
// bits of a color-set command word.
func decodeColor(w uint64) RGBA {
	return RGBA{
		R: uint8(w >> 24),
		G: uint8(w >> 16),
		B: uint8(w >> 8),
		A: uint8(w),
	}
}

// Other-mode field accessors (see the bit map in the
// package documentation lineage: the other-modes
// register as the RDP receives it, with the high and
// low halves already merged by the microcode upstream).

func (r *RDP) omCycleType() int  { return int((r.otherModes >> 52) & 3) }
func (r *RDP) omPerspTex() bool  { return (r.otherModes>>51)&1 != 0 }
func (r *RDP) omDetailTex() bool { return (r.otherModes>>50)&1 != 0 }
func (r *RDP) omSharpenTex() bool {
	return (r.otherModes>>49)&1 != 0
}
func (r *RDP) omTexLOD() bool      { return (r.otherModes>>48)&1 != 0 }
func (r *RDP) omEnTLUT() bool      { return (r.otherModes>>47)&1 != 0 }
func (r *RDP) omTLUTType() int     { return int((r.otherModes >> 46) & 1) }
func (r *RDP) omSampleType() int   { return int((r.otherModes >> 45) & 1) }
func (r *RDP) omMidTexel() bool    { return (r.otherModes>>44)&1 != 0 }
func (r *RDP) omBiLerp0() bool     { return (r.otherModes>>43)&1 != 0 }
func (r *RDP) omBiLerp1() bool     { return (r.otherModes>>42)&1 != 0 }
func (r *RDP) omConvertOne() bool  { return (r.otherModes>>41)&1 != 0 }
func (r *RDP) omKeyEn() bool       { return (r.otherModes>>40)&1 != 0 }
func (r *RDP) omRGBDither() int    { return int((r.otherModes >> 38) & 3) }
func (r *RDP) omAlphaDither() int  { return int((r.otherModes >> 36) & 3) }
func (r *RDP) omForceBlend() bool  { return (r.otherModes>>14)&1 != 0 }
func (r *RDP) omAlphaCvgSel() bool { return (r.otherModes>>13)&1 != 0 }
func (r *RDP) omCvgTimesAlpha() bool {
	return (r.otherModes>>12)&1 != 0
}
func (r *RDP) omZMode() int        { return int((r.otherModes >> 10) & 3) }
func (r *RDP) omCvgDest() int      { return int((r.otherModes >> 8) & 3) }
func (r *RDP) omColorOnCvg() bool  { return (r.otherModes>>7)&1 != 0 }
func (r *RDP) omImageRead() bool   { return (r.otherModes>>6)&1 != 0 }
func (r *RDP) omZUpdate() bool     { return (r.otherModes>>5)&1 != 0 }
func (r *RDP) omZCompare() bool    { return (r.otherModes>>4)&1 != 0 }
func (r *RDP) omAntialias() bool   { return (r.otherModes>>3)&1 != 0 }
func (r *RDP) omZSourcePrim() bool { return (r.otherModes>>2)&1 != 0 }
func (r *RDP) omDitherAlpha() bool { return (r.otherModes>>1)&1 != 0 }
func (r *RDP) omAlphaCompare() bool {
	return r.otherModes&1 != 0
}

// Blender mux fields for one cycle. cycle 0 uses the
// _0 field group, cycle 1 the _1 group.
func (r *RDP) omBlendMux(cycle, which int) int {
	// which: 0 m1a, 1 m1b, 2 m2a, 3 m2b
	base := [2][4]int{
		{30, 26, 22, 18}, // cycle 0: m1a_0 is bits 31..30
		{28, 24, 20, 16}, // cycle 1
	}
	return int((r.otherModes >> uint(base[cycle][which])) & 3)
}

// fillRect executes the Fill Rectangle command. Field
// layout: XL (55..44), YL (43..32) in the high word,
// XH (23..12), YH (11..0) in the low positions, all in
// 10.2 fixed point. The fill color is written directly;
// in fill and copy cycle modes both corners are
// included, in 1-cycle and 2-cycle modes the lower
// right corner follows the exclusive rule of the other
// primitives.
func (r *RDP) fillRect(w uint64) {
	xl := int((w>>44)&0xFFF) >> 2
	yl := int((w>>32)&0xFFF) >> 2
	xh := int((w>>12)&0xFFF) >> 2
	yh := int(w&0xFFF) >> 2
	if ct := r.omCycleType(); ct == 0 || ct == 1 {
		xl--
		yl--
	}
	r.fillSpan(xh, yh, xl, yl)
}

// fillSpan writes the fill color over the rectangle
// from (x0, y0) to (x1, y1) with both corners included.
// The scissor box clips the writes.
func (r *RDP) fillSpan(x0, y0, x1, y1 int) {
	for y := y0; y <= y1; y++ {
		if !r.inScissorY(y) {
			continue
		}
		for x := x0; x <= x1; x++ {
			if x < r.sciX0>>2 || x >= r.sciX1>>2 {
				continue
			}
			r.writeFillPixel(x, y)
		}
	}
}
