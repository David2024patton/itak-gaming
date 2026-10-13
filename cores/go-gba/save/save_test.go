package save

import (
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-gba/cart"
)

// Flash helpers: the window base the bus presents.
const win = 0x0E000000

func fw(d *Device, off uint32, b byte) { d.FlashWrite(win+off, uint32(b)) }
func fr(d *Device, off uint32) byte    { return byte(d.FlashRead(win+off, 1)) }

func unlock(d *Device) {
	fw(d, 0x5555, 0xAA)
	fw(d, 0x2AAA, 0x55)
}

func command(d *Device, c byte) {
	unlock(d)
	fw(d, 0x5555, c)
}

func program(d *Device, off uint32, b byte) {
	command(d, 0xA0)
	fw(d, off, b)
}

func TestFlashIDMode(t *testing.T) {
	d := New(cart.SaveFlash, 0)
	command(d, 0x90)
	if got := fr(d, 0); got != 0xC2 {
		t.Fatalf("64K manufacturer ID = %#02x, want 0xC2 (Macronix)", got)
	}
	if got := fr(d, 1); got != 0x1C {
		t.Fatalf("64K device ID = %#02x, want 0x1C", got)
	}
	// A bare 0xF0 to 0x5555 leaves ID mode.
	fw(d, 0x5555, 0xF0)
	if got := fr(d, 0); got != 0xFF {
		t.Fatalf("after ID exit, offset 0 reads %#02x, want erased data 0xFF", got)
	}

	d1 := New(cart.SaveFlash1M, 0)
	command(d1, 0x90)
	if got := fr(d1, 0); got != 0x62 {
		t.Fatalf("128K manufacturer ID = %#02x, want 0x62 (Sanyo)", got)
	}
	if got := fr(d1, 1); got != 0x13 {
		t.Fatalf("128K device ID = %#02x, want 0x13", got)
	}
}

func TestFlashProgramANDSemantics(t *testing.T) {
	d := New(cart.SaveFlash512, 0)
	program(d, 0x1234, 0x3C)
	if got := fr(d, 0x1234); got != 0x3C {
		t.Fatalf("programmed byte = %#02x, want 0x3C", got)
	}
	// Programming can only clear bits: 0x3C AND 0xF0 = 0x30.
	program(d, 0x1234, 0xF0)
	if got := fr(d, 0x1234); got != 0x30 {
		t.Fatalf("second program = %#02x, want 0x30 (AND semantics)", got)
	}
	// And a program of 0xFF changes nothing.
	program(d, 0x1234, 0xFF)
	if got := fr(d, 0x1234); got != 0x30 {
		t.Fatalf("program 0xFF = %#02x, want 0x30", got)
	}
	// Neighbours are untouched.
	if got := fr(d, 0x1235); got != 0xFF {
		t.Fatalf("neighbour = %#02x, want 0xFF", got)
	}
}

func TestFlashSectorAndChipErase(t *testing.T) {
	d := New(cart.SaveFlash, 0)
	program(d, 0x0010, 0x11)
	program(d, 0x1010, 0x22)
	// Sector erase: setup, second unlock, 0x30 anywhere
	// inside the sector.
	command(d, 0x80)
	unlock(d)
	fw(d, 0x0ABC, 0x30)
	if got := fr(d, 0x0010); got != 0xFF {
		t.Fatalf("sector 0 after erase = %#02x, want 0xFF", got)
	}
	if got := fr(d, 0x1010); got != 0x22 {
		t.Fatalf("sector 1 after sector erase = %#02x, want 0x22 intact", got)
	}
	// Chip erase clears everything.
	command(d, 0x80)
	unlock(d)
	fw(d, 0x5555, 0x10)
	if got := fr(d, 0x1010); got != 0xFF {
		t.Fatalf("after chip erase = %#02x, want 0xFF", got)
	}
}

func TestFlashBankSwitch128K(t *testing.T) {
	d := New(cart.SaveFlash1M, 0)
	program(d, 0x0100, 0x11) // bank 0
	command(d, 0xB0)
	fw(d, 0x0000, 1) // bank 1
	if got := fr(d, 0x0100); got != 0xFF {
		t.Fatalf("bank 1 fresh byte = %#02x, want 0xFF", got)
	}
	program(d, 0x0100, 0x22)
	command(d, 0xB0)
	fw(d, 0x0000, 0) // back to bank 0
	if got := fr(d, 0x0100); got != 0x11 {
		t.Fatalf("bank 0 byte = %#02x, want 0x11", got)
	}
	sr := d.SaveRAM()
	if len(sr) != Flash128Size {
		t.Fatalf("SaveRAM len = %d, want %d", len(sr), Flash128Size)
	}
	if sr[0x0100] != 0x11 || sr[0x10100] != 0x22 {
		t.Fatalf("SaveRAM banks = %#02x/%#02x, want 0x11/0x22", sr[0x0100], sr[0x10100])
	}
	// The 64 KB kinds report the smaller image.
	if got := len(New(cart.SaveFlash, 0).SaveRAM()); got != Flash64Size {
		t.Fatalf("64K SaveRAM len = %d, want %d", got, Flash64Size)
	}
}

func TestFlashStateRoundTrip(t *testing.T) {
	d := New(cart.SaveFlash1M, 0)
	program(d, 0x0042, 0x77)
	command(d, 0xB0)
	fw(d, 0x0000, 1)
	buf := make([]byte, StateSize)
	if n := d.Serialize(buf); n != StateSize {
		t.Fatalf("Serialize used %d, want %d", n, StateSize)
	}
	d2 := New(cart.SaveFlash1M, 0)
	if !d2.Deserialize(buf) {
		t.Fatal("Deserialize rejected a valid block")
	}
	// Bank 1 is still selected after the round trip.
	if got := fr(d2, 0x0042); got != 0xFF {
		t.Fatalf("bank state lost: bank1[0x42] = %#02x, want 0xFF", got)
	}
	command(d2, 0xB0)
	fw(d2, 0x0000, 0)
	if got := fr(d2, 0x0042); got != 0x77 {
		t.Fatalf("restored byte = %#02x, want 0x77", got)
	}
	if d2.Deserialize([]byte{1, 2, 3}) {
		t.Fatal("Deserialize accepted a short block")
	}
}

// EEPROM helpers: clock bits through the serial window.

func ew(d *Device, bit uint32) { d.EEPROMWrite(bit) }
func er(d *Device) uint32      { return d.EEPROMRead() }

// eeWriteBlock clocks one full write transfer and polls
// ready, which must read 1 on the first poll in this
// model (the programming interval is collapsed).
func eeWriteBlock(t *testing.T, d *Device, addr uint32, addrBits int, value uint64) {
	t.Helper()
	ew(d, 1) // start
	ew(d, 0) // write request
	for i := addrBits - 1; i >= 0; i-- {
		ew(d, (addr>>uint(i))&1)
	}
	for i := 63; i >= 0; i-- {
		ew(d, uint32(value>>uint(i))&1)
	}
	ew(d, 0) // stop
	if got := er(d); got != 1 {
		t.Fatalf("ready poll after write = %d, want 1", got)
	}
}

// eeReadBlock clocks one full read transfer and returns
// the 64 bit block, checking the 4 dummy bits are zero.
func eeReadBlock(t *testing.T, d *Device, addr uint32, addrBits int) uint64 {
	t.Helper()
	ew(d, 1) // start
	ew(d, 1) // read request
	for i := addrBits - 1; i >= 0; i-- {
		ew(d, (addr>>uint(i))&1)
	}
	ew(d, 0) // stop
	for i := 0; i < 4; i++ {
		if got := er(d); got != 0 {
			t.Fatalf("dummy bit %d = %d, want 0", i, got)
		}
	}
	var v uint64
	for i := 0; i < 64; i++ {
		v = v<<1 | uint64(er(d))
	}
	return v
}

func TestEEPROMRoundTrip8K(t *testing.T) {
	d := New(cart.SaveEEPROM, 8*1024*1024)
	if got := er(d); got != 1 {
		t.Fatalf("idle read = %d, want ready 1", got)
	}
	const want uint64 = 0x0123456789ABCDEF
	eeWriteBlock(t, d, 0x123, 14, want)
	if got := eeReadBlock(t, d, 0x123, 14); got != want {
		t.Fatalf("read back = %#016x, want %#016x", got, want)
	}
	if got := d.EEPROMSize(); got != EEPROM8K {
		t.Fatalf("EEPROMSize = %d, want %d", got, EEPROM8K)
	}
	if got := len(d.SaveRAM()); got != EEPROM8K {
		t.Fatalf("SaveRAM len = %d, want %d", got, EEPROM8K)
	}
	// A different block is still zero.
	if got := eeReadBlock(t, d, 0x124, 14); got != 0 {
		t.Fatalf("untouched block = %#016x, want 0", got)
	}
}

func TestEEPROMRoundTrip512(t *testing.T) {
	d := New(cart.SaveEEPROM, 8*1024*1024)
	const want uint64 = 0xFEDCBA9876543210
	eeWriteBlock(t, d, 0x2A, 6, want)
	if got := d.EEPROMSize(); got != EEPROM512 {
		t.Fatalf("EEPROMSize = %d, want %d (learned from the 6 bit address)", got, EEPROM512)
	}
	if got := eeReadBlock(t, d, 0x2A, 6); got != want {
		t.Fatalf("read back = %#016x, want %#016x", got, want)
	}
	if got := len(d.SaveRAM()); got != EEPROM512 {
		t.Fatalf("SaveRAM len = %d, want %d", got, EEPROM512)
	}
}

func TestEEPROMBackToBackTransfers(t *testing.T) {
	d := New(cart.SaveEEPROM, 8*1024*1024)
	// A small part write followed IMMEDIATELY by a read
	// transfer (no ready poll in between): the read's
	// start bit lands while the write is still pending.
	ew(d, 1)
	ew(d, 0)
	for i := 5; i >= 0; i-- {
		ew(d, (0x11>>uint(i))&1)
	}
	const want uint64 = 0xA5A5A5A5A5A5A5A5
	for i := 63; i >= 0; i-- {
		ew(d, uint32(want>>uint(i))&1)
	}
	ew(d, 0) // stop: the write is now pending commit
	if got := eeReadBlock(t, d, 0x11, 6); got != want {
		t.Fatalf("back to back read = %#016x, want %#016x", got, want)
	}
}

func TestEEPROMWindowRule(t *testing.T) {
	big := New(cart.SaveEEPROM, 32*1024*1024)
	if big.EEPROMActive(0x0D000000) {
		t.Fatal("32 MB ROM: window offset 0 must not be EEPROM")
	}
	if !big.EEPROMActive(0x0DFFFF00) {
		t.Fatal("32 MB ROM: window offset 0xFFFF00 must be EEPROM")
	}
	if !big.EEPROMActive(0x0DFFFFFF) {
		t.Fatal("32 MB ROM: window offset 0xFFFFFF must be EEPROM")
	}
	small := New(cart.SaveEEPROM, 16*1024*1024)
	if !small.EEPROMActive(0x0D000000) {
		t.Fatal("16 MB ROM: the whole window must be EEPROM")
	}
	none := New(cart.SaveFlash, 8*1024*1024)
	if none.EEPROMActive(0x0D000000) {
		t.Fatal("a flash cartridge must never activate the EEPROM window")
	}
}

func TestEEPROMSetSaveRAMSizePin(t *testing.T) {
	d := New(cart.SaveEEPROM, 8*1024*1024)
	img := make([]byte, EEPROM512)
	img[0x10] = 0x99
	d.SetSaveRAM(img)
	if got := d.EEPROMSize(); got != EEPROM512 {
		t.Fatalf("EEPROMSize after 512 byte image = %d, want %d", got, EEPROM512)
	}
	// Block 2 byte 0 sits at image offset 0x10.
	if got := eeReadBlock(t, d, 2, 6); got != 0x9900000000000000 {
		t.Fatalf("read block 2 = %#016x, want 0x9900000000000000", got)
	}
}
