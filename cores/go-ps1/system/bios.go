package system

// BIOS HLE for PS1-3. There is no Sony BIOS in this
// core and there never will be; instead the machine
// intercepts execution at the three documented call
// vectors (0xA0, 0xB0, 0xC0, function number in $t1,
// arguments in $a0 to $a3, return value in $v0) and at
// the syscall instruction for the critical-section
// functions, and services a tight, documented subset
// in Go. Everything outside the subset is recorded
// (see UnimplementedBIOSCalls) and returns to the
// caller with $v0 = 0xFFFFFFFF, the documented error
// value shape; it never crashes the machine.
//
// Function numbers follow the psx-spx BIOS function
// summary exactly. That corrects three numbers the
// PS1-3 brief carried from secondary sources:
//
//   - TestEvent is B(0Bh), EnableEvent is B(0Ch) and
//     DisableEvent is B(0Dh) (not C0 functions).
//   - EnterCriticalSection and ExitCriticalSection
//     are SYS(01h) and SYS(02h), reached through the
//     syscall instruction with the function number in
//     $a0 (not C(08h)/C(09h), which are SysInitMemory
//     and SysInitKernelVariables).
//   - The root-counter BIOS API is B(02h) init_timer,
//     B(03h) get_timer, B(04h) enable_timer_irq,
//     B(05h) disable_timer_irq, B(06h) restart_timer
//     and C(0Ah) ChangeClearRCnt. The brief also
//     required the SDK-level names SetRCnt, GetRCnt
//     and ResetRCnt; those are implemented at the
//     C(19h)/C(1Ah)/C(1Bh) slots the brief named,
//     with the mode word applied in the hardware
//     timer mode-register layout, because psx-spx
//     does not define a different encoding for them.
//
// The implemented set:
//
//	A(44h) FlushCache            no-op (no cache model)
//	A(48h) SendGP1Command        write GP1, $v0 kept
//	A(49h) GPU_cw                write GP0, $v0 = 0
//	A(4Ah) GPU_cwp               copy words to GP0
//	A(4Dh) GetGPUStatus          $v0 = GPUSTAT
//	B(02h) init_timer            per the doc formula
//	B(03h) get_timer             counter readback
//	B(04h) enable_timer_irq      I_MASK bits per doc
//	B(05h) disable_timer_irq     I_MASK bits per doc
//	B(06h) restart_timer         counter = 0
//	B(07h) DeliverEvent          class/spec delivery
//	B(08h) OpenEvent             handle 0xF1000000+n
//	B(09h) CloseEvent            always returns 1
//	B(0Ah) WaitEvent             non-blocking, see below
//	B(0Bh) TestEvent             1 when ready, consumes
//	B(0Ch) EnableEvent           always returns 1
//	B(0Dh) DisableEvent          always returns 1
//	B(12h) InitPAD2              buffers recorded
//	B(13h) StartPAD2             pad fill on VBlank on
//	B(14h) StopPAD2              pad fill off
//	B(20h) UnDeliverEvent        ready back to busy
//	C(04h) get_free_EvCB_slot    first free event slot
//	C(08h) SysInitMemory         no-op in the HLE
//	C(09h) SysInitKernelVariables no-op in the HLE
//	C(0Ah) ChangeClearRCnt       flag store, old value
//	C(19h) SetRCnt               SDK alias, see above
//	C(1Ah) GetRCnt               SDK alias, see above
//	C(1Bh) ResetRCnt             SDK alias, see above
//	SYS(01h) EnterCriticalSection
//	SYS(02h) ExitCriticalSection
//
// Event model: the event table holds 16 slots (the
// documented default EvCB count). An event opens
// disabled, exactly as documented. Delivery happens
// when the matching hardware interrupt latches in
// I_STAT (the kernel's default IRQ handler events:
// class 0xF0000001+irq, spec 0x1000, plus the root
// counter classes 0xF2000000+n with spec 0x0002), or
// when guest code calls DeliverEvent. Delivery
// follows the I_STAT latch, not the I_MASK bit: on
// hardware a masked interrupt never reaches the
// kernel handler, so this is a stated simplification.
// Mode 0x2000 events are marked ready for
// TestEvent/WaitEvent. Mode 0x1000 callback events
// are queued and invoked at the next Step boundary
// through a synthetic call frame (registers saved,
// $ra set to a return trampoline in the kernel
// reserved area at 0x00000E10, interrupts masked in
// SR during the callback); when the callback returns
// to the trampoline the saved context is restored.
// WaitEvent cannot hang in an HLE that advances time
// only while the CPU executes, so it returns the
// current readiness (1 and consumes when ready, 0
// otherwise) instead of blocking; that is the one
// deliberate semantic deviation in this file.
//
// Critical sections: EnterCriticalSection saves the
// SR interrupt-enable bit on first entry and clears
// SR.IEc (the documented effect is masking the
// kernel's interrupt sources; clearing IEc is the
// CPU-visible equivalent here); a nesting counter is
// kept. ExitCriticalSection restores IEc when the
// counter returns to zero.

// BIOSCall records one call the HLE did not
// implement: the table (A, B, C or S for syscall),
// the function number from $t1 (or $a0 for syscalls),
// and the caller PC (the return address).
type BIOSCall struct {
	Table byte
	Func  uint32
	PC    uint32
}

// Event classes and specs used by the delivery
// model (psx-spx BIOS Event Summary).
const (
	evClassVBlank  uint32 = 0xF0000001
	evClassGPU     uint32 = 0xF0000002
	evClassCDROM   uint32 = 0xF0000003
	evClassDMA     uint32 = 0xF0000004
	evClassTMR0    uint32 = 0xF0000005
	evClassTMR1    uint32 = 0xF0000006 // TMR2 shares this class (documented quirk)
	evClassPAD     uint32 = 0xF0000008
	evClassSPU     uint32 = 0xF0000009
	evSpecIRQ      uint32 = 0x1000
	evSpecRCnt     uint32 = 0x0002
	evClassRCnt0   uint32 = 0xF2000000
	evClassRCntVB  uint32 = 0xF2000003
	evModeCallback uint32 = 0x1000
	evModeReady    uint32 = 0x2000
	evHandleBase   uint32 = 0xF1000000
)

// Event slot states.
const (
	evUnused = iota
	evDisabled
	evBusy  // enabled, not delivered
	evReady // enabled, delivered (mode 0x2000)
)

const (
	evSlots     = 16
	cbQueueSize = 16
	cbTrampAddr = 0x00000E10 // kernel reserved area, return trampoline
	biosCycles  = 16         // flat charge per vector call
)

// biosEvent is one EvCB slot.
type biosEvent struct {
	class, spec uint32
	mode        uint32
	fn          uint32
	state       uint8
}

// biosState is the whole HLE BIOS runtime state. It
// is serialized as part of the machine state.
type biosState struct {
	events [evSlots]biosEvent

	critCount int
	critIEc   bool // SR.IEc saved by the outermost EnterCriticalSection

	clearRCnt [4]uint32 // ChangeClearRCnt flags (t = 0..2, 3 = vblank)

	padBuf1, padBuf2 uint32
	padStarted       bool

	// Callback invocation machinery.
	cbActive bool
	cbSaved  cbContext
	cbQueue  [cbQueueSize]uint32
	cbHead   int // index of the oldest queued callback
	cbCount  int

	unimpl     []BIOSCall
	unimplSeen int // total unimplemented calls, including dropped ones
}

// cbContext is the CPU context saved while a
// mode 0x1000 event callback runs.
type cbContext struct {
	regs   [32]uint32
	hi, lo uint32
	pc     uint32
	nextPC uint32
	sr     uint32
}

func (b *biosState) reset() {
	*b = biosState{}
}

// UnimplementedBIOSCalls returns a copy of the
// recorded unimplemented-call log (bounded; the
// total count is UnimplementedBIOSCallCount).
func (m *Machine) UnimplementedBIOSCalls() []BIOSCall {
	out := make([]BIOSCall, len(m.bios.unimpl))
	copy(out, m.bios.unimpl)
	return out
}

// UnimplementedBIOSCallCount returns how many
// unimplemented BIOS calls have been intercepted in
// total (the log itself is capped).
func (m *Machine) UnimplementedBIOSCallCount() int { return m.bios.unimplSeen }

func (m *Machine) logUnimplemented(table byte, fn, pc uint32) {
	m.bios.unimplSeen++
	if len(m.bios.unimpl) < 256 {
		m.bios.unimpl = append(m.bios.unimpl, BIOSCall{Table: table, Func: fn, PC: pc})
	}
}

// deliverEvent marks or invokes every enabled event
// matching class and spec.
func (m *Machine) deliverEvent(class, spec uint32) {
	for i := range m.bios.events {
		ev := &m.bios.events[i]
		if ev.state != evBusy && ev.state != evReady {
			continue
		}
		if ev.class != class || ev.spec != spec {
			continue
		}
		switch ev.mode {
		case evModeReady:
			ev.state = evReady
		case evModeCallback:
			if ev.fn != 0 {
				m.enqueueCallback(ev.fn)
			}
		}
	}
}

func (m *Machine) enqueueCallback(fn uint32) {
	if m.bios.cbCount >= cbQueueSize {
		return // drop: stated bound, the queue is drained every Step
	}
	tail := (m.bios.cbHead + m.bios.cbCount) % cbQueueSize
	m.bios.cbQueue[tail] = fn
	m.bios.cbCount++
}

// interruptPending mirrors the CPU's own check (SR
// interrupt enable and any unmasked Cause IP bit) so
// the Step interception never preempts an interrupt
// the CPU is about to take.
func (m *Machine) interruptPending() bool {
	return m.CPU.SR&1 != 0 && m.CPU.SR&m.CPU.Cause&0xFF00 != 0
}

// stepBIOS checks the interception points before a
// normal CPU step. It returns (cycles, true) when it
// handled the step itself.
func (m *Machine) stepBIOS() (int, bool) {
	pc := m.CPU.PC
	// Callback return trampoline.
	if pc == cbTrampAddr && m.bios.cbActive {
		s := m.bios.cbSaved
		m.CPU.Regs = s.regs
		m.CPU.HI, m.CPU.LO = s.hi, s.lo
		m.CPU.PC, m.CPU.NextPC = s.pc, s.nextPC
		m.CPU.SR = s.sr
		m.bios.cbActive = false
		m.advance(4)
		return 4, true
	}
	if m.interruptPending() {
		return 0, false
	}
	// Vector calls. The vectors live in low RAM, so
	// the KUSEG address and its KSEG0/KSEG1 mirrors
	// are the same physical slot; match on the
	// physical address.
	if phys := pc & 0x1FFFFFFF; phys == 0xA0 || phys == 0xB0 || phys == 0xC0 {
		m.CPU.CommitPendingLoad()
		m.execBIOSCall(phys)
		ret := m.CPU.Regs[31]
		m.CPU.PC = ret
		m.CPU.NextPC = ret + 4
		m.advance(biosCycles)
		return biosCycles, true
	}
	// Syscall interception for the critical-section
	// functions (other syscalls take the normal
	// exception path, as they did before the HLE).
	if w := m.Bus.Read32(pc); w&0xFC00003F == 0x0000000C {
		fn := m.CPU.Regs[4]
		if fn == 1 || fn == 2 {
			m.CPU.CommitPendingLoad()
			if fn == 1 {
				m.enterCritical()
			} else {
				m.exitCritical()
			}
			m.CPU.PC = m.CPU.NextPC
			m.CPU.NextPC += 4
			m.advance(4)
			return 4, true
		}
		m.logUnimplemented('S', fn, pc)
		return 0, false
	}
	// Start a queued event callback.
	if !m.bios.cbActive && m.bios.cbCount > 0 {
		fn := m.bios.cbQueue[m.bios.cbHead]
		m.bios.cbHead = (m.bios.cbHead + 1) % cbQueueSize
		m.bios.cbCount--
		s := &m.bios.cbSaved
		s.regs = m.CPU.Regs
		s.hi, s.lo = m.CPU.HI, m.CPU.LO
		s.pc, s.nextPC = m.CPU.PC, m.CPU.NextPC
		s.sr = m.CPU.SR
		m.bios.cbActive = true
		m.CPU.Regs[31] = cbTrampAddr
		m.CPU.SR &^= 1 // callbacks run with interrupts masked
		m.CPU.PC = fn
		m.CPU.NextPC = fn + 4
		c := m.CPU.Step()
		m.advance(c)
		return c, true
	}
	return 0, false
}

func (m *Machine) enterCritical() {
	if m.bios.critCount == 0 {
		m.bios.critIEc = m.CPU.SR&1 != 0
		m.CPU.SR &^= 1
	}
	m.bios.critCount++
}

func (m *Machine) exitCritical() {
	if m.bios.critCount > 0 {
		m.bios.critCount--
		if m.bios.critCount == 0 && m.bios.critIEc {
			m.CPU.SR |= 1
		}
	}
}

// execBIOSCall services one vector call. On entry the
// PC is the vector; $t1 holds the function number,
// $a0 to $a3 the arguments, $ra the return address.
func (m *Machine) execBIOSCall(vector uint32) {
	fn := m.CPU.Regs[9]
	a0, a1, a2, a3 := m.CPU.Regs[4], m.CPU.Regs[5], m.CPU.Regs[6], m.CPU.Regs[7]
	ret := m.CPU.Regs[31]
	setV := func(v uint32) { m.CPU.Regs[2] = v }

	switch vector {
	case 0xA0:
		switch fn {
		case 0x44: // FlushCache: no cache model, nothing to flush
		case 0x48: // SendGP1Command
			m.GPU.WriteGP1(a0)
		case 0x49: // GPU_cw
			m.writeGP0(a0)
			setV(0)
		case 0x4A: // GPU_cwp
			for i := uint32(0); i < a1; i++ {
				m.writeGP0(m.Bus.Read32(a0 + i*4))
			}
			setV(0)
		case 0x4D: // GetGPUStatus
			setV(m.GPU.Status())
		default:
			m.logUnimplemented('A', fn, ret)
			setV(0xFFFFFFFF)
		}
	case 0xB0:
		switch fn {
		case 0x02: // init_timer(t, reload, flags)
			setV(m.biosInitTimer(a0, a1, a2))
		case 0x03: // get_timer(t)
			if a0 < 3 {
				setV(uint32(m.Timers.T[a0].Counter))
			} else {
				setV(0)
			}
		case 0x04: // enable_timer_irq(t)
			switch {
			case a0 < 3:
				m.imask |= 1 << uint(IRQTMR0+int(a0))
				m.updateIRQ()
				setV(1)
			case a0 == 3:
				m.imask |= 1 << IRQVBlank
				m.updateIRQ()
				setV(0) // documented: enable returns 0 for t=3
			default:
				setV(0)
			}
		case 0x05: // disable_timer_irq(t)
			switch {
			case a0 < 3:
				m.imask &^= 1 << uint(IRQTMR0+int(a0))
			case a0 == 3:
				m.imask &^= 1 << IRQVBlank
			}
			m.updateIRQ()
			setV(1) // documented: disable always returns 1
		case 0x06: // restart_timer(t)
			if a0 < 3 {
				m.Timers.WriteCounter(int(a0), 0)
				setV(1)
			} else {
				setV(0)
			}
		case 0x07: // DeliverEvent(class, spec)
			m.deliverEvent(a0, a1)
		case 0x08: // OpenEvent(class, spec, mode, func)
			setV(m.biosOpenEvent(a0, a1, a2, a3))
		case 0x09: // CloseEvent(event)
			if ev := m.biosEventByHandle(a0); ev != nil {
				*ev = biosEvent{}
			}
			setV(1)
		case 0x0A: // WaitEvent(event): non-blocking, see file comment
			setV(m.biosTestEvent(a0, true))
		case 0x0B: // TestEvent(event)
			setV(m.biosTestEvent(a0, false))
		case 0x0C: // EnableEvent(event)
			if ev := m.biosEventByHandle(a0); ev != nil && ev.state == evDisabled {
				ev.state = evBusy
			}
			setV(1)
		case 0x0D: // DisableEvent(event)
			if ev := m.biosEventByHandle(a0); ev != nil && ev.state != evUnused {
				ev.state = evDisabled
			}
			setV(1)
		case 0x12: // InitPAD2(buf1, siz1, buf2, siz2)
			m.bios.padBuf1, m.bios.padBuf2 = a0, a2
			m.bios.padStarted = false
			zero := func(addr, size uint32) {
				if size > 0x100 {
					size = 0x100
				}
				for i := uint32(0); i < size; i++ {
					m.Bus.Write8(addr+i, 0)
				}
			}
			zero(a0, a1)
			zero(a2, a3)
		case 0x13: // StartPAD2
			m.bios.padStarted = true
		case 0x14: // StopPAD2
			m.bios.padStarted = false
		case 0x20: // UnDeliverEvent(class, spec)
			for i := range m.bios.events {
				ev := &m.bios.events[i]
				if ev.state == evReady && ev.mode == evModeReady && ev.class == a0 && ev.spec == a1 {
					ev.state = evBusy
				}
			}
		default:
			m.logUnimplemented('B', fn, ret)
			setV(0xFFFFFFFF)
		}
	case 0xC0:
		switch fn {
		case 0x04: // get_free_EvCB_slot
			setV(0xFFFFFFFF)
			for i := range m.bios.events {
				if m.bios.events[i].state == evUnused {
					setV(uint32(i))
					break
				}
			}
		case 0x08: // SysInitMemory: the HLE kernel memory needs no setup
		case 0x09: // SysInitKernelVariables: same
		case 0x0A: // ChangeClearRCnt(t, flag)
			if a0 < 4 {
				old := m.bios.clearRCnt[a0]
				m.bios.clearRCnt[a0] = a1
				setV(old)
			} else {
				setV(0)
			}
		case 0x19: // SetRCnt(spec, target, mode): spec low bits = counter
			if a0&3 < 3 {
				n := int(a0 & 3)
				m.Timers.WriteTarget(n, uint16(a1))
				m.Timers.WriteMode(n, uint16(a2))
				setV(1)
			} else {
				setV(0)
			}
		case 0x1A: // GetRCnt(spec)
			if a0&3 < 3 {
				setV(uint32(m.Timers.T[a0&3].Counter))
			} else {
				setV(0)
			}
		case 0x1B: // ResetRCnt(spec)
			if a0&3 < 3 {
				n := int(a0 & 3)
				m.Timers.WriteCounter(n, 0)
				m.istat &^= 1 << uint(IRQTMR0+n)
				m.updateIRQ()
				setV(1)
			} else {
				setV(0)
			}
		default:
			m.logUnimplemented('C', fn, ret)
			setV(0xFFFFFFFF)
		}
	}
}

// biosInitTimer implements B(02h) with the exact
// mode computation psx-spx documents.
func (m *Machine) biosInitTimer(t, reload, flags uint32) uint32 {
	if t > 2 {
		return 0
	}
	n := int(t)
	m.Timers.WriteMode(n, 0)
	m.Timers.WriteTarget(n, uint16(reload))
	var mode uint32
	if flags&(1<<4) == 0 {
		mode = 0x48
	} else {
		mode = 0x49
	}
	if flags&1 == 0 {
		mode |= 0x100
	}
	if flags&(1<<12) != 0 {
		mode |= 0x10
	}
	m.Timers.WriteMode(n, uint16(mode))
	return 1
}

func (m *Machine) biosOpenEvent(class, spec, mode, fn uint32) uint32 {
	for i := range m.bios.events {
		if m.bios.events[i].state == evUnused {
			m.bios.events[i] = biosEvent{class: class, spec: spec, mode: mode, fn: fn, state: evDisabled}
			return evHandleBase + uint32(i)
		}
	}
	return 0xFFFFFFFF
}

func (m *Machine) biosEventByHandle(h uint32) *biosEvent {
	if h&0xFFFF0000 != evHandleBase {
		return nil
	}
	i := h & 0xFFFF
	if i >= evSlots || m.bios.events[i].state == evUnused {
		return nil
	}
	return &m.bios.events[i]
}

// biosTestEvent backs TestEvent and WaitEvent.
func (m *Machine) biosTestEvent(h uint32, wait bool) uint32 {
	_ = wait // both are non-blocking in this HLE; see the file comment
	ev := m.biosEventByHandle(h)
	if ev == nil {
		return 0
	}
	if ev.state == evReady {
		ev.state = evBusy
		return 1
	}
	return 0
}

// fillPadBuffers writes the InitPAD2 buffers on a
// VBlank when StartPAD2 has run: port 1 gets the
// wired pad (status okay, digital ID, the two
// active-low button bytes from the pad package via
// the machine button state), port 2 reports the
// documented no-device timeout status.
func (m *Machine) fillPadBuffers() {
	if !m.bios.padStarted {
		return
	}
	if m.bios.padBuf1 != 0 {
		b := m.buttons
		m.Bus.Write8(m.bios.padBuf1+0, 0x00)
		m.Bus.Write8(m.bios.padBuf1+1, 0x41)
		m.Bus.Write8(m.bios.padBuf1+2, byte(^(b >> 0)))
		m.Bus.Write8(m.bios.padBuf1+3, byte(^(b >> 8)))
	}
	if m.bios.padBuf2 != 0 {
		m.Bus.Write8(m.bios.padBuf2+0, 0xFF)
		m.Bus.Write8(m.bios.padBuf2+1, 0x00)
	}
}

// biosStateSize is the serialized BIOS block size:
// events, callback context and queue, and the scalar
// fields, in the fixed layout SerializeBIOS uses.
const biosStateSize = evSlots*20 + // class, spec, mode, fn, state+pad
	4 + 4 + // critCount, critIEc pad
	4*4 + // clearRCnt
	4 + 4 + 4 + // padBuf1, padBuf2, padStarted pad
	4 + // cbActive pad
	32*4 + 5*4 + // cbSaved regs + hi/lo/pc/nextPC/sr
	4 + 4 + // cbHead, cbCount
	cbQueueSize*4 +
	4 + 4 // unimplSeen, unimpl log length (entries are not serialized)

func (m *Machine) serializeBIOS() []byte {
	out := make([]byte, biosStateSize)
	off := 0
	put := func(v uint32) { put32(out[off:], v); off += 4 }
	for i := range m.bios.events {
		ev := &m.bios.events[i]
		put(ev.class)
		put(ev.spec)
		put(ev.mode)
		put(ev.fn)
		put(uint32(ev.state))
	}
	put(uint32(int32(m.bios.critCount)))
	put(bool32(m.bios.critIEc))
	for _, v := range m.bios.clearRCnt {
		put(v)
	}
	put(m.bios.padBuf1)
	put(m.bios.padBuf2)
	put(bool32(m.bios.padStarted))
	put(bool32(m.bios.cbActive))
	for _, r := range m.bios.cbSaved.regs {
		put(r)
	}
	put(m.bios.cbSaved.hi)
	put(m.bios.cbSaved.lo)
	put(m.bios.cbSaved.pc)
	put(m.bios.cbSaved.nextPC)
	put(m.bios.cbSaved.sr)
	put(uint32(m.bios.cbHead))
	put(uint32(m.bios.cbCount))
	for _, fn := range m.bios.cbQueue {
		put(fn)
	}
	put(uint32(m.bios.unimplSeen))
	put(uint32(len(m.bios.unimpl)))
	return out
}

func (m *Machine) deserializeBIOS(in []byte) {
	off := 0
	get := func() uint32 { v := get32(in[off:]); off += 4; return v }
	for i := range m.bios.events {
		ev := &m.bios.events[i]
		ev.class = get()
		ev.spec = get()
		ev.mode = get()
		ev.fn = get()
		ev.state = uint8(get())
	}
	m.bios.critCount = int(int32(get()))
	m.bios.critIEc = get() != 0
	for i := range m.bios.clearRCnt {
		m.bios.clearRCnt[i] = get()
	}
	m.bios.padBuf1 = get()
	m.bios.padBuf2 = get()
	m.bios.padStarted = get() != 0
	m.bios.cbActive = get() != 0
	for i := range m.bios.cbSaved.regs {
		m.bios.cbSaved.regs[i] = get()
	}
	m.bios.cbSaved.hi = get()
	m.bios.cbSaved.lo = get()
	m.bios.cbSaved.pc = get()
	m.bios.cbSaved.nextPC = get()
	m.bios.cbSaved.sr = get()
	m.bios.cbHead = int(get())
	m.bios.cbCount = int(get())
	for i := range m.bios.cbQueue {
		m.bios.cbQueue[i] = get()
	}
	m.bios.unimplSeen = int(get())
	_ = get() // log length: the log itself is diagnostic only, not restored
	m.bios.unimpl = nil
}

func bool32(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}
