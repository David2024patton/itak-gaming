package m68k

import (
	"encoding/binary"
	"testing"
)

// ramBus is a flat big-endian RAM implementing the CPU's Bus interface.
type ramBus struct{ mem []byte }

func newRAMBus() *ramBus { return &ramBus{mem: make([]byte, 0x10000)} }

func (b *ramBus) Read8(a uint32) uint8       { return b.mem[a] }
func (b *ramBus) Read16(a uint32) uint16     { return binary.BigEndian.Uint16(b.mem[a:]) }
func (b *ramBus) Read32(a uint32) uint32     { return binary.BigEndian.Uint32(b.mem[a:]) }
func (b *ramBus) Write8(a uint32, v uint8)   { b.mem[a] = v }
func (b *ramBus) Write16(a uint32, v uint16) { binary.BigEndian.PutUint16(b.mem[a:], v) }
func (b *ramBus) Write32(a uint32, v uint32) { binary.BigEndian.PutUint32(b.mem[a:], v) }
func (b *ramBus) Reset()                     {}

const codeBase = 0x1000

// newCPU builds a CPU with the reset vector pointing at codeBase and
// the given instruction words laid out there. Initial SSP is 0xF000.
func newCPU(code ...uint16) (*CPU, *ramBus) {
	bus := newRAMBus()
	bus.Write32(0, 0x0000F000)
	bus.Write32(4, codeBase)
	for i, w := range code {
		bus.Write16(codeBase+uint32(2*i), w)
	}
	c := New(bus)
	c.Reset()
	return c, bus
}

// arrange mutates the register file through the public snapshot API.
func arrange(c *CPU, fn func(r *Registers)) {
	r := c.Registers()
	fn(&r)
	c.SetState(r)
}

func flags(t *testing.T, c *CPU, want, clear uint16) {
	t.Helper()
	sr := c.Registers().SR
	if sr&want != want {
		t.Errorf("SR = %04X, want flags %04X set", sr, want)
	}
	if sr&clear != 0 {
		t.Errorf("SR = %04X, want flags %04X clear", sr, clear)
	}
}

func TestReset(t *testing.T) {
	c, _ := newCPU(0x4E71)
	r := c.Registers()
	if r.SR != 0x2700 {
		t.Errorf("SR = %04X, want 2700", r.SR)
	}
	if r.A[7] != 0xF000 || r.SSP != 0xF000 {
		t.Errorf("SP = %08X/%08X, want F000", r.A[7], r.SSP)
	}
	if r.PC != codeBase {
		t.Errorf("PC = %08X, want %08X", r.PC, codeBase)
	}
	if c.Cycles() != 0 {
		t.Errorf("cycles = %d, want 0", c.Cycles())
	}
}

func TestAddFlags(t *testing.T) {
	// ADD.W D1,D0 = 0xD041; 0x7FFF + 1 overflows into the sign bit.
	c, _ := newCPU(0xD041)
	arrange(c, func(r *Registers) { r.D[0] = 0x7FFF; r.D[1] = 1 })
	c.Step()
	if got := c.Registers().D[0]; got != 0x8000 {
		t.Errorf("ADD.W result = %08X, want 8000", got)
	}
	flags(t, c, srN|srV, srZ|srC|srX)

	// ADD.B D1,D0 = 0xD001; 0xFF + 1 wraps with carry, upper bytes kept.
	c, _ = newCPU(0xD001)
	arrange(c, func(r *Registers) { r.D[0] = 0x123456FF; r.D[1] = 1 })
	c.Step()
	if got := c.Registers().D[0]; got != 0x12345600 {
		t.Errorf("ADD.B result = %08X, want 12345600", got)
	}
	flags(t, c, srZ|srC|srX, srN|srV)

	// ADD.L D1,D0 = 0xD081; 0x7FFFFFFF + 1.
	c, _ = newCPU(0xD081)
	arrange(c, func(r *Registers) { r.D[0] = 0x7FFFFFFF; r.D[1] = 1 })
	c.Step()
	if got := c.Registers().D[0]; got != 0x80000000 {
		t.Errorf("ADD.L result = %08X, want 80000000", got)
	}
	flags(t, c, srN|srV, srZ|srC)
}

func TestSubCmpFlags(t *testing.T) {
	// SUB.W D1,D0 = 0x9041; 0 - 1 borrows.
	c, _ := newCPU(0x9041)
	arrange(c, func(r *Registers) { r.D[0] = 0; r.D[1] = 1 })
	c.Step()
	if got := c.Registers().D[0]; got != 0xFFFF {
		t.Errorf("SUB.W result = %08X, want FFFF", got)
	}
	flags(t, c, srN|srC|srX, srZ|srV)

	// SUB.W overflow: 0x8000 - 1 = 0x7FFF with V set.
	c, _ = newCPU(0x9041)
	arrange(c, func(r *Registers) { r.D[0] = 0x8000; r.D[1] = 1 })
	c.Step()
	flags(t, c, srV, srN|srZ|srC)

	// CMP.W D1,D0 = 0xB041 preserves X and writes nothing back.
	c, _ = newCPU(0xB041)
	arrange(c, func(r *Registers) {
		r.D[0] = 5
		r.D[1] = 5
		r.SR = 0x2700 | srX
	})
	c.Step()
	if got := c.Registers().D[0]; got != 5 {
		t.Errorf("CMP changed D0 to %08X", got)
	}
	flags(t, c, srZ|srX, srN|srV|srC)
}

func TestAddXStickyZ(t *testing.T) {
	// ADDX.W D1,D0 = 0xD141.
	// Case 1: 0 + 0 + X(1) = 1: nonzero result clears Z, no carry out.
	c, _ := newCPU(0xD141)
	arrange(c, func(r *Registers) { r.SR = 0x2700 | srX | srZ })
	c.Step()
	if got := c.Registers().D[0]; got != 1 {
		t.Errorf("ADDX result = %08X, want 1", got)
	}
	flags(t, c, 0, srZ|srX|srC)

	// Case 2: 0xFFFF + 0 + X(1) = 0 with carry; Z was clear and the
	// sticky-Z rule keeps it clear for a zero result.
	c, _ = newCPU(0xD141)
	arrange(c, func(r *Registers) { r.D[0] = 0xFFFF; r.SR = 0x2700 | srX })
	c.Step()
	if got := c.Registers().D[0]; got != 0 {
		t.Errorf("ADDX result = %08X, want 0", got)
	}
	flags(t, c, srX|srC, srZ)

	// Case 3: same sum, but Z was set; sticky Z keeps it set.
	c, _ = newCPU(0xD141)
	arrange(c, func(r *Registers) { r.D[0] = 0xFFFF; r.SR = 0x2700 | srX | srZ })
	c.Step()
	flags(t, c, srZ|srX|srC, 0)
}

func TestShifts(t *testing.T) {
	// ASL.W #1,D0 = 0xE340: 0x8001 -> 0x0002, carry out 1, sign changed
	// so V is set.
	c, _ := newCPU(0xE340)
	arrange(c, func(r *Registers) { r.D[0] = 0x8001 })
	c.Step()
	if got := c.Registers().D[0]; got != 0x0002 {
		t.Errorf("ASL result = %08X, want 0002", got)
	}
	flags(t, c, srC|srX|srV, srN|srZ)

	// ASL.W D1,D0 = 0xE360 with a zero register count: value unchanged,
	// C cleared, X preserved, V clear.
	c, _ = newCPU(0xE360)
	arrange(c, func(r *Registers) { r.D[0] = 0x8000; r.D[1] = 0; r.SR = 0x2700 | srX })
	c.Step()
	if got := c.Registers().D[0]; got != 0x8000 {
		t.Errorf("ASL count-0 result = %08X, want 8000", got)
	}
	flags(t, c, srN|srX, srC|srV|srZ)

	// LSR.B #1,D0 = 0xE208: 0x03 -> 0x01, carry 1.
	c, _ = newCPU(0xE208)
	arrange(c, func(r *Registers) { r.D[0] = 0x03 })
	c.Step()
	if got := c.Registers().D[0]; got != 0x01 {
		t.Errorf("LSR result = %08X, want 01", got)
	}
	flags(t, c, srC|srX, srN|srZ|srV)

	// ROXL.W #1,D0 = 0xE350 with X set: 0x8000 -> 0x0001, X stays 1.
	c, _ = newCPU(0xE350)
	arrange(c, func(r *Registers) { r.D[0] = 0x8000; r.SR = 0x2700 | srX })
	c.Step()
	if got := c.Registers().D[0]; got != 0x0001 {
		t.Errorf("ROXL result = %08X, want 0001", got)
	}
	flags(t, c, srC|srX, srN|srZ|srV)

	// ASR.L #8,D0 = 0xE080: sign-extends, 0x80000000 -> 0xFF800000,
	// last bit out is 0.
	c, _ = newCPU(0xE080)
	arrange(c, func(r *Registers) { r.D[0] = 0x80000000 })
	c.Step()
	if got := c.Registers().D[0]; got != 0xFF800000 {
		t.Errorf("ASR result = %08X, want FF800000", got)
	}
	flags(t, c, srN, srZ|srC|srV)

	// ASL.W (A0) = 0xE1D0 (memory form, count one): 0xC000 -> 0x8000,
	// carry 1, sign unchanged so V clear.
	c, bus := newCPU(0xE1D0)
	arrange(c, func(r *Registers) { r.A[0] = 0x2000 })
	bus.Write16(0x2000, 0xC000)
	c.Step()
	if got := bus.Read16(0x2000); got != 0x8000 {
		t.Errorf("ASL mem result = %04X, want 8000", got)
	}
	flags(t, c, srN|srC|srX, srZ|srV)
}

func TestDivide(t *testing.T) {
	// DIVU #10,D0 = 0x80FC: 600000 / 10 = 60000 remainder 0.
	c, _ := newCPU(0x80FC, 0x000A)
	arrange(c, func(r *Registers) { r.D[0] = 600000 })
	c.Step()
	if got := c.Registers().D[0]; got != 60000 {
		t.Errorf("DIVU result = %08X, want 60000 (0000EA60)", got)
	}
	// 60000 = 0xEA60 has the word sign bit set, so N is set.
	flags(t, c, srN, srZ|srV|srC)

	// DIVU overflow: 65536 / 1 does not fit in a word; the destination
	// is unchanged and V is set.
	c, _ = newCPU(0x80FC, 0x0001)
	arrange(c, func(r *Registers) { r.D[0] = 0x00010000 })
	c.Step()
	if got := c.Registers().D[0]; got != 0x00010000 {
		t.Errorf("DIVU overflow changed D0 to %08X", got)
	}
	flags(t, c, srV, srC)

	// DIVS #-3,D0 = 0x81FC: -100 / -3 = 33 remainder -1 (the remainder
	// takes the dividend's sign).
	c, _ = newCPU(0x81FC, 0xFFFD)
	arrange(c, func(r *Registers) { r.D[0] = 0xFFFFFF9C })
	c.Step()
	if got := c.Registers().D[0]; got != 0xFFFF0021 {
		t.Errorf("DIVS result = %08X, want FFFF0021", got)
	}

	// Divide by zero takes exception vector 5: the handler address is
	// stacked-frame reachable and the return PC is the next instruction.
	c, bus := newCPU(0x80FC, 0x0000)
	bus.Write32(5*4, 0x2000)
	arrange(c, func(r *Registers) { r.D[0] = 42 })
	c.Step()
	r := c.Registers()
	if r.PC != 0x2000 {
		t.Errorf("div-zero PC = %08X, want handler 2000", r.PC)
	}
	if r.A[7] != 0xF000-6 {
		t.Errorf("div-zero SP = %08X, want EFFA", r.A[7])
	}
	if got := bus.Read16(0xEFFA); got != 0x2700 {
		t.Errorf("stacked SR = %04X, want 2700", got)
	}
	if got := bus.Read32(0xEFFC); got != codeBase+4 {
		t.Errorf("stacked PC = %08X, want %08X", got, codeBase+4)
	}
}

func TestAddressingModes(t *testing.T) {
	// MOVE.W (A0)+,D0 = 0x3018.
	c, bus := newCPU(0x3018)
	arrange(c, func(r *Registers) { r.A[0] = 0x2000 })
	bus.Write16(0x2000, 0xBEEF)
	c.Step()
	if got := c.Registers().D[0]; got != 0xBEEF {
		t.Errorf("(A0)+ value = %08X, want BEEF", got)
	}
	if got := c.Registers().A[0]; got != 0x2002 {
		t.Errorf("(A0)+ A0 = %08X, want 2002", got)
	}

	// MOVE.B (A7)+,D0 = 0x101F: the stack-pointer byte quirk moves A7
	// by 2, not 1.
	c, bus = newCPU(0x101F)
	bus.Write8(0xF000, 0x5A)
	c.Step()
	if got := c.Registers().D[0]; got != 0x5A {
		t.Errorf("(A7)+ value = %08X, want 5A", got)
	}
	if got := c.Registers().A[7]; got != 0xF002 {
		t.Errorf("(A7)+ A7 = %08X, want F002", got)
	}

	// MOVE.W -(A0),D0 = 0x3020.
	c, bus = newCPU(0x3020)
	arrange(c, func(r *Registers) { r.A[0] = 0x2000 })
	bus.Write16(0x1FFE, 0x1234)
	c.Step()
	if got := c.Registers().D[0]; got != 0x1234 {
		t.Errorf("-(A0) value = %08X, want 1234", got)
	}
	if got := c.Registers().A[0]; got != 0x1FFE {
		t.Errorf("-(A0) A0 = %08X, want 1FFE", got)
	}

	// MOVE.B D0,-(A7) = 0x1F00: byte predecrement on A7 also moves by 2.
	c, bus = newCPU(0x1F00)
	arrange(c, func(r *Registers) { r.D[0] = 0xAB })
	c.Step()
	if got := c.Registers().A[7]; got != 0xEFFE {
		t.Errorf("-(A7) A7 = %08X, want EFFE", got)
	}
	if got := bus.Read8(0xEFFE); got != 0xAB {
		t.Errorf("-(A7) byte = %02X, want AB", got)
	}

	// MOVE.W $10(A0),D0 = 0x3028.
	c, bus = newCPU(0x3028, 0x0010)
	arrange(c, func(r *Registers) { r.A[0] = 0x2000 })
	bus.Write16(0x2010, 0x0D0A)
	c.Step()
	if got := c.Registers().D[0]; got != 0x0D0A {
		t.Errorf("d16(A0) value = %08X, want 0D0A", got)
	}

	// MOVE.W $4(A0,D1.W),D0 = 0x3030, index word 0x1004.
	c, bus = newCPU(0x3030, 0x1004)
	arrange(c, func(r *Registers) { r.A[0] = 0x2000; r.D[1] = 0x10 })
	bus.Write16(0x2014, 0x600D)
	c.Step()
	if got := c.Registers().D[0]; got != 0x600D {
		t.Errorf("d8(A0,D1.W) value = %08X, want 600D", got)
	}

	// Same with a long index of -16 (sign-extended from the register):
	// index word 0x1804 selects D1.L.
	c, bus = newCPU(0x3030, 0x1804)
	arrange(c, func(r *Registers) { r.A[0] = 0x2000; r.D[1] = 0xFFFFFFF0 })
	bus.Write16(0x1FF4, 0x0BAD)
	c.Step()
	if got := c.Registers().D[0]; got != 0x0BAD {
		t.Errorf("d8(A0,D1.L) value = %08X, want 0BAD", got)
	}

	// MOVE.W (d16,PC),D0 = 0x303A: the displacement base is the
	// extension word's address (codeBase+2); a displacement of 0x4E
	// reaches codeBase+0x50.
	c, bus = newCPU(0x303A, 0x004E)
	bus.Write16(codeBase+0x50, 0xCAFE)
	c.Step()
	if got := c.Registers().D[0]; got != 0xCAFE {
		t.Errorf("d16(PC) value = %08X, want CAFE", got)
	}

	// MOVE.L D0,-(A7) = 0x2F00.
	c, bus = newCPU(0x2F00)
	arrange(c, func(r *Registers) { r.D[0] = 0xDEADBEEF })
	c.Step()
	if got := c.Registers().A[7]; got != 0xEFFC {
		t.Errorf("push A7 = %08X, want EFFC", got)
	}
	if got := bus.Read32(0xEFFC); got != 0xDEADBEEF {
		t.Errorf("pushed value = %08X, want DEADBEEF", got)
	}
}

func TestInterruptMaskingAndRTE(t *testing.T) {
	// Program: MOVE.W #$2300,SR then NOPs. Autovector handler (level 4
	// -> vector 28) sits at 0x2000 and is a bare RTE.
	c, bus := newCPU(0x46FC, 0x2300, 0x4E71, 0x4E71)
	bus.Write32(28*4, 0x2000)
	bus.Write16(0x2000, 0x4E73)

	// Mask is 7 after reset, so the level-4 request latches but is not
	// serviced; a lower level-2 request must not displace it.
	c.RequestInterrupt(4, nil)
	c.RequestInterrupt(2, nil)
	c.Step() // executes MOVE to SR (mask drops to 3)
	if got := c.Registers().PC; got != codeBase+4 {
		t.Fatalf("PC after MOVE = %08X, want %08X", got, codeBase+4)
	}
	c.Step() // now the latched level-4 interrupt is serviced
	r := c.Registers()
	if r.PC != 0x2000 {
		t.Fatalf("interrupt PC = %08X, want handler 2000", r.PC)
	}
	if r.SR != 0x2400 {
		t.Errorf("interrupt SR = %04X, want 2400 (S set, mask = level)", r.SR)
	}
	if got := bus.Read16(0xEFFA); got != 0x2300 {
		t.Errorf("stacked SR = %04X, want 2300", got)
	}
	if got := bus.Read32(0xEFFC); got != codeBase+4 {
		t.Errorf("stacked PC = %08X, want %08X", got, codeBase+4)
	}
	c.Step() // RTE in the handler
	r = c.Registers()
	if r.PC != codeBase+4 {
		t.Errorf("RTE return PC = %08X, want %08X", r.PC, codeBase+4)
	}
	if r.SR != 0x2300 {
		t.Errorf("RTE restored SR = %04X, want 2300", r.SR)
	}
	if r.A[7] != 0xF000 {
		t.Errorf("RTE restored SP = %08X, want F000", r.A[7])
	}
}

func TestExplicitVectorInterrupt(t *testing.T) {
	// A request carrying an explicit vector uses that vector instead of
	// the autovector.
	c, bus := newCPU(0x46FC, 0x2000, 0x4E71) // MOVE.W #$2000,SR: mask 0
	bus.Write32(64*4, 0x2000)
	vec := uint8(64)
	c.RequestInterrupt(3, &vec)
	c.Step() // MOVE to SR executes (mask was 7 at the boundary)
	c.Step() // interrupt serviced through vector 64
	if got := c.Registers().PC; got != 0x2000 {
		t.Errorf("explicit-vector PC = %08X, want 2000", got)
	}
}

func TestStopWake(t *testing.T) {
	// STOP #$2000 then NOP. Level 6 autovector = 30.
	c, bus := newCPU(0x4E72, 0x2000, 0x4E71)
	bus.Write32(30*4, 0x2000)
	c.Step() // STOP
	if got := c.Registers().PC; got != codeBase+4 {
		t.Fatalf("PC after STOP = %08X, want %08X", got, codeBase+4)
	}
	if n := c.Step(); n != 4 {
		t.Errorf("stopped Step = %d cycles, want 4", n)
	}
	if got := c.Registers().PC; got != codeBase+4 {
		t.Errorf("stopped PC moved to %08X", got)
	}
	c.RequestInterrupt(6, nil)
	c.Step() // wakes and services
	r := c.Registers()
	if r.PC != 0x2000 {
		t.Errorf("STOP wake PC = %08X, want 2000", r.PC)
	}
	if got := bus.Read16(0xEFFA); got != 0x2000 {
		t.Errorf("STOP stacked SR = %04X, want 2000", got)
	}

	// STOP with mask 7: a level-6 request cannot wake it, but the
	// non-maskable level 7 (autovector 31) can.
	c2, bus2 := newCPU(0x4E72, 0x2700, 0x4E71)
	bus2.Write32(31*4, 0x2000)
	c2.Step()
	c2.RequestInterrupt(6, nil)
	if n := c2.Step(); n != 4 {
		t.Errorf("masked stopped Step = %d, want 4", n)
	}
	if got := c2.Registers().PC; got != codeBase+4 {
		t.Errorf("masked STOP woke early, PC = %08X", got)
	}
	// StepCycles on a stopped CPU consumes the whole budget as time.
	if n := c2.StepCycles(40); n != 40 {
		t.Errorf("stopped StepCycles = %d, want 40", n)
	}
	c2.RequestInterrupt(7, nil)
	c2.Step()
	if got := c2.Registers().PC; got != 0x2000 {
		t.Errorf("level-7 wake PC = %08X, want 2000", got)
	}
}

func TestSetStateContract(t *testing.T) {
	c, bus := newCPU(0x4E71)
	bus.Write32(31*4, 0x2000)
	// Burn some cycles so the SetState cycle reset is observable.
	c.Step()
	c.Step()
	if c.Cycles() == 0 {
		t.Fatal("expected nonzero cycles before SetState")
	}
	// Latch an interrupt that SetState must clear.
	c.RequestInterrupt(7, nil)

	c.SetState(Registers{
		D:   [8]uint32{3: 0xDEADBEEF},
		A:   [8]uint32{2: 0x11112222, 7: 0x77777777}, // A[7] must be ignored
		PC:  codeBase,
		SR:  0x2300, // supervisor
		USP: 0x9000,
		SSP: 0x8000,
	})
	r := c.Registers()
	if r.A[7] != 0x8000 {
		t.Errorf("supervisor A[7] = %08X, want SSP 8000", r.A[7])
	}
	if r.A[2] != 0x11112222 || r.D[3] != 0xDEADBEEF {
		t.Errorf("register copy wrong: A2=%08X D3=%08X", r.A[2], r.D[3])
	}
	if c.Cycles() != 0 {
		t.Errorf("SetState left cycles = %d, want 0", c.Cycles())
	}
	c.AddCycles(1234)
	if c.Cycles() != 1234 {
		t.Errorf("AddCycles: cycles = %d, want 1234", c.Cycles())
	}
	// The pending level-7 request was cleared: Step runs the NOP, not
	// the interrupt handler.
	c.Step()
	if got := c.Registers().PC; got != codeBase+2 {
		t.Errorf("PC after cleared-pending Step = %08X, want %08X (NOP ran)", got, codeBase+2)
	}

	// User mode: A[7] comes from USP instead.
	c.SetState(Registers{PC: codeBase, SR: 0x0300, USP: 0x9000, SSP: 0x8000})
	if got := c.Registers().A[7]; got != 0x9000 {
		t.Errorf("user A[7] = %08X, want USP 9000", got)
	}
	if got := c.Registers().SSP; got != 0x8000 {
		t.Errorf("user-mode SSP field = %08X, want 8000", got)
	}
}

func TestMovem(t *testing.T) {
	// MOVEM.L D0-D2/A0-A1,-(A7) = 0x48E7; under predecrement the mask
	// is read in reverse bit order: D2..D0 land at bits 13..15 and
	// A1..A0 at bits 6..7, hence 0xE0C0.
	c, bus := newCPU(0x48E7, 0xE0C0, 0x4CDF, 0x0307)
	arrange(c, func(r *Registers) {
		r.D[0], r.D[1], r.D[2] = 0x11111111, 0x22222222, 0x33333333
		r.A[0], r.A[1] = 0x44444444, 0x55555555
	})
	c.Step()
	if got := c.Registers().A[7]; got != 0xF000-20 {
		t.Fatalf("MOVEM store SP = %08X, want %08X", got, 0xF000-20)
	}
	base := uint32(0xF000 - 20)
	want := []uint32{0x11111111, 0x22222222, 0x33333333, 0x44444444, 0x55555555}
	for i, w := range want {
		if got := bus.Read32(base + uint32(4*i)); got != w {
			t.Errorf("MOVEM memory[%d] = %08X, want %08X", i, got, w)
		}
	}
	// Clear the registers (keeping PC and the stack pointer) and load
	// them back with MOVEM.L (A7)+,D0-D2/A0-A1 = 0x4CDF, mask 0x0307.
	arrange(c, func(r *Registers) {
		r.D[0], r.D[1], r.D[2] = 0, 0, 0
		r.A[0], r.A[1] = 0, 0
		r.SSP = base
	})
	c.Step()
	r := c.Registers()
	if r.D[0] != want[0] || r.D[1] != want[1] || r.D[2] != want[2] ||
		r.A[0] != want[3] || r.A[1] != want[4] {
		t.Errorf("MOVEM load wrong: D=%08X %08X %08X A=%08X %08X",
			r.D[0], r.D[1], r.D[2], r.A[0], r.A[1])
	}
	if r.A[7] != 0xF000 {
		t.Errorf("MOVEM load SP = %08X, want F000", r.A[7])
	}

	// MOVEM.W (A0),D0 = 0x4C90 sign-extends word loads.
	c2, bus2 := newCPU(0x4C90, 0x0001)
	arrange(c2, func(r *Registers) { r.A[0] = 0x2000 })
	bus2.Write16(0x2000, 0x8001)
	c2.Step()
	if got := c2.Registers().D[0]; got != 0xFFFF8001 {
		t.Errorf("MOVEM.W load = %08X, want FFFF8001", got)
	}
}

func TestBranchAndDBcc(t *testing.T) {
	// MOVEQ #3,D0; DBF D0,-2 (loops on itself); NOP.
	c, _ := newCPU(0x7003, 0x51C8, 0xFFFE, 0x4E71)
	c.Step() // MOVEQ
	for i := 0; i < 4; i++ {
		c.Step() // DBF runs 4 times total: 3 branches + fall-through
	}
	r := c.Registers()
	if r.PC != codeBase+6 {
		t.Errorf("DBF exit PC = %08X, want %08X", r.PC, codeBase+6)
	}
	if got := r.D[0] & 0xFFFF; got != 0xFFFF {
		t.Errorf("DBF final counter = %04X, want FFFF", got)
	}

	// MOVEQ #0,D0 (sets Z); BEQ +2 skips MOVEQ #5,D1.
	c, _ = newCPU(0x7000, 0x6702, 0x7205, 0x4E71)
	c.Step()
	c.Step()
	if got := c.Registers().PC; got != codeBase+6 {
		t.Errorf("BEQ taken PC = %08X, want %08X", got, codeBase+6)
	}
	if got := c.Registers().D[1]; got != 0 {
		t.Errorf("BEQ skipped instruction still ran: D1 = %08X", got)
	}

	// MOVEQ #1,D0 (clears Z); BEQ falls through; MOVEQ #5,D1 runs.
	c, _ = newCPU(0x7001, 0x6702, 0x7205, 0x4E71)
	c.Step()
	c.Step()
	c.Step()
	if got := c.Registers().D[1]; got != 5 {
		t.Errorf("BEQ fall-through D1 = %08X, want 5", got)
	}
}

func TestPrivilegeViolation(t *testing.T) {
	// In user mode, MOVE.W #$2700,SR must take the privilege exception
	// (vector 8), stacking on the supervisor stack.
	c, bus := newCPU(0x46FC, 0x2700)
	bus.Write32(8*4, 0x2000)
	arrange(c, func(r *Registers) {
		r.SR = 0x0300 // user mode
		r.USP = 0x9000
		r.SSP = 0x8000
	})
	c.Step()
	r := c.Registers()
	if r.PC != 0x2000 {
		t.Fatalf("privilege PC = %08X, want handler 2000", r.PC)
	}
	if r.A[7] != 0x8000-6 {
		t.Errorf("privilege SP = %08X, want %08X", r.A[7], 0x8000-6)
	}
	if got := bus.Read16(0x8000 - 6); got != 0x0300 {
		t.Errorf("stacked SR = %04X, want 0300 (user SR)", got)
	}
	// The stacked PC is the privileged instruction itself, not the
	// next instruction: the instruction never executed, and the manual
	// has the handler inspect the offending word (the same rule as
	// illegal instruction and the line A/F emulation vectors, verified
	// against the reference implementation by differential test).
	if got := bus.Read32(0x8000 - 4); got != codeBase {
		t.Errorf("stacked PC = %08X, want %08X", got, codeBase)
	}
	if r.USP != 0x9000 {
		t.Errorf("USP after switch = %08X, want 9000", r.USP)
	}
}

func TestAddressError(t *testing.T) {
	// MOVE.W $00001001,D0 reads a word at an odd address: vector 3.
	c, bus := newCPU(0x3039, 0x0000, 0x1001)
	bus.Write32(3*4, 0x2000)
	c.Step()
	if got := c.Registers().PC; got != 0x2000 {
		t.Errorf("address-error PC = %08X, want handler 2000", got)
	}
	if c.Halted() {
		t.Error("CPU halted on a single address error")
	}
}

func TestChk(t *testing.T) {
	// CHK.W #10,D0 = 0x41BC. In range: no trap.
	c, _ := newCPU(0x41BC, 0x000A, 0x4E71)
	arrange(c, func(r *Registers) { r.D[0] = 5 })
	c.Step()
	if got := c.Registers().PC; got != codeBase+4 {
		t.Errorf("CHK in-range PC = %08X, want %08X", got, codeBase+4)
	}

	// Above the bound: trap to vector 6 with N clear.
	c, bus := newCPU(0x41BC, 0x000A)
	bus.Write32(6*4, 0x2000)
	arrange(c, func(r *Registers) { r.D[0] = 20 })
	c.Step()
	if got := c.Registers().PC; got != 0x2000 {
		t.Errorf("CHK trap PC = %08X, want 2000", got)
	}
	flags(t, c, 0, srN)

	// Negative register: trap with N set.
	c, bus = newCPU(0x41BC, 0x000A)
	bus.Write32(6*4, 0x2000)
	arrange(c, func(r *Registers) { r.D[0] = 0xFFFF })
	c.Step()
	if got := c.Registers().PC; got != 0x2000 {
		t.Errorf("CHK negative PC = %08X, want 2000", got)
	}
	flags(t, c, srN, 0)
}

func TestMoveP(t *testing.T) {
	// MOVEP.L D0,(0,A0) = 0x01C8 spreads the bytes to even addresses.
	c, bus := newCPU(0x01C8, 0x0000)
	arrange(c, func(r *Registers) { r.A[0] = 0x2000; r.D[0] = 0x11223344 })
	c.Step()
	for i, want := range []uint8{0x11, 0x22, 0x33, 0x44} {
		if got := bus.Read8(0x2000 + uint32(2*i)); got != want {
			t.Errorf("MOVEP byte %d = %02X, want %02X", i, got, want)
		}
	}

	// MOVEP.W (0,A0),D1 = 0x0308 gathers them back, high word kept.
	c, bus = newCPU(0x0308, 0x0000)
	arrange(c, func(r *Registers) { r.A[0] = 0x2000; r.D[1] = 0xCAFE0000 })
	bus.Write8(0x2000, 0xAB)
	bus.Write8(0x2002, 0xCD)
	c.Step()
	if got := c.Registers().D[1]; got != 0xCAFEABCD {
		t.Errorf("MOVEP load = %08X, want CAFEABCD", got)
	}
}

func TestBCD(t *testing.T) {
	// ABCD D1,D0 = 0xC101: 0x15 + 0x27 = 0x42.
	c, _ := newCPU(0xC101)
	arrange(c, func(r *Registers) { r.D[0] = 0x15; r.D[1] = 0x27 })
	c.Step()
	if got := c.Registers().D[0] & 0xFF; got != 0x42 {
		t.Errorf("ABCD = %02X, want 42", got)
	}
	flags(t, c, 0, srC|srX)

	// ABCD carry case: 0x99 + 0x01 = 0x00 with carry.
	c, _ = newCPU(0xC101)
	arrange(c, func(r *Registers) { r.D[0] = 0x99; r.D[1] = 0x01 })
	c.Step()
	if got := c.Registers().D[0] & 0xFF; got != 0x00 {
		t.Errorf("ABCD carry = %02X, want 00", got)
	}
	flags(t, c, srC|srX, srZ) // note: Z is sticky; SR started clear of Z

	// SBCD D1,D0 = 0x8101: 0x42 - 0x15 = 0x27.
	c, _ = newCPU(0x8101)
	arrange(c, func(r *Registers) { r.D[0] = 0x42; r.D[1] = 0x15 })
	c.Step()
	if got := c.Registers().D[0] & 0xFF; got != 0x27 {
		t.Errorf("SBCD = %02X, want 27", got)
	}

	// NBCD D0 = 0x4800: 0 - 1 = 0x99 with borrow.
	c, _ = newCPU(0x4800)
	arrange(c, func(r *Registers) { r.D[0] = 0x01 })
	c.Step()
	if got := c.Registers().D[0] & 0xFF; got != 0x99 {
		t.Errorf("NBCD = %02X, want 99", got)
	}
	flags(t, c, srC|srX, 0)
}

func TestBitOpsAndTAS(t *testing.T) {
	// BTST #3,D0 = 0x0800, extension 0x0003: bit set -> Z clear.
	c, _ := newCPU(0x0800, 0x0003)
	arrange(c, func(r *Registers) { r.D[0] = 0x08 })
	c.Step()
	flags(t, c, 0, srZ)

	// Bit clear -> Z set.
	c, _ = newCPU(0x0800, 0x0003)
	arrange(c, func(r *Registers) { r.D[0] = 0x00 })
	c.Step()
	flags(t, c, srZ, 0)

	// BSET D1,(A0) = 0x03D0: D1 = 9, so bit 1 of the byte at (A0).
	c, bus := newCPU(0x03D0)
	arrange(c, func(r *Registers) { r.A[0] = 0x2000; r.D[1] = 9 })
	c.Step()
	if got := bus.Read8(0x2000); got != 0x02 {
		t.Errorf("BSET byte = %02X, want 02", got)
	}
	flags(t, c, srZ, 0) // the bit was clear before the set

	// TAS (A0) = 0x4AD0: tests the byte and sets its high bit.
	c, bus = newCPU(0x4AD0)
	arrange(c, func(r *Registers) { r.A[0] = 0x2000 })
	bus.Write8(0x2000, 0x7F)
	c.Step()
	if got := bus.Read8(0x2000); got != 0xFF {
		t.Errorf("TAS byte = %02X, want FF", got)
	}
	flags(t, c, 0, srN|srZ|srV|srC)
}

func TestMiscInstructions(t *testing.T) {
	// SWAP D0 = 0x4840.
	c, _ := newCPU(0x4840)
	arrange(c, func(r *Registers) { r.D[0] = 0x12345678 })
	c.Step()
	if got := c.Registers().D[0]; got != 0x56781234 {
		t.Errorf("SWAP = %08X, want 56781234", got)
	}

	// EXT.W D0 = 0x4880 then EXT.L D0 = 0x48C0.
	c, _ = newCPU(0x4880, 0x48C0)
	arrange(c, func(r *Registers) { r.D[0] = 0x00000080 })
	c.Step()
	if got := c.Registers().D[0]; got != 0x0000FF80 {
		t.Errorf("EXT.W = %08X, want 0000FF80", got)
	}
	c.Step()
	if got := c.Registers().D[0]; got != 0xFFFFFF80 {
		t.Errorf("EXT.L = %08X, want FFFFFF80", got)
	}

	// ADDQ.W #8,D0 = 0x5040 (data field 0 encodes 8): 0xFFFF + 8.
	c, _ = newCPU(0x5040)
	arrange(c, func(r *Registers) { r.D[0] = 0xFFFF })
	c.Step()
	if got := c.Registers().D[0]; got != 0x0007 {
		t.Errorf("ADDQ = %08X, want 0007", got)
	}
	flags(t, c, srC|srX, srZ)

	// LEA $10(A0),A1 = 0x43E8.
	c, _ = newCPU(0x43E8, 0x0010)
	arrange(c, func(r *Registers) { r.A[0] = 0x2000 })
	c.Step()
	if got := c.Registers().A[1]; got != 0x2010 {
		t.Errorf("LEA = %08X, want 2010", got)
	}

	// PEA (A0) = 0x4850 pushes the address itself.
	c, bus := newCPU(0x4850)
	arrange(c, func(r *Registers) { r.A[0] = 0x2000 })
	c.Step()
	if got := bus.Read32(0xEFFC); got != 0x2000 {
		t.Errorf("PEA pushed %08X, want 2000", got)
	}

	// LINK A0,#-4 = 0x4E50 / UNLK A0 = 0x4E58 round trip.
	c, _ = newCPU(0x4E50, 0xFFFC, 0x4E58)
	arrange(c, func(r *Registers) { r.A[0] = 0xAAAA0000 })
	c.Step()
	r := c.Registers()
	if r.A[0] != 0xF000-4 || r.A[7] != 0xF000-8 {
		t.Fatalf("LINK: A0=%08X A7=%08X, want EFFC/EFF8", r.A[0], r.A[7])
	}
	c.Step()
	r = c.Registers()
	if r.A[0] != 0xAAAA0000 || r.A[7] != 0xF000 {
		t.Errorf("UNLK: A0=%08X A7=%08X, want AAAA0000/F000", r.A[0], r.A[7])
	}

	// JSR $00001010 = 0x4EB9 / RTS = 0x4E75.
	c, bus = newCPU(0x4EB9, 0x0000, 0x1010, 0x4E71)
	bus.Write16(0x1010, 0x4E75)
	c.Step()
	if got := c.Registers().PC; got != 0x1010 {
		t.Fatalf("JSR PC = %08X, want 1010", got)
	}
	if got := bus.Read32(0xEFFC); got != codeBase+6 {
		t.Errorf("JSR return = %08X, want %08X", got, codeBase+6)
	}
	c.Step()
	if got := c.Registers().PC; got != codeBase+6 {
		t.Errorf("RTS PC = %08X, want %08X", got, codeBase+6)
	}

	// TRAP #5 = 0x4E45 goes through vector 37.
	c, bus = newCPU(0x4E45, 0x4E71)
	bus.Write32(37*4, 0x2000)
	c.Step()
	if got := c.Registers().PC; got != 0x2000 {
		t.Errorf("TRAP PC = %08X, want 2000", got)
	}
	if got := bus.Read32(0xEFFC); got != codeBase+2 {
		t.Errorf("TRAP stacked PC = %08X, want %08X", got, codeBase+2)
	}

	// CMPM.W (A0)+,(A1)+ = 0xB348 with equal words sets Z.
	c, bus = newCPU(0xB348)
	arrange(c, func(r *Registers) { r.A[0] = 0x2000; r.A[1] = 0x3000 })
	bus.Write16(0x2000, 5)
	bus.Write16(0x3000, 5)
	c.Step()
	flags(t, c, srZ, 0)
	r = c.Registers()
	if r.A[0] != 0x2002 || r.A[1] != 0x3002 {
		t.Errorf("CMPM postincrement: A0=%08X A1=%08X, want 2002/3002", r.A[0], r.A[1])
	}

	// CMPA.W #$1000,A0 = 0xB0FC with A0 = 0x1000 sets Z.
	c, _ = newCPU(0xB0FC, 0x1000)
	arrange(c, func(r *Registers) { r.A[0] = 0x1000 })
	c.Step()
	flags(t, c, srZ, 0)
}

func TestUserSupervisorStacks(t *testing.T) {
	// MOVE.W #$0300,SR in supervisor mode drops to user mode and swaps
	// A7 for the USP. The reset SSP is 0xF000; only USP needs staging.
	c, _ := newCPU(0x46FC, 0x0300)
	arrange(c, func(r *Registers) { r.USP = 0x9000 })
	c.Step()
	r := c.Registers()
	if r.A[7] != 0x9000 {
		t.Errorf("after drop to user, A[7] = %08X, want USP 9000", r.A[7])
	}
	if r.SSP != 0xF000 {
		t.Errorf("SSP field = %08X, want F000 (the stack we left)", r.SSP)
	}
}

func TestCycleAccounting(t *testing.T) {
	code := make([]uint16, 30)
	for i := range code {
		code[i] = 0x4E71 // NOP
	}
	c, _ := newCPU(code...)
	if n := c.Step(); n != 4 {
		t.Errorf("NOP Step = %d cycles, want 4", n)
	}
	if n := c.StepCycles(96); n != 96 {
		t.Errorf("StepCycles(96) = %d, want 96", n)
	}
	if got := c.Cycles(); got != 100 {
		t.Errorf("Cycles = %d, want 100", got)
	}
	if got := c.Registers().PC; got != codeBase+50 {
		t.Errorf("PC = %08X, want %08X", got, codeBase+50)
	}
	// MOVEQ is also 4 cycles.
	c2, _ := newCPU(0x7003)
	if n := c2.Step(); n != 4 {
		t.Errorf("MOVEQ Step = %d cycles, want 4", n)
	}
}

func TestDisassemble(t *testing.T) {
	tests := []struct {
		code []uint16
		want string
		next int // words consumed
	}{
		{[]uint16{0x3018}, "MOVE.W (A0)+,D0", 1},
		{[]uint16{0xD041}, "ADD.W D1,D0", 1},
		{[]uint16{0x7003}, "MOVEQ #$03,D0", 1},
		{[]uint16{0x6000, 0xFFFC}, "BRA $000FFE", 2},
		{[]uint16{0x51C8, 0xFFFE}, "DBF D0,$001000", 2},
		{[]uint16{0x48E7, 0xE0C0}, "MOVEM.L D0-D2/A0-A1,-(A7)", 2},
		{[]uint16{0x4E75}, "RTS", 1},
		{[]uint16{0x80FC, 0x000A}, "DIVU.W #$000A,D0", 2},
		{[]uint16{0xA000}, "DC.W $A000", 1},
		{[]uint16{0x303A, 0x004E}, "MOVE.W $4E(PC),D0", 2},
	}
	for _, tc := range tests {
		bus := newRAMBus()
		for i, w := range tc.code {
			bus.Write16(codeBase+uint32(2*i), w)
		}
		fetch := func(addr uint32) uint16 { return bus.Read16(addr) }
		got, next := Disassemble(codeBase, fetch)
		if got != tc.want {
			t.Errorf("Disassemble(%04X) = %q, want %q", tc.code[0], got, tc.want)
		}
		if want := uint32(codeBase + 2*tc.next); next != want {
			t.Errorf("Disassemble(%04X) next = %08X, want %08X", tc.code[0], next, want)
		}
	}
}
