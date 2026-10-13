package video

// Display capture (DISPCAPCNT at 0x04000064), with the
// documented simplifications: source A is engine A's
// composed line before master brightness (the hardware's
// 3D+2D mix; the 3D layer takes part since DS-3), source B
// is read as
// 15bpp pixels from a VRAM bank at the same coordinates the
// write offset addresses (the hardware's separate source
// addressing is not modeled), and capture completes at the
// end of the last captured line, clearing enable and busy.
// Sizes: 0 = 128x128, 1 = 256x64, 2 = 256x128, 3 = 256x192.
// The write offset (bits 18-19) is in 32 KB units; the source
// select (bit 25) applies when the blend bit (24) is clear.

var capSizes = [4][2]int{{128, 128}, {256, 64}, {256, 128}, {256, 192}}

// CaptureLine runs one line of an armed capture. The system
// calls it after rendering each visible line.
func (v *Video) CaptureLine(y int) {
	if !v.capOn {
		return
	}
	cap := v.rd32(0x064)
	w, h := capSizes[(cap>>20)&3][0], capSizes[(cap>>20)&3][1]
	if y >= h {
		return
	}
	dst := v.bus.VRAMBank(int(cap>>16) & 3)
	srcB := v.bus.VRAMBank(int(cap>>26) & 3)
	writeOff := int(cap>>18&3) * 0x8000
	blendAB := cap&(1<<24) != 0
	selB := cap&(1<<25) != 0
	eva := int(cap & 0x1F)
	evb := int(cap >> 8 & 0x1F)
	to5 := func(c uint32) (uint32, uint32, uint32) {
		return (c >> 1) & 31, (c >> 7) & 31, (c >> 13) & 31
	}
	for x := 0; x < w; x++ {
		addr := writeOff + (y*w+x)*2
		if addr+2 > len(dst) {
			break
		}
		ar, ag, ab := to5(v.lineA[x])
		var r, g, b uint32
		if blendAB || selB {
			sw := uint16(srcB[addr]) | uint16(srcB[addr+1])<<8
			br, bg, bb := uint32(sw&31), uint32(sw>>5&31), uint32(sw>>10&31)
			if blendAB {
				mix := func(a, s uint32) uint32 {
					val := (int(a)*eva + int(s)*evb) >> 4
					if val > 31 {
						val = 31
					}
					return uint32(val)
				}
				r, g, b = mix(ar, br), mix(ag, bg), mix(ab, bb)
			} else {
				r, g, b = br, bg, bb
			}
		} else {
			r, g, b = ar, ag, ab
		}
		w16 := uint16(r) | uint16(g)<<5 | uint16(b)<<10 | 0x8000
		dst[addr] = byte(w16)
		dst[addr+1] = byte(w16 >> 8)
	}
	v.capLine++
	if y == h-1 {
		v.capOn = false
		cap = v.rd32(0x064)
		v.wr32(0x064, cap&^(1<<31|1<<29))
	}
}
