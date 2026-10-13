package kernel

import "sort"

// Synchronization objects and the wake evaluation rules. Waiters
// are always considered in (priority, wait order) sequence, which
// is the order this kernel wakes them in.

type semaObj struct {
	uid     uint32
	name    string
	attr    uint32
	initVal int32
	maxVal  int32
	cur     int32
	waiters []*Thread
}

func (s *semaObj) objKind() uint32 { return kindSema }

type mutexObj struct {
	uid       uint32
	name      string
	attr      uint32
	initCount int32
	lockCount int32
	owner     *Thread
	waiters   []*Thread
}

func (m *mutexObj) objKind() uint32 { return kindMutex }

// mutexAttrRecursive is the documented recursive attribute bit for
// sceKernelCreateMutex (PSP_MUTEX_ATTR_RECURSIVE).
const mutexAttrRecursive = 0x400

type eventFlagObj struct {
	uid     uint32
	name    string
	attr    uint32
	initPat uint32
	pattern uint32
	waiters []*Thread
}

func (e *eventFlagObj) objKind() uint32 { return kindEventF }

// Event flag wait mode bits (pspsdk pspthreadman.h).
const (
	evfWaitOr       = 0x01 // PSP_EVENT_WAITOR (AND is 0)
	evfWaitClearAll = 0x10 // PSP_EVENT_WAITCLEARALL
	evfWaitClearPat = 0x20 // PSP_EVENT_WAITCLEAR_PAT
	evfAttrMulti    = 0x200
)

type callbackObj struct {
	uid       uint32
	name      string
	fn        uint32
	arg       uint32
	thread    *Thread
	notifyArg int32
	// Callbacks are registered and counted but never dispatched in
	// this slice: no exit or UMD event source exists to fire them.
}

func (c *callbackObj) objKind() uint32 { return kindCallback }

// lwMutexObj is a lightweight mutex. Unlike the UID mutexes, the
// lock state (level and owner) lives in the guest workarea, a
// documented 32 byte structure the kernel reads and writes in
// place: lock level at +0, owning thread UID at +4 (0 = none),
// attribute at +8, waiting thread count at +12, this object's UID
// at +16. The kernel object only tracks the waiter list.
type lwMutexObj struct {
	uid       uint32
	name      string
	attr      uint32
	initCount int32
	workarea  uint32
	waiters   []*Thread
}

func (l *lwMutexObj) objKind() uint32 { return kindLwMutex }

// lwMutexRecursiveMask accepts either published spelling of the
// recursive attribute bit: the kernel headers use 0x200 for the
// lightweight mutex attribute set while older pspsdk documentation
// shows 0x400 for mutexes generally. The guest's own glue writes
// the bit into the workarea attribute at create time.
const lwMutexRecursiveMask = 0x600

// LwMutex workarea field offsets.
const (
	lwOffLevel  = 0
	lwOffOwner  = 4
	lwOffAttr   = 8
	lwOffWaits  = 12
	lwOffUID    = 16
	lwNoSuchUID = 0xFFFFFFFF
)

func (k *Kernel) lwLevel(l *lwMutexObj) int32 { return int32(k.mem.Read32(l.workarea + lwOffLevel)) }
func (k *Kernel) lwOwner(l *lwMutexObj) uint32 {
	return k.mem.Read32(l.workarea + lwOffOwner)
}

func (k *Kernel) lwSetState(l *lwMutexObj, level int32, owner uint32) {
	k.write32(l.workarea+lwOffLevel, uint32(level))
	k.write32(l.workarea+lwOffOwner, owner)
}

func (k *Kernel) lwSetWaitCount(l *lwMutexObj) {
	k.write32(l.workarea+lwOffWaits, uint32(len(l.waiters)))
}

// wakeLwMutexWaiter hands a free lightweight mutex to its first
// live waiter, writing the acquisition into the workarea. Stale
// entries (waits completed by timeout) are dropped.
func (k *Kernel) wakeLwMutexWaiter(l *lwMutexObj) {
	if k.lwLevel(l) != 0 {
		return
	}
	sortWaiters(l.waiters)
	kept := l.waiters[:0]
	done := false
	for _, w := range l.waiters {
		if !done && w.state == thWait && w.wait == waitLwMutex {
			k.lwSetState(l, w.waitCount, w.uid)
			k.completeWait(w, 0)
			k.maybePreempt(w)
			done = true
			continue
		}
		if w.state == thWait && w.wait == waitLwMutex {
			kept = append(kept, w)
		}
	}
	l.waiters = kept
	k.lwSetWaitCount(l)
}

func sortWaiters(ts []*Thread) {
	sort.SliceStable(ts, func(i, j int) bool {
		if ts[i].priority != ts[j].priority {
			return ts[i].priority < ts[j].priority
		}
		return ts[i].seq < ts[j].seq
	})
}

func removeWaiter(list []*Thread, th *Thread) []*Thread {
	for i, w := range list {
		if w == th {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}

// evfPattern returns the current pattern of an event flag object,
// or 0 when the UID is not one.
func (k *Kernel) evfPattern(uid uint32) uint32 {
	if o, ok := k.lookup(uid); ok {
		if e, ok := o.(*eventFlagObj); ok {
			return e.pattern
		}
	}
	return 0
}

// deliverEventFlagResult writes the result pattern a completed (or
// timed out) event flag wait must observe.
func (k *Kernel) deliverEventFlagResult(th *Thread, pattern uint32) {
	if th.waitResultAt != 0 {
		k.write32(th.waitResultAt, pattern)
	}
	th.waitResultAt = 0
}

// tryAcquireSema attempts to take count units for th. It reports
// whether the take succeeded.
func tryAcquireSema(s *semaObj, count int32) bool {
	if s.cur >= count {
		s.cur -= count
		return true
	}
	return false
}

// wakeSemaWaiters satisfies as many waiters of s as the current
// count allows, in (priority, order) sequence.
func (k *Kernel) wakeSemaWaiters(s *semaObj) {
	sortWaiters(s.waiters)
	kept := s.waiters[:0]
	for _, w := range s.waiters {
		if w.state == thWait && w.wait == waitSema && s.cur >= w.waitCount {
			s.cur -= w.waitCount
			k.completeWait(w, 0)
			k.maybePreempt(w)
			continue
		}
		kept = append(kept, w)
	}
	s.waiters = kept
}

// wakeMutexWaiter hands a free mutex to its first waiter, if any.
func (k *Kernel) wakeMutexWaiter(m *mutexObj) {
	if m.lockCount != 0 || len(m.waiters) == 0 {
		return
	}
	sortWaiters(m.waiters)
	w := m.waiters[0]
	m.waiters = m.waiters[1:]
	m.owner = w
	m.lockCount = w.waitCount
	k.completeWait(w, 0)
	k.maybePreempt(w)
}

// evfSatisfied reports whether pattern meets a waiter's condition.
func evfSatisfied(pattern, want, mode uint32) bool {
	if mode&evfWaitOr != 0 {
		return pattern&want != 0
	}
	return pattern&want == want
}

// wakeEventFlagWaiters completes every waiter whose condition the
// flag's current pattern meets, applying each woken waiter's
// clear mode before re-evaluating the next, per the documented
// semantics.
func (k *Kernel) wakeEventFlagWaiters(e *eventFlagObj) {
	sortWaiters(e.waiters)
	kept := e.waiters[:0]
	for _, w := range e.waiters {
		if w.state == thWait && w.wait == waitEventFlag && evfSatisfied(e.pattern, w.waitPattern, w.waitMode) {
			result := e.pattern
			if w.waitMode&evfWaitClearAll != 0 {
				e.pattern = 0
			} else if w.waitMode&evfWaitClearPat != 0 {
				e.pattern &^= w.waitPattern
			}
			k.deliverEventFlagResult(w, result)
			k.completeWait(w, 0)
			k.maybePreempt(w)
			continue
		}
		kept = append(kept, w)
	}
	e.waiters = kept
}

// detachWaiter removes a thread from whatever object wait list it
// sits on (used by terminate and delete paths).
func (k *Kernel) detachWaiter(th *Thread) {
	o, ok := k.lookup(th.waitUID)
	if !ok {
		return
	}
	switch obj := o.(type) {
	case *semaObj:
		obj.waiters = removeWaiter(obj.waiters, th)
	case *mutexObj:
		obj.waiters = removeWaiter(obj.waiters, th)
	case *eventFlagObj:
		obj.waiters = removeWaiter(obj.waiters, th)
	case *lwMutexObj:
		obj.waiters = removeWaiter(obj.waiters, th)
		k.lwSetWaitCount(obj)
	}
}
