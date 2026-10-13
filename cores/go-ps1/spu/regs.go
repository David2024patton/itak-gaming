package spu

// Register interface over 1F801C00h..1F801DFFh.
// The DAx block follows the psx-spx documented map:
// 1F801DA2h ESA, 1F801DA4h IRQA, 1F801DA6h TSA,
// 1F801DA8h DATA, 1F801DAAh ATTR/SPUCNT, 1F801DACh
// RAM transfer control, 1F801DAEh STATX.

// ReadReg reads size bytes at an absolute SPU
// address. Size 2 is one halfword access, size 4 is
// two halfword accesses (low at addr, high at
// addr+2). Size 1 and out of window addresses are
// not claimed.
func (s *SPU) ReadReg(addr uint32, size int) (uint32, bool) {
	if size == 2 {
		v, ok := s.readHalf(addr)
		return uint32(v), ok
	}
	if size == 4 {
		lo, ok1 := s.readHalf(addr)
		hi, ok2 := s.readHalf(addr + 2)
		if !ok1 || !ok2 {
			return 0, false
		}
		return uint32(lo) | uint32(hi)<<16, true
	}
	return 0, false
}

// WriteReg writes size bytes at an absolute SPU
// address, with the same size rules as ReadReg.
func (s *SPU) WriteReg(addr uint32, size int, v uint32) bool {
	if size == 2 {
		return s.writeHalf(addr, uint16(v))
	}
	if size == 4 {
		ok1 := s.writeHalf(addr, uint16(v))
		ok2 := s.writeHalf(addr+2, uint16(v>>16))
		return ok1 && ok2
	}
	return false
}

func inWindow(addr uint32) bool {
	return addr >= regBase && addr <= regTop && addr&1 == 0
}

func (s *SPU) readHalf(addr uint32) (uint16, bool) {
	if !inWindow(addr) {
		return 0, false
	}
	switch addr {
	case 0x1F801DA4: // IRQA
		return s.irqAddr, true
	case 0x1F801DA6: // TSA (stored value; does not increment)
		return s.AddrReg, true
	case 0x1F801DA8: // DATA port
		return s.ReadData(), true
	case 0x1F801DAE:
		return s.statusReg(), true
	case 0x1F801D9C:
		return uint16(s.endx), true
	case 0x1F801D9E:
		return uint16(s.endx >> 16), true
	case 0x1F801DB8:
		return uint16(s.mainCurL), true
	case 0x1F801DBA:
		return uint16(s.mainCurR), true
	}
	if addr >= 0x1F801C00 && addr < 0x1F801D80 {
		n := int(addr-0x1F801C00) >> 4
		v := &s.voices[n]
		switch (addr - 0x1F801C00) & 0xF {
		case 0x0:
			return v.volLRaw, true
		case 0x2:
			return v.volRRaw, true
		case 0x4:
			return v.pitch, true
		case 0x6:
			return v.ssa, true
		case 0x8:
			return v.adsrLo, true
		case 0xA:
			return v.adsrHi, true
		case 0xC:
			return uint16(v.envLevel), true
		case 0xE:
			return v.lsax, true
		}
	}
	return s.shadow[(addr-regBase)/2], true
}

func (s *SPU) statusReg() uint16 {
	st := uint16(s.spucnt & 0x3F)
	if s.spucnt&0x20 != 0 {
		st |= 0x80 // DMA request, mirrors SPUCNT bit 5 per the document
	}
	if s.irqFlag {
		st |= 0x40
	}
	if (s.totalSamples & 0x100) != 0 {
		st |= 0x800 // writing to second half of capture buffers
	}
	return st
}

func (s *SPU) writeHalf(addr uint32, val uint16) bool {
	if !inWindow(addr) {
		return false
	}
	s.shadow[(addr-regBase)/2] = val
	switch {
	case addr >= 0x1F801C00 && addr < 0x1F801D80:
		n := int(addr-0x1F801C00) >> 4
		v := &s.voices[n]
		switch (addr - 0x1F801C00) & 0xF {
		case 0x0:
			v.volLRaw = val
			v.curVolL, v.sweepOnL = applyVolumeWrite(v.curVolL, val)
		case 0x2:
			v.volRRaw = val
			v.curVolR, v.sweepOnR = applyVolumeWrite(v.curVolR, val)
		case 0x4:
			v.pitch = val
		case 0x6:
			v.ssa = val
		case 0x8:
			v.adsrLo = val
		case 0xA:
			v.adsrHi = val
		case 0xC:
			v.envLevel = int32(int16(val))
		case 0xE:
			v.lsax = val
			v.repeatWritten = true
		}
		return true
	case addr >= 0x1F801DC0 && addr <= 0x1F801DFE:
		s.rev[(addr-0x1F801DC0)/2] = val
		return true
	}
	switch addr {
	case 0x1F801D80:
		s.mainRawL = val
		s.mainCurL, s.mainSweepOnL = applyMainVolumeWrite(s.mainCurL, val)
	case 0x1F801D82:
		s.mainRawR = val
		s.mainCurR, s.mainSweepOnR = applyMainVolumeWrite(s.mainCurR, val)
	case 0x1F801D84:
		s.evolL = int32(int16(val))
	case 0x1F801D86:
		s.evolR = int32(int16(val))
	case 0x1F801D88:
		for i := 0; i < 16; i++ {
			if val>>uint(i)&1 != 0 {
				s.keyOn(i)
			}
		}
	case 0x1F801D8A:
		for i := 0; i < 8; i++ {
			if val>>uint(i)&1 != 0 {
				s.keyOn(16 + i)
			}
		}
	case 0x1F801D8C:
		for i := 0; i < 16; i++ {
			if val>>uint(i)&1 != 0 {
				s.keyOff(i)
			}
		}
	case 0x1F801D8E:
		for i := 0; i < 8; i++ {
			if val>>uint(i)&1 != 0 {
				s.keyOff(16 + i)
			}
		}
	case 0x1F801D90:
		s.pmon = (s.pmon & 0xFFFF0000) | uint32(val)
	case 0x1F801D92:
		s.pmon = (s.pmon & 0xFFFF) | uint32(val)<<16
		s.pmon &= 0xFFFFFF
	case 0x1F801D94:
		s.non = (s.non & 0xFFFF0000) | uint32(val)
	case 0x1F801D96:
		s.non = (s.non & 0xFFFF) | uint32(val)<<16
		s.non &= 0xFFFFFF
	case 0x1F801D98:
		s.eon = (s.eon & 0xFFFF0000) | uint32(val)
	case 0x1F801D9A:
		s.eon = (s.eon & 0xFFFF) | uint32(val)<<16
		s.eon &= 0xFFFFFF
	case 0x1F801DA2:
		s.esa = val
		s.revBuf = uint32(val) << 3
	case 0x1F801DA4: // IRQA
		s.irqAddr = val
	case 0x1F801DA6: // TSA
		s.SetAddress(val)
	case 0x1F801DA8: // DATA port
		s.WriteData(val)
	case 0x1F801DAA:
		s.spucnt = val
		if val&0x40 == 0 {
			s.irqFlag = false // acknowledge / disable
		}
	case 0x1F801DAC:
		s.ramCtrl = val
	case 0x1F801DB0:
		s.cdVolL = int32(int16(val))
	case 0x1F801DB2:
		s.cdVolR = int32(int16(val))
	case 0x1F801DB4:
		s.extVolL = int32(int16(val))
	case 0x1F801DB6:
		s.extVolR = int32(int16(val))
	}
	return true
}

// applyVolumeWrite handles a voice volume register
// write: direct mode (bit15=0) sets the current
// volume immediately, sweep mode (bit15=1) keeps the
// current volume and starts sweeping from it.
func applyVolumeWrite(cur int32, raw uint16) (int32, bool) {
	if raw&0x8000 == 0 {
		v := int32(raw & 0x7FFF)
		if v >= 0x4000 {
			v -= 0x8000
		}
		return v * 2, false
	}
	return cur, true
}

// applyMainVolumeWrite is the main volume variant:
// the main registers carry a plain signed 16-bit
// volume in direct mode (0x7FFF is full, 0x3FFF is
// half), and a sweep parameter set in sweep mode.
func applyMainVolumeWrite(cur int32, raw uint16) (int32, bool) {
	if raw&0x8000 == 0 {
		return int32(int16(raw)), false
	}
	return cur, true
}
