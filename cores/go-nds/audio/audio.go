// Package audio implements the Nintendo DS sound unit from
// the published GBATEK documentation: sixteen channels with
// PCM8, PCM16 and IMA ADPCM playback, PSG square waves on
// channels 8 to 13, noise on channels 14 and 15, a stereo
// mixer with master volume and bias, and two sound capture
// units that write back into memory.
//
// Stated model choices and simplifications:
//
//   - The channel sample value is held between channel steps,
//     with no interpolation.
//   - Volume, divider, pan, duty, repeat mode and format are
//     read from the live control register, while SAD, TMR,
//     PNT and LEN are latched when a channel starts. GBATEK
//     does not document live reload of those address and
//     timing registers, so this package latches them, and a
//     register write takes effect on the next start.
//   - Capture runs at the mixer rate of 32768 Hz, one sample
//     per output sample. GBATEK shares the channel 1 and 3
//     timers for capture timing; this package does not model
//     that sharing.
//   - The two documented hardware capture bugs (an all
//     negative result case and an overflow wrap case) are not
//     emulated. Captured values are clamped instead.
//   - PCM8 capture stores byte(value shifted right 8 plus
//     128), so a captured byte round trips with the PCM8
//     playback scale used here.
//   - Noise polarity: a generated sample is positive when the
//     LFSR output bit is 0 and negative when it is 1.
//   - The internal output buffer is capped at 65536 stereo
//     samples. On overflow the new samples are dropped.
//
// The unit raises no interrupts: the DS sound unit has no
// per channel interrupt source.
package audio

// Memory is the byte addressed memory the sound unit reads
// sample data from and writes captured data to. Read16 and
// Write16 are little endian.
type Memory interface {
	Read8(addr uint32) uint8
	Read16(addr uint32) uint16
	Write8(addr uint32, v uint8)
	Write16(addr uint32, v uint16)
}

const (
	clockHz   = 16756991
	outRate   = 32768
	numChan   = 16
	outCap    = 65536
	regBase   = 0x400
	regEnd    = 0x520
	cntStart  = 1 << 31
	cntHold   = 1 << 15
	sndEnable = 1 << 15
)

// adpcmStep is the standard IMA ADPCM step table. It has 89
// entries, for step indices 0 to 88.
var adpcmStep = [...]int32{
	7, 8, 9, 10, 11, 12, 13, 14, 16, 17,
	19, 21, 23, 25, 28, 31, 34, 37, 41, 45,
	50, 55, 60, 66, 73, 80, 88, 97, 107, 118,
	130, 143, 157, 173, 190, 209, 230, 253, 279, 307,
	337, 371, 408, 449, 494, 544, 598, 658, 724, 796,
	876, 963, 1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066,
	2272, 2499, 2749, 3024, 3327, 3660, 4026, 4428, 4871, 5358,
	5894, 6484, 7132, 7845, 8630, 9493, 10442, 11487, 12635, 13899,
	15289, 16818, 18500, 20350, 22385, 24623, 27086, 29794, 32767,
}

// adpcmIndex adjusts the step index from the low three bits
// of a data nibble.
var adpcmIndex = [...]int32{-1, -1, -1, -1, 2, 4, 6, 8}

func init() {
	if len(adpcmStep) != 89 {
		panic("audio: ADPCM step table must have 89 entries")
	}
	if len(adpcmIndex) != 8 {
		panic("audio: ADPCM index table must have 8 entries")
	}
}

var divShift = [...]uint{0, 1, 2, 4}

type channel struct {
	idx int

	// Live and stored registers.
	cnt    uint32
	sadReg uint32
	tmrReg uint16
	pntReg uint16
	lenReg uint32

	// Registers latched at channel start.
	lSad uint32
	lTmr uint16
	lPnt uint16
	lLen uint32

	busy       bool
	hang       bool
	holdActive bool

	pos uint64
	acc uint64

	pred      int32
	index     int32
	snapPred  int32
	snapIndex int32
	snapTaken bool

	lfsr  uint16
	phase uint8

	cur  int32
	hold int32
}

type captureUnit struct {
	cntReg uint8
	dadReg uint32
	lenReg uint16

	busy      bool
	lDad      uint32
	lLenWords uint32
	lCnt      uint8
	posBytes  uint32
}

// Sound is the whole DS sound unit.
type Sound struct {
	mem      Memory
	ch       [numChan]channel
	caps     [2]captureUnit
	soundCnt uint32
	bias     uint32
	phase    uint64
	out      []int16
}

// New builds a sound unit reading and writing through mem.
// The bias register starts at its power on value 0x200 and
// every other register starts at zero.
func New(mem Memory) *Sound {
	s := &Sound{mem: mem, bias: 0x200}
	for i := range s.ch {
		s.ch[i].idx = i
		s.ch[i].lfsr = 0x7FFF
	}
	return s
}

func clamp16(v int32) int32 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return v
}

// getU32 extracts the unit of the given size at the byte lane
// of off from a 32-bit register value.
func getU32(v uint32, off uint32, size int) uint32 {
	switch size {
	case 1:
		return (v >> ((off & 3) * 8)) & 0xFF
	case 2:
		return (v >> ((off & 2) * 8)) & 0xFFFF
	}
	return v
}

// putU32 merges a written unit of the given size at the byte
// lane of off into a 32-bit register value.
func putU32(v uint32, off uint32, size int, val uint32) uint32 {
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

// writeTouches reports whether a write starting at off and
// spanning size bytes includes the byte at addr.
func writeTouches(off uint32, size int, addr uint32) bool {
	if size <= 0 {
		return false
	}
	return off <= addr && addr < off+uint32(size)
}

func (s *Sound) capCntWord() uint32 {
	var w uint32
	for i := range s.caps {
		b := uint32(s.caps[i].cntReg & 0x7F)
		if s.caps[i].busy {
			b |= 0x80
		}
		w |= b << (8 * uint(i))
	}
	return w
}

// ReadIO claims ARM7 IO offsets 0x400 to 0x51F.
func (s *Sound) ReadIO(off uint32, size int) (uint32, bool) {
	if off < regBase || off >= regEnd {
		return 0, false
	}
	base := off &^ 3
	var word uint32
	switch {
	case base < 0x500:
		idx := (base - regBase) / 16
		rel := (base - regBase) % 16
		ch := &s.ch[idx]
		switch rel {
		case 0:
			word = ch.cnt &^ cntStart
			if ch.busy {
				word |= cntStart
			}
		case 4:
			word = ch.sadReg
		case 8:
			word = uint32(ch.tmrReg) | uint32(ch.pntReg)<<16
		case 12:
			word = ch.lenReg
		}
	case base == 0x500:
		word = s.soundCnt
	case base == 0x504:
		word = s.bias
	case base == 0x508:
		word = s.capCntWord()
	case base == 0x510:
		word = s.caps[0].dadReg
	case base == 0x514:
		word = uint32(s.caps[0].lenReg)
	case base == 0x518:
		word = s.caps[1].dadReg
	case base == 0x51C:
		word = uint32(s.caps[1].lenReg)
	}
	return getU32(word, off, size), true
}

// WriteIO claims ARM7 IO offsets 0x400 to 0x51F.
func (s *Sound) WriteIO(off uint32, size int, val uint32) bool {
	if off < regBase || off >= regEnd {
		return false
	}
	switch {
	case off < 0x500:
		rel := off - regBase
		idx := rel / 16
		r := rel % 16
		ch := &s.ch[idx]
		switch {
		case r < 4:
			ch.cnt = putU32(ch.cnt, off, size, val)
			startAddr := regBase + idx*16 + 3
			if writeTouches(off, size, startAddr) {
				if ch.cnt&cntStart != 0 {
					s.startChannel(int(idx))
				} else {
					s.stopChannel(ch)
				}
			}
		case r < 8:
			ch.sadReg = putU32(ch.sadReg, off, size, val) & 0x07FFFFFC
		case r < 12:
			w := uint32(ch.tmrReg) | uint32(ch.pntReg)<<16
			w = putU32(w, off, size, val)
			ch.tmrReg = uint16(w)
			ch.pntReg = uint16(w >> 16)
		default:
			ch.lenReg = putU32(ch.lenReg, off, size, val) & 0x003FFFFF
		}
	case off < 0x504:
		s.soundCnt = putU32(s.soundCnt, off, size, val)
	case off < 0x508:
		s.bias = putU32(s.bias, off, size, val)
	case off < 0x50C:
		old := uint32(s.caps[0].cntReg) | uint32(s.caps[1].cntReg)<<8
		nw := putU32(old, off, size, val)
		for i := range s.caps {
			byteAddr := uint32(0x508 + i)
			if writeTouches(off, size, byteAddr) {
				s.caps[i].cntReg = uint8(nw >> (8 * uint(i)))
				if s.caps[i].cntReg&0x80 != 0 {
					s.startCapture(i)
				} else {
					s.caps[i].busy = false
				}
			}
		}
	case off < 0x510:
		// Unused gap between the capture control bytes and
		// the capture destination registers. Writes land
		// nowhere, but the offset is claimed.
	case off < 0x514:
		s.caps[0].dadReg = putU32(s.caps[0].dadReg, off, size, val)
	case off < 0x518:
		s.caps[0].lenReg = uint16(putU32(uint32(s.caps[0].lenReg), off, size, val))
	case off < 0x51C:
		s.caps[1].dadReg = putU32(s.caps[1].dadReg, off, size, val)
	default:
		s.caps[1].lenReg = uint16(putU32(uint32(s.caps[1].lenReg), off, size, val))
	}
	return true
}

func (ch *channel) format() uint32 {
	return (ch.cnt >> 29) & 3
}

func (ch *channel) totalWords() uint64 {
	return uint64(ch.lPnt) + uint64(ch.lLen)
}

func (ch *channel) totalSamples(format uint32) uint64 {
	tw := ch.totalWords()
	switch format {
	case 0:
		return tw * 4
	case 1:
		return tw * 2
	case 2:
		if tw == 0 {
			return 0
		}
		return 8 * (tw - 1)
	}
	return 0
}

func (ch *channel) loopStartSample(format uint32) uint64 {
	switch format {
	case 0:
		return uint64(ch.lPnt) * 4
	case 1:
		return uint64(ch.lPnt) * 2
	case 2:
		if ch.lPnt == 0 {
			return 0
		}
		return 8 * (uint64(ch.lPnt) - 1)
	}
	return 0
}

// startChannel latches SAD, TMR, PNT and LEN, resets the
// playback position and loads the first sample immediately.
// A total length below four words hangs the channel for the
// sample formats: it stays busy and outputs nothing until a
// control write stops it.
func (s *Sound) startChannel(idx int) {
	ch := &s.ch[idx]
	ch.busy = true
	ch.holdActive = false
	ch.acc = 0
	ch.pos = 0
	ch.lSad = ch.sadReg & 0x07FFFFFC
	ch.lTmr = ch.tmrReg
	ch.lPnt = ch.pntReg
	ch.lLen = ch.lenReg & 0x003FFFFF
	ch.pred = 0
	ch.index = 0
	ch.snapPred = 0
	ch.snapIndex = 0
	ch.snapTaken = false
	ch.lfsr = 0x7FFF
	ch.phase = 0
	ch.cur = 0
	ch.hold = 0
	ch.hang = false

	format := ch.format()
	if format == 3 {
		if idx >= 14 {
			ch.stepNoise()
		}
		return
	}
	if ch.totalWords() < 4 {
		ch.hang = true
		return
	}
	if format == 2 {
		s.loadADPCMHeader(ch)
		if ch.loopStartSample(format) == 0 {
			ch.snapPred = ch.pred
			ch.snapIndex = ch.index
			ch.snapTaken = true
		}
	}
	s.loadSample(ch)
}

// stopChannel stops a channel from a control write with the
// start bit clear. The hold bit in the new control value
// decides whether the last value keeps sounding.
func (s *Sound) stopChannel(ch *channel) {
	var raw int32
	if ch.busy {
		raw = ch.rawCurrent()
	} else if ch.holdActive {
		raw = ch.hold
	}
	ch.busy = false
	if ch.cnt&cntHold != 0 {
		ch.holdActive = true
		ch.hold = raw
	} else {
		ch.holdActive = false
	}
}

func (s *Sound) loadADPCMHeader(ch *channel) {
	lo := s.mem.Read16(ch.lSad)
	hi := s.mem.Read16(ch.lSad + 2)
	hdr := uint32(lo) | uint32(hi)<<16
	ch.pred = int32(int16(uint16(hdr)))
	idx := int32((hdr >> 16) & 0x7F)
	if idx > 88 {
		idx = 88
	}
	ch.index = idx
}

func (s *Sound) loadSample(ch *channel) {
	switch ch.format() {
	case 0:
		b := s.mem.Read8(ch.lSad + uint32(ch.pos))
		ch.cur = (int32(b) - 128) << 8
	case 1:
		v := s.mem.Read16(ch.lSad + uint32(ch.pos)*2)
		ch.cur = int32(int16(v))
	case 2:
		addr := ch.lSad + 4 + uint32(ch.pos/2)
		b := s.mem.Read8(addr)
		var nib uint32
		if ch.pos%2 == 0 {
			nib = uint32(b & 0x0F)
		} else {
			nib = uint32(b >> 4)
		}
		s.decodeADPCM(ch, nib)
	}
}

// decodeADPCM applies one nibble to the predictor and step
// index. The emitted sample is the predictor after the
// nibble has been applied.
func (s *Sound) decodeADPCM(ch *channel, nib uint32) {
	step := adpcmStep[ch.index]
	diff := step >> 3
	if nib&1 != 0 {
		diff += step >> 2
	}
	if nib&2 != 0 {
		diff += step >> 1
	}
	if nib&4 != 0 {
		diff += step
	}
	pred := ch.pred
	if nib&8 != 0 {
		pred -= diff
	} else {
		pred += diff
	}
	ch.pred = clamp16(pred)
	idx := ch.index + adpcmIndex[nib&7]
	if idx < 0 {
		idx = 0
	}
	if idx > 88 {
		idx = 88
	}
	ch.index = idx
	ch.cur = ch.pred
}

// stepNoise advances the 15 bit LFSR once and generates the
// next noise sample. Polarity choice: the sample is positive
// when the output bit taken from the LFSR is 0 and negative
// when it is 1.
func (ch *channel) stepNoise() {
	outBit := ch.lfsr & 1
	ch.lfsr >>= 1
	if outBit != 0 {
		ch.lfsr ^= 0x6000
	}
	if outBit == 0 {
		ch.cur = 32512
	} else {
		ch.cur = -32512
	}
}

// rawCurrent is the sample value before volume and pan.
func (ch *channel) rawCurrent() int32 {
	if ch.format() == 3 && ch.idx >= 8 && ch.idx <= 13 {
		duty := (ch.cnt >> 24) & 7
		if uint32(ch.phase) < duty+1 {
			return 32512
		}
		return -32512
	}
	return ch.cur
}

// monoValue is the channel value after the volume multiplier
// and divisor, before pan. Capture and the direct channel 1
// and 3 outputs use this value.
func (ch *channel) monoValue() int32 {
	if ch.hang && ch.busy {
		return 0
	}
	var raw int32
	if ch.busy {
		raw = ch.rawCurrent()
	} else if ch.holdActive {
		raw = ch.hold
	} else {
		return 0
	}
	vol := int32(ch.cnt & 0x7F)
	v := (raw * vol) >> 7
	v >>= divShift[(ch.cnt>>8)&3]
	return v
}

// stepChannel advances a running channel by one channel
// sample, handling the end of the data: a loop wrap restores
// the loop start (and, for ADPCM, the snapshot taken when
// playback first reached the loop word; a loop start at word
// zero re-reads the header), and the manual, one shot and
// prohibited repeat modes stop the channel, honouring hold.
func (s *Sound) stepChannel(ch *channel) {
	format := ch.format()
	if format == 3 {
		switch {
		case ch.idx >= 8 && ch.idx <= 13:
			ch.phase = (ch.phase + 1) & 7
			ch.pos++
		case ch.idx >= 14:
			ch.pos++
			ch.stepNoise()
		}
		return
	}
	total := ch.totalSamples(format)
	ch.pos++
	if ch.pos >= total {
		repeat := (ch.cnt >> 27) & 3
		if repeat == 1 {
			ch.pos = ch.loopStartSample(format)
			if format == 2 {
				if ch.lPnt == 0 {
					s.loadADPCMHeader(ch)
					ch.snapPred = ch.pred
					ch.snapIndex = ch.index
					ch.snapTaken = true
				} else if ch.snapTaken {
					ch.pred = ch.snapPred
					ch.index = ch.snapIndex
				}
			}
			if ch.pos < total {
				s.loadSample(ch)
			}
			return
		}
		ch.busy = false
		if ch.cnt&cntHold != 0 {
			ch.holdActive = true
			ch.hold = ch.cur
		} else {
			ch.holdActive = false
		}
		return
	}
	if format == 2 && !ch.snapTaken && ch.pos == ch.loopStartSample(format) {
		// The position has just crossed into the loop word.
		// Snapshot the decoder state before this sample is
		// decoded, so a later wrap can restore it and replay
		// the loop region identically.
		ch.snapPred = ch.pred
		ch.snapIndex = ch.index
		ch.snapTaken = true
	}
	s.loadSample(ch)
}

// Advance runs the unit for the given number of ARM7 cycles.
func (s *Sound) Advance(cycles uint64) {
	s.phase += cycles * outRate
	n := s.phase / clockHz
	s.phase %= clockHz
	var i uint64
	for i = 0; i < n; i++ {
		s.produce()
	}
}

// produce mixes one output sample from the current channel
// values, appends the stereo result to the output buffer,
// runs the capture units for that sample, and then steps
// every running channel toward the next output sample. The
// first sample loaded at channel start is therefore the
// first sample heard.
func (s *Sound) produce() {
	var mono [numChan]int32
	for i := range s.ch {
		mono[i] = s.ch[i].monoValue()
	}

	add0 := s.caps[0].busy && s.caps[0].lCnt&1 != 0
	add1 := s.caps[1].busy && s.caps[1].lCnt&1 != 0

	var mixL, mixR int64
	for i := range s.ch {
		ch := &s.ch[i]
		v := int64(mono[i])
		switch i {
		case 0:
			if add0 {
				v += int64(mono[1])
			}
		case 1:
			if add0 || s.soundCnt&(1<<12) != 0 {
				continue
			}
		case 2:
			if add1 {
				v += int64(mono[3])
			}
		case 3:
			if add1 || s.soundCnt&(1<<13) != 0 {
				continue
			}
		}
		pan := int64((ch.cnt >> 16) & 0x7F)
		mixL += v * (127 - pan) / 127
		mixR += v * pan / 127
	}

	left := s.mixOut(selectSource((s.soundCnt>>8)&3, mixL, &mono))
	right := s.mixOut(selectSource((s.soundCnt>>10)&3, mixR, &mono))

	if len(s.out)/2 < outCap {
		s.out = append(s.out, left, right)
	}
	s.runCapture(0, left, right, &mono)
	s.runCapture(1, left, right, &mono)

	for i := range s.ch {
		ch := &s.ch[i]
		if !ch.busy || ch.hang {
			continue
		}
		ch.acc += clockHz
		threshold := uint64(65536-uint32(ch.lTmr)) * outRate
		for ch.busy && ch.acc >= threshold {
			ch.acc -= threshold
			s.stepChannel(ch)
		}
	}
}

func selectSource(code uint32, mix int64, mono *[numChan]int32) int64 {
	switch code {
	case 1:
		return int64(mono[1])
	case 2:
		return int64(mono[3])
	case 3:
		return int64(mono[1]) + int64(mono[3])
	}
	return mix
}

// mixOut applies the master volume and the bias to one
// selected side value and returns the final output sample.
// With the master enable clear the selected value is muted
// and only the bias contributes.
func (s *Sound) mixOut(sel int64) int16 {
	var t int64
	if s.soundCnt&sndEnable != 0 {
		t = (sel * int64(s.soundCnt&0x7F)) >> 7
		t >>= 6
		if t > 511 {
			t = 511
		}
		if t < -512 {
			t = -512
		}
	}
	out := (t + int64(s.bias&0x3FF) - 0x200) << 6
	if out > 32767 {
		out = 32767
	}
	if out < -32768 {
		out = -32768
	}
	return int16(out)
}

func (s *Sound) startCapture(i int) {
	cap := &s.caps[i]
	cap.busy = true
	cap.posBytes = 0
	cap.lDad = cap.dadReg
	words := uint32(cap.lenReg)
	if words == 0 {
		words = 1
	}
	cap.lLenWords = words
	cap.lCnt = cap.cntReg
}

// runCapture writes one captured sample for a capture unit.
// The mixer source is the final output sample for the side;
// the channel source is the channel mono value, plus the
// associated channel mono value when the addition bit is
// set, clamped to int16 range.
func (s *Sound) runCapture(i int, left int16, right int16, mono *[numChan]int32) {
	cap := &s.caps[i]
	if !cap.busy {
		return
	}
	var val int32
	if cap.lCnt&(1<<1) == 0 {
		if i == 0 {
			val = int32(left)
		} else {
			val = int32(right)
		}
	} else {
		if i == 0 {
			val = mono[0]
			if cap.lCnt&1 != 0 {
				val += mono[1]
			}
		} else {
			val = mono[2]
			if cap.lCnt&1 != 0 {
				val += mono[3]
			}
		}
		val = clamp16(val)
	}
	total := cap.lLenWords * 4
	addr := cap.lDad + cap.posBytes
	if cap.lCnt&(1<<3) == 0 {
		s.mem.Write16(addr, uint16(int16(val)))
		cap.posBytes += 2
	} else {
		b := uint8((clamp16(val) >> 8) + 128)
		s.mem.Write8(addr, b)
		cap.posBytes++
	}
	if cap.posBytes >= total {
		if cap.lCnt&(1<<2) == 0 {
			cap.posBytes %= total
		} else {
			cap.busy = false
			cap.posBytes = total
		}
	}
}

// Drain returns the buffered stereo output samples,
// interleaved left then right, and clears the buffer.
func (s *Sound) Drain() []int16 {
	out := s.out
	s.out = nil
	if out == nil {
		return []int16{}
	}
	return out
}
