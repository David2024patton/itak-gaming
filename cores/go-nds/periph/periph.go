// Package periph implements the DS-2 device registers that are
// not plain storage: the per-CPU interrupt controllers, timers,
// DMA engines, the IPC sync and FIFO unit, the game card
// command port, the AUX SPI save memory, the ARM7 SPI bus
// (power, firmware flash, touchscreen), and the RTC stub. The
// system installs a Set as the bus IOHook; offsets the Set does
// not claim fall back to plain IO storage in the bus.
//
// Offset space: the bus hook offsets (IO page offset, plus the
// 0x04100000 window at 0x2000). Side index 0 is the ARM9, side
// index 1 is the ARM7, matching sideIdx.
package periph

import (
	"github.com/davidpatton/itak-gaming/cores/go-nds/bus"
)

// Interrupt source bits in IE/IF (GBATEK). Bit 7 is the RTC on
// the ARM7 only; bit 21 is the geometry FIFO on the ARM9 and
// unused on the ARM7; bits 22-24 are ARM7 only (pen down,
// wifi (no source in this module), SPI).
const (
	IRQVBlank   = 0
	IRQHBlank   = 1 // raised by the system, not this package
	IRQVCount   = 2 // raised by the system, not this package
	IRQTimer0   = 3
	IRQTimer1   = 4
	IRQTimer2   = 5
	IRQTimer3   = 6
	IRQRTC      = 7
	IRQDMA0     = 8
	IRQDMA1     = 9
	IRQDMA2     = 10
	IRQDMA3     = 11
	IRQKeys     = 12
	IRQCartSlot = 13 // GBA slot: no source in this module
	IRQIPCSync  = 16
	IRQIPCSend  = 17
	IRQIPCRecv  = 18
	IRQCardData = 19
	IRQCardIO   = 20
	IRQPendown  = 22
	IRQWifi     = 23 // no source in this module
	IRQSPI      = 24
)

// GXFIFOAddr is the geometry command FIFO port. DMA transfers
// and CPU writes landing here are counted in GXCount as the
// DS-3 sink.
const GXFIFOAddr = 0x04000400

// IO offsets (hook offset space) of the registers claimed here.
const (
	offDMA9   = 0x0B0
	offDMA7   = 0x0B0
	offTimers = 0x100
	offKeyIn  = 0x130
	offExtKey = 0x136
	offRTC    = 0x138
	offIPC    = 0x180
	offAuxSPI = 0x1A0
	offROMCtl = 0x1A4
	offROMCmd = 0x1A8
	offIME    = 0x208
	offIE     = 0x210
	offIF     = 0x214
	offSPI    = 0x1C0
	offGXFIFO = 0x400

	extFIFORecv = 0x2000 // 0x04100000
	extCardData = 0x2010 // 0x04100010
)

func sideIdx(nine bool) int {
	if nine {
		return 0
	}
	return 1
}

// irqCtl is one CPU's IME/IE/IF register set. IF bits are set
// by devices through raise and cleared by the CPU writing 1s.
type irqCtl struct {
	ime    uint32
	ie     uint32
	iflags uint32
}

// Set is the whole DS-2 peripheral set behind the bus hook.
type Set struct {
	bus   *bus.Bus
	view9 bus.View
	view7 bus.View
	rom   func() []byte
	keys  func() uint32
	touch func() (uint16, uint16, bool)
	penDn bool

	irq [2]irqCtl
	tim [2][4]Timer
	dm9 [4]dma9Chan
	dm7 [4]dma7Chan
	ipc IPC
	crd Card
	aux AuxSPI
	spi SPI
	rtc RTC

	// GXCount is the number of 32-bit words consumed into the
	// geometry FIFO sink (the DS-3 geometry engine will drain
	// it for real).
	GXCount uint32
}

// New builds the peripheral set. The callbacks reach the parts
// of the machine the devices touch: the cartridge image, the
// button state, and the touchscreen state.
func New(b *bus.Bus, rom func() []byte, keys func() uint32, touch func() (uint16, uint16, bool)) *Set {
	s := &Set{
		bus:   b,
		view9: b.ARM9(),
		view7: b.ARM7(),
		rom:   rom,
		keys:  keys,
		touch: touch,
	}
	s.spi.fw = buildFirmware()
	return s
}

// Raise sets an interrupt flag on one side.
func (s *Set) Raise(nine bool, bit uint) {
	s.irq[sideIdx(nine)].iflags |= 1 << bit
}

// PendingIRQ reports whether the side's IRQ line should be
// asserted: master enable set and at least one enabled source
// pending.
func (s *Set) PendingIRQ(nine bool) bool {
	c := &s.irq[sideIdx(nine)]
	return c.ime&1 != 0 && c.ie&c.iflags != 0
}

// IFlags returns the side's pending interrupt flags (tests and
// the system's keypad evaluation read them here).
func (s *Set) IFlags(nine bool) uint32 { return s.irq[sideIdx(nine)].iflags }

// SetPenDown records the touchscreen pen state; a transition to
// down raises the ARM7 pen-down interrupt.
func (s *Set) SetPenDown(down bool) {
	if down && !s.penDn {
		s.Raise(false, IRQPendown)
	}
	s.penDn = down
}

// SaveRAM returns the cartridge save memory (the AUX SPI
// EEPROM backing).
func (s *Set) SaveRAM() []byte { return s.aux.save[:] }

// ReadIO implements bus.IOHook for the offsets this package
// claims.
func (s *Set) ReadIO(nine bool, off uint32, size int) (uint32, bool) {
	side := sideIdx(nine)
	switch {
	case off >= offDMA9 && off < offDMA9+0x40 && nine:
		return s.readDMA9(off-offDMA9, size), true
	case off >= offDMA7 && off < offDMA7+0x38 && !nine:
		return s.readDMA7(off-offDMA7, size), true
	case off >= offTimers && off < offTimers+0x10:
		return s.tim[side][(off-offTimers)/4].readReg(off&3, size), true
	case off == offKeyIn:
		return getU32(0xFFFF&^(s.keys()&0x3FF), off, size), true
	case off == offExtKey && !nine:
		v := uint32(0xFFFF)
		if s.keys()&0x400 != 0 {
			v &^= 1 << 0 // X
		}
		if s.keys()&0x800 != 0 {
			v &^= 1 << 1 // Y
		}
		if _, _, down := s.touch(); down {
			v &^= 1 << 6 // pen down
		}
		return getU32(v, off, size), true
	case off == offRTC && !nine:
		return uint32(s.rtc.read()), true
	case off == offIPC || off == offIPC+4 || off == offIPC+8:
		return s.ipc.read(side, off-offIPC, off, size), true
	case off == offAuxSPI || off == offAuxSPI+2:
		return s.aux.read(off-offAuxSPI, off, size), true
	case off == offROMCtl && nine:
		return getU32(s.crd.readROMCTRL(), off, size), true
	case off >= offROMCmd && off < offROMCmd+8 && nine:
		return s.crd.readCmd(off-offROMCmd, size), true
	case off == offSPI || off == offSPI+2:
		if nine {
			return 0, false
		}
		return s.spi.read(off-offSPI, off, size), true
	case off >= offIME && off < offIME+4, off >= offIE && off < offIE+4, off >= offIF && off < offIF+4:
		c := &s.irq[side]
		var v uint32
		switch {
		case off < offIME+4:
			v = c.ime
		case off < offIE+4:
			v = c.ie
		default:
			v = c.iflags
		}
		return getU32(v, off, size), true
	case !nine && (off == 0x204 || off == 0x205 || off == 0x247):
		// The ARM7 reads EXMEMCNT and WRAMCNT from the ARM9
		// page (the DS-1 bus routes these reads itself when no
		// hook is installed; with the hook we serve them).
		v := uint32(s.bus.IO9[off])
		if size >= 2 && off == 0x204 {
			v |= uint32(s.bus.IO9[0x205]) << 8
		}
		return v, true
	case off == extFIFORecv:
		return s.ipc.recv(side, s), true
	case off == extCardData && nine:
		v, done := s.crd.readData()
		if done {
			s.Raise(true, IRQCardIO)
		}
		return v, true
	}
	return 0, false
}

// WriteIO implements bus.IOHook for the offsets this package
// claims.
func (s *Set) WriteIO(nine bool, off uint32, size int, val uint32) bool {
	side := sideIdx(nine)
	switch {
	case off >= offDMA9 && off < offDMA9+0x40 && nine:
		s.writeDMA9(off-offDMA9, size, val)
		return true
	case off >= offDMA7 && off < offDMA7+0x38 && !nine:
		s.writeDMA7(off-offDMA7, size, val)
		return true
	case off >= offTimers && off < offTimers+0x10:
		s.tim[side][(off-offTimers)/4].writeReg(off&3, size, val)
		return true
	case off == offRTC && !nine:
		s.rtc.write()
		return true
	case off == offIPC || off == offIPC+4:
		s.ipc.writeCnt(side, off-offIPC, off, size, val, s)
		return true
	case off == offIPC+8:
		// IPCFIFOSEND is a full-word port; partial writes are
		// ignored on hardware.
		if size == 4 {
			s.ipc.send(side, val, s)
		}
		return true
	case off == offAuxSPI || off == offAuxSPI+2:
		s.aux.write(off-offAuxSPI, off, size, val)
		return true
	case off == offROMCtl && nine:
		s.crd.romctrl = putU32(s.crd.romctrl, off, size, val)
		if merged := s.crd.romctrl; merged&(1<<31) != 0 {
			s.crd.start(s)
		}
		return true
	case off >= offROMCmd && off < offROMCmd+8 && nine:
		s.crd.writeCmd(off-offROMCmd, size, val)
		return true
	case (off == offSPI || off == offSPI+2) && !nine:
		s.spi.write(off-offSPI, off, size, val, s)
		return true
	case off >= offIME && off < offIME+4:
		c := &s.irq[side]
		c.ime = putU32(c.ime, off, size, val) & 1
		return true
	case off >= offIE && off < offIE+4:
		c := &s.irq[side]
		c.ie = putU32(c.ie, off, size, val)
		return true
	case off >= offIF && off < offIF+4:
		// Writing 1s acknowledges (clears) the written flags.
		c := &s.irq[side]
		c.iflags &^= putU32(0, off, size, val)
		return true
	case off == offGXFIFO && nine:
		if size == 4 {
			s.GXCount++
		}
		return true
	}
	return false
}

// FrameTick advances the frame-based devices (the RTC) by one
// video frame. The system calls it once per RunFrame.
func (s *Set) FrameTick() {
	if s.rtc.tick() {
		s.Raise(false, IRQRTC)
	}
}

// getU32 extracts the unit of the given size at byte lane
// (off&3) from a 32-bit register value.
func getU32(v uint32, off uint32, size int) uint32 {
	switch size {
	case 1:
		return (v >> ((off & 3) * 8)) & 0xFF
	case 2:
		return (v >> ((off & 2) * 8)) & 0xFFFF
	}
	return v
}

// putU32 merges a written unit of the given size at byte lane
// (off&3) into a 32-bit register value and returns the result.
func putU32(v uint32, off uint32, size int, val uint32) uint32 {
	switch size {
	case 1:
		sh := (off & 3) * 8
		return v&^(0xFF<<sh) | (val&0xFF)<<sh
	case 2:
		sh := (off & 2) * 8
		return v&^(0xFFFF<<sh) | (val&0xFFFF)<<sh
	}
	return val
}

// stateWriter and stateReader are byte cursors for the device
// state blob, in the armcore style.
type stateWriter struct {
	b []byte
	n int
}

func (w *stateWriter) u8(v uint8)   { w.b[w.n] = v; w.n++ }
func (w *stateWriter) u16(v uint16) { w.u8(uint8(v)); w.u8(uint8(v >> 8)) }
func (w *stateWriter) u32(v uint32) {
	w.u16(uint16(v))
	w.u16(uint16(v >> 16))
}
func (w *stateWriter) u64(v uint64) { w.u32(uint32(v)); w.u32(uint32(v >> 32)) }
func (w *stateWriter) bytes(p []byte) {
	copy(w.b[w.n:], p)
	w.n += len(p)
}
func (w *stateWriter) flag(b bool) {
	if b {
		w.u8(1)
	} else {
		w.u8(0)
	}
}

type stateReader struct {
	b []byte
	n int
}

func (r *stateReader) u8() uint8 { v := r.b[r.n]; r.n++; return v }
func (r *stateReader) u16() uint16 {
	v := uint16(r.u8()) | uint16(r.u8())<<8
	return v
}
func (r *stateReader) u32() uint32 {
	v := uint32(r.u16()) | uint32(r.u16())<<16
	return v
}
func (r *stateReader) u64() uint64 {
	v := uint64(r.u32()) | uint64(r.u32())<<32
	return v
}
func (r *stateReader) bytes(p []byte) {
	copy(p, r.b[r.n:r.n+len(p)])
	r.n += len(p)
}
func (r *stateReader) flag() bool { return r.u8() != 0 }
