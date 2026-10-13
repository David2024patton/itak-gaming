package ai

import (
	"math"
	"testing"
)

type fakeMem struct{ b []byte }

func (f *fakeMem) Read8(addr uint32) uint8 {
	if int(addr) < len(f.b) {
		return f.b[addr]
	}
	return 0
}

const cyclesPerFrame48 = 486_000_000 / 48_000 // 10125

func put32(a *AI, off uint32, v uint32) {
	a.Write16(off, uint16(v>>16))
	a.Write16(off+2, uint16(v))
}

func get32(a *AI, off uint32) uint32 {
	return uint32(a.Read16(off))<<16 | uint32(a.Read16(off+2))
}

// setup builds an AI with tone data at 0x1000 in fake
// memory and starts a full volume stream of frames
// stereo frames, with the AI interrupt unmasked.
func setup(t *testing.T, frames int) (*AI, *fakeMem, []int16) {
	t.Helper()
	mem := &fakeMem{b: make([]byte, 1<<16)}
	want := make([]int16, 0, frames)
	for i := 0; i < frames; i++ {
		phase := 2 * math.Pi * 440 * float64(i) / 48000
		s := int16(10000 * math.Sin(phase))
		off := 0x1000 + i*4
		mem.b[off] = byte(uint16(s) >> 8)
		mem.b[off+1] = byte(uint16(s))
		mem.b[off+2] = byte(uint16(s) >> 8)
		mem.b[off+3] = byte(uint16(s))
		want = append(want, s)
	}
	a := New(mem)
	a.Write16(regAIVRHigh, 127)
	a.Write16(regAIVRLow, 127)
	a.Write16(regDMAAddrH, 0)
	a.Write16(regDMAAddrL, 0x1000)
	a.Write16(regDMALenL, uint16(frames*4))
	a.Write16(regDMALenH, dmaEnable)
	a.Write16(regAICRLow, aicrPSTAT|aicrAIINTMSK)
	return a, mem, want
}

func TestPlaybackGate(t *testing.T) {
	const frames = 4800 // 0.1 s at 48000 Hz, exactly 44 tone cycles
	a, _, _ := setup(t, frames)

	if got := a.Rate(); got != 48000 {
		t.Fatalf("Rate = %d, want 48000", got)
	}
	var irq []bool
	a.OnAIDInt = func(on bool) { irq = append(irq, on) }

	// One frame short of the end: no interrupt yet.
	a.Advance((frames - 1) * cyclesPerFrame48)
	if a.aicr&aicrAIINT != 0 {
		t.Fatal("AIINT set one frame early")
	}
	if got := get32(a, regCounterHigh); got != frames-1 {
		t.Fatalf("sample counter = %d, want %d", got, frames-1)
	}
	// The final frame completes the DMA.
	a.Advance(cyclesPerFrame48)
	if a.aicr&aicrAIINT == 0 {
		t.Fatal("AIINT not set at end of DMA")
	}
	if !a.IRQAsserted() {
		t.Fatal("IRQAsserted false with AIINT set and AIINTMSK on")
	}
	if len(irq) != 1 || !irq[0] {
		t.Fatalf("OnAIDInt calls = %v, want [true]", irq)
	}
	if got := get32(a, regCounterHigh); got != frames {
		t.Fatalf("sample counter = %d, want %d", got, frames)
	}
	if a.Read16(regDMALenH)&dmaEnable != 0 {
		t.Fatal("DMA enable bit still set after completion")
	}

	out := a.Drain()
	if len(out) != 2*frames {
		t.Fatalf("Drain length = %d, want %d", len(out), 2*frames)
	}
	crossings := 0
	for i := 1; i < frames; i++ {
		if out[(i-1)*2] < 0 && out[i*2] >= 0 {
			crossings++
		}
	}
	if crossings < 43 || crossings > 45 {
		t.Fatalf("positive zero crossings = %d, want 44 within 1", crossings)
	}
	if again := a.Drain(); len(again) != 0 {
		t.Fatalf("second Drain returned %d samples, want 0", len(again))
	}

	// Rate select.
	a.Write16(regAICRLow, aicrPSTAT|aicrAIINTMSK|aicrAISFR)
	if got := a.Rate(); got != 32000 {
		t.Fatalf("Rate with AISFR = %d, want 32000", got)
	}
	// Masking the interrupt drops IRQAsserted but keeps
	// the status bit; writing 1 to AIINT clears it and
	// reports the change.
	a.Write16(regAICRLow, aicrPSTAT|aicrAISFR)
	if a.IRQAsserted() {
		t.Fatal("IRQAsserted true with AIINTMSK off")
	}
	if a.aicr&aicrAIINT == 0 {
		t.Fatal("AIINT status lost when the mask changed")
	}
	a.Write16(regAICRLow, aicrPSTAT|aicrAISFR|aicrAIINT)
	if a.aicr&aicrAIINT != 0 {
		t.Fatal("AIINT not cleared by write 1 to clear")
	}
	if len(irq) != 2 || irq[1] {
		t.Fatalf("OnAIDInt calls = %v, want [true false]", irq)
	}
}

func TestVolumeScaling(t *testing.T) {
	mem := &fakeMem{b: make([]byte, 1<<16)}
	// Eight identical frames: left 1000, right -2000.
	for i := 0; i < 8; i++ {
		off := 0x2000 + i*4
		mem.b[off], mem.b[off+1] = 0x03, 0xE8   // 1000
		mem.b[off+2], mem.b[off+3] = 0xF8, 0x30 // -2000
	}
	a := New(mem)
	a.Write16(regAIVRHigh, 64) // left volume 64
	a.Write16(regAIVRLow, 127) // right volume 127
	a.Write16(regDMAAddrL, 0x2000)
	a.Write16(regDMALenL, 32)
	a.Write16(regDMALenH, dmaEnable)
	a.Write16(regAICRLow, aicrPSTAT)
	a.Advance(8 * cyclesPerFrame48)
	out := a.Drain()
	if len(out) != 16 {
		t.Fatalf("Drain length = %d, want 16", len(out))
	}
	if out[0] != 503 { // 1000 * 64 / 127 in integer math
		t.Fatalf("left sample = %d, want 503", out[0])
	}
	if out[1] != -2000 { // full volume passes through
		t.Fatalf("right sample = %d, want -2000", out[1])
	}
	// A stopped stream produces no frames at all.
	a.Write16(regAICRLow, 0)
	a.Advance(100 * cyclesPerFrame48)
	if got := a.Drain(); len(got) != 0 {
		t.Fatalf("stopped stream produced %d samples", len(got))
	}
}

func TestTimingInterruptOncePerCrossing(t *testing.T) {
	a, _, _ := setup(t, 64)
	put32(a, regTimingHigh, 4)
	a.Advance(4 * cyclesPerFrame48)
	if a.aicr&aicrAIINT == 0 {
		t.Fatal("AIINT not set when the counter reached AIINTTIMING")
	}
	a.Write16(regAICRLow, aicrPSTAT|aicrAIINTMSK|aicrAIINT) // clear
	a.Advance(4 * cyclesPerFrame48)
	if a.aicr&aicrAIINT != 0 {
		t.Fatal("timing interrupt fired again without rearming")
	}
	// Resetting the counter through SCRST rearms it and
	// zeroes the counter.
	a.Write16(regAICRLow, aicrPSTAT|aicrAIINTMSK|aicrSCRST)
	if got := get32(a, regCounterHigh); got != 0 {
		t.Fatalf("sample counter after SCRST = %d, want 0", got)
	}
}

func TestZeroLengthDMAInterruptsAtOnce(t *testing.T) {
	a := New(&fakeMem{b: make([]byte, 1024)})
	a.Write16(regAICRLow, aicrPSTAT|aicrAIINTMSK)
	a.Write16(regDMALenL, 0)
	a.Write16(regDMALenH, dmaEnable)
	if a.aicr&aicrAIINT == 0 {
		t.Fatal("zero length DMA did not interrupt immediately")
	}
}

func TestSerializeRoundTripMidStream(t *testing.T) {
	a, mem, _ := setup(t, 64)
	twin := New(mem)
	a.Advance(3 * cyclesPerFrame48)
	state := a.Serialize()
	if len(state) != SerializeSize {
		t.Fatalf("Serialize length = %d, want %d", len(state), SerializeSize)
	}
	if err := twin.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	a.Advance(5 * cyclesPerFrame48)
	twin.Advance(5 * cyclesPerFrame48)
	gotA, gotTwin := a.Drain(), twin.Drain()
	if len(gotA) != len(gotTwin) {
		t.Fatalf("drained lengths differ: %d vs %d", len(gotA), len(gotTwin))
	}
	for i := range gotA {
		if gotA[i] != gotTwin[i] {
			t.Fatalf("sample %d differs after restore: %d vs %d", i, gotA[i], gotTwin[i])
		}
	}
	if a.counter != twin.counter || a.counter != 8 {
		t.Fatalf("counters = %d and %d, want 8 and 8", a.counter, twin.counter)
	}
	if err := twin.Deserialize([]byte{1, 2, 3}); err != ErrBadState {
		t.Fatalf("Deserialize short state = %v, want ErrBadState", err)
	}
}
