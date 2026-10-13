package system

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// ---------------------------------------------------------------------------
// SCD-3 system gate: a hand-assembled sub CPU initial program
// builds a stamp scene in Word RAM, programs the graphics ASIC
// through its register page, traces a 16x16 identity scene into
// the image buffer, checksums the buffer into the comm registers,
// and completes the comm handshake with the main stub. A save
// taken after the trace restores and continues byte-identically.
// ---------------------------------------------------------------------------

// cdASICIP is loaded to sub address 0x026000 by the boot stub.
// It fills stamp 1 (Word RAM bytes 128..255) with byte values
// 0..127 (so stamped byte k holds value k), sets stamp map entry
// (0,0) to stamp 1, builds a 16 line identity trace table at
// Word RAM 0x2000 (start (0, line), dot delta 1.0), programs the
// ASIC (map at 0x1000, buffer at 0x4000, 16x16 dots), triggers
// the draw, polls GRON clear, checksums the 128 buffer bytes
// (0+1+...+127 = 0x1FC0) into comm status 1, publishes the done
// magic as comm status 0, then stores the main CPU ack (comm
// status 1 XOR 0xFFFF = 0xE03F) at PRG-RAM 0x020500, sets sub
// flag 2 and idles.
var cdASICIP = []byte{
	0x41, 0xF9, 0x00, 0x20, 0x01, 0x00, // lea $200100, a0
	0x30, 0x3C, 0x00, 0x7F, // move.w #$7F, d0
	0x11, 0x00, // fill: move.b d0, -(a0)
	0x51, 0xC8, 0xFF, 0xFC, // dbra d0, fill
	0x33, 0xFC, 0x00, 0x01, 0x00, 0x20, 0x10, 0x00, // move.w #1, $201000
	0x41, 0xF9, 0x00, 0x20, 0x20, 0x00, // lea $202000, a0
	0x70, 0x00, // moveq #0, d0
	0x72, 0x00, // moveq #0, d1
	0x34, 0x3C, 0x00, 0x0F, // move.w #15, d2
	0x30, 0xC0, // tloop: move.w d0, (a0)+  (start X)
	0x30, 0xC1, // move.w d1, (a0)+          (start Y)
	0x30, 0xFC, 0x08, 0x00, // move.w #$0800, (a0)+ (delta X = 1.0)
	0x30, 0xC0, // move.w d0, (a0)+          (delta Y = 0)
	0x06, 0x41, 0x00, 0x08, // addi.w #8, d1
	0x51, 0xCA, 0xFF, 0xF0, // dbra d2, tloop
	0x33, 0xFC, 0x00, 0x00, 0x00, 0xFF, 0x80, 0x58, // move.w #0, $FF8058 (stamp ctl)
	0x33, 0xFC, 0x04, 0x00, 0x00, 0xFF, 0x80, 0x5A, // move.w #$400, $FF805A (map base)
	0x33, 0xFC, 0x00, 0x01, 0x00, 0xFF, 0x80, 0x5C, // move.w #1, $FF805C (vcell)
	0x33, 0xFC, 0x10, 0x00, 0x00, 0xFF, 0x80, 0x5E, // move.w #$1000, $FF805E (img start)
	0x33, 0xFC, 0x00, 0x00, 0x00, 0xFF, 0x80, 0x60, // move.w #0, $FF8060 (offset)
	0x33, 0xFC, 0x00, 0x10, 0x00, 0xFF, 0x80, 0x62, // move.w #16, $FF8062 (h dots)
	0x33, 0xFC, 0x00, 0x10, 0x00, 0xFF, 0x80, 0x64, // move.w #16, $FF8064 (v dots)
	0x33, 0xFC, 0x08, 0x00, 0x00, 0xFF, 0x80, 0x66, // move.w #$800, $FF8066 (trace base: starts)
	0x10, 0x39, 0x00, 0xFF, 0x80, 0x58, // poll: move.b $FF8058, d0 (GRON)
	0x4A, 0x00, // tst.b d0
	0x66, 0xF6, // bne.b poll
	0x41, 0xF9, 0x00, 0x20, 0x40, 0x00, // lea $204000, a0
	0x74, 0x00, // moveq #0, d2
	0x76, 0x00, // moveq #0, d3 (byte accumulator: high byte must start 0)
	0x32, 0x3C, 0x00, 0x7F, // move.w #$7F, d1
	0x16, 0x18, // csum: move.b (a0)+, d3
	0xD4, 0x43, // add.w d3, d2
	0x51, 0xC9, 0xFF, 0xFA, // dbra d1, csum
	0x33, 0xC2, 0x00, 0xFF, 0x80, 0x22, // move.w d2, $FF8022 (comm status 1)
	0x33, 0xFC, 0x60, 0x0D, 0x00, 0xFF, 0x80, 0x20, // move.w #$600D, $FF8020
	0x10, 0x39, 0x00, 0xFF, 0x80, 0x0E, // wait: move.b $FF800E, d0
	0x0C, 0x00, 0x00, 0x01, // cmpi.b #1, d0
	0x66, 0x00, 0xFF, 0xF4, // bne.w wait
	0x30, 0x39, 0x00, 0xFF, 0x80, 0x10, // move.w $FF8010, d0
	0x33, 0xC0, 0x00, 0x02, 0x05, 0x00, // move.w d0, $020500
	0x13, 0xFC, 0x00, 0x02, 0x00, 0xFF, 0x80, 0x0F, // move.b #2, $FF800F
	0x60, 0xFE, // idle: bra idle
}

func TestCDASICTrace(t *testing.T) {
	iso := makeSCDISO(t, cdASICIP)

	sys := New()
	if err := sys.LoadDisc(iso); err != nil {
		t.Fatalf("LoadDisc: %v", err)
	}
	if got, want := sys.SerializeSize(), 1070469; got != want {
		t.Fatalf("CD mode SerializeSize = %d, want %d (SCD-4 size: SCD-3 size plus BRAM and BIOS sections)", got, want)
	}
	if !runUntilSubDone(sys, 180) {
		t.Fatalf("sub flag = %#02x after 180 frames, want 2 (ASIC handshake never completed)", sys.CDUnit().SubFlag())
	}
	u := sys.CDUnit()

	// The image buffer holds the stamp bytes exactly (the 16x16
	// identity trace with matching buffer geometry copies them).
	for i := 0; i < 128; i++ {
		if got := u.WordRAM()[0x4000+i]; got != byte(i) {
			t.Fatalf("image buffer byte %d = %#02x, want %#02x", i, got, byte(i))
		}
	}
	// The sub CPU checksummed the buffer and published it; the
	// main stub answered with its inverse in comm command 0, and
	// the IP stored that ack in PRG-RAM.
	if got := u.CommStatus(1); got != 0x1FC0 {
		t.Fatalf("comm status 1 = %#06x, want 0x1FC0 (buffer checksum)", got)
	}
	if got := binary.BigEndian.Uint16(u.PRGRAM()[0x500:0x502]); got != 0xE03F {
		t.Fatalf("ack cell = %#06x, want 0xE03F (checksum XOR 0xFFFF)", got)
	}
	if u.ASICBusy() {
		t.Fatal("ASIC busy after the completed trace")
	}
	if got := u.ASICLinesRemaining(); got != 0 {
		t.Fatalf("ASIC lines remaining = %d, want 0", got)
	}

	// Save after the trace, restore into a fresh machine, and
	// the continuation is byte-identical.
	sA := make([]byte, sys.SerializeSize())
	if err := sys.Serialize(sA); err != nil {
		t.Fatalf("Serialize A: %v", err)
	}
	for i := 0; i < 15; i++ {
		sys.RunFrame()
	}
	sA2 := make([]byte, sys.SerializeSize())
	if err := sys.Serialize(sA2); err != nil {
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
		t.Fatal("state after restore plus 15 frames differs from the unrestored run")
	}
}
