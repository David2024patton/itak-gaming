// Instruction execution for the W65C816S: the 256-opcode
// dispatch, the addressing-mode access sequences, and the
// operations themselves.
//
// Access sequences follow the datasheet's cycle tables.
// Operand fetches read from the program bank; data accesses
// read from the data bank (or bank 0 for direct page, stack
// relative, and pointer fetches); indexed modes perform the
// documented dummy read at the partially computed address
// (high byte not yet fixed) whenever a page is crossed, and
// always for stores and read-modify-writes. Sixteen-bit
// values move low byte first for loads and stores, and high
// byte first on the write-back of a read-modify-write, as
// the datasheet's tables show.
package w65c816

// setFlag sets or clears one status bit.
func (c *CPU) setFlag(f uint8, on bool) {
	if on {
		c.p |= f
	} else {
		c.p &^= f
	}
}

// setNZ8 sets N and Z from an 8-bit result.
func (c *CPU) setNZ8(v uint8) {
	c.setFlag(FlagN, v&0x80 != 0)
	c.setFlag(FlagZ, v == 0)
}

// setNZ16 sets N and Z from a 16-bit result.
func (c *CPU) setNZ16(v uint16) {
	c.setFlag(FlagN, v&0x8000 != 0)
	c.setFlag(FlagZ, v == 0)
}

// setNZM sets N and Z from a result at the M width.
func (c *CPU) setNZM(v uint16) {
	if c.m8() {
		c.setNZ8(uint8(v))
	} else {
		c.setNZ16(v)
	}
}

// setNZX sets N and Z from a result at the X width.
func (c *CPU) setNZX(v uint16) {
	if c.x8() {
		c.setNZ8(uint8(v))
	} else {
		c.setNZ16(v)
	}
}

// setA stores an M-width result in the accumulator: in
// 8-bit mode only the low byte changes and the B
// accumulator is preserved.
func (c *CPU) setA(v uint16) {
	if c.m8() {
		c.a = (c.a & 0xFF00) | (v & 0x00FF)
	} else {
		c.a = v
	}
}

// setX stores an X-width result in X; the high byte clears
// in 8-bit index mode.
func (c *CPU) setX(v uint16) {
	if c.x8() {
		c.x = v & 0x00FF
	} else {
		c.x = v
	}
}

// setY stores an X-width result in Y.
func (c *CPU) setY(v uint16) {
	if c.x8() {
		c.y = v & 0x00FF
	} else {
		c.y = v
	}
}

// hiBank picks the bank of a 16-bit access's second
// byte: bank 0 accesses (direct page, stack relative)
// wrap inside bank 0, while data bank accesses carry
// into the next bank at 0xFFFF (the corpus shows both:
// STZ dp wraps its high byte to bank 0, LDA abs,X reads
// its high byte from DBR+1).
func hiBank(bank uint8, addr uint16) uint8 {
	if bank == 0 {
		return 0
	}
	return bank + uint8((uint32(addr)+1)>>16)
}

// readW reads an M-width value at a bank:offset address.
func (c *CPU) readW(bank uint8, addr uint16) uint16 {
	lo := c.read(bank, addr)
	if c.m8() {
		return uint16(lo)
	}
	hi := c.read(hiBank(bank, addr), addr+1)
	return uint16(hi)<<8 | uint16(lo)
}

// readWL reads an M-width value at a 24-bit address; the
// second byte increments the full address (into the next
// bank at a boundary).
func (c *CPU) readWL(addr uint32) uint16 {
	lo := c.read24(addr)
	if c.m8() {
		return uint16(lo)
	}
	hi := c.read24(addr + 1)
	return uint16(hi)<<8 | uint16(lo)
}

// writeW stores an M-width value, low byte first; the
// high byte's bank follows hiBank.
func (c *CPU) writeW(bank uint8, addr uint16, v uint16) {
	c.write(bank, addr, uint8(v))
	if !c.m8() {
		c.write(hiBank(bank, addr), addr+1, uint8(v>>8))
	}
}

// writeWL stores an M-width value at a 24-bit address.
func (c *CPU) writeWL(addr uint32, v uint16) {
	c.write24(addr, uint8(v))
	if !c.m8() {
		c.write24(addr+1, uint8(v>>8))
	}
}

// readXW reads an X-width value at a bank:offset address;
// the high byte's bank follows hiBank.
func (c *CPU) readXW(bank uint8, addr uint16) uint16 {
	lo := c.read(bank, addr)
	if c.x8() {
		return uint16(lo)
	}
	hi := c.read(hiBank(bank, addr), addr+1)
	return uint16(hi)<<8 | uint16(lo)
}

// writeXW stores an X-width value, low byte first; the
// high byte's bank follows hiBank.
func (c *CPU) writeXW(bank uint8, addr uint16, v uint16) {
	c.write(bank, addr, uint8(v))
	if !c.x8() {
		c.write(hiBank(bank, addr), addr+1, uint8(v>>8))
	}
}

// --- direct page address computation -------------------------------------

// dpAddrEx computes a direct-page address: the direct
// page register plus an 8-bit offset, an index, and a small
// displacement (for pointer byte fetches). In emulation
// mode with the direct page low byte zero, the additions
// wrap within the page the register points at, the classic
// 6502 behavior; otherwise the full 16-bit sum is used.
func (c *CPU) dpAddrEx(off uint8, idx uint16, n uint16) uint16 {
	if c.e && c.d&0x00FF == 0 {
		return (c.d & 0xFF00) | uint16(uint8(off)+uint8(idx)+uint8(n))
	}
	return c.d + uint16(off) + idx + n
}

// dpOff fetches a direct-page offset byte and charges the
// extra cycle the hardware spends when the direct page low
// byte is nonzero.
func (c *CPU) dpOff() uint8 {
	off := c.fetch()
	if c.d&0x00FF != 0 {
		c.io()
	}
	return off
}

// dpPtr16 fetches a 16-bit pointer from direct page. The
// base address (offset plus index) follows the direct
// page rules, including the emulation page wrap on the
// addition itself; the high byte is then fetched from
// the next address up with a plain increment, page
// boundary or not (the corpus's (dp,X) trace at the
// boundary reads it from the following page).
func (c *CPU) dpPtr16(off uint8, idx uint16) uint16 {
	base := c.dpAddrEx(off, idx, 0)
	lo := c.read(0, base)
	hi := c.read(0, base+1)
	return uint16(hi)<<8 | uint16(lo)
}

// dpPtr16Wrapped fetches a 16-bit pointer the way PEI
// does: both bytes addressed through the direct page
// rules, so in emulation mode with DL = 0 the high byte
// wraps inside the page (PEI's corpus trace reads it
// from the page base at offset 0xFF, where the (dp)
// modes read the following page).
func (c *CPU) dpPtr16Wrapped(off uint8) uint16 {
	lo := c.read(0, c.dpAddrEx(off, 0, 0))
	hi := c.read(0, c.dpAddrEx(off, 0, 1))
	return uint16(hi)<<8 | uint16(lo)
}

// --- operations ----------------------------------------------------------

func (c *CPU) opORA(v uint16) {
	r := c.a | v
	c.setA(r)
	c.setNZM(c.a)
}

func (c *CPU) opAND(v uint16) {
	r := c.a & v
	c.setA(r)
	c.setNZM(c.a)
}

func (c *CPU) opEOR(v uint16) {
	r := c.a ^ v
	c.setA(r)
	c.setNZM(c.a)
}

func (c *CPU) opLDA(v uint16) {
	c.setA(v)
	c.setNZM(v)
}

func (c *CPU) opLDX(v uint16) {
	c.setX(v)
	c.setNZX(v)
}

func (c *CPU) opLDY(v uint16) {
	c.setY(v)
	c.setNZX(v)
}

func (c *CPU) carryIn() uint16 {
	if c.p&FlagC != 0 {
		return 1
	}
	return 0
}

func (c *CPU) opADC(v uint16) {
	if c.p&FlagD != 0 {
		c.adcDecimal(v)
		return
	}
	cy := c.carryIn()
	if c.m8() {
		a := c.a & 0x00FF
		m := v & 0x00FF
		r := a + m + cy
		c.setFlag(FlagV, (^(a^m)&(a^r)&0x0080) != 0)
		c.setFlag(FlagC, r > 0x00FF)
		c.setA(r)
		c.setNZ8(uint8(r))
		return
	}
	r := uint32(c.a) + uint32(v) + uint32(cy)
	c.setFlag(FlagV, (^(c.a^v)&(c.a^uint16(r))&0x8000) != 0)
	c.setFlag(FlagC, r > 0xFFFF)
	c.a = uint16(r)
	c.setNZ16(c.a)
}

func (c *CPU) opSBC(v uint16) {
	if c.p&FlagD != 0 {
		c.sbcDecimal(v)
		return
	}
	cy := c.carryIn()
	if c.m8() {
		a := c.a & 0x00FF
		m := v & 0x00FF
		r := a - m - (1 - cy)
		c.setFlag(FlagV, ((a^m)&(a^r)&0x0080) != 0)
		c.setFlag(FlagC, r <= 0x00FF)
		c.setA(r & 0x00FF)
		c.setNZ8(uint8(r))
		return
	}
	r := uint32(c.a) - uint32(v) - uint32(1-cy)
	c.setFlag(FlagV, ((c.a^v)&(c.a^uint16(r))&0x8000) != 0)
	c.setFlag(FlagC, r <= 0xFFFF)
	c.a = uint16(r)
	c.setNZ16(c.a)
}

// bcdAddByte adds two bytes as BCD with a carry in,
// returning the adjusted byte, the carry out, and the
// intermediate sum taken after the low nibble's adjust
// but before the high nibble's: the value the overflow
// flag is computed from, on this core as on the 65C02
// family. The low adjust is the hardware's: a nibble sum
// of ten or more gets six added, and the carry between
// nibbles is a plain boolean (the corpus's invalid-digit
// cases pin this down: 0x5F + 0x2C in decimal is 0x81
// with no carry, because the low nibble's 0x1B adjusts to
// a digit of 1 with a carry of exactly 1 into the high
// nibble's 5 + 2 + 1 = 8).
func bcdAddByte(a, m, cy int) (uint8, int, int) {
	lo := (a & 0x0F) + (m & 0x0F) + cy
	if lo >= 10 {
		lo += 6
	}
	c1 := 0
	if lo > 0x0F {
		c1 = 1
	}
	mid := (a & 0xF0) + (m & 0xF0) + (lo & 0x0F) + c1<<4
	r, cout := mid, 0
	if r >= 0xA0 {
		r += 0x60
	}
	if r > 0xFF {
		cout = 1
	}
	return uint8(r), cout, mid
}

// adcDecimal performs BCD addition at the M width. The
// sign and zero flags come from the adjusted result; the
// overflow flag comes from the intermediate sum (see
// bcdAddByte), which is the documented 65C02-family
// decimal behavior the corpus exhibits. Note the corpus
// charges no extra cycle for decimal arithmetic on this
// core.
func (c *CPU) adcDecimal(v uint16) {
	cy := int(c.carryIn())
	if c.m8() {
		a := int(c.a & 0x00FF)
		m := int(v & 0x00FF)
		r, cout, mid := bcdAddByte(a, m, cy)
		c.setFlag(FlagV, (^(a^m)&(a^mid)&0x80) != 0)
		c.setFlag(FlagC, cout != 0)
		c.setA(uint16(r))
		c.setNZ8(r)
		return
	}
	a := int(c.a)
	m := int(v)
	loByte, cy2, _ := bcdAddByte(a&0x00FF, m&0x00FF, cy)
	hiA, hiM := (a>>8)&0x00FF, (m>>8)&0x00FF
	hiByte, cout, mid := bcdAddByte(hiA, hiM, cy2)
	c.setFlag(FlagV, (^(hiA^hiM)&(hiA^mid)&0x80) != 0)
	c.setFlag(FlagC, cout != 0)
	c.a = uint16(hiByte)<<8 | uint16(loByte)
	c.setNZ16(c.a)
}

// sbcDecimal performs BCD subtraction at the M width.
func (c *CPU) sbcDecimal(v uint16) {
	borrow := 1 - int(c.carryIn())
	if c.m8() {
		a := int(c.a & 0x00FF)
		m := int(v & 0x00FF)
		bin := a - m - borrow
		c.setFlag(FlagV, ((a^m)&(a^bin)&0x80) != 0)
		lo := (a & 0x0F) - (m & 0x0F) - borrow
		b2 := 0
		if lo < 0 {
			lo -= 6
			b2 = 1
		}
		hi := (a >> 4) - (m >> 4) - b2
		cOut := true
		if hi < 0 {
			hi -= 6
			cOut = false
		}
		r := uint8((hi&0x0F)<<4 | (lo & 0x0F))
		c.setFlag(FlagC, cOut)
		c.setA(uint16(r))
		c.setNZ8(r)
		return
	}
	a := int(c.a)
	m := int(v)
	bin := a - m - borrow
	c.setFlag(FlagV, ((a^m)&(a^bin)&0x8000) != 0)
	bcdByte := func(av, mv, b int) (uint8, int) {
		lo := (av & 0x0F) - (mv & 0x0F) - b
		b2 := 0
		if lo < 0 {
			lo -= 6
			b2 = 1
		}
		hi := (av >> 4) - (mv >> 4) - b2
		out := 0
		if hi < 0 {
			hi -= 6
			out = 1
		}
		return uint8((hi&0x0F)<<4 | (lo & 0x0F)), out
	}
	loByte, b1 := bcdByte(a&0x00FF, m&0x00FF, borrow)
	hiByte, bOut := bcdByte((a>>8)&0x00FF, (m>>8)&0x00FF, b1)
	c.setFlag(FlagC, bOut == 0)
	c.a = uint16(hiByte)<<8 | uint16(loByte)
	c.setNZ16(c.a)
}

func (c *CPU) opCMP(v uint16) {
	if c.m8() {
		a := c.a & 0x00FF
		m := v & 0x00FF
		c.setFlag(FlagC, a >= m)
		c.setNZ8(uint8(a - m))
		return
	}
	c.setFlag(FlagC, c.a >= v)
	c.setNZ16(c.a - v)
}

func (c *CPU) opCPX(v uint16) { c.compareIdx(c.x, v) }
func (c *CPU) opCPY(v uint16) { c.compareIdx(c.y, v) }

func (c *CPU) compareIdx(reg, v uint16) {
	if c.x8() {
		r := reg & 0x00FF
		m := v & 0x00FF
		c.setFlag(FlagC, r >= m)
		c.setNZ8(uint8(r - m))
		return
	}
	c.setFlag(FlagC, reg >= v)
	c.setNZ16(reg - v)
}

// opBIT tests the accumulator against memory. The immediate
// form sets only Z; memory forms also copy the value's top
// two bits into N and V (at the M width).
func (c *CPU) opBIT(v uint16, imm bool) {
	if !imm {
		if c.m8() {
			c.setFlag(FlagN, v&0x0080 != 0)
			c.setFlag(FlagV, v&0x0040 != 0)
		} else {
			c.setFlag(FlagN, v&0x8000 != 0)
			c.setFlag(FlagV, v&0x4000 != 0)
		}
	}
	if c.m8() {
		c.setFlag(FlagZ, (c.a&v)&0x00FF == 0)
	} else {
		c.setFlag(FlagZ, c.a&v == 0)
	}
}

// Shifts and rotates operate on M-width values; each returns
// the new value and sets C, N, Z on the way.

func (c *CPU) opASL(v uint16) uint16 {
	if c.m8() {
		c.setFlag(FlagC, v&0x0080 != 0)
		r := uint16(uint8(v) << 1)
		c.setNZ8(uint8(r))
		return r
	}
	c.setFlag(FlagC, v&0x8000 != 0)
	r := v << 1
	c.setNZ16(r)
	return r
}

func (c *CPU) opLSR(v uint16) uint16 {
	if c.m8() {
		c.setFlag(FlagC, v&0x0001 != 0)
		r := uint16(uint8(v) >> 1)
		c.setNZ8(uint8(r))
		return r
	}
	c.setFlag(FlagC, v&0x0001 != 0)
	r := v >> 1
	c.setNZ16(r)
	return r
}

func (c *CPU) opROL(v uint16) uint16 {
	in := c.carryIn()
	if c.m8() {
		c.setFlag(FlagC, v&0x0080 != 0)
		r := uint16(uint8(v)<<1) | in
		c.setNZ8(uint8(r))
		return r
	}
	c.setFlag(FlagC, v&0x8000 != 0)
	r := (v << 1) | in
	c.setNZ16(r)
	return r
}

func (c *CPU) opROR(v uint16) uint16 {
	in := c.carryIn()
	if c.m8() {
		c.setFlag(FlagC, v&0x0001 != 0)
		r := uint16(uint8(v)>>1) | (in << 7)
		c.setNZ8(uint8(r))
		return r
	}
	c.setFlag(FlagC, v&0x0001 != 0)
	r := (v >> 1) | (in << 15)
	c.setNZ16(r)
	return r
}

func (c *CPU) opINC(v uint16) uint16 {
	r := (v + 1) & c.mMask()
	c.setNZM(r)
	return r
}

func (c *CPU) opDEC(v uint16) uint16 {
	r := (v - 1) & c.mMask()
	c.setNZM(r)
	return r
}

// mMask is the value mask at the M width.
func (c *CPU) mMask() uint16 {
	if c.m8() {
		return 0x00FF
	}
	return 0xFFFF
}

// --- addressing-mode runners ---------------------------------------------
//
// Each runner fetches its operands, performs the mode's
// access sequence, and hands the value (for reads) or the
// target address (for stores and read-modify-writes) to the
// operation.

type readFn func(v uint16)

// immM runs an immediate-mode M-width operation.
func (c *CPU) immM(fn readFn) {
	if c.m8() {
		fn(uint16(c.fetch()))
		return
	}
	fn(c.fetch16())
}

// immX runs an immediate-mode X-width operation.
func (c *CPU) immX(fn readFn) {
	if c.x8() {
		fn(uint16(c.fetch()))
		return
	}
	fn(c.fetch16())
}

// rdDP runs a direct-page read.
func (c *CPU) rdDP(fn readFn) {
	off := c.dpOff()
	fn(c.readW(0, c.dpAddrEx(off, 0, 0)))
}

// rdDPX runs a direct-page,X read.
func (c *CPU) rdDPX(fn readFn) {
	off := c.dpOff()
	c.io()
	fn(c.readW(0, c.dpAddrEx(off, c.x, 0)))
}

// rdDPY runs a direct-page,Y read (LDX only).
func (c *CPU) rdDPY(fn readFn) {
	off := c.dpOff()
	c.io()
	fn(c.readXW(0, c.dpAddrEx(off, c.y, 0)))
}

// rdDPIndX runs a (direct page,X) read.
func (c *CPU) rdDPIndX(fn readFn) {
	off := c.dpOff()
	c.io()
	ptr := c.dpPtr16(off, c.x)
	fn(c.readW(c.dbr, ptr))
}

// rdDPInd runs a (direct page) read.
func (c *CPU) rdDPInd(fn readFn) {
	off := c.dpOff()
	ptr := c.dpPtr16(off, 0)
	fn(c.readW(c.dbr, ptr))
}

// rdDPIndY runs a (direct page),Y read.
func (c *CPU) rdDPIndY(fn readFn) {
	off := c.dpOff()
	ptr := c.dpPtr16(off, 0)
	c.rdIndexedY(c.dbr, ptr, fn, c.readW)
}

// rdDPLong runs a [direct page] long read.
func (c *CPU) rdDPLong(fn readFn) {
	off := c.dpOff()
	ptr := c.dpPtr24Linear(off)
	fn(c.readWL(ptr))
}

// rdDPLongY runs a [direct page],Y long read.
func (c *CPU) rdDPLongY(fn readFn) {
	off := c.dpOff()
	ptr := c.dpPtr24(off) + uint32(c.y)
	fn(c.readWL(ptr & 0xFFFFFF))
}

// dpPtr24 fetches a 24-bit pointer from direct page for
// the [dp],Y modes: all three bytes follow the direct
// page wrap rules (in emulation with DL = 0 the bank
// byte's fetch wraps inside the page too: offset 0xFE
// reads it from the page base).
func (c *CPU) dpPtr24(off uint8) uint32 {
	lo := c.read(0, c.dpAddrEx(off, 0, 0))
	hi := c.read(0, c.dpAddrEx(off, 0, 1))
	bk := c.read(0, c.dpAddrEx(off, 0, 2))
	return uint32(bk)<<16 | uint32(hi)<<8 | uint32(lo)
}

// dpPtr24Linear fetches a 24-bit pointer for the plain
// [dp] modes: the base follows the direct page rules and
// the remaining bytes are plain increments from it (the
// corpus's lone DL = 0, offset 0xFE case for [dp] reads
// the bank byte from base+2 across the page line). The
// [dp],Y form wraps all three fetches instead; see
// dpPtr24. The split is what the corpus's sparse
// boundary cases show; see the README notes.
func (c *CPU) dpPtr24Linear(off uint8) uint32 {
	base := c.dpAddrEx(off, 0, 0)
	lo := c.read(0, base)
	hi := c.read(0, base+1)
	bk := c.read(0, base+2)
	return uint32(bk)<<16 | uint32(hi)<<8 | uint32(lo)
}

// rdAbs runs an absolute read.
func (c *CPU) rdAbs(fn readFn) {
	addr := c.fetch16()
	fn(c.readW(c.dbr, addr))
}

// rdAbsLong runs an absolute long read.
func (c *CPU) rdAbsLong(fn readFn) {
	addr := uint32(c.fetch16())
	bank := c.fetch()
	fn(c.readWL(uint32(bank)<<16 | addr))
}

// rdAbsX runs an absolute,X read.
func (c *CPU) rdAbsX(fn readFn) {
	base := c.fetch16()
	c.rdIndexed(c.dbr, base, c.x, fn, c.readW)
}

// rdAbsY runs an absolute,Y read.
func (c *CPU) rdAbsY(fn readFn) {
	base := c.fetch16()
	c.rdIndexed(c.dbr, base, c.y, fn, c.readW)
}

// rdAbsLongX runs an absolute long,X read. Long indexed
// modes never pay a page-cross cycle; the index carries
// into the bank.
func (c *CPU) rdAbsLongX(fn readFn) {
	addr := uint32(c.fetch16())
	bank := c.fetch()
	fn(c.readWL((uint32(bank)<<16 | addr) + uint32(c.x)))
}

// rdSR runs a stack-relative read.
func (c *CPU) rdSR(fn readFn) {
	off := c.fetch()
	c.io()
	fn(c.readW(0, c.s+uint16(off)))
}

// rdSRIndY runs a (stack relative),Y read: the pointer
// comes off the stack, the data from the data bank. The
// Y addition always costs a dead cycle, spent driving the
// pointer's high-byte address again (per the corpus
// trace), whether or not the addition crosses a page.
func (c *CPU) rdSRIndY(fn readFn) {
	off := c.fetch()
	c.io()
	loAddr := c.s + uint16(off)
	lo := c.read(0, loAddr)
	hi := c.read(0, loAddr+1)
	ptr := uint16(hi)<<8 | uint16(lo)
	c.dummy(0, loAddr+1)
	sum := uint32(ptr) + uint32(c.y)
	fn(c.readW(c.dbr+uint8(sum>>16), uint16(sum)))
}

// rdIndexed performs an indexed read within a bank. When
// the index addition changes the page, the hardware
// spends a dead cycle driving the partially fixed address
// (the corpus records it with no valid-access pins, so it
// is a dummy cycle here). When the addition overflows 16
// bits entirely, the data bank increments: the corpus's
// BIT abs,X trace reads its data from DBR+1 in that case.
func (c *CPU) rdIndexed(bank uint8, base, idx uint16, fn readFn, rd func(uint8, uint16) uint16) {
	sum := uint32(base) + uint32(idx)
	eff := uint16(sum)
	if (base^eff)&0xFF00 != 0 || !c.x8() {
		// The fixup cycle is owed when the addition
		// changes the page, and always when the index
		// registers are 16-bit (the corpus's (dp),Y trace
		// spends it on an in-page addition with a 16-bit
		// index whose value happens to be small).
		c.dummy(bank, (base&0xFF00)|(eff&0x00FF))
	}
	fn(rd(bank+uint8(sum>>16), eff))
}

// rdIndexedY is rdIndexed for a pointer already fetched:
// the same carry rule against the pointer.
func (c *CPU) rdIndexedY(bank uint8, ptr uint16, fn readFn, rd func(uint8, uint16) uint16) {
	c.rdIndexed(bank, ptr, c.y, fn, rd)
}

// --- store runners ---------------------------------------------------------

type addrFn func(bank uint8, addr uint16)

// stIndexed is shared by the indexed store modes: the
// dead fixup cycle always happens (a dummy cycle; see
// rdIndexed), and a 16-bit overflow carries into the
// bank like the read side.
func (c *CPU) stIndexed(bank uint8, base, idx uint16, wr func(uint8, uint16)) {
	sum := uint32(base) + uint32(idx)
	c.dummy(bank, (base&0xFF00)|(uint16(sum)&0x00FF))
	wr(bank+uint8(sum>>16), uint16(sum))
}

func (c *CPU) stDP(v uint16) {
	off := c.dpOff()
	c.writeW(0, c.dpAddrEx(off, 0, 0), v)
}

func (c *CPU) stDPX(v uint16) {
	off := c.dpOff()
	c.io()
	c.writeW(0, c.dpAddrEx(off, c.x, 0), v)
}

func (c *CPU) stDPIndX(v uint16) {
	off := c.dpOff()
	c.io()
	ptr := c.dpPtr16(off, c.x)
	c.writeW(c.dbr, ptr, v)
}

func (c *CPU) stDPInd(v uint16) {
	off := c.dpOff()
	ptr := c.dpPtr16(off, 0)
	c.writeW(c.dbr, ptr, v)
}

func (c *CPU) stDPIndY(v uint16) {
	off := c.dpOff()
	ptr := c.dpPtr16(off, 0)
	c.stIndexed(c.dbr, ptr, c.y, func(b uint8, a uint16) { c.writeW(b, a, v) })
}

func (c *CPU) stDPLong(v uint16) {
	off := c.dpOff()
	ptr := c.dpPtr24Linear(off)
	c.writeWL(ptr, v)
}

func (c *CPU) stDPLongY(v uint16) {
	off := c.dpOff()
	ptr := (c.dpPtr24(off) + uint32(c.y)) & 0xFFFFFF
	c.writeWL(ptr, v)
}

func (c *CPU) stAbs(v uint16) {
	addr := c.fetch16()
	c.writeW(c.dbr, addr, v)
}

func (c *CPU) stAbsLong(v uint16) {
	addr := uint32(c.fetch16())
	bank := c.fetch()
	c.writeWL(uint32(bank)<<16|addr, v)
}

func (c *CPU) stAbsX(v uint16) {
	base := c.fetch16()
	c.stIndexed(c.dbr, base, c.x, func(b uint8, a uint16) { c.writeW(b, a, v) })
}

func (c *CPU) stAbsY(v uint16) {
	base := c.fetch16()
	c.stIndexed(c.dbr, base, c.y, func(b uint8, a uint16) { c.writeW(b, a, v) })
}

func (c *CPU) stAbsLongX(v uint16) {
	addr := uint32(c.fetch16())
	bank := c.fetch()
	c.writeWL((uint32(bank)<<16|addr)+uint32(c.x), v)
}

func (c *CPU) stSR(v uint16) {
	off := c.fetch()
	c.io()
	c.writeW(0, c.s+uint16(off), v)
}

func (c *CPU) stSRIndY(v uint16) {
	off := c.fetch()
	c.io()
	loAddr := c.s + uint16(off)
	lo := c.read(0, loAddr)
	hi := c.read(0, loAddr+1)
	ptr := uint16(hi)<<8 | uint16(lo)
	c.dummy(0, loAddr+1)
	sum := uint32(ptr) + uint32(c.y)
	c.writeW(c.dbr+uint8(sum>>16), uint16(sum), v)
}

// Index stores at X width (STX uses dp,Y; STY uses dp,X).

func (c *CPU) stXDP(v uint16) {
	off := c.dpOff()
	c.writeXW(0, c.dpAddrEx(off, 0, 0), v)
}

func (c *CPU) stXDPX(v uint16) { // STY dp,X
	off := c.dpOff()
	c.io()
	c.writeXW(0, c.dpAddrEx(off, c.x, 0), v)
}

func (c *CPU) stXDPY(v uint16) { // STX dp,Y
	off := c.dpOff()
	c.io()
	c.writeXW(0, c.dpAddrEx(off, c.y, 0), v)
}

func (c *CPU) stXAbs(v uint16) {
	addr := c.fetch16()
	c.writeXW(c.dbr, addr, v)
}

// --- read-modify-write runners ---------------------------------------------

type rmwFn func(v uint16) uint16

// rmwFinish writes a modified value back, matching the
// corpus traces. In 8-bit mode the hardware first drives
// the original value back out (the dummy write: a real
// bus write the corpus does not count as a valid access)
// and then writes the modified value, with no internal
// cycle. In 16-bit mode the modify costs one internal
// cycle, then the new value is written high byte first.
func (c *CPU) rmwFinish(bank uint8, addr uint16, orig, v uint16) {
	if c.m8() {
		c.writeDummy(bank, addr, uint8(orig))
		c.write(bank, addr, uint8(v))
		return
	}
	c.io()
	c.write(bank, addr+1, uint8(v>>8))
	c.write(bank, addr, uint8(v))
}

func (c *CPU) rmwDP(fn rmwFn) {
	off := c.dpOff()
	addr := c.dpAddrEx(off, 0, 0)
	v := c.readW(0, addr)
	c.rmwFinish(0, addr, v, fn(v))
}

func (c *CPU) rmwDPX(fn rmwFn) {
	off := c.dpOff()
	c.io()
	addr := c.dpAddrEx(off, c.x, 0)
	v := c.readW(0, addr)
	c.rmwFinish(0, addr, v, fn(v))
}

func (c *CPU) rmwAbs(fn rmwFn) {
	addr := c.fetch16()
	v := c.readW(c.dbr, addr)
	c.rmwFinish(c.dbr, addr, v, fn(v))
}

func (c *CPU) rmwAbsX(fn rmwFn) {
	base := c.fetch16()
	sum := uint32(base) + uint32(c.x)
	c.dummy(c.dbr, (base&0xFF00)|(uint16(sum)&0x00FF))
	bank := c.dbr + uint8(sum>>16)
	addr := uint16(sum)
	v := c.readW(bank, addr)
	c.rmwFinish(bank, addr, v, fn(v))
}

// --- branches and jumps ----------------------------------------------------

// branch performs a short relative branch: the taken
// case costs one internal cycle. The page-cross penalty
// the 6502 pays applies on this core only in emulation
// mode; the corpus's native-mode traces cross pages in
// three cycles total.
func (c *CPU) branch(cond bool) {
	off := int8(c.fetch())
	if !cond {
		return
	}
	c.io()
	target := uint16(int32(c.pc) + int32(off))
	if c.e && (c.pc^target)&0xFF00 != 0 {
		c.io()
	}
	c.pc = target
}

// branchLong performs a 16-bit relative branch (BRA and
// BRL: always taken, 4 cycles either way).
func (c *CPU) branchLong() {
	off := int16(c.fetch16())
	c.io()
	c.pc = uint16(int32(c.pc) + int32(off))
}

// --- block moves -----------------------------------------------------------

// execBlockMove executes an MVN (0x54) or MVP (0x44) from
// its opcode fetch (already done by Step) to completion.
// The corpus's traces pin the shape: the move runs as a
// series of identical seven-cycle iterations, each one
// re-fetching the opcode and both bank operands
// (destination first, then source, as they lie in memory),
// reading the source byte, writing the destination byte,
// and spending two dead cycles at the write address. DBR
// takes the destination bank as the move runs. Between
// iterations the interrupt poll is honored: if an
// interrupt was recognized the move suspends with PC back
// on its own opcode byte and A, X, and Y holding the
// progress, so the interrupt is taken at the boundary and
// the re-executed move continues on return, which is the
// hardware's documented block-move interrupt behavior.
func (c *CPU) execBlockMove(op uint8) {
	c.mvOp = op
	opAddr := c.pc - 1
	c.mvOpcodePC = opAddr
	first := true
	for {
		if !first {
			c.fetch() // the iteration's opcode re-fetch
		}
		first = false
		c.mvDstBank = c.fetch()
		c.mvSrcBank = c.fetch()
		c.dbr = c.mvDstBank
		v := c.read(c.mvSrcBank, c.x)
		c.write(c.mvDstBank, c.y, v)
		wa := c.y // the dead cycles sit on the write address
		// The register updates land before the iteration's
		// two dead cycles: the corpus's states cut at a
		// completing move's final cycle already show them.
		if op == 0x54 { // MVN counts up
			c.x++
			c.y++
		} else { // MVP counts down
			c.x--
			c.y--
		}
		if c.x8() {
			// In 8-bit index mode the pointers wrap inside
			// their low bytes, like every other index
			// register update.
			c.x &= 0x00FF
			c.y &= 0x00FF
		}
		c.a--
		done := c.a == 0xFFFF
		c.dummy(c.mvDstBank, wa)
		c.dummy(c.mvDstBank, wa)
		if done {
			return
		}
		c.pc = opAddr
		// Poll between bytes: the samples from the move's
		// cycles decide whether an interrupt suspends it.
		c.latchPoll()
		if c.takeNMI || c.takeIRQ || c.takeAbort {
			return
		}
	}
}

// --- dispatch ---------------------------------------------------------------

// alu selects an M-width ALU operation by its group index:
// the order matches the opcode map's aaa field for the
// cc=01 family (0 ORA, 1 AND, 2 EOR, 3 ADC, 4 STA, 5 LDA,
// 6 CMP, 7 SBC).
func (c *CPU) alu(aaa uint8, v uint16) {
	switch aaa {
	case 0:
		c.opORA(v)
	case 1:
		c.opAND(v)
	case 2:
		c.opEOR(v)
	case 3:
		c.opADC(v)
	case 5:
		c.opLDA(v)
	case 6:
		c.opCMP(v)
	case 7:
		c.opSBC(v)
	}
}

// aluMode runs the ALU operation aaa in addressing mode
// bbb of the cc=01 family: 0 (dp,X), 1 dp, 2 immediate,
// 3 absolute, 4 (dp),Y, 5 dp,X, 6 absolute,Y, 7 absolute,X.
// STA (aaa 4) has a store runner per mode instead, and its
// immediate slot is BIT #.
func (c *CPU) aluMode(aaa, bbb uint8) {
	if aaa == 4 { // STA
		switch bbb {
		case 0:
			c.stDPIndX(c.a)
		case 1:
			c.stDP(c.a)
		case 3:
			c.stAbs(c.a)
		case 4:
			c.stDPIndY(c.a)
		case 5:
			c.stDPX(c.a)
		case 6:
			c.stAbsY(c.a)
		case 7:
			c.stAbsX(c.a)
		}
		return
	}
	fn := func(v uint16) { c.alu(aaa, v) }
	switch bbb {
	case 0:
		c.rdDPIndX(fn)
	case 1:
		c.rdDP(fn)
	case 2:
		c.immM(fn)
	case 3:
		c.rdAbs(fn)
	case 4:
		c.rdDPIndY(fn)
	case 5:
		c.rdDPX(fn)
	case 6:
		c.rdAbsY(fn)
	case 7:
		c.rdAbsX(fn)
	}
}

// aluExt runs the additional modes the 65816 adds around
// the cc=01 family: stack relative, direct page indirect
// variants, and the longs. slot names the mode by its
// offset in the opcode row (0x03 sr, 0x07 [dp], 0x12 (dp),
// 0x0F absl, 0x13 (sr,s),Y, 0x17 [dp],Y, 0x1F absl,X).
func (c *CPU) aluExt(aaa uint8, slot uint8) {
	if aaa == 4 { // STA
		switch slot {
		case 0x03:
			c.stSR(c.a)
		case 0x07:
			c.stDPLong(c.a)
		case 0x12:
			c.stDPInd(c.a)
		case 0x0F:
			c.stAbsLong(c.a)
		case 0x13:
			c.stSRIndY(c.a)
		case 0x17:
			c.stDPLongY(c.a)
		case 0x1F:
			c.stAbsLongX(c.a)
		}
		return
	}
	fn := func(v uint16) { c.alu(aaa, v) }
	switch slot {
	case 0x03:
		c.rdSR(fn)
	case 0x07:
		c.rdDPLong(fn)
	case 0x12:
		c.rdDPInd(fn)
	case 0x0F:
		c.rdAbsLong(fn)
	case 0x13:
		c.rdSRIndY(fn)
	case 0x17:
		c.rdDPLongY(fn)
	case 0x1F:
		c.rdAbsLongX(fn)
	}
}

// exec executes the instruction whose opcode was just
// fetched. The dispatch is an explicit switch over all 256
// opcodes: the 65816 map extends the 6502's regular groups
// with enough single opcodes that a table reads clearer
// than a decoder.
func (c *CPU) exec(op uint8) {
	// The cc=01 ALU family occupies opcodes whose low two
	// bits are 01, laid out as (aaa<<5)|(bbb<<2)|1.
	if op&0x03 == 0x01 {
		aaa := op >> 5
		bbb := (op >> 2) & 0x07
		if aaa == 4 && bbb == 2 {
			// 0x89: BIT # takes STA's immediate slot.
			c.immM(func(v uint16) { c.opBIT(v, true) })
			return
		}
		c.aluMode(aaa, bbb)
		return
	}
	// The 65816's extra cc=01 modes sit at offsets 0x02,
	// 0x03, 0x07, 0x0F, 0x12, 0x13, 0x17, 0x1F in each row
	// for some slots; those opcodes are dispatched in the
	// switch below by their exact values.
	switch op {
	// Row 0x00
	case 0x00: // BRK
		c.softwareInterrupt(false)
	case 0x02: // COP
		c.softwareInterrupt(true)
	case 0x03, 0x07, 0x0F, 0x13, 0x17, 0x1F, 0x12:
		c.aluExt(0, op&0x1F) // ORA extended modes (0x12 grouped here)
	case 0x04: // TSB dp
		c.rmwDP(func(v uint16) uint16 {
			c.setFlag(FlagZ, c.a&v&c.mMask() == 0)
			return v | c.a&c.mMask()
		})
	case 0x06: // ASL dp
		c.rmwDP(c.opASL)
	case 0x08: // PHP
		c.io()
		c.push(c.statusByte(true))
	case 0x0A: // ASL A
		c.io()
		c.setA(c.opASL(c.a))
	case 0x0B: // PHD
		c.io()
		c.push16(c.d)
	case 0x0C: // TSB abs
		c.rmwAbs(func(v uint16) uint16 {
			c.setFlag(FlagZ, c.a&v&c.mMask() == 0)
			return v | c.a&c.mMask()
		})
	case 0x0E: // ASL abs
		c.rmwAbs(c.opASL)

	// Row 0x10
	case 0x10: // BPL
		c.branch(c.p&FlagN == 0)
	case 0x14: // TRB dp
		c.rmwDP(func(v uint16) uint16 {
			c.setFlag(FlagZ, c.a&v&c.mMask() == 0)
			return v &^ (c.a & c.mMask())
		})
	case 0x16: // ASL dp,X
		c.rmwDPX(c.opASL)
	case 0x18: // CLC
		c.io()
		c.p &^= FlagC
	case 0x1A: // INC A
		c.io()
		c.setA(c.opINC(c.a))
	case 0x1B: // TCS
		c.io()
		if c.e {
			c.s = 0x0100 | (c.a & 0x00FF)
		} else {
			c.s = c.a
		}
	case 0x1C: // TRB abs
		c.rmwAbs(func(v uint16) uint16 {
			c.setFlag(FlagZ, c.a&v&c.mMask() == 0)
			return v &^ (c.a & c.mMask())
		})
	case 0x1E: // ASL abs,X
		c.rmwAbsX(c.opASL)
	case 0x23, 0x27, 0x2F, 0x33, 0x37, 0x3F, 0x32:
		c.aluExt(1, op&0x1F) // AND extended modes

	// Row 0x20
	case 0x20: // JSR abs
		addr := c.fetch16()
		c.io()
		ret := c.pc - 1 // return address: the high operand byte
		c.pushInt(uint8(ret >> 8))
		c.pushInt(uint8(ret))
		c.pc = addr
	case 0x22: // JSR long (JSL)
		addr := c.fetch16()
		c.push(c.pbr)
		c.io()
		bank := c.fetch()
		c.push16(c.pc - 1) // return address: the bank operand byte
		c.pbr = bank
		c.pc = addr
	case 0x24: // BIT dp
		c.rdDP(func(v uint16) { c.opBIT(v, false) })
	case 0x26: // ROL dp
		c.rmwDP(c.opROL)
	case 0x28: // PLP
		c.io()
		c.setP(c.pullB())
		c.io()
	case 0x2A: // ROL A
		c.io()
		c.setA(c.opROL(c.a))
	case 0x2B: // PLD
		c.io()
		c.d = c.pull16()
		c.setNZ16(c.d)
		c.io()
	case 0x2C: // BIT abs
		c.rdAbs(func(v uint16) { c.opBIT(v, false) })
	case 0x2E: // ROL abs
		c.rmwAbs(c.opROL)

	// Row 0x30
	case 0x30: // BMI
		c.branch(c.p&FlagN != 0)
	case 0x34: // BIT dp,X
		off := c.dpOff()
		c.io()
		c.opBIT(c.readW(0, c.dpAddrEx(off, c.x, 0)), false)
	case 0x36: // ROL dp,X
		c.rmwDPX(c.opROL)
	case 0x38: // SEC
		c.io()
		c.p |= FlagC
	case 0x3A: // DEC A
		c.io()
		c.setA(c.opDEC(c.a))
	case 0x3B: // TSC
		c.io()
		c.a = c.s
		c.setNZ16(c.a)
	case 0x3C: // BIT abs,X
		base := c.fetch16()
		c.rdIndexed(c.dbr, base, c.x, func(v uint16) { c.opBIT(v, false) }, c.readW)
	case 0x3E: // ROL abs,X
		c.rmwAbsX(c.opROL)
	case 0x43, 0x47, 0x4F, 0x53, 0x57, 0x5F, 0x52:
		c.aluExt(2, op&0x1F) // EOR extended modes

	// Row 0x40
	case 0x40: // RTI
		c.io()
		c.io()
		c.setP(c.pullB())
		if c.e {
			lo := c.pullB()
			hi := c.pullB()
			c.pc = uint16(hi)<<8 | uint16(lo)
		} else {
			c.pc = c.pull16()
			c.pbr = c.pull()
		}
	case 0x42: // WDM: a two-byte NOP; the reserved second
		// byte is skipped in a dead cycle, not fetched (the
		// corpus records only the opcode fetch as valid).
		c.dummy(c.pbr, c.pc)
		c.pc++
	case 0x44: // MVP
		c.execBlockMove(op)
	case 0x50: // BVC
		c.branch(c.p&FlagV == 0)
	case 0x58: // CLI
		c.io()
		c.p &^= FlagI
	case 0x46: // LSR dp
		c.rmwDP(c.opLSR)
	case 0x48: // PHA
		c.io()
		if c.m8() {
			c.push(uint8(c.a))
		} else {
			c.push16(c.a)
		}
	case 0x4A: // LSR A
		c.io()
		c.setA(c.opLSR(c.a))
	case 0x4B: // PHK
		c.io()
		c.push(c.pbr)
	case 0x4C: // JMP abs
		c.pc = c.fetch16()
	case 0x4E: // LSR abs
		c.rmwAbs(c.opLSR)
	case 0x54: // MVN
		c.execBlockMove(op)
	case 0x56: // LSR dp,X
		c.rmwDPX(c.opLSR)
	case 0x5A: // PHY
		c.io()
		if c.x8() {
			c.push(uint8(c.y))
		} else {
			c.push16(c.y)
		}
	case 0x5B: // TCD
		c.io()
		c.d = c.a
		c.setNZ16(c.d)
	case 0x5C: // JMP absl
		addr := c.fetch16()
		c.pbr = c.fetch()
		c.pc = addr
	case 0x5E: // LSR abs,X
		c.rmwAbsX(c.opLSR)
	case 0x63, 0x67, 0x6F, 0x73, 0x77, 0x7F, 0x72:
		c.aluExt(3, op&0x1F) // ADC extended modes

	// Row 0x60
	case 0x60: // RTS
		c.io()
		if c.e {
			lo := c.pullB()
			hi := c.pullB()
			c.pc = uint16(hi)<<8 | uint16(lo)
		} else {
			c.pc = c.pull16()
		}
		c.io()
		c.pc++
		c.io()
	case 0x62: // PER
		off := int16(c.fetch16())
		c.io()
		c.push16(uint16(int32(c.pc) + int32(off)))
	case 0x64: // STZ dp
		c.stDP(0)
	case 0x66: // ROR dp
		c.rmwDP(c.opROR)
	case 0x68: // PLA
		c.io()
		if c.m8() {
			c.setA(uint16(c.pullB()))
		} else {
			c.setA(c.pull16())
		}
		c.setNZM(c.a)
		c.io()
	case 0x6A: // ROR A
		c.io()
		c.setA(c.opROR(c.a))
	case 0x6B: // RTL
		c.io()
		c.pc = c.pull16()
		c.pbr = c.pull()
		c.pc++
		c.io()
	case 0x6C: // JMP (abs)
		addr := c.fetch16()
		lo := c.read(0, addr)
		hi := c.read(0, addr+1)
		c.pc = uint16(hi)<<8 | uint16(lo)
	case 0x6E: // ROR abs
		c.rmwAbs(c.opROR)
	case 0x70: // BVS
		c.branch(c.p&FlagV != 0)
	case 0x74: // STZ dp,X
		c.stDPX(0)
	case 0x76: // ROR dp,X
		c.rmwDPX(c.opROR)
	case 0x78: // SEI
		c.io()
		c.p |= FlagI
	case 0x7A: // PLY
		c.io()
		if c.x8() {
			c.setY(uint16(c.pullB()))
		} else {
			c.setY(c.pull16())
		}
		c.setNZX(c.y)
		c.io()
	case 0x7B: // TDC: like TSC, it flags the moved value.
		c.io()
		c.a = c.d
		c.setNZ16(c.a)
	case 0x7C: // JMP (abs,X): the pointer table lives in
		// the program bank (unlike JMP [abs], whose pointer
		// is in bank 0; the corpus traces separate the two).
		addr := c.fetch16()
		c.io()
		sum := addr + c.x
		lo := c.read(c.pbr, sum)
		hi := c.read(c.pbr, sum+1)
		c.pc = uint16(hi)<<8 | uint16(lo)
	case 0x7E: // ROR abs,X
		c.rmwAbsX(c.opROR)
	case 0x83, 0x87, 0x8F, 0x93, 0x97, 0x9F, 0x92:
		c.aluExt(4, op&0x1F) // STA extended modes

	// Row 0x80
	case 0x80: // BRA
		c.branch(true)
	case 0x82: // BRL
		c.branchLong()
	case 0x84: // STY dp
		c.stXDP(c.y)
	case 0x86: // STX dp
		c.stXDP(c.x)
	case 0x88: // DEY
		c.io()
		c.setY(c.y - 1)
		c.setNZX(c.y)
	case 0x8A: // TXA
		c.io()
		if c.m8() {
			c.setA(c.x & 0x00FF)
		} else {
			c.setA(c.x)
		}
		c.setNZM(c.a)
	case 0x8B: // PHB
		c.io()
		c.push(c.dbr)
	case 0x8C: // STY abs
		c.stXAbs(c.y)
	case 0x8E: // STX abs
		c.stXAbs(c.x)
	case 0x90: // BCC
		c.branch(c.p&FlagC == 0)
	case 0x94: // STY dp,X
		c.stXDPX(c.y)
	case 0x96: // STX dp,Y
		c.stXDPY(c.x)
	case 0x98: // TYA
		c.io()
		if c.m8() {
			c.setA(c.y & 0x00FF)
		} else {
			c.setA(c.y)
		}
		c.setNZM(c.a)
	case 0x9A: // TXS
		c.io()
		if c.e {
			c.s = 0x0100 | (c.x & 0x00FF)
		} else {
			c.s = c.x
		}
	case 0x9B: // TXY
		c.io()
		c.setY(c.x)
		c.setNZX(c.y)
	case 0x9C: // STZ abs
		c.stAbs(0)
	case 0x9E: // STZ abs,X
		c.stAbsX(0)
	case 0xA3, 0xA7, 0xAF, 0xB3, 0xB7, 0xBF, 0xB2:
		c.aluExt(5, op&0x1F) // LDA extended modes

	// Row 0xA0
	case 0xA0: // LDY #
		c.immX(c.opLDY)
	case 0xA2: // LDX #
		c.immX(c.opLDX)
	case 0xA4: // LDY dp
		off := c.dpOff()
		c.opLDY(c.readXW(0, c.dpAddrEx(off, 0, 0)))
	case 0xA5:
		// (LDA dp is in the cc=01 family.)
	case 0xA6: // LDX dp
		off := c.dpOff()
		c.opLDX(c.readXW(0, c.dpAddrEx(off, 0, 0)))
	case 0xA8: // TAY
		c.io()
		c.setY(c.a)
		c.setNZX(c.y)
	case 0xAA: // TAX
		c.io()
		c.setX(c.a)
		c.setNZX(c.x)
	case 0xAB: // PLB
		c.io()
		c.dbr = c.pullB()
		c.setNZ8(c.dbr)
		c.io()
	case 0xAC: // LDY abs
		addr := c.fetch16()
		c.opLDY(c.readXW(c.dbr, addr))
	case 0xAE: // LDX abs
		addr := c.fetch16()
		c.opLDX(c.readXW(c.dbr, addr))
	case 0xB0: // BCS
		c.branch(c.p&FlagC != 0)
	case 0xB4: // LDY dp,X
		off := c.dpOff()
		c.io()
		c.opLDY(c.readXW(0, c.dpAddrEx(off, c.x, 0)))
	case 0xB6: // LDX dp,Y
		c.rdDPY(c.opLDX)
	case 0xB8: // CLV
		c.io()
		c.p &^= FlagV
	case 0xBA: // TSX
		c.io()
		if c.x8() {
			c.x = c.s & 0x00FF
		} else {
			c.x = c.s
		}
		c.setNZX(c.x)
	case 0xBB: // TYX
		c.io()
		c.setX(c.y)
		c.setNZX(c.x)
	case 0xBC: // LDY abs,X
		base := c.fetch16()
		c.rdIndexed(c.dbr, base, c.x, c.opLDY, c.readXW)
	case 0xBE: // LDX abs,Y
		base := c.fetch16()
		c.rdIndexed(c.dbr, base, c.y, c.opLDX, c.readXW)
	case 0xC3, 0xC7, 0xCF, 0xD3, 0xD7, 0xDF, 0xD2:
		c.aluExt(6, op&0x1F) // CMP extended modes

	// Row 0xC0
	case 0xC0: // CPY #
		c.immX(c.opCPY)
	case 0xC2: // REP #
		v := c.fetch()
		c.io()
		c.p &^= v
		// If REP widened the indexes, nothing is forced;
		// the stored high bytes simply count again.
	case 0xC4: // CPY dp
		off := c.dpOff()
		c.opCPY(c.readXW(0, c.dpAddrEx(off, 0, 0)))
	case 0xC6: // DEC dp
		c.rmwDP(c.opDEC)
	case 0xC8: // INY
		c.io()
		c.setY(c.y + 1)
		c.setNZX(c.y)
	case 0xCA: // DEX
		c.io()
		c.setX(c.x - 1)
		c.setNZX(c.x)
	case 0xCB: // WAI
		// The single-step corpus models WAI as the fetch
		// plus three internal cycles (the last standing in
		// for the wait itself); the wait proper continues
		// one internal cycle per Step until a wake input.
		c.io()
		c.io()
		c.io()
		c.waiting = true
	case 0xCC: // CPY abs
		addr := c.fetch16()
		c.opCPY(c.readXW(c.dbr, addr))
	case 0xCE: // DEC abs
		c.rmwAbs(c.opDEC)
	case 0xD0: // BNE
		c.branch(c.p&FlagZ == 0)
	case 0xD4: // PEI (dp)
		off := c.dpOff()
		ptr := c.dpPtr16Wrapped(off)
		c.push16(ptr)
	case 0xD6: // DEC dp,X
		c.rmwDPX(c.opDEC)
	case 0xD8: // CLD
		c.io()
		c.p &^= FlagD
	case 0xDA: // PHX
		c.io()
		if c.x8() {
			c.push(uint8(c.x))
		} else {
			c.push16(c.x)
		}
	case 0xDB: // STP: like WAI in the corpus's accounting,
		// the fetch plus three internal cycles.
		c.io()
		c.io()
		c.io()
		c.stopped = true
	case 0xDC: // JMP [abs]
		addr := c.fetch16()
		lo := c.read(0, addr)
		hi := c.read(0, addr+1)
		bk := c.read(0, addr+2)
		c.pbr = bk
		c.pc = uint16(hi)<<8 | uint16(lo)
	case 0xDE: // DEC abs,X
		c.rmwAbsX(c.opDEC)
	case 0xE3, 0xE7, 0xEF, 0xF3, 0xF7, 0xFF, 0xF2:
		c.aluExt(7, op&0x1F) // SBC extended modes

	// Row 0xE0
	case 0xE0: // CPX #
		c.immX(c.opCPX)
	case 0xE2: // SEP #
		v := c.fetch()
		c.io()
		c.p |= v
		if c.x8() {
			c.x &= 0x00FF
			c.y &= 0x00FF
		}
	case 0xE4: // CPX dp
		off := c.dpOff()
		c.opCPX(c.readXW(0, c.dpAddrEx(off, 0, 0)))
	case 0xE6: // INC dp
		c.rmwDP(c.opINC)
	case 0xE8: // INX
		c.io()
		c.setX(c.x + 1)
		c.setNZX(c.x)
	case 0xEA: // NOP
		c.io()
	case 0xEB: // XBA
		c.io()
		c.io()
		c.a = (c.a << 8) | (c.a >> 8)
		c.setNZ8(uint8(c.a))
	case 0xEC: // CPX abs
		addr := c.fetch16()
		c.opCPX(c.readXW(c.dbr, addr))
	case 0xEE: // INC abs
		c.rmwAbs(c.opINC)
	case 0xF0: // BEQ
		c.branch(c.p&FlagZ != 0)
	case 0xF4: // PEA
		v := c.fetch16()
		c.push16(v)
	case 0xF6: // INC dp,X
		c.rmwDPX(c.opINC)
	case 0xF8: // SED
		c.io()
		c.p |= FlagD
	case 0xFA: // PLX
		c.io()
		if c.x8() {
			c.setX(uint16(c.pullB()))
		} else {
			c.setX(c.pull16())
		}
		c.setNZX(c.x)
		c.io()
	case 0xFB: // XCE
		c.io()
		carry := c.p&FlagC != 0
		if c.e {
			c.p |= FlagC
		} else {
			c.p &^= FlagC
		}
		c.e = carry
		if c.e {
			c.s = 0x0100 | (c.s & 0x00FF)
			c.x &= 0x00FF
			c.y &= 0x00FF
		}
	case 0xFC: // JSR (abs,X): low operand byte, the return
		// address push (the high operand byte's address),
		// high operand byte, an internal cycle, then the
		// target pointer from the program bank; 8 cycles in
		// all per the corpus trace.
		lo := c.fetch()
		if c.e {
			// Emulation JSR pushes use the page-1 form on
			// every access, like JSR absolute's.
			c.pushInt(uint8(c.pc >> 8))
			c.pushInt(uint8(c.pc))
		} else {
			c.push16(c.pc) // PC points at the high operand byte
		}
		hi := c.fetch()
		c.io()
		ptr := uint16(hi)<<8 | uint16(lo)
		sum := ptr + c.x
		tlo := c.read(c.pbr, sum)
		thi := c.read(c.pbr, sum+1)
		c.pc = uint16(thi)<<8 | uint16(tlo)
	case 0xFE: // INC abs,X
		c.rmwAbsX(c.opINC)

	default:
		// The remaining opcodes belong to the cc=01 family
		// (handled above) or the extended-mode groups
		// dispatched in this switch. Anything reaching here
		// is a one-byte NOP in this table (0xA5 is LDA dp,
		// reached via the family decoder; it is listed in
		// the switch only to keep the map complete).
		c.io()
	}
}
