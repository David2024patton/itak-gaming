package core

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	ps1homebrew "github.com/davidpatton/itak-gaming/cores/go-ps1/homebrew"
)

// Boot smoke tests for every native adapter: each core loads a
// minimal synthetic game through the production entry points
// (LoadNative by core file name, then NativeBridge.LoadGame on
// a real file), runs frames in this process, produces a frame
// of the expected shape, and round trips a save state. The
// fixtures mirror each core module's own test fixtures.

func writeGame(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func bootCore(t *testing.T, coreFile, gameName string, data []byte) *NativeBridge {
	t.Helper()
	nb, ok := LoadNative("/data/data/net.itak.itak_gaming/files/cores/" + coreFile)
	if !ok {
		t.Fatalf("LoadNative did not recognize %s", coreFile)
	}
	if err := nb.LoadGame(writeGame(t, gameName, data)); err != nil {
		t.Fatalf("%s LoadGame: %v", coreFile, err)
	}
	return nb
}

func runAndCheck(t *testing.T, nb *NativeBridge, frames, wantW, wantH int) {
	t.Helper()
	for i := 0; i < frames; i++ {
		nb.RunFrame()
	}
	w, h, pitch, data := nb.GetFrame()
	if w != wantW || h != wantH {
		t.Fatalf("frame geometry = %dx%d, want %dx%d", w, h, wantW, wantH)
	}
	if len(data) != h*pitch || pitch < w {
		t.Fatalf("frame pitch/len = %d/%d for %dx%d", pitch, len(data), w, h)
	}
	if _, _, _, err := nb.FrameToRGBA(); err != nil {
		t.Fatalf("FrameToRGBA: %v", err)
	}
	_ = nb.DrainAudio(4096)
	state, err := nb.SaveState()
	if err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if len(state) == 0 {
		t.Fatal("empty save state")
	}
	if err := nb.LoadState(state); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for i := 0; i < 10; i++ {
		nb.RunFrame()
	}
}

func TestNativeRegistryHasAllSix(t *testing.T) {
	for _, name := range []string{
		"libgo_gameboy.so", "libgo_nes.so", "libgo_snes.so",
		"libgo_genesis.so", "libgo_gba.so", "libgo_ps1.so",
	} {
		nb, ok := LoadNative("/x/" + name)
		if !ok {
			t.Fatalf("registry missing %s", name)
		}
		nb.Close()
	}
}

// gbROM mirrors go-gameboy's cart test fixture: 32 KB ROM-only
// cartridge with the boot logo, header checksum, and a spin
// loop at the entry point.
func gbROM() []byte {
	data := make([]byte, 32768)
	logo := [48]byte{
		0xCE, 0xED, 0x66, 0x66, 0xCC, 0x0D, 0x00, 0x0B, 0x03, 0x73, 0x00, 0x83,
		0x00, 0x0C, 0x00, 0x0D, 0x00, 0x08, 0x11, 0x1F, 0x88, 0x89, 0x00, 0x0E,
		0xDC, 0xCC, 0x6E, 0xE6, 0xDD, 0xDD, 0xD9, 0x99, 0xBB, 0xBB, 0x67, 0x63,
		0x6E, 0x0E, 0xEC, 0xCC, 0xDD, 0xDC, 0x99, 0x9F, 0xBB, 0xB9, 0x33, 0x3E,
	}
	copy(data[0x104:0x134], logo[:])
	copy(data[0x134:], "TESTCART")
	data[0x100] = 0x00 // nop
	data[0x101] = 0xC3 // jp 0x0150
	data[0x102] = 0x50 //
	data[0x103] = 0x01 //
	data[0x150] = 0x18 // jr -2 (spin)
	data[0x151] = 0xFE //
	data[0x147] = 0x00 // ROM only
	data[0x148] = 0x00 // 32 KB
	data[0x149] = 0x00 // no RAM
	var chk uint8
	for i := 0x134; i <= 0x14C; i++ {
		chk = chk - data[i] - 1
	}
	data[0x14D] = chk
	return data
}

func TestNativeGameBoyBoots(t *testing.T) {
	nb := bootCore(t, "libgo_gameboy.so", "spin.gb", gbROM())
	defer nb.Close()
	runAndCheck(t, nb, 60, 160, 144)
}

// nesROM mirrors go-nes's system test fixture: NROM with a
// JMP $8000 self loop and the reset vector at $8000.
func nesROM() []byte {
	img := []byte{'N', 'E', 'S', 0x1A, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	prg := make([]byte, 16384)
	copy(prg, []byte{0x4C, 0x00, 0x80}) // JMP $8000
	prg[0x3FFC] = 0x00
	prg[0x3FFD] = 0x80
	img = append(img, prg...)
	img = append(img, make([]byte, 8192)...)
	return img
}

func TestNativeNESBoots(t *testing.T) {
	nb := bootCore(t, "libgo_nes.so", "spin.nes", nesROM())
	defer nb.Close()
	runAndCheck(t, nb, 60, 256, 240)
}

// genesisROM mirrors go-genesis's system test fixture shape:
// stack and reset vectors, the SEGA header string, and a
// bra.s self loop at the reset target.
func genesisROM() []byte {
	rom := make([]byte, 0x10000)
	binary.BigEndian.PutUint32(rom[0:], 0x00FFFE00)
	binary.BigEndian.PutUint32(rom[4:], 0x00000200)
	copy(rom[0x100:], "SEGA GENESIS    ")
	rom[0x200] = 0x60 // bra.s *
	rom[0x201] = 0xFE
	return rom
}

func TestNativeGenesisBoots(t *testing.T) {
	nb := bootCore(t, "libgo_genesis.so", "spin.bin", genesisROM())
	defer nb.Close()
	// The fixture never programs the VDP, so the machine stays
	// in its power-on H32 mode: 256 wide. The adapter reports
	// the live VDP size, like the shell.
	runAndCheck(t, nb, 60, 256, 224)
}

// A Genesis CD load for a missing disc must fail cleanly,
// not panic.
func TestNativeGenesisMissingDisc(t *testing.T) {
	nb, ok := LoadNative("/x/libgo_genesis.so")
	if !ok {
		t.Fatal("no genesis adapter")
	}
	defer nb.Close()
	missing := filepath.Join(t.TempDir(), "nope.cue")
	if err := os.WriteFile(missing, []byte("FILE \"nope.bin\" BINARY\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := nb.LoadGame(missing); err == nil {
		t.Fatal("LoadGame on a cue with a missing bin should error")
	}
}

// gbaROM is a minimal GBA image: an ARM branch-to-self at the
// entry point and the fixed header byte at 0xB2 (the only two
// things cart.Parse enforces besides size).
func gbaROM() []byte {
	rom := make([]byte, 32768)
	binary.LittleEndian.PutUint32(rom[0:], 0xEAFFFFFE) // b .
	rom[0xB2] = 0x96
	return rom
}

func TestNativeGBABoots(t *testing.T) {
	nb := bootCore(t, "libgo_gba.so", "spin.gba", gbaROM())
	defer nb.Close()
	runAndCheck(t, nb, 60, 240, 160)
}

// ps1EXE builds a PS-X EXE whose text is a MIPS branch-to-self
// loop, using the core module's own homebrew fixture builder.
func ps1EXE() []byte {
	return ps1homebrew.BuildEXE([]uint32{0x1000FFFF}) // beq $zero, $zero, -1
}

func TestNativePS1EXEBoots(t *testing.T) {
	nb := bootCore(t, "libgo_ps1.so", "spin.exe", ps1EXE())
	defer nb.Close()
	// The EXE never programs the GPU, so the display stays at
	// the power-on mode: 256x240. The adapter reads the live
	// display size on every frame, like the shell.
	runAndCheck(t, nb, 60, 256, 240)
}

// Garbage content must be rejected, not crash, for the
// cartridge cores.
func TestNativeGarbageRejected(t *testing.T) {
	junk := make([]byte, 100)
	for _, tc := range []struct{ core, name string }{
		{"libgo_gameboy.so", "junk.gb"},
		{"libgo_nes.so", "junk.nes"},
		{"libgo_gba.so", "junk.gba"},
		{"libgo_ps1.so", "junk.exe"},
	} {
		nb, ok := LoadNative("/x/" + tc.core)
		if !ok {
			t.Fatalf("no adapter for %s", tc.core)
		}
		if err := nb.LoadGame(writeGame(t, tc.name, junk)); err == nil {
			t.Fatalf("%s accepted a 100-byte garbage file", tc.core)
		}
		nb.Close()
	}
}
