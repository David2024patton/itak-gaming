package bus

import "testing"

func TestBIOSNOPPattern(t *testing.T) {
	b := New()
	if got := b.Read8(0x00000000); got != 0x09 {
		t.Errorf("Read8(0) = %#02x, want 0x09", got)
	}
	if got := b.Read8(0x00000001); got != 0x00 {
		t.Errorf("Read8(1) = %#02x, want 0x00", got)
	}
	if got := b.Read8(0x001FFFFE); got != 0x09 {
		t.Errorf("Read8(last even BIOS byte) = %#02x, want 0x09", got)
	}
	if got := b.Read16(0x00000000); got != 0x0009 {
		t.Errorf("Read16(0) = %#04x, want 0x0009", got)
	}
	if got := b.Read32(0x00000000); got != 0x00090009 {
		t.Errorf("Read32(0) = %#08x, want 0x00090009", got)
	}
	if got := b.Read64(0x00000000); got != 0x0009000900090009 {
		t.Errorf("Read64(0) = %#016x, want 0x0009000900090009", got)
	}
	if got := b.Read32(0x00001000); got != 0x00090009 {
		t.Errorf("Read32(0x1000) = %#08x, want 0x00090009", got)
	}
	// Unaligned reads still compose NOPs byte by byte.
	if got := b.Read16(0x00000001); got != 0x0900 {
		t.Errorf("Read16(1) = %#04x, want 0x0900", got)
	}
	// The BIOS window ignores writes.
	b.Write32(0x00000000, 0xFFFFFFFF)
	b.Write8(0x00000002, 0xAA)
	if got := b.Read32(0x00000000); got != 0x00090009 {
		t.Errorf("Read32(0) after writes = %#08x, want 0x00090009", got)
	}
}

func TestFlashWriteReadAndMirror(t *testing.T) {
	b := New()
	b.Write32(0x00200000, 0xDEADBEEF)
	if got := b.Read32(0x00200000); got != 0xDEADBEEF {
		t.Fatalf("flash Read32 = %#08x, want 0xDEADBEEF", got)
	}
	// The 256 KB store mirrors across the whole 1 MB window.
	for _, mirror := range []uint32{0x00240000, 0x00280000, 0x002C0000} {
		if got := b.Read32(mirror); got != 0xDEADBEEF {
			t.Errorf("flash mirror Read32(%#08x) = %#08x, want 0xDEADBEEF", mirror, got)
		}
	}
	// A write through a mirror lands in the same cell.
	b.Write8(0x00240005, 0x5A)
	if got := b.Read8(0x00200005); got != 0x5A {
		t.Errorf("flash mirror byte = %#02x, want 0x5A", got)
	}
	if got := b.Read8(0x002C0005); got != 0x5A {
		t.Errorf("flash second mirror byte = %#02x, want 0x5A", got)
	}
}

func TestRAMMirror(t *testing.T) {
	b := New()
	b.Write32(0x0C000100, 0x12345678)
	if got := b.Read32(0x0C000100); got != 0x12345678 {
		t.Fatalf("RAM Read32 = %#08x, want 0x12345678", got)
	}
	// Main RAM mirrors every 16 MB inside its 64 MB span.
	for _, mirror := range []uint32{0x0D000100, 0x0E000100, 0x0F000100} {
		if got := b.Read32(mirror); got != 0x12345678 {
			t.Errorf("RAM mirror Read32(%#08x) = %#08x, want 0x12345678", mirror, got)
		}
	}
	b.Write16(0x0F000102, 0xBEEF)
	if got := b.Read16(0x0C000102); got != 0xBEEF {
		t.Errorf("RAM mirror half = %#04x, want 0xBEEF", got)
	}
}

func TestVRAMWindowsAlias(t *testing.T) {
	b := New()
	// A write through the 32-bit view reads back through the
	// 64-bit view: both windows share one backing store.
	b.Write32(0x04000004, 0xCAFEBABE)
	if got := b.Read32(0x05000004); got != 0xCAFEBABE {
		t.Errorf("VRAM 64-bit view = %#08x, want 0xCAFEBABE", got)
	}
	// And the other direction.
	b.Write64(0x05000010, 0x0123456789ABCDEF)
	if got := b.Read64(0x04000010); got != 0x0123456789ABCDEF {
		t.Errorf("VRAM 32-bit view = %#016x, want 0x0123456789ABCDEF", got)
	}
	// Each window also mirrors across its own 16 MB span.
	if got := b.Read32(0x04800004); got != 0xCAFEBABE {
		t.Errorf("VRAM 32-bit window mirror = %#08x, want 0xCAFEBABE", got)
	}
	if got := b.Read32(0x05800004); got != 0xCAFEBABE {
		t.Errorf("VRAM 64-bit window mirror = %#08x, want 0xCAFEBABE", got)
	}
}

func TestAICARAM(t *testing.T) {
	b := New()
	b.Write32(0x00800100, 0x11223344)
	if got := b.Read32(0x00800100); got != 0x11223344 {
		t.Errorf("AICA RAM Read32 = %#08x, want 0x11223344", got)
	}
	// AICA RAM is its own store, not an alias of main RAM.
	if got := b.Read32(0x0C000100); got != 0 {
		t.Errorf("main RAM at same offset = %#08x, want 0", got)
	}
}

func TestRegisterStubs(t *testing.T) {
	b := New()
	// Untouched stub registers read 0.
	if got := b.Read32(0x005F8000); got != 0 {
		t.Errorf("PowerVR stub default = %#08x, want 0", got)
	}
	if got := b.Read32(0x00700000); got != 0 {
		t.Errorf("AICA stub default = %#08x, want 0", got)
	}
	if got := b.Read32(0x00600000); got != 0 {
		t.Errorf("modem stub default = %#08x, want 0", got)
	}

	// A 32-bit write reads back at the same aligned address.
	b.Write32(0x005F7010, 0xA5A5A5A5)
	if got := b.Read32(0x005F7010); got != 0xA5A5A5A5 {
		t.Fatalf("GD stub Read32 = %#08x, want 0xA5A5A5A5", got)
	}
	// Neighbouring stub words are independent.
	if got := b.Read32(0x005F7014); got != 0 {
		t.Errorf("GD stub neighbour = %#08x, want 0", got)
	}
	// Byte and half writes merge into the stored word.
	b.Write8(0x005F7011, 0x3C)
	if got := b.Read32(0x005F7010); got != 0xA5A53CA5 {
		t.Errorf("GD stub after byte merge = %#08x, want 0xA5A53CA5", got)
	}
	if got := b.Read8(0x005F7011); got != 0x3C {
		t.Errorf("GD stub byte readback = %#02x, want 0x3C", got)
	}
	b.Write16(0x005F7012, 0x0F0F)
	if got := b.Read32(0x005F7010); got != 0x0F0F3CA5 {
		t.Errorf("GD stub after half merge = %#08x, want 0x0F0F3CA5", got)
	}
	if got := b.Read16(0x005F7010); got != 0x3CA5 {
		t.Errorf("GD stub half readback = %#04x, want 0x3CA5", got)
	}
	// Each stub range keeps its own storage.
	b.Write32(0x005F8400, 0x11111111)
	b.Write32(0x00703FF0, 0x22222222)
	b.Write32(0x006FFFF0, 0x33333333)
	if got := b.Read32(0x005F8400); got != 0x11111111 {
		t.Errorf("TA stub = %#08x, want 0x11111111", got)
	}
	if got := b.Read32(0x00703FF0); got != 0x22222222 {
		t.Errorf("AICA stub = %#08x, want 0x22222222", got)
	}
	if got := b.Read32(0x006FFFF0); got != 0x33333333 {
		t.Errorf("modem stub = %#08x, want 0x33333333", got)
	}
}

func TestUnmappedSpace(t *testing.T) {
	b := New()
	for _, pa := range []uint32{0x00300000, 0x01000000, 0x02000000, 0x06000000, 0x10000000, 0x1FFFFFF0} {
		if got := b.Read32(pa); got != 0 {
			t.Errorf("unmapped Read32(%#08x) = %#08x, want 0", pa, got)
		}
		b.Write32(pa, 0xFFFFFFFF)
		if got := b.Read32(pa); got != 0 {
			t.Errorf("unmapped Read32(%#08x) after write = %#08x, want 0", pa, got)
		}
	}
}

func TestUnalignedRAMAccess(t *testing.T) {
	b := New()
	b.Write32(0x0C000001, 0xA1B2C3D4)
	if got := b.Read32(0x0C000001); got != 0xA1B2C3D4 {
		t.Errorf("unaligned RAM Read32 = %#08x, want 0xA1B2C3D4", got)
	}
	if got := b.Read8(0x0C000001); got != 0xD4 {
		t.Errorf("unaligned RAM first byte = %#02x, want 0xD4", got)
	}
	if got := b.Read8(0x0C000004); got != 0xA1 {
		t.Errorf("unaligned RAM last byte = %#02x, want 0xA1", got)
	}
	b.Write64(0x0C000103, 0xFFEEDDCCBBAA9988)
	if got := b.Read64(0x0C000103); got != 0xFFEEDDCCBBAA9988 {
		t.Errorf("unaligned RAM Read64 = %#016x, want 0xFFEEDDCCBBAA9988", got)
	}
}
