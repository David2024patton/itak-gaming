package psg

import (
	"bytes"
	"math"
	"testing"
)

var segaConfig = Config{LFSRBits: 16, WhiteNoiseTaps: 0x0009}

// The latch byte selects a register (bits 6-5 channel, bit 4 type)
// and loads its low 4 data bits at once; a following data byte
// supplies the high 6 bits of a tone period or replaces the low 4
// bits of a volume.
func TestLatchProtocol(t *testing.T) {
	p := New(3579545, 44100, 1024, segaConfig)

	p.Write(0x8A) // latch tone 0 period, low bits 0xA
	if p.latched != 0 {
		t.Fatalf("latched = %d, want 0", p.latched)
	}
	if p.periods[0] != 0x00A {
		t.Fatalf("periods[0] = %#x, want 0x00a", p.periods[0])
	}
	p.Write(0x3F) // data byte: high 6 bits
	if p.periods[0] != 0x3FA {
		t.Fatalf("periods[0] = %#x, want 0x3fa", p.periods[0])
	}
	p.Write(0x85) // latch again: only the low 4 bits change
	if p.periods[0] != 0x3F5 {
		t.Fatalf("periods[0] = %#x, want 0x3f5", p.periods[0])
	}

	p.Write(0x9B) // latch tone 0 volume = 11
	if p.volumes[0] != 11 {
		t.Fatalf("volumes[0] = %d, want 11", p.volumes[0])
	}
	p.Write(0x36) // data byte replaces the volume's low bits
	if p.volumes[0] != 6 {
		t.Fatalf("volumes[0] = %d, want 6", p.volumes[0])
	}

	p.Write(0xA7) // latch tone 1 period, low bits 7
	if p.periods[1] != 0x007 {
		t.Fatalf("periods[1] = %#x, want 0x007", p.periods[1])
	}

	p.Write(0xF9) // latch noise volume = 9
	if p.volumes[3] != 9 {
		t.Fatalf("volumes[3] = %d, want 9", p.volumes[3])
	}
}

// Writing the noise control register applies the low bits and
// reseeds the shift register, for both latch and data byte writes.
func TestNoiseControlWrite(t *testing.T) {
	p := New(3579545, 44100, 256, segaConfig)
	p.Write(0xE4) // white noise, rate 0
	if p.noiseCtl != 0x04 {
		t.Fatalf("noiseCtl = %#x, want 0x04", p.noiseCtl)
	}
	if p.lfsr != 0x8000 {
		t.Fatalf("lfsr = %#x, want seed 0x8000", p.lfsr)
	}
	p.shiftNoise()
	p.shiftNoise()
	if p.lfsr == 0x8000 {
		t.Fatal("lfsr did not advance")
	}
	p.Write(0x07) // data byte to the latched noise control
	if p.noiseCtl != 0x07 {
		t.Fatalf("noiseCtl = %#x, want 0x07", p.noiseCtl)
	}
	if p.lfsr != 0x8000 {
		t.Fatalf("lfsr = %#x, want reseeded 0x8000", p.lfsr)
	}
}

// With sampleRate == clockFreq every input clock yields one sample,
// which exposes the tone waveform directly: the counter decrements
// once per 16 clocks, so with period 4 the output polarity flips
// every 64 samples.
func TestToneWaveform(t *testing.T) {
	p := New(16384, 16384, 4096, segaConfig)
	p.Write(0x84) // tone 0 period low = 4
	p.Write(0x00) // tone 0 period high = 0
	p.Write(0x90) // tone 0 volume = 0 (loudest)
	p.counters[0] = 4
	p.polarity[0] = true

	if n := p.GenerateSamples(200); n != 200 {
		t.Fatalf("GenerateSamples returned %d, want 200", n)
	}
	buf, n := p.GetBuffer()
	if n != 200 {
		t.Fatalf("buffered %d samples, want 200", n)
	}
	// First toggle at clock 64, second at clock 128. Output is
	// unipolar and normalized: high state = volume/4, low = 0.
	for i := 0; i < 200; i++ {
		want := float32(0.25)
		if i >= 63 && i < 127 || i >= 191 {
			want = 0
		}
		if buf[i] != want {
			t.Fatalf("sample %d = %v, want %v", i, buf[i], want)
		}
	}
}

// A period register of 0 counts through the full 10-bit range:
// the first toggle takes 1024 counter ticks = 16384 input clocks.
func TestPeriodZeroIs1024(t *testing.T) {
	p := New(16384, 16384, 64, segaConfig)
	// periods[0] is 0 and counters[0] starts at 1024 from New.
	p.GenerateSamples(16383)
	if !p.polarity[0] {
		t.Fatal("polarity toggled before 16384 clocks")
	}
	p.GenerateSamples(1)
	if p.polarity[0] {
		t.Fatal("polarity did not toggle at 16384 clocks")
	}
}

// The attenuation table: 2 dB steps from 1.0 down to silence at 15.
// Output is unipolar and normalized by 4: a channel contributes
// table[vol]/4 while its generator is high, nothing while low.
func TestVolumeTable(t *testing.T) {
	p := New(3579545, 44100, 256, segaConfig)
	close := func(got, want float32) bool {
		return math.Abs(float64(got-want)) < 1e-4
	}
	// Other channels sit at attenuation 15 (amplitude 0), so the
	// output is tone 0 alone.
	p.volumes[0] = 0
	p.polarity[0] = true
	if got := p.output(); got != 0.25 {
		t.Fatalf("output at volume 0 = %v, want 0.25", got)
	}
	for vol, want := range map[uint8]float32{
		1: 0.1986, 2: 0.1578, 5: 0.0791, 10: 0.025, 14: 0.0100, 15: 0.0,
	} {
		p.volumes[0] = vol
		if got := p.output(); !close(got, want) {
			t.Fatalf("output at volume %d = %v, want %v", vol, got, want)
		}
	}
	p.volumes[0] = 0
	p.polarity[0] = false
	if got := p.output(); got != 0 {
		t.Fatalf("output in low state = %v, want 0", got)
	}
	// All four channels loud and in the high state sum to 1.0.
	p.polarity = [3]bool{true, true, true}
	p.volumes = [4]uint8{0, 0, 0, 0}
	p.lfsr |= 1
	if got := p.output(); got != 1.0 {
		t.Fatalf("full sum = %v, want 1.0", got)
	}
}

// The white-noise LFSR sequence for the Sega configuration (16-bit,
// taps on bits 0 and 3), worked out from the documented rule:
// feedback = parity(lfsr & 0x0009), shifting right, seed 0x8000.
func TestNoiseLFSRSequenceWhite(t *testing.T) {
	p := New(3579545, 44100, 256, segaConfig)
	p.Write(0xE4) // white noise, rate 0
	wantStates := []uint32{
		0x8000, 0x4000, 0x2000, 0x1000, 0x0800, 0x0400, 0x0200, 0x0100,
		0x0080, 0x0040, 0x0020, 0x0010, 0x0008, 0x8004, 0x4002, 0x2001,
		0x9000, 0x4800, 0x2400, 0x1200, 0x0900, 0x0480, 0x0240, 0x0120,
		0x0090, 0x0048, 0x8024, 0x4012, 0x2009, 0x1004, 0x0802, 0x0401,
		0x8200,
	}
	// Output bit (bit 0) read at each state, before shifting.
	wantBits := "0000000000000001" + "0000000000001001" + "0"
	gotBits := make([]byte, 0, len(wantStates))
	for i, want := range wantStates {
		if p.lfsr != want {
			t.Fatalf("state %d: lfsr = %#06x, want %#06x", i, p.lfsr, want)
		}
		if p.lfsr&1 != 0 {
			gotBits = append(gotBits, '1')
		} else {
			gotBits = append(gotBits, '0')
		}
		p.shiftNoise()
	}
	if string(gotBits) != wantBits {
		t.Fatalf("output bits = %s, want %s", gotBits, wantBits)
	}
}

// In periodic mode the seed bit rotates through the register: the
// output is high for exactly one shift in every LFSRBits shifts and
// the register returns to the seed after a full rotation.
func TestNoisePeriodic(t *testing.T) {
	p := New(3579545, 44100, 256, segaConfig)
	p.Write(0xE0) // periodic noise, rate 0
	ones := 0
	for i := 0; i < 16; i++ {
		if p.lfsr&1 != 0 {
			ones++
		}
		p.shiftNoise()
	}
	if ones != 1 {
		t.Fatalf("output high %d times in 16 shifts, want 1", ones)
	}
	if p.lfsr != 0x8000 {
		t.Fatalf("lfsr after full rotation = %#06x, want 0x8000", p.lfsr)
	}
}

// At fixed rate 0 the rate source is a 512-clock square wave, so
// shifts land on its falling edges: clocks 256, 768, ...
func TestNoiseFixedRateTiming(t *testing.T) {
	p := New(3579545, 44100, 8192, segaConfig)
	p.Write(0xE4) // white noise, rate 0
	p.GenerateSamples(255)
	if p.lfsr != 0x8000 {
		t.Fatalf("after 255 clocks lfsr = %#06x, want 0x8000", p.lfsr)
	}
	p.GenerateSamples(1)
	if p.lfsr != 0x4000 {
		t.Fatalf("after 256 clocks lfsr = %#06x, want 0x4000", p.lfsr)
	}
	p.GenerateSamples(511)
	if p.lfsr != 0x4000 {
		t.Fatalf("after 767 clocks lfsr = %#06x, want 0x4000", p.lfsr)
	}
	p.GenerateSamples(1)
	if p.lfsr != 0x2000 {
		t.Fatalf("after 768 clocks lfsr = %#06x, want 0x2000", p.lfsr)
	}
}

// Rate 3 slaves the noise shift to tone channel 3's output: shifts
// happen on its falling edges only.
func TestNoiseSlavedToTone3(t *testing.T) {
	p := New(3579545, 44100, 256, segaConfig)
	p.Write(0xC1) // tone 3 (channel 2) period low = 1
	p.Write(0x00) // period high = 0
	p.counters[2] = 1
	p.polarity[2] = true
	p.Write(0xE7) // white noise, rate 3 (slaved)
	if p.lfsr != 0x8000 {
		t.Fatalf("lfsr = %#06x, want seed 0x8000", p.lfsr)
	}
	// Tone 3 toggles every 16 clocks; falling edges at 16, 48, ...
	p.GenerateSamples(16)
	if p.lfsr != 0x4000 {
		t.Fatalf("after falling edge lfsr = %#06x, want 0x4000", p.lfsr)
	}
	p.GenerateSamples(16)
	if p.lfsr != 0x4000 {
		t.Fatalf("after rising edge lfsr = %#06x, want 0x4000", p.lfsr)
	}
	p.GenerateSamples(16)
	if p.lfsr != 0x2000 {
		t.Fatalf("after second falling edge lfsr = %#06x, want 0x2000", p.lfsr)
	}
}

// Config defaults: zero LFSRBits selects the 15-bit TI register
// (seed 0x4000) and zero taps select the TI taps 0x0003; the Sega
// config seeds 0x8000.
func TestConfigDefaults(t *testing.T) {
	p := New(0, 0, 0, Config{})
	if p.clockFreq != 3579545 || p.sampleRate != 44100 {
		t.Fatalf("rate defaults = %d/%d, want 3579545/44100", p.clockFreq, p.sampleRate)
	}
	if len(p.buffer) != 4096 {
		t.Fatalf("buffer size = %d, want 4096", len(p.buffer))
	}
	if p.lfsrBits != 15 || p.taps != 0x0003 {
		t.Fatalf("config defaults = %d bits, taps %#x; want 15, 0x0003", p.lfsrBits, p.taps)
	}
	p.Write(0xE4)
	if p.lfsr != 0x4000 {
		t.Fatalf("15-bit seed = %#06x, want 0x4000", p.lfsr)
	}

	q := New(3579545, 44100, 128, segaConfig)
	q.Write(0xE4)
	if q.lfsr != 0x8000 {
		t.Fatalf("16-bit seed = %#06x, want 0x8000", q.lfsr)
	}
}

// The fractional accumulator must be exact over the long run: one
// second of input clocks produces exactly one second of samples.
func TestSampleRateExact(t *testing.T) {
	p := New(3579545, 44100, 100000, segaConfig)
	if n := p.GenerateSamples(3579545); n != 44100 {
		t.Fatalf("generated %d samples for 1s of clocks, want 44100", n)
	}
	if p.samplePhase != 0 {
		t.Fatalf("residual phase = %d, want 0", p.samplePhase)
	}
}

// Overflow policy: chip time advances in full, samples beyond the
// buffer capacity are discarded, and the return value counts only
// the samples actually stored.
func TestBufferOverflow(t *testing.T) {
	p := New(48000, 48000, 8, segaConfig) // one sample per clock
	if n := p.GenerateSamples(100); n != 8 {
		t.Fatalf("stored %d samples, want 8", n)
	}
	if _, n := p.GetBuffer(); n != 8 {
		t.Fatalf("buffered %d samples, want 8", n)
	}
	if n := p.GenerateSamples(100); n != 0 {
		t.Fatalf("stored %d samples into a full buffer, want 0", n)
	}
	// Time still advanced: 200 clocks = 12 tone ticks (at 16, 32,
	// ...) plus 8 more clocks, so the counter dropped by 12.
	if p.counters[0] != 1024-12 {
		t.Fatalf("counter = %d, want %d", p.counters[0], 1012)
	}
	p.ResetBuffer()
	if n := p.GenerateSamples(3); n != 3 {
		t.Fatalf("stored %d samples after reset, want 3", n)
	}
}

// A state captured mid-stream must restore exactly: re-serializing
// gives identical bytes and the restored chip generates identical
// samples going forward.
func TestSerializeRoundTrip(t *testing.T) {
	if SerializeSize != 41 {
		t.Fatalf("SerializeSize = %d, want 41", SerializeSize)
	}
	p := New(3579545, 44100, 2048, segaConfig)
	p.Write(0x8C)            // tone 0 period low = 0xC
	p.Write(0x2B)            // tone 0 period high = 0x2B -> period 0x2BC
	p.Write(0x93)            // tone 0 volume = 3
	p.Write(0xA4)            // tone 1 period low = 4
	p.Write(0x06)            // tone 1 period high = 6
	p.Write(0xE5)            // white noise, rate 1
	p.Write(0xF5)            // noise volume = 5
	p.GenerateSamples(12345) // odd counts: fractional state everywhere
	p.ResetBuffer()

	state := make([]byte, SerializeSize)
	if err := p.Serialize(state); err != nil {
		t.Fatal(err)
	}
	q := New(3579545, 44100, 2048, segaConfig)
	if err := q.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	restored := make([]byte, SerializeSize)
	if err := q.Serialize(restored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(state, restored) {
		t.Fatalf("state mismatch after round trip:\n%x\n%x", state, restored)
	}

	np := p.GenerateSamples(5000)
	nq := q.GenerateSamples(5000)
	if np != nq {
		t.Fatalf("sample counts diverge: %d vs %d", np, nq)
	}
	pb, _ := p.GetBuffer()
	qb, _ := q.GetBuffer()
	for i := 0; i < np; i++ {
		if pb[i] != qb[i] {
			t.Fatalf("sample %d diverges: %v vs %v", i, pb[i], qb[i])
		}
	}

	if err := p.Serialize(make([]byte, SerializeSize-1)); err == nil {
		t.Fatal("Serialize accepted a short buffer")
	}
	if err := q.Deserialize(make([]byte, SerializeSize-1)); err == nil {
		t.Fatal("Deserialize accepted a short buffer")
	}
	bad := bytes.Clone(state)
	bad[0] = 0xFF
	if err := q.Deserialize(bad); err == nil {
		t.Fatal("Deserialize accepted an unknown version")
	}
}
