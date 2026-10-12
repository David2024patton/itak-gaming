package vdp

import "testing"

// writeReg performs a single-word register write through the control
// port, the way a 68k program sets a VDP register.
func writeReg(v *VDP, reg, val uint8) {
	v.WritePort(0x04, 0x8000|uint16(reg)<<8|uint16(val))
}

// writeCommand performs the two-word control port sequence that sets
// the address and access code.
func writeCommand(v *VDP, first, second uint16) {
	v.WritePort(0x04, first)
	v.WritePort(0x04, second)
}

func TestRegisterWriteViaControlPort(t *testing.T) {
	v := New()
	v.WritePort(0x04, 0x8164) // register 1 = 0x64
	if v.Regs[1] != 0x64 {
		t.Fatalf("Regs[1] = %#02x, want 0x64", v.Regs[1])
	}
	if v.pending {
		t.Fatal("register write left a pending command")
	}

	// A status read must clear a latched first command word.
	v.WritePort(0x04, 0x4000)
	if !v.pending {
		t.Fatal("first command word did not latch")
	}
	if status := v.ReadPort(0x04); status != 0x3600 {
		t.Fatalf("status = %#04x, want 0x3600 (base | FIFO empty)", status)
	}
	if v.pending {
		t.Fatal("status read did not clear the pending latch")
	}
}

func TestVRAMWriteByteSwapAndAutoIncrement(t *testing.T) {
	v := New()
	writeReg(v, 15, 2)              // auto-increment = 2
	writeCommand(v, 0x4000, 0x0000) // VRAM write at 0x0000

	v.WritePort(0x00, 0x1234)
	if v.VRAM[0] != 0x34 || v.VRAM[1] != 0x12 {
		t.Fatalf("VRAM[0:2] = %02x %02x, want 34 12 (byte-swapped)", v.VRAM[0], v.VRAM[1])
	}
	if v.addr != 2 {
		t.Fatalf("addr = %#x, want 2 after auto-increment", v.addr)
	}

	v.WritePort(0x00, 0xABCD)
	if v.VRAM[2] != 0xCD || v.VRAM[3] != 0xAB {
		t.Fatalf("VRAM[2:4] = %02x %02x, want cd ab", v.VRAM[2], v.VRAM[3])
	}
	if v.addr != 4 {
		t.Fatalf("addr = %#x, want 4", v.addr)
	}

	// Reading back through the data port returns the written words:
	// the first read after a read command yields the prefetched word.
	writeCommand(v, 0x0000, 0x0000) // VRAM read at 0x0000
	if got := v.ReadPort(0x00); got != 0x1234 {
		t.Fatalf("first VRAM read = %#04x, want 0x1234", got)
	}
	if got := v.ReadPort(0x00); got != 0xABCD {
		t.Fatalf("second VRAM read = %#04x, want 0xabcd", got)
	}
}

func TestCRAMWrite(t *testing.T) {
	v := New()
	writeReg(v, 15, 2)
	writeCommand(v, 0xC000, 0x0000) // CRAM write at 0x00

	v.WritePort(0x00, 0x0ABC)
	if v.CRAM[0] != 0x0A || v.CRAM[1] != 0xBC {
		t.Fatalf("CRAM[0:2] = %02x %02x, want 0a bc (big-endian, no swap)", v.CRAM[0], v.CRAM[1])
	}
	v.WritePort(0x00, 0x000E)
	if v.CRAM[2] != 0x00 || v.CRAM[3] != 0x0E {
		t.Fatalf("CRAM[2:4] = %02x %02x, want 00 0e", v.CRAM[2], v.CRAM[3])
	}
}

func TestDMAFrom68kToVRAM(t *testing.T) {
	v := New()
	var calls []uint32
	v.SetDMASource(func(addr uint32) uint16 {
		calls = append(calls, addr)
		return 0x5000 + uint16(addr)
	})

	writeReg(v, 1, 0x10)  // DMA enable
	writeReg(v, 19, 2)    // length = 2 words
	writeReg(v, 20, 0)    // length high
	writeReg(v, 21, 0x10) // source word address low: byte address 0x20
	writeReg(v, 22, 0)    // source mid
	writeReg(v, 23, 0)    // source high + mode 0 (68k transfer)

	// VRAM write command to address 0 with CD5 (DMA) set: the DMA bit
	// rides in bit 7 of the second command word.
	writeCommand(v, 0x4000, 0x0080)

	if len(calls) != 2 || calls[0] != 0x20 || calls[1] != 0x22 {
		t.Fatalf("DMA source calls = %v, want [0x20 0x22]", calls)
	}
	// Words 0x5020 and 0x5022 land byte-swapped in VRAM.
	want := []uint8{0x20, 0x50, 0x22, 0x50}
	for i, b := range want {
		if v.VRAM[i] != b {
			t.Fatalf("VRAM[%d] = %#02x, want %#02x", i, v.VRAM[i], b)
		}
	}
	if v.addr != 4 {
		t.Fatalf("addr = %#x, want 4 after a 2-word DMA", v.addr)
	}
}

func TestRenderSmoke(t *testing.T) {
	v := New()
	writeReg(v, 1, 0x40)  // display enable
	writeReg(v, 2, 0x08)  // plane A name table at 0x2000
	writeReg(v, 4, 0x07)  // plane B name table at 0xE000
	writeReg(v, 5, 0x18)  // sprite table at 0x3000 (all zero: no sprites)
	writeReg(v, 13, 0x10) // horizontal scroll table at 0x4000 (all zero)
	writeReg(v, 16, 0x00) // planes are 32x32 cells

	// Plane A, cell (0,0): tile 1, palette 0, no priority/flips. Name
	// entries are stored in VRAM in the same byte-swapped word order
	// the data port produces, so the low byte comes first.
	v.VRAM[0x2000] = 0x01
	v.VRAM[0x2001] = 0x00
	// Tile 1, row 0: first pixel uses palette entry 1, second is
	// transparent (index 0). Four bytes per tile row, high nibble is
	// the left pixel. Pattern bytes follow the same byte-swapped word
	// order as everything else in VRAM, so the byte holding pixels
	// 0-1 of the row lives at offset 33, not 32.
	v.VRAM[33] = 0x10
	// Palette 0 entry 1: full red (word 0x000E, stored big-endian).
	v.CRAM[2] = 0x00
	v.CRAM[3] = 0x0E

	v.RenderLine(0)
	fb := v.Framebuffer()
	if fb[0] != 0xF800 {
		t.Fatalf("pixel (0,0) = %#04x, want 0xf800 (full red in RGB565)", fb[0])
	}
	if fb[1] != 0x0000 {
		t.Fatalf("pixel (1,0) = %#04x, want 0x0000 (transparent shows backdrop)", fb[1])
	}
}
