package cpu6502

import "testing"

// flatBus is a 64 KB RAM with no devices: every address reads
// back what was written, which is all the CPU unit tests need.
type flatBus struct{ mem [65536]uint8 }

func (b *flatBus) Read(a uint16) uint8     { return b.mem[a] }
func (b *flatBus) Write(a uint16, v uint8) { b.mem[a] = v }

// newCPUAt returns a CPU reset into a program placed at start.
func newCPUAt(bus *flatBus, start uint16, prog ...uint8) *CPU {
	bus.mem[VectorReset] = uint8(start)
	bus.mem[VectorReset+1] = uint8(start >> 8)
	copy(bus.mem[start:], prog)
	c := New()
	c.Attach(bus)
	c.Reset()
	return c
}

// stepCycles runs one step and reports its cycle cost.
func stepCycles(c *CPU) int {
	before := c.Cycles()
	c.Step()
	return int(c.Cycles() - before)
}

func TestADCBinaryEvenWithDecimalFlag(t *testing.T) {
	bus := &flatBus{}
	// SED; LDA #$50; ADC #$50. In binary: $A0, V set, C clear.
	// If decimal arithmetic leaked in, the result would be $00
	// with C set, which is exactly what nestest checks for.
	c := newCPUAt(bus, 0x8000, 0xF8, 0xA9, 0x50, 0x69, 0x50)
	c.Step() // SED
	c.Step() // LDA
	c.Step() // ADC
	r := c.Regs()
	if r.A != 0xA0 {
		t.Fatalf("A = %02X, want A0 (binary ADC with D set)", r.A)
	}
	if r.P&FlagC != 0 {
		t.Fatalf("C set, want clear")
	}
	if r.P&FlagV == 0 {
		t.Fatalf("V clear, want set")
	}
	if r.P&FlagD == 0 {
		t.Fatalf("D flag itself should still read as set")
	}
}

func TestSBCBinaryEvenWithDecimalFlag(t *testing.T) {
	bus := &flatBus{}
	// SED; LDA #$00; SEC; SBC #$01: binary $FF with borrow.
	c := newCPUAt(bus, 0x8000, 0xF8, 0xA9, 0x00, 0x38, 0xE9, 0x01)
	for i := 0; i < 4; i++ {
		c.Step()
	}
	r := c.Regs()
	if r.A != 0xFF {
		t.Fatalf("A = %02X, want FF (binary SBC with D set)", r.A)
	}
	if r.P&FlagC != 0 {
		t.Fatalf("C set, want clear (borrow)")
	}
}

func TestIndexedPageCrossCycles(t *testing.T) {
	bus := &flatBus{}
	// LDX #$02; LDA $80FF,X crosses into $8101: 5 cycles.
	// With X = 0 the same instruction costs 4.
	c := newCPUAt(bus, 0x8000, 0xA2, 0x02, 0xBD, 0xFF, 0x80)
	c.Step()
	if got := stepCycles(c); got != 5 {
		t.Fatalf("LDA abs,X crossing cost %d, want 5", got)
	}
	bus2 := &flatBus{}
	c2 := newCPUAt(bus2, 0x8000, 0xA2, 0x00, 0xBD, 0xFF, 0x80)
	c2.Step()
	if got := stepCycles(c2); got != 4 {
		t.Fatalf("LDA abs,X not crossing cost %d, want 4", got)
	}
}

func TestBranchCyclePenalties(t *testing.T) {
	// Not taken: 2 cycles. Taken same page: 3. Taken across a
	// page boundary: 4.
	bus := &flatBus{}
	c := newCPUAt(bus, 0x8000, 0xD0, 0x10) // BNE +16, Z set after reset? P=$24: Z clear
	stepCycles(c)                          // Z is clear, so BNE is taken, same page
	if c.PC() != 0x8012 {
		t.Fatalf("PC = %04X, want 8012", c.PC())
	}
	bus2 := &flatBus{}
	c2 := newCPUAt(bus2, 0x8000, 0xF0, 0x10) // BEQ +16, Z clear: not taken
	if got := stepCycles(c2); got != 2 {
		t.Fatalf("branch not taken cost %d, want 2", got)
	}
	bus3 := &flatBus{}
	c3 := newCPUAt(bus3, 0x80F0, 0xD0, 0x20) // BNE +32 from $80F2 to $8112: page cross
	if got := stepCycles(c3); got != 4 {
		t.Fatalf("branch taken crossing cost %d, want 4", got)
	}
	bus4 := &flatBus{}
	c4 := newCPUAt(bus4, 0x8000, 0xD0, 0x10)
	if got := stepCycles(c4); got != 3 {
		t.Fatalf("branch taken cost %d, want 3", got)
	}
}

func TestJMPIndirectPageWrapBug(t *testing.T) {
	bus := &flatBus{}
	bus.mem[0x02FF] = 0x34
	bus.mem[0x0200] = 0x80 // high byte must come from $0200, not $0300
	bus.mem[0x0300] = 0x99
	c := newCPUAt(bus, 0x8000, 0x6C, 0xFF, 0x02)
	c.Step()
	if c.PC() != 0x8034 {
		t.Fatalf("PC = %04X, want 8034 (the wrap bug)", c.PC())
	}
}

func TestBRKPushesBreakStatus(t *testing.T) {
	bus := &flatBus{}
	bus.mem[VectorIRQ] = 0x00
	bus.mem[VectorIRQ+1] = 0x90
	c := newCPUAt(bus, 0x8000, 0x00)
	if got := stepCycles(c); got != 7 {
		t.Fatalf("BRK cost %d, want 7", got)
	}
	// Stack after reset (SP $FD): pushes land at $01FD (PCH),
	// $01FC (PCL), $01FB (status).
	if bus.mem[0x01FB] != 0x34 { // live P $24, plus B and U
		t.Fatalf("BRK status byte = %02X, want 34", bus.mem[0x01FB])
	}
	if c.PC() != 0x9000 {
		t.Fatalf("PC = %04X, want 9000", c.PC())
	}
	if c.Regs().P&FlagI == 0 {
		t.Fatalf("BRK must set I")
	}
}

func TestInterruptPushesClearBreakStatus(t *testing.T) {
	bus := &flatBus{}
	bus.mem[VectorIRQ] = 0x00
	bus.mem[VectorIRQ+1] = 0x90
	// CLI, then NOPs. After CLI the IRQ line is honored.
	c := newCPUAt(bus, 0x8000, 0x58, 0xEA, 0xEA)
	c.Step() // CLI: P goes to $20
	c.SetIRQLine(true)
	if got := stepCycles(c); got != 7 {
		t.Fatalf("interrupt sequence cost %d, want 7", got)
	}
	if bus.mem[0x01FB]&FlagB != 0 {
		t.Fatalf("hardware interrupt status byte has B set: %02X", bus.mem[0x01FB])
	}
	if bus.mem[0x01FB]&FlagU == 0 {
		t.Fatalf("hardware interrupt status byte lost the U bit")
	}
	if c.PC() != 0x9000 {
		t.Fatalf("PC = %04X, want 9000", c.PC())
	}
	// Return address must be the NOP's address, $8001.
	if bus.mem[0x01FD] != 0x80 || bus.mem[0x01FC] != 0x01 {
		t.Fatalf("return address pushed = %02X%02X, want 8001", bus.mem[0x01FD], bus.mem[0x01FC])
	}
}

// hijackBus raises the NMI line the moment the CPU pushes BRK's
// return address, simulating a PPU vblank landing mid-BRK.
type hijackBus struct {
	flatBus
	cpu *CPU
}

func (b *hijackBus) Write(a uint16, v uint8) {
	b.flatBus.Write(a, v)
	if a == 0x01FD && b.cpu != nil {
		b.cpu.SetNMILine(true)
	}
}

func TestNMIHijacksBRK(t *testing.T) {
	bus := &hijackBus{}
	bus.mem[VectorIRQ] = 0x00
	bus.mem[VectorIRQ+1] = 0x90
	bus.mem[VectorNMI] = 0x00
	bus.mem[VectorNMI+1] = 0xA0
	bus.mem[VectorReset] = 0x00
	bus.mem[VectorReset+1] = 0x80
	bus.mem[0x8000] = 0x00 // BRK
	c := New()
	c.Attach(bus)
	bus.cpu = c
	c.Reset()
	c.Step()
	if c.PC() != 0xA000 {
		t.Fatalf("PC = %04X, want A000 (NMI vector, hijacked)", c.PC())
	}
	// The pushed bytes are still BRK's: B-set status, return past
	// the padding byte.
	if bus.mem[0x01FB] != 0x34 {
		t.Fatalf("status byte = %02X, want 34 (BRK bytes)", bus.mem[0x01FB])
	}
	if c.NMIPending() {
		t.Fatalf("NMI latch should be consumed by the hijack")
	}
}

func TestPHPAndPLPBitRules(t *testing.T) {
	bus := &flatBus{}
	// LDA #$00 (Z set), PHP, SEC, CLC... then PLP restores.
	c := newCPUAt(bus, 0x8000,
		0xA9, 0x00, // LDA #$00: P = U|Z = $26 after reset's $24 loses I? (I stays) P=$26
		0x08, // PHP
		0x38, // SEC
		0x28, // PLP
	)
	c.Step()
	c.Step() // PHP
	pushed := bus.mem[0x01FD]
	if pushed&FlagB == 0 || pushed&FlagU == 0 {
		t.Fatalf("PHP byte %02X must carry B and U", pushed)
	}
	c.Step() // SEC
	if c.Regs().P&FlagC == 0 {
		t.Fatalf("SEC did not set C")
	}
	c.Step() // PLP
	r := c.Regs()
	if r.P&FlagC != 0 {
		t.Fatalf("PLP should have restored C clear")
	}
	if r.P&FlagB != 0 {
		t.Fatalf("live P must never carry B")
	}
	if r.P&FlagU == 0 {
		t.Fatalf("live P must always carry U")
	}
}

func TestUnofficialComposites(t *testing.T) {
	bus := &flatBus{}
	bus.mem[0x0010] = 0x81
	// SLO $10: memory becomes $02 with C set, A = A OR $02.
	c := newCPUAt(bus, 0x8000, 0x07, 0x10)
	c.Step()
	if bus.mem[0x0010] != 0x02 {
		t.Fatalf("SLO memory = %02X, want 02", bus.mem[0x0010])
	}
	if c.Regs().A != 0x02 || c.Regs().P&FlagC == 0 {
		t.Fatalf("SLO result A=%02X P=%02X, want A=02 C set", c.Regs().A, c.Regs().P)
	}

	bus2 := &flatBus{}
	bus2.mem[0x0020] = 0x05
	// LDA #$05; DCP $20: memory $04, compare A with $04: C set, Z clear.
	c2 := newCPUAt(bus2, 0x8000, 0xA9, 0x05, 0xC7, 0x20)
	c2.Step()
	c2.Step()
	if bus2.mem[0x0020] != 0x04 {
		t.Fatalf("DCP memory = %02X, want 04", bus2.mem[0x0020])
	}
	if c2.Regs().P&FlagC == 0 || c2.Regs().P&FlagZ != 0 {
		t.Fatalf("DCP flags P=%02X, want C set Z clear", c2.Regs().P)
	}

	bus3 := &flatBus{}
	// LDA #$FF; TAX; AXS #$01: (A&X)=$0F? No: TAX copies $FF.
	// Use LDA #$1F; TAX; AXS #$01: A&X=$1F, X=$1E, C set.
	c3 := newCPUAt(bus3, 0x8000, 0xA9, 0x1F, 0xAA, 0xCB, 0x01)
	c3.Step()
	c3.Step()
	c3.Step()
	if c3.Regs().X != 0x1E || c3.Regs().P&FlagC == 0 {
		t.Fatalf("AXS result X=%02X P=%02X, want X=1E C set", c3.Regs().X, c3.Regs().P)
	}

	bus4 := &flatBus{}
	// LDA #$80; ANC #$FF: A=$80, C copies N (set).
	c4 := newCPUAt(bus4, 0x8000, 0xA9, 0x80, 0x0B, 0xFF)
	c4.Step()
	c4.Step()
	if c4.Regs().A != 0x80 || c4.Regs().P&FlagC == 0 {
		t.Fatalf("ANC result A=%02X P=%02X, want A=80 C set", c4.Regs().A, c4.Regs().P)
	}

	bus5 := &flatBus{}
	// LDA #$81; CLC; ARR #$FF: A&$FF=$81, ROR with C clear gives
	// $40; C is bit 6 (set), V is bit6 XOR bit5 (set).
	c5 := newCPUAt(bus5, 0x8000, 0xA9, 0x81, 0x18, 0x6B, 0xFF)
	c5.Step()
	c5.Step()
	c5.Step()
	r5 := c5.Regs()
	if r5.A != 0x40 || r5.P&FlagC == 0 || r5.P&FlagV == 0 {
		t.Fatalf("ARR result A=%02X P=%02X, want A=40 C and V set", r5.A, r5.P)
	}

	bus6 := &flatBus{}
	// LDA #$01; SEC; ARR #$01: AND gives $01, ROR with C set
	// gives $80. C and V must both be clear (bits 6 and 5 of
	// $80 are both 0) even though bit 7 is set. This is the case
	// Blargg's checksum sweep catches.
	c6 := newCPUAt(bus6, 0x8000, 0xA9, 0x01, 0x38, 0x6B, 0x01)
	c6.Step()
	c6.Step()
	c6.Step()
	r6 := c6.Regs()
	if r6.A != 0x80 || r6.P&FlagC != 0 || r6.P&FlagV != 0 || r6.P&FlagN == 0 {
		t.Fatalf("ARR result A=%02X P=%02X, want A=80 N set, C and V clear", r6.A, r6.P)
	}
}

func TestJamAndStall(t *testing.T) {
	bus := &flatBus{}
	c := newCPUAt(bus, 0x8000, 0x02, 0xEA) // KIL, NOP
	c.Step()
	if !c.Jammed() {
		t.Fatalf("KIL did not jam the CPU")
	}
	pc := c.PC()
	c.Step()
	if c.PC() != pc {
		t.Fatalf("jammed CPU moved PC to %04X", c.PC())
	}
	c.Reset()
	if c.Jammed() {
		t.Fatalf("reset must clear the jam")
	}

	bus2 := &flatBus{}
	c2 := newCPUAt(bus2, 0x8000, 0xEA)
	c2.Stall(3)
	for i := 0; i < 3; i++ {
		if got := stepCycles(c2); got != 1 {
			t.Fatalf("stall step cost %d, want 1", got)
		}
		if c2.PC() != 0x8000 {
			t.Fatalf("stall burned an instruction, PC=%04X", c2.PC())
		}
	}
	c2.Step() // the NOP finally executes
	if c2.PC() != 0x8001 {
		t.Fatalf("PC after stall = %04X, want 8001", c2.PC())
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	bus := &flatBus{}
	c := newCPUAt(bus, 0x8000, 0xA9, 0x42, 0xAA, 0xE8, 0x8D, 0x00, 0x02)
	c.Step()
	c.Step()
	snap := c.Serialize()
	want := c.Regs()
	wantCycles := c.Cycles()
	// Scramble and restore.
	c.Step()
	c.Step()
	c.Step()
	if err := c.Deserialize(snap); err != nil {
		t.Fatal(err)
	}
	if c.Regs() != want || c.Cycles() != wantCycles {
		t.Fatalf("restored %+v cycles %d, want %+v cycles %d", c.Regs(), c.Cycles(), want, wantCycles)
	}
	if err := c.Deserialize([]byte{1, 2, 3}); err == nil {
		t.Fatalf("short state accepted")
	}
	bad := append([]byte{}, snap...)
	bad[0] = 99
	if err := c.Deserialize(bad); err == nil {
		t.Fatalf("bad version accepted")
	}
}

// TestOpcodeSpaceCoverage steps every opcode once on a flat RAM
// bus and checks that the disassembler's instruction length
// agrees with the executor's PC advance (control-flow opcodes
// excluded; they land where they land), and that exactly the KIL
// set jams.
func TestOpcodeSpaceCoverage(t *testing.T) {
	controlFlow := map[uint8]bool{
		0x00: true, // BRK
		0x20: true, // JSR
		0x40: true, // RTI
		0x60: true, // RTS
		0x4C: true, // JMP abs
		0x6C: true, // JMP ind
	}
	kil := map[uint8]bool{}
	for _, op := range []uint8{0x02, 0x12, 0x22, 0x32, 0x42, 0x52, 0x62, 0x72, 0x92, 0xB2, 0xD2, 0xF2} {
		kil[op] = true
	}
	for op := 0; op < 256; op++ {
		bus := &flatBus{}
		c := newCPUAt(bus, 0x8000, uint8(op), 0x00, 0x00)
		c.Step()
		if c.Jammed() != kil[uint8(op)] {
			t.Fatalf("opcode %02X jammed=%v, want %v", op, c.Jammed(), kil[uint8(op)])
		}
		if controlFlow[uint8(op)] || kil[uint8(op)] {
			continue
		}
		want := uint16(disLen(disOf(uint8(op)).mode))
		if got := c.PC() - 0x8000; got != want {
			t.Fatalf("opcode %02X advanced PC by %d, disassembler says %d", op, got, want)
		}
	}
}
