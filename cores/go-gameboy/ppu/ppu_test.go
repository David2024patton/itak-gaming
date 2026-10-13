package ppu

import "testing"

func irqCounter() (func(bit int), *int, *int) {
	vbl, stat := 0, 0
	return func(bit int) {
		if bit == IRQVBlank {
			vbl++
		} else {
			stat++
		}
	}, &vbl, &stat
}

func TestFrameLengthAndModes(t *testing.T) {
	p := New()
	irq, vbl, _ := irqCounter()
	p.Reset()
	if p.Mode() != 2 || p.LY() != 0 {
		t.Fatalf("after reset: mode=%d line=%d, want mode 2 line 0", p.Mode(), p.LY())
	}
	tick := func(n int) {
		for i := 0; i < n; i++ {
			p.Tick(1, irq)
		}
	}
	tick(79)
	if p.Mode() != 2 {
		t.Fatalf("dot 79: mode=%d, want 2 (OAM scan)", p.Mode())
	}
	tick(1) // dot 80
	if p.Mode() != 3 {
		t.Fatalf("dot 80: mode=%d, want 3 (transfer)", p.Mode())
	}
	tick(172) // dot 252
	if p.Mode() != 0 {
		t.Fatalf("dot 252: mode=%d, want 0 (HBlank)", p.Mode())
	}
	tick(456 - 252) // end of line 0
	if p.LY() != 1 || p.Mode() != 2 {
		t.Fatalf("line wrap: line=%d mode=%d, want line 1 mode 2", p.LY(), p.Mode())
	}
	// Run to the end of the visible frame: VBlank IRQ exactly
	// once, at line 144.
	tick(456*143 + 456 - 456) // through line 143 into 144
	if *vbl != 1 {
		t.Fatalf("vblank IRQs = %d, want 1", *vbl)
	}
	if p.LY() != 144 || p.Mode() != 1 {
		t.Fatalf("vblank: line=%d mode=%d, want 144/1", p.LY(), p.Mode())
	}
	// Finish the frame: total dots from reset must be 70224.
	tick(456 * 10)
	if p.LY() != 0 {
		t.Fatalf("after full frame: line=%d, want 0", p.LY())
	}
}

func TestVRAMOAMAccessBlocking(t *testing.T) {
	p := New()
	irq, _, _ := irqCounter()
	p.Reset()
	// Move to HBlank, where both memories are CPU-accessible.
	for p.Mode() != 0 {
		p.Tick(1, irq)
	}
	p.Write(0x8000, 0xAB, irq)
	p.Write(0xFE00, 0xCD, irq)
	if got := p.Read(0x8000); got != 0xAB {
		t.Fatalf("VRAM read in mode 0 = 0x%02X, want 0xAB", got)
	}
	// Line 1 mode 2 (OAM scan): OAM blocked, VRAM open.
	for p.Mode() != 2 {
		p.Tick(1, irq)
	}
	if got := p.Read(0xFE00); got != 0xFF {
		t.Fatalf("OAM read in mode 2 = 0x%02X, want 0xFF", got)
	}
	if got := p.Read(0x8000); got != 0xAB {
		t.Fatalf("VRAM read in mode 2 = 0x%02X, want 0xAB", got)
	}
	p.Write(0xFE00, 0x11, irq) // ignored
	// Mode 3 (transfer): both blocked.
	for p.Mode() != 3 {
		p.Tick(1, irq)
	}
	if got := p.Read(0x8000); got != 0xFF {
		t.Fatalf("VRAM read in mode 3 = 0x%02X, want 0xFF", got)
	}
	p.Write(0x8000, 0x55, irq) // ignored
	// HBlank again: both open, and the ignored writes never landed.
	for p.Mode() != 0 {
		p.Tick(1, irq)
	}
	if got := p.Read(0x8000); got != 0xAB {
		t.Fatalf("VRAM after mode 3 = 0x%02X, want 0xAB (write was blocked)", got)
	}
	if got := p.Read(0xFE00); got != 0xCD {
		t.Fatalf("OAM after mode 2 = 0x%02X, want 0xCD (write was blocked)", got)
	}
}

func TestLCDOffFreezesDisplay(t *testing.T) {
	p := New()
	irq, vbl, stat := irqCounter()
	p.Reset()
	for i := 0; i < 1000; i++ {
		p.Tick(1, irq)
	}
	p.Write(0xFF40, 0x11, irq) // LCD off, BG on
	if got := p.Read(0xFF44); got != 0 {
		t.Fatalf("LY with LCD off = %d, want 0", got)
	}
	if p.Mode() != 0 {
		t.Fatalf("mode with LCD off = %d, want 0", p.Mode())
	}
	v, s := *vbl, *stat
	for i := 0; i < DotsPerFrame; i++ {
		p.Tick(1, irq)
	}
	if *vbl != v || *stat != s {
		t.Fatalf("interrupts fired with LCD off: vbl %d->%d stat %d->%d", v, *vbl, s, *stat)
	}
	p.Write(0xFF40, 0x91, irq) // back on at line 0
	if p.LY() != 0 {
		t.Fatalf("line after LCD on = %d, want 0", p.LY())
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	p := New()
	irq, _, _ := irqCounter()
	p.Reset()
	p.Write(0x8010, 0x3C, irq)
	p.Write(0xFF47, 0xE4, irq)
	for i := 0; i < 30000; i++ {
		p.Tick(1, irq)
	}
	buf := make([]byte, SerializeSize)
	if err := p.Serialize(buf); err != nil {
		t.Fatal(err)
	}
	q := New()
	if err := q.Deserialize(buf); err != nil {
		t.Fatal(err)
	}
	if q.LY() != p.LY() || q.Mode() != p.Mode() {
		t.Fatalf("restored line/mode = %d/%d, want %d/%d", q.LY(), q.Mode(), p.LY(), p.Mode())
	}
	buf2 := make([]byte, SerializeSize)
	if err := q.Serialize(buf2); err != nil {
		t.Fatal(err)
	}
	for i := range buf {
		if buf[i] != buf2[i] {
			t.Fatalf("state mismatch at byte %d: 0x%02X vs 0x%02X", i, buf[i], buf2[i])
		}
	}
	// Both PPUs must finish the frame identically.
	for i := 0; i < DotsPerFrame; i++ {
		p.Tick(1, irq)
		q.Tick(1, irq)
	}
	fa, fb := p.Framebuffer(), q.Framebuffer()
	for i := range fa {
		if fa[i] != fb[i] {
			t.Fatalf("framebuffer mismatch at pixel %d after restore", i)
		}
	}
}
