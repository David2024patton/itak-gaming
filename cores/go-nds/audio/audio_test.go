package audio_test

import (
	"math"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-nds/audio"
)

// fakeMem is a byte backed Memory for the tests.
type fakeMem struct {
	b []byte
}

func newFakeMem() *fakeMem {
	return &fakeMem{b: make([]byte, 1<<20)}
}

func (m *fakeMem) Read8(addr uint32) uint8 {
	if int(addr) >= len(m.b) {
		return 0
	}
	return m.b[addr]
}

func (m *fakeMem) Read16(addr uint32) uint16 {
	return uint16(m.Read8(addr)) | uint16(m.Read8(addr+1))<<8
}

func (m *fakeMem) Write8(addr uint32, v uint8) {
	if int(addr) < len(m.b) {
		m.b[addr] = v
	}
}

func (m *fakeMem) Write16(addr uint32, v uint16) {
	m.Write8(addr, uint8(v))
	m.Write8(addr+1, uint8(v>>8))
}

func put(t *testing.T, s *audio.Sound, off uint32, size int, val uint32) {
	t.Helper()
	if !s.WriteIO(off, size, val) {
		t.Fatalf("WriteIO offset %#x not claimed", off)
	}
}

func get(t *testing.T, s *audio.Sound, off uint32, size int) uint32 {
	t.Helper()
	v, ok := s.ReadIO(off, size)
	if !ok {
		t.Fatalf("ReadIO offset %#x not claimed", off)
	}
	return v
}

func masterOn(t *testing.T, s *audio.Sound) {
	t.Helper()
	put(t, s, 0x500, 4, 0x8000|127)
}

// chanRegs writes the address, timer, loop point and length
// registers of a channel. Call it before the control write
// that starts the channel, because those registers latch at
// start.
func chanRegs(t *testing.T, s *audio.Sound, ch int, sad uint32, divisor uint32, pnt uint32, words uint32) {
	t.Helper()
	base := uint32(0x400 + ch*16)
	put(t, s, base+4, 4, sad)
	put(t, s, base+8, 2, 65536-divisor)
	put(t, s, base+0xA, 2, pnt)
	put(t, s, base+0xC, 4, words)
}

func cntValue(pan uint32, repeat uint32, format uint32) uint32 {
	return 127 | pan<<16 | repeat<<27 | format<<29 | 1<<31
}

// fillSine16 writes one period of a PCM16 sine of the given
// number of samples and amplitude into memory.
func fillSine16(m *fakeMem, base uint32, samples int, amp float64) {
	for i := 0; i < samples; i++ {
		v := int16(amp * math.Sin(2*math.Pi*float64(i)/float64(samples)))
		m.Write16(base+uint32(i)*2, uint16(v))
	}
}

func leftChannel(stereo []int16) []int16 {
	out := make([]int16, len(stereo)/2)
	for i := range out {
		out[i] = stereo[2*i]
	}
	return out
}

// Gate 1: a PCM16 loop tone comes out at the expected pitch
// and level.
func TestPCM16LoopTone(t *testing.T) {
	m := newFakeMem()
	const base = 0x1000
	const period = 256
	fillSine16(m, base, period, 30000)
	s := audio.New(m)
	masterOn(t, s)
	chanRegs(t, s, 0, base, 511, 0, period/2)
	put(t, s, 0x400, 4, cntValue(64, 1, 1))
	s.Advance(16756991)
	stereo := s.Drain()
	if len(stereo) != 32768*2 {
		t.Fatalf("drained %d samples, want %d", len(stereo), 32768*2)
	}
	left := leftChannel(stereo)
	rate := 16756991.0 / 511.0
	wantFreq := rate / period
	cross := 0
	for i := 1; i < len(left); i++ {
		if left[i-1] < 0 && left[i] >= 0 {
			cross++
		}
	}
	gotFreq := float64(cross)
	if math.Abs(gotFreq-wantFreq)/wantFreq > 0.02 {
		t.Fatalf("frequency %v Hz from %d crossings, want %v Hz within 2 percent", gotFreq, cross, wantFreq)
	}
	var peak int
	var sumSq float64
	for _, v := range left {
		a := int(v)
		if a < 0 {
			a = -a
		}
		if a > peak {
			peak = a
		}
		sumSq += float64(v) * float64(v)
	}
	rms := math.Sqrt(sumSq / float64(len(left)))
	if peak < 10000 || peak > 20000 {
		t.Fatalf("peak %d, want between 10000 and 20000", peak)
	}
	if rms < 8000 || rms > 14000 {
		t.Fatalf("rms %v, want between 8000 and 14000", rms)
	}
}

// Gate 2: a PSG square on channel 8 sounds at rate over 8.
func TestPSGPitch(t *testing.T) {
	m := newFakeMem()
	s := audio.New(m)
	masterOn(t, s)
	const ch = 8
	base := uint32(0x400 + ch*16)
	chanRegs(t, s, ch, 0, 511, 0, 0)
	cnt := uint32(127) | 64<<16 | 3<<24 | 3<<29 | 1<<31
	put(t, s, base, 4, cnt)
	s.Advance(16756991)
	left := leftChannel(s.Drain())
	if len(left) != 32768 {
		t.Fatalf("drained %d left samples, want 32768", len(left))
	}
	cross := 0
	for i := 1; i < len(left); i++ {
		if left[i-1] < 0 && left[i] >= 0 {
			cross++
		}
	}
	wantFreq := (16756991.0 / 511.0) / 8.0
	gotFreq := float64(cross)
	if math.Abs(gotFreq-wantFreq)/wantFreq > 0.02 {
		t.Fatalf("frequency %v Hz from %d crossings, want %v Hz within 2 percent", gotFreq, cross, wantFreq)
	}
	var peak int
	for _, v := range left {
		a := int(v)
		if a < 0 {
			a = -a
		}
		if a > peak {
			peak = a
		}
	}
	if peak < 10000 {
		t.Fatalf("peak %d, want at least 10000", peak)
	}
}

func runPan(t *testing.T, pan uint32) []int16 {
	t.Helper()
	m := newFakeMem()
	const base = 0x2000
	for i := 0; i < 64; i++ {
		m.Write16(base+uint32(i)*2, uint16(int16(20000)))
	}
	s := audio.New(m)
	masterOn(t, s)
	chanRegs(t, s, 0, base, 511, 0, 32)
	put(t, s, 0x400, 4, cntValue(pan, 1, 1))
	s.Advance(200000)
	return s.Drain()
}

// Gate 3: pan hard left silences the right side and pan hard
// right silences the left side, on a DC signal.
func TestPan(t *testing.T) {
	stereo := runPan(t, 0)
	if len(stereo) == 0 {
		t.Fatal("no output for pan 0")
	}
	for i := 0; i < len(stereo); i += 2 {
		if stereo[i] == 0 {
			t.Fatalf("left sample %d is zero with pan 0", i/2)
		}
		if stereo[i+1] != 0 {
			t.Fatalf("right sample %d is %d with pan 0, want 0", i/2, stereo[i+1])
		}
	}
	stereo = runPan(t, 127)
	for i := 0; i < len(stereo); i += 2 {
		if stereo[i] != 0 {
			t.Fatalf("left sample %d is %d with pan 127, want 0", i/2, stereo[i])
		}
		if stereo[i+1] == 0 {
			t.Fatalf("right sample %d is zero with pan 127", i/2)
		}
	}
}

// IMA tables duplicated in the test so the reference encoder
// below stands on the published tables alone.
var testStep = [...]int32{
	7, 8, 9, 10, 11, 12, 13, 14, 16, 17,
	19, 21, 23, 25, 28, 31, 34, 37, 41, 45,
	50, 55, 60, 66, 73, 80, 88, 97, 107, 118,
	130, 143, 157, 173, 190, 209, 230, 253, 279, 307,
	337, 371, 408, 449, 494, 544, 598, 658, 724, 796,
	876, 963, 1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066,
	2272, 2499, 2749, 3024, 3327, 3660, 4026, 4428, 4871, 5358,
	5894, 6484, 7132, 7845, 8630, 9493, 10442, 11487, 12635, 13899,
	15289, 16818, 18500, 20350, 22385, 24623, 27086, 29794, 32767,
}

var testIndex = [...]int32{-1, -1, -1, -1, 2, 4, 6, 8}

func decodeNibble(pred int32, idx int32, nib uint32) (int32, int32) {
	step := testStep[idx]
	diff := step >> 3
	if nib&1 != 0 {
		diff += step >> 2
	}
	if nib&2 != 0 {
		diff += step >> 1
	}
	if nib&4 != 0 {
		diff += step
	}
	if nib&8 != 0 {
		pred -= diff
	} else {
		pred += diff
	}
	if pred > 32767 {
		pred = 32767
	}
	if pred < -32768 {
		pred = -32768
	}
	idx += testIndex[nib&7]
	if idx < 0 {
		idx = 0
	}
	if idx > 88 {
		idx = 88
	}
	return pred, idx
}

// Gate 4: ADPCM data encoded by a reference encoder in the
// test decodes through the package close to the source sine.
func TestADPCM(t *testing.T) {
	if len(testStep) != 89 {
		t.Fatalf("step table has %d entries, want 89", len(testStep))
	}
	m := newFakeMem()
	const base = 0x3000
	const period = 128
	const nsamp = 2048
	const amp = 12000.0
	pred := int32(0)
	idx := int32(30)
	m.Write16(base, 0)
	m.Write8(base+2, uint8(idx))
	m.Write8(base+3, 0)
	for i := 0; i < nsamp; i++ {
		target := int32(amp * math.Sin(2*math.Pi*float64(i)/period))
		best := uint32(0)
		var bestPred, bestIdx int32
		bestErr := int32(-1)
		var nib uint32
		for nib = 0; nib < 16; nib++ {
			p, ix := decodeNibble(pred, idx, nib)
			err := p - target
			if err < 0 {
				err = -err
			}
			if bestErr < 0 || err < bestErr {
				bestErr = err
				best = nib
				bestPred = p
				bestIdx = ix
			}
		}
		pred, idx = bestPred, bestIdx
		addr := base + 4 + uint32(i)/2
		if i%2 == 0 {
			m.Write8(addr, uint8(best))
		} else {
			m.Write8(addr, m.Read8(addr)|uint8(best)<<4)
		}
	}

	s := audio.New(m)
	masterOn(t, s)
	chanRegs(t, s, 0, base, 511, 0, 1+nsamp/8)
	put(t, s, 0x400, 4, cntValue(64, 1, 2))
	s.Advance(16756991)
	left := leftChannel(s.Drain())
	if len(left) < 20000 {
		t.Fatalf("drained %d left samples, want at least 20000", len(left))
	}
	rate := 16756991.0 / 511.0
	var maxErr, sumSq float64
	n := 0
	for out := 1000; out < 20000; out++ {
		pos := float64(out) * rate / 32768.0
		want := amp * math.Sin(2*math.Pi*pos/period)
		est := float64(left[out]) * 128.0 / 63.0
		err := math.Abs(est - want)
		if err > maxErr {
			maxErr = err
		}
		d := est - want
		sumSq += d * d
		n++
	}
	rms := math.Sqrt(sumSq / float64(n))
	t.Logf("ADPCM max error %v, rms error %v over %d samples", maxErr, rms, n)
	if maxErr >= 6000 {
		t.Fatalf("max error %v, want below 6000", maxErr)
	}
	if rms > 2500 {
		t.Fatalf("rms error %v, want at most 2500", rms)
	}
	var peak int
	for _, v := range left {
		a := int(v)
		if a < 0 {
			a = -a
		}
		if a > peak {
			peak = a
		}
	}
	if peak < 2000 {
		t.Fatalf("peak %d, ADPCM output looks silent", peak)
	}
}

// Gate 5: capture 0 on the mixer source writes exactly the
// samples that Drain returns for the same interval.
func TestCaptureMixerExact(t *testing.T) {
	m := newFakeMem()
	const base = 0x1000
	const period = 256
	fillSine16(m, base, period, 30000)
	s := audio.New(m)
	masterOn(t, s)
	chanRegs(t, s, 0, base, 511, 0, period/2)
	put(t, s, 0x400, 4, cntValue(64, 1, 1))

	const dad = 0x8000
	const capWords = 64
	put(t, s, 0x510, 4, dad)
	put(t, s, 0x514, 2, capWords)
	// One shot, PCM16, mixer source, start.
	put(t, s, 0x508, 1, 0x80|0x04)
	s.Advance(200000)
	stereo := s.Drain()
	nsamp := capWords * 2
	if len(stereo)/2 < nsamp {
		t.Fatalf("only %d output samples, capture needs %d", len(stereo)/2, nsamp)
	}
	for i := 0; i < nsamp; i++ {
		got := int16(m.Read16(dad + uint32(i)*2))
		want := stereo[2*i]
		if got != want {
			t.Fatalf("captured sample %d is %d, drained left sample is %d", i, got, want)
		}
	}
	if v := get(t, s, 0x508, 1); v&0x80 != 0 {
		t.Fatalf("capture still busy after one shot, cnt %#x", v)
	}
}

// Gate 6: a serialized scene continues bit identically in a
// fresh unit, including the undrained output buffer.
func TestStateRoundTrip(t *testing.T) {
	m := newFakeMem()
	const base = 0x1000
	fillSine16(m, base, 256, 30000)
	build := func() *audio.Sound {
		s := audio.New(m)
		masterOn(t, s)
		chanRegs(t, s, 0, base, 511, 0, 128)
		put(t, s, 0x400, 4, cntValue(64, 1, 1))
		const ch = 9
		b9 := uint32(0x400 + ch*16)
		chanRegs(t, s, ch, 0, 700, 0, 0)
		cnt := uint32(100) | 90<<16 | 5<<24 | 3<<29 | 1<<31
		put(t, s, b9, 4, cnt)
		return s
	}
	orig := build()
	orig.Advance(5000000)
	blob := make([]byte, audio.StateSize)
	if n := orig.Serialize(blob); n != audio.StateSize {
		t.Fatalf("Serialize returned %d, want %d", n, audio.StateSize)
	}
	fresh := audio.New(m)
	if n := fresh.Deserialize(blob); n != audio.StateSize {
		t.Fatalf("Deserialize returned %d, want %d", n, audio.StateSize)
	}
	orig.Advance(3000000)
	fresh.Advance(3000000)
	a := orig.Drain()
	b := fresh.Drain()
	if len(a) != len(b) {
		t.Fatalf("drain lengths differ: %d and %d", len(a), len(b))
	}
	if len(a) == 0 {
		t.Fatal("scene produced no output")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("sample %d differs: %d and %d", i, a[i], b[i])
		}
	}
	blobA := make([]byte, audio.StateSize)
	blobB := make([]byte, audio.StateSize)
	orig.Serialize(blobA)
	fresh.Serialize(blobB)
	for i := range blobA {
		if blobA[i] != blobB[i] {
			t.Fatalf("re-serialized byte %d differs: %d and %d", i, blobA[i], blobB[i])
		}
	}
}

// Gate 7: register readback, including the busy bit.
func TestRegisterReadback(t *testing.T) {
	m := newFakeMem()
	const base = 0x1000
	fillSine16(m, base, 256, 20000)
	s := audio.New(m)
	if _, ok := s.ReadIO(0x3FC, 4); ok {
		t.Fatal("ReadIO below 0x400 claimed")
	}
	if _, ok := s.ReadIO(0x520, 4); ok {
		t.Fatal("ReadIO at 0x520 claimed")
	}
	if s.WriteIO(0x520, 4, 0) {
		t.Fatal("WriteIO at 0x520 claimed")
	}
	masterOn(t, s)
	if v := get(t, s, 0x500, 4); v != 0x8000|127 {
		t.Fatalf("SOUNDCNT readback %#x, want %#x", v, 0x8000|127)
	}
	if v := get(t, s, 0x504, 4); v != 0x200 {
		t.Fatalf("SOUNDBIAS power on readback %#x, want 0x200", v)
	}
	put(t, s, 0x504, 2, 0x123)
	if v := get(t, s, 0x504, 4); v != 0x123 {
		t.Fatalf("SOUNDBIAS readback %#x, want 0x123", v)
	}
	put(t, s, 0x504, 2, 0x200)

	chanRegs(t, s, 0, base, 511, 0, 128)
	if v := get(t, s, 0x404, 4); v != base {
		t.Fatalf("SAD readback %#x, want %#x", v, base)
	}
	put(t, s, 0x400, 4, cntValue(64, 1, 1))
	if v := get(t, s, 0x400, 4); v&(1<<31) == 0 {
		t.Fatalf("CNT busy bit clear while playing, cnt %#x", v)
	}
	s.Advance(100000)
	if v := get(t, s, 0x400, 4); v&(1<<31) == 0 {
		t.Fatalf("CNT busy bit clear mid loop, cnt %#x", v)
	}
	put(t, s, 0x400, 4, 0)
	if v := get(t, s, 0x400, 4); v&(1<<31) != 0 {
		t.Fatalf("CNT busy bit set after stop, cnt %#x", v)
	}
}
