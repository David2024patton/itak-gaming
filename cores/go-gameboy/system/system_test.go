package system

import (
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-gameboy/cart"
)

// makeImage builds a minimal flat 32 KB ROM with the given program at
// the 0x0100 entry point and a valid header.
func makeImage(t *testing.T, prog ...uint8) []byte {
	t.Helper()
	data := make([]byte, 32768)
	copy(data[0x100:], prog)
	data[0x147] = 0x00 // ROM ONLY
	data[0x148] = 0x00
	data[0x149] = 0x00
	// Header checksum at 0x14D over 0x134..0x14C.
	var x uint8
	for i := 0x134; i <= 0x14C; i++ {
		x = x - data[i] - 1
	}
	data[0x14D] = x
	return data
}

func TestBootState(t *testing.T) {
	s, err := New(makeImage(t, 0x00))
	if err != nil {
		t.Fatal(err)
	}
	r := s.CPU.Regs()
	if r.PC != 0x0100 || r.SP != 0xFFFE || r.A != 0x01 || r.F != 0xB0 {
		t.Fatalf("boot regs = %+v, want PC=0x0100 SP=0xFFFE A=0x01 F=0xB0", r)
	}
	if s.Cart.Kind != cart.MBC0 {
		t.Fatalf("kind = %v, want MBC0", s.Cart.Kind)
	}
}

func TestSerialCapture(t *testing.T) {
	// Print the letter H, then idle forever.
	prog := []uint8{
		0x3E, 'H', // LD A, 'H'
		0xE0, 0x01, // LDH (SB), A
		0x3E, 0x81, // LD A, 0x81
		0xE0, 0x02, // LDH (SC), A
		0x18, 0xFE, // JR -2
	}
	s, err := New(makeImage(t, prog...))
	if err != nil {
		t.Fatal(err)
	}
	s.RunMCycles(1100) // an internal-clock transfer takes 4096 dots
	if s.Serial() != "H" {
		t.Fatalf("Serial = %q, want %q", s.Serial(), "H")
	}
}

func TestTimerInterrupt(t *testing.T) {
	s, err := New(makeImage(t, 0x00)) // NOP sled
	if err != nil {
		t.Fatal(err)
	}
	s.Write(regTMA, 0x42)
	s.Write(regTIMA, 0xFF)
	s.Write(regTAC, 0x05) // enable, 16 T-cycle period
	s.RunMCycles(4)       // 16 T-cycles: the tick that overflows
	if got := s.Read(regTIMA); got != 0x00 {
		t.Fatalf("TIMA in the reload window = 0x%02X, want 0x00 (reload is delayed one M-cycle)", got)
	}
	s.RunMCycles(1) // the reload lands one M-cycle after the overflow
	if got := s.Read(regTIMA); got != 0x42 {
		t.Fatalf("TIMA after reload = 0x%02X, want reload value 0x42", got)
	}
	if s.Read(regIF)&0x04 == 0 {
		t.Fatalf("timer interrupt not requested in IF")
	}
}

func TestDivider(t *testing.T) {
	s, err := New(makeImage(t, 0x00))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Read(regDIV); got != 0xAB {
		t.Fatalf("DIV at boot = 0x%02X, want 0xAB (DMG hand-off value)", got)
	}
	s.RunMCycles(64) // 256 T-cycles: DIV ticks once
	if got := s.Read(regDIV); got != 0xAC {
		t.Fatalf("DIV after 256 T-cycles = 0x%02X, want 0xAC", got)
	}
	s.Write(regDIV, 0)
	if got := s.Read(regDIV); got != 0 {
		t.Fatalf("DIV after write = 0x%02X, want 0", got)
	}
}
