package bus

import "testing"

func TestRAMAliasesShareBacking(t *testing.T) {
	b := New()
	b.Write32(0x08000000, 0xDEADBEEF) // KUSEG direct
	if got := b.Read32(0x88000000); got != 0xDEADBEEF {
		t.Errorf("cached alias read = %#x, want 0xDEADBEEF", got)
	}
	if got := b.Read32(0xA8000000); got != 0xDEADBEEF {
		t.Errorf("uncached alias read = %#x, want 0xDEADBEEF", got)
	}
	b.Write8(0xA8000004, 0x7E)
	if got := b.Read8(0x08000004); got != 0x7E {
		t.Errorf("uncached write not visible via KUSEG: %#x", got)
	}
}

func TestScratchpadAndAlias(t *testing.T) {
	b := New()
	b.Write16(0x00010010, 0xBEEF) // kernel scratchpad address
	if got := b.Read16(0x00000010); got != 0xBEEF {
		t.Errorf("scratchpad zero alias read = %#x, want 0xBEEF", got)
	}
	b.Write8(0x00003FFF, 0x11) // top of the alias window
	if got := b.Scratchpad[0x3FFF]; got != 0x11 {
		t.Errorf("scratchpad backing byte = %#x, want 0x11", got)
	}
}

func TestVRAMMirror(t *testing.T) {
	b := New()
	b.Write32(0x04000000, 0x11223344)
	if got := b.Read32(0x04200000); got != 0x11223344 {
		t.Errorf("VRAM 2 MB mirror read = %#x, want 0x11223344", got)
	}
	if got := b.Read32(0x04400000); got != 0x11223344 {
		t.Errorf("VRAM 4 MB mirror read = %#x, want 0x11223344", got)
	}
}

func TestIOStubsStoreValues(t *testing.T) {
	b := New()
	b.Write32(0x1E000020, 0xA5A5A5A5)                   // interrupt controller page (stub)
	if got := b.Read32(0xBE000020); got != 0xA5A5A5A5 { // KSEG1 alias
		t.Errorf("IO stub read = %#x, want 0xA5A5A5A5", got)
	}
	b.Write8(0x1E000021, 0x3C)
	if got := b.Read32(0x1E000020); got != 0xA5A53CA5 {
		t.Errorf("IO byte write = %#x, want 0xA5A53CA5", got)
	}
}

func TestUnmappedReadsZero(t *testing.T) {
	b := New()
	if got := b.Read32(0x10000000); got != 0 {
		t.Errorf("unmapped read = %#x, want 0", got)
	}
	b.Write32(0x10000000, 0xFFFFFFFF) // must not panic or stick
	if got := b.Read32(0x10000000); got != 0 {
		t.Errorf("unmapped write stuck: %#x", got)
	}
}

func TestWidthsCompose(t *testing.T) {
	b := New()
	b.Write32(0x08000000, 0x01020304)
	if got := b.Read8(0x08000000); got != 0x04 {
		t.Errorf("byte 0 = %#x, want 0x04", got)
	}
	if got := b.Read16(0x08000002); got != 0x0102 {
		t.Errorf("half at +2 = %#x, want 0x0102", got)
	}
}
