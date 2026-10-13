package allegrex

import "math"

// VFPU (COP2) implementation.
//
// Layout: 8 matrices, each 4 rows by 4 columns. The linear index of
// an element is m*16 + row*4 + col. A 7 bit register field encodes
// the matrix in bits 6:4 and a position in the low 4 bits; the low
// nibble means different things per operand size, see vecIndices.
//
// Prefixes: the source prefix (spfx for VS reads, tpfx for VT reads)
// swizzles, takes absolute value, negates, or substitutes a constant
// per component. The destination prefix (dpfx) saturates or masks
// per component on a VD write. Each prefix is one shot: it is
// consumed by the first matching read or write that uses it.
// Matrix operations (vmmul, vmscl, matrix vmov) do not apply any
// prefix; that is a documented simplification of this slice.
// Conversions read their source with the source prefix applied but
// write their destination raw, with no destination prefix.
// The VFPU trig unit is pi/2: vsin(x) is sin(x * pi/2) and likewise
// for vcos and vnsin.

var vfpuConstTable = [8]float32{0, 1, 2, 1.0 / 3.0, 1.0 / 4.0, 1.0 / 6.0, 1.0 / 2.0, 3}

// vecIndices decodes a 7 bit register field r for an operand of
// size n lanes (1 single, 2 pair, 3 triple, 4 quad) and returns the
// linear indices of its elements; the first n entries are valid.
func vecIndices(r uint32, n int) [4]int {
	m := int((r >> 4) & 7)
	low := r & 15
	var out [4]int
	switch n {
	case 1:
		row := int((low >> 2) & 3)
		col := int(low & 3)
		out[0] = m*16 + row*4 + col
	case 2:
		tr := low & 1
		half := int((low >> 1) & 1)
		ix := int((low >> 2) & 3)
		if tr == 0 {
			out[0] = m*16 + ix*4 + half*2
			out[1] = m*16 + ix*4 + half*2 + 1
		} else {
			out[0] = m*16 + (half*2)*4 + ix
			out[1] = m*16 + (half*2+1)*4 + ix
		}
	case 3:
		tr := low & 1
		ix := int((low >> 2) & 3)
		if tr == 0 {
			for i := 0; i < 3; i++ {
				out[i] = m*16 + ix*4 + i
			}
		} else {
			for i := 0; i < 3; i++ {
				out[i] = m*16 + i*4 + ix
			}
		}
	default: // 4
		tr := low & 1
		ix := int((low >> 2) & 3)
		if tr == 0 {
			for i := 0; i < 4; i++ {
				out[i] = m*16 + ix*4 + i
			}
		} else {
			for i := 0; i < 4; i++ {
				out[i] = m*16 + i*4 + ix
			}
		}
	}
	return out
}

// singleIndex decodes a 7 bit field as a single element.
func singleIndex(r uint32) int {
	return vecIndices(r, 1)[0]
}

// matIndex returns the linear index of matrix m element (i, j),
// where i is the row and j is the column.
func matIndex(m, i, j int) int { return m*16 + i*4 + j }

// matrixOrigin decodes the matrix, transpose flag and index from a
// 7 bit field.
func matrixOrigin(r uint32) (m int, tr bool, ix int) {
	m = int((r >> 4) & 7)
	low := r & 15
	tr = low&1 != 0
	ix = int((low >> 2) & 3)
	return m, tr, ix
}

// matrixFilePos maps a logical matrix element (i, j) of size n to a
// file position (row, col) under the origin rules: with no
// transpose the origin row is ix masked to the size and the column
// origin is 0; transposed, the origin column is ix masked to the
// size and the row origin is 0, and the element reads the file at
// (j, originC + i).
func matrixFilePos(r uint32, n, i, j int) (int, int, int) {
	m, tr, ix := matrixOrigin(r)
	if !tr {
		originR := ix &^ (n - 1)
		return m, originR + i, j
	}
	originC := ix &^ (n - 1)
	_ = originC
	return m, j, originC + i
}

// readMatrix reads a size n matrix operand raw, with no prefix.
func (c *CPU) readMatrix(r uint32, n int) [4][4]float64 {
	var out [4][4]float64
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			m, row, col := matrixFilePos(r, n, i, j)
			out[i][j] = fpInput(c.vregs[matIndex(m, row, col)])
		}
	}
	return out
}

// writeMatrix writes a size n matrix operand raw, with no prefix.
func (c *CPU) writeMatrix(r uint32, n int, vals [4][4]float64) {
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			m, row, col := matrixFilePos(r, n, i, j)
			c.vregs[matIndex(m, row, col)] = roundToSingle(vals[i][j], 0)
		}
	}
}

// writeMatrixRawBits writes a size n matrix of raw bit patterns.
func (c *CPU) writeMatrixRawBits(r uint32, n int, vals [4][4]uint32) {
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			m, row, col := matrixFilePos(r, n, i, j)
			c.vregs[matIndex(m, row, col)] = vals[i][j]
		}
	}
}

// applySourcePrefix applies a 19 bit source prefix immediate to a
// padded source vector. Lanes at or beyond the source size were
// padded with zero by the caller. Per output lane the field selects
// a swizzle lane, an absolute value flag and a negate flag; abs and
// neg together select a constant instead.
func applySourcePrefix(padded [4]float64, imm uint32, n int) [4]float64 {
	var out [4]float64
	for i := 0; i < n && i < 4; i++ {
		field := (imm >> uint(4*i)) & 0xF
		swizzle := int(field & 3)
		abs := field&4 != 0
		neg := field&8 != 0
		if abs && neg {
			idx := swizzle | int(((imm>>16)&1)<<2)
			out[i] = float64(vfpuConstTable[idx])
			continue
		}
		v := padded[swizzle]
		if abs {
			v = math.Abs(v)
		}
		if neg {
			v = -v
		}
		out[i] = v
	}
	return out
}

// paddedFloats builds the padded float vector for a raw read: the
// operand lanes through fpInput, zeros beyond the size.
func (c *CPU) paddedFloats(r uint32, n int) [4]float64 {
	idx := vecIndices(r, n)
	var out [4]float64
	for i := 0; i < n; i++ {
		out[i] = fpInput(c.vregs[idx[i]])
	}
	return out
}

// readVS reads the VS operand of size n, consuming the source
// prefix if one is armed.
func (c *CPU) readVS(r uint32, n int) [4]float64 {
	padded := c.paddedFloats(r, n)
	if c.spfxOn {
		c.spfxOn = false
		return applySourcePrefix(padded, c.spfxImm, n)
	}
	return padded
}

// readVT reads the VT operand of size n, consuming the target
// prefix if one is armed.
func (c *CPU) readVT(r uint32, n int) [4]float64 {
	padded := c.paddedFloats(r, n)
	if c.tpfxOn {
		c.tpfxOn = false
		return applySourcePrefix(padded, c.tpfxImm, n)
	}
	return padded
}

// readVSRaw reads the VS operand raw with no prefix and no consume.
func (c *CPU) readVSRaw(r uint32, n int) [4]float64 {
	return c.paddedFloats(r, n)
}

// applySPFXNow applies and consumes the source prefix on an
// already converted float vector. Used by vh2f and vi2f, whose
// sources are not floats in the register file.
func (c *CPU) applySPFXNow(vals [4]float64, n int) [4]float64 {
	if c.spfxOn {
		c.spfxOn = false
		return applySourcePrefix(vals, c.spfxImm, n)
	}
	return vals
}

// writeVD writes float values to the VD operand of size n,
// consuming the destination prefix if one is armed: saturation
// clamps per lane, masked lanes keep their old value.
func (c *CPU) writeVD(r uint32, n int, vals [4]float64) {
	idx := vecIndices(r, n)
	var imm uint32
	on := c.dpfxOn
	if on {
		imm = c.dpfxImm
		c.dpfxOn = false
	}
	for i := 0; i < n; i++ {
		v := vals[i]
		if on {
			field := (imm >> uint(3*i)) & 0x7
			sat := field & 3
			mask := field&4 != 0
			if mask {
				continue
			}
			switch sat {
			case 1:
				if math.IsNaN(v) {
					v = 0
				} else if v < 0 {
					v = 0
				} else if v > 1 {
					v = 1
				}
			case 2:
				if math.IsNaN(v) {
					v = 0
				} else if v < -1 {
					v = -1
				} else if v > 1 {
					v = 1
				}
			}
		}
		c.vregs[idx[i]] = roundToSingle(v, 0)
	}
}

// writeRawVec writes raw bit patterns to an operand of size n with
// no destination prefix and no consumption of it.
func (c *CPU) writeRawVec(r uint32, n int, vals [4]uint32) {
	idx := vecIndices(r, n)
	for i := 0; i < n; i++ {
		c.vregs[idx[i]] = vals[i]
	}
}

// writeRawFloats converts float values to bits and writes them raw.
func (c *CPU) writeRawFloats(r uint32, n int, vals [4]float64) {
	var bits [4]uint32
	for i := 0; i < n; i++ {
		bits[i] = roundToSingle(vals[i], 0)
	}
	c.writeRawVec(r, n, bits)
}

// vfpuUnimplemented faults a genuinely unimplemented VFPU encoding.
func (c *CPU) vfpuUnimplemented() int {
	c.vfpuFaultCount++
	c.raiseException(excRI, 0)
	return 1
}

// vfpuCtrlRead reads a VFPU control index for MFVC.
func (c *CPU) vfpuCtrlRead(idx uint32) uint32 {
	switch {
	case idx == 128:
		return c.spfxImm
	case idx == 129:
		return c.tpfxImm
	case idx == 130:
		return c.dpfxImm
	case idx == 131:
		return c.cc
	case idx == 132:
		return 0xFFFFFFFF
	case idx == 135:
		return 0x00003351
	case idx >= 136 && idx <= 143:
		return c.rngState[idx-136]
	default:
		if idx < 144 {
			return c.mfvcStore[idx]
		}
		return 0
	}
}

// vfpuCtrlWrite writes a VFPU control index for MTVC. Writing a
// prefix index arms that prefix.
func (c *CPU) vfpuCtrlWrite(idx uint32, v uint32) {
	switch {
	case idx == 128:
		c.spfxImm = v & 0x7FFFF
		c.spfxOn = true
	case idx == 129:
		c.tpfxImm = v & 0x7FFFF
		c.tpfxOn = true
	case idx == 130:
		c.dpfxImm = v & 0xFFF
		c.dpfxOn = true
	case idx == 131:
		c.cc = v
	case idx == 132, idx == 135:
		// Ignored: read only registers.
	case idx >= 136 && idx <= 143:
		c.rngState[idx-136] = v
	default:
		if idx < 144 {
			c.mfvcStore[idx] = v
		}
	}
}

// execCOP2 dispatches COP2 instructions. COP2 now holds only the
// moves between the GPRs and the VFPU and the BC2 branch; every
// arithmetic encoding lives under its own primary opcode. Any
// other rs value is a reserved instruction.
func (c *CPU) execCOP2(instr uint32) int {
	rs := (instr >> 21) & 31
	rt := int((instr >> 16) & 31)
	switch rs {
	case 0x00: // MFC2
		reg7 := ((instr>>11)&31)<<2 | ((instr >> 6) & 3)
		c.setReg(rt, c.vregs[singleIndex(reg7)])
	case 0x02: // CFC2
		rd := (instr >> 11) & 31
		v := c.ctrl[rd]
		if rd == 0 {
			v = 0x00003351
		}
		c.setReg(rt, v)
	case 0x03: // MFVC
		idx := (instr >> 8) & 0xFF
		c.setReg(rt, c.vfpuCtrlRead(idx))
	case 0x04: // MTC2
		reg7 := ((instr>>11)&31)<<2 | ((instr >> 6) & 3)
		c.vregs[singleIndex(reg7)] = c.regs[rt]
	case 0x06: // CTC2
		rd := (instr >> 11) & 31
		if rd != 0 {
			c.ctrl[rd] = c.regs[rt]
		}
	case 0x07: // MTVC
		idx := (instr >> 8) & 0xFF
		c.vfpuCtrlWrite(idx, c.regs[rt])
	case 0x08: // BC2
		condTrue := c.cc&1 != 0
		taken := (rt&1 != 0) == condTrue
		if (rt&2) != 0 && !taken {
			// Likely form: annul the delay slot.
			c.pc = c.nextPC
			c.nextPC = c.pc + 4
			c.inDelaySlot = false
		} else {
			c.branch(taken, c.branchTarget(signExt16(instr&0xFFFF)))
		}
	default:
		return c.vfpuUnimplemented()
	}
	return 1
}

// vvFields decodes the shared VV operand fields. The size enum is
// split across bit 7 and bit 15: 0 single, 1 pair, 2 triple,
// 3 quad. The group field G is bits 25:23.
func vvFields(instr uint32) (vd, vs, vt uint32, sizeEnum uint32, n int, g uint32) {
	vd = instr & 0x7F
	vs = (instr >> 8) & 0x7F
	vt = (instr >> 16) & 0x7F
	sizeEnum = (instr>>7)&1 | ((instr>>15)&1)<<1
	n = int(sizeEnum) + 1
	g = (instr >> 23) & 7
	return vd, vs, vt, sizeEnum, n, g
}

// vvBinary applies an element wise binary float operation over
// the VS and VT operands and writes VD.
func (c *CPU) vvBinary(vd, vs, vt uint32, n int, f func(a, b float64) float64) int {
	a := c.readVS(vs, n)
	b := c.readVT(vt, n)
	var out [4]float64
	for i := 0; i < n; i++ {
		out[i] = f(a[i], b[i])
	}
	c.writeVD(vd, n, out)
	return 1
}

// execVVArith dispatches opcode 0x18: G=0 vadd, G=1 vsub,
// G=2 vsbn, G=7 vdiv.
func (c *CPU) execVVArith(instr uint32) int {
	vd, vs, vt, _, n, g := vvFields(instr)
	switch g {
	case 0:
		return c.vvBinary(vd, vs, vt, n, func(a, b float64) float64 { return a + b })
	case 1:
		return c.vvBinary(vd, vs, vt, n, func(a, b float64) float64 { return a - b })
	case 2: // vsbn, single only: set the exponent of VS from VT
		if n != 1 {
			return c.vfpuUnimplemented()
		}
		src := c.vregs[singleIndex(vs)]
		exp := int32(c.vregs[singleIndex(vt)])
		out := src
		v := fpInput(src)
		if v != 0 && !math.IsNaN(v) && !math.IsInf(v, 0) {
			out = (src & 0x807FFFFF) | uint32((exp+127)&0xFF)<<23
		}
		var bits [4]uint32
		bits[0] = out
		c.writeRawVec(vd, 1, bits)
		return 1
	case 7:
		return c.vvBinary(vd, vs, vt, n, func(a, b float64) float64 { return a / b })
	}
	return c.vfpuUnimplemented()
}

// execVVProd dispatches opcode 0x19: G=0 vmul, G=1 vdot,
// G=2 vscl, G=4 vhdp, G=5 vcrs, G=6 vdet.
func (c *CPU) execVVProd(instr uint32) int {
	vd, vs, vt, _, n, g := vvFields(instr)
	switch g {
	case 0:
		return c.vvBinary(vd, vs, vt, n, func(a, b float64) float64 { return a * b })
	case 1: // vdot, pair and up
		if n < 2 {
			return c.vfpuUnimplemented()
		}
		a := c.readVS(vs, n)
		b := c.readVT(vt, n)
		sum := 0.0
		for i := 0; i < n; i++ {
			sum += a[i] * b[i]
		}
		c.vregs[singleIndex(vd)] = roundToSingle(sum, 0)
		return 1
	case 2: // vscl: scale VS by the VT scalar, read raw
		a := c.readVS(vs, n)
		s := fpInput(c.vregs[singleIndex(vt)])
		var out [4]float64
		for i := 0; i < n; i++ {
			out[i] = a[i] * s
		}
		c.writeVD(vd, n, out)
		return 1
	case 4: // vhdp, pair and up: dot of all but the last lane,
		// plus the last VT lane on its own
		if n < 2 {
			return c.vfpuUnimplemented()
		}
		a := c.readVS(vs, n)
		b := c.readVT(vt, n)
		sum := 0.0
		for i := 0; i < n-1; i++ {
			sum += a[i] * b[i]
		}
		sum += b[n-1]
		c.vregs[singleIndex(vd)] = roundToSingle(sum, 0)
		return 1
	case 5: // vcrs, triple only: partial cross product
		if n != 3 {
			return c.vfpuUnimplemented()
		}
		a := c.readVS(vs, 3)
		b := c.readVT(vt, 3)
		var out [4]float64
		out[0] = a[1] * b[2]
		out[1] = a[2] * b[0]
		out[2] = a[0] * b[1]
		c.writeVD(vd, 3, out)
		return 1
	case 6: // vdet, pair only: 2x2 determinant to a single
		if n != 2 {
			return c.vfpuUnimplemented()
		}
		a := c.readVS(vs, 2)
		b := c.readVT(vt, 2)
		c.vregs[singleIndex(vd)] = roundToSingle(a[0]*b[1]-a[1]*b[0], 0)
		return 1
	}
	return c.vfpuUnimplemented()
}

// execVVCmpGrp dispatches opcode 0x1B: G=0 vcmp, G=2 vmin,
// G=3 vmax, G=6 vsge, G=7 vslt.
func (c *CPU) execVVCmpGrp(instr uint32) int {
	vd, vs, vt, _, n, g := vvFields(instr)
	switch g {
	case 0:
		return c.execVCMP(instr)
	case 2, 3:
		return c.vvBinary(vd, vs, vt, n, func(a, b float64) float64 {
			return vfpuMinMax(a, b, g == 3)
		})
	case 6, 7:
		return c.vvBinary(vd, vs, vt, n, func(a, b float64) float64 {
			ok := a >= b
			if g == 7 {
				ok = a < b
			}
			if ok {
				return 1
			}
			return 0
		})
	}
	return c.vfpuUnimplemented()
}

// vfpuMinMax is the shared vmin and vmax lane operation: a NaN
// operand yields the other operand, two NaNs yield a NaN.
func vfpuMinMax(a, b float64, isMax bool) float64 {
	if math.IsNaN(a) && math.IsNaN(b) {
		return math.NaN()
	}
	if math.IsNaN(a) {
		return b
	}
	if math.IsNaN(b) {
		return a
	}
	if isMax {
		if a > b {
			return a
		}
		return b
	}
	if a < b {
		return a
	}
	return b
}

// nextRand steps the VFPU xorshift generator once and returns
// the new state word.
func (c *CPU) nextRand() uint32 {
	x := c.rngState[0]
	if x == 0 {
		x = 0x1
	}
	x ^= x << 13
	x ^= x >> 17
	x ^= x << 5
	c.rngState[0] = x
	return x
}

// randUnit returns the next generator output as a value in
// [0, 1), using the top 24 bits of the state.
func (c *CPU) randUnit() float64 {
	return float64(c.nextRand()>>8) * (1.0 / 16777216.0)
}

// execVFPU34 dispatches opcode 0x34 by its group field, bits
// 25:23: group 0 is the unary group, group 4 the scaled float to
// int conversions, group 5 the scaled int to float conversion
// plus the vcmov carve out.
func (c *CPU) execVFPU34(instr uint32) int {
	vd, vs, _, _, n, g := vvFields(instr)
	f := (instr >> 16) & 0x7F
	switch g {
	case 0:
		return c.execVFPUUnary(instr, vd, vs, n, f)
	case 4:
		return c.execVF2I(vd, vs, n, f)
	case 5:
		return c.execVI2FOrCmov(vd, vs, n, f)
	}
	return c.vfpuUnimplemented()
}

// execVFPUUnary dispatches the opcode 0x34 group 0 unary and
// conversion functions, F = bits 22:16.
func (c *CPU) execVFPUUnary(instr uint32, vd, vs uint32, n int, f uint32) int {
	switch f {
	case 0x00, 0x01, 0x02, 0x04, 0x05, 0x10, 0x11, 0x12, 0x13,
		0x14, 0x15, 0x16, 0x17, 0x18, 0x1A, 0x1C, 0x4A:
		a := c.readVS(vs, n)
		var out [4]float64
		for i := 0; i < n; i++ {
			out[i] = vfpuUnaryOp(f, a[i])
		}
		c.writeVD(vd, n, out)
		return 1
	case 0x03: // vidt: identity matrix, raw write, no source
		var id [4][4]float64
		for i := 0; i < n; i++ {
			id[i][i] = 1
		}
		c.writeMatrix(vd, n, id)
		return 1
	case 0x06: // vzero
		var out [4]float64
		c.writeVD(vd, n, out)
		return 1
	case 0x07: // vone
		var out [4]float64
		for i := range out {
			out[i] = 1
		}
		c.writeVD(vd, n, out)
		return 1
	case 0x08: // vrepl: raw source single, no prefix. This
		// encoding could not be confirmed in the published docs;
		// it sits at a free group 0 F value, see the report.
		v := fpInput(c.vregs[singleIndex(vs)])
		var out [4]float64
		for i := range out {
			out[i] = v
		}
		c.writeVD(vd, n, out)
		return 1
	case 0x09: // vsplt: same unconfirmed status as vrepl.
		a := c.readVS(vs, n)
		lane := int(vs & 3)
		if lane >= n {
			lane = n - 1
		}
		var out [4]float64
		for i := range out {
			out[i] = a[lane]
		}
		c.writeVD(vd, n, out)
		return 1
	case 0x20: // vrnds: seed the generator from the raw VS bits
		c.rngState[0] = c.vregs[singleIndex(vs)]
		return 1
	case 0x21: // vrndi: raw random words per lane
		var bits [4]uint32
		for i := 0; i < n; i++ {
			bits[i] = c.nextRand()
		}
		c.writeRawVec(vd, n, bits)
		return 1
	case 0x22, 0x23: // vrndf1 in [1, 2), vrndf2 in [2, 4)
		var out [4]float64
		for i := 0; i < n; i++ {
			u := c.randUnit()
			if f == 0x22 {
				out[i] = 1 + u
			} else {
				out[i] = 2 + 2*u
			}
		}
		c.writeVD(vd, n, out)
		return 1
	case 0x32: // vf2h
		return c.execVF2H(vd, vs, n)
	case 0x33: // vh2f
		return c.execVH2F(vd, vs, n)
	case 0x3C: // vi2uc, quad only: pack the top byte of each
		// int, clamping negatives to zero, into one single
		if n != 4 {
			return c.vfpuUnimplemented()
		}
		idx := vecIndices(vs, 4)
		var word uint32
		for i := 0; i < 4; i++ {
			raw := c.vregs[idx[i]]
			var b uint32
			if int32(raw) >= 0 {
				b = (raw >> 23) & 0xFF
			}
			word |= b << uint(8*i)
		}
		var bits [4]uint32
		bits[0] = word
		c.writeRawVec(vd, 1, bits)
		return 1
	case 0x3F: // vi2s, pair or quad: pack the top halfword of
		// each int
		if n != 2 && n != 4 {
			return c.vfpuUnimplemented()
		}
		idx := vecIndices(vs, n)
		var bits [4]uint32
		for i := 0; i < n; i++ {
			h := (c.vregs[idx[i]] >> 16) & 0xFFFF
			bits[i/2] |= h << uint(16*(i%2))
		}
		c.writeRawVec(vd, n/2, bits)
		return 1
	}
	if f >= 0x60 { // vcst: constant index is the low 5 bits of F
		idx := f & 0x1F
		// The prefix constant table holds 8 entries; the index
		// is wrapped into it so out of range indices stay safe.
		v := float64(vfpuConstTable[idx&7])
		var out [4]float64
		for i := range out {
			out[i] = v
		}
		c.writeVD(vd, n, out)
		return 1
	}
	return c.vfpuUnimplemented()
}

// vfpuUnaryOp evaluates one element wise unary function.
func vfpuUnaryOp(f uint32, v float64) float64 {
	switch f {
	case 0x00: // vmov
		return v
	case 0x01: // vabs
		return math.Abs(v)
	case 0x02: // vneg
		return -v
	case 0x04: // vsat0
		if math.IsNaN(v) || v < 0 {
			return 0
		}
		if v > 1 {
			return 1
		}
		return v
	case 0x05: // vsat1
		if math.IsNaN(v) {
			return 0
		}
		if v < -1 {
			return -1
		}
		if v > 1 {
			return 1
		}
		return v
	case 0x10: // vrcp
		return 1 / v
	case 0x11: // vrsq
		if v < 0 {
			return math.NaN()
		}
		if v == 0 {
			return math.Inf(1)
		}
		return 1 / math.Sqrt(v)
	case 0x12: // vsin, in units of pi/2
		return math.Sin(v * math.Pi / 2)
	case 0x13: // vcos
		return math.Cos(v * math.Pi / 2)
	case 0x14: // vexp2
		return math.Pow(2, v)
	case 0x15: // vlog2
		return math.Log2(v)
	case 0x16: // vsqrt
		return math.Sqrt(v)
	case 0x17: // vasin, result in units of pi/2
		return math.Asin(v) * 2 / math.Pi
	case 0x18: // vnrcp
		return -1 / v
	case 0x1A: // vnsin
		return -math.Sin(v * math.Pi / 2)
	case 0x1C: // vrexp2
		return math.Pow(2, -v)
	case 0x4A: // vsgn
		if v > 0 {
			return 1
		}
		if v < 0 {
			return -1
		}
		return 0
	}
	return math.NaN()
}

// execVF2I implements the scaled float to int family: the top
// two bits of the 7 bit field select the rounding (0 nearest
// even, 1 toward zero, 2 up as unsigned, 3 down) and the low
// five bits are the scale; the result is round(float * 2^scale).
func (c *CPU) execVF2I(vd, vs uint32, n int, f uint32) int {
	mode := f >> 5
	scale := int(f & 0x1F)
	a := c.readVS(vs, n)
	var bits [4]uint32
	for i := 0; i < n; i++ {
		bits[i] = floatToIntBits(math.Ldexp(a[i], scale), mode)
	}
	c.writeRawVec(vd, n, bits)
	return 1
}

// execVI2FOrCmov implements group 5 of opcode 0x34: vi2f with a
// five bit scale (result is int * 2^-scale), except the field
// range 0x20..0x2F, which is vcmovt (0x20..0x27) and vcmovf
// (0x28..0x2F) with sel = field & 7.
func (c *CPU) execVI2FOrCmov(vd, vs uint32, n int, f uint32) int {
	if f >= 0x20 && f <= 0x2F {
		isF := f >= 0x28
		sel := f & 7
		src := c.readVS(vs, n)
		idx := vecIndices(vd, n)
		var bits [4]uint32
		for i := 0; i < n; i++ {
			cur := fpInput(c.vregs[idx[i]])
			var take bool
			switch {
			case sel <= 3:
				take = c.ccBit(int(sel))
			case sel == 4:
				take = (c.cc>>4)&1 != 0
			case sel == 5:
				take = (c.cc>>5)&1 != 0
			case sel == 6:
				take = c.ccBit(i)
			default: // 7: unspecified in the docs; the landed
				// per lane inverted behavior is kept
				take = !c.ccBit(i)
			}
			if isF {
				take = !take
			}
			v := cur
			if take {
				v = src[i]
			}
			bits[i] = roundToSingle(v, 0)
		}
		// vcmov bypasses the destination prefix: raw write.
		c.writeRawVec(vd, n, bits)
		return 1
	}
	if f > 0x1F {
		return c.vfpuUnimplemented()
	}
	scale := int(f)
	idx := vecIndices(vs, n)
	var vals [4]float64
	for i := 0; i < n; i++ {
		vals[i] = math.Ldexp(float64(int32(c.vregs[idx[i]])), -scale)
	}
	vals = c.applySPFXNow(vals, n)
	c.writeRawFloats(vd, n, vals)
	return 1
}

// ccBit returns VFPU condition bit i.
func (c *CPU) ccBit(i int) bool { return (c.cc>>uint(i))&1 != 0 }

// execMatrixGroup dispatches opcode 0x3C: G=0 vmmul, G=1..3
// the vtfm and vhtfm forms, G=4 vmscl, G=5 vcrsp and vqmul,
// G=7 the matrix fill group selected by F.
func (c *CPU) execMatrixGroup(instr uint32) int {
	vd, vs, vt, sizeEnum, n, g := vvFields(instr)
	switch g {
	case 0: // vmmul
		if n < 2 {
			return c.vfpuUnimplemented()
		}
		am := c.readMatrix(vs^0x20, n)
		bm := c.readMatrix(vt, n)
		var cm [4][4]float64
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				sum := 0.0
				for k := 0; k < n; k++ {
					sum += am[i][k] * bm[k][j]
				}
				cm[i][j] = sum
			}
		}
		c.writeMatrix(vd, n, cm)
		return 1
	case 1, 2, 3: // vtfmN and vhtfmN, matrix size is G+1
		m := int(g) + 1
		mat := c.readMatrix(vs^0x20, m)
		var out [4]float64
		switch {
		case int(sizeEnum) == m-1: // vtfm: full vector operand
			vec := c.paddedFloats(vt, m)
			for i := 0; i < m; i++ {
				sum := 0.0
				for j := 0; j < m; j++ {
					sum += mat[i][j] * vec[j]
				}
				out[i] = sum
			}
		case int(sizeEnum) == m-2: // vhtfm: the last matrix
			// column is weighted by an implicit 1.0
			vec := c.paddedFloats(vt, m-1)
			for i := 0; i < m; i++ {
				sum := 0.0
				for j := 0; j < m-1; j++ {
					sum += mat[i][j] * vec[j]
				}
				out[i] = sum + mat[i][m-1]
			}
		default:
			return c.vfpuUnimplemented()
		}
		c.writeRawFloats(vd, m, out)
		return 1
	case 4: // vmscl
		if n < 2 {
			return c.vfpuUnimplemented()
		}
		am := c.readMatrix(vs^0x20, n)
		s := fpInput(c.vregs[singleIndex(vt)])
		var cm [4][4]float64
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				cm[i][j] = am[i][j] * s
			}
		}
		c.writeMatrix(vd, n, cm)
		return 1
	case 5:
		if sizeEnum == 2 { // vcrsp, triple: full cross product
			a := c.readVS(vs, 3)
			b := c.readVT(vt, 3)
			var out [4]float64
			out[0] = a[1]*b[2] - a[2]*b[1]
			out[1] = a[2]*b[0] - a[0]*b[2]
			out[2] = a[0]*b[1] - a[1]*b[0]
			c.writeVD(vd, 3, out)
			return 1
		}
		if sizeEnum == 3 { // vqmul, quad: quaternion product
			a := c.paddedFloats(vs, 4)
			b := c.paddedFloats(vt, 4)
			var out [4]float64
			out[0] = a[3]*b[0] - a[2]*b[1] + a[1]*b[2] + a[0]*b[3]
			out[1] = a[3]*b[1] + a[2]*b[0] + a[1]*b[3] - a[0]*b[2]
			out[2] = a[3]*b[2] + a[2]*b[3] - a[1]*b[0] + a[0]*b[1]
			out[3] = a[3]*b[3] - a[2]*b[2] - a[1]*b[1] - a[0]*b[0]
			c.writeRawFloats(vd, 4, out)
			return 1
		}
		return c.vfpuUnimplemented()
	case 7:
		f := (instr >> 16) & 0x7F
		switch f {
		case 0: // vmmov: raw matrix copy, no prefix
			if n < 2 {
				return c.vfpuUnimplemented()
			}
			c.writeMatrix(vd, n, c.readMatrix(vs, n))
			return 1
		case 3: // vmidt
			var cm [4][4]float64
			for i := 0; i < n; i++ {
				cm[i][i] = 1
			}
			c.writeMatrix(vd, n, cm)
			return 1
		case 6: // vmzero
			var cm [4][4]float64
			c.writeMatrix(vd, n, cm)
			return 1
		case 7: // vmone
			var cm [4][4]float64
			for i := 0; i < n; i++ {
				for j := 0; j < n; j++ {
					cm[i][j] = 1
				}
			}
			c.writeMatrix(vd, n, cm)
			return 1
		}
		return c.vfpuUnimplemented()
	}
	return c.vfpuUnimplemented()
}

// execPrefixes dispatches opcode 0x37, the standalone prefix
// instructions: bits 25:24 select source (0), target (1) or
// destination (2); the rest of the word is the immediate.
func (c *CPU) execPrefixes(instr uint32) int {
	switch (instr >> 24) & 3 {
	case 0: // vpfxs
		c.spfxImm = instr & 0x7FFFF
		c.spfxOn = true
	case 1: // vpfxt
		c.tpfxImm = instr & 0x7FFFF
		c.tpfxOn = true
	case 2: // vpfxd
		c.dpfxImm = instr & 0xFFF
		c.dpfxOn = true
	default:
		return c.vfpuUnimplemented()
	}
	return 1
}

// floatToIntBits converts a float value to integer bits for
// the vf2i family. mode selects the rounding: 0 nearest even
// (signed), 1 toward zero (signed), 2 up (unsigned), 3 down
// (signed). NaN converts to 0 and out of range values clamp.
func floatToIntBits(v float64, mode uint32) uint32 {
	if math.IsNaN(v) {
		return 0
	}
	var r float64
	switch mode {
	case 0:
		r = math.RoundToEven(v)
	case 1:
		r = math.Trunc(v)
	case 2:
		r = math.Ceil(v)
	case 3:
		r = math.Floor(v)
	}
	if mode == 2 { // unsigned
		if r <= 0 {
			return 0
		}
		if r >= 4294967296.0 {
			return 0xFFFFFFFF
		}
		return uint32(r)
	}
	if r >= 2147483648.0 {
		return 0x7FFFFFFF
	}
	if r < -2147483648.0 {
		return 0x80000000
	}
	return uint32(int32(r))
}

// execVF2H converts floats to packed half floats.
func (c *CPU) execVF2H(vd, vs uint32, n int) int {
	a := c.readVS(vs, n)
	var halves [4]uint32
	for i := 0; i < 4; i++ {
		var fv float32
		if i < n {
			fv = float32(a[i])
			// Preserve a NaN or infinity that came from the exact
			// source bits rather than a float64 round trip.
			if math.IsNaN(a[i]) {
				fv = math.Float32frombits(canonicalNaN)
			}
		}
		halves[i] = uint32(floatToHalf(fv))
	}
	var bits [4]uint32
	switch n {
	case 1:
		bits[0] = halves[0]
		c.writeRawVec(vd, 1, bits)
	case 2:
		bits[0] = halves[0] | halves[1]<<16
		c.writeRawVec(vd, 1, bits)
	case 3:
		bits[0] = halves[0] | halves[1]<<16
		bits[1] = halves[2]
		c.writeRawVec(vd, 2, bits)
	default: // 4
		bits[0] = halves[0] | halves[1]<<16
		bits[1] = halves[2] | halves[3]<<16
		c.writeRawVec(vd, 2, bits)
	}
	return 1
}

// execVH2F converts packed half floats to floats. The size bits
// give the destination size; the source holds half as many singles.
func (c *CPU) execVH2F(vd, vs uint32, n int) int {
	var halves [4]uint16
	switch n {
	case 1:
		w := c.vregs[singleIndex(vs)]
		halves[0] = uint16(w)
	case 2:
		w := c.vregs[singleIndex(vs)]
		halves[0] = uint16(w)
		halves[1] = uint16(w >> 16)
	default: // 3 or 4: source is a pair
		idx := vecIndices(vs, 2)
		w0 := c.vregs[idx[0]]
		w1 := c.vregs[idx[1]]
		halves[0] = uint16(w0)
		halves[1] = uint16(w0 >> 16)
		halves[2] = uint16(w1)
		halves[3] = uint16(w1 >> 16)
	}
	var vals [4]float64
	for i := 0; i < n; i++ {
		vals[i] = float64(halfToFloat(halves[i]))
	}
	vals = c.applySPFXNow(vals, n)
	c.writeRawFloats(vd, n, vals)
	return 1
}

// floatToHalf converts a float32 to IEEE 754 half bits with full
// denormal, infinity and NaN handling. A NaN becomes 0x7E00 with
// its sign preserved.
func floatToHalf(f float32) uint16 {
	bits := math.Float32bits(f)
	sign := uint16((bits >> 16) & 0x8000)
	rawExp := int((bits >> 23) & 0xFF)
	exp := rawExp - 112 // the half exponent field for a normal result
	man := bits & 0x7FFFFF
	if rawExp == 0xFF {
		if man != 0 {
			return sign | 0x7E00 // NaN
		}
		return sign | 0x7C00 // infinity
	}
	if exp >= 31 {
		return sign | 0x7C00 // overflow to infinity
	}
	if exp <= 0 {
		if exp < -10 {
			return sign // too small: signed zero
		}
		// Denormal half: add the implicit leading bit and shift.
		man |= 0x800000
		shift := uint(14 - exp)
		// Round to nearest even on the shifted out bits.
		half := man >> shift
		rem := man & ((uint32(1) << shift) - 1)
		mid := uint32(1) << (shift - 1)
		if rem > mid || (rem == mid && half&1 != 0) {
			half++
		}
		return sign | uint16(half)
	}
	// Normal half: round to nearest even on the dropped 13 bits.
	half := uint32(exp)<<10 | (man >> 13)
	rem := man & 0x1FFF
	if rem > 0x1000 || (rem == 0x1000 && half&1 != 0) {
		half++
	}
	return sign | uint16(half)
}

// halfToFloat converts IEEE 754 half bits to a float32. The result
// is exact: every half value is representable in single precision.
func halfToFloat(h uint16) float32 {
	sign := uint32(h&0x8000) << 16
	exp := (h >> 10) & 0x1F
	man := uint32(h & 0x3FF)
	switch {
	case exp == 0:
		if man == 0 {
			return math.Float32frombits(sign)
		}
		// Denormal half: normalize.
		v := float64(man) * math.Pow(2, -24)
		if sign != 0 {
			v = -v
		}
		return float32(v)
	case exp == 31:
		if man == 0 {
			return math.Float32frombits(sign | 0x7F800000)
		}
		return math.Float32frombits(sign | 0x7F800000 | man<<13)
	default:
		return math.Float32frombits(sign | uint32(int(exp)-15+127)<<23 | man<<13)
	}
}

// execVCMP implements the compare of opcode 0x1B group 0: the
// condition is the low five bits of the word (bits 6:5 are
// zero), and the operands sit in the VV positions. Prefixes do
// apply to its source reads.
func (c *CPU) execVCMP(instr uint32) int {
	cond := instr & 0x1F
	if cond > 15 || instr&0x60 != 0 {
		return c.vfpuUnimplemented()
	}
	_, vs, vt, _, n, _ := vvFields(instr)
	a := c.readVS(vs, n)
	b := c.readVT(vt, n)
	var cc uint32
	all := true
	any := false
	for i := 0; i < n; i++ {
		r := evalVCond(cond, a[i], b[i])
		if r {
			cc |= 1 << uint(i)
			any = true
		} else {
			all = false
		}
	}
	if any {
		cc |= 1 << 4
	}
	if all {
		cc |= 1 << 5
	}
	c.cc = cc
	return 1
}

// evalVCond evaluates one VCMP condition code on converted
// values.
func evalVCond(cond uint32, a, b float64) bool {
	switch cond {
	case 0: // FL
		return false
	case 1: // EQ
		return a == b
	case 2: // LT
		return a < b
	case 3: // LE
		return a <= b
	case 4: // TR
		return true
	case 5: // NE
		return a != b
	case 6: // GE
		return a >= b
	case 7: // GT
		return a > b
	case 8: // EZ
		return a == 0
	case 9: // EN
		return math.IsNaN(a)
	case 10: // EI
		return math.IsInf(a, 0)
	case 11: // ES
		return math.IsInf(a, 0) || math.IsNaN(a)
	case 12: // NZ
		return a != 0
	case 13: // NN
		return !math.IsNaN(a)
	case 14: // NI
		return !math.IsInf(a, 0)
	case 15: // NS
		return !math.IsNaN(a) && !math.IsInf(a, 0)
	}
	return false
}

// vfpuAddr computes a VFPU load or store address: the 14 bit field
// at bits 15:2 is a word offset, sign extended then shifted by 2.
func (c *CPU) vfpuAddr(instr uint32) uint32 {
	rs := int((instr >> 21) & 31)
	v := int32((instr >> 2) & 0x3FFF)
	if v&0x2000 != 0 {
		v -= 0x4000
	}
	return c.regs[rs] + uint32(v<<2)
}

// execLVS implements LV.S (opcode 0x32).
func (c *CPU) execLVS(instr uint32) int {
	addr := c.vfpuAddr(instr)
	if addr&3 != 0 {
		return c.addrError(excAdEL, addr)
	}
	reg7 := ((instr>>16)&31)<<2 | (instr & 3)
	c.vregs[singleIndex(reg7)] = c.bus.Read32(addr)
	return 1
}

// execSVS implements SV.S (opcode 0x3A).
func (c *CPU) execSVS(instr uint32) int {
	addr := c.vfpuAddr(instr)
	if addr&3 != 0 {
		return c.addrError(excAdES, addr)
	}
	reg7 := ((instr>>16)&31)<<2 | (instr & 3)
	c.bus.Write32(addr, c.vregs[singleIndex(reg7)])
	return 1
}

// quadByte returns byte k (0..15) of a quad operand.
func (c *CPU) quadByte(idx [4]int, k int) uint8 {
	w := c.vregs[idx[k/4]]
	return uint8(w >> uint(8*(k%4)))
}

// setQuadByte sets byte k (0..15) of a quad operand.
func (c *CPU) setQuadByte(idx [4]int, k int, b uint8) {
	pos := idx[k/4]
	shift := uint(8 * (k % 4))
	w := c.vregs[pos]
	w &^= 0xFF << shift
	w |= uint32(b) << shift
	c.vregs[pos] = w
}

// execLVQ implements the LV.Q family (opcode 0x36): LV.Q, LVL.Q
// and LVR.Q by the low two bits.
func (c *CPU) execLVQ(instr uint32) int {
	sub := instr & 3
	if sub == 1 {
		return c.vfpuUnimplemented()
	}
	addr := c.vfpuAddr(instr)
	reg7q := ((instr >> 16) & 31) << 2
	idx := vecIndices(reg7q, 4)
	switch sub {
	case 0: // LV.Q
		if addr&15 != 0 {
			return c.addrError(excAdEL, addr)
		}
		for i := 0; i < 16; i++ {
			c.setQuadByte(idx, i, c.bus.Read8(addr+uint32(i)))
		}
	case 2: // LVL.Q
		a := int(addr & 15)
		for i := 0; i < 16-a; i++ {
			c.setQuadByte(idx, i, c.bus.Read8(addr+uint32(i)))
		}
	case 3: // LVR.Q
		a := int(addr & 15)
		base := addr - uint32(a)
		for i := 0; i <= a; i++ {
			c.setQuadByte(idx, 15-a+i, c.bus.Read8(base+uint32(i)))
		}
	}
	return 1
}

// execSVQ implements the SV.Q family (opcode 0x3E): SV.Q, SVL.Q
// and SVR.Q by the low two bits.
func (c *CPU) execSVQ(instr uint32) int {
	sub := instr & 3
	if sub == 1 {
		return c.vfpuUnimplemented()
	}
	addr := c.vfpuAddr(instr)
	reg7q := ((instr >> 16) & 31) << 2
	idx := vecIndices(reg7q, 4)
	switch sub {
	case 0: // SV.Q
		if addr&15 != 0 {
			return c.addrError(excAdES, addr)
		}
		for i := 0; i < 16; i++ {
			c.bus.Write8(addr+uint32(i), c.quadByte(idx, i))
		}
	case 2: // SVL.Q
		a := int(addr & 15)
		for i := 0; i < 16-a; i++ {
			c.bus.Write8(addr+uint32(i), c.quadByte(idx, i))
		}
	case 3: // SVR.Q
		a := int(addr & 15)
		base := addr - uint32(a)
		for i := 0; i <= a; i++ {
			c.bus.Write8(base+uint32(i), c.quadByte(idx, 15-a+i))
		}
	}
	return 1
}
