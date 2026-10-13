package spu

import (
	"math"
	"testing"
)

func TestTransferPortSequential(t *testing.T) {
	s := New()
	// The transfer address is in documented 8-byte
	// units (psx-spx SPU Memory Access: TSA is the
	// address in the sound buffer divided by eight),
	// so SetAddress(0x0010) means byte address 0x80.
	// PS1-2 used 4-byte units here; corrected in
	// PS1-3.
	s.SetAddress(0x0010) // byte address 0x80
	s.WriteData(0xBEEF)
	s.WriteData(0xCAFE)
	if s.Address() != 0x84 {
		t.Fatalf("address = 0x%X, want 0x84", s.Address())
	}
	s.SetAddress(0x0010)
	if v := s.ReadData(); v != 0xBEEF {
		t.Errorf("read = 0x%04X, want 0xBEEF", v)
	}
	if v := s.ReadData(); v != 0xCAFE {
		t.Errorf("read = 0x%04X, want 0xCAFE", v)
	}
	// The bytes landed in RAM at the register-derived
	// address.
	if s.RAM[0x80] != 0xEF || s.RAM[0x81] != 0xBE {
		t.Errorf("RAM bytes = %02X %02X, want EF BE", s.RAM[0x80], s.RAM[0x81])
	}
}

// TestTransferPortRegisters drives the transfer port
// through the documented register addresses: TSA at
// 1F801DA6h, DATA at 1F801DA8h, IRQA at 1F801DA4h.
func TestTransferPortRegisters(t *testing.T) {
	s := New()
	wr(t, s, 0x1F801DA6, 0x0010) // TSA: byte address 0x80
	if got := s.Address(); got != 0x80 {
		t.Fatalf("Address after TSA write = 0x%X, want 0x80", got)
	}
	wr(t, s, 0x1F801DA8, 0xBEEF) // DATA port
	wr(t, s, 0x1F801DA8, 0xCAFE)
	if got := s.Address(); got != 0x84 {
		t.Fatalf("Address after DATA writes = 0x%X, want 0x84", got)
	}
	if s.RAM[0x80] != 0xEF || s.RAM[0x81] != 0xBE || s.RAM[0x82] != 0xFE || s.RAM[0x83] != 0xCA {
		t.Errorf("RAM at 0x80 = %02X %02X %02X %02X, want EF BE FE CA",
			s.RAM[0x80], s.RAM[0x81], s.RAM[0x82], s.RAM[0x83])
	}
	// TSA readback returns the stored register value
	// (it does not increment during transfers).
	if v := rd(t, s, 0x1F801DA6); v != 0x0010 {
		t.Errorf("TSA readback = 0x%04X, want 0x0010", v)
	}
	if v := rd(t, s, 0x1F801DA4); v != 0 {
		t.Errorf("IRQA readback before write = 0x%04X, want 0", v)
	}
	// Read back through TSA + DATA at the documented
	// addresses.
	wr(t, s, 0x1F801DA6, 0x0010)
	if v := rd(t, s, 0x1F801DA8); v != 0xBEEF {
		t.Errorf("DATA read = 0x%04X, want 0xBEEF", v)
	}
	if v := rd(t, s, 0x1F801DA8); v != 0xCAFE {
		t.Errorf("DATA read = 0x%04X, want 0xCAFE", v)
	}
	// IRQA at DA4 stores its own value and must not
	// disturb the transfer address.
	wr(t, s, 0x1F801DA4, 0x0200)
	if v := rd(t, s, 0x1F801DA4); v != 0x0200 {
		t.Errorf("IRQA readback = 0x%04X, want 0x0200", v)
	}
	if got := s.Address(); got != 0x84 {
		t.Errorf("Address after IRQA write = 0x%X, want 0x84 (unchanged)", got)
	}
}

func TestWordAccess(t *testing.T) {
	s := New()
	s.SetAddress(0)
	s.WriteWord(0xDEADBEEF)
	s.SetAddress(0)
	if v := s.ReadWord(); v != 0xDEADBEEF {
		t.Errorf("word = 0x%08X, want 0xDEADBEEF", v)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	s := New()
	s.SetAddress(0x0100)
	s.WriteWord(0x11223344)
	b := s.Serialize()
	if len(b) != StateSize {
		t.Fatalf("state = %d, want %d", len(b), StateSize)
	}
	s2 := New()
	s2.Deserialize(b)
	if s2.RAM != s.RAM || s2.Address() != s.Address() || s2.AddrReg != s.AddrReg {
		t.Errorf("SPU state diverged after round trip")
	}
}

// ----------------------------------------------------------------
// PS1-3 full chip gates. All fixtures are synthetic:
// ADPCM data is built here with an encoder that uses
// the documented decoder math (filter pairs
// 0/60/115/98/122 and 0/0/-52/-55/-60 from the
// psx-spx XA chapter, sample = (nibble<<12)>>shift +
// (old*f0 + older*f1 + 32)/64, clamped to int16).
// ----------------------------------------------------------------

var encF0 = [5]int32{0, 60, 115, 98, 122}
var encF1 = [5]int32{0, 0, -52, -55, -60}

func encRecon(nib, shift, f0, f1, old, older int32) int32 {
	s := (nib<<12)>>uint(shift) + (old*f0+older*f1+32)/64
	if s > 0x7FFF {
		s = 0x7FFF
	}
	if s < -0x8000 {
		s = -0x8000
	}
	return s
}

// encodeBlock encodes 28 PCM samples into one
// 16-byte ADPCM block, greedily choosing the
// filter/shift/nibbles that minimize squared error
// under the documented decoder.
func encodeBlock(pcm []int16, old, older int32) ([16]byte, int32, int32) {
	var best [16]byte
	bestErr := int64(-1)
	var bestOld, bestOlder int32
	for filter := 0; filter <= 4; filter++ {
		for shift := 0; shift <= 12; shift++ {
			var blk [16]byte
			blk[0] = byte(filter<<4 | shift)
			o, oo := old, older
			err := int64(0)
			var nibs [28]int32
			for i := 0; i < 28; i++ {
				target := int32(pcm[i])
				bn, be := int32(0), int64(-1)
				for nib := int32(-8); nib <= 7; nib++ {
					r := encRecon(nib, int32(shift), encF0[filter], encF1[filter], o, oo)
					d := int64(r - target)
					if be < 0 || d*d < be {
						be, bn = d*d, nib
					}
				}
				r := encRecon(bn, int32(shift), encF0[filter], encF1[filter], o, oo)
				oo, o = o, r
				err += be
				nibs[i] = bn
			}
			if bestErr < 0 || err < bestErr {
				bestErr, bestOld, bestOlder = err, o, oo
				copy(best[:], blk[:])
				for i := 0; i < 28; i++ {
					u := uint8(nibs[i] & 0xF)
					if i&1 == 0 {
						best[2+i/2] |= u
					} else {
						best[2+i/2] |= u << 4
					}
				}
			}
		}
	}
	return best, bestOld, bestOlder
}

// encodeStream encodes pcm (a multiple of 28) into
// consecutive blocks, carrying decoder state.
func encodeStream(pcm []int16) []byte {
	var out []byte
	var old, older int32
	for i := 0; i+28 <= len(pcm); i += 28 {
		blk, o, oo := encodeBlock(pcm[i:i+28], old, older)
		old, older = o, oo
		out = append(out, blk[:]...)
	}
	return out
}

// encodeLoop encodes pcm as a seamlessly looping
// stream: several encoding passes around the loop so
// the decoder state at the end matches the start,
// then loop start/end flags are set on the first and
// last blocks.
func encodeLoop(pcm []int16) []byte {
	var out []byte
	var old, older int32
	for pass := 0; pass < 3; pass++ {
		out = nil
		for i := 0; i+28 <= len(pcm); i += 28 {
			blk, o, oo := encodeBlock(pcm[i:i+28], old, older)
			old, older = o, oo
			out = append(out, blk[:]...)
		}
	}
	out[1] |= 4           // loop start on first block
	out[len(out)-15] |= 3 // loop end + repeat on last block
	return out
}

func sinePCM(n, periods int, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * math.Sin(2*math.Pi*float64(periods)*float64(i)/float64(n)))
	}
	return out
}

func wr(t *testing.T, s *SPU, addr uint32, v uint16) {
	t.Helper()
	if !s.WriteReg(addr, 2, uint32(v)) {
		t.Fatalf("WriteReg(%#x) not claimed", addr)
	}
}

func rd(t *testing.T, s *SPU, addr uint32) uint16 {
	t.Helper()
	v, ok := s.ReadReg(addr, 2)
	if !ok {
		t.Fatalf("ReadReg(%#x) not claimed", addr)
	}
	return uint16(v)
}

// adsrFastHold: attack shift 0 step 0 (full in a few
// samples), decay shift 0, sustain level 15, sustain
// shift 31 step 3 (all-ones rate, never steps),
// release shift 31.
const adsrFastHoldLo = 0x000F
const adsrFastHoldHi = 0x1FDF

func setupChip(t *testing.T, mainVol uint16) *SPU {
	t.Helper()
	s := New()
	wr(t, s, 0x1F801D80, mainVol)
	wr(t, s, 0x1F801D82, mainVol)
	wr(t, s, 0x1F801DB0, 0) // CD volume 0
	wr(t, s, 0x1F801DB2, 0)
	wr(t, s, 0x1F801DAA, 0xC000) // SPU on, unmuted
	return s
}

func setupVoice(t *testing.T, s *SPU, n int, addrBytes uint32, pitch, adsrLo, adsrHi uint16) {
	t.Helper()
	base := uint32(0x1F801C00 + n*0x10)
	wr(t, s, base+0, 0x3FFF) // VOLL full (direct mode)
	wr(t, s, base+2, 0x3FFF) // VOLR full
	wr(t, s, base+4, pitch)
	wr(t, s, base+6, uint16(addrBytes/8))
	wr(t, s, base+8, adsrLo)
	wr(t, s, base+0xA, adsrHi)
}

func keyOn(t *testing.T, s *SPU, mask uint32) {
	t.Helper()
	wr(t, s, 0x1F801D88, uint16(mask))
	wr(t, s, 0x1F801D8A, uint16(mask>>16))
}

func advanceSamples(s *SPU, n int) {
	s.Advance(n*768, nil)
}

func leftChannel(s *SPU) []int16 {
	buf := s.AudioBuffer()
	out := make([]int16, len(buf)/2)
	for i := range out {
		out[i] = buf[2*i]
	}
	return out
}

func posCrossings(x []int16) int {
	c := 0
	for i := 1; i < len(x); i++ {
		if x[i-1] < 0 && x[i] >= 0 {
			c++
		}
	}
	return c
}

func maxAbs(x []int16) int {
	m := 0
	for _, v := range x {
		if int(v) < 0 {
			if -int(v) > m {
				m = -int(v)
			}
		} else if int(v) > m {
			m = int(v)
		}
	}
	return m
}

func TestADPCMTone440(t *testing.T) {
	// 2800 samples, 28 periods: 441 Hz at 44100 Hz.
	data := encodeLoop(sinePCM(2800, 28, 12000))
	measure := func(pitch uint16) float64 {
		s := setupChip(t, 0x7FFF)
		copy(s.RAM[0x1000:], data)
		setupVoice(t, s, 0, 0x1000, pitch, adsrFastHoldLo, adsrFastHoldHi)
		keyOn(t, s, 1)
		advanceSamples(s, 44100)
		l := leftChannel(s)
		win := l[len(l)-22050:]
		f := float64(posCrossings(win)) / 0.5
		t.Logf("pitch %#x: crossings %d in 0.5s = %.1f Hz, peak %d",
			pitch, posCrossings(win), f, maxAbs(win))
		return f
	}
	f1 := measure(0x1000)
	if f1 < 430 || f1 > 450 {
		t.Errorf("440 Hz gate: measured %.1f Hz, want 440 +/- 10", f1)
	}
	f2 := measure(0x2000)
	if f2 < 860 || f2 > 900 {
		t.Errorf("pitch double gate: measured %.1f Hz, want %.1f +/- 20", f2, 2*f1)
	}
}

func TestADSREnvelopeCheckpoints(t *testing.T) {
	// Attack shift 11 step 0: step = 7 per sample,
	// one step per sample. KON delay is 5 samples.
	// Release shift 11: step = -8 per sample.
	s := setupChip(t, 0x7FFF)
	data := encodeLoop(sinePCM(2800, 28, 8000))
	copy(s.RAM[0x1000:], data)
	adsrLo := uint16(11<<10 | 0<<4 | 0xF) // attack shift 11, decay 0, sustain level 15
	adsrHi := uint16(31<<8 | 3<<6 | 11)   // sustain never steps, release shift 11
	setupVoice(t, s, 0, 0x1000, 0x1000, adsrLo, adsrHi)
	keyOn(t, s, 1)
	advanceSamples(s, 5+100)
	if e := s.VoiceEnvelope(0); e < 700-28 || e > 700+28 {
		t.Errorf("attack checkpoint 100 steps: envelope %d, want 700 +/- 28", e)
	}
	advanceSamples(s, 900)
	if e := s.VoiceEnvelope(0); e < 7000-28 || e > 7000+28 {
		t.Errorf("attack checkpoint 1000 steps: envelope %d, want 7000 +/- 28", e)
	}
	advanceSamples(s, 4000)
	if e := s.VoiceEnvelope(0); e < 0x7F00 {
		t.Errorf("attack end: envelope %d, want near 0x7FFF", e)
	}
	// KOFF: release at -8 per sample.
	wr(t, s, 0x1F801D8C, 1)
	advanceSamples(s, 500)
	if e := s.VoiceEnvelope(0); e < 28767-64 || e > 28767+64 {
		t.Errorf("release checkpoint 500 steps: envelope %d, want 28767 +/- 64", e)
	}
	advanceSamples(s, 5000)
	if e := s.VoiceEnvelope(0); e != 0 {
		t.Errorf("release end: envelope %d, want 0", e)
	}
	if s.VoicePlaying(0) {
		t.Errorf("voice still playing after full release")
	}
	s.AudioBuffer() // drain everything generated so far
	advanceSamples(s, 200)
	if m := maxAbs(leftChannel(s)); m != 0 {
		t.Errorf("output after release not silent: peak %d", m)
	}
}

func TestLoopENDXAndIRQ(t *testing.T) {
	s := setupChip(t, 0x7FFF)
	data := encodeStream(sinePCM(56, 2, 8000))
	data[1] |= 4    // loop start, block 0
	data[16+1] |= 3 // loop end + repeat, block 1
	copy(s.RAM[0x1000:], data)
	wr(t, s, 0x1F801DA4, 0x1000/8) // IRQA (at DA4) = loop start
	irqs := 0
	s.OnIRQ = func() { irqs++ }
	wr(t, s, 0x1F801DAA, 0xC040) // enable IRQ in SPUCNT
	setupVoice(t, s, 0, 0x1000, 0x1000, adsrFastHoldLo, adsrFastHoldHi)
	keyOn(t, s, 1)
	advanceSamples(s, 2000)
	if !s.VoicePlaying(0) {
		t.Errorf("looping voice stopped")
	}
	if e := rd(t, s, 0x1F801D9C); e&1 == 0 {
		t.Errorf("ENDX bit 0 not set, ENDX0 = %#x", e)
	}
	if irqs < 1 {
		t.Errorf("OnIRQ did not fire for loop address, count %d", irqs)
	}
	t.Logf("loop: ENDX set, IRQ count %d, envelope %d", irqs, s.VoiceEnvelope(0))
}

func TestPitchModulationFM(t *testing.T) {
	run := func(pmon bool) []int16 {
		s := setupChip(t, 0x7FFF)
		constData := make([]int16, 2800)
		for i := range constData {
			constData[i] = 16000
		}
		copy(s.RAM[0x1000:], encodeLoop(constData))
		copy(s.RAM[0x3000:], encodeLoop(sinePCM(2800, 28, 12000)))
		// Voice 0: constant modulator, silent volumes.
		setupVoice(t, s, 0, 0x1000, 0x1000, adsrFastHoldLo, adsrFastHoldHi)
		wr(t, s, 0x1F801C00, 0)
		wr(t, s, 0x1F801C02, 0)
		// Voice 1: carrier tone.
		setupVoice(t, s, 1, 0x3000, 0x1000, adsrFastHoldLo, adsrFastHoldHi)
		if pmon {
			wr(t, s, 0x1F801D90, 0x2) // PMON voice 1
		}
		keyOn(t, s, 0x3)
		advanceSamples(s, 44100)
		l := leftChannel(s)
		return l[len(l)-22050:]
	}
	base := run(false)
	fm := run(true)
	diff := 0
	for i := range base {
		if base[i] != fm[i] {
			diff++
		}
	}
	bc, fc := posCrossings(base), posCrossings(fm)
	t.Logf("FM: base crossings %d, FM crossings %d, differing samples %d/%d",
		bc, fc, diff, len(base))
	if float64(diff)/float64(len(base)) < 0.5 {
		t.Errorf("FM output barely differs from baseline: %d/%d", diff, len(base))
	}
	// The modulator sits near +16000, so the pitch
	// factor is about (16000+32768)/32768 = 1.49 and
	// the crossing count must RISE by roughly that.
	if float64(fc) < float64(bc)*1.25 {
		t.Errorf("FM crossings did not rise as expected: base %d, FM %d", bc, fc)
	}
}

func TestReverbEcho(t *testing.T) {
	s := setupChip(t, 0x7FFF)
	// Impulse block: silence except sample 12 at
	// 24000 (after the 5-sample KON delay and the
	// fast attack), flags = end+mute.
	pcm := make([]int16, 28)
	pcm[12] = 24000
	imp := encodeStream(pcm)
	imp[1] |= 1
	copy(s.RAM[0x1000:], imp)
	// Silent loop block at 0x1010 (loop start + end +
	// repeat, zero data) as the jump target.
	s.RAM[0x1010+1] = 7
	setupVoice(t, s, 0, 0x1000, 0x1000, adsrFastHoldLo, adsrFastHoldHi)
	wr(t, s, 0x1F801C0E, 0x1010/8) // LSAX = silent block
	// Reverb configuration: ESA at byte 0x20000,
	// SAME writes input (vIIR full, vWALL 0), COMB1
	// reads it back (mLSAME 0x100, mLCOMB1 0x0F0:
	// 16 address units * 8 bytes / 2 bytes per tick
	// = 64 ticks at 22050 Hz = 128 output samples),
	// APF stages pass through with dAPF 0 and full
	// gains on untouched addresses.
	wr(t, s, 0x1F801DA2, 0x20000/8)
	rev := [32]uint16{
		0, 0, 0x7FFF, 0x7FFF, 0, 0, 0, 0, // dAPF1 dAPF2 vIIR vCOMB1..4 vWALL
		0x7FFF, 0x7FFF, 0x100, 0x100, 0x0F0, 0x0F0, 1, 1, // vAPF1 vAPF2 mLSAME mRSAME combs
		0, 0, 0x100, 0x100, 1, 1, 1, 1, // dLSAME dRSAME diffs combs
		0, 0, 0x80, 0x80, 0x90, 0x90, 0x7FFF, 0x7FFF, // dDIFF mLAPF mRAPF vLIN vRIN
	}
	for i, v := range rev {
		wr(t, s, uint32(0x1F801DC0+i*2), v)
	}
	wr(t, s, 0x1F801D84, 0x7FFF) // EVOLL
	wr(t, s, 0x1F801D86, 0x7FFF) // EVOLR
	wr(t, s, 0x1F801D98, 1)      // EON voice 0
	wr(t, s, 0x1F801DAA, 0xC080) // SPUCNT: on, unmuted, reverb master on
	keyOn(t, s, 1)
	advanceSamples(s, 400)
	l := leftChannel(s)
	direct := maxAbs(l[0:40])
	gap := maxAbs(l[40:110])
	echoPeak, echoAt := 0, 0
	for i := 115; i < 190 && i < len(l); i++ {
		if a := maxAbs(l[i : i+1]); a > echoPeak {
			echoPeak, echoAt = a, i
		}
	}
	t.Logf("reverb: direct peak %d, gap peak %d, echo peak %d at sample %d (expect ~140)",
		direct, gap, echoPeak, echoAt)
	if direct < 5000 {
		t.Errorf("direct impulse too small: %d", direct)
	}
	if gap > 800 {
		t.Errorf("output not quiet between direct and echo: %d", gap)
	}
	if echoPeak < 1500 {
		t.Errorf("no reverb echo energy in window 115..190: peak %d", echoPeak)
	}
}

func TestNoiseVoice(t *testing.T) {
	s := New()
	wr(t, s, 0x1F801D80, 0x7FFF)
	wr(t, s, 0x1F801D82, 0x7FFF)
	// SPUCNT: on, unmuted, noise shift 15, step 3.
	wr(t, s, 0x1F801DAA, 0xFF00)
	copy(s.RAM[0x1000:], encodeStream(make([]int16, 28)))
	setupVoice(t, s, 0, 0x1000, 0x1000, adsrFastHoldLo, adsrFastHoldHi)
	wr(t, s, 0x1F801D94, 1) // NON voice 0
	keyOn(t, s, 1)
	advanceSamples(s, 4410)
	l := leftChannel(s)[100:]
	distinct := map[int16]bool{}
	nonzero := 0
	for _, v := range l {
		distinct[v] = true
		if v != 0 {
			nonzero++
		}
	}
	t.Logf("noise: distinct values %d, peak %d, nonzero %d/%d",
		len(distinct), maxAbs(l), nonzero, len(l))
	if len(distinct) < 500 {
		t.Errorf("noise output not varying: %d distinct values", len(distinct))
	}
	if maxAbs(l) < 5000 {
		t.Errorf("noise output too quiet: peak %d", maxAbs(l))
	}
}

func TestMasterVolumeRatio(t *testing.T) {
	peak := func(mainVol uint16) int {
		s := setupChip(t, mainVol)
		copy(s.RAM[0x1000:], encodeLoop(sinePCM(2800, 28, 12000)))
		setupVoice(t, s, 0, 0x1000, 0x1000, adsrFastHoldLo, adsrFastHoldHi)
		keyOn(t, s, 1)
		advanceSamples(s, 44100)
		l := leftChannel(s)
		return maxAbs(l[len(l)-22050:])
	}
	full, half := peak(0x7FFF), peak(0x3FFF)
	ratio := float64(half) / float64(full)
	t.Logf("master volume: full peak %d, half peak %d, ratio %.3f", full, half, ratio)
	if ratio < 0.44 || ratio > 0.56 {
		t.Errorf("master volume ratio %.3f, want 0.5 within tolerance", ratio)
	}
}

func TestStateDeterminismMidPlayback(t *testing.T) {
	s := setupChip(t, 0x7FFF)
	copy(s.RAM[0x1000:], encodeLoop(sinePCM(2800, 28, 12000)))
	setupVoice(t, s, 0, 0x1000, 0x1000, adsrFastHoldLo, adsrFastHoldHi)
	keyOn(t, s, 1)
	s.Advance(100000, nil) // not a multiple of 768: tests the remainder
	s.AudioBuffer()        // drain
	b := s.Serialize()
	if len(b) != StateSize {
		t.Fatalf("state = %d, want %d", len(b), StateSize)
	}
	s2 := New()
	s2.Deserialize(b)
	const cycles = 4096 * 768
	s.Advance(cycles, nil)
	s2.Advance(cycles, nil)
	a, c := s.AudioBuffer(), s2.AudioBuffer()
	if len(a) != 8192 || len(c) != 8192 {
		t.Fatalf("sample counts %d and %d, want 8192 each", len(a), len(c))
	}
	for i := range a {
		if a[i] != c[i] {
			t.Fatalf("sample %d diverged after restore: %d vs %d", i, a[i], c[i])
		}
	}
	if s.VoiceEnvelope(0) != s2.VoiceEnvelope(0) {
		t.Errorf("envelope diverged after restore")
	}
	t.Logf("state determinism: 4096 samples bit-identical, StateSize %d", StateSize)
}
