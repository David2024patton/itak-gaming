package apu

import (
	"bytes"
	"testing"
)

// Timers over exactly one second (1,024,000 SPC cycles):
// T0 ticks its stage at 8000 Hz and T2 at 64000 Hz. With
// targets 128 and 100 the output counters increment
// 8000/128 = 62.5 -> 62 times and 64000/100 = 640 times.
// The outputs are only 4 bits, so the test polls the
// read-and-clear ports in 1600 cycle slices and sums.
func TestTimersOneSecond(t *testing.T) {
	a := New()
	a.Write(0xFA, 128)  // T0 target
	a.Write(0xFC, 100)  // T2 target
	a.Write(0xF1, 0x85) // CONTROL: IPL on, T0 and T2 enabled

	sum0, sum2 := 0, 0
	for i := 0; i < 640; i++ { // 640 * 1600 = 1,024,000 cycles
		a.Step(1600)
		sum0 += int(a.Read(0xFD))
		sum2 += int(a.Read(0xFF))
	}
	if sum0 != 62 {
		t.Fatalf("T0 increments in 1 s = %d, want 62", sum0)
	}
	if sum2 != 640 {
		t.Fatalf("T2 increments in 1 s = %d, want 640", sum2)
	}
	// Both counters were drained by the polling reads.
	if got := a.Read(0xFD); got != 0 {
		t.Fatalf("T0OUT after drain = %d, want 0", got)
	}
}

// Read of 00FDh..00FFh returns the 4 bit counter and
// clears it to zero.
func TestTimerOutputReadClears(t *testing.T) {
	a := New()
	a.Write(0xFC, 1)    // T2 target 1: one increment per 16 cycles
	a.Write(0xF1, 0x84) // IPL on, T2 enabled
	a.Step(16 * 5)
	if got := a.Read(0xFF); got != 5 {
		t.Fatalf("T2OUT = %d, want 5", got)
	}
	if got := a.Read(0xFF); got != 0 {
		t.Fatalf("T2OUT second read = %d, want 0 (read clears)", got)
	}
	// Write only registers read back as 0.
	if got := a.Read(0xF1); got != 0 {
		t.Fatalf("CONTROL read = %#x, want 0", got)
	}
	if got := a.Read(0xFC); got != 0 {
		t.Fatalf("T2DIV read = %#x, want 0", got)
	}
}

// stepUntilPort steps the APU until output port 0 shows
// want, with a generous bound.
func stepUntilPort(t *testing.T, a *APU, want uint8) {
	t.Helper()
	for i := 0; i < 200000; i++ {
		if a.ReadPort(0) == want {
			return
		}
		a.Step(8)
	}
	t.Fatalf("port 0 never became %#x (now %#x, port1 %#x)", want, a.ReadPort(0), a.ReadPort(1))
}

// uploadBlock runs one IPL transfer block through the
// ports only, following the uploader pseudocode in
// fullsnes, and returns the kick for the next command:
// (len+2) OR 1.
func uploadBlock(t *testing.T, a *APU, dest uint16, data []byte, kick uint8) uint8 {
	t.Helper()
	a.WritePort(2, uint8(dest))
	a.WritePort(3, uint8(dest>>8))
	a.WritePort(1, 0x01) // command: transfer
	a.WritePort(0, kick)
	stepUntilPort(t, a, kick)
	for i, b := range data {
		a.WritePort(1, b)
		a.WritePort(0, uint8(i))
		stepUntilPort(t, a, uint8(i))
	}
	return uint8(len(data)+2) | 1
}

// toneProgram is a hand assembled SPC700 program. It
// writes magic values to the four output ports, programs
// the DSP for a 440 Hz tone on voice 0 (the sample data
// is uploaded separately to 1000h), keys the voice on,
// and loops forever.
//
//	8F 11 F4    mov [F4],#11   ; output port 0 = 11h
//	8F 22 F5    mov [F5],#22   ; output port 1 = 22h
//	8F 33 F6    mov [F6],#33   ; output port 2 = 33h
//	8F 44 F7    mov [F7],#44   ; output port 3 = 44h
//	8F 5D F2 / 8F 10 F3   DSP DIR   = 10h (table at 1000h)
//	8F 0C F2 / 8F 7F F3   DSP MVOLL = 7Fh
//	8F 2C F2 / 8F 7F F3   DSP MVOLR = 7Fh
//	8F 6C F2 / 8F 20 F3   DSP FLG   = 20h (echo writes off)
//	8F 00 F2 / 8F 7F F3   DSP V0VOLL = 7Fh
//	8F 01 F2 / 8F 7F F3   DSP V0VOLR = 7Fh
//	8F 02 F2 / 8F 85 F3   DSP PITCHL = 85h (pitch 0x385)
//	8F 03 F2 / 8F 03 F3   DSP PITCHH = 03h
//	8F 04 F2 / 8F 00 F3   DSP SRCN   = 00h
//	8F 05 F2 / 8F 8F F3   DSP ADSR1  = 8Fh
//	8F 06 F2 / 8F E0 F3   DSP ADSR2  = E0h
//	8F 4C F2 / 8F 01 F3   DSP KON    = 01h
//	2F FE       bra $               ; loop forever
var toneProgram = []byte{
	0x8F, 0x11, 0xF4,
	0x8F, 0x22, 0xF5,
	0x8F, 0x33, 0xF6,
	0x8F, 0x44, 0xF7,
	0x8F, 0x5D, 0xF2, 0x8F, 0x10, 0xF3,
	0x8F, 0x0C, 0xF2, 0x8F, 0x7F, 0xF3,
	0x8F, 0x2C, 0xF2, 0x8F, 0x7F, 0xF3,
	0x8F, 0x6C, 0xF2, 0x8F, 0x20, 0xF3,
	0x8F, 0x00, 0xF2, 0x8F, 0x7F, 0xF3,
	0x8F, 0x01, 0xF2, 0x8F, 0x7F, 0xF3,
	0x8F, 0x02, 0xF2, 0x8F, 0x85, 0xF3,
	0x8F, 0x03, 0xF2, 0x8F, 0x03, 0xF3,
	0x8F, 0x04, 0xF2, 0x8F, 0x00, 0xF3,
	0x8F, 0x05, 0xF2, 0x8F, 0x8F, 0xF3,
	0x8F, 0x06, 0xF2, 0x8F, 0xE0, 0xF3,
	0x8F, 0x4C, 0xF2, 0x8F, 0x01, 0xF3,
	0x2F, 0xFE,
}

// toneData is uploaded to 1000h: a sample directory entry
// (start 1100h, loop 1100h) and, at 1080h in the same
// block layout below, nothing; the BRR block itself is a
// separate upload to 1100h. Block layout at 1000h is just
// the 4 directory bytes; the rest of the gap is RAM zeros.
var toneDirBlock = []byte{0x00, 0x11, 0x00, 0x11}

// toneBRR is the looping 16 sample sine block from the
// pitch test (header 43h, range 4, filter 0, end+loop).
var toneBRR = []byte{0x43, 0x03, 0x56, 0x76, 0x53, 0x0D, 0xBA, 0x9A, 0xBD}

// bootTone boots a fresh APU through the IPL protocol,
// uploads program and data, and jumps to the program.
func bootTone(t *testing.T) *APU {
	t.Helper()
	a := New()
	// Wait for the IPL ready signature AAh / BBh.
	for i := 0; i < 200000 && !(a.ReadPort(0) == 0xAA && a.ReadPort(1) == 0xBB); i++ {
		a.Step(8)
	}
	if a.ReadPort(0) != 0xAA || a.ReadPort(1) != 0xBB {
		t.Fatalf("no IPL signature: port0=%#x port1=%#x", a.ReadPort(0), a.ReadPort(1))
	}
	kick := uploadBlock(t, a, 0x1000, toneDirBlock, 0xCC)
	kick = uploadBlock(t, a, 0x1100, toneBRR, kick)
	kick = uploadBlock(t, a, 0x0200, toneProgram, kick)
	// Final command: entry point at 0200h, command byte 0.
	a.WritePort(2, 0x00)
	a.WritePort(3, 0x02)
	a.WritePort(1, 0x00)
	a.WritePort(0, kick)
	stepUntilPort(t, a, kick)
	return a
}

// IPL end to end: boot, upload, run. The program must
// present its magic on the output ports and the DSP must
// produce non silent audio.
func TestIPLEndToEnd(t *testing.T) {
	a := bootTone(t)
	a.Step(32000) // 1/32 s of SPC time is plenty to run the setup
	for i, want := range []uint8{0x11, 0x22, 0x33, 0x44} {
		if got := a.ReadPort(i); got != want {
			t.Fatalf("port %d = %#x, want %#x", i, got, want)
		}
	}
	a.DrainAudio()
	a.Step(32000 * 10)
	samples := a.DrainAudio()
	if len(samples) == 0 {
		t.Fatal("no audio produced")
	}
	peak := 0
	for _, s := range samples {
		if int(s) > peak {
			peak = int(s)
		}
		if -int(s) > peak {
			peak = -int(s)
		}
	}
	if peak < 10 {
		t.Fatalf("audio is silent: peak %d over %d samples", peak, len(samples))
	}
	t.Logf("IPL tone: %d samples, peak %d", len(samples), peak)
}

// Serialize roundtrip: serialize mid performance, keep
// playing the original, restore into a fresh APU, and
// require bit identical audio and bit identical state
// afterwards.
func TestSerializeRoundtrip(t *testing.T) {
	a := bootTone(t)
	a.Step(5000)
	a.DrainAudio()
	state := a.Serialize()
	if len(state) != SerializeSize {
		t.Fatalf("Serialize len = %d, want SerializeSize %d", len(state), SerializeSize)
	}
	a.Step(32000)
	audioA := a.DrainAudio()
	stateA := a.Serialize()

	b := New()
	if err := b.Deserialize(state); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	b.Step(32000)
	audioB := b.DrainAudio()
	stateB := b.Serialize()

	if len(audioA) != len(audioB) {
		t.Fatalf("audio lengths differ: %d vs %d", len(audioA), len(audioB))
	}
	for i := range audioA {
		if audioA[i] != audioB[i] {
			t.Fatalf("audio sample %d differs: %d vs %d", i, audioA[i], audioB[i])
		}
	}
	if !bytes.Equal(stateA, stateB) {
		t.Fatal("post-step serialized states differ")
	}
	if err := b.Deserialize([]byte{1, 2, 3}); err == nil {
		t.Fatal("short Deserialize accepted")
	}
}
