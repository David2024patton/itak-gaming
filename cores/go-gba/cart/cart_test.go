package cart

import (
	"encoding/binary"
	"strings"
	"testing"
)

// synthetic builds a minimal valid image of the given
// size with a proper header; fixups sets fields before
// the checksum is computed, and a non-nil checksum
// override writes that byte at 0xBD instead.
func synthetic(size int, fixups func(rom []byte), checksumOverride *uint8) []byte {
	rom := make([]byte, size)
	// Entry: an ARM branch to the code offset past the
	// header, the shape a real image starts with.
	binary.LittleEndian.PutUint32(rom[0x00:], 0xEA00002E)
	copy(rom[0xA0:], "TESTGAME")
	copy(rom[0xAC:], "ATGE")
	copy(rom[0xB0:], "01")
	rom[0xB2] = 0x96
	rom[0xBC] = 0x03
	if fixups != nil {
		fixups(rom)
	}
	chk := headerChecksum(rom)
	if checksumOverride != nil {
		chk = *checksumOverride
	}
	rom[0xBD] = chk
	return rom
}

func TestParseValid(t *testing.T) {
	rom := synthetic(0x4000, nil, nil)
	c, err := Parse(rom)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Title != "TESTGAME" {
		t.Errorf("Title = %q, want TESTGAME", c.Title)
	}
	if c.GameCode != "ATGE" {
		t.Errorf("GameCode = %q, want ATGE", c.GameCode)
	}
	if c.MakerCode != "01" {
		t.Errorf("MakerCode = %q, want 01", c.MakerCode)
	}
	if c.Version != 0x03 {
		t.Errorf("Version = %#02x, want 0x03", c.Version)
	}
	if c.EntryPoint != 0xEA00002E {
		t.Errorf("EntryPoint = %#08x, want 0xEA00002E", c.EntryPoint)
	}
	if !c.HeaderChecksumValid {
		t.Error("HeaderChecksumValid = false on a checksummed header")
	}
	if c.HeaderChecksum != rom[0xBD] {
		t.Errorf("HeaderChecksum = %#02x, want %#02x", c.HeaderChecksum, rom[0xBD])
	}
	if c.ROMSize != 0x4000 {
		t.Errorf("ROMSize = %#x, want 0x4000", c.ROMSize)
	}
	if c.SaveKind != SaveNone || c.SaveType != "" {
		t.Errorf("Save = %v %q, want none", c.SaveKind, c.SaveType)
	}
	if len(c.ROM) != len(rom) {
		t.Errorf("ROM holds %d bytes, want %d", len(c.ROM), len(rom))
	}
}

func TestParseTitleTrimming(t *testing.T) {
	rom := synthetic(0x400, func(r []byte) {
		copy(r[0xA0:], "AB CD\x00\x00\x00\x00\x00\x00\x00")
	}, nil)
	c, err := Parse(rom)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Title != "AB CD" {
		t.Errorf("Title = %q, want %q", c.Title, "AB CD")
	}
}

func TestParseBadFixedByte(t *testing.T) {
	rom := synthetic(0x400, func(r []byte) { r[0xB2] = 0x95 }, nil)
	if _, err := Parse(rom); err == nil {
		t.Fatal("Parse accepted fixed byte 0x95 at 0xB2")
	}
}

func TestParseTooSmall(t *testing.T) {
	for _, size := range []int{0, 16, 0x80, 0xBF} {
		if _, err := Parse(make([]byte, size)); err == nil {
			t.Errorf("Parse accepted a %d byte image", size)
		}
	}
}

func TestParseChecksumWrong(t *testing.T) {
	bad := uint8(0x00)
	rom := synthetic(0x400, nil, &bad)
	// Make sure the override really is wrong for this
	// header before trusting the test.
	if headerChecksum(rom) == bad {
		t.Fatal("test setup: override checksum accidentally valid")
	}
	c, err := Parse(rom)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.HeaderChecksumValid {
		t.Error("HeaderChecksumValid = true on a bad checksum")
	}
	if c.HeaderChecksum != bad {
		t.Errorf("HeaderChecksum = %#02x, want %#02x", c.HeaderChecksum, bad)
	}
}

func TestROMSizeRounding(t *testing.T) {
	cases := []struct {
		size int
		want int
	}{
		{0xC0, 0x100},
		{0x100, 0x100},
		{0x101, 0x200},
		{0x12345, 0x20000},
		{32 * 1024 * 1024, 32 * 1024 * 1024},
		{33 * 1024 * 1024, 32 * 1024 * 1024}, // capped at the 32 MB window
	}
	for _, tc := range cases {
		c, err := Parse(synthetic(tc.size, nil, nil))
		if err != nil {
			t.Fatalf("Parse(%d bytes): %v", tc.size, err)
		}
		if c.ROMSize != tc.want {
			t.Errorf("ROMSize for %d bytes = %#x, want %#x", tc.size, c.ROMSize, tc.want)
		}
	}
}

func TestSaveSignatures(t *testing.T) {
	cases := []struct {
		name string
		sig  string
		kind SaveKind
		want string
	}{
		{"SRAM", "SRAM_V110", SaveSRAM, "SRAM_V110"},
		{"FLASH", "FLASH_V126", SaveFlash, "FLASH_V126"},
		{"FLASH512", "FLASH512_V131", SaveFlash512, "FLASH512_V131"},
		{"FLASH1M", "FLASH1M_V103", SaveFlash1M, "FLASH1M_V103"},
		{"EEPROM", "EEPROM_V124", SaveEEPROM, "EEPROM_V124"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rom := synthetic(0x1000, func(r []byte) {
				copy(r[0x800:], tc.sig)
				copy(r[0x800+len(tc.sig):], "\x00\xff not digits")
			}, nil)
			c, err := Parse(rom)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if c.SaveKind != tc.kind {
				t.Errorf("SaveKind = %v, want %v", c.SaveKind, tc.kind)
			}
			if c.SaveType != tc.want {
				t.Errorf("SaveType = %q, want %q", c.SaveType, tc.want)
			}
		})
	}
}

// TestSaveSignatureLongestWins puts the FLASH1M signature
// in an image that also carries a bare FLASH_V string, in
// both orders: the longer signature must win either way,
// because scanning checks longer signatures first.
func TestSaveSignatureLongestWins(t *testing.T) {
	for _, order := range []string{"flash-first", "flash1m-first"} {
		rom := synthetic(0x2000, func(r []byte) {
			if order == "flash-first" {
				copy(r[0x800:], "FLASH_V126")
				copy(r[0x1000:], "FLASH1M_V103")
			} else {
				copy(r[0x800:], "FLASH1M_V103")
				copy(r[0x1000:], "FLASH_V126")
			}
		}, nil)
		c, err := Parse(rom)
		if err != nil {
			t.Fatalf("%s: Parse: %v", order, err)
		}
		if c.SaveKind != SaveFlash1M || c.SaveType != "FLASH1M_V103" {
			t.Errorf("%s: Save = %v %q, want FLASH1M FLASH1M_V103", order, c.SaveKind, c.SaveType)
		}
		if strings.Contains(c.SaveType, "FLASH_V") && c.SaveKind == SaveFlash {
			t.Errorf("%s: bare FLASH signature won over FLASH1M", order)
		}
	}
	// Same check for FLASH512 against FLASH.
	rom := synthetic(0x2000, func(r []byte) {
		copy(r[0x800:], "FLASH_V126")
		copy(r[0x1000:], "FLASH512_V131")
	}, nil)
	c, err := Parse(rom)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.SaveKind != SaveFlash512 || c.SaveType != "FLASH512_V131" {
		t.Errorf("Save = %v %q, want FLASH512 FLASH512_V131", c.SaveKind, c.SaveType)
	}
}
