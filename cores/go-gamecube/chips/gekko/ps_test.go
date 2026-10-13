package gekko

import (
	"math"
	"testing"
)

// GCN-2 paired single gates.

// psA builds an opcode 4 A-form word.
func psA(d, a, b, cc, xo5, rc int) uint32 { return fa(4, d, a, b, cc, xo5, rc) }

// psX builds an opcode 4 X-form word.
func psX(d, a, b, xo10, rc int) uint32 {
	return 4<<26 | uint32(d)<<21 | uint32(a)<<16 |
		uint32(b)<<11 | uint32(xo10)<<1 | uint32(rc)
}

func pairOf(x, y float32) (uint32, uint32) {
	return math.Float32bits(x), math.Float32bits(y)
}

func setPairF(c *CPU, reg int, x, y float32) {
	p0, p1 := pairOf(x, y)
	c.setPair(reg, p0, p1)
}

func getPairF(c *CPU, reg int) (float32, float32) {
	return math.Float32frombits(uint32(c.FPR[reg] >> 32)),
		math.Float32frombits(uint32(c.FPR[reg]))
}

func TestPSLaneArithmetic(t *testing.T) {
	c, bus := fpCPU()
	setPairF(c, 1, 2, 3)
	setPairF(c, 2, 10, 20)
	setPairF(c, 3, 5, 7)
	runAt(c, bus, 0x1000,
		psA(4, 1, 2, 3, 12, 0), // ps_muls0 f4, f1, f3 -> (2*5, 3*5)
		psA(5, 1, 2, 3, 13, 0), // ps_muls1 f5, f1, f3 -> (2*7, 3*7)
		psA(6, 1, 2, 0, 21, 0), // ps_add f6, f1, f2   -> (12, 23)
		psA(7, 2, 1, 0, 20, 0), // ps_sub f7, f2, f1   -> (8, 17)
		psA(8, 1, 2, 3, 25, 0), // ps_mul f8, f1, f3   -> (10, 21)
	)
	if x, y := getPairF(c, 4); x != 10 || y != 15 {
		t.Fatalf("ps_muls0: got (%v,%v), want (10,15)", x, y)
	}
	if x, y := getPairF(c, 5); x != 14 || y != 21 {
		t.Fatalf("ps_muls1: got (%v,%v), want (14,21)", x, y)
	}
	if x, y := getPairF(c, 6); x != 12 || y != 23 {
		t.Fatalf("ps_add: got (%v,%v), want (12,23)", x, y)
	}
	if x, y := getPairF(c, 7); x != 8 || y != 17 {
		t.Fatalf("ps_sub: got (%v,%v), want (8,17)", x, y)
	}
	if x, y := getPairF(c, 8); x != 10 || y != 21 {
		t.Fatalf("ps_mul: got (%v,%v), want (10,21)", x, y)
	}
	// ps_madd and ps_div.
	runAt(c, bus, 0x1100,
		psA(9, 1, 2, 3, 29, 0),  // ps_madd f9, f1, f2, f3 -> (2*5+10, 3*7+20)
		psA(10, 2, 1, 0, 18, 0), // ps_div f10, f2, f1    -> (5, 20/3)
	)
	if x, y := getPairF(c, 9); x != 20 || y != 41 {
		t.Fatalf("ps_madd: got (%v,%v), want (20,41)", x, y)
	}
	if x, y := getPairF(c, 10); x != 5 || y != float32(20)/float32(3) {
		t.Fatalf("ps_div: got (%v,%v)", x, y)
	}
	// ps_madds0 multiplies both lanes by frC ps0.
	runAt(c, bus, 0x1200, psA(11, 1, 2, 3, 14, 0)) // (2*5+10, 3*5+20)
	if x, y := getPairF(c, 11); x != 20 || y != 35 {
		t.Fatalf("ps_madds0: got (%v,%v), want (20,35)", x, y)
	}
}

func TestPSSumCrossLane(t *testing.T) {
	c, bus := fpCPU()
	setPairF(c, 1, 1, 100) // frA
	setPairF(c, 2, 10, 2)  // frB: its ps1 (2) feeds the sum
	setPairF(c, 3, 20, 30) // frC: copied lane source
	runAt(c, bus, 0x1000,
		psA(4, 1, 2, 3, 10, 0), // ps_sum0: (A0+B1, C1) = (3, 30)
		psA(5, 1, 2, 3, 11, 0), // ps_sum1: (C0, A0+B1) = (20, 3)
	)
	if x, y := getPairF(c, 4); x != 3 || y != 30 {
		t.Fatalf("ps_sum0: got (%v,%v), want (3,30)", x, y)
	}
	if x, y := getPairF(c, 5); x != 20 || y != 3 {
		t.Fatalf("ps_sum1: got (%v,%v), want (20,3)", x, y)
	}
}

func TestPSMergeAndMoves(t *testing.T) {
	c, bus := fpCPU()
	setPairF(c, 1, 1, 2)
	setPairF(c, 2, 3, 4)
	runAt(c, bus, 0x1000,
		psX(3, 1, 2, 528, 0), // ps_merge00 -> (1,3)
		psX(4, 1, 2, 560, 0), // ps_merge01 -> (1,4)
		psX(5, 1, 2, 592, 0), // ps_merge10 -> (2,3)
		psX(6, 1, 2, 624, 0), // ps_merge11 -> (2,4)
	)
	wants := [][2]float32{{1, 3}, {1, 4}, {2, 3}, {2, 4}}
	for i, wnt := range wants {
		if x, y := getPairF(c, 3+i); x != wnt[0] || y != wnt[1] {
			t.Fatalf("merge %d: got (%v,%v), want %v", i, x, y, wnt)
		}
	}
	setPairF(c, 1, -1.5, 2.5)
	runAt(c, bus, 0x1100,
		psX(3, 0, 1, 40, 0),  // ps_neg -> (1.5, -2.5)
		psX(4, 0, 1, 264, 0), // ps_abs -> (1.5, 2.5)
		psX(5, 0, 1, 136, 0), // ps_nabs -> (-1.5, -2.5)
		psX(6, 0, 1, 72, 0),  // ps_mr  -> (-1.5, 2.5)
	)
	if x, y := getPairF(c, 3); x != 1.5 || y != -2.5 {
		t.Fatalf("ps_neg: (%v,%v)", x, y)
	}
	if x, y := getPairF(c, 4); x != 1.5 || y != 2.5 {
		t.Fatalf("ps_abs: (%v,%v)", x, y)
	}
	if x, y := getPairF(c, 5); x != -1.5 || y != -2.5 {
		t.Fatalf("ps_nabs: (%v,%v)", x, y)
	}
	if x, y := getPairF(c, 6); x != -1.5 || y != 2.5 {
		t.Fatalf("ps_mr: (%v,%v)", x, y)
	}
}

func TestPSSelResCmp(t *testing.T) {
	c, bus := fpCPU()
	setPairF(c, 1, 1, -1)                         // frA: the per-lane condition
	setPairF(c, 2, 10, 20)                        // frB: taken when frA lane < 0
	setPairF(c, 3, 30, 40)                        // frC: taken when frA lane >= 0
	runAt(c, bus, 0x1000, psA(4, 1, 2, 3, 23, 0)) // ps_sel f4, f1, f2, f3
	if x, y := getPairF(c, 4); x != 30 || y != 20 {
		t.Fatalf("ps_sel: got (%v,%v), want (30,20)", x, y)
	}
	// ps_res and ps_rsqrte are the documented approximate
	// pair; here the inputs make the exact values
	// representable, so the results are exact too.
	setPairF(c, 5, 4, 0.25)
	runAt(c, bus, 0x1100,
		psA(6, 0, 5, 0, 24, 0), // ps_res f6, f5 -> (0.25, 4)
		psA(7, 0, 5, 0, 26, 0), // ps_rsqrte f7, f5 -> (0.5, 2)
	)
	if x, y := getPairF(c, 6); x != 0.25 || y != 4 {
		t.Fatalf("ps_res: got (%v,%v), want (0.25,4)", x, y)
	}
	if x, y := getPairF(c, 7); x != 0.5 || y != 2 {
		t.Fatalf("ps_rsqrte: got (%v,%v), want (0.5,2)", x, y)
	}
	// ps_cmpu0 compares the ps0 lanes only: 9 vs 1 in ps1
	// must not matter.
	setPairF(c, 1, 1, 9)
	setPairF(c, 2, 2, 1)
	runAt(c, bus, 0x1200, psX(0, 1, 2, 0, 0)) // ps_cmpu0 cr0, f1, f2
	if nib := (c.CR >> 28) & 0xF; nib != 8 {
		t.Fatalf("ps_cmpu0: CR0=%04b, want 1000 (less)", nib)
	}
	runAt(c, bus, 0x1300, psX(0, 1, 2, 64, 0)) // ps_cmpu1: 9 vs 1, greater
	if nib := (c.CR >> 28) & 0xF; nib != 4 {
		t.Fatalf("ps_cmpu1: CR0=%04b, want 0100 (greater)", nib)
	}
}

// makeGQR packs a GQR value from its fields.
func makeGQR(ldScale, ldType, stScale, stType int) uint32 {
	return uint32(ldScale&0x3F)<<24 | uint32(ldType)<<16 |
		uint32(stScale&0x3F)<<8 | uint32(stType)
}

// psqD builds a primary 56/57/60/61 word.
func psqD(op, fr, ra, wBit, gi, disp int) uint32 {
	return uint32(op)<<26 | uint32(fr)<<21 | uint32(ra)<<16 |
		uint32(wBit)<<15 | uint32(gi)<<12 | uint32(disp&0xFFF)
}

func TestPSQScaleProof(t *testing.T) {
	// Dequantize proof from the manual pseudocode: the
	// scale is subtracted from the exponent, so the loaded
	// float is I * 2^(-S). With S = 5 and I = 32 (s16) the
	// value is exactly 32 * 2^-5 = 1.0. The opposite sign
	// convention would give 1024.0 and fail here.
	c, bus := fpCPU()
	c.SetSPR(SPRGQR0+2, makeGQR(5, gqrTypeS16, 5, gqrTypeS16))
	bus.Write32(0x3000, 0x0020FF9C) // s16 elements 32 and -100
	c.GPR[4] = 0x3000
	runAt(c, bus, 0x1000, psqD(56, 3, 4, 0, 2, 0)) // psq_l f3, 0(r4), gqr2
	if x, y := getPairF(c, 3); x != 1.0 || y != -100.0/32 {
		t.Fatalf("psq_l scale5: got (%v,%v), want (1, %v)", x, y, -100.0/32)
	}
}

func TestPSQRoundTrips(t *testing.T) {
	types := []struct {
		typ  int
		size int
		raw  []uint32 // raw element patterns, 2 elements per case
	}{
		{gqrTypeU8, 1, []uint32{200, 3}},
		{gqrTypeU16, 2, []uint32{60000, 1234}},
		{gqrTypeS8, 1, []uint32{0x80, 0x7F}},      // -128, 127
		{gqrTypeS16, 2, []uint32{0x8000, 0x7FFF}}, // -32768, 32767
		{gqrTypeS16, 2, []uint32{0xCFC7, 0x03E7}}, // -12345, 999
	}
	for _, scale := range []int{0, 5} {
		for _, tc := range types {
			c, bus := fpCPU()
			c.SetSPR(SPRGQR0+1, makeGQR(scale, tc.typ, scale, tc.typ))
			// Write the raw elements big-endian.
			base := uint32(0x3000)
			for i, r := range tc.raw {
				switch tc.size {
				case 1:
					bus.Write8(base+uint32(i), uint8(r))
				case 2:
					bus.Write16(base+uint32(i*2), uint16(r))
				}
			}
			c.GPR[4] = base
			runAt(c, bus, 0x1000,
				psqD(56, 3, 4, 0, 1, 0),    // psq_l f3
				psqD(60, 3, 4, 0, 1, 0x40), // psq_st f3 at +0x40
			)
			for i, r := range tc.raw {
				var got uint32
				switch tc.size {
				case 1:
					got = uint32(bus.Read8(base + 0x40 + uint32(i)))
				case 2:
					got = uint32(bus.Read16(base + 0x40 + uint32(i*2)))
				}
				if got != r {
					t.Fatalf("typ=%d scale=%d elem=%d: round trip got %#x, want %#x",
						tc.typ, scale, i, got, r)
				}
			}
		}
	}
	// Float type ignores the scales entirely.
	c, bus := fpCPU()
	c.SetSPR(SPRGQR0+0, makeGQR(5, gqrTypeFloat, 5, gqrTypeFloat))
	bus.Write32(0x3000, math.Float32bits(1.25))
	bus.Write32(0x3004, math.Float32bits(-0.5))
	c.GPR[4] = 0x3000
	runAt(c, bus, 0x1000,
		psqD(56, 3, 4, 0, 0, 0),
		psqD(60, 3, 4, 0, 0, 0x20),
	)
	if got := bus.Read32(0x3020); got != math.Float32bits(1.25) {
		t.Fatalf("float psq_st ps0: %#x", got)
	}
	if got := bus.Read32(0x3024); got != math.Float32bits(-0.5) {
		t.Fatalf("float psq_st ps1: %#x", got)
	}
	// Store clamping: a too-large u8 value clamps to 255.
	c, bus = fpCPU()
	c.SetSPR(SPRGQR0+0, makeGQR(0, gqrTypeU8, 0, gqrTypeU8))
	setPairF(c, 3, 300, -5)
	c.GPR[4] = 0x3100
	runAt(c, bus, 0x1000, psqD(60, 3, 4, 0, 0, 0))
	if bus.Read8(0x3100) != 255 || bus.Read8(0x3101) != 0 {
		t.Fatalf("u8 clamp: got %d,%d want 255,0", bus.Read8(0x3100), bus.Read8(0x3101))
	}
}

func TestPSQForms(t *testing.T) {
	// W=1 load: only PS0 transfers, PS1 becomes 1.0.
	c, bus := fpCPU()
	c.SetSPR(SPRGQR0+0, makeGQR(0, gqrTypeFloat, 0, gqrTypeFloat))
	bus.Write32(0x3000, math.Float32bits(7.5))
	bus.Write32(0x3004, math.Float32bits(9.5))
	c.GPR[4] = 0x3000
	runAt(c, bus, 0x1000, psqD(56, 3, 4, 1, 0, 0))
	if x, y := getPairF(c, 3); x != 7.5 || y != 1.0 {
		t.Fatalf("psq_l W=1: got (%v,%v), want (7.5,1)", x, y)
	}
	// W=1 store writes only PS0; the second slot keeps its
	// sentinel.
	bus.Write32(0x3104, 0xDEADBEEF)
	setPairF(c, 3, 2.25, 8.75)
	c.GPR[4] = 0x3100
	runAt(c, bus, 0x1100, psqD(60, 3, 4, 1, 0, 0))
	if got := bus.Read32(0x3100); got != math.Float32bits(2.25) {
		t.Fatalf("psq_st W=1 ps0: %#x", got)
	}
	if got := bus.Read32(0x3104); got != 0xDEADBEEF {
		t.Fatalf("psq_st W=1 wrote ps1 slot: %#x", got)
	}
	// Update load writes EA back to rA.
	c.GPR[4] = 0x2FF0
	runAt(c, bus, 0x1200, psqD(57, 5, 4, 0, 0, 0x10)) // psq_lu f5, 0x10(r4)
	if c.GPR[4] != 0x3000 {
		t.Fatalf("psq_lu: r4=%#x, want 0x3000", c.GPR[4])
	}
	if x, y := getPairF(c, 5); x != 7.5 || y != 9.5 {
		t.Fatalf("psq_lu data: (%v,%v)", x, y)
	}
	// Indexed load: psq_lx with W/I in the X-form bits.
	c.GPR[4] = 0x2F00
	c.GPR[5] = 0x100
	word := uint32(4)<<26 | 6<<21 | 4<<16 | 5<<11 | 0<<10 | 0<<7 | 6<<1
	runAt(c, bus, 0x1300, word) // psq_lx f6, r4, r5, gqr0, W=0
	if x, y := getPairF(c, 6); x != 7.5 || y != 9.5 {
		t.Fatalf("psq_lx: (%v,%v)", x, y)
	}
}

func TestDCBZL(t *testing.T) {
	c, bus := fpCPU()
	for i := 0; i < 96; i++ {
		bus.Write8(0x4000+uint32(i), 0xAA)
	}
	c.GPR[4] = 0x4000
	c.GPR[5] = 37                                // unaligned offset; the block is aligned down
	runAt(c, bus, 0x1000, psX(0, 4, 5, 1014, 0)) // dcbz_l r4, r5
	for i := 0; i < 32; i++ {
		if bus.Read8(0x4020+uint32(i)) != 0 {
			t.Fatalf("dcbz_l: byte %d of the aligned block not zeroed", i)
		}
	}
	if bus.Read8(0x401F) != 0xAA || bus.Read8(0x4040) != 0xAA {
		t.Fatalf("dcbz_l: bytes outside the block were touched")
	}
}

func TestPSMatrixVec4(t *testing.T) {
	// 4x4 matrix times vec4 with ps ops only: per row, two
	// ps_mul pairs, one ps_add, and one ps_sum0 horizontal
	// reduction. All values are small integers, so the
	// float32 pipeline and the float64 reference agree
	// exactly.
	m := [4][4]float32{{1, 2, 3, 4}, {0, 1, 0, 2}, {5, 0, 1, 1}, {2, 2, 2, 2}}
	v := [4]float32{5, 6, 7, 8}
	c, bus := fpCPU()
	setPairF(c, 20, v[0], v[1]) // vec low pair
	setPairF(c, 21, v[2], v[3]) // vec high pair
	for row := 0; row < 4; row++ {
		setPairF(c, 22, m[row][0], m[row][1])
		setPairF(c, 23, m[row][2], m[row][3])
		runAt(c, bus, 0x1000,
			psA(24, 22, 0, 20, 25, 0), // ps_mul t0, row01, vec01
			psA(25, 23, 0, 21, 25, 0), // ps_mul t1, row23, vec23
			psA(26, 24, 25, 0, 21, 0), // ps_add s, t0, t1
			psA(27, 26, 26, 0, 10, 0), // ps_sum0 out, s, s, anyC
		)
		got, _ := getPairF(c, 27)
		want := float64(0)
		for k := 0; k < 4; k++ {
			want += float64(m[row][k]) * float64(v[k])
		}
		if float64(got) != want {
			t.Fatalf("row %d: got %v, want %v", row, got, want)
		}
	}
}
