package system

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Synthetic ROM.

// armImm builds a data processing instruction with an
// immediate operand from its fields: MOV is opcode 13,
// ORR is opcode 12. The operand is imm8 rotated right by
// rot*2 bits, the ARM modified immediate encoding.
func armImm(opcode, rn, rd, rot, imm8 uint32) uint32 {
	return 0xE0000000 | 1<<25 | opcode<<21 | rn<<16 | rd<<12 | rot<<8 | imm8
}

// armBuildConst emits the instructions that load a
// 32-bit constant into register rd: a MOV of the low
// byte followed by one ORR per non-zero higher byte,
// each byte rotated into its position.
func armBuildConst(rd, v uint32) []uint32 {
	out := []uint32{armImm(13, 0, rd, 0, v&0xFF)} // MOV rd, #low byte
	rots := []uint32{0, 12, 8, 4}                 // ROR 0, 24, 16, 8 by byte position
	for i := 1; i < 4; i++ {
		b := (v >> (8 * i)) & 0xFF
		if b != 0 {
			out = append(out, armImm(12, rd, rd, rots[i], b)) // ORR rd, rd, #byte in place
		}
	}
	return out
}

// armSTR builds STR rd, [rn] (byte=false) or STRB rd,
// [rn] (byte=true) with a zero immediate offset.
func armSTR(rd, rn uint32, byteOp bool) uint32 {
	w := uint32(0xE5800000) | rn<<16 | rd<<12
	if byteOp {
		w |= 1 << 22
	}
	return w
}

// armBranch builds a branch at code index from to code
// index to, with the given condition field (0xE is
// always, 0x1 is not equal). The offset counts in words
// from the address of the branch plus 8, like hardware.
func armBranch(cond uint32, from, to int) uint32 {
	return cond<<28 | 0x0A000000 | uint32((to-(from+2))&0xFFFFFF)
}

// syntheticROM builds a 4 KB image behind a valid header.
// The entry word at 0x00 is an ARM branch to the code at
// 0xC0: the branch offset is (0xC0 - 8) / 4 = 0x2E words,
// since the program counter reads as the address of the
// branch plus 8, so the word is 0xEA00002E. The code at
// 0xC0 first fills 64 EWRAM words with 0x11223344 + i,
// then loops forever: it sums the 64 words into a
// checksum, stores the checksum at IWRAM+0, the running
// iteration count at IWRAM+4, and the low checksum byte
// at SRAM+0. Because the loop never ends, a save state
// taken mid-run captures live progress, and continuing
// from it must reproduce the uninterrupted run exactly.
func syntheticROM() []byte {
	rom := make([]byte, 0x1000)
	binary.LittleEndian.PutUint32(rom[0x00:], 0xEA00002E)
	copy(rom[0xA0:], "SYNTHTEST")
	copy(rom[0xAC:], "ASYE")
	copy(rom[0xB0:], "01")
	rom[0xB2] = 0x96
	rom[0xBC] = 0x01
	sum := 0x19
	for _, c := range rom[0xA0 : 0xBC+1] {
		sum += int(c)
	}
	rom[0xBD] = uint8(-sum)

	var code []uint32
	code = append(code, armBuildConst(0, 0x02000000)...) // r0 = EWRAM base
	code = append(code, armBuildConst(1, 0x11223344)...) // r1 = fill pattern
	code = append(code, armBuildConst(4, 0x03000000)...) // r4 = IWRAM base
	code = append(code, armBuildConst(7, 0x0E000000)...) // r7 = SRAM base
	code = append(code, armImm(13, 0, 2, 0, 0))          // MOV r2, #0
	fill := len(code)
	code = append(code,
		0xE7801102,             // STR r1, [r0, r2, LSL #2]
		armImm(4, 1, 1, 0, 1),  // ADD r1, r1, #1
		armImm(4, 2, 2, 0, 1),  // ADD r2, r2, #1
		0xE3520040,             // CMP r2, #64
		0,                      // BNE fill (patched below)
		armImm(13, 0, 6, 0, 0), // MOV r6, #0 (iteration count)
	)
	outer := len(code)
	code = append(code,
		armImm(13, 0, 5, 0, 0), // MOV r5, #0 (checksum)
		armImm(13, 0, 2, 0, 0), // MOV r2, #0
	)
	sumLoop := len(code)
	code = append(code,
		0xE7903102,            // LDR r3, [r0, r2, LSL #2]
		0xE0855003,            // ADD r5, r5, r3
		armImm(4, 2, 2, 0, 1), // ADD r2, r2, #1
		0xE3520040,            // CMP r2, #64
		0,                     // BNE sumLoop (patched below)
		armSTR(5, 4, false),   // STR r5, [r4]
		0xE5846004,            // STR r6, [r4, #4]
		armSTR(5, 7, true),    // STRB r5, [r7]
		armImm(4, 6, 6, 0, 1), // ADD r6, r6, #1
		0,                     // B outer (patched below)
	)
	code[fill+4] = armBranch(1, fill+4, fill)
	code[sumLoop+4] = armBranch(1, sumLoop+4, sumLoop)
	last := len(code) - 1
	code[last] = armBranch(0xE, last, outer)
	for i, w := range code {
		binary.LittleEndian.PutUint32(rom[0xC0+4*i:], w)
	}
	return rom
}

// wantChecksum is the sum the synthetic program
// accumulates over its 64 filled EWRAM words.
func wantChecksum() uint32 {
	var s uint32
	for i := uint32(0); i < 64; i++ {
		s += 0x11223344 + i
	}
	return s
}

// assertProgramEffects checks the results the synthetic
// program produces: the filled EWRAM table, the checksum
// and a positive iteration count in IWRAM, and the low
// checksum byte in SRAM.
func assertProgramEffects(t *testing.T, m *Machine) {
	t.Helper()
	if got := m.Bus().Read(0x02000000, 4); got != 0x11223344 {
		t.Errorf("EWRAM word 0 = %#08x, want 0x11223344", got)
	}
	if got := m.Bus().Read(0x02000000+63*4, 4); got != 0x11223344+63 {
		t.Errorf("EWRAM word 63 = %#08x, want %#08x", got, 0x11223344+63)
	}
	if got := m.Bus().Read(0x03000000, 4); got != wantChecksum() {
		t.Errorf("IWRAM checksum = %#08x, want %#08x", got, wantChecksum())
	}
	if got := m.Bus().Read(0x03000004, 4); got == 0 {
		t.Error("IWRAM iteration count is 0, the checksum loop never stored")
	}
	if got := m.Bus().Read(0x0E000000, 1); got != uint32(byte(wantChecksum())) {
		t.Errorf("SRAM byte = %#02x, want %#02x", got, byte(wantChecksum()))
	}
}

// Tests.

func TestLoadCartAndFramebuffer(t *testing.T) {
	m := New()
	if err := m.LoadCart(syntheticROM()); err != nil {
		t.Fatalf("LoadCart: %v", err)
	}
	if m.Title() != "SYNTHTEST" {
		t.Errorf("Title = %q, want SYNTHTEST", m.Title())
	}
	if m.Cart() == nil || m.Cart().GameCode != "ASYE" {
		t.Errorf("Cart header not wired: %+v", m.Cart())
	}
	if Width != 240 || Height != 160 {
		t.Errorf("dimensions = %dx%d, want 240x160", Width, Height)
	}
	fb := m.Framebuffer()
	if len(fb) != Width*Height {
		t.Fatalf("framebuffer has %d pixels, want %d", len(fb), Width*Height)
	}
	for i, p := range fb {
		if p != 0xFF000000 {
			t.Fatalf("framebuffer[%d] = %#08x, want 0xFF000000 (blank)", i, p)
		}
	}
	// RunFrame must return and leave the blank frame.
	m.RunFrame()
	if len(m.Framebuffer()) != Width*Height {
		t.Fatal("framebuffer changed size after RunFrame")
	}
}

func TestLoadCartBadImage(t *testing.T) {
	m := New()
	bad := syntheticROM()
	bad[0xB2] = 0x00
	if err := m.LoadCart(bad); err == nil {
		t.Fatal("LoadCart accepted a bad fixed byte")
	}
	if m.Cart() != nil {
		t.Fatal("failed LoadCart installed a cartridge")
	}
}

func TestButtons(t *testing.T) {
	m := New()
	m.SetButtons(ButtonA | ButtonStart | ButtonL)
	if got := m.Buttons(); got != ButtonA|ButtonStart|ButtonL {
		t.Fatalf("Buttons = %#04x, want %#04x", got, uint32(ButtonA|ButtonStart|ButtonL))
	}
	// Bit order spot checks against GBATEK KEYINPUT.
	if ButtonA != 1<<0 || ButtonB != 1<<1 || ButtonSelect != 1<<2 || ButtonStart != 1<<3 ||
		ButtonRight != 1<<4 || ButtonLeft != 1<<5 || ButtonUp != 1<<6 || ButtonDown != 1<<7 ||
		ButtonR != 1<<8 || ButtonL != 1<<9 {
		t.Fatal("button constants are not in GBATEK KEYINPUT bit order")
	}
	m.SetButtons(0xFFFF)
	if got := m.Buttons(); got != 0x3FF {
		t.Fatalf("Buttons after wide mask = %#04x, want 0x3FF", got)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	rom := syntheticROM()
	m := New()
	if err := m.LoadCart(rom); err != nil {
		t.Fatalf("LoadCart: %v", err)
	}
	m.RunFrame()
	// Scribble every serialized store with known values.
	m.Bus().Write(0x02000000, 4, 0x11223344)
	m.Bus().Write(0x03000000, 4, 0x55667788)
	m.Bus().Write(0x0E000000, 1, 0xAA)
	m.Bus().Write(0x05000000, 2, 0x7FFF)
	m.Bus().Write(0x06000000, 4, 0xCAFEBABE)
	m.Bus().Write(0x07000000, 4, 0x0BADF00D)
	m.Bus().Write(0x04000204, 2, 0x4317)

	s1 := m.Serialize()
	if len(s1) != m.StateSize() {
		t.Fatalf("Serialize produced %d bytes, StateSize says %d", len(s1), m.StateSize())
	}
	if s1[0] != 4 {
		t.Fatalf("version byte = %d, want 4", s1[0])
	}

	// Deserialize must not depend on cart size or history:
	// a fresh machine with the same cart restores it and
	// serializes back to identical bytes.
	m2 := New()
	if err := m2.LoadCart(rom); err != nil {
		t.Fatalf("LoadCart (second machine): %v", err)
	}
	if m2.StateSize() != m.StateSize() {
		t.Fatalf("StateSize differs between machines: %d vs %d", m2.StateSize(), m.StateSize())
	}
	if err := m2.Deserialize(s1); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if s2 := m2.Serialize(); !bytes.Equal(s1, s2) {
		t.Fatal("serialize after deserialize differs from the original state")
	}
	// Restoring into the same machine is also a no-op.
	if err := m.Deserialize(s1); err != nil {
		t.Fatalf("Deserialize (same machine): %v", err)
	}
	if s3 := m.Serialize(); !bytes.Equal(s1, s3) {
		t.Fatal("same machine round trip differs")
	}
}

func TestDeserializeRejectsBadInput(t *testing.T) {
	m := New()
	if err := m.LoadCart(syntheticROM()); err != nil {
		t.Fatalf("LoadCart: %v", err)
	}
	good := m.Serialize()
	if err := m.Deserialize(good[:len(good)-1]); err == nil {
		t.Error("Deserialize accepted a short state")
	}
	if err := m.Deserialize(append(append([]byte{}, good...), 0)); err == nil {
		t.Error("Deserialize accepted an oversized state")
	}
	badVersion := append([]byte{}, good...)
	badVersion[0] = 99
	if err := m.Deserialize(badVersion); err == nil {
		t.Error("Deserialize accepted version 99")
	}
}

// primeEntry loads the CPU pipeline at the cartridge
// entry point 0x08000000. A powered-on GBA starts in the
// BIOS, which is what normally jumps there; this core
// ships no BIOS (and BIOS SWI HLE is a later slice), so
// the tests perform that jump themselves, in exactly the
// state the CPU's own Reset refill produces for the reset
// vector.
func primeEntry(m *Machine) {
	c := m.CPU()
	const entry = 0x08000000
	c.Executing = m.Bus().Fetch(entry, 4)
	c.Pipe[0] = m.Bus().Fetch(entry+4, 4)
	c.Pipe[1] = m.Bus().Fetch(entry+8, 4)
	c.R[15] = entry + 8
	c.ExecutingValid = true
}

// TestSyntheticROMExecution runs the hand-assembled
// program through the machine and checks the filled
// table, the checksum loop results in IWRAM, and the
// low byte in SRAM.
func TestSyntheticROMExecution(t *testing.T) {
	m := New()
	if err := m.LoadCart(syntheticROM()); err != nil {
		t.Fatalf("LoadCart: %v", err)
	}
	primeEntry(m)
	m.RunFrame()
	assertProgramEffects(t, m)
}

// TestSaveStateContinuation proves the deterministic
// round trip under execution: run the checksum loop,
// save mid-run, run further, and require that a machine
// restored from the mid-run save reaches byte-identical
// state after the same additional frames.
func TestSaveStateContinuation(t *testing.T) {
	rom := syntheticROM()
	m := New()
	if err := m.LoadCart(rom); err != nil {
		t.Fatalf("LoadCart: %v", err)
	}
	primeEntry(m)
	m.RunFrame()
	m.RunFrame()
	s1 := m.Serialize()
	m.RunFrame()
	m.RunFrame()
	want := m.Serialize()
	assertProgramEffects(t, m)

	m2 := New()
	if err := m2.LoadCart(rom); err != nil {
		t.Fatalf("LoadCart (second machine): %v", err)
	}
	if err := m2.Deserialize(s1); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	m2.RunFrame()
	m2.RunFrame()
	if got := m2.Serialize(); !bytes.Equal(got, want) {
		t.Fatal("continuation after a mid-run restore differs from the uninterrupted run")
	}
	assertProgramEffects(t, m2)
}
