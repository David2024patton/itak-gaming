// Package bus implements the Nintendo DS memory system as two
// CPU-facing views over one set of backing stores. The ARM9 view
// honors the CP15-configured ITCM and DTCM windows; both views
// see the shared WRAM window through the WRAMCNT ownership
// mapping and the cartridge window through the EXMEMCNT
// ownership bit.
//
// Slice 1 simplifications, stated plainly:
//   - The instruction and data caches are NOT simulated. The
//     CP15 cache configuration registers exist as storage in the
//     CPU, but every access goes straight to backing memory.
//   - No DS or GBA BIOS bytes exist anywhere in this module (and
//     none may be added: no dumps). Reads of the BIOS windows
//     return the side's open-bus value, the documented
//     approximation for code that reads unmapped space.
//   - VRAM banks are backing stores mapped at their LCDC
//     addresses. The VRAMCNT bank assignment registers are plain
//     IO storage; the 2D engines in the video package consume
//     the assignments. Banks C and D assigned as ARM7 WRAM
//     (MST 2) appear in the ARM7 view at 0x06000000, as on
//     hardware.
//   - DS-2 device registers (timers, DMA, IPC, interrupts, the
//     card ports) are not plain storage. When the system
//     installs an IOHook, accesses to the IO pages are offered
//     to it first and fall back to storage for the offsets it
//     does not claim. A bare bus (unit tests) has no hook and
//     behaves exactly as slice 1.
//   - The cartridge image is mapped linearly in the
//     0x08000000 window for foundation work. The real DS game
//     card streams through a serial command interface; direct
//     boot staging in the system package loads binaries from the
//     parsed image into RAM and does not execute from this
//     window.
package bus

import "github.com/davidpatton/itak-gaming/cores/go-nds/chips/armcore"

// Memory region sizes.
const (
	MainRAMSize = 4 << 20
	ITCMSize    = 32 << 10
	DTCMSize    = 32 << 10
	SharedSize  = 32 << 10
	ARM7RAMSize = 64 << 10
	VRAMSize    = 0xA4000 // banks A-I at their LCDC offsets
	PaletteSize = 2 << 10 // BG/OBJ palettes for both engines
	OAMSize     = 2 << 10 // 1 KB of OAM per engine
	GBASRAMSize = 64 << 10
	IOSize      = 0x1000 // one side's IO page (the ARM7 page)
	IO9Size     = 0x2000 // ARM9 IO: its page plus the engine B page
)

// IO offsets of the shared registers, within the 0x04000000 page.
const (
	ioEXMEMCNT = 0x204 // 16-bit, in the ARM9 IO page
	ioWRAMCNT  = 0x247 // 8-bit, in the ARM9 IO page
)

// VRAMCNT byte offsets in the ARM9 IO page, one per bank A-I.
// Banks C and D (offsets 0x242 and 0x243) with MST 2 assign the
// bank as ARM7 WRAM, decoded in the ARM7 view.
const (
	ioVRAMCNTC = 0x242
	ioVRAMCNTD = 0x243
)

// IOHook is the device-register dispatcher the system installs
// for DS-2 hardware. ReadIO and WriteIO receive the side, an
// offset in the hook offset space, and the access size in bytes.
// The offset space is the IO page offset (0x0000-0x1FFF on the
// ARM9 side, which includes the engine B page at 0x1000-0x1FFF;
// 0x0000-0x0FFF on the ARM7 side) plus the 0x04100000 window
// remapped to 0x2000-0x2FFF (the IPC FIFO receive port at
// 0x2000 and the card data port at 0x2010). A false return
// means the offset is plain storage and the bus handles the
// access itself.
type IOHook interface {
	ReadIO(nine bool, off uint32, size int) (uint32, bool)
	WriteIO(nine bool, off uint32, size int, val uint32) bool
}

// hookOffset maps an address to the hook offset space, if the
// address belongs to a hooked window for that side.
func hookOffset(nine bool, addr uint32) (uint32, bool) {
	switch {
	case addr >= 0x04000000 && addr < 0x04001000:
		return addr & 0xFFF, true
	case nine && addr >= 0x04001000 && addr < 0x04002000:
		return addr & 0x1FFF, true
	case addr >= 0x04100000 && addr < 0x04101000:
		return 0x2000 + (addr & 0xFFF), true
	}
	return 0, false
}

// Bus holds every DS backing store and hands out the two CPU
// views. Construct with New, then AttachCPU9 so the ARM9 view can
// decode the CP15 TCM configuration.
type Bus struct {
	MainRAM  []byte
	ITCM     []byte
	DTCM     []byte
	Shared   []byte
	ARM7WRAM []byte
	VRAM     []byte
	Palette  []byte
	OAM      []byte
	GBASRAM  []byte
	IO9      []byte
	IO7      []byte

	rom   []byte
	cpu9  *armcore.CPU
	hook  IOHook
	open9 uint32
	open7 uint32
}

// New returns a zeroed DS memory system.
func New() *Bus {
	return &Bus{
		MainRAM:  make([]byte, MainRAMSize),
		ITCM:     make([]byte, ITCMSize),
		DTCM:     make([]byte, DTCMSize),
		Shared:   make([]byte, SharedSize),
		ARM7WRAM: make([]byte, ARM7RAMSize),
		VRAM:     make([]byte, VRAMSize),
		Palette:  make([]byte, PaletteSize),
		OAM:      make([]byte, OAMSize),
		GBASRAM:  make([]byte, GBASRAMSize),
		IO9:      make([]byte, IO9Size),
		IO7:      make([]byte, IOSize),
	}
}

// SetIOHook installs the device-register dispatcher. The system
// calls it once at construction; unit tests leave it nil.
func (b *Bus) SetIOHook(h IOHook) { b.hook = h }

// AttachCPU9 binds the ARM9 CPU whose CP15 TCM registers the
// ARM9 view decodes against.
func (b *Bus) AttachCPU9(c *armcore.CPU) { b.cpu9 = c }

// SetROM installs the cartridge image used by the ROM window.
// The bytes are kept by reference.
func (b *Bus) SetROM(rom []byte) { b.rom = rom }

// ROMImage returns the installed cartridge image.
func (b *Bus) ROMImage() []byte { return b.rom }

// WRAMCNT returns the shared WRAM ownership register (ARM9 IO).
func (b *Bus) WRAMCNT() uint32 { return uint32(b.IO9[ioWRAMCNT]) }

// SetWRAMCNT stores the shared WRAM ownership register.
func (b *Bus) SetWRAMCNT(v uint32) { b.IO9[ioWRAMCNT] = byte(v) }

// EXMEMCNT returns the external memory control register.
func (b *Bus) EXMEMCNT() uint32 {
	return uint32(b.IO9[ioEXMEMCNT]) | uint32(b.IO9[ioEXMEMCNT+1])<<8
}

// cartOwner9 reports whether the ARM9 owns the cartridge window:
// EXMEMCNT bit 7 clear means ARM9, set means ARM7.
func (b *Bus) cartOwner9() bool { return b.EXMEMCNT()&(1<<7) == 0 }

// View is one CPU's window onto the bus.
type View struct {
	b    *Bus
	nine bool
}

// ARM9 returns the ARM9 bus view.
func (b *Bus) ARM9() View { return View{b: b, nine: true} }

// ARM7 returns the ARM7 bus view.
func (b *Bus) ARM7() View { return View{b: b, nine: false} }

// bankOwnedBy9 reports whether shared WRAM bank (0 or 1) belongs
// to the ARM9 under the WRAMCNT mapping: mode 0 both ARM7,
// mode 1 bank 0 ARM7 and bank 1 ARM9, mode 2 bank 0 ARM9 and
// bank 1 ARM7, mode 3 both ARM9.
func bankOwnedBy9(mode, bank uint32) bool {
	switch mode & 3 {
	case 0:
		return false
	case 1:
		return bank == 1
	case 2:
		return bank == 0
	default:
		return true
	}
}

// sharedWindow translates an address in the 0x03000000 shared
// WRAM window to the shared backing store for one CPU side, or
// reports the access unmapped when the bank belongs to the other
// CPU.
func (b *Bus) sharedWindow(nine bool, addr uint32) ([]byte, uint32, bool) {
	win := addr & 0x7FFF
	bank := win >> 14
	if bankOwnedBy9(b.WRAMCNT(), bank) != nine {
		return nil, 0, false
	}
	rank := uint32(0)
	for i := uint32(0); i < bank; i++ {
		if bankOwnedBy9(b.WRAMCNT(), i) == nine {
			rank++
		}
	}
	return b.Shared, rank<<14 | (win & 0x3FFF), true
}

// decode9 resolves an ARM9 address to a backing store and
// offset. ok=false means open bus.
func (b *Bus) decode9(addr uint32, fetch bool) ([]byte, uint32, bool) {
	if b.cpu9 != nil {
		if base, size, en := b.cpu9.DTCMRegion(); en && size > 0 && addr-base < size {
			return b.DTCM, (addr - base) % uint32(len(b.DTCM)), true
		}
		if base, size, en := b.cpu9.ITCMRegion(); en && size > 0 {
			// The ITCM mirrors across 0x00000000-0x01FFFFFF.
			if rel := addr - base; addr < 0x02000000 && rel < 0x02000000 {
				off := rel % size
				if off < uint32(len(b.ITCM)) {
					return b.ITCM, off, true
				}
			}
		}
	}
	switch {
	case addr >= 0x02000000 && addr < 0x03000000:
		return b.MainRAM, addr & (MainRAMSize - 1), true
	case addr >= 0x03000000 && addr < 0x03008000:
		return b.sharedWindow(true, addr)
	case addr >= 0x04000000 && addr < 0x04002000:
		return b.IO9, addr & 0x1FFF, true
	case addr >= 0x05000000 && addr < 0x05001000:
		return b.Palette, addr & (PaletteSize - 1), true
	case addr >= 0x06800000 && addr < 0x06800000+VRAMSize:
		return b.VRAM, addr - 0x06800000, true
	case addr >= 0x07000000 && addr < 0x07001000:
		return b.OAM, addr & (OAMSize - 1), true
	case addr >= 0x08000000 && addr < 0x0A000000:
		if b.cartOwner9() && len(b.rom) > 0 {
			return b.rom, (addr - 0x08000000) % uint32(len(b.rom)), true
		}
	case addr >= 0x0A000000 && addr < 0x0B000000:
		if b.cartOwner9() {
			return b.GBASRAM, addr & (GBASRAMSize - 1), true
		}
	}
	return nil, 0, false
}

// decode7 resolves an ARM7 address to a backing store and
// offset. ok=false means open bus (which covers its 16 KB BIOS
// window at 0x00000000: no BIOS bytes exist in this module).
func (b *Bus) decode7(addr uint32) ([]byte, uint32, bool) {
	switch {
	case addr >= 0x02000000 && addr < 0x03000000:
		return b.MainRAM, addr & (MainRAMSize - 1), true
	case addr >= 0x03000000 && addr < 0x03008000:
		return b.sharedWindow(false, addr)
	case addr >= 0x03800000 && addr < 0x04000000:
		return b.ARM7WRAM, addr & (ARM7RAMSize - 1), true
	case addr >= 0x06000000 && addr < 0x06040000:
		// VRAM banks C and D assigned as ARM7 WRAM (VRAMCNT
		// MST 2) appear here: bank C or D at 0x06000000 plus
		// 0x20000 times its OFS low bit.
		for i, cntOff := range []int{ioVRAMCNTC, ioVRAMCNTD} {
			cnt := uint32(b.IO9[cntOff])
			if cnt&0x80 != 0 && cnt&7 == 2 && uint32(addr-0x06000000)>>17 == (cnt>>3)&1 {
				return b.VRAMBank(2 + i), addr & 0x1FFFF, true
			}
		}
		return nil, 0, false
	case addr >= 0x04000000 && addr < 0x04001000:
		// The ARM7 reads EXMEMCNT and WRAMCNT from the ARM9 page;
		// its writes to them are ignored.
		if off := addr & 0xFFF; off == ioEXMEMCNT || off == ioEXMEMCNT+1 || off == ioWRAMCNT {
			return b.IO9, off, true
		}
		return b.IO7, addr & 0xFFF, true
	case addr >= 0x08000000 && addr < 0x0A000000:
		if !b.cartOwner9() && len(b.rom) > 0 {
			return b.rom, (addr - 0x08000000) % uint32(len(b.rom)), true
		}
	case addr >= 0x0A000000 && addr < 0x0B000000:
		if !b.cartOwner9() {
			return b.GBASRAM, addr & (GBASRAMSize - 1), true
		}
	}
	return nil, 0, false
}

func readBytes(mem []byte, off uint32, size int) uint32 {
	var v uint32
	for i := 0; i < size; i++ {
		v |= uint32(mem[(off+uint32(i))%uint32(len(mem))]) << (8 * i)
	}
	return v
}

func writeBytes(mem []byte, off uint32, size int, val uint32) {
	for i := 0; i < size; i++ {
		mem[(off+uint32(i))%uint32(len(mem))] = byte(val >> (8 * i))
	}
}

func (v View) open() *uint32 {
	if v.nine {
		return &v.b.open9
	}
	return &v.b.open7
}

// Read implements armcore.Bus for this side.
func (v View) Read(addr uint32, size int) uint32 {
	if v.b.hook != nil {
		if off, in := hookOffset(v.nine, addr); in {
			if val, handled := v.b.hook.ReadIO(v.nine, off, size); handled {
				*v.open() = val
				return val
			}
		}
	}
	var mem []byte
	var off uint32
	var ok bool
	if v.nine {
		mem, off, ok = v.b.decode9(addr, false)
	} else {
		mem, off, ok = v.b.decode7(addr)
	}
	if !ok {
		o := *v.open()
		switch size {
		case 1:
			return o & 0xFF
		case 2:
			return o & 0xFFFF
		}
		return o
	}
	val := readBytes(mem, off, size)
	*v.open() = val
	return val
}

// Fetch implements armcore.Bus for this side. Fetches see the
// same map as reads on the ARM9 side (ITCM first), which is the
// slice 1 model.
func (v View) Fetch(addr uint32, size int) uint32 {
	if !v.nine {
		return v.Read(addr, size)
	}
	mem, off, ok := v.b.decode9(addr, true)
	if !ok {
		return v.Read(addr, size)
	}
	val := readBytes(mem, off, size)
	v.b.open9 = val
	return val
}

// Write implements armcore.Bus for this side. ARM7 writes to the
// shared control registers are dropped (decode7 routes their
// reads to the ARM9 page, so writes must not land there).
func (v View) Write(addr uint32, size int, val uint32) {
	if !v.nine && addr >= 0x04000000 && addr < 0x04001000 {
		if off := addr & 0xFFF; off >= ioEXMEMCNT && off <= ioEXMEMCNT+1 || off == ioWRAMCNT {
			return
		}
	}
	if v.b.hook != nil {
		if off, in := hookOffset(v.nine, addr); in {
			if v.b.hook.WriteIO(v.nine, off, size, val) {
				*v.open() = val
				return
			}
		}
	}
	var mem []byte
	var off uint32
	var ok bool
	if v.nine {
		mem, off, ok = v.b.decode9(addr, false)
	} else {
		mem, off, ok = v.b.decode7(addr)
	}
	if !ok {
		return
	}
	writeBytes(mem, off, size, val)
	*v.open() = val
}

// LoadBytes copies data into the memory map through one side's
// view (used by cartridge staging, which writes into windows the
// owning CPU will read). It returns how many bytes were placed.
func (b *Bus) LoadBytes(nine bool, addr uint32, data []byte) int {
	n := 0
	for i, x := range data {
		var mem []byte
		var off uint32
		var ok bool
		if nine {
			mem, off, ok = b.decode9(addr+uint32(i), false)
		} else {
			mem, off, ok = b.decode7(addr + uint32(i))
		}
		if !ok {
			continue
		}
		mem[off] = x
		n++
	}
	return n
}

// VRAMBank returns the backing store of one VRAM bank (0=A ..
// 8=I) at its LCDC offset within the flat VRAM array.
func (b *Bus) VRAMBank(i int) []byte {
	offs := []struct{ at, size int }{
		{0x00000, 128 << 10}, // A
		{0x20000, 128 << 10}, // B
		{0x40000, 128 << 10}, // C
		{0x60000, 128 << 10}, // D
		{0x80000, 64 << 10},  // E
		{0x90000, 16 << 10},  // F
		{0x94000, 16 << 10},  // G
		{0x98000, 32 << 10},  // H
		{0xA0000, 16 << 10},  // I
	}
	if i < 0 || i >= len(offs) {
		return nil
	}
	o := offs[i]
	return b.VRAM[o.at : o.at+o.size]
}

// StateSize is the serialized size of the whole memory system.
const StateSize = MainRAMSize + ITCMSize + DTCMSize + SharedSize + ARM7RAMSize +
	VRAMSize + PaletteSize + OAMSize + GBASRAMSize + IO9Size + IOSize + 8

// Serialize writes every backing store, plus the two open-bus
// values, to dst (at least StateSize bytes).
func (b *Bus) Serialize(dst []byte) int {
	n := 0
	put := func(src []byte) {
		copy(dst[n:], src)
		n += len(src)
	}
	put(b.MainRAM)
	put(b.ITCM)
	put(b.DTCM)
	put(b.Shared)
	put(b.ARM7WRAM)
	put(b.VRAM)
	put(b.Palette)
	put(b.OAM)
	put(b.GBASRAM)
	put(b.IO9)
	put(b.IO7)
	for _, v := range []uint32{b.open9, b.open7} {
		dst[n] = byte(v)
		dst[n+1] = byte(v >> 8)
		dst[n+2] = byte(v >> 16)
		dst[n+3] = byte(v >> 24)
		n += 4
	}
	return n
}

// Deserialize restores a state written by Serialize.
func (b *Bus) Deserialize(src []byte) bool {
	if len(src) < StateSize {
		return false
	}
	n := 0
	get := func(dst []byte) {
		copy(dst, src[n:n+len(dst)])
		n += len(dst)
	}
	get(b.MainRAM)
	get(b.ITCM)
	get(b.DTCM)
	get(b.Shared)
	get(b.ARM7WRAM)
	get(b.VRAM)
	get(b.Palette)
	get(b.OAM)
	get(b.GBASRAM)
	get(b.IO9)
	get(b.IO7)
	rd := func() uint32 {
		v := uint32(src[n]) | uint32(src[n+1])<<8 | uint32(src[n+2])<<16 | uint32(src[n+3])<<24
		n += 4
		return v
	}
	b.open9 = rd()
	b.open7 = rd()
	return true
}
