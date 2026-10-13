package system

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// testProgram is a hand assembled SH-4 routine. It loads a data
// pointer and a VRAM pointer from a literal pool, sums 32 bytes
// from the data page into R0 with a counted loop, stores R0 as a
// 32-bit word at the start of VRAM, then spins in place.
//
// Virtual addresses (the machine runs with the MMU off, so a
// virtual address reaches the bus masked to its physical form):
//
//	data page   0x8C010000 (P1 alias of physical 0x0C010000)
//	VRAM store  0xA5000000 (P2 alias of physical 0x05000000)
//
// Instruction map (offsets from the program base):
//
//	0x00  D106  MOV.L @(6,PC),R1   ; R1 = literal at 0x1C
//	0x02  0009  NOP                ; pad so the next MOV.L is 4-aligned
//	0x04  D406  MOV.L @(6,PC),R4   ; R4 = literal at 0x20
//	0x06  E000  MOV #0,R0          ; sum = 0
//	0x08  E220  MOV #32,R2         ; count = 32
//	0x0A  6314  MOV.B @R1+,R3      ; loop: R3 = next byte, sign extended
//	0x0C  303C  ADD R3,R0          ; sum += R3
//	0x0E  4210  DT R2              ; count, T set when it hits 0
//	0x10  8BFB  BF -5              ; back to loop while T is clear
//	0x12  0009  NOP                ; BF delay slot (runs when taken)
//	0x14  2402  MOV.L R0,@R4       ; store the sum to VRAM
//	0x16  AFFE  BRA -2             ; spin here forever
//	0x18  0009  NOP                ; BRA delay slot
//	0x1A  0000                     ; pad so the literals are 4-aligned
//	0x1C  .long 0x8C010000
//	0x20  .long 0xA5000000
//
// The NOP at 0x12 matters: on SH-4 a BF delay slot executes when
// the branch is taken and is skipped when it is not, so any real
// instruction parked there would run once per loop pass and then
// be skipped on the final pass. Both MOV.L instructions sit at
// 4-aligned offsets, so the PC-relative base (this instruction
// plus 4, aligned down to a word) is the same under either
// reading of the manual. The data bytes are all below 128, so
// the sign extension of MOV.B does not change the sum.
var testProgramWords = []uint16{
	0xD106, 0x0009, 0xD406, 0xE000, 0xE220,
	0x6314, 0x303C, 0x4210, 0x8BFB, 0x0009,
	0x2402, 0xAFFE, 0x0009, 0x0000,
	0x0000, 0x8C01, // 0x8C010000, little-endian
	0x0000, 0xA500, // 0xA5000000, little-endian
}

const (
	testDataPhys = 0x0C010000
	testSumWant  = 528 // 1 + 2 + ... + 32
)

func testProgram() []byte {
	out := make([]byte, len(testProgramWords)*2)
	for i, w := range testProgramWords {
		binary.LittleEndian.PutUint16(out[i*2:], w)
	}
	return out
}

// loadTestMachine returns a machine with the test program in main
// RAM at physical 0x0C000000 (entered at 0x8C000000) and the data
// page filled with the bytes 1..32.
func loadTestMachine(t *testing.T) *Machine {
	t.Helper()
	m := New()
	for i := 0; i < 32; i++ {
		m.Bus.Write8(testDataPhys+uint32(i), uint8(i+1))
	}
	m.LoadProgram(0x0C000000, testProgram())
	if m.CPU.PC != 0x8C000000 {
		t.Fatalf("PC after LoadProgram = %#08x, want 0x8C000000", m.CPU.PC)
	}
	return m
}

func stepN(m *Machine, n int) {
	for i := 0; i < n; i++ {
		m.CPU.Step()
	}
}

func TestSyntheticProgramChecksum(t *testing.T) {
	m := loadTestMachine(t)
	stepN(m, 200)
	if got := m.Bus.Read32(0x05000000); got != testSumWant {
		t.Fatalf("VRAM word after 200 steps = %d (%#08x), want %d", got, got, testSumWant)
	}
	// The same word is visible through the other VRAM window.
	if got := m.Bus.Read32(0x04000000); got != testSumWant {
		t.Errorf("VRAM word via 32-bit window = %d, want %d", got, testSumWant)
	}
}

func TestSyntheticProgramDeterminismAcrossRestore(t *testing.T) {
	// Uninterrupted run: 100 steps, snapshot, 100 more.
	m := loadTestMachine(t)
	stepN(m, 100)
	mid := m.Serialize()
	stepN(m, 100)
	wantWord := m.Bus.Read32(0x05000000)
	if wantWord != testSumWant {
		t.Fatalf("uninterrupted VRAM word = %d, want %d", wantWord, testSumWant)
	}
	wantFinal := m.Serialize()

	// Restored run: a fresh machine takes the mid state and runs
	// the same remaining 100 steps.
	m2 := New()
	if !m2.Deserialize(mid) {
		t.Fatal("Deserialize of the mid-run state returned false")
	}
	stepN(m2, 100)
	if got := m2.Bus.Read32(0x05000000); got != wantWord {
		t.Errorf("restored VRAM word = %d, want %d", got, wantWord)
	}
	if got := m2.Serialize(); !bytes.Equal(got, wantFinal) {
		t.Errorf("restored final state differs from the uninterrupted run (%d vs %d bytes)", len(got), len(wantFinal))
	}
}

func TestSerializeRoundTripFreshMachine(t *testing.T) {
	m := New()
	state := m.Serialize()
	if len(state) != m.StateSize() {
		t.Fatalf("len(Serialize) = %d, want StateSize %d", len(state), m.StateSize())
	}
	m2 := New()
	if !m2.Deserialize(state) {
		t.Fatal("Deserialize of a fresh state returned false")
	}
	if got := m2.Serialize(); !bytes.Equal(got, state) {
		t.Error("state after a fresh round trip differs from the original")
	}
	// Bad inputs are rejected.
	if m2.Deserialize(state[:len(state)-1]) {
		t.Error("Deserialize accepted a truncated state")
	}
	bad := append([]byte(nil), state...)
	bad[0] = 0x7F
	if m2.Deserialize(bad) {
		t.Error("Deserialize accepted a state with a wrong version byte")
	}
}

func TestPadStateAndFramebufferShape(t *testing.T) {
	m := New()
	p := PadState{Mask: BtnA | BtnStart | BtnRT, TriggerL: 12, TriggerR: 250, JoyX: 128, JoyY: 30}
	m.SetButtons(p)
	if got := m.Buttons(); got != p {
		t.Errorf("Buttons = %+v, want %+v", got, p)
	}
	fb := m.Framebuffer()
	if len(fb) != Width*Height {
		t.Fatalf("len(Framebuffer) = %d, want %d", len(fb), Width*Height)
	}
	for i, px := range fb {
		if px != 0 {
			t.Fatalf("framebuffer pixel %d = %#08x, want 0 (blank this slice)", i, px)
		}
	}
}

func TestLoadDiscErrors(t *testing.T) {
	m := New()
	missing := filepath.Join(t.TempDir(), "no-such-disc.gdi")
	if err := m.LoadDisc(missing); err == nil {
		t.Errorf("LoadDisc(%q) returned no error, want one", missing)
	}
	if m.Disc != nil {
		t.Error("Disc is set after a failed LoadDisc, want nil")
	}
}

func TestLoadDiscSyntheticGDI(t *testing.T) {
	dir := t.TempDir()
	trackPath := filepath.Join(dir, "track01.bin")
	if err := os.WriteFile(trackPath, make([]byte, 2352*16), 0o644); err != nil {
		t.Fatal(err)
	}
	gdi := "1\n1 0 4 2352 \"track01.bin\" 0\n"
	gdiPath := filepath.Join(dir, "game.gdi")
	if err := os.WriteFile(gdiPath, []byte(gdi), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.LoadDisc(gdiPath); err != nil {
		t.Fatalf("LoadDisc(synthetic .gdi) error: %v", err)
	}
	if m.Disc == nil {
		t.Fatal("Disc is nil after a successful LoadDisc")
	}
	if got := m.DiscPath(); got != gdiPath {
		t.Errorf("DiscPath = %q, want %q", got, gdiPath)
	}
	_ = m.Disc.Close()
}
