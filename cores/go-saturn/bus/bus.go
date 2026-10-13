// Package bus implements the shared Sega Saturn memory bus.
//
// Two SH-2 CPUs, a sound CPU, the SCU, both VDPs, the SMPC, the CD block
// and the cartridge port all sit behind one big endian address space.
// This slice provides plain storage for every region listed in the
// package contract, the MINIT and SINIT capture flags, and the SH-2
// on-chip divide unit. It does not model bus wait states, DMA, or any
// device command behaviour.
//
// Simplifications baked into this slice, stated plainly:
//
//   - The BIOS window reads as zero. No BIOS bytes exist in this core,
//     the window is an HLE stub.
//   - The A-bus CS0 and CS1 cartridge windows and the A-bus dummy area
//     read as the open value zero and ignore writes.
//   - The CD block register page mirrors one 64 byte block across the
//     whole CS2 window. Real hardware places the block at CS2 offset
//     0x90000 with its registers at the offsets defined below; mirroring
//     the block everywhere is the simplification for this slice.
//   - The SCU DSP program and data RAMs are not present yet. Only the
//     256 byte SCU register page is stored here; the DSP RAMs are a
//     later slice.
//   - Sound CPU work RAM is 512 KiB mirrored across its 1 MiB window.
//   - Division is computed immediately on the trigger write. The 39
//     cycle hardware latency is not modelled.
//   - On quotient overflow the remainder stored is the value computed
//     from the saturated quotient, truncated to 32 bits. The divide by
//     zero remainder follows the documented partial behaviour instead:
//     the dividend shifted right 29 places, arithmetic.
package bus

import "math"

// Exported divide unit register addresses. These are the documented
// SH7604 absolute addresses inside the on-chip window.
const (
	RegDVSR   uint32 = 0xFFFFFF00
	RegDVDNT  uint32 = 0xFFFFFF04
	RegDVCR   uint32 = 0xFFFFFF08
	RegVCRDIV uint32 = 0xFFFFFF0C
	RegDVDNTH uint32 = 0xFFFFFF10
	RegDVDNTL uint32 = 0xFFFFFF14
)

// Aliases used by some Saturn documentation for the same registers.
const (
	RegDIVSOR uint32 = RegDVSR
	RegDVDEN  uint32 = RegDVCR
	RegDIVSRC uint32 = RegDVDNTH
)

// CD block register offsets within the mirrored 64 byte block.
const (
	CDHIRQ     uint32 = 0x08
	CDHIRQMask uint32 = 0x0C
	CDCR1      uint32 = 0x18
	CDCR2      uint32 = 0x1C
	CDCR3      uint32 = 0x20
	CDCR4      uint32 = 0x24
)

// Short aliases for the CD block register offsets.
const (
	HIRQ     uint32 = CDHIRQ
	HIRQMask uint32 = CDHIRQMask
	CR1      uint32 = CDCR1
	CR2      uint32 = CDCR2
	CR3      uint32 = CDCR3
	CR4      uint32 = CDCR4
)

const (
	maskAddr     uint32 = 0x1FFFFFFF
	onChipBase   uint32 = 0x1FFFFE00
	onChipTop    uint32 = 0x1FFFFFFF
	onChipSize          = 0x200
	divFileStart uint32 = 0x100
	divFileEnd   uint32 = 0x120
)

// Divide register offsets inside the on-chip window.
const (
	divOffDVSR         uint32 = 0x100
	divOffDVDNT        uint32 = 0x104
	divOffDVCR         uint32 = 0x108
	divOffVCRDIV       uint32 = 0x10C
	divOffDVDNTH       uint32 = 0x110
	divOffDVDNTL       uint32 = 0x114
	divOffDVDNTHMirror uint32 = 0x118
	divOffDVDNTLMirror uint32 = 0x11C
)

// divUnit holds one CPU divide unit register file.
// The file is five storage words plus the control and vector words:
// divisor, dividend and quotient word, control, vector, dividend high
// and remainder word, and dividend low and quotient mirror word.
type divUnit struct {
	dvsr   uint32
	dvdnt  uint32
	dvcr   uint32
	vcrdiv uint32
	dvdnth uint32
	dvdntl uint32
}

// regValue returns the word stored for a divide register offset.
// The two mirror offsets read back the high and low words.
func (d *divUnit) regValue(off uint32) uint32 {
	switch off {
	case divOffDVSR:
		return d.dvsr
	case divOffDVDNT:
		return d.dvdnt
	case divOffDVCR:
		return d.dvcr
	case divOffVCRDIV:
		return d.vcrdiv
	case divOffDVDNTH, divOffDVDNTHMirror:
		return d.dvdnth
	case divOffDVDNTL, divOffDVDNTLMirror:
		return d.dvdntl
	}
	return 0
}

// assign stores a word without triggering a division.
// A write to the control word follows the hardware rule as a plain
// store: writing bit 0 as zero clears the overflow flag, writing it
// as one leaves the stored word with the flag set.
func (d *divUnit) assign(off uint32, v uint32) {
	switch off {
	case divOffDVSR:
		d.dvsr = v
	case divOffDVDNT:
		d.dvdnt = v
	case divOffDVCR:
		d.dvcr = v
	case divOffVCRDIV:
		d.vcrdiv = v
	case divOffDVDNTH, divOffDVDNTHMirror:
		d.dvdnth = v
	case divOffDVDNTL, divOffDVDNTLMirror:
		d.dvdntl = v
	}
}

// runDivide performs one signed division, truncating toward zero.
// dividend is the full signed dividend, the divisor is the stored
// DVSR word read as a signed value.
func (d *divUnit) runDivide(dividend int64) {
	divisor := int32(d.dvsr)
	if divisor == 0 {
		d.dvcr |= 1
		var q int32
		if dividend >= 0 {
			q = math.MaxInt32
		} else {
			q = math.MinInt32
		}
		rem := int32(dividend >> 29)
		d.dvdnt = uint32(q)
		d.dvdntl = uint32(q)
		d.dvdnth = uint32(rem)
		return
	}
	// One documented special case: the most negative 32 bit dividend
	// divided by negative one is not overflow on this unit. It yields
	// the most negative value as the quotient and zero as remainder.
	if dividend == int64(math.MinInt32) && divisor == -1 {
		d.dvdnt = 0x80000000
		d.dvdntl = d.dvdnt
		d.dvdnth = 0
		return
	}
	q64 := dividend / int64(divisor)
	if q64 > int64(math.MaxInt32) || q64 < int64(math.MinInt32) {
		d.dvcr |= 1
		var q int32
		if q64 > 0 {
			q = math.MaxInt32
		} else {
			q = math.MinInt32
		}
		// Stated choice for this slice: the remainder on quotient
		// overflow is computed from the saturated quotient and then
		// truncated to 32 bits.
		rem := int32(dividend - int64(q)*int64(divisor))
		d.dvdnt = uint32(q)
		d.dvdntl = uint32(q)
		d.dvdnth = uint32(rem)
		return
	}
	q := int32(q64)
	rem := int32(dividend - q64*int64(divisor))
	d.dvdnt = uint32(q)
	d.dvdntl = uint32(q)
	d.dvdnth = uint32(rem)
}

// write32 stores a full word in the divide file, triggering a
// division for the two documented trigger offsets.
func (d *divUnit) write32(off uint32, v uint32) {
	switch off {
	case divOffDVDNT:
		// The written value sign extends to a 64 bit dividend.
		d.runDivide(int64(int32(v)))
	case divOffDVDNTL:
		// The stored high word and the written low word form the
		// 64 bit dividend.
		bits := uint64(d.dvdnth)<<32 | uint64(v)
		d.runDivide(int64(bits))
	default:
		d.assign(off, v)
	}
}

// Bus is the shared Saturn memory bus. The zero value is not usable;
// build one with New.
type Bus struct {
	workLow  []byte
	workHigh []byte
	backup   []byte
	smpc     [128]byte
	cdRegs   [64]byte
	soundRAM []byte
	scspRegs []byte
	vdp1VRAM []byte
	vdp1FB   []byte
	vdp1Regs [128]byte
	vdp2VRAM []byte
	vdp2CRAM []byte
	vdp2Regs [512]byte
	scuRegs  [256]byte
	onChip   [2][onChipSize]byte
	div      [2]divUnit
	capture  [2]bool
	current  int
}

// New returns a bus with all storage zeroed.
func New() *Bus {
	return &Bus{
		workLow:  make([]byte, 1<<20),
		workHigh: make([]byte, 1<<20),
		backup:   make([]byte, 32<<10),
		soundRAM: make([]byte, 512<<10),
		scspRegs: make([]byte, 4<<10),
		vdp1VRAM: make([]byte, 512<<10),
		vdp1FB:   make([]byte, 256<<10),
		vdp2VRAM: make([]byte, 512<<10),
		vdp2CRAM: make([]byte, 4<<10),
	}
}

// SetCurrentCPU selects whose on-chip register window answers in the
// on-chip range: 0 for the master, 1 for the slave. Other values are
// ignored so a bad selector cannot corrupt the current choice.
func (b *Bus) SetCurrentCPU(id int) {
	if id == 0 || id == 1 {
		b.current = id
	}
}

// WorkRAMLow returns the 1 MiB Work RAM-L backing store, for save
// states. Writes through the slice are visible through the bus.
func (b *Bus) WorkRAMLow() []byte { return b.workLow }

// WorkRAMHigh returns the 1 MiB Work RAM-H backing store.
func (b *Bus) WorkRAMHigh() []byte { return b.workHigh }

// BackupRAM returns the 32 KiB logical backup data store. This is the
// packed data, not the odd byte window layout seen on the bus.
func (b *Bus) BackupRAM() []byte { return b.backup }

// CaptureEvent reports whether the given CPU has a pending FRT
// capture flag raised by a 16 bit write to the other CPU MINIT or
// SINIT window. Reading a pending flag clears it, so a true result
// is delivered exactly once.
func (b *Bus) CaptureEvent(cpu int) bool {
	if cpu != 0 && cpu != 1 {
		return false
	}
	if !b.capture[cpu] {
		return false
	}
	b.capture[cpu] = false
	return true
}

// Divide32 divides dividend by the stored divisor of the given CPU
// divide unit, through the same code path as a DVDNT trigger write.
// It returns the resulting quotient and remainder.
func (b *Bus) Divide32(cpu int, dividend int32) (quotient, remainder int32) {
	if cpu != 0 && cpu != 1 {
		return 0, 0
	}
	d := &b.div[cpu]
	d.write32(divOffDVDNT, uint32(dividend))
	return int32(d.dvdnt), int32(d.dvdnth)
}

// Divide64 divides a 64 bit dividend by the stored divisor of the
// given CPU divide unit, through the same code path as a DVDNTL
// trigger write. It returns the resulting quotient and remainder.
func (b *Bus) Divide64(cpu int, dividend int64) (quotient, remainder int32) {
	if cpu != 0 && cpu != 1 {
		return 0, 0
	}
	d := &b.div[cpu]
	d.dvdnth = uint32(uint64(dividend) >> 32)
	d.write32(divOffDVDNTL, uint32(uint64(dividend)&0xFFFFFFFF))
	return int32(d.dvdnt), int32(d.dvdnth)
}

// isOnChip reports whether a masked address falls in the on-chip
// register window. Checking the masked form first means the original
// 0xFFFFFE00 range and every cache alias of it land here before the
// external decode below can claim the address.
func isOnChip(masked uint32) bool {
	return masked >= onChipBase && masked <= onChipTop
}

// divByte reads one big endian lane byte of a divide register word.
func (b *Bus) divByte(off uint32) uint8 {
	base := off &^ uint32(3)
	lane := off & uint32(3)
	word := b.div[b.current].regValue(base)
	shift := (3 - lane) * 8
	return uint8(word >> shift)
}

// writeDivByte composes one byte into a divide register word without
// triggering a division. Partial writes update storage only; only a
// full 32 bit write to a trigger offset starts a division.
func (b *Bus) writeDivByte(off uint32, v uint8) {
	base := off &^ uint32(3)
	lane := off & uint32(3)
	d := &b.div[b.current]
	word := d.regValue(base)
	shift := (3 - lane) * 8
	word &^= uint32(0xFF) << shift
	word |= uint32(v) << shift
	d.assign(base, word)
}

// read8At reads one byte at an already masked address.
func (b *Bus) read8At(masked uint32) uint8 {
	if isOnChip(masked) {
		off := masked - onChipBase
		if off >= divFileStart && off < divFileEnd {
			return b.divByte(off)
		}
		return b.onChip[b.current][off]
	}
	switch {
	case masked < 0x00100000:
		// BIOS window: HLE stub, reads return zero.
		return 0
	case masked < 0x00180000:
		off := masked - 0x00100000
		if off&1 == 0 {
			return 0xFF
		}
		return b.smpc[off&0x7F]
	case masked < 0x00200000:
		off := masked - 0x00180000
		if off&1 == 0 {
			return 0xFF
		}
		return b.backup[(off>>1)&0x7FFF]
	case masked < 0x00300000:
		return b.workLow[masked-0x00200000]
	case masked < 0x01000000:
		return 0
	case masked < 0x02000000:
		// MINIT and SINIT windows read as all ones.
		return 0xFF
	case masked < 0x05000000:
		// A-bus CS0, CS1: open value zero.
		return 0
	case masked < 0x05800000:
		// A-bus dummy area.
		return 0
	case masked < 0x05900000:
		return b.cdRegs[(masked-0x05800000)&0x3F]
	case masked < 0x05A00000:
		return 0
	case masked < 0x05B00000:
		return b.soundRAM[(masked-0x05A00000)&0x7FFFF]
	case masked < 0x05C00000:
		return b.scspRegs[(masked-0x05B00000)&0xFFF]
	case masked < 0x05C80000:
		return b.vdp1VRAM[masked-0x05C00000]
	case masked < 0x05D00000:
		return b.vdp1FB[(masked-0x05C80000)&0x3FFFF]
	case masked < 0x05D80000:
		return b.vdp1Regs[(masked-0x05D00000)&0x7F]
	case masked < 0x05E00000:
		return 0
	case masked < 0x05F00000:
		return b.vdp2VRAM[(masked-0x05E00000)&0x7FFFF]
	case masked < 0x05F80000:
		return b.vdp2CRAM[(masked-0x05F00000)&0xFFF]
	case masked < 0x05FC0000:
		return b.vdp2Regs[(masked-0x05F80000)&0x1FF]
	case masked < 0x05FE0000:
		return 0
	case masked < 0x05FF0000:
		return b.scuRegs[(masked-0x05FE0000)&0xFF]
	case masked < 0x06000000:
		return 0
	case masked < 0x08000000:
		return b.workHigh[(masked-0x06000000)&0xFFFFF]
	}
	return 0
}

// write8At writes one byte at an already masked address.
func (b *Bus) write8At(masked uint32, v uint8) {
	if isOnChip(masked) {
		off := masked - onChipBase
		if off >= divFileStart && off < divFileEnd {
			b.writeDivByte(off, v)
			return
		}
		b.onChip[b.current][off] = v
		return
	}
	switch {
	case masked < 0x00100000:
		// BIOS window: writes ignored.
	case masked < 0x00180000:
		off := masked - 0x00100000
		if off&1 == 1 {
			b.smpc[off&0x7F] = v
		}
	case masked < 0x00200000:
		off := masked - 0x00180000
		// A write at any offset lands in the logical cell. Writing
		// an even byte is the same as writing its odd neighbour.
		b.backup[(off>>1)&0x7FFF] = v
	case masked < 0x00300000:
		b.workLow[masked-0x00200000] = v
	case masked < 0x05000000:
		// MINIT, SINIT and cartridge windows: byte writes have no
		// effect. In particular a byte write never raises a capture
		// flag; only an exact 16 bit write does.
	case masked < 0x05800000:
		// A-bus dummy area: writes ignored.
	case masked < 0x05900000:
		b.cdRegs[(masked-0x05800000)&0x3F] = v
	case masked < 0x05A00000:
		// Unmapped gap: writes ignored.
	case masked < 0x05B00000:
		b.soundRAM[(masked-0x05A00000)&0x7FFFF] = v
	case masked < 0x05C00000:
		b.scspRegs[(masked-0x05B00000)&0xFFF] = v
	case masked < 0x05C80000:
		b.vdp1VRAM[masked-0x05C00000] = v
	case masked < 0x05D00000:
		b.vdp1FB[(masked-0x05C80000)&0x3FFFF] = v
	case masked < 0x05D80000:
		b.vdp1Regs[(masked-0x05D00000)&0x7F] = v
	case masked < 0x05E00000:
		// Unmapped gap: writes ignored.
	case masked < 0x05F00000:
		b.vdp2VRAM[(masked-0x05E00000)&0x7FFFF] = v
	case masked < 0x05F80000:
		b.vdp2CRAM[(masked-0x05F00000)&0xFFF] = v
	case masked < 0x05FC0000:
		b.vdp2Regs[(masked-0x05F80000)&0x1FF] = v
	case masked < 0x05FE0000:
		// Unmapped gap: writes ignored.
	case masked < 0x05FF0000:
		b.scuRegs[(masked-0x05FE0000)&0xFF] = v
	case masked < 0x06000000:
		// Unmapped gap: writes ignored.
	case masked < 0x08000000:
		b.workHigh[(masked-0x06000000)&0xFFFFF] = v
	}
}

// Read8 reads one byte. The address is masked to the external decode
// space first, so cache and cache through aliases land on the same
// storage as their plain forms.
func (b *Bus) Read8(addr uint32) uint8 {
	return b.read8At(addr & maskAddr)
}

// Read16 reads one big endian 16 bit word, composed of byte reads.
func (b *Bus) Read16(addr uint32) uint16 {
	hi := uint16(b.Read8(addr))
	lo := uint16(b.Read8(addr + 1))
	return hi<<8 | lo
}

// Read32 reads one big endian 32 bit word, composed of byte reads.
func (b *Bus) Read32(addr uint32) uint32 {
	b0 := uint32(b.Read8(addr))
	b1 := uint32(b.Read8(addr + 1))
	b2 := uint32(b.Read8(addr + 2))
	b3 := uint32(b.Read8(addr + 3))
	return b0<<24 | b1<<16 | b2<<8 | b3
}

// Write8 writes one byte.
func (b *Bus) Write8(addr uint32, v uint8) {
	b.write8At(addr&maskAddr, v)
}

// Write16 writes one big endian 16 bit word. A write of exactly this
// size to the MINIT window raises the slave capture flag, and one to
// the SINIT window raises the master capture flag. Everywhere else
// the write is composed of two byte writes.
func (b *Bus) Write16(addr uint32, v uint16) {
	masked := addr & maskAddr
	if !isOnChip(masked) {
		if masked >= 0x01000000 && masked < 0x01800000 {
			b.capture[1] = true
			return
		}
		if masked >= 0x01800000 && masked < 0x02000000 {
			b.capture[0] = true
			return
		}
	}
	b.write8At(masked, uint8(v>>8))
	b.write8At((addr+1)&maskAddr, uint8(v))
}

// Write32 writes one big endian 32 bit word. A full word write to a
// divide unit trigger register starts a division; a full word write
// anywhere else is composed of byte writes. A 32 bit write to the
// MINIT or SINIT window does not raise a capture flag.
func (b *Bus) Write32(addr uint32, v uint32) {
	masked := addr & maskAddr
	if isOnChip(masked) {
		off := masked - onChipBase
		if off >= divFileStart && off < divFileEnd && off&3 == 0 {
			b.div[b.current].write32(off, v)
			return
		}
	}
	b.write8At(masked, uint8(v>>24))
	b.write8At((addr+1)&maskAddr, uint8(v>>16))
	b.write8At((addr+2)&maskAddr, uint8(v>>8))
	b.write8At((addr+3)&maskAddr, uint8(v))
}
