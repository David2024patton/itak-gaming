package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/davidpatton/itak-gaming/cores/go-psp/enginetest"
	"github.com/davidpatton/itak-gaming/cores/go-psp/system"
)

// In-process shell gates: the libretro contract (info, AV,
// memory, serialize) plus both load paths and clean rejection
// of bad images, driven through loadPath/runFrame directly.

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestShellSystemInfo(t *testing.T) {
	name, _, exts := systemInfoValues()
	if name != "iTaK GoPSP" {
		t.Fatalf("library name = %q, want %q", name, "iTaK GoPSP")
	}
	if exts != "iso|elf|prx" {
		t.Fatalf("valid extensions = %q, want %q", exts, "iso|elf|prx")
	}
	// need_fullpath is declared 1 in retro_get_system_info:
	// ISO boots read through the umd layer by path.
}

func TestShellAVInfo(t *testing.T) {
	w, h, fps, rate := avInfoValues()
	if w != 480 || h != 272 {
		t.Fatalf("geometry = %dx%d, want 480x272", w, h)
	}
	if fps != 60.0 {
		t.Fatalf("fps = %v, want 60.0 (3,700,000 cycles per frame at 222 MHz)", fps)
	}
	if rate != 44100 {
		t.Fatalf("sample_rate = %v, want 44100", rate)
	}
}

func TestShellLoadISOAndRun(t *testing.T) {
	defer retro_unload_game()
	path := writeTemp(t, "engine.iso", enginetest.ISOImage())
	if !loadPath(path) {
		t.Fatal("loadPath(.iso) failed")
	}
	for i := 0; i < 100; i++ {
		runFrame()
	}
	if mach == nil {
		t.Fatal("no machine after load")
	}
	if got := mach.Bus.Read32(enginetest.MilestoneAddr + enginetest.OffProgress); got != enginetest.MilestoneFinal {
		t.Fatalf("progress = %d, want %d", got, enginetest.MilestoneFinal)
	}
	if got := mach.ConsoleOutput(); got != enginetest.ConsoleText {
		t.Fatalf("console = %q, want %q", got, enginetest.ConsoleText)
	}
	fb := mach.Framebuffer()
	if got := fb[10*system.Width+10]; got != enginetest.SpritePixel {
		t.Fatalf("sprite pixel = %#08x, want %#08x", got, enginetest.SpritePixel)
	}
	if got := fb[0]; got != enginetest.ClearPixel {
		t.Fatalf("clear pixel = %#08x, want %#08x", got, enginetest.ClearPixel)
	}
	// Memory contract: SYSTEM_RAM is the 32 MB main RAM,
	// SAVE_RAM is nil/0 (the ms0: store has no fixed backing).
	if got := retro_get_memory_size(memorySysRAM); got != 32<<20 {
		t.Fatalf("SYSTEM_RAM size = %d, want %d", got, 32<<20)
	}
	if got := retro_get_memory_size(memorySaveRAM); got != 0 {
		t.Fatalf("SAVE_RAM size = %d, want 0", got)
	}
	if p := retro_get_memory_data(memorySaveRAM); p != nil {
		t.Fatalf("SAVE_RAM data = %v, want nil", p)
	}
	if p := retro_get_memory_data(memorySysRAM); p == nil {
		t.Fatal("SYSTEM_RAM data is nil")
	}
}

func TestShellSerializeRoundTrip(t *testing.T) {
	defer retro_unload_game()
	path := writeTemp(t, "engine.iso", enginetest.ISOImage())
	if !loadPath(path) {
		t.Fatal("loadPath failed")
	}
	for i := 0; i < 20; i++ {
		runFrame()
	}
	size := retro_serialize_size()
	if size == 0 {
		t.Fatal("serialize size is 0 after load")
	}
	buf := make([]byte, size)
	if retro_serialize(unsafe.Pointer(&buf[0]), size) != 1 {
		t.Fatal("retro_serialize failed")
	}
	for i := 0; i < 5; i++ {
		runFrame()
	}
	if retro_unserialize(unsafe.Pointer(&buf[0]), size) != 1 {
		t.Fatal("retro_unserialize failed")
	}
	want := mach.Serialize()
	if !bytes.Equal(buf, want) {
		t.Fatal("state after unserialize differs from the serialized bytes")
	}
	// Reset re-boots the loaded content.
	retro_reset()
	if got := mach.Bus.Read32(enginetest.MilestoneAddr + enginetest.OffProgress); got != 0 {
		t.Fatalf("progress after reset = %d, want 0 (fresh boot)", got)
	}
	for i := 0; i < 100; i++ {
		runFrame()
	}
	if got := mach.Bus.Read32(enginetest.MilestoneAddr + enginetest.OffProgress); got != enginetest.MilestoneFinal {
		t.Fatalf("progress after reset + 100 frames = %d, want %d", got, enginetest.MilestoneFinal)
	}
}

func TestShellLoadELFDirect(t *testing.T) {
	defer retro_unload_game()
	path := writeTemp(t, "guest.elf", enginetest.GuestELF())
	if !loadPath(path) {
		t.Fatal("loadPath(.elf) failed")
	}
	for i := 0; i < 100; i++ {
		runFrame()
	}
	if got := mach.Bus.Read32(enginetest.MilestoneAddr + enginetest.OffProgress); got != enginetest.MilestoneFinal {
		t.Fatalf("progress = %d, want %d", got, enginetest.MilestoneFinal)
	}
}

func TestShellRejectsBadImages(t *testing.T) {
	defer retro_unload_game()
	iso := enginetest.ISOImage()
	cases := []struct {
		name string
		data []byte
		file string
	}{
		{"garbage iso", []byte("this is not an ISO image at all"), "bad.iso"},
		{"truncated iso", iso[:len(iso)/2], "trunc.iso"},
		{"garbage elf", []byte{0x7F, 'E', 'L', 'F', 1, 2, 3}, "bad.elf"},
		{"empty prx", nil, "empty.prx"},
		{"wrong extension", iso, "game.bin"},
	}
	for _, c := range cases {
		if loadPath(writeTemp(t, c.file, c.data)) {
			t.Fatalf("%s: loadPath succeeded, want clean rejection", c.name)
		}
	}
	// A well-formed ISO whose SYSDR holds no boot executable:
	// rename BOOT.BIN in the directory data.
	noBoot := append([]byte{}, iso...)
	idx := bytes.Index(noBoot, []byte("BOOT.BIN;1"))
	if idx < 0 {
		t.Fatal("fixture ISO does not contain BOOT.BIN;1")
	}
	noBoot[idx] = 'X'
	if loadPath(writeTemp(t, "noboot.iso", noBoot)) {
		t.Fatal("ISO without BOOT.BIN/EBOOT.BIN loaded, want clean rejection")
	}
}
