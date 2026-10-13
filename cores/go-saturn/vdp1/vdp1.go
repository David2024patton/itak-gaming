// Package vdp1 implements the Sega Saturn VDP1 sprite and polygon
// processor. It owns no memory of its own: the caller supplies the
// VRAM slice, the frame buffer slice and the register page, all of
// which the system wires to the bus.
//
// Frame buffer layout: 512 KiB total, two banks of 256 KiB. Bank 0
// starts at offset 0 and bank 1 at offset 262144. The draw bank is
// the bank being rendered into; the display bank is the other one.
//
// Simplifications relative to real hardware are documented at the
// site where each one lives.
package vdp1

// Register page offsets, in bytes. All registers are 16 bit big
// endian words inside the 128 byte page.
const (
	regTVMR = 0x00
	regFBCR = 0x02
	regPTMR = 0x04
	regEWDR = 0x06
	regEWLR = 0x08
	regEWRR = 0x0A
	regEDSR = 0x10
	regLOPR = 0x12
	regCOPR = 0x14
	regMODR = 0x16
)

const (
	vramSize    = 512 * 1024
	vramMask    = vramSize - 1
	fbBankSize  = 256 * 1024
	maxCommands = 200000
)

// rect is an inclusive rectangle in frame buffer coordinates.
type rect struct {
	x1, y1, x2, y2 int
}

func normRect(x1, y1, x2, y2 int) rect {
	if x2 < x1 {
		x1, x2 = x2, x1
	}
	if y2 < y1 {
		y1, y2 = y2, y1
	}
	return rect{x1: x1, y1: y1, x2: x2, y2: y2}
}

func (r rect) contains(x, y int) bool {
	return x >= r.x1 && x <= r.x2 && y >= r.y1 && y <= r.y2
}

// VDP1 is the sprite processor state. Memory slices are shared with
// the caller; register values are read fresh from the page on every
// use and status values are written back into the page.
type VDP1 struct {
	vram []byte
	fb   []byte
	regs []byte

	width  int
	height int

	drawBank int
	drawEnd  bool
	prevPTM  uint16

	rotate func(x, y, line int) (int, int, bool)

	// Draw state, reset at the start of every Draw call.
	sysClip   rect
	userClip  rect
	userValid bool
	localX    int
	localY    int
}

// New creates a VDP1 bound to the supplied memory. The vram slice is
// expected to be 512 KiB, the frame buffer 512 KiB and the register
// page 128 bytes.
func New(vram, fb, regs []byte) *VDP1 {
	return &VDP1{vram: vram, fb: fb, regs: regs}
}

// SetDisplaySize records the frame buffer line stride in pixels and
// the clip bounds, taken from the VDP2 TVMD register by the system.
func (v *VDP1) SetDisplaySize(w, h int) {
	v.width = w
	v.height = h
}

func (v *VDP1) reg(off int) uint16 {
	if off < 0 || off+1 >= len(v.regs) {
		return 0
	}
	return uint16(v.regs[off])<<8 | uint16(v.regs[off+1])
}

func (v *VDP1) setReg(off int, val uint16) {
	if off < 0 || off+1 >= len(v.regs) {
		return
	}
	v.regs[off] = byte(val >> 8)
	v.regs[off+1] = byte(val)
}

// composeMODR builds the MODR readback value. This is a documented
// approximation of the hardware mode register readback.
func (v *VDP1) composeMODR() uint16 {
	return (v.reg(regTVMR) & 0xF) |
		(v.reg(regFBCR)&0x1F)<<4 |
		(v.reg(regPTMR)&0x3)<<9
}

// EightBit reports the TVMR depth bit: true means 8 bit per pixel
// frame buffer mode, false means 16 bit per pixel.
func (v *VDP1) EightBit() bool {
	return v.reg(regTVMR)&0x1 != 0
}

// DrawBank returns the index of the bank currently drawn into.
func (v *VDP1) DrawBank() int {
	return v.drawBank
}

// SetDrawBank sets the draw bank index. The display bank is always
// the other bank.
func (v *VDP1) SetDrawBank(b int) {
	v.drawBank = b & 1
}

// TakeDrawEnd reports the draw end flag once per completed Draw,
// clearing it as it is consumed.
func (v *VDP1) TakeDrawEnd() bool {
	if !v.drawEnd {
		return false
	}
	v.drawEnd = false
	return true
}

// Misc packs the extra state that save states need and the register
// page does not hold: bit0 is the draw bank, bit1 is the draw end
// flag pending, bits3-2 are the previous PTM value seen by
// PollTrigger.
func (v *VDP1) Misc() uint16 {
	m := uint16(v.drawBank & 1)
	if v.drawEnd {
		m |= 0x2
	}
	m |= (v.prevPTM & 0x3) << 2
	return m
}

// SetMisc restores the state packed by Misc.
func (v *VDP1) SetMisc(m uint16) {
	v.drawBank = int(m & 1)
	v.drawEnd = m&0x2 != 0
	v.prevPTM = (m >> 2) & 0x3
}

// SetRotateFunc installs the rotation coordinate transform used by
// SpriteWordAt while TVMR rotation is enabled. The system supplies
// it from the VDP2 rotation parameter tables.
func (v *VDP1) SetRotateFunc(f func(x, y, line int) (int, int, bool)) {
	v.rotate = f
}

// SpriteWordAt samples the display bank for the VDP2 sprite layer.
// In bounds positions always report present, even when the sampled
// word is zero; the VDP2 side applies the sprite priority 0
// transparency rule. Out of bounds positions report not present.
func (v *VDP1) SpriteWordAt(x, y, line int) (uint16, bool) {
	sx, sy := x, y
	// TVMR bit1 is the rotation enable.
	if v.reg(regTVMR)&0x2 != 0 && v.rotate != nil {
		rx, ry, ok := v.rotate(x, y, line)
		if !ok {
			return 0, false
		}
		sx, sy = rx, ry
	}
	if sx < 0 || sx >= v.width || sy < 0 || sy >= v.height {
		return 0, false
	}
	base := (v.drawBank ^ 1) * fbBankSize
	if v.EightBit() {
		off := base + sy*v.width + sx
		if off < 0 || off >= len(v.fb) {
			return 0, false
		}
		return uint16(v.fb[off]), true
	}
	off := base + (sy*v.width+sx)*2
	if off < 0 || off+1 >= len(v.fb) {
		return 0, false
	}
	return uint16(v.fb[off])<<8 | uint16(v.fb[off+1]), true
}

// FrameBoundary runs frame change processing: it decodes the frame
// change mode from (VBE, FCM, FCT), then swaps banks, erases and
// auto draws as the mode requires. Combinations not listed in the
// mode table do nothing.
func (v *VDP1) FrameBoundary() {
	tvmr := v.reg(regTVMR)
	fbcr := v.reg(regFBCR)
	ptmr := v.reg(regPTMR)
	vbe := (tvmr >> 3) & 1
	fcm := (fbcr >> 1) & 1
	fct := fbcr & 1
	ptm := ptmr & 0x3
	autoDraw := ptm == 0 || ptm == 2

	switch {
	case vbe == 0 && fcm == 0 && fct == 0:
		// 1 cycle mode.
		v.drawBank ^= 1
		v.eraseBank(v.drawBank)
		if autoDraw {
			v.Draw()
		}
	case vbe == 0 && fcm == 1 && fct == 0:
		// Manual erase: the display bank is erased, no swap.
		v.eraseBank(v.drawBank ^ 1)
	case vbe == 0 && fcm == 1 && fct == 1:
		// Manual change: swap only, then the CPU owned FCT bit
		// is cleared in the page.
		v.drawBank ^= 1
		v.setReg(regFBCR, v.reg(regFBCR)&^0x0001)
	case vbe == 1 && fcm == 1 && fct == 1:
		// Erase and change. VBE stays as written; FCT is cleared.
		v.drawBank ^= 1
		v.eraseBank(v.drawBank)
		if autoDraw {
			v.Draw()
		}
		v.setReg(regFBCR, v.reg(regFBCR)&^0x0001)
	default:
		// All other combinations do nothing.
	}
}

// eraseBank fills the erase rectangle of one frame buffer bank with
// the EWDR fill word. The rectangle comes from EWLR and EWRR: the X
// fields are in 8 dot units, the Y fields are in lines. The
// rectangle is clipped to the display size.
func (v *VDP1) eraseBank(bank int) {
	ewdr := v.reg(regEWDR)
	ewlr := v.reg(regEWLR)
	ewrr := v.reg(regEWRR)
	x1 := int((ewlr>>9)&0x7F) * 8
	y1 := int(ewlr & 0x1FF)
	x2 := int((ewrr>>9)&0x7F)*8 + 7
	y2 := int(ewrr & 0x1FF)
	if x1 < 0 {
		x1 = 0
	}
	if y1 < 0 {
		y1 = 0
	}
	if x2 >= v.width {
		x2 = v.width - 1
	}
	if y2 >= v.height {
		y2 = v.height - 1
	}
	if x1 > x2 || y1 > y2 || v.width <= 0 || v.height <= 0 {
		return
	}
	base := bank * fbBankSize
	if v.EightBit() {
		fill := byte(ewdr)
		for y := y1; y <= y2; y += 1 {
			off := base + y*v.width + x1
			for x := x1; x <= x2; x += 1 {
				if off >= 0 && off < len(v.fb) {
					v.fb[off] = fill
				}
				off += 1
			}
		}
		return
	}
	hi := byte(ewdr >> 8)
	lo := byte(ewdr)
	for y := y1; y <= y2; y += 1 {
		off := base + (y*v.width+x1)*2
		for x := x1; x <= x2; x += 1 {
			if off >= 0 && off+1 < len(v.fb) {
				v.fb[off] = hi
				v.fb[off+1] = lo
			}
			off += 2
		}
	}
}

// PollTrigger is called once per scanline. It watches the PTMR
// word: when the PTM bits become 1 and were not 1 at the previous
// poll, a manual draw starts.
func (v *VDP1) PollTrigger() {
	cur := v.reg(regPTMR) & 0x3
	prev := v.prevPTM
	v.prevPTM = cur
	if cur == 1 && prev != 1 {
		v.Draw()
	}
}

// readDrawPixel returns the current draw bank pixel at (x, y) as a
// 16 bit value. In 8 bit mode the stored byte is zero extended;
// color calculation in 8 bit mode operates on 16 bit values first,
// so this is the documented source for the old value there.
func (v *VDP1) readDrawPixel(x, y int) uint16 {
	base := v.drawBank * fbBankSize
	if v.EightBit() {
		off := base + y*v.width + x
		if off < 0 || off >= len(v.fb) {
			return 0
		}
		return uint16(v.fb[off])
	}
	off := base + (y*v.width+x)*2
	if off < 0 || off+1 >= len(v.fb) {
		return 0
	}
	return uint16(v.fb[off])<<8 | uint16(v.fb[off+1])
}

// writeDrawPixel writes one computed pixel to the draw bank. In 8
// bit mode only the low byte of the 16 bit value is stored.
func (v *VDP1) writeDrawPixel(x, y int, data uint16) {
	base := v.drawBank * fbBankSize
	if v.EightBit() {
		off := base + y*v.width + x
		if off >= 0 && off < len(v.fb) {
			v.fb[off] = byte(data)
		}
		return
	}
	off := base + (y*v.width+x)*2
	if off >= 0 && off+1 < len(v.fb) {
		v.fb[off] = byte(data >> 8)
		v.fb[off+1] = byte(data)
	}
}
