package bus

import (
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-nds/chips/armcore"
)

func TestMainRAMSharedAndMirrored(t *testing.T) {
	b := New()
	b.ARM9().Write(0x02000004, 4, 0xDEADBEEF)
	if got := b.ARM7().Read(0x02000004, 4); got != 0xDEADBEEF {
		t.Errorf("ARM7 main RAM read: got %08x", got)
	}
	// 4 MB mirrored across the 16 MB window.
	if got := b.ARM9().Read(0x02400004, 4); got != 0xDEADBEEF {
		t.Errorf("main RAM mirror: got %08x", got)
	}
	if got := b.ARM7().Read(0x02C00004, 4); got != 0xDEADBEEF {
		t.Errorf("main RAM mirror 2: got %08x", got)
	}
}

func TestSharedWRAMOwnership(t *testing.T) {
	b := New()
	// Mode 3: ARM9 owns both banks.
	b.SetWRAMCNT(3)
	b.ARM9().Write(0x03000000, 4, 0x11111111)
	b.ARM9().Write(0x03004000, 4, 0x22222222)
	if b.Shared[0] != 0x11 || b.Shared[0x4000] != 0x22 {
		t.Fatalf("shared backing: %02x %02x", b.Shared[0], b.Shared[0x4000])
	}
	// ARM7 sees open bus, not the ARM9 values.
	if got := b.ARM7().Read(0x03000000, 4); got == 0x11111111 {
		t.Error("ARM7 read an ARM9-owned bank")
	}
	// Mode 0: ARM7 owns both banks, same backing cells.
	b.SetWRAMCNT(0)
	if got := b.ARM7().Read(0x03000000, 4); got != 0x11111111 {
		t.Errorf("mode 0 ARM7 read bank 0: got %08x", got)
	}
	if got := b.ARM7().Read(0x03004000, 4); got != 0x22222222 {
		t.Errorf("mode 0 ARM7 read bank 1: got %08x", got)
	}
	if got := b.ARM9().Read(0x03000000, 4); got == 0x11111111 {
		t.Error("ARM9 read an ARM7-owned bank")
	}
	// Mode 1: split, bank 0 ARM7, bank 1 ARM9. Each side's
	// window packs its own bank at offset 0.
	b.SetWRAMCNT(1)
	if got := b.ARM7().Read(0x03000000, 4); got != 0x11111111 {
		t.Errorf("mode 1 ARM7 bank: got %08x", got)
	}
	if got := b.ARM9().Read(0x03000000, 4); got != 0x22222222 {
		t.Errorf("mode 1 ARM9 packed bank: got %08x", got)
	}
}

func TestTCMWindows(t *testing.T) {
	b := New()
	cpu := armcore.NewARM9(b.ARM9())
	b.AttachCPU9(cpu)
	// Reset config: ITCM 32 KB at 0, DTCM 16 KB at 0x00800000.
	b.ARM9().Write(0x00000100, 4, 0xAAAA0001)
	if got := readWord(b.ITCM, 0x100); got != 0xAAAA0001 {
		t.Errorf("ITCM write landed at %08x", got)
	}
	b.ARM9().Write(0x00800040, 4, 0xBBBB0002)
	if got := readWord(b.DTCM, 0x40); got != 0xBBBB0002 {
		t.Errorf("DTCM write landed at %08x", got)
	}
	// The ARM7 cannot see the ITCM window: its 0x00000100 is the
	// BIOS window, which reads open bus.
	if got := b.ARM7().Read(0x00000100, 4); got == 0xAAAA0001 {
		t.Error("ARM7 read into the ARM9 ITCM")
	}
	// Reprogramming the DTCM base through CP15 moves the window.
	cpu.CP15Write(9, 1, 0, 0x02000000|5<<1)
	b.ARM9().Write(0x02000080, 4, 0xCCCC0003)
	if got := readWord(b.DTCM, 0x80); got != 0xCCCC0003 {
		t.Errorf("moved DTCM write landed at %08x", got)
	}
	if got := readWord(b.MainRAM, 0x80); got == 0xCCCC0003 {
		t.Error("moved DTCM shadowed by main RAM write")
	}
}

func TestCartWindowOwnership(t *testing.T) {
	b := New()
	rom := []byte{0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80}
	b.SetROM(rom)
	// Default EXMEMCNT: ARM9 owns the window.
	if got := b.ARM9().Read(0x08000000, 4); got != 0x40302010 {
		t.Errorf("ARM9 cart read: got %08x", got)
	}
	if got := b.ARM7().Read(0x08000000, 4); got == 0x40302010 {
		t.Error("ARM7 read the cart window while ARM9 owns it")
	}
	// Hand ownership to the ARM7: EXMEMCNT bit 7 set. A fresh
	// bus is used for the ARM9 denial check so the ARM9 open-bus
	// value cannot coincidentally equal the ROM contents.
	b.IO9[ioEXMEMCNT] = 0x80
	if got := b.ARM7().Read(0x08000000, 4); got != 0x40302010 {
		t.Errorf("ARM7 cart read: got %08x", got)
	}
	b2 := New()
	b2.SetROM(rom)
	b2.IO9[ioEXMEMCNT] = 0x80
	if got := b2.ARM9().Read(0x08000000, 4); got == 0x40302010 {
		t.Error("ARM9 read the cart window while ARM7 owns it")
	}
	// The ARM7 reads the same EXMEMCNT value from its own page.
	if got := b.ARM7().Read(0x04000204, 2); got != 0x80 {
		t.Errorf("ARM7 EXMEMCNT read: got %04x", got)
	}
	// And cannot change it.
	b.ARM7().Write(0x04000204, 2, 0)
	if b.EXMEMCNT() != 0x80 {
		t.Errorf("ARM7 wrote EXMEMCNT: now %04x", b.EXMEMCNT())
	}
}

func TestARM7PrivateWRAMAndIO(t *testing.T) {
	b := New()
	b.ARM7().Write(0x03800100, 4, 0x77777777)
	if got := readWord(b.ARM7WRAM, 0x100); got != 0x77777777 {
		t.Errorf("ARM7 WRAM: got %08x", got)
	}
	// Mirrored across its 8 MB window.
	if got := b.ARM7().Read(0x03840100, 4); got != 0x77777777 {
		t.Errorf("ARM7 WRAM mirror: got %08x", got)
	}
	// Sound registers are plain IO storage on the ARM7 page.
	b.ARM7().Write(0x04000400, 4, 0x12345678)
	if got := b.ARM7().Read(0x04000400, 4); got != 0x12345678 {
		t.Errorf("ARM7 IO storage: got %08x", got)
	}
	// DISPCNT is plain IO storage on the ARM9 page.
	b.ARM9().Write(0x04000000, 4, 0x00020001)
	if got := b.ARM9().Read(0x04000000, 4); got != 0x00020001 {
		t.Errorf("ARM9 IO storage: got %08x", got)
	}
	// VRAM bank B lands in the flat store at its LCDC offset.
	b.ARM9().Write(0x06820000, 4, 0x99999999)
	if got := readWord(b.VRAMBank(1), 0); got != 0x99999999 {
		t.Errorf("VRAM bank B: got %08x", got)
	}
}

func TestBusSerializeRoundTrip(t *testing.T) {
	b := New()
	b.ARM9().Write(0x02000000, 4, 0x13572468)
	b.SetWRAMCNT(2)
	buf := make([]byte, StateSize)
	if n := b.Serialize(buf); n != StateSize {
		t.Fatalf("Serialize wrote %d, StateSize %d", n, StateSize)
	}
	d := New()
	if !d.Deserialize(buf) {
		t.Fatal("Deserialize refused")
	}
	if got := d.ARM9().Read(0x02000000, 4); got != 0x13572468 {
		t.Errorf("main RAM round trip: %08x", got)
	}
	if d.WRAMCNT() != 2 {
		t.Errorf("WRAMCNT round trip: %d", d.WRAMCNT())
	}
	if d.Deserialize(buf[:StateSize-1]) {
		t.Error("Deserialize accepted a short state")
	}
}

func readWord(mem []byte, off int) uint32 {
	return uint32(mem[off]) | uint32(mem[off+1])<<8 | uint32(mem[off+2])<<16 | uint32(mem[off+3])<<24
}
