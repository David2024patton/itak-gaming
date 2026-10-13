package cart

import (
	"strings"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-snes/chips/w65c816"
)

// buildImage makes a synthetic ROM image of size bytes
// with a header written at headerOff. mutate adjusts the
// default header fields.
func buildImage(size, headerOff int, mutate func(b []byte)) []byte {
	img := make([]byte, size)
	b := img[headerOff : headerOff+0x40]
	copy(b, "SYNTHETIC TEST ROM   ")
	b[0x15] = 0x20 // LoROM, slow
	b[0x16] = 0x02 // ROM + RAM + battery
	b[0x17] = 0x09 // 512 KB declared (adjusted by caller as needed)
	b[0x18] = 0x03 // 8 KB SRAM
	b[0x19] = 0x01 // North America
	b[0x1A] = 0x01
	// Checksum and complement pair.
	b[0x1C], b[0x1D] = 0x55, 0xAA
	b[0x1E], b[0x1F] = 0xAA, 0x55
	// Reset vector to 0x8000, first byte SEI.
	b[0x3C], b[0x3D] = 0x00, 0x80
	img[0] = 0x78
	if size > 0x10000 {
		img[0x8000] = 0x78 // HiROM bank 0xC0 reset landing
	}
	if mutate != nil {
		mutate(img[headerOff : headerOff+0x40])
	}
	return img
}

func TestLoadLoROM(t *testing.T) {
	img := buildImage(0x80000, 0x7FC0, nil)
	c, err := Load(img)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Header.Layout != LayoutLoROM {
		t.Errorf("layout = %v, want LoROM", c.Header.Layout)
	}
	if c.Header.Title != "SYNTHETIC TEST ROM" {
		t.Errorf("title = %q", c.Header.Title)
	}
	if c.Header.RAMSize != 8192 {
		t.Errorf("RAMSize = %d, want 8192", c.Header.RAMSize)
	}
	if !c.Header.Battery {
		t.Errorf("battery not decoded")
	}
	if !c.Header.ChecksumOK {
		t.Errorf("checksum pair not decoded")
	}
	if c.Header.Vectors.EmuReset != 0x8000 {
		t.Errorf("reset vector = %#x", c.Header.Vectors.EmuReset)
	}
}

func TestLoadHiROM(t *testing.T) {
	img := buildImage(0x100000, 0xFFC0, func(b []byte) {
		b[0x15] = 0x21 // HiROM, slow
		b[0x17] = 0x0A // 1 MB
		// The LoROM candidate location must not win: leave
		// garbage at 0x7FC0 (zeros score nothing).
	})
	// The HiROM reset vector's landing byte lives at file
	// offset 0x8000 under the HiROM mirror; it is 0x78 in
	// the builder.
	c, err := Load(img)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Header.Layout != LayoutHiROM {
		t.Errorf("layout = %v, want HiROM", c.Header.Layout)
	}
}

func TestLoadFastROM(t *testing.T) {
	img := buildImage(0x80000, 0x7FC0, func(b []byte) {
		b[0x15] = 0x30 // LoROM, fast
	})
	c, err := Load(img)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.Header.FastROM {
		t.Errorf("FastROM not decoded")
	}
	m := NewMap(c)
	if got := m.Speed(0x80, 0x8000); got != w65c816.SpeedFast {
		t.Errorf("fast bank speed = %v, want SpeedFast", got)
	}
	if got := m.Speed(0x00, 0x8000); got != w65c816.SpeedSlow {
		t.Errorf("low bank speed = %v, want SpeedSlow", got)
	}
}

func TestLoadRejectsCoprocessors(t *testing.T) {
	cases := []struct {
		name    string
		chipset uint8
		want    string
	}{
		{"SA-1", 0x34, "SA-1"},
		{"SuperFX", 0x15, "SuperFX"},
		{"DSP", 0x03, "DSP"},
		{"S-DD1", 0x45, "S-DD1"},
		{"SPC7110", 0xF5, "SPC7110"},
		{"Cx4", 0xF9, "Cx4"},
	}
	for _, tc := range cases {
		img := buildImage(0x80000, 0x7FC0, func(b []byte) {
			b[0x16] = tc.chipset
			if tc.chipset == 0xF5 {
				b[0x15] = 0x25 // ExHiROM map mode marks SPC7110
			}
		})
		_, err := Load(img)
		if err == nil {
			t.Errorf("%s: Load succeeded, want refusal", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not name %q", tc.name, err, tc.want)
		}
	}
}

func TestLoadRejectsGarbage(t *testing.T) {
	img := make([]byte, 0x80000)
	if _, err := Load(img); err == nil {
		t.Errorf("zero image loaded, want error")
	}
	if _, err := Load([]byte{1, 2, 3}); err == nil {
		t.Errorf("tiny image loaded, want error")
	}
}

func TestCopierHeaderStripped(t *testing.T) {
	base := buildImage(0x80000, 0x7FC0, nil)
	img := make([]byte, 0, len(base)+512)
	img = append(img, make([]byte, 512)...)
	img = append(img, base...)
	c, err := Load(img)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.ROM()) != len(base) {
		t.Errorf("ROM size = %d, want %d", len(c.ROM()), len(base))
	}
}

func TestLoROMMap(t *testing.T) {
	img := buildImage(0x80000, 0x7FC0, nil)
	// Give pages recognizable contents: page p starts with
	// byte p.
	for p := 0; p < 16; p++ {
		img[p*0x8000] = byte(p)
	}
	img[0] = 0x78 // restore reset landing byte
	c, _ := Load(img)
	m := NewMap(c)
	checks := []struct {
		bank uint8
		addr uint16
		want uint8
	}{
		{0x00, 0x8000, 0x78}, // page 0 starts with the SEI reset landing byte
		{0x01, 0x8000, 1},    // page 1
		{0x80, 0x8000, 0x78}, // mirror
		{0x81, 0x8000, 1},
		{0x02, 0xFFFF, 0}, // inside page 2: byte value unmarked (0)
	}
	for _, ch := range checks {
		if got := m.Read(ch.bank, ch.addr); got != ch.want {
			t.Errorf("Read(%02x:%04x) = %d, want %d", ch.bank, ch.addr, got, ch.want)
		}
	}
	// Work RAM mirror and bank 7E/7F agree.
	m.Write(0x00, 0x0010, 0xAB)
	if got := m.Read(0x7E, 0x0010); got != 0xAB {
		t.Errorf("WRAM mirror read = %#x", got)
	}
	m.Write(0x7F, 0x1234, 0xCD)
	if got := m.Read(0x7F, 0x1234); got != 0xCD {
		t.Errorf("WRAM high bank read = %#x", got)
	}
	// SRAM window at 0x70:0000.
	m.Write(0x70, 0x0000, 0x5A)
	if got := m.Read(0x70, 0x0000); got != 0x5A {
		t.Errorf("SRAM read = %#x", got)
	}
	if len(m.SRAM()) != 8192 {
		t.Errorf("SRAM size = %d", len(m.SRAM()))
	}
	// IO region reads open bus (SNES-1 placeholder).
	if got := m.Read(0x00, 0x2100); got != m.OpenBus {
		t.Errorf("IO read = %#x, want open bus", got)
	}
	if got := m.Speed(0x00, 0x2100); got != w65c816.SpeedXSlow {
		t.Errorf("IO speed = %v, want SpeedXSlow", got)
	}
}

func TestHiROMMap(t *testing.T) {
	img := buildImage(0x100000, 0xFFC0, func(b []byte) {
		b[0x15] = 0x21
		b[0x17] = 0x0A
	})
	for p := 0; p < 16; p++ {
		img[p*0x10000] = byte(p + 1)
	}
	img[0x8000] = 0x78
	c, err := Load(img)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m := NewMap(c)
	if got := m.Read(0xC0, 0x0000); got != 1 {
		t.Errorf("C0:0000 = %d, want 1", got)
	}
	if got := m.Read(0xC1, 0x0000); got != 2 {
		t.Errorf("C1:0000 = %d, want 2", got)
	}
	if got := m.Read(0x00, 0x8000); got != img[0x8000] {
		t.Errorf("00:8000 mirror = %#x, want %#x", got, img[0x8000])
	}
	// HiROM SRAM window at 0x20:6000.
	m.Write(0x20, 0x6000, 0x77)
	if got := m.Read(0x20, 0x6000); got != 0x77 {
		t.Errorf("HiROM SRAM read = %#x", got)
	}
	if got := m.Read(0xA0, 0x6000); got != 0x77 {
		t.Errorf("HiROM SRAM mirror read = %#x", got)
	}
}
