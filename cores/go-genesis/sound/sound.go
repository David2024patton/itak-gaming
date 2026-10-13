// Package sound is the Genesis sound section: a Yamaha YM2612 FM chip and
// the TI SN76489 PSG (the Sega variant built into the VDP's silicon, with
// the 16-bit LFSR and 0x0009 noise taps), mixed to 44.1kHz stereo.
//
// The PSG is our own SN76489 (cores/go-genesis/chips/psg), written
// from the TI datasheet and the Sega variant documentation. The
// YM2612 is written here from Yamaha's published register map.
// Milestone 1 ships the YM2612 as a register-accurate shell (writes
// land, timers run, output silent); the FM operator engine lands in
// milestone 2.
//
// PINNED API (bus and system are written against this):
//
//	New() *Sound
//	WriteYM(addr uint32, val uint8)   addr 0-3: 0=addr port A, 1=data A, 2=addr port B, 3=data B
//	ReadYM() uint8                    status byte (busy + timer flags)
//	WritePSG(val uint8)
//	Produce(frames int) []int16       interleaved stereo, advances the PSG
//	SerializeSize() int / Serialize / Deserialize
package sound

import (
	"encoding/binary"
	"errors"

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

	psgCarry float64 // fractional PSG clocks owed to the next Produce call
	out      []int16 // reusable mix buffer

	psgWrites int // diagnostic counters: chip register writes seen
	ymWrites  int
}

// WriteCounts returns how many PSG and YM2612 writes the chips have
// received since power-on. Diagnostic aid (the FM engine is milestone 2,
// so a game can be writing the YM constantly while only the PSG sounds).
func (s *Sound) WriteCounts() (psg, ym int) { return s.psgWrites, s.ymWrites }

// New returns the sound section in power-on state.
func New() *Sound {
	return &Sound{
		psg: psg.New(psgClock, SampleRate, 4096, psg.Config{
			LFSRBits:       16,     // Sega variant
			WhiteNoiseTaps: 0x0009, // bits 0 and 3
		}),
	}
}

// WritePSG passes a byte to the PSG.
func (s *Sound) WritePSG(val uint8) { s.psgWrites++; s.psg.Write(val) }

// serializeSize is the fixed save-state size of the sound section:
//
//	ymAddrA(1) + ymAddrB(1) + ymStatus(1) + pad(1) + ymRegsA(256) +
//	ymRegsB(256) + psgLen(4, little endian) + psg state (1024 max)
const serializeSize = 1544

// psgStateOffset is the offset of the PSG state region in the layout.
const psgStateOffset = 520

// WriteYM handles a write to one of the YM2612's four ports. Milestone 1
// is a register-accurate shell: writes land in the register file and no
// FM audio is produced yet. That includes the bank A DAC data register
// (0x2A), which is stored like any other register; the DAC output path
// arrives with the FM engine in milestone 2. The timer registers (bank A
// 0x24-0x27) are stored raw; the timers themselves are milestone 2, so a
// write to 0x27 can only clear timer flags, never set them.
func (s *Sound) WriteYM(addr uint32, val uint8) {
	s.ymWrites++
	switch addr {
	case 0: // bank A address latch
		s.ymAddrA = val
	case 1: // bank A data
		s.ymRegsA[s.ymAddrA] = val
		if s.ymAddrA == 0x27 {
			// Timer mode: bit4 (0x10) resets the timer A flag,
			// bit5 (0x20) resets the timer B flag.
			if val&0x10 != 0 {
				s.ymStatus &^= 0x01
			}
			if val&0x20 != 0 {
				s.ymStatus &^= 0x02
			}
		}
	case 2: // bank B address latch
		s.ymAddrB = val
	case 3: // bank B data
		s.ymRegsB[s.ymAddrB] = val
	}
}

// ReadYM returns the YM2612 status byte: bit7 busy (always 0 here, port
// writes complete instantly), bit1 timer B flag, bit0 timer A flag.
func (s *Sound) ReadYM() uint8 {
	return s.ymStatus & 0x03
}

// Produce advances the PSG by the clocks owed for frames output frames
// and returns exactly frames interleaved stereo pairs. The PSG is mono,
// so both channels carry the same sample; the YM2612 joins the mix in
// milestone 2.
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
		v := int16(f * 24000)
		s.out[2*i] = v
		s.out[2*i+1] = v
	}
	return s.out
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
	if psgLen > serializeSize-psgStateOffset {
		return errors.New("sound: corrupt PSG state length")
	}
	if psgLen > 0 {
		if err := s.psg.Deserialize(buf[psgStateOffset : psgStateOffset+psgLen]); err != nil {
			return err
		}
	}
	return nil
}
