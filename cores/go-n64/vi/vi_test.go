package vi

import (
	"encoding/binary"
	"testing"
)

// programNTSC240p sets up the standard test timing: a
// 16-bit framebuffer, 525 half-lines (progressive 240p),
// interrupt at half-line 2, and 1.0 scaling on both axes.
func programNTSC240p(v *VI) {
	v.WriteRegister(RegCtrl, 2) // TYPE 2, 16-bit
	v.WriteRegister(RegVTotal, 524)
	v.WriteRegister(RegVIntr, 2)
	v.WriteRegister(RegWidth, 320)
	v.WriteRegister(RegHVideo, 108<<16|428)
	v.WriteRegister(RegVVideo, 37<<16|517)
	v.WriteRegister(RegXScale, 0x400)
	v.WriteRegister(RegYScale, 0x400)
}

func TestNTSC240pInterruptOncePerField(t *testing.T) {
	v := New()
	fires := 0
	clears := 0
	v.OnInterrupt = func(active bool) {
		if active {
			fires++
		} else {
			clears++
		}
	}
	programNTSC240p(v)

	v.Advance(1562500) // exactly one field
	if fires != 1 {
		t.Fatalf("interrupt fired %d times in one field, want 1", fires)
	}
	if !v.Pending() {
		t.Fatal("interrupt not pending after the field")
	}
	if c := v.Current(); c > 2 {
		t.Fatalf("counter %d after a full field, want near 0", c)
	}
	if v.Field() != 0 {
		t.Fatalf("field %d in progressive mode, want 0", v.Field())
	}

	v.WriteRegister(RegVCurrent, 0)
	if v.Pending() {
		t.Fatal("interrupt still pending after a V_CURRENT write")
	}
	if clears != 1 {
		t.Fatalf("clear callback ran %d times, want 1", clears)
	}
}

func TestRegisterReadback(t *testing.T) {
	v := New()
	v.WriteRegister(RegOrigin, 0xFF123456)
	if got := v.ReadRegister(RegOrigin); got != 0x123456 {
		t.Fatalf("ORIGIN read %#x, want 0x123456 (24-bit mask)", got)
	}
	v.WriteRegister(RegBurst, 0xDEADBEEF)
	if got := v.ReadRegister(RegBurst); got != 0xDEADBEEF {
		t.Fatalf("BURST read %#x, want the raw written value", got)
	}

	programNTSC240p(v)
	if got := v.Current(); got != 0 {
		t.Fatalf("Current at reset %d, want 0", got)
	}
	// 10,000 cycles at 525 half-lines per 1,562,500 cycles
	// is exactly 3 half-lines plus a remainder.
	v.Advance(10000)
	if got := v.Current(); got != 2 {
		t.Fatalf("Current after 3 half-lines %d, want 2 (counter 3, field 0)", got)
	}
	if got := v.ReadRegister(RegVCurrent); got != v.Current() {
		t.Fatalf("V_CURRENT read %d, want Current %d", got, v.Current())
	}

	// Interlaced timing: an even half-line total toggles
	// the field flag at the wrap, and the flag is bit 0
	// of V_CURRENT.
	v2 := New()
	programNTSC240p(v2)
	v2.WriteRegister(RegVTotal, 525) // 526 half-lines, interlaced
	v2.Advance(1562500)
	if v2.Field() != 1 {
		t.Fatalf("field after an interlaced field %d, want 1", v2.Field())
	}
	if got := v2.Current(); got != 1 {
		t.Fatalf("Current after interlaced wrap %d, want 1 (counter 0, field 1)", got)
	}
	v2.Advance(1562500)
	if v2.Field() != 0 {
		t.Fatalf("field after two interlaced fields %d, want 0", v2.Field())
	}
}

func TestVIOffFreezesCounter(t *testing.T) {
	v := New()
	fires := 0
	v.OnInterrupt = func(active bool) {
		if active {
			fires++
		}
	}
	v.WriteRegister(RegVTotal, 524)
	v.WriteRegister(RegVIntr, 2)
	v.Advance(1562500) // TYPE is still 0
	if fires != 0 {
		t.Fatalf("interrupt fired %d times while off, want 0", fires)
	}
	if got := v.Current(); got != 0 {
		t.Fatalf("Current while off %d, want 0", got)
	}
	if _, w, h := v.Present(make([]byte, 1<<20)); w != 0 || h != 0 {
		t.Fatalf("Present while off gave %dx%d, want 0x0", w, h)
	}
}

func putPixel16(dram []byte, width, x, y int, val uint16) {
	addr := (y*width + x) * 2
	binary.BigEndian.PutUint16(dram[addr:], val)
}

func TestPresent16Bit(t *testing.T) {
	v := New()
	programNTSC240p(v)
	v.WriteRegister(RegOrigin, 0)

	dram := make([]byte, 320*240*2)
	putPixel16(dram, 320, 0, 0, 0xF801) // pure red, alpha 1
	putPixel16(dram, 320, 1, 0, 0x07C1) // pure green, alpha 1
	putPixel16(dram, 320, 0, 2, 0x003F) // pure blue, alpha 1
	putPixel16(dram, 320, 2, 0, 0xF800) // red with alpha 0

	fb, w, h := v.Present(dram)
	if w != 320 || h != 240 {
		t.Fatalf("Present geometry %dx%d, want 320x240", w, h)
	}
	if fb == nil || len(fb) != w*h {
		t.Fatalf("Present returned %d pixels, want %d", len(fb), w*h)
	}
	if got := fb[0]; got != 0xFFFF0000 {
		t.Fatalf("pixel (0,0) %#08x, want 0xFFFF0000", got)
	}
	if got := fb[1]; got != 0xFF00FF00 {
		t.Fatalf("pixel (1,0) %#08x, want 0xFF00FF00", got)
	}
	if got := fb[2*w]; got != 0xFF0000FF {
		t.Fatalf("pixel (0,2) %#08x, want 0xFF0000FF", got)
	}
	if got := fb[2]; got != 0x00FF0000 {
		t.Fatalf("pixel (2,0) %#08x, want 0x00FF0000 (alpha 0 keeps the channels)", got)
	}

	v.WriteRegister(RegCtrl, 0)
	fb, w, h = v.Present(dram)
	if fb != nil || w != 0 || h != 0 {
		t.Fatalf("Present with TYPE 0 gave %v %dx%d, want nil 0x0", fb, w, h)
	}
}

func TestPresent32BitAndGamma(t *testing.T) {
	v := New()
	programNTSC240p(v)
	v.WriteRegister(RegCtrl, 3) // TYPE 3, 32-bit
	dram := make([]byte, 320*240*4)
	copy(dram[0:], []byte{0x10, 0x20, 0x30, 0x40})
	fb, _, _ := v.Present(dram)
	if fb == nil {
		t.Fatal("Present returned nil for TYPE 3")
	}
	if got := fb[0]; got != 0x40102030 {
		t.Fatalf("32-bit pixel %#08x, want 0x40102030", got)
	}

	// Gamma brightens mid values through the square root
	// curve: 0x40 (64) maps to 128.
	v.WriteRegister(RegCtrl, 3|CtrlGamma)
	copy(dram[0:], []byte{0x40, 0x40, 0x40, 0xFF})
	fb, _, _ = v.Present(dram)
	if got := fb[0]; got != 0xFF808080 {
		t.Fatalf("gamma pixel %#08x, want 0xFF808080", got)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	if SerializeSize != 68 {
		t.Fatalf("SerializeSize %d, want 68", SerializeSize)
	}
	v := New()
	programNTSC240p(v)
	v.Advance(10000)

	buf := make([]byte, SerializeSize)
	if n := v.Serialize(buf); n != SerializeSize {
		t.Fatalf("Serialize wrote %d bytes, want %d", n, SerializeSize)
	}

	v2 := New()
	if err := v2.Deserialize(buf); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if v2.Current() != v.Current() {
		t.Fatalf("restored Current %d, want %d", v2.Current(), v.Current())
	}
	if got := v2.ReadRegister(RegWidth); got != 320 {
		t.Fatalf("restored WIDTH %d, want 320", got)
	}

	fires1, fires2 := 0, 0
	v.OnInterrupt = func(active bool) {
		if active {
			fires1++
		}
	}
	v2.OnInterrupt = func(active bool) {
		if active {
			fires2++
		}
	}
	// Run both to just past the field wrap; the target at
	// half-line 2 must fire exactly once in the new field
	// on both machines.
	for i := 0; i < 10; i++ {
		v.Advance(156250)
		v2.Advance(156250)
		if v.Current() != v2.Current() {
			t.Fatalf("step %d: Current diverged, %d vs %d", i, v.Current(), v2.Current())
		}
	}
	if fires1 != 1 || fires2 != 1 {
		t.Fatalf("fires after restore %d and %d, want 1 and 1", fires1, fires2)
	}

	if err := v2.Deserialize(buf[:10]); err == nil {
		t.Fatal("Deserialize accepted a short state")
	}
}
