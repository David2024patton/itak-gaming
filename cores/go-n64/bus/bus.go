// Package bus implements the Nintendo 64 physical memory
// map for the go-n64 core: RDRAM, the RCP register pages
// (SP, DP, MI, VI, AI, PI, RI, SI), the cartridge ROM
// window, and the PIF region. It is original code written
// from the published N64 hardware documentation.
//
// The SP window and SP registers route to the real RSP
// (chips/rsp) since slice N64-2. Since N64-3 the DP page
// routes to the RDP (rdp package), the VI page to the
// video interface (vi package), and the AI page to the
// audio interface (ai package); the RSP's own DP register
// window (CP0 8..15) routes to the same RDP. The PI
// implements its DMA engine for real, including the
// completion interrupt through MI. Since N64-4 the PIF
// RAM window and SI page route to the real PIF (pif
// package), and the PI's second domain (0x08000000)
// reaches the cartridge save device (cart package).
package bus

import (
	"encoding/binary"

	"github.com/davidpatton/itak-gaming/cores/go-n64/ai"
	"github.com/davidpatton/itak-gaming/cores/go-n64/cart"
	"github.com/davidpatton/itak-gaming/cores/go-n64/chips/rsp"
	"github.com/davidpatton/itak-gaming/cores/go-n64/pif"
	"github.com/davidpatton/itak-gaming/cores/go-n64/rdp"
	"github.com/davidpatton/itak-gaming/cores/go-n64/vi"
)

// MI interrupt bit assignments (MI_INTR and MI_MASK).
const (
	MIIntrSP = 1 << 0
	MIIntrSI = 1 << 1
	MIIntrAI = 1 << 2
	MIIntrVI = 1 << 3
	MIIntrPI = 1 << 4
	MIIntrDP = 1 << 5
)

// Memory is the physical address space.
type Memory struct {
	RDRAM []byte // 4 MB, or 8 MB with the Expansion Pak
	ROM   []byte // cartridge ROM image (canonical big-endian)

	// RSP is the signal processor. The SP memory window,
	// SP registers, and SP PC on the bus route to it, and
	// its DMA engine works on RDRAM.
	RSP *rsp.RSP

	// DP is the display processor (RDP). The DP register
	// page routes to it, and the RSP's CP0 DP window
	// (registers 8..15) routes to the same engine.
	DP *rdp.RDP

	miMode uint32
	miIntr uint32
	miMask uint32

	// VI is the video interface; AI is the audio
	// interface. Their register pages route to the
	// devices, which the system advances with CPU time.
	VI *vi.VI
	AI *ai.AI

	piDramAddr uint32
	piCartAddr uint32
	piStatus   uint32
	piConfig   [0x34]byte // BSD domain configuration registers

	riRegs [0x20]byte
	rdRegs [0x40]byte // RDRAM registers

	// PIF is the peripheral interface behind the PIF RAM
	// window and the SI page. Saves is the cartridge
	// save device behind PI domain 2 (0x08000000).
	PIF   *pif.PIF
	Saves *cart.SaveStore

	siDramAddr uint32
	siPIFAddr  uint32
	siStatus   uint32 // SI_STATUS: only the interrupt bit (12) is modeled set

	// OnMIChange, when set, is called whenever the MI
	// interrupt line level may have changed. The system
	// wires it to the CPU's external interrupt input.
	OnMIChange func(active bool)
}

// New returns a Memory with 4 MB of RDRAM (8 MB when
// expansion is true) and the given ROM image attached.
func New(rom []byte, expansion bool) *Memory {
	size := 4 << 20
	if expansion {
		size = 8 << 20
	}
	m := &Memory{RDRAM: make([]byte, size), ROM: rom}
	m.RSP = rsp.New()
	m.RSP.AttachDRAM(m.RDRAM)
	m.RSP.OnInterrupt = func(active bool) {
		m.SetMI(MIIntrSP, active)
	}
	m.PIF = pif.New()
	m.Saves = cart.NewSaveStore(cart.SaveNone)
	m.PIF.AttachCart(m.Saves)
	m.wireDevices()
	return m
}

// wireDevices (re)builds the RDP, VI, and AI around the
// current RDRAM and RSP and wires their interrupt
// outputs into MI.
func (m *Memory) wireDevices() {
	m.DP = rdp.New()
	m.DP.AttachDRAM(m.RDRAM)
	m.DP.AttachDMEM(m.RSP.DMEM[:])
	m.DP.OnInterrupt = func(active bool) {
		m.SetMI(MIIntrDP, active)
	}
	m.RSP.DPRead = m.DP.ReadCPURegister
	m.RSP.DPWrite = m.DP.WriteCPURegister
	m.VI = vi.New()
	m.VI.OnInterrupt = func(active bool) {
		m.SetMI(MIIntrVI, active)
	}
	m.AI = ai.New(m.RDRAM)
	m.AI.OnInterrupt = func(active bool) {
		m.SetMI(MIIntrAI, active)
	}
}

// ResetDevices returns the RDP, VI, and AI to their
// power-on state (used by System.Reset).
func (m *Memory) ResetDevices() { m.wireDevices() }

// SetROM replaces the cartridge image.
func (m *Memory) SetROM(rom []byte) { m.ROM = rom }

// MIInterrupt reports whether the MI interrupt line is
// asserted (any pending interrupt that is unmasked).
func (m *Memory) MIInterrupt() bool {
	return m.miIntr&m.miMask != 0
}

// MIState returns the raw MI interrupt and mask
// registers, for save states.
func (m *Memory) MIState() (uint32, uint32) { return m.miIntr, m.miMask }

// RestoreMI sets the MI interrupt and mask registers
// from a save state; the caller refreshes the CPU
// interrupt line afterward.
func (m *Memory) RestoreMI(intr, mask uint32) { m.miIntr, m.miMask = intr, mask }

func (m *Memory) fireMI() {
	if m.OnMIChange != nil {
		m.OnMIChange(m.MIInterrupt())
	}
}

// SetMI raises or clears device interrupt bits (SP, SI, AI,
// VI, DP are driven by their devices; PI raises its own on
// DMA completion).
func (m *Memory) SetMI(bits uint32, on bool) {
	if on {
		m.miIntr |= bits
	} else {
		m.miIntr &^= bits
	}
	m.fireMI()
}

// romByte reads a byte from the cartridge window offset,
// mirroring the ROM image across its window.
func (m *Memory) romByte(off uint32) byte {
	if len(m.ROM) == 0 {
		return 0
	}
	return m.ROM[off%uint32(len(m.ROM))]
}

func (m *Memory) romRead(off uint32, size int) uint64 {
	var v uint64
	for i := 0; i < size; i++ {
		v = v<<8 | uint64(m.romByte(off+uint32(i)))
	}
	return v
}

// Read reads size bytes (1, 2, 4, or 8) at a physical
// address, big-endian.
func (m *Memory) Read(addr uint32, size int) uint64 {
	switch {
	case addr < 0x03F00000: // RDRAM
		return readBytes(m.RDRAM, addr&(uint32(len(m.RDRAM))-1), size)
	case addr < 0x04000000: // RDRAM registers
		return readBytes(m.rdRegs[:], addr-0x03F00000, size)
	case addr < 0x04002000: // SP memory (DMEM/IMEM)
		var v uint64
		for i := 0; i < size; i++ {
			v = v<<8 | uint64(m.spByteAt(addr-0x04000000+uint32(i)))
		}
		return v
	case addr < 0x04040000:
		return 0
	case addr < 0x04040020: // SP registers
		off := addr - 0x04040000
		return extract32(m.RSP.ReadRegister(off&^3), off&3, size)
	case addr >= 0x04080000 && addr < 0x04080004: // SP PC
		return extract32(m.RSP.ReadPC(), addr&3, size)
	case addr >= 0x04100000 && addr < 0x04100020: // DP registers
		off := addr - 0x04100000
		return extract32(m.DP.ReadRegister(off&^3), off&3, size)
	case addr >= 0x04300000 && addr < 0x04300010: // MI
		return uint64(m.readMI(addr - 0x04300000))
	case addr >= 0x04400000 && addr < 0x04400040: // VI
		off := addr - 0x04400000
		return extract32(m.VI.ReadRegister(off&^3), off&3, size)
	case addr >= 0x04500000 && addr < 0x04500040: // AI
		off := addr - 0x04500000
		return extract32(m.AI.ReadRegister(off&^3), off&3, size)
	case addr >= 0x04600000 && addr < 0x04600034: // PI
		return uint64(m.readPI(addr - 0x04600000))
	case addr >= 0x04700000 && addr < 0x04700020: // RI
		return readBytes(m.riRegs[:], addr-0x04700000, size)
	case addr >= 0x04800000 && addr < 0x04800020: // SI
		off := addr - 0x04800000
		return extract32(m.readSI(off&^3), off&3, size)
	case addr >= 0x08000000 && addr < 0x08020000: // PI domain 2: save device
		return m.saveRead(addr-0x08000000, size)
	case addr >= 0x10000000 && addr < 0x1FC00000: // cartridge ROM
		return m.romRead(addr-0x10000000, size)
	case addr >= 0x1FC007C0 && addr < 0x1FC00800: // PIF RAM
		return readBytes(m.PIF.RAM[:], addr-0x1FC007C0, size)
	}
	return 0
}

// Write writes size bytes at a physical address, big-endian.
func (m *Memory) Write(addr uint32, size int, val uint64) {
	switch {
	case addr < 0x03F00000: // RDRAM
		writeBytes(m.RDRAM, addr&(uint32(len(m.RDRAM))-1), size, val)
	case addr < 0x04000000: // RDRAM registers
		writeBytes(m.rdRegs[:], addr-0x03F00000, size, val)
	case addr < 0x04002000: // SP memory
		for i := 0; i < size; i++ {
			m.setSPByteAt(addr-0x04000000+uint32(i), byte(val>>(8*uint(size-1-i))))
		}
	case addr < 0x04040000:
		// Unmapped gap.
	case addr < 0x04040020: // SP registers (32-bit accesses)
		if size == 4 {
			m.RSP.WriteRegister(addr-0x04040000, uint32(val))
		}
	case addr >= 0x04080000 && addr < 0x04080004: // SP PC
		if size == 4 {
			m.RSP.WritePC(uint32(val))
		}
	case addr >= 0x04100000 && addr < 0x04100020: // DP registers
		if size == 4 {
			m.DP.WriteRegister(addr-0x04100000, uint32(val))
		}
	case addr >= 0x04300000 && addr < 0x04300010: // MI
		m.writeMI(addr-0x04300000, uint32(val))
	case addr >= 0x04400000 && addr < 0x04400040: // VI
		if size == 4 {
			m.VI.WriteRegister(addr-0x04400000, uint32(val))
		}
	case addr >= 0x04500000 && addr < 0x04500040: // AI
		if size == 4 {
			m.AI.WriteRegister(addr-0x04500000, uint32(val))
		}
	case addr >= 0x04600000 && addr < 0x04600034: // PI
		m.writePI(addr-0x04600000, uint32(val), size)
	case addr >= 0x04700000 && addr < 0x04700020: // RI
		writeBytes(m.riRegs[:], addr-0x04700000, size, val)
	case addr >= 0x04800000 && addr < 0x04800020: // SI
		if size == 4 {
			m.writeSI(addr-0x04800000, uint32(val))
		}
	case addr >= 0x08000000 && addr < 0x08020000: // PI domain 2: save device
		m.saveWrite(addr-0x08000000, size, val)
	case addr >= 0x10000000 && addr < 0x1FC00000:
		// Cartridge ROM is read-only on the bus.
	case addr >= 0x1FC007C0 && addr < 0x1FC00800: // PIF RAM
		writeBytes(m.PIF.RAM[:], addr-0x1FC007C0, size, val)
		m.PIF.AfterWrite()
	}
}

func maskFor(size int) uint64 {
	if size >= 8 {
		return ^uint64(0)
	}
	return (uint64(1) << (8 * uint(size))) - 1
}

// extract32 pulls a big-endian subfield of size bytes at
// byte offset off out of a 32-bit register value.
func extract32(v uint32, off uint32, size int) uint64 {
	if size >= 4 {
		return uint64(v)
	}
	shift := 8 * (3 - int(off) - (size - 1))
	return uint64(v>>uint(shift)) & maskFor(size)
}

// readBytes composes a big-endian value from a byte slice
// at off; bytes past the end read as 0.
func readBytes(b []byte, off uint32, size int) uint64 {
	var v uint64
	for i := 0; i < size; i++ {
		v <<= 8
		if int(off)+i < len(b) {
			v |= uint64(b[int(off)+i])
		}
	}
	return v
}

// writeBytes stores the low size bytes of val big-endian.
func writeBytes(b []byte, off uint32, size int, val uint64) {
	for i := 0; i < size; i++ {
		if int(off)+i < len(b) {
			b[int(off)+i] = byte(val >> (8 * uint(size-1-i)))
		}
	}
}

// readMI reads an MI register by offset.
func (m *Memory) readMI(off uint32) uint32 {
	switch off {
	case 0x00: // MI_MODE
		return m.miMode
	case 0x04: // MI_VERSION
		return 0x02020102
	case 0x08: // MI_INTR
		return m.miIntr
	case 0x0C: // MI_MASK
		return m.miMask
	}
	return 0
}

var miMaskBits = [6]uint32{MIIntrSP, MIIntrSI, MIIntrAI, MIIntrVI, MIIntrPI, MIIntrDP}

// writeMI handles MI register writes. MI_MASK uses the
// documented paired-bit semantics: for each device, an
// even bit clears the mask bit and the following odd bit
// sets it.
func (m *Memory) writeMI(off uint32, val uint32) {
	switch off {
	case 0x00: // MI_MODE (upper bits are DP interrupt clear on hardware; stored here)
		m.miMode = val
		if val&(1<<11) != 0 {
			m.miIntr &^= MIIntrDP
			m.fireMI()
		}
	case 0x04, 0x08:
		// Version and interrupt status are read-only.
	case 0x0C: // MI_MASK
		for i, bit := range miMaskBits {
			if val&(1<<uint(2*i+1)) != 0 {
				m.miMask |= bit
			} else if val&(1<<uint(2*i)) != 0 {
				m.miMask &^= bit
			}
		}
		m.fireMI()
	}
}

// readPI reads a PI register by offset.
func (m *Memory) readPI(off uint32) uint32 {
	switch off {
	case 0x00:
		return m.piDramAddr
	case 0x04:
		return m.piCartAddr
	case 0x08, 0x0C:
		return 0 // length registers read back 0 after transfer
	case 0x10:
		return m.piStatus
	default:
		if off >= 0x14 && off < 0x34 {
			return binary.BigEndian.Uint32(m.piConfig[off-0x14:])
		}
	}
	return 0
}

// writePI handles PI register writes; writing a length
// register starts the DMA immediately (this slice completes
// transfers instantly and raises the PI interrupt).
func (m *Memory) writePI(off uint32, val uint32, size int) {
	switch off {
	case 0x00:
		m.piDramAddr = val & 0xFFFFFF
	case 0x04:
		m.piCartAddr = val
	case 0x08: // PI_RD_LEN: cartridge to RDRAM
		m.piDMA(val, true)
	case 0x0C: // PI_WR_LEN: RDRAM to cartridge
		m.piDMA(val, false)
	case 0x10: // PI_STATUS
		if val&0x2 != 0 {
			m.miIntr &^= MIIntrPI
			m.fireMI()
		}
		if val&0x1 != 0 {
			m.piStatus &^= 0x1 // reset: DMA is never busy in this slice
		}
	default:
		if off >= 0x14 && off < 0x34 && size == 4 {
			binary.BigEndian.PutUint32(m.piConfig[off-0x14:], val)
		}
	}
}

// piDMA performs a PI transfer of (val&0xFFFFFF)+1 bytes,
// rounded up to an even length as the hardware does.
// Transfers whose cartridge address falls in PI domain 2
// (0x08000000) go to the cartridge save device instead
// of the ROM window.
func (m *Memory) piDMA(val uint32, fromCart bool) {
	length := int(val&0xFFFFFF) + 1
	if length&1 != 0 {
		length++
	}
	dram := m.piDramAddr & (uint32(len(m.RDRAM)) - 1)
	cart := m.piCartAddr
	if cart >= 0x08000000 && cart < 0x08020000 {
		m.piSaveDMA(cart-0x08000000, dram, length, fromCart)
		m.miIntr |= MIIntrPI
		m.fireMI()
		return
	}
	if cart >= 0x10000000 {
		cart -= 0x10000000
	}
	for i := 0; i < length; i++ {
		if fromCart {
			m.RDRAM[(int(dram)+i)&(len(m.RDRAM)-1)] = m.romByte(cart + uint32(i))
		}
		// Writes toward the cartridge are discarded: the
		// ROM window is read-only. The transfer still
		// completes and interrupts.
	}
	m.miIntr |= MIIntrPI
	m.fireMI()
}

// piSaveDMA moves bytes between RDRAM and the save
// device window at the given window offset.
func (m *Memory) piSaveDMA(off uint32, dram uint32, length int, fromCart bool) {
	for i := 0; i < length; i++ {
		d := (int(dram) + i) & (len(m.RDRAM) - 1)
		if fromCart {
			m.RDRAM[d] = m.saveReadByte(off + uint32(i))
		} else {
			m.saveWriteByte(off+uint32(i), m.RDRAM[d])
		}
	}
}

// saveReadByte reads one byte of the domain 2 window:
// SRAM is a direct store; Flash answers through its
// command state machine; with no PI-side device fitted
// the window reads as 0.
func (m *Memory) saveReadByte(off uint32) byte {
	switch m.Saves.Type {
	case cart.SaveSRAM:
		return m.Saves.SRAM[off&(cart.SRAMSize-1)]
	case cart.SaveFlash:
		return m.Saves.Flash.LoadByte(off)
	}
	return 0
}

// saveWriteByte writes one byte of the domain 2 window
// (data path only; Flash commands arrive as 32-bit CPU
// writes through saveWrite).
func (m *Memory) saveWriteByte(off uint32, v byte) {
	switch m.Saves.Type {
	case cart.SaveSRAM:
		m.Saves.SRAM[off&(cart.SRAMSize-1)] = v
	case cart.SaveFlash:
		m.Saves.Flash.StoreByte(off, v)
	}
}

// saveRead composes a big-endian read from the window.
func (m *Memory) saveRead(off uint32, size int) uint64 {
	var v uint64
	for i := 0; i < size; i++ {
		v = v<<8 | uint64(m.saveReadByte(off+uint32(i)))
	}
	return v
}

// saveWrite handles a direct CPU write to the window.
// With a Flash device fitted, a 32-bit write whose top
// byte is a command byte is a Flash command (the real
// chip latches commands exactly this way); other writes
// are data.
func (m *Memory) saveWrite(off uint32, size int, val uint64) {
	if m.Saves.Type == cart.SaveFlash {
		m.Saves.Flash.Write(off, size, val)
		return
	}
	for i := 0; i < size; i++ {
		m.saveWriteByte(off+uint32(i), byte(val>>(8*uint(size-1-i))))
	}
}

// SI register offsets (page base 0x04800000).
const (
	siRegDramAddr  = 0x00
	siRegPIFAddrRD = 0x04 // write: start a PIF to RDRAM DMA
	siRegPIFAddrWR = 0x10 // write: start a RDRAM to PIF DMA
	siRegStatus    = 0x1C
	siStatusIntr   = 1 << 12
)

// readSI reads an SI register by offset.
func (m *Memory) readSI(off uint32) uint32 {
	switch off {
	case siRegDramAddr:
		return m.siDramAddr
	case siRegPIFAddrRD, siRegPIFAddrWR:
		return m.siPIFAddr
	case siRegStatus:
		return m.siStatus
	}
	return 0
}

// writeSI handles SI register writes. Writing either
// PIF address register runs a fixed 64-byte DMA between
// RDRAM and PIF RAM and raises the SI interrupt, as
// documented: a read-side DMA first executes the parsed
// Joybus frame in the PIF, a write-side DMA delivers
// the new frame (and any other command byte bits) to
// the PIF for parsing.
func (m *Memory) writeSI(off uint32, val uint32) {
	switch off {
	case siRegDramAddr:
		m.siDramAddr = val & 0xFFFFFF
	case siRegPIFAddrRD:
		m.siPIFAddr = val
		m.PIF.Execute()
		base := int(m.siDramAddr) & (len(m.RDRAM) - 1)
		for i := 0; i < pif.RAMSize; i++ {
			m.RDRAM[(base+i)&(len(m.RDRAM)-1)] = m.PIF.RAM[i]
		}
		m.siDMAComplete()
	case siRegPIFAddrWR:
		m.siPIFAddr = val
		base := int(m.siDramAddr) & (len(m.RDRAM) - 1)
		for i := 0; i < pif.RAMSize; i++ {
			m.PIF.RAM[i] = m.RDRAM[(base+i)&(len(m.RDRAM)-1)]
		}
		m.PIF.AfterWrite()
		m.siDMAComplete()
	case siRegStatus:
		// Any write clears the SI interrupt.
		m.siStatus &^= siStatusIntr
		m.SetMI(MIIntrSI, false)
	}
}

func (m *Memory) siDMAComplete() {
	m.siStatus |= siStatusIntr
	m.SetMI(MIIntrSI, true)
}

// SIState returns the SI registers for save states.
func (m *Memory) SIState() (dramAddr, status uint32) { return m.siDramAddr, m.siStatus }

// RestoreSI sets the SI registers from a save state.
func (m *Memory) RestoreSI(dramAddr, status uint32) {
	m.siDramAddr, m.siStatus = dramAddr, status
}

// spByteAt reads one byte of the SP memory window;
// offsets below 0x1000 are DMEM, the rest IMEM.
func (m *Memory) spByteAt(off uint32) byte {
	if off < 0x1000 {
		return m.RSP.DMEM[off]
	}
	return m.RSP.IMEM[off-0x1000]
}

// setSPByteAt writes one byte of the SP memory window.
func (m *Memory) setSPByteAt(off uint32, v byte) {
	if off < 0x1000 {
		m.RSP.DMEM[off] = v
	} else {
		m.RSP.IMEM[off-0x1000] = v
	}
}

// SPMem returns a snapshot of the SP DMEM (first 4 KB)
// and IMEM (second 4 KB) as one 8 KB slice.
func (m *Memory) SPMem() []byte {
	out := make([]byte, 0x2000)
	copy(out, m.RSP.DMEM[:])
	copy(out[0x1000:], m.RSP.IMEM[:])
	return out
}

// PIFRAM returns the PIF RAM backing store.
func (m *Memory) PIFRAM() []byte { return m.PIF.RAM[:] }
