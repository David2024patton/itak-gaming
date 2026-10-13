// Package ai models the GameCube audio interface: the
// control, volume, sample counter and interrupt timing
// registers, and the audio DMA engine that streams big
// endian stereo samples out of MEM1 at 48000 Hz or
// 32000 Hz.
//
// The MMIO window base is 0x0C006C00. All access here is
// through 16 bit lanes at offsets inside that window: a
// 32 bit register occupies two lanes, the high half at
// the even offset and the low half at the next offset.
// Side effects trigger on the low lane write.
package ai

import (
	"encoding/binary"
	"errors"
)

// Mem is physical MEM1 as the AI sees it: byte reads at
// zero based physical addresses.
type Mem interface {
	Read8(addr uint32) uint8
}

// AICR bit definitions (low half of the register).
const (
	aicrPSTAT    = 1 << 0 // stream playing
	aicrAIINTMSK = 1 << 1 // AI interrupt mask
	aicrAIINT    = 1 << 2 // AI interrupt status, write 1 to clear
	aicrAIINTVLD = 1 << 3 // interrupt timing valid, stored
	aicrSCRST    = 1 << 4 // sample counter reset, write 1
	aicrAISFR    = 1 << 5 // sample rate select: 0 is 48000, 1 is 32000
)

// Register offsets inside the MMIO window.
const (
	regAICRHigh      = 0x00
	regAICRLow       = 0x02
	regAIVRHigh      = 0x04
	regAIVRLow       = 0x06
	regCounterHigh   = 0x08
	regCounterLow    = 0x0A
	regTimingHigh    = 0x0C
	regTimingLow     = 0x0E
	regDMAAddrH      = 0x20
	regDMAAddrL      = 0x22
	regDMALenH       = 0x24
	regDMALenL       = 0x26
	dmaEnable        = 0x8000 // bit 15 of DMA_LEN_H starts the DMA
	cpuClock         = 486_000_000
	outCapFrames     = 1 << 16 // stereo frames kept in a save state
	serializeVersion = 1
)

// AI is the audio interface device.
type AI struct {
	mem Mem

	// OnAIDInt, when non nil, is called whenever the
	// AIINT status bit changes, so the system can mirror
	// it into the DSP control register.
	OnAIDInt func(on bool)

	aicr        uint32 // AIINT and SCRST handled specially, see Write16
	aivr        uint32
	counter     uint32 // stereo frames consumed from the DMA stream
	timing      uint32 // AIINTTIMING
	timingArmed bool   // the timing crossing may still fire

	addrH uint16
	addrL uint16
	lenH  uint16 // bit 15 is the DMA enable
	lenL  uint16

	curAddr   uint32 // current DMA stream address
	remaining uint32 // DMA bytes not yet consumed
	enabled   bool   // DMA stream started and not finished

	acc int64   // cycle to frame ratio accumulator
	out []int16 // buffered interleaved stereo output
}

// New returns an audio interface in its reset state,
// reading sample data from mem (which may be nil, in
// which case the stream reads as silence).
func New(mem Mem) *AI {
	a := &AI{mem: mem}
	a.Reset()
	return a
}

// Reset restores the power on state: every register
// zero, the sample counter stopped at zero, the timing
// interrupt rearmed, no DMA in flight and an empty
// output buffer. The memory link and the OnAIDInt hook
// are kept.
func (a *AI) Reset() {
	was := a.aicr&aicrAIINT != 0
	mem, cb := a.mem, a.OnAIDInt
	*a = AI{mem: mem, OnAIDInt: cb, timingArmed: true}
	if was && cb != nil {
		cb(false)
	}
}

// Rate returns the AICR selected sample rate in Hz:
// 48000 normally, 32000 when AISFR is set.
func (a *AI) Rate() int {
	if a.aicr&aicrAISFR != 0 {
		return 32000
	}
	return 48000
}

// IRQAsserted reports whether the AI interrupt is both
// pending (AIINT) and unmasked (AIINTMSK).
func (a *AI) IRQAsserted() bool {
	return a.aicr&aicrAIINT != 0 && a.aicr&aicrAIINTMSK != 0
}

// setAIINT moves the AIINT status bit and reports the
// change through OnAIDInt.
func (a *AI) setAIINT(on bool) {
	cur := a.aicr&aicrAIINT != 0
	if cur == on {
		return
	}
	if on {
		a.aicr |= aicrAIINT
	} else {
		a.aicr &^= aicrAIINT
	}
	if a.OnAIDInt != nil {
		a.OnAIDInt(on)
	}
}

// Read16 reads one 16 bit MMIO lane at off.
func (a *AI) Read16(off uint32) uint16 {
	var v uint16
	switch off {
	case regAICRHigh:
		v = uint16(a.aicr >> 16)
	case regAICRLow:
		v = uint16(a.aicr)
	case regAIVRHigh:
		v = uint16(a.aivr >> 16)
	case regAIVRLow:
		v = uint16(a.aivr)
	case regCounterHigh:
		v = uint16(a.counter >> 16)
	case regCounterLow:
		v = uint16(a.counter)
	case regTimingHigh:
		v = uint16(a.timing >> 16)
	case regTimingLow:
		v = uint16(a.timing)
	case regDMAAddrH:
		v = a.addrH
	case regDMAAddrL:
		v = a.addrL
	case regDMALenH:
		v = a.lenH
	case regDMALenL:
		v = a.lenL
	}
	if off == regAICRHigh || off == regAICRLow {
		// SCRST is self clearing on read.
		a.aicr &^= aicrSCRST
	}
	return v
}

// Write16 writes one 16 bit MMIO lane at off.
func (a *AI) Write16(off uint32, v uint16) {
	switch off {
	case regAICRHigh:
		a.aicr = a.aicr&0x0000FFFF | uint32(v)<<16
	case regAICRLow:
		a.writeAICRLow(v)
	case regAIVRHigh:
		a.aivr = a.aivr&0x0000FFFF | uint32(v)<<16
	case regAIVRLow:
		a.aivr = a.aivr&0xFFFF0000 | uint32(v)
	case regCounterHigh, regCounterLow:
		// The sample counter is read only.
	case regTimingHigh:
		a.timing = a.timing&0x0000FFFF | uint32(v)<<16
		a.timingArmed = true
	case regTimingLow:
		a.timing = a.timing&0xFFFF0000 | uint32(v)
		a.timingArmed = true
	case regDMAAddrH:
		a.addrH = v
	case regDMAAddrL:
		a.addrL = v
	case regDMALenH:
		a.lenH = v
		if v&dmaEnable != 0 {
			a.startDMA()
		} else {
			a.enabled = false
		}
	case regDMALenL:
		a.lenL = v
	}
}

// writeAICRLow applies the low lane rules of AICR: AIINT
// is write 1 to clear and can never be set by software,
// and a 1 in SCRST resets the sample counter, rearms
// the timing interrupt and is not stored.
func (a *AI) writeAICRLow(v uint16) {
	aiintAfter := a.aicr&aicrAIINT != 0
	if v&aicrAIINT != 0 {
		aiintAfter = false
	}
	low := uint32(v) &^ (aicrAIINT | aicrSCRST)
	// Keep the current AIINT in place until setAIINT has
	// compared it: clearing it here first would make
	// every write look like a fresh assert and would
	// swallow the deassert on a write 1 to clear.
	if a.aicr&aicrAIINT != 0 {
		low |= aicrAIINT
	}
	a.aicr = a.aicr&0xFFFF0000 | low
	a.setAIINT(aiintAfter)
	if v&aicrSCRST != 0 {
		a.counter = 0
		a.timingArmed = true
	}
}

// startDMA snapshots the address and length registers
// into the playback state. A masked length of zero is
// no transfer at all and raises the interrupt at once.
func (a *AI) startDMA() {
	a.curAddr = uint32(a.addrH)<<16 | uint32(a.addrL&0xFFE0)
	length := (uint32(a.lenH&0x1F)<<16 | uint32(a.lenL)) &^ 0x1F
	a.remaining = length
	if length == 0 {
		a.enabled = false
		a.lenH &^= dmaEnable
		a.setAIINT(true)
		return
	}
	a.enabled = true
}

// volume returns the effective per side volume, clamped
// to the 0 to 127 scale.
func (a *AI) volume(left bool) int32 {
	var raw uint32
	if left {
		raw = (a.aivr >> 16) & 0xFF
	} else {
		raw = a.aivr & 0xFF
	}
	if raw > 127 {
		raw = 127
	}
	return int32(raw)
}

// mem8 reads one byte of the DMA stream source.
func (a *AI) mem8(addr uint32) uint32 {
	if a.mem == nil {
		return 0
	}
	return uint32(a.mem.Read8(addr))
}

// Advance clocks the interface forward by cpuCycles CPU
// cycles. An exact ratio accumulator converts cycles at
// 486 MHz into stereo frames at the current rate, so no
// drift accumulates across calls. While PSTAT is clear
// no frames are produced at all, and the cycles are not
// banked for later. For each frame produced while
// playing: if the DMA stream has at least 4 bytes left,
// one big endian stereo pair is consumed, the sample
// counter advances and the volume scaled pair joins the
// output buffer; otherwise a silent frame is produced
// (the DAC keeps clocking) and the counter stays put,
// because it counts DMA samples only. When the stream
// runs out, AIINT is set and the DMA enable bit clears.
// The timing interrupt fires once when the counter
// reaches AIINTTIMING; it rearms only when the counter
// is reset through SCRST or AIINTTIMING is rewritten.
func (a *AI) Advance(cpuCycles int) {
	if cpuCycles <= 0 || a.aicr&aicrPSTAT == 0 {
		return
	}
	a.acc += int64(cpuCycles) * int64(a.Rate())
	frames := a.acc / cpuClock
	a.acc %= cpuClock
	volL, volR := a.volume(true), a.volume(false)
	for i := int64(0); i < frames; i++ {
		if a.enabled && a.remaining >= 4 {
			rawL := int16(a.mem8(a.curAddr)<<8 | a.mem8(a.curAddr+1))
			rawR := int16(a.mem8(a.curAddr+2)<<8 | a.mem8(a.curAddr+3))
			a.curAddr += 4
			a.remaining -= 4
			a.counter++
			a.out = append(a.out,
				int16(int32(rawL)*volL/127),
				int16(int32(rawR)*volR/127))
			if a.timingArmed && a.timing != 0 && a.counter == a.timing {
				a.timingArmed = false
				a.setAIINT(true)
			}
			if a.remaining == 0 {
				a.enabled = false
				a.lenH &^= dmaEnable
				a.setAIINT(true)
			}
		} else {
			a.out = append(a.out, 0, 0)
		}
	}
}

// Drain returns the buffered output as interleaved
// stereo samples at the current rate and empties the
// buffer.
func (a *AI) Drain() []int16 {
	out := a.out
	a.out = nil
	return out
}

// SerializeSize is the fixed save state size: a version
// byte, the registers, the DMA playback state, the
// ratio accumulator, and the output buffer capped at
// outCapFrames stereo frames.
const SerializeSize = 1 +
	4 + 4 + 4 + 4 + // AICR, AIVR, counter, timing
	2 + 2 + 2 + 2 + // DMA address and length lanes
	4 + 4 + // current address, remaining bytes
	8 + // ratio accumulator
	1 + 1 + // timing armed, DMA enabled
	4 + // buffered frame count
	outCapFrames*4 // buffered interleaved samples

// ErrBadState reports a state block of the wrong size
// or version.
var ErrBadState = errors.New("ai: bad save state")

const serializeVersionByte = serializeVersion

// Serialize writes the complete AI state. Output past
// outCapFrames frames is not kept in the state; states
// are normally taken after a Drain, well under the cap.
func (a *AI) Serialize() []byte {
	out := make([]byte, SerializeSize)
	out[0] = serializeVersionByte
	off := 1
	put32 := func(v uint32) {
		binary.BigEndian.PutUint32(out[off:], v)
		off += 4
	}
	put16 := func(v uint16) {
		binary.BigEndian.PutUint16(out[off:], v)
		off += 2
	}
	put32(a.aicr &^ aicrSCRST)
	put32(a.aivr)
	put32(a.counter)
	put32(a.timing)
	put16(a.addrH)
	put16(a.addrL)
	put16(a.lenH)
	put16(a.lenL)
	put32(a.curAddr)
	put32(a.remaining)
	binary.BigEndian.PutUint64(out[off:], uint64(a.acc))
	off += 8
	if a.timingArmed {
		out[off] = 1
	}
	off++
	if a.enabled {
		out[off] = 1
	}
	off++
	frames := len(a.out) / 2
	if frames > outCapFrames {
		frames = outCapFrames
	}
	put32(uint32(frames))
	for i := 0; i < frames*2; i++ {
		put16(uint16(a.out[i]))
	}
	off += (outCapFrames - frames) * 4
	if off != SerializeSize {
		panic("ai: serialize size mismatch")
	}
	return out
}

// Deserialize restores a state written by Serialize.
func (a *AI) Deserialize(in []byte) error {
	if len(in) != SerializeSize || in[0] != serializeVersionByte {
		return ErrBadState
	}
	was := a.aicr&aicrAIINT != 0
	off := 1
	get32 := func() uint32 {
		v := binary.BigEndian.Uint32(in[off:])
		off += 4
		return v
	}
	get16 := func() uint16 {
		v := binary.BigEndian.Uint16(in[off:])
		off += 2
		return v
	}
	a.aicr = get32()
	a.aivr = get32()
	a.counter = get32()
	a.timing = get32()
	a.addrH = get16()
	a.addrL = get16()
	a.lenH = get16()
	a.lenL = get16()
	a.curAddr = get32()
	a.remaining = get32()
	a.acc = int64(binary.BigEndian.Uint64(in[off:]))
	off += 8
	a.timingArmed = in[off] != 0
	off++
	a.enabled = in[off] != 0
	off++
	frames := int(get32())
	if frames > outCapFrames {
		return ErrBadState
	}
	a.out = make([]int16, 0, frames*2)
	for i := 0; i < frames*2; i++ {
		a.out = append(a.out, int16(get16()))
	}
	now := a.aicr&aicrAIINT != 0
	if was != now && a.OnAIDInt != nil {
		a.OnAIDInt(now)
	}
	return nil
}
