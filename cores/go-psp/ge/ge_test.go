package ge

import (
	"bytes"
	"math"
	"testing"
)

// -----------------------------------------------------------------------------
// Fake memory: 16 MB of RAM at 0x08000000 and 2 MB of VRAM mirrored
// across 0x04000000..0x04800000, matching the bus map for the ranges the
// GE touches in these tests. Everything else reads 0 and drops writes.
// -----------------------------------------------------------------------------

type fakeMem struct {
	ram  []byte
	vram []byte
}

func newFakeMem() *fakeMem {
	return &fakeMem{
		ram:  make([]byte, 16<<20),
		vram: make([]byte, 2<<20),
	}
}

func (m *fakeMem) Read8(addr uint32) uint8 {
	switch {
	case addr >= 0x04000000 && addr < 0x04800000:
		return m.vram[(addr-0x04000000)&(2<<20-1)]
	case addr >= 0x08000000 && addr < 0x08000000+uint32(len(m.ram)):
		return m.ram[addr-0x08000000]
	}
	return 0
}

func (m *fakeMem) Write8(addr uint32, v uint8) {
	switch {
	case addr >= 0x04000000 && addr < 0x04800000:
		m.vram[(addr-0x04000000)&(2<<20-1)] = v
	case addr >= 0x08000000 && addr < 0x08000000+uint32(len(m.ram)):
		m.ram[addr-0x08000000] = v
	}
}

func (m *fakeMem) Read16(addr uint32) uint16 {
	return uint16(m.Read8(addr)) | uint16(m.Read8(addr+1))<<8
}

func (m *fakeMem) Write16(addr uint32, v uint16) {
	m.Write8(addr, uint8(v))
	m.Write8(addr+1, uint8(v>>8))
}

func (m *fakeMem) Read32(addr uint32) uint32 {
	return uint32(m.Read16(addr)) | uint32(m.Read16(addr+2))<<16
}

func (m *fakeMem) Write32(addr uint32, v uint32) {
	m.Write16(addr, uint16(v))
	m.Write16(addr+2, uint16(v>>16))
}

// -----------------------------------------------------------------------------
// List and vertex helpers
// -----------------------------------------------------------------------------

const listAddr = 0x08010000

func cmd(op int, arg uint32) uint32 {
	return uint32(op)<<24 | (arg & 0xFFFFFF)
}

func putList(m *fakeMem, addr uint32, words ...uint32) {
	for i, w := range words {
		m.Write32(addr+uint32(i*4), w)
	}
}

// float24 encodes a float32 the way float command arguments carry it:
// the top 24 bits of the float32 bit pattern.
func float24(f float32) uint32 {
	return math.Float32bits(f) >> 8
}

// VTYPE encodings used by the tests.
const (
	vtypeThroughColorPos = 0x800000 | (4 << 2) | (2 << 7)     // through, color 8888, pos s16; stride 10
	vtypeThroughTexPos   = 0x800000 | 3 | (4 << 2) | (2 << 7) // through, tc float, color 8888, pos s16; stride 18
	vtypeFloatPos        = (4 << 2) | (3 << 7)                // color 8888, pos float; stride 16
)

func rgbaWord(r, g, b, a uint8) uint32 {
	return uint32(r) | uint32(g)<<8 | uint32(b)<<16 | uint32(a)<<24
}

// writeVertColorPos writes one through-mode vertex for
// vtypeThroughColorPos at addr: color word, then x, y as 12.4 fixed
// point int16, then z as raw uint16.
func writeVertColorPos(m *fakeMem, addr uint32, x, y int, z uint16, color uint32) {
	m.Write32(addr, color)
	m.Write16(addr+4, uint16(int16(x<<4)))
	m.Write16(addr+6, uint16(int16(y<<4)))
	m.Write16(addr+8, z)
}

// writeVertTex writes one textured through-mode vertex for
// vtypeThroughTexPos at addr: uv float32 pair, color word, position.
func writeVertTex(m *fakeMem, addr uint32, x, y int, z uint16, u, v float32, color uint32) {
	m.Write32(addr, math.Float32bits(u))
	m.Write32(addr+4, math.Float32bits(v))
	m.Write32(addr+8, color)
	m.Write16(addr+12, uint16(int16(x<<4)))
	m.Write16(addr+14, uint16(int16(y<<4)))
	m.Write16(addr+16, z)
}

// writeVertFloat writes one non-through float position vertex for
// vtypeFloatPos at addr: color word, then x, y, z float32.
func writeVertFloat(m *fakeMem, addr uint32, x, y, z float32, color uint32) {
	m.Write32(addr, color)
	m.Write32(addr+4, math.Float32bits(x))
	m.Write32(addr+8, math.Float32bits(y))
	m.Write32(addr+12, math.Float32bits(z))
}

func fbSetup() []uint32 {
	return []uint32{
		cmd(0x9C, 0),   // FRAMEBUFPTR = VRAM base
		cmd(0x9D, 512), // FRAMEBUFWIDTH
		cmd(0xE8, 3),   // PIXELFORMAT 8888
	}
}

func withFB(extra ...uint32) []uint32 {
	return append(fbSetup(), extra...)
}

// clearList returns a list that clears the full 480x272 region to the
// given CLEARCOLOR argument with alpha 0xFF.
func clearList(clearColorArg uint32) []uint32 {
	return withFB(
		cmd(0x15, 0),             // REGION1 (0,0)
		cmd(0x16, 479|(271<<10)), // REGION2 (479,271)
		cmd(0xC9, clearColorArg), // CLEARCOLOR
		cmd(0xCA, 0xFF),          // CLEARALPHA
		cmd(0xCC, 0x1|0x2|0x4),   // CLEAR enable|color|alpha
		cmd(0x04, (3<<16)|3),     // PRIM triangles, 3 (runs the clear)
		cmd(0x0C, 0),             // END
	)
}

// triList returns a draw list for one through-mode triangle whose
// vertices the caller already wrote at RAM offset 0x2000.
func triList() []uint32 {
	return withFB(
		cmd(0xCC, 0),                    // CLEAR mode off
		cmd(0x14, 0x080000),             // ORIGIN: originPtr = 0x08000000
		cmd(0x12, vtypeThroughColorPos), // VTYPE
		cmd(0x01, 0x2000),               // VADDR
		cmd(0x04, (3<<16)|3),            // PRIM triangles, 3
		cmd(0x0C, 0),                    // END
	)
}

func writeTestTri(m *fakeMem, z uint16, color uint32) {
	base := uint32(0x08002000)
	writeVertColorPos(m, base, 10, 10, z, color)
	writeVertColorPos(m, base+10, 100, 10, z, color)
	writeVertColorPos(m, base+20, 10, 100, z, color)
}

func assertPixel(t *testing.T, g *GE, x, y int, r, gr, b, a uint8) {
	t.Helper()
	pr, pg, pb, pa := g.FramebufferPixel(x, y)
	if pr != r || pg != gr || pb != b || pa != a {
		t.Errorf("pixel (%d,%d) = (%d,%d,%d,%d), want (%d,%d,%d,%d)",
			x, y, pr, pg, pb, pa, r, gr, b, a)
	}
}

// -----------------------------------------------------------------------------
// Gate 1: clear
// -----------------------------------------------------------------------------

func TestClear(t *testing.T) {
	m := newFakeMem()
	g := New(m)
	putList(m, listAddr, clearList(0x0000FF)...) // CLEARCOLOR r=255
	if st := g.Submit(listAddr); st != StateCompleted {
		t.Fatalf("state = %v, want Completed", st)
	}
	// Pure red, opaque, across the region including its corners.
	assertPixel(t, g, 0, 0, 255, 0, 0, 255)
	assertPixel(t, g, 240, 136, 255, 0, 0, 255)
	assertPixel(t, g, 479, 271, 255, 0, 0, 255)
	// Just past the region the framebuffer is untouched.
	r, gr, b, a := g.FramebufferPixel(480, 0)
	if r != 0 || gr != 0 || b != 0 || a != 0 {
		t.Errorf("pixel (480,0) = (%d,%d,%d,%d), want untouched zero", r, gr, b, a)
	}
	// Raw VRAM word at the framebuffer base is packed 8888.
	if w := m.Read32(0x04000000); w != 0xFF0000FF {
		t.Errorf("VRAM word = %#08x, want 0xff0000ff", w)
	}
	// DisplayPixel falls back to the draw buffer while DispSet is false.
	dr, dg, db, da := g.DisplayPixel(240, 136)
	if dr != 255 || dg != 0 || db != 0 || da != 255 {
		t.Errorf("DisplayPixel = (%d,%d,%d,%d), want red opaque", dr, dg, db, da)
	}
}

// -----------------------------------------------------------------------------
// Gate 2: through-mode triangle
// -----------------------------------------------------------------------------

func TestTriangleThrough(t *testing.T) {
	m := newFakeMem()
	g := New(m)
	writeTestTri(m, 0, rgbaWord(0, 255, 0, 255)) // pure green
	putList(m, listAddr, triList()...)
	if st := g.Submit(listAddr); st != StateCompleted {
		t.Fatalf("state = %v, want Completed", st)
	}
	assertPixel(t, g, 20, 20, 0, 255, 0, 255)
	r, gr, b, _ := g.FramebufferPixel(90, 90)
	if r != 0 || gr != 0 || b != 0 {
		t.Errorf("pixel (90,90) = (%d,%d,%d), want black (outside triangle)", r, gr, b)
	}
}

// -----------------------------------------------------------------------------
// Gate 3: textured sprite, unswizzled and swizzled
// -----------------------------------------------------------------------------

func TestTexturedSprite(t *testing.T) {
	texel := func(x, y int) uint32 {
		switch {
		case x == 0 && y == 0:
			return rgbaWord(255, 0, 0, 255) // red
		case x == 1 && y == 0:
			return rgbaWord(0, 255, 0, 255) // green
		case x == 0 && y == 1:
			return rgbaWord(0, 0, 255, 255) // blue
		default:
			return rgbaWord(255, 255, 255, 255) // white
		}
	}
	for _, swizzled := range []bool{false, true} {
		m := newFakeMem()
		g := New(m)
		texBase := uint32(0x08004000)
		for y := 0; y < 2; y++ {
			for x := 0; x < 2; x++ {
				var off int
				if swizzled {
					// Independent computation of the documented swizzle
					// formula for a 2 texel wide 8888 texture:
					// rowBytes = 8, off = (y>>3)*rowBytes*8 +
					// ((x*4)>>4)*128 + (y&7)*16 + (x*4 & 15).
					rowBytes := 2 * 4
					off = (y>>3)*rowBytes*8 + ((x*4)>>4)*128 + (y&7)*16 + (x * 4 & 15)
				} else {
					off = (y*2 + x) * 4
				}
				m.Write32(texBase+uint32(off), texel(x, y))
			}
		}
		vbase := uint32(0x08003000)
		white := rgbaWord(255, 255, 255, 255)
		writeVertTex(m, vbase, 0, 0, 0, 0, 0, white)
		writeVertTex(m, vbase+18, 2, 2, 0, 1, 1, white)
		texMode := uint32(0)
		if swizzled {
			texMode = 1
		}
		putList(m, listAddr, withFB(
			cmd(0x14, 0x080000),           // ORIGIN
			cmd(0xA0, 0x4000),             // TEXADDR0
			cmd(0xB8, 1|(1<<8)),           // TEXSIZE0: 2x2
			cmd(0xA8, 2),                  // TEXBUFWIDTH0
			cmd(0xC5, 3),                  // TEXFORMAT 8888
			cmd(0xC0, float24(1)),         // TEXSCALEU
			cmd(0xC1, float24(1)),         // TEXSCALEV
			cmd(0x53, 1),                  // TEXENABLE
			cmd(0xC4, texMode),            // TEXMODE swizzle flag
			cmd(0x12, vtypeThroughTexPos), // VTYPE
			cmd(0x01, 0x3000),             // VADDR
			cmd(0x04, (6<<16)|2),          // PRIM sprites, 2
			cmd(0x0C, 0),                  // END
		)...)
		if st := g.Submit(listAddr); st != StateCompleted {
			t.Fatalf("swizzled=%v: state = %v, want Completed", swizzled, st)
		}
		assertPixel(t, g, 0, 0, 255, 0, 0, 255)
		assertPixel(t, g, 1, 0, 0, 255, 0, 255)
		assertPixel(t, g, 0, 1, 0, 0, 255, 255)
		assertPixel(t, g, 1, 1, 255, 255, 255, 255)
	}
}

// -----------------------------------------------------------------------------
// Gate 4: depth test
// -----------------------------------------------------------------------------

func TestDepth(t *testing.T) {
	// Geometry note: with the z buffer at byte offset 0x8000 and a
	// 512 wide 8888 framebuffer, a large clear region or triangle would
	// make color and depth writes overlap in VRAM. The gate therefore
	// uses a small corner region and triangle, where the prescribed
	// layout does not collide: color bytes for pixels in rows 0..15 end
	// below offset 0x8000, and depth bytes start at 0x8000.
	m := newFakeMem()
	g := New(m)
	// Clear color (black) and depth (0xFFFF) together.
	putList(m, listAddr, withFB(
		cmd(0x9E, 0x8000),      // ZBUFPTR
		cmd(0x9F, 512),         // ZBUFWIDTH
		cmd(0xD4, 3),           // ZTESTENABLE test+write
		cmd(0xD5, 4),           // ZFUNC Less
		cmd(0x15, 0),           // REGION1 (0,0)
		cmd(0x16, 15|(15<<10)), // REGION2 (15,15)
		cmd(0xC9, 0),           // CLEARCOLOR black
		cmd(0xCA, 0xFF),        // CLEARALPHA
		cmd(0xCB, 0xFFFF),      // CLEARDEPTH
		cmd(0xCC, 0x1|0x2|0x8), // CLEAR enable|color|depth
		cmd(0x04, (3<<16)|3),   // PRIM (runs the clear)
		cmd(0x0C, 0),           // END
	)...)
	if st := g.Submit(listAddr); st != StateCompleted {
		t.Fatalf("clear state = %v", st)
	}
	draw := func(z uint16, color uint32) {
		base := uint32(0x08002000)
		writeVertColorPos(m, base, 0, 0, z, color)
		writeVertColorPos(m, base+10, 8, 0, z, color)
		writeVertColorPos(m, base+20, 0, 8, z, color)
		putList(m, listAddr, triList()...)
		if st := g.Submit(listAddr); st != StateCompleted {
			t.Fatalf("draw state = %v", st)
		}
	}
	red := rgbaWord(255, 0, 0, 255)
	green := rgbaWord(0, 255, 0, 255)
	draw(40000, red)   // far first
	draw(10000, green) // near wins
	assertPixel(t, g, 2, 2, 0, 255, 0, 255)
	draw(40000, red) // far again: must be rejected
	assertPixel(t, g, 2, 2, 0, 255, 0, 255)
	// Stored depth at the center is the near value.
	depthAddr := uint32(0x04008000) + uint32((2*512+2)*2)
	if d := m.Read16(depthAddr); d != 10000 {
		t.Errorf("stored depth = %d, want 10000", d)
	}
}

// -----------------------------------------------------------------------------
// Gate 5: sprite rectangle
// -----------------------------------------------------------------------------

func TestSprite(t *testing.T) {
	m := newFakeMem()
	g := New(m)
	blue := rgbaWord(0, 0, 255, 255)
	base := uint32(0x08002000)
	writeVertColorPos(m, base, 0, 0, 0, blue)
	writeVertColorPos(m, base+10, 4, 3, 0, blue)
	putList(m, listAddr, withFB(
		cmd(0x14, 0x080000),
		cmd(0x12, vtypeThroughColorPos),
		cmd(0x01, 0x2000),
		cmd(0x04, (6<<16)|2), // PRIM sprites, 2
		cmd(0x0C, 0),
	)...)
	if st := g.Submit(listAddr); st != StateCompleted {
		t.Fatalf("state = %v", st)
	}
	for _, p := range [][2]int{{0, 0}, {3, 0}, {0, 2}, {3, 2}, {2, 1}} {
		assertPixel(t, g, p[0], p[1], 0, 0, 255, 255)
	}
	for _, p := range [][2]int{{4, 0}, {0, 3}} {
		r, gr, b, a := g.FramebufferPixel(p[0], p[1])
		if r != 0 || gr != 0 || b != 0 || a != 0 {
			t.Errorf("pixel %v = (%d,%d,%d,%d), want untouched zero", p, r, gr, b, a)
		}
	}
}

// -----------------------------------------------------------------------------
// Gate 6: SIGNAL stall and FINISH on Continue
// -----------------------------------------------------------------------------

func TestSignalFinish(t *testing.T) {
	m := newFakeMem()
	g := New(m)
	putList(m, listAddr,
		cmd(0x00, 0),            // NOP
		cmd(0x0E, 0x2A|(3<<16)), // SIGNAL value 0x2A, behavior 3
		cmd(0x0F, 0),            // FINISH (after the signal)
		cmd(0x0C, 0),            // END
	)
	if st := g.Submit(listAddr); st != StateStalled {
		t.Fatalf("state = %v, want Stalled", st)
	}
	value, behavior, finished := g.SignalInfo()
	if value&0xFFFF != 0x2A {
		t.Errorf("SignalValue = %#x, want low bits 0x2a", value)
	}
	if behavior != 3 {
		t.Errorf("SignalBehavior = %d, want 3", behavior)
	}
	if finished {
		t.Error("FinishedSeen = true before Continue, want false")
	}
	if st := g.Continue(); st != StateCompleted {
		t.Fatalf("after Continue state = %v, want Completed", st)
	}
	if !g.FinishedSeen {
		t.Error("FinishedSeen = false after Continue, want true")
	}
	for op, want := range map[int]uint64{0x0E: 1, 0x0F: 1, 0x0C: 1} {
		if got := g.CmdCount(op); got != want {
			t.Errorf("CmdCount(%#02x) = %d, want %d", op, got, want)
		}
	}
}

// -----------------------------------------------------------------------------
// Gate 7: CLUT8 texture
// -----------------------------------------------------------------------------

func TestCLUT(t *testing.T) {
	m := newFakeMem()
	g := New(m)
	// 2x1 CLUT8 texture: indices [1, 0] at RAM offset 0x4000.
	texBase := uint32(0x08004000)
	m.Write8(texBase, 1)
	m.Write8(texBase+1, 0)
	// Palette at RAM offset 0x5000: entry 0 white, entry 1 magenta.
	clutBase := uint32(0x08005000)
	m.Write32(clutBase, rgbaWord(255, 255, 255, 255))
	m.Write32(clutBase+4, rgbaWord(255, 0, 255, 255))
	// Sprite vertices.
	vbase := uint32(0x08003000)
	white := rgbaWord(255, 255, 255, 255)
	writeVertTex(m, vbase, 0, 0, 0, 0, 0, white)
	writeVertTex(m, vbase+18, 2, 1, 0, 1, 1, white)
	putList(m, listAddr, withFB(
		cmd(0x14, 0x080000),           // ORIGIN
		cmd(0xA0, 0x4000),             // TEXADDR0
		cmd(0xB8, 1),                  // TEXSIZE0: w=2, h=1
		cmd(0xA8, 2),                  // TEXBUFWIDTH0
		cmd(0xC5, 5),                  // TEXFORMAT CLUT8
		cmd(0xB0, 0x5000),             // CLUTADDR
		cmd(0xB1, 3|(31<<8)),          // CLUTFORMAT 8888, shift 0, mask 31, start 0
		cmd(0xB2, 1),                  // CLUTLOAD 1 block = 8 entries
		cmd(0xC0, float24(1)),         // TEXSCALEU
		cmd(0xC1, float24(1)),         // TEXSCALEV
		cmd(0x53, 1),                  // TEXENABLE
		cmd(0x12, vtypeThroughTexPos), // VTYPE
		cmd(0x01, 0x3000),             // VADDR
		cmd(0x04, (6<<16)|2),          // PRIM sprites, 2
		cmd(0x0C, 0),                  // END
	)...)
	if st := g.Submit(listAddr); st != StateCompleted {
		t.Fatalf("state = %v", st)
	}
	// Index 1 is magenta, index 0 is white.
	assertPixel(t, g, 0, 0, 255, 0, 255, 255)
	assertPixel(t, g, 1, 0, 255, 255, 255, 255)
}

// -----------------------------------------------------------------------------
// Gate 8: blending
// -----------------------------------------------------------------------------

func TestBlend(t *testing.T) {
	m := newFakeMem()
	g := New(m)
	putList(m, listAddr, clearList(0x0000FF)...) // opaque red destination
	if st := g.Submit(listAddr); st != StateCompleted {
		t.Fatalf("clear state = %v", st)
	}
	writeTestTri(m, 0, rgbaWord(0, 255, 0, 128)) // green, alpha 128
	putList(m, listAddr, withFB(
		cmd(0xCC, 0),               // CLEAR mode off
		cmd(0xD6, 1),               // BLENDENABLE
		cmd(0xD7, 0|(4<<4)|(5<<8)), // BLENDFUNC Add, SrcAlpha, OneMinusSrcAlpha
		cmd(0x14, 0x080000),
		cmd(0x12, vtypeThroughColorPos),
		cmd(0x01, 0x2000),
		cmd(0x04, (3<<16)|3),
		cmd(0x0C, 0),
	)...)
	if st := g.Submit(listAddr); st != StateCompleted {
		t.Fatalf("draw state = %v", st)
	}
	r, gr, _, _ := g.FramebufferPixel(20, 20)
	// r = 255 * (1 - 128/255) = 127, g = 255 * 128/255 = 128.
	if r < 126 || r > 128 {
		t.Errorf("blended r = %d, want about 127", r)
	}
	if gr < 127 || gr > 129 {
		t.Errorf("blended g = %d, want about 128", gr)
	}
}

// -----------------------------------------------------------------------------
// Gate 9: alpha test
// -----------------------------------------------------------------------------

func TestAlphaTest(t *testing.T) {
	m := newFakeMem()
	g := New(m)
	putList(m, listAddr, clearList(0x0000FF)...) // red destination
	if st := g.Submit(listAddr); st != StateCompleted {
		t.Fatalf("clear state = %v", st)
	}
	writeTestTri(m, 0, rgbaWord(0, 255, 0, 0)) // alpha 0: must be dropped
	putList(m, listAddr, withFB(
		cmd(0xCC, 0),
		cmd(0xDA, 1),                     // ALPHATESTENABLE
		cmd(0xDB, 2|(255<<8)|(0xFF<<16)), // ALPHAFUNC Equal, ref 255, mask 0xFF
		cmd(0x14, 0x080000),
		cmd(0x12, vtypeThroughColorPos),
		cmd(0x01, 0x2000),
		cmd(0x04, (3<<16)|3),
		cmd(0x0C, 0),
	)...)
	if st := g.Submit(listAddr); st != StateCompleted {
		t.Fatalf("draw state = %v", st)
	}
	assertPixel(t, g, 20, 20, 255, 0, 0, 255) // still the clear color
}

// -----------------------------------------------------------------------------
// Gate 10: JUMP, CALL, RET
// -----------------------------------------------------------------------------

func TestJumpCallRet(t *testing.T) {
	m := newFakeMem()
	g := New(m)
	putList(m, listAddr,
		cmd(0x10, listAddr>>8), // BASE: basePtr = listAddr
		cmd(0x08, 12),          // JUMP to word index 3, skipping the SIGNAL
		cmd(0x0E, 0x99),        // SIGNAL 0x99: must never execute
		cmd(0x0A, 0x100),       // CALL subroutine at listAddr+0x100
		cmd(0x0C, 0),           // END
	)
	putList(m, listAddr+0x100,
		cmd(0x0E, 0x77), // SIGNAL 0x77: stalls inside the subroutine
		cmd(0x0B, 0),    // RET
	)
	if st := g.Submit(listAddr); st != StateStalled {
		t.Fatalf("state = %v, want Stalled (subroutine SIGNAL)", st)
	}
	if g.SignalValue&0xFFFF != 0x77 {
		t.Errorf("SignalValue = %#x, want 0x77", g.SignalValue)
	}
	if got := g.CmdCount(0x0E); got != 1 {
		t.Errorf("CmdCount(SIGNAL) = %d, want 1 (the jumped-over SIGNAL never ran)", got)
	}
	if st := g.Continue(); st != StateCompleted {
		t.Fatalf("after Continue state = %v, want Completed", st)
	}
	for op, want := range map[int]uint64{0x08: 1, 0x0A: 1, 0x0B: 1, 0x0C: 1} {
		if got := g.CmdCount(op); got != want {
			t.Errorf("CmdCount(%#02x) = %d, want %d", op, got, want)
		}
	}
}

// -----------------------------------------------------------------------------
// Gate 11: viewport transform with float positions
// -----------------------------------------------------------------------------

func TestViewportFloat(t *testing.T) {
	m := newFakeMem()
	g := New(m)
	green := rgbaWord(0, 255, 0, 255)
	base := uint32(0x08002000)
	writeVertFloat(m, base, 10, 10, 0, green)
	writeVertFloat(m, base+16, 100, 10, 0, green)
	writeVertFloat(m, base+32, 10, 100, 0, green)
	putList(m, listAddr, withFB(
		cmd(0x14, 0x080000),      // ORIGIN
		cmd(0x4A, float24(1)),    // VPXSCALE = 1.0
		cmd(0x4B, float24(1)),    // VPYSCALE = 1.0
		cmd(0x4C, 0),             // VPXCENTER = 0
		cmd(0x4D, 0),             // VPYCENTER = 0
		cmd(0x12, vtypeFloatPos), // VTYPE float positions, not through
		cmd(0x01, 0x2000),        // VADDR
		cmd(0x04, (3<<16)|3),     // PRIM triangles, 3
		cmd(0x0C, 0),             // END
	)...)
	if st := g.Submit(listAddr); st != StateCompleted {
		t.Fatalf("state = %v", st)
	}
	assertPixel(t, g, 20, 20, 0, 255, 0, 255)
	r, gr, b, _ := g.FramebufferPixel(90, 90)
	if r != 0 || gr != 0 || b != 0 {
		t.Errorf("pixel (90,90) = (%d,%d,%d), want black (outside triangle)", r, gr, b)
	}
}

// -----------------------------------------------------------------------------
// Extra gates: counted no-draw commands, serialization round trip
// -----------------------------------------------------------------------------

func TestCountedNoDrawCommands(t *testing.T) {
	m := newFakeMem()
	g := New(m)
	putList(m, listAddr,
		cmd(0x05, 0x1234), // BEZIER: counted, no draw
		cmd(0x06, 0x55),   // SPLINE: counted, no draw
		cmd(0x07, 0x66),   // BBOX: counted, no draw
		cmd(0x09, 0x77),   // BJUMP: counted, never jumps
		cmd(0x00, 0),      // NOP
		cmd(0x0C, 0),      // END
	)
	if st := g.Submit(listAddr); st != StateCompleted {
		t.Fatalf("state = %v", st)
	}
	for _, op := range []int{0x05, 0x06, 0x07, 0x09} {
		if got := g.CmdCount(op); got != 1 {
			t.Errorf("CmdCount(%#02x) = %d, want 1", op, got)
		}
	}
	if got := g.TotalCommands(); got != 6 {
		t.Errorf("TotalCommands = %d, want 6", got)
	}
	if got := g.Reg(0x05); got != 0x1234 {
		t.Errorf("Reg(BEZIER) = %#x, want 0x1234 (every command stores its arg)", got)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	m := newFakeMem()
	g := New(m)
	putList(m, listAddr, clearList(0x0000FF)...)
	if st := g.Submit(listAddr); st != StateCompleted {
		t.Fatalf("state = %v", st)
	}
	data := g.Serialize()
	g2 := New(m)
	if err := g2.Deserialize(data); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if got := g2.Reg(0xC9); got != 0x0000FF {
		t.Errorf("restored Reg(CLEARCOLOR) = %#x, want 0xff", got)
	}
	if g2.State() != StateCompleted {
		t.Errorf("restored state = %v, want Completed", g2.State())
	}
	assertPixel(t, g2, 240, 136, 255, 0, 0, 255)
	if again := g2.Serialize(); !bytes.Equal(again, data) {
		t.Error("re-serialized state differs from the original bytes")
	}
	if err := g2.Deserialize(data[:len(data)-1]); err == nil {
		t.Error("Deserialize of truncated data: want error, got nil")
	}
	// ConvertDisplay returns 4 bytes per pixel of RGBA8.
	img := g.ConvertDisplay(2, 1)
	if len(img) != 8 || img[0] != 255 || img[3] != 255 {
		t.Errorf("ConvertDisplay(2,1) = %v, want red opaque first pixel", img)
	}
}
