package system

// The machine is the bus IOHook. It dispatches the DS-2 device
// offsets between the video package (DISPSTAT, VCOUNT, and the
// main-memory display FIFO address) and the peripheral set
// (everything else), so neither package imports the other.

// getLanes extracts the unit of the given size at the byte
// lane of off from a 32-bit value.
func getLanes(v uint32, off uint32, size int) uint32 {
	switch size {
	case 1:
		return (v >> ((off & 3) * 8)) & 0xFF
	case 2:
		return (v >> ((off & 2) * 8)) & 0xFFFF
	}
	return v
}

// putLanes merges a written unit into a 32-bit value.
func putLanes(v uint32, off uint32, size int, val uint32) uint32 {
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

// ReadIO implements bus.IOHook.
func (m *Machine) ReadIO(nine bool, off uint32, size int) (uint32, bool) {
	if nine && off >= 0x004 && off < 0x008 {
		word := uint32(m.Video.DISPSTAT()) | uint32(uint16(m.Video.VCount()))<<16
		return getLanes(word, off, size), true
	}
	if nine && off == 0x068 {
		return 0, true // DISP_MMEM_FIFO reads back 0
	}
	if nine && (off == 0x320 || off >= 0x330 && off < 0x3C0 || off >= 0x400 && off < 0x6B0) {
		// RDLINES_COUNT, the RE register block, the GX
		// command ports, and the GX status/result block.
		return m.GX.ReadIO(off, size)
	}
	if !nine && off >= 0x400 && off < 0x520 {
		return m.Audio.ReadIO(off, size)
	}
	return m.Periph.ReadIO(nine, off, size)
}

// WriteIO implements bus.IOHook.
func (m *Machine) WriteIO(nine bool, off uint32, size int, val uint32) bool {
	if nine && off >= 0x004 && off < 0x006 {
		merged := putLanes(uint32(m.Video.DISPSTAT()), off, size, val)
		m.Video.SetDISPSTAT(uint16(merged))
		return true
	}
	if nine && off >= 0x006 && off < 0x008 {
		return true // VCOUNT is read-only
	}
	if nine && off == 0x068 {
		m.Video.SetMainDispAddr(val)
		return true
	}
	if nine && (off == 0x320 || off >= 0x330 && off < 0x3C0 || off >= 0x400 && off < 0x6B0) {
		return m.GX.WriteIO(off, size, val)
	}
	if !nine && off >= 0x400 && off < 0x520 {
		return m.Audio.WriteIO(off, size, val)
	}
	return m.Periph.WriteIO(nine, off, size, val)
}
