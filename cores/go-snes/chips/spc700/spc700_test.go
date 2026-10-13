package spc700

import "testing"

// ramBus is a flat 64 KB test bus that records nothing:
// the unit tests inspect memory directly.
type ramBus struct {
	mem []byte
}

func newRAMBus() *ramBus { return &ramBus{mem: make([]byte, 1<<16)} }

func (b *ramBus) Read(addr uint16) uint8 { return b.mem[addr] }
func (b *ramBus) Write(addr uint16, v uint8) {
	b.mem[addr] = v
}
func (b *ramBus) at(addr uint16) uint8 { return b.mem[addr] }
func (b *ramBus) put(addr uint16, v uint8) {
	b.mem[addr] = v
}
func (b *ramBus) put16(addr uint16, v uint16) {
	b.mem[addr] = uint8(v)
	b.mem[addr+1] = uint8(v >> 8)
}

// base is the baseline register state: code at $8000, stack
// pointer at the top of page 1, flags clear (direct page 0).
func base() Registers {
	return Registers{PC: 0x8000, SP: 0xFF}
}

// newTestCPU returns a CPU attached to a fresh RAM bus,
// placed in the given state with code at PC.
func newTestCPU(regs Registers, code ...byte) (*CPU, *ramBus) {
	bus := newRAMBus()
	cpu := New()
	cpu.Attach(bus)
	cpu.SetRegs(regs)
	for i, v := range code {
		bus.put(regs.PC+uint16(i), v)
	}
	return cpu, bus
}

func TestResetLoadsVectorAndClearsState(t *testing.T) {
	bus := newRAMBus()
	bus.put16(0xFFFE, 0xFFC0)
	cpu := New()
	cpu.Attach(bus)
	cpu.SetRegs(Registers{PC: 0x1234, A: 0xAA, X: 0xBB, SP: 0x42, P: 0xFF})
	cpu.Reset()
	got := cpu.Regs()
	if got != (Registers{PC: 0xFFC0}) {
		t.Errorf("after reset: %+v, want PC=FFC0 and everything else zero", got)
	}
	if cpu.Cycles() != 0 || cpu.Halted() {
		t.Errorf("after reset: cycles=%d halted=%v", cpu.Cycles(), cpu.Halted())
	}
}

func TestADCFlagsAndHalfCarry(t *testing.T) {
	r := base()
	r.A = 0x0F
	cpu, _ := newTestCPU(r, 0x88, 0x01) // ADC A,#$01
	if n := cpu.Step(); n != 2 {
		t.Errorf("ADC imm cycles = %d, want 2", n)
	}
	got := cpu.Regs()
	if got.A != 0x10 || got.P&FlagH == 0 || got.P&FlagC != 0 {
		t.Errorf("0F+01: A=%02x P=%08b, want A=10 H set C clear", got.A, got.P)
	}
	r.A = 0x7F
	cpu2, _ := newTestCPU(r, 0x88, 0x01)
	cpu2.Step()
	got = cpu2.Regs()
	if got.A != 0x80 || got.P&FlagV == 0 || got.P&FlagN == 0 {
		t.Errorf("7F+01: A=%02x P=%08b, want A=80 V and N set", got.A, got.P)
	}
	r.A = 0xFF
	r.P = 0
	cpu3, _ := newTestCPU(r, 0x88, 0x01)
	cpu3.Step()
	got = cpu3.Regs()
	if got.A != 0x00 || got.P&FlagZ == 0 || got.P&FlagC == 0 || got.P&FlagH == 0 {
		t.Errorf("FF+01: A=%02x P=%08b, want A=00 Z, C, H set", got.A, got.P)
	}
}

func TestSBCBorrowAndHalfCarry(t *testing.T) {
	r := base()
	r.A = 0x10
	r.P = FlagC                         // no borrow in
	cpu, _ := newTestCPU(r, 0xA8, 0x01) // SBC A,#$01
	cpu.Step()
	got := cpu.Regs()
	// 0x10-0x01 = 0x0F: borrow out of bit 4, so H clear; no
	// borrow out of bit 7, so C set.
	if got.A != 0x0F || got.P&FlagH != 0 || got.P&FlagC == 0 {
		t.Errorf("10-01: A=%02x P=%08b, want A=0F H clear C set", got.A, got.P)
	}
	r.A = 0x00
	r.P = 0 // borrow in
	cpu2, _ := newTestCPU(r, 0xA8, 0x01)
	cpu2.Step()
	got = cpu2.Regs()
	if got.A != 0xFE || got.P&FlagC != 0 || got.P&FlagN == 0 {
		t.Errorf("00-01-borrow: A=%02x P=%08b, want A=FE C clear N set", got.A, got.P)
	}
}

func TestDAAAfterBCDAdd(t *testing.T) {
	r := base()
	r.A = 0x45
	cpu, _ := newTestCPU(r, 0x88, 0x55, 0xDF) // ADC #$55; DAA
	cpu.Step()                                // binary 45+55 = 9A, H and C clear
	if got := cpu.Regs(); got.A != 0x9A {
		t.Fatalf("pre-DAA A = %02x, want 9A", got.A)
	}
	cpu.Step()
	got := cpu.Regs()
	// BCD 45+55 = 100: A=00 with carry.
	if got.A != 0x00 || got.P&FlagC == 0 || got.P&FlagZ == 0 {
		t.Errorf("DAA: A=%02x P=%08b, want A=00 C and Z set", got.A, got.P)
	}
	r.A = 0x09
	cpu2, _ := newTestCPU(r, 0x88, 0x01, 0xDF) // 09+01 = 0A, DAA -> 10
	cpu2.Step()
	cpu2.Step()
	if got := cpu2.Regs(); got.A != 0x10 || got.P&FlagC != 0 {
		t.Errorf("DAA 09+01: A=%02x P=%08b, want A=10 C clear", got.A, got.P)
	}
}

func TestDASAfterBCDSubtract(t *testing.T) {
	r := base()
	r.A = 0x50
	r.P = FlagC
	cpu, _ := newTestCPU(r, 0xA8, 0x01, 0xBE) // SBC #$01; DAS
	cpu.Step()                                // binary 50-01 = 4F, H clear (nibble borrow), C set
	if got := cpu.Regs(); got.A != 0x4F || got.P&FlagH != 0 {
		t.Fatalf("pre-DAS: A=%02x P=%08b, want A=4F H clear", got.A, got.P)
	}
	cpu.Step()
	got := cpu.Regs()
	// BCD 50-01 = 49.
	if got.A != 0x49 || got.P&FlagC == 0 {
		t.Errorf("DAS: A=%02x P=%08b, want A=49 C set", got.A, got.P)
	}
}

func TestMULFlagsFromHighByte(t *testing.T) {
	r := base()
	r.Y, r.A = 0x10, 0x10
	cpu, _ := newTestCPU(r, 0xCF) // MUL YA
	if n := cpu.Step(); n != 9 {
		t.Errorf("MUL cycles = %d, want 9", n)
	}
	got := cpu.Regs()
	// Product 0x0100: A=00 but Z must be clear, because the
	// flags come from Y (the high byte), not from A.
	if got.A != 0x00 || got.Y != 0x01 || got.P&FlagZ != 0 {
		t.Errorf("10*10: A=%02x Y=%02x P=%08b, want A=00 Y=01 Z clear", got.A, got.Y, got.P)
	}
	r.Y, r.A = 0xFF, 0xFF
	cpu2, _ := newTestCPU(r, 0xCF)
	cpu2.Step()
	got = cpu2.Regs()
	if got.Y != 0xFE || got.A != 0x01 || got.P&FlagN == 0 {
		t.Errorf("FF*FF: YA=%02x%02x P=%08b, want FE01 N set", got.Y, got.A, got.P)
	}
}

func TestDIVNormalOverflowAndZero(t *testing.T) {
	r := base()
	r.Y, r.A, r.X = 0x00, 100, 10
	cpu, _ := newTestCPU(r, 0x9E) // DIV YA,X
	if n := cpu.Step(); n != 12 {
		t.Errorf("DIV cycles = %d, want 12", n)
	}
	got := cpu.Regs()
	if got.A != 10 || got.Y != 0 || got.P&FlagV != 0 {
		t.Errorf("100/10: A=%d Y=%d P=%08b, want A=10 Y=0 V clear", got.A, got.Y, got.P)
	}
	// Overflow: 0x80E1 / 0x53. The hardware divider leaves
	// A=8D, Y=2A with V set (the true remainder would be
	// 4A; the overflow range is the documented quirk).
	r.Y, r.A, r.X = 0x80, 0xE1, 0x53
	cpu2, _ := newTestCPU(r, 0x9E)
	cpu2.Step()
	got = cpu2.Regs()
	if got.A != 0x8D || got.Y != 0x2A || got.P&FlagV == 0 || got.P&FlagN == 0 {
		t.Errorf("80E1/53: A=%02x Y=%02x P=%08b, want A=8D Y=2A V and N set", got.A, got.Y, got.P)
	}
	// Divide by zero runs the same algorithm with a zero
	// divisor: YA=0 becomes A=FF, Y=00, V set, H set (the
	// nibble compare 0 >= 0).
	r.Y, r.A, r.X = 0x00, 0x00, 0x00
	cpu3, _ := newTestCPU(r, 0x9E)
	cpu3.Step()
	got = cpu3.Regs()
	if got.A != 0xFF || got.Y != 0x00 || got.P&FlagV == 0 || got.P&FlagH == 0 {
		t.Errorf("0/0: A=%02x Y=%02x P=%08b, want A=FF Y=00 V and H set", got.A, got.Y, got.P)
	}
	// H is the nibble compare of the original operands.
	r.Y, r.A, r.X = 0x25, 0x00, 0x13
	cpu4, _ := newTestCPU(r, 0x9E)
	cpu4.Step()
	if got := cpu4.Regs(); got.P&FlagH == 0 {
		t.Errorf("DIV H: P=%08b, want H set ((Y&F)=5 >= (X&F)=3)", got.P)
	}
}

func TestMOVWDirectPageWrap(t *testing.T) {
	r := base()
	r.P = FlagP                           // direct page = page 1
	cpu, bus := newTestCPU(r, 0xBA, 0xFF) // MOVW YA,$FF
	bus.put(0x01FF, 0x34)                 // low byte at the page end
	bus.put(0x0100, 0x12)                 // high byte wraps inside the page
	bus.put(0x0200, 0x99)                 // decoy: the next page must not be read
	if n := cpu.Step(); n != 5 {
		t.Errorf("MOVW load cycles = %d, want 5", n)
	}
	got := cpu.Regs()
	if got.A != 0x34 || got.Y != 0x12 {
		t.Errorf("MOVW wrap: YA=%02x%02x, want 1234", got.Y, got.A)
	}
	// The store wraps the same way.
	r.A, r.Y = 0xEF, 0xBE
	cpu2, bus2 := newTestCPU(r, 0xDA, 0xFF) // MOVW $FF,YA
	cpu2.Step()
	if bus2.at(0x01FF) != 0xEF || bus2.at(0x0100) != 0xBE || bus2.at(0x0200) != 0x00 {
		t.Errorf("MOVW store wrap: [01FF]=%02x [0100]=%02x [0200]=%02x, want EF BE 00",
			bus2.at(0x01FF), bus2.at(0x0100), bus2.at(0x0200))
	}
}

func TestStackPushPopOrderAndPageWrap(t *testing.T) {
	r := base()
	r.SP = 0x00
	r.A, r.X = 0xAA, 0xBB
	cpu, bus := newTestCPU(r,
		0x2D, // PUSH A: writes $0100, SP wraps to FF
		0x4D, // PUSH X: writes $01FF, SP = FE
		0xCE, // POP X
		0xAE, // POP A
	)
	cpu.Step()
	if bus.at(0x0100) != 0xAA || cpu.Regs().SP != 0xFF {
		t.Fatalf("PUSH A at SP=00: [0100]=%02x SP=%02x, want AA FF", bus.at(0x0100), cpu.Regs().SP)
	}
	cpu.Step()
	if bus.at(0x01FF) != 0xBB || cpu.Regs().SP != 0xFE {
		t.Fatalf("PUSH X: [01FF]=%02x SP=%02x, want BB FE", bus.at(0x01FF), cpu.Regs().SP)
	}
	cpu.Step()
	cpu.Step()
	got := cpu.Regs()
	if got.X != 0xBB || got.A != 0xAA || got.SP != 0x00 {
		t.Errorf("after pops: X=%02x A=%02x SP=%02x, want BB AA 00", got.X, got.A, got.SP)
	}
	// POPs set no flags.
	if got.P != 0 {
		t.Errorf("POP set flags: P=%08b, want 0", got.P)
	}
}

func TestCallPushesExactReturnAndRet(t *testing.T) {
	r := base()
	cpu, bus := newTestCPU(r, 0x3F, 0x00, 0x90) // CALL $9000
	bus.put(0x9000, 0x6F)                       // RET
	if n := cpu.Step(); n != 8 {
		t.Errorf("CALL cycles = %d, want 8", n)
	}
	got := cpu.Regs()
	if got.PC != 0x9000 || got.SP != 0xFD {
		t.Fatalf("after CALL: PC=%04x SP=%02x, want 9000 FD", got.PC, got.SP)
	}
	// Return address is the exact next instruction, high
	// byte at $01FF, low at $01FE.
	if ret := uint16(bus.at(0x01FF))<<8 | uint16(bus.at(0x01FE)); ret != 0x8003 {
		t.Errorf("pushed return = %04x, want 8003", ret)
	}
	if n := cpu.Step(); n != 5 {
		t.Errorf("RET cycles = %d, want 5", n)
	}
	if got := cpu.Regs(); got.PC != 0x8003 || got.SP != 0xFF {
		t.Errorf("after RET: PC=%04x SP=%02x, want 8003 FF", got.PC, got.SP)
	}
}

func TestTCALLVectors(t *testing.T) {
	r := base()
	cpu, bus := newTestCPU(r, 0x31) // TCALL 3: vector at $FFDE-6 = $FFD8
	bus.put16(0xFFD8, 0x4000)
	if n := cpu.Step(); n != 8 {
		t.Errorf("TCALL cycles = %d, want 8", n)
	}
	if got := cpu.Regs(); got.PC != 0x4000 || got.SP != 0xFD {
		t.Errorf("TCALL 3: PC=%04x SP=%02x, want 4000 FD", got.PC, got.SP)
	}
	cpu2, bus2 := newTestCPU(r, 0xF1) // TCALL 15: vector at $FFC0
	bus2.put16(0xFFC0, 0x2222)
	cpu2.Step()
	if got := cpu2.Regs(); got.PC != 0x2222 {
		t.Errorf("TCALL 15: PC=%04x, want 2222", got.PC)
	}
	cpu3, bus3 := newTestCPU(r, 0x4F, 0x42) // PCALL $FF42
	if n := cpu3.Step(); n != 6 {
		t.Errorf("PCALL cycles = %d, want 6", n)
	}
	if got := cpu3.Regs(); got.PC != 0xFF42 {
		t.Errorf("PCALL: PC=%04x, want FF42", got.PC)
	}
	_ = bus3
}

func TestBRKPushesPSWAndRET1Restores(t *testing.T) {
	r := base()
	r.P = FlagI | FlagC
	cpu, bus := newTestCPU(r, 0x0F) // BRK
	bus.put16(0xFFDE, 0x9100)
	bus.put(0x9100, 0x7F) // RET1
	if n := cpu.Step(); n != 8 {
		t.Errorf("BRK cycles = %d, want 8", n)
	}
	got := cpu.Regs()
	if got.PC != 0x9100 || got.SP != 0xFC {
		t.Fatalf("after BRK: PC=%04x SP=%02x, want 9100 FC", got.PC, got.SP)
	}
	// Stack: PCH at $01FF, PCL at $01FE, PSW at $01FD, and
	// the pushed PSW is the pre-BRK one (B still clear in
	// the copy); the live PSW has B set and I clear.
	if bus.at(0x01FD) != FlagI|FlagC {
		t.Errorf("pushed PSW = %08b, want %08b", bus.at(0x01FD), FlagI|FlagC)
	}
	if got.P&FlagB == 0 || got.P&FlagI != 0 || got.P&FlagC == 0 {
		t.Errorf("after BRK: P=%08b, want B set, I clear, C kept", got.P)
	}
	if n := cpu.Step(); n != 6 {
		t.Errorf("RET1 cycles = %d, want 6", n)
	}
	got = cpu.Regs()
	if got.PC != 0x8001 || got.P != FlagI|FlagC || got.SP != 0xFF {
		t.Errorf("after RET1: PC=%04x P=%08b SP=%02x, want 8001 IC FF", got.PC, got.P, got.SP)
	}
}

func TestBBSAndBBC(t *testing.T) {
	r := base()
	cpu, bus := newTestCPU(r, 0x63, 0x10, 0x02) // BBS $10.3, +2
	bus.put(0x0010, 0x08)                       // bit 3 set
	if n := cpu.Step(); n != 7 {
		t.Errorf("BBS taken cycles = %d, want 7", n)
	}
	if got := cpu.Regs(); got.PC != 0x8005 {
		t.Errorf("BBS taken: PC=%04x, want 8005", got.PC)
	}
	cpu2, bus2 := newTestCPU(r, 0x63, 0x10, 0x02)
	bus2.put(0x0010, 0x00)
	if n := cpu2.Step(); n != 5 {
		t.Errorf("BBS not taken cycles = %d, want 5", n)
	}
	if got := cpu2.Regs(); got.PC != 0x8003 {
		t.Errorf("BBS not taken: PC=%04x, want 8003", got.PC)
	}
	cpu3, bus3 := newTestCPU(r, 0x73, 0x10, 0xFE) // BBC $10.3, -2 (bit clear: taken)
	bus3.put(0x0010, 0x00)
	cpu3.Step()
	if got := cpu3.Regs(); got.PC != 0x8001 {
		t.Errorf("BBC taken: PC=%04x, want 8001", got.PC)
	}
}

func TestCBNETakenAndNot(t *testing.T) {
	r := base()
	r.A = 0x42
	cpu, bus := newTestCPU(r, 0x2E, 0x20, 0x10) // CBNE $20, +16
	bus.put(0x0020, 0x42)                       // equal: not taken
	if n := cpu.Step(); n != 5 {
		t.Errorf("CBNE not taken cycles = %d, want 5", n)
	}
	if got := cpu.Regs(); got.PC != 0x8003 {
		t.Errorf("CBNE not taken: PC=%04x, want 8003", got.PC)
	}
	cpu2, bus2 := newTestCPU(r, 0xDE, 0x20, 0x10) // CBNE $20+X, +16
	bus2.put(0x0020, 0x41)
	cpu2.Step()
	// X=0 here, so the compare is against $20: not equal.
	if got := cpu2.Regs(); got.PC != 0x8013 {
		t.Errorf("CBNE dp+X taken: PC=%04x, want 8013", got.PC)
	}
	// CBNE affects no flags.
	if got := cpu2.Regs(); got.P != 0 {
		t.Errorf("CBNE set flags: P=%08b", got.P)
	}
}

func TestDBNZLoops(t *testing.T) {
	// MOV $10,#3; loop: DBNZ $10, loop; STOP
	cpu, bus := newTestCPU(base(),
		0x8F, 0x03, 0x10, // MOV $10,#$03
		0x6E, 0x10, 0xFD, // DBNZ $10,-3
		0xFF, // STOP
	)
	total := 0
	for i := 0; i < 3; i++ {
		total += cpu.Step()
	}
	if bus.at(0x0010) != 1 {
		t.Fatalf("after 2 DBNZ: dp[10]=%d, want 1", bus.at(0x0010))
	}
	total += cpu.Step() // third DBNZ falls through
	if got := cpu.Regs(); got.PC != 0x8006 {
		t.Fatalf("after loop: PC=%04x, want 8006", got.PC)
	}
	if total != 5+7+7+5 {
		t.Errorf("loop cycles = %d, want %d", total, 5+7+7+5)
	}
	if bus.at(0x0010) != 0 {
		t.Errorf("dp[10] = %d, want 0", bus.at(0x0010))
	}
	// DBNZ Y form: Y counts down in the register.
	r := base()
	r.Y = 2
	cpu2, _ := newTestCPU(r, 0xFE, 0xFE, 0xFF) // DBNZ Y,-2; STOP
	cpu2.Step()
	if got := cpu2.Regs(); got.PC != 0x8000 || got.Y != 1 {
		t.Errorf("DBNZ Y taken: PC=%04x Y=%d, want 8000 1", got.PC, got.Y)
	}
	if n := cpu2.Step(); n != 4 {
		t.Errorf("DBNZ Y not taken cycles = %d, want 4", n)
	}
}

func TestTSET1AndTCLR1Semantics(t *testing.T) {
	r := base()
	r.A = 0x0F
	r.P = FlagC | FlagH                         // both must survive: only N and Z change
	cpu, bus := newTestCPU(r, 0x0E, 0x34, 0x12) // TSET1 $1234
	bus.put(0x1234, 0xF0)
	if n := cpu.Step(); n != 6 {
		t.Errorf("TSET1 cycles = %d, want 6", n)
	}
	if got := bus.at(0x1234); got != 0xFF {
		t.Errorf("TSET1 result = %02x, want FF (F0 OR 0F)", got)
	}
	got := cpu.Regs()
	// Flags from CMP A,mem on the original value: 0F-F0 =
	// 1F, so N and Z clear; C and H are the caller's.
	if got.P != FlagC|FlagH {
		t.Errorf("TSET1 flags: P=%08b, want C and H only", got.P)
	}
	cpu2, bus2 := newTestCPU(r, 0x4E, 0x34, 0x12) // TCLR1 $1234
	bus2.put(0x1234, 0xFF)
	cpu2.Step()
	if got := bus2.at(0x1234); got != 0xF0 {
		t.Errorf("TCLR1 result = %02x, want F0 (FF AND NOT 0F)", got)
	}
}

func TestBitOpsOnCarry(t *testing.T) {
	// Address $0234 bit 5 encodes as operand bytes 34 A2:
	// low byte 34, then (bit<<5)|(addr>>8) = A0|02.
	r := base()
	cpu, bus := newTestCPU(r,
		0xAA, 0x34, 0xA2, // MOV1 C,$0234.5
		0x8A, 0x34, 0xA2, // EOR1 C,$0234.5
		0xCA, 0x34, 0xC2, // MOV1 $0234.6,C
		0xEA, 0x34, 0x82, // NOT1 $0234.4
		0x6A, 0x34, 0x22, // AND1 C,/$0234.1
		0x0A, 0x34, 0x22, // OR1 C,$0234.1
	)
	bus.put(0x0234, 0x20) // bit 5 set, bits 6,4,1 clear
	cpu.Step()            // MOV1: C = 1
	if got := cpu.Regs(); got.P&FlagC == 0 {
		t.Fatalf("MOV1 C,bit: C clear, want set")
	}
	cpu.Step() // EOR1 with a set bit: C = 0
	if got := cpu.Regs(); got.P&FlagC != 0 {
		t.Fatalf("EOR1: C set, want clear")
	}
	cpu.Step() // MOV1 bit,C with C=0: bit 6 stays clear
	if got := bus.at(0x0234); got != 0x20 {
		t.Fatalf("MOV1 bit,C: mem=%02x, want 20", got)
	}
	cpu.Step() // NOT1 bit 4: mem becomes 30
	if got := bus.at(0x0234); got != 0x30 {
		t.Fatalf("NOT1: mem=%02x, want 30", got)
	}
	cpu.Step() // AND1 C,/bit1: bit 1 is 0, inverted to 1; C was 0, stays 0
	if got := cpu.Regs(); got.P&FlagC != 0 {
		t.Fatalf("AND1 /bit: C set, want clear (C was 0)")
	}
	cpu.Step() // OR1 C,bit1: bit is 0, C stays 0... then set up a set-bit case below
	if got := cpu.Regs(); got.P&FlagC != 0 {
		t.Fatalf("OR1 bit: C set, want clear (bit is 0)")
	}
	// SET1/CLR1 on the direct page.
	cpu2, bus2 := newTestCPU(base(), 0xE2, 0x10, 0x12, 0x10) // SET1 $10.7; CLR1 $10.0
	bus2.put(0x0010, 0x01)
	cpu2.Step()
	if got := bus2.at(0x0010); got != 0x81 {
		t.Errorf("SET1: mem=%02x, want 81", got)
	}
	cpu2.Step()
	if got := bus2.at(0x0010); got != 0x80 {
		t.Errorf("CLR1: mem=%02x, want 80", got)
	}
}

func TestBranchesNegativeOffsetAndCycles(t *testing.T) {
	r := base()                         // Z clear
	cpu, _ := newTestCPU(r, 0xD0, 0xFC) // BNE -4
	if n := cpu.Step(); n != 4 {
		t.Errorf("BNE taken cycles = %d, want 4", n)
	}
	if got := cpu.Regs(); got.PC != 0x7FFE {
		t.Errorf("BNE -4: PC=%04x, want 7FFE", got.PC)
	}
	r.P = FlagZ
	cpu2, _ := newTestCPU(r, 0xD0, 0xFC)
	if n := cpu2.Step(); n != 2 {
		t.Errorf("BNE not taken cycles = %d, want 2", n)
	}
	if got := cpu2.Regs(); got.PC != 0x8002 {
		t.Errorf("BNE not taken: PC=%04x, want 8002", got.PC)
	}
	cpu3, _ := newTestCPU(base(), 0x2F, 0x02) // BRA +2
	if n := cpu3.Step(); n != 4 {
		t.Errorf("BRA cycles = %d, want 4", n)
	}
	if got := cpu3.Regs(); got.PC != 0x8004 {
		t.Errorf("BRA +2: PC=%04x, want 8004", got.PC)
	}
}

func TestWordALUFlags(t *testing.T) {
	r := base()
	cpu, bus := newTestCPU(r, 0x7A, 0x30) // ADDW YA,$30
	bus.put16(0x0030, 0x0001)
	r.A, r.Y = 0xFF, 0x0F // YA = 0FFF
	cpu.SetRegs(r)
	cpu.Step()
	got := cpu.Regs()
	// 0FFF+0001 = 1000: H set (carry out of bit 11), C clear.
	if got.A != 0x00 || got.Y != 0x10 || got.P&FlagH == 0 || got.P&FlagC != 0 {
		t.Errorf("ADDW: YA=%02x%02x P=%08b, want 1000 H set C clear", got.Y, got.A, got.P)
	}
	cpu2, bus2 := newTestCPU(base(), 0x9A, 0x30) // SUBW YA,$30
	bus2.put16(0x0030, 0x0001)
	r2 := base()
	r2.A, r2.Y = 0x00, 0x10 // YA = 1000
	cpu2.SetRegs(r2)
	cpu2.Step()
	got = cpu2.Regs()
	// 1000-0001 = 0FFF: C set (no borrow), H clear (the
	// low 12 bits borrowed: 000 < 001).
	if got.Y != 0x0F || got.A != 0xFF || got.P&FlagC == 0 || got.P&FlagH != 0 {
		t.Errorf("SUBW: YA=%02x%02x P=%08b, want 0FFF C set H clear", got.Y, got.A, got.P)
	}
	cpu3, bus3 := newTestCPU(base(), 0x5A, 0x30) // CMPW YA,$30
	bus3.put16(0x0030, 0x1234)
	r3 := base()
	r3.A, r3.Y = 0x34, 0x12
	cpu3.SetRegs(r3)
	cpu3.Step()
	if got := cpu3.Regs(); got.P&FlagZ == 0 || got.P&FlagC == 0 {
		t.Errorf("CMPW equal: P=%08b, want Z and C set", got.P)
	}
	// INCW wraps to zero and reports it.
	cpu4, bus4 := newTestCPU(base(), 0x3A, 0x30) // INCW $30
	bus4.put16(0x0030, 0xFFFF)
	cpu4.Step()
	if bus4.at(0x30) != 0 || bus4.at(0x31) != 0 || cpu4.Regs().P&FlagZ == 0 {
		t.Errorf("INCW wrap: word=%02x%02x P=%08b, want 0000 Z set",
			bus4.at(0x31), bus4.at(0x30), cpu4.Regs().P)
	}
}

func TestAddressingModesIndirect(t *testing.T) {
	r := base()
	r.Y = 0x05
	r.X = 0x20
	cpu, bus := newTestCPU(r,
		0xF7, 0x40, // MOV A,[$40]+Y
		0xE7, 0x10, // MOV A,[$10+X]
		0xBF, // MOV A,(X)+
		0xAF, // MOV (X)+,A
	)
	bus.put16(0x0040, 0x9000)
	bus.put(0x9005, 0x77)
	bus.put16(0x0030, 0x9100) // pointer at dp 10+X = 30
	bus.put(0x9100, 0x66)
	bus.put(0x0020, 0x55) // (X) with X=20
	cpu.Step()
	if got := cpu.Regs(); got.A != 0x77 {
		t.Fatalf("MOV A,[dp]+Y: A=%02x, want 77", got.A)
	}
	cpu.Step()
	if got := cpu.Regs(); got.A != 0x66 {
		t.Fatalf("MOV A,[dp+X]: A=%02x, want 66", got.A)
	}
	cpu.Step()
	got := cpu.Regs()
	if got.A != 0x55 || got.X != 0x21 {
		t.Fatalf("MOV A,(X)+: A=%02x X=%02x, want 55 21", got.A, got.X)
	}
	cpu.Step()
	got = cpu.Regs()
	if bus.at(0x0021) != 0x55 || got.X != 0x22 {
		t.Errorf("MOV (X)+,A: [0021]=%02x X=%02x, want 55 22", bus.at(0x0021), got.X)
	}
}

func TestDirectPageSelectFlag(t *testing.T) {
	cpu, bus := newTestCPU(base(),
		0x40,       // SETP
		0xE4, 0x10, // MOV A,$10 (page 1 while P is set)
		0x20,       // CLRP
		0xE4, 0x10, // MOV A,$10 (page 0)
	)
	bus.put(0x0110, 0x11)
	bus.put(0x0010, 0x22)
	cpu.Step()
	cpu.Step()
	if got := cpu.Regs(); got.A != 0x11 {
		t.Fatalf("SETP MOV A,dp: A=%02x, want 11 (page 1)", got.A)
	}
	cpu.Step()
	cpu.Step()
	if got := cpu.Regs(); got.A != 0x22 {
		t.Errorf("CLRP MOV A,dp: A=%02x, want 22 (page 0)", got.A)
	}
}

func TestShiftsRotatesAndXCN(t *testing.T) {
	r := base()
	r.A = 0x81
	cpu, _ := newTestCPU(r, 0x1C) // ASL A
	cpu.Step()
	if got := cpu.Regs(); got.A != 0x02 || got.P&FlagC == 0 {
		t.Errorf("ASL: A=%02x P=%08b, want 02 C set", got.A, got.P)
	}
	r.A = 0x02
	r.P = FlagC
	cpu2, _ := newTestCPU(r, 0x3C) // ROL A (carry in at bit 0)
	cpu2.Step()
	if got := cpu2.Regs(); got.A != 0x05 || got.P&FlagC != 0 {
		t.Errorf("ROL: A=%02x P=%08b, want 05 C clear", got.A, got.P)
	}
	r.A = 0x03
	r.P = FlagC
	cpu3, _ := newTestCPU(r, 0x7C) // ROR A (carry in at bit 7)
	cpu3.Step()
	if got := cpu3.Regs(); got.A != 0x81 || got.P&FlagC == 0 {
		t.Errorf("ROR: A=%02x P=%08b, want 81 C set", got.A, got.P)
	}
	r.A = 0xAB
	r.P = 0
	cpu4, _ := newTestCPU(r, 0x9F) // XCN A
	if n := cpu4.Step(); n != 5 {
		t.Errorf("XCN cycles = %d, want 5", n)
	}
	if got := cpu4.Regs(); got.A != 0xBA {
		t.Errorf("XCN: A=%02x, want BA", got.A)
	}
}

func TestMOVDpDpAndDpImmOperandOrder(t *testing.T) {
	// MOV dp,dp takes source first, then destination.
	cpu, bus := newTestCPU(base(), 0xFA, 0x30, 0x40)
	bus.put(0x0030, 0x99)
	cpu.Step()
	if bus.at(0x0040) != 0x99 || bus.at(0x0030) != 0x99 {
		t.Errorf("MOV dp,dp: dst=%02x src=%02x, want 99 99", bus.at(0x0040), bus.at(0x0030))
	}
	// MOV dp,#imm takes the immediate first, then the dp.
	cpu2, bus2 := newTestCPU(base(), 0x8F, 0x77, 0x40)
	cpu2.Step()
	if bus2.at(0x0040) != 0x77 {
		t.Errorf("MOV dp,#imm: [0040]=%02x, want 77", bus2.at(0x0040))
	}
}

func TestJMPIndirectX(t *testing.T) {
	r := base()
	r.X = 0x10
	cpu, bus := newTestCPU(r, 0x1F, 0x00, 0x20) // JMP [$2000+X]
	bus.put16(0x2010, 0x3456)
	if n := cpu.Step(); n != 6 {
		t.Errorf("JMP [abs+X] cycles = %d, want 6", n)
	}
	if got := cpu.Regs(); got.PC != 0x3456 {
		t.Errorf("JMP [abs+X]: PC=%04x, want 3456", got.PC)
	}
}

func TestHaltStopAndSleep(t *testing.T) {
	cpu, _ := newTestCPU(base(), 0xFF) // STOP
	if n := cpu.Step(); n != 16 {
		t.Errorf("STOP step cycles = %d, want 16", n)
	}
	if !cpu.Halted() {
		t.Fatalf("STOP did not halt")
	}
	before := cpu.Regs()
	if n := cpu.Step(); n != 16 {
		t.Errorf("halted step cycles = %d, want 16", n)
	}
	if cpu.Regs() != before {
		t.Errorf("halted step changed state: %+v vs %+v", cpu.Regs(), before)
	}
	if cpu.Cycles() != 32 {
		t.Errorf("cycles = %d, want 32", cpu.Cycles())
	}
	cpu.Reset()
	if cpu.Halted() {
		t.Errorf("reset did not clear the halt")
	}
	cpu2, _ := newTestCPU(base(), 0xEF) // SLEEP
	cpu2.Step()
	if !cpu2.Halted() {
		t.Errorf("SLEEP did not halt")
	}
}

func TestSerializeRoundtripMidProgram(t *testing.T) {
	// Program: MOV $10,#5; loop: DBNZ $10,loop; MOV A,#$42; STOP
	prog := []byte{0x8F, 0x05, 0x10, 0x6E, 0x10, 0xFD, 0xE8, 0x42, 0xFF}
	cpu, bus := newTestCPU(base(), prog...)
	cpu.Step()
	cpu.Step()
	state := cpu.Serialize()
	if len(state) != SerializeSize {
		t.Fatalf("Serialize len = %d, want %d", len(state), SerializeSize)
	}
	// Restore into a fresh CPU on a bus with identical
	// contents, then run both to the halt and compare
	// everything.
	bus2 := newRAMBus()
	copy(bus2.mem, bus.mem)
	cpu2 := New()
	cpu2.Attach(bus2)
	if err := cpu2.Deserialize(state); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if cpu2.Regs() != cpu.Regs() || cpu2.Cycles() != cpu.Cycles() {
		t.Fatalf("restored state differs: %+v/%d vs %+v/%d",
			cpu2.Regs(), cpu2.Cycles(), cpu.Regs(), cpu.Cycles())
	}
	for !cpu.Halted() {
		cpu.Step()
		cpu2.Step()
	}
	if cpu2.Regs() != cpu.Regs() || cpu2.Cycles() != cpu.Cycles() {
		t.Errorf("post-restore runs diverged: %+v/%d vs %+v/%d",
			cpu2.Regs(), cpu2.Cycles(), cpu.Regs(), cpu.Cycles())
	}
	for i := range bus.mem {
		if bus.mem[i] != bus2.mem[i] {
			t.Fatalf("bus diverged at %04x: %02x vs %02x", i, bus.mem[i], bus2.mem[i])
		}
	}
	if got := cpu.Regs(); got.A != 0x42 {
		t.Errorf("final A = %02x, want 42", got.A)
	}
	// Halted state roundtrips too.
	cpu3 := New()
	cpu3.Attach(newRAMBus())
	if err := cpu3.Deserialize(cpu.Serialize()); err != nil {
		t.Fatalf("Deserialize halted: %v", err)
	}
	if !cpu3.Halted() {
		t.Errorf("halted flag lost in round trip")
	}
	if err := cpu3.Deserialize([]byte{1, 2, 3}); err == nil {
		t.Errorf("short state accepted")
	}
	bad := cpu.Serialize()
	bad[0] = 0xFF
	if err := cpu3.Deserialize(bad); err == nil {
		t.Errorf("bad version accepted")
	}
}
