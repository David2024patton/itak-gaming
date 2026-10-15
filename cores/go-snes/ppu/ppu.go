// Package ppu is the SNES picture processing unit: the
// register file ($2100-$213F), VRAM, CGRAM, and OAM, and the
// renderer. Rendering is evaluated per scanline: at the end
// of each display line the machine asks for that line, and
// the PPU composes it from the register and memory state as
// it stands at that moment. Register changes made mid-line
// by the CPU therefore take effect from the following line,
// not partway through the current one; that is the one
// documented timing simplification of this slice (SNES-2).
// Sprite and background fetches, windows, and color math
// otherwise follow the published register documentation
// (the SNES development manual material as collected in
// fullsnes and the SNESdev wiki).
package ppu

import (
	"encoding/binary"
	"errors"
)

// Display geometry of the framebuffer this core produces.
const (
	Width  = 256
	Height = 224
)

// VRAM translation and increment helpers.

// translate applies the VMAIN address translation (the
// rotate modes used by games uploading interleaved tile
// data) to a VRAM word address.
func (p *PPU) translate(addr uint16) uint16 {
	switch p.vmIncTrans {
	case 1: // 8-bit rotation
		return addr&0xFF00 | (addr&0x00FF)>>3 | (addr&0x07)<<5
	case 2: // 9-bit rotation
		return addr&0xFE00 | (addr&0x01FF)>>3 | (addr&0x07)<<6
	case 3: // 10-bit rotation
		return addr&0xFC00 | (addr&0x03FF)>>3 | (addr&0x07)<<7
	}
	return addr
}

var vmIncAmount = [4]uint16{1, 32, 128, 128}

// bgLayer holds one background layer's register state.
type bgLayer struct {
	tilemapBase uint16 // word address of the tilemap
	tilemapW    int    // in tiles: 32 or 64
	tilemapH    int
	chrBase     uint16 // word address of the tile data
	hofs, vofs  uint16 // raw latch-formula values; masked at use
	tile16      bool   // 16x16 tiles (mode 5/6 force 16 wide)
}

// PPU is the whole picture unit.
type PPU struct {
	vram  [0x8000]uint16
	cgram [256]uint16
	oam   [544]byte
	frame []uint32

	// $2100 INIDISP.
	forcedBlank bool
	brightness  uint8

	// $2101 OBSEL.
	objBase uint16 // word address
	objGap  uint16 // word offset of the second tile page
	objSize int    // size select 0..7

	// OAM access.
	oamAddr    int  // byte address, wraps at 1024
	oamPrioRot bool // $2103 bit 7: priority rotation
	oamBuf     byte // buffered even byte for $2104 writes

	// $2105 BGMODE, $2106 mosaic.
	bgMode     uint8
	bg3PrioBit bool
	mosaicSize int // 1..16
	mosaicBG   uint8

	bg [4]bgLayer

	// Scroll write latches (the documented shared-latch
	// formula: one latch for the previous byte across all
	// scroll registers, plus per-axis low-bit latches).
	bgofsLatch uint8
	hofsLatch  uint8
	vofsLatch  uint8

	// $2115 VMAIN.
	vmIncHigh  bool   // increment after $2119 (else after $2118)
	vmIncAmt   uint16 // 1, 32, or 128
	vmIncTrans uint8

	// VRAM port.
	vmAddr     uint16 // word address, 15 bits
	vmPrefetch uint16

	// $211A M7SEL and the matrix.
	m7FlipX, m7FlipY   bool
	m7Over             uint8 // screen-over mode 0..3
	m7a, m7b, m7c, m7d int16
	m7x, m7y           uint16 // 13-bit values, sign-extended at use
	m7Latch            uint8
	mpy                int32 // last Mode 7 multiply result

	// CGRAM port.
	cgAddr     uint8 // color index
	cgBuf      byte  // buffered low byte for $2122 writes
	cgHighNext bool  // next $2122 write is the high byte
	cgReadHigh bool  // next $213B read is the high byte

	// Windows.
	w12sel, w34sel, wobjsel uint8
	wh                      [4]uint8 // left/right of window 1, left/right of window 2
	wbglog, wobjlog         uint8
	tm, ts, tmw, tsw        uint8

	// Color math.
	cgwsel uint8
	cgadsb uint8
	fixedR uint8 // 5-bit fixed color channels from $2132
	fixedG uint8
	fixedB uint8

	// $2133 SETINI.
	setINI uint8

	// Counters and status.
	hLatch, vLatch uint16
	ophctHigh      bool // next $213C read returns the high bit
	opvctHigh      bool
	latchFlag      bool // counters latched ($2137 or pin) since last $213F read
	rangeOver      bool
	timeOver       bool
	field          bool // interlace field for $213F

	// Set by the machine at every sync: whether a CPU write
	// right now would land in the active display area (used
	// to gate CGRAM writes the way the hardware drops them
	// mid-display).
	displayActive bool

	// Scanline composition scratch, owned by the renderer
	// and rewritten on every RenderLine: it is derived
	// state, never saved, and exists so a frame of
	// composition allocates nothing.
	lbBG      [4]lineBuf
	lbObj     lineBuf
	lbMain    lineBuf
	lbSub     lineBuf
	winIn     [6][512]bool
	blackAt   [512]bool
	subRegAt  [512]bool
	objOnLine [33]objSpr
	objDraw   [33]objSpr
	rowMemo   [128]tileRowEntry
	lineStamp uint32
	expTab    [32768]uint32
	expBright uint8
	expValid  bool
}

// New returns a PPU in its power-on state.
func New() *PPU {
	p := &PPU{frame: make([]uint32, Width*Height)}
	p.mosaicSize = 1
	p.vmIncAmt = 1
	return p
}

// Framebuffer returns the current picture: 256 by 224
// pixels in XRGB8888.
func (p *PPU) Framebuffer() []uint32 { return p.frame }

// SetDisplayActive tells the PPU whether the machine is
// currently inside the active display area (a visible line,
// before HBlank, not forced blank). The hardware drops
// CGRAM writes that land there; the machine tracks the dot
// position, the PPU just records the verdict.
func (p *PPU) SetDisplayActive(active bool) { p.displayActive = active }

// ForcedBlank reports whether INIDISP bit 7 forces the
// picture blank.
func (p *PPU) ForcedBlank() bool { return p.forcedBlank }

// Overscan reports whether SETINI bit 3 selects the
// 239-line picture.
func (p *PPU) Overscan() bool { return p.setINI&0x08 != 0 }

// Interlace reports whether SETINI bit 0 selects interlace.
func (p *PPU) Interlace() bool { return p.setINI&0x01 != 0 }

// Field returns the current interlace field.
func (p *PPU) Field() bool { return p.field }

// SetField records the interlace field parity the machine
// is running (it owns the frame timing).
func (p *PPU) SetField(f bool) { p.field = f }

// ClearSpriteFlags resets the range-over and time-over
// flags; the machine calls this at the frame boundary the
// documentation gives them.
func (p *PPU) ClearSpriteFlags() { p.rangeOver, p.timeOver = false, false }

// LatchCounters captures the H and V counters (the $2137
// read); the machine owns the position and passes it in.
func (p *PPU) LatchCounters(h, v uint16) {
	p.hLatch, p.vLatch = h, v
	p.latchFlag = true
}

// vramWord reads one VRAM word for the renderer and tests.
func (p *PPU) vramWord(addr uint16) uint16 { return p.vram[addr&0x7FFF] }

// WriteReg handles a CPU write to $2100-$213F.
func (p *PPU) WriteReg(addr uint16, val uint8) {
	switch {
	case addr == 0x2100: // INIDISP
		p.forcedBlank = val&0x80 != 0
		p.brightness = val & 0x0F
	case addr == 0x2101: // OBSEL
		p.objBase = uint16(val&0x03) << 13
		p.objGap = (uint16((val>>3)&0x03) + 1) << 12
		p.objSize = int(val >> 5)
	case addr == 0x2102: // OAMADDL
		p.oamAddr = (p.oamAddr & 0x200) | int(val)<<1
	case addr == 0x2103: // OAMADDH
		p.oamAddr = (p.oamAddr & 0x1FE) | int(val&0x01)<<9
		p.oamPrioRot = val&0x80 != 0
	case addr == 0x2104: // OAMDATA
		p.writeOAM(val)
	case addr == 0x2105: // BGMODE
		p.bgMode = val & 0x07
		p.bg3PrioBit = val&0x08 != 0
		p.bg[0].tile16 = val&0x10 != 0
		p.bg[1].tile16 = val&0x20 != 0
		p.bg[2].tile16 = val&0x40 != 0
		p.bg[3].tile16 = val&0x80 != 0
	case addr == 0x2106: // MOSAIC: size in the high nibble,
		// per-layer enables (BG1 is bit 0) in the low
		p.mosaicSize = int(val>>4) + 1
		p.mosaicBG = val & 0x0F
	case addr >= 0x2107 && addr <= 0x210A: // BGnSC
		b := &p.bg[addr-0x2107]
		b.tilemapBase = uint16(val&0xFC) << 8
		switch val & 0x03 {
		case 0:
			b.tilemapW, b.tilemapH = 32, 32
		case 1:
			b.tilemapW, b.tilemapH = 64, 32
		case 2:
			b.tilemapW, b.tilemapH = 32, 64
		case 3:
			b.tilemapW, b.tilemapH = 64, 64
		}
	case addr == 0x210B: // BG12NBA
		p.bg[0].chrBase = uint16(val&0x0F) << 12
		p.bg[1].chrBase = uint16(val>>4) << 12
	case addr == 0x210C: // BG34NBA
		p.bg[2].chrBase = uint16(val&0x0F) << 12
		p.bg[3].chrBase = uint16(val>>4) << 12
	case addr >= 0x210D && addr <= 0x2114: // scroll registers
		p.writeScroll(int(addr-0x210D), val)
	case addr == 0x2115: // VMAIN
		p.vmIncHigh = val&0x80 != 0
		p.vmIncAmt = vmIncAmount[val&0x03]
		p.vmIncTrans = (val >> 2) & 0x03
	case addr == 0x2116: // VMADDL
		p.vmAddr = (p.vmAddr & 0x7F00) | uint16(val)
	case addr == 0x2117: // VMADDH
		p.vmAddr = (p.vmAddr & 0x00FF) | uint16(val&0x7F)<<8
	case addr == 0x2118: // VMDATAL
		ta := p.translate(p.vmAddr)
		p.vram[ta] = (p.vram[ta] & 0xFF00) | uint16(val)
		if !p.vmIncHigh {
			p.vmAddr = (p.vmAddr + p.vmIncAmt) & 0x7FFF
		}
	case addr == 0x2119: // VMDATAH
		ta := p.translate(p.vmAddr)
		p.vram[ta] = (p.vram[ta] & 0x00FF) | uint16(val)<<8
		if p.vmIncHigh {
			p.vmAddr = (p.vmAddr + p.vmIncAmt) & 0x7FFF
		}
	case addr == 0x211A: // M7SEL
		p.m7Over = val & 0x03
		p.m7FlipX = val&0x40 != 0
		p.m7FlipY = val&0x80 != 0
	case addr >= 0x211B && addr <= 0x2120: // Mode 7 registers
		p.writeMode7(int(addr-0x211B), val)
	case addr == 0x2121: // CGADD
		p.cgAddr = val
		p.cgHighNext = false
	case addr == 0x2122: // CGDATA
		p.writeCGRAM(val)
	case addr == 0x2123: // W12SEL
		p.w12sel = val
	case addr == 0x2124: // W34SEL
		p.w34sel = val
	case addr == 0x2125: // WOBJSEL
		p.wobjsel = val
	case addr >= 0x2126 && addr <= 0x2129: // WH0..WH3
		p.wh[addr-0x2126] = val
	case addr == 0x212A: // WBGLOG
		p.wbglog = val
	case addr == 0x212B: // WOBJLOG
		p.wobjlog = val
	case addr == 0x212C: // TM
		p.tm = val & 0x1F
	case addr == 0x212D: // TS
		p.ts = val & 0x1F
	case addr == 0x212E: // TMW
		p.tmw = val & 0x1F
	case addr == 0x212F: // TSW
		p.tsw = val & 0x1F
	case addr == 0x2130: // CGWSEL
		p.cgwsel = val
	case addr == 0x2131: // CGADSUB
		p.cgadsb = val
	case addr == 0x2132: // COLDATA
		if val&0x20 != 0 {
			p.fixedR = val & 0x1F
		}
		if val&0x40 != 0 {
			p.fixedG = val & 0x1F
		}
		if val&0x80 != 0 {
			p.fixedB = val & 0x1F
		}
	case addr == 0x2133: // SETINI
		p.setINI = val
	}
	// All other $21xx writes (read-only registers, the
	// multiply outputs, and the rest) are dropped.
}

// oamIndex folds a byte address into the 544-byte OAM.
func oamIndex(addr int) int {
	addr %= 1024
	if addr >= 544 {
		addr -= 544
	}
	return addr
}

// writeOAM implements the $2104 buffered pair write: the
// even byte is held in the internal buffer and only lands
// (together with the odd byte that follows it) when the
// odd byte is written; the address then advances.
func (p *PPU) writeOAM(val uint8) {
	if p.oamAddr&1 == 0 {
		p.oamBuf = val
	} else {
		p.oam[oamIndex(p.oamAddr-1)] = p.oamBuf
		p.oam[oamIndex(p.oamAddr)] = val
	}
	// The byte pointer advances on every write; only the
	// word address (for priority rotation) stands still
	// between the two halves of a pair.
	p.oamAddr = (p.oamAddr + 1) & 0x3FF
}

// writeScroll implements the shared-latch write formula
// for $210D-$2114: the value written becomes the high byte,
// the low bits come from the previous scroll write (one
// latch shared by all eight registers, one low-3-bit latch
// per axis). Registers: 0 BG1H, 1 BG1V (also Mode 7
// scrolls), 2 BG2H, 3 BG2V, 4 BG3H, 5 BG3V, 6 BG4H, 7 BG4V.
func (p *PPU) writeScroll(idx int, val uint8) {
	bg := &p.bg[idx/2]
	if idx%2 == 0 { // horizontal
		bg.hofs = (uint16(val) << 8) | uint16(p.bgofsLatch&0xF8) | uint16(p.hofsLatch&0x07)
		p.hofsLatch = val
	} else { // vertical
		bg.vofs = (uint16(val) << 8) | uint16(p.bgofsLatch&0xF8) | uint16(p.vofsLatch&0x07)
		p.vofsLatch = val
	}
	p.bgofsLatch = val
}

// writeMode7 implements the $211B-$2120 register writes,
// all sharing the one-byte latch: 0 M7A, 1 M7B, 2 M7C,
// 3 M7D, 4 M7X, 5 M7Y. Writing M7B also updates the
// multiply result ($2134-$2136): M7A times the high byte
// of M7B, signed, 24 bits.
func (p *PPU) writeMode7(idx int, val uint8) {
	full := uint16(val)<<8 | uint16(p.m7Latch)
	p.m7Latch = val
	switch idx {
	case 0:
		p.m7a = int16(full)
	case 1:
		p.m7b = int16(full)
		p.mpy = int32(p.m7a) * int32(int8(p.m7b>>8))
	case 2:
		p.m7c = int16(full)
	case 3:
		p.m7d = int16(full)
	case 4:
		p.m7x = full & 0x1FFF
	case 5:
		p.m7y = full & 0x1FFF
	}
}

// writeCGRAM implements the $2122 write: pairs of bytes
// form one 15-bit color, and a write that lands in the
// active display area is dropped, as on hardware.
func (p *PPU) writeCGRAM(val uint8) {
	if !p.cgHighNext {
		p.cgBuf = val
		p.cgHighNext = true
		return
	}
	p.cgHighNext = false
	if p.displayActive {
		// The address still advances; the write is lost.
		p.cgAddr++
		return
	}
	p.cgram[p.cgAddr] = uint16(val&0x7F)<<8 | uint16(p.cgBuf)
	p.cgAddr++
}

// ReadReg handles a CPU read of $2134-$213F, given the
// machine's open-bus byte for the fields hardware leaves
// undriven.
func (p *PPU) ReadReg(addr uint16, openBus uint8) uint8 {
	switch addr {
	case 0x2134: // MPYL
		return uint8(p.mpy)
	case 0x2135: // MPYM
		return uint8(p.mpy >> 8)
	case 0x2136: // MPYH
		return uint8(p.mpy >> 16)
	case 0x2137: // SLHV (the machine latches via LatchCounters)
		return openBus
	case 0x2138: // OAMDATAREAD
		v := p.oam[oamIndex(p.oamAddr)]
		if p.oamAddr&1 == 1 {
			p.oamAddr = (p.oamAddr + 1) & 0x3FF
		} else {
			p.oamAddr++
		}
		return v
	case 0x2139: // VMDATALREAD
		v := uint8(p.vmPrefetch)
		w := p.vram[p.translate(p.vmAddr)]
		p.vmPrefetch = (p.vmPrefetch & 0xFF00) | (w & 0x00FF)
		if !p.vmIncHigh {
			p.vmAddr = (p.vmAddr + p.vmIncAmt) & 0x7FFF
		}
		return v
	case 0x213A: // VMDATAHREAD
		v := uint8(p.vmPrefetch >> 8)
		w := p.vram[p.translate(p.vmAddr)]
		p.vmPrefetch = (p.vmPrefetch & 0x00FF) | (w & 0xFF00)
		if p.vmIncHigh {
			p.vmAddr = (p.vmAddr + p.vmIncAmt) & 0x7FFF
		}
		return v
	case 0x213B: // CGDATAREAD
		var v uint8
		if p.cgReadHigh {
			v = uint8(p.cgram[p.cgAddr] >> 8)
			p.cgAddr++
			p.cgReadHigh = false
		} else {
			v = uint8(p.cgram[p.cgAddr])
			p.cgReadHigh = true
		}
		return v
	case 0x213C: // OPHCT
		if p.ophctHigh {
			p.ophctHigh = false
			return uint8(p.hLatch>>8) & 0x01
		}
		p.ophctHigh = true
		return uint8(p.hLatch)
	case 0x213D: // OPVCT
		if p.opvctHigh {
			p.opvctHigh = false
			return uint8(p.vLatch>>8) & 0x01
		}
		p.opvctHigh = true
		return uint8(p.vLatch)
	case 0x213E: // STAT77
		v := uint8(1) // PPU1 version
		if p.timeOver {
			v |= 0x40
		}
		if p.rangeOver {
			v |= 0x80
		}
		return v
	case 0x213F: // STAT78
		v := uint8(3) // PPU2 version (NTSC part)
		if p.field {
			v |= 0x40
		}
		if p.latchFlag {
			v |= 0x80
		}
		p.latchFlag = false
		p.ophctHigh = false
		p.opvctHigh = false
		return v
	}
	return openBus
}

// The register block serialized below is fixed-size; the
// memory arrays are serialized by the same block layout in
// the same order the fields appear here.

// serializeSize is the PPU's save-state size: VRAM
// (64 KB), CGRAM (512), OAM (544), then registers.
const serializeRegsSize = 256
const SerializeSize = 0x8000*2 + 256*2 + 544 + serializeRegsSize

// Serialize writes the PPU state into buf, which must be
// at least SerializeSize.
func (p *PPU) Serialize(buf []byte) {
	off := 0
	for _, w := range p.vram {
		binary.LittleEndian.PutUint16(buf[off:], w)
		off += 2
	}
	for _, w := range p.cgram {
		binary.LittleEndian.PutUint16(buf[off:], w)
		off += 2
	}
	copy(buf[off:], p.oam[:])
	off += len(p.oam)
	r := buf[off : off+serializeRegsSize]
	for i := range r {
		r[i] = 0
	}
	put16 := func(o int, v uint16) { binary.LittleEndian.PutUint16(r[o:], v) }
	put32 := func(o int, v int32) { binary.LittleEndian.PutUint32(r[o:], uint32(v)) }
	if p.forcedBlank {
		r[0] |= 1
	}
	r[1] = p.brightness
	put16(2, p.objBase)
	put16(4, p.objGap)
	r[6] = uint8(p.objSize)
	put16(7, uint16(p.oamAddr))
	if p.oamPrioRot {
		r[9] |= 1
	}
	r[10] = p.oamBuf
	r[11] = p.bgMode
	if p.bg3PrioBit {
		r[12] |= 1
	}
	r[13] = uint8(p.mosaicSize)
	r[14] = p.mosaicBG
	for i := range p.bg {
		b := 16 + i*10
		put16(b, p.bg[i].tilemapBase)
		r[b+2] = uint8(p.bg[i].tilemapW)
		r[b+3] = uint8(p.bg[i].tilemapH)
		put16(b+4, p.bg[i].chrBase)
		put16(b+6, p.bg[i].hofs)
		put16(b+8, p.bg[i].vofs)
		if p.bg[i].tile16 {
			r[15] |= 1 << i
		}
	}
	r[56] = p.bgofsLatch
	r[57] = p.hofsLatch
	r[58] = p.vofsLatch
	if p.vmIncHigh {
		r[59] |= 1
	}
	put16(60, p.vmIncAmt)
	r[62] = p.vmIncTrans
	put16(63, p.vmAddr)
	put16(65, p.vmPrefetch)
	if p.m7FlipX {
		r[67] |= 1
	}
	if p.m7FlipY {
		r[67] |= 2
	}
	r[68] = p.m7Over
	put16(69, uint16(p.m7a))
	put16(71, uint16(p.m7b))
	put16(73, uint16(p.m7c))
	put16(75, uint16(p.m7d))
	put16(77, p.m7x)
	put16(79, p.m7y)
	r[81] = p.m7Latch
	put32(82, p.mpy)
	r[86] = p.cgAddr
	r[87] = p.cgBuf
	if p.cgHighNext {
		r[88] |= 1
	}
	if p.cgReadHigh {
		r[88] |= 2
	}
	r[89], r[90], r[91] = p.w12sel, p.w34sel, p.wobjsel
	copy(r[92:], p.wh[:])
	r[96], r[97] = p.wbglog, p.wobjlog
	r[98], r[99], r[100], r[101] = p.tm, p.ts, p.tmw, p.tsw
	r[102], r[103] = p.cgwsel, p.cgadsb
	r[104], r[105], r[106] = p.fixedR, p.fixedG, p.fixedB
	r[107] = p.setINI
	put16(108, p.hLatch)
	put16(110, p.vLatch)
	if p.ophctHigh {
		r[112] |= 1
	}
	if p.opvctHigh {
		r[112] |= 2
	}
	if p.latchFlag {
		r[112] |= 4
	}
	if p.rangeOver {
		r[112] |= 8
	}
	if p.timeOver {
		r[112] |= 16
	}
	if p.field {
		r[112] |= 32
	}
	if p.displayActive {
		r[112] |= 64
	}
}

// Deserialize restores a state written by Serialize.
func (p *PPU) Deserialize(buf []byte) error {
	if len(buf) < SerializeSize {
		return errors.New("ppu: state too small")
	}
	off := 0
	for i := range p.vram {
		p.vram[i] = binary.LittleEndian.Uint16(buf[off:])
		off += 2
	}
	for i := range p.cgram {
		p.cgram[i] = binary.LittleEndian.Uint16(buf[off:])
		off += 2
	}
	copy(p.oam[:], buf[off:])
	off += len(p.oam)
	r := buf[off : off+serializeRegsSize]
	get16 := func(o int) uint16 { return binary.LittleEndian.Uint16(r[o:]) }
	p.forcedBlank = r[0]&1 != 0
	p.brightness = r[1]
	p.objBase = get16(2)
	p.objGap = get16(4)
	p.objSize = int(r[6])
	p.oamAddr = int(get16(7))
	p.oamPrioRot = r[9]&1 != 0
	p.oamBuf = r[10]
	p.bgMode = r[11]
	p.bg3PrioBit = r[12]&1 != 0
	p.mosaicSize = int(r[13])
	p.mosaicBG = r[14]
	for i := range p.bg {
		b := 16 + i*10
		p.bg[i].tilemapBase = get16(b)
		p.bg[i].tilemapW = int(r[b+2])
		p.bg[i].tilemapH = int(r[b+3])
		p.bg[i].chrBase = get16(b + 4)
		p.bg[i].hofs = get16(b + 6)
		p.bg[i].vofs = get16(b + 8)
		p.bg[i].tile16 = r[15]&(1<<i) != 0
	}
	p.bgofsLatch = r[56]
	p.hofsLatch = r[57]
	p.vofsLatch = r[58]
	p.vmIncHigh = r[59]&1 != 0
	p.vmIncAmt = get16(60)
	p.vmIncTrans = r[62]
	p.vmAddr = get16(63)
	p.vmPrefetch = get16(65)
	p.m7FlipX = r[67]&1 != 0
	p.m7FlipY = r[67]&2 != 0
	p.m7Over = r[68]
	p.m7a = int16(get16(69))
	p.m7b = int16(get16(71))
	p.m7c = int16(get16(73))
	p.m7d = int16(get16(75))
	p.m7x = get16(77)
	p.m7y = get16(79)
	p.m7Latch = r[81]
	p.mpy = int32(binary.LittleEndian.Uint32(r[82:]))
	p.cgAddr = r[86]
	p.cgBuf = r[87]
	p.cgHighNext = r[88]&1 != 0
	p.cgReadHigh = r[88]&2 != 0
	p.w12sel, p.w34sel, p.wobjsel = r[89], r[90], r[91]
	copy(p.wh[:], r[92:96])
	p.wbglog, p.wobjlog = r[96], r[97]
	p.tm, p.ts, p.tmw, p.tsw = r[98], r[99], r[100], r[101]
	p.cgwsel, p.cgadsb = r[102], r[103]
	p.fixedR, p.fixedG, p.fixedB = r[104], r[105], r[106]
	p.setINI = r[107]
	p.hLatch = get16(108)
	p.vLatch = get16(110)
	p.ophctHigh = r[112]&1 != 0
	p.opvctHigh = r[112]&2 != 0
	p.latchFlag = r[112]&4 != 0
	p.rangeOver = r[112]&8 != 0
	p.timeOver = r[112]&16 != 0
	p.field = r[112]&32 != 0
	p.displayActive = r[112]&64 != 0
	return nil
}
