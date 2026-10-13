package allegrex

import (
	"math"
	"testing"
)

// VFPU test encoders and operand helpers.

func fb(v float32) uint32 { return math.Float32bits(v) }
func fv(b uint32) float32 { return math.Float32frombits(b) }

func sReg(m, r, c uint32) uint32    { return m<<4 | r<<2 | c }
func qRow(m, r uint32) uint32       { return m<<4 | r<<2 }
func qCol(m, c uint32) uint32       { return m<<4 | c<<2 | 1 }
func pRow(m, r, half uint32) uint32 { return m<<4 | r<<2 | half<<1 }
func tRow(m, r uint32) uint32       { return m<<4 | r<<2 }
func lin(m, r, c int) int           { return m*16 + r*4 + c }

func encVV(op, g, vd, vs, vt, size uint32) uint32 {
	return op<<26 | g<<23 | vt<<16 | ((size>>1)&1)<<15 | vs<<8 | (size&1)<<7 | vd
}
func encVCmp(vs, vt, size, cond uint32) uint32 {
	return 0x1B<<26 | vt<<16 | ((size>>1)&1)<<15 | vs<<8 | (size&1)<<7 | cond
}
func encVUnary(f, vd, vs, size uint32) uint32 {
	return 0x34<<26 | f<<16 | ((size>>1)&1)<<15 | vs<<8 | (size&1)<<7 | vd
}
func encVF2I(variant, scale, vd, vs, size uint32) uint32 {
	return 0x34<<26 | 4<<23 | (variant<<5|scale)<<16 | ((size>>1)&1)<<15 | vs<<8 | (size&1)<<7 | vd
}
func encVI2F(scale, vd, vs, size uint32) uint32 {
	return 0x34<<26 | 5<<23 | scale<<16 | ((size>>1)&1)<<15 | vs<<8 | (size&1)<<7 | vd
}
func encVCmov(isF bool, sel, vd, vs, size uint32) uint32 {
	field := uint32(0x20) | sel
	if isF {
		field = 0x28 | sel
	}
	return 0x34<<26 | 5<<23 | field<<16 | ((size>>1)&1)<<15 | vs<<8 | (size&1)<<7 | vd
}
func encVPrefix(which, imm uint32) uint32 {
	return 0x37<<26 | which<<24 | imm
}
func encMatrix(g, vd, vs, vt, size uint32) uint32 {
	return 0x3C<<26 | g<<23 | vt<<16 | ((size>>1)&1)<<15 | vs<<8 | (size&1)<<7 | vd
}
func encMatrixF(f, vd, vs, size uint32) uint32 {
	return 0x3C<<26 | 7<<23 | f<<16 | ((size>>1)&1)<<15 | vs<<8 | (size&1)<<7 | vd
}
func encVLoad(op, base, reg7, byteOff, sub uint32) uint32 {
	return op<<26 | base<<21 | ((reg7>>2)&31)<<16 | ((byteOff>>2)&0x3FFF)<<2 | ((reg7 & 3) | sub)
}
func encMFC2(rt, reg7 uint32) uint32 {
	return 0x12<<26 | 0<<21 | rt<<16 | ((reg7>>2)&31)<<11 | (reg7&3)<<6
}
func encMTC2(rt, reg7 uint32) uint32 {
	return 0x12<<26 | 4<<21 | rt<<16 | ((reg7>>2)&31)<<11 | (reg7&3)<<6
}
func encCFC2(rt, rd uint32) uint32 { return 0x12<<26 | 2<<21 | rt<<16 | rd<<11 }
func encCTC2(rt, rd uint32) uint32 { return 0x12<<26 | 6<<21 | rt<<16 | rd<<11 }
func encMFVC(rt, idx uint32) uint32 {
	return 0x12<<26 | 3<<21 | rt<<16 | idx<<8
}
func encMTVC(rt, idx uint32) uint32 {
	return 0x12<<26 | 7<<21 | rt<<16 | idx<<8
}
func encBC2(tf, likely bool, off int32) uint32 {
	var rt uint32
	if tf {
		rt |= 1
	}
	if likely {
		rt |= 2
	}
	return 0x12<<26 | 8<<21 | rt<<16 | uint32(uint16(off))
}
func setVec(h *harness, reg uint32, n int, vals ...float32) {
	idx := vecIndices(reg, n)
	for i, v := range vals {
		h.c.SetVSingle(idx[i], fb(v))
	}
}
func getVec(h *harness, reg uint32, n int) []float32 {
	idx := vecIndices(reg, n)
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = fv(h.c.VSingle(idx[i]))
	}
	return out
}
func checkVec(t *testing.T, name string, got []float32, want ...float32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: len %d want %d", name, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s lane %d = %v (bits %#08x), want %v", name, i, got[i], fb(got[i]), want[i])
		}
	}
}
func checkApprox(t *testing.T, name string, got float32, want float64, tol float64) {
	t.Helper()
	d := math.Abs(float64(got) - want)
	scale := math.Abs(want)
	if scale < 1 {
		scale = 1
	}
	if d/scale > tol {
		t.Errorf("%s = %v, want %v (tol %v)", name, got, want, tol)
	}
}

func TestVFPUAddSubMulDivSizes(t *testing.T) {
	// S: vadd
	h := newHarness()
	setVec(h, sReg(0, 0, 0), 1, 1.5)
	setVec(h, sReg(2, 0, 1), 1, 2.25)
	h.r.load(0, encVV(0x18, 0, sReg(1, 0, 0), sReg(0, 0, 0), sReg(2, 0, 1), 0))
	h.run(1)
	checkVec(t, "vadd.s", getVec(h, sReg(1, 0, 0), 1), 3.75)
	// P: vsub
	h = newHarness()
	setVec(h, pRow(0, 1, 0), 2, 5, 3)
	setVec(h, pRow(2, 2, 0), 2, 1, 4)
	h.r.load(0, encVV(0x18, 1, pRow(1, 0, 0), pRow(0, 1, 0), pRow(2, 2, 0), 1))
	h.run(1)
	checkVec(t, "vsub.p", getVec(h, pRow(1, 0, 0), 2), 4, -1)
	// T: vmul
	h = newHarness()
	setVec(h, tRow(0, 0), 3, 2, 3, 4)
	setVec(h, tRow(2, 1), 3, 0.5, 2, -1)
	h.r.load(0, encVV(0x19, 0, tRow(1, 0), tRow(0, 0), tRow(2, 1), 2))
	h.run(1)
	checkVec(t, "vmul.t", getVec(h, tRow(1, 0), 3), 1, 6, -4)
	// Q: vdiv
	h = newHarness()
	setVec(h, qRow(0, 0), 4, 8, 9, 10, 12)
	setVec(h, qRow(2, 1), 4, 2, 3, 4, 6)
	h.r.load(0, encVV(0x18, 7, qRow(1, 0), qRow(0, 0), qRow(2, 1), 3))
	h.run(1)
	checkVec(t, "vdiv.q", getVec(h, qRow(1, 0), 4), 4, 3, 2.5, 2)
}

func TestVFPUMinMaxNaN(t *testing.T) {
	nan := math.Float32frombits(canonicalNaN)
	h := newHarness()
	setVec(h, qRow(0, 0), 4, 1, nan, nan, 5)
	setVec(h, qRow(0, 1), 4, 2, 7, nan, 3)
	h.r.load(0,
		encVV(0x1B, 2, qRow(1, 0), qRow(0, 0), qRow(0, 1), 3), // vmin
		encVV(0x1B, 3, qRow(1, 1), qRow(0, 0), qRow(0, 1), 3), // vmax
	)
	h.run(2)
	checkVec(t, "vmin", getVec(h, qRow(1, 0), 4)[:2], 1, 7)
	if got := h.c.VSingle(vecIndices(qRow(1, 0), 4)[2]); got != canonicalNaN {
		t.Errorf("vmin both NaN bits = %#08x, want canonical NaN", got)
	}
	checkVec(t, "vmin lane3", getVec(h, qRow(1, 0), 4)[3:], 3)
	checkVec(t, "vmax", getVec(h, qRow(1, 1), 4)[:2], 2, 7)
	if got := h.c.VSingle(vecIndices(qRow(1, 1), 4)[2]); got != canonicalNaN {
		t.Errorf("vmax both NaN bits = %#08x, want canonical NaN", got)
	}
	checkVec(t, "vmax lane3", getVec(h, qRow(1, 1), 4)[3:], 5)
}

func TestVFPUSatAndUnary(t *testing.T) {
	nan := math.Float32frombits(canonicalNaN)
	h := newHarness()
	setVec(h, qRow(0, 0), 4, -0.5, 0.5, 2, nan)
	h.r.load(0,
		encVUnary(0x04, qRow(1, 0), qRow(0, 0), 3), // vsat0
		encVUnary(0x05, qRow(1, 1), qRow(0, 0), 3), // vsat1
	)
	h.run(2)
	checkVec(t, "vsat0", getVec(h, qRow(1, 0), 4), 0, 0.5, 1, 0)
	checkVec(t, "vsat1", getVec(h, qRow(1, 1), 4), -0.5, 0.5, 1, 0)
	// vmov, vabs, vneg
	h = newHarness()
	setVec(h, qRow(0, 0), 4, -1.5, 2, -3, 4)
	h.r.load(0,
		encVUnary(0x00, qRow(1, 0), qRow(0, 0), 3),
		encVUnary(0x01, qRow(1, 1), qRow(0, 0), 3),
		encVUnary(0x02, qRow(1, 2), qRow(0, 0), 3),
	)
	h.run(3)
	checkVec(t, "vmov", getVec(h, qRow(1, 0), 4), -1.5, 2, -3, 4)
	checkVec(t, "vabs", getVec(h, qRow(1, 1), 4), 1.5, 2, 3, 4)
	checkVec(t, "vneg", getVec(h, qRow(1, 2), 4), 1.5, -2, 3, -4)
}

func TestVFPUZeroOneIdt(t *testing.T) {
	h := newHarness()
	h.r.load(0,
		encVUnary(0x06, qRow(0, 0), 0, 3), // vzero
		encVUnary(0x07, qRow(0, 1), 0, 3), // vone
		encVUnary(0x03, qRow(2, 0), 0, 3), // vidt.q into matrix 2
	)
	h.run(3)
	checkVec(t, "vzero", getVec(h, qRow(0, 0), 4), 0, 0, 0, 0)
	checkVec(t, "vone", getVec(h, qRow(0, 1), 4), 1, 1, 1, 1)
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			want := float32(0)
			if i == j {
				want = 1
			}
			if got := fv(h.c.VSingle(lin(2, i, j))); got != want {
				t.Errorf("vidt [%d][%d] = %v, want %v", i, j, got, want)
			}
		}
	}
}

func TestVFPUFloatFuncs(t *testing.T) {
	h := newHarness()
	setVec(h, sReg(0, 0, 0), 1, 2.25)
	setVec(h, sReg(0, 0, 1), 1, 4)
	setVec(h, sReg(0, 0, 2), 1, 3)
	setVec(h, sReg(0, 0, 3), 1, 8)
	h.r.load(0,
		encVUnary(0x16, sReg(1, 0, 0), sReg(0, 0, 0), 0), // vsqrt 2.25 = 1.5
		encVUnary(0x10, sReg(1, 0, 1), sReg(0, 0, 1), 0), // vrcp 4 = 0.25
		encVUnary(0x11, sReg(1, 0, 2), sReg(0, 0, 1), 0), // vrsq 4 = 0.5
		encVUnary(0x14, sReg(1, 0, 3), sReg(0, 0, 2), 0), // vexp2 3 = 8
		encVUnary(0x15, sReg(1, 1, 0), sReg(0, 0, 3), 0), // vlog2 8 = 3
	)
	h.run(5)
	checkApprox(t, "vsqrt", fv(h.c.VSingle(lin(1, 0, 0))), 1.5, 1e-6)
	checkApprox(t, "vrcp", fv(h.c.VSingle(lin(1, 0, 1))), 0.25, 1e-6)
	checkApprox(t, "vrsq", fv(h.c.VSingle(lin(1, 0, 2))), 0.5, 1e-6)
	checkApprox(t, "vexp2", fv(h.c.VSingle(lin(1, 0, 3))), 8, 1e-6)
	checkApprox(t, "vlog2", fv(h.c.VSingle(lin(1, 1, 0))), 3, 1e-6)
	// sqrt of a negative is canonical NaN, rcp of 0 is +Inf.
	h = newHarness()
	setVec(h, sReg(0, 0, 0), 1, -1)
	setVec(h, sReg(0, 0, 1), 1, 0)
	h.r.load(0,
		encVUnary(0x16, sReg(1, 0, 0), sReg(0, 0, 0), 0),
		encVUnary(0x10, sReg(1, 0, 1), sReg(0, 0, 1), 0),
	)
	h.run(2)
	if got := h.c.VSingle(lin(1, 0, 0)); got != canonicalNaN {
		t.Errorf("vsqrt(-1) bits = %#08x, want canonical NaN", got)
	}
	if got := h.c.VSingle(lin(1, 0, 1)); got != 0x7F800000 {
		t.Errorf("vrcp(0) bits = %#08x, want +Inf", got)
	}
}

func TestVFPUTrig(t *testing.T) {
	for _, x := range []float32{0, 0.5, 1.0, 2.0} {
		h := newHarness()
		setVec(h, sReg(0, 0, 0), 1, x)
		h.r.load(0,
			encVUnary(0x12, sReg(1, 0, 0), sReg(0, 0, 0), 0),
			encVUnary(0x13, sReg(1, 0, 1), sReg(0, 0, 0), 0),
			encVUnary(0x1A, sReg(1, 0, 2), sReg(0, 0, 0), 0),
		)
		h.run(3)
		wantSin := math.Sin(float64(x) * math.Pi / 2)
		wantCos := math.Cos(float64(x) * math.Pi / 2)
		checkApprox(t, "vsin", fv(h.c.VSingle(lin(1, 0, 0))), wantSin, 1e-5)
		checkApprox(t, "vcos", fv(h.c.VSingle(lin(1, 0, 1))), wantCos, 1e-5)
		checkApprox(t, "vnsin", fv(h.c.VSingle(lin(1, 0, 2))), -wantSin, 1e-5)
	}
}

func TestVFPUHalfConversions(t *testing.T) {
	h := newHarness()
	setVec(h, qRow(0, 0), 4, 1.0, -2.5, 0.5, -0.25)
	h.r.load(0, encVUnary(0x32, pRow(1, 0, 0), qRow(0, 0), 3)) // vf2h.q
	h.run(1)
	idx := vecIndices(pRow(1, 0, 0), 2)
	if got := h.c.VSingle(idx[0]); got != 0xC1003C00 {
		t.Errorf("vf2h lane0 = %#08x, want 0xC1003C00", got)
	}
	if got := h.c.VSingle(idx[1]); got != 0xB4003800 {
		t.Errorf("vf2h lane1 = %#08x, want 0xB4003800", got)
	}
	// Round trip back with vh2f.q.
	h.r.load(0x40, encVUnary(0x33, qRow(2, 0), pRow(1, 0, 0), 3))
	h.c.SetPC(0x40)
	h.run(1)
	checkVec(t, "vh2f round trip", getVec(h, qRow(2, 0), 4), 1.0, -2.5, 0.5, -0.25)
	// vf2h of NaN: half exponent all ones with nonzero mantissa.
	h = newHarness()
	h.c.SetVSingle(lin(0, 0, 0), canonicalNaN)
	h.r.load(0, encVUnary(0x32, sReg(1, 0, 0), sReg(0, 0, 0), 0))
	h.run(1)
	if got := h.c.VSingle(lin(1, 0, 0)) & 0xFFFF; got != 0x7E00 {
		t.Errorf("vf2h NaN half = %#04x, want 0x7E00", got)
	}
	// vh2f of the smallest denormal half: 2^-24.
	h = newHarness()
	h.c.SetVSingle(lin(0, 0, 0), 0x00000001)
	h.r.load(0, encVUnary(0x33, pRow(1, 0, 0), sReg(0, 0, 0), 1)) // vh2f.p
	h.run(1)
	got := getVec(h, pRow(1, 0, 0), 2)
	checkApprox(t, "vh2f denormal", got[0], 5.9604645e-08, 1e-6)
	if got[1] != 0 {
		t.Errorf("vh2f denormal lane1 = %v, want 0", got[1])
	}
	// vf2h pair and single shapes.
	h = newHarness()
	setVec(h, pRow(0, 0, 0), 2, 1.0, -2.5)
	h.r.load(0, encVUnary(0x32, sReg(1, 0, 0), pRow(0, 0, 0), 1)) // vf2h.p
	h.run(1)
	if got := h.c.VSingle(lin(1, 0, 0)); got != 0xC1003C00 {
		t.Errorf("vf2h.p = %#08x, want 0xC1003C00", got)
	}
}

func TestVFPUIntConversions(t *testing.T) {
	run := func(variant uint32, vals ...float32) []uint32 {
		h := newHarness()
		setVec(h, qRow(0, 0), 4, vals...)
		h.r.load(0, encVF2I(variant, 0, qRow(1, 0), qRow(0, 0), 3))
		h.run(1)
		idx := vecIndices(qRow(1, 0), 4)
		var out []uint32
		for i := 0; i < len(vals); i++ {
			out = append(out, h.c.VSingle(idx[i]))
		}
		return out
	}
	nan := math.Float32frombits(canonicalNaN)
	if got := run(0, 2.5, -2.5, 3.5, nan); got[0] != 2 || got[1] != 0xFFFFFFFE || got[2] != 4 || got[3] != 0 {
		t.Errorf("vf2in = %v, want [2 -2 4 0]", got)
	}
	if got := run(1, 2.5, -2.5, 3.5, nan); got[0] != 2 || got[1] != 0xFFFFFFFE || got[2] != 3 || got[3] != 0 {
		t.Errorf("vf2iz = %v, want [2 -2 3 0]", got)
	}
	if got := run(2, 2.5, -2.5, 3.5, nan); got[0] != 3 || got[1] != 0 || got[2] != 4 || got[3] != 0 {
		t.Errorf("vf2iu = %v, want [3 0 4 0]", got)
	}
	if got := run(3, 2.5, -2.5, 3.5, nan); got[0] != 2 || got[1] != 0xFFFFFFFD || got[2] != 3 || got[3] != 0 {
		t.Errorf("vf2id = %v, want [2 -3 3 0]", got)
	}
	if got := run(0, 1e30); got[0] != 0x7FFFFFFF {
		t.Errorf("vf2in overflow = %#08x, want 0x7FFFFFFF", got[0])
	}
	if got := run(0, -1e30); got[0] != 0x80000000 {
		t.Errorf("vf2in underflow = %#08x, want 0x80000000", got[0])
	}
	// vi2f
	h := newHarness()
	idx := vecIndices(qRow(0, 0), 4)
	for i, v := range []int32{-2, 0, 3, 100} {
		h.c.SetVSingle(idx[i], uint32(v))
	}
	h.r.load(0, encVI2F(0, qRow(1, 0), qRow(0, 0), 3))
	h.run(1)
	checkVec(t, "vi2f", getVec(h, qRow(1, 0), 4), -2, 0, 3, 100)
}

func TestVFPUDotCrossScalCmp(t *testing.T) {
	h := newHarness()
	setVec(h, qRow(0, 0), 4, 1, 2, 3, 4)
	setVec(h, qRow(0, 1), 4, 5, 6, 7, 8)
	h.r.load(0,
		encVV(0x19, 1, sReg(1, 0, 0), qRow(0, 0), qRow(0, 1), 3), // vdot
		encVV(0x19, 4, sReg(1, 0, 1), qRow(0, 0), qRow(0, 1), 3), // vhdp
	)
	h.run(2)
	checkVec(t, "vdot", []float32{fv(h.c.VSingle(lin(1, 0, 0)))}, 70)
	checkVec(t, "vhdp", []float32{fv(h.c.VSingle(lin(1, 0, 1)))}, 46)
	// vcrsp: basis vectors and a general case.
	h = newHarness()
	setVec(h, tRow(0, 0), 3, 1, 0, 0)
	setVec(h, tRow(0, 1), 3, 0, 1, 0)
	h.r.load(0, encMatrix(5, tRow(1, 0), tRow(0, 0), tRow(0, 1), 2))
	h.run(1)
	checkVec(t, "vcrsp basis", getVec(h, tRow(1, 0), 3), 0, 0, 1)
	h = newHarness()
	setVec(h, tRow(0, 0), 3, 2, 3, 4)
	setVec(h, tRow(0, 1), 3, 5, 6, 7)
	h.r.load(0, encMatrix(5, tRow(1, 0), tRow(0, 0), tRow(0, 1), 2))
	h.run(1)
	checkVec(t, "vcrsp general", getVec(h, tRow(1, 0), 3), -3, 6, -3)
	// vscl
	h = newHarness()
	setVec(h, qRow(0, 0), 4, 1, -2, 3, -4)
	setVec(h, sReg(0, 3, 3), 1, 2.5)
	h.r.load(0, encVV(0x19, 2, qRow(1, 0), qRow(0, 0), sReg(0, 3, 3), 3))
	h.run(1)
	checkVec(t, "vscl", getVec(h, qRow(1, 0), 4), 2.5, -5, 7.5, -10)
	// vsge and vslt, with a NaN lane giving 0.
	h = newHarness()
	nan := math.Float32frombits(canonicalNaN)
	setVec(h, qRow(0, 0), 4, 1, 5, nan, 2)
	setVec(h, qRow(0, 1), 4, 2, 5, 1, 2)
	h.r.load(0,
		encVV(0x1B, 6, qRow(1, 0), qRow(0, 0), qRow(0, 1), 3),
		encVV(0x1B, 7, qRow(1, 1), qRow(0, 0), qRow(0, 1), 3),
	)
	h.run(2)
	checkVec(t, "vsge", getVec(h, qRow(1, 0), 4), 0, 1, 0, 1)
	checkVec(t, "vslt", getVec(h, qRow(1, 1), 4), 1, 0, 0, 0)
}

func TestVFPUMatrixOps(t *testing.T) {
	setMat := func(h *harness, m int, vals [4][4]float32) {
		for i := 0; i < 4; i++ {
			for j := 0; j < 4; j++ {
				h.c.SetVSingle(lin(m, i, j), fb(vals[i][j]))
			}
		}
	}
	// vmmul: the VS field carries the matrix operand with its
	// bit 5 inverted by the decoder, so the field for matrix 1
	// is the matrix 3 field. B is the VT matrix operand.
	h := newHarness()
	setMat(h, 0, [4][4]float32{{0, 1, 0, 0}, {0, 0, 1, 0}, {0, 0, 0, 1}, {1, 0, 0, 0}})
	setMat(h, 1, [4][4]float32{{2, 0, 0, 0}, {0, 3, 0, 0}, {0, 0, 4, 0}, {0, 0, 0, 5}})
	h.r.load(0, encMatrix(0, qRow(2, 0), qRow(1, 0)^0x20, qRow(0, 0), 3)) // vmmul VD=M2, VS=M1, VT=M0
	h.run(1)
	want := [4][4]float32{{0, 2, 0, 0}, {0, 0, 3, 0}, {0, 0, 0, 4}, {5, 0, 0, 0}}
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			if got := fv(h.c.VSingle(lin(2, i, j))); got != want[i][j] {
				t.Errorf("vmmul [%d][%d] = %v, want %v", i, j, got, want[i][j])
			}
		}
	}
	// vmscl: the scalar is the VT single at field 1 (matrix 0,
	// row 0, col 1).
	h = newHarness()
	setMat(h, 1, [4][4]float32{{1, 2, 3, 4}, {5, 6, 7, 8}, {9, 10, 11, 12}, {13, 14, 15, 16}})
	h.c.SetVSingle(lin(0, 0, 1), fb(2))
	h.r.load(0, encMatrix(4, qRow(2, 0), qRow(1, 0)^0x20, 1, 3)) // vmscl
	h.run(1)
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			want := float32((i*4 + j + 1) * 2)
			if got := fv(h.c.VSingle(lin(2, i, j))); got != want {
				t.Errorf("vmscl [%d][%d] = %v, want %v", i, j, got, want)
			}
		}
	}
	// Matrix vmov: group 7, F=0, source copied raw.
	h = newHarness()
	setMat(h, 1, [4][4]float32{{1, 2, 3, 4}, {5, 6, 7, 8}, {9, 10, 11, 12}, {13, 14, 15, 16}})
	h.r.load(0, encMatrixF(0, qRow(3, 0), qRow(1, 0), 3))
	h.run(1)
	if got := fv(h.c.VSingle(lin(3, 2, 1))); got != 10 {
		t.Errorf("vmov matrix [2][1] = %v, want 10", got)
	}
}

func TestVFPUPrefixes(t *testing.T) {
	// Swizzle reverse: vadd with spfx [w,z,y,x] and a zero VT.
	h := newHarness()
	setVec(h, qRow(0, 0), 4, 1, 2, 3, 4)
	setVec(h, qRow(2, 1), 4, 0, 0, 0, 0)
	h.r.load(0,
		encVPrefix(0, 0x0123), // fields: lane0 w, lane1 z, lane2 y, lane3 x
		encVV(0x18, 0, qRow(1, 0), qRow(0, 0), qRow(2, 1), 3),
		encVV(0x18, 0, qRow(1, 1), qRow(0, 0), qRow(2, 1), 3), // prefix consumed: plain
	)
	h.run(3)
	checkVec(t, "swizzle reverse", getVec(h, qRow(1, 0), 4), 4, 3, 2, 1)
	checkVec(t, "prefix one shot", getVec(h, qRow(1, 1), 4), 1, 2, 3, 4)
	// Abs on some lanes, neg on others (both bits together on one
	// lane would select a constant instead).
	h = newHarness()
	setVec(h, qRow(0, 0), 4, -2, 3, -4, 5)
	setVec(h, qRow(2, 1), 4, 0, 0, 0, 0)
	// lane0: abs x (4|0), lane1: neg y (8|1), lane2: abs z (4|2), lane3: neg w (8|3)
	imm := uint32(4) | uint32(9)<<4 | uint32(6)<<8 | uint32(11)<<12
	h.r.load(0,
		encVPrefix(0, imm),
		encVV(0x18, 0, qRow(1, 0), qRow(0, 0), qRow(2, 1), 3),
	)
	h.run(2)
	checkVec(t, "abs and neg", getVec(h, qRow(1, 0), 4), 2, -3, 4, -5)
	// Constant: abs and neg together, swizzle 1, bank bit clear
	// gives constant index 1 = 1.0.
	h = newHarness()
	setVec(h, sReg(0, 0, 0), 1, 99)
	setVec(h, sReg(2, 0, 1), 1, 0)
	h.r.load(0,
		encVPrefix(0, 0xD), // lane0 field: swizzle 1, abs, neg
		encVV(0x18, 0, sReg(1, 0, 0), sReg(0, 0, 0), sReg(2, 0, 1), 0),
	)
	h.run(2)
	checkVec(t, "prefix constant 1", getVec(h, sReg(1, 0, 0), 1), 1)
	// Bank bit set: constant index 5 = 1/6.
	h = newHarness()
	setVec(h, sReg(0, 0, 0), 1, 99)
	setVec(h, sReg(2, 0, 1), 1, 0)
	h.r.load(0,
		encVPrefix(0, 0x1000D),
		encVV(0x18, 0, sReg(1, 0, 0), sReg(0, 0, 0), sReg(2, 0, 1), 0),
	)
	h.run(2)
	checkApprox(t, "prefix constant 1/6", fv(h.c.VSingle(lin(1, 0, 0))), 1.0/6.0, 1e-6)
}

func TestVFPUDestPrefix(t *testing.T) {
	// Mask lane 1.
	h := newHarness()
	setVec(h, qRow(0, 0), 4, 10, 20, 30, 40)
	setVec(h, qRow(2, 1), 4, 1, 1, 1, 1)
	setVec(h, qRow(1, 0), 4, 7, 7, 7, 7)
	h.r.load(0,
		encVPrefix(2, 1<<5), // dpfx: lane1 mask bit at 3*1+2
		encVV(0x18, 0, qRow(1, 0), qRow(0, 0), qRow(2, 1), 3),
	)
	h.run(2)
	checkVec(t, "dpfx mask", getVec(h, qRow(1, 0), 4), 11, 7, 31, 41)
	// Saturate to [0,1].
	h = newHarness()
	setVec(h, sReg(0, 0, 0), 1, 1.5)
	setVec(h, sReg(2, 0, 1), 1, 0)
	h.r.load(0,
		encVPrefix(2, 1), // lane0 sat = 1
		encVV(0x18, 0, sReg(1, 0, 0), sReg(0, 0, 0), sReg(2, 0, 1), 0),
	)
	h.run(2)
	checkVec(t, "dpfx sat01", getVec(h, sReg(1, 0, 0), 1), 1)
	// Saturate to [-1,1].
	h = newHarness()
	setVec(h, sReg(0, 0, 0), 1, -1.5)
	setVec(h, sReg(2, 0, 1), 1, 0)
	h.r.load(0,
		encVPrefix(2, 2), // lane0 sat = 2
		encVV(0x18, 0, sReg(1, 0, 0), sReg(0, 0, 0), sReg(2, 0, 1), 0),
	)
	h.run(2)
	checkVec(t, "dpfx sat -1..1", getVec(h, sReg(1, 0, 0), 1), -1)
}

func TestVFPUReplSplt(t *testing.T) {
	h := newHarness()
	setVec(h, sReg(0, 1, 2), 1, 9.5)
	setVec(h, qRow(0, 0), 4, 1, 2, 3, 4)
	h.r.load(0,
		encVUnary(0x08, qRow(1, 0), sReg(0, 1, 2), 3), // vrepl.q
		encVUnary(0x09, qRow(1, 1), qRow(0, 0), 3),    // vsplt: VS field low bits are the quad reg; lane from field
	)
	h.run(2)
	checkVec(t, "vrepl", getVec(h, qRow(1, 0), 4), 9.5, 9.5, 9.5, 9.5)
	// vsplt uses lane = VS & 3. qRow(0,0) field is 0, so lane 0.
	checkVec(t, "vsplt lane0", getVec(h, qRow(1, 1), 4), 1, 1, 1, 1)
	// vsplt with a field whose low bits select lane 2: use a pair
	// style field value 2 as the VS field of a quad op.
	h = newHarness()
	setVec(h, qRow(0, 0), 4, 1, 2, 3, 4)
	h.r.load(0, encVUnary(0x09, qRow(1, 0), 2, 3))
	h.run(1)
	// Field 2 decodes as single row 0 col 2 for the padded read;
	// the splat lane is 2, giving the third lane of the padded
	// source read through that field. The padded read of field 2
	// as a quad gives lanes from row 0 (since low bit 0): values
	// 1,2,3,4, so lane 2 is 3.
	checkVec(t, "vsplt lane2", getVec(h, qRow(1, 0), 4), 3, 3, 3, 3)
}

func TestVFPUCmpAndBC2(t *testing.T) {
	// GT is condition 7 in the published table.
	h := newHarness()
	setVec(h, qRow(0, 0), 4, 1, 2, 3, 4)
	setVec(h, qRow(4, 1), 4, 0, 9, 2, 0)
	h.r.load(0, encVCmp(qRow(0, 0), qRow(4, 1), 3, 7)) // vcmp.q GT
	h.run(1)
	if got := h.c.VFPUCC(); got != 0x1D {
		t.Errorf("vcmp GT cc = %#x, want 0x1D (lanes 0,2,3 plus any)", got)
	}
	// LT against a larger VT gives all lanes true, so the any
	// and all bits both set.
	h = newHarness()
	setVec(h, qRow(0, 0), 4, 1, 2, 3, 4)
	setVec(h, qRow(4, 1), 4, 9, 9, 9, 9)
	h.r.load(0, encVCmp(qRow(0, 0), qRow(4, 1), 3, 2)) // vcmp.q LT
	h.run(1)
	if got := h.c.VFPUCC(); got != 0x3F {
		t.Errorf("vcmp LT cc = %#x, want 0x3F (all lanes plus any plus all)", got)
	}
	// BC2T taken: cc bit 0 set via MTVC.
	h = newHarness()
	h.r.load(0,
		encI(0x09, 0, 8, 1),    // 0: addiu r8, r0, 1
		encMTVC(8, 131),        // 4: mtvc r8, cc
		encBC2(true, false, 2), // 8: bc2t +2 -> 20
		encI(0x09, 0, 9, 111),  // 12: delay slot
		encI(0x09, 0, 9, 222),  // 16: skipped
		encI(0x09, 0, 10, 7),   // 20: target
	)
	h.run(5)
	if h.c.Reg(9) != 111 || h.c.Reg(10) != 7 {
		t.Errorf("bc2t taken: r9=%d r10=%d, want 111/7", h.c.Reg(9), h.c.Reg(10))
	}
	// BC2F not taken when cc bit 0 is set.
	h = newHarness()
	h.r.load(0,
		encI(0x09, 0, 8, 1),
		encMTVC(8, 131),
		encBC2(false, false, 2), // 8: bc2f +2 -> 20, not taken
		encI(0x09, 0, 9, 111),   // 12: delay slot
		encI(0x09, 0, 9, 222),   // 16: fallthrough
		encI(0x09, 0, 10, 7),    // 20
	)
	h.run(5)
	if h.c.Reg(9) != 222 {
		t.Errorf("bc2f not taken: r9=%d, want 222", h.c.Reg(9))
	}
}

func TestVFPUCmov(t *testing.T) {
	setup := func() *harness {
		h := newHarness()
		setVec(h, qRow(0, 0), 4, 10, 20, 30, 40) // source
		setVec(h, qRow(1, 0), 4, 1, 2, 3, 4)     // dest current
		h.c.SetReg(8, 0x5)                       // cc lanes 0 and 2
		return h
	}
	h := setup()
	h.r.load(0,
		encI(0x09, 0, 8, 5),
		encMTVC(8, 131),
		encVCmov(false, 6, qRow(1, 0), qRow(0, 0), 3), // vcmovt, per lane
	)
	h.run(3)
	checkVec(t, "vcmovt per lane", getVec(h, qRow(1, 0), 4), 10, 2, 30, 4)
	h = setup()
	h.r.load(0,
		encI(0x09, 0, 8, 5),
		encMTVC(8, 131),
		encVCmov(true, 6, qRow(1, 0), qRow(0, 0), 3), // vcmovf, per lane
	)
	h.run(3)
	checkVec(t, "vcmovf per lane", getVec(h, qRow(1, 0), 4), 1, 20, 3, 40)
}

func TestVFPULoadStore(t *testing.T) {
	// LV.S and SV.S round trip.
	h := newHarness()
	h.r.Write32(0x200, fb(3.5))
	h.c.SetReg(8, 0x200)
	h.r.load(0,
		encVLoad(0x32, 8, sReg(1, 2, 3), 0, 0), // lv.s
		encVLoad(0x3A, 8, sReg(1, 2, 3), 4, 0), // sv.s to 0x204
	)
	h.run(2)
	if got := fv(h.c.VSingle(lin(1, 2, 3))); got != 3.5 {
		t.Errorf("lv.s = %v, want 3.5", got)
	}
	if got := fv(h.r.Read32(0x204)); got != 3.5 {
		t.Errorf("sv.s stored %v, want 3.5", got)
	}
	// LV.Q and SV.Q round trip.
	h = newHarness()
	setVec(h, qRow(0, 0), 4, 1, 2, 3, 4)
	h.c.SetReg(8, 0x200)
	h.r.load(0,
		encVLoad(0x3E, 8, qRow(0, 0), 0, 0), // sv.q
		encVLoad(0x36, 8, qRow(1, 0), 0, 0), // lv.q
	)
	h.run(2)
	checkVec(t, "lv.q round trip", getVec(h, qRow(1, 0), 4), 1, 2, 3, 4)
	for i := 0; i < 4; i++ {
		if got := fv(h.r.Read32(0x200 + uint32(4*i))); got != float32(i+1) {
			t.Errorf("sv.q mem word %d = %v", i, got)
		}
	}
}

func TestVFPULvlLvr(t *testing.T) {
	preload := func(h *harness) {
		for i := 0; i < 4; i++ {
			var w uint32
			for k := 0; k < 4; k++ {
				w |= uint32(0xA0+i*4+k) << uint(8*k)
			}
			h.c.SetVSingle(lin(0, 0, i), w)
		}
	}
	quadBytes := func(h *harness) [16]byte {
		var out [16]byte
		idx := vecIndices(qRow(0, 0), 4)
		for i := 0; i < 16; i++ {
			out[i] = uint8(h.c.VSingle(idx[i/4]) >> uint(8*(i%4)))
		}
		return out
	}
	// LVL.Q at base 0x100 plus offset 20: a = 4, first 12 bytes
	// come from memory, last 4 keep the preload.
	h := newHarness()
	preload(h)
	for i := 0; i < 12; i++ {
		h.r.Write8(0x100+20+uint32(i), uint8(0x10+i))
	}
	h.c.SetReg(8, 0x100)
	h.r.load(0, encVLoad(0x36, 8, qRow(0, 0), 20, 2))
	h.run(1)
	b := quadBytes(h)
	for i := 0; i < 12; i++ {
		if b[i] != uint8(0x10+i) {
			t.Errorf("lvl byte %d = %#x, want %#x", i, b[i], 0x10+i)
		}
	}
	for i := 12; i < 16; i++ {
		if b[i] != uint8(0xA0+i) {
			t.Errorf("lvl kept byte %d = %#x, want %#x", i, b[i], 0xA0+i)
		}
	}
	// LVR.Q at the same address: a = 4, bytes 11..15 come from
	// memory at 0x114..0x118, first 11 keep the preload.
	h = newHarness()
	preload(h)
	for i := 0; i < 5; i++ {
		h.r.Write8(0x100+16+uint32(i), uint8(0x20+i))
	}
	h.c.SetReg(8, 0x100)
	h.r.load(0, encVLoad(0x36, 8, qRow(0, 0), 20, 3))
	h.run(1)
	b = quadBytes(h)
	for i := 0; i < 11; i++ {
		if b[i] != uint8(0xA0+i) {
			t.Errorf("lvr kept byte %d = %#x, want %#x", i, b[i], 0xA0+i)
		}
	}
	for i := 11; i < 16; i++ {
		if b[i] != uint8(0x20+i-11) {
			t.Errorf("lvr byte %d = %#x, want %#x", i, b[i], 0x20+i-11)
		}
	}
}

func TestVFPURnd(t *testing.T) {
	prog := func() *harness {
		h := newHarness()
		h.r.load(0,
			encI(0x09, 0, 8, 12345),              // addiu r8, r0, 12345
			encMTVC(8, 136),                      // seed RCX0
			encVUnary(0x22, sReg(1, 0, 0), 0, 0), // vrndf1
			encVUnary(0x22, sReg(1, 0, 1), 0, 0), // vrndf1 again
		)
		h.run(4)
		return h
	}
	a := prog()
	v0 := fv(a.c.VSingle(lin(1, 0, 0)))
	v1 := fv(a.c.VSingle(lin(1, 0, 1)))
	if v0 < 1 || v0 >= 2 || v1 < 1 || v1 >= 2 {
		t.Errorf("vrndf1 out of range: %v %v", v0, v1)
	}
	if v0 == v1 {
		t.Errorf("vrndf1 repeated a value: %v", v0)
	}
	b := prog()
	if fv(b.c.VSingle(lin(1, 0, 0))) != v0 || fv(b.c.VSingle(lin(1, 0, 1))) != v1 {
		t.Errorf("vrndf1 not deterministic for a fixed seed")
	}
}

func TestVFPUMovesAndCtrl(t *testing.T) {
	h := newHarness()
	h.c.SetReg(8, 0xDEADBEEF)
	h.r.load(0,
		encMTC2(8, sReg(2, 1, 1)), // mtc2 r8 -> VFPU single
		encMFC2(9, sReg(2, 1, 1)), // mfc2 r9 <- same
		encCFC2(10, 0),            // cfc2 r10, revision
		encI(0x09, 0, 11, 0x77),   // addiu r11, r0, 0x77
		encCTC2(11, 5),            // ctc2 r11 -> ctrl 5
		encCFC2(12, 5),            // cfc2 r12 <- ctrl 5
	)
	h.run(6)
	if got := h.c.Reg(9); got != 0xDEADBEEF {
		t.Errorf("mfc2 = %#08x, want 0xDEADBEEF", got)
	}
	if got := h.c.Reg(10); got != 0x00003351 {
		t.Errorf("cfc2 revision = %#08x, want 0x00003351", got)
	}
	if got := h.c.Reg(12); got != 0x77 {
		t.Errorf("ctc2/cfc2 = %#08x, want 0x77", got)
	}
	// MFVC reads back the source prefix set by vpfxs.
	h = newHarness()
	h.r.load(0,
		encVPrefix(0, 0x0123),
		encMFVC(8, 128),
		encMFVC(9, 132), // always all ones
	)
	h.run(3)
	if got := h.c.Reg(8); got != 0x0123 {
		t.Errorf("mfvc spfx = %#08x, want 0x0123", got)
	}
	if got := h.c.Reg(9); got != 0xFFFFFFFF {
		t.Errorf("mfvc 132 = %#08x, want all ones", got)
	}
}

func TestVFPUUnimplementedFaults(t *testing.T) {
	h := newHarness()
	// F=0x30 is not an implemented unary function.
	h.r.load(0, encVUnary(0x30, qRow(0, 0), qRow(0, 0), 3))
	h.run(1)
	if h.c.VFPUFaultCount() != 1 {
		t.Errorf("VFPUFaultCount = %d, want 1", h.c.VFPUFaultCount())
	}
	if excCode(h.c) != excRI {
		t.Errorf("ExcCode = %d, want %d (RI)", excCode(h.c), excRI)
	}
	// A bare COP2 move and LV.S execute cleanly now.
	h = newHarness()
	h.r.load(0, 0x12<<26, encI(0x32, 0, 8, 0))
	h.run(2)
	if h.c.VFPUFaultCount() != 0 || excCode(h.c) != 0 {
		t.Errorf("implemented encodings faulted: count=%d code=%d", h.c.VFPUFaultCount(), excCode(h.c))
	}
}

func TestVFPUSerialize(t *testing.T) {
	h := newHarness()
	setVec(h, qRow(3, 2), 4, 1, 2, 3, 4)
	h.c.cc = 0x15
	snap := h.c.Serialize()
	h2 := newHarness()
	if err := h2.c.Deserialize(snap); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	checkVec(t, "serialized vregs", getVec(h2, qRow(3, 2), 4), 1, 2, 3, 4)
	if h2.c.VFPUCC() != 0x15 {
		t.Errorf("serialized cc = %#x, want 0x15", h2.c.VFPUCC())
	}
}

func TestVFPUArithVTMatrix0And1(t *testing.T) {
	type opCase struct {
		name string
		word func(vd, vs, vt uint32) uint32
		want []float32
	}
	a := []float32{8, 9, 10, 12}
	b := []float32{2, 3, 4, 6}
	cases := []opCase{
		{"vadd.q", func(vd, vs, vt uint32) uint32 { return encVV(0x18, 0, vd, vs, vt, 3) }, []float32{10, 12, 14, 18}},
		{"vsub.q", func(vd, vs, vt uint32) uint32 { return encVV(0x18, 1, vd, vs, vt, 3) }, []float32{6, 6, 6, 6}},
		{"vmul.q", func(vd, vs, vt uint32) uint32 { return encVV(0x19, 0, vd, vs, vt, 3) }, []float32{16, 27, 40, 72}},
		{"vdiv.q", func(vd, vs, vt uint32) uint32 { return encVV(0x18, 7, vd, vs, vt, 3) }, []float32{4, 3, 2.5, 2}},
		{"vmin.q", func(vd, vs, vt uint32) uint32 { return encVV(0x1B, 2, vd, vs, vt, 3) }, []float32{2, 3, 4, 6}},
		{"vmax.q", func(vd, vs, vt uint32) uint32 { return encVV(0x1B, 3, vd, vs, vt, 3) }, []float32{8, 9, 10, 12}},
	}
	for _, vtMat := range []uint32{0, 1} {
		for _, tc := range cases {
			h := newHarness()
			setVec(h, qRow(2, 0), 4, a...)
			setVec(h, qRow(vtMat, 1), 4, b...)
			h.r.load(0, tc.word(qRow(3, 0), qRow(2, 0), qRow(vtMat, 1)))
			h.run(1)
			if h.c.VFPUFaultCount() != 0 {
				t.Errorf("%s with VT in matrix %d: VFPUFaultCount = %d, want 0", tc.name, vtMat, h.c.VFPUFaultCount())
				continue
			}
			checkVec(t, tc.name, getVec(h, qRow(3, 0), 4), tc.want...)
		}
		// vdot.q with VT in this matrix.
		h := newHarness()
		setVec(h, qRow(2, 0), 4, a...)
		setVec(h, qRow(vtMat, 1), 4, b...)
		h.r.load(0, encVV(0x19, 1, sReg(3, 0, 0), qRow(2, 0), qRow(vtMat, 1), 3))
		h.run(1)
		if h.c.VFPUFaultCount() != 0 {
			t.Errorf("vdot.q with VT in matrix %d: VFPUFaultCount = %d, want 0", vtMat, h.c.VFPUFaultCount())
		}
		checkVec(t, "vdot.q", []float32{fv(h.c.VSingle(lin(3, 0, 0)))}, 155)
	}
}

func TestVFPUAnchorWordVAdd(t *testing.T) {
	// Published anchor for vadd.q with zero operands is
	// 0x60008080; adding operand fields must still add, and
	// must not be taken for an MFC2.
	h := newHarness()
	setVec(h, qRow(0, 0), 4, 1, 2, 3, 4)
	setVec(h, qRow(2, 1), 4, 10, 20, 30, 40)
	h.c.SetReg(9, 0xCAFEBABE)
	word := uint32(0x60008080) | qRow(2, 1)<<16 | qRow(0, 0)<<8 | qRow(1, 0)
	h.r.load(0, word)
	h.run(1)
	checkVec(t, "anchor vadd.q", getVec(h, qRow(1, 0), 4), 11, 22, 33, 44)
	if h.c.VFPUFaultCount() != 0 {
		t.Errorf("anchor vadd faulted: count = %d", h.c.VFPUFaultCount())
	}
	if got := h.c.Reg(9); got != 0xCAFEBABE {
		t.Errorf("anchor vadd touched r9: %#08x (decoded as MFC2?)", got)
	}
}

func TestVFPUMFC2Genuine(t *testing.T) {
	h := newHarness()
	h.c.SetVSingle(lin(2, 1, 1), 0xDEADBEEF)
	h.r.load(0, encMFC2(9, sReg(2, 1, 1)))
	h.run(1)
	if got := h.c.Reg(9); got != 0xDEADBEEF {
		t.Errorf("mfc2 = %#08x, want 0xDEADBEEF", got)
	}
	if h.c.VFPUFaultCount() != 0 {
		t.Errorf("mfc2 faulted: count = %d", h.c.VFPUFaultCount())
	}
	// An unimplemented COP2 rs value faults as reserved.
	h = newHarness()
	h.r.load(0, 0x12<<26|1<<21)
	h.run(1)
	if h.c.VFPUFaultCount() != 1 || excCode(h.c) != excRI {
		t.Errorf("cop2 rs=1: count = %d code = %d, want 1/%d", h.c.VFPUFaultCount(), excCode(h.c), excRI)
	}
}

func TestVCMPCondCorrected(t *testing.T) {
	run := func(a, b []float32, cond uint32) uint32 {
		h := newHarness()
		setVec(h, qRow(0, 0), 4, a...)
		setVec(h, qRow(1, 0), 4, b...)
		h.r.load(0, encVCmp(qRow(0, 0), qRow(1, 0), 3, cond))
		h.run(1)
		return h.c.VFPUCC()
	}
	// cond 6 GE: lanes [F,T,T,T] plus the any bit.
	if got := run([]float32{1, 5, 5, 0}, []float32{2, 5, 4, 0}, 6); got != 0x1E {
		t.Errorf("vcmp GE cc = %#x, want 0x1E", got)
	}
	// cond 7 GT: lanes [F,F,T,F] plus the any bit.
	if got := run([]float32{1, 5, 5, 0}, []float32{2, 5, 4, 0}, 7); got != 0x14 {
		t.Errorf("vcmp GT cc = %#x, want 0x14", got)
	}
	inf := float32(math.Inf(1))
	nan := math.Float32frombits(canonicalNaN)
	// cond 10 EI: true for both infinities only.
	if got := run([]float32{inf, 1, nan, -inf}, []float32{0, 0, 0, 0}, 10); got != 0x19 {
		t.Errorf("vcmp EI cc = %#x, want 0x19", got)
	}
	// cond 11 ES: true for infinity or NaN.
	if got := run([]float32{inf, nan, 1, 0}, []float32{0, 0, 0, 0}, 11); got != 0x13 {
		t.Errorf("vcmp ES cc = %#x, want 0x13", got)
	}
	// cond 15 NS: true only for finite values.
	if got := run([]float32{inf, nan, 1, 0}, []float32{0, 0, 0, 0}, 15); got != 0x1C {
		t.Errorf("vcmp NS cc = %#x, want 0x1C", got)
	}
}

func TestVFPUMFVCRevAndRNGSeed(t *testing.T) {
	h := newHarness()
	h.r.load(0, encMFVC(8, 135))
	h.run(1)
	if got := h.c.Reg(8); got != 0x00003351 {
		t.Errorf("mfvc REV = %#08x, want 0x00003351", got)
	}
	// MTVC to index 136 seeds RCX0; two runs seeded the same
	// way produce the same vrndi sequence, a different seed
	// produces a different one.
	seq := func(seed uint32) (uint32, uint32) {
		h := newHarness()
		h.c.SetReg(8, seed)
		h.r.load(0,
			encMTVC(8, 136),
			encVUnary(0x21, sReg(1, 0, 0), 0, 0), // vrndi.s
			encVUnary(0x21, sReg(1, 0, 1), 0, 0),
		)
		h.run(3)
		return h.c.VSingle(lin(1, 0, 0)), h.c.VSingle(lin(1, 0, 1))
	}
	a0, a1 := seq(12345)
	b0, b1 := seq(12345)
	if a0 != b0 || a1 != b1 {
		t.Errorf("vrndi not deterministic: (%#x, %#x) vs (%#x, %#x)", a0, a1, b0, b1)
	}
	if a0 == a1 {
		t.Errorf("vrndi repeated a value: %#x", a0)
	}
	c0, _ := seq(999)
	if c0 == a0 {
		t.Errorf("vrndi seed had no effect: %#x", c0)
	}
	// vrnds.s seeds from the raw VS register bits and lands on
	// the same sequence as the MTVC seed.
	h = newHarness()
	h.c.SetVSingle(lin(0, 0, 0), 12345)
	h.r.load(0,
		encVUnary(0x20, 0, sReg(0, 0, 0), 0), // vrnds.s
		encVUnary(0x21, sReg(1, 0, 0), 0, 0),
	)
	h.run(2)
	if got := h.c.VSingle(lin(1, 0, 0)); got != a0 {
		t.Errorf("vrnds/vrndi = %#x, want %#x (MTVC seeded)", got, a0)
	}
	// MTVC to the read only REV index is ignored.
	h = newHarness()
	h.c.SetReg(8, 0xFFFF)
	h.r.load(0, encMTVC(8, 135), encMFVC(9, 135))
	h.run(2)
	if got := h.c.Reg(9); got != 0x00003351 {
		t.Errorf("mfvc REV after mtvc = %#08x, want 0x00003351", got)
	}
}

func TestVFPUCmovCarveOutAndVI2FScale(t *testing.T) {
	// vcmovt through the group 5 carve out (field 0x20|sel).
	h := newHarness()
	setVec(h, qRow(0, 0), 4, 10, 20, 30, 40)
	setVec(h, qRow(1, 0), 4, 1, 2, 3, 4)
	h.r.load(0,
		encI(0x09, 0, 8, 5),
		encMTVC(8, 131),
		encVCmov(false, 6, qRow(1, 0), qRow(0, 0), 3),
	)
	h.run(3)
	checkVec(t, "vcmovt carve out", getVec(h, qRow(1, 0), 4), 10, 2, 30, 4)
	// vi2f with scale 3 (field 3, outside the carve out range)
	// still converts: result is int * 2^-3.
	h = newHarness()
	idx := vecIndices(qRow(0, 0), 4)
	for i, v := range []int32{16, -8, 3, 100} {
		h.c.SetVSingle(idx[i], uint32(v))
	}
	h.r.load(0, encVI2F(3, qRow(1, 0), qRow(0, 0), 3))
	h.run(1)
	checkVec(t, "vi2f scale 3", getVec(h, qRow(1, 0), 4), 2, -1, 0.375, 12.5)
	// vf2in with scale 1 doubles before rounding to nearest.
	h = newHarness()
	setVec(h, qRow(0, 0), 4, 1.25, -1.25, 0.4, 2.5)
	h.r.load(0, encVF2I(0, 1, qRow(1, 0), qRow(0, 0), 3))
	h.run(1)
	idx = vecIndices(qRow(1, 0), 4)
	want := []uint32{2, 0xFFFFFFFE, 1, 5}
	for i, w := range want {
		if got := h.c.VSingle(idx[i]); got != w {
			t.Errorf("vf2in scale1 lane %d = %#08x, want %#08x", i, got, w)
		}
	}
}

func TestVFPUMatrixTransform(t *testing.T) {
	setMat := func(h *harness, m int, vals [4][4]float32) {
		for i := 0; i < 4; i++ {
			for j := 0; j < 4; j++ {
				h.c.SetVSingle(lin(m, i, j), fb(vals[i][j]))
			}
		}
	}
	diag := [4][4]float32{{1, 0, 0, 0}, {0, 2, 0, 0}, {0, 0, 3, 0}, {0, 0, 0, 4}}
	// vtfm4.q: full vector operand.
	h := newHarness()
	setMat(h, 1, diag)
	setVec(h, qRow(2, 0), 4, 1, 1, 1, 1)
	h.r.load(0, encMatrix(3, qRow(3, 0), qRow(1, 0)^0x20, qRow(2, 0), 3))
	h.run(1)
	checkVec(t, "vtfm4", getVec(h, qRow(3, 0), 4), 1, 2, 3, 4)
	// vhtfm4: size enum is triple; the last matrix column is
	// weighted by an implicit 1.0.
	h = newHarness()
	setMat(h, 1, diag)
	setVec(h, tRow(2, 0), 3, 5, 6, 7)
	h.r.load(0, encMatrix(3, qRow(3, 0), qRow(1, 0)^0x20, tRow(2, 0), 2))
	h.run(1)
	checkVec(t, "vhtfm4", getVec(h, qRow(3, 0), 4), 5, 12, 21, 4)
	// vtfm2.p and vhtfm2 under group 1.
	h = newHarness()
	setMat(h, 1, diag)
	setVec(h, pRow(2, 0, 0), 2, 3, 4)
	h.r.load(0, encMatrix(1, pRow(3, 0, 0), qRow(1, 0)^0x20, pRow(2, 0, 0), 1))
	h.run(1)
	checkVec(t, "vtfm2", getVec(h, pRow(3, 0, 0), 2), 3, 8)
	h = newHarness()
	setMat(h, 1, diag)
	setVec(h, sReg(2, 0, 0), 1, 3)
	h.r.load(0, encMatrix(1, pRow(3, 0, 0), qRow(1, 0)^0x20, sReg(2, 0, 0), 0))
	h.run(1)
	checkVec(t, "vhtfm2", getVec(h, pRow(3, 0, 0), 2), 3, 2)
	// Anchor words for the matrix fill group.
	h = newHarness()
	h.r.load(0, 0xF3838080, 0xF3868080) // vmidt.q M0, then vmzero.q M0
	h.run(1)
	if got := fv(h.c.VSingle(lin(0, 1, 1))); got != 1 {
		t.Errorf("vmidt.q [1][1] = %v, want 1", got)
	}
	h.run(1)
	for i := 0; i < 16; i++ {
		if got := h.c.VSingle(i); got != 0 {
			t.Errorf("vmzero.q word %d = %#08x, want 0", i, got)
		}
	}
}

func TestVFPUVsbnVcstPack(t *testing.T) {
	// vsbn.s: exponent of 1.5 (field 127) is replaced by the
	// biased value for integer 1, giving 3.0.
	h := newHarness()
	setVec(h, sReg(0, 0, 0), 1, 1.5)
	h.c.SetVSingle(lin(0, 0, 1), 1)
	h.r.load(0, encVV(0x18, 2, sReg(1, 0, 0), sReg(0, 0, 0), sReg(0, 0, 1), 0))
	h.run(1)
	checkVec(t, "vsbn", getVec(h, sReg(1, 0, 0), 1), 3)
	// vcst.s with constant index 2 loads 2.0.
	h = newHarness()
	h.r.load(0, encVUnary(0x62, sReg(1, 0, 0), 0, 0))
	h.run(1)
	checkVec(t, "vcst 2", getVec(h, sReg(1, 0, 0), 1), 2)
	// vi2uc.q packs top bytes, negatives clamped to zero.
	h = newHarness()
	idx := vecIndices(qRow(0, 0), 4)
	for i, v := range []uint32{0x12345678, 0xFFFFFFFB, 0x7F000000, 0x01000000} {
		h.c.SetVSingle(idx[i], v)
	}
	h.r.load(0, encVUnary(0x3C, sReg(1, 0, 0), qRow(0, 0), 3))
	h.run(1)
	if got := h.c.VSingle(lin(1, 0, 0)); got != 0x02FE0024 {
		t.Errorf("vi2uc = %#08x, want 0x02FE0024", got)
	}
	// vi2s.p packs top halfwords.
	h = newHarness()
	idx = vecIndices(pRow(0, 0, 0), 2)
	h.c.SetVSingle(idx[0], 0x12345678)
	h.c.SetVSingle(idx[1], 0x9ABCDEF0)
	h.r.load(0, encVUnary(0x3F, sReg(1, 0, 0), pRow(0, 0, 0), 1))
	h.run(1)
	if got := h.c.VSingle(lin(1, 0, 0)); got != 0x9ABC1234 {
		t.Errorf("vi2s = %#08x, want 0x9ABC1234", got)
	}
	// vsgn.
	h = newHarness()
	setVec(h, qRow(0, 0), 4, -2.5, 0, 7, -0.001)
	h.r.load(0, encVUnary(0x4A, qRow(1, 0), qRow(0, 0), 3))
	h.run(1)
	checkVec(t, "vsgn", getVec(h, qRow(1, 0), 4), -1, 0, 1, -1)
}
