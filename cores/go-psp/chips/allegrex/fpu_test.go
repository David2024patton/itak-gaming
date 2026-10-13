package allegrex

import "testing"

// FPU test harness: same as newHarness plus CU1 enabled.
func newFPUHarness() *harness {
	h := newHarness()
	h.c.cp0[12][0] = 0x20400000 // BEV | CU1
	return h
}

// encFP builds a COP1 fmt S instruction: ft, fs, fd, funct.
func encFP(ft, fs, fd, fn uint32) uint32 {
	return 0x11<<26 | 0x10<<21 | ft<<16 | fs<<11 | fd<<6 | fn
}
func encMFC1(rt, fs uint32) uint32 { return 0x11<<26 | 0<<21 | rt<<16 | fs<<11 }
func encMTC1(rt, fs uint32) uint32 { return 0x11<<26 | 4<<21 | rt<<16 | fs<<11 }
func encCFC1(rt, fs uint32) uint32 { return 0x11<<26 | 2<<21 | rt<<16 | fs<<11 }
func encCTC1(rt, fs uint32) uint32 { return 0x11<<26 | 6<<21 | rt<<16 | fs<<11 }

// encBC1 builds a BC1 branch: cc in bits 20..18, nd (likely)
// bit 17, tf bit 16 (the hardware layout; an earlier version of
// this helper had tf and nd swapped to match a swapped decoder).
func encBC1(cc uint32, tf bool, off uint32) uint32 {
	w := 0x11<<26 | 8<<21 | cc<<18 | (off & 0xFFFF)
	if tf {
		w |= 1 << 16
	}
	return w
}

// IEEE 754 bit patterns used below.
const (
	fOne   = 0x3F800000 // 1.0
	fOne5  = 0x3FC00000 // 1.5
	fTwo   = 0x40000000 // 2.0
	fTwo25 = 0x40100000 // 2.25
	fThree = 0x40400000 // 3.0
)

func TestFPUArithmetic(t *testing.T) {
	h := newFPUHarness()
	h.c.SetFReg(1, fOne5)
	h.c.SetFReg(2, fTwo25)
	h.r.load(0,
		encFP(2, 1, 3, 0x00), // add.s f3, f1, f2 = 3.75
		encFP(2, 1, 4, 0x02), // mul.s f4, f1, f2 = 3.375
		encFP(2, 1, 5, 0x01), // sub.s f5, f1, f2 = -0.75
		encFP(1, 2, 6, 0x07), // neg.s f6, f2
		encFP(0, 5, 7, 0x05), // abs.s f7, f5
	)
	h.run(5)
	want := map[int]uint32{
		3: 0x40700000, // 3.75
		4: 0x40580000, // 3.375
		5: 0xBF400000, // -0.75
		6: 0xC0100000, // -2.25
		7: 0x3F400000, // 0.75
	}
	for reg, v := range want {
		if got := h.c.FReg(reg); got != v {
			t.Errorf("f%d = %#08x, want %#08x", reg, got, v)
		}
	}
}

func TestFPUDivSqrt(t *testing.T) {
	h := newFPUHarness()
	h.c.SetFReg(1, fOne)
	h.c.SetFReg(2, fThree)
	h.c.SetFReg(3, fTwo)
	h.r.load(0,
		encFP(2, 1, 4, 0x03), // div.s f4, f1, f2 = 1/3
		encFP(0, 3, 5, 0x04), // sqrt.s f5, f3 = sqrt(2)
	)
	if n := h.c.Step(); n != 12 {
		t.Errorf("div.s cycles = %d, want 12", n)
	}
	if got := h.c.FReg(4); got != 0x3EAAAAAB {
		t.Errorf("1/3 = %#08x, want 0x3EAAAAAB", got)
	}
	if n := h.c.Step(); n != 12 {
		t.Errorf("sqrt.s cycles = %d, want 12", n)
	}
	if got := h.c.FReg(5); got != 0x3FB504F3 {
		t.Errorf("sqrt(2) = %#08x, want 0x3FB504F3", got)
	}
}

func TestFPUSpecialValues(t *testing.T) {
	// Denormal input flushes to zero: denormal + 1.0 = 1.0.
	h := newFPUHarness()
	h.c.SetFReg(1, 0x00000001) // smallest denormal
	h.c.SetFReg(2, fOne)
	h.r.load(0, encFP(2, 1, 3, 0x00))
	h.run(1)
	if got := h.c.FReg(3); got != fOne {
		t.Errorf("denormal + 1.0 = %#08x, want 1.0", got)
	}
	// 1.0 / 0.0 = +Inf.
	h = newFPUHarness()
	h.c.SetFReg(1, fOne)
	h.c.SetFReg(2, 0)
	h.r.load(0, encFP(2, 1, 3, 0x03))
	h.run(1)
	if got := h.c.FReg(3); got != 0x7F800000 {
		t.Errorf("1/0 = %#08x, want +Inf", got)
	}
	// 0.0 / 0.0 = canonical NaN.
	h = newFPUHarness()
	h.c.SetFReg(1, 0)
	h.c.SetFReg(2, 0)
	h.r.load(0, encFP(2, 1, 3, 0x03))
	h.run(1)
	if got := h.c.FReg(3); got != 0x7FC00000 {
		t.Errorf("0/0 = %#08x, want canonical NaN", got)
	}
	// Denormal results follow the FCSR FS bit (pinned by the
	// pspautotests fpu matrix): with FS clear the product
	// 2^-70 * 2^-70 = 2^-140 keeps its denormal bits, with FS
	// set it flushes to signed zero.
	h = newFPUHarness()
	h.c.SetFReg(1, 0x1C800000) // 2^-70
	h.c.SetFReg(2, 0x1C800000) // 2^-70
	h.r.load(0, encFP(2, 1, 3, 0x02))
	h.run(1)
	if got := h.c.FReg(3); got != 0x00000200 {
		t.Errorf("denormal product (FS=0) = %#08x, want 0x00000200", got)
	}
	h = newFPUHarness()
	h.c.SetFCSR(0x01000000) // FS set
	h.c.SetFReg(1, 0x1C800000)
	h.c.SetFReg(2, 0x1C800000)
	h.r.load(0, encFP(2, 1, 3, 0x02))
	h.run(1)
	if got := h.c.FReg(3); got != 0 {
		t.Errorf("denormal product (FS=1) = %#08x, want flushed 0", got)
	}
	// A NaN operand propagates its own quieted bits (roundmode
	// matrix), it is not canonicalized.
	h = newFPUHarness()
	h.c.SetFReg(1, 0xFFC00000) // negative quiet NaN
	h.c.SetFReg(2, 0x3F800000) // 1.0
	h.r.load(0, encFP(2, 1, 3, 0x00))
	h.run(1)
	if got := h.c.FReg(3); got != 0xFFC00000 {
		t.Errorf("add.s NaN propagation = %#08x, want 0xffc00000", got)
	}
	h = newFPUHarness()
	h.c.SetFReg(1, 0x7F800001) // signaling NaN, payload 1
	h.c.SetFReg(2, 0x3F800000)
	h.r.load(0, encFP(2, 1, 3, 0x02))
	h.run(1)
	if got := h.c.FReg(3); got != 0x7FC00001 {
		t.Errorf("mul.s sNaN propagation = %#08x, want 0x7fc00001", got)
	}
}

func TestFPUConvertModes(t *testing.T) {
	// CVT.W.S of 2.5 under each FCSR rounding mode:
	// RN 2, RZ 2, RP 3, RM 2.
	modes := []struct {
		mode uint32
		want uint32
	}{
		{0, 2}, {1, 2}, {2, 3}, {3, 2},
	}
	for _, tc := range modes {
		h := newFPUHarness()
		h.c.fcr[31] = tc.mode
		h.c.SetFReg(1, 0x40200000)        // 2.5
		h.r.load(0, encFP(0, 1, 2, 0x24)) // cvt.w.s f2, f1
		h.run(1)
		if got := h.c.FReg(2); got != tc.want {
			t.Errorf("cvt.w.s 2.5 mode %d = %#08x, want %d", tc.mode, got, tc.want)
		}
	}
	// Explicit variants on -2.5.
	explicit := []struct {
		fn   uint32
		want uint32
	}{
		{0x0C, 0xFFFFFFFE}, // round.w.s: -2 (ties to even)
		{0x0D, 0xFFFFFFFE}, // trunc.w.s: -2
		{0x0E, 0xFFFFFFFE}, // ceil.w.s: -2
		{0x0F, 0xFFFFFFFD}, // floor.w.s: -3
	}
	for _, tc := range explicit {
		h := newFPUHarness()
		h.c.SetFReg(1, 0xC0200000) // -2.5
		h.r.load(0, encFP(0, 1, 2, tc.fn))
		h.run(1)
		if got := h.c.FReg(2); got != tc.want {
			t.Errorf("fn %#x of -2.5 = %#08x, want %#08x", tc.fn, got, tc.want)
		}
	}
	// CVT.W.S saturates by sign (Allegrex behavior, per the
	// pspautotests roundmode and fpu matrices): positive
	// overflow gives 0x7FFFFFFF, negative overflow gives
	// 0x80000000, and NaN of either sign gives 0x7FFFFFFF.
	for _, tc := range []struct {
		in, want uint32
	}{
		{0x7FC00000, 0x7FFFFFFF}, // positive NaN
		{0xFFC00000, 0x7FFFFFFF}, // negative NaN
		{0x4F000000, 0x7FFFFFFF}, // exactly 2^31
		{0x7F800000, 0x7FFFFFFF}, // +Inf
		{0xCF000001, 0x80000000}, // negative, out of range
		{0xFF800000, 0x80000000}, // -Inf
	} {
		h := newFPUHarness()
		h.c.SetFReg(1, tc.in)
		h.r.load(0, encFP(0, 1, 2, 0x24))
		h.run(1)
		if got := h.c.FReg(2); got != tc.want {
			t.Errorf("cvt.w.s %#08x = %#08x, want %#08x", tc.in, got, tc.want)
		}
	}
	// CVT.S.W of -3 is -3.0.
	h := newFPUHarness()
	h.c.SetFReg(1, 0xFFFFFFFD)                           // int32 -3 as raw bits
	h.r.load(0, 0x11<<26|0x14<<21|0<<16|1<<11|2<<6|0x20) // cvt.s.w f2, f1
	h.run(1)
	if got := h.c.FReg(2); got != 0xC0400000 {
		t.Errorf("cvt.s.w -3 = %#08x, want 0xC0400000 (-3.0)", got)
	}
}

func TestFPUCompareAndBranch(t *testing.T) {
	// C.LT.S (cond 4: less bit) sets FCC, BC1T branches, BC1F does
	// not, and the delay slot still executes for the non likely
	// forms.
	h := newFPUHarness()
	h.c.SetFReg(1, fOne)
	h.c.SetFReg(2, fTwo)
	h.r.load(0,
		encFP(2, 1, 0, 0x30|4), // 0: c.lt.s f1, f2 (true)
		encBC1(0, true, 2),     // 4: bc1t +2 -> 16
		encI(0x09, 9, 9, 1),    // 8: delay slot, r9 += 1
		encI(0x09, 0, 8, 99),   // 12: skipped
		encI(0x09, 0, 10, 7),   // 16: r10 = 7
	)
	h.run(1)
	if !h.c.fccRead(0) {
		t.Fatalf("c.lt.s 1.0 < 2.0 did not set FCC")
	}
	h.run(3)
	if h.c.Reg(9) != 1 || h.c.Reg(8) != 0 || h.c.Reg(10) != 7 {
		t.Errorf("bc1t flow: r8=%d r9=%d r10=%d", h.c.Reg(8), h.c.Reg(9), h.c.Reg(10))
	}
	// C.EQ.S false, BC1F branches.
	h = newFPUHarness()
	h.c.SetFReg(1, fOne)
	h.c.SetFReg(2, fTwo)
	h.r.load(0,
		encFP(2, 1, 0, 0x30|2), // 0: c.eq.s f1, f2 (false)
		encBC1(0, false, 2),    // 4: bc1f +2 -> 16
		encI(0x09, 9, 9, 1),    // 8: delay slot
		encI(0x09, 0, 8, 99),   // 12: skipped
		encI(0x09, 0, 10, 7),   // 16: r10 = 7
	)
	h.run(1)
	if h.c.fccRead(0) {
		t.Fatalf("c.eq.s 1.0 == 2.0 set FCC")
	}
	h.run(3)
	if h.c.Reg(9) != 1 || h.c.Reg(8) != 0 || h.c.Reg(10) != 7 {
		t.Errorf("bc1f flow: r8=%d r9=%d r10=%d", h.c.Reg(8), h.c.Reg(9), h.c.Reg(10))
	}
	// Unordered compare: C.ULT.S (cond 5: unordered or less) with
	// a NaN operand is true; ordered C.LT.S (cond 4) is false.
	// (PSP-1 encoded the predicates with the unordered term on
	// bit 3; the hardware condition field puts it on bit 0.)
	h = newFPUHarness()
	h.c.SetFReg(1, 0x7FC00000) // NaN
	h.c.SetFReg(2, fOne)
	h.r.load(0,
		encFP(2, 1, 0, 0x30|5),    // c.ult.s
		encFP(2, 1, 1<<2, 0x30|4), // c.lt.s into fcc1 via cc bits
	)
	h.run(1)
	if !h.c.fccRead(0) {
		t.Errorf("unordered compare with NaN did not set FCC")
	}
	h.run(1)
	if h.c.fccRead(1) {
		t.Errorf("ordered c.lt.s with NaN set FCC1")
	}
}

func TestFPUMovesAndControl(t *testing.T) {
	h := newFPUHarness()
	h.c.SetReg(8, fOne5)
	h.r.load(0,
		encMTC1(8, 1),        // mtc1 r8, f1
		encMFC1(9, 1),        // mfc1 r9, f1
		encI(0x09, 0, 10, 2), // addiu r10, r0, 2 (rounding RP)
		encCTC1(10, 31),      // ctc1 r10, fcsr
		encCFC1(11, 31),      // cfc1 r11, fcsr
		encCFC1(12, 0),       // cfc1 r12, fcr0 = implementation ID
	)
	h.run(6)
	if got := h.c.FReg(1); got != fOne5 {
		t.Errorf("mtc1 f1 = %#08x", got)
	}
	if got := h.c.Reg(9); got != fOne5 {
		t.Errorf("mfc1 r9 = %#08x", got)
	}
	if got := h.c.Reg(11); got != 2 {
		t.Errorf("cfc1 fcsr = %#08x, want 2", got)
	}
	if got := h.c.Reg(12); got != 0x00003351 {
		t.Errorf("cfc1 fcr0 = %#08x, want 0x00003351", got)
	}
	// LWC1 and SWC1 round trip through memory.
	h = newFPUHarness()
	h.c.SetFReg(3, 0x40580000)
	h.r.load(0,
		encI(0x39, 0, 3, 0x500), // swc1 f3, 0x500(r0)
		encI(0x31, 0, 5, 0x500), // lwc1 f5, 0x500(r0)
	)
	h.run(2)
	if got := h.c.FReg(5); got != 0x40580000 {
		t.Errorf("lwc1/swc1 round trip = %#08x", got)
	}
	// MOVT.S moves when FCC is set, MOVF.S does not.
	h = newFPUHarness()
	h.c.SetFReg(1, fOne)
	h.c.SetFReg(2, fTwo)
	h.c.SetFReg(3, 0xDEADBEEF)
	h.c.SetFReg(4, 0x12345678)
	h.r.load(0,
		encFP(2, 1, 0, 0x30|4), // c.lt.s: FCC = true
		encFP(0, 1, 3, 0x11),   // movt.s f3, f1
		encFP(0, 1, 4, 0x10),   // movf.s f4, f1 (no move)
	)
	h.run(3)
	if got := h.c.FReg(3); got != fOne {
		t.Errorf("movt.s f3 = %#08x, want 1.0 bits", got)
	}
	if got := h.c.FReg(4); got != 0x12345678 {
		t.Errorf("movf.s f4 = %#08x, want unchanged", got)
	}
}

func TestFPURoundingModesArithmetic(t *testing.T) {
	// 1.5 * 1.5 = 2.25 exactly in every mode.
	for mode := uint32(0); mode < 4; mode++ {
		h := newFPUHarness()
		h.c.fcr[31] = mode
		h.c.SetFReg(1, fOne5)
		h.c.SetFReg(2, fOne5)
		h.r.load(0, encFP(2, 1, 3, 0x02))
		h.run(1)
		if got := h.c.FReg(3); got != 0x40100000 {
			t.Errorf("mode %d: 1.5*1.5 = %#08x, want 2.25 bits", mode, got)
		}
	}
	// 1/3 under RZ truncates the RN result by one ulp:
	// RN 0x3EAAAAAB, RZ 0x3EAAAAAA.
	h := newFPUHarness()
	h.c.fcr[31] = 1 // RZ
	h.c.SetFReg(1, fOne)
	h.c.SetFReg(2, fThree)
	h.r.load(0, encFP(2, 1, 3, 0x03))
	h.run(1)
	if got := h.c.FReg(3); got != 0x3EAAAAAA {
		t.Errorf("RZ 1/3 = %#08x, want 0x3EAAAAAA", got)
	}
	// RP goes the other way from RN for a value below the RN result
	// is wrong way check: use -1/3: RN = 0xBEAAAAAB, RP (toward
	// +Inf) must be 0xBEAAAAAA.
	h = newFPUHarness()
	h.c.fcr[31] = 2            // RP
	h.c.SetFReg(1, 0xBF800000) // -1.0
	h.c.SetFReg(2, fThree)
	h.r.load(0, encFP(2, 1, 3, 0x03))
	h.run(1)
	if got := h.c.FReg(3); got != 0xBEAAAAAA {
		t.Errorf("RP -1/3 = %#08x, want 0xBEAAAAAA", got)
	}
}
