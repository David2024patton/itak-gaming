package g2dma

import "testing"

// fakeMem backs the two windows the tests use: AICA space at
// 0x00800000 (2 MB) and main RAM at 0x0C000000 (16 MB), packed
// into one 18 MB slice.
type fakeMem struct{ b []byte }

func newFakeMem() *fakeMem { return &fakeMem{b: make([]byte, 18<<20)} }

func (f *fakeMem) idx(pa uint32) uint32 {
	if pa >= 0x0C000000 {
		return pa - 0x0C000000 + 0x200000
	}
	return pa - 0x00800000
}

func (f *fakeMem) Read8(pa uint32) uint8 { return f.b[f.idx(pa)] }
func (f *fakeMem) Write8(pa uint32, v uint8) {
	f.b[f.idx(pa)] = v
}

func (f *fakeMem) peek(pa uint32) uint8    { return f.b[f.idx(pa)] }
func (f *fakeMem) poke(pa uint32, v uint8) { f.b[f.idx(pa)] = v }

type fakeSink struct{ bits uint32 }

func (s *fakeSink) RaiseNormal(bits uint32) { s.bits |= bits }

type fakeAICA struct{ ends int }

func (a *fakeAICA) NotifyG2DMAEnd() { a.ends++ }

const (
	sbADSTAG = 0x005F7800
	sbADSTAR = 0x005F7804
	sbADLEN  = 0x005F7808
	sbADDIR  = 0x005F780C
	sbADTSEL = 0x005F7810
	sbADEN   = 0x005F7814
	sbADST   = 0x005F7818

	sbE1STAR = 0x005F7824
	sbE1LEN  = 0x005F7828
	sbE1EN   = 0x005F7834
	sbE1ST   = 0x005F7838

	sbADSTAGD = 0x005F78C0
	sbADSTARD = 0x005F78C4
	sbADLEND  = 0x005F78C8
)

func TestAICADMAToWaveRAM(t *testing.T) {
	mem := newFakeMem()
	sink := &fakeSink{}
	aica := &fakeAICA{}
	c := New(mem, aica, sink)

	const sysAddr = 0x0C001000
	const waveAddr = 0x00800000
	for i := 0; i < 64; i++ {
		mem.poke(sysAddr+uint32(i), byte(i*3+1))
	}
	c.Write32(sbADEN, 0)
	c.Write32(sbADSTAG, waveAddr)
	c.Write32(sbADSTAR, sysAddr)
	c.Write32(sbADLEN, 64)
	c.Write32(sbADDIR, 0)
	c.Write32(sbADTSEL, 0)
	c.Write32(sbADEN, 1)
	c.Write32(sbADST, 1)

	for i := 0; i < 64; i++ {
		if got := mem.peek(waveAddr + uint32(i)); got != byte(i*3+1) {
			t.Fatalf("wave[%d] = %#x, want %#x", i, got, byte(i*3+1))
		}
	}
	if got := c.Read32(sbADST); got != 0 {
		t.Errorf("ADST = %#x, want 0 (complete)", got)
	}
	if got := c.Read32(sbADSTAGD); got != waveAddr+64 {
		t.Errorf("ADSTAGD = %#x, want %#x", got, waveAddr+64)
	}
	if got := c.Read32(sbADSTARD); got != sysAddr+64 {
		t.Errorf("ADSTARD = %#x, want %#x", got, sysAddr+64)
	}
	if got := c.Read32(sbADLEND); got != 0 {
		t.Errorf("ADLEND = %#x, want 0", got)
	}
	if sink.bits&(1<<15) == 0 {
		t.Error("ISTNRM AICA DMA done event was not raised")
	}
	if aica.ends != 1 {
		t.Errorf("AICA DMA end notifications = %d, want 1", aica.ends)
	}
}

func TestAICADMAFromWaveRAM(t *testing.T) {
	mem := newFakeMem()
	c := New(mem, &fakeAICA{}, &fakeSink{})
	const sysAddr = 0x0C002000
	const waveAddr = 0x00801000
	for i := 0; i < 32; i++ {
		mem.poke(waveAddr+uint32(i), byte(0xA0+i))
	}
	c.Write32(sbADSTAG, waveAddr)
	c.Write32(sbADSTAR, sysAddr)
	c.Write32(sbADLEN, 32)
	c.Write32(sbADDIR, 1)
	c.Write32(sbADEN, 1)
	c.Write32(sbADST, 1)
	for i := 0; i < 32; i++ {
		if got := mem.peek(sysAddr + uint32(i)); got != byte(0xA0+i) {
			t.Fatalf("sys[%d] = %#x, want %#x", i, got, byte(0xA0+i))
		}
	}
}

func TestAICADMANotEnabled(t *testing.T) {
	mem := newFakeMem()
	sink := &fakeSink{}
	c := New(mem, &fakeAICA{}, sink)
	c.Write32(sbADSTAG, 0x00800000)
	c.Write32(sbADSTAR, 0x0C000000)
	c.Write32(sbADLEN, 16)
	c.Write32(sbADEN, 0)
	c.Write32(sbADST, 1)
	if sink.bits != 0 {
		t.Error("transfer ran with ADEN = 0")
	}
	if got := mem.peek(0x00800000); got != 0 {
		t.Error("wave RAM written with ADEN = 0")
	}
}

func TestExtChannelBusyForever(t *testing.T) {
	mem := newFakeMem()
	sink := &fakeSink{}
	c := New(mem, &fakeAICA{}, sink)
	c.Write32(sbE1STAR, 0x0C000000)
	c.Write32(sbE1LEN, 64)
	c.Write32(sbE1EN, 1)
	c.Write32(sbE1ST, 1)
	for i := 0; i < 100; i++ {
		if got := c.Read32(sbE1ST); got != 1 {
			t.Fatalf("E1ST read %d = %#x, want busy 1", i, got)
		}
	}
	if sink.bits != 0 {
		t.Error("absent-device channel raised a completion event")
	}
	// Clearing the enable drops the busy state in this model.
	c.Write32(sbE1EN, 0)
	if got := c.Read32(sbE1ST); got != 0 {
		t.Errorf("E1ST after EN=0 = %#x, want 0", got)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	mem := newFakeMem()
	c := New(mem, &fakeAICA{}, &fakeSink{})
	c.Write32(sbADSTAG, 0x00800000)
	c.Write32(sbADLEN, 128)
	c.Write32(sbE1EN, 1)
	c.Write32(sbE1ST, 1)
	b := make([]byte, SerializeSize)
	if n := c.Serialize(b); n != SerializeSize {
		t.Fatalf("Serialize wrote %d bytes, want %d", n, SerializeSize)
	}
	c2 := New(mem, &fakeAICA{}, &fakeSink{})
	if !c2.Deserialize(b) {
		t.Fatal("Deserialize rejected the block")
	}
	if got := c2.Read32(sbADLEN); got != 128 {
		t.Errorf("ADLEN after restore = %#x, want 128", got)
	}
	if got := c2.Read32(sbE1ST); got != 1 {
		t.Errorf("E1ST after restore = %#x, want busy 1", got)
	}
}
