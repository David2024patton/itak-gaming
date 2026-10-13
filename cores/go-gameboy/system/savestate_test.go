package system

import (
	"hash/fnv"
	"os"
	"path/filepath"
	"testing"
)

// homebrewROM locates the free homebrew game used for whole
// machine determinism checks.
func homebrewROM(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "..", "homebrew", "tobutobugirl.gb")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("homebrew ROM absent: %v", err)
	}
	return data
}

func hashFrame(s *System) uint64 {
	h := fnv.New64a()
	fb := s.Framebuffer()
	var b [2]byte
	for _, px := range fb {
		b[0] = uint8(px)
		b[1] = uint8(px >> 8)
		h.Write(b[:])
	}
	return h.Sum64()
}

func hashAudio(samples []int16) uint64 {
	h := fnv.New64a()
	var b [2]byte
	for _, v := range samples {
		b[0] = uint8(v)
		b[1] = uint8(v >> 8)
		h.Write(b[:])
	}
	return h.Sum64()
}

// TestSaveStateDeterminism runs a real homebrew game, saves the
// whole machine mid-play, runs on, then restores and proves the
// continuation is bit-identical in video and audio.
func TestSaveStateDeterminism(t *testing.T) {
	s, err := New(homebrewROM(t))
	if err != nil {
		t.Fatal(err)
	}
	// Play for two seconds of emulated time, pressing buttons so
	// the game leaves its title screen and exercises input.
	for f := 0; f < 120; f++ {
		if f > 30 {
			s.SetButtons(0, 0x80) // Start held from frame 30
		}
		if f > 40 {
			s.SetButtons(0, 0x10) // then A
		}
		s.RunFrame()
	}
	size := s.SerializeSize()
	state := make([]byte, size)
	if err := s.Serialize(state); err != nil {
		t.Fatal(err)
	}
	t.Logf("whole-machine save state: %d bytes", size)

	// First continuation.
	for f := 0; f < 30; f++ {
		s.RunFrame()
	}
	wantFrame := hashFrame(s)
	_ = s.AudioBuffer()

	// Restore and continue again.
	if err := s.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	for f := 0; f < 30; f++ {
		s.RunFrame()
	}
	gotFrame := hashFrame(s)
	if gotFrame != wantFrame {
		t.Fatalf("frame hash after restore = %016x, want %016x", gotFrame, wantFrame)
	}

	// Audio: hash the samples produced across the continuation.
	// Redo the pair so both runs capture audio the same way.
	if err := s.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	var a1 []int16
	for f := 0; f < 30; f++ {
		s.RunFrame()
		a1 = append(a1, s.AudioBuffer()...)
	}
	if err := s.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	var a2 []int16
	for f := 0; f < 30; f++ {
		s.RunFrame()
		a2 = append(a2, s.AudioBuffer()...)
	}
	if len(a1) != len(a2) || len(a1) == 0 {
		t.Fatalf("audio lengths: %d vs %d", len(a1), len(a2))
	}
	if hashAudio(a1) != hashAudio(a2) {
		t.Fatal("audio after restore differs from the first continuation")
	}
	// The restored machine must also serialize back to the
	// identical byte image once it has run the same frames.
	end := make([]byte, size)
	if err := s.Serialize(end); err != nil {
		t.Fatal(err)
	}
	if err := s.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	for f := 0; f < 30; f++ {
		s.RunFrame()
	}
	end2 := make([]byte, size)
	if err := s.Serialize(end2); err != nil {
		t.Fatal(err)
	}
	for i := range end {
		if end[i] != end2[i] {
			t.Fatalf("post-run state mismatch at byte %d: 0x%02X vs 0x%02X", i, end[i], end2[i])
		}
	}
}
