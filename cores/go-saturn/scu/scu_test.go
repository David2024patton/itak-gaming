package scu

import "testing"

// fakeCPU records the last interrupt offered to it.
type fakeCPU struct {
	level  int
	vector int
	calls  int
}

func (f *fakeCPU) offer(s *SCU) bool {
	bit, vec := s.Deliver()
	if bit < 0 {
		return false
	}
	f.level = Level
	f.vector = vec
	f.calls++
	return true
}

func newPage() (*SCU, []byte) {
	page := make([]byte, 256)
	return New(page), page
}

func TestRaiseAndPending(t *testing.T) {
	s, _ := newPage()
	if got := s.Pending(); got != 0 {
		t.Fatalf("pending = %#x, want 0", got)
	}
	s.Raise(IntVBlankIn)
	s.Raise(IntSpriteEnd)
	want := uint32(1<<IntVBlankIn | 1<<IntSpriteEnd)
	if got := s.Pending(); got != want {
		t.Fatalf("pending = %#x, want %#x", got, want)
	}
	if got := s.Status(); got != want {
		t.Fatalf("status = %#x, want %#x", got, want)
	}
}

func TestMaskBlocksDeliveryNotStatus(t *testing.T) {
	s, page := newPage()
	s.Raise(IntVBlankIn)
	// Mask VBlank-IN by writing IMS through the page: bit 0
	// of the word at 0xA0. Big endian: bits 0..7 live in
	// the last byte of the word.
	page[0xA3] = 0x01
	if got := s.Pending(); got != 0 {
		t.Fatalf("pending = %#x, want 0 while masked", got)
	}
	if got := s.Status(); got != 1 {
		t.Fatalf("status = %#x, want 1 (mask must not clear status)", got)
	}
	f := &fakeCPU{}
	if f.offer(s) {
		t.Fatal("masked source was delivered")
	}
}

func TestDeliverPriorityOrderAndClear(t *testing.T) {
	s, page := newPage()
	s.Raise(IntSpriteEnd)
	s.Raise(IntHBlankIn)
	s.Raise(IntVBlankIn)
	f := &fakeCPU{}
	if !f.offer(s) || f.vector != 0x40 {
		t.Fatalf("first delivery vector = %#x, want 0x40 (VBlank-IN)", f.vector)
	}
	// Software clears VBlank-IN by writing 0 to IST bit 0
	// through the page.
	page[0xA7] &^= 0x01
	if !f.offer(s) || f.vector != 0x42 {
		t.Fatalf("second delivery vector = %#x, want 0x42 (HBlank-IN)", f.vector)
	}
	page[0xA7] &^= 0x04
	if !f.offer(s) || f.vector != 0x4D {
		t.Fatalf("third delivery vector = %#x, want 0x4D (sprite end)", f.vector)
	}
	page[0xA6] &^= 0x10
	if f.offer(s) {
		t.Fatal("delivery reported with nothing pending")
	}
}

func TestVectorTable(t *testing.T) {
	if got := Vector(IntVBlankIn); got != 0x40 {
		t.Fatalf("VBlank-IN vector = %#x, want 0x40", got)
	}
	if got := Vector(IntVBlankOut); got != 0x41 {
		t.Fatalf("VBlank-OUT vector = %#x, want 0x41", got)
	}
	if got := Vector(IntHBlankIn); got != 0x42 {
		t.Fatalf("HBlank-IN vector = %#x, want 0x42", got)
	}
	if got := Vector(99); got != -1 {
		t.Fatalf("out of range vector = %d, want -1", got)
	}
	s, _ := newPage()
	s.Raise(99)
	if got := s.Status(); got != 0 {
		t.Fatalf("out of range raise changed status: %#x", got)
	}
}
