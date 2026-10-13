package system

// The peripheral multiplexer: the Machine implements
// bus.IODevice, claiming the behavioral register
// windows inside the bus register page, and the DMA
// device endpoints. Addresses not claimed here fall
// back to the bus's stored stub page (pad registers,
// SPU voice registers and the like stay storage in
// this slice, as the README states).

// byteMask returns the mask of value bytes an access
// of the given size covers.
func byteMask(size int) uint32 {
	switch size {
	case 1:
		return 0xFF
	case 2:
		return 0xFFFF
	}
	return 0xFFFFFFFF
}

// ReadIO implements bus.IODevice.
func (m *Machine) ReadIO(addr uint32, size int) (uint32, bool) {
	switch {
	case addr == 0x1F801070:
		return uint32(m.istat), true
	case addr == 0x1F801074:
		return uint32(m.imask), true
	case addr == 0x1F801810:
		return m.GPU.ReadData(), true
	case addr == 0x1F801814:
		return m.GPU.Status(), true
	case addr >= 0x1F801100 && addr < 0x1F801130:
		reg := addr - 0x1F801100
		n := int(reg >> 4)
		switch reg & 0xF {
		case 0:
			return uint32(m.Timers.T[n].Counter), true
		case 4:
			// Reading the mode register clears its
			// reached flags (documented).
			return uint32(m.Timers.ReadMode(n)), true
		case 8:
			return uint32(m.Timers.T[n].Target), true
		}
		return 0, true
	case addr >= 0x1F801080 && addr < 0x1F801100:
		return m.DMA.ReadReg(addr &^ 3), true
	case addr >= 0x1F801800 && addr <= 0x1F801803:
		return uint32(m.CD.ReadReg(addr - 0x1F801800)), true
	case addr == 0x1F801DA4:
		return uint32(m.SPU.AddrReg), true
	case addr == 0x1F801DA6:
		if size == 1 {
			return 0, false
		}
		return uint32(m.SPU.ReadData()), true
	}
	return 0, false
}

// WriteIO implements bus.IODevice.
func (m *Machine) WriteIO(addr uint32, size int, v uint32) bool {
	switch {
	case addr == 0x1F801070:
		// Write-to-acknowledge: bits written as 0
		// clear, bits written as 1 keep their state.
		keep := v | (^byteMask(size) & 0xFFFF)
		m.istat &= uint16(keep)
		m.updateIRQ()
		return true
	case addr == 0x1F801074:
		mask := byteMask(size)
		m.imask = uint16((uint32(m.imask) &^ mask) | (v & mask))
		m.updateIRQ()
		return true
	case addr == 0x1F801810:
		if size == 4 {
			m.GPU.WriteGP0(v)
			if m.GPU.IRQRequested() && !m.gpuIRQPrev {
				m.istat |= 1 << IRQGPU
				m.gpuIRQPrev = true
				m.updateIRQ()
			}
		}
		return true
	case addr == 0x1F801814:
		if size == 4 {
			m.GPU.WriteGP1(v)
		}
		return true
	case addr >= 0x1F801100 && addr < 0x1F801130:
		reg := addr - 0x1F801100
		n := int(reg >> 4)
		switch reg & 0xF {
		case 0:
			m.Timers.WriteCounter(n, uint16(v))
		case 4:
			m.Timers.WriteMode(n, uint16(v))
		case 8:
			m.Timers.WriteTarget(n, uint16(v))
		}
		return true
	case addr >= 0x1F801080 && addr < 0x1F801100:
		if size != 4 {
			return false // DMA registers are word-only
		}
		m.DMA.WriteReg(addr&^3, v)
		return true
	case addr >= 0x1F801800 && addr <= 0x1F801803:
		m.CD.WriteReg(addr-0x1F801800, uint8(v))
		return true
	case addr == 0x1F801DA4:
		m.SPU.SetAddress(uint16(v))
		return true
	case addr == 0x1F801DA6:
		if size == 1 {
			return false
		}
		m.SPU.WriteData(uint16(v))
		if size == 4 {
			m.SPU.WriteData(uint16(v >> 16))
		}
		return true
	}
	return false
}

// ----------------------------------------------------------------
// DMA device endpoints (dma.Devices). The MDEC
// endpoints are stubs: MDEC-in sinks words into a
// count and checksum the gates can inspect, MDEC-out
// sources zeros. The codec is PS1-3.
// ----------------------------------------------------------------

// GP0Write implements dma.Devices.
func (m *Machine) GP0Write(w uint32) { m.GPU.WriteGP0(w) }

// GPURead implements dma.Devices.
func (m *Machine) GPURead() uint32 { return m.GPU.ReadData() }

// CDReadWord implements dma.Devices.
func (m *Machine) CDReadWord() (uint32, bool) { return m.CD.ReadWord() }

// SPUReadWord implements dma.Devices.
func (m *Machine) SPUReadWord() uint32 { return m.SPU.ReadWord() }

// SPUWriteWord implements dma.Devices.
func (m *Machine) SPUWriteWord(w uint32) { m.SPU.WriteWord(w) }

// MDECInWrite implements dma.Devices.
func (m *Machine) MDECInWrite(w uint32) {
	m.mdecCount++
	m.mdecSum = m.mdecSum*31 + w
}

// MDECOutRead implements dma.Devices.
func (m *Machine) MDECOutRead() uint32 { return 0 }

// MDECSink returns the MDEC-in stub's word count and
// checksum (test and diagnostics surface).
func (m *Machine) MDECSink() (uint32, uint32) { return m.mdecCount, m.mdecSum }
