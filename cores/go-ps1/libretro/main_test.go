package main

// In-process shell gates. The payloads are the
// homebrew enginetest EXE and the synthetic cue/bin
// disc carrying it, driven through the same Go-level
// load and run functions the retro_* exports wrap.
// The literal C ABI is gated end to end by the
// shared enginetest harness (go_backend), which
// loads the built .so through the production bridge;
// this test exists so go test alone catches shell
// regressions: load by extension, boot, PASS magic,
// save state round trip, memory regions, clean
// rejection of bad content.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-ps1/homebrew"
	"github.com/davidpatton/itak-gaming/cores/go-ps1/system"
)

func runUntilPass(t *testing.T) bool {
	t.Helper()
	if sys == nil {
		t.Fatal("no machine loaded")
	}
	for frame := 0; frame < 180; frame++ {
		runFrame()
		if frame%10 == 9 && sys.Bus.Read32(0x80020000) == 0x600DF00D {
			return true
		}
	}
	return sys.Bus.Read32(0x80020000) == 0x600DF00D
}

// runUntilStage advances until the guest's stage
// marker (result +0x100) reaches want, or the frame
// budget runs out.
func runUntilStage(want uint32, maxFrames int) bool {
	for frame := 0; frame < maxFrames; frame++ {
		runFrame()
		if frame%10 == 9 && sys.Bus.Read32(0x80020100) >= want {
			return true
		}
	}
	return sys.Bus.Read32(0x80020100) >= want
}

func TestShellEXE(t *testing.T) {
	dir := t.TempDir()
	exe := homebrew.EnginetestEXE()
	exePath := filepath.Join(dir, "engtest.exe")
	if err := os.WriteFile(exePath, exe, 0o644); err != nil {
		t.Fatal(err)
	}
	if !loadPath(exePath, exe) {
		t.Fatal("loadPath(.exe) returned false")
	}
	defer func() { sys = nil }()
	if sys == nil {
		t.Fatal("machine not set after load")
	}
	if !sys.AnalogMode() {
		t.Error("analog mode actual false after shell load, want forced on")
	}
	// A bare .exe load has no disc attached, and
	// this guest's stage D reads a CD sector: with
	// nothing to read it parks in its timeout spin
	// after stage 4 (its CD poll cap). The full PASS
	// run needs the disc, which TestShellDisc does
	// through the .cue. Here the gate is: the BIOS
	// event stage, GPU stage and SPU stage all
	// complete (marker 4) and the quad is on screen.
	if !runUntilStage(4, 180) {
		t.Fatalf("guest stage marker actual %d through the shell (EXE), want 4", sys.Bus.Read32(0x80020100))
	}
	if w, h := sys.DisplayWidth(), sys.DisplayHeight(); w != 320 || h != 240 {
		t.Errorf("display size after the guest's GP1 setup actual %dx%d, want 320x240", w, h)
	}
	fb := sys.Framebuffer()
	nonblack := 0
	for _, px := range fb {
		if px != 0xFF000000 {
			nonblack++
		}
	}
	if nonblack == 0 {
		t.Error("framebuffer is all black after the GPU stage")
	}
	if got := sys.UnimplementedBIOSCallCount(); got != 0 {
		t.Errorf("unimplemented BIOS calls actual %d, want 0", got)
	}

	// Save state round trip through the machine the
	// shell wraps (retro_serialize forwards here).
	saved := sys.Serialize()
	if len(saved) != sys.StateSize() {
		t.Fatalf("state length actual %d, want StateSize %d", len(saved), sys.StateSize())
	}
	for i := 0; i < 10; i++ {
		runFrame()
	}
	m2 := system.New()
	if err := m2.Deserialize(saved); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if again := m2.Serialize(); !bytes.Equal(saved, again) {
		t.Error("state bytes differ after a serialize/deserialize round trip")
	}

	// Memory regions the shell exposes: the card is
	// 128 KB, main RAM is 2 MB.
	if got := len(sys.CardImage()); got != 128*1024 {
		t.Errorf("card image length actual %d, want %d", got, 128*1024)
	}
	if got := len(sys.SystemRAM()); got != 2*1024*1024 {
		t.Errorf("system RAM length actual %d, want %d", got, 2*1024*1024)
	}
}

func TestShellDisc(t *testing.T) {
	dir := t.TempDir()
	exe := homebrew.EnginetestEXE()
	cue, bin := homebrew.EnginetestDisc(exe)
	if err := os.WriteFile(filepath.Join(dir, "ENGTEST.BIN"), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	cuePath := filepath.Join(dir, "engtest.cue")
	if err := os.WriteFile(cuePath, []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}
	if !loadPath(cuePath, []byte(cue)) {
		t.Fatal("loadPath(.cue) returned false")
	}
	defer func() { sys = nil }()
	if !runUntilPass(t) {
		t.Fatal("guest PASS magic not reached through the shell (disc)")
	}
}

func TestShellRejectsBadContent(t *testing.T) {
	dir := t.TempDir()
	bogus := filepath.Join(dir, "bogus.exe")
	if err := os.WriteFile(bogus, []byte("definitely not a PS-X EXE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if loadPath(bogus, nil) {
		t.Error("loadPath accepted a non-EXE .exe file")
	}
	missing := filepath.Join(dir, "missing.cue")
	if loadPath(missing, nil) {
		t.Error("loadPath accepted a missing .cue file")
	}
	// A readable image with no SYSTEM.CNF does not
	// boot and is rejected.
	raw := filepath.Join(dir, "data.iso")
	if err := os.WriteFile(raw, make([]byte, 40*2048), 0o644); err != nil {
		t.Fatal(err)
	}
	if loadPath(raw, nil) {
		t.Error("loadPath accepted an unbootable .iso")
	}
	if sys != nil {
		t.Error("a rejected load left a machine behind")
	}
}
