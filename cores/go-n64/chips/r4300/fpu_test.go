package r4300

import (
	"math"
	"testing"
)

// COP1 encoders.
func mtc1(rt, fs uint32) uint32 { return 0x11<<26 | 4<<21 | rt<<16 | fs<<11 }
func mfc1(rt, fs uint32) uint32 { return 0x11<<26 | 0<<21 | rt<<16 | fs<<11 }
func dmtc1(rt, fs uint32) uint32 {
	return 0x11<<26 | 5<<21 | rt<<16 | fs<<11
}
func dmfc1(rt, fs uint32) uint32 {
	return 0x11<<26 | 1<<21 | rt<<16 | fs<<11
}
func ctc1(rt, fs uint32) uint32 { return 0x11<<26 | 6<<21 | rt<<16 | fs<<11 }
func fpArith(fmt, ft, fs, fd, funct uint32) uint32 {
	return 0x11<<26 | fmt<<21 | ft<<16 | fs<<11 | fd<<6 | funct
}
func bc1f(off int16) uint32 { return 0x11<<26 | 8<<21 | 0<<16 | uint32(uint16(off)) }
func bc1t(off int16) uint32 { return 0x11<<26 | 8<<21 | 1<<16 | uint32(uint16(off)) }

func f32bits(f float32) uint32 { return math.Float32bits(f) }

func TestFPURegisterPairFR0(t *testing.T) {
	// With FR=0, a double lives in the low words of an
	// even/odd pair: 1.5 = 0x3FF8000000000000, so $f0 gets
	// the low word 0 and $f1 the high word 0x3FF80000.
	c, _ := setupCPU(
		lui(rT0, 0x3FF8),
		mtc1(rT0, 1),            // $f1 = high word
		mtc1(rZero, 0),          // $f0 = low word 0
		dmfc1(rT1, 0),           // t1 = 1.5 as raw double
		fpArith(17, 0, 0, 2, 0), // add.d $f2, $f0, $f0 = 3.0
		dmfc1(rT2, 2),
	)
	run(c, 6)
	if c.Reg[rT1] != 0x3FF8000000000000 {
		t.Errorf("pair read: got %x", c.Reg[rT1])
	}
	if c.Reg[rT2] != 0x4008000000000000 {
		t.Errorf("add.d: got %x", c.Reg[rT2])
	}
}

func TestFPURegisterFR1(t *testing.T) {
	// With FR=1 a double occupies one whole register.
	c2, _ := setupCPU(
		dmtc1(rT0, 3),
		dmfc1(rT1, 3),
		fpArith(16, 3, 3, 4, 6), // mov.s $f4, $f3 (single view of low word)
		mfc1(rT2, 4),
	)
	c2.CP0[Cp0Status] |= StatusFR
	c2.Reg[rT0] = 0x3FF8000000000000
	run(c2, 4)
	if c2.Reg[rT1] != 0x3FF8000000000000 {
		t.Errorf("fr1 dmtc1/dmfc1: got %x", c2.Reg[rT1])
	}
	if uint32(c2.Reg[rT2]) != 0x00000000 {
		t.Errorf("fr1 single view of 1.5 low word: got %x", uint32(c2.Reg[rT2]))
	}
}

func TestFPURoundingModesConvert(t *testing.T) {
	// CVT.W.S honors FCSR RM: values 2.5 and -2.5.
	for _, tc := range []struct {
		rm   uint32
		in   float32
		want int32
	}{
		{0, 2.5, 2}, // RN ties to even
		{0, 3.5, 4}, // RN ties to even
		{1, 2.5, 2}, // RZ
		{1, -2.5, -2},
		{2, 2.5, 3}, // RP
		{2, -2.5, -2},
		{3, 2.5, 2}, // RM
		{3, -2.5, -3},
	} {
		c, _ := setupCPU(
			mtc1(rT0, 0),
			fpArith(16, 0, 0, 2, 36), // cvt.w.s $f2, $f0
			mfc1(rT1, 2),
		)
		c.Reg[rT0] = uint64(f32bits(tc.in))
		c.FCSR = tc.rm
		run(c, 3)
		if int32(c.Reg[rT1]) != tc.want {
			t.Errorf("cvt.w.s rm=%d in=%v: got %d want %d",
				tc.rm, tc.in, int32(c.Reg[rT1]), tc.want)
		}
	}
}

func TestFPUAddSingleRoundingModes(t *testing.T) {
	// 1.0 + 2^-24 is exactly halfway between 1.0 and the
	// next single (spacing 2^-23): RN keeps 1.0 (ties to
	// even), RP steps up, RZ and RM keep 1.0.
	const tiny = 0x33800000 // 2^-24
	for _, tc := range []struct {
		rm   uint32
		want uint32
	}{
		{0, 0x3F800000},
		{1, 0x3F800000},
		{2, 0x3F800001},
		{3, 0x3F800000},
	} {
		c, _ := setupCPU(
			mtc1(rT0, 0),
			mtc1(rT1, 1),
			fpArith(16, 1, 0, 2, 0), // add.s $f2, $f0, $f1
			mfc1(rT2, 2),
		)
		c.Reg[rT0] = uint64(f32bits(1.0))
		c.Reg[rT1] = uint64(tiny)
		c.FCSR = tc.rm
		run(c, 4)
		if uint32(c.Reg[rT2]) != tc.want {
			t.Errorf("add.s rm=%d: got %08x want %08x",
				tc.rm, uint32(c.Reg[rT2]), tc.want)
		}
	}
	// -1.0 + -2^-24 under RM steps away from zero.
	c, _ := setupCPU(
		mtc1(rT0, 0),
		mtc1(rT1, 1),
		fpArith(16, 1, 0, 2, 0),
		mfc1(rT2, 2),
	)
	c.Reg[rT0] = uint64(f32bits(-1.0))
	c.Reg[rT1] = uint64(0xB3800000) // -2^-24
	c.FCSR = 3
	run(c, 4)
	if uint32(c.Reg[rT2]) != 0xBF800001 {
		t.Errorf("add.s rm negative: got %08x", uint32(c.Reg[rT2]))
	}
}

func TestFPUCompareAndBranch(t *testing.T) {
	c, _ := setupCPU(
		mtc1(rT0, 0),
		mtc1(rT1, 1),
		fpArith(16, 1, 0, 0, 50), // c.eq.s $f0, $f1 (funct 48+2)
		bc1t(2),                  // taken: target is index 6
		addiu(rT2, rZero, 111),   // delay slot: still executes
		addiu(rT2, rZero, 999),   // skipped by the taken branch
		addiu(rT3, rZero, 5),     // branch target
	)
	c.Reg[rT0] = uint64(f32bits(2.0))
	c.Reg[rT1] = uint64(f32bits(2.0))
	run(c, 6)
	if !c.Cond[0] {
		t.Error("c.eq.s did not set FCC")
	}
	if c.Reg[rT2] != 111 {
		t.Errorf("bc1t delay slot: t2=%d want 111 (delay slot ran, 999 skipped)", c.Reg[rT2])
	}
	if c.Reg[rT3] != 5 {
		t.Errorf("bc1t target: t3=%d want 5", c.Reg[rT3])
	}
}

func TestFPUConversionsAndTrunc(t *testing.T) {
	c, _ := setupCPU(
		dmtc1(rT0, 0),            // $f0 = -2.9 double
		fpArith(17, 0, 0, 2, 13), // trunc.w.d $f2, $f0 -> -2
		mfc1(rT1, 2),
		fpArith(17, 0, 0, 4, 32), // cvt.s.d $f4, $f0
		mfc1(rT2, 4),
		mtc1(rT3, 6),             // $f6 = int pattern 7 (via cvt path below)
		fpArith(20, 0, 6, 8, 33), // cvt.d.w $f8, $f6 -> 7.0
		dmfc1(rT4, 8),
	)
	c.Reg[rT0] = math.Float64bits(-2.9)
	c.Reg[rT3] = 7
	run(c, 8)
	if int32(c.Reg[rT1]) != -2 {
		t.Errorf("trunc.w.d: got %d", int32(c.Reg[rT1]))
	}
	if uint32(c.Reg[rT2]) != f32bits(-2.9) {
		t.Errorf("cvt.s.d: got %08x", uint32(c.Reg[rT2]))
	}
	if c.Reg[rT4] != math.Float64bits(7.0) {
		t.Errorf("cvt.d.w: got %x", c.Reg[rT4])
	}
}

func TestFPUDivideByZeroFlag(t *testing.T) {
	c, _ := setupCPU(
		mtc1(rT0, 0),
		mtc1(rT1, 1),
		fpArith(16, 1, 0, 2, 3), // div.s $f2, $f0, $f1
		mfc1(rT2, 2),
	)
	c.Reg[rT0] = uint64(f32bits(1.0))
	c.Reg[rT1] = 0
	run(c, 4)
	if !math.IsInf(float64(math.Float32frombits(uint32(c.Reg[rT2]))), 1) {
		t.Errorf("1/0: got %x", uint32(c.Reg[rT2]))
	}
	if c.FCSR&fcsrFlagZ == 0 {
		t.Error("divide-by-zero flag not set in FCSR")
	}
}
