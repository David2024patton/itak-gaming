package pvr

import "testing"

// Fog gates for DC-3, in the house style of render_test.go:
// the standard 32 by 32 RGB0565 scene from newRenderPVR,
// one untextured opaque quad at constant depth over the
// standard background, and hand computed framebuffer
// pixels read back through Present.

// fogISPWord builds an ISP/TSP word like ispWord and adds
// the fog control field at bits 21:20.
func fogISPWord(mode int, offset bool) uint32 {
	return ispWord(DepthAlways, 0, false, false, offset) | uint32(mode)<<20
}

// fogPolyHeader submits a single volume opaque polygon
// header with explicit base and offset header colors.
func fogPolyHeader(p *PVR, isp, baseColor, offColor uint32) {
	tsp := tspWord(0, BlendOne, BlendZero, false, 8, 8)
	packet(p, pcw(paraPolygon, ListOpaque), isp, tsp, 0, baseColor, offColor, 0, 0)
}

// setFogTable writes all 128 fog table entries through the
// register window at offset 0x200, two entries per word.
func setFogTable(p *PVR, entries [128]uint16) {
	for i := 0; i < 128; i += 2 {
		v := uint32(entries[i]) | uint32(entries[i+1])<<16
		p.Write32(regBase+0x200+uint32(i*2), v)
	}
}

// fogScene builds the shared table fog scene: a black
// fullscreen quad at Z 0.5 (eye distance 1 / 0.5 = 2) with
// the given fog mode, FOG_COL_RAM white, FOG_DENSITY
// 0x8802 (mantissa byte 0x88 = 136, so 136 / 128 = 1.0625,
// times 2^2 = 4.25). The scaled index is 4.25 * 2 = 8.5,
// exactly between table entries 8 and 9, whose low bytes
// are 100 and 156 (the high bytes are junk on purpose: the
// alpha is the low byte only). The interpolated alpha is
// (100 + 156) / 2 = 128, so the mix factor is 128 / 255.
func fogScene(t *testing.T, mode int) *PVR {
	t.Helper()
	p, _ := newRenderPVR(t)
	var entries [128]uint16
	entries[8] = 0xAB64
	entries[9] = 0xCD9C
	setFogTable(p, entries)
	p.Write32(regBase+RegFogColRAM, 0x00FFFFFF)
	p.Write32(regBase+RegFogDensity, 0x8802)
	fogPolyHeader(p, fogISPWord(mode, false), 0xFF000000, 0)
	quad(p, ListOpaque, 0.5, 0, 0, 0, 0, 0, 0, 32, 32, 0xFF000000)
	return p
}

// FOG_DENSITY decoding: mantissa byte over 128 times two
// to the signed exponent. Every case is a dyadic value, so
// the float64 results are exact.
func TestFogDensityDecode(t *testing.T) {
	p, _ := newRenderPVR(t)
	for _, tc := range []struct {
		reg  uint32
		want float64
	}{
		{0x8002, 4},        // 1.0 * 2^2
		{0x8802, 4.25},     // 1.0625 * 2^2
		{0x80FF, 0.5},      // 1.0 * 2^-1
		{0x7F01, 1.984375}, // (127 / 128) * 2^1
		{0x0000, 0},
	} {
		p.Write32(regBase+RegFogDensity, tc.reg)
		if got := p.fogDensity(); got != tc.want {
			t.Errorf("density of %#06x = %v, want %v", tc.reg, got, tc.want)
		}
	}
}

// Gate (1): table fog, exact pixel. Black mixed toward
// white by 128 / 255 gives 128 / 255 per channel, which
// quantizes in 0565 to r5 16, g6 32, b5 16, expanded by
// Present to R 132, G 130, B 132.
func TestFogTableLookupExact(t *testing.T) {
	p := fogScene(t, 0)
	out := renderNow(t, p)
	want := uint32(0xFF848284)
	for _, xy := range [][2]int{{16, 16}, {3, 27}} {
		if got := px(out, xy[0], xy[1]); got != want {
			t.Errorf("pixel %v = %#08x, want %#08x", xy, got, want)
		}
	}
}

// Gate (2): fog mode 2 (No Fog) on the identical scene
// leaves the shaded pixel exact: black.
func TestFogNoFogMode(t *testing.T) {
	p := fogScene(t, 2)
	out := renderNow(t, p)
	if got := px(out, 16, 16); got != 0xFF000000 {
		t.Errorf("no fog center = %#08x, want black 0xFF000000", got)
	}
}

// Gate (3): per vertex fog. The flat offset color alpha is
// 128 / 255, the base color is blue, and FOG_COL_VERT is
// green, so the mix by a = 128 / 255 is R 0, G 128 / 255,
// B 127 / 255, quantizing to r5 0, g6 32, b5 15, expanded
// to R 0, G 130, B 123. With the Offset bit clear the same
// scene is unfogged, per the documented rule.
func TestFogPerVertex(t *testing.T) {
	build := func(offset bool) *PVR {
		p, _ := newRenderPVR(t)
		p.Write32(regBase+RegFogColVert, 0x0000FF00)
		fogPolyHeader(p, fogISPWord(1, offset), 0xFF0000FF, 0x80000000)
		quad(p, ListOpaque, 0.5, 0, 0, 0, 0, 0, 0, 32, 32, 0xFF0000FF)
		return p
	}
	out := renderNow(t, build(true))
	if got := px(out, 16, 16); got != 0xFF00827B {
		t.Errorf("per vertex center = %#08x, want 0xFF00827B", got)
	}
	out2 := renderNow(t, build(false))
	if got := px(out2, 16, 16); got != 0xFF0000FF {
		t.Errorf("per vertex without offset = %#08x, want blue 0xFF0000FF", got)
	}
}

// Gate (4): mode 3 (Look Up Table Mode 2) on the fogScene.
// The documented substitution makes the fog color the
// polygon color itself, and this renderer has one color
// buffer (the modifier value is a counter, not a second
// accumulation buffer), so the mix collapses to an
// identity: a mode 3 frame must equal a mode 2 frame
// pixel for pixel. See the mode 3 note in fog.go.
func TestFogLUTMode2EqualsNoFog(t *testing.T) {
	out3 := renderNow(t, fogScene(t, 3))
	out2 := renderNow(t, fogScene(t, 2))
	for i := range out2 {
		if out3[i] != out2[i] {
			t.Fatalf("pixel %d: mode 3 = %#08x, mode 2 = %#08x", i, out3[i], out2[i])
		}
	}
	if got := px(out3, 16, 16); got != 0xFF000000 {
		t.Errorf("mode 3 center = %#08x, want unfogged black 0xFF000000", got)
	}
}

// Gate (5): depth gradient. FOG_DENSITY is 1.0 (mantissa
// byte 0x80, exponent 0) and the table ramps by index:
// entry 2 is 64, entry 4 is 128. The left half quad sits
// at Z 0.5 (distance 2, index 2) and the right half at
// Z 0.25 (distance 4, index 4), both black, FOG_COL_RAM
// white. Left: 64 / 255 per channel quantizes to r5 8,
// g6 16, b5 8, expanded to R 66, G 65, B 66. Right:
// 128 / 255 per channel, the gate (1) values.
func TestFogDepthGradient(t *testing.T) {
	p, _ := newRenderPVR(t)
	var entries [128]uint16
	entries[2] = 64
	entries[4] = 128
	setFogTable(p, entries)
	p.Write32(regBase+RegFogColRAM, 0x00FFFFFF)
	p.Write32(regBase+RegFogDensity, 0x8000)
	fogPolyHeader(p, fogISPWord(0, false), 0xFF000000, 0)
	quad(p, ListOpaque, 0.5, 0, 0, 0, 0, 0, 0, 16, 32, 0xFF000000)
	fogPolyHeader(p, fogISPWord(0, false), 0xFF000000, 0)
	quad(p, ListOpaque, 0.25, 0, 0, 0, 0, 16, 0, 32, 32, 0xFF000000)
	out := renderNow(t, p)
	if got := px(out, 8, 16); got != 0xFF424142 {
		t.Errorf("near pixel = %#08x, want 0xFF424142", got)
	}
	if got := px(out, 24, 16); got != 0xFF848284 {
		t.Errorf("far pixel = %#08x, want 0xFF848284", got)
	}
}
