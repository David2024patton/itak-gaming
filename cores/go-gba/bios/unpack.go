package bios

import (
	"github.com/davidpatton/itak-gaming/cores/go-gba/chips/arm7tdmi"
)

// The decompression SWIs share a header layout: a word at
// the source whose low byte is a type tag and whose upper
// 24 bits are the decompressed size in bytes.

// bitUnpack (SWI 0x10) widens (or narrows) a packed bit
// stream. The parameter block at R2 reads:
//
//	u16 source length in bytes
//	u8  source bit width (1, 2, 4 or 8)
//	u8  destination bit width (1, 2, 4, 8, 16 or 32)
//	u32 offset word: bits 0 to 30 are an offset, bit 31
//	    is the add-offset-to-all-data flag
//
// Source units are consumed LSB first from the source bit
// stream. Each unit that is nonzero gains the offset; zero
// units stay zero unless bit 31 is set, in which case every
// unit gains it. Destination units are written at their
// width: word and halfword writes for 32 and 16, and for
// narrower widths the units pack into bytes LSB first.
func (h *Handler) bitUnpack(c *arm7tdmi.CPU, ret uint32) int {
	src, dst, params := c.R[0], c.R[1], c.R[2]
	srcLen := int(h.rd16(params + 0))
	srcWidth := int(h.rd8(params + 2))
	dstWidth := int(h.rd8(params + 3))
	offsetWord := h.rd32(params + 4)
	offset := offsetWord & 0x7FFFFFFF
	addAll := offsetWord&(1<<31) != 0
	c.Jump(ret)
	if srcWidth == 0 || dstWidth == 0 {
		return 3
	}
	totalUnits := srcLen * 8 / srcWidth

	// Source bit reader: bitPos counts bits consumed from
	// the source, LSB first within each byte.
	bitPos := 0
	readUnit := func() uint32 {
		var v uint32
		for b := 0; b < srcWidth; b++ {
			byteVal := h.rd8(src + uint32(bitPos/8))
			bit := (byteVal >> uint(bitPos%8)) & 1
			v |= bit << uint(b)
			bitPos++
		}
		return v
	}

	// Destination accumulator for sub byte widths.
	dstAddr := dst
	var acc uint32
	accBits := 0
	flush := func() {
		if accBits > 0 {
			h.wr8(dstAddr, acc)
			dstAddr++
			acc, accBits = 0, 0
		}
	}
	dstMask := uint32(0xFFFFFFFF)
	if dstWidth < 32 {
		dstMask = (uint32(1) << uint(dstWidth)) - 1
	}
	for i := 0; i < totalUnits; i++ {
		v := readUnit()
		if v != 0 || addAll {
			v += offset
		}
		v &= dstMask
		switch {
		case dstWidth == 32:
			h.wr32(dstAddr, v)
			dstAddr += 4
		case dstWidth == 16:
			h.wr16(dstAddr, v)
			dstAddr += 2
		case dstWidth == 8:
			h.wr8(dstAddr, v)
			dstAddr++
		default:
			acc |= v << uint(accBits)
			accBits += dstWidth
			if accBits >= 8 {
				flush()
			}
		}
	}
	flush()
	return 3 + totalUnits
}

// lz77 (SWI 0x11 for WRAM destinations, 0x12 for VRAM)
// decompresses the GBA LZSS format: a header word (type
// 0x10, size in the upper 24 bits), then flag bytes whose
// bits are consumed from the most significant bit down.
// A 0 bit introduces a literal byte; a 1 bit introduces a
// two byte back reference: the length is the high nibble
// plus 3, and the displacement is the remaining 12 bits
// plus 1, counted back from the current destination. The
// copy runs byte by byte, so overlapping references repeat
// as expected. Both variants write byte by byte here: on
// hardware the VRAM variant stages through a buffer
// because VRAM ignores byte writes, and byte writes are
// the safe equivalent for this HLE.
func (h *Handler) lz77(c *arm7tdmi.CPU, ret uint32) int {
	src, dst := c.R[0], c.R[1]
	header := h.rd32(src)
	size := int(header >> 8)
	srcPos := src + 4
	dstPos := dst
	produced := 0
	for produced < size {
		flags := h.rd8(srcPos)
		srcPos++
		for bit := 7; bit >= 0 && produced < size; bit-- {
			if flags&(1<<uint(bit)) == 0 {
				h.wr8(dstPos, h.rd8(srcPos))
				srcPos++
				dstPos++
				produced++
				continue
			}
			b0 := h.rd8(srcPos)
			b1 := h.rd8(srcPos + 1)
			srcPos += 2
			length := int(b0>>4) + 3
			disp := int(((b0&0x0F)<<8)|b1) + 1
			for k := 0; k < length && produced < size; k++ {
				v := h.rd8(dstPos - uint32(disp))
				h.wr8(dstPos, v)
				dstPos++
				produced++
			}
		}
	}
	c.Jump(ret)
	return 3 + size
}

// huffUnComp (SWI 0x13) decompresses Huffman coded data.
// The header low byte is 0x20 plus the data bit width
// (1, 2, 4 or 8) and the upper 24 bits are the
// decompressed size in bytes.
//
// After the header comes the tree block. Its first byte N
// says the block occupies (N+1)*2 bytes including that
// byte. The root node is the next byte. A node byte holds,
// in bits 0 to 5, an offset in 2 byte units to its pair of
// children, counted from the node's own position rounded
// down to even, plus one pair (the documented GBATEK
// formula adds 2 bytes, then 0 for the left child and 1
// for the right); bit 6 marks the left child as data and
// bit 7 marks the right child as data. A data child byte
// is the symbol value itself, not a node.
//
// The coded stream follows the tree as 32 bit words, bits
// consumed from the most significant bit down: 0 walks to
// the left child, 1 to the right child, until a data child
// yields one symbol. Symbols narrower than a byte pack
// into output bytes LSB first, wider ones are whole bytes.
func (h *Handler) huffUnComp(c *arm7tdmi.CPU, ret uint32) int {
	src, dst := c.R[0], c.R[1]
	header := h.rd32(src)
	bitsPerSymbol := int(header & 0x0F)
	size := int(header >> 8)
	c.Jump(ret)
	if bitsPerSymbol == 0 || size == 0 {
		return 3
	}
	treeBase := src + 4
	treeBytes := int(h.rd8(treeBase)+1) * 2
	dataBase := treeBase + uint32(treeBytes)

	// Tree bytes are read relative to the block start; the
	// root node sits at relative offset 1, right after the
	// size byte.
	treeByte := func(rel int) uint32 {
		return h.rd8(treeBase + uint32(rel))
	}

	totalSymbols := size * 8 / bitsPerSymbol

	// Bit reader over the 32 bit stream words, MSB first.
	wordIndex := 0
	bitIndex := 32 // forces a load on first use
	var streamWord uint32
	readBit := func() uint32 {
		if bitIndex >= 32 {
			streamWord = h.rd32(dataBase + uint32(4*wordIndex))
			wordIndex++
			bitIndex = 0
		}
		bit := (streamWord >> uint(31-bitIndex)) & 1
		bitIndex++
		return bit
	}

	dstPos := dst
	var acc uint32
	accBits := 0
	emit := func(sym uint32) {
		if bitsPerSymbol == 8 {
			h.wr8(dstPos, sym)
			dstPos++
			return
		}
		acc |= (sym & ((uint32(1) << uint(bitsPerSymbol)) - 1)) << uint(accBits)
		accBits += bitsPerSymbol
		if accBits >= 8 {
			h.wr8(dstPos, acc)
			dstPos++
			acc, accBits = 0, 0
		}
	}

	for s := 0; s < totalSymbols; s++ {
		node := 1 // relative offset of the root node
		for {
			nodeByte := treeByte(node)
			offset := int(nodeByte & 0x3F)
			bit := readBit()
			// Child pair address, relative offsets: the
			// node's pair base, plus the offset in 2 byte
			// units, plus one pair, plus 0 or 1 for the
			// side.
			pairBase := node &^ 1
			child := pairBase + offset*2 + 2 + int(bit)
			isData := false
			if bit == 0 {
				isData = nodeByte&(1<<6) != 0
			} else {
				isData = nodeByte&(1<<7) != 0
			}
			if isData {
				emit(treeByte(child))
				break
			}
			node = child
		}
	}
	if accBits > 0 {
		h.wr8(dstPos, acc)
	}
	return 3 + totalSymbols
}

// rlUnComp (SWI 0x14 for WRAM, 0x15 for VRAM) decodes the
// run length format: after the header (type 0x30), each
// block starts with a flag byte. With bit 7 set, the low
// 7 bits plus 3 give a run length for copies of the next
// single byte; with bit 7 clear, the low 7 bits plus 1
// literal bytes follow. Both variants write byte by byte
// (see the LZ77 note for why that is the safe HLE form).
func (h *Handler) rlUnComp(c *arm7tdmi.CPU, ret uint32) int {
	src, dst := c.R[0], c.R[1]
	header := h.rd32(src)
	size := int(header >> 8)
	srcPos := src + 4
	dstPos := dst
	produced := 0
	for produced < size {
		flag := h.rd8(srcPos)
		srcPos++
		if flag&0x80 != 0 {
			count := int(flag&0x7F) + 3
			v := h.rd8(srcPos)
			srcPos++
			for k := 0; k < count && produced < size; k++ {
				h.wr8(dstPos, v)
				dstPos++
				produced++
			}
			continue
		}
		count := int(flag&0x7F) + 1
		for k := 0; k < count && produced < size; k++ {
			h.wr8(dstPos, h.rd8(srcPos))
			srcPos++
			dstPos++
			produced++
		}
	}
	c.Jump(ret)
	return 3 + size
}

// diffUnFilter (SWI 0x16 and 0x17, 8 bit; SWI 0x18, 16
// bit) reverses difference filtering. The header type
// byte is 0x80 for the 8 bit form and 0x81 for the 16 bit
// form, with the size in bytes in the upper 24 bits as
// usual. The data starts with one literal unit; every
// following unit is a delta added to the previous output
// unit, wrapping at the unit width.
func (h *Handler) diffUnFilter(c *arm7tdmi.CPU, ret uint32, wide bool) int {
	src, dst := c.R[0], c.R[1]
	header := h.rd32(src)
	size := int(header >> 8)
	srcPos := src + 4
	dstPos := dst
	if wide {
		count := size / 2
		if count > 0 {
			prev := h.rd16(srcPos)
			srcPos += 2
			h.wr16(dstPos, prev)
			dstPos += 2
			for i := 1; i < count; i++ {
				prev = (prev + h.rd16(srcPos)) & 0xFFFF
				srcPos += 2
				h.wr16(dstPos, prev)
				dstPos += 2
			}
		}
		c.Jump(ret)
		return 3 + size
	}
	if size > 0 {
		prev := h.rd8(srcPos)
		srcPos++
		h.wr8(dstPos, prev)
		dstPos++
		for i := 1; i < size; i++ {
			prev = (prev + h.rd8(srcPos)) & 0xFF
			srcPos++
			h.wr8(dstPos, prev)
			dstPos++
		}
	}
	c.Jump(ret)
	return 3 + size
}
