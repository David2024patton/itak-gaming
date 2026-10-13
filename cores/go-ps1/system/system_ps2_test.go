package system

// PS1-2 system gates: display timing, timers through
// the register page, DMA through the machine, the
// interrupt controller with real CPU programs, the CD
// DMA path, and the full determinism gate.

import (
	"bytes"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-ps1/cd"
)

// ----------------------------------------------------------------
// MIPS program helpers (hand assembled, our own code)
// ----------------------------------------------------------------

func loadProg(m *Machine, addr uint32, words []uint32) {
	for i, w := range words {
		m.Bus.Write32(addr+uint32(4*i), w)
	}
}

func setPC(m *Machine, addr uint32) {
	m.CPU.PC = addr
	m.CPU.NextPC = addr + 4
}

// installCounterHandler writes an interrupt handler
// at 0x80000000 that increments the word at
// counterAddr, acknowledges I_STAT with ackValue,
// and returns through EPC/RFE. NOPs sit after the
// load and after mfc0: both results land a cycle
// late on the R3000, so the next instruction must
// not consume them.
//
//	lui k0, hi(counter) ; lw k1, lo(k0) ; nop
//	addiu k1, k1, 1 ; sw k1, lo(k0)
//	lui k0, 0x1F80 ; lui k1, hi(ack) ; ori k1, lo(ack)
//	sw k1, 0x1070(k0) ; mfc0 k0, EPC ; nop
//	jr k0 ; rfe
func installCounterHandler(m *Machine, counterAddr, ackValue uint32) {
	hi := counterAddr >> 16
	lo := counterAddr & 0xFFFF
	words := []uint32{
		0x3C1A0000 | hi,                  // lui k0, hi
		0x8F5B0000 | lo,                  // lw k1, lo(k0)
		0x00000000,                       // nop (load delay)
		0x277B0001,                       // addiu k1, k1, 1
		0xAF5B0000 | lo,                  // sw k1, lo(k0)
		0x3C1A1F80,                       // lui k0, 0x1F80
		0x3C1B0000 | (ackValue >> 16),    // lui k1, hi(ack)
		0x377B0000 | (ackValue & 0xFFFF), // ori k1, k1, lo(ack)
		0xAF5B1070,                       // sw k1, 0x1070(k0)  I_STAT ack
		0x401A7000,                       // mfc0 k0, EPC
		0x00000000,                       // nop (COP0 delay)
		0x03400008,                       // jr k0
		0x42000010,                       // rfe
	}
	loadProg(m, 0x80000000, words)
}

// spinProgram enables interrupts (SR = IEc + IM
// external) and loops forever. Setup words before
// the loop are supplied by the test.
func spinProgram(setup []uint32) []uint32 {
	prog := append([]uint32{}, setup...)
	prog = append(prog,
		0x24090401, // addiu t1, zero, 0x401
		0x40896000, // mtc0 t1, SR
		0x1000FFFF, // beq zero, zero, -1
		0x00000000, // nop (delay slot)
	)
	return prog
}

func TestVBlankInterruptProgram(t *testing.T) {
	m := New()
	counter := uint32(0x80012000)
	installCounterHandler(m, counter, 0xFFFFFFFE) // ack bit 0 only
	// I_MASK = VBlank only.
	prog := spinProgram([]uint32{
		0x3C081F80, // lui t0, 0x1F80
		0x24090001, // addiu t1, zero, 1
		0xAD091074, // sw t1, 0x1074(t0)
	})
	loadProg(m, 0x80010000, prog)
	setPC(m, 0x80010000)
	for i := 0; i < 3; i++ {
		m.RunFrame()
	}
	if got := m.Bus.Read32(counter); got != 3 {
		t.Fatalf("vblank handler count over 3 frames = %d, want 3", got)
	}
	// Masking at the controller stops delivery even
	// though the source keeps latching I_STAT.
	m.Bus.Write32(0x1F801074, 0)
	m.RunFrame()
	m.RunFrame()
	if got := m.Bus.Read32(counter); got != 3 {
		t.Errorf("count after masking = %d, want still 3", got)
	}
	if m.IStat()&(1<<IRQVBlank) == 0 {
		t.Errorf("I_STAT vblank bit not latched while masked")
	}
}

func TestTimerInterruptThroughISTAT(t *testing.T) {
	m := New()
	loadProg(m, 0x80010000, []uint32{0x1000FFFF, 0x00000000})
	setPC(m, 0x80010000)
	// Timer 0: target 100, reset at target, IRQ at
	// target, repeat. Configured through the real
	// register page.
	m.Bus.Write32(0x1F801074, 1<<IRQTMR0) // I_MASK
	m.Bus.Write16(0x1F801108, 100)        // target
	m.Bus.Write16(0x1F801104, 0x0058)     // mode
	m.RunCycles(950)
	if m.IStat()&(1<<IRQTMR0) == 0 {
		t.Fatalf("timer 0 IRQ never reached I_STAT: I_STAT=0x%04X", m.IStat())
	}
	// Acknowledge: write with the bit 0 clears it.
	m.Bus.Write32(0x1F801070, 0xFFFFFFEF)
	if m.IStat()&(1<<IRQTMR0) != 0 {
		t.Errorf("I_STAT timer bit not cleared by ack write")
	}
	// Counter kept counting from the mode write:
	// after 950 cycles with period 100 it wrapped 9
	// times and reads 50 (reads happen at the last
	// instruction boundary, allow the final partial
	// instruction of slack).
	ctr := m.Bus.Read16(0x1F801100)
	if ctr < 45 || ctr > 55 {
		t.Errorf("timer 0 counter = %d, want about 50", ctr)
	}
}

func TestHBlankTimerExactOverDisplayFrame(t *testing.T) {
	m := New()
	loadProg(m, 0x80010000, []uint32{0x1000FFFF, 0x00000000})
	setPC(m, 0x80010000)
	// Timer 1 clocked by hblank.
	m.Bus.Write16(0x1F801114, 0x0100)
	base := m.DisplayFrames()
	// Step in small increments and stop just past the
	// wrap: the overshoot into the next frame stays a
	// fraction of a line, far from the next hblank
	// pulse at dot 3072, so the count is exact.
	for m.DisplayFrames() == base {
		m.RunCycles(100)
	}
	// One hblank pulse per line, 263 lines, exactly:
	// the beam overshoot into the next frame is a
	// fraction of one line and cannot reach the next
	// pulse at dot 3072.
	if got := m.Timers.T[1].Counter; got != 263 {
		t.Errorf("timer 1 hblank count = %d, want 263", got)
	}
}

func TestHBlankTimerPAL(t *testing.T) {
	m := New()
	loadProg(m, 0x80010000, []uint32{0x1000FFFF, 0x00000000})
	setPC(m, 0x80010000)
	m.Bus.Write32(0x1F801814, 0x08000008) // GP1 mode: PAL
	m.Bus.Write16(0x1F801114, 0x0100)     // timer 1: hblank clock
	for m.DisplayFrames() == 0 {
		m.RunCycles(100)
	}
	if got := m.Timers.T[1].Counter; got != 314 {
		t.Errorf("PAL timer 1 hblank count = %d, want 314", got)
	}
}

func TestDMAGPUFillGate(t *testing.T) {
	m := New()
	// Draw area wide open.
	m.Bus.Write32(0x1F801810, 0xE3000000)
	m.Bus.Write32(0x1F801810, 0xE407FFFF)
	// Packet in RAM: one GP0 fill (green, 4x4 at
	// (60,60)).
	const pkt = 0x80015000
	loadProg(m, pkt, []uint32{0x0200FF00, 0x003C003C, 0x00040004})
	// DICR: enable ch2 IRQ + master.
	m.Bus.Write32(0x1F8010F4, (1<<18)|(1<<23))
	// DPCR: enable ch2.
	m.Bus.Write32(0x1F8010F0, 0x0800)
	m.Bus.Write32(0x1F8010A0, pkt)        // MADR
	m.Bus.Write32(0x1F8010A4, 3)          // BCR: 3 words
	m.Bus.Write32(0x1F8010A8, 0x01000001) // CHCR: from RAM, sync 0, start
	if got := m.GPU.VRAMAt(60, 60); got != 0x03E0 {
		t.Errorf("fill pixel = 0x%04X, want 0x03E0 (DMA fed the fill)", got)
	}
	if got := m.GPU.VRAMAt(63, 63); got != 0x03E0 {
		t.Errorf("fill corner = 0x%04X, want 0x03E0", got)
	}
	dicr := m.Bus.Read32(0x1F8010F4)
	if dicr&(1<<26) == 0 || dicr&(1<<31) == 0 {
		t.Errorf("DICR = 0x%08X, want ch2 flag (bit 26) and master (bit 31)", dicr)
	}
	if m.IStat()&(1<<IRQDMA) == 0 {
		t.Errorf("DMA completion did not reach I_STAT bit 3")
	}
}

func TestDMALinkedListGate(t *testing.T) {
	m := New()
	m.Bus.Write32(0x1F801810, 0xE3000000)
	m.Bus.Write32(0x1F801810, 0xE407FFFF)
	// Three nodes, each one fill command (3 words).
	loadProg(m, 0x80016000, []uint32{
		0x03016100, 0x020000FF, 0x00640064, 0x00020002, // node A -> B, red at (100,100)
	})
	loadProg(m, 0x80016100, []uint32{
		0x03016200, 0x0200FF00, 0x006E006E, 0x00020002, // node B -> C, green at (110,110)
	})
	loadProg(m, 0x80016200, []uint32{
		0x03FFFFFF, 0x02FF0000, 0x00780078, 0x00020002, // node C end, blue at (120,120)
	})
	m.Bus.Write32(0x1F8010F0, 0x0800)
	m.Bus.Write32(0x1F8010A0, 0x80016000)
	m.Bus.Write32(0x1F8010A8, 0x01000401) // sync 2 linked list, from RAM, start
	if got := m.GPU.VRAMAt(100, 100); got != 0x001F {
		t.Errorf("node A fill = 0x%04X, want 0x001F", got)
	}
	if got := m.GPU.VRAMAt(110, 110); got != 0x03E0 {
		t.Errorf("node B fill = 0x%04X, want 0x03E0", got)
	}
	if got := m.GPU.VRAMAt(120, 120); got != 0x7C00 {
		t.Errorf("node C fill = 0x%04X, want 0x7C00", got)
	}
}

func TestDMAInterruptProgram(t *testing.T) {
	m := New()
	m.Bus.Write32(0x1F801810, 0xE3000000)
	m.Bus.Write32(0x1F801810, 0xE407FFFF)
	const pkt = 0x80017000
	loadProg(m, pkt, []uint32{0x0200FF00, 0x003C003C, 0x00040004})
	counter := uint32(0x80012008)
	installCounterHandler(m, counter, 0xFFFFFFF7) // ack bit 3 only
	prog := spinProgram([]uint32{
		0x3C081F80, // lui t0, 0x1F80
		0x24090008, // addiu t1, zero, 8 (DMA mask)
		0xAD091074, // sw t1, I_MASK
		0x24090800, // addiu t1, zero, 0x800 (DPCR ch2 enable)
		0xAD0910F0, // sw t1, DPCR
		0x3C090084, // lui t1, 0x0084 (DICR en ch2 + master)
		0xAD0910F4, // sw t1, DICR
		0x3C098001, // lui t1, 0x8001
		0x35297000, // ori t1, 0x7000 (packet address)
		0xAD0910A0, // sw t1, MADR
		0x24090003, // addiu t1, zero, 3
		0xAD0910A4, // sw t1, BCR
		0x3C090100, // lui t1, 0x0100
		0x35290001, // ori t1, 1 (CHCR start, from RAM, sync 0)
		0xAD0910A8, // sw t1, CHCR: triggers the transfer
	})
	loadProg(m, 0x80010000, prog)
	setPC(m, 0x80010000)
	m.RunCycles(100000)
	if got := m.Bus.Read32(counter); got != 1 {
		t.Errorf("DMA handler count = %d, want 1", got)
	}
	if got := m.GPU.VRAMAt(60, 60); got != 0x03E0 {
		t.Errorf("DMA fill pixel = 0x%04X, want 0x03E0", got)
	}
}

// seqDisc is a synthetic cd.Reader for the system
// gates: sector LBA n is filled with the pattern
// byte(i) = byte(i>>2) + byte(n), so word w of
// sector 0 reads as 0xWWWWWWWW for w < 256.
type seqDisc struct{ sectors int }

func (d *seqDisc) Tracks() []cd.Track {
	return []cd.Track{{Number: 1, Mode: cd.TrackData, SectorSize: 2048, UserSize: 2048, Length: d.sectors}}
}
func (d *seqDisc) ReadSector(lba int, raw bool) ([]byte, error) {
	if lba < 0 || lba >= d.sectors {
		return nil, errLBA
	}
	out := make([]byte, 2048)
	for i := range out {
		out[i] = byte(i>>2) + byte(lba)
	}
	return out, nil
}
func (d *seqDisc) Close() error { return nil }

var errLBA = errorString("lba out of range")

type errorString string

func (e errorString) Error() string { return string(e) }

func TestCDROMDMAGate(t *testing.T) {
	m := New()
	disc := &seqDisc{sectors: 16}
	if err := m.LoadDisc(disc); err != nil {
		t.Fatalf("LoadDisc: %v", err)
	}
	// CD: enable IRQ types, Setloc 00:02:00 (LBA 0),
	// ReadN.
	m.Bus.Write8(0x1F801803, 0x1F) // irq enable (index 0)
	m.Bus.Write8(0x1F801802, 0x00) // param mm
	m.Bus.Write8(0x1F801802, 0x02) // param ss
	m.Bus.Write8(0x1F801802, 0x00) // param ff
	m.Bus.Write8(0x1F801801, 0x02) // Setloc
	m.Bus.Read8(0x1F801801)        // drain response
	m.Bus.Write8(0x1F801801, 0x06) // ReadN
	if m.CD.DataLen() != 2048 {
		t.Fatalf("CD data FIFO = %d bytes, want 2048", m.CD.DataLen())
	}
	if m.IStat()&(1<<IRQCDROM) == 0 {
		t.Errorf("CD IRQ did not reach I_STAT bit 2")
	}
	// DMA ch3 pulls the sector into RAM at 0x80013000.
	m.Bus.Write32(0x1F8010F0, 0x8000) // DPCR ch3 enable
	m.Bus.Write32(0x1F8010B0, 0x80013000)
	m.Bus.Write32(0x1F8010B4, 512) // one sector of words
	m.Bus.Write32(0x1F8010B8, 0x01000000)
	if got := m.Bus.Read32(0x80013000 + 12); got != 0x03030303 {
		t.Errorf("DMA word 3 = 0x%08X, want 0x03030303", got)
	}
	if got := m.Bus.Read32(0x80013000 + 255*4); got != 0xFFFFFFFF {
		t.Errorf("DMA word 255 = 0x%08X, want 0xFFFFFFFF", got)
	}
}

// setupDeterminismMachine builds the determinism
// gate machine: a 4x4 15-bit texture in VRAM, a
// linked-list DMA packet holding one textured quad
// command stream, the DMA executed, and a CPU
// program spinning on timer 0 target IRQs.
func setupDeterminismMachine(t *testing.T) *Machine {
	t.Helper()
	m := New()
	gp0 := func(words ...uint32) {
		for _, w := range words {
			m.Bus.Write32(0x1F801810, w)
		}
	}
	gp0(0xE3000000, 0xE407FFFF) // drawing area full
	// Upload the 4x4 texture (halfword i = 0x1000+i)
	// to VRAM (0,0) via a CPU to VRAM transfer.
	gp0(0xA0000000, 0x00000000, 0x00040004)
	for w := 0; w < 8; w++ {
		lo := uint32(0x1000 + 2*w)
		hi := uint32(0x1000 + 2*w + 1)
		gp0(lo | hi<<16)
	}
	// Linked list node at 0x80014000: one textured
	// quad (9 words) blended with neutral color.
	loadProg(m, 0x80014000, []uint32{
		0x09FFFFFF,             // header: 9 words, end of list
		0x2C808080,             // textured quad, color 0x808080
		0x00100010, 0x00000000, // v0 (16,16), uv0
		0x00100014, 0x01000003, // v1 (20,16), uv1 u=3 + 15-bit page
		0x00140010, 0x00000300, // v2 (16,20), uv2 v=3
		0x00140014, 0x00000303, // v3 (20,20), uv3
	})
	// GP1: DMA direction CPU to GP0; DPCR enable ch2;
	// run the linked list.
	m.Bus.Write32(0x1F801814, 0x04000002)
	m.Bus.Write32(0x1F8010F0, 0x0800)
	m.Bus.Write32(0x1F8010A0, 0x80014000)
	m.Bus.Write32(0x1F8010A8, 0x01000401)
	// The quad must already be drawn, with exact
	// texel colors (neutral modulation).
	if got := m.GPU.VRAMAt(16, 16); got != 0x1000 {
		t.Fatalf("quad texel (0,0) = 0x%04X, want 0x1000", got)
	}
	// (19,19) interpolates to texel (2,2) under the
	// barycentric rule; (16,19) lands on texel (0,2).
	if got := m.GPU.VRAMAt(19, 19); got != 0x100A {
		t.Fatalf("quad texel at (19,19) = 0x%04X, want 0x100A (texel 2,2)", got)
	}
	if got := m.GPU.VRAMAt(16, 19); got != 0x1008 {
		t.Fatalf("quad texel at (16,19) = 0x%04X, want 0x1008 (texel 0,2)", got)
	}
	// CPU program: timer 0 target IRQ every 5000
	// cycles, handler counts them.
	installCounterHandler(m, 0x8001200C, 0xFFFFFFEF)
	prog := spinProgram([]uint32{
		0x3C081F80, // lui t0, 0x1F80
		0x24090010, // addiu t1, zero, 16 (timer 0 mask)
		0xAD091074, // sw t1, I_MASK
		0x24091388, // addiu t1, zero, 5000
		0xAD091108, // sw t1, timer 0 target
		0x24090058, // addiu t1, zero, 0x58 (reset at target, IRQ, repeat)
		0xAD091104, // sw t1, timer 0 mode
	})
	loadProg(m, 0x80010000, prog)
	setPC(m, 0x80010000)
	return m
}

func TestDeterminismGate(t *testing.T) {
	m := setupDeterminismMachine(t)
	m.RunCycles(60000)
	irqCount := m.Bus.Read32(0x8001200C)
	if irqCount < 5 {
		t.Fatalf("timer IRQ count after 60000 cycles = %d, want at least 5 (spinning on IRQ works)", irqCount)
	}
	mid := m.Serialize()
	m.RunCycles(30000)
	wantState := m.Serialize()
	wantFB := append([]uint32(nil), m.Framebuffer()...)
	wantCount := m.Bus.Read32(0x8001200C)

	m2 := setupDeterminismMachine(t)
	if err := m2.Deserialize(mid); err != nil {
		t.Fatalf("Deserialize mid-frame state: %v", err)
	}
	m2.RunCycles(30000)
	if got := m2.Bus.Read32(0x8001200C); got != wantCount {
		t.Errorf("restored IRQ count = %d, want %d", got, wantCount)
	}
	gotFB := m2.Framebuffer()
	if len(gotFB) != len(wantFB) {
		t.Fatalf("framebuffer size diverged")
	}
	for i := range wantFB {
		if gotFB[i] != wantFB[i] {
			t.Fatalf("framebuffer pixel %d diverged: 0x%08X vs 0x%08X", i, gotFB[i], wantFB[i])
		}
	}
	if got := m2.Serialize(); !bytes.Equal(got, wantState) {
		t.Errorf("state after identical continuation diverged")
	}
}

func TestGPUInterruptSource(t *testing.T) {
	m := New()
	// GP0(1F) through the GP0 port latches I_STAT
	// bit 1 without any CPU step.
	m.Bus.Write32(0x1F801810, 0x1F000000)
	if m.IStat()&(1<<IRQGPU) == 0 {
		t.Errorf("GP0 IRQ command did not latch I_STAT bit 1")
	}
	// GP1(02) clears the GPUSTAT request, I_STAT
	// stays latched until acknowledged by write.
	m.Bus.Write32(0x1F801814, 0x02000000)
	if m.GPU.IRQRequested() {
		t.Errorf("GP1(02) did not clear GPUSTAT irq")
	}
	m.Bus.Write32(0x1F801070, 0xFFFFFFFD)
	if m.IStat()&(1<<IRQGPU) != 0 {
		t.Errorf("I_STAT GPU bit not acked")
	}
}
