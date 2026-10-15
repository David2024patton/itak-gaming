package core

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// loopROM builds a minimal LoROM whose reset code is an
// endless branch loop. Mirrors the go-snes system tests'
// fixture: enough for the machine to power on and run frames.
func loopROM() []byte {
	img := make([]byte, 0x80000)
	img[0] = 0x80 // BRA -2 (spin)
	img[1] = 0xFE
	h := img[0x7FC0 : 0x7FC0+0x40]
	copy(h, "SNES LOOP TEST       ")
	h[0x15] = 0x20 // LoROM
	h[0x16] = 0x02 // ROM + RAM + battery
	h[0x17] = 0x09 // 512 KB
	h[0x18] = 0x03 // 8 KB SRAM
	h[0x1C], h[0x1D] = 0x55, 0xAA
	h[0x1E], h[0x1F] = 0xAA, 0x55
	h[0x3C], h[0x3D] = 0x00, 0x80 // reset vector 0x8000
	return img
}

func loadLoopSNES(t *testing.T) *NativeBridge {
	t.Helper()
	romPath := filepath.Join(t.TempDir(), "loop.sfc")
	if err := os.WriteFile(romPath, loopROM(), 0o644); err != nil {
		t.Fatal(err)
	}
	nb, ok := LoadNative("/data/data/net.itak.itak_gaming/files/cores/libgo_snes.so")
	if !ok {
		t.Fatal("LoadNative did not recognize libgo_snes.so")
	}
	if err := nb.LoadGame(romPath); err != nil {
		t.Fatalf("LoadGame: %v", err)
	}
	return nb
}

// The registry must not claim cores that have no native
// adapter yet.
func TestLoadNativeUnknown(t *testing.T) {
	if _, ok := LoadNative("/x/libgo_psp.so"); ok {
		t.Fatal("LoadNative claimed a core with no adapter")
	}
}

// A native SNES runs frames in this process (one Go runtime),
// produces a correctly shaped frame, and round trips a save
// state deterministically.
func TestNativeSNESRunsInProcess(t *testing.T) {
	a := loadLoopSNES(t)
	defer a.Close()
	for i := 0; i < 120; i++ {
		a.RunFrame()
	}
	w, h, pitch, data := a.GetFrame()
	if w != 256 || h != 224 {
		t.Fatalf("frame geometry = %dx%d, want 256x224", w, h)
	}
	if pitch != 256*4 || len(data) != 224*pitch {
		t.Fatalf("frame pitch/len = %d/%d, want %d/%d", pitch, len(data), 1024, 224*1024)
	}
	if got := a.VideoFrameCount(); got != 120 {
		t.Fatalf("VideoFrameCount = %d, want 120", got)
	}
	if _, _, _, err := a.FrameToRGBA(); err != nil {
		t.Fatalf("FrameToRGBA: %v", err)
	}
	if a.PixelFormat() != 1 {
		t.Fatalf("PixelFormat = %d, want 1 (XRGB8888)", a.PixelFormat())
	}
	if a.SampleRate() != 32000 {
		t.Fatalf("SampleRate = %v, want 32000", a.SampleRate())
	}
	// Audio drain must be safe even for a silent ROM.
	_ = a.DrainAudio(4096)

	state, err := a.SaveState()
	if err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if len(state) < 1000 {
		t.Fatalf("state size = %d, suspiciously small", len(state))
	}

	// A second machine restored from the state must continue
	// byte identically.
	b := loadLoopSNES(t)
	defer b.Close()
	for i := 0; i < 120; i++ {
		b.RunFrame()
	}
	if err := b.LoadState(state); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for i := 0; i < 60; i++ {
		a.RunFrame()
		b.RunFrame()
	}
	sa, err := a.SaveState()
	if err != nil {
		t.Fatalf("SaveState A: %v", err)
	}
	sb, err := b.SaveState()
	if err != nil {
		t.Fatalf("SaveState B: %v", err)
	}
	if !bytes.Equal(sa, sb) {
		t.Fatal("states diverged after restore + 60 frames")
	}
	_, _, _, da := a.GetFrame()
	_, _, _, db := b.GetFrame()
	if !bytes.Equal(da, db) {
		t.Fatal("frames diverged after restore + 60 frames")
	}
}
