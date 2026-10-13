package audio

// StateSize is the serialized size of the sound unit: a
// fixed layout holding every channel register and playback
// field, both capture units, the master registers, the mixer
// phase accumulator, and the output buffer as a count plus a
// fixed capacity array of samples.
//
// Per channel 75 bytes, times 16: 1200.
// Per capture unit 21 bytes, times 2: 42.
// Master registers and the mixer phase: 16.
// Output buffer count: 4.
// Output buffer samples: 65536 stereo samples at 2 channels
// and 2 bytes each: 262144.
const StateSize = 16*75 + 2*21 + 16 + 4 + 65536*4

// Serialize writes exactly StateSize bytes into dst and
// returns StateSize.
func (s *Sound) Serialize(dst []byte) int {
	w := &stateWriter{b: dst}
	for i := range s.ch {
		ch := &s.ch[i]
		w.u32(ch.cnt)
		w.u32(ch.sadReg)
		w.u16(ch.tmrReg)
		w.u16(ch.pntReg)
		w.u32(ch.lenReg)
		w.u32(ch.lSad)
		w.u16(ch.lTmr)
		w.u16(ch.lPnt)
		w.u32(ch.lLen)
		w.u64(ch.pos)
		w.u64(ch.acc)
		w.u32(uint32(ch.pred))
		w.u32(uint32(ch.index))
		w.u32(uint32(ch.snapPred))
		w.u32(uint32(ch.snapIndex))
		w.flag(ch.snapTaken)
		w.u16(ch.lfsr)
		w.u8(ch.phase)
		w.flag(ch.busy)
		w.flag(ch.hang)
		w.flag(ch.holdActive)
		w.u32(uint32(ch.cur))
		w.u32(uint32(ch.hold))
	}
	for i := range s.caps {
		cap := &s.caps[i]
		w.u8(cap.cntReg)
		w.u32(cap.dadReg)
		w.u16(cap.lenReg)
		w.flag(cap.busy)
		w.u32(cap.lDad)
		w.u32(cap.lLenWords)
		w.u8(cap.lCnt)
		w.u32(cap.posBytes)
	}
	w.u32(s.soundCnt)
	w.u32(s.bias)
	w.u64(s.phase)
	count := len(s.out) / 2
	w.u32(uint32(count))
	total := outCap * 2
	for i := 0; i < total; i++ {
		if i < len(s.out) {
			w.u16(uint16(s.out[i]))
		} else {
			w.u16(0)
		}
	}
	return w.n
}

// Deserialize restores the state written by Serialize and
// returns the byte count consumed.
func (s *Sound) Deserialize(src []byte) int {
	r := &stateReader{b: src}
	for i := range s.ch {
		ch := &s.ch[i]
		ch.idx = i
		ch.cnt = r.u32()
		ch.sadReg = r.u32()
		ch.tmrReg = r.u16()
		ch.pntReg = r.u16()
		ch.lenReg = r.u32()
		ch.lSad = r.u32()
		ch.lTmr = r.u16()
		ch.lPnt = r.u16()
		ch.lLen = r.u32()
		ch.pos = r.u64()
		ch.acc = r.u64()
		ch.pred = int32(r.u32())
		ch.index = int32(r.u32())
		ch.snapPred = int32(r.u32())
		ch.snapIndex = int32(r.u32())
		ch.snapTaken = r.flag()
		ch.lfsr = r.u16()
		ch.phase = r.u8()
		ch.busy = r.flag()
		ch.hang = r.flag()
		ch.holdActive = r.flag()
		ch.cur = int32(r.u32())
		ch.hold = int32(r.u32())
	}
	for i := range s.caps {
		cap := &s.caps[i]
		cap.cntReg = r.u8()
		cap.dadReg = r.u32()
		cap.lenReg = r.u16()
		cap.busy = r.flag()
		cap.lDad = r.u32()
		cap.lLenWords = r.u32()
		cap.lCnt = r.u8()
		cap.posBytes = r.u32()
	}
	s.soundCnt = r.u32()
	s.bias = r.u32()
	s.phase = r.u64()
	count := int(r.u32())
	if count > outCap {
		count = outCap
	}
	if count < 0 {
		count = 0
	}
	out := make([]int16, 0, count*2)
	total := outCap * 2
	for i := 0; i < total; i++ {
		v := int16(r.u16())
		if i < count*2 {
			out = append(out, v)
		}
	}
	s.out = out
	return r.n
}

// stateWriter and stateReader are byte cursors for the state
// blob, in the style of the sibling packages.
type stateWriter struct {
	b []byte
	n int
}

func (w *stateWriter) u8(v uint8) { w.b[w.n] = v; w.n++ }
func (w *stateWriter) u16(v uint16) {
	w.u8(uint8(v))
	w.u8(uint8(v >> 8))
}
func (w *stateWriter) u32(v uint32) {
	w.u16(uint16(v))
	w.u16(uint16(v >> 16))
}
func (w *stateWriter) u64(v uint64) {
	w.u32(uint32(v))
	w.u32(uint32(v >> 32))
}
func (w *stateWriter) flag(b bool) {
	if b {
		w.u8(1)
	} else {
		w.u8(0)
	}
}

type stateReader struct {
	b []byte
	n int
}

func (r *stateReader) u8() uint8 {
	v := r.b[r.n]
	r.n++
	return v
}
func (r *stateReader) u16() uint16 {
	return uint16(r.u8()) | uint16(r.u8())<<8
}
func (r *stateReader) u32() uint32 {
	return uint32(r.u16()) | uint32(r.u16())<<16
}
func (r *stateReader) u64() uint64 {
	return uint64(r.u32()) | uint64(r.u32())<<32
}
func (r *stateReader) flag() bool { return r.u8() != 0 }
