package system

import "testing"

// nmiFlagROM builds a LoROM that models the Harvest Moon
// main loop pattern (found 2026-11-02): the NMI handler
// sets a WRAM flag, the main loop waits on the flag and
// counts one per frame, and the game NEVER reads 210.
// On hardware the PPU pulses NMI every vblank whether or
// not 210 is read, so the counter must advance roughly
// once per frame. Driving the CPU NMI line from the
// readable flag instead delivered exactly one NMI ever
// and parked this pattern forever.
func nmiFlagROM() []byte {
	img := make([]byte, 0x80000)
	code := []byte{
		0x78,       // reset: SEI
		0x18, 0xFB, // CLC, XCE (native mode)
		0xC2, 0x30, // REP #0
		0xA9, 0xFF, 0x01, // LDA #bash1FF
		0x1B,             // TCS
		0xA9, 0x00, 0x00, // LDA #bash000
		0x5B,             // TCD (D = 0)
		0xE2, 0x20,       // SEP #0
		0x64, 0x10,       // STZ 0 (frame counter)
		0xA9, 0x81,       // LDA #1
		0x8D, 0x00, 0x42, // STA 200 (NMI on)
		// wait:
		0x64, 0x00, // STZ bash0 (clear flag)
		// poll:
		0xA5, 0x00, // LDA bash0
		0xF0, 0xFC, // BEQ poll
		0xE6, 0x10, // INC 0
		0x80, 0xF6, // BRA wait
	}
	copy(img, code)
	handler := []byte{
		0x48,             // PHA
		0xA9, 0x01,       // LDA #bash1
		0x8D, 0x00, 0x00, // STA bash000
		0x68,             // PLA
		0x40,             // RTI
	}
	copy(img[0x100:], handler)
	h := img[0x7FC0 : 0x7FC0+0x40]
	copy(h, "SNES-2 NMI FLAG TEST ")
	h[0x15] = 0x20 // LoROM
	h[0x16] = 0x02 // ROM + RAM + battery
	h[0x17] = 0x09 // 512 KB
	h[0x18] = 0x03 // 8 KB SRAM
	h[0x1C], h[0x1D] = 0x55, 0xAA
	h[0x1E], h[0x1F] = 0xAA, 0x55
	h[0x2A], h[0x2B] = 0x00, 0x81 // native NMI vector 100
	h[0x3A], h[0x3B] = 0x00, 0x81 // emulation NMI vector 100
	h[0x3C], h[0x3D] = 0x00, 0x80 // reset vector 000
	return img
}

func TestNMIFlagWaitPattern(t *testing.T) {
	s, err := New(nmiFlagROM())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for f := 0; f < 30; f++ {
		s.RunFrame()
	}
	got := s.SystemRAM()[0x10]
	if got < 25 {
		t.Fatalf("frame counter = %d after 30 frames, want >= 25 (NMI must pulse every vblank even when 210 is never read)", got)
	}
}
