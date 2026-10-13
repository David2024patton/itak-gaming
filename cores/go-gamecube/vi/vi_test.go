package vi

import "testing"

// read32 assembles a 32-bit register from its two lanes,
// high lane at the lower offset.
func read32(v *VI, off uint32) uint32 {
	return uint32(v.Read16(off))<<16 | uint32(v.Read16(off+2))
}

// write32 stores a 32-bit register as two lanes. For the
// display interrupt registers the caller keeps the DIS
// bit (bit 16) explicit in val, following the lane rules
// in Write16.
func write32(v *VI, off uint32, val uint32) {
	v.Write16(off, uint16(val>>16))
	v.Write16(off+2, uint16(val))
}

// programDI0 sets display interrupt 0 to target with the
// enable bit set and the status bit clear.
func programDI0(v *VI, target uint32) {
	write32(v, regVIDI0, dilDIE|(target&dilTargetMask))
}

// clearDI0 clears the DIS status of interrupt 0 by
// writing its high lane with bit 16 clear, enable kept.
func clearDI0(v *VI) {
	v.Write16(regVIDI0, dilDIELaneBit)
}

func TestResetDefaults(t *testing.T) {
	v := New()
	if got := v.Read16(regVITV); got != (6<<10)|240 {
		t.Errorf("VITV = %#04x, want %#04x", got, (6<<10)|240)
	}
	if got := v.Read16(regVIDCR); got != 1 {
		t.Errorf("VIDCR = %#04x, want 0001 (ENB, interlaced)", got)
	}
	for _, off := range []uint32{regVIVTO, regVIVTE} {
		if got := v.Read16(off); got != (15<<10)|3 {
			t.Errorf("reg %#02x = %#04x, want %#04x", off, got, (15<<10)|3)
		}
	}
	for _, off := range []uint32{regVIHTR0, regVIHTR1, regVIDI0, regVIDIL0, regVIPICCONF, regVITFBL} {
		if got := read32(v, off); got != 0 {
			t.Errorf("reg %#02x = %#08x, want 0 after reset", off, got)
		}
	}
	if got := v.Read16(regVIHSR); got != 0 {
		t.Errorf("VIHSR = %#04x, want 0 after reset", got)
	}
	if v.Pending() {
		t.Error("Pending after reset, want false")
	}
	if got := v.CurrentHalfline(); got != 0 {
		t.Errorf("CurrentHalfline after reset = %d, want 0", got)
	}
}

func TestStoredRegisters(t *testing.T) {
	v := New()
	// 32-bit stored registers round trip lane by lane.
	write32(v, regVIHTR0, 0xABCD1234)
	write32(v, regVIHTR1, 0x0BADF00D)
	write32(v, regVIDIL0, 0x11112222)
	write32(v, regVIDIL1, 0x33334444)
	write32(v, regVIPICCONF, 0x55556666)
	write32(v, regVITFBL, 0x80000000)
	write32(v, regVITFBR, 0x80001000)
	write32(v, regVIBFBL, 0x80002000)
	write32(v, regVIBFBR, 0x80003000)
	for off, want := range map[uint32]uint32{
		regVIHTR0: 0xABCD1234, regVIHTR1: 0x0BADF00D,
		regVIDIL0: 0x11112222, regVIDIL1: 0x33334444,
		regVIPICCONF: 0x55556666, regVITFBL: 0x80000000,
		regVITFBR: 0x80001000, regVIBFBL: 0x80002000,
		regVIBFBR: 0x80003000,
	} {
		if got := read32(v, off); got != want {
			t.Errorf("reg %#02x = %#08x, want %#08x", off, got, want)
		}
	}
	// 16-bit stored registers: HSR and all 12 filter
	// coefficient words at 0x34 through 0x4A.
	v.Write16(regVIHSR, 0xBEEF)
	if got := v.Read16(regVIHSR); got != 0xBEEF {
		t.Errorf("VIHSR = %#04x, want beef", got)
	}
	for i := 0; i < 12; i++ {
		off := uint32(regFilter0 + 2*i)
		v.Write16(off, uint16(0x1000+i))
	}
	for i := 0; i < 12; i++ {
		off := uint32(regFilter0 + 2*i)
		if got := v.Read16(off); got != uint16(0x1000+i) {
			t.Errorf("filter word %d at %#02x = %#04x, want %#04x", i, off, got, 0x1000+i)
		}
	}
	// VIDCR fields are stored, including FMT.
	v.Write16(regVIDCR, 1|(1<<2)|(2<<8))
	if got := v.Read16(regVIDCR); got != 1|(1<<2)|(2<<8) {
		t.Errorf("VIDCR = %#04x, want %#04x", got, 1|(1<<2)|(2<<8))
	}
	// Out of window offsets read zero and ignore writes.
	v.Write16(0x80, 0xFFFF)
	if got := v.Read16(0x80); got != 0 {
		t.Errorf("out of window read = %#04x, want 0", got)
	}
}

func TestDisplayInterruptFiresAndClears(t *testing.T) {
	v := New()
	programDI0(v, 4)
	if v.Pending() {
		t.Fatal("Pending before any advance, want false")
	}
	// Half a frame is well past halfline 4.
	v.Advance(CyclesPerFrame / 2)
	if !v.Pending() {
		t.Fatal("Pending after crossing target, want true")
	}
	if got := read32(v, regVIDI0); got&dilDIS == 0 {
		t.Errorf("VIDI0 = %#08x, want DIS (bit 16) set", got)
	}
	// Writing the register with bit 16 set leaves DIS set.
	v.Write16(regVIDI0, dilDIELaneBit|dilDISLaneBit)
	if !v.Pending() {
		t.Fatal("write with bit 16 set cleared DIS, want it left set")
	}
	// Writing with bit 16 clear clears DIS; DIE survives.
	clearDI0(v)
	if v.Pending() {
		t.Fatal("Pending after clear write, want false")
	}
	if got := read32(v, regVIDI0); got&dilDIE == 0 || got&dilDIS != 0 {
		t.Errorf("VIDI0 = %#08x, want DIE set and DIS clear", got)
	}
	// Advance to just before the target point of the next
	// frame: finish this frame, then step to halfline 2.
	v.Advance(CyclesPerFrame / 2) // exactly the frame boundary
	v.Advance(20000)              // halfline 2 of the new frame
	if v.Pending() {
		t.Fatal("Pending just before target next frame, want false")
	}
	// Cross the target again: the interrupt fires again.
	v.Advance(20000) // halfline 5
	if !v.Pending() {
		t.Fatal("Pending after crossing target next frame, want true")
	}
}

func TestDisplayInterruptDisabledNeverFires(t *testing.T) {
	v := New()
	write32(v, regVIDI1, 4) // target set, DIE clear
	v.Advance(CyclesPerFrame)
	if v.Pending() {
		t.Fatal("Pending with DIE clear, want false")
	}
	if got := read32(v, regVIDI1); got&dilDIS != 0 {
		t.Errorf("VIDI1 = %#08x, want DIS clear when DIE is clear", got)
	}
}

func TestDisplayInterruptOncePerFrame(t *testing.T) {
	v := New()
	programDI0(v, 4)
	fires := 0
	for frame := 0; frame < 3; frame++ {
		v.Advance(CyclesPerFrame)
		if v.Pending() {
			fires++
			clearDI0(v)
		}
	}
	if fires != 3 {
		t.Errorf("firings over 3 frames = %d, want 3", fires)
	}
}

func TestCurrentHalflineMonotonicAndWraps(t *testing.T) {
	v := New()
	prev := v.CurrentHalfline()
	if prev != 0 {
		t.Fatalf("initial halfline = %d, want 0", prev)
	}
	for i := 0; i < 8; i++ {
		v.Advance(1_000_000)
		cur := v.CurrentHalfline()
		if cur < prev {
			t.Fatalf("halfline went backwards inside a frame: %d then %d", prev, cur)
		}
		prev = cur
	}
	if prev == 0 {
		t.Fatal("halfline never advanced inside the frame")
	}
	// One more step crosses the frame boundary (8,000,000
	// plus 200,000 cycles total) and the counter wraps.
	v.Advance(200_000)
	if got := v.CurrentHalfline(); got >= prev {
		t.Errorf("halfline after wrap = %d, want less than %d", got, prev)
	}
	// VICURR reads the same value and ignores writes.
	if got := v.Read16(regVICURR); int(got) != v.CurrentHalfline() {
		t.Errorf("VICURR = %d, want %d", got, v.CurrentHalfline())
	}
	before := v.CurrentHalfline()
	v.Write16(regVICURR, 999)
	if got := v.CurrentHalfline(); got != before {
		t.Errorf("halfline after VICURR write = %d, want %d", got, before)
	}
}

func TestNonInterlacedTiming(t *testing.T) {
	interlaced := New()
	interlaced.Advance(CyclesPerFrame / 2)
	// Defaults: 258 lines per field, 2 fields, so 1032
	// halflines per frame and half a frame is halfline 516.
	if got := interlaced.CurrentHalfline(); got != 516 {
		t.Errorf("interlaced half frame halfline = %d, want 516", got)
	}
	progressive := New()
	progressive.Write16(regVIDCR, 1|(1<<2)) // ENB + NIN
	progressive.Advance(CyclesPerFrame / 2)
	// NIN: 1 field per frame, 516 halflines per frame, so
	// half a frame is halfline 258.
	if got := progressive.CurrentHalfline(); got != 258 {
		t.Errorf("non-interlaced half frame halfline = %d, want 258", got)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	v := New()
	write32(v, regVIHTR0, 0xABCD1234)
	v.Write16(regFilter0, 0x777)
	programDI0(v, 4)
	v.Advance(1_234_567)
	state := v.Serialize()
	if len(state) != SerializeSize {
		t.Fatalf("Serialize size = %d, want %d", len(state), SerializeSize)
	}
	v2 := New()
	if err := v2.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	for off := uint32(0); off < regEnd; off += 2 {
		if got, want := v2.Read16(off), v.Read16(off); got != want {
			t.Errorf("reg %#02x after restore = %#04x, want %#04x", off, got, want)
		}
	}
	if got, want := v2.CurrentHalfline(), v.CurrentHalfline(); got != want {
		t.Errorf("halfline after restore = %d, want %d (cycle count not preserved)", got, want)
	}
	if v2.Pending() != v.Pending() {
		t.Errorf("Pending after restore = %v, want %v", v2.Pending(), v.Pending())
	}
	// Both copies keep advancing in lockstep.
	v.Advance(100_000)
	v2.Advance(100_000)
	if got, want := v2.CurrentHalfline(), v.CurrentHalfline(); got != want {
		t.Errorf("halfline after post-restore advance = %d, want %d", got, want)
	}
	if err := v2.Deserialize([]byte{1, 2, 3}); err != ErrBadState {
		t.Fatalf("short state error = %v, want ErrBadState", err)
	}
	bad := append([]byte(nil), state...)
	bad[0] = 99
	if err := v2.Deserialize(bad); err != ErrBadState {
		t.Fatalf("bad version error = %v, want ErrBadState", err)
	}
}
