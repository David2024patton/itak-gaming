package bus

import "testing"

func TestRAMMirrorsAcrossSegments(t *testing.T) {
	b := New()
	b.Write32(0x00001000, 0xAABBCCDD)
	for _, addr := range []uint32{0x00001000, 0x00201000, 0x00401000, 0x00601000, 0x80001000, 0x80201000, 0xA0201000} {
		if got := b.Read32(addr); got != 0xAABBCCDD {
			t.Errorf("Read32(0x%08X) = 0x%08X, want 0xAABBCCDD (RAM mirror)", addr, got)
		}
	}
	b.Write8(0x00000000, 0x11)
	if got := b.Read8(0x00200000); got != 0x11 {
		t.Errorf("Read8(0x00200000) = 0x%02X, want 0x11 (2 MB wrap)", got)
	}
}

func TestScratchpad(t *testing.T) {
	b := New()
	b.Write32(0x1F800000, 0x11223344)
	if got := b.Read32(0x9F800000); got != 0x11223344 {
		t.Errorf("KSEG0 scratchpad read = 0x%08X, want 0x11223344", got)
	}
	if got := b.Read32(0xBF800000); got != 0x11223344 {
		t.Errorf("KSEG1 scratchpad read = 0x%08X, want 0x11223344", got)
	}
	// The last scratchpad word does not bleed into the
	// register page.
	b.Write32(0x1F8003FC, 0x55667788)
	if got := b.Read32(0x1F8003FC); got != 0x55667788 {
		t.Errorf("scratchpad top word = 0x%08X, want 0x55667788", got)
	}
}

func TestBIOSWindowIsStub(t *testing.T) {
	b := New()
	if got := b.Read32(0xBFC00000); got != 0 {
		t.Errorf("BIOS reset vector read = 0x%08X, want 0 (HLE stub pattern)", got)
	}
	if got := b.Read32(0x1FC00180); got != 0 {
		t.Errorf("BIOS exception vector read = 0x%08X, want 0", got)
	}
	b.Write32(0xBFC00000, 0xFFFFFFFF)
	if got := b.Read32(0xBFC00000); got != 0 {
		t.Errorf("BIOS write was not dropped: read 0x%08X", got)
	}
}

func TestIORegisterStubsAndResetValues(t *testing.T) {
	b := New()
	if got := b.Read32(0x1F801060); got != ramSizeReset {
		t.Errorf("RAM_SIZE = 0x%08X, want documented reset 0x%08X", got, uint32(ramSizeReset))
	}
	// Interrupt control and a DMA channel register are
	// plain stored stubs in this slice.
	b.Write32(0x1F801070, 0x00001234)
	b.Write32(0x1F8010A0, 0xDEADBEEF)
	if got := b.Read32(0x1F801070); got != 0x1234 {
		t.Errorf("I_STAT stub = 0x%08X, want 0x1234", got)
	}
	if got := b.Read32(0x1F8010A0); got != 0xDEADBEEF {
		t.Errorf("DMA stub = 0x%08X, want 0xDEADBEEF", got)
	}
	b.Write16(0x1F801814, 0xBEEF) // inside the GPU register window
	if got := b.Read16(0x1F801814); got != 0xBEEF {
		t.Errorf("GPU window stub = 0x%04X, want 0xBEEF", got)
	}
}

func TestCacheControlRegister(t *testing.T) {
	bus := New()
	bus.Write32(cacheCtrlAdr, 0xDEADBEEF)
	if got := bus.Read32(cacheCtrlAdr); got != 0xDEADBEEF {
		t.Errorf("cache control read = 0x%08X, want 0xDEADBEEF", got)
	}
	if bus.CacheControl() != 0xDEADBEEF {
		t.Errorf("CacheControl() = 0x%08X, want 0xDEADBEEF", bus.CacheControl())
	}
	bus.Write8(cacheCtrlAdr+1, 0x00)
	if got := bus.Read32(cacheCtrlAdr); got != 0xDEAD00EF {
		t.Errorf("cache control byte write: read 0x%08X, want 0xDEAD00EF", got)
	}
}

func TestExpansionReadsOpenBus(t *testing.T) {
	b := New()
	if got := b.Read32(0x1F000000); got != 0xFFFFFFFF {
		t.Errorf("expansion 1 read = 0x%08X, want 0xFFFFFFFF (open bus)", got)
	}
	if got := b.Read8(0x1FA00000); got != 0xFF {
		t.Errorf("expansion byte read = 0x%02X, want 0xFF", got)
	}
	b.Write32(0x1F000000, 0)
	if got := b.Read32(0x1F000000); got != 0xFFFFFFFF {
		t.Errorf("expansion write changed the open bus value: 0x%08X", got)
	}
}

func TestResetClearsStores(t *testing.T) {
	b := New()
	b.Write32(0x00002000, 0xFFFFFFFF)
	b.Write32(0x1F800000, 0xFFFFFFFF)
	b.Reset()
	if got := b.Read32(0x00002000); got != 0 {
		t.Errorf("RAM after reset = 0x%08X, want 0", got)
	}
	if got := b.Read32(0x1F800000); got != 0 {
		t.Errorf("scratchpad after reset = 0x%08X, want 0", got)
	}
	if got := b.Read32(0x1F801060); got != ramSizeReset {
		t.Errorf("RAM_SIZE after reset = 0x%08X, want 0x%08X", got, uint32(ramSizeReset))
	}
}
