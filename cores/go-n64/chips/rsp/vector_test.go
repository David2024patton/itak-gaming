package rsp

import (
	"encoding/binary"
	"testing"
)

// vop encodes a COP2 computational instruction:
// opcode COP2, bit 25 set, element, vt, vs, vd, funct.
func vop(funct, e, vt, vs, vd uint32) uint32 {
	return 0x12<<26 | 1<<25 | e<<21 | vt<<16 | vs<<11 | vd<<6 | funct
}

// step1 executes a single instruction placed at IMEM 0
// (a BREAK sits in the next slot and is not executed).
func step1(r *RSP, instr uint32) {
	binary.BigEndian.PutUint32(r.IMEM[0:], instr)
	binary.BigEndian.PutUint32(r.IMEM[4:], breakInstr)
	r.PC, r.NPC = 0, 4
	r.halted = false
	r.Step()
}

// cfc2 runs a CFC2 into scalar register rt and returns
// its new value. sel: 0 = VCO, 1 = VCC, 2 = VCE.
func cfc2(r *RSP, rt, sel uint32) uint32 {
	step1(r, 0x12<<26|2<<21|rt<<16|sel<<11)
	return r.Reg[rt]
}

// fill sets every lane of a vector register.
func fill(r *RSP, reg int, v uint16) {
	for i := 0; i < 8; i++ {
		r.V[reg][i] = v
	}
}

func TestVMULFCornerAndValues(t *testing.T) {
	r := New()
	fill(r, 1, 0x8000)
	fill(r, 2, 0x8000)
	step1(r, vop(0x00, 0, 2, 1, 3)) // vmulf v3, v1, v2
	for i := 0; i < 8; i++ {
		if r.V[3][i] != 0x7FFF {
			t.Errorf("lane %d: %04x, want 7fff (saturated)", i, r.V[3][i])
		}
		if r.ACC[i] != 0x80008000 {
			t.Errorf("acc lane %d: %x, want 80008000", i, r.ACC[i])
		}
	}
	// Read the accumulator back through VSAR slices:
	// element 1 = mid, 0 = high, 2 = low.
	step1(r, vop(0x1D, 1, 0, 0, 4)) // vsar v4, v0, [1]
	step1(r, vop(0x1D, 0, 0, 0, 5)) // vsar v5, v0, [0]
	step1(r, vop(0x1D, 2, 0, 0, 6)) // vsar v6, v0, [2]
	if r.V[4][0] != 0x8000 || r.V[5][0] != 0x0000 || r.V[6][0] != 0x8000 {
		t.Errorf("vsar readback: mid %04x high %04x low %04x",
			r.V[4][0], r.V[5][0], r.V[6][0])
	}

	// Ordinary fractions: 0.5*0.5, 0.25*0.5, -0.25*0.5,
	// and the near-1.0 product.
	r.V[1] = [8]uint16{0x4000, 0x2000, 0xE000, 0x7FFF}
	r.V[2] = [8]uint16{0x4000, 0x4000, 0x4000, 0x4000}
	step1(r, vop(0x00, 0, 2, 1, 3))
	want := [8]uint16{0x2000, 0x1000, 0xF000, 0x4000}
	for i := 0; i < 4; i++ {
		if r.V[3][i] != want[i] {
			t.Errorf("vmulf lane %d: %04x want %04x", i, r.V[3][i], want[i])
		}
	}
	// VMULU clamps the same corner to the unsigned result.
	fill(r, 1, 0x8000)
	fill(r, 2, 0x8000)
	step1(r, vop(0x01, 0, 2, 1, 3)) // vmulu
	if r.V[3][0] != 0x8000 {
		t.Errorf("vmulu corner: %04x want 8000", r.V[3][0])
	}
}

func TestVMACChainSaturates(t *testing.T) {
	r := New()
	fill(r, 1, 0x7FFF)
	fill(r, 2, 0x7FFF)
	step1(r, vop(0x00, 0, 2, 1, 3)) // vmulf: ACC = 0x7FFE8002
	if r.V[3][0] != 0x7FFE {
		t.Fatalf("vmulf: %04x", r.V[3][0])
	}
	step1(r, vop(0x08, 0, 2, 1, 3)) // vmacf: ACC = 0x7FFE8002 + 0x7FFE0002
	if got := r.ACC[0]; got != 0xFFFC8004 {
		t.Fatalf("acc after vmacf: %x", got)
	}
	if r.V[3][0] != 0x7FFF {
		t.Fatalf("vmacf read-out not saturated: %04x", r.V[3][0])
	}
	step1(r, vop(0x08, 0, 2, 1, 3)) // vmacf again
	if got := r.ACC[0]; got != 0x17FFA8006 {
		t.Errorf("acc after second vmacf: %x", got)
	}
	if r.V[3][0] != 0x7FFF {
		t.Errorf("still saturated: %04x", r.V[3][0])
	}
}

func TestQuarterBroadcast(t *testing.T) {
	r := New()
	// vt lanes; quarter element 1 (e=3) broadcasts vt[1],
	// vt[3], vt[5], vt[7] within their pairs. vs = 0.5 in
	// every lane, so each result is exactly vt/2.
	r.V[2] = [8]uint16{0x1000, 0x2000, 0x3000, 0x4000, 0x5000, 0x6000, 0x7000, 0x7F00}
	fill(r, 1, 0x4000)
	step1(r, vop(0x00, 3, 2, 1, 3)) // vmulf v3, v1, v2[1q]
	want := [8]uint16{0x1000, 0x1000, 0x2000, 0x2000, 0x3000, 0x3000, 0x3F80, 0x3F80}
	if r.V[3] != want {
		t.Errorf("quarter broadcast: %04x want %04x", r.V[3], want)
	}
	// Half broadcast (e=6 -> vt[2] and vt[6]) and whole
	// broadcast (e=8+5 -> vt[5]) for contrast.
	fill(r, 1, 0x4000)
	step1(r, vop(0x00, 6, 2, 1, 3))
	wantH := [8]uint16{0x1800, 0x1800, 0x1800, 0x1800, 0x3800, 0x3800, 0x3800, 0x3800}
	if r.V[3] != wantH {
		t.Errorf("half broadcast: %04x want %04x", r.V[3], wantH)
	}
	step1(r, vop(0x00, 13, 2, 1, 3))
	for i := 0; i < 8; i++ {
		if r.V[3][i] != 0x3000 {
			t.Errorf("whole broadcast lane %d: %04x", i, r.V[3][i])
		}
	}
}

func TestVADDCVSUBCCarryChain(t *testing.T) {
	r := New()
	// VSUBC on the low halves: 0x0000 - 0x0001 borrows
	// and is not equal, setting VCO = 0xFF00 (NE byte)
	// plus borrow bits 0x00FF... borrow is bit i, NE is
	// bit i+8: all lanes borrow, so VCO = 0xFFFF.
	fill(r, 1, 0x0000)
	fill(r, 2, 0x0001)
	step1(r, vop(0x15, 0, 2, 1, 3)) // vsubc v3, v1, v2
	if r.V[3][0] != 0xFFFF {
		t.Fatalf("vsubc result: %04x", r.V[3][0])
	}
	if got := cfc2(r, 5, 0); got != 0xFFFFFFFF { // sign-extended 0xFFFF
		t.Errorf("cfc2 vco after vsubc: %08x", got)
	}
	// VADD on the high halves consumes the borrow:
	// 5 - 3 - 1 = 1 (32-bit: 0x00050000 - 0x00030001 =
	// 0x0001FFFF).
	fill(r, 1, 5)
	fill(r, 2, 3)
	step1(r, vop(0x11, 0, 2, 1, 4)) // vsub v4, v1, v2 (uses VCO borrow)
	if r.V[4][0] != 1 {
		t.Errorf("vsub with borrow in: %04x", r.V[4][0])
	}
	if r.VCO != 0 {
		t.Errorf("vsub did not clear VCO: %04x", r.VCO)
	}
	// VADDC carry: 0xFFFF + 0x0001 carries in every lane;
	// its not-equal byte is written 0 per the documented
	// Operation, so VCO = 0x00FF.
	fill(r, 1, 0xFFFF)
	fill(r, 2, 0x0001)
	step1(r, vop(0x14, 0, 2, 1, 3)) // vaddc
	if r.V[3][0] != 0x0000 {
		t.Errorf("vaddc result: %04x", r.V[3][0])
	}
	if got := cfc2(r, 5, 0); got != 0x000000FF {
		t.Errorf("cfc2 vco after vaddc: %08x", got)
	}
}

func TestCompareCombineWithVSUBC(t *testing.T) {
	r := New()
	// Double-precision VEQ: low halves equal via VSUBC,
	// high halves equal, so VEQ reports true.
	fill(r, 1, 0x1234)
	fill(r, 2, 0x1234)
	step1(r, vop(0x15, 0, 2, 1, 3)) // vsubc: result 0, NE = 0
	fill(r, 1, 0x0007)
	fill(r, 2, 0x0007)
	step1(r, vop(0x21, 0, 2, 1, 3)) // veq
	if r.VCC != 0x00FF {
		t.Errorf("veq combined: VCC %04x", r.VCC)
	}
	if r.V[3][0] != 0x0007 { // true selects vs
		t.Errorf("veq select: %04x", r.V[3][0])
	}
	// Same highs, but low halves differ (NE = 1): VEQ
	// reports false and selects vt.
	fill(r, 1, 0x1234)
	fill(r, 2, 0x1235)
	step1(r, vop(0x15, 0, 2, 1, 3))
	fill(r, 1, 0x0007)
	fill(r, 2, 0x0007)
	step1(r, vop(0x21, 0, 2, 1, 3))
	if r.VCC != 0 {
		t.Errorf("veq with NE lows: VCC %04x", r.VCC)
	}
	// VMRG merges by the VCC a compare leaves.
	fill(r, 1, 10)
	fill(r, 2, 20)
	step1(r, vop(0x20, 0, 2, 1, 3)) // vlt: 10 < 20 true
	fill(r, 1, 0xAAAA)
	fill(r, 2, 0xBBBB)
	step1(r, vop(0x27, 0, 2, 1, 4)) // vmrg
	if r.V[4][0] != 0xAAAA {
		t.Errorf("vmrg: %04x", r.V[4][0])
	}
}

func TestVCHVCLClip(t *testing.T) {
	r := New()
	// Equal values: ge set, VD = vt.
	fill(r, 1, 100)
	fill(r, 2, 100)
	step1(r, vop(0x25, 0, 2, 1, 3)) // vch
	if r.V[3][0] != 100 || r.VCC != 0xFF00 || r.VCO != 0 || r.VCE != 0 {
		t.Errorf("vch equal: vd %04x vcc %04x vco %04x vce %02x",
			r.V[3][0], r.VCC, r.VCO, r.VCE)
	}
	// Above range, same sign: clamped to +vt.
	fill(r, 1, 200)
	fill(r, 2, 100)
	step1(r, vop(0x25, 0, 2, 1, 3))
	if r.V[3][0] != 100 || r.VCC != 0xFF00 {
		t.Errorf("vch clamp high: vd %04x vcc %04x", r.V[3][0], r.VCC)
	}
	// Below range, signs differ: clamped to -vt, le set.
	fill(r, 1, 0xFF38) // -200
	fill(r, 2, 100)
	step1(r, vop(0x25, 0, 2, 1, 3))
	if r.V[3][0] != 0xFF9C || r.VCC != 0x00FF {
		t.Errorf("vch clamp low: vd %04x vcc %04x", r.V[3][0], r.VCC)
	}
	// Inside range: unchanged.
	fill(r, 1, 50)
	fill(r, 2, 100)
	step1(r, vop(0x25, 0, 2, 1, 3))
	if r.V[3][0] != 50 || r.VCC != 0x0000 {
		t.Errorf("vch inside: vd %04x vcc %04x", r.V[3][0], r.VCC)
	}

	// Double precision: value (100, 1) against bound
	// (100, 0) is just above the bound and must clamp to
	// exactly (100, 0): VCH on the highs ties (ge, eq),
	// then VCL on the lows resolves with the difference.
	r2 := New()
	fill(r2, 1, 100)                 // vs high
	fill(r2, 2, 100)                 // vt high
	step1(r2, vop(0x25, 0, 2, 1, 3)) // vch -> VD high
	hi := r2.V[3]
	fill(r2, 1, 0x0001)              // vs low
	fill(r2, 2, 0x0000)              // vt low
	step1(r2, vop(0x24, 0, 2, 1, 4)) // vcl -> VD low
	if hi[0] != 100 || r2.V[4][0] != 0 {
		t.Errorf("vch/vcl clamp: high %04x low %04x", hi[0], r2.V[4][0])
	}
	if r2.VCO != 0 || r2.VCE != 0 {
		t.Errorf("vcl did not clear VCO/VCE: %04x %02x", r2.VCO, r2.VCE)
	}
	// And (99, 0xFFFF) against (100, 0) is inside: the
	// low half passes through untouched.
	fill(r2, 1, 99)
	fill(r2, 2, 100)
	step1(r2, vop(0x25, 0, 2, 1, 3))
	fill(r2, 1, 0xFFFF)
	fill(r2, 2, 0x0000)
	step1(r2, vop(0x24, 0, 2, 1, 4))
	if r2.V[4][0] != 0xFFFF {
		t.Errorf("vcl inside: low %04x", r2.V[4][0])
	}
}

func TestReciprocals(t *testing.T) {
	r := New()
	// VRCP of 0.5 (element 0x4000) is exactly 2.0:
	// combined result 0x00010000 across VRCP + VRCPH.
	r.V[5][3] = 0x4000
	step1(r, vop(0x30, 11, 5, 0, 6)) // vrcp v6[0], v5[3]
	lo := r.V[6][0]
	step1(r, vop(0x32, 8, 0, 1, 6)) // vrcph v6[1], v0[0]
	combined := uint32(r.V[6][1])<<16 | uint32(lo)
	if combined != 0x00010000 {
		t.Errorf("vrcp(0.5): %08x want 00010000", combined)
	}
	// VRCP of 0x7FFF (the closest S1.15 value to 1.0):
	// 2^30/32767 = 32768.99997 in exact arithmetic,
	// rounding to 0x8001 in S16.15. The exact 0.5 input
	// 0x8000 gives exactly 1.0 (0x8000).
	r.V[5][3] = 0x7FFF
	step1(r, vop(0x30, 11, 5, 0, 6))
	step1(r, vop(0x32, 8, 0, 1, 6))
	combined = uint32(r.V[6][1])<<16 | uint32(r.V[6][0])
	if combined != 0x00008001 {
		t.Errorf("vrcp(~1.0): %08x want 00008001", combined)
	}
	// Negative input: ones complement of the positive
	// result. vrcp(-0.5): positive is 0x00010000, result
	// 0xFFFEFFFF.
	r.V[5][3] = 0xC000
	step1(r, vop(0x30, 11, 5, 0, 6))
	step1(r, vop(0x32, 8, 0, 1, 6))
	combined = uint32(r.V[6][1])<<16 | uint32(r.V[6][0])
	if combined != 0xFFFEFFFF {
		t.Errorf("vrcp(-0.5): %08x want fffeffff", combined)
	}
	// VRSQ of 0.5 is sqrt(2): 1.41421 * 32768 = 46341.
	r.V[5][3] = 0x4000
	step1(r, vop(0x34, 11, 5, 0, 6)) // vrsq
	if got := r.V[6][0]; got != 0xB505 {
		t.Errorf("vrsq(0.5) low: %04x want b505", got)
	}
	// Double precision: vrcph latches the high half of
	// 1.0 (S15.16 0x00010000), vrcpl the low half; the
	// reciprocal is 1.0 in S16.15 (0x00008000).
	r.V[5][0] = 0x0001
	step1(r, vop(0x32, 8, 5, 0, 6)) // vrcph v6[0], v5[0]
	r.V[5][0] = 0x0000
	step1(r, vop(0x31, 8, 5, 0, 6)) // vrcpl v6[0], v5[0]
	if got := r.V[6][0]; got != 0x8000 {
		t.Errorf("vrcp dp 1.0 low: %04x want 8000", got)
	}
	step1(r, vop(0x32, 8, 0, 1, 6)) // vrcph writes the high half
	if got := r.V[6][1]; got != 0x0000 {
		t.Errorf("vrcp dp 1.0 high: %04x", got)
	}
	// Divide by zero saturates.
	r.V[5][3] = 0
	step1(r, vop(0x30, 11, 5, 0, 6))
	step1(r, vop(0x32, 8, 0, 1, 6))
	combined = uint32(r.V[6][1])<<16 | uint32(r.V[6][0])
	if combined != 0x7FFFFFFF {
		t.Errorf("vrcp(0): %08x want 7fffffff", combined)
	}
}

func TestMovesAndControlRegs(t *testing.T) {
	r := New()
	r.Reg[5] = 0x1234BEEF
	// MTC2 stores the low half of rt at byte element 4
	// of v7 (lane 2).
	step1(r, 0x12<<26|4<<21|5<<16|7<<11|4<<7)
	if r.V[7][2] != 0xBEEF {
		t.Fatalf("mtc2: %04x", r.V[7][2])
	}
	// MFC2 sign-extends the element back out.
	step1(r, 0x12<<26|0<<21|6<<16|7<<11|4<<7)
	if r.Reg[6] != 0xFFFFBEEF {
		t.Errorf("mfc2: %08x", r.Reg[6])
	}
	// An odd byte element merges halves of two lanes:
	// element 5 of v7 is low byte of lane 2 then high
	// byte of lane 3.
	r.V[7][3] = 0xCA00
	step1(r, 0x12<<26|0<<21|6<<16|7<<11|5<<7)
	if r.Reg[6] != 0xFFFFEFCA {
		t.Errorf("mfc2 odd element: %08x", r.Reg[6])
	}
	// CTC2/CFC2 round trip on VCO.
	r.Reg[5] = 0x0000ABCD
	step1(r, 0x12<<26|6<<21|5<<16|0<<11) // ctc2 vco
	if r.VCO != 0xABCD {
		t.Fatalf("ctc2 vco: %04x", r.VCO)
	}
	if got := cfc2(r, 6, 0); got != 0xFFFFABCD {
		t.Errorf("cfc2 vco: %08x", got)
	}
	// VMOV copies one element; VNOP changes nothing.
	r.V[2][5] = 0x7777
	step1(r, vop(0x33, 13, 2, 1, 3)) // vmov v3[1], v2[5]
	if r.V[3][1] != 0x7777 {
		t.Errorf("vmov: %04x", r.V[3][1])
	}
	before := r.V[3]
	step1(r, vop(0x37, 0, 0, 0, 3)) // vnop
	if r.V[3] != before {
		t.Error("vnop modified state")
	}
}

func TestLQVUnalignedAndLRVPair(t *testing.T) {
	r := New()
	for i := 0; i < 16; i++ {
		r.DMEM[0x100+i] = 0xA0 + byte(i)
		r.DMEM[0x110+i] = 0xB0 + byte(i)
	}
	fill(r, 1, 0xEEEE)
	// LQV at 0x104 loads 12 bytes (to the end of the
	// 16-byte block) into register bytes 0..11; bytes
	// 12..15 keep the old value.
	r.Reg[2] = 4
	step1(r, 0x32<<26|2<<21|1<<16|4<<11|0<<7|0x10) // lqv v1, 0x100($2) -> addr 0x104
	if got := r.vByte(1, 0); got != 0xA4 {
		t.Errorf("lqv byte 0: %02x", got)
	}
	if got := r.vByte(1, 11); got != 0xAF {
		t.Errorf("lqv byte 11: %02x", got)
	}
	if got := r.vByte(1, 12); got != 0xEE {
		t.Errorf("lqv overwrote byte 12: %02x", got)
	}
	// LRV at 0x114 loads the first 4 bytes of the next
	// block into register bytes 12..15, completing the
	// unaligned 16-byte load.
	step1(r, 0x32<<26|2<<21|1<<16|5<<11|0<<7|0x11) // lrv v1, 0x110($2) -> addr 0x114
	for k := 0; k < 4; k++ {
		if got := r.vByte(1, 12+k); got != 0xB0+byte(k) {
			t.Errorf("lrv byte %d: %02x", 12+k, got)
		}
	}
	// SQV stores the same span back out.
	for i := range r.DMEM {
		r.DMEM[i] = 0
	}
	r.Reg[2] = 4
	step1(r, 0x3A<<26|2<<21|1<<16|4<<11|0<<7|0x10) // sqv v1, 0x100($2)
	if r.DMEM[0x104] != 0xA4 || r.DMEM[0x10F] != 0xAF || r.DMEM[0x110] != 0 {
		t.Errorf("sqv: %02x %02x %02x", r.DMEM[0x104], r.DMEM[0x10F], r.DMEM[0x110])
	}
}

func TestTransposeStore(t *testing.T) {
	r := New()
	// A 4x4 matrix in the low lanes of v0..v3: A[r][c].
	for row := 0; row < 4; row++ {
		for col := 0; col < 4; col++ {
			r.V[row][col] = uint16(row*4 + col + 1)
		}
	}
	// STV at lane c writes the group column starting at
	// register c (the documented start-element rotation):
	// block c short j is A[(j+c)&7][c], with the lanes of
	// registers 4..7 reading as zero here. The four
	// blocks are the transpose in rotated form.
	r.Reg[2] = 0
	for c := 0; c < 4; c++ {
		off := uint32(0x20 + c*2) // field: addr = off << 4 = 0x200 + c*0x20
		step1(r, 0x3A<<26|2<<21|0<<16|11<<11|uint32(2*c)<<7|off)
	}
	for c := 0; c < 4; c++ {
		base := 0x200 + c*0x20
		for j := 0; j < 4; j++ {
			got := uint16(r.DMEM[base+2*j])<<8 | uint16(r.DMEM[base+2*j+1])
			row := (j + c) & 7
			var want uint16
			if row < 4 {
				want = uint16(row*4 + c + 1) // A[row][c]
			}
			if got != want {
				t.Errorf("transpose block %d short %d: %04x want %04x", c, j, got, want)
			}
		}
	}
	// LTV with the same lane rotation loads the diagonal
	// back: from block 0 (lane 0) register v(j) lane 0
	// receives short j rotated by the lane.
	r2 := New()
	for j := 0; j < 8; j++ {
		binary.BigEndian.PutUint16(r2.DMEM[0x200+2*j:], uint16(j+1))
	}
	r2.Reg[2] = 0
	step1(r2, 0x32<<26|2<<21|0<<16|11<<11|0<<7|0x20) // ltv v0[0], 0x200($2)
	for j := 0; j < 8; j++ {
		if got := r2.V[j][0]; got != uint16(j+1) {
			t.Errorf("ltv lane 0 reg %d: %04x", j, got)
		}
	}
}

func TestPackedLoadsStores(t *testing.T) {
	r := New()
	for i := 0; i < 16; i++ {
		r.DMEM[0x300+i] = byte(i + 1)
	}
	// LPV/LUV reach 0x300 through the base register (the
	// 7-bit signed offset field cannot express it at
	// their shift). LHV/LFV shift by 4, field 0x30.
	r.Reg[2] = 0x300
	// LPV: byte i becomes lane i << 8.
	step1(r, 0x32<<26|2<<21|1<<16|6<<11|0<<7|0)
	for i := 0; i < 8; i++ {
		if r.V[1][i] != uint16(i+1)<<8 {
			t.Errorf("lpv lane %d: %04x", i, r.V[1][i])
		}
	}
	// LUV: byte i becomes lane i << 7.
	step1(r, 0x32<<26|2<<21|2<<16|7<<11|0<<7|0)
	for i := 0; i < 8; i++ {
		if r.V[2][i] != uint16(i+1)<<7 {
			t.Errorf("luv lane %d: %04x", i, r.V[2][i])
		}
	}
	// LHV: every second byte (offsets 0,2,4,...) << 7.
	r.Reg[2] = 0
	step1(r, 0x32<<26|2<<21|3<<16|8<<11|0<<7|0x30)
	for i := 0; i < 8; i++ {
		if r.V[3][i] != uint16(2*i+1)<<7 {
			t.Errorf("lhv lane %d: %04x", i, r.V[3][i])
		}
	}
	// SHV stores bits 14..7 back to every second byte.
	for i := range r.DMEM {
		r.DMEM[i] = 0
	}
	step1(r, 0x3A<<26|2<<21|3<<16|8<<11|0<<7|0x30)
	for i := 0; i < 8; i++ {
		if r.DMEM[0x300+2*i] != byte(2*i+1) {
			t.Errorf("shv byte %d: %02x", 2*i, r.DMEM[0x300+2*i])
		}
	}
	// LFV: every fourth byte into lanes 0..3 (element 0).
	for i := 0; i < 16; i++ {
		r.DMEM[0x300+i] = byte(0x40 + i)
	}
	step1(r, 0x32<<26|2<<21|4<<16|9<<11|0<<7|0x30)
	for i := 0; i < 4; i++ {
		if r.V[4][i] != uint16(0x40+4*i)<<7 {
			t.Errorf("lfv lane %d: %04x", i, r.V[4][i])
		}
	}
}
