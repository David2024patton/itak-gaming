// Package bus implements the Game Boy Advance memory map
// for GBA-1, written from the published GBATEK memory map.
//
// The map, by the top byte of the address:
//
//	0x00000000  BIOS, 16 KB window (read only; the backing
//	            store stays zero filled in this core, see
//	            the README honesty notes)
//	0x02000000  EWRAM, 256 KB, mirrored over its 16 MB slot
//	0x03000000  IWRAM, 32 KB, mirrored over its 16 MB slot
//	0x04000000  IO registers, 1 KB plain store in this slice
//	            (WAITCNT at 0x204 included; no register has
//	            behavior yet, and wait states are not timed)
//	0x05000000  Palette RAM, 1 KB, mirrored over its slot
//	0x06000000  VRAM, 96 KB, in a 128 KB window: the window
//	            offset is masked with 0x17FFF, so the top
//	            32 KB of the window mirrors the 32 KB below
//	            it, and the window repeats over the slot
//	0x07000000  OAM, 1 KB, mirrored over its slot
//	0x08000000  GamePak ROM, wait state 0 window (32 MB)
//	0x0A000000  GamePak ROM, wait state 1 window (32 MB)
//	0x0C000000  GamePak ROM, wait state 2 window (32 MB)
//	0x0E000000  GamePak SRAM, 64 KB, byte wide (0x0F000000
//	            mirrors the same SRAM, as on hardware)
//
// Everything else is unmapped. Unmapped reads return the
// open bus value and unmapped writes are ignored.
//
// Open bus model: the bus remembers the last value a read
// or a fetch drove onto it, and an unmapped read returns
// that value unchanged. That is an approximation of the
// real hardware, where an open read returns whatever the
// bus capacitance last held (in practice the prefetched
// instruction word for code running in other regions);
// last-value tracking is the documented GBA-1 model.
//
// Width rules at the bus level: Read returns the raw
// aligned unit, with no rotation: Read(addr, 4) returns
// the aligned word at addr&^3, Read(addr, 2) returns the
// aligned halfword at addr&^1 zero extended, and
// Read(addr, 1) returns the byte at addr. The GBATEK
// misaligned load rotation lives in the CPU, which is
// also what the conformance corpus expects: its recorded
// bus transactions carry the raw units. GamePak SRAM is
// byte wide, so wide SRAM reads return the addressed
// byte replicated across all lanes and half reads return
// it in both bytes; SRAM writes use only the low byte
// regardless of size. Writes to normal regions force the
// address to the access alignment: Write(addr, 4) stores
// the full word at addr&^3 and Write(addr, 2) stores the
// low halfword at addr&^1.
package bus

import (
	"github.com/davidpatton/itak-gaming/cores/go-gba/chips/arm7tdmi"
)

// Region sizes in bytes.
const (
	BIOSSize    = 0x4000  // 16 KB
	EWRAMSize   = 0x40000 // 256 KB
	IWRAMSize   = 0x8000  // 32 KB
	IOSize      = 0x400   // 1 KB
	PaletteSize = 0x400   // 1 KB
	VRAMSize    = 0x18000 // 96 KB
	OAMSize     = 0x400   // 1 KB
	SRAMSize    = 0x10000 // 64 KB
)

// Bus is the GBA system bus. The backing stores are
// exported so the system package can serialize them and
// tools can inspect them; ROM is the cartridge image,
// set through SetROM.
type Bus struct {
	BIOS    [BIOSSize]byte
	EWRAM   [EWRAMSize]byte
	IWRAM   [IWRAMSize]byte
	IO      [IOSize]byte
	Palette [PaletteSize]byte
	VRAM    [VRAMSize]byte
	OAM     [OAMSize]byte
	SRAM    [SRAMSize]byte

	ROM []byte

	// IORead and IOWrite, when set by the system, give IO
	// registers behavior: they are consulted for accesses
	// inside the 1 KB IO window before the plain store is.
	// The offset is relative to 0x04000000. A handler that
	// reports false leaves the access to the plain store
	// (which is where behavior-less registers such as
	// WAITCNT continue to live).
	IORead  func(off uint32, size int) (uint32, bool)
	IOWrite func(off uint32, size int, val uint32) bool

	// last is the open bus value: the last value a read
	// or fetch drove onto the bus.
	last uint32
}

// Bus must satisfy the CPU's memory interface.
var _ arm7tdmi.Bus = (*Bus)(nil)

// New returns a bus with all stores zeroed and no
// cartridge attached.
func New() *Bus { return &Bus{} }

// SetROM attaches a cartridge ROM image to the GamePak
// ROM windows. A nil or empty image detaches it, and
// reads of the ROM windows then return the open bus
// value.
func (b *Bus) SetROM(rom []byte) { b.ROM = rom }

// OpenBus returns the current open bus value (the last
// value a read or fetch drove).
func (b *Bus) OpenBus() uint32 { return b.last }

// isSRAM reports whether addr lands in the GamePak SRAM
// windows (0x0E and its 0x0F mirror).
func isSRAM(addr uint32) bool {
	top := addr >> 24
	return top == 0x0E || top == 0x0F
}

// decode maps an aligned address in a normal (non SRAM)
// region to its backing store and offset. readOnly marks
// stores that ignore writes (the BIOS and the ROM), and
// isROM marks the GamePak windows, whose reads wrap
// inside the image instead of indexing a fixed store.
// ok is false for unmapped addresses and for ROM windows
// with no image attached.
func (b *Bus) decode(addr uint32) (store []byte, off uint32, readOnly, isROM, ok bool) {
	switch top := addr >> 24; {
	case top == 0x00:
		if addr < BIOSSize {
			return b.BIOS[:], addr, true, false, true
		}
	case top == 0x02:
		return b.EWRAM[:], addr & (EWRAMSize - 1), false, false, true
	case top == 0x03:
		return b.IWRAM[:], addr & (IWRAMSize - 1), false, false, true
	case top == 0x04:
		if addr&0x00FFFFFF < IOSize {
			return b.IO[:], addr & (IOSize - 1), false, false, true
		}
	case top == 0x05:
		return b.Palette[:], addr & (PaletteSize - 1), false, false, true
	case top == 0x06:
		// The 128 KB window with the upper 32 KB mirrored:
		// masking with 0x17FFF does both at once, since
		// window offsets 0x18000..0x1FFFF fold back onto
		// 0x10000..0x17FFF and higher windows repeat.
		return b.VRAM[:], addr & 0x17FFF, false, false, true
	case top == 0x07:
		return b.OAM[:], addr & (OAMSize - 1), false, false, true
	case top >= 0x08 && top <= 0x0D:
		if len(b.ROM) == 0 {
			return nil, 0, true, true, false
		}
		return b.ROM, addr & 0x01FFFFFF, true, true, true
	}
	return nil, 0, false, false, false
}

// readROM assembles size bytes starting at window offset
// off, wrapping inside the image so an undersized ROM
// mirrors across the whole window.
func (b *Bus) readROM(off uint32, size int) uint32 {
	n := uint32(len(b.ROM))
	var v uint32
	if n&(n-1) == 0 {
		// Power of two image: mirror with a mask.
		for i := 0; i < size; i++ {
			v |= uint32(b.ROM[(off+uint32(i))&(n-1)]) << (8 * i)
		}
		return v
	}
	for i := 0; i < size; i++ {
		v |= uint32(b.ROM[(off+uint32(i))%n]) << (8 * i)
	}
	return v
}

// readAligned reads size bytes from an address already
// aligned down to size, with no rotation applied.
func (b *Bus) readAligned(addr uint32, size int) (uint32, bool) {
	store, off, _, isROM, ok := b.decode(addr)
	if !ok {
		return 0, false
	}
	if isROM {
		return b.readROM(off, size), true
	}
	var v uint32
	for i := 0; i < size; i++ {
		v |= uint32(store[off+uint32(i)]) << (8 * i)
	}
	return v, true
}

// Read implements arm7tdmi.Bus, delivering the raw
// aligned unit (see the package comment for the full
// width contract): a 4-byte read returns the word at
// addr&^3, a 2-byte read returns the halfword at addr&^1
// zero extended, and a 1-byte read returns the byte at
// addr. The CPU applies the GBATEK misaligned rotation
// itself. Any other size returns the open bus value.
func (b *Bus) Read(addr uint32, size int) uint32 {
	switch size {
	case 1, 2, 4:
	default:
		return b.last
	}
	if addr>>24 == 0x04 && addr&0x00FFFFFF < IOSize && b.IORead != nil {
		if v, ok := b.IORead(addr&(IOSize-1), size); ok {
			b.last = v
			return v
		}
	}
	if isSRAM(addr) {
		// Byte wide SRAM: the addressed byte is replicated
		// across the bus lanes, so every width returns the
		// replicated byte (a half read returns it in both
		// bytes).
		v := uint32(b.SRAM[addr&(SRAMSize-1)])
		switch size {
		case 1:
			// v already holds just the byte.
		case 2:
			v |= v << 8
		case 4:
			v |= v << 8
			v |= v << 16
		}
		b.last = v
		return v
	}
	aligned := addr
	switch size {
	case 2:
		aligned &^= 1
	case 4:
		aligned &^= 3
	}
	v, ok := b.readAligned(aligned, size)
	if !ok {
		return b.last
	}
	b.last = v
	return v
}

// Fetch implements arm7tdmi.Bus. It behaves exactly like
// Read; keeping it a separate entry point is what lets
// the open bus value track the instruction stream, which
// is what dominates the real hardware's open bus reads.
func (b *Bus) Fetch(addr uint32, size int) uint32 {
	return b.Read(addr, size)
}

// Write implements arm7tdmi.Bus. Unmapped writes and
// writes to the BIOS and ROM windows are ignored. Wide
// writes to SRAM store only the low byte at the
// addressed byte. Writes do not change the open bus
// value in this model: only reads and fetches drive it.
func (b *Bus) Write(addr uint32, size int, val uint32) {
	switch size {
	case 1, 2, 4:
	default:
		return
	}
	if addr>>24 == 0x04 && addr&0x00FFFFFF < IOSize && b.IOWrite != nil {
		if b.IOWrite(addr&(IOSize-1), size, val) {
			return
		}
	}
	if isSRAM(addr) {
		b.SRAM[addr&(SRAMSize-1)] = byte(val)
		return
	}
	aligned := addr
	if size == 2 {
		aligned &^= 1
	} else if size == 4 {
		aligned &^= 3
	}
	store, off, readOnly, _, ok := b.decode(aligned)
	if !ok || readOnly {
		return
	}
	for i := 0; i < size; i++ {
		store[off+uint32(i)] = byte(val >> (8 * i))
	}
}
