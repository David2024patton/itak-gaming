// Package aica implements the Dreamcast AICA sound subsystem: 64 PCM
// and ADPCM slots with envelope generators and LFOs, a 128 step DSP,
// three timers, a shared interrupt controller, an internal DMA engine,
// a real time clock, and the ARM7 sound CPU that drives it all.
//
// Hardware facts follow section 8 of the official Sega Dev.Box System
// Architecture document. Registers are 16 bit values at a 4 byte
// stride in a window shared by the SH4 and the ARM7. Only the low 16
// bits of a 32 bit access are valid, except the RTC window and the
// 24 bit DSP data areas, which are true 32 bit.
package aica

import (
	arm7 "github.com/davidpatton/itak-gaming/cores/go-dreamcast/chips/arm7"
)

// ASICSink is the Holly external interrupt bank. AICA uses bit 1.
type ASICSink interface {
	RaiseExternal(bits uint32)
	ClearExternal(bits uint32)
}

const (
	regWindowBase = 0x00700000
	regWindowSize = 0x00008000
	rtcBase       = 0x00710000

	armRegBase = 0x00800000
	armRAMSize = 0x00200000

	sampleMasterCycles = 512
	masterHz           = 22579200

	outCap = 16384
)

// AudioSampleRate returns the fixed output sample rate.
func AudioSampleRate() int { return 44100 }

type timer struct {
	counter uint8
	divider int
}

// AICA is the whole sound subsystem.
type AICA struct {
	ram  []byte
	sink ASICSink
	arm  *arm7.CPU

	// Raw register storage as bytes, indexed by window byte offset.
	// Computed readbacks (monitor, pending, timers) layer on top.
	raw [regWindowSize]byte

	coef  [128]uint16
	madrs [64]uint16
	mpro  [128][4]uint16

	temp  [128]int32
	mems  [32]int32
	mixs  [16]int32
	efreg [16]int32

	dsp dspState

	slots [64]slot

	timers [3]timer

	pending      uint16
	presentedSrc int
	presentedLvl int
	fiqAsserted  bool
	sinkRaised   bool
	armRP        bool

	out [][2]int16

	masterRem int
	armRem    int

	rtcSeconds uint32
	rtcRem     int
	rtcEnable  bool
}

// New creates an AICA over the shared 2 MB wave RAM slice. The ARM7
// starts held in reset (ARMRST power on value is 1).
func New(ram []byte, sink ASICSink) *AICA {
	a := &AICA{ram: ram, sink: sink, presentedSrc: -1}
	a.setRaw16(0x2C00, 1)
	a.arm = arm7.New(&armBus{a: a})
	return a
}

// ARM exposes the sound CPU for tests and integration.
func (a *AICA) ARM() *arm7.CPU { return a.arm }

func (a *AICA) armHeld() bool {
	return a.getRaw16(0x2C00)&1 != 0
}

// Raw register helpers. off is a window byte offset.

func (a *AICA) getRaw16(off uint32) uint16 {
	if off+1 >= regWindowSize {
		return 0
	}
	return uint16(a.raw[off]) | uint16(a.raw[off+1])<<8
}

func (a *AICA) setRaw16(off uint32, v uint16) {
	if off+1 >= regWindowSize {
		return
	}
	a.raw[off] = byte(v)
	a.raw[off+1] = byte(v >> 8)
}

// isDSPWordArea reports whether off holds a true 24 bit value.
func dspWordArea(off uint32) (base uint32, count int, ok bool) {
	switch {
	case off >= 0x4000 && off < 0x4200:
		return 0x4000, 128, true
	case off >= 0x4400 && off < 0x4480:
		return 0x4400, 32, true
	case off >= 0x4800 && off < 0x4840:
		return 0x4800, 16, true
	case off >= 0x4C00 && off < 0x4C40:
		return 0x4C00, 16, true
	}
	return 0, 0, false
}

func (a *AICA) dspWordGet(off uint32) int32 {
	base, _, ok := dspWordArea(off)
	if !ok {
		return 0
	}
	idx := (off - base) >> 2
	switch base {
	case 0x4000:
		return a.temp[idx]
	case 0x4400:
		return a.mems[idx]
	case 0x4800:
		return a.mixs[idx]
	case 0x4C00:
		return int32(int16(a.efreg[idx]))
	}
	return 0
}

func (a *AICA) dspWordSet(off uint32, v int32) {
	base, _, ok := dspWordArea(off)
	if !ok {
		return
	}
	idx := (off - base) >> 2
	switch base {
	case 0x4000:
		a.temp[idx] = wrap24(v)
	case 0x4400:
		a.mems[idx] = wrap24(v)
	case 0x4800:
		// CPU writes to MIXS are ignored per the documentation.
	case 0x4C00:
		a.efreg[idx] = v
	}
}

// read16 returns the stored or computed 16 bit register at off.
func (a *AICA) read16(off uint32, fromARM bool) uint16 {
	if base, _, ok := dspWordArea(off &^ 3); ok {
		v := a.dspWordGet(base + ((off &^ 3) - base))
		if off&2 != 0 {
			return uint16(uint32(v) >> 16)
		}
		return uint16(v)
	}
	switch {
	case off >= 0x3000 && off < 0x3200:
		return a.coef[(off-0x3000)>>2]
	case off >= 0x3200 && off < 0x3300:
		return a.madrs[(off-0x3200)>>2]
	case off >= 0x3400 && off < 0x3C00:
		idx := (off - 0x3400) >> 2
		return a.mpro[idx>>2][idx&3]
	}
	switch off {
	case 0x2808:
		// MIDI status stub: output empty reads 1, input empty reads 1.
		return (1 << 11) | (1 << 8)
	case 0x2810:
		return a.monitorValue()
	case 0x2814:
		return a.caValue()
	case 0x2890, 0x2894, 0x2898:
		t := &a.timers[(off-0x2890)>>2]
		return (a.getRaw16(off) & 0x0700) | uint16(t.counter)
	case 0x28A0, 0x28B8:
		return a.pending
	case 0x28A4, 0x28BC:
		return 0
	case 0x2C00:
		if fromARM {
			return 0
		}
		return a.getRaw16(off)
	case 0x2D00:
		if !fromARM {
			return 0
		}
		if a.presentedSrc < 0 {
			return 0
		}
		return uint16(a.presentedLvl)
	case 0x2D04:
		return 0
	}
	return a.getRaw16(off)
}

// write16 stores a register and applies side effects when fx is set.
// DMA transfers call it with fx clear through raw writes instead.
func (a *AICA) write16(off uint32, v uint16, fromARM bool, fx bool) {
	if _, _, ok := dspWordArea(off); ok {
		cur := a.dspWordGet(off)
		if off&2 != 0 {
			a.dspWordSet(off, int32(uint32(cur)&0xFFFF|uint32(v)<<16))
		} else {
			a.dspWordSet(off, int32(uint32(cur)&0xFFFF0000|uint32(v)))
		}
		return
	}
	if off&2 != 0 {
		// Packed half of a 32 bit stride slot: only the DSP word
		// areas and DMA targets use it. Plain registers ignore it,
		// except raw packed storage used by the internal DMA.
		if fx {
			return
		}
		a.setRaw16(off, v)
		return
	}
	switch {
	case off >= 0x3000 && off < 0x3200:
		a.coef[(off-0x3000)>>2] = v
		return
	case off >= 0x3200 && off < 0x3300:
		a.madrs[(off-0x3200)>>2] = v
		return
	case off >= 0x3400 && off < 0x3C00:
		idx := (off - 0x3400) >> 2
		a.mpro[idx>>2][idx&3] = v
		return
	}
	switch off {
	case 0x2808, 0x2810, 0x2814:
		return // read only or stub status
	case 0x2C00:
		if fromARM {
			return
		}
		old := a.getRaw16(off)
		a.setRaw16(off, v)
		if fx {
			was := old & 1
			now := v & 1
			if was == 1 && now == 0 && a.arm != nil {
				a.arm.Reset()
			}
		}
		return
	case 0x2D00:
		return
	case 0x2D04:
		if !fromARM {
			return
		}
		if v&(1<<8) != 0 {
			a.armRP = true
		} else {
			a.armRP = false
		}
		if fx && v&1 != 0 {
			a.eoi()
		}
		return
	case 0x28A0, 0x28B8:
		// Only the CPU request bit can be set by writing.
		if fx && v&(1<<5) != 0 {
			a.setPending(1 << 5)
		}
		return
	case 0x28A4, 0x28BC:
		if fx && v != 0 {
			a.clearPending(v & 0x7FF)
		}
		return
	case 0x2890, 0x2894, 0x2898:
		a.setRaw16(off, v&0x0700)
		a.timers[(off-0x2890)>>2].counter = uint8(v)
		return
	}
	a.setRaw16(off, v)
	if !fx {
		return
	}
	switch {
	case off < 0x2000 && off&0x7F == 0:
		if v&(1<<15) != 0 {
			a.applyKeyOnEx()
		}
	case off == 0x280C:
		// A MOBUF write drains at once and raises the MIDI out source.
		a.setPending(1 << 9)
	case off == 0x288C:
		if v&1 != 0 {
			a.runDMA()
		}
	case off == 0x289C || off == 0x28B4 || off == 0x28A8 || off == 0x28AC || off == 0x28B0:
		a.recomputeInterrupts()
	}
}

// read32 is the register window read at a window offset.
func (a *AICA) read32(off uint32, fromARM bool) uint32 {
	if _, _, ok := dspWordArea(off); ok {
		v := a.dspWordGet(off)
		if off >= 0x4800 && off < 0x4840 {
			return uint32(v & 0xFFFFF)
		}
		if off >= 0x4C00 {
			return uint32(uint16(v))
		}
		return uint32(v & 0xFFFFFF)
	}
	return uint32(a.read16(off, fromARM))
}

// write32 is the register window write at a window offset.
func (a *AICA) write32(off uint32, v uint32, fromARM bool) {
	if _, _, ok := dspWordArea(off); ok {
		if off >= 0x4C00 {
			a.dspWordSet(off, int32(int16(v)))
		} else {
			a.dspWordSet(off, signExtend(v&0xFFFFFF, 24))
		}
		return
	}
	a.write16(off, uint16(v), fromARM, true)
}

// Read32 is the SH4 view of the register and RTC windows.
func (a *AICA) Read32(pa uint32) uint32 {
	if pa >= rtcBase && pa < rtcBase+12 {
		return a.readRTC(pa - rtcBase)
	}
	if pa >= regWindowBase && pa < regWindowBase+regWindowSize {
		return a.read32(pa-regWindowBase, false)
	}
	return 0
}

// Write32 is the SH4 write view of the register and RTC windows.
func (a *AICA) Write32(pa uint32, v uint32) {
	if pa >= rtcBase && pa < rtcBase+12 {
		a.writeRTC(pa-rtcBase, v)
		return
	}
	if pa >= regWindowBase && pa < regWindowBase+regWindowSize {
		a.write32(pa-regWindowBase, v, false)
	}
}

// RTC: one 32 bit seconds counter split across two addresses. Writes
// are honored only when the enable register was written 1 since the
// last RTC write; the enable self clears after a write. This gated
// write model is a deliberate simplification of the hardware latch
// sequence.
func (a *AICA) readRTC(off uint32) uint32 {
	switch off {
	case 0:
		return a.rtcSeconds & 0xFFFF0000
	case 4:
		return a.rtcSeconds & 0xFFFF
	case 8:
		if a.rtcEnable {
			return 1
		}
		return 0
	}
	return 0
}

func (a *AICA) writeRTC(off uint32, v uint32) {
	switch off {
	case 8:
		a.rtcEnable = v&1 != 0
	case 0:
		if a.rtcEnable {
			a.rtcSeconds = (a.rtcSeconds & 0xFFFF) | (v & 0xFFFF0000)
			a.rtcEnable = false
		}
	case 4:
		if a.rtcEnable {
			a.rtcSeconds = (a.rtcSeconds & 0xFFFF0000) | (v & 0xFFFF)
			a.rtcEnable = false
		}
	}
}

// Interrupts

func (a *AICA) setPending(bits uint16) {
	if a.pending&bits == bits {
		return
	}
	a.pending |= bits
	a.recomputeInterrupts()
}

func (a *AICA) clearPending(bits uint16) {
	a.pending &^= bits
	a.recomputeInterrupts()
}

func (a *AICA) eoi() {
	if a.presentedSrc >= 0 {
		a.pending &^= 1 << uint(a.presentedSrc)
	}
	a.recomputeInterrupts()
}

func (a *AICA) levelOf(src int) int {
	key := src
	if key > 7 {
		key = 7
	}
	lvl := 0
	for j := 0; j < 3; j += 1 {
		reg := a.getRaw16(0x28A8 + uint32(j)*4)
		if reg>>uint(key)&1 != 0 {
			lvl |= 1 << uint(j)
		}
	}
	return lvl
}

// recomputeInterrupts picks the presented source for the ARM7: among
// pending and SCIEB, the highest level wins, ties go to the lowest
// source index. It also drives the SH4 external line through MCIEB.
func (a *AICA) recomputeInterrupts() {
	en := a.getRaw16(0x289C)
	best := -1
	bestLvl := -1
	for s := 0; s < 11; s += 1 {
		if a.pending&(1<<uint(s)) == 0 || en&(1<<uint(s)) == 0 {
			continue
		}
		l := a.levelOf(s)
		if l > bestLvl {
			bestLvl = l
			best = s
		}
	}
	a.presentedSrc = best
	if best >= 0 {
		a.presentedLvl = bestLvl
	} else {
		a.presentedLvl = 0
	}
	a.fiqAsserted = best >= 0
	if a.arm != nil {
		a.arm.SetFIQ(a.fiqAsserted)
	}
	if a.sink != nil {
		want := a.pending&a.getRaw16(0x28B4) != 0
		if want && !a.sinkRaised {
			a.sink.RaiseExternal(1 << 1)
			a.sinkRaised = true
		}
		if !want && a.sinkRaised {
			a.sink.ClearExternal(1 << 1)
			a.sinkRaised = false
		}
	}
}

// NotifyG2DMAEnd raises the DMA end interrupt source.
func (a *AICA) NotifyG2DMAEnd() {
	a.setPending(1 << 4)
}

// Internal DMA. DEXE write runs the transfer synchronously. Register
// area writes use raw storage only, so a transfer cannot retrigger
// key on or other side effects.
func (a *AICA) dmaWaveAddr() uint32 {
	hi := uint32(a.getRaw16(0x2880) & 0x7F)
	lo := uint32(a.getRaw16(0x2884) & 0x7FFF)
	return hi<<16 | lo<<2
}

func (a *AICA) ramIndex(addr uint32) int {
	if len(a.ram) == 0 {
		return 0
	}
	if len(a.ram)&(len(a.ram)-1) == 0 {
		return int(addr & uint32(len(a.ram)-1))
	}
	return int(addr % uint32(len(a.ram)))
}

func (a *AICA) ramRead32(addr uint32) uint32 {
	if len(a.ram) < 4 {
		return 0
	}
	v := uint32(0)
	for k := 0; k < 4; k += 1 {
		v |= uint32(a.ram[a.ramIndex(addr+uint32(k))]) << uint(8*k)
	}
	return v
}

func (a *AICA) ramWrite32(addr uint32, v uint32) {
	for k := 0; k < 4; k += 1 {
		if len(a.ram) == 0 {
			return
		}
		a.ram[a.ramIndex(addr+uint32(k))] = byte(v >> uint(8*k))
	}
}

func (a *AICA) runDMA() {
	reg := a.getRaw16(0x288C)
	count := int((reg >> 2) & 0x1FFF)
	gate := a.getRaw16(0x2888)&(1<<15) != 0
	dirToWave := reg&(1<<15) != 0
	regField := uint32((a.getRaw16(0x2888) >> 2) & 0x1FFF)
	wave := a.dmaWaveAddr()
	for i := 0; i < count; i += 1 {
		regOff := regField*4 + uint32(i)*4
		waddr := wave + uint32(i)*4
		if regOff >= 0x45C8 {
			continue
		}
		if dirToWave {
			var v uint32
			if gate {
				v = 0
			} else {
				lo := a.dmaRawRead16(regOff)
				hi := a.dmaRawRead16(regOff + 2)
				v = uint32(lo) | uint32(hi)<<16
			}
			a.ramWrite32(waddr, v)
		} else {
			v := a.ramRead32(waddr)
			if gate {
				v = 0
			}
			a.dmaRawWrite16(regOff, uint16(v))
			a.dmaRawWrite16(regOff+2, uint16(v>>16))
		}
	}
	a.setRaw16(0x288C, reg&^(1))
	a.setPending(1 << 4)
}

// dmaRawRead16 reads register storage with no side effects and no
// computed readbacks, except DSP word areas which keep their arrays.
func (a *AICA) dmaRawRead16(off uint32) uint16 {
	if _, _, ok := dspWordArea(off &^ 3); ok {
		v := a.dspWordGet(off &^ 3)
		if off&2 != 0 {
			return uint16(uint32(v) >> 16)
		}
		return uint16(v)
	}
	switch {
	case off >= 0x3000 && off < 0x3200:
		return a.coef[(off-0x3000)>>2]
	case off >= 0x3200 && off < 0x3300:
		return a.madrs[(off-0x3200)>>2]
	case off >= 0x3400 && off < 0x3C00:
		idx := (off - 0x3400) >> 2
		return a.mpro[idx>>2][idx&3]
	}
	return a.getRaw16(off)
}

func (a *AICA) dmaRawWrite16(off uint32, v uint16) {
	a.write16(off, v, false, false)
}

// AdvanceMaster runs the subsystem for masterCycles of the 22.5792
// MHz domain. One sample is 512 master cycles. The ARM7 runs at half
// the master rate with the remainder carried between calls.
func (a *AICA) AdvanceMaster(masterCycles int) {
	if masterCycles <= 0 {
		return
	}
	a.rtcRem += masterCycles
	for a.rtcRem >= masterHz {
		a.rtcRem -= masterHz
		a.rtcSeconds += 1
	}
	a.masterRem += masterCycles
	samples := a.masterRem / sampleMasterCycles
	a.masterRem %= sampleMasterCycles
	for i := 0; i < samples; i += 1 {
		a.generateSample()
	}
	a.armRem += masterCycles
	armCycles := a.armRem / 2
	a.armRem %= 2
	if armCycles > 0 && !a.armHeld() && a.arm != nil {
		a.arm.SetFIQ(a.fiqAsserted)
		a.arm.Run(armCycles)
	}
}

// AudioBuffer drains the produced stereo samples.
func (a *AICA) AudioBuffer() [][2]int16 {
	out := a.out
	a.out = nil
	if out == nil {
		return [][2]int16{}
	}
	return out
}

func (a *AICA) generateSample() {
	for t := 0; t < 3; t += 1 {
		ctl := int((a.getRaw16(0x2890+uint32(t)*4) >> 8) & 7)
		tm := &a.timers[t]
		tm.divider += 1
		if tm.divider >= (1 << uint(ctl)) {
			tm.divider = 0
			prev := tm.counter
			tm.counter = prev + 1
			if prev == 0xFF {
				a.pending |= 1 << uint(6+t)
			}
		}
	}
	a.pending |= 1 << 10

	mono := a.getRaw16(0x2800)&(1<<15) != 0
	var masterL, masterR float64
	for i := 0; i < 64; i += 1 {
		l, r, send := a.slotSample(i, mono)
		masterL += l
		masterR += r
		if send != 0 {
			isel := int(a.slotReg(i, 0x20) & 0xF)
			a.mixs[isel] = clampInt32(a.mixs[isel]+send, -524288, 524287)
		}
	}

	a.runDSP()

	for i := 0; i < 16; i += 1 {
		out := int16(a.efreg[i])
		if out == 0 {
			continue
		}
		outs := a.getRaw16(0x2000 + uint32(i)*4)
		g := sendGain((outs >> 8) & 0xF)
		if g == 0 {
			continue
		}
		pan := outs & 0x1F
		gl, gr := g, g
		if !mono {
			gl = g * panGain(pan, 0)
			gr = g * panGain(pan, 1)
		}
		masterL += float64(out) * gl
		masterR += float64(out) * gr
	}

	mvol := sendGain(a.getRaw16(0x2800) & 0xF)
	sample := [2]int16{
		clampSample(masterL * mvol),
		clampSample(masterR * mvol),
	}
	if len(a.out) >= outCap {
		copy(a.out, a.out[1:])
		a.out = a.out[:len(a.out)-1]
	}
	a.out = append(a.out, sample)
	a.recomputeInterrupts()
}

func clampSample(f float64) int16 {
	if f > 32767 {
		return 32767
	}
	if f < -32768 {
		return -32768
	}
	if f >= 0 {
		return int16(f + 0.5)
	}
	return int16(f - 0.5)
}

// armBus adapts the AICA to the ARM7 bus interface: wave RAM at 0,
// registers at 0x00800000, everything else open bus.
type armBus struct{ a *AICA }

func (b *armBus) inRAM(addr uint32) bool { return addr < armRAMSize }

func (b *armBus) regOff(addr uint32) (uint32, bool) {
	if addr >= armRegBase && addr < armRegBase+regWindowSize {
		return addr - armRegBase, true
	}
	return 0, false
}

func (b *armBus) Read8(addr uint32) uint8 {
	if b.inRAM(addr) {
		if len(b.a.ram) == 0 {
			return 0
		}
		return b.a.ram[b.a.ramIndex(addr)]
	}
	if off, ok := b.regOff(addr); ok {
		v := b.a.read32(off&^uint32(3), true)
		return byte(v >> uint((addr&3)*8))
	}
	return 0
}

func (b *armBus) Read16(addr uint32) uint16 {
	if b.inRAM(addr) {
		return uint16(b.Read8(addr)) | uint16(b.Read8(addr+1))<<8
	}
	if off, ok := b.regOff(addr); ok {
		return uint16(b.a.read32(off&^uint32(3), true) >> uint((addr&2)*8))
	}
	return 0
}

func (b *armBus) Read32(addr uint32) uint32 {
	if b.inRAM(addr) {
		return b.a.ramRead32(addr)
	}
	if off, ok := b.regOff(addr); ok {
		return b.a.read32(off, true)
	}
	return 0
}

func (b *armBus) Write8(addr uint32, v uint8) {
	if b.inRAM(addr) {
		if len(b.a.ram) != 0 {
			b.a.ram[b.a.ramIndex(addr)] = v
		}
		return
	}
	if off, ok := b.regOff(addr); ok {
		cur := b.a.read32(off&^uint32(3), true)
		shift := uint((addr & 3) * 8)
		cur &^= 0xFF << shift
		cur |= uint32(v) << shift
		b.a.write32(off&^uint32(3), cur, true)
	}
}

func (b *armBus) Write16(addr uint32, v uint16) {
	if b.inRAM(addr) {
		b.Write8(addr, uint8(v))
		b.Write8(addr+1, uint8(v>>8))
		return
	}
	if off, ok := b.regOff(addr); ok {
		b.a.write16(off&^uint32(2), v, true, true)
	}
}

func (b *armBus) Write32(addr uint32, v uint32) {
	if b.inRAM(addr) {
		b.a.ramWrite32(addr, v)
		return
	}
	if off, ok := b.regOff(addr); ok {
		b.a.write32(off, v, true)
	}
}
