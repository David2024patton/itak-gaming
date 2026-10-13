package z80

import "testing"

// ramBus is a flat 64KB memory with scriptable I/O ports.
type ramBus struct {
	mem    [65536]uint8
	inVal  map[uint16]uint8
	outLog []outWrite
}

type outWrite struct {
	port uint16
	val  uint8
}

func (b *ramBus) Fetch(a uint16) uint8    { return b.mem[a] }
func (b *ramBus) Read(a uint16) uint8     { return b.mem[a] }
func (b *ramBus) Write(a uint16, v uint8) { b.mem[a] = v }
func (b *ramBus) In(p uint16) uint8       { return b.inVal[p] }
func (b *ramBus) Out(p uint16, v uint8) {
	b.outLog = append(b.outLog, outWrite{p, v})
}

func newCPU(prog ...byte) (*CPU, *ramBus) {
	bus := &ramBus{inVal: map[uint16]uint8{}}
	copy(bus.mem[:], prog)
	return New(bus), bus
}

func flags(c *CPU) uint8 { return uint8(c.Registers().AF) }
func acc(c *CPU) uint8   { return uint8(c.Registers().AF >> 8) }

func TestADDFlagsOverflow(t *testing.T) {
	cpu, _ := newCPU(0x3E, 0x7F, 0xC6, 0x01) // LD A,0x7F; ADD A,0x01
	cpu.Step()
	if got := cpu.Step(); got != 7 {
		t.Fatalf("ADD A,n cost = %d, want 7", got)
	}
	if a := acc(cpu); a != 0x80 {
		t.Fatalf("A = %#02x, want 0x80", a)
	}
	// S=1, H=1 (nibble carry), PV=1 (signed overflow), Z/Y/X/N/C=0.
	if f, want := flags(cpu), uint8(flagS|flagH|flagPV); f != want {
		t.Fatalf("F = %#02x, want %#02x", f, want)
	}
}

func TestSUBFlagsBorrow(t *testing.T) {
	cpu, _ := newCPU(0x3E, 0x00, 0xD6, 0x01) // LD A,0; SUB 0x01
	cpu.Step()
	cpu.Step()
	if a := acc(cpu); a != 0xFF {
		t.Fatalf("A = %#02x, want 0xFF", a)
	}
	// S=1, Y=1, H=1, X=1, N=1, C=1; Z=0, PV=0.
	want := uint8(flagS | flagY | flagH | flagX | flagN | flagC)
	if f := flags(cpu); f != want {
		t.Fatalf("F = %#02x, want %#02x", f, want)
	}
}

func TestCPFlagsComeFromOperand(t *testing.T) {
	// CP with equal operands: result is 0, but the undocumented
	// Y/X bits must be copied from the operand (0x28), not the result.
	cpu, _ := newCPU(0x3E, 0x28, 0xFE, 0x28) // LD A,0x28; CP 0x28
	cpu.Step()
	cpu.Step()
	if a := acc(cpu); a != 0x28 {
		t.Fatalf("CP changed A: %#02x", a)
	}
	want := uint8(flagZ | flagY | flagX | flagN)
	if f := flags(cpu); f != want {
		t.Fatalf("F = %#02x, want %#02x", f, want)
	}
}

func TestDAA(t *testing.T) {
	tests := []struct {
		name  string
		prog  []byte
		wantA uint8
		wantF uint8
	}{
		// 0x15 + 0x27 = 0x3C -> DAA -> 0x42 (15 + 27 = 42 in BCD).
		{"add", []byte{0x3E, 0x15, 0xC6, 0x27, 0x27}, 0x42, flagH | flagPV},
		// 0x99 + 0x99 = 0x32 carry -> DAA -> 0x98, carry stays.
		// 0x98 has bit 3 set, so X is set; parity is odd, so PV clear.
		{"add carry", []byte{0x3E, 0x99, 0xC6, 0x99, 0x27}, 0x98, flagS | flagX | flagC},
		// 0x42 - 0x27 = 0x1B (N) -> DAA -> 0x15.
		{"sub", []byte{0x3E, 0x42, 0xD6, 0x27, 0x27}, 0x15, flagN},
		// 0x00 - 0x01 = 0xFF (N) -> DAA -> 0x99 with carry (borrow).
		// 0x99 = 1001 1001b: S and X set, Y clear, even parity (PV).
		{"sub borrow", []byte{0x3E, 0x00, 0xD6, 0x01, 0x27}, 0x99, flagS | flagX | flagPV | flagN | flagC},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cpu, _ := newCPU(tc.prog...)
			for range tc.prog {
				if cpu.Registers().PC >= uint16(len(tc.prog)) {
					break
				}
				cpu.Step()
			}
			if a := acc(cpu); a != tc.wantA {
				t.Fatalf("A = %#02x, want %#02x", a, tc.wantA)
			}
			if f := flags(cpu); f != tc.wantF {
				t.Fatalf("F = %#02x, want %#02x", f, tc.wantF)
			}
		})
	}
}

func TestINCDECFlags(t *testing.T) {
	// INC B from 0x7F: overflow to 0x80, carry (set by SCF) preserved.
	cpu, _ := newCPU(0x37, 0x06, 0x7F, 0x04) // SCF; LD B,0x7F; INC B
	cpu.Step()
	cpu.Step()
	cpu.Step()
	if b := cpu.Registers().BC >> 8; b != 0x80 {
		t.Fatalf("B = %#02x, want 0x80", b)
	}
	if f, want := flags(cpu), uint8(flagS|flagH|flagPV|flagC); f != want {
		t.Fatalf("INC F = %#02x, want %#02x", f, want)
	}
	// DEC B from 0x80: underflow to 0x7F, PV set, N set, carry kept.
	cpu, _ = newCPU(0x37, 0x06, 0x80, 0x05) // SCF; LD B,0x80; DEC B
	cpu.Step()
	cpu.Step()
	cpu.Step()
	if b := cpu.Registers().BC >> 8; b != 0x7F {
		t.Fatalf("B = %#02x, want 0x7F", b)
	}
	want := uint8(flagY | flagH | flagX | flagPV | flagN | flagC)
	if f := flags(cpu); f != want {
		t.Fatalf("DEC F = %#02x, want %#02x", f, want)
	}
}

func TestLDIRBlockTransfer(t *testing.T) {
	prog := []byte{
		0x21, 0x00, 0x20, // LD HL,0x2000
		0x11, 0x00, 0x30, // LD DE,0x3000
		0x01, 0x04, 0x00, // LD BC,0x0004
		0xED, 0xB0, // LDIR
		0x76, // HALT
	}
	cpu, bus := newCPU(prog...)
	copy(bus.mem[0x2000:], []byte{0x11, 0x22, 0x33, 0x44})
	cpu.Step()
	cpu.Step()
	cpu.Step()
	wantCosts := []int{21, 21, 21, 16}
	for i, want := range wantCosts {
		if got := cpu.Step(); got != want {
			t.Fatalf("LDIR iteration %d cost = %d, want %d", i, got, want)
		}
	}
	for i := 0; i < 4; i++ {
		if got := bus.mem[0x3000+i]; got != bus.mem[0x2000+i] {
			t.Fatalf("copied byte %d = %#02x, want %#02x", i, got, bus.mem[0x2000+i])
		}
	}
	reg := cpu.Registers()
	if reg.BC != 0 || reg.HL != 0x2004 || reg.DE != 0x3004 {
		t.Fatalf("end state BC=%#04x HL=%#04x DE=%#04x, want 0/0x2004/0x3004",
			reg.BC, reg.HL, reg.DE)
	}
	if reg.PC != 11 {
		t.Fatalf("PC = %d, want 11 (past the LDIR)", reg.PC)
	}
	if flags(cpu)&flagPV != 0 {
		t.Fatalf("PV must be clear when BC reaches 0, F = %#02x", flags(cpu))
	}
	if cpu.Cycles() != 10+10+10+21+21+21+16 {
		t.Fatalf("Cycles = %d, want 109", cpu.Cycles())
	}
}

func TestInterruptMode1(t *testing.T) {
	cpu, bus := newCPU()
	cpu.SetState(Registers{SP: 0xFF00, PC: 0x1000, IM: 1, IFF1: true})
	bus.mem[0x1000] = 0x00 // NOP
	cpu.INT(true, 0x00)
	if got := cpu.Step(); got != 4+13 {
		t.Fatalf("Step cost = %d, want 17 (NOP + IM1 response)", got)
	}
	reg := cpu.Registers()
	if reg.PC != 0x0038 {
		t.Fatalf("PC = %#04x, want 0x0038", reg.PC)
	}
	if reg.SP != 0xFEFE {
		t.Fatalf("SP = %#04x, want 0xFEFE", reg.SP)
	}
	if pushed := uint16(bus.mem[0xFEFF])<<8 | uint16(bus.mem[0xFEFE]); pushed != 0x1001 {
		t.Fatalf("pushed PC = %#04x, want 0x1001", pushed)
	}
	if reg.IFF1 || reg.IFF2 {
		t.Fatalf("IFF1/IFF2 must be clear after INT acceptance")
	}
}

func TestInterruptMode2Vector(t *testing.T) {
	cpu, bus := newCPU()
	cpu.SetState(Registers{SP: 0xFF00, PC: 0x1000, IM: 2, I: 0x12, IFF1: true})
	bus.mem[0x1000] = 0x00
	bus.mem[0x1234] = 0x78 // vector table entry 0x5678, little endian
	bus.mem[0x1235] = 0x56
	cpu.INT(true, 0x34)
	if got := cpu.Step(); got != 4+19 {
		t.Fatalf("Step cost = %d, want 23 (NOP + IM2 response)", got)
	}
	if pc := cpu.Registers().PC; pc != 0x5678 {
		t.Fatalf("PC = %#04x, want 0x5678 (from vector 0x1234)", pc)
	}
	if pushed := uint16(bus.mem[0xFEFF])<<8 | uint16(bus.mem[0xFEFE]); pushed != 0x1001 {
		t.Fatalf("pushed PC = %#04x, want 0x1001", pushed)
	}
}

func TestNMIAndRETN(t *testing.T) {
	cpu, bus := newCPU()
	cpu.SetState(Registers{SP: 0xFF00, PC: 0x2000, IFF1: true})
	bus.mem[0x2000] = 0x00 // NOP
	bus.mem[0x0066] = 0xED // RETN
	bus.mem[0x0067] = 0x45 //
	cpu.NMI()
	if got := cpu.Step(); got != 4+11 {
		t.Fatalf("Step cost = %d, want 15 (NOP + NMI response)", got)
	}
	reg := cpu.Registers()
	if reg.PC != 0x0066 {
		t.Fatalf("PC = %#04x, want 0x0066", reg.PC)
	}
	if reg.IFF1 {
		t.Fatal("IFF1 must be clear inside the NMI handler")
	}
	if !reg.IFF2 {
		t.Fatal("IFF2 must hold the pre-NMI IFF1 value")
	}
	if pushed := uint16(bus.mem[0xFEFF])<<8 | uint16(bus.mem[0xFEFE]); pushed != 0x2001 {
		t.Fatalf("pushed PC = %#04x, want 0x2001", pushed)
	}
	if got := cpu.Step(); got != 14 {
		t.Fatalf("RETN cost = %d, want 14", got)
	}
	reg = cpu.Registers()
	if reg.PC != 0x2001 || !reg.IFF1 {
		t.Fatalf("after RETN PC = %#04x IFF1 = %v, want 0x2001/true", reg.PC, reg.IFF1)
	}
}

func TestEIDelay(t *testing.T) {
	cpu, bus := newCPU()
	cpu.SetState(Registers{SP: 0xFF00, PC: 0x3000, IM: 1})
	bus.mem[0x3000] = 0xFB // EI
	bus.mem[0x3001] = 0x00 // NOP
	bus.mem[0x3002] = 0x00 // NOP
	cpu.INT(true, 0x00)
	cpu.Step() // EI: the interrupt must NOT be taken yet
	if reg := cpu.Registers(); reg.PC != 0x3001 || reg.SP != 0xFF00 {
		t.Fatalf("after EI: PC = %#04x SP = %#04x, want 0x3001/0xFF00", reg.PC, reg.SP)
	}
	cpu.Step() // NOP: now the pending interrupt is accepted
	reg := cpu.Registers()
	if reg.PC != 0x0038 {
		t.Fatalf("after the instruction following EI: PC = %#04x, want 0x0038", reg.PC)
	}
	if pushed := uint16(bus.mem[0xFEFF])<<8 | uint16(bus.mem[0xFEFE]); pushed != 0x3002 {
		t.Fatalf("pushed PC = %#04x, want 0x3002", pushed)
	}
}

func TestHALTAndWake(t *testing.T) {
	cpu, bus := newCPU()
	cpu.SetState(Registers{SP: 0xFF00, PC: 0x4000, IM: 1, IFF1: true})
	bus.mem[0x4000] = 0x76 // HALT
	bus.mem[0x4001] = 0x00 // NOP
	if got := cpu.Step(); got != 4 {
		t.Fatalf("HALT cost = %d, want 4", got)
	}
	if !cpu.Registers().Halted {
		t.Fatal("CPU must report Halted after HALT")
	}
	if got := cpu.Step(); got != 4 {
		t.Fatalf("HALT idle cost = %d, want 4", got)
	}
	if pc := cpu.Registers().PC; pc != 0x4001 {
		t.Fatalf("PC while halted = %#04x, want 0x4001", pc)
	}
	cpu.INT(true, 0x00)
	if got := cpu.Step(); got != 13 {
		t.Fatalf("wake+IM1 cost = %d, want 13", got)
	}
	reg := cpu.Registers()
	if reg.Halted || reg.PC != 0x0038 {
		t.Fatalf("after wake: Halted = %v PC = %#04x, want false/0x0038", reg.Halted, reg.PC)
	}
	if pushed := uint16(bus.mem[0xFEFF])<<8 | uint16(bus.mem[0xFEFE]); pushed != 0x4001 {
		t.Fatalf("pushed PC = %#04x, want 0x4001 (instruction after HALT)", pushed)
	}
}

func TestRefreshRegister(t *testing.T) {
	cpu, bus := newCPU()
	cpu.SetState(Registers{PC: 0x0000, R: 0xFF})
	bus.mem[0x0000] = 0x00 // NOP: one M1 cycle
	cpu.Step()
	if r := cpu.Registers().R; r != 0x80 {
		t.Fatalf("R = %#02x, want 0x80 (low 7 bits wrap, bit 7 kept)", r)
	}
	// LD A,R is ED 5F: two M1 fetches, so A receives R bumped twice.
	cpu.SetState(Registers{PC: 0x0000, R: 0x80})
	bus.mem[0x0000] = 0xED
	bus.mem[0x0001] = 0x5F
	cpu.Step()
	if a := acc(cpu); a != 0x82 {
		t.Fatalf("A after LD A,R = %#02x, want 0x82", a)
	}
}

func TestIOPortFormation(t *testing.T) {
	// OUT (n),A puts A in the high byte of the port address.
	cpu, bus := newCPU(0xD3, 0x10)
	cpu.SetState(Registers{AF: 0xAB00, PC: 0})
	if got := cpu.Step(); got != 11 {
		t.Fatalf("OUT (n),A cost = %d, want 11", got)
	}
	if len(bus.outLog) != 1 || bus.outLog[0] != (outWrite{0xAB10, 0xAB}) {
		t.Fatalf("out log = %v, want [{0xAB10 0xAB}]", bus.outLog)
	}
	// IN A,(n) reads through the same address formation.
	cpu, bus = newCPU(0xDB, 0x10)
	cpu.SetState(Registers{AF: 0xAB00, PC: 0})
	bus.inVal[0xAB10] = 0x5A
	cpu.Step()
	if a := acc(cpu); a != 0x5A {
		t.Fatalf("A = %#02x, want 0x5A", a)
	}
	// OUT (C),B uses the full BC pair as the port.
	cpu, bus = newCPU(0xED, 0x41)
	cpu.SetState(Registers{BC: 0x1234, PC: 0})
	cpu.Step()
	if len(bus.outLog) != 1 || bus.outLog[0] != (outWrite{0x1234, 0x12}) {
		t.Fatalf("out log = %v, want [{0x1234 0x12}]", bus.outLog)
	}
	// IN A,(C): flags from the value, carry preserved.
	cpu, bus = newCPU(0xED, 0x78)
	cpu.SetState(Registers{AF: 0x0001, BC: 0x1234, PC: 0}) // C flag set
	bus.inVal[0x1234] = 0x80
	cpu.Step()
	if a := acc(cpu); a != 0x80 {
		t.Fatalf("A = %#02x, want 0x80", a)
	}
	if f, want := flags(cpu), uint8(flagS|flagC); f != want {
		t.Fatalf("IN flags = %#02x, want %#02x", f, want)
	}
}

func TestINIBlockIOFlags(t *testing.T) {
	// INI with C = 0x10 and input 0xF0: k = 0xF0 + (0x10 + 1) = 0x101,
	// so H and C set; N from bit 7 of the input; B drops to 0 so Z set.
	cpu, bus := newCPU(0xED, 0xA2)
	cpu.SetState(Registers{BC: 0x0110, HL: 0x3000, PC: 0})
	bus.inVal[0x0110] = 0xF0
	if got := cpu.Step(); got != 16 {
		t.Fatalf("INI cost = %d, want 16", got)
	}
	if bus.mem[0x3000] != 0xF0 {
		t.Fatalf("(HL) = %#02x, want 0xF0", bus.mem[0x3000])
	}
	reg := cpu.Registers()
	if reg.BC != 0x0010 || reg.HL != 0x3001 {
		t.Fatalf("BC = %#04x HL = %#04x, want 0x0010/0x3001", reg.BC, reg.HL)
	}
	want := uint8(flagZ | flagH | flagN | flagC)
	if f := flags(cpu); f != want {
		t.Fatalf("INI flags = %#02x, want %#02x", f, want)
	}
}

func TestIndexedOps(t *testing.T) {
	// LD (IX+5),0x42 then LD A,(IX+5).
	cpu, bus := newCPU(0xDD, 0x36, 0x05, 0x42, 0xDD, 0x7E, 0x05)
	cpu.SetState(Registers{IX: 0x2000, PC: 0})
	if got := cpu.Step(); got != 19 {
		t.Fatalf("LD (IX+d),n cost = %d, want 19", got)
	}
	if bus.mem[0x2005] != 0x42 {
		t.Fatalf("(IX+5) = %#02x, want 0x42", bus.mem[0x2005])
	}
	if got := cpu.Step(); got != 19 {
		t.Fatalf("LD A,(IX+d) cost = %d, want 19", got)
	}
	if a := acc(cpu); a != 0x42 {
		t.Fatalf("A = %#02x, want 0x42", a)
	}
	// Index halves: LD IXh,0x33 and LD A,IXh.
	cpu, _ = newCPU(0xDD, 0x26, 0x33, 0xDD, 0x7C)
	cpu.SetState(Registers{IX: 0x2000, PC: 0})
	if got := cpu.Step(); got != 11 {
		t.Fatalf("LD IXh,n cost = %d, want 11", got)
	}
	if ix := cpu.Registers().IX; ix != 0x3300 {
		t.Fatalf("IX = %#04x, want 0x3300", ix)
	}
	if got := cpu.Step(); got != 8 {
		t.Fatalf("LD A,IXh cost = %d, want 8", got)
	}
	if a := acc(cpu); a != 0x33 {
		t.Fatalf("A = %#02x, want 0x33", a)
	}
}

func TestDDCBSetAndBit(t *testing.T) {
	// SET 3,(IX+2) with the register-copy form landing in B (z = 0).
	cpu, bus := newCPU(0xDD, 0xCB, 0x02, 0xD8)
	cpu.SetState(Registers{IX: 0x2000, PC: 0})
	if got := cpu.Step(); got != 23 {
		t.Fatalf("DDCB SET cost = %d, want 23", got)
	}
	if bus.mem[0x2002] != 0x08 {
		t.Fatalf("(IX+2) = %#02x, want 0x08", bus.mem[0x2002])
	}
	if b := cpu.Registers().BC >> 8; b != 0x08 {
		t.Fatalf("B (register copy) = %#02x, want 0x08", b)
	}
	// BIT 3,(IX+2): set, so Z clear; cost 20. BIT 2: clear, Z and PV set.
	cpu, _ = newCPU(0xDD, 0xCB, 0x02, 0x5E, 0xDD, 0xCB, 0x02, 0x56)
	cpu.SetState(Registers{IX: 0x2000, PC: 0})
	bus2 := cpu.bus.(*ramBus)
	bus2.mem[0x2002] = 0x08
	if got := cpu.Step(); got != 20 {
		t.Fatalf("DDCB BIT cost = %d, want 20", got)
	}
	if f := flags(cpu); f&flagZ != 0 || f&flagH == 0 {
		t.Fatalf("BIT 3 flags = %#02x, want H set and Z clear", f)
	}
	cpu.Step()
	if f := flags(cpu); f&(flagZ|flagPV) != flagZ|flagPV {
		t.Fatalf("BIT 2 flags = %#02x, want Z and PV set", f)
	}
}

func Test16BitArithmeticFlags(t *testing.T) {
	// SBC HL,BC: 0x0000 - 0x0001 -> 0xFFFF with borrow.
	cpu, _ := newCPU(0xED, 0x42)
	cpu.SetState(Registers{HL: 0x0000, BC: 0x0001, PC: 0})
	cpu.Step()
	if hl := cpu.Registers().HL; hl != 0xFFFF {
		t.Fatalf("HL = %#04x, want 0xFFFF", hl)
	}
	want := uint8(flagS | flagY | flagH | flagX | flagN | flagC)
	if f := flags(cpu); f != want {
		t.Fatalf("SBC flags = %#02x, want %#02x", f, want)
	}
	// ADC HL,BC: 0x7FFF + 0x0001 -> 0x8000, signed overflow.
	cpu, _ = newCPU(0xED, 0x4A)
	cpu.SetState(Registers{HL: 0x7FFF, BC: 0x0001, PC: 0})
	cpu.Step()
	if hl := cpu.Registers().HL; hl != 0x8000 {
		t.Fatalf("HL = %#04x, want 0x8000", hl)
	}
	if f, want := flags(cpu), uint8(flagS|flagH|flagPV); f != want {
		t.Fatalf("ADC flags = %#02x, want %#02x", f, want)
	}
}

func TestDJNZLoop(t *testing.T) {
	cpu, _ := newCPU(0x06, 0x03, 0x10, 0xFE, 0x76) // LD B,3; DJNZ -2; HALT
	if got := cpu.Step(); got != 7 {
		t.Fatalf("LD B,n cost = %d, want 7", got)
	}
	for i, want := range []int{13, 13, 8} {
		if got := cpu.Step(); got != want {
			t.Fatalf("DJNZ step %d cost = %d, want %d", i, got, want)
		}
	}
	reg := cpu.Registers()
	if reg.BC != 0 || reg.PC != 4 {
		t.Fatalf("B = %d PC = %d, want 0/4", reg.BC>>8, reg.PC)
	}
	if cpu.Cycles() != 7+13+13+8 {
		t.Fatalf("Cycles = %d, want 41", cpu.Cycles())
	}
}

func TestLoadAIFlags(t *testing.T) {
	// LD A,I copies IFF2 into PV.
	cpu, _ := newCPU(0xED, 0x57)
	cpu.SetState(Registers{I: 0x42, IFF2: true, PC: 0})
	cpu.Step()
	if a := acc(cpu); a != 0x42 {
		t.Fatalf("A = %#02x, want 0x42", a)
	}
	if f := flags(cpu); f != flagPV {
		t.Fatalf("F = %#02x, want just PV (%#02x)", f, uint8(flagPV))
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	cpu, bus := newCPU(0x06, 0x03, 0x10, 0xFE, 0x76)
	cpu.Step()
	cpu.Step()
	cpu.INT(true, 0x34)
	wantRegs := cpu.Registers()
	wantCycles := cpu.Cycles()

	buf := make([]byte, SerializeSize)
	if err := cpu.Serialize(buf); err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	other, _ := newCPU(0x00)
	other.StepCycles(500) // scramble cycles and state
	if err := other.Deserialize(buf); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if got := other.Registers(); got != wantRegs {
		t.Fatalf("registers after round trip:\n got %+v\nwant %+v", got, wantRegs)
	}
	if other.Cycles() != wantCycles {
		t.Fatalf("cycles after round trip = %d, want %d", other.Cycles(), wantCycles)
	}
	buf2 := make([]byte, SerializeSize)
	if err := other.Serialize(buf2); err != nil {
		t.Fatalf("re-Serialize: %v", err)
	}
	for i := range buf {
		if buf[i] != buf2[i] {
			t.Fatalf("state byte %d differs after round trip: %#02x != %#02x", i, buf[i], buf2[i])
		}
	}
	_ = bus
	if err := cpu.Serialize(make([]byte, 8)); err == nil {
		t.Fatal("Serialize into a short buffer must fail")
	}
	bad := make([]byte, SerializeSize)
	copy(bad, buf)
	bad[0] = 0x7E
	if err := other.Deserialize(bad); err == nil {
		t.Fatal("Deserialize with a bad version must fail")
	}
}

func TestStepCyclesBudget(t *testing.T) {
	cpu, _ := newCPU() // all-zero memory: endless NOPs
	if got := cpu.StepCycles(100); got != 100 {
		t.Fatalf("StepCycles(100) = %d, want 100 (25 NOPs at 4 T)", got)
	}
	if cpu.Cycles() != 100 {
		t.Fatalf("Cycles = %d, want 100", cpu.Cycles())
	}
	if got := cpu.StepCycles(6); got != 8 {
		t.Fatalf("StepCycles(6) = %d, want 8 (whole instructions only)", got)
	}
}
