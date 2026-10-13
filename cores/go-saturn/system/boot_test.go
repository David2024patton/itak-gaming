package system

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-saturn/cd"
)

// bootTestImage builds a minimal bootable disc image: a
// System ID with the documented binary fields set, and a
// tiny IP at offset 0x100 that writes its entry marker to
// Work RAM-H and spins.
func bootTestImage(t *testing.T) *cd.Image {
	t.Helper()
	img := make([]byte, 4*2048)
	copy(img[0:16], "SEGA SEGASATURN ")
	copy(img[0x60:0x60+12], "BOOT TEST IP")
	binary.BigEndian.PutUint32(img[0xE0:0xE4], 0x1000)     // IP size
	binary.BigEndian.PutUint32(img[0xE8:0xEC], 0x06001800) // Stack-M
	binary.BigEndian.PutUint32(img[0xEC:0xF0], 0x06001400) // Stack-S
	// IP code at file offset 0x100 (RAM 0x06002100):
	// load two literals, store the marker at 0x06003000,
	// then spin.
	words := []uint16{
		0xD105,         // 0x00 MOV.L @(20,PC),R1 ; R1 = 0x06003000
		0xD206,         // 0x02 MOV.L @(24,PC),R2 ; R2 = 0xB007600D
		0x2122,         // 0x04 MOV.L R2,@R1
		0xAFFE,         // 0x06 BRA self
		0x0009,         // 0x08 delay NOP
		0x0009, 0x0009, // 0x0A padding
		0x0009, 0x0009, // 0x0E padding
		0x0009, 0x0009, // 0x12 padding
		0x0009,         // 0x16 padding
		0x0600, 0x3000, // 0x18 literal 0x06003000
		0xB007, 0x600D, // 0x1C literal 0xB007600D
	}
	for i, w := range words {
		binary.BigEndian.PutUint16(img[0x100+2*i:0x100+2*i+2], w)
	}
	im, err := cd.OpenBytes(img, 2048)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return im
}

func TestBootDiscPostBootState(t *testing.T) {
	s := New()
	if err := s.LoadDiscImage(bootTestImage(t)); err != nil {
		t.Fatalf("LoadDiscImage: %v", err)
	}
	if err := s.BootDisc(); err != nil {
		t.Fatalf("BootDisc: %v", err)
	}
	if got := s.Master.PC; got != IPEntryAddress {
		t.Fatalf("master PC = %#x, want %#x", got, uint32(IPEntryAddress))
	}
	if got := s.Master.Reg[15]; got != 0x06001800 {
		t.Fatalf("master SP = %#x, want Stack-M 0x06001800", got)
	}
	if got := s.Slave.PC; got != IPEntryAddress {
		t.Fatalf("slave PC = %#x, want %#x", got, uint32(IPEntryAddress))
	}
	if got := s.Slave.Reg[15]; got != 0x06001400 {
		t.Fatalf("slave SP = %#x, want Stack-S 0x06001400", got)
	}
	if s.SlaveReleased() {
		t.Fatal("slave released at boot, want held until SSHON")
	}
	// The IP bytes landed at the load address, including
	// the System ID at its base.
	if got := string(s.Bus.WorkRAMHigh()[0x2000:0x2010]); got != "SEGA SEGASATURN " {
		t.Fatalf("Work RAM-H at load base = %q, want the System ID", got)
	}
	// The boot ROM copies: System ID at 0x06000C00 and the
	// parameter block at 0x060002A0.
	ram := s.Bus.WorkRAMHigh()
	if got := string(ram[0xC00:0xC10]); got != "SEGA SEGASATURN " {
		t.Fatalf("System ID copy = %q", got)
	}
	if got := binary.BigEndian.Uint32(ram[0x2A0:0x2A4]); got != 0x1000 {
		t.Fatalf("boot params IP size = %#x, want 0x1000", got)
	}
	if got := binary.BigEndian.Uint32(ram[0x2A8:0x2AC]); got != 0x06001800 {
		t.Fatalf("boot params Stack-M = %#x, want 0x06001800", got)
	}
	// Sound and CD start held off, matching the SMPC power
	// on status the gates pin.
	if s.Sound.SoundOn() {
		t.Fatal("sound on at boot, want held until SNDON")
	}
	// Run: the IP writes its marker and spins; the slave
	// never runs a step while held.
	for i := 0; i < 3; i++ {
		s.RunFrame()
	}
	if got := s.Bus.Read32(0x06003000); got != 0xB007600D {
		t.Fatalf("IP marker = %#x, want 0xB007600D", got)
	}
	if got := s.Slave.Cycles; got != 0 {
		t.Fatalf("slave cycles = %d while held, want 0", got)
	}
	// The BIOS window still reads zero: no BIOS bytes are
	// involved in the boot.
	if got := s.Bus.Read32(0x00000000); got != 0 {
		t.Fatalf("BIOS window = %#x, want 0", got)
	}
}

func TestBootDiscErrors(t *testing.T) {
	s := New()
	if err := s.BootDisc(); !errors.Is(err, ErrNoDisc) {
		t.Fatalf("BootDisc with no disc = %v, want ErrNoDisc", err)
	}
	// IP size smaller than the System ID.
	img := make([]byte, 2*2048)
	copy(img[0:16], "SEGA SEGASATURN ")
	binary.BigEndian.PutUint32(img[0xE0:0xE4], 0x80)
	im, err := cd.OpenBytes(img, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LoadDiscImage(im); err != nil {
		t.Fatal(err)
	}
	if err := s.BootDisc(); err == nil {
		t.Fatal("BootDisc accepted an IP size below the System ID size")
	}
	// IP size larger than the image holds.
	binary.BigEndian.PutUint32(img[0xE0:0xE4], 0x4000)
	im2, err := cd.OpenBytes(img, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LoadDiscImage(im2); err != nil {
		t.Fatal(err)
	}
	if err := s.BootDisc(); err == nil {
		t.Fatal("BootDisc accepted an IP size beyond the image")
	}
	// A failed boot leaves the master at the power on park.
	if got := s.Master.PC; got != 0 {
		t.Fatalf("master PC after failed boot = %#x, want the power on park 0", got)
	}
}
