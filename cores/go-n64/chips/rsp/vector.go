package rsp

import "math"

// This file implements COP2: the vector computational
// instructions, the vector loads and stores (LWC2/SWC2),
// and the scalar/vector moves. Semantics follow the
// documented Operation pseudocode for each instruction,
// summarized in our own words in the per-op comments.
//
// Two documented judgment calls, both flagged in the
// README:
//
//   - The plain compares (VLT/VEQ/VNE/VGE) combine with a
//     preceding VSUBC exactly as the chapter prose
//     describes: they read the borrow (VCO low byte) and
//     not-equal (VCO high byte) flags VSUBC leaves, and
//     clear VCO and VCE on output. In single precision
//     (VCO clear) they reduce to the plain signed
//     comparison, as documented.
//   - The clip compares write VCC with the le (vs <= -vt)
//     bit for element i at bit i and the ge (vs >= vt) bit
//     at bit i+8, following the guide's register-format
//     figure and the VCH/VCL Operation sections (one
//     prose sentence in the guide swaps them).

// elemIndex maps a destination lane i to the source
// element of vt for the documented element encodings:
// 0..1 whole vector, 2..3 scalar quarter (one of each
// element pair broadcast within the pair), 4..7 scalar
// half (one of each group of four broadcast within the
// group), 8..15 scalar whole (one element broadcast to
// all lanes).
func elemIndex(e, i int) int {
	switch {
	case e < 2:
		return i
	case e < 4:
		return (e & 1) + (i & 6)
	case e < 8:
		return (e & 3) + (i & 4)
	default:
		return e & 7
	}
}

// vByte reads byte b (0 = most significant) of a vector
// register.
func (r *RSP) vByte(reg, b int) byte {
	return byte(r.V[reg][b>>1] >> (8 * uint(1-(b&1))))
}

// setVByte writes byte b of a vector register.
func (r *RSP) setVByte(reg, b int, v byte) {
	if b&1 == 0 {
		r.V[reg][b>>1] = r.V[reg][b>>1]&0x00FF | uint16(v)<<8
	} else {
		r.V[reg][b>>1] = r.V[reg][b>>1]&0xFF00 | uint16(v)
	}
}

// execCOP2 handles the COP2 opcode space: moves between
// the scalar and vector files (rs 0/2/4/6) and vector
// computation (rs bit 4 set, the element field rides in
// the low rs bits).
func (r *RSP) execCOP2(instr uint32) {
	rs := (instr >> 21) & 0x1F
	rt := (instr >> 16) & 0x1F
	vd := (instr >> 11) & 0x1F
	elem := (instr >> 7) & 0xF
	switch {
	case rs == 0: // MFC2: sign-extended 16-bit element at byte offset elem
		v := uint16(r.vByte(int(vd), int(elem)))<<8 |
			uint16(r.vByte(int(vd), int((elem+1)&0xF)))
		r.Reg[rt] = uint32(int32(int16(v)))
	case rs == 2: // CFC2: control register selected by the rd field
		var v uint16
		switch vd {
		case 0:
			v = r.VCO
		case 1:
			v = r.VCC
		case 2:
			v = uint16(r.VCE)
		}
		r.Reg[rt] = uint32(int32(int16(v)))
	case rs == 4: // MTC2: low 16 bits of rt stored at byte offset elem
		v := uint16(r.Reg[rt])
		r.setVByte(int(vd), int(elem), byte(v>>8))
		r.setVByte(int(vd), int((elem+1)&0xF), byte(v))
	case rs == 6: // CTC2
		switch vd {
		case 0:
			r.VCO = uint16(r.Reg[rt])
		case 1:
			r.VCC = uint16(r.Reg[rt])
		case 2:
			r.VCE = uint8(r.Reg[rt])
		}
	case rs&0x10 != 0: // vector computation
		r.execVector(instr)
	}
}

// execVector decodes and runs one vector computational
// instruction.
func (r *RSP) execVector(instr uint32) {
	e := int((instr >> 21) & 0xF)
	vt := int((instr >> 16) & 0x1F)
	vs := int((instr >> 11) & 0x1F)
	vd := int((instr >> 6) & 0x1F)
	funct := instr & 0x3F

	var vsA, vtA [8]uint16
	for i := 0; i < 8; i++ {
		vsA[i] = r.V[vs][i]
		vtA[i] = r.V[vt][elemIndex(e, i)]
	}

	switch {
	case funct < 0x10: // multiply family
		r.vecMultiply(vd, vs, vsA, vtA, funct)
	case funct < 0x18 || funct == 0x1D: // add family (VSAR lives at 0x1D)
		r.vecAdd(vd, vs, vsA, vtA, e, funct)
	case funct < 0x20: // reserved in the add group: NOP
	case funct < 0x28: // select family
		r.vecSelect(vd, vsA, vtA, funct)
	case funct < 0x30: // logical family
		r.vecLogical(vd, vsA, vtA, funct)
	default: // divide family
		r.vecDivide(vd, vt, vs, e, funct)
	}
}

// clamp16s clamps a 32-bit value to a signed 16-bit
// result.
func clamp16s(v int32) uint16 {
	if v > 32767 {
		return 0x7FFF
	}
	if v < -32768 {
		return 0x8000
	}
	return uint16(v)
}

// clampHiSigned reads accumulator bits 31..16 with the
// documented signed saturation (VMULF/VMACF read-out and
// the M/H partial-product read-outs).
func clampHiSigned(acc int64) uint16 {
	v := acc >> 16
	if v > 32767 {
		return 0x7FFF
	}
	if v < -32768 {
		return 0x8000
	}
	return uint16(v)
}

// clampHiUnsigned is clampHiSigned with unsigned
// saturation (VMULU/VMACU read-out).
func clampHiUnsigned(acc int64) uint16 {
	if acc < 0 {
		return 0
	}
	v := acc >> 16
	if v > 0xFFFF {
		return 0xFFFF
	}
	return uint16(v)
}

// clampLowSigned reads accumulator bits 15..0 with the
// documented signed saturation for the L/N partial
// products: the raw low half passes through when the
// accumulator fits in 32 bits; a genuine 48-bit overflow
// saturates by sign.
func clampLowSigned(acc int64) uint16 {
	if acc == int64(int32(acc)) {
		return uint16(acc)
	}
	if acc < 0 {
		return 0x8000
	}
	return 0x7FFF
}

// clampQ reads accumulator bits 32..17 with signed
// saturation and masks to the documented 0xFFF0 result
// (VMULQ/VMACQ read-out).
func clampQ(acc int64) uint16 {
	v := acc >> 17
	if v > 32767 {
		v = 32767
	}
	if v < -32768 {
		v = -32768
	}
	return uint16(v) & 0xFFF0
}

// vecMultiply implements the multiply group. The funct
// low three bits select the format (F, U, RND, Q, L, M,
// N, H) and bit 3 selects accumulate over load. Products
// and clamps follow the documented multiply table.
func (r *RSP) vecMultiply(vd, vsField int, vsA, vtA [8]uint16, funct uint32) {
	accum := funct&0x08 != 0
	format := funct & 0x07
	for i := 0; i < 8; i++ {
		s := int32(int16(vsA[i]))
		t := int32(int16(vtA[i]))
		su := uint32(vsA[i])
		tu := uint32(vtA[i])
		switch format {
		case 0, 1: // F: signed fraction; U: same product, unsigned clamp
			prod := int64(s) * int64(t)
			if accum {
				r.ACC[i] = acc48(r.ACC[i] + (prod << 1))
			} else {
				r.ACC[i] = acc48((prod << 1) + 0x8000)
			}
			if format == 0 {
				r.V[vd][i] = clampHiSigned(r.ACC[i])
			} else {
				r.V[vd][i] = clampHiUnsigned(r.ACC[i])
			}
		case 2: // RND: conditionally add vt to the accumulator
			shift := uint(0)
			if vsField == 1 { // the vs instruction field, not a register
				shift = 16
			}
			add := int64(t) << shift
			if !accum { // VRNDN: add when accumulator is negative
				if r.ACC[i] < 0 {
					r.ACC[i] = acc48(r.ACC[i] + add)
				}
			} else { // VRNDP: add when accumulator is positive
				if r.ACC[i] > 0 {
					r.ACC[i] = acc48(r.ACC[i] + add)
				}
			}
			r.V[vd][i] = clampHiSigned(r.ACC[i])
		case 3: // Q: MPEG quantization multiply / oddification
			if !accum { // VMULQ
				prod := int64(s) * int64(t)
				acc := prod << 16
				if prod < 0 {
					acc += 31 << 16
				}
				r.ACC[i] = acc48(acc)
			} else { // VMACQ: oddify the accumulator in place
				acc := r.ACC[i]
				if acc < 0 && (acc>>21)&1 == 0 {
					acc += 1 << 21
				} else if acc > 0 && (acc>>21)&1 == 0 && (acc>>21) != 0 {
					acc -= 1 << 21
				}
				r.ACC[i] = acc48(acc)
			}
			r.V[vd][i] = clampQ(r.ACC[i])
		case 4: // L: unsigned x unsigned, product shifted right 16
			prod := uint64(su) * uint64(tu)
			delta := int64(prod >> 16)
			if accum {
				r.ACC[i] = acc48(r.ACC[i] + delta)
			} else {
				r.ACC[i] = delta
			}
			r.V[vd][i] = clampLowSigned(r.ACC[i])
		case 5: // M: signed x unsigned, full product
			prod := int64(s) * int64(tu)
			if accum {
				r.ACC[i] = acc48(r.ACC[i] + prod)
			} else {
				r.ACC[i] = acc48(prod)
			}
			r.V[vd][i] = clampHiSigned(r.ACC[i])
		case 6: // N: unsigned x signed, full product
			prod := int64(su) * int64(t)
			if accum {
				r.ACC[i] = acc48(r.ACC[i] + prod)
			} else {
				r.ACC[i] = acc48(prod)
			}
			r.V[vd][i] = clampLowSigned(r.ACC[i])
		case 7: // H: signed x signed, product shifted left 16
			prod := int64(s) * int64(t)
			delta := prod << 16
			if accum {
				r.ACC[i] = acc48(r.ACC[i] + delta)
			} else {
				r.ACC[i] = acc48(delta)
			}
			r.V[vd][i] = clampHiSigned(r.ACC[i])
		}
	}
}

// vecAdd implements the add group: VADD, VSUB, VABS,
// VADDC, VSUBC, and VSAR. VADD/VSUB consume the VCO low
// byte as carry/borrow in and clear VCO; VADDC/VSUBC
// produce it.
func (r *RSP) vecAdd(vd, vs int, vsA, vtA [8]uint16, e int, funct uint32) {
	switch funct {
	case 0x10, 0x11: // VADD, VSUB
		vco := r.VCO
		for i := 0; i < 8; i++ {
			var res int32
			if funct == 0x10 {
				res = int32(int16(vsA[i])) + int32(int16(vtA[i])) + int32((vco>>uint(i))&1)
			} else {
				res = int32(int16(vsA[i])) - int32(int16(vtA[i])) - int32((vco>>uint(i))&1)
			}
			r.ACC[i] = int64(res)
			r.V[vd][i] = clamp16s(res)
		}
		r.VCO = 0
	case 0x13: // VABS: negate vt by the sign of vs
		for i := 0; i < 8; i++ {
			var res uint16
			switch s := int16(vsA[i]); {
			case s < 0:
				res = uint16(-int32(int16(vtA[i])))
			case s == 0:
				res = 0
			default:
				res = vtA[i]
			}
			r.ACC[i] = int64(int16(res))
			r.V[vd][i] = res
		}
	case 0x14: // VADDC
		var vco uint16
		for i := 0; i < 8; i++ {
			sum := uint32(vsA[i]) + uint32(vtA[i])
			r.ACC[i] = int64(int16(uint16(sum)))
			r.V[vd][i] = uint16(sum)
			if sum > 0xFFFF {
				vco |= 1 << uint(i)
			}
			// The not-equal bit is written 0 by VADDC per
			// the documented Operation.
		}
		r.VCO = vco
	case 0x15: // VSUBC
		var vco uint16
		for i := 0; i < 8; i++ {
			d := int32(vsA[i]) - int32(vtA[i])
			r.ACC[i] = int64(int16(uint16(d)))
			r.V[vd][i] = uint16(d)
			if d < 0 {
				vco |= 1 << uint(i)
			}
			if d != 0 {
				vco |= 1 << uint(i+8)
			}
		}
		r.VCO = vco
	case 0x1D: // VSAR: read one accumulator slice, write vs into it
		if e > 2 {
			return
		}
		shift := uint((2 - e) * 16)
		for i := 0; i < 8; i++ {
			r.V[vd][i] = uint16(r.ACC[i] >> shift)
			u := uint64(r.ACC[i]) & ((1 << 48) - 1)
			u = u&^(0xFFFF<<shift) | uint64(r.V[vs][i])<<shift
			r.ACC[i] = acc48(int64(u))
		}
	}
}

// vecSelect implements the select group: the plain
// compares, the VCH/VCL/VCR clip family, and VMRG.
func (r *RSP) vecSelect(vd int, vsA, vtA [8]uint16, funct uint32) {
	switch funct {
	case 0x20, 0x21, 0x22, 0x23: // VLT, VEQ, VNE, VGE
		vco := r.VCO
		var vcc uint16
		for i := 0; i < 8; i++ {
			s, t := int16(vsA[i]), int16(vtA[i])
			borrow := (vco >> uint(i)) & 1
			ne := (vco >> uint(i+8)) & 1
			var c bool
			switch funct {
			case 0x20: // VLT
				c = s < t || (s == t && borrow != 0)
			case 0x21: // VEQ
				c = s == t && ne == 0
			case 0x22: // VNE
				c = s != t || (s == t && ne != 0)
			case 0x23: // VGE
				c = s > t || (s == t && borrow == 0)
			}
			sel := vtA[i]
			if c {
				sel = vsA[i]
				vcc |= 1 << uint(i)
			}
			r.ACC[i] = int64(int16(sel))
			r.V[vd][i] = sel
		}
		r.VCC = vcc
		r.VCO = 0
		r.VCE = 0
	case 0x25: // VCH: clip test, high half
		var vcc, vco uint16
		var vce uint8
		for i := 0; i < 8; i++ {
			s := int32(int16(vsA[i]))
			t := int32(int16(vtA[i]))
			sign := (vsA[i]^vtA[i])&0x8000 != 0
			var ge, le, isVCE, eq bool
			var di int32
			if sign {
				ge = t < 0
				sum := s + t
				le = sum <= 0
				isVCE = sum == -1
				eq = sum == 0
				if le {
					di = -t
				} else {
					di = s
				}
			} else {
				le = t < 0
				d := s - t
				ge = d >= 0
				eq = d == 0
				if ge {
					di = t
				} else {
					di = s
				}
			}
			res := uint16(di)
			r.ACC[i] = int64(int16(res))
			r.V[vd][i] = res
			if ge {
				vcc |= 1 << uint(i+8)
			}
			if le {
				vcc |= 1 << uint(i)
			}
			if sign {
				vco |= 1 << uint(i)
			}
			if !eq {
				vco |= 1 << uint(i+8)
			}
			if isVCE {
				vce |= 1 << uint(i)
			}
		}
		r.VCC, r.VCO, r.VCE = vcc, vco, vce
	case 0x24: // VCL: clip test, low half (consumes VCH state)
		vccIn, vcoIn, vceIn := r.VCC, r.VCO, r.VCE
		var vcc uint16
		for i := 0; i < 8; i++ {
			le := (vccIn>>uint(i))&1 != 0
			ge := (vccIn>>uint(i+8))&1 != 0
			vce := (vceIn>>uint(i))&1 != 0
			eq := (vcoIn>>uint(i+8))&1 == 0
			sign := (vcoIn>>uint(i))&1 != 0
			var di uint16
			if sign {
				sum := uint32(vsA[i]) + uint32(vtA[i])
				carry := sum > 0xFFFF
				if eq {
					if vce {
						le = (sum&0xFFFF) == 0 || !carry
					} else {
						le = (sum&0xFFFF) == 0 && !carry
					}
				}
				if le {
					di = -vtA[i]
				} else {
					di = vsA[i]
				}
			} else {
				d := int32(int16(vsA[i])) - int32(int16(vtA[i]))
				if eq {
					ge = d >= 0
				}
				if ge {
					di = vtA[i]
				} else {
					di = vsA[i]
				}
			}
			r.ACC[i] = int64(int16(di))
			r.V[vd][i] = di
			if le {
				vcc |= 1 << uint(i)
			}
			if ge {
				vcc |= 1 << uint(i+8)
			}
		}
		r.VCC = vcc
		r.VCO = 0
		r.VCE = 0
	case 0x26: // VCR: ones-complement clip test
		var vcc uint16
		for i := 0; i < 8; i++ {
			s := int32(int16(vsA[i]))
			t := int32(int16(vtA[i]))
			sign := (vsA[i]^vtA[i])&0x8000 != 0
			var ge, le bool
			var di uint16
			if sign {
				ge = t < 0
				le = s+t+1 <= 0
				if le {
					di = ^vtA[i]
				} else {
					di = vsA[i]
				}
			} else {
				le = t < 0
				ge = s-t >= 0
				if ge {
					di = vtA[i]
				} else {
					di = vsA[i]
				}
			}
			r.ACC[i] = int64(int16(di))
			r.V[vd][i] = di
			if ge {
				vcc |= 1 << uint(i+8)
			}
			if le {
				vcc |= 1 << uint(i)
			}
		}
		r.VCC = vcc
		r.VCO = 0
		r.VCE = 0
	case 0x27: // VMRG: merge by VCC low byte
		for i := 0; i < 8; i++ {
			sel := vtA[i]
			if (r.VCC>>uint(i))&1 != 0 {
				sel = vsA[i]
			}
			r.ACC[i] = int64(int16(sel))
			r.V[vd][i] = sel
		}
	}
}

// vecLogical implements the logical group. Results also
// land in the accumulator low halves, per the documented
// Operation sections.
func (r *RSP) vecLogical(vd int, vsA, vtA [8]uint16, funct uint32) {
	for i := 0; i < 8; i++ {
		var res uint16
		switch funct {
		case 0x28: // VAND
			res = vsA[i] & vtA[i]
		case 0x29: // VNAND
			res = ^(vsA[i] & vtA[i])
		case 0x2A: // VOR
			res = vsA[i] | vtA[i]
		case 0x2B: // VNOR
			res = ^(vsA[i] | vtA[i])
		case 0x2C: // VXOR
			res = vsA[i] ^ vtA[i]
		case 0x2D: // VNXOR
			res = ^(vsA[i] ^ vtA[i])
		}
		r.ACC[i] = int64(int16(res))
		r.V[vd][i] = res
	}
}

// vecDivide implements the divide group: VRCP, VRCPL,
// VRCPH, VRSQ, VRSQL, VRSQH, VMOV, VNOP. The destination
// element comes from the vs instruction field and the
// source element from the element field, per the
// documentation. The H forms latch the input high half
// and write out the previously computed result's high
// half; the L forms combine with the latched high half.
//
// Reciprocals are computed exactly (integer arithmetic
// for RCP, float64 for RSQ) in the documented formats:
// single precision reads the element as an S1.15 fraction
// and returns S16.15; double precision reads the latched
// 32-bit input as S15.16 and returns S16.15. The hardware
// uses a 1024-entry lookup table with about 10 bits of
// precision, so hardware low bits can differ; see the
// README precision note. Negative inputs produce the
// ones complement of the positive result, and zero
// saturates to 0x7FFFFFFF, both per the documentation.
func (r *RSP) vecDivide(vd, vt, vsField, e int, funct uint32) {
	de := vsField & 7
	elem := r.V[vt][e&7]
	switch funct {
	case 0x30, 0x34: // VRCP, VRSQ (single precision)
		mag, neg := abs16(elem)
		res := recipResult(mag, 15, funct == 0x34)
		if neg {
			res = ^res
		}
		r.divOut = res
		r.V[vd][de] = uint16(res)
		r.recipAccSideEffect(elem)
	case 0x31, 0x35: // VRCPL, VRSQL (double precision, low half in)
		d := (r.divIn &^ 0xFFFF) | int32(elem)
		r.divIn = d
		mag, neg := abs32(d)
		res := recipResult(mag, 16, funct == 0x35)
		if neg {
			res = ^res
		}
		r.divOut = res
		r.V[vd][de] = uint16(res)
		r.recipAccSideEffect(elem)
	case 0x32, 0x36: // VRCPH, VRSQH (latch high half, write prior high)
		r.divIn = (int32(int16(elem)) << 16) | (r.divIn & 0xFFFF)
		r.V[vd][de] = uint16(r.divOut >> 16)
		r.recipAccSideEffect(elem)
	case 0x33: // VMOV
		r.V[vd][de] = elem
	case 0x37: // VNOP
	}
}

// recipAccSideEffect writes the source element into the
// low 16 bits of every accumulator lane, the documented
// side effect of the reciprocal instructions.
func (r *RSP) recipAccSideEffect(elem uint16) {
	for i := 0; i < 8; i++ {
		r.ACC[i] = (r.ACC[i] &^ 0xFFFF) | int64(elem)
	}
}

func abs16(v uint16) (uint32, bool) {
	s := int32(int16(v))
	if s < 0 {
		return uint32(-s), true
	}
	return uint32(s), false
}

func abs32(v int32) (uint32, bool) {
	if v < 0 {
		return uint32(-(int64(v))), true
	}
	return uint32(v), false
}

// recipResult computes a reciprocal (rsq=false) or
// reciprocal square root (rsq=true) of the positive
// magnitude mag, where the input value is mag / 2^frac
// and the result is in S16.15 format.
func recipResult(mag uint32, frac uint, rsq bool) int32 {
	if mag == 0 {
		return 0x7FFFFFFF
	}
	if !rsq {
		num := uint64(1) << (15 + frac)
		v := (num + uint64(mag)/2) / uint64(mag)
		if v > 0x7FFFFFFF {
			v = 0x7FFFFFFF
		}
		return int32(v)
	}
	d := float64(mag) / float64(uint64(1)<<frac)
	v := math.Floor(32768.0/math.Sqrt(d) + 0.5)
	if v > 0x7FFFFFFF {
		v = 0x7FFFFFFF
	}
	return int32(v)
}

// loadStoreShift gives the offset scale for each vector
// load/store op (the rd-field encoding).
var loadStoreShift = [12]uint{0, 1, 2, 3, 4, 4, 3, 3, 4, 4, 4, 4}

// execLoadStore implements LWC2 (load=true) and SWC2
// (load=false). The rd field selects the transfer kind,
// the element field is a byte index into the vector
// register, and the 7-bit offset is sign-extended and
// scaled by the per-op shift. All addresses wrap in the
// 4 KB DMEM.
func (r *RSP) execLoadStore(instr uint32, load bool) {
	base := (instr >> 21) & 0x1F
	vt := int((instr >> 16) & 0x1F)
	op := (instr >> 11) & 0x1F
	elem := int((instr >> 7) & 0xF)
	off7 := int32(instr & 0x7F)
	if off7&0x40 != 0 {
		off7 -= 0x80
	}
	if op > 11 {
		return
	}
	addr := (r.Reg[base] + uint32(off7<<loadStoreShift[op])) & 0xFFF

	// memByte and regByte move one byte in the transfer
	// direction.
	get := func(a uint32) byte { return r.dmemByte(a) }
	put := func(a uint32, v byte) { r.setDMEMByte(a, v) }

	switch op {
	case 0: // LBV / SBV
		if load {
			r.setVByte(vt, elem, get(addr))
		} else {
			put(addr, r.vByte(vt, elem))
		}
	case 1, 2, 3: // LSV/SSV (2), LLV/SLV (4), LDV/SDV (8)
		n := 2 << (op - 1)
		for k := 0; k < n; k++ {
			if load {
				r.setVByte(vt, (elem+k)&0xF, get(addr+uint32(k)))
			} else {
				put(addr+uint32(k), r.vByte(vt, (elem+k)&0xF))
			}
		}
	case 4: // LQV / SQV: from addr to the end of its 16-byte block
		n := 16 - int(addr&0xF)
		for k := 0; k < n; k++ {
			if load {
				r.setVByte(vt, (elem+k)&0xF, get(addr+uint32(k)))
			} else {
				put(addr+uint32(k), r.vByte(vt, (elem+k)&0xF))
			}
		}
	case 5: // LRV / SRV: the tail of the block into the register tail
		n := int(addr & 0xF)
		for k := 0; k < n; k++ {
			a := addr - uint32(n) + uint32(k)
			if load {
				r.setVByte(vt, 16-n+k, get(a))
			} else {
				put(a, r.vByte(vt, 16-n+k))
			}
		}
	case 6, 7: // LPV/SPV (bits 15..8), LUV/SUV (bits 14..7)
		shift := uint(8)
		if op == 7 {
			shift = 7
		}
		for i := 0; i < 8; i++ {
			if load {
				r.V[vt][i] = uint16(get(addr+uint32(i))) << shift
			} else {
				put(addr+uint32(i), byte(r.V[vt][i]>>shift))
			}
		}
	case 8: // LHV / SHV: every second byte, bits 14..7
		for i := 0; i < 8; i++ {
			a := addr + uint32(2*i)
			if load {
				r.V[vt][i] = uint16(get(a)) << 7
			} else {
				put(a, byte(r.V[vt][i]>>7))
			}
		}
	case 9: // LFV / SFV: every fourth byte into lanes (elem/2 + i)
		base0 := elem >> 1 // documented element is 0 or 8, giving lanes 0..3 or 4..7
		for i := 0; i < 4; i++ {
			a := addr + uint32(4*i)
			if load {
				r.V[vt][base0+i] = uint16(get(a)) << 7
			} else {
				put(a, byte(r.V[vt][base0+i]>>7))
			}
		}
	case 10: // SWV (store only): the register rotated by elem/2 lanes
		if load {
			return
		}
		block := addr &^ 0xF
		for j := 0; j < 8; j++ {
			v := r.V[vt][(j+(elem>>1))&7]
			put(block+uint32(2*j), byte(v>>8))
			put(block+uint32(2*j)+1, byte(v))
		}
	case 11: // LTV / STV: transpose across an 8-register group
		block := addr &^ 0xF
		lane := elem >> 1
		for j := 0; j < 8; j++ {
			reg := (vt & 0x18) | ((j + lane) & 7)
			a := block + uint32(2*j)
			if load {
				r.V[reg][lane] = uint16(get(a))<<8 | uint16(get(a+1))
			} else {
				v := r.V[reg][lane]
				put(a, byte(v>>8))
				put(a+1, byte(v))
			}
		}
	}
}
