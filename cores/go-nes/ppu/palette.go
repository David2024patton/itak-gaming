package ppu

// The NTSC palette: the 64 RGB colors the PPU's composite
// output produces for its 64 palette entries, as published in
// the standard references (the NESdev wiki's palette table, the
// values most emulators ship as their default). The PPU itself
// stores only 6-bit color indices; this table is the fixed
// translation the core applies when it produces the XRGB8888
// framebuffer, so games see the same colors on any host.
var ntscPalette = [64][3]uint8{
	{84, 84, 84}, {0, 30, 116}, {8, 16, 144}, {48, 0, 136},
	{68, 0, 100}, {92, 0, 48}, {84, 4, 0}, {60, 24, 0},
	{32, 42, 0}, {8, 58, 0}, {0, 64, 0}, {0, 60, 0},
	{0, 50, 60}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0},

	{152, 150, 152}, {8, 76, 196}, {48, 50, 236}, {92, 30, 228},
	{136, 20, 176}, {160, 20, 100}, {152, 34, 32}, {120, 60, 0},
	{84, 90, 0}, {40, 114, 0}, {8, 124, 0}, {0, 118, 40},
	{0, 102, 120}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0},

	{236, 238, 236}, {76, 154, 236}, {120, 124, 236}, {176, 98, 236},
	{228, 84, 236}, {236, 88, 180}, {236, 106, 100}, {212, 136, 32},
	{160, 170, 0}, {116, 196, 0}, {76, 208, 32}, {56, 204, 108},
	{56, 180, 204}, {60, 60, 60}, {0, 0, 0}, {0, 0, 0},

	{236, 238, 236}, {168, 204, 236}, {188, 188, 236}, {212, 178, 236},
	{236, 174, 236}, {236, 164, 212}, {236, 180, 176}, {228, 196, 144},
	{204, 210, 120}, {180, 222, 120}, {168, 226, 144}, {152, 226, 180},
	{160, 214, 228}, {160, 162, 160}, {0, 0, 0}, {0, 0, 0},
}

// emphasisScale returns the per-channel multiplier (as a
// fraction out of 256) for the $2001 emphasis bits: emphasizing
// one channel attenuates the other two. On hardware the factors
// come from the composite signal levels; the published
// approximation used across emulators is about 0.75 for the
// attenuated channels.
func emphasisScale(emphasis uint8, channel int) int {
	if emphasis == 0 {
		return 256
	}
	if emphasis&(1<<uint(channel)) != 0 {
		return 256
	}
	return 192 // 0.75
}

// rgbTable resolves (emphasis, color index) to a packed
// XRGB8888 pixel. The output stage asks for one pixel per dot
// (about 61 thousand per frame), and the answer depends only
// on the 6-bit color index and the three emphasis bits, so the
// full 512-entry table is built once at startup with exactly
// the arithmetic colorRGB used to do per pixel.
var rgbTable [8][64]uint32

func init() {
	for e := 0; e < 8; e++ {
		for i := 0; i < 64; i++ {
			c := ntscPalette[i]
			r := int(c[0]) * emphasisScale(uint8(e), 0) >> 8
			g := int(c[1]) * emphasisScale(uint8(e), 1) >> 8
			b := int(c[2]) * emphasisScale(uint8(e), 2) >> 8
			rgbTable[e][i] = 0xFF000000 | uint32(r)<<16 | uint32(g)<<8 | uint32(b)
		}
	}
}

// colorRGB resolves a 6-bit PPU palette index under the current
// emphasis bits to a packed XRGB8888 pixel.
func (p *PPU) colorRGB(index uint8) uint32 {
	emph := (p.mask >> 5) & 7 // $2001 bits 5,6,7: red, green, blue
	return rgbTable[emph][index&0x3F]
}
