package mdec

import (
	"math"
	"testing"
)

// Test fixtures are synthetic and built in this file
// from the documented formats: run length halfwords
// for hardware mode (section 6.4), the standard scale
// table printed in section 6.3, and BS bitstreams for
// the BS extension (sections 13.19 to 13.21).
// Expected values are computed here from the
// documented math: a float64 IDCT reference and the
// YUV to RGB formula of section 6.3.

// stdScaleWords is the standard scale table printed
// in section 6.3 under set_scale_table, row by row,
// transcribed as unsigned hex halfwords.
var stdScaleWords = [64]uint16{
	0x5A82, 0x5A82, 0x5A82, 0x5A82, 0x5A82, 0x5A82, 0x5A82, 0x5A82,
	0x7D8A, 0x6A6D, 0x471C, 0x18F8, 0xE707, 0xB8E3, 0x9592, 0x8275,
	0x7641, 0x30FB, 0xCF04, 0x89BE, 0x89BE, 0xCF04, 0x30FB, 0x7641,
	0x6A6D, 0xE707, 0x8275, 0xB8E3, 0x471C, 0x7D8A, 0x18F8, 0x9592,
	0x5A82, 0xA57D, 0xA57D, 0x5A82, 0x5A82, 0xA57D, 0xA57D, 0x5A82,
	0x471C, 0x8275, 0x18F8, 0x6A6D, 0x9592, 0xE707, 0x7D8A, 0xB8E3,
	0x30FB, 0x89BE, 0x7641, 0xCF04, 0xCF04, 0x7641, 0x89BE, 0x30FB,
	0x18F8, 0xB8E3, 0x6A6D, 0x8275, 0x7D8A, 0x9592, 0x471C, 0xE707,
}

func allQuant(v byte) [64]byte {
	var q [64]byte
	for i := range q {
		q[i] = v
	}
	return q
}

func sendQuant(d *MDEC, color bool, qy, quv [64]byte) {
	cmd := uint32(2) << 29
	var seq []byte
	seq = append(seq, qy[:]...)
	if color {
		cmd |= 1
		seq = append(seq, quv[:]...)
	}
	d.WriteWord(cmd)
	for i := 0; i < len(seq); i += 4 {
		var w uint32
		for j := 0; j < 4; j++ {
			w |= uint32(seq[i+j]) << (8 * j)
		}
		d.WriteWord(w)
	}
}

func sendScale(d *MDEC, tab [64]uint16) {
	d.WriteWord(uint32(3) << 29)
	for i := 0; i < 64; i += 2 {
		d.WriteWord(uint32(tab[i]) | uint32(tab[i+1])<<16)
	}
}

func loaded(t *testing.T) *MDEC {
	t.Helper()
	d := New()
	sendQuant(d, true, allQuant(8), allQuant(8))
	sendScale(d, stdScaleWords)
	if d.Status()&(1<<29) != 0 {
		t.Fatalf("decoder still busy after table loads: %08x", d.Status())
	}
	return d
}

func dcCode(q int, dc int) uint16 {
	return uint16(q<<10) | uint16(dc&0x3FF)
}

func rlAC(run int, lvl int) uint16 {
	return uint16(run<<10) | uint16(lvl&0x3FF)
}

const eob = 0xFE00

func packWords(halfs []uint16) []uint32 {
	words := make([]uint32, 0, (len(halfs)+1)/2)
	for i := 0; i < len(halfs); i += 2 {
		hi := uint16(eob)
		if i+1 < len(halfs) {
			hi = halfs[i+1]
		}
		words = append(words, uint32(halfs[i])|uint32(hi)<<16)
	}
	return words
}

func decodeCmdWord(depth int, signed, stp, bs bool, total int) uint32 {
	w := uint32(1)<<29 | uint32(depth)<<27 | uint32(total)
	if signed {
		w |= 1 << 26
	}
	if stp {
		w |= 1 << 25
	}
	if bs {
		w |= 1 << 24
	}
	return w
}

func runRL(t *testing.T, d *MDEC, depth int, signed, stp bool, halfs []uint16) []uint32 {
	t.Helper()
	words := packWords(halfs)
	d.WriteWord(decodeCmdWord(depth, signed, stp, false, len(words)))
	for _, w := range words {
		d.WriteWord(w)
	}
	return drain(t, d)
}

func drain(t *testing.T, d *MDEC) []uint32 {
	t.Helper()
	var out []uint32
	for i := 0; i < 100000; i++ {
		if d.Status()&(1<<31) != 0 {
			return out
		}
		out = append(out, d.ReadWord())
	}
	t.Fatalf("output FIFO never drained")
	return out
}

// floatIDCT is the independent float64 reference for
// the two dimensional inverse DCT, in the standard
// JPEG form. in and out are indexed x + y*8, with x
// the horizontal frequency or position.
func floatIDCT(in [64]float64) [64]float64 {
	var out [64]float64
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			var sum float64
			for v := 0; v < 8; v++ {
				cv := 1.0
				if v == 0 {
					cv = 1 / math.Sqrt2
				}
				for u := 0; u < 8; u++ {
					cu := 1.0
					if u == 0 {
						cu = 1 / math.Sqrt2
					}
					sum += cu * cv * in[v*8+u] *
						math.Cos(float64(2*x+1)*float64(u)*math.Pi/16) *
						math.Cos(float64(2*y+1)*float64(v)*math.Pi/16)
				}
			}
			out[y*8+x] = sum / 4
		}
	}
	return out
}

// refRGB applies the section 6.3 YUV to RGB formula
// to one pixel in float64, saturates, rounds, and
// converts to unsigned bytes.
func refRGB(y, cr, cb float64) (uint8, uint8, uint8) {
	clamp := func(v float64) int {
		if v < -128 {
			v = -128
		}
		if v > 127 {
			v = 127
		}
		return int(math.Round(v)) + 128
	}
	r := clamp(y + 1.402*cr)
	g := clamp(y - 0.3437*cb - 0.7143*cr)
	b := clamp(y + 1.772*cb)
	return uint8(r), uint8(g), uint8(b)
}

// solidColorMB builds the run length stream for one
// colored macroblock whose six blocks are DC only:
// Cr, Cb, then Y1..Y4, with q scale 1.
func solidColorMB(crDC, cbDC, yDC int) []uint16 {
	var halfs []uint16
	for _, dc := range []int{crDC, cbDC, yDC, yDC, yDC, yDC} {
		halfs = append(halfs, dcCode(1, dc), eob)
	}
	return halfs
}

// solidColorExpected computes, through the float
// reference, the spatial block values and the RGB
// bytes of the solid macroblock built by
// solidColorMB with all quant entries equal to 8.
func solidColorExpected(crDC, cbDC, yDC int) (uint8, uint8, uint8) {
	spatial := func(dc int) float64 {
		var in [64]float64
		in[0] = float64(dc * 8) // DC dequant: DC times quant[0]
		return floatIDCT(in)[0]
	}
	return refRGB(spatial(yDC), spatial(crDC), spatial(cbDC))
}

func TestQuantAndScaleLoadsAndUse(t *testing.T) {
	d := New()

	// Quant load (both tables): status parameter
	// field starts at words remaining minus 1 and
	// decreases as words are written.
	d.WriteWord(uint32(2)<<29 | 1)
	if got := d.Status() & 0xFFFF; got != 31 {
		t.Fatalf("quant param field after command = %04x, want 1f", got)
	}
	if d.Status()&(1<<29) == 0 {
		t.Fatalf("quant load should be busy")
	}
	qy, quv := allQuant(8), allQuant(8)
	seq := append(append([]byte{}, qy[:]...), quv[:]...)
	for wi := 0; wi < 32; wi++ {
		var w uint32
		for j := 0; j < 4; j++ {
			w |= uint32(seq[wi*4+j]) << (8 * j)
		}
		d.WriteWord(w)
		if wi == 0 {
			if got := d.Status() & 0xFFFF; got != 30 {
				t.Fatalf("quant param field after 1 word = %04x, want 1e", got)
			}
		}
	}
	if got := d.Status() & 0xFFFF; got != 0xFFFF {
		t.Fatalf("quant param field after last word = %04x, want ffff", got)
	}
	if d.Status()&(1<<29) != 0 {
		t.Fatalf("quant load should be done")
	}
	sendScale(d, stdScaleWords)

	base := runRL(t, d, 1, false, false, []uint16{dcCode(1, 16), eob})
	baseByte := int(byte(base[0]))

	// Changing one quant entry (luma table entry 0)
	// changes the DC output in the documented
	// direction and magnitude: the DC dequant is
	// DC times quant[0], so doubling quant[0] from 8
	// to 16 doubles the dequantized DC and adds
	// about (256-128)/8 = 16 to the spatial value.
	qy2 := allQuant(8)
	qy2[0] = 16
	sendQuant(d, false, qy2, allQuant(8))
	changed := runRL(t, d, 1, false, false, []uint16{dcCode(1, 16), eob})
	changedByte := int(byte(changed[0]))
	if diff := changedByte - baseByte; diff < 15 || diff > 17 {
		t.Fatalf("quant[0] 8 to 16 changed DC byte by %d, want about 16 (%d to %d)", diff, baseByte, changedByte)
	}

	// The scale table is used too: an all zero scale
	// table makes the IDCT output all zero, so the
	// mono byte is the unsigned zero level 128.
	sendScale(d, [64]uint16{})
	zeroed := runRL(t, d, 1, false, false, []uint16{dcCode(1, 16), eob})
	for _, w := range zeroed {
		if w != 0x80808080 {
			t.Fatalf("zero scale decode word = %08x, want 80808080", w)
		}
	}
}

func TestDCOnlyLuminance(t *testing.T) {
	d := loaded(t)
	// One monochrome macroblock: a single luma block
	// whose only coefficient is the DC value 16,
	// quant scale 1, quant[0] = 8.
	out := runRL(t, d, 1, false, false, []uint16{dcCode(1, 16), eob})
	if len(out) != 16 {
		t.Fatalf("mono 8 bit macroblock words = %d, want 16", len(out))
	}
	// Documented DC path: dequantized DC is
	// 16 * 8 = 128; the IDCT DC gain from the float
	// reference is 1/8, so the uniform sample is 16
	// and the unsigned byte is 144, within the plus
	// or minus 1 rounding tolerance of the integer
	// IDCT path.
	var ref [64]float64
	ref[0] = 128
	want := int(math.Round(floatIDCT(ref)[0])) + 128
	first := int(byte(out[0]))
	if first < want-1 || first > want+1 {
		t.Fatalf("DC byte = %d, want %d plus or minus 1", first, want)
	}
	for wi, w := range out {
		for j := 0; j < 4; j++ {
			if b := int(byte(w >> (8 * j))); b != first {
				t.Fatalf("block not uniform: word %d byte %d = %d, want %d", wi, j, b, first)
			}
		}
	}
}

func TestFullMacroblock15Bit(t *testing.T) {
	d := loaded(t)
	out := runRL(t, d, 3, false, true, solidColorMB(16, 16, 32))
	if len(out) != 128 {
		t.Fatalf("15 bit macroblock words = %d, want 128", len(out))
	}
	r, g, b := solidColorExpected(16, 16, 32)
	var hw uint32 = 1 << 15
	hw |= uint32(b>>3) << 10
	hw |= uint32(g>>3) << 5
	hw |= uint32(r >> 3)
	want := hw | hw<<16
	for _, idx := range []int{0, 1, 31, 32, 64, 127} {
		if out[idx] != want {
			t.Fatalf("15 bit word %d = %08x, want %08x (RGB %d %d %d)", idx, out[idx], want, r, g, b)
		}
	}
	for i, w := range out {
		if w != want {
			t.Fatalf("15 bit macroblock not solid: word %d = %08x, want %08x", i, w, want)
		}
	}
}

func TestACSingleCoefficient(t *testing.T) {
	d := loaded(t)
	// One AC coefficient: scan position 1, run 0,
	// level 16, quant scale 8, quant[1] = 8.
	// Dequantized value (16*8*8+4)/8 = 128 at raster
	// position zagzig[1] = 1.
	out := runRL(t, d, 1, false, false, []uint16{dcCode(8, 0), rlAC(0, 16), eob})
	if len(out) != 16 {
		t.Fatalf("mono words = %d, want 16", len(out))
	}
	var coeff [64]float64
	coeff[1] = 128
	ref := floatIDCT(coeff)
	// Tolerance: plus or minus 2 per sample between
	// the integer IDCT path and the float reference,
	// as recorded in the package documentation.
	for i := 0; i < 64; i++ {
		got := int(byte(out[i/4] >> (8 * (i % 4))))
		want := int(math.Round(ref[i])) + 128
		if got < want-2 || got > want+2 {
			t.Fatalf("sample %d = %d, float reference %d, tolerance plus or minus 2", i, got, want)
		}
	}
}

func Test24BitPacking(t *testing.T) {
	d := loaded(t)
	out := runRL(t, d, 2, false, false, solidColorMB(16, 16, 32))
	if len(out) != 192 {
		t.Fatalf("24 bit macroblock words = %d, want 192", len(out))
	}
	r, g, b := solidColorExpected(16, 16, 32)
	cycle := []uint8{r, g, b}
	// Every byte of the stream must follow the R, G,
	// B per pixel byte order.
	for i := 0; i < 256*3; i++ {
		got := byte(out[i/4] >> (8 * (i % 4)))
		if got != cycle[i%3] {
			t.Fatalf("24 bit byte %d = %d, want %d (RGB %d %d %d)", i, got, cycle[i%3], r, g, b)
		}
	}
	// The first word packs bytes R, G, B, R low byte
	// first, and the second quadrant starts at word
	// 48 with the same alignment (192 bytes per
	// quadrant is a multiple of 3).
	want0 := uint32(r) | uint32(g)<<8 | uint32(b)<<16 | uint32(r)<<24
	if out[0] != want0 {
		t.Fatalf("24 bit word 0 = %08x, want %08x", out[0], want0)
	}
	if out[48] != want0 {
		t.Fatalf("24 bit word 48 = %08x, want %08x", out[48], want0)
	}
}

func TestMono8And4BitModes(t *testing.T) {
	d := loaded(t)
	out8 := runRL(t, d, 1, false, false, []uint16{dcCode(1, 8), eob})
	if len(out8) != 16 {
		t.Fatalf("8 bit mono words = %d, want 16", len(out8))
	}
	// DC 8 with quant[0] 8 gives spatial value 8,
	// unsigned byte 136 (plus or minus 1 rounding).
	for _, w := range out8 {
		for j := 0; j < 4; j++ {
			if b := int(byte(w >> (8 * j))); b < 135 || b > 137 {
				t.Fatalf("8 bit mono byte = %d, want about 136", b)
			}
		}
	}
	out4 := runRL(t, d, 0, false, false, []uint16{dcCode(1, 16), eob})
	if len(out4) != 8 {
		t.Fatalf("4 bit mono words = %d, want 8", len(out4))
	}
	// Gray byte 144 (plus or minus 1) has top nibble
	// 9, so every packed nibble is 9.
	for _, w := range out4 {
		if w != 0x99999999 {
			t.Fatalf("4 bit mono word = %08x, want 99999999", w)
		}
	}
}

func TestFEPaddingIgnored(t *testing.T) {
	clean := runRL(t, loaded(t), 1, false, false, []uint16{dcCode(1, 16), eob})
	padded := runRL(t, loaded(t), 1, false, false,
		[]uint16{eob, dcCode(1, 16), eob, eob, eob})
	if len(clean) != len(padded) {
		t.Fatalf("padding changed output length %d vs %d", len(clean), len(padded))
	}
	for i := range clean {
		if clean[i] != padded[i] {
			t.Fatalf("padding changed word %d: %08x vs %08x", i, clean[i], padded[i])
		}
	}
}

func TestStatusDecode(t *testing.T) {
	d := loaded(t)
	if got := New().Status(); got != 0x80040000 {
		t.Fatalf("status after reset = %08x, want 80040000", got)
	}
	// The data out FIFO empty flag (bit 31) reads set
	// at reset, per the document.
	if d.Status()&(1<<31) == 0 {
		t.Fatalf("data out empty flag not set after reset")
	}
	words := packWords([]uint16{dcCode(1, 16), eob})
	d.WriteWord(decodeCmdWord(1, false, false, false, len(words)))
	if got := d.Status() & 0xFFFF; got != 0 {
		t.Fatalf("remaining field at decode start = %04x, want 0000 (1 word minus 1)", got)
	}
	if d.Status()&(1<<29) == 0 {
		t.Fatalf("decode should read busy while receiving")
	}
	d.WriteWord(words[0])
	if got := d.Status() & 0xFFFF; got != 0xFFFF {
		t.Fatalf("remaining field after final word = %04x, want ffff", got)
	}
	if d.Status()&(1<<29) != 0 {
		t.Fatalf("decode should not read busy after final word")
	}
	out := drain(t, d)
	if len(out) != 16 {
		t.Fatalf("mono macroblock output words = %d, want the documented 16", len(out))
	}
}

func TestControlResetAndUnknownCommands(t *testing.T) {
	d := New()
	d.WriteControl(1<<30 | 1<<29)
	if got := d.Control(); got != 1<<30|1<<29 {
		t.Fatalf("control readback = %08x", got)
	}
	if d.Status()&(1<<28) == 0 {
		t.Fatalf("data in request should read set with DMA in enabled and idle")
	}
	// Command 5 is invalid and acts as MDEC(0): no
	// function, no parameters, and the low bits are
	// reflected into the status low field.
	d.WriteWord(uint32(5)<<29 | 0x1234)
	if got := d.Status() & 0xFFFF; got != 0x1234 {
		t.Fatalf("unknown command reflection = %04x, want 1234", got)
	}
	if d.Status()&(1<<29) != 0 {
		t.Fatalf("unknown command must not be busy")
	}
	// Control reset aborts and restores the reset
	// status value.
	d.WriteWord(decodeCmdWord(1, false, false, false, 4))
	if d.Status()&(1<<29) == 0 {
		t.Fatalf("decode should be busy before abort")
	}
	d.WriteControl(1 << 31)
	if got := d.Status(); got != 0x80040000 {
		t.Fatalf("status after control reset = %08x, want 80040000", got)
	}
}

func TestSerializeMidDecode(t *testing.T) {
	halfs := solidColorMB(16, 16, 32)
	words := packWords(halfs)
	ref := runRL(t, loaded(t), 3, false, true, halfs)

	// Mid decode: serialize after 3 of 6 input
	// words, restore into a fresh decoder (tables
	// come from the serialized state), finish the
	// stream, and compare byte for byte.
	d := loaded(t)
	d.WriteWord(decodeCmdWord(3, false, true, false, len(words)))
	for i := 0; i < 3; i++ {
		d.WriteWord(words[i])
	}
	state := d.Serialize()
	if len(state) != StateSize {
		t.Fatalf("state size = %d, want %d", len(state), StateSize)
	}
	d2 := New()
	if err := d2.Deserialize(state); err != nil {
		t.Fatalf("deserialize: %v", err)
	}
	for i := 3; i < len(words); i++ {
		d2.WriteWord(words[i])
	}
	got := drain(t, d2)
	if len(got) != len(ref) {
		t.Fatalf("restored decode words = %d, want %d", len(got), len(ref))
	}
	for i := range ref {
		if got[i] != ref[i] {
			t.Fatalf("restored decode word %d = %08x, want %08x", i, got[i], ref[i])
		}
	}

	// Output partially drained: serialize after
	// reading 5 output words, restore, and the rest
	// must match the uninterrupted run exactly.
	d3 := loaded(t)
	d3.WriteWord(decodeCmdWord(3, false, true, false, len(words)))
	for _, w := range words {
		d3.WriteWord(w)
	}
	var joined []uint32
	for i := 0; i < 5; i++ {
		joined = append(joined, d3.ReadWord())
	}
	d4 := New()
	if err := d4.Deserialize(d3.Serialize()); err != nil {
		t.Fatalf("deserialize partial drain: %v", err)
	}
	joined = append(joined, drain(t, d4)...)
	if len(joined) != len(ref) {
		t.Fatalf("partial drain total = %d, want %d", len(joined), len(ref))
	}
	for i := range ref {
		if joined[i] != ref[i] {
			t.Fatalf("partial drain word %d = %08x, want %08x", i, joined[i], ref[i])
		}
	}

	if err := New().Deserialize([]byte{1, 2, 3}); err == nil {
		t.Fatalf("short state should fail")
	}
	bad := New().Serialize()
	bad[0] ^= 0xFF
	if err := New().Deserialize(bad); err == nil {
		t.Fatalf("bad magic state should fail")
	}
}

// bitWriter builds a BS bitstream: MSB first inside
// each halfword.
type bitWriter struct {
	halfs []uint16
	cur   uint16
	n     int
}

func (b *bitWriter) bits(v uint32, count int) {
	for i := count - 1; i >= 0; i-- {
		if (v>>uint(i))&1 == 1 {
			b.cur |= 1 << uint(15-b.n)
		}
		b.n++
		if b.n == 16 {
			b.halfs = append(b.halfs, b.cur)
			b.cur = 0
			b.n = 0
		}
	}
}

// pattern writes a code pattern of fixed bits plus
// letter s (sign) and letter x or n (index bits in
// order, most significant first).
func (b *bitWriter) pattern(pat string, sign, idx int) {
	total := 0
	for i := 0; i < len(pat); i++ {
		if pat[i] == 'x' || pat[i] == 'n' {
			total++
		}
	}
	seen := 0
	for i := 0; i < len(pat); i++ {
		switch pat[i] {
		case '0':
			b.bits(0, 1)
		case '1':
			b.bits(1, 1)
		case 's':
			b.bits(uint32(sign), 1)
		case 'x', 'n':
			b.bits(uint32((idx>>uint(total-1-seen))&1), 1)
			seen++
		}
	}
}

// finish pads the final halfword with 1 bits and then
// pads to a whole number of words with FF FFh
// halfwords. The pad loop runs while a partial
// halfword is open (n != 0): bits() resets n to 0
// when the halfword flushes at 16, so a "n < 16"
// condition would never terminate.
func (b *bitWriter) finish() []uint16 {
	for b.n != 0 {
		b.bits(1, 1)
	}
	if len(b.halfs)%2 == 1 {
		b.halfs = append(b.halfs, 0xFFFF)
	}
	return b.halfs
}

func runBS(t *testing.T, d *MDEC, depth int, stp bool, version, quant int, bw *bitWriter) []uint32 {
	t.Helper()
	halfs := bw.finish()
	words := packWords(halfs)
	total := 2 + len(words)
	d.WriteWord(decodeCmdWord(depth, false, stp, true, total))
	d.WriteWord(0x3800<<16 | 0x0010)                 // header: size/4, file ID 3800h
	d.WriteWord(uint32(version)<<16 | uint32(quant)) // header: quant, version
	for _, w := range words {
		d.WriteWord(w)
	}
	return drain(t, d)
}

func TestBSEscapeVersusInTable(t *testing.T) {
	// BS v2, monochrome. DC is a raw 10 bit value.
	// One AC pair (run 0, level 1) encoded in the
	// table as 11s with s = 0, and again via the
	// escape code 000001 followed by the raw 16 bit
	// MDEC code 0001h. Both must decode identically.
	build := func(escape bool) *bitWriter {
		bw := &bitWriter{}
		bw.bits(20, 10) // DC value 20
		if escape {
			bw.bits(0x01, 6)    // 000001 escape
			bw.bits(0x0001, 16) // raw MDEC code
		} else {
			bw.pattern("11s", 0, 0)
		}
		bw.bits(0x2, 2)    // end of block: 10
		bw.bits(0x1FF, 10) // end of frame: 0111111111
		return bw
	}
	a := runBS(t, loaded(t), 1, false, 2, 1, build(false))
	b := runBS(t, loaded(t), 1, false, 2, 1, build(true))
	if len(a) != 16 || len(b) != 16 {
		t.Fatalf("BS mono words = %d and %d, want 16", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("escape decode differs at word %d: %08x vs %08x", i, a[i], b[i])
		}
	}
	// Sanity: DC 20 with quant[0] 8 is spatial 20,
	// so the first byte is about 148.
	if got := int(byte(a[0])); got < 146 || got > 150 {
		t.Fatalf("BS mono first byte = %d, want about 148", got)
	}
}

func TestBSv3DifferentialDC(t *testing.T) {
	// BS v3, 15 bit color. DC values are Huffman
	// offsets against the previous DC of the same
	// component. Per the section 13.20 transcription
	// in bs.go, a category decodes to the signed
	// value (base + index) * 4, so the offsets below
	// accumulate to Cr +128, Cb -64, and Y +96 on Y1,
	// with Y2..Y4 holding there through the zero
	// offset code (which only gives a solid
	// macroblock if the Y accumulator is shared
	// across the four Y blocks, and the Cr and Cb
	// accumulators are separate). NOTE: the *4
	// reconstruction in bs.go is the transcription
	// this package ships; it has not been verified
	// against a real BS v3 stream, and an earlier
	// draft of this fixture assumed unscaled
	// offsets. The category table structure itself
	// (prefixes, sign bit, index bits) is not in
	// question here.
	bw := &bitWriter{}
	bw.pattern("111110snnnnn", 1, 0) // Cr: base 32, index 0, +128 scaled
	bw.bits(0x2, 2)                  // EOB
	bw.pattern("11110snnnn", 0, 0)   // Cb: base 16, index 0, -64 scaled
	bw.bits(0x2, 2)                  // EOB
	bw.pattern("1110snnnn", 1, 8)    // Y1: base 16, index 8, +96 scaled
	bw.bits(0x2, 2)                  // EOB
	for i := 0; i < 3; i++ {
		bw.pattern("100", 0, 0) // Y offset 0
		bw.bits(0x2, 2)         // EOB
	}
	bw.bits(0x3FF, 10) // end of frame: ten 1 bits
	out := runBS(t, loaded(t), 3, true, 3, 1, bw)
	if len(out) != 128 {
		t.Fatalf("BS v3 macroblock words = %d, want 128", len(out))
	}
	r, g, b := refRGB(96, 128, -64)
	want := [3]int{int(b >> 3), int(g >> 3), int(r >> 3)}
	// Per channel tolerance of one 5 bit step: the
	// fixed point IDCT rounds a DC only block to
	// within a step of the exact value (the same
	// tolerance the AC gate uses).
	for i, w := range out {
		for _, hw := range [2]uint32{w, w >> 16} {
			if hw>>15&1 != 1 {
				t.Fatalf("BS v3 word %d half %08x: STP bit not set", i, hw)
			}
			got := [3]int{int(hw >> 10 & 0x1F), int(hw >> 5 & 0x1F), int(hw & 0x1F)}
			for ch := 0; ch < 3; ch++ {
				if got[ch] < want[ch]-1 || got[ch] > want[ch]+1 {
					t.Fatalf("BS v3 word %d channel %d = %d, want %d +/- 1 (RGB %d %d %d)",
						i, ch, got[ch], want[ch], r, g, b)
				}
			}
		}
	}
}

func TestBSSerializeMidStream(t *testing.T) {
	build := func() (*bitWriter, []uint32) {
		bw := &bitWriter{}
		bw.bits(20, 10)
		bw.pattern("11s", 0, 0)
		bw.bits(0x2, 2)
		bw.bits(0x1FF, 10)
		return bw, nil
	}
	bwRef, _ := build()
	ref := runBS(t, loaded(t), 1, false, 2, 1, bwRef)

	bw, _ := build()
	halfs := bw.finish()
	words := packWords(halfs)
	total := 2 + len(words)
	d := loaded(t)
	d.WriteWord(decodeCmdWord(1, false, false, true, total))
	d.WriteWord(0x3800<<16 | 0x0010)
	d.WriteWord(2<<16 | 1)
	d.WriteWord(words[0]) // first bitstream word only
	d2 := New()
	if err := d2.Deserialize(d.Serialize()); err != nil {
		t.Fatalf("deserialize mid BS: %v", err)
	}
	for _, w := range words[1:] {
		d2.WriteWord(w)
	}
	got := drain(t, d2)
	if len(got) != len(ref) {
		t.Fatalf("restored BS words = %d, want %d", len(got), len(ref))
	}
	for i := range ref {
		if got[i] != ref[i] {
			t.Fatalf("restored BS word %d = %08x, want %08x", i, got[i], ref[i])
		}
	}
}
