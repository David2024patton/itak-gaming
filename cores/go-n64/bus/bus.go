// Package bus implements the Nintendo 64 physical memory
// map for the go-n64 core: RDRAM, the RCP register pages
// (SP, DP, MI, VI, AI, PI, RI, SI), the cartridge ROM
// window, and the PIF region. It is original code written
// from the published N64 hardware documentation.
//
// Devices that are later slices (RSP, RDP rendering, PIF
// boot, audio and video output) appear here as register
// storage with their documented reset behavior, so CPU code
// can be developed and tested against the real map now.
// The PI implements its DMA engine for real, including the
// completion interrupt through MI.
package bus

import "encoding/binary"

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

	spMem  [0x2000]byte // SP DMEM + IMEM
	spRegs [0x20]byte
	spPC   uint32
	dpRegs [0x20]byte

	miMode uint32
	miIntr uint32
	miMask uint32

	viRegs [0x40]byte
	aiRegs [0x40]byte

	piDramAddr uint32
	piCartAddr uint32
	piStatus   uint32
	piConfig   [0x34]byte // BSD domain configuration registers

	riRegs [0x20]byte
	siRegs [0x20]byte
	rdRegs [0x40]byte // RDRAM registers
	pifRAM [0x40]byte

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
	return &Memory{RDRAM: make([]byte, size), ROM: rom}
}

// SetROM replaces the cartridge image.
func (m *Memory) SetROM(rom []byte) { m.ROM = rom }

// MIInterrupt reports whether the MI interrupt line is
// asserted (any pending interrupt that is unmasked).
func (m *Memory) MIInterrupt() bool {
	return m.miIntr&m.miMask != 0
}

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
		return readBytes(m.spMem[:], addr-0x04000000, size)
	case addr < 0x04040000:
		return 0
	case addr < 0x04040020: // SP registers
		return readBytes(m.spRegs[:], addr-0x04040000, size)
	case addr >= 0x04080000 && addr < 0x04080004: // SP PC
		if size == 4 {
			return uint64(m.spPC)
		}
		return uint64(uint32(m.spPC)>>(8*(3-(addr&3)))) & maskFor(size)
	case addr >= 0x04100000 && addr < 0x04100020: // DP registers
		return readBytes(m.dpRegs[:], addr-0x04100000, size)
	case addr >= 0x04300000 && addr < 0x04300010: // MI
		return uint64(m.readMI(addr - 0x04300000))
	case addr >= 0x04400000 && addr < 0x04400040: // VI
		return readBytes(m.viRegs[:], addr-0x04400000, size)
	case addr >= 0x04500000 && addr < 0x04500040: // AI
		return readBytes(m.aiRegs[:], addr-0x04500000, size)
	case addr >= 0x04600000 && addr < 0x04600034: // PI
		return uint64(m.readPI(addr - 0x04600000))
	case addr >= 0x04700000 && addr < 0x04700020: // RI
		return readBytes(m.riRegs[:], addr-0x04700000, size)
	case addr >= 0x04800000 && addr < 0x04800020: // SI
		return readBytes(m.siRegs[:], addr-0x04800000, size)
	case addr >= 0x10000000 && addr < 0x1FC00000: // cartridge ROM
		return m.romRead(addr-0x10000000, size)
	case addr >= 0x1FC007C0 && addr < 0x1FC00800: // PIF RAM
		return readBytes(m.pifRAM[:], addr-0x1FC007C0, size)
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
		writeBytes(m.spMem[:], addr-0x04000000, size, val)
	case addr < 0x04040000:
		// Unmapped gap.
	case addr < 0x04040020: // SP registers
		writeBytes(m.spRegs[:], addr-0x04040000, size, val)
	case addr >= 0x04080000 && addr < 0x04080004: // SP PC
		if size == 4 {
			m.spPC = uint32(val) & 0xFFF
		}
	case addr >= 0x04100000 && addr < 0x04100020: // DP registers
		writeBytes(m.dpRegs[:], addr-0x04100000, size, val)
	case addr >= 0x04300000 && addr < 0x04300010: // MI
		m.writeMI(addr-0x04300000, uint32(val))
	case addr >= 0x04400000 && addr < 0x04400040: // VI
		writeBytes(m.viRegs[:], addr-0x04400000, size, val)
	case addr >= 0x04500000 && addr < 0x04500040: // AI
		writeBytes(m.aiRegs[:], addr-0x04500000, size, val)
	case addr >= 0x04600000 && addr < 0x04600034: // PI
		m.writePI(addr-0x04600000, uint32(val), size)
	case addr >= 0x04700000 && addr < 0x04700020: // RI
		writeBytes(m.riRegs[:], addr-0x04700000, size, val)
	case addr >= 0x04800000 && addr < 0x04800020: // SI
		writeBytes(m.siRegs[:], addr-0x04800000, size, val)
	case addr >= 0x10000000 && addr < 0x1FC00000:
		// Cartridge ROM is read-only on the bus.
	case addr >= 0x1FC007C0 && addr < 0x1FC00800: // PIF RAM
		writeBytes(m.pifRAM[:], addr-0x1FC007C0, size, val)
	}
}

func maskFor(size int) uint64 {
	if size >= 8 {
		return ^uint64(0)
	}
	return (uint64(1) << (8 * uint(size))) - 1
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
func (m *Memory) piDMA(val uint32, fromCart bool) {
	length := int(val&0xFFFFFF) + 1
	if length&1 != 0 {
		length++
	}
	dram := m.piDramAddr & (uint32(len(m.RDRAM)) - 1)
	cart := m.piCartAddr
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

// SPMemi returns the SP DMEM/IMEM backing store (used by
// save states and later RSP slices).
func (m *Memory) SPMem() []byte { return m.spMem[:] }

// PIFRAM returns the PIF RAM backing store.
func (m *Memory) PIFRAM() []byte { return m.pifRAM[:] }
