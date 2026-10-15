// Package sound is the Genesis sound section: a Yamaha YM2612 FM chip and
// the TI SN76489 PSG (the Sega variant built into the VDP's silicon, with
// the 16-bit LFSR and 0x0009 noise taps), mixed to 44.1kHz stereo.
//
// The PSG is our own SN76489 (cores/go-genesis/chips/psg), written
// from the TI datasheet and the Sega variant documentation. The
// YM2612 FM engine (ym2612.go) is written from Yamaha's published
// documentation: full six-channel FM with operators, envelopes,
// SSG-EG, LFO, timers and the DAC path. Milestone 1 shipped the
// YM2612 as a register shell; milestone 2 is the real engine.
//
// PINNED API (bus and system are written against this):
//
//	New() *Sound
//	WriteYM(addr uint32, val uint8)   addr 0-3: 0=addr port A, 1=data A, 2=addr port B, 3=data B
//	ReadYM() uint8                    status byte (busy + timer flags)
//	WritePSG(val uint8)
//	Produce(frames int) []int16       interleaved stereo, advances the PSG
//	SerializeSize() int / Serialize / Deserialize
//
// AdvanceClocks(cycles uint32) was added after the original pin:
// it steps only the YM2612 timers, in master clocks, so the system
// can move them at scanline boundaries; Produce accounts for the
// stepped time through a serialized ledger (see AdvanceClocks).
//
// RenderYMTo(clock int64) was added for timestamped register
// writes: it renders the pending FM span up to an emulated moment
// (YM2612 master clocks since power-on), so a write that lands
// right after it, driven by the bus with a CPU clock, takes
// effect at its true position inside the frame instead of at
// frame end. DAC sample streams depend on it.
package sound

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/davidpatton/itak-gaming/cores/go-genesis/chips/psg"
)

// SampleRate is the core's audio output rate.
const SampleRate = 44100

// YM2612 master clock on the Genesis (NTSC): 7,670,454 Hz.
const ymClock = 7670454

// PSG clock on the Genesis (NTSC): 3,579,545 Hz.
const psgClock = 3579545

// Sound holds both chips and the mix state.
type Sound struct {
	psg *psg.PSG

	ymAddrA  uint8
	ymAddrB  uint8
	ymRegsA  [256]uint8
	ymRegsB  [256]uint8
	ymStatus uint8 // YM2612 timer flags: bit0 = timer A, bit1 = timer B (busy is never set)
	ym       ymState

	// ymRegGen counts YM2612 register data writes; ymPrep holds
	// per-channel render constants derived from the register file
	// (see ym2612.go), rebuilt lazily whenever the generation has
	// moved since the channel was last prepared. Register changes
	// only ever arrive between render spans, so constants derived
	// at a generation stay valid for a whole span.
	ymRegGen uint64
	ymPrep   [6]ymChanPrep

	psgCarry float64 // fractional PSG clocks owed to the next Produce call
	out      []int16 // reusable mix buffer

	// FM resampler state: fmBuf holds native-rate stereo samples,
	// fmPrev is the last native sample of the previous batch and
	// fmAcc is how far (in native samples, 0..1) the next output
	// frame's position has advanced past it. preRendered counts
	// the samples at the head of fmBuf that timestamped writes
	// (RenderYMTo) already generated for the next Produce window;
	// it is always zero between frames, when states are taken, so
	// it is not serialized (the samples themselves are regenerated
	// identically from the serialized engine and stream state).
	fmBuf       []float32
	fmPrevL     float32
	fmPrevR     float32
	fmAcc       float64
	preRendered int

	psgWrites int // diagnostic counters: chip register writes seen
	ymWrites  int
}

// WriteCounts returns how many PSG and YM2612 writes the chips have
// received since power-on. Diagnostic aid: it separates a game that
// never touches a chip from one whose sound is genuinely broken.
func (s *Sound) WriteCounts() (psg, ym int) { return s.psgWrites, s.ymWrites }

// New returns the sound section in power-on state.
func New() *Sound {
	s := &Sound{
		psg: psg.New(psgClock, SampleRate, 4096, psg.Config{
			LFSRBits:       16,     // Sega variant
			WhiteNoiseTaps: 0x0009, // bits 0 and 3
		}),
	}
	// Operators power on silent: envelope at full attenuation,
	// stage off (the zero values of the ymState fields).
	for ch := range s.ym.ops {
		for op := range s.ym.ops[ch] {
			s.ym.ops[ch][op].levelFP = ymMaxFP
		}
	}
	// Pan powers on routed to BOTH sides on real hardware, even
	// though the register value reads as zero: the registers are
	// write-only, and games prove the routing every boot. Sonic 1
	// streams the SEGA jingle through the DAC without ever writing
	// channel 6's pan register first, and it is audible on hardware.
	// An explicit pan write (including 0x00, full mute) still takes
	// effect exactly as written.
	for _, bank := range []*[256]uint8{&s.ymRegsA, &s.ymRegsB} {
		bank[0xB4] = 0xC0
		bank[0xB5] = 0xC0
		bank[0xB6] = 0xC0
	}
	return s
}

// WritePSG passes a byte to the PSG.
func (s *Sound) WritePSG(val uint8) { s.psgWrites++; s.psg.Write(val) }

// serializeSize is the fixed save-state size of the sound section.
// The first 1544 bytes are the milestone 1 layout; the FM engine
// state (milestone 2) is appended after it:
//
//	ymAddrA(1) + ymAddrB(1) + ymStatus(1) + pad(1) + ymRegsA(256) +
//	ymRegsB(256) + psgLen(4, little endian) + psg state (1024 max) +
//	psgCarry f64(8) + fmAcc f64(8) + fmPrev f32x2(8) + lfoPhase(4) +
//	timerAAcc(4) + timerBAcc(4) + egDiv(1) + prev27(1) + pad(2) +
//	per-channel feedback history f32x2 x6(48) +
//	per-operator state x24(12 each: phase(4) + levelFP(4) +
//	stage(1) + flags(1: keyOn, ssgDown, ssgHold) + pad(2)) +
//	advLedger i64(8): timer clocks the system stepped ahead of the
//	render loop (see AdvanceClocks); without it a restore would
//	step that span of timer time a second time or not at all.
//	genClock i64(8): emulated time the render stream has reached
//	(see RenderYMTo); without it a restore could not place
//	timestamped writes against the stream and would skip or
//	double-render the span around the save point.
const serializeSize = 1936

// psgStateOffset is the offset of the PSG state region in the layout.
const psgStateOffset = 520

// ymStateOffset is the offset of the appended FM engine state.
const ymStateOffset = 1544

// WriteYM handles a write to one of the YM2612's four ports: address
// latch or data for bank A (channels 1-3 plus the global registers)
// or bank B (channels 4-6). Data writes land in the register file,
// which the engine reads live, and the two registers with immediate
// side effects act at once: 0x27 resets timer flags and starts or
// stops the timers through its load bits, 0x28 keys operators on
// and off.
func (s *Sound) WriteYM(addr uint32, val uint8) {
	s.ymWrites++
	switch addr {
	case 0: // bank A address latch
		s.ymAddrA = val
	case 1: // bank A data
		s.ymRegsA[s.ymAddrA] = val
		s.ymRegGen++
		switch s.ymAddrA {
		case 0x27:
			// Timer mode: bit4 (0x10) resets the timer A flag,
			// bit5 (0x20) resets the timer B flag.
			if val&0x10 != 0 {
				s.ymStatus &^= 0x01
			}
			if val&0x20 != 0 {
				s.ymStatus &^= 0x02
			}
			// A rising load bit restarts that timer's count.
			if val&0x01 != 0 && s.ym.prev27&0x01 == 0 {
				s.ym.timerAAcc = 0
			}
			if val&0x02 != 0 && s.ym.prev27&0x02 == 0 {
				s.ym.timerBAcc = 0
			}
			s.ym.prev27 = val
		case 0x28:
			s.ymKey(val)
		}
	case 2: // bank B address latch
		s.ymAddrB = val
	case 3: // bank B data
		s.ymRegsB[s.ymAddrB] = val
		s.ymRegGen++
	}
}

// ReadYM returns the YM2612 status byte: bit7 busy (always 0 here, port
// writes complete instantly), bit1 timer B flag, bit0 timer A flag.
func (s *Sound) ReadYM() uint8 {
	return s.ymStatus & 0x03
}

// AdvanceClocks steps the YM2612 timers forward by the given
// number of master clocks, outside the render path. On the
// Genesis the YM2612 clock (master / 7) equals the 68000 clock, so
// callers pass elapsed 68000 cycles. The system calls this at
// scanline boundaries so a CPU polling the status port sees timer
// flags flip mid-frame, the way DAC sample playback is paced,
// instead of only after the frame's audio renders. The stepped
// clocks are recorded in the engine's advance ledger and Produce
// renders past them without stepping a second time, so a span of
// emulated time moves the timers exactly once. Only the timers
// move here; envelopes and phase still render in Produce.
func (s *Sound) AdvanceClocks(cycles uint32) {
	if cycles == 0 {
		return
	}
	s.ymAdvanceTimers(cycles)
	s.ym.advLedger += int64(cycles)
}

// RenderYMTo renders the FM stream forward to the given emulated
// moment, in YM2612 master clocks since power-on (on the Genesis
// the YM2612 clock equals the 68000 clock). The bus calls this
// with the writing CPU's current time just before a register
// write lands, so the pending span is rendered with the register
// values that were in force during it and the write takes effect
// at its true position inside the frame. Without it, a DAC
// stream writing register 0x2A hundreds of times per frame would
// be heard as only its last byte, held for the whole frame.
// Rendering happens in whole native samples (144 clocks); a
// sub-sample remainder stays pending for the next call, so many
// small advances cost nothing until a full sample is owed.
// Timers ride along through the render loop's ledger accounting,
// exactly as if Produce had generated the span. A clock at or
// behind the rendered position renders nothing.
func (s *Sound) RenderYMTo(clock int64) {
	pending := clock - s.ym.genClock
	if pending < 144 {
		return
	}
	s.ymGenerate(int(pending / 144))
}

// Produce advances both chips by the time owed for frames output
// frames and returns exactly frames interleaved stereo pairs. The
// PSG runs as it always has (native samples at the output rate,
// mono duplicated to both channels). The YM2612 runs at its native
// rate (master / 144) and is linear-interpolated down to the output
// rate; the interpolation position and the previous native sample
// carry across calls and are part of the save state, so a restore
// resumes the FM stream sample-exactly.
func (s *Sound) Produce(frames int) []int16 {
	if frames < 0 {
		frames = 0
	}
	total := s.psgCarry + float64(frames)*psgClock/SampleRate
	whole := int(total)
	s.psgCarry = total - float64(whole)

	s.psg.GenerateSamples(whole)
	buf, count := s.psg.GetBuffer()
	s.psg.ResetBuffer()

	// Generate the native-rate FM samples this call spans, on top
	// of any already rendered into the head of the buffer by
	// timestamped writes (RenderYMTo). The next output frame sits
	// at native position -fmAcc relative to the first sample of
	// the window, with the previous batch's last sample at
	// position -1.
	n := 0
	if frames > 0 {
		n = int(float64(frames)*ymRatio-s.fmAcc) + 1
		if n < 0 {
			n = 0
		}
		if s.preRendered < n {
			s.ymGenerate(n - s.preRendered)
		}
	}

	need := frames * 2
	if cap(s.out) < need {
		s.out = make([]int16, need)
	}
	s.out = s.out[:need]
	for i := 0; i < frames; i++ {
		var f float32
		if i < count {
			f = buf[i]
		}
		if f > 1 {
			f = 1
		} else if f < -1 {
			f = -1
		}

		// Linear interpolation of the FM stream at this frame's
		// native position.
		pos := -s.fmAcc + float64(i)*ymRatio
		k := int(pos)
		if pos < 0 && float64(k) != pos {
			k-- // floor for negative positions
		}
		frac := float32(pos - float64(k))
		var l0, r0 float32
		if k < 0 {
			l0, r0 = s.fmPrevL, s.fmPrevR
		} else {
			l0, r0 = s.fmBuf[2*k], s.fmBuf[2*k+1]
		}
		l1, r1 := s.fmBuf[2*(k+1)], s.fmBuf[2*(k+1)+1]
		fl := l0 + (l1-l0)*frac
		fr := r0 + (r1-r0)*frac

		s.out[2*i] = clamp16(f*24000 + fl*12000)
		s.out[2*i+1] = clamp16(f*24000 + fr*12000)
	}
	if n > 0 {
		s.fmPrevL = s.fmBuf[2*(n-1)]
		s.fmPrevR = s.fmBuf[2*(n-1)+1]
		s.fmAcc = float64(n) + s.fmAcc - float64(frames)*ymRatio
		// Samples the timestamped writes rendered past this
		// window (their emulated moment falls inside the next
		// one) carry over as the head of the next window; the
		// rest of the buffer is scratch.
		if left := s.preRendered - n; left > 0 {
			copy(s.fmBuf, s.fmBuf[2*n:2*(n+left)])
			s.preRendered = left
		} else {
			s.preRendered = 0
		}
	}
	return s.out
}

// clamp16 converts a mixed float sample to int16 with saturation.
func clamp16(v float32) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// SerializeSize returns the fixed save-state size of the sound section.
func (s *Sound) SerializeSize() int { return serializeSize }

// Serialize writes the sound section state into buf (layout documented at
// serializeSize). The PSG state goes into the 1024-byte region at the
// tail. Observed chip behavior: psg.Serialize needs only
// psg.SerializeSize (41) bytes, so with a 1024-byte scratch it never
// errors; if it ever did, psgLen is written as 0 and the region left
// zeroed instead of failing the whole state.
func (s *Sound) Serialize(buf []byte) error {
	if len(buf) < serializeSize {
		return errors.New("sound: serialize buffer too small")
	}
	buf[0] = s.ymAddrA
	buf[1] = s.ymAddrB
	buf[2] = s.ymStatus
	buf[3] = 0
	copy(buf[4:260], s.ymRegsA[:])
	copy(buf[260:516], s.ymRegsB[:])

	region := buf[psgStateOffset:serializeSize]
	for i := range region {
		region[i] = 0
	}
	psgLen := 0
	var scratch [1024]byte
	if err := s.psg.Serialize(scratch[:]); err == nil {
		psgLen = psg.SerializeSize
		copy(region, scratch[:psgLen])
	}
	binary.LittleEndian.PutUint32(buf[516:520], uint32(psgLen))

	// FM engine state (milestone 2), layout at serializeSize.
	b := buf[ymStateOffset:]
	binary.LittleEndian.PutUint64(b[0:8], math.Float64bits(s.psgCarry))
	binary.LittleEndian.PutUint64(b[8:16], math.Float64bits(s.fmAcc))
	binary.LittleEndian.PutUint32(b[16:20], math.Float32bits(s.fmPrevL))
	binary.LittleEndian.PutUint32(b[20:24], math.Float32bits(s.fmPrevR))
	binary.LittleEndian.PutUint32(b[24:28], s.ym.lfoPhase)
	binary.LittleEndian.PutUint32(b[28:32], s.ym.timerAAcc)
	binary.LittleEndian.PutUint32(b[32:36], s.ym.timerBAcc)
	b[36] = s.ym.egDiv
	b[37] = s.ym.prev27
	b[38], b[39] = 0, 0
	off := 40
	for ch := range s.ym.chans {
		binary.LittleEndian.PutUint32(b[off:off+4], math.Float32bits(s.ym.chans[ch].fb0))
		binary.LittleEndian.PutUint32(b[off+4:off+8], math.Float32bits(s.ym.chans[ch].fb1))
		off += 8
	}
	for ch := range s.ym.ops {
		for op := range s.ym.ops[ch] {
			o := &s.ym.ops[ch][op]
			binary.LittleEndian.PutUint32(b[off:off+4], o.phase)
			binary.LittleEndian.PutUint32(b[off+4:off+8], uint32(o.levelFP))
			b[off+8] = o.stage
			var flags uint8
			if o.keyOn {
				flags |= 1
			}
			if o.ssgDown {
				flags |= 2
			}
			if o.ssgHold {
				flags |= 4
			}
			b[off+9] = flags
			b[off+10], b[off+11] = 0, 0
			off += 12
		}
	}
	binary.LittleEndian.PutUint64(b[off:off+8], uint64(s.ym.advLedger))
	binary.LittleEndian.PutUint64(b[off+8:off+16], uint64(s.ym.genClock))
	return nil
}

// Deserialize restores state written by Serialize.
func (s *Sound) Deserialize(buf []byte) error {
	if len(buf) < serializeSize {
		return errors.New("sound: deserialize buffer too small")
	}
	s.ymAddrA = buf[0]
	s.ymAddrB = buf[1]
	s.ymStatus = buf[2] & 0x03
	copy(s.ymRegsA[:], buf[4:260])
	copy(s.ymRegsB[:], buf[260:516])

	psgLen := int(binary.LittleEndian.Uint32(buf[516:520]))
	if psgLen > ymStateOffset-psgStateOffset {
		return errors.New("sound: corrupt PSG state length")
	}
	if psgLen > 0 {
		if err := s.psg.Deserialize(buf[psgStateOffset : psgStateOffset+psgLen]); err != nil {
			return err
		}
	}

	// FM engine state (milestone 2), layout at serializeSize.
	b := buf[ymStateOffset:]
	s.psgCarry = math.Float64frombits(binary.LittleEndian.Uint64(b[0:8]))
	s.fmAcc = math.Float64frombits(binary.LittleEndian.Uint64(b[8:16]))
	s.fmPrevL = math.Float32frombits(binary.LittleEndian.Uint32(b[16:20]))
	s.fmPrevR = math.Float32frombits(binary.LittleEndian.Uint32(b[20:24]))
	s.ym.lfoPhase = binary.LittleEndian.Uint32(b[24:28])
	s.ym.timerAAcc = binary.LittleEndian.Uint32(b[28:32])
	s.ym.timerBAcc = binary.LittleEndian.Uint32(b[32:36])
	s.ym.egDiv = b[36]
	s.ym.prev27 = b[37]
	off := 40
	for ch := range s.ym.chans {
		s.ym.chans[ch].fb0 = math.Float32frombits(binary.LittleEndian.Uint32(b[off : off+4]))
		s.ym.chans[ch].fb1 = math.Float32frombits(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		off += 8
	}
	for ch := range s.ym.ops {
		for op := range s.ym.ops[ch] {
			o := &s.ym.ops[ch][op]
			o.phase = binary.LittleEndian.Uint32(b[off : off+4])
			o.levelFP = int32(binary.LittleEndian.Uint32(b[off+4 : off+8]))
			o.stage = b[off+8]
			flags := b[off+9]
			o.keyOn = flags&1 != 0
			o.ssgDown = flags&2 != 0
			o.ssgHold = flags&4 != 0
			off += 12
		}
	}
	s.ym.advLedger = int64(binary.LittleEndian.Uint64(b[off : off+8]))
	s.ym.genClock = int64(binary.LittleEndian.Uint64(b[off+8 : off+16]))
	// fmBuf content is scratch and preRendered is always zero at
	// frame boundaries, where states are taken; make that true
	// here too so a mid-stream restore cannot resurrect stale
	// pending samples.
	s.preRendered = 0
	// The register file was replaced wholesale: force every
	// channel's prepared render constants to rebuild.
	s.ymRegGen++
	return nil
}
