package sound

import (
	"math"
	"testing"
)

// Register addresses in the APU's own map.
const (
	nr10 = 0xFF10
	nr11 = 0xFF11
	nr12 = 0xFF12
	nr13 = 0xFF13
	nr14 = 0xFF14
	nr21 = 0xFF16
	nr22 = 0xFF17
	nr23 = 0xFF18
	nr24 = 0xFF19
	nr30 = 0xFF1A
	nr31 = 0xFF1B
	nr32 = 0xFF1C
	nr33 = 0xFF1D
	nr34 = 0xFF1E
	nr41 = 0xFF20
	nr42 = 0xFF21
	nr43 = 0xFF22
	nr44 = 0xFF23
	nr50 = 0xFF24
	nr51 = 0xFF25
	nr52 = 0xFF26
)

// powered returns an APU with the master power on and full volume.
func powered() *APU {
	s := New()
	s.Write(nr52, 0x80)
	s.Write(nr50, 0x77)
	s.Write(nr51, 0xFF)
	return s
}

// freqReg converts a frequency in Hz to the 11-bit period
// register: tone = clock / (32 * (2048 - reg)).
func freqReg(hz float64) int {
	return 2048 - int(math.Round(4194304/(32*hz)))
}

// measureFreq estimates a channel's dominant frequency by counting
// rising zero crossings of its debug waveform over dots samples.
func measureFreq(s *APU, ch int, dots int) float64 {
	crossings := 0
	prev := s.DebugChannelSample(ch)
	for i := 0; i < dots; i++ {
		s.Tick(1)
		v := s.DebugChannelSample(ch)
		if prev <= 0 && v > 0 {
			crossings++
		}
		prev = v
	}
	return float64(crossings) * 4194304 / float64(dots)
}

// triggerSquare2 programs square channel 2 (index 1) and triggers it.
func triggerSquare2(s *APU, reg int, duty uint8) {
	s.Write(nr21, duty<<6)
	s.Write(nr22, 0xF0) // volume 15, envelope off
	s.Write(nr23, uint8(reg))
	s.Write(nr24, 0x80|uint8(reg>>8))
}

func TestSquareA4(t *testing.T) {
	s := powered()
	reg := freqReg(440)
	triggerSquare2(s, reg, 2) // 50% duty
	if !s.DebugEnabled(1) {
		t.Fatal("square 2 not enabled after trigger")
	}
	got := measureFreq(s, 1, 4194304/4) // a quarter second
	cents := 1200 * math.Log2(got/440)
	if math.Abs(cents) > 25 {
		t.Fatalf("square A4 measured %.2f Hz (%.1f cents off)", got, cents)
	}
	t.Logf("square A4: reg=%d measured %.2f Hz (%.1f cents)", reg, got, cents)
}

func TestSweepMovesFrequency(t *testing.T) {
	s := powered()
	reg := freqReg(128)
	// Square 1: sweep pace 1, up, slope 2: each sweep clock
	// replaces the register with reg + reg>>2. Sweep clocks
	// land on sequencer steps 2 and 6, so 8 sequencer steps
	// apply two calculations: 1024 -> 1280 -> 1600. (Each clock
	// also runs a second overflow-check calculation that does
	// not store; here it peaks at 2000, so the channel lives.)
	s.Write(nr10, 0x12)
	s.Write(nr11, 2<<6)
	s.Write(nr12, 0xF0)
	s.Write(nr13, uint8(reg))
	s.Write(nr14, 0x80|uint8(reg>>8))
	for i := 0; i < 8; i++ {
		s.StepFrameSequencer()
	}
	if !s.DebugEnabled(0) {
		t.Fatal("channel died during a non-overflowing sweep")
	}
	got := measureFreq(s, 0, 4194304/4)
	want := 4194304 / (32 * float64(2048-1600))
	if math.Abs(got-want)/want > 0.05 {
		t.Fatalf("swept square measured %.1f Hz, want about %.1f Hz", got, want)
	}
	t.Logf("sweep: 128 Hz swept to %.1f Hz (model %.1f)", got, want)
}

func TestSweepOverflowDisables(t *testing.T) {
	s := powered()
	reg := 2000 // sweeping up by reg>>1 overflows 2047 at once
	s.Write(nr10, 0x11)
	s.Write(nr11, 2<<6)
	s.Write(nr12, 0xF0)
	s.Write(nr13, uint8(reg))
	s.Write(nr14, 0x80|uint8(reg>>8))
	if s.DebugEnabled(0) {
		t.Fatal("channel with an overflowing sweep should be disabled at trigger")
	}
}

func TestEnvelopeDecaysOnSchedule(t *testing.T) {
	s := powered()
	s.Write(nr21, 2<<6)
	s.Write(nr22, 0xF1) // volume 15, decreasing, pace 1
	reg := freqReg(440)
	s.Write(nr23, uint8(reg))
	s.Write(nr24, 0x80|uint8(reg>>8))
	peak := func() float64 {
		m := 0.0
		for i := 0; i < 20000; i++ {
			s.Tick(1)
			if v := s.DebugChannelSample(1); math.Abs(v) > m {
				m = math.Abs(v)
			}
		}
		return m
	}
	p0 := peak() // volume 15: full scale
	// Envelope clocks land on sequencer step 7, one per 8 steps
	// at pace 1 the volume drops every envelope clock.
	for i := 0; i < 8; i++ {
		s.StepFrameSequencer()
	}
	p1 := peak() // volume 14
	for i := 0; i < 7*8; i++ {
		s.StepFrameSequencer()
	}
	p8 := peak() // volume 7 after 8 envelope clocks total
	if !(p0 > p1 && p1 > p8) {
		t.Fatalf("envelope did not decay stepwise: %.3f -> %.3f -> %.3f", p0, p1, p8)
	}
	if want := p0 * 7 / 15; math.Abs(p8-want) > 0.05*p0 {
		t.Fatalf("after 8 envelope clocks amplitude %.3f, want about %.3f (vol 7/15)", p8, want)
	}
}

func TestLengthCounterStopsChannel(t *testing.T) {
	s := powered()
	// Length load 60 gives a counter of 4; length enabled.
	s.Write(nr21, 2<<6|60)
	s.Write(nr22, 0xF0)
	reg := freqReg(440)
	s.Write(nr23, uint8(reg))
	s.Write(nr24, 0xC0|uint8(reg>>8)) // trigger + length enable
	n0 := s.DebugLength(1)
	if n0 < 3 || n0 > 4 {
		t.Fatalf("length counter after trigger = %d, want 4 (or 3 if the enable edge clocked it)", n0)
	}
	drops := 0
	prev := n0
	disabledAt := -1
	for i := 0; i < 32; i++ {
		s.StepFrameSequencer()
		l := s.DebugLength(1)
		if l < prev {
			drops++
			prev = l
		}
		if !s.DebugEnabled(1) && disabledAt < 0 {
			disabledAt = drops
		}
	}
	if disabledAt != n0 {
		t.Fatalf("channel disabled after %d length clocks, want %d", disabledAt, n0)
	}
}

func TestWaveChannelPlaysTable(t *testing.T) {
	s := powered()
	// Wave RAM: first half loud (0xF), second half silent.
	for i := 0; i < 8; i++ {
		s.Write(0xFF30+uint16(i), 0xFF)
	}
	for i := 8; i < 16; i++ {
		s.Write(0xFF30+uint16(i), 0x00)
	}
	s.Write(nr30, 0x80) // DAC on
	s.Write(nr31, 0)    // length 256
	s.Write(nr32, 0x20) // volume 100%
	reg := freqReg(220)
	s.Write(nr33, uint8(reg))
	s.Write(nr34, 0x80|uint8(reg>>8))
	if !s.DebugEnabled(2) {
		t.Fatal("wave channel not enabled after trigger")
	}
	// The wave position advances one sample per (2048-reg)*2
	// dots; over one full table cycle the output must show both
	// the loud half and the silent half.
	loud, silent := false, false
	period := (2048 - reg) * 2
	for i := 0; i < period*32; i++ {
		s.Tick(1)
		v := s.DebugChannelSample(2)
		if v > 0.2 {
			loud = true
		}
		if v < -0.2 {
			silent = true
		}
	}
	if !loud || !silent {
		t.Fatalf("wave output did not follow the table: loud=%v silent=%v", loud, silent)
	}
	// DMG quirk: wave RAM reads return 0xFF while the channel plays.
	if got := s.Read(0xFF30); got != 0xFF {
		t.Fatalf("wave RAM read while playing = 0x%02X, want 0xFF", got)
	}
}

func TestNoiseChannelRuns(t *testing.T) {
	run := func(wide bool) []float64 {
		s := powered()
		s.Write(nr41, 0)
		s.Write(nr42, 0xF0)
		v := uint8(0x03) // divisor 3, shift 0: period 64 dots-ish
		if wide {
			v |= 0x08
		}
		s.Write(nr43, v)
		s.Write(nr44, 0x80)
		if !s.DebugEnabled(3) {
			t.Fatal("noise channel not enabled after trigger")
		}
		out := make([]float64, 0, 4096)
		for i := 0; i < 4096; i++ {
			s.Tick(16)
			out = append(out, s.DebugChannelSample(3))
		}
		return out
	}
	a, b := run(false), run(true)
	pos, neg := false, false
	for _, v := range a {
		if v > 0 {
			pos = true
		}
		if v < 0 {
			neg = true
		}
	}
	if !pos || !neg {
		t.Fatal("noise output never toggles")
	}
	diff := false
	for i := range a {
		if a[i] != b[i] {
			diff = true
		}
	}
	if !diff {
		t.Fatal("7-bit LFSR mode produces the same stream as 15-bit")
	}
}

func TestPowerOffClearsRegistersKeepsLengths(t *testing.T) {
	s := powered()
	s.Write(nr21, 2<<6|20) // length counter becomes 44
	s.Write(nr22, 0xF0)
	reg := freqReg(440)
	s.Write(nr23, uint8(reg))
	s.Write(nr24, 0x80|uint8(reg>>8))
	s.Write(nr52, 0x00) // power off
	if got := s.Read(nr52) & 0x0F; got != 0 {
		t.Fatalf("NR52 channel bits after power off = 0x%X, want 0", got)
	}
	if got := s.DebugLength(1); got != 44 {
		t.Fatalf("length counter after power off = %d, want 44 (lengths survive power cycles)", got)
	}
	// Register writes are ignored while off: an NR21 write now
	// must not move the length counter.
	s.Write(nr52, 0x00)
	s.Write(nr21, 63)
	if got := s.DebugLength(1); got != 44 {
		t.Fatalf("length counter after off-state NR21 write = %d, want 44 (write ignored)", got)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	s := powered()
	triggerSquare2(s, freqReg(330), 2)
	s.Write(nr10, 0x23)
	for i := 0; i < 5000; i++ {
		s.Tick(7)
	}
	buf := make([]byte, SerializeSize)
	if err := s.Serialize(buf); err != nil {
		t.Fatal(err)
	}
	other := New()
	if err := other.Deserialize(buf); err != nil {
		t.Fatal(err)
	}
	buf2 := make([]byte, SerializeSize)
	if err := other.Serialize(buf2); err != nil {
		t.Fatal(err)
	}
	for i := range buf {
		if buf[i] != buf2[i] {
			t.Fatalf("state mismatch at byte %d: 0x%02X vs 0x%02X", i, buf[i], buf2[i])
		}
	}
	// Both APUs must now render identical samples when ticked
	// the same way. Drain first: the original still holds its
	// pre-serialize backlog, the restored one starts empty.
	_ = s.Produce(-1)
	_ = other.Produce(-1)
	for i := 0; i < 3000; i++ {
		s.Tick(7)
		other.Tick(7)
	}
	a := s.Produce(-1)
	b := other.Produce(-1)
	if len(a) != len(b) || len(a) == 0 {
		t.Fatalf("produce lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("rendered sample %d differs after restore: %d vs %d", i, a[i], b[i])
		}
	}
}
