// Package ppu is an original implementation of the Game Boy
// pixel processing unit, covering both the DMG and the Game Boy
// Color (CGB) models, written from the published hardware
// documentation (the Pan Docs graphics chapters and the published
// pixel FIFO research).
//
// The framebuffer is XRGB8888 (one uint32 per pixel, 0xFFRRGGBB):
// the format the libretro bridge negotiates. DMG shades map to
// the gray levels FF, AA, 55, 00 the acid2 references prescribe;
// CGB 15-bit colors are upconverted per channel with the standard
// (v<<3)|(v>>2) expansion the cgb-acid2 reference prescribes.
//
// The PPU is dot-stepped: one frame is 154 lines of 456 dots
// (70,224 dots). Each visible line runs mode 2 (OAM scan, 80 dots),
// mode 3 (pixel transfer, variable length), and mode 0 (HBlank) in
// order; lines 144-153 are mode 1 (VBlank). Mode 3 lasts
// 172 + (SCX mod 8) plus the sprite and window fetch penalties
// described at latchLine.
//
// Lines are rendered whole at the mode 3 to mode 0 transition from
// the scroll, palette, and sprite state latched when the line's
// transfer began, with tile data read live (VRAM writes are blocked
// during mode 3, so the bytes a transfer would have fetched cannot
// change under it). Register writes that land during HBlank
// therefore apply to the next line, as they do on hardware.
package ppu

import (
	"encoding/binary"
	"errors"
)

// Geometry of the DMG picture.
const (
	Width         = 160
	Height        = 144
	DotsPerLine   = 456
	LinesPerFrame = 154
	DotsPerFrame  = DotsPerLine * LinesPerFrame // 70,224

	modeHBlank = 0
	modeVBlank = 1
	modeOAM    = 2
	modeDraw   = 3
)

// lcdWarmup is the number of dots the first line after an LCD
// turn-on spends reading mode 0 before the first OAM scan begins.
const lcdWarmup = 88

// IRQ callback bits: the system passes a function the PPU calls to
// request an interrupt. Bit 0 is VBlank, bit 1 is LCD STAT.
const (
	IRQVBlank = 0
	IRQSTAT   = 1
)

// shadeXRGB maps the four DMG shades (0 lightest to 3 darkest) to
// XRGB8888 pixels. The gray levels are the ones the dmg-acid2
// reference prescribes for comparison: FF, AA, 55, 00.
var shadeXRGB = [4]uint32{0xFFFFFFFF, 0xFFAAAAAA, 0xFF555555, 0xFF000000}

// expand5 upconverts one 5-bit color channel to 8 bits.
func expand5(v uint16) uint32 {
	return uint32(v<<3 | v>>2)
}

// colorXRGB upconverts a CGB 15-bit color (RRRRRGG GGGBBBBB in a
// little-endian palette word) to an XRGB8888 framebuffer pixel.
func colorXRGB(c uint16) uint32 {
	return 0xFF000000 |
		expand5(c&0x1F)<<16 |
		expand5((c>>5)&0x1F)<<8 |
		expand5((c>>10)&0x1F)
}

// sprite is one OAM entry latched for a scanline during mode 2.
type sprite struct {
	y, x  uint8
	tile  uint8
	flags uint8
	index int // OAM order, breaks X ties
}

// PPU is one pixel processing unit. A PPU built with NewCGB
// models the Game Boy Color: two VRAM banks, tile attribute maps,
// color palettes, and CGB priority rules; New models the DMG,
// where the CGB registers are absent and bank 1 is unreachable.
type PPU struct {
	cgb    bool
	compat bool         // DMG compatibility mode: palette data ports lock
	vram   [0x4000]byte // two 8 KB banks; the DMG uses bank 0 only
	oam    [0xA0]byte
	fb     []uint32 // Width*Height XRGB8888

	// CGB state: the VBK-selected VRAM bank, palette RAM and the
	// BCPS/OCPS index registers (bit 7 of an index is the
	// auto-increment flag), and the FF6C object priority mode
	// (0 = OAM order, 1 = X order, the DMG rule).
	vbk    uint8
	bgPal  [64]byte
	objPal [64]byte
	bcps   uint8
	ocps   uint8
	opri   uint8
	bgAttr [Width]uint8 // per-pixel BG attribute latched by renderLineCGB
	spPalN [Width]uint8 // per-pixel sprite palette number (CGB render)
	spPrio [Width]bool  // per-pixel sprite priority flag (CGB render)

	// OnHBlank, when set by the machine, is called on the dot the
	// PPU enters mode 0 on a visible line. The system uses it to
	// clock HBlank HDMA transfers.
	OnHBlank func()

	// Registers.
	lcdc   uint8
	statEn uint8 // STAT bits 3-6: interrupt enables
	scy    uint8
	scx    uint8
	lyc    uint8
	bgp    uint8
	obp0   uint8
	obp1   uint8
	wy     uint8
	wx     uint8

	lcdOn bool
	line  int // current scanline 0-153 (register LY while on)
	dot   int // dot within the line 0-455
	mode  int

	statLine bool // current level of the STAT interrupt line

	// Per-line latched state, captured when a line's mode 2 ends.
	sprites    [10]sprite
	spPal      [Width]bool // per-pixel sprite palette choice while rendering
	numSprites int
	latScx     uint8
	latWx      int // window trigger X for this line, or -1
	wyArmed    bool
	winLine    int // window internal line counter
	winUsed    bool
	mode3Len   int

	lycEq bool // stored LYC coincidence flag. The comparison only
	// advances while the LCD is on (the comparison clock stops when
	// it is off), so the flag freezes across an LCD-off period and
	// LYC writes then have no effect. Recomputed on line changes,
	// LYC writes, and LCD turn-on, all while on.

	// LCD turn-on warmup: the first line after the LCD turns on
	// spends its first lcdWarmup dots reading mode 0 before the
	// first OAM scan begins, and its HBlank is shortened by the
	// same amount so the frame stays 456 dots per line. The
	// warmup length is bracketed by the mooneye lcdon_timing
	// measurements (mode still reads 0 at 68 dots, mode 3 reached
	// by 240 dots); the exact value is not pinned by the gated
	// tests.
	warm    int // warmup dots remaining on the current line
	shorten int // dots this line is shortened by (the warmup)
}

// New returns a PPU in the DMG post-boot state: the boot ROM hands
// over with the LCD on (LCDC 0x91), BGP 0xFC, object palettes 0xFF,
// parked at the top of a frame in mode 2.
func New() *PPU {
	p := &PPU{fb: make([]uint32, Width*Height)}
	p.Reset()
	return p
}

// NewCGB returns a PPU in the CGB post-boot state: the DMG
// hand-off plus cleared palette RAM (all colors read white until
// the game writes its palettes, the documented boot behavior) and
// VRAM bank 0 selected.
func NewCGB() *PPU {
	p := &PPU{fb: make([]uint32, Width*Height), cgb: true}
	p.Reset()
	return p
}

// CGB reports whether this PPU models the Game Boy Color.
func (p *PPU) CGB() bool { return p.cgb }

// SetCompat marks DMG compatibility mode (a DMG cartridge running
// on CGB hardware): the palette data ports and the sprite
// priority register lock to reads of 0xFF, as the hardware locks
// them, while the index registers keep working.
func (p *PPU) SetCompat() { p.compat = true }

// LCDOn reports whether the LCD is currently enabled.
func (p *PPU) LCDOn() bool { return p.lcdOn }

// Reset restores the post-boot state and clears the memories.
func (p *PPU) Reset() {
	for i := range p.vram {
		p.vram[i] = 0
	}
	for i := range p.oam {
		p.oam[i] = 0
	}
	for i := range p.fb {
		p.fb[i] = shadeXRGB[0]
	}
	p.vbk = 0
	p.bcps, p.ocps = 0, 0
	p.opri = 0
	for i := range p.bgPal {
		p.bgPal[i] = 0xFF
	}
	for i := range p.objPal {
		p.objPal[i] = 0xFF
	}
	p.lcdc = 0x91
	p.statEn = 0
	p.scy, p.scx = 0, 0
	p.lyc = 0
	p.bgp = 0xFC
	p.obp0, p.obp1 = 0xFF, 0xFF
	p.wy, p.wx = 0, 0
	p.lcdOn = true
	p.line, p.dot, p.mode = 0, 0, modeOAM
	p.lycEq = true
	p.statLine = false
	p.wyArmed = false
	p.winLine = 0
	p.winUsed = false
}

// Framebuffer returns the current frame as Width*Height
// XRGB8888 pixels, row major.
func (p *PPU) Framebuffer() []uint32 { return p.fb }

// LY returns the current scanline, or 0 while the LCD is off (the
// beam position is not observable then).
func (p *PPU) LY() int {
	if !p.lcdOn {
		return 0
	}
	return p.line
}

// Mode returns the current STAT mode (0 while the LCD is off).
func (p *PPU) Mode() int {
	if !p.lcdOn {
		return 0
	}
	return p.mode
}

// coincidence reports the stored LYC coincidence flag. The
// comparison clock only runs while the LCD is on, so the flag
// freezes (retained, not recomputed) while the LCD is off, and
// LYC writes then do not move it.
func (p *PPU) coincidence() bool { return p.lycEq }

// evalSTAT recomputes the STAT interrupt line from the enables and
// conditions and requests the STAT interrupt on a rising edge. The
// line is the OR of the enabled sources; while it stays high no
// new request is made, which is the hardware's STAT blocking
// behavior (a source that is already active when enabled, or that
// never drops, cannot retrigger).
func (p *PPU) evalSTAT(irq func(bit int)) {
	line := false
	if p.lcdOn {
		if p.statEn&0x40 != 0 && p.coincidence() {
			line = true
		}
		if p.statEn&0x20 != 0 && p.mode == modeOAM {
			line = true
		}
		if p.statEn&0x10 != 0 && p.mode == modeVBlank {
			line = true
		}
		if p.statEn&0x08 != 0 && p.mode == modeHBlank {
			line = true
		}
	}
	if line && !p.statLine && irq != nil {
		irq(IRQSTAT)
	}
	p.statLine = line
}

// Read handles a read from the PPU register and memory area
// (0x8000-0x9FFF VRAM, 0xFE00-0xFE9F OAM, 0xFF40-0xFF4B registers).
// Mode-based access blocking follows the hardware: VRAM is
// unreachable during mode 3, OAM during modes 2 and 3; blocked
// reads return 0xFF.
func (p *PPU) Read(addr uint16) uint8 {
	switch {
	case addr >= 0x8000 && addr < 0xA000:
		if p.lcdOn && p.mode == modeDraw {
			return 0xFF
		}
		return p.vram[int(p.vbk)*0x2000+int(addr-0x8000)]
	case addr >= 0xFE00 && addr < 0xFEA0:
		if p.lcdOn && (p.mode == modeOAM || p.mode == modeDraw) {
			return 0xFF
		}
		return p.oam[addr-0xFE00]
	}
	switch addr {
	case 0xFF40:
		return p.lcdc
	case 0xFF41: // STAT: bit 7 set, enables, LYC flag, mode
		v := uint8(0x80) | p.statEn
		if p.coincidence() {
			v |= 0x04
		}
		return v | uint8(p.Mode())
	case 0xFF42:
		return p.scy
	case 0xFF43:
		return p.scx
	case 0xFF44:
		return uint8(p.LY())
	case 0xFF45:
		return p.lyc
	case 0xFF47:
		return p.bgp
	case 0xFF48:
		return p.obp0
	case 0xFF49:
		return p.obp1
	case 0xFF4A:
		return p.wy
	case 0xFF4B:
		return p.wx
	case 0xFF4F: // VBK: CGB only; unused bits read back set
		if !p.cgb {
			return 0xFF
		}
		return 0xFE | p.vbk
	case 0xFF68: // BCPS
		if !p.cgb {
			return 0xFF
		}
		return 0x40 | p.bcps
	case 0xFF69: // BGPD: palette RAM is unreachable during mode 3
		if !p.cgb || p.compat || (p.lcdOn && p.mode == modeDraw) {
			return 0xFF
		}
		v := p.bgPal[p.bcps&0x3F]
		if p.bcps&0x80 != 0 {
			p.bcps = 0x80 | (p.bcps+1)&0x3F
		}
		return v
	case 0xFF6A: // OCPS
		if !p.cgb {
			return 0xFF
		}
		return 0x40 | p.ocps
	case 0xFF6B: // OBPD
		if !p.cgb || p.compat || (p.lcdOn && p.mode == modeDraw) {
			return 0xFF
		}
		v := p.objPal[p.ocps&0x3F]
		if p.ocps&0x80 != 0 {
			p.ocps = 0x80 | (p.ocps+1)&0x3F
		}
		return v
	case 0xFF6C: // OPRI: CGB object priority mode select
		if !p.cgb || p.compat {
			return 0xFF
		}
		return 0xFE | p.opri
	}
	return 0xFF
}

// Write handles a write to the PPU area. Blocked writes (VRAM in
// mode 3, OAM in modes 2 and 3) are ignored, as on hardware. LY is
// read-only. STAT stores only its interrupt enable bits.
func (p *PPU) Write(addr uint16, val uint8, irq func(bit int)) {
	switch {
	case addr >= 0x8000 && addr < 0xA000:
		if !(p.lcdOn && p.mode == modeDraw) {
			p.vram[int(p.vbk)*0x2000+int(addr-0x8000)] = val
		}
		return
	case addr >= 0xFE00 && addr < 0xFEA0:
		if !(p.lcdOn && (p.mode == modeOAM || p.mode == modeDraw)) {
			p.oam[addr-0xFE00] = val
		}
		return
	}
	switch addr {
	case 0xFF40:
		p.writeLCDC(val, irq)
	case 0xFF41:
		p.statEn = val & 0x78
		p.evalSTAT(irq)
	case 0xFF42:
		p.scy = val
	case 0xFF43:
		p.scx = val
	case 0xFF44:
		// LY is read-only.
	case 0xFF45:
		p.lyc = val
		if p.lcdOn {
			p.lycEq = p.line == int(val)
			p.evalSTAT(irq)
		}
	case 0xFF47:
		p.bgp = val
	case 0xFF48:
		p.obp0 = val
	case 0xFF49:
		p.obp1 = val
	case 0xFF4A:
		p.wy = val
	case 0xFF4B:
		p.wx = val
	case 0xFF4F: // VBK
		if p.cgb {
			p.vbk = val & 1
		}
	case 0xFF68: // BCPS
		if p.cgb {
			p.bcps = val & 0xBF
		}
	case 0xFF69: // BGPD: ignored during mode 3, as on hardware
		if p.cgb && !p.compat && !(p.lcdOn && p.mode == modeDraw) {
			p.bgPal[p.bcps&0x3F] = val
			if p.bcps&0x80 != 0 {
				p.bcps = 0x80 | (p.bcps+1)&0x3F
			}
		}
	case 0xFF6A: // OCPS
		if p.cgb {
			p.ocps = val & 0xBF
		}
	case 0xFF6B: // OBPD: ignored during mode 3
		if p.cgb && !p.compat && !(p.lcdOn && p.mode == modeDraw) {
			p.objPal[p.ocps&0x3F] = val
			if p.ocps&0x80 != 0 {
				p.ocps = 0x80 | (p.ocps+1)&0x3F
			}
		}
	case 0xFF6C: // OPRI
		if p.cgb && !p.compat {
			p.opri = val & 1
		}
	}
}

// writeLCDC handles the LCD control register, including the LCD
// power transitions. Turning the LCD off parks the beam: LY reads
// as 0, the mode as 0, and no interrupts are generated. Turning it
// back on restarts the frame at line 0 in mode 2.
func (p *PPU) writeLCDC(val uint8, irq func(bit int)) {
	was := p.lcdOn
	p.lcdc = val
	p.lcdOn = val&0x80 != 0
	if was && !p.lcdOn {
		p.line, p.dot, p.mode = 0, 0, modeHBlank
		p.warm, p.shorten = 0, 0
	}
	if !was && p.lcdOn {
		// The beam restarts at line 0; the first OAM scan
		// begins after the turn-on warmup (see the warm
		// field). The comparison clock restarts too, which
		// can raise the STAT line right here (LY 0 vs LYC 0).
		p.line, p.dot, p.mode = 0, 0, modeHBlank
		p.warm, p.shorten = lcdWarmup, lcdWarmup
		p.lycEq = int(p.lyc) == 0
		p.wyArmed = false
		p.winLine = 0
		p.evalSTAT(irq)
	}
}

// ReadVRAMRaw and the OAM raw accessors bypass mode blocking. The
// DMA unit uses them: its transfers run on the bus independent of
// the pixel transfer, and its OAM writes are the entire point.
// ReadVRAMRaw reads the VBK-selected bank; WriteVRAMRaw writes a
// VRAM offset (0x0000-0x1FFF) in the VBK-selected bank, which is
// what HDMA targets.
func (p *PPU) ReadVRAMRaw(addr uint16) uint8 {
	return p.vram[int(p.vbk)*0x2000+int(addr-0x8000)]
}
func (p *PPU) WriteVRAMRaw(off int, val uint8) {
	if off >= 0 && off < 0x2000 {
		p.vram[int(p.vbk)*0x2000+off] = val
	}
}
func (p *PPU) WriteOAMRaw(off int, val uint8) {
	if off >= 0 && off < len(p.oam) {
		p.oam[off] = val
	}
}
func (p *PPU) ReadOAMRaw(off int) uint8 {
	if off >= 0 && off < len(p.oam) {
		return p.oam[off]
	}
	return 0xFF
}

// latchLine runs at the start of a visible line's mode 3: the OAM
// scan result (up to ten sprites covering this line, in OAM order)
// and the scroll and window state the transfer will use.
func (p *PPU) latchLine() {
	// OAM scan: two dots per entry over mode 2; the first ten
	// sprites whose Y range covers the line are latched.
	height := 8
	if p.lcdc&0x04 != 0 {
		height = 16
	}
	p.numSprites = 0
	for i := 0; i < 40 && p.numSprites < 10; i++ {
		y := p.oam[i*4]
		if int(p.line)+16 >= int(y) && int(p.line)+16 < int(y)+height {
			p.sprites[p.numSprites] = sprite{
				y: y, x: p.oam[i*4+1], tile: p.oam[i*4+2],
				flags: p.oam[i*4+3], index: i,
			}
			p.numSprites++
		}
	}
	p.latScx = p.scx
	// Window arming: the window becomes drawable on the line
	// where LY reaches WY, and stays armed for the rest of the
	// frame. The trigger X is WX - 7; WX below 7 triggers at the
	// left edge, WX above 166 never triggers.
	if int(p.wy) == p.line {
		p.wyArmed = true
	}
	p.latWx = -1
	// On the DMG the window needs LCDC bits 5 and 0; on the CGB
	// bit 0 is priority-only and the window displays without it.
	if p.lcdc&0x20 != 0 && (p.cgb || p.lcdc&0x01 != 0) && p.wyArmed {
		t := int(p.wx) - 7
		if t < 0 {
			t = 0
		}
		if t < Width {
			p.latWx = t
		}
	}
	// Mode 3 length: the base transfer plus the scroll discard,
	// the window fetch, and the sprite fetch penalties. A sprite
	// fetch stalls the pixel FIFO for 6 dots; the first sprite
	// fetched also pays the phase alignment of its X position
	// (8 - (X mod 8) in total for that fetch), and sprites at
	// X >= 168 sit fully off the right edge and are never
	// fetched. The full phase interaction between sprites at
	// different X positions is FIFO-internal; this is the
	// documented level the mooneye mode timing tests check apart
	// from intr_2_mode0_timing_sprites, which stays out of the
	// gate for exactly that reason.
	p.mode3Len = 172 + int(p.latScx&7)
	fetched := 0
	minX := 0
	for i := 0; i < p.numSprites; i++ {
		if int(p.sprites[i].x) < 168 {
			if fetched == 0 || int(p.sprites[i].x) < minX {
				minX = int(p.sprites[i].x)
			}
			fetched++
		}
	}
	if fetched > 0 {
		p.mode3Len += 8 - (minX & 7) + 6*(fetched-1)
	}
	if p.latWx >= 0 {
		p.mode3Len += 6
	}
	p.winUsed = false
}

// renderLine draws the current line into the framebuffer from the
// latched state and live tile data. The CGB renderer handles
// color; the DMG renderer below handles monochrome.
func (p *PPU) renderLine() {
	if p.cgb {
		p.renderLineCGB()
		return
	}
	y := p.line
	out := p.fb[y*Width : (y+1)*Width]
	bgOn := p.lcdc&0x01 != 0
	winOn := p.lcdc&0x20 != 0 && p.latWx >= 0
	var bgIdx [Width]uint8

	bgMap := uint16(0x1800)
	if p.lcdc&0x08 != 0 {
		bgMap = 0x1C00
	}
	winMap := uint16(0x1800)
	if p.lcdc&0x40 != 0 {
		winMap = 0x1C00
	}
	signedTiles := p.lcdc&0x10 == 0

	tilePixel := func(mapBase uint16, tx, ty, px, py int) uint8 {
		idx := p.vram[mapBase+uint16(ty)*32+uint16(tx)]
		var data uint16
		if signedTiles {
			data = 0x1000 + uint16(int(int8(idx))*16)
		} else {
			data = uint16(idx) * 16
		}
		lo := p.vram[data+uint16(py)*2]
		hi := p.vram[data+uint16(py)*2+1]
		bit := uint(7 - px)
		return (hi>>bit&1)<<1 | lo>>bit&1
	}

	for x := 0; x < Width; x++ {
		var idx uint8
		if bgOn {
			if winOn && x >= p.latWx {
				// Window pixel: the window's own line counter
				// supplies the tile row.
				wx := x - p.latWx
				idx = tilePixel(winMap, wx/8, p.winLine/8, wx%8, p.winLine%8)
				p.winUsed = true
			} else {
				bx := x + int(p.latScx)
				by := y + int(p.scy)
				idx = tilePixel(bgMap, (bx/8)%32, (by/8)%32, bx%8, by%8)
			}
		}
		bgIdx[x] = idx
		out[x] = uint32(idx)
	}

	// Sprites: priority order is smallest X first, OAM order
	// breaking ties; the first sprite to claim a pixel wins it,
	// subject to the BG-over-sprite flag (which only yields to a
	// background color index of 0).
	if p.lcdc&0x02 != 0 {
		height := 8
		if p.lcdc&0x04 != 0 {
			height = 16
		}
		order := make([]int, p.numSprites)
		for i := range order {
			order[i] = i
		}
		// Stable insertion sort by X (lists hold at most ten).
		for i := 1; i < len(order); i++ {
			for j := i; j > 0 && p.sprites[order[j]].x < p.sprites[order[j-1]].x; j-- {
				order[j], order[j-1] = order[j-1], order[j]
			}
		}
		var claimed [Width]bool
		for _, oi := range order {
			sp := p.sprites[oi]
			row := y + 16 - int(sp.y)
			if sp.flags&0x40 != 0 { // Y-flip
				row = height - 1 - row
			}
			tile := sp.tile
			if height == 16 {
				tile &^= 1
				if row >= 8 {
					tile++
					row -= 8
				}
			}
			data := uint16(tile) * 16
			lo := p.vram[data+uint16(row)*2]
			hi := p.vram[data+uint16(row)*2+1]
			for px := 0; px < 8; px++ {
				sx := int(sp.x) - 8 + px
				if sx < 0 || sx >= Width || claimed[sx] {
					continue
				}
				bit := uint(px)
				if sp.flags&0x20 == 0 { // X-flip mirrors the pixel order
					bit = uint(7 - px)
				}
				ci := (hi>>bit&1)<<1 | lo>>bit&1
				if ci == 0 {
					continue
				}
				claimed[sx] = true
				if sp.flags&0x80 != 0 && bgIdx[sx] != 0 {
					continue // BG wins over this sprite pixel
				}
				out[sx] = uint32(ci) | 0x80 // tag as sprite for the palette pass
				p.spPal[sx] = sp.flags&0x10 != 0
			}
		}
	}

	// Palette pass: background pixels through BGP, sprite pixels
	// (tagged with bit 7 of the stored value) through the object
	// palette their flags selected.
	for x := 0; x < Width; x++ {
		v := out[x]
		if v&0x80 != 0 {
			pal := p.obp0
			if p.spPal[x] {
				pal = p.obp1
			}
			out[x] = shadeXRGB[(pal>>(2*(v&3)))&3]
		} else {
			out[x] = shadeXRGB[(p.bgp>>(2*v))&3]
		}
	}
}

// renderLineCGB draws one line with CGB color: tile attributes
// from VRAM bank 1 (palette, tile bank, flips, priority), sprites
// with per-sprite palettes and banks, and the CGB priority
// resolution between the background and sprite layers:
//
//   - LCDC bit 0 is the BG/window master priority. Clear, the
//     background and window still display, but sprites always
//     win over them: the priority bits are ignored. (Blanking
//     the background is the DMG compatibility mode behavior,
//     which this core does not model.)
//   - A background color index of 0 always loses to a sprite.
//   - Otherwise, if either the BG attribute priority bit or the
//     sprite's priority bit is set, the background wins; if not,
//     the sprite wins.
//
// Sprite overlap uses OAM order (the first sprite in OAM keeps
// the pixel), unless FF6C (OPRI) selects the DMG rule (smallest
// X, OAM order breaking ties).
func (p *PPU) renderLineCGB() {
	y := p.line
	out := p.fb[y*Width : (y+1)*Width]
	master := p.lcdc&0x01 != 0
	winOn := p.lcdc&0x20 != 0 && p.latWx >= 0

	bgMap := uint16(0x1800)
	if p.lcdc&0x08 != 0 {
		bgMap = 0x1C00
	}
	winMap := uint16(0x1800)
	if p.lcdc&0x40 != 0 {
		winMap = 0x1C00
	}
	signedTiles := p.lcdc&0x10 == 0

	// tilePixelCGB resolves one background or window pixel: the
	// tile index from bank 0, its attribute from bank 1, and the
	// tile data from the attribute's bank, flips applied.
	tilePixel := func(mapBase uint16, tx, ty, px, py int) (idx uint8, attr uint8) {
		off := mapBase + uint16(ty)*32 + uint16(tx)
		tile := p.vram[off]
		attr = p.vram[0x2000+off]
		if attr&0x20 != 0 { // X flip
			px = 7 - px
		}
		if attr&0x40 != 0 { // Y flip
			py = 7 - py
		}
		var data uint16
		if signedTiles {
			data = 0x1000 + uint16(int(int8(tile))*16)
		} else {
			data = uint16(tile) * 16
		}
		base := int(attr>>3&1) * 0x2000
		lo := p.vram[base+int(data)+py*2]
		hi := p.vram[base+int(data)+py*2+1]
		bit := uint(7 - px)
		return (hi>>bit&1)<<1 | lo>>bit&1, attr
	}

	var bgIdx [Width]uint8
	for x := 0; x < Width; x++ {
		var attr uint8
		if winOn && x >= p.latWx {
			wx := x - p.latWx
			bgIdx[x], attr = tilePixel(winMap, wx/8, p.winLine/8, wx%8, p.winLine%8)
			p.winUsed = true
		} else {
			bx := x + int(p.latScx)
			by := y + int(p.scy)
			bgIdx[x], attr = tilePixel(bgMap, (bx/8)%32, (by/8)%32, bx%8, by%8)
		}
		p.bgAttr[x] = attr
		out[x] = uint32(bgIdx[x]) // resolved in the composite pass
	}

	// Sprites: the first sprite (in the active priority order)
	// to claim a pixel with a non-transparent index keeps it.
	var spIdx [Width]uint8
	var claimed [Width]bool
	if p.lcdc&0x02 != 0 {
		height := 8
		if p.lcdc&0x04 != 0 {
			height = 16
		}
		order := make([]int, p.numSprites)
		for i := range order {
			order[i] = i
		}
		if p.opri&1 != 0 {
			// DMG ordering: smallest X first, OAM order breaking
			// ties (stable sort).
			for i := 1; i < len(order); i++ {
				for j := i; j > 0 && p.sprites[order[j]].x < p.sprites[order[j-1]].x; j-- {
					order[j], order[j-1] = order[j-1], order[j]
				}
			}
		}
		for _, oi := range order {
			sp := p.sprites[oi]
			row := y + 16 - int(sp.y)
			if sp.flags&0x40 != 0 {
				row = height - 1 - row
			}
			tile := sp.tile
			if height == 16 {
				tile &^= 1
				if row >= 8 {
					tile++
					row -= 8
				}
			}
			base := int(sp.flags>>3&1) * 0x2000
			data := base + int(tile)*16
			lo := p.vram[data+row*2]
			hi := p.vram[data+row*2+1]
			for px := 0; px < 8; px++ {
				sx := int(sp.x) - 8 + px
				if sx < 0 || sx >= Width || claimed[sx] {
					continue
				}
				bit := uint(px)
				if sp.flags&0x20 == 0 {
					bit = uint(7 - px)
				}
				ci := (hi>>bit&1)<<1 | lo>>bit&1
				if ci == 0 {
					continue
				}
				claimed[sx] = true
				spIdx[sx] = ci
				p.spPalN[sx] = sp.flags & 0x07
				p.spPrio[sx] = sp.flags&0x80 != 0
			}
		}
	}

	for x := 0; x < Width; x++ {
		bgPalIdx := p.bgAttr[x] & 0x07
		bg := binary.LittleEndian.Uint16(p.bgPal[bgPalIdx*8+bgIdx[x]*2:])
		if claimed[x] {
			spriteWins := false
			switch {
			case bgIdx[x] == 0:
				spriteWins = true
			case !master:
				spriteWins = true
			case p.bgAttr[x]&0x80 != 0 || p.spPrio[x]:
				spriteWins = false
			default:
				spriteWins = true
			}
			if spriteWins {
				out[x] = colorXRGB(binary.LittleEndian.Uint16(
					p.objPal[p.spPalN[x]*8+spIdx[x]*2:]))
				continue
			}
		}
		out[x] = colorXRGB(bg)
	}
}

// Tick advances the PPU by the given number of dots, calling irq
// for interrupt requests. While the LCD is off the beam is parked
// and Tick does nothing; the caller keeps frame pacing.
func (p *PPU) Tick(dots int, irq func(bit int)) {
	if !p.lcdOn {
		return
	}
	for i := 0; i < dots; i++ {
		if p.warm > 0 {
			// Warmup dots still advance the line's dot count,
			// so the first line totals 456 dots like any other.
			p.dot++
			p.warm--
			if p.warm == 0 {
				p.mode = modeOAM
				p.evalSTAT(irq)
			}
			if p.dot == DotsPerLine {
				p.endLine(irq)
			}
			continue
		}
		p.dot++
		switch p.mode {
		case modeOAM:
			if p.dot == 80+p.shorten {
				p.latchLine()
				p.mode = modeDraw
				p.evalSTAT(irq)
			}
		case modeDraw:
			if p.dot == 80+p.shorten+p.mode3Len {
				p.renderLine()
				p.mode = modeHBlank
				if p.OnHBlank != nil {
					p.OnHBlank()
				}
				p.evalSTAT(irq)
			}
		case modeHBlank:
			if p.dot == DotsPerLine {
				p.endLine(irq)
			}
		case modeVBlank:
			if p.dot == DotsPerLine {
				p.endLine(irq)
			}
		}
	}
}

// endLine advances to the next scanline at the line boundary:
// LY changes (with its LYC evaluation), VBlank entry raises the
// VBlank interrupt, the window line counter advances when the
// window drew, and the frame wraps after line 153.
func (p *PPU) endLine(irq func(bit int)) {
	p.dot = 0
	p.shorten = 0
	if p.winUsed {
		p.winLine++
	}
	p.line++
	if p.line >= LinesPerFrame {
		p.line = 0
		p.wyArmed = false
		p.winLine = 0
	}
	p.lycEq = p.line == int(p.lyc)
	if p.line == 144 {
		p.mode = modeVBlank
		if irq != nil {
			irq(IRQVBlank)
		}
		p.evalSTAT(irq)
		return
	}
	if p.line < Height {
		p.mode = modeOAM
	} else {
		p.mode = modeVBlank
	}
	p.evalSTAT(irq)
}

// --- save states ---------------------------------------------------------

// SerializeSize is the fixed size of the PPU state: both VRAM
// banks, OAM, the framebuffer, palette RAM, registers, and beam
// state.
const SerializeSize = 0x4000 + 0xA0 + Width*Height*4 + 128 + 128

const serializeVersion = 2

// Serialize writes the full PPU state into buf.
func (p *PPU) Serialize(buf []byte) error {
	if len(buf) < SerializeSize {
		return errors.New("ppu: serialize buffer too small")
	}
	for i := 0; i < SerializeSize; i++ {
		buf[i] = 0
	}
	off := 0
	copy(buf[off:], p.vram[:])
	off += len(p.vram)
	copy(buf[off:], p.oam[:])
	off += len(p.oam)
	for i, px := range p.fb {
		binary.LittleEndian.PutUint32(buf[off+i*4:], px)
	}
	off += len(p.fb) * 4
	copy(buf[off:], p.bgPal[:])
	off += len(p.bgPal)
	copy(buf[off:], p.objPal[:])
	off += len(p.objPal)
	b := buf[off:]
	b[0] = serializeVersion
	b[1] = p.lcdc
	b[2] = p.statEn
	b[3] = p.scy
	b[4] = p.scx
	b[5] = p.lyc
	b[6] = p.bgp
	b[7] = p.obp0
	b[8] = p.obp1
	b[9] = p.wy
	b[10] = p.wx
	if p.lcdOn {
		b[11] = 1
	}
	binary.LittleEndian.PutUint16(b[12:], uint16(p.line))
	binary.LittleEndian.PutUint16(b[14:], uint16(p.dot))
	b[16] = uint8(p.mode)
	if p.statLine {
		b[17] = 1
	}
	if p.wyArmed {
		b[18] = 1
	}
	binary.LittleEndian.PutUint16(b[19:], uint16(p.winLine))
	if p.winUsed {
		b[21] = 1
	}
	b[22] = p.latScx
	binary.LittleEndian.PutUint16(b[23:], uint16(int16(p.latWx)))
	binary.LittleEndian.PutUint16(b[25:], uint16(p.mode3Len))
	b[27] = uint8(p.numSprites)
	for i := 0; i < p.numSprites; i++ {
		sp := p.sprites[i]
		b[28+i*5] = sp.y
		b[29+i*5] = sp.x
		b[30+i*5] = sp.tile
		b[31+i*5] = sp.flags
		b[32+i*5] = uint8(sp.index)
	}
	binary.LittleEndian.PutUint16(b[80:], uint16(p.warm))
	binary.LittleEndian.PutUint16(b[82:], uint16(p.shorten))
	if p.lycEq {
		b[84] = 1
	}
	if p.cgb {
		b[85] = 1
	}
	b[86] = p.vbk
	b[87] = p.bcps
	b[88] = p.ocps
	b[89] = p.opri
	return nil
}

// Deserialize restores a state written by Serialize.
func (p *PPU) Deserialize(buf []byte) error {
	if len(buf) < SerializeSize {
		return errors.New("ppu: deserialize buffer too small")
	}
	off := 0
	copy(p.vram[:], buf[off:])
	off += len(p.vram)
	copy(p.oam[:], buf[off:])
	off += len(p.oam)
	for i := range p.fb {
		p.fb[i] = binary.LittleEndian.Uint32(buf[off+i*4:])
	}
	off += len(p.fb) * 4
	copy(p.bgPal[:], buf[off:])
	off += len(p.bgPal)
	copy(p.objPal[:], buf[off:])
	off += len(p.objPal)
	b := buf[off:]
	if b[0] != serializeVersion {
		return errors.New("ppu: unsupported serialize version")
	}
	p.lcdc = b[1]
	p.statEn = b[2]
	p.scy = b[3]
	p.scx = b[4]
	p.lyc = b[5]
	p.bgp = b[6]
	p.obp0 = b[7]
	p.obp1 = b[8]
	p.wy = b[9]
	p.wx = b[10]
	p.lcdOn = b[11] != 0
	p.line = int(binary.LittleEndian.Uint16(b[12:]))
	p.dot = int(binary.LittleEndian.Uint16(b[14:]))
	p.mode = int(b[16])
	p.statLine = b[17] != 0
	p.wyArmed = b[18] != 0
	p.winLine = int(binary.LittleEndian.Uint16(b[19:]))
	p.winUsed = b[21] != 0
	p.latScx = b[22]
	p.latWx = int(int16(binary.LittleEndian.Uint16(b[23:])))
	p.mode3Len = int(binary.LittleEndian.Uint16(b[25:]))
	p.numSprites = int(b[27])
	for i := 0; i < p.numSprites && i < 10; i++ {
		p.sprites[i] = sprite{
			y: b[28+i*5], x: b[29+i*5], tile: b[30+i*5],
			flags: b[31+i*5], index: int(b[32+i*5]),
		}
	}
	p.warm = int(binary.LittleEndian.Uint16(b[80:]))
	p.shorten = int(binary.LittleEndian.Uint16(b[82:]))
	p.lycEq = b[84] != 0
	// The model's identity comes from how the PPU was built, not
	// from the state; the stored flag is only a sanity marker.
	p.vbk = b[86]
	p.bcps = b[87]
	p.ocps = b[88]
	p.opri = b[89]
	return nil
}
