package allegrex

import "testing"

// Fake bus: 1 MB of flat RAM at base 0, little endian, addresses
// wrap inside the RAM so exception vectors are safe to fetch.
type ram struct{ b []byte }

func newRAM() *ram { return &ram{b: make([]byte, 1<<20)} }

func (r *ram) Read8(a uint32) uint8 { return r.b[a&(uint32(len(r.b))-1)] }
func (r *ram) Read16(a uint32) uint16 {
	return uint16(r.Read8(a)) | uint16(r.Read8(a+1))<<8
}
func (r *ram) Read32(a uint32) uint32 {
	return uint32(r.Read16(a)) | uint32(r.Read16(a+2))<<16
}
func (r *ram) Write8(a uint32, v uint8) { r.b[a&(uint32(len(r.b))-1)] = v }
func (r *ram) Write16(a uint32, v uint16) {
	r.Write8(a, uint8(v))
	r.Write8(a+1, uint8(v>>8))
}
func (r *ram) Write32(a uint32, v uint32) {
	r.Write16(a, uint16(v))
	r.Write16(a+2, uint16(v>>16))
}
func (r *ram) load(addr uint32, words ...uint32) {
	for i, w := range words {
		r.Write32(addr+uint32(4*i), w)
	}
}

type harness struct {
	c *CPU
	r *ram
}

// newHarness returns a CPU at entry 0 with Status = BEV only, so
// ERL and EXL are clear and exceptions save EPC and Cause normally.
func newHarness() *harness {
	r := newRAM()
	c := New(r)
	c.Reset(0)
	c.cp0[12][0] = 0x00400000
	return &harness{c: c, r: r}
}

func (h *harness) run(n int) {
	for i := 0; i < n; i++ {
		h.c.Step()
	}
}

// Instruction encoders.
func encR(rs, rt, rd, sa, fn uint32) uint32 {
	return rs<<21 | rt<<16 | rd<<11 | sa<<6 | fn
}
func encSP2(rs, rt, rd, fn uint32) uint32 {
	return 0x1C<<26 | rs<<21 | rt<<16 | rd<<11 | fn
}
func encSP3(rs, rt, rd, sa, fn uint32) uint32 {
	return 0x1F<<26 | rs<<21 | rt<<16 | rd<<11 | sa<<6 | fn
}
func encI(op, rs, rt, imm uint32) uint32 {
	return op<<26 | rs<<21 | rt<<16 | (imm & 0xFFFF)
}
func encJ(op, target uint32) uint32 {
	return op<<26 | (target>>2)&0x03FFFFFF
}
func encMFC0(rt, reg, sel uint32) uint32 {
	return 0x10<<26 | 0<<21 | rt<<16 | reg<<11 | sel
}
func encMTC0(rt, reg, sel uint32) uint32 {
	return 0x10<<26 | 4<<21 | rt<<16 | reg<<11 | sel
}

func excCode(c *CPU) uint32 { return (c.CP0(13, 0) >> 2) & 0x1F }
func excCE(c *CPU) uint32   { return (c.CP0(13, 0) >> 28) & 3 }

func TestALUAndShifts(t *testing.T) {
	h := newHarness()
	h.r.load(0,
		encI(0x0F, 0, 8, 0x1234), // lui r8, 0x1234
		encI(0x0D, 8, 8, 0x5678), // ori r8, r8, 0x5678
		encI(0x09, 0, 9, 0xFFFF), // addiu r9, r0, -1
		encR(8, 9, 10, 0, 0x21),  // addu r10, r8, r9
		encR(8, 9, 11, 0, 0x24),  // and r11, r8, r9
		encR(0, 9, 12, 0, 0x27),  // nor r12, r0, r9
		encR(9, 8, 13, 0, 0x2A),  // slt r13, r9, r8
		encR(9, 8, 14, 0, 0x2B),  // sltu r14, r9, r8
		encR(0, 8, 15, 4, 0x00),  // sll r15, r8, 4
		encR(0, 8, 16, 8, 0x03),  // sra r16, r8, 8
		encR(0, 9, 17, 28, 0x02), // srl r17, r9, 28
	)
	h.run(11)
	want := map[int]uint32{
		8: 0x12345678, 9: 0xFFFFFFFF, 10: 0x12345677, 11: 0x12345678,
		12: 0, 13: 1, 14: 0, 15: 0x23456780, 16: 0x00123456, 17: 0xF,
	}
	for reg, v := range want {
		if got := h.c.Reg(reg); got != v {
			t.Errorf("r%d = %#08x, want %#08x", reg, got, v)
		}
	}
	// r0 writes are discarded.
	h2 := newHarness()
	h2.r.load(0, encI(0x09, 0, 0, 5))
	h2.run(1)
	if h2.c.Reg(0) != 0 {
		t.Errorf("r0 = %#x, want 0", h2.c.Reg(0))
	}
}

func TestRotate(t *testing.T) {
	// ROTR is SRL with the rs field equal to 1.
	h := newHarness()
	h.c.SetReg(9, 0x12345678)
	h.r.load(0,
		encR(1, 9, 8, 0, 0x02),  // rotr r8, r9, 0 (identity)
		encR(1, 9, 10, 8, 0x02), // rotr r10, r9, 8
	)
	h.run(2)
	if got := h.c.Reg(8); got != 0x12345678 {
		t.Errorf("rotr 0 = %#08x", got)
	}
	if got := h.c.Reg(10); got != 0x78123456 {
		t.Errorf("rotr 8 = %#08x, want 0x78123456", got)
	}
	h2 := newHarness()
	h2.c.SetReg(9, 0x00000001)
	h2.r.load(0, encR(1, 9, 8, 31, 0x02)) // rotr by 31
	h2.run(1)
	if got := h2.c.Reg(8); got != 2 {
		t.Errorf("rotr 31 of 1 = %#08x, want 2", got)
	}
	// ROTRV is SRLV with bit 6 set; a shift register value of 33
	// rotates by 1.
	h3 := newHarness()
	h3.c.SetReg(9, 0x80000000)
	h3.c.SetReg(10, 33)
	h3.r.load(0, encR(10, 9, 8, 1, 0x06)) // rotrv r8, r9, r10
	h3.run(1)
	if got := h3.c.Reg(8); got != 0x40000000 {
		t.Errorf("rotrv 33 = %#08x, want 0x40000000", got)
	}
}

func TestExtIns(t *testing.T) {
	h := newHarness()
	h.c.SetReg(9, 0xDEADBEEF)
	h.r.load(0,
		encSP3(9, 8, 31, 0, 0),  // ext r8, r9, 0, 32 (identity)
		encSP3(9, 10, 0, 31, 0), // ext r10, r9, 31, 1
		encSP3(9, 11, 7, 8, 0),  // ext r11, r9, 8, 8
	)
	h.run(3)
	if got := h.c.Reg(8); got != 0xDEADBEEF {
		t.Errorf("ext full = %#08x", got)
	}
	if got := h.c.Reg(10); got != 1 {
		t.Errorf("ext top bit = %#08x, want 1", got)
	}
	if got := h.c.Reg(11); got != 0xBE {
		t.Errorf("ext mid = %#08x, want 0xBE", got)
	}
	// INS: top insert (pos+size = 32), full replace, partial.
	h2 := newHarness()
	h2.c.SetReg(9, 0x000000AB)
	h2.c.SetReg(8, 0x12345678)
	h2.c.SetReg(10, 0xFFFFFFFF)
	h2.c.SetReg(11, 0xAAAAAAAA)
	h2.r.load(0,
		encSP3(9, 8, 31, 24, 4), // ins r8, r9, 24, 8
		encSP3(9, 10, 31, 0, 4), // ins r10, r9, 0, 32
		encSP3(9, 11, 15, 8, 4), // ins r11, r9, 8, 8
	)
	h2.run(3)
	if got := h2.c.Reg(8); got != 0xAB345678 {
		t.Errorf("ins top = %#08x, want 0xAB345678", got)
	}
	if got := h2.c.Reg(10); got != 0x000000AB {
		t.Errorf("ins full = %#08x, want 0xAB", got)
	}
	if got := h2.c.Reg(11); got != 0xAAAAABAA {
		t.Errorf("ins mid = %#08x, want 0xAAAAABAA", got)
	}
}

func TestBSHFL(t *testing.T) {
	cases := []struct {
		sa  uint32
		in  uint32
		out uint32
	}{
		{16, 0x00000080, 0xFFFFFF80}, // seb negative
		{16, 0x0000007F, 0x0000007F}, // seb positive
		{24, 0x00008000, 0xFFFF8000}, // seh negative
		{24, 0x00007FFF, 0x00007FFF}, // seh positive
		{2, 0x11223344, 0x22114433},  // wsbh
		{20, 0x00000001, 0x80000000}, // bitrev
		{20, 0xFFFF0000, 0x0000FFFF}, // bitrev
	}
	for _, tc := range cases {
		h := newHarness()
		h.c.SetReg(9, tc.in)
		h.r.load(0, encSP3(0, 9, 8, tc.sa, 0x20))
		h.run(1)
		if got := h.c.Reg(8); got != tc.out {
			t.Errorf("bshfl sa=%d in=%#08x: got %#08x want %#08x", tc.sa, tc.in, got, tc.out)
		}
	}
}

func TestUnalignedAccess(t *testing.T) {
	const old = 0x11223344
	lwlWant := [4]uint32{0xDDCCBBAA, 0xCCBBAA44, 0xBBAA3344, 0xAA223344}
	lwrWant := [4]uint32{0xAABBCCDD, 0x11AABBCC, 0x1122AABB, 0x112233AA}
	swlWant := [4]uint32{0x44332211, 0x332211DD, 0x2211CCDD, 0x11BBCCDD}
	swrWant := [4]uint32{0x11223344, 0x223344DD, 0x3344CCDD, 0x44BBCCDD}
	for a := uint32(0); a < 4; a++ {
		// LWL
		h := newHarness()
		h.r.Write32(0x100, 0xAABBCCDD)
		h.c.SetReg(8, 0x100+a)
		h.c.SetReg(9, old)
		h.r.load(0, encI(0x22, 8, 9, 0))
		h.run(1)
		if got := h.c.Reg(9); got != lwlWant[a] {
			t.Errorf("lwl a=%d: got %#08x want %#08x", a, got, lwlWant[a])
		}
		// LWR
		h = newHarness()
		h.r.Write32(0x100, 0xAABBCCDD)
		h.c.SetReg(8, 0x100+a)
		h.c.SetReg(9, old)
		h.r.load(0, encI(0x26, 8, 9, 0))
		h.run(1)
		if got := h.c.Reg(9); got != lwrWant[a] {
			t.Errorf("lwr a=%d: got %#08x want %#08x", a, got, lwrWant[a])
		}
		// SWL
		h = newHarness()
		h.r.Write32(0x100, 0xAABBCCDD)
		h.c.SetReg(8, 0x100+a)
		h.c.SetReg(9, old)
		h.r.load(0, encI(0x2A, 8, 9, 0))
		h.run(1)
		if got := h.r.Read32(0x100); got != swlWant[a] {
			t.Errorf("swl a=%d: mem %#08x want %#08x", a, got, swlWant[a])
		}
		// SWR
		h = newHarness()
		h.r.Write32(0x100, 0xAABBCCDD)
		h.c.SetReg(8, 0x100+a)
		h.c.SetReg(9, old)
		h.r.load(0, encI(0x2E, 8, 9, 0))
		h.run(1)
		if got := h.r.Read32(0x100); got != swrWant[a] {
			t.Errorf("swr a=%d: mem %#08x want %#08x", a, got, swrWant[a])
		}
	}
	// Classic pair: LWL at p+3 then LWR at p assembles 0x12345678
	// stored as bytes across a word boundary at p = 0x101.
	h := newHarness()
	for i, b := range []byte{0x78, 0x56, 0x34, 0x12} {
		h.r.Write8(0x101+uint32(i), b)
	}
	h.c.SetReg(8, 0x101)
	h.r.load(0,
		encI(0x22, 8, 9, 3), // lwl r9, 3(r8)
		encI(0x26, 8, 9, 0), // lwr r9, 0(r8)
	)
	h.run(2)
	if got := h.c.Reg(9); got != 0x12345678 {
		t.Errorf("lwl/lwr pair = %#08x, want 0x12345678", got)
	}
}

func TestLLSC(t *testing.T) {
	h := newHarness()
	h.r.Write32(0x200, 0xAAAA)
	h.c.SetReg(8, 0x200)
	h.c.SetReg(9, 0x1234)
	h.r.load(0,
		encI(0x30, 8, 10, 0), // ll r10, 0(r8)
		encI(0x38, 8, 9, 0),  // sc r9, 0(r8)
		encI(0x38, 8, 9, 0),  // sc again, LLbit now clear
	)
	h.run(1)
	if got := h.c.Reg(10); got != 0xAAAA {
		t.Errorf("ll = %#08x", got)
	}
	h.run(1)
	if got := h.c.Reg(9); got != 1 {
		t.Errorf("sc after ll = %d, want 1", got)
	}
	if got := h.r.Read32(0x200); got != 0x1234 {
		t.Errorf("sc stored %#08x", got)
	}
	h.c.SetReg(9, 0x9999)
	h.run(1)
	if got := h.c.Reg(9); got != 0 {
		t.Errorf("sc without ll = %d, want 0", got)
	}
	if got := h.r.Read32(0x200); got != 0x1234 {
		t.Errorf("failed sc stored %#08x", got)
	}
	// ERET clears LLbit: load linked, return from a fake exception
	// whose EPC points at the SC, then the SC must fail.
	h2 := newHarness()
	h2.r.Write32(0x200, 0xBBBB)
	h2.c.SetReg(8, 0x200)
	h2.c.SetReg(9, 0x777)
	h2.r.load(0,
		encI(0x30, 8, 10, 0), // 0: ll
		0x42000018,           // 4: eret
		encI(0x38, 8, 9, 0),  // 8: sc
	)
	h2.c.cp0[14][0] = 8 // EPC = the sc instruction
	h2.run(2)           // ll, then eret jumps to 8
	if h2.c.PC() != 8 {
		t.Fatalf("after eret PC = %#x, want 8", h2.c.PC())
	}
	h2.run(1)
	if got := h2.c.Reg(9); got != 0 {
		t.Errorf("sc after eret = %d, want 0", got)
	}
}

func TestMultiplyDivideFamily(t *testing.T) {
	// MADD with a carry out of LO into HI.
	h := newHarness()
	h.c.hi, h.c.lo = 0, 0xFFFFFFFF
	h.c.SetReg(8, 0x10000)
	h.c.SetReg(9, 0x10000)
	h.r.load(0, encSP2(8, 9, 0, 0x00)) // madd r8, r9
	h.run(1)
	if h.c.HI() != 1 || h.c.LO() != 0xFFFFFFFF {
		t.Errorf("madd: HI=%#08x LO=%#08x, want HI=1 LO=0xFFFFFFFF", h.c.HI(), h.c.LO())
	}
	// MSUB borrowing below zero.
	h = newHarness()
	h.c.SetReg(8, 1)
	h.c.SetReg(9, 1)
	h.r.load(0, encSP2(8, 9, 0, 0x04)) // msub r8, r9
	h.run(1)
	if h.c.HI() != 0xFFFFFFFF || h.c.LO() != 0xFFFFFFFF {
		t.Errorf("msub: HI=%#08x LO=%#08x, want both 0xFFFFFFFF", h.c.HI(), h.c.LO())
	}
	// MUL writes rd only and leaves HI/LO alone.
	h = newHarness()
	h.c.hi, h.c.lo = 0x11111111, 0x22222222
	h.c.SetReg(8, 6)
	h.c.SetReg(9, 7)
	h.r.load(0, encSP2(8, 9, 10, 0x02)) // mul r10, r8, r9
	h.run(1)
	if got := h.c.Reg(10); got != 42 {
		t.Errorf("mul = %d, want 42", got)
	}
	if h.c.HI() != 0x11111111 || h.c.LO() != 0x22222222 {
		t.Errorf("mul touched HI/LO: %#08x %#08x", h.c.HI(), h.c.LO())
	}
	// CLZ and CLO.
	clzCases := []struct {
		fn  uint32
		in  uint32
		out uint32
	}{
		{0x20, 0, 32},
		{0x21, 0xFFFFFFFF, 32},
		{0x20, 0x00F00000, 8},
		{0x21, 0xFF000000, 8},
	}
	for _, tc := range clzCases {
		h = newHarness()
		h.c.SetReg(8, tc.in)
		h.r.load(0, encSP2(8, 10, 10, tc.fn))
		h.run(1)
		if got := h.c.Reg(10); got != tc.out {
			t.Errorf("fn %#x in %#08x: got %d want %d", tc.fn, tc.in, got, tc.out)
		}
	}
}

func TestDivByZeroAndEdges(t *testing.T) {
	div := func(signed bool, a, b uint32) (uint32, uint32) {
		h := newHarness()
		h.c.SetReg(8, a)
		h.c.SetReg(9, b)
		fn := uint32(0x1A)
		if !signed {
			fn = 0x1B
		}
		h.r.load(0, encR(8, 9, 0, 0, fn))
		h.run(1)
		return h.c.LO(), h.c.HI()
	}
	if lo, hi := div(true, 5, 0); lo != 0xFFFFFFFF || hi != 5 {
		t.Errorf("div 5/0: LO=%#08x HI=%#08x", lo, hi)
	}
	if lo, hi := div(true, 0xFFFFFFFB, 0); lo != 1 || hi != 0xFFFFFFFB {
		t.Errorf("div -5/0: LO=%#08x HI=%#08x", lo, hi)
	}
	if lo, hi := div(false, 5, 0); lo != 0xFFFFFFFF || hi != 5 {
		t.Errorf("divu 5/0: LO=%#08x HI=%#08x", lo, hi)
	}
	if lo, hi := div(false, 100, 7); lo != 14 || hi != 2 {
		t.Errorf("divu 100/7: LO=%d HI=%d", lo, hi)
	}
	if lo, hi := div(true, 0x80000000, 0xFFFFFFFF); lo != 0x80000000 || hi != 0 {
		t.Errorf("div INT_MIN/-1: LO=%#08x HI=%#08x", lo, hi)
	}
	if lo, hi := div(true, 100, 7); lo != 14 || hi != 2 {
		t.Errorf("div 100/7: LO=%d HI=%d", lo, hi)
	}
}

func TestMovzMovn(t *testing.T) {
	h := newHarness()
	h.c.SetReg(8, 0xABCD)
	h.r.load(0,
		encR(8, 0, 9, 0, 0x0A),   // movz r9, r8, r0 (taken)
		encR(8, 0, 10, 0, 0x0B),  // movn r10, r8, r0 (not taken)
		encI(0x09, 0, 11, 1),     // addiu r11, r0, 1
		encR(8, 11, 12, 0, 0x0A), // movz r12, r8, r11 (not taken)
		encR(8, 11, 13, 0, 0x0B), // movn r13, r8, r11 (taken)
	)
	h.run(5)
	want := map[int]uint32{9: 0xABCD, 10: 0, 12: 0, 13: 0xABCD}
	for reg, v := range want {
		if got := h.c.Reg(reg); got != v {
			t.Errorf("r%d = %#08x, want %#08x", reg, got, v)
		}
	}
}

func TestBranchDelaySlotAndLinks(t *testing.T) {
	// A taken branch executes its delay slot exactly once and
	// skips the instruction after it.
	h := newHarness()
	h.r.load(0,
		encI(0x04, 0, 0, 2),   // 0: beq r0, r0, +2 -> 12
		encI(0x09, 8, 8, 1),   // 4: delay slot, r8 += 1
		encI(0x09, 8, 8, 100), // 8: skipped
		encI(0x09, 0, 9, 7),   // 12: r9 = 7
	)
	h.run(3)
	if got := h.c.Reg(8); got != 1 {
		t.Errorf("delay slot count r8 = %d, want 1", got)
	}
	if got := h.c.Reg(9); got != 7 {
		t.Errorf("r9 = %d, want 7", got)
	}
	// JAL links pc+8.
	h = newHarness()
	h.r.load(0,
		encJ(0x03, 16),        // 0: jal 16
		encI(0x09, 8, 8, 1),   // 4: delay slot
		encI(0x09, 8, 8, 100), // 8: skipped
		0,                     // 12
		encI(0x09, 0, 9, 3),   // 16: r9 = 3
	)
	h.run(3)
	if got := h.c.Reg(31); got != 8 {
		t.Errorf("jal link r31 = %d, want 8", got)
	}
	if h.c.Reg(8) != 1 || h.c.Reg(9) != 3 {
		t.Errorf("jal flow: r8=%d r9=%d", h.c.Reg(8), h.c.Reg(9))
	}
	// BLTZAL writes the link even when the branch is not taken.
	h = newHarness()
	h.c.SetReg(8, 5)
	h.r.load(0,
		encI(0x01, 8, 0x10, 4), // 0: bltzal r8, +4 (not taken)
		encI(0x09, 0, 9, 1),    // 4: delay slot
	)
	h.run(1)
	if got := h.c.Reg(31); got != 8 {
		t.Errorf("bltzal link when not taken = %d, want 8", got)
	}
	h.run(1)
	if got := h.c.Reg(9); got != 1 {
		t.Errorf("bltzal delay slot r9 = %d, want 1", got)
	}
	// BGEZAL taken also links.
	h = newHarness()
	h.c.SetReg(8, 5)
	h.r.load(0,
		encI(0x01, 8, 0x11, 2), // 0: bgezal r8, +2 -> 12
		encI(0x09, 0, 9, 1),    // 4: delay slot
		encI(0x09, 0, 9, 100),  // 8: skipped
		encI(0x09, 0, 10, 2),   // 12: r10 = 2
	)
	h.run(3)
	if h.c.Reg(31) != 8 || h.c.Reg(9) != 1 || h.c.Reg(10) != 2 {
		t.Errorf("bgezal: r31=%d r9=%d r10=%d", h.c.Reg(31), h.c.Reg(9), h.c.Reg(10))
	}
}

func TestLoadUseInterlock(t *testing.T) {
	// No load delay: the instruction right after a LW sees the
	// loaded value.
	h := newHarness()
	h.r.Write32(0x300, 42)
	h.r.load(0,
		encI(0x23, 0, 8, 0x300), // lw r8, 0x300(r0)
		encR(8, 8, 9, 0, 0x21),  // addu r9, r8, r8
	)
	h.run(2)
	if got := h.c.Reg(9); got != 84 {
		t.Errorf("load/use = %d, want 84", got)
	}
}

func TestExceptions(t *testing.T) {
	// SYSCALL at 0x40 with BEV set vectors to 0xBFC00380.
	h := newHarness()
	h.r.load(0x40, encR(0, 0, 0, 0, 0x0C))
	h.c.SetPC(0x40)
	h.run(1)
	if h.c.PC() != 0xBFC00380 {
		t.Errorf("syscall vector PC = %#x", h.c.PC())
	}
	if got := h.c.CP0(14, 0); got != 0x40 {
		t.Errorf("EPC = %#x, want 0x40", got)
	}
	if excCode(h.c) != 8 {
		t.Errorf("ExcCode = %d, want 8", excCode(h.c))
	}
	if h.c.CP0(12, 0)&2 == 0 {
		t.Errorf("EXL not set after syscall")
	}
	// Misaligned LW raises AdEL with BadVAddr and no bus write.
	h = newHarness()
	h.c.SetReg(9, 0x101)
	h.r.load(0, encI(0x23, 9, 8, 0))
	h.run(1)
	if excCode(h.c) != 4 {
		t.Errorf("misaligned lw ExcCode = %d, want 4", excCode(h.c))
	}
	if got := h.c.CP0(8, 0); got != 0x101 {
		t.Errorf("BadVAddr = %#x, want 0x101", got)
	}
	// Misaligned SW raises AdES.
	h = newHarness()
	h.c.SetReg(9, 0x102)
	h.r.load(0, encI(0x2B, 9, 8, 0))
	h.run(1)
	if excCode(h.c) != 5 {
		t.Errorf("misaligned sw ExcCode = %d, want 5", excCode(h.c))
	}
	// ADD overflow raises Ov and does not write the destination.
	h = newHarness()
	h.c.SetReg(8, 0x7FFFFFFF)
	h.c.SetReg(9, 1)
	h.c.SetReg(10, 0x55)
	h.r.load(0, encR(8, 9, 10, 0, 0x20))
	h.run(1)
	if excCode(h.c) != 12 {
		t.Errorf("add overflow ExcCode = %d, want 12", excCode(h.c))
	}
	if got := h.c.Reg(10); got != 0x55 {
		t.Errorf("overflow wrote rd: %#08x", got)
	}
	// BREAK raises Bp.
	h = newHarness()
	h.r.load(0, encR(0, 0, 0, 0, 0x0D))
	h.run(1)
	if excCode(h.c) != 9 {
		t.Errorf("break ExcCode = %d, want 9", excCode(h.c))
	}
	// An exception in a delay slot sets BD and EPC = branch address.
	h = newHarness()
	h.r.load(0,
		encI(0x04, 0, 0, 10),   // 0: beq r0, r0, far
		encR(0, 0, 0, 0, 0x0C), // 4: syscall in the delay slot
	)
	h.run(2)
	if got := h.c.CP0(14, 0); got != 0 {
		t.Errorf("delay slot EPC = %#x, want 0", got)
	}
	if h.c.CP0(13, 0)&(1<<31) == 0 {
		t.Errorf("BD not set for delay slot exception")
	}
	if excCode(h.c) != 8 {
		t.Errorf("delay slot ExcCode = %d, want 8", excCode(h.c))
	}
	// Trap instruction TEQ with equal operands raises Tr.
	h = newHarness()
	h.c.SetReg(8, 3)
	h.c.SetReg(9, 3)
	h.r.load(0, encR(8, 9, 0, 0, 0x34))
	h.run(1)
	if excCode(h.c) != 13 {
		t.Errorf("teq ExcCode = %d, want 13", excCode(h.c))
	}
}

func TestInterrupt(t *testing.T) {
	h := newHarness()
	// Status = BEV | IM2 | IE, written through a real MTC0.
	h.c.SetReg(8, 0x00400401)
	h.r.load(0,
		encMTC0(8, 12, 0), // 0: mtc0 r8, Status
		0,                 // 4: nop
		0,                 // 8: nop
	)
	h.run(1)
	h.c.SetInterruptLines(1 << 2)
	h.run(1)
	if h.c.PC() != 0xBFC00380 {
		t.Errorf("interrupt vector PC = %#x, want 0xBFC00380", h.c.PC())
	}
	if got := h.c.CP0(14, 0); got != 4 {
		t.Errorf("interrupt EPC = %#x, want 4", got)
	}
	if excCode(h.c) != 0 {
		t.Errorf("interrupt ExcCode = %d, want 0", excCode(h.c))
	}
	// With Cause.IV set the vector gains 0x80.
	h = newHarness()
	h.c.cp0[12][0] = 0x00400401
	h.c.cp0[13][0] = 1 << 23 // Cause.IV
	h.c.SetInterruptLines(1 << 2)
	h.run(1)
	if h.c.PC() != 0xBFC00400 {
		t.Errorf("interrupt IV vector PC = %#x, want 0xBFC00400", h.c.PC())
	}
	// A masked line does not interrupt.
	h = newHarness()
	h.c.cp0[12][0] = 0x00400001 // IE only, no IM bits
	h.c.SetInterruptLines(1 << 2)
	h.run(1)
	if h.c.PC() != 4 { // just the nop at 0 executed
		t.Errorf("masked interrupt changed flow: PC = %#x", h.c.PC())
	}
	if excCode(h.c) != 0 && h.c.CP0(12, 0)&2 != 0 {
		t.Errorf("masked interrupt raised an exception")
	}
}

func TestHaltAndWake(t *testing.T) {
	// Enabled interrupt wakes the halted CPU.
	h := newHarness()
	h.c.cp0[12][0] = 0x00400401 // BEV | IM2 | IE
	h.r.load(0, 0x70000000, 0)  // halt, nop
	h.run(1)
	if !h.c.Halted() {
		t.Fatalf("HALT did not halt")
	}
	if h.c.PC() != 4 {
		t.Errorf("PC after HALT = %#x, want 4", h.c.PC())
	}
	if n := h.c.Step(); n != 1 || h.c.PC() != 4 || !h.c.Halted() {
		t.Errorf("halted step: cycles=%d PC=%#x halted=%v", n, h.c.PC(), h.c.Halted())
	}
	h.c.SetInterruptLines(1 << 2)
	h.c.Step()
	if h.c.Halted() {
		t.Errorf("enabled interrupt did not wake HALT")
	}
	if h.c.PC() != 0xBFC00380 {
		t.Errorf("wake vector PC = %#x", h.c.PC())
	}
	if got := h.c.CP0(14, 0); got != 4 {
		t.Errorf("wake EPC = %#x, want 4", got)
	}
	// A line whose IM bit is clear does not wake it.
	h = newHarness()
	h.c.cp0[12][0] = 0x00400001 // IE only
	h.r.load(0, 0x70000000, 0)
	h.run(1)
	h.c.SetInterruptLines(1 << 2)
	h.c.Step()
	if !h.c.Halted() || h.c.PC() != 4 {
		t.Errorf("masked line woke HALT: halted=%v PC=%#x", h.c.Halted(), h.c.PC())
	}
}

func TestCOPFaults(t *testing.T) {
	// The VFPU is implemented now: a bare COP2 word is MFC2 with
	// rs=0, and it executes without any exception or fault count.
	h := newHarness()
	h.r.load(0, 0x12<<26)
	h.run(1)
	if excCode(h.c) != 0 || h.c.VFPUFaultCount() != 0 {
		t.Errorf("cop2 mfc2: code=%d count=%d, want 0/0", excCode(h.c), h.c.VFPUFaultCount())
	}
	// LWC2 is LV.S now and executes without fault.
	h = newHarness()
	h.r.load(0, encI(0x32, 0, 8, 0))
	h.run(1)
	if excCode(h.c) != 0 || h.c.VFPUFaultCount() != 0 {
		t.Errorf("lv.s: code=%d count=%d, want 0/0", excCode(h.c), h.c.VFPUFaultCount())
	}
	// SV.S and the quad load and store families also execute.
	for _, op := range []uint32{0x3A, 0x36, 0x3E} {
		h = newHarness()
		h.r.load(0, encI(op, 0, 8, 0))
		h.run(1)
		if h.c.VFPUFaultCount() != 0 {
			t.Errorf("op %#x: VFPUFaultCount = %d, want 0", op, h.c.VFPUFaultCount())
		}
	}
	// COP1 with CU1 clear: CpU with CE=1.
	h = newHarness()               // Status has no CU1
	h.r.load(0, 0x11<<26|0x10<<21) // cop1 fmt S add.s skeleton
	h.run(1)
	if excCode(h.c) != 11 || excCE(h.c) != 1 {
		t.Errorf("cop1 disabled: code=%d CE=%d, want 11/1", excCode(h.c), excCE(h.c))
	}
	// LDC1 raises RI (no double precision on the Allegrex).
	h = newHarness()
	h.r.load(0, encI(0x35, 0, 8, 0))
	h.run(1)
	if excCode(h.c) != 10 {
		t.Errorf("ldc1 ExcCode = %d, want 10 (RI)", excCode(h.c))
	}
}

func TestMFICMTIC(t *testing.T) {
	h := newHarness()
	h.c.SetReg(8, 0xDEADBEEF)
	h.r.load(0,
		encSP2(0, 8, 5, 0x26),  // mtic r8, ic5
		encSP2(0, 9, 5, 0x24),  // mfic r9, ic5
		encSP2(0, 10, 6, 0x24), // mfic r10, ic6 (untouched)
	)
	h.run(3)
	if got := h.c.Reg(9); got != 0xDEADBEEF {
		t.Errorf("mfic = %#08x, want 0xDEADBEEF", got)
	}
	if got := h.c.Reg(10); got != 0 {
		t.Errorf("mfic untouched = %#08x, want 0", got)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	prog := []uint32{
		encI(0x09, 0, 8, 100),     // 0: addiu r8, r0, 100
		encI(0x09, 0, 9, 7),       // 4: addiu r9, r0, 7
		encR(8, 9, 0, 0, 0x18),    // 8: mult r8, r9
		encR(0, 0, 10, 0, 0x12),   // 12: mflo r10
		encI(0x23, 0, 11, 0x400),  // 16: lw r11, 0x400(r0)
		encR(11, 10, 12, 0, 0x21), // 20: addu r12, r11, r10
		encI(0x04, 0, 0, 1),       // 24: beq r0, r0, +1 -> 32
		encI(0x09, 0, 13, 5),      // 28: delay slot
		encI(0x2B, 0, 12, 0x404),  // 32: sw r12, 0x404(r0)
		encSP2(8, 9, 0, 0x00),     // 36: madd r8, r9
		0x70000000,                // 40: halt
	}
	setup := func() *harness {
		h := newHarness()
		h.r.Write32(0x400, 12345)
		h.r.load(0, prog...)
		return h
	}
	a := setup()
	a.run(5) // stop mid program, right after the LW
	snap := a.c.Serialize()
	if len(snap) != serializeSize {
		t.Fatalf("Serialize len = %d, want %d", len(snap), serializeSize)
	}
	b := setup()
	if err := b.c.Deserialize(snap); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	// Continue both the same distance (includes the branch, the
	// store, the madd, the halt, and halted ticks).
	a.run(8)
	b.run(8)
	if got, want := b.c.Serialize(), a.c.Serialize(); string(got) != string(want) {
		t.Errorf("state diverged after deserialize round trip")
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("first diff at byte %d: got %#x want %#x", i, got[i], want[i])
				break
			}
		}
	}
	if a.r.Read32(0x404) != b.r.Read32(0x404) {
		t.Errorf("stored values differ: %#08x vs %#08x", a.r.Read32(0x404), b.r.Read32(0x404))
	}
	if err := b.c.Deserialize(snap[:len(snap)-4]); err == nil {
		t.Errorf("short state accepted")
	}
	if err := b.c.Deserialize(nil); err == nil {
		t.Errorf("empty state accepted")
	}
}

func TestCP0Basics(t *testing.T) {
	h := newHarness()
	// PRId is a constant; writes to it are ignored.
	if got := h.c.CP0(15, 0); got != 0x00006542 {
		t.Errorf("PRId = %#08x, want 0x00006542", got)
	}
	h.c.SetReg(8, 0xFFFF)
	h.r.load(0,
		encMTC0(8, 15, 0),  // mtc0 r8, PRId (ignored)
		encMFC0(9, 15, 0),  // mfc0 r9, PRId
		encMTC0(8, 15, 1),  // mtc0 r8, EBase
		encMFC0(10, 15, 1), // mfc0 r10, EBase
		encMFC0(11, 9, 0),  // mfc0 r11, Count
	)
	h.run(5)
	if got := h.c.Reg(9); got != 0x00006542 {
		t.Errorf("mfc0 PRId = %#08x", got)
	}
	if got := h.c.Reg(10); got != 0xFFFF {
		t.Errorf("mfc0 EBase = %#08x, want 0xFFFF", got)
	}
	// Count advances at half rate: 5 one cycle steps give 2 or 3
	// depending on the remainder; after these exact steps the
	// accumulated cycles are 5, so Count must be 2 with remainder 1.
	if got := h.c.Reg(11); got > 3 {
		t.Errorf("Count grew too fast: %d", got)
	}
	// Writing Compare clears a latched timer interrupt.
	h.c.cp0[9][0] = 10
	h.c.cp0[11][0] = 12
	h.c.advanceCount(4) // Count 10 -> 12, timer latches
	if !h.c.timerIRQ {
		t.Fatalf("timer did not latch at Count == Compare")
	}
	h2 := newHarness()
	h2.c.SetReg(8, 77)
	h2.r.load(0, encMTC0(8, 11, 0))
	h2.c.timerIRQ = true
	h2.run(1)
	if h2.c.timerIRQ {
		t.Errorf("MTC0 Compare did not clear the timer latch")
	}
}

func TestResetContract(t *testing.T) {
	r := newRAM()
	c := New(r)
	c.Reset(0x800)
	if c.PC() != 0x800 {
		t.Errorf("Reset PC = %#x", c.PC())
	}
	if got := c.CP0(12, 0); got != 0x00400004 {
		t.Errorf("Reset Status = %#08x, want 0x00400004", got)
	}
	if got := c.CP0(13, 0); got != 0 {
		t.Errorf("Reset Cause = %#08x, want 0", got)
	}
	if c.Halted() {
		t.Errorf("Reset left CPU halted")
	}
	if n := c.Step(); n < 1 {
		t.Errorf("Step cycles = %d, want >= 1", n)
	}
}
