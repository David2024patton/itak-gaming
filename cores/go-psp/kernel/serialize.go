package kernel

import (
	"encoding/binary"
	"errors"
	"sort"

	"github.com/davidpatton/itak-gaming/cores/go-psp/ge"
)

// Save state serialization for the kernel (the system embeds this
// blob in its version 3 states). The format is versioned
// internally and fully deterministic: maps are written in sorted
// or creation order, so identical machines produce identical
// bytes.

const kernelStateMagic = "PSPK"

// kernelStateVersion 3 (PSP-4) appends the sceAudio block
// (channels, mixer position, output ring, routing modes) after
// the file descriptor block. Version 2 blobs are rejected.
const kernelStateVersion = 3

type stateWriter struct{ buf []byte }

func (w *stateWriter) u8(v uint8)   { w.buf = append(w.buf, v) }
func (w *stateWriter) i16(v int16)  { w.u32(uint32(uint16(v))) }
func (w *stateWriter) u32(v uint32) { w.buf = binary.LittleEndian.AppendUint32(w.buf, v) }
func (w *stateWriter) i32(v int32)  { w.u32(uint32(v)) }
func (w *stateWriter) u64(v uint64) { w.buf = binary.LittleEndian.AppendUint64(w.buf, v) }
func (w *stateWriter) i64(v int64)  { w.u64(uint64(v)) }
func (w *stateWriter) str(s string) {
	w.u32(uint32(len(s)))
	w.buf = append(w.buf, s...)
}
func (w *stateWriter) bytes(b []byte) {
	w.u32(uint32(len(b)))
	w.buf = append(w.buf, b...)
}
func (w *stateWriter) boolean(v bool) {
	if v {
		w.u8(1)
	} else {
		w.u8(0)
	}
}

type stateReader struct {
	buf []byte
	pos int
	err error
}

func (r *stateReader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || r.pos+n > len(r.buf) {
		r.err = errors.New("kernel: state blob truncated")
		return nil
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	return b
}

func (r *stateReader) u8() uint8 {
	b := r.take(1)
	if b == nil {
		return 0
	}
	return b[0]
}

func (r *stateReader) u32() uint32 {
	b := r.take(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func (r *stateReader) i32() int32 { return int32(r.u32()) }

func (r *stateReader) u64() uint64 {
	b := r.take(8)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

func (r *stateReader) i64() int64 { return int64(r.u64()) }

func (r *stateReader) str() string {
	n := int(r.u32())
	b := r.take(n)
	if b == nil {
		return ""
	}
	return string(b)
}

func (r *stateReader) bytes() []byte {
	n := int(r.u32())
	b := r.take(n)
	if b == nil {
		return nil
	}
	out := make([]byte, n)
	copy(out, b)
	return out
}

func (r *stateReader) boolean() bool { return r.u8() != 0 }

// Serialize returns the full kernel state.
func (k *Kernel) Serialize() []byte {
	w := &stateWriter{}
	w.buf = append(w.buf, kernelStateMagic...)
	w.u32(kernelStateVersion)
	w.u64(k.cycles)
	w.u32(k.vcount)
	w.boolean(k.exited)
	w.i32(k.exitCode)
	w.u32(k.uidSeq)
	w.u64(k.seq)
	w.u32(k.nextCode)
	w.u32(k.sdkVersion)
	w.u32(k.exitCallbackUID)
	w.str(k.bootPath)
	w.u32(k.moduleBlockUID)
	w.u32(k.moduleBase)
	w.u32(k.moduleSize)
	for _, v := range k.dispMode {
		w.u32(v)
	}
	w.boolean(k.dispSet)
	w.u32(k.samplingCtl)

	w.bytes(k.console)

	unknowns := k.UnknownCalls()
	w.u32(uint32(len(unknowns)))
	for _, u := range unknowns {
		w.str(u.Module)
		w.u32(u.NID)
		w.u32(uint32(u.Count))
	}

	assigned := make([]modNid, 0, len(k.assigned))
	for key := range k.assigned {
		assigned = append(assigned, key)
	}
	sort.Slice(assigned, func(i, j int) bool {
		if assigned[i].module != assigned[j].module {
			return assigned[i].module < assigned[j].module
		}
		return assigned[i].nid < assigned[j].nid
	})
	w.u32(uint32(len(assigned)))
	for _, key := range assigned {
		w.str(key.module)
		w.u32(key.nid)
		w.u32(k.assigned[key])
	}

	w.u32(uint32(len(k.part.blocks)))
	for _, b := range k.part.blocks {
		w.u32(b.uid)
		w.str(b.name)
		w.u32(b.addr)
		w.u32(b.size)
	}

	w.u32(uint32(len(k.order)))
	for _, th := range k.order {
		w.u32(th.uid)
		w.str(th.name)
		w.u32(th.entry)
		w.u32(th.attr)
		w.i32(th.initPriority)
		w.i32(th.priority)
		w.u8(uint8(th.state))
		w.u32(th.stackAddr)
		w.u32(th.stackSize)
		w.u32(th.stackBlock)
		w.i32(th.exitStatus)
		w.u64(th.runCycles)
		w.u8(uint8(th.wait))
		w.u32(th.waitUID)
		w.u64(th.waitDeadline)
		w.u32(th.waitPattern)
		w.u32(th.waitMode)
		w.u32(th.waitResultAt)
		w.i32(th.waitCount)
		w.u64(th.seq)
		w.i32(th.waitResult)
		w.boolean(th.hasResult)
		w.boolean(th.loaded)
		w.bytes(th.ctx)
	}
	var currentUID uint32
	if k.current != nil {
		currentUID = k.current.uid
	}
	w.u32(currentUID)

	semas, mutexes, evfs, cbs, lwms := k.syncObjects()
	w.u32(uint32(len(semas)))
	for _, s := range semas {
		w.u32(s.uid)
		w.str(s.name)
		w.u32(s.attr)
		w.i32(s.initVal)
		w.i32(s.maxVal)
		w.i32(s.cur)
		writeWaiters(w, s.waiters)
	}
	w.u32(uint32(len(mutexes)))
	for _, m := range mutexes {
		w.u32(m.uid)
		w.str(m.name)
		w.u32(m.attr)
		w.i32(m.initCount)
		w.i32(m.lockCount)
		var owner uint32
		if m.owner != nil {
			owner = m.owner.uid
		}
		w.u32(owner)
		writeWaiters(w, m.waiters)
	}
	w.u32(uint32(len(evfs)))
	for _, e := range evfs {
		w.u32(e.uid)
		w.str(e.name)
		w.u32(e.attr)
		w.u32(e.initPat)
		w.u32(e.pattern)
		writeWaiters(w, e.waiters)
	}
	w.u32(uint32(len(cbs)))
	for _, c := range cbs {
		w.u32(c.uid)
		w.str(c.name)
		w.u32(c.fn)
		w.u32(c.arg)
		var owner uint32
		if c.thread != nil {
			owner = c.thread.uid
		}
		w.u32(owner)
		w.i32(c.notifyArg)
	}
	w.u32(uint32(len(lwms)))
	for _, l := range lwms {
		w.u32(l.uid)
		w.str(l.name)
		w.u32(l.attr)
		w.i32(l.initCount)
		w.u32(l.workarea)
		writeWaiters(w, l.waiters)
	}

	w.i32(k.nextGeID)
	geIDs := make([]int32, 0, len(k.geLists))
	for id := range k.geLists {
		geIDs = append(geIDs, id)
	}
	sort.Slice(geIDs, func(i, j int) bool { return geIDs[i] < geIDs[j] })
	w.u32(uint32(len(geIDs)))
	for _, id := range geIDs {
		l := k.geLists[id]
		w.i32(l.id)
		w.u32(uint32(l.state))
		w.u32(l.stallAddr)
	}

	w.i32(int32(k.nextFD))
	fdIDs := make([]int, 0, len(k.fds))
	for id := range k.fds {
		fdIDs = append(fdIDs, id)
	}
	sort.Ints(fdIDs)
	w.u32(uint32(len(fdIDs)))
	for _, id := range fdIDs {
		f := k.fds[id]
		w.i32(int32(id))
		w.boolean(f.isDir)
		w.boolean(f.writing)
		w.boolean(f.fromMS)
		w.str(f.keyPrefix)
		w.str(f.isoPath)
		w.str(f.msKey)
		w.i64(f.pos)
		w.u32(uint32(len(f.dirNames)))
		for _, n := range f.dirNames {
			w.str(n)
		}
		w.i32(int32(f.dirIdx))
		w.str(f.dirPath)
	}

	// sceAudio block (version 3).
	w.u64(k.audioMixed)
	w.u32(uint32(len(k.audioOut)))
	for _, s := range k.audioOut {
		w.i16(s)
	}
	w.i32(k.audioRoutingMode)
	w.i32(k.audioRoutingVolMode)
	for i := range k.audioChans {
		c := &k.audioChans[i]
		w.boolean(c.reserved)
		w.u32(c.sampleCount)
		w.u32(c.format)
		w.i32(c.leftVol)
		w.i32(c.rightVol)
		w.u32(c.addr)
		w.u32(c.remaining)
		var waiterUID uint32
		if c.waiter != nil {
			waiterUID = c.waiter.uid
		}
		w.u32(waiterUID)
		w.u32(c.waitAddr)
		w.i32(c.waitLeftVol)
		w.i32(c.waitRightVol)
	}
	return w.buf
}

func writeWaiters(w *stateWriter, ts []*Thread) {
	w.u32(uint32(len(ts)))
	for _, t := range ts {
		w.u32(t.uid)
	}
}

func (k *Kernel) syncObjects() (semas []*semaObj, mutexes []*mutexObj, evfs []*eventFlagObj, cbs []*callbackObj, lwms []*lwMutexObj) {
	for _, o := range k.objects {
		switch obj := o.(type) {
		case *semaObj:
			semas = append(semas, obj)
		case *mutexObj:
			mutexes = append(mutexes, obj)
		case *eventFlagObj:
			evfs = append(evfs, obj)
		case *callbackObj:
			cbs = append(cbs, obj)
		case *lwMutexObj:
			lwms = append(lwms, obj)
		}
	}
	sort.Slice(semas, func(i, j int) bool { return semas[i].uid < semas[j].uid })
	sort.Slice(mutexes, func(i, j int) bool { return mutexes[i].uid < mutexes[j].uid })
	sort.Slice(evfs, func(i, j int) bool { return evfs[i].uid < evfs[j].uid })
	sort.Slice(cbs, func(i, j int) bool { return cbs[i].uid < cbs[j].uid })
	sort.Slice(lwms, func(i, j int) bool { return lwms[i].uid < lwms[j].uid })
	return semas, mutexes, evfs, cbs, lwms
}

// Deserialize restores a state produced by Serialize into this
// kernel. The host (memory contents, CPU, ISO, memory stick store)
// is restored by the system around this call; the loaded thread's
// context is re-deserialized into the CPU at the end when one was
// running.
func (k *Kernel) Deserialize(data []byte) error {
	r := &stateReader{buf: data}
	if string(r.take(4)) != kernelStateMagic {
		return errors.New("kernel: bad state magic")
	}
	if v := r.u32(); v != kernelStateVersion {
		return errors.New("kernel: unsupported kernel state version")
	}
	k.cycles = r.u64()
	k.vcount = r.u32()
	k.exited = r.boolean()
	k.exitCode = r.i32()
	k.uidSeq = r.u32()
	k.seq = r.u64()
	k.nextCode = r.u32()
	k.sdkVersion = r.u32()
	k.exitCallbackUID = r.u32()
	k.bootPath = r.str()
	k.moduleBlockUID = r.u32()
	k.moduleBase = r.u32()
	k.moduleSize = r.u32()
	for i := range k.dispMode {
		k.dispMode[i] = r.u32()
	}
	k.dispSet = r.boolean()
	k.samplingCtl = r.u32()
	k.console = r.bytes()

	k.unknown = map[modNid]*UnknownCall{}
	for n := int(r.u32()); n > 0; n-- {
		u := &UnknownCall{Module: r.str(), NID: r.u32(), Count: int(r.u32())}
		k.unknown[modNid{u.Module, u.NID}] = u
	}

	k.assigned = map[modNid]uint32{}
	k.codes = map[uint32]*syscallEntry{}
	for n := int(r.u32()); n > 0; n-- {
		key := modNid{r.str(), r.u32()}
		code := r.u32()
		k.assigned[key] = code
		if e, ok := registry[key]; ok {
			cp := e
			k.codes[code] = &cp
		} else {
			k.codes[code] = nil
		}
	}

	k.objects = map[uint32]object{}
	k.part = newPartition(UserPartitionBase, UserPartitionSize)
	for n := int(r.u32()); n > 0; n-- {
		b := &memBlock{uid: r.u32(), name: r.str(), addr: r.u32(), size: r.u32()}
		k.part.blocks = append(k.part.blocks, b)
		k.objects[b.uid] = b
	}
	sort.Slice(k.part.blocks, func(i, j int) bool { return k.part.blocks[i].addr < k.part.blocks[j].addr })

	k.threads = map[uint32]*Thread{}
	k.order = nil
	for n := int(r.u32()); n > 0; n-- {
		th := &Thread{}
		th.uid = r.u32()
		th.name = r.str()
		th.entry = r.u32()
		th.attr = r.u32()
		th.initPriority = r.i32()
		th.priority = r.i32()
		th.state = threadState(r.u8())
		th.stackAddr = r.u32()
		th.stackSize = r.u32()
		th.stackBlock = r.u32()
		th.exitStatus = r.i32()
		th.runCycles = r.u64()
		th.wait = waitKind(r.u8())
		th.waitUID = r.u32()
		th.waitDeadline = r.u64()
		th.waitPattern = r.u32()
		th.waitMode = r.u32()
		th.waitResultAt = r.u32()
		th.waitCount = r.i32()
		th.seq = r.u64()
		th.waitResult = r.i32()
		th.hasResult = r.boolean()
		th.loaded = r.boolean()
		th.ctx = r.bytes()
		k.threads[th.uid] = th
		k.order = append(k.order, th)
		k.objects[th.uid] = th
	}
	currentUID := r.u32()
	k.current = nil
	if currentUID != 0 {
		k.current = k.threads[currentUID]
	}

	readWaiters := func() []*Thread {
		n := int(r.u32())
		out := make([]*Thread, 0, n)
		for i := 0; i < n; i++ {
			if th, ok := k.threads[r.u32()]; ok {
				out = append(out, th)
			}
		}
		return out
	}
	for n := int(r.u32()); n > 0; n-- {
		s := &semaObj{}
		s.uid = r.u32()
		s.name = r.str()
		s.attr = r.u32()
		s.initVal = r.i32()
		s.maxVal = r.i32()
		s.cur = r.i32()
		s.waiters = readWaiters()
		k.objects[s.uid] = s
	}
	for n := int(r.u32()); n > 0; n-- {
		m := &mutexObj{}
		m.uid = r.u32()
		m.name = r.str()
		m.attr = r.u32()
		m.initCount = r.i32()
		m.lockCount = r.i32()
		owner := r.u32()
		if owner != 0 {
			m.owner = k.threads[owner]
		}
		m.waiters = readWaiters()
		k.objects[m.uid] = m
	}
	for n := int(r.u32()); n > 0; n-- {
		e := &eventFlagObj{}
		e.uid = r.u32()
		e.name = r.str()
		e.attr = r.u32()
		e.initPat = r.u32()
		e.pattern = r.u32()
		e.waiters = readWaiters()
		k.objects[e.uid] = e
	}
	for n := int(r.u32()); n > 0; n-- {
		c := &callbackObj{}
		c.uid = r.u32()
		c.name = r.str()
		c.fn = r.u32()
		c.arg = r.u32()
		owner := r.u32()
		if owner != 0 {
			c.thread = k.threads[owner]
		}
		c.notifyArg = r.i32()
		k.objects[c.uid] = c
	}
	for n := int(r.u32()); n > 0; n-- {
		l := &lwMutexObj{}
		l.uid = r.u32()
		l.name = r.str()
		l.attr = r.u32()
		l.initCount = r.i32()
		l.workarea = r.u32()
		l.waiters = readWaiters()
		k.objects[l.uid] = l
	}

	k.nextGeID = r.i32()
	k.geLists = map[int32]*geList{}
	for n := int(r.u32()); n > 0; n-- {
		l := &geList{id: r.i32(), state: ge.ListState(r.u32()), stallAddr: r.u32()}
		k.geLists[l.id] = l
	}

	k.nextFD = int(r.i32())
	k.fds = map[int]*fileDesc{}
	for n := int(r.u32()); n > 0; n-- {
		id := int(r.i32())
		f := &fileDesc{}
		f.isDir = r.boolean()
		f.writing = r.boolean()
		f.fromMS = r.boolean()
		f.keyPrefix = r.str()
		f.isoPath = r.str()
		f.msKey = r.str()
		f.pos = r.i64()
		for m := int(r.u32()); m > 0; m-- {
			f.dirNames = append(f.dirNames, r.str())
		}
		f.dirIdx = int(r.i32())
		f.dirPath = r.str()
		k.fds[id] = f
	}

	// sceAudio block (version 3).
	k.audioMixed = r.u64()
	if n := int(r.u32()); n > 0 {
		k.audioOut = make([]int16, n)
		for i := range k.audioOut {
			k.audioOut[i] = int16(uint16(r.u32()))
		}
	} else {
		k.audioOut = nil
	}
	k.audioRoutingMode = r.i32()
	k.audioRoutingVolMode = r.i32()
	for i := range k.audioChans {
		c := &k.audioChans[i]
		c.reserved = r.boolean()
		c.sampleCount = r.u32()
		c.format = r.u32()
		c.leftVol = r.i32()
		c.rightVol = r.i32()
		c.addr = r.u32()
		c.remaining = r.u32()
		waiterUID := r.u32()
		c.waitAddr = r.u32()
		c.waitLeftVol = r.i32()
		c.waitRightVol = r.i32()
		c.waiter = nil
		if waiterUID != 0 {
			c.waiter = k.threads[waiterUID]
		}
	}
	if r.err != nil {
		return r.err
	}
	if r.pos != len(r.buf) {
		return errors.New("kernel: trailing bytes in state blob")
	}
	// Reinstall the hook (a fresh kernel object may have been used).
	k.cpu.SyscallHook = k.handleSyscall
	if k.current != nil && k.current.loaded {
		if err := k.cpu.Deserialize(k.current.ctx); err != nil {
			return err
		}
	}
	return nil
}
