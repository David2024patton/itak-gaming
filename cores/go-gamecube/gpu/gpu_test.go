package gpu

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// fakeMem is a flat big-endian test memory.
type fakeMem struct{ b []byte }

func newFakeMem() *fakeMem { return &fakeMem{b: make([]byte, 2<<20)} }

func (m *fakeMem) Read8(addr uint32) uint8 {
	if int(addr) < len(m.b) {
		return m.b[addr]
	}
	return 0
}

func (m *fakeMem) Read16(addr uint32) uint16 {
	return uint16(m.Read8(addr))<<8 | uint16(m.Read8(addr+1))
}

func (m *fakeMem) Read32(addr uint32) uint32 {
	return uint32(m.Read8(addr))<<24 | uint32(m.Read8(addr+1))<<16 |
		uint32(m.Read8(addr+2))<<8 | uint32(m.Read8(addr+3))
}

func (m *fakeMem) put16(addr uint32, v uint16) {
	m.b[addr] = byte(v >> 8)
	m.b[addr+1] = byte(v)
}

func (m *fakeMem) putBytes(addr uint32, data []byte) { copy(m.b[addr:], data) }

// stream builds FIFO byte streams.
type stream struct{ bytes.Buffer }

func (s *stream) u32(v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	s.Write(b[:])
}

func (s *stream) bp(reg byte, val uint32) {
	s.WriteByte(0x61)
	s.u32(uint32(reg)<<24 | (val & 0xFFFFFF))
}

func (s *stream) cp(reg byte, val uint32) {
	s.WriteByte(0x08)
	s.WriteByte(reg)
	s.u32(val)
}

func (s *stream) xf(addr uint32, vals ...uint32) {
	s.WriteByte(0x10)
	s.u32(uint32(len(vals)-1)<<16 | addr)
	for _, v := range vals {
		s.u32(v)
	}
}

func (s *stream) xfF(addr uint32, vals ...float32) {
	us := make([]uint32, len(vals))
	for i, f := range vals {
		us[i] = math.Float32bits(f)
	}
	s.xf(addr, us...)
}

func (s *stream) vertex(x, y, z float32, c color8, tex ...float32) {
	for _, f := range []float32{x, y, z} {
		s.u32(math.Float32bits(f))
	}
	s.Write([]byte{c.r, c.g, c.b, c.a})
	for _, f := range tex {
		s.u32(math.Float32bits(f))
	}
}

func (s *stream) draw(op byte, n int) {
	s.WriteByte(op)
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], uint16(n))
	s.Write(b[:])
}

// setup writes the minimal documented register setup used
// by the gates: CP VCD/VAT with direct f32 position and
// direct RGBA8 color (plus direct f32 texcoord 0 when tex is
// true), an identity modelview and identity orthographic
// projection loaded through XF registers, and a 640x480
// viewport (xs 320, ys -240, zs 2^23, origins 320, 240, 2^23)
// from which the expected screen coordinates are derived:
// screen x = 320 + 320*nx, screen y = 240 - 240*ny, for
// object coordinates in the -1..1 range.
func setup(s *stream, tex bool) {
	vcdLo := uint32(1<<9) | uint32(1<<13) // position direct, color 0 direct
	s.cp(cpVCDLo, vcdLo)
	vcdHi := uint32(0)
	if tex {
		vcdHi = 1 // texcoord 0 direct
	}
	s.cp(cpVCDHi, vcdHi)
	g0 := uint32(1) | uint32(4<<1) | uint32(1<<13) | uint32(5<<14)
	if tex {
		g0 |= uint32(1<<21) | uint32(4<<22)
	}
	s.cp(cpVATGroupA+0, g0)
	s.cp(cpVATGroupB+0, 0)
	s.cp(cpVATGroupC+0, 0)
	s.xfF(0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0) // identity modelview
	// Orthographic projection: A=1, B=1, C=0, D=0, E=1, F=0,
	// type word 1 at 0x1026.
	s.xfF(xfProjection, 1, 1, 0, 0, 1, 0)
	s.xf(xfProjection+6, 1)
	s.xfF(xfViewport, 320, -240, 8388608, 320, 240, 8388608)
	s.bp(bpPEControl, pfbRGBA8)
	s.bp(bpZMode, 0) // depth test off for the basic gates
	s.bp(bpGenMode, 0)
}

func quadVerts(s *stream, z float32, c color8) {
	// Object-space corners of a quad spanning -0.5..0.5,
	// which the viewport maps to screen (160,120) through
	// (480,360).
	s.vertex(-0.5, 0.5, z, c)
	s.vertex(0.5, 0.5, z, c)
	s.vertex(0.5, -0.5, z, c)
	s.vertex(-0.5, -0.5, z, c)
}

func drawQuad(s *stream, z float32, c color8) {
	s.draw(0x80, 4)
	quadVerts(s, z, c)
}

func wantPixel(t *testing.T, g *GPU, x, y int, want color8) {
	t.Helper()
	r, gg, b, a := g.Pixel(x, y)
	got := color8{r, gg, b, a}
	if got != want {
		t.Errorf("Pixel(%d, %d) = %v, want %v", x, y, got, want)
	}
}

func TestSolidQuad(t *testing.T) {
	mem := newFakeMem()
	g := New(mem)
	var s stream
	setup(&s, false)
	red := color8{255, 0, 0, 255}
	drawQuad(&s, 0, red)
	g.WriteFIFO(s.Bytes())
	// Covered region is x 160..479, y 120..359.
	wantPixel(t, g, 160, 120, red) // top-left corner
	wantPixel(t, g, 479, 120, red)
	wantPixel(t, g, 160, 359, red)
	wantPixel(t, g, 479, 359, red) // bottom-right corner
	wantPixel(t, g, 320, 240, red) // center
	// Outside pixels keep the reset color (black, alpha 0
	// under the RGBA8 pixel format).
	black := color8{0, 0, 0, 0}
	wantPixel(t, g, 159, 120, black)
	wantPixel(t, g, 480, 240, black)
	wantPixel(t, g, 320, 119, black)
	wantPixel(t, g, 320, 360, black)
}

func TestVAT1Registers(t *testing.T) {
	mem := newFakeMem()
	g := New(mem)
	var s stream
	setup(&s, false)
	// Program VAT 1 through the documented group addresses:
	// group A at 0x71, group B at 0x81, group C at 0x91,
	// with the same format values setup uses for VAT 0.
	g0 := uint32(1) | uint32(4<<1) | uint32(1<<13) | uint32(5<<14)
	s.cp(cpVATGroupA+1, g0)
	s.cp(cpVATGroupB+1, 0)
	s.cp(cpVATGroupC+1, 0)
	red := color8{255, 0, 0, 255}
	s.draw(0x81, 4) // quads, VAT 1
	quadVerts(&s, 0, red)
	g.WriteFIFO(s.Bytes())
	// Same covered and outside pixels as TestSolidQuad.
	// Covered region is x 160..479, y 120..359.
	wantPixel(t, g, 160, 120, red)
	wantPixel(t, g, 479, 120, red)
	wantPixel(t, g, 160, 359, red)
	wantPixel(t, g, 479, 359, red)
	wantPixel(t, g, 320, 240, red)
	black := color8{0, 0, 0, 0}
	wantPixel(t, g, 159, 120, black)
	wantPixel(t, g, 480, 240, black)
	wantPixel(t, g, 320, 119, black)
	wantPixel(t, g, 320, 360, black)
}

func TestIndexedPositionDraw(t *testing.T) {
	mem := newFakeMem()
	// Position array at 0x1000 in fake memory holding the
	// four quad corners from the existing tests: object
	// space -0.5..0.5 in x and y, z 0, three f32 components
	// per entry.
	const posBase = 0x1000
	const posStride = 12
	corners := [][3]float32{
		{-0.5, 0.5, 0},
		{0.5, 0.5, 0},
		{0.5, -0.5, 0},
		{-0.5, -0.5, 0},
	}
	for i, c := range corners {
		for j, f := range c {
			binary.BigEndian.PutUint32(mem.b[posBase+i*posStride+4*j:], math.Float32bits(f))
		}
	}
	g := New(mem)
	var s stream
	setup(&s, false)
	// VCD: position index16 (mode 3 at bits 9-10), color 0
	// direct (mode 1 at bits 13-14). VAT 0 is already set by
	// setup with position format f32 and 3 components.
	s.cp(cpVCDLo, uint32(3<<9)|uint32(1<<13))
	s.cp(cpVCDHi, 0)
	// CP array 0: base at 0xA0, stride at 0xB0.
	s.cp(cpArrayBase+0, posBase)
	s.cp(cpArrayStr+0, posStride)
	red := color8{255, 0, 0, 255}
	s.draw(0x80, 4) // quads, VAT 0
	for i := 0; i < 4; i++ {
		var idx [2]byte
		binary.BigEndian.PutUint16(idx[:], uint16(i))
		s.Write(idx[:])
		s.Write([]byte{red.r, red.g, red.b, red.a})
	}
	g.WriteFIFO(s.Bytes())
	wantPixel(t, g, 320, 240, red) // center
	wantPixel(t, g, 160, 120, red) // top-left corner
	wantPixel(t, g, 479, 359, red) // bottom-right corner
}

func TestDepthLessBothOrders(t *testing.T) {
	run := func(nearFirst bool) *GPU {
		mem := newFakeMem()
		g := New(mem)
		var s stream
		setup(&s, false)
		// Depth viewport: zs = -2^23, zo = 2^23, so the
		// near vertex (z = -0.25) produces depth
		// 8388608 + 2097152 = 10485760 and the far vertex
		// (z = -0.75) produces 14680064. LESS keeps the
		// smaller, nearer value.
		s.xfF(xfViewport, 320, -240, -8388608, 320, 240, 8388608)
		s.bp(bpZMode, 1|(1<<1)|(1<<4)) // enable, LESS, update
		red := color8{255, 0, 0, 255}
		green := color8{0, 255, 0, 255}
		if nearFirst {
			drawQuad(&s, -0.25, green)
			drawQuad(&s, -0.75, red)
		} else {
			drawQuad(&s, -0.75, red)
			drawQuad(&s, -0.25, green)
		}
		g.WriteFIFO(s.Bytes())
		return g
	}
	for _, nearFirst := range []bool{false, true} {
		g := run(nearFirst)
		wantPixel(t, g, 320, 240, color8{0, 255, 0, 255})
		if d := g.Depth(320, 240); d != 10485760 {
			t.Errorf("nearFirst=%v: Depth(320,240) = %d, want 10485760", nearFirst, d)
		}
	}
}

// texturedSetup points stage 0 at a texture in fake memory.
func texturedSetup(s *stream, format uint32, w, h int, addr uint32) {
	s.bp(bpTexGroup0+0, 0) // clamp, nearest
	s.bp(bpTexGroup0+2, uint32(w-1)|uint32(h-1)<<10|format<<20)
	s.bp(bpTexGroup0+5, addr>>5)
}

func texTriangle(s *stream, c color8) {
	s.draw(0x90, 3)
	s.vertex(-1, 1, -0.5, c, 0, 0)
	s.vertex(1, 1, -0.5, c, 1, 0)
	s.vertex(-1, -1, -0.5, c, 0, 1)
}

func TestTexturedTriangleRGB565(t *testing.T) {
	mem := newFakeMem()
	// 4x4 RGB565 texture at 0x1000: one 4x4 tile, row major.
	const base = 0x1000
	mem.put16(base+0, 0xF800)     // texel (0,0) red
	mem.put16(base+3*2, 0x07E0)   // texel (3,0) green
	mem.put16(base+3*4*2, 0x001F) // texel (0,3) blue
	g := New(mem)
	var s stream
	setup(&s, true)
	texturedSetup(&s, tfRGB, 4, 4, base)
	white := color8{255, 255, 255, 255}
	texTriangle(&s, white)
	g.WriteFIFO(s.Bytes())
	// The triangle covers the top-left half of the screen;
	// u follows screen x / 640 and v follows screen y / 480.
	wantPixel(t, g, 10, 10, color8{255, 0, 0, 255})
	wantPixel(t, g, 600, 10, color8{0, 255, 0, 255})
	wantPixel(t, g, 10, 470, color8{0, 0, 255, 255})
}

func TestTexturedCI8WithTLUT(t *testing.T) {
	mem := newFakeMem()
	// 4x4 CI8 texture at 0x1000: 8x4 tile, so texel (x, y)
	// sits at tile offset y*8 + x.
	const base = 0x1000
	mem.b[base+0] = 1  // (0,0) -> palette 1
	mem.b[base+3] = 3  // (3,0) -> palette 3
	mem.b[base+24] = 2 // (0,3) -> palette 2
	// TLUT source at 0x2000, RGB565 entries.
	const tlutSrc = 0x2000
	mem.put16(tlutSrc+0, 0x0000)
	mem.put16(tlutSrc+2, 0xFFE0) // yellow
	mem.put16(tlutSrc+4, 0x07FF) // cyan
	mem.put16(tlutSrc+6, 0xF800) // red
	g := New(mem)
	var s stream
	setup(&s, true)
	texturedSetup(&s, tfCI8, 4, 4, base)
	s.bp(bpTexGroup0+6, 1<<10) // TLUT at TMEM 0, format RGB565
	s.bp(bpLoadTLUT0, tlutSrc>>5)
	s.bp(bpLoadTLUT1, 3<<10) // (count-1) = 3, TMEM offset 0
	white := color8{255, 255, 255, 255}
	texTriangle(&s, white)
	g.WriteFIFO(s.Bytes())
	wantPixel(t, g, 10, 10, color8{255, 255, 0, 255})
	wantPixel(t, g, 600, 10, color8{255, 0, 0, 255})
	wantPixel(t, g, 10, 470, color8{0, 255, 255, 255})
}

func TestBlendAdd(t *testing.T) {
	mem := newFakeMem()
	g := New(mem)
	var s stream
	setup(&s, false)
	drawQuad(&s, -0.5, color8{200, 0, 0, 255})
	// Blend on, source factor ONE (bits 8-10 = 1),
	// destination factor ONE (bits 5-7 = 1), updates on.
	s.bp(bpBlendMode, 1|(1<<3)|(1<<4)|(1<<5)|(1<<8))
	drawQuad(&s, -0.5, color8{0, 100, 0, 255})
	g.WriteFIFO(s.Bytes())
	wantPixel(t, g, 320, 240, color8{200, 100, 0, 255})
	wantPixel(t, g, 160, 120, color8{200, 100, 0, 255})
}

func TestCMPTexture(t *testing.T) {
	mem := newFakeMem()
	// One 8x8 CMP tile at 0x1000 (32 bytes). Sub-block 0
	// covers texels (0..3, 0..3): endpoints white and black
	// (white greater, so the four-color interpolation mode
	// applies), every row uses indices 0,1,2,3.
	const base = 0x1000
	var tile [32]byte
	tile[0], tile[1] = 0xFF, 0xFF // c0 white
	tile[2], tile[3] = 0x00, 0x00 // c1 black
	for r := 0; r < 4; r++ {
		tile[4+r] = 0x1B // indices 0,1,2,3
	}
	// Sub-blocks 1-3: solid red via equal endpoints.
	for sb := 1; sb < 4; sb++ {
		off := sb * 8
		tile[off], tile[off+1] = 0xF8, 0x00
		tile[off+2], tile[off+3] = 0xF8, 0x00
	}
	mem.putBytes(base, tile[:])
	g := New(mem)
	var s stream
	setup(&s, true)
	texturedSetup(&s, tfCMP, 8, 8, base)
	white := color8{255, 255, 255, 255}
	s.draw(0x80, 4)
	s.vertex(-1, 1, -0.5, white, 0, 0)
	s.vertex(1, 1, -0.5, white, 1, 0)
	s.vertex(1, -1, -0.5, white, 1, 1)
	s.vertex(-1, -1, -0.5, white, 0, 1)
	g.WriteFIFO(s.Bytes())
	// Texel (x, 0) center sits at screen ((x+0.5)*80,
	// (0.5)*60) = (x*80+40, 30).
	wantPixel(t, g, 40, 30, color8{255, 255, 255, 255}) // index 0
	wantPixel(t, g, 120, 30, color8{0, 0, 0, 255})      // index 1
	wantPixel(t, g, 200, 30, color8{170, 170, 170, 255})
	wantPixel(t, g, 280, 30, color8{85, 85, 85, 255})
	// Sub-block 1 (texels 4..7 of row 0) is solid red.
	wantPixel(t, g, 360, 30, color8{255, 0, 0, 255})
}

func TestFIFOSplitOneByteChunks(t *testing.T) {
	build := func() (*GPU, *fakeMem) {
		mem := newFakeMem()
		g := New(mem)
		var s stream
		setup(&s, false)
		drawQuad(&s, -0.5, color8{10, 200, 30, 255})
		return g, mem
	}
	var s stream
	setup(&s, false)
	drawQuad(&s, -0.5, color8{10, 200, 30, 255})
	data := s.Bytes()

	g1, _ := build()
	g1.WriteFIFO(data)
	g2, _ := build()
	for _, b := range data {
		g2.WriteFIFO([]byte{b})
	}
	dst1 := make([]uint32, 640*480)
	dst2 := make([]uint32, 640*480)
	g1.Present(dst1)
	g2.Present(dst2)
	for i := range dst1 {
		if dst1[i] != dst2[i] {
			t.Fatalf("Present differs at pixel %d: %08x vs %08x", i, dst1[i], dst2[i])
		}
	}
	wantPixel(t, g2, 320, 240, color8{10, 200, 30, 255})
}

func TestSerializeRoundTrip(t *testing.T) {
	mem := newFakeMem()
	g := New(mem)
	var s stream
	setup(&s, false)
	drawQuad(&s, -0.5, color8{1, 2, 3, 255})
	g.WriteFIFO(s.Bytes())
	state := g.Serialize()
	if len(state) != SerializeSize {
		t.Fatalf("Serialize size = %d, want %d", len(state), SerializeSize)
	}
	g2 := New(mem)
	if err := g2.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	wantPixel(t, g2, 320, 240, color8{1, 2, 3, 255})
	if err := g2.Deserialize([]byte{1, 2, 3}); err != ErrBadState {
		t.Fatalf("short state error = %v, want ErrBadState", err)
	}
}

func TestBPClearRect(t *testing.T) {
	mem := newFakeMem()
	g := New(mem)
	var s stream
	setup(&s, false)
	// Copy source rectangle (160,120) to (479,359), clear
	// color RGBA (10, 20, 30, 255), clear depth 0x123456,
	// then trigger the copy with the clear bit (bit 14).
	s.bp(bpEFBTL, 160|(120<<10))
	s.bp(bpEFBBR, 479|(359<<10))
	s.bp(bpClearAR, 10|(255<<16))
	s.bp(bpClearGB, 20|(30<<8))
	s.bp(bpClearZ, 0x123456)
	s.bp(bpTriggerCopy, 1<<14)
	g.WriteFIFO(s.Bytes())
	wantPixel(t, g, 160, 120, color8{10, 20, 30, 255})
	wantPixel(t, g, 479, 359, color8{10, 20, 30, 255})
	wantPixel(t, g, 159, 120, color8{0, 0, 0, 0})
	if d := g.Depth(320, 240); d != 0x123456 {
		t.Errorf("Depth(320,240) = %06x, want 123456", d)
	}
	if d := g.Depth(0, 0); d != 0xFFFFFF {
		t.Errorf("Depth(0,0) = %06x, want ffffff (outside clear rect)", d)
	}
	// Present maps the copy rectangle onto the destination.
	dst := make([]uint32, 640*480)
	g.Present(dst)
	if dst[0] != 0xFF0A141E {
		t.Errorf("Present[0] = %08x, want ff0a141e", dst[0])
	}
}

func TestPointAndLine(t *testing.T) {
	mem := newFakeMem()
	g := New(mem)
	var s stream
	setup(&s, false)
	c := color8{0, 0, 255, 255}
	s.draw(0xB8, 1) // points, VAT 0
	s.vertex(0, 0, -0.5, c)
	s.draw(0xA8, 2) // lines, VAT 0
	s.vertex(-0.5, 0, -0.5, c)
	s.vertex(0.5, 0, -0.5, c)
	g.WriteFIFO(s.Bytes())
	// Object (0,0) maps to screen (320,240); the horizontal
	// line at object y 0 maps to screen y 240, x 160..480.
	wantPixel(t, g, 320, 240, c)
	wantPixel(t, g, 200, 240, c)
	wantPixel(t, g, 479, 240, c)
	wantPixel(t, g, 320, 241, color8{0, 0, 0, 0})
}

func TestMMIOStoredRegisters(t *testing.T) {
	g := New(newFakeMem())
	g.WriteMMIO(0x10, 0x1234)
	if v := g.ReadMMIO(0x10); v != 0x1234 {
		t.Fatalf("MMIO 0x10 = %04x, want 1234", v)
	}
	if v := g.ReadMMIO(0x00); v&0x0C != 0x0C {
		t.Fatalf("status = %04x, want idle bits 2 and 3 set", v)
	}
}
