package apu

import (
	"bytes"
	"math"
	"testing"
)

func masterOn(a *APU) {
	a.WriteReg(0x84, 1, 0x80)
}

// countRising counts positive zero crossings of the
// left channel of interleaved stereo samples.
func countRising(samples []int16) int {
	count := 0
	prev := samples[0]
	for i := 1; i < len(samples)/2; i++ {
		cur := samples[2*i]
		if prev < 0 && cur >= 0 {
			count++
		}
		prev = cur
	}
	return count
}

func anyNonzero(samples []int16) bool {
	for _, s := range samples {
		if s != 0 {
			return true
		}
	}
	return false
}

func TestA4Pitch(t *testing.T) {
	a := New()
	masterOn(a)
	a.WriteReg(0x80, 2, 0x1177)
	a.WriteReg(0x82, 2, 2)
	a.WriteReg(0x62, 2, 0xF080)
	// Frequency 0x6D6 (1750): 131072 / (2048 - 1750) =
	// 439.84 Hz, the documented nearest value to A4.
	a.WriteReg(0x64, 2, 0x86D6)
	if !a.DebugChannelOn(1) {
		t.Fatal("channel 1 not on after trigger")
	}
	a.Advance(16777216)
	out := a.AudioBuffer()
	if got := len(out) / 2; got != 32768 {
		t.Fatalf("frames = %d, want 32768", got)
	}
	if !anyNonzero(out) {
		t.Fatal("all samples are zero")
	}
	count := countRising(out)
	t.Logf("positive zero crossings in one second: %d", count)
	if math.Abs(float64(count-440)) > 1 {
		t.Fatalf("crossings = %d, want 440 plus or minus 1", count)
	}
}

func TestSweepSteps(t *testing.T) {
	a := New()
	masterOn(a)
	a.WriteReg(0x80, 2, 0x1177)
	// Sweep pace 1, shift 1, direction up.
	a.WriteReg(0x60, 2, 0x0011)
	a.WriteReg(0x62, 2, 0xF080)
	a.WriteReg(0x64, 2, 0x8100)
	clocks := []int{98304, 229376, 360448, 491520, 622592}
	want := []int{384, 576, 864, 1296, 1944}
	total := 0
	for i, clk := range clocks {
		a.Advance(clk - total)
		total = clk
		if got := a.DebugFreq(1); got != want[i] {
			t.Fatalf("after sweep clock at %d: freq = %d, want %d", clk, got, want[i])
		}
		t.Logf("sweep clock %d: freq %d", clk, a.DebugFreq(1))
	}
	if a.DebugChannelOn(1) {
		t.Fatal("channel 1 still on after the overflowing sweep calculation")
	}
}

func TestEnvelopeDecay(t *testing.T) {
	a := New()
	masterOn(a)
	// Route channel 2 to both sides at full master
	// volume, PSG ratio 100 percent, so the envelope is
	// observable in the output samples themselves.
	a.WriteReg(0x80, 2, 0x2277) // SOUNDCNT_L: ch2 L+R, volumes 7
	a.WriteReg(0x82, 2, 0x0002) // SOUNDCNT_H: PSG 100%
	// Duty 0, length 0 in the low byte; envelope byte
	// 0xF1: initial volume 15, direction down, step 1.
	a.WriteReg(0x68, 2, 0xF100)
	a.WriteReg(0x6C, 2, 0x86D6)
	a.Advance(262143)
	if got := a.DebugVol(2); got != 15 {
		t.Fatalf("vol after 262143 cycles = %d, want 15", got)
	}
	// Sample amplitude at volume 15: the square swings
	// between the code 15 and code 0 terms, a peak to
	// peak of 16384 in the output domain.
	out := a.AudioBuffer()
	amp := amplitude(out)
	if amp < 16000 {
		t.Fatalf("output amplitude at vol 15 = %d, want near 16384", amp)
	}
	a.Advance(1)
	if got := a.DebugVol(2); got != 14 {
		t.Fatalf("vol after 262144 cycles = %d, want 14", got)
	}
	a.Advance(15*262144 - 262144)
	if got := a.DebugVol(2); got != 0 {
		t.Fatalf("vol after 15 envelope periods = %d, want 0", got)
	}
	if !a.DebugChannelOn(2) {
		t.Fatal("channel 2 off after envelope reached zero")
	}
	// At volume 0 the channel holds DAC code 0: the
	// output is a constant level (the code 0 term,
	// -8192) with zero amplitude.
	a.AudioBuffer()
	a.Advance(512 * 16)
	out = a.AudioBuffer()
	if amp := amplitude(out); amp != 0 {
		t.Fatalf("output amplitude at vol 0 = %d, want 0", amp)
	}
	for i, s := range out {
		if s != -8192 {
			t.Fatalf("sample %d = %d at vol 0, want the constant code 0 level -8192", i, s)
		}
	}
	t.Logf("envelope reached 0 on schedule; output amplitude fell from %d to a constant -8192", amp)
}

// amplitude returns the peak to peak swing of the left
// channel in a drained stereo buffer.
func amplitude(out []int16) int {
	lo, hi := 0, 0
	for i := 0; i+1 < len(out); i += 2 {
		v := int(out[i])
		if i == 0 || v < lo {
			lo = v
		}
		if i == 0 || v > hi {
			hi = v
		}
	}
	return hi - lo
}

func TestLengthShutdown(t *testing.T) {
	a := New()
	masterOn(a)
	// Length load 32 gives a length counter of 32;
	// envelope 0xF0 in the high byte.
	a.WriteReg(0x68, 2, 0xF020)
	// Trigger with length enable set.
	a.WriteReg(0x6C, 2, 0xC6D6)
	a.Advance(2064383)
	if !a.DebugChannelOn(2) {
		t.Fatal("channel 2 off before the 32nd length clock")
	}
	a.Advance(1)
	if a.DebugChannelOn(2) {
		t.Fatal("channel 2 still on after the 32nd length clock at cycle 2064384")
	}
	if got := a.ReadReg(0x84, 1); got&2 != 0 {
		t.Fatalf("SOUNDCNT_X = %#x, channel 2 flag still set", got)
	}
	t.Logf("channel 2 shut down at cycle 2064384 as expected")
}

func TestWaveRamp(t *testing.T) {
	a := New()
	masterOn(a)
	// Bank bit clear: CPU wave RAM writes go to bank 1.
	a.WriteReg(0x70, 1, 0x00)
	pattern := []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF}
	for i, b := range pattern {
		a.WriteReg(0x90+uint32(i), 1, uint32(b))
	}
	// DAC on, play bank 1, one bank mode.
	a.WriteReg(0x70, 1, 0xC0)
	// Volume code 1 (100 percent) at bits 13 to 14.
	a.WriteReg(0x72, 2, 1<<13)
	// Word write carrying frequency 1984 (0x7C0) and
	// the trigger in its top byte; digit period is
	// (2048 - 1984) * 8 = 512 cycles, exactly one
	// output sample per digit at the default rate.
	a.WriteReg(0x74, 2, 0x87C0)
	if !a.DebugChannelOn(3) {
		t.Fatal("wave channel not on after trigger")
	}
	a.WriteReg(0x80, 2, 0x4477)
	a.WriteReg(0x82, 2, 2)
	a.Advance(512 * 40)
	out := a.AudioBuffer()
	frames := len(out) / 2
	if frames < 40 {
		t.Fatalf("frames = %d, want at least 40", frames)
	}
	for i := 1; i < 40; i++ {
		digit := (i % 32) % 16
		want := (float64(digit)*256.0/15.0 - 128.0) * 64.0
		got := float64(out[2*i])
		if math.Abs(got-want) > 4 {
			t.Fatalf("frame %d left = %v, want within 4 of %v (digit %d)", i, got, want, digit)
		}
	}
	for i := 1; i <= 7; i++ {
		if out[2*i] != out[2*(i+32)] {
			t.Fatalf("frame %d left = %d, frame %d left = %d, want identical", i, out[2*i], i+32, out[2*(i+32)])
		}
	}
	t.Logf("wave ramp frames checked: 40, frame 1 = %d, frame 15 = %d, frame 16 = %d",
		out[2], out[30], out[32])
}

func TestNoiseLFSR(t *testing.T) {
	a := New()
	masterOn(a)
	a.WriteReg(0x78, 2, 0xF000)

	// 7 bit width, ratio 0, shift 0: period 32 cycles.
	a.WriteReg(0x7C, 2, 0x8008)
	if got := a.DebugLFSR(); got != 0x40 {
		t.Fatalf("LFSR after 7 bit trigger = %#x, want 0x40", got)
	}
	x := uint16(0x40)
	for step := 0; step < 24; step++ {
		carry := x & 1
		x >>= 1
		x &= 0x7F
		wantOut := 0
		if carry != 0 {
			x ^= 0x60
			wantOut = 1
		}
		a.Advance(32)
		if got := a.DebugLFSR(); got != x {
			t.Fatalf("7 bit step %d: LFSR = %#x, want %#x", step, got, x)
		}
		if got := a.DebugNoiseOut(); got != wantOut {
			t.Fatalf("7 bit step %d: out = %d, want %d", step, got, wantOut)
		}
	}
	t.Logf("7 bit LFSR after 24 clocks: %#x", a.DebugLFSR())

	// 15 bit width, same period.
	a.WriteReg(0x7C, 2, 0x8000)
	if got := a.DebugLFSR(); got != 0x4000 {
		t.Fatalf("LFSR after 15 bit trigger = %#x, want 0x4000", got)
	}
	x = 0x4000
	for step := 0; step < 24; step++ {
		carry := x & 1
		x >>= 1
		wantOut := 0
		if carry != 0 {
			x ^= 0x6000
			wantOut = 1
		}
		x &= 0x7FFF
		a.Advance(32)
		if got := a.DebugLFSR(); got != x {
			t.Fatalf("15 bit step %d: LFSR = %#x, want %#x", step, got, x)
		}
		if got := a.DebugNoiseOut(); got != wantOut {
			t.Fatalf("15 bit step %d: out = %d, want %d", step, got, wantOut)
		}
	}
	t.Logf("15 bit LFSR after 24 clocks: %#x", a.DebugLFSR())

	// One second of 15 bit noise routed to both sides.
	a.WriteReg(0x80, 2, 0x8877)
	a.WriteReg(0x82, 2, 2)
	a.WriteReg(0x78, 2, 0xF000)
	a.WriteReg(0x7C, 2, 0x8000)
	a.AudioBuffer()
	a.Advance(16777216)
	out := a.AudioBuffer()
	if got := len(out) / 2; got != 32768 {
		t.Fatalf("frames = %d, want 32768", got)
	}
	if !anyNonzero(out) {
		t.Fatal("noise output is all zero")
	}
	count := countRising(out)
	t.Logf("noise positive zero crossings in one second: %d", count)
	if count < 2000 || count > 14000 {
		t.Fatalf("noise crossings = %d, want within 2000 to 14000", count)
	}
}

func TestMasterOff(t *testing.T) {
	a := New()
	masterOn(a)
	a.WriteReg(0x80, 2, 0x1177)
	a.WriteReg(0x82, 2, 2)
	a.WriteReg(0x62, 2, 0xF080)
	a.WriteReg(0x64, 2, 0x86D6)
	a.Advance(512 * 100)
	out := a.AudioBuffer()
	if !anyNonzero(out) {
		t.Fatal("expected nonzero samples before master off")
	}
	a.WriteReg(0x84, 1, 0x00)
	if got := a.ReadReg(0x62, 2); got != 0x003F {
		t.Fatalf("SOUND1CNT_H after master off = %#x, want 0x003f", got)
	}
	if got := a.ReadReg(0x82, 2); got != 2 {
		t.Fatalf("SOUNDCNT_H after master off = %#x, want its stored value 2", got)
	}
	if a.DebugChannelOn(1) {
		t.Fatal("channel 1 still on after master off")
	}
	a.Advance(512 * 100)
	out = a.AudioBuffer()
	if len(out) != 200 {
		t.Fatalf("drained %d samples after master off, want 200", len(out))
	}
	for i, s := range out {
		if s != 0 {
			t.Fatalf("sample %d after master off = %d, want 0", i, s)
		}
	}
	t.Logf("master off: registers zeroed, SOUNDCNT_H preserved, output silent")
}

func TestFIFOPopRefill(t *testing.T) {
	a := New()
	masterOn(a)
	// PSG ratio 100 percent, A volume 100 percent, A
	// enabled right and left, A timer select 0.
	a.WriteReg(0x82, 2, 0x0306)
	refills := 0
	a.Refill = func(fifo int) {
		if fifo == 0 {
			refills++
		}
	}
	for i := 0; i < 4; i++ {
		a.PushFIFO(0, 0x04030201)
	}
	if got := a.FIFOLevel(0); got != 16 {
		t.Fatalf("FIFO level = %d, want 16", got)
	}
	if !a.WantsData(0) {
		t.Fatal("WantsData false at level 16")
	}
	a.OnTimerOverflow(1)
	if got := a.FIFOLevel(0); got != 16 {
		t.Fatalf("level after wrong timer overflow = %d, want 16", got)
	}
	if refills != 0 {
		t.Fatalf("refills after wrong timer = %d, want 0", refills)
	}
	a.OnTimerOverflow(0)
	if got := a.FIFOLevel(0); got != 15 {
		t.Fatalf("level after first pop = %d, want 15", got)
	}
	a.Advance(512)
	out := a.AudioBuffer()
	if len(out) != 2 {
		t.Fatalf("drained %d samples, want 2 (one stereo frame)", len(out))
	}
	// Held sample is the first pushed byte, 1: its
	// Direct Sound term is 1 * 1.0 * 4 = 4 in the
	// mixer domain, times 64 at emission gives 256.
	if out[0] != 256 || out[1] != 256 {
		t.Fatalf("first FIFO sample = %v, want [256 256]", out)
	}
	for want := 14; want >= 10; want-- {
		a.OnTimerOverflow(0)
		if got := a.FIFOLevel(0); got != want {
			t.Fatalf("level = %d, want %d", got, want)
		}
	}
	if refills != 6 {
		t.Fatalf("refill calls = %d, want 6 (one per pop)", refills)
	}
	a.PushFIFO(0, 0x04030201)
	if got := a.FIFOLevel(0); got != 14 {
		t.Fatalf("level after extra push = %d, want 14", got)
	}
	t.Logf("FIFO pops 16 to 10 with 6 refills, held sample rendered 256")
}

func TestStateRoundTrip(t *testing.T) {
	a := New()
	masterOn(a)
	a.WriteReg(0x80, 2, 0x5577)
	a.WriteReg(0x82, 2, 2)
	a.WriteReg(0x62, 2, 0xF080)
	a.WriteReg(0x64, 2, 0x86D6)

	a.WriteReg(0x70, 1, 0x00)
	pattern := []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF}
	for i, b := range pattern {
		a.WriteReg(0x90+uint32(i), 1, uint32(b))
	}
	a.WriteReg(0x70, 1, 0xC0)
	a.WriteReg(0x72, 2, 1<<13)
	a.WriteReg(0x74, 2, 0x87C0)

	for i := 0; i < 5; i++ {
		a.PushFIFO(0, 0x04030201)
	}
	if got := a.FIFOLevel(0); got != 20 {
		t.Fatalf("FIFO level = %d, want 20", got)
	}
	a.Advance(100000)
	a.AudioBuffer()

	s1 := make([]byte, StateSize)
	if n := a.Serialize(s1); n != StateSize {
		t.Fatalf("Serialize wrote %d bytes, want %d", n, StateSize)
	}
	b := New()
	if !b.Deserialize(s1) {
		t.Fatal("Deserialize rejected a state written by Serialize")
	}
	s2 := make([]byte, StateSize)
	if n := b.Serialize(s2); n != StateSize {
		t.Fatalf("second Serialize wrote %d bytes, want %d", n, StateSize)
	}
	if !bytes.Equal(s1, s2) {
		t.Fatal("serialized state changed across a round trip")
	}
	a.Advance(4096 * 512)
	b.Advance(4096 * 512)
	outA := a.AudioBuffer()
	outB := b.AudioBuffer()
	if len(outA) != len(outB) {
		t.Fatalf("drained lengths differ: %d and %d", len(outA), len(outB))
	}
	for i := range outA {
		if outA[i] != outB[i] {
			t.Fatalf("sample %d differs after round trip: %d and %d", i, outA[i], outB[i])
		}
	}
	t.Logf("round trip identical, %d stereo samples compared bit for bit", len(outA)/2)
}
