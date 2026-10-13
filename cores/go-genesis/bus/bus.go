// Package bus wires the Genesis address maps for both CPUs.
//
// 68k map (24-bit):
//
//	0x000000-0x3FFFFF cartridge ROM (bank SRAM window at 0x200000+ when the
//	                  cart has SRAM and ROM is switched out via 0xA130F1)
//	0xA00000-0xA07FFF Z80 address space window
//	0xA04000-0xA04003 YM2612
//	0xA06000          bank register mirror (Z80 view handles the real one)
//	0xA10000-0xA1001F I/O: version, controller data/control, serial
//	0xA12000-0xA120FF Sega CD gate array, only when a unit is attached via AttachCD
//	0xA11100          Z80 bus request (0x0100 = grant/hold, 0x0000 = release)
//	0xA11200          Z80 reset (0x0000 = assert, 0x0100 = release)
//	0xA130F1          SRAM enable / ROM bank switch
//	0xA14000-0xA14003 TMSS register (accepted, ignored)
//	0xC00000-0xDFFFFF VDP ports (mirrored every 0x20) + PSG at 0xC00011
//	0xE00000-0xFFFFFF 64KB work RAM (mirrored)
//
// Z80 map (64KB):
//
//	0x0000-0x1FFF its own 8KB RAM
//	0x4000-0x4003 YM2612
//	0x6000        bank register (9 bits shifted in, selects the 32KB window)
//	0x7F11        PSG
//	0x8000-0xFFFF window into 68k space through the bank register
//
// Pad button bits for SetPad (internal layout, libretro translates):
//
//	Up=1<<0 Down=1<<1 Left=1<<2 Right=1<<3 B=1<<4 C=1<<5 A=1<<6 Start=1<<7 Mode=1<<8
//
// PINNED API (system and libretro are written against this):
//
//	New(c *cart.Cart, v *vdp.VDP, s *sound.Sound) *Bus
//	m68k.Bus: Read8/16/32, Write8/16/32, Reset()
//	z80.Bus: Fetch, Read, Write, In, Out
//	SetPad(player int, mask uint16)
//	Z80Halted() bool     bus requested or reset asserted
//	TakeZ80Reset() bool  true once after reset is released (system resets the Z80)
//	AttachCD(u *scd.Unit) / CDAttached() bool  Sega CD gate array window
//	SerializeSize() int / Serialize / Deserialize (RAM, Z80 RAM, SRAM, IO state)
//
// SetYMClock(fn func(fromZ80 bool) int64) was added after the
// original pin: the system installs an emulated-time source (YM2612
// master clocks since power-on) that timestamps YM2612 writes, so
// the sound section can render the pending FM span before each
// write lands (see writeYM). Without one, writes behave exactly
// as before.
package bus

import (
	"encoding/binary"
	"errors"

	"github.com/davidpatton/itak-gaming/cores/go-genesis/cart"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/scd"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/sound"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/vdp"
)

// Pad button bits.
const (
	PadUp    = 1 << 0
	PadDown  = 1 << 1
	PadLeft  = 1 << 2
	PadRight = 1 << 3
	PadB     = 1 << 4
	PadC     = 1 << 5
	PadA     = 1 << 6
	PadStart = 1 << 7
	PadMode  = 1 << 8
)

// Bus is the shared address space both CPUs see.
type Bus struct {
	cart  *cart.Cart
	vdp   *vdp.VDP
	sound *sound.Sound
	cd    *scd.Unit

	RAM  [0x10000]uint8 // 68k work RAM
	ZRAM [0x2000]uint8  // Z80 RAM
	SRAM []uint8        // cartridge save RAM (sized from the cart header)

	pads          [2]uint16                // current button state per player (Pad* bits)
	ioData        [3]uint8                 // last values written to the data ports
	ioCtrl        [3]uint8                 // control (direction/TH select) per port
	z80Req        bool                     // 68k holds the Z80 bus (Z80 halted)
	z80Rst        bool                     // Z80 reset asserted
	z80RstPending bool                     // reset was released; system should reset the Z80
	bank          uint32                   // Z80 window bank (9 bits, in 32KB units >> 15)
	sramOn        bool                     // SRAM mapped at 0x200000 instead of ROM
	ymClockFn     func(fromZ80 bool) int64 // emulated time for YM writes, nil = untimestamped
}

// New builds the bus around a cartridge, VDP and sound chip set.
func New(c *cart.Cart, v *vdp.VDP, s *sound.Sound) *Bus {
	b := &Bus{cart: c, vdp: v, sound: s, z80Rst: true, z80RstPending: true}
	if c != nil && c.SRAM {
		n := c.SRAMEnd - c.SRAMStart + 1
		if n == 0 || n > 0x100000 {
			n = 0x10000
		}
		b.SRAM = make([]uint8, n)
		for i := range b.SRAM {
			b.SRAM[i] = 0xFF
		}
	}
	v.SetDMASource(func(addr uint32) uint16 { return b.Read16(addr) })
	return b
}

// AttachCD attaches a Sega CD unit so its gate array answers at
// 0xA12000-0xA120FF on the 68k map.
func (b *Bus) AttachCD(u *scd.Unit) { b.cd = u }

// CDAttached reports whether a Sega CD unit is attached.
func (b *Bus) CDAttached() bool { return b.cd != nil }

// SetPad records the button state for a player (0 or 1).
func (b *Bus) SetPad(player int, mask uint16) {
	if player >= 0 && player < 2 {
		b.pads[player] = mask
	}
}

// SetYMClock installs the emulated-time source used to timestamp
// YM2612 writes: fn reports the current time in YM2612 master
// clocks since power-on for a write coming from the 68000
// (fromZ80 false) or from the Z80 (fromZ80 true). The bus is a
// wiring layer and owns no clock of its own; the system, which
// owns both CPUs, supplies the source. Passing nil (or never
// calling this) leaves writes untimestamped.
func (b *Bus) SetYMClock(fn func(fromZ80 bool) int64) { b.ymClockFn = fn }

// writeYM passes one YM2612 port write to the sound section. When
// a clock source is installed, the FM stream is first rendered up
// to the write's emulated moment, so the register change takes
// effect at its true position inside the frame: a DAC stream
// writing data hundreds of times per frame is rendered as a
// stream, not as its last byte held to frame end.
func (b *Bus) writeYM(addr uint32, val uint8, fromZ80 bool) {
	if b.ymClockFn != nil {
		b.sound.RenderYMTo(b.ymClockFn(fromZ80))
	}
	b.sound.WriteYM(addr, val)
}

// Z80Halted reports whether the Z80 must not run (bus requested by the
// 68k, or reset asserted).
func (b *Bus) Z80Halted() bool { return b.z80Req || b.z80Rst }

// TakeZ80Reset reports true once each time the Z80 leaves reset, so the
// system can reset the CPU at the right moment.
func (b *Bus) TakeZ80Reset() bool {
	if b.z80RstPending && !b.z80Rst {
		b.z80RstPending = false
		return true
	}
	return false
}

// Reset satisfies the m68k Bus interface (bus reset hook; state is owned
// by New and the serialize path, so this is intentionally light).
func (b *Bus) Reset() {}

// ---------------------------------------------------------------------------
// Cartridge window (shared by the 68k map and the Z80 window helper)
// ---------------------------------------------------------------------------

// readROMByte resolves a byte in the cartridge window, honoring the SRAM
// overlay. SRAM is a byte-wide device decoded on odd addresses only: odd
// addresses reach the SRAM array (byte index = offset/2), even addresses
// read back open bus.
func (b *Bus) readROMByte(addr uint32) uint8 {
	if b.sramOn && b.cart != nil && b.cart.SRAM &&
		addr >= b.cart.SRAMStart && addr <= b.cart.SRAMEnd {
		if addr&1 == 0 {
			return 0xFF
		}
		if i := (addr - b.cart.SRAMStart) >> 1; int(i) < len(b.SRAM) {
			return b.SRAM[i]
		}
		return 0xFF
	}
	if b.cart == nil {
		return 0xFF
	}
	return b.cart.ReadROM(addr)
}

// writeROMByte stores into the SRAM overlay when it is switched in; plain
// ROM writes are ignored.
func (b *Bus) writeROMByte(addr uint32, val uint8) {
	if b.sramOn && b.cart != nil && b.cart.SRAM &&
		addr >= b.cart.SRAMStart && addr <= b.cart.SRAMEnd {
		if addr&1 == 0 {
			return
		}
		if i := (addr - b.cart.SRAMStart) >> 1; int(i) < len(b.SRAM) {
			b.SRAM[i] = val
		}
	}
}

// ---------------------------------------------------------------------------
// 68k address space
// ---------------------------------------------------------------------------

// Read8 reads one byte from the 68k address space.
func (b *Bus) Read8(addr uint32) uint8 {
	addr &= 0xFFFFFF
	switch {
	case addr < 0x400000:
		return b.readROMByte(addr)
	case addr >= 0xE00000:
		return b.RAM[addr&0xFFFF]
	case addr >= 0xC00000: // VDP window 0xC00000-0xDFFFFF
		w := b.vdp.ReadPort((addr & 0x1F) &^ 1)
		if addr&1 == 1 {
			return uint8(w)
		}
		return uint8(w >> 8)
	case addr >= 0xA04000 && addr <= 0xA04003: // YM2612
		if addr == 0xA04000 {
			return b.sound.ReadYM()
		}
		return 0
	case addr >= 0xA10000 && addr <= 0xA1001F:
		return b.readIO8(addr)
	case addr == 0xA11100: // Z80 bus request status, high byte: reads 1
		// while the Z80 still owns the bus, 0 once the bus is granted to
		// the 68000. Games poll this bit waiting for it to clear (the
		// stock Sega startup code and Sonic's sound driver loader do).
		if b.Z80Halted() {
			return 0x00
		}
		return 0x01
	case addr == 0xA11101: // Z80 bus request status, low byte
		return 0x00
	case addr == 0xA11200 || addr == 0xA11201: // Z80 reset
		return 0
	case addr >= 0xA14000 && addr <= 0xA14003: // TMSS
		return 0
	case addr >= 0xA12000 && addr <= 0xA120FF:
		if b.cd != nil {
			return b.cd.MainRead8(addr - 0xA12000)
		}
		return 0xFF
	case addr >= 0xA00000: // Z80 window 0xA00000-0xA0FFFF (mirrored decode)
		if a := addr & 0x7FFF; a < 0x2000 {
			return b.ZRAM[a]
		}
		return 0xFF
	}
	return 0xFF
}

// Read16 reads a big-endian word. Device regions with their own word
// semantics (VDP ports, Z80 control, TMSS) are handled directly; all other
// regions compose from byte reads.
func (b *Bus) Read16(addr uint32) uint16 {
	addr &= 0xFFFFFF
	switch {
	case addr >= 0xC00000 && addr < 0xE00000:
		return b.vdp.ReadPort(addr & 0x1F)
	case addr == 0xA11100: // status word: 0x0100 while the Z80 owns the
		// bus, 0x0000 once granted (see the byte read above)
		if b.Z80Halted() {
			return 0x0000
		}
		return 0x0100
	case addr == 0xA11200:
		return 0
	case addr >= 0xA14000 && addr <= 0xA14003:
		return 0
	}
	return uint16(b.Read8(addr))<<8 | uint16(b.Read8(addr+1))
}

// Read32 reads a big-endian long as two word accesses (which is also how
// a 32-bit VDP access must be decomposed).
func (b *Bus) Read32(addr uint32) uint32 {
	return uint32(b.Read16(addr))<<16 | uint32(b.Read16(addr+2))
}

// Write8 writes one byte into the 68k address space.
func (b *Bus) Write8(addr uint32, val uint8) {
	addr &= 0xFFFFFF
	switch {
	case addr < 0x400000:
		b.writeROMByte(addr, val)
	case addr >= 0xE00000:
		b.RAM[addr&0xFFFF] = val
	case addr >= 0xC00000: // VDP window 0xC00000-0xDFFFFF
		off := addr & 0x1F
		switch {
		case off == 0x11: // PSG byte lane (0xC00011)
			b.sound.WritePSG(val)
		case off == 0x10: // PSG port high lane: writes are ignored
		case addr&1 == 0:
			b.vdp.WritePort(off, uint16(val)<<8)
		default:
			b.vdp.WritePort(off, uint16(val))
		}
	case addr >= 0xA04000 && addr <= 0xA04003: // YM2612
		b.writeYM(addr&3, val, false)
	case addr >= 0xA130F0 && addr <= 0xA130FF: // SRAM / ROM switch
		b.sramOn = val&1 == 1
	case addr == 0xA11100: // Z80 bus request: bit 0 of the byte is the grant bit
		b.z80Req = val&1 == 1
	case addr == 0xA11200: // Z80 reset
		b.writeZ80Reset(val&1 == 1)
	case addr >= 0xA10000 && addr <= 0xA1001F:
		b.writeIO8(addr, val)
	case addr >= 0xA12000 && addr <= 0xA120FF:
		if b.cd != nil {
			b.cd.MainWrite8(addr-0xA12000, val)
		}
	case addr >= 0xA00000: // Z80 window 0xA00000-0xA0FFFF (mirrored decode)
		if a := addr & 0x7FFF; a < 0x2000 {
			b.ZRAM[a] = val
		}
	}
	// Everything else (TMSS, unmapped): writes are ignored.
}

// writeZ80Reset applies the Z80 reset line level: released=false asserts
// reset; releasing it after an assert arms the one-shot TakeZ80Reset.
func (b *Bus) writeZ80Reset(released bool) {
	if !released {
		b.z80Rst = true
		return
	}
	if b.z80Rst {
		b.z80RstPending = true
	}
	b.z80Rst = false
}

// Write16 writes a big-endian word. The VDP takes word accesses directly;
// every other region decomposes into byte writes, which matches hardware
// bus behavior for the byte-wide devices behind them (YM2612 latches,
// IO ports, the Z80 control registers whose grant bit lives in the high
// byte, and the SRAM switch whose low byte lands last).
func (b *Bus) Write16(addr uint32, val uint16) {
	addr &= 0xFFFFFF
	if addr >= 0xC00000 && addr < 0xE00000 {
		b.vdp.WritePort(addr&0x1F, val)
		return
	}
	b.Write8(addr, uint8(val>>8))
	b.Write8(addr+1, uint8(val))
}

// Write32 writes a big-endian long as two word accesses.
func (b *Bus) Write32(addr uint32, val uint32) {
	b.Write16(addr, uint16(val>>16))
	b.Write16(addr+2, uint16(val))
}

// ---------------------------------------------------------------------------
// 68k I/O ports (0xA10000-0xA1001F)
// ---------------------------------------------------------------------------

func (b *Bus) readIO8(addr uint32) uint8 {
	switch addr {
	case 0xA10000: // version register: overseas (export), NTSC, no TMSS
		return 0x80
	case 0xA10003, 0xA10005: // controller data ports 1 and 2
		return b.padValue(int((addr - 0xA10003) / 2))
	case 0xA10007: // expansion port: nothing attached
		return 0xFF
	case 0xA10009, 0xA1000B, 0xA1000D: // control ports
		return b.ioCtrl[(addr-0xA10009)/2]
	}
	return 0xFF
}

func (b *Bus) writeIO8(addr uint32, val uint8) {
	switch addr {
	case 0xA10003, 0xA10005, 0xA10007:
		b.ioData[(addr-0xA10003)/2] = val
	case 0xA10009, 0xA1000B, 0xA1000D:
		b.ioCtrl[(addr-0xA10009)/2] = val
	}
}

// padValue builds the active-low controller byte for one port. TH selects
// which button group is multiplexed onto the direction pins: when the
// control register marks TH as an output, TH is whatever the game wrote
// to data bit 6; when TH is an input it floats high on a standard pad.
func (b *Bus) padValue(p int) uint8 {
	th := uint8(1)
	if b.ioCtrl[p]&0x40 != 0 {
		th = (b.ioData[p] >> 6) & 1
	}
	m := b.pads[p]
	inv := func(bit uint16) uint8 {
		if m&bit == 0 {
			return 1
		}
		return 0
	}
	if th == 1 {
		// ?1CBRLDU: C,B on the button pins, full direction pad.
		return 0x80 | 0x40 |
			inv(PadC)<<5 | inv(PadB)<<4 |
			inv(PadRight)<<3 | inv(PadLeft)<<2 |
			inv(PadDown)<<1 | inv(PadUp)
	}
	// ?0SA00DU: Start,A on the button pins; the Left/Right pins read 0
	// on a 3-button pad in this phase.
	return 0x80 |
		inv(PadStart)<<5 | inv(PadA)<<4 |
		inv(PadDown)<<1 | inv(PadUp)
}

// ---------------------------------------------------------------------------
// Z80 address space
// ---------------------------------------------------------------------------

// Fetch reads an opcode byte (M1 cycle); identical to Read here.
func (b *Bus) Fetch(addr uint16) uint8 { return b.Read(addr) }

// Read reads one byte from the Z80 address space.
func (b *Bus) Read(addr uint16) uint8 {
	switch {
	case addr < 0x2000:
		return b.ZRAM[addr]
	case addr >= 0x4000 && addr <= 0x4003: // YM2612
		if addr == 0x4000 {
			return b.sound.ReadYM()
		}
		return 0xFF
	case addr >= 0x6000 && addr < 0x8000: // bank register / PSG window: reads float
		return 0xFF
	case addr >= 0x8000: // 32KB window into 68k space
		return b.read68kByte((b.bank << 15) | uint32(addr&0x7FFF))
	}
	return 0xFF
}

// Write writes one byte into the Z80 address space.
func (b *Bus) Write(addr uint16, val uint8) {
	switch {
	case addr < 0x2000:
		b.ZRAM[addr] = val
	case addr >= 0x4000 && addr <= 0x4003: // YM2612
		b.writeYM(uint32(addr&3), val, true)
	case addr == 0x6000: // bank register: shift one bit in, LSB first
		b.bank = (b.bank >> 1) | (uint32(val&1) << 8)
		b.bank &= 0x1FF
	case addr == 0x7F11: // PSG
		b.sound.WritePSG(val)
	}
}

// In reads an I/O port; the Genesis Z80 has no port devices attached.
func (b *Bus) In(port uint16) uint8 { return 0xFF }

// Out writes an I/O port; ignored.
func (b *Bus) Out(port uint16, val uint8) {}

// read68kByte is the Z80 window's view into 68k space. Only the memory
// regions are reachable this way (cartridge window with the SRAM overlay,
// work RAM); device regions read as open bus so the sound CPU can never
// trigger VDP or IO side effects through the window.
func (b *Bus) read68kByte(a uint32) uint8 {
	a &= 0xFFFFFF
	switch {
	case a < 0x400000:
		return b.readROMByte(a)
	case a >= 0xE00000:
		return b.RAM[a&0xFFFF]
	}
	return 0xFF
}

// ---------------------------------------------------------------------------
// Save states
// ---------------------------------------------------------------------------

// sramStateSize is the fixed width of the SRAM field inside a serialized
// state. Carts with more SRAM than this (rare) keep only their first
// 64KB in save states; carts with less are zero padded.
const sramStateSize = 0x10000

// SerializeSize returns the exact serialized state size in bytes:
// work RAM (64KB) + Z80 RAM (8KB) + SRAM length prefix (4) + SRAM field
// (64KB) + trailing bus register block (16).
func (b *Bus) SerializeSize() int {
	return 0x10000 + 0x2000 + 4 + sramStateSize + 16
}

// Serialize writes the bus-owned state (RAM, Z80 RAM, SRAM, IO and Z80
// control state) into buf, which must be at least SerializeSize() bytes.
func (b *Bus) Serialize(buf []byte) error {
	if len(buf) < b.SerializeSize() {
		return errors.New("bus: serialize buffer too small")
	}
	off := 0
	copy(buf[off:off+0x10000], b.RAM[:])
	off += 0x10000
	copy(buf[off:off+0x2000], b.ZRAM[:])
	off += 0x2000
	n := len(b.SRAM)
	if n > sramStateSize {
		n = sramStateSize
	}
	binary.LittleEndian.PutUint32(buf[off:off+4], uint32(n))
	off += 4
	field := buf[off : off+sramStateSize]
	for i := range field {
		field[i] = 0
	}
	copy(field, b.SRAM[:n])
	off += sramStateSize
	blk := buf[off : off+16]
	for i := range blk {
		blk[i] = 0
	}
	copy(blk[0:3], b.ioData[:])
	copy(blk[3:6], b.ioCtrl[:])
	var flags uint8
	if b.z80Req {
		flags |= 1 << 0
	}
	if b.z80Rst {
		flags |= 1 << 1
	}
	if b.z80RstPending {
		flags |= 1 << 2
	}
	if b.sramOn {
		flags |= 1 << 3
	}
	blk[6] = flags
	binary.LittleEndian.PutUint16(blk[7:9], uint16(b.bank))
	return nil
}

// Deserialize restores state written by Serialize into the existing RAM
// arrays and SRAM slice (allocating SRAM if this bus has none but the
// state carries some).
func (b *Bus) Deserialize(buf []byte) error {
	if len(buf) < b.SerializeSize() {
		return errors.New("bus: state buffer too small")
	}
	off := 0
	copy(b.RAM[:], buf[off:off+0x10000])
	off += 0x10000
	copy(b.ZRAM[:], buf[off:off+0x2000])
	off += 0x2000
	n := int(binary.LittleEndian.Uint32(buf[off : off+4]))
	off += 4
	if n > sramStateSize {
		n = sramStateSize
	}
	if n > 0 {
		if len(b.SRAM) < n {
			b.SRAM = make([]uint8, n)
		}
		copy(b.SRAM[:n], buf[off:off+n])
	}
	off += sramStateSize
	blk := buf[off : off+16]
	copy(b.ioData[:], blk[0:3])
	copy(b.ioCtrl[:], blk[3:6])
	flags := blk[6]
	b.z80Req = flags&(1<<0) != 0
	b.z80Rst = flags&(1<<1) != 0
	b.z80RstPending = flags&(1<<2) != 0
	b.sramOn = flags&(1<<3) != 0
	b.bank = uint32(binary.LittleEndian.Uint16(blk[7:9])) & 0x1FF
	return nil
}
