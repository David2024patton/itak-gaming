package gekko

import (
	"math"
	"math/rand"
	"testing"
)

// GCN-2 scalar FPU gates. Instruction encoders mirror the
// helpers in gekko_test.go (ins builds an X/A form word).

func fpCPU() (*CPU, *ramBus) {
	bus := newRAM()
	c := New(bus)
	c.MSR |= MSRFP
	c.MSR &^= MSRIP
	return c, bus
}

// fa builds an A-form word for opcodes 59 and 63:
// frD, frA, frB field, frC field, 5-bit XO, Rc.
func fa(op, d, a, b, cc, xo5, rc int) uint32 {
	return uint32(op)<<26 | uint32(d)<<21 | uint32(a)<<16 |
		uint32(b)<<11 | uint32(cc)<<6 | uint32(xo5)<<1 | uint32(rc)
}

// fx63 builds an opcode 63 X-form word: frD (or cr field in
// the top bits), frA, frB, 10-bit XO, Rc.
func fx63(d, a, b, xo10, rc int) uint32 {
	return 63<<26 | uint32(d)<<21 | uint32(a)<<16 |
		uint32(b)<<11 | uint32(xo10)<<1 | uint32(rc)
}

func runAt(c *CPU, bus *ramBus, addr uint32, words ...uint32) {
	prog(c, bus, addr, words...)
	steps(c, len(words))
}

func dbl(f float64) uint64 { return math.Float64bits(f) }

func TestFPRoundingModesFRSP(t *testing.T) {
	// x sits above the midpoint between the singles 1.0
	// and 1+2^-23, inexactly.
	x := 1 + math.Ldexp(1, -24) + math.Ldexp(1, -26)
	// want holds the single bits each mode must choose.
	cases := []struct {
		rn   uint32
		want uint32
	}{
		{0, 0x3F800001}, // nearest: up
		{1, 0x3F800000}, // toward zero: down
		{2, 0x3F800001}, // toward +Inf: up
		{3, 0x3F800000}, // toward -Inf: down
	}
	for _, tc := range cases {
		c, bus := fpCPU()
		c.FPSCR = tc.rn
		c.FPR[1] = dbl(x)
		runAt(c, bus, 0x1000, fx63(2, 0, 1, 12, 0)) // frsp f2, f1
		if got := c.FPR[2]; got != dbl(float64(math.Float32frombits(tc.want))) {
			t.Fatalf("rn=%d: frsp=%#x, want single %#x widened", tc.rn, got, tc.want)
		}
		if c.FPSCR&fpscrFI == 0 || c.FPSCR&fpscrXX == 0 {
			t.Fatalf("rn=%d: frsp of an inexact value must set FI and XX, FPSCR=%#x", tc.rn, c.FPSCR)
		}
	}
	// The exact midpoint ties to even under RN: 1.0 wins.
	c, bus := fpCPU()
	c.FPR[1] = dbl(1 + math.Ldexp(1, -24))
	runAt(c, bus, 0x1000, fx63(2, 0, 1, 12, 0))
	if c.FPR[2] != dbl(1.0) {
		t.Fatalf("tie: frsp=%v, want 1.0", math.Float64frombits(c.FPR[2]))
	}
	// Negative mirror: -x under RP goes toward +Inf (down
	// in magnitude), under RM away.
	c, bus = fpCPU()
	c.FPSCR = 2 // RP
	c.FPR[1] = dbl(-x)
	runAt(c, bus, 0x1000, fx63(2, 0, 1, 12, 0))
	if got := c.FPR[2]; got != dbl(-1.0) {
		t.Fatalf("RP negative: frsp=%#x, want -1.0", got)
	}
	c, bus = fpCPU()
	c.FPSCR = 3 // RM
	c.FPR[1] = dbl(-x)
	runAt(c, bus, 0x1000, fx63(2, 0, 1, 12, 0))
	if got := c.FPR[2]; got != dbl(float64(math.Float32frombits(0xBF800001))) {
		t.Fatalf("RM negative: frsp=%#x, want single 0xBF800001 widened", got)
	}
}

func TestFCTIWPatterns(t *testing.T) {
	cases := []struct {
		name string
		in   uint64
		z    bool // use fctiwz instead of fctiw
		want uint64
	}{
		{"+Inf", dbl(math.Inf(1)), false, 0x000000007fffffff},
		{"-Inf", dbl(math.Inf(-1)), false, 0xffffffff80000000},
		{"NaN", qnanDoubleBits, false, 0xffffffff80000000},
		{"over range", dbl(3e9), false, 0x000000007fffffff},
		{"under range", dbl(-3e9), false, 0xffffffff80000000},
		{"fctiwz 42.9", dbl(42.9), true, 42},
		{"fctiwz -42.9", dbl(-42.9), true, 0xffffffffffffffd6},
		{"fctiw RN 2.5 ties even", dbl(2.5), false, 2},
		{"fctiw RN 3.5 ties even", dbl(3.5), false, 4},
		{"fctiw RN -2.5 ties even", dbl(-2.5), false, 0xfffffffffffffffe},
	}
	for _, tc := range cases {
		c, bus := fpCPU()
		c.FPR[1] = tc.in
		xo := 14
		if tc.z {
			xo = 15
		}
		runAt(c, bus, 0x1000, fx63(2, 0, 1, xo, 0))
		if c.FPR[2] != tc.want {
			t.Fatalf("%s: fctiw result=%#x, want %#x", tc.name, c.FPR[2], tc.want)
		}
	}
	// Out-of-range conversion raises VXCVI.
	c, bus := fpCPU()
	c.FPR[1] = dbl(math.Inf(1))
	runAt(c, bus, 0x1000, fx63(2, 0, 1, 14, 0))
	if c.FPSCR&fpscrVXCVI == 0 {
		t.Fatalf("fctiw +Inf: VXCVI not set, FPSCR=%#x", c.FPSCR)
	}
}

func TestFMADDSingleRounding(t *testing.T) {
	// Pinned case found by exact rational search: with
	// these operands the separately rounded product loses
	// one ulp that the fused operation keeps.
	a := uint64(0xbfb4aedac30e6d50)
	b := uint64(0xbfdae86fa12d0764)
	add := uint64(0x3ef688994d13d958)
	c, bus := fpCPU()
	c.FPR[1], c.FPR[2], c.FPR[3] = a, add, b
	// fmul f4, f1, f3 then fadd f5, f4, f2 (the C operand
	// of fmul is the field at bits 21..25, here register 3).
	runAt(c, bus, 0x1000,
		fa(63, 4, 1, 0, 3, 25, 0), // fmul f4, f1, f3
		fa(63, 5, 4, 2, 0, 21, 0), // fadd f5, f4, f2
		fa(63, 6, 1, 2, 3, 29, 0), // fmadd f6, f1, f2, f3
	)
	if c.FPR[5] != 0x3fa1671eaa5f2fce {
		t.Fatalf("separate mul+add: got %#x, want 0x3fa1671eaa5f2fce", c.FPR[5])
	}
	if c.FPR[6] != 0x3fa1671eaa5f2fcf {
		t.Fatalf("fmadd: got %#x, want 0x3fa1671eaa5f2fcf (one ulp above separate)", c.FPR[6])
	}
}

func TestFCMPUNaN(t *testing.T) {
	c, bus := fpCPU()
	c.FPR[1] = qnanDoubleBits
	c.FPR[2] = dbl(1.0)
	// fcmpu cr2, f1, f2 (cr field is in the top of the
	// frD field: crfD<<2).
	runAt(c, bus, 0x1000, fx63(2<<2, 1, 2, 0, 0))
	if nib := (c.CR >> 20) & 0xF; nib != 1 { // cr2 occupies value bits 23..20
		t.Fatalf("fcmpu NaN: CR field=%04b, want 0001 (unordered)", nib)
	}
	if fpcc := (c.FPSCR >> 12) & 0xF; fpcc != 1 {
		t.Fatalf("fcmpu NaN: FPCC=%04b, want 0001", fpcc)
	}
	if c.FPSCR&(fpscrVXSNAN|fpscrVXVC) != 0 {
		t.Fatalf("fcmpu quiet NaN: no invalid bit expected, FPSCR=%#x", c.FPSCR)
	}
	// fcmpo with a quiet NaN raises VXVC.
	c, bus = fpCPU()
	c.FPR[1] = qnanDoubleBits
	c.FPR[2] = dbl(1.0)
	runAt(c, bus, 0x1000, fx63(2<<2, 1, 2, 32, 0))
	if c.FPSCR&fpscrVXVC == 0 {
		t.Fatalf("fcmpo quiet NaN: VXVC not set, FPSCR=%#x", c.FPSCR)
	}
	// A signaling NaN raises VXSNAN for both forms.
	c, bus = fpCPU()
	c.FPR[1] = 0x7FF0000000000001
	c.FPR[2] = dbl(1.0)
	runAt(c, bus, 0x1000, fx63(0, 1, 2, 0, 0))
	if c.FPSCR&fpscrVXSNAN == 0 {
		t.Fatalf("fcmpu signaling NaN: VXSNAN not set, FPSCR=%#x", c.FPSCR)
	}
	// Ordinary less-than: CR nibble 1000 and FPCC matches.
	c, bus = fpCPU()
	c.FPR[1] = dbl(1.0)
	c.FPR[2] = dbl(2.0)
	runAt(c, bus, 0x1000, fx63(0, 1, 2, 0, 0))
	if nib := (c.CR >> 28) & 0xF; nib != 8 {
		t.Fatalf("fcmpu 1<2: CR0=%04b, want 1000", nib)
	}
	if fpcc := (c.FPSCR >> 12) & 0xF; fpcc != 8 {
		t.Fatalf("fcmpu 1<2: FPCC=%04b, want 1000", fpcc)
	}
}

func TestMTFSB1MFFS(t *testing.T) {
	c, bus := fpCPU()
	c.FPSCR = 2 // RN = toward +Inf; mtfsb must never touch RN
	runAt(c, bus, 0x1000,
		fx63(6, 0, 0, 38, 0),  // mtfsb1 6 (the XX bit)
		fx63(3, 0, 0, 583, 0), // mffs f3
	)
	if c.FPR[3]&uint64(fpscrXX) == 0 {
		t.Fatalf("mffs after mtfsb1: XX bit missing, FPSCR copy=%#x", c.FPR[3])
	}
	if c.FPSCR&fpscrRNMask != 2 {
		t.Fatalf("RN changed by mtfsb1, FPSCR=%#x", c.FPSCR)
	}
	// mtfsb1 on bit 31 and mtfsb0 on bit 30 must leave RN
	// exactly as it was.
	runAt(c, bus, 0x1100,
		fx63(31, 0, 0, 38, 0), // mtfsb1 31: ignored
		fx63(30, 0, 0, 70, 0), // mtfsb0 30: ignored
	)
	if c.FPSCR&fpscrRNMask != 2 {
		t.Fatalf("mtfsb touched RN: FPSCR=%#x", c.FPSCR)
	}
	// mtfsfi writes an immediate into one FPSCR field.
	c, bus = fpCPU()
	runAt(c, bus, 0x1000, 63<<26|uint32(7<<2)<<21|3<<12|134<<1) // mtfsfi cr7, 3
	if c.FPSCR&fpscrRNMask != 3 {
		t.Fatalf("mtfsfi RN: FPSCR=%#x, want RN=3", c.FPSCR)
	}
	// mtfsf with FM selecting only field 7 copies the low
	// nibble from frB and leaves the rest alone.
	c, bus = fpCPU()
	c.FPSCR = 0xA0000000
	c.FPR[5] = 0x00000001
	runAt(c, bus, 0x1000, 63<<26|uint32(0x01)<<17|5<<11|711<<1) // mtfsf fm=0x01, f5
	if c.FPSCR != 0xA0000001 {
		t.Fatalf("mtfsf field 7: FPSCR=%#x, want 0xa0000001", c.FPSCR)
	}
}

func TestFPArithmeticBasics(t *testing.T) {
	c, bus := fpCPU()
	c.FPR[1] = dbl(1.5)
	c.FPR[2] = dbl(2.25)
	runAt(c, bus, 0x1000,
		fa(63, 3, 1, 2, 0, 21, 0), // fadd f3, f1, f2
		fa(63, 4, 2, 1, 0, 20, 0), // fsub f4, f2, f1
		fa(63, 5, 1, 0, 2, 25, 0), // fmul f5, f1, f2
		fa(63, 6, 2, 1, 0, 18, 0), // fdiv f6, f2, f1
	)
	if c.FPR[3] != dbl(3.75) || c.FPR[4] != dbl(0.75) ||
		c.FPR[5] != dbl(3.375) || c.FPR[6] != dbl(1.5) {
		t.Fatalf("basic ops: %v %v %v %v", c.FPR[3], c.FPR[4], c.FPR[5], c.FPR[6])
	}
	// fsqrt is correctly rounded.
	c.FPR[1] = dbl(2.0)
	runAt(c, bus, 0x1100, fa(63, 3, 0, 1, 0, 22, 0)) // fsqrt f3, f1
	if c.FPR[3] != dbl(math.Sqrt(2)) {
		t.Fatalf("fsqrt 2: got %#x, want %#x", c.FPR[3], dbl(math.Sqrt(2)))
	}
	// Overflow sets OX and yields Inf under RN.
	c, bus = fpCPU()
	c.FPR[1] = dbl(math.MaxFloat64)
	c.FPR[2] = dbl(math.MaxFloat64)
	runAt(c, bus, 0x1000, fa(63, 3, 1, 2, 0, 21, 0))
	if c.FPR[3] != dbl(math.Inf(1)) || c.FPSCR&fpscrOX == 0 {
		t.Fatalf("overflow: res=%#x FPSCR=%#x", c.FPR[3], c.FPSCR)
	}
	// 1/0 yields +Inf and ZX, with no invalid bit.
	c, bus = fpCPU()
	c.FPR[1] = dbl(1)
	c.FPR[2] = dbl(0)
	runAt(c, bus, 0x1000, fa(63, 3, 1, 2, 0, 18, 0))
	if c.FPR[3] != dbl(math.Inf(1)) || c.FPSCR&fpscrZX == 0 || c.FPSCR&fpscrVX != 0 {
		t.Fatalf("1/0: res=%#x FPSCR=%#x", c.FPR[3], c.FPSCR)
	}
	// 0/0 is invalid VXZDZ with a quiet NaN result.
	c, bus = fpCPU()
	c.FPR[2] = dbl(0)
	runAt(c, bus, 0x1000, fa(63, 3, 1, 2, 0, 18, 0))
	if c.FPR[3] != qnanDoubleBits || c.FPSCR&fpscrVXZDZ == 0 {
		t.Fatalf("0/0: res=%#x FPSCR=%#x", c.FPR[3], c.FPSCR)
	}
	// Rc on arithmetic writes CR1 from FPSCR bits 0..3:
	// an overflowing fadd. leaves FX and OX set, so CR1 is
	// 1001 (FEX stays clear, enables are off).
	c, bus = fpCPU()
	c.FPR[1] = dbl(math.MaxFloat64)
	c.FPR[2] = dbl(math.MaxFloat64)
	runAt(c, bus, 0x1000, fa(63, 3, 1, 2, 0, 21, 1))
	if nib := (c.CR >> 24) & 0xF; nib != 9 {
		t.Fatalf("fadd. CR1=%04b, want 1001", nib)
	}
	// Singles: fadds computes on singles and stores the
	// result widened to a double.
	c, bus = fpCPU()
	c.FPR[1] = dbl(float64(float32(0.1)))
	c.FPR[2] = dbl(float64(float32(0.2)))
	runAt(c, bus, 0x1000, fa(59, 3, 1, 2, 0, 21, 0))
	want := float32(0.1) + float32(0.2)
	if c.FPR[3] != dbl(float64(want)) {
		t.Fatalf("fadds: got %#x, want single %#x widened", c.FPR[3], math.Float32bits(want))
	}
}

func TestFPMovesAndSel(t *testing.T) {
	c, bus := fpCPU()
	c.FPR[1] = dbl(-2.5)
	runAt(c, bus, 0x1000,
		fx63(2, 0, 1, 264, 0), // fabs f2, f1
		fx63(3, 0, 1, 40, 0),  // fneg f3, f1
		fx63(4, 0, 1, 136, 0), // fnabs f4, f1
		fx63(5, 0, 1, 72, 0),  // fmr f5, f1
	)
	if c.FPR[2] != dbl(2.5) || c.FPR[3] != dbl(2.5) ||
		c.FPR[4] != dbl(-2.5) || c.FPR[5] != dbl(-2.5) {
		t.Fatalf("moves: %v %v %v %v", c.FPR[2], c.FPR[3], c.FPR[4], c.FPR[5])
	}
	// fsel: frA >= 0 picks frC, negative and NaN pick frB.
	c.FPR[1] = dbl(0.0)
	c.FPR[2] = dbl(111)
	c.FPR[3] = dbl(222)
	runAt(c, bus, 0x1100, fa(63, 4, 1, 2, 3, 23, 0)) // fsel f4, f1, f2, f3
	if c.FPR[4] != dbl(222) {
		t.Fatalf("fsel +0: got %v, want frC 222", math.Float64frombits(c.FPR[4]))
	}
	c.FPR[1] = dbl(-0.5)
	runAt(c, bus, 0x1200, fa(63, 4, 1, 2, 3, 23, 0))
	if c.FPR[4] != dbl(111) {
		t.Fatalf("fsel negative: got %v, want frB 111", math.Float64frombits(c.FPR[4]))
	}
	c.FPR[1] = qnanDoubleBits
	runAt(c, bus, 0x1300, fa(63, 4, 1, 2, 3, 23, 0))
	if c.FPR[4] != dbl(111) {
		t.Fatalf("fsel NaN: got %v, want frB 111", math.Float64frombits(c.FPR[4]))
	}
	// fres and frsqrte are exact here: 1/4 and 1/sqrt(4).
	c, bus = fpCPU()
	c.FPR[1] = dbl(float64(float32(4)))
	c.FPR[2] = dbl(4)
	runAt(c, bus, 0x1000,
		fa(59, 3, 0, 1, 0, 24, 0), // fres f3, f1 (single)
		fa(63, 4, 0, 2, 0, 26, 0), // frsqrte f4, f2
	)
	if c.FPR[3] != dbl(0.25) || c.FPR[4] != dbl(0.5) {
		t.Fatalf("estimates: fres=%v frsqrte=%v", math.Float64frombits(c.FPR[3]), math.Float64frombits(c.FPR[4]))
	}
}

func TestFPLoadStoreRoundTrips(t *testing.T) {
	c, bus := fpCPU()
	// stfd then lfd.
	c.FPR[1] = dbl(3.141592653589793)
	c.GPR[4] = 0x2000
	runAt(c, bus, 0x1000,
		insD(54, 1, 4, 0), // stfd f1, 0(r4)
		insD(50, 2, 4, 0), // lfd f2, 0(r4)
	)
	if c.FPR[2] != c.FPR[1] {
		t.Fatalf("lfd/stfd: got %#x, want %#x", c.FPR[2], c.FPR[1])
	}
	// lfs widens a single; stfs rounds a double back.
	bus.Write32(0x2100, 0x3FC00000) // 1.5f
	runAt(c, bus, 0x1100,
		insD(48, 3, 4, 0x100), // lfs f3, 0x100(r4)
	)
	if c.FPR[3] != dbl(1.5) {
		t.Fatalf("lfs: got %#x, want 1.5 widened", c.FPR[3])
	}
	c.FPR[5] = dbl(2.5)
	runAt(c, bus, 0x1200,
		insD(52, 5, 4, 0x110), // stfs f5, 0x110(r4)
	)
	if got := bus.Read32(0x2110); got != 0x40200000 {
		t.Fatalf("stfs: memory=%#x, want 0x40200000 (2.5f)", got)
	}
	// Update forms write EA back to rA.
	c.GPR[4] = 0x2200
	c.FPR[1] = dbl(-7.25)
	runAt(c, bus, 0x1300,
		insD(55, 1, 4, 8), // stfdu f1, 8(r4)
	)
	if c.GPR[4] != 0x2208 {
		t.Fatalf("stfdu: r4=%#x, want 0x2208", c.GPR[4])
	}
	c.GPR[4] = 0x21F8
	runAt(c, bus, 0x1400,
		insD(51, 6, 4, 0x10), // lfdu f6, 0x10(r4) -> 0x2208
	)
	if c.GPR[4] != 0x2208 || c.FPR[6] != dbl(-7.25) {
		t.Fatalf("lfdu: r4=%#x f6=%#x", c.GPR[4], c.FPR[6])
	}
	// Indexed forms: stfdx/lfdx, stfsx/lfsx, stfiwx.
	c, bus = fpCPU()
	c.GPR[4] = 0x2300
	c.GPR[5] = 0x10
	c.FPR[1] = dbl(9.5)
	runAt(c, bus, 0x1000,
		ins(31, 1, 4, 5, 727, false), // stfdx f1, r4, r5
		ins(31, 3, 4, 5, 599, false), // lfdx f3, r4, r5
		ins(31, 1, 4, 5, 663, false), // stfsx f1, r4, r5
		ins(31, 2, 4, 5, 535, false), // lfsx f2, r4, r5
	)
	if c.FPR[3] != dbl(9.5) {
		t.Fatalf("lfdx after stfdx: got %#x, want 9.5", c.FPR[3])
	}
	if c.FPR[2] != dbl(9.5) {
		t.Fatalf("lfsx after stfsx: got %#x, want 9.5 widened", c.FPR[2])
	}
	negThree := int64(-3)
	c.FPR[7] = uint64(negThree)                         // low word is the integer image
	runAt(c, bus, 0x1100, ins(31, 7, 4, 5, 983, false)) // stfiwx f7, r4, r5
	if got := bus.Read32(0x2310); got != 0xFFFFFFFD {
		t.Fatalf("stfiwx: memory=%#x, want 0xfffffffd", got)
	}
	// Little-endian double round trip through stfd/lfd. In
	// this core LE mode also fetches instructions
	// byte-swapped (see TestLittleEndianAccess), so the
	// two program words are stored swapped.
	c, bus = fpCPU()
	c.MSR |= MSRLE
	c.FPR[1] = dbl(1.25)
	c.GPR[4] = 0x2400
	prog(c, bus, 0x1000,
		insD(54, 1, 4, 0),
		insD(50, 2, 4, 0),
	)
	bus.Write32(0x1000, swap32(insD(54, 1, 4, 0)))
	bus.Write32(0x1004, swap32(insD(50, 2, 4, 0)))
	c.PC = 0x1000
	steps(c, 2)
	if c.FPR[2] != c.FPR[1] {
		t.Fatalf("LE lfd/stfd: got %#x, want %#x", c.FPR[2], c.FPR[1])
	}
}

func TestFPUnavailableWhenFPOff(t *testing.T) {
	// An FP load with MSR[FP]=0.
	bus := newRAM()
	c := New(bus)
	prog(c, bus, 0x1000, insD(50, 1, 4, 0)) // lfd f1, 0(r4)
	steps(c, 1)
	if c.PC != 0xFFF00000+VecFPUnavail {
		t.Fatalf("lfd with FP off: PC=%#x, want FP unavailable vector", c.PC)
	}
	// A paired single op with MSR[FP]=0.
	bus = newRAM()
	c = New(bus)
	prog(c, bus, 0x1000, fa(4, 3, 1, 2, 0, 21, 0)) // ps_add
	steps(c, 1)
	if c.PC != 0xFFF00000+VecFPUnavail {
		t.Fatalf("ps_add with FP off: PC=%#x, want FP unavailable vector", c.PC)
	}
}

func TestFPSerializeRoundTrip(t *testing.T) {
	if SerializeSize != 4688+36 {
		t.Fatalf("SerializeSize=%d, want 4724 (GCN-1 4688 + FPSCR 4 + GQR 32)", SerializeSize)
	}
	c, _ := fpCPU()
	c.FPSCR = 0x1E2C4800
	c.SetSPR(SPRGQR0+0, 0xDEADBEEF)
	c.SetSPR(SPRGQR0+7, 0x01234567)
	if got := c.GetSPR(SPRGQR0Alt + 0); got != 0xDEADBEEF {
		t.Fatalf("GQR alias 912: got %#x, want 0xdeadbeef", got)
	}
	c.setPair(9, 0x3FC00000, 0xC0200000) // (1.5, -2.5)
	state := c.Serialize()
	c2, _ := fpCPU()
	if err := c2.Deserialize(state); err != nil {
		t.Fatalf("deserialize: %v", err)
	}
	if c2.FPSCR != c.FPSCR || c2.GQR != c.GQR || c2.FPR[9] != c.FPR[9] {
		t.Fatalf("round trip: FPSCR %#x/%#x GQR %v/%v FPR9 %#x/%#x",
			c2.FPSCR, c.FPSCR, c2.GQR, c.GQR, c2.FPR[9], c.FPR[9])
	}
	// Reset clears FPSCR and the GQRs.
	c2.Reset()
	if c2.FPSCR != 0 || c2.GQR != [8]uint32{} {
		t.Fatalf("reset: FPSCR=%#x GQR=%v", c2.FPSCR, c2.GQR)
	}
}

func TestFPRandomDifferential(t *testing.T) {
	// Differential check of the exact rational engine
	// against the host IEEE operations, which are
	// correctly rounded under round-to-nearest: fadd,
	// fmul, fdiv, fmadd (against math.FMA), and the single
	// forms fadds and fmuls. Bit patterns are filtered to
	// finite values.
	rng := rand.New(rand.NewSource(0xC0FFEE))
	finite := func() uint64 {
		for {
			b := rng.Uint64()
			if classDouble(b) != classQNaN && classDouble(b) != classSNaN &&
				!classDouble(b).isInf() {
				return b
			}
		}
	}
	for i := 0; i < 120; i++ {
		a, b, cc := finite(), finite(), finite()
		fa64, fb64, fc64 := math.Float64frombits(a), math.Float64frombits(b), math.Float64frombits(cc)
		c, bus := fpCPU()
		c.FPR[1], c.FPR[2], c.FPR[3] = a, b, cc
		runAt(c, bus, 0x1000,
			fa(63, 4, 1, 2, 0, 21, 0), // fadd f4, f1, f2
			fa(63, 5, 1, 0, 2, 25, 0), // fmul f5, f1, f2
			fa(63, 6, 1, 2, 0, 18, 0), // fdiv f6, f1, f2
			fa(63, 7, 1, 2, 3, 29, 0), // fmadd f7, f1, f2, f3
		)
		if want := fa64 + fb64; c.FPR[4] != dbl(want) {
			t.Fatalf("iter %d fadd: got %#x want %#x (a=%#x b=%#x)", i, c.FPR[4], dbl(want), a, b)
		}
		if want := fa64 * fb64; c.FPR[5] != dbl(want) {
			t.Fatalf("iter %d fmul: got %#x want %#x (a=%#x b=%#x)", i, c.FPR[5], dbl(want), a, b)
		}
		if fb64 != 0 {
			if want := fa64 / fb64; c.FPR[6] != dbl(want) {
				t.Fatalf("iter %d fdiv: got %#x want %#x (a=%#x b=%#x)", i, c.FPR[6], dbl(want), a, b)
			}
		}
		if want := math.FMA(fa64, fc64, fb64); c.FPR[7] != dbl(want) {
			t.Fatalf("iter %d fmadd: got %#x want %#x (a=%#x b=%#x c=%#x)", i, c.FPR[7], dbl(want), a, b, cc)
		}
		// Singles: operands are the float32 conversions
		// of the doubles, skipping patterns whose
		// conversion lands on Inf or NaN (the host NaN
		// sign there is not the architected canonical
		// result this core produces).
		sa := float32(fa64)
		sb := float32(fb64)
		if math.IsInf(float64(sa), 0) || math.IsInf(float64(sb), 0) {
			continue
		}
		c.FPR[1] = dbl(float64(sa))
		c.FPR[2] = dbl(float64(sb))
		runAt(c, bus, 0x2000,
			fa(59, 4, 1, 2, 0, 21, 0), // fadds f4, f1, f2
			fa(59, 5, 1, 0, 2, 25, 0), // fmuls f5, f1, f2
		)
		if want := sa + sb; c.FPR[4] != dbl(float64(want)) {
			t.Fatalf("iter %d fadds: got %#x want %#x", i, c.FPR[4], dbl(float64(want)))
		}
		if want := sa * sb; c.FPR[5] != dbl(float64(want)) {
			t.Fatalf("iter %d fmuls: got %#x want %#x", i, c.FPR[5], dbl(float64(want)))
		}
	}
}
