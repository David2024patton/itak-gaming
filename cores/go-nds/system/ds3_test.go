package system_test

import (
	"bytes"
	"math"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-nds/bus"
	"github.com/davidpatton/itak-gaming/cores/go-nds/system"
)

// gxCmd sends one geometry command through its direct port
// on the ARM9 IO page.
func gxCmd(v bus.View, id uint8, params ...uint32) {
	port := uint32(0x04000400) + uint32(id)*4
	if len(params) == 0 {
		v.Write(port, 4, 0)
		return
	}
	for _, p := range params {
		v.Write(port, 4, p)
	}
}

func gxIdentity(v bus.View) {
	gxCmd(v, 0x10, 0) // MTX_MODE projection
	gxCmd(v, 0x15)    // MTX_IDENTITY
	gxCmd(v, 0x10, 1) // MTX_MODE position
	gxCmd(v, 0x15)
	gxCmd(v, 0x60, 0xBFFF0000) // VIEWPORT full screen
}

func gxVtx(v bus.View, x, y, z int) {
	gxCmd(v, 0x23, uint32(uint16(x))|uint32(uint16(y))<<16, uint32(uint16(z)))
}

// setupDisplay routes engine A BG0 to the 3D layer and
// assigns VRAM banks A (texture slot 0) and E (texture
// palette) for the geometry engine.
func setupDisplay(m *system.Machine) {
	v9 := m.Bus.ARM9()
	v9.Write(0x04000304, 2, 0x020F)     // POWCNT1
	v9.Write(0x04000000, 4, 0x00010108) // DISPCNT A: mode 1, BG0 on + 3D
	v9.Write(0x04000240, 1, 0x83)       // VRAMCNT A: texture image slot 0
	v9.Write(0x04000244, 1, 0x83)       // VRAMCNT E: texture palette
	v9.Write(0x04000354, 4, 0x7FFF)     // CLEAR_DEPTH far
}

func TestDS3ClipReadbackAndPosTest(t *testing.T) {
	m := system.New()
	v9 := m.Bus.ARM9()
	gxIdentity(v9)
	// A known projection matrix; identity position.
	mat := [16]uint32{0x1000, 0x0800, 0xFFFFF000, 3,
		0x2000, 0x1000, 0, 0xFFFFE000,
		1, 2, 3, 4,
		0x3000, 0xFFFFC000, 0x7FFFFFFF, 0x1000}
	gxCmd(v9, 0x10, 0)
	gxCmd(v9, 0x16, mat[:]...)
	gxCmd(v9, 0x10, 1)
	gxCmd(v9, 0x15)
	for i := 0; i < 16; i++ {
		if got := v9.Read(0x04000640+uint32(i*4), 4); got != mat[i] {
			t.Fatalf("CLIPMTX_RESULT[%d] = %08x want %08x", i, got, mat[i])
		}
	}
	// POS_TEST with a translated position matrix.
	gxIdentity(v9)
	gxCmd(v9, 0x1C, 0x800, 0xFFFFFC00, 0x2000)
	gxCmd(v9, 0x71, 0x1000, 0)
	want := [4]uint32{0x1800, 0xFFFFFC00, 0x2000, 0x1000}
	for i := 0; i < 4; i++ {
		if got := v9.Read(0x04000620+uint32(i*4), 4); got != want[i] {
			t.Fatalf("POS_RESULT[%d] = %08x want %08x", i, got, want[i])
		}
	}
}

func TestDS3RedTriangleScreen(t *testing.T) {
	m := system.New()
	setupDisplay(m)
	v9 := m.Bus.ARM9()
	v9.Write(0x04000350, 4, 31<<16) // CLEAR_COLOR black opaque
	gxIdentity(v9)
	gxCmd(v9, 0x29, 31<<16|0xC0)
	gxCmd(v9, 0x20, 0x001F)
	gxCmd(v9, 0x40, 0)
	gxVtx(v9, -0x800, -0x800, 0)
	gxVtx(v9, 0x800, -0x800, 0)
	gxVtx(v9, 0, 0x800, 0)
	gxCmd(v9, 0x50, 0)
	m.RunFrame()
	m.RunFrame() // the swap renders at VBlank; frame two shows it
	fb, _ := m.Framebuffer()
	if got := fb[96*256+128]; got != 0xFFFF0000 {
		t.Fatalf("center pixel = %08x want red", got)
	}
	if got := fb[2*256+2]; got != 0xFF000000 {
		t.Fatalf("corner pixel = %08x want clear black", got)
	}
}

// ds3Scene builds the determinism scene: a textured far quad
// under a translucent flat near quad with fog on, plus a PCM
// loop tone and a PSG square, all poked through the bus.
func ds3Scene(m *system.Machine) {
	setupDisplay(m)
	v9 := m.Bus.ARM9()
	v7 := m.Bus.ARM7()
	// 8x8 256-color texture: a four-color checker in index
	// space; palette entries 1..4 are red, green, blue,
	// white.
	a := m.Bus.VRAMBank(0)
	for ty := 0; ty < 8; ty++ {
		for tx := 0; tx < 8; tx++ {
			a[ty*8+tx] = byte(1 + (tx+ty)%4)
		}
	}
	e := m.Bus.VRAMBank(4)
	for i, c := range []uint16{0x001F, 0x03E0, 0x7C00, 0x7FFF} {
		e[2+2*i] = byte(c)
		e[3+2*i] = byte(c >> 8)
	}
	// DISP3DCNT: texture on, alpha blending on, fog on.
	v9.Write(0x04000060, 2, 0x89)
	// CLEAR_COLOR: mid gray, opaque, fog flagged.
	v9.Write(0x04000350, 4, 0x2108|0x8000|31<<16)
	// FOG_COLOR dark blue, alpha 20; offset 0x1000; density
	// table ramps 0, 4, 8, ...
	v9.Write(0x04000358, 4, 0x4000|20<<16)
	v9.Write(0x0400035C, 4, 0x1000)
	for i := 0; i < 32; i++ {
		v9.Write(0x04000360+uint32(i), 1, uint32(i*4))
	}
	gxIdentity(v9)
	// Textured far quad.
	gxCmd(v9, 0x29, 31<<16|0xC0)
	gxCmd(v9, 0x20, 0x7FFF)
	gxCmd(v9, 0x2A, 4<<26)
	gxCmd(v9, 0x2B, 0)
	gxCmd(v9, 0x40, 1)
	quad := [][4]int{
		{-0x1000, -0x1000, 0, 128}, {0x1000, -0x1000, 128, 128},
		{0x1000, 0x1000, 128, 0}, {-0x1000, 0x1000, 0, 0},
	}
	for _, q := range quad {
		gxCmd(v9, 0x22, uint32(uint16(q[2]))|uint32(uint16(q[3]))<<16)
		gxVtx(v9, q[0], q[1], 0x400)
	}
	// Translucent flat near quad (magenta, alpha 20).
	gxCmd(v9, 0x29, 20<<16|0xC0)
	gxCmd(v9, 0x20, 0x7C1F)
	gxCmd(v9, 0x40, 1)
	for _, q := range quad {
		gxVtx(v9, q[0]/2, q[1]/2, -0x400)
	}
	gxCmd(v9, 0x50, 0)
	// PCM16 loop tone on channel 0: channel rate
	// 16756991/512 = 32728 samples per second and 74 samples
	// per period gives a 442 Hz tone.
	sine := make([]byte, 74*2)
	for i := 0; i < 74; i++ {
		s := int16(math.Sin(2*math.Pi*float64(i)/74) * 20000)
		sine[2*i] = byte(uint16(s))
		sine[2*i+1] = byte(uint16(s) >> 8)
	}
	m.Bus.LoadBytes(false, 0x02010000, sine)
	v7.Write(0x04000400, 4, 0)          // ch0 CNT (placeholder lanes)
	v7.Write(0x04000404, 4, 0x02010000) // SAD
	v7.Write(0x04000408, 4, 65024)      // TMR: period 512
	v7.Write(0x0400040C, 4, 37)         // PNT 0, LEN 37 words
	v7.Write(0x04000400, 4, 0x80000000|1<<27|1<<29|127|64<<16)
	// PSG square on channel 8: duty 3 (half), TMR for an
	// 880 Hz square (rate 7040).
	v7.Write(0x04000488, 4, 63156) // TMR
	v7.Write(0x04000480, 4, 0x80000000|3<<29|3<<24|2<<27|100|64<<16)
	// SOUNDCNT: master volume 127, mixer both sides, enable.
	v7.Write(0x04000500, 4, 0x807F)
}

func TestDS3DeterminismGate(t *testing.T) {
	build := func() *system.Machine {
		m := system.New()
		ds3Scene(m)
		return m
	}
	m := build()
	for i := 0; i < 3; i++ {
		m.RunFrame()
	}
	m.AudioSamples() // drain
	state1 := m.Serialize()
	if len(state1) != system.StateSize {
		t.Fatalf("state size = %d want %d", len(state1), system.StateSize)
	}
	if state1[0] != 3 {
		t.Fatalf("state version = %d want 3", state1[0])
	}
	for i := 0; i < 4; i++ {
		m.RunFrame()
	}
	fbA1, fbB1 := m.Framebuffer()
	audio1 := m.AudioSamples()
	state2 := m.Serialize()

	m2 := build()
	if !m2.Deserialize(state1) {
		t.Fatal("deserialize rejected the DS-3 state")
	}
	for i := 0; i < 4; i++ {
		m2.RunFrame()
	}
	fbA2, fbB2 := m2.Framebuffer()
	audio2 := m2.AudioSamples()
	state3 := m2.Serialize()

	if len(audio1) < 4096 {
		t.Fatalf("audio captured %d values, want at least 4096 (2048 stereo samples)", len(audio1))
	}
	if len(audio1) != len(audio2) {
		t.Fatalf("audio lengths differ: %d vs %d", len(audio1), len(audio2))
	}
	for i := range audio1 {
		if audio1[i] != audio2[i] {
			t.Fatalf("audio sample %d differs: %d vs %d", i, audio1[i], audio2[i])
		}
	}
	for i := range fbA1 {
		if fbA1[i] != fbA2[i] {
			t.Fatalf("engine A pixel %d differs: %08x vs %08x", i, fbA1[i], fbA2[i])
		}
	}
	for i := range fbB1 {
		if fbB1[i] != fbB2[i] {
			t.Fatalf("engine B pixel %d differs", i)
		}
	}
	if !bytes.Equal(state2, state3) {
		t.Fatal("state bytes differ after restore and continuation")
	}
	t.Logf("state size %d bytes; audio compared %d samples", system.StateSize, len(audio1))
}

func TestDS3TexturedAndNearQuadScreen(t *testing.T) {
	m := system.New()
	setupDisplay(m)
	v9 := m.Bus.ARM9()
	v9.Write(0x04000060, 2, 0x01) // texture on
	v9.Write(0x04000350, 4, 31<<16)
	a := m.Bus.VRAMBank(0)
	for i := 0; i < 64; i++ {
		a[i] = 1
	}
	e := m.Bus.VRAMBank(4)
	e[2], e[3] = 0x1F, 0x00
	gxIdentity(v9)
	gxCmd(v9, 0x29, 31<<16|0xC0)
	gxCmd(v9, 0x20, 0x7FFF)
	gxCmd(v9, 0x2A, 4<<26)
	gxCmd(v9, 0x2B, 0)
	gxCmd(v9, 0x40, 1)
	quad := [][4]int{
		{-0x1000, -0x1000, 0, 128}, {0x1000, -0x1000, 128, 128},
		{0x1000, 0x1000, 128, 0}, {-0x1000, 0x1000, 0, 0},
	}
	for _, q := range quad {
		gxCmd(v9, 0x22, uint32(uint16(q[2]))|uint32(uint16(q[3]))<<16)
		gxVtx(v9, q[0], q[1], 0x400)
	}
	// Nearer flat green quad over the middle (texture
	// format off, so it is a pure color quad).
	gxCmd(v9, 0x2A, 0)
	gxCmd(v9, 0x20, 0x03E0)
	gxCmd(v9, 0x40, 1)
	for _, q := range quad {
		gxVtx(v9, q[0]/2, q[1]/2, -0x400)
	}
	gxCmd(v9, 0x50, 0)
	m.RunFrame()
	m.RunFrame()
	fb, _ := m.Framebuffer()
	if got := fb[96*256+128]; got != 0xFF00FF00 {
		t.Fatalf("overlap = %08x want green (near quad)", got)
	}
	if got := fb[10*256+10]; got != 0xFFFF0000 {
		t.Fatalf("far corner = %08x want red texel", got)
	}
}

func TestDS3PCMToneThroughMachine(t *testing.T) {
	m := system.New()
	v7 := m.Bus.ARM7()
	const n = 74
	sine := make([]byte, n*2)
	for i := 0; i < n; i++ {
		s := int16(math.Sin(2*math.Pi*float64(i)/n) * 20000)
		sine[2*i] = byte(uint16(s))
		sine[2*i+1] = byte(uint16(s) >> 8)
	}
	m.Bus.LoadBytes(false, 0x02010000, sine)
	v7.Write(0x04000404, 4, 0x02010000)
	v7.Write(0x04000408, 4, 65024)
	v7.Write(0x0400040C, 4, 37)
	v7.Write(0x04000500, 4, 0x807F)
	v7.Write(0x04000400, 4, 0x80000000|1<<27|1<<29|127|64<<16)
	for i := 0; i < 30; i++ {
		m.RunFrame()
	}
	samples := m.AudioSamples()
	// Count positive zero crossings on the left channel.
	cross := 0
	prev := int16(0)
	for i := 0; i+1 < len(samples); i += 2 {
		s := samples[i]
		if prev <= 0 && s > 0 {
			cross++
		}
		prev = s
	}
	secs := float64(len(samples)/2) / 32768.0
	freq := float64(cross) / secs
	rate := 16756991.0 / 512.0
	expect := rate / n
	if math.Abs(freq-expect)/expect > 0.02 {
		t.Fatalf("tone freq = %.2f Hz want %.2f (rate %.1f / %d)", freq, expect, rate, n)
	}
}

func TestDS3PanAndCaptureThroughMachine(t *testing.T) {
	m := system.New()
	v7 := m.Bus.ARM7()
	// Constant DC PCM16 on channel 0, pan full left.
	dc := make([]byte, 8*2)
	for i := 0; i < 8; i++ {
		dc[2*i] = byte(10000 & 0xFF)
		dc[2*i+1] = byte(10000 >> 8)
	}
	m.Bus.LoadBytes(false, 0x02010000, dc)
	v7.Write(0x04000404, 4, 0x02010000)
	v7.Write(0x04000408, 4, 27452)
	v7.Write(0x0400040C, 4, 4) // 8 samples looped
	v7.Write(0x04000500, 4, 0x807F)
	v7.Write(0x04000400, 4, 0x80000000|1<<27|1<<29|127) // pan 0
	// Capture 0: mixer left source, PCM16, one-shot, 512
	// samples (256 words).
	v7.Write(0x04000510, 4, 0x02018000) // DAD
	v7.Write(0x04000514, 4, 256)        // LEN words
	v7.Write(0x04000508, 1, 0x84)       // start, one-shot
	for i := 0; i < 2; i++ {
		m.RunFrame()
	}
	out := m.AudioSamples()
	if len(out) < 1024 {
		t.Fatalf("drained %d values, want at least 1024", len(out))
	}
	// Pan full left: the right channel stays at the bias
	// center, which is exactly 0 with bias 0x200.
	for i := 1; i < len(out); i += 2 {
		if out[i] != 0 {
			t.Fatalf("right sample %d = %d want 0 (pan full left)", i/2, out[i])
		}
	}
	if out[0] == 0 {
		t.Fatal("left channel silent with pan full left")
	}
	// Capture RAM holds the same left samples the drain
	// returned (the stated capture model).
	vm := m.Bus.ARM7()
	for i := 0; i < 512; i++ {
		got := int16(vm.Read(0x02018000+uint32(2*i), 2))
		if got != out[2*i] {
			t.Fatalf("captured sample %d = %d want drained %d", i, got, out[2*i])
		}
	}
}

func TestDS3Microphone(t *testing.T) {
	m := system.New()
	m.SetMicrophone([]int16{0x4000, -0x4000})
	v7 := m.Bus.ARM7()
	readADC := func() uint16 {
		v7.Write(0x040001C0, 2, 0x8200) // SPICNT: enable, touchscreen
		v7.Write(0x040001C2, 1, 0xE0)   // control: channel 6 (microphone)
		v7.Write(0x040001C2, 1, 0)
		b1 := v7.Read(0x040001C2, 1)
		v7.Write(0x040001C2, 1, 0)
		b2 := v7.Read(0x040001C2, 1)
		return uint16((b1<<8 | b2) >> 3)
	}
	if got := readADC(); got != 0xC00 {
		t.Fatalf("mic sample 1 ADC = %03x want C00", got)
	}
	if got := readADC(); got != 0x400 {
		t.Fatalf("mic sample 2 ADC = %03x want 400", got)
	}
	if got := readADC(); got != 0x800 {
		t.Fatalf("mic idle ADC = %03x want 800 (center)", got)
	}
}
