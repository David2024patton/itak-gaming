package kernel

// The HLE function registry, keyed by (module, NID). NID values
// come from the published pspsdk NID tables. Most user library
// NIDs are the low 32 bits of the SHA-1 of the function name (the
// documented NID generation scheme); this table was cross-checked
// against that computation and against the import tables of the
// open pspautotests corpus. A handful of firmware era NIDs
// predate the scheme (noted where used).

var registry = buildRegistry()

func buildRegistry() map[modNid]syscallEntry {
	m := map[modNid]syscallEntry{}
	reg := func(module string, nid uint32, name string, fn syscallFunc) {
		m[modNid{module, nid}] = syscallEntry{module: module, nid: nid, name: name, fn: fn}
	}

	// Lightweight mutexes: registered under both import names
	// (pspsdk stubs import them from ThreadManForUser; the
	// functions also live in Kernel_Library).
	for _, mod := range []string{"Kernel_Library", "ThreadManForUser"} {
		reg(mod, 0x19CFF145, "sceKernelCreateLwMutex", hleCreateLwMutex)
		reg(mod, 0x60107536, "sceKernelDeleteLwMutex", hleDeleteLwMutex)
		reg(mod, 0xBEA46419, "sceKernelLockLwMutex", hleLockLwMutex)
		reg(mod, 0x1FC64E09, "sceKernelLockLwMutexCB", hleLockLwMutex)
		reg(mod, 0x15B6446B, "sceKernelUnlockLwMutex", hleUnlockLwMutex)
		reg(mod, 0xDC692EE3, "sceKernelTryLockLwMutex", hleTryLockLwMutex)
	}
	reg("sceUtility", 0xA5DA2406, "sceUtilityGetSystemParamInt", hleUtilityGetSystemParamInt)

	// ThreadManForUser
	const tm = "ThreadManForUser"
	reg(tm, 0x446D8DE6, "sceKernelCreateThread", hleCreateThread)
	reg(tm, 0xF475845D, "sceKernelStartThread", hleStartThread)
	reg(tm, 0xAA73C935, "sceKernelExitThread", hleExitThread)
	reg(tm, 0x809CE29B, "sceKernelExitDeleteThread", hleExitDeleteThread)
	reg(tm, 0x9FA03CD3, "sceKernelDeleteThread", hleDeleteThread)
	reg(tm, 0x616403BA, "sceKernelTerminateThread", hleTerminateThread)
	reg(tm, 0x383F7BCC, "sceKernelTerminateDeleteThread", hleTerminateDeleteThread)
	reg(tm, 0x278C0DF5, "sceKernelWaitThreadEnd", hleWaitThreadEnd)
	reg(tm, 0x840E8133, "sceKernelWaitThreadEndCB", hleWaitThreadEnd)
	reg(tm, 0xCEADEB47, "sceKernelDelayThread", hleDelayThread)
	reg(tm, 0x68DA9E36, "sceKernelDelayThreadCB", hleDelayThread)
	reg(tm, 0xBD123D9E, "sceKernelDelaySysClockThread", hleDelaySysClockThread)
	reg(tm, 0x9ACE131E, "sceKernelSleepThread", hleSleepThread)
	reg(tm, 0x82826F70, "sceKernelSleepThreadCB", hleSleepThread)
	reg(tm, 0x912354A7, "sceKernelRotateThreadReadyQueue", hleRotateReadyQueue)
	reg(tm, 0x293B45B8, "sceKernelGetThreadId", hleGetThreadId)
	reg(tm, 0x94AA61EE, "sceKernelGetThreadCurrentPriority", hleGetCurrentPriority)
	reg(tm, 0x17C1684E, "sceKernelReferThreadStatus", hleReferThreadStatus)
	reg(tm, 0xE81CAF8F, "sceKernelCreateCallback", hleCreateCallback)
	reg(tm, 0xEDBA5844, "sceKernelDeleteCallback", hleDeleteCallback)
	reg(tm, 0xD6DA4BA1, "sceKernelCreateSema", hleCreateSema)
	reg(tm, 0x4E3A1105, "sceKernelWaitSema", hleWaitSema)
	reg(tm, 0x58B1F937, "sceKernelPollSema", hlePollSema)
	reg(tm, 0x3F53E640, "sceKernelSignalSema", hleSignalSema)
	reg(tm, 0x28B6489C, "sceKernelDeleteSema", hleDeleteSema)
	reg(tm, 0xBC6FEBC5, "sceKernelReferSemaStatus", hleReferSemaStatus)
	reg(tm, 0xB7D098C6, "sceKernelCreateMutex", hleCreateMutex)
	reg(tm, 0xB011B11F, "sceKernelLockMutex", hleLockMutex)
	reg(tm, 0x0DDCD2C9, "sceKernelTryLockMutex", hleTryLockMutex)
	reg(tm, 0x6B30100F, "sceKernelUnlockMutex", hleUnlockMutex)
	reg(tm, 0xF8170FBE, "sceKernelDeleteMutex", hleDeleteMutex)
	reg(tm, 0xA9C2CB9A, "sceKernelReferMutexStatus", hleReferMutexStatus)
	reg(tm, 0x55C20A00, "sceKernelCreateEventFlag", hleCreateEventFlag)
	reg(tm, 0x402FCF22, "sceKernelWaitEventFlag", hleWaitEventFlag)
	reg(tm, 0x30FD48F0, "sceKernelPollEventFlag", hlePollEventFlag)
	reg(tm, 0x1FB15A32, "sceKernelSetEventFlag", hleSetEventFlag)
	reg(tm, 0x812346E4, "sceKernelClearEventFlag", hleClearEventFlag)
	reg(tm, 0xEF9E4C70, "sceKernelDeleteEventFlag", hleDeleteEventFlag)
	reg(tm, 0xA66B0120, "sceKernelReferEventFlagStatus", hleReferEventFlagStatus)
	reg(tm, 0x82BC5777, "sceKernelGetSystemTimeWide", hleGetSystemTimeWide)
	reg(tm, 0x369ED59D, "sceKernelGetSystemTimeLow", hleGetSystemTimeLow)
	reg(tm, 0xDB738F35, "sceKernelGetSystemTime", hleGetSystemTime)

	// SysMemUserForUser
	const sm = "SysMemUserForUser"
	reg(sm, 0x237DBD4F, "sceKernelAllocPartitionMemory", hleAllocPartitionMemory)
	reg(sm, 0xB6D61D02, "sceKernelFreePartitionMemory", hleFreePartitionMemory)
	reg(sm, 0x9D9A5BA1, "sceKernelGetBlockHeadAddr", hleGetBlockHeadAddr)
	reg(sm, 0xF919F628, "sceKernelTotalFreeMemSize", hleTotalFreeMemSize)
	reg(sm, 0xA291F107, "sceKernelMaxFreeMemSize", hleMaxFreeMemSize)
	reg(sm, 0xACBD88CA, "sceKernelTotalMemSize", hleTotalMemSize)
	reg(sm, 0x13A5ABEF, "sceKernelPrintf", hlePrintf)
	reg(sm, 0x3FC9AE6A, "sceKernelDevkitVersion", hleDevkitVersion)
	reg(sm, 0xFC114573, "sceKernelGetCompiledSdkVersion", hleGetCompiledSdkVersion)
	for _, nid := range []uint32{0x7591C7DB, 0x342061E5, 0x315AD3A0, 0xEBD5C3E6,
		0x057E7380, 0x91DE343C, 0x7893F79A, 0x35669D4C, 0x1B4217BC, 0x358CA1BB} {
		reg(sm, nid, "sceKernelSetCompiledSdkVersion*", hleSetCompiledSdkVersion)
	}
	reg(sm, 0xF77D77CB, "sceKernelSetCompilerVersion", hleSetCompiledSdkVersion)

	// IoFileMgrForUser (handlers in hle_io.go)
	const io = "IoFileMgrForUser"
	reg(io, 0x109F50BC, "sceIoOpen", hleIoOpen)
	reg(io, 0x810C4BC3, "sceIoClose", hleIoClose)
	reg(io, 0x6A638D83, "sceIoRead", hleIoRead)
	reg(io, 0x42EC03AC, "sceIoWrite", hleIoWrite)
	reg(io, 0x27EB27B8, "sceIoLseek", hleIoLseek)
	reg(io, 0x68963324, "sceIoLseek32", hleIoLseek32)
	reg(io, 0xACE946E8, "sceIoGetstat", hleIoGetstat)
	reg(io, 0x54F5FB11, "sceIoDevctl", hleIoDevctl)
	reg(io, 0xB29DDF9C, "sceIoDopen", hleIoDopen)
	reg(io, 0xE3EB004C, "sceIoDread", hleIoDread)
	reg(io, 0xEB092469, "sceIoDclose", hleIoDclose)

	// sceCtrl (handlers in hle_ge.go)
	reg("sceCtrl", 0x1F803938, "sceCtrlReadBufferPositive", hleCtrlRead)
	reg("sceCtrl", 0x3A622550, "sceCtrlPeekBufferPositive", hleCtrlRead)
	reg("sceCtrl", 0x1F4011E6, "sceCtrlSetSamplingMode", hleCtrlSetSamplingMode)

	// sceDisplay (handlers in hle_ge.go)
	reg("sceDisplay", 0x0E20F177, "sceDisplaySetMode", hleDisplaySetMode)
	reg("sceDisplay", 0x289D82FE, "sceDisplaySetFrameBuf", hleDisplaySetFrameBuf)
	reg("sceDisplay", 0xEEDA2E54, "sceDisplayWaitVblankStart", hleDisplayWaitVblankStart) // legacy NID, corpus attested
	reg("sceDisplay", 0x984C27E7, "sceDisplayWaitVblankStart", hleDisplayWaitVblankStart) // SHA-1 of the name
	reg("sceDisplay", 0x9C6EAAD7, "sceDisplayGetVcount", hleDisplayGetVcount)
	reg("sceDisplay", 0x4D4E10EC, "sceDisplayIsVblank", hleDisplayIsVblank)

	// sceGe_user (handlers in hle_ge.go)
	reg("sceGe_user", 0xE47E40E4, "sceGeEdramGetAddr", hleGeEdramGetAddr)
	reg("sceGe_user", 0xAB49E76A, "sceGeListEnQueue", hleGeListEnQueue)
	reg("sceGe_user", 0x03444EB4, "sceGeListSync", hleGeListSync)
	reg("sceGe_user", 0xB287BD61, "sceGeDrawSync", hleGeDrawSync)
	reg("sceGe_user", 0xB448EC0D, "sceGeBreak", hleGeBreak)
	reg("sceGe_user", 0x4C06E472, "sceGeContinue", hleGeContinue)

	// scePower
	reg("scePower", 0xFDB5BFE9, "scePowerGetCpuClockFrequencyInt", hlePowerGetCpuClock)
	reg("scePower", 0xEFD3C963, "scePowerTick", hlePowerTick)

	// sceRtc
	reg("sceRtc", 0x3F7AD767, "sceRtcGetCurrentTick", hleRtcGetCurrentTick)

	// UtilsForUser and UtilsForKernel: libc time and cache ops.
	for _, mod := range []string{"UtilsForUser", "UtilsForKernel"} {
		reg(mod, 0x27CC57F0, "sceKernelLibcTime", hleLibcTime)
		reg(mod, 0x91E4F6A7, "sceKernelLibcClock", hleLibcClock)
		reg(mod, 0x71EC4271, "sceKernelLibcGettimeofday", hleLibcGettimeofday)
		reg(mod, 0x82BC5777, "sceKernelGetSystemTimeWide", hleGetSystemTimeWide)
		reg(mod, 0x369ED59D, "sceKernelGetSystemTimeLow", hleGetSystemTimeLow)
		reg(mod, 0x920F104A, "sceKernelIcacheInvalidateAll", hleNoopSuccess)
		reg(mod, 0xC2DF770E, "sceKernelIcacheInvalidateRange", hleNoopSuccess) // legacy NID, corpus attested
		reg(mod, 0xB435DEC5, "sceKernelDcacheWritebackInvalidateAll", hleNoopSuccess)
	}

	// LoadExecForUser
	reg("LoadExecForUser", 0x05572A5F, "sceKernelExitGame", hleExitGame)
	reg("LoadExecForUser", 0x4AC57943, "sceKernelRegisterExitCallback", hleRegisterExitCallback)
	// ModuleMgrForUser: unloading the only module ends the program
	// in this single module kernel (documented approximation).
	reg("ModuleMgrForUser", 0xD675EBB8, "sceKernelSelfStopUnloadModule", hleExitGame)

	// StdioForUser. The Stdin/Stdout/Stderr getters return the
	// fixed console fds; pspsdk libc asks for them while setting
	// up its stdio table, and the whole corpus depends on sane
	// answers here. These three NIDs are legacy values from the
	// published PPSSPP table (they predate the SHA-1 scheme).
	reg("StdioForUser", 0x7D58F994, "sceKernelStdoutWrite", hleStdoutWrite)
	reg("StdioForUser", 0x172D316E, "sceKernelStdin", hleStdinFd)
	reg("StdioForUser", 0xA6BAB2E9, "sceKernelStdout", hleStdoutFd)
	reg("StdioForUser", 0xF78BA90A, "sceKernelStderr", hleStderrFd)

	return m
}

func hleGetCurrentPriority(k *Kernel, th *Thread) syscallAction {
	k.ret(th, uint32(th.priority))
	return actNone
}

// write64 stores a 64 bit value into guest memory.
func (k *Kernel) write64(addr uint32, v uint64) {
	k.mem.Write32(addr, uint32(v))
	k.mem.Write32(addr+4, uint32(v>>32))
}

// SysClock: the documented kernel time base is a microsecond
// count (sceKernelSysClock2USec divides by 1,000,000), derived
// here from the CPU cycle counter at 222 MHz.

func hleGetSystemTimeWide(k *Kernel, th *Thread) syscallAction {
	k.ret64(th, k.SysClockMicros())
	return actNone
}

func hleGetSystemTimeLow(k *Kernel, th *Thread) syscallAction {
	k.ret(th, uint32(k.SysClockMicros()))
	return actNone
}

func hleGetSystemTime(k *Kernel, th *Thread) syscallAction {
	ptr := k.arg(th, 0)
	if ptr == 0 {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	k.write64(ptr, k.SysClockMicros())
	k.ret(th, 0)
	return actNone
}

// rtcTickBase is the microsecond tick of 1970-01-01 in the RTC's
// year-1 based reckoning. The RTC starts at the Unix epoch at
// boot in this model.
const rtcTickBase = uint64(62135596800000000)

func hleRtcGetCurrentTick(k *Kernel, th *Thread) syscallAction {
	ptr := k.arg(th, 0)
	if ptr == 0 {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	k.write64(ptr, rtcTickBase+k.SysClockMicros())
	k.ret(th, 0)
	return actNone
}

func hleLibcTime(k *Kernel, th *Thread) syscallAction {
	secs := uint32(k.SysClockMicros() / 1000000)
	if ptr := k.arg(th, 0); ptr != 0 {
		k.write32(ptr, secs)
	}
	k.ret(th, secs)
	return actNone
}

func hleLibcClock(k *Kernel, th *Thread) syscallAction {
	k.ret(th, uint32(k.SysClockMicros()))
	return actNone
}

func hleLibcGettimeofday(k *Kernel, th *Thread) syscallAction {
	us := k.SysClockMicros()
	if ptr := k.arg(th, 0); ptr != 0 {
		k.write32(ptr, uint32(us/1000000))
		k.write32(ptr+4, uint32(us%1000000))
	}
	k.ret(th, 0)
	return actNone
}

func hleNoopSuccess(k *Kernel, th *Thread) syscallAction {
	k.ret(th, 0)
	return actNone
}

func hlePowerGetCpuClock(k *Kernel, th *Thread) syscallAction {
	k.ret(th, ClockHz/1_000_000)
	return actNone
}

func hlePowerTick(k *Kernel, th *Thread) syscallAction {
	k.ret(th, 0)
	return actNone
}

// --- SysMem handlers ---

// devkitVersion is this kernel's model firmware version (6.60).
const devkitVersion = 0x06060010

func hleDevkitVersion(k *Kernel, th *Thread) syscallAction {
	k.ret(th, devkitVersion)
	return actNone
}

func hleSetCompiledSdkVersion(k *Kernel, th *Thread) syscallAction {
	k.sdkVersion = k.arg(th, 0)
	k.ret(th, 0)
	return actNone
}

func hleGetCompiledSdkVersion(k *Kernel, th *Thread) syscallAction {
	k.ret(th, k.sdkVersion)
	return actNone
}

func hleAllocPartitionMemory(k *Kernel, th *Thread) syscallAction {
	partitionID := k.arg(th, 0)
	name := k.readCString(k.arg(th, 1), 32)
	typ := k.arg(th, 2)
	size := k.arg(th, 3)
	addr := k.stackArg(th, 4) // the fifth argument rides the stack in o32
	if partitionID != UserPartitionID {
		k.ret(th, ErrIllegalPartition)
		return actNone
	}
	if typ > allocHighAligned {
		k.ret(th, ErrIllegalMemBlockType)
		return actNone
	}
	// For the aligned types the fifth argument is the alignment,
	// for the address type the exact address: validate the shape
	// the partition allocator itself enforces.
	if (typ == allocLowAligned || typ == allocHighAligned) && (addr == 0 || addr&(addr-1) != 0) {
		k.ret(th, ErrIllegalAlignment)
		return actNone
	}
	uid, _, err := k.part.allocInternal(k, name, int(typ), size, addr)
	if err != nil {
		k.ret(th, ErrMemBlockAllocFailed)
		return actNone
	}
	k.ret(th, uid)
	return actNone
}

func hleFreePartitionMemory(k *Kernel, th *Thread) syscallAction {
	uid := k.arg(th, 0)
	if !k.part.free(uid) {
		k.ret(th, ErrUnknownUID)
		return actNone
	}
	k.removeObject(uid)
	k.ret(th, 0)
	return actNone
}

func hleGetBlockHeadAddr(k *Kernel, th *Thread) syscallAction {
	b := k.part.blockByUID(k.arg(th, 0))
	if b == nil {
		k.ret(th, ErrUnknownUID)
		return actNone
	}
	k.ret(th, b.addr)
	return actNone
}

func hleTotalFreeMemSize(k *Kernel, th *Thread) syscallAction {
	k.ret(th, k.part.totalFree())
	return actNone
}

func hleMaxFreeMemSize(k *Kernel, th *Thread) syscallAction {
	k.ret(th, k.part.maxFree())
	return actNone
}

func hleTotalMemSize(k *Kernel, th *Thread) syscallAction {
	k.ret(th, 32<<20) // PSP main RAM size reported by the firmware call
	return actNone
}

func hlePrintf(k *Kernel, th *Thread) syscallAction {
	s := k.kernelPrintf(th, k.arg(th, 0))
	k.writeConsole([]byte(s))
	k.ret(th, uint32(len(s)))
	return actNone
}

// --- LoadExec / exit ---

func hleExitGame(k *Kernel, th *Thread) syscallAction {
	k.exited = true
	k.exitCode = int32(k.arg(th, 0))
	if k.current == th {
		k.current = nil
	}
	return actBlock
}

func hleRegisterExitCallback(k *Kernel, th *Thread) syscallAction {
	uid := k.arg(th, 0)
	if _, ok := k.lookup(uid); !ok {
		k.ret(th, ErrUnknownCBID)
		return actNone
	}
	k.exitCallbackUID = uid
	k.ret(th, 0)
	return actNone
}

func hleStdinFd(k *Kernel, th *Thread) syscallAction  { k.ret(th, 0); return actNone }
func hleStdoutFd(k *Kernel, th *Thread) syscallAction { k.ret(th, 1); return actNone }
func hleStderrFd(k *Kernel, th *Thread) syscallAction { k.ret(th, 2); return actNone }

func hleStdoutWrite(k *Kernel, th *Thread) syscallAction {
	ptr := k.arg(th, 0)
	length := int(k.arg(th, 1))
	if ptr == 0 {
		k.ret(th, ErrIllegalArgument)
		return actNone
	}
	if length <= 0 || length > 1<<20 {
		s := k.readCString(ptr, 8192)
		k.writeConsole([]byte(s))
		k.ret(th, uint32(len(s)))
		return actNone
	}
	k.writeConsole(k.readBytes(ptr, length))
	k.ret(th, uint32(length))
	return actNone
}

// hleUtilityGetSystemParamInt answers sceUtility system parameter
// queries with the model's values: every integer parameter reads
// 0 (language Japanese, timezone offset 0, 24h clock unset, and
// so on). Runtime startup code asks during init; nothing in the
// test corpus branches on the values.
func hleUtilityGetSystemParamInt(k *Kernel, th *Thread) syscallAction {
	if ptr := k.arg(th, 1); ptr != 0 {
		k.write32(ptr, 0)
	}
	k.ret(th, 0)
	return actNone
}
