package mdec

// BS Huffman layer (extension, not hardware).
//
// Sections 13.19 to 13.21 describe the BS bitstream
// format used by STR movies and BS picture files:
// software Huffman decodes a BS stream into the same
// 16 bit run length codes that the MDEC hardware then
// consumes. Real software does that decoding on the
// CPU (often with the GTE) and never sends Huffman
// data to the MDEC ports. This file implements that
// documented software layer inside the package as an
// extension so it is testable on its own terms:
//
//   - It is selected only when bit 24 of the MDEC(1)
//     command word is set. Bit 24 is one of the bits
//     section 6.2 lists as not used (should be zero),
//     so no documented hardware stream can select it
//     by accident. With bit 24 clear, WriteWord is
//     pure hardware behavior.
//   - In BS mode the parameter words start with the
//     BS v1/v2/v3 header of section 13.19, packed as
//     two words: word 0 holds the MDEC size divided
//     by 4 in its low halfword and the file ID 3800h
//     in its high halfword (the size and ID are read
//     and otherwise unused), word 1 holds the quant
//     value in its low halfword and the version in
//     its high halfword.
//   - The bitstream follows, MSB first inside each
//     16 bit halfword, halfwords in low first order
//     inside each word (the good for PSX arrangement
//     of section 13.11: first bit in bit 15 of the
//     first halfword).
//   - Version 1 and 2 use raw signed 10 bit DC values
//     with the 0111111111b end of frame code, and
//     version 3 uses the Huffman DC offsets of
//     section 13.20, per section 13.20's DC v1/v2 and
//     DC v3 entries. AC values for all of v1, v2 and
//     v3 use the Huffman table of section 13.21.
//   - Each decoded DC or AC symbol is turned into the
//     equivalent run length code and fed through the
//     same startBlock and acStep path as hardware
//     input, so dequantization, IDCT, color
//     conversion and output are shared, not forked.

// bsWriteWord handles one parameter word in BS mode.
func (d *MDEC) bsWriteWord(w uint32) {
	switch d.bsStage {
	case 0:
		// BS header word 0: size/4 and file ID
		// 3800h. Both are accepted as documented
		// and not otherwise used.
		d.bsStage = 1
	case 1:
		d.bsQuant = int(uint16(w)) & 0x3F
		d.bsVersion = int(uint16(w >> 16))
		d.bsStage = 2
	default:
		if d.bsEnded {
			return
		}
		d.bitQ = append(d.bitQ, uint16(w), uint16(w>>16))
		d.bsPump()
	}
}

// availBits returns the number of unconsumed bits in
// the bit queue.
func (d *MDEC) availBits() int {
	n := (len(d.bitQ)-d.bitHead)*16 - d.bitPos
	if n < 0 {
		return 0
	}
	return n
}

// peekBits returns the next n bits (n at most 24)
// without consuming them, MSB first, or false when
// fewer than n bits are queued.
func (d *MDEC) peekBits(n int) (uint32, bool) {
	if d.availBits() < n {
		return 0, false
	}
	var out uint32
	idx := d.bitHead
	pos := d.bitPos
	rem := n
	for rem > 0 {
		h := d.bitQ[idx]
		take := 16 - pos
		if take > rem {
			take = rem
		}
		seg := (uint32(h) >> uint(16-pos-take)) & ((1 << uint(take)) - 1)
		out = out<<uint(take) | seg
		pos += take
		rem -= take
		if pos == 16 {
			idx++
			pos = 0
		}
	}
	return out, true
}

// consumeBits drops n bits from the bit queue.
func (d *MDEC) consumeBits(n int) {
	d.bitPos += n
	for d.bitPos >= 16 {
		d.bitPos -= 16
		d.bitHead++
	}
	if d.bitHead > 1024 {
		d.bitQ = append([]uint16(nil), d.bitQ[d.bitHead:]...)
		d.bitHead = 0
	}
}

// bsDecodeResult classifies one BS decode attempt.
type bsResult int

const (
	bsNeedMore bsResult = iota
	bsOK
	bsEndedFrame
	bsSkipped
)

// bsPump decodes as many complete symbols as the
// queued bits allow. It returns when the next symbol
// is incomplete; the rest of the symbol stays queued
// for the next parameter word.
func (d *MDEC) bsPump() {
	for !d.bsEnded {
		if d.blkState == blkNeedDC {
			if res := d.bsDecodeDC(); res == bsNeedMore {
				return
			} else if res == bsEndedFrame {
				d.bsEnded = true
				return
			}
		} else {
			if res := d.bsDecodeAC(); res == bsNeedMore {
				return
			}
		}
	}
}

// bsIsChroma reports whether the block currently
// being decoded is a Cr or Cb block.
func (d *MDEC) bsIsChroma() bool {
	return !d.mono() && d.mbIdx < 2
}

// bsDecodeDC decodes one DC value in BS mode and
// starts the block with the synthesized run length DC
// code (quant scale from the BS header in bits 15 to
// 10, DC in bits 9 to 0), which is the QuantDC form
// of section 13.20.
func (d *MDEC) bsDecodeDC() bsResult {
	if d.bsVersion == 3 {
		// End of frame: ten 1 bits, checked at the
		// DC position (section 13.20).
		if bits, ok := d.peekBits(10); ok && bits == 0x3FF {
			d.consumeBits(10)
			return bsEndedFrame
		}
		off, res := d.bsDecodeDCv3Offset()
		if res != bsOK {
			return res
		}
		// Differential accumulation per component:
		// Cr, Cb and Y have separate previous DC
		// values, starting at 0 for the first
		// macroblock (section 13.20). The result
		// wraps inside the 10 bit range, the newer
		// decoder behavior the document describes
		// (QuantDC uses DC AND 3FFh).
		var acc *int32
		switch {
		case d.mono() || d.mbIdx >= 2:
			acc = &d.dcY
		case d.mbIdx == 0:
			acc = &d.dcCr
		default:
			acc = &d.dcCb
		}
		*acc = wrap10(*acc + off)
		code := uint16(d.bsQuant<<10) | uint16(*acc&0x3FF)
		d.startBlock(code)
		return bsOK
	}
	// v1, v2 (and any other version value, treated
	// as v2): raw signed 10 bit DC, with the value
	// 0111111111b as the end of frame code
	// (section 13.20, DC v1/v2).
	bits, ok := d.peekBits(10)
	if !ok {
		return bsNeedMore
	}
	d.consumeBits(10)
	if bits == 0x1FF {
		return bsEndedFrame
	}
	code := uint16(d.bsQuant<<10) | uint16(bits&0x3FF)
	d.startBlock(code)
	return bsOK
}

func wrap10(v int32) int32 {
	v %= 1024
	if v > 511 {
		v -= 1024
	} else if v < -512 {
		v += 1024
	}
	return v
}

// bsDecodeDCv3Offset decodes one Huffman DC offset
// from the category table in tables.go. The pattern
// for a category is prefix, then a sign bit, then
// index bits. Prefixes inside one component table are
// not prefixes of each other (checked against the
// printed patterns), so a prefix match selects the
// category.
func (d *MDEC) bsDecodeDCv3Offset() (int32, bsResult) {
	chroma := d.bsIsChroma()
	for _, cat := range dcCategories {
		pre := cat.lumaPre
		if chroma {
			pre = cat.chromaPre
		}
		bits, ok := d.peekBits(len(pre))
		if !ok {
			return 0, bsNeedMore
		}
		if bits != parseBitPattern(pre) {
			continue
		}
		if cat.base == 0 {
			d.consumeBits(len(pre))
			return 0, bsOK
		}
		total := len(pre) + 1 + cat.indexBits
		all, ok := d.peekBits(total)
		if !ok {
			return 0, bsNeedMore
		}
		sign := (all >> uint(cat.indexBits)) & 1
		idx := all & ((1 << uint(cat.indexBits)) - 1)
		d.consumeBits(total)
		mag := int32(cat.base) + int32(idx)
		if sign == 1 {
			return mag * 4, bsOK
		}
		return -mag * 4, bsOK
	}
	// No category matched. The patterns the document
	// lists as unused (111111110 and 1111111110)
	// land here when not enough bits follow them to
	// form the longest category; consume one bit so
	// the pump makes progress and keeps looking,
	// which treats stray padding as skippable.
	if d.availBits() == 0 {
		return 0, bsNeedMore
	}
	d.consumeBits(1)
	return 0, bsSkipped
}

// bsDecodeAC decodes one AC symbol in BS mode and
// feeds the equivalent run length code. The end of
// block code 10 and the escape code 000001 followed
// by a raw 16 bit code are handled before the table
// (section 13.21). The escape encoding exists for run
// and level pairs that have no table entry; the raw
// code after it is used verbatim.
func (d *MDEC) bsDecodeAC() bsResult {
	if bits, ok := d.peekBits(2); ok && bits == 0x2 {
		// 10: end of block.
		d.consumeBits(2)
		d.acStep(0xFE00)
		return bsOK
	}
	if bits, ok := d.peekBits(6); ok && bits == 0x01 {
		// 000001: escape, raw 16 bit code follows.
		if d.availBits() < 22 {
			return bsNeedMore
		}
		d.consumeBits(6)
		raw, _ := d.peekBits(16)
		d.consumeBits(16)
		d.acStep(uint16(raw))
		return bsOK
	}
	for _, e := range acTable {
		bits, ok := d.peekBits(len(e.pattern))
		if !ok {
			continue
		}
		idx, sign, match := matchACPattern(e.pattern, bits)
		if !match {
			continue
		}
		d.consumeBits(len(e.pattern))
		if e.unused {
			// The unused all zero pattern is
			// consumed and treated as the end of
			// the block, so a damaged or padded
			// stream terminates the block instead
			// of looping. This is a choice for a
			// case the document only marks unused.
			d.acStep(0xFE00)
			return bsOK
		}
		code := e.values[idx]
		if sign == 1 {
			run := code >> 10
			lvl := sign10(code)
			code = (run << 10) | uint16(-lvl&0x3FF)
		}
		d.acStep(code)
		return bsOK
	}
	if d.availBits() == 0 {
		return bsNeedMore
	}
	// Unknown prefix: consume one bit and retry, so
	// the pump cannot spin forever on damaged data.
	d.consumeBits(1)
	return bsSkipped
}

// parseBitPattern converts a string of 0 and 1
// characters to its numeric value.
func parseBitPattern(s string) uint32 {
	var v uint32
	for i := 0; i < len(s); i++ {
		v <<= 1
		if s[i] == '1' {
			v |= 1
		}
	}
	return v
}

// matchACPattern matches a peeked bit field against
// one AC table pattern and extracts the index bits
// (letter x, in order) and the sign bit (letter s).
func matchACPattern(pattern string, bits uint32) (idx int, sign int, match bool) {
	n := len(pattern)
	for i := 0; i < n; i++ {
		bit := int((bits >> uint(n-1-i)) & 1)
		switch pattern[i] {
		case '0':
			if bit != 0 {
				return 0, 0, false
			}
		case '1':
			if bit != 1 {
				return 0, 0, false
			}
		case 'x':
			idx = idx<<1 | bit
		case 's':
			sign = bit
		}
	}
	return idx, sign, true
}
