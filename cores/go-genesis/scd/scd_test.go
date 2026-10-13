package scd

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-genesis/chips/m68k"
)

// makeISO builds a two sector temp .iso for tests: sector 0 is a
// Sega CD style header sector carrying the SEGADISCSYSTEM
// signature at offset 0, the IP start LBA (1) at offset 0x30 and
// the IP size at offset 0x34, and sector 1 is the IP padded to
// 2048 bytes. It returns the image path.
func makeISO(t *testing.T, ip []byte) string {
	t.Helper()
	img := make([]byte, 2*2048)
	copy(img[0:], "SEGADISCSYSTEM")
	binary.BigEndian.PutUint32(img[0x30:], 1)
	binary.BigEndian.PutUint32(img[0x34:], uint32(len(ip)))
	copy(img[2048:], ip)
	path := filepath.Join(t.TempDir(), "test.iso")
	if err := os.WriteFile(path, img, 0o644); err != nil {
		t.Fatalf("write ISO: %v", err)
	}
	return path
}

// openISO opens a disc for a test and closes it at test end.
func openISO(t *testing.T, ip []byte) *Disc {
	t.Helper()
	d, err := OpenDisc(makeISO(t, ip))
	if err != nil {
		t.Fatalf("OpenDisc: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// pollCDC reads the sub CPU CDC status register until the
// transfer complete bit sets, and returns the final status byte.
func pollCDC(t *testing.T, u *Unit) uint8 {
	t.Helper()
	var st uint8
	for i := 0; i < 10; i++ {
		st = u.Read8(0xFF8037)
		if st&0x02 != 0 {
			return st
		}
	}
	t.Fatalf("CDC transfer never completed, last status %#04x", st)
	return st
}

func TestGateArrayCommAndFlags(t *testing.T) {
	u := NewUnit(nil)

	// Comm command 3 = 0xCAFE, written by the main CPU one byte
	// lane at a time (offset 0x16 = high byte, 0x17 = low byte).
	u.MainWrite8(0x16, 0xCA)
	u.MainWrite8(0x17, 0xFE)
	if got := u.CommCommand(3); got != 0xCAFE {
		t.Fatalf("CommCommand(3) = %#06x, want 0xCAFE", got)
	}
	if got := u.Read8(0xFF8016); got != 0xCA {
		t.Fatalf("sub read of comm command 3 high byte = %#04x, want 0xCA", got)
	}
	if got := u.Read8(0xFF8017); got != 0xFE {
		t.Fatalf("sub read of comm command 3 low byte = %#04x, want 0xFE", got)
	}

	// Comm status 5 and the sub flag are written by the sub CPU
	// and read back by the main CPU.
	u.Write8(0xFF802A, 0x12)
	u.Write8(0xFF802B, 0x34)
	if got := u.CommStatus(5); got != 0x1234 {
		t.Fatalf("CommStatus(5) = %#06x, want 0x1234", got)
	}
	if got := u.MainRead8(0x2A); got != 0x12 {
		t.Fatalf("main read of comm status 5 high byte = %#04x, want 0x12", got)
	}
	if got := u.MainRead8(0x2B); got != 0x34 {
		t.Fatalf("main read of comm status 5 low byte = %#04x, want 0x34", got)
	}
	u.Write8(0xFF800F, 0x5A)
	if got := u.SubFlag(); got != 0x5A {
		t.Fatalf("SubFlag() = %#04x, want 0x5A", got)
	}
	if got := u.MainRead8(0x0F); got != 0x5A {
		t.Fatalf("main read of sub flag = %#04x, want 0x5A", got)
	}

	// The main flag is written by the main CPU and read by the sub.
	u.MainWrite8(0x0E, 0xA5)
	if got := u.MainFlag(); got != 0xA5 {
		t.Fatalf("MainFlag() = %#04x, want 0xA5", got)
	}
	if got := u.Read8(0xFF800E); got != 0xA5 {
		t.Fatalf("sub read of main flag = %#04x, want 0xA5", got)
	}

	// Write protection: the sub CPU cannot write the main flag,
	// the main CPU cannot write the sub flag, the comm status
	// words, or the CD block offsets at or above 0x30.
	u.Write8(0xFF800E, 0x00)
	if got := u.MainFlag(); got != 0xA5 {
		t.Fatalf("sub write changed MainFlag to %#04x, want 0xA5", got)
	}
	u.MainWrite8(0x0F, 0x00)
	if got := u.SubFlag(); got != 0x5A {
		t.Fatalf("main write changed SubFlag to %#04x, want 0x5A", got)
	}
	u.MainWrite8(0x2A, 0xFF)
	if got := u.CommStatus(5); got != 0x1234 {
		t.Fatalf("main write changed CommStatus(5) to %#06x, want 0x1234", got)
	}
	u.Write8(0xFF8016, 0x00)
	if got := u.CommCommand(3); got != 0xCAFE {
		t.Fatalf("sub write changed CommCommand(3) to %#06x, want 0xCAFE", got)
	}
	if got := u.MainRead8(0x30); got != 0 {
		t.Fatalf("main read of offset 0x30 = %#04x, want 0", got)
	}
}

func TestWordRAMOwnership(t *testing.T) {
	u := NewUnit(nil)

	// Power-on: 2M mode, sub CPU owns Word RAM.
	if !u.WordOwnerSub() {
		t.Fatal("power-on Word RAM owner is not the sub CPU")
	}
	u.Write8(0x200000, 0x5A)
	if got := u.WordRAM()[0]; got != 0x5A {
		t.Fatalf("WordRAM()[0] = %#04x, want 0x5A", got)
	}
	if got := u.MainWordRead8(0x200000); got != 0xFF {
		t.Fatalf("main Word RAM read while sub owns = %#04x, want 0xFF", got)
	}
	u.MainWordWrite8(0x200000, 0x99)
	if got := u.WordRAM()[0]; got != 0x5A {
		t.Fatalf("main Word RAM write while sub owns landed: %#04x", got)
	}

	// A non-owner DMNA write is ignored in 2M mode.
	u.MainWrite8(0x02, 0x02)
	if !u.WordOwnerSub() {
		t.Fatal("non-owner DMNA transferred Word RAM ownership")
	}

	// The owner gives: ownership flips to the main CPU and the
	// main side RET flag sets.
	u.Write8(0xFF8002, 0x02)
	if u.WordOwnerSub() {
		t.Fatal("owner DMNA did not transfer Word RAM ownership")
	}
	if got := u.MainWordRead8(0x200000); got != 0x5A {
		t.Fatalf("main Word RAM read after transfer = %#04x, want 0x5A", got)
	}
	if got := u.MainRead8(0x02); got&0x04 == 0 {
		t.Fatalf("main read of 0x02 = %#04x, want RET bit set", got)
	}
	if got := u.Read8(0x200000); got != 0xFF {
		t.Fatalf("sub Word RAM read while main owns = %#04x, want 0xFF", got)
	}

	// Switch to 1M with a combined MODE plus DMNA write from the
	// owner (main) side: mode becomes 1M and the halves swap, so
	// the sub CPU now owns the second 128 KB half.
	u.MainWrite8(0x02, 0x03)
	if got := u.MainRead8(0x02); got&0x01 == 0 {
		t.Fatalf("mode readback = %#04x, want 1M mode bit set", got)
	}
	u.Write8(0x200000, 0xA5) // sub half: physical 0x20000
	if got := u.WordRAM()[0x20000]; got != 0xA5 {
		t.Fatalf("WordRAM()[0x20000] = %#04x, want 0xA5 (sub 1M half)", got)
	}
	if got := u.MainWordRead8(0x200000); got != 0x5A {
		t.Fatalf("main 1M window read = %#04x, want its own half byte 0x5A", got)
	}
	u.MainWordWrite8(0x200000, 0x11) // main half: physical 0
	if got := u.WordRAM()[0]; got != 0x11 {
		t.Fatalf("WordRAM()[0] = %#04x, want 0x11 (main 1M half)", got)
	}

	// A 1M DMNA swap exchanges the halves: each side now sees the
	// byte the other side wrote.
	u.MainWrite8(0x02, 0x03)
	if got := u.MainWordRead8(0x200000); got != 0xA5 {
		t.Fatalf("main window after 1M swap = %#04x, want sub byte 0xA5", got)
	}
	if got := u.Read8(0x200000); got != 0x11 {
		t.Fatalf("sub window after 1M swap = %#04x, want main byte 0x11", got)
	}
}

func TestCDCPolledAndDMA(t *testing.T) {
	ip := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x02, 0x03, 0x04}
	disc := openISO(t, ip)
	u := NewUnit(disc)

	// DMA transfer of sector 1 (the IP) into PRG-RAM at sub
	// address 0x00030000, destination type PRG-RAM (DDT = 1).
	u.Write8(0xFF8003, 0x08)
	u.Write32(0xFF8038, 1)          // LBA latch
	u.Write32(0xFF803E, 0x00030000) // destination latch
	u.Write16(0xFF803C, 1)          // sector count
	u.Write8(0xFF8036, 0x01)        // start
	st := pollCDC(t, u)
	if st&0x08 != 0 {
		t.Fatalf("CDC status %#04x has the error bit set", st)
	}
	base := 0x30000 - 0x20000
	for i, want := range ip {
		if got := u.PRGRAM()[base+i]; got != want {
			t.Fatalf("PRGRAM()[%#x] = %#04x, want IP byte %#04x", base+i, got, want)
		}
	}

	// Buffer-only transfer (DDT = 0) of sector 0, then stream it
	// out of the CDC data port.
	u.Write8(0xFF8003, 0x00)
	u.Write32(0xFF8038, 0)
	u.Write32(0xFF803E, 0)
	u.Write16(0xFF803C, 1)
	u.Write8(0xFF8036, 0x01)
	st = pollCDC(t, u)
	if st&0x04 == 0 {
		t.Fatalf("CDC status %#04x does not report data ready", st)
	}
	got := make([]byte, 14)
	for i := range got {
		got[i] = u.Read8(0xFF8048)
	}
	if string(got) != "SEGADISCSYSTEM" {
		t.Fatalf("data port stream = %q, want SEGADISCSYSTEM", got)
	}
}

func TestBootStubBootsIP(t *testing.T) {
	// IP program: MOVE.W #0x600D, 0xFF8020.l (comm status 0),
	// then BRA . to idle. The absolute long encoding is 0x33FC
	// followed by the immediate word and the 32 bit address.
	ip := []byte{
		0x33, 0xFC, 0x60, 0x0D, 0x00, 0xFF, 0x80, 0x20,
		0x60, 0xFE,
	}
	disc := openISO(t, ip)
	u := NewUnit(disc)
	cpu := m68k.New(u)

	// The reset one-shot does not report while RES = 0, and the
	// arm survives the early call.
	if u.TakeSubReset() {
		t.Fatal("TakeSubReset reported a reset before release")
	}
	if !u.SubHalted() {
		t.Fatal("sub CPU is not halted at power-on")
	}
	u.MainWrite8(0x00, 0x01) // RES = 1: release the sub CPU
	if u.SubHalted() {
		t.Fatal("sub CPU is still halted after RES = 1")
	}
	if !u.TakeSubReset() {
		t.Fatal("TakeSubReset did not report the release")
	}
	if u.TakeSubReset() {
		t.Fatal("TakeSubReset reported the same release twice")
	}

	cpu.Reset()
	for i := 0; i < 200 && u.SubFlag() != 1; i++ {
		cpu.StepCycles(1000)
	}
	if got := u.SubFlag(); got != 1 {
		t.Fatalf("SubFlag() = %#04x, want 1 (boot OK) after boot stub run", got)
	}
	if got := u.CommStatus(0); got != 0x600D {
		t.Fatalf("CommStatus(0) = %#06x, want 0x600D written by the IP (the stub wrote 0xB007 first)", got)
	}
	if cpu.Halted() {
		t.Fatal("sub CPU halted during boot")
	}
}

func TestUnitSerializeRoundTrip(t *testing.T) {
	ip := []byte{0x10, 0x20, 0x30}
	disc := openISO(t, ip)
	u := NewUnit(disc)

	// Dirty every piece of serialized state.
	u.PRGRAM()[0] = 0xAA
	u.PRGRAM()[0x7FFFF] = 0xBB
	u.WordRAM()[0] = 0xCC
	u.WordRAM()[0x3FFFF] = 0xDD
	u.Write8(0xFF0003, 0x42) // PCM register
	u.Write8(0xFF1000, 0x77) // PCM wave RAM through its window
	u.MainWrite8(0x00, 0x01) // control: RES = 1
	u.MainWrite8(0x01, 0x24) // LED
	u.Write8(0xFF8002, 0x02) // DMNA: Word RAM to main, RET main
	u.Write8(0xFF8003, 0x08) // CDC destination PRG-RAM
	u.MainWrite8(0x04, 0x06) // H-INT interval
	u.MainWrite8(0x05, 0x00) // H-INT vector (autovector)
	u.MainWrite8(0x0E, 0xE0) // main flag
	u.Write8(0xFF800F, 0x0F) // sub flag
	u.MainWrite8(0x12, 0x12) // comm command 1 = 0x1234
	u.MainWrite8(0x13, 0x34)
	u.Write8(0xFF8024, 0xAB) // comm status 2 = 0xABCD
	u.Write8(0xFF8025, 0xCD)
	u.Write8(0xFF8030, 0x00) // CDD command, refreshes response
	u.Write8(0xFF8031, 0x99) // CDD parameter
	u.Write32(0xFF8038, 7)   // CDC LBA latch
	u.Write16(0xFF803C, 3)   // CDC count latch
	u.Write32(0xFF803E, 0x00026000)
	// Run one buffer transfer so the sector buffer, data pointer
	// and CDC state are all nonzero in the saved state.
	u.Write8(0xFF8003, 0x00)
	u.Write32(0xFF8038, 1)
	u.Write16(0xFF803C, 1)
	u.Write8(0xFF8036, 0x01)
	pollCDC(t, u)
	_ = u.Read8(0xFF8048) // advance the data port pointer
	_ = u.Read8(0xFF8048)

	buf1 := make([]byte, u.SerializeSize())
	if err := u.Serialize(buf1); err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	u2 := NewUnit(disc)
	if err := u2.Deserialize(buf1); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	buf2 := make([]byte, u2.SerializeSize())
	if err := u2.Serialize(buf2); err != nil {
		t.Fatalf("re-Serialize: %v", err)
	}
	if !bytes.Equal(buf1, buf2) {
		t.Fatal("serialize, deserialize, serialize did not round trip byte for byte")
	}
	if got := u2.CommStatus(2); got != 0xABCD {
		t.Fatalf("restored CommStatus(2) = %#06x, want 0xABCD", got)
	}
	if got := u2.SubFlag(); got != 0x0F {
		t.Fatalf("restored SubFlag() = %#04x, want 0x0F", got)
	}
}

func TestBootStubImageVectors(t *testing.T) {
	img := BootStubImage()
	if got := binary.BigEndian.Uint32(img[0:]); got != 0x000A0000 {
		t.Fatalf("boot stub SSP = %#010x, want 0x000A0000", got)
	}
	if got := binary.BigEndian.Uint32(img[4:]); got != 0x00000200 {
		t.Fatalf("boot stub PC = %#010x, want 0x00000200", got)
	}
	if !bytes.Contains(img, []byte("SEGADISCSYSTEM")) {
		t.Fatal("boot stub image does not embed the SEGADISCSYSTEM signature")
	}

	rom := MainStubROM()
	if got := binary.BigEndian.Uint32(rom[4:]); got != 0x00000200 {
		t.Fatalf("main stub PC = %#010x, want 0x00000200", got)
	}
	if got := string(rom[0x100 : 0x100+4]); got != "SEGA" {
		t.Fatalf("main stub header at 0x100 = %q, want SEGA prefix", got)
	}
}
