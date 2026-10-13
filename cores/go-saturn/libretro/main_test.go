package main

// In-process shell gates. The payload is the
// enginetest package's synthetic Saturn disc, driven
// through the same Go-level load and run functions
// the retro_* exports wrap. The literal C ABI is
// gated end to end by the shared enginetest harness
// (go_backend), which loads the built .so through
// the production bridge; this test exists so go test
// alone catches shell regressions: load by path,
// BootDisc boot, guest milestones, save state round
// trip, memory regions, clean rejection of bad
// content.
//
// The guest's pad stage reads no buttons here (no
// frontend input callbacks are installed), so its
// INTBACK report is the all released pattern; the
// system gate covers the pressed buttons case.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-saturn/enginetest"
	"github.com/davidpatton/itak-gaming/cores/go-saturn/system"
)

func mileCount() int {
	return int(sys.Bus.Read32(enginetest.MilestoneAddress + enginetest.MileCount))
}

func TestShellDisc(t *testing.T) {
	dir := t.TempDir()
	isoPath := filepath.Join(dir, "enginetest.iso")
	if err := os.WriteFile(isoPath, enginetest.BuildDisc(), 0o644); err != nil {
		t.Fatal(err)
	}
	if !loadPath(isoPath) {
		t.Fatal("loadPath(.iso) returned false")
	}
	defer func() { sys = nil; hasDisc = false }()

	// The guest runs from BootDisc and completes all
	// seven milestones through the shell's runFrame.
	frames := 0
	for frames < 300 && mileCount() < 7 {
		runFrame()
		frames++
	}
	if got := sys.Bus.Read32(enginetest.MilestoneAddress + enginetest.MileError); got != 0 {
		t.Fatalf("guest failure stage = %d", got)
	}
	if got := mileCount(); got != 7 {
		t.Fatalf("milestones = %d after %d shell frames, want 7", got, frames)
	}
	if got := sys.Bus.Read32(enginetest.MilestoneAddress + enginetest.MileChecksum); got != enginetest.PayloadChecksum() {
		t.Fatalf("guest checksum = %#x, want %#x", got, enginetest.PayloadChecksum())
	}
	// No buttons through the shell: the INTBACK data
	// bytes read all released.
	pad := sys.Bus.WorkRAMHigh()[0x4000+enginetest.MilePad : 0x4000+enginetest.MilePad+4]
	if !bytes.Equal(pad, []byte{0xF1, 0x02, 0xFF, 0xFF}) {
		t.Fatalf("INTBACK report = % x, want f1 02 ff ff (no buttons)", pad)
	}

	// Settle the VDP1 draw and swap pipeline, then
	// probe the frame the system gate proves.
	for i := 0; i < 5; i++ {
		runFrame()
	}
	if w, h := sys.DisplaySize(); w != 320 || h != 224 {
		t.Fatalf("display size = %dx%d, want 320x224", w, h)
	}
	fb := sys.Framebuffer()
	if got := fb[0]; got != 0xFF00FF00 {
		t.Fatalf("pixel (0,0) = %#x, want green 0xFF00FF00", got)
	}
	if got := fb[55*320+105]; got != 0xFFFFFFFF {
		t.Fatalf("pixel (105,55) = %#x, want white 0xFFFFFFFF", got)
	}

	// Save state round trip through the machine the
	// shell wraps (retro_serialize forwards here).
	saved := sys.Serialize()
	if len(saved) != sys.StateSize() {
		t.Fatalf("state length = %d, want StateSize %d", len(saved), sys.StateSize())
	}
	m2 := system.New()
	if err := m2.Deserialize(saved); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if again := m2.Serialize(); !bytes.Equal(saved, again) {
		t.Error("state bytes differ after a serialize/deserialize round trip")
	}

	// Memory regions: backup RAM logical store is
	// 32 KiB; the SYSTEM_RAM mirror is 2 MiB and
	// holds the booted IP in its Work RAM-H half.
	if got := len(sys.Bus.BackupRAM()); got != 32*1024 {
		t.Errorf("backup RAM length = %d, want %d", got, 32*1024)
	}
	if got := len(sysRAMCache); got != 2*1024*1024 {
		t.Fatalf("SYSTEM_RAM cache length = %d, want %d", got, 2*1024*1024)
	}
	if got := string(sysRAMCache[0x100000+0x2000 : 0x100000+0x2000+16]); got != "SEGA SEGASATURN " {
		t.Errorf("SYSTEM_RAM Work RAM-H holds %q at the IP base, want the System ID", got)
	}
	// The guest's backup pattern is visible through
	// the logical store the shell exposes.
	for i := 0; i < 32; i++ {
		if got := sys.Bus.BackupRAM()[i]; got != byte(0x41+i) {
			t.Fatalf("backup byte %d = %#x, want %#x", i, got, byte(0x41+i))
		}
	}
}

func TestShellRejectsBadContent(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "data.iso")
	if err := os.WriteFile(raw, make([]byte, 40*2048), 0o644); err != nil {
		t.Fatal(err)
	}
	if loadPath(raw) {
		t.Error("loadPath accepted an .iso with no Saturn System ID")
	}
	missing := filepath.Join(dir, "missing.iso")
	if loadPath(missing) {
		t.Error("loadPath accepted a missing .iso file")
	}
	if sys != nil {
		t.Error("a rejected load left a machine behind")
	}
}
