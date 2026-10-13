// Package enginetest holds the PSP-4 enginetest fixture: a
// synthetic bootable UMD image whose guest exercises the whole
// core the way the go_backend enginetest harness drives it. The
// guest prints a known string through the kernel console,
// submits a GE display list (clear color plus one sprite),
// starts a sceAudio sine and keeps it playing with no input,
// polls sceCtrl and records the host buttons, writes milestone
// flags in order to a fixed RAM address, then loops on vblank
// waits forever. Everything is synthetic: the guest is hand
// assembled here and the ISO is built here; no game data is
// involved.
package enginetest

import (
	"encoding/binary"
	"math"
)

// Milestone block in guest RAM. The guest stores the count of
// completed milestones at OffProgress (written strictly in
// order, each value only ever replacing a smaller one), the
// last sceCtrl button mask it read at OffButtons, its completed
// blocking audio calls at OffAudioCalls, and its vblank count
// at OffVblanks.
const (
	MilestoneAddr = 0x08900000
	OffProgress   = 0
	OffButtons    = 4
	OffAudioCalls = 8
	OffVblanks    = 12
)

// Milestones, in the order the guest reaches them.
const (
	MilestoneConsole = 1 // printed ConsoleText
	MilestoneGE      = 2 // display list submitted and drawn
	MilestoneAudio   = 3 // first blocking audio call completed
	MilestoneCtrl    = 4 // first sceCtrl poll recorded
	MilestoneSustain = 5 // eight blocking audio calls completed
	MilestoneFinal   = MilestoneSustain
)

const (
	ConsoleText = "PSP4 ENGINE OK\n"
	GameTitle   = "PSP4 Engine Test"

	// The display list clears to this color and draws one
	// sprite of SpritePixel covering (8,8) to (16,12).
	ClearPixel  = 0xFF302010
	SpritePixel = 0xFF00FF00

	AudioBufFrames = 1024
	TonePeriod     = 100
	ToneAmp        = 12000
	ToneRate       = 44100
)

// ToneTable is the guest's waveform: one period of a cosine at
// amplitude ToneAmp, TonePeriod frames long (441 Hz at the
// output rate). The guest copies it into its play buffers with
// a running phase, so the tone is continuous across buffers.
func ToneTable() []int16 {
	tab := make([]int16, TonePeriod)
	for i := range tab {
		tab[i] = int16(math.Round(ToneAmp * math.Cos(2*math.Pi*float64(i)/TonePeriod)))
	}
	return tab
}

func toneTableBytes() []byte {
	tab := ToneTable()
	out := make([]byte, 2*len(tab))
	for i, v := range tab {
		out[2*i] = byte(v)
		out[2*i+1] = byte(v >> 8)
	}
	return out
}

func geCmd(op, arg uint32) uint32 { return op<<24 | (arg & 0xFFFFFF) }

// displayList is the guest's GE list; the ORIGIN and VADDR
// words depend on the vertex address the builder layout picks,
// so they are patched in after layout.
func displayList(vertsAddr uint32) []uint32 {
	const originBase = 0x08800000
	return []uint32{
		geCmd(0x14, originBase>>8),        // ORIGIN
		geCmd(0x9C, 0),                    // FRAMEBUFPTR (VRAM 0)
		geCmd(0x9D, 512),                  // FRAMEBUFWIDTH
		geCmd(0xE8, 3),                    // PIXELFORMAT = 8888
		geCmd(0x15, 0),                    // REGION1 (0,0)
		geCmd(0x16, 479|(271<<10)),        // REGION2 (479,271)
		geCmd(0xC9, 0x102030),             // CLEARCOLOR r=0x30 g=0x20 b=0x10
		geCmd(0xCA, 0xFF),                 // CLEARALPHA
		geCmd(0xCC, 0x7),                  // CLEAR enable|color|alpha
		geCmd(0x04, 3<<16|3),              // PRIM (clear)
		geCmd(0xCC, 0),                    // CLEAR mode off
		geCmd(0x12, 0x800113),             // VTYPE tc float, color 8888, pos s16, through
		geCmd(0x01, vertsAddr-originBase), // VADDR (origin relative)
		geCmd(0x04, 6<<16|2),              // PRIM sprite, 2 vertices
		geCmd(0x0F, 0),                    // FINISH
		geCmd(0x0C, 0),                    // END
	}
}

// spriteVerts: two through-mode vertices, stride 18 bytes (uv
// float pair, color word, position x/y/z as 12.4 fixed / raw z),
// the PSP-2 gate fixture's sprite.
func spriteVerts() []byte {
	out := make([]byte, 0, 36)
	put32 := func(v uint32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], v)
		out = append(out, b[:]...)
	}
	put16 := func(v uint16) {
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], v)
		out = append(out, b[:]...)
	}
	put32(0)          // u = 0.0
	put32(0)          // v = 0.0
	put32(0xFF00FF00) // opaque green
	put16(8 << 4)
	put16(8 << 4)
	put16(0)
	put32(0x3F800000) // u = 1.0
	put32(0x3F800000) // v = 1.0
	put32(0xFF00FF00)
	put16(16 << 4)
	put16(12 << 4)
	put16(0)
	return out
}

// milestone emits the max-store of n into the progress cell at
// 0(rS1): the value lands only if it is larger than what is
// there, which keeps the recorded order honest.
func milestone(a *asm, n int, tag string) {
	a.lw(rT0, 0, rS1)
	a.li(rT1, int32(n))
	a.slt(rT2, rT0, rT1)
	a.beq(rT2, rZero, "ms"+tag)
	a.nop()
	a.sw(rT1, 0, rS1)
	a.here("ms" + tag)
}

// fillBuffer emits the loop that fills the stereo play buffer
// in rS2 from the tone table, continuing the phase in rS4.
func fillBuffer(a *asm, tag string) {
	a.la(rS6, "tabE")
	a.li(rT0, 0)
	a.here("fill" + tag)
	a.addu(rT2, rS4, rT0)
	a.li(rT3, TonePeriod)
	a.divu(rT2, rT3)
	a.mfhi(rT4)
	a.sll(rT5, rT4, 1)
	a.addu(rT5, rS6, rT5)
	a.lh(rT6, 0, rT5)
	a.sll(rT7, rT0, 2)
	a.addu(rT7, rS2, rT7)
	a.sh(rT6, 0, rT7)
	a.sh(rT6, 2, rT7)
	a.addiu(rT0, rT0, 1)
	a.li(rT1, AudioBufFrames)
	a.bne(rT0, rT1, "fill"+tag)
	a.nop()
	a.addiu(rS4, rS4, AudioBufFrames)
}

// selectFill picks the play buffer by the parity in rS5 into
// rS2 and fills it.
func selectFill(a *asm, tag string) {
	a.la(rS2, "bufE0")
	a.beq(rS5, rZero, "sel"+tag)
	a.nop()
	a.la(rS2, "bufE1")
	a.here("sel" + tag)
	fillBuffer(a, tag)
}

// outputBlocking emits one sceAudioOutputBlocking on the
// channel in rS0 with the buffer in rS2 at unity volume, then
// counts the call into the milestone block.
func outputBlocking(a *asm) {
	a.move(rA0, rS0)
	a.li(rA1, 0x8000)
	a.move(rA2, rS2)
	a.jalStub(4, 1) // sceAudioOutputBlocking
	a.nop()
	a.addiu(rS3, rS3, 1)
	a.sw(rS3, OffAudioCalls, rS1)
	a.xori(rS5, rS5, 1)
}

// GuestELF builds the enginetest guest as an ET_EXEC ELF.
func GuestELF() []byte {
	libs := []stubLib{
		{"SysMemUserForUser", []uint32{0x13A5ABEF}},      // 0: sceKernelPrintf
		{"sceDisplay", []uint32{0x984C27E7}},             // 1: sceDisplayWaitVblankStart
		{"sceCtrl", []uint32{0x1F803938}},                // 2: sceCtrlReadBufferPositive
		{"sceGe_user", []uint32{0xAB49E76A, 0xB287BD61}}, // 3: sceGeListEnQueue, sceGeDrawSync
		{"sceAudio", []uint32{0x5EC81C55, 0x136CAF51}},   // 4: sceAudioChReserve, sceAudioOutputBlocking
	}
	b := newBuilder(libs)
	b.roStr("fmtE", ConsoleText)
	b.ro("tabE", toneTableBytes())
	b.roWords("listE", make([]uint32, 16)...) // patched below
	b.ro("vertsE", spriteVerts())
	b.ro("bufE0", make([]byte, AudioBufFrames*4))
	b.ro("bufE1", make([]byte, AudioBufFrames*4))
	b.ro("ctrlE", make([]byte, 32))

	a := b.code
	a.addiu(rSP, rSP, -32)
	// Milestone block base and a clean slate.
	a.li(rS1, MilestoneAddr)
	a.sw(rZero, OffProgress, rS1)
	a.sw(rZero, OffButtons, rS1)
	a.sw(rZero, OffAudioCalls, rS1)
	a.sw(rZero, OffVblanks, rS1)
	// Console print.
	a.la(rA0, "fmtE")
	a.jalStub(0, 0) // sceKernelPrintf
	a.nop()
	milestone(a, MilestoneConsole, "1")
	// Display list.
	a.la(rA0, "listE")
	a.li(rA1, 0)
	a.li(rA2, 0)
	a.li(rA3, 0)
	a.jalStub(3, 0) // sceGeListEnQueue
	a.nop()
	a.li(rA0, 0)
	a.jalStub(3, 1) // sceGeDrawSync
	a.nop()
	milestone(a, MilestoneGE, "2")
	// Audio: reserve and start the tone.
	a.addiu(rA0, rZero, -1)
	a.li(rA1, AudioBufFrames)
	a.li(rA2, 0)    // stereo
	a.jalStub(4, 0) // sceAudioChReserve
	a.nop()
	a.move(rS0, rV0)
	a.li(rS3, 0) // calls
	a.li(rS4, 0) // phase
	a.li(rS5, 0) // buffer parity
	a.li(rS7, 0) // vblanks
	selectFill(a, "first")
	outputBlocking(a)
	milestone(a, MilestoneAudio, "3")
	// Steady state: poll the pad, keep the tone playing, idle
	// on vblank waits. Nothing here needs input.
	a.here("loopE")
	a.la(rA0, "ctrlE")
	a.li(rA1, 1)
	a.jalStub(2, 0) // sceCtrlReadBufferPositive
	a.nop()
	a.la(rT0, "ctrlE")
	a.lw(rT1, 4, rT0) // SceCtrlData.Buttons
	a.sw(rT1, OffButtons, rS1)
	milestone(a, MilestoneCtrl, "4")
	selectFill(a, "loop")
	outputBlocking(a)
	a.li(rT0, 8)
	a.bne(rS3, rT0, "notFive")
	a.nop()
	milestone(a, MilestoneSustain, "5")
	a.here("notFive")
	a.jalStub(1, 0) // sceDisplayWaitVblankStart
	a.nop()
	a.addiu(rS7, rS7, 1)
	a.sw(rS7, OffVblanks, rS1)
	a.j("loopE")
	a.nop()

	// Patch the display list now that the layout (and so the
	// vertex address) is known; the list length does not
	// change, so every address stays put.
	_, roAddr, _, _ := b.layout()
	list := displayList(roAddr["vertsE"])
	buf := make([]byte, 4*len(list))
	for i, w := range list {
		binary.LittleEndian.PutUint32(buf[i*4:], w)
	}
	b.roData["listE"] = buf
	return b.build()
}

// --- ISO9660 image ------------------------------------------------

const sectorSize = 2048

func both16(dst []byte, v uint16) {
	binary.LittleEndian.PutUint16(dst[0:], v)
	binary.BigEndian.PutUint16(dst[2:], v)
}

func both32(dst []byte, v uint32) {
	binary.LittleEndian.PutUint32(dst[0:], v)
	binary.BigEndian.PutUint32(dst[4:], v)
}

func dirRecord(name []byte, extent, size uint32, dir bool) []byte {
	n := 33 + len(name)
	if n%2 == 1 {
		n++
	}
	rec := make([]byte, n)
	rec[0] = byte(n)
	both32(rec[2:], extent)
	both32(rec[10:], size)
	rec[18] = 125 // year 2025
	rec[19] = 10
	rec[20] = 13
	if dir {
		rec[25] = 2
	}
	both16(rec[28:], 1)
	rec[32] = byte(len(name))
	copy(rec[33:], name)
	return rec
}

func buildSFO() []byte {
	// One entry: TITLE (null terminated UTF-8).
	key := "TITLE"
	hdr := make([]byte, 20)
	hdr[1], hdr[2], hdr[3] = 'P', 'S', 'F'
	binary.LittleEndian.PutUint32(hdr[4:], 0x00000101)
	keyOff := 20 + 16
	dataOff := keyOff + len(key) + 1
	binary.LittleEndian.PutUint32(hdr[8:], uint32(keyOff))
	binary.LittleEndian.PutUint32(hdr[12:], uint32(dataOff))
	binary.LittleEndian.PutUint32(hdr[16:], 1)
	idx := make([]byte, 16)
	binary.LittleEndian.PutUint16(idx[2:], 0x0204)
	binary.LittleEndian.PutUint32(idx[4:], uint32(len(GameTitle)+1))
	binary.LittleEndian.PutUint32(idx[8:], uint32(len(GameTitle)+1))
	out := append(hdr, idx...)
	out = append(out, key...)
	out = append(out, 0)
	out = append(out, GameTitle...)
	out = append(out, 0)
	return out
}

// ISOImage builds the bootable UMD image: PSP_GAME/SYSDR/
// BOOT.BIN holding the guest ELF, plus PSP_GAME/PARAM.SFO.
func ISOImage() []byte {
	elf := GuestELF()
	sfo := buildSFO()
	elfSectors := (len(elf) + sectorSize - 1) / sectorSize

	const (
		pathTableSector = 18
		rootSector      = 20
		gameSector      = 21
		sysdrSector     = 22
		bootSector      = 23
	)
	sfoSector := bootSector + elfSectors
	total := sfoSector + 1

	img := make([]byte, total*sectorSize)
	// Path table: root, PSP_GAME, SYSDR.
	pt := img[pathTableSector*sectorSize:]
	off := 0
	putPT := func(name []byte, extent, parent uint32) {
		pt[off] = byte(len(name))
		both32(pt[off+2:], extent)
		binary.LittleEndian.PutUint16(pt[off+6:], uint16(parent))
		copy(pt[off+8:], name)
		n := 8 + len(name)
		if len(name)%2 == 1 {
			n++
		}
		off += n
	}
	putPT([]byte{0}, rootSector, 1)
	putPT([]byte("PSP_GAME"), gameSector, 1)
	putPT([]byte("SYSDR"), sysdrSector, 2)
	ptSize := off

	// Primary volume descriptor.
	pvd := img[16*sectorSize:]
	pvd[0] = 1
	copy(pvd[1:], "CD001")
	pvd[6] = 1
	for i := 40; i < 72; i++ {
		pvd[i] = ' '
	}
	copy(pvd[40:], "PSP4_ENGINE_TEST")
	both32(pvd[80:], uint32(total))
	both16(pvd[120:], 1)
	both16(pvd[124:], 1)
	both16(pvd[128:], sectorSize)
	both32(pvd[132:], uint32(ptSize))
	binary.LittleEndian.PutUint32(pvd[140:], pathTableSector)
	binary.BigEndian.PutUint32(pvd[148:], pathTableSector)
	copy(pvd[156:], dirRecord([]byte{0}, rootSector, sectorSize, true))
	// Terminator.
	term := img[17*sectorSize:]
	term[0] = 255
	copy(term[1:], "CD001")
	term[6] = 1

	writeDir := func(sector uint32, records [][]byte) {
		dst := img[sector*sectorSize:]
		o := 0
		for _, r := range records {
			copy(dst[o:], r)
			o += len(r)
		}
	}
	self := func(sector uint32) []byte { return dirRecord([]byte{0}, sector, sectorSize, true) }
	parent := func(sector uint32) []byte { return dirRecord([]byte{1}, sector, sectorSize, true) }
	writeDir(rootSector, [][]byte{
		self(rootSector), parent(rootSector),
		dirRecord([]byte("PSP_GAME"), gameSector, sectorSize, true),
	})
	writeDir(gameSector, [][]byte{
		self(gameSector), parent(rootSector),
		dirRecord([]byte("SYSDR"), sysdrSector, sectorSize, true),
		dirRecord([]byte("PARAM.SFO;1"), uint32(sfoSector), uint32(len(sfo)), false),
	})
	writeDir(sysdrSector, [][]byte{
		self(sysdrSector), parent(gameSector),
		dirRecord([]byte("BOOT.BIN;1"), bootSector, uint32(len(elf)), false),
	})
	copy(img[bootSector*sectorSize:], elf)
	copy(img[sfoSector*sectorSize:], sfo)
	return img
}
