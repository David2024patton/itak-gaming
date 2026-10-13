// End-to-end gates on David's local Super Mario World
// test ROM. The ROM is never committed; when it is
// absent (CI, fresh clones) these tests skip, like the
// Tom Harte corpus harnesses.
//
// TestSMWAttractDemoVisible is the regression gate for
// the SNES-4a black screen: with the CGWSEL region
// fields read from the wrong bit positions, the title
// rendered but the attract demo (and everything after
// the title fade) was forced to black by a color
// window clip the game never asked for. The oracle
// (Snes9x, same ROM) holds roughly 53,000 non-black
// pixels through the whole demo; every checkpoint
// below must stay in that class.
package system

import (
	"bytes"
	"hash/fnv"
	"os"
	"path/filepath"
	"testing"
)

func smwROM(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "..", "testdata", "Super Mario World (USA).sfc")
	rom, err := os.ReadFile(path)
	if err != nil {
		t.Skip("SMW test ROM absent")
	}
	return rom
}

func nonblack(s *System) int {
	n := 0
	for _, px := range s.Framebuffer() {
		if px&0x00FFFFFF != 0 {
			n++
		}
	}
	return n
}

func frameHash(s *System) uint64 {
	h := fnv.New64a()
	var buf [4]byte
	for _, px := range s.Framebuffer() {
		buf[0] = byte(px)
		buf[1] = byte(px >> 8)
		buf[2] = byte(px >> 16)
		buf[3] = byte(px >> 24)
		h.Write(buf[:])
	}
	return h.Sum64()
}

func TestSMWAttractDemoVisible(t *testing.T) {
	rom := smwROM(t)
	checkpoints := map[int]bool{120: true, 320: true, 450: true, 600: true, 1200: true}
	run := func() map[int]uint64 {
		s, err := New(rom)
		if err != nil {
			t.Fatal(err)
		}
		hashes := map[int]uint64{}
		for f := 1; f <= 1200; f++ {
			s.RunFrame()
			if !checkpoints[f] {
				continue
			}
			if nb := nonblack(s); nb < 50000 {
				t.Errorf("frame %d: nonblack = %d, want > 50000 (logo/title/demo must be visible)", f, nb)
			}
			hashes[f] = frameHash(s)
		}
		return hashes
	}
	first := run()
	second := run()
	for f, h := range first {
		if second[f] != h {
			t.Errorf("frame %d: run-to-run frame hash differs (%016x vs %016x), want deterministic",
				f, h, second[f])
		}
	}
}

func TestSMWGameplayAndSaveState(t *testing.T) {
	rom := smwROM(t)
	s, err := New(rom)
	if err != nil {
		t.Fatal(err)
	}
	wram := s.SystemRAM()
	mode := func() int { return int(wram[0x100]) }
	hold := func(f, from, to int, mask uint16) {
		if f >= from && f <= to {
			s.SetButtons(0, mask)
		}
	}
	// Ride the attract demo, exit it with B (the game's
	// demo-exit check accepts B, Y, A, X, L, R), then
	// confirm through the menus with A and Start. The
	// script must carry the state machine from the demo
	// (mode 7) to the file select (mode 8) and on to
	// the overworld map (mode 20), each transition
	// driven by the scripted input.
	sawFileSelect := false
	sawOverworld := false
	for f := 1; f <= 1000; f++ {
		hold(f, 600, 609, ButtonB)
		hold(f, 700, 709, ButtonA)
		hold(f, 800, 809, ButtonA)
		hold(f, 900, 909, ButtonStart)
		if f < 600 || (f > 609 && f < 700) || (f > 709 && f < 800) ||
			(f > 809 && f < 900) || f > 909 {
			s.SetButtons(0, 0)
		}
		s.RunFrame()
		if mode() == 8 {
			sawFileSelect = true
		}
		if mode() == 20 {
			sawOverworld = true
		}
	}
	if !sawFileSelect {
		t.Errorf("never reached the file select (mode 8); final mode = %d", mode())
	}
	if !sawOverworld {
		t.Fatalf("never reached the overworld map (mode 20); final mode = %d", mode())
	}
	if nb := nonblack(s); nb < 40000 {
		t.Fatalf("overworld frame nonblack = %d, want > 40000", nb)
	}
	// Save state at gameplay: serialize, continue,
	// restore, and the continuation must be identical
	// in picture and audio.
	s.AudioBuffer() // drain
	state := s.Serialize()
	continueRun := func() (uint64, []int16) {
		for f := 0; f < 30; f++ {
			s.RunFrame()
		}
		return frameHash(s), s.AudioBuffer()
	}
	wantHash, wantAudio := continueRun()
	if err := s.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	gotHash, gotAudio := continueRun()
	if gotHash != wantHash {
		t.Errorf("post-restore frame hash = %016x, want %016x", gotHash, wantHash)
	}
	if !bytes.Equal(int16Bytes(gotAudio), int16Bytes(wantAudio)) {
		t.Errorf("post-restore audio differs from the uninterrupted run (%d vs %d samples)",
			len(gotAudio), len(wantAudio))
	}
}

func int16Bytes(in []int16) []byte {
	out := make([]byte, 2*len(in))
	for i, v := range in {
		out[2*i] = byte(v)
		out[2*i+1] = byte(v >> 8)
	}
	return out
}
