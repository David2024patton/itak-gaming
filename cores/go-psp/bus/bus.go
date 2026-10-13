// Package bus implements the PSP physical memory map behind the virtual
// addresses the Allegrex CPU issues.
//
// The CPU hands this package virtual addresses. Kernel segments are folded
// onto physical addresses the way the hardware does without a TLB lookup:
// KSEG0 (0x80000000) and KSEG1 (0xA0000000) both strip the top three bits,
// and KUSEG addresses below 0x80000000 pass through unchanged. There is no
// TLB emulation in this core, so KUSEG is a direct map; that is a stated
// simplification of the design, not an oversight.
//
// Cached and uncached aliases land on the same backing store, so the bus is
// coherent by construction. The real PSP has a 64-bit external bus; this
// package exposes the documented 32-bit view the CPU core consumes.
package bus

// Memory region sizes in bytes.
const (
	RAMSize        = 32 << 20 // main RAM
	VRAMSize       = 2 << 20  // frame buffer / GE memory
	ScratchpadSize = 16 << 10 // fast on-chip scratchpad
)

// Physical base addresses.
const (
	scratchpadAliasBase = 0x00000000 // scratchpad also decodes at physical zero
	scratchpadBase      = 0x00010000
	vramBase            = 0x04000000
	vramWindowEnd       = 0x04800000 // VRAM mirrors every 2 MB across this window
	ramBase             = 0x08000000
	ioBase              = 0x1C000000 // hardware register pages (stored stubs)
	ioEnd               = 0x20000000
)

// Bus is the PSP memory system. RAM, VRAM and Scratchpad are exported so the
// system package can serialize them and tests can inspect them.
type Bus struct {
	RAM        []byte
	VRAM       []byte
	Scratchpad []byte

	io map[uint32]uint32 // sparse 32-bit stub cells for hardware registers
}

// New returns a zeroed memory system.
func New() *Bus {
	return &Bus{
		RAM:        make([]byte, RAMSize),
		VRAM:       make([]byte, VRAMSize),
		Scratchpad: make([]byte, ScratchpadSize),
		io:         make(map[uint32]uint32),
	}
}

// translate folds a virtual address onto its physical address.
func translate(vaddr uint32) uint32 {
	if vaddr >= 0x80000000 {
		return vaddr & 0x1FFFFFFF
	}
	return vaddr
}

func (b *Bus) readPhys8(p uint32) uint8 {
	switch {
	case p < ScratchpadSize:
		// Scratchpad alias at physical zero.
		return b.Scratchpad[p]
	case p >= scratchpadBase && p < scratchpadBase+ScratchpadSize:
		return b.Scratchpad[p-scratchpadBase]
	case p >= vramBase && p < vramWindowEnd:
		return b.VRAM[(p-vramBase)&(VRAMSize-1)]
	case p >= ramBase && p < ramBase+RAMSize:
		return b.RAM[p-ramBase]
	case p >= ioBase && p < ioEnd:
		cell := b.io[p&^3]
		return uint8(cell >> ((p & 3) * 8))
	default:
		// Unmapped reads return zero.
		return 0
	}
}

func (b *Bus) writePhys8(p uint32, v uint8) {
	switch {
	case p < ScratchpadSize:
		b.Scratchpad[p] = v
	case p >= scratchpadBase && p < scratchpadBase+ScratchpadSize:
		b.Scratchpad[p-scratchpadBase] = v
	case p >= vramBase && p < vramWindowEnd:
		b.VRAM[(p-vramBase)&(VRAMSize-1)] = v
	case p >= ramBase && p < ramBase+RAMSize:
		b.RAM[p-ramBase] = v
	case p >= ioBase && p < ioEnd:
		key := p &^ 3
		cell := b.io[key]
		shift := (p & 3) * 8
		cell &^= 0xFF << shift
		cell |= uint32(v) << shift
		b.io[key] = cell
	default:
		// Unmapped writes are dropped.
	}
}

// Read8 reads one byte from a virtual address.
func (b *Bus) Read8(addr uint32) uint8 {
	return b.readPhys8(translate(addr))
}

// Read16 reads a little-endian halfword from a virtual address.
func (b *Bus) Read16(addr uint32) uint16 {
	lo := uint16(b.Read8(addr))
	hi := uint16(b.Read8(addr + 1))
	return lo | hi<<8
}

// Read32 reads a little-endian word from a virtual address.
func (b *Bus) Read32(addr uint32) uint32 {
	lo := uint32(b.Read16(addr))
	hi := uint32(b.Read16(addr + 2))
	return lo | hi<<16
}

// Write8 writes one byte to a virtual address.
func (b *Bus) Write8(addr uint32, v uint8) {
	b.writePhys8(translate(addr), v)
}

// Write16 writes a little-endian halfword to a virtual address.
func (b *Bus) Write16(addr uint32, v uint16) {
	b.Write8(addr, uint8(v))
	b.Write8(addr+1, uint8(v>>8))
}

// Write32 writes a little-endian word to a virtual address.
func (b *Bus) Write32(addr uint32, v uint32) {
	b.Write16(addr, uint16(v))
	b.Write16(addr+2, uint16(v>>16))
}

// IOCount reports how many hardware register stub cells have been written.
// It exists for diagnostics and tests.
func (b *Bus) IOCount() int {
	return len(b.io)
}
