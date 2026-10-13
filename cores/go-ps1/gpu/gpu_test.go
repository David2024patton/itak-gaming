package gpu

import "testing"

func gp0(g *GPU, words ...uint32) {
	for _, w := range words {
		g.WriteGP0(w)
	}
}

func gp1(g *GPU, w uint32) { g.WriteGP1(w) }

// fullDraw sets the drawing area to the whole VRAM
// and the offset to 0.
func fullDraw(g *GPU) {
	gp0(g,
		0xE3000000, // E3 top-left (0,0)
		0xE407FFFF, // E4 bottom-right (1023,511)
		0xE5000000, // E5 offset (0,0)
	)
}

func countColor(g *GPU, col uint16) int {
	n := 0
	for _, v := range g.VRAM() {
		if v == col {
			n++
		}
	}
	return n
}

func TestResetStatus(t *testing.T) {
	g := New()
	if s := g.Status(); s != 0x14802000 {
		t.Errorf("GPUSTAT after reset = 0x%08X, want 0x14802000", s)
	}
}

func TestFlatTriangleCoverageRule(t *testing.T) {
	g := New()
	fullDraw(g)
	// Right triangle (0,0),(8,0),(0,8), pure red.
	gp0(g, 0x200000FF, 0x00000000, 0x00000008, 0x00080000)
	if n := countColor(g, 0x001F); n != 36 {
		t.Errorf("red pixels = %d, want 36 (lower-right edges excluded)", n)
	}
	if g.VRAMAt(7, 0) != 0x001F || g.VRAMAt(0, 7) != 0x001F || g.VRAMAt(0, 0) != 0x001F {
		t.Errorf("top/left edge pixels missing")
	}
	if g.VRAMAt(8, 0) != 0 || g.VRAMAt(0, 8) != 0 || g.VRAMAt(4, 4) != 0 {
		t.Errorf("lower-right edge pixels drawn but must be excluded")
	}
}

func TestGouraudInterpolationExact(t *testing.T) {
	g := New()
	fullDraw(g)
	// (0,0) red, (9,0) green, (0,9) blue.
	gp0(g,
		0x300000FF, 0x00000000, // cmd+color0, v0
		0x0000FF00, 0x00000009, // color1, v1
		0x00FF0000, 0x00090000, // color2, v2
	)
	// Barycentric weights at (2,2) are 45/18/18 of 81:
	// r=142, g=57, b=57 in 8-bit space, quantized to
	// 17/7/7.
	if got := g.VRAMAt(2, 2); got != 0x1CF1 {
		t.Errorf("pixel (2,2) = 0x%04X, want 0x1CF1", got)
	}
}

func TestQuadSplitsAsTwoTriangles(t *testing.T) {
	g := New()
	fullDraw(g)
	// Quad (0,0),(8,0),(0,8),(8,8) green.
	gp0(g, 0x2800FF00, 0x00000000, 0x00000008, 0x00080000, 0x00080008)
	// Union of triangles (0,0)(8,0)(0,8) and
	// (8,0)(0,8)(8,8): only the shared-diagonal
	// pixels and the outer lower-right corners are
	// excluded; spot check interior pixels.
	for _, pt := range [][2]int{{0, 0}, {7, 1}, {1, 7}, {7, 7}, {4, 4}} {
		if g.VRAMAt(pt[0], pt[1]) != 0x03E0 {
			t.Errorf("quad pixel %v = 0x%04X, want 0x03E0", pt, g.VRAMAt(pt[0], pt[1]))
		}
	}
	if g.VRAMAt(8, 0) != 0 || g.VRAMAt(0, 8) != 0 || g.VRAMAt(8, 8) != 0 {
		t.Errorf("quad outer lower-right corners drawn but must be excluded")
	}
}

func TestSemiTransparencyModes(t *testing.T) {
	backdrop := uint16(0x294A) // (10,10,10)
	front := uint32(0x00F8F8F8)
	want := []uint16{
		0x5294, // B/2 + F/2: 5 + 15 = 20 per channel
		0x7FFF, // B + F clamped: 31
		0x0000, // B - F clamped: 0
		0x4631, // B + F/4: 10 + 7 = 17
	}
	for mode := 0; mode < 4; mode++ {
		g := New()
		fullDraw(g)
		gp0(g, 0xE1000000|uint32(mode)<<5) // E1 semi mode
		g.SetVRAM(2, 2, backdrop)
		g.SetVRAM(3, 2, backdrop)
		g.SetVRAM(2, 3, backdrop)
		// Semi-transparent flat triangle (cmd 0x22)
		// over (2,2),(3,2),(2,3).
		gp0(g, 0x22000000|front, 0x00020002, 0x00020003, 0x00030002)
		if got := g.VRAMAt(2, 2); got != want[mode] {
			t.Errorf("mode %d: pixel = 0x%04X, want 0x%04X", mode, got, want[mode])
		}
	}
}

func TestFillMaskingRules(t *testing.T) {
	g := New()
	// Height parameter 512 masks to 0: nothing fills.
	gp0(g, 0x02FFFFFF, 0x00000000, 0x02000010)
	if n := countColor(g, 0x7FFF); n != 0 {
		t.Fatalf("fill with height 512 drew %d pixels, want 0", n)
	}
	// X position 0x123 masks to 0x120; width 17 rounds
	// up to 32; color low bits are dropped (0xFFFFFF
	// becomes channels of 31 = 0x7FFF, mask bit 0).
	gp0(g, 0x02FFFFFF, 0x00000123, 0x00010011)
	if g.VRAMAt(0x120, 0) != 0x7FFF || g.VRAMAt(0x120+31, 0) != 0x7FFF {
		t.Errorf("fill span wrong: [0x120]=0x%04X [0x13F]=0x%04X",
			g.VRAMAt(0x120, 0), g.VRAMAt(0x13F, 0))
	}
	if g.VRAMAt(0x120+32, 0) != 0 || g.VRAMAt(0x11F, 0) != 0 {
		t.Errorf("fill leaked outside the masked span")
	}
	if g.VRAMAt(0x120, 0)&0x8000 != 0 {
		t.Errorf("fill set the mask bit; it is forced to 0")
	}
}

func TestTexturedRect4BitCLUTRaw(t *testing.T) {
	g := New()
	fullDraw(g)
	// CLUT at (0,256): entries 1,2,3 colored, 0
	// transparent.
	g.SetVRAM(1, 256, 0x001F)
	g.SetVRAM(2, 256, 0x03E0)
	g.SetVRAM(3, 256, 0x7C00)
	// Texture row: nibbles for u=0..3 are 1,2,3,0.
	g.SetVRAM(0, 0, 0x0321)
	// Raw textured variable rect (0x65) at (10,10),
	// 4x1; the UV word's high halfword is the CLUT
	// coordinate (0,256): 256<<6 in the high half.
	gp0(g, 0x65000000, 0x000A000A, 0x40000000, 0x00010004)
	want := []uint16{0x001F, 0x03E0, 0x7C00, 0x0000}
	for i, wv := range want {
		if got := g.VRAMAt(10+i, 10); got != wv {
			t.Errorf("pixel (%d,10) = 0x%04X, want 0x%04X", 10+i, got, wv)
		}
	}
}

func TestTexturedRectBlendedModulation(t *testing.T) {
	g := New()
	fullDraw(g)
	g.SetVRAM(1, 256, 0x001F) // CLUT[1] = red 31
	g.SetVRAM(0, 0, 0x0111)   // nibbles: u=0,1,2 all index CLUT 1
	// Blended textured variable-size rect (0x64)
	// with a 1x1 size word. Neutral color 0x80
	// leaves the texel unchanged.
	gp0(g, 0x64008080, 0x00140014, 0x40000000, 0x00010001)
	if got := g.VRAMAt(20, 20); got != 0x001F {
		t.Errorf("neutral modulation = 0x%04X, want 0x001F", got)
	}
	// Color 0x40 halves it: 255*64>>7 = 127, >>3 = 15.
	gp0(g, 0x64004040, 0x00140015, 0x40000000, 0x00010001)
	if got := g.VRAMAt(21, 20); got != 0x000F {
		t.Errorf("half modulation = 0x%04X, want 0x000F", got)
	}
}

func TestTexturedTriangle8BitCLUT(t *testing.T) {
	g := New()
	fullDraw(g)
	// CLUT (0,300) entry 5.
	g.SetVRAM(5, 300, 0x03FF)
	// Texture bytes: u=0 and u=1 both index 5.
	g.SetVRAM(0, 0, 0x0505)
	// Raw textured triangle (0x25) (0,0),(2,0),(0,2),
	// UVs (0,0),(1,0),(0,1); texpage fmt = 8-bit.
	gp0(g,
		0x25000000,
		0x00000000, 0x4B000000, // v0, uv0 (clut (0,300) in the high halfword)
		0x00000002, 0x00800001, // v1, uv1 (u=1, fmt 8-bit)
		0x00020000, 0x00000100, // v2, uv2 (v=1)
	)
	if got := g.VRAMAt(0, 0); got != 0x03FF {
		t.Errorf("pixel (0,0) = 0x%04X, want 0x03FF", got)
	}
}

func TestTexturedTriangle15BitDirect(t *testing.T) {
	g := New()
	fullDraw(g)
	g.SetVRAM(0, 0, 0x4210)
	g.SetVRAM(1, 0, 0x4210)
	// Raw 15-bit triangle at (20,20), all UVs (0,0).
	gp0(g,
		0x25000000,
		0x00140014, 0x00000000,
		0x00140016, 0x01000000, // texpage fmt 15-bit
		0x00160014, 0x00000000,
	)
	for _, pt := range [][2]int{{20, 20}, {21, 20}, {20, 21}} {
		if got := g.VRAMAt(pt[0], pt[1]); got != 0x4210 {
			t.Errorf("pixel %v = 0x%04X, want 0x4210", pt, got)
		}
	}
}

func TestSemiTexturedBlendsOnlySTP(t *testing.T) {
	g := New()
	fullDraw(g)
	gp0(g, 0xE1000000)      // semi mode 0
	g.SetVRAM(0, 0, 0x001F) // texel A: red, STP clear
	g.SetVRAM(1, 0, 0x801F) // texel B: red, STP set
	g.SetVRAM(40, 40, 0x7FFF)
	g.SetVRAM(41, 40, 0x7FFF)
	// Semi blended triangle (0x26) (40,40),(42,40),
	// (40,42), UVs (0,0),(2,0),(0,2), 15-bit page.
	gp0(g,
		0x26808080,
		0x00280028, 0x00000000,
		0x0028002A, 0x01000002,
		0x002A0028, 0x00000200,
	)
	// Neutral modulation (0x80) reproduces the texel
	// exactly; STP clear means opaque write.
	if got := g.VRAMAt(40, 40); got != 0x001F {
		t.Errorf("STP-clear pixel = 0x%04X, want opaque 0x001F", got)
	}
	// STP set blends B/2+F/2 over white: (15+15,
	// 15+0, 15+0) = 0x3DFE, and the texel STP bit is
	// written into the pixel mask bit, giving 0xBDFE.
	if got := g.VRAMAt(41, 40); got != 0xBDFE {
		t.Errorf("STP-set pixel = 0x%04X, want 0xBDFE", got)
	}
}

func TestTextureWindow(t *testing.T) {
	g := New()
	fullDraw(g)
	gp0(g, 0xE1000100) // E1: 15-bit page at (0,0)
	g.SetVRAM(0, 0, 0x2222)
	g.SetVRAM(8, 0, 0x1111)
	// Window: maskX=1, offsetX=1 maps u=0 to u=8.
	gp0(g, 0xE2000401)
	// Raw textured 1x1 rect at (0,20), UV (0,0).
	gp0(g, 0x65000000, 0x00140000, 0x00000000, 0x00010001)
	if got := g.VRAMAt(0, 20); got != 0x1111 {
		t.Errorf("windowed sample = 0x%04X, want 0x1111 (u=8)", got)
	}
}

func TestMaskSetAndCheck(t *testing.T) {
	g := New()
	fullDraw(g)
	gp0(g, 0xE6000001) // set mask bit while drawing
	// White 1x1 rect at (5,5).
	gp0(g, 0x68FFFFFF, 0x00050005)
	if got := g.VRAMAt(5, 5); got != 0xFFFF {
		t.Fatalf("masked draw = 0x%04X, want 0xFFFF", got)
	}
	gp0(g, 0xE6000002)             // check mask before drawing
	gp0(g, 0x680000FF, 0x00050005) // red over masked pixel: blocked
	if got := g.VRAMAt(5, 5); got != 0xFFFF {
		t.Errorf("mask check failed: pixel = 0x%04X", got)
	}
	gp0(g, 0x680000FF, 0x00050006) // red at (6,5): drawn
	if got := g.VRAMAt(6, 5); got != 0x001F {
		t.Errorf("unmasked pixel = 0x%04X, want 0x001F", got)
	}
}

func TestDitherRules(t *testing.T) {
	// Gouraud triangle with all vertex colors 0x80:
	// dither on produces the matrix pattern.
	g := New()
	fullDraw(g)
	gp0(g, 0xE1000200) // E1 dither on
	gp0(g,
		0x30808080, 0x00000000,
		0x00808080, 0x00000007,
		0x00808080, 0x00070000,
	)
	want := []uint16{0x3DEF, 0x4210, 0x3DEF, 0x4210} // (128-4)>>3=15, 128>>3=16, (128-3)>>3=15, (128+1)>>3=16
	for x, wv := range want {
		if got := g.VRAMAt(x, 0); got != wv {
			t.Errorf("dithered pixel (%d,0) = 0x%04X, want 0x%04X", x, got, wv)
		}
	}
	// Monochrome triangles never dither.
	g2 := New()
	fullDraw(g2)
	gp0(g2, 0xE1000200)
	gp0(g2, 0x20808080, 0x00000000, 0x00000007, 0x00070000)
	for x := 0; x < 4; x++ {
		if got := g2.VRAMAt(x, 0); got != 0x4210 {
			t.Errorf("mono pixel (%d,0) = 0x%04X, want undithered 0x4210", x, got)
		}
	}
}

func TestLinesAndPolyline(t *testing.T) {
	g := New()
	fullDraw(g)
	// Horizontal mono line (0,30)-(4,30), white.
	gp0(g, 0x40FFFFFF, 0x001E0000, 0x001E0004)
	for x := 0; x <= 4; x++ {
		if g.VRAMAt(x, 30) != 0x7FFF {
			t.Errorf("line pixel (%d,30) = 0x%04X", x, g.VRAMAt(x, 30))
		}
	}
	if g.VRAMAt(5, 30) != 0 {
		t.Errorf("line overran its endpoint")
	}
	// Diagonal (0,32)-(4,36).
	gp0(g, 0x40FFFFFF, 0x00200000, 0x00240004)
	for i := 0; i <= 4; i++ {
		if g.VRAMAt(i, 32+i) != 0x7FFF {
			t.Errorf("diagonal pixel (%d,%d) missing", i, 32+i)
		}
	}
	// Polyline: (0,40) to (4,40) to (4,44), then the
	// terminator word.
	gp0(g, 0x48FFFFFF, 0x00280000, 0x00280004, 0x002C0004, 0x50005000)
	if g.VRAMAt(2, 40) != 0x7FFF || g.VRAMAt(4, 42) != 0x7FFF {
		t.Errorf("polyline segments missing: (2,40)=0x%04X (4,42)=0x%04X",
			g.VRAMAt(2, 40), g.VRAMAt(4, 42))
	}
	// The command stream still works after the
	// terminator: a fill must land.
	gp0(g, 0x0200FF00, 0x00320032, 0x00020002)
	if g.VRAMAt(50, 50) != 0x03E0 {
		t.Errorf("post-terminator fill failed: 0x%04X", g.VRAMAt(50, 50))
	}
}

func TestRectFixedSizesAndOffset(t *testing.T) {
	g := New()
	fullDraw(g)
	// Untextured 8x8 rect (0x70) blue at (0,50).
	gp0(g, 0x70FF0000, 0x00320000)
	if g.VRAMAt(7, 57) != 0x7C00 {
		t.Errorf("8x8 corner = 0x%04X, want 0x7C00", g.VRAMAt(7, 57))
	}
	if g.VRAMAt(8, 57) != 0 {
		t.Errorf("8x8 rect overran in x")
	}
	// Drawing offset (3,2): a 1x1 rect at vertex
	// (0,0) lands at (3,2).
	gp0(g, 0xE5001003) // offset X=3, Y=2
	gp0(g, 0x680000FF, 0x00000000)
	if g.VRAMAt(3, 2) != 0x001F {
		t.Errorf("offset pixel = 0x%04X, want 0x001F at (3,2)", g.VRAMAt(3, 2))
	}
}

func TestDrawingAreaClip(t *testing.T) {
	g := New()
	gp0(g, 0xE300280A, 0xE4005014, 0xE5000000) // area (10,10)-(20,20)
	// Big red triangle over everything nearby.
	gp0(g, 0x200000FF, 0x00000000, 0x0000001E, 0x001E0000)
	if g.VRAMAt(14, 15) != 0x001F {
		t.Errorf("pixel inside drawing area missing")
	}
	if g.VRAMAt(5, 15) != 0 || g.VRAMAt(15, 5) != 0 {
		t.Errorf("pixels outside drawing area drawn")
	}
}

func TestOverspanPrimitiveSkipped(t *testing.T) {
	g := New()
	fullDraw(g)
	// X span of 1123 exceeds 1023: not drawn.
	// Vertices: (-100,0), (1023,0), (0,10); -100 in
	// 11-bit two's complement is 0x79C.
	gp0(g, 0x200000FF, 0x0000079C, 0x000003FF, 0x000A0000)
	if n := countColor(g, 0x001F); n != 0 {
		t.Errorf("overspan triangle drew %d pixels, want 0", n)
	}
}

func TestVRAMTransfers(t *testing.T) {
	g := New()
	fullDraw(g)
	// CPU to VRAM: 2 halfwords at (0,0).
	gp0(g, 0xA0000000, 0x00000000, 0x00010002, 0xBBBBAAAA)
	if g.VRAMAt(0, 0) != 0xAAAA || g.VRAMAt(1, 0) != 0xBBBB {
		t.Fatalf("CPU to VRAM landed wrong: 0x%04X 0x%04X", g.VRAMAt(0, 0), g.VRAMAt(1, 0))
	}
	// VRAM to VRAM: copy (0,0)-(1,0) to (10,5).
	gp0(g, 0x80000000, 0x00000000, 0x0005000A, 0x00010002)
	if g.VRAMAt(10, 5) != 0xAAAA || g.VRAMAt(11, 5) != 0xBBBB {
		t.Errorf("VRAM to VRAM copy wrong: 0x%04X 0x%04X", g.VRAMAt(10, 5), g.VRAMAt(11, 5))
	}
	// VRAM to CPU: arm and read back.
	gp0(g, 0xC0000000, 0x00000000, 0x00010002)
	if g.Status()&(1<<27) == 0 {
		t.Errorf("GPUSTAT bit 27 (VRAM read ready) clear during readback")
	}
	if got := g.ReadData(); got != 0xBBBBAAAA {
		t.Errorf("GPUREAD = 0x%08X, want 0xBBBBAAAA", got)
	}
	if g.Status()&(1<<27) != 0 {
		t.Errorf("GPUSTAT bit 27 still set after readback drained")
	}
}

func TestGP0IRQCommand(t *testing.T) {
	g := New()
	gp0(g, 0x1F000000)
	if g.Status()&(1<<24) == 0 || !g.IRQRequested() {
		t.Errorf("GP0(1F) did not set the GPU interrupt bit")
	}
	gp1(g, 0x02000000) // GP1(02) acknowledge
	if g.Status()&(1<<24) != 0 {
		t.Errorf("GP1(02) did not clear the GPU interrupt bit")
	}
}

func TestGP1ReadRegister(t *testing.T) {
	g := New()
	gp0(g, 0xE3001805) // draw area TL (5,6)
	gp1(g, 0x10000003)
	if got := g.ReadData(); got != 0x1805 {
		t.Errorf("GP1(10) draw area = 0x%08X, want 0x1805", got)
	}
	gp1(g, 0x10000007)
	if got := g.ReadData(); got != 2 {
		t.Errorf("GP1(10) GPU version = %d, want 2", got)
	}
}

func TestDisplayOutput(t *testing.T) {
	g := New()
	g.SetVRAM(0, 0, 0x001F) // red
	g.SetVRAM(1, 0, 0x8000) // non-transparent black
	gp1(g, 0x05000000)      // display area (0,0)
	gp1(g, 0x08000000)      // mode 0: 256x240 15-bit
	gp1(g, 0x03000000)      // display on
	fb, w, h := g.Framebuffer()
	if w != 256 || h != 240 || len(fb) != 256*240 {
		t.Fatalf("framebuffer = %dx%d len %d", w, h, len(fb))
	}
	if fb[0] != 0xFFFF0000 {
		t.Errorf("fb[0] = 0x%08X, want 0xFFFF0000", fb[0])
	}
	if fb[1] != 0xFF000000 {
		t.Errorf("fb[1] = 0x%08X, want 0xFF000000", fb[1])
	}
	// Display off: black.
	gp1(g, 0x03000001)
	fb, _, _ = g.Framebuffer()
	if fb[0] != 0xFF000000 {
		t.Errorf("display-off framebuffer not black: 0x%08X", fb[0])
	}
}

func TestDisplay24Bit(t *testing.T) {
	g := New()
	g.SetVRAM(0, 0, 0xBBAA) // bytes AA BB
	g.SetVRAM(1, 0, 0x00CC) // byte CC
	gp1(g, 0x05000000)
	gp1(g, 0x08000010) // 24-bit depth
	gp1(g, 0x03000000)
	fb, _, _ := g.Framebuffer()
	if fb[0] != 0xFFAABBCC {
		t.Errorf("24-bit pixel = 0x%08X, want 0xFFAABBCC", fb[0])
	}
}

func TestDisplayInterlace480(t *testing.T) {
	g := New()
	g.SetVRAM(0, 1, 0x03E0) // green on VRAM line 1
	gp1(g, 0x05000000)
	gp1(g, 0x08000024) // vres 480 + interlace
	gp1(g, 0x03000000)
	if g.DisplayHeight() != 480 {
		t.Fatalf("DisplayHeight = %d, want 480", g.DisplayHeight())
	}
	g.OddFrame = false
	fb, w, _ := g.Framebuffer()
	if fb[w] != 0xFF00FF00 {
		t.Errorf("even frame line 1 = 0x%08X, want green", fb[w])
	}
	g.OddFrame = true
	fb, _, _ = g.Framebuffer()
	if fb[0] != 0xFF00FF00 {
		t.Errorf("odd frame line 0 = 0x%08X, want green (field offset applied)", fb[0])
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	g := New()
	fullDraw(g)
	gp0(g, 0xE1000100)
	gp0(g, 0x200000FF, 0x00000000, 0x00000008, 0x00080000)
	gp1(g, 0x08000001) // 320 wide
	gp1(g, 0x03000000)
	s := g.Serialize()
	if len(s) != StateSize {
		t.Fatalf("state = %d, want %d", len(s), StateSize)
	}
	g2 := New()
	g2.Deserialize(s)
	if g2.Status() != g.Status() {
		t.Errorf("status diverged: 0x%08X vs 0x%08X", g2.Status(), g.Status())
	}
	fb1, w1, h1 := g.Framebuffer()
	fb2, w2, h2 := g2.Framebuffer()
	if w1 != w2 || h1 != h2 {
		t.Fatalf("display size diverged")
	}
	for i := range fb1 {
		if fb1[i] != fb2[i] {
			t.Fatalf("framebuffer diverged at pixel %d", i)
		}
	}
}
