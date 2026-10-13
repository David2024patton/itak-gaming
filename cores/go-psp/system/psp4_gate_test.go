package system

import (
	"math"
	"testing"
)

// PSP-4 synthetic gates for sceAudio: hand assembled guests in
// the PSP-3 gate builder style, booted through LoadExecutable,
// with the host measuring the drained 44,100 Hz stereo stream
// sample by sample.
//
// The tone fixture is a 100 frame cosine table (441 Hz at the
// output rate), amplitude 12000, generated here in the test;
// the guests copy it into their play buffers with a running
// phase so the waveform is continuous across buffer joins.

const (
	audioGateBufFrames = 1024
	audioGateTableLen  = 100
	audioGateAmp       = 12000
)

func audioGateTable() []int16 {
	tab := make([]int16, audioGateTableLen)
	for i := range tab {
		tab[i] = int16(math.Round(audioGateAmp * math.Cos(2*math.Pi*float64(i)/audioGateTableLen)))
	}
	return tab
}

func audioGateTableBytes() []byte {
	tab := audioGateTable()
	out := make([]byte, 2*len(tab))
	for i, v := range tab {
		out[2*i] = byte(v)
		out[2*i+1] = byte(v >> 8)
	}
	return out
}

// Extra register numbers the gate assembler leaves out.
const (
	rT6 = 14
	rT7 = 15
)

// Extra assembler forms the audio gates need (the PSP-3
// assembler lives in psp3_gate_test.go, same package).
func (a *gateAsm) mfhi(rd int) { a.emit(rtype(0, 0, rd, 0, 0x10)) }
func (a *gateAsm) xori(rt, rs, imm int) {
	a.emit(itype(0x0E, rs, rt, imm))
}
func (a *gateAsm) lh(rt, off, rs int) {
	a.emit(itype(0x21, rs, rt, off))
}
func (a *gateAsm) sh(rt, off, rs int) {
	a.emit(itype(0x29, rs, rt, off))
}

// sceAudio stubs for the gates, in call order.
var libAudio4 = gateLib{"sceAudio", []uint32{
	0x5EC81C55, // 0 sceAudioChReserve
	0x136CAF51, // 1 sceAudioOutputBlocking
	0xE9D97901, // 2 sceAudioGetChannelRestLen
	0x6FC46853, // 3 sceAudioChRelease
}}

// audioFillAsm emits the loop that fills the stereo play buffer
// at bufLab from the cosine table at tabLab, continuing the
// phase counter in register phaseReg (kept across calls by the
// caller). Clobbers t0..t7 and the phase register advances by
// one buffer.
func audioFillAsm(a *gateAsm, tabLab, bufLab string, phaseReg int, tag string) {
	a.la(rS7, bufLab)
	audioFillRegAsm(a, tabLab, rS7, phaseReg, tag)
}

// audioFillRegAsm is audioFillAsm with the buffer address in a
// register (double buffering guests select the target first).
func audioFillRegAsm(a *gateAsm, tabLab string, bufReg, phaseReg int, tag string) {
	a.la(rS6, tabLab)
	a.li(rT0, 0)
	a.here("fill" + tag)
	a.addu(rT2, phaseReg, rT0)
	a.li(rT3, audioGateTableLen)
	a.divu(rT2, rT3)
	a.mfhi(rT4) // table index
	a.sll(rT5, rT4, 1)
	a.addu(rT5, rS6, rT5)
	a.lh(rT6, 0, rT5)
	a.sll(rT7, rT0, 2)
	a.addu(rT7, bufReg, rT7)
	a.sh(rT6, 0, rT7)
	a.sh(rT6, 2, rT7)
	a.addiu(rT0, rT0, 1)
	a.li(rT1, audioGateBufFrames)
	a.bne(rT0, rT1, "fill"+tag)
	a.nop()
	a.addiu(phaseReg, phaseReg, audioGateBufFrames)
}

// audioWaitDrainAsm emits a poll loop: sceAudioGetChannelRestLen
// on the channel in chReg until it reads 0.
func audioWaitDrainAsm(a *gateAsm, chReg int, tag string) {
	a.here("wait" + tag)
	a.move(rA0, chReg)
	a.jalStub(0, 2) // sceAudioGetChannelRestLen
	a.nop()
	a.bne(rV0, rZero, "wait"+tag)
	a.nop()
}

// runAudioGate boots img and runs until the guest exits,
// draining the audio stream after every frame. It returns the
// machine and the full drained stream.
func runAudioGate(t *testing.T, m *Machine, maxFrames int) []int16 {
	t.Helper()
	var stream []int16
	for f := 0; f < maxFrames; f++ {
		m.RunFrame()
		stream = append(stream, m.AudioBuffer()...)
		if m.Exited() {
			return stream
		}
	}
	t.Fatalf("guest did not exit within %d frames; console %q", maxFrames, m.ConsoleOutput())
	return stream
}

// --- gate (a): one channel, blocking sine, exact stream --------

func buildGateA() *gateBuilder {
	b := newGateBuilder("GATEA", []gateLib{libAudio4, libSysMem, libLoadExec})
	b.ro("tabA", audioGateTableBytes())
	// Two play buffers, alternated: the mixer reads samples
	// live from guest memory (the documented model), so the
	// buffer being filled must never be the one playing.
	b.ro("pbufA0", make([]byte, audioGateBufFrames*4))
	b.ro("pbufA1", make([]byte, audioGateBufFrames*4))
	b.roWords("cellChA", 0)
	b.roWords("cellCallsA", 0)
	b.roStr("fmtA", "ch=%d calls=%d\n")
	a := b.code
	a.addiu(rSP, rSP, -32)
	a.addiu(rA0, rZero, -1) // first free channel
	a.li(rA1, audioGateBufFrames)
	a.li(rA2, 0)    // stereo
	a.jalStub(0, 0) // sceAudioChReserve
	a.nop()
	a.move(rS0, rV0)
	a.la(rT0, "cellChA")
	a.sw(rS0, 0, rT0)
	a.li(rS3, 0) // calls done
	a.li(rS4, 0) // phase
	a.li(rS5, 0) // buffer parity
	a.here("playA")
	a.la(rS2, "pbufA0")
	a.beq(rS5, rZero, "bufAReady")
	a.nop()
	a.la(rS2, "pbufA1")
	a.here("bufAReady")
	audioFillRegAsm(a, "tabA", rS2, rS4, "A")
	a.move(rA0, rS0)
	a.li(rA1, 0x8000) // unity volume
	a.move(rA2, rS2)
	a.jalStub(0, 1) // sceAudioOutputBlocking
	a.nop()
	a.xori(rS5, rS5, 1)
	a.addiu(rS3, rS3, 1)
	a.la(rT0, "cellCallsA")
	a.sw(rS3, 0, rT0)
	a.li(rT1, 40)
	a.bne(rS3, rT1, "playA")
	a.nop()
	audioWaitDrainAsm(a, rS0, "A")
	a.move(rA0, rS0)
	a.jalStub(0, 3) // sceAudioChRelease
	a.nop()
	a.la(rA0, "fmtA")
	a.move(rA1, rS0)
	a.move(rA2, rS3)
	a.jalStub(1, 0) // sceKernelPrintf
	a.nop()
	a.li(rA0, 0)
	a.jalStub(2, 0) // sceKernelExitGame
	a.nop()
	return b
}

// checkToneSpan asserts the stream is silent everywhere except
// a contiguous span of wantFrames frames starting at the first
// nonzero frame, whose samples reproduce the cosine table
// exactly (unity volume), and returns the measured frequency
// from rising zero crossings inside the span.
func checkToneSpan(t *testing.T, stream []int16, wantFrames int) float64 {
	t.Helper()
	tab := audioGateTable()
	frames := len(stream) / 2
	start := -1
	for i := 0; i < frames; i++ {
		if stream[2*i] != 0 || stream[2*i+1] != 0 {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("stream is entirely silent")
	}
	for i := 0; i < frames; i++ {
		l, r := stream[2*i], stream[2*i+1]
		if i >= start && i < start+wantFrames {
			want := tab[(i-start)%audioGateTableLen]
			if l != want || r != want {
				t.Fatalf("frame %d: got (%d, %d), want table value %d", i, l, r, want)
			}
		} else if l != 0 || r != 0 {
			t.Fatalf("frame %d outside the tone span is (%d, %d), want silence", i, l, r)
		}
	}
	crossings := 0
	prev := int16(0)
	for j := 0; j < wantFrames; j++ {
		v := stream[2*(start+j)]
		if prev < 0 && v >= 0 {
			crossings++
		}
		prev = v
	}
	return float64(crossings) * audioGateSampleRate / float64(wantFrames)
}

const audioGateSampleRate = 44100.0

func TestPSP4GateAudioSine(t *testing.T) {
	b := buildGateA()
	img := b.build(false, nil)
	_, _, roAddr, _, _ := b.layout(false)

	m := New()
	if err := m.LoadExecutable(img); err != nil {
		t.Fatalf("LoadExecutable: %v", err)
	}
	stream := runAudioGate(t, m, 400)

	if got := m.Bus.Read32(roAddr["cellChA"]); got != 7 {
		t.Fatalf("reserved channel = %d, want 7 (free search runs downward)", got)
	}
	if got := m.Bus.Read32(roAddr["cellCallsA"]); got != 40 {
		t.Fatalf("completed output calls = %d, want 40", got)
	}
	if want := "ch=7 calls=40\n"; m.ConsoleOutput() != want {
		t.Fatalf("console = %q, want %q", m.ConsoleOutput(), want)
	}
	freq := checkToneSpan(t, stream, 40*audioGateBufFrames)
	if freq < 441*0.98 || freq > 441*1.02 {
		t.Fatalf("measured tone frequency %.2f Hz, want 441 within 2 percent", freq)
	}
	t.Logf("tone: %d frames exact, measured %.2f Hz", 40*audioGateBufFrames, freq)
}

// TestPSP4GateAudioSaveRestore reruns gate (a) with a save
// taken mid-playback (a buffer in flight, the guest parked or
// filling): the continuation must be byte identical to the
// uninterrupted run, stream included.
func TestPSP4GateAudioSaveRestore(t *testing.T) {
	b := buildGateA()
	img := b.build(false, nil)
	_, _, roAddr, _, _ := b.layout(false)
	callsAddr := roAddr["cellCallsA"]

	ref := New()
	if err := ref.LoadExecutable(img); err != nil {
		t.Fatalf("LoadExecutable: %v", err)
	}
	refStream := runAudioGate(t, ref, 400)
	refState := ref.Serialize()

	m := New()
	if err := m.LoadExecutable(img); err != nil {
		t.Fatalf("LoadExecutable: %v", err)
	}
	var stream []int16
	var saved []byte
	savedAt := -1
	for f := 0; f < 400 && !m.Exited(); f++ {
		m.RunFrame()
		stream = append(stream, m.AudioBuffer()...)
		if saved == nil && m.Bus.Read32(callsAddr) >= 20 {
			saved = m.Serialize()
			savedAt = len(stream)
		}
	}
	if saved == nil {
		t.Fatal("guest never reached 20 calls; no mid-playback save taken")
	}
	if err := m.Deserialize(saved); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	stream = stream[:savedAt]
	for f := 0; f < 400 && !m.Exited(); f++ {
		m.RunFrame()
		stream = append(stream, m.AudioBuffer()...)
	}
	if !m.Exited() {
		t.Fatal("restored guest did not exit")
	}
	if len(stream) != len(refStream) {
		t.Fatalf("stream length %d, want %d", len(stream), len(refStream))
	}
	for i := range stream {
		if stream[i] != refStream[i] {
			t.Fatalf("stream sample %d = %d, want %d (reference run)", i, stream[i], refStream[i])
		}
	}
	if got := m.Serialize(); !equalBytes(got, refState) {
		t.Fatal("final state differs from the uninterrupted run")
	}
	if got := m.ConsoleOutput(); got != ref.ConsoleOutput() {
		t.Fatalf("console %q, want %q", got, ref.ConsoleOutput())
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- gate (b): two channels mixed, exact sums and clamping -----

func buildMixGate() *gateBuilder {
	b := newGateBuilder("GATEB", []gateLib{libAudio4, libSysMem, libLoadExec})
	b.ro("tabB", audioGateTableBytes())
	b.ro("bufBA", make([]byte, audioGateBufFrames*4))
	b.ro("bufBB", make([]byte, audioGateBufFrames*4))
	b.roWords("cellChBA", 0)
	b.roWords("cellChBB", 0)
	b.roStr("fmtB", "mix a=%d b=%d\n")
	a := b.code
	a.addiu(rSP, rSP, -32)
	// Reserve two channels.
	for _, cell := range []string{"cellChBA", "cellChBB"} {
		a.addiu(rA0, rZero, -1)
		a.li(rA1, audioGateBufFrames)
		a.li(rA2, 0)
		a.jalStub(0, 0) // sceAudioChReserve
		a.nop()
		a.la(rT0, cell)
		a.sw(rV0, 0, rT0)
	}
	a.la(rT0, "cellChBA")
	a.lw(rS0, 0, rT0)
	a.la(rT0, "cellChBB")
	a.lw(rS1, 0, rT0)
	// Fill both buffers with the same sine phase.
	a.li(rS4, 0)
	audioFillAsm(a, "tabB", "bufBA", rS4, "BA")
	a.li(rS4, 0)
	audioFillAsm(a, "tabB", "bufBB", rS4, "BB")
	// 10 buffer pairs at unity volume.
	a.li(rS3, 0)
	a.here("pairB")
	a.move(rA0, rS0)
	a.li(rA1, 0x8000)
	a.la(rA2, "bufBA")
	a.jalStub(0, 1)
	a.nop()
	a.move(rA0, rS1)
	a.li(rA1, 0x8000)
	a.la(rA2, "bufBB")
	a.jalStub(0, 1)
	a.nop()
	a.addiu(rS3, rS3, 1)
	a.li(rT1, 10)
	a.bne(rS3, rT1, "pairB")
	a.nop()
	// The mixer reads the buffers live, so a phase change must
	// wait for the drain before refilling them.
	audioWaitDrainAsm(a, rS0, "BX")
	audioWaitDrainAsm(a, rS1, "BY")
	// Fill both buffers with a +30000 constant, 2 pairs (the
	// sum 60000 clamps to 32767).
	audioFillConstAsm(a, "bufBA", 30000, "P")
	audioFillConstAsm(a, "bufBB", 30000, "Q")
	a.li(rS3, 0)
	a.here("pairBP")
	a.move(rA0, rS0)
	a.li(rA1, 0x8000)
	a.la(rA2, "bufBA")
	a.jalStub(0, 1)
	a.nop()
	a.move(rA0, rS1)
	a.li(rA1, 0x8000)
	a.la(rA2, "bufBB")
	a.jalStub(0, 1)
	a.nop()
	a.addiu(rS3, rS3, 1)
	a.li(rT1, 2)
	a.bne(rS3, rT1, "pairBP")
	a.nop()
	audioWaitDrainAsm(a, rS0, "BZ")
	audioWaitDrainAsm(a, rS1, "BW")
	// Fill both with -30000, 2 pairs (the sum clamps to -32768).
	audioFillConstAsm(a, "bufBA", -30000, "R")
	audioFillConstAsm(a, "bufBB", -30000, "S")
	a.li(rS3, 0)
	a.here("pairBN")
	a.move(rA0, rS0)
	a.li(rA1, 0x8000)
	a.la(rA2, "bufBA")
	a.jalStub(0, 1)
	a.nop()
	a.move(rA0, rS1)
	a.li(rA1, 0x8000)
	a.la(rA2, "bufBB")
	a.jalStub(0, 1)
	a.nop()
	a.addiu(rS3, rS3, 1)
	a.li(rT1, 2)
	a.bne(rS3, rT1, "pairBN")
	a.nop()
	audioWaitDrainAsm(a, rS0, "BA")
	audioWaitDrainAsm(a, rS1, "BB")
	a.la(rA0, "fmtB")
	a.move(rA1, rS0)
	a.move(rA2, rS1)
	a.jalStub(1, 0) // sceKernelPrintf
	a.nop()
	a.li(rA0, 0)
	a.jalStub(2, 0) // sceKernelExitGame
	a.nop()
	return b
}

// audioFillConstAsm fills a stereo buffer with a constant
// halfword value.
func audioFillConstAsm(a *gateAsm, bufLab string, val int32, tag string) {
	a.la(rS7, bufLab)
	a.li(rT6, val)
	a.li(rT0, 0)
	a.here("const" + tag)
	a.sll(rT7, rT0, 2)
	a.addu(rT7, rS7, rT7)
	a.sh(rT6, 0, rT7)
	a.sh(rT6, 2, rT7)
	a.addiu(rT0, rT0, 1)
	a.li(rT1, audioGateBufFrames)
	a.bne(rT0, rT1, "const"+tag)
	a.nop()
}

func TestPSP4GateAudioMix(t *testing.T) {
	b := buildMixGate()
	img := b.build(false, nil)
	_, _, roAddr, _, _ := b.layout(false)

	m := New()
	if err := m.LoadExecutable(img); err != nil {
		t.Fatalf("LoadExecutable: %v", err)
	}
	stream := runAudioGate(t, m, 400)

	if got := m.Bus.Read32(roAddr["cellChBA"]); got != 7 {
		t.Fatalf("first reserved channel = %d, want 7", got)
	}
	if got := m.Bus.Read32(roAddr["cellChBB"]); got != 6 {
		t.Fatalf("second reserved channel = %d, want 6", got)
	}
	if want := "mix a=7 b=6\n"; m.ConsoleOutput() != want {
		t.Fatalf("console = %q, want %q", m.ConsoleOutput(), want)
	}

	tab := audioGateTable()
	frames := len(stream) / 2
	start := -1
	for i := 0; i < frames; i++ {
		if stream[2*i] != 0 {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("stream is entirely silent")
	}
	// Both channels must start on the same output frame: the
	// first span frame is the sum at table index 0 on both.
	if got := stream[2*start]; got != 2*tab[0] {
		t.Fatalf("first mixed frame = %d, want %d (channels did not start in lockstep)", got, 2*tab[0])
	}
	// The three phases are separated by silence while the
	// guest waits for the drain and refills (its fill time is
	// guest behavior, not core behavior), so each segment is
	// located by scanning and checked exactly.
	const (
		sineFrames = 10 * audioGateBufFrames
		posFrames  = 2 * audioGateBufFrames
		negFrames  = 2 * audioGateBufFrames
	)
	nextNonzero := func(from int) int {
		for i := from; i < frames; i++ {
			if stream[2*i] != 0 || stream[2*i+1] != 0 {
				return i
			}
		}
		return -1
	}
	seg := start
	for j := 0; j < sineFrames; j++ {
		// The guest replays one static buffer per channel, so
		// the stream phase restarts every buffer.
		want := 2 * tab[(j%audioGateBufFrames)%audioGateTableLen]
		if stream[2*(seg+j)] != want || stream[2*(seg+j)+1] != want {
			t.Fatalf("sine segment frame %d: got (%d, %d), want %d", j, stream[2*(seg+j)], stream[2*(seg+j)+1], want)
		}
	}
	seg = nextNonzero(seg + sineFrames)
	if seg < 0 {
		t.Fatal("no positive clamp segment after the sine segment")
	}
	for j := 0; j < posFrames; j++ {
		if stream[2*(seg+j)] != 32767 || stream[2*(seg+j)+1] != 32767 {
			t.Fatalf("positive clamp frame %d: got (%d, %d), want 32767", j, stream[2*(seg+j)], stream[2*(seg+j)+1])
		}
	}
	seg = nextNonzero(seg + posFrames)
	if seg < 0 {
		t.Fatal("no negative clamp segment after the positive segment")
	}
	for j := 0; j < negFrames; j++ {
		if stream[2*(seg+j)] != -32768 || stream[2*(seg+j)+1] != -32768 {
			t.Fatalf("negative clamp frame %d: got (%d, %d), want -32768", j, stream[2*(seg+j)], stream[2*(seg+j)+1])
		}
	}
	if tail := nextNonzero(seg + negFrames); tail >= 0 {
		t.Fatalf("frame %d after the last segment is (%d, %d), want silence to the end", tail, stream[2*tail], stream[2*tail+1])
	}
	t.Logf("mix: %d frames exact (sine sums, +clamp, -clamp)", sineFrames+posFrames+negFrames)
}

// --- gate (d): documented edge semantics -----------------------
//
// One straight-line guest walks the documented validation
// orders and error codes and stores every return value into a
// results vector the host compares exactly, including the
// RestLen/RestLength disagreement after a null-pointer output
// and the Output2/SRC families staying on the unknown path.

var libAudioSem = gateLib{"sceAudio", []uint32{
	0x80F1F7E0, // 0 sceAudioInit
	0x5EC81C55, // 1 sceAudioChReserve
	0x6FC46853, // 2 sceAudioChRelease
	0x8C1009B2, // 3 sceAudioOutput
	0x136CAF51, // 4 sceAudioOutputBlocking
	0xE9D97901, // 5 sceAudioGetChannelRestLen
	0xB011922F, // 6 sceAudioGetChannelRestLength
	0xCB2E439E, // 7 sceAudioSetChannelDataLen
	0xB7E1D8E7, // 8 sceAudioChangeChannelVolume
	0xA2BEAA6C, // 9 sceAudioSetFrequency
	0x6D4BEC68, // 10 sceAudioInput
	0x7DE61688, // 11 sceAudioInputInit
	0xA708C6A6, // 12 sceAudioGetInputLength
	0x01562BA3, // 13 sceAudioOutput2Reserve (not implemented)
	0x38553111, // 14 sceAudioSRCChReserve (not implemented)
	0x39240E7D, // 15 sceAudioRoutingGetMode
	0x36FD8AA9, // 16 sceAudioRoutingSetMode
	0x41EFADE7, // 17 sceAudioOneshotOutput
}}

func TestPSP4GateAudioSemantics(t *testing.T) {
	b := newGateBuilder("GATESEM", []gateLib{libAudioSem, libSysMem, libLoadExec})
	bufPat := make([]byte, 4096)
	for i := range bufPat {
		bufPat[i] = 0xAA
	}
	b.ro("bufS", bufPat)
	b.ro("resS", make([]byte, 34*4))
	b.roStr("fmtS", "sem done\n")
	a := b.code
	a.addiu(rSP, rSP, -64)
	rec := func(idx int) { // store v0 into resS[idx]
		a.la(rT9(), "resS")
		a.sw(rV0, 4*idx, rT9())
	}
	call := func(fn int, args ...int32) {
		regs := []int{rA0, rA1, rA2, rA3}
		for i, v := range args {
			a.li(regs[i], v)
		}
		a.jalStub(0, fn)
		a.nop()
	}
	bufAddr := func(reg int) { a.la(reg, "bufS") }

	a.jalStub(0, 0) // Init
	a.nop()
	rec(0)
	call(1, 8, 1024, 0) // ChReserve chan 8
	rec(1)
	call(1, -1, 100, 0) // ChReserve bad count
	rec(2)
	call(1, -1, 1024, 7) // ChReserve bad format
	rec(3)
	call(1, -1, 1024, 0) // ChReserve -> 7
	rec(4)
	call(1, 7, 1024, 0) // ChReserve again
	rec(5)
	// Output on channel 9: invalid channel.
	a.li(rA0, 9)
	a.li(rA1, 0x8000)
	bufAddr(rA2)
	a.jalStub(0, 3)
	a.nop()
	rec(6)
	// Output with an oversize volume on a good channel.
	a.li(rA0, 7)
	a.li(rA1, 0x10000)
	bufAddr(rA2)
	a.jalStub(0, 3)
	a.nop()
	rec(7)
	// Output on unreserved channel 5.
	a.li(rA0, 5)
	a.li(rA1, 0x8000)
	bufAddr(rA2)
	a.jalStub(0, 3)
	a.nop()
	rec(8)
	// Null pointer output on channel 7.
	a.li(rA0, 7)
	a.li(rA1, 0x8000)
	a.li(rA2, 0)
	a.jalStub(0, 3)
	a.nop()
	rec(9)
	call(5, 7) // GetChannelRestLen
	rec(10)
	call(6, 7) // GetChannelRestLength
	rec(11)
	// Real output right after the null one: not busy.
	a.li(rA0, 7)
	a.li(rA1, 0x8000)
	bufAddr(rA2)
	a.jalStub(0, 3)
	a.nop()
	rec(12)
	// And again while it plays: busy.
	a.li(rA0, 7)
	a.li(rA1, 0x8000)
	bufAddr(rA2)
	a.jalStub(0, 3)
	a.nop()
	rec(13)
	call(7, 7, 100) // SetChannelDataLen bad length
	rec(14)
	call(7, 5, 2048) // SetChannelDataLen unreserved
	rec(15)
	call(7, 7, 2048) // SetChannelDataLen ok
	rec(16)
	call(8, 7, 0x10000, 0) // ChangeChannelVolume oversize
	rec(17)
	call(8, 7, 0x4000, -1) // ChangeChannelVolume ok
	rec(18)
	call(9, 22050) // SetFrequency bad
	rec(19)
	call(9, 44100) // SetFrequency ok
	rec(20)
	call(11, 0, 0) // InputInit
	rec(21)
	// Input(64, 44100, buf): silence written, count returned.
	a.li(rA0, 64)
	a.li(rA1, 44100)
	bufAddr(rA2)
	a.jalStub(0, 10)
	a.nop()
	rec(22)
	// Input with a null buffer.
	a.li(rA0, 64)
	a.li(rA1, 44100)
	a.li(rA2, 0)
	a.jalStub(0, 10)
	a.nop()
	rec(23)
	call(12) // GetInputLength
	rec(24)
	call(15) // RoutingGetMode
	rec(25)
	call(16, 0) // RoutingSetMode(0) returns the previous
	rec(26)
	call(15) // RoutingGetMode
	rec(27)
	call(13, 1024) // Output2Reserve: unknown
	rec(28)
	call(14, 1024, 44100, 2) // SRCChReserve: unknown
	rec(29)
	call(2, 5) // ChRelease unreserved
	rec(30)
	call(2, 7) // ChRelease with a buffer in flight
	rec(31)
	// Oneshot on the next free channel: 7 is still playing
	// out its released buffer, so the search lands on 6.
	a.li(rA0, -1)
	a.li(rA1, 512)
	a.li(rA2, 0)
	a.li(rA3, 0x8000)
	a.li(rT0, 0x8000)
	a.sw(rT0, 16, rSP) // rightvol (stack arg 4)
	bufAddr(rT0)
	a.sw(rT0, 20, rSP) // buf (stack arg 5)
	a.jalStub(0, 17)
	a.nop()
	rec(32)
	call(5, 6) // GetChannelRestLen on the oneshot channel
	rec(33)

	a.la(rA0, "fmtS")
	a.jalStub(1, 0) // sceKernelPrintf
	a.nop()
	a.li(rA0, 0)
	a.jalStub(2, 0) // sceKernelExitGame
	a.nop()

	img := b.build(false, nil)
	_, _, roAddr, _, _ := b.layout(false)
	m := New()
	if err := m.LoadExecutable(img); err != nil {
		t.Fatalf("LoadExecutable: %v", err)
	}
	runAudioGate(t, m, 60)
	if got := m.ConsoleOutput(); got != "sem done\n" {
		t.Fatalf("console = %q, want %q", got, "sem done\n")
	}
	want := []uint32{
		0,          // Init
		0x80260003, // reserve chan 8: invalid channel
		0x80260006, // reserve count 100: not aligned
		0x80260007, // reserve format 7: invalid format
		7,          // reserve: first free from the top
		0x80260003, // reserve 7 again: invalid channel
		0x80260003, // output chan 9
		0x8026000B, // output volume 0x10000
		0x80260001, // output unreserved 5: not init
		1024,       // null output accepted
		1024,       // RestLen counts the null buffer
		0,          // RestLength does not
		1024,       // real output after null: not busy
		0x80260002, // output while playing: busy
		0x80260006, // SetChannelDataLen 100
		0x80260001, // SetChannelDataLen unreserved
		0,          // SetChannelDataLen 2048
		0x8026000B, // ChangeChannelVolume oversize
		0,          // ChangeChannelVolume
		0x8026000A, // SetFrequency 22050
		0,          // SetFrequency 44100
		0,          // InputInit
		64,         // Input delivers 64 silent samples
		0xFFFFFFFF, // Input null buffer
		0,          // GetInputLength
		1,          // RoutingGetMode default
		1,          // RoutingSetMode returns previous
		0,          // RoutingGetMode after set
		0x80020001, // Output2Reserve: unknown NID
		0x80020001, // SRCChReserve: unknown NID
		0x80260008, // ChRelease unreserved
		0,          // ChRelease in flight
		6,          // Oneshot lands on channel 6
		512,        // oneshot rest length
	}
	resAddr := roAddr["resS"]
	for i, w := range want {
		if got := m.Bus.Read32(resAddr + uint32(4*i)); got != w {
			t.Fatalf("result %d = %#08x, want %#08x", i, got, w)
		}
	}
	// The input call zeroed the first 128 bytes of the buffer.
	buf := roAddr["bufS"]
	for i := 0; i < 128; i++ {
		if got := m.Bus.Read8(buf + uint32(i)); got != 0 {
			t.Fatalf("input buffer byte %d = %#x, want 0 (silence)", i, got)
		}
	}
	// Exactly the two unimplemented families were logged.
	unknown := m.Kernel().UnknownCalls()
	if len(unknown) != 2 {
		t.Fatalf("UnknownCalls = %v, want exactly the Output2 and SRC reserves", unknown)
	}
	seen := map[uint32]bool{}
	for _, u := range unknown {
		if u.Module != "sceAudio" {
			t.Fatalf("unknown call module = %q, want sceAudio", u.Module)
		}
		seen[u.NID] = true
	}
	if !seen[0x01562BA3] || !seen[0x38553111] {
		t.Fatalf("UnknownCalls NIDs = %v, want 0x01562BA3 and 0x38553111", seen)
	}
}

// rT9 is $t9 (25), used by the semantics gate as a scratch
// base register.
func rT9() int { return 25 }

// --- gate (c): a pure audio guest cannot deadlock --------------

func TestPSP4GateAudioOnlyGuest(t *testing.T) {
	b := newGateBuilder("GATEC", []gateLib{libAudio4})
	b.ro("tabC", audioGateTableBytes())
	b.ro("pbufC", make([]byte, audioGateBufFrames*4))
	b.roWords("cellCallsC", 0)
	a := b.code
	a.addiu(rSP, rSP, -32)
	a.addiu(rA0, rZero, -1)
	a.li(rA1, audioGateBufFrames)
	a.li(rA2, 0)
	a.jalStub(0, 0) // sceAudioChReserve
	a.nop()
	a.move(rS0, rV0)
	a.li(rS4, 0)
	audioFillAsm(a, "tabC", "pbufC", rS4, "C")
	a.li(rS3, 0)
	a.here("loopC")
	a.move(rA0, rS0)
	a.li(rA1, 0x8000)
	a.la(rA2, "pbufC")
	a.jalStub(0, 1) // sceAudioOutputBlocking, forever
	a.nop()
	a.addiu(rS3, rS3, 1)
	a.la(rT0, "cellCallsC")
	a.sw(rS3, 0, rT0)
	a.j("loopC")
	a.nop()
	img := b.build(false, nil)
	_, _, roAddr, _, _ := b.layout(false)

	m := New()
	if err := m.LoadExecutable(img); err != nil {
		t.Fatalf("LoadExecutable: %v", err)
	}
	const frames = 30
	var stream []int16
	for f := 0; f < frames; f++ {
		m.RunFrame()
		stream = append(stream, m.AudioBuffer()...)
	}
	if m.Exited() {
		t.Fatal("pure audio guest exited; it should loop on output forever")
	}
	if got := m.Kernel().Vcount(); got != frames {
		t.Fatalf("vcount = %d, want %d (frames must keep advancing)", got, frames)
	}
	if want := frames * 735 * 2; len(stream) != want {
		t.Fatalf("drained %d samples, want %d (735 stereo frames per frame)", len(stream), want)
	}
	nonSilent := 0
	for _, s := range stream {
		if s != 0 {
			nonSilent++
		}
	}
	if nonSilent < 20000 {
		t.Fatalf("only %d non-silent samples in %d; the tone must be playing", nonSilent, len(stream))
	}
	if calls := m.Bus.Read32(roAddr["cellCallsC"]); calls < 15 {
		t.Fatalf("guest completed %d blocking calls in 30 frames, want at least 15", calls)
	} else {
		t.Logf("pure audio guest: %d blocking calls in 30 frames, %d samples drained", calls, len(stream))
	}
	// The guest replays one static buffer forever, so after
	// the leading silence every frame is the buffer pattern
	// exactly: buffer frame j holds table[j mod 100].
	tab := audioGateTable()
	start := -1
	for i := 0; i < len(stream)/2; i++ {
		if stream[2*i] != 0 {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("stream is entirely silent")
	}
	for i := start; i < len(stream)/2; i++ {
		want := tab[((i-start)%audioGateBufFrames)%audioGateTableLen]
		if stream[2*i] != want || stream[2*i+1] != want {
			t.Fatalf("frame %d: got (%d, %d), want replay value %d", i, stream[2*i], stream[2*i+1], want)
		}
	}
}
