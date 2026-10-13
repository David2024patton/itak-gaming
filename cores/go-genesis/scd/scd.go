package scd

import (
	"encoding/binary"
	"errors"
)

// Unit is the whole Sega CD add-on as seen by the two 68000 CPUs.
//
// The unit owns the sub CPU memory map (PRG-RAM, Word RAM, the boot
// stub window, PCM storage, the gate array register page and the
// CDD/CDC register block), the main CPU gate array view, the Word
// RAM ownership model, the polled CDC transfer model and save state
// serialization. It implements m68k.Bus for the sub CPU: the caller
// constructs the sub CPU with m68k.New(unit).
//
// The gate array and the CD block share one physical register page.
// The main CPU sees offsets 0x00 to 0x2F through MainRead8 and
// MainWrite8. The sub CPU sees offsets 0x00 to 0x5F through its bus
// window at 0xFF8000, where 0x30 to 0x5F are the CD block.
//
// Simplifications documented for this slice: Word RAM contention
// reads as 0xFF instead of stalling the requesting CPU, and the CDC
// busy time is measured in status register reads instead of elapsed
// cycles (see cdcStatus).
type Unit struct {
	disc *Disc
	stub []byte

	prgRAM  [0x80000]byte
	wordRAM [0x40000]byte
	pcmWave [0x10000]byte
	pcmRegs [16]byte

	// Gate array state.
	control    uint8 // offset 0x00: bit0 RES, bit1 BRQ (bits 0-1 only)
	resetArmed bool  // one-shot sub CPU reset notification pending
	led        uint8 // offset 0x01 stored byte
	memMode    uint8 // offset 0x02 stored bit0: 0 = 2M, 1 = 1M
	wordOwner  int   // 2M Word RAM owner: 0 = main CPU, 1 = sub CPU
	subHalf    int   // 1M mode: which 128 KB half the sub CPU owns (0 or 1)
	retMain    bool  // offset 0x02 RET flag as read by the main CPU
	retSub     bool  // offset 0x02 RET flag as read by the sub CPU
	reg03      uint8 // offset 0x03: bit0 H-INT enable, bits 3-4 CDC destination
	reg04      uint8 // offset 0x04: H-INT interval in scanlines
	reg05      uint8 // offset 0x05: H-INT vector byte, 0 = autovector
	mainFlag   uint8 // offset 0x0E: main CPU communication flag
	subFlag    uint8 // offset 0x0F: sub CPU communication flag
	commCmd    [8]uint16
	commStatus [8]uint16

	// CD block state (sub CPU offsets 0x30 to 0x5F).
	cddCmd    uint8
	cddParam  uint8
	cddResp   [4]uint8
	cdcBusy   bool
	cdcDone   bool // transfer complete flag
	cdcReady  bool // data ready flag: sector buffer holds a sector
	cdcError  bool
	busyReads int // status reads remaining before busy clears
	cdcLBA    [4]byte
	cdcCount  [2]byte
	cdcDest   [4]byte
	sectorBuf [2048]byte
	dataPtr   int // CDC data port stream position in sectorBuf
}

// NewUnit builds a unit with the given disc attached. The disc may
// be nil, which models an empty drive. Power-on state: the sub CPU
// is held in reset (RES = 0), the reset one-shot is armed, Word RAM
// is in 2M mode and owned by the sub CPU, and both RET flags are
// clear.
func NewUnit(d *Disc) *Unit {
	u := &Unit{
		disc:       d,
		stub:       BootStubImage(),
		wordOwner:  1,
		resetArmed: true,
	}
	u.refreshCDDResponse()
	return u
}

// refreshCDDResponse recomputes the CDD response bytes from the
// attached disc. It runs on construction and on every CDD command
// write.
func (u *Unit) refreshCDDResponse() {
	u.cddResp = [4]uint8{0x00, 0x00, 0x01, 0x00}
	if u.disc != nil {
		u.cddResp[1] = byte(u.disc.DataTracks())
	}
}

// ---------------------------------------------------------------------------
// Unit misc API
// ---------------------------------------------------------------------------

// SubHalted reports whether the sub CPU must not run: it is held in
// reset (RES = 0) or bus-requested by the main CPU (BRQ = 1).
func (u *Unit) SubHalted() bool {
	return u.control&0x01 == 0 || u.control&0x02 != 0
}

// TakeSubReset returns true exactly once per reset release: the
// power-on arm, each RES 0 to 1 transition, and each ForceSubReset
// arm report true, but only once the sub CPU is actually released
// (RES = 1). While RES = 0 it returns false and keeps the arm. The
// pattern mirrors the Genesis bus TakeZ80Reset.
func (u *Unit) TakeSubReset() bool {
	if u.resetArmed && u.control&0x01 != 0 {
		u.resetArmed = false
		return true
	}
	return false
}

// ForceSubReset asserts the sub CPU reset line (RES = 0) and arms
// the one-shot reset notification, modelling the system reset
// button.
func (u *Unit) ForceSubReset() {
	u.control &^= 0x01
	u.resetArmed = true
}

// LineTick evaluates the H-INT condition for one display line. It
// fires when the H-INT enable bit (register 0x03 bit0) is set, the
// interval (register 0x04) is nonzero, the line number is positive,
// and the line is an exact multiple of the interval. The returned
// vector is nil for autovector (register 0x05 = 0) and otherwise a
// pointer to a copy of the vector byte.
func (u *Unit) LineTick(line int) (fire bool, vector *uint8) {
	if u.reg03&0x01 == 0 {
		return false, nil
	}
	interval := int(u.reg04)
	if interval <= 0 || line <= 0 || line%interval != 0 {
		return false, nil
	}
	if u.reg05 == 0 {
		return true, nil
	}
	v := u.reg05
	return true, &v
}

// PRGRAM returns the 512 KB PRG-RAM array.
func (u *Unit) PRGRAM() []byte { return u.prgRAM[:] }

// WordRAM returns the 256 KB Word RAM array.
func (u *Unit) WordRAM() []byte { return u.wordRAM[:] }

// SubFlag returns the sub CPU communication flag byte (offset 0x0F).
func (u *Unit) SubFlag() uint8 { return u.subFlag }

// MainFlag returns the main CPU communication flag byte (offset 0x0E).
func (u *Unit) MainFlag() uint8 { return u.mainFlag }

// CommStatus returns comm status word i (0 to 7), or 0 out of range.
func (u *Unit) CommStatus(i int) uint16 {
	if i < 0 || i >= len(u.commStatus) {
		return 0
	}
	return u.commStatus[i]
}

// CommCommand returns comm command word i (0 to 7), or 0 out of range.
func (u *Unit) CommCommand(i int) uint16 {
	if i < 0 || i >= len(u.commCmd) {
		return 0
	}
	return u.commCmd[i]
}

// WordOwnerSub reports whether the sub CPU owns Word RAM in 2M mode.
func (u *Unit) WordOwnerSub() bool { return u.wordOwner == 1 }

// ---------------------------------------------------------------------------
// Sub CPU bus (m68k.Bus)
// ---------------------------------------------------------------------------

// Reset satisfies the m68k.Bus interface. The unit owns no bus reset
// behavior of its own, so this is a no-op.
func (u *Unit) Reset() {}

// Read8 reads one byte from the sub CPU address space. Addresses
// are masked to 24 bits.
func (u *Unit) Read8(addr uint32) uint8 {
	addr &= 0xFFFFFF
	switch {
	case addr < 0x020000:
		// Boot stub window.
		if int(addr) < len(u.stub) {
			return u.stub[addr]
		}
		return 0xFF
	case addr < 0x0A0000:
		return u.prgRAM[addr-0x020000]
	case addr >= 0x200000 && addr < 0x240000:
		return u.subWordRead8(addr)
	case addr >= 0xFF0000 && addr < 0xFF0010:
		return u.pcmRegs[addr-0xFF0000]
	case addr >= 0xFF1000 && addr < 0xFF2000:
		off := int(u.pcmRegs[0x0C]&0x0F)<<12 | int(addr&0xFFF)
		return u.pcmWave[off]
	case addr >= 0xFF8000 && addr < 0xFF8200:
		return u.subRegRead(uint8(addr & 0xFF))
	}
	return 0xFF
}

// Read16 reads a big-endian word composed from two byte reads.
func (u *Unit) Read16(addr uint32) uint16 {
	return uint16(u.Read8(addr))<<8 | uint16(u.Read8(addr+1))
}

// Read32 reads a big-endian long composed from two word reads.
func (u *Unit) Read32(addr uint32) uint32 {
	return uint32(u.Read16(addr))<<16 | uint32(u.Read16(addr+2))
}

// Write8 writes one byte into the sub CPU address space.
func (u *Unit) Write8(addr uint32, v uint8) {
	addr &= 0xFFFFFF
	switch {
	case addr < 0x020000:
		// Boot stub window: writes ignored.
	case addr < 0x0A0000:
		u.prgRAM[addr-0x020000] = v
	case addr >= 0x200000 && addr < 0x240000:
		u.subWordWrite8(addr, v)
	case addr >= 0xFF0000 && addr < 0xFF0010:
		u.pcmRegs[addr-0xFF0000] = v
	case addr >= 0xFF1000 && addr < 0xFF2000:
		off := int(u.pcmRegs[0x0C]&0x0F)<<12 | int(addr&0xFFF)
		u.pcmWave[off] = v
	case addr >= 0xFF8000 && addr < 0xFF8200:
		u.subRegWrite(uint8(addr&0xFF), v)
	}
	// Everything else: writes ignored.
}

// Write16 writes a big-endian word as two byte writes in address
// order.
func (u *Unit) Write16(addr uint32, v uint16) {
	u.Write8(addr, uint8(v>>8))
	u.Write8(addr+1, uint8(v))
}

// Write32 writes a big-endian long as four byte writes in address
// order.
func (u *Unit) Write32(addr uint32, v uint32) {
	u.Write8(addr, uint8(v>>24))
	u.Write8(addr+1, uint8(v>>16))
	u.Write8(addr+2, uint8(v>>8))
	u.Write8(addr+3, uint8(v))
}

// ---------------------------------------------------------------------------
// Word RAM windows and ownership
// ---------------------------------------------------------------------------

// subWordRead8 is the sub CPU view of the Word RAM window at
// 0x200000 to 0x23FFFF.
func (u *Unit) subWordRead8(addr uint32) uint8 {
	if u.memMode == 0 {
		if u.wordOwner != 1 {
			return 0xFF
		}
		return u.wordRAM[addr-0x200000]
	}
	off := (addr - 0x200000) & 0x1FFFF
	return u.wordRAM[uint32(u.subHalf)*0x20000+off]
}

// subWordWrite8 is the sub CPU write view of the Word RAM window.
func (u *Unit) subWordWrite8(addr uint32, v uint8) {
	if u.memMode == 0 {
		if u.wordOwner != 1 {
			return
		}
		u.wordRAM[addr-0x200000] = v
		return
	}
	off := (addr - 0x200000) & 0x1FFFF
	u.wordRAM[uint32(u.subHalf)*0x20000+off] = v
}

// MainWordRead8 is the main CPU view of the Word RAM window at
// 0x200000 to 0x23FFFF, the mirror image of the sub CPU view. It is
// a helper for tests and for the later Genesis bus wiring. In 2M
// mode the main CPU only sees Word RAM while it owns it. In 1M mode
// it sees the 128 KB half the sub CPU does not own.
func (u *Unit) MainWordRead8(addr uint32) uint8 {
	if addr < 0x200000 || addr >= 0x240000 {
		return 0xFF
	}
	if u.memMode == 0 {
		if u.wordOwner != 0 {
			return 0xFF
		}
		return u.wordRAM[addr-0x200000]
	}
	off := (addr - 0x200000) & 0x1FFFF
	return u.wordRAM[uint32(1-u.subHalf)*0x20000+off]
}

// MainWordWrite8 is the main CPU write view of the Word RAM window.
func (u *Unit) MainWordWrite8(addr uint32, v uint8) {
	if addr < 0x200000 || addr >= 0x240000 {
		return
	}
	if u.memMode == 0 {
		if u.wordOwner != 0 {
			return
		}
		u.wordRAM[addr-0x200000] = v
		return
	}
	off := (addr - 0x200000) & 0x1FFFF
	u.wordRAM[uint32(1-u.subHalf)*0x20000+off] = v
}

// writeMemMode handles a write to gate array offset 0x02 from
// either CPU. Bit0 (MODE) is stored. Bit1 (DMNA) is an action and
// is never stored: the writing side clears its own RET flag, then,
// in 2M mode, the current owner gives ownership to the other side
// and the receiving side RET flag sets (a non-owner DMNA write
// changes no ownership); in 1M mode, any DMNA write swaps which
// half the sub CPU owns and the other side RET flag sets. The mode
// stored by the same write is the mode the DMNA action runs in, so
// a combined MODE plus DMNA write enters the new mode and performs
// that mode transfer in one step.
func (u *Unit) writeMemMode(v uint8, fromMain bool) {
	u.memMode = v & 0x01
	if v&0x02 == 0 {
		return
	}
	if fromMain {
		u.retMain = false
	} else {
		u.retSub = false
	}
	if u.memMode == 0 {
		ownerIsMain := u.wordOwner == 0
		if ownerIsMain != fromMain {
			return // non-owner DMNA in 2M mode is ignored
		}
		if ownerIsMain {
			u.wordOwner = 1
			u.retSub = true
		} else {
			u.wordOwner = 0
			u.retMain = true
		}
		return
	}
	u.subHalf ^= 1
	if fromMain {
		u.retSub = true
	} else {
		u.retMain = true
	}
}

// ---------------------------------------------------------------------------
// Gate array register file (offsets 0x00 to 0x2F)
// ---------------------------------------------------------------------------

// MainRead8 reads one gate array byte for the main CPU. Offsets at
// or above 0x30 belong to the sub CPU CD block and read as 0 here.
func (u *Unit) MainRead8(off uint32) uint8 {
	if off >= 0x30 {
		return 0
	}
	return u.gateRead(uint8(off), true)
}

// MainWrite8 writes one gate array byte for the main CPU. Offsets
// at or above 0x30 are ignored, as are the sub CPU owned offsets
// (0x0F and the comm status words 0x20 to 0x2F).
func (u *Unit) MainWrite8(off uint32, v uint8) {
	if off >= 0x30 {
		return
	}
	u.gateWrite(uint8(off), v, true)
}

// gateRead reads a gate array offset for either CPU.
func (u *Unit) gateRead(off uint8, main bool) uint8 {
	switch {
	case off == 0x00:
		if main {
			return 0
		}
		return u.control
	case off == 0x01:
		return (u.led & 0xFE) | 0x01 // bit0 reads 1: a CD is present
	case off == 0x02:
		v := u.memMode
		if main {
			if u.retMain {
				v |= 0x04
			}
		} else {
			if u.retSub {
				v |= 0x04
			}
		}
		return v
	case off == 0x03:
		return u.reg03
	case off == 0x04:
		return u.reg04
	case off == 0x05:
		return u.reg05
	case off == 0x0E:
		return u.mainFlag
	case off == 0x0F:
		return u.subFlag
	case off >= 0x10 && off < 0x20:
		w := u.commCmd[(off-0x10)/2]
		if (off-0x10)%2 == 0 {
			return uint8(w >> 8)
		}
		return uint8(w)
	case off >= 0x20 && off < 0x30:
		w := u.commStatus[(off-0x20)/2]
		if (off-0x20)%2 == 0 {
			return uint8(w >> 8)
		}
		return uint8(w)
	}
	return 0
}

// gateWrite writes a gate array offset for either CPU, applying
// the per-offset ownership rules of the register file.
func (u *Unit) gateWrite(off uint8, v uint8, main bool) {
	switch {
	case off == 0x00:
		if !main {
			return // sub CPU writes to the control register are ignored
		}
		old := u.control
		u.control = v & 0x03
		if old&0x01 == 0 && u.control&0x01 != 0 {
			u.resetArmed = true // RES 0 to 1 arms the one-shot
		}
	case off == 0x01:
		if main {
			u.led = v
		}
	case off == 0x02:
		u.writeMemMode(v, main)
	case off == 0x03:
		u.reg03 = v
	case off == 0x04:
		u.reg04 = v
	case off == 0x05:
		u.reg05 = v
	case off == 0x0E:
		if main {
			u.mainFlag = v
		}
	case off == 0x0F:
		if !main {
			u.subFlag = v
		}
	case off >= 0x10 && off < 0x20:
		if !main {
			return
		}
		i := (off - 0x10) / 2
		if (off-0x10)%2 == 0 {
			u.commCmd[i] = (u.commCmd[i] & 0x00FF) | uint16(v)<<8
		} else {
			u.commCmd[i] = (u.commCmd[i] & 0xFF00) | uint16(v)
		}
	case off >= 0x20 && off < 0x30:
		if main {
			return
		}
		i := (off - 0x20) / 2
		if (off-0x20)%2 == 0 {
			u.commStatus[i] = (u.commStatus[i] & 0x00FF) | uint16(v)<<8
		} else {
			u.commStatus[i] = (u.commStatus[i] & 0xFF00) | uint16(v)
		}
	}
	// All other offsets: writes ignored.
}

// ---------------------------------------------------------------------------
// Sub CPU register page (offsets 0x00 to 0xFF at 0xFF8000)
// ---------------------------------------------------------------------------

// subRegRead reads one byte of the sub CPU register page.
func (u *Unit) subRegRead(off uint8) uint8 {
	if off < 0x30 {
		return u.gateRead(off, false)
	}
	return u.cdRead(off)
}

// subRegWrite writes one byte of the sub CPU register page.
func (u *Unit) subRegWrite(off uint8, v uint8) {
	if off < 0x30 {
		u.gateWrite(off, v, false)
		return
	}
	u.cdWrite(off, v)
}

// ---------------------------------------------------------------------------
// CD block (sub CPU offsets 0x30 to 0x5F)
// ---------------------------------------------------------------------------

// cdRead reads one CD block register.
func (u *Unit) cdRead(off uint8) uint8 {
	switch {
	case off == 0x30:
		// CDD status: bit0 disc present, bit1 TOC ready.
		if u.disc != nil {
			return 0x03
		}
		return 0x00
	case off == 0x31:
		return u.cddParam
	case off >= 0x32 && off <= 0x35:
		return u.cddResp[off-0x32]
	case off == 0x36:
		return 0 // CDC control is write only
	case off == 0x37:
		return u.cdcStatus()
	case off >= 0x38 && off <= 0x3B:
		return u.cdcLBA[off-0x38]
	case off >= 0x3C && off <= 0x3D:
		return u.cdcCount[off-0x3C]
	case off >= 0x3E && off <= 0x41:
		return u.cdcDest[off-0x3E]
	case off == 0x48:
		return u.cdcDataPortRead()
	}
	return 0
}

// cdWrite writes one CD block register.
func (u *Unit) cdWrite(off uint8, v uint8) {
	switch {
	case off == 0x30:
		u.cddCmd = v
		u.refreshCDDResponse()
	case off == 0x31:
		u.cddParam = v
	case off == 0x36:
		if v&0x02 != 0 {
			u.cdcDone = false // clear complete flag
		}
		if v&0x01 != 0 {
			u.cdcStart()
		}
	case off >= 0x38 && off <= 0x3B:
		u.cdcLBA[off-0x38] = v
	case off >= 0x3C && off <= 0x3D:
		u.cdcCount[off-0x3C] = v
	case off >= 0x3E && off <= 0x41:
		u.cdcDest[off-0x3E] = v
	}
	// Response bytes, status, and the data port ignore writes.
}

// cdcStatus reads the CDC status register. It is also the only
// clock the CDC busy model has: each read while busy decrements
// busyReads, and the read that brings it to zero clears busy and
// sets the complete flag, which that same read reports. Status
// bits: bit0 busy, bit1 transfer complete, bit2 data ready (the
// sector buffer holds a sector for the data port), bit3 error.
func (u *Unit) cdcStatus() uint8 {
	if u.cdcBusy {
		u.busyReads--
		if u.busyReads <= 0 {
			u.busyReads = 0
			u.cdcBusy = false
			u.cdcDone = true
		}
	}
	var s uint8
	if u.cdcBusy {
		s |= 0x01
	}
	if u.cdcDone {
		s |= 0x02
	}
	if u.cdcReady {
		s |= 0x04
	}
	if u.cdcError {
		s |= 0x08
	}
	return s
}

// cdcDataPortRead streams the internal sector buffer. The read
// pointer resets on every transfer start; reads past the end of
// the 2048 byte buffer return 0xFF.
func (u *Unit) cdcDataPortRead() uint8 {
	if u.dataPtr >= len(u.sectorBuf) {
		return 0xFF
	}
	v := u.sectorBuf[u.dataPtr]
	u.dataPtr++
	return v
}

// cdcStart executes one CDC transfer on a control register write
// with bit0 set. The LBA, sector count and destination are taken
// from the CDC latches, assembled big-endian. Sectors are written
// per the CDC destination field (register 0x03 bits 3-4): 0 keeps
// the data in the sector buffer only, 1 DMA writes to PRG-RAM (the
// destination latch is a sub CPU bus address and any part of a
// sector falling outside the PRG-RAM window sets the error flag
// and stops the transfer), 2 writes Word RAM at a physical offset
// wrapping at 0x40000, and 3 writes PCM wave RAM at a physical
// offset wrapping at 0x10000. The last sector read is always kept
// in the sector buffer and data ready is set once at least one
// sector has been read. With no disc attached the error and
// complete flags set immediately with no busy period. Otherwise
// busy is set with busyReads = 2 and complete only sets after the
// status read countdown; a zero sector count is a no-op success.
func (u *Unit) cdcStart() {
	u.cdcDone = false
	u.cdcError = false
	u.cdcReady = false
	u.dataPtr = 0
	if u.disc == nil {
		u.cdcError = true
		u.cdcDone = true
		u.cdcBusy = false
		u.busyReads = 0
		return
	}
	lba := int64(binary.BigEndian.Uint32(u.cdcLBA[:]))
	count := int(binary.BigEndian.Uint16(u.cdcCount[:]))
	dest := binary.BigEndian.Uint32(u.cdcDest[:])
	ddt := (u.reg03 >> 3) & 0x03

loop:
	for i := 0; i < count; i++ {
		data, err := u.disc.ReadData(int(lba) + i)
		if err != nil {
			u.cdcError = true
			break loop
		}
		for j := range u.sectorBuf {
			u.sectorBuf[j] = 0
		}
		copy(u.sectorBuf[:], data)
		u.cdcReady = true
		switch ddt {
		case 1:
			start := int64(dest) - 0x020000 + int64(i)*2048
			if start < 0 || start+2048 > int64(len(u.prgRAM)) {
				u.cdcError = true
				break loop
			}
			copy(u.prgRAM[start:start+2048], data)
		case 2:
			base := int64(dest) + int64(i)*2048
			for j, b := range data {
				u.wordRAM[(base+int64(j))%0x40000] = b
			}
		case 3:
			base := int64(dest) + int64(i)*2048
			for j, b := range data {
				u.pcmWave[(base+int64(j))%0x10000] = b
			}
		}
	}
	u.cdcBusy = true
	u.busyReads = 2
	u.cdcDone = false
}

// ---------------------------------------------------------------------------
// Save states
// ---------------------------------------------------------------------------

// SerializeSize returns the exact serialized size of a Unit:
// PRG-RAM (0x80000) + Word RAM (0x40000) + PCM wave RAM (0x10000)
// + the CDC sector buffer (2048) + a fixed 256 byte register and
// state block. The disc pointer is not part of the state.
func (u *Unit) SerializeSize() int {
	return 0x80000 + 0x40000 + 0x10000 + 2048 + 256
}

// The 256 byte register and state block at the tail of a serialized
// Unit has this layout (all multi-byte values big-endian):
//
//	0        control byte
//	1        LED byte
//	2        memory mode bit (offset 0x02 bit0)
//	3        Word RAM owner (0 main, 1 sub)
//	4        1M sub half (0 or 1)
//	5        flags: bit0 RET main, bit1 RET sub, bit2 reset armed,
//	         bit3 CDC busy, bit4 CDC complete, bit5 CDC data ready,
//	         bit6 CDC error
//	6        register 0x03
//	7        register 0x04
//	8        register 0x05
//	9        main comm flag (0x0E)
//	10       sub comm flag (0x0F)
//	11       reserved, zero
//	12..27   PCM registers (16 bytes)
//	28..43   comm command words (8, big-endian)
//	44..59   comm status words (8, big-endian)
//	60       CDD command byte
//	61       CDD parameter byte
//	62..65   CDD response bytes
//	66..69   CDC LBA latch
//	70..71   CDC sector count latch
//	72..75   CDC destination latch
//	76..77   CDC busy read countdown
//	78..79   CDC data port pointer
//	80..255  reserved, zero

// Serialize writes the unit state into buf, which must be at least
// SerializeSize bytes.
func (u *Unit) Serialize(buf []byte) error {
	if len(buf) < u.SerializeSize() {
		return errors.New("scd: serialize buffer too small")
	}
	off := 0
	copy(buf[off:off+0x80000], u.prgRAM[:])
	off += 0x80000
	copy(buf[off:off+0x40000], u.wordRAM[:])
	off += 0x40000
	copy(buf[off:off+0x10000], u.pcmWave[:])
	off += 0x10000
	copy(buf[off:off+2048], u.sectorBuf[:])
	off += 2048
	blk := buf[off : off+256]
	for i := range blk {
		blk[i] = 0
	}
	blk[0] = u.control
	blk[1] = u.led
	blk[2] = u.memMode
	blk[3] = byte(u.wordOwner)
	blk[4] = byte(u.subHalf)
	var flags uint8
	if u.retMain {
		flags |= 1 << 0
	}
	if u.retSub {
		flags |= 1 << 1
	}
	if u.resetArmed {
		flags |= 1 << 2
	}
	if u.cdcBusy {
		flags |= 1 << 3
	}
	if u.cdcDone {
		flags |= 1 << 4
	}
	if u.cdcReady {
		flags |= 1 << 5
	}
	if u.cdcError {
		flags |= 1 << 6
	}
	blk[5] = flags
	blk[6] = u.reg03
	blk[7] = u.reg04
	blk[8] = u.reg05
	blk[9] = u.mainFlag
	blk[10] = u.subFlag
	copy(blk[12:28], u.pcmRegs[:])
	for i, w := range u.commCmd {
		binary.BigEndian.PutUint16(blk[28+i*2:30+i*2], w)
	}
	for i, w := range u.commStatus {
		binary.BigEndian.PutUint16(blk[44+i*2:46+i*2], w)
	}
	blk[60] = u.cddCmd
	blk[61] = u.cddParam
	copy(blk[62:66], u.cddResp[:])
	copy(blk[66:70], u.cdcLBA[:])
	copy(blk[70:72], u.cdcCount[:])
	copy(blk[72:76], u.cdcDest[:])
	binary.BigEndian.PutUint16(blk[76:78], uint16(u.busyReads))
	binary.BigEndian.PutUint16(blk[78:80], uint16(u.dataPtr))
	return nil
}

// Deserialize restores state written by Serialize. It restores
// everything except the disc pointer, which the caller keeps.
func (u *Unit) Deserialize(buf []byte) error {
	if len(buf) < u.SerializeSize() {
		return errors.New("scd: state buffer too small")
	}
	if u.stub == nil {
		u.stub = BootStubImage()
	}
	off := 0
	copy(u.prgRAM[:], buf[off:off+0x80000])
	off += 0x80000
	copy(u.wordRAM[:], buf[off:off+0x40000])
	off += 0x40000
	copy(u.pcmWave[:], buf[off:off+0x10000])
	off += 0x10000
	copy(u.sectorBuf[:], buf[off:off+2048])
	off += 2048
	blk := buf[off : off+256]
	u.control = blk[0] & 0x03
	u.led = blk[1]
	u.memMode = blk[2] & 0x01
	u.wordOwner = int(blk[3]) & 1
	u.subHalf = int(blk[4]) & 1
	flags := blk[5]
	u.retMain = flags&(1<<0) != 0
	u.retSub = flags&(1<<1) != 0
	u.resetArmed = flags&(1<<2) != 0
	u.cdcBusy = flags&(1<<3) != 0
	u.cdcDone = flags&(1<<4) != 0
	u.cdcReady = flags&(1<<5) != 0
	u.cdcError = flags&(1<<6) != 0
	u.reg03 = blk[6]
	u.reg04 = blk[7]
	u.reg05 = blk[8]
	u.mainFlag = blk[9]
	u.subFlag = blk[10]
	copy(u.pcmRegs[:], blk[12:28])
	for i := range u.commCmd {
		u.commCmd[i] = binary.BigEndian.Uint16(blk[28+i*2 : 30+i*2])
	}
	for i := range u.commStatus {
		u.commStatus[i] = binary.BigEndian.Uint16(blk[44+i*2 : 46+i*2])
	}
	u.cddCmd = blk[60]
	u.cddParam = blk[61]
	copy(u.cddResp[:], blk[62:66])
	copy(u.cdcLBA[:], blk[66:70])
	copy(u.cdcCount[:], blk[70:72])
	copy(u.cdcDest[:], blk[72:76])
	u.busyReads = int(binary.BigEndian.Uint16(blk[76:78]))
	u.dataPtr = int(binary.BigEndian.Uint16(blk[78:80]))
	return nil
}
