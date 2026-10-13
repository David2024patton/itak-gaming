package m68ec000

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// memBus is a flat big endian memory double.
type memBus struct{ b []byte }

func (m *memBus) Read8(a uint32) uint8 { return m.b[a] }
func (m *memBus) Read16(a uint32) uint16 {
	return binary.BigEndian.Uint16(m.b[a:])
}
func (m *memBus) Read32(a uint32) uint32 {
	return binary.BigEndian.Uint32(m.b[a:])
}
func (m *memBus) Write8(a uint32, v uint8) { m.b[a] = v }
func (m *memBus) Write16(a uint32, v uint16) {
	binary.BigEndian.PutUint16(m.b[a:], v)
}
func (m *memBus) Write32(a uint32, v uint32) {
	binary.BigEndian.PutUint32(m.b[a:], v)
}

func (m *memBus) w16(a uint32, v uint16) { m.Write16(a, v) }
func (m *memBus) w32(a uint32, v uint32) { m.Write32(a, v) }
func (m *memBus) r16(a uint32) uint16    { return m.Read16(a) }
func (m *memBus) r32(a uint32) uint32    { return m.Read32(a) }

// newCPU builds a CPU over fresh memory with the reset
// vectors pointing at stack 0x8000 and program 0x1000.
func newCPU() (*CPU, *memBus) {
	bus := &memBus{b: make([]byte, 1<<20)}
	bus.w32(0, 0x8000)
	bus.w32(4, 0x1000)
	c := New(bus)
	c.Reset()
	return c, bus
}

// load writes instruction words at addr.
func load(bus *memBus, addr uint32, words ...uint16) {
	for i, w := range words {
		bus.w16(addr+uint32(2*i), w)
	}
}

// exec1 places one instruction at 0x1000 and steps it.
func exec1(c *CPU, bus *memBus, op uint16, exts ...uint16) int {
	load(bus, 0x1000, append([]uint16{op}, exts...)...)
	c.pc = 0x1000
	return c.Step()
}

func flag(t *testing.T, c *CPU, f uint16, want bool, name string) {
	t.Helper()
	if got := c.SR()&f != 0; got != want {
		t.Fatalf("%s flag: got %v, want %v (SR=%04X)", name, got, want, c.SR())
	}
}

func TestMoveAndMoveq(t *testing.T) {
	c, bus := newCPU()
	exec1(c, bus, 0x203C, 0x1234, 0x5678) // MOVE.L #$12345678, D0
	if c.D(0) != 0x12345678 {
		t.Fatalf("D0=%08X", c.D(0))
	}
	flag(t, c, srN, false, "N")
	flag(t, c, srZ, false, "Z")
	exec1(c, bus, 0x70FE) // MOVEQ #-2, D0
	if c.D(0) != 0xFFFFFFFE {
		t.Fatalf("D0=%08X", c.D(0))
	}
	flag(t, c, srN, true, "N")
	exec1(c, bus, 0x7000) // MOVEQ #0, D0
	flag(t, c, srZ, true, "Z")
}

func TestAddxSubxChains(t *testing.T) {
	c, bus := newCPU()
	c.d[0] = 0xFFFF
	c.d[2] = 1
	exec1(c, bus, 0xD042) // ADD.W D2, D0
	if c.D(0)&0xFFFF != 0 {
		t.Fatalf("D0 low=%04X", c.D(0)&0xFFFF)
	}
	flag(t, c, srX, true, "X")
	flag(t, c, srZ, true, "Z")
	c.d[1] = 0xFFFF
	c.d[3] = 0
	exec1(c, bus, 0xD343) // ADDX.W D3, D1
	if c.D(1)&0xFFFF != 0 {
		t.Fatalf("D1 low=%04X", c.D(1)&0xFFFF)
	}
	flag(t, c, srX, true, "X")
	// Multi precision Z rule: zero result with Z
	// already set keeps Z set.
	flag(t, c, srZ, true, "Z")

	c2, bus2 := newCPU()
	c2.d[0] = 0
	c2.d[2] = 1
	exec1(c2, bus2, 0x9042) // SUB.W D2, D0
	if c2.D(0)&0xFFFF != 0xFFFF {
		t.Fatalf("D0 low=%04X", c2.D(0)&0xFFFF)
	}
	flag(t, c2, srX, true, "X")
	c2.d[1] = 0
	c2.d[3] = 0
	exec1(c2, bus2, 0x9343) // SUBX.W D3, D1
	if c2.D(1)&0xFFFF != 0xFFFF {
		t.Fatalf("D1 low=%04X", c2.D(1)&0xFFFF)
	}
	flag(t, c2, srX, true, "X")
}

func TestABCDSBCD(t *testing.T) {
	c, bus := newCPU()
	c.d[0] = 0x27
	c.d[1] = 0x15
	exec1(c, bus, 0xC300) // ABCD D0, D1
	if c.D(1)&0xFF != 0x42 {
		t.Fatalf("ABCD 15+27: D1=%02X", c.D(1)&0xFF)
	}
	flag(t, c, srC, false, "C")
	flag(t, c, srX, false, "X")
	flag(t, c, srZ, false, "Z")

	c2, bus2 := newCPU()
	c2.d[0] = 0x01
	c2.d[1] = 0x99
	exec1(c2, bus2, 0xC300) // ABCD D0, D1: 99+1
	if c2.D(1)&0xFF != 0x00 {
		t.Fatalf("ABCD 99+01: D1=%02X", c2.D(1)&0xFF)
	}
	flag(t, c2, srC, true, "C")
	flag(t, c2, srX, true, "X")
	// Z was clear before a zero result, so the multi
	// precision rule leaves it clear.
	flag(t, c2, srZ, false, "Z")
	flag(t, c2, srV, false, "V")

	c3, bus3 := newCPU()
	c3.d[0] = 0x01
	c3.d[1] = 0x00
	exec1(c3, bus3, 0x8300) // SBCD D0, D1: 00-01
	if c3.D(1)&0xFF != 0x99 {
		t.Fatalf("SBCD 00-01: D1=%02X", c3.D(1)&0xFF)
	}
	flag(t, c3, srC, true, "C")
	flag(t, c3, srX, true, "X")
	flag(t, c3, srN, true, "N")

	c4, bus4 := newCPU()
	c4.d[0] = 0x27
	c4.d[1] = 0x42
	exec1(c4, bus4, 0x8300) // SBCD D0, D1: 42-27
	if c4.D(1)&0xFF != 0x15 {
		t.Fatalf("SBCD 42-27: D1=%02X", c4.D(1)&0xFF)
	}
	flag(t, c4, srC, false, "C")
}

func TestDivuDivs(t *testing.T) {
	c, bus := newCPU()
	c.d[0] = 100
	c.d[1] = 3
	exec1(c, bus, 0x80C1) // DIVU D1, D0
	if c.D(0) != 0x00010021 {
		t.Fatalf("DIVU packing: D0=%08X", c.D(0))
	}
	flag(t, c, srV, false, "V")

	c.d[0] = 0x00020000
	c.d[1] = 1
	exec1(c, bus, 0x80C1) // DIVU overflow
	if c.D(0) != 0x00020000 {
		t.Fatalf("DIVU overflow changed D0: %08X", c.D(0))
	}
	flag(t, c, srV, true, "V")

	c2, bus2 := newCPU()
	bus2.w32(5*4, 0x3000) // divide by zero vector
	c2.d[0] = 100
	c2.d[1] = 0
	exec1(c2, bus2, 0x80C1)
	if c2.PC() != 0x3000 {
		t.Fatalf("div0 PC=%08X", c2.PC())
	}
	if c2.A(7) != 0x7FFA {
		t.Fatalf("div0 SP=%08X", c2.A(7))
	}
	if bus2.r16(0x7FFA) != 0x2700 {
		t.Fatalf("div0 stacked SR=%04X", bus2.r16(0x7FFA))
	}
	if bus2.r32(0x7FFC) != 0x1002 {
		t.Fatalf("div0 stacked PC=%08X", bus2.r32(0x7FFC))
	}

	c3, bus3 := newCPU()
	c3.d[0] = 0xFFFFFF9C    // -100
	c3.d[1] = 0xFFFD        // -3
	exec1(c3, bus3, 0x81C1) // DIVS D1, D0
	if c3.D(0) != 0xFFFF0021 {
		t.Fatalf("DIVS -100/-3: D0=%08X", c3.D(0))
	}
	c3.d[0] = 100
	exec1(c3, bus3, 0x81C1) // DIVS 100/-3
	if c3.D(0) != 0x0001FFDF {
		t.Fatalf("DIVS 100/-3: D0=%08X", c3.D(0))
	}
	flag(t, c3, srN, true, "N")
}

func TestMul(t *testing.T) {
	c, bus := newCPU()
	c.d[0] = 0xFFFF
	c.d[1] = 0xFFFF
	exec1(c, bus, 0xC0C1) // MULU D1, D0
	if c.D(0) != 0xFFFE0001 {
		t.Fatalf("MULU: D0=%08X", c.D(0))
	}
	c.d[0] = 0xFFFF // -1
	c.d[1] = 2
	exec1(c, bus, 0xC1C1) // MULS D1, D0
	if c.D(0) != 0xFFFFFFFE {
		t.Fatalf("MULS: D0=%08X", c.D(0))
	}
	flag(t, c, srN, true, "N")
	flag(t, c, srV, false, "V")
}

func TestMovemPredecStoreAndPostincLoad(t *testing.T) {
	c, bus := newCPU()
	c.d[0] = 0x11111111
	c.d[1] = 0x22222222
	c.a[0] = 0x33333333
	c.a[1] = 0x44444444
	c.a[6] = 0x3000
	// MOVEM.L D0-D1/A0-A1, -(A6); predecrement mask
	// names registers in reverse bit order.
	exec1(c, bus, 0x48E6, 0xC0C0)
	if c.A(6) != 0x2FF0 {
		t.Fatalf("A6 after store=%08X", c.A(6))
	}
	want := []uint32{0x11111111, 0x22222222, 0x33333333, 0x44444444}
	for i, w := range want {
		if got := bus.r32(0x2FF0 + uint32(4*i)); got != w {
			t.Fatalf("memory long %d: got %08X want %08X", i, got, w)
		}
	}
	c.d[0], c.d[1], c.a[0], c.a[1] = 0, 0, 0, 0
	// MOVEM.L (A6)+, D0-D1/A0-A1 with the normal mask.
	exec1(c, bus, 0x4CDE, 0x0303)
	if c.D(0) != want[0] || c.D(1) != want[1] || c.A(0) != want[2] || c.A(1) != want[3] {
		t.Fatalf("loaded: D0=%08X D1=%08X A0=%08X A1=%08X", c.D(0), c.D(1), c.A(0), c.A(1))
	}
	if c.A(6) != 0x3000 {
		t.Fatalf("A6 after load=%08X", c.A(6))
	}
}

func TestDBccLoopCount(t *testing.T) {
	c, bus := newCPU()
	load(bus, 0x1000,
		0x7002,         // MOVEQ #2, D0
		0x7200,         // MOVEQ #0, D1
		0x5201,         // loop: ADDQ.B #1, D1
		0x51C8, 0xFFFC) // DBRA D0, loop
	for i := 0; i < 8; i++ {
		c.Step()
	}
	if c.D(1) != 3 {
		t.Fatalf("body ran for D1=%d, want 3", c.D(1))
	}
	if c.D(0)&0xFFFF != 0xFFFF {
		t.Fatalf("D0 low=%04X", c.D(0)&0xFFFF)
	}
	if c.PC() != 0x100A {
		t.Fatalf("PC=%08X", c.PC())
	}
}

func TestChk(t *testing.T) {
	c, bus := newCPU()
	c.d[0] = 5
	exec1(c, bus, 0x41BC, 10) // CHK #10, D0: in range
	if c.PC() != 0x1004 || c.A(7) != 0x8000 {
		t.Fatalf("CHK in range: PC=%08X SP=%08X", c.PC(), c.A(7))
	}
	c2, bus2 := newCPU()
	bus2.w32(6*4, 0x3000)
	c2.d[0] = 20
	exec1(c2, bus2, 0x41BC, 10) // CHK #10, D0: taken
	if c2.PC() != 0x3000 {
		t.Fatalf("CHK taken PC=%08X", c2.PC())
	}
	if bus2.r32(0x7FFC) != 0x1004 {
		t.Fatalf("CHK stacked PC=%08X", bus2.r32(0x7FFC))
	}
	flag(t, c2, srN, false, "N")
	c3, bus3 := newCPU()
	bus3.w32(6*4, 0x3000)
	c3.d[0] = 0xFFFF // -1
	exec1(c3, bus3, 0x41BC, 10)
	if c3.PC() != 0x3000 {
		t.Fatalf("CHK negative PC=%08X", c3.PC())
	}
	flag(t, c3, srN, true, "N")
}

func TestRoxlThroughX(t *testing.T) {
	c, bus := newCPU()
	exec1(c, bus, 0x303C, 0x8000) // MOVE.W #$8000, D0
	exec1(c, bus, 0xE350)         // ROXL.W #1, D0
	if c.D(0)&0xFFFF != 0 {
		t.Fatalf("D0 low=%04X", c.D(0)&0xFFFF)
	}
	flag(t, c, srX, true, "X")
	flag(t, c, srC, true, "C")
	c.d[1] = 0
	exec1(c, bus, 0xE351) // ROXL.W #1, D1: X shifts in
	if c.D(1)&0xFFFF != 1 {
		t.Fatalf("D1 low=%04X", c.D(1)&0xFFFF)
	}
	flag(t, c, srX, false, "X")
}

func TestBtstModulo(t *testing.T) {
	c, bus := newCPU()
	c.d[0] = 0x80000000
	exec1(c, bus, 0x0800, 31) // BTST #31, D0
	flag(t, c, srZ, false, "Z")
	c.d[0] = 0x100
	exec1(c, bus, 0x0800, 8) // BTST #8, D0
	flag(t, c, srZ, false, "Z")
	exec1(c, bus, 0x0800, 31) // BTST #31, D0: clear
	flag(t, c, srZ, true, "Z")

	bus.Write8(0x2000, 0x02)
	exec1(c, bus, 0x0839, 9, 0x0000, 0x2000) // BTST #9, $2000.L: bit 1
	flag(t, c, srZ, false, "Z")
	bus.Write8(0x2000, 0x01)
	exec1(c, bus, 0x0839, 8, 0x0000, 0x2000) // BTST #8: bit 0
	flag(t, c, srZ, false, "Z")
	bus.Write8(0x2000, 0x02)
	exec1(c, bus, 0x0839, 8, 0x0000, 0x2000) // BTST #8: bit 0 clear
	flag(t, c, srZ, true, "Z")
}

func TestAddressErrorFrame(t *testing.T) {
	c, bus := newCPU()
	bus.w32(3*4, 0x2000)
	load(bus, 0x1000, 0x3039, 0x0000, 0x1001) // MOVE.W $1001.L, D0
	c.Step()
	if c.PC() != 0x2000 {
		t.Fatalf("PC=%08X", c.PC())
	}
	if c.A(7) != 0x7FF2 {
		t.Fatalf("SP=%08X", c.A(7))
	}
	want := []byte{
		0x27, 0x00, // SR
		0x00, 0x00, 0x10, 0x06, // PC: next instruction
		0x30, 0x39, // IR
		0x00, 0x00, 0x10, 0x01, // fault address
		0x00, 0x35, // SSW: FC 5, data, read
	}
	if got := bus.b[0x7FF2 : 0x7FF2+14]; !bytes.Equal(got, want) {
		t.Fatalf("frame: got % X want % X", got, want)
	}
}

func TestInterruptAfterMaskLowered(t *testing.T) {
	c, bus := newCPU()
	bus.w32(27*4, 0x4000)
	load(bus, 0x1000, 0x46FC, 0x2000) // MOVE.W #$2000, SR
	c.Interrupt(3)
	c.Step() // mask is 7, so the MOVE runs first
	if c.SR() != 0x2000 {
		t.Fatalf("SR=%04X", c.SR())
	}
	if c.PC() != 0x1004 {
		t.Fatalf("PC=%08X", c.PC())
	}
	c.Step() // now the latched level 3 is accepted
	if c.PC() != 0x4000 {
		t.Fatalf("PC=%08X", c.PC())
	}
	if c.A(7) != 0x7FFA {
		t.Fatalf("SP=%08X", c.A(7))
	}
	if bus.r16(0x7FFA) != 0x2000 {
		t.Fatalf("stacked SR=%04X", bus.r16(0x7FFA))
	}
	if bus.r32(0x7FFC) != 0x1004 {
		t.Fatalf("stacked PC=%08X", bus.r32(0x7FFC))
	}
	if c.SR() != 0x2300 {
		t.Fatalf("SR in handler=%04X", c.SR())
	}
}

func TestStopWakesOnInterrupt(t *testing.T) {
	c, bus := newCPU()
	bus.w32(26*4, 0x4000)
	load(bus, 0x1000, 0x4E72, 0x2000) // STOP #$2000
	c.Step()
	if !c.Halted() {
		t.Fatal("not halted after STOP")
	}
	c.Step()
	if !c.Halted() || c.PC() != 0x1004 {
		t.Fatalf("halted step moved: halted=%v PC=%08X", c.Halted(), c.PC())
	}
	c.Interrupt(2)
	c.Step()
	if c.Halted() {
		t.Fatal("still halted after interrupt")
	}
	if c.PC() != 0x4000 {
		t.Fatalf("PC=%08X", c.PC())
	}
	if bus.r32(c.A(7)+2) != 0x1004 {
		t.Fatalf("stacked PC=%08X", bus.r32(c.A(7)+2))
	}
}

func TestTas(t *testing.T) {
	cases := []struct {
		in   uint8
		out  uint8
		n, z bool
	}{
		{0x7F, 0xFF, false, false},
		{0x80, 0x80, true, false},
		{0x00, 0x80, false, true},
	}
	for _, tc := range cases {
		c, bus := newCPU()
		bus.Write8(0x2000, tc.in)
		exec1(c, bus, 0x4AF9, 0x0000, 0x2000) // TAS $2000.L
		if got := bus.Read8(0x2000); got != tc.out {
			t.Fatalf("TAS %02X: memory=%02X want %02X", tc.in, got, tc.out)
		}
		flag(t, c, srN, tc.n, "N")
		flag(t, c, srZ, tc.z, "Z")
	}
}

func TestMovepRoundTrip(t *testing.T) {
	c, bus := newCPU()
	c.d[0] = 0xAABBCCDD
	c.a[0] = 0x2000
	bus.Write8(0x2001, 0xEE)
	exec1(c, bus, 0x0188, 0x0000) // MOVEP.L D0, (0,A0)
	for i, want := range []uint8{0xAA, 0xBB, 0xCC, 0xDD} {
		if got := bus.Read8(0x2000 + uint32(2*i)); got != want {
			t.Fatalf("byte %d: got %02X want %02X", i, got, want)
		}
	}
	if bus.Read8(0x2001) != 0xEE {
		t.Fatal("MOVEP touched an odd byte")
	}
	c.d[0] = 0
	exec1(c, bus, 0x01C8, 0x0000) // MOVEP.L (0,A0), D0
	if c.D(0) != 0xAABBCCDD {
		t.Fatalf("D0=%08X", c.D(0))
	}
}

func TestScc(t *testing.T) {
	c, bus := newCPU()
	c.sr = 0x2700 | srZ
	c.d[0] = 0x12345600
	exec1(c, bus, 0x57C0) // SEQ D0 with Z set
	if c.D(0) != 0x123456FF {
		t.Fatalf("SEQ: D0=%08X", c.D(0))
	}
	c2, bus2 := newCPU()
	c2.sr = 0x2700 | srZ
	c2.d[0] = 0x123456AA
	exec1(c2, bus2, 0x56C0) // SNE D0 with Z set: false
	if c2.D(0) != 0x12345600 {
		t.Fatalf("SNE: D0=%08X", c2.D(0))
	}
	c3, bus3 := newCPU()
	c3.d[0] = 0
	exec1(c3, bus3, 0x56C0) // SNE D0 with Z clear: true
	if c3.D(0) != 0xFF {
		t.Fatalf("SNE true: D0=%08X", c3.D(0))
	}
}

func TestBranchDisplacements(t *testing.T) {
	c, bus := newCPU()
	load(bus, 0x1000,
		0x6000, 0x0004, // BRA +4 (word displacement)
		0x7001, // MOVEQ #1, D0 (skipped)
		0x7007) // MOVEQ #7, D0
	c.Step()
	if c.PC() != 0x1006 {
		t.Fatalf("PC=%08X", c.PC())
	}
	c.Step()
	if c.D(0) != 7 {
		t.Fatalf("D0=%d", c.D(0))
	}

	// A byte displacement of 0xFF is -1 on the base
	// 68000: BNE at 0x1000 targets 0x1001, and the odd
	// fetch there takes the address error vector with
	// the faulting fetch address stacked.
	c2, bus2 := newCPU()
	bus2.w32(3*4, 0x2000)
	load(bus2, 0x1000, 0x66FF) // BNE -1, Z clear after reset
	c2.Step()
	if c2.PC() != 0x1001 {
		t.Fatalf("BNE -1 target PC=%08X", c2.PC())
	}
	c2.Step()
	if c2.PC() != 0x2000 {
		t.Fatalf("odd fetch PC=%08X", c2.PC())
	}
	if bus2.r32(c2.A(7)+2) != 0x1001 {
		t.Fatalf("stacked fetch PC=%08X", bus2.r32(c2.A(7)+2))
	}
	if bus2.r16(c2.A(7)+12) != 0x0026 {
		t.Fatalf("SSW=%04X", bus2.r16(c2.A(7)+12))
	}
}

func TestPrivilegeViolationStackedPC(t *testing.T) {
	c, bus := newCPU()
	bus.w32(8*4, 0x3000)
	c.otherSP = 0x7000                // USP value while in supervisor
	c.setSR(0x0000)                   // enter user mode: A7 becomes USP
	load(bus, 0x1000, 0x46FC, 0x2700) // MOVE.W #$2700, SR
	c.Step()
	if c.PC() != 0x3000 {
		t.Fatalf("PC=%08X", c.PC())
	}
	if c.A(7) != 0x8000-6 {
		t.Fatalf("SSP=%08X", c.A(7))
	}
	if bus.r32(0x8000-4) != 0x1002 {
		t.Fatalf("stacked PC=%08X, want past the opcode", bus.r32(0x8000-4))
	}
	if bus.r16(0x8000-6) != 0x0000 {
		t.Fatalf("stacked SR=%04X", bus.r16(0x8000-6))
	}
}

func TestLineAAndFAndIllegal(t *testing.T) {
	for _, tc := range []struct {
		op   uint16
		vec  int
		name string
	}{
		{0xA000, 10, "line A"},
		{0xF000, 11, "line F"},
		{0x4AFC, 4, "illegal"},
	} {
		c, bus := newCPU()
		bus.w32(uint32(tc.vec)*4, 0x3000)
		exec1(c, bus, tc.op)
		if c.PC() != 0x3000 {
			t.Fatalf("%s: PC=%08X", tc.name, c.PC())
		}
		if bus.r32(c.A(7)+2) != 0x1002 {
			t.Fatalf("%s: stacked PC=%08X", tc.name, bus.r32(c.A(7)+2))
		}
	}
}

func summationCPU(t *testing.T) (*CPU, *memBus) {
	t.Helper()
	c, bus := newCPU()
	load(bus, 0x1000,
		0x41F9, 0x0000, 0x2000, // LEA $2000.L, A0
		0x7000,         // MOVEQ #0, D0
		0x7203,         // MOVEQ #3, D1
		0xD098,         // loop: ADD.L (A0)+, D0
		0x51C9, 0xFFFC, // DBRA D1, loop
		0x60FE) // BRA *
	for i, v := range []uint32{10, 20, 30, 40} {
		bus.w32(0x2000+uint32(4*i), v)
	}
	return c, bus
}

func TestSummationProgramDeterministicCycles(t *testing.T) {
	var cycles []uint64
	for run := 0; run < 2; run++ {
		c, _ := summationCPU(t)
		for i := 0; i < 11; i++ {
			c.Step()
		}
		if c.D(0) != 100 {
			t.Fatalf("run %d: sum=%d", run, c.D(0))
		}
		if c.PC() != 0x1010 {
			t.Fatalf("run %d: PC=%08X", run, c.PC())
		}
		cycles = append(cycles, c.Cycles)
	}
	if cycles[0] != cycles[1] {
		t.Fatalf("cycles differ across runs: %d vs %d", cycles[0], cycles[1])
	}
}

func TestSerializeRoundTripMidProgram(t *testing.T) {
	c, bus := summationCPU(t)
	for i := 0; i < 5; i++ {
		c.Step()
	}
	blob := c.Serialize()
	if len(blob) != SerializeSize {
		t.Fatalf("blob size %d", len(blob))
	}
	c2 := New(bus)
	if err := c2.Deserialize(blob); err != nil {
		t.Fatal(err)
	}
	if c2.D(0) != c.D(0) || c2.D(1) != c.D(1) || c2.PC() != c.PC() ||
		c2.SR() != c.SR() || c2.Cycles != c.Cycles || c2.A(0) != c.A(0) {
		t.Fatalf("restored state differs: D0=%d PC=%08X Cycles=%d",
			c2.D(0), c2.PC(), c2.Cycles)
	}
	if !bytes.Equal(c2.Serialize(), blob) {
		t.Fatal("re-serialized blob differs")
	}
	for i := 0; i < 6; i++ {
		c2.Step()
	}
	if c2.D(0) != 100 {
		t.Fatalf("resumed sum=%d", c2.D(0))
	}
}
