package sound

import (
	"math"
	"testing"
)

// ymWriteA writes one bank A register (latch then data).
func ymWriteA(s *Sound, addr, val uint8) {
	s.WriteYM(0, addr)
	s.WriteYM(1, val)
}

// ymWriteB writes one bank B register (latch then data).
func ymWriteB(s *Sound, addr, val uint8) {
	s.WriteYM(2, addr)
	s.WriteYM(3, val)
}

// ymWriteCh writes a channel register on the bank that owns ch.
func ymWriteCh(s *Sound, ch int, addr, val uint8) {
	if ch < 3 {
		ymWriteA(s, addr, val)
	} else {
		ymWriteB(s, addr, val)
	}
}

// ymPatchSine programs one channel as a plain sine: operator 1 only,
// algorithm 7 (independent carriers), total level 0, fastest attack,
// no decay, both output sides on. Other operators stay unkeyed and
// therefore silent whatever their registers hold.
func ymPatchSine(s *Sound, ch, fnum, block int) {
	off := uint8(ch % 3)
	w := func(addr, val uint8) { ymWriteCh(s, ch, addr, val) }
	w(0x30+off, 0x01) // OP1: DT 0, MUL 1
	w(0x40+off, 0x00) // OP1: TL 0
	w(0x50+off, 0x1F) // OP1: KS 0, AR 31
	w(0x60+off, 0x00) // OP1: AM off, D1R 0
	w(0x70+off, 0x00) // OP1: D2R 0
	w(0x80+off, 0x00) // OP1: SL 0, RR 0
	w(0xA0+off, uint8(fnum&0xFF))
	w(0xA4+off, uint8(block<<3|fnum>>8))
	w(0xB0+off, 0x07) // FB 0, algorithm 7
	w(0xB4+off, 0xC0) // L+R on, AMS 0, PMS 0
}

// ymKey writes the key on/off register: slotMask bit s keys the
// operator in register slot s (slot 0 is OP1). ch is 0-based.
func ymKey(s *Sound, ch int, slotMask uint8) {
	sel := ch
	if ch >= 3 {
		sel = ch + 1
	}
	ymWriteA(s, 0x28, slotMask<<4|uint8(sel))
}

// produceAll runs Produce in frame-sized chunks and concatenates.
func produceAll(s *Sound, frames int) []int16 {
	var out []int16
	for frames > 0 {
		n := frames
		if n > 735 {
			n = 735
		}
		out = append(out, s.Produce(n)...)
		frames -= n
	}
	return out
}

func rmsRange(out []int16, from, to int) float64 {
	if from < 0 {
		from = 0
	}
	if to > len(out) {
		to = len(out)
	}
	var sum float64
	for _, v := range out[from:to] {
		sum += float64(v) * float64(v)
	}
	if to <= from {
		return 0
	}
	return math.Sqrt(sum / float64(to-from))
}

func TestYMKeyOnProducesSound(t *testing.T) {
	s := New()
	ymPatchSine(s, 0, 1083, 4)
	ymKey(s, 0, 0x1) // OP1 on

	out := s.Produce(735)
	if len(out) != 1470 {
		t.Fatalf("len(Produce(735)) = %d, want 1470", len(out))
	}
	if n := countNonzero(out); n < 1000 {
		t.Fatalf("keyed-on FM channel produced only %d nonzero samples of 1470", n)
	}
}

func TestYMFrequencyA4(t *testing.T) {
	s := New()
	// F-number 1083, block 4 is 440.1 Hz by the documented
	// frequency formula.
	ymPatchSine(s, 0, 1083, 4)
	ymKey(s, 0, 0x1)

	out := produceAll(s, 15435) // 0.35 s
	// Rising zero crossings of the left channel between 0.10 s
	// and 0.30 s (attack long settled, sustain crawl negligible).
	from, to := 4410*2, 13230*2
	crossings := 0
	for i := from; i+2 < to && i+2 < len(out); i += 2 {
		if out[i] < 0 && out[i+2] >= 0 {
			crossings++
		}
	}
	freq := float64(crossings) / 0.20
	if freq < 415 || freq > 465 {
		t.Fatalf("measured %.1f Hz from %d crossings, want about 440", freq, crossings)
	}
}

func TestYMEnvelopeDecay(t *testing.T) {
	s := New()
	ymPatchSine(s, 0, 1083, 4)
	// Fast first decay to a deep sustain level: D1R 16, SL 15.
	ymWriteCh(s, 0, 0x60, 0x10)
	ymWriteCh(s, 0, 0x80, 0xF0)
	ymKey(s, 0, 0x1)

	out := produceAll(s, 11025)            // 0.25 s
	early := rmsRange(out, 88*2, 265*2)    // 2-6 ms: still loud
	late := rmsRange(out, 8820*2, 11025*2) // 200-250 ms: decayed away
	if early < 500 {
		t.Fatalf("early RMS = %.1f, want a loud note right after attack", early)
	}
	if late > early*0.05 {
		t.Fatalf("late RMS = %.1f, want under 5%% of early %.1f after decay", late, early)
	}
}

func TestYMKeyOffRelease(t *testing.T) {
	s := New()
	ymPatchSine(s, 0, 1083, 4)
	ymWriteCh(s, 0, 0x80, 0x0F) // SL 0, RR 15 (fast release)
	ymKey(s, 0, 0x1)

	out := produceAll(s, 4410) // 0.1 s of sustain
	if got := rmsRange(out, 2205*2, 4410*2); got < 1000 {
		t.Fatalf("sustaining RMS = %.1f, want a loud held note", got)
	}

	ymKey(s, 0, 0x0)           // key off
	out = produceAll(s, 13230) // 0.3 s
	max := 0
	for _, v := range out[11025*2:] { // final 50 ms
		if v < 0 {
			v = -v
		}
		if int(v) > max {
			max = int(v)
		}
	}
	if max > 1 {
		t.Fatalf("max sample after release = %d, want silence (<= 1)", max)
	}
}

func TestYMTimerAFlag(t *testing.T) {
	s := New()
	ymWriteA(s, 0x24, 0xFF)
	ymWriteA(s, 0x25, 0xC0) // NA = 1023: period 18 master clocks
	ymWriteA(s, 0x27, 0x05) // load + enable timer A
	if got := s.ReadYM(); got&0x01 != 0 {
		t.Fatalf("timer A flag set before any time passed: %#02x", got)
	}
	s.Produce(16)
	if got := s.ReadYM(); got&0x01 == 0 {
		t.Fatalf("timer A flag not set after its period: %#02x", got)
	}
	ymWriteA(s, 0x27, 0x10) // reset the flag (drops load bits too)
	if got := s.ReadYM(); got&0x01 != 0 {
		t.Fatalf("timer A flag not cleared by the 0x27 reset bit: %#02x", got)
	}

	// A long period must not fire early: NA = 0 is 18432 clocks.
	s2 := New()
	ymWriteA(s2, 0x24, 0x00)
	ymWriteA(s2, 0x25, 0x00)
	ymWriteA(s2, 0x27, 0x05)
	s2.Produce(10) // about 1700 clocks
	if got := s2.ReadYM(); got&0x01 != 0 {
		t.Fatalf("timer A fired early at period 18432: %#02x", got)
	}
	s2.Produce(200) // about 36,000 clocks total
	if got := s2.ReadYM(); got&0x01 == 0 {
		t.Fatalf("timer A never fired at period 18432: %#02x", got)
	}
}

func TestYMAdvanceClocksTimerA(t *testing.T) {
	s := New()
	ymWriteA(s, 0x24, 0xFA)
	ymWriteA(s, 0x25, 0x00) // NA = 1000: period 18*24 = 432 master clocks
	ymWriteA(s, 0x27, 0x05) // load + enable timer A
	// No Produce call anywhere: the flag must move on stepped
	// clocks alone, at exactly the accumulated period.
	s.AdvanceClocks(431)
	if got := s.ReadYM(); got&0x01 != 0 {
		t.Fatalf("timer A flag set after 431 of 432 clocks: %#02x", got)
	}
	s.AdvanceClocks(1)
	if got := s.ReadYM(); got&0x01 == 0 {
		t.Fatalf("timer A flag not set at 432 clocks: %#02x", got)
	}
	ymWriteA(s, 0x27, 0x10) // reset bit still clears it
	if got := s.ReadYM(); got&0x01 != 0 {
		t.Fatalf("timer A flag not cleared by the 0x27 reset bit: %#02x", got)
	}
}

func TestYMAdvanceClocksTimerB(t *testing.T) {
	s := New()
	ymWriteA(s, 0x26, 0xFE) // NB = 254: period 288*2 = 576 master clocks
	ymWriteA(s, 0x27, 0x0A) // load + enable timer B
	s.AdvanceClocks(575)
	if got := s.ReadYM(); got&0x02 != 0 {
		t.Fatalf("timer B flag set after 575 of 576 clocks: %#02x", got)
	}
	s.AdvanceClocks(1)
	if got := s.ReadYM(); got&0x02 == 0 {
		t.Fatalf("timer B flag not set at 576 clocks: %#02x", got)
	}
}

func TestYMAdvanceClocksLedger(t *testing.T) {
	s := New()
	ymWriteA(s, 0x24, 0xFA)
	ymWriteA(s, 0x25, 0x00) // period 432 master clocks
	ymWriteA(s, 0x27, 0x05) // load + enable timer A
	s.AdvanceClocks(400)
	// One rendered output frame from a fresh chip generates two
	// native samples (288 clocks), all inside the 400 already
	// stepped. If Produce stepped them again the total would be
	// 688 and the flag would be up; it must stay down.
	s.Produce(1)
	if got := s.ReadYM(); got&0x01 != 0 {
		t.Fatalf("timer A flag set: Produce double-stepped system time: %#02x", got)
	}

	// The ledger is part of the save state: a chip restored here
	// agrees on the remaining distance to the flag.
	buf := make([]byte, s.SerializeSize())
	if err := s.Serialize(buf); err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	s2 := New()
	if err := s2.Deserialize(buf); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	s2.AdvanceClocks(31) // 431 total
	if got := s2.ReadYM(); got&0x01 != 0 {
		t.Fatalf("restored chip: timer A flag set after 431 clocks: %#02x", got)
	}
	s.AdvanceClocks(31)
	s.AdvanceClocks(1)
	s2.AdvanceClocks(1)
	if got := s.ReadYM(); got&0x01 == 0 {
		t.Fatalf("timer A flag not set at 432 clocks: %#02x", got)
	}
	if got := s2.ReadYM(); got&0x01 == 0 {
		t.Fatalf("restored chip: timer A flag not set at 432 clocks: %#02x", got)
	}
}

func TestYMDACMode(t *testing.T) {
	s := New()
	ymWriteA(s, 0x2A, 0xFF) // DAC data, near positive full scale
	ymWriteA(s, 0x2B, 0x80) // DAC enable: channel 6 becomes the DAC
	ymWriteB(s, 0xB6, 0xC0) // channel 6 pan: L+R

	out := s.Produce(400)
	for i := 200; i < len(out); i++ {
		if out[i] < 2000 {
			t.Fatalf("DAC sample %d = %d, want a steady value over 2000", i, out[i])
		}
		if out[i] != out[200] {
			t.Fatalf("DAC output not steady: sample %d = %d, sample 200 = %d", i, out[i], out[200])
		}
	}
}

// The DAC must be audible with NO pan write at all: pan powers on
// routed to both sides (Sonic 1's SEGA jingle streams the DAC
// without ever writing channel 6's pan register first).
func TestYMDACPanPowerOnDefault(t *testing.T) {
	s := New()
	ymWriteA(s, 0x2A, 0xFF) // DAC data, near positive full scale
	ymWriteA(s, 0x2B, 0x80) // DAC enable: channel 6 becomes the DAC

	out := s.Produce(400)
	for i := 200; i < len(out); i++ {
		if out[i] < 2000 {
			t.Fatalf("DAC sample %d = %d, want a steady value over 2000 with no pan write", i, out[i])
		}
	}
}

func TestYMSerializeDeterministic(t *testing.T) {
	s := New()
	ymPatchSine(s, 0, 1083, 4)
	ymWriteCh(s, 0, 0x60, 0x10) // some decay so the EG is mid-flight
	ymWriteCh(s, 0, 0x80, 0xF0)
	ymKey(s, 0, 0x1)
	s.Produce(1000)

	buf := make([]byte, s.SerializeSize())
	if err := s.Serialize(buf); err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	s2 := New()
	if err := s2.Deserialize(buf); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}

	// A state restored and immediately re-serialized is
	// byte-identical to the state that was saved.
	buf2 := make([]byte, s2.SerializeSize())
	if err := s2.Serialize(buf2); err != nil {
		t.Fatalf("re-Serialize: %v", err)
	}
	for i := range buf {
		if buf[i] != buf2[i] {
			t.Fatalf("re-serialized state differs at byte %d: %02x vs %02x", i, buf[i], buf2[i])
		}
	}

	// And the restored chip produces exactly the samples the
	// original produces from the same point.
	outA := s.Produce(500)
	outB := s2.Produce(500)
	if len(outA) != len(outB) {
		t.Fatalf("post-restore lengths differ: %d vs %d", len(outA), len(outB))
	}
	for i := range outA {
		if outA[i] != outB[i] {
			t.Fatalf("post-restore sample %d differs: %d vs %d", i, outA[i], outB[i])
		}
	}
}
