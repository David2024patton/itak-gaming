package sm83

import "testing"

// flatBus is a plain 64 KB test bus.
type flatBus struct{ mem [65536]uint8 }

func (b *flatBus) Read(addr uint16) uint8       { return b.mem[addr] }
func (b *flatBus) Write(addr uint16, val uint8) { b.mem[addr] = val }
func (b *flatBus) load(at uint16, bytes ...uint8) {
	copy(b.mem[at:], bytes)
}

// run executes n steps of a program placed at 0x0100 and returns the
// CPU and bus for inspection.
func run(t *testing.T, prog []uint8, setup func(c *CPU, b *flatBus), steps int) (*CPU, *flatBus) {
	t.Helper()
	b := &flatBus{}
	b.load(0x0100, prog...)
	c := New()
	c.SetRegs(Registers{SP: 0xFFFE, PC: 0x0100})
	if setup != nil {
		setup(c, b)
	}
	for i := 0; i < steps; i++ {
		c.Step(b)
	}
	return c, b
}

func TestDAAAfterAddition(t *testing.T) {
	// 0x15 + 0x27 = 0x3C raw; DAA must give 0x42, no carry.
	c, _ := run(t, []uint8{0x3E, 0x15, 0xC6, 0x27, 0x27}, nil, 3)
	if c.Regs().A != 0x42 {
		t.Fatalf("A = 0x%02X, want 0x42", c.Regs().A)
	}
	if c.Regs().F&flagC != 0 {
		t.Fatalf("carry set, want clear (F=0x%02X)", c.Regs().F)
	}
}

func TestDAAAfterAdditionWithCarry(t *testing.T) {
	// 0x99 + 0x99 = 0x98 with carry after DAA (99 + 99 = 198).
	c, _ := run(t, []uint8{0x3E, 0x99, 0xC6, 0x99, 0x27}, nil, 3)
	r := c.Regs()
	if r.A != 0x98 || r.F&flagC == 0 {
		t.Fatalf("A=0x%02X F=0x%02X, want A=0x98 with carry", r.A, r.F)
	}
}

func TestDAAAfterSubtraction(t *testing.T) {
	// 0x42 - 0x27 = 0x1B raw (N and H set); DAA must give 0x15.
	c, _ := run(t, []uint8{0x3E, 0x42, 0xD6, 0x27, 0x27}, nil, 3)
	r := c.Regs()
	if r.A != 0x15 {
		t.Fatalf("A = 0x%02X, want 0x15", r.A)
	}
	if r.F&flagN == 0 {
		t.Fatalf("N cleared by DAA, want preserved (F=0x%02X)", r.F)
	}
}

func TestCBRotateFlags(t *testing.T) {
	// RLC B with B=0x80: result 0x01, carry set, zero clear.
	c, _ := run(t, []uint8{0xCB, 0x00}, func(c *CPU, b *flatBus) {
		c.SetRegs(Registers{B: 0x80, SP: 0xFFFE, PC: 0x0100})
	}, 1)
	r := c.Regs()
	if r.B != 0x01 || r.F&flagC == 0 || r.F&flagZ != 0 {
		t.Fatalf("RLC: B=0x%02X F=0x%02X, want B=0x01 C=1 Z=0", r.B, r.F)
	}
	// RLC B with B=0x00: result 0x00, zero SET (unlike the A rotates),
	// carry clear.
	c, _ = run(t, []uint8{0xCB, 0x00}, func(c *CPU, b *flatBus) {
		c.SetRegs(Registers{B: 0x00, SP: 0xFFFE, PC: 0x0100})
	}, 1)
	r = c.Regs()
	if r.B != 0x00 || r.F&flagZ == 0 || r.F&flagC != 0 {
		t.Fatalf("RLC zero: B=0x%02X F=0x%02X, want B=0x00 Z=1 C=0", r.B, r.F)
	}
	// RL B through a clear carry with B=0x80: result 0x00, zero set,
	// carry set.
	c, _ = run(t, []uint8{0xCB, 0x10}, func(c *CPU, b *flatBus) {
		c.SetRegs(Registers{B: 0x80, F: 0, SP: 0xFFFE, PC: 0x0100})
	}, 1)
	r = c.Regs()
	if r.B != 0x00 || r.F&flagZ == 0 || r.F&flagC == 0 {
		t.Fatalf("RL: B=0x%02X F=0x%02X, want B=0x00 Z=1 C=1", r.B, r.F)
	}
}

func TestAccumulatorRotateZeroQuirk(t *testing.T) {
	// RLCA with A=0x00 gives 0x00 but the SM83 still clears Z.
	c, _ := run(t, []uint8{0x07}, func(c *CPU, b *flatBus) {
		c.SetRegs(Registers{A: 0x00, SP: 0xFFFE, PC: 0x0100})
	}, 1)
	if c.Regs().F&flagZ != 0 {
		t.Fatalf("RLCA set Z on a zero result; the SM83 never does (F=0x%02X)", c.Regs().F)
	}
}

func TestAddSPFlags(t *testing.T) {
	// SP=0xFFF8, +8: result 0x0000, H and C from the low byte, Z=0.
	c, _ := run(t, []uint8{0xE8, 0x08}, func(c *CPU, b *flatBus) {
		c.SetRegs(Registers{SP: 0xFFF8, PC: 0x0100})
	}, 1)
	r := c.Regs()
	if r.SP != 0x0000 {
		t.Fatalf("SP = 0x%04X, want 0x0000", r.SP)
	}
	if r.F != flagH|flagC {
		t.Fatalf("F = 0x%02X, want exactly H|C (Z and N clear)", r.F)
	}
	// SP=0x1000, +1: no half carry, no carry.
	c, _ = run(t, []uint8{0xE8, 0x01}, func(c *CPU, b *flatBus) {
		c.SetRegs(Registers{SP: 0x1000, PC: 0x0100})
	}, 1)
	if c.Regs().F != 0 {
		t.Fatalf("F = 0x%02X, want 0", c.Regs().F)
	}
	// SP=0x0008, -8: result 0x0000; low byte 0x08+0xF8 carries both.
	c, _ = run(t, []uint8{0xE8, 0xF8}, func(c *CPU, b *flatBus) {
		c.SetRegs(Registers{SP: 0x0008, PC: 0x0100})
	}, 1)
	r = c.Regs()
	if r.SP != 0x0000 || r.F != flagH|flagC {
		t.Fatalf("SP=0x%04X F=0x%02X, want SP=0 F=H|C", r.SP, r.F)
	}
}

func TestLDHAddressing(t *testing.T) {
	// LDH (0x80), A then LDH A, (0x80) via a different register.
	c, b := run(t, []uint8{0x3E, 0x5A, 0xE0, 0x80, 0x3E, 0x00, 0xF0, 0x80}, nil, 4)
	if b.mem[0xFF80] != 0x5A {
		t.Fatalf("memory at 0xFF80 = 0x%02X, want 0x5A", b.mem[0xFF80])
	}
	if c.Regs().A != 0x5A {
		t.Fatalf("A = 0x%02X, want 0x5A", c.Regs().A)
	}
}

func TestPopAFMasksFlags(t *testing.T) {
	// Stack holds 0xFFFF; POP AF must give A=0xFF and F=0xF0.
	c, _ := run(t, []uint8{0xF1}, func(c *CPU, b *flatBus) {
		b.mem[0xFFFC] = 0xFF
		b.mem[0xFFFD] = 0xFF
		c.SetRegs(Registers{SP: 0xFFFC, PC: 0x0100})
	}, 1)
	r := c.Regs()
	if r.A != 0xFF || r.F != 0xF0 {
		t.Fatalf("A=0x%02X F=0x%02X, want A=0xFF F=0xF0", r.A, r.F)
	}
}

func TestEIDelay(t *testing.T) {
	// EI; NOP; NOP with a pending enabled interrupt. The interrupt
	// must not dispatch until the instruction after EI completes.
	prog := []uint8{0xFB, 0x00, 0x00}
	setup := func(c *CPU, b *flatBus) {
		b.mem[0xFFFF] = 0x01 // IE: VBlank enabled
		b.mem[0xFF0F] = 0x01 // IF: VBlank requested
	}
	c, b := run(t, prog, setup, 1) // EI
	if c.Regs().PC != 0x0101 || c.IME() {
		t.Fatalf("after EI: PC=0x%04X IME=%v, want PC=0x0101 IME=false", c.Regs().PC, c.IME())
	}
	c.Step(b) // NOP executes; no dispatch yet
	r := c.Regs()
	if r.PC != 0x0102 || r.SP != 0xFFFE {
		t.Fatalf("after NOP post-EI: PC=0x%04X SP=0x%04X, want PC=0x0102 SP untouched", r.PC, r.SP)
	}
	c.Step(b) // now the dispatch happens
	r = c.Regs()
	if r.PC != 0x0040 {
		t.Fatalf("after dispatch step: PC=0x%04X, want 0x0040", r.PC)
	}
	if b.mem[0xFFFC] != 0x02 || b.mem[0xFFFD] != 0x01 {
		t.Fatalf("pushed return = 0x%02X%02X, want 0x0102", b.mem[0xFFFD], b.mem[0xFFFC])
	}
	if b.mem[0xFF0F]&0x01 != 0 {
		t.Fatalf("IF bit not cleared by dispatch")
	}
}

func TestHaltBugDoublesNextByte(t *testing.T) {
	// IME=0 with a pending enabled interrupt: HALT does not halt,
	// and the byte after HALT (INC A) executes twice.
	prog := []uint8{0x76, 0x3C, 0x3C}
	setup := func(c *CPU, b *flatBus) {
		b.mem[0xFFFF] = 0x01
		b.mem[0xFF0F] = 0x01
		c.SetRegs(Registers{A: 0, SP: 0xFFFE, PC: 0x0100})
	}
	c, _ := run(t, prog, setup, 1) // HALT
	if c.Halted() {
		t.Fatalf("CPU halted despite a pending interrupt with IME clear")
	}
	c2, _ := run(t, prog, setup, 3) // HALT, INC A (bugged fetch), INC A
	if c2.Regs().A != 2 {
		t.Fatalf("A = %d after HALT;INC A under the bug, want 2 (byte fetched twice)", c2.Regs().A)
	}
	if c2.Regs().PC != 0x0102 {
		t.Fatalf("PC = 0x%04X, want 0x0102 after the doubled byte plus one real fetch", c2.Regs().PC)
	}
}

func TestHaltWakesOnInterrupt(t *testing.T) {
	// Nothing pending: HALT halts; an idle step costs 1 cycle; when
	// the request arrives with IME clear the CPU resumes in place.
	b := &flatBus{}
	b.load(0x0100, 0x76, 0x3C) // HALT; INC A
	c := New()
	c.SetRegs(Registers{SP: 0xFFFE, PC: 0x0100})
	c.Step(b)
	if !c.Halted() {
		t.Fatalf("CPU did not halt with nothing pending")
	}
	if n := c.Step(b); n != 1 || !c.Halted() {
		t.Fatalf("idle halted step = %d cycles, halted=%v, want 1 and true", n, c.Halted())
	}
	b.mem[0xFFFF] = 0x01
	b.mem[0xFF0F] = 0x01
	c.Step(b) // wakes and runs INC A
	if c.Regs().A != 1 || c.Halted() {
		t.Fatalf("after wake: A=%d halted=%v, want A=1 halted=false", c.Regs().A, c.Halted())
	}
}

func TestInstructionCycles(t *testing.T) {
	cases := []struct {
		name   string
		prog   []uint8
		cycles int
	}{
		{"NOP", []uint8{0x00}, 1},
		{"LD BC,d16", []uint8{0x01, 0x34, 0x12}, 3},
		{"LD (a16),SP", []uint8{0x08, 0x00, 0xC0}, 5},
		{"CALL a16", []uint8{0xCD, 0x00, 0x02}, 6},
		{"RET", []uint8{0xC9}, 4},
		{"PUSH BC", []uint8{0xC5}, 4},
		{"POP BC", []uint8{0xC1}, 3},
		{"LD (HL),d8", []uint8{0x36, 0x99}, 3},
		{"BIT 7,(HL)", []uint8{0xCB, 0x7E}, 3},
		{"RLC (HL)", []uint8{0xCB, 0x06}, 4},
		{"JP (HL)", []uint8{0xE9}, 1},
		{"ADD SP,r8", []uint8{0xE8, 0x01}, 4},
	}
	for _, tc := range cases {
		b := &flatBus{}
		b.load(0x0100, tc.prog...)
		b.load(0x0200, 0xC9) // CALL target: RET
		b.mem[0xFFFC] = 0x00 // RET/POP stack fodder
		b.mem[0xFFFD] = 0x01
		c := New()
		c.SetRegs(Registers{SP: 0xFFFC, PC: 0x0100})
		if n := c.Step(b); n != tc.cycles {
			t.Errorf("%s: %d M-cycles, want %d", tc.name, n, tc.cycles)
		}
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	c, _ := run(t, []uint8{0x3E, 0x77, 0x06, 0x42}, nil, 2)
	buf := make([]byte, SerializeSize)
	if err := c.Serialize(buf); err != nil {
		t.Fatal(err)
	}
	c2 := New()
	if err := c2.Deserialize(buf); err != nil {
		t.Fatal(err)
	}
	if c.Regs() != c2.Regs() || c.Cycles() != c2.Cycles() {
		t.Fatalf("round trip mismatch: %+v vs %+v", c.Regs(), c2.Regs())
	}
	if err := c2.Deserialize([]byte{0xFF}); err == nil {
		t.Fatalf("short buffer accepted")
	}
	bad := make([]byte, SerializeSize)
	bad[0] = 99
	if err := c2.Deserialize(bad); err == nil {
		t.Fatalf("bad version accepted")
	}
}

func TestEIChainSchedulesOnce(t *testing.T) {
	// The mooneye ei_sequence scenario: the program raises IF and IE
	// itself, then executes a run of EIs. The enable scheduled by
	// the first EI fires after the second EI completes; later EIs
	// do not postpone it. The dispatch must push 0x010A (the third
	// EI's address) and jump to the serial vector.
	b := &flatBus{}
	b.load(0x100,
		0x3E, 0x08, // LD A, 8 (serial)
		0xEA, 0x0F, 0xFF, // LD (IF), A
		0xEA, 0xFF, 0xFF, // LD (IE), A
		0xFB, 0xFB, 0xFB, 0xFB, // EI x4 at 0x108..0x10B
		0x00,
	)
	c := &CPU{pc: 0x100, sp: 0xFFFE}
	for i := 0; i < 6; i++ {
		c.Step(b)
	}
	if c.pc != 0x58 {
		t.Fatalf("after 6 steps PC = 0x%04X, want serial vector 0x0058", c.pc)
	}
	ret := uint16(b.mem[c.sp]) | uint16(b.mem[c.sp+1])<<8
	if ret != 0x010A {
		t.Errorf("pushed return = 0x%04X, want 0x010A", ret)
	}
}

func TestEIBeforeHaltDispatches(t *testing.T) {
	// The mooneye halt_ime0_ei scenario at the CPU level: IE is
	// enabled, IF is clear, and EI immediately precedes HALT. When
	// the interrupt source raises IF while the CPU sits in HALT,
	// the CPU must dispatch (the EI has taken effect, so this is
	// IME=1 halt behavior), not just wake and continue.
	b := &flatBus{}
	b.load(0x100,
		0x3E, 0x01, // LD A, 1 (vblank)
		0xEA, 0xFF, 0xFF, // LD (IE), A
		0xFB, // EI
		0x76, // HALT
		0x00, // NOP (resume target if wrongly woken)
	)
	c := &CPU{pc: 0x100, sp: 0xFFFE}
	for i := 0; i < 4; i++ {
		c.Step(b)
	}
	if !c.halted {
		t.Fatal("CPU should be halted after HALT with no pending interrupt")
	}
	b.mem[0xFF0F] = 0x01 // interrupt source raises IF
	c.Step(b)
	if c.pc != 0x40 {
		t.Fatalf("PC = 0x%04X, want vblank vector 0x0040 (dispatch, not resume)", c.pc)
	}
	ret := uint16(b.mem[c.sp]) | uint16(b.mem[c.sp+1])<<8
	if ret != 0x0107 {
		t.Errorf("pushed return = 0x%04X, want 0x0107", ret)
	}
}
