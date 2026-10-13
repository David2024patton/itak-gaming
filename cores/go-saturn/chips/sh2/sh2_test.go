package sh2

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// ram is a flat big endian memory mock, 64 KiB.
type ram struct {
	b []byte
}

func newRAM() *ram {
	return &ram{b: make([]byte, 0x10000)}
}

func (r *ram) Read8(addr uint32) uint8 {
	return r.b[addr&0xFFFF]
}

func (r *ram) Read16(addr uint32) uint16 {
	return binary.BigEndian.Uint16(r.b[addr&0xFFFF:])
}

func (r *ram) Read32(addr uint32) uint32 {
	return binary.BigEndian.Uint32(r.b[addr&0xFFFF:])
}

func (r *ram) Write8(addr uint32, v uint8) {
	r.b[addr&0xFFFF] = v
}

func (r *ram) Write16(addr uint32, v uint16) {
	binary.BigEndian.PutUint16(r.b[addr&0xFFFF:], v)
}

func (r *ram) Write32(addr uint32, v uint32) {
	binary.BigEndian.PutUint32(r.b[addr&0xFFFF:], v)
}

func (r *ram) put16(addr uint32, v uint16) { r.Write16(addr, v) }
func (r *ram) put32(addr uint32, v uint32) { r.Write32(addr, v) }
func (r *ram) get32(addr uint32) uint32    { return r.Read32(addr) }

// prog loads instruction words at base and returns a
// CPU reset to base with a stack at 0x8000.
func prog(words []uint16) (*CPU, *ram) {
	r := newRAM()
	base := uint32(0x1000)
	for i, w := range words {
		r.put16(base+uint32(i*2), w)
	}
	c := New(r)
	c.Reset(base, 0x8000)
	return c, r
}

func eq(t *testing.T, what string, got, want uint32) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got 0x%08X, want 0x%08X", what, got, want)
	}
}

func eqB(t *testing.T, what string, got, want bool) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", what, got, want)
	}
}

func stepN(c *CPU, n int) {
	for i := 0; i < n; i += 1 {
		c.Step()
	}
}

// Opcode builders, in the manual field order Rn, Rm.
func op0(n, m, low int) uint16 { return uint16(0x0000 | n<<8 | m<<4 | low) }
func op2(n, m, low int) uint16 { return uint16(0x2000 | n<<8 | m<<4 | low) }
func op3(n, m, low int) uint16 { return uint16(0x3000 | n<<8 | m<<4 | low) }
func op4(n, lowByte int) uint16 {
	return uint16(0x4000 | n<<8 | lowByte)
}
func op6(n, m, low int) uint16 { return uint16(0x6000 | n<<8 | m<<4 | low) }
func movImm(n, imm int) uint16 { return uint16(0xE000 | n<<8 | imm&0xFF) }

func TestReset(t *testing.T) {
	c, _ := prog(nil)
	eq(t, "reset SR", c.SR, 0x400000F0)
	eq(t, "reset VBR", c.VBR, 0)
	eq(t, "reset PC", c.PC, 0x1000)
	eq(t, "reset SP", c.Reg[15], 0x8000)
	eq(t, "serialize size", uint32(len(c.Serialize())), SerializeSize)
	if err := c.Deserialize(make([]byte, 10)); err == nil {
		t.Errorf("short deserialize: got nil error, want an error")
	}
}

func TestMOVAddressing(t *testing.T) {
	t.Run("post increment load", func(t *testing.T) {
		c, r := prog([]uint16{op6(2, 1, 6)})
		r.put32(0x2000, 0x11223344)
		c.Reg[1] = 0x2000
		stepN(c, 1)
		eq(t, "loaded", c.Reg[2], 0x11223344)
		eq(t, "pointer", c.Reg[1], 0x2004)
	})
	t.Run("pre decrement store", func(t *testing.T) {
		c, r := prog([]uint16{op2(1, 2, 6)})
		c.Reg[1] = 0x2004
		c.Reg[2] = 0xAABBCCDD
		stepN(c, 1)
		eq(t, "stored", r.get32(0x2000), 0xAABBCCDD)
		eq(t, "pointer", c.Reg[1], 0x2000)
	})
	t.Run("indexed load and store", func(t *testing.T) {
		c, r := prog([]uint16{op0(3, 1, 14), op0(5, 4, 4)})
		r.put32(0x2004, 0x55667788)
		c.Reg[0] = 4
		c.Reg[1] = 0x2000
		stepN(c, 1)
		eq(t, "indexed loaded", c.Reg[3], 0x55667788)
		c.Reg[0] = 3
		c.Reg[5] = 0x2000
		c.Reg[4] = 0xAB
		stepN(c, 1)
		eq(t, "indexed stored byte", uint32(r.Read8(0x2003)), 0xAB)
	})
	t.Run("disp4 load and store", func(t *testing.T) {
		c, r := prog([]uint16{0x5611, 0x1162})
		r.put32(0x2004, 0x0BADF00D)
		c.Reg[1] = 0x2000
		stepN(c, 1)
		eq(t, "disp4 loaded", c.Reg[6], 0x0BADF00D)
		stepN(c, 1)
		eq(t, "disp4 stored", r.get32(0x2008), 0x0BADF00D)
	})
	t.Run("gbr disp store and load", func(t *testing.T) {
		c, r := prog([]uint16{0xC202, 0xE000, 0xC602})
		c.GBR = 0x2000
		c.Reg[0] = 0x12345678
		stepN(c, 1)
		eq(t, "gbr stored", r.get32(0x2008), 0x12345678)
		stepN(c, 2)
		eq(t, "gbr loaded", c.Reg[0], 0x12345678)
	})
	t.Run("pc relative word load sign extends", func(t *testing.T) {
		c, r := prog([]uint16{0x9706})
		r.put16(0x1010, 0x8001)
		stepN(c, 1)
		eq(t, "pc word", c.Reg[7], 0xFFFF8001)
	})
	t.Run("pc relative long load", func(t *testing.T) {
		c, r := prog([]uint16{0xD804})
		r.put32(0x1014, 0xCAFEBABE)
		stepN(c, 1)
		eq(t, "pc long", c.Reg[8], 0xCAFEBABE)
	})
	t.Run("mova", func(t *testing.T) {
		c, _ := prog([]uint16{0xC703})
		stepN(c, 1)
		eq(t, "mova", c.Reg[0], 0x1010)
	})
	t.Run("mov immediate sign extends", func(t *testing.T) {
		c, _ := prog([]uint16{movImm(3, -1)})
		stepN(c, 1)
		eq(t, "mov imm", c.Reg[3], 0xFFFFFFFF)
	})
	t.Run("movt real encoding, and 0009 is nop", func(t *testing.T) {
		// The SH-2 manual gives MOVT the 0x0n29 encoding
		// and 0x0009 is NOP, changing no register. The
		// zero form was treated as MOVT R0 in SAT-1 by
		// direction; SAT-4 corrects it to the manual
		// (NOP delay slots must not corrupt R0).
		c, _ := prog([]uint16{0x0018, 0x0529, 0x0009})
		stepN(c, 1)
		stepN(c, 1)
		eq(t, "movt with T set", c.Reg[5], 1)
		stepN(c, 1)
		eq(t, "opcode 0009 is nop, r0 untouched", c.Reg[0], 0)
	})
	t.Run("swap byte and word", func(t *testing.T) {
		c, _ := prog([]uint16{op6(2, 1, 8), op6(3, 1, 9)})
		c.Reg[1] = 0x11223344
		stepN(c, 2)
		eq(t, "swap.b", c.Reg[2], 0x11224433)
		eq(t, "swap.w", c.Reg[3], 0x33441122)
	})
	t.Run("xtrct", func(t *testing.T) {
		c, _ := prog([]uint16{op2(2, 1, 13)})
		c.Reg[1] = 0xAABBCCDD
		c.Reg[2] = 0x11223344
		stepN(c, 1)
		eq(t, "xtrct", c.Reg[2], 0xCCDD1122)
	})
	t.Run("byte and word loads sign extend", func(t *testing.T) {
		c, r := prog([]uint16{op6(2, 1, 0), op6(3, 1, 1)})
		r.Write8(0x2000, 0x80)
		r.put16(0x2002, 0x8000)
		c.Reg[1] = 0x2000
		stepN(c, 1)
		eq(t, "byte load", c.Reg[2], 0xFFFFFF80)
		c.Reg[1] = 0x2002
		stepN(c, 1)
		eq(t, "word load", c.Reg[3], 0xFFFF8000)
	})
	t.Run("disp4 byte and word with r0 in group 8", func(t *testing.T) {
		c, r := prog([]uint16{0x8013, 0x8112, 0xE000, 0x8413, 0x8512})
		c.Reg[1] = 0x2000
		c.Reg[0] = 0x7F01
		stepN(c, 2)
		eq(t, "disp byte stored", uint32(r.Read8(0x2003)), 0x01)
		eq(t, "disp word stored", uint32(r.Read16(0x2004)), 0x7F01)
		stepN(c, 1)
		eq(t, "r0 cleared", c.Reg[0], 0)
		stepN(c, 1)
		eq(t, "disp byte loaded", c.Reg[0], 1)
	})
}

func TestArithmetic(t *testing.T) {
	t.Run("add and sub", func(t *testing.T) {
		c, _ := prog([]uint16{op3(1, 2, 12), op3(1, 2, 8)})
		c.Reg[1] = 30
		c.Reg[2] = 12
		stepN(c, 1)
		eq(t, "add", c.Reg[1], 42)
		stepN(c, 1)
		eq(t, "sub", c.Reg[1], 30)
	})
	t.Run("addc chain", func(t *testing.T) {
		c, _ := prog([]uint16{0x0018, op3(1, 2, 14), op3(3, 2, 14)})
		c.Reg[1] = 0xFFFFFFFF
		c.Reg[2] = 1
		c.Reg[3] = 0
		stepN(c, 3)
		eq(t, "addc low", c.Reg[1], 1)
		eq(t, "addc high", c.Reg[3], 2)
		eq(t, "addc final T", c.SR&1, 0)
	})
	t.Run("subc chain", func(t *testing.T) {
		c, _ := prog([]uint16{0x0018, op3(1, 2, 10), op3(3, 2, 10)})
		c.Reg[1] = 0
		c.Reg[2] = 0
		c.Reg[3] = 5
		stepN(c, 3)
		eq(t, "subc low", c.Reg[1], 0xFFFFFFFF)
		eq(t, "subc high", c.Reg[3], 4)
		eq(t, "subc final T", c.SR&1, 0)
	})
	t.Run("addv overflow", func(t *testing.T) {
		c, _ := prog([]uint16{op3(1, 2, 15)})
		c.Reg[1] = 0x7FFFFFFF
		c.Reg[2] = 1
		stepN(c, 1)
		eq(t, "addv result", c.Reg[1], 0x80000000)
		eq(t, "addv T", c.SR&1, 1)
	})
	t.Run("subv overflow", func(t *testing.T) {
		c, _ := prog([]uint16{op3(1, 2, 11)})
		c.Reg[1] = 0x80000000
		c.Reg[2] = 1
		stepN(c, 1)
		eq(t, "subv result", c.Reg[1], 0x7FFFFFFF)
		eq(t, "subv T", c.SR&1, 1)
	})
	t.Run("cmp family", func(t *testing.T) {
		c, _ := prog([]uint16{
			op3(1, 2, 0), op3(1, 2, 2), op3(1, 2, 3),
			op3(1, 2, 6), op3(1, 2, 7), op2(1, 2, 12),
		})
		c.Reg[1] = 0xFFFFFFFF
		c.Reg[2] = 0xFFFFFFFF
		stepN(c, 1)
		eq(t, "cmp eq true", c.SR&1, 1)
		c.Reg[1] = 0xFFFFFFFF
		c.Reg[2] = 1
		stepN(c, 1)
		eq(t, "cmp hs true", c.SR&1, 1)
		stepN(c, 1)
		eq(t, "cmp ge false for minus one vs one", c.SR&1, 0)
		c.Reg[1] = 2
		c.Reg[2] = 1
		stepN(c, 1)
		eq(t, "cmp hi true", c.SR&1, 1)
		c.Reg[1] = 0xFFFFFFFF
		c.Reg[2] = 1
		stepN(c, 1)
		eq(t, "cmp gt false signed", c.SR&1, 0)
		c.Reg[1] = 0x11223344
		c.Reg[2] = 0x99003399
		stepN(c, 1)
		eq(t, "cmp str byte match", c.SR&1, 1)
	})
	t.Run("logic register ops", func(t *testing.T) {
		c, _ := prog([]uint16{op2(1, 2, 9), op2(1, 2, 10), op2(1, 2, 11), op2(1, 2, 8)})
		c.Reg[1] = 0xFF0F
		c.Reg[2] = 0x0FF0
		stepN(c, 1)
		eq(t, "and", c.Reg[1], 0x0F00)
		c.Reg[1] = 0xFF00
		stepN(c, 1)
		eq(t, "xor", c.Reg[1], 0xF0F0)
		c.Reg[1] = 0xF000
		stepN(c, 1)
		eq(t, "or", c.Reg[1], 0xFFF0)
		c.Reg[1] = 0xF0F0
		stepN(c, 1)
		eq(t, "tst nonzero clears T", c.SR&1, 0)
		c.Reg[1] = 0
		c.Reg[2] = 0xFFFF
		c.PC = 0x1006
		stepN(c, 1)
		eq(t, "tst zero sets T", c.SR&1, 1)
	})
	t.Run("add immediate negative", func(t *testing.T) {
		c, _ := prog([]uint16{0x71FB})
		c.Reg[1] = 10
		stepN(c, 1)
		eq(t, "add imm", c.Reg[1], 5)
	})
}

func TestImmediateLogicAndGBRByte(t *testing.T) {
	c, r := prog([]uint16{0xC8F0, 0xC93C, 0xCAFF, 0xCB01, 0xCD3C, 0xCF01, 0xCEFF, 0xCC0F})
	c.Reg[0] = 0x0F
	stepN(c, 1)
	eq(t, "tst imm sets T", c.SR&1, 1)
	c.Reg[0] = 0xFF
	stepN(c, 1)
	eq(t, "and imm", c.Reg[0], 0x3C)
	stepN(c, 1)
	eq(t, "xor imm", c.Reg[0], 0xC3)
	stepN(c, 1)
	eq(t, "or imm", c.Reg[0], 0xC3)
	c.GBR = 0x2000
	c.Reg[0] = 0x10
	r.Write8(0x2010, 0x0F)
	stepN(c, 1)
	eq(t, "and.b at gbr", uint32(r.Read8(0x2010)), 0x0C)
	stepN(c, 1)
	eq(t, "or.b at gbr", uint32(r.Read8(0x2010)), 0x0D)
	stepN(c, 1)
	eq(t, "xor.b at gbr", uint32(r.Read8(0x2010)), 0xF2)
	stepN(c, 1)
	eq(t, "tst.b at gbr T", c.SR&1, 0)
	c2, _ := prog([]uint16{0x8805})
	c2.Reg[0] = 5
	stepN(c2, 1)
	eq(t, "cmp eq imm", c2.SR&1, 1)
}

// divProgram builds the manual style division sequence.
// Signed: example sequence for a 32 bit signed dividend
// in R2 by R0, quotient ending in R2, with the original
// dividend saved in R4 so the remainder can be formed
// as dividend minus divisor times quotient in R6.
// Unsigned: DIV0U form with the high word R1 zeroed,
// quotient ending in R2, original saved in R4, and the
// remainder likewise formed in R6.
func divProgram(signed bool) []uint16 {
	w := []uint16{op6(4, 2, 3)}
	if signed {
		w = append(w, op6(3, 2, 3), op4(3, 0x24), op3(1, 1, 10),
			op2(3, 3, 10), op3(2, 3, 10), op2(1, 0, 7))
	} else {
		w = append(w, 0x0019)
	}
	for i := 0; i < 32; i += 1 {
		w = append(w, op4(2, 0x24), op3(1, 0, 4))
	}
	w = append(w, op4(2, 0x24))
	if signed {
		w = append(w, op3(2, 3, 14))
	}
	w = append(w, op0(2, 0, 7), 0x051A, op6(6, 4, 3), op3(6, 5, 8))
	return w
}

func TestDivision(t *testing.T) {
	t.Run("div0s and div0u flags", func(t *testing.T) {
		c, _ := prog([]uint16{op2(1, 0, 7), 0x0019})
		c.Reg[0] = 0x80000000
		c.Reg[1] = 5
		stepN(c, 1)
		eq(t, "div0s M", (c.SR>>9)&1, 1)
		eq(t, "div0s Q", (c.SR>>8)&1, 0)
		eq(t, "div0s T", c.SR&1, 1)
		stepN(c, 1)
		eq(t, "div0u MQT clear", c.SR&0x0301, 0)
	})
	t.Run("unsigned 100 by 7", func(t *testing.T) {
		w := divProgram(false)
		c, _ := prog(w)
		c.Reg[0] = 7
		c.Reg[1] = 0
		c.Reg[2] = 100
		stepN(c, len(w))
		wantQ := uint32(100 / 7)
		wantR := uint32(100 % 7)
		eq(t, "unsigned quotient", c.Reg[2], wantQ)
		eq(t, "unsigned remainder", c.Reg[6], wantR)
	})
	t.Run("signed minus 100 by 7", func(t *testing.T) {
		w := divProgram(true)
		c, _ := prog(w)
		c.Reg[0] = 7
		c.Reg[2] = 0xFFFFFF9C
		stepN(c, len(w))
		wantQ := int64(-100) / 7
		wantR := int64(-100) % 7
		eq(t, "signed quotient", c.Reg[2], uint32(int32(wantQ)))
		eq(t, "signed remainder", c.Reg[6], uint32(int32(wantR)))
	})
	t.Run("signed 100 by minus 7", func(t *testing.T) {
		w := divProgram(true)
		c, _ := prog(w)
		c.Reg[0] = 0xFFFFFFF9
		c.Reg[2] = 100
		stepN(c, len(w))
		eq(t, "signed quotient neg divisor", c.Reg[2], 0xFFFFFFF2)
		eq(t, "signed remainder neg divisor", c.Reg[6], uint32(int32(2)))
	})
}

func TestMultiplyAndMAC(t *testing.T) {
	t.Run("mul.l and word multiplies", func(t *testing.T) {
		c, _ := prog([]uint16{op0(2, 1, 7), op2(3, 1, 14), op2(4, 1, 15)})
		c.Reg[1] = 0xFFFFFFFD
		c.Reg[2] = 7
		stepN(c, 1)
		eq(t, "mul.l macl", c.MACL, 0xFFFFFFEB)
		c.Reg[3] = 0x0000FFFF
		c.Reg[1] = 3
		stepN(c, 1)
		eq(t, "mulu.w macl", c.MACL, 0x0002FFFD)
		c.Reg[4] = 0x0000FFFF
		c.Reg[1] = 3
		stepN(c, 1)
		eq(t, "muls.w macl", c.MACL, 0xFFFFFFFD)
	})
	t.Run("dmuls and dmulu", func(t *testing.T) {
		c, _ := prog([]uint16{op3(2, 1, 13), op3(2, 1, 5)})
		c.Reg[1] = 0xFFFFFFFD
		c.Reg[2] = 7
		stepN(c, 1)
		eq(t, "dmuls mach", c.MACH, 0xFFFFFFFF)
		eq(t, "dmuls macl", c.MACL, 0xFFFFFFEB)
		c.Reg[1] = 0xFFFFFFFF
		c.Reg[2] = 2
		stepN(c, 1)
		eq(t, "dmulu mach", c.MACH, 1)
		eq(t, "dmulu macl", c.MACL, 0xFFFFFFFE)
	})
	t.Run("mac.w accumulates and advances", func(t *testing.T) {
		c, r := prog([]uint16{0x421F, 0x421F})
		r.put16(0x2000, 5)
		r.put16(0x2002, 2)
		r.put16(0x2100, 0xFFFA)
		r.put16(0x2102, 10)
		c.Reg[1] = 0x2000
		c.Reg[2] = 0x2100
		c.MACL = 100
		stepN(c, 1)
		eq(t, "mac.w first sum", c.MACL, 70)
		eq(t, "mac.w ptr m", c.Reg[1], 0x2002)
		eq(t, "mac.w ptr n", c.Reg[2], 0x2102)
		stepN(c, 1)
		eq(t, "mac.w second sum", c.MACL, 90)
	})
	t.Run("mac.l accumulates and advances", func(t *testing.T) {
		c, r := prog([]uint16{op0(2, 1, 15)})
		r.put32(0x2000, 0xFFFFFFFD)
		r.put32(0x2100, 7)
		c.Reg[1] = 0x2000
		c.Reg[2] = 0x2100
		c.MACL = 100
		stepN(c, 1)
		eq(t, "mac.l sum", c.MACL, 79)
		eq(t, "mac.l ptr m", c.Reg[1], 0x2004)
		eq(t, "mac.l ptr n", c.Reg[2], 0x2104)
	})
	t.Run("mac.l same register advances twice", func(t *testing.T) {
		c, r := prog([]uint16{op0(1, 1, 15)})
		r.put32(0x2000, 3)
		r.put32(0x2004, 4)
		c.Reg[1] = 0x2000
		stepN(c, 1)
		eq(t, "mac.l same reg sum", c.MACL, 12)
		eq(t, "mac.l same reg ptr", c.Reg[1], 0x2008)
	})
	t.Run("mac.w saturation clamps high", func(t *testing.T) {
		c, r := prog([]uint16{0x421F})
		r.put16(0x2000, 16)
		r.put16(0x2100, 2)
		c.Reg[1] = 0x2000
		c.Reg[2] = 0x2100
		c.SR |= 0x00000002
		c.MACH = 0x00007FFF
		c.MACL = 0xFFFFFFF0
		stepN(c, 1)
		eq(t, "mac.w sat mach", c.MACH, 0x00007FFF)
		eq(t, "mac.w sat macl", c.MACL, 0xFFFFFFFF)
	})
	t.Run("clrmac", func(t *testing.T) {
		c, _ := prog([]uint16{0x0028})
		c.MACH = 9
		c.MACL = 9
		stepN(c, 1)
		eq(t, "clrmac mach", c.MACH, 0)
		eq(t, "clrmac macl", c.MACL, 0)
	})
}

func TestShiftsAndRotates(t *testing.T) {
	t.Run("one bit shifts", func(t *testing.T) {
		c, _ := prog([]uint16{op4(1, 0x00), op4(1, 0x01), op4(1, 0x21)})
		c.Reg[1] = 0x80000001
		stepN(c, 1)
		eq(t, "shll result", c.Reg[1], 2)
		eq(t, "shll T", c.SR&1, 1)
		c.Reg[1] = 3
		stepN(c, 1)
		eq(t, "shlr result", c.Reg[1], 1)
		eq(t, "shlr T", c.SR&1, 1)
		c.Reg[1] = 0x80000003
		stepN(c, 1)
		eq(t, "shar result", c.Reg[1], 0xC0000001)
		eq(t, "shar T", c.SR&1, 1)
	})
	t.Run("rotates", func(t *testing.T) {
		c, _ := prog([]uint16{op4(1, 0x04), op4(1, 0x05), op4(1, 0x24), op4(1, 0x25)})
		c.Reg[1] = 0x80000000
		stepN(c, 1)
		eq(t, "rotl result", c.Reg[1], 1)
		eq(t, "rotl T", c.SR&1, 1)
		c.Reg[1] = 1
		stepN(c, 1)
		eq(t, "rotr result", c.Reg[1], 0x80000000)
		eq(t, "rotr T", c.SR&1, 1)
		c.SR |= 1
		c.Reg[1] = 0x40000000
		stepN(c, 1)
		eq(t, "rotcl result", c.Reg[1], 0x80000001)
		eq(t, "rotcl T", c.SR&1, 0)
		c.SR &^= 1
		c.Reg[1] = 0
		stepN(c, 1)
		eq(t, "rotcr result", c.Reg[1], 0)
		eq(t, "rotcr T", c.SR&1, 0)
	})
	t.Run("multi bit shifts keep T", func(t *testing.T) {
		c, _ := prog([]uint16{op4(1, 0x08), op4(1, 0x29), op4(1, 0x28), op4(1, 0x19)})
		c.SR |= 1
		c.Reg[1] = 0x12345678
		stepN(c, 1)
		eq(t, "shll2", c.Reg[1], 0x48D159E0)
		eq(t, "shll2 T kept", c.SR&1, 1)
		c.Reg[1] = 0x12345678
		stepN(c, 1)
		eq(t, "shlr16", c.Reg[1], 0x00001234)
		c.Reg[1] = 0x00001234
		stepN(c, 1)
		eq(t, "shll16", c.Reg[1], 0x12340000)
		c.Reg[1] = 0x12345678
		stepN(c, 1)
		eq(t, "shlr8", c.Reg[1], 0x00123456)
	})
	t.Run("cmp pz and pl", func(t *testing.T) {
		c, _ := prog([]uint16{op4(1, 0x11), op4(1, 0x15), op4(1, 0x11), op4(1, 0x15)})
		c.Reg[1] = 0
		stepN(c, 1)
		eq(t, "cmp pz zero", c.SR&1, 1)
		stepN(c, 1)
		eq(t, "cmp pl zero", c.SR&1, 0)
		c.Reg[1] = 0x80000000
		stepN(c, 1)
		eq(t, "cmp pz negative", c.SR&1, 0)
		c.Reg[1] = 7
		stepN(c, 1)
		eq(t, "cmp pl positive", c.SR&1, 1)
	})
	t.Run("dt loop counts down", func(t *testing.T) {
		c, _ := prog([]uint16{op4(5, 0x10), 0x8BFD})
		c.Reg[5] = 4
		steps := 0
		for c.PC != 0x1004 && steps < 40 {
			c.Step()
			steps += 1
		}
		eq(t, "dt final value", c.Reg[5], 0)
		eq(t, "dt loop steps", uint32(steps), 8)
	})
}

func TestTAS(t *testing.T) {
	c, r := prog([]uint16{op4(1, 0x1B), op4(1, 0x1B)})
	r.Write8(0x2000, 0)
	c.Reg[1] = 0x2000
	stepN(c, 1)
	eq(t, "tas zero T", c.SR&1, 1)
	eq(t, "tas zero byte", uint32(r.Read8(0x2000)), 0x80)
	r.Write8(0x2000, 0x05)
	stepN(c, 1)
	eq(t, "tas nonzero T", c.SR&1, 0)
	eq(t, "tas nonzero byte", uint32(r.Read8(0x2000)), 0x85)
}

func TestBranches(t *testing.T) {
	t.Run("bra executes delay slot", func(t *testing.T) {
		c, _ := prog([]uint16{0xA001, movImm(1, 7), movImm(2, 9), movImm(3, 3)})
		stepN(c, 1)
		eq(t, "bra slot ran", c.Reg[1], 7)
		eq(t, "bra target pc", c.PC, 0x1006)
		eq(t, "bra skipped word", c.Reg[2], 0)
		stepN(c, 1)
		eq(t, "bra landed", c.Reg[3], 3)
	})
	t.Run("bsr sets pr and runs slot", func(t *testing.T) {
		c, _ := prog([]uint16{0xB001, movImm(1, 7), movImm(2, 9), movImm(3, 3)})
		stepN(c, 1)
		eq(t, "bsr pr", c.PR, 0x1004)
		eq(t, "bsr slot ran", c.Reg[1], 7)
		eq(t, "bsr pc", c.PC, 0x1006)
	})
	t.Run("jmp and jsr register forms", func(t *testing.T) {
		c, _ := prog([]uint16{op4(1, 0x2B), movImm(5, 5), movImm(6, 6), movImm(7, 7)})
		c.Reg[1] = 0x1008
		stepN(c, 1)
		eq(t, "jmp slot ran", c.Reg[5], 5)
		eq(t, "jmp pc", c.PC, 0x1008)
		c2, _ := prog([]uint16{op4(1, 0x0B), movImm(5, 5), movImm(6, 6), movImm(7, 7)})
		c2.Reg[1] = 0x1008
		stepN(c2, 1)
		eq(t, "jsr pr", c2.PR, 0x1004)
		eq(t, "jsr pc", c2.PC, 0x1008)
	})
	t.Run("braf form", func(t *testing.T) {
		c, _ := prog([]uint16{op0(1, 0, 3) | 0x0020, movImm(5, 5), movImm(6, 6), movImm(7, 7)})
		c.Reg[1] = 0x1008
		stepN(c, 1)
		eq(t, "braf pc", c.PC, 0x1008)
		eq(t, "braf slot ran", c.Reg[5], 5)
	})
	t.Run("rts uses pr and runs slot", func(t *testing.T) {
		c, _ := prog([]uint16{0x000B, movImm(5, 5), movImm(6, 6)})
		c.PR = 0x1006
		stepN(c, 1)
		eq(t, "rts pc", c.PC, 0x1006)
		eq(t, "rts slot ran", c.Reg[5], 5)
	})
	t.Run("bt s not taken skips delay slot", func(t *testing.T) {
		c, _ := prog([]uint16{0x8D02, movImm(1, 7), movImm(2, 5)})
		n := c.Step()
		eq(t, "bt s not taken cost", uint32(n), 1)
		eq(t, "bt s slot skipped", c.Reg[1], 0)
		eq(t, "bt s fallthrough pc", c.PC, 0x1004)
		stepN(c, 1)
		eq(t, "bt s next ran", c.Reg[2], 5)
	})
	t.Run("bt s taken runs delay slot", func(t *testing.T) {
		c, _ := prog([]uint16{0x8D01, movImm(1, 7), movImm(2, 9), movImm(3, 3)})
		c.SR |= 1
		stepN(c, 1)
		eq(t, "bt s taken slot ran", c.Reg[1], 7)
		eq(t, "bt s taken pc", c.PC, 0x1006)
	})
	t.Run("bf non delayed", func(t *testing.T) {
		c, _ := prog([]uint16{0x8B01, movImm(1, 7), movImm(2, 9), movImm(3, 3)})
		n := c.Step()
		eq(t, "bf taken cost", uint32(n), 3)
		eq(t, "bf taken pc", c.PC, 0x1006)
		eq(t, "bf has no delay slot", c.Reg[1], 0)
	})
	t.Run("slot illegal lands at vector 6", func(t *testing.T) {
		c, r := prog([]uint16{0xA001, 0xA000, movImm(1, 1)})
		r.put32(6*4, 0x3000)
		stepN(c, 1)
		eq(t, "slot illegal pc", c.PC, 0x3000)
		eq(t, "slot illegal sp", c.Reg[15], 0x8000-8)
		eq(t, "slot illegal saved pc", r.get32(0x8000-8), 0x1002)
		eq(t, "slot illegal saved sr", r.get32(0x8000-4), 0x400000F0)
		eq(t, "slot illegal slot not run", c.Reg[1], 0)
	})
	t.Run("rte restores and runs slot", func(t *testing.T) {
		c, r := prog([]uint16{0x002B, movImm(9, 9)})
		r.put32(0x8000, 0x5000)
		r.put32(0x8004, 0x00000001)
		stepN(c, 1)
		eq(t, "rte pc", c.PC, 0x5000)
		eq(t, "rte sr", c.SR, 1)
		eq(t, "rte sp", c.Reg[15], 0x8008)
		eq(t, "rte slot ran", c.Reg[9], 9)
	})
}

func TestExceptionsAndInterrupts(t *testing.T) {
	t.Run("trapa frame and vector", func(t *testing.T) {
		c, r := prog([]uint16{0xC340})
		r.put32(48*4, 0x3000)
		stepN(c, 1)
		eq(t, "trapa pc", c.PC, 0x3000)
		eq(t, "trapa sp", c.Reg[15], 0x8000-8)
		eq(t, "trapa saved pc is next", r.get32(0x8000-8), 0x1002)
		eq(t, "trapa saved sr", r.get32(0x8000-4), 0x400000F0)
	})
	t.Run("illegal opcode ffff vector 4", func(t *testing.T) {
		c, r := prog([]uint16{0xFFFF})
		r.put32(4*4, 0x3000)
		stepN(c, 1)
		eq(t, "illegal pc", c.PC, 0x3000)
		eq(t, "illegal saved pc", r.get32(0x8000-8), 0x1000)
	})
	t.Run("illegal 3 group low 1", func(t *testing.T) {
		c, r := prog([]uint16{op3(1, 0, 1)})
		r.put32(4*4, 0x3000)
		stepN(c, 1)
		eq(t, "illegal low1 pc", c.PC, 0x3000)
	})
	t.Run("illegal top f forms", func(t *testing.T) {
		c, r := prog([]uint16{0xF000})
		r.put32(4*4, 0x3000)
		stepN(c, 1)
		eq(t, "illegal f pc", c.PC, 0x3000)
	})
	t.Run("misaligned word load vector 7", func(t *testing.T) {
		c, r := prog([]uint16{op6(2, 1, 1)})
		r.put32(7*4, 0x3000)
		c.Reg[1] = 0x2001
		c.Reg[2] = 0xDEADBEEF
		stepN(c, 1)
		eq(t, "addr error pc", c.PC, 0x3000)
		eq(t, "addr error saved pc", r.get32(0x8000-8), 0x1000)
		eq(t, "addr error dest unchanged", c.Reg[2], 0xDEADBEEF)
	})
	t.Run("misaligned fetch vector 7", func(t *testing.T) {
		c, r := prog([]uint16{movImm(1, 1)})
		r.put32(7*4, 0x3000)
		c.PC = 0x1001
		stepN(c, 1)
		eq(t, "fetch addr error pc", c.PC, 0x3000)
		eq(t, "fetch addr error saved pc", r.get32(0x8000-8), 0x1001)
	})
	t.Run("interrupt accepted above mask", func(t *testing.T) {
		c, r := prog([]uint16{movImm(1, 1)})
		r.put32(64*4, 0x3000)
		r.put16(0x3000, 0x002B)
		r.put16(0x3002, movImm(2, 2))
		c.SR = 0x40000030
		eqB(t, "interrupt latched", c.Interrupt(5, 64), true)
		stepN(c, 1)
		eq(t, "interrupt pc", c.PC, 0x3000)
		eq(t, "interrupt saved pc", r.get32(0x8000-8), 0x1000)
		eq(t, "interrupt saved sr", r.get32(0x8000-4), 0x40000030)
		eq(t, "interrupt new I field", (c.SR>>4)&0x0F, 5)
		stepN(c, 1)
		eq(t, "rte returns pc", c.PC, 0x1000)
		eq(t, "rte returns sr", c.SR, 0x40000030)
		eq(t, "rte sp restored", c.Reg[15], 0x8000)
		eq(t, "rte slot ran", c.Reg[2], 2)
	})
	t.Run("interrupt masked at or below I field stays pending", func(t *testing.T) {
		c, r := prog([]uint16{movImm(1, 1), movImm(3, 3)})
		r.put32(64*4, 0x3000)
		c.SR = 0x40000030
		eqB(t, "masked latched", c.Interrupt(3, 64), true)
		stepN(c, 1)
		eq(t, "masked not taken, instr ran", c.Reg[1], 1)
		eq(t, "masked not taken pc", c.PC, 0x1002)
		c.SR = 0x40000010
		stepN(c, 1)
		eq(t, "later accepted pc", c.PC, 0x3000)
		eq(t, "later accepted saved pc", r.get32(0x8000-8), 0x1002)
	})
	t.Run("interrupt argument checks and highest kept", func(t *testing.T) {
		c, r := prog([]uint16{movImm(1, 1)})
		r.put32(20*4, 0x3000)
		eqB(t, "level zero rejected", c.Interrupt(0, 10), false)
		eqB(t, "vector too big rejected", c.Interrupt(5, 200), false)
		eqB(t, "level five latched", c.Interrupt(5, 10), true)
		eqB(t, "level nine takes the slot", c.Interrupt(9, 20), true)
		eqB(t, "level seven not kept", c.Interrupt(7, 30), false)
		c.SR = 0x40000000
		stepN(c, 1)
		eq(t, "highest vector used", c.PC, 0x3000)
	})
	t.Run("nmi taken with mask 15", func(t *testing.T) {
		c, r := prog([]uint16{movImm(1, 1)})
		r.put32(11*4, 0x3000)
		c.RaiseNMI()
		stepN(c, 1)
		eq(t, "nmi pc", c.PC, 0x3000)
		eq(t, "nmi I field 15", (c.SR>>4)&0x0F, 15)
		eq(t, "nmi saved pc", r.get32(0x8000-8), 0x1000)
		eq(t, "nmi instr not run", c.Reg[1], 0)
	})
}

func TestSystemTransfers(t *testing.T) {
	t.Run("stc and ldc round trip", func(t *testing.T) {
		c, _ := prog([]uint16{0x0102, 0x0212, 0x0322, op4(3, 0x0E), 0x0102})
		c.GBR = 0x6000
		c.VBR = 0x200
		stepN(c, 3)
		eq(t, "stc sr", c.Reg[1], 0x400000F0)
		eq(t, "stc gbr", c.Reg[2], 0x6000)
		eq(t, "stc vbr", c.Reg[3], 0x200)
		c.Reg[3] = 0x00000123
		stepN(c, 1)
		eq(t, "ldc sr stored as given", c.SR, 0x123)
		stepN(c, 1)
		eq(t, "stc sr again", c.Reg[1], 0x123)
	})
	t.Run("sts and lds round trip", func(t *testing.T) {
		c, _ := prog([]uint16{0x040A, 0x041A, 0x042A, op4(5, 0x0A), op4(5, 0x2A), 0x042A})
		c.MACH = 0x11111111
		c.MACL = 0x22222222
		c.PR = 0x33333333
		stepN(c, 1)
		eq(t, "sts mach", c.Reg[4], 0x11111111)
		stepN(c, 1)
		eq(t, "sts macl", c.Reg[4], 0x22222222)
		stepN(c, 1)
		eq(t, "sts pr", c.Reg[4], 0x33333333)
		c.Reg[5] = 0x44444444
		stepN(c, 1)
		eq(t, "lds mach", c.MACH, 0x44444444)
		c.Reg[5] = 0x55555555
		stepN(c, 1)
		eq(t, "lds pr", c.PR, 0x55555555)
		stepN(c, 1)
		eq(t, "sts pr after lds", c.Reg[4], 0x55555555)
	})
	t.Run("sts.l and lds.l through the stack", func(t *testing.T) {
		c, r := prog([]uint16{0x4F22, 0x4F26})
		c.PR = 0x77777777
		stepN(c, 1)
		eq(t, "sts.l stored", r.get32(0x7FFC), 0x77777777)
		eq(t, "sts.l sp", c.Reg[15], 0x7FFC)
		c.PR = 0
		stepN(c, 1)
		eq(t, "lds.l loaded pr", c.PR, 0x77777777)
		eq(t, "lds.l sp", c.Reg[15], 0x8000)
	})
	t.Run("stc.l and ldc.l round trip sr", func(t *testing.T) {
		c, r := prog([]uint16{0x4F03, 0x4F07})
		stepN(c, 1)
		eq(t, "stc.l stored sr", r.get32(0x7FFC), 0x400000F0)
		c.SR = 0
		stepN(c, 1)
		eq(t, "ldc.l restored sr", c.SR, 0x400000F0)
		eq(t, "ldc.l sp", c.Reg[15], 0x8000)
	})
	t.Run("ldc to sr lowers mask and pending interrupt lands", func(t *testing.T) {
		c, r := prog([]uint16{op4(3, 0x0E), movImm(1, 1)})
		r.put32(40*4, 0x3000)
		c.Reg[3] = 0x40000000
		eqB(t, "pending while masked", c.Interrupt(4, 40), true)
		stepN(c, 1)
		eq(t, "ldc ran not interrupt", c.SR, 0x40000000)
		stepN(c, 1)
		eq(t, "interrupt after ldc", c.PC, 0x3000)
	})
}

func TestSleep(t *testing.T) {
	c, r := prog([]uint16{0x001B, movImm(1, 1)})
	r.put32(33*4, 0x3000)
	stepN(c, 1)
	eqB(t, "sleeping set", c.Sleeping, true)
	eq(t, "sleep pc advanced", c.PC, 0x1002)
	n := c.Step()
	eq(t, "sleep step returns 1", uint32(n), 1)
	eq(t, "sleep pc held", c.PC, 0x1002)
	eq(t, "sleep instr not run", c.Reg[1], 0)
	c.SR = 0x40000000
	eqB(t, "wake latched", c.Interrupt(2, 33), true)
	stepN(c, 1)
	eqB(t, "sleeping cleared", c.Sleeping, false)
	eq(t, "wake lands at handler", c.PC, 0x3000)
}

func TestSerializeRoundTripMidProgram(t *testing.T) {
	words := []uint16{
		movImm(1, 10), movImm(2, 3), op3(1, 2, 12),
		op3(1, 2, 12), movImm(4, 7), op6(5, 1, 3),
	}
	a, _ := prog(words)
	b, _ := prog(words)
	stepN(a, 3)
	eqB(t, "a pending latched", a.Interrupt(4, 40), true)
	mid := a.Serialize()
	eq(t, "mid size", uint32(len(mid)), SerializeSize)
	stepN(b, 3)
	if err := b.Deserialize(mid); err != nil {
		t.Fatalf("deserialize mid: %v", err)
	}
	if !bytes.Equal(mid, b.Serialize()) {
		t.Errorf("restored state differs from saved state")
	}
	stepN(a, 3)
	stepN(b, 3)
	fa := a.Serialize()
	fb := b.Serialize()
	eq(t, "a reached r5", a.Reg[5], 16)
	if !bytes.Equal(fa, fb) {
		t.Errorf("final states differ after restore")
	}
	eq(t, "step smoke returns at least 1", uint32(a.Step()), 1)
}
