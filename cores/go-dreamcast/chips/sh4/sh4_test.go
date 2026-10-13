package sh4

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// testMem is a flat 32MB physical memory for tests. Physical
// addresses are masked into the array, so the P2 window
// 0xA0000000 maps to offset 0.
type testMem struct {
	data []byte
}

const testMemSize = 32 * 1024 * 1024

func newTestMem() *testMem { return &testMem{data: make([]byte, testMemSize)} }

func (m *testMem) off(pa uint32, size uint32) int {
	return int(pa&(testMemSize-1)) &^ int(size-1)
}

func (m *testMem) Read8(pa uint32) uint8 { return m.data[m.off(pa, 1)] }
func (m *testMem) Read16(pa uint32) uint16 {
	return binary.LittleEndian.Uint16(m.data[m.off(pa, 2):])
}
func (m *testMem) Read32(pa uint32) uint32 {
	return binary.LittleEndian.Uint32(m.data[m.off(pa, 4):])
}
func (m *testMem) Read64(pa uint32) uint64 {
	return uint64(m.Read32(pa)) | uint64(m.Read32(pa+4))<<32
}
func (m *testMem) Write8(pa uint32, v uint8) { m.data[m.off(pa, 1)] = v }
func (m *testMem) Write16(pa uint32, v uint16) {
	binary.LittleEndian.PutUint16(m.data[m.off(pa, 2):], v)
}
func (m *testMem) Write32(pa uint32, v uint32) {
	binary.LittleEndian.PutUint32(m.data[m.off(pa, 4):], v)
}
func (m *testMem) Write64(pa uint32, v uint64) {
	m.Write32(pa, uint32(v))
	m.Write32(pa+4, uint32(v>>32))
}

const codeBase = 0xA0000000

func newTestCPU() (*CPU, *testMem) {
	mem := newTestMem()
	c := New(mem)
	// The power-on state (SR = 0x700080F0: bank 1, exceptions
	// blocked, FPU disabled) is what boot ROM code runs in; it
	// clears BL, selects bank 0, and enables the FPU before any
	// game code runs. Tests emulate that post-boot environment.
	// TestResetState constructs its CPU with New directly to
	// pin the raw power-on values.
	c.SR = 0x400000F0
	return c, mem
}

// execOne writes one instruction at the CPU's current PC and steps
// once. Used for single-instruction unit tests.
func execOne(c *CPU, mem *testMem, op uint16) {
	mem.Write16(c.PC&0x1FFFFFFF, op)
	c.Step()
}

// loadProg writes a program at va and points the CPU at it.
func loadProg(c *CPU, mem *testMem, va uint32, words ...uint16) {
	for i, w := range words {
		mem.Write16((va+uint32(2*i))&0x1FFFFFFF, w)
	}
	c.PC = va
}

func stepN(c *CPU, n int) {
	for i := 0; i < n; i++ {
		c.Step()
	}
}

// Small assembler helpers. Register fields: n is the destination
// register (bits 11..8), m the source register (bits 7..4).
func ins3(base uint16, n, m int, low uint16) uint16 {
	return base | uint16(n)<<8 | uint16(m)<<4 | low
}
func movImm(rn int, imm int) uint16 { return 0xE000 | uint16(rn)<<8 | uint16(uint8(imm)) }
func addImm(rn int, imm int) uint16 { return 0x7000 | uint16(rn)<<8 | uint16(uint8(imm)) }

func TestResetState(t *testing.T) {
	c := New(newTestMem())
	if c.PC != 0xA0000000 {
		t.Errorf("PC = %#x, want 0xA0000000", c.PC)
	}
	if c.SR != 0x700080F0 {
		t.Errorf("SR = %#x, want 0x700080F0", c.SR)
	}
	if c.FPSCR != 0x00040001 {
		t.Errorf("FPSCR = %#x, want 0x00040001", c.FPSCR)
	}
	if c.VBR != 0 || c.MMUCR != 0 || c.Halted {
		t.Errorf("VBR=%#x MMUCR=%#x Halted=%v, want 0, 0, false", c.VBR, c.MMUCR, c.Halted)
	}
	for i, v := range c.R {
		if v != 0 {
			t.Errorf("R%d = %#x after reset, want 0", i, v)
		}
	}
}

func TestArithmeticFlags(t *testing.T) {
	c, mem := newTestCPU()

	// ADDC chain: 0xFFFFFFFF + 1 with T=0 gives 0 and carry out.
	c.R[0] = 0xFFFFFFFF
	c.R[1] = 1
	execOne(c, mem, 0x0008) // CLRT
	execOne(c, mem, ins3(0x3000, 0, 1, 0xE))
	if c.R[0] != 0 || c.getT() != 1 {
		t.Errorf("ADDC: R0=%#x T=%d, want 0 and 1", c.R[0], c.getT())
	}
	// The carry feeds the next ADDC: 0x20 + 0x10 + 1 = 0x31, T=0.
	c.R[2] = 0x10
	c.R[3] = 0x20
	execOne(c, mem, ins3(0x3000, 3, 2, 0xE))
	if c.R[3] != 0x31 || c.getT() != 0 {
		t.Errorf("ADDC chain: R3=%#x T=%d, want 0x31 and 0", c.R[3], c.getT())
	}
	// SUBC chain: 0 - 1 with T=0 borrows.
	c.R[0] = 0
	c.R[1] = 1
	execOne(c, mem, 0x0008) // CLRT
	execOne(c, mem, ins3(0x3000, 0, 1, 0xA))
	if c.R[0] != 0xFFFFFFFF || c.getT() != 1 {
		t.Errorf("SUBC: R0=%#x T=%d, want 0xFFFFFFFF and 1", c.R[0], c.getT())
	}
	// 3 - 5 - borrow(1) = -3, still borrowing.
	c.R[2] = 5
	c.R[3] = 3
	execOne(c, mem, ins3(0x3000, 3, 2, 0xA))
	if c.R[3] != 0xFFFFFFFD || c.getT() != 1 {
		t.Errorf("SUBC chain: R3=%#x T=%d, want 0xFFFFFFFD and 1", c.R[3], c.getT())
	}
	// ADDV signed overflow.
	c.R[0] = 0x7FFFFFFF
	c.R[1] = 1
	execOne(c, mem, ins3(0x3000, 0, 1, 0xF))
	if c.R[0] != 0x80000000 || c.getT() != 1 {
		t.Errorf("ADDV: R0=%#x T=%d, want 0x80000000 and 1", c.R[0], c.getT())
	}
	// SUBV signed overflow: -2^31 - 1 wraps and sets T.
	c.R[0] = 0x80000000
	c.R[1] = 1
	execOne(c, mem, ins3(0x3000, 0, 1, 0xB))
	if c.R[0] != 0x7FFFFFFF || c.getT() != 1 {
		t.Errorf("SUBV: R0=%#x T=%d, want 0x7FFFFFFF and 1", c.R[0], c.getT())
	}
	// SHLL shifts the top bit into T; SHLR the bottom bit.
	c.R[0] = 0x80000000
	execOne(c, mem, 0x4000) // SHLL R0
	if c.R[0] != 0 || c.getT() != 1 {
		t.Errorf("SHLL: R0=%#x T=%d, want 0 and 1", c.R[0], c.getT())
	}
	c.R[0] = 1
	execOne(c, mem, 0x4001) // SHLR R0
	if c.R[0] != 0 || c.getT() != 1 {
		t.Errorf("SHLR: R0=%#x T=%d, want 0 and 1", c.R[0], c.getT())
	}
	// SHAR keeps the sign.
	c.R[0] = 0x80000001
	execOne(c, mem, 0x4021) // SHAR R0
	if c.R[0] != 0xC0000000 || c.getT() != 1 {
		t.Errorf("SHAR: R0=%#x T=%d, want 0xC0000000 and 1", c.R[0], c.getT())
	}
	// ROTCL rotates through T: with T=1, 0x80000000 becomes 1, T=1.
	c.R[0] = 0x80000000
	execOne(c, mem, 0x0018) // SETT
	execOne(c, mem, 0x4024) // ROTCL R0
	if c.R[0] != 1 || c.getT() != 1 {
		t.Errorf("ROTCL: R0=%#x T=%d, want 1 and 1", c.R[0], c.getT())
	}
	// CMP/STR sets T when any byte position matches.
	c.R[0] = 0x11223344
	c.R[1] = 0x99003377
	execOne(c, mem, ins3(0x2000, 0, 1, 0xC))
	if c.getT() != 1 {
		t.Errorf("CMP/STR: T=%d, want 1", c.getT())
	}
	c.R[1] = 0x55667788
	execOne(c, mem, ins3(0x2000, 0, 1, 0xC))
	if c.getT() != 0 {
		t.Errorf("CMP/STR no match: T=%d, want 0", c.getT())
	}
	// XTRCT takes the middle 32 bits of Rm:Rn.
	c.R[0] = 0xAABBCCDD
	c.R[1] = 0x11223344
	execOne(c, mem, ins3(0x2000, 0, 1, 0xD))
	if c.R[0] != 0x3344AABB {
		t.Errorf("XTRCT: R0=%#x, want 0x3344AABB", c.R[0])
	}
	// NEGC: 0 - 5 - T(0) = -5 with borrow; then with T=1 gives -6.
	c.R[1] = 5
	execOne(c, mem, 0x0008) // CLRT
	execOne(c, mem, ins3(0x6000, 0, 1, 0xA))
	if c.R[0] != 0xFFFFFFFB || c.getT() != 1 {
		t.Errorf("NEGC: R0=%#x T=%d, want 0xFFFFFFFB and 1", c.R[0], c.getT())
	}
	// DT decrements and sets T at zero.
	c.R[0] = 1
	execOne(c, mem, 0x4010) // DT R0
	if c.R[0] != 0 || c.getT() != 1 {
		t.Errorf("DT: R0=%#x T=%d, want 0 and 1", c.R[0], c.getT())
	}
	// SHAD by a negative count shifts right arithmetically.
	c.R[0] = 0x80000000
	c.R[1] = 0xFFFFFFFF // -1
	execOne(c, mem, ins3(0x4000, 0, 1, 0xC))
	if c.R[0] != 0xC0000000 {
		t.Errorf("SHAD -1: R0=%#x, want 0xC0000000", c.R[0])
	}
	// SHLD by a negative count shifts right logically.
	c.R[0] = 0x80000000
	execOne(c, mem, ins3(0x4000, 0, 1, 0xD))
	if c.R[0] != 0x40000000 {
		t.Errorf("SHLD -1: R0=%#x, want 0x40000000", c.R[0])
	}
}

func TestMultiplyAndMAC(t *testing.T) {
	c, mem := newTestCPU()

	// MUL.L keeps the low 32 bits of the product in MACL.
	c.R[0] = 0xFFFFFFFE // -2
	c.R[1] = 3
	execOne(c, mem, ins3(0x0000, 0, 1, 0x7))
	if c.MACL != 0xFFFFFFFA {
		t.Errorf("MUL.L: MACL=%#x, want 0xFFFFFFFA", c.MACL)
	}
	// MULS.W multiplies the sign-extended low words.
	c.R[0] = 0x0000FFFE // -2 as a word
	c.R[1] = 3
	execOne(c, mem, ins3(0x2000, 0, 1, 0xF))
	if c.MACL != 0xFFFFFFFA {
		t.Errorf("MULS.W: MACL=%#x, want 0xFFFFFFFA", c.MACL)
	}
	// MULU.W zero-extends.
	c.R[0] = 0x0000FFFF
	c.R[1] = 0x0000FFFF
	execOne(c, mem, ins3(0x2000, 0, 1, 0xE))
	if c.MACL != 0xFFFE0001 {
		t.Errorf("MULU.W: MACL=%#x, want 0xFFFE0001", c.MACL)
	}
	// DMULS.L produces a full 64-bit product.
	c.R[0] = 0xFFFFFFFE
	c.R[1] = 3
	execOne(c, mem, ins3(0x3000, 0, 1, 0xD))
	if c.MACL != 0xFFFFFFFA || c.MACH != 0xFFFFFFFF {
		t.Errorf("DMULS.L: MACH:MACL=%#x:%#x, want 0xFFFFFFFF:0xFFFFFFFA", c.MACH, c.MACL)
	}
	// DMULU.L unsigned.
	c.R[0] = 0xFFFFFFFF
	c.R[1] = 2
	execOne(c, mem, ins3(0x3000, 0, 1, 0x5))
	if c.MACL != 0xFFFFFFFE || c.MACH != 1 {
		t.Errorf("DMULU.L: MACH:MACL=%#x:%#x, want 1:0xFFFFFFFE", c.MACH, c.MACL)
	}

	// MAC.W @Rm+,@Rn+ with S=0 accumulates into the 64-bit MAC.
	const dataVA = 0xA0002000
	mem.Write16(0x2000, uint16(0xFFFD)) // -3 at R0's pointer
	mem.Write16(0x2100, uint16(5))      // 5 at R1's pointer
	c.R[0] = dataVA
	c.R[1] = dataVA + 0x100
	c.MACH, c.MACL = 0, 0
	execOne(c, mem, ins3(0x4000, 0, 1, 0xF)) // MAC.W @R1+ is m, @R0+ is n
	if c.MACL != 0xFFFFFFF1 || c.MACH != 0xFFFFFFFF {
		t.Errorf("MAC.W: MACH:MACL=%#x:%#x, want 0xFFFFFFFF:0xFFFFFFF1 (-15)", c.MACH, c.MACL)
	}
	if c.R[0] != dataVA+2 || c.R[1] != dataVA+0x102 {
		t.Errorf("MAC.W pointers: R0=%#x R1=%#x, want +2 each", c.R[0], c.R[1])
	}
	// MAC.L adds a 32x32 product to the same accumulator: -15 + 100000 * -3.
	mem.Write32(0x2000, 100000)
	mem.Write32(0x2100, 0xFFFFFFFD) // -3
	c.R[0] = dataVA
	c.R[1] = dataVA + 0x100
	execOne(c, mem, ins3(0x0000, 0, 1, 0xF)) // MAC.L
	want := int64(-15) + int64(-300000)
	if c.MACL != uint32(want) || c.MACH != uint32(want>>32) {
		t.Errorf("MAC.L: MACH:MACL=%#x:%#x, want %#x:%#x", c.MACH, c.MACL, uint32(want>>32), uint32(want))
	}
	// MAC.W with S=1 saturates MACL at 32 bits and sets MACH=1.
	execOne(c, mem, 0x0058) // SETS
	c.MACL = 0x7FFFFFF0
	c.MACH = 0x12345678
	mem.Write16(0x2000, 100)
	mem.Write16(0x2100, 100)
	c.R[0] = dataVA
	c.R[1] = dataVA + 0x100
	execOne(c, mem, ins3(0x4000, 0, 1, 0xF)) // MAC.W, product 10000 overflows
	if c.MACL != 0x7FFFFFFF || c.MACH != 1 {
		t.Errorf("MAC.W sat: MACH:MACL=%#x:%#x, want 1:0x7FFFFFFF", c.MACH, c.MACL)
	}
	execOne(c, mem, 0x0048) // CLRS
	// CLRMAC clears both halves.
	execOne(c, mem, 0x0028)
	if c.MACH != 0 || c.MACL != 0 {
		t.Errorf("CLRMAC: MACH=%#x MACL=%#x, want 0", c.MACH, c.MACL)
	}
}

// runDivSequence executes the documented SH division routine for a
// signed 32/32 division: DIV0S initializes Q, M, and T from a
// sign-extension register, then 32 rounds of ROTCL on the dividend
// register interleaved with DIV1 on the remainder register feed the
// dividend bits in and collect the quotient bits, and one final
// ROTCL brings the last quotient bit in. The raw remainder register
// then holds the remainder offset by one divisor exactly when
// Q != M, which the caller corrects.
func runDivSequence(c *CPU, mem *testMem, dividend, divisor int32) {
	c.R[0] = uint32(dividend)
	c.R[1] = uint32(divisor)
	if dividend < 0 {
		c.R[2] = 0xFFFFFFFF
	} else {
		c.R[2] = 0
	}
	execOne(c, mem, ins3(0x2000, 2, 1, 0x7)) // DIV0S R1,R2
	for i := 0; i < 32; i++ {
		execOne(c, mem, 0x4024)                  // ROTCL R0
		execOne(c, mem, ins3(0x3000, 2, 1, 0x4)) // DIV1 R1,R2
	}
	execOne(c, mem, 0x4024) // ROTCL R0
}

func TestDivisionSequence(t *testing.T) {
	c, mem := newTestCPU()
	remainder := func(divisor int32) int32 {
		rem := int32(c.R[2])
		q := (c.SR >> 8) & 1
		m := (c.SR >> 9) & 1
		if q != m {
			rem += divisor
		}
		return rem
	}

	runDivSequence(c, mem, 100, 7)
	if c.R[0] != 14 {
		t.Errorf("100/7 quotient = %d, want 14", int32(c.R[0]))
	}
	if r := remainder(7); r != 2 {
		t.Errorf("100/7 remainder = %d, want 2", r)
	}

	runDivSequence(c, mem, -100, -7)
	if c.R[0] != 14 {
		t.Errorf("-100/-7 quotient = %d, want 14", int32(c.R[0]))
	}
	if r := remainder(-7); r != -2 {
		t.Errorf("-100/-7 remainder = %d, want -2", r)
	}

	// For opposite signs the raw sequence yields the floored pair:
	// -100/7 gives quotient -15 and remainder +5, and the manual's
	// closing sign correction (quotient +1, remainder - divisor,
	// applied when the signs differ and the remainder is nonzero)
	// converts it to the truncated pair -14 and -2.
	runDivSequence(c, mem, -100, 7)
	q := int32(c.R[0])
	r := int32(c.R[2])
	if q != -15 || r != 5 {
		t.Errorf("-100/7 raw = %d rem %d, want floored -15 rem 5", q, r)
	}
	if q+1 != -14 || r-7 != -2 {
		t.Errorf("-100/7 corrected = %d rem %d, want -14 rem -2", q+1, r-7)
	}

	// Unsigned: DIV0U then the same ROTCL/DIV1 loop.
	c.R[0] = 1000
	c.R[1] = 13
	c.R[2] = 0
	execOne(c, mem, 0x0019) // DIV0U
	for i := 0; i < 32; i++ {
		execOne(c, mem, 0x4024)
		execOne(c, mem, ins3(0x3000, 2, 1, 0x4))
	}
	execOne(c, mem, 0x4024)
	if c.R[0] != 76 {
		t.Errorf("1000/13 unsigned quotient = %d, want 76", c.R[0])
	}
	if r := remainder(13); r != 12 {
		t.Errorf("1000/13 unsigned remainder = %d, want 12", r)
	}
}

func TestMOVForms(t *testing.T) {
	c, mem := newTestCPU()
	const dataVA = 0xA0002000

	// MOV.L @(disp,PC),Rn reads a literal.
	loadProg(c, mem, codeBase, 0xD000, 0x0009) // MOV.L @(0,PC),R0 ; NOP
	mem.Write32(4, 0xDEADBEEF)
	c.Step()
	if c.R[0] != 0xDEADBEEF {
		t.Errorf("MOV.L PC-rel: R0=%#x, want 0xDEADBEEF", c.R[0])
	}
	// MOV.W @(disp,PC),Rn sign extends.
	loadProg(c, mem, codeBase, 0x9000, 0x0009) // MOV.W @(0,PC),R0 ; NOP
	mem.Write16(4, 0x8001)
	c.Step()
	if c.R[0] != 0xFFFF8001 {
		t.Errorf("MOV.W PC-rel: R0=%#x, want 0xFFFF8001", c.R[0])
	}
	// Post-increment load and pre-decrement store.
	c.PC = codeBase + 0x100
	c.R[1] = dataVA
	mem.Write32(0x2000, 0x11223344)
	execOne(c, mem, ins3(0x6000, 0, 1, 0x6)) // MOV.L @R1+,R0
	if c.R[0] != 0x11223344 || c.R[1] != dataVA+4 {
		t.Errorf("MOV.L @R1+: R0=%#x R1=%#x", c.R[0], c.R[1])
	}
	c.R[0] = 0xCAFEBABE
	execOne(c, mem, ins3(0x2000, 1, 0, 0x6)) // MOV.L R0,@-R1
	if c.R[1] != dataVA || mem.Read32(0x2000) != 0xCAFEBABE {
		t.Errorf("MOV.L @-R1: R1=%#x mem=%#x", c.R[1], mem.Read32(0x2000))
	}
	// Displacement forms.
	c.R[2] = dataVA
	c.R[0] = 0xAABBCCDD
	execOne(c, mem, ins3(0x1000, 2, 0, 0x2)) // MOV.L R0,@(8,R2)
	if mem.Read32(0x2008) != 0xAABBCCDD {
		t.Errorf("MOV.L disp store: mem=%#x", mem.Read32(0x2008))
	}
	execOne(c, mem, ins3(0x5000, 3, 2, 0x2)) // MOV.L @(8,R2),R3
	if c.R[3] != 0xAABBCCDD {
		t.Errorf("MOV.L disp load: R3=%#x", c.R[3])
	}
	// Indexed forms through R0.
	c.R[0] = 0x10
	c.R[1] = dataVA
	c.R[2] = 0x55667788
	execOne(c, mem, ins3(0x0000, 1, 2, 0x6)) // MOV.L R2,@(R0,R1)
	if mem.Read32(0x2010) != 0x55667788 {
		t.Errorf("MOV.L indexed store: mem=%#x", mem.Read32(0x2010))
	}
	execOne(c, mem, ins3(0x0000, 3, 1, 0xE)) // MOV.L @(R0,R1),R3
	if c.R[3] != 0x55667788 {
		t.Errorf("MOV.L indexed load: R3=%#x", c.R[3])
	}
	// GBR-relative byte store and load, and the GBR byte logic ops.
	c.GBR = dataVA + 0x100
	c.R[0] = 0xAB
	execOne(c, mem, 0xC005) // MOV.B R0,@(5,GBR)
	if mem.Read8(0x2105) != 0xAB {
		t.Errorf("MOV.B GBR store: mem=%#x", mem.Read8(0x2105))
	}
	execOne(c, mem, 0xC405) // MOV.B @(5,GBR),R0
	if c.R[0] != 0xFFFFFFAB {
		t.Errorf("MOV.B GBR load: R0=%#x, want sign extended 0xFFFFFFAB", c.R[0])
	}
	c.R[0] = 0x05           // byte offset for the @(R0,GBR) logic forms
	execOne(c, mem, 0xCD3C) // AND.B #0x3C,@(R0,GBR): 0xAB & 0x3C = 0x28
	if mem.Read8(0x2105) != 0x28 {
		t.Errorf("AND.B: mem=%#x, want 0x28", mem.Read8(0x2105))
	}
	execOne(c, mem, 0xCC0F) // TST.B #0x0F,@(R0,GBR): 0x28 & 0x0F != 0
	if c.getT() != 0 {
		t.Errorf("TST.B: T=%d, want 0", c.getT())
	}
	execOne(c, mem, 0xCCC0) // TST.B #0xC0: 0x28 & 0xC0 == 0
	if c.getT() != 1 {
		t.Errorf("TST.B zero: T=%d, want 1", c.getT())
	}
	// MOVA computes a PC-relative address without touching memory.
	c.PC = codeBase
	execOne(c, mem, 0xC702) // MOVA @(2,PC),R0
	if c.R[0] != codeBase+4+8 {
		t.Errorf("MOVA: R0=%#x, want %#x", c.R[0], uint32(codeBase+12))
	}
	// MOVCA.L stores R0.
	c.R[1] = dataVA
	c.R[0] = 0x12345678
	execOne(c, mem, 0x00C3|1<<8) // MOVCA.L R0,@R1
	if mem.Read32(0x2000) != 0x12345678 {
		t.Errorf("MOVCA.L: mem=%#x", mem.Read32(0x2000))
	}
	// TAS.B sets T on a zero byte and sets the top bit either way.
	c.R[1] = dataVA
	mem.Write8(0x2000, 0)
	execOne(c, mem, 0x401B|1<<8) // TAS.B @R1
	if c.getT() != 1 || mem.Read8(0x2000) != 0x80 {
		t.Errorf("TAS.B zero: T=%d mem=%#x, want 1 and 0x80", c.getT(), mem.Read8(0x2000))
	}
	execOne(c, mem, 0x401B|1<<8)
	if c.getT() != 0 || mem.Read8(0x2000) != 0x80 {
		t.Errorf("TAS.B nonzero: T=%d mem=%#x, want 0 and 0x80", c.getT(), mem.Read8(0x2000))
	}
	// MOVT materializes T.
	execOne(c, mem, 0x0018)      // SETT
	execOne(c, mem, 0x0029|5<<8) // MOVT R5
	if c.R[5] != 1 {
		t.Errorf("MOVT: R5=%d, want 1", c.R[5])
	}
}

func TestSystemRegisterMoves(t *testing.T) {
	c, mem := newTestCPU()
	c.PC = codeBase + 0x100

	// LDS/STS with PR.
	c.R[0] = 0xA0003000
	execOne(c, mem, 0x400A|0<<8|2<<4) // LDS R0,PR
	if c.PR != 0xA0003000 {
		t.Errorf("LDS PR: PR=%#x", c.PR)
	}
	execOne(c, mem, 0x000A|1<<8|2<<4) // STS PR,R1
	if c.R[1] != 0xA0003000 {
		t.Errorf("STS PR: R1=%#x", c.R[1])
	}
	// STS.L / LDS.L through memory.
	c.R[2] = 0xA0002000
	execOne(c, mem, 0x4002|2<<8|2<<4) // STS.L PR,@-R2 (stack style: writes at R2 after decrement in our flow via R2 pointing one past)
	// Our STS.L decrements first, so the value lands at 0xA0001FFC.
	if mem.Read32(0x1FFC) != 0xA0003000 || c.R[2] != 0xA0001FFC {
		t.Errorf("STS.L PR: mem=%#x R2=%#x", mem.Read32(0x1FFC), c.R[2])
	}
	c.PR = 0
	execOne(c, mem, 0x4006|2<<8|2<<4) // LDS.L @R2+,PR
	if c.PR != 0xA0003000 || c.R[2] != 0xA0002000 {
		t.Errorf("LDS.L PR: PR=%#x R2=%#x", c.PR, c.R[2])
	}
	// LDC/STC with GBR works in any mode.
	c.R[0] = 0xA0004000
	execOne(c, mem, 0x400E|0<<8|1<<4) // LDC R0,GBR
	if c.GBR != 0xA0004000 {
		t.Errorf("LDC GBR: GBR=%#x", c.GBR)
	}
	execOne(c, mem, 0x0002|3<<8|1<<4) // STC GBR,R3
	if c.R[3] != 0xA0004000 {
		t.Errorf("STC GBR: R3=%#x", c.R[3])
	}
	// LDS/STS with MACH and MACL.
	c.R[0] = 0x11223344
	execOne(c, mem, 0x400A|0<<8|0<<4) // LDS R0,MACH
	execOne(c, mem, 0x400A|0<<8|1<<4) // LDS R0,MACL (m field is bits 11..8 here: R0)
	if c.MACH != 0x11223344 || c.MACL != 0x11223344 {
		t.Errorf("LDS MACH/MACL: %#x %#x", c.MACH, c.MACL)
	}
	execOne(c, mem, 0x000A|4<<8|0<<4) // STS MACH,R4
	if c.R[4] != 0x11223344 {
		t.Errorf("STS MACH: R4=%#x", c.R[4])
	}
}

func TestDelaySlots(t *testing.T) {
	// BRA: the delay slot executes, the instruction after it is
	// skipped, and control reaches the target.
	c, mem := newTestCPU()
	loadProg(c, mem, codeBase,
		0xA001,       // BRA +1 (target = base+6)
		movImm(0, 1), // delay slot
		movImm(0, 2), // skipped
		movImm(0, 3), // target
	)
	stepN(c, 3)
	if c.R[0] != 3 {
		t.Errorf("BRA delay slot: R0=%d, want 3 (slot ran, +2 skipped)", c.R[0])
	}

	// BT not taken: the delay slot does not execute at all.
	c, mem = newTestCPU()
	loadProg(c, mem, codeBase,
		0x0008,       // CLRT
		0x8901,       // BT +1 (would target base+8)
		movImm(0, 1), // delay slot, must be skipped
		movImm(0, 2), // fall-through target
		movImm(0, 3),
	)
	stepN(c, 3)
	if c.R[0] != 2 {
		t.Errorf("BT not taken: R0=%d, want 2 (slot skipped)", c.R[0])
	}
	// BT taken: the delay slot executes before the target.
	c, mem = newTestCPU()
	loadProg(c, mem, codeBase,
		0x0018,       // SETT
		0x8901,       // BT +1 (target = base+8)
		movImm(0, 1), // delay slot
		movImm(0, 2), // skipped
		movImm(0, 3), // target
	)
	stepN(c, 4)
	if c.R[0] != 3 {
		t.Errorf("BT taken: R0=%d, want 3", c.R[0])
	}
	// BT/S not taken: the delay slot still executes, then control
	// falls through.
	c, mem = newTestCPU()
	loadProg(c, mem, codeBase,
		0x0008,       // CLRT
		0x8D01,       // BT/S +1
		movImm(0, 1), // delay slot, executes
		movImm(0, 2), // fall-through
	)
	stepN(c, 4)
	if c.R[0] != 2 {
		t.Errorf("BT/S not taken: R0=%d, want 2 (slot ran, then fall-through)", c.R[0])
	}
	// BF/S taken mirrors BT/S.
	c, mem = newTestCPU()
	loadProg(c, mem, codeBase,
		0x0008,       // CLRT
		0x8F01,       // BF/S +1 (target = base+8)
		movImm(0, 1), // delay slot
		movImm(0, 2), // skipped
		movImm(0, 3), // target
	)
	stepN(c, 4)
	if c.R[0] != 3 {
		t.Errorf("BF/S taken: R0=%d, want 3", c.R[0])
	}

	// A branch in a delay slot raises the slot illegal exception.
	c, mem = newTestCPU()
	loadProg(c, mem, codeBase,
		0xA000, // BRA +0
		0xA000, // BRA in the delay slot: illegal
		0x0009,
	)
	stepN(c, 2)
	if c.EXPEVT != 0x1A0 {
		t.Errorf("branch in delay slot: EXPEVT=%#x, want 0x1A0", c.EXPEVT)
	}
	if c.PC != c.VBR+0x100 {
		t.Errorf("branch in delay slot: PC=%#x, want VBR+0x100", c.PC)
	}
	// TRAPA in a delay slot is also a slot illegal exception, not a
	// trap exception.
	c, mem = newTestCPU()
	loadProg(c, mem, codeBase,
		0xA000, // BRA +0
		0xC301, // TRAPA #1 in the delay slot
		0x0009,
	)
	stepN(c, 2)
	if c.EXPEVT != 0x1A0 {
		t.Errorf("TRAPA in delay slot: EXPEVT=%#x, want 0x1A0", c.EXPEVT)
	}
	// An illegal opcode in a delay slot is a slot illegal exception.
	c, mem = newTestCPU()
	loadProg(c, mem, codeBase,
		0xA000, // BRA +0
		0xFFFF, // undefined
		0x0009,
	)
	stepN(c, 2)
	if c.EXPEVT != 0x1A0 {
		t.Errorf("illegal in delay slot: EXPEVT=%#x, want 0x1A0", c.EXPEVT)
	}
}

func TestSubroutineFlow(t *testing.T) {
	c, mem := newTestCPU()
	loadProg(c, mem, codeBase,
		0xB002,       // BSR +2 (target = base+8)
		0x0009,       // NOP (delay slot)
		movImm(1, 7), // return point
		0x0009,       // NOP
		movImm(0, 5), // subroutine: MOV #5,R0
		0x000B,       // RTS
		0x0009,       // NOP (delay slot)
	)
	stepN(c, 5)
	if c.R[0] != 5 {
		t.Errorf("BSR flow: R0=%d, want 5", c.R[0])
	}
	if c.PC != codeBase+4 {
		t.Errorf("BSR flow: PC=%#x, want return point %#x", c.PC, uint32(codeBase+4))
	}
	c.Step()
	if c.R[1] != 7 {
		t.Errorf("BSR flow: R1=%d after return, want 7", c.R[1])
	}
	// JSR @Rn and JMP @Rn.
	c, mem = newTestCPU()
	c.R[3] = codeBase + 8
	loadProg(c, mem, codeBase,
		0x400B|3<<8,  // JSR @R3
		0x0009,       // NOP (delay slot)
		movImm(1, 7), // return point (base+4)
		0x0009,
		movImm(0, 9), // subroutine at base+8
		0x402B|3<<8,  // JMP @R3 (R3 still holds base+8? PR path below re-sets it)
		0x0009,
	)
	c.R[3] = codeBase + 8
	stepN(c, 3) // JSR, NOP slot, subroutine MOV
	if c.R[0] != 9 || c.PR != codeBase+4 {
		t.Errorf("JSR: R0=%d PR=%#x, want 9 and %#x", c.R[0], c.PR, uint32(codeBase+4))
	}
}

func TestBankSwitching(t *testing.T) {
	c, mem := newTestCPU()
	c.PC = codeBase + 0x100
	// Reset SR has RB=0, so R shows bank 0 and Bank holds bank 1.
	c.R[0] = 0x11111111    // bank 0 R0
	c.Bank[0] = 0x22222222 // bank 1 R0

	// LDC R1,SR with RB=1 swaps the active bank.
	c.R[1] = 0x700000F0               // MD=1, RB=1, BL=1, IMASK=F
	execOne(c, mem, 0x400E|1<<8|0<<4) // LDC R1,SR
	if c.R[0] != 0x22222222 || c.Bank[0] != 0x11111111 {
		t.Errorf("after RB=1: R0=%#x Bank0=%#x, want bank 1 active", c.R[0], c.Bank[0])
	}
	// Flipping RB back restores bank 0.
	c.R[1] = 0x500000F0               // MD=1, RB=0, BL=1
	execOne(c, mem, 0x400E|1<<8|0<<4) // LDC R1,SR
	if c.R[0] != 0x11111111 || c.Bank[0] != 0x22222222 {
		t.Errorf("after RB=0: R0=%#x Bank0=%#x, want bank 0 active", c.R[0], c.Bank[0])
	}
	// LDC Rm,Rn_BANK writes the inactive bank regardless of RB.
	c.R[2] = 0x33333333
	execOne(c, mem, 0x400E|2<<8|(8+0)<<4) // LDC R2,R0_BANK
	if c.Bank[0] != 0x33333333 || c.R[0] != 0x11111111 {
		t.Errorf("LDC R0_BANK: Bank0=%#x R0=%#x, want 0x33333333 in the inactive bank", c.Bank[0], c.R[0])
	}
	// STC Rm_BANK,Rn reads it back.
	execOne(c, mem, 0x0002|3<<8|(8+0)<<4) // STC R0_BANK,R3
	if c.R[3] != 0x33333333 {
		t.Errorf("STC R0_BANK: R3=%#x, want 0x33333333", c.R[3])
	}
	// STC SR,Rn reports the current SR.
	execOne(c, mem, 0x0002|4<<8|0<<4) // STC SR,R4
	if c.R[4] != c.SR {
		t.Errorf("STC SR: R4=%#x SR=%#x", c.R[4], c.SR)
	}
}

func TestTRAPAAndRTE(t *testing.T) {
	c, mem := newTestCPU()
	c.VBR = 0xA0001000
	loadProg(c, mem, codeBase, 0xC321)             // TRAPA #0x21
	mem.Write16((0x1000+0x100)&0x1FFFFFFF, 0x002B) // RTE at VBR+0x100
	mem.Write16((0x1000+0x102)&0x1FFFFFFF, 0x0009) // its delay slot
	c.R[15] = 0xA0008000
	srBefore := c.SR
	c.Step()
	if c.EXPEVT != 0x160 {
		t.Errorf("TRAPA: EXPEVT=%#x, want 0x160", c.EXPEVT)
	}
	if c.TRA != 0x21<<2 {
		t.Errorf("TRAPA: TRA=%#x, want %#x", c.TRA, uint32(0x21<<2))
	}
	if c.PC != 0xA0001000+0x100 {
		t.Errorf("TRAPA: PC=%#x, want VBR+0x100", c.PC)
	}
	if c.SPC != codeBase+2 || c.SSR != srBefore || c.SGR != 0xA0008000 {
		t.Errorf("TRAPA: SPC=%#x SSR=%#x SGR=%#x", c.SPC, c.SSR, c.SGR)
	}
	if c.SR&(srMD|srRB|srBL) != srMD|srRB|srBL {
		t.Errorf("TRAPA: SR=%#x, MD/RB/BL must be set", c.SR)
	}
	// RTE returns to SPC with SR restored from SSR; its delay slot
	// executes first.
	stepN(c, 2)
	if c.PC != codeBase+2 {
		t.Errorf("RTE: PC=%#x, want %#x", c.PC, uint32(codeBase+2))
	}
	if c.SR != srBefore {
		t.Errorf("RTE: SR=%#x, want %#x", c.SR, srBefore)
	}
}

func TestIllegalAndPrivileged(t *testing.T) {
	c, mem := newTestCPU()
	execOne(c, mem, 0xFFFF)
	if c.EXPEVT != 0x180 || c.PC != 0x100 {
		t.Errorf("illegal: EXPEVT=%#x PC=%#x, want 0x180 and VBR+0x100", c.EXPEVT, c.PC)
	}
	// A privileged instruction in user mode is a general illegal
	// instruction exception. MD is cleared directly here; the bank
	// state is irrelevant to this check.
	c, mem = newTestCPU()
	c.PC = 0x00001000
	c.SR = 0x000000F0                 // user mode
	execOne(c, mem, 0x400E|0<<8|0<<4) // LDC R0,SR
	if c.EXPEVT != 0x180 {
		t.Errorf("user LDC SR: EXPEVT=%#x, want 0x180", c.EXPEVT)
	}
	// But LDC to GBR is allowed in user mode.
	c, mem = newTestCPU()
	c.PC = 0x00001000
	c.SR = 0x000000F0
	c.R[0] = 0xA0005000
	execOne(c, mem, 0x400E|0<<8|1<<4) // LDC R0,GBR
	if c.GBR != 0xA0005000 || c.EXPEVT == 0x180 {
		t.Errorf("user LDC GBR: GBR=%#x EXPEVT=%#x, want it to succeed", c.GBR, c.EXPEVT)
	}
	// SLEEP halts; a halted CPU reports steps without moving.
	c, mem = newTestCPU()
	c.PC = codeBase
	execOne(c, mem, 0x001B) // SLEEP
	if !c.Halted {
		t.Fatal("SLEEP: Halted is false")
	}
	pc := c.PC
	if c.Step() != 1 || c.PC != pc {
		t.Errorf("halted Step: PC moved to %#x", c.PC)
	}
}

func TestInterruptModel(t *testing.T) {
	park := func(c *CPU, mem *testMem) {
		// NOP sled at the parked PC so a non-accepting Step just
		// advances one instruction.
		for i := 0; i < 8; i++ {
			mem.Write16((codeBase+0x40+uint32(2*i))&0x1FFFFFFF, 0x0009)
		}
		c.VBR = 0xA0001000
		c.PC = codeBase + 0x40
	}

	// IMASK gates acceptance: the post-boot SR has IMASK 15, so a
	// level 6 request latches as pending but is not taken.
	c, mem := newTestCPU()
	park(c, mem)
	c.RaiseInterrupt(0x320, 6)
	c.Step()
	if c.PC != codeBase+0x42 || c.INTEVT == 0x320 {
		t.Errorf("masked interrupt taken: PC=%#x INTEVT=%#x", c.PC, c.INTEVT)
	}
	if code, level, valid := c.PendingInterrupt(); !valid || code != 0x320 || level != 6 {
		t.Errorf("pending = (%#x, %d, %v), want (0x320, 6, true)", code, level, valid)
	}
	// The request survives a serialize round trip.
	c2, _ := newTestCPU()
	if !c2.Deserialize(c.Serialize()) {
		t.Fatal("Deserialize of the pending state failed")
	}
	if code, level, valid := c2.PendingInterrupt(); !valid || code != 0x320 || level != 6 {
		t.Errorf("restored pending = (%#x, %d, %v), want (0x320, 6, true)", code, level, valid)
	}
	// Drop IMASK to 3 and the request is accepted at the next
	// instruction boundary.
	c.PC = codeBase + 0x40
	c.SR = 0x40000030 // MD set, IMASK 3
	c.Step()
	if c.INTEVT != 0x320 {
		t.Errorf("INTEVT=%#x, want 0x320", c.INTEVT)
	}
	if c.PC != 0xA0001000+0x600 || c.SPC != codeBase+0x40 {
		t.Errorf("interrupt vector: PC=%#x SPC=%#x", c.PC, c.SPC)
	}
	if _, _, valid := c.PendingInterrupt(); valid {
		t.Error("pending request still latched after acceptance")
	}
	if c.SR&srBL == 0 {
		t.Error("BL not set on interrupt entry")
	}

	// BL blocks acceptance even at level 15 with IMASK 0, until it
	// clears.
	c, mem = newTestCPU()
	park(c, mem)
	c.SR = 0x40000000 | srBL
	c.RaiseInterrupt(0x200, 15)
	c.Step()
	if c.PC != codeBase+0x42 || c.INTEVT == 0x200 {
		t.Errorf("BL-blocked interrupt taken: PC=%#x INTEVT=%#x", c.PC, c.INTEVT)
	}
	c.SR &^= srBL
	c.PC = codeBase + 0x40
	c.Step()
	if c.INTEVT != 0x200 || c.PC != 0xA0001000+0x600 {
		t.Errorf("unblocked interrupt: PC=%#x INTEVT=%#x", c.PC, c.INTEVT)
	}

	// Withdrawal drops a latched request before acceptance.
	c, mem = newTestCPU()
	park(c, mem)
	c.SR = 0x40000000
	c.RaiseInterrupt(0x320, 6)
	c.WithdrawInterrupt()
	c.Step()
	if c.PC != codeBase+0x42 || c.INTEVT == 0x320 {
		t.Errorf("withdrawn interrupt taken: PC=%#x INTEVT=%#x", c.PC, c.INTEVT)
	}

	// A lower level request does not displace a latched higher one.
	c, _ = newTestCPU()
	c.RaiseInterrupt(0x320, 6)
	c.RaiseInterrupt(0x3A0, 2)
	if code, level, _ := c.PendingInterrupt(); code != 0x320 || level != 6 {
		t.Errorf("pending after lower raise = (%#x, %d), want (0x320, 6)", code, level)
	}
}

func TestMMUTranslation(t *testing.T) {
	c, mem := newTestCPU()
	c.MMUCR = 1
	mk := func(vpn, ppn uint32, size, pr int, d bool) TLBEntry {
		return TLBEntry{VPN: vpn, PPN: ppn, Size: size, V: true, PR: pr, D: d}
	}
	c.UTLB[0] = mk(0x00400000, 0x00800000, 1, 3, true)  // 4KB
	c.UTLB[1] = mk(0x00010000, 0x00200000, 0, 3, true)  // 1KB
	c.UTLB[2] = mk(0x01000000, 0x02000000, 2, 3, true)  // 64KB
	c.UTLB[3] = mk(0x10000000, 0x04000000, 3, 3, true)  // 1MB
	c.UTLB[4] = mk(0x00600000, 0x00A00000, 1, 1, true)  // writable, no user
	c.UTLB[5] = mk(0x00700000, 0x00B00000, 1, 2, true)  // user, read-only
	c.UTLB[6] = mk(0x00800000, 0x00C00000, 1, 3, false) // clean page
	c.UTLB[7] = TLBEntry{VPN: 0x00900000, PPN: 0x00D00000, Size: 1, V: false, PR: 3, D: true}

	check := func(va uint32, write bool, wantPA uint32, wantOK bool, wantCode uint32) {
		t.Helper()
		pa, ok := c.TranslateVA(va, write)
		if ok != wantOK {
			t.Errorf("TranslateVA(%#x, write=%v): ok=%v, want %v", va, write, ok, wantOK)
			return
		}
		if ok && pa != wantPA {
			t.Errorf("TranslateVA(%#x): pa=%#x, want %#x", va, pa, wantPA)
		}
		if !ok && c.EXPEVT != wantCode {
			t.Errorf("TranslateVA(%#x, write=%v): EXPEVT=%#x, want %#x", va, write, c.EXPEVT, wantCode)
		}
		if !ok && c.TEA != va {
			t.Errorf("TranslateVA(%#x): TEA=%#x, want the faulting VA", va, c.TEA)
		}
	}
	check(0x00400123, false, 0x00800123, true, 0)
	check(0x00400FFF, true, 0x00800FFF, true, 0)
	check(0x000103FF, false, 0x002003FF, true, 0)
	check(0x0100ABCD, false, 0x0200ABCD, true, 0)
	check(0x10054321, false, 0x04054321, true, 0)
	// Misses: no entry at all, and an entry with V=0.
	check(0x20000000, false, 0, false, 0x040)
	check(0x20000000, true, 0, false, 0x060)
	check(0x00900000, false, 0, false, 0x040)
	// Write to a user-readable but not writable page (privileged).
	check(0x00700000, true, 0, false, 0x0C0)
	// Write to a valid clean page: initial page write.
	check(0x00800000, true, 0, false, 0x080)
	check(0x00800000, false, 0x00C00000, true, 0)

	// User mode (MD=0, set directly; no code runs in this state):
	// pages without the user bit raise protection faults.
	c.SR = 0x000000F0
	check(0x00600000, false, 0, false, 0x0A0)
	check(0x00600000, true, 0, false, 0x0C0)
	check(0x00400123, false, 0x00800123, true, 0)
	// User mode may not touch the untranslated areas at all.
	check(0xA0000000, false, 0, false, 0x0E0)
	check(0xA0000000, true, 0, false, 0x100)
	c.SR = 0x400000F0

	// With AT=0 every area maps directly.
	c.MMUCR = 0
	check(0x00400123, false, 0x00400123&0x1FFFFFFF, true, 0)
	check(0xA0001234, false, 0x1234, true, 0)
	c.MMUCR = 1

	// Data access through the MMU from running code: a load from
	// the 4KB page at VA 0x00400000 reads physical 0x00800000.
	mem.Write32(0x800000, 0x11223344)
	c.PC = codeBase
	c.R[1] = 0x00400000
	execOne(c, mem, ins3(0x6000, 0, 1, 0x2)) // MOV.L @R1,R0
	if c.R[0] != 0x11223344 {
		t.Errorf("MMU load: R0=%#x, want 0x11223344", c.R[0])
	}
	// A load from an unmapped VA vectors the TLB miss exception to
	// VBR+0x600.
	c.VBR = 0
	c.R[1] = 0x20000000
	execOne(c, mem, ins3(0x6000, 0, 1, 0x2))
	if c.EXPEVT != 0x040 || c.PC != 0x600 || c.TEA != 0x20000000 {
		t.Errorf("MMU miss: EXPEVT=%#x PC=%#x TEA=%#x, want 0x040, 0x600, 0x20000000", c.EXPEVT, c.PC, c.TEA)
	}
}

func TestLDTLB(t *testing.T) {
	c, mem := newTestCPU()
	c.MMUCR = 1
	c.PTEH = 0x00600000
	// PTEL per the documented layout in loadTLB: PPN in the high
	// bits, V at bit 8, PR at bits 6..5, size at bits 4..3, D at
	// bit 2.
	c.PTEL = 0x00A00000 | 1<<8 | 3<<5 | 1<<3 | 1<<2
	c.PC = codeBase
	execOne(c, mem, 0x0038) // LDTLB
	e := c.UTLB[0]
	if !e.V || e.VPN != 0x00600000 || e.PPN != 0x00A00000 || e.Size != 1 || e.PR != 3 || !e.D {
		t.Fatalf("LDTLB entry: %+v", e)
	}
	pa, ok := c.TranslateVA(0x00600ABC, false)
	if !ok || pa != 0x00A00ABC {
		t.Errorf("LDTLB translate read: pa=%#x ok=%v, want 0x00A00ABC", pa, ok)
	}
	if _, ok := c.TranslateVA(0x00600ABC, true); !ok {
		t.Errorf("LDTLB translate write: not ok, EXPEVT=%#x (D bit must be set)", c.EXPEVT)
	}
	// A second LDTLB goes to the next replacement slot.
	c.PTEH = 0x00700000
	c.PTEL = 0x00B00000 | 1<<8 | 3<<5 | 1<<3 | 1<<2
	execOne(c, mem, 0x0038)
	if c.UTLB[1].VPN != 0x00700000 {
		t.Errorf("LDTLB slot 1: %+v", c.UTLB[1])
	}
}

func TestFPU(t *testing.T) {
	c, mem := newTestCPU()
	c.PC = codeBase + 0x100
	// Reset FPSCR has PR=0 (single precision) and SR has FD=0.

	// FLDI1 / FLDI0.
	execOne(c, mem, 0xF000|1<<8|0x9D) // FLDI1 FR1
	if c.FR[1] != 0x3F800000 {
		t.Errorf("FLDI1: FR1=%#x, want 0x3F800000", c.FR[1])
	}
	execOne(c, mem, 0xF000|2<<8|0x8D) // FLDI0 FR2
	if c.FR[2] != 0 {
		t.Errorf("FLDI0: FR2=%#x, want 0", c.FR[2])
	}
	// FADD / FMUL / FDIV on singles: 1.5 and 2.25.
	c.FR[0] = math.Float32bits(1.5)
	c.FR[1] = math.Float32bits(2.25)
	execOne(c, mem, ins3(0xF000, 0, 1, 0x0)) // FADD FR1,FR0
	if got := math.Float32frombits(c.FR[0]); got != 3.75 {
		t.Errorf("FADD: %v, want 3.75", got)
	}
	c.FR[0] = math.Float32bits(1.5)
	execOne(c, mem, ins3(0xF000, 0, 1, 0x2)) // FMUL FR1,FR0
	if got := math.Float32frombits(c.FR[0]); got != 3.375 {
		t.Errorf("FMUL: %v, want 3.375", got)
	}
	c.FR[0] = math.Float32bits(7)
	execOne(c, mem, ins3(0xF000, 0, 1, 0x3)) // FDIV FR1,FR0: 7 / 2.25
	if got := math.Float32frombits(c.FR[0]); math.Abs(float64(got-7.0/2.25)) > 1e-6 {
		t.Errorf("FDIV: %v, want %v", got, 7.0/2.25)
	}
	c.FR[0] = math.Float32bits(7)
	c.FR[1] = math.Float32bits(2)
	execOne(c, mem, ins3(0xF000, 0, 1, 0x3)) // 7 / 2 = 3.5
	if got := math.Float32frombits(c.FR[0]); got != 3.5 {
		t.Errorf("FDIV exact: %v, want 3.5", got)
	}
	// FSUB.
	c.FR[0] = math.Float32bits(5)
	c.FR[1] = math.Float32bits(1.25)
	execOne(c, mem, ins3(0xF000, 0, 1, 0x1))
	if got := math.Float32frombits(c.FR[0]); got != 3.75 {
		t.Errorf("FSUB: %v, want 3.75", got)
	}
	// FCMP/EQ and FCMP/GT drive T.
	c.FR[0] = math.Float32bits(2)
	c.FR[1] = math.Float32bits(2)
	execOne(c, mem, ins3(0xF000, 0, 1, 0x4)) // FCMP/EQ FR1,FR0
	if c.getT() != 1 {
		t.Errorf("FCMP/EQ: T=%d, want 1", c.getT())
	}
	c.FR[0] = math.Float32bits(3)
	execOne(c, mem, ins3(0xF000, 0, 1, 0x5)) // FCMP/GT FR1,FR0: 3 > 2
	if c.getT() != 1 {
		t.Errorf("FCMP/GT: T=%d, want 1", c.getT())
	}
	execOne(c, mem, ins3(0xF000, 1, 0, 0x5)) // FCMP/GT FR0,FR1: 2 > 3 false
	if c.getT() != 0 {
		t.Errorf("FCMP/GT false: T=%d, want 0", c.getT())
	}
	// FMAC: FR0 * FR1 + FR2 with FR0=2, FR1=3, FR2=4 gives 10.
	c.FR[0] = math.Float32bits(2)
	c.FR[1] = math.Float32bits(3)
	c.FR[2] = math.Float32bits(4)
	execOne(c, mem, ins3(0xF000, 2, 1, 0xE)) // FMAC FR1,FR2
	if got := math.Float32frombits(c.FR[2]); got != 10 {
		t.Errorf("FMAC: %v, want 10", got)
	}
	// FLOAT and FTRC round trip, including saturation.
	c.FPUL = 0xFFFFFFFD
	execOne(c, mem, 0xF02D) // FLOAT FPUL,FR0
	if got := math.Float32frombits(c.FR[0]); got != -3 {
		t.Errorf("FLOAT: %v, want -3", got)
	}
	c.FR[1] = math.Float32bits(3.99)
	execOne(c, mem, 0xF03D|1<<8) // FTRC FR1,FPUL
	if int32(c.FPUL) != 3 {
		t.Errorf("FTRC: FPUL=%d, want 3", int32(c.FPUL))
	}
	c.FR[1] = math.Float32bits(1e30)
	execOne(c, mem, 0xF03D|1<<8)
	if c.FPUL != 0x7FFFFFFF {
		t.Errorf("FTRC saturate high: FPUL=%#x, want 0x7FFFFFFF", c.FPUL)
	}
	c.FR[1] = math.Float32bits(-1e30)
	execOne(c, mem, 0xF03D|1<<8)
	if c.FPUL != 0x80000000 {
		t.Errorf("FTRC saturate low: FPUL=%#x, want 0x80000000", c.FPUL)
	}
	// FSQRT and FSRRA.
	c.FR[0] = math.Float32bits(2)
	execOne(c, mem, 0xF06D) // FSQRT FR0
	if got := math.Float32frombits(c.FR[0]); math.Abs(float64(got)-math.Sqrt(2)) > 1e-6 {
		t.Errorf("FSQRT: %v, want sqrt(2)", got)
	}
	c.FR[0] = math.Float32bits(4)
	execOne(c, mem, 0xF07D) // FSRRA FR0
	if got := math.Float32frombits(c.FR[0]); got != 0.5 {
		t.Errorf("FSRRA: %v, want 0.5", got)
	}
	// FNEG and FABS.
	c.FR[0] = math.Float32bits(-2.5)
	execOne(c, mem, 0xF05D) // FABS FR0
	if got := math.Float32frombits(c.FR[0]); got != 2.5 {
		t.Errorf("FABS: %v, want 2.5", got)
	}
	execOne(c, mem, 0xF04D) // FNEG FR0
	if got := math.Float32frombits(c.FR[0]); got != -2.5 {
		t.Errorf("FNEG: %v, want -2.5", got)
	}
	// FMOV.S through memory, including post-increment.
	c.FR[0] = math.Float32bits(1.5)
	c.R[1] = 0xA0002000
	execOne(c, mem, ins3(0xF000, 1, 0, 0xA)) // FMOV.S FR0,@R1
	if mem.Read32(0x2000) != math.Float32bits(1.5) {
		t.Errorf("FMOV.S store: mem=%#x", mem.Read32(0x2000))
	}
	execOne(c, mem, ins3(0xF000, 2, 1, 0x8)) // FMOV.S @R1,FR2
	if c.FR[2] != math.Float32bits(1.5) {
		t.Errorf("FMOV.S load: FR2=%#x", c.FR[2])
	}
	execOne(c, mem, ins3(0xF000, 3, 1, 0x9)) // FMOV.S @R1+,FR3
	if c.FR[3] != math.Float32bits(1.5) || c.R[1] != 0xA0002004 {
		t.Errorf("FMOV.S post-inc: FR3=%#x R1=%#x", c.FR[3], c.R[1])
	}
	// FSTS and FLDS move values through FPUL.
	execOne(c, mem, 0xF01D|2<<8) // FLDS FR2,FPUL
	if c.FPUL != math.Float32bits(1.5) {
		t.Errorf("FLDS: FPUL=%#x", c.FPUL)
	}
	execOne(c, mem, 0xF00D|4<<8) // FSTS FPUL,FR4
	if c.FR[4] != math.Float32bits(1.5) {
		t.Errorf("FSTS: FR4=%#x", c.FR[4])
	}
	// LDS/STS FPUL and FPSCR.
	c.R[0] = 0xDEADBEEF
	execOne(c, mem, 0x400A|0<<8|5<<4) // LDS R0,FPUL
	if c.FPUL != 0xDEADBEEF {
		t.Errorf("LDS FPUL: FPUL=%#x", c.FPUL)
	}
	execOne(c, mem, 0x000A|5<<8|5<<4) // STS FPUL,R5
	if c.R[5] != 0xDEADBEEF {
		t.Errorf("STS FPUL: R5=%#x", c.R[5])
	}
}

func TestFPUBanksAndDoubles(t *testing.T) {
	c, mem := newTestCPU()
	c.PC = codeBase + 0x100

	// FPSCR.FR swap via LDS: the FR and XFR banks exchange places.
	c.FR[0] = 0xAAAAAAAA
	c.XFR[0] = 0xBBBBBBBB
	c.R[0] = c.FPSCR ^ (1 << 21)
	execOne(c, mem, 0x400A|0<<8|6<<4) // LDS R0,FPSCR
	if c.FR[0] != 0xBBBBBBBB || c.XFR[0] != 0xAAAAAAAA {
		t.Errorf("FRCHG via LDS: FR0=%#x XFR0=%#x", c.FR[0], c.XFR[0])
	}
	// FRCHG swaps them back.
	execOne(c, mem, 0xFBFD)
	if c.FR[0] != 0xAAAAAAAA || c.FPSCR&(1<<21) != 0 {
		t.Errorf("FRCHG: FR0=%#x FPSCR=%#x", c.FR[0], c.FPSCR)
	}

	// Double precision with PR=1: 1.5 + 2.25 = 3.75 in DR0.
	c.R[0] = c.FPSCR | (1 << 19)
	execOne(c, mem, 0x400A|0<<8|6<<4) // LDS R0,FPSCR (sets PR)
	setDR := func(pair int, v float64) {
		b := math.Float64bits(v)
		c.FR[pair] = uint32(b)
		c.FR[pair+1] = uint32(b >> 32)
	}
	getDR := func(pair int) float64 {
		return math.Float64frombits(uint64(c.FR[pair]) | uint64(c.FR[pair+1])<<32)
	}
	setDR(0, 1.5)
	setDR(2, 2.25)
	execOne(c, mem, ins3(0xF000, 0, 1, 0x0)) // FADD DR1,DR0 (fields hold pair numbers 0 and 2 >> 1 in the m/n slots? see note)
	// Note: with PR=1 the register fields name the even DR pair
	// directly: DRm is field value 2 for DR2. ins3 placed m=1,
	// which the decoder masks to pair 0, so this added DR0 to
	// itself. Redo with the correct field:
	setDR(0, 1.5)
	setDR(2, 2.25)
	execOne(c, mem, ins3(0xF000, 0, 2, 0x0)) // FADD with m field 2 = DR2
	if got := getDR(0); got != 3.75 {
		t.Errorf("FADD double: %v, want 3.75", got)
	}
	// FLOAT to a double, and FTRC back from a double.
	c.FPUL = 0xFFFFFFF9
	execOne(c, mem, 0xF02D|2<<8) // FLOAT FPUL,DR2 (n field 2)
	if got := getDR(2); got != -7 {
		t.Errorf("FLOAT double: %v, want -7", got)
	}
	setDR(2, -9.75)
	execOne(c, mem, 0xF03D|2<<8) // FTRC DR2,FPUL
	if int32(c.FPUL) != -9 {
		t.Errorf("FTRC double: FPUL=%d, want -9", int32(c.FPUL))
	}
	// FCNVSD and FCNVDS.
	c.FPUL = math.Float32bits(1.5)
	execOne(c, mem, 0xF0AD) // FCNVSD FPUL,DR0
	if got := getDR(0); got != 1.5 {
		t.Errorf("FCNVSD: %v, want 1.5", got)
	}
	setDR(0, 2.5)
	execOne(c, mem, 0xF0BD) // FCNVDS DR0,FPUL
	if c.FPUL != math.Float32bits(2.5) {
		t.Errorf("FCNVDS: FPUL=%#x, want bits of 2.5", c.FPUL)
	}
	// Back to single precision for the FMOV double tests: PR only
	// affects arithmetic, FMOV honors SZ.
	c.R[0] = c.FPSCR | (1 << 20) // set SZ, keep PR as is
	execOne(c, mem, 0x400A|0<<8|6<<4)
	setDR(0, 3.5)
	c.R[1] = 0xA0002000
	execOne(c, mem, ins3(0xF000, 1, 0, 0xA)) // FMOV DR0,@R1 (SZ=1)
	if mem.Read64(0x2000) != math.Float64bits(3.5) {
		t.Errorf("FMOV double store: mem=%#x", mem.Read64(0x2000))
	}
	setDR(0, 0)
	execOne(c, mem, ins3(0xF000, 0, 1, 0x8)) // FMOV @R1,DR0
	if got := getDR(0); got != 3.5 {
		t.Errorf("FMOV double load: %v, want 3.5", got)
	}
	// FMOV between DR and XD banks.
	c.XFR[0], c.XFR[1] = 0x11111111, 0x22222222
	execOne(c, mem, ins3(0xF000, 0, 1, 0xC)) // FMOV XD0,DR0: m field 1 = XD0
	if c.FR[0] != 0x11111111 || c.FR[1] != 0x22222222 {
		t.Errorf("FMOV XD to DR: FR0=%#x FR1=%#x", c.FR[0], c.FR[1])
	}
}

func TestFPUGraphics(t *testing.T) {
	c, mem := newTestCPU()
	c.PC = codeBase + 0x100

	// FIPR FV0,FV4: dot product of (1,2,3,4) and (5,6,7,8) is 70,
	// stored in the last element of FV4 (FR7).
	for i := 0; i < 4; i++ {
		c.FR[i] = math.Float32bits(float32(i + 1))
		c.FR[4+i] = math.Float32bits(float32(i + 5))
	}
	execOne(c, mem, 0xF000|1<<10|0<<8|0xED) // FIPR FV0,FV4
	if got := math.Float32frombits(c.FR[7]); got != 70 {
		t.Errorf("FIPR: FR7=%v, want 70", got)
	}
	if got := math.Float32frombits(c.FR[4]); got != 5 {
		t.Errorf("FIPR: FR4=%v, want 5 (only the last element changes)", got)
	}

	// FTRV with the identity matrix in the X bank leaves FV0 alone.
	for i := range c.XFR {
		c.XFR[i] = 0
	}
	for i := 0; i < 4; i++ {
		c.XFR[i*4+i] = math.Float32bits(1)
		c.FR[i] = math.Float32bits(float32(i + 1))
	}
	execOne(c, mem, 0xF1FD) // FTRV XMTRX,FV0
	for i := 0; i < 4; i++ {
		if got := math.Float32frombits(c.FR[i]); got != float32(i+1) {
			t.Errorf("FTRV identity: FR%d=%v, want %d", i, got, i+1)
		}
	}
	// FTRV with a 90 degree rotation about Z (row-major in the X
	// bank): (1,0,0,1) becomes (0,1,0,1).
	for i := range c.XFR {
		c.XFR[i] = 0
	}
	c.XFR[1] = math.Float32bits(-1) // row 0: 0,-1,0,0
	c.XFR[4] = math.Float32bits(1)  // row 1: 1,0,0,0
	c.XFR[10] = math.Float32bits(1)
	c.XFR[15] = math.Float32bits(1)
	c.FR[0] = math.Float32bits(1)
	c.FR[1] = math.Float32bits(0)
	c.FR[2] = math.Float32bits(0)
	c.FR[3] = math.Float32bits(1)
	execOne(c, mem, 0xF1FD)
	want := []float32{0, 1, 0, 1}
	for i := 0; i < 4; i++ {
		got := math.Float32frombits(c.FR[i])
		if math.Abs(float64(got-want[i])) > 1e-6 {
			t.Errorf("FTRV rotZ90: FR%d=%v, want %v", i, got, want[i])
		}
	}
}

func TestFPUDisabled(t *testing.T) {
	c, mem := newTestCPU()
	c.SR |= srFD
	c.PC = codeBase
	execOne(c, mem, 0xF000|0<<8|0x8D) // FLDI0 FR0 with FD=1
	if c.EXPEVT != 0x800 {
		t.Errorf("FPU disabled: EXPEVT=%#x, want 0x800", c.EXPEVT)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	c, _ := newTestCPU()
	for i := range c.R {
		c.R[i] = 0x1000 + uint32(i)
	}
	for i := range c.Bank {
		c.Bank[i] = 0x2000 + uint32(i)
	}
	c.PC, c.PR, c.SR, c.GBR, c.VBR = 0xA0001234, 0x11112222, 0x400000F0, 0x33334444, 0x55556666
	c.SSR, c.SPC, c.SGR, c.DBR = 0x77778888, 0x9999AAAA, 0xBBBBCCCC, 0xDDDDEEEE
	c.MACH, c.MACL = 0x01234567, 0x89ABCDEF
	c.FPUL, c.FPSCR = 0x0F0F0F0F, 0x00140001
	for i := range c.FR {
		c.FR[i] = 0x3000 + uint32(i)
		c.XFR[i] = 0x4000 + uint32(i)
	}
	c.MMUCR, c.PTEH, c.PTEL, c.PTEA, c.TTB, c.TEA = 1, 2, 3, 4, 5, 6
	c.EXPEVT, c.INTEVT, c.TRA, c.TTR = 0x160, 0x200, 0x84, 0
	c.UTLB[3] = TLBEntry{VPN: 0x00400000, PPN: 0x00800000, Size: 1, V: true, PR: 3, D: true, SH: false}
	c.UTLB[63] = TLBEntry{VPN: 0x10000000, PPN: 0x04000000, Size: 3, V: true, PR: 2, D: false, SH: true}
	c.Halted = true

	b1 := c.Serialize()
	if len(b1) != SerializeSize {
		t.Fatalf("Serialize length = %d, want SerializeSize %d", len(b1), SerializeSize)
	}
	// Scramble, restore, and require byte-identical re-serialization.
	c.Reset()
	c.R[0] = 0xFFFFFFFF
	if !c.Deserialize(b1) {
		t.Fatal("Deserialize returned false")
	}
	b2 := c.Serialize()
	if !bytes.Equal(b1, b2) {
		t.Fatal("serialize after deserialize differs from the original")
	}
	if c.Deserialize(b1[:len(b1)-1]) {
		t.Fatal("Deserialize accepted a short buffer")
	}
	if c.Deserialize(append(append([]byte{}, b1...), 0)) {
		t.Fatal("Deserialize accepted a long buffer")
	}
}
