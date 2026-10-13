package gpu

import (
	"encoding/binary"
	"testing"
)

// TestLoadXFIndexedGroupA exercises the load XF indexed
// command in group A (opcode 0x20), which reads its words
// from main memory through CP array 12: the source starts
// at array base + index*stride and the words land at
// consecutive XF addresses taken from the command header.
func TestLoadXFIndexedGroupA(t *testing.T) {
	// Decoy words at array index 0 (address 0x1000) and
	// the wanted words at array index 1 (address 0x1040).
	decoys := [3]uint32{0xDEAD0001, 0xDEAD0002, 0xDEAD0003}
	want := [3]uint32{0xA5A50001, 0x5A5A0002, 0x12340003}

	build := func() (*GPU, []byte) {
		mem := newFakeMem()
		for i, w := range decoys {
			binary.BigEndian.PutUint32(mem.b[0x1000+4*i:], w)
		}
		for i, w := range want {
			binary.BigEndian.PutUint32(mem.b[0x1040+4*i:], w)
		}
		var s stream
		// CP array 12: base register 0xA0+12 = 0xAC,
		// stride register 0xB0+12 = 0xBC.
		s.cp(cpArrayBase+12, 0x1000)
		s.cp(cpArrayStr+12, 0x40)
		// Load XF indexed, group A: opcode, u16 index 1,
		// then a u32 header with (count-1) in the high
		// half and the XF start address in the low half.
		// Count 3, XF address 0x1000. Total 7 bytes.
		s.WriteByte(0x20)
		var idx [2]byte
		binary.BigEndian.PutUint16(idx[:], 1)
		s.Write(idx[:])
		s.u32(uint32(3-1)<<16 | 0x1000)
		return New(mem), s.Bytes()
	}
	check := func(t *testing.T, g *GPU) {
		t.Helper()
		for i, w := range want {
			addr := uint32(0x1000 + i)
			if got := g.xfReg(addr); got != w {
				t.Errorf("xfReg(%#04x) = %#08x, want %#08x (memory at %#04x)", addr, got, w, 0x1040+4*i)
			}
			if got := g.xfReg(addr); got == decoys[i] {
				t.Errorf("xfReg(%#04x) = %#08x, the index 0 decoy word; index was not applied", addr, got)
			}
		}
	}

	g, data := build()
	if len(data) != 6+6+7 {
		t.Fatalf("stream length = %d, want 19 (two CP loads plus the 7 byte command)", len(data))
	}
	g.WriteFIFO(data)
	check(t, g)

	// The same stream fed one byte at a time must buffer
	// the partial command and produce the same result.
	g2, data2 := build()
	for _, b := range data2 {
		g2.WriteFIFO([]byte{b})
	}
	check(t, g2)
}
