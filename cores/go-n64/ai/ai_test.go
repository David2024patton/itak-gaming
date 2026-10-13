package ai

import (
	"encoding/binary"
	"math"
	"testing"
)

// fillSine writes stereo sine frames into dram starting
// at addr: framesPerPeriod samples per period, both
// channels the same, amplitude 12000.
func fillSine(dram []byte, addr, frames, framesPerPeriod int) {
	for i := 0; i < frames; i++ {
		v := int16(12000 * math.Sin(2*math.Pi*float64(i%framesPerPeriod)/float64(framesPerPeriod)))
		off := addr + i*4
		binary.BigEndian.PutUint16(dram[off:], uint16(v))
		binary.BigEndian.PutUint16(dram[off+2:], uint16(v))
	}
}

// programSine sets up a running transfer of the sine at
// 0x1000 with a sample rate near 32006 Hz.
func programSine(a *AI, length uint32) {
	a.WriteRegister(RegDACRate, 1520) // 48681818 / 1521 = about 32006 Hz
	a.WriteRegister(RegBitRate, 4)
	a.WriteRegister(RegControl, 1)
	a.WriteRegister(RegDRAMAddr, 0x1000)
	a.WriteRegister(RegLength, length)
}

func TestSineFrequency(t *testing.T) {
	const framesPerPeriod = 32
	const periods = 1000
	dram := make([]byte, 1<<20)
	fillSine(dram, 0x1000, framesPerPeriod*periods, framesPerPeriod)

	a := New(dram)
	programSine(a, 128*periods) // 128 bytes per period
	if got := a.SampleRate(); got != 48681818/1521 {
		t.Fatalf("SampleRate %d, want %d", got, 48681818/1521)
	}

	var left []int16
	for chunk := 0; chunk < 200 && len(left) < 32000; chunk++ {
		a.Advance(937500) // 0.01 seconds of CPU time
		out := a.Drain()
		for i := 0; i+1 < len(out); i += 2 {
			left = append(left, out[i])
		}
	}
	if len(left) < 32000 {
		t.Fatalf("collected %d left samples, want at least 32000", len(left))
	}

	crossings := 0
	for i := 1; i < len(left); i++ {
		if left[i-1] < 0 && left[i] >= 0 {
			crossings++
		}
	}
	seconds := float64(len(left)) / float64(a.SampleRate())
	measured := float64(crossings) / seconds
	want := float64(a.SampleRate()) / framesPerPeriod // about 1000.2 Hz
	if measured < 970 || measured > 1030 {
		t.Fatalf("measured sine frequency %.1f Hz, want near %.1f Hz", measured, want)
	}
}

func TestInterruptAtTransferStart(t *testing.T) {
	dram := make([]byte, 1<<16)
	a := New(dram)
	fires := 0
	clears := 0
	a.OnInterrupt = func(active bool) {
		if active {
			fires++
		} else {
			clears++
		}
	}
	a.WriteRegister(RegDACRate, 1520)
	a.WriteRegister(RegControl, 1)

	a.WriteRegister(RegDRAMAddr, 0)
	a.WriteRegister(RegLength, 128)
	if fires != 1 {
		t.Fatalf("fires after first length write %d, want 1 (fires at start)", fires)
	}

	a.WriteRegister(RegDRAMAddr, 0x200)
	a.WriteRegister(RegLength, 128)
	if fires != 1 {
		t.Fatalf("fires after queueing a second transfer %d, want still 1", fires)
	}
	if !a.Full() {
		t.Fatal("second transfer not reported as queued")
	}

	// 128 bytes is 32 sample frames, about a millisecond
	// at this rate; 200,000 cycles is more than enough
	// for the first transfer to finish and the second to
	// start.
	a.Advance(200000)
	if fires != 2 {
		t.Fatalf("fires after the second transfer starts %d, want 2", fires)
	}

	a.WriteRegister(RegStatus, 0)
	if clears != 1 {
		t.Fatalf("STATUS write produced %d clear calls, want 1", clears)
	}
}

func TestLengthReadbackAndStatus(t *testing.T) {
	dram := make([]byte, 1<<16)
	a := New(dram)
	program := func(addr, length uint32) {
		a.WriteRegister(RegDRAMAddr, addr)
		a.WriteRegister(RegLength, length)
	}
	a.WriteRegister(RegDACRate, 1520)
	a.WriteRegister(RegControl, 1)
	program(0, 4096)
	program(0x4000, 4096)

	a.Advance(937500) // about 320 frames, 1280 bytes
	rem := a.ReadRegister(RegLength)
	if rem >= 4096 || rem == 0 {
		t.Fatalf("LENGTH remaining after partial play %d, want between 1 and 4095", rem)
	}
	st := a.ReadRegister(RegStatus)
	if st&StatusBusy == 0 {
		t.Fatalf("STATUS %#x: BUSY clear during a transfer", st)
	}
	if st&StatusFull == 0 || st&StatusFullLow == 0 {
		t.Fatalf("STATUS %#x: FULL bits clear while a transfer is queued", st)
	}
	if st&StatusEnabled == 0 || st&StatusConst == 0 {
		t.Fatalf("STATUS %#x: ENABLED or the constant bit clear", st)
	}

	// Run well past both transfers (2048 frames total at
	// about 32006 frames per second).
	for i := 0; i < 20 && a.Busy(); i++ {
		a.Advance(937500)
		a.Drain()
	}
	if a.Busy() {
		t.Fatal("BUSY still set after both transfers finished")
	}
	if got := a.ReadRegister(RegLength); got != 0 {
		t.Fatalf("LENGTH after completion %d, want 0", got)
	}
	if st := a.ReadRegister(RegStatus); st&StatusBusy != 0 {
		t.Fatalf("STATUS %#x: BUSY set while idle", st)
	}
}

func TestReadMirrorQuirk(t *testing.T) {
	dram := make([]byte, 1<<16)
	a := New(dram)
	a.WriteRegister(RegDACRate, 1520)
	a.WriteRegister(RegControl, 1)
	a.WriteRegister(RegDRAMAddr, 0)
	a.WriteRegister(RegLength, 4096)
	a.Advance(937500)
	a.Drain()

	rem := a.ReadRegister(RegLength)
	if rem == 0 || rem == 4096 {
		t.Fatalf("remaining %d, want a partial count", rem)
	}
	for _, off := range []uint32{RegDRAMAddr, RegControl, RegDACRate, RegBitRate} {
		if got := a.ReadRegister(off); got != rem {
			t.Fatalf("read of offset %#x returned %d, want the mirrored remaining %d", off, got, rem)
		}
	}
}

func TestDMAEnableGate(t *testing.T) {
	dram := make([]byte, 1<<16)
	a := New(dram)
	fires := 0
	a.OnInterrupt = func(active bool) {
		if active {
			fires++
		}
	}
	a.WriteRegister(RegDACRate, 1520)
	a.WriteRegister(RegDRAMAddr, 0)
	a.WriteRegister(RegLength, 4096) // CONTROL is still 0
	a.Advance(937500)
	if out := a.Drain(); len(out) != 0 {
		t.Fatalf("consumed %d samples while DMA was disabled", len(out)/2)
	}
	if fires != 0 {
		t.Fatalf("start interrupt fired %d times while disabled, want 0", fires)
	}
	a.WriteRegister(RegControl, 1)
	if fires != 1 {
		t.Fatalf("enabling DMA fired %d start interrupts, want 1", fires)
	}
	a.Advance(937500)
	if out := a.Drain(); len(out) == 0 {
		t.Fatal("no samples after enabling DMA")
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	if SerializeSize != 60 {
		t.Fatalf("SerializeSize %d, want 60", SerializeSize)
	}
	dram := make([]byte, 1<<20)
	for i := range dram {
		dram[i] = byte(i*37 + 11)
	}

	setup := func() *AI {
		a := New(dram)
		a.WriteRegister(RegDACRate, 1520)
		a.WriteRegister(RegBitRate, 4)
		a.WriteRegister(RegControl, 1)
		a.WriteRegister(RegDRAMAddr, 0x100)
		a.WriteRegister(RegLength, 8192)
		a.WriteRegister(RegDRAMAddr, 0x2000)
		a.WriteRegister(RegLength, 4096) // pending behind the first
		return a
	}
	const chunk = 15625
	const preChunks = 20
	const postChunks = 120

	// Uninterrupted reference run.
	ref := setup()
	var want []int16
	for i := 0; i < preChunks+postChunks; i++ {
		ref.Advance(chunk)
		want = append(want, ref.Drain()...)
	}
	if len(want) == 0 {
		t.Fatal("reference run produced no samples")
	}

	// Same run, but serialized and restored mid-transfer.
	sub := setup()
	var got []int16
	for i := 0; i < preChunks; i++ {
		sub.Advance(chunk)
		got = append(got, sub.Drain()...)
	}
	buf := make([]byte, SerializeSize)
	if n := sub.Serialize(buf); n != SerializeSize {
		t.Fatalf("Serialize wrote %d bytes, want %d", n, SerializeSize)
	}
	restored := New(dram)
	if err := restored.Deserialize(buf); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if !restored.Busy() || !restored.Full() {
		t.Fatalf("restored state busy=%v full=%v, want both true", restored.Busy(), restored.Full())
	}
	for i := 0; i < postChunks; i++ {
		restored.Advance(chunk)
		got = append(got, restored.Drain()...)
	}

	if len(got) != len(want) {
		t.Fatalf("restored run produced %d samples, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d diverged: got %d, want %d", i, got[i], want[i])
		}
	}

	if err := restored.Deserialize(buf[:10]); err == nil {
		t.Fatal("Deserialize accepted a short state")
	}
}
