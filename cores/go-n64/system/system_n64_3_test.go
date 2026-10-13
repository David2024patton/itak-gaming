package system

import (
	"bytes"
	"math"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-n64/chips/r4300"
)

// N64-3 gates: RDP display lists (RDRAM and DMEM
// sources), VI presentation, AI audio, and mid-run
// determinism across the new devices.

func n64Cmd(op int, rest uint64) uint64 { return uint64(op)<<56 | rest }

// triWord0 builds a triangle command's first word.
func triWord0(lft bool, level, tile int, yl, ym, yh int32) uint64 {
	var w uint64
	if lft {
		w |= 1 << 55
	}
	w |= uint64(level&7) << 51
	w |= uint64(tile&7) << 48
	w |= uint64(uint32(yl)&0x3FFF) << 32
	w |= uint64(uint32(ym)&0x3FFF) << 16
	w |= uint64(uint32(yh) & 0x3FFF)
	return w
}

func edgeWord(x, dxdy int32) uint64 {
	return uint64(uint32(x))<<32 | uint64(uint32(dxdy))
}

// shadeWords builds the 8 shade coefficient words for a
// constant color plus an optional per-pixel G slope.
// Color components are 0..255 in the integer lanes.
func shadeWords(r, g, b, a int, dGdx int32) []uint64 {
	w := make([]uint64, 8)
	w[0] = uint64(r)<<48 | uint64(g)<<32 | uint64(b)<<16 | uint64(a)
	if dGdx != 0 {
		// dDx integer word: G lane holds the slope's
		// integer part (the derivative is 16.16 in units
		// of color per pixel; dGdx here is already the
		// 16.16 value with fraction 0).
		w[1] = uint64(uint32(dGdx>>16)) << 32
	}
	return w
}

// zWords builds the 2 z coefficient words for a
// constant depth given as an 18-bit value.
func zWords(z18 int32) []uint64 {
	z32 := uint32(z18) << 14
	return []uint64{uint64(z32) << 32, 0}
}

// plantTexture writes the 32x32 RGBA16 test texture at
// RDRAM 0x200000: texel (s,t) = s<<11 | t<<6 | 0x21.
func plantTexture(sys *System) {
	for t := 0; t < 32; t++ {
		for s := 0; s < 32; s++ {
			v := uint64(s<<11 | t<<6 | 0x21)
			sys.Mem.Write(uint32(0x200000+(t*32+s)*2), 2, v)
		}
	}
}

// sceneList builds the gate display list: fill the
// background, clear depth, draw a shaded z triangle,
// draw a closer magenta z triangle, draw a farther
// green z triangle over the same spot (must lose), draw
// a point-sampled textured rect, FULLSYNC.
func sceneList() []uint64 {
	img320 := uint64(0) // format RGBA
	setColor := n64Cmd(0x3F, img320<<53|uint64(2)<<51|uint64(319)<<32|0x100000)
	setZ := n64Cmd(0x3E, 0x180000)
	omFill := n64Cmd(0x2F, 3<<52)
	bg := uint64(0x2221) // RGB (32,64,128) in RGBA5551 + alpha
	fillFull := n64Cmd(0x36, uint64(319*4)<<44|uint64(239*4)<<32)
	setColorZ := n64Cmd(0x3F, uint64(2)<<51|uint64(319)<<32|0x180000)

	words := []uint64{
		setColor,
		setZ,
		omFill,
		n64Cmd(0x37, bg<<16|bg), // SETFILLCOLOR
		fillFull,                // FILLRECT background
		setColorZ,
		n64Cmd(0x37, 0xFFFFFFFF),
		fillFull, // FILLRECT depth clear
		setColor, // color image back
		n64Cmd(0x3D, uint64(2)<<51|uint64(31)<<32|0x200000), // SETTIMG
		// SETTILE tile 0: RGBA16, line 8 words, tmem 0.
		n64Cmd(0x35, uint64(2)<<51|uint64(8)<<41),
		// SETTILESIZE tile 0: 0,0 .. 31,31 (10.2).
		n64Cmd(0x32, uint64(124)<<12|124),
		// LOADBLOCK tile 0: 1024 texels, dxt 256.
		n64Cmd(0x33, uint64(1023)<<12|256),
	}
	// Triangles: 1-cycle, z compare + update, no dither.
	omTri := n64Cmd(0x2F, (3<<38)|(1<<4)|(1<<5))
	combShade := uint64(0)
	{
		// (SHADE - 0) * 0 + SHADE for RGB and alpha.
		c := combineGate(4, 16, 8, 4, 4, 7, 7, 4)
		combShade = c
	}
	words = append(words, omTri, n64Cmd(0x3C, combShade))
	// Shaded triangle (40,40) (200,40) (40,160), z 0.5.
	// G is 0 at the anchor (the H vertex at x=40) with
	// a slope of one color unit per pixel, so
	// G(x) = x - 40 across the triangle.
	{
		w := []uint64{triWord0(false, 0, 0, 640, 160, 160),
			edgeWord(40<<16, 0), edgeWord(40<<16, 0),
			edgeWord(200<<16, -87381)}
		w[0] |= uint64(0x0D) << 56
		w = append(w, shadeWords(200, 0, 50, 255, 1<<16)...)
		w = append(w, zWords(0x20000)...)
		words = append(words, w...)
	}
	// Magenta triangle (80,60) (160,60) (80,140), z 0.25.
	{
		w := []uint64{triWord0(false, 0, 0, 560, 240, 240),
			edgeWord(80<<16, 0), edgeWord(80<<16, 0),
			edgeWord(160<<16, -65536)}
		w[0] |= uint64(0x0D) << 56
		w = append(w, shadeWords(255, 0, 255, 255, 0)...)
		w = append(w, zWords(0x10000)...)
		words = append(words, w...)
	}
	// Green triangle, same geometry, z 0.75 (must lose).
	{
		w := []uint64{triWord0(false, 0, 0, 560, 240, 240),
			edgeWord(80<<16, 0), edgeWord(80<<16, 0),
			edgeWord(160<<16, -65536)}
		w[0] |= uint64(0x0D) << 56
		w = append(w, shadeWords(0, 255, 0, 255, 0)...)
		w = append(w, zWords(0x30000)...)
		words = append(words, w...)
	}
	// Textured rect (220,20)-(251,51), point sampled.
	omRect := n64Cmd(0x2F, 3<<38)
	combTex := combineGate(1, 16, 8, 1, 1, 7, 7, 1)
	words = append(words, omRect, n64Cmd(0x3C, combTex))
	words = append(words,
		n64Cmd(0x24, uint64(220*4)<<44|uint64(20*4)<<32|
			uint64(252*4)<<12|uint64(52*4)),
		uint64(1024)<<16|1024, // S=0, T=0, DsDx=DtDy=1.0 (5.10)
	)
	words = append(words, n64Cmd(0x29, 0)) // SYNCFULL
	return words
}

// combineGate builds a combine register image with the
// same muxes in both cycles (mirrors the rdp test
// helper's layout).
func combineGate(subAR, mulR, subBR, addR, subAA, mulA, subBA, addA int) uint64 {
	var w uint64
	w |= uint64(subAR) << 52
	w |= uint64(mulR) << 47
	w |= uint64(subAA) << 44
	w |= uint64(mulA) << 41
	w |= uint64(subBR) << 28
	w |= uint64(addR) << 15
	w |= uint64(subBA) << 12
	w |= uint64(addA) << 9
	w |= uint64(subAR) << 37
	w |= uint64(mulR) << 32
	w |= uint64(subAA) << 21
	w |= uint64(mulA) << 18
	w |= uint64(subBR) << 24
	w |= uint64(addR) << 6
	w |= uint64(subBA) << 3
	w |= uint64(addA)
	return w
}

// plantList writes a display list into RDRAM at base.
func plantList(sys *System, base uint32, words []uint64) {
	for i, w := range words {
		sys.Mem.Write(base+uint32(i*8), 8, w)
	}
}

// programVI points the VI at the scene's color image:
// 320x240 RGBA5551, 1:1 scales, interrupt at line 2.
func programVI(sys *System) {
	w := func(off uint32, v uint64) { sys.Mem.Write(0x04400000+off, 4, v) }
	w(0x00, 2)        // CTRL: type 16-bit
	w(0x04, 0x100000) // ORIGIN
	w(0x08, 320)      // WIDTH
	w(0x0C, 2)        // V_INTR
	w(0x18, 524)      // V_TOTAL (525 half-lines)
	w(0x24, 320)      // H_VIDEO: start 0, end 320
	w(0x28, 480)      // V_VIDEO: start 0, end 480
	w(0x30, 0x400)    // X_SCALE 1.0
	w(0x34, 0x400)    // Y_SCALE 1.0
}

// dpHandler plants the DP interrupt handler at the
// general exception vector: it increments the counter
// at RDRAM 0x310 and clears the DP interrupt through
// MI_MODE bit 11.
func dpHandler(sys *System) {
	handler := []uint32{
		lui(26, 0x8000),     // k0 = RDRAM kseg0
		lw(27, 26, 0x310),   // k1 = count
		addiu(27, 27, 1),    // count++
		sw(27, 26, 0x310),   // store
		lui(26, 0xA430),     // MI page
		addiu(27, 0, 0x800), // MI_MODE bit 11: clear DP
		sw(27, 26, 0),       // MI_MODE
		0x42000018,          // eret
	}
	for i, in := range handler {
		sys.Mem.Write(uint32(0x180+i*4), 4, uint64(in))
	}
}

// bootSceneMachine builds a machine whose CPU program
// unmasks the DP interrupt, triggers the scene list
// through the DP registers, and polls the handler
// counter at RDRAM 0x310.
func bootSceneMachine(t *testing.T) *System {
	t.Helper()
	sys, err := New(testROM())
	if err != nil {
		t.Fatal(err)
	}
	plantTexture(sys)
	words := sceneList()
	plantList(sys, 0x40000, words)
	programVI(sys)
	dpHandler(sys)
	end := 0x40000 + len(words)*8
	prog := []uint32{
		lui(s0, 0xA430),          // 0: MI page
		addiu(t0, 0, 0x800),      // 1: DP mask set bit
		sw(t0, s0, 0xC),          // 2: MI_MASK
		lui(s0, 0xA410),          // 3: DP page
		lui(t0, 0x0004),          // 4: DP_START = 0x40000
		sw(t0, s0, 0),            // 5
		lui(t0, uint16(end>>16)), // 6
		ori(t0, t0, uint16(end)), // 7: DP_END
		sw(t0, s0, 4),            // 8: execute
		lui(t2, 0x8000),          // 9
		lw(t3, t2, 0x310),        // 10: poll count
		ii(0x04, t3, 0, 0xFFFF),  // 11: beq t3, $0, 10 (off -2)
		0,                        // 12: nop
		0x0800000D,               // 13: j self
		0,                        // 14: nop
	}
	// Fix the poll branch offset: from index 11 back to
	// index 10 is -1 in branch units... branch offset is
	// relative to the next instruction: target 10 from
	// 11 gives offset -2.
	prog[11] = ii(0x04, t3, 0, 0xFFFE)
	for i, in := range prog {
		sys.Mem.Write(uint32(i*4), 4, uint64(in))
	}
	sys.CPU.CP0[r4300.Cp0Status] = r4300.StatusCU0 | r4300.StatusCU1 | 0x400 | 0x1
	sys.CPU.PC = 0xFFFFFFFF80000000
	sys.CPU.NPC = sys.CPU.PC + 4
	return sys
}

func px16(sys *System, x, y int) uint16 {
	return uint16(sys.Mem.Read(uint32(0x100000+(y*320+x)*2), 2))
}

func TestDisplayListGate(t *testing.T) {
	sys := bootSceneMachine(t)
	for i := 0; i < 20000 && sys.Mem.Read(0x310, 4) == 0; i++ {
		sys.RunCycles(10)
	}
	if got := sys.Mem.Read(0x310, 4); got != 1 {
		t.Fatalf("DP interrupt count = %d, want 1", got)
	}
	// Background corners (fill).
	if v := px16(sys, 0, 0); v != 0x2221 {
		t.Errorf("bg (0,0) = %#04x, want 0x2221", v)
	}
	if v := px16(sys, 319, 239); v != 0x2221 {
		t.Errorf("bg (319,239) = %#04x, want 0x2221", v)
	}
	// Shaded triangle interior: (60,50): R 200, G
	// 20 (x - 40 by the gradient), B 50 -> 0xC88D.
	if v := px16(sys, 60, 50); v != 0xC88D {
		t.Errorf("shaded (60,50) = %#04x, want 0xC88D", v)
	}
	// Z winner at the overlap (100,80): magenta (the
	if v := px16(sys, 100, 80); v != 0xF83F {
		t.Errorf("overlap (100,80) = %#04x, want 0xF83F (magenta)", v)
	}
	// Textured rect probes.
	if v := px16(sys, 225, 27); v != 0x29E1 {
		t.Errorf("texrect (225,27) = %#04x, want 0x29E1", v)
	}
	if v := px16(sys, 251, 51); v != 0xFFE1 {
		t.Errorf("texrect (251,51) = %#04x, want 0xFFE1", v)
	}
	if v := px16(sys, 219, 27); v != 0x2221 {
		t.Errorf("left of rect (219,27) = %#04x, want bg 0x2221", v)
	}
	// VI presentation matches at the probe points.
	fb := sys.Framebuffer()
	if len(fb) != 320*240 {
		t.Fatalf("framebuffer size %d", len(fb))
	}
	probes := []struct {
		x, y int
		want uint32
	}{
		{0, 0, 0xFF214284},     // bg (32,64,128) expanded
		{100, 80, 0xFFFF00FF},  // magenta
		{225, 27, 0xFF293984},  // texel (5,7) expanded
		{60, 50, 0xFFCE1031},   // shaded (206,16,49)
		{319, 239, 0xFF214284}, // bg corner
	}
	for _, p := range probes {
		if got := fb[p.y*320+p.x]; got != p.want {
			t.Errorf("fb (%d,%d) = %#08x, want %#08x", p.x, p.y, got, p.want)
		}
	}
}

// TestDisplayListDMEMGate runs a short list out of RSP
// DMEM: an RSP program DMAs the list into DMEM, sets the
// XBUS/DMEM bit through its CP0 DP window, and starts
// the list from there.
func TestDisplayListDMEMGate(t *testing.T) {
	sys, err := New(testROM())
	if err != nil {
		t.Fatal(err)
	}
	// Short list at RDRAM 0x40000: fill the scene
	// background color and sync.
	words := []uint64{
		n64Cmd(0x3F, uint64(2)<<51|uint64(319)<<32|0x100000),
		n64Cmd(0x2F, 3<<52),
		n64Cmd(0x37, 0x22212221),
		n64Cmd(0x36, uint64(319*4)<<44|uint64(239*4)<<32),
		n64Cmd(0x29, 0),
	}
	plantList(sys, 0x40000, words)
	dpHandler(sys)
	// RSP program at RDRAM 0x2000 (11 instructions).
	rspProg := []uint32{
		mtc0(0, 0),       // MEM_ADDR = DMEM 0
		lui(t0, 0x0004),  // DRAM_ADDR = 0x40000
		mtc0(t0, 1),      //
		addiu(t0, 0, 39), // RD_LEN: 40 bytes
		mtc0(t0, 2),      // DMA fires
		addiu(t0, 0, 2),  // DP_STATUS write: set XBUS
		mtc0(t0, 11),     //
		mtc0(0, 8),       // DP_START = 0 (DMEM)
		addiu(t0, 0, 40), //
		mtc0(t0, 9),      // DP_END = 40: execute
		0x0000000D,       // break
	}
	for i, in := range rspProg {
		sys.Mem.Write(uint32(0x2000+i*4), 4, uint64(in))
	}
	cpuProg := []uint32{
		lui(s0, 0xA404),      // SP registers
		addiu(t0, 0, 0x1000), // MEM_ADDR = IMEM
		sw(t0, s0, 0),
		addiu(t0, 0, 0x2000), // DRAM_ADDR = 0x2000
		sw(t0, s0, 4),
		addiu(t0, 0, 43), // RD_LEN: 44 bytes
		sw(t0, s0, 8),
		lui(t1, 0xA408),     // SP_PC page
		sw(0, t1, 0),        // SP_PC = 0
		lui(t1, 0xA430),     // MI page
		addiu(t0, 0, 0x800), // DP mask
		sw(t0, t1, 0xC),     // MI_MASK
		addiu(t0, 0, 1),     // clear halt only
		sw(t0, s0, 0x10),    // SP_STATUS: RSP runs
		lui(t2, 0x8000),
		lw(t3, t2, 0x310), // poll DP count
		ii(0x04, t3, 0, 0xFFFE),
		0,
		0x08000012, // j self (index 18)
		0,
	}
	for i, in := range cpuProg {
		sys.Mem.Write(uint32(i*4), 4, uint64(in))
	}
	sys.CPU.CP0[r4300.Cp0Status] = r4300.StatusCU0 | r4300.StatusCU1 | 0x400 | 0x1
	sys.CPU.PC = 0xFFFFFFFF80000000
	sys.CPU.NPC = sys.CPU.PC + 4
	for i := 0; i < 40000 && sys.Mem.Read(0x310, 4) == 0; i++ {
		sys.RunCycles(10)
	}
	if got := sys.Mem.Read(0x310, 4); got != 1 {
		t.Fatalf("DP interrupt count (DMEM list) = %d, want 1", got)
	}
	if v := px16(sys, 0, 0); v != 0x2221 {
		t.Errorf("DMEM fill (0,0) = %#04x, want 0x2221", v)
	}
	if v := px16(sys, 200, 100); v != 0x2221 {
		t.Errorf("DMEM fill (200,100) = %#04x, want 0x2221", v)
	}
}

// TestAIGate plays a 1 kHz sine through the AI with two
// queued transfers and checks the measured frequency,
// the per-start interrupt count, and the LENGTH
// readback behavior.
func TestAIGate(t *testing.T) {
	sys, err := New(testROM())
	if err != nil {
		t.Fatal(err)
	}
	// Sine: 32 frames per period, amplitude 12000.
	// 12800 frames (400 periods) per buffer.
	plant := func(base uint32) {
		for i := 0; i < 12800; i++ {
			v := int16(12000 * math.Sin(2*math.Pi*float64(i%32)/32))
			sys.Mem.Write(base+uint32(i*4), 2, uint64(uint16(v)))
			sys.Mem.Write(base+uint32(i*4+2), 2, uint64(uint16(v)))
		}
	}
	plant(0x50000)
	plant(0x60000)
	// AI interrupt handler at 0x180: count at 0x318,
	// ack by writing AI STATUS.
	handler := []uint32{
		lui(26, 0x8000),
		lw(27, 26, 0x318),
		addiu(27, 27, 1),
		sw(27, 26, 0x318),
		lui(26, 0xA450), // AI page
		sw(0, 26, 0xC),  // STATUS write: ack
		0x42000018,      // eret
	}
	for i, in := range handler {
		sys.Mem.Write(uint32(0x180+i*4), 4, uint64(in))
	}
	prog := []uint32{
		lui(s1, 0xA430),    // MI page
		addiu(t0, 0, 0x20), // AI mask set (bit 5)
		sw(t0, s1, 0xC),    // MI_MASK
		lui(s0, 0xA450),    // AI page
		addiu(t0, 0, 1520), // DACRATE (32006 Hz)
		sw(t0, s0, 0x10),
		addiu(t0, 0, 15), // BITRATE
		sw(t0, s0, 0x14),
		lui(t0, 0x0005), // DRAM_ADDR = 0x50000
		sw(t0, s0, 0),
		addiu(t0, 0, 1), // CONTROL: DMA enable
		sw(t0, s0, 8),
		ori(t0, 0, 0xC800), // LENGTH: 51200 bytes
		sw(t0, s0, 4),      // transfer 1 starts
		lui(t0, 0x0006),    // DRAM_ADDR = 0x60000
		sw(t0, s0, 0),
		ori(t0, 0, 0xC800),
		sw(t0, s0, 4), // transfer 2 queued (51200)
		0x08000012,    // j self (index 18)
		0,
	}
	for i, in := range prog {
		sys.Mem.Write(uint32(i*4), 4, uint64(in))
	}
	sys.CPU.CP0[r4300.Cp0Status] = r4300.StatusCU0 | r4300.StatusCU1 | 0x400 | 0x1
	sys.CPU.PC = 0xFFFFFFFF80000000
	sys.CPU.NPC = sys.CPU.PC + 4

	// One frame in: the CPU has programmed the AI, so
	// the rate reads back, transfer 1 is playing, and
	// the LENGTH readback decreases over time.
	sys.RunFrame()
	if got := sys.AudioRate(); got != 32006 {
		t.Fatalf("AudioRate = %d, want 32006", got)
	}
	len1 := sys.Mem.Read(0x04500004, 4)
	sys.RunFrame()
	len2 := sys.Mem.Read(0x04500004, 4)
	if !(len1 > 0 && len2 < len1 && len1 <= 51200) {
		t.Fatalf("LENGTH readback not decreasing: %d then %d", len1, len2)
	}
	// Run a full second of emulated time (60 frames
	// total), draining audio as it comes.
	var samples []int16
	samples = append(samples, sys.AudioBuffer()...)
	for i := 0; i < 58; i++ {
		sys.RunFrame()
		samples = append(samples, sys.AudioBuffer()...)
	}
	frames := len(samples) / 2
	if frames < 24000 {
		t.Fatalf("only %d stereo frames from two 51200-byte transfers", frames)
	}
	// Positive-going zero crossings on the left channel.
	crossings := 0
	for i := 1; i < frames; i++ {
		if samples[2*(i-1)] < 0 && samples[2*i] >= 0 {
			crossings++
		}
	}
	seconds := float64(frames) / float64(sys.AudioRate())
	freq := float64(crossings) / seconds
	if freq < 970 || freq > 1030 {
		t.Fatalf("measured sine = %.1f Hz, want about 1000", freq)
	}
	// Both transfers started: exactly 2 AI interrupts.
	if got := sys.Mem.Read(0x318, 4); got != 2 {
		t.Fatalf("AI interrupt count = %d, want 2", got)
	}
}

// TestN64_3Determinism runs the scene and audio, saves
// mid-run, restores into a fresh machine, and requires
// byte-identical continuation.
func TestN64_3Determinism(t *testing.T) {
	setup := func() *System {
		sys := bootTestMachine(t)
		plantTexture(sys)
		plantList(sys, 0x40000, sceneList())
		programVI(sys)
		// Execute the list through the bus DP page.
		sys.Mem.Write(0x04100000, 4, 0x40000)
		sys.Mem.Write(0x04100004, 4, 0x40000+uint64(len(sceneList())*8))
		// AI: a long sine transfer.
		for i := 0; i < 3200; i++ {
			v := int16(12000 * math.Sin(2*math.Pi*float64(i%32)/32))
			sys.Mem.Write(uint32(0x50000+i*4), 2, uint64(uint16(v)))
			sys.Mem.Write(uint32(0x50000+i*4+2), 2, uint64(uint16(v)))
		}
		sys.Mem.Write(0x04500010, 4, 1520)    // DACRATE
		sys.Mem.Write(0x04500014, 4, 15)      // BITRATE
		sys.Mem.Write(0x04500000, 4, 0x50000) // DRAM_ADDR
		sys.Mem.Write(0x04500008, 4, 1)       // CONTROL
		sys.Mem.Write(0x04500004, 4, 12800)   // LENGTH
		return sys
	}
	a := setup()
	a.RunCycles(200000)
	_ = a.AudioBuffer() // drain pre-save output
	state := a.Serialize()
	a.RunCycles(100000)
	audioA := a.AudioBuffer()
	stateA := a.Serialize()
	fbA := append([]uint32(nil), a.Framebuffer()...)

	b := setup()
	if err := b.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	b.RunCycles(100000)
	audioB := b.AudioBuffer()
	stateB := b.Serialize()
	fbB := append([]uint32(nil), b.Framebuffer()...)

	if !bytes.Equal(stateA, stateB) {
		t.Error("states diverged after N64-3 restore")
	}
	if !bytes.Equal(int16s(audioA), int16s(audioB)) {
		t.Error("audio diverged after N64-3 restore")
	}
	if len(audioA) == 0 {
		t.Error("no audio produced in the continuation window")
	}
	if !bytes.Equal(uint32s(fbA), uint32s(fbB)) {
		t.Error("framebuffers diverged after N64-3 restore")
	}
	if fbA[80*320+100] != 0xFFFF00FF {
		t.Errorf("restored framebuffer probe = %#08x, want magenta", fbA[80*320+100])
	}
}

func int16s(v []int16) []byte {
	out := make([]byte, 2*len(v))
	for i, s := range v {
		out[2*i] = byte(uint16(s) >> 8)
		out[2*i+1] = byte(uint16(s))
	}
	return out
}

func uint32s(v []uint32) []byte {
	out := make([]byte, 4*len(v))
	for i, s := range v {
		out[4*i] = byte(s >> 24)
		out[4*i+1] = byte(s >> 16)
		out[4*i+2] = byte(s >> 8)
		out[4*i+3] = byte(s)
	}
	return out
}
