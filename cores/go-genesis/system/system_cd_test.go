package system

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

// makeSCDISO writes a minimal two sector disc image: sector 0 is
// the boot header (SEGADISCSYSTEM signature, IP LBA and IP size in
// the header fields at 0x30 and 0x34) and sector 1 holds the IP.
func makeSCDISO(t *testing.T, ip []byte) string {
	t.Helper()
	img := make([]byte, 4096)
	copy(img[0:], "SEGADISCSYSTEM")
	binary.BigEndian.PutUint32(img[0x30:], 1)
	binary.BigEndian.PutUint32(img[0x34:], uint32(len(ip)))
	copy(img[2048:], ip)
	path := t.TempDir() + "/test.iso"
	if err := os.WriteFile(path, img, 0o644); err != nil {
		t.Fatalf("write ISO: %v", err)
	}
	return path
}

// cdTestIP is the initial program loaded to sub address 0x026000 by
// the boot stub. It fills the first 256 bytes of Word RAM with the
// descending sequence 255 down to 0 (so byte i of Word RAM ends up
// holding value i), copies that block through to PRG-RAM at
// 0x028000, records both checksums (0+1+...+255 = 0x7F80) in
// PRG-RAM cells, publishes the copy checksum as comm status 1 and
// the done magic 0x600D as comm status 0, then waits for the main
// CPU comm flag, stores the main CPU acknowledgement from comm
// command 0 (the main stub sends comm status 1 XOR 0xFFFF, which is
// 0x807F), sets sub flag 2 and idles.
var cdTestIP = []byte{
	0x41, 0xF9, 0x00, 0x20, 0x01, 0x00, // lea $200100, a0
	0x74, 0x00, // moveq #0, d2
	0x30, 0x3C, 0x00, 0xFF, // move.w #$FF, d0
	0x11, 0x00, // fill: move.b d0, -(a0)
	0xD4, 0x40, // add.w d0, d2
	0x51, 0xC8, 0xFF, 0xFA, // dbra d0, fill
	0x41, 0xF9, 0x00, 0x20, 0x00, 0x00, // lea $200000, a0
	0x43, 0xF9, 0x00, 0x02, 0x80, 0x00, // lea $028000, a1
	0x76, 0x00, // moveq #0, d3
	0x78, 0x00, // moveq #0, d4
	0x32, 0x3C, 0x00, 0xFF, // move.w #$FF, d1
	0x18, 0x18, // copy: move.b (a0)+, d4
	0x12, 0xC4, // move.b d4, (a1)+
	0xD6, 0x44, // add.w d4, d3
	0x51, 0xC9, 0xFF, 0xF8, // dbra d1, copy
	0x23, 0xC2, 0x00, 0x02, 0x05, 0x00, // move.l d2, $020500
	0x23, 0xC3, 0x00, 0x02, 0x05, 0x10, // move.l d3, $020510
	0x33, 0xC3, 0x00, 0xFF, 0x80, 0x22, // move.w d3, $FF8022
	0x33, 0xFC, 0x60, 0x0D, 0x00, 0xFF, 0x80, 0x20, // move.w #$600D, $FF8020
	0x10, 0x39, 0x00, 0xFF, 0x80, 0x0E, // poll: move.b $FF800E, d0
	0x0C, 0x00, 0x00, 0x01, // cmpi.b #1, d0
	0x66, 0x00, 0xFF, 0xF4, // bne poll
	0x30, 0x39, 0x00, 0xFF, 0x80, 0x10, // move.w $FF8010, d0
	0x33, 0xC0, 0x00, 0x02, 0x05, 0x08, // move.w d0, $020508
	0x13, 0xFC, 0x00, 0x02, 0x00, 0xFF, 0x80, 0x0F, // move.b #2, $FF800F
	0x60, 0xFE, // idle: bra idle
}

// runUntilSubDone runs frames until the IP sets sub flag 2 and
// reports whether that happened within maxFrames.
func runUntilSubDone(sys *System, maxFrames int) bool {
	for i := 0; i < maxFrames; i++ {
		if sys.CDUnit().SubFlag() == 2 {
			return true
		}
		sys.RunFrame()
	}
	return sys.CDUnit().SubFlag() == 2
}

// TestCDBootHandshake boots a disc with no cartridge loaded: the
// main stub releases the sub CPU, the boot stub loads the IP, and
// the two sides complete the comm handshake.
func TestCDBootHandshake(t *testing.T) {
	sys := New()
	if err := sys.LoadDisc(makeSCDISO(t, cdTestIP)); err != nil {
		t.Fatalf("LoadDisc: %v", err)
	}
	if !sys.CDAttached() {
		t.Fatalf("CDAttached = false after LoadDisc")
	}
	if !runUntilSubDone(sys, 180) {
		t.Fatalf("sub flag = %#02x after 180 frames, want 2 (boot handshake never completed)", sys.CDUnit().SubFlag())
	}
	u := sys.CDUnit()
	for i := 0; i < 256; i++ {
		if got := u.WordRAM()[i]; got != byte(i) {
			t.Fatalf("WordRAM[%d] = %#02x, want %#02x", i, got, byte(i))
		}
		if got := u.PRGRAM()[0x8000+i]; got != byte(i) {
			t.Fatalf("PRGRAM[%#x] = %#02x, want %#02x", 0x8000+i, got, byte(i))
		}
	}
	if got := binary.BigEndian.Uint32(u.PRGRAM()[0x500:0x504]); got != 0x7F80 {
		t.Fatalf("fill checksum cell = %#08x, want 0x7F80", got)
	}
	if got := binary.BigEndian.Uint32(u.PRGRAM()[0x510:0x514]); got != 0x7F80 {
		t.Fatalf("copy checksum cell = %#08x, want 0x7F80", got)
	}
	if got := binary.BigEndian.Uint16(u.PRGRAM()[0x508:0x50A]); got != 0x807F {
		t.Fatalf("ack cell = %#04x, want 0x807F", got)
	}
}

// TestCDStateDeterminism proves a CD mode save state restores into
// a fresh machine and continues byte-identically.
func TestCDStateDeterminism(t *testing.T) {
	iso := makeSCDISO(t, cdTestIP)

	a := New()
	if err := a.LoadDisc(iso); err != nil {
		t.Fatalf("LoadDisc A: %v", err)
	}
	if !runUntilSubDone(a, 180) {
		t.Fatalf("machine A: sub flag = %#02x, want 2", a.CDUnit().SubFlag())
	}
	sA := make([]byte, a.SerializeSize())
	if err := a.Serialize(sA); err != nil {
		t.Fatalf("Serialize A: %v", err)
	}
	for i := 0; i < 15; i++ {
		a.RunFrame()
	}
	sA2 := make([]byte, a.SerializeSize())
	if err := a.Serialize(sA2); err != nil {
		t.Fatalf("Serialize A2: %v", err)
	}

	b := New()
	if err := b.LoadDisc(iso); err != nil {
		t.Fatalf("LoadDisc B: %v", err)
	}
	if err := b.Deserialize(sA); err != nil {
		t.Fatalf("Deserialize B: %v", err)
	}
	for i := 0; i < 15; i++ {
		b.RunFrame()
	}
	sB2 := make([]byte, b.SerializeSize())
	if err := b.Serialize(sB2); err != nil {
		t.Fatalf("Serialize B2: %v", err)
	}
	if !bytes.Equal(sA2, sB2) {
		t.Fatalf("state after restore plus 15 frames differs from the unrestored run")
	}

	cartSys := New()
	if err := cartSys.LoadGame(ymTimerROM(0x05)); err != nil {
		t.Fatalf("LoadGame cart: %v", err)
	}
	if cartSys.SerializeSize() >= a.SerializeSize() {
		t.Fatalf("cartridge SerializeSize %d, want smaller than CD mode %d", cartSys.SerializeSize(), a.SerializeSize())
	}
	cartSized := make([]byte, cartSys.SerializeSize())
	if err := a.Deserialize(cartSized); err == nil {
		t.Fatalf("Deserialize of a cartridge-sized buffer in CD mode succeeded, want an error")
	}
}

// TestCDInertWithCartridge proves that attaching a disc alongside a
// cartridge leaves the cartridge fully in control: the sub CPU is
// never released, so the CD side stays at its power-on state.
func TestCDInertWithCartridge(t *testing.T) {
	sys := New()
	if err := sys.LoadGame(ymTimerROM(0x05)); err != nil {
		t.Fatalf("LoadGame: %v", err)
	}
	if err := sys.LoadDisc(makeSCDISO(t, cdTestIP)); err != nil {
		t.Fatalf("LoadDisc: %v", err)
	}
	for i := 0; i < 3; i++ {
		sys.RunFrame()
	}
	if got := binary.BigEndian.Uint32(sys.SystemRAM()[0:4]); got != ymTimerMagic {
		t.Fatalf("RAM magic = %#08x, want %#08x: the cartridge program was disturbed by the CD attachment", got, ymTimerMagic)
	}
	u := sys.CDUnit()
	if got := u.SubFlag(); got != 0 {
		t.Fatalf("SubFlag = %#02x, want 0", got)
	}
	if got := u.CommStatus(0); got != 0 {
		t.Fatalf("CommStatus(0) = %#04x, want 0", got)
	}
	for i, v := range u.PRGRAM() {
		if v != 0 {
			t.Fatalf("PRGRAM[%#x] = %#02x, want all zeros (sub CPU was never released)", i, v)
		}
	}
}
