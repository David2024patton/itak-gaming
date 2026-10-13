package scsp

import (
	"encoding/binary"
)

// Serialized layout, version 1, big endian. Sound RAM and the
// register page are NOT included; the system serializes those
// separately. The pending audio buffer is not included either;
// callers drain AudioBuffer before saving for a bit exact
// continuation.
//
//	bytes 0 to 3: magic "SCSP"
//	byte 4: version (1)
//	byte 5: flags: bit 0 sound on
//	bytes 6 to 13: sample phase accumulator (SH-2 cycle units)
//	bytes 14 to 22: timers: per timer the preset, counter and
//	  prescaler phase, 3 bytes each
//	bytes 23 to 24: sound CPU pending mask
//	bytes 25 to 26: main CPU pending mask
//	bytes 27 to 28: main bits already signalled
//	byte 29: DMA flags: bit 0 active, bit 1 register to RAM,
//	  bit 2 DGATE
//	bytes 30 to 33: DMA words remaining
//	bytes 34 to 37: DMA RAM word address
//	bytes 38 to 41: DMA register word address
//	then 32 slot records of 32 bytes each:
//	  phase (8), EG level Q8 (4), EGHOLD ticks left (4),
//	  LFO phase (4), noise LFSR (4), previous output (4),
//	  EG phase (1), active (1), direction sign (1), spare (1)
//	then the 88 byte m68ec000 state
//	then the DSP record:
//	  accumulator (4), memory phase (4), sample count (8),
//	  TEMP base (4), last memory read (4), TEMP 128 words of
//	  4 bytes, MEMS 32, MIXS 16, EFREG 16, EXTS 2
//	then the CD queue: a 4 byte count followed by that many
//	  big endian int16 samples
const stateVersion = 1

const (
	stateHeader  = 42
	slotRecSize  = 32
	dspFixedSize = 4 + 4 + 8 + 4 + 4 + 128*4 + 32*4 + 16*4 + 16*4 + 2*4
	// SerializeBaseSize is the serialized size with an empty CD
	// queue.
	SerializeBaseSize = stateHeader + 32*slotRecSize + 88 + dspFixedSize + 4
)

// Serialize returns the chip state in the layout above.
func (s *SCSP) Serialize() []byte {
	out := make([]byte, SerializeBaseSize+len(s.cdQueue)*2)
	copy(out[0:4], "SCSP")
	out[4] = stateVersion
	if s.on {
		out[5] |= 1
	}
	binary.BigEndian.PutUint64(out[6:14], uint64(s.acc))
	for i := 0; i < 3; i++ {
		out[14+i*3] = s.timers[i].preset
		out[15+i*3] = s.timers[i].counter
		out[16+i*3] = byte(s.timers[i].phase)
	}
	binary.BigEndian.PutUint16(out[23:25], s.sciPend)
	binary.BigEndian.PutUint16(out[25:27], s.mciPend)
	binary.BigEndian.PutUint16(out[27:29], s.mainSent)
	if s.dma.active {
		out[29] |= 1
	}
	if s.dma.dirRegToRAM {
		out[29] |= 2
	}
	if s.dma.gate {
		out[29] |= 4
	}
	binary.BigEndian.PutUint32(out[30:34], uint32(s.dma.remaining))
	binary.BigEndian.PutUint32(out[34:38], s.dma.ramWord)
	binary.BigEndian.PutUint32(out[38:42], s.dma.regWord)

	off := stateHeader
	for i := 0; i < 32; i++ {
		st := &s.slots[i]
		rec := out[off : off+slotRecSize]
		binary.BigEndian.PutUint64(rec[0:8], uint64(st.phase))
		binary.BigEndian.PutUint32(rec[8:12], uint32(st.egLevel))
		binary.BigEndian.PutUint32(rec[12:16], uint32(st.egHold))
		binary.BigEndian.PutUint32(rec[16:20], st.lfoPhase)
		binary.BigEndian.PutUint32(rec[20:24], st.lfsr)
		binary.BigEndian.PutUint32(rec[24:28], uint32(st.prevOut))
		rec[28] = byte(st.egPhase)
		if st.active {
			rec[29] = 1
		}
		if st.dir < 0 {
			rec[30] = 1
		}
		off += slotRecSize
	}
	copy(out[off:off+88], s.cpu.Serialize())
	off += 88

	d := &s.dsp
	binary.BigEndian.PutUint32(out[off:off+4], uint32(d.acc))
	binary.BigEndian.PutUint32(out[off+4:off+8], d.memPhase)
	binary.BigEndian.PutUint64(out[off+8:off+16], d.samples)
	binary.BigEndian.PutUint32(out[off+16:off+20], uint32(d.tempBase))
	binary.BigEndian.PutUint32(out[off+20:off+24], uint32(d.memrd))
	p := off + 24
	put32s := func(vals []int32) {
		for _, v := range vals {
			binary.BigEndian.PutUint32(out[p:p+4], uint32(v))
			p += 4
		}
	}
	put32s(d.temp[:])
	put32s(d.mems[:])
	put32s(d.mixs[:])
	put32s(d.efreg[:])
	put32s(d.exts[:])
	off = p

	binary.BigEndian.PutUint32(out[off:off+4], uint32(len(s.cdQueue)))
	for i, v := range s.cdQueue {
		binary.BigEndian.PutUint16(out[off+4+i*2:off+6+i*2], uint16(v))
	}
	return out
}

// Deserialize restores a state produced by Serialize.
func (s *SCSP) Deserialize(b []byte) error {
	if len(b) < SerializeBaseSize || string(b[0:4]) != "SCSP" || b[4] != stateVersion {
		return errState
	}
	s.on = b[5]&1 != 0
	s.acc = int64(binary.BigEndian.Uint64(b[6:14]))
	for i := 0; i < 3; i++ {
		s.timers[i].preset = b[14+i*3]
		s.timers[i].counter = b[15+i*3]
		s.timers[i].phase = int(b[16+i*3])
	}
	s.sciPend = binary.BigEndian.Uint16(b[23:25])
	s.mciPend = binary.BigEndian.Uint16(b[25:27])
	s.mainSent = binary.BigEndian.Uint16(b[27:29])
	s.dma.active = b[29]&1 != 0
	s.dma.dirRegToRAM = b[29]&2 != 0
	s.dma.gate = b[29]&4 != 0
	s.dma.remaining = int(binary.BigEndian.Uint32(b[30:34]))
	s.dma.ramWord = binary.BigEndian.Uint32(b[34:38])
	s.dma.regWord = binary.BigEndian.Uint32(b[38:42])

	off := stateHeader
	for i := 0; i < 32; i++ {
		st := &s.slots[i]
		rec := b[off : off+slotRecSize]
		st.phase = int64(binary.BigEndian.Uint64(rec[0:8]))
		st.egLevel = int32(binary.BigEndian.Uint32(rec[8:12]))
		st.egHold = int32(binary.BigEndian.Uint32(rec[12:16]))
		st.lfoPhase = binary.BigEndian.Uint32(rec[16:20])
		st.lfsr = binary.BigEndian.Uint32(rec[20:24])
		st.prevOut = int32(binary.BigEndian.Uint32(rec[24:28]))
		st.out = st.prevOut
		st.egPhase = int(rec[28])
		st.active = rec[29] != 0
		st.dir = 1
		if rec[30] != 0 {
			st.dir = -1
		}
		off += slotRecSize
	}
	if err := s.cpu.Deserialize(b[off : off+88]); err != nil {
		return err
	}
	off += 88

	d := &s.dsp
	d.acc = int32(binary.BigEndian.Uint32(b[off : off+4]))
	d.memPhase = binary.BigEndian.Uint32(b[off+4 : off+8])
	d.samples = binary.BigEndian.Uint64(b[off+8 : off+16])
	d.tempBase = int(binary.BigEndian.Uint32(b[off+16:off+20])) & 127
	d.memrd = int32(binary.BigEndian.Uint32(b[off+20 : off+24]))
	p := off + 24
	get32s := func(vals []int32) {
		for i := range vals {
			vals[i] = int32(binary.BigEndian.Uint32(b[p : p+4]))
			p += 4
		}
	}
	get32s(d.temp[:])
	get32s(d.mems[:])
	get32s(d.mixs[:])
	get32s(d.efreg[:])
	get32s(d.exts[:])
	off = p

	n := int(binary.BigEndian.Uint32(b[off : off+4]))
	if len(b) != off+4+n*2 {
		return errState
	}
	s.cdQueue = make([]int16, n)
	for i := 0; i < n; i++ {
		s.cdQueue[i] = int16(binary.BigEndian.Uint16(b[off+4+i*2 : off+6+i*2]))
	}
	s.audio = nil
	return nil
}
