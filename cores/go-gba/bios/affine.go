package bios

import (
	"math"

	"github.com/davidpatton/itak-gaming/cores/go-gba/chips/arm7tdmi"
)

// affineParams computes the pa, pb, pc, pd matrix entries
// (signed 8.8 fixed point) for a rotation by angle, where
// angle is in BIOS units (0x10000 per full turn) and the
// ratios are signed 8.8 fixed point scale factors.
//
// With th the angle in radians, the matrix in screen
// space (where Y points down, so a positive angle turns
// clockwise on screen) is:
//
//	pa = cos(th) * ratioX    pb = -sin(th) * ratioX
//	pc = sin(th) * ratioY    pd = cos(th) * ratioY
//
// The ratios are already 8.8 values and cos and sin are
// unit values, so the float products land in 8.8 directly
// and convert to int16 with the usual truncation.
func affineParams(ratioX, ratioY int16, angle uint16) (pa, pb, pc, pd int16) {
	th := float64(angle) * (2 * math.Pi) / 0x10000
	cosT := math.Cos(th)
	sinT := math.Sin(th)
	pa = int16(cosT * float64(ratioX))
	pb = int16(-sinT * float64(ratioX))
	pc = int16(sinT * float64(ratioY))
	pd = int16(cosT * float64(ratioY))
	return pa, pb, pc, pd
}

// bgAffineSet (SWI 0x0E) expands compact background
// affine descriptions into the full parameter sets the
// display hardware uses.
//
// Source entries are 20 bytes, in GBATEK order:
//
//	s32 originX, s32 originY: the reference point in
//	texture space, 8 fractional bits
//	s16 centerX, s16 centerY: the display centre in
//	plain pixels
//	s16 ratioX, s16 ratioY: scale factors, 8 fractional
//	bits (0x100 is 1.0)
//	u16 angle: 0x10000 per full turn
//	2 bytes padding
//
// Note on the centre fields: they are halfwords in the
// documented 20 byte entry. (Four word sized fields plus
// the ratios and angle would need 22 bytes, so the entry
// size pins the centres as halfwords; the formulas use
// them as plain integer pixel counts, sign extended.)
//
// Destination entries are 16 bytes: s16 pa, pb, pc, pd,
// then s32 dx, dy, the texture coordinate of the display
// centre minus the rotated centre offset:
//
//	dx = originX - ((pa*centerX + pb*centerY) >> 8)
//	dy = originY - ((pc*centerX + pd*centerY) >> 8)
//
// The products are 8.8 fixed point times integer pixels,
// so shifting them right by 8 (an arithmetic shift on
// int64 intermediates) brings them back into 8.8 texture
// space alongside the origins.
func (h *Handler) bgAffineSet(c *arm7tdmi.CPU, ret uint32) int {
	src, dst, count := c.R[0], c.R[1], c.R[2]
	for i := uint32(0); i < count; i++ {
		s := src + 20*i
		originX := int64(int32(h.rd32(s + 0)))
		originY := int64(int32(h.rd32(s + 4)))
		centerX := int64(int16(h.rd16(s + 8)))
		centerY := int64(int16(h.rd16(s + 10)))
		ratioX := int16(h.rd16(s + 12))
		ratioY := int16(h.rd16(s + 14))
		angle := uint16(h.rd16(s + 16))

		pa, pb, pc, pd := affineParams(ratioX, ratioY, angle)
		dx := originX - ((int64(pa)*centerX + int64(pb)*centerY) >> 8)
		dy := originY - ((int64(pc)*centerX + int64(pd)*centerY) >> 8)

		d := dst + 16*i
		h.wr16(d+0, uint32(pa))
		h.wr16(d+2, uint32(pb))
		h.wr16(d+4, uint32(pc))
		h.wr16(d+6, uint32(pd))
		h.wr32(d+8, uint32(int32(dx)))
		h.wr32(d+12, uint32(int32(dy)))
	}
	c.Jump(ret)
	return 3 + 12*int(count)
}

// objAffineSet (SWI 0x0F) computes the same matrix for
// sprites. Source entries are 8 bytes: s16 ratioX, s16
// ratioY, u16 angle, 2 bytes padding. The destination is
// an OAM base: the four parameters of entry i land in the
// strided affine slots of OAM entry i, at byte offsets
// i*0x20 + 0x06 (pa), +0x0E (pb), +0x16 (pc) and +0x1E
// (pd) from the base.
func (h *Handler) objAffineSet(c *arm7tdmi.CPU, ret uint32) int {
	src, dst, count := c.R[0], c.R[1], c.R[2]
	for i := uint32(0); i < count; i++ {
		s := src + 8*i
		ratioX := int16(h.rd16(s + 0))
		ratioY := int16(h.rd16(s + 2))
		angle := uint16(h.rd16(s + 4))

		pa, pb, pc, pd := affineParams(ratioX, ratioY, angle)
		base := dst + 0x20*i
		h.wr16(base+0x06, uint32(pa))
		h.wr16(base+0x0E, uint32(pb))
		h.wr16(base+0x16, uint32(pc))
		h.wr16(base+0x1E, uint32(pd))
	}
	c.Jump(ret)
	return 3 + 8*int(count)
}
