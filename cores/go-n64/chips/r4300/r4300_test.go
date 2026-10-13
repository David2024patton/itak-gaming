package r4300

import (
	"encoding/binary"
	"testing"
)

// testBus is a flat 16 MB physical RAM for CPU tests.
type testBus struct{ mem []byte }

func newTestBus() *testBus { return &testBus{mem: make([]byte, 16<<20)} }

func (b *testBus) Read(addr uint32, size int) uint64 {
	var v uint64
	for i := 0; i < size; i++ {
		v = v<<8 | uint64(b.mem[addr+uint32(i)])
	}
	return v
}

func (b *testBus) Write(addr uint32, size int, val uint64) {
	for i := 0; i < size; i++ {
		b.mem[addr+uint32(i)] = byte(val >> (8 * uint(size-1-i)))
	}
}

func (b *testBus) load32(paddr uint32, instr uint32) {
	binary.BigEndian.PutUint32(b.mem[paddr:], instr)
}

// Assembler helpers.
func rr(rs, rt, rd, sa, funct uint32) uint32 {
	return rs<<21 | rt<<16 | rd<<11 | sa<<6 | funct
}
func ii(op, rs, rt uint32, imm uint16) uint32 {
	return op<<26 | rs<<21 | rt<<16 | uint32(imm)
}
func lui(rt uint32, imm uint16) uint32 { return ii(0x0F, 0, rt, imm) }
func ori(rt, rs uint32, imm uint16) uint32 {
	return ii(0x0D, rs, rt, imm)
}
func addiu(rt, rs uint32, imm int16) uint32 { return ii(0x09, rs, rt, uint16(imm)) }
func addi(rt, rs uint32, imm int16) uint32  { return ii(0x08, rs, rt, uint16(imm)) }
func lw(rt, rs uint32, off int16) uint32    { return ii(0x23, rs, rt, uint16(off)) }
func sw(rt, rs uint32, off int16) uint32    { return ii(0x2B, rs, rt, uint16(off)) }
func beq(rs, rt uint32, off int16) uint32   { return ii(0x04, rs, rt, uint16(off)) }
func bne(rs, rt uint32, off int16) uint32   { return ii(0x05, rs, rt, uint16(off)) }
func mtc0(rt, rd uint32) uint32 {
	return 0x10<<26 | 4<<21 | rt<<16 | rd<<11
}
func mfc0(rt, rd uint32) uint32 {
	return 0x10<<26 | 0<<21 | rt<<16 | rd<<11
}
func nop() uint32 { return 0 }

const (
	rZero = 0
	rV0   = 2
	rA0   = 4
	rT0   = 8
	rT1   = 9
	rT2   = 10
	rT3   = 11
	rT4   = 12
	rS0   = 16
	rS1   = 17
	rRA   = 31
)

const progBase = 0x80000000 // kseg0, physical 0

// setupCPU returns a CPU with kernel-mode Status (BEV
// clear), the program loaded at physical 0, PC at progBase.
func setupCPU(prog ...uint32) (*CPU, *testBus) {
	bus := newTestBus()
	for i, in := range prog {
		bus.load32(uint32(i*4), in)
	}
	c := New(bus)
	c.CP0[Cp0Status] = StatusCU0 | StatusCU1
	c.PC = signExtend32(progBase)
	c.NPC = c.PC + 4
	return c, bus
}

func run(c *CPU, steps int) {
	for i := 0; i < steps; i++ {
		c.Step()
	}
}

func TestArithmeticVectors(t *testing.T) {
	c, _ := setupCPU(
		lui(rT0, 0x1234),
		ori(rT0, rT0, 0x5678),       // t0 = 0x12345678
		rr(rT0, rT0, rT1, 0, 0x21),  // addu t1 = 0x2468ACF0
		rr(rT0, rT0, rT2, 0, 0x2D),  // daddu t2 = 0x2468ACF0
		addiu(rT3, rZero, -1),       // t3 = -1
		rr(rT3, rT0, rT4, 0, 0x2C),  // dadd t4 = 0x12345677
		rr(rT0, rT0, rT0, 16, 0x38), // dsll t0 = 0x123456780000
	)
	run(c, 7)
	if c.Reg[rT1] != 0x2468ACF0 {
		t.Errorf("addu: got %x", c.Reg[rT1])
	}
	if c.Reg[rT2] != 0x2468ACF0 {
		t.Errorf("daddu: got %x", c.Reg[rT2])
	}
	if c.Reg[rT4] != 0x12345677 {
		t.Errorf("dadd: got %x", c.Reg[rT4])
	}
	if c.Reg[rT0] != 0x123456780000 {
		t.Errorf("dsll: got %x", c.Reg[rT0])
	}
	if c.Reg[rT3] != 0xFFFFFFFFFFFFFFFF {
		t.Errorf("addiu sign extension: got %x", c.Reg[rT3])
	}
}

func TestMultiplyDivideVectors(t *testing.T) {
	c, _ := setupCPU(
		addiu(rT0, rZero, -3),
		addiu(rT1, rZero, 7),
		rr(rT0, rT1, 0, 0, 0x18), // mult: -21
		rr(0, 0, rT2, 0, 0x12),   // mflo
		rr(0, 0, rT3, 0, 0x10),   // mfhi = -1 (sign extended)
		rr(rT0, rT1, 0, 0, 0x1C), // dmult
		rr(0, 0, rT4, 0, 0x12),   // mflo
		rr(rT1, rT0, 0, 0, 0x1F), // ddivu 7 / 0xFFFF...FD
	)
	run(c, 8)
	if int64(c.Reg[rT2]) != -21 {
		t.Errorf("mult lo: got %d", int64(c.Reg[rT2]))
	}
	if c.Reg[rT3] != 0xFFFFFFFFFFFFFFFF {
		t.Errorf("mult hi: got %x", c.Reg[rT3])
	}
	if int64(c.Reg[rT4]) != -21 {
		t.Errorf("dmult lo: got %d", int64(c.Reg[rT4]))
	}
}

func TestDivideByZeroDocumented(t *testing.T) {
	c, _ := setupCPU(
		addiu(rT0, rZero, 42),
		addiu(rT1, rZero, 0),
		rr(rT0, rT1, 0, 0, 0x1A), // div by zero
		rr(0, 0, rT2, 0, 0x12),   // mflo = -1 (dividend >= 0)
		rr(0, 0, rT3, 0, 0x10),   // mfhi = 42
	)
	run(c, 5)
	if c.Reg[rT2] != 0xFFFFFFFFFFFFFFFF {
		t.Errorf("div0 lo: got %x", c.Reg[rT2])
	}
	if c.Reg[rT3] != 42 {
		t.Errorf("div0 hi: got %x", c.Reg[rT3])
	}
}

func TestAddOverflowTraps(t *testing.T) {
	c, _ := setupCPU(
		lui(rT0, 0x7FFF),
		ori(rT0, rT0, 0xFFFF), // t0 = 0x7FFFFFFF
		addi(rT1, rT0, 1),     // overflow
	)
	run(c, 2)
	c.Step() // overflow raises
	if got := (c.CP0[Cp0Cause] & CauseExcCode) >> 2; got != ExcOverflow {
		t.Errorf("cause: got %d want overflow", got)
	}
	if c.CP0[Cp0EPC] != signExtend32(progBase+8) {
		t.Errorf("epc: got %x", c.CP0[Cp0EPC])
	}
	if c.PC != signExtend32(0x80000180) {
		t.Errorf("vector: got %x", c.PC)
	}
}

func TestDelaySlotExecutes(t *testing.T) {
	c, _ := setupCPU(
		addiu(rT0, rZero, 1),
		beq(rZero, rZero, 2), // to index 4
		addiu(rT0, rT0, 10),  // delay slot: t0 = 11
		addiu(rT0, rT0, 100), // skipped
		addiu(rT1, rT0, 1),   // t1 = 12
	)
	run(c, 4)
	if c.Reg[rT0] != 11 {
		t.Errorf("delay slot: t0 = %d, want 11", c.Reg[rT0])
	}
	if c.Reg[rT1] != 12 {
		t.Errorf("delay slot: t1 = %d, want 12", c.Reg[rT1])
	}
}

func TestBranchLikelyNullifies(t *testing.T) {
	// A not-taken branch-likely nullifies its delay slot.
	c2, _ := setupCPU(
		addiu(rT0, rZero, 1),
		ii(0x15, rT0, rT0, 2), // bnel t0, t0: equal, not taken, nullify next
		addiu(rT0, rT0, 10),   // nullified
		addiu(rT1, rT0, 1),    // executes: t1 = 2
	)
	run(c2, 4)
	if c2.Reg[rT0] != 1 || c2.Reg[rT1] != 2 {
		t.Errorf("likely nullify: t0=%d t1=%d", c2.Reg[rT0], c2.Reg[rT1])
	}
}

func TestExceptionInDelaySlotSetsBD(t *testing.T) {
	c, _ := setupCPU(
		beq(rZero, rZero, 1), // to index 2
		0x0000000C,           // syscall in the delay slot
		nop(),
	)
	run(c, 2)
	if c.CP0[Cp0EPC] != signExtend32(progBase) {
		t.Errorf("epc should be the branch: got %x", c.CP0[Cp0EPC])
	}
	if c.CP0[Cp0Cause]&CauseBD == 0 {
		t.Error("BD not set in Cause")
	}
	if got := (c.CP0[Cp0Cause] & CauseExcCode) >> 2; got != ExcSyscall {
		t.Errorf("cause: got %d", got)
	}
}

func TestUnalignedLoadAddressError(t *testing.T) {
	c, _ := setupCPU(
		lui(rT0, 0x8000),
		lw(rT1, rT0, 2), // address 0x80000002: misaligned
	)
	run(c, 2)
	if got := (c.CP0[Cp0Cause] & CauseExcCode) >> 2; got != ExcAdEL {
		t.Errorf("cause: got %d want AdEL", got)
	}
	if c.CP0[Cp0BadVAddr] != signExtend32(0x80000002) {
		t.Errorf("badvaddr: got %x", c.CP0[Cp0BadVAddr])
	}
}

func TestUserModeSegmentFaultAndCpU(t *testing.T) {
	c, _ := setupCPU(
		lui(rT0, 0x8000),
		lw(rT1, rT0, 0),
	)
	// Drop to user mode: KSU=2 (user), EXL/ERL clear.
	// Even fetching from kseg0 faults in user mode.
	c.CP0[Cp0Status] = StatusCU1 | 2<<3
	run(c, 2)
	if got := (c.CP0[Cp0Cause] & CauseExcCode) >> 2; got != ExcAdEL {
		t.Errorf("user kseg0: cause %d want AdEL", got)
	}
	// COP0 in user mode is unusable. The instruction must
	// be fetched from kuseg, so map VA 0 -> PA 0 through
	// the TLB and place the mfc0 at VA 0x100.
	c2, bus2 := setupCPU(nop())
	c2.TLB[0] = TLBEntry{
		PageMask: 0,
		EntryHi:  0,    // VPN2 0, ASID 0
		EntryLo0: 0x1F, // PFN 0, C=3, D, V, G
		EntryLo1: 0x405F,
	}
	bus2.load32(0x100, mfc0(rT0, Cp0Status))
	c2.CP0[Cp0Status] = StatusCU1 | 2<<3
	c2.PC = 0x100
	c2.NPC = 0x104
	run(c2, 1)
	if got := (c2.CP0[Cp0Cause] & CauseExcCode) >> 2; got != ExcCpU {
		t.Errorf("user cop0: cause %d want CpU", got)
	}
	if ce := (c2.CP0[Cp0Cause] & CauseCE) >> 28; ce != 0 {
		t.Errorf("user cop0: CE %d want 0", ce)
	}
}

func TestCountCompareInterrupt(t *testing.T) {
	c, bus := setupCPU(nop(), nop(), nop(), nop())
	_ = bus
	// Handler at the special interrupt vector 0x80000200:
	// record v0 = 0x600D and eret.
	handler := []uint32{
		addiu(rV0, rZero, 0x600D&0x7FFF),
		0x42000018, // eret
	}
	for i, in := range handler {
		bus.load32(0x200+uint32(i*4), in)
	}
	c.CP0[Cp0Cause] = CauseIV
	c.CP0[Cp0Status] = StatusCU0 | StatusIE | (1 << 15) // IM7
	c.CP0[Cp0Count] = 0
	c.CP0[Cp0Compare] = 3
	run(c, 12)
	if c.Reg[rV0] != 0x600D {
		t.Errorf("timer interrupt handler did not run: v0=%x cause=%x count=%d",
			c.Reg[rV0], c.CP0[Cp0Cause], c.CP0[Cp0Count])
	}
}

func TestLLSC(t *testing.T) {
	c, bus := setupCPU(
		lui(rT0, 0x8000),
		ii(0x30, rT0, rT1, 0x40), // ll t1, 0x40(t0)
		addiu(rT1, rT1, 1),
		ii(0x38, rT0, rT1, 0x40), // sc t1: succeeds, t1=1
		ii(0x38, rT0, rT2, 0x40), // sc t2 without ll: fails, t2=0
	)
	// Seed the linked cell at physical 0x40 with 41.
	bus.Write(0x40, 4, 41)
	run(c, 5)
	if c.Reg[rT1] != 1 {
		t.Errorf("sc after ll: t1=%d want 1", c.Reg[rT1])
	}
	if c.Reg[rT2] != 0 {
		t.Errorf("sc without ll: t2=%d want 0", c.Reg[rT2])
	}
	if got := bus.Read(0x40, 4); got != 42 {
		t.Errorf("cell after sc: got %d want 42", got)
	}
}

func TestLoadStorePartialBigEndian(t *testing.T) {
	c, bus := setupCPU(
		lui(rT0, 0x8000),
		// Store 0xAABBCCDD at phys 0x100.
		lui(rT1, 0xAABB),
		ori(rT1, rT1, 0xCCDD),
		sw(rT1, rT0, 0x100),
		// LWL at 0x101 into zeroed t2: bytes  BB CC DD fill top.
		ii(0x22, rT0, rT2, 0x101), // lwl t2, 0x101(t0)
		// LWR at 0x102: bytes AA BB CC fill bottom of t3 (t3 starts 0xFF.. marker via lui).
		lui(rT3, 0xFF00),
		ii(0x26, rT0, rT3, 0x102), // lwr t3, 0x102(t0)
		// SWL of 0x11223344 at 0x201: stores 11 22 33 at 0x201..0x203.
		lui(rT4, 0x1122),
		ori(rT4, rT4, 0x3344),
		ii(0x2A, rT0, rT4, 0x201), // swl t4, 0x201(t0)
		// SWR of 0x11223344 at 0x302: the low bytes of the
		// register fill memory from the unit start, so
		// 0x300 gets 0x22, 0x301 gets 0x33, 0x302 gets 0x44.
		ii(0x2E, rT0, rT4, 0x302), // swr t4, 0x302(t0)
	)
	run(c, 11)
	if got := bus.Read(0x100, 4); got != 0xAABBCCDD {
		t.Fatalf("seed store: %08x", got)
	}
	if got := uint32(c.Reg[rT2]); got != 0xBBCCDD00 {
		t.Errorf("lwl: got %08x want BBCCDD00", got)
	}
	if got := uint32(c.Reg[rT3]); got != 0xFFAABBCC {
		t.Errorf("lwr: got %08x want FFAABBCC", got)
	}
	// SWL at 0x201 (k=1): stores rt's top 3 bytes (11 22 33)
	// at 0x201,0x202,0x203.
	if got := bus.Read(0x200, 4); got != 0x00112233 {
		t.Errorf("swl bytes: got %08x want 00112233", got)
	}
	// SWR at 0x302 (k=2): mem[0x300]=0x22, mem[0x301]=0x33,
	// mem[0x302]=0x44 (the byte at the address is the
	// register's lowest byte).
	if got := bus.Read(0x300, 4); got != 0x22334400 {
		t.Errorf("swr bytes: got %08x want 22334400", got)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	prog := []uint32{
		lui(rT0, 0x1111),
		ori(rT0, rT0, 0x2222),
		rr(rT0, rT0, rT1, 0, 0x2D), // daddu t1, t0, t0
		lui(rT2, 0x8000),
		sw(rT0, rT2, 0x80),
	}
	c1, b1 := setupCPU(prog...)
	run(c1, 2)
	state := make([]byte, SerializeSize)
	if n := c1.Serialize(state); n != SerializeSize {
		t.Fatalf("serialize size %d want %d", n, SerializeSize)
	}
	run(c1, 3)
	c2, b2 := setupCPU(prog...)
	if err := c2.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	run(c2, 3)
	for i := range c1.Reg {
		if c1.Reg[i] != c2.Reg[i] {
			t.Errorf("reg %d: %x != %x", i, c1.Reg[i], c2.Reg[i])
		}
	}
	if b1.Read(0x80, 4) != b2.Read(0x80, 4) {
		t.Error("memory diverged after restore")
	}
}
