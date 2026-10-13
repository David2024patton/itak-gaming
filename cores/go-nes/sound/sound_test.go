package sound

import (
	"math"
	"testing"
)

// tick advances the APU n CPU cycles, dropping any samples.
func tick(a *APU, n int) {
	for i := 0; i < n; i++ {
		a.Tick()
	}
	a.AudioBuffer()
}

// measureFreq estimates a channel's frequency by counting
// rising zero crossings of its debug level over cycles ticks.
func measureFreq(a *APU, ch, cycles int) float64 {
	crossings := 0
	prev := a.DebugChannelSample(ch)
	for i := 0; i < cycles; i++ {
		a.Tick()
		v := a.DebugChannelSample(ch)
		if prev <= 0 && v > 0 {
			crossings++
		}
		prev = v
	}
	a.AudioBuffer()
	return float64(crossings) * cpuHz / float64(cycles)
}

// triggerPulse programs pulse ch (0 or 1) with a constant
// volume of 15, 50 percent duty, and the given 11-bit period,
// and triggers it (which also loads a long length).
func triggerPulse(a *APU, ch int, period int) {
	base := uint16(0x4000 + 4*ch)
	a.Write(0x4015, 0x01<<uint(ch))
	a.Write(base+0, 0x80|0x10|0x0F) // duty 2, constant volume 15
	a.Write(base+2, uint8(period))
	// Length index 1 (254 counts) in bits 7-3, period high
	// bits in 2-0.
	a.Write(base+3, 0x08|uint8(period>>8))
}

func TestPulseA4(t *testing.T) {
	a := New()
	// A4 on a pulse: period register 253 gives
	// 1789773 / (16 * 254) = 440.29 Hz, about 1.1 cents sharp.
	triggerPulse(a, 0, 253)
	if !a.DebugEnabled(0) {
		t.Fatal("pulse 1 not enabled after $4015 write")
	}
	// A full second of crossings keeps the quantization of
	// the count itself under a couple of cents.
	got := measureFreq(a, 0, cpuHz)
	cents := 1200 * math.Log2(got/440)
	if math.Abs(cents) > 6 {
		t.Fatalf("pulse A4 measured %.2f Hz (%.1f cents off)", got, cents)
	}
	t.Logf("pulse A4: measured %.2f Hz (%.1f cents)", got, cents)
}

func TestPulseMutedBelowPeriod8(t *testing.T) {
	a := New()
	triggerPulse(a, 0, 4) // period < 8: the sweep unit mutes it
	for i := 0; i < 2000; i++ {
		a.Tick()
		if a.DebugChannelSample(0) != 0 {
			t.Fatal("pulse with period 4 produced output")
		}
	}
}

func TestPulseSweepTargetOverflowMutes(t *testing.T) {
	a := New()
	// Period $700 with an upward sweep of shift 1 has target
	// $A80, above the 11-bit range: muted from the start.
	triggerPulse(a, 0, 0x700)
	a.Write(0x4001, 0x81) // sweep enabled, period 0, shift 1
	for i := 0; i < 2000; i++ {
		a.Tick()
		if a.DebugChannelSample(0) != 0 {
			t.Fatal("pulse with overflowing sweep target produced output")
		}
	}
}

func TestEnvelopeDecaySchedule(t *testing.T) {
	a := New()
	a.Write(0x4015, 0x01)
	a.Write(0x4000, 0x80|0x02) // duty 2, envelope on, V=2
	a.Write(0x4002, 253)
	a.Write(0x4003, 0x08) // trigger, long length
	// The envelope start is processed at the first quarter
	// clock (sequencer cycle 3729, about CPU cycle 7458);
	// the level then steps every V+1=3 quarter clocks. The
	// level must still be 15 shortly after the first
	// quarter clock and 14 after four more.
	tick(a, 7458+10)
	if got := a.DebugChannelSample(0); got != 15 && got != 0 {
		// The duty waveform gates the level; sample the peak
		// over a few steps instead.
		peak := got
		for i := 0; i < 16; i++ {
			tick(a, 508)
			if v := a.DebugChannelSample(0); v > peak {
				peak = v
			}
		}
		if peak != 15 {
			t.Fatalf("envelope peak after start = %v, want 15", peak)
		}
	}
	tick(a, 7458*3)
	peak := 0.0
	for i := 0; i < 40; i++ {
		tick(a, 127)
		if v := a.DebugChannelSample(0); v > peak {
			peak = v
		}
	}
	if peak != 14 {
		t.Fatalf("envelope peak after 3 more quarter clocks = %v, want 14", peak)
	}
}

func TestLengthCounterHaltAndDisable(t *testing.T) {
	a := New()
	a.Write(0x4015, 0x01)
	a.Write(0x4000, 0x80|0x10|0x0F)
	a.Write(0x4002, 253)
	a.Write(0x4003, 0x00) // length index 0: 10 counts
	if got := a.DebugLength(0); got != 10 {
		t.Fatalf("length after load = %d, want 10", got)
	}
	// Half clocks land at sequencer cycles 7457 and 14915,
	// which are CPU cycles ~14915 and ~29830 (the sequencer
	// runs at half rate): after one full sequence the
	// counter must have dropped by exactly 2.
	tick(a, 16000)
	if got := a.DebugLength(0); got != 9 {
		t.Fatalf("length after first half clock = %d, want 9", got)
	}
	tick(a, 16000)
	if got := a.DebugLength(0); got != 8 {
		t.Fatalf("length after one sequence = %d, want 8", got)
	}
	// Halt suspends the countdown.
	a.Write(0x4000, 0x80|0x20|0x10|0x0F)
	a.Write(0x4003, 0x00)
	tick(a, 29830*2)
	if got := a.DebugLength(0); got != 10 {
		t.Fatalf("halted length = %d, want 10", got)
	}
	// Disabling through $4015 clears it at once.
	a.Write(0x4015, 0x00)
	if got := a.DebugLength(0); got != 0 {
		t.Fatalf("length after disable = %d, want 0", got)
	}
	// And a disabled channel refuses loads.
	a.Write(0x4003, 0x00)
	if got := a.DebugLength(0); got != 0 {
		t.Fatalf("length loaded while disabled = %d, want 0", got)
	}
}

func TestFrameIRQSetTiming(t *testing.T) {
	a := New()
	// The first set lands at the end of the first 29830
	// cycle sequence; the exact cycle within the last few
	// is phase-dependent (the apu_test ROMs pin it). It
	// must not be set well before the end of the sequence,
	// and it must be set shortly after.
	a.Write(0x4017, 0x00)
	tick(a, 29800)
	if a.ReadStatus()&0x40 != 0 {
		t.Fatal("frame IRQ flag set well before the sequence end")
	}
	tick(a, 200)
	if a.ReadStatus()&0x40 == 0 {
		t.Fatal("frame IRQ flag not set at the sequence end")
	}
	// That read cleared the flag; one full sequence later
	// it is set again (the flag is re-set every sequence).
	tick(a, 29830)
	if a.ReadStatus()&0x40 == 0 {
		t.Fatal("frame IRQ flag not re-set on the next sequence")
	}
	// Inhibit clears the flag and keeps it clear.
	a.Write(0x4017, 0x40)
	if a.ReadStatus()&0x40 != 0 {
		t.Fatal("inhibit write did not clear the frame IRQ flag")
	}
	tick(a, 70000)
	if a.ReadStatus()&0x40 != 0 {
		t.Fatal("frame IRQ flag set while inhibited")
	}
}

func TestDMCPlaybackAndIRQ(t *testing.T) {
	a := New()
	fetches := 0
	a.FetchDMC = func(addr uint16) uint8 {
		fetches++
		if addr != 0xC000 {
			t.Errorf("DMC fetched at $%04X, want $C000", addr)
		}
		return 0x00 // all-zero bits drive the output down by 2 per bit
	}
	a.Write(0x4010, 0x80|0x0F) // IRQ enabled, fastest rate
	a.Write(0x4011, 60)        // output level 60
	a.Write(0x4012, 0x00)      // sample address $C000
	a.Write(0x4013, 0x00)      // sample length 1 byte
	a.Write(0x4015, 0x10)      // start playback
	if got := a.DebugLength(4); got != 1 {
		t.Fatalf("DMC bytes left after start = %d, want 1", got)
	}
	// The byte is fetched on the first tick; with the loop
	// off and IRQ enabled, the IRQ flag rises as the reader
	// finishes.
	tick(a, 2)
	if !a.IRQAsserted() {
		t.Fatal("DMC IRQ not asserted after the sample was fetched")
	}
	if fetches != 1 {
		t.Fatalf("DMC fetched %d bytes, want 1", fetches)
	}
	if got := a.DebugLength(4); got != 0 {
		t.Fatalf("DMC bytes left = %d, want 0", got)
	}
	// The fetched byte plays out through the shifter: one
	// shifter cycle loads it, then 8 bits of 0 at rate 54
	// pull the output from 60 down to 44.
	tick(a, 900)
	if got := a.DebugChannelSample(4); got != 44 {
		t.Fatalf("DMC output after zero byte = %v, want 44", got)
	}
	// A $4015 write clears the DMC IRQ flag.
	a.Write(0x4015, 0x00)
	if a.IRQAsserted() {
		t.Fatal("DMC IRQ flag survived a $4015 write")
	}
}

func TestDMCLoopRestarts(t *testing.T) {
	a := New()
	fetches := 0
	a.FetchDMC = func(addr uint16) uint8 { fetches++; return 0xFF }
	a.Write(0x4010, 0x40|0x0F) // loop on, IRQ off, fastest rate
	a.Write(0x4012, 0x00)
	a.Write(0x4013, 0x00)
	a.Write(0x4015, 0x10)
	tick(a, 2000)
	if fetches < 3 {
		t.Fatalf("looped DMC fetched %d bytes in 2000 cycles, want >= 3", fetches)
	}
	if a.IRQAsserted() {
		t.Fatal("looped DMC asserted an IRQ")
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	a := New()
	triggerPulse(a, 0, 253)
	a.Write(0x4008, 0x85) // triangle linear load 5, control on
	a.Write(0x400A, 100)
	a.Write(0x4015, 0x1F)
	tick(a, 5000)
	state := a.Serialize()
	b := New()
	if err := b.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	if got := b.Serialize(); string(got) != string(state) {
		t.Fatal("APU state did not round trip byte-exactly")
	}
	// And the restored APU produces the same next samples.
	for i := 0; i < 2000; i++ {
		a.Tick()
		b.Tick()
	}
	sa, sb := a.AudioBuffer(), b.AudioBuffer()
	if len(sa) != len(sb) {
		t.Fatalf("sample counts differ after restore: %d vs %d", len(sa), len(sb))
	}
	for i := range sa {
		if sa[i] != sb[i] {
			t.Fatalf("sample %d differs after restore: %d vs %d", i, sa[i], sb[i])
		}
	}
}
