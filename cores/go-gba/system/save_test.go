package system

import (
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-gba/cart"
	"github.com/davidpatton/itak-gaming/cores/go-gba/save"
)

// GBA-4 gates for the cartridge save devices: synthetic
// ROMs whose save signature strings activate each device,
// exercised through the machine's bus windows, plus the
// SaveRAM frontend surface and the device block inside
// save states.

// saveSigROM builds a minimal cartridge image carrying
// the given save signature string, in the scene ROM
// style: a branch at the entry point (never run by these
// tests, which drive the bus directly) and the fixed
// header byte set.
func saveSigROM(sig string) []byte {
	rom := make([]byte, 0x4000)
	rom[0x00] = 0xFE
	rom[0x01] = 0xFF
	rom[0x02] = 0xFF
	rom[0x03] = 0xEA // B .
	rom[0xB2] = 0x96
	copy(rom[0x200:], sig)
	return rom
}

func loadSaveROM(t *testing.T, sig string) *Machine {
	t.Helper()
	m := New()
	if err := m.LoadCart(saveSigROM(sig)); err != nil {
		t.Fatalf("LoadCart(%q): %v", sig, err)
	}
	return m
}

func TestSaveKindActivation(t *testing.T) {
	cases := []struct {
		sig  string
		kind cart.SaveKind
	}{
		{"SRAM_V110", cart.SaveSRAM},
		{"FLASH_V103", cart.SaveFlash},
		{"FLASH512_V101", cart.SaveFlash512},
		{"FLASH1M_V103", cart.SaveFlash1M},
		{"EEPROM_V122", cart.SaveEEPROM},
	}
	for _, c := range cases {
		m := loadSaveROM(t, c.sig)
		if m.Cart().SaveKind != c.kind {
			t.Fatalf("%s detected as %v, want %v", c.sig, m.Cart().SaveKind, c.kind)
		}
	}
}

func TestSRAMWindowMirrorAndSaveRAM(t *testing.T) {
	m := loadSaveROM(t, "SRAM_V110")
	b := m.Bus()
	b.Write(0x0E001234, 1, 0x5A)
	if got := b.Read(0x0E001234, 1); got != 0x5A {
		t.Fatalf("SRAM read back = %#02x, want 0x5A", got)
	}
	// The 32 KB part mirrors at +32 KB.
	if got := b.Read(0x0E009234, 1); got != 0x5A {
		t.Fatalf("SRAM mirror read = %#02x, want 0x5A", got)
	}
	sr := m.SaveRAM()
	if len(sr) != save.SRAMSize {
		t.Fatalf("SaveRAM len = %d, want %d", len(sr), save.SRAMSize)
	}
	if sr[0x1234] != 0x5A {
		t.Fatalf("SaveRAM[0x1234] = %#02x, want 0x5A", sr[0x1234])
	}
	// Round trip into a fresh machine.
	m2 := loadSaveROM(t, "SRAM_V110")
	m2.SetSaveRAM(sr)
	if got := m2.Bus().Read(0x0E001234, 1); got != 0x5A {
		t.Fatalf("restored SRAM byte = %#02x, want 0x5A", got)
	}
}

func TestFlashThroughBus(t *testing.T) {
	m := loadSaveROM(t, "FLASH_V103")
	b := m.Bus()
	w := func(off uint32, v byte) { b.Write(0x0E000000+off, 1, uint32(v)) }
	r := func(off uint32) byte { return byte(b.Read(0x0E000000+off, 1)) }
	w(0x5555, 0xAA)
	w(0x2AAA, 0x55)
	w(0x5555, 0x90)
	if got := r(0); got != 0xC2 {
		t.Fatalf("flash ID manufacturer via bus = %#02x, want 0xC2", got)
	}
	if got := r(1); got != 0x1C {
		t.Fatalf("flash ID device via bus = %#02x, want 0x1C", got)
	}
	w(0x5555, 0xF0)
	w(0x5555, 0xAA)
	w(0x2AAA, 0x55)
	w(0x5555, 0xA0)
	w(0x0200, 0x66)
	if got := r(0x0200); got != 0x66 {
		t.Fatalf("programmed byte via bus = %#02x, want 0x66", got)
	}
	// The bus SRAM store must NOT have taken the writes:
	// the flash hook claimed the window.
	if got := b.SRAM[0x0200]; got != 0 {
		t.Fatalf("bus SRAM store polluted by flash traffic: %#02x", got)
	}
	if got := len(m.SaveRAM()); got != save.Flash64Size {
		t.Fatalf("SaveRAM len = %d, want %d", got, save.Flash64Size)
	}
	if got := m.SaveRAM()[0x0200]; got != 0x66 {
		t.Fatalf("SaveRAM[0x200] = %#02x, want 0x66", got)
	}
}

func TestFlash1MThroughBus(t *testing.T) {
	m := loadSaveROM(t, "FLASH1M_V103")
	if got := len(m.SaveRAM()); got != save.Flash128Size {
		t.Fatalf("SaveRAM len = %d, want %d", got, save.Flash128Size)
	}
	b := m.Bus()
	w := func(off uint32, v byte) { b.Write(0x0E000000+off, 1, uint32(v)) }
	cmd := func(c byte) {
		w(0x5555, 0xAA)
		w(0x2AAA, 0x55)
		w(0x5555, c)
	}
	cmd(0xA0)
	w(0x0010, 0x0B) // bank 0
	cmd(0xB0)
	w(0x0000, 0x01) // bank 1
	cmd(0xA0)
	w(0x0010, 0x0E) // bank 1
	sr := m.SaveRAM()
	if sr[0x0010] != 0x0B || sr[0x10010] != 0x0E {
		t.Fatalf("SaveRAM banks = %#02x/%#02x, want 0x0B/0x0E", sr[0x0010], sr[0x10010])
	}
}

// eeBusWriteBlock / eeBusReadBlock drive the EEPROM bit
// protocol through the machine bus with halfword
// accesses at 0x0D000000, the way games do it (usually
// via DMA; one bus access per bit either way).
func eeBusWriteBlock(t *testing.T, m *Machine, addr uint32, addrBits int, value uint64) {
	t.Helper()
	b := m.Bus()
	w := func(bit uint32) { b.Write(0x0D000000, 2, bit) }
	w(1)
	w(0)
	for i := addrBits - 1; i >= 0; i-- {
		w((addr >> uint(i)) & 1)
	}
	for i := 63; i >= 0; i-- {
		w(uint32(value>>uint(i)) & 1)
	}
	w(0) // stop
	if got := b.Read(0x0D000000, 2) & 1; got != 1 {
		t.Fatalf("EEPROM ready poll = %d, want 1", got)
	}
}

func eeBusReadBlock(t *testing.T, m *Machine, addr uint32, addrBits int) uint64 {
	t.Helper()
	b := m.Bus()
	w := func(bit uint32) { b.Write(0x0D000000, 2, bit) }
	w(1)
	w(1)
	for i := addrBits - 1; i >= 0; i-- {
		w((addr >> uint(i)) & 1)
	}
	w(0) // stop
	for i := 0; i < 4; i++ {
		if got := b.Read(0x0D000000, 2) & 1; got != 0 {
			t.Fatalf("EEPROM dummy bit %d = %d, want 0", i, got)
		}
	}
	var v uint64
	for i := 0; i < 64; i++ {
		v = v<<1 | uint64(b.Read(0x0D000000, 2)&1)
	}
	return v
}

func TestEEPROMThroughBus(t *testing.T) {
	m := loadSaveROM(t, "EEPROM_V122")
	const want uint64 = 0x1122334455667788
	eeBusWriteBlock(t, m, 0x2BC, 14, want)
	if got := eeBusReadBlock(t, m, 0x2BC, 14); got != want {
		t.Fatalf("EEPROM block via bus = %#016x, want %#016x", got, want)
	}
	if got := len(m.SaveRAM()); got != save.EEPROM8K {
		t.Fatalf("SaveRAM len = %d, want %d", got, save.EEPROM8K)
	}
}

func TestEEPROM512ThroughBus(t *testing.T) {
	m := loadSaveROM(t, "EEPROM_V110")
	// Pin the small part the way a frontend would: load a
	// 512 byte save image.
	m.SetSaveRAM(make([]byte, save.EEPROM512))
	const want uint64 = 0x8877665544332211
	eeBusWriteBlock(t, m, 0x15, 6, want)
	if got := eeBusReadBlock(t, m, 0x15, 6); got != want {
		t.Fatalf("EEPROM 512 block via bus = %#016x, want %#016x", got, want)
	}
	if got := len(m.SaveRAM()); got != save.EEPROM512 {
		t.Fatalf("SaveRAM len = %d, want %d", got, save.EEPROM512)
	}
}

func TestSaveDeviceInState(t *testing.T) {
	m := loadSaveROM(t, "FLASH_V103")
	b := m.Bus()
	w := func(off uint32, v byte) { b.Write(0x0E000000+off, 1, uint32(v)) }
	w(0x5555, 0xAA)
	w(0x2AAA, 0x55)
	w(0x5555, 0xA0)
	w(0x0300, 0x42)
	s := m.Serialize()
	m2 := loadSaveROM(t, "FLASH_V103")
	if err := m2.Deserialize(s); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if got := byte(m2.Bus().Read(0x0E000300, 1)); got != 0x42 {
		t.Fatalf("flash byte after state restore = %#02x, want 0x42", got)
	}
	if got := m2.SaveRAM()[0x0300]; got != 0x42 {
		t.Fatalf("SaveRAM after state restore = %#02x, want 0x42", got)
	}
	// The SRAM-side contents also ride the state (they
	// live in the bus stores, as before).
	m3 := loadSaveROM(t, "SRAM_V110")
	m3.Bus().Write(0x0E000777, 1, 0x24)
	s3 := m3.Serialize()
	m4 := loadSaveROM(t, "SRAM_V110")
	if err := m4.Deserialize(s3); err != nil {
		t.Fatalf("Deserialize SRAM state: %v", err)
	}
	if got := m4.SaveRAM()[0x777]; got != 0x24 {
		t.Fatalf("SRAM byte after state restore = %#02x, want 0x24", got)
	}
}
