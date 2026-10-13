// Package bus is the Dreamcast physical memory map for DC-1.
//
// The CPU package hands the bus physical addresses only: it has
// already translated a virtual address through its functional MMU
// (or, with the MMU off, masked the address to its 29-bit physical
// form). Everything here is therefore keyed on the physical map:
//
//	0x00000000-0x001FFFFF  BIOS window (HLE NOP stub, read only)
//	0x00200000-0x002FFFFF  flash ROM, 256 KB mirrored
//	0x005F7000-0x005F70FF  GD-ROM registers (stub storage)
//	0x005F8000-0x005F83FF  PowerVR core registers (stub storage)
//	0x005F8400-0x005F87FF  PowerVR TA registers (stub storage)
//	0x005F9000-0x005FAFFF  PowerVR RAM and ISP stubs (stub storage)
//	0x00600000-0x006FFFFF  modem and expansion area (stub storage)
//	0x00700000-0x00703FFF  AICA registers (stub storage)
//	0x00800000-0x009FFFFF  AICA RAM, 2 MB
//	0x04000000-0x04FFFFFF  VRAM, 8 MB mirrored (32-bit view)
//	0x05000000-0x05FFFFFF  VRAM, 8 MB mirrored (64-bit view)
//	0x0C000000-0x0FFFFFFF  main RAM, 16 MB mirrored
//
// Register stubs are plain stored words this slice: a read returns
// the last value written to that exact aligned address (default 0)
// and no register has behaviour yet. That keeps early CPU and disc
// work testable before the PowerVR, AICA and Maple slices land.
//
// Multi-byte accesses are composed little-endian from individual
// bytes, so unaligned accesses simply work and never trap at the
// bus level; alignment checks, where a device needs them, belong
// to the device slices.
package bus

// Backing store sizes.
const (
	RAMSize   = 16 << 20
	VRAMSize  = 8 << 20
	FlashSize = 256 << 10

	// AICARAMSize is the size of the AICA sound RAM backing store.
	AICARAMSize = 2 << 20
)

// Physical window bases.
const (
	biosBase  = 0x00000000
	biosEnd   = 0x00200000 // exclusive
	flashBase = 0x00200000
	flashEnd  = 0x00300000 // exclusive, 256 KB mirrored across 1 MB
	aicaBase  = 0x00800000
	aicaEnd   = 0x00A00000 // exclusive
	vram32    = 0x04000000
	vram64    = 0x05000000
	vramSpan  = 0x01000000 // each VRAM window spans 16 MB, mirrored
	ramBase   = 0x0C000000
	ramEnd    = 0x10000000 // exclusive, 16 MB mirrored across 64 MB
)

// Bus is the Dreamcast physical address space.
type Bus struct {
	RAM     []uint8
	VRAM    []uint8
	Flash   []uint8
	AICARAM []uint8

	// regs holds the register stub storage: one 32-bit word per
	// aligned address that software has written, keyed by the
	// aligned physical address.
	regs map[uint32]uint32
}

// New returns a powered-on bus with all RAM-like stores zeroed.
func New() *Bus {
	return &Bus{
		RAM:     make([]uint8, RAMSize),
		VRAM:    make([]uint8, VRAMSize),
		Flash:   make([]uint8, FlashSize),
		AICARAM: make([]uint8, AICARAMSize),
		regs:    make(map[uint32]uint32),
	}
}

// isStub reports whether pa falls in one of the register stub
// ranges. Stub ranges read back the last written word and have no
// behaviour in this slice.
func isStub(pa uint32) bool {
	switch {
	case pa >= 0x005F7000 && pa <= 0x005F70FF: // GD-ROM registers
		return true
	case pa >= 0x005F8000 && pa <= 0x005F83FF: // PowerVR core registers
		return true
	case pa >= 0x005F8400 && pa <= 0x005F87FF: // PowerVR TA registers
		return true
	case pa >= 0x005F9000 && pa <= 0x005FAFFF: // PowerVR RAM and ISP stubs
		return true
	case pa >= 0x00600000 && pa <= 0x006FFFFF: // modem and expansion, idle
		return true
	case pa >= 0x00700000 && pa <= 0x00703FFF: // AICA registers
		return true
	}
	return false
}

// backing returns the RAM-like slice and offset for pa, or nil if
// pa is not RAM-like (BIOS, a register stub, or unmapped).
func (b *Bus) backing(pa uint32) ([]uint8, uint32) {
	switch {
	case pa >= flashBase && pa < flashEnd:
		return b.Flash, (pa - flashBase) & (FlashSize - 1)
	case pa >= aicaBase && pa < aicaEnd:
		return b.AICARAM, (pa - aicaBase) & (AICARAMSize - 1)
	case pa >= vram32 && pa < vram32+vramSpan:
		return b.VRAM, (pa - vram32) & (VRAMSize - 1)
	case pa >= vram64 && pa < vram64+vramSpan:
		return b.VRAM, (pa - vram64) & (VRAMSize - 1)
	case pa >= ramBase && pa < ramEnd:
		return b.RAM, (pa - ramBase) & (RAMSize - 1)
	}
	return nil, 0
}

// readByte is the single-byte read primitive every wider read is
// composed from.
func (b *Bus) readByte(pa uint32) uint8 {
	if pa >= biosBase && pa < biosEnd {
		// HLE BIOS stub: an endless NOP sled. The SH-4 NOP is
		// the 16-bit value 0x0009, stored little-endian, so an
		// even byte reads 0x09 and an odd byte reads 0x00.
		if pa&1 == 0 {
			return 0x09
		}
		return 0x00
	}
	if isStub(pa) {
		word := b.regs[pa&^3]
		return uint8(word >> ((pa & 3) * 8))
	}
	if store, off := b.backing(pa); store != nil {
		return store[off]
	}
	return 0
}

// writeByte is the single-byte write primitive every wider write
// is decomposed into.
func (b *Bus) writeByte(pa uint32, v uint8) {
	if pa >= biosBase && pa < biosEnd {
		return // the BIOS window ignores writes
	}
	if isStub(pa) {
		if b.regs == nil {
			b.regs = make(map[uint32]uint32)
		}
		key := pa &^ 3
		shift := (pa & 3) * 8
		word := b.regs[key]
		word &^= 0xFF << shift
		word |= uint32(v) << shift
		b.regs[key] = word
		return
	}
	if store, off := b.backing(pa); store != nil {
		store[off] = v
	}
	// Writes to unmapped space are ignored. Flash writes are
	// plain stores this slice: real flash command cycles are
	// not emulated yet.
}

// Read8 reads one byte at the physical address pa.
func (b *Bus) Read8(pa uint32) uint8 { return b.readByte(pa) }

// Read16 reads a little-endian 16-bit value at pa, composed from
// two byte reads so unaligned addresses work.
func (b *Bus) Read16(pa uint32) uint16 {
	return uint16(b.readByte(pa)) |
		uint16(b.readByte(pa+1))<<8
}

// Read32 reads a little-endian 32-bit value at pa, composed from
// four byte reads so unaligned addresses work.
func (b *Bus) Read32(pa uint32) uint32 {
	return uint32(b.readByte(pa)) |
		uint32(b.readByte(pa+1))<<8 |
		uint32(b.readByte(pa+2))<<16 |
		uint32(b.readByte(pa+3))<<24
}

// Read64 reads a little-endian 64-bit value at pa, composed from
// eight byte reads so unaligned addresses work.
func (b *Bus) Read64(pa uint32) uint64 {
	return uint64(b.Read32(pa)) | uint64(b.Read32(pa+4))<<32
}

// Write8 writes one byte at the physical address pa.
func (b *Bus) Write8(pa uint32, v uint8) { b.writeByte(pa, v) }

// Write16 writes a little-endian 16-bit value at pa as two byte
// writes, so unaligned addresses and stub byte merging both work.
func (b *Bus) Write16(pa uint32, v uint16) {
	b.writeByte(pa, uint8(v))
	b.writeByte(pa+1, uint8(v>>8))
}

// Write32 writes a little-endian 32-bit value at pa as four byte
// writes.
func (b *Bus) Write32(pa uint32, v uint32) {
	b.writeByte(pa, uint8(v))
	b.writeByte(pa+1, uint8(v>>8))
	b.writeByte(pa+2, uint8(v>>16))
	b.writeByte(pa+3, uint8(v>>24))
}

// Write64 writes a little-endian 64-bit value at pa as eight byte
// writes.
func (b *Bus) Write64(pa uint32, v uint64) {
	b.Write32(pa, uint32(v))
	b.Write32(pa+4, uint32(v>>32))
}
