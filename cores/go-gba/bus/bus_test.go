package bus

import "testing"

func TestMirroring(t *testing.T) {
	cases := []struct {
		name  string
		write uint32
		size  int
		val   uint32
		alias uint32 // an address that must mirror the write address
	}{
		{"EWRAM 256 KB mirror", 0x02000100, 4, 0x11223344, 0x02040100},
		{"EWRAM slot top mirror", 0x02000100, 4, 0x55667788, 0x02FC0100},
		{"IWRAM 32 KB mirror", 0x03000040, 4, 0x99AABBCC, 0x03008040},
		{"IWRAM slot top mirror", 0x03000040, 2, 0xBEEF, 0x03FF8040},
		{"palette 1 KB mirror", 0x05000010, 2, 0x7FFF, 0x05000410},
		{"palette slot mirror", 0x05000010, 4, 0x03E07C1F, 0x05010010},
		{"OAM 1 KB mirror", 0x07000020, 4, 0xDEADBEEF, 0x07000420},
		{"OAM slot mirror", 0x07000020, 2, 0x1234, 0x07020020},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := New()
			b.Write(c.write, c.size, c.val)
			if got := b.Read(c.alias, c.size); got != c.val {
				t.Fatalf("Read(%#08x, %d) = %#08x, want %#08x (mirror of write at %#08x)",
					c.alias, c.size, got, c.val, c.write)
			}
			if got := b.Read(c.write, c.size); got != c.val {
				t.Fatalf("Read(%#08x, %d) = %#08x, want %#08x", c.write, c.size, got, c.val)
			}
		})
	}
}

func TestVRAMUpperMirror(t *testing.T) {
	b := New()
	// The top 32 KB of VRAM lives at window offsets
	// 0x10000..0x17FFF and is mirrored at 0x18000..0x1FFFF.
	b.Write(0x06010000, 4, 0xCAFEBABE)
	for _, addr := range []uint32{0x06010000, 0x06018000, 0x06030000, 0x06038000} {
		if got := b.Read(addr, 4); got != 0xCAFEBABE {
			t.Fatalf("Read(%#08x, 4) = %#08x, want 0xCAFEBABE", addr, got)
		}
	}
	// The lower 64 KB repeats every 128 KB window too.
	b.Write(0x06000004, 4, 0x0BADF00D)
	for _, addr := range []uint32{0x06000004, 0x06020004, 0x06040004} {
		if got := b.Read(addr, 4); got != 0x0BADF00D {
			t.Fatalf("Read(%#08x, 4) = %#08x, want 0x0BADF00D", addr, got)
		}
	}
	// A write through the upper mirror lands in the same
	// backing bytes as the first copy.
	b.Write(0x06018008, 4, 0x11223344)
	if got := b.Read(0x06010008, 4); got != 0x11223344 {
		t.Fatalf("Read(0x06010008, 4) = %#08x, want 0x11223344", got)
	}
}

// TestVRAMMiddleBand pins the window offsets
// 0x8000..0xFFFF: they are real VRAM (the second 32 KB
// bank and the OBJ tile area starts right above them),
// NOT aliases of the 32 KB below. A mask-based decode
// once dropped bit 15 for the whole window, folding this
// band onto 0x0000..0x7FFF and scrambling any game that
// places maps or tiles there (found by the Anguna gate
// in GBA-4: its BG2 tilemap at window 0xE000 landed at
// backing offset 0x6000).
func TestVRAMMiddleBand(t *testing.T) {
	b := New()
	marks := []struct {
		addr uint32
		val  uint32
	}{
		{0x06008000, 0x11111111},
		{0x0600A000, 0x22222222},
		{0x0600E000, 0x33333333},
		{0x0600FFFC, 0x44444444},
	}
	for _, mk := range marks {
		b.Write(mk.addr, 4, mk.val)
	}
	for _, mk := range marks {
		if got := b.Read(mk.addr, 4); got != mk.val {
			t.Fatalf("Read(%#08x, 4) = %#08x, want %#08x", mk.addr, got, mk.val)
		}
	}
	// The spots the buggy fold used to hit must be empty.
	for _, addr := range []uint32{0x06000000, 0x06002000, 0x06006000, 0x06007FFC} {
		if got := b.Read(addr, 4); got != 0 {
			t.Fatalf("Read(%#08x, 4) = %#08x, want 0 (the middle band must not alias the low band)", addr, got)
		}
	}
	// And the backing store must hold the bytes at the
	// true offsets.
	if got := b.VRAM[0x8000]; got != 0x11 {
		t.Fatalf("VRAM backing at 0x8000 = %#02x, want 0x11", got)
	}
	if got := b.VRAM[0xE000]; got != 0x33 {
		t.Fatalf("VRAM backing at 0xE000 = %#02x, want 0x33", got)
	}
}

func TestROMMirroring(t *testing.T) {
	rom := make([]byte, 0x1000) // 4 KB fake ROM
	for i := range rom {
		rom[i] = byte(i*7 + 3)
	}
	b := New()
	b.SetROM(rom)
	want := b.Read(0x08000040, 4)
	for _, addr := range []uint32{0x08001040, 0x08002040, 0x0A000040, 0x0A003040, 0x0C000040, 0x0C007040} {
		if got := b.Read(addr, 4); got != want {
			t.Fatalf("Read(%#08x, 4) = %#08x, want %#08x (4 KB ROM mirror)", addr, got, want)
		}
	}
	// Byte reads agree with the image at the base window.
	for i := 0; i < 16; i++ {
		if got := b.Read(0x08000000+uint32(i), 1); got != uint32(rom[i]) {
			t.Fatalf("Read(%#08x, 1) = %#02x, want %#02x", 0x08000000+i, got, rom[i])
		}
	}
	// Writes to the ROM windows are ignored.
	b.Write(0x08000000, 4, 0xFFFFFFFF)
	if got := b.Read(0x08000000, 4); got == 0xFFFFFFFF {
		t.Fatal("write to ROM window changed the image")
	}
}

func TestSRAMByteReplication(t *testing.T) {
	b := New()
	b.Write(0x0E000000, 1, 0xAB)
	reads := []struct {
		addr uint32
		size int
		want uint32
	}{
		{0x0E000000, 1, 0xAB},
		{0x0E000000, 2, 0xABAB},
		{0x0E000000, 4, 0xABABABAB},
		{0x0E000001, 1, 0x00}, // the next byte is untouched
	}
	for _, r := range reads {
		if got := b.Read(r.addr, r.size); got != r.want {
			t.Fatalf("Read(%#08x, %d) = %#08x, want %#08x", r.addr, r.size, got, r.want)
		}
	}
	// An odd address with its own byte replicates that
	// byte with no rotation applied.
	b.Write(0x0E000001, 1, 0xCD)
	if got := b.Read(0x0E000001, 4); got != 0xCDCDCDCD {
		t.Fatalf("Read(0x0E000001, 4) = %#08x, want 0xCDCDCDCD", got)
	}
	if got := b.Read(0x0E000001, 2); got != 0xCDCD {
		t.Fatalf("Read(0x0E000001, 2) = %#08x, want 0xCDCD", got)
	}
	// The 0x0F window mirrors the same SRAM.
	if got := b.Read(0x0F000000, 1); got != 0xAB {
		t.Fatalf("Read(0x0F000000, 1) = %#02x, want 0xAB", got)
	}
}

func TestSRAMWideWritesUseLowByte(t *testing.T) {
	b := New()
	b.SRAM[0x11] = 0xEE // neighbor sentinel
	b.Write(0x0E000010, 4, 0x12345678)
	if got := b.SRAM[0x10]; got != 0x78 {
		t.Fatalf("SRAM[0x10] = %#02x, want 0x78 (low byte of wide write)", got)
	}
	if got := b.SRAM[0x11]; got != 0xEE {
		t.Fatalf("SRAM[0x11] = %#02x, want 0xEE (neighbor untouched)", got)
	}
	b.Write(0x0E000020, 2, 0x1234)
	if got := b.Read(0x0E000020, 1); got != 0x34 {
		t.Fatalf("Read(0x0E000020, 1) = %#02x, want 0x34", got)
	}
}

// TestRawAlignedReads pins the exact values the bus
// delivers for the word 0x11223344 stored at an aligned
// EWRAM address: the bus returns the raw aligned unit
// with no rotation (the CPU owns the GBATEK misaligned
// rotation). A word read returns the aligned word for
// every address inside it, a half read returns the
// aligned halfword at addr&^1, and a byte read returns
// the addressed byte.
func TestRawAlignedReads(t *testing.T) {
	b := New()
	b.Write(0x02000000, 4, 0x11223344)
	cases := []struct {
		addr uint32
		size int
		want uint32
	}{
		{0x02000000, 4, 0x11223344},
		{0x02000001, 4, 0x11223344},
		{0x02000002, 4, 0x11223344},
		{0x02000003, 4, 0x11223344},
		{0x02000000, 2, 0x00003344},
		{0x02000001, 2, 0x00003344},
		{0x02000002, 2, 0x00001122},
		{0x02000003, 2, 0x00001122},
		{0x02000000, 1, 0x44},
		{0x02000001, 1, 0x33},
		{0x02000002, 1, 0x22},
		{0x02000003, 1, 0x11},
	}
	for _, c := range cases {
		if got := b.Read(c.addr, c.size); got != c.want {
			t.Errorf("Read(%#08x, %d) = %#08x, want %#08x", c.addr, c.size, got, c.want)
		}
	}
}

// TestWritesForceAlignment pins the write side of the
// contract: a word write stores at addr&^3 and a half
// write stores the low halfword at addr&^1.
func TestWritesForceAlignment(t *testing.T) {
	b := New()
	b.Write(0x02000002, 4, 0xAABBCCDD)
	if got := b.Read(0x02000000, 4); got != 0xAABBCCDD {
		t.Fatalf("after unaligned word write, Read(0x02000000, 4) = %#08x, want 0xAABBCCDD", got)
	}
	b.Write(0x02000011, 2, 0x12345678)
	if got := b.Read(0x02000010, 2); got != 0x5678 {
		t.Fatalf("after unaligned half write, Read(0x02000010, 2) = %#08x, want 0x5678", got)
	}
	if got := b.Read(0x02000012, 2); got != 0x0000 {
		t.Fatalf("half write touched the next halfword: Read(0x02000012, 2) = %#08x", got)
	}
}

func TestOpenBus(t *testing.T) {
	b := New()
	// A fresh bus has driven nothing, and the BIOS store
	// reads back as zeros.
	if got := b.Read(0x01000000, 4); got != 0 {
		t.Fatalf("fresh open bus Read = %#08x, want 0", got)
	}
	if got := b.Read(0x00000000, 4); got != 0 {
		t.Fatalf("BIOS Read = %#08x, want 0 (zero filled store)", got)
	}
	b.Write(0x02000000, 4, 0xDEADBEEF)
	if got := b.Read(0x02000000, 4); got != 0xDEADBEEF {
		t.Fatalf("Read = %#08x, want 0xDEADBEEF", got)
	}
	// Unused region 0x01 returns the last driven value.
	if got := b.Read(0x01000000, 4); got != 0xDEADBEEF {
		t.Fatalf("open bus Read(0x01000000, 4) = %#08x, want 0xDEADBEEF", got)
	}
	// Address 0x0D000000 with no cartridge attached: the
	// ROM window has nothing to drive, so it is open bus
	// as well.
	if got := b.Read(0x0D000000, 4); got != 0xDEADBEEF {
		t.Fatalf("open bus Read(0x0D000000, 4) = %#08x, want 0xDEADBEEF", got)
	}
	// A fetch drives the bus too, and a later open read
	// returns the fetched value.
	b.Write(0x02000004, 4, 0x12345678)
	if got := b.Fetch(0x02000004, 4); got != 0x12345678 {
		t.Fatalf("Fetch = %#08x, want 0x12345678", got)
	}
	if got := b.Read(0x01000000, 4); got != 0x12345678 {
		t.Fatalf("open bus after Fetch = %#08x, want 0x12345678", got)
	}
	if got := b.OpenBus(); got != 0x12345678 {
		t.Fatalf("OpenBus() = %#08x, want 0x12345678", got)
	}
	// Writes to unmapped regions are ignored and do not
	// disturb real stores.
	b.Write(0x01000000, 4, 0xFFFFFFFF)
	if got := b.Read(0x02000004, 4); got != 0x12345678 {
		t.Fatalf("Read after unmapped write = %#08x, want 0x12345678", got)
	}
}

func TestIOStore(t *testing.T) {
	b := New()
	// WAITCNT lives at 0x04000204; in this slice it is a
	// plain cell of the IO store.
	b.Write(0x04000204, 2, 0x4317)
	if got := b.Read(0x04000204, 2); got != 0x4317 {
		t.Fatalf("WAITCNT Read = %#08x, want 0x4317", got)
	}
	if b.IO[0x204] != 0x17 || b.IO[0x205] != 0x43 {
		t.Fatalf("IO store bytes at 0x204 = %#02x %#02x, want 0x17 0x43", b.IO[0x204], b.IO[0x205])
	}
	b.Write(0x04000000, 4, 0xA5A5A5A5)
	if got := b.Read(0x04000000, 4); got != 0xA5A5A5A5 {
		t.Fatalf("IO Read = %#08x, want 0xA5A5A5A5", got)
	}
	b.Write(0x04000100, 1, 0x5A)
	if got := b.Read(0x04000100, 1); got != 0x5A {
		t.Fatalf("IO byte Read = %#08x, want 0x5A", got)
	}
	// Past the 1 KB register store the region is unmapped:
	// the read falls back to the open bus (the last IO
	// byte read, 0x5A).
	if got := b.Read(0x04000400, 4); got != 0x5A {
		t.Fatalf("Read past IO store = %#08x, want open bus 0x5A", got)
	}
}
