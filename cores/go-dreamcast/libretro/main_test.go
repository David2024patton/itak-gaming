package main

// In-process shell gates. The payload is a minimal
// synthetic .iso whose IP.BIN carries a tiny hand
// assembled program (the IP itself is the program: it runs
// straight from the IP image at 0x8C008300), driven
// through the same Go-level load and run functions the
// retro_* exports wrap. The literal C ABI is gated end to
// end by the shared enginetest harness (go_backend), which
// loads the built .so through the production bridge; the
// full boot through the BIOS GD interface is gated in the
// system package (TestDC4Enginetest). This test exists so
// go test alone catches shell regressions: load by path,
// boot, magic word, save state round trip, memory
// regions, clean rejection of bad content.

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/system"
)

// shellISO builds a minimal ISO: the boot area carries a
// valid IP.BIN header plus a tiny program at the entry
// offset that stores a magic word at 0x8C200000 and spins.
//
//	0x300: D103  MOV.L @(3,PC),R1   ; literal at 0x310
//	0x302: D204  MOV.L @(4,PC),R2   ; literal at 0x314
//	0x304: 2122  MOV.L R2,@R1
//	0x306: AFFE  BRA -2              ; spin
//	0x308: 0009  NOP                 ; delay slot
//	0x30A: 0009  NOP
//	0x30C: 0000  pad
//	0x30E: 0000  pad
//	0x310: .long 0x8C200000
//	0x314: .long 0x600DCAFE
func shellISO(t *testing.T) []byte {
	t.Helper()
	img := make([]byte, 20*2048)
	put := func(off int, s string) {
		for len(s) < 16 {
			s += " "
		}
		copy(img[off:off+16], s)
	}
	put(0x00, "SEGA SEGAKATANA")
	put(0x60, "1ST_READ.BIN")
	prog := []uint16{
		0xD103, 0xD204, 0x2122, 0xAFFE, 0x0009,
		0x0009, 0x0000, 0x0000,
		0x0000, 0x8C20, // 0x8C200000
		0xCAFE, 0x600D, // 0x600DCAFE
	}
	for i, w := range prog {
		binary.LittleEndian.PutUint16(img[0x300+2*i:], w)
	}
	return img
}

func TestShellLoadBootAndState(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "game.iso")
	if err := os.WriteFile(p, shellISO(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if !loadPath(p) {
		t.Fatal("loadPath(.iso) returned false")
	}
	defer func() { sys = nil }()
	if sys == nil {
		t.Fatal("machine not set after load")
	}
	if !sys.Booted() {
		t.Error("machine not booted after shell load")
	}
	for i := 0; i < 3; i++ {
		runFrame()
	}
	if got := sys.Bus.Read32(0x0C200000); got != 0x600DCAFE {
		t.Errorf("guest magic = %#08x, want 0x600DCAFE", got)
	}
	if fb := sys.Framebuffer(); len(fb) != system.Width*system.Height {
		t.Errorf("framebuffer length = %d, want %d", len(fb), system.Width*system.Height)
	}

	// Memory regions: 16 MB system RAM, 128 KB VMU image.
	if got := retro_get_memory_size(memSystemRAM); int(got) != 16*1024*1024 {
		t.Errorf("SYSTEM_RAM size = %d, want %d", got, 16*1024*1024)
	}
	if got := retro_get_memory_size(memSaveRAM); int(got) != 128*1024 {
		t.Errorf("SAVE_RAM size = %d, want %d", got, 128*1024)
	}
	if retro_get_memory_data(memSystemRAM) == nil || retro_get_memory_data(memSaveRAM) == nil {
		t.Error("memory data pointer is nil for a documented region")
	}

	// Save state round trip through the machine the shell
	// wraps.
	saved := sys.Serialize()
	if len(saved) != sys.StateSize() {
		t.Fatalf("state length actual %d, want StateSize %d", len(saved), sys.StateSize())
	}
	m2 := system.New()
	if !m2.Deserialize(saved) {
		t.Fatal("Deserialize of the shell machine's state failed")
	}
	if again := m2.Serialize(); !bytes.Equal(saved, again) {
		t.Error("state bytes differ after a serialize/deserialize round trip")
	}

	// Reset reboots the same disc and the guest runs again.
	sys.Bus.Write32(0x0C200000, 0)
	retro_reset()
	for i := 0; i < 3; i++ {
		runFrame()
	}
	if got := sys.Bus.Read32(0x0C200000); got != 0x600DCAFE {
		t.Errorf("guest magic after reset = %#08x, want 0x600DCAFE", got)
	}
}

func TestShellRejects(t *testing.T) {
	dir := t.TempDir()
	// An unsupported extension.
	cue := filepath.Join(dir, "game.cue")
	if err := os.WriteFile(cue, []byte("FILE \"game.bin\" BINARY\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if loadPath(cue) {
		t.Error("loadPath(.cue) returned true, want false (no cue parser in this core)")
	}
	// A zeroed ISO: no SEGA header in the IP.BIN.
	zero := filepath.Join(dir, "zero.iso")
	if err := os.WriteFile(zero, make([]byte, 20*2048), 0o644); err != nil {
		t.Fatal(err)
	}
	if loadPath(zero) {
		t.Error("loadPath(zero.iso) returned true, want false")
	}
	// A missing file.
	if loadPath(filepath.Join(dir, "missing.gdi")) {
		t.Error("loadPath(missing.gdi) returned true, want false")
	}
	if sys != nil {
		t.Error("machine set after rejected loads")
	}
}
