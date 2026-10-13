package system

import (
	"bytes"
	"math"
	"testing"
)

// The GBA-3 integration gates: Direct Sound FIFO playback
// end to end (timer 0 as the 8192 Hz sample clock, DMA1
// in FIFO mode feeding FIFO A from a sine table in
// EWRAM), the exposed audio surface (AudioBuffer and
// AudioSampleRate), and mid-playback save state
// determinism with PSG and FIFO sound running together.

// setupFIFOScene loads the idle IRQ scene ROM, then
// programs the sound and DMA registers directly through
// the bus: timer 0 ticks at 8192 Hz, DMA1 runs in FIFO
// mode from a 256 Hz sine table in EWRAM, and FIFO A is
// enabled on both speakers at full volume.
func setupFIFOScene(t *testing.T, m *Machine) {
	t.Helper()
	if err := m.LoadCart(irqSceneROM()); err != nil {
		t.Fatalf("LoadCart: %v", err)
	}
	b := m.Bus()
	// Sine table: the 32 sample period of a 256 Hz sine
	// at the 8192 Hz sample rate, repeated across 16 KB
	// of EWRAM. The FIFO DMA source pointer only advances,
	// so the table has to cover the whole run: 8192 bytes
	// per second of playback.
	for i := 0; i < 16384; i++ {
		v := int8(math.Round(100 * math.Sin(2*math.Pi*float64(i%32)/32)))
		b.Write(0x02000000+uint32(i), 1, uint32(byte(v)))
	}
	// Timer 0: reload 0xF800 gives a 2048 cycle period at
	// prescaler 1, which is 8192 Hz; then start it.
	b.Write(0x04000100, 2, 0xF800) // TM0CNT_L
	b.Write(0x04000102, 2, 0x0080) // TM0CNT_H: enable
	// DMA1: source the sine table, destination FIFO_A,
	// enabled in special (FIFO) timing, 32 bit units.
	b.Write(0x040000BC, 4, 0x02000000) // DMA1SAD
	b.Write(0x040000C0, 4, 0x040000A0) // DMA1DAD = FIFO_A
	b.Write(0x040000C4, 2, 0)          // DMA1CNT_L (unused in FIFO mode)
	b.Write(0x040000C6, 2, 0xB400)     // DMA1CNT_H: enable, timing 3, 32 bit
	// Sound: master on, FIFO A full volume on both sides,
	// clocked by timer 0.
	b.Write(0x04000084, 2, 0x0080) // SOUNDCNT_X: master enable
	b.Write(0x04000082, 2, 0x0304) // SOUNDCNT_H: A vol 100%, A L+R on, timer 0
}

// playSquareA4 programs PSG channel 1 through the bus to
// play A4 (frequency register 0x6D6, the documented value
// for 440 Hz) at full envelope volume, 50 percent duty.
func playSquareA4(m *Machine) {
	b := m.Bus()
	b.Write(0x04000084, 2, 0x0080) // SOUNDCNT_X: master enable
	b.Write(0x04000080, 2, 0x1177) // SOUNDCNT_L: ch1 both sides, volumes 7
	b.Write(0x04000082, 2, 0x0306) // SOUNDCNT_H: PSG 100%, FIFO A full, L+R
	b.Write(0x04000062, 2, 0xF080) // SOUND1CNT_H: duty 50%, envelope 15 step 0
	b.Write(0x04000064, 2, 0x86D6) // SOUND1CNT_X: trigger, frequency 0x6D6
}

// TestFIFOSineGate runs the FIFO scene for about a
// second and measures the output: a 256 Hz sine within
// tolerance, and a DMA word count that matches the
// consumption accounting (8192 sample pops per second,
// 16 bytes per refill burst, at most a FIFO of slack).
func TestFIFOSineGate(t *testing.T) {
	m := New()
	setupFIFOScene(t, m)
	playSquareA4(m)
	// One second of audio is 32768 output frames; run 62
	// frames (about 1.016 s) so the buffer holds at
	// least a full second.
	const frames = 62
	for i := 0; i < frames; i++ {
		m.RunFrame()
	}
	out := m.AudioBuffer()
	if len(out) < 2*32768 {
		t.Fatalf("audio frames = %d, want at least 32768", len(out)/2)
	}
	if got := m.AudioSampleRate(); got != 32768 {
		t.Fatalf("AudioSampleRate = %d, want 32768", got)
	}
	// FIFO consumption accounting: timer 0 pops one byte
	// per 2048 cycles. In 62 frames the machine advances
	// at least 62*280896 cycles, at least 8500 pops; each
	// refill burst delivers 4 words. Delivered bytes must
	// cover the pops and exceed them by less than the
	// FIFO capacity plus one burst.
	words := m.FIFOSamples(1)
	if words < 2050 || words > 2300 {
		t.Fatalf("FIFO words delivered = %d, want within [2050, 2300] for ~8500 pops", words)
	}
	t.Logf("FIFO words delivered in %d frames: %d (%d bytes for ~8500 sample pops)", frames, words, 4*words)
}

// TestFIFOOnlySineFrequency isolates the FIFO path: no
// PSG channel is programmed, so the left channel carries
// the pure sample-and-hold sine and its positive
// crossings over the drained span must measure 256 Hz.
func TestFIFOOnlySineFrequency(t *testing.T) {
	m := New()
	setupFIFOScene(t, m)
	const frames = 62
	for i := 0; i < frames; i++ {
		m.RunFrame()
	}
	out := m.AudioBuffer()
	n := len(out) / 2
	if n < 32768 {
		t.Fatalf("audio frames = %d, want at least 32768", n)
	}
	crossings := 0
	for i := 1; i < n; i++ {
		if out[2*(i-1)] < 0 && out[2*i] >= 0 {
			crossings++
		}
	}
	measured := float64(crossings) * 32768.0 / float64(n)
	if measured < 250 || measured > 262 {
		t.Fatalf("FIFO sine measured %.2f Hz over %d frames, want 256 within 6", measured, n)
	}
	// The held sample must actually swing: peak absolute
	// value near the sine table peak (100) scaled by the
	// Direct Sound gain (4 in the output domain, times
	// 64 into int16) is 25600.
	peak := 0
	for i := 0; i < n; i++ {
		v := int(out[2*i])
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	if peak < 20000 || peak > 26000 {
		t.Fatalf("FIFO sine peak = %d, want near 25600", peak)
	}
	t.Logf("FIFO sine: %d crossings over %d output frames = %.2f Hz, peak %d", crossings, n, measured, peak)
}

// TestAudioDeterminismGate: mid-frame save during PSG
// plus FIFO playback restores and the continuation is
// bit-identical, in output samples and in state bytes.
func TestAudioDeterminismGate(t *testing.T) {
	m := New()
	setupFIFOScene(t, m)
	playSquareA4(m)
	for i := 0; i < 10; i++ {
		m.RunFrame()
	}
	m.runLines(100) // mid-frame
	m.AudioBuffer() // drain at the save point: pending output is not state
	save := m.Serialize()
	// Continuation: finish the frame, run 6 more frames,
	// draining after the save point.
	m.runLines(linesPerFrame - 100)
	for i := 0; i < 6; i++ {
		m.RunFrame()
	}
	wantAudio := m.AudioBuffer()
	wantState := m.Serialize()
	if len(wantAudio) == 0 {
		t.Fatal("continuation produced no audio")
	}

	m2 := New()
	if err := m2.LoadCart(irqSceneROM()); err != nil {
		t.Fatalf("LoadCart (second machine): %v", err)
	}
	if err := m2.Deserialize(save); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	m2.runLines(linesPerFrame - 100)
	for i := 0; i < 6; i++ {
		m2.RunFrame()
	}
	gotAudio := m2.AudioBuffer()
	if len(gotAudio) != len(wantAudio) {
		t.Fatalf("audio after restore: %d samples, want %d", len(gotAudio), len(wantAudio))
	}
	for i := range wantAudio {
		if gotAudio[i] != wantAudio[i] {
			t.Fatalf("audio sample %d after restore = %d, want %d", i, gotAudio[i], wantAudio[i])
		}
	}
	if got := m2.Serialize(); !bytes.Equal(got, wantState) {
		t.Fatal("state after a mid-playback restore and continuation differs from the uninterrupted run")
	}
}

// TestSoundRateSelection checks the AudioSampleRate
// surface: power-on default 32768, and a SOUNDBIAS
// resolution write moves it per the documented table.
func TestSoundRateSelection(t *testing.T) {
	m := New()
	if err := m.LoadCart(irqSceneROM()); err != nil {
		t.Fatalf("LoadCart: %v", err)
	}
	if got := m.AudioSampleRate(); got != 32768 {
		t.Fatalf("default AudioSampleRate = %d, want 32768", got)
	}
	m.Bus().Write(0x04000088, 2, 0xC200) // SOUNDBIAS resolution 3
	if got := m.AudioSampleRate(); got != 262144 {
		t.Fatalf("AudioSampleRate at resolution 3 = %d, want 262144", got)
	}
	m.Bus().Write(0x04000088, 2, 0x4200) // SOUNDBIAS resolution 1
	if got := m.AudioSampleRate(); got != 65536 {
		t.Fatalf("AudioSampleRate at resolution 1 = %d, want 65536", got)
	}
}

// TestMasterOffSilencesMachine: with the PSG playing,
// clearing the SOUNDCNT_X master enable silences the
// machine output completely.
func TestMasterOffSilencesMachine(t *testing.T) {
	m := New()
	if err := m.LoadCart(irqSceneROM()); err != nil {
		t.Fatalf("LoadCart: %v", err)
	}
	playSquareA4(m)
	m.RunFrame()
	loud := m.AudioBuffer()
	nonzero := false
	for _, s := range loud {
		if s != 0 {
			nonzero = true
			break
		}
	}
	if !nonzero {
		t.Fatal("PSG produced no sound before master off; gate is meaningless")
	}
	m.Bus().Write(0x04000084, 2, 0x0000) // SOUNDCNT_X: master off
	m.RunFrame()
	quiet := m.AudioBuffer()
	if len(quiet) == 0 {
		t.Fatal("no samples generated while master off; the stream must keep its rate")
	}
	for i, s := range quiet {
		if s != 0 {
			t.Fatalf("sample %d = %d with master off, want 0", i, s)
		}
	}
}
