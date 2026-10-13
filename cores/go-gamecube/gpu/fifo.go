package gpu

import (
	"encoding/binary"
)

// GX FIFO command map implemented here (the first byte of
// each command):
//
//   0x00 NOP (1 byte)
//   0x02, 0x44, 0x48 and any other unmodelled low opcode:
//        single-byte no-ops (update metrics, invalidate
//        vertex cache, and friends are stored-effect free)
//   0x08 load CP register: opcode, register byte, u32 value
//   0x10 load XF direct: opcode, u32 header (count-1 in the
//        high half, starting address in the low half), then
//        count u32 words stored at consecutive XF addresses
//   0x20, 0x28, 0x30, 0x38 load XF indexed groups A-D:
//        opcode, u16 index, u32 header (same layout as the
//        direct load), then the words are read from main
//        memory through CP array base and stride of the
//        group (A uses array 12, B array 13, C array 14, D
//        array 15), starting at base + index*stride
//   0x40 call display list: opcode, u32 address, u32 size;
//        the list bytes are read from memory and parsed
//        recursively (depth limited)
//   0x61 load BP register: opcode, u32 word whose high byte
//        is the BP address and whose low 24 bits are the
//        value
//   0x80-0xBF draw commands, see vertex.go
//
// Partial commands at a chunk boundary stay in the buffer
// until the rest arrives; WriteFIFO never loses bytes.

// WriteFIFO consumes a chunk of the GX FIFO byte stream.
func (g *GPU) WriteFIFO(data []byte) {
	g.buf = append(g.buf, data...)
	g.buf = g.parse(g.buf, 0)
}

// parse consumes every complete command in buf and returns
// the unconsumed remainder (a partial command, if any).
func (g *GPU) parse(buf []byte, dlDepth int) []byte {
	i := 0
	for i < len(buf) {
		need := g.commandLen(buf[i:])
		if need < 0 {
			break // incomplete command, wait for more bytes
		}
		if need == 0 {
			need = 1 // unknown opcode, skip one byte
		}
		g.exec(buf[i:i+need], dlDepth)
		i += need
	}
	rest := append([]byte(nil), buf[i:]...)
	return rest
}

// commandLen returns the total byte length of the command
// starting at buf[0], or -1 when buf does not yet hold the
// whole command.
func (g *GPU) commandLen(buf []byte) int {
	op := buf[0]
	switch {
	case op == 0x08: // CP load
		if len(buf) < 6 {
			return -1
		}
		return 6
	case op == 0x10: // XF direct
		if len(buf) < 5 {
			return -1
		}
		count := int(binary.BigEndian.Uint32(buf[1:])>>16) + 1
		if len(buf) < 5+4*count {
			return -1
		}
		return 5 + 4*count
	case op == 0x20 || op == 0x28 || op == 0x30 || op == 0x38: // XF indexed
		if len(buf) < 7 {
			return -1
		}
		return 7
	case op == 0x40: // call display list
		if len(buf) < 9 {
			return -1
		}
		return 9
	case op == 0x61: // BP load
		if len(buf) < 5 {
			return -1
		}
		return 5
	case op&0x80 != 0: // draw
		if len(buf) < 3 {
			return -1
		}
		count := int(binary.BigEndian.Uint16(buf[1:]))
		vs := g.vertexStreamSize(int(op & 7))
		total := 3 + count*vs
		if len(buf) < total {
			return -1
		}
		return total
	default:
		return 1
	}
}

func (g *GPU) exec(cmd []byte, dlDepth int) {
	op := cmd[0]
	switch {
	case op == 0x08:
		g.writeCP(cmd[1], binary.BigEndian.Uint32(cmd[2:]))
	case op == 0x10:
		hdr := binary.BigEndian.Uint32(cmd[1:])
		count := int(hdr>>16) + 1
		addr := uint32(uint16(hdr))
		for i := 0; i < count; i++ {
			g.storeXF(addr+uint32(i), binary.BigEndian.Uint32(cmd[5+4*i:]))
		}
	case op == 0x20 || op == 0x28 || op == 0x30 || op == 0x38:
		group := int((op - 0x20) / 8)
		index := uint32(binary.BigEndian.Uint16(cmd[1:]))
		hdr := binary.BigEndian.Uint32(cmd[3:])
		count := int(hdr>>16) + 1
		addr := uint32(uint16(hdr))
		base := g.arrayBase[12+group] + index*g.arrayStride[12+group]
		for i := 0; i < count; i++ {
			v := uint32(0)
			if g.mem != nil {
				v = g.mem.Read32(base + uint32(4*i))
			}
			g.storeXF(addr+uint32(i), v)
		}
	case op == 0x40:
		if dlDepth >= 4 || g.mem == nil {
			return
		}
		addr := binary.BigEndian.Uint32(cmd[1:])
		size := binary.BigEndian.Uint32(cmd[5:])
		if size > 1<<22 {
			size = 1 << 22
		}
		dl := make([]byte, size)
		for i := range dl {
			dl[i] = g.mem.Read8(addr + uint32(i))
		}
		g.parse(dl, dlDepth+1)
	case op == 0x61:
		w := binary.BigEndian.Uint32(cmd[1:])
		g.writeBP(uint8(w>>24), w&0xFFFFFF)
	case op&0x80 != 0:
		g.draw(cmd)
	default:
		// NOP and the single-byte no-op commands.
	}
}

// writeCP applies one CP register load. Documented CP
// register map: matrix index A at 0x30, matrix index B at
// 0x40, VCD low at 0x50, VCD high at 0x60, VAT group A at
// 0x70 plus the VAT index, VAT group B at 0x80 plus the
// VAT index, VAT group C at 0x90 plus the VAT index,
// array bases at 0xA0-0xAF and array strides at
// 0xB0-0xBF. Every write is also kept in the raw CP file.
func (g *GPU) writeCP(reg byte, v uint32) {
	g.cpRegs[reg] = v
	switch {
	case reg == cpMatIndA:
		g.matIndA = v
	case reg == cpMatIndB:
		g.matIndB = v
	case reg == cpVCDLo:
		g.vcdLo = v
	case reg == cpVCDHi:
		g.vcdHi = v
	case reg >= cpVATGroupA && reg < cpVATGroupA+8:
		g.vat[int(reg-cpVATGroupA)][0] = v
	case reg >= cpVATGroupB && reg < cpVATGroupB+8:
		g.vat[int(reg-cpVATGroupB)][1] = v
	case reg >= cpVATGroupC && reg < cpVATGroupC+8:
		g.vat[int(reg-cpVATGroupC)][2] = v
	case reg >= cpArrayBase && reg < cpArrayBase+16:
		g.arrayBase[reg-cpArrayBase] = v
	case reg >= cpArrayStr && reg < cpArrayStr+16:
		g.arrayStride[reg-cpArrayStr] = v
	}
}

// writeBP applies one BP register load (24-bit value) and
// runs the two trigger registers this renderer models: the
// TLUT load destination and the EFB copy trigger.
func (g *GPU) writeBP(addr uint8, v uint32) {
	v &= 0xFFFFFF
	g.bp[addr] = v
	switch addr {
	case bpLoadTLUT0:
		g.tlutSrc = (v & 0x1FFFFF) << 5
	case bpLoadTLUT1:
		g.loadTLUT(v)
	case bpTriggerCopy:
		if v&(1<<14) != 0 {
			g.clearEFBRect()
		}
	}
}

// loadTLUT copies palette entries from main memory into
// TMEM. Each entry is two bytes, copied big-endian. The
// destination TMEM byte offset and the entry count come from
// the bpLoadTLUT1 fields documented in gpu.go; the source
// address was latched by bpLoadTLUT0.
func (g *GPU) loadTLUT(v uint32) {
	dst := (v & 0x3FF) << 5
	count := int((v>>10)&0x7FF) + 1
	if g.mem == nil {
		return
	}
	for i := 0; i < count; i++ {
		off := dst + uint32(2*i)
		if off+1 >= TMEMSize {
			break
		}
		w := g.mem.Read16(g.tlutSrc + uint32(2*i))
		g.tmem[off] = byte(w >> 8)
		g.tmem[off+1] = byte(w)
	}
}

// clearEFBRect fills the BP copy source rectangle with the
// PE clear color and the clear depth. This is the BP EFB
// copy path used by GX programs to clear the frame buffer:
// a copy command whose control bit 14 marks it as a clear.
// The rectangle is clipped to the EFB; an unprogrammed
// rectangle clears the full EFB.
func (g *GPU) clearEFBRect() {
	tlx := int(g.bp[bpEFBTL] & 0x3FF)
	tly := int((g.bp[bpEFBTL] >> 10) & 0x3FF)
	brx := int(g.bp[bpEFBBR] & 0x3FF)
	bry := int((g.bp[bpEFBBR] >> 10) & 0x3FF)
	if tlx == 0 && tly == 0 && brx == 0 && bry == 0 {
		brx, bry = EFBWidth-1, EFBHeight-1
	}
	if tlx < 0 {
		tlx = 0
	}
	if tly < 0 {
		tly = 0
	}
	if brx >= EFBWidth {
		brx = EFBWidth - 1
	}
	if bry >= EFBHeight {
		bry = EFBHeight - 1
	}
	ar := g.bp[bpClearAR]
	gb := g.bp[bpClearGB]
	col := color8{
		r: uint8(ar & 0xFF),
		g: uint8(gb & 0xFF),
		b: uint8((gb >> 8) & 0xFF),
		a: uint8((ar >> 16) & 0xFF),
	}
	raw := encodeEFB(g.bp[bpPEControl]&7, col)
	z := g.bp[bpClearZ] & 0xFFFFFF
	for y := tly; y <= bry; y++ {
		for x := tlx; x <= brx; x++ {
			g.efbColor[y*EFBWidth+x] = raw
			g.efbDepth[y*EFBWidth+x] = z
		}
	}
}
