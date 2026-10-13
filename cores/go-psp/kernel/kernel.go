// Package kernel is the PSP-3 HLE kernel: a deterministic subset
// of the PSP system software, implemented from the published
// pspsdk headers and the uofw documentation. It loads ELF and PRX
// modules through the load package, assigns a syscall number to
// every imported (module, NID) pair and patches it into the import
// stub's delay slot word, and dispatches SYSCALL instructions on
// the shared Allegrex CPU through the CPU's SyscallHook.
//
// Threading is cooperative with priority preemption at defined
// scheduling points: a thread runs until it blocks in an HLE call,
// exits, yields, a higher priority thread is unblocked by one of
// its calls, or it exhausts a 10,000 instruction timeslice check
// and an equal or higher priority thread is ready. One Allegrex
// CPU is shared by all threads; a thread's full CPU state (the
// PSP-2 serialized form, 2,864 bytes including the VFPU) is swapped
// in and out on every switch.
//
// Object ids (SceUID) are sequential with a type tag: the low byte
// is the object kind, the remaining bytes a per-kernel sequence
// number starting at 1, so the first object created is 0x101,
// 0x201, and so on in creation order across all kinds.
package kernel

import (
	"fmt"
	"sort"
	"strings"

	"github.com/davidpatton/itak-gaming/cores/go-psp/chips/allegrex"
	"github.com/davidpatton/itak-gaming/cores/go-psp/ge"
	"github.com/davidpatton/itak-gaming/cores/go-psp/load"
	"github.com/davidpatton/itak-gaming/cores/go-psp/umd"
)

// ClockHz is the Allegrex clock the kernel times against: the
// documented default 222 MHz. One microsecond is 222 cycles.
const ClockHz = 222_000_000

// CyclesPerMicro is the number of CPU cycles in one microsecond.
const CyclesPerMicro = ClockHz / 1_000_000

// Kernel error codes returned in $v0, from the published PSP
// kernel error table (pspsdk pspkernelerror.h values).
const (
	ErrOK                   = 0
	ErrError                = 0x80020001 // SCE_KERNEL_ERROR_ERROR
	ErrIllegalArgument      = 0x800200D2 // SCE_KERNEL_ERROR_ILLEGAL_ARGUMENT
	ErrMemBlockAllocFailed  = 0x800200D9 // SCE_KERNEL_ERROR_MEMBLOCK_ALLOC_FAILED
	ErrUnknownUID           = 0x800200CB // SCE_KERNEL_ERROR_UNKNOWN_UID
	ErrNoFile               = 0x8002012F // SCE_KERNEL_ERROR_NOFILE
	ErrIllegalAttr          = 0x80020191 // SCE_KERNEL_ERROR_ILLEGAL_ATTR
	ErrIllegalEntry         = 0x80020192 // SCE_KERNEL_ERROR_ILLEGAL_ENTRY
	ErrIllegalPriority      = 0x80020193 // SCE_KERNEL_ERROR_ILLEGAL_PRIORITY
	ErrIllegalStackSize     = 0x80020194 // SCE_KERNEL_ERROR_ILLEGAL_STACK_SIZE
	ErrUnknownThid          = 0x80020198 // SCE_KERNEL_ERROR_UNKNOWN_THID
	ErrUnknownSemid         = 0x80020199 // SCE_KERNEL_ERROR_UNKNOWN_SEMID
	ErrUnknownEvfid         = 0x8002019A // SCE_KERNEL_ERROR_UNKNOWN_EVFID
	ErrUnknownCBID          = 0x800201A1 // SCE_KERNEL_ERROR_UNKNOWN_CBID
	ErrDormant              = 0x800201A2 // SCE_KERNEL_ERROR_DORMANT
	ErrNotDormant           = 0x800201A4 // SCE_KERNEL_ERROR_NOT_DORMANT
	ErrWaitTimeout          = 0x800201A8 // SCE_KERNEL_ERROR_WAIT_TIMEOUT
	ErrWaitCancel           = 0x800201A9 // SCE_KERNEL_ERROR_WAIT_CANCEL
	ErrReleaseWait          = 0x800201AA // SCE_KERNEL_ERROR_RELEASE_WAIT
	ErrThreadTerminated     = 0x800201AC // SCE_KERNEL_ERROR_THREAD_TERMINATED
	ErrSemaZero             = 0x800201AD // SCE_KERNEL_ERROR_SEMA_ZERO
	ErrSemaOvf              = 0x800201AE // SCE_KERNEL_ERROR_SEMA_OVF
	ErrEvfCond              = 0x800201AF // SCE_KERNEL_ERROR_EVF_COND
	ErrEvfIlpat             = 0x800201B1 // SCE_KERNEL_ERROR_EVF_ILPAT
	ErrWaitDelete           = 0x800201B5 // SCE_KERNEL_ERROR_WAIT_DELETE
	ErrMutexNotFound        = 0x800201C3 // SCE_KERNEL_ERROR_MUTEX_NOT_FOUND
	ErrMutexLocked          = 0x800201C4 // SCE_KERNEL_ERROR_MUTEX_LOCKED
	ErrMutexUnlocked        = 0x800201C5 // SCE_KERNEL_ERROR_MUTEX_UNLOCKED
	ErrMutexLockOverflow    = 0x800201C6 // SCE_KERNEL_ERROR_MUTEX_LOCK_OVERFLOW
	ErrMutexUnlockUnderflow = 0x800201C7 // SCE_KERNEL_ERROR_MUTEX_UNLOCK_UNDERFLOW
	ErrMutexRecursiveDenied = 0x800201C8 // SCE_KERNEL_ERROR_MUTEX_RECURSIVE_NOT_ALLOWED
	ErrIllegalCount         = 0x800201BD // SCE_KERNEL_ERROR_ILLEGAL_COUNT
	ErrLwMutexNotFound      = 0x800201CA // SCE_KERNEL_ERROR_LWMUTEX_NOT_FOUND
	ErrLwMutexLocked        = 0x800201CB // SCE_KERNEL_ERROR_LWMUTEX_LOCKED
	ErrLwMutexUnlocked      = 0x800201CC // SCE_KERNEL_ERROR_LWMUTEX_UNLOCKED
	ErrLwMutexLockOverflow  = 0x800201CD // SCE_KERNEL_ERROR_LWMUTEX_LOCK_OVERFLOW
	ErrLwMutexUnlockUnder   = 0x800201CE // SCE_KERNEL_ERROR_LWMUTEX_UNLOCK_UNDERFLOW
	ErrLwMutexRecursiveDeny = 0x800201CF // SCE_KERNEL_ERROR_LWMUTEX_RECURSIVE_NOT_ALLOWED
	ErrErrnoFileNotFound    = 0x80010002 // SCE_KERNEL_ERROR_ERRNO_FILE_NOT_FOUND
	ErrErrnoBadFd           = 0x80010009 // SCE_KERNEL_ERROR_ERRNO_INVALID_FILE_DESCRIPTOR
	ErrErrnoInvalidArgument = 0x80010016 // SCE_KERNEL_ERROR_ERRNO_INVALID_ARGUMENT
	ErrIllegalPartition     = 0x800200D6 // SCE_KERNEL_ERROR_ILLEGAL_PARTITION
	ErrIllegalMemBlockType  = 0x800200D8 // SCE_KERNEL_ERROR_ILLEGAL_MEMBLOCKTYPE
	ErrIllegalAlignment     = 0x800200E4 // SCE_KERNEL_ERROR_ILLEGAL_ALIGNMENT_SIZE
	ErrIllegalMemSize       = 0x800201B7 // SCE_KERNEL_ERROR_ILLEGAL_MEMSIZE
	ErrModuleMgrUnsupported = 0x80020148 // SCE_KERNEL_ERROR_UNSUPPORTED_PRX_TYPE
	ErrUnknownSyscall       = ErrError   // model value, see README
)

// Host is what the kernel needs from the machine that owns it. The
// system package implements it.
type Host interface {
	// Memory is the guest address space (the bus).
	Memory() load.Memory
	// CPU is the single shared Allegrex the threads time-share.
	SharedCPU() *allegrex.CPU
	// SubmitDisplayList runs a GE display list synchronously.
	SubmitDisplayList(addr uint32) ge.ListState
	// ContinueDisplayList resumes a SIGNAL-stalled list.
	ContinueDisplayList() ge.ListState
	// PresentFrameBuffer makes the GE present the given buffer.
	PresentFrameBuffer(addr uint32, width, format int)
	// Buttons and Analog expose the current controller state.
	Buttons() uint32
	Analog() (byte, byte)
	// ISO returns the mounted UMD image, or nil.
	ISO() *umd.ISO
	// MemoryStickFiles is the in-memory ms0: store (may be nil).
	MemoryStickFiles() map[string][]byte
}

// UnknownCall records one distinct unimplemented (module, NID)
// pair the guest called, with how many times it fired.
type UnknownCall struct {
	Module string
	NID    uint32
	Count  int
}

// Object kinds, stored in the low byte of every SceUID.
const (
	kindThread   = 0x01
	kindSema     = 0x02
	kindMutex    = 0x03
	kindEventF   = 0x04
	kindMemBlock = 0x05
	kindCallback = 0x06
	kindLwMutex  = 0x07
)

type object interface {
	objKind() uint32
}

// syscallAction tells the scheduler what an HLE handler did to the
// calling thread.
type syscallAction int

const (
	actNone    syscallAction = iota // keep running
	actBlock                        // the handler parked the thread in a wait
	actResched                      // re-run the scheduler pick after this call
)

type syscallFunc func(k *Kernel, th *Thread) syscallAction

type syscallEntry struct {
	module string
	nid    uint32
	name   string
	fn     syscallFunc
}

type modNid struct {
	module string
	nid    uint32
}

// Kernel is the HLE kernel state. It is created once per loaded
// module (one module per boot in this slice).
type Kernel struct {
	host Host
	mem  load.Memory
	cpu  *allegrex.CPU

	uidSeq  uint32
	objects map[uint32]object
	threads map[uint32]*Thread
	order   []*Thread // creation order, for deterministic scans
	current *Thread
	seq     uint64 // ready/wait ordering counter

	part *partition

	cycles   uint64 // sysclock in CPU cycles; mirrors the machine cycle count
	vcount   uint32
	exited   bool
	exitCode int32

	console []byte
	unknown map[modNid]*UnknownCall

	codes    map[uint32]*syscallEntry // assigned syscall number -> handler
	assigned map[modNid]uint32        // (module, NID) -> assigned syscall number
	nextCode uint32

	moduleBlockUID uint32
	moduleBase     uint32
	moduleSize     uint32
	bootPath       string

	exitCallbackUID uint32
	sdkVersion      uint32 // set by the sceKernelSetCompiledSdkVersion family

	dispMode    [3]uint32 // width, height, type from sceDisplaySetMode
	dispSet     bool
	samplingCtl uint32 // sceCtrl sampling mode, stored
	inVblank    bool   // true only while NotifyVblank is waking waiters

	geLists  map[int32]*geList
	nextGeID int32

	fds    map[int]*fileDesc
	nextFD int

	resched bool // an HLE call asked for a scheduler re-pick
}

// New creates a kernel bound to host and installs the syscall hook
// on the host CPU.
func New(host Host) *Kernel {
	k := &Kernel{
		host:     host,
		mem:      host.Memory(),
		cpu:      host.SharedCPU(),
		objects:  make(map[uint32]object),
		threads:  make(map[uint32]*Thread),
		part:     newPartition(UserPartitionBase, UserPartitionSize),
		unknown:  make(map[modNid]*UnknownCall),
		codes:    make(map[uint32]*syscallEntry),
		assigned: make(map[modNid]uint32),
		nextCode: 0x100,
		geLists:  make(map[int32]*geList),
		fds:      make(map[int]*fileDesc),
		nextFD:   3,
		bootPath: "umd0:",
	}
	k.cpu.SyscallHook = k.handleSyscall
	return k
}

// NewUID allocates the next sequential UID with the kind tag.
func (k *Kernel) NewUID(kind uint32) uint32 {
	k.uidSeq++
	return k.uidSeq<<8 | kind
}

func (k *Kernel) registerObject(uid uint32, o object) { k.objects[uid] = o }

func (k *Kernel) lookup(uid uint32) (object, bool) {
	o, ok := k.objects[uid]
	return o, ok
}

func (k *Kernel) removeObject(uid uint32) { delete(k.objects, uid) }

// Cycles returns the kernel sysclock in CPU cycles.
func (k *Kernel) Cycles() uint64 { return k.cycles }

// SysClockMicros returns the sysclock in microseconds: cycles at
// 222 cycles per microsecond, truncated.
func (k *Kernel) SysClockMicros() uint64 { return k.cycles / CyclesPerMicro }

// Vcount returns the number of vblanks notified so far.
func (k *Kernel) Vcount() uint32 { return k.vcount }

// Exited reports whether the guest asked to exit (sceKernelExitGame)
// or every thread died.
func (k *Kernel) Exited() bool { return k.exited }

// ExitCode returns the status from sceKernelExitGame.
func (k *Kernel) ExitCode() int32 { return k.exitCode }

// ConsoleText returns everything written to the kernel console so
// far (sceKernelPrintf, sceIoWrite to fds 1 and 2, StdoutWrite).
func (k *Kernel) ConsoleText() string { return string(k.console) }

func (k *Kernel) writeConsole(b []byte) { k.console = append(k.console, b...) }

// UnknownCalls returns the unimplemented (module, NID) pairs the
// guest has called, sorted for determinism.
func (k *Kernel) UnknownCalls() []UnknownCall {
	out := make([]UnknownCall, 0, len(k.unknown))
	for _, u := range k.unknown {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Module != out[j].Module {
			return out[i].Module < out[j].Module
		}
		return out[i].NID < out[j].NID
	})
	return out
}

// CurrentThread returns the running thread, or nil between dispatches.
func (k *Kernel) CurrentThread() *Thread { return k.current }

// ModuleBase returns the load base of the booted module (0 for a
// fixed ET_EXEC image).
func (k *Kernel) ModuleBase() uint32 { return k.moduleBase }

// UserPartitionFree returns total and largest free bytes in the
// user partition, for tests and diagnostics.
func (k *Kernel) UserPartitionFree() (total, max uint32) {
	return k.part.totalFree(), k.part.maxFree()
}

// LoadModule loads an ELF or PRX image into the user partition,
// patches every import stub with its assigned syscall number, and
// creates (but does not start) the main thread at the module
// entry. Defaults follow the published boot model: priority 0x20
// and a 256 KB stack. SceModuleInfo carries no thread fields (the
// toolchain's crt0 owns those), so the defaults always apply here.
func (k *Kernel) LoadModule(data []byte) error {
	size, relocatable, err := load.Probe(data)
	if err != nil {
		return err
	}
	var base uint32
	if relocatable {
		// The module block starts at the partition base, but the
		// image loads moduleLoadReserve bytes in: the first 16 KB
		// of the user partition stay reserved, which is where the
		// reference loaders place PRX images (the pspautotests
		// .expected files embed absolute addresses that pin the
		// image base at partition base + 0x4000).
		uid, addr, err := k.part.allocInternal(k, "module", allocLow, size+moduleLoadReserve, 0)
		if err != nil {
			return fmt.Errorf("kernel: module does not fit in the user partition: %w", err)
		}
		k.moduleBlockUID = uid
		base = addr + moduleLoadReserve
	} else {
		// A fixed ET_EXEC image sits at its linked addresses,
		// inside the user partition by construction.
		base = 0
		k.moduleBase = 0
	}
	mod, err := load.Load(k.mem, data, base)
	if err != nil {
		return err
	}
	k.moduleBase = mod.Base
	k.moduleSize = mod.Size

	for _, im := range mod.Imports {
		if im.IsVar {
			// Variable imports have no stub to patch; resolving
			// them is outside this slice (none of the gates or
			// the pspautotests cpu set use them).
			continue
		}
		code := k.assignSyscall(im.Module, im.NID)
		// The stub is JR $ra with the syscall in its delay slot:
		// the SYSCALL word goes at StubAddr+4.
		k.mem.Write32(im.StubAddr+4, syscallWord(code))
	}

	main, err := k.createThread("root", mod.Entry, 0x20, 256*1024, 0)
	if err != nil {
		return err
	}
	// Boot arguments: a0 is the length of the boot path string and
	// a1 points at the string itself, placed in the partition.
	pathAddr, err := k.writeBootArgs(main)
	if err != nil {
		return err
	}
	k.startThreadPrepared(main, uint32(len(k.bootPath)), pathAddr, mod.GP)
	return nil
}

// writeBootArgs copies the boot path string into the partition and
// returns its address.
func (k *Kernel) writeBootArgs(main *Thread) (uint32, error) {
	_, addr, err := k.part.allocInternal(k, "bootargs", allocLow, uint32(len(k.bootPath)+1), 0)
	if err != nil {
		return 0, err
	}
	for i := 0; i < len(k.bootPath); i++ {
		k.mem.Write8(addr+uint32(i), k.bootPath[i])
	}
	k.mem.Write8(addr+uint32(len(k.bootPath)), 0)
	return addr, nil
}

// syscallWord encodes a SYSCALL instruction with the given code.
func syscallWord(code uint32) uint32 {
	return (code << 6) | 0x0C
}

// assignSyscall returns the syscall number for a (module, NID)
// pair, allocating a new one on first sight. Known pairs dispatch
// to their HLE handler; unknown pairs dispatch to the logger.
func (k *Kernel) assignSyscall(module string, nid uint32) uint32 {
	key := modNid{module, nid}
	if code, ok := k.assigned[key]; ok {
		return code
	}
	code := k.nextCode
	k.nextCode++
	k.assigned[key] = code
	if e, ok := registry[key]; ok {
		cp := e
		k.codes[code] = &cp
	} else {
		k.codes[code] = nil // unknown: handleSyscall logs it
	}
	return code
}

// handleSyscall is the allegrex SyscallHook. It returns false when
// no thread is running (the bare PSP-1/2 path keeps its exception
// behavior); the kernel installs it only for kernel mode anyway.
func (k *Kernel) handleSyscall(cpu *allegrex.CPU, code uint32) bool {
	cpu.ClearLL() // a SYSCALL breaks any LL reservation (llsc corpus case)
	th := k.current
	if th == nil {
		return false
	}
	e := k.codes[code]
	if e == nil {
		// Either an unassigned raw SYSCALL or an import whose
		// (module, NID) pair is not implemented. Recover the pair
		// from the assignment table when the code is one of ours.
		key, known := k.keyForCode(code)
		mn := UnknownCall{Module: "<raw syscall>", NID: code}
		if known {
			mn = UnknownCall{Module: key.module, NID: key.nid}
		}
		slot := modNid{mn.Module, mn.NID}
		if u, ok := k.unknown[slot]; ok {
			u.Count++
		} else {
			cp := mn
			cp.Count = 1
			k.unknown[slot] = &cp
		}
		cpu.SetReg(2, ErrUnknownSyscall)
		cpu.SetReg(3, 0)
		return true
	}
	action := e.fn(k, th)
	switch action {
	case actBlock:
		th.blocked = true
	case actResched:
		k.resched = true
	}
	return true
}

func (k *Kernel) keyForCode(code uint32) (modNid, bool) {
	for key, c := range k.assigned {
		if c == code {
			return key, true
		}
	}
	return modNid{}, false
}

// arg helpers: HLE functions take their arguments from $a0..$a3
// (regs 4..7) and $t0..$t3 (regs 8..11).

func (k *Kernel) arg(th *Thread, i int) uint32 { return k.cpu.Reg(4 + i) }

// stackArg reads argument i (i >= 4) from the caller's stack slot:
// o32 puts argument slots 4 and up at $sp+16, $sp+20, ...
func (k *Kernel) stackArg(th *Thread, i int) uint32 {
	return k.mem.Read32(k.cpu.Reg(29) + 16 + uint32(i-4)*4)
}
func (k *Kernel) ret(th *Thread, v uint32) { k.cpu.SetReg(2, v) }
func (k *Kernel) ret64(th *Thread, v uint64) {
	k.cpu.SetReg(2, uint32(v))
	k.cpu.SetReg(3, uint32(v>>32))
}

// readCString reads a NUL terminated string from guest memory,
// capped at max bytes.
func (k *Kernel) readCString(addr uint32, max int) string {
	if addr == 0 {
		return ""
	}
	var sb strings.Builder
	for i := 0; i < max; i++ {
		b := k.mem.Read8(addr + uint32(i))
		if b == 0 {
			break
		}
		sb.WriteByte(b)
	}
	return sb.String()
}

// readBytes copies n bytes out of guest memory.
func (k *Kernel) readBytes(addr uint32, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = k.mem.Read8(addr + uint32(i))
	}
	return out
}

// writeBytes copies b into guest memory.
func (k *Kernel) writeBytes(addr uint32, b []byte) {
	for i, v := range b {
		k.mem.Write8(addr+uint32(i), v)
	}
}

// write32 stores one little endian word through the bus, so KSEG
// aliases behave the same as for guest stores.
func (k *Kernel) write32(addr uint32, v uint32) {
	k.mem.Write32(addr, v)
}
