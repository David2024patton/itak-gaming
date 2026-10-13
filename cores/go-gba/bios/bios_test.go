package bios

import (
	"encoding/binary"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-gba/bus"
	"github.com/davidpatton/itak-gaming/cores/go-gba/chips/arm7tdmi"
)

// rig bundles a bus with the BIOS installed, a handler and
// a CPU whose pipeline is parked as if a SWI at swiAt is
// about to be answered by a direct Hook call.
type rig struct {
	b *bus.Bus
	h *Handler
	c *arm7tdmi.CPU
}

const swiAt = 0x08000100

func newRig(t *testing.T) *rig {
	t.Helper()
	b := bus.New()
	Install(b.BIOS[:])
	// A zero filled ROM gives Jump targets harmless words
	// (zero decodes to a condition failed AND) to refill
	// the pipeline with, and a home for hand built data.
	b.SetROM(make([]byte, 0x8000))
	h := New(b)
	c := arm7tdmi.New(b)
	c.SWIHook = h.Hook
	return &rig{b: b, h: h, c: c}
}

// callSWI invokes the hook directly with the CPU set up as
// if executing a SWI at swiAt in ARM state: R15 is the SWI
// address plus 8. It reports the hook result.
func (r *rig) callSWI(num uint32) (int, bool) {
	r.c.CPSR &^= arm7tdmi.BitT // ARM state
	r.c.R[15] = swiAt + 8
	return r.h.Hook(r.c, num)
}

// branchTarget decodes an ARM branch word at addr.
func branchTarget(addr, word uint32) (uint32, bool) {
	if word>>24 != 0xEA {
		return 0, false
	}
	off := int32(word<<8) >> 8
	return uint32(int32(addr+8) + off<<2), true
}

func TestInstallVectors(t *testing.T) {
	store := make([]byte, bus.BIOSSize)
	Install(store)
	cases := []struct {
		off  uint32
		want uint32
	}{
		{0x00, bootAddr},
		{0x18, dispatcherAddr},
		{0x08, returnStubAddr},
	}
	for _, tc := range cases {
		word := binary.LittleEndian.Uint32(store[tc.off:])
		got, ok := branchTarget(tc.off, word)
		if !ok || got != tc.want {
			t.Errorf("vector %#x: word %#08x decodes to %#x (ok %v), want target %#x", tc.off, word, got, ok, tc.want)
		}
	}
	if w := binary.LittleEndian.Uint32(store[returnStubAddr:]); w != returnStub {
		t.Errorf("return stub word %#08x, want %#08x", w, returnStub)
	}
}

func TestBootStubExecution(t *testing.T) {
	b := bus.New()
	Install(b.BIOS[:])
	// A sentinel in the check word proves the stub clears it.
	b.IWRAM[0x7FF8] = 0xFF
	b.IWRAM[0x7FF9] = 0xFF
	b.IWRAM[0x7FFA] = 0xFF
	b.IWRAM[0x7FFB] = 0xFF
	// Cartridge entry: an infinite branch to itself.
	rom := make([]byte, 0x200)
	binary.LittleEndian.PutUint32(rom, 0xEAFFFFFE)
	b.SetROM(rom)

	c := arm7tdmi.New(b)
	c.Reset()
	for i := 0; i < 40; i++ {
		c.Step()
	}
	if got := c.R[15] - 8; got != 0x08000000 {
		t.Errorf("executing at %#x, want cartridge entry 0x08000000", got)
	}
	if c.Executing != 0xEAFFFFFE {
		t.Errorf("executing word %#08x, want the cartridge infinite branch", c.Executing)
	}
	if mode := c.Mode(); mode != arm7tdmi.ModeSYS {
		t.Errorf("mode %#x, want SYS (%#x)", mode, arm7tdmi.ModeSYS)
	}
	if c.R[13] != UserStackTop {
		t.Errorf("SYS stack pointer %#x, want %#x", c.R[13], UserStackTop)
	}
	if c.RIRQ[0] != IRQStackTop {
		t.Errorf("IRQ stack pointer %#x, want %#x", c.RIRQ[0], IRQStackTop)
	}
	if c.RSVC[0] != SVCStackTop {
		t.Errorf("SVC stack pointer %#x, want %#x", c.RSVC[0], SVCStackTop)
	}
	if v := b.Read(CheckWordAddr, 4); v != 0 {
		t.Errorf("check word %#x, want 0", v)
	}
	if c.CPSR&arm7tdmi.BitI != 0 {
		t.Error("IRQ should be enabled after boot (I bit clear)")
	}
	if c.CPSR&arm7tdmi.BitF == 0 {
		t.Error("FIQ should be masked after boot (F bit set)")
	}
}

// TestDispatcherExecution runs the installed dispatcher on
// the real CPU: IRQ mode, one pending vblank flag, and a
// user handler that is a bare BX LR stub in EWRAM.
func TestDispatcherExecution(t *testing.T) {
	b := bus.New()
	Install(b.BIOS[:])
	b.SetROM(make([]byte, 0x8000))

	// Handler stub in EWRAM: BX LR.
	b.Write(0x02000100, 4, 0xE12FFF1E)
	// Return target in the ROM window: branch to itself.
	binary.LittleEndian.PutUint32(b.ROM[0x200:], 0xEAFFFFFE)

	b.Write(0x04000200, 2, 1) // IE: vblank
	b.Write(0x04000202, 2, 1) // IF: vblank pending
	b.Write(HandlerPtrAddr, 4, 0x02000100)

	c := arm7tdmi.New(b)
	// Enter IRQ mode by hand for the test: the bank swap
	// helpers are private, so set the fields the dispatcher
	// actually touches (current SP and LR, the IRQ SPSR it
	// returns through) directly.
	c.CPSR = (c.CPSR &^ 0x1F) | arm7tdmi.ModeIRQ
	c.R[13] = IRQStackTop
	c.R[14] = 0x08000200 + 4 // SUBS PC, LR, #4 lands at 0x08000200
	c.SPSR[arm7tdmi.SPSRIRQ] = arm7tdmi.ModeSYS
	c.R[0] = 0xAABBCCDD // sentinel the dispatcher must preserve
	c.Jump(dispatcherAddr)
	for i := 0; i < 40; i++ {
		c.Step()
	}
	if v := b.Read(CheckWordAddr, 4); v != 1 {
		t.Errorf("check word %#x, want 1 (vblank ORed in by the dispatcher)", v)
	}
	if got := c.R[15] - 8; got != 0x08000200 {
		t.Errorf("executing at %#x, want the return target 0x08000200", got)
	}
	if c.RIRQ[0] != IRQStackTop {
		t.Errorf("IRQ stack pointer after return %#x, want balanced %#x", c.RIRQ[0], IRQStackTop)
	}
	if c.R[0] != 0xAABBCCDD {
		t.Errorf("R0 %#x, want preserved sentinel 0xAABBCCDD", c.R[0])
	}
}

func TestDiv(t *testing.T) {
	r := newRig(t)
	cases := []struct {
		num, den    int32
		q, rem, abs int32
	}{
		{100, 7, 14, 2, 14},
		{-100, 7, -14, -2, 14},
		{100, -7, -14, 2, 14},
		{-100, -7, 14, -2, 14},
		{7, 0, 1, 7, 1}, // documented divide by zero behaviour
		{-7, 0, -1, -7, 1},
	}
	for _, tc := range cases {
		r.c.R[0] = uint32(tc.num)
		r.c.R[1] = uint32(tc.den)
		if _, ok := r.callSWI(0x06); !ok {
			t.Fatalf("Div(%d, %d) not handled", tc.num, tc.den)
		}
		if int32(r.c.R[0]) != tc.q || int32(r.c.R[1]) != tc.rem || int32(r.c.R[3]) != tc.abs {
			t.Errorf("Div(%d, %d) = q %d r %d abs %d, want q %d r %d abs %d",
				tc.num, tc.den, int32(r.c.R[0]), int32(r.c.R[1]), int32(r.c.R[3]), tc.q, tc.rem, tc.abs)
		}
		// DivArm takes the inputs swapped and must agree.
		r.c.R[0] = uint32(tc.den)
		r.c.R[1] = uint32(tc.num)
		if _, ok := r.callSWI(0x07); !ok {
			t.Fatalf("DivArm(%d, %d) not handled", tc.den, tc.num)
		}
		if int32(r.c.R[0]) != tc.q || int32(r.c.R[1]) != tc.rem || int32(r.c.R[3]) != tc.abs {
			t.Errorf("DivArm swapped (%d, %d) = q %d r %d abs %d, want q %d r %d abs %d",
				tc.num, tc.den, int32(r.c.R[0]), int32(r.c.R[1]), int32(r.c.R[3]), tc.q, tc.rem, tc.abs)
		}
	}
}

func TestSqrt(t *testing.T) {
	r := newRig(t)
	cases := []struct{ in, want uint32 }{
		{0, 0}, {1, 1}, {2, 1}, {144, 12}, {0xFFFFFFFF, 65535}, {0xFFFE0001, 65535},
	}
	for _, tc := range cases {
		r.c.R[0] = tc.in
		if _, ok := r.callSWI(0x08); !ok {
			t.Fatalf("Sqrt(%#x) not handled", tc.in)
		}
		if r.c.R[0] != tc.want {
			t.Errorf("Sqrt(%#x) = %d, want %d", tc.in, r.c.R[0], tc.want)
		}
	}
}

func TestArcTan(t *testing.T) {
	r := newRig(t)
	// tan 0 -> angle 0; tan 1.0 (0x4000 in 1.14) -> one
	// eighth of a turn, 0x2000.
	r.c.R[0] = 0
	r.callSWI(0x09)
	if r.c.R[0] != 0 {
		t.Errorf("ArcTan(0) = %#x, want 0", r.c.R[0])
	}
	r.c.R[0] = 0x4000
	r.callSWI(0x09)
	if r.c.R[0] != 0x2000 {
		t.Errorf("ArcTan(0x4000) = %#x, want 0x2000", r.c.R[0])
	}
	// ArcTan2: (x=1.0, y=0) -> 0; (x=0, y=1.0) -> a quarter
	// turn, 0x4000.
	r.c.R[0], r.c.R[1] = 0x4000, 0
	r.callSWI(0x0A)
	if r.c.R[0] != 0 {
		t.Errorf("ArcTan2(1, 0) = %#x, want 0", r.c.R[0])
	}
	r.c.R[0], r.c.R[1] = 0, 0x4000
	r.callSWI(0x0A)
	if r.c.R[0] != 0x4000 {
		t.Errorf("ArcTan2(0, 1) = %#x, want 0x4000", r.c.R[0])
	}
}

func TestGetBiosChecksum(t *testing.T) {
	r := newRig(t)
	r.callSWI(0x0D)
	if r.c.R[0] != 0xBAAE187F {
		t.Errorf("checksum %#08x, want 0xBAAE187F", r.c.R[0])
	}
}

func TestUnknownSWI(t *testing.T) {
	r := newRig(t)
	if _, ok := r.callSWI(0x77); ok {
		t.Error("unknown SWI 0x77 should be unhandled so the CPU takes the exception")
	}
	// The encoded ARM form (number in bits 16 to 23) must
	// decode to the same function.
	r.c.R[0], r.c.R[1] = 100, 7
	r.c.R[15] = swiAt + 8
	if _, ok := r.h.Hook(r.c, 0x06<<16); !ok {
		t.Fatal("encoded SWI 0x060000 not handled")
	}
	if r.c.R[0] != 14 {
		t.Errorf("encoded Div quotient %d, want 14", r.c.R[0])
	}
}

func TestSoftResetFlagZero(t *testing.T) {
	r := newRig(t)
	// Flag byte at 0x03007FFA is zero: restart goes through
	// the BIOS reset entry, so the pipeline refills at 0.
	r.callSWI(0x00)
	if r.c.R[15] != 8 {
		t.Errorf("R15 after SoftReset %#x, want 8 (refill at the reset vector plus 8)", r.c.R[15])
	}
	if r.c.Executing != binary.LittleEndian.Uint32(r.b.BIOS[0:]) {
		t.Error("executing word after SoftReset is not the reset vector branch")
	}
}

func TestSoftResetFlagSet(t *testing.T) {
	r := newRig(t)
	r.b.Write(0x03007FFA, 1, 1)              // restart flag
	r.b.Write(HandlerPtrAddr, 4, 0x08000401) // stored target, thumb bit set
	r.callSWI(0x00)
	// Target used with the low bit cleared: refill at
	// 0x08000400 in ARM state gives R15 = target + 8.
	if r.c.R[15] != 0x08000400+8 {
		t.Errorf("R15 after flagged SoftReset %#x, want %#x", r.c.R[15], 0x08000408)
	}
}

func TestRegisterRamReset(t *testing.T) {
	r := newRig(t)
	fill := func(addr uint32, size int) {
		for i := 0; i < size; i += 4 {
			r.b.Write(addr+uint32(i), 4, 0xAAAAAAAA)
		}
	}
	fill(0x02000000, 0x100) // EWRAM
	fill(0x03000000, 0x100) // IWRAM low
	fill(0x05000000, 0x100) // palette
	fill(0x06000000, 0x100) // VRAM
	fill(0x07000000, 0x100) // OAM
	// Sentinels in the preserved IWRAM system area.
	r.b.Write(0x03007F00, 4, 0x12345678)
	r.b.Write(0x03007FF8, 4, 0x9ABCDEF0)

	r.c.R[0] = 0x1F // bits 0 to 4: the five memory areas
	r.callSWI(0x01)
	for _, addr := range []uint32{0x02000000, 0x03000000, 0x05000000, 0x06000000, 0x07000000} {
		if v := r.b.Read(addr, 4); v != 0 {
			t.Errorf("region at %#x still %#08x, want cleared", addr, v)
		}
	}
	if v := r.b.Read(0x03007F00, 4); v != 0x12345678 {
		t.Errorf("system area at 0x03007F00 = %#08x, want preserved sentinel", v)
	}
	if v := r.b.Read(0x03007FF8, 4); v != 0x9ABCDEF0 {
		t.Errorf("check word = %#08x, want preserved sentinel", v)
	}
}

func TestCpuSet(t *testing.T) {
	r := newRig(t)
	src, dst := uint32(0x02000000), uint32(0x02000100)
	for i, v := range []uint16{0x1111, 0x2222, 0x3333} {
		r.b.Write(src+uint32(2*i), 2, uint32(v))
	}
	// 16 bit copy of 3 units.
	r.c.R[0], r.c.R[1], r.c.R[2] = src, dst, 3
	r.callSWI(0x0B)
	for i, want := range []uint32{0x1111, 0x2222, 0x3333} {
		if v := r.b.Read(dst+uint32(2*i), 2); v != want {
			t.Errorf("copied half %d = %#x, want %#x", i, v, want)
		}
	}
	// 32 bit fill of 4 units from one source word.
	r.b.Write(0x02000200, 4, 0xDEADBEEF)
	r.c.R[0], r.c.R[1] = 0x02000200, 0x02000300
	r.c.R[2] = 4 | (1 << 24) | (1 << 26)
	r.callSWI(0x0B)
	for i := 0; i < 4; i++ {
		if v := r.b.Read(0x02000300+uint32(4*i), 4); v != 0xDEADBEEF {
			t.Errorf("filled word %d = %#08x, want 0xDEADBEEF", i, v)
		}
	}
}

func TestCpuFastSetRounding(t *testing.T) {
	r := newRig(t)
	src, dst := uint32(0x02000000), uint32(0x02000400)
	for i := 0; i < 8; i++ {
		r.b.Write(src+uint32(4*i), 4, 0x1000+uint32(i))
	}
	// Count 3 must round up to 8 units of 32 bits.
	r.c.R[0], r.c.R[1], r.c.R[2] = src, dst, 3
	r.callSWI(0x0C)
	for i := 0; i < 8; i++ {
		if v := r.b.Read(dst+uint32(4*i), 4); v != 0x1000+uint32(i) {
			t.Errorf("fast copied word %d = %#x, want %#x (count rounds to 8)", i, v, 0x1000+i)
		}
	}
}

// buildAffineSrc packs one BgAffineSet source entry using
// the documented 20 byte layout (see affine.go).
func buildAffineSrc(r *rig, at uint32, originX, originY int32, centerX, centerY, ratioX, ratioY int16, angle uint16) {
	r.b.Write(at+0, 4, uint32(originX))
	r.b.Write(at+4, 4, uint32(originY))
	r.b.Write(at+8, 2, uint32(centerX))
	r.b.Write(at+10, 2, uint32(centerY))
	r.b.Write(at+12, 2, uint32(ratioX))
	r.b.Write(at+14, 2, uint32(ratioY))
	r.b.Write(at+16, 2, uint32(angle))
}

func TestBgAffineSetIdentity(t *testing.T) {
	r := newRig(t)
	src, dst := uint32(0x02000000), uint32(0x02000100)
	// Identity: angle 0, ratios 1.0 (0x100). By the
	// formulas: pa = pd = 0x100, pb = pc = 0, and
	// dx = originX - centerX, dy = originY - centerY
	// (the >>8 cancels the 0x100 scale exactly).
	buildAffineSrc(r, src, 0x1000, 0x2000, 10, 20, 0x100, 0x100, 0)
	r.c.R[0], r.c.R[1], r.c.R[2] = src, dst, 1
	r.callSWI(0x0E)
	checks := []struct {
		off  uint32
		size int
		want int32
	}{
		{0, 2, 0x100}, {2, 2, 0}, {4, 2, 0}, {6, 2, 0x100},
		{8, 4, 0x1000 - 10}, {12, 4, 0x2000 - 20},
	}
	for _, ch := range checks {
		got := r.b.Read(dst+ch.off, ch.size)
		if int32(got) != ch.want {
			if ch.size == 2 {
				if int32(int16(got)) == ch.want {
					continue
				}
			}
			t.Errorf("dest +%d = %#x, want %#x", ch.off, got, ch.want)
		}
	}
}

func TestBgAffineSetQuarterTurn(t *testing.T) {
	r := newRig(t)
	src, dst := uint32(0x02000000), uint32(0x02000100)
	// Angle 0x4000 is a quarter turn: cos 0, sin 1. With
	// ratios 0x100: pa = 0, pb = -0x100, pc = 0x100,
	// pd = 0. Then
	// dx = originX - ((0*10 + -0x100*20) >> 8)
	//    = 0x1000 - (-20) = 0x1014
	// dy = originY - ((0x100*10 + 0*20) >> 8)
	//    = 0x2000 - 10 = 0x1FF6
	buildAffineSrc(r, src, 0x1000, 0x2000, 10, 20, 0x100, 0x100, 0x4000)
	r.c.R[0], r.c.R[1], r.c.R[2] = src, dst, 1
	r.callSWI(0x0E)
	if v := int16(r.b.Read(dst+0, 2)); v != 0 {
		t.Errorf("pa = %d, want 0", v)
	}
	if v := int16(r.b.Read(dst+2, 2)); v != -0x100 {
		t.Errorf("pb = %d, want -256", v)
	}
	if v := int16(r.b.Read(dst+4, 2)); v != 0x100 {
		t.Errorf("pc = %d, want 256", v)
	}
	if v := int16(r.b.Read(dst+6, 2)); v != 0 {
		t.Errorf("pd = %d, want 0", v)
	}
	if v := int32(r.b.Read(dst+8, 4)); v != 0x1014 {
		t.Errorf("dx = %#x, want 0x1014", v)
	}
	if v := int32(r.b.Read(dst+12, 4)); v != 0x1FF6 {
		t.Errorf("dy = %#x, want 0x1FF6", v)
	}
}

func TestObjAffineSet(t *testing.T) {
	r := newRig(t)
	src, dst := uint32(0x02000000), uint32(0x07000000)
	// Entry: ratios 1.0, angle 0 -> identity matrix.
	r.b.Write(src+0, 2, 0x100)
	r.b.Write(src+2, 2, 0x100)
	r.b.Write(src+4, 2, 0)
	r.c.R[0], r.c.R[1], r.c.R[2] = src, dst, 1
	r.callSWI(0x0F)
	for _, tc := range []struct {
		off  uint32
		want uint32
	}{
		{0x06, 0x100}, {0x0E, 0}, {0x16, 0}, {0x1E, 0x100},
	} {
		if v := r.b.Read(dst+tc.off, 2); v != tc.want {
			t.Errorf("OAM +%#x = %#x, want %#x", tc.off, v, tc.want)
		}
	}
}

func TestBitUnpack(t *testing.T) {
	r := newRig(t)
	src, dst, params := uint32(0x02000000), uint32(0x02000100), uint32(0x02000200)
	// Source byte 0b11100100 holds four 2 bit units LSB
	// first: 0, 1, 2, 3. Widened to 8 bits with an offset
	// of 0x10 on nonzero units: 0, 0x11, 0x12, 0x13.
	r.b.Write(src, 1, 0xE4)
	r.b.Write(params+0, 2, 1)    // source length: 1 byte
	r.b.Write(params+2, 1, 2)    // source width 2
	r.b.Write(params+3, 1, 8)    // dest width 8
	r.b.Write(params+4, 4, 0x10) // offset, add-to-all flag clear
	r.c.R[0], r.c.R[1], r.c.R[2] = src, dst, params
	r.callSWI(0x10)
	for i, want := range []uint32{0, 0x11, 0x12, 0x13} {
		if v := r.b.Read(dst+uint32(i), 1); v != want {
			t.Errorf("unpacked byte %d = %#x, want %#x", i, v, want)
		}
	}
}

func TestLZ77(t *testing.T) {
	r := newRig(t)
	src, dst := uint32(0x02000000), uint32(0x02000100)
	// Payload "ABCABCABC" (9 bytes): three literals then
	// one back reference of length 6 at displacement 3.
	// Flag byte bits (MSB first): 0,0,0,1 then unused 0s
	// gives 0x10. Reference bytes: length 6 -> high
	// nibble 6-3 = 3; displacement 3 -> disp-1 = 2, so
	// b0 = 0x30, b1 = 0x02.
	r.b.Write(src, 4, 9<<8|0x10) // header
	stream := []byte{0x10, 'A', 'B', 'C', 0x30, 0x02}
	for i, v := range stream {
		r.b.Write(src+4+uint32(i), 1, uint32(v))
	}
	r.c.R[0], r.c.R[1] = src, dst
	r.callSWI(0x11)
	for i, want := range []byte("ABCABCABC") {
		if v := r.b.Read(dst+uint32(i), 1); v != uint32(want) {
			t.Errorf("byte %d = %#x, want %#x", i, v, want)
		}
	}
}

func TestRLUnComp(t *testing.T) {
	r := newRig(t)
	src, dst := uint32(0x02000000), uint32(0x02000100)
	// Payload "AAAAAXYZ" (8 bytes): a run of five 'A'
	// (flag 0x80 | (5-3) = 0x82, then the byte), then a
	// literal run of three (flag 3-1 = 0x02, then bytes).
	r.b.Write(src, 4, 8<<8|0x30)
	stream := []byte{0x82, 'A', 0x02, 'X', 'Y', 'Z'}
	for i, v := range stream {
		r.b.Write(src+4+uint32(i), 1, uint32(v))
	}
	r.c.R[0], r.c.R[1] = src, dst
	r.callSWI(0x14)
	for i, want := range []byte("AAAAAXYZ") {
		if v := r.b.Read(dst+uint32(i), 1); v != uint32(want) {
			t.Errorf("byte %d = %#x, want %#x", i, v, want)
		}
	}
}

func TestDiff8(t *testing.T) {
	r := newRig(t)
	src, dst := uint32(0x02000000), uint32(0x02000100)
	// Output [0x10, 0x15, 0x0F, 0x20]: first byte literal
	// 0x10, deltas +0x05, -6 (0xFA), +0x11.
	r.b.Write(src, 4, 4<<8|0x80)
	for i, v := range []byte{0x10, 0x05, 0xFA, 0x11} {
		r.b.Write(src+4+uint32(i), 1, uint32(v))
	}
	r.c.R[0], r.c.R[1] = src, dst
	r.callSWI(0x16)
	for i, want := range []byte{0x10, 0x15, 0x0F, 0x20} {
		if v := r.b.Read(dst+uint32(i), 1); v != uint32(want) {
			t.Errorf("byte %d = %#x, want %#x", i, v, want)
		}
	}
}

func TestDiff16(t *testing.T) {
	r := newRig(t)
	src, dst := uint32(0x02000000), uint32(0x02000100)
	// Output [0x1000, 0x1005, 0x0FF0]: first halfword
	// literal, deltas +0x0005 and -0x15 (0xFFEB). Size is
	// 6 bytes (3 halfwords), header type byte 0x81.
	r.b.Write(src, 4, 6<<8|0x81)
	for i, v := range []uint32{0x1000, 0x0005, 0xFFEB} {
		r.b.Write(src+4+uint32(2*i), 2, v)
	}
	r.c.R[0], r.c.R[1] = src, dst
	r.callSWI(0x18)
	for i, want := range []uint32{0x1000, 0x1005, 0x0FF0} {
		if v := r.b.Read(dst+uint32(2*i), 2); v != want {
			t.Errorf("halfword %d = %#x, want %#x", i, v, want)
		}
	}
}

func TestHuffUnComp(t *testing.T) {
	r := newRig(t)
	src, dst := uint32(0x02000000), uint32(0x02000100)
	// Hand built tree and stream for the output "ABA"
	// with 8 bit symbols.
	//
	// Tree block (4 bytes, so the size byte N satisfies
	// (N+1)*2 = 4, N = 1):
	//   byte 0: 0x01, the size byte
	//   byte 1: 0xC0, the root node: offset 0, bit 6 set
	//           (left child is data), bit 7 set (right
	//           child is data). The child pair sits at
	//           (1 AND NOT 1) + 0*2 + 2 = 2, so the left
	//           child is byte 2 and the right is byte 3.
	//   byte 2: 'A' (the left symbol)
	//   byte 3: 'B' (the right symbol)
	// Stream bits walk 0 for A and 1 for B, MSB first in
	// a 32 bit word: A B A is bits 0, 1, 0 from the top,
	// the word 0x40000000.
	r.b.Write(src, 4, 3<<8|0x28) // header: type 0x20 | 8, size 3
	tree := []byte{0x01, 0xC0, 'A', 'B'}
	for i, v := range tree {
		r.b.Write(src+4+uint32(i), 1, uint32(v))
	}
	r.b.Write(src+8, 4, 0x40000000) // stream word
	r.c.R[0], r.c.R[1] = src, dst
	r.callSWI(0x13)
	for i, want := range []byte("ABA") {
		if v := r.b.Read(dst+uint32(i), 1); v != uint32(want) {
			t.Errorf("byte %d = %#x, want %#x", i, v, want)
		}
	}
}

func TestHaltAndPoll(t *testing.T) {
	r := newRig(t)
	r.callSWI(0x02)
	if !r.c.Halted || !r.h.Waiting() {
		t.Fatal("Halt should halt the CPU and mark the handler waiting")
	}
	// No IF flags: Poll must not wake.
	r.h.Poll(r.c)
	if !r.c.Halted {
		t.Fatal("Poll with no pending IF woke the CPU early")
	}
	r.b.Write(0x04000202, 2, 1) // IF: vblank
	r.h.Poll(r.c)
	if r.c.Halted || r.h.Waiting() {
		t.Fatal("Poll with a pending IF should wake the CPU")
	}
	if want := uint32(swiAt + 4 + 8); r.c.R[15] != want {
		t.Errorf("R15 after wake %#x, want %#x (return address plus pipeline 8)", r.c.R[15], want)
	}
}

func TestIntrWaitImmediateReturn(t *testing.T) {
	r := newRig(t)
	// Check word already holds bit 2, plus an unrelated
	// bit 3 that must survive.
	r.b.Write(CheckWordAddr, 4, (1<<2)|(1<<3))
	r.c.R[0] = 0      // discard flag clear
	r.c.R[1] = 1 << 2 // mask
	r.callSWI(0x04)
	if r.c.Halted || r.h.Waiting() {
		t.Fatal("IntrWait with a pending masked bit should return immediately")
	}
	if v := r.b.Read(CheckWordAddr, 4); v != 1<<3 {
		t.Errorf("check word %#x, want %#x (waited bit consumed, other bit kept)", v, 1<<3)
	}
}

func TestIntrWaitBlocking(t *testing.T) {
	r := newRig(t)
	r.b.Write(CheckWordAddr, 4, 0)
	r.c.R[0] = 1 // discard
	r.c.R[1] = 1 << 1
	r.callSWI(0x04)
	if !r.c.Halted || !r.h.Waiting() {
		t.Fatal("IntrWait with a clear check word should block")
	}
	r.b.Write(CheckWordAddr, 4, 1<<1) // dispatcher sets the flag
	r.h.Poll(r.c)
	if r.c.Halted || r.h.Waiting() {
		t.Fatal("Poll should wake the IntrWait once its bit lands")
	}
	if v := r.b.Read(CheckWordAddr, 4); v != 0 {
		t.Errorf("check word %#x, want 0 (bit consumed on wake)", v)
	}
	if want := uint32(swiAt + 4 + 8); r.c.R[15] != want {
		t.Errorf("R15 after wake %#x, want %#x", r.c.R[15], want)
	}
	// The wake refilled the pipeline at the return
	// address: the CPU is executing there now (checked via
	// R15 above), and one step advances it to the next
	// instruction, proving the return address executed.
	r.c.Step()
	if got := r.c.R[15] - 8; got != swiAt+4+4 {
		t.Errorf("after one step, executing at %#x, want %#x (one past the return address)", got, swiAt+8)
	}
}

func TestVBlankIntrWaitDiscardsStaleBit(t *testing.T) {
	r := newRig(t)
	// A stale vblank bit is already in the check word.
	// VBlankIntrWait discards it and waits for the next one.
	r.b.Write(CheckWordAddr, 4, 1)
	r.callSWI(0x05)
	if !r.c.Halted || !r.h.Waiting() {
		t.Fatal("VBlankIntrWait should block even with a stale vblank bit present")
	}
	if v := r.b.Read(CheckWordAddr, 4); v != 0 {
		t.Errorf("check word %#x, want 0 (stale bit discarded)", v)
	}
	r.b.Write(CheckWordAddr, 4, 1) // the next vblank lands
	r.h.Poll(r.c)
	if r.c.Halted {
		t.Fatal("Poll should wake VBlankIntrWait on the next vblank bit")
	}
}

func TestHandlerSerializeRoundTrip(t *testing.T) {
	r := newRig(t)
	r.c.R[0] = 1
	r.c.R[1] = 0x55
	r.callSWI(0x04) // blocks: waiting state is live
	buf := make([]byte, HandlerStateSize)
	if n := r.h.Serialize(buf); n != HandlerStateSize {
		t.Fatalf("Serialize wrote %d bytes, want %d", n, HandlerStateSize)
	}
	h2 := New(r.b)
	if !h2.Deserialize(buf) {
		t.Fatal("Deserialize rejected a valid state")
	}
	if !h2.Waiting() {
		t.Fatal("restored handler should still be waiting")
	}
	// The restored handler wakes on its mask like the original.
	r.b.Write(CheckWordAddr, 4, 0x55)
	h2.Poll(r.c)
	if r.c.Halted {
		t.Fatal("restored handler failed to wake the CPU")
	}
	if h2.Deserialize(make([]byte, 4)) {
		t.Fatal("Deserialize should reject a short buffer")
	}
}
