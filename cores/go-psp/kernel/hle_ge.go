package kernel

import "github.com/davidpatton/itak-gaming/cores/go-psp/ge"

// Display, controller and GE handlers. The display and GE work
// goes through the GEHost the system installs, since the GE
// engine and the presented framebuffer live in the system
// package (PSP-2).

func hleDisplaySetMode(k *Kernel, th *Thread) syscallAction {
	// (mode, width, height): stored in the kernel's display fields.
	k.dispMode = [3]uint32{k.arg(th, 1), k.arg(th, 2), k.arg(th, 0)}
	k.dispSet = true
	k.ret(th, 0)
	return actNone
}

func hleDisplaySetFrameBuf(k *Kernel, th *Thread) syscallAction {
	topAddr := k.arg(th, 0)
	bufferWidth := int(k.arg(th, 1))
	pixelFormat := int(k.arg(th, 2))
	// arg 3 is the sync mode; presentation is immediate here.
	k.host.PresentFrameBuffer(topAddr, bufferWidth, pixelFormat)
	k.ret(th, 0)
	return actNone
}

func hleDisplayWaitVblankStart(k *Kernel, th *Thread) syscallAction {
	if k.inVblank {
		k.ret(th, 0)
		return actNone
	}
	return k.blockCurrent(th, waitVblank, 0, 0)
}

func hleDisplayGetVcount(k *Kernel, th *Thread) syscallAction {
	k.ret(th, uint32(k.vcount))
	return actNone
}

func hleDisplayIsVblank(k *Kernel, th *Thread) syscallAction {
	if k.inVblank {
		k.ret(th, 1)
	} else {
		k.ret(th, 0)
	}
	return actNone
}

// SceCtrlData (24 bytes): TimeStamp u32, Buttons u32, Lx u8,
// Ly u8, Rsrv[6], plus the analog extension fields this kernel
// leaves zeroed (the documented structure's first 16 bytes are
// what the corpus and the brief exercise).
const (
	ctrlButtonSelect   = 0x000001
	ctrlButtonStart    = 0x000008
	ctrlButtonUp       = 0x000010
	ctrlButtonRight    = 0x000020
	ctrlButtonDown     = 0x000040
	ctrlButtonLeft     = 0x000080
	ctrlButtonLTrigger = 0x000100
	ctrlButtonRTrigger = 0x000200
	ctrlButtonTriangle = 0x001000
	ctrlButtonCircle   = 0x002000
	ctrlButtonCross    = 0x004000
	ctrlButtonSquare   = 0x008000
	ctrlButtonHold     = 0x200000
)

func hleCtrlRead(k *Kernel, th *Thread) syscallAction {
	bufPtr := k.arg(th, 0)
	count := int(k.arg(th, 1))
	if count < 1 {
		count = 1
	}
	for i := 0; i < count; i++ {
		w := &guestWriter{k: k, addr: bufPtr + uint32(i*24)}
		w.u32(uint32(k.SysClockMicros())) // TimeStamp
		w.u32(k.host.Buttons())
		lx, ly := k.host.Analog()
		w.u32(uint32(lx) | uint32(ly)<<8)
		// Rsrv[4..6] and the remaining pad words of the 24 byte
		// structure: zero.
		for j := 0; j < 3; j++ {
			w.u32(0)
		}
	}
	k.ret(th, uint32(count))
	return actNone
}

func hleCtrlSetSamplingMode(k *Kernel, th *Thread) syscallAction {
	k.samplingCtl = k.arg(th, 0)
	k.ret(th, 0)
	return actNone
}

// --- sceGe ---

// geList tracks one enqueued display list for the sync calls.
type geList struct {
	id        int32
	state     ge.ListState
	stallAddr uint32
}

// geListStates are the documented PspGeListStates values.
const (
	geListCompleted = 0
	geListQueued    = 1
	geListDrawing   = 2
	geListStalled   = 3
	geListPaused    = 4
)

func geListStateCode(s ge.ListState) int32 {
	switch s {
	case ge.StateReady:
		return geListCompleted
	case ge.StateStalled:
		return geListStalled
	case ge.StateDrawing:
		return geListDrawing
	case ge.StateCompleted:
		return geListCompleted
	}
	return geListCompleted
}

func hleGeEdramGetAddr(k *Kernel, th *Thread) syscallAction {
	// Returns the GE EDRAM base as a GE local address (0x04000000
	// folded to the GE's own address space origin): the
	// documented form is the raw EDRAM pointer guests translate
	// with sceGeEdramGetAddr's contract, 0x04000000.
	k.ret(th, 0x04000000)
	return actNone
}

func hleGeListEnQueue(k *Kernel, th *Thread) syscallAction {
	listAddr := k.arg(th, 0)
	stallAddr := k.arg(th, 1)
	// arg 2 is a callback id, arg 3 a callback argument pointer;
	// callbacks are not dispatched in this model (documented).
	id := k.nextGeID
	k.nextGeID++
	st := k.host.SubmitDisplayList(listAddr)
	k.geLists[id] = &geList{id: id, state: st, stallAddr: stallAddr}
	k.ret(th, uint32(id))
	return actNone
}

func hleGeListSync(k *Kernel, th *Thread) syscallAction {
	id := int32(k.arg(th, 0))
	syncType := k.arg(th, 1)
	l, ok := k.geLists[id]
	if !ok {
		k.ret(th, ErrError)
		return actNone
	}
	if syncType == 0 && l.state == ge.StateStalled {
		// PSP_GE_LIST_SYNC: wait for the list, which in the
		// synchronous model means run it past its stall point.
		l.state = k.host.ContinueDisplayList()
	}
	k.ret(th, uint32(geListStateCode(l.state)))
	return actNone
}

func hleGeDrawSync(k *Kernel, th *Thread) syscallAction {
	syncType := k.arg(th, 0)
	if syncType == 0 {
		for _, l := range k.geLists {
			if l.state == ge.StateStalled {
				l.state = k.host.ContinueDisplayList()
			}
		}
	}
	k.ret(th, 0)
	return actNone
}

func hleGeBreak(k *Kernel, th *Thread) syscallAction {
	// Mode 1 cancels pending lists; the synchronous model has
	// nothing in flight beyond stalls, which are dropped.
	for _, l := range k.geLists {
		l.state = ge.StateCompleted
	}
	k.ret(th, 0)
	return actNone
}

func hleGeContinue(k *Kernel, th *Thread) syscallAction {
	for _, l := range k.geLists {
		if l.state == ge.StateStalled {
			l.state = k.host.ContinueDisplayList()
		}
	}
	k.ret(th, 0)
	return actNone
}
