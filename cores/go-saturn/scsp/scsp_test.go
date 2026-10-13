package scsp

import (
	"encoding/binary"
	"math"
	"testing"
)

func newTestSCSP() (*SCSP, []byte, []byte) {
	ram := make([]byte, soundRAMSize)
	regs := make([]byte, regPageSize)
	s := New(ram, regs)
	binary.BigEndian.PutUint16(regs[RegMaster:RegMaster+2], 0x800F) // MEM4MB, MVOL 15
	return s, ram, regs
}

func putReg(regs []byte, off int, v uint16) {
	binary.BigEndian.PutUint16(regs[off:off+2], v)
}

func getReg(regs []byte, off int) uint16 {
	return binary.BigEndian.Uint16(regs[off : off+2])
}

// tickN advances exactly n sample ticks.
func tickN(s *SCSP, n int) {
	for i := 0; i < n; i++ {
		target := len(s.audio) + 2
		for len(s.audio) < target {
			s.Step(100)
		}
	}
}

// setupSineSlot loads a 256 sample sine into RAM at 0x2000 and
// programs slot 0 to loop it forward at the given pitch word, with
// instant attack and a sustaining envelope.
func setupSineSlot(s *SCSP, ram, regs []byte, pitchWord uint16) {
	const sa = 0x2000
	for i := 0; i < 256; i++ {
		v := int16(30000 * math.Sin(2*math.Pi*float64(i)/256))
		binary.BigEndian.PutUint16(ram[sa+i*2:sa+i*2+2], uint16(v))
	}
	base := 0                           // slot 0
	putReg(regs, base+SlotCtrl, 0x4100) // KYONB, LPCTL forward loop
	putReg(regs, base+SlotSA, sa)
	putReg(regs, base+SlotLSA, 0)
	putReg(regs, base+SlotLEA, 255)
	putReg(regs, base+SlotEG1, 31)     // AR 31 instant
	putReg(regs, base+SlotEG2, 0x7800) // KRS 15 (scaling off)
	putReg(regs, base+SlotVol, 0)      // TL 0
	putReg(regs, base+SlotPitch, pitchWord)
	putReg(regs, base+SlotOutSel, 0xE000) // DISDL 7, centre pan
	// Key on: set KYONEX alongside KYONB.
	putReg(regs, base+SlotCtrl, 0xC100)
}

// positiveCrossings returns the sample indices where the left
// channel crosses from negative to non-negative.
func positiveCrossings(out []int16) []int {
	var idx []int
	for n := 1; n*2 < len(out); n++ {
		if out[(n-1)*2] < 0 && out[n*2] >= 0 {
			idx = append(idx, n)
		}
	}
	return idx
}

func TestSineFrequencyAndLoopWrap(t *testing.T) {
	s, ram, regs := newTestSCSP()
	setupSineSlot(s, ram, regs, 0)
	tickN(s, 1024)
	out := s.AudioBuffer()
	if len(out) != 2048 {
		t.Fatalf("audio len %d, want 2048", len(out))
	}
	cross := positiveCrossings(out)
	if len(cross) < 3 {
		t.Fatalf("crossings %v, want at least 3", cross)
	}
	period := cross[1] - cross[0]
	if period != 256 {
		t.Fatalf("period %d samples, want exactly 256 (loop wrap at LEA)", period)
	}
	freq := float64(sampleRate) / float64(period)
	want := 44100.0 / 256
	if math.Abs(freq-want)/want > 0.01 {
		t.Fatalf("freq %.2f Hz, want %.2f within 1 percent", freq, want)
	}
	// Loop wrap exactness: the pattern repeats bit exactly.
	for k := 0; k < 512; k++ {
		if out[k] != out[k+512] {
			t.Fatalf("loop repeat mismatch at %d: %d vs %d", k, out[k], out[k+512])
		}
	}
}

func TestOctaveDoublesFrequency(t *testing.T) {
	s, ram, regs := newTestSCSP()
	setupSineSlot(s, ram, regs, 1<<11) // OCT 1
	tickN(s, 1024)
	out := s.AudioBuffer()
	cross := positiveCrossings(out)
	if len(cross) < 3 {
		t.Fatalf("crossings %v, want at least 3", cross)
	}
	if period := cross[1] - cross[0]; period != 128 {
		t.Fatalf("OCT 1 period %d, want 128 (double the unity frequency)", period)
	}
}

func TestEGAttackCountAndReleaseCheckpoints(t *testing.T) {
	s, ram, regs := newTestSCSP()
	const sa = 0x2000
	for i := 0; i < 64; i++ {
		binary.BigEndian.PutUint16(ram[sa+i*2:sa+i*2+2], 20000)
	}
	putReg(regs, SlotCtrl, 0x4100)
	putReg(regs, SlotSA, sa)
	putReg(regs, SlotLSA, 0)
	putReg(regs, SlotLEA, 63)
	// AR 20, D1R 0, D2R 0; KRS 15 off, DL 0, RR 20.
	putReg(regs, SlotEG1, 20)
	putReg(regs, SlotEG2, 0x7800|20)
	putReg(regs, SlotOutSel, 0xE000)
	putReg(regs, SlotCtrl, 0xC100) // key on

	ticks := 0
	for s.slots[0].egLevel != 0 {
		tickN(s, 1)
		ticks++
		if ticks > 1000 {
			t.Fatal("attack never reached full volume")
		}
	}
	// AR 20 with scaling off: effective rate 40, step 8 levels per
	// sample over the 1023 level range: exactly 128 samples.
	if ticks != 128 {
		t.Fatalf("attack took %d samples, want exactly 128 (plus or minus 1 allowed by the brief)", ticks)
	}
	tickN(s, 1) // decay 1 runs and, DL being 0, hands to decay 2
	if s.slots[0].egPhase != egDecay2 {
		t.Fatalf("after attack with DL 0 the phase is %d, want decay2 (sustain)", s.slots[0].egPhase)
	}

	// Key off and check two release checkpoints.
	putReg(regs, SlotCtrl, 0x8100) // KYONEX, KYONB 0
	tickN(s, 32)
	if got := int(s.slots[0].egLevel >> 8); got != 256 {
		t.Fatalf("release checkpoint 1: level %d, want 256", got)
	}
	tickN(s, 32)
	if got := int(s.slots[0].egLevel >> 8); got != 512 {
		t.Fatalf("release checkpoint 2: level %d, want 512", got)
	}
}

func TestPanFullLeft(t *testing.T) {
	s, ram, regs := newTestSCSP()
	setupSineSlot(s, ram, regs, 0)
	putReg(regs, SlotOutSel, 0xE000|0x1F<<8) // DISDL 7, DIPAN 1FH full left
	tickN(s, 256)
	out := s.AudioBuffer()
	nonzeroL := false
	for i := 0; i+1 < len(out); i += 2 {
		if out[i+1] != 0 {
			t.Fatalf("full left pan: right channel sample %d is %d, want exactly 0", i/2, out[i+1])
		}
		if out[i] != 0 {
			nonzeroL = true
		}
	}
	if !nonzeroL {
		t.Fatal("full left pan: left channel is silent")
	}
}

func TestKeyOnOffIntegration(t *testing.T) {
	s, ram, regs := newTestSCSP()
	setupSineSlot(s, ram, regs, 0)
	tickN(s, 64)
	out := s.AudioBuffer()
	peak := 0
	for _, v := range out {
		if int(v) > peak {
			peak = int(v)
		}
	}
	if peak < 25000 {
		t.Fatalf("key on produced peak %d, want near 30000", peak)
	}
	// Key off with an instant release (RR 31).
	putReg(regs, SlotEG2, 0x7800|31)
	putReg(regs, SlotCtrl, 0x8100)
	tickN(s, 16)
	out = s.AudioBuffer()
	for i, v := range out {
		if v != 0 {
			t.Fatalf("after key off and instant release, sample %d is %d, want 0", i, v)
		}
	}
	if s.slots[0].active {
		t.Fatal("slot still active after release completed")
	}
}

// dspStepWords builds one subset microprogram step.
func dspStep(dest, destIdx, alu int, mul bool, srcKind, srcIdx, coefIdx int, w2 uint16) (uint16, uint16, uint16, uint16) {
	w0 := uint16(dest&0xF)<<12 | uint16(destIdx&0x7F)<<5 | uint16(alu&0xF)<<1
	if mul {
		w0 |= 1
	}
	kind := srcKind
	ext := uint16(0)
	if kind >= 4 {
		kind -= 4
		ext = 1
	}
	w1 := uint16(kind&3)<<14 | ext<<13 | uint16(srcIdx&0x7F)<<6 | uint16(coefIdx&0x3F)
	return w0, w1, w2, 0
}

func putMPRO(regs []byte, step int, w0, w1, w2, w3 uint16) {
	off := RegMPRO + step*8
	putReg(regs, off, w0)
	putReg(regs, off+2, w1)
	putReg(regs, off+4, w2)
	putReg(regs, off+6, w3)
}

func TestDSPGainHalf(t *testing.T) {
	s, _, regs := newTestSCSP()
	putReg(regs, RegCOEF, 0x0800) // COEF 0 = 0.5 in Q12
	w0, w1, w2, w3 := dspStep(dspDestEFREG, 0, dspALULoad, true, dspSrcMEMS, 0, 0, 0)
	putMPRO(regs, 0, w0, w1, w2, w3)
	inputs := []int32{1000, -2000, 4000, -8000, 16000, 32767, -32768, 12345}
	for i, v := range inputs {
		var mixs [16]int32
		mixs[0] = v
		s.runDSP(mixs)
		want := v / 2
		if v == 32767 {
			want = 16383 // (32767 * 2048) >> 12, truncated
		}
		if v == 12345 {
			want = 6172
		}
		if got := s.dsp.efreg[0]; got != want {
			t.Fatalf("sample %d: EFREG0 %d, want %d (MEMS0 %d times 0.5)", i, got, want, v)
		}
	}
}

func TestDSPFeedbackDelay(t *testing.T) {
	s, ram, regs := newTestSCSP()
	_ = ram
	putReg(regs, RegCOEF, 0x0800)   // a = 0.5
	putReg(regs, RegCOEF+2, 0x0400) // b = 0.25
	putReg(regs, RegCOEF+63*2, 4)   // ring length extension: 4 words
	putReg(regs, RegMADRS, 0)       // delay line at ring word 0
	// Step 0: read the delay tap into MEMS 1.
	putMPRO(regs, 0, 0, 0, 0x8100, 0)
	// Step 1: ACC = MEMS0 * a.
	w0, w1, w2, w3 := dspStep(dspDestNone, 0, dspALULoad, true, dspSrcMEMS, 0, 0, 0)
	putMPRO(regs, 1, w0, w1, w2, w3)
	// Step 2: ACC += MEMS1 * b.
	w0, w1, w2, w3 = dspStep(dspDestNone, 0, dspALUAdd, true, dspSrcMEMS, 1, 1, 0)
	putMPRO(regs, 2, w0, w1, w2, w3)
	// Step 3: EFREG0 = ACC.
	w0, w1, w2, w3 = dspStep(dspDestEFREG, 0, dspALUNone, false, dspSrcMEMS, 0, 0, 0)
	putMPRO(regs, 3, w0, w1, w2, w3)
	// Step 4: write ACC back to the delay line.
	w0, w1, w2, w3 = dspStep(dspDestMemWrite, 0, dspALUNone, false, dspSrcMEMS, 0, 0, 0)
	putMPRO(regs, 4, w0, w1, w2, w3)

	// Input is 8000 every fourth sample, 0 otherwise. Hand
	// computed with a = 1/2, b = 1/4, D = 4 and a zeroed line:
	// out(t) = in(t)/2 + out(t-4)/4 for t a multiple of 4.
	want := []int32{
		4000, 0, 0, 0,
		5000, 0, 0, 0,
		5250, 0, 0, 0,
		5312, 0, 0, 0,
	}
	for i := 0; i < 16; i++ {
		var mixs [16]int32
		if i%4 == 0 {
			mixs[0] = 8000
		}
		s.runDSP(mixs)
		if got := s.dsp.efreg[0]; got != want[i] {
			t.Fatalf("sample %d: EFREG0 %d, want %d", i, got, want[i])
		}
	}
}

func TestTimerAInterruptCount44100(t *testing.T) {
	s, ram, regs := newTestSCSP()
	// Program: vectors at RAM 0, main loop at 200H, handler at
	// 300H. The handler increments the long at 1000H, clears the
	// timer interrupt through SCIRE, and returns.
	binary.BigEndian.PutUint32(ram[0:4], 0x00080000)
	binary.BigEndian.PutUint32(ram[4:8], 0x00000200)
	binary.BigEndian.PutUint32(ram[0x7C:0x80], 0x00000300) // autovector level 7
	ram[0x200], ram[0x201] = 0x60, 0xFE                    // BRA.S self
	handler := []byte{
		0x52, 0xB9, 0x00, 0x00, 0x10, 0x00, // ADDQ.L #1, (1000H)
		0x31, 0xFC, 0x00, 0x40, 0x00, 0x10, 0x04, 0x22, // MOVE.W #40H, SCIRE
		0x4E, 0x73, // RTE
	}
	copy(ram[0x300:], handler)

	s.SetSoundOn(false)
	s.SetSoundOn(true) // reset the CPU against the loaded program

	putReg(regs, RegTimerA, 0x00FE) // TACTL 0 (every sample), TIMA FEH
	putReg(regs, RegSCIEB, 0x0040)  // enable timer A to the sound CPU
	putReg(regs, RegSCILV0, 0x0040) // timer A level code 111 = level 7
	putReg(regs, RegSCILV1, 0x0040)
	putReg(regs, RegSCILV2, 0x0040)

	s.Step(60 * 477273) // exactly 60 frames of SH-2 cycles
	got := binary.BigEndian.Uint32(ram[0x1000:0x1004])
	if got != 44100 {
		t.Fatalf("timer A handler ran %d times in 60 frames, want exactly 44100", got)
	}
}

func TestMainCPURequestCallback(t *testing.T) {
	s, _, regs := newTestSCSP()
	calls := 0
	s.OnSoundRequest = func() { calls++ }
	putReg(regs, RegMCIEB, 0x0020) // enable the manual request bit
	putReg(regs, RegMCIPD, 0x0020) // sound CPU requests the main CPU
	tickN(s, 2)
	if calls != 1 {
		t.Fatalf("OnSoundRequest called %d times, want 1", calls)
	}
	tickN(s, 2)
	if calls != 1 {
		t.Fatalf("OnSoundRequest re-fired while pending: %d calls", calls)
	}
	putReg(regs, RegMCIRE, 0x0020) // main CPU clears the request
	tickN(s, 1)
	putReg(regs, RegMCIPD, 0x0020)
	tickN(s, 1)
	if calls != 2 {
		t.Fatalf("OnSoundRequest called %d times after re-request, want 2", calls)
	}
}

func TestDMATransfer(t *testing.T) {
	s, ram, regs := newTestSCSP()
	binary.BigEndian.PutUint16(ram[0x1000:0x1002], 0x1111)
	binary.BigEndian.PutUint16(ram[0x1002:0x1004], 0x2222)
	// DMEA word address 800H, DRGA register word 100H, 2 words,
	// RAM to register, DEXE set.
	putReg(regs, RegDMALow, 0x800<<1)
	putReg(regs, RegDMAHigh, 0x100<<1)
	putReg(regs, RegDMACtrl, 0x2000|2<<1)
	tickN(s, 1)
	if got := getReg(regs, 0x200); got != 0x1111 {
		t.Fatalf("DMA word 0 at register 200H is %04X, want 1111", got)
	}
	if got := getReg(regs, 0x202); got != 0x2222 {
		t.Fatalf("DMA word 1 at register 202H is %04X, want 2222", got)
	}
	if s.mciPend&(1<<IntDMAEnd) == 0 {
		t.Fatal("DMA end interrupt source not raised")
	}
	if getReg(regs, RegDMACtrl)&0x2000 != 0 {
		t.Fatal("DEXE was not cleared after the transfer")
	}
}

func TestCDInputRouting(t *testing.T) {
	s, _, regs := newTestSCSP()
	// EXTS0 (CD left) through slot 16 EFSDL 7 panned full left,
	// EXTS1 (CD right) through slot 17 EFSDL 7 panned full right.
	putReg(regs, 16*slotStride+SlotOutSel, 7<<5|0x1F)
	putReg(regs, 17*slotStride+SlotOutSel, 7<<5|0x0F)
	s.InputCDSamples([]int16{10000, -10000, 20000, -20000})
	tickN(s, 2)
	out := s.AudioBuffer()
	want := []int16{10000, -10000, 20000, -20000}
	if len(out) != len(want) {
		t.Fatalf("CD output len %d, want %d", len(out), len(want))
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("CD sample %d is %d, want %d", i, out[i], want[i])
		}
	}
}

func TestLFOAmplitudeWobble(t *testing.T) {
	s, ram, regs := newTestSCSP()
	const sa = 0x2000
	for i := 0; i < 64; i++ {
		binary.BigEndian.PutUint16(ram[sa+i*2:sa+i*2+2], 30000)
	}
	putReg(regs, SlotCtrl, 0x4100)
	putReg(regs, SlotSA, sa)
	putReg(regs, SlotLEA, 63)
	putReg(regs, SlotEG1, 31)
	putReg(regs, SlotEG2, 0x7800)
	putReg(regs, SlotOutSel, 0xE000)
	// LFOF 31 (172.3 Hz), sawtooth AM at depth 7 (24 dB).
	putReg(regs, SlotLFO, 31<<10|7)
	putReg(regs, SlotCtrl, 0xC100)
	tickN(s, 200)
	out := s.AudioBuffer()
	early := int(out[10*2])
	late := int(out[130*2])
	if late*2 >= early {
		t.Fatalf("ALFO wobble too small: early %d, late %d (want late under half of early)", early, late)
	}
}

func TestPCM8BitAndSoundOff(t *testing.T) {
	s, ram, regs := newTestSCSP()
	ram[0x2000] = 0x40             // 8 bit PCM sample 64, scales to 16384
	putReg(regs, SlotCtrl, 0x4180) // KYONB, forward loop, PCM8B
	putReg(regs, SlotSA, 0x2000)
	putReg(regs, SlotLEA, 0)
	putReg(regs, SlotVol, 0x4000) // SDIR: bypass EG and TL
	putReg(regs, SlotOutSel, 0xE000)
	putReg(regs, SlotCtrl, 0xC180)
	tickN(s, 2)
	out := s.AudioBuffer()
	if len(out) == 0 || out[0] != 16384 {
		t.Fatalf("PCM8B first sample %v, want 16384 first", out)
	}

	s.SetSoundOn(false)
	pc := s.CPU().PC()
	accBefore := s.acc
	s.Step(100000)
	if got := s.AudioBuffer(); len(got) != 0 {
		t.Fatalf("sound off produced %d samples, want none", len(got))
	}
	if s.CPU().PC() != pc {
		t.Fatal("sound off: the 68EC000 PC advanced while held in reset")
	}
	if s.acc != accBefore {
		t.Fatal("sound off: Step accumulated cycles while off")
	}
}

func TestSerializeMidPlaybackDeterministic(t *testing.T) {
	s, ram, regs := newTestSCSP()
	setupSineSlot(s, ram, regs, 0)
	tickN(s, 300)
	s.AudioBuffer()
	state := s.Serialize()
	if len(state) != SerializeBaseSize {
		t.Fatalf("serialize size %d, want base %d with an empty CD queue", len(state), SerializeBaseSize)
	}
	tickN(s, 200)
	want := s.AudioBuffer()

	s2 := New(ram, regs)
	if err := s2.Deserialize(state); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	tickN(s2, 200)
	got := s2.AudioBuffer()
	if len(got) != len(want) {
		t.Fatalf("restored audio len %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("restored audio differs at sample %d: %d vs %d", i, got[i], want[i])
		}
	}
}
