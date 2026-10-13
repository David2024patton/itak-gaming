package kernel

import (
	"encoding/binary"

	"github.com/davidpatton/itak-gaming/cores/go-psp/chips/allegrex"
)

// Thread states (the simplified documented set; dormant is the
// created-but-not-started state the hardware also distinguishes).
type threadState uint8

const (
	thDormant threadState = iota
	thReady
	thRun
	thWait
	thDead
)

// Wait kinds a thread can be parked in.
type waitKind uint8

const (
	waitNone waitKind = iota
	waitDelay
	waitSleep
	waitSema
	waitMutex
	waitEventFlag
	waitVblank
	waitThreadEnd
	waitLwMutex
)

// timesliceInsns is the documented timeslice check interval: every
// 10,000 instructions of a running thread, the scheduler checks
// whether an equal or higher priority thread is ready and rotates
// to it if so.
const timesliceInsns = 10000

// Thread is one kernel thread: a saved Allegrex context plus its
// scheduling and wait state.
type Thread struct {
	uid          uint32
	name         string
	entry        uint32
	attr         uint32
	initPriority int32
	priority     int32
	state        threadState

	ctx    []byte // serialized allegrex state while not loaded
	loaded bool

	stackAddr  uint32
	stackSize  uint32
	stackBlock uint32 // partition block UID, 0 for none

	exitStatus int32
	runCycles  uint64

	// Wait state.
	wait         waitKind
	waitUID      uint32
	waitDeadline uint64 // kernel cycle at which a timeout fires; 0 = never
	waitPattern  uint32 // event flag pattern
	waitMode     uint32 // event flag wait mode
	waitResultAt uint32 // address to receive the event flag result pattern
	waitCount    int32  // sema/mutex count requested
	seq          uint64 // order stamp for FIFO within a priority

	blocked     bool  // set by the syscall dispatcher when a handler parks us
	waitResult  int32 // $v0 delivered when the wait completes
	hasResult   bool
	dispatchIns uint64 // instructions run in the current dispatch
}

func (t *Thread) objKind() uint32 { return kindThread }

// UID returns the thread's SceUID.
func (t *Thread) UID() uint32 { return t.uid }

// Name returns the thread's name.
func (t *Thread) Name() string { return t.name }

// createThread builds a dormant thread with a fresh context: PC at
// entry, $sp at the top of its stack minus the documented 256 byte
// kernel reservation, $ra = 0 (the exit trampoline), $gp as given.
func (k *Kernel) createThread(name string, entry uint32, priority int32, stackSize uint32, attr uint32) (*Thread, error) {
	if entry == 0 {
		return nil, errCode(ErrIllegalEntry)
	}
	if priority < 0 || priority > 119 {
		return nil, errCode(ErrIllegalPriority)
	}
	if stackSize != 0 && stackSize < 512 {
		return nil, errCode(ErrIllegalStackSize)
	}
	if stackSize == 0 {
		stackSize = 0x1000
	}
	uid := k.NewUID(kindThread)
	th := &Thread{
		uid:          uid,
		name:         name,
		entry:        entry,
		attr:         attr,
		initPriority: priority,
		priority:     priority,
		state:        thDormant,
	}
	blockUID, addr, err := k.part.allocInternal(k, "stack/"+name, allocHigh, stackSize, 0)
	if err != nil {
		return nil, errCode(ErrMemBlockAllocFailed)
	}
	th.stackBlock = blockUID
	th.stackAddr = addr
	th.stackSize = roundBlockSize(stackSize)
	tmp := allegrex.New(k.mem)
	tmp.Reset(entry)
	tmp.SetSR(allegrex.ThreadStatus)      // firmware thread status (FPU on, RAM vectors)
	tmp.SetFCSR(allegrex.DefaultFCSR)     // firmware FPU control defaults
	tmp.SetReg(29, addr+th.stackSize-256) // $sp below the k0 reservation
	tmp.SetReg(31, 0)                     // $ra: return exits the thread
	th.ctx = tmp.Serialize()
	k.registerObject(uid, th)
	k.threads[uid] = th
	k.order = append(k.order, th)
	return th, nil
}

// startThreadPrepared readies a thread whose context already holds
// its arguments (used for the main thread by LoadModule).
func (k *Kernel) startThreadPrepared(th *Thread, a0, a1, gp uint32) {
	ctxSetReg(th.ctx, 4, a0)
	ctxSetReg(th.ctx, 5, a1)
	ctxSetReg(th.ctx, 28, gp)
	th.state = thReady
	th.seq = k.nextSeq()
}

// ctxSetReg patches one GPR word inside a serialized allegrex
// context. The serialized layout starts with the 32 GPRs.
func ctxSetReg(ctx []byte, reg int, v uint32) {
	binary.LittleEndian.PutUint32(ctx[reg*4:], v)
}

func ctxReg(ctx []byte, reg int) uint32 {
	return binary.LittleEndian.Uint32(ctx[reg*4:])
}

func (k *Kernel) nextSeq() uint64 {
	k.seq++
	return k.seq
}

type errCode uint32

func (e errCode) Error() string { return "kernel error" }

// threadByUID resolves a UID to a thread.
func (k *Kernel) threadByUID(uid uint32) (*Thread, bool) {
	th, ok := k.threads[uid]
	return th, ok
}

// Run advances the kernel by up to budget cycles. It is the
// scheduler's main loop and implements the documented fast
// forward: when no thread is runnable but some thread waits on a
// deadline inside the budget, the sysclock jumps to that deadline;
// when nothing can run at all before the budget ends, the sysclock
// jumps straight to the end of the budget.
func (k *Kernel) Run(budget uint64) {
	if k.exited {
		return
	}
	end := k.cycles + budget
	for k.cycles < end {
		th := k.pickReady(end)
		if th == nil {
			k.cycles = end
			break
		}
		k.dispatch(th, end)
		if k.exited {
			break
		}
		if k.nothingCanRun() {
			k.exited = true
			break
		}
	}
}

// nothingCanRun reports whether no thread can ever make progress
// again: none ready, running, dormant, or waiting on anything the
// machine can deliver (a deadline or a vblank).
func (k *Kernel) nothingCanRun() bool {
	for _, th := range k.order {
		switch th.state {
		case thReady, thRun, thDormant:
			return false
		case thWait:
			if th.waitDeadline != 0 || th.wait == waitVblank {
				return false
			}
		}
	}
	return true
}

// pickReady expires elapsed waits, then returns the highest
// priority ready thread (lowest priority number; FIFO within a
// priority). If none is ready it fast forwards the sysclock to the
// earliest wait deadline within the budget and retries.
func (k *Kernel) pickReady(end uint64) *Thread {
	for {
		k.expireWaits()
		var best *Thread
		for _, th := range k.order {
			if th.state != thReady {
				continue
			}
			if best == nil || th.priority < best.priority ||
				(th.priority == best.priority && th.seq < best.seq) {
				best = th
			}
		}
		if best != nil {
			return best
		}
		next := uint64(0)
		for _, th := range k.order {
			if th.state == thWait && th.waitDeadline != 0 && th.waitDeadline > k.cycles {
				if next == 0 || th.waitDeadline < next {
					next = th.waitDeadline
				}
			}
		}
		if next == 0 || next >= end {
			return nil
		}
		k.cycles = next
	}
}

// expireWaits completes every wait whose deadline has passed, in
// (priority, order) sequence for determinism. Delay waits complete
// successfully; object waits complete with WAIT_TIMEOUT.
func (k *Kernel) expireWaits() {
	var expired []*Thread
	for _, th := range k.order {
		if th.state == thWait && th.waitDeadline != 0 && th.waitDeadline <= k.cycles {
			expired = append(expired, th)
		}
	}
	sortThreads(expired)
	for _, th := range expired {
		switch th.wait {
		case waitDelay, waitSleep:
			k.completeWait(th, 0)
		case waitSema, waitMutex, waitEventFlag, waitThreadEnd, waitLwMutex:
			if th.wait == waitEventFlag {
				k.deliverEventFlagResult(th, k.evfPattern(th.waitUID))
			}
			k.completeWait(th, errResult(ErrWaitTimeout))
		}
	}
}

func sortThreads(ts []*Thread) {
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0; j-- {
			a, b := ts[j-1], ts[j]
			if a.priority < b.priority || (a.priority == b.priority && a.seq < b.seq) {
				break
			}
			ts[j-1], ts[j] = b, a
		}
	}
}

// completeWait readies a waiting thread and records the $v0 it
// will see when next dispatched.
func (k *Kernel) completeWait(th *Thread, result int32) {
	th.state = thReady
	th.wait = waitNone
	th.waitUID = 0
	th.waitDeadline = 0
	th.waitResult = result
	th.hasResult = true
	th.seq = k.nextSeq()
}

// blockCurrent parks the running thread in the given wait and
// tells the dispatcher to switch away.
func (k *Kernel) blockCurrent(th *Thread, w waitKind, uid uint32, deadline uint64) syscallAction {
	th.state = thWait
	th.wait = w
	th.waitUID = uid
	th.waitDeadline = deadline
	th.seq = k.nextSeq()
	return actBlock
}

// deadlineFromTimeout converts a guest timeout pointer (address of
// a microsecond count, 0 for wait forever) into a cycle deadline.
func (k *Kernel) deadlineFromTimeout(th *Thread, timeoutPtr uint32) (deadline uint64, isTimeout bool, timedOutNow bool) {
	if timeoutPtr == 0 {
		return 0, false, false
	}
	us := k.mem.Read32(timeoutPtr)
	if us == 0 {
		return 0, true, true // a zero timeout polls: already expired
	}
	return k.cycles + uint64(us)*CyclesPerMicro, true, false
}

// switchTo loads a thread's context into the shared CPU and
// delivers any pending wait result into $v0.
func (k *Kernel) switchTo(th *Thread) {
	if err := k.cpu.Deserialize(th.ctx); err != nil {
		panic(err) // a context this kernel serialized always parses
	}
	// Returning to a thread passes through the kernel, which
	// breaks any LL reservation the thread held (ERET semantics).
	k.cpu.ClearLL()
	th.loaded = true
	k.current = th
	if th.hasResult {
		k.cpu.SetReg(2, uint32(th.waitResult))
		th.hasResult = false
	}
	th.state = thRun
	th.dispatchIns = 0
}

// saveContext stores the running thread's CPU state back.
func (k *Kernel) saveContext(th *Thread) {
	th.ctx = k.cpu.Serialize()
	th.loaded = false
	if k.current == th {
		k.current = nil
	}
}

// dispatch runs one thread until it blocks, exits, is preempted at
// a scheduling point, or the budget ends.
func (k *Kernel) dispatch(th *Thread, end uint64) {
	k.switchTo(th)
	for k.cycles < end && !k.exited {
		c := k.cpu.Step()
		if c < 1 {
			c = 1
		}
		k.cycles += uint64(c)
		th.runCycles += uint64(c)
		th.dispatchIns++
		if th.state == thDead {
			return // context is dead weight; do not save
		}
		if th.blocked {
			th.blocked = false
			k.saveContext(th)
			return
		}
		if k.cpu.PC() == 0 {
			// Return trampoline: the thread function returned
			// with $ra = 0, which exits the thread with $v0.
			status := int32(k.cpu.Reg(2))
			k.exitThread(th, status, false)
			return
		}
		if k.resched {
			k.resched = false
			k.saveContext(th)
			if th.state == thRun {
				th.state = thReady
				th.seq = k.nextSeq()
			}
			return
		}
		if th.dispatchIns >= timesliceInsns {
			th.dispatchIns = 0
			k.expireWaits()
			if k.readyPeerExists(th) {
				k.saveContext(th)
				th.state = thReady
				th.seq = k.nextSeq()
				return
			}
		}
	}
	if th.state == thRun {
		k.saveContext(th)
		th.state = thReady
		th.seq = k.nextSeq()
	}
}

// readyPeerExists reports whether a ready thread of equal or higher
// priority (numerically lower or equal) exists.
func (k *Kernel) readyPeerExists(th *Thread) bool {
	for _, o := range k.order {
		if o != th && o.state == thReady && o.priority <= th.priority {
			return true
		}
	}
	return false
}

// exitThread moves a thread to the dead state, wakes any
// WaitThreadEnd waiters with its status, and handles deletion.
func (k *Kernel) exitThread(th *Thread, status int32, delete bool) {
	th.state = thDead
	th.exitStatus = status
	th.wait = waitNone
	th.waitDeadline = 0
	if th.loaded {
		th.loaded = false
		if k.current == th {
			k.current = nil
		}
	}
	// Wake WaitThreadEnd waiters in priority order.
	var waiters []*Thread
	for _, o := range k.order {
		if o.state == thWait && o.wait == waitThreadEnd && o.waitUID == th.uid {
			waiters = append(waiters, o)
		}
	}
	sortThreads(waiters)
	for _, w := range waiters {
		k.completeWait(w, status)
		k.maybePreempt(w)
	}
	if delete {
		k.deleteThreadObject(th)
	}
}

// deleteThreadObject frees a thread's stack block and removes its
// object registrations.
func (k *Kernel) deleteThreadObject(th *Thread) {
	if th.stackBlock != 0 {
		k.part.free(th.stackBlock)
		k.removeObject(th.stackBlock)
		th.stackBlock = 0
	}
	k.removeObject(th.uid)
	delete(k.threads, th.uid)
	for i, o := range k.order {
		if o == th {
			k.order = append(k.order[:i], k.order[i+1:]...)
			break
		}
	}
}

// maybePreempt asks for a reschedule when a newly readied thread
// outranks the running one.
func (k *Kernel) maybePreempt(woken *Thread) {
	if cur := k.current; cur != nil && woken.priority < cur.priority {
		k.resched = true
	}
}

// NotifyVblank is called by the machine at each frame end: it
// bumps the vblank counter and wakes vblank waiters in priority
// order.
func (k *Kernel) NotifyVblank() {
	k.vcount++
	k.inVblank = true
	var waiters []*Thread
	for _, th := range k.order {
		if th.state == thWait && th.wait == waitVblank {
			waiters = append(waiters, th)
		}
	}
	sortThreads(waiters)
	for _, th := range waiters {
		k.completeWait(th, 0)
	}
	k.inVblank = false
}

// InVblank reports whether vblank waiters are being woken right
// now (the only moment guest code can observe the vblank in this
// frame synchronous model is never; see the README).
func (k *Kernel) InVblank() bool { return k.inVblank }

// errResult reinterprets an unsigned kernel error code as the
// signed $v0 value a completed wait delivers.
func errResult(code uint32) int32 { return int32(code) }
