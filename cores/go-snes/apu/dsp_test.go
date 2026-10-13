package apu

import (
	"math"
	"testing"
)

func newTestDSP() (*DSP, []byte) {
	ram := make([]byte, 65536)
	d := NewDSP()
	d.AttachRAM(ram)
	return d, ram
}

// putBlock writes a 9 byte BRR block at addr.
func putBlock(ram []byte, addr uint16, header byte, data [8]byte) {
	ram[addr] = header
	copy(ram[addr+1:], data[:])
}

// decodeN decodes n samples straight from the BRR decoder
// of voice 0, bypassing pitch and interpolation.
func decodeN(d *DSP, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = d.decodeNextBRR(0)
	}
	return out
}

func setupDecodeVoice(d *DSP, start, loop uint16) {
	v := &d.voices[0]
	v.on = true
	v.blockAddr = start
	v.loopAddr = loop
	v.sampleIdx = 0
}

func eqInts(t *testing.T, name string, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d samples, want %d", name, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: sample %d = %d, want %d (full got %v)", name, i, got[i], want[i], got)
		}
	}
}

// Range shift with filter 0: sample = (nibble SHL 4) SAR 1
// = nibble * 8. Nibbles: 1,-1,2,-2,3,-3,4,-4,5,-5,6,-6,
// 7,-7,-8,0.
func TestBRRRangeShiftFilter0(t *testing.T) {
	d, ram := newTestDSP()
	putBlock(ram, 0x1000, 0x40, [8]byte{0x1F, 0x2E, 0x3D, 0x4C, 0x5B, 0x6A, 0x79, 0x80})
	setupDecodeVoice(d, 0x1000, 0x1000)
	got := decodeN(d, 16)
	want := []int{8, -8, 16, -16, 24, -24, 32, -32, 40, -40, 48, -48, 56, -56, -64, 0}
	eqInts(t, "range4 f0", got, want)
}

// Filter 1: out = s + old - (old SAR 4) in the doc exact
// form s + old + ((-old) SAR 4). With range 1 the source
// samples equal the nibbles; feeding constant 4s, the
// recurrence o(n) = 4 + o(n-1) + ((-o(n-1)) SAR 4) walks
// 4, 7, 10, ... and settles at the fixed point 49 (where
// 4 + 49 - 4 = 49). The state carries across the block
// boundary into the second identical block.
func TestBRRFilter1AcrossBlocks(t *testing.T) {
	d, ram := newTestDSP()
	data := [8]byte{0x44, 0x44, 0x44, 0x44, 0x44, 0x44, 0x44, 0x44}
	putBlock(ram, 0x1000, 0x14, data) // range 1, filter 1
	putBlock(ram, 0x1009, 0x14, data)
	setupDecodeVoice(d, 0x1000, 0x1000)
	got := decodeN(d, 32)
	want := []int{
		4, 7, 10, 13, 16, 19, 21, 23, 25, 27, 29, 31, 33, 34, 35, 36,
		37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 49, 49, 49,
	}
	eqInts(t, "filter1", got, want)
}

// Filter 2 and 3 on an impulse: range 3, first nibble 4
// gives s = (4 SHL 3) SAR 1 = 16, then all zero nibbles.
// Expected values are the two recurrences from fullsnes
// iterated by hand:
// f2: o = s + 2*old + ((-3*old) SAR 5) - older + (older SAR 4)
// f3: o = s + 2*old + ((-13*old) SAR 6) - older + ((3*older) SAR 4)
func TestBRRFilter2And3Impulse(t *testing.T) {
	data := [8]byte{0x40, 0, 0, 0, 0, 0, 0, 0}

	d, ram := newTestDSP()
	putBlock(ram, 0x1000, 0x38, data) // range 3, filter 2
	setupDecodeVoice(d, 0x1000, 0x1000)
	eqInts(t, "filter2", decodeN(d, 16),
		[]int{16, 30, 42, 51, 57, 60, 60, 57, 51, 43, 33, 21, 9, -3, -15, -27})

	d2, ram2 := newTestDSP()
	putBlock(ram2, 0x1000, 0x3C, data) // range 3, filter 3
	setupDecodeVoice(d2, 0x1000, 0x1000)
	eqInts(t, "filter3", decodeN(d2, 16),
		[]int{16, 28, 37, 43, 46, 47, 46, 43, 39, 35, 30, 24, 18, 12, 6, 0})
}

// Range 13 special case: decoding works as if shift=12
// and nibble=(nibble SAR 3), so positive nibbles give 0
// and negative nibbles give ( -1 SHL 12) SAR 1 = -2048.
// Nibbles here: 7, -8, 3, -1, then zeros.
func TestBRRRange13SpecialCase(t *testing.T) {
	d, ram := newTestDSP()
	putBlock(ram, 0x1000, 0xD0, [8]byte{0x78, 0x3F, 0, 0, 0, 0, 0, 0})
	setupDecodeVoice(d, 0x1000, 0x1000)
	got := decodeN(d, 6)
	eqInts(t, "range13", got, []int{0, -2048, 0, -2048, 0, 0})
}

// End block without loop: ENDX is set when the end block
// starts decoding (per fullsnes), and when the block
// finishes the voice is keyed off with envelope zero.
func TestBRREndBlockStopsVoice(t *testing.T) {
	d, ram := newTestDSP()
	putBlock(ram, 0x1000, 0x01, [8]byte{}) // range 0, filter 0, end+mute
	setupDecodeVoice(d, 0x1000, 0x1000)
	d.voices[0].env = 0x500
	d.voices[0].envState = envSustain
	decodeN(d, 1)
	if d.endx&1 == 0 {
		t.Fatal("ENDX bit 0 not set at start of end block")
	}
	decodeN(d, 15)
	if d.voices[0].on {
		t.Fatal("voice still on after end+mute block")
	}
	if d.voices[0].env != 0 {
		t.Fatalf("env = %d, want 0 after end+mute", d.voices[0].env)
	}
	if got := d.ReadReg(0x7C); got&1 == 0 {
		t.Fatalf("ENDX register = %#x, want bit 0 set", got)
	}
	// Any write to ENDX clears all bits.
	d.WriteReg(0x7C, 0xA5)
	if d.endx != 0 {
		t.Fatalf("ENDX = %#x after write, want 0", d.endx)
	}
}

// End block with loop: after 16 samples the decoder
// jumps to the directory loop address; ENDX stays set
// until written clear.
func TestBRRLoopBlockJumps(t *testing.T) {
	d, ram := newTestDSP()
	putBlock(ram, 0x1000, 0x03, [8]byte{}) // end+loop
	putBlock(ram, 0x2000, 0x00, [8]byte{})
	setupDecodeVoice(d, 0x1000, 0x2000)
	decodeN(d, 16)
	if d.voices[0].blockAddr != 0x2000 {
		t.Fatalf("blockAddr = %#x, want loop address 0x2000", d.voices[0].blockAddr)
	}
	if !d.voices[0].on {
		t.Fatal("voice keyed off by a looping end block")
	}
	if d.endx&1 == 0 {
		t.Fatal("ENDX not set by looping end block")
	}
}

// setupToneVoice prepares voice 0 pointing at a looping
// silent block, with full volumes, so envelope tests can
// KON it and watch the envelope machine in isolation.
func setupToneVoice(d *DSP, ram []byte) {
	ram[0x0600] = 0x00
	ram[0x0601] = 0x10 // start 0x1000
	ram[0x0602] = 0x00
	ram[0x0603] = 0x10                     // loop 0x1000
	putBlock(ram, 0x1000, 0x03, [8]byte{}) // end+loop, silent
	d.WriteReg(0x5D, 0x06)                 // DIR = 0x0600
	d.WriteReg(0x00, 0x7F)                 // VOLL
	d.WriteReg(0x01, 0x7F)                 // VOLR
	d.WriteReg(0x02, 0x00)                 // PITCHL
	d.WriteReg(0x03, 0x10)                 // PITCHH: pitch 0x1000
	d.WriteReg(0x04, 0x00)                 // SRCN
	d.WriteReg(0x0C, 0x7F)                 // MVOLL
	d.WriteReg(0x2C, 0x7F)                 // MVOLR
	d.WriteReg(0x6C, 0x20)                 // FLG: echo writes off
}

func stepDSP(d *DSP, n int) {
	for i := 0; i < n; i++ {
		d.StepSample()
	}
}

// ADSR attack: AR=7 gives rate 15, period 64 ticks from
// the rate table. Each step adds 0x20, so the envelope
// needs 64 steps to first reach 0x7FF (63 steps reach
// 0x7E0, the 64th caps at 0x7FF): 64*64 = 4096 samples.
func TestADSRAttackTiming(t *testing.T) {
	d, ram := newTestDSP()
	setupToneVoice(d, ram)
	d.WriteReg(0x05, 0x87) // ADSR on, DR=0, AR=7
	d.WriteReg(0x06, 0xE0) // SL=7, SR=0
	d.WriteReg(0x4C, 0x01) // KON voice 0
	stepDSP(d, 4095)
	if got := d.Envelope(0); got != 0x7E0 {
		t.Fatalf("env after 4095 samples = %#x, want 0x7e0", got)
	}
	stepDSP(d, 1)
	if got := d.Envelope(0); got != 0x7FF {
		t.Fatalf("env after 4096 samples = %#x, want 0x7ff", got)
	}
	if d.voices[0].envState != envDecay {
		t.Fatalf("state = %d, want decay after attack peak", d.voices[0].envState)
	}
}

// ADSR decay: DR=5 gives rate 10, period 192. Each decay
// step applies env = env*251/256. From the attack peak
// 0x7FF, 10 steps must match the recurrence exactly, and
// with SL=6 the sustain threshold is ((6+1)<<8)-1 = 1791,
// crossed at step 7, which must switch the voice to
// Sustain.
func TestADSRDecayAndSustainCrossing(t *testing.T) {
	d, ram := newTestDSP()
	setupToneVoice(d, ram)
	d.WriteReg(0x05, 0xDF) // ADSR on, DR=5, AR=15 (rate 31: step every sample)
	d.WriteReg(0x06, 0xC0) // SL=6, SR=0
	d.WriteReg(0x4C, 0x01)
	stepDSP(d, 64) // 64 attack steps of 0x20 reach the peak
	if got := d.Envelope(0); got != 0x7FF {
		t.Fatalf("env after attack = %#x, want 0x7ff", got)
	}
	want := 0x7FF
	for i := 0; i < 7; i++ {
		want = want * 251 / 256
	}
	stepDSP(d, 7*192)
	if got := d.Envelope(0); got != want {
		t.Fatalf("env after 7 decay steps = %d, want %d", got, want)
	}
	if d.voices[0].envState != envSustain {
		t.Fatalf("state = %d, want sustain after crossing 1791", d.voices[0].envState)
	}
	// Three more decay period lengths in Sustain: the
	// sustain rate is SR*2 = 0, period 2048, so within
	// 3*192 = 576 further samples no step may occur.
	stepDSP(d, 576)
	if got := d.Envelope(0); got != want {
		t.Fatalf("env moved in sustain before its period: %d, want %d", got, want)
	}
	stepDSP(d, 2048-576)
	want = want * 251 / 256
	if got := d.Envelope(0); got != want {
		t.Fatalf("env after one sustain step = %d, want %d", got, want)
	}
}

// GAIN bent increase: rate 31 (step every sample), steps
// are +0x20 below 0x600 and +0x10 at and above 0x600.
// From 0: 47 steps reach 0x5E0, step 48 reaches exactly
// 0x600 (still a full step, taken while below), step 49
// must add only 0x10.
func TestGAINBentIncreaseKnee(t *testing.T) {
	d, ram := newTestDSP()
	setupToneVoice(d, ram)
	d.WriteReg(0x05, 0x00) // ADSR1 bit 7 clear: GAIN mode
	d.WriteReg(0x07, 0xFF) // GAIN: custom, mode 3 bent, rate 31
	d.WriteReg(0x4C, 0x01)
	stepDSP(d, 47)
	if got := d.Envelope(0); got != 0x5E0 {
		t.Fatalf("env after 47 steps = %#x, want 0x5e0", got)
	}
	stepDSP(d, 1)
	if got := d.Envelope(0); got != 0x600 {
		t.Fatalf("env after 48 steps = %#x, want 0x600", got)
	}
	stepDSP(d, 1)
	if got := d.Envelope(0); got != 0x610 {
		t.Fatalf("env after 49 steps = %#x, want 0x610 (half step above the knee)", got)
	}
}

// Noise: with FLG rate 31 the LFSR clocks every sample.
// The first states from the reset pattern 4000h, applying
// the documented rule (shift right, feedback = bit0 XOR
// bit1 into bit 14), must match iteration of that rule.
func TestNoiseLFSRSequence(t *testing.T) {
	d, _ := newTestDSP()
	d.WriteReg(0x6C, 0x1F) // noise rate 31, writes on, unmuted
	want := uint16(0x4000)
	for i := 0; i < 32; i++ {
		fb := (want & 1) ^ ((want >> 1) & 1)
		want = (want>>1)&0x3FFF | fb<<14
		d.StepSample()
		if got := d.NoiseState(); got != want {
			t.Fatalf("after %d clocks noise = %#04x, want %#04x", i+1, got, want)
		}
	}
	if got := noiseValue(0x4000); got != -16384 {
		t.Fatalf("noiseValue(0x4000) = %d, want -16384", got)
	}
}

// Echo delay: EDL=1 gives a 512 frame ring. A voice plays
// one impulse (the only nonzero source sample sits at
// BRR index 8, after the 5 sample KON delay) and ends.
// FIR coefficient 7 is 0x40, which makes the FIR output
// exactly the newest history frame ((0x40*x)>>6 = x), and
// EFB=0 kills feedback, so the stored burst must come
// back exactly 512 samples after it was written, with
// silence in between.
func TestEchoDelay512(t *testing.T) {
	d, ram := newTestDSP()
	ram[0x1000] = 0x00
	ram[0x1001] = 0x20 // dir entry: start 0x2000
	ram[0x1002] = 0x00
	ram[0x1003] = 0x20 // loop 0x2000
	// BRR block: range 12, filter 0, end+mute; sample 8 is
	// nibble 7 (the impulse), the rest are zero.
	putBlock(ram, 0x2000, 0xC1, [8]byte{0, 0, 0, 0, 0x70, 0, 0, 0})
	d.WriteReg(0x5D, 0x10) // DIR = 0x1000
	d.WriteReg(0x00, 0x7F)
	d.WriteReg(0x01, 0x7F)
	d.WriteReg(0x02, 0x00)
	d.WriteReg(0x03, 0x10) // pitch 0x1000
	d.WriteReg(0x04, 0x00)
	d.WriteReg(0x05, 0x00) // GAIN mode
	d.WriteReg(0x07, 0x7F) // direct gain 0x7F0
	d.WriteReg(0x0C, 0x7F) // MVOLL
	d.WriteReg(0x2C, 0x7F) // MVOLR
	d.WriteReg(0x1C, 0x7F) // EVOLL
	d.WriteReg(0x3C, 0x7F) // EVOLR
	d.WriteReg(0x0D, 0x00) // EFB 0
	d.WriteReg(0x4D, 0x01) // EON voice 0
	d.WriteReg(0x6D, 0x40) // ESA = 0x4000
	d.WriteReg(0x7D, 0x01) // EDL 1: 512 frames
	d.WriteReg(0x7F, 0x40) // FIR7 = 0x40 (exact unity on newest tap)
	d.WriteReg(0x6C, 0x00) // FLG: echo writes on, unmuted
	d.WriteReg(0x4C, 0x01) // KON

	const total = 1400
	out := make([]int16, total)
	for i := range out {
		l, _ := d.StepSample()
		out[i] = l
	}
	first := -1
	for i := 0; i < 400; i++ {
		if out[i] != 0 {
			first = i
			break
		}
	}
	if first < 0 {
		t.Fatal("voice burst never appeared in the output")
	}
	last := -1
	for i := 0; i < 400; i++ {
		if out[i] != 0 {
			last = i
		}
	}
	// Silence between the end of the direct burst and the
	// echo return.
	for i := last + 1; i < first+512; i++ {
		if out[i] != 0 {
			t.Fatalf("output at %d = %d, want silence before the 512 sample echo", i, out[i])
		}
	}
	if out[first+512] == 0 {
		t.Fatalf("no echo at %d (first burst sample %d + 512)", first+512, first)
	}
}

// Pitch accuracy: one voice loops a 16 sample BRR sine
// block (header 0x43: range 4, filter 0, end+loop; the
// nibbles are 0,3,5,6,7,6,5,3,0,-3,-5,-6,-7,-6,-5,-3)
// at pitch 901. One period is 16 source samples, so the
// tone is 32000 * 901 / (4096 * 16) = 440.06 Hz. Count
// positive zero crossings over 32000 output samples.
func TestPitch440(t *testing.T) {
	d, ram := newTestDSP()
	ram[0x0100] = 0x00
	ram[0x0101] = 0x02 // start 0x0200
	ram[0x0102] = 0x00
	ram[0x0103] = 0x02 // loop 0x0200
	putBlock(ram, 0x0200, 0x43, [8]byte{0x03, 0x56, 0x76, 0x53, 0x0D, 0xBA, 0x9A, 0xBD})
	d.WriteReg(0x5D, 0x01) // DIR = 0x0100
	d.WriteReg(0x00, 0x7F)
	d.WriteReg(0x01, 0x7F)
	d.WriteReg(0x02, 0x85) // PITCHL: 901 = 0x385
	d.WriteReg(0x03, 0x03) // PITCHH
	d.WriteReg(0x04, 0x00)
	d.WriteReg(0x05, 0x8F) // ADSR on, DR=0, AR=15 (fast attack, slow decay)
	d.WriteReg(0x06, 0xE0) // SL=7, SR=0
	d.WriteReg(0x0C, 0x7F)
	d.WriteReg(0x2C, 0x7F)
	d.WriteReg(0x6C, 0x20) // FLG: echo writes off
	d.WriteReg(0x4C, 0x01)

	crossings := 0
	prev := int16(0)
	for i := 0; i < 32000; i++ {
		l, _ := d.StepSample()
		if prev < 0 && l >= 0 {
			crossings++
		}
		prev = l
	}
	hz := float64(crossings) // 32000 samples = 1 second
	if math.Abs(hz-440.06) > 0.02*440.06 {
		t.Fatalf("measured %d crossings in 1 s (%.2f Hz), want within 2%% of 440.06", crossings, hz)
	}
	t.Logf("pitch test: %d crossings, %.2f Hz", crossings, hz)
}
