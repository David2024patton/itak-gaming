package spu

import "encoding/binary"

// Serialized layout (see the package comment):
// RAM, the original 8-byte transfer tail, then the
// register shadow (512 bytes), a global scalar block
// (192 bytes, 48 little endian uint32 slots), the
// reverb registers (64 bytes), per-voice blocks
// (voiceStateSize bytes each) and the pending audio
// buffer (audioStateCap int16 values; its length is
// global slot 31).

const (
	shadowStateSize = 512
	globalStateSize = 192
	revStateSize    = 64
	voiceStateSize  = 138
	audioStateCap   = 16384
	extraStateSize  = shadowStateSize + globalStateSize + revStateSize +
		numVoices*voiceStateSize + audioStateCap*2
)

// StateSize is the serialized size: the sound RAM,
// the transfer address state, and all chip state
// added in PS1-3.
const StateSize = RAMSize + 8 + extraStateSize

// Serialize writes the SPU state.
func (s *SPU) Serialize() []byte {
	out := make([]byte, StateSize)
	copy(out, s.RAM[:])
	off := RAMSize
	out[off] = byte(s.AddrReg)
	out[off+1] = byte(s.AddrReg >> 8)
	out[off+2] = byte(s.addr)
	out[off+3] = byte(s.addr >> 8)
	out[off+4] = byte(s.addr >> 16)
	off += 8
	for i := 0; i < 256; i++ {
		binary.LittleEndian.PutUint16(out[off+i*2:], s.shadow[i])
	}
	off += shadowStateSize
	g := func(slot int, v uint32) {
		binary.LittleEndian.PutUint32(out[off+slot*4:], v)
	}
	audioLen := len(s.audio)
	if audioLen > audioStateCap {
		audioLen = audioStateCap
	}
	g(0, s.pmon)
	g(1, s.non)
	g(2, s.eon)
	g(3, s.endx)
	g(4, uint32(s.esa))
	g(5, uint32(s.irqAddr))
	g(6, uint32(s.spucnt))
	g(7, uint32(s.ramCtrl))
	g(8, uint32(s.cdVolL))
	g(9, uint32(s.cdVolR))
	g(10, uint32(s.extVolL))
	g(11, uint32(s.extVolR))
	g(12, uint32(s.evolL))
	g(13, uint32(s.evolR))
	g(14, uint32(s.mainCurL))
	g(15, uint32(s.mainCurR))
	g(16, uint32(s.mainSweepCntL))
	g(17, uint32(s.mainSweepCntR))
	if s.mainSweepOnL {
		g(18, 1)
	}
	if s.mainSweepOnR {
		g(18, binary.LittleEndian.Uint32(out[off+18*4:])|2)
	}
	g(19, s.revBuf)
	g(20, uint32(s.revOutL))
	g(21, uint32(s.revOutR))
	if s.revPhase {
		g(22, 1)
	}
	g(23, uint32(s.noiseLevel))
	g(24, uint32(s.noiseTimer))
	g(25, uint32(s.cycleAcc))
	g(26, uint32(s.totalSamples))
	g(27, uint32(s.totalSamples>>32))
	if s.irqFlag {
		g(28, 1)
	}
	g(29, uint32(s.mainRawL))
	g(30, uint32(s.mainRawR))
	g(31, uint32(audioLen))
	off += globalStateSize
	for i := 0; i < 32; i++ {
		binary.LittleEndian.PutUint16(out[off+i*2:], s.rev[i])
	}
	off += revStateSize
	for n := 0; n < numVoices; n++ {
		off = s.serializeVoice(out, off, n)
	}
	for i := 0; i < audioLen; i++ {
		binary.LittleEndian.PutUint16(out[off+i*2:], uint16(s.audio[i]))
	}
	return out
}

func (s *SPU) serializeVoice(out []byte, off int, n int) int {
	v := &s.voices[n]
	u16 := func(vv uint16) {
		binary.LittleEndian.PutUint16(out[off:], vv)
		off += 2
	}
	u32 := func(vv uint32) {
		binary.LittleEndian.PutUint32(out[off:], vv)
		off += 4
	}
	u16(v.volLRaw)
	u16(v.volRRaw)
	u16(v.pitch)
	u16(v.ssa)
	u16(v.adsrLo)
	u16(v.adsrHi)
	u16(v.lsax)
	u32(uint32(v.curVolL))
	u32(uint32(v.curVolR))
	u32(uint32(v.sweepCntL))
	u32(uint32(v.sweepCntR))
	u32(uint32(v.envLevel))
	u32(uint32(v.envCounter))
	flags := uint32(v.phase)
	if v.sweepOnL {
		flags |= 0x10
	}
	if v.sweepOnR {
		flags |= 0x20
	}
	if v.active {
		flags |= 0x40
	}
	if v.repeatWritten {
		flags |= 0x80
	}
	if v.blockLoaded {
		flags |= 0x100
	}
	u32(flags)
	u32(uint32(v.konDelay))
	u32(v.addr)
	u32(v.pitchCounter)
	u32(uint32(v.decOld))
	u32(uint32(v.decOlder))
	u32(uint32(v.pos))
	u32(uint32(v.blockFlags))
	for i := 0; i < 28; i++ {
		u16(uint16(v.block[i]))
	}
	for i := 0; i < 4; i++ {
		u16(uint16(int16(v.hist[i])))
	}
	u32(uint32(v.outx))
	return off
}

// Deserialize restores a state produced by Serialize.
// The input length must match StateSize exactly; any
// other length is ignored.
func (s *SPU) Deserialize(in []byte) {
	if len(in) != StateSize {
		return
	}
	copy(s.RAM[:], in[:RAMSize])
	off := RAMSize
	s.AddrReg = uint16(in[off]) | uint16(in[off+1])<<8
	s.addr = uint32(in[off+2]) | uint32(in[off+3])<<8 | uint32(in[off+4])<<16
	s.addr &= RAMSize - 1
	off += 8
	for i := 0; i < 256; i++ {
		s.shadow[i] = binary.LittleEndian.Uint16(in[off+i*2:])
	}
	off += shadowStateSize
	g := func(slot int) uint32 {
		return binary.LittleEndian.Uint32(in[off+slot*4:])
	}
	s.pmon = g(0)
	s.non = g(1)
	s.eon = g(2)
	s.endx = g(3)
	s.esa = uint16(g(4))
	s.irqAddr = uint16(g(5))
	s.spucnt = uint16(g(6))
	s.ramCtrl = uint16(g(7))
	s.cdVolL = int32(g(8))
	s.cdVolR = int32(g(9))
	s.extVolL = int32(g(10))
	s.extVolR = int32(g(11))
	s.evolL = int32(g(12))
	s.evolR = int32(g(13))
	s.mainCurL = int32(g(14))
	s.mainCurR = int32(g(15))
	s.mainSweepCntL = int32(g(16))
	s.mainSweepCntR = int32(g(17))
	s.mainSweepOnL = g(18)&1 != 0
	s.mainSweepOnR = g(18)&2 != 0
	s.revBuf = g(19)
	s.revOutL = int32(g(20))
	s.revOutR = int32(g(21))
	s.revPhase = g(22) != 0
	s.noiseLevel = int32(g(23))
	s.noiseTimer = int32(g(24))
	s.cycleAcc = int64(g(25))
	s.totalSamples = uint64(g(26)) | uint64(g(27))<<32
	s.irqFlag = g(28) != 0
	s.mainRawL = uint16(g(29))
	s.mainRawR = uint16(g(30))
	audioLen := int(g(31))
	if audioLen > audioStateCap {
		audioLen = audioStateCap
	}
	off += globalStateSize
	for i := 0; i < 32; i++ {
		s.rev[i] = binary.LittleEndian.Uint16(in[off+i*2:])
	}
	off += revStateSize
	for n := 0; n < numVoices; n++ {
		off = s.deserializeVoice(in, off, n)
	}
	s.audio = make([]int16, audioLen)
	for i := 0; i < audioLen; i++ {
		s.audio[i] = int16(binary.LittleEndian.Uint16(in[off+i*2:]))
	}
}

func (s *SPU) deserializeVoice(in []byte, off int, n int) int {
	v := &s.voices[n]
	u16 := func() uint16 {
		vv := binary.LittleEndian.Uint16(in[off:])
		off += 2
		return vv
	}
	u32 := func() uint32 {
		vv := binary.LittleEndian.Uint32(in[off:])
		off += 4
		return vv
	}
	v.volLRaw = u16()
	v.volRRaw = u16()
	v.pitch = u16()
	v.ssa = u16()
	v.adsrLo = u16()
	v.adsrHi = u16()
	v.lsax = u16()
	v.curVolL = int32(u32())
	v.curVolR = int32(u32())
	v.sweepCntL = int32(u32())
	v.sweepCntR = int32(u32())
	v.envLevel = int32(u32())
	v.envCounter = int32(u32())
	flags := u32()
	v.phase = int(flags & 0xF)
	v.sweepOnL = flags&0x10 != 0
	v.sweepOnR = flags&0x20 != 0
	v.active = flags&0x40 != 0
	v.repeatWritten = flags&0x80 != 0
	v.blockLoaded = flags&0x100 != 0
	v.konDelay = int(int32(u32()))
	v.addr = u32()
	v.pitchCounter = u32()
	v.decOld = int32(u32())
	v.decOlder = int32(u32())
	v.pos = int(u32())
	v.blockFlags = uint8(u32())
	for i := 0; i < 28; i++ {
		v.block[i] = int16(u16())
	}
	for i := 0; i < 4; i++ {
		v.hist[i] = int32(int16(u16()))
	}
	v.outx = int32(u32())
	return off
}
