// Package vdp2 implements the Sega Saturn VDP2 background and
// composition hardware as a pure function of VRAM, CRAM, the
// register page, and a sprite word source supplied by VDP1.
//
// Rendering holds no state between lines. Every line is recomputed
// from the three slices and the sprite source, and all rotation
// math is rebuilt from the parameter table for the requested line.
package vdp2

import "sort"

// SpriteSource supplies raw VDP1 frame buffer words to VDP2.
type SpriteSource interface {
	// SpriteWordAt returns the raw VDP1 display frame buffer word
	// at display coords (x, y) on scanline line, plus whether a dot
	// position exists there at all (false outside the frame buffer).
	SpriteWordAt(x, y, line int) (uint16, bool)
	// EightBit reports the VDP1 8 bit per pixel frame buffer mode.
	EightBit() bool
}

// VDP2 is the background processor. Its fields are the exact slices
// handed to New, so callers observe register writes made here and
// this type observes memory writes made by callers.
type VDP2 struct {
	vram []byte
	cram []byte
	regs []byte
	spr  SpriteSource
}

// New builds a VDP2 over the given slices. Expected lengths are
// 512K of VRAM, 4K of CRAM, and 512 bytes of registers.
func New(vram, cram, regs []byte, spr SpriteSource) *VDP2 {
	return &VDP2{vram: vram, cram: cram, regs: regs, spr: spr}
}

// Register offsets in the 512 byte register page.
const (
	regTVMD   = 0x000
	regTVSTAT = 0x004
	regRAMCTL = 0x00E
	regBGON   = 0x020
	regMZCTL  = 0x022
	regSFSEL  = 0x024
	regSFCODE = 0x026
	regRPMD   = 0x0B0
	regCCCTL  = 0x0EC
	regSFPRMD = 0x0EA
	regSFCCMD = 0x0EE
	regLNCLEN = 0x0E8
	regSDCTL  = 0x0E2
	regCLOFEN = 0x110
	regCLOFSL = 0x112
	regCCRLB  = 0x10E
)

// Layer identifiers in fixed tie break order for equal priority.
// Back screen and line color sit outside the sorted layer order.
const (
	layerSprite = iota
	layerRBG0
	layerRBG1
	layerNBG0
	layerNBG1
	layerNBG2
	layerNBG3
	layerBack
	layerLine
)

type rgb struct {
	r, g, b int
}

func (c rgb) pack() uint32 {
	return 0xFF000000 | uint32(c.r&0xFF)<<16 | uint32(c.g&0xFF)<<8 | uint32(c.b&0xFF)
}

func expand555(w uint16) rgb {
	exp := func(v uint16) int {
		return int(v)<<3 | int(v)>>2
	}
	return rgb{exp((w >> 10) & 0x1F), exp((w >> 5) & 0x1F), exp(w & 0x1F)}
}

func (v *VDP2) reg16(off int) uint16 {
	if off < 0 || off+1 >= len(v.regs) {
		return 0
	}
	return uint16(v.regs[off])<<8 | uint16(v.regs[off+1])
}

func (v *VDP2) setReg16(off int, val uint16) {
	if off < 0 || off+1 >= len(v.regs) {
		return
	}
	v.regs[off] = byte(val >> 8)
	v.regs[off+1] = byte(val)
}

func sliceByte(s []byte, addr int) byte {
	if addr < 0 || addr >= len(s) {
		return 0
	}
	return s[addr]
}

// vramByte reads VRAM with the address wrapped into the 512K space.
func (v *VDP2) vramByte(addr int) byte {
	return sliceByte(v.vram, addr&0x7FFFF)
}

func (v *VDP2) vramWord(addr int) uint16 {
	return uint16(v.vramByte(addr))<<8 | uint16(v.vramByte(addr+1))
}

func (v *VDP2) vramLong(addr int) uint32 {
	return uint32(v.vramWord(addr))<<16 | uint32(v.vramWord(addr+2))
}

func (v *VDP2) cramByte(addr int) byte {
	return sliceByte(v.cram, addr&0xFFF)
}

// DisplaySize decodes TVMD into the visible resolution.
func (v *VDP2) DisplaySize() (int, int) {
	tvmd := v.reg16(regTVMD)
	hreso := int(tvmd & 0x7)
	widths := [8]int{320, 352, 640, 704, 320, 352, 640, 704}
	w := widths[hreso]
	if hreso >= 4 {
		// Exclusive modes are already 480 line modes, so no
		// interlace doubling is applied on top of them.
		return w, 480
	}
	heights := [4]int{224, 240, 256, 256}
	h := heights[(tvmd>>4)&0x3]
	if (tvmd>>6)&0x3 == 3 {
		h = h * 2
	}
	return w, h
}

// TotalLines reports total lines per frame including blanking.
func (v *VDP2) TotalLines() int {
	tvmd := v.reg16(regTVMD)
	hreso := int(tvmd & 0x7)
	if hreso >= 4 {
		w := [8]int{320, 352, 640, 704, 320, 352, 640, 704}[hreso]
		if w == 352 || w == 704 {
			return 561
		}
		return 525
	}
	return 263
}

// SetTiming writes VCNT and the TVSTAT VBLANK and ODD bits for the
// line that is about to be stepped. The system calls it at each
// line start before stepping the CPUs.
func (v *VDP2) SetTiming(line int, oddField bool) {
	v.setReg16(0x00A, uint16(line&0x3FF))
	_, h := v.DisplaySize()
	st := v.reg16(regTVSTAT)
	st &^= (1 << 3) | (1 << 1)
	if line >= h || v.reg16(regTVMD)&0x8000 == 0 {
		st |= 1 << 3
	}
	if oddField {
		st |= 1 << 1
	}
	v.setReg16(regTVSTAT, st)
}

// SetHBlank writes the TVSTAT HBLANK bit.
func (v *VDP2) SetHBlank(in bool) {
	st := v.reg16(regTVSTAT)
	st &^= 1 << 2
	if in {
		st |= 1 << 2
	}
	v.setReg16(regTVSTAT, st)
}

// cramMode returns RAMCTL CRMD.
func (v *VDP2) cramMode() int {
	return int((v.reg16(regRAMCTL) >> 12) & 0x3)
}

// cramReadIndex resolves a CRAM index to a color by CRAM mode.
// Modes 0 and 1 read RGB555 words. Modes 2 and 3 read RGB888 longs
// whose bytes are split across the two halves of CRAM: bytes 0 and
// 1 sit at the low half offset and bytes 2 and 3 at the plus 0x800
// offset, so red is byte 1, green byte 2, and blue byte 3.
func (v *VDP2) cramReadIndex(idx int) rgb {
	switch v.cramMode() {
	case 0:
		return expand555(uint16(v.cramByte((idx&0x3FF)*2))<<8 | uint16(v.cramByte((idx&0x3FF)*2+1)))
	case 1:
		return expand555(uint16(v.cramByte((idx&0x7FF)*2))<<8 | uint16(v.cramByte((idx&0x7FF)*2+1)))
	default:
		li := idx & 0x3FF
		r := int(v.cramByte(li*2 + 1))
		g := int(v.cramByte(0x800 + li*2))
		b := int(v.cramByte(0x800 + li*2 + 1))
		return rgb{r, g, b}
	}
}

// cramColorForDot forms a CRAM index from a palette dot color with
// the layer color offset added into bits 10 to 8, then reads it.
func (v *VDP2) cramColorForDot(dotColor, offset int) rgb {
	idx := (dotColor & 0xFF) | ((((dotColor >> 8) + offset) & 0x7) << 8)
	return v.cramReadIndex(idx)
}

// tableEntryColor reads one back screen or line color table entry.
// Long entries are plain sequential bytes in VRAM, unlike the
// split CRAM long layout. The insert flag is bit 15 of a word
// entry or bit 31 of a long entry.
func (v *VDP2) tableEntryColor(addr int) (rgb, bool) {
	if v.cramMode() >= 2 {
		b0 := v.vramByte(addr)
		r := int(v.vramByte(addr + 1))
		g := int(v.vramByte(addr + 2))
		b := int(v.vramByte(addr + 3))
		return rgb{r, g, b}, b0&0x80 != 0
	}
	w := v.vramWord(addr)
	return expand555(w & 0x7FFF), w&0x8000 != 0
}

func (v *VDP2) tableEntrySize() int {
	if v.cramMode() >= 2 {
		return 4
	}
	return 2
}

// backColorForLine reads the back screen color for a line.
func (v *VDP2) backColorForLine(line int) rgb {
	up := v.reg16(0x0AC)
	addr := (int(up&0x7) << 16) | int(v.reg16(0x0AE))
	if up&0x8000 != 0 {
		addr += line * v.tableEntrySize()
	}
	c, _ := v.tableEntryColor(addr)
	return c
}

// lineColorForLine reads the line color entry and its insert flag.
func (v *VDP2) lineColorForLine(line int) (rgb, bool) {
	up := v.reg16(0x0A8)
	addr := (int(up&0x7) << 16) | int(v.reg16(0x0AA))
	if up&0x8000 != 0 {
		addr += line * v.tableEntrySize()
	}
	return v.tableEntryColor(addr)
}

type candidate struct {
	col       rgb
	prio      int
	ccRatio   int
	ccEnabled bool
	layer     int
}

func blend(a, b rgb, ratio int) rgb {
	mix := func(x, y int) int {
		return (x*ratio + y*(32-ratio)) / 32
	}
	return rgb{mix(a.r, b.r), mix(a.g, b.g), mix(a.b, b.b)}
}

func clamp255(x int) int {
	if x < 0 {
		return 0
	}
	if x > 255 {
		return 255
	}
	return x
}

// clofBit maps a layer to its bit in CLOFEN and CLOFSL, whose
// layout runs sprite, back, R0, N3, N2, N1, N0.
func clofBit(layer int) int {
	switch layer {
	case layerSprite:
		return 6
	case layerBack:
		return 5
	case layerRBG0, layerRBG1:
		return 4
	case layerNBG3:
		return 3
	case layerNBG2:
		return 2
	case layerNBG1:
		return 1
	case layerNBG0:
		return 0
	}
	return -1
}

func sdctlBit(layer int) int {
	switch layer {
	case layerBack:
		return 5
	case layerRBG0, layerRBG1:
		return 4
	case layerNBG3:
		return 3
	case layerNBG2:
		return 2
	case layerNBG1:
		return 1
	case layerNBG0:
		return 0
	}
	return -1
}

func lnclBit(layer int) int {
	switch layer {
	case layerSprite:
		return 5
	case layerRBG0, layerRBG1:
		return 4
	case layerNBG3:
		return 3
	case layerNBG2:
		return 2
	case layerNBG1:
		return 1
	case layerNBG0:
		return 0
	}
	return -1
}

// anyDisplayedLNCL reports whether any BGON enabled layer has its
// line color enable bit set. Used when no layer produced a dot.
func (v *VDP2) anyDisplayedLNCL() bool {
	bgon := v.reg16(regBGON)
	lncl := v.reg16(regLNCLEN)
	// BGON low bits run N0, N1, N2, N3, R0, R1 while LNCLEN bits
	// run N0, N1, N2, N3, R0, sprite, so map each enabled layer.
	pairs := [][2]int{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}, {5, 4}}
	for _, p := range pairs {
		if bgon&(1<<uint(p[0])) != 0 && lncl&(1<<uint(p[1])) != 0 {
			return true
		}
	}
	return false
}

func (v *VDP2) colorOffset(layer int, col rgb) rgb {
	bit := clofBit(layer)
	if bit < 0 {
		return col
	}
	if v.reg16(regCLOFEN)&(1<<uint(bit)) == 0 {
		return col
	}
	base := 0x114
	if v.reg16(regCLOFSL)&(1<<uint(bit)) != 0 {
		base = 0x11A
	}
	off := func(off int) int {
		n := int(v.reg16(off) & 0x1FF)
		if n >= 256 {
			n -= 512
		}
		return n
	}
	return rgb{
		clamp255(col.r + off(base)),
		clamp255(col.g + off(base+2)),
		clamp255(col.b + off(base+4)),
	}
}

// RenderLine renders one visible line into fb, which is w by h
// XRGB8888 pixels. Only lines 0 to h minus 1 are expected.
func (v *VDP2) RenderLine(fb []uint32, y int) {
	w, h := v.DisplaySize()
	if y < 0 || y >= h || w <= 0 {
		return
	}
	base := y * w
	if base+w > len(fb) {
		return
	}
	tvmd := v.reg16(regTVMD)
	back := v.backColorForLine(y)
	if tvmd&0x8000 == 0 {
		fill := uint32(0xFF000000)
		if tvmd&0x0100 != 0 {
			fill = back.pack()
		}
		for x := 0; x < w; x++ {
			fb[base+x] = fill
		}
		return
	}
	lineCol, lineInsert := v.lineColorForLine(y)
	mzctl := v.reg16(regMZCTL)
	mzW := int((mzctl>>8)&0xF) + 1
	mzH := int((mzctl>>12)&0xF) + 1

	for x := 0; x < w; x++ {
		spr := v.decodeSprite(x, y)
		cands := make([]candidate, 0, 8)

		if spr.present && spr.prio != 0 {
			if v.windowPass(v.spriteWindowByte(), x, y, spr.signal) {
				cands = append(cands, candidate{spr.col, spr.prio, spr.ccRatio, spr.ccEnabled, layerSprite})
			}
		}

		// Rotation layers.
		bgon := v.reg16(regBGON)
		if bgon&0x30 != 0 {
			for i := 0; i < 2; i++ {
				layer := layerRBG0 + i
				if bgon&(1<<uint(4+i)) == 0 {
					continue
				}
				ex, ey := x, y
				if mzctl&0x10 != 0 {
					ex = x - x%mzW
					ey = y - y%mzH
				}
				set := v.rotationSetFor(layer, x, y, ey)
				if pix, ok := v.fetchRotation(set, ex, ey); ok {
					v.addScrollCandidate(&cands, pix, layer, 4, x, y, spr.signal)
				}
			}
		}
		// Normal backgrounds.
		for i := 0; i < 4; i++ {
			layer := layerNBG0 + i
			if bgon&(1<<uint(i)) == 0 {
				continue
			}
			ex, ey := x, y
			mzBit := uint(i)
			if i == 0 {
				mzBit = 0
			}
			// MZCTL enable bits run N0 bit 0, N1 bit 1, N2 bit 2,
			// N3 bit 3, so the loop index is the bit directly.
			if mzctl&(1<<mzBit) != 0 {
				ex = x - x%mzW
				ey = y - y%mzH
			}
			if pix, ok := v.fetchNBG(i, ex, ey); ok {
				v.addScrollCandidate(&cands, pix, layer, i, x, y, spr.signal)
			}
		}

		sort.SliceStable(cands, func(a, b int) bool {
			if cands[a].prio != cands[b].prio {
				return cands[a].prio > cands[b].prio
			}
			return cands[a].layer < cands[b].layer
		})

		backCand := candidate{back, 0, int((v.reg16(regCCRLB) >> 8) & 0x1F), v.reg16(regCCCTL)&0x8000 != 0, layerBack}
		lineCand := candidate{lineCol, 0, int(v.reg16(regCCRLB) & 0x1F), v.reg16(regCCCTL)&0x20 != 0, layerLine}

		list := make([]candidate, 0, 10)
		inserted := false
		if lineInsert && len(cands) > 0 {
			topBit := lnclBit(cands[0].layer)
			if topBit >= 0 && v.reg16(regLNCLEN)&(1<<uint(topBit)) != 0 {
				list = append(list, cands[0], lineCand)
				list = append(list, cands[1:]...)
				inserted = true
			}
		}
		if !inserted {
			if lineInsert && len(cands) == 0 && v.anyDisplayedLNCL() {
				list = append(list, lineCand, backCand)
				inserted = true
			} else {
				list = append(list, cands...)
				list = append(list, backCand)
			}
		}

		out := v.compose(list, inserted, x, y, spr)
		fb[base+x] = out.pack()
	}
}

// compose applies color calculation, color offset, and shadow to
// the ordered candidate list for one dot. The list always ends
// with the back screen candidate unless line color was placed on
// top of it.
func (v *VDP2) compose(list []candidate, inserted bool, x, y int, spr spriteInfo) rgb {
	top := list[0]
	out := top.col
	ccctl := v.reg16(regCCCTL)
	if len(list) >= 2 && top.ccEnabled && v.windowPass(v.reg16(0x0D6)>>8, x, y, spr.signal) {
		second := list[1].col
		if ccctl&0x0400 != 0 && ccctl&0x0100 == 0 {
			// Extended color calculation, ratio mode only. The
			// effective second image is a half blend down the
			// candidate chain before the main blend. The
			// hardware restriction of extended calculation to
			// normal TV modes is not applied here.
			if inserted {
				third := second
				haveThird := false
				if len(list) >= 4 {
					third = blend(list[2].col, list[3].col, 16)
					haveThird = true
				} else if len(list) >= 3 {
					third = list[2].col
					haveThird = true
				}
				if haveThird {
					second = blend(list[1].col, third, 16)
				}
			} else if len(list) >= 3 {
				second = blend(list[1].col, list[2].col, 16)
			}
		}
		if ccctl&0x0100 != 0 {
			// Addition mode.
			out = rgb{
				clamp255(top.col.r + second.r),
				clamp255(top.col.g + second.g),
				clamp255(top.col.b + second.b),
			}
		} else {
			ratio := top.ccRatio
			if ccctl&0x0200 != 0 {
				ratio = list[1].ccRatio
			}
			out = blend(top.col, second, ratio)
		}
	}
	out = v.colorOffset(top.layer, out)
	if spr.shadow {
		bit := sdctlBit(top.layer)
		if bit >= 0 && v.reg16(regSDCTL)&(1<<uint(bit)) != 0 {
			// Shadow halves the final color. TPSDSL is ignored:
			// transparent shadow handling is not modeled.
			out = rgb{out.r >> 1, out.g >> 1, out.b >> 1}
		}
	}
	return out
}

// addScrollCandidate converts a fetched scroll layer pixel into a
// candidate, applying special priority and special color
// calculation modes and the per layer window test.
func (v *VDP2) addScrollCandidate(cands *[]candidate, pix pix, layer, scrollIdx, x, y int, sprSignal bool) {
	regPrio := v.scrollPriority(scrollIdx)
	ratio := v.scrollCCRatio(scrollIdx)

	sfCodeMatch := false
	{
		sel := v.reg16(regSFSEL)
		code := uint16(v.reg16(regSFCODE) & 0xFF)
		if sel&(1<<uint(scrollIdx)) != 0 {
			code = v.reg16(regSFCODE) >> 8
		}
		sfCodeMatch = (pix.dotColor >> 3) == int(code)
	}

	prio := regPrio
	switch (v.reg16(regSFPRMD) >> uint(scrollIdx*2)) & 0x3 {
	case 1:
		sp := 0
		if pix.spPri {
			sp = 1
		}
		prio = (regPrio & 6) | sp
	case 2:
		sp := 0
		if sfCodeMatch {
			sp = 1
		}
		prio = (regPrio & 6) | sp
	case 3:
		// Mode 3 is forbidden on hardware; it is treated as
		// mode 0 here.
		prio = regPrio
	}
	if prio == 0 {
		return
	}

	ccEnabled := false
	switch (v.reg16(regSFCCMD) >> uint(scrollIdx*2)) & 0x3 {
	case 0:
		ccEnabled = v.reg16(regCCCTL)&(1<<uint(ccctlScrollBit(scrollIdx))) != 0
	case 1:
		ccEnabled = pix.spCC
	case 2:
		ccEnabled = sfCodeMatch
	case 3:
		// Undocumented for color calculation; treated as mode 0.
		ccEnabled = v.reg16(regCCCTL)&(1<<uint(ccctlScrollBit(scrollIdx))) != 0
	}

	if !v.windowPass(v.layerWindowByte(layer), x, y, sprSignal) {
		return
	}
	*cands = append(*cands, candidate{pix.col, prio, ratio, ccEnabled, layer})
}

func ccctlScrollBit(scrollIdx int) int {
	// CCCTL low byte bits run N0, N1, N2, N3, R0 at bits 0 to 4,
	// which matches the scroll index order used here.
	return scrollIdx
}

func (v *VDP2) scrollPriority(scrollIdx int) int {
	switch scrollIdx {
	case 0:
		return int(v.reg16(0x0F8) & 0x7)
	case 1:
		return int((v.reg16(0x0F8) >> 8) & 0x7)
	case 2:
		return int(v.reg16(0x0FA) & 0x7)
	case 3:
		return int((v.reg16(0x0FA) >> 8) & 0x7)
	default:
		return int(v.reg16(0x0FC) & 0x7)
	}
}

func (v *VDP2) scrollCCRatio(scrollIdx int) int {
	switch scrollIdx {
	case 0:
		return int(v.reg16(0x108) & 0x1F)
	case 1:
		return int((v.reg16(0x108) >> 8) & 0x1F)
	case 2:
		return int(v.reg16(0x10A) & 0x1F)
	case 3:
		return int((v.reg16(0x10A) >> 8) & 0x1F)
	default:
		return int(v.reg16(0x10C) & 0x1F)
	}
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q -= 1
	}
	return q
}

func floorMod(a, b int) int {
	return a - floorDiv(a, b)*b
}

func floorDiv64(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q -= 1
	}
	return q
}

func signExtendWord(w uint16, bits int) int {
	if bits <= 0 {
		return 0
	}
	if bits >= 32 {
		return int(int32(w))
	}
	return int(int32(uint32(w)<<(32-uint(bits))) >> (32 - uint(bits)))
}

// fixed1024 decodes a fixed point pair with 10 fraction bits. The
// integer word contributes intBits of signed integer, masked from
// its low bits, and the fraction word contributes its top 10 bits.
func fixed1024(intWord, fracWord uint16, intBits int) int32 {
	return int32(signExtendWord(intWord, intBits))<<10 + int32(fracWord>>6)
}
