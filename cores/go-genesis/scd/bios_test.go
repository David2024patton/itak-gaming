package scd

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-genesis/chips/m68k"
)

// ---------------------------------------------------------------------------
// SCD-4 part 1 gates: the BIOS jump table and stub layout, an
// end to end 68k call through the jump table and trampoline into
// the Go services, BURAM at the mailbox level, CDBTOCREAD on a
// synthetic disc, the asynchronous drive queue, the CDC read
// family, and the grown save state with legacy acceptance.
// ---------------------------------------------------------------------------

// biosCallResult is one mailbox round trip outcome.
type biosCallResult struct {
	d0, d1, a0, a1 uint32
	carry          bool
}

// biosCall performs one mailbox call the way a trampoline
// leaves it: register image written, family byte stored, request
// byte set, one BIOSService pass, then the result block and CCR
// word read back.
func biosCall(u *Unit, family uint8, d0, d1, a0, a1 uint32) biosCallResult {
	img := [15]uint32{}
	img[0] = d0
	img[1] = d1
	img[8] = a0
	img[9] = a1
	for i, v := range img {
		u.Write32(mbImageAddr+uint32(i*4), v)
	}
	u.Write8(mbFamilyAddr, family)
	u.Write8(mbRequestAddr, 1)
	u.BIOSService()
	if u.Read8(mbRequestAddr) != 0 {
		panic("biosCall: request byte not cleared by BIOSService")
	}
	return biosCallResult{
		d0:    u.Read32(mbResultAddr),
		d1:    u.Read32(mbResultAddr + 4),
		a0:    u.Read32(mbResultAddr + 8),
		a1:    u.Read32(mbResultAddr + 12),
		carry: u.Read16(mbCCRAddr)&0x0001 != 0,
	}
}

// biosServiceN runs n bare BIOSService passes.
func biosServiceN(u *Unit, n int) {
	for i := 0; i < n; i++ {
		u.BIOSService()
	}
}

// putBytes writes data into PRG-RAM through the sub bus at addr.
func putBytes(u *Unit, addr uint32, data []byte) {
	for i, b := range data {
		u.Write8(addr+uint32(i), b)
	}
}

// getBytes reads n bytes from the sub bus at addr.
func getBytes(u *Unit, addr uint32, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = u.Read8(addr + uint32(i))
	}
	return out
}

func TestBIOSJumpTableAndVectors(t *testing.T) {
	img := BootStubImage()
	if len(img) != 0x6000 {
		t.Fatalf("BootStubImage len = %#x, want 0x6000", len(img))
	}
	if got := string(img[0x100:0x10C]); got != "iTaK CD BIOS" {
		t.Fatalf("ID string at 0x100 = %q, want iTaK CD BIOS", got)
	}
	vec := func(n int) uint32 { return binary.BigEndian.Uint32(img[n*4:]) }
	if vec(0) != 0x000A0000 || vec(1) != 0x00000200 {
		t.Fatalf("reset vectors = %#x/%#x, want SSP 0xA0000 and PC 0x200", vec(0), vec(1))
	}
	levels := [7]uint32{0x5EE0, 0x5EE6, 0x5EEC, 0x5EF2, 0x5EF8, 0x5EFE, 0x5F04}
	for i, want := range levels {
		if got := vec(25 + i); got != want {
			t.Errorf("autovector %d = %#x, want jump table entry %#x", 25+i, got, want)
		}
	}
	// Vector 24 (spurious) targets the RTE stub.
	if tgt := vec(24); img[tgt] != 0x4E || img[tgt+1] != 0x73 {
		t.Errorf("spurious vector target %#x holds %#02x %#02x, want RTE (4E 73)", tgt, img[tgt], img[tgt+1])
	}
	// Vectors 2 and 40 target the fatal stub: MOVE.B #0xEE, abs.
	for _, v := range []int{2, 23, 32, 40, 63} {
		tgt := vec(v)
		if img[tgt] != 0x13 || img[tgt+1] != 0xFC || img[tgt+2] != 0x00 || img[tgt+3] != 0xEE {
			t.Errorf("vector %d target %#x does not start with MOVE.B #0xEE (13 FC 00 EE)", v, tgt)
		}
	}
	// Every jump table entry is a JMP absolute long.
	for i := 0; i < 14; i++ {
		off := JumpTableBase + i*6
		if img[off] != 0x4E || img[off+1] != 0xF9 {
			t.Fatalf("jump table entry %d at %#x = %#02x %#02x, want JMP abs (4E F9)", i, off, img[off], img[off+1])
		}
	}
	entryTarget := func(off int) uint32 { return binary.BigEndian.Uint32(img[off+2:]) }
	// Level entries target an RTE, usercalls target an RTS, and
	// the three family entries target MOVEM trampolines.
	for i := 0; i < 7; i++ {
		tgt := entryTarget(JumpTableBase + i*6)
		if img[tgt] != 0x4E || img[tgt+1] != 0x73 {
			t.Errorf("level entry %d target %#x is not an RTE stub", i+1, tgt)
		}
	}
	for _, idx := range []int{7, 8, 10, 13} {
		tgt := entryTarget(JumpTableBase + idx*6)
		if img[tgt] != 0x4E || img[tgt+1] != 0x75 {
			t.Errorf("usercall entry %d target %#x is not an RTS stub", idx, tgt)
		}
	}
	for _, off := range []int{0x5F16, 0x5F22, 0x5F28} {
		tgt := entryTarget(off)
		if img[tgt] != 0x48 || img[tgt+1] != 0xF9 {
			t.Errorf("family entry at %#x target %#x does not start with MOVEM (48 F9)", off, tgt)
		}
	}

	// The RAM overlay: reads come from the overlay copy, writes
	// patch it, and the stub image itself is untouched.
	u := NewUnit(nil)
	if got := u.Read8(JumpTableBase); got != 0x4E {
		t.Fatalf("overlay read at jump table = %#02x, want 0x4E", got)
	}
	u.Write8(0x5F0A, 0xAA)
	if got := u.Read8(0x5F0A); got != 0xAA {
		t.Fatalf("overlay patched read = %#02x, want 0xAA", got)
	}
	if got := BootStubImage()[0x5F0A]; got != 0x4E {
		t.Fatalf("stub image changed by overlay write: %#02x", got)
	}
	if got := u.Read8(0x0100); got != 'i' {
		t.Fatalf("stub read outside overlay = %#02x, want i", got)
	}
}

// runCaller places a hand assembled caller at 0x26000 in PRG-RAM,
// runs the sub CPU on the unit bus with BIOSService interleaved,
// and returns the CPU once the caller reaches its final BRA.
func runCaller(t *testing.T, u *Unit, caller []byte, braOff int) *m68k.CPU {
	t.Helper()
	putBytes(u, 0x26000, caller)
	cpu := m68k.New(u)
	r := cpu.Registers()
	r.PC = 0x26000
	r.SR = 0x2700
	r.SSP = 0x000A0000
	cpu.SetState(r)
	braAddr := uint32(0x26000 + braOff)
	for i := 0; i < 200; i++ {
		cpu.StepCycles(1000)
		u.BIOSService()
		if cpu.Registers().PC == braAddr && !u.BIOSPending() {
			return cpu
		}
		if cpu.Halted() {
			t.Fatal("sub CPU halted during BIOS caller run")
		}
	}
	t.Fatalf("caller never reached its idle BRA (PC = %#x)", cpu.Registers().PC)
	return cpu
}

func TestBIOSEndToEndBuramCall(t *testing.T) {
	u := NewUnit(nil)
	// Caller: A0 = scratch, A1 = display buffer, BRMFORMAT via
	// _buram, then BRMSTAT via _buram, then idle.
	caller := []byte{
		0x20, 0x7C, 0x00, 0x03, 0x00, 0x00, // MOVEA.L #0x30000, A0
		0x22, 0x7C, 0x00, 0x03, 0x01, 0x00, // MOVEA.L #0x30100, A1
		0x70, 0x06, // MOVEQ #6, D0 (BRMFORMAT)
		0x4E, 0xB9, 0x00, 0x00, 0x5F, 0x16, // JSR _buram
		0x70, 0x01, // MOVEQ #1, D0 (BRMSTAT)
		0x4E, 0xB9, 0x00, 0x00, 0x5F, 0x16, // JSR _buram
		0x60, 0xFE, // BRA .
	}
	cpu := runCaller(t, u, caller, len(caller)-2)
	regs := cpu.Registers()
	if got := regs.D[0] & 0xFFFF; got != 125 {
		t.Fatalf("D0.w after BRMSTAT = %d, want 125 free blocks", got)
	}
	if got := regs.D[1] & 0xFFFF; got != 0 {
		t.Fatalf("D1.w after BRMSTAT = %d, want 0 files", got)
	}
	if regs.SR&0x0001 != 0 {
		t.Fatalf("SR carry set after BRMSTAT, want clear (SR = %#06x)", regs.SR)
	}
	if got := u.Read8(mbFamilyAddr); got != 1 {
		t.Fatalf("mailbox family byte = %d, want 1 (buram trampoline)", got)
	}
	if got := string(getBytes(u, 0x30100, 12)); got != bramDisplayString {
		t.Fatalf("display buffer = %q, want %q", got, bramDisplayString)
	}
}

func TestBIOSEndToEndCdbiosCall(t *testing.T) {
	u := NewUnit(nil)
	// Caller: D0 = CDBCHK (0x80) via _cdbios, then idle. With an
	// empty drive queue the carry result is clear.
	caller := []byte{
		0x20, 0x3C, 0x00, 0x00, 0x00, 0x80, // MOVE.L #0x80, D0
		0x4E, 0xB9, 0x00, 0x00, 0x5F, 0x22, // JSR _cdbios
		0x60, 0xFE, // BRA .
	}
	cpu := runCaller(t, u, caller, len(caller)-2)
	regs := cpu.Registers()
	if got := regs.D[0] & 0xFFFF; got != 0x80 {
		t.Fatalf("D0.w after CDBCHK = %#x, want 0x80 (registers unchanged)", got)
	}
	if regs.SR&0x0001 != 0 {
		t.Fatalf("SR carry set after CDBCHK with empty queue, want clear")
	}
	if got := u.Read8(mbFamilyAddr); got != 0 {
		t.Fatalf("mailbox family byte = %d, want 0 (cdbios trampoline)", got)
	}
}

// bramNameTable writes a BRMWRITE style table {name, flag,
// blockSize word} at addr.
func bramNameTable(u *Unit, addr uint32, name string, flag uint8, size uint16) {
	putBytes(u, addr, []byte(name))
	u.Write8(addr+11, flag)
	u.Write16(addr+12, size)
}

func TestBIOSBuramRoundTrip(t *testing.T) {
	u := NewUnit(nil)
	const nameTab = 0x31000
	const dispBuf = 0x30100

	// Unformatted: BRMINIT fails with D0.b = 1, BRMSTAT reports
	// the 0xFFFF pair.
	res := biosCall(u, 1, 0, 0, 0, dispBuf)
	if !res.carry || res.d0&0xFF != 1 {
		t.Fatalf("BRMINIT unformatted: carry %v D0.b %#02x, want carry set and 1", res.carry, res.d0&0xFF)
	}
	res = biosCall(u, 1, 1, 0, 0, dispBuf)
	if res.d0&0xFFFF != 0xFFFF || res.d1&0xFFFF != 0xFFFF {
		t.Fatalf("BRMSTAT unformatted: D0.w %#x D1.w %#x, want FFFF/FFFF", res.d0&0xFFFF, res.d1&0xFFFF)
	}

	// Format and check the footer byte for byte.
	res = biosCall(u, 1, 6, 0, 0, 0)
	if res.carry {
		t.Fatal("BRMFORMAT reported carry set")
	}
	fo := BRAMSize - 64
	for i := 0; i < 11; i++ {
		if u.bram[fo+i] != 0x5F {
			t.Fatalf("footer byte %d = %#02x, want 0x5F", i, u.bram[fo+i])
		}
	}
	for i := 11; i < 15; i++ {
		if u.bram[fo+i] != 0 {
			t.Fatalf("footer byte %d = %#02x, want 0", i, u.bram[fo+i])
		}
	}
	if u.bram[fo+15] != 0x40 {
		t.Fatalf("footer byte 15 = %#02x, want 0x40", u.bram[fo+15])
	}
	for k := 0; k < 4; k++ {
		if got := binary.BigEndian.Uint16(u.bram[fo+16+k*2:]); got != 125 {
			t.Fatalf("footer free word %d = %d, want 125", k, got)
		}
		if got := binary.BigEndian.Uint16(u.bram[fo+24+k*2:]); got != 0 {
			t.Fatalf("footer file word %d = %d, want 0", k, got)
		}
	}
	if got := string(u.bram[fo+32 : fo+43]); got != "SEGA_CD_ROM" {
		t.Fatalf("footer ID = %q, want SEGA_CD_ROM", got)
	}
	if u.bram[fo+43] != 0 || u.bram[fo+44] != 0x01 {
		t.Fatalf("footer bytes 43/44 = %#02x/%#02x, want 0/1", u.bram[fo+43], u.bram[fo+44])
	}
	if got := string(u.bram[fo+48 : fo+64]); got != "RAM_CARTRIDGE___" {
		t.Fatalf("footer name = %q, want RAM_CARTRIDGE___", got)
	}
	res = biosCall(u, 1, 0, 0, 0, dispBuf)
	if res.carry || string(getBytes(u, dispBuf, 12)) != bramDisplayString {
		t.Fatalf("BRMINIT formatted: carry %v display %q", res.carry, getBytes(u, dispBuf, 12))
	}

	// Write a one block file with known bytes.
	data := make([]byte, 64)
	for i := range data {
		data[i] = byte(i*3 + 1)
	}
	putBytes(u, 0x32000, data)
	bramNameTable(u, nameTab, "TESTFILE___", 0, 1)
	res = biosCall(u, 1, 4, 0, nameTab, 0x32000)
	if res.carry {
		t.Fatal("BRMWRITE reported carry set")
	}
	if res.a0 != 0x600040 {
		t.Fatalf("BRMWRITE A0 = %#x, want 0x600040 (block 1)", res.a0)
	}
	// Search it.
	res = biosCall(u, 1, 2, 0, nameTab, 0)
	if res.carry || res.d0&0xFFFF != 1 || res.d1&0xFF != 0 || res.a0 != 0x600040 {
		t.Fatalf("BRMSERCH: carry %v D0.w %d D1.b %#02x A0 %#x, want clear/1/0/0x600040",
			res.carry, res.d0&0xFFFF, res.d1&0xFF, res.a0)
	}
	// Read it back byte equal.
	res = biosCall(u, 1, 3, 0, nameTab, 0x33000)
	if res.carry {
		t.Fatal("BRMREAD reported carry set")
	}
	if got := getBytes(u, 0x33000, 64); !bytes.Equal(got, data) {
		t.Fatal("BRMREAD data does not match what was written")
	}
	// Stat: 125 - 1 data block - 1 directory block = 123.
	res = biosCall(u, 1, 1, 0, 0, dispBuf)
	if res.d0&0xFFFF != 123 || res.d1&0xFFFF != 1 {
		t.Fatalf("BRMSTAT: free %d files %d, want 123/1", res.d0&0xFFFF, res.d1&0xFFFF)
	}
	// Verify: match, mismatch, and missing file outcomes.
	res = biosCall(u, 1, 8, 0, nameTab, 0x32000)
	if res.carry {
		t.Fatal("BRMVERIFY with matching data reported carry set")
	}
	u.Write8(0x32000, data[0]^0xFF)
	res = biosCall(u, 1, 8, 0, nameTab, 0x32000)
	if !res.carry || res.d0&0xFFFF != 0xFFFF {
		t.Fatalf("BRMVERIFY mismatch: carry %v D0.w %#x, want set/FFFF", res.carry, res.d0&0xFFFF)
	}
	u.Write8(0x32000, data[0])
	bramNameTable(u, 0x31800, "NOSUCHFILE_", 0, 1)
	res = biosCall(u, 1, 8, 0, 0x31800, 0x32000)
	if !res.carry || res.d0&0xFFFF != 0 {
		t.Fatalf("BRMVERIFY missing: carry %v D0.w %#x, want set/0", res.carry, res.d0&0xFFFF)
	}
	// Directory listing with a wildcard template.
	putBytes(u, 0x31900, []byte("TEST*******"))
	res = biosCall(u, 1, 7, 4, 0x31900, 0x33800) // capacity 4 in D1 low word
	if res.carry {
		t.Fatal("BRMDIR with room for all matches reported carry set")
	}
	dirEnt := getBytes(u, 0x33800, 16)
	if string(dirEnt[:11]) != "TESTFILE___" || binary.BigEndian.Uint16(dirEnt[12:]) != 1 || binary.BigEndian.Uint16(dirEnt[14:]) != 1 {
		t.Fatalf("BRMDIR entry = %q start %d units %d, want TESTFILE___/1/1",
			dirEnt[:11], binary.BigEndian.Uint16(dirEnt[12:]), binary.BigEndian.Uint16(dirEnt[14:]))
	}
	res = biosCall(u, 1, 7, 0, 0x31900, 0x33800) // capacity 0: the match does not fit
	if !res.carry {
		t.Fatal("BRMDIR capacity 0 did not set carry (more entries remain)")
	}
	res = biosCall(u, 1, 7, 1, 0x31900, 0x33800) // capacity 1: fits the one match
	if res.carry {
		t.Fatal("BRMDIR capacity 1 with one match reported carry set")
	}
	res = biosCall(u, 1, 7, 0x00010000, 0x31900, 0x33800) // skip 1 in D1 high word: nothing left
	if res.carry {
		t.Fatal("BRMDIR skip past the only match reported carry set")
	}
	// Delete, search fails, stat returns to 125 free.
	res = biosCall(u, 1, 5, 0, nameTab, 0)
	if res.carry {
		t.Fatal("BRMDEL reported carry set")
	}
	res = biosCall(u, 1, 2, 0, nameTab, 0)
	if !res.carry {
		t.Fatal("BRMSERCH after delete did not set carry")
	}
	res = biosCall(u, 1, 1, 0, 0, dispBuf)
	if res.d0&0xFFFF != 125 || res.d1&0xFFFF != 0 {
		t.Fatalf("BRMSTAT after delete: free %d files %d, want 125/0", res.d0&0xFFFF, res.d1&0xFFFF)
	}

	// Protected file: flag 0xFF, 4 units of 0x20 = 128 bytes.
	pdata := make([]byte, 128)
	for i := range pdata {
		pdata[i] = byte(0xA0 + i)
	}
	putBytes(u, 0x32000, pdata)
	bramNameTable(u, nameTab, "PROT_______", 0xFF, 4)
	res = biosCall(u, 1, 4, 0, nameTab, 0x32000)
	if res.carry {
		t.Fatal("BRMWRITE protected reported carry set")
	}
	res = biosCall(u, 1, 2, 0, nameTab, 0)
	if res.carry || res.d0&0xFFFF != 4 || res.d1&0xFF != 0xFF {
		t.Fatalf("BRMSERCH protected: carry %v units %d flag %#02x, want clear/4/FF",
			res.carry, res.d0&0xFFFF, res.d1&0xFF)
	}
	res = biosCall(u, 1, 3, 0, nameTab, 0x33000)
	if res.carry {
		t.Fatal("BRMREAD protected reported carry set")
	}
	if got := getBytes(u, 0x33000, 128); !bytes.Equal(got, pdata) {
		t.Fatal("BRMREAD protected data does not match (want 128 bytes)")
	}
	res = biosCall(u, 1, 1, 0, 0, dispBuf)
	if res.d0&0xFFFF != 122 {
		t.Fatalf("BRMSTAT with protected file: free %d, want 122 (125 - 2 data - 1 dir)", res.d0&0xFFFF)
	}
}

func TestBIOSCDTOCREAD(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	u := NewUnit(disc)
	toc := func(tno uint32) biosCallResult {
		return biosCall(u, 0, 0x83, tno, 0, 0)
	}
	res := toc(1)
	if res.d0 != 0x00028001 || res.d1&0xFF != 0xFF || res.carry {
		t.Fatalf("track 1: D0 %#010x D1.b %#02x carry %v, want 00028001/FF/clear", res.d0, res.d1&0xFF, res.carry)
	}
	res = toc(2)
	if res.d0 != 0x00021002 || res.d1&0xFF != 0x00 {
		t.Fatalf("track 2: D0 %#010x D1.b %#02x, want 00021002/00", res.d0, res.d1&0xFF)
	}
	res = toc(3)
	if res.d0 != 0x00041003 {
		t.Fatalf("track 3: D0 %#010x, want 00041003", res.d0)
	}
	res = toc(0)
	if res.d0 != 0x00061000 {
		t.Fatalf("lead-out: D0 %#010x, want 00061000", res.d0)
	}
	res = toc(9)
	if res.d0 != 0x00028000 {
		t.Fatalf("out of range: D0 %#010x, want 00028000 (disc start, tno 0)", res.d0)
	}
	// A TOC override is consulted first.
	putBytes(u, 0x31000, []byte{0x01, 0x23, 0xC5, 0x02, 0x00, 0x00, 0x00, 0xFF})
	biosCall(u, 0, 0x82, 0, 0x31000, 0)
	res = toc(2)
	if res.d0 != 0x0123C502 || res.d1&0xFF != 0xFF {
		t.Fatalf("override track 2: D0 %#010x D1.b %#02x, want 0123C502/FF", res.d0, res.d1&0xFF)
	}
	res = toc(1)
	if res.d0 != 0x00028001 {
		t.Fatalf("track 1 with override present: D0 %#010x, want disc value 00028001", res.d0)
	}
}

func TestBIOSAsyncDriveQueue(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	u := NewUnit(disc)

	// DRVINIT queues and returns at once.
	putBytes(u, 0x31000, []byte{1, 3})
	res := biosCall(u, 0, 0x10, 0, 0x31000, 0)
	if res.carry {
		t.Fatal("DRVINIT call reported carry set")
	}
	if !u.BIOSPending() {
		t.Fatal("drive queue empty right after DRVINIT call")
	}
	if u.bios.driveInit {
		t.Fatal("DRVINIT executed in the call pass, want deferred")
	}
	// CDBCHK reports busy for a while, then clear once the
	// queued command has aged 223 service calls and executed.
	sawBusy := false
	cleared := false
	for i := 0; i < 400; i++ {
		res = biosCall(u, 0, 0x80, 0, 0, 0)
		if res.carry {
			sawBusy = true
		} else {
			cleared = true
			break
		}
	}
	if !sawBusy {
		t.Fatal("CDBCHK never reported the queue busy")
	}
	if !cleared {
		t.Fatal("CDBCHK never cleared after 400 service calls")
	}
	if !u.bios.driveInit {
		t.Fatal("queued DRVINIT never executed")
	}

	// Rejected function numbers never queue.
	res = biosCall(u, 0, 0x0D, 0, 0x31000, 0)
	if res.carry || u.BIOSPending() {
		t.Fatal("rejected fn 0x0D queued or set carry")
	}
	res = biosCall(u, 0, 0x25, 0, 0x31000, 0)
	if res.carry || u.BIOSPending() {
		t.Fatal("rejected fn 0x25 queued or set carry")
	}

	// MSCPLAY on audio track 2 leaves the CDD playing it.
	u.Write16(0x31000, 2)
	biosCall(u, 0, 0x11, 0, 0x31000, 0)
	if !u.BIOSPending() {
		t.Fatal("MSCPLAY did not queue")
	}
	biosServiceN(u, 300)
	u.AdvanceCycles(200000)
	if u.CDDState() != CDDPlaying {
		t.Fatalf("CDD state after MSCPLAY = %v, want playing", u.CDDState())
	}
	if pos := u.CDDPositionLBA(); pos < 10 {
		t.Fatalf("CDD position = LBA %d, want at or past track 2 start (10)", pos)
	}
}

func TestBIOSCDCReadFlow(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	u := NewUnit(disc)

	// ROMREADN of 2 sectors at LBA 2.
	u.Write32(0x31000, 2)
	u.Write32(0x31004, 2)
	biosCall(u, 0, 0x20, 0, 0x31000, 0)
	biosServiceN(u, 300)
	if !u.bios.readActive || u.bios.readRemaining != 2 {
		t.Fatalf("read state after ROMREADN: active %v remaining %d, want true/2",
			u.bios.readActive, u.bios.readRemaining)
	}

	wantHeaders := [2]uint32{0x00020201, 0x00020301}
	for i, lba := range []int{2, 3} {
		// Wait for the sector to land in the CDC buffer.
		ready := false
		for try := 0; try < 20 && !ready; try++ {
			u.AdvanceCycles(166667)
			res := biosCall(u, 0, 0x8A, 0, 0, 0) // CDCSTAT
			ready = !res.carry
		}
		if !ready {
			t.Fatalf("sector %d never became ready", lba)
		}
		res := biosCall(u, 0, 0x8B, 0, 0, 0) // CDCREAD
		if res.carry || res.d0 != wantHeaders[i] {
			t.Fatalf("CDCREAD sector %d: carry %v D0 %#010x, want clear/%#010x", lba, res.carry, res.d0, wantHeaders[i])
		}
		res = biosCall(u, 0, 0x8C, 0, 0x34000, 0x35000) // CDCTRN
		if res.carry {
			t.Fatalf("CDCTRN sector %d reported carry set", lba)
		}
		if res.a0 != 0x34000+2336 || res.a1 != 0x35004 {
			t.Fatalf("CDCTRN results: A0 %#x A1 %#x, want 34000+2336/35004", res.a0, res.a1)
		}
		want, err := disc.ReadData(lba)
		if err != nil {
			t.Fatalf("ReadData(%d): %v", lba, err)
		}
		if got := getBytes(u, 0x34000, 2048); !bytes.Equal(got, want) {
			t.Fatalf("CDCTRN sector %d data does not match the disc sector", lba)
		}
		for j, b := range getBytes(u, 0x34000+2048, 288) {
			if b != 0 {
				t.Fatalf("CDCTRN sector %d EDC/ECC byte %d = %#02x, want 0", lba, j, b)
			}
		}
		hdr := getBytes(u, 0x35000, 4)
		if hdr[0] != byte(wantHeaders[i]>>24) || hdr[1] != byte(wantHeaders[i]>>16) ||
			hdr[2] != byte(wantHeaders[i]>>8) || hdr[3] != byte(wantHeaders[i]) {
			t.Fatalf("CDCTRN sector %d header buffer = % x, want header long bytes", lba, hdr)
		}
		res = biosCall(u, 0, 0x8D, 0, 0, 0) // CDCACK
		if res.carry {
			t.Fatalf("CDCACK sector %d reported carry set", lba)
		}
	}
	// The bounded count is exhausted: the BIOS read is inactive.
	// The CDD transport itself is left streaming (CDCSTOP is the
	// call that stops it), matching the full stack fixture.
	if u.bios.readActive {
		t.Fatal("read still active after the bounded count was ACKed out")
	}
	if u.CDDState() != CDDReading {
		t.Fatalf("CDD state after bounded read = %v, want reading (transport left running)", u.CDDState())
	}
	biosCall(u, 0, 0x89, 0, 0, 0) // CDCSTOP
	if u.CDDState() != CDDStopped {
		t.Fatalf("CDD state after CDCSTOP = %v, want stopped", u.CDDState())
	}
	res := biosCall(u, 0, 0x8B, 0, 0, 0)
	if !res.carry {
		t.Fatal("CDCREAD with nothing prepared did not set carry")
	}
}

func TestBIOSSundrySync(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	u := NewUnit(disc)

	// CDBSTAT fills the table and returns its address.
	res := biosCall(u, 0, 0x81, 0, 0, 0)
	if res.a0 != 0x25E80 {
		t.Fatalf("CDBSTAT A0 = %#x, want 25E80", res.a0)
	}
	tbl := getBytes(u, 0x25E80, 32)
	if tbl[0] != 0 || tbl[1] != 0 || tbl[2] != 0 {
		t.Fatalf("CDBSTAT drv/msc/rom = %d/%d/%d, want 0/0/0 (disc in, stopped)", tbl[0], tbl[1], tbl[2])
	}
	if tbl[4] != 0x09 {
		t.Fatalf("CDBSTAT BP0 = %#02x, want 09 (stopped display status)", tbl[4])
	}
	if tbl[7] != 1 {
		t.Fatalf("CDBSTAT BP3 current track = %d, want 1", tbl[7])
	}
	if tbl[16] != 1 || tbl[17] != 3 {
		t.Fatalf("CDBSTAT first/last = %d/%d, want 1/3", tbl[16], tbl[17])
	}
	if tbl[19] != 0x04 {
		t.Fatalf("CDBSTAT BP15 = %#02x, want 04 (current track is data)", tbl[19])
	}
	if tbl[20] != 0x00 || tbl[21] != 0x06 || tbl[22] != 0x10 {
		t.Fatalf("CDBSTAT lead-out MSF = %02x %02x %02x, want 00 06 10 BCD", tbl[20], tbl[21], tbl[22])
	}
	if got := binary.BigEndian.Uint32(tbl[24:28]); got != 0x04000400 {
		t.Fatalf("CDBSTAT volume long = %#010x, want 04000400", got)
	}

	// CDBPAUSE stores the standby time.
	biosCall(u, 0, 0x84, 0x1234, 0, 0)
	if u.bios.standbyPause != 0x1234 {
		t.Fatalf("standby pause = %#x, want 1234", u.bios.standbyPause)
	}
	// FDRSET volume then master.
	biosCall(u, 0, 0x85, 0x0200, 0, 0)
	if u.bios.faderVol != 0x0200 || u.cddEngine().FaderFD() != 512 {
		t.Fatalf("FDRSET volume: bios %d CDD FD %d, want 512/512", u.bios.faderVol, u.cddEngine().FaderFD())
	}
	biosCall(u, 0, 0x85, 0x8000|0x0200, 0, 0) // master = 0x0200
	if u.bios.faderMaster != 0x0200 || u.cddEngine().FaderFD() != 256 {
		t.Fatalf("FDRSET master: bios %d CDD FD %d, want 512/256", u.bios.faderMaster, u.cddEngine().FaderFD())
	}
	// LEDSET mode 2 (access, both) lands in the LED storage.
	biosCall(u, 0, 0x95, 2, 0, 0)
	if u.led != 0x06 || u.bios.ledMode != 2 {
		t.Fatalf("LEDSET: led storage %#02x mode %d, want 06/2", u.led, u.bios.ledMode)
	}
	// CDCSETMODE with a valid DDT value writes reg03 bits 3-4.
	biosCall(u, 0, 0x96, 2, 0, 0)
	if got := u.Read8(0xFF8003) & 0x18; got != 0x10 {
		t.Fatalf("reg03 DDT bits = %#02x, want 10", got)
	}
	if u.bios.cdcDestMode != 2 {
		t.Fatalf("cdcDestMode = %d, want 2", u.bios.cdcDestMode)
	}
	// SCDSTAT succeeds with zeros; SCDREAD fails (no subcode).
	res = biosCall(u, 0, 0x91, 0, 0, 0)
	if res.carry || res.d0 != 0 || res.d1 != 0 {
		t.Fatalf("SCDSTAT: carry %v D0 %#x D1 %#x, want clear/0/0", res.carry, res.d0, res.d1)
	}
	res = biosCall(u, 0, 0x92, 0, 0, 0)
	if !res.carry || res.d0 != 0 {
		t.Fatalf("SCDREAD: carry %v D0 %#x, want set/0", res.carry, res.d0)
	}
	// WONDERREQ is a failure stub.
	res = biosCall(u, 0, 0x97, 0, 0, 0)
	if !res.carry || res.d0 != 0 {
		t.Fatalf("WONDERREQ: carry %v D0 %#x, want set/0", res.carry, res.d0)
	}
	// Unknown family fails with carry set and D0 = 0.
	res = biosCall(u, 9, 0, 0, 0, 0)
	if !res.carry || res.d0 != 0 {
		t.Fatalf("unknown family: carry %v D0 %#x, want set/0", res.carry, res.d0)
	}
}

func TestBIOSCDBoot(t *testing.T) {
	// The cue fixture is a data disc without the boot signature.
	disc, _ := buildCDDFixture(t)
	u := NewUnit(disc)
	res := biosCall(u, 2, 5, 0, 0, 0) // CBTCHKSTAT
	if res.carry || res.d0&0xFFFF != 2 {
		t.Fatalf("CBTCHKSTAT data non-boot: carry %v D0.w %d, want clear/2", res.carry, res.d0&0xFFFF)
	}
	res = biosCall(u, 2, 4, 0, 0x36000, 0) // CBTCHKDISC
	if !res.carry {
		t.Fatal("CBTCHKDISC on a non-boot disc did not set carry")
	}
	want0, err := disc.ReadData(0)
	if err != nil {
		t.Fatalf("ReadData(0): %v", err)
	}
	if got := getBytes(u, 0x36000, 2048); !bytes.Equal(got, want0) {
		t.Fatal("CBTCHKDISC did not copy sector 0 into the scratch")
	}

	// A boot ISO (sector 0 carries SEGADISCSYSTEM) is type 5 and
	// CBTCHKDISC clears carry.
	boot := openISO(t, []byte{0x4E, 0x71})
	u2 := NewUnit(boot)
	res = biosCall(u2, 2, 5, 0, 0, 0)
	if res.carry || res.d0&0xFFFF != 5 {
		t.Fatalf("CBTCHKSTAT boot disc: carry %v D0.w %d, want clear/5", res.carry, res.d0&0xFFFF)
	}
	res = biosCall(u2, 2, 4, 0, 0x36000, 0)
	if res.carry {
		t.Fatal("CBTCHKDISC on a boot disc reported carry set")
	}
	if got := string(getBytes(u2, 0x36000, 14)); got != "SEGADISCSYSTEM" {
		t.Fatalf("CBTCHKDISC scratch starts %q, want SEGADISCSYSTEM", got)
	}
	// CBTINIT and CBTINT are no-op successes.
	for _, fn := range []uint32{0, 1, 3} {
		res = biosCall(u2, 2, fn, 0, 0, 0)
		if res.carry {
			t.Fatalf("CDBOOT fn %d reported carry set", fn)
		}
	}
	// No disc: type -1.
	u3 := NewUnit(nil)
	res = biosCall(u3, 2, 5, 0, 0, 0)
	if res.d0&0xFFFF != 0xFFFF {
		t.Fatalf("CBTCHKSTAT no disc: D0.w %#x, want FFFF", res.d0&0xFFFF)
	}
	res = biosCall(u3, 2, 4, 0, 0x36000, 0)
	if !res.carry {
		t.Fatal("CBTCHKDISC with no disc did not set carry")
	}
	// Unknown cdboot function fails.
	res = biosCall(u3, 2, 9, 0, 0, 0)
	if !res.carry || res.d0 != 0 {
		t.Fatalf("CDBOOT unknown fn: carry %v D0 %#x, want set/0", res.carry, res.d0)
	}
}

func TestBIOSStateGrowth(t *testing.T) {
	if BRAMSize != 8192 || BIOSStateSize != 768 {
		t.Fatalf("BRAMSize %d BIOSStateSize %d, want 8192/768", BRAMSize, BIOSStateSize)
	}
	u := NewUnit(nil)
	if got := u.SerializeSize(); got != 863232 {
		t.Fatalf("SerializeSize = %d, want 863232", got)
	}
	// Put a file in BRAM and patch a jump table overlay byte.
	biosCall(u, 1, 6, 0, 0, 0)
	data := []byte("hello bram world, this file survives a save state")
	putBytes(u, 0x32000, data)
	bramNameTable(u, 0x31000, "SAVEFILE___", 0, 1)
	if res := biosCall(u, 1, 4, 0, 0x31000, 0x32000); res.carry {
		t.Fatal("BRMWRITE for state test reported carry set")
	}
	u.Write8(0x5F0B, 0xF8)

	buf := make([]byte, u.SerializeSize())
	if err := u.Serialize(buf); err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	u2 := NewUnit(nil)
	if err := u2.Deserialize(buf); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	// Re-serialize matches byte for byte (checked before any
	// further mailbox traffic writes into the restored PRG-RAM).
	buf2 := make([]byte, u2.SerializeSize())
	if err := u2.Serialize(buf2); err != nil {
		t.Fatalf("re-Serialize: %v", err)
	}
	if !bytes.Equal(buf, buf2) {
		t.Fatal("state round trip did not reproduce the serialized bytes")
	}
	res := biosCall(u2, 1, 2, 0, 0x31000, 0)
	if res.carry || res.a0 != 0x600040 {
		t.Fatalf("serch after restore: carry %v A0 %#x, want clear/600040", res.carry, res.a0)
	}
	res = biosCall(u2, 1, 3, 0, 0x31000, 0x33000)
	if res.carry || !bytes.Equal(getBytes(u2, 0x33000, len(data)), data) {
		t.Fatal("BRAM file contents did not survive the state round trip")
	}
	if got := u2.Read8(0x5F0B); got != 0xF8 {
		t.Fatalf("overlay byte after restore = %#02x, want F8", got)
	}

	// A legacy 854272 byte state loads with BRAM unformatted and
	// BIOS defaults in place.
	u3 := NewUnit(nil)
	if err := u3.Deserialize(buf[:legacySerializeSize]); err != nil {
		t.Fatalf("Deserialize legacy: %v", err)
	}
	res = biosCall(u3, 1, 0, 0, 0, 0x30100)
	if !res.carry || res.d0&0xFF != 1 {
		t.Fatal("legacy state BRAM is formatted, want unformatted")
	}
	if u3.bios.faderVol != 0x0400 || u3.bios.faderMaster != 0x0400 {
		t.Fatalf("legacy fader = %d/%d, want 1024/1024", u3.bios.faderVol, u3.bios.faderMaster)
	}
	if got := u3.Read8(JumpTableBase); got != 0x4E {
		t.Fatalf("legacy overlay byte = %#02x, want 4E from the stub image", got)
	}
	if err := u3.Deserialize(make([]byte, 100)); err == nil {
		t.Fatal("Deserialize of a tiny buffer succeeded, want an error")
	}
}
