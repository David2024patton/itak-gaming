package pvr

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/holly"
)

// fakeSink records what the Holly controller presents.
type fakeSink struct {
	raises    [][2]uint32
	withdraws int
}

func (s *fakeSink) RaiseInterrupt(code uint32, level int) {
	s.raises = append(s.raises, [2]uint32{code, uint32(level)})
}

func (s *fakeSink) WithdrawInterrupt() { s.withdraws++ }

func newTestPVR() (*PVR, *holly.Controller, *fakeSink) {
	sink := &fakeSink{}
	hc := holly.New(sink)
	return New(make([]byte, 8<<20), hc), hc, sink
}

func reg(p *PVR, off uint32, v uint32) { p.Write32(pvrBase+off, v) }
func rd(p *PVR, off uint32) uint32     { return p.Read32(pvrBase + off) }

// packet feeds one 32-byte TA parameter (8 words, zero padded).
func packet(p *PVR, words ...uint32) {
	for i := 0; i < 8; i++ {
		var w uint32
		if i < len(words) {
			w = words[i]
		}
		p.TAWrite(w)
	}
}

func f32b(f float32) uint32 { return math.Float32bits(f) }

// programSmallSPG sets a 20 pixel by 10 line SPG with vblank-in
// on line 8 and vblank-out on line 0.
func programSmallSPG(p *PVR) {
	reg(p, RegSPGLoad, 9<<16|19)
	reg(p, RegSPGVBlankInt, 8<<16|0)
}

// cyclesForPixels converts pixels to CPU cycles under the
// default 13.5 MHz pixel clock (27/400 per cycle), rounding up.
func cyclesForPixels(px int) int { return (px*400 + 26) / 27 }

func TestDefaults(t *testing.T) {
	p, _, _ := newTestPVR()
	if got := rd(p, RegSPGLoad); got != 262<<16|857 {
		t.Errorf("SPG_LOAD default = %#x, want NTSC 858x263", got)
	}
	if p.PixelsPerLine() != 858 || p.LinesPerField() != 263 {
		t.Errorf("totals = %dx%d, want 858x263", p.PixelsPerLine(), p.LinesPerField())
	}
	// 858 pixels at 13.5 MHz: 200e6 * 858 / 13.5e6 = 12711.1
	if got := p.CyclesPerLine(); got != 12712 {
		t.Errorf("CyclesPerLine = %d, want 12712", got)
	}
}

func TestSPGVBlankWindowAndCounter(t *testing.T) {
	p, hc, sink := newTestPVR()
	programSmallSPG(p)
	// Deliver vblank-in at level 6.
	reg(p, 0, 0) // no-op sanity
	hc.Write32(0x005F6930, 1<<holly.EvtVBlankIn)

	// Advance to line 8 (160 pixels): vblank-in fires exactly
	// once and the status bit reads back.
	p.Advance(cyclesForPixels(160))
	if !p.InVBlank() {
		t.Fatal("not in vblank at line 8")
	}
	if p.SPGLine() != 8 {
		t.Errorf("line = %d, want 8", p.SPGLine())
	}
	if got := hc.Status(holly.BankNormal); got != 1<<holly.EvtVBlankIn {
		t.Errorf("holly status = %#x, want vblank-in only", got)
	}
	if len(sink.raises) != 1 || sink.raises[0] != [2]uint32{0x320, 6} {
		t.Errorf("raises = %v, want one (0x320, 6)", sink.raises)
	}
	if got := rd(p, RegSPGStatus); got&(1<<11) == 0 || got&0x3FF != 8 {
		t.Errorf("SPG_STATUS = %#x, want line 8 with vblank bit", got)
	}
	// Acknowledge, then run four more fields: exactly four more
	// vblank-in deliveries, one per field, and vblank-out fired
	// on every wrap (its bit stays set until acked too).
	hc.Write32(0x005F6900, 1<<holly.EvtVBlankIn)
	for f := 0; f < 4; f++ {
		p.Advance(cyclesForPixels(200))
		if got := hc.Status(holly.BankNormal); got&(1<<holly.EvtVBlankOut) == 0 {
			t.Errorf("field %d: vblank-out bit not set", f)
		}
		hc.Write32(0x005F6900, 1<<holly.EvtVBlankIn)
	}
	if got := p.SPGFrames(); got != 4 {
		t.Errorf("frames = %d, want 4", got)
	}
	if len(sink.raises) != 5 {
		t.Errorf("raises = %d, want 5 (one per field)", len(sink.raises))
	}
	// Without the ack the source stays pending (the handler that
	// never clears ISTNRM would be re-entered on hardware).
	p.Advance(cyclesForPixels(200))
	if got := hc.Status(holly.BankNormal); got&(1<<holly.EvtVBlankIn) == 0 {
		t.Error("vblank-in bit cleared without an ack")
	}
}

func TestSPGFieldFlipInterlace(t *testing.T) {
	p, _, _ := newTestPVR()
	programSmallSPG(p)
	reg(p, RegSPGControl, spgInterlace)
	if p.SPGField() != 0 {
		t.Fatal("field starts at 1")
	}
	p.Advance(cyclesForPixels(200))
	if p.SPGField() != 1 {
		t.Errorf("field after one field = %d, want 1", p.SPGField())
	}
	if got := rd(p, RegSPGStatus); got&(1<<12) == 0 {
		t.Errorf("SPG_STATUS = %#x, want field bit set", got)
	}
	p.Advance(cyclesForPixels(200))
	if p.SPGField() != 0 {
		t.Errorf("field after two fields = %d, want 0", p.SPGField())
	}
	// Progressive mode never flips.
	p2, _, _ := newTestPVR()
	programSmallSPG(p2)
	p2.Advance(cyclesForPixels(400))
	if p2.SPGField() != 0 {
		t.Errorf("progressive field = %d, want 0", p2.SPGField())
	}
}

func TestSPGHBlankEventsAndStatus(t *testing.T) {
	p, hc, _ := newTestPVR()
	programSmallSPG(p)
	reg(p, RegSPGHBlankInt, 5)                 // fire at pixel 5 of each line
	reg(p, RegSPGHBlank, 19<<16|10)            // status window pixels 10..19
	hc.Write32(0x005F6910, 1<<holly.EvtHBlank) // level 2 masks

	count := 0
	for line := 0; line < 3; line++ {
		p.Advance(cyclesForPixels(20))
		if hc.Status(holly.BankNormal)&(1<<holly.EvtHBlank) != 0 {
			count++
			hc.Write32(0x005F6900, 1<<holly.EvtHBlank)
		}
	}
	if count != 3 {
		t.Errorf("hblank events for 3 lines = %d, want 3", count)
	}
	// Status: mid-line the hblank window bit reads set at a pixel
	// inside 10..19, clear before it.
	p2, _, _ := newTestPVR()
	programSmallSPG(p2)
	reg(p2, RegSPGHBlank, 19<<16|10)
	p2.Advance(cyclesForPixels(12))
	if got := rd(p2, RegSPGStatus); got&(1<<10) == 0 {
		t.Errorf("SPG_STATUS = %#x, want hblank window bit at pixel 12", got)
	}
	// The disable bit stops the event.
	p3, hc3, _ := newTestPVR()
	programSmallSPG(p3)
	reg(p3, RegSPGHBlankInt, 5|(1<<31))
	p3.Advance(cyclesForPixels(60))
	if hc3.Status(holly.BankNormal)&(1<<holly.EvtHBlank) != 0 {
		t.Error("hblank event fired while disabled")
	}
}

func TestRegisterWindowStorage(t *testing.T) {
	p, _, _ := newTestPVR()
	reg(p, RegTextControl, 0x80000000|0x1234)
	if got := rd(p, RegTextControl); got != 0x80001234 {
		t.Errorf("TEXT_CONTROL = %#x", got)
	}
	reg(p, RegPALRamCtrl, 2)
	if got := rd(p, RegPALRamCtrl); got != 2 {
		t.Errorf("PAL_RAM_CTRL = %#x, want 2", got)
	}
	// Palette RAM lives at +0x400.
	p.Write32(pvrBase+0x400+4*17, 0xAABBCCDD)
	if got := p.Read32(pvrBase + 0x400 + 4*17); got != 0xAABBCCDD {
		t.Errorf("palette[17] = %#x", got)
	}
	if p.palette[17] != 0xAABBCCDD {
		t.Errorf("palette backing = %#x", p.palette[17])
	}
	// Fog table at +0x200 holds 16-bit entries.
	p.Write32(pvrBase+0x200, 0x11223344)
	if p.fogTable[0] != 0x3344 || p.fogTable[1] != 0x1122 {
		t.Errorf("fog table = %#x %#x", p.fogTable[0], p.fogTable[1])
	}
	if got := p.Read32(pvrBase + 0x200); got != 0x11223344 {
		t.Errorf("fog readback = %#x", got)
	}
	// SPG_STATUS ignores writes.
	before := rd(p, RegSPGStatus)
	p.Write32(pvrBase+RegSPGStatus, 0xFFFFFFFF)
	if got := rd(p, RegSPGStatus); got != before {
		t.Errorf("SPG_STATUS changed by a write: %#x -> %#x", before, got)
	}
}

func TestTAParserBuildsPrimAndISPBlock(t *testing.T) {
	p, hc, _ := newTestPVR()
	reg(p, RegTAISPBase, 0x1000)
	reg(p, RegTAListInit, 0)
	hdr := []uint32{
		(4 << 29) | (ListOpaque << 24), // polygon header, opaque
		uint32(DepthAlways),            // ISP/TSP: depth always
		0, 0,                           // TSP, TCW
		0xFF112233, 0, // base color, offset
		0, 0,
	}
	packet(p, hdr...)
	vtx := func(x, y float32, eos bool) []uint32 {
		pcw := uint32(7 << 29)
		if eos {
			pcw |= pcwEOS
		}
		return []uint32{pcw, f32b(x), f32b(y), f32b(0.5), 0, 0, 0xFFFFFFFF, 0, 0, 0, 0, 0, 0, 0, 0, 0}[:8]
	}
	packet(p, vtx(0, 0, false)...)
	packet(p, vtx(32, 0, false)...)
	packet(p, vtx(0, 32, false)...)
	packet(p, vtx(32, 32, true)...)
	if len(p.ta.prims[ListOpaque]) != 1 {
		t.Fatalf("opaque prims = %d, want 1", len(p.ta.prims[ListOpaque]))
	}
	pr := p.ta.prims[ListOpaque][0]
	if len(pr.Verts) != 4 || pr.BaseColor != 0xFF112233 {
		t.Errorf("prim = %+v", pr)
	}
	if pr.Verts[2].X != 0 || pr.Verts[2].Y != 32 || !pr.Verts[3].EOS {
		t.Errorf("verts = %+v", pr.Verts)
	}
	// The ISP parameter block landed in VRAM at TA_ISP_BASE.
	if got := p.vram32(0x1000); got != hdr[0] {
		t.Errorf("ISP block PCW = %#x, want %#x", got, hdr[0])
	}
	if got := rd(p, RegTAITPCurrent); got != 0x1000+36 {
		t.Errorf("TA_ITP_CURRENT = %#x, want %#x", got, 0x1000+36)
	}
	// End of list fires the opaque-done event.
	packet(p, 0, 0, 0, 0, 0, 0, 0, 0)
	if got := hc.Status(holly.BankNormal); got&(1<<holly.EvtOpaqueDone) == 0 {
		t.Errorf("holly status = %#x, want opaque-done set", got)
	}
}

func TestTAParserErrorsAndClip(t *testing.T) {
	p, hc, _ := newTestPVR()
	reg(p, RegTAListInit, 0)
	// A vertex with no header raises the TA input error.
	packet(p, 7<<29, 0, 0, 0, 0, 0, 0, 0)
	if got := hc.Status(holly.BankError); got&(1<<holly.ErrTAInputError) == 0 {
		t.Errorf("error status = %#x, want TA input error", got)
	}
	// User tile clip packet: word 1 packs x0, y0, x1, y1.
	packet(p, 1<<29, 1|2<<8|30<<16|40<<24, 0, 0, 0, 0, 0, 0)
	if p.ta.clip != [4]uint8{1, 2, 30, 40} {
		t.Errorf("clip = %v", p.ta.clip)
	}
	// Control packet with the endian flip set: a byte-swapped
	// header parses as written.
	packet(p, 2<<29, 1, 0, 0, 0, 0, 0, 0)
	swap := func(v uint32) uint32 { return bitsReverseBytes(v) }
	hdr := []uint32{(4 << 29) | (ListTrans << 24), uint32(DepthAlways), 0, 0, 0xFF0000FF, 0, 0, 0}
	sw := make([]uint32, 8)
	for i, w := range hdr {
		sw[i] = swap(w)
	}
	packet(p, sw...)
	if !p.ta.hdrValid || p.ta.hdr.List != ListTrans {
		t.Errorf("swapped header not parsed: valid=%v list=%d", p.ta.hdrValid, p.ta.hdr.List)
	}
}

func bitsReverseBytes(v uint32) uint32 {
	return v<<24 | (v&0xFF00)<<8 | (v>>8)&0xFF00 | v>>24
}

func TestYUVConverter(t *testing.T) {
	p, hc, _ := newTestPVR()
	reg(p, RegTAYUVTexBase, 0x2000)
	reg(p, RegTAYUVTexCtrl, 1<<16|1) // 1x1 macroblocks: 16x16 texels
	// One macroblock: U = V = 128 (neutral chroma), Y = 128,
	// fed as 96 words of 0x80808080.
	for i := 0; i < 96; i++ {
		p.YUVWrite(128 | 128<<8 | 128<<16 | 128<<24)
	}
	if got := hc.Status(holly.BankNormal); got&(1<<holly.EvtYUVDone) == 0 {
		t.Errorf("holly status = %#x, want YUV done", got)
	}
	r, g, b := yuvToRGB(128, 128, 128)
	want := uint16(uint32(r>>3)<<11 | uint32(g>>2)<<5 | uint32(b>>3))
	for _, xy := range [][2]int{{0, 0}, {15, 15}, {3, 9}} {
		idx := twiddleIndex(xy[0], xy[1], 16, 16)
		got := binary.LittleEndian.Uint16(p.vram[0x2000+idx*2:])
		if got != want {
			t.Errorf("texel %v = %#04x, want %#04x", xy, got, want)
		}
	}
	if got := rd(p, RegTAYUVTexCnt); got != 0 {
		t.Errorf("YUV count after completion = %d, want 0 (reset)", got)
	}
}

func TestPVRSerializeRoundTrip(t *testing.T) {
	p, _, _ := newTestPVR()
	programSmallSPG(p)
	p.Advance(cyclesForPixels(37)) // beam mid-line
	reg(p, RegTAISPBase, 0x1000)
	reg(p, RegTAListInit, 0)
	packet(p, (4<<29)|(ListOpaque<<24), uint32(DepthAlways), 0, 0, 0xFF00FF00, 0, 0, 0)
	packet(p, 7<<29, f32b(0), f32b(0), f32b(0.5), 0, 0, 0xFFFFFFFF, 0)
	packet(p, 7<<29|pcwEOS, f32b(10), f32b(0), f32b(0.5), 0, 0, 0xFFFFFFFF, 0)
	// A second prim left open mid-strip.
	packet(p, (4<<29)|(ListTrans<<24), uint32(DepthGreater), 0, 0, 0xFFFF0000, 0, 0, 0)
	packet(p, 7<<29, f32b(1), f32b(2), f32b(0.25), 0, 0, 0xFF123456, 0)
	p.palette[5] = 0xDEADBEEF

	b := p.Serialize()
	if len(b) != p.SerializeSize() {
		t.Fatalf("len = %d, SerializeSize = %d", len(b), p.SerializeSize())
	}
	if got := SerializedSize(b); got != len(b) {
		t.Fatalf("SerializedSize = %d, want %d", got, len(b))
	}
	p2, _, _ := newTestPVR()
	if !p2.Deserialize(b) {
		t.Fatal("Deserialize failed")
	}
	if got := p2.Serialize(); !bytes.Equal(got, b) {
		t.Error("round trip state differs")
	}
	if len(p2.ta.prims[ListTrans]) != 1 || len(p2.ta.prims[ListTrans][0].Verts) != 1 {
		t.Error("open prim not restored")
	}
	if p2.ta.open != 0 {
		t.Errorf("open index = %d, want 0", p2.ta.open)
	}
	if p2.Deserialize(b[:len(b)-1]) {
		t.Error("Deserialize accepted a truncated state")
	}
}
