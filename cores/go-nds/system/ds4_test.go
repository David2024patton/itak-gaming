package system_test

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-nds/system"
)

// DS-4 enginetest: a synthetic cartridge whose ARM9 and
// ARM7 guest programs drive the whole machine the way a
// game would. The ARM9 boots through the real direct
// boot, uploads VRAM data and programs both 2D engines,
// submits a GX scene (a textured far quad under a flat
// near quad) plus a 2D text tile on engine A, swaps
// buffers, then asks the ARM7 over the IPC FIFO to start
// a PCM loop tone, and finally to poll the touchscreen
// over SPI and report the ADC values. Milestones land in
// a sequence log in main RAM in completion order.
//
// The guests are built with the tiny two-pass assembler
// below (the DS-2 gate hand-writes its words; the DS-4
// programs are long enough that encoding helpers pay
// off). Data uploads and register programming on the
// ARM9 side run through one (address, value) word table
// executed by a short copy loop, the same table the GX
// command stream is expressed in (GX direct ports are
// just word writes to 0x04000400 + command*4).

const (
	ds4LogAddr   = 0x0201F000 // milestone sequence log
	ds4FailAddr  = 0x0201F080 // guest failure magic lands here
	ds4TouchXAdr = 0x0201F100 // ARM7 stores the touch ADC X here
	ds4TouchYAdr = 0x0201F104 // and Y here

	ds4CmdTone   = 0x544F4E45 // "TONE"
	ds4AckTone   = 0x41434B54 // "ACKT"
	ds4CmdTouch  = 0x544F5543 // "TOUC"
	ds4AckTouch  = 0x41434B55 // "ACKU"
	ds4FailMagic = 0x0BAD0BAD
)

// ---- tiny ARM assembler --------------------------------

type ds4Fix struct {
	at     int
	label  string
	branch bool
	addr   bool // the word itself is patched to the label address
}

type ds4Asm struct {
	base   uint32
	w      []uint32
	labels map[string]int
	fix    []ds4Fix
}

func newDS4Asm(base uint32) *ds4Asm {
	return &ds4Asm{base: base, labels: map[string]int{}}
}

func (a *ds4Asm) label(name string) { a.labels[name] = len(a.w) }

func (a *ds4Asm) emit(w uint32) { a.w = append(a.w, w) }

// dataWord appends a raw word (literal pool entry or a
// data table entry) under a label.
func (a *ds4Asm) dataWord(name string, v uint32) {
	a.label(name)
	a.emit(v)
}

// addrWord appends a word whose value becomes the
// address of the named label, patched at build time.
func (a *ds4Asm) addrWord(name, target string) {
	a.label(name)
	a.fix = append(a.fix, ds4Fix{at: len(a.w), label: target, addr: true})
	a.emit(0)
}

func (a *ds4Asm) ldrLit(rd int, label string) {
	a.fix = append(a.fix, ds4Fix{at: len(a.w), label: label})
	a.emit(0xE59F0000 | uint32(rd)<<12)
}

func (a *ds4Asm) b(cond uint32, label string) {
	a.fix = append(a.fix, ds4Fix{at: len(a.w), label: label, branch: true})
	a.emit(cond<<28 | 0x0A000000)
}

func (a *ds4Asm) build(t *testing.T) []uint32 {
	for _, f := range a.fix {
		idx, ok := a.labels[f.label]
		if !ok {
			t.Fatalf("assembler: unknown label %q", f.label)
		}
		switch {
		case f.addr:
			a.w[f.at] = a.base + uint32(idx*4)
		case f.branch:
			off := idx - (f.at + 2)
			a.w[f.at] |= uint32(off) & 0xFFFFFF
		default: // LDR literal
			off := (idx - (f.at + 2)) * 4
			if off < 0 || off > 4095 {
				t.Fatalf("assembler: literal %q out of range (%d)", f.label, off)
			}
			a.w[f.at] |= uint32(off)
		}
	}
	return a.w
}

const (
	ds4EQ = 0x0
	ds4NE = 0x1
)

// ds4RotImm returns the ARM rotated-immediate encoding
// of v, or false when v is not representable.
func ds4RotImm(v uint32) (uint32, bool) {
	for rot := uint(0); rot < 16; rot++ {
		// imm8 = v ROL (2*rot)
		var imm uint32
		if rot == 0 {
			imm = v
		} else {
			imm = v<<(2*rot) | v>>(32-2*rot)
		}
		if imm <= 0xFF {
			return uint32(rot)<<8 | imm, true
		}
	}
	return 0, false
}

func (a *ds4Asm) immOp(op uint32, rd, rn int, v uint32) {
	enc, ok := ds4RotImm(v)
	if !ok {
		panic("immediate not encodable")
	}
	a.emit(op | uint32(rn)<<16 | uint32(rd)<<12 | enc)
}

func (a *ds4Asm) movImm(rd int, v uint32) { a.immOp(0xE3A00000, rd, 0, v) }
func (a *ds4Asm) cmpImm(rn int, v uint32) { a.immOp(0xE3500000, 0, rn, v) }
func (a *ds4Asm) tstImm(rn int, v uint32) { a.immOp(0xE3100000, 0, rn, v) }
func (a *ds4Asm) subsImm(rd, rn int, v uint32) {
	a.immOp(0xE2500000, rd, rn, v)
}
func (a *ds4Asm) movReg(rd, rm int) {
	a.emit(0xE1A00000 | uint32(rd)<<12 | uint32(rm))
}
func (a *ds4Asm) cmpReg(rn, rm int) {
	a.emit(0xE1500000 | uint32(rn)<<16 | uint32(rm))
}
func (a *ds4Asm) ldr(rd, rn, off int) {
	a.emit(0xE5900000 | uint32(rn)<<16 | uint32(rd)<<12 | uint32(off))
}
func (a *ds4Asm) ldrPost(rd, rn, off int) {
	a.emit(0xE4900000 | uint32(rn)<<16 | uint32(rd)<<12 | uint32(off))
}
func (a *ds4Asm) str(rd, rn int) {
	a.emit(0xE5800000 | uint32(rn)<<16 | uint32(rd)<<12)
}
func (a *ds4Asm) strPost(rd, rn, off int) {
	a.emit(0xE4800000 | uint32(rn)<<16 | uint32(rd)<<12 | uint32(off))
}
func (a *ds4Asm) ldrb(rd, rn int) {
	a.emit(0xE5D00000 | uint32(rn)<<16 | uint32(rd)<<12)
}
func (a *ds4Asm) strb(rd, rn int) {
	a.emit(0xE5C00000 | uint32(rn)<<16 | uint32(rd)<<12)
}
func (a *ds4Asm) strh(rd, rn int) {
	a.emit(0xE1C000B0 | uint32(rn)<<16 | uint32(rd)<<12)
}
func (a *ds4Asm) ldrh(rd, rn int) {
	a.emit(0xE1D000B0 | uint32(rn)<<16 | uint32(rd)<<12)
}
func (a *ds4Asm) lslImm(rd, rm, n int) {
	a.emit(0xE1A00000 | uint32(rd)<<12 | uint32(n)<<7 | uint32(rm))
}
func (a *ds4Asm) lsrImm(rd, rm, n int) {
	a.emit(0xE1A00020 | uint32(rd)<<12 | uint32(n)<<7 | uint32(rm))
}
func (a *ds4Asm) orrReg(rd, rn, rm int) {
	a.emit(0xE1800000 | uint32(rn)<<16 | uint32(rd)<<12 | uint32(rm))
}
func (a *ds4Asm) spin() { a.emit(0xEAFFFFFE) } // B .

// log appends one milestone to the sequence log (r8 is
// the log cursor on the ARM9).
func (a *ds4Asm) log(stage uint32) {
	a.movImm(0, stage)
	a.strPost(0, 8, 4)
}

// fail writes the failure magic and parks the CPU.
func (a *ds4Asm) fail() {
	a.label("fail")
	a.ldrLit(0, "fail_addr")
	a.ldrLit(1, "fail_magic")
	a.str(1, 0)
	a.spin()
}

// waitFIFO polls FIFOCNT until the receive FIFO is
// non-empty, then pops one word into rd and compares it
// against the expected literal; mismatch branches to
// the named label (the ARM9 passes "fail", the ARM7
// loops back to its wait).
func (a *ds4Asm) waitFIFO(rd int, want string, badLabel string, tag string) {
	a.label("wait_" + tag)
	a.ldrLit(0, "fifocnt")
	a.ldr(2, 0, 0)
	a.tstImm(2, 0x100) // receive-empty bit
	a.b(ds4NE, "wait_"+tag)
	a.ldrLit(0, "fiforecv")
	a.ldr(rd, 0, 0)
	a.ldrLit(1, want)
	a.cmpReg(rd, 1)
	a.b(ds4NE, badLabel)
}

// sendFIFO pushes the named literal word to the peer.
func (a *ds4Asm) sendFIFO(what string) {
	a.ldrLit(0, "fifosend")
	a.ldrLit(1, what)
	a.str(1, 0)
}

// enableFIFO turns this side's IPC FIFO unit on.
func (a *ds4Asm) enableFIFO() {
	a.ldrLit(0, "fifocnt")
	a.ldrLit(1, "v8000")
	a.str(1, 0)
}

// ---- the ARM9 guest ------------------------------------

// ds4GXPort is the direct command port of a GX command.
func ds4GXPort(id uint32) uint32 { return 0x04000400 + id*4 }

// ds4IOTable is the (address, value) word-write table the
// ARM9 guest executes: VRAM uploads through the LCDC
// window, palette entries, VRAMCNT assignment, both
// engines' display registers, the GX clear registers,
// and the whole GX command stream for the scene. It
// mirrors, write for write, the bus pokes of the DS-3
// gates (setupDisplay and the textured/near quad gate).
func ds4IOTable() [][2]uint32 {
	var tab [][2]uint32
	put := func(addr, val uint32) { tab = append(tab, [2]uint32{addr, val}) }

	// 8x8 256-color texture in bank A (LCDC 0x06800000):
	// a four-color checker, index 1+(tx+ty)%4.
	rows := []uint32{0x04030201, 0x01040302, 0x02010403, 0x03020104}
	for ty := 0; ty < 8; ty++ {
		put(0x06800000+uint32(ty*8), rows[ty%4])
		put(0x06800004+uint32(ty*8), rows[ty%4])
	}
	// Texture palette in bank E (LCDC 0x06880000):
	// entry 1 red, 2 green, 3 blue, 4 white.
	put(0x06880000, 0x001F0000)
	put(0x06880004, 0x7C0003E0)
	put(0x06880008, 0x00007FFF)
	// Engine A BG1 tile 1 (filled with color index 1)
	// and map entry 0 in bank B (LCDC 0x06820000).
	for i := 0; i < 8; i++ {
		put(0x06824020+uint32(i*4), 0x11111111)
	}
	put(0x06821000, 1)
	// Engine B BG0 tile 1 and map entry 0 in bank H
	// (LCDC 0x06898000).
	for i := 0; i < 8; i++ {
		put(0x0689C020+uint32(i*4), 0x11111111)
	}
	put(0x06899000, 1)
	// BG palettes: engine A entry 1 magenta (a color the
	// texture palette does not contain, so the 2D probe
	// is unambiguous), engine B entry 1 white.
	put(0x05000000, 0x7C1F0000)
	put(0x05000400, 0x7FFF0000)
	// VRAMCNT: A texture slot 0, B = BG A, E texture
	// palette, H = BG B (WRAMCNT byte preserved as 0).
	put(0x04000240, 0x00008183)
	put(0x04000244, 0x00000083)
	put(0x04000248, 0x00000081)
	// Engine A: graphics display, BG0 = 3D (priority 1),
	// BG1 text (priority 0, char base 1, screen base 2).
	put(0x04000000, 0x00010308) // DISPCNT A
	put(0x04000008, 0x02040001) // BG1CNT << 16 | BG0CNT
	put(0x04000060, 0x00000001) // DISP3DCNT: texture on
	// Engine B: graphics display, BG0 text.
	put(0x04001000, 0x00010100) // DISPCNT B
	put(0x04001008, 0x00000204) // BG0CNT B
	// GX rear plane.
	put(0x04000350, 31<<16) // CLEAR_COLOR black, alpha 31
	put(0x04000354, 0x7FFF) // CLEAR_DEPTH far
	// GX command stream (direct ports).
	gx := func(id uint32, params ...uint32) {
		if len(params) == 0 {
			put(ds4GXPort(id), 0)
			return
		}
		for _, p := range params {
			put(ds4GXPort(id), p)
		}
	}
	gx(0x10, 0)          // MTX_MODE projection
	gx(0x15)             // MTX_IDENTITY
	gx(0x10, 1)          // MTX_MODE position
	gx(0x15)             // MTX_IDENTITY
	gx(0x60, 0xBFFF0000) // VIEWPORT full screen
	// Textured far quad.
	gx(0x29, 31<<16|0xC0) // POLYGON_ATTR
	gx(0x20, 0x7FFF)      // COLOR white
	gx(0x2A, 4<<26)       // TEXIMAGE_PARAM 256-color
	gx(0x2B, 0)           // PLTT_BASE
	gx(0x40, 1)           // BEGIN_VTXS quads
	vtx := func(s, t, x, y, z int) {
		gx(0x22, uint32(uint16(s))|uint32(uint16(t))<<16)
		gx(0x23, uint32(uint16(x))|uint32(uint16(y))<<16, uint32(uint16(z)))
	}
	vtx(0, 128, -0x1000, -0x1000, 0x400)
	vtx(128, 128, 0x1000, -0x1000, 0x400)
	vtx(128, 0, 0x1000, 0x1000, 0x400)
	vtx(0, 0, -0x1000, 0x1000, 0x400)
	// Flat green near quad (half size, closer).
	gx(0x2A, 0)      // TEXIMAGE_PARAM off
	gx(0x20, 0x03E0) // COLOR green
	gx(0x40, 1)      // BEGIN_VTXS quads
	fvtx := func(x, y, z int) {
		gx(0x23, uint32(uint16(x))|uint32(uint16(y))<<16, uint32(uint16(z)))
	}
	fvtx(-0x800, -0x800, -0x400)
	fvtx(0x800, -0x800, -0x400)
	fvtx(0x800, 0x800, -0x400)
	fvtx(-0x800, 0x800, -0x400)
	gx(0x50, 0) // SWAP_BUFFERS
	return tab
}

func ds4ARM9Program(t *testing.T) []uint32 {
	a := newDS4Asm(0x02000000)
	// Stage 1: the direct boot flag halfword reads 1.
	a.ldrLit(0, "bootflag")
	a.ldrh(1, 0)
	a.cmpImm(1, 1)
	a.b(ds4NE, "fail")
	a.ldrLit(8, "log_base")
	a.log(1)
	// Stage 2: display setup, VRAM uploads, GX scene,
	// swap buffers (one table copy loop).
	a.ldrLit(0, "io_table_lit")
	a.ldrLit(1, "io_table_end_lit")
	a.label("tabloop")
	a.cmpReg(0, 1)
	a.b(ds4EQ, "tabdone")
	a.ldrPost(2, 0, 4)
	a.ldrPost(3, 0, 4)
	a.str(3, 2)
	a.b(0xE, "tabloop") // AL
	a.label("tabdone")
	a.log(2)
	// Stage 3: IPC tone handshake with the ARM7.
	a.enableFIFO()
	a.sendFIFO("cmd_tone")
	a.waitFIFO(3, "ack_tone", "fail", "tone")
	a.log(3)
	// Stage 4: IPC touch handshake; the ARM7 leaves the
	// ADC values in main RAM.
	a.sendFIFO("cmd_touch")
	a.waitFIFO(3, "ack_touch", "fail", "touch")
	a.log(4)
	a.spin()
	a.fail()
	// Literal pool.
	a.dataWord("bootflag", 0x027FFFFA)
	a.dataWord("log_base", ds4LogAddr)
	a.dataWord("fail_addr", ds4FailAddr)
	a.dataWord("fail_magic", ds4FailMagic)
	a.dataWord("fifocnt", 0x04000184)
	a.dataWord("fifosend", 0x04000188)
	a.dataWord("fiforecv", 0x04100000)
	a.dataWord("v8000", 0x8000)
	a.dataWord("cmd_tone", ds4CmdTone)
	a.dataWord("ack_tone", ds4AckTone)
	a.dataWord("cmd_touch", ds4CmdTouch)
	a.dataWord("ack_touch", ds4AckTouch)
	// The table start/end literals: pool words holding
	// the table's own start and end addresses.
	a.addrWord("io_table_lit", "io_table")
	a.addrWord("io_table_end_lit", "io_table_end")
	a.label("io_table")
	for _, e := range ds4IOTable() {
		a.emit(e[0])
		a.emit(e[1])
	}
	a.label("io_table_end")
	return a.build(t)
}

// ---- the ARM7 guest ------------------------------------

// ds4SPIRead emits one touchscreen ADC read for the
// given control byte (channel in bits 6-4, bit 7 set),
// leaving the 12-bit ADC value in r4. It mirrors the
// byte sequence of the DS-3 microphone gate.
func ds4SPIRead(a *ds4Asm, ctrl uint32) {
	a.ldrLit(0, "spicnt")
	a.ldrLit(1, "spicnt_val")
	a.strh(1, 0)
	a.ldrLit(0, "spidata")
	a.movImm(1, ctrl)
	a.strb(1, 0)
	a.movImm(1, 0)
	a.strb(1, 0)
	a.ldrb(2, 0) // first data byte (adc >> 5)
	a.strb(1, 0)
	a.ldrb(3, 0) // second data byte ((adc & 0x1F) << 3)
	a.lslImm(2, 2, 8)
	a.orrReg(2, 2, 3)
	a.lsrImm(4, 2, 3)
}

func ds4ARM7Program(t *testing.T) []uint32 {
	a := newDS4Asm(0x03800000)
	// Wait for the tone command.
	a.enableFIFO()
	a.waitFIFO(3, "cmd_tone", "wait_tone", "tone")
	// Copy the sine table into main RAM (the address
	// the sound channel will stream from).
	a.ldrLit(0, "sine_ptr")
	a.ldrLit(1, "ram_sine")
	a.movImm(2, 37)
	a.label("cpy")
	a.ldrPost(3, 0, 4)
	a.strPost(3, 1, 4)
	a.subsImm(2, 2, 1)
	a.b(ds4NE, "cpy")
	// Program sound channel 0 and the mixer, mirroring
	// the DS-3 machine gate's register writes; the CNT
	// write (with the enable bit) comes last.
	a.ldrLit(0, "snd_sad")
	a.ldrLit(1, "ram_sine")
	a.str(1, 0)
	a.ldrLit(0, "snd_tmr")
	a.ldrLit(1, "tmr_val")
	a.str(1, 0)
	a.ldrLit(0, "snd_len")
	a.movImm(1, 37)
	a.str(1, 0)
	a.ldrLit(0, "snd_main")
	a.ldrLit(1, "main_val")
	a.str(1, 0)
	a.ldrLit(0, "snd_cnt")
	a.ldrLit(1, "cnt_val")
	a.str(1, 0)
	a.sendFIFO("ack_tone")
	// Wait for the touch command, then poll the
	// touchscreen over SPI: channel 5 is X, channel 1
	// is Y.
	a.waitFIFO(3, "cmd_touch", "wait_touch", "touch")
	ds4SPIRead(a, 0xD0) // X
	a.ldrLit(0, "touch_x")
	a.str(4, 0)
	ds4SPIRead(a, 0x90) // Y
	a.ldrLit(0, "touch_y")
	a.str(4, 0)
	a.sendFIFO("ack_touch")
	a.spin()
	// Literal pool.
	a.dataWord("fifocnt", 0x04000184)
	a.dataWord("fifosend", 0x04000188)
	a.dataWord("fiforecv", 0x04100000)
	a.dataWord("v8000", 0x8000)
	a.dataWord("cmd_tone", ds4CmdTone)
	a.dataWord("ack_tone", ds4AckTone)
	a.dataWord("cmd_touch", ds4CmdTouch)
	a.dataWord("ack_touch", ds4AckTouch)
	a.dataWord("ram_sine", 0x02010000)
	a.dataWord("snd_sad", 0x04000404)
	a.dataWord("snd_tmr", 0x04000408)
	a.dataWord("snd_len", 0x0400040C)
	a.dataWord("snd_cnt", 0x04000400)
	a.dataWord("snd_main", 0x04000500)
	a.dataWord("tmr_val", 65024) // period 512: 16,756,991/512 per second
	a.dataWord("cnt_val", 0xA840007F)
	a.dataWord("main_val", 0x807F)
	a.dataWord("spicnt", 0x040001C0)
	a.dataWord("spidata", 0x040001C2)
	a.dataWord("spicnt_val", 0x8200)
	a.dataWord("touch_x", ds4TouchXAdr)
	a.dataWord("touch_y", ds4TouchYAdr)
	a.addrWord("sine_ptr", "sine_data")
	// The 74-sample PCM16 sine table (37 words), the
	// same table the DS-3 tone gates use.
	a.label("sine_data")
	for i := 0; i < 37; i++ {
		s0 := int16(math.Sin(2*math.Pi*float64(2*i)/74) * 20000)
		s1 := int16(math.Sin(2*math.Pi*float64(2*i+1)/74) * 20000)
		a.emit(uint32(uint16(s0)) | uint32(uint16(s1))<<16)
	}
	return a.build(t)
}

// ---- the gate ------------------------------------------

// buildDS4ROM assembles the synthetic DS-4 cartridge
// (the DS-2 builder's layout: ARM9 at 0x02000000, ARM7
// at 0x03800000, entries at the load addresses).
func buildDS4ROM(arm9prog, arm7prog []uint32) []byte {
	rom := make([]byte, 0x4000)
	copy(rom[0x000:], "DS4 PROOF")
	copy(rom[0x00C:], "ADSE")
	copy(rom[0x010:], "01")
	le := binary.LittleEndian
	le.PutUint32(rom[0x020:], 0x1000)
	le.PutUint32(rom[0x024:], 0x02000000)
	le.PutUint32(rom[0x028:], 0x02000000)
	le.PutUint32(rom[0x02C:], uint32(len(arm9prog)*4))
	le.PutUint32(rom[0x030:], 0x2000)
	le.PutUint32(rom[0x034:], 0x03800000)
	le.PutUint32(rom[0x038:], 0x03800000)
	le.PutUint32(rom[0x03C:], uint32(len(arm7prog)*4))
	for i, w := range arm9prog {
		le.PutUint32(rom[0x1000+i*4:], w)
	}
	for i, w := range arm7prog {
		le.PutUint32(rom[0x2000+i*4:], w)
	}
	return rom
}

func ds4Word(m *system.Machine, addr uint32) uint32 {
	return binary.LittleEndian.Uint32(m.Bus.MainRAM[addr-0x02000000:])
}

// ds4Stacked composes the two screens exactly as the
// libretro shell presents them: top screen in rows
// 0-191, bottom screen in rows 192-383.
func ds4Stacked(m *system.Machine) []uint32 {
	top, bottom := m.Framebuffer()
	out := make([]uint32, 256*384)
	copy(out[:256*192], top)
	copy(out[256*192:], bottom)
	return out
}

func TestDS4EngineGate(t *testing.T) {
	rom := buildDS4ROM(ds4ARM9Program(t), ds4ARM7Program(t))
	// Leave the ROM next to the built shell library so
	// the shared enginetest harness can run the same
	// image through the real .so.
	if err := os.MkdirAll(filepath.Join("..", "builds"), 0o755); err == nil {
		p := filepath.Join("..", "builds", "enginetest.nds")
		if err := os.WriteFile(p, rom, 0o644); err != nil {
			t.Logf("writing harness ROM: %v", err)
		} else {
			t.Logf("harness ROM at %s", p)
		}
	}

	m := system.New()
	if err := m.LoadROM(rom); err != nil {
		t.Fatal(err)
	}
	// The pen is down at (100, 60) before anything runs;
	// the ARM7's SPI poll must see exactly this point.
	m.SetTouch(100, 60, true)
	m.Start()
	frames := 0
	var log []uint32
	for frames = 0; frames < 30; frames++ {
		m.RunFrame()
		log = log[:0]
		for i := 0; i < 8; i++ {
			if w := ds4Word(m, ds4LogAddr+uint32(4*i)); w != 0 {
				log = append(log, w)
			}
		}
		if len(log) >= 4 {
			break
		}
	}
	if got := ds4Word(m, ds4FailAddr); got != 0 {
		t.Fatalf("guest failure magic %08x after %d frames", got, frames)
	}
	if len(log) != 4 || log[0] != 1 || log[1] != 2 || log[2] != 3 || log[3] != 4 {
		t.Fatalf("milestone log = %v after %d frames, want [1 2 3 4]", log, frames)
	}
	// Touch ADC values: the documented linear map of
	// (100, 60) from the DS-2/DS-3 gates.
	if got := ds4Word(m, ds4TouchXAdr); got != 0x67D {
		t.Fatalf("touch ADC X = %03x want 67D", got)
	}
	if got := ds4Word(m, ds4TouchYAdr); got != 0x565 {
		t.Fatalf("touch ADC Y = %03x want 565", got)
	}

	// The GX swap renders at the next VBlank edge (the
	// DS-3 model), so give the presented frame two more
	// frames to settle before probing pixels.
	m.RunFrame()
	m.RunFrame()

	// Shell-presented stacked frame probes.
	fb := ds4Stacked(m)
	probe := func(x, y int, want uint32, what string) {
		if got := fb[y*256+x]; got != want {
			t.Fatalf("stacked (%d,%d) %s = %08x want %08x", x, y, what, got, want)
		}
	}
	probe(4, 4, 0xFFFF00FF, "engine A BG1 tile (2D over 3D)")
	probe(10, 10, 0xFFFF0000, "textured quad texel (0,0) red")
	probe(40, 20, 0xFF00FF00, "textured quad texel (1,0) green")
	probe(128, 96, 0xFF00FF00, "near flat quad green")
	probe(200, 150, 0xFFFF0000, "textured quad texel (6,6) red")
	probe(4, 196, 0xFFFFFFFF, "engine B BG0 tile white")
	probe(100, 292, 0xFF000000, "engine B backdrop black")

	// Audio: the PCM loop tone the ARM7 started. Keep
	// running so at least half a second of output is
	// available, then measure the left channel's pitch
	// by positive zero crossings (the DS-3 method).
	for i := 0; i < 30; i++ {
		m.RunFrame()
	}
	samples := m.AudioSamples()
	cross := 0
	prev := int16(0)
	nonSilent := 0
	for i := 0; i+1 < len(samples); i += 2 {
		s := samples[i]
		if s != 0 {
			nonSilent++
		}
		if prev <= 0 && s > 0 {
			cross++
		}
		prev = s
	}
	if nonSilent == 0 {
		t.Fatal("audio is silent after the IPC tone start")
	}
	secs := float64(len(samples)/2) / 32768.0
	freq := float64(cross) / secs
	expect := (16756991.0 / 512.0) / 74.0
	if math.Abs(freq-expect)/expect > 0.02 {
		t.Fatalf("tone freq = %.2f Hz want %.2f (within 2%%)", freq, expect)
	}
	t.Logf("milestones in %d frames; tone %.2f Hz (expect %.2f); %d audio values",
		frames, freq, expect, len(samples))

	// Determinism: drain, save mid-run, continue on this
	// machine and on a fresh one restored from the save;
	// framebuffers, continuation audio, and final state
	// bytes must be identical.
	m.AudioSamples()
	saved := m.Serialize()
	if len(saved) != system.StateSize {
		t.Fatalf("state size = %d want %d", len(saved), system.StateSize)
	}
	for i := 0; i < 4; i++ {
		m.RunFrame()
	}
	topA, botA := m.Framebuffer()
	audioA := m.AudioSamples()
	stateA := m.Serialize()

	m2 := system.New()
	if err := m2.LoadROM(rom); err != nil {
		t.Fatal(err)
	}
	m2.SetTouch(100, 60, true)
	if !m2.Deserialize(saved) {
		t.Fatal("Deserialize refused the DS-4 state")
	}
	for i := 0; i < 4; i++ {
		m2.RunFrame()
	}
	topB, botB := m2.Framebuffer()
	audioB := m2.AudioSamples()
	stateB := m2.Serialize()
	if len(audioA) == 0 || len(audioA) != len(audioB) {
		t.Fatalf("continuation audio lengths: %d vs %d", len(audioA), len(audioB))
	}
	for i := range audioA {
		if audioA[i] != audioB[i] {
			t.Fatalf("continuation audio value %d differs: %d vs %d", i, audioA[i], audioB[i])
		}
	}
	for i := range topA {
		if topA[i] != topB[i] || botA[i] != botB[i] {
			t.Fatalf("framebuffer pixel %d differs after restore", i)
		}
	}
	if !bytes.Equal(stateA, stateB) {
		t.Fatal("state bytes differ after restore and continuation")
	}
}
