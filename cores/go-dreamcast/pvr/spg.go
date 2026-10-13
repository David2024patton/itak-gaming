package pvr

import "github.com/davidpatton/itak-gaming/cores/go-dreamcast/holly"

// SPG (sync pulse generator) timing. The beam position is derived
// from the CPU cycle stream with an exact fraction: the pixel
// clock is 27 MHz against the 200 MHz CPU clock, or 13.5 MHz when
// FB_R_CTRL has vclk_div set, so pixels advance at 27/200 or
// 27/400 per CPU cycle with the remainder carried in spg.frac (the
// same accumulate-and-carry pattern the SNES core uses for its APU
// clock ratio).
//
// Register fields implemented:
//   - SPG_LOAD: bits 9:0 = pixels per line minus 1, bits 25:16 =
//     lines per field minus 1.
//   - SPG_VBLANK_INT: bits 25:16 = vblank-in line, bits 9:0 =
//     vblank-out line. Reaching the in line sets the vblank state
//     and raises EvtVBlankIn; reaching the out line clears it and
//     raises EvtVBlankOut.
//   - SPG_HBLANK_INT: bits 9:0 = pixel position of the hblank
//     event each line; bit 31 disables the event.
//   - SPG_HBLANK: bits 9:0 = hblank status window start, bits
//     25:16 = end, reported in SPG_STATUS.
//   - SPG_CONTROL bit 6: interlace. Each completed field flips
//     the field bit; without interlace the field stays 0.
//   - SPG_STATUS (read): bits 9:0 = current line, bit 10 = hblank
//     window active, bit 11 = vblank active, bit 12 = field.
//
// In interlace mode one SPG frame is one field (262 or 263 lines
// for NTSC), so the frames counter counts fields. SPG_VBLANK and
// SPG_WIDTH are stored but do not gate anything in this model.

type spgState struct {
	frac     uint32 // pixel clock remainder, in cycle*27 units
	hcount   uint32 // pixels into the current line
	vcount   uint32 // lines into the current field
	field    uint32 // current field, 0 or 1
	frames   uint64 // completed fields (frames when progressive)
	inVBlank bool
}

// htotal returns the programmed pixels per line.
func (p *PVR) htotal() uint32 { return (p.reg(RegSPGLoad) & 0x3FF) + 1 }

// vtotal returns the programmed lines per field.
func (p *PVR) vtotal() uint32 { return ((p.reg(RegSPGLoad) >> 16) & 0x3FF) + 1 }

// pixelDenominator returns the pixel clock ratio denominator:
// pixels per CPU cycle = 27/denominator.
func (p *PVR) pixelDenominator() uint64 {
	if p.reg(RegFBRCTRL)&fbVclkDiv != 0 {
		return 400 // 13.5 MHz
	}
	return 200 // 27 MHz
}

// CyclesPerLine returns the CPU cycles in one scanline under the
// current SPG_LOAD and pixel clock, rounded up. The machine uses
// it to interleave CPU execution with SPG event delivery.
func (p *PVR) CyclesPerLine() int {
	d := p.pixelDenominator()
	return int((uint64(p.htotal())*d + 26) / 27)
}

// Advance moves the beam by the given number of CPU cycles,
// raising hblank and vblank events at the programmed positions.
func (p *PVR) Advance(cycles int) {
	if cycles <= 0 {
		return
	}
	d := p.pixelDenominator()
	acc := uint64(p.spg.frac) + uint64(cycles)*27
	px := acc / d
	p.spg.frac = uint32(acc % d)
	for ; px > 0; px-- {
		p.advancePixel()
	}
}

// advancePixel steps the beam one pixel, wrapping lines and
// fields and firing the per-line events.
func (p *PVR) advancePixel() {
	s := &p.spg
	s.hcount++
	hb := p.reg(RegSPGHBlankInt)
	if hb&(1<<31) == 0 && s.hcount == hb&0x3FF {
		p.raise(holly.BankNormal, 1<<holly.EvtHBlank)
	}
	if s.hcount < p.htotal() {
		return
	}
	s.hcount = 0
	s.vcount++
	if s.vcount >= p.vtotal() {
		s.vcount = 0
		s.frames++
		if p.reg(RegSPGControl)&spgInterlace != 0 {
			s.field ^= 1
		}
	}
	vbi := p.reg(RegSPGVBlankInt)
	vbin := (vbi >> 16) & 0x3FF
	vbout := vbi & 0x3FF
	if s.vcount == vbin {
		s.inVBlank = true
		p.raise(holly.BankNormal, 1<<holly.EvtVBlankIn)
	}
	if s.vcount == vbout && vbout != vbin {
		s.inVBlank = false
		p.raise(holly.BankNormal, 1<<holly.EvtVBlankOut)
	}
}

// spgStatus computes the SPG_STATUS read value.
func (p *PVR) spgStatus() uint32 {
	var v uint32
	v |= p.spg.vcount & 0x3FF
	hb := p.reg(RegSPGHBlank)
	if p.spg.hcount >= hb&0x3FF && p.spg.hcount <= (hb>>16)&0x3FF {
		v |= 1 << 10
	}
	if p.spg.inVBlank {
		v |= 1 << 11
	}
	if p.spg.field != 0 {
		v |= 1 << 12
	}
	return v
}

// SPGLine returns the current line within the field.
func (p *PVR) SPGLine() int { return int(p.spg.vcount) }

// SPGField returns the current field (0 or 1; flips per field in
// interlace mode).
func (p *PVR) SPGField() int { return int(p.spg.field) }

// SPGFrames returns the number of completed fields (frames in
// progressive mode).
func (p *PVR) SPGFrames() uint64 { return p.spg.frames }

// InVBlank reports whether the beam is inside the programmed
// vblank window.
func (p *PVR) InVBlank() bool { return p.spg.inVBlank }

// PixelsPerLine returns the programmed line total.
func (p *PVR) PixelsPerLine() int { return int(p.htotal()) }

// LinesPerField returns the programmed field total.
func (p *PVR) LinesPerField() int { return int(p.vtotal()) }
