package armcore

// Hand-derived ARMv5TE unit vectors, worked out from the ARM
// architecture reference encodings and semantics (the ARM7TDMI
// corpus cannot cover any of these: it predates the E
// extensions). Each test assembles one instruction word from its
// documented bit fields and checks the architectural result,
// including the Q flag behavior the task calls out.

import "testing"

type ramBus struct{ mem [1 << 16]byte }

func (b *ramBus) Read(addr uint32, size int) uint32 {
	addr &= 0xFFFF
	var v uint32
	for i := 0; i < size; i++ {
		v |= uint32(b.mem[(addr+uint32(i))&0xFFFF]) << (8 * i)
	}
	return v
}

func (b *ramBus) Write(addr uint32, size int, val uint32) {
	addr &= 0xFFFF
	for i := 0; i < size; i++ {
		b.mem[(addr+uint32(i))&0xFFFF] = byte(val >> (8 * i))
	}
}

func (b *ramBus) Fetch(addr uint32, size int) uint32 { return b.Read(addr, size) }

func newTestCPU(v5 bool) (*CPU, *ramBus) {
	b := &ramBus{}
	if v5 {
		return NewARM9(b), b
	}
	return NewARM7(b), b
}

// execARMWord places word at addr, jumps there, and steps once.
func execARMWord(c *CPU, b *ramBus, addr, word uint32) {
	b.Write(addr, 4, word)
	c.Jump(addr)
	c.Step()
}

func TestCLZEdges(t *testing.T) {
	c, b := newTestCPU(true)
	clz := func(rm uint32) uint32 { return 0xE16F0F10 | rm }
	cases := []struct{ in, want uint32 }{
		{0, 32},
		{0x80000000, 0},
		{0x00010000, 15},
		{0x00000001, 31},
		{0x00F00000, 8},
	}
	for _, tc := range cases {
		c.R[1] = tc.in
		execARMWord(c, b, 0x100, clz(1))
		if c.R[0] != tc.want {
			t.Errorf("CLZ(%08x): got %d want %d", tc.in, c.R[0], tc.want)
		}
	}
}

func TestQOps(t *testing.T) {
	// Encodings: base 0xE1000050 (QADD), opcode bits 23-20 step
	// by 2 per operation; Rn at bits 19-16, Rd at bits 15-12,
	// Rm at bits 3-0.
	const qadd = 0xE1000050
	const qsub = 0xE1200050
	const qdadd = 0xE1400050
	const qdsub = 0xE1600050
	mk := func(base uint32) uint32 { return base | 2<<16 | 0<<12 | 1 } // Rd=R0, Rm=R1, Rn=R2

	c, b := newTestCPU(true)
	// QADD saturation high.
	c.R[1], c.R[2] = 0x7FFFFFFF, 1
	execARMWord(c, b, 0x100, mk(qadd))
	if c.R[0] != 0x7FFFFFFF || c.CPSR&FlagQ == 0 {
		t.Errorf("QADD sat: R0=%08x Q=%v", c.R[0], c.CPSR&FlagQ != 0)
	}
	// Q flag is sticky: a clean QADD leaves it set.
	c.R[1], c.R[2] = 3, 4
	execARMWord(c, b, 0x100, mk(qadd))
	if c.R[0] != 7 || c.CPSR&FlagQ == 0 {
		t.Errorf("QADD clean after sat: R0=%08x Q=%v", c.R[0], c.CPSR&FlagQ != 0)
	}
	c.CPSR &^= FlagQ
	// QSUB saturation low.
	c.R[1], c.R[2] = 0x80000000, 1
	execARMWord(c, b, 0x100, mk(qsub))
	if c.R[0] != 0x80000000 || c.CPSR&FlagQ == 0 {
		t.Errorf("QSUB sat: R0=%08x Q=%v", c.R[0], c.CPSR&FlagQ != 0)
	}
	c.CPSR &^= FlagQ
	// QDADD: the doubling saturates first (Rn=0x40000000 doubles
	// past the range), Q set, final result saturates too.
	c.R[1], c.R[2] = 10, 0x40000000
	execARMWord(c, b, 0x100, mk(qdadd))
	if c.R[0] != 0x7FFFFFFF || c.CPSR&FlagQ == 0 {
		t.Errorf("QDADD: R0=%08x Q=%v", c.R[0], c.CPSR&FlagQ != 0)
	}
	c.CPSR &^= FlagQ
	// QDSUB: doubling saturates (Q set) but the subtraction
	// itself lands in range.
	c.R[1], c.R[2] = 0, 0x40000000
	execARMWord(c, b, 0x100, mk(qdsub))
	if c.R[0] != 0x80000001 || c.CPSR&FlagQ == 0 {
		t.Errorf("QDSUB: R0=%08x Q=%v", c.R[0], c.CPSR&FlagQ != 0)
	}
	// QDADD in range: 5 + 2*7 = 19, Q stays clear.
	c.CPSR &^= FlagQ
	c.R[1], c.R[2] = 5, 7
	execARMWord(c, b, 0x100, mk(qdadd))
	if c.R[0] != 19 || c.CPSR&FlagQ != 0 {
		t.Errorf("QDADD clean: R0=%08x Q=%v", c.R[0], c.CPSR&FlagQ != 0)
	}
	// NZCV must be untouched by Q ops.
	c.CPSR |= FlagN | FlagV
	c.R[1], c.R[2] = 1, 2
	execARMWord(c, b, 0x100, mk(qadd))
	if c.CPSR&FlagN == 0 || c.CPSR&FlagV == 0 {
		t.Errorf("QADD clobbered NZCV: CPSR=%08x", c.CPSR)
	}
}

func TestDSPMultiplies(t *testing.T) {
	c, b := newTestCPU(true)
	// SMULBB R0, R1, R2: opcode 1011, halves from bits 6 (y, Rs)
	// and 5 (x, Rm); base includes bit 7 set, bit 4 clear.
	smulbb := uint32(0xE1600281)
	c.R[1], c.R[2] = 0x00030004, 0x00050006
	execARMWord(c, b, 0x100, smulbb)
	if c.R[0] != 24 {
		t.Errorf("SMULBB: got %d want 24", c.R[0])
	}
	// SMULTT: both top halves: 3 * 5 = 15.
	smultt := uint32(0xE16002E1)
	execARMWord(c, b, 0x100, smultt)
	if c.R[0] != 15 {
		t.Errorf("SMULTT: got %d want 15", c.R[0])
	}
	// SMULBB with negative halves: low halves are 0xFFFE (-2)
	// and 0x0003 (3): product -6.
	c.R[1], c.R[2] = 0x0000FFFE, 0x00000003
	execARMWord(c, b, 0x100, smulbb)
	if int32(c.R[0]) != -6 {
		t.Errorf("SMULBB neg: got %d want -6", int32(c.R[0]))
	}
	// SMLABB R0, R1, R2, R3: 4*6 + 100 = 124.
	smlabb := uint32(0xE1003281)
	c.R[1], c.R[2], c.R[3] = 0x00030004, 0x00050006, 100
	execARMWord(c, b, 0x100, smlabb)
	if c.R[0] != 124 {
		t.Errorf("SMLABB: got %d want 124", c.R[0])
	}
	// SMLA overflow wraps and sets Q without clamping:
	// low halves 0x7FFF * 0x7FFF = 0x3FFF0001, plus Rn
	// 0x7FFFFFFF wraps negative.
	c.CPSR &^= FlagQ
	c.R[1], c.R[2], c.R[3] = 0x00007FFF, 0x00007FFF, 0x7FFFFFFF
	execARMWord(c, b, 0x100, smlabb)
	want := uint32(int64(0x3FFF0001) + int64(0x7FFFFFFF))
	if c.R[0] != want || c.CPSR&FlagQ == 0 {
		t.Errorf("SMLA wrap: R0=%08x want %08x Q=%v", c.R[0], want, c.CPSR&FlagQ != 0)
	}
	// SMULWT R0, R1, R2: (0x00020000 * 4) >> 16 = 8.
	smulwt := uint32(0xE12002E1)
	c.R[1], c.R[2] = 0x00020000, 0x00040000
	execARMWord(c, b, 0x100, smulwt)
	if c.R[0] != 8 {
		t.Errorf("SMULWT: got %d want 8", c.R[0])
	}
	// SMLAWB R0, R1, R2, R3: (0x00010000 * 3) >> 16 + 10 = 13.
	smlawb := uint32(0xE1203281)
	c.R[1], c.R[2], c.R[3] = 0x00010000, 0x00000003, 10
	execARMWord(c, b, 0x100, smlawb)
	if c.R[0] != 13 {
		t.Errorf("SMLAWB: got %d want 13", c.R[0])
	}
	// SMLALBB R1:R0 pair (RdHi=R0 at bits 19-16, RdLo=R1 at
	// bits 15-12), Rm=R2, Rs=R3: 5 + 3*4 = 17.
	smlalbb := uint32(0xE1401382)
	c.R[0], c.R[1] = 0, 5
	c.R[2], c.R[3] = 3, 4
	execARMWord(c, b, 0x100, smlalbb)
	if c.R[1] != 17 || c.R[0] != 0 {
		t.Errorf("SMLALBB: pair=%08x:%08x want 0:17", c.R[0], c.R[1])
	}
}

func TestDoublewordTransfers(t *testing.T) {
	c, b := newTestCPU(true)
	// STRD R0, [R4] = 0xE1C400F0; LDRD R0, [R4] = 0xE1C400D0.
	c.R[4] = 0x200
	c.R[0], c.R[1] = 0xAABBCCDD, 0x11223344
	execARMWord(c, b, 0x100, 0xE1C400F0)
	if got := b.Read(0x200, 4); got != 0xAABBCCDD {
		t.Fatalf("STRD low word: got %08x", got)
	}
	if got := b.Read(0x204, 4); got != 0x11223344 {
		t.Fatalf("STRD high word: got %08x", got)
	}
	c.R[0], c.R[1] = 0, 0
	execARMWord(c, b, 0x100, 0xE1C400D0)
	if c.R[0] != 0xAABBCCDD || c.R[1] != 0x11223344 {
		t.Errorf("LDRD: got %08x:%08x", c.R[1], c.R[0])
	}
}

func TestCP15RoundTrip(t *testing.T) {
	c, b := newTestCPU(true)
	// MCR p15, 0, R0, c1, c0, 0 (control) = 0xEE010F10.
	// MRC p15, 0, R1, c1, c0, 0 = 0xEE111F10.
	c.R[0] = 0x00052078
	execARMWord(c, b, 0x100, 0xEE010F10)
	execARMWord(c, b, 0x100, 0xEE111F10)
	if c.R[1] != 0x00052078 {
		t.Errorf("CP15 control round trip: got %08x", c.R[1])
	}
	// High vectors bit drives the vector base.
	if c.VectorBase() != 0xFFFF0000 {
		t.Errorf("VectorBase with V set: got %08x", c.VectorBase())
	}
	// DTCM config: MCR p15, 0, R0, c9, c1, 0 = 0xEE090F11.
	// Value: base 0x02000000, size field 6 (32 KB).
	c.R[0] = 0x02000000 | 6<<1
	execARMWord(c, b, 0x100, 0xEE090F11)
	base, size, en := c.DTCMRegion()
	if base != 0x02000000 || size != 32768 || !en {
		t.Errorf("DTCMRegion: base=%08x size=%d en=%v", base, size, en)
	}
	// MRC into R15 loads only the flag nibble of the CPSR.
	c.CP15Write(0, 0, 0, 0xF0000000)
	execARMWord(c, b, 0x100, 0xEE10FF10) // MRC p15,0,R15,c0,c0,0
	if c.CPSR&0xF0000000 != 0xF0000000 {
		t.Errorf("MRC to R15: CPSR=%08x", c.CPSR)
	}
}

func TestCoprocessorUndefinedOnARM7(t *testing.T) {
	c, b := newTestCPU(false)
	execARMWord(c, b, 0x100, 0xEE010F10) // MCR on ARMv4T: no CP15
	if c.Mode() != ModeUND {
		t.Errorf("MCR on ARM7 should trap undefined, mode=%02x", c.Mode())
	}
	if c.R[14] != 0x104 {
		t.Errorf("UND LR: got %08x want 104", c.R[14])
	}
}

func TestBKPTPrefetchAbort(t *testing.T) {
	c, b := newTestCPU(true)
	execARMWord(c, b, 0x100, 0xE1200070) // BKPT #0
	if c.Mode() != ModeABT {
		t.Fatalf("BKPT should enter ABT mode, mode=%02x", c.Mode())
	}
	if c.R[14] != 0x104 {
		t.Errorf("ABT LR after BKPT: got %08x want 104", c.R[14])
	}
	if c.FAR != 0x100 {
		t.Errorf("FAR after BKPT: got %08x want 100", c.FAR)
	}
	if got := c.CP15Read(5, 0, 0); got != c.FSR {
		t.Errorf("CP15 FSR mirror: got %08x want %08x", got, c.FSR)
	}
	if got := c.CP15Read(6, 0, 0); got != 0x100 {
		t.Errorf("CP15 FAR: got %08x", got)
	}
}

func TestDataAbortRegisters(t *testing.T) {
	c, _ := newTestCPU(true)
	c.DataAbort(0x02000004)
	if c.Mode() != ModeABT || c.FAR != 0x02000004 || c.FSR == 0 {
		t.Errorf("DataAbort: mode=%02x FAR=%08x FSR=%08x", c.Mode(), c.FAR, c.FSR)
	}
}

func TestThumbBLXRegister(t *testing.T) {
	c, b := newTestCPU(true)
	b.Write(0x100, 2, 0x4788) // BLX R1
	c.CPSR |= BitT
	c.Jump(0x100)
	c.R[1] = 0x200
	c.Step()
	if c.Thumb() {
		t.Error("BLX register should leave Thumb state for an even target")
	}
	if c.R[14] != 0x103 {
		t.Errorf("BLX LR: got %08x want 103", c.R[14])
	}
	if c.R[15] != 0x208 {
		t.Errorf("BLX target R15: got %08x want 208", c.R[15])
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	c, _ := newTestCPU(true)
	c.R[3] = 0xCAFEBABE
	c.R[13] = 0x02004000
	c.CP15Write(1, 0, 0, 0x00052078)
	c.FSR, c.FAR = 0x5, 0x2000
	buf := make([]byte, StateSize)
	if n := c.Serialize(buf); n != StateSize {
		t.Fatalf("Serialize wrote %d bytes, StateSize=%d", n, StateSize)
	}
	d, _ := newTestCPU(true)
	if !d.Deserialize(buf) {
		t.Fatal("Deserialize refused a full state")
	}
	if d.R[3] != 0xCAFEBABE || d.R[13] != 0x02004000 {
		t.Errorf("registers did not round trip: R3=%08x R13=%08x", d.R[3], d.R[13])
	}
	if d.CP15Read(1, 0, 0) != 0x00052078 {
		t.Errorf("CP15 did not round trip: %08x", d.CP15Read(1, 0, 0))
	}
	if d.FSR != 0x5 || d.FAR != 0x2000 {
		t.Errorf("FSR/FAR did not round trip: %08x %08x", d.FSR, d.FAR)
	}
	if d.Deserialize(buf[:StateSize-1]) {
		t.Error("Deserialize accepted a short state")
	}
}

func TestResetDefaultsARM9(t *testing.T) {
	c, _ := newTestCPU(true)
	if c.Mode() != ModeSVC || c.Thumb() {
		t.Errorf("reset state: mode=%02x CPSR=%08x", c.Mode(), c.CPSR)
	}
	base, size, en := c.ITCMRegion()
	if base != 0 || size != 32768 || !en {
		t.Errorf("ITCM reset: base=%08x size=%d en=%v", base, size, en)
	}
	base, size, en = c.DTCMRegion()
	if base != 0x00800000 || size != 16384 || !en {
		t.Errorf("DTCM reset: base=%08x size=%d en=%v", base, size, en)
	}
}
