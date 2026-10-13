package kernel

// Semaphore, mutex and event flag handlers (ThreadManForUser).

func (k *Kernel) semaByUID(uid uint32) (*semaObj, bool) {
	o, ok := k.lookup(uid)
	if !ok {
		return nil, false
	}
	s, ok := o.(*semaObj)
	return s, ok
}

func (k *Kernel) mutexByUID(uid uint32) (*mutexObj, bool) {
	o, ok := k.lookup(uid)
	if !ok {
		return nil, false
	}
	m, ok := o.(*mutexObj)
	return m, ok
}

func (k *Kernel) evfByUID(uid uint32) (*eventFlagObj, bool) {
	o, ok := k.lookup(uid)
	if !ok {
		return nil, false
	}
	e, ok := o.(*eventFlagObj)
	return e, ok
}

// --- Semaphores ---

func hleCreateSema(k *Kernel, th *Thread) syscallAction {
	name := k.readCString(k.arg(th, 0), 32)
	attr := k.arg(th, 1)
	initVal := int32(k.arg(th, 2))
	maxVal := int32(k.arg(th, 3))
	if maxVal <= 0 || initVal < 0 || initVal > maxVal {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	s := &semaObj{
		uid:     k.NewUID(kindSema),
		name:    name,
		attr:    attr,
		initVal: initVal,
		maxVal:  maxVal,
		cur:     initVal,
	}
	k.registerObject(s.uid, s)
	k.ret(th, s.uid)
	return actNone
}

func hleWaitSema(k *Kernel, th *Thread) syscallAction {
	s, ok := k.semaByUID(k.arg(th, 0))
	if !ok {
		k.ret(th, ErrUnknownSemid)
		return actNone
	}
	count := int32(k.arg(th, 1))
	if count <= 0 || count > s.maxVal {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	if tryAcquireSema(s, count) {
		k.ret(th, 0)
		return actNone
	}
	deadline, _, poll := k.deadlineFromTimeout(th, k.arg(th, 2))
	if poll {
		k.ret(th, ErrWaitTimeout)
		return actNone
	}
	th.waitCount = count
	s.waiters = append(s.waiters, th)
	return k.blockCurrent(th, waitSema, s.uid, deadline)
}

func hlePollSema(k *Kernel, th *Thread) syscallAction {
	s, ok := k.semaByUID(k.arg(th, 0))
	if !ok {
		k.ret(th, ErrUnknownSemid)
		return actNone
	}
	count := int32(k.arg(th, 1))
	if count <= 0 || count > s.maxVal {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	if tryAcquireSema(s, count) {
		k.ret(th, 0)
	} else {
		k.ret(th, ErrSemaZero)
	}
	return actNone
}

func hleSignalSema(k *Kernel, th *Thread) syscallAction {
	s, ok := k.semaByUID(k.arg(th, 0))
	if !ok {
		k.ret(th, ErrUnknownSemid)
		return actNone
	}
	count := int32(k.arg(th, 1))
	if count < 0 {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	if s.cur+count > s.maxVal {
		k.ret(th, ErrSemaOvf)
		return actNone
	}
	s.cur += count
	k.wakeSemaWaiters(s)
	k.ret(th, 0)
	return actNone
}

func hleDeleteSema(k *Kernel, th *Thread) syscallAction {
	s, ok := k.semaByUID(k.arg(th, 0))
	if !ok {
		k.ret(th, ErrUnknownSemid)
		return actNone
	}
	for _, w := range s.waiters {
		if w.state == thWait && w.wait == waitSema {
			k.completeWait(w, errResult(ErrWaitDelete))
		}
	}
	s.waiters = nil
	k.removeObject(s.uid)
	k.ret(th, 0)
	return actNone
}

func hleReferSemaStatus(k *Kernel, th *Thread) syscallAction {
	s, ok := k.semaByUID(k.arg(th, 0))
	if !ok {
		k.ret(th, ErrUnknownSemid)
		return actNone
	}
	infoPtr := k.arg(th, 1)
	if infoPtr == 0 {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	w := &guestWriter{k: k, addr: infoPtr}
	w.u32(56)
	w.str(s.name, 32)
	w.u32(s.attr)
	w.i32(s.initVal)
	w.i32(s.cur)
	w.i32(s.maxVal)
	w.i32(int32(len(s.waiters)))
	k.ret(th, 0)
	return actNone
}

// --- Mutexes ---

func hleCreateMutex(k *Kernel, th *Thread) syscallAction {
	name := k.readCString(k.arg(th, 0), 32)
	attr := k.arg(th, 1)
	initCount := int32(k.arg(th, 2))
	if initCount < 0 {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	m := &mutexObj{
		uid:       k.NewUID(kindMutex),
		name:      name,
		attr:      attr,
		initCount: initCount,
	}
	if initCount > 0 {
		m.owner = th
		m.lockCount = initCount
	}
	k.registerObject(m.uid, m)
	k.ret(th, m.uid)
	return actNone
}

func hleLockMutex(k *Kernel, th *Thread) syscallAction {
	m, ok := k.mutexByUID(k.arg(th, 0))
	if !ok {
		k.ret(th, ErrMutexNotFound)
		return actNone
	}
	count := int32(k.arg(th, 1))
	if count <= 0 {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	if m.lockCount == 0 {
		m.owner = th
		m.lockCount = count
		k.ret(th, 0)
		return actNone
	}
	if m.owner == th {
		if m.attr&mutexAttrRecursive == 0 {
			k.ret(th, ErrMutexRecursiveDenied)
			return actNone
		}
		m.lockCount += count
		k.ret(th, 0)
		return actNone
	}
	deadline, _, poll := k.deadlineFromTimeout(th, k.arg(th, 2))
	if poll {
		k.ret(th, ErrWaitTimeout)
		return actNone
	}
	th.waitCount = count
	m.waiters = append(m.waiters, th)
	return k.blockCurrent(th, waitMutex, m.uid, deadline)
}

func hleTryLockMutex(k *Kernel, th *Thread) syscallAction {
	m, ok := k.mutexByUID(k.arg(th, 0))
	if !ok {
		k.ret(th, ErrMutexNotFound)
		return actNone
	}
	count := int32(k.arg(th, 1))
	if count <= 0 {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	if m.lockCount == 0 {
		m.owner = th
		m.lockCount = count
		k.ret(th, 0)
		return actNone
	}
	if m.owner == th && m.attr&mutexAttrRecursive != 0 {
		m.lockCount += count
		k.ret(th, 0)
		return actNone
	}
	k.ret(th, ErrMutexLocked)
	return actNone
}

func hleUnlockMutex(k *Kernel, th *Thread) syscallAction {
	m, ok := k.mutexByUID(k.arg(th, 0))
	if !ok {
		k.ret(th, ErrMutexNotFound)
		return actNone
	}
	count := int32(k.arg(th, 1))
	if count <= 0 {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	if m.lockCount == 0 || m.owner != th {
		k.ret(th, ErrMutexUnlocked)
		return actNone
	}
	if count > m.lockCount {
		k.ret(th, ErrMutexUnlockUnderflow)
		return actNone
	}
	m.lockCount -= count
	if m.lockCount == 0 {
		m.owner = nil
		k.wakeMutexWaiter(m)
	}
	k.ret(th, 0)
	return actNone
}

func hleDeleteMutex(k *Kernel, th *Thread) syscallAction {
	m, ok := k.mutexByUID(k.arg(th, 0))
	if !ok {
		k.ret(th, ErrMutexNotFound)
		return actNone
	}
	for _, w := range m.waiters {
		if w.state == thWait && w.wait == waitMutex {
			k.completeWait(w, errResult(ErrWaitDelete))
		}
	}
	m.waiters = nil
	k.removeObject(m.uid)
	k.ret(th, 0)
	return actNone
}

func hleReferMutexStatus(k *Kernel, th *Thread) syscallAction {
	m, ok := k.mutexByUID(k.arg(th, 0))
	if !ok {
		k.ret(th, ErrMutexNotFound)
		return actNone
	}
	infoPtr := k.arg(th, 1)
	if infoPtr == 0 {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	var ownerUID uint32
	if m.owner != nil {
		ownerUID = m.owner.uid
	}
	w := &guestWriter{k: k, addr: infoPtr}
	w.u32(56)
	w.str(m.name, 32)
	w.u32(m.attr)
	w.i32(m.initCount)
	w.i32(m.lockCount)
	w.u32(ownerUID)
	w.i32(int32(len(m.waiters)))
	k.ret(th, 0)
	return actNone
}

// --- Event flags ---

func hleCreateEventFlag(k *Kernel, th *Thread) syscallAction {
	name := k.readCString(k.arg(th, 0), 32)
	attr := k.arg(th, 1)
	initPat := k.arg(th, 2)
	e := &eventFlagObj{
		uid:     k.NewUID(kindEventF),
		name:    name,
		attr:    attr,
		initPat: initPat,
		pattern: initPat,
	}
	k.registerObject(e.uid, e)
	k.ret(th, e.uid)
	return actNone
}

func hleWaitEventFlag(k *Kernel, th *Thread) syscallAction {
	e, ok := k.evfByUID(k.arg(th, 0))
	if !ok {
		k.ret(th, ErrUnknownEvfid)
		return actNone
	}
	pattern := k.arg(th, 1)
	mode := k.arg(th, 2)
	resultPtr := k.arg(th, 3)
	if pattern == 0 {
		k.ret(th, ErrEvfIlpat)
		return actNone
	}
	if evfSatisfied(e.pattern, pattern, mode) {
		result := e.pattern
		if mode&evfWaitClearAll != 0 {
			e.pattern = 0
		} else if mode&evfWaitClearPat != 0 {
			e.pattern &^= pattern
		}
		if resultPtr != 0 {
			k.write32(resultPtr, result)
		}
		k.ret(th, 0)
		return actNone
	}
	timeoutPtr := k.stackArg(th, 4) // the fifth argument rides the stack in o32
	deadline, _, poll := k.deadlineFromTimeout(th, timeoutPtr)
	if poll {
		if resultPtr != 0 {
			k.write32(resultPtr, e.pattern)
		}
		k.ret(th, ErrWaitTimeout)
		return actNone
	}
	th.waitPattern = pattern
	th.waitMode = mode
	th.waitResultAt = resultPtr
	e.waiters = append(e.waiters, th)
	return k.blockCurrent(th, waitEventFlag, e.uid, deadline)
}

func hlePollEventFlag(k *Kernel, th *Thread) syscallAction {
	e, ok := k.evfByUID(k.arg(th, 0))
	if !ok {
		k.ret(th, ErrUnknownEvfid)
		return actNone
	}
	pattern := k.arg(th, 1)
	mode := k.arg(th, 2)
	resultPtr := k.arg(th, 3)
	if pattern == 0 {
		k.ret(th, ErrEvfIlpat)
		return actNone
	}
	if resultPtr != 0 {
		k.write32(resultPtr, e.pattern)
	}
	if !evfSatisfied(e.pattern, pattern, mode) {
		k.ret(th, ErrEvfCond)
		return actNone
	}
	if mode&evfWaitClearAll != 0 {
		e.pattern = 0
	} else if mode&evfWaitClearPat != 0 {
		e.pattern &^= pattern
	}
	k.ret(th, 0)
	return actNone
}

func hleSetEventFlag(k *Kernel, th *Thread) syscallAction {
	e, ok := k.evfByUID(k.arg(th, 0))
	if !ok {
		k.ret(th, ErrUnknownEvfid)
		return actNone
	}
	e.pattern |= k.arg(th, 1)
	k.wakeEventFlagWaiters(e)
	k.ret(th, 0)
	return actNone
}

func hleClearEventFlag(k *Kernel, th *Thread) syscallAction {
	e, ok := k.evfByUID(k.arg(th, 0))
	if !ok {
		k.ret(th, ErrUnknownEvfid)
		return actNone
	}
	e.pattern &^= k.arg(th, 1)
	k.ret(th, 0)
	return actNone
}

func hleDeleteEventFlag(k *Kernel, th *Thread) syscallAction {
	e, ok := k.evfByUID(k.arg(th, 0))
	if !ok {
		k.ret(th, ErrUnknownEvfid)
		return actNone
	}
	for _, w := range e.waiters {
		if w.state == thWait && w.wait == waitEventFlag {
			k.deliverEventFlagResult(w, e.pattern)
			k.completeWait(w, errResult(ErrWaitDelete))
		}
	}
	e.waiters = nil
	k.removeObject(e.uid)
	k.ret(th, 0)
	return actNone
}

func hleReferEventFlagStatus(k *Kernel, th *Thread) syscallAction {
	e, ok := k.evfByUID(k.arg(th, 0))
	if !ok {
		k.ret(th, ErrUnknownEvfid)
		return actNone
	}
	infoPtr := k.arg(th, 1)
	if infoPtr == 0 {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	w := &guestWriter{k: k, addr: infoPtr}
	w.u32(52)
	w.str(e.name, 32)
	w.u32(e.attr)
	w.u32(e.initPat)
	w.u32(e.pattern)
	w.i32(int32(len(e.waiters)))
	k.ret(th, 0)
	return actNone
}
