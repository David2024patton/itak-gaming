package gekko

import (
	"bytes"
	"testing"
)

// ramBus is a flat big-endian test memory.
type ramBus struct{ b []byte }

func newRAM() *ramBus {
	return &ramBus{b: make([]byte, 16<<20)}
}

func (r *ramBus) mask(a uint32) uint32 { return a & uint32(len(r.b)-1) }
func (r *ramBus) Read8(a uint32) uint8 { return r.b[r.mask(a)] }
func (r *ramBus) Read16(a uint32) uint16 {
	return uint16(r.Read8(a))<<8 | uint16(r.Read8(a+1))
}
func (r *ramBus) Read32(a uint32) uint32 {
	return uint32(r.Read8(a))<<24 | uint32(r.Read8(a+1))<<16 |
		uint32(r.Read8(a+2))<<8 | uint32(r.Read8(a+3))
}
func (r *ramBus) Write8(a uint32, v uint8) { r.b[r.mask(a)] = v }
func (r *ramBus) Write16(a uint32, v uint16) {
	r.Write8(a, uint8(v>>8))
	r.Write8(a+1, uint8(v))
}
func (r *ramBus) Write32(a uint32, v uint32) {
	r.Write8(a, uint8(v>>24))
	r.Write8(a+1, uint8(v>>16))
	r.Write8(a+2, uint8(v>>8))
	r.Write8(a+3, uint8(v))
}

// ins builds an X/XO-form instruction word.
func ins(op, f1v, f2v, f3v, xov uint32, rc bool) uint32 {
	w := op<<26 | f1v<<21 | f2v<<16 | f3v<<11 | xov<<1
	if rc {
		w |= 1
	}
	return w
}

// insD builds a D-form instruction word.
func insD(op, f1v, f2v, imm uint32) uint32 {
	return op<<26 | f1v<<21 | f2v<<16 | imm&0xFFFF
}

const nop = 0x60000000 // ori r0, r0, 0

// prog loads words at addr and points the CPU at them.
func prog(c *CPU, bus *ramBus, addr uint32, words ...uint32) {
	for i, w := range words {
		bus.Write32(addr+uint32(4*i), w)
	}
	c.PC = addr
}

func steps(c *CPU, n int) {
	for i := 0; i < n; i++ {
		c.Step()
	}
}

func TestRLWinmMasks(t *testing.T) {
	cases := []struct {
		v          uint32
		sh, mb, me uint32
		want       uint32
	}{
		{0x12345678, 0, 0, 31, 0x12345678},
		{0x12345678, 0, 8, 23, 0x00345600},
		{0x89ABCDEF, 0, 28, 3, 0x8000000F},  // wrapping mask
		{0x80000001, 1, 0, 31, 0x00000003},  // rotate across the top
		{0xDEADBEEF, 0, 16, 15, 0xDEADBEEF}, // wrap band covering all bits
		{0xFFFFFFFF, 0, 4, 27, 0x0FFFFFF0},
		{0x00000001, 31, 0, 0, 0x80000000}, // single bit rotated to the top
		{0xA5A5A5A5, 8, 8, 15, 0x00A50000}, // MB/ME are PowerPC-numbered: bits 8..15 are value bits 23..16
	}
	for i, tc := range cases {
		bus := newRAM()
		c := New(bus)
		c.GPR[4] = tc.v
		// rlwinm r3, r4, sh, mb, me
		w := 21<<26 | 4<<21 | 3<<16 | tc.sh<<11 | tc.mb<<6 | tc.me<<1
		prog(c, bus, 0x1000, w)
		steps(c, 1)
		if c.GPR[3] != tc.want {
			t.Errorf("case %d: rlwinm = %#08x, want %#08x", i, c.GPR[3], tc.want)
		}
	}
}

func TestRLWimiAndRLWnm(t *testing.T) {
	bus := newRAM()
	c := New(bus)
	c.GPR[3] = 0xFF00FF00
	c.GPR[4] = 0x0000ABCD
	// rlwimi r3, r4, 8, 16, 23: insert rotated low half of r4
	// (0x00ABCD00) into r3 bits 16..23 -> 0xFFCDFF00? bits
	// 16..23 of the result are mask positions 8..15 in value
	// terms: mask = bits with MB=16, ME=23 -> 0x0000FF00.
	// rotl(0xABCD, 8) = 0x00ABCD00; & 0x0000FF00 = 0x0000CD00.
	// r3 = 0xFF00FF00 with those bits replaced = 0xFF00CD00.
	w := uint32(20<<26 | 4<<21 | 3<<16 | 8<<11 | 16<<6 | 23<<1)
	prog(c, bus, 0x1000, w)
	steps(c, 1)
	if c.GPR[3] != 0xFF00CD00 {
		t.Fatalf("rlwimi = %#08x, want 0xFF00CD00", c.GPR[3])
	}

	bus = newRAM()
	c = New(bus)
	c.GPR[4] = 0x00000003
	c.GPR[5] = 30
	// rlwnm r3, r4, r5, 0, 1: rotl(3, 30) = 0xC0000000,
	// mask MB=0 ME=1 -> 0xC0000000.
	w = 23<<26 | 4<<21 | 3<<16 | 5<<11 | 0<<6 | 1<<1
	prog(c, bus, 0x1000, w)
	steps(c, 1)
	if c.GPR[3] != 0xC0000000 {
		t.Fatalf("rlwnm = %#08x, want 0xC0000000", c.GPR[3])
	}
}

func TestCarryOverflow(t *testing.T) {
	type tc struct {
		name  string
		words []uint32
		pre   func(c *CPU)
		reg   int
		want  uint32
		ca    int // -1 = do not check
		ov    int
	}
	addc := func(rd, ra, rb uint32, oe, rc bool) uint32 {
		w := ins(31, rd, ra, rb, 10, rc)
		if oe {
			w |= 0x400
		}
		return w
	}
	subfc := func(rd, ra, rb uint32, oe bool) uint32 {
		w := ins(31, rd, ra, rb, 8, false)
		if oe {
			w |= 0x400
		}
		return w
	}
	cases := []tc{
		{"addc carry", []uint32{addc(3, 4, 5, false, false)},
			func(c *CPU) { c.GPR[4], c.GPR[5] = 0xFFFFFFFF, 1 }, 3, 0, 1, -1},
		{"addc no carry", []uint32{addc(3, 4, 5, false, false)},
			func(c *CPU) { c.GPR[4], c.GPR[5] = 7, 8 }, 3, 15, 0, -1},
		{"addco overflow", []uint32{addc(3, 4, 5, true, false)},
			func(c *CPU) { c.GPR[4], c.GPR[5] = 0x7FFFFFFF, 1 }, 3, 0x80000000, 0, 1},
		{"addco neg no overflow", []uint32{addc(3, 4, 5, true, false)},
			func(c *CPU) { c.GPR[4], c.GPR[5] = 0x80000000, 1 }, 3, 0x80000001, 0, 0},
		{"subfc borrow", []uint32{subfc(3, 4, 5, false)},
			func(c *CPU) { c.GPR[4], c.GPR[5] = 5, 3 }, 3, 0xFFFFFFFE, 0, -1},
		{"subfc no borrow", []uint32{subfc(3, 4, 5, false)},
			func(c *CPU) { c.GPR[4], c.GPR[5] = 3, 5 }, 3, 2, 1, -1},
		{"subfo overflow", []uint32{subfc(3, 4, 5, true)},
			func(c *CPU) { c.GPR[4], c.GPR[5] = 0x80000000, 0x7FFFFFFF }, 3, 0xFFFFFFFF, -1, 1},
		{"subfic borrow", []uint32{insD(8, 3, 4, 10)},
			func(c *CPU) { c.GPR[4] = 20 }, 3, 0xFFFFFFF6, 0, -1},
		{"subfic no borrow", []uint32{insD(8, 3, 4, 10)},
			func(c *CPU) { c.GPR[4] = 3 }, 3, 7, 1, -1},
	}
	for _, tc := range cases {
		bus := newRAM()
		c := New(bus)
		tc.pre(c)
		prog(c, bus, 0x1000, tc.words...)
		steps(c, len(tc.words))
		if c.GPR[tc.reg] != tc.want {
			t.Errorf("%s: r%d = %#08x, want %#08x", tc.name, tc.reg, c.GPR[tc.reg], tc.want)
		}
		if tc.ca >= 0 {
			got := 0
			if c.XER&(1<<29) != 0 {
				got = 1
			}
			if got != tc.ca {
				t.Errorf("%s: CA = %d, want %d", tc.name, got, tc.ca)
			}
		}
		if tc.ov >= 0 {
			got := 0
			if c.XER&(1<<30) != 0 {
				got = 1
			}
			if got != tc.ov {
				t.Errorf("%s: OV = %d, want %d", tc.name, got, tc.ov)
			}
			if tc.ov == 1 && c.XER&(1<<31) == 0 {
				t.Errorf("%s: SO not latched with OV", tc.name)
			}
		}
	}
}

func TestAddChainAndMulDiv(t *testing.T) {
	bus := newRAM()
	c := New(bus)
	// addc wraps to zero setting CA, addze then adds the
	// carry into the high word.
	c.GPR[4] = 0xFFFFFFFF
	c.GPR[5] = 1
	c.GPR[6] = 41
	prog(c, bus, 0x1000,
		ins(31, 3, 4, 5, 10, false),   // addc r3, r4, r5
		ins(31, 7, 6, 0, 202, false),  // addze r7, r6
		ins(31, 8, 4, 5, 75, false),   // mulhw r8, r4, r5
		ins(31, 9, 4, 5, 11, false),   // mulhwu r9, r4, r5
		ins(31, 10, 4, 5, 491, false), // divw r10, r4, r5
	)
	steps(c, 5)
	if c.GPR[3] != 0 || c.GPR[7] != 42 {
		t.Fatalf("addc/addze: r3 = %#x r7 = %#x", c.GPR[3], c.GPR[7])
	}
	// mulhw of -1 * 1 = -1 -> high = 0xFFFFFFFF; mulhwu of
	// 0xFFFFFFFF * 1 -> high = 0.
	if c.GPR[8] != 0xFFFFFFFF || c.GPR[9] != 0 {
		t.Fatalf("mulhw/mulhwu: r8 = %#x r9 = %#x", c.GPR[8], c.GPR[9])
	}
	if c.GPR[10] != 0xFFFFFFFF { // -1 / 1
		t.Fatalf("divw: r10 = %#x", c.GPR[10])
	}
}

func TestStringInstructions(t *testing.T) {
	bus := newRAM()
	c := New(bus)
	copy(bus.b[0x2000:], "ABCDEFGHIJ")
	c.GPR[4] = 0x2000
	// lswi r5, r4, 10
	prog(c, bus, 0x1000, ins(31, 5, 4, 10, 597, false))
	steps(c, 1)
	if c.GPR[5] != 0x41424344 || c.GPR[6] != 0x45464748 || c.GPR[7] != 0x494A0000 {
		t.Fatalf("lswi: r5=%#08x r6=%#08x r7=%#08x", c.GPR[5], c.GPR[6], c.GPR[7])
	}

	// Register window wrap: lswi r31 with 6 bytes fills r31
	// and wraps into r0.
	bus = newRAM()
	c = New(bus)
	copy(bus.b[0x2000:], "ABCDEF")
	c.GPR[4] = 0x2000
	prog(c, bus, 0x1000, ins(31, 31, 4, 6, 597, false))
	steps(c, 1)
	if c.GPR[31] != 0x41424344 || c.GPR[0] != 0x45460000 {
		t.Fatalf("lswi wrap: r31=%#08x r0=%#08x", c.GPR[31], c.GPR[0])
	}

	// stswi stores the bytes back out, MSB first.
	bus = newRAM()
	c = New(bus)
	c.GPR[5] = 0x41424344
	c.GPR[6] = 0x45460000
	c.GPR[4] = 0x3000
	prog(c, bus, 0x1000, ins(31, 5, 4, 6, 725, false))
	steps(c, 1)
	if got := string(bus.b[0x3000:0x3006]); got != "ABCDEF" {
		t.Fatalf("stswi: memory = %q", got)
	}

	// lswx takes its byte count from XER.
	bus = newRAM()
	c = New(bus)
	copy(bus.b[0x2000:], "HELLO")
	c.GPR[4] = 0x2000
	c.XER = 5
	prog(c, bus, 0x1000, ins(31, 8, 0, 4, 533, false)) // lswx r8, r0, r4
	steps(c, 1)
	if c.GPR[8] != 0x48454C4C || c.GPR[9] != 0x4F000000 {
		t.Fatalf("lswx: r8=%#08x r9=%#08x", c.GPR[8], c.GPR[9])
	}
}

func TestReservation(t *testing.T) {
	bus := newRAM()
	c := New(bus)
	bus.Write32(0x2000, 0xDEADBEEF)
	c.GPR[4] = 0x2000
	c.GPR[5] = 0x12345678
	prog(c, bus, 0x1000,
		ins(31, 3, 0, 4, 20, false),  // lwarx r3, r0, r4
		ins(31, 5, 0, 4, 150, false), // stwcx. r5, r0, r4
		ins(31, 5, 0, 4, 150, false), // stwcx. again: must fail
	)
	steps(c, 1)
	if c.GPR[3] != 0xDEADBEEF {
		t.Fatalf("lwarx loaded %#08x", c.GPR[3])
	}
	steps(c, 1)
	if c.crField(0)&2 == 0 {
		t.Fatalf("first stwcx. should set CR0 EQ")
	}
	if bus.Read32(0x2000) != 0x12345678 {
		t.Fatalf("stwcx. did not store")
	}
	c.GPR[5] = 0x99999999
	steps(c, 1)
	if c.crField(0)&2 != 0 {
		t.Fatalf("second stwcx. should clear CR0 EQ")
	}
	if bus.Read32(0x2000) != 0x12345678 {
		t.Fatalf("failed stwcx. stored anyway")
	}

	// A reservation on one address does not authorize a
	// store to another.
	bus = newRAM()
	c = New(bus)
	c.GPR[4] = 0x2000
	c.GPR[6] = 0x2004
	prog(c, bus, 0x1000,
		ins(31, 3, 0, 4, 20, false),
		ins(31, 5, 0, 6, 150, false),
	)
	steps(c, 2)
	if c.crField(0)&2 != 0 {
		t.Fatalf("stwcx. to a different address should fail")
	}
}

func TestBATTranslation(t *testing.T) {
	bus := newRAM()
	c := New(bus)
	// Identity while DR is off.
	if pa, ok := c.Translate(0x80001234, false, false); !ok || pa != 0x80001234 {
		t.Fatalf("identity: pa=%#x ok=%v", pa, ok)
	}
	// DBAT0: 1 MB block, EA 0x80000000 -> PA 0x00000000,
	// supervisor read/write.
	c.SetSPR(SPRDBAT0U, 0x80000000|(7<<2)|(1<<1))
	c.SetSPR(SPRDBAT0U+1, 2) // BRPN 0, PP = 2
	c.MSR |= MSRDR
	if pa, ok := c.Translate(0x80012345, false, false); !ok || pa != 0x00012345 {
		t.Fatalf("bat 1MB: pa=%#x ok=%v", pa, ok)
	}
	if pa, ok := c.Translate(0x800FFFFF, false, true); !ok || pa != 0x000FFFFF {
		t.Fatalf("bat top: pa=%#x ok=%v", pa, ok)
	}
	if _, ok := c.Translate(0x80100000, false, false); ok {
		t.Fatalf("bat miss above block should fault")
	}
	// Shrink to 128 KB (BL = 0).
	c.SetSPR(SPRDBAT0U, 0x80000000|(0<<2)|(1<<1))
	if pa, ok := c.Translate(0x8001FFFF, false, false); !ok || pa != 0x1FFFF {
		t.Fatalf("bat 128K: pa=%#x ok=%v", pa, ok)
	}
	if _, ok := c.Translate(0x80020000, false, false); ok {
		t.Fatalf("bat 128K miss should fault")
	}
	// User mode: Vp is clear in BATU, so the BAT does not
	// apply and the access misses.
	c.MSR |= MSRPR
	if _, ok := c.Translate(0x80000000, false, false); ok {
		t.Fatalf("user access through supervisor-only BAT should fault")
	}
	// Set Vp and PP = 1 (read only): loads pass, stores
	// fault.
	c.SetSPR(SPRDBAT0U, 0x80000000|(0<<2)|(1<<1)|1)
	c.SetSPR(SPRDBAT0U+1, 1)
	if _, ok := c.Translate(0x80000000, false, false); !ok {
		t.Fatalf("user read through PP=1 BAT should pass")
	}
	if _, ok := c.Translate(0x80000000, false, true); ok {
		t.Fatalf("user store through PP=1 BAT should fault")
	}
	// PP = 0 denies the supervisor too.
	c.MSR &^= MSRPR
	c.SetSPR(SPRDBAT0U+1, 0)
	if _, ok := c.Translate(0x80000000, false, false); ok {
		t.Fatalf("PP=0 BAT should deny supervisor reads")
	}
}

func TestDSIOnProtectedLoad(t *testing.T) {
	bus := newRAM()
	c := New(bus)
	c.SetSPR(SPRDBAT0U, 0x80000000|(7<<2)|(1<<1))
	c.SetSPR(SPRDBAT0U+1, 0) // PP = 0: no access
	c.MSR |= MSRDR
	c.GPR[4] = 0x80001000
	prog(c, bus, 0x1000, insD(32, 3, 4, 0)) // lwz r3, 0(r4)
	steps(c, 1)
	if c.PC != 0xFFF00000+VecDSI {
		t.Fatalf("PC = %#x, want DSI vector", c.PC)
	}
	if c.DSISR&DSISRProtection == 0 {
		t.Fatalf("DSISR = %#x, want protection bit", c.DSISR)
	}
	if c.DSISR&DSISRStore != 0 {
		t.Fatalf("DSISR store bit set on a load")
	}
	if c.DAR != 0x80001000 {
		t.Fatalf("DAR = %#x", c.DAR)
	}
	if c.SRR0 != 0x1000 {
		t.Fatalf("SRR0 = %#x, want faulting instruction", c.SRR0)
	}
}

func TestDecrementerTiming(t *testing.T) {
	bus := newRAM()
	c := New(bus)
	pat := []byte{0x60, 0x00, 0x00, 0x00}
	for i := range bus.b {
		bus.b[i] = pat[i%4]
	}
	c.MSR |= MSREE
	c.SetSPR(SPRDEC, 100)
	// DEC ticks at steps 4, 8, ...; it reaches zero at step
	// 400, wraps at step 404, and the exception is taken at
	// the start of step 405.
	got := 0
	for s := 1; s <= 1000; s++ {
		c.Step()
		if c.PC == 0xFFF00000+VecDecrementer {
			got = s
			break
		}
	}
	if got != 405 {
		t.Fatalf("decrementer taken at step %d, want 405", got)
	}
	if c.TB != 101 {
		t.Fatalf("TB = %d, want 101", c.TB)
	}
	if c.SRR0 != 0xFFF00100+4*404 {
		t.Fatalf("SRR0 = %#x, want the interrupted PC", c.SRR0)
	}
	// mftb reads the time base back. The TBR number (268)
	// sits scrambled in the SPR field, bits 11-20.
	bus2 := newRAM()
	c2 := New(bus2)
	tbr := uint32((268&0x1F)<<5 | 268>>5)
	prog(c2, bus2, 0x1000, 31<<26|3<<21|tbr<<11|371<<1) // mftb r3
	c2.TB = 0x1122334455667788
	steps(c2, 1)
	if c2.GPR[3] != 0x55667788 {
		t.Fatalf("mftb low = %#08x", c2.GPR[3])
	}
}

func TestBranchConditions(t *testing.T) {
	type tc struct {
		bo, bi uint32
		cr     uint32
		ctr    uint32
		taken  bool
		ctrOut uint32
	}
	eqField := uint32(2) << 28 // CR0 EQ bit (bit 2) set
	cases := []tc{
		{12, 2, eqField, 0, true, 0}, // beq, equal
		{12, 2, 0, 0, false, 0},      // beq, not equal
		{4, 2, eqField, 0, false, 0}, // bne, equal
		{4, 2, 0, 0, true, 0},        // bne, not equal
		{16, 0, 0, 3, true, 2},       // bdnz, ctr 3 -> 2
		{16, 0, 0, 1, false, 0},      // bdnz, ctr 1 -> 0
		{18, 0, 0, 1, true, 0},       // bdz, ctr 1 -> 0
		{18, 0, 0, 2, false, 1},      // bdz, ctr 2 -> 1
		{20, 0, 0, 0, true, 0},       // unconditional form
		{12, 0, 8 << 28, 0, true, 0}, // blt form: LT is CR bit 0
		{12, 0, 0, 0, false, 0},      // blt form, clear
	}
	for i, tc := range cases {
		bus := newRAM()
		c := New(bus)
		c.CR = tc.cr
		c.CTR = tc.ctr
		// bc bo, bi, +0x40
		w := 16<<26 | tc.bo<<21 | tc.bi<<16 | 0x40
		prog(c, bus, 0x1000, w)
		steps(c, 1)
		want := uint32(0x1004)
		if tc.taken {
			want = 0x1040
		}
		if c.PC != want {
			t.Errorf("case %d (bo=%d bi=%d): PC = %#x, want %#x", i, tc.bo, tc.bi, c.PC, want)
		}
		if c.CTR != tc.ctrOut {
			t.Errorf("case %d: CTR = %d, want %d", i, c.CTR, tc.ctrOut)
		}
	}
}

func TestExceptionEntry(t *testing.T) {
	// sc: SRR0 is the next instruction, vector 0xC00.
	bus := newRAM()
	c := New(bus)
	prog(c, bus, 0x1000, 17<<26)
	steps(c, 1)
	if c.PC != 0xFFF00000+VecSyscall || c.SRR0 != 0x1004 {
		t.Fatalf("sc: PC=%#x SRR0=%#x", c.PC, c.SRR0)
	}
	if c.MSR&MSREE != 0 || c.MSR&MSRIP == 0 {
		t.Fatalf("sc: MSR = %#x, EE should be clear, IP kept", c.MSR)
	}

	// tw with TO = 31 always traps.
	bus = newRAM()
	c = New(bus)
	prog(c, bus, 0x1000, ins(31, 31, 3, 4, 4, false))
	steps(c, 1)
	if c.PC != 0xFFF00000+VecProgram || c.SRR1&SRR1ProgTrap == 0 {
		t.Fatalf("tw: PC=%#x SRR1=%#x", c.PC, c.SRR1)
	}

	// Illegal instruction word 0.
	bus = newRAM()
	c = New(bus)
	prog(c, bus, 0x1000, 0)
	steps(c, 1)
	if c.PC != 0xFFF00000+VecProgram || c.SRR1&SRR1ProgIllegal == 0 {
		t.Fatalf("illegal: PC=%#x SRR1=%#x", c.PC, c.SRR1)
	}

	// Floating point in GCN-1 always raises FP unavailable.
	bus = newRAM()
	c = New(bus)
	c.MSR |= MSRFP
	prog(c, bus, 0x1000, 63<<26|3<<21|4<<16|5<<11|21<<1) // fadd-class word
	steps(c, 1)
	if c.PC != 0xFFF00000+VecFPUnavail {
		t.Fatalf("fp: PC=%#x, want FP unavailable vector", c.PC)
	}

	// Privileged instruction in user mode.
	bus = newRAM()
	c = New(bus)
	c.MSR |= MSRPR
	prog(c, bus, 0x1000, ins(31, 3, 0, 0, 146, false)) // mtmsr r3
	steps(c, 1)
	if c.PC != 0xFFF00000+VecProgram || c.SRR1&SRR1ProgPrivileged == 0 {
		t.Fatalf("priv: PC=%#x SRR1=%#x", c.PC, c.SRR1)
	}
}

func TestAlignmentFaults(t *testing.T) {
	// lmw at a non-word-aligned address.
	bus := newRAM()
	c := New(bus)
	c.GPR[4] = 0x2002
	prog(c, bus, 0x1000, insD(46, 5, 4, 0)) // lmw r5, 0(r4)
	steps(c, 1)
	if c.PC != 0xFFF00000+VecAlignment || c.DAR != 0x2002 {
		t.Fatalf("lmw align: PC=%#x DAR=%#x", c.PC, c.DAR)
	}

	// stwcx. at a non-word-aligned address.
	bus = newRAM()
	c = New(bus)
	c.GPR[4] = 0x2001
	prog(c, bus, 0x1000, ins(31, 5, 0, 4, 150, false))
	steps(c, 1)
	if c.PC != 0xFFF00000+VecAlignment || c.DAR != 0x2001 {
		t.Fatalf("stwcx align: PC=%#x DAR=%#x", c.PC, c.DAR)
	}

	// Unaligned scalar lwz is performed, not faulted.
	bus = newRAM()
	c = New(bus)
	bus.Write32(0x2000, 0xAABBCCDD)
	c.GPR[4] = 0x2001
	prog(c, bus, 0x1000, insD(32, 3, 4, 0))
	steps(c, 1)
	if c.GPR[3] != 0xBBCCDD00 {
		t.Fatalf("unaligned lwz = %#08x, want 0xBBCCDD00", c.GPR[3])
	}
}

func TestLoadStoreForms(t *testing.T) {
	bus := newRAM()
	c := New(bus)
	c.GPR[4] = 0x2000
	c.GPR[3] = 0x11223344
	c.GPR[5] = 0x80000000
	prog(c, bus, 0x1000,
		insD(36, 3, 4, 0),            // stw r3, 0(r4)
		insD(34, 6, 4, 1),            // lbz r6, 1(r4)
		insD(42, 7, 4, 0),            // lha r7, 0(r4)
		ins(31, 8, 0, 4, 534, false), // lwbrx r8, r0, r4
		ins(31, 3, 0, 4, 662, false), // stwbrx r3, r0, r4
		insD(32, 9, 4, 0),            // lwz r9, 0(r4)
		insD(37, 5, 4, 8),            // stwu r5, 8(r4)
	)
	steps(c, 7)
	if c.GPR[6] != 0x22 {
		t.Fatalf("lbz = %#x", c.GPR[6])
	}
	if c.GPR[7] != 0x1122 {
		t.Fatalf("lha = %#x", c.GPR[7])
	}
	if c.GPR[8] != 0x44332211 {
		t.Fatalf("lwbrx = %#08x", c.GPR[8])
	}
	// stwbrx wrote the byte-reversed word; lwz read it back.
	if c.GPR[9] != 0x44332211 {
		t.Fatalf("lwz after stwbrx = %#08x", c.GPR[9])
	}
	if bus.Read32(0x2008) != 0x80000000 || c.GPR[4] != 0x2008 {
		t.Fatalf("stwu: mem=%#08x r4=%#x", bus.Read32(0x2008), c.GPR[4])
	}

	// lmw/stw round trip.
	bus = newRAM()
	c = New(bus)
	for i := 0; i < 4; i++ {
		c.GPR[28+i] = 0xA0 + uint32(i)
	}
	c.GPR[4] = 0x2000
	prog(c, bus, 0x1000,
		insD(47, 28, 4, 0), // stmw r28, 0(r4)
		insD(46, 28, 4, 0), // lmw r28, 0(r4) after clobber
	)
	c.Step()
	for i := 0; i < 4; i++ {
		c.GPR[28+i] = 0
	}
	c.Step()
	for i := 0; i < 4; i++ {
		if c.GPR[28+i] != 0xA0+uint32(i) {
			t.Fatalf("lmw r%d = %#x", 28+i, c.GPR[28+i])
		}
	}
}

func TestLittleEndianAccess(t *testing.T) {
	bus := newRAM()
	c := New(bus)
	c.MSR |= MSRLE
	c.GPR[4] = 0x2000
	c.GPR[3] = 0x11223344
	prog(c, bus, 0x1000, insD(36, 3, 4, 0)) // stw
	// In LE mode the instruction word itself is fetched
	// byte-swapped; emulate that by storing it swapped.
	bus.Write32(0x1000, swap32(insD(36, 3, 4, 0)))
	c.PC = 0x1000
	steps(c, 1)
	if got := bus.b[0x2000]; got != 0x44 {
		t.Fatalf("LE stw first byte = %#x, want 0x44", got)
	}
	if bus.Read32(0x2000) != 0x44332211 {
		t.Fatalf("LE stw word (BE read) = %#08x", bus.Read32(0x2000))
	}
}

func TestSPRAndZeroRule(t *testing.T) {
	bus := newRAM()
	c := New(bus)
	c.GPR[0] = 0x77777777
	lrField := uint32((8&0x1F)<<5 | 8>>5) // SPR 8 (LR), scrambled
	prog(c, bus, 0x1000,
		insD(14, 3, 0, 5),               // addi r3, r0, 5 (r0 reads as 0)
		31<<26|4<<21|lrField<<11|339<<1, // mfspr r4, LR
	)
	c.LR = 0xCAFEBABE
	steps(c, 2)
	if c.GPR[3] != 5 {
		t.Fatalf("addi r0 rule: r3 = %#x", c.GPR[3])
	}
	if c.GPR[4] != 0xCAFEBABE {
		t.Fatalf("mfspr LR: r4 = %#08x", c.GPR[4])
	}
	if got := c.GetSPR(SPRPVR); got != PVRGekko {
		t.Fatalf("PVR = %#08x", got)
	}
	// SPR storage round trip through instructions.
	bus = newRAM()
	c = New(bus)
	c.GPR[3] = 0x0BADF00D
	prog(c, bus, 0x1000,
		ins(31, 3, 0, 0, 467, false), // mtspr (field encodes SPR below)
		ins(31, 4, 0, 0, 339, false), // mfspr r4
	)
	// Patch both words with the SPRG1 field encoding.
	spr := uint32(SPRSPRG0 + 1)
	f := (spr&0x1F)<<5 | spr>>5
	for i := 0; i < 2; i++ {
		w := bus.Read32(0x1000 + uint32(4*i))
		w = w&^(0x3FF<<11) | f<<11
		bus.Write32(0x1000+uint32(4*i), w)
	}
	steps(c, 2)
	if c.GPR[4] != 0x0BADF00D {
		t.Fatalf("SPRG round trip: r4 = %#08x", c.GPR[4])
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	bus := newRAM()
	c := New(bus)
	c.GPR[3] = 0x11111111
	c.GPR[31] = 0x22222222
	c.FPR[7] = 0x3FF0000000000000
	c.CR = 0x12345678
	c.XER = 0xA0000005
	c.LR = 0x80004000
	c.CTR = 99
	c.MSR |= MSRDR | MSREE
	c.SetSPR(SPRDBAT0U, 0x8000001E)
	c.SetSPR(SPRDBAT0U+1, 2)
	c.SetSPR(SPRDEC, 12345)
	c.TB = 0x1122334455667788
	c.SR[3] = 0x0BADBEEF
	state := c.Serialize()
	if len(state) != SerializeSize {
		t.Fatalf("state size = %d, want %d", len(state), SerializeSize)
	}
	c2 := New(newRAM())
	if err := c2.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c2.Serialize(), state) {
		t.Fatalf("serialize after deserialize differs")
	}
	if c2.GPR[3] != 0x11111111 || c2.TB != 0x1122334455667788 ||
		c2.Decrementer() != 12345 || c2.SR[3] != 0x0BADBEEF {
		t.Fatalf("restored state mismatch")
	}
	if err := c2.Deserialize(state[:100]); err != ErrStateSize {
		t.Fatalf("short state: err = %v", err)
	}
}
