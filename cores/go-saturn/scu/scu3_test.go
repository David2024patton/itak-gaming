package scu

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// fakeMem is a sparse byte addressed Mem for tests.
type fakeMem struct {
	b map[uint32]uint8
}

func newFakeMem() *fakeMem { return &fakeMem{b: map[uint32]uint8{}} }

func (m *fakeMem) Read8(a uint32) uint8 { return m.b[a] }

func (m *fakeMem) Read16(a uint32) uint16 {
	return uint16(m.b[a])<<8 | uint16(m.b[a+1])
}

func (m *fakeMem) Read32(a uint32) uint32 {
	return uint32(m.b[a])<<24 | uint32(m.b[a+1])<<16 |
		uint32(m.b[a+2])<<8 | uint32(m.b[a+3])
}

func (m *fakeMem) Write8(a uint32, v uint8) { m.b[a] = v }

func (m *fakeMem) Write16(a uint32, v uint16) {
	m.b[a] = uint8(v >> 8)
	m.b[a+1] = uint8(v)
}

func (m *fakeMem) Write32(a uint32, v uint32) {
	m.b[a] = uint8(v >> 24)
	m.b[a+1] = uint8(v >> 16)
	m.b[a+2] = uint8(v >> 8)
	m.b[a+3] = uint8(v)
}

func setPage(page []byte, off int, v uint32) {
	binary.BigEndian.PutUint32(page[off:off+4], v)
}

func pageWord(page []byte, off int) uint32 {
	return binary.BigEndian.Uint32(page[off : off+4])
}

// setupDMA programs one channel's registers on the page.
func setupDMA(page []byte, ch int, r, w, c, ad, en, md uint32) {
	base := ch * 0x20
	setPage(page, base+0x00, r)
	setPage(page, base+0x04, w)
	setPage(page, base+0x08, c)
	setPage(page, base+0x0C, ad)
	setPage(page, base+0x14, md)
	setPage(page, base+0x10, en)
}

func TestLevelOf(t *testing.T) {
	// Delivery levels are the manual's per source table
	// (SCU manual Table 2.1). SAT-3 replaced the flat
	// level 6 delivery of SAT-2 with these; LevelOf and
	// ManualLevelOf report the same table.
	manual := map[int]int{
		IntVBlankIn: 15, IntVBlankOut: 14, IntHBlankIn: 13,
		IntTimer0: 12, IntTimer1: 11, IntDSPEnd: 10,
		IntSoundReq: 9, IntSMPC: 8, IntPad: 8,
		IntDMA2End: 6, IntDMA1End: 6, IntDMA0End: 5,
		IntSpriteEnd: 2,
	}
	for bit, want := range manual {
		if got := LevelOf(bit); got != want {
			t.Fatalf("LevelOf(%d) = %d, want %d", bit, got, want)
		}
		if got := ManualLevelOf(bit); got != want {
			t.Fatalf("ManualLevelOf(%d) = %d, want %d", bit, got, want)
		}
	}
	// The A-Bus CD input delivers at this core's fixed
	// level 8; the manual assigns it no fixed level.
	if got := LevelOf(IntABusCD); got != 8 {
		t.Fatalf("LevelOf(IntABusCD) = %d, want 8", got)
	}
	if got := Vector(IntABusCD); got != 0x50 {
		t.Fatalf("Vector(IntABusCD) = %#x, want 0x50", got)
	}
	if got := LevelOf(99); got != -1 {
		t.Fatalf("LevelOf(99) = %d, want -1", got)
	}
}

func TestDMADirectImmediateEdge(t *testing.T) {
	s, page := newPage()
	mem := newFakeMem()
	s.AttachMem(mem)
	mem.Write32(0x1000, 0xDEADBEEF)
	// Factor 7 (immediate), enable at bit 0 (brief form).
	setupDMA(page, 0, 0x1000, 0x2000, 4, 0x101, 1, FactorImmediate)
	s.Poll()
	if got := mem.Read32(0x2000); got != 0xDEADBEEF {
		t.Fatalf("copied word = %#x, want 0xDEADBEEF", got)
	}
	if s.Status()&(1<<IntDMA0End) == 0 {
		t.Fatal("DMA0 end interrupt not raised")
	}
	if got := pageWord(page, offDSTA); got != 0 {
		t.Fatalf("DSTA after Poll = %#x, want 0 (idle)", got)
	}
	// Save mode: address registers unchanged.
	if got := pageWord(page, 0x00); got != 0x1000 {
		t.Fatalf("D0R after save mode run = %#x, want 0x1000", got)
	}
	// The enable edge is consumed: changing the source
	// and polling again does not rerun the channel.
	mem.Write32(0x1000, 0x12345678)
	s.Poll()
	if got := mem.Read32(0x2000); got != 0xDEADBEEF {
		t.Fatalf("channel reran without an edge: dest = %#x", got)
	}
}

func TestDMADirectUpdateMode(t *testing.T) {
	s, page := newPage()
	mem := newFakeMem()
	s.AttachMem(mem)
	md := uint32(FactorImmediate) | 1<<16 | 1<<8 // update both
	setupDMA(page, 0, 0x1000, 0x2000, 8, 0x101, 1, md)
	s.Poll()
	if got := pageWord(page, 0x00); got != 0x1008 {
		t.Fatalf("D0R after update run = %#x, want 0x1008", got)
	}
	if got := pageWord(page, 0x04); got != 0x2008 {
		t.Fatalf("D0W after update run = %#x, want 0x2008", got)
	}
}

func TestDMAFactorFromRaiseAndStartFactor(t *testing.T) {
	s, page := newPage()
	mem := newFakeMem()
	s.AttachMem(mem)
	mem.Write32(0x1000, 0xAAAAAAAA)
	mem.Write32(0x1100, 0xBBBBBBBB)
	// Channel 1 waits for VBlank-IN; enable at bit 8
	// (manual form). Channel 2 waits for Timer 0.
	setupDMA(page, 1, 0x1000, 0x2000, 4, 0x101, 1<<8, FactorVBlankIn)
	setupDMA(page, 2, 0x1100, 0x2100, 4, 0x101, 1, FactorTimer0)
	s.Poll() // enables latch, no factors yet
	if mem.Read32(0x2000) != 0 || mem.Read32(0x2100) != 0 {
		t.Fatal("channel ran before its factor")
	}
	s.Raise(IntVBlankIn)
	s.Poll()
	if got := mem.Read32(0x2000); got != 0xAAAAAAAA {
		t.Fatalf("channel 1 dest = %#x, want 0xAAAAAAAA", got)
	}
	if s.Status()&(1<<IntDMA1End) == 0 {
		t.Fatal("DMA1 end interrupt not raised")
	}
	if mem.Read32(0x2100) != 0 {
		t.Fatal("channel 2 ran on the wrong factor")
	}
	s.StartFactor(FactorTimer0)
	s.Poll()
	if got := mem.Read32(0x2100); got != 0xBBBBBBBB {
		t.Fatalf("channel 2 dest = %#x, want 0xBBBBBBBB", got)
	}
	if s.Status()&(1<<IntDMA2End) == 0 {
		t.Fatal("DMA2 end interrupt not raised")
	}
}

func TestDMAChannelOrderZeroFirst(t *testing.T) {
	s, page := newPage()
	mem := newFakeMem()
	s.AttachMem(mem)
	mem.Write32(0x1000, 0x11111111)
	mem.Write32(0x1100, 0x22222222)
	// Both channels due on VBlank-IN, same destination:
	// channel 0 runs first, so channel 1 lands last.
	setupDMA(page, 0, 0x1000, 0x2000, 4, 0x101, 1, FactorVBlankIn)
	setupDMA(page, 1, 0x1100, 0x2000, 4, 0x101, 1, FactorVBlankIn)
	s.Poll()
	s.Raise(IntVBlankIn)
	s.Poll()
	if got := mem.Read32(0x2000); got != 0x22222222 {
		t.Fatalf("overlap dest = %#x, want channel 1 result 0x22222222", got)
	}
}

func TestDMACountZeroMeansFullWidth(t *testing.T) {
	s, page := newPage()
	mem := newFakeMem()
	s.AttachMem(mem)
	for i := 0; i < 4096; i++ {
		mem.Write8(0x4000+uint32(i), uint8(i))
	}
	// Channel 1 count width is 12 bits; 0 means 4096.
	setupDMA(page, 1, 0x4000, 0x8000, 0, 0x101, 1, FactorImmediate)
	s.Poll()
	if got := mem.Read8(0x8000 + 4095); got != 0xFF {
		t.Fatalf("last copied byte = %#x, want 0xFF", got)
	}
	if got := mem.Read8(0x8000 + 100); got != uint8(100) {
		t.Fatalf("byte 100 = %#x, want 100", got)
	}
}

func TestDMAIndirectMode(t *testing.T) {
	s, page := newPage()
	mem := newFakeMem()
	s.AttachMem(mem)
	mem.Write32(0x1000, 0xCAFEBABE)
	mem.Write32(0x1100, 0x0D15EA5E)
	// Table at 0x8000: two entries, second has the end
	// flag (bit 31 of the count word) and 3 bytes.
	mem.Write32(0x8000, 0x1000)
	mem.Write32(0x8004, 0x2000)
	mem.Write32(0x8008, 4)
	mem.Write32(0x800C, 0x1100)
	mem.Write32(0x8010, 0x2100)
	mem.Write32(0x8014, 0x80000003)
	md := uint32(1<<24) | uint32(FactorImmediate) | 1<<8 // indirect, WUP
	setupDMA(page, 0, 0, 0x8000, 0, 0x101, 1, md)
	s.Poll()
	if got := mem.Read32(0x2000); got != 0xCAFEBABE {
		t.Fatalf("entry 1 dest = %#x, want 0xCAFEBABE", got)
	}
	if got := mem.Read32(0x2100) & 0xFFFFFF00; got != 0x0D15EA00 {
		t.Fatalf("entry 2 dest = %#x, want 0x0D15EAxx", got)
	}
	if s.Status()&(1<<IntDMA0End) == 0 {
		t.Fatal("DMA0 end interrupt not raised for indirect run")
	}
	// WUP: DxW left past the last entry (table + 24).
	if got := pageWord(page, 0x04); got != 0x8018 {
		t.Fatalf("D0W after indirect update = %#x, want 0x8018", got)
	}
}

func TestDMAIllegalSCUPage(t *testing.T) {
	s, page := newPage()
	mem := newFakeMem()
	s.AttachMem(mem)
	mem.Write32(0x1000, 0xFFFFFFFF)
	setupDMA(page, 0, 0x1000, 0x05FE0000, 4, 0x101, 1, FactorImmediate)
	s.Poll()
	if got := s.IllegalDMA(); got != 1 {
		t.Fatalf("IllegalDMA = %d, want 1", got)
	}
	if s.Status()&(1<<IntDMA0End) != 0 {
		t.Fatal("illegal transfer raised the end interrupt")
	}
}

func TestDMAForceStop(t *testing.T) {
	s, page := newPage()
	mem := newFakeMem()
	s.AttachMem(mem)
	mem.Write32(0x1000, 0x77777777)
	setupDMA(page, 0, 0x1000, 0x2000, 4, 0x101, 1, FactorImmediate)
	setPage(page, offDSTP, 1)
	s.Poll()
	if got := pageWord(page, 0x10); got != 0 {
		t.Fatalf("D0EN after DSTP = %#x, want 0 (cleared)", got)
	}
	if mem.Read32(0x2000) != 0 {
		t.Fatal("transfer ran despite force stop")
	}
	// The brief's transposed stop offset 0x7C also stops.
	setupDMA(page, 0, 0x1000, 0x2000, 4, 0x101, 1, FactorImmediate)
	setPage(page, offDSTA, 1)
	s.Poll()
	if got := pageWord(page, 0x10); got != 0 {
		t.Fatalf("D0EN after stop write to 0x7C = %#x, want 0", got)
	}
	if mem.Read32(0x2000) != 0 {
		t.Fatal("transfer ran despite stop write to 0x7C")
	}
}

// opWord assembles a DSP operation command word.
func opWord(alu, xField, yField, d1Field uint32) uint32 {
	return alu<<26 | xField<<20 | yField<<14 | d1Field
}

// loadProgramThroughPorts writes a program into DSP
// program RAM using the control port and the program
// data port, one Poll per word, as software would.
func loadProgramThroughPorts(t *testing.T, s *SCU, page []byte, prog []uint32) {
	t.Helper()
	for i, w := range prog {
		setPage(page, offPPAF, uint32(i))
		setPage(page, offPPD, w)
		s.Poll()
		if got := s.DSPProgram(i); got != w {
			t.Fatalf("program[%d] = %#x, want %#x (port load)", i, got, w)
		}
	}
}

func TestDSPDotProduct(t *testing.T) {
	s, page := newPage()
	bank0 := []uint32{3, 5, 7, 11}
	bank1 := []uint32{2, 4, 6, 8}
	for i, v := range bank0 {
		s.DSPWriteData(0, i, v)
	}
	for i, v := range bank1 {
		s.DSPWriteData(1, i, v)
	}
	prog := []uint32{
		opWord(0, 0, 1<<3, 0),              // CLR A
		opWord(0, 0, 0, (1<<12)|(10<<8)|0), // MOV 0, [CT0]
		opWord(0, 0, 0, (1<<12)|(11<<8)|0), // MOV 0, [CT1]
		opWord(0, 0, 0, (1<<12)|(9<<8)|5),  // MOV 5, [TOP]
		opWord(0, 0, 0, (1<<12)|(8<<8)|3),  // MOV 3, [LOP]
		opWord(0, (4<<3)|4, (4<<3)|5, 0),   // loop: MOV MC0,X | MOV MC1,Y
		opWord(0, 2<<3, 0, 0),              // MOV MUL, P
		opWord(4, 0, 2<<3, 0),              // ADD | MOV ALU, A
		0xE0000000,                         // BTM
		0xF8000000,                         // ENDI
	}
	loadProgramThroughPorts(t, s, page, prog)

	setPage(page, offPPAF, 1<<ppafEX) // start at 0, execute
	s.Poll()
	if got := s.DSPAccumulator(); got != 156 {
		t.Fatalf("dot product accumulator = %d, want 156", got)
	}
	if s.dsp.ct[0] != 4 || s.dsp.ct[1] != 4 {
		t.Fatalf("CT after loop = %v, want CT0 and CT1 at 4", s.dsp.ct)
	}
	if s.Status()&(1<<IntDSPEnd) == 0 {
		t.Fatal("DSP end interrupt not raised after ENDI")
	}
	ppaf := pageWord(page, offPPAF)
	if ppaf&(1<<ppafEX) != 0 {
		t.Fatal("EX flag still set after ENDI")
	}
	if ppaf&(1<<ppafE) == 0 {
		t.Fatal("E flag not set in PPAF after ENDI")
	}
	if s.DSPRunning() {
		t.Fatal("DSP still running after ENDI")
	}
}

func TestDSPDataPortLoadAndRead(t *testing.T) {
	s, page := newPage()
	// Select data RAM bank 1, address 0, then write a
	// word through the data port.
	setPage(page, offPDA, 0x40)
	s.Poll()
	setPage(page, offPDD, 0xCAFEBABE)
	s.Poll()
	if got := s.DSPData(1, 0); got != 0xCAFEBABE {
		t.Fatalf("data RAM bank 1 word 0 = %#x, want 0xCAFEBABE", got)
	}
	// The address counts up after the access.
	if got := pageWord(page, offPDA); got != 0x41 {
		t.Fatalf("PDA after write = %#x, want 0x41", got)
	}
	// Read back: select the address again and Poll;
	// the data port then presents the stored word.
	setPage(page, offPDA, 0x40)
	s.Poll()
	if got := pageWord(page, offPDD); got != 0xCAFEBABE {
		t.Fatalf("PDD read back = %#x, want 0xCAFEBABE", got)
	}
}

func TestDSPDMATransfers(t *testing.T) {
	s, page := newPage()
	mem := newFakeMem()
	s.AttachMem(mem)
	for i, v := range []uint32{111, 222, 333, 444} {
		mem.Write32(0x1000+uint32(i*4), v)
	}
	// Program: CT2 = 0, RA0 = 0x400 (byte 0x1000),
	// DMA D0, [M2], 4, ENDI.
	s.DSPWriteProgram(0, opWord(0, 0, 0, (1<<12)|(12<<8)|0))
	s.DSPWriteProgram(1, (2<<30)|(6<<26)|0x400) // MVI 0x400, [RA0]
	s.DSPWriteProgram(2, 0xC0000000|(1<<15)|(2<<8)|4)
	s.DSPWriteProgram(3, 0xF8000000) // ENDI
	if n := s.RunDSP(100); n != 4 {
		t.Fatalf("RunDSP executed %d instructions, want 4", n)
	}
	for i, want := range []uint32{111, 222, 333, 444} {
		if got := s.DSPData(2, i); got != want {
			t.Fatalf("DSP DMA in: data RAM2[%d] = %d, want %d", i, got, want)
		}
	}
	if s.Status()&(1<<IntDSPEnd) == 0 {
		t.Fatal("DSP end interrupt not raised")
	}

	// Second program at 10: CT2 = 0, WA0 = 0x800
	// (byte 0x2000), DMA [M2], D0, 4, ENDI.
	s.DSPWriteProgram(10, opWord(0, 0, 0, (1<<12)|(12<<8)|0))
	s.DSPWriteProgram(11, (2<<30)|(7<<26)|0x800) // MVI 0x800, [WA0]
	s.DSPWriteProgram(12, 0xC0000000|(1<<15)|(1<<12)|(2<<8)|4)
	s.DSPWriteProgram(13, 0xF8000000)
	setPage(page, offPPAF, 10|(1<<ppafLE)) // load PC = 10
	s.Poll()
	s.RunDSP(100)
	for i, want := range []uint32{111, 222, 333, 444} {
		if got := mem.Read32(0x2000 + uint32(i*4)); got != want {
			t.Fatalf("DSP DMA out: mem word %d = %d, want %d", i, got, want)
		}
	}
}

func TestDSPJump(t *testing.T) {
	s, _ := newPage()
	s.DSPWriteProgram(0, 0xD0000002) // JMP 2
	s.DSPWriteProgram(1, opWord(0, 0, 0, (1<<12)|(4<<8)|99))
	s.DSPWriteProgram(2, opWord(0, 0, 0, (1<<12)|(4<<8)|7))
	s.DSPWriteProgram(3, 0xF8000000) // ENDI
	s.RunDSP(100)
	if got := s.dsp.rx; got != 7 {
		t.Fatalf("RX after jump = %d, want 7 (address 1 skipped)", got)
	}
}

func TestTimer0DotModel(t *testing.T) {
	// No dot timing set: only compare 0 fires.
	s, page := newPage()
	setPage(page, offT0C, 0)
	s.TickLine(0)
	if s.Status()&(1<<IntTimer0) == 0 {
		t.Fatal("timer 0 did not fire for compare 0 without dot timing")
	}
	s2, page2 := newPage()
	setPage(page2, offT0C, 5)
	s2.TickLine(0)
	if s2.Status()&(1<<IntTimer0) != 0 {
		t.Fatal("timer 0 fired for compare 5 without dot timing")
	}
	// With 100 dots per line, compare 250 lies in the
	// dot range of line 2 only.
	s3, page3 := newPage()
	s3.SetLineDots(100)
	setPage(page3, offT0C, 250)
	s3.TickLine(0)
	s3.TickLine(1)
	if s3.Status()&(1<<IntTimer0) != 0 {
		t.Fatal("timer 0 fired before line 2")
	}
	s3.TickLine(2)
	if s3.Status()&(1<<IntTimer0) == 0 {
		t.Fatal("timer 0 did not fire on line 2 for compare 250")
	}
	_ = page
}

func TestTimer1LineCounting(t *testing.T) {
	// Mode 0: counts every line, fires at T1S = 3, and
	// resets to fire again three lines later.
	s, page := newPage()
	setPage(page, offT0C, 900) // timer 0 out of the way
	setPage(page, offT1S, 3)
	setPage(page, offT1MD, 1) // enabled, mode bit clear
	s.TickLine(0)
	s.TickLine(1)
	if s.Status()&(1<<IntTimer1) != 0 {
		t.Fatal("timer 1 fired before 3 lines")
	}
	s.TickLine(2)
	if s.Status()&(1<<IntTimer1) == 0 {
		t.Fatal("timer 1 did not fire at 3 lines")
	}
	setPage(page, offIST, 0)
	s.TickLine(3)
	s.TickLine(4)
	s.TickLine(5)
	if s.Status()&(1<<IntTimer1) == 0 {
		t.Fatal("timer 1 did not fire again after reset")
	}

	// Mode 1 (bit 8): counts only lines where timer 0
	// fires. Compare 250 with 100 dots per line fires
	// on line 2, so two ticks of line 2 reach T1S = 2.
	s2, page2 := newPage()
	s2.SetLineDots(100)
	setPage(page2, offT0C, 250)
	setPage(page2, offT1S, 2)
	setPage(page2, offT1MD, (1<<8)|1) // enabled, mode bit set
	s2.TickLine(0)
	s2.TickLine(1)
	s2.TickLine(2)
	if s2.Status()&(1<<IntTimer1) != 0 {
		t.Fatal("timer 1 fired after only one timer 0 line")
	}
	s2.TickLine(2)
	if s2.Status()&(1<<IntTimer1) == 0 {
		t.Fatal("timer 1 did not fire after two timer 0 lines")
	}
}

func TestSerializeExtraRoundTrip(t *testing.T) {
	s, page := newPage()
	mem := newFakeMem()
	s.AttachMem(mem)
	s.DSPWriteData(0, 0, 42)
	s.DSPWriteProgram(0, 0xF8000000)
	s.SetLineDots(228)
	setupDMA(page, 0, 0x1000, 0x2000, 4, 0x101, 1, FactorImmediate)
	s.StartFactor(FactorTimer1)
	s.Poll()
	first := s.SerializeExtra()
	if len(first) != ExtraSize {
		t.Fatalf("SerializeExtra len = %d, want %d", len(first), ExtraSize)
	}
	s2, _ := newPage()
	if err := s2.DeserializeExtra(first); err != nil {
		t.Fatal(err)
	}
	second := s2.SerializeExtra()
	if !bytes.Equal(first, second) {
		t.Fatal("extra state did not round trip")
	}
	if got := s2.DSPData(0, 0); got != 42 {
		t.Fatalf("restored data RAM = %d, want 42", got)
	}
	if got := s2.DSPProgram(0); got != 0xF8000000 {
		t.Fatalf("restored program RAM = %#x, want 0xF8000000", got)
	}
	if err := s2.DeserializeExtra(first[:100]); err == nil {
		t.Fatal("short extra state accepted")
	}
}
