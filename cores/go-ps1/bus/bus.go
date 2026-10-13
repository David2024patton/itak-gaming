// Package bus is the PlayStation memory map for the
// go-ps1 core, written from the psx-spx memory map.
//
// Virtual segments follow the R3000 fixed map: KUSEG
// (0x00000000-0x7FFFFFFF) is physical, KSEG0 and KSEG1
// strip their high bits to the same physical space,
// and KSEG2 holds the cache control register at
// 0xFFFE0130. Physical regions:
//
//   - main RAM: 2 MB at physical 0, mirrored four times
//     across the 8 MB window 0x000000-0x7FFFFF.
//   - scratchpad: 1 KB at 0x1F800000.
//   - hardware registers: 0x1F801000-0x1F802FFF, stored
//     stubs in this slice (peripherals, timers, DMA
//     channel registers, CD controller, GPU and SPU
//     register windows all live here; no register has
//     behavior yet).
//   - expansion regions: reads return all ones, the
//     open-bus value used for unpopulated space.
//   - BIOS window: 512 KB mirrored across
//     0x1FC00000-0x1FFFFFFF. There is no Sony BIOS in
//     this core and none ever will be: reads return
//     zero words (a NOP sled) in this slice, and the
//     project's HLE BIOS behavior arrives in a later
//     slice (see the core README).
//
// The memory control register RAM_SIZE (0x1F801060)
// resets to its documented 2 MB value 0x00000B88. The
// cache control register at 0xFFFE0130 is stored.
package bus

const (
	RAMSize        = 2 * 1024 * 1024
	ScratchpadSize = 1024
	IOSize         = 0x2000 // 0x1F801000-0x1F802FFF

	ramWindow    = 0x00800000 // RAM mirrors span 8 MB
	scratchBase  = 0x1F800000
	ioBase       = 0x1F801000
	exp1Base     = 0x1F000000
	exp1Size     = 0x00800000
	biosBase     = 0x1FC00000
	biosWindow   = 0x00400000
	cacheCtrlAdr = 0xFFFE0130

	ramSizeReset = 0x00000B88 // documented reset value for 2 MB
	ramSizeOff   = 0x60       // offset of RAM_SIZE from 0x1F801000
)

// Bus is the whole physical address space.
type Bus struct {
	ram        [RAMSize]byte
	scratchpad [ScratchpadSize]byte
	io         [IOSize]byte
	cacheCtrl  uint32
}

// New returns a bus in reset state.
func New() *Bus {
	b := &Bus{}
	b.Reset()
	return b
}

// Reset clears RAM, scratchpad and the register page,
// and restores the documented memory-control reset
// values.
func (b *Bus) Reset() {
	b.ram = [RAMSize]byte{}
	b.scratchpad = [ScratchpadSize]byte{}
	b.io = [IOSize]byte{}
	b.cacheCtrl = 0
	b.Write32(ioBase+ramSizeOff, ramSizeReset)
}

// RAM returns the live main RAM store (for save states
// and tools).
func (b *Bus) RAM() []byte { return b.ram[:] }

// Scratchpad returns the live scratchpad store.
func (b *Bus) Scratchpad() []byte { return b.scratchpad[:] }

// CacheControl returns the stored cache control
// register (0xFFFE0130).
func (b *Bus) CacheControl() uint32 { return b.cacheCtrl }

// region identifies a physical region and the offset
// inside it.
type region int

const (
	regionRAM region = iota
	regionScratchpad
	regionIO
	regionBIOS
	regionOpenBus
)

// decode maps a virtual address to its region and the
// byte offset or value source inside it.
func decode(addr uint32) (region, uint32) {
	if addr >= cacheCtrlAdr && addr < cacheCtrlAdr+4 {
		// The cache control word lives in a sentinel
		// slot just past the IO page: offsets IOSize+0..3
		// are its four bytes, little-endian.
		return regionIO, IOSize + (addr - cacheCtrlAdr)
	}
	phys := addr
	switch {
	case addr >= 0x80000000 && addr < 0xA0000000: // KSEG0
		phys = addr - 0x80000000
	case addr >= 0xA0000000 && addr < 0xC0000000: // KSEG1
		phys = addr - 0xA0000000
	case addr >= 0xC0000000: // KSEG2 other than the cache register
		return regionOpenBus, 0
	}
	switch {
	case phys < ramWindow:
		return regionRAM, phys & (RAMSize - 1)
	case phys >= scratchBase && phys < scratchBase+ScratchpadSize:
		return regionScratchpad, phys - scratchBase
	case phys >= ioBase && phys < ioBase+IOSize:
		return regionIO, phys - ioBase
	case phys >= biosBase && phys < biosBase+biosWindow:
		return regionBIOS, phys - biosBase
	}
	return regionOpenBus, phys
}

// Read8 reads one byte from the virtual address.
func (b *Bus) Read8(addr uint32) uint8 {
	reg, off := decode(addr)
	switch reg {
	case regionRAM:
		return b.ram[off]
	case regionScratchpad:
		return b.scratchpad[off]
	case regionIO:
		if off >= IOSize {
			return uint8(b.cacheCtrl >> (8 * (off - IOSize)))
		}
		return b.io[off]
	case regionBIOS:
		// HLE stub pattern: zero bytes. Zero words are
		// NOPs, so a CPU reset into the window slides
		// harmlessly until the HLE slice lands.
		return 0
	}
	return 0xFF // expansion and unmapped space read as open bus
}

// Read16 reads a little-endian halfword.
func (b *Bus) Read16(addr uint32) uint16 {
	return uint16(b.Read8(addr)) | uint16(b.Read8(addr+1))<<8
}

// Read32 reads a little-endian word.
func (b *Bus) Read32(addr uint32) uint32 {
	return uint32(b.Read8(addr)) | uint32(b.Read8(addr+1))<<8 |
		uint32(b.Read8(addr+2))<<16 | uint32(b.Read8(addr+3))<<24
}

// Write8 writes one byte. BIOS and expansion writes
// are dropped, as on hardware with nothing there.
func (b *Bus) Write8(addr uint32, v uint8) {
	reg, off := decode(addr)
	switch reg {
	case regionRAM:
		b.ram[off] = v
	case regionScratchpad:
		b.scratchpad[off] = v
	case regionIO:
		if off >= IOSize {
			shift := 8 * (off - IOSize)
			b.cacheCtrl = (b.cacheCtrl &^ (0xFF << shift)) | uint32(v)<<shift
		} else {
			b.io[off] = v
		}
	}
}

// Write16 writes a little-endian halfword.
func (b *Bus) Write16(addr uint32, v uint16) {
	b.Write8(addr, uint8(v))
	b.Write8(addr+1, uint8(v>>8))
}

// Write32 writes a little-endian word.
func (b *Bus) Write32(addr uint32, v uint32) {
	b.Write8(addr, uint8(v))
	b.Write8(addr+1, uint8(v>>8))
	b.Write8(addr+2, uint8(v>>16))
	b.Write8(addr+3, uint8(v>>24))
}
