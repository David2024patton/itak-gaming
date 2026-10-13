package vdp2

// Rotation parameter handling. All math is stateless per line:
// the effective parameter set for a line is rebuilt from the
// parameter table in VRAM on every fetch.

// rotParams holds one decoded parameter set in mixed fixed point
// units. Values ending in 1024 are in 1 over 1024 units, values
// ending in 65536 are in 1 over 65536 units, and px, py, cx, and
// cy are plain integers.
type rotParams struct {
	xst1024, yst1024   int32
	dxst1024, dyst1024 int32
	dx1024, dy1024     int32
	a1024, b1024       int32
	c1024, d1024       int32
	px, py             int64
	cx, cy             int64
	mx1024, my1024     int32
	kx65536, ky65536   int32
	kast1024           int32
	dkast1024          int32
	dkax1024           int32
}

// rotLead returns the lead byte addresses of sets A and B.
func (v *VDP2) rotLead() (int, int) {
	up := v.reg16(0x0BC)
	lo := v.reg16(0x0BE)
	combined := (int(up&0x7) << 15) | int((lo>>1)&0x7FFF)
	leadA := ((combined &^ 0x40) << 1) & 0x7FFFF
	return leadA, leadA + 0x80
}

// decodePx decodes the 13 bit signed integer stored in a word
// whose sign sits in bit 15 and whose value sits in bits 12 to 0.
func decodePx(w uint16) int64 {
	n := int64(w & 0x1FFF)
	if n >= 0x1000 {
		n -= 0x2000
	}
	return n
}

// parseRotEntry decodes the 0x60 byte parameter block at addr.
func (v *VDP2) parseRotEntry(addr int) rotParams {
	var p rotParams
	pair := func(off, intBits int) int32 {
		return fixed1024(v.vramWord(addr+off), v.vramWord(addr+off+2), intBits)
	}
	p.xst1024 = pair(0x00, 12)
	p.yst1024 = pair(0x04, 12)
	p.dxst1024 = pair(0x0C, 3)
	p.dyst1024 = pair(0x10, 3)
	p.dx1024 = pair(0x14, 3)
	p.dy1024 = pair(0x18, 3)
	p.a1024 = pair(0x1C, 4)
	p.b1024 = pair(0x20, 4)
	p.c1024 = pair(0x24, 4)
	p.d1024 = pair(0x28, 4)
	p.px = decodePx(v.vramWord(addr + 0x34))
	p.py = decodePx(v.vramWord(addr + 0x36))
	p.cx = decodePx(v.vramWord(addr + 0x3C))
	p.cy = decodePx(v.vramWord(addr + 0x3E))
	p.mx1024 = pair(0x44, 12)
	p.my1024 = pair(0x48, 12)
	kpair := func(off int) int32 {
		iw := v.vramWord(addr + off)
		fw := v.vramWord(addr + off + 2)
		return int32(signExtendWord(iw&0xFF, 8))<<16 + int32(fw)
	}
	p.kx65536 = kpair(0x4C)
	p.ky65536 = kpair(0x50)
	// KAst is an unsigned 16 bit integer plus a 10 bit fraction.
	p.kast1024 = int32(v.vramWord(addr+0x54))<<10 + int32(v.vramWord(addr+0x56)>>6)
	p.dkast1024 = fixed1024(v.vramWord(addr+0x58), v.vramWord(addr+0x5A), 9)
	p.dkax1024 = fixed1024(v.vramWord(addr+0x5C), v.vramWord(addr+0x5E), 9)
	return p
}

// effectiveRotParams builds the parameters for one set on one
// line. When any read enable for the set is on, fields come from
// the per line entry, except Xst, Yst, and KAst, which come from
// the per line entry only when their own read enable is on and
// otherwise advance from the base entry by their per line delta.
// When no read enable is on, the base entry is used with the same
// delta advance, which is the stateless form of the hardware
// start address counters.
func (v *VDP2) effectiveRotParams(set, line int) rotParams {
	leadA, _ := v.rotLead()
	baseAddr := leadA + set*0x80
	base := v.parseRotEntry(baseAddr)
	rprctl := v.reg16(0x0B2)
	var en uint16
	if set == 0 {
		en = rprctl & 0x7
	} else {
		en = (rprctl >> 8) & 0x7
	}
	if en == 0 {
		p := base
		p.xst1024 = base.xst1024 + base.dxst1024*int32(line)
		p.yst1024 = base.yst1024 + base.dyst1024*int32(line)
		p.kast1024 = base.kast1024 + base.dkast1024*int32(line)
		return p
	}
	p := v.parseRotEntry(leadA + line*0x100 + set*0x80)
	if en&0x1 == 0 {
		p.xst1024 = base.xst1024 + base.dxst1024*int32(line)
	}
	if en&0x2 == 0 {
		p.yst1024 = base.yst1024 + base.dyst1024*int32(line)
	}
	if en&0x4 == 0 {
		p.kast1024 = base.kast1024 + base.dkast1024*int32(line)
	}
	return p
}

// rotCfg builds the character configuration for one rotation set.
// Character control and pattern name control are shared by both
// sets (there is a single RBG block in CHCTLB and PNCR), while
// plane size, over mode, map offset, maps, and the over pattern
// follow the set. The over fields are returned alongside.
func (v *VDP2) rotCfg(set int) (charCfg, int, uint16) {
	cfg := charCfg{}
	chctlb := v.reg16(0x02A)
	cfg.colorMode = int((chctlb >> 12) & 0x7)
	if cfg.colorMode > colRGB888 {
		cfg.colorMode = colRGB888
	}
	cfg.bmsz = int((chctlb >> 10) & 0x1)
	cfg.bmen = chctlb&0x0200 != 0
	cfg.charSize = int((chctlb >> 8) & 0x1)
	bgon := v.reg16(regBGON)
	cfg.tpon = bgon&0x1000 != 0
	p := v.bmpnbFields()
	cfg.bmpPal, cfg.bmpr, cfg.bmcc = p.pal, p.pr, p.cc
	pnc := v.reg16(0x038)
	cfg.pnb = pnc&0x8000 != 0
	cfg.cnsm = pnc&0x4000 != 0
	cfg.spr = pnc&0x0200 != 0
	cfg.scc = pnc&0x0100 != 0
	cfg.splt = int((pnc >> 5) & 0x7)
	cfg.scn = int(pnc & 0x1F)
	plsz := v.reg16(0x03A)
	if set == 0 {
		cfg.planeSize = int((plsz >> 8) & 0x3)
	} else {
		cfg.planeSize = int((plsz >> 12) & 0x3)
	}
	ovr := 0
	if set == 0 {
		ovr = int((plsz >> 10) & 0x3)
	} else {
		ovr = int((plsz >> 14) & 0x3)
	}
	mpofr := v.reg16(0x03E)
	if set == 0 {
		cfg.mapOffset = int(mpofr & 0x7)
	} else {
		cfg.mapOffset = int((mpofr >> 8) & 0x7)
	}
	base := 0x050 + set*0x10
	for i := 0; i < 8; i++ {
		r := v.reg16(base + i*2)
		cfg.maps[i*2] = int(r & 0x3F)
		cfg.maps[i*2+1] = int((r >> 8) & 0x3F)
	}
	cfg.craof = int(v.reg16(0x0E6) & 0x7)
	ovpn := v.reg16(0x0B8 + set*2)
	return cfg, ovr, ovpn
}

func (v *VDP2) bmpnbFields() bmpFields {
	r := v.reg16(0x02E)
	return bmpFields{pal: int(r & 0x7), pr: r&0x20 != 0, cc: r&0x10 != 0}
}

// coeffFor reads the coefficient table entry for one dot and
// returns the coefficient in 1 over 65536 units plus the raw word
// (whose top bit drives RPMD mode 2). It reports false when the
// coefficient table is disabled for this set.
func (v *VDP2) coeffFor(set int, p rotParams, x int) (int32, uint32, bool) {
	ktctl := v.reg16(0x0B4)
	var field uint16
	if set == 0 {
		field = ktctl & 0x1F
	} else {
		field = (ktctl >> 8) & 0x1F
	}
	if field&0x1 == 0 {
		return 0, 0, false
	}
	kastNow := int64(p.kast1024) + int64(p.dkax1024)*int64(x)
	ki := floorDiv64(kastNow, 1024)
	ktaof := v.reg16(0x0B6)
	var ofs int
	if set == 0 {
		ofs = int(ktaof & 0x7)
	} else {
		ofs = int((ktaof >> 8) & 0x7)
	}
	byteAddr := (((ofs << 16) | int(ki&0xFFFF)) << 2) & 0x7FFFF
	var word uint32
	if field&0x2 != 0 {
		// KDBS: the coefficient table lives in CRAM instead.
		a := byteAddr & 0xFFF
		word = uint32(v.cramByte(a))<<24 | uint32(v.cramByte(a+1))<<16 | uint32(v.cramByte(a+2))<<8 | uint32(v.cramByte(a+3))
	} else {
		word = v.vramLong(byteAddr)
	}
	se := int32(uint32(word>>8)<<(32-24)) >> (32 - 24)
	return se, word, true
}

// applyCoeff replaces kx and ky from the coefficient by KMD mode.
// KLCE is ignored: line color insertion through the coefficient
// table is not modeled.
func (v *VDP2) applyCoeff(set int, p *rotParams, coeff int32) {
	ktctl := v.reg16(0x0B4)
	var kmd int
	if set == 0 {
		kmd = int((ktctl >> 2) & 0x3)
	} else {
		kmd = int((ktctl >> 10) & 0x3)
	}
	switch kmd {
	case 0:
		p.kx65536 = coeff
		p.ky65536 = coeff
	case 1:
		p.kx65536 = coeff
	case 2:
		p.ky65536 = coeff
	case 3:
		// Both scale factors come from the table parameters.
	}
}

// rotationSetFor picks the parameter set a rotation layer uses at
// one dot, following RPMD.
func (v *VDP2) rotationSetFor(layer, x, y, evalLine int) int {
	rpmd := int(v.reg16(regRPMD) & 0x3)
	isRBG0 := layer == layerRBG0
	switch rpmd {
	case 0:
		if isRBG0 {
			return 0
		}
		return 1
	case 1:
		if isRBG0 {
			return 1
		}
		return 0
	case 2:
		// Per line switching by the coefficient MSB read for the
		// line. The probe reads set A at dot 0 of the line, or
		// set B when only set B has its table enabled.
		msb := uint32(0)
		pa := v.effectiveRotParams(0, evalLine)
		if _, w, ok := v.coeffFor(0, pa, 0); ok {
			msb = w
		} else {
			pb := v.effectiveRotParams(1, evalLine)
			if _, w, ok := v.coeffFor(1, pb, 0); ok {
				msb = w
			}
		}
		setA := msb>>31 == 0
		if isRBG0 {
			if setA {
				return 0
			}
			return 1
		}
		if setA {
			return 1
		}
		return 0
	default: // mode 3
		inside := v.insideRPWindow(x, y)
		if isRBG0 {
			if inside {
				return 1
			}
			return 0
		}
		if inside {
			return 0
		}
		return 1
	}
}

// rotTransform runs the rotation matrix for one dot and returns
// source dot coords. Units throughout are 1 over 65536.
func rotTransform(p rotParams, x int) (int, int) {
	xst := int64(p.xst1024) << 6
	yst := int64(p.yst1024) << 6
	xv := xst + (int64(p.dx1024)<<6)*int64(x)
	yv := yst + (int64(p.dy1024)<<6)*int64(x)
	px := p.px << 16
	py := p.py << 16
	cx := p.cx << 16
	cy := p.cy << 16
	mx := int64(p.mx1024) << 6
	my := int64(p.my1024) << 6
	a := int64(p.a1024) << 6
	b := int64(p.b1024) << 6
	c := int64(p.c1024) << 6
	d := int64(p.d1024) << 6
	innerX := (a*(xv-px))>>16 + (b*(yv-py))>>16
	innerY := (c*(xv-px))>>16 + (d*(yv-py))>>16
	outX := cx + (int64(p.kx65536)*innerX)>>16 + mx
	outY := cy + (int64(p.ky65536)*innerY)>>16 + my
	return int(floorDiv64(outX, 65536)), int(floorDiv64(outY, 65536))
}

// fetchRotation fetches one rotation layer dot. evalX and evalY
// are screen coords; evalY is also the line used for parameter
// selection, which is how mosaic on rotation layers ends up using
// the sample line parameters.
func (v *VDP2) fetchRotation(set, evalX, evalY int) (pix, bool) {
	cfg, ovr, ovpn := v.rotCfg(set)
	p := v.effectiveRotParams(set, evalY)
	if coeff, _, ok := v.coeffFor(set, p, evalX); ok {
		v.applyCoeff(set, &p, coeff)
	}
	sx, sy := rotTransform(p, evalX)

	gridW, gridH := planeGrid(cfg.planeSize)
	fieldW := gridW * 512
	fieldH := gridH * 512
	outside := sx < 0 || sy < 0 || sx >= fieldW || sy >= fieldH
	if outside {
		switch ovr {
		case 0:
			sx = floorMod(sx, fieldW)
			sy = floorMod(sy, fieldH)
		case 1:
			return v.overPatternPixel(cfg, ovpn, sx, sy)
		default:
			// Modes 2 and 3 are both transparent outside here.
			// Mode 3 on hardware applies a display limit rather
			// than field limits; the field limit is used.
			return pix{}, false
		}
	}
	if cfg.bmen {
		return v.bitmapPixel(cfg, sx, sy)
	}
	return v.cellPixel(cfg, sx, sy)
}

// overPatternPixel samples the single over pattern entry for a
// dot outside the rotation field. Simplification: the OVPN
// register word is used as both words of the 2 word pattern
// entry, since the register page holds only one word per set for
// this value; its low bits then form the character number.
func (v *VDP2) overPatternPixel(cfg charCfg, ovpn uint16, sx, sy int) (pix, bool) {
	forced := cfg
	forced.pnb = false
	p := v.decodePattern(forced, ovpn, ovpn)
	dx := floorMod(sx, 8)
	dy := floorMod(sy, 8)
	if p.flipH {
		dx = 7 - dx
	}
	if p.flipV {
		dy = 7 - dy
	}
	code, direct, raw := v.charDot(p.charBase, dx, dy, forced)
	return v.assemble(forced, code, direct, raw, p.palette, p)
}

// FBRotationCoord returns the VDP1 frame buffer rotation read
// coords for a display dot, from the rotation parameter A
// viewpoint only. No rotation matrix is applied, per the frame
// buffer rotation read rule.
func (v *VDP2) FBRotationCoord(x, y, line int) (int, int, bool) {
	p := v.effectiveRotParams(0, line)
	xv := (int64(p.xst1024) << 6) + (int64(p.dx1024)<<6)*int64(x)
	yv := (int64(p.yst1024) << 6) + (int64(p.dy1024)<<6)*int64(x)
	return int(floorDiv64(xv, 65536)), int(floorDiv64(yv, 65536)), true
}
