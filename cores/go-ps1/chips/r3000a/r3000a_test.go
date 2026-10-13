package r3000a

import "testing"

// ramBus is a flat little-endian test memory. Addresses
// are masked into the store, so programs can use any
// segment.
type ramBus struct{ mem []byte }

func newRAMBus() *ramBus { return &ramBus{mem: make([]byte, 1<<20)} }

func (b *ramBus) Read8(addr uint32) uint8 { return b.mem[addr&(uint32(len(b.mem))-1)] }
func (b *ramBus) Read16(addr uint32) uint16 {
	return uint16(b.Read8(addr)) | uint16(b.Read8(addr+1))<<8
}
func (b *ramBus) Read32(addr uint32) uint32 {
	return uint32(b.Read8(addr)) | uint32(b.Read8(addr+1))<<8 |
		uint32(b.Read8(addr+2))<<16 | uint32(b.Read8(addr+3))<<24
}
func (b *ramBus) Write8(addr uint32, v uint8) { b.mem[addr&(uint32(len(b.mem))-1)] = v }
func (b *ramBus) Write16(addr uint32, v uint16) {
	b.Write8(addr, uint8(v))
	b.Write8(addr+1, uint8(v>>8))
}
func (b *ramBus) Write32(addr uint32, v uint32) {
	b.Write8(addr, uint8(v))
	b.Write8(addr+1, uint8(v>>8))
	b.Write8(addr+2, uint8(v>>16))
	b.Write8(addr+3, uint8(v>>24))
}
func (b *ramBus) load(base uint32, words []uint32) {
	for i, w := range words {
		b.Write32(base+4*uint32(i), w)
	}
}

// Register numbers used below.
const (
	zero = 0
	t0   = 8
	t1   = 9
	t2   = 10
	t3   = 11
	t4   = 12
	t5   = 13
	t6   = 14
	t7   = 15
	s0   = 16
	s1   = 17
	s2   = 18
	s3   = 19
	s4   = 20
	s5   = 21
	s6   = 22
	s7   = 23
	ra   = 31
)

func rI(op, rs, rt, imm uint32) uint32 { return op<<26 | rs<<21 | rt<<16 | (imm & 0xFFFF) }
func rR(rs, rt, rd, sh, fn uint32) uint32 {
	return rs<<21 | rt<<16 | rd<<11 | sh<<6 | fn
}
func rJ(op, target uint32) uint32 { return op<<26 | (target>>2)&0x03FFFFFF }
func cop0(rs, rt, rd uint32) uint32 {
	return 0x10<<26 | rs<<21 | rt<<16 | rd<<11
}

const nop = 0

func newCPUAt(bus *ramBus, words []uint32, base uint32) *CPU {
	bus.load(base, words)
	c := New(bus)
	c.PC = base
	c.NextPC = base + 4
	return c
}

func stepN(c *CPU, n int) {
	for i := 0; i < n; i++ {
		c.Step()
	}
}

func TestArithmeticAndLogic(t *testing.T) {
	bus := newRAMBus()
	words := []uint32{
		rI(0x09, zero, t0, 40),     // addiu t0, 40
		rI(0x09, zero, t1, 2),      // addiu t1, 2
		rR(t0, t1, t2, 0, 0x21),    // addu t2 = 42
		rR(t0, t1, t3, 0, 0x23),    // subu t3 = 38
		rR(0, t1, t4, 3, 0x00),     // sll t4 = 16
		rR(0, t0, t5, 2, 0x02),     // srl t5 = 10
		rI(0x09, zero, t6, 0xFFF0), // addiu t6, -16
		rR(0, t6, t7, 2, 0x03),     // sra t7 = -4
		rR(t1, t0, s0, 0, 0x2A),    // slt s0 = 1
		rR(t0, t1, s1, 0, 0x2B),    // sltu s1 = 0
		rR(t0, t1, s2, 0, 0x24),    // and s2 = 0
		rR(t0, t1, s3, 0, 0x25),    // or s3 = 42
		rR(t0, t1, s4, 0, 0x26),    // xor s4 = 42
		rR(t0, t1, s5, 0, 0x27),    // nor s5 = ^42
		rR(t0, t1, 0, 0, 0x18),     // mult
		rR(0, 0, s6, 0, 0x12),      // mflo s6 = 80
		rR(0, 0, s7, 0, 0x10),      // mfhi s7 = 0
		rI(0x0F, zero, s0, 0x12),   // lui s0, 0x12
	}
	c := newCPUAt(bus, words, 0x1000)
	stepN(c, len(words))
	want := map[uint32]uint32{
		t0: 40, t1: 2, t2: 42, t3: 38, t4: 16, t5: 10,
		t6: 0xFFFFFFF0, t7: 0xFFFFFFFC,
		s1: 0, s2: 0, s3: 42, s4: 42, s5: ^uint32(42),
		s6: 80, s7: 0, s0: 0x00120000,
	}
	for reg, v := range want {
		if c.Regs[reg] != v {
			t.Errorf("r%d = 0x%08X, want 0x%08X", reg, c.Regs[reg], v)
		}
	}
	if c.Regs[zero] != 0 {
		t.Errorf("r0 = 0x%08X, want 0", c.Regs[zero])
	}
}

func TestLoadDelayBasic(t *testing.T) {
	bus := newRAMBus()
	bus.Write32(0x2000, 0xDEADBEEF)
	words := []uint32{
		rI(0x09, zero, t0, 0x2000), // addiu t0, 0x2000
		rI(0x23, t0, t1, 0),        // lw t1, 0(t0)
		rR(t1, zero, t2, 0, 0x21),  // addu t2, t1 (delay slot: old value 0)
		rR(t1, zero, t3, 0, 0x21),  // addu t3, t1 (loaded value)
	}
	c := newCPUAt(bus, words, 0x1000)
	stepN(c, 4)
	if c.Regs[t2] != 0 {
		t.Errorf("delay slot read t2 = 0x%08X, want 0", c.Regs[t2])
	}
	if c.Regs[t3] != 0xDEADBEEF {
		t.Errorf("post delay read t3 = 0x%08X, want 0xDEADBEEF", c.Regs[t3])
	}
}

func TestLoadDelayCancellationByWrite(t *testing.T) {
	bus := newRAMBus()
	bus.Write32(0x2000, 0xDEADBEEF)
	words := []uint32{
		rI(0x09, zero, t0, 0x2000),
		rI(0x23, t0, t1, 0),       // lw t1 (pending)
		rI(0x09, zero, t1, 7),     // addiu t1, 7: writes t1, load is cancelled
		rR(t1, zero, t2, 0, 0x21), // addu t2 = 7
		rR(t1, zero, t3, 0, 0x21), // addu t3 = 7, the load never lands
	}
	c := newCPUAt(bus, words, 0x1000)
	stepN(c, 5)
	if c.Regs[t2] != 7 || c.Regs[t3] != 7 {
		t.Errorf("t2=0x%08X t3=0x%08X, want 7 and 7 (load cancelled)", c.Regs[t2], c.Regs[t3])
	}
}

func TestLoadDelayBackToBackLoads(t *testing.T) {
	bus := newRAMBus()
	bus.Write32(0x2000, 111)
	bus.Write32(0x2004, 222)
	words := []uint32{
		rI(0x09, zero, t0, 0x2000),
		rI(0x23, t0, t1, 0),       // lw t1 = 111 (pending)
		rI(0x23, t0, t1, 4),       // lw t1 = 222 (delay slot load; does not cancel the first)
		rR(t1, zero, t2, 0, 0x21), // addu t2: reads the first load, 111
		rR(t1, zero, t3, 0, 0x21), // addu t3: reads the second load, 222
	}
	c := newCPUAt(bus, words, 0x1000)
	stepN(c, 5)
	if c.Regs[t2] != 111 {
		t.Errorf("t2 = %d, want 111 (first of two back-to-back loads lands on schedule)", c.Regs[t2])
	}
	if c.Regs[t3] != 222 {
		t.Errorf("t3 = %d, want 222", c.Regs[t3])
	}
}

func TestBranchDelayAndJal(t *testing.T) {
	bus := newRAMBus()
	words := []uint32{
		rJ(0x03, 20),                 // 0: jal 20
		rI(0x09, zero, s0, 1),        // 1: delay slot
		rI(0x09, zero, s1, 2),        // 2: return address lands here
		rI(0x04, zero, zero, 0xFFFF), // 3: beq zero, zero, -1 (halt loop)
		nop,                          // 4
		rI(0x09, zero, s2, 3),        // 5: subroutine at byte 20
		rR(ra, 0, 0, 0, 0x08),        // 6: jr ra
		rI(0x09, zero, s3, 4),        // 7: jr delay slot
	}
	c := newCPUAt(bus, words, 0)
	stepN(c, 12)
	if c.Regs[ra] != 8 {
		t.Errorf("ra = 0x%X, want 8", c.Regs[ra])
	}
	for reg, v := range map[uint32]uint32{s0: 1, s1: 2, s2: 3, s3: 4} {
		if c.Regs[reg] != v {
			t.Errorf("r%d = %d, want %d", reg, c.Regs[reg], v)
		}
	}
}

func TestBranchAndLinkUnconditionalLink(t *testing.T) {
	bus := newRAMBus()
	// bltzal with a non-negative source is NOT taken,
	// but the R3000A writes the link register anyway.
	words := []uint32{
		rI(0x09, zero, t0, 5), // addiu t0, 5
		rI(0x01, t0, 0x10, 4), // bltzal t0, +4 words (not taken)
		nop,                   // delay slot
		rI(0x09, zero, s0, 1), // fall-through marker
	}
	c := newCPUAt(bus, words, 0x1000)
	stepN(c, 4)
	if c.Regs[ra] != 0x100C {
		t.Errorf("ra = 0x%08X, want 0x100C (link written despite not-taken branch)", c.Regs[ra])
	}
	if c.Regs[s0] != 1 {
		t.Errorf("s0 = %d, want 1 (fell through)", c.Regs[s0])
	}
}

func TestUnalignedLoadStore(t *testing.T) {
	bus := newRAMBus()
	bus.Write32(0x1000, 0x11223344)
	bus.Write32(0x1004, 0x55667788)
	words := []uint32{
		rI(0x09, zero, t0, 0x1000), // base
		rI(0x09, zero, t1, 0),      // t1 = 0
		rI(0x22, t0, t1, 4),        // lwl t1, 4(t0)
		rI(0x26, t0, t1, 1),        // lwr t1, 1(t0)
		nop,                        // load delay
		rR(t1, zero, t2, 0, 0x21),  // addu t2 = unaligned word at 0x1001
		rI(0x09, zero, t3, 0x2000), // dest base
		rI(0x2A, t3, t1, 4),        // swl t1, 4(t3)
		rI(0x2E, t3, t1, 1),        // swr t1, 1(t3)
	}
	c := newCPUAt(bus, words, 0x4000)
	stepN(c, len(words))
	if c.Regs[t2] != 0x88112233 {
		t.Errorf("unaligned load = 0x%08X, want 0x88112233", c.Regs[t2])
	}
	for off, want := range map[uint32]uint8{0x2001: 0x33, 0x2002: 0x22, 0x2003: 0x11, 0x2004: 0x88} {
		if got := bus.Read8(off); got != want {
			t.Errorf("mem[0x%X] = 0x%02X, want 0x%02X", off, got, want)
		}
	}
	if bus.Read8(0x2000) != 0 || bus.Read8(0x2005) != 0 {
		t.Errorf("unaligned store touched bytes outside 0x2001-0x2004")
	}
}

func TestMultiplyDivideResults(t *testing.T) {
	bus := newRAMBus()
	words := []uint32{
		rI(0x09, zero, t0, 0xFFFA), // addiu t0, -6
		rI(0x09, zero, t1, 7),
		rR(t0, t1, 0, 0, 0x18), // mult: -42
		rR(0, 0, t2, 0, 0x12),  // mflo = -42
		rR(0, 0, t3, 0, 0x10),  // mfhi = -1
		rI(0x09, zero, t0, 100),
		rR(t0, t1, 0, 0, 0x1A), // div 100/7
		rR(0, 0, t4, 0, 0x12),  // mflo = 14
		rR(0, 0, t5, 0, 0x10),  // mfhi = 2
		rI(0x09, zero, t1, 0),
		rR(t0, t1, 0, 0, 0x1B),     // divu 100/0
		rR(0, 0, t6, 0, 0x12),      // mflo = 0xFFFFFFFF
		rR(0, 0, t7, 0, 0x10),      // mfhi = 100
		rI(0x09, zero, t0, 0xFFFB), // addiu t0, -5
		rR(t0, t1, 0, 0, 0x1A),     // div -5/0
		rR(0, 0, s0, 0, 0x12),      // mflo = 1
		rR(0, 0, s1, 0, 0x10),      // mfhi = -5
		rI(0x0F, zero, t0, 0x8000), // t0 = 0x80000000
		rI(0x09, zero, t1, 0xFFFF), // addiu t1, -1
		rR(t0, t1, 0, 0, 0x1A),     // div INT_MIN/-1
		rR(0, 0, s2, 0, 0x12),      // mflo = 0x80000000
		rR(0, 0, s3, 0, 0x10),      // mfhi = 0
	}
	c := newCPUAt(bus, words, 0x1000)
	stepN(c, len(words))
	want := map[uint32]uint32{
		t2: 0xFFFFFFD6, t3: 0xFFFFFFFF, t4: 14, t5: 2,
		t6: 0xFFFFFFFF, t7: 100, s0: 1, s1: 0xFFFFFFFB,
		s2: 0x80000000, s3: 0,
	}
	for reg, v := range want {
		if c.Regs[reg] != v {
			t.Errorf("r%d = 0x%08X, want 0x%08X", reg, c.Regs[reg], v)
		}
	}
}

func TestHiLoStallTiming(t *testing.T) {
	bus := newRAMBus()
	// Immediate consumer stalls for the busy count.
	words := []uint32{
		rI(0x09, zero, t0, 6),
		rI(0x09, zero, t1, 7),
		rR(t0, t1, 0, 0, 0x18), // mult
		rR(0, 0, t2, 0, 0x12),  // mflo
	}
	c := newCPUAt(bus, words, 0x1000)
	stepN(c, 3)
	if got := c.Step(); got != 1+multBusyCycles {
		t.Errorf("mflo right after mult charged %d cycles, want %d", got, 1+multBusyCycles)
	}
	if c.Regs[t2] != 42 {
		t.Errorf("mflo = %d, want 42", c.Regs[t2])
	}
	// Spaced consumer does not stall.
	bus2 := newRAMBus()
	spaced := []uint32{
		rI(0x09, zero, t0, 6),
		rI(0x09, zero, t1, 7),
		rR(t0, t1, 0, 0, 0x18), // mult
	}
	for i := 0; i < multBusyCycles; i++ {
		spaced = append(spaced, nop)
	}
	spaced = append(spaced, rR(0, 0, t2, 0, 0x12)) // mflo
	c3 := newCPUAt(bus2, spaced, 0x1000)
	stepN(c3, len(spaced)-1)
	if got := c3.Step(); got != 1 {
		t.Errorf("mflo after %d spacing instructions charged %d cycles, want 1", multBusyCycles, got)
	}
}

func TestOverflowException(t *testing.T) {
	bus := newRAMBus()
	words := []uint32{
		rI(0x0F, zero, t0, 0x7FFF), // lui t0, 0x7FFF
		rI(0x0D, t0, t0, 0xFFFF),   // ori t0, 0xFFFF -> 0x7FFFFFFF
		rI(0x08, t0, t1, 1),        // addi t1, t0, 1 -> overflow
	}
	c := newCPUAt(bus, words, 0x1000)
	stepN(c, 3)
	if c.PC != generalVectorBEV {
		t.Errorf("PC = 0x%08X, want exception vector 0x%08X", c.PC, uint32(generalVectorBEV))
	}
	if c.EPC != 0x1008 {
		t.Errorf("EPC = 0x%08X, want 0x1008", c.EPC)
	}
	if code := (c.Cause >> 2) & 0x1F; code != ExcOv {
		t.Errorf("ExcCode = %d, want %d (overflow)", code, ExcOv)
	}
	if c.Regs[t1] != 0 {
		t.Errorf("t1 = 0x%08X, want 0 (faulting instruction has no effect)", c.Regs[t1])
	}
}

func TestAddressErrorAndBadVAddr(t *testing.T) {
	bus := newRAMBus()
	words := []uint32{
		rI(0x09, zero, t0, 0x1001),
		rI(0x23, t0, t1, 0), // lw from odd-ish address 0x1001
	}
	c := newCPUAt(bus, words, 0x1000)
	stepN(c, 2)
	if code := (c.Cause >> 2) & 0x1F; code != ExcAdEL {
		t.Errorf("ExcCode = %d, want %d (AdEL)", code, ExcAdEL)
	}
	if c.BadVAddr != 0x1001 {
		t.Errorf("BadVAddr = 0x%08X, want 0x1001", c.BadVAddr)
	}
	if c.EPC != 0x1004 {
		t.Errorf("EPC = 0x%08X, want 0x1004", c.EPC)
	}
}

func TestSyscallAndInterrupt(t *testing.T) {
	bus := newRAMBus()
	c := newCPUAt(bus, []uint32{rR(0, 0, 0, 0, 0x0C)}, 0x1000)
	c.Step()
	if code := (c.Cause >> 2) & 0x1F; code != ExcSys {
		t.Errorf("syscall ExcCode = %d, want %d", code, ExcSys)
	}
	if c.EPC != 0x1000 || c.PC != generalVectorBEV {
		t.Errorf("syscall EPC/PC = 0x%08X/0x%08X, want 0x1000/0x%08X", c.EPC, c.PC, uint32(generalVectorBEV))
	}

	bus2 := newRAMBus()
	c2 := newCPUAt(bus2, []uint32{rI(0x09, zero, t0, 1)}, 0x1000)
	c2.SR = 0x00000401 // IEc + IM for IP2
	c2.SetInterrupt(true)
	c2.Step()
	if c2.Regs[t0] != 0 {
		t.Errorf("interrupt taken after the instruction ran; t0 = %d, want 0", c2.Regs[t0])
	}
	if c2.EPC != 0x1000 {
		t.Errorf("interrupt EPC = 0x%08X, want 0x1000", c2.EPC)
	}
	if code := (c2.Cause >> 2) & 0x1F; code != ExcInt {
		t.Errorf("interrupt ExcCode = %d, want 0", code)
	}
}

func TestMFC0LoadDelay(t *testing.T) {
	bus := newRAMBus()
	words := []uint32{
		rI(0x09, zero, t0, 1),     // addiu t0, 1
		cop0(0x04, t0, cp0SR),     // mtc0 t0, SR
		cop0(0x00, t1, cp0SR),     // mfc0 t1, SR (delayed)
		rR(t1, zero, t2, 0, 0x21), // addu t2: old t1 = 0
		rR(t1, zero, t3, 0, 0x21), // addu t3: SR = 1
	}
	c := newCPUAt(bus, words, 0x1000)
	stepN(c, 5)
	if c.Regs[t2] != 0 || c.Regs[t3] != 1 {
		t.Errorf("t2=%d t3=%d, want 0 and 1 (mfc0 has a load delay)", c.Regs[t2], c.Regs[t3])
	}
	if got := c.readCP0(cp0PRId); got != 0x00000002 {
		t.Errorf("PRId = 0x%08X, want 0x00000002", got)
	}
}

func TestCacheIsolation(t *testing.T) {
	bus := newRAMBus()
	words := []uint32{
		rI(0x09, zero, t0, 0x2000), // base
		rI(0x0F, zero, t1, 0xCAFE), // lui
		rI(0x0D, t1, t1, 0xBABE),   // ori -> 0xCAFEBABE
		rI(0x2B, t0, t1, 0),        // sw
		rI(0x23, t0, t2, 0),        // lw
		nop,
		rR(t2, zero, t3, 0, 0x21), // addu t3
	}
	c := newCPUAt(bus, words, 0x1000)
	c.SR = 0x00010000 // Isc: isolate the data cache
	stepN(c, len(words))
	if c.Regs[t3] != 0xCAFEBABE {
		t.Errorf("isolated load = 0x%08X, want 0xCAFEBABE", c.Regs[t3])
	}
	if bus.Read32(0x2000) != 0 {
		t.Errorf("isolated store leaked to the bus: mem = 0x%08X", bus.Read32(0x2000))
	}
	// A second CPU without isolation sees the (empty) bus.
	c2 := newCPUAt(bus, []uint32{
		rI(0x09, zero, t0, 0x2000),
		rI(0x23, t0, t2, 0),
		nop,
		rR(t2, zero, t3, 0, 0x21),
	}, 0x1000)
	stepN(c2, 4)
	if c2.Regs[t3] != 0 {
		t.Errorf("non-isolated load = 0x%08X, want 0", c2.Regs[t3])
	}
}

func TestSerializeRoundTripWithPendingLoad(t *testing.T) {
	bus := newRAMBus()
	bus.Write32(0x2000, 0x12345678)
	words := []uint32{
		rI(0x09, zero, t0, 0x2000),
		rI(0x23, t0, t1, 0),       // lw t1 (pending at save time)
		rR(t1, zero, t2, 0, 0x21), // addu t2 (delay slot)
		rR(t1, zero, t3, 0, 0x21), // addu t3 (loaded value)
	}
	c := newCPUAt(bus, words, 0x1000)
	stepN(c, 2) // the load is now pending, not yet landed
	state := c.Serialize()
	if len(state) != StateSize {
		t.Fatalf("state size = %d, want %d", len(state), StateSize)
	}
	c2 := New(bus)
	if err := c2.Deserialize(state); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	stepN(c, 2)
	stepN(c2, 2)
	if c.Regs != c2.Regs {
		t.Errorf("registers diverged after restore: %v vs %v", c.Regs, c2.Regs)
	}
	if c2.Regs[t3] != 0x12345678 {
		t.Errorf("restored pending load produced t3 = 0x%08X, want 0x12345678", c2.Regs[t3])
	}
	if err := c2.Deserialize([]byte{1, 2, 3}); err == nil {
		t.Errorf("short state accepted, want error")
	}
}
