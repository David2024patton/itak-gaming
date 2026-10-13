package mdec

import "math"

// Output assembly: color conversion and pixel packing
// for one finished macroblock, per sections 6.3 and
// 6.4.
//
// Colored macroblocks are emitted as four 8x8
// quadrants in Y1, Y2, Y3, Y4 order (the 8x8 bitmap
// order of the data port, section 6.1), each quadrant
// left to right and top to bottom. Monochrome
// macroblocks are a single 8x8 block in the same
// order.
//
// Color conversion follows yuv_to_rgb in section
// 6.3: with R holding the Cr sample and B holding the
// Cb sample for the pixel,
//
//	R = 1.402 * R
//	G = -0.3437 * B - 0.7143 * R
//	B = 1.772 * B
//
// and each output channel is the luma sample plus
// that term, saturated to the signed range -128..127.
// The chroma sample for a pixel comes from the
// quadrant formula in the document, Cr and Cb block
// index ((x+xx)/2) + ((y+yy)/2)*8, where (xx, yy) is
// the quadrant origin inside the 16x16 macroblock.
// The document states the exact fixed point
// resolution of this stage is unknown; this
// implementation computes in float64, saturates, and
// then rounds to the nearest integer, which is the
// choice recorded here and used by the tests. For
// unsigned output 128 is added to each channel (the
// document writes this as an xor with 808080h on the
// signed byte). For signed output the channel byte is
// the two complement byte of the signed value.
//
// Packing: 24 bit output is a byte stream of R, G, B
// per pixel (the document names the pixel value BGR,
// stored little endian), packed into words low byte
// first. 15 bit output is one halfword per pixel,
// bit 15 from the command STP flag, then blue, green
// and red in 5 bits each taken from the top 5 bits of
// the channel byte, packed low halfword first. 8 bit
// mono is one byte per pixel packed low byte first.
// 4 bit mono is one nibble per pixel, the top 4 bits
// of the gray byte, packed low nibble first.

var quadrantOrigin = [4][2]int{{0, 0}, {8, 0}, {0, 8}, {8, 8}}

func (d *MDEC) channelByte(signedVal int32) uint8 {
	if d.signedOut {
		return uint8(int8(signedVal))
	}
	return uint8(signedVal + 128)
}

func yuvChannel(y, cr, cb int32) (r, g, b int32) {
	rf := float64(y) + 1.402*float64(cr)
	gf := float64(y) - 0.3437*float64(cb) - 0.7143*float64(cr)
	bf := float64(y) + 1.772*float64(cb)
	r = clampI32(int32(math.Round(rf)), -128, 127)
	g = clampI32(int32(math.Round(gf)), -128, 127)
	b = clampI32(int32(math.Round(bf)), -128, 127)
	return r, g, b
}

func (d *MDEC) assembleColor() {
	if d.depth == 2 {
		data := make([]byte, 0, 256*3)
		for q := 0; q < 4; q++ {
			xx, yy := quadrantOrigin[q][0], quadrantOrigin[q][1]
			for py := 0; py < 8; py++ {
				for px := 0; px < 8; px++ {
					ci := (px+xx)/2 + ((py+yy)/2)*8
					y := clip9(d.yBlk[q][px+py*8])
					cr := clip9(d.crBlk[ci])
					cb := clip9(d.cbBlk[ci])
					r, g, b := yuvChannel(y, cr, cb)
					data = append(data, d.channelByte(r), d.channelByte(g), d.channelByte(b))
				}
			}
		}
		words := make([]uint32, 0, wordsPerMB(2))
		for i := 0; i < len(data); i += 4 {
			var w uint32
			for j := 0; j < 4 && i+j < len(data); j++ {
				w |= uint32(data[i+j]) << (8 * j)
			}
			words = append(words, w)
		}
		d.enqueue(words, 2)
		return
	}
	// 15 bit depth.
	halfs := make([]uint16, 0, 256)
	for q := 0; q < 4; q++ {
		xx, yy := quadrantOrigin[q][0], quadrantOrigin[q][1]
		for py := 0; py < 8; py++ {
			for px := 0; px < 8; px++ {
				ci := (px+xx)/2 + ((py+yy)/2)*8
				y := clip9(d.yBlk[q][px+py*8])
				cr := clip9(d.crBlk[ci])
				cb := clip9(d.cbBlk[ci])
				r, g, b := yuvChannel(y, cr, cb)
				var hw uint16
				if d.stp {
					hw |= 1 << 15
				}
				hw |= uint16(d.channelByte(b)>>3) << 10
				hw |= uint16(d.channelByte(g)>>3) << 5
				hw |= uint16(d.channelByte(r) >> 3)
				halfs = append(halfs, hw)
			}
		}
	}
	words := make([]uint32, 0, wordsPerMB(3))
	for i := 0; i < len(halfs); i += 2 {
		words = append(words, uint32(halfs[i])|uint32(halfs[i+1])<<16)
	}
	d.enqueue(words, 3)
}

func (d *MDEC) assembleMono() {
	gray := make([]uint8, 64)
	for i := 0; i < 64; i++ {
		v := clip9(d.yBlk[0][i])
		gray[i] = d.channelByte(v)
	}
	if d.depth == 1 {
		words := make([]uint32, 0, wordsPerMB(1))
		for i := 0; i < 64; i += 4 {
			var w uint32
			for j := 0; j < 4; j++ {
				w |= uint32(gray[i+j]) << (8 * j)
			}
			words = append(words, w)
		}
		d.enqueue(words, 1)
		return
	}
	// 4 bit depth: top nibble of each gray byte,
	// low nibble of each output byte first.
	words := make([]uint32, 0, wordsPerMB(0))
	for i := 0; i < 64; i += 8 {
		var w uint32
		for j := 0; j < 8; j++ {
			w |= uint32(gray[i+j]>>4) << (4 * j)
		}
		words = append(words, w)
	}
	d.enqueue(words, 0)
}
