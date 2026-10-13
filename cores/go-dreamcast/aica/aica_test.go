package aica

import (
	"encoding/binary"
	"testing"
)

type fakeSink struct {
	raised bool
	raises int
	clears int
}

func (f *fakeSink) RaiseExternal(bits uint32) {
	if bits == 1<<1 {
		f.raised = true
		f.raises += 1
	}
}

func (f *fakeSink) ClearExternal(bits uint32) {
	if bits == 1<<1 {
		f.raised = false
		f.clears += 1
	}
}

func newTestAICA() (*AICA, []byte, *fakeSink) {
	ram := make([]byte, 2<<20)
	sink := &fakeSink{}
	return New(ram, sink), ram, sink
}

func wreg(a *AICA, off uint32, v uint32) {
	a.Write32(0x00700000+off, v)
}

func rreg(a *AICA, off uint32) uint32 {
	return a.Read32(0x00700000 + off)
}

// setupSlot programs slot 0 with fast envelopes, unity gains, the
// pass through filter setting (FLV all maximum, Q = 4), and the given
// format and loop settings, then keys it on.
func setupSlot(a *AICA, r0 uint16, lea uint16) {
	wreg(a, 0x2800, 15) // MVOL 15, stereo
	// AR 31 with KRS 0xF is the instant attack rate. Decay and
	// release rates are 0, which is infinite time, so the envelope
	// holds at full volume for the whole test.
	wreg(a, 0x10, 0x001F)
	wreg(a, 0x14, 0x3C00) // KRS 0xF
	wreg(a, 0x28, 4)      // TL 0, Q 4
	for i := 0; i < 5; i += 1 {
		wreg(a, 0x2C+uint32(i)*4, 0x1FF8)
	}
	wreg(a, 0x40, 0x1F00) // FAR 31, FD1R 0
	wreg(a, 0x44, 0)      // FD2R 0, FRR 0
	wreg(a, 0x24, 0xF00)  // DISDL 15, DIPAN 0
	wreg(a, 0x08, 0)      // LSA
	wreg(a, 0x0C, uint32(lea))
	wreg(a, 0x00, uint32(r0&^(1<<15)))
	wreg(a, 0x00, uint32(r0|(1<<15))) // KYONEX write applies the key state
}

func putPCM16(ram []byte, pos int, v int16) {
	binary.LittleEndian.PutUint16(ram[pos*2:], uint16(v))
}

func TestPCM16LoopExact(t *testing.T) {
	a, ram, _ := newTestAICA()
	for i := 0; i < 64; i += 1 {
		putPCM16(ram, i, 1000)
	}
	// Register writes mask to 16 bits.
	wreg(a, 0x04, 0xDEAD0000)
	if got := rreg(a, 0x04); got != 0 {
		t.Fatalf("masked SA read = %#x, want 0", got)
	}
	setupSlot(a, (1<<14)|(1<<9), 10) // KYONB, LPCTL
	a.AdvanceMaster(512 * 8)
	out := a.AudioBuffer()
	if len(out) != 8 {
		t.Fatalf("samples = %d, want 8", len(out))
	}
	for i, s := range out {
		if s != [2]int16{1000, 1000} {
			t.Fatalf("sample %d = %v, want [1000 1000]", i, s)
		}
	}
	if len(a.AudioBuffer()) != 0 {
		t.Fatalf("buffer not drained")
	}
	if AudioSampleRate() != 44100 {
		t.Fatalf("sample rate = %d", AudioSampleRate())
	}
}

func TestADPCMHandComputed(t *testing.T) {
	a, ram, _ := newTestAICA()
	// Nibbles, low first: 0,1,2,3,4,5,6,7,8,9.
	copy(ram, []byte{0x10, 0x32, 0x54, 0x76, 0x98})
	// Expected values computed by hand from the documented rules:
	// step starts at 127, predictor at 0. For magnitudes 0 to 3 the
	// step factor 230 keeps the step clamped at 127, so the deltas
	// are 15, 47, 79, 111. Magnitude 4 raises the step to 152,
	// giving delta 142 then 209 then 393 then 907 as the step grows
	// through 242 and 484 to 1160. The last two nibbles carry the
	// sign bit, subtracting 145 and 390.
	want := []int16{15, 62, 141, 252, 394, 603, 996, 1903, 1758, 1368}
	setupSlot(a, (1<<14)|(1<<9)|(2<<7), 100) // KYONB, LPCTL, PCMS 2
	a.AdvanceMaster(512 * len(want))
	out := a.AudioBuffer()
	if len(out) != len(want) {
		t.Fatalf("samples = %d, want %d", len(out), len(want))
	}
	for i, w := range want {
		if out[i] != [2]int16{w, w} {
			t.Fatalf("sample %d = %v, want [%d %d]", i, out[i], w, w)
		}
	}
}

func TestPitchStepAndCA(t *testing.T) {
	a, ram, _ := newTestAICA()
	for i := 0; i < 64; i += 1 {
		putPCM16(ram, i, int16(i*100))
	}
	setupSlot(a, (1<<14)|(1<<9), 1000)
	// FNS 512 with OCT 0 gives a step of 1536, one and a half
	// samples per output sample.
	wreg(a, 0x18, 512)
	a.AdvanceMaster(512 * 4)
	out := a.AudioBuffer()
	want := []int16{0, 150, 300, 450}
	if len(out) != 4 {
		t.Fatalf("samples = %d, want 4", len(out))
	}
	for i, w := range want {
		if out[i][0] != w {
			t.Fatalf("sample %d = %d, want %d", i, out[i][0], w)
		}
	}
	// Four advances of one and a half samples put CA at 6.
	if got := rreg(a, 0x2814); got != 6 {
		t.Fatalf("CA = %d, want 6", got)
	}
}

func setupTimerA(a *AICA) {
	wreg(a, 0x2890, 0xFE) // CTL 0, counter starts at 254
}

func TestTimerWrapInterruptAndEOI(t *testing.T) {
	a, _, _ := newTestAICA()
	setupTimerA(a)
	wreg(a, 0x289C, 1<<6) // SCIEB timer A
	wreg(a, 0x28A8, 1<<6) // SCILV0: level bit 0
	wreg(a, 0x28B0, 1<<6) // SCILV2: level bit 2, level 5 total
	a.AdvanceMaster(512 * 1)
	if a.pending&(1<<6) != 0 {
		t.Fatalf("timer pending after 1 sample, counter should be 255")
	}
	a.AdvanceMaster(512 * 1)
	if a.pending&(1<<6) == 0 {
		t.Fatalf("timer pending missing after wrap at 2 samples")
	}
	if !a.fiqAsserted || a.presentedSrc != 6 || a.presentedLvl != 5 {
		t.Fatalf("presentation = src %d lvl %d fiq %v", a.presentedSrc, a.presentedLvl, a.fiqAsserted)
	}
	bus := &armBus{a: a}
	if got := bus.Read32(0x00802D00); got != 5 {
		t.Fatalf("L register = %d, want 5", got)
	}
	bus.Write32(0x00802D04, 1) // EOI
	if a.pending&(1<<6) != 0 || a.fiqAsserted {
		t.Fatalf("EOI did not clear timer source")
	}
}

func TestSH4ExternalInterrupt(t *testing.T) {
	a, _, sink := newTestAICA()
	setupTimerA(a)
	wreg(a, 0x28B4, 1<<6) // MCIEB timer A
	a.AdvanceMaster(512 * 2)
	if !sink.raised || sink.raises < 1 {
		t.Fatalf("sink not raised: %+v", sink)
	}
	wreg(a, 0x28A4, 1<<6) // SCIRE clears the source
	if sink.raised || sink.clears < 1 {
		t.Fatalf("sink not cleared: %+v", sink)
	}
}

// TestDSPGainDelayAndMemory is the DSP unit gate.
//
// The slot feeds a constant 8000 sample into MIXS 0 with direct
// output muted. The program:
//
//	step 0: ACC = INPUTS(MIXS 0) * COEF gain 0.5
//	step 1: TEMP[5 + DEC] = SHIFTED (the halved input)
//	step 2: ACC = TEMP[6 + DEC] * COEF gain 0.5 (last sample, since
//	        DEC fell by one since it was written)
//	step 3: EFREG 0 += SHIFTED >> 8, so EFREG gets prev / 4
//	step 4: ACC = INPUTS(MIXS 0) * COEF gain 0.5 again
//	step 5: odd step MWT writes SHIFTED >> 8 (NOFL) to the ring at
//	        MADRS 0 plus mdec
//	step 7: odd step MRD reads that word back into MEMVAL
//	step 8: MEMS 3 = MEMVAL via IWT
//
// Expected audio: sample 0 is 0 (TEMP starts clear), then 2000 for
// every following sample. After two samples the ring holds the word
// 4000 at word 0 and at word 8191 (mdec after one decrement), and
// MEMS 3 holds 4000 << 8.
func TestDSPGainDelayAndMemory(t *testing.T) {
	a, ram, _ := newTestAICA()
	for i := 0; i < 64; i += 1 {
		putPCM16(ram, i, 8000)
	}
	setupSlot(a, (1<<14)|(1<<9), 100)
	wreg(a, 0x24, 0)       // DISDL 0: direct muted
	wreg(a, 0x20, 0xF0)    // IMXL 15, ISEL 0
	wreg(a, 0x2000, 0xF00) // EFSDL 15 for DSP output 0
	for _, s := range []int{0, 2, 4} {
		a.coef[s] = 0x4000 // Y = 2048, gain one half
	}
	a.mpro[0] = [4]uint16{0, (1 << 15) | (1 << 13) | (0x20 << 7), 1 << 1, 0}
	a.mpro[1] = [4]uint16{(1 << 8) | (5 << 1), 0, 0, 0}
	a.mpro[2] = [4]uint16{6 << 9, 1 << 13, 1 << 1, 0}
	a.mpro[3] = [4]uint16{0, 0, 1 << 12, 0}
	a.mpro[4] = [4]uint16{0, (1 << 15) | (1 << 13) | (0x20 << 7), 1 << 1, 0}
	a.mpro[5] = [4]uint16{0, 0, 1 << 14, 1 << 15}
	a.mpro[7] = [4]uint16{0, 0, 1 << 13, 1 << 15}
	a.mpro[8] = [4]uint16{0, (1 << 6) | (3 << 1), 0, 0}

	a.AdvanceMaster(512 * 4)
	out := a.AudioBuffer()
	want := []int16{0, 2000, 2000, 2000}
	if len(out) != 4 {
		t.Fatalf("samples = %d, want 4", len(out))
	}
	for i, w := range want {
		if out[i] != [2]int16{w, w} {
			t.Fatalf("sample %d = %v, want [%d %d]", i, out[i], w, w)
		}
	}
	if got := binary.LittleEndian.Uint16(ram[0:]); got != 4000 {
		t.Fatalf("ring word 0 = %d, want 4000", got)
	}
	if got := binary.LittleEndian.Uint16(ram[8191*2:]); got != 4000 {
		t.Fatalf("ring word 8191 = %d, want 4000", got)
	}
	if a.mems[3] != 4000<<8 {
		t.Fatalf("MEMS 3 = %d, want %d", a.mems[3], 4000<<8)
	}
	if got := rreg(a, 0x4C00); got != 2000 {
		t.Fatalf("EFREG 0 read = %d, want 2000", got)
	}
}

func TestInternalDMA(t *testing.T) {
	a, ram, _ := newTestAICA()
	binary.LittleEndian.PutUint32(ram[0x100:], 0x11223344)
	binary.LittleEndian.PutUint32(ram[0x104:], 0x55667788)
	wreg(a, 0x2880, 0)          // DMEA high bits
	wreg(a, 0x2884, 0x100>>2)   // DMEA low bits
	wreg(a, 0x2888, 0x80>>2<<2) // DRGA field for byte offset 0x80
	wreg(a, 0x288C, 2<<2|1)     // DLG 2 words, DDIR 0, DEXE
	if got := a.getRaw16(0x80); got != 0x3344 {
		t.Fatalf("DMA reg low = %#x, want 0x3344", got)
	}
	if got := a.getRaw16(0x82); got != 0x1122 {
		t.Fatalf("DMA reg high = %#x, want 0x1122", got)
	}
	if got := a.getRaw16(0x84); got != 0x7788 {
		t.Fatalf("DMA reg 2 low = %#x, want 0x7788", got)
	}
	if a.pending&(1<<4) == 0 {
		t.Fatalf("DMA end pending missing")
	}
	if rreg(a, 0x288C)&1 != 0 {
		t.Fatalf("DEXE did not clear")
	}
	// Send the same register words back out to wave RAM at 0x200.
	wreg(a, 0x2884, 0x200>>2)
	wreg(a, 0x288C, (1<<15)|2<<2|1) // DDIR 1
	if got := binary.LittleEndian.Uint32(ram[0x200:]); got != 0x11223344 {
		t.Fatalf("DMA back word 0 = %#x", got)
	}
	if got := binary.LittleEndian.Uint32(ram[0x204:]); got != 0x55667788 {
		t.Fatalf("DMA back word 1 = %#x", got)
	}
}

func TestARMHeldAndReleased(t *testing.T) {
	a, _, _ := newTestAICA()
	if a.ARM() == nil {
		t.Fatalf("ARM() is nil")
	}
	before := make([]byte, 4096)
	n := a.ARM().Serialize(before)
	before = before[:n]
	a.AdvanceMaster(512 * 100)
	after := make([]byte, 4096)
	m := a.ARM().Serialize(after)
	after = after[:m]
	if string(before) != string(after) {
		t.Fatalf("ARM state changed while ARMRST held it in reset")
	}
	wreg(a, 0x2C00, 0) // release
	a.AdvanceMaster(512 * 100)
	released := make([]byte, 4096)
	k := a.ARM().Serialize(released)
	released = released[:k]
	if string(before) == string(released) {
		t.Fatalf("ARM state did not change after release")
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	a, ram, _ := newTestAICA()
	for i := 0; i < 64; i += 1 {
		putPCM16(ram, i, int16(500+i*10))
	}
	setupSlot(a, (1<<14)|(1<<9), 40)
	a.AdvanceMaster(512 * 30)
	buf := make([]byte, SerializeSize)
	if n := a.Serialize(buf); n != SerializeSize {
		t.Fatalf("Serialize wrote %d, want %d", n, SerializeSize)
	}
	ramCopy := make([]byte, len(ram))
	copy(ramCopy, ram)
	b := New(ramCopy, &fakeSink{})
	if !b.Deserialize(buf) {
		t.Fatalf("Deserialize rejected a valid blob")
	}
	if b.Deserialize(buf[:len(buf)-1]) {
		t.Fatalf("Deserialize accepted a short blob")
	}
	a.AdvanceMaster(512 * 50)
	b.AdvanceMaster(512 * 50)
	outA := a.AudioBuffer()
	outB := b.AudioBuffer()
	if len(outA) != len(outB) {
		t.Fatalf("output lengths %d and %d", len(outA), len(outB))
	}
	for i := range outA {
		if outA[i] != outB[i] {
			t.Fatalf("sample %d differs: %v and %v", i, outA[i], outB[i])
		}
	}
	bufA := make([]byte, SerializeSize)
	bufB := make([]byte, SerializeSize)
	a.Serialize(bufA)
	b.Serialize(bufB)
	if string(bufA) != string(bufB) {
		t.Fatalf("post run states differ")
	}
}

func TestRTC(t *testing.T) {
	a, _, _ := newTestAICA()
	a.Write32(0x00710000, 0x12340000) // no enable, ignored
	if got := a.Read32(0x00710000); got != 0 {
		t.Fatalf("RTC write without enable took effect: %#x", got)
	}
	a.Write32(0x00710008, 1)
	a.Write32(0x00710000, 0x12340000)
	a.Write32(0x00710008, 1)
	a.Write32(0x00710004, 0x5678)
	if got := a.Read32(0x00710000) | a.Read32(0x00710004); got != 0x12345678 {
		t.Fatalf("RTC = %#x, want 0x12345678", got)
	}
	a.AdvanceMaster(22579200)
	if got := a.Read32(0x00710000) | a.Read32(0x00710004); got != 0x12345679 {
		t.Fatalf("RTC after one second = %#x", got)
	}
}
