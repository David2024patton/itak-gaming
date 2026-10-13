package spu

// The reverb network follows the documented formula
// in the psx-spx SPU chapter: input gains, same side
// and different side reflections, four comb filters,
// two all pass filters, output gains, all in signed
// 16-bit sound RAM with multiplications divided by
// 8000h, running at half the output rate. Register
// indices into s.rev, in document order starting at
// 1F801DC0h.
const (
	rvDAPF1 = iota
	rvDAPF2
	rvVIIR
	rvVCOMB1
	rvVCOMB2
	rvVCOMB3
	rvVCOMB4
	rvVWALL
	rvVAPF1
	rvVAPF2
	rvMLSAME
	rvMRSAME
	rvMLCOMB1
	rvMRCOMB1
	rvMLCOMB2
	rvMRCOMB2
	rvDLSAME
	rvDRSAME
	rvMLDIFF
	rvMRDIFF
	rvMLCOMB3
	rvMRCOMB3
	rvMLCOMB4
	rvMRCOMB4
	rvDLDIFF
	rvDRDIFF
	rvMLAPF1
	rvMRAPF1
	rvMLAPF2
	rvMRAPF2
	rvVLIN
	rvVRIN
)

func (s *SPU) revReg(i int) int32 {
	return int32(int16(s.rev[i]))
}

// revAddr resolves a reverb address register (in
// 8-byte units, relative to the current buffer
// address) to a byte address in sound RAM, wrapped
// inside the work area ESA..7FFFEh.
func (s *SPU) revAddr(rel int32) uint32 {
	esa := int32(s.esa) << 3
	size := int32(RAMSize) - esa
	if size <= 0 {
		return uint32(esa) & (RAMSize - 1)
	}
	a := int32(s.revBuf) + rel
	a = (a-esa)%size + esa
	if a < esa {
		a += size
	}
	return uint32(a)
}

func (s *SPU) revRead(rel int32) int32 {
	a := s.revAddr(rel)
	return int32(int16(uint16(s.RAM[a]) | uint16(s.RAM[a+1])<<8))
}

func (s *SPU) revWrite(rel int32, v int32, store bool) {
	if !store {
		return
	}
	a := s.revAddr(rel)
	v = clamp16(v)
	s.RAM[a] = byte(uint16(int16(v)))
	s.RAM[a+1] = byte(uint16(int16(v)) >> 8)
}

// runReverb performs one 22050 Hz reverb cycle with
// the documented equations. inL/inR are the mixed
// reverb send of the current output sample. When the
// reverb master enable (SPUCNT bit 7) is clear, the
// documented behaviour is that buffer writes become
// reads, so nothing is stored but output is still
// produced from the buffer contents; the SAME/DIFF
// stages are skipped in that case per the document.
func (s *SPU) runReverb(inL, inR int32) {
	enabled := s.spucnt&0x80 != 0
	store := enabled
	lin := mul15(s.revReg(rvVLIN), inL)
	rin := mul15(s.revReg(rvVRIN), inR)
	if enabled {
		// Same side reflections.
		mL := int32(s.rev[rvMLSAME]) << 3
		mR := int32(s.rev[rvMRSAME]) << 3
		prevL := s.revRead(mL - 2)
		prevR := s.revRead(mR - 2)
		nL := mul15(s.revReg(rvVIIR), lin+mul15(s.revReg(rvVWALL), s.revRead(int32(s.rev[rvDLSAME])<<3))-prevL) + prevL
		nR := mul15(s.revReg(rvVIIR), rin+mul15(s.revReg(rvVWALL), s.revRead(int32(s.rev[rvDRSAME])<<3))-prevR) + prevR
		s.revWrite(mL, nL, store)
		s.revWrite(mR, nR, store)
		// Different side reflections.
		mLd := int32(s.rev[rvMLDIFF]) << 3
		mRd := int32(s.rev[rvMRDIFF]) << 3
		prevLd := s.revRead(mLd - 2)
		prevRd := s.revRead(mRd - 2)
		nLd := mul15(s.revReg(rvVIIR), lin+mul15(s.revReg(rvVWALL), s.revRead(int32(s.rev[rvDRDIFF])<<3))-prevLd) + prevLd
		nRd := mul15(s.revReg(rvVIIR), rin+mul15(s.revReg(rvVWALL), s.revRead(int32(s.rev[rvDLDIFF])<<3))-prevRd) + prevRd
		s.revWrite(mLd, nLd, store)
		s.revWrite(mRd, nRd, store)
	}
	// Early echo: comb filters.
	lout := mul15(s.revReg(rvVCOMB1), s.revRead(int32(s.rev[rvMLCOMB1])<<3)) +
		mul15(s.revReg(rvVCOMB2), s.revRead(int32(s.rev[rvMLCOMB2])<<3)) +
		mul15(s.revReg(rvVCOMB3), s.revRead(int32(s.rev[rvMLCOMB3])<<3)) +
		mul15(s.revReg(rvVCOMB4), s.revRead(int32(s.rev[rvMLCOMB4])<<3))
	rout := mul15(s.revReg(rvVCOMB1), s.revRead(int32(s.rev[rvMRCOMB1])<<3)) +
		mul15(s.revReg(rvVCOMB2), s.revRead(int32(s.rev[rvMRCOMB2])<<3)) +
		mul15(s.revReg(rvVCOMB3), s.revRead(int32(s.rev[rvMRCOMB3])<<3)) +
		mul15(s.revReg(rvVCOMB4), s.revRead(int32(s.rev[rvMRCOMB4])<<3))
	// Late reverb APF1 and APF2.
	lout = s.runAPF(lout, int32(s.rev[rvMLAPF1])<<3, int32(s.rev[rvDAPF1])<<3, s.revReg(rvVAPF1), store)
	rout = s.runAPF(rout, int32(s.rev[rvMRAPF1])<<3, int32(s.rev[rvDAPF1])<<3, s.revReg(rvVAPF1), store)
	lout = s.runAPF(lout, int32(s.rev[rvMLAPF2])<<3, int32(s.rev[rvDAPF2])<<3, s.revReg(rvVAPF2), store)
	rout = s.runAPF(rout, int32(s.rev[rvMRAPF2])<<3, int32(s.rev[rvDAPF2])<<3, s.revReg(rvVAPF2), store)
	s.revOutL = mul15(lout, s.evolL)
	s.revOutR = mul15(rout, s.evolR)
	// Advance the buffer address.
	nb := (s.revBuf + 2) & 0x7FFFE
	if nb < uint32(s.esa)<<3 {
		nb = uint32(s.esa) << 3
	}
	s.revBuf = nb
}

// runAPF performs one documented all pass stage:
// out = in - v*[addr-disp]; [addr] = out;
// out = out*v + [addr-disp] (the old value).
func (s *SPU) runAPF(in, addr, disp, v int32, store bool) int32 {
	delayed := s.revRead(addr - disp)
	out := in - mul15(v, delayed)
	s.revWrite(addr, out, store)
	return mul15(out, v) + delayed
}
