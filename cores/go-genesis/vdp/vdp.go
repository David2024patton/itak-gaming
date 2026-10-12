// Package vdp emulates the Sega Genesis VDP (video display processor).
//
// Clean-room implementation from published hardware documentation
// (the Genesis VDP register map, command protocol and tile formats are
// documented in Sega's Genesis Technical Overview and Charles MacDonald's
// VDP notes).
//
// PINNED API (bus, system and libretro are written against this; do not
// change signatures):
//
//	New() *VDP
//	SetDMASource(f func(addr uint32) uint16)
//	ReadPort(off uint32) uint16      off = 68k address & 0x1F within 0xC00000 window
//	WritePort(off uint32, val uint16)
//	BeginFrame()
//	RenderLine(y int)                renders visible line y (0..DisplayHeight()-1)
//	EnterVBlank() bool               sets the vblank flag; true if IRQ6 is enabled
//	ExitVBlank()
//	LineIRQ(y int) bool              true when an HBlank IRQ4 fires on line y
//	Framebuffer() []uint16           RGB565 pixels, Width() x Height()
//	DisplayWidth() int               256 (H32) or 320 (H40)
//	DisplayHeight() int              224 (NTSC) or 240
//	SerializeSize() int
//	Serialize(buf []byte) error
//	Deserialize(buf []byte) error
//
// Port offsets (off): 0x00/0x02 data, 0x04/0x06 control (read = status),
// 0x08-0x0E HV counter, 0x10/0x11 PSG write (bus forwards those to sound),
// 0x18 debug register (ignore), others mirror.
package vdp

import (
	"encoding/binary"
	"errors"
)

// Frame geometry maxima.
const (
	MaxWidth  = 320
	MaxHeight = 240
)

// accessTarget identifies the memory selected by a VDP command code.
// The low command bits encode VRAM as 0, CRAM as 1 (bit 1 set) and
// VSRAM as 2 (bit 2 set); bit 0 distinguishes a write (1) from a read (0).
type accessTarget int

const (
	targetVRAM accessTarget = iota
	targetCRAM
	targetVSRAM
)

// VDP holds the full video chip state.
type VDP struct {
	Regs [24]uint8 // control registers 0-23

	VRAM  [0x10000]uint8 // 64KB video RAM (patterns, name tables, sprite table, hscroll)
	CRAM  [128]uint8     // 64 colors x 2 bytes (9-bit BGR)
	VSRAM [80]uint8      // vertical scroll, 40 entries x 2 bytes

	// Command state
	addr    uint32 // current VRAM/CRAM/VSRAM address (17 bits used)
	code    uint8  // access type from the two control writes
	pending bool   // first control word latched
	first   uint16 // latched first control word

	// Status / timing
	vblank    bool
	dmaBusy   bool
	dmaFill   bool
	dmaSrc    uint32
	dmaLen    uint16
	hintCtr   int // scanlines remaining before the next HBlank interrupt
	readBuf   uint16
	currentLn int // last line handed to RenderLine, for the HV counter
	frames    int // completed frames (kept for timing/debug state)

	dmaSource func(addr uint32) uint16

	fb []uint16 // framebuffer, MaxWidth*MaxHeight RGB565
}

// New returns a VDP in power-on state.
func New() *VDP {
	return &VDP{fb: make([]uint16, MaxWidth*MaxHeight)}
}

// SetDMASource installs the reader the VDP uses for DMA transfers from
// 68k address space (word reads).
func (v *VDP) SetDMASource(f func(addr uint32) uint16) { v.dmaSource = f }

// ReadPort reads one of the VDP ports. off is the offset within the
// 0xC00000 window, already masked to 0x1F by the caller (we mask again
// defensively so mirrors behave the same).
func (v *VDP) ReadPort(off uint32) uint16 {
	switch off & 0x1F {
	case 0x00, 0x02:
		return v.readData()
	case 0x04, 0x06:
		return v.readStatus()
	case 0x08, 0x0A, 0x0C, 0x0E:
		return v.readHV()
	default:
		// PSG, debug register and unused mirrors read back as zero.
		return 0
	}
}

// WritePort writes one of the VDP ports.
func (v *VDP) WritePort(off uint32, val uint16) {
	switch off & 0x1F {
	case 0x00, 0x02:
		v.writeData(val)
	case 0x04, 0x06:
		v.writeControl(val)
	default:
		// HV counter is read-only; PSG writes are forwarded to the
		// sound section by the bus; the debug register is ignored.
	}
}

// readStatus composes the status register. The top bits are the fixed
// open-bus pattern (0x3400), bit 9 reports an empty FIFO, and bit 4 is
// the vertical blank flag. Reading the status also clears the latched
// first command word; games depend on that side effect.
func (v *VDP) readStatus() uint16 {
	status := uint16(0x3400) | 0x0200 // FIFO empty
	if v.vblank {
		status |= 0x0010
	}
	if v.dmaBusy {
		status |= 0x0004
	}
	v.pending = false
	return status
}

// readHV returns the horizontal/vertical counter pair. Milestone 1 has
// no cycle-level beam position, so the horizontal counter reports a
// fixed mid-line value; the vertical counter tracks the last rendered
// line, or the first vertical blank line while in vblank.
func (v *VDP) readHV() uint16 {
	line := v.currentLn
	if v.vblank {
		line = 224
	}
	if line < 0 {
		line = 0
	}
	const hcount = 0x80
	return uint16(line&0xFF)<<8 | (hcount >> 1)
}

// writeControl runs the two-word command latch and the single-word
// register write encoding.
func (v *VDP) writeControl(word uint16) {
	if !v.pending {
		// A first word of the form 100xxxxx xxxxxxxx is a register
		// write: bits 12-8 select the register, the low byte is the
		// value. It leaves no pending command behind.
		if word&0xE000 == 0x8000 {
			if reg := (word >> 8) & 0x1F; reg < uint16(len(v.Regs)) {
				v.Regs[reg] = uint8(word)
			}
			v.pending = false
			return
		}
		// Otherwise this is the first command word: the low 14 bits
		// are address bits A0-A13 and bits 15/14 are CD1/CD0, which
		// land in code bits 1/0.
		v.first = word
		v.pending = true
		v.addr = uint32(word & 0x3FFF)
		v.code = uint8((word >> 14) & 0x03)
		return
	}

	// Second command word: bits 1-0 are address bits A14-A15 and
	// bits 7-4 are CD5-CD2, which land in code bits 5-2.
	v.pending = false
	v.addr |= uint32(word&0x03) << 14
	v.code |= uint8((word & 0xF0) >> 2)
	v.finishCommand()
}

// finishCommand acts on a completed command word pair: DMA commands run
// (or, for a fill, arm) immediately, and read commands prefetch the
// first word into the read buffer.
func (v *VDP) finishCommand() {
	v.dmaFill = false
	if v.code&0x20 != 0 {
		v.handleDMACommand()
		return
	}
	if v.code&0x01 == 0 {
		v.readBuf = v.readTargetWord(targetForCode(v.code), v.addr)
	}
}

// targetForCode decodes the memory selected by a command code.
func targetForCode(code uint8) accessTarget {
	switch {
	case code&0x04 != 0:
		return targetVSRAM
	case code&0x02 != 0:
		return targetCRAM
	default:
		return targetVRAM
	}
}

// normalizeAddr applies the wrap rules of the selected memory to an
// internal address after an increment.
func normalizeAddr(target accessTarget, addr uint32) uint32 {
	switch target {
	case targetCRAM:
		return addr & 0x7F
	case targetVSRAM:
		return addr % 80
	default:
		return addr & 0xFFFF
	}
}

// readTargetWord reads one 16-bit word from the selected memory at a
// byte address, using the storage byte order of that memory: VRAM words
// are stored low byte first (the byte-swap quirk described at
// writeTargetWord), CRAM and VSRAM words are stored big-endian.
func (v *VDP) readTargetWord(target accessTarget, addr uint32) uint16 {
	switch target {
	case targetCRAM:
		a := addr & 0x7F
		return uint16(v.CRAM[a])<<8 | uint16(v.CRAM[(a+1)&0x7F])
	case targetVSRAM:
		a := addr % 80
		return uint16(v.VSRAM[a])<<8 | uint16(v.VSRAM[(a+1)%80])
	default:
		a := addr & 0xFFFF
		return uint16(v.VRAM[a]) | uint16(v.VRAM[(a+1)&0xFFFF])<<8
	}
}

// writeTargetWord stores one 16-bit word into the selected memory at a
// byte address. VRAM stores the word byte-swapped (low byte at addr,
// high byte at addr+1): the 68k data bus is wired to the VDP so that a
// word written by the CPU lands swapped in VRAM, and tile patterns are
// consequently read back linearly from VRAM when rendering. CRAM and
// VSRAM store the word big-endian, with no swap.
func (v *VDP) writeTargetWord(target accessTarget, addr uint32, val uint16) {
	switch target {
	case targetCRAM:
		a := addr & 0x7F
		v.CRAM[a] = uint8(val >> 8)
		v.CRAM[(a+1)&0x7F] = uint8(val)
	case targetVSRAM:
		a := addr % 80
		v.VSRAM[a] = uint8(val >> 8)
		v.VSRAM[(a+1)%80] = uint8(val)
	default:
		a := addr & 0xFFFF
		v.VRAM[a] = uint8(val)
		v.VRAM[(a+1)&0xFFFF] = uint8(val >> 8)
	}
}

// readData services a data port read. The first read after a read
// command returns the prefetched word; each read then advances the
// address by the auto-increment register and prefetches the next word.
func (v *VDP) readData() uint16 {
	if v.code&0x20 != 0 || v.code&0x01 != 0 {
		// Not a read command; the buffer still holds its last value.
		return v.readBuf
	}
	target := targetForCode(v.code)
	result := v.readBuf
	v.addr = normalizeAddr(target, v.addr+uint32(v.Regs[15]))
	v.readBuf = v.readTargetWord(target, v.addr)
	return result
}

// writeData services a data port write. If a VRAM fill DMA is armed, the
// written word is the fill data and triggers the fill instead.
func (v *VDP) writeData(val uint16) {
	if v.dmaFill {
		v.dmaFill = false
		v.performFill(val)
		return
	}
	if v.code&0x20 != 0 || v.code&0x01 == 0 {
		// DMA commands and read commands do not take data writes.
		return
	}
	target := targetForCode(v.code)
	v.writeTargetWord(target, v.addr, val)
	v.addr = normalizeAddr(target, v.addr+uint32(v.Regs[15]))
}

// dmaLengthWords returns the DMA length in words from registers 19/20;
// a programmed length of zero means 0x10000 words.
func (v *VDP) dmaLengthWords() int {
	n := int(v.Regs[19]) | int(v.Regs[20])<<8
	if n == 0 {
		return 0x10000
	}
	return n
}

// dmaSourceByteAddr returns the DMA source as a byte address: registers
// 21/22/23 hold a word address, so the byte address is that value
// shifted left by one. mask limits the high register to the bits that
// are address (0x7F normally; register 23's top two bits are the DMA
// mode, so a VRAM copy uses 0x3F).
func (v *VDP) dmaSourceByteAddr(mask uint8) uint32 {
	raw := uint32(v.Regs[21]) |
		uint32(v.Regs[22])<<8 |
		uint32(v.Regs[23]&mask)<<16
	return raw << 1
}

// handleDMACommand dispatches a completed DMA command (command code bit
// CD5 set) on the mode bits in register 23. DMA does nothing unless
// register 1 bit 4 (DMA enable) is set.
func (v *VDP) handleDMACommand() {
	if v.Regs[1]&0x10 == 0 {
		return
	}
	switch mode := v.Regs[23]; {
	case mode&0x80 == 0:
		v.perform68kDMA()
	case mode&0x40 == 0:
		// VRAM fill: the transfer waits for the next data port write,
		// which supplies the fill word. The length is latched now.
		if targetForCode(v.code) != targetVRAM {
			return
		}
		v.dmaLen = uint16(v.Regs[19]) | uint16(v.Regs[20])<<8
		v.dmaFill = true
	default:
		v.performCopyDMA()
	}
}

// perform68kDMA copies the programmed number of words from 68k address
// space (through the DMA source callback) into the selected memory.
// The destination advances by two bytes per word with the target's
// wrap rules; the auto-increment register does not apply to DMA.
func (v *VDP) perform68kDMA() {
	target := targetForCode(v.code)
	src := v.dmaSourceByteAddr(0x7F)
	v.dmaSrc = src
	v.dmaBusy = true
	for i, n := 0, v.dmaLengthWords(); i < n; i++ {
		var word uint16
		if v.dmaSource != nil {
			word = v.dmaSource(src)
		}
		v.writeTargetWord(target, v.addr, word)
		src += 2
		v.addr = normalizeAddr(target, v.addr+2)
	}
	v.dmaBusy = false
}

// performFill runs an armed VRAM fill: the supplied word is stored,
// byte-swapped like every VRAM word, at consecutive word addresses for
// the latched length.
func (v *VDP) performFill(fillWord uint16) {
	n := int(v.dmaLen)
	if n == 0 {
		n = 0x10000
	}
	v.dmaBusy = true
	for i := 0; i < n; i++ {
		v.writeTargetWord(targetVRAM, v.addr, fillWord)
		v.addr = normalizeAddr(targetVRAM, v.addr+2)
	}
	v.dmaBusy = false
}

// performCopyDMA copies words within VRAM from the DMA source address
// to the command address. Bytes move in stored order, so the copy is
// exact regardless of the VRAM word byte-swap.
func (v *VDP) performCopyDMA() {
	src := v.dmaSourceByteAddr(0x3F)
	v.dmaSrc = src
	v.dmaBusy = true
	for i, n := 0, v.dmaLengthWords(); i < n; i++ {
		s := src & 0xFFFF
		d := v.addr & 0xFFFF
		b0 := v.VRAM[s]
		b1 := v.VRAM[(s+1)&0xFFFF]
		v.VRAM[d] = b0
		v.VRAM[(d+1)&0xFFFF] = b1
		src += 2
		v.addr = normalizeAddr(targetVRAM, v.addr+2)
	}
	v.dmaBusy = false
}

// BeginFrame starts a new frame: the horizontal interrupt counter
// reloads from register 10.
func (v *VDP) BeginFrame() {
	v.hintCtr = int(v.Regs[10])
}

// EnterVBlank marks the start of vertical blank and reports whether the
// vertical blank interrupt (IRQ6) is enabled in register 1 bit 5.
func (v *VDP) EnterVBlank() bool {
	v.vblank = true
	return v.Regs[1]&0x20 != 0
}

// ExitVBlank marks the end of vertical blank.
func (v *VDP) ExitVBlank() {
	v.vblank = false
	v.frames++
}

// LineIRQ reports whether a horizontal blank interrupt (IRQ4) fires on
// scanline y. The counter reloads to register 10 each time it fires, so
// interrupts occur every register 10 + 1 active lines. The system calls
// this once per line; only active display lines count.
func (v *VDP) LineIRQ(y int) bool {
	if v.Regs[0]&0x04 == 0 {
		return false
	}
	if y < 0 || y >= v.DisplayHeight() {
		return false
	}
	if v.hintCtr == 0 {
		v.hintCtr = int(v.Regs[10])
		return true
	}
	v.hintCtr--
	return false
}

// DisplayWidth returns the horizontal resolution: 320 in H40 mode
// (register 12 bits 7 and 0 both set), otherwise 256.
func (v *VDP) DisplayWidth() int {
	if v.Regs[12]&0x81 == 0x81 {
		return 320
	}
	return 256
}

// DisplayHeight returns 240 when register 1 bit 3 selects the 240-line
// mode, otherwise 224.
func (v *VDP) DisplayHeight() int {
	if v.Regs[1]&0x08 != 0 {
		return 240
	}
	return 224
}

// Framebuffer returns the RGB565 frame. It is always MaxWidth wide; the
// system crops each line to DisplayWidth when presenting.
func (v *VDP) Framebuffer() []uint16 {
	return v.fb
}

// cramColor converts a CRAM palette index (0-63) to RGB565. A CRAM word
// is laid out 0000 BBB0 GGG0 RRR0: three bits per channel, scaled to
// the RGB565 channel ranges.
func (v *VDP) cramColor(index int) uint16 {
	a := (index * 2) & 0x7F
	word := uint16(v.CRAM[a])<<8 | uint16(v.CRAM[(a+1)&0x7F])
	r := uint32((word >> 1) & 0x07)
	g := uint32((word >> 5) & 0x07)
	b := uint32((word >> 9) & 0x07)
	return uint16(r*31/7)<<11 | uint16(g*63/7)<<5 | uint16(b*31/7)
}

// backdropColor returns the color behind every layer, selected by
// register 7 (palette in bits 5-4, color in bits 3-0).
func (v *VDP) backdropColor() uint16 {
	index := int((v.Regs[7]>>4)&0x03)*16 + int(v.Regs[7]&0x0F)
	return v.cramColor(index)
}

// vramWord reads a 16-bit word from VRAM in CPU order: because data
// port writes store VRAM words byte-swapped (low byte first), the word
// a program wrote is recovered as low | high<<8.
func (v *VDP) vramWord(addr int) uint16 {
	a := uint32(addr) & 0xFFFF
	return uint16(v.VRAM[a]) | uint16(v.VRAM[(a+1)&0xFFFF])<<8
}

// vsramWord reads a big-endian 16-bit word from VSRAM.
func (v *VDP) vsramWord(entry int) uint16 {
	a := (entry * 2) % 80
	return uint16(v.VSRAM[a])<<8 | uint16(v.VSRAM[(a+1)%80])
}

// planeCells decodes register 16 into the plane size in cells. The
// encoding is 0=32, 1=64, 2=invalid (treated as 128), 3=128 for both
// the width (bits 1-0) and the height (bits 5-4).
func (v *VDP) planeCells() (wCells, hCells int) {
	sizes := [4]int{32, 64, 128, 128}
	return sizes[v.Regs[16]&0x03], sizes[(v.Regs[16]>>4)&0x03]
}

// mod returns a mod m in the mathematical (always non-negative) sense,
// for wrapping scrolled plane coordinates.
func mod(a, m int) int {
	r := a % m
	if r < 0 {
		r += m
	}
	return r
}

// hScroll returns the horizontal scroll offset for a plane on line y.
// The scroll table holds one pair of words (plane A, then plane B) per
// row; register 11 bits 1-0 select full-screen (row 0), per-cell (one
// row per 8 lines) or per-line scrolling. The convention used here:
// screen pixel x shows plane pixel x + scroll, where scroll is the
// negated table value (the table stores the shift as a two's complement
// value that the hardware subtracts).
func (v *VDP) hScroll(planeB bool, y int) int {
	row := 0
	switch v.Regs[11] & 0x03 {
	case 2:
		row = y &^ 7
	case 3:
		row = y
	}
	base := int(v.Regs[13]&0x3F) << 10
	off := base + row*4
	if planeB {
		off += 2
	}
	return -int(int16(v.vramWord(off)))
}

// vScroll returns the vertical scroll offset for a plane at screen
// column x. Register 11 bit 2 selects full-screen scrolling (VSRAM
// entries 0 and 1) or per-16-pixel-column scrolling (entry pairs per
// column). Only the low 10 bits of each entry are significant.
func (v *VDP) vScroll(planeB bool, x int) int {
	entry := 0
	if v.Regs[11]&0x04 != 0 {
		entry = (x / 16) * 2
	}
	if planeB {
		entry++
	}
	return int(v.vsramWord(entry) & 0x3FF)
}

// windowAt reports whether screen pixel (x, y) falls inside the window
// plane's rectangle. Register 17 sets the horizontal edge in 16-pixel
// units (bit 7: window extends rightward from the edge instead of
// leftward from 0); register 18 sets the vertical edge in 8-line units
// (bit 7: window extends downward from the edge).
func (v *VDP) windowAt(x, y int) bool {
	hEdge := int(v.Regs[17]&0x1F) * 16
	vEdge := int(v.Regs[18]&0x1F) * 8
	inX := x < hEdge
	if v.Regs[17]&0x80 != 0 {
		inX = x >= hEdge
	}
	inY := y < vEdge
	if v.Regs[18]&0x80 != 0 {
		inY = y >= vEdge
	}
	return inX && inY
}

// tilePixel resolves one pixel of a tile given its name table entry and
// the pixel's position (0-7) inside the tile, applying the entry's flip
// bits. It returns the RGB565 color, the priority bit, and whether the
// pixel is opaque (palette index 0 of any palette is transparent).
func (v *VDP) tilePixel(entry uint16, tx, ty int) (uint16, bool, bool) {
	if entry&0x0800 != 0 { // horizontal flip
		tx = 7 - tx
	}
	if entry&0x1000 != 0 { // vertical flip
		ty = 7 - ty
	}
	patternAddr := int(entry&0x07FF)*32 + ty*4 + tx/2
	// VRAM words are stored byte-swapped (see writeTargetWord), so the
	// byte holding this pixel pair lives at the neighboring address.
	b := v.VRAM[(patternAddr^1)&0xFFFF]
	nibble := b >> 4
	if tx&1 != 0 {
		nibble = b & 0x0F
	}
	if nibble == 0 {
		return 0, false, false
	}
	palette := (entry >> 13) & 0x03
	return v.cramColor(int(palette)*16 + int(nibble)), entry&0x8000 != 0, true
}

// planePixel resolves one screen pixel of a background plane, applying
// scrolling and, for plane A, replacement by the window plane inside
// the window rectangle.
func (v *VDP) planePixel(planeB bool, x, y int) (uint16, bool, bool) {
	if !planeB && v.windowAt(x, y) {
		// The window plane is not scrolled. Its name table has a
		// fixed row stride: 64 cells in H40, 32 cells in H32.
		mask := uint8(0x7C)
		stride := 64
		if v.DisplayWidth() == 256 {
			mask = 0x78
			stride = 32
		}
		base := int(v.Regs[3]&mask) << 10
		entry := v.vramWord(base + ((y/8)*stride+(x/8))*2)
		return v.tilePixel(entry, x&7, y&7)
	}

	wCells, hCells := v.planeCells()
	srcX := mod(x+v.hScroll(planeB, y), wCells*8)
	srcY := mod(y+v.vScroll(planeB, x), hCells*8)

	var base int
	if planeB {
		base = int(v.Regs[4]&0x07) << 13
	} else {
		base = int(v.Regs[2]&0x38) << 10
	}
	entry := v.vramWord(base + ((srcY/8)*wCells+(srcX/8))*2)
	return v.tilePixel(entry, srcX&7, srcY&7)
}

// renderSprites draws one scanline of sprites into the low and high
// priority sprite layers. Sprites are visited in link-chain order
// starting from sprite 0; within a priority layer the earliest sprite
// wins a pixel, as on hardware. Per line at most 20 sprites (H40) or
// 16 (H32) are drawn, loosely applying the hardware sprite pixel
// budget of one display width.
//
// Sprite attribute format (8 bytes per sprite, read here as four
// words): word 0 is the Y position (bits 9-0, biased by 128); word 1
// holds the size byte in its high half (bits 11-10 horizontal cells-1,
// bits 9-8 vertical cells-1) and the link byte in its low half (bits
// 6-0, the next sprite's index); word 2 is the pattern attribute
// (bit 15 priority, bits 14-13 palette, bit 12 vertical flip, bit 11
// horizontal flip, bits 10-0 first tile); word 3 is the X position
// (bits 8-0, biased by 128). Tiles inside a sprite are arranged
// column-major: the tile index increases down a column first.
func (v *VDP) renderSprites(y int, low, high *[MaxWidth]uint16, lowOK, highOK *[MaxWidth]bool) {
	width := v.DisplayWidth()
	maxSprites, lineLimit := 80, 20
	if width == 256 {
		maxSprites, lineLimit = 64, 16
	}
	base := int(v.Regs[5]&0x7E) << 9
	pixelBudget := width
	drawn := 0

	var visited [80]bool
	index := 0
	for n := 0; n < maxSprites; n++ {
		if index < 0 || index >= maxSprites || visited[index] {
			break
		}
		visited[index] = true
		entryBase := base + index*8
		yWord := v.vramWord(entryBase)
		sizeLink := v.vramWord(entryBase + 2)
		attr := v.vramWord(entryBase + 4)
		xWord := v.vramWord(entryBase + 6)
		link := int(sizeLink & 0x7F)

		hCells := int((sizeLink>>10)&0x03) + 1
		vCells := int((sizeLink>>8)&0x03) + 1
		top := int(yWord&0x03FF) - 128
		left := int(xWord&0x01FF) - 128
		sprW, sprH := hCells*8, vCells*8

		if y >= top && y < top+sprH && left < width && left+sprW > 0 {
			drawn++
			if drawn > lineLimit {
				break
			}
			layer, layerOK := low, lowOK
			if attr&0x8000 != 0 {
				layer, layerOK = high, highOK
			}
			sy := y - top
			for sx := 0; sx < sprW && pixelBudget > 0; sx++ {
				pixelBudget--
				screenX := left + sx
				if screenX < 0 || screenX >= width {
					continue
				}
				// Work in unflipped sprite space so flips move
				// both the tile order and the pixels inside a
				// tile, as the hardware does.
				srcX, srcY := sx, sy
				if attr&0x0800 != 0 { // horizontal flip
					srcX = sprW - 1 - sx
				}
				if attr&0x1000 != 0 { // vertical flip
					srcY = sprH - 1 - sy
				}
				tile := int(attr&0x07FF) + (srcX/8)*vCells + srcY/8
				tx, ty := srcX&7, srcY&7
				patternAddr := tile*32 + ty*4 + tx/2
				b := v.VRAM[(patternAddr^1)&0xFFFF] // byte-swapped storage, as in tilePixel
				nibble := b >> 4
				if tx&1 != 0 {
					nibble = b & 0x0F
				}
				if nibble == 0 || layerOK[screenX] {
					continue
				}
				palette := (attr >> 13) & 0x03
				layer[screenX] = v.cramColor(int(palette)*16 + int(nibble))
				layerOK[screenX] = true
			}
			if pixelBudget <= 0 {
				break
			}
		}

		if link == index {
			break
		}
		index = link
	}
}

// RenderLine renders one visible scanline into the framebuffer. Layers
// compose back to front in priority order: backdrop, plane B low,
// plane A low, sprites low, plane B high, plane A high, sprites high;
// a layer pixel covers what is below only when it is opaque.
func (v *VDP) RenderLine(y int) {
	if y < 0 || y >= MaxHeight || y >= v.DisplayHeight() {
		return
	}
	v.currentLn = y
	width := v.DisplayWidth()
	line := v.fb[y*MaxWidth : y*MaxWidth+MaxWidth]
	backdrop := v.backdropColor()

	if v.Regs[1]&0x40 == 0 { // display disabled
		for x := 0; x < width; x++ {
			line[x] = backdrop
		}
		return
	}

	var aCol, bCol, sLow, sHigh [MaxWidth]uint16
	var aOK, bOK, sLowOK, sHighOK [MaxWidth]bool
	var aPri, bPri [MaxWidth]bool

	for x := 0; x < width; x++ {
		if c, pri, ok := v.planePixel(true, x, y); ok {
			bCol[x], bPri[x], bOK[x] = c, pri, ok
		}
		if c, pri, ok := v.planePixel(false, x, y); ok {
			aCol[x], aPri[x], aOK[x] = c, pri, ok
		}
	}
	v.renderSprites(y, &sLow, &sHigh, &sLowOK, &sHighOK)

	for x := 0; x < width; x++ {
		c := backdrop
		if bOK[x] && !bPri[x] {
			c = bCol[x]
		}
		if aOK[x] && !aPri[x] {
			c = aCol[x]
		}
		if sLowOK[x] {
			c = sLow[x]
		}
		if bOK[x] && bPri[x] {
			c = bCol[x]
		}
		if aOK[x] && aPri[x] {
			c = aCol[x]
		}
		if sHighOK[x] {
			c = sHigh[x]
		}
		line[x] = c
	}
}

// SerializeSize returns the byte size of a serialized VDP: registers,
// VRAM, CRAM, VSRAM, then the command state (addr, code, pending,
// first, readBuf).
func (v *VDP) SerializeSize() int {
	return len(v.Regs) + len(v.VRAM) + len(v.CRAM) + len(v.VSRAM) + 4 + 1 + 1 + 2 + 2
}

// Serialize writes the VDP state into buf in this exact order:
// Regs(24), VRAM(64K), CRAM(128), VSRAM(80), addr(4 LE), code(1),
// pending(1), first(2 LE), readBuf(2 LE).
func (v *VDP) Serialize(buf []byte) error {
	if len(buf) < v.SerializeSize() {
		return errors.New("vdp: serialize buffer too small")
	}
	off := 0
	off += copy(buf[off:], v.Regs[:])
	off += copy(buf[off:], v.VRAM[:])
	off += copy(buf[off:], v.CRAM[:])
	off += copy(buf[off:], v.VSRAM[:])
	binary.LittleEndian.PutUint32(buf[off:], v.addr)
	off += 4
	buf[off] = v.code
	off++
	if v.pending {
		buf[off] = 1
	} else {
		buf[off] = 0
	}
	off++
	binary.LittleEndian.PutUint16(buf[off:], v.first)
	off += 2
	binary.LittleEndian.PutUint16(buf[off:], v.readBuf)
	return nil
}

// Deserialize reads state written by Serialize and clears the transient
// vblank and DMA state.
func (v *VDP) Deserialize(buf []byte) error {
	if len(buf) < v.SerializeSize() {
		return errors.New("vdp: deserialize buffer too small")
	}
	off := 0
	off += copy(v.Regs[:], buf[off:])
	off += copy(v.VRAM[:], buf[off:])
	off += copy(v.CRAM[:], buf[off:])
	off += copy(v.VSRAM[:], buf[off:])
	v.addr = binary.LittleEndian.Uint32(buf[off:])
	off += 4
	v.code = buf[off]
	off++
	v.pending = buf[off] != 0
	off++
	v.first = binary.LittleEndian.Uint16(buf[off:])
	off += 2
	v.readBuf = binary.LittleEndian.Uint16(buf[off:])

	v.vblank = false
	v.dmaBusy = false
	v.dmaFill = false
	v.dmaSrc = 0
	v.dmaLen = 0
	return nil
}
