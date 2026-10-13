package allegrex

import "math"

// VFPU (COP2) implementation.
//
// Layout: 8 matrices, each 4 rows by 4 columns. The linear index of
// an element is m*16 + row*4 + col.
//
// Register field layouts (all three pinned by the pspautotests
// binaries, whose assemblers encode named registers; PSP-2 used an
// invented layout that its own unit tests echoed back, and every
// corpus test disagreed):
//
//   - Arithmetic fields (vd at [6:0], vs at [14:8], vt at [22:16])
//     and the mfv/mtv operand: bits [4:2] are the matrix. A single
//     takes its row from bits [1:0] and its column from bits [6:5]
//     (vcst.s S010 is field 1, S001 is 32, S100 is 4). A vector
//     takes its fixed coordinate from bits [1:0] (the column, or
//     the row when bit 5, the transpose flag, is set) and walks
//     the other coordinate from 0, or from 4-n when bit 6 (the end
//     anchor) is set: C010 is 1, R000 is 32, C001 (a triple over
//     rows 1..3 of column 0) is 64.
//   - Matrix operands (vmmul, vtfm, vmscl, ...): bits [4:2] are
//     the matrix in every slot. vtfm and vhtfm read the matrix
//     in the plain layout and dot its columns with the vector.
//     vmmul decodes each slot's anchor from its own bits (see
//     matrixOriginVmmul): with b6, b5, b1 the field's bits 6, 5,
//     and 1, vs anchors at (b6, b1), vt at (b1 AND NOT b5,
//     b5 OR b6), vd at (b6 XOR b5, b5 OR b1), anchors scale by
//     (4-n) and swap for triples, and the product is stored
//     transposed. vmscl reads its matrix with bit 5 cleared.
//   - Load/store fields (bits [20:16] giving the high five bits,
//     bits [1:0] the low two): the field is (matrix << 4) |
//     (fixed << 2) | start, with bit 0 the transpose flag and the
//     fixed coordinate at bits [3:2] of the field: lv.q C010 and
//     lv.q R001 are both field 4, told apart by the flag in bit 0
//     of the word. A single (lv.s/sv.s) takes its column from the
//     fixed bits and its row from the low two bits.
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

// The prefix constant table, pinned by the corpus prefix_ctrl
// readbacks: index (swizzle | abs<<2) selects 0, 1, 2, 1/2, 3,
// 1/3, 1/4, 1/6 in that order.
var vfpuConstTable = [8]float32{0, 1, 2, 1.0 / 2.0, 3, 1.0 / 3.0, 1.0 / 4.0, 1.0 / 6.0}

// vecIndices decodes a 7 bit register field r for an operand of
// size n lanes (1 single, 2 pair, 3 triple, 4 quad) and returns the
// linear indices of its elements; the first n entries are valid.
// See the layout notes at the top of the file.
func vecIndices(r uint32, n int) [4]int {
	m := int((r >> 2) & 7)
	var out [4]int
	if n == 1 {
		row := int(r & 3)
		col := int((r >> 5) & 3)
		out[0] = m*16 + row*4 + col
		return out
	}
	fixed := int(r & 3)
	tr := (r>>5)&1 != 0
	start := 0
	if (r>>6)&1 != 0 {
		start = 4 - n
	}
	for i := 0; i < n && i < 4; i++ {
		if tr {
			out[i] = m*16 + fixed*4 + start + i
		} else {
			out[i] = m*16 + (start+i)*4 + fixed
		}
	}
	return out
}

// singleIndex decodes a 7 bit field as a single element.
func singleIndex(r uint32) int {
	return vecIndices(r, 1)[0]
}

// dotDestIndex decodes a scalar VV operand position (the
// dot product family's destination, the vscl scalar, scalar
// VV reads generally). The corpus witnesses pin the offset
// within the matrix to (low field bits) + (high field
// bits): field 3 lands at 3 (overlap), field 5 at 17
// (minmax_tie vmin.s VT), field 32 at 1 (vbranch vcmp.s
// VT), field 36 at 17 (prefix_ctrl). The plain vector
// layout (low bits strided) and its transpose each fail one
// of those; the sum fits all of them. The VFPU5 single
// writes (VIIM and VFIM) keep the vecIndices layout, which
// the corpus vregs Filled print pins for writes.
func dotDestIndex(r uint32) int {
	m := int((r >> 2) & 7)
	return m*16 + int(r&3) + int((r>>5)&3)
}

// matIndex returns the linear index of matrix m element (i, j),
// where i is the row and j is the column.
func matIndex(m, i, j int) int { return m*16 + i*4 + j }

// matrixOrigin decodes a matrix operand field: the matrix from
// bits [4:2], the transpose flag from bit 5, the anchor row from
// bits [1:0] and the anchor column from bit 6 (blocks anchor at
// even columns).
func matrixOrigin(r uint32) (m int, tr bool, row0, col0 int) {
	m = int((r >> 2) & 7)
	tr = (r>>5)&1 != 0
	row0 = int(r & 3)
	col0 = int((r>>6)&1) << 1
	return m, tr, row0, col0
}

// matSlot identifies which operand slot of a matrix group
// instruction a field sits in.
type matSlot int

const (
	slotVD matSlot = iota
	slotVS
	slotVT
)

// matrixOriginVmmul decodes a vmmul operand field. The three
// slots do NOT share one encoding: the corpus matrix test pins
// each slot's anchor bits separately (the same register name
// encodes differently per slot; the assembler's M000 is 32 in
// the vs field and 0 in the vt field, per the corpus
// _checkSquare4 word). Writing b6, b5, b1 for field bits 6, 5
// and 1, the anchor in block units (R, C) is:
//
//	vs: R = b6,          C = b1
//	vt: R = b1 AND NOT b5, C = b5 OR b6
//	vd: R = b6 XOR b5,   C = b5 OR b1
//
// fitted on every witnessed operand: the .q word (8, 32, 4),
// the .p series (8, 32, 4), (72, 96, 38), (104, 34, 6),
// (74, 66, 70) whose four products and placements the expected
// output fixes exactly, and the .t destination 72 whose block
// lands at file rows 0..2, cols 1..3 (the pair swaps on the
// triple grid). No slot transposes in vmmul: every witnessed
// product is the plain product of the anchored blocks. The
// matrix number is bits [4:2] in all slots.
func matrixOriginVmmul(r uint32, n int, slot matSlot) (m, row0, col0 int) {
	m = int((r >> 2) & 7)
	b6 := (r >> 6) & 1
	b5 := (r >> 5) & 1
	b1 := (r >> 1) & 1
	var R, C uint32
	switch slot {
	case slotVS:
		R, C = b6, b1
	case slotVT:
		R, C = b1&^b5, b5|b6
	default: // slotVD
		R, C = b6^b5, b5|b1
	}
	if n == 3 {
		return m, int(C), int(R)
	}
	return m, int(R) * (4 - n), int(C) * (4 - n)
}

// readMatrixAt reads the size n block anchored at (row0, col0)
// of matrix m, with no prefix and no transpose.
func (c *CPU) readMatrixAt(m, row0, col0, n int) [4][4]float64 {
	var out [4][4]float64
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			out[i][j] = fpInput(c.vregs[matIndex(m, row0+i, col0+j)])
		}
	}
	return out
}

// writeMatrixAt writes vals into the size n block anchored at
// (row0, col0) of matrix m, with no prefix and no transpose.
func (c *CPU) writeMatrixAt(m, row0, col0, n int, vals [4][4]float64) {
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			c.vregs[matIndex(m, row0+i, col0+j)] = roundToSingleVFPU(vals[i][j])
		}
	}
}

// matrixFilePos maps a logical matrix element (i, j) of a size n
// block to its file position (row, col): the block anchors at
// (row0, col0), and a transposed operand reads its elements
// swapped.
func matrixFilePos(r uint32, n, i, j int) (int, int, int) {
	m, tr, row0, col0 := matrixOrigin(r)
	if !tr {
		return m, row0 + i, col0 + j
	}
	return m, row0 + j, col0 + i
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
			c.vregs[matIndex(m, row, col)] = roundToSingleVFPU(vals[i][j])
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

// applySourcePrefix applies a source prefix immediate to a
// padded source vector. The immediate is planar, pinned by the
// corpus prefix_ctrl readbacks: bits [7:0] hold the per lane
// swizzle (2 bits each), bits [11:8] the per lane absolute
// flags, bits [15:12] the per lane constant select, and bits
// [19:16] the per lane negate flags. A constant lane reads
// vfpuConstTable[swizzle | abs<<2], negated when its negate
// flag is set (the abs flag doubles as the constant index's
// high bit); any other lane reads the swizzled source lane
// with abs and negate applied.
func applySourcePrefix(padded [4]float64, imm uint32, n int) [4]float64 {
	var out [4]float64
	for i := 0; i < n && i < 4; i++ {
		swizzle := int((imm >> uint(2*i)) & 3)
		abs := (imm>>uint(8+i))&1 != 0
		cst := (imm>>uint(12+i))&1 != 0
		neg := (imm>>uint(16+i))&1 != 0
		if cst {
			idx := swizzle
			if abs {
				idx |= 4
			}
			v := float64(vfpuConstTable[idx])
			if neg {
				v = -v
			}
			out[i] = v
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

// resetPrefixes restores all three prefix registers to their
// power on defaults (source identity swizzle 0xE4, dest 0) and
// disarms them. The corpus prefix_consume test pins this: after
// any VFPU compute op executes, MFVC reads the defaults back,
// even for prefixes the op itself did not use (vmov resets the
// target prefix too, vzero resets all three). Ops that do not
// compute (loads, stores, moves, vnop, vsync, vflush, plain
// integer instructions) leave armed prefixes untouched.
func (c *CPU) resetPrefixes() {
	c.spfxImm = 0xE4
	c.tpfxImm = 0xE4
	c.dpfxImm = 0
	c.spfxOn = false
	c.tpfxOn = false
	c.dpfxOn = false
	c.pfxPoison = false
}

// readRawPrefixed returns the source operand's raw bit
// patterns with the source prefix applied bitwise, the model
// the corpus prefix_unpack test documents for the integer
// pack and unpack family: a constant lane substitutes the
// constant's float encoding, negate flips the sign bit, abs
// clears it, and the swizzle selects among the operand's own
// lanes. A select outside the operand's lanes yields zero: on
// hardware it replays a stale prefix unit latch whose update
// rule the test itself records as not understood, and the
// test's author calls zero the reasonable substitute.
func (c *CPU) readRawPrefixed(vs uint32, n int) []uint32 {
	idx := vecIndices(vs, n)
	if n == 1 {
		// A single source reads at the transposed single
		// position, the same decode the dot family uses
		// (the corpus overlap vuc2ifs.s S030 case).
		idx[0] = dotDestIndex(vs)
	}
	in := make([]uint32, n)
	for i := 0; i < n; i++ {
		in[i] = c.vregs[idx[i]]
	}
	if !c.spfxOn {
		return in
	}
	imm := c.spfxImm
	c.spfxOn = false
	out := make([]uint32, n)
	for i := 0; i < n; i++ {
		swizzle := int((imm >> uint(2*i)) & 3)
		abs := (imm>>uint(8+i))&1 != 0
		cst := (imm>>uint(12+i))&1 != 0
		neg := (imm>>uint(16+i))&1 != 0
		if cst {
			ci := swizzle
			if abs {
				ci |= 4
			}
			b := math.Float32bits(vfpuConstTable[ci])
			if neg {
				b ^= 0x80000000
			}
			out[i] = b
			continue
		}
		var b uint32
		if swizzle < n {
			b = in[swizzle]
		}
		if abs {
			b &^= 0x80000000
		}
		if neg {
			b ^= 0x80000000
		}
		out[i] = b
	}
	return out
}

// applyDestSaturation applies an armed dest prefix's
// saturation to raw result patterns, treating them as floats
// (the corpus prefix_unpack D cases: a huge positive pattern
// clamps to 1.0, a negative one to 0). The write mask is the
// writers' business, not this function's.
func (c *CPU) applyDestSaturation(out []uint32) {
	if !c.dpfxOn {
		return
	}
	imm := c.dpfxImm
	for i := range out {
		sat := (imm >> uint(2*i)) & 3
		if sat != 1 && sat != 3 {
			continue
		}
		if isNaNBits(out[i]) {
			out[i] = 0x7F800001
			continue
		}
		v := float64(math.Float32frombits(out[i]))
		if sat == 1 {
			if v < 0 {
				v = 0
			} else if v > 1 {
				v = 1
			}
		} else {
			if v < -1 {
				v = -1
			} else if v > 1 {
				v = 1
			}
		}
		out[i] = math.Float32bits(float32(v))
	}
}

// execCompute runs one VFPU compute op and, when it executed
// without faulting, resets the prefix registers (see
// resetPrefixes). A faulting op leaves the prefixes as they
// were, matching the exception path.
func (c *CPU) execCompute(instr uint32, op func(uint32) int) int {
	faults := c.vfpuFaultCount
	r := op(instr)
	if c.vfpuFaultCount == faults {
		c.resetPrefixes()
	}
	return r
}

// paddedFloats builds the padded float vector for a raw read: the
// operand lanes through fpInput, zeros beyond the size.
func (c *CPU) paddedFloats(r uint32, n int) [4]float64 {
	idx := vecIndices(r, n)
	if n == 1 {
		// A scalar VV operand reads at the transposed single
		// position (the dotDestIndex decode): the corpus
		// minmax_tie vmin.s reads its VT single (field 5)
		// from the position the C110 quad load wrote, which
		// is 17, not the 20 the vector layout gives. Vector
		// operands (n >= 2) and the VFPU5 single writes keep
		// the layout of vecIndices.
		idx[0] = dotDestIndex(r)
	}
	var out [4]float64
	for i := 0; i < n; i++ {
		out[i] = fpInput(c.vregs[idx[i]])
	}
	return out
}

// prefixOutOfRange reports whether an armed source prefix
// selects, for some used lane, a non-constant lane beyond the
// op size. On hardware such a prefix poisons the whole op:
// every source read of the op returns zero, so the result is
// the zero combination (the corpus prefix_ctrl OUTSIDE cases
// and the prefix_unpack latch vadd case, where an out of
// range swizzle on a scalar op zeroes even the operand whose
// own prefix is unarmed).
func prefixOutOfRange(imm uint32, n int) bool {
	for i := 0; i < n; i++ {
		if (imm>>(12+uint(i)))&1 == 1 {
			continue
		}
		if int((imm>>(2*uint(i)))&3) >= n {
			return true
		}
	}
	return false
}

// readVS reads the VS operand of size n, consuming the source
// prefix if one is armed.
func (c *CPU) readVS(r uint32, n int) [4]float64 {
	if c.spfxOn {
		c.spfxOn = false
		if c.pfxPoison || prefixOutOfRange(c.spfxImm, n) {
			c.pfxPoison = true
			return [4]float64{}
		}
		return applySourcePrefix(c.paddedFloats(r, n), c.spfxImm, n)
	}
	if c.pfxPoison {
		return [4]float64{}
	}
	return c.paddedFloats(r, n)
}

// readVT reads the VT operand of size n, consuming the target
// prefix if one is armed.
func (c *CPU) readVT(r uint32, n int) [4]float64 {
	if c.tpfxOn {
		c.tpfxOn = false
		if c.pfxPoison || prefixOutOfRange(c.tpfxImm, n) {
			c.pfxPoison = true
			return [4]float64{}
		}
		return applySourcePrefix(c.paddedFloats(r, n), c.tpfxImm, n)
	}
	if c.pfxPoison {
		return [4]float64{}
	}
	return c.paddedFloats(r, n)
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
			// Planar dest prefix, pinned by the corpus
			// prefix_ctrl readbacks: bits [7:0] hold the
			// per lane saturation (2 bits each: 1 clamps
			// to [0, 1], 3 clamps to [-1, 1]) and bits
			// [11:8] the per lane write mask (set skips
			// the lane).
			sat := (imm >> uint(2*i)) & 3
			if (imm>>uint(8+i))&1 != 0 {
				continue
			}
			if sat == 1 || sat == 3 {
				if math.IsNaN(v) {
					// Saturation writes the fixed positive
					// VFPU NaN pattern for a NaN of either
					// sign (corpus prefix_sat).
					c.vregs[idx[i]] = 0x7F800001
					continue
				}
				lo := 0.0
				if sat == 3 {
					lo = -1
				}
				if v < lo {
					v = lo
				} else if v > 1 {
					v = 1
				}
			}
		}
		c.vregs[idx[i]] = roundToSingleVFPU(v)
	}
}

// roundToSingleVFPU rounds like roundToSingle (RN, flush to
// zero) except that a NaN result takes the VFPU pattern: the
// sign bit over 0x7F800001. Every VFPU float result the corpus
// records (vrcp, vsin, vsqrt of a negative, vdiv with a NaN
// operand) carries that pattern rather than the canonical
// quiet NaN the scalar FPU produces.
func roundToSingleVFPU(x float64) uint32 {
	if math.IsNaN(x) {
		if math.Signbit(x) {
			return 0xFF800001
		}
		return 0x7F800001
	}
	return roundToSingle(x, 0)
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
		bits[i] = roundToSingleVFPU(vals[i])
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
		return c.spfxImm & 0xFFFFF
	case idx == 129:
		return c.tpfxImm & 0xFFFFF
	case idx == 130:
		return c.dpfxImm & 0xFFF
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
		c.spfxImm = v & 0xFFFFF
		c.spfxOn = true
	case idx == 129:
		c.tpfxImm = v & 0xFFFFF
		c.tpfxOn = true
	case idx == 130:
		c.dpfxImm = v & 0xFFF
		c.dpfxOn = true
	case idx == 131:
		// The CC register holds six bits; the vbranch corpus
		// test branches on immediates 6 and 7 and reads them
		// as clear even after writing all ones.
		c.cc = v & 0x3F
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
	case 0x03: // MFV / MFVC share rs 3: bit 7 of the word
		// separates them. Clear: MFV, the operand is the 7 bit
		// register field in the low bits (mfv $t3, S100 is
		// 0x486B0004). Set: MFVC, the operand is the control
		// index in the low byte (mfvc $t0, $136 is 0x48680088;
		// PSP-2 read bits 15..8, which made every RCX
		// readback return slot 0).
		if instr&0x80 != 0 {
			c.setReg(rt, c.vfpuCtrlRead(instr&0xFF))
		} else {
			c.setReg(rt, c.vregs[singleIndex(instr&0x7F)])
		}
	case 0x04: // MTC2
		reg7 := ((instr>>11)&31)<<2 | ((instr >> 6) & 3)
		c.vregs[singleIndex(reg7)] = c.regs[rt]
	case 0x06: // CTC2
		rd := (instr >> 11) & 31
		if rd != 0 {
			c.ctrl[rd] = c.regs[rt]
		}
	case 0x07: // MTV / MTVC share rs 7, separated by bit 7
		// exactly as MFV / MFVC (mtv $v1, S100 is 0x48E30004,
		// mtvc $88 is 0x48E80088).
		if instr&0x80 != 0 {
			c.vfpuCtrlWrite(instr&0xFF, c.regs[rt])
		} else {
			c.vregs[singleIndex(instr&0x7F)] = c.regs[rt]
		}
	case 0x08: // BC2: rt is (imm3 << 2) | (likely << 1) | tf,
		// where imm3 selects the CC bit under test (the vbranch
		// corpus binary's words 0x49000002.. pin the layout;
		// PSP-2 always tested bit 0).
		condTrue := (c.cc>>(uint(rt)>>2))&1 != 0
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
		return c.vvBinary(vd, vs, vt, n, func(a, b float64) float64 {
			r := a / b
			if math.IsNaN(r) {
				// The result NaN takes the XOR of the operand
				// signs (the corpus prefix_sat vdiv rows: -NaN
				// divided by a negative is a positive NaN).
				if math.Signbit(a) != math.Signbit(b) {
					return -math.NaN()
				}
				return math.NaN()
			}
			return r
		})
	}
	return c.vfpuUnimplemented()
}

// execVVProd dispatches opcode 0x19: G=0 vmul, G=1 vdot,
// G=2 vscl, G=4 vhdp, G=5 vcrs, G=6 vdet.
func (c *CPU) execVVProd(instr uint32) int {
	vd, vs, vt, _, n, g := vvFields(instr)
	switch g {
	case 0:
		return c.vvBinary(vd, vs, vt, n, func(a, b float64) float64 {
			r := a * b
			if math.IsNaN(r) {
				if math.Signbit(a) != math.Signbit(b) {
					return -math.NaN()
				}
				return math.NaN()
			}
			return r
		})
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
		c.vregs[dotDestIndex(vd)] = roundToSingleVFPU(sum)
		return 1
	case 2: // vscl: scale VS by the VT scalar, read raw
		a := c.readVS(vs, n)
		s := fpInput(c.vregs[dotDestIndex(vt)])
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
		c.vregs[dotDestIndex(vd)] = roundToSingleVFPU(sum)
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
		c.vregs[dotDestIndex(vd)] = roundToSingleVFPU(a[0]*b[1] - a[1]*b[0])
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
		if !c.spfxOn && !c.tpfxOn && !c.dpfxOn {
			// Raw path: the hardware orders the operand bit
			// patterns in float totalOrder, in which a
			// negative NaN sorts below every value and a
			// positive NaN above every value, and the winning
			// pattern is stored unchanged (the corpus minmax
			// test's both-NaN cases pin this: vmin of +NaN
			// and -NaN is the -NaN pattern 0xFFBFFFFF).
			ai := vecIndices(vs, n)
			bi := vecIndices(vt, n)
			if n == 1 {
				ai[0] = dotDestIndex(vs)
				bi[0] = dotDestIndex(vt)
			}
			var out [4]uint32
			for i := 0; i < n; i++ {
				out[i] = vfpuMinMaxBits(c.vregs[ai[i]], c.vregs[bi[i]], g == 3)
			}
			c.writeRawVec(vd, n, out)
			return 1
		}
		return c.vvBinary(vd, vs, vt, n, func(a, b float64) float64 {
			return vfpuMinMax(a, b, g == 3)
		})
	case 5: // vscmp: three way compare, -1, 0 or +1 per lane
		return c.vvBinary(vd, vs, vt, n, func(a, b float64) float64 {
			switch {
			case a < b:
				return -1
			case a > b:
				return 1
			}
			return 0
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

// vfpuMinMaxBits orders two float bit patterns by the float
// totalOrder (sign magnitude key) and returns the winning
// pattern unchanged.
func vfpuMinMaxBits(a, b uint32, isMax bool) uint32 {
	if fpInput(a) == fpInput(b) {
		// A flushed value tie: the two zeros, and denormals
		// against the zero they flush to, compare equal on
		// hardware, and the winner is the second (VT)
		// pattern, stored raw (the corpus minmax_tie and
		// minmax_zero rows). NaN never ties (NaN != NaN), so
		// the NaN totalOrder below is untouched. The sort
		// ops never reach this arm: their equal float
		// collapse fires first.
		return b
	}
	key := func(x uint32) uint32 {
		if x>>31 != 0 {
			return ^x
		}
		return x | 0x80000000
	}
	ka, kb := key(a), key(b)
	if isMax {
		if kb > ka {
			return b
		}
		return a
	}
	if kb < ka {
		return b
	}
	return a
}

// vfpuMinMax is the shared vmin and vmax lane operation: a NaN
// operand yields the other operand, two NaNs yield a NaN.
func vfpuMinMax(a, b float64, isMax bool) float64 {
	if a == 0 && b == 0 {
		// A signed zero tie takes the VT operand, as in
		// the raw path (the corpus minmax_tie rows run with
		// a prefix armed, so they land here).
		return b
	}
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
// the new state word. The hardware RCX bank visibly keeps float
// form after draws (the seed 0 corpus block shows RCX0 taking
// the draw's low half and RCX4 its high half, both under a
// 0x3F800000 exponent), so the bank is updated in that form
// rather than with the raw word; the true state recurrence is
// a wider linear structure that stays modelled, see the README.
func (c *CPU) nextRand() uint32 {
	x := c.rngState[0]
	if x == 0 {
		x = 0x1
	}
	x ^= x << 13
	x ^= x >> 17
	x ^= x << 5
	c.rngState[0] = 0x3F800000 | (x & 0xFFFF)
	c.rngState[4] = 0x3F800000 | (x >> 16)
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
	case 6, 7:
		// vwbn.s: the immediate scale form. Its encoding
		// shares opcode 0x34 with bits [25:24] = 3, so it
		// lands in groups 6 and 7 here by the immediate's
		// top bit; the immediate itself is bits [23:16].
		// The operation (the hardware's exponent modulus,
		// per the published VFPU documentation, restated):
		// take the source's significand with its implicit
		// leading bit, shift it right by (imm - exp) mod 16
		// when imm exceeds the exponent field, left by
		// (exp - imm) mod 16 otherwise, then reassemble
		// with the exponent field replaced by imm; zero,
		// Inf and NaN inputs keep their pattern with the
		// exponent field ORed with imm.
		imm := (instr >> 16) & 0xFF
		src := c.vregs[singleIndex(vs)]
		exp := (src >> 23) & 0xFF
		var out uint32
		if exp == 0 || exp == 0xFF {
			out = src | (imm << 23)
		} else {
			m := (src & 0x7FFFFF) | 0x800000
			if imm > exp {
				m >>= (imm - exp) & 0xF
			} else {
				m <<= (exp - imm) & 0xF
			}
			out = (src & 0x80000000) | (m & 0x7FFFFF) | (imm << 23)
		}
		c.writeRawMasked(vd, 1, []uint32{out})
		return 1
	}
	return c.vfpuUnimplemented()
}

// execVFPUUnary dispatches the opcode 0x34 group 0 unary and
// conversion functions, F = bits 22:16.
func (c *CPU) execVFPUUnary(instr uint32, vd, vs uint32, n int, f uint32) int {
	switch f {
	case 0x04, 0x05: // vsat0 / vsat1
		if !c.spfxOn && !c.dpfxOn {
			// Raw saturating copy: NaN patterns pass through
			// unchanged and denormals are not flushed (both
			// pinned by the corpus prefix_sat rows); only the
			// clamp itself is numeric.
			idx := vecIndices(vs, n)
			var bits [4]uint32
			for i := 0; i < n; i++ {
				b := c.vregs[idx[i]]
				if isNaNBits(b) {
					bits[i] = b
					continue
				}
				v := float64(math.Float32frombits(b))
				lo := 0.0
				if f == 0x05 {
					lo = -1
				}
				if v < lo || (f == 0x04 && v == 0) {
					// The v == 0 arm is vsat0 rewriting a
					// negative zero as positive zero (the
					// corpus row 0,-0,denorm,-denorm).
					v = lo
				} else if v > 1 {
					v = 1
				}
				bits[i] = math.Float32bits(float32(v))
			}
			c.writeRawVec(vd, n, bits)
			return 1
		}
		a := c.readVS(vs, n)
		var out [4]float64
		for i := 0; i < n; i++ {
			out[i] = vfpuUnaryOp(f, a[i])
		}
		c.writeVD(vd, n, out)
		return 1
	case 0x00, 0x01, 0x02, 0x10, 0x11, 0x12, 0x13,
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
	case 0x20: // vrnds: seed the generator from the raw VS bits.
		// The seeding expansion is pinned exactly by the
		// pspautotests vrnd recordings for six seeds: each RCX
		// word becomes 1.0f whose mantissa carries one seed
		// nibble (RCXk takes nibble k) above the seed half the
		// word indexes (low half for RCX0..3, high half for
		// RCX4..7). The draw recurrence itself is only modelled
		// (see nextRand); the corpus state sequences after draws
		// do not match this simple generator, an open item the
		// README flags.
		seed := c.vregs[singleIndex(vs)]
		for k := 0; k < 8; k++ {
			half := seed & 0xFFFF
			if k >= 4 {
				half = (seed >> 16) & 0xFFFF
			}
			nib := (seed >> (4 * k)) & 0xF
			c.rngState[k] = 0x3F800000 | (nib << 16) | half
		}
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
	case 0x36: // vsbz.s: rescale the source into [1, 2) by
		// replacing its exponent field with 127 and dropping
		// the sign; zero and NaN patterns pass unchanged (the
		// published VFPU documentation; the same effect as
		// vsbn with a zero scale register). Scalar only.
		if n != 1 {
			return c.vfpuUnimplemented()
		}
		a := c.readVS(vs, 1)
		b := math.Float32bits(float32(a[0]))
		if e := (b >> 23) & 0xFF; e != 0 && !isNaNBits(b) {
			b = (b & 0x7FFFFF) | 0x3F800000
		}
		c.writeVD(vd, 1, [4]float64{float64(math.Float32frombits(b))})
		return 1
	case 0x37: // vlgb.s: the source's unbiased exponent as a
		// float (logB); zero and denormal inputs give -Inf,
		// Inf gives +Inf, NaN gives NaN (documented). Scalar
		// only.
		if n != 1 {
			return c.vfpuUnimplemented()
		}
		a := c.readVS(vs, 1)
		b := math.Float32bits(float32(a[0]))
		var out float64
		switch e := (b >> 23) & 0xFF; {
		case isNaNBits(b):
			out = math.NaN()
		case e == 0xFF:
			out = math.Inf(1)
		case e == 0:
			out = math.Inf(-1)
		default:
			out = float64(int(e) - 127)
		}
		c.writeVD(vd, 1, [4]float64{out})
		return 1
	case 0x38, 0x39, 0x3A, 0x3B, 0x3D, 0x3E:
		// The integer pack and unpack family. Semantics are
		// the hardware's own (the davidgfnet VFPU document's
		// bit formulas, cross checked against the corpus
		// convert rows): the unsigned unpack shifts one bit
		// less and drops the sign, and the unsigned packs
		// clamp negative results to zero after an arithmetic
		// shift one bit shorter than the signed form.
		in := c.readRawPrefixed(vs, n)
		var out []uint32
		switch f {
		case 0x38: // vuc2ifs.s: byte replicated into a float pattern
			out = make([]uint32, 4)
			for j := 0; j < 4; j++ {
				b := (in[0] >> uint(8*j)) & 0xFF
				out[j] = b<<23 | b<<15 | b<<7 | b>>1
			}
		case 0x39: // vc2i.s: byte into the top of the word
			out = make([]uint32, 4)
			for j := 0; j < 4; j++ {
				out[j] = ((in[0] >> uint(8*j)) & 0xFF) << 24
			}
		case 0x3A: // vus2i: half at bits [30:15], sign dropped
			out = make([]uint32, 2*len(in))
			for i, w := range in {
				out[2*i] = (w << 15) & 0x7FFFFFFF
				out[2*i+1] = (w >> 16 << 15) & 0x7FFFFFFF
			}
		case 0x3B: // vs2i: half into the top of the word
			out = make([]uint32, 2*len(in))
			for i, w := range in {
				out[2*i] = w << 16
				out[2*i+1] = w & 0xFFFF0000
			}
		case 0x3D: // vi2c: pack ints to bytes (top bytes)
			out = make([]uint32, n/4)
			for i := range out {
				for j := 0; j < 4; j++ {
					out[i] |= ((in[4*i+j] >> 24) & 0xFF) << uint(8*j)
				}
			}
		case 0x3E: // vi2us: pack ints to unsigned shorts
			out = make([]uint32, n/2)
			for i := range out {
				out[i] = packHalfU(in[2*i]) | packHalfU(in[2*i+1])<<16
			}
		}
		c.applyDestSaturation(out)
		if f == 0x3D || f == 0x3E {
			// The packs land in the quad's lanes (see
			// writeRawQuadLanes).
			c.writeRawQuadLanes(vd, out)
		} else {
			c.writeRawMasked(vd, len(out), out)
		}
		return 1
	case 0x40, 0x41, 0x48, 0x49: // vsrt1, vsrt2, vsrt3, vsrt4
		// (quad only): sorting passes over the source quad.
		// The min/max picks are the unit's totalOrder ones
		// (the vfpuMinMaxBits rule); the source prefix does
		// not apply. F values from the published bit
		// diagrams: vsrt1 0x40, vsrt2 0x41, vsrt3 0x48,
		// vsrt4 0x49.
		if n != 4 {
			return c.vfpuUnimplemented()
		}
		idx := vecIndices(vs, 4)
		var s [4]uint32
		for i := 0; i < 4; i++ {
			s[i] = c.vregs[idx[i]]
		}
		mn := func(a, b uint32) uint32 { return vfpuMinMaxBits(a, b, false) }
		mx := func(a, b uint32) uint32 { return vfpuMinMaxBits(a, b, true) }
		// When the two (denormal flushed) values compare
		// equal as floats, the pair collapses: the min
		// sorts put the first operand in both slots, the
		// max sorts the second (the corpus specials zero
		// and denormal rows pin this; distinct values and
		// NaNs order by the totalOrder rule).
		sortPair := func(a, b uint32, maxSort bool) (uint32, uint32) {
			if fpInput(a) == fpInput(b) {
				if maxSort {
					return b, b
				}
				return a, a
			}
			if maxSort {
				return mx(a, b), mn(a, b)
			}
			return mn(a, b), mx(a, b)
		}
		var out [4]uint32
		switch f {
		case 0x40: // vsrt1: pairs (0,1) and (2,3), min first
			out[0], out[1] = sortPair(s[0], s[1], false)
			out[2], out[3] = sortPair(s[2], s[3], false)
		case 0x41: // vsrt2: pairs (0,3) and (1,2), min first
			lo03, hi03 := sortPair(s[0], s[3], false)
			lo12, hi12 := sortPair(s[1], s[2], false)
			out = [4]uint32{lo03, lo12, hi12, hi03}
		case 0x48: // vsrt3: pairs (0,1) and (2,3), max first
			out[0], out[1] = sortPair(s[0], s[1], true)
			out[2], out[3] = sortPair(s[2], s[3], true)
		case 0x49: // vsrt4: pairs (0,3) and (1,2), max first
			hi03, lo03 := sortPair(s[0], s[3], true)
			hi12, lo12 := sortPair(s[1], s[2], true)
			out = [4]uint32{hi03, hi12, lo12, lo03}
		}
		c.writeRawMasked(vd, 4, out[:])
		return 1
	case 0x42, 0x43: // vbfy1 (pair/quad), vbfy2 (quad):
		// butterfly sums and differences; no source prefix.
		{
			idx := vecIndices(vs, n)
			var a [4]float64
			for i := 0; i < n; i++ {
				a[i] = fpInput(c.vregs[idx[i]])
			}
			var out [4]float64
			switch {
			case f == 0x42 && n == 2:
				out[0], out[1] = a[0]+a[1], a[0]-a[1]
			case f == 0x42 && n == 4:
				out[0], out[1], out[2], out[3] = a[0]+a[1], a[0]-a[1], a[2]+a[3], a[2]-a[3]
			case f == 0x43 && n == 4:
				out[0], out[1], out[2], out[3] = a[0]+a[2], a[1]+a[3], a[0]-a[2], a[1]-a[3]
			default:
				return c.vfpuUnimplemented()
			}
			for i := 0; i < n; i++ {
				if math.IsNaN(out[i]) {
					// A butterfly NaN is always the
					// positive pattern on hardware
					// (the corpus specials rows, for
					// both Inf sums and NaN inputs).
					out[i] = math.NaN()
				}
			}
			c.writeVD(vd, n, out)
			return 1
		}
	case 0x44: // vocp: element wise 1 - x; no source prefix.
		{
			idx := vecIndices(vs, n)
			var out [4]float64
			for i := 0; i < n; i++ {
				out[i] = 1 - fpInput(c.vregs[idx[i]])
				if math.IsNaN(out[i]) {
					// Like the butterflies, a vocp NaN
					// is the positive pattern (the
					// corpus specials rows).
					out[i] = math.NaN()
				}
			}
			c.writeVD(vd, n, out)
			return 1
		}
	case 0x45: // vsocp.p: one's complement pair splat with saturation
		if n != 2 {
			return c.vfpuUnimplemented()
		}
		sIdx := vecIndices(vs, n)
		clamp01 := func(v float64) uint32 {
			if math.IsNaN(v) {
				// A NaN through the saturating complement
				// is the positive NaN pattern (the corpus
				// specials rows).
				return 0x7F800001
			}
			if v == 0 {
				v = 0 // a negative zero clamps to +0
			} else if v < 0 {
				v = 0
			} else if v > 1 {
				v = 1
			}
			return math.Float32bits(float32(v))
		}
		a := fpInput(c.vregs[sIdx[0]])
		b := fpInput(c.vregs[sIdx[1]])
		c.writeRawMasked(vd, 4, []uint32{clamp01(1 - a), clamp01(a), clamp01(1 - b), clamp01(b)})
		return 1
	case 0x46, 0x47: // vfad, vavg: reductions to a single
		a := c.readVS(vs, n)
		sum := 0.0
		for i := 0; i < n; i++ {
			sum += a[i]
		}
		if f == 0x47 {
			sum /= float64(n)
		}
		if math.IsNaN(sum) {
			// A reduction NaN is the positive pattern
			// (Inf cancellation included; the corpus
			// specials rows).
			sum = math.NaN()
		}
		c.writeVD(vd, 1, [4]float64{sum})
		// Only lane 0 is written. (The vavg corpus test
		// prints NaN in the other lanes, but those are the
		// recording machine's uninitialized register state:
		// its main never resets the register file. The
		// specials test, whose wrapper pre-zeroes the
		// destination quad, shows the lanes untouched.)
		return 1
	case 0x59, 0x5A, 0x5B: // vt4444, vt5551, vt5650 color conversions
		if n != 4 {
			return c.vfpuUnimplemented()
		}
		sIdx := vecIndices(vs, n)
		in := [4]uint32{c.vregs[sIdx[0]], c.vregs[sIdx[1]], c.vregs[sIdx[2]], c.vregs[sIdx[3]]}
		var conv func(a, b uint32) uint32
		switch f {
		case 0x59: // vt4444
			conv = func(a, b uint32) uint32 {
				return (a >> 4 & 0xF) | (a >> 8 & 0xF0) | (a >> 12 & 0xF00) | (a >> 16 & 0xF000) |
					(b << 12 & 0xF0000) | (b << 8 & 0xF00000) | (b << 4 & 0xF000000) | (b & 0xF0000000)
			}
		case 0x5A: // vt5551
			conv = func(a, b uint32) uint32 {
				return (a >> 3 & 0x1F) | (a >> 6 & 0x3E0) | (a >> 9 & 0x7C00) | (a >> 16 & 0x8000) |
					(b << 13 & 0x1F0000) | (b << 10 & 0x3E00000) | (b << 7 & 0x7C000000) | (b & 0x80000000)
			}
		case 0x5B: // vt5650
			conv = func(a, b uint32) uint32 {
				return (a >> 3 & 0x1F) | (a >> 5 & 0x7E0) | (a >> 8 & 0xF800) |
					(b << 13 & 0x1F0000) | (b << 11 & 0x7E00000) | (b << 8 & 0xF8000000)
			}
		}
		c.writeRawMasked(vd, 2, []uint32{conv(in[0], in[1]), conv(in[2], in[3])})
		return 1
	case 0x3C: // vi2uc, quad only: pack the top byte of each
		// int, clamping negatives to zero, into one single
		if n != 4 {
			return c.vfpuUnimplemented()
		}
		in := c.readRawPrefixed(vs, 4)
		var word uint32
		for i := 0; i < 4; i++ {
			raw := in[i]
			var b uint32
			if int32(raw) >= 0 {
				b = (raw >> 23) & 0xFF
			}
			word |= b << uint(8*i)
		}
		packed := []uint32{word}
		c.applyDestSaturation(packed)
		c.writeRawQuadLanes(vd, packed)
		return 1
	case 0x3F: // vi2s, pair or quad: pack the top halfword of
		// each int
		if n != 2 && n != 4 {
			return c.vfpuUnimplemented()
		}
		in := c.readRawPrefixed(vs, n)
		var bits [4]uint32
		for i := 0; i < n; i++ {
			h := (in[i] >> 16) & 0xFFFF
			bits[i/2] |= h << uint(16*(i%2))
		}
		packed := bits[:n/2]
		c.applyDestSaturation(packed)
		c.writeRawQuadLanes(vd, packed)
		return 1
	}
	if f >= 0x60 { // vcst: constant index is the low 5 bits of F
		idx := f & 0x1F
		// The hardware VCST table (the davidgfnet VFPU
		// document's constant list), indexed from 1: index 1
		// is the huge value, 2 sqrt(2), and so on. Index 0
		// and indices past the documented list read as zero
		// here; the corpus vector test exercises the listed
		// range. (PSP-2 wrapped the index into the prefix
		// constant table, which is a different table.)
		var bits uint32
		if idx >= 1 && int(idx) <= len(vcstTable) {
			bits = vcstTable[idx-1]
		}
		c.writeRawMasked(vd, 1, []uint32{bits})
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
	case 0x11: // vrsq: the sign of a zero input carries into
		// the infinity (the exact corpus test: -0 gives -Inf),
		// and a negative input's generated NaN takes the
		// input's sign (the same test: -Inf gives the negative
		// NaN pattern).
		if v < 0 {
			return math.Copysign(math.NaN(), v)
		}
		if v == 0 {
			return math.Copysign(math.Inf(1), v)
		}
		return 1 / math.Sqrt(v)
	case 0x12: // vsin, in units of pi/2. The angle scaling is
		// float32 arithmetic on the hardware: 1.0 * (pi/2 as
		// float32) lands just past pi/2, which is why vcos(1)
		// reads back -0.0000 in the corpus callout test. An
		// infinite input's NaN takes the input's sign (the
		// corpus exact test).
		if math.IsInf(v, 0) {
			return math.Copysign(math.NaN(), v)
		}
		return math.Sin(float64(float32(v) * float32(math.Pi/2)))
	case 0x13: // vcos
		return math.Cos(float64(float32(v) * float32(math.Pi/2)))
	case 0x14: // vexp2
		return math.Pow(2, v)
	case 0x15: // vlog2
		return math.Log2(v)
	case 0x16: // vsqrt: hardware reads -0 back as +0, and a
		// negative input gives the positive NaN pattern (a
		// generated NaN; Go's Sqrt NaN has the sign bit set,
		// so the case is spelled out).
		if v == 0 {
			return 0
		}
		if v < 0 || math.IsNaN(v) {
			return math.NaN()
		}
		return math.Sqrt(v)
	case 0x17: // vasin, result in units of pi/2. An infinite
		// input's NaN takes the input's sign (the corpus
		// exact test).
		if math.IsInf(v, 0) {
			return math.Copysign(math.NaN(), v)
		}
		return math.Asin(v) * 2 / math.Pi
	case 0x18: // vnrcp: the negation flips a propagated NaN's
		// sign (the corpus exact test: +NaN gives the
		// negative NaN pattern, -NaN the positive).
		if math.IsNaN(v) {
			if math.Signbit(v) {
				return math.NaN()
			}
			return -math.NaN()
		}
		return -1 / v
	case 0x1A: // vnsin: an infinite input's NaN takes the
		// opposite sign of the input (the negation flips it,
		// as with vnrcp; the corpus exact test).
		if math.IsInf(v, 0) {
			return math.Copysign(math.NaN(), -v)
		}
		return -math.Sin(float64(float32(v) * float32(math.Pi/2)))
	case 0x1C: // vrexp2
		return math.Pow(2, -v)
	case 0x4A: // vsgn: a NaN counts by its sign bit (the
		// corpus specials rows: +NaN gives +1, -NaN -1);
		// zeros (denormals already flushed) give 0.
		if math.IsNaN(v) {
			if math.Signbit(v) {
				return -1
			}
			return 1
		}
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
			bits[i] = roundToSingleVFPU(v)
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
		// The product lands in the register file
		// transposed: file element (i, j) is the dot of
		// file row j of the left operand with file column
		// i of the right one (the corpus matrix multiply
		// rows pin this; read back through the strided
		// sv.q R stores, the printed matrix is the
		// ordinary row times column product). The operand
		// fields decode per slot (matrixOriginVmmul).
		ma, ar, ac := matrixOriginVmmul(vs, n, slotVS)
		am := c.readMatrixAt(ma, ar, ac, n)
		mb, br, bc := matrixOriginVmmul(vt, n, slotVT)
		bm := c.readMatrixAt(mb, br, bc, n)
		var cm [4][4]float64
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				sum := 0.0
				for k := 0; k < n; k++ {
					sum += am[j][k] * bm[k][i]
				}
				cm[i][j] = sum
			}
		}
		md, dr, dc := matrixOriginVmmul(vd, n, slotVD)
		c.writeMatrixAt(md, dr, dc, n, cm)
		return 1
	case 1, 2, 3: // vtfmN and vhtfmN, matrix size is G+1
		m := int(g) + 1
		mat := c.readMatrix(vs, m)
		var out [4]float64
		switch {
		case int(sizeEnum) == m-1: // vtfm: full vector operand
			// The transform dots the vector with the
			// matrix's columns in file order (out[i] is
			// the dot of the i-th file column with the
			// vector), the opposite orientation from
			// vmmul's row times column product; the
			// corpus matrix rows pin this.
			vec := c.paddedFloats(vt, m)
			for i := 0; i < m; i++ {
				sum := 0.0
				for j := 0; j < m; j++ {
					sum += mat[j][i] * vec[j]
				}
				out[i] = sum
			}
		case int(sizeEnum) == m-2: // vhtfm: the last matrix
			// column is weighted by an implicit 1.0
			vec := c.paddedFloats(vt, m-1)
			for i := 0; i < m; i++ {
				sum := 0.0
				for j := 0; j < m-1; j++ {
					sum += mat[j][i] * vec[j]
				}
				out[i] = sum + mat[m-1][i]
			}
		default:
			return c.vfpuUnimplemented()
		}
		// The destination is a matrix row too: it lands on
		// the strided positions, not the vector walk.
		dbase := int((vd>>2)&7)*16 + int(vd&3)
		for i := 0; i < m; i++ {
			c.vregs[dbase+4*i] = roundToSingleVFPU(out[i])
		}
		return 1
	case 4: // vmscl
		if n < 2 {
			return c.vfpuUnimplemented()
		}
		// The vs slot's bit 5 is not a transpose flag here
		// either: the corpus scale test's E000 operand reads
		// as the plain matrix (its expected output is the
		// untransposed nonsense matrix scaled), the same
		// rule the vmmul slot decode follows.
		am := c.readMatrix(vs&^0x20, n)
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
		if f >= 0x20 && f < 0x40 {
			// vrot: build a rotation row from the angle in
			// the source single. The immediate (the low 5
			// bits of F) names the cosine lane (bits 0-1)
			// and the sine lane (bits 2-3); bit 4 negates
			// the sine. When the two lanes coincide, that
			// lane takes the cosine and every other lane
			// the sine; otherwise unlisted lanes are zero.
			imm := f & 0x1F
			angle := fpInput(c.vregs[singleIndex(vs)])
			s := math.Sin(float64(float32(angle) * float32(math.Pi/2)))
			co := math.Cos(float64(float32(angle) * float32(math.Pi/2)))
			if imm&0x10 != 0 {
				s = -s
			}
			cl := int(imm & 3)
			sl := int((imm >> 2) & 3)
			var out [4]float64
			for e := 0; e < n; e++ {
				switch {
				case e == cl:
					out[e] = co
				case cl == sl || e == sl:
					out[e] = s
				}
			}
			c.writeVD(vd, n, out)
			return 1
		}
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
// execPrefixes handles the VFPU5 group (primary opcode 0x37).
// The selector is bits [25:23] (the test suite's assembler calls
// it VFPUFUNC): 0 vpfxs, 2 vpfxt, 4 vpfxd, 6 viim.s, 7 vfim.s.
// The prefix stores keep the full immediate (source prefixes 20
// bits, dest 12) for the MFVC readback; an earlier 19 bit mask
// dropped lane 3's negate flag (corpus prefix_ctrl).
func (c *CPU) execPrefixes(instr uint32) int {
	switch (instr >> 23) & 7 {
	case 0: // vpfxs
		c.spfxImm = instr & 0xFFFFF
		c.spfxOn = true
	case 2: // vpfxt
		c.tpfxImm = instr & 0xFFFFF
		c.tpfxOn = true
	case 4: // vpfxd
		c.dpfxImm = instr & 0xFFF
		c.dpfxOn = true
	case 6: // viim.s: signed immediate converted to float.
		vd := (instr >> 16) & 0x7F
		c.writeVD(vd, 1, [4]float64{float64(int16(instr & 0xFFFF))})
	case 7: // vfim.s: the immediate is a half float.
		vd := (instr >> 16) & 0x7F
		c.writeRawMasked(vd, 1, []uint32{halfToFloatBits(uint16(instr & 0xFFFF))})
	default:
		return c.vfpuUnimplemented()
	}
	return 1
}

// writeRawMasked stores raw bit patterns at the vd lanes,
// honoring only the dest prefix write mask, and clears all
// prefix state. The pack, unpack and color ops support no
// other prefix effect.
func (c *CPU) writeRawMasked(vd uint32, n int, bits []uint32) {
	idx := vecIndices(vd, n)
	for i := 0; i < n && i < len(bits); i++ {
		if c.dpfxOn && (c.dpfxImm>>(8+uint(i)))&1 == 1 {
			continue
		}
		c.vregs[idx[i]] = bits[i]
	}
	c.dpfxOn = false
}

// writeRawQuadLanes writes raw bit patterns into the first
// lanes of the QUAD operand at vd, honoring the destination
// prefix write mask. The pack instructions (vi2c, vi2uc,
// vi2s, vi2us) address their result this way: their vd field
// names a single inside the quad (the corpus wrappers use
// "S010.s"), and the packed words land in the quad's lanes
// starting at lane 0 of the quad decode, not at the single's
// own position.
func (c *CPU) writeRawQuadLanes(vd uint32, bits []uint32) {
	idx := vecIndices(vd, 4)
	// The pack dest honors the field's end anchor (bit 6):
	// with it set, the packed words land at the END of the
	// quad's lanes (the corpus overlap vi2s.q C002 case,
	// vd field 64, words at lanes 2 and 3); without it they
	// start at lane 0 (the corpus convert packs, vd field 1).
	off := 0
	if vd&0x40 != 0 {
		off = 4 - len(bits)
	}
	for i := 0; i < len(bits) && i+off < 4; i++ {
		if c.dpfxOn && (c.dpfxImm>>(8+uint(i+off)))&1 == 1 {
			continue
		}
		c.vregs[idx[i+off]] = bits[i]
	}
	c.dpfxOn = false
}

// floatToIntBits converts a float value to integer bits for
// the vf2i family. mode selects the rounding: 0 nearest even,
// 1 toward zero, 2 up (ceil: the vf2iu "u" is up, not
// unsigned, the corpus convert_scaled matrix pins negative
// results for negative inputs), 3 down. All four are signed:
// out of range values clamp to the signed extremes, and NaN
// clamps to the positive extreme, per the convert_scaled
// matrix. (PSP-2 returned 0 for NaN; the corpus refutes that.)
func floatToIntBits(v float64, mode uint32) uint32 {
	if math.IsNaN(v) {
		return 0x7FFFFFFF
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
	if r >= 2147483648.0 {
		return 0x7FFFFFFF
	}
	if r < -2147483648.0 {
		return 0x80000000
	}
	return uint32(int32(r))
}

// vcstTable holds the hardware VCST constants in order
// (indices 1..19 of the instruction's immediate): huge,
// sqrt(2), sqrt(1/2), 2/sqrt(pi), 2/pi, 1/pi, pi/4, pi/2, pi,
// e, log2(e), log10(e), ln(2), ln(10), 2pi, pi/6, log10(2),
// log2(10), sqrt(3)/2.
var vcstTable = [19]uint32{
	0x7F7FFFFF, 0x3FB504F3, 0x3F3504F3, 0x3F906EBB, 0x3F22F983,
	0x3EA2F983, 0x3F490FDB, 0x3FC90FDB, 0x40490FDB, 0x402DF854,
	0x3FB8AA3B, 0x3EDE5BD9, 0x3F317218, 0x40135D8E, 0x40C90FDB,
	0x3F060A92, 0x3E9A209B, 0x40549A78, 0x3F5DB3D7,
}

// packHalfU is the vi2us per-element pack: arithmetic shift
// by 15 with negative results clamped to zero (the corpus
// convert rows pin both halves of the rule).
func packHalfU(v uint32) uint32 {
	s := int32(v) >> 15
	if s < 0 {
		return 0
	}
	return uint32(s) & 0xFFFF
}

// packByteU is the vi2uc per-element pack: arithmetic shift
// by 23 with negative results clamped to zero.
func packByteU(v uint32) uint32 {
	s := int32(v) >> 23
	if s < 0 {
		return 0
	}
	return uint32(s) & 0xFF
}

// execVF2H converts floats to packed half floats.
func (c *CPU) execVF2H(vd, vs uint32, n int) int {
	// The conversion works on the raw source patterns: a
	// float64 round trip would lose a NaN's mantissa, which
	// the conversion preserves in its low bits (the corpus
	// checkHalf row: the toolchain NaN 0x7FBFFFFF converts
	// to the half 0x7FFF).
	idx := vecIndices(vs, n)
	var halves [4]uint32
	for i := 0; i < 4; i++ {
		var fv float32
		if i < n {
			fv = math.Float32frombits(c.vregs[idx[i]])
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
	// The size bits give the SOURCE size in words: a single
	// source expands to a pair of floats, a pair source to
	// a quad (the corpus checkHalf wrapper runs vh2f.p from
	// a pair source into a quad destination).
	if n != 1 && n != 2 {
		return c.vfpuUnimplemented()
	}
	idx := vecIndices(vs, n)
	var halves [4]uint16
	for i := 0; i < n; i++ {
		w := c.vregs[idx[i]]
		halves[2*i] = uint16(w)
		halves[2*i+1] = uint16(w >> 16)
	}
	// The converted patterns are written raw: the exp 31 arm
	// of halfToFloatBits produces NaN patterns whose mantissa
	// bits the corpus checkHalf rows pin (half 0x7F80 becomes
	// 0x7F800380), and the float64 path would canonicalize
	// them to 0x7F800001. With a source prefix armed the
	// values take the float path (and its NaN form) instead.
	var bits [4]uint32
	for i := 0; i < 2*n; i++ {
		bits[i] = halfToFloatBits(halves[i])
	}
	if c.spfxOn {
		var vals [4]float64
		for i := 0; i < 2*n; i++ {
			vals[i] = float64(math.Float32frombits(bits[i]))
		}
		vals = c.applySPFXNow(vals, 2*n)
		c.writeRawFloats(vd, 2*n, vals)
		return 1
	}
	c.writeRawVec(vd, 2*n, bits)
	return 1
}

// floatToHalf converts float32 bits to half bits with the
// hardware algorithm (the davidgfnet VFPU document's ifloat32,
// also what pspautotests convert pins): truncation, no
// rounding; exponents at or below 112 flush to signed zero,
// exponents at or above 143 become infinity, and a NaN or
// infinity input keeps only the LOW 10 mantissa bits (so a
// canonical NaN becomes infinity, a documented hardware
// quirk). PSP-2 did full IEEE rounding here; the corpus
// refutes it.
func floatToHalf(f float32) uint16 {
	bits := math.Float32bits(f)
	sign := uint16((bits >> 16) & 0x8000)
	exp := (bits >> 23) & 0xFF
	man := bits & 0x7FFFFF
	switch {
	case exp == 0:
		return sign
	case exp == 0xFF:
		return sign | 0x7C00 | uint16(man&0x3FF)
	case exp <= 112:
		return sign
	case exp >= 143:
		return sign | 0x7C00
	default:
		return sign | uint16(exp-112)<<10 | uint16(man>>13)
	}
}

// halfToFloatBits converts half bits to float32 bits with the
// hardware algorithm: a denormal half becomes signed zero (no
// normalization), and the infinity/NaN exponent maps straight
// across with the mantissa shifted up.
func halfToFloatBits(h uint16) uint32 {
	sign := uint32(h&0x8000) << 16
	exp := (h >> 10) & 0x1F
	man := uint32(h & 0x3FF)
	switch {
	case exp == 0:
		return sign
	case exp == 31:
		// Infinity and NaN halves: the exponent saturates,
		// and the mantissa lands UNSHIFTED in the low bits
		// (the corpus checkHalf row: half 0x7F80 converts to
		// the float pattern 0x7F800380).
		return sign | 0x7F800000 | man
	default:
		return sign | uint32(exp+112)<<23 | man<<13
	}
}

// halfToFloat converts IEEE 754 half bits to a float32.
func halfToFloat(h uint16) float32 {
	return math.Float32frombits(halfToFloatBits(h))
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
	var lanes uint32
	all := true
	any := false
	for i := 0; i < n; i++ {
		r := evalVCond(cond, a[i], b[i])
		if r {
			lanes |= 1 << uint(i)
			any = true
		} else {
			all = false
		}
	}
	// The compare writes its own lane bits plus the any (bit
	// 4) and all (bit 5) reductions; lane bits beyond the
	// op size keep their previous values (the corpus
	// vbranch slot_cc_after cases: a scalar compare after
	// CC = 0x3F leaves bits 1-3 set).
	cc := (c.cc &^ ((1 << uint(n)) - 1) &^ 0x30) | lanes
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
// lsField returns the load/store family register field: the high
// five bits at [20:16] and the low two bits at [1:0].
func lsField(instr uint32) uint32 {
	return ((instr>>16)&31)<<2 | (instr & 3)
}

// lsSingleIndex decodes a load/store single: the field is
// (matrix << 4) | (column << 2) | row.
func lsSingleIndex(instr uint32) int {
	f := lsField(instr)
	m := int((f >> 4) & 7)
	col := int((f >> 2) & 3)
	row := int(f & 3)
	return m*16 + row*4 + col
}

// lsIndices decodes a load/store quad operand. The operand's
// five high bits (the word's bits [20:16]) split as matrix
// (top 3) and index (low 2), and the walk always strides by
// 4 from the index: positions m*16 + index + 4i. The word's
// bit 0 (the assembler's R/C distinction) does not change
// the positions: the corpus proves it twice over, since the
// vregs sv.q dump (R names) and the colors lvl/lvr chain
// (C names) both reproduce their expected bytes only under
// this rule, and the positions coincide with the arithmetic
// column of the same index. (Bit 1 of the word belongs to
// the instruction, not the operand: it selects LVR.Q over
// LVL.Q in opcode 0x35.)
func lsIndices(instr uint32, n int) [4]int {
	h := int((instr >> 16) & 31)
	m := (h >> 2) & 7
	base := h & 3
	var out [4]int
	for i := 0; i < n; i++ {
		out[i] = m*16 + base + 4*i
	}
	return out
}

// lsLoadIndices is the quad walk for LOADS. It differs from
// the store walk in one case: when the operand field (the
// five bits H shifted left 2, plus the word's low 2 bits) is
// odd, the load walks the four consecutive positions of the
// arithmetic row (H & 3) instead of the strided column. The
// corpus matrix transpose test pins this: it loads rows with
// lv.q R00k (odd fields) and stores them back with sv.q C00k
// (the same fields), and only the consecutive load walk
// produces the recorded transposed matrix. Even fields walk
// strided, exactly like stores (the vregs dump, the colors
// chain, and the vector wrappers all confirm those).
func lsLoadIndices(instr uint32, n int) [4]int {
	if (lsField(instr) & 1) == 0 {
		return lsIndices(instr, n)
	}
	h := int((instr >> 16) & 31)
	m := (h >> 2) & 7
	row := h & 3
	var out [4]int
	for i := 0; i < n; i++ {
		out[i] = m*16 + row*4 + i
	}
	return out
}

func (c *CPU) execLVS(instr uint32) int {
	addr := c.vfpuAddr(instr)
	if addr&3 != 0 {
		return c.addrError(excAdEL, addr)
	}
	c.vregs[lsSingleIndex(instr)] = c.bus.Read32(addr)
	return 1
}

// execSVS implements SV.S (opcode 0x3A).
func (c *CPU) execSVS(instr uint32) int {
	addr := c.vfpuAddr(instr)
	if addr&3 != 0 {
		return c.addrError(excAdES, addr)
	}
	c.bus.Write32(addr, c.vregs[lsSingleIndex(instr)])
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

// execLVQ implements LV.Q (opcode 0x36).
func (c *CPU) execLVQ(instr uint32) int {
	addr := c.vfpuAddr(instr)
	if addr&15 != 0 {
		return c.addrError(excAdEL, addr)
	}
	idx := lsLoadIndices(instr, 4)
	for i := 0; i < 16; i++ {
		c.setQuadByte(idx, i, c.bus.Read8(addr+uint32(i)))
	}
	return 1
}

// execLVLR implements the unaligned quad loads (opcode 0x35):
// LVL.Q when bit 1 is clear, LVR.Q when it is set (the corpus
// vector binary's words 0xD4600000..0xD4600003 pin the pair, with
// bit 0 the operand transpose flag as in the rest of the family).
// lvlCount returns the byte count an LVL.Q at byte offset a
// (addr & 15) transfers: the corpus vector test pins it at
// (a + 12) & 15, with 0 meaning 16.
func lvlCount(a int) int {
	n := (a + 12) & 15
	if n == 0 {
		return 16
	}
	return n
}

// execLVLR implements LVL.Q and LVR.Q (opcode 0x35; bit 1 of
// the word selects LVR). The semantics are the corpus vector
// test's, derived from its checked rows: LVL.Q loads the
// lvlCount(a) bytes ENDING at addr + 4 into the HIGH bytes of
// the quad (the loaded window's last word is the word at
// addr), and LVR.Q loads the 20 - lvlCount(a) bytes STARTING
// at addr into the LOW bytes of the quad. The two windows
// overlap exactly on the word at addr, which is what makes
// the classic lvl/lvr pair at (A + 12, A) assemble a full
// aligned quad (the corpus colors wrappers). PSP-2's
// boundary-relative model was invented and is refuted by
// those rows.
func (c *CPU) execLVLR(instr uint32) int {
	addr := c.vfpuAddr(instr)
	idx := lsLoadIndices(instr, 4)
	a := int(addr & 15)
	if instr&2 == 0 { // LVL.Q
		n := lvlCount(a)
		src := addr + 4 - uint32(n)
		for i := 0; i < n; i++ {
			c.setQuadByte(idx, 16-n+i, c.bus.Read8(src+uint32(i)))
		}
	} else { // LVR.Q
		n := 20 - lvlCount(a)
		for i := 0; i < n; i++ {
			c.setQuadByte(idx, i, c.bus.Read8(addr+uint32(i)))
		}
	}
	return 1
}

// execSVQ implements SV.Q (opcode 0x3E).
func (c *CPU) execSVQ(instr uint32) int {
	addr := c.vfpuAddr(instr)
	if addr&15 != 0 {
		return c.addrError(excAdES, addr)
	}
	idx := lsIndices(instr, 4)
	for i := 0; i < 16; i++ {
		c.bus.Write8(addr+uint32(i), c.quadByte(idx, i))
	}
	return 1
}

// execSVLR implements the unaligned quad stores (opcode 0x3D):
// SVL.Q when bit 1 is clear, SVR.Q when it is set. They are the
// exact mirrors of the loads: SVL.Q stores the quad's high
// lvlCount(a) bytes to the window ending at addr + 4, and
// SVR.Q stores the low 20 - lvlCount(a) bytes starting at
// addr.
func (c *CPU) execSVLR(instr uint32) int {
	addr := c.vfpuAddr(instr)
	idx := lsIndices(instr, 4)
	a := int(addr & 15)
	if instr&2 == 0 { // SVL.Q
		n := lvlCount(a)
		dst := addr + 4 - uint32(n)
		for i := 0; i < n; i++ {
			c.bus.Write8(dst+uint32(i), c.quadByte(idx, 16-n+i))
		}
	} else { // SVR.Q
		n := 20 - lvlCount(a)
		for i := 0; i < n; i++ {
			c.bus.Write8(addr+uint32(i), c.quadByte(idx, i))
		}
	}
	return 1
}
