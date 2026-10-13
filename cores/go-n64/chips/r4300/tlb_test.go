package r4300

import "testing"

// programTLB installs, by executing real CP0 instructions,
// a 4 KB page pair at index 0: VA 0x00400000 -> PA
// 0x00100000 (even page, EntryLo0) and VA 0x00401000 -> PA
// 0x00101000 (odd page, EntryLo1). lo0/lo1 are parameters so
// tests can vary Valid and Dirty.
func programTLB(lo0, lo1 uint32) []uint32 {
	return []uint32{
		lui(rT0, 0x0040),         // t0 = 0x00400000
		mtc0(rT0, Cp0EntryHi),    // VPN2 = 0x200, ASID 0
		mtc0(rZero, Cp0PageMask), // 4 KB pages
		addiu(rT1, rZero, int16(lo0)),
		mtc0(rT1, Cp0EntryLo0),
		addiu(rT1, rZero, int16(lo1)),
		mtc0(rT1, Cp0EntryLo1),
		mtc0(rZero, Cp0Index),
		0x42000002, // tlbwi
	}
}

const (
	loEven = 0x401F // PFN 0x100, C=3, D=1, V=1, G=1
	loOdd  = 0x405F // PFN 0x101, C=3, D=1, V=1, G=1
)

func TestTLBMappedStoreAndLoad(t *testing.T) {
	prog := append(programTLB(loEven, loOdd),
		lui(rT2, 0x1234),
		ori(rT2, rT2, 0x5678),
		sw(rT2, rT0, 0),      // VA 0x00400000 -> PA 0x00100000
		lw(rT3, rT0, 0),      // read back through the TLB
		sw(rT2, rT0, 0x1000), // odd page -> PA 0x00101000
	)
	c, bus := setupCPU(prog...)
	run(c, len(prog))
	if got := bus.Read(0x100000, 4); got != 0x12345678 {
		t.Errorf("even page store: phys %08x", got)
	}
	if got := bus.Read(0x101000, 4); got != 0x12345678 {
		t.Errorf("odd page store: phys %08x", got)
	}
	if c.Reg[rT3] != 0x12345678 {
		t.Errorf("tlb load: got %x", c.Reg[rT3])
	}
}

func TestTLBRefillVectorAndRegisters(t *testing.T) {
	prog := []uint32{
		lui(rT0, 0x0080), // VA 0x00800000, unmapped
		sw(rT0, rT0, 0),
	}
	c, _ := setupCPU(prog...)
	run(c, 2)
	if got := (c.CP0[Cp0Cause] & CauseExcCode) >> 2; got != ExcTLBS {
		t.Errorf("cause: got %d want TLBS", got)
	}
	// Refill with EXL clear uses vector offset 0.
	if c.PC != signExtend32(0x80000000) {
		t.Errorf("refill vector: got %x", c.PC)
	}
	if c.CP0[Cp0BadVAddr] != signExtend32(0x00800000) {
		t.Errorf("badvaddr: got %x", c.CP0[Cp0BadVAddr])
	}
	if c.CP0[Cp0Status]&StatusEXL == 0 {
		t.Error("EXL not set after refill")
	}
	if got := (c.CP0[Cp0Context] >> 4) & 0x7FFFF; got != 0x00800000>>13 {
		t.Errorf("context vpn: got %x", got)
	}
}

func TestTLBInvalidUsesGeneralVector(t *testing.T) {
	// Same mapping but Valid clear in both halves.
	prog := append(programTLB(loEven&^2, loOdd&^2),
		lui(rT0, 0x0040),
		lw(rT3, rT0, 0),
	)
	c, _ := setupCPU(prog...)
	run(c, len(prog))
	if got := (c.CP0[Cp0Cause] & CauseExcCode) >> 2; got != ExcTLBL {
		t.Errorf("cause: got %d want TLBL", got)
	}
	if c.PC != signExtend32(0x80000180) {
		t.Errorf("invalid vector: got %x want general", c.PC)
	}
}

func TestTLBModifiedOnCleanStore(t *testing.T) {
	// Valid set, Dirty clear: loads work, stores raise Mod.
	prog := append(programTLB(loEven&^4, loOdd&^4),
		lui(rT0, 0x0040),
		sw(rT0, rT0, 0),
	)
	c, _ := setupCPU(prog...)
	run(c, len(prog))
	if got := (c.CP0[Cp0Cause] & CauseExcCode) >> 2; got != ExcTLBMod {
		t.Errorf("cause: got %d want TLBMod", got)
	}
}

func TestTLBProbe(t *testing.T) {
	prog := append(programTLB(loEven, loOdd),
		lui(rT0, 0x0040),
		mtc0(rT0, Cp0EntryHi),
		0x42000008, // tlbp: hit at index 0
		mfc0(rT4, Cp0Index),
		lui(rT0, 0x0060), // unmapped VPN
		mtc0(rT0, Cp0EntryHi),
		0x42000008, // tlbp: miss, P bit set
		mfc0(rS0, Cp0Index),
	)
	c, _ := setupCPU(prog...)
	run(c, len(prog))
	if c.Reg[rT4] != 0 {
		t.Errorf("tlbp hit: index %x want 0", c.Reg[rT4])
	}
	if c.Reg[rS0]&0x80000000 == 0 {
		t.Errorf("tlbp miss: P bit not set in %x", c.Reg[rS0])
	}
}

func TestTLBLargerPage(t *testing.T) {
	// 16 KB pages: PageMask = 3<<13. Pair covers 32 KB:
	// VA 0x00400000..0x00407FFF. EntryHi VPN2 key is
	// vaddr>>15, so EntryHi = 0x00400000 still encodes it
	// (the low bits masked by the compare shift).
	prog := []uint32{
		lui(rT0, 0x0040),
		mtc0(rT0, Cp0EntryHi),
		addiu(rT1, rZero, 0x6000), // PageMask field value 3<<13 = 0x6000
		mtc0(rT1, Cp0PageMask),
		addiu(rT1, rZero, int16(loEven)),
		mtc0(rT1, Cp0EntryLo0),
		addiu(rT1, rZero, int16(loOdd)),
		mtc0(rT1, Cp0EntryLo1),
		mtc0(rZero, Cp0Index),
		0x42000002, // tlbwi
		lui(rT2, 0x0BAD),
		sw(rT2, rT0, 0x4000), // VA 0x00404000: odd half (16K page 1) -> PA 0x101000 + 0x0? offset within page
		lw(rT3, rT0, 0x4000),
	}
	c, bus := setupCPU(prog...)
	run(c, len(prog))
	// VA 0x00404000: half = (vaddr>>14)&1 = 1 -> EntryLo1,
	// PA = 0x101000 + (0x4000 & 0x3FFF) = 0x101000.
	if got := bus.Read(0x101000, 4); got != 0x0BAD0000 {
		t.Errorf("16k page store: phys %08x at 0x101000", got)
	}
	if c.Reg[rT3] != 0x0BAD0000 {
		t.Errorf("16k page load: got %x", c.Reg[rT3])
	}
}

func TestERETReturnsAndClearsEXL(t *testing.T) {
	prog := []uint32{
		0x0000000C, // syscall
	}
	c, bus := setupCPU(prog...)
	// Handler at the general vector: bump a counter cell,
	// then eret.
	handler := []uint32{
		lui(rT0, 0x8000),
		lw(rT1, rT0, 0x40),
		addiu(rT1, rT1, 1),
		sw(rT1, rT0, 0x40),
		0x42000018, // eret
	}
	for i, in := range handler {
		bus.load32(0x180+uint32(i*4), in)
	}
	run(c, 1) // syscall -> vector
	if c.PC != signExtend32(0x80000180) {
		t.Fatalf("no vector: pc %x", c.PC)
	}
	run(c, 5) // handler + eret -> back to EPC (progBase) with EXL clear
	if c.CP0[Cp0Status]&StatusEXL != 0 {
		t.Error("EXL still set after eret")
	}
	if got := bus.Read(0x40, 4); got != 1 {
		t.Errorf("handler ran %d times", got)
	}
	if c.PC != signExtend32(progBase) {
		t.Errorf("eret target: got %x", c.PC)
	}
}
