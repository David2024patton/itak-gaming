// Package spu is the PlayStation SPU as far as
// PS1-2 needs it: the 512 KB sound RAM and the data
// transfer port that DMA channel 4 moves words
// through. The 24 voices, ADSR envelopes and reverb
// are PS1-3; nothing here makes sound.
//
// The transfer address register (0x1F801DA4) counts
// 4-byte units in this implementation, so the byte
// address inside sound RAM is the register value
// shifted left by 2, wrapping at 512 KB. The data
// port (0x1F801DA6) reads or writes one halfword at
// the current address and advances it by 2 bytes;
// DMA word accesses perform two halfword accesses,
// low halfword first, matching the hardware port.
package spu

// RAMSize is the sound RAM size in bytes.
const RAMSize = 512 * 1024

// SPU is the sound RAM and its transfer port state.
type SPU struct {
	RAM     [RAMSize]byte
	AddrReg uint16 // last value written to 0x1F801DA4
	addr    uint32 // current byte address inside RAM
}

// New returns an SPU in reset state.
func New() *SPU { return &SPU{} }

// Reset clears sound RAM and the transfer address.
func (s *SPU) Reset() {
	s.RAM = [RAMSize]byte{}
	s.AddrReg = 0
	s.addr = 0
}

// SetAddress writes the transfer address register and
// loads the internal byte address.
func (s *SPU) SetAddress(v uint16) {
	s.AddrReg = v
	s.addr = (uint32(v) << 2) & (RAMSize - 1)
}

// Address returns the current byte address.
func (s *SPU) Address() uint32 { return s.addr }

// ReadData reads the halfword at the transfer
// address and advances it.
func (s *SPU) ReadData() uint16 {
	v := uint16(s.RAM[s.addr]) | uint16(s.RAM[s.addr+1])<<8
	s.addr = (s.addr + 2) & (RAMSize - 1)
	return v
}

// WriteData writes a halfword at the transfer
// address and advances it.
func (s *SPU) WriteData(v uint16) {
	s.RAM[s.addr] = byte(v)
	s.RAM[s.addr+1] = byte(v >> 8)
	s.addr = (s.addr + 2) & (RAMSize - 1)
}

// ReadWord performs a DMA word read (two halfword
// port accesses, low first).
func (s *SPU) ReadWord() uint32 {
	lo := s.ReadData()
	hi := s.ReadData()
	return uint32(lo) | uint32(hi)<<16
}

// WriteWord performs a DMA word write.
func (s *SPU) WriteWord(w uint32) {
	s.WriteData(uint16(w))
	s.WriteData(uint16(w >> 16))
}

// StateSize is the serialized size: the sound RAM
// plus the transfer address state.
const StateSize = RAMSize + 8

// Serialize writes the SPU state.
func (s *SPU) Serialize() []byte {
	out := make([]byte, StateSize)
	copy(out, s.RAM[:])
	out[RAMSize] = byte(s.AddrReg)
	out[RAMSize+1] = byte(s.AddrReg >> 8)
	out[RAMSize+2] = byte(s.addr)
	out[RAMSize+3] = byte(s.addr >> 8)
	out[RAMSize+4] = byte(s.addr >> 16)
	return out
}

// Deserialize restores a state produced by Serialize.
func (s *SPU) Deserialize(in []byte) {
	copy(s.RAM[:], in[:RAMSize])
	s.AddrReg = uint16(in[RAMSize]) | uint16(in[RAMSize+1])<<8
	s.addr = uint32(in[RAMSize+2]) | uint32(in[RAMSize+3])<<8 | uint32(in[RAMSize+4])<<16
	s.addr &= RAMSize - 1
}
