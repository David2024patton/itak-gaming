package system

import (
	"bytes"
	"math"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-gamecube/chips/gekko"
	"github.com/davidpatton/itak-gaming/cores/go-gamecube/disc"
)

// MMIO window bases used by the GCN-3 tests, at the
// uncached alias software normally uses.
const (
	mmioVI  = 0xCC002000
	mmioPI  = 0xCC003000
	mmioDSP = 0xCC005000
	mmioDI  = 0xCC006000
	mmioSI  = 0xCC006400
	mmioEXI = 0xCC006800
	mmioAI  = 0xCC006C00
)

// PI cause bits, mirrored from system.go for assertions.
const (
	piBitDSP uint32 = 0x00000001
	piBitAI  uint32 = 0x00000002
	piBitEXI uint32 = 0x00000004
	piBitSI  uint32 = 0x00000008
	piBitDI  uint32 = 0x00000010
	piBitVI  uint32 = 0x00000020
)

func TestDIReadThroughMMIO(t *testing.T) {
	img, err := disc.Parse(syntheticImage())
	if err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.LoadDisc(img); err != nil {
		t.Fatal(err)
	}

	// Unmask the transfer complete interrupt, aim the DMA
	// at MEM1 0x4000, and read 64 disc bytes from disc
	// offset 0x40 (CMDBUF1 holds the offset shifted by 2).
	m.Write32(mmioDI+0x00, 0x10)       // DISR: TCINTMSK
	m.Write32(mmioDI+0x08, 0xA8000000) // DICMDBUF0: read
	m.Write32(mmioDI+0x0C, 0x10)       // DICMDBUF1: offset 0x40
	m.Write32(mmioDI+0x10, 64)         // DICMDBUF2: length
	m.Write32(mmioDI+0x18, 0x4000)     // DIMAR
	m.Write32(mmioDI+0x20, 0x3)        // DICR: TSTART | DMA

	want := make([]byte, 64)
	if n := img.ReadAt(0x40, want); n != 64 {
		t.Fatalf("disc ReadAt = %d, want 64", n)
	}
	for i, b := range want {
		if got := m.Read8(uint32(0x4000 + i)); got != b {
			t.Fatalf("MEM1 byte %d = %#x, want %#x from the disc", i, got, b)
		}
	}
	if got := m.Read32(mmioDI + 0x1C); got != 0 {
		t.Fatalf("DILENGTH = %d, want 0 when done", got)
	}
	// The PI aggregate shows the DI cause, and with the DI
	// bit in PI_INTMSK the CPU IRQ line asserts.
	if got := m.Read32(mmioPI + 0x00); got&piBitDI == 0 {
		t.Fatalf("PI_INTSR = %#x, want the DI bit set", got)
	}
	m.Write32(mmioPI+0x04, piBitDI)
	if got := m.Read32(mmioPI + 0x04); got != piBitDI {
		t.Fatalf("PI_INTMSK = %#x, want %#x", got, piBitDI)
	}
	if !m.CPU().IRQ() {
		t.Fatal("CPU IRQ line not asserted with DI pending and unmasked in PI")
	}
}

func TestSIPollThroughMMIO(t *testing.T) {
	m := New()
	m.SetButtons(PadState{
		Buttons: PadA | PadStart, TriggerL: 3, TriggerR: 4,
		StickX: 200, StickY: 30, CStickX: 1, CStickY: 2,
	})
	// POLL with channel 0 enabled polls at once in this
	// model.
	m.Write32(mmioSI+0x30, 1)
	hi := m.Read32(mmioSI + 0x04)
	wantHi := uint32(PadA|PadStart)<<16 | 200<<8 | 30
	if hi != wantHi {
		t.Fatalf("SI INBUF_HI = %#08x, want %#08x", hi, wantHi)
	}
	lo := m.Read32(mmioSI + 0x08)
	wantLo := uint32(1)<<24 | uint32(2)<<16 | uint32(3)<<8 | 4
	if lo != wantLo {
		t.Fatalf("SI INBUF_LO = %#08x, want %#08x", lo, wantLo)
	}
}

func TestVIInterruptsOverThreeFrames(t *testing.T) {
	m := loadMachine(t)
	m.Start()
	// Display interrupt 0 at halfline 4, enabled, and the
	// VI cause unmasked in the PI.
	m.Write32(mmioVI+0x14, (1<<28)|4) // VIDI0: DIE | target 4
	m.Write32(mmioPI+0x04, piBitVI)

	fires := 0
	for frame := 0; frame < 3; frame++ {
		m.RunFrame()
		if got := m.Read32(mmioPI + 0x00); got&piBitVI != 0 {
			fires++
		} else {
			t.Fatalf("frame %d: PI_INTSR = %#x, want the VI bit set", frame, got)
		}
		// Clear DIS between frames, as hardware requires:
		// write the VIDI0 high lane with DIS clear and DIE
		// kept (DIE is lane bit 12).
		m.Write16(mmioVI+0x14, 1<<12)
		if got := m.Read32(mmioPI + 0x00); got&piBitVI != 0 {
			t.Fatalf("frame %d: VI bit still set after the DIS clear", frame)
		}
	}
	if fires != 3 {
		t.Fatalf("VI assertions over 3 frames = %d, want exactly 3", fires)
	}
}

// spinImage builds a synthetic disc whose DOL is a
// single branch-to-self at 0x80001000: a CPU that steps
// forever without touching anything, for tests that only
// need the devices clocked.
func spinImage() []byte {
	data := make([]byte, 0x10000)
	data[0] = 'G'
	copy(data[0x20:], "GCN-3 Spin Synthetic")
	put32(data, 0x1C, disc.GCMagic)
	put32(data, 0x420, 0x5000) // DOL offset
	dol := uint32(0x5000)
	put32(data, dol+0x00, 0x100)      // text0 offset
	put32(data, dol+0x48, 0x80001000) // text0 address
	put32(data, dol+0x90, 4)
	put32(data, dol+0xE0, 0x80001000) // entry
	put32(data, 0x5100, 18<<26)       // b .
	return data
}

func spinMachine(t *testing.T) *Machine {
	t.Helper()
	img, err := disc.Parse(spinImage())
	if err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.LoadDisc(img); err != nil {
		t.Fatal(err)
	}
	m.Start()
	return m
}

// aiFillSine fills MEM1 at the physical base address
// with a stereo sine buffer.
func aiFillSine(m *Machine, base uint32, frames int, freq float64) {
	for i := 0; i < frames; i++ {
		phase := 2 * math.Pi * freq * float64(i) / 48000
		s := uint16(int16(10000 * math.Sin(phase)))
		off := base + uint32(i*4)
		m.Write16(off, s)
		m.Write16(off+2, s)
	}
}

// aiProgram programs the AI through MMIO to stream
// frames stereo frames from the physical base address.
func aiProgram(m *Machine, base uint32, frames int, volL, volR uint32) {
	m.Write32(mmioAI+0x04, volL<<16|volR) // AIVR
	m.Write32(mmioAI+0x20, base)          // DMA address
	// DMA length: the length low lane first, then the
	// high lane with the enable bit, because the DMA
	// starts on the high lane write.
	m.Write16(mmioAI+0x26, uint16(frames*4))
	m.Write16(mmioAI+0x24, 0x8000|uint16(frames*4>>16))
	m.Write32(mmioAI+0x00, 1|2) // AICR: PSTAT | AIINTMSK
}

func TestAIDMAAtSystemLevel(t *testing.T) {
	m := spinMachine(t)

	const frames = 960 // 20 ms at 48000 Hz
	const base = 0x8000
	aiFillSine(m, base, frames, 440)
	aiProgram(m, base, frames, 127, 127)
	if got := m.AudioRate(); got != 48000 {
		t.Fatalf("AudioRate = %d, want 48000", got)
	}
	m.Write32(mmioPI+0x04, piBitAI)
	m.RunCycles(frames * 10125) // exactly 960 sample frames

	out := m.AudioBuffer()
	if len(out) != 2*frames {
		t.Fatalf("AudioBuffer = %d samples, want %d", len(out), 2*frames)
	}
	crossings := 0
	for i := 1; i < frames; i++ {
		if out[(i-1)*2] < 0 && out[i*2] >= 0 {
			crossings++
		}
	}
	// 440 Hz over 20 ms is 8.8 periods: 8 or 9 positive
	// zero crossings, a frequency within tolerance.
	if crossings < 8 || crossings > 9 {
		t.Fatalf("positive zero crossings = %d, want 8 or 9 (440 Hz)", crossings)
	}
	freq := float64(crossings) * 48000 / frames
	if freq < 400 || freq > 480 {
		t.Fatalf("measured frequency = %.0f Hz, want 440 within tolerance", freq)
	}
	// The DMA completion raised the AI interrupt, visible
	// in the PI aggregate and on the CPU IRQ line.
	if got := m.Read32(mmioPI + 0x00); got&piBitAI == 0 {
		t.Fatalf("PI_INTSR = %#x, want the AI bit set", got)
	}
	if !m.CPU().IRQ() {
		t.Fatal("CPU IRQ line not asserted with AI pending and unmasked in PI")
	}

	// Volume scaling: a short constant stream at left
	// volume 64, right volume 127.
	for i := 0; i < 8; i++ {
		off := base + uint32(i*4)
		m.Write16(off, 1000)
		m.Write16(off+2, uint16(0xF830)) // -2000
	}
	// Clear AIINT (write 1 to clear), keep playing.
	m.Write32(mmioAI+0x00, 1|2|4)
	aiProgram(m, base, 8, 64, 127)
	m.RunCycles(8 * 10125)
	out = m.AudioBuffer()
	if len(out) < 2 {
		t.Fatalf("volume run produced %d samples", len(out))
	}
	if out[0] != 503 { // 1000 * 64 / 127
		t.Fatalf("left sample at volume 64 = %d, want 503", out[0])
	}
	if out[1] != -2000 { // full volume passes through
		t.Fatalf("right sample at volume 127 = %d, want -2000", out[1])
	}
}

// dspMailProgram is a small IRAM program in the
// published GCDSP encodings: it loads the CPU mail low
// word from DMBL (0xFFFD, which clears the pending
// flag), doubles it in ACC0 through the AX0 path, loads
// the magic 0x2A2A into ACC0.M, stores ACC0.M to DMBH
// and ACC0.L to DMBL (which commits the return mail),
// raises DSPINT through DIRQ, and halts.
func dspMailProgram() []uint16 {
	return []uint16{
		0x00D8, 0xFFFD, // LR $ax0.l, @0xFFFD
		0x6800,         // MOVAX $ac0, $ax0
		0x4800,         // ADDAX $ac0, $ax0 (ac0 = 2 * mail low)
		0x009D, 0x2A2A, // LRI $ac0.m, #0x2A2A
		0x0092, 0x00FF, // LRI $cr, #0xFF (SRS page)
		0x2DFC,         // SRS @0xFFFC, $ac0.m (DMBH = magic)
		0x2CFD,         // SRS @0xFFFD, $ac0.l (DMBL = doubled)
		0x16F9, 0x0001, // SI @0xFFF9, #1 (DIRQ)
		0x0021, // HALT
	}
}

// dspSetup uploads the mail program through the DSP
// accessor, releases DSPRST with DSPINTMSK set through
// the CONTROL lane at the DSP window, and sends the
// mail 0x00001234 from the CPU side.
func dspSetup(t *testing.T, m *Machine) {
	t.Helper()
	m.DSP().UploadIRAM(0, dspMailProgram())
	// CONTROL: DSPRST clear (release), DSPINTMSK set.
	m.Write16(mmioDSP+0x0C, 0x0008)
	m.Write16(mmioDSP+0x00, 0x0000) // mail high
	m.Write16(mmioDSP+0x02, 0x1234) // mail low, sets pending
}

func TestDSPMailboxAtSystemLevel(t *testing.T) {
	m := spinMachine(t)
	dspSetup(t, m)
	m.RunCycles(600)

	hi := m.Read16(mmioDSP + 0x04)
	if hi != 0x8000|0x2A2A {
		t.Fatalf("DSP mail high = %#04x, want 0x8000|0x2A2A (ready, magic)", hi)
	}
	lo := m.Read16(mmioDSP + 0x06)
	if lo != 0x2468 {
		t.Fatalf("DSP mail low = %#04x, want 0x2468 (doubled 0x1234)", lo)
	}
	// The DIRQ raised the DSP cause in the PI aggregate,
	// and unmasking it there asserts the CPU IRQ line.
	if got := m.Read32(mmioPI + 0x00); got&piBitDSP == 0 {
		t.Fatalf("PI_INTSR = %#x, want the DSP bit set", got)
	}
	m.Write32(mmioPI+0x04, piBitDSP)
	if !m.CPU().IRQ() {
		t.Fatal("CPU IRQ line not asserted with DSP pending and unmasked in PI")
	}
}

// bootImage builds a synthetic disc whose DOL stores a
// magic word at 0x80003000 and spins, with a BSS block
// ending at physical 0x5100 and an FST of fstSize pattern
// bytes at disc offset 0x6000.
func bootImage(fstSize uint32) []byte {
	data := make([]byte, 0x10000)
	data[0] = 'G'
	copy(data[0x20:], "GCN-3 Boot Synthetic")
	put32(data, 0x1C, disc.GCMagic)
	put32(data, 0x420, 0x5000)  // DOL offset
	put32(data, 0x424, 0x6000)  // FST offset
	put32(data, 0x428, fstSize) // FST size
	for i := uint32(0); i < fstSize; i++ {
		data[0x6000+i] = byte(i*3 + 1)
	}

	prog := []uint32{
		d(15, 3, 0, 0x8000), // addis r3, r0, 0x8000
		d(14, 3, 3, 0x3000), // addi r3, r3, 0x3000
		d(15, 4, 0, 0x1234), // addis r4, r0, 0x1234
		d(24, 4, 4, 0x5678), // ori r4, r4, 0x5678
		d(36, 4, 3, 0),      // stw r4, 0(r3)
		18 << 26,            // spin: b .
	}
	// The text sits at physical 0x2100, above the boot
	// exception vectors (0x100 to 0x1F00) and the return
	// trampoline at 0x2000, as real DOLs keep low memory
	// clear.
	dol := uint32(0x5000)
	put32(data, dol+0x00, 0x100)      // text0 offset
	put32(data, dol+0x48, 0x80002100) // text0 address
	put32(data, dol+0x90, uint32(4*len(prog)))
	put32(data, dol+0xD8, 0x80005000) // BSS address
	put32(data, dol+0xDC, 0x100)      // BSS size: ends at physical 0x5100
	put32(data, dol+0xE0, 0x80002100) // entry
	for i, w := range prog {
		put32(data, 0x5100+uint32(4*i), w)
	}
	return data
}

func TestBootDiscGate(t *testing.T) {
	if err := New().BootDisc(); err != ErrNoDisc {
		t.Fatalf("BootDisc with no disc = %v, want ErrNoDisc", err)
	}

	const fstSize = 0x30
	img, err := disc.Parse(bootImage(fstSize))
	if err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.LoadDisc(img); err != nil {
		t.Fatal(err)
	}
	if err := m.BootDisc(); err != nil {
		t.Fatal(err)
	}
	cpu := m.CPU()

	// FST placement: immediately after the BSS end
	// (physical 0x5100, already 32-byte aligned).
	const fstPhys = 0x5100
	const fstEff = 0x80000000 | fstPhys
	if cpu.GPR[3] != fstEff {
		t.Fatalf("r3 = %#x, want the FST effective address %#x", cpu.GPR[3], fstEff)
	}
	if cpu.GPR[4] != fstSize {
		t.Fatalf("r4 = %#x, want the FST size %#x", cpu.GPR[4], fstSize)
	}
	for i := uint32(0); i < fstSize; i++ {
		if got, want := m.Read8(fstEff+i), byte(i*3+1); got != want {
			t.Fatalf("FST byte %d at the effective address = %#x, want %#x", i, got, want)
		}
	}

	// Boot info block.
	checks := []struct {
		off  uint32
		want uint32
		name string
	}{
		{0x00, 1, "console type"},
		{0x20, 0x0D15EA5E, "BS2 executed magic"},
		{0x28, MEM1Size, "simulated memory size"},
		{0x2C, fstPhys, "FST start physical"},
		// Arena low is the FST end aligned up to 32 bytes:
		// 0x5100 + 0x30 = 0x5130, aligned up is 0x5140.
		{0x30, 0x80000000 | 0x5140, "arena low"},
		{0x34, 0x81800000, "arena high"},
		{0x38, fstSize, "FST max size"},
	}
	for _, c := range checks {
		if got := m.Read32(c.off); got != c.want {
			t.Fatalf("boot info %s at %#x = %#x, want %#x", c.name, c.off, got, c.want)
		}
	}

	// CPU boot state.
	if cpu.GPR[1] != 0x817FFFE0 {
		t.Fatalf("r1 (stack) = %#x, want 0x817fffe0", cpu.GPR[1])
	}
	if cpu.LR != 0x80002000 {
		t.Fatalf("LR = %#x, want 0x80002000", cpu.LR)
	}
	if got := m.Read32(0x2000); got != 0x48000000 {
		t.Fatalf("trampoline at physical 0x2000 = %#x, want branch to self", got)
	}
	for _, vec := range []uint32{0x100, 0x500, 0x900, 0x1F00} {
		if got := m.Read32(vec); got != 0x48000000 {
			t.Fatalf("vector at %#x = %#x, want branch to self", vec, got)
		}
	}
	if cpu.MSR != gekko.MSRFP|gekko.MSRIR|gekko.MSRDR {
		t.Fatalf("MSR = %#x, want %#x (FP, IR, DR)", cpu.MSR, gekko.MSRFP|gekko.MSRIR|gekko.MSRDR)
	}
	if cpu.SR[0] != 0x80000000 || cpu.SR[15] != 0x80000000 {
		t.Fatalf("segment registers = %#x .. %#x, want 0x80000000", cpu.SR[0], cpu.SR[15])
	}
	bats := []struct {
		spr  int
		want uint32
	}{
		{528, 0x800003FF}, {529, 0x00000002},
		{530, 0x000003FF}, {531, 0x00000002},
		{536, 0x800003FF}, {537, 0x00000002},
		{538, 0x000003FF}, {539, 0x00000002},
	}
	for _, b := range bats {
		if got := cpu.GetSPR(b.spr); got != b.want {
			t.Fatalf("SPR %d = %#x, want %#x", b.spr, got, b.want)
		}
	}
	if cpu.PC != 0x80002100 || cpu.Halted {
		t.Fatalf("PC = %#x halted = %v, want entry 0x80002100 running", cpu.PC, cpu.Halted)
	}
	if !m.Started() || !m.Booted() {
		t.Fatal("machine should report started and booted after BootDisc")
	}

	// Control reaches the DOL entry: its first store lands
	// in MEM1 through the boot BATs.
	m.RunCycles(50)
	if got := m.Read32(0x80003000); got != 0x12345678 {
		t.Fatalf("DOL store at 0x80003000 = %#x, want 0x12345678", got)
	}
}

// TestGCN3ExtendedDeterminism boots the GCN-2 GX scene
// disc through BootDisc (its DOL text relocated above
// the boot vectors), runs it with a DSP mailbox program
// and an AI DMA stream live, saves mid-run, restores
// into a fresh booted machine, and requires the
// continuation to be byte-identical in state, pixels
// and audio.
func TestGCN3ExtendedDeterminism(t *testing.T) {
	raw := gxImage()
	// Relocate the DOL text and entry from physical
	// 0x1000 to 0x2100, above the boot exception vectors.
	put32(raw, 0x5000+0x48, 0x80002100)
	put32(raw, 0x5000+0xE0, 0x80002100)
	img, err := disc.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	setup := func() *Machine {
		m := New()
		if err := m.LoadDisc(img); err != nil {
			t.Fatal(err)
		}
		if err := m.BootDisc(); err != nil {
			t.Fatal(err)
		}
		dspSetup(t, m)
		aiFillSine(m, 0x6000, 480, 440)
		aiProgram(m, 0x6000, 480, 127, 127)
		return m
	}

	ref := setup()
	ref.RunCycles(200000)
	mid := ref.Serialize()
	if len(mid) != StateSize {
		t.Fatalf("state size = %d, want %d", len(mid), StateSize)
	}
	ref.RunCycles(200000)
	stateRef := ref.Serialize()
	fbRef := append([]uint32(nil), ref.Framebuffer()...)
	audioRef := ref.AudioBuffer()

	// Sanity: all three subsystems actually ran.
	if px := fbRef[240*Width+320]; px != 0xFFC81E3C {
		t.Fatalf("GX quad center pixel = %#08x, want ffc81e3c", px)
	}
	if len(audioRef) == 0 {
		t.Fatal("reference run produced no audio")
	}
	if lo := ref.Read16(mmioDSP + 0x06); lo != 0x2468 {
		t.Fatalf("reference DSP mail low = %#04x, want 0x2468", lo)
	}

	m2 := setup()
	if err := m2.Deserialize(mid); err != nil {
		t.Fatal(err)
	}
	m2.RunCycles(200000)
	if !bytes.Equal(m2.Serialize(), stateRef) {
		t.Fatal("continuation after restore diverged from the uninterrupted run")
	}
	fb2 := m2.Framebuffer()
	for i := range fbRef {
		if fb2[i] != fbRef[i] {
			t.Fatalf("framebuffer pixel %d differs after restore: %#08x vs %#08x", i, fb2[i], fbRef[i])
		}
	}
	audio2 := m2.AudioBuffer()
	if len(audio2) != len(audioRef) {
		t.Fatalf("audio after restore = %d samples, want %d", len(audio2), len(audioRef))
	}
	for i := range audioRef {
		if audio2[i] != audioRef[i] {
			t.Fatalf("audio sample %d differs after restore: %d vs %d", i, audio2[i], audioRef[i])
		}
	}
}
