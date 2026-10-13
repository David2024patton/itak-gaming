package dma

import "testing"

type fakeMem struct{ m map[uint32]uint32 }

func newFakeMem() *fakeMem                    { return &fakeMem{m: map[uint32]uint32{}} }
func (f *fakeMem) Read32(a uint32) uint32     { return f.m[a] }
func (f *fakeMem) Write32(a uint32, v uint32) { f.m[a] = v }

type fakeDev struct {
	gp0     []uint32
	gpuRead []uint32
	cdWords []uint32
	spuIn   []uint32
	spuOut  []uint32
	spuIdx  int
	mdecIn  []uint32
}

func (d *fakeDev) GP0Write(w uint32) { d.gp0 = append(d.gp0, w) }
func (d *fakeDev) GPURead() uint32 {
	if len(d.gpuRead) == 0 {
		return 0
	}
	v := d.gpuRead[0]
	d.gpuRead = d.gpuRead[1:]
	return v
}
func (d *fakeDev) CDReadWord() (uint32, bool) {
	if len(d.cdWords) == 0 {
		return 0, false
	}
	v := d.cdWords[0]
	d.cdWords = d.cdWords[1:]
	return v, true
}
func (d *fakeDev) SPUReadWord() uint32 {
	if d.spuIdx >= len(d.spuOut) {
		return 0
	}
	v := d.spuOut[d.spuIdx]
	d.spuIdx++
	return v
}
func (d *fakeDev) SPUWriteWord(w uint32) { d.spuIn = append(d.spuIn, w) }
func (d *fakeDev) MDECInWrite(w uint32)  { d.mdecIn = append(d.mdecIn, w) }
func (d *fakeDev) MDECOutRead() uint32   { return 0xABCD0000 }

func newRig() (*Controller, *fakeMem, *fakeDev) {
	c := New()
	m := newFakeMem()
	d := &fakeDev{}
	c.Mem = m
	c.Dev = d
	return c, m, d
}

func TestSync0RAMtoGPU(t *testing.T) {
	c, m, d := newRig()
	m.m[0x1000] = 0x11223344
	m.m[0x1004] = 0x55667788
	c.WriteReg(0x1F8010F0, 0x0800) // enable ch2 (nibble bit 3 at 4*2+3=11)
	if c.DPCR != 0x0800 {
		t.Fatalf("DPCR = 0x%08X", c.DPCR)
	}
	c.WriteReg(0x1F8010A0, 0x1000)     // MADR
	c.WriteReg(0x1F8010A4, 2)          // BCR: 2 words
	c.WriteReg(0x1F8010A8, 0x01000001) // dir from RAM, sync 0, start
	if len(d.gp0) != 2 || d.gp0[0] != 0x11223344 || d.gp0[1] != 0x55667788 {
		t.Errorf("gp0 words = %v", d.gp0)
	}
	if got := c.ReadReg(0x1F8010A8); got&(1<<24) != 0 {
		t.Errorf("CHCR busy bit still set: 0x%08X", got)
	}
	if c.ReadReg(0x1F8010A0) != 0x1008 {
		t.Errorf("MADR after = 0x%X, want 0x1008", c.ReadReg(0x1F8010A0))
	}
}

func TestLinkedListThreeNodes(t *testing.T) {
	c, m, d := newRig()
	// Node A at 0x2000: 2 words, next 0x2100.
	m.m[0x2000] = 0x02002100
	m.m[0x2004] = 0xAAAA0001
	m.m[0x2008] = 0xAAAA0002
	// Node B at 0x2100: 1 word, next 0x2200.
	m.m[0x2100] = 0x01002200
	m.m[0x2104] = 0xBBBB0001
	// Node C at 0x2200: 3 words, terminator.
	m.m[0x2200] = 0x03FFFFFF
	m.m[0x2204] = 0xCCCC0001
	m.m[0x2208] = 0xCCCC0002
	m.m[0x220C] = 0xCCCC0003
	c.WriteReg(0x1F8010F0, 0x0800)
	c.WriteReg(0x1F8010A0, 0x2000)
	c.WriteReg(0x1F8010A8, 0x01000401) // sync 2, from RAM, start
	want := []uint32{0xAAAA0001, 0xAAAA0002, 0xBBBB0001, 0xCCCC0001, 0xCCCC0002, 0xCCCC0003}
	if len(d.gp0) != len(want) {
		t.Fatalf("gp0 = %v, want %v", d.gp0, want)
	}
	for i := range want {
		if d.gp0[i] != want[i] {
			t.Errorf("gp0[%d] = 0x%08X, want 0x%08X", i, d.gp0[i], want[i])
		}
	}
}

func TestOTCBuildsDocumentedTable(t *testing.T) {
	c, m, _ := newRig()
	c.WriteReg(0x1F8010F0, 0x8000000) // enable ch6 (bit 4*6+3 = 27)
	c.WriteReg(0x1F8010E0, 0x12300C)  // MADR = last entry
	c.WriteReg(0x1F8010E4, 4)         // BCR = 4 entries
	c.WriteReg(0x1F8010E8, 0x01000000)
	want := map[uint32]uint32{
		0x123000: 0x00FFFFFF,
		0x123004: 0x00123000,
		0x123008: 0x00123004,
		0x12300C: 0x00123008,
	}
	for a, v := range want {
		if m.m[a] != v {
			t.Errorf("table[0x%X] = 0x%08X, want 0x%08X", a, m.m[a], v)
		}
	}
}

func TestDICRFlagRuleAndInterrupt(t *testing.T) {
	c, m, _ := newRig()
	m.m[0x4000] = 0x99
	irqs := 0
	c.OnIRQ = func() { irqs++ }
	// Completion WITHOUT the channel IRQ enable must
	// not raise the flag (documented rule).
	c.WriteReg(0x1F8010F0, 0x0800)
	c.WriteReg(0x1F8010A0, 0x4000)
	c.WriteReg(0x1F8010A4, 1)
	c.WriteReg(0x1F8010A8, 0x01000001)
	if c.DICR()&(1<<26) != 0 {
		t.Errorf("DICR flag set without enable: 0x%08X", c.DICR())
	}
	if irqs != 0 {
		t.Fatalf("irq fired without enable")
	}
	// Enable ch2 IRQ + master; transfer again.
	c.WriteDICR(1<<18 | 1<<23)
	c.WriteReg(0x1F8010A0, 0x4000)
	c.WriteReg(0x1F8010A4, 1)
	c.WriteReg(0x1F8010A8, 0x01000001)
	d := c.DICR()
	if d&(1<<26) == 0 || d&(1<<31) == 0 {
		t.Errorf("DICR = 0x%08X, want flag bit 26 and master bit 31 set", d)
	}
	if irqs != 1 {
		t.Errorf("irq callbacks = %d, want 1", irqs)
	}
	// Acknowledge by writing 1 to the flag bit.
	c.WriteDICR(1<<18 | 1<<23 | 1<<26)
	if c.DICR()&(1<<31) != 0 {
		t.Errorf("DICR master still set after ack: 0x%08X", c.DICR())
	}
}

func TestCDROMChannelPullsFIFO(t *testing.T) {
	c, m, d := newRig()
	d.cdWords = []uint32{0x11111111, 0x22222222}
	c.WriteReg(0x1F8010F0, 0x8000) // enable ch3 (bit 15)
	c.WriteReg(0x1F8010B0, 0x8000)
	c.WriteReg(0x1F8010B4, 2)
	c.WriteReg(0x1F8010B8, 0x01000000) // to RAM, sync 0, start
	if m.m[0x8000] != 0x11111111 || m.m[0x8004] != 0x22222222 {
		t.Errorf("RAM got 0x%08X 0x%08X", m.m[0x8000], m.m[0x8004])
	}
}

func TestSync1NeedsTriggerBit(t *testing.T) {
	c, m, d := newRig()
	m.m[0x3000] = 0x77
	c.WriteReg(0x1F8010F0, 0x0800)
	c.WriteReg(0x1F8010A0, 0x3000)
	c.WriteReg(0x1F8010A4, 0x00010001) // block size 1, count 1
	c.WriteReg(0x1F8010A8, 0x01000201) // sync 1, start, NO trigger
	if len(d.gp0) != 0 {
		t.Fatalf("sync mode 1 moved data without the trigger bit")
	}
	c.WriteReg(0x1F8010A8, 0x03000201) // now with trigger bit 25
	if len(d.gp0) != 1 || d.gp0[0] != 0x77 {
		t.Errorf("gp0 = %v after trigger", d.gp0)
	}
}

func TestChoppingBursts(t *testing.T) {
	c, m, _ := newRig()
	for i := 0; i < 16; i++ {
		m.m[0x5000+uint32(4*i)] = uint32(i)
	}
	c.WriteReg(0x1F8010F0, 0x0800)
	c.WriteReg(0x1F8010A0, 0x5000)
	c.WriteReg(0x1F8010A4, 16)
	// Chopping on, DMA window 2^2 = 4 words.
	c.WriteReg(0x1F8010A8, 0x01020005)
	if got := c.BurstCount(2); got != 4 {
		t.Errorf("bursts = %d, want 4", got)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	c, _, _ := newRig()
	c.WriteDICR(1<<18 | 1<<23)
	c.Ch[2].MADR = 0x1234
	s := c.Serialize()
	c2 := New()
	c2.Deserialize(s)
	if c2.DICR() != c.DICR() || c2.Ch[2].MADR != 0x1234 || c2.DPCR != c.DPCR {
		t.Errorf("DMA state diverged after round trip")
	}
}
