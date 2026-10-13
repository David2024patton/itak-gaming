package ppu

import "testing"

func wr(p *PPU, addr uint16, v uint8) { p.WriteReg(addr, v) }

// setVMADD points the VRAM address at a word address.
func setVMADD(p *PPU, w uint16) {
	wr(p, 0x2116, uint8(w))
	wr(p, 0x2117, uint8(w>>8))
}

func TestVRAMWriteAndIncrement(t *testing.T) {
	p := New()
	wr(p, 0x2115, 0x80) // increment after high byte, step 1
	setVMADD(p, 0x0100)
	wr(p, 0x2118, 0x34)
	if p.vram[0x100] != 0x0034 {
		t.Fatalf("after low write vram = %#06x, want 0x0034", p.vram[0x100])
	}
	wr(p, 0x2119, 0x12)
	if p.vram[0x100] != 0x1234 {
		t.Fatalf("after high write vram = %#06x, want 0x1234", p.vram[0x100])
	}
	// The address incremented after the high byte only.
	setVMADD(p, 0x0101)
	wr(p, 0x2118, 0x78)
	wr(p, 0x2119, 0x56)
	if p.vram[0x101] != 0x5678 {
		t.Fatalf("vram[0x101] = %#06x, want 0x5678", p.vram[0x101])
	}
}

func TestVRAMIncrementAfterLow(t *testing.T) {
	p := New()
	wr(p, 0x2115, 0x00) // increment after low byte, step 1
	setVMADD(p, 0x0200)
	wr(p, 0x2118, 0xAA) // low of 0x0200, address advances
	wr(p, 0x2118, 0xBB) // low of 0x0201
	if p.vram[0x200] != 0x00AA || p.vram[0x201] != 0x00BB {
		t.Fatalf("vram = %#06x %#06x, want 0x00aa 0x00bb", p.vram[0x200], p.vram[0x201])
	}
}

func TestVRAMPrefetchDiscard(t *testing.T) {
	p := New()
	p.vram[0x100] = 0x1234
	p.vram[0x101] = 0x5678
	wr(p, 0x2115, 0x80)
	setVMADD(p, 0x0100)
	// First low read returns the stale (power-on) buffer
	// and refills it from the addressed word.
	if v := p.ReadReg(0x2139, 0); v != 0x00 {
		t.Fatalf("first $2139 = %#x, want stale 0x00", v)
	}
	if v := p.ReadReg(0x2139, 0); v != 0x34 {
		t.Fatalf("second $2139 = %#x, want 0x34", v)
	}
	// The low reads never touched the high buffer: the
	// first high read is stale too. Each high read
	// refills its own half from the addressed word and
	// (increment-after-high mode) advances the address:
	// the reads return 0x12 (word 0x100), then 0x56
	// (word 0x101).
	if v := p.ReadReg(0x213A, 0); v != 0x00 {
		t.Fatalf("first $213A = %#x, want stale 0x00", v)
	}
	if v := p.ReadReg(0x213A, 0); v != 0x12 {
		t.Fatalf("second $213A = %#x, want 0x12", v)
	}
	if v := p.ReadReg(0x213A, 0); v != 0x56 {
		t.Fatalf("third $213A = %#x, want 0x56", v)
	}
}

func TestVRAMTranslationMode1(t *testing.T) {
	p := New()
	wr(p, 0x2115, 0x80|0x04) // inc after high, translation 1
	setVMADD(p, 0x0108)
	wr(p, 0x2118, 0xAB)
	// Mode 1 rotates the low address byte per the
	// documented bit order (a2 a1 a0 a7 a6 a5 a4 a3):
	// 0x08 (bit a3) lands at bit 0, giving 0x01.
	if p.vram[0x101] != 0x00AB {
		t.Fatalf("vram[0x101] = %#06x, want 0x00ab", p.vram[0x101])
	}
	if p.vram[0x108] != 0 {
		t.Fatalf("vram[0x108] written despite translation")
	}
}

func TestCGRAMPairLatch(t *testing.T) {
	p := New()
	wr(p, 0x2121, 0x10) // CGADD is the color index
	wr(p, 0x2122, 0xFF) // low byte buffered
	if p.cgram[0x10] != 0 {
		t.Fatalf("color committed on low byte alone")
	}
	wr(p, 0x2122, 0x7F) // high byte commits the pair
	if p.cgram[0x10] != 0x7FFF {
		t.Fatalf("cgram[0x10] = %#06x, want 0x7fff", p.cgram[0x10])
	}
	// Read back: low then high, address advances after
	// the high read.
	wr(p, 0x2121, 0x10)
	if v := p.ReadReg(0x213B, 0); v != 0xFF {
		t.Fatalf("CGDATAREAD low = %#x, want 0xff", v)
	}
	if v := p.ReadReg(0x213B, 0); v != 0x7F {
		t.Fatalf("CGDATAREAD high = %#x, want 0x7f", v)
	}
}

func TestCGRAMWriteDroppedDuringDisplay(t *testing.T) {
	p := New()
	p.SetDisplayActive(true)
	wr(p, 0x2121, 0x00)
	wr(p, 0x2122, 0x34)
	wr(p, 0x2122, 0x12) // dropped: active display
	if p.cgram[0] != 0 {
		t.Fatalf("cgram[0] = %#06x, want 0 (write dropped)", p.cgram[0])
	}
	p.SetDisplayActive(false)
	wr(p, 0x2121, 0x00)
	wr(p, 0x2122, 0x34)
	wr(p, 0x2122, 0x12)
	if p.cgram[0] != 0x1234 {
		t.Fatalf("cgram[0] = %#06x, want 0x1234", p.cgram[0])
	}
}

func TestOAMBufferedWritesAndReads(t *testing.T) {
	p := New()
	wr(p, 0x2102, 0x00)
	wr(p, 0x2103, 0x00)
	wr(p, 0x2104, 0x11) // buffered
	if p.oam[0] != 0 {
		t.Fatalf("OAM committed on first byte alone")
	}
	wr(p, 0x2104, 0x22) // pair commits
	if p.oam[0] != 0x11 || p.oam[1] != 0x22 {
		t.Fatalf("oam = %#x %#x, want 0x11 0x22", p.oam[0], p.oam[1])
	}
	wr(p, 0x2102, 0x00)
	wr(p, 0x2103, 0x00)
	if v := p.ReadReg(0x2138, 0); v != 0x11 {
		t.Fatalf("OAMDATAREAD = %#x, want 0x11", v)
	}
	if v := p.ReadReg(0x2138, 0); v != 0x22 {
		t.Fatalf("OAMDATAREAD = %#x, want 0x22", v)
	}
}

func TestScrollLatchFormula(t *testing.T) {
	p := New()
	wr(p, 0x210D, 0x34) // BG1 HOFS low byte first
	wr(p, 0x210D, 0x12)
	if p.bg[0].hofs != 0x1234 {
		t.Fatalf("BG1 hofs = %#06x, want 0x1234", p.bg[0].hofs)
	}
	// The byte latch is shared by all eight scroll
	// registers: BG2's first write picks up its low bits
	// from BG1's last latched byte (0x12 gives 0x10 in
	// bits 3-7 and 0x02 in bits 0-2).
	wr(p, 0x210F, 0x00) // BG2 HOFS, first write
	if p.bg[1].hofs != 0x0012 {
		t.Fatalf("BG2 hofs after one write = %#06x, want 0x0012", p.bg[1].hofs)
	}
}

func TestMode7Multiply(t *testing.T) {
	p := New()
	wr(p, 0x211B, 0x00) // M7A low
	wr(p, 0x211B, 0x01) // M7A = 0x0100 (1.0 in 8.8)
	wr(p, 0x211C, 0x00) // M7B low: no multiply yet
	wr(p, 0x211C, 0x02) // M7B high byte 2: product 0x0100*2
	if v := p.ReadReg(0x2134, 0); v != 0x00 {
		t.Fatalf("MPYL = %#x, want 0x00", v)
	}
	if v := p.ReadReg(0x2135, 0); v != 0x02 {
		t.Fatalf("MPYM = %#x, want 0x02", v)
	}
	if v := p.ReadReg(0x2136, 0); v != 0x00 {
		t.Fatalf("MPYH = %#x, want 0x00", v)
	}
	// Negative multiplier: M7A = 0xFF00 (minus 1.0) times
	// high byte 0x01 gives minus 256 = 0xFFFF00.
	wr(p, 0x211B, 0x00)
	wr(p, 0x211B, 0xFF)
	wr(p, 0x211C, 0x00)
	wr(p, 0x211C, 0x01)
	got := uint32(p.ReadReg(0x2134, 0)) | uint32(p.ReadReg(0x2135, 0))<<8 | uint32(p.ReadReg(0x2136, 0))<<16
	if got != 0xFFFF00 {
		t.Fatalf("MPY = %#06x, want 0xffff00", got)
	}
}

func TestCounterLatchAndStatus(t *testing.T) {
	p := New()
	p.LatchCounters(0x123, 0x45)
	if v := p.ReadReg(0x213C, 0); v != 0x23 {
		t.Fatalf("OPHCT low = %#x, want 0x23", v)
	}
	if v := p.ReadReg(0x213C, 0); v != 0x01 {
		t.Fatalf("OPHCT high = %#x, want 0x01", v)
	}
	if v := p.ReadReg(0x213D, 0); v != 0x45 {
		t.Fatalf("OPVCT low = %#x, want 0x45", v)
	}
	// STAT77 version 1, STAT78 version 3 with latch flag
	// set; reading STAT78 clears the latch flag.
	if v := p.ReadReg(0x213E, 0); v != 0x01 {
		t.Fatalf("STAT77 = %#x, want 0x01", v)
	}
	if v := p.ReadReg(0x213F, 0); v != 0x83 {
		t.Fatalf("STAT78 = %#x, want 0x83", v)
	}
	if v := p.ReadReg(0x213F, 0); v != 0x03 {
		t.Fatalf("STAT78 after read = %#x, want 0x03 (latch cleared)", v)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	p := New()
	wr(p, 0x2115, 0x80)
	setVMADD(p, 0x0042)
	wr(p, 0x2118, 0x5A)
	wr(p, 0x2121, 0x01)
	wr(p, 0x2122, 0x34)
	wr(p, 0x2122, 0x12)
	wr(p, 0x2100, 0x0F)
	buf := make([]byte, SerializeSize)
	p.Serialize(buf)
	q := New()
	if err := q.Deserialize(buf); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if q.vram[0x42] != 0x005A {
		t.Errorf("restored vram = %#06x", q.vram[0x42])
	}
	if q.cgram[1] != 0x1234 {
		t.Errorf("restored cgram[1] = %#06x", q.cgram[1])
	}
	if q.brightness != 15 || q.forcedBlank {
		t.Errorf("restored display state = %d/%v", q.brightness, q.forcedBlank)
	}
	// The PPU block carries no version byte of its own
	// (the system state has the version); a truncated
	// block must be refused. (TestSerializeRoundTrip's
	// state version lives in package system.)
	if err := q.Deserialize(buf[:100]); err == nil {
		t.Errorf("Deserialize accepted a truncated block")
	}
}
