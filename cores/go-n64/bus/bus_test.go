package bus

import "testing"

func TestRDRAMAccessWidthsAndMirror(t *testing.T) {
	m := New(nil, false)
	m.Write(0x100, 4, 0xAABBCCDD)
	if got := m.Read(0x100, 4); got != 0xAABBCCDD {
		t.Errorf("word: %08x", got)
	}
	if got := m.Read(0x100, 2); got != 0xAABB {
		t.Errorf("half at +0: %04x", got)
	}
	if got := m.Read(0x102, 2); got != 0xCCDD {
		t.Errorf("half at +2: %04x", got)
	}
	if got := m.Read(0x101, 1); got != 0xBB {
		t.Errorf("byte: %02x", got)
	}
	if got := m.Read(0x100, 8); got != 0xAABBCCDD00000000 {
		t.Errorf("double: %016x", got)
	}
	// 4 MB mirror: 0x400100 aliases 0x100.
	if got := m.Read(0x400100, 4); got != 0xAABBCCDD {
		t.Errorf("mirror: %08x", got)
	}
	m.Write(0x800102, 2, 0x1234)
	if got := m.Read(0x102, 2); got != 0x1234 {
		t.Errorf("mirror write: %04x", got)
	}
}

func TestExpansionPakSize(t *testing.T) {
	m := New(nil, true)
	if len(m.RDRAM) != 8<<20 {
		t.Fatalf("expansion size %d", len(m.RDRAM))
	}
	m.Write(0x600000, 4, 0xDEADBEEF)
	if got := m.Read(0x600000, 4); got != 0xDEADBEEF {
		t.Errorf("expansion read: %08x", got)
	}
	// With 8 MB, 0xE00000 mirrors 0x600000.
	if got := m.Read(0xE00000, 4); got != 0xDEADBEEF {
		t.Errorf("expansion mirror: %08x", got)
	}
}

func TestMIMaskPairedSemanticsAndLine(t *testing.T) {
	m := New(nil, false)
	line := false
	m.OnMIChange = func(active bool) { line = active }
	// Set the SP mask bit: MI_MASK write with bit 1 set.
	m.Write(0x0430000C, 4, 1<<1)
	if m.readMI(0x0C)&MIIntrSP == 0 {
		t.Fatal("SP mask not set")
	}
	if line {
		t.Fatal("line asserted with no interrupt pending")
	}
	m.SetMI(MIIntrSP, true)
	if !line {
		t.Fatal("line not asserted with SP pending and unmasked")
	}
	// Clear the SP mask bit with bit 0.
	m.Write(0x0430000C, 4, 1<<0)
	if line {
		t.Fatal("line still asserted after mask clear")
	}
	// MI_VERSION reads the documented value.
	if got := m.Read(0x04300004, 4); got != 0x02020102 {
		t.Errorf("version: %08x", got)
	}
}

func TestPIDMACartToRDRAM(t *testing.T) {
	rom := make([]byte, 0x2000)
	for i := range rom {
		rom[i] = byte(i*3 + 5)
	}
	m := New(rom, false)
	line := false
	m.OnMIChange = func(active bool) { line = active }
	// Unmask PI interrupts.
	m.Write(0x0430000C, 4, 1<<9)       // PI mask set bit is 2*4+1 = 9
	m.Write(0x04600000, 4, 0x00200000) // PI_DRAM_ADDR
	m.Write(0x04600004, 4, 0x10001000) // PI_CART_ADDR
	m.Write(0x04600008, 4, 63)         // PI_RD_LEN: 64 bytes
	for i := 0; i < 64; i++ {
		if m.RDRAM[0x200000+i] != rom[0x1000+i] {
			t.Fatalf("dma byte %d: got %02x want %02x", i, m.RDRAM[0x200000+i], rom[0x1000+i])
		}
	}
	if m.readMI(0x08)&MIIntrPI == 0 {
		t.Error("PI interrupt not raised")
	}
	if !line {
		t.Error("MI line not asserted after PI DMA")
	}
	if got := m.Read(0x04600010, 4); got&1 != 0 {
		t.Error("PI status reports DMA busy after instant transfer")
	}
	// Clear the PI interrupt through PI_STATUS bit 1.
	m.Write(0x04600010, 4, 0x2)
	if m.readMI(0x08)&MIIntrPI != 0 {
		t.Error("PI interrupt not cleared")
	}
}

func TestPIDMAOddLengthRoundsUp(t *testing.T) {
	rom := make([]byte, 0x1000)
	for i := range rom {
		rom[i] = 0xA5
	}
	m := New(rom, false)
	m.Write(0x04600000, 4, 0x1000)
	m.Write(0x04600004, 4, 0x10000000)
	m.Write(0x04600008, 4, 62) // 63 bytes requested, rounds to 64
	if m.RDRAM[0x1000+63] != 0xA5 {
		t.Error("64th byte not transferred (odd length round-up)")
	}
}

func TestSPMemAndPIFRAMWindows(t *testing.T) {
	m := New(nil, false)
	m.Write(0x04000000, 4, 0x11223344) // SP DMEM
	m.Write(0x04001000, 4, 0x55667788) // SP IMEM
	if got := m.Read(0x04000000, 4); got != 0x11223344 {
		t.Errorf("dmem: %08x", got)
	}
	if got := m.Read(0x04001000, 4); got != 0x55667788 {
		t.Errorf("imem: %08x", got)
	}
	m.Write(0x1FC007C0, 4, 0xCAFEBABE) // PIF RAM
	if got := m.Read(0x1FC007C0, 4); got != 0xCAFEBABE {
		t.Errorf("pif ram: %08x", got)
	}
	// PIF ROM region reads as zero in this slice.
	if got := m.Read(0x1FC00000, 4); got != 0 {
		t.Errorf("pif rom: %08x", got)
	}
	// ROM window is read-only.
	m2 := New([]byte{0x80, 0x37, 0x12, 0x40}, false)
	m2.Write(0x10000000, 4, 0xFFFFFFFF)
	if got := m2.Read(0x10000000, 4); got != 0x80371240 {
		t.Errorf("rom changed: %08x", got)
	}
}
