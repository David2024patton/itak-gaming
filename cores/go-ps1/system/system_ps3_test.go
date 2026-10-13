package system

// PS1-3 system gates: a full PS-X EXE enginetest that
// drives the BIOS event system, the GPU textured quad
// path, SPU ADPCM playback through DMA, a CD sector
// read through DMA with a checksum, and GTE RTPS
// projection, all from one guest program, plus a
// determinism leg across Serialize and Deserialize,
// and focused BIOS HLE unit tests.
//
// Deviations from the original slice plan, each one
// resolved in favor of the code as built (the plan
// itself instructed this):
//
//   - DMA channel enable bits in DPCR are bit 4*n+3
//     per dma.Controller.channelEnabled, so channel 4
//     (SPU) is 0x80000, not 0x10000. Channel 3 (CD) is
//     0x8000 as planned.
//   - DMA CHCR direction: dma.transferWord treats
//     CHCR bit 0 set as RAM to device, so the SPU
//     upload uses CHCR 0x01000001. The CD channel
//     moves device to RAM and uses 0x01000000 as
//     planned.
//   - The RTPS CPU word is 0x4A080001, not the draft
//     0x4A000001: chips/r3000a execCOP2 dispatches a
//     GTE command when the rs field is 0x10 or more
//     and passes the whole instruction word to the
//     GTE, whose opcode is bits 0 to 5 and whose sf
//     bit is bit 19. The draft word has sf clear,
//     which would shift the Z result; 0x4A080001 has
//     sf set, matching the documented RTPS word form
//     the gte package tests use (0x0180001 as a bare
//     command).
//   - The textured quad second vertex UV carries
//     u=16, not u=0: vertex 1 is the top right corner
//     (96,32) of a quad mapping a 16 texel wide
//     texture, and the framebuffer probes below are
//     only satisfiable with u=16 there. The texpage
//     halfword is 0x0110, matching
//     gpu.texPageFromWord: x base 0, y base bit at
//     bit 4 (VRAM y 256), color format bits 7 to 8
//     equal 2 (15-bit direct).
//   - The GP1 display enable word is written after
//     the textured quad, not before it: gpu.plot
//     suppresses any drawing into the display area
//     while the display is enabled and the E1
//     draw-to-display bit is clear, and the quad
//     covers display pixels by design. All planned
//     GP1 words are still written, in stage B.
//   - Stage markers share one word at result+0x100,
//     written with the stage number as each stage
//     completes (the plan's simpler option), because
//     per-stage slots at result+0x100+4*stage would
//     collide with the CD checksum at +0x104 and the
//     GTE results at +0x108 onward.

import (
	"bytes"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-ps1/homebrew"
)

// ----------------------------------------------------------------
// The enginetest gate.
// ----------------------------------------------------------------

func ps3SX(w uint32) int { return int(int16(w & 0xFFFF)) }
func ps3SY(w uint32) int { return int(int16(w >> 16)) }

func TestPS1Enginetest(t *testing.T) {
	tone := homebrew.EncodeLoop(homebrew.SinePCM(2800, 28, 12000))
	if len(tone) != 1600 {
		t.Fatalf("tone size actual %d bytes, want 1600 (100 ADPCM blocks)", len(tone))
	}
	exe := homebrew.BuildEXE(homebrew.EngineWords(tone))

	m := New()
	if err := m.LoadDisc(&seqDisc{sectors: 16}); err != nil {
		t.Fatalf("LoadDisc: %v", err)
	}
	if err := m.LoadEXE(exe); err != nil {
		t.Fatalf("LoadEXE: %v", err)
	}
	var audio []int16
	passed := false
	for chunk := 0; chunk < 40 && !passed; chunk++ {
		m.RunCycles(1000000)
		audio = append(audio, m.AudioBuffer()...)
		if m.Bus.Read32(0x80020000) == 0x600DF00D {
			passed = true
		}
	}
	if !passed {
		t.Fatalf("PASS magic not reached: stage marker actual %d, want 6; PASS word actual 0x%08X, want 0x600DF00D; unimplemented BIOS calls actual %d, want 0",
			m.Bus.Read32(0x80020100), m.Bus.Read32(0x80020000), m.UnimplementedBIOSCallCount())
	}
	// Keep the tone playing so the audio analysis
	// below has well over a second of output: the
	// looped voice keeps sounding while the guest
	// spins on its PASS loop.
	for i := 0; i < 30; i++ {
		m.RunCycles(1000000)
		audio = append(audio, m.AudioBuffer()...)
	}
	if got := m.Bus.Read32(0x80020100); got != 6 {
		t.Errorf("final stage marker actual %d, want 6", got)
	}
	if got := m.UnimplementedBIOSCallCount(); got != 0 {
		t.Errorf("unimplemented BIOS calls actual %d, want 0 (log %v)", got, m.UnimplementedBIOSCalls())
	}

	// GPU probes: one framebuffer pixel per texture
	// quadrant.
	if w, h := m.DisplayWidth(), m.DisplayHeight(); w != 320 || h != 240 {
		t.Errorf("display size actual %dx%d, want 320x240", w, h)
	}
	fb := m.Framebuffer()
	fw := m.DisplayWidth()
	for _, p := range []struct {
		x, y int
		want uint32
	}{
		{48, 48, 0xFFFF0000},
		{80, 48, 0xFF00FF00},
		{48, 80, 0xFF0000FF},
		{80, 80, 0xFFFFFFFF},
	} {
		if got := fb[p.y*fw+p.x]; got != p.want {
			t.Errorf("framebuffer pixel (%d,%d) actual 0x%08X, want 0x%08X", p.x, p.y, got, p.want)
		}
	}

	// CD checksum: the expected sum comes straight
	// from the seqDisc sector 0 pattern, byte i
	// equal to byte(i>>2), packed little endian.
	var wantSum uint32
	for i := 0; i < 2048; i += 4 {
		var w uint32
		for k := 0; k < 4; k++ {
			w |= uint32(byte((i+k)>>2)) << (8 * k)
		}
		wantSum += w
	}
	gotSum := m.Bus.Read32(0x80020104)
	if gotSum != wantSum {
		t.Errorf("CD sector checksum actual 0x%08X, want 0x%08X", gotSum, wantSum)
	}

	// GTE results.
	sxyp1 := m.Bus.Read32(0x80020108)
	if ps3SX(sxyp1) != 160 || ps3SY(sxyp1) != 120 {
		t.Errorf("RTPS centered point actual (%d,%d), want (160,120) exactly (SXYP word 0x%08X)",
			ps3SX(sxyp1), ps3SY(sxyp1), sxyp1)
	}
	if got := m.Bus.Read32(0x8002010C); got != 300 {
		t.Errorf("RTPS SZ3 actual %d, want 300", got)
	}
	if flag := m.Bus.Read32(0x80020110); flag&(1<<31) != 0 {
		t.Errorf("RTPS FLAG actual 0x%08X, want bit 31 clear", flag)
	}
	sxyp2 := m.Bus.Read32(0x80020114)
	if sx, sy := ps3SX(sxyp2), ps3SY(sxyp2); sx < 223 || sx > 229 || sy < 84 || sy > 90 {
		t.Errorf("RTPS second point actual (%d,%d), want SX in [223,229] and SY in [84,90] (SXYP word 0x%08X)", sx, sy, sxyp2)
	}

	// Audio: skip the first 0.25 s, count positive
	// zero crossings on the left channel over the
	// next 0.5 s.
	pairs := len(audio) / 2
	if pairs < 11025+22050 {
		t.Fatalf("audio length actual %d sample pairs, want at least %d for the analysis window", pairs, 11025+22050)
	}
	crossings := 0
	prev := int16(0)
	for i := 0; i < 22050; i++ {
		cur := audio[2*(11025+i)]
		if i > 0 && prev < 0 && cur >= 0 {
			crossings++
		}
		prev = cur
	}
	freq := float64(crossings) / 0.5
	t.Logf("enginetest: stage marker 6, CD checksum 0x%08X, SXYP1 (%d,%d), SXYP2 (%d,%d), audio %d crossings in 0.5 s = %.1f Hz",
		gotSum, ps3SX(sxyp1), ps3SY(sxyp1), ps3SX(sxyp2), ps3SY(sxyp2), crossings, freq)
	if freq < 415 || freq > 465 {
		t.Errorf("audio frequency actual %.1f Hz, want in [415,465]", freq)
	}

	// Determinism leg: a fixed cycle run, a mid
	// Serialize, then identical continuations on the
	// original machine and on a fresh machine that
	// only Deserializes the mid state. Pending audio
	// is drained and discarded on both sides of the
	// restore point, because the serialized SPU audio
	// buffer is capped and the original machine would
	// otherwise carry a longer pending tail.
	m2 := New()
	if err := m2.LoadDisc(&seqDisc{sectors: 16}); err != nil {
		t.Fatalf("determinism LoadDisc: %v", err)
	}
	if err := m2.LoadEXE(exe); err != nil {
		t.Fatalf("determinism LoadEXE: %v", err)
	}
	m2.RunCycles(12000000)
	mid := m2.Serialize()
	_ = m2.AudioBuffer() // discard pending, see above
	m2.RunCycles(2000000)
	audio2 := m2.AudioBuffer()
	wantState := m2.Serialize()
	wantFB := append([]uint32(nil), m2.Framebuffer()...)
	wantRes := make([]uint32, 128)
	for i := range wantRes {
		wantRes[i] = m2.Bus.Read32(0x80020000 + uint32(4*i))
	}
	if wantRes[0] != 0x600DF00D {
		t.Fatalf("determinism leg: m2 PASS word actual 0x%08X, want 0x600DF00D after 14M cycles (stage marker %d)",
			wantRes[0], m2.Bus.Read32(0x80020100))
	}

	m3 := New()
	if err := m3.LoadDisc(&seqDisc{sectors: 16}); err != nil {
		t.Fatalf("determinism LoadDisc m3: %v", err)
	}
	if err := m3.Deserialize(mid); err != nil {
		t.Fatalf("determinism Deserialize: %v", err)
	}
	_ = m3.AudioBuffer() // discard restored pending
	m3.RunCycles(2000000)
	audio3 := m3.AudioBuffer()
	if len(audio3) != len(audio2) {
		t.Fatalf("determinism audio length actual %d, want %d", len(audio3), len(audio2))
	}
	for i := range audio2 {
		if audio3[i] != audio2[i] {
			t.Fatalf("determinism audio sample %d actual %d, want %d", i, audio3[i], audio2[i])
		}
	}
	if got := m3.Serialize(); !bytes.Equal(got, wantState) {
		t.Errorf("determinism state after identical continuation diverged: actual %d bytes vs want %d bytes, byte equal false", len(got), len(wantState))
		for i := range wantState {
			if got[i] != wantState[i] {
				t.Logf("first divergent state byte at offset %d: actual 0x%02X, want 0x%02X", i, got[i], wantState[i])
				break
			}
		}
	}
	gotFB := m3.Framebuffer()
	if len(gotFB) != len(wantFB) {
		t.Fatalf("determinism framebuffer size actual %d, want %d", len(gotFB), len(wantFB))
	}
	for i := range wantFB {
		if gotFB[i] != wantFB[i] {
			t.Fatalf("determinism framebuffer pixel %d actual 0x%08X, want 0x%08X", i, gotFB[i], wantFB[i])
		}
	}
	for i := range wantRes {
		if got := m3.Bus.Read32(0x80020000 + uint32(4*i)); got != wantRes[i] {
			t.Fatalf("determinism result word %d actual 0x%08X, want 0x%08X", i, got, wantRes[i])
		}
	}
	t.Logf("determinism: state bytes equal %v, audio samples after restore %d, framebuffer pixels %d",
		bytes.Equal(m3.Serialize(), wantState), len(audio2), len(wantFB))
}

// ----------------------------------------------------------------
// BIOS HLE unit tests.
// ----------------------------------------------------------------

func TestBIOSUnimplementedLogged(t *testing.T) {
	a := homebrew.NewAsm()
	a.Li(homebrew.A0, 0)
	a.Li(homebrew.A1, 0)
	a.Li(homebrew.A2, 0)
	a.Li(homebrew.A3, 0)
	a.CallBIOS(0xA0, 0x7F) // unimplemented A function
	a.Lui(homebrew.T0, 0x8002)
	a.Sw(homebrew.V0, 0x210, homebrew.T0)
	a.Spin("spin")
	m := New()
	loadProg(m, 0x80010000, a.Build(0x80010000))
	setPC(m, 0x80010000)
	m.RunCycles(10000)
	if got := m.Bus.Read32(0x80020210); got != 0xFFFFFFFF {
		t.Errorf("unimplemented call return actual 0x%08X, want 0xFFFFFFFF", got)
	}
	if got := m.UnimplementedBIOSCallCount(); got < 1 {
		t.Fatalf("unimplemented call count actual %d, want at least 1", got)
	}
	calls := m.UnimplementedBIOSCalls()
	if len(calls) < 1 {
		t.Fatalf("unimplemented call log length actual %d, want at least 1", len(calls))
	}
	if calls[0].Table != 'A' || calls[0].Func != 0x7F {
		t.Errorf("unimplemented call log entry actual {%c, 0x%02X}, want {A, 0x7F}", calls[0].Table, calls[0].Func)
	}
}

func TestBIOSCallbackEvent(t *testing.T) {
	a := homebrew.NewAsm()
	a.Li(homebrew.A0, 0xF0000001)
	a.Li(homebrew.A1, 0x1000)
	a.Li(homebrew.A2, 0x1000) // callback mode
	a.LiLabel(homebrew.A3, "callback")
	a.CallBIOS(0xB0, 0x08) // OpenEvent
	a.Addu(homebrew.S1, homebrew.V0, homebrew.Zero)
	a.Addu(homebrew.A0, homebrew.S1, homebrew.Zero)
	a.CallBIOS(0xB0, 0x0C)        // EnableEvent
	a.StoreWordAbs(0x1F801074, 1) // I_MASK = VBlank
	a.Label("spin")
	a.Lui(homebrew.T2, 0x8002)
	a.Lw(homebrew.T0, 0x204, homebrew.T2)
	a.Nop() // load delay
	a.Addiu(homebrew.T0, homebrew.T0, 1)
	a.Sw(homebrew.T0, 0x204, homebrew.T2)
	a.Beq(homebrew.Zero, homebrew.Zero, "spin")
	a.Nop()
	for a.Len() < 64 {
		a.Nop()
	}
	a.Label("callback") // at 0x80010100
	a.Lui(homebrew.T0, 0x8002)
	a.Lw(homebrew.T1, 0x200, homebrew.T0)
	a.Nop() // load delay
	a.Addiu(homebrew.T1, homebrew.T1, 1)
	a.Sw(homebrew.T1, 0x200, homebrew.T0)
	a.Jr(homebrew.RA)
	a.Nop()
	m := New()
	loadProg(m, 0x80010000, a.Build(0x80010000))
	setPC(m, 0x80010000)
	for i := 0; i < 3; i++ {
		m.RunFrame()
	}
	got := m.Bus.Read32(0x80020200)
	t.Logf("callback event: counter %d after 3 frames", got)
	if got < 2 {
		t.Errorf("callback counter actual %d, want at least 2 after 3 frames", got)
	}
	h1 := m.Bus.Read32(0x80020204)
	m.RunCycles(100000)
	h2 := m.Bus.Read32(0x80020204)
	if h2 == h1 {
		t.Errorf("main spin heartbeat actual %d after more cycles, want a value different from %d (spin still alive)", h2, h1)
	}
}

func TestBIOSSetRCntRoundTrip(t *testing.T) {
	a := homebrew.NewAsm()
	a.Li(homebrew.A0, 0)
	a.Li(homebrew.A1, 1234)
	a.Li(homebrew.A2, 0x0058)
	a.CallBIOS(0xC0, 0x19) // SetRCnt(0, 1234, 0x0058)
	a.Li(homebrew.A0, 0)
	a.CallBIOS(0xC0, 0x1A) // GetRCnt(0)
	a.Lui(homebrew.T0, 0x8002)
	a.Sw(homebrew.V0, 0x220, homebrew.T0)
	a.Li(homebrew.A0, 0)
	a.CallBIOS(0xC0, 0x1B) // ResetRCnt(0)
	a.Li(homebrew.A0, 0)
	a.CallBIOS(0xC0, 0x1A) // GetRCnt(0)
	a.Lui(homebrew.T0, 0x8002)
	a.Sw(homebrew.V0, 0x224, homebrew.T0)
	a.Spin("spin")
	m := New()
	loadProg(m, 0x80010000, a.Build(0x80010000))
	setPC(m, 0x80010000)
	m.RunCycles(10000)
	if got := m.Timers.T[0].Target; got != 1234 {
		t.Errorf("timer 0 target actual %d, want 1234", got)
	}
	if got := m.Timers.T[0].Mode; got != 0x0058 {
		t.Errorf("timer 0 mode actual 0x%04X, want 0x0058", got)
	}
	first := m.Bus.Read32(0x80020220)
	second := m.Bus.Read32(0x80020224)
	t.Logf("SetRCnt round trip: first GetRCnt %d, second GetRCnt after reset %d", first, second)
	if first > 1234 {
		t.Errorf("first GetRCnt actual %d, want at most 1234", first)
	}
	if !(second < first || second < 100) {
		t.Errorf("second GetRCnt actual %d, want less than first (%d) or less than 100", second, first)
	}
}

func TestBIOSInitPAD2Fill(t *testing.T) {
	a := homebrew.NewAsm()
	a.Li(homebrew.A0, 0x80020300)
	a.Li(homebrew.A1, 0x22)
	a.Li(homebrew.A2, 0x80020340)
	a.Li(homebrew.A3, 0x22)
	a.CallBIOS(0xB0, 0x12) // InitPAD2
	a.CallBIOS(0xB0, 0x13) // StartPAD2
	a.Spin("spin")
	m := New()
	loadProg(m, 0x80010000, a.Build(0x80010000))
	setPC(m, 0x80010000)
	m.SetButtons(ButtonStart | ButtonCross)
	m.RunFrame()
	m.RunFrame()
	for _, c := range []struct {
		addr uint32
		want uint8
	}{
		{0x80020300, 0x00}, // port 1 status okay
		{0x80020301, 0x41}, // digital pad ID
		{0x80020302, 0xF7}, // low buttons: Start is bit 3, byte = ^0x08
		{0x80020303, 0xBF}, // high buttons: Cross is bit 14, byte = ^0x40
		{0x80020340, 0xFF}, // port 2: no device
	} {
		if got := m.Bus.Read8(c.addr); got != c.want {
			t.Errorf("PAD buffer byte at 0x%08X actual 0x%02X, want 0x%02X", c.addr, got, c.want)
		}
	}
}
