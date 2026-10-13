package w65c816

import "testing"

// ramBus is a flat 16 MB test bus, uniform slow, that
// records nothing: the unit tests inspect memory directly.
type ramBus struct {
	mem []byte
}

func newRAMBus() *ramBus { return &ramBus{mem: make([]byte, 1<<24)} }

func (b *ramBus) Read(bank uint8, addr uint16) uint8 {
	return b.mem[uint32(bank)<<16|uint32(addr)]
}

func (b *ramBus) Write(bank uint8, addr uint16, val uint8) {
	b.mem[uint32(bank)<<16|uint32(addr)] = val
}

func (b *ramBus) Speed(uint8, uint16) Speed { return SpeedSlow }

func (b *ramBus) at(bank uint8, addr uint16) uint8 { return b.Read(bank, addr) }
func (b *ramBus) put(bank uint8, addr uint16, v uint8) {
	b.mem[uint32(bank)<<16|uint32(addr)] = v
}

// newTestCPU returns a CPU attached to a fresh RAM bus,
// placed in the given state with code at PBR:PC.
func newTestCPU(regs Registers, code ...byte) (*CPU, *ramBus) {
	bus := newRAMBus()
	cpu := New()
	cpu.Attach(bus)
	cpu.SetRegs(regs)
	for i, v := range code {
		bus.put(regs.PBR, regs.PC+uint16(i), v)
	}
	return cpu, bus
}

// native returns baseline native-mode registers: 16-bit
// accumulator and indexes, D=0, DBR=0, stack at 0x01FF,
// code at 00:8000.
func native() Registers {
	return Registers{S: 0x01FF, PC: 0x8000, P: 0x00, E: false}
}

// emu returns baseline emulation-mode registers.
func emu() Registers {
	return Registers{S: 0x01FF, PC: 0x8000, P: FlagM | FlagX, E: true}
}

func TestXCEClampsIndexHighBytes(t *testing.T) {
	r := native()
	r.X, r.Y = 0x1234, 0xABCD
	r.P = FlagC                   // carry set: XCE swaps it into E
	cpu, _ := newTestCPU(r, 0xFB) // XCE
	cpu.Step()
	got := cpu.Regs()
	if !got.E {
		t.Fatalf("E = false after XCE with carry set")
	}
	if got.X != 0x0034 || got.Y != 0x00CD {
		t.Errorf("X,Y = %#x,%#x, want 0x34, 0xCD (high bytes clamped)", got.X, got.Y)
	}
	if got.P&FlagC != 0 {
		t.Errorf("C still set after XCE swapped in the old E (0)")
	}
	if got.S != 0x01FF {
		t.Errorf("S = %#x, want 0x01FF (already page 1)", got.S)
	}
}

func TestXCERoundTripRestoresWidths(t *testing.T) {
	cpu, _ := newTestCPU(emu(), 0xFB, 0xFB) // XCE, XCE
	// Start: E=1, C=0. First XCE: E<-0, C<-1. Second: E<-1, C<-0.
	cpu.Step()
	if cpu.Regs().E {
		t.Fatalf("first XCE should leave emulation")
	}
	cpu.Step()
	if !cpu.Regs().E {
		t.Fatalf("second XCE should re-enter emulation")
	}
}

func TestSEPWidthTransitionPreservesB(t *testing.T) {
	r := native()
	r.A = 0xBEEF
	cpu, _ := newTestCPU(r, 0xE2, 0x20, 0xA9, 0x42, 0xEA) // SEP #$20; LDA #$42; NOP
	cpu.Step()                                            // SEP: M set
	cpu.Step()                                            // LDA #$42 (8-bit)
	got := cpu.Regs()
	if got.A != 0xBE42 {
		t.Errorf("A = %#x, want 0xBE42 (B accumulator preserved)", got.A)
	}
	cpu2, _ := newTestCPU(native(), 0xC2, 0x20) // REP #$20 from M=0: no-op on width
	cpu2.Step()
	if cpu2.Regs().P&FlagM != 0 {
		t.Errorf("REP set M")
	}
}

func TestDirectPageWrapEmulationVsNative(t *testing.T) {
	// LDA $FF with D=0x1200: emulation wraps the pointer
	// arithmetic in the page; native reads D+0xFF linear.
	code := []byte{0xA5, 0xFF} // LDA dp $FF
	r := emu()
	r.D = 0x1200
	cpu, bus := newTestCPU(r, code...)
	bus.put(0, 0x12FF, 0x11) // linear target
	bus.put(0, 0x1200+0xFF, 0x11)
	bus.put(0, 0x12FF, 0x11)
	// Emu wrap: address = (D & 0xFF00) | (0x00 + 0xFF) = 0x12FF
	// (DL=0, so it coincides here); the distinguishing case
	// is the pointer high byte of (dp) at offset 0xFF, see
	// the indirect test below.
	cpu.Step()
	if cpu.Regs().A != 0x11 {
		t.Errorf("emu LDA dp: A = %#x", cpu.Regs().A)
	}
	// Native with D=0x12FF, LDA $02: reads 0x1301.
	r2 := native()
	r2.D = 0x12FF
	cpu2, bus2 := newTestCPU(r2, 0xA5, 0x02)
	bus2.put(0, 0x1301, 0x77)
	bus2.put(0, 0x1201, 0x66) // page-wrapped decoy
	cpu2.Step()
	if cpu2.Regs().A != 0x77 {
		t.Errorf("native LDA dp: A = %#x, want 0x77 (no wrap)", cpu2.Regs().A)
	}
}

func TestDPIndirectPointerSecondByteIncrements(t *testing.T) {
	// LDA ($FF) with D=0x1200 in emulation: the base
	// addition wraps in the page (the pointer low byte is
	// at 0x12FF), but the pointer's high byte is fetched
	// from the next address up, 0x1300, with a plain
	// increment. The single-step corpus's (dp,X) boundary
	// traces show the second byte always at base+1.
	r := emu()
	r.D = 0x1200
	cpu, bus := newTestCPU(r, 0xB2, 0xFF) // LDA (dp) $FF
	bus.put(0, 0x12FF, 0x34)              // pointer low
	bus.put(0, 0x1300, 0x12)              // pointer high at base+1
	bus.put(0, 0x1200, 0x99)              // page-wrap decoy
	bus.put(0, 0x1234, 0x5C)              // the data
	cpu.Step()
	if cpu.Regs().A != 0x5C {
		t.Errorf("A = %#x, want 0x5C", cpu.Regs().A)
	}
}

func TestPHBPLBAndDBRWraps(t *testing.T) {
	r := native()
	r.DBR = 0x42
	cpu, bus := newTestCPU(r,
		0x8B,       // PHB
		0xA9, 0x99, // LDA #$99 (M=0: two bytes follow)
		0x00, // operand high byte of LDA (0x9900 & ...)
		0xAB, // PLB (pulls back 0x42 after we trash DBR)
	)
	_ = bus
	// Simpler direct sequence: PHB; LDA #imm16 (0x0099); PLB
	// would pull the wrong byte if the immediate is consumed
	// wrong. Instead verify with a hand-driven stack:
	cpu.Step() // PHB pushes 0x42
	if got := bus.at(0, 0x01FF); got != 0x42 {
		t.Fatalf("stack top = %#x, want 0x42", got)
	}
	cpu.SetRegs(Registers{S: 0x01FE, PC: 0x8004, DBR: 0x00, P: 0, E: false})
	// PC 0x8004 is the PLB in the stream above only if stream
	// laid out so; this state drives PLB directly:
	bus.put(0, 0x8004, 0xAB)
	cpu.Step()
	if cpu.Regs().DBR != 0x42 {
		t.Errorf("DBR = %#x, want 0x42", cpu.Regs().DBR)
	}
}

func TestJMPLongAndJSRReturn(t *testing.T) {
	r := native()
	cpu, bus := newTestCPU(r,
		0x22, 0x00, 0x90, 0x05, // JSL $05:9000
	)
	bus.put(0x05, 0x9000, 0x6B) // RTL
	cpu.Step()                  // JSL
	got := cpu.Regs()
	if got.PBR != 0x05 || got.PC != 0x9000 {
		t.Fatalf("after JSL: PBR:PC = %02x:%04x", got.PBR, got.PC)
	}
	cpu.Step() // RTL
	got = cpu.Regs()
	if got.PBR != 0x00 || got.PC != 0x8004 {
		t.Errorf("after RTL: PBR:PC = %02x:%04x, want 00:8004", got.PBR, got.PC)
	}
	if got.S != 0x01FF {
		t.Errorf("S = %#x, want restored 0x01FF", got.S)
	}
}

func TestJMPIndirectNoPageWrap(t *testing.T) {
	// The 65816 (unlike the NMOS 6502) does not wrap the
	// JMP (abs) pointer fetch at a page boundary: the high
	// byte comes from 0x0300 here. The single-step corpus
	// trace for 0x6C shows the 0x59FF to 0x5A00 fetch pair.
	r := native()
	cpu, bus := newTestCPU(r, 0x6C, 0xFF, 0x02) // JMP ($02FF)
	bus.put(0, 0x02FF, 0x00)
	bus.put(0, 0x0200, 0x90) // the 6502's wrap target: must NOT be used
	bus.put(0, 0x0300, 0x77)
	cpu.Step()
	if cpu.Regs().PC != 0x7700 {
		t.Errorf("PC = %#x, want 0x7700 (no page wrap)", cpu.Regs().PC)
	}
}

func TestBRKSkipsSignatureAndPushesBank(t *testing.T) {
	r := native()
	r.PBR = 0x03
	cpu, bus := newTestCPU(r, 0x00, 0xEA) // BRK, signature byte
	bus.put(0, 0xFFE6, 0x00)
	bus.put(0, 0xFFE7, 0x95) // native BRK vector = 0x9500
	bus.put(0, 0x9500, 0xEA)
	cpu.Step()
	got := cpu.Regs()
	if got.PC != 0x9500 || got.PBR != 0x00 {
		t.Fatalf("after BRK: %02x:%04x, want 00:9500", got.PBR, got.PC)
	}
	// Stack: PBR at 0x01FF, PCH 0x01FE, PCL 0x01FD, P 0x01FC.
	if v := bus.at(0, 0x01FF); v != 0x03 {
		t.Errorf("pushed PBR = %#x", v)
	}
	ret := uint16(bus.at(0, 0x01FE))<<8 | uint16(bus.at(0, 0x01FD))
	if ret != 0x8002 {
		t.Errorf("pushed return = %#x, want 0x8002 (signature skipped)", ret)
	}
	if got.P&FlagI == 0 {
		t.Errorf("I not set after BRK")
	}
}

func TestCOPUsesCOPVector(t *testing.T) {
	r := native()
	cpu, bus := newTestCPU(r, 0x02, 0x07) // COP 7
	bus.put(0, 0xFFE4, 0x00)
	bus.put(0, 0xFFE5, 0x91)
	cpu.Step()
	if cpu.Regs().PC != 0x9100 {
		t.Errorf("PC = %#x, want 0x9100 (COP vector)", cpu.Regs().PC)
	}
}

func TestBlockMoveCopiesAndSetsDBR(t *testing.T) {
	r := native()
	// MVN dst=0x7E src=0x00: copy 4 bytes (A=3) from
	// 00:9000.. to 7E:8000...
	cpu, bus := newTestCPU(r, 0x54, 0x7E, 0x00)
	cpu.SetRegs(Registers{A: 3, X: 0x9000, Y: 0x8000, S: 0x01FF, PC: 0x8000, E: false})
	for i, v := range []byte{0xDE, 0xAD, 0xBE, 0xEF} {
		bus.put(0x00, 0x9000+uint16(i), v)
	}
	cycles := cpu.Step()
	for i, want := range []byte{0xDE, 0xAD, 0xBE, 0xEF} {
		if got := bus.at(0x7E, 0x8000+uint16(i)); got != want {
			t.Errorf("dest[%d] = %#x, want %#x", i, got, want)
		}
	}
	got := cpu.Regs()
	if got.A != 0xFFFF {
		t.Errorf("A = %#x, want 0xFFFF (counter wrapped)", got.A)
	}
	if got.X != 0x9004 || got.Y != 0x8004 {
		t.Errorf("X,Y = %#x,%#x, want 0x9004,0x8004", got.X, got.Y)
	}
	if got.DBR != 0x7E {
		t.Errorf("DBR = %#x, want 0x7E (destination bank)", got.DBR)
	}
	// Cycle structure: seven cycles per byte copied, each
	// iteration carrying its own opcode and operand
	// fetches (the single-step corpus's shape).
	if cycles != 7*4 {
		t.Errorf("MVN cycles = %d, want %d", cycles, 7*4)
	}
}

func TestBlockMoveBackwards(t *testing.T) {
	r := native()
	cpu, bus := newTestCPU(r, 0x44, 0x7E, 0x00) // MVP dst=0x7E src=0x00
	cpu.SetRegs(Registers{A: 1, X: 0x9001, Y: 0x8001, S: 0x01FF, PC: 0x8000, E: false})
	bus.put(0, 0x9001, 0x11)
	bus.put(0, 0x9000, 0x22)
	cpu.Step()
	if got := bus.at(0x7E, 0x8001); got != 0x11 {
		t.Errorf("dest[1] = %#x, want 0x11", got)
	}
	if got := bus.at(0x7E, 0x8000); got != 0x22 {
		t.Errorf("dest[0] = %#x, want 0x22", got)
	}
	if cpu.Regs().X != 0x8FFF {
		t.Errorf("X = %#x, want 0x8FFF (counted down past start)", cpu.Regs().X)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	r := native()
	r.A, r.X, r.D, r.DBR = 0x1234, 0x5678, 0x9ABC, 0x42
	cpu, _ := newTestCPU(r, 0xEA)
	cpu.Step()
	state := cpu.Serialize()
	cpu2, _ := newTestCPU(emu())
	if err := cpu2.Deserialize(state); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if cpu2.Regs() != cpu.Regs() {
		t.Errorf("regs differ after round trip: %+v vs %+v", cpu2.Regs(), cpu.Regs())
	}
	if cpu2.Cycles() != cpu.Cycles() || cpu2.MasterCycles() != cpu.MasterCycles() {
		t.Errorf("cycle counters differ after round trip")
	}
	if err := cpu2.Deserialize([]byte{1, 2, 3}); err == nil {
		t.Errorf("short state accepted")
	}
}

func TestResetEntersEmulationAtVector(t *testing.T) {
	bus := newRAMBus()
	bus.put(0, 0xFFFC, 0x00)
	bus.put(0, 0xFFFD, 0x80)
	cpu := New()
	cpu.Attach(bus)
	cpu.Reset()
	got := cpu.Regs()
	if !got.E || got.PC != 0x8000 || got.S != 0x01FF {
		t.Errorf("after reset: %+v", got)
	}
	if cpu.Cycles() != 7 {
		t.Errorf("reset cycles = %d, want 7", cpu.Cycles())
	}
}

func TestEmulationStackConfinedToPage1(t *testing.T) {
	r := emu()
	r.S = 0x3456                    // stored high byte is junk; addressing must confine
	cpu, bus := newTestCPU(r, 0x48) // PHA
	cpu.Step()
	if got := bus.at(0, 0x0156); got != uint8(cpu.Regs().A) {
		t.Errorf("PHA wrote %#x at 0x0156", got)
	}
	if cpu.Regs().S != 0x0155 {
		t.Errorf("S = %#x, want 0x0155", cpu.Regs().S)
	}
}

func TestWAISleepsUntilInterrupt(t *testing.T) {
	r := native()
	r.P = FlagI                         // IRQ masked: wake but do not take
	cpu, _ := newTestCPU(r, 0xCB, 0xEA) // WAI; NOP
	cpu.Step()
	if !cpu.Waiting() {
		t.Fatalf("WAI did not stop the CPU")
	}
	cpu.Step()
	cpu.Step()
	if !cpu.Waiting() {
		t.Fatalf("WAI woke without an interrupt input")
	}
	cpu.SetIRQLine(true)
	cpu.Step() // the wake cycle
	if cpu.Waiting() {
		t.Fatalf("WAI did not wake on IRQ line")
	}
	cpu.Step() // masked by I: executes the NOP, no interrupt taken
	if cpu.Regs().PC != 0x8002 {
		t.Errorf("PC = %#x, want 0x8002 (resumed past NOP)", cpu.Regs().PC)
	}
}

func TestDecimalADC(t *testing.T) {
	r := native()
	r.P = FlagD | FlagM // decimal, 8-bit accumulator
	r.A = 0x0045
	cpu, _ := newTestCPU(r, 0x69, 0x55) // ADC #$55: 45 + 55 = 00 carry 1 in BCD
	cpu.Step()
	got := cpu.Regs()
	if got.A&0xFF != 0x00 || got.P&FlagC == 0 {
		t.Errorf("BCD 45+55: A = %#x, P = %08b, want 0x00 with carry", got.A, got.P)
	}
	r2 := native()
	r2.P = FlagD | FlagM
	r2.A = 0x0009
	cpu2, _ := newTestCPU(r2, 0x69, 0x01) // ADC #$01: 09 + 01 = 10
	cpu2.Step()
	if cpu2.Regs().A&0xFF != 0x10 {
		t.Errorf("BCD 09+01: A = %#x, want 0x10", cpu2.Regs().A)
	}
}
