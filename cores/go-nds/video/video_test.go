package video_test

import (
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-nds/bus"
	"github.com/davidpatton/itak-gaming/cores/go-nds/video"
)

// rig: a bus plus video unit with both engines powered
// (POWCNT1 = 0x020F) and bank A assigned to BG A.
type rig struct {
	b *bus.Bus
	v *video.Video
}

func newRig() *rig {
	b := bus.New()
	r := &rig{b: b, v: video.New(b)}
	r.w16(0x304, 0x020F)
	return r
}

func (r *rig) w16(off int, v uint16) {
	r.b.IO9[off] = byte(v)
	r.b.IO9[off+1] = byte(v >> 8)
}
func (r *rig) w32(off int, v uint32) {
	r.w16(off, uint16(v))
	r.w16(off+2, uint16(v>>16))
}

// pal sets one standard palette entry (15bpp) for an engine
// palette page offset.
func (r *rig) pal(off int, c uint16) {
	r.b.Palette[off] = byte(c)
	r.b.Palette[off+1] = byte(c >> 8)
}

// solidTile4 fills a 4bpp tile (32 bytes) with one nibble.
func solidTile4(r *rig, bankOff int, nib byte) {
	for i := 0; i < 32; i++ {
		r.b.VRAM[bankOff+i] = nib | nib<<4
	}
}

// textSceneA builds the reusable engine A text scene: bank A
// to BG A, BG0 4bpp, tile 1 solid palette-1 at screen (0,0),
// tile 2 solid palette-2 at (1,0).
func textSceneA(r *rig) {
	r.b.IO9[0x240] = 0x81 // VRAMCNT A: BG A at 0
	r.w32(0x000, 0x00010100)
	r.w16(0x008, 0x0204) // BG0CNT: char base 0x4000, screen base 0x1000
	r.pal(0, 0x0000)
	r.pal(2, 0x001F) // palette 1: red
	r.pal(4, 0x03E0) // palette 2: green
	solidTile4(r, 0x4000+32, 1)
	solidTile4(r, 0x4000+64, 2)
	r.b.VRAM[0x1000] = 1 // screen entry (0,0) = tile 1
	r.b.VRAM[0x1002] = 2 // screen entry (1,0) = tile 2
}

func TestTextBGEngineA(t *testing.T) {
	r := newRig()
	textSceneA(r)
	r.v.RenderLine(0)
	if got := r.v.A[0]; got != 0xFFFF0000 {
		t.Fatalf("A pixel (0,0): %08x want FFFF0000 (red)", got)
	}
	if got := r.v.A[8]; got != 0xFF00FF00 {
		t.Fatalf("A pixel (8,0): %08x want FF00FF00 (green)", got)
	}
	if got := r.v.A[16]; got != 0xFF000000 {
		t.Fatalf("A pixel (16,0): %08x want backdrop black", got)
	}
}

func TestTextBGEngineBIndependent(t *testing.T) {
	r := newRig()
	textSceneA(r)
	// Engine B: bank H to BG B, its own palette and tiles.
	r.b.IO9[0x248] = 0x81 // VRAMCNT H: BG B at 0
	r.w32(0x1000, 0x00010100)
	r.w16(0x1008, 0x0204)
	r.pal(0x400, 0x7C00) // B backdrop: blue
	r.pal(0x402, 0x7FFF) // B palette 1: white
	for i := 0; i < 32; i++ {
		r.b.VRAM[0x98000+0x4000+32+i] = 0x11
	}
	r.b.VRAM[0x98000+0x1000] = 1
	r.v.RenderLine(0)
	if got := r.v.B[0]; got != 0xFFFFFFFF {
		t.Fatalf("B pixel (0,0): %08x want FFFFFFFF (white)", got)
	}
	if got := r.v.B[8]; got != 0xFF0000FF {
		t.Fatalf("B pixel (8,0): %08x want FF0000FF (blue backdrop)", got)
	}
	if got := r.v.A[0]; got != 0xFFFF0000 {
		t.Fatalf("A pixel changed by B scene: %08x", got)
	}
}

func TestAffineBGProbe(t *testing.T) {
	r := newRig()
	r.b.IO9[0x240] = 0x81
	r.w32(0x000, 0x00010402) // mode 2, BG2 on, graphics
	r.w16(0x00C, 0x0104)     // BG2CNT: char base 0x4000, screen base 0x800, 16x16 map
	// 8bpp tile 1: all index 1; map entry (0,0) = 1.
	for i := 0; i < 64; i++ {
		r.b.VRAM[0x4000+64+i] = 1
	}
	r.b.VRAM[0x800] = 1
	r.pal(2, 0x03E0) // palette 1: green (8bpp uses the 256 palette)
	r.w16(0x020, 0x0100)
	r.w16(0x026, 0x0100) // PA = PD = 1.0
	r.v.LatchAffine()
	r.v.RenderLine(0)
	if got := r.v.A[0]; got != 0xFF00FF00 {
		t.Fatalf("affine pixel (0,0): %08x want FF00FF00", got)
	}
	if got := r.v.A[8]; got != 0xFF000000 {
		t.Fatalf("affine pixel (8,0): %08x want black (map entry 0)", got)
	}
}

func TestExtendedBitmapBG(t *testing.T) {
	r := newRig()
	r.b.IO9[0x240] = 0x81
	r.w32(0x000, 0x00010805) // mode 5, BG3 on, graphics
	r.w16(0x00E, 0x0184)     // BG3CNT: bitmap, direct color, data base 0x4000, 128x128
	// BG3 affine params at 0x30.
	r.w16(0x030, 0x0100)
	r.w16(0x036, 0x0100)
	put := func(off int, c uint16) {
		r.b.VRAM[0x4000+off] = byte(c)
		r.b.VRAM[0x4000+off+1] = byte(c >> 8)
	}
	put(0, 0xFFFF) // (0,0) white
	put(2, 0x801F) // (1,0) red
	put(4, 0x001F) // (2,0) alpha bit clear: transparent
	r.v.LatchAffine()
	r.v.RenderLine(0)
	if got := r.v.A[0]; got != 0xFFFFFFFF {
		t.Fatalf("bitmap pixel (0,0): %08x want FFFFFFFF", got)
	}
	if got := r.v.A[1]; got != 0xFFFF0000 {
		t.Fatalf("bitmap pixel (1,0): %08x want FFFF0000", got)
	}
	if got := r.v.A[2]; got != 0xFF000000 {
		t.Fatalf("bitmap pixel (2,0): %08x want backdrop (alpha clear)", got)
	}
}

func TestBitmapOBJ(t *testing.T) {
	r := newRig()
	r.b.IO9[0x241] = 0x82 // VRAMCNT B: OBJ A at 0
	r.w32(0x000, 0x00011000)
	r.pal(0x200, 0x0000)
	// OAM entry 0: 8x8 bitmap OBJ at (0,0), tile 0.
	r.b.OAM[0] = 0x00
	r.b.OAM[1] = 0x0C // attr0 high byte: mode 3 (bitmap)
	put := func(off int, c uint16) {
		r.b.VRAM[0x20000+off] = byte(c)
		r.b.VRAM[0x20000+off+1] = byte(c >> 8)
	}
	put(0, 0xFFFF)
	put(2, 0x83E0)
	r.v.RenderLine(0)
	if got := r.v.A[0]; got != 0xFFFFFFFF {
		t.Fatalf("OBJ pixel (0,0): %08x want FFFFFFFF", got)
	}
	if got := r.v.A[1]; got != 0xFF00FF00 {
		t.Fatalf("OBJ pixel (1,0): %08x want FF00FF00", got)
	}
	if got := r.v.A[2]; got != 0xFF000000 {
		t.Fatalf("OBJ pixel (2,0): %08x want backdrop (alpha clear)", got)
	}
}

func TestMasterBrightness(t *testing.T) {
	r := newRig()
	textSceneA(r)
	r.pal(2, 0x0010)     // r5 = 16, rgb6 r = 33
	r.w16(0x06C, 0x4008) // up, factor 8
	r.v.RenderLine(0)
	if got := r.v.A[0]; got != 0xFFC37D7D {
		t.Fatalf("brightness up: %08x want FFC37D7D (r6 48, zero channels lift to 31)", got)
	}
	r.w16(0x06C, 0x8008) // down, factor 8
	r.v.RenderLine(0)
	if got := r.v.A[0]; got != 0xFF450000 {
		t.Fatalf("brightness down: %08x want FF450000 (r6 17)", got)
	}
}

func TestAlphaBlend(t *testing.T) {
	r := newRig()
	r.b.IO9[0x240] = 0x81
	r.w32(0x000, 0x00010300) // mode 0, BG0+BG1, graphics
	// BG0: tile of palette 1 (red), screen base 0x1000.
	r.w16(0x008, 0x0204)
	// BG1: tile of palette 2 (blue), screen base 0x1800, prio 1.
	r.w16(0x00A, 0x0305)
	r.pal(2, 0x001F)
	r.pal(4, 0x7C00)
	solidTile4(r, 0x4000+32, 1)
	solidTile4(r, 0x4000+64, 2)
	r.b.VRAM[0x1000] = 1
	r.b.VRAM[0x1800] = 2
	r.w16(0x050, 0x0241) // BLDCNT: alpha, BG0 target1, BG1 target2
	r.w16(0x052, 0x0808) // EVA 8, EVB 8
	r.v.RenderLine(0)
	if got := r.v.A[0]; got != 0xFF7D007D {
		t.Fatalf("alpha blend: %08x want FF7D007D (r6/b6 31)", got)
	}
}

func TestVRAMDisplayMode(t *testing.T) {
	r := newRig()
	for i := 0; i < 0x20000; i += 2 {
		off := 0x40000 + i // bank C
		r.b.VRAM[off] = 0xE0
		r.b.VRAM[off+1] = 0x03 // 0x03E0 green
	}
	r.w32(0x000, 0x000A0000) // display mode 2, VRAM block C
	r.v.RenderLine(3)
	if got := r.v.A[3*256+5]; got != 0xFF00FF00 {
		t.Fatalf("VRAM display pixel: %08x want FF00FF00", got)
	}
}

func TestCaptureBlendIntoBank(t *testing.T) {
	r := newRig()
	textSceneA(r) // engine A shows red at (0,0)
	// Source B bank C: solid blue.
	for i := 0; i < 0x20000; i += 2 {
		r.b.VRAM[0x40000+i] = 0x00
		r.b.VRAM[0x40000+i+1] = 0x7C
	}
	cap := uint32(1)<<31 | uint32(3)<<20 | uint32(3)<<16 | uint32(2)<<26 | uint32(1)<<24 | 8 | 8<<8
	r.w32(0x064, cap) // enable, 256x192, write bank D, srcB bank C, blend, EVA=EVB=8
	r.v.BeginFrame()
	for y := 0; y < 192; y++ {
		r.v.RenderLine(y)
		r.v.CaptureLine(y)
	}
	dst := r.b.VRAMBank(3)
	got := uint16(dst[0]) | uint16(dst[1])<<8
	if got != 0xBC0F {
		t.Fatalf("captured pixel: %04x want BC0F (blend of red and blue at 8/8)", got)
	}
	if c := r.b.IO9[0x67]; c&0x80 != 0 {
		t.Fatal("DISPCAPCNT enable bit not cleared")
	}
}

func TestExtPaletteAssignment(t *testing.T) {
	r := newRig()
	r.b.IO9[0x240] = 0x81
	r.w32(0x000, 0x40010100) // BG0, graphics, BG ext palettes on
	r.w16(0x008, 0x0284)     // BG0CNT: 8bpp, char base 0x4000, screen base 0x1000
	r.pal(0, 0x7FFF)         // backdrop white
	for i := 0; i < 64; i++ {
		r.b.VRAM[0x4000+64+i] = 1 // 8bpp tile 1: index 1
	}
	r.b.VRAM[0x1000] = 1
	// No extended palette bank assigned: tiles read the empty
	// extended palette and come out black on white.
	r.v.RenderLine(0)
	if got := r.v.A[0]; got != 0xFF000000 {
		t.Fatalf("no ext pal: %08x want black", got)
	}
	if got := r.v.A[8]; got != 0xFFFFFFFF {
		t.Fatalf("backdrop: %08x want white", got)
	}
	// Assign bank E as BG extended palette A with entry 1 red.
	r.b.VRAM[0x80000+2] = 0x1F
	r.b.VRAM[0x80000+3] = 0x00
	r.b.IO9[0x244] = 0x84 // VRAMCNT E: MST 4 (BG ext palette A)
	r.v.RenderLine(0)
	if got := r.v.A[0]; got != 0xFFFF0000 {
		t.Fatalf("ext pal assigned: %08x want red", got)
	}
	// Move bank E to OBJ A: the extended palette goes away.
	r.b.IO9[0x244] = 0x82
	r.v.RenderLine(0)
	if got := r.v.A[0]; got != 0xFF000000 {
		t.Fatalf("ext pal removed: %08x want black", got)
	}
}

func TestVideoStateRoundTrip(t *testing.T) {
	r := newRig()
	r.v.SetDISPSTAT(0xFF08) // LYC 0xFF... plus enables
	r.v.SetLine(100)
	r.v.LatchAffine()
	blob := make([]byte, video.StateSize)
	if n := r.v.Serialize(blob); n != video.StateSize {
		t.Fatalf("serialize n = %d want %d", n, video.StateSize)
	}
	r2 := newRig()
	if n := r2.v.Deserialize(blob); n != video.StateSize {
		t.Fatalf("deserialize n = %d want %d", n, video.StateSize)
	}
	if r2.v.LYC() != r.v.LYC() || r2.v.VCount() != 100 {
		t.Fatal("video scalars not restored")
	}
	blob2 := make([]byte, video.StateSize)
	r2.v.Serialize(blob2)
	for i := range blob {
		if blob[i] != blob2[i] {
			t.Fatalf("video state bytes differ at %d", i)
		}
	}
}
