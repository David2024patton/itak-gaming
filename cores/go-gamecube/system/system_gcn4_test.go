package system

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-gamecube/disc"
)

// GCN-4 homebrew enginetest: a fully synthetic GameCube
// disc whose DOL, entered through the real BootDisc HLE
// boot with no seeding, drives every subsystem the core
// built across GCN-1 to GCN-3 and reports progress in
// milestone flag words in MEM1. The DOL walks the FST to
// find TESTDATA.BIN, DI-DMAs it into MEM1 and checksums
// it, streams a 440 Hz sine through the AI, polls the SI
// pad, round-trips a 512 byte pattern through memory
// card A over EXI, pushes a GX FIFO stream that clears
// the EFB to a programmed color and draws a flat-shaded
// quad, then spins on the VI current-halfline register.
//
// Effective addresses the DOL uses: work areas ride the
// boot BAT0 MEM1 window (0x80000000). The hardware page
// at 0x0C000000 sits outside both boot BATs (DBAT1 spans
// only the first 32 MB), so the DOL programs DBAT2 for
// it through mtspr before touching any device, the same
// thing the SDK runtime does on real software.

const (
	engFlags   = 0x80006000 // five milestone words
	engLog     = 0x80006100 // stage numbers in completion order
	engSIBuf   = 0x80007000 // SI INBUF pair copied here
	engSinePhy = 0x00008000 // AI DMA source (physical)
	engDIDest  = 0x00009000 // DI DMA destination (physical)
	engCardPat = 0x0000A000 // card pattern source (physical)
	engCardRB  = 0x0000B000 // card readback destination (physical)
	engTestOff = 0x20000    // TESTDATA.BIN offset on the disc
	engTestLen = 2048
	engSineN   = 960 // stereo frames of 440 Hz sine
)

// testDataByte is the TESTDATA.BIN content pattern.
func testDataByte(i int) byte { return byte(i*7 + 3) }

func testDataSum() uint32 {
	var s uint32
	for i := 0; i < engTestLen; i++ {
		s += uint32(testDataByte(i))
	}
	return s
}

// ppcAsm is a tiny two-pass assembler for the fixture
// program: instructions are emitted with named labels,
// and branches are patched once every label is known.
type ppcFix struct {
	at     int
	label  string
	bo, bi uint32
	abs    bool
}

type ppcAsm struct {
	code   []uint32
	labels map[string]int
	fix    []ppcFix
	serial int
}

func newAsm() *ppcAsm { return &ppcAsm{labels: map[string]int{}} }

func (a *ppcAsm) e(w uint32) { a.code = append(a.code, w) }

func (a *ppcAsm) lab(name string) { a.labels[name] = len(a.code) }

func (a *ppcAsm) uniq(prefix string) string {
	a.serial++
	return prefix + string(rune('a'+a.serial%26)) + string(rune('0'+(a.serial/26)%10))
}

func (a *ppcAsm) b(label string) {
	a.fix = append(a.fix, ppcFix{at: len(a.code), label: label, abs: true})
	a.e(0)
}

func (a *ppcAsm) bc(bo, bi uint32, label string) {
	a.fix = append(a.fix, ppcFix{at: len(a.code), label: label, bo: bo, bi: bi})
	a.e(0)
}

func (a *ppcAsm) beq(label string) { a.bc(12, 2, label) }
func (a *ppcAsm) bne(label string) { a.bc(4, 2, label) }

// li32 loads a 32 bit constant with lis plus ori.
func (a *ppcAsm) li32(rt int, v uint32) {
	a.e(d(15, uint32(rt), 0, v>>16))
	a.e(d(24, uint32(rt), uint32(rt), v&0xFFFF))
}

func (a *ppcAsm) words() []uint32 {
	for _, f := range a.fix {
		target, ok := a.labels[f.label]
		if !ok {
			panic("ppcAsm: unknown label " + f.label)
		}
		off := target - f.at
		if f.abs {
			a.code[f.at] = 18<<26 | (uint32(off)&0xFFFFFF)<<2
		} else {
			a.code[f.at] = 16<<26 | f.bo<<21 | f.bi<<16 | (uint32(off)&0x3FFF)<<2
		}
	}
	return a.code
}

// rlwinm encodes a rotate-left-word-immediate then AND
// with mask: the shape srwi and field extracts use.
func rlwinm(rs, ra, sh, mb, me uint32) uint32 {
	return 21<<26 | rs<<21 | ra<<16 | sh<<11 | mb<<6 | me<<1
}

// gxStreamGCN4 is the FIFO stream the DOL pushes: the
// audited CP and BP setup from the GCN-2 gate, a
// fullscreen quad in the clear color (20, 40, 200), then
// the flat center quad in (200, 30, 60). The pixel
// probes the gate asserts were measured against this
// core before the stream was baked into the fixture.
func gxStreamGCN4() []byte {
	var b bytes.Buffer
	cp := func(reg byte, v uint32) {
		b.WriteByte(0x08)
		b.WriteByte(reg)
		var w [4]byte
		binary.BigEndian.PutUint32(w[:], v)
		b.Write(w[:])
	}
	bp := func(reg byte, v uint32) {
		b.WriteByte(0x61)
		var w [4]byte
		binary.BigEndian.PutUint32(w[:], uint32(reg)<<24|(v&0xFFFFFF))
		b.Write(w[:])
	}
	cp(0x50, (1<<9)|(1<<13)) // VCD low: position direct, color 0 direct
	cp(0x60, 0)              // VCD high
	cp(0x70, 1|(4<<1)|(1<<13)|(5<<14))
	cp(0x80, 0)
	cp(0x90, 0)
	bp(0x43, 2) // PE control: RGBA8 pixel format
	bp(0x40, 0) // Z mode off
	bp(0x00, 0) // gen mode: no culling
	quad := func(half float32, r, g, bl byte) {
		b.WriteByte(0x80)
		var n [2]byte
		binary.BigEndian.PutUint16(n[:], 4)
		b.Write(n[:])
		verts := [][3]float32{
			{-half, half, 0}, {half, half, 0}, {half, -half, 0}, {-half, -half, 0},
		}
		for _, v := range verts {
			for _, f := range v {
				var w [4]byte
				binary.BigEndian.PutUint32(w[:], math.Float32bits(f))
				b.Write(w[:])
			}
			b.Write([]byte{r, g, bl, 255})
		}
	}
	quad(1.0, 20, 40, 200)
	quad(0.5, 200, 30, 60)
	return b.Bytes()
}

// mtspr builds a move-to-SPR word; the SPR number is
// encoded in bits 11 to 20 with its two 5 bit halves
// swapped, per the architecture.
func mtspr(spr, rs int) uint32 {
	enc := uint32((spr&0x1F)<<5 | (spr >> 5))
	return 31<<26 | uint32(rs)<<21 | enc<<11 | 467<<1
}

// engineProgram assembles the DOL. streamAddr and
// nameAddr are the effective addresses of the FIFO stream
// and the TESTDATA.BIN name literal inside data0.
func engineProgram(streamLen int, streamAddr, nameAddr uint32) []uint32 {
	a := newAsm()
	milestone := func(k int, flagOff uint32) {
		a.e(d(14, 27, 0, uint32(k))) // addi r27, r0, k
		a.e(d(36, 27, 31, flagOff))  // stw r27, flagOff(r31)
		a.e(d(36, 27, 30, 0))        // stw r27, 0(r30)
		a.e(d(14, 30, 30, 4))        // addi r30, r30, 4
	}
	badFlag := func(k int, flagOff uint32) {
		a.li32(27, 0xBAD00000|uint32(k))
		a.e(d(36, 27, 31, flagOff)) // stw r27, flagOff(r31)
	}

	// Setup: r29 = FST base (the boot argument in r3),
	// r31 = flags, r30 = log pointer; both areas zeroed.
	a.e(x(3, 29, 3, 444)) // or r29, r3, r3
	a.li32(31, engFlags)
	a.li32(30, engLog)
	for i := 0; i < 8; i++ {
		a.e(d(36, 0, 31, uint32(4*i))) // stw r0, flags
		a.e(d(36, 0, 30, uint32(4*i))) // stw r0, log (r30 advances later)
	}
	// The log stores above used r30 as base before it
	// starts advancing; reset it to the log base.
	a.li32(30, engLog)

	// The boot BATs map MEM1 only (DBAT0 at 0x80000000,
	// DBAT1 over the first 32 MB at 0). The hardware page
	// at 0x0C000000 is outside both, so the DOL maps it
	// itself with DBAT2: a 32 MB block, effective and
	// physical both 0x0C000000, supervisor read/write.
	a.li32(27, 0x0C0003FF)
	a.e(mtspr(540, 27)) // DBAT2U
	a.li32(27, 0x0C000002)
	a.e(mtspr(541, 27)) // DBAT2L

	// Stage 1: walk the FST from r29 for the one file,
	// verify its name, then DI DMA read and checksum it.
	a.e(d(32, 5, 29, 8))         // lwz r5, 8(r29): entry count
	a.e(rlwinm(5, 10, 2, 0, 29)) // slwi r10, r5, 2
	a.e(rlwinm(5, 9, 3, 0, 28))  // slwi r9, r5, 3
	a.e(x(10, 10, 9, 266))       // add r10, r10, r9 (count * 12)
	a.e(x(11, 29, 10, 266))      // add r11, r29, r10: string table base
	a.e(d(14, 6, 0, 1))          // addi r6, r0, 1 (entry index)
	a.e(d(14, 7, 29, 12))        // addi r7, r29, 12 (entry cursor)
	a.lab("fstloop")
	a.e(x(0, 6, 5, 0)) // cmpw r6, r5
	a.beq("fstfail")
	a.e(d(34, 8, 7, 0)) // lbz r8, 0(r7): entry type
	a.e(d(11, 0, 8, 0)) // cmpwi r8, 0
	a.bne("fstnext")
	a.e(d(32, 8, 7, 0))         // lwz r8, 0(r7): type and name offset
	a.e(rlwinm(8, 9, 0, 8, 31)) // extract name offset
	a.e(x(10, 11, 9, 266))      // add r10, r11, r9: name pointer
	a.li32(23, nameAddr)        // expected name literal
	a.e(d(14, 12, 0, 12))       // addi r12, r0, 12
	a.e(x(10, 13, 10, 444))     // or r13, r10, r10
	a.e(x(23, 14, 23, 444))     // or r14, r23, r23
	a.lab("namecmp")
	a.e(d(34, 15, 13, 0)) // lbz r15, 0(r13)
	a.e(d(34, 16, 14, 0)) // lbz r16, 0(r14)
	a.e(x(0, 15, 16, 0))  // cmpw r15, r16
	a.bne("fstnext")
	a.e(d(14, 13, 13, 1))
	a.e(d(14, 14, 14, 1))
	a.e(d(14, 12, 12, 0xFFFF)) // addi r12, r12, -1
	a.e(d(11, 0, 12, 0))       // cmpwi r12, 0
	a.bne("namecmp")
	a.e(d(32, 25, 7, 4)) // lwz r25, 4(r7): file disc offset
	a.e(d(32, 24, 7, 8)) // lwz r24, 8(r7): file size
	a.b("distart")
	a.lab("fstnext")
	a.e(d(14, 7, 7, 12))
	a.e(d(14, 6, 6, 1))
	a.b("fstloop")
	a.lab("fstfail")
	badFlag(1, 0)
	a.b("stage2")
	a.lab("distart")
	a.li32(28, 0x0C006000)         // DI window
	a.e(d(14, 27, 0, 0x10))        // TCINTMSK
	a.e(d(36, 27, 28, 0))          // stw DISR
	a.li32(27, 0xA8000000)         // read command
	a.e(d(36, 27, 28, 8))          // stw CMDBUF0
	a.e(rlwinm(25, 27, 30, 2, 31)) // srwi r27, r25, 2
	a.e(d(36, 27, 28, 12))         // stw CMDBUF1
	a.e(d(36, 24, 28, 16))         // stw CMDBUF2 = size
	a.li32(27, engDIDest)
	a.e(d(36, 27, 28, 24)) // stw DIMAR (physical)
	a.e(d(14, 27, 0, 3))   // TSTART | DMA
	a.e(d(36, 27, 28, 32)) // stw DICR
	a.lab("dipoll")
	a.e(d(32, 27, 28, 0))   // lwz r27, DISR
	a.e(d(28, 27, 27, 0x8)) // andi. r27, r27, TCINT
	a.beq("dipoll")
	a.li32(13, 0x80000000|engDIDest)
	a.e(d(14, 14, 0, 0))    // sum = 0
	a.e(x(24, 15, 24, 444)) // or r15, r24, r24 (count)
	a.lab("dick")
	a.e(d(34, 16, 13, 0)) // lbz r16, 0(r13)
	a.e(x(14, 14, 16, 266))
	a.e(d(14, 13, 13, 1))
	a.e(d(14, 15, 15, 0xFFFF))
	a.e(d(11, 0, 15, 0))
	a.bne("dick")
	a.li32(26, testDataSum())
	a.e(x(0, 14, 26, 0)) // cmpw sum, expected
	a.beq("s1ok")
	badFlag(2, 0)
	a.b("stage2")
	a.lab("s1ok")
	milestone(1, 0)

	// Stage 2: AI DMA of the sine table the DOL carries
	// in data1 at physical 0x8000; poll AIINT.
	a.lab("stage2")
	a.li32(28, 0x0C006C00) // AI window
	a.li32(27, 0x007F007F) // AIVR: full volume both sides
	a.e(d(36, 27, 28, 4))
	a.li32(27, engSinePhy)
	a.e(d(36, 27, 28, 32)) // DMA address lanes at +0x20
	a.e(d(14, 27, 0, engSineN*4))
	a.e(d(44, 27, 28, 38)) // sth lenL at +0x26 first
	a.li32(27, 0x00008000) // lenH with the enable bit
	a.e(d(44, 27, 28, 36)) // sth lenH at +0x24 starts the DMA
	a.e(d(14, 27, 0, 3))   // PSTAT | AIINTMSK
	a.e(d(36, 27, 28, 0))  // stw AICR
	a.lab("aipoll")
	a.e(d(32, 27, 28, 0))
	a.e(d(28, 27, 27, 0x4)) // AIINT
	a.beq("aipoll")
	milestone(2, 4)

	// Stage 3: SI poll of channel 0 into the buffer.
	a.li32(28, 0x0C006400) // SI window
	a.e(d(14, 27, 0, 1))
	a.e(d(36, 27, 28, 48)) // stw POLL = channel 0 enable
	a.li32(13, engSIBuf)
	a.e(d(32, 27, 28, 4))
	a.e(d(36, 27, 13, 0))
	a.e(d(32, 27, 28, 8))
	a.e(d(36, 27, 13, 4))
	milestone(3, 8)

	// Stage 4: memory card A over EXI channel 0.
	a.li32(28, 0x0C006800)
	exiSel := func() { a.li32(27, 0x100); a.e(d(36, 27, 28, 0)) }
	exiDesel := func() { a.e(d(36, 0, 28, 0)) }
	exiClear := func() { a.li32(27, 0x108); a.e(d(36, 27, 28, 0)) }
	exiPoll := func() {
		l := a.uniq("exipoll")
		a.lab(l)
		a.e(d(32, 27, 28, 0))
		a.e(d(28, 27, 27, 0x8)) // TCINT
		a.beq(l)
	}
	exiCmd := func(cmd uint32, cr uint32) {
		exiClear()
		if cmd == 0 {
			a.e(d(36, 0, 28, 16))
		} else {
			a.li32(27, cmd<<24)
			a.e(d(36, 27, 28, 16))
		}
		a.e(d(14, 27, 0, cr))
		a.e(d(36, 27, 28, 12))
		exiPoll()
	}
	// (a) Read ID: wakes the card; expect 0x1000 in the
	// top half of DATA (16 Mbit size byte, then zero).
	// The first transfer writes the one-byte ID command;
	// the second clocks the two ID bytes into DATA (its
	// DATA store is inert, only the CR write matters).
	exiSel()
	exiCmd(0x00, 0x9)  // write 1 byte: the ID command
	exiCmd(0x00, 0x11) // read 2 bytes into DATA
	a.e(d(32, 27, 28, 16))
	a.e(rlwinm(27, 27, 16, 16, 31)) // srwi r27, r27, 16
	a.e(d(11, 0, 27, 0x1000))
	a.beq("idok")
	badFlag(4, 12)
	a.b("stage5")
	a.lab("idok")
	exiDesel()
	// (b) Write one 512 byte page at card address 0 from
	// the pattern at physical 0xA000.
	exiSel()
	exiCmd(0xF2, 0x9)  // write page command, 1 byte
	exiCmd(0x00, 0x29) // address word, 4 bytes (DATA = 0)
	exiClear()
	a.li32(27, engCardPat)
	a.e(d(36, 27, 28, 4)) // MAR
	a.e(d(14, 27, 0, 512))
	a.e(d(36, 27, 28, 8)) // LENGTH
	a.e(d(14, 27, 0, 0xB))
	a.e(d(36, 27, 28, 12)) // CR: TSTART | DMA | write
	exiPoll()
	exiDesel()
	// (c) Read the page back to physical 0xB000.
	exiSel()
	exiCmd(0x52, 0x9)  // read command, 1 byte
	exiCmd(0x00, 0x29) // address word, 4 bytes
	exiClear()
	a.li32(27, engCardRB)
	a.e(d(36, 27, 28, 4))
	a.e(d(14, 27, 0, 512))
	a.e(d(36, 27, 28, 8))
	a.e(d(14, 27, 0, 0x3))
	a.e(d(36, 27, 28, 12)) // CR: TSTART | DMA | read
	exiPoll()
	exiDesel()
	// (d) Byte-compare pattern and readback.
	a.li32(13, 0x80000000|engCardPat)
	a.li32(14, 0x80000000|engCardRB)
	a.e(d(14, 15, 0, 512))
	a.lab("cardcmp")
	a.e(d(34, 16, 13, 0))
	a.e(d(34, 17, 14, 0))
	a.e(x(0, 16, 17, 0))
	a.bne("cardbad")
	a.e(d(14, 13, 13, 1))
	a.e(d(14, 14, 14, 1))
	a.e(d(14, 15, 15, 0xFFFF))
	a.e(d(11, 0, 15, 0))
	a.bne("cardcmp")
	milestone(4, 12)
	a.b("stage5")
	a.lab("cardbad")
	badFlag(5, 12)

	// Stage 5: push the GX FIFO stream byte by byte.
	a.lab("stage5")
	a.li32(13, streamAddr)
	a.e(d(14, 14, 0, uint32(streamLen)))
	a.li32(28, 0x0C008000) // GX FIFO port
	a.lab("gxloop")
	a.e(d(34, 27, 13, 0))
	a.e(d(38, 27, 28, 0))
	a.e(d(14, 13, 13, 1))
	a.e(d(14, 14, 14, 0xFFFF))
	a.e(d(11, 0, 14, 0))
	a.bne("gxloop")
	milestone(5, 16)

	// Spin on the VI current halfline: wait for it to
	// change, forever.
	a.li32(28, 0x0C002000)
	a.lab("spin")
	a.e(d(40, 27, 28, 16)) // lhz r27, VICURR
	a.lab("spinwait")
	a.e(d(40, 13, 28, 16))
	a.e(x(0, 13, 27, 0))
	a.beq("spinwait")
	a.b("spin")

	return a.words()
}

// buildEngineDisc builds the synthetic enginetest disc:
// header, bi2 and apploader stubs, the DOL (engineProgram
// plus the FIFO stream and name literal in data0), an FST
// with the single file TESTDATA.BIN, and the file bytes.
func buildEngineDisc() []byte {
	stream := gxStreamGCN4()
	nameLit := []byte("TESTDATA.BIN\x00")
	dataLen := len(stream)
	if dataLen%4 != 0 {
		dataLen += 4 - dataLen%4
	}
	nameOff := dataLen
	dataLen += len(nameLit)
	prog := engineProgram(len(stream), 0x80004000, 0x80004000+uint32(nameOff))

	data := make([]byte, 0x21000)
	data[0] = 'G'
	copy(data[1:], "GT")
	copy(data[3:], "01")
	copy(data[0x20:], "iTaK GoGCN Engine Test")
	put32(data, 0x1C, disc.GCMagic)
	put32(data, 0x420, 0x5000) // DOL offset
	// FST: root plus one file, string table "\0TESTDATA.BIN\0".
	fstSize := uint32(24 + 14)
	put32(data, 0x424, 0x6000)
	put32(data, 0x428, fstSize)
	put32(data, 0x444, 24<<20) // bi2 simulated memory size
	copy(data[0x2440:], "2026/10/13")
	fst := uint32(0x6000)
	data[fst] = 1 // root directory
	put32(data, fst+8, 2)
	data[fst+12+1] = 0
	data[fst+12+2] = 0
	data[fst+12+3] = 1 // file name offset 1 in the string table
	put32(data, fst+12+4, engTestOff)
	put32(data, fst+12+8, engTestLen)
	copy(data[fst+24:], "\x00TESTDATA.BIN\x00")
	for i := 0; i < engTestLen; i++ {
		data[engTestOff+i] = testDataByte(i)
	}

	dol := uint32(0x5000)
	put32(data, dol+0x00, 0x100)      // text0 offset
	put32(data, dol+0x48, 0x80002100) // text0 address
	put32(data, dol+0x90, uint32(4*len(prog)))
	put32(data, dol+0x1C, 0x2000) // data0 offset
	put32(data, dol+0x64, 0x80004000)
	put32(data, dol+0xAC, uint32(dataLen))
	// data1 is the AI sine table, so the disc is
	// self-contained: real homebrew carries its data in
	// the DOL instead of relying on the host to stage
	// memory. It lands at physical engSinePhy, which is
	// where the DOL points the AI DMA.
	put32(data, dol+0x20, 0x3000) // data1 offset
	put32(data, dol+0x68, 0x80000000|engSinePhy)
	put32(data, dol+0xB0, uint32(engSineN*4))
	put32(data, dol+0xD8, 0x8000C000) // BSS address
	put32(data, dol+0xDC, 0x100)      // BSS size
	put32(data, dol+0xE0, 0x80002100) // entry
	for i, w := range prog {
		put32(data, 0x5100+uint32(4*i), w)
	}
	copy(data[0x7000:], stream)
	copy(data[0x7000+uint32(nameOff):], nameLit)
	for i := 0; i < engSineN; i++ {
		phase := 2 * math.Pi * 440 * float64(i) / 48000
		s := uint16(int16(10000 * math.Sin(phase)))
		off := 0x8000 + uint32(i*4)
		data[off] = byte(s >> 8)
		data[off+1] = byte(s)
		data[off+2] = byte(s >> 8)
		data[off+3] = byte(s)
	}
	return data
}

// cardPattern is the EXI round-trip pattern the harness
// stages in MEM1.
func cardPattern(i int) byte { return byte(i ^ 0x5A) }

// engineSetup loads and boots the enginetest disc and
// stages the card pattern and the pad state. (The AI
// sine table ships inside the DOL as data1, so the
// disc runs identically through the libretro shell,
// where no test harness stages memory for it.)
func engineSetup(t *testing.T) *Machine {
	t.Helper()
	img, err := disc.Parse(buildEngineDisc())
	if err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.LoadDisc(img); err != nil {
		t.Fatal(err)
	}
	if err := m.BootDisc(); err != nil {
		t.Fatal(err)
	}
	if !m.Booted() {
		t.Fatal("machine did not report booted")
	}
	for i := 0; i < 512; i++ {
		m.Write8(engCardPat+uint32(i), cardPattern(i))
	}
	m.SetButtons(PadState{
		Buttons: PadA | PadStart, TriggerL: 3, TriggerR: 4,
		StickX: 200, StickY: 30, CStickX: 1, CStickY: 2,
	})
	return m
}

func TestGCN4HomebrewEnginetest(t *testing.T) {
	m := engineSetup(t)
	for i := 0; i < 25; i++ {
		m.RunFrame()
	}

	// Milestones, in order: the flag words carry the
	// stage numbers and the log records completion order.
	for k := 1; k <= 5; k++ {
		off := uint32(4 * (k - 1))
		if got := m.Read32(engFlags + off); got != uint32(k) {
			t.Fatalf("milestone flag %d = %#x, want %d", k, got, k)
		}
		if got := m.Read32(engLog + off); got != uint32(k) {
			t.Fatalf("milestone log %d = %#x, want %d", k, got, k)
		}
	}

	// Framebuffer: the flat quad color at the center, the
	// programmed clear color elsewhere.
	fb := m.Framebuffer()
	if px := fb[240*Width+320]; px != 0xFFC81E3C {
		t.Fatalf("center pixel = %#08x, want ffc81e3c (flat quad)", px)
	}
	for _, pt := range [][2]int{{0, 0}, {639, 479}, {10, 10}, {630, 20}} {
		if px := fb[pt[1]*Width+pt[0]]; px != 0xFF1428C8 {
			t.Fatalf("pixel (%d,%d) = %#08x, want ff1428c8 (clear color)", pt[0], pt[1], px)
		}
	}

	// Audio: the 440 Hz sine the AI DMA streamed.
	out := m.AudioBuffer()
	if len(out) < 2*engSineN {
		t.Fatalf("audio = %d samples, want at least %d", len(out), 2*engSineN)
	}
	crossings, peak := 0, 0
	for i := 0; i < engSineN; i++ {
		s := int(out[2*i])
		if s < 0 {
			s = -s
		}
		if s > peak {
			peak = s
		}
		if i > 0 && out[2*(i-1)] < 0 && out[2*i] >= 0 {
			crossings++
		}
	}
	if crossings < 8 || crossings > 9 {
		t.Fatalf("sine positive zero crossings = %d, want 8 or 9 (440 Hz)", crossings)
	}
	if peak < 5000 {
		t.Fatalf("sine peak = %d, want a non-silent stream", peak)
	}

	// DI: the exact TESTDATA.BIN bytes landed in MEM1.
	for i := 0; i < engTestLen; i++ {
		if got := m.Read8(0x80000000 + engDIDest + uint32(i)); got != testDataByte(i) {
			t.Fatalf("DI byte %d = %#x, want %#x", i, got, testDataByte(i))
		}
	}

	// EXI card: the pattern is in the card media itself
	// and in the readback buffer.
	card := m.CardImage(0)
	if len(card) < 512 {
		t.Fatalf("card image = %d bytes", len(card))
	}
	for i := 0; i < 512; i++ {
		if card[i] != cardPattern(i) {
			t.Fatalf("card byte %d = %#x, want %#x", i, card[i], cardPattern(i))
		}
		if got := m.Read8(0x80000000 + engCardRB + uint32(i)); got != cardPattern(i) {
			t.Fatalf("card readback byte %d = %#x", i, got)
		}
	}

	// SI: the polled pad matches the staged buttons.
	wantHi := uint32(PadA|PadStart)<<16 | 200<<8 | 30
	if got := m.Read32(engSIBuf); got != wantHi {
		t.Fatalf("SI buffer high = %#08x, want %#08x", got, wantHi)
	}
	wantLo := uint32(1)<<24 | uint32(2)<<16 | uint32(3)<<8 | 4
	if got := m.Read32(engSIBuf + 4); got != wantLo {
		t.Fatalf("SI buffer low = %#08x, want %#08x", got, wantLo)
	}
}

func TestGCN4Determinism(t *testing.T) {
	ref := engineSetup(t)
	for i := 0; i < 10; i++ {
		ref.RunFrame()
	}
	ref.AudioBuffer() // drain at the save point, both runs alike
	mid := ref.Serialize()
	if len(mid) != StateSize {
		t.Fatalf("state size = %d, want %d", len(mid), StateSize)
	}
	for i := 0; i < 10; i++ {
		ref.RunFrame()
	}
	stateRef := ref.Serialize()
	fbRef := append([]uint32(nil), ref.Framebuffer()...)
	audioRef := ref.AudioBuffer()

	m2 := engineSetup(t)
	for i := 0; i < 10; i++ {
		m2.RunFrame()
	}
	m2.AudioBuffer()
	if err := m2.Deserialize(mid); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		m2.RunFrame()
	}
	if !bytes.Equal(m2.Serialize(), stateRef) {
		t.Fatal("continuation after restore diverged from the uninterrupted run")
	}
	fb2 := m2.Framebuffer()
	for i := range fbRef {
		if fb2[i] != fbRef[i] {
			t.Fatalf("framebuffer pixel %d differs after restore", i)
		}
	}
	audio2 := m2.AudioBuffer()
	if len(audio2) != len(audioRef) {
		t.Fatalf("audio after restore = %d samples, want %d", len(audio2), len(audioRef))
	}
	for i := range audioRef {
		if audio2[i] != audioRef[i] {
			t.Fatalf("audio sample %d differs after restore: %d vs %d", i, audio2[i], audioRef[i])
		}
	}
}

// TestGCN4WriteDiscFixture writes the synthetic disc to
// the path in GCN4_DISC_OUT when set, so the shared
// enginetest harness can run the same disc through the
// built libretro shell. It skips when the variable is
// unset.
func TestGCN4WriteDiscFixture(t *testing.T) {
	path := os.Getenv("GCN4_DISC_OUT")
	if path == "" {
		t.Skip("GCN4_DISC_OUT not set")
	}
	if err := os.WriteFile(path, buildEngineDisc(), 0644); err != nil {
		t.Fatal(err)
	}
}
