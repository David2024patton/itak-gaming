package cart

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// syntheticZ64 builds a 1 MB .z64 image with known header
// fields and a deterministic pattern past the header.
func syntheticZ64() []byte {
	rom := make([]byte, 1<<20)
	binary.BigEndian.PutUint32(rom[0x00:], 0x80371240)
	binary.BigEndian.PutUint32(rom[0x04:], 0x0000000F) // clock rate
	binary.BigEndian.PutUint32(rom[0x08:], 0x80000400) // boot address
	binary.BigEndian.PutUint32(rom[0x0C:], 0x00001441) // release
	binary.BigEndian.PutUint32(rom[0x10:], 0xDEADBEEF) // CRC1
	binary.BigEndian.PutUint32(rom[0x14:], 0x12345678) // CRC2
	copy(rom[0x20:], "UNIT TEST KART      ")
	rom[0x3B] = 0x00 // media format (cartridge)
	rom[0x3C] = 0x00 // cartridge type
	rom[0x3E] = 0x45 // country: North America
	for i := 0x1000; i < len(rom); i++ {
		rom[i] = byte(i >> 4)
	}
	return rom
}

func TestParseZ64Header(t *testing.T) {
	c, err := Parse(syntheticZ64())
	if err != nil {
		t.Fatal(err)
	}
	if c.BootAddress != 0x80000400 {
		t.Errorf("boot address %08x", c.BootAddress)
	}
	if c.CRC1 != 0xDEADBEEF || c.CRC2 != 0x12345678 {
		t.Errorf("crcs %08x %08x", c.CRC1, c.CRC2)
	}
	if c.Name != "UNIT TEST KART" {
		t.Errorf("name %q", c.Name)
	}
	if c.Country != 0x45 {
		t.Errorf("country %02x", c.Country)
	}
	if c.CIC != CICUnknown {
		t.Errorf("cic %v, want unknown for synthetic boot area", c.CIC)
	}
}

func TestNormalizeV64AndN64(t *testing.T) {
	z := syntheticZ64()
	v := make([]byte, len(z))
	for i := 0; i < len(z); i += 2 {
		v[i], v[i+1] = z[i+1], z[i]
	}
	n := make([]byte, len(z))
	for i := 0; i < len(z); i += 4 {
		n[i], n[i+1], n[i+2], n[i+3] = z[i+3], z[i+2], z[i+1], z[i]
	}
	for name, img := range map[string][]byte{"v64": v, "n64": n} {
		c, err := Parse(img)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(c.ROM, z) {
			t.Errorf("%s: normalized image differs from z64", name)
		}
		if c.Name != "UNIT TEST KART" {
			t.Errorf("%s: name %q", name, c.Name)
		}
	}
}

func TestParseRejectsBadMagicAndSmall(t *testing.T) {
	if _, err := Parse(make([]byte, 0x2000)); err == nil {
		t.Error("zero image accepted")
	}
	if _, err := Parse([]byte{0x80, 0x37}); err == nil {
		t.Error("tiny image accepted")
	}
}
