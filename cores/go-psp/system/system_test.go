package system

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// checksumProgram sums the 64 data bytes at 0x08000100 (values 1..64) and
// stores the total (2080) as a word at VRAM address 0x04000000, then spins.
// Hand-assembled MIPS, little endian words.
var checksumProgram = []uint32{
	0x3C080800, // lui   t0, 0x0800
	0x35080100, // ori   t0, t0, 0x0100        ; t0 = data pointer
	0x25090040, // addiu t1, t0, 64            ; t1 = end pointer
	0x240A0000, // addiu t2, $zero, 0          ; t2 = sum
	0x910B0000, // loop: lbu t3, 0(t0)
	0x014B5021, // addu  t2, t2, t3
	0x25080001, // addiu t0, t0, 1
	0x1509FFFC, // bne   t0, t1, loop
	0x3C0C0400, // lui   t4, 0x0400
	0xAD8A0000, // sw    t2, 0(t4)             ; checksum to VRAM
	0x0A00000A, // j     self
	0x00000000, // nop (delay slot)
}

func loadChecksumMachine(t *testing.T) *Machine {
	t.Helper()
	m := New()
	buf := make([]byte, 4)
	for i, w := range checksumProgram {
		binary.LittleEndian.PutUint32(buf, w)
		m.LoadBytes(0x08000000+uint32(4*i), buf)
	}
	data := make([]byte, 64)
	for i := range data {
		data[i] = byte(i + 1)
	}
	m.LoadBytes(0x08000100, data)
	m.Boot(0x08000000)
	return m
}

func TestChecksumProgram(t *testing.T) {
	m := loadChecksumMachine(t)
	m.RunCycles(10000)
	if got := m.Bus.Read32(0x04000000); got != 2080 {
		t.Fatalf("VRAM checksum = %d, want 2080", got)
	}
}

func TestSerializeDeterministicContinuation(t *testing.T) {
	a := loadChecksumMachine(t)
	a.RunCycles(100)
	mid := a.Serialize()
	if len(mid) != a.StateSize() {
		t.Fatalf("serialized %d bytes, StateSize says %d", len(mid), a.StateSize())
	}
	a.RunCycles(2000)
	finalA := a.Serialize()

	b := loadChecksumMachine(t)
	if err := b.Deserialize(mid); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	b.RunCycles(2000)
	finalB := b.Serialize()

	if !bytes.Equal(finalA, finalB) {
		t.Fatal("continuation after restore diverged from the uninterrupted run")
	}
	if got := b.Bus.Read32(0x04000000); got != 2080 {
		t.Fatalf("restored machine checksum = %d, want 2080", got)
	}
	if b.TotalCycles() != a.TotalCycles() {
		t.Fatalf("cycle counts differ: %d vs %d", b.TotalCycles(), a.TotalCycles())
	}
}

func TestDeserializeRejectsBadStates(t *testing.T) {
	m := New()
	if err := m.Deserialize([]byte{1, 2, 3}); err == nil {
		t.Fatal("short state accepted")
	}
	state := m.Serialize()
	state[0] = 0xEE
	if err := m.Deserialize(state); err == nil {
		t.Fatal("bad version accepted")
	}
}

func TestFramebufferBlankAndSized(t *testing.T) {
	m := New()
	fb := m.Framebuffer()
	if len(fb) != Width*Height {
		t.Fatalf("framebuffer has %d pixels, want %d", len(fb), Width*Height)
	}
	for i, px := range fb {
		if px != 0xFF000000 {
			t.Fatalf("pixel %d = %#x, want opaque black", i, px)
		}
	}
}

func TestInputStoredAndSerialized(t *testing.T) {
	m := New()
	m.SetButtons(ButtonCross | ButtonStart)
	m.SetAnalog(0x10, 0xE0)
	state := m.Serialize()

	n := New()
	if err := n.Deserialize(state); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if n.Buttons() != ButtonCross|ButtonStart {
		t.Errorf("buttons = %#x, want %#x", n.Buttons(), ButtonCross|ButtonStart)
	}
	if x, y := n.Analog(); x != 0x10 || y != 0xE0 {
		t.Errorf("analog = (%#x, %#x), want (0x10, 0xE0)", x, y)
	}
}

// --- synthetic ISO / SFO builders (test fixtures only) ---

func both16(dst []byte, v uint16) {
	binary.LittleEndian.PutUint16(dst[0:], v)
	binary.BigEndian.PutUint16(dst[2:], v)
}

func both32(dst []byte, v uint32) {
	binary.LittleEndian.PutUint32(dst[0:], v)
	binary.BigEndian.PutUint32(dst[4:], v)
}

func dirRecord(name []byte, extent, size uint32, dir bool) []byte {
	n := 33 + len(name)
	if n%2 == 1 {
		n++
	}
	rec := make([]byte, n)
	rec[0] = byte(n)
	both32(rec[2:], extent)
	both32(rec[10:], size)
	rec[18] = 125 // year 2025
	rec[19] = 10
	rec[20] = 13
	if dir {
		rec[25] = 2
	}
	both16(rec[28:], 1)
	rec[32] = byte(len(name))
	copy(rec[33:], name)
	return rec
}

func buildTestSFO() []byte {
	// Keys sorted: CATEGORY ("UG"), TITLE ("Sys Test").
	hdr := make([]byte, 20)
	hdr[1], hdr[2], hdr[3] = 'P', 'S', 'F'
	binary.LittleEndian.PutUint32(hdr[4:], 0x00000101)
	binary.LittleEndian.PutUint32(hdr[8:], 52)  // key table offset
	binary.LittleEndian.PutUint32(hdr[12:], 68) // data table offset
	binary.LittleEndian.PutUint32(hdr[16:], 2)  // entry count

	out := append([]byte{}, hdr...)
	entry := func(keyOff uint16, length, maxLen, dataOff uint32) {
		e := make([]byte, 16)
		binary.LittleEndian.PutUint16(e[0:], keyOff)
		binary.LittleEndian.PutUint16(e[2:], 0x0204) // UTF-8, null terminated
		binary.LittleEndian.PutUint32(e[4:], length)
		binary.LittleEndian.PutUint32(e[8:], maxLen)
		binary.LittleEndian.PutUint32(e[12:], dataOff)
		out = append(out, e...)
	}
	entry(0, 3, 4, 0)  // CATEGORY -> data offset 0
	entry(9, 9, 16, 4) // TITLE -> data offset 4
	out = append(out, "CATEGORY\x00TITLE\x00"...)
	out = append(out, 0) // pad key table to 4-byte alignment (52+15+1 = 68)
	data := make([]byte, 20)
	copy(data[0:], "UG\x00")
	copy(data[4:], "Sys Test\x00")
	out = append(out, data...)
	return out
}

func buildTestISO(t *testing.T, sfo []byte) string {
	t.Helper()
	const sec = 2048
	img := make([]byte, 24*sec)

	pvd := img[16*sec:]
	pvd[0] = 1
	copy(pvd[1:], "CD001")
	pvd[6] = 1
	for i := 40; i < 72; i++ {
		pvd[i] = ' '
	}
	copy(pvd[40:], "PSP_SYS_TEST")
	both32(pvd[80:], 24)
	both16(pvd[120:], 1)
	both16(pvd[124:], 1)
	both16(pvd[128:], sec)
	both32(pvd[132:], 10)
	binary.LittleEndian.PutUint32(pvd[140:], 19)
	binary.BigEndian.PutUint32(pvd[148:], 19)
	copy(pvd[156:], dirRecord([]byte{0}, 20, sec, true))

	root := img[20*sec:]
	off := 0
	for _, r := range [][]byte{
		dirRecord([]byte{0}, 20, sec, true),
		dirRecord([]byte{1}, 20, sec, true),
		dirRecord([]byte("PSP_GAME"), 21, sec, true),
	} {
		copy(root[off:], r)
		off += len(r)
	}

	game := img[21*sec:]
	off = 0
	for _, r := range [][]byte{
		dirRecord([]byte{0}, 21, sec, true),
		dirRecord([]byte{1}, 21, sec, true),
		dirRecord([]byte("PARAM.SFO;1"), 22, uint32(len(sfo)), false),
	} {
		copy(game[off:], r)
		off += len(r)
	}
	copy(img[22*sec:], sfo)

	path := filepath.Join(t.TempDir(), "test.iso")
	if err := os.WriteFile(path, img, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadISOReadsMetadata(t *testing.T) {
	path := buildTestISO(t, buildTestSFO())
	m := New()
	if err := m.LoadISO(path); err != nil {
		t.Fatalf("LoadISO: %v", err)
	}
	if got := m.GameTitle(); got != "Sys Test" {
		t.Errorf("GameTitle = %q, want %q", got, "Sys Test")
	}
	entries, err := m.ISO().List("/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name == "PSP_GAME" && e.Directory {
			found = true
		}
	}
	if !found {
		t.Errorf("root listing %v lacks PSP_GAME directory", entries)
	}
}

func TestLoadISORejectsNonISO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junk.iso")
	if err := os.WriteFile(path, []byte("not an iso at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.LoadISO(path); err == nil {
		t.Fatal("LoadISO accepted a non-ISO file")
	}
}
