package aica

import (
	"encoding/binary"
	"math"

	arm7 "github.com/davidpatton/itak-gaming/cores/go-dreamcast/chips/arm7"
)

// Fixed serialize layout, in order:
//
//	raw register image        32768 bytes
//	COEF 128 x u16              256 bytes
//	MADRS 64 x u16              128 bytes
//	MPRO 128 x 4 x u16         1024 bytes
//	TEMP 128 x i32              512 bytes
//	MEMS 32 x i32               128 bytes
//	MIXS 16 x i32                64 bytes
//	EFREG 16 x i32               64 bytes
//	DSP internals 7 x i32        28 bytes
//	slots 64 x 76 bytes        4864 bytes
//	timers 3 x (u32, u32)        24 bytes
//	interrupt block 4 x u32      16 bytes
//	clock block                  28 bytes
//	output ring count u32 plus
//	  outCap stereo pairs     65540 bytes
//	ARM7 state             arm7.SerializeSize bytes
const serializeOwnSize = 32768 + 256 + 128 + 1024 + 512 + 128 + 64 + 64 +
	28 + 4864 + 24 + 16 + 28 + 65540

// SerializeSize is the exact size of a serialized AICA.
const SerializeSize = serializeOwnSize + arm7.SerializeSize

type serWriter struct {
	buf []byte
	off int
}

func (w *serWriter) u8(v uint8) {
	w.buf[w.off] = v
	w.off += 1
}

func (w *serWriter) u16(v uint16) {
	binary.LittleEndian.PutUint16(w.buf[w.off:], v)
	w.off += 2
}

func (w *serWriter) u32(v uint32) {
	binary.LittleEndian.PutUint32(w.buf[w.off:], v)
	w.off += 4
}

func (w *serWriter) u64(v uint64) {
	binary.LittleEndian.PutUint64(w.buf[w.off:], v)
	w.off += 8
}

func (w *serWriter) i32(v int32) { w.u32(uint32(v)) }
func (w *serWriter) i64(v int64) { w.u64(uint64(v)) }
func (w *serWriter) bytes(b []byte) {
	copy(w.buf[w.off:], b)
	w.off += len(b)
}

type serReader struct {
	buf []byte
	off int
}

func (r *serReader) u8() uint8 {
	v := r.buf[r.off]
	r.off += 1
	return v
}

func (r *serReader) u16() uint16 {
	v := binary.LittleEndian.Uint16(r.buf[r.off:])
	r.off += 2
	return v
}

func (r *serReader) u32() uint32 {
	v := binary.LittleEndian.Uint32(r.buf[r.off:])
	r.off += 4
	return v
}

func (r *serReader) u64() uint64 {
	v := binary.LittleEndian.Uint64(r.buf[r.off:])
	r.off += 8
	return v
}

func (r *serReader) i32() int32 { return int32(r.u32()) }
func (r *serReader) i64() int64 { return int64(r.u64()) }
func (r *serReader) bytes(b []byte) {
	copy(b, r.buf[r.off:r.off+len(b)])
	r.off += len(b)
}

func boolByte(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// Serialize writes the full AICA state into dst, which must be at
// least SerializeSize bytes. It returns the number of bytes written.
// The wave RAM slice is not included; the system serializes it as
// bus state.
func (a *AICA) Serialize(dst []byte) int {
	if len(dst) < SerializeSize {
		return 0
	}
	w := &serWriter{buf: dst}
	w.bytes(a.raw[:])
	for i := 0; i < 128; i += 1 {
		w.u16(a.coef[i])
	}
	for i := 0; i < 64; i += 1 {
		w.u16(a.madrs[i])
	}
	for s := 0; s < 128; s += 1 {
		for k := 0; k < 4; k += 1 {
			w.u16(a.mpro[s][k])
		}
	}
	for i := 0; i < 128; i += 1 {
		w.i32(a.temp[i])
	}
	for i := 0; i < 32; i += 1 {
		w.i32(a.mems[i])
	}
	for i := 0; i < 16; i += 1 {
		w.i32(a.mixs[i])
	}
	for i := 0; i < 16; i += 1 {
		w.i32(a.efreg[i])
	}
	w.i32(a.dsp.acc)
	w.i32(a.dsp.memval)
	w.i32(a.dsp.frc)
	w.i32(a.dsp.yreg)
	w.i32(a.dsp.adrs)
	w.i32(a.dsp.dec)
	w.i32(a.dsp.mdec)
	for i := 0; i < 64; i += 1 {
		s := &a.slots[i]
		w.u64(s.phase)
		w.i64(s.egVolFP)
		w.i64(s.fegFP)
		w.u64(math.Float64bits(s.filterY))
		w.i32(s.adpcmPred)
		w.i32(s.adpcmStep)
		w.i32(int32(s.seqCur))
		w.i32(int32(s.seqNext))
		w.i32(int32(s.seqPos))
		w.u32(s.lfsr)
		w.u32(s.lfoPhase)
		w.u8(uint8(s.egState))
		w.u8(uint8(s.fegState))
		w.u8(boolByte(s.active))
		w.u8(boolByte(s.loopHit))
		w.u8(boolByte(s.adpcmInit))
		w.u8(s.lfoNoise)
		for p := 0; p < 10; p += 1 {
			w.u8(0)
		}
	}
	for t := 0; t < 3; t += 1 {
		w.u32(uint32(a.timers[t].counter))
		w.u32(uint32(a.timers[t].divider))
	}
	w.u32(uint32(a.pending))
	w.i32(int32(a.presentedSrc))
	w.i32(int32(a.presentedLvl))
	w.u32(uint32(boolByte(a.sinkRaised)))
	w.u32(uint32(a.masterRem))
	w.u32(uint32(a.armRem))
	w.u32(uint32(a.rtcRem))
	w.u32(uint32(boolByte(a.rtcEnable)))
	w.u64(uint64(a.rtcSeconds))
	w.u32(uint32(boolByte(a.armRP)))
	w.u32(uint32(len(a.out)))
	for i := 0; i < outCap; i += 1 {
		if i < len(a.out) {
			w.u16(uint16(a.out[i][0]))
			w.u16(uint16(a.out[i][1]))
		} else {
			w.u32(0)
		}
	}
	if a.arm != nil {
		n := a.arm.Serialize(dst[w.off:])
		w.off += n
	} else {
		w.off += arm7.SerializeSize
	}
	return w.off
}

// Deserialize restores a state written by Serialize. It validates
// the length only; versioning is the caller's job.
func (a *AICA) Deserialize(src []byte) bool {
	if len(src) != SerializeSize {
		return false
	}
	r := &serReader{buf: src}
	r.bytes(a.raw[:])
	for i := 0; i < 128; i += 1 {
		a.coef[i] = r.u16()
	}
	for i := 0; i < 64; i += 1 {
		a.madrs[i] = r.u16()
	}
	for s := 0; s < 128; s += 1 {
		for k := 0; k < 4; k += 1 {
			a.mpro[s][k] = r.u16()
		}
	}
	for i := 0; i < 128; i += 1 {
		a.temp[i] = r.i32()
	}
	for i := 0; i < 32; i += 1 {
		a.mems[i] = r.i32()
	}
	for i := 0; i < 16; i += 1 {
		a.mixs[i] = r.i32()
	}
	for i := 0; i < 16; i += 1 {
		a.efreg[i] = r.i32()
	}
	a.dsp.acc = r.i32()
	a.dsp.memval = r.i32()
	a.dsp.frc = r.i32()
	a.dsp.yreg = r.i32()
	a.dsp.adrs = r.i32()
	a.dsp.dec = r.i32()
	a.dsp.mdec = r.i32()
	for i := 0; i < 64; i += 1 {
		s := &a.slots[i]
		s.phase = r.u64()
		s.egVolFP = r.i64()
		s.fegFP = r.i64()
		s.filterY = math.Float64frombits(r.u64())
		s.adpcmPred = r.i32()
		s.adpcmStep = r.i32()
		s.seqCur = int16(r.i32())
		s.seqNext = int16(r.i32())
		s.seqPos = int(r.i32())
		s.lfsr = r.u32()
		s.lfoPhase = r.u32()
		s.egState = int(r.u8())
		s.fegState = int(r.u8())
		s.active = r.u8() != 0
		s.loopHit = r.u8() != 0
		s.adpcmInit = r.u8() != 0
		s.lfoNoise = r.u8()
		for p := 0; p < 10; p += 1 {
			r.u8()
		}
	}
	for t := 0; t < 3; t += 1 {
		a.timers[t].counter = uint8(r.u32())
		a.timers[t].divider = int(r.u32())
	}
	a.pending = uint16(r.u32())
	a.presentedSrc = int(r.i32())
	a.presentedLvl = int(r.i32())
	a.sinkRaised = r.u32() != 0
	a.masterRem = int(r.u32())
	a.armRem = int(r.u32())
	a.rtcRem = int(r.u32())
	a.rtcEnable = r.u32() != 0
	a.rtcSeconds = uint32(r.u64())
	a.armRP = r.u32() != 0
	count := int(r.u32())
	a.out = nil
	for i := 0; i < outCap; i += 1 {
		l := int16(r.u16())
		rr := int16(r.u16())
		if i < count && i < outCap {
			a.out = append(a.out, [2]int16{l, rr})
		}
	}
	if a.arm != nil {
		if !a.arm.Deserialize(src[r.off:]) {
			return false
		}
	}
	a.fiqAsserted = a.presentedSrc >= 0
	return true
}
