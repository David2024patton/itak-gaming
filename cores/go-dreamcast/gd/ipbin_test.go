package gd

import (
	"bytes"
	"testing"
)

// Test (4): IP.BIN at the start of the data area of a synthetic .iso.
func TestIPBinFromImage(t *testing.T) {
	dir := t.TempDir()
	iso := buildTestISO()
	boot := buildTestBootArea()
	// The boot area occupies sectors 0 to 15; the ISO PVD at sector
	// 16 must survive, which the ISO checks below confirm.
	copy(iso[:0x8000], boot)
	path := writeTestFile(t, dir, "boot.iso", iso)

	im, err := Open(path)
	if err != nil {
		t.Fatalf("Open boot iso: %v", err)
	}
	defer im.Close()

	gotBoot, err := im.BootArea()
	if err != nil {
		t.Fatalf("BootArea: %v", err)
	}
	if len(gotBoot) != 0x8000 {
		t.Fatalf("BootArea is %d bytes, want 0x8000", len(gotBoot))
	}
	if !bytes.Equal(gotBoot, boot) {
		t.Errorf("BootArea bytes differ from crafted boot area")
	}

	ip, err := im.IPBin()
	if err != nil {
		t.Fatalf("IPBin: %v", err)
	}
	want := IPBin{
		HardwareID:   "SEGA SEGAKATANA",
		MakerID:      "TEST MAKER",
		DeviceInfo:   "GD-ROM1/1",
		AreaSymbols:  "JUE",
		Peripherals:  "E000F10",
		ProductNo:    "T-12345",
		Version:      "V1.000",
		ReleaseDate:  "20000101",
		BootFilename: "1ST_READ.BIN",
		CompanyName:  "TEST COMPANY",
		Title:        "MY TEST GAME",
	}
	if *ip != want {
		t.Errorf("IPBin mismatch:\n got %+v\nwant %+v", *ip, want)
	}

	// The ISO filesystem still parses with the boot area in place.
	checkISOContents(t, im)
}

func TestParseIPBinDirect(t *testing.T) {
	boot := buildTestBootArea()
	ip, err := ParseIPBin(boot)
	if err != nil {
		t.Fatalf("ParseIPBin: %v", err)
	}
	if ip.HardwareID != "SEGA SEGAKATANA" || ip.Title != "MY TEST GAME" {
		t.Errorf("ParseIPBin fields wrong: %+v", *ip)
	}
	// Only the first 0x100 bytes are required.
	ip2, err := ParseIPBin(boot[:0x100])
	if err != nil {
		t.Fatalf("ParseIPBin on exactly 0x100 bytes: %v", err)
	}
	if *ip2 != *ip {
		t.Errorf("ParseIPBin on 0x100 bytes differs: %+v vs %+v", *ip2, *ip)
	}
	// NUL padding must trim just like space padding.
	nulBoot := buildTestBootArea()
	for i := 0x80 + len("MY TEST GAME"); i < 0x100; i++ {
		nulBoot[i] = 0x00
	}
	ip3, err := ParseIPBin(nulBoot)
	if err != nil {
		t.Fatalf("ParseIPBin NUL padded: %v", err)
	}
	if ip3.Title != "MY TEST GAME" {
		t.Errorf("NUL padded title = %q, want %q", ip3.Title, "MY TEST GAME")
	}
}

func TestParseIPBinNegative(t *testing.T) {
	if _, err := ParseIPBin(make([]byte, 0x8000)); err == nil {
		t.Errorf("ParseIPBin on zero block: expected error, got nil")
	}
	garbage := bytes.Repeat([]byte("X"), 0x8000)
	if _, err := ParseIPBin(garbage); err == nil {
		t.Errorf("ParseIPBin on garbage block: expected error, got nil")
	}
	if _, err := ParseIPBin(make([]byte, 0x80)); err == nil {
		t.Errorf("ParseIPBin on short block: expected error, got nil")
	}

	// An image whose boot area is all zeros must fail IPBin().
	dir := t.TempDir()
	path := writeTestFile(t, dir, "plain.iso", buildTestISO())
	im, err := Open(path)
	if err != nil {
		t.Fatalf("Open plain iso: %v", err)
	}
	defer im.Close()
	if _, err := im.IPBin(); err == nil {
		t.Errorf("IPBin on zero boot area: expected error, got nil")
	}
}
