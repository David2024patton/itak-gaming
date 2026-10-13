package gd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func checkISOContents(t *testing.T, im *Image) {
	t.Helper()

	root, err := im.ISOListDir("")
	if err != nil {
		t.Fatalf("ISOListDir root: %v", err)
	}
	if len(root) != 2 {
		t.Fatalf("root listing has %d entries, want 2: %+v", len(root), root)
	}
	byName := make(map[string]ISOEntry)
	for _, e := range root {
		byName[e.Name] = e
	}
	hello, ok := byName["HELLO.TXT"]
	if !ok {
		t.Fatalf("root listing missing HELLO.TXT: %+v", root)
	}
	if hello.IsDir || hello.Size != len(testHelloContent) {
		t.Errorf("HELLO.TXT entry = %+v, want file of size %d", hello, len(testHelloContent))
	}
	dataEntry, ok := byName["DATA"]
	if !ok {
		t.Fatalf("root listing missing DATA: %+v", root)
	}
	if !dataEntry.IsDir {
		t.Errorf("DATA entry = %+v, want directory", dataEntry)
	}

	// Root via "/" must match root via "".
	rootSlash, err := im.ISOListDir("/")
	if err != nil {
		t.Fatalf("ISOListDir /: %v", err)
	}
	if len(rootSlash) != len(root) {
		t.Errorf("ISOListDir / returned %d entries, want %d", len(rootSlash), len(root))
	}

	sub, err := im.ISOListDir("DATA")
	if err != nil {
		t.Fatalf("ISOListDir DATA: %v", err)
	}
	if len(sub) != 1 || sub[0].Name != "WORLD.BIN" || sub[0].IsDir || sub[0].Size != len(testWorldContent) {
		t.Fatalf("DATA listing = %+v, want one WORLD.BIN file of size %d", sub, len(testWorldContent))
	}

	gotHello, err := im.ISOReadFile("HELLO.TXT")
	if err != nil {
		t.Fatalf("ISOReadFile HELLO.TXT: %v", err)
	}
	if !bytes.Equal(gotHello, testHelloContent) {
		t.Errorf("HELLO.TXT bytes differ:\n got %q\nwant %q", gotHello, testHelloContent)
	}

	// Case-insensitive lookup with a version suffix.
	gotHelloLower, err := im.ISOReadFile("hello.txt;1")
	if err != nil {
		t.Fatalf("ISOReadFile hello.txt;1: %v", err)
	}
	if !bytes.Equal(gotHelloLower, testHelloContent) {
		t.Errorf("hello.txt;1 bytes differ from HELLO.TXT content")
	}

	gotWorld, err := im.ISOReadFile("DATA/WORLD.BIN")
	if err != nil {
		t.Fatalf("ISOReadFile DATA/WORLD.BIN: %v", err)
	}
	if !bytes.Equal(gotWorld, testWorldContent) {
		t.Errorf("WORLD.BIN bytes differ: got %d bytes, want %d", len(gotWorld), len(testWorldContent))
	}

	gotWorldLower, err := im.ISOReadFile("data/world.bin")
	if err != nil {
		t.Fatalf("ISOReadFile data/world.bin: %v", err)
	}
	if !bytes.Equal(gotWorldLower, testWorldContent) {
		t.Errorf("data/world.bin bytes differ from WORLD.BIN content")
	}

	if _, err := im.ISOReadFile("NOPE.TXT"); err == nil {
		t.Errorf("ISOReadFile NOPE.TXT: expected error, got nil")
	}
	if _, err := im.ISOReadFile("DATA"); err == nil {
		t.Errorf("ISOReadFile DATA (a directory): expected error, got nil")
	}
	if _, err := im.ISOListDir("HELLO.TXT"); err == nil {
		t.Errorf("ISOListDir HELLO.TXT (a file): expected error, got nil")
	}
}

// Test (1): synthetic ISO written as a .iso file.
func TestISOImage(t *testing.T) {
	dir := t.TempDir()
	iso := buildTestISO()
	path := writeTestFile(t, dir, "test.iso", iso)

	im, err := Open(path)
	if err != nil {
		t.Fatalf("Open iso: %v", err)
	}
	defer im.Close()

	dt := im.DataTrack()
	if dt == nil {
		t.Fatal("DataTrack is nil for .iso")
	}
	if dt.SectorSize != 2048 || dt.Ctrl != 4 || dt.LBA != 0 || dt.File != "" {
		t.Errorf("data track = %+v, want LBA 0 ctrl 4 sector size 2048 empty file", dt)
	}

	sec, err := im.ReadSector(16)
	if err != nil {
		t.Fatalf("ReadSector 16: %v", err)
	}
	if !bytes.Equal(sec, iso[16*2048:17*2048]) {
		t.Errorf("sector 16 differs from source ISO sector")
	}
	if len(sec) != 2048 {
		t.Errorf("ReadSector returned %d bytes, want 2048", len(sec))
	}
	if _, err := im.ReadSector(testTotalSectors + 10); err == nil {
		t.Errorf("ReadSector past end: expected error, got nil")
	}

	checkISOContents(t, im)
}

// Test (2): the same ISO sectors wrapped in raw 2352 byte sectors.
func TestRawBinImage(t *testing.T) {
	dir := t.TempDir()
	iso := buildTestISO()
	raw := wrapRaw2352(iso)
	if len(raw) != testTotalSectors*2352 {
		t.Fatalf("raw image is %d bytes, want %d", len(raw), testTotalSectors*2352)
	}
	path := writeTestFile(t, dir, "test.bin", raw)

	im, err := Open(path)
	if err != nil {
		t.Fatalf("Open bin: %v", err)
	}
	defer im.Close()

	dt := im.DataTrack()
	if dt == nil || dt.SectorSize != 2352 {
		t.Fatalf("data track = %+v, want sector size 2352", dt)
	}
	for _, lba := range []int{0, 16, testRootExtent, testHelloExtent, testWorldExtent} {
		got, err := im.ReadSector(lba)
		if err != nil {
			t.Fatalf("ReadSector %d: %v", lba, err)
		}
		want := iso[lba*2048 : (lba+1)*2048]
		if !bytes.Equal(got, want) {
			t.Errorf("raw sector %d user data differs from ISO sector", lba)
		}
	}
	checkISOContents(t, im)
}

// Test: 2336 byte sectors, reachable only through a .gdi track list.
func TestSector2336ViaGDI(t *testing.T) {
	dir := t.TempDir()
	data := wrap2336(buildTestISO())
	writeTestFile(t, dir, "data2336.bin", data)
	gdiText := "1\n1 0 4 2336 \"data2336.bin\" 0\n"
	gdiPath := writeTestFile(t, dir, "test.gdi", []byte(gdiText))

	im, err := Open(gdiPath)
	if err != nil {
		t.Fatalf("Open gdi 2336: %v", err)
	}
	defer im.Close()
	if dt := im.DataTrack(); dt == nil || dt.SectorSize != 2336 {
		t.Fatalf("data track = %+v, want sector size 2336", dt)
	}
	checkISOContents(t, im)
}

func TestMSFToLBA(t *testing.T) {
	cases := []struct {
		m, s, f, want int
	}{
		{0, 2, 0, 0},
		{1, 0, 0, 4350},
		{0, 2, 1, 1},
		{2, 0, 0, 8850},
	}
	for _, c := range cases {
		if got := msfToLBA(c.m, c.s, c.f); got != c.want {
			t.Errorf("msfToLBA(%d, %d, %d) = %d, want %d", c.m, c.s, c.f, got, c.want)
		}
	}
}

func TestOpenUnsupported(t *testing.T) {
	dir := t.TempDir()
	cdi := writeTestFile(t, dir, "game.cdi", []byte("not a real cdi"))
	if _, err := Open(cdi); err == nil {
		t.Errorf("Open .cdi: expected unsupported error, got nil")
	} else if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("Open .cdi error %q does not say not supported", err)
	}
	xyz := writeTestFile(t, dir, "game.xyz", []byte("junk"))
	if _, err := Open(xyz); err == nil {
		t.Errorf("Open .xyz: expected error, got nil")
	}
	if _, err := Open(filepath.Join(dir, "missing.iso")); err == nil {
		t.Errorf("Open missing file: expected error, got nil")
	}
}
