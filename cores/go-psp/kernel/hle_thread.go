package kernel

// ThreadManForUser handlers. Argument order follows the pspsdk
// pspthreadman.h prototypes; arguments arrive in $a0..$a3 then
// $t0.. as the Allegrex calling convention packs them.

func hleCreateThread(k *Kernel, th *Thread) syscallAction {
	name := k.readCString(k.arg(th, 0), 32)
	entry := k.arg(th, 1)
	priority := int32(k.arg(th, 2))
	stackSize := k.arg(th, 3)
	attr := k.stackArg(th, 4) // the fifth argument rides the stack in o32
	nt, err := k.createThread(name, entry, priority, stackSize, attr)
	if err == nil {
		// A new thread inherits the creator's $gp (the module's
		// small data base). Runtime startup code relies on this:
		// the crt0 spawns user_main and its stdio setup does
		// $gp-relative loads before anything sets $gp itself.
		ctxSetReg(nt.ctx, 28, k.cpu.Reg(28))
	}
	if err != nil {
		if ec, ok := err.(errCode); ok {
			k.ret(th, uint32(ec))
		} else {
			k.ret(th, ErrError)
		}
		return actNone
	}
	k.ret(th, nt.uid)
	return actNone
}

func hleStartThread(k *Kernel, th *Thread) syscallAction {
	uid := k.arg(th, 0)
	target, ok := k.threadByUID(uid)
	if !ok {
		k.ret(th, ErrUnknownThid)
		return actNone
	}
	if target.state != thDormant {
		k.ret(th, ErrNotDormant)
		return actNone
	}
	argsSize := k.arg(th, 1)
	argPtr := k.arg(th, 2)
	sp := ctxReg(target.ctx, 29)
	a1 := uint32(0)
	if argsSize > 0 && argPtr != 0 {
		aligned := (argsSize + 3) &^ 3
		sp -= aligned
		k.writeBytes(sp, k.readBytes(argPtr, int(argsSize)))
		a1 = sp
		ctxSetReg(target.ctx, 29, sp)
	}
	ctxSetReg(target.ctx, 4, argsSize)
	ctxSetReg(target.ctx, 5, a1)
	target.state = thReady
	target.seq = k.nextSeq()
	k.maybePreempt(target)
	k.ret(th, 0)
	return actNone
}

func hleExitThread(k *Kernel, th *Thread) syscallAction {
	k.exitThread(th, int32(k.arg(th, 0)), false)
	return actBlock // never actually resumes; the thread is dead
}

func hleExitDeleteThread(k *Kernel, th *Thread) syscallAction {
	k.exitThread(th, int32(k.arg(th, 0)), true)
	return actBlock
}

func hleDeleteThread(k *Kernel, th *Thread) syscallAction {
	uid := k.arg(th, 0)
	target, ok := k.threadByUID(uid)
	if !ok {
		k.ret(th, ErrUnknownThid)
		return actNone
	}
	if target == th {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	if target.state != thDormant && target.state != thDead {
		k.ret(th, ErrDormant)
		return actNone
	}
	k.deleteThreadObject(target)
	k.ret(th, 0)
	return actNone
}

func hleTerminateThread(k *Kernel, th *Thread) syscallAction {
	uid := k.arg(th, 0)
	target, ok := k.threadByUID(uid)
	if !ok {
		k.ret(th, ErrUnknownThid)
		return actNone
	}
	if target.state == thDead {
		k.ret(th, ErrThreadTerminated)
		return actNone
	}
	if target.state == thWait {
		k.detachWaiter(target)
	}
	wasSelf := target == th
	k.exitThread(target, -1, false)
	k.ret(th, 0)
	if wasSelf {
		return actBlock
	}
	return actNone
}

func hleTerminateDeleteThread(k *Kernel, th *Thread) syscallAction {
	uid := k.arg(th, 0)
	target, ok := k.threadByUID(uid)
	if !ok {
		k.ret(th, ErrUnknownThid)
		return actNone
	}
	if target.state == thDead {
		k.deleteThreadObject(target)
		k.ret(th, 0)
		return actNone
	}
	if target.state == thWait {
		k.detachWaiter(target)
	}
	wasSelf := target == th
	k.exitThread(target, -1, true)
	k.ret(th, 0)
	if wasSelf {
		return actBlock
	}
	return actNone
}

func hleWaitThreadEnd(k *Kernel, th *Thread) syscallAction {
	uid := k.arg(th, 0)
	target, ok := k.threadByUID(uid)
	if !ok {
		k.ret(th, ErrUnknownThid)
		return actNone
	}
	if target == th {
		k.ret(th, ErrWaitCancel) // a thread cannot wait on itself in this model
		return actNone
	}
	if target.state == thDead {
		k.ret(th, uint32(target.exitStatus))
		return actNone
	}
	deadline, _, poll := k.deadlineFromTimeout(th, k.arg(th, 1))
	if poll {
		k.ret(th, ErrWaitTimeout)
		return actNone
	}
	return k.blockCurrent(th, waitThreadEnd, uid, deadline)
}

func hleDelayThread(k *Kernel, th *Thread) syscallAction {
	us := k.arg(th, 0)
	if us == 0 {
		return actResched // a zero delay is a yield on the real kernel path
	}
	return k.blockCurrent(th, waitDelay, 0, k.cycles+uint64(us)*CyclesPerMicro)
}

func hleDelaySysClockThread(k *Kernel, th *Thread) syscallAction {
	ptr := k.arg(th, 0)
	if ptr == 0 {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	// SceKernelSysClock is a 64 bit microsecond count.
	us := uint64(k.mem.Read32(ptr)) | uint64(k.mem.Read32(ptr+4))<<32
	if us == 0 {
		return actResched
	}
	return k.blockCurrent(th, waitDelay, 0, k.cycles+us*CyclesPerMicro)
}

func hleSleepThread(k *Kernel, th *Thread) syscallAction {
	return k.blockCurrent(th, waitSleep, 0, 0)
}

func hleRotateReadyQueue(k *Kernel, th *Thread) syscallAction {
	k.ret(th, 0)
	return actResched
}

func hleGetThreadId(k *Kernel, th *Thread) syscallAction {
	k.ret(th, th.uid)
	return actNone
}

func hleChangeCurrentThreadAttr(k *Kernel, th *Thread) syscallAction {
	// Attributes are stored; none of them change behavior in this
	// model beyond what createThread recorded.
	th.attr = (th.attr &^ k.arg(th, 0)) | (k.arg(th, 1) & k.arg(th, 0))
	k.ret(th, 0)
	return actNone
}

func hleReferThreadStatus(k *Kernel, th *Thread) syscallAction {
	uid := k.arg(th, 0)
	infoPtr := k.arg(th, 1)
	target := th
	if uid != 0 {
		t, ok := k.threadByUID(uid)
		if !ok {
			k.ret(th, ErrUnknownThid)
			return actNone
		}
		target = t
	}
	if infoPtr == 0 {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	w := &guestWriter{k: k, addr: infoPtr}
	w.u32(104) // size
	w.str(target.name, 32)
	w.u32(target.attr)
	w.u32(threadStatusBits(target.state))
	w.u32(target.entry)
	w.u32(target.stackAddr)
	w.u32(target.stackSize)
	w.u32(ctxReg(target.ctx, 28)) // gpReg: the saved context's $gp
	w.i32(target.initPriority)
	w.i32(target.priority)
	w.i32(waitTypeCode(target.wait))
	w.u32(waitUIDForStatus(target))
	w.i32(0) // wakeupCount
	if target.state == thDead {
		w.i32(target.exitStatus)
	} else {
		w.i32(0)
	}
	w.u64(target.runCycles / CyclesPerMicro) // runClocks as a SysClock
	w.u32(0)                                 // intrPreemptCount
	w.u32(0)                                 // threadPreemptCount
	w.u32(0)                                 // releaseCount
	k.ret(th, 0)
	return actNone
}

func threadStatusBits(s threadState) uint32 {
	switch s {
	case thRun:
		return 0x01
	case thReady:
		return 0x02
	case thWait:
		return 0x04
	case thDormant:
		return 0x10
	case thDead:
		return 0x20
	}
	return 0
}

// waitTypeCode maps the internal wait kinds to the status codes
// this kernel reports (a stated model subset of the kernel's
// internal wait type numbering).
func waitTypeCode(w waitKind) int32 {
	switch w {
	case waitNone:
		return 0
	case waitSema:
		return 1
	case waitMutex:
		return 2
	case waitEventFlag:
		return 3
	case waitVblank:
		return 4
	case waitThreadEnd:
		return 5
	case waitDelay:
		return 6
	case waitSleep:
		return 7
	}
	return 0
}

func waitUIDForStatus(th *Thread) uint32 {
	if th.state == thWait {
		return th.waitUID
	}
	return 0
}

// guestWriter writes a structure into guest memory field by field.
type guestWriter struct {
	k    *Kernel
	addr uint32
}

func (w *guestWriter) u32(v uint32) {
	w.k.mem.Write32(w.addr, v)
	w.addr += 4
}

func (w *guestWriter) i32(v int32) { w.u32(uint32(v)) }

func (w *guestWriter) u64(v uint64) {
	w.k.mem.Write32(w.addr, uint32(v))
	w.k.mem.Write32(w.addr+4, uint32(v>>32))
	w.addr += 8
}

func (w *guestWriter) str(s string, size int) {
	for i := 0; i < size; i++ {
		b := byte(0)
		if i < len(s) {
			b = s[i]
		}
		w.k.mem.Write8(w.addr+uint32(i), b)
	}
	w.addr += uint32(size)
}

// Kernel callbacks (ThreadManForUser). They are registered and
// deleted like the real objects, but this slice has no event
// source that dispatches them (documented in the README).

func hleCreateCallback(k *Kernel, th *Thread) syscallAction {
	name := k.readCString(k.arg(th, 0), 32)
	cb := &callbackObj{
		uid:    k.NewUID(kindCallback),
		name:   name,
		fn:     k.arg(th, 1),
		arg:    k.arg(th, 2),
		thread: th,
	}
	k.registerObject(cb.uid, cb)
	k.ret(th, cb.uid)
	return actNone
}

func hleDeleteCallback(k *Kernel, th *Thread) syscallAction {
	uid := k.arg(th, 0)
	o, ok := k.lookup(uid)
	if !ok {
		k.ret(th, ErrUnknownUID)
		return actNone
	}
	if _, isCB := o.(*callbackObj); !isCB {
		k.ret(th, ErrUnknownCBID)
		return actNone
	}
	k.removeObject(uid)
	k.ret(th, 0)
	return actNone
}
