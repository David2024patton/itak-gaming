package scd

import "encoding/binary"

// ---------------------------------------------------------------------------
// Graphics ASIC (rotation and scaling), SCD-3.
//
// The ASIC register file lives in the sub CPU register page only,
// at offsets 0x58 to 0x67 (sub CPU addresses 0xFF8058 to 0xFF8067).
// The main CPU has no access to it on the documented hardware: the
// main side window at 0xA12010 to 0xA1202F is the communication
// register area (SCD-1), not the ASIC. Register map, from the
// published register documentation (the public Sega CD ASIC
// register list, cross-checked against the homebrew gate array
// headers that name the same offsets):
//
//	0x58  stamp size and status (word): bit15 GRON (read only,
//	      1 while a graphics operation is running), bit2 SMS
//	      (stamp map size), bit1 STS (stamp size), bit0 RPT
//	      (repeat stamp map)
//	0x5A  stamp map base address: Word RAM byte address / 4
//	0x5C  image buffer vertical cell size: low byte is the image
//	      buffer column height in 8 dot cells, minus 1
//	0x5E  image buffer start address: Word RAM byte address / 4
//	0x60  image buffer offset: low byte bits 0-2 horizontal dot
//	      offset, bits 3-5 vertical dot offset
//	0x62  image buffer horizontal dot size (9 bits)
//	0x64  image buffer vertical dot size: low byte; hardware
//	      counts it down per rendered line
//	0x66  trace table base address: Word RAM byte address / 4;
//	      writing it starts the graphics operation
//
// The trace vectors are NOT registers. They live in a trace table
// in Word RAM, one 8 byte entry per output line: start X and
// start Y as unsigned 13.3 fixed point words, then delta X and
// delta Y as signed 5.11 fixed point words. The ASIC walks one
// entry per line, accumulating the position at 13.11 precision
// (the start value shifted left 8) and adding the delta after
// each dot.
//
// Stamp map entries are words: bits 10:0 are the stamp number
// (stamp data byte address = number * 128; with 32x32 stamps the
// low 2 bits are masked out, so numbers are multiples of 4),
// bit15 is the horizontal flip, bits 14:13 are a rotation in
// quarter turns (00 = 0, 01 = 90, 10 = 180, 11 = 270). There is
// no vertical flip bit and no per-entry priority bit: a vertical
// flip is the horizontal flip plus the 180 rotation. Stamp data
// starts at Word RAM offset 0 and stamp 0 is always blank. A
// stamp is a grid of 8x8 cells stored column by column like Mega
// Drive sprites: the cell at column cx, row cy sits at cell index
// cy + cx * (cells per side) inside the stamp, each cell 32
// bytes in standard tile layout, two 4 bit dots per byte with the
// even dot in the high nibble.
//
// The image buffer uses the same cell layout in Word RAM: dots
// are addressed by a dot index that steps across an 8 dot cell
// row, then jumps by the column stride (the column height in
// dots, minus 7) to the next cell column, and the next output
// line starts 8 dots later. Byte offset = dot index / 2.
//
// Pixel writes follow the priority mode field of the memory mode
// register (in this core's register model, bits 4:3 of register
// 0x03, the same two bits the SCD-1 model reads as the CDC
// destination select; software sets the field before whichever
// operation it runs):
//
//	0  normal:    the fetched pixel always replaces the old one
//	1  underwrite: the fetched pixel lands only where the old
//	              nibble is 0
//	2  overwrite: the fetched pixel lands only when it is not 0
//	3  prohibited: nothing is written
//
// A position outside the stamp map (with repeat off) and stamp 0
// both fetch pixel 0, which then goes through the priority rule
// like any other pixel: the brief for this slice said pixel 0 is
// always skipped, but the documented rule is the mode field
// above, and only overwrite mode skips zeros.
//
// Rendering model: the draw runs synchronously when the trace
// table base register is written, except that the ASIC can only
// reach Word RAM while the sub CPU has access to it (2M mode
// owned by the sub CPU, or 1M mode, where the sub CPU always
// holds a half). A trace started without access stays pending:
// GRON reads 1 and the draw runs the moment access is granted.
// While a draw is not pending GRON reads 0, so software polling
// the status bit sees completion immediately in the common case.
// Hardware draw timing (Word RAM access cycles per line) and the
// level 1 completion interrupt are not modeled; this core's
// SCD-1 register model has no level 1 interrupt mask register.
// In 1M mode the ASIC addresses the full physical 256 KB Word
// RAM, as it does in 2M mode; the published material documents
// the access requirement but no bank selection for the ASIC.
// The ASIC also has a font conversion function at offsets 0x4C
// to 0x57 on real hardware; it is a separate feature and is not
// implemented in this slice (those offsets keep their SCD-1
// behavior).
//
// Byte access note: the registers are word wide. In this byte
// oriented bus the trace starts on the write that completes the
// trace base value, its low byte (offset 0x67); a 68k word write
// therefore behaves exactly as on hardware.
// ---------------------------------------------------------------------------

// ASICStateSize is the serialized size of the graphics ASIC
// state. It occupies bytes 242 to 254 of the unit's 256 byte
// state block, in the previously reserved tail:
// stamp control byte, stamp map base, image buffer vertical cell
// size, image buffer start, image buffer offset, image buffer
// horizontal dots, image buffer vertical dots, trace table base,
// and the pending flag. SCD-1 and SCD-2 states have zeros there
// and load with the ASIC at its power-on state.
const ASICStateSize = 13

// asicState is the graphics ASIC register file and draw state.
// Registers are stored raw, as written; documented masks are
// applied where the values are used.
type asicState struct {
	stampCtl  uint8  // offset 0x58 low byte: bit2 SMS, bit1 STS, bit0 RPT
	stampMap  uint16 // offset 0x5A: stamp map base, Word RAM bytes / 4
	imgVCell  uint8  // offset 0x5C low byte: column height in cells minus 1
	imgStart  uint16 // offset 0x5E: image buffer start, Word RAM bytes / 4
	imgOffset uint8  // offset 0x60 low byte: bits 0-2 H dots, bits 3-5 V dots
	imgHDots  uint16 // offset 0x62: dots per line (9 bits used)
	imgVDots  uint8  // offset 0x64 low byte: lines to render
	traceBase uint16 // offset 0x66: trace table base, Word RAM bytes / 4
	pending   bool   // a trace is waiting for sub CPU Word RAM access
}

// asicRead reads one ASIC register byte, offsets 0x58 to 0x67.
func (u *Unit) asicRead(off uint8) uint8 {
	a := &u.asic
	switch off {
	case 0x58:
		if a.pending {
			return 0x80 // GRON: a graphics operation is running
		}
		return 0
	case 0x59:
		return a.stampCtl
	case 0x5A:
		return uint8(a.stampMap >> 8)
	case 0x5B:
		return uint8(a.stampMap)
	case 0x5C:
		return 0
	case 0x5D:
		return a.imgVCell
	case 0x5E:
		return uint8(a.imgStart >> 8)
	case 0x5F:
		return uint8(a.imgStart)
	case 0x60:
		return 0
	case 0x61:
		return a.imgOffset
	case 0x62:
		return uint8(a.imgHDots >> 8)
	case 0x63:
		return uint8(a.imgHDots)
	case 0x64:
		return 0
	case 0x65:
		return a.imgVDots
	case 0x66:
		return uint8(a.traceBase >> 8)
	case 0x67:
		return uint8(a.traceBase)
	}
	return 0
}

// asicWrite writes one ASIC register byte, offsets 0x58 to 0x67.
// Writing the trace table base starts the operation. On this
// byte oriented bus the start is the low byte write (offset
// 0x67): a 68k word write stores the high byte first and then
// completes the value with the low byte, so word writes start
// exactly one operation with the full value, as on hardware.
func (u *Unit) asicWrite(off uint8, v uint8) {
	a := &u.asic
	switch off {
	case 0x58:
		// GRON is read-only status; high byte writes are ignored.
	case 0x59:
		a.stampCtl = v
	case 0x5A:
		a.stampMap = a.stampMap&0x00FF | uint16(v)<<8
	case 0x5B:
		a.stampMap = a.stampMap&0xFF00 | uint16(v)
	case 0x5C:
	case 0x5D:
		a.imgVCell = v
	case 0x5E:
		a.imgStart = a.imgStart&0x00FF | uint16(v)<<8
	case 0x5F:
		a.imgStart = a.imgStart&0xFF00 | uint16(v)
	case 0x60:
	case 0x61:
		a.imgOffset = v
	case 0x62:
		a.imgHDots = a.imgHDots&0x00FF | uint16(v)<<8
	case 0x63:
		a.imgHDots = a.imgHDots&0xFF00 | uint16(v)
	case 0x64:
	case 0x65:
		a.imgVDots = v
	case 0x66:
		a.traceBase = a.traceBase&0x00FF | uint16(v)<<8
	case 0x67:
		a.traceBase = a.traceBase&0xFF00 | uint16(v)
		u.startTrace()
	}
}

// ASICBusy reports whether a graphics operation is running: in
// this synchronous model that means a trace is latched and
// waiting for the sub CPU to gain Word RAM access (GRON = 1).
func (u *Unit) ASICBusy() bool { return u.asic.pending }

// ASICLinesRemaining returns the image buffer vertical dot count
// as the register reads it: the programmed line count while a
// trace is pending, and 0 once the draw has run (hardware counts
// it down line by line as it draws).
func (u *Unit) ASICLinesRemaining() int { return int(u.asic.imgVDots) }

// subWordAccess reports whether the sub CPU can reach Word RAM
// right now: always in 1M mode (the sub CPU holds one half), and
// in 2M mode only while it owns the whole array.
func (u *Unit) subWordAccess() bool {
	return u.memMode != 0 || u.wordOwner == 1
}

// startTrace latches a graphics operation and runs it at once if
// the ASIC can reach Word RAM.
func (u *Unit) startTrace() {
	u.asic.pending = true
	u.tryASIC()
}

// tryASIC runs a pending trace once the sub CPU has Word RAM
// access. It is called on trace starts, on memory mode changes
// that can grant access, and on every cycle advance, so a
// deferred draw lands the moment ownership passes.
func (u *Unit) tryASIC() {
	if !u.asic.pending || !u.subWordAccess() {
		return
	}
	u.asic.pending = false
	u.drawTrace()
}

// asicPrioNibble applies one priority mode to a single nibble:
// old is the nibble already in the image buffer, pix the fetched
// pixel.
func asicPrioNibble(pm, old, pix uint8) uint8 {
	switch pm {
	case 0: // normal
		return pix
	case 1: // underwrite
		if old != 0 {
			return old
		}
		return pix
	case 2: // overwrite
		if pix != 0 {
			return pix
		}
		return old
	default: // 3: prohibited
		return old
	}
}

// asicTransform maps a within-stamp destination dot coordinate
// to the source coordinate the ASIC samples, applying a stamp
// map entry's flip and rotation field (hrr: bit2 horizontal
// flip, bits 1:0 rotation in quarter turns). The flip applies
// first, then the rotation components, in the documented
// transform order. mask is stampSize-1 (15 or 31).
func asicTransform(px, py, mask uint32, hrr uint16) (uint32, uint32) {
	if hrr&4 != 0 { // horizontal flip
		px = mask - px
	}
	if hrr&2 != 0 { // rotation bit 1: flip both axes
		px = mask - px
		py = mask - py
	}
	if hrr&1 != 0 { // rotation bit 0: transpose with a flip
		px, py = mask-py, px
	}
	return px, py
}

// drawTrace runs one full graphics operation: every line of the
// image buffer is traced from its trace table entry. All Word
// RAM addressing wraps at the 256 KB boundary.
func (u *Unit) drawTrace() {
	a := &u.asic
	wr := u.wordRAM[:]

	sms := a.stampCtl&0x04 != 0
	sts := a.stampCtl&0x02 != 0
	rpt := a.stampCtl&0x01 != 0

	// Map geometry per (SMS, STS), from the register
	// documentation: dotMask is the stamp map extent in the
	// internal 13.11 dot coordinate space, stampShift converts a
	// dot coordinate to a stamp column, mapShift is log2 of the
	// stamps per map row, and mapAlign masks the stamp map base
	// address to its documented alignment.
	var dotMask, mapAlign uint32
	var stampShift, mapShift uint
	var stampSize uint32 = 16
	switch {
	case !sms && !sts: // 256x256 dots, 16x16 stamps, 16x16 map
		dotMask, stampShift, mapShift, mapAlign = 0x7FFFF, 15, 4, 0x3FE00
	case !sms && sts: // 256x256 dots, 32x32 stamps, 8x8 map
		dotMask, stampShift, mapShift, mapAlign = 0x7FFFF, 16, 3, 0x3FF80
		stampSize = 32
	case sms && !sts: // 4096x4096 dots, 16x16 stamps, 256x256 map
		dotMask, stampShift, mapShift, mapAlign = 0x7FFFFF, 15, 8, 0x20000
	case sms && sts: // 4096x4096 dots, 32x32 stamps, 128x128 map
		dotMask, stampShift, mapShift, mapAlign = 0x7FFFFF, 16, 7, 0x38000
		stampSize = 32
	}
	stampMask := uint32(0x7FF)
	if sts {
		stampMask = 0x7FC // 32x32 stamp numbers are multiples of 4
	}
	cellsPerSide := stampSize / 8

	mapBase := (uint32(a.stampMap) << 2) & mapAlign
	traceAddr := (uint32(a.traceBase) << 2) & 0x3FFF8
	colStride := ((uint32(a.imgVCell&0x1F) + 1) << 6) - 7
	bufStart := (uint32(a.imgStart)<<3)&0x7FFC0 + uint32(a.imgOffset&0x3F)
	width := uint32(a.imgHDots) & 0x1FF
	pm := (u.reg03 >> 3) & 0x03

	lines := int(a.imgVDots)
	bufIdx := bufStart
	for line := 0; line < lines; line++ {
		x := int32(uint32(binary.BigEndian.Uint16(wr[traceAddr:])) << 8)
		y := int32(uint32(binary.BigEndian.Uint16(wr[traceAddr+2:])) << 8)
		dx := int32(int16(binary.BigEndian.Uint16(wr[traceAddr+4:])))
		dy := int32(int16(binary.BigEndian.Uint16(wr[traceAddr+6:])))
		traceAddr = (traceAddr + 8) & 0x3FFFF

		idx := bufIdx
		for d := uint32(0); d < width; d++ {
			ux, uy := uint32(x), uint32(y)
			if rpt {
				ux &= dotMask
				uy &= dotMask
			} else {
				ux &= 0xFFFFFF
				uy &= 0xFFFFFF
			}
			var pix uint8
			if (ux|uy)&^dotMask == 0 {
				mapIdx := (ux >> stampShift) | ((uy >> stampShift) << mapShift)
				entry := binary.BigEndian.Uint16(wr[(mapBase+mapIdx*2)&0x3FFFF:])
				if num := uint32(entry) & stampMask; num != 0 {
					px := (ux >> 11) & (stampSize - 1)
					py := (uy >> 11) & (stampSize - 1)
					tx, ty := asicTransform(px, py, stampSize-1, (entry>>13)&7)
					cellIdx := (ty / 8) + (tx/8)*cellsPerSide
					dotIdx := num<<8 | cellIdx<<6 | (ty&7)<<3 | (tx & 7)
					b := wr[(dotIdx>>1)&0x3FFFF]
					if dotIdx&1 != 0 {
						pix = b & 0x0F
					} else {
						pix = b >> 4
					}
				}
			}
			// Priority mode write of the fetched pixel (or 0)
			// into the image buffer nibble.
			bi := (idx >> 1) & 0x3FFFF
			old := wr[bi]
			if idx&1 != 0 {
				wr[bi] = old&0xF0 | asicPrioNibble(pm, old&0x0F, pix)
			} else {
				wr[bi] = asicPrioNibble(pm, old>>4, pix)<<4 | old&0x0F
			}
			if idx&7 != 7 {
				idx++
			} else {
				idx += colStride
			}
			x += dx
			y += dy
		}
		bufIdx += 8
	}
	a.imgVDots = 0
}

// serializeState writes ASICStateSize bytes into buf.
func (a *asicState) serializeState(buf []byte) {
	buf[0] = a.stampCtl
	binary.BigEndian.PutUint16(buf[1:3], a.stampMap)
	buf[3] = a.imgVCell
	binary.BigEndian.PutUint16(buf[4:6], a.imgStart)
	buf[6] = a.imgOffset
	binary.BigEndian.PutUint16(buf[7:9], a.imgHDots)
	buf[9] = a.imgVDots
	binary.BigEndian.PutUint16(buf[10:12], a.traceBase)
	buf[12] = 0
	if a.pending {
		buf[12] = 1
	}
}

// deserializeState restores state written by serializeState.
func (a *asicState) deserializeState(buf []byte) {
	a.stampCtl = buf[0]
	a.stampMap = binary.BigEndian.Uint16(buf[1:3])
	a.imgVCell = buf[3]
	a.imgStart = binary.BigEndian.Uint16(buf[4:6])
	a.imgOffset = buf[6]
	a.imgHDots = binary.BigEndian.Uint16(buf[7:9])
	a.imgVDots = buf[9]
	a.traceBase = binary.BigEndian.Uint16(buf[10:12])
	a.pending = buf[12]&1 != 0
}
