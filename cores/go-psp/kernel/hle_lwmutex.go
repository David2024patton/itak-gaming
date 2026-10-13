package kernel

// Lightweight mutexes (sceKernel* LwMutex). Runtime libraries use
// these for their internal locks (the pspsdk libc locks its stdio,
// malloc, and environment state with them), so real programs call
// them constantly. The lock state lives in the guest workarea; see
// sync.go for the layout. Calls reach here under both the
// Kernel_Library and ThreadManForUser import names: pspsdk builds
// import them from ThreadManForUser, other documentation places
// them in Kernel_Library, so both are registered.

func (k *Kernel) lwMutexByWorkarea(workarea uint32) (*lwMutexObj, bool) {
	if workarea == 0 {
		return nil, false
	}
	uid := k.mem.Read32(workarea + lwOffUID)
	if uid == lwNoSuchUID {
		return nil, false
	}
	o, ok := k.lookup(uid)
	if !ok {
		return nil, false
	}
	l, ok := o.(*lwMutexObj)
	return l, ok
}

func hleCreateLwMutex(k *Kernel, th *Thread) syscallAction {
	workarea := k.arg(th, 0)
	name := k.readCString(k.arg(th, 1), 32)
	attr := k.arg(th, 2)
	initCount := int32(k.arg(th, 3))
	if workarea == 0 {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	if initCount < 0 {
		k.ret(th, ErrIllegalCount)
		return actNone
	}
	l := &lwMutexObj{
		uid:       k.NewUID(kindLwMutex),
		name:      name,
		attr:      attr,
		initCount: initCount,
		workarea:  workarea,
	}
	k.registerObject(l.uid, l)
	// Initialize the workarea in place: level, owner (the creator
	// when starting locked), attr, waiter count, uid, zeroed pad.
	var owner uint32
	if initCount > 0 {
		owner = th.uid
	}
	k.lwSetState(l, initCount, owner)
	k.write32(workarea+lwOffAttr, attr)
	k.write32(workarea+lwOffWaits, 0)
	k.write32(workarea+lwOffUID, l.uid)
	k.write32(workarea+20, 0)
	k.write32(workarea+24, 0)
	k.write32(workarea+28, 0)
	k.ret(th, l.uid)
	return actNone
}

func hleDeleteLwMutex(k *Kernel, th *Thread) syscallAction {
	workarea := k.arg(th, 0)
	l, ok := k.lwMutexByWorkarea(workarea)
	if !ok {
		k.ret(th, ErrLwMutexNotFound)
		return actNone
	}
	for _, w := range l.waiters {
		if w.state == thWait && w.wait == waitLwMutex {
			k.completeWait(w, errResult(ErrWaitDelete))
		}
	}
	l.waiters = nil
	k.removeObject(l.uid)
	// The workarea is cleared: level 0, owner and uid marked gone.
	k.lwSetState(l, 0, lwNoSuchUID)
	k.write32(workarea+lwOffUID, lwNoSuchUID)
	k.ret(th, 0)
	return actNone
}

// lwLockCheck validates a lock request against the workarea state
// and returns a kernel error code, or ErrOK when the caller may
// acquire immediately or block.
func (k *Kernel) lwLockCheck(l *lwMutexObj, th *Thread, count int32) uint32 {
	if count <= 0 {
		return ErrIllegalCount
	}
	recursive := l.attr&lwMutexRecursiveMask != 0
	if count > 1 && !recursive {
		return ErrIllegalCount
	}
	level := k.lwLevel(l)
	if level+count < level {
		return ErrLwMutexLockOverflow
	}
	if level != 0 && k.lwOwner(l) == th.uid && !recursive {
		return ErrLwMutexRecursiveDeny
	}
	return ErrOK
}

func (k *Kernel) lwTryAcquire(l *lwMutexObj, th *Thread, count int32) bool {
	level := k.lwLevel(l)
	if level == 0 {
		k.lwSetState(l, count, th.uid)
		return true
	}
	if k.lwOwner(l) == th.uid && l.attr&lwMutexRecursiveMask != 0 {
		k.lwSetState(l, level+count, th.uid)
		return true
	}
	return false
}

func hleLockLwMutex(k *Kernel, th *Thread) syscallAction {
	workarea := k.arg(th, 0)
	l, ok := k.lwMutexByWorkarea(workarea)
	if !ok {
		k.ret(th, ErrLwMutexNotFound)
		return actNone
	}
	count := int32(k.arg(th, 1))
	if ec := k.lwLockCheck(l, th, count); ec != ErrOK {
		k.ret(th, ec)
		return actNone
	}
	if k.lwTryAcquire(l, th, count) {
		k.ret(th, 0)
		return actNone
	}
	deadline, _, poll := k.deadlineFromTimeout(th, k.arg(th, 2))
	if poll {
		k.ret(th, ErrWaitTimeout)
		return actNone
	}
	th.waitCount = count
	l.waiters = append(l.waiters, th)
	k.lwSetWaitCount(l)
	return k.blockCurrent(th, waitLwMutex, l.uid, deadline)
}

func hleTryLockLwMutex(k *Kernel, th *Thread) syscallAction {
	workarea := k.arg(th, 0)
	l, ok := k.lwMutexByWorkarea(workarea)
	if !ok {
		k.ret(th, ErrLwMutexNotFound)
		return actNone
	}
	count := int32(k.arg(th, 1))
	if ec := k.lwLockCheck(l, th, count); ec != ErrOK {
		k.ret(th, ec)
		return actNone
	}
	if k.lwTryAcquire(l, th, count) {
		k.ret(th, 0)
		return actNone
	}
	// A failed try-lock reports the plain mutex try-lock error.
	k.ret(th, ErrMutexLocked)
	return actNone
}

func hleUnlockLwMutex(k *Kernel, th *Thread) syscallAction {
	workarea := k.arg(th, 0)
	l, ok := k.lwMutexByWorkarea(workarea)
	if !ok {
		k.ret(th, ErrLwMutexNotFound)
		return actNone
	}
	count := int32(k.arg(th, 1))
	if count <= 0 {
		k.ret(th, ErrIllegalCount)
		return actNone
	}
	if count > 1 && l.attr&lwMutexRecursiveMask == 0 {
		k.ret(th, ErrIllegalCount)
		return actNone
	}
	level := k.lwLevel(l)
	if level == 0 || k.lwOwner(l) != th.uid {
		k.ret(th, ErrLwMutexUnlocked)
		return actNone
	}
	if level < count {
		k.ret(th, ErrLwMutexUnlockUnder)
		return actNone
	}
	level -= count
	if level == 0 {
		k.lwSetState(l, 0, 0)
		k.wakeLwMutexWaiter(l)
	} else {
		k.lwSetState(l, level, th.uid)
	}
	k.ret(th, 0)
	return actNone
}
