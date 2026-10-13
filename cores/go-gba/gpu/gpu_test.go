package gpu

import (
	"bytes"
	"testing"
)

// Test colours in 15 bit form and their packed output
// form, worked out by hand from the channel expansion
// rule (v<<3)|(v>>2).
const (
	c555Red   = 0x001F
	c555Green = 0x03E0
	c555Blue  = 0x7C00
	c555White = 0x7FFF
	c555Yel   = 0x03FF
	c555Cyan  = 0x7FE0
	c555Mag   = 0x7C1F

	pxRed   = 0xFFFF0000
	pxGreen = 0xFF00FF00
	pxBlue  = 0xFF0000FF
	pxWhite = 0xFFFFFFFF
	pxYel   = 0xFFFFFF00
	pxCyan  = 0xFF00FFFF
)

func newRig() (*GPU, []byte, []byte, []byte) {
	pal := make([]byte, 0x400)
	vram := make([]byte, 0x18000)
	oam := make([]byte, 0x400)
	return New(pal, vram, oam), pal, vram, oam
}

func setPal(pal []byte, idx int, val uint16) {
	pal[idx*2] = byte(val)
	pal[idx*2+1] = byte(val >> 8)
}

func setObjPal(pal []byte, idx int, val uint16) {
	off := 0x200 + idx*2
	pal[off] = byte(val)
	pal[off+1] = byte(val >> 8)
}

func putVRAM16(vram []byte, off int, val uint16) {
	vram[off] = byte(val)
	vram[off+1] = byte(val >> 8)
}

func putOAM16(oam []byte, off int, val uint16) {
	oam[off] = byte(val)
	oam[off+1] = byte(val >> 8)
}

// disableSpritesExcept marks every sprite except the
// named one as disabled, since zeroed sprite entries
// are otherwise live sprites stacked at the origin.
func disableSpritesExcept(oam []byte, keep int) {
	for i := 0; i < 128; i++ {
		if i != keep {
			putOAM16(oam, i*8, 1<<9)
		}
	}
}

// renderRow renders one line and returns a copy of that
// row of the framebuffer.
func renderRow(g *GPU, line int) []uint32 {
	g.SetLine(line)
	g.BeginLine()
	g.RenderLine()
	row := make([]uint32, Width)
	copy(row, g.Framebuffer()[line*Width:(line+1)*Width])
	return row
}

// setupTextBG0 points BG0 at screen block 1 with
// character base 0 and feeds it a single screen entry
// for tile 0. prio is the BG0 priority value.
func setupTextBG0(g *GPU, vram []byte, prio uint16) {
	putVRAM16(vram, 0x800, 0) // entry: tile 0, no flips, bank 0
	g.WriteReg(0x08, 2, uint32((1<<8)|prio))
	g.WriteReg(0x00, 2, 0x0100) // mode 0, BG0 on
}

func TestTextTilePixel(t *testing.T) {
	t.Run("4bpp pixels and transparency", func(t *testing.T) {
		g, pal, vram, _ := newRig()
		setPal(pal, 0, c555Blue) // backdrop
		setPal(pal, 1, c555Red)
		setPal(pal, 2, c555Green)
		// Tile 0, row 0: pixels are indices 1, 2, 0, 0.
		vram[0] = 0x21
		vram[1] = 0x00
		setupTextBG0(g, vram, 0)
		row := renderRow(g, 0)
		if row[0] != pxRed {
			t.Errorf("pixel (0,0) = %#08x, want red %#08x", row[0], pxRed)
		}
		if row[1] != pxGreen {
			t.Errorf("pixel (1,0) = %#08x, want green %#08x", row[1], pxGreen)
		}
		if row[2] != pxBlue {
			t.Errorf("pixel (2,0) = %#08x, want backdrop blue %#08x", row[2], pxBlue)
		}
	})

	t.Run("8bpp tile pixel", func(t *testing.T) {
		g, pal, vram, _ := newRig()
		setPal(pal, 0, c555Blue)
		setPal(pal, 5, c555Yel)
		setPal(pal, 6, c555Cyan)
		vram[0] = 5 // tile 0 texel (0,0) in 8bpp
		vram[1] = 6 // texel (1,0)
		putVRAM16(vram, 0x800, 0)
		g.WriteReg(0x08, 2, (1<<8)|(1<<7)) // screen base 1, 8bpp
		g.WriteReg(0x00, 2, 0x0100)
		row := renderRow(g, 0)
		if row[0] != pxYel {
			t.Errorf("pixel (0,0) = %#08x, want yellow %#08x", row[0], pxYel)
		}
		if row[1] != pxCyan {
			t.Errorf("pixel (1,0) = %#08x, want cyan %#08x", row[1], pxCyan)
		}
	})

	t.Run("4bpp palette bank selection", func(t *testing.T) {
		g, pal, vram, _ := newRig()
		setPal(pal, 0, c555Blue)
		setPal(pal, 3, c555Red)    // bank 0 index 3: the wrong colour
		setPal(pal, 35, c555Green) // bank 2 index 3: the right colour
		vram[0] = 0x03             // texel (0,0) is index 3
		putVRAM16(vram, 0x800, 2<<12)
		g.WriteReg(0x08, 2, 1<<8)
		g.WriteReg(0x00, 2, 0x0100)
		row := renderRow(g, 0)
		if row[0] != pxGreen {
			t.Errorf("pixel (0,0) = %#08x, want bank 2 green %#08x", row[0], pxGreen)
		}
	})
}

func TestPriority(t *testing.T) {
	build := func(bgPrio, objPrio uint16) uint32 {
		g, pal, vram, oam := newRig()
		setPal(pal, 0, c555Blue)
		setPal(pal, 1, c555Red) // background pixel colour
		setObjPal(pal, 1, c555White)
		vram[0] = 0x01       // BG tile 0 texel (0,0) index 1
		vram[0x10000] = 0x01 // OBJ tile 0 texel (0,0) index 1
		setupTextBG0(g, vram, bgPrio)
		disableSpritesExcept(oam, 0)
		putOAM16(oam, 0, 0)           // attr0: y 0, 8x8, normal
		putOAM16(oam, 2, 0)           // attr1: x 0, size 0
		putOAM16(oam, 4, objPrio<<10) // attr2: tile 0, priority
		g.WriteReg(0x00, 2, 0x1100)   // BG0 and OBJ on, mode 0
		return renderRow(g, 0)[0]
	}
	t.Run("sprite wins on equal priority", func(t *testing.T) {
		if got := build(1, 1); got != pxWhite {
			t.Errorf("pixel = %#08x, want sprite white %#08x", got, pxWhite)
		}
	})
	t.Run("background wins with lower priority number", func(t *testing.T) {
		if got := build(0, 1); got != pxRed {
			t.Errorf("pixel = %#08x, want background red %#08x", got, pxRed)
		}
	})
}

func affineRig(pa, pd uint16) (*GPU, []byte) {
	g, pal, vram, _ := newRig()
	setPal(pal, 0, c555Blue)
	setPal(pal, 2, c555Red)
	setPal(pal, 3, c555Green)
	vram[0x800] = 1           // map entry at (0,0): tile 1
	vram[64] = 2              // tile 1 texel (0,0)
	vram[64+9] = 3            // tile 1 texel (1,1)
	g.WriteReg(0x0C, 2, 1<<8) // BG2CNT: screen base 1, size 0
	g.WriteReg(0x20, 2, uint32(pa))
	g.WriteReg(0x22, 2, 0)
	g.WriteReg(0x24, 2, 0)
	g.WriteReg(0x26, 2, uint32(pd))
	g.WriteReg(0x28, 4, 0)      // BG2X
	g.WriteReg(0x2C, 4, 0)      // BG2Y
	g.WriteReg(0x00, 2, 0x0401) // mode 1, BG2 on
	return g, vram
}

func TestAffineSampling(t *testing.T) {
	t.Run("identity samples the expected texel", func(t *testing.T) {
		g, _ := affineRig(0x100, 0x100)
		row := renderRow(g, 0)
		if row[0] != pxRed {
			t.Errorf("pixel (0,0) = %#08x, want texel (0,0) red %#08x", row[0], pxRed)
		}
		if row[1] != pxBlue {
			t.Errorf("pixel (1,0) = %#08x, want transparent texel over backdrop %#08x", row[1], pxBlue)
		}
	})
	t.Run("half scale reaches texel (1,1) at screen (2,2)", func(t *testing.T) {
		g, _ := affineRig(0x80, 0x80)
		renderRow(g, 0)
		renderRow(g, 1)
		row := renderRow(g, 2)
		if row[2] != pxGreen {
			t.Errorf("pixel (2,2) = %#08x, want texel (1,1) green %#08x", row[2], pxGreen)
		}
	})
}

func identityAffine(g *GPU) {
	g.WriteReg(0x20, 2, 0x100)
	g.WriteReg(0x22, 2, 0)
	g.WriteReg(0x24, 2, 0)
	g.WriteReg(0x26, 2, 0x100)
	g.WriteReg(0x28, 4, 0)
	g.WriteReg(0x2C, 4, 0)
}

func TestMode3DirectPixel(t *testing.T) {
	g, _, vram, _ := newRig()
	putVRAM16(vram, (3*240+5)*2, c555Yel)
	identityAffine(g)
	g.WriteReg(0x00, 2, 0x0403) // mode 3, BG2 on
	renderRow(g, 0)
	renderRow(g, 1)
	renderRow(g, 2)
	row := renderRow(g, 3)
	if row[5] != pxYel {
		t.Errorf("pixel (5,3) = %#08x, want yellow %#08x", row[5], pxYel)
	}
}

func TestMode4PageSelect(t *testing.T) {
	build := func(page bool) uint32 {
		g, pal, vram, _ := newRig()
		setPal(pal, 0, c555Blue)
		setPal(pal, 9, c555Red)
		setPal(pal, 7, c555Green)
		vram[0] = 9      // page 0 texel (0,0)
		vram[0xA000] = 7 // page 1 texel (0,0)
		identityAffine(g)
		dc := uint32(0x0404) // mode 4, BG2 on
		if page {
			dc |= 1 << 4
		}
		g.WriteReg(0x00, 2, dc)
		return renderRow(g, 0)[0]
	}
	if got := build(true); got != pxGreen {
		t.Errorf("page 1 pixel = %#08x, want green %#08x", got, pxGreen)
	}
	if got := build(false); got != pxRed {
		t.Errorf("page 0 pixel = %#08x, want red %#08x", got, pxRed)
	}
}

// blendRig builds BG0 over BG1 at pixel (0,0): BG0 uses
// palette index bg0Idx, BG1 uses palette index bg1Idx.
func blendRig(bg0Idx, bg1Idx byte) *GPU {
	g, pal, vram, _ := newRig()
	setPal(pal, 0, c555Blue)
	setPal(pal, 1, c555Red)
	setPal(pal, 2, c555Blue)
	setPal(pal, 3, c555White)
	vram[0] = bg0Idx                     // BG0 tile 0 texel (0,0)
	putVRAM16(vram, 0x800, 0)            // BG0 entry
	vram[0x4000] = bg1Idx                // BG1 tile 0 (char base 1) texel (0,0)
	putVRAM16(vram, 0x1000, 0)           // BG1 entry at screen base 2
	g.WriteReg(0x08, 2, (1<<8)|0)        // BG0CNT: prio 0
	g.WriteReg(0x0A, 2, (2<<8)|(1<<2)|1) // BG1CNT: screen 2, char 1, prio 1
	g.WriteReg(0x00, 2, 0x0300)          // mode 0, BG0 and BG1 on
	return g
}

func TestAlphaBlend(t *testing.T) {
	t.Run("half and half average", func(t *testing.T) {
		g := blendRig(1, 2)
		// First target BG0, mode alpha, second target BG1.
		g.WriteReg(0x50, 2, 1|(1<<6)|(1<<9))
		g.WriteReg(0x52, 2, 8|(8<<8)) // EVA 8, EVB 8
		row := renderRow(g, 0)
		// Red (31,0,0) over blue (0,0,31) at 8/16 each:
		// r and b become (31*8)/16 = 15, expanded to
		// (15<<3)|(15>>2) = 123 = 0x7B.
		want := uint32(0xFF7B007B)
		if row[0] != want {
			t.Errorf("pixel = %#08x, want blended %#08x", row[0], want)
		}
	})
	t.Run("full coefficients clamp at 31", func(t *testing.T) {
		g := blendRig(3, 3) // white over white
		g.WriteReg(0x50, 2, 1|(1<<6)|(1<<9))
		g.WriteReg(0x52, 2, 16|(16<<8)) // EVA 16, EVB 16
		row := renderRow(g, 0)
		if row[0] != pxWhite {
			t.Errorf("pixel = %#08x, want clamped white %#08x", row[0], pxWhite)
		}
	})
}

func TestBrightness(t *testing.T) {
	build := func(mode uint16) uint32 {
		g, pal, vram, _ := newRig()
		setPal(pal, 0, c555Blue)
		setPal(pal, 1, 0x4210) // channels 16, 16, 16
		vram[0] = 0x01
		setupTextBG0(g, vram, 0)
		g.WriteReg(0x50, 2, uint32(1|(mode<<6))) // first target BG0
		g.WriteReg(0x54, 2, 16)                  // EVY 16
		return renderRow(g, 0)[0]
	}
	t.Run("brighten to white", func(t *testing.T) {
		// 16 + (31-16)*16/16 = 31 per channel.
		if got := build(2); got != pxWhite {
			t.Errorf("pixel = %#08x, want white %#08x", got, pxWhite)
		}
	})
	t.Run("darken to black", func(t *testing.T) {
		// 16 - 16*16/16 = 0 per channel.
		if got := build(3); got != 0xFF000000 {
			t.Errorf("pixel = %#08x, want black %#08x", got, 0xFF000000)
		}
	})
}

func TestWindowClip(t *testing.T) {
	g, pal, vram, _ := newRig()
	setPal(pal, 0, c555Blue)
	setPal(pal, 1, c555Red)
	vram[0] = 0x01
	// Fill the whole first tile row of the map with tile 0
	// entries whose pixels are red: entry tile 0 already
	// gives red at index 1 for every texel of row 0 only,
	// so give tile 0 an all-ones row instead.
	for i := 0; i < 4; i++ {
		vram[i] = 0x11
	}
	setupTextBG0(g, vram, 0)
	g.WriteReg(0x40, 2, 120)    // WIN0H: left 0, right 120
	g.WriteReg(0x44, 2, 160)    // WIN0V: top 0, bottom 160
	g.WriteReg(0x48, 2, 0x01)   // WININ low: BG0 on inside
	g.WriteReg(0x4A, 2, 0x00)   // WINOUT low: nothing outside
	g.WriteReg(0x00, 2, 0x2100) // BG0 on, window 0 on
	row := renderRow(g, 0)
	if row[10] != pxRed {
		t.Errorf("pixel (10,0) = %#08x, want inside window red %#08x", row[10], pxRed)
	}
	if row[200] != pxBlue {
		t.Errorf("pixel (200,0) = %#08x, want outside window backdrop %#08x", row[200], pxBlue)
	}
}

func TestMosaic(t *testing.T) {
	g, pal, vram, _ := newRig()
	setPal(pal, 0, c555Blue)
	setPal(pal, 1, c555Red)
	setPal(pal, 2, c555Green)
	setPal(pal, 3, c555Yel)
	setPal(pal, 4, c555Mag)
	vram[0] = 0x21 // texels 1, 2
	vram[1] = 0x43 // texels 3, 4
	putVRAM16(vram, 0x800, 0)
	g.WriteReg(0x08, 2, (1<<8)|(1<<6)) // screen base 1, mosaic on
	g.WriteReg(0x4C, 2, 3<<8)          // BG mosaic horizontal size 3
	g.WriteReg(0x00, 2, 0x0100)
	row := renderRow(g, 0)
	for x := 0; x < 4; x++ {
		if row[x] != pxRed {
			t.Errorf("pixel (%d,0) = %#08x, want mosaic block red %#08x", x, row[x], pxRed)
		}
	}
}

func TestSemiTransparentSprite(t *testing.T) {
	g, pal, vram, oam := newRig()
	setPal(pal, 0, c555Blue)
	setPal(pal, 2, c555Blue) // background pixel colour
	setObjPal(pal, 1, c555Red)
	vram[0] = 0x02       // BG texel index 2
	vram[0x10000] = 0x01 // OBJ texel index 1
	setupTextBG0(g, vram, 1)
	disableSpritesExcept(oam, 0)
	putOAM16(oam, 0, 1<<10) // attr0: y 0, semi-transparent mode
	putOAM16(oam, 2, 0)
	putOAM16(oam, 4, 0) // attr2: tile 0, priority 0
	// Second target is BG0; the sprite counts as first
	// target on its own.
	g.WriteReg(0x50, 2, 1<<8)
	g.WriteReg(0x52, 2, 8|(8<<8))
	g.WriteReg(0x00, 2, 0x1100)
	row := renderRow(g, 0)
	want := uint32(0xFF7B007B) // same 15/0/15 mix as the alpha test
	if row[0] != want {
		t.Errorf("pixel = %#08x, want blended %#08x", row[0], want)
	}
}

func TestRegisterBehaviour(t *testing.T) {
	g, _, _, _ := newRig()
	// DISPSTAT: stored bits are the IRQ enables and the
	// line compare value; the flags come from setters.
	g.WriteReg(0x04, 2, 0x1238)
	g.SetLine(18)
	g.SetVBlank(true)
	g.SetHBlank(true)
	if got := g.ReadReg(0x04, 2); got != 0x123F {
		t.Errorf("DISPSTAT = %#04x, want 0x123f", got)
	}
	if !g.VCountMatch() {
		t.Error("VCountMatch false at line 18 with compare 0x12")
	}
	if got := g.ReadReg(0x06, 2); got != 18 {
		t.Errorf("VCOUNT = %d, want 18", got)
	}
	g.WriteReg(0x06, 2, 99) // read only: ignored
	if g.Line() != 18 {
		t.Errorf("Line after VCOUNT write = %d, want 18", g.Line())
	}
	// Scroll registers are write only for reads but keep
	// their stored value.
	g.WriteReg(0x10, 2, 0x01AB)
	if got := g.ReadReg(0x10, 2); got != 0 {
		t.Errorf("scroll read = %#04x, want 0", got)
	}
	// Byte write touches only its lane of DISPCNT.
	g.WriteReg(0x00, 2, 0x0100)
	g.WriteReg(0x01, 1, 0x12)
	if got := g.ReadReg(0x00, 2); got != 0x1200 {
		t.Errorf("DISPCNT after byte write = %#04x, want 0x1200", got)
	}
	// Affine reference: reads return zero, and a stored
	// value with bit 27 set is held as a negative
	// internal point (visible in the serialized state).
	g.WriteReg(0x28, 4, 0x0FFFFFFF)
	if got := g.ReadReg(0x28, 4); got != 0 {
		t.Errorf("BG2X read = %#08x, want 0", got)
	}
	buf := make([]byte, StateSize)
	if n := g.Serialize(buf); n != StateSize {
		t.Fatalf("Serialize returned %d, want %d", n, StateSize)
	}
	// Live references sit at byte 112 in the layout.
	if got := int32(uint32(buf[112]) | uint32(buf[113])<<8 | uint32(buf[114])<<16 | uint32(buf[115])<<24); got != -1 {
		t.Errorf("serialized BG2 ref X = %d, want -1", got)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	g, pal, vram, oam := newRig()
	setPal(pal, 0, c555Blue)
	setPal(pal, 1, c555Red)
	vram[0] = 0x01
	setupTextBG0(g, vram, 0)
	g.WriteReg(0x10, 2, 3) // BG0 horizontal scroll
	g.WriteReg(0x50, 2, 0x0081)
	g.WriteReg(0x54, 2, 4)
	for line := 0; line < 3; line++ {
		renderRow(g, line)
	}
	first := make([]byte, StateSize)
	if n := g.Serialize(first); n != StateSize {
		t.Fatalf("Serialize returned %d, want %d", n, StateSize)
	}
	// Deserialize into a fresh GPU over the same backing stores.
	fresh := New(pal, vram, oam)
	if !fresh.Deserialize(first) {
		t.Fatal("Deserialize reported false")
	}
	second := make([]byte, StateSize)
	if n := fresh.Serialize(second); n != StateSize {
		t.Fatalf("second Serialize returned %d, want %d", n, StateSize)
	}
	if !bytes.Equal(first, second) {
		t.Error("state bytes differ after a serialize round trip")
	}
	if !bytes.Equal(
		func() []byte { b, _ := frameBytes(g); return b }(),
		func() []byte { b, _ := frameBytes(fresh); return b }(),
	) {
		t.Error("framebuffers differ after a serialize round trip")
	}
}

func frameBytes(g *GPU) ([]byte, bool) {
	buf := make([]byte, StateSize)
	if g.Serialize(buf) != StateSize {
		return nil, false
	}
	return buf[136:], true
}
