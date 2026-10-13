package scd

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// ---------------------------------------------------------------------------
// SCD-3 unit gates: the graphics ASIC. Every gate compares exact
// image buffer bytes against expectations derived from the
// documented trace table, stamp, and buffer cell layout rules,
// never from the renderer itself.
//
// Fixture layout in Word RAM (byte offsets):
//
//	0x0000  stamp data (stamp 1 at byte 128, stamp 4 at byte 512)
//	0x1000  stamp map
//	0x2000  trace table
//	0x4000  image buffer
// ---------------------------------------------------------------------------

const (
	asicTestMapBase   = 0x1000
	asicTestTraceBase = 0x2000
	asicTestBufBase   = 0x4000
)

// asicSetReg writes one ASIC register word through the sub CPU
// register page.
func asicSetReg(u *Unit, off uint32, v uint16) { u.Write16(0xFF8000+off, v) }

// asicGetReg reads one ASIC register word through the sub CPU
// register page.
func asicGetReg(u *Unit, off uint32) uint16 { return u.Read16(0xFF8000 + off) }

// fillStamp writes one stamp into Word RAM stamp data. num is the
// stamp number (256 dots per number), size is 16 or 32, and pix
// gives the pixel at stamp coordinate (x, y). Cells are stored
// column by column: cell index = (y/8) + (x/8) * (size/8).
func fillStamp(wr []byte, num, size int, pix func(x, y int) uint8) {
	cps := size / 8
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			cell := (y / 8) + (x/8)*cps
			dot := num*256 + cell*64 + (y%8)*8 + (x % 8)
			v := pix(x, y) & 0x0F
			if dot%2 == 0 {
				wr[dot/2] = wr[dot/2]&0x0F | v<<4
			} else {
				wr[dot/2] = wr[dot/2]&0xF0 | v
			}
		}
	}
}

// setMapEntry writes one stamp map entry (row major words).
func setMapEntry(wr []byte, base, idx int, entry uint16) {
	binary.BigEndian.PutUint16(wr[base+idx*2:], entry)
}

// setTrace writes one trace table entry: start X and Y in 13.3
// fixed point, delta X and Y as raw signed 5.11 words.
func setTrace(wr []byte, base, line int, sx, sy, dx, dy uint16) {
	o := base + line*8
	binary.BigEndian.PutUint16(wr[o:], sx)
	binary.BigEndian.PutUint16(wr[o+2:], sy)
	binary.BigEndian.PutUint16(wr[o+4:], dx)
	binary.BigEndian.PutUint16(wr[o+6:], dy)
}

// expectBufBytes packs a raster image of w by h pixels into the
// image buffer cell layout for a buffer whose columns are
// cellsPerCol cells high.
func expectBufBytes(w, h, cellsPerCol int, pix func(x, y int) uint8) []byte {
	out := make([]byte, (w/8)*cellsPerCol*32)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dot := (x/8)*cellsPerCol*64 + (y/8)*64 + (y%8)*8 + (x % 8)
			v := pix(x, y) & 0x0F
			if dot%2 == 0 {
				out[dot/2] |= v << 4
			} else {
				out[dot/2] |= v
			}
		}
	}
	return out
}

// programASIC writes the register block for the fixture layout
// and triggers the draw with the trace base write.
func programASIC(u *Unit, ctl, vcell, offset uint8, hdot uint16, vdot uint8) {
	asicSetReg(u, 0x58, uint16(ctl))
	asicSetReg(u, 0x5A, asicTestMapBase/4)
	asicSetReg(u, 0x5C, uint16(vcell))
	asicSetReg(u, 0x5E, asicTestBufBase/4)
	asicSetReg(u, 0x60, uint16(offset))
	asicSetReg(u, 0x62, hdot)
	asicSetReg(u, 0x64, uint16(vdot))
	asicSetReg(u, 0x66, asicTestTraceBase/4) // trigger
}

// pat16 is the 16x16 test pattern: every pixel nonzero and every
// coordinate identifiable.
func pat16(x, y int) uint8 { return uint8((x+y*16)%15 + 1) }

// pat32 is the 32x32 test pattern.
func pat32(x, y int) uint8 { return uint8((x+y*32)%15 + 1) }

// identityTrace fills the trace table for a straight copy of a
// size by size stamp area at the map origin.
func identityTrace(wr []byte, lines int) {
	for l := 0; l < lines; l++ {
		setTrace(wr, asicTestTraceBase, l, 0, uint16(l<<3), 0x0800, 0)
	}
}

func TestASICIdentityTrace(t *testing.T) {
	u := NewUnit(nil)
	wr := u.WordRAM()
	fillStamp(wr, 1, 16, pat16)
	setMapEntry(wr, asicTestMapBase, 0, 1)
	identityTrace(wr, 16)

	programASIC(u, 0x00, 1, 0, 16, 16)

	// For a 16x16 buffer two cells high, the buffer cell layout
	// and the stamp cell layout coincide, so the buffer bytes
	// must equal the stamp bytes exactly.
	got := wr[asicTestBufBase : asicTestBufBase+128]
	want := wr[128:256]
	if !bytes.Equal(got, want) {
		t.Fatalf("identity buffer differs from stamp bytes:\n got %x\nwant %x", got, want)
	}
	if u.ASICBusy() {
		t.Fatal("ASIC still busy after a synchronous draw")
	}
	if got := u.ASICLinesRemaining(); got != 0 {
		t.Fatalf("lines remaining = %d, want 0 after the draw", got)
	}
	if got := asicGetReg(u, 0x58); got&0x8000 != 0 {
		t.Fatalf("GRON reads set after the draw: %#06x", got)
	}
}

func TestASICRotationTrace(t *testing.T) {
	u := NewUnit(nil)
	wr := u.WordRAM()
	fillStamp(wr, 1, 16, pat16)
	setMapEntry(wr, asicTestMapBase, 0, 1)
	// Quarter turn: each output line starts one stamp dot to the
	// left of the previous line's start (line step (-1, 0)) and
	// walks down the stamp (dot delta (0, +1)).
	for l := 0; l < 16; l++ {
		setTrace(wr, asicTestTraceBase, l, uint16((15-l)<<3), 0, 0, 0x0800)
	}

	programASIC(u, 0x00, 1, 0, 16, 16)

	want := expectBufBytes(16, 16, 2, func(x, y int) uint8 {
		return pat16(15-y, x)
	})
	got := wr[asicTestBufBase : asicTestBufBase+len(want)]
	if !bytes.Equal(got, want) {
		t.Fatalf("rotated buffer mismatch:\n got %x\nwant %x", got, want)
	}
}

func TestASICScaleDown(t *testing.T) {
	u := NewUnit(nil)
	wr := u.WordRAM()
	fillStamp(wr, 1, 16, pat16)
	setMapEntry(wr, asicTestMapBase, 0, 1)
	// 2x scale down: dot delta 2.0, line starts step 2 dots.
	for l := 0; l < 8; l++ {
		setTrace(wr, asicTestTraceBase, l, 0, uint16((2*l)<<3), 0x1000, 0)
	}

	programASIC(u, 0x00, 0, 0, 8, 8)

	want := expectBufBytes(8, 8, 1, func(x, y int) uint8 {
		return pat16(2*x, 2*y)
	})
	got := wr[asicTestBufBase : asicTestBufBase+len(want)]
	if !bytes.Equal(got, want) {
		t.Fatalf("scale down buffer mismatch:\n got %x\nwant %x", got, want)
	}
}

func TestASICUpscaleFractional(t *testing.T) {
	u := NewUnit(nil)
	wr := u.WordRAM()
	fillStamp(wr, 1, 16, pat16)
	setMapEntry(wr, asicTestMapBase, 0, 1)
	// 2x upscale: dot delta 0.5; each pair of lines starts on the
	// same stamp row, so every stamp pixel becomes a 2x2 block.
	for l := 0; l < 32; l++ {
		setTrace(wr, asicTestTraceBase, l, 0, uint16((l/2)<<3), 0x0400, 0)
	}

	programASIC(u, 0x00, 3, 0, 32, 32)

	want := expectBufBytes(32, 32, 4, func(x, y int) uint8 {
		return pat16(x/2, y/2)
	})
	got := wr[asicTestBufBase : asicTestBufBase+len(want)]
	if !bytes.Equal(got, want) {
		t.Fatalf("upscale buffer mismatch:\n got %x\nwant %x", got, want)
	}
}

func TestASICEntryFlipAndRotation(t *testing.T) {
	cases := []struct {
		name  string
		entry uint16
		pix   func(x, y int) uint8
	}{
		{"horizontal flip", 0x8001, func(x, y int) uint8 { return pat16(15-x, y) }},
		{"vertical flip (hflip + 180)", 0xC001, func(x, y int) uint8 { return pat16(x, 15-y) }},
		{"rotation field 1", 0x2001, func(x, y int) uint8 { return pat16(15-y, x) }},
		{"rotation field 2 (180)", 0x4001, func(x, y int) uint8 { return pat16(15-x, 15-y) }},
		{"rotation field 3", 0x6001, func(x, y int) uint8 { return pat16(y, 15-x) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := NewUnit(nil)
			wr := u.WordRAM()
			fillStamp(wr, 1, 16, pat16)
			setMapEntry(wr, asicTestMapBase, 0, tc.entry)
			identityTrace(wr, 16)
			programASIC(u, 0x00, 1, 0, 16, 16)
			want := expectBufBytes(16, 16, 2, tc.pix)
			got := wr[asicTestBufBase : asicTestBufBase+len(want)]
			if !bytes.Equal(got, want) {
				t.Fatalf("buffer mismatch:\n got %x\nwant %x", got, want)
			}
		})
	}
}

func TestASICRepeatWrapAndClip(t *testing.T) {
	setup := func(u *Unit) {
		wr := u.WordRAM()
		fillStamp(wr, 1, 16, pat16)
		for i := 0; i < 16; i++ {
			setMapEntry(wr, asicTestMapBase, i, 1)
		}
		// Lines start at stamp space x = 250, six dots from the
		// 256 dot map edge, and walk 16 dots.
		for l := 0; l < 16; l++ {
			setTrace(wr, asicTestTraceBase, l, 250<<3, uint16(l<<3), 0x0800, 0)
		}
	}

	// Repeat on: the position wraps at the map edge.
	u := NewUnit(nil)
	setup(u)
	programASIC(u, 0x01, 1, 0, 16, 16)
	want := expectBufBytes(16, 16, 2, func(x, y int) uint8 {
		return pat16((250+x)%16, y)
	})
	if got := u.WordRAM()[asicTestBufBase : asicTestBufBase+len(want)]; !bytes.Equal(got, want) {
		t.Fatalf("repeat wrap buffer mismatch:\n got %x\nwant %x", got, want)
	}

	// Repeat off, overwrite priority: out-of-map dots fetch
	// pixel 0 and leave the sentinel nibbles untouched.
	u2 := NewUnit(nil)
	setup(u2)
	for i := asicTestBufBase; i < asicTestBufBase+128; i++ {
		u2.WordRAM()[i] = 0xEE
	}
	u2.Write8(0xFF8003, 0x10) // priority mode 2 (overwrite)
	programASIC(u2, 0x00, 1, 0, 16, 16)
	want2 := expectBufBytes(16, 16, 2, func(x, y int) uint8 {
		if x < 6 {
			return pat16(10+x, y)
		}
		return 0x0E // sentinel nibble survives
	})
	if got := u2.WordRAM()[asicTestBufBase : asicTestBufBase+len(want2)]; !bytes.Equal(got, want2) {
		t.Fatalf("repeat off buffer mismatch:\n got %x\nwant %x", got, want2)
	}
}

func TestASICPriorityModes(t *testing.T) {
	// Stamp: left half transparent (0), right half nonzero.
	halfZero := func(x, y int) uint8 {
		if x < 8 {
			return 0
		}
		return uint8((x+y)%15 + 1)
	}
	cases := []struct {
		name    string
		pm      uint8
		prefill byte
		pix     func(x, y int) uint8
	}{
		{"overwrite skips zeros", 2, 0x33, func(x, y int) uint8 {
			if x < 8 {
				return 3
			}
			return halfZero(x, y)
		}},
		{"normal writes zeros", 0, 0x33, func(x, y int) uint8 {
			return halfZero(x, y)
		}},
		{"underwrite keeps nonzero buffer", 1, 0x44, func(x, y int) uint8 {
			return 4
		}},
		{"prohibited writes nothing", 3, 0x33, func(x, y int) uint8 {
			return 3
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := NewUnit(nil)
			wr := u.WordRAM()
			fillStamp(wr, 1, 16, halfZero)
			setMapEntry(wr, asicTestMapBase, 0, 1)
			identityTrace(wr, 16)
			for i := asicTestBufBase; i < asicTestBufBase+128; i++ {
				wr[i] = tc.prefill
			}
			u.Write8(0xFF8003, tc.pm<<3) // priority mode field
			programASIC(u, 0x00, 1, 0, 16, 16)
			want := expectBufBytes(16, 16, 2, tc.pix)
			got := wr[asicTestBufBase : asicTestBufBase+len(want)]
			if !bytes.Equal(got, want) {
				t.Fatalf("buffer mismatch:\n got %x\nwant %x", got, want)
			}
		})
	}
}

func TestASICStamp32(t *testing.T) {
	for _, entry := range []uint16{4, 7} { // 7 must mask down to 4
		u := NewUnit(nil)
		wr := u.WordRAM()
		fillStamp(wr, 4, 32, pat32)
		setMapEntry(wr, asicTestMapBase, 0, entry)
		identityTrace(wr, 32)
		programASIC(u, 0x02, 3, 0, 32, 32) // STS: 32x32 stamps
		want := expectBufBytes(32, 32, 4, pat32)
		got := wr[asicTestBufBase : asicTestBufBase+len(want)]
		if !bytes.Equal(got, want) {
			t.Fatalf("entry %#06x: 32x32 buffer mismatch:\n got %x\nwant %x", entry, got, want)
		}
	}
}

func TestASICLargeMap(t *testing.T) {
	u := NewUnit(nil)
	wr := u.WordRAM()
	fillStamp(wr, 1, 16, pat16)
	// 4096 dot map: base aligned to 0x20000; the 16 sampled dots
	// start at x = 300, spanning stamp columns 18 and 19 of
	// row 0 (entry indices 18 and 19).
	binary.BigEndian.PutUint16(wr[0x20000+18*2:], 1)
	binary.BigEndian.PutUint16(wr[0x20000+19*2:], 1)
	setTrace(wr, asicTestTraceBase, 0, 300<<3, 8<<3, 0x0800, 0)
	asicSetReg(u, 0x58, 0x0004) // SMS: 4096x4096 dot map
	asicSetReg(u, 0x5A, 0x20000/4)
	asicSetReg(u, 0x5C, 0)
	asicSetReg(u, 0x5E, asicTestBufBase/4)
	asicSetReg(u, 0x60, 0)
	asicSetReg(u, 0x62, 16)
	asicSetReg(u, 0x64, 1)
	asicSetReg(u, 0x66, asicTestTraceBase/4)

	want := expectBufBytes(16, 1, 1, func(x, y int) uint8 {
		return pat16((300+x)%16, 8)
	})
	got := wr[asicTestBufBase : asicTestBufBase+len(want)]
	if !bytes.Equal(got, want) {
		t.Fatalf("large map buffer mismatch:\n got %x\nwant %x", got, want)
	}
}

func TestASICBufferOffset(t *testing.T) {
	u := NewUnit(nil)
	wr := u.WordRAM()
	fillStamp(wr, 1, 16, pat16)
	setMapEntry(wr, asicTestMapBase, 0, 1)
	identityTrace(wr, 16)
	// Offset: 3 horizontal dots, 2 vertical dots (register bits
	// 0-2 and 3-5) shift the first drawn dot to buffer dot
	// index 3 + 2*8 = 19.
	programASIC(u, 0x00, 1, 3|(2<<3), 16, 16)

	dotAt := func(idx int) uint8 {
		b := wr[asicTestBufBase+idx/2]
		if idx%2 == 0 {
			return b >> 4
		}
		return b & 0x0F
	}
	if got := dotAt(18); got != 0 {
		t.Fatalf("dot 18 = %d, want 0 (before the offset start)", got)
	}
	if got := dotAt(19); got != pat16(0, 0) {
		t.Fatalf("dot 19 = %d, want %d (pattern origin)", got, pat16(0, 0))
	}
	if got := dotAt(27); got != pat16(0, 1) {
		t.Fatalf("dot 27 = %d, want %d (line 1 origin)", got, pat16(0, 1))
	}
	// Dot (8, 0) sits one column stride (128 dots) past dot 0.
	if got := dotAt(19 + 128); got != pat16(8, 0) {
		t.Fatalf("dot 147 = %d, want %d (pattern (8,0))", got, pat16(8, 0))
	}
}

func TestASICRegisterFile(t *testing.T) {
	u := NewUnit(nil)
	if got := asicGetReg(u, 0x58); got != 0 {
		t.Fatalf("stamp size/status at power-on = %#06x, want 0", got)
	}
	asicSetReg(u, 0x5A, 0x1234)
	asicSetReg(u, 0x5C, 0x001F)
	asicSetReg(u, 0x5E, 0x2ABC)
	asicSetReg(u, 0x60, 0x003F)
	asicSetReg(u, 0x62, 0x01FF)
	asicSetReg(u, 0x64, 0x00F0)
	for off, want := range map[uint32]uint16{
		0x5A: 0x1234, 0x5C: 0x001F, 0x5E: 0x2ABC,
		0x60: 0x003F, 0x62: 0x01FF, 0x64: 0x00F0,
	} {
		if got := asicGetReg(u, off); got != want {
			t.Fatalf("register %#04x = %#06x, want %#06x", off, got, want)
		}
	}
	// The stamp control byte stores through the low byte only;
	// the GRON high byte ignores writes.
	u.Write8(0xFF8058, 0xFF)
	u.Write8(0xFF8059, 0x07)
	if got := asicGetReg(u, 0x58); got != 0x0007 {
		t.Fatalf("stamp control = %#06x, want 0x0007", got)
	}
	// A trace with zero vertical dots completes at once and
	// draws nothing.
	asicSetReg(u, 0x64, 0x0000)
	u.WordRAM()[asicTestBufBase] = 0xAB
	u.Write8(0xFF8067, 0x00) // trace base low byte: starts the op
	if u.ASICBusy() {
		t.Fatal("zero line trace left the ASIC busy")
	}
	if got := u.WordRAM()[asicTestBufBase]; got != 0xAB {
		t.Fatalf("zero line trace wrote the buffer: %#04x", got)
	}
}

func TestASICDeferredUntilWordRAMGiven(t *testing.T) {
	u := NewUnit(nil)
	wr := u.WordRAM()
	fillStamp(wr, 1, 16, pat16)
	setMapEntry(wr, asicTestMapBase, 0, 1)
	identityTrace(wr, 16)

	// The sub CPU hands Word RAM to the main CPU (2M DMNA).
	u.Write8(0xFF8002, 0x02)
	programASIC(u, 0x00, 1, 0, 16, 16)
	if !u.ASICBusy() {
		t.Fatal("trace did not latch while the main CPU owns Word RAM")
	}
	if got := u.Read8(0xFF8058); got != 0x80 {
		t.Fatalf("GRON byte = %#04x, want 0x80 while deferred", got)
	}
	if got := u.ASICLinesRemaining(); got != 16 {
		t.Fatalf("lines remaining = %d, want 16 while deferred", got)
	}
	for i := asicTestBufBase; i < asicTestBufBase+128; i++ {
		if wr[i] != 0 {
			t.Fatalf("buffer byte %#x written while deferred", i)
		}
	}

	// The main CPU hands Word RAM back: the draw runs at once.
	u.MainWrite8(0x02, 0x02)
	if u.ASICBusy() {
		t.Fatal("ASIC still busy after Word RAM was handed back")
	}
	got := wr[asicTestBufBase : asicTestBufBase+128]
	if want := wr[128:256]; !bytes.Equal(got, want) {
		t.Fatalf("deferred draw buffer mismatch:\n got %x\nwant %x", got, want)
	}
}

func TestASIC1MMode(t *testing.T) {
	u := NewUnit(nil)
	wr := u.WordRAM()
	fillStamp(wr, 1, 16, pat16)
	setMapEntry(wr, asicTestMapBase, 0, 1)
	identityTrace(wr, 16)
	u.Write8(0xFF8002, 0x01) // 1M mode: the sub CPU holds a half
	programASIC(u, 0x00, 1, 0, 16, 16)
	if u.ASICBusy() {
		t.Fatal("draw deferred in 1M mode (the sub CPU always has access)")
	}
	got := wr[asicTestBufBase : asicTestBufBase+128]
	if want := wr[128:256]; !bytes.Equal(got, want) {
		t.Fatalf("1M draw buffer mismatch:\n got %x\nwant %x", got, want)
	}
}

func TestASICStateRoundTrip(t *testing.T) {
	u := NewUnit(nil)
	wr := u.WordRAM()
	fillStamp(wr, 1, 16, pat16)
	setMapEntry(wr, asicTestMapBase, 0, 1)
	identityTrace(wr, 16)
	u.Write8(0xFF8002, 0x02)           // main CPU owns Word RAM: draw defers
	programASIC(u, 0x01, 1, 0, 16, 16) // RPT on: identity is in map
	if !u.ASICBusy() {
		t.Fatal("setup failed: draw not pending")
	}

	buf := make([]byte, u.SerializeSize())
	if err := u.Serialize(buf); err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	u2 := NewUnit(nil)
	if err := u2.Deserialize(buf); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if !u2.ASICBusy() {
		t.Fatal("pending draw lost in the state round trip")
	}
	if got := u2.ASICLinesRemaining(); got != 16 {
		t.Fatalf("restored lines remaining = %d, want 16", got)
	}
	if got := asicGetReg(u2, 0x5A); got != asicTestMapBase/4 {
		t.Fatalf("restored stamp map base = %#06x, want %#06x", got, asicTestMapBase/4)
	}
	buf2 := make([]byte, u2.SerializeSize())
	if err := u2.Serialize(buf2); err != nil {
		t.Fatalf("re-Serialize: %v", err)
	}
	if !bytes.Equal(buf, buf2) {
		t.Fatal("ASIC state did not round trip byte for byte")
	}
	// The deferred draw completes identically after restore.
	u2.MainWrite8(0x02, 0x02)
	got := u2.WordRAM()[asicTestBufBase : asicTestBufBase+128]
	want := u2.WordRAM()[128:256]
	if !bytes.Equal(got, want) {
		t.Fatalf("post-restore draw mismatch:\n got %x\nwant %x", got, want)
	}

	// An SCD-2 era state (ASIC extension bytes all zero) loads
	// with the ASIC at power-on state.
	old := make([]byte, len(buf))
	copy(old, buf)
	tail := len(old) - 256
	for i := tail + 242; i < tail+255; i++ {
		old[i] = 0
	}
	u3 := NewUnit(nil)
	if err := u3.Deserialize(old); err != nil {
		t.Fatalf("Deserialize SCD-2 era state: %v", err)
	}
	if u3.ASICBusy() || u3.ASICLinesRemaining() != 0 {
		t.Fatal("SCD-2 era state did not load the ASIC at power-on state")
	}

	// The unit serialized size is unchanged from SCD-1/SCD-2.
	if got, want := u.SerializeSize(), 0x80000+0x40000+0x10000+2048+256; got != want {
		t.Fatalf("SerializeSize = %d, want %d", got, want)
	}
}
