package arm7

import "testing"

// ram is a flat byte addressed memory for unit tests.
type ram struct {
	mem []byte
}

func newRAM(size int) *ram { return &ram{mem: make([]byte, size)} }

func (r *ram) Read8(addr uint32) uint8 { return r.mem[addr] }
func (r *ram) Read16(addr uint32) uint16 {
	return uint16(r.mem[addr]) | uint16(r.mem[addr+1])<<8
}
func (r *ram) Read32(addr uint32) uint32 {
	return uint32(r.mem[addr]) | uint32(r.mem[addr+1])<<8 |
		uint32(r.mem[addr+2])<<16 | uint32(r.mem[addr+3])<<24
}
func (r *ram) Write8(addr uint32, v uint8) { r.mem[addr] = v }
func (r *ram) Write16(addr uint32, v uint16) {
	r.mem[addr] = uint8(v)
	r.mem[addr+1] = uint8(v >> 8)
}
func (r *ram) Write32(addr uint32, v uint32) {
	r.mem[addr] = uint8(v)
	r.mem[addr+1] = uint8(v >> 8)
	r.mem[addr+2] = uint8(v >> 16)
	r.mem[addr+3] = uint8(v >> 24)
}

// putARM writes ARM opcodes into memory at consecutive addresses.
func putARM(r *ram, addr uint32, ops ...uint32) {
	for i, op := range ops {
		r.Write32(addr+uint32(4*i), op)
	}
}

// putThumb writes Thumb opcodes into memory at consecutive
// addresses.
func putThumb(r *ram, addr uint32, ops ...uint16) {
	for i, op := range ops {
		r.Write16(addr+uint32(2*i), op)
	}
}

func TestResetState(t *testing.T) {
	c := New(newRAM(0x100))
	if c.Mode() != ModeSVC {
		t.Errorf("mode = %x, want SVC", c.Mode())
	}
	if c.CPSR&cpsrI == 0 || c.CPSR&cpsrF == 0 {
		t.Errorf("CPSR %08x: IRQ and FIQ should be masked", c.CPSR)
	}
	if c.Thumb() {
		t.Error("should reset into ARM state")
	}
	if c.R[15] != 8 {
		t.Errorf("R15 = %x, want 8", c.R[15])
	}
}

// TestModeBanking checks that entering FIQ mode banks R8 to R14
// and that returning restores the shared registers.
func TestModeBanking(t *testing.T) {
	c := New(newRAM(0x100))
	// MSR CPSR_c, R0 with a plain mode value; build the state by
	// hand instead: start in SVC, plant markers, switch to FIQ via
	// setCPSR, then back.
	c.R[8] = 0xA8
	c.R[13] = 0xA13
	c.R[14] = 0xA14
	c.CPSR = ModeSVC | cpsrI | cpsrF
	c.setCPSR(ModeFIQ | cpsrI | cpsrF)
	if c.R[8] != 0 || c.R[13] != 0 || c.R[14] != 0 {
		t.Errorf("FIQ view not zero bank: R8=%x R13=%x R14=%x", c.R[8], c.R[13], c.R[14])
	}
	c.R[8] = 0xF8
	c.R[13] = 0xF13
	c.R[14] = 0xF14
	c.setCPSR(ModeSVC | cpsrI | cpsrF)
	if c.R[8] != 0xA8 || c.R[13] != 0xA13 || c.R[14] != 0xA14 {
		t.Errorf("SVC view not restored: R8=%x R13=%x R14=%x", c.R[8], c.R[13], c.R[14])
	}
	// The FIQ values must be in the FIQ bank storage now.
	if c.RFIQ[0] != 0xF8 || c.RFIQ[5] != 0xF13 || c.RFIQ[6] != 0xF14 {
		t.Errorf("FIQ bank wrong: %v", c.RFIQ)
	}
	if c.RSVC[0] != 0xA13 || c.RSVC[1] != 0xA14 {
		t.Errorf("SVC bank wrong: %v", c.RSVC)
	}
	// And the user shadow of R8 to R12 holds the shared values.
	c.setCPSR(ModeFIQ | cpsrI | cpsrF)
	if c.usr8_12[0] != 0xA8 {
		t.Errorf("usr8_12[0] = %x, want a8", c.usr8_12[0])
	}
}

// TestSWIAndReturn runs a tiny ARM program: a SWI in user mode
// whose handler returns with MOVS PC, LR.
func TestSWIAndReturn(t *testing.T) {
	r := newRAM(0x1000)
	// At 0x100: user code: MOV R0, #0x11 ; SWI 0 ; MOV R1, #7
	putARM(r, 0x100, 0xE3A00011, 0xEF000000, 0xE3A01007)
	// At 0x08: handler: MOV R2, #0x42 ; MOVS PC, LR
	putARM(r, 0x08, 0xE3A02042, 0xE1B0F00E)
	c := New(r)
	c.setCPSR(ModeUSR)
	c.flush(0x100)
	c.Step() // MOV R0
	c.Step() // SWI
	if c.Mode() != ModeSVC {
		t.Fatalf("after SWI mode = %x, want SVC", c.Mode())
	}
	if c.R[14] != 0x108 {
		t.Errorf("LR_svc = %x, want 108 (next instruction)", c.R[14])
	}
	if c.SPSR[spsrSVC]&cpsrMode != ModeUSR {
		t.Errorf("SPSR_svc mode = %x, want USR", c.SPSR[spsrSVC]&cpsrMode)
	}
	if c.CPSR&cpsrI == 0 {
		t.Error("SWI must mask IRQ")
	}
	c.Step() // MOV R2 in handler
	if c.R[2] != 0x42 {
		t.Errorf("R2 = %x, want 42", c.R[2])
	}
	c.Step() // MOVS PC, LR: back to user mode at 0x108
	if c.Mode() != ModeUSR {
		t.Errorf("after return mode = %x, want USR", c.Mode())
	}
	c.Step() // MOV R1
	if c.R[1] != 7 {
		t.Errorf("R1 = %x, want 7", c.R[1])
	}
	if c.R[0] != 0x11 {
		t.Errorf("R0 = %x, want 11", c.R[0])
	}
}

// TestIRQEntry checks IRQ delivery, masking, and return.
func TestIRQEntry(t *testing.T) {
	r := newRAM(0x1000)
	// IRQ handler at 0x18: MOV R3, #9 ; SUBS PC, LR, #4
	putARM(r, 0x18, 0xE3A03009, 0xE25EF004)
	// User code at 0x200: MOV R0, #1 ; MOV R0, #2
	putARM(r, 0x200, 0xE3A00001, 0xE3A00002)
	c := New(r)
	c.setCPSR(ModeUSR) // IRQ unmasked (I clear)
	c.flush(0x200)
	c.SetIRQ(true)
	c.Step() // interrupt taken instead of the MOV
	if c.Mode() != ModeIRQ {
		t.Fatalf("mode = %x, want IRQ", c.Mode())
	}
	if c.R[14] != 0x204 {
		t.Errorf("LR_irq = %x, want 204 (interrupted address + 4)", c.R[14])
	}
	if c.SPSR[spsrIRQ]&cpsrMode != ModeUSR {
		t.Errorf("SPSR_irq mode wrong: %x", c.SPSR[spsrIRQ])
	}
	c.SetIRQ(false)
	c.Step() // handler MOV R3
	c.Step() // SUBS PC, LR, #4 returns to 0x200 in USR mode
	if c.Mode() != ModeUSR {
		t.Fatalf("after IRQ return mode = %x, want USR", c.Mode())
	}
	if c.R[3] != 9 {
		t.Errorf("R3 = %x, want 9", c.R[3])
	}
	c.Step() // the interrupted MOV now executes
	if c.R[0] != 1 {
		t.Errorf("R0 = %x, want 1", c.R[0])
	}
	// With I set, a raised IRQ must not be taken.
	c.setCPSR(ModeUSR | cpsrI)
	c.SetIRQ(true)
	c.Step()
	if c.Mode() != ModeUSR {
		t.Error("masked IRQ was taken")
	}
	if c.R[0] != 2 {
		t.Errorf("R0 = %x, want 2 (masked step executed an instruction)", c.R[0])
	}
}

// TestUndefinedException checks that a coprocessor instruction
// raises the undefined exception and returns cleanly.
func TestUndefinedException(t *testing.T) {
	r := newRAM(0x1000)
	// At 0x300: MRC (coprocessor space) ; MOV R5, #3
	putARM(r, 0x300, 0xEE100F10, 0xE3A05003)
	// Handler at 0x04: MOVS PC, LR (skip the bad instruction)
	putARM(r, 0x04, 0xE1B0F00E)
	c := New(r)
	c.setCPSR(ModeUSR)
	c.flush(0x300)
	c.Step()
	if c.Mode() != ModeUND {
		t.Fatalf("mode = %x, want UND", c.Mode())
	}
	if c.R[14] != 0x304 {
		t.Errorf("LR_und = %x, want 304", c.R[14])
	}
	c.Step() // return to 0x304
	if c.Mode() != ModeUSR {
		t.Fatalf("mode after return = %x", c.Mode())
	}
	c.Step()
	if c.R[5] != 3 {
		t.Errorf("R5 = %x, want 3", c.R[5])
	}
}

// TestThumbBasics exercises a short Thumb sequence: arithmetic,
// a conditional branch, and a subroutine call and return.
func TestThumbBasics(t *testing.T) {
	r := newRAM(0x1000)
	// At 0x400 (Thumb):
	//   MOV R0, #10      0x2010? no: MOV imm is 0x2000|rd<<8|imm
	//   ADD R0, #5       0x3005 with rd=0: 0x3005
	//   CMP R0, #15      0x2D0F
	//   BNE +skip        (not taken)
	//   BL sub           prefix/suffix
	//   MOV R1, R0       hi reg form is 0x4681? use ADD R1, R0, #0 form3: 0x1C09
	// sub at known address returns with BX LR (0x4770)
	putThumb(r, 0x400,
		0x200A, // MOV R0, #10
		0x3005, // ADD R0, #5
		0x280F, // CMP R0, #15
		0xD101, // BNE +2 (not taken: Z set)
		0xF000, // BL prefix: LR = 0x40C
		0xF802, // BL suffix: target = 0x40C + 2*2 = 0x410
		0x1C01, // ADD R1, R0, #0
	)
	// Subroutine at 0x410: ADD R0, #1 (0x3001); BX LR (0x4770)
	putThumb(r, 0x410, 0x3001, 0x4770)
	c := New(r)
	c.setCPSR(ModeUSR | cpsrT)
	c.flush(0x400)
	c.Step() // MOV
	c.Step() // ADD -> R0 = 15
	c.Step() // CMP -> Z set
	if !c.flagZ() {
		t.Fatal("CMP should set Z")
	}
	c.Step() // BNE not taken
	c.Step() // BL prefix
	c.Step() // BL suffix -> 0x410, LR = 0x40C | 1
	if c.R[14] != 0x40D {
		t.Errorf("LR = %x, want 40d", c.R[14])
	}
	c.Step() // ADD R0 -> 16
	c.Step() // BX LR -> back to 0x40C
	c.Step() // ADD R1, R0, #0
	if c.R[0] != 16 || c.R[1] != 16 {
		t.Errorf("R0=%x R1=%x, want 16 and 16", c.R[0], c.R[1])
	}
}

// TestSerializeRoundTrip checks that a mid program state survives
// a serialize and deserialize cycle byte for byte in behaviour.
func TestSerializeRoundTrip(t *testing.T) {
	r := newRAM(0x1000)
	putARM(r, 0x100, 0xE3A00005, 0xE2801003, 0xE2511020)
	c := New(r)
	c.setCPSR(ModeUSR)
	c.flush(0x100)
	c.Step()
	c.Step()
	buf := make([]byte, SerializeSize)
	if n := c.Serialize(buf); n != SerializeSize {
		t.Fatalf("Serialize returned %d", n)
	}
	saved := make([]byte, SerializeSize)
	copy(saved, buf)
	c.Step() // disturb the state
	d := &CPU{Bus: r}
	if !d.Deserialize(saved) {
		t.Fatal("Deserialize failed")
	}
	if d.R != c.R && false {
		t.Fatal("unreachable")
	}
	// The restored CPU must match the saved snapshot, not the
	// disturbed one.
	if d.R[0] != 5 || d.R[1] != 8 {
		t.Errorf("restored R0=%x R1=%x, want 5 and 8", d.R[0], d.R[1])
	}
	if d.CPSR != c.CPSR {
		// CPSR was not changed by the third instruction beyond
		// flags; compare the snapshot byte image instead.
		buf2 := make([]byte, SerializeSize)
		d.Serialize(buf2)
		for i := range saved {
			if saved[i] != buf2[i] {
				t.Fatalf("round trip mismatch at byte %d: %x != %x", i, saved[i], buf2[i])
			}
		}
	}
	if d.Deserialize(make([]byte, 4)) {
		t.Error("short buffer should be rejected")
	}
}
