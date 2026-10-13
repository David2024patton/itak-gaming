package system

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-gamecube/disc"
)

func d(op, f1v, f2v, imm uint32) uint32 {
	return op<<26 | f1v<<21 | f2v<<16 | imm&0xFFFF
}

func x(f1v, f2v, f3v, xov uint32) uint32 {
	return 31<<26 | f1v<<21 | f2v<<16 | f3v<<11 | xov<<1
}

func put32(b []byte, off uint32, v uint32) {
	binary.BigEndian.PutUint32(b[off:], v)
}

// testProgram is a hand-assembled PowerPC program: it
// sums the 16 data bytes at 0x80002000 into the word at
// 0x80003000, loads the decrementer with 40, enables
// external interrupts (which also drops the interrupt
// prefix, so the decrementer vector is physical 0x900),
// and spins. A one-word handler (b .) sits at 0x900.
func testProgram() []uint32 {
	mtsprDEC := uint32(31<<26 | 8<<21 | 704<<11 | 467<<1) // mtspr 22, r8
	return []uint32{
		d(14, 3, 0, 0),                  // addi r3, r0, 0        ; sum = 0
		d(15, 4, 0, 0x8000),             // addis r4, r0, 0x8000  ; r4 = 0x80000000
		d(14, 4, 4, 0x2000),             // addi r4, r4, 0x2000   ; data pointer
		d(14, 5, 0, 16),                 // addi r5, r0, 16       ; count
		d(34, 6, 4, 0),                  // loop: lbz r6, 0(r4)
		x(3, 3, 6, 266),                 // add r3, r3, r6
		d(14, 4, 4, 1),                  // addi r4, r4, 1
		d(14, 5, 5, 0xFFFF),             // addi r5, r5, -1
		d(11, 0, 5, 0),                  // cmpwi r5, 0
		16<<26 | 4<<21 | 2<<16 | 0xFFEC, // bne loop (-20)
		d(15, 7, 0, 0x8000),             // addis r7, r0, 0x8000
		d(14, 7, 7, 0x3000),             // addi r7, r7, 0x3000
		d(36, 3, 7, 0),                  // stw r3, 0(r7)
		d(14, 8, 0, 40),                 // addi r8, r0, 40
		mtsprDEC,                        // mtspr DEC, r8
		d(24, 0, 9, 0x8000),             // ori r9, r0, 0x8000 (MSR EE)
		x(9, 0, 0, 146),                 // mtmsr r9
		18 << 26,                        // spin: b .
	}
}

// syntheticImage builds a GC disc image whose DOL contains
// testProgram plus the decrementer handler and data.
func syntheticImage() []byte {
	data := make([]byte, 0x10000)
	data[0] = 'G'
	copy(data[0x20:], "System Synthetic")
	put32(data, 0x1C, disc.GCMagic)
	put32(data, 0x420, 0x5000) // DOL offset

	dol := uint32(0x5000)
	prog := testProgram()
	put32(data, dol+0x00, 0x100)      // text0 offset
	put32(data, dol+0x48, 0x80001000) // text0 address
	put32(data, dol+0x90, uint32(4*len(prog)))
	put32(data, dol+0x04, 0x200)      // text1 offset
	put32(data, dol+0x4C, 0x80000900) // text1 address (the DEC vector page)
	put32(data, dol+0x94, 4)
	put32(data, dol+0x1C, 0x300)      // data0 offset
	put32(data, dol+0x64, 0x80002000) // data0 address
	put32(data, dol+0xAC, 16)
	put32(data, dol+0xE0, 0x80001000) // entry

	for i, w := range prog {
		put32(data, 0x5100+uint32(4*i), w)
	}
	put32(data, 0x5200, 18<<26) // handler: b .
	for i := 0; i < 16; i++ {
		data[0x5300+i] = byte(i + 1)
	}
	return data
}

func loadMachine(t *testing.T) *Machine {
	t.Helper()
	img, err := disc.Parse(syntheticImage())
	if err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.LoadDisc(img); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMemoryMap(t *testing.T) {
	m := New()
	m.Write32(0x80000000, 0xDEADBEEF)
	if got := m.Read32(0x00000000); got != 0xDEADBEEF {
		t.Fatalf("physical alias read = %#x", got)
	}
	if got := m.Read32(0xC0000000); got != 0xDEADBEEF {
		t.Fatalf("uncached alias read = %#x", got)
	}
	m.Write16(0x0C001234, 0xABCD)
	if got := m.Read16(0x0C001234); got != 0xABCD {
		t.Fatalf("hw page = %#x", got)
	}
	m.Write8(0x0D00FFFF, 0x5A)
	if got := m.Read8(0x0D00FFFF); got != 0x5A {
		t.Fatalf("hw page 0D = %#x", got)
	}
	m.Write32(0xE0000100, 0x12345678)
	if got := m.Read32(0xE0000100); got != 0x12345678 {
		t.Fatalf("locked cache = %#x", got)
	}
	if got := m.Read32(0xFFF00100); got != 0 {
		t.Fatalf("IPL stub read = %#x, want 0", got)
	}
	m.Write32(0xFFF00100, 0xFFFFFFFF)
	if got := m.Read32(0xFFF00100); got != 0 {
		t.Fatalf("IPL stub accepted a write")
	}
	if got := m.Read32(0x10000000); got != 0 {
		t.Fatalf("unmapped read = %#x", got)
	}
}

func TestLoadAndRunWithSaveState(t *testing.T) {
	m := loadMachine(t)
	if m.CPU().PC != 0x80001000 {
		t.Fatalf("entry PC = %#x", m.CPU().PC)
	}
	if m.Started() {
		t.Fatal("machine should start halted")
	}
	// Halted: cycles pass, nothing executes, no result.
	m.RunCycles(200)
	if got := m.Read32(0x80003000); got != 0 {
		t.Fatalf("halted machine produced %#x", got)
	}
	if got := m.Read8(0x80002000); got != 1 {
		t.Fatalf("data section not staged: %#x", got)
	}
	if got := m.Read8(0x8000200F); got != 16 {
		t.Fatalf("data section end: %#x", got)
	}

	m.SetButtons(PadState{Buttons: PadA | PadStart, TriggerL: 12, StickX: 200, CStickY: 33})
	m.Start()
	// 103 instructions take the program through the stw
	// of the checksum (4 setup + 96 loop + 3).
	m.RunCycles(103)
	if got := m.Read32(0x80003000); got != 136 {
		t.Fatalf("checksum = %d, want 136", got)
	}
	state1 := m.Serialize()
	if len(state1) != StateSize {
		t.Fatalf("state size = %d, want %d", len(state1), StateSize)
	}

	// Continue: the decrementer (40 ticks of 4 cycles,
	// loaded at instruction 105, EE enabled at 107) fires
	// and the CPU lands in the handler at 0x900 spinning.
	m.RunCycles(400)
	if pc := m.CPU().PC; pc != 0x900 {
		t.Fatalf("PC after DEC exception = %#x, want 0x900", pc)
	}
	if srr0 := m.CPU().SRR0; srr0 != 0x80001044 {
		t.Fatalf("SRR0 = %#x, want the spin loop 0x80001044", srr0)
	}
	stateA := m.Serialize()

	// A fresh machine restored from the mid-run state must
	// continue bit-identically.
	m2 := loadMachine(t)
	if err := m2.Deserialize(state1); err != nil {
		t.Fatal(err)
	}
	if !m2.Started() {
		t.Fatal("restored machine lost its started flag")
	}
	if m2.Buttons() != m.Buttons() {
		t.Fatalf("restored buttons = %+v", m2.Buttons())
	}
	m2.RunCycles(400)
	if pc := m2.CPU().PC; pc != 0x900 {
		t.Fatalf("restored PC = %#x, want 0x900", pc)
	}
	if got := m2.Read32(0x80003000); got != 136 {
		t.Fatalf("restored checksum = %d", got)
	}
	if !bytes.Equal(m2.Serialize(), stateA) {
		t.Fatal("continuation after restore diverged from the uninterrupted run")
	}
}

func TestFramebufferAndBadStates(t *testing.T) {
	m := New()
	fb := m.Framebuffer()
	if len(fb) != Width*Height {
		t.Fatalf("framebuffer = %d pixels", len(fb))
	}
	// GCN-2: the framebuffer is the GPU's EFB presented
	// after reset, which is opaque black, not the zeroed
	// stub buffer of GCN-1.
	for i, px := range fb {
		if px != 0xFF000000 {
			t.Fatalf("framebuffer pixel %d = %#x, want opaque black", i, px)
		}
	}
	if CyclesPerFrame != 8_100_000 {
		t.Fatalf("CyclesPerFrame = %d", CyclesPerFrame)
	}
	if err := m.Deserialize([]byte{1, 2, 3}); err != ErrBadState {
		t.Fatalf("short state err = %v", err)
	}
	bad := m.Serialize()
	bad[0] = 99
	if err := m.Deserialize(bad); err != ErrBadState {
		t.Fatalf("wrong version err = %v", err)
	}
}

// gxStream builds the GX FIFO byte stream the GCN-2
// determinism program copies out of MEM1: CP setup for
// direct f32 position plus direct RGBA8 color vertices
// (VAT 0 programmed through the documented CP addresses:
// group A 0x70, group B 0x80, group C 0x90), BP setup for
// the RGBA8 EFB format with depth off, and one quad.
func gxStream() []byte {
	var b bytes.Buffer
	cp := func(reg byte, v uint32) {
		b.WriteByte(0x08)
		b.WriteByte(reg)
		var w [4]byte
		binary.BigEndian.PutUint32(w[:], v)
		b.Write(w[:])
	}
	bp := func(reg byte, v uint32) {
		b.WriteByte(0x61)
		var w [4]byte
		binary.BigEndian.PutUint32(w[:], uint32(reg)<<24|(v&0xFFFFFF))
		b.Write(w[:])
	}
	cp(0x50, (1<<9)|(1<<13)) // VCD low: position direct, color 0 direct
	cp(0x60, 0)              // VCD high
	// VAT 0 group A: position 3 components f32, color 0
	// present in RGBA8 format.
	cp(0x70, 1|(4<<1)|(1<<13)|(5<<14))
	cp(0x80, 0) // VAT 0 group B
	cp(0x90, 0) // VAT 0 group C
	bp(0x43, 2) // PE control: RGBA8 pixel format
	bp(0x40, 0) // Z mode off
	bp(0x00, 0) // gen mode: no culling
	b.WriteByte(0x80)
	var n [2]byte
	binary.BigEndian.PutUint16(n[:], 4)
	b.Write(n[:])
	verts := [][3]float32{
		{-0.5, 0.5, 0}, {0.5, 0.5, 0}, {0.5, -0.5, 0}, {-0.5, -0.5, 0},
	}
	for _, v := range verts {
		for _, f := range v {
			var w [4]byte
			binary.BigEndian.PutUint32(w[:], math.Float32bits(f))
			b.Write(w[:])
		}
		b.Write([]byte{200, 30, 60, 255})
	}
	return b.Bytes()
}

// gxProgram is the hand-assembled GCN-2 gate program: it
// enables the FPU, runs a paired-single add through the
// quantized load and store (GQR0 float, scale 0), then
// copies the GX FIFO stream staged at 0x80005000 byte by
// byte to the FIFO write port at 0xCC008000, and spins.
func gxProgram(streamLen int) []uint32 {
	psqL := uint32(56<<26 | 0<<21 | 3<<16)       // psq_l f0, 0(r3), W=0, GQR0
	psAdd := uint32(4<<26 | 1<<21 | 21<<1)       // ps_add f1, f0, f0
	psqSt := uint32(60<<26 | 1<<21 | 3<<16 | 16) // psq_st f1, 16(r3), W=0, GQR0
	return []uint32{
		d(24, 0, 9, 0x2000),             // ori r9, r0, 0x2000 (MSR FP)
		x(9, 0, 0, 146),                 // mtmsr r9
		d(15, 3, 0, 0x8000),             // addis r3, r0, 0x8000
		d(24, 3, 3, 0x4000),             // ori r3, r3, 0x4000
		psqL,                            // f0 = (1.5, 2.25)
		psAdd,                           // f1 = (3.0, 4.5)
		psqSt,                           // result cell at 0x80004010
		d(15, 3, 0, 0x8000),             // addis r3, r0, 0x8000
		d(24, 3, 3, 0x5000),             // ori r3, r3, 0x5000 (stream)
		d(14, 4, 0, uint32(streamLen)),  // addi r4, r0, len
		d(15, 5, 0, 0xCC00),             // addis r5, r0, 0xCC00
		d(24, 5, 5, 0x8000),             // ori r5, r5, 0x8000 (FIFO port)
		d(34, 6, 3, 0),                  // loop: lbz r6, 0(r3)
		d(38, 6, 5, 0),                  // stb r6, 0(r5)
		d(14, 3, 3, 1),                  // addi r3, r3, 1
		d(14, 4, 4, 0xFFFF),             // addi r4, r4, -1
		d(11, 0, 4, 0),                  // cmpwi r4, 0
		16<<26 | 4<<21 | 2<<16 | 0xFFEC, // bne loop
		18 << 26,                        // spin: b .
	}
}

// gxImage builds a synthetic disc whose DOL carries the
// gate program, the paired-single operands, and the FIFO
// stream.
func gxImage() []byte {
	stream := gxStream()
	prog := gxProgram(len(stream))
	data := make([]byte, 0x10000)
	data[0] = 'G'
	copy(data[0x20:], "GCN-2 Synthetic")
	put32(data, 0x1C, disc.GCMagic)
	put32(data, 0x420, 0x5000) // DOL offset

	dol := uint32(0x5000)
	put32(data, dol+0x00, 0x100)      // text0 offset
	put32(data, dol+0x48, 0x80001000) // text0 address
	put32(data, dol+0x90, uint32(4*len(prog)))
	put32(data, dol+0x1C, 0x400)      // data0 offset
	put32(data, dol+0x64, 0x80004000) // data0 address (ps operands)
	put32(data, dol+0xAC, 24)
	put32(data, dol+0x20, 0x500) // data1 offset
	put32(data, dol+0x68, 0x80005000)
	put32(data, dol+0xB0, uint32(len(stream)))
	put32(data, dol+0xE0, 0x80001000) // entry

	// DOL section offsets are relative to the DOL header,
	// which sits at file offset 0x5000 in this image.
	for i, w := range prog {
		put32(data, 0x5100+uint32(4*i), w)
	}
	put32(data, 0x5400, math.Float32bits(1.5))
	put32(data, 0x5404, math.Float32bits(2.25))
	copy(data[0x5500:], stream)
	return data
}

func TestGXAndPairedSingleDeterminism(t *testing.T) {
	img, err := disc.Parse(gxImage())
	if err != nil {
		t.Fatal(err)
	}
	load := func() *Machine {
		m := New()
		if err := m.LoadDisc(img); err != nil {
			t.Fatal(err)
		}
		m.Start()
		return m
	}

	// Uninterrupted reference run.
	ref := load()
	ref.RunCycles(300)
	midState := ref.Serialize()
	ref.RunCycles(400)
	if got := ref.Read32(0x80004010); got != math.Float32bits(3.0) {
		t.Fatalf("ps_add ps0 result = %#x, want 3.0f bits", got)
	}
	if got := ref.Read32(0x80004014); got != math.Float32bits(4.5) {
		t.Fatalf("ps_add ps1 result = %#x, want 4.5f bits", got)
	}
	fbRef := append([]uint32(nil), ref.Framebuffer()...)
	if px := fbRef[240*Width+320]; px != 0xFFC81E3C {
		t.Fatalf("quad center pixel = %#08x, want ffc81e3c", px)
	}
	if px := fbRef[0]; px != 0xFF000000 {
		t.Fatalf("outside pixel = %#08x, want opaque black", px)
	}
	stateRef := ref.Serialize()

	// The mid-run state was taken while the FIFO copy was
	// partway through the draw command, so the GPU's
	// partial-command buffer is inside the state. A fresh
	// machine restored from it must continue identically.
	m2 := load()
	if err := m2.Deserialize(midState); err != nil {
		t.Fatal(err)
	}
	m2.RunCycles(400)
	if !bytes.Equal(m2.Serialize(), stateRef) {
		t.Fatal("continuation after restore diverged from the uninterrupted run")
	}
	fb2 := m2.Framebuffer()
	for i := range fbRef {
		if fb2[i] != fbRef[i] {
			t.Fatalf("framebuffer pixel %d differs after restore: %#08x vs %#08x", i, fb2[i], fbRef[i])
		}
	}
}

func TestLoadWiiDiscFails(t *testing.T) {
	data := make([]byte, 0x50000)
	put32(data, 0x18, disc.WiiMagic)
	img, err := disc.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.LoadDisc(img); err != disc.ErrWiiDisc {
		t.Fatalf("LoadDisc(Wii) err = %v", err)
	}
}
