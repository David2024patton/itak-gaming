package sh2

import "testing"

// The 0x6 group register ops beyond MOV and SWAP: NOT,
// NEG, NEGC, EXTU.B, EXTU.W, EXTS.B, EXTS.W. Encodings
// are group 6, low nibble 7, A, B, C, D, E, F.

func TestGroup6Extensions(t *testing.T) {
	t.Run("NOT", func(t *testing.T) {
		c, _ := prog([]uint16{op6(2, 1, 7)})
		c.Reg[1] = 0x0F0FF0F0
		stepN(c, 1)
		eq(t, "not", c.Reg[2], 0xF0F00F0F)
	})
	t.Run("NEG", func(t *testing.T) {
		c, _ := prog([]uint16{op6(2, 1, 0x0B)})
		c.Reg[1] = 5
		stepN(c, 1)
		eq(t, "neg", c.Reg[2], 0xFFFFFFFB)
	})
	t.Run("NEGC with borrow set", func(t *testing.T) {
		c, _ := prog([]uint16{op6(2, 1, 0x0A)})
		c.Reg[1] = 0
		c.SR |= 1
		stepN(c, 1)
		eq(t, "value", c.Reg[2], 0xFFFFFFFF)
		eq(t, "T", c.SR&1, 1)
	})
	t.Run("NEGC no borrow", func(t *testing.T) {
		c, _ := prog([]uint16{op6(2, 1, 0x0A)})
		c.Reg[1] = 0
		c.SR &^= 1
		stepN(c, 1)
		eq(t, "value", c.Reg[2], 0)
		eq(t, "T", c.SR&1, 0)
	})
	t.Run("NEGC value with T clear", func(t *testing.T) {
		c, _ := prog([]uint16{op6(2, 1, 0x0A)})
		c.Reg[1] = 10
		c.SR &^= 1
		stepN(c, 1)
		eq(t, "value", c.Reg[2], 0xFFFFFFF6)
		eq(t, "T", c.SR&1, 1)
	})
	t.Run("EXTU.B clears high bytes", func(t *testing.T) {
		c, _ := prog([]uint16{op6(2, 1, 0x0C)})
		c.Reg[1] = 0xDEADBEEF
		stepN(c, 1)
		eq(t, "extu.b", c.Reg[2], 0x000000EF)
	})
	t.Run("EXTU.W clears high word", func(t *testing.T) {
		c, _ := prog([]uint16{op6(2, 1, 0x0D)})
		c.Reg[1] = 0xDEADBEEF
		stepN(c, 1)
		eq(t, "extu.w", c.Reg[2], 0x0000BEEF)
	})
	t.Run("EXTS.B sign extends", func(t *testing.T) {
		c, _ := prog([]uint16{op6(2, 1, 0x0E)})
		c.Reg[1] = 0x00000080
		stepN(c, 1)
		eq(t, "exts.b", c.Reg[2], 0xFFFFFF80)
	})
	t.Run("EXTS.W sign extends", func(t *testing.T) {
		c, _ := prog([]uint16{op6(2, 1, 0x0F)})
		c.Reg[1] = 0x00008001
		stepN(c, 1)
		eq(t, "exts.w", c.Reg[2], 0xFFFF8001)
	})
}
