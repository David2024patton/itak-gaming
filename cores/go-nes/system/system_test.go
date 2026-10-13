package system

import "testing"

// nromImage builds a small NROM image (mapper 0, 16 KB PRG,
// 8 KB CHR) with the given bytes at the start of PRG, optionally
// with the battery flag so the cart carries PRG RAM.
func nromImage(battery bool, prog ...uint8) []byte {
	var flags byte
	if battery {
		flags = 0x02
	}
	img := []byte{'N', 'E', 'S', 0x1A, 1, 1, flags, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	prg := make([]byte, 16384)
	copy(prg, prog)
	// Reset vector to $8000.
	prg[0x3FFC] = 0x00
	prg[0x3FFD] = 0x80
	img = append(img, prg...)
	img = append(img, make([]byte, 8192)...)
	return img
}

// jmpSelf is a one-instruction program: JMP $8000 forever.
var jmpSelf = []uint8{0x4C, 0x00, 0x80}

func TestRAMMirrors(t *testing.T) {
	s, err := New(nromImage(false, jmpSelf...))
	if err != nil {
		t.Fatal(err)
	}
	s.Write(0x0001, 0xAB)
	for _, addr := range []uint16{0x0001, 0x0801, 0x1001, 0x1801} {
		if got := s.Read(addr); got != 0xAB {
			t.Fatalf("RAM mirror at %04X = %02X, want AB", addr, got)
		}
	}
}

func TestVblankStubCadenceAndReadClear(t *testing.T) {
	s, err := New(nromImage(false, jmpSelf...))
	if err != nil {
		t.Fatal(err)
	}
	// Run until the stub raises the vblank flag. A frame is
	// 29781 CPU cycles; two frames of budget is plenty.
	seen := false
	for i := 0; i < 60000 && !seen; i++ {
		s.Step()
		if s.Peek(0x2002)&0x80 != 0 {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("vblank flag never rose")
	}
	// A read reports it once and clears it; a peek after the
	// read must agree it is gone for this frame window.
	if got := s.Read(0x2002); got&0x80 == 0 {
		t.Fatalf("first $2002 read missed the flag")
	}
	if got := s.Read(0x2002); got&0x80 != 0 {
		t.Fatalf("second $2002 read should see the flag cleared")
	}
}

func TestNMIOnVblankWhenEnabled(t *testing.T) {
	s, err := New(nromImage(false, jmpSelf...))
	if err != nil {
		t.Fatal(err)
	}
	s.Write(0x2000, 0x80) // NMI enable
	// The CPU must latch an NMI within a frame.
	for i := 0; i < 40000; i++ {
		s.Step()
		if s.CPU().NMIPending() {
			return
		}
		// JMP self keeps executing; the latch is consumed by the
		// very next Step, so observe it right after the poll by
		// checking whether an interrupt sequence ran: the stack
		// pointer drops by three when one does.
		if s.CPU().Regs().SP != 0xFD {
			return
		}
	}
	t.Fatalf("no NMI was taken within a frame despite enable")
}

func TestControllerShift(t *testing.T) {
	s, err := New(nromImage(false, jmpSelf...))
	if err != nil {
		t.Fatal(err)
	}
	s.SetButtons(0, 0x81) // A (bit 0) and Right (bit 7)
	s.Write(0x4016, 1)
	s.Write(0x4016, 0)
	want := []uint8{1, 0, 0, 0, 0, 0, 0, 1, 1, 1}
	for i, w := range want {
		if got := s.Read(0x4016); got != w {
			t.Fatalf("controller read %d = %d, want %d", i, got, w)
		}
	}
}

func TestOAMDMAStallHook(t *testing.T) {
	s, err := New(nromImage(false, 0xEA)) // NOP at $8000
	if err != nil {
		t.Fatal(err)
	}
	// The stub copies the page into OAM; what matters here is
	// the stall: after reset the CPU sits at odd cycle 7, so the
	// DMA costs 514 stall steps before the NOP executes.
	s.Write(0x4014, 0x02)
	steps := 0
	for s.CPU().PC() == 0x8000 && steps < 2000 {
		s.Step()
		steps++
	}
	// 514 stall steps plus the NOP's own step.
	if steps != 515 {
		t.Fatalf("DMA stalled %d steps before the NOP ran, want 515 (514 stall + 1)", steps)
	}
}

func TestBlarggResultProtocol(t *testing.T) {
	s, err := New(nromImage(true, jmpSelf...))
	if err != nil {
		t.Fatal(err)
	}
	// Drive the protocol memory through the bus, the way a test
	// ROM does.
	s.Write(0x6001, 0xDE)
	s.Write(0x6002, 0xB0)
	s.Write(0x6003, 0x61)
	for i, b := range []byte("Passed\n\x00") {
		s.Write(0x6004+uint16(i), b)
	}
	s.Write(0x6000, 0x80) // still running
	if ok, _, _ := s.BlarggResult(); ok {
		t.Fatalf("result reported while running")
	}
	s.Write(0x6000, 0x00)
	ok, passed, text := s.BlarggResult()
	if !ok || !passed || text != "Passed\n" {
		t.Fatalf("BlarggResult = %v %v %q", ok, passed, text)
	}
}
