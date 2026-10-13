package system

import (
	"encoding/binary"
	"math"
	"os"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-n64/cart"
	"github.com/davidpatton/itak-gaming/cores/go-n64/chips/r4300"
)

// N64-4 swap-class gate: a synthetic ROM whose
// hand-written IPL3 runs the documented boot chain
// (PIF post-boot state, IPL3 from DMEM, game entry),
// after which the game payload exercises the RDP, the
// AI, the PIF/SI controller path, a Controller Pak
// round trip, and the cartridge EEPROM, setting
// milestone flags in RDRAM as it goes. The gate boots
// only through LoadROM + Start: no seeding.
//
// Milestone and detail words live in RDRAM at 0x300:
//   0x310 DP interrupt count    0x318 AI interrupt count
//   0x31C SI interrupt count
//   0x320..0x334 milestone flags 1..6, set in order
//   0x340/0x344 controller reply words from the poll
//   0x348 EEPROM check (0x600D ok, 0x0BADxxxx fail)
//   0x34C pak check (0x600D ok, 0x0BAD fail)
//   0x350 spin loop counter
//   0x354 ch4 info check (0x600D ok, else the word seen)
//   0x358 pak info status byte (3 while fresh, then 1)
//   0x360/0x364 video/audio wait timeout details
//   0x3F0 the CIC seed byte recorded from $s6 at entry

// Extra register numbers for the guest assembler.
const (
	at = 1
	v0 = 2
	a0 = 4
	a1 = 5
	a2 = 6
	a3 = 7
	s3 = 19
	s4 = 20
	s5 = 21
	s6 = 22
	s7 = 23
	t8 = 24
	t9 = 25
	k0 = 26
	k1 = 27
	sp = 29
	ra = 31
)

// guestAsm is a tiny two-pass MIPS assembler for the
// fixture code: instructions are emitted with the
// package's encoding helpers, and branch/jump targets
// are named and patched in finish.
type guestAsm struct {
	base   uint32
	code   []uint32
	labels map[string]int
	fix    []guestFix
}

type guestFix struct {
	at   int
	name string
	jump bool
}

func newGuestAsm(base uint32) *guestAsm {
	return &guestAsm{base: base, labels: map[string]int{}}
}

func (a *guestAsm) mark(name string) { a.labels[name] = len(a.code) }

func (a *guestAsm) emit(w uint32) { a.code = append(a.code, w) }

func (a *guestAsm) branch(op, rs, rt uint32, name string) {
	a.fix = append(a.fix, guestFix{at: len(a.code), name: name})
	a.emit(op<<26 | rs<<21 | rt<<16)
}

func (a *guestAsm) beq(rs, rt uint32, name string) { a.branch(0x04, rs, rt, name) }
func (a *guestAsm) bne(rs, rt uint32, name string) { a.branch(0x05, rs, rt, name) }

func (a *guestAsm) jump(op uint32, name string) {
	a.fix = append(a.fix, guestFix{at: len(a.code), name: name, jump: true})
	a.emit(op << 26)
}

func (a *guestAsm) j(name string)   { a.jump(0x02, name) }
func (a *guestAsm) jal(name string) { a.jump(0x03, name) }

func (a *guestAsm) finish() []uint32 {
	for _, f := range a.fix {
		target, ok := a.labels[f.name]
		if !ok {
			panic("guestAsm: unknown label " + f.name)
		}
		if f.jump {
			addr := (a.base + uint32(4*target)) >> 2 & 0x3FFFFFF
			a.code[f.at] |= addr
		} else {
			off := int16(target - (f.at + 1))
			a.code[f.at] |= uint32(uint16(off))
		}
	}
	return a.code
}

// sb, lbu, srl, sll, addu, and, xor encodings used by
// the fixture (the rest come from the package helpers).
func sb(rt, rs uint32, off int16) uint32  { return ii(0x28, rs, rt, uint16(off)) }
func lbu(rt, rs uint32, off int16) uint32 { return ii(0x24, rs, rt, uint16(off)) }
func andi(rt, rs uint32, imm uint16) uint32 {
	return ii(0x0C, rs, rt, imm)
}
func xori(rt, rs uint32, imm uint16) uint32 {
	return ii(0x0E, rs, rt, imm)
}
func srl(rd, rt uint32, sa uint32) uint32 { return rr(0, rt, rd, sa, 0x02) }
func sll(rd, rt uint32, sa uint32) uint32 { return rr(0, rt, rd, sa, 0x00) }
func addu(rd, rs, rt uint32) uint32       { return rr(rs, rt, rd, 0, 0x21) }
func and_(rd, rs, rt uint32) uint32       { return rr(rs, rt, rd, 0, 0x24) }
func xor_(rd, rs, rt uint32) uint32       { return rr(rs, rt, rd, 0, 0x26) }
func jr(rs uint32) uint32                 { return rr(rs, 0, 0, 0, 0x08) }

// Payload image layout (offsets within the 0x2000-byte
// payload the IPL3 copies to the entry address).
const (
	imgHandlerOff = 0x800
	imgListOff    = 0x900
	imgSineOff    = 0x1000
	imgPayloadLen = 0x2000
	imgEntryAddr  = 0x80000400
)

// engineIPL3 assembles the fixture's IPL3: it reads the
// entry address and the payload length from the header
// copy in DMEM (offsets 0x08 and 0x18), copies the
// payload from the cartridge window to the entry
// address, and jumps to it. This is the documented
// IPL3 job, in original code.
func engineIPL3() []uint32 {
	a := newGuestAsm(0xA4000040)
	a.emit(lui(t0, 0xA400))     // DMEM base
	a.emit(lw(t1, t0, 8))       // entry address
	a.emit(lw(t2, t0, 0x18))    // payload length
	a.emit(lui(t3, 0xB000))     //
	a.emit(ori(t3, t3, 0x1000)) // src = cart 0x1000 (kseg1)
	a.emit(addu(t4, t1, 0))     // dst = entry
	a.mark("copy")
	a.emit(lw(t5, t3, 0))
	a.emit(sw(t5, t4, 0))
	a.emit(addiu(t3, t3, 4))
	a.emit(addiu(t4, t4, 4))
	a.emit(addiu(t2, t2, -4))
	a.bne(t2, 0, "copy")
	a.emit(0)
	a.emit(jr(t1))
	a.emit(0)
	return a.finish()
}

// engineHandler assembles the interrupt handler the
// payload plants at RDRAM 0x180: it counts DP, AI, and
// SI interrupts (at 0x310/0x318/0x31C) and acknowledges
// each at its source.
func engineHandler() []uint32 {
	a := newGuestAsm(0x80000180)
	count := func(tag string, intrBit uint16, counterOff int16, ack func()) {
		a.emit(lui(k0, 0xA430)) // MI page
		a.emit(lw(k1, k0, 8))   // MI_INTR
		a.emit(andi(at, k1, intrBit))
		a.beq(at, 0, "next"+tag)
		a.emit(0)
		a.emit(lui(at, 0x8000))
		a.emit(lw(k1, at, counterOff))
		a.emit(addiu(k1, k1, 1))
		a.emit(sw(k1, at, counterOff))
		ack()
		a.mark("next" + tag)
	}
	count("dp", 0x20, 0x310, func() { // DP: clear via MI_MODE bit 11
		a.emit(ori(k1, 0, 0x800))
		a.emit(sw(k1, k0, 0))
	})
	count("ai", 0x04, 0x318, func() { // AI: status write
		a.emit(lui(k0, 0xA450))
		a.emit(sw(0, k0, 0xC))
	})
	count("si", 0x02, 0x31C, func() { // SI: status write
		a.emit(lui(k0, 0xA480))
		a.emit(sw(0, k0, 0x1C))
	})
	a.emit(0x42000018) // eret
	return a.finish()
}

// engineROM builds the synthetic cartridge: header with
// the fixture payload length at 0x18, the assembled
// IPL3 at 0x40, and the payload image at 0x1000. The
// header checksum words are filled with the published
// IPL3 checksum for the 6102 family so the boot's
// report-only verification passes.
func engineROM(t *testing.T) []byte {
	t.Helper()
	handler := engineHandler()
	payload := enginePayload(t, len(handler))
	// The handler words live inside the payload image.
	for i, w := range handler {
		binary.BigEndian.PutUint32(payload[imgHandlerOff+i*4:], w)
	}
	rom := make([]byte, 0x101000)
	binary.BigEndian.PutUint32(rom[0x00:], 0x80371240)
	binary.BigEndian.PutUint32(rom[0x08:], imgEntryAddr)
	binary.BigEndian.PutUint32(rom[0x18:], imgPayloadLen)
	copy(rom[0x20:], "GON64 ENGINE TEST   ")
	rom[0x3B] = 'N'
	rom[0x3E] = 'E'
	for i, w := range engineIPL3() {
		binary.BigEndian.PutUint32(rom[0x40+i*4:], w)
	}
	copy(rom[0x1000:], payload)
	c1, c2, ok := cart.IPL3Checksum(rom, cart.CIC6102)
	if !ok {
		t.Fatal("checksum not computable")
	}
	binary.BigEndian.PutUint32(rom[0x10:], c1)
	binary.BigEndian.PutUint32(rom[0x14:], c2)
	return rom
}

// enginePayload assembles the game program and lays out
// the payload image: code at 0, the display list (the
// N64-3 gate scene) at imgListOff, the sine table at
// imgSineOff. The interrupt handler words are patched
// in by engineROM at imgHandlerOff.
func enginePayload(t *testing.T, handlerWords int) []byte {
	t.Helper()
	list := sceneList()
	listBytes := len(list) * 8
	a := newGuestAsm(imgEntryAddr)

	// ---- entry and setup ----
	a.emit(lui(sp, 0x8008))     // sp = 0x80080000
	a.emit(lui(s7, 0x8000))     // s7 = RDRAM base (kseg0)
	a.emit(sw(s6, s7, 0x3F0))   // record the CIC seed from $s6
	a.emit(lui(t8, 0x8000))     //
	a.emit(ori(t8, t8, 0x9000)) // t8 = PIF frame block 0x80009000
	// Plant the interrupt handler at 0x180.
	a.emit(lui(t0, 0x8000))
	a.emit(ori(t0, t0, 0x0400+imgHandlerOff)) // src = image handler
	a.emit(lui(t1, 0x8000))
	a.emit(ori(t1, t1, 0x0180)) // dst = vector area
	a.emit(addiu(t2, 0, int16(handlerWords)))
	a.mark("copyHandler")
	a.emit(lw(t3, t0, 0))
	a.emit(sw(t3, t1, 0))
	a.emit(addiu(t0, t0, 4))
	a.emit(addiu(t1, t1, 4))
	a.emit(addiu(t2, t2, -1))
	a.bne(t2, 0, "copyHandler")
	a.emit(0)
	// Enable interrupts: Status CU0|CU1|IM2|IE.
	a.emit(lui(t0, 0x3000))
	a.emit(ori(t0, t0, 0x0401))
	a.emit(mtc0(t0, r4300.Cp0Status))
	// MI_MASK: unmask DP, AI, SI.
	a.emit(lui(t0, 0xA430))
	a.emit(ori(t1, 0, 0x828))
	a.emit(sw(t1, t0, 0xC))
	// Milestone 0: boot reached the payload.
	a.emit(addiu(t1, 0, 1))
	a.emit(sw(t1, s7, 0x320))
	// Terminate the PIF boot (command byte bit 0x08).
	a.emit(lui(t0, 0xBFC0))
	a.emit(ori(t0, t0, 0x07FF))
	a.emit(addiu(t1, 0, 8))
	a.emit(sb(t1, t0, 0))

	// ---- milestone 1: VI + display list ----
	a.emit(lui(s0, 0xA440)) // VI page
	a.emit(addiu(t1, 0, 2))
	a.emit(sw(t1, s0, 0x00)) // CTRL: 16-bit
	a.emit(lui(t1, 0x0010))
	a.emit(sw(t1, s0, 0x04)) // ORIGIN 0x100000
	a.emit(addiu(t1, 0, 320))
	a.emit(sw(t1, s0, 0x08)) // WIDTH
	a.emit(addiu(t1, 0, 2))
	a.emit(sw(t1, s0, 0x0C)) // V_INTR
	a.emit(addiu(t1, 0, 524))
	a.emit(sw(t1, s0, 0x18)) // V_TOTAL
	a.emit(addiu(t1, 0, 320))
	a.emit(sw(t1, s0, 0x24)) // H_VIDEO
	a.emit(addiu(t1, 0, 480))
	a.emit(sw(t1, s0, 0x28)) // V_VIDEO
	a.emit(addiu(t1, 0, 0x400))
	a.emit(sw(t1, s0, 0x30)) // X_SCALE
	a.emit(sw(t1, s0, 0x34)) // Y_SCALE
	a.emit(lui(s0, 0xA410))  // DP page
	a.emit(addiu(t1, 0, 0x400+imgListOff))
	a.emit(sw(t1, s0, 0)) // DP_START
	a.emit(addiu(t1, 0, 0x400+imgListOff+int16(listBytes)))
	a.emit(sw(t1, s0, 4))   // DP_END: execute
	a.emit(lui(t2, 0x0010)) // wait cap: 1M polls
	a.mark("waitDP")
	a.emit(lw(t0, s7, 0x310))
	a.bne(t0, 0, "dpDone")
	a.emit(0)
	a.emit(addiu(t2, t2, -1))
	a.bne(t2, 0, "waitDP")
	a.emit(0)
	a.emit(lui(t1, 0x0BAD))
	a.emit(sw(t1, s7, 0x360)) // timeout detail
	a.j("afterVideo")
	a.emit(0)
	a.mark("dpDone")
	a.emit(addiu(t1, 0, 2))
	a.emit(sw(t1, s7, 0x324)) // milestone 1
	a.mark("afterVideo")

	// ---- milestone 2: AI sine ----
	// Fill the buffer at 0x50000 from the 32-entry table.
	a.emit(lui(t0, 0x8005)) // dst = 0x80050000
	a.emit(lui(t1, 0x8000))
	a.emit(ori(t1, t1, 0x0400+imgSineOff)) // table in image
	a.emit(addiu(t2, 0, 12800))
	a.mark("fillSine")
	a.emit(addiu(t4, 0, 31))
	a.emit(and_(t3, t2, t4))
	a.emit(sll(t3, t3, 2))
	a.emit(addu(t5, t1, t3))
	a.emit(lw(t6, t5, 0))
	a.emit(sw(t6, t0, 0))
	a.emit(addiu(t0, t0, 4))
	a.emit(addiu(t2, t2, -1))
	a.bne(t2, 0, "fillSine")
	a.emit(0)
	a.emit(lui(s0, 0xA450)) // AI page
	a.emit(addiu(t1, 0, 1520))
	a.emit(sw(t1, s0, 0x10)) // DACRATE (32006 Hz)
	a.emit(addiu(t1, 0, 15))
	a.emit(sw(t1, s0, 0x14)) // BITRATE
	a.emit(lui(t1, 0x0005))
	a.emit(sw(t1, s0, 0x00)) // DRAM_ADDR 0x50000
	a.emit(addiu(t1, 0, 1))
	a.emit(sw(t1, s0, 0x08)) // CONTROL: enable
	a.emit(ori(t1, 0, 0xC800))
	a.emit(sw(t1, s0, 0x04)) // LENGTH 51200: starts
	a.emit(lui(t2, 0x0010))
	a.mark("waitAI")
	a.emit(lw(t0, s7, 0x318))
	a.bne(t0, 0, "aiDone")
	a.emit(0)
	a.emit(addiu(t2, t2, -1))
	a.bne(t2, 0, "waitAI")
	a.emit(0)
	a.emit(lui(t1, 0x0BAD))
	a.emit(sw(t1, s7, 0x364)) // timeout detail
	a.j("afterAudio")
	a.emit(0)
	a.mark("aiDone")
	a.emit(addiu(t1, 0, 3))
	a.emit(sw(t1, s7, 0x328)) // milestone 2
	a.mark("afterAudio")

	// ---- milestone 3: controller poll + cart info ----
	a.jal("pollPad")
	a.emit(0)
	a.emit(lw(t0, t8, 0))
	a.emit(sw(t0, s7, 0x340)) // reply word 0 (TX/RX/cmd/button hi)
	a.emit(lw(t0, t8, 4))
	a.emit(sw(t0, s7, 0x344)) // reply word 1 (button lo/stick)
	a.emit(lw(t0, t8, 12))
	a.emit(ori(t1, 0, 0x8000))
	a.bne(t0, t1, "infoBad")
	a.emit(0)
	a.emit(addiu(t1, 0, 0x600D))
	a.emit(sw(t1, s7, 0x354))
	a.j("infoDone")
	a.emit(0)
	a.mark("infoBad")
	a.emit(sw(t0, s7, 0x354)) // the word actually seen
	a.mark("infoDone")
	a.emit(addiu(t1, 0, 4))
	a.emit(sw(t1, s7, 0x32C)) // milestone 3

	// ---- milestone 4: controller pak round trip ----
	// Info on channel 0 first: a fresh pak behaves as
	// absent until an Info reports status 3.
	a.jal("zeroFrame")
	a.emit(0)
	a.emit(addiu(t1, 0, 1))
	a.emit(sb(t1, t8, 0)) // TX
	a.emit(addiu(t1, 0, 3))
	a.emit(sb(t1, t8, 1)) // RX
	a.emit(addiu(t1, 0, 0xFF))
	a.emit(sb(t1, t8, 2)) // cmd info
	a.emit(addiu(t1, 0, 1))
	a.emit(sb(t1, t8, 0x3F)) // command byte
	a.jal("siExchange")
	a.emit(0)
	a.emit(lw(t0, t8, 4))
	a.emit(srl(t0, t0, 16))
	a.emit(andi(t0, t0, 0xFF))
	a.emit(sw(t0, s7, 0x358)) // pak info status byte
	// Write 32 pattern bytes to pak address 0x0020 (the
	// encoded address carries its checksum: 0x0035).
	a.jal("zeroFrame")
	a.emit(0)
	a.emit(addiu(t1, 0, 0x23))
	a.emit(sb(t1, t8, 0)) // TX = 35
	a.emit(addiu(t1, 0, 1))
	a.emit(sb(t1, t8, 1)) // RX = 1 (the CRC)
	a.emit(addiu(t1, 0, 3))
	a.emit(sb(t1, t8, 2)) // cmd write
	a.emit(addiu(t1, 0, 0x35))
	a.emit(sb(t1, t8, 4)) // address low byte (high is 0)
	a.emit(addiu(t2, 0, 32))
	a.emit(addu(t0, t8, 0))
	a.emit(addiu(t0, t0, 5))
	a.emit(addiu(t3, 0, 0))
	a.mark("pakPat")
	a.emit(sll(t4, t3, 1))
	a.emit(addu(t4, t4, t3))
	a.emit(addiu(t4, t4, 1)) // value = i*3+1
	a.emit(sb(t4, t0, 0))
	a.emit(addiu(t0, t0, 1))
	a.emit(addiu(t3, t3, 1))
	a.emit(addiu(t2, t2, -1))
	a.bne(t2, 0, "pakPat")
	a.emit(0)
	a.emit(addiu(t1, 0, 1))
	a.emit(sb(t1, t8, 0x3F))
	a.jal("siExchange")
	a.emit(0)
	// Read the block back.
	a.jal("zeroFrame")
	a.emit(0)
	a.emit(addiu(t1, 0, 3))
	a.emit(sb(t1, t8, 0)) // TX = 3
	a.emit(addiu(t1, 0, 0x21))
	a.emit(sb(t1, t8, 1)) // RX = 33 (data + CRC)
	a.emit(addiu(t1, 0, 2))
	a.emit(sb(t1, t8, 2)) // cmd read
	a.emit(addiu(t1, 0, 0x35))
	a.emit(sb(t1, t8, 4))
	a.emit(addiu(t1, 0, 1))
	a.emit(sb(t1, t8, 0x3F))
	a.jal("siExchange")
	a.emit(0)
	// Verify data against the pattern while accumulating
	// the documented Joybus CRC-8 (poly 0x85).
	a.emit(addiu(t2, 0, 32))
	a.emit(addu(t0, t8, 0))
	a.emit(addiu(t0, t0, 5))
	a.emit(addiu(t3, 0, 0))
	a.emit(addiu(s1, 0, 0)) // crc
	a.mark("pakVerify")
	a.emit(lbu(t4, t0, 0))
	a.emit(sll(t5, t3, 1))
	a.emit(addu(t5, t5, t3))
	a.emit(addiu(t5, t5, 1))
	a.bne(t4, t5, "pakFail")
	a.emit(0)
	a.emit(xor_(s1, s1, t4))
	a.emit(addiu(t6, 0, 8))
	a.mark("pakCRCbit")
	a.emit(andi(t5, s1, 0x80))
	a.emit(sll(s1, s1, 1))
	a.beq(t5, 0, "pakCRCnoFold")
	a.emit(0)
	a.emit(xori(s1, s1, 0x85))
	a.mark("pakCRCnoFold")
	a.emit(addiu(t6, t6, -1))
	a.bne(t6, 0, "pakCRCbit")
	a.emit(0)
	a.emit(andi(s1, s1, 0xFF))
	a.emit(addiu(t0, t0, 1))
	a.emit(addiu(t3, t3, 1))
	a.emit(addiu(t2, t2, -1))
	a.bne(t2, 0, "pakVerify")
	a.emit(0)
	a.emit(lbu(t4, t8, 37)) // the CRC byte after the data
	a.bne(t4, s1, "pakFail")
	a.emit(0)
	a.emit(addiu(t1, 0, 0x600D))
	a.emit(sw(t1, s7, 0x34C))
	a.emit(addiu(t1, 0, 5))
	a.emit(sw(t1, s7, 0x330)) // milestone 4
	a.j("pakDone")
	a.emit(0)
	a.mark("pakFail")
	a.emit(lui(t1, 0x0BAD))
	a.emit(sw(t1, s7, 0x34C))
	a.mark("pakDone")

	// ---- milestone 5: EEPROM block round trip ----
	a.jal("zeroFrame")
	a.emit(0)
	a.emit(addiu(t1, 0, 0x0A))
	a.emit(sb(t1, t8, 4)) // ch4 TX = 10
	a.emit(addiu(t1, 0, 1))
	a.emit(sb(t1, t8, 5)) // RX = 1
	a.emit(addiu(t1, 0, 5))
	a.emit(sb(t1, t8, 6)) // cmd write
	a.emit(sb(t1, t8, 7)) // block 5
	a.emit(addiu(t2, 0, 8))
	a.emit(addu(t0, t8, 0))
	a.emit(addiu(t0, t0, 8))
	a.emit(addiu(t3, 0, 0))
	a.mark("eePat")
	a.emit(addiu(t4, t3, 0xA0)) // value = 0xA0 + i
	a.emit(sb(t4, t0, 0))
	a.emit(addiu(t0, t0, 1))
	a.emit(addiu(t3, t3, 1))
	a.emit(addiu(t2, t2, -1))
	a.bne(t2, 0, "eePat")
	a.emit(0)
	a.emit(addiu(t1, 0, 1))
	a.emit(sb(t1, t8, 0x3F))
	a.jal("siExchange")
	a.emit(0)
	a.jal("zeroFrame")
	a.emit(0)
	a.emit(addiu(t1, 0, 2))
	a.emit(sb(t1, t8, 4)) // TX = 2
	a.emit(addiu(t1, 0, 8))
	a.emit(sb(t1, t8, 5)) // RX = 8
	a.emit(addiu(t1, 0, 4))
	a.emit(sb(t1, t8, 6)) // cmd read
	a.emit(addiu(t1, 0, 5))
	a.emit(sb(t1, t8, 7)) // block 5
	a.emit(addiu(t1, 0, 1))
	a.emit(sb(t1, t8, 0x3F))
	a.jal("siExchange")
	a.emit(0)
	a.emit(addiu(t2, 0, 8))
	a.emit(addu(t0, t8, 0))
	a.emit(addiu(t0, t0, 8))
	a.emit(addiu(t3, 0, 0))
	a.mark("eeVerify")
	a.emit(lbu(t4, t0, 0))
	a.emit(addiu(t5, t3, 0xA0))
	a.bne(t4, t5, "eeFail")
	a.emit(0)
	a.emit(addiu(t0, t0, 1))
	a.emit(addiu(t3, t3, 1))
	a.emit(addiu(t2, t2, -1))
	a.bne(t2, 0, "eeVerify")
	a.emit(0)
	a.emit(addiu(t1, 0, 0x600D))
	a.emit(sw(t1, s7, 0x348))
	a.emit(addiu(t1, 0, 6))
	a.emit(sw(t1, s7, 0x334)) // milestone 5
	a.j("eeDone")
	a.emit(0)
	a.mark("eeFail")
	a.emit(lui(t1, 0x0BAD))
	a.emit(sw(t1, s7, 0x348))
	a.mark("eeDone")

	// ---- spin: count loops, keep the AI fed, and keep
	// polling the pad ----
	a.mark("spin")
	a.emit(lw(t0, s7, 0x350))
	a.emit(addiu(t0, t0, 1))
	a.emit(sw(t0, s7, 0x350))
	a.emit(lui(t0, 0xA450))
	a.emit(lw(t1, t0, 4)) // AI_LENGTH: 0 means drained
	a.bne(t1, 0, "spinPolled")
	a.emit(0)
	a.emit(lui(t1, 0x0005))
	a.emit(sw(t1, t0, 0)) // DRAM_ADDR
	a.emit(ori(t1, 0, 0xC800))
	a.emit(sw(t1, t0, 4)) // LENGTH: restart the sine
	a.mark("spinPolled")
	a.jal("pollPad")
	a.emit(0)
	a.j("spin")
	a.emit(0)

	// ---- subroutines ----
	a.mark("zeroFrame")
	a.emit(addiu(t2, 0, 16))
	a.emit(addu(t0, t8, 0))
	a.mark("zfLoop")
	a.emit(sw(0, t0, 0))
	a.emit(addiu(t0, t0, 4))
	a.emit(addiu(t2, t2, -1))
	a.bne(t2, 0, "zfLoop")
	a.emit(0)
	a.emit(jr(ra))
	a.emit(0)

	a.mark("siExchange")
	a.emit(lui(t0, 0xA480)) // SI page
	a.emit(ori(t1, 0, 0x9000))
	a.emit(sw(t1, t0, 0)) // SI_DRAM_ADDR
	a.emit(lui(t1, 0x1FC0))
	a.emit(ori(t1, t1, 0x07C0)) // PIF RAM address
	a.emit(sw(t1, t0, 0x10))    // WR64B: frame to PIF (parses)
	a.emit(sw(t1, t0, 0x04))    // RD64B: execute + read back
	a.emit(jr(ra))
	a.emit(0)

	a.mark("pollPad")
	a.emit(addiu(sp, sp, -8))
	a.emit(sw(ra, sp, 4))
	a.jal("zeroFrame")
	a.emit(0)
	a.emit(addiu(t1, 0, 1))
	a.emit(sb(t1, t8, 0)) // ch0 TX = 1
	a.emit(addiu(t1, 0, 4))
	a.emit(sb(t1, t8, 1)) // ch0 RX = 4
	a.emit(addiu(t1, 0, 1))
	a.emit(sb(t1, t8, 2)) // cmd: read state
	// Channels 1..3 stay zeroed (skip), channel 4 asks
	// for its info signature.
	a.emit(addiu(t1, 0, 1))
	a.emit(sb(t1, t8, 10)) // ch4 TX = 1
	a.emit(addiu(t1, 0, 3))
	a.emit(sb(t1, t8, 11)) // ch4 RX = 3
	a.emit(addiu(t1, 0, 1))
	a.emit(sb(t1, t8, 0x3F)) // command byte
	a.jal("siExchange")
	a.emit(0)
	a.emit(lw(ra, sp, 4))
	a.emit(addiu(sp, sp, 8))
	a.emit(jr(ra))
	a.emit(0)

	code := a.finish()
	if len(code)*4 > imgHandlerOff {
		t.Fatalf("payload code is %d bytes, over the %d budget", len(code)*4, imgHandlerOff)
	}
	if listBytes > imgSineOff-imgListOff {
		t.Fatalf("display list is %d bytes, over budget", listBytes)
	}
	img := make([]byte, imgPayloadLen)
	for i, w := range code {
		binary.BigEndian.PutUint32(img[i*4:], w)
	}
	for i, w := range list {
		binary.BigEndian.PutUint64(img[imgListOff+i*8:], w)
	}
	for i := 0; i < 32; i++ {
		v := int16(12000 * math.Sin(2*math.Pi*float64(i)/32))
		binary.BigEndian.PutUint32(img[imgSineOff+i*4:], uint32(uint16(v))<<16|uint32(uint16(v)))
	}
	return img
}

// TestEngineBootGate is the N64-4 swap gate. It boots
// the synthetic ROM through LoadROM + Start only, and
// asserts every milestone in order, the video and
// audio evidence, the pak and EEPROM internal checks,
// and byte-identical continuation across a mid-run
// save/restore.
func TestEngineBootGate(t *testing.T) {
	rom := engineROM(t)
	sys, err := New(rom)
	if err != nil {
		t.Fatal(err)
	}
	// The synthetic boot area is original code, so the
	// CRC32 detection cannot name its CIC family: the
	// boot must fall back to the documented 6102
	// default.
	if sys.Cart.CIC != cart.CICUnknown {
		t.Fatalf("synthetic CIC detected as %s (test premise broken)", sys.Cart.CIC)
	}
	sys.SetSaveType(cart.SaveEEPROM4K)
	sys.SetButtons(ButtonA | ButtonStart)
	sys.Start()

	// Post-boot PIF state, before the payload's own SI
	// traffic overwrites PIF RAM.
	pram := sys.Mem.PIFRAM()
	if pram[0x3F] != 0x80 {
		t.Errorf("PIF command byte after boot = %#02x, want 0x80", pram[0x3F])
	}
	if pram[0x25] != 0x3F || pram[0x26] != 0x3F {
		t.Errorf("boot info seeds = %#02x/%#02x, want 0x3F", pram[0x25], pram[0x26])
	}
	if !sys.Booted() || sys.BootCIC() != cart.CIC6102 {
		t.Errorf("boot cic = %s, booted = %v", sys.BootCIC(), sys.Booted())
	}
	if !sys.BootChecksumOK() {
		c1, c2 := sys.BootChecksum()
		t.Errorf("IPL3 checksum report: computed %08x/%08x, header %08x/%08x",
			c1, c2, sys.Cart.CRC1, sys.Cart.CRC2)
	}
	// IPL3 sits in DMEM where IPL2 would leave it.
	if got := sys.Mem.Read(0x04000040, 4); got == 0 {
		t.Error("DMEM holds no IPL3 at 0x40")
	}

	// Run until all milestones land, recording the
	// order they appear in. Audio is collected from
	// the first frame: the payload's transfer is a
	// finite 0.4 seconds of sine.
	rd32 := func(addr uint32) uint32 { return uint32(sys.Mem.Read(addr, 4)) }
	order := []int{}
	seen := map[uint32]bool{}
	var samples []int16
	deadline := 400 // frames
	for frame := 0; frame < deadline && len(order) < 6; frame++ {
		sys.RunFrame()
		samples = append(samples, sys.AudioBuffer()...)
		for m := 0; m < 6; m++ {
			addr := uint32(0x320 + 4*m)
			if !seen[addr] && rd32(addr) == uint32(m+1) {
				seen[addr] = true
				order = append(order, m+1)
			}
		}
	}
	if len(order) < 6 {
		t.Fatalf("milestones reached: %v (flags %08x %08x %08x %08x %08x %08x, "+
			"details: info %08x pak %08x ee %08x vto %08x ato %08x, spin %d, pc %x)",
			order,
			rd32(0x320), rd32(0x324), rd32(0x328), rd32(0x32C), rd32(0x330), rd32(0x334),
			rd32(0x354), rd32(0x34C), rd32(0x348), rd32(0x360), rd32(0x364),
			rd32(0x350), sys.CPU.PC)
	}
	for i, m := range order {
		if m != i+1 {
			t.Fatalf("milestones out of order: %v", order)
		}
	}
	// The seed IPL3 received in $s6 is the 6102 seed.
	if got := rd32(0x3F0); got != 0x3F {
		t.Errorf("seed at entry = %#x, want 0x3F", got)
	}
	// The PIF boot was terminated by the payload.
	if !sys.Mem.PIF.BootTerminated() {
		t.Error("PIF boot termination command never arrived")
	}
	// Controller evidence: A + Start in the button
	// bytes the payload copied out of its poll.
	w0, w1 := rd32(0x340), rd32(0x344)
	// Frame word 0 bytes are TX, RX, command, button
	// high byte.
	if byte(w0>>24) != 0x01 || byte(w0>>16) != 0x04 || byte(w0>>8) != 0x01 {
		t.Errorf("poll frame head = %08x (TX/RX/cmd bytes wrong)", w0)
	}
	buttons := uint16(w0&0xFF)<<8 | uint16(w1>>24)
	if buttons != ButtonA|ButtonStart {
		t.Errorf("polled buttons = %#04x, want %#04x", buttons, uint16(ButtonA|ButtonStart))
	}
	if got := rd32(0x354); got != 0x600D {
		t.Errorf("ch4 info check = %08x, want 0x600D", got)
	}
	if got := rd32(0x358); got != 3 {
		t.Errorf("pak fresh status = %d, want 3", got)
	}
	if got := rd32(0x34C); got != 0x600D {
		t.Errorf("pak round trip = %08x, want 0x600D", got)
	}
	if got := rd32(0x348); got != 0x600D {
		t.Errorf("eeprom round trip = %08x, want 0x600D", got)
	}
	// The EEPROM block the payload wrote is in the
	// save device now.
	if sys.SaveRAM()[5*8] != 0xA0 || sys.SaveRAM()[5*8+7] != 0xA7 {
		t.Errorf("eeprom block 5 = %02x..%02x", sys.SaveRAM()[40], sys.SaveRAM()[47])
	}
	// Video evidence: the scene's probes through the
	// VI presentation.
	fb := sys.Framebuffer()
	if got := fb[80*320+100]; got != 0xFFFF00FF {
		t.Errorf("triangle probe = %#08x, want magenta 0xFFFF00FF", got)
	}
	if got := fb[0]; got != 0xFF214284 {
		t.Errorf("background probe = %#08x, want 0xFF214284", got)
	}

	// Audio evidence: run out the rest of the
	// payload's sine transfer and measure it (32
	// frames per period at a 32006 Hz DAC rate is
	// 1000.19 Hz ideal).
	for i := 0; i < 40; i++ {
		sys.RunFrame()
		samples = append(samples, sys.AudioBuffer()...)
	}
	frames := len(samples) / 2
	if frames < 10000 {
		t.Fatalf("only %d audio frames produced", frames)
	}
	crossings := 0
	for i := 1; i < frames; i++ {
		if samples[2*(i-1)] < 0 && samples[2*i] >= 0 {
			crossings++
		}
	}
	freq := float64(crossings) / (float64(frames) / float64(sys.AudioRate()))
	if freq < 970 || freq > 1030 {
		t.Errorf("measured sine = %.2f Hz, want about 1000", freq)
	} else {
		t.Logf("sine measured %.2f Hz (ideal 1000.19)", freq)
	}

	// Mid-run save/restore: byte-identical continuation.
	_ = sys.AudioBuffer()
	state := sys.Serialize()
	sys.RunCycles(300000)
	audioA := sys.AudioBuffer()
	stateA := sys.Serialize()
	fbA := append([]uint32(nil), sys.Framebuffer()...)

	sys2, err := New(rom)
	if err != nil {
		t.Fatal(err)
	}
	sys2.SetSaveType(cart.SaveEEPROM4K)
	sys2.SetButtons(ButtonA | ButtonStart)
	sys2.Start()
	if err := sys2.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	sys2.RunCycles(300000)
	audioB := sys2.AudioBuffer()
	stateB := sys2.Serialize()
	fbB := append([]uint32(nil), sys2.Framebuffer()...)
	if string(stateA) != string(stateB) {
		t.Error("continuation states diverged after restore")
	}
	if string(int16s(audioA)) != string(int16s(audioB)) {
		t.Error("continuation audio diverged after restore")
	}
	if len(audioA) == 0 {
		t.Error("no audio in the continuation window")
	}
	for i := range fbA {
		if fbA[i] != fbB[i] {
			t.Errorf("continuation framebuffer diverged at pixel %d", i)
			break
		}
	}
	if got := uint32(sys2.Mem.Read(0x350, 4)); got == 0 {
		t.Error("spin counter did not resume after restore")
	}
	t.Logf("state size v4 = %d bytes", sys.SerializeSize())
}

// TestBootChecksumMismatchReportedOnly proves the boot
// proceeds on a bad header checksum and merely
// reports it.
func TestBootChecksumMismatchReportedOnly(t *testing.T) {
	rom := engineROM(t)
	rom[0x10] ^= 0xFF // corrupt the stored checksum word
	sys, err := New(rom)
	if err != nil {
		t.Fatal(err)
	}
	sys.Start()
	if sys.BootChecksumOK() {
		t.Error("corrupted checksum reported OK")
	}
	// Boot still ran: the payload's first milestone
	// lands within a few frames.
	for i := 0; i < 30; i++ {
		sys.RunFrame()
		if sys.Mem.Read(0x320, 4) == 1 {
			return
		}
	}
	t.Error("payload never started on a checksum-mismatched ROM")
}

// TestWriteEngineROM materializes the synthetic engine
// ROM for the cross-platform harness
// (go_backend/cmd/enginetest). It runs only when
// GON64_ENGINE_ROM_OUT names the output path:
//
//	GON64_ENGINE_ROM_OUT=/tmp/gon64-engine.z64 go test ./system -run TestWriteEngineROM
func TestWriteEngineROM(t *testing.T) {
	out := os.Getenv("GON64_ENGINE_ROM_OUT")
	if out == "" {
		t.Skip("GON64_ENGINE_ROM_OUT not set")
	}
	if err := os.WriteFile(out, engineROM(t), 0o644); err != nil {
		t.Fatal(err)
	}
}
