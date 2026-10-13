package vdp2

// Sprite type fields from SPCTL SPTYPE. PR and CC positions are
// the least significant bit of a field that grows upward, so the
// priority field always occupies the top bits of the word and the
// color field always starts at bit 0. shadowPos is negative when
// the type has no shadow bit.
type spriteType struct {
	prPos, prCount int
	ccPos, ccCount int
	colorBits      int
	shadowPos      int
}

var spriteTypes = [16]spriteType{
	{15, 1, 12, 3, 11, -1}, // type 0
	{15, 1, 12, 3, 11, 11}, // type 1
	{14, 2, 12, 2, 11, 11}, // type 2
	{14, 2, 11, 3, 10, -1}, // type 3
	{13, 3, 12, 1, 11, 11}, // type 4
	{15, 1, 13, 2, 11, 11}, // type 5
	{14, 2, 13, 1, 11, 11}, // type 6
	{13, 3, 10, 3, 9, 9},   // type 7
	{15, 1, 0, 0, 11, -1},  // type 8
	{14, 2, 0, 0, 11, -1},  // type 9
	{15, 1, 13, 2, 11, -1}, // type A
	{13, 3, 0, 0, 11, -1},  // type B
	{15, 1, 12, 3, 11, -1}, // type C
	{14, 2, 12, 2, 11, -1}, // type D
	{13, 3, 12, 1, 11, -1}, // type E
	{15, 1, 13, 2, 11, -1}, // type F
}

// spriteInfo is the decoded sprite dot at one screen position.
// present reports a color candidate; shadow reports a shadow dot,
// which is never a color candidate; signal is the sprite window
// signal used by other layers at this dot.
type spriteInfo struct {
	present   bool
	shadow    bool
	signal    bool
	col       rgb
	prio      int
	ccRatio   int
	ccEnabled bool
}

func spriteRegValue(regs [4]uint16, idx int) int {
	r := regs[idx/2]
	if idx%2 == 0 {
		return int(r & 0x7)
	}
	return int((r >> 8) & 0x7)
}

func spriteRatioValue(regs [4]uint16, idx int) int {
	r := regs[idx/2]
	if idx%2 == 0 {
		return int(r & 0x1F)
	}
	return int((r >> 8) & 0x1F)
}

func (v *VDP2) decodeSprite(x, y int) spriteInfo {
	var out spriteInfo
	if v.spr == nil {
		return out
	}
	word, ok := v.spr.SpriteWordAt(x, y, y)
	if !ok {
		return out
	}
	spctl := v.reg16(0x0E0)
	sptype := int(spctl & 0xF)
	spclmd := spctl&0x20 != 0
	spccn := int((spctl >> 8) & 0x7)

	// The sprite window signal is independent of whether the dot
	// becomes a color candidate.
	if spctl&0x10 != 0 && !spclmd && sptype >= 2 && sptype <= 7 && word&0x8000 != 0 {
		out.signal = true
	}

	prioRegs := [4]uint16{v.reg16(0x0F0), v.reg16(0x0F2), v.reg16(0x0F4), v.reg16(0x0F6)}
	ratioRegs := [4]uint16{v.reg16(0x100), v.reg16(0x102), v.reg16(0x104), v.reg16(0x106)}
	sprOffset := int((v.reg16(0x0E6) >> 4) & 0x7)
	ccctl := v.reg16(regCCCTL)

	finish := func(prioIdx, ccIdx int, col rgb) spriteInfo {
		out.present = true
		out.col = col
		out.prio = spriteRegValue(prioRegs, prioIdx)
		out.ccRatio = spriteRatioValue(ratioRegs, ccIdx)
		ccen := ccctl&0x40 != 0
		if ccen {
			// SPCCCS gates sprite color calculation by comparing
			// the sprite priority number with the CC index. This
			// is the documented simplification of the condition.
			switch (spctl >> 12) & 0x3 {
			case 1:
				ccen = out.prio <= ccIdx
			case 2:
				ccen = out.prio == ccIdx
			case 3:
				ccen = out.prio >= ccIdx
			}
		}
		out.ccEnabled = ccen
		return out
	}

	if v.spr.EightBit() {
		code := int(word & 0xFF)
		if code == 0 {
			return out
		}
		// In 8 bit mode an RGB sprite word cannot exist, so the
		// dot is treated as a palette dot even when SPCLMD is 1.
		return finish(0, spccn, v.cramColorForDot(code, sprOffset))
	}

	t := spriteTypes[sptype]
	field := func(pos, count int) int {
		if count == 0 {
			return 0
		}
		return int((word >> uint(pos)) & uint16((1<<uint(count))-1))
	}
	prBits := field(t.prPos, t.prCount)
	prioIdx := prBits << uint(3-t.prCount)
	ccIdx := spccn
	if t.ccCount > 0 {
		ccIdx = field(t.ccPos, t.ccCount) << uint(3-t.ccCount)
	}
	colorField := int(word & uint16((1<<uint(t.colorBits))-1))
	colorMask := (1 << uint(t.colorBits)) - 1
	if t.shadowPos >= 0 && word&(1<<uint(t.shadowPos)) != 0 && colorField == colorMask^1 {
		out.shadow = true
		return out
	}
	if spclmd {
		if word&0x7FFF == 0 {
			return out
		}
		return finish(prioIdx, ccIdx, expand555(word&0x7FFF))
	}
	if colorField == 0 {
		return out
	}
	return finish(prioIdx, ccIdx, v.cramColorForDot(colorField, sprOffset))
}
