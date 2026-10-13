// Package ai implements the Nintendo 64 Audio Interface.
//
// The AI streams 16-bit big-endian stereo samples out of
// RDRAM through a DMA engine with a two-deep queue: one
// transfer runs while one more waits behind it. A small
// exact timing model converts CPU cycles into video
// clocks, then into sample periods set by the DAC rate
// register, so the output sample rate is exact over long
// runs with no drift.
//
// Model choices, stated plainly:
//
//   - The interrupt fires when a DMA transfer STARTS, not
//     when it ends. That is the real hardware behavior,
//     established by hardware tests: the CPU uses the
//     start interrupt to enqueue the next buffer early,
//     while the current one is still playing.
//   - The sample rate is the NTSC video clock divided by
//     DACRATE plus one: 48,681,818 / (DACRATE + 1) Hz.
//   - Writing a length while a transfer is running and
//     one is already pending REPLACES the pending
//     transfer. The queue never grows past two.
//   - Samples are consumed only while the DMA enable bit
//     in CONTROL is set. A queued transfer waits, and
//     enabling CONTROL starts it (and fires the interrupt
//     at that start). An idle timing phase never
//     accumulates past one sample period, so a long idle
//     stretch does not dump a burst of samples when a
//     transfer finally starts.
//   - Reads of the write-only registers (DRAM_ADDR,
//     CONTROL, DACRATE, BITRATE) return the number of
//     bytes remaining in the current transfer. That
//     mirror is a documented hardware quirk.
//   - Any write to STATUS acknowledges the interrupt.
//   - The drained-but-unconsumed output buffer is NOT
//     part of a save state. Drain before saving, or the
//     samples buffered since the last Drain are lost in
//     the restore. The asserted interrupt level is not
//     serialized either; the coordinator re-derives it.
//     The started flag is serialized, so a restored
//     running transfer does not fire its start interrupt
//     a second time.
package ai

import (
	"encoding/binary"
	"errors"
)

// Register byte offsets from the AI base at 0x04500000.
const (
	RegDRAMAddr = 0x00
	RegLength   = 0x04
	RegControl  = 0x08
	RegStatus   = 0x0C
	RegDACRate  = 0x10
	RegBitRate  = 0x14
)

// STATUS register bits.
const (
	StatusFull    = 1 << 31 // a transfer is queued behind the current one
	StatusBusy    = 1 << 30 // a transfer is running
	StatusEnabled = 1 << 25 // mirror of the CONTROL DMA enable bit
	StatusConst   = 1 << 24 // reads as 1 on hardware
	StatusFullLow = 1 << 0  // same value as StatusFull
)

// Timing constants. The CPU runs at 93.75 MHz and the
// NTSC video clock is 48,681,818 Hz. Advance keeps an
// exact rational accumulator in video-clock units:
// accNum gains cpuCycles times videoClock per call and
// whole multiples of cpuClock become video clocks.
const (
	cpuClock   = 93750000
	videoClock = 48681818
)

// transfer is one queued DMA transfer: a run of stereo
// samples in RDRAM.
type transfer struct {
	addr      uint32 // current RDRAM byte address
	remaining uint32 // bytes not yet consumed
}

// AI is the audio interface device.
type AI struct {
	dram []byte // attached RDRAM

	dramAddrReg uint32 // DRAM_ADDR register (24-bit, 8-byte aligned)
	control     uint32 // CONTROL register, bit 0 is DMA enable
	dacrate     uint32 // DACRATE register, 14 bits
	bitrate     uint32 // BITRATE register, 4 bits

	cur     transfer
	curOn   bool // a current transfer is loaded
	started bool // the current transfer has started (and fired)

	pend   transfer // pending transfer; its length field holds the full length
	pendOn bool

	accNum int64 // exact timing numerator, video-clock units
	phase  int64 // video clocks accumulated toward the next sample

	out []int16 // buffered output samples, interleaved L, R

	// OnInterrupt, when set, is called with true when a
	// transfer starts and with false when a STATUS write
	// acknowledges the interrupt. The coordinator wires
	// it to the MI AI interrupt bit.
	OnInterrupt func(active bool)
}

// New returns an AI in its power-on state with the given
// RDRAM attached. Pass the same slice the rest of the
// machine uses; samples are read from it in place.
func New(dram []byte) *AI {
	return &AI{dram: dram}
}

// AttachDRAM replaces the RDRAM backing store, for
// example after a machine reset swaps the memory image.
func (a *AI) AttachDRAM(dram []byte) {
	a.dram = dram
}

// enabled reports whether the DMA enable bit is set.
func (a *AI) enabled() bool {
	return a.control&1 != 0
}

// running reports whether samples are being consumed
// right now: a current transfer exists, it has started,
// and DMA is enabled.
func (a *AI) running() bool {
	return a.curOn && a.started && a.enabled()
}

// remaining returns the bytes left in the current
// transfer, or 0 when idle. This is the value LENGTH
// reads back, and the value the write-only registers
// mirror.
func (a *AI) remaining() uint32 {
	if !a.curOn {
		return 0
	}
	return a.cur.remaining
}

// Busy reports whether a current transfer is loaded.
func (a *AI) Busy() bool {
	return a.curOn
}

// Full reports whether a transfer is queued behind the
// current one.
func (a *AI) Full() bool {
	return a.pendOn
}

// SampleRate returns the output sample rate in Hz for the
// programmed DAC rate: 48,681,818 / (DACRATE + 1). A
// DACRATE of 0 gives the full video clock rate.
func (a *AI) SampleRate() int {
	return videoClock / (int(a.dacrate) + 1)
}

// startCurrent marks the current transfer started and
// fires the start interrupt. Callers guarantee a current
// transfer exists and has not started yet.
func (a *AI) startCurrent() {
	a.started = true
	if a.OnInterrupt != nil {
		a.OnInterrupt(true)
	}
}

// ReadRegister reads the 32-bit register at the given
// byte offset. LENGTH reads the bytes remaining in the
// current transfer. The write-only registers (DRAM_ADDR,
// CONTROL, DACRATE, BITRATE) mirror that same remaining
// count on reads, a documented hardware quirk. STATUS
// reads its flag bits.
func (a *AI) ReadRegister(off uint32) uint32 {
	switch off {
	case RegDRAMAddr, RegLength, RegControl, RegDACRate, RegBitRate:
		return a.remaining()
	case RegStatus:
		var s uint32
		s |= StatusConst
		if a.pendOn {
			s |= StatusFull | StatusFullLow
		}
		if a.curOn {
			s |= StatusBusy
		}
		if a.enabled() {
			s |= StatusEnabled
		}
		return s
	}
	return 0
}

// WriteRegister writes the 32-bit register at the given
// byte offset.
//
// DRAM_ADDR keeps 24 bits with the low 3 bits forced to
// zero. A LENGTH write builds a transfer from the
// current DRAM_ADDR value and the length (bits 17-0,
// low 3 bits ignored, a zero length is ignored entirely):
// it becomes the current transfer when the queue is
// empty, otherwise the pending transfer, replacing any
// pending transfer already there. A CONTROL write that
// enables DMA starts a waiting current transfer and
// fires its start interrupt. Any STATUS write
// acknowledges the interrupt.
func (a *AI) WriteRegister(off uint32, val uint32) {
	switch off {
	case RegDRAMAddr:
		a.dramAddrReg = val & 0xFFFFFF &^ 7
	case RegLength:
		length := val & 0x3FFF8
		if length == 0 {
			return
		}
		t := transfer{addr: a.dramAddrReg, remaining: length}
		if !a.curOn {
			a.cur = t
			a.curOn = true
			a.started = false
			if a.enabled() {
				a.startCurrent()
			}
		} else {
			// One running, one pending: a third write
			// replaces the pending transfer.
			a.pend = t
			a.pendOn = true
		}
	case RegControl:
		a.control = val & 1
		if a.enabled() && a.curOn && !a.started {
			a.startCurrent()
		}
	case RegStatus:
		if a.OnInterrupt != nil {
			a.OnInterrupt(false)
		}
	case RegDACRate:
		a.dacrate = val & 0x3FFF
	case RegBitRate:
		a.bitrate = val & 0xF
	}
}

// finishCurrent retires a completed current transfer and
// promotes the pending transfer, if any, starting it
// (and firing its start interrupt) when DMA is enabled.
func (a *AI) finishCurrent() {
	if a.pendOn {
		a.cur = a.pend
		a.curOn = true
		a.started = false
		a.pend = transfer{}
		a.pendOn = false
		if a.enabled() {
			a.startCurrent()
		}
	} else {
		a.cur = transfer{}
		a.curOn = false
		a.started = false
	}
}

// consumeSample reads one stereo sample frame (4 bytes:
// left high, left low, right high, right low) from the
// current transfer address, appends both channels to the
// output buffer, and advances the transfer. Addresses
// outside RDRAM read as silence.
func (a *AI) consumeSample() {
	addr := int(a.cur.addr & 0xFFFFFF)
	var l, r int16
	if addr >= 0 && addr+4 <= len(a.dram) {
		l = int16(binary.BigEndian.Uint16(a.dram[addr:]))
		r = int16(binary.BigEndian.Uint16(a.dram[addr+2:]))
	}
	a.out = append(a.out, l, r)
	a.cur.addr += 4
	a.cur.remaining -= 4
	if a.cur.remaining == 0 {
		a.finishCurrent()
	}
}

// Advance runs the sample timing for the given number of
// CPU cycles.
//
// The accumulator is exact: accNum gains cpuCycles times
// the video clock, whole multiples of the CPU clock
// become video clocks, and the remainder carries over.
// Video clocks feed the sample phase; each time the
// phase reaches the DAC period (DACRATE + 1 video
// clocks) one stereo sample is consumed from the running
// transfer. While no transfer is running the phase is
// clamped to one period minus one, so idle time cannot
// build up an unbounded backlog.
func (a *AI) Advance(cpuCycles int) {
	if cpuCycles <= 0 {
		return
	}
	a.accNum += int64(cpuCycles) * videoClock
	viClocks := a.accNum / cpuClock
	a.accNum -= viClocks * cpuClock
	a.phase += viClocks

	period := int64(a.dacrate) + 1
	for a.phase >= period && a.running() {
		a.phase -= period
		a.consumeSample()
	}
	if !a.running() && a.phase > period-1 {
		a.phase = period - 1
	}
}

// Drain returns the buffered output samples, interleaved
// left and right, and resets the buffer. It returns nil
// when no samples have been consumed since the last
// Drain. The returned slice is owned by the caller.
func (a *AI) Drain() []int16 {
	if len(a.out) == 0 {
		return nil
	}
	out := a.out
	a.out = nil
	return out
}

// SerializeSize is the fixed byte size of a serialized AI.
//
// Layout, all big-endian:
//
//	 0: DRAM_ADDR register, uint32
//	 4: CONTROL register, uint32
//	 8: DACRATE register, uint32
//	12: BITRATE register, uint32
//	16: current transfer address, uint32
//	20: current transfer bytes remaining, uint32
//	24: current transfer active flag, uint32 (0 or 1)
//	28: pending transfer address, uint32
//	32: pending transfer length, uint32
//	36: pending transfer flag, uint32 (0 or 1)
//	40: timing accumulator, uint64
//	48: sample phase, uint64
//	56: started flag, uint32 (0 or 1)
//
// The output buffer and the interrupt level are not in
// the layout; see the package documentation.
const SerializeSize = 10*4 + 8 + 8 + 4

// Serialize writes the AI state into dst, which must be
// at least SerializeSize bytes, and returns the number
// of bytes written. The layout is documented on
// SerializeSize.
func (a *AI) Serialize(dst []byte) int {
	binary.BigEndian.PutUint32(dst[0:], a.dramAddrReg)
	binary.BigEndian.PutUint32(dst[4:], a.control)
	binary.BigEndian.PutUint32(dst[8:], a.dacrate)
	binary.BigEndian.PutUint32(dst[12:], a.bitrate)
	binary.BigEndian.PutUint32(dst[16:], a.cur.addr)
	binary.BigEndian.PutUint32(dst[20:], a.cur.remaining)
	var curFlag, pendFlag, startedFlag uint32
	if a.curOn {
		curFlag = 1
	}
	if a.pendOn {
		pendFlag = 1
	}
	if a.started {
		startedFlag = 1
	}
	binary.BigEndian.PutUint32(dst[24:], curFlag)
	binary.BigEndian.PutUint32(dst[28:], a.pend.addr)
	binary.BigEndian.PutUint32(dst[32:], a.pend.remaining)
	binary.BigEndian.PutUint32(dst[36:], pendFlag)
	binary.BigEndian.PutUint64(dst[40:], uint64(a.accNum))
	binary.BigEndian.PutUint64(dst[48:], uint64(a.phase))
	binary.BigEndian.PutUint32(dst[56:], startedFlag)
	return SerializeSize
}

// Deserialize restores a state written by Serialize. The
// DRAM attachment and the OnInterrupt callback are not
// part of the state; attach DRAM to a fresh AI before or
// after restoring. The output buffer is cleared, since
// buffered samples are not serialized.
func (a *AI) Deserialize(in []byte) error {
	if len(in) < SerializeSize {
		return errors.New("ai: state too short")
	}
	a.dramAddrReg = binary.BigEndian.Uint32(in[0:])
	a.control = binary.BigEndian.Uint32(in[4:])
	a.dacrate = binary.BigEndian.Uint32(in[8:])
	a.bitrate = binary.BigEndian.Uint32(in[12:])
	a.cur.addr = binary.BigEndian.Uint32(in[16:])
	a.cur.remaining = binary.BigEndian.Uint32(in[20:])
	a.curOn = binary.BigEndian.Uint32(in[24:]) != 0
	a.pend.addr = binary.BigEndian.Uint32(in[28:])
	a.pend.remaining = binary.BigEndian.Uint32(in[32:])
	a.pendOn = binary.BigEndian.Uint32(in[36:]) != 0
	a.accNum = int64(binary.BigEndian.Uint64(in[40:]))
	a.phase = int64(binary.BigEndian.Uint64(in[48:]))
	a.started = binary.BigEndian.Uint32(in[56:]) != 0
	a.out = nil
	return nil
}
