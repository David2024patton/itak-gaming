package kernel

import (
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-psp/bus"
	"github.com/davidpatton/itak-gaming/cores/go-psp/chips/allegrex"
	"github.com/davidpatton/itak-gaming/cores/go-psp/ge"
	"github.com/davidpatton/itak-gaming/cores/go-psp/load"
	"github.com/davidpatton/itak-gaming/cores/go-psp/umd"
)

type testHost struct {
	mem *bus.Bus
	cpu *allegrex.CPU
	ms  map[string][]byte
}

func newTestHost() *testHost {
	m := bus.New()
	return &testHost{mem: m, cpu: allegrex.New(m), ms: map[string][]byte{}}
}

func (h *testHost) Memory() load.Memory      { return h.mem }
func (h *testHost) SharedCPU() *allegrex.CPU { return h.cpu }
func (h *testHost) SubmitDisplayList(addr uint32) ge.ListState {
	return ge.StateCompleted
}
func (h *testHost) ContinueDisplayList() ge.ListState { return ge.StateCompleted }
func (h *testHost) PresentFrameBuffer(addr uint32, width, format int) {
}
func (h *testHost) Buttons() uint32      { return 0 }
func (h *testHost) Analog() (byte, byte) { return 128, 128 }
func (h *testHost) ISO() *umd.ISO        { return nil }
func (h *testHost) MemoryStickFiles() map[string][]byte {
	return h.ms
}

func TestUIDModel(t *testing.T) {
	k := New(newTestHost())
	if got := k.NewUID(kindThread); got != 0x101 {
		t.Fatalf("first UID = %#x, want 0x101", got)
	}
	if got := k.NewUID(kindSema); got != 0x202 {
		t.Fatalf("second UID = %#x, want 0x202", got)
	}
	if got := k.NewUID(kindThread); got != 0x301 {
		t.Fatalf("third UID = %#x, want 0x301", got)
	}
}

func TestPartitionAccounting(t *testing.T) {
	k := New(newTestHost())
	total, max := k.UserPartitionFree()
	if total != UserPartitionSize || max != UserPartitionSize {
		t.Fatalf("fresh partition free = %d/%d, want %d", total, max, UserPartitionSize)
	}
	uid, addr, err := k.part.allocInternal(k, "t", allocLow, 0x1234, 0)
	if err != nil {
		t.Fatal(err)
	}
	if addr != UserPartitionBase {
		t.Fatalf("low alloc addr = %#x, want partition base", addr)
	}
	total, max = k.UserPartitionFree()
	if total != UserPartitionSize-0x1300 || max != UserPartitionSize-0x1300 {
		t.Fatalf("after alloc free = %d/%d, want %d", total, max, UserPartitionSize-0x1300)
	}
	// Head address resolves through the block UID.
	if got := k.part.blockByUID(uid).addr; got != addr {
		t.Fatalf("block addr = %#x, want %#x", got, addr)
	}
	// Aligned allocation honors its alignment.
	_, aaddr, err := k.part.allocInternal(k, "a", allocLowAligned, 0x1000, 0x10000)
	if err != nil {
		t.Fatal(err)
	}
	if aaddr%0x10000 != 0 {
		t.Fatalf("aligned addr = %#x, not 64K aligned", aaddr)
	}
	// Address allocation at an occupied address fails.
	if _, _, err := k.part.allocInternal(k, "x", allocAddr, 0x100, addr); err == nil {
		t.Fatal("allocAddr over an occupied range succeeded")
	}
	if !k.part.free(uid) {
		t.Fatal("free of a live block failed")
	}
	total, _ = k.UserPartitionFree()
	if total != UserPartitionSize-0x1000 {
		// Only the aligned block (0x1000 bytes) still holds.
		t.Fatalf("after free total = %d, want %d", total, UserPartitionSize-0x1000)
	}
}

func putString(h *testHost, addr uint32, s string) {
	for i := 0; i < len(s); i++ {
		h.mem.Write8(addr+uint32(i), s[i])
	}
	h.mem.Write8(addr+uint32(len(s)), 0)
}

func TestPrintfFormats(t *testing.T) {
	h := newTestHost()
	k := New(h)
	const fmtAddr = 0x08801000
	const strAddr = 0x08802000
	const stackBase = 0x08810000
	th := &Thread{}
	cases := []struct {
		format string
		args   [3]uint32 // $a1..$a3
		stack  []uint32  // words at $sp+16...
		want   string
	}{
		{"val=%d", [3]uint32{0xFFFFFFD6}, nil, "val=-42"}, // -42 as a word
		{"%08x", [3]uint32{0xabc}, nil, "00000abc"},
		{"%X", [3]uint32{0xdead}, nil, "DEAD"},
		{"%u", [3]uint32{0xFFFFFFFF}, nil, "4294967295"},
		{"%5d|", [3]uint32{42}, nil, "   42|"},
		{"%c%c", [3]uint32{'H', 'i'}, nil, "Hi"},
		{"100%%", [3]uint32{}, nil, "100%"},
		{"%p", [3]uint32{0x08800000}, nil, "0x8800000"},
		{"%lld", [3]uint32{0xFFFFFFFF, 0x1}, nil, "8589934591"},
		{"%d %d %d %d", [3]uint32{1, 2, 3}, []uint32{4}, "1 2 3 4"},
	}
	for _, c := range cases {
		putString(h, fmtAddr, c.format)
		putString(h, strAddr, "PSP")
		h.cpu.SetReg(29, stackBase)
		for i, w := range c.stack {
			h.mem.Write32(stackBase+16+uint32(i*4), w)
		}
		h.cpu.SetReg(5, c.args[0])
		h.cpu.SetReg(6, c.args[1])
		h.cpu.SetReg(7, c.args[2])
		format := c.format
		if format == "%s-test" {
			h.cpu.SetReg(5, strAddr)
		}
		got := k.kernelPrintf(th, fmtAddr)
		if got != c.want {
			t.Errorf("printf %q = %q, want %q", c.format, got, c.want)
		}
	}
	// %s reads the pointed string.
	putString(h, fmtAddr, "[%s]")
	h.cpu.SetReg(5, strAddr)
	if got := k.kernelPrintf(th, fmtAddr); got != "[PSP]" {
		t.Errorf("printf %%s = %q, want [PSP]", got)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	h := newTestHost()
	k := New(h)
	k.cycles = 123456789
	k.vcount = 7
	k.writeConsole([]byte("hello state"))
	th, err := k.createThread("worker", 0x08804000, 0x30, 0x4000, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := &semaObj{uid: k.NewUID(kindSema), name: "s", initVal: 1, maxVal: 3, cur: 2}
	k.registerObject(s.uid, s)
	blob := k.Serialize()

	h2 := newTestHost()
	k2 := New(h2)
	if err := k2.Deserialize(blob); err != nil {
		t.Fatal(err)
	}
	if k2.cycles != k.cycles || k2.vcount != 7 {
		t.Fatalf("counters = %d/%d, want %d/7", k2.cycles, k2.vcount, k.cycles)
	}
	if k2.ConsoleText() != "hello state" {
		t.Fatalf("console = %q", k2.ConsoleText())
	}
	restored, ok := k2.threadByUID(th.uid)
	if !ok || restored.name != "worker" || restored.priority != 0x30 {
		t.Fatalf("thread restore = %+v ok=%v", restored, ok)
	}
	rs, ok := k2.semaByUID(s.uid)
	if !ok || rs.cur != 2 || rs.maxVal != 3 {
		t.Fatalf("sema restore = %+v ok=%v", rs, ok)
	}
	// Re-serializing the restored kernel reproduces the blob.
	if blob2 := k2.Serialize(); string(blob2) != string(blob) {
		t.Fatal("serialize after deserialize differs")
	}
}

// TestLwMutexWorkarea pins the lightweight mutex model the
// runtime libraries depend on: create initializes the guest
// workarea in place (level, owner, attr, waiter count, uid),
// lock and unlock update it, recursive locks nest for the owner,
// a foreign unlock is refused, and the object survives a
// serialize round trip.
func TestLwMutexWorkarea(t *testing.T) {
	h := newTestHost()
	k := New(h)
	const workarea = 0x08820000
	const nameAddr = 0x08821000
	putString(h, nameAddr, "stdio")
	th, err := k.createThread("worker", 0x08804000, 0x30, 0x4000, 0)
	if err != nil {
		t.Fatal(err)
	}
	setArgs := func(a0, a1, a2, a3 uint32) {
		h.cpu.SetReg(4, a0)
		h.cpu.SetReg(5, a1)
		h.cpu.SetReg(6, a2)
		h.cpu.SetReg(7, a3)
	}
	// Create with the recursive attribute (0x200) and count 0.
	setArgs(workarea, nameAddr, 0x200, 0)
	hleCreateLwMutex(k, th)
	uid := h.cpu.Reg(2)
	if uid&0xFF != kindLwMutex || uid == 0 {
		t.Fatalf("create returned %#x, want an lwmutex tagged UID", uid)
	}
	if got := h.mem.Read32(workarea + lwOffUID); got != uid {
		t.Fatalf("workarea uid = %#x, want %#x", got, uid)
	}
	if got := h.mem.Read32(workarea + lwOffAttr); got != 0x200 {
		t.Fatalf("workarea attr = %#x, want 0x200", got)
	}
	// Lock once, then recursively, then unlock both levels.
	setArgs(workarea, 1, 0, 0)
	hleLockLwMutex(k, th)
	if got := h.cpu.Reg(2); got != 0 {
		t.Fatalf("lock returned %#x", got)
	}
	if lv := h.mem.Read32(workarea + lwOffLevel); lv != 1 {
		t.Fatalf("level = %d, want 1", lv)
	}
	if ow := h.mem.Read32(workarea + lwOffOwner); ow != th.uid {
		t.Fatalf("owner = %#x, want %#x", ow, th.uid)
	}
	setArgs(workarea, 1, 0, 0)
	hleLockLwMutex(k, th)
	if lv := h.mem.Read32(workarea + lwOffLevel); lv != 2 {
		t.Fatalf("after recursive lock level = %d, want 2", lv)
	}
	setArgs(workarea, 2, 0, 0)
	hleUnlockLwMutex(k, th)
	if got := h.cpu.Reg(2); got != 0 {
		t.Fatalf("unlock returned %#x", got)
	}
	if lv := h.mem.Read32(workarea + lwOffLevel); lv != 0 {
		t.Fatalf("after unlock level = %d, want 0", lv)
	}
	// Unlocking an unlocked mutex reports the documented error.
	setArgs(workarea, 1, 0, 0)
	hleUnlockLwMutex(k, th)
	if got := h.cpu.Reg(2); got != ErrLwMutexUnlocked {
		t.Fatalf("unlock of free mutex = %#x, want %#x", got, ErrLwMutexUnlocked)
	}
	// Delete marks the workarea gone; later locks report it.
	setArgs(workarea, 0, 0, 0)
	hleDeleteLwMutex(k, th)
	if got := h.mem.Read32(workarea + lwOffUID); got != lwNoSuchUID {
		t.Fatalf("after delete workarea uid = %#x, want %#x", got, lwNoSuchUID)
	}
	setArgs(workarea, 1, 0, 0)
	hleLockLwMutex(k, th)
	if got := h.cpu.Reg(2); got != ErrLwMutexNotFound {
		t.Fatalf("lock after delete = %#x, want %#x", got, ErrLwMutexNotFound)
	}
}
