package rdp

import (
	"testing"
)

// newTestRDP returns an RDP with 4 MB of RDRAM.
func newTestRDP() (*RDP, []byte) {
	dram := make([]byte, 4<<20)
	r := New()
	r.AttachDRAM(dram)
	return r, dram
}

func cmdWord(op int, rest uint64) uint64 {
	return uint64(op)<<56 | rest
}

// runList writes words into RDRAM at base and triggers
// execution through the DP registers.
func runList(r *RDP, dram []byte, base uint32, words []uint64) {
	for i, w := range words {
		for j := 0; j < 8; j++ {
			dram[base+uint32(i*8+j)] = byte(w >> uint(56-8*j))
		}
	}
	r.WriteRegister(RegStart, base)
	r.WriteRegister(RegEnd, base+uint32(len(words)*8))
}

// omWord builds an other-modes value with the given
// cycle type and extra low bits.
func omWord(cycleType int, extra uint64) uint64 {
	return uint64(cycleType)<<52 | extra
}

// combineWord builds a combine register value with the
// same muxes in both cycles.
func combineWord(subAR, mulR, subBR, addR, subAA, mulA, subBA, addA int) uint64 {
	var w uint64
	w |= uint64(subAR) << 52
	w |= uint64(mulR) << 47
	w |= uint64(subAA) << 44
	w |= uint64(mulA) << 41
	w |= uint64(subBR) << 28
	w |= uint64(addR) << 15
	w |= uint64(subBA) << 12
	w |= uint64(addA) << 9
	w |= uint64(subAR) << 37
	w |= uint64(mulR) << 32
	w |= uint64(subAA) << 21
	w |= uint64(mulA) << 18
	w |= uint64(subBR) << 24
	w |= uint64(addR) << 6
	w |= uint64(subBA) << 3
	w |= uint64(addA)
	return w
}

func TestFillRectViaList(t *testing.T) {
	r, dram := newTestRDP()
	// Color image 64x64 RGBA16 at 0x10000.
	setImg := uint64(FmtRGBA)<<53 | uint64(Size16)<<51 |
		uint64(63)<<32 | 0x10000
	// Fill color: pixels pair (red, green).
	fill := cmdWord(CmdSetFillColor, 0xF80107C1)
	// Fill rect: XL=63.0 YL=63.0 XH=0 YH=0 (10.2 fields).
	fr := cmdWord(CmdFillRect,
		uint64(63*4)<<44|uint64(63*4)<<32|uint64(0)<<12|0)
	words := []uint64{
		cmdWord(CmdSetColorImg, setImg),
		cmdWord(CmdSetOtherMode, omWord(3, 0)), // fill cycle
		fill,
		fr,
	}
	runList(r, dram, 0x8000, words)
	// Pixel (0,0): even x takes the high half (red).
	if v := r.dramRead16(0x10000); v != 0xF801 {
		t.Fatalf("pixel (0,0) = %#04x, want 0xF801", v)
	}
	// Pixel (1,0): low half (green).
	if v := r.dramRead16(0x10002); v != 0x07C1 {
		t.Fatalf("pixel (1,0) = %#04x, want 0x07C1", v)
	}
	// Pixel (63,63) is included in fill mode.
	off := uint32(0x10000 + (63*64+63)*2)
	if v := r.dramRead16(off); v != 0x07C1 {
		t.Fatalf("pixel (63,63) = %#04x, want 0x07C1", v)
	}
	// DP status: start and end valid set, clock counted.
	st := r.ReadRegister(RegStatus)
	if st&StatusStartVal == 0 || st&StatusEndValid == 0 {
		t.Fatalf("status %#x missing valid bits", st)
	}
	if got := r.ReadRegister(RegClock); got != 4 {
		t.Fatalf("clock = %d, want 4", got)
	}
	if got := r.ReadRegister(RegCurrent); got != 0x8000+32 {
		t.Fatalf("current = %#x, want end", got)
	}
}

func TestFreezeDefersExecution(t *testing.T) {
	r, dram := newTestRDP()
	r.WriteRegister(RegStatus, 1<<3) // set freeze
	words := []uint64{
		cmdWord(CmdSetColorImg, uint64(FmtRGBA)<<53|uint64(Size16)<<51|uint64(63)<<32|0x10000),
		cmdWord(CmdSetOtherMode, omWord(3, 0)),
		cmdWord(CmdSetFillColor, 0xFFFF0000),
		cmdWord(CmdFillRect, uint64(10*4)<<44|uint64(10*4)<<32),
	}
	for i, w := range words {
		for j := 0; j < 8; j++ {
			dram[0x8000+i*8+j] = byte(w >> uint(56-8*j))
		}
	}
	r.WriteRegister(RegStart, 0x8000)
	r.WriteRegister(RegEnd, 0x8000+32)
	if v := r.dramRead16(0x10000); v != 0 {
		t.Fatalf("frozen list executed: %#04x", v)
	}
	r.WriteRegister(RegStatus, 1<<2) // clear freeze
	if v := r.dramRead16(0x10000); v != 0xFFFF {
		t.Fatalf("after unfreeze pixel = %#04x, want 0xFFFF", v)
	}
}

func TestFullSyncInterrupt(t *testing.T) {
	r, dram := newTestRDP()
	fired := 0
	r.OnInterrupt = func(active bool) {
		if active {
			fired++
		}
	}
	runList(r, dram, 0x8000, []uint64{
		cmdWord(CmdNoOp, 0),
		cmdWord(CmdSyncPipe, 0),
		cmdWord(CmdSyncFull, 0),
	})
	if fired != 1 {
		t.Fatalf("FULLSYNC fired %d times, want 1", fired)
	}
}

func TestFillPacking8And32(t *testing.T) {
	r, dram := newTestRDP()
	// 8-bit image at 0x20000, width 16.
	runList(r, dram, 0x8000, []uint64{
		cmdWord(CmdSetColorImg, uint64(FmtI)<<53|uint64(Size8)<<51|uint64(15)<<32|0x20000),
		cmdWord(CmdSetOtherMode, omWord(3, 0)),
		cmdWord(CmdSetFillColor, 0x11223344),
		cmdWord(CmdFillRect, uint64(3*4)<<44|uint64(0)<<32),
	})
	want := []byte{0x11, 0x22, 0x33, 0x44}
	for i, wb := range want {
		if dram[0x20000+i] != wb {
			t.Fatalf("8-bit fill pixel %d = %#02x, want %#02x", i, dram[0x20000+i], wb)
		}
	}
	// 32-bit image at 0x30000, width 16.
	runList(r, dram, 0x8000, []uint64{
		cmdWord(CmdSetColorImg, uint64(FmtRGBA)<<53|uint64(Size32)<<51|uint64(15)<<32|0x30000),
		cmdWord(CmdSetFillColor, 0xA1B2C3D4),
		cmdWord(CmdFillRect, uint64(1*4)<<44|uint64(0)<<32),
	})
	if v := r.dramRead32(0x30000); v != 0xA1B2C3D4 {
		t.Fatalf("32-bit fill pixel = %#08x, want 0xA1B2C3D4", v)
	}
	if v := r.dramRead32(0x30004); v != 0xA1B2C3D4 {
		t.Fatalf("32-bit fill pixel 1 = %#08x", v)
	}
}

// texImage4x4 plants a 4x4 RGBA16 texture image at
// 0x40000 in the texture image registers' target and
// returns the distinct texel values.
func texImage4x4(r *RDP) {
	// Texture image: RGBA16, width 4, at 0x40000.
	vals := [16]uint16{
		0xF801, 0x07C1, 0x003F, 0xFFFF,
		0x0001, 0x801F, 0x7C01, 0x03E1,
		0x1234, 0x2345, 0x3456, 0x4567,
		0x5678, 0x6789, 0x789A, 0x89AB,
	}
	for i, v := range vals {
		r.dramWrite16(0x40000+uint32(i*2), v)
	}
	r.texImg = Image{Format: FmtRGBA, Size: Size16, Width: 4, Addr: 0x40000}
}

func TestLoadBlockMatchesLoadTile(t *testing.T) {
	ra, _ := newTestRDP()
	texImage4x4(ra)
	// Tile 0: line 1 word (8 bytes per line of 4 texels).
	ra.tiles[0] = Tile{Format: FmtRGBA, Size: Size16, Line: 1, TMem: 0}
	ra.tiles[0].SL, ra.tiles[0].TL, ra.tiles[0].SH = 0, 0, 15
	ra.tiles[0].TH = 3 * 4
	ra.loadBlock(0, 2048) // dxt: one word per source line

	rb, _ := newTestRDP()
	texImage4x4(rb)
	rb.tiles[0] = Tile{Format: FmtRGBA, Size: Size16, Line: 1, TMem: 0}
	rb.tiles[0].SL, rb.tiles[0].TL = 0, 0
	rb.tiles[0].SH, rb.tiles[0].TH = 3*4, 3*4 // 10.2
	rb.loadTile(0)

	for i := 0; i < 32; i++ {
		if ra.tmem[i] != rb.tmem[i] {
			t.Fatalf("TMEM byte %d differs: block %#02x tile %#02x",
				i, ra.tmem[i], rb.tmem[i])
		}
	}
	// And the sampled texel (2,1) matches the source.
	got := ra.fetchTexel(0, 2, 1)
	want := expandRGBA16(0x7C01)
	if got != want {
		t.Fatalf("texel (2,1) = %+v, want %+v", got, want)
	}
}

func TestCombinerArithmetic(t *testing.T) {
	// (A - B) * C + D with floor on the scaled product.
	if v := combChan(200, 50, 128, 10); v != 85 {
		// (150 * 128) >> 8 = 75, + 10 = 85.
		t.Fatalf("combChan = %d, want 85", v)
	}
	if v := combChan(0, 255, 255, 0); v != 0 {
		t.Fatalf("combChan negative clamp = %d, want 0", v)
	}
	if v := combChan(255, 0, 255, 255); v != 255 {
		t.Fatalf("combChan high clamp = %d, want 255", v)
	}
	if v := combChan(255, 0, 255, 0); v != 254 {
		// The documented /256 scaling: 255*255>>8 = 254.
		t.Fatalf("combChan 255*ONE = %d, want 254", v)
	}
}

func TestBlenderEquation(t *testing.T) {
	r, _ := newTestRDP()
	// Cycle 0 blender: P = pixel, M = memory,
	// A = pixel alpha, B = 1 - A.
	var om uint64
	om |= 0 << 30 // b_m1a_0 = pixel
	om |= 1 << 26 // b_m1b_0 = memory
	om |= 0 << 22 // b_m2a_0 = pixel alpha
	om |= 0 << 18 // b_m2b_0 = 1 - A
	r.otherModes = om
	out := r.blendPass(0, RGBA{200, 100, 50, 255}, 128,
		RGBA{40, 60, 80, 255}, 8, &frag{})
	// (200*128 + 40*127)/255 = 120 (truncated).
	if out.R != 120 {
		t.Fatalf("blend R = %d, want 120", out.R)
	}
	// (100*128 + 60*127)/255 = 80.
	if out.G != 80 {
		t.Fatalf("blend G = %d, want 80", out.G)
	}
	// (50*128 + 80*127)/255 = 64.
	if out.B != 64 {
		t.Fatalf("blend B = %d, want 64", out.B)
	}
}

func TestScissorClipsFill(t *testing.T) {
	r, dram := newTestRDP()
	runList(r, dram, 0x8000, []uint64{
		cmdWord(CmdSetColorImg, uint64(FmtRGBA)<<53|uint64(Size16)<<51|uint64(63)<<32|0x10000),
		cmdWord(CmdSetOtherMode, omWord(3, 0)),
		// Scissor: XH=10, YH=10, XL=20, YL=20 (10.2).
		cmdWord(CmdSetScissor, uint64(40)<<44|uint64(40)<<32|uint64(80)<<12|80),
		cmdWord(CmdSetFillColor, 0x12341234),
		cmdWord(CmdFillRect, uint64(63*4)<<44|uint64(63*4)<<32),
	})
	if v := r.dramRead16(0x10000); v != 0 {
		t.Fatalf("outside scissor written: %#04x", v)
	}
	off := uint32(0x10000 + (15*64+15)*2)
	if v := r.dramRead16(off); v != 0x1234 {
		t.Fatalf("inside scissor = %#04x, want 0x1234", v)
	}
	off = uint32(0x10000 + (20*64+20)*2)
	if v := r.dramRead16(off); v != 0 {
		t.Fatalf("scissor lower-right (exclusive) written: %#04x", v)
	}
}

func TestCI4TLUTAndFormats(t *testing.T) {
	r, _ := newTestRDP()
	// TLUT in the texture image: 16 RGBA16 entries at
	// 0x50000; entry 3 is pure red.
	for i := 0; i < 16; i++ {
		r.dramWrite16(0x50000+uint32(i*2), 0x0000)
	}
	r.dramWrite16(0x50000+6, 0xF801)
	r.texImg = Image{Format: FmtRGBA, Size: Size16, Width: 16, Addr: 0x50000}
	r.tiles[7] = Tile{Format: FmtRGBA, Size: Size16, Line: 1, TMem: 256}
	r.tiles[7].SL, r.tiles[7].TL = 0, 0
	r.tiles[7].SH = 15 * 4
	r.loadTLUT(7)
	// Enable TLUT (bit 47) and fetch a CI4 texel with
	// index 3 through tile 0.
	r.otherModes = 1 << 47
	r.tiles[0] = Tile{Format: FmtCI, Size: Size4, Line: 1, TMem: 0}
	r.tiles[0].SH, r.tiles[0].TH = 3*4, 0
	r.writeTMEMRaw(&r.tiles[0], 0, 0, 3)
	got := r.fetchTexel(0, 0, 0)
	if got != (RGBA{255, 0, 0, 255}) {
		t.Fatalf("CI4 TLUT texel = %+v, want red", got)
	}
	// IA8: intensity high nibble, alpha low nibble.
	r.tiles[1] = Tile{Format: FmtIA, Size: Size8, Line: 1, TMem: 32}
	r.tiles[1].SH, r.tiles[1].TH = 3*4, 0
	r.writeTMEMRaw(&r.tiles[1], 0, 0, 0xAB)
	got = r.fetchTexel(1, 0, 0)
	if got != (RGBA{170, 170, 170, 187}) {
		t.Fatalf("IA8 texel = %+v, want (170,170,170,187)", got)
	}
	// I4: nibble * 17.
	r.tiles[2] = Tile{Format: FmtI, Size: Size4, Line: 1, TMem: 64}
	r.tiles[2].SH, r.tiles[2].TH = 3*4, 0
	r.writeTMEMRaw(&r.tiles[2], 0, 0, 9)
	got = r.fetchTexel(2, 0, 0)
	if got != (RGBA{153, 153, 153, 255}) {
		t.Fatalf("I4 texel = %+v, want 153 gray", got)
	}
}

func TestYUVConversion(t *testing.T) {
	r, _ := newTestRDP()
	// Documented typical convert coefficients.
	r.otherModes = 0 // TLUT off
	conv := []int32{175, -43, -89, 222, 114, 42}
	var w uint64
	for i := 0; i < 6; i++ {
		w |= uint64(uint32(conv[i])&0x1FF) << uint(45-9*i)
	}
	r.dispatchState(CmdSetConvert, cmdWord(CmdSetConvert, w))
	// YUV red: Y=76, U=85, V=255 in a 16-bit pair.
	r.tiles[0] = Tile{Format: FmtYUV, Size: Size16, Line: 1, TMem: 0}
	r.tiles[0].SH, r.tiles[0].TH = 3*4, 0
	r.writeTMEMRaw(&r.tiles[0], 0, 0, uint32(76)<<8|85)
	r.writeTMEMRaw(&r.tiles[0], 1, 0, uint32(76)<<8|255)
	got := r.fetchTexel(0, 0, 0)
	if got != (RGBA{255, 0, 0, 255}) {
		t.Fatalf("YUV red = %+v, want (255,0,0,255)", got)
	}
}

func TestThreeTexelFilter(t *testing.T) {
	r, _ := newTestRDP()
	// 2x2 mode on, bilerp path requested by the caller.
	r.otherModes = 1 << 45 // sample_type = 2x2
	r.tiles[0] = Tile{Format: FmtRGBA, Size: Size32, Line: 1, TMem: 0}
	r.tiles[0].SH, r.tiles[0].TH = 4*4, 4*4 // clamp range
	// RGBA32 texels: (0,0) black, (1,0) R=64,
	// (0,1) G=128, (1,1) white.
	r.writeTMEMRaw(&r.tiles[0], 0, 0, 0x000000FF)
	r.writeTMEMRaw(&r.tiles[0], 1, 0, 0x400000FF)
	r.writeTMEMRaw(&r.tiles[0], 0, 1, 0x008000FF)
	r.writeTMEMRaw(&r.tiles[0], 1, 1, 0xFFFFFFFF)
	// Half-texel position: fx = fy = 16 (of 32):
	// weights (0, 16, 16, 0) over the diagonal split.
	got := r.fetchFiltered(0, 16, 16, true)
	want := RGBA{32, 64, 0, 255}
	if got != want {
		t.Fatalf("triangle filter = %+v, want %+v", got, want)
	}
	// Point mode at the same position returns (0,0).
	got = r.fetchFiltered(0, 16, 16, false)
	if got != (RGBA{0, 0, 0, 255}) {
		t.Fatalf("point sample = %+v, want black", got)
	}
}

func TestPrimDepthAndZ(t *testing.T) {
	r, dram := newTestRDP()
	// Clear the z image (at 0x18000) to max depth by
	// filling it through the color image register.
	runList(r, dram, 0x8000, []uint64{
		cmdWord(CmdSetZImage, 0x18000),
		cmdWord(CmdSetColorImg, uint64(FmtRGBA)<<53|uint64(Size16)<<51|uint64(63)<<32|0x18000),
		cmdWord(CmdSetOtherMode, omWord(3, 0)),
		cmdWord(CmdSetFillColor, 0xFFFFFFFF),
		cmdWord(CmdFillRect, uint64(63*4)<<44|uint64(63*4)<<32),
		cmdWord(CmdSetColorImg, uint64(FmtRGBA)<<53|uint64(Size16)<<51|uint64(63)<<32|0x10000),
	})
	// A 1x1 white texture in tile 0.
	r.tiles[0] = Tile{Format: FmtRGBA, Size: Size16, Line: 1, TMem: 0}
	r.tiles[0].SH, r.tiles[0].TH = 0, 0
	r.writeTMEMRaw(&r.tiles[0], 0, 0, 0xFFFF)
	// 1-cycle, z compare + update on, z source = prim.
	om := omWord(0, (1<<4)|(1<<5)|(1<<2))
	// TEXRECT x 8..24, y 8..24 (lower right exclusive),
	// S = T = 0, DsDx = DtDy = 0 (the 1x1 clamps).
	rect := []uint64{
		cmdWord(CmdTexRect, uint64(8*4)<<44|uint64(8*4)<<32|
			uint64(24*4)<<12|24*4),
		0,
	}
	words := []uint64{
		cmdWord(CmdSetOtherMode, om),
		cmdWord(CmdSetCombine, combineWord(1, 16, 8, 1, 1, 7, 7, 1)),
		cmdWord(CmdSetPrimDepth, 0x8000<<16),
	}
	words = append(words, rect...)
	words = append(words, cmdWord(CmdSetPrimDepth, 0xC000<<16))
	words = append(words, rect...)
	runList(r, dram, 0x8000, words)
	// Pixel (12,12): the near rect (z 0x8000) drew
	// white; the farther rect (z 0xC000) lost the test.
	off := uint32(0x10000 + (12*64+12)*2)
	if v := r.dramRead16(off); v != 0xFFFF {
		t.Fatalf("prim-depth pixel = %#04x, want 0xFFFF (near wins)", v)
	}
	// Stored z at that pixel: z18 = primZ << 2.
	z, _ := r.readZ(12, 12)
	if z != 0x20000 {
		t.Fatalf("stored z = %#x, want 0x20000", z)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	r, dram := newTestRDP()
	runList(r, dram, 0x8000, []uint64{
		cmdWord(CmdSetColorImg, uint64(FmtRGBA)<<53|uint64(Size16)<<51|uint64(63)<<32|0x10000),
		cmdWord(CmdSetOtherMode, omWord(1, 0)),
		cmdWord(CmdSetPrimColor, 0x30<<40|0x40<<32|0x11223344),
		cmdWord(CmdSetTile, 0),
	})
	buf := make([]byte, r.SerializeSize())
	n := r.Serialize(buf)
	if n != r.SerializeSize() {
		t.Fatalf("Serialize wrote %d, size says %d", n, r.SerializeSize())
	}
	r2, _ := newTestRDP()
	if err := r2.Deserialize(buf); err != nil {
		t.Fatal(err)
	}
	if r2.colorImg != r.colorImg || r2.primColor != r.primColor {
		t.Fatalf("round trip mismatch: %+v vs %+v", r2.colorImg, r.colorImg)
	}
	buf2 := make([]byte, r2.SerializeSize())
	r2.Serialize(buf2)
	for i := range buf {
		if buf[i] != buf2[i] {
			t.Fatalf("state byte %d differs after round trip", i)
		}
	}
	if err := r2.Deserialize(buf[:10]); err == nil {
		t.Fatal("short state accepted")
	}
}
