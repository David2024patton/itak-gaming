package system

// PS1-3 system gates: a full PS-X EXE enginetest that
// drives the BIOS event system, the GPU textured quad
// path, SPU ADPCM playback through DMA, a CD sector
// read through DMA with a checksum, and GTE RTPS
// projection, all from one guest program, plus a
// determinism leg across Serialize and Deserialize,
// and focused BIOS HLE unit tests.
//
// Deviations from the original slice plan, each one
// resolved in favor of the code as built (the plan
// itself instructed this):
//
//   - DMA channel enable bits in DPCR are bit 4*n+3
//     per dma.Controller.channelEnabled, so channel 4
//     (SPU) is 0x80000, not 0x10000. Channel 3 (CD) is
//     0x8000 as planned.
//   - DMA CHCR direction: dma.transferWord treats
//     CHCR bit 0 set as RAM to device, so the SPU
//     upload uses CHCR 0x01000001. The CD channel
//     moves device to RAM and uses 0x01000000 as
//     planned.
//   - The RTPS CPU word is 0x4A080001, not the draft
//     0x4A000001: chips/r3000a execCOP2 dispatches a
//     GTE command when the rs field is 0x10 or more
//     and passes the whole instruction word to the
//     GTE, whose opcode is bits 0 to 5 and whose sf
//     bit is bit 19. The draft word has sf clear,
//     which would shift the Z result; 0x4A080001 has
//     sf set, matching the documented RTPS word form
//     the gte package tests use (0x0180001 as a bare
//     command).
//   - The textured quad second vertex UV carries
//     u=16, not u=0: vertex 1 is the top right corner
//     (96,32) of a quad mapping a 16 texel wide
//     texture, and the framebuffer probes below are
//     only satisfiable with u=16 there. The texpage
//     halfword is 0x0110, matching
//     gpu.texPageFromWord: x base 0, y base bit at
//     bit 4 (VRAM y 256), color format bits 7 to 8
//     equal 2 (15-bit direct).
//   - The GP1 display enable word is written after
//     the textured quad, not before it: gpu.plot
//     suppresses any drawing into the display area
//     while the display is enabled and the E1
//     draw-to-display bit is clear, and the quad
//     covers display pixels by design. All planned
//     GP1 words are still written, in stage B.
//   - Stage markers share one word at result+0x100,
//     written with the stage number as each stage
//     completes (the plan's simpler option), because
//     per-stage slots at result+0x100+4*stage would
//     collide with the CD checksum at +0x104 and the
//     GTE results at +0x108 onward.

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// ----------------------------------------------------------------
// Register numbers used by the assembler.
// ----------------------------------------------------------------

const (
	ps3Zero = 0
	ps3V0   = 2
	ps3A0   = 4
	ps3A1   = 5
	ps3A2   = 6
	ps3A3   = 7
	ps3T0   = 8
	ps3T1   = 9
	ps3T2   = 10
	ps3T3   = 11
	ps3T4   = 12
	ps3T5   = 13
	ps3T6   = 14
	ps3T7   = 15
	ps3S0   = 16
	ps3S1   = 17
	ps3S2   = 18
	ps3S4   = 20
	ps3RA   = 31
)

// ----------------------------------------------------------------
// A small MIPS assembler with label fixups.
// ----------------------------------------------------------------

const (
	ps3PatchBranch = iota
	ps3PatchJAL
	ps3PatchLoadHi
	ps3PatchLoadLo
)

type ps3Patch struct {
	idx   int
	kind  int
	label string
}

type ps3Asm struct {
	words   []uint32
	labels  map[string]int
	patches []ps3Patch
}

func newPS3Asm() *ps3Asm {
	return &ps3Asm{labels: map[string]int{}}
}

func (a *ps3Asm) emit(w uint32) int {
	a.words = append(a.words, w)
	return len(a.words) - 1
}

func (a *ps3Asm) label(name string) { a.labels[name] = len(a.words) }

func (a *ps3Asm) labelAt(name string, idx int) { a.labels[name] = idx }

func (a *ps3Asm) nop() { a.emit(0x00000000) }

func (a *ps3Asm) lui(rt, imm uint32) {
	a.emit(0x3C000000 | rt<<16 | (imm & 0xFFFF))
}

func (a *ps3Asm) ori(rt, rs, imm uint32) {
	a.emit(0x34000000 | rs<<21 | rt<<16 | (imm & 0xFFFF))
}

// li loads a 32-bit constant in one or two words.
func (a *ps3Asm) li(rt, v uint32) {
	switch {
	case v>>16 == 0:
		a.ori(rt, ps3Zero, v)
	case v&0xFFFF == 0:
		a.lui(rt, v>>16)
	default:
		a.lui(rt, v>>16)
		a.ori(rt, rt, v)
	}
}

// liLabel loads the absolute address of a label.
func (a *ps3Asm) liLabel(rt uint32, name string) {
	idx := a.emit(0x3C000000 | rt<<16)
	a.patches = append(a.patches, ps3Patch{idx: idx, kind: ps3PatchLoadHi, label: name})
	idx = a.emit(0x34000000 | rt<<21 | rt<<16)
	a.patches = append(a.patches, ps3Patch{idx: idx, kind: ps3PatchLoadLo, label: name})
}

func (a *ps3Asm) addiu(rt, rs uint32, imm int) {
	a.emit(0x24000000 | rs<<21 | rt<<16 | uint32(uint16(imm)))
}

func (a *ps3Asm) addu(rd, rs, rt uint32) {
	a.emit(0x00000021 | rs<<21 | rt<<16 | rd<<11)
}

func (a *ps3Asm) andi(rt, rs, imm uint32) {
	a.emit(0x30000000 | rs<<21 | rt<<16 | (imm & 0xFFFF))
}

func (a *ps3Asm) lw(rt uint32, off int, rs uint32) {
	a.emit(0x8C000000 | rs<<21 | rt<<16 | uint32(uint16(off)))
}

func (a *ps3Asm) sw(rt uint32, off int, rs uint32) {
	a.emit(0xAC000000 | rs<<21 | rt<<16 | uint32(uint16(off)))
}

func (a *ps3Asm) lbu(rt uint32, off int, rs uint32) {
	a.emit(0x90000000 | rs<<21 | rt<<16 | uint32(uint16(off)))
}

func (a *ps3Asm) sb(rt uint32, off int, rs uint32) {
	a.emit(0xA0000000 | rs<<21 | rt<<16 | uint32(uint16(off)))
}

func (a *ps3Asm) sh(rt uint32, off int, rs uint32) {
	a.emit(0xA4000000 | rs<<21 | rt<<16 | uint32(uint16(off)))
}

func (a *ps3Asm) beq(rs, rt uint32, label string) {
	idx := a.emit(0x10000000 | rs<<21 | rt<<16)
	a.patches = append(a.patches, ps3Patch{idx: idx, kind: ps3PatchBranch, label: label})
}

func (a *ps3Asm) bne(rs, rt uint32, label string) {
	idx := a.emit(0x14000000 | rs<<21 | rt<<16)
	a.patches = append(a.patches, ps3Patch{idx: idx, kind: ps3PatchBranch, label: label})
}

func (a *ps3Asm) jalAbs(addr uint32) {
	a.emit(0x0C000000 | ((addr >> 2) & 0x3FFFFFF))
}

func (a *ps3Asm) jr(rs uint32) { a.emit(0x00000008 | rs<<21) }

func (a *ps3Asm) syscall() { a.emit(0x0000000C) }

// callBIOS emits a vector call: function number in
// $t1, jal to the vector, NOP in the delay slot (the
// HLE intercepts when the PC reaches the vector,
// after the delay slot has executed).
func (a *ps3Asm) callBIOS(vector uint32, fn int) {
	a.addiu(ps3T1, ps3Zero, fn)
	a.jalAbs(vector)
	a.nop()
}

// storeWordAbs stores a constant to an absolute
// address, using $t5 and $t6 as scratch.
func (a *ps3Asm) storeWordAbs(addr, val uint32) {
	a.li(ps3T5, addr)
	a.li(ps3T6, val)
	a.sw(ps3T6, 0, ps3T5)
}

// storeWordLabelAbs stores a label address to an
// absolute address.
func (a *ps3Asm) storeWordLabelAbs(addr uint32, label string) {
	a.li(ps3T5, addr)
	a.liLabel(ps3T6, label)
	a.sw(ps3T6, 0, ps3T5)
}

// storeHalfAbs stores a constant halfword to an
// absolute address.
func (a *ps3Asm) storeHalfAbs(addr, val uint32) {
	a.li(ps3T5, addr)
	a.li(ps3T6, val)
	a.sh(ps3T6, 0, ps3T5)
}

// marker records stage completion: the stage number
// goes to the shared marker word at result+0x100.
// $s0 must hold the result base 0x80020000.
func (a *ps3Asm) marker(n int) {
	a.li(ps3T1, uint32(n))
	a.sw(ps3T1, 0x100, ps3S0)
}

// gpuCW sends one GP0 word through BIOS GPU_cw.
func (a *ps3Asm) gpuCW(w uint32) {
	a.li(ps3A0, w)
	a.callBIOS(0xA0, 0x49)
}

func (a *ps3Asm) mtc2(rt, rd uint32) { a.emit(0x48800000 | rt<<16 | rd<<11) }
func (a *ps3Asm) ctc2(rt, rd uint32) { a.emit(0x48C00000 | rt<<16 | rd<<11) }

// mfc2Store reads a GTE data register into $t0 (with
// the load delay NOP) and stores it at off($s0).
func (a *ps3Asm) mfc2Store(rd uint32, off int) {
	a.emit(0x48000000 | ps3T0<<16 | rd<<11) // mfc2 t0, rd
	a.nop()
	a.sw(ps3T0, off, ps3S0)
}

// cfc2Store reads a GTE control register the same
// way as mfc2Store.
func (a *ps3Asm) cfc2Store(rd uint32, off int) {
	a.emit(0x48400000 | ps3T0<<16 | rd<<11) // cfc2 t0, rd
	a.nop()
	a.sw(ps3T0, off, ps3S0)
}

// build resolves all patches against base (the
// address word 0 lands at) and returns the words.
func (a *ps3Asm) build(base uint32) []uint32 {
	out := append([]uint32(nil), a.words...)
	for _, p := range a.patches {
		li, ok := a.labels[p.label]
		if !ok {
			panic("ps3Asm: unknown label " + p.label)
		}
		switch p.kind {
		case ps3PatchBranch:
			off := li - (p.idx + 1)
			if off < -32768 || off > 32767 {
				panic("ps3Asm: branch out of range to " + p.label)
			}
			out[p.idx] |= uint32(uint16(off))
		case ps3PatchJAL:
			addr := base + uint32(4*li)
			out[p.idx] |= (addr >> 2) & 0x3FFFFFF
		case ps3PatchLoadHi:
			addr := base + uint32(4*li)
			out[p.idx] |= (addr >> 16) & 0xFFFF
		case ps3PatchLoadLo:
			addr := base + uint32(4*li)
			out[p.idx] |= addr & 0xFFFF
		}
	}
	return out
}

// spin emits an endless self loop with its NOP
// delay slot.
func (a *ps3Asm) spin(name string) {
	a.label(name)
	a.beq(ps3Zero, ps3Zero, name)
	a.nop()
}

// ----------------------------------------------------------------
// Fixtures: the 16x16 texture table and the ADPCM
// tone (encoder copied in shape from the spu package
// tests, which document the decoder math from the
// psx-spx XA chapter).
// ----------------------------------------------------------------

// ps3TextureWords builds the 128 data words of the
// 16x16 texture: two halfword pixels per word, low
// first, with one solid color per quadrant.
func ps3TextureWords() []uint32 {
	color := func(x, y int) uint32 {
		switch {
		case x < 8 && y < 8:
			return 0x001F // red
		case x >= 8 && y < 8:
			return 0x03E0 // green
		case x < 8:
			return 0x7C00 // blue
		default:
			return 0x7FFF // white
		}
	}
	out := make([]uint32, 0, 128)
	for wi := 0; wi < 128; wi++ {
		var w uint32
		for half := 0; half < 2; half++ {
			p := wi*2 + half
			w |= color(p%16, p/16) << (16 * half)
		}
		out = append(out, w)
	}
	return out
}

var ps3EncF0 = [5]int32{0, 60, 115, 98, 122}
var ps3EncF1 = [5]int32{0, 0, -52, -55, -60}

func ps3EncRecon(nib, shift, f0, f1, old, older int32) int32 {
	s := (nib<<12)>>uint(shift) + (old*f0+older*f1+32)/64
	if s > 0x7FFF {
		s = 0x7FFF
	}
	if s < -0x8000 {
		s = -0x8000
	}
	return s
}

func ps3EncodeBlock(pcm []int16, old, older int32) ([16]byte, int32, int32) {
	var best [16]byte
	bestErr := int64(-1)
	var bestOld, bestOlder int32
	for filter := 0; filter <= 4; filter++ {
		for shift := 0; shift <= 12; shift++ {
			var blk [16]byte
			blk[0] = byte(filter<<4 | shift)
			o, oo := old, older
			err := int64(0)
			var nibs [28]int32
			for i := 0; i < 28; i++ {
				target := int32(pcm[i])
				bn, be := int32(0), int64(-1)
				for nib := int32(-8); nib <= 7; nib++ {
					r := ps3EncRecon(nib, int32(shift), ps3EncF0[filter], ps3EncF1[filter], o, oo)
					d := int64(r - target)
					if be < 0 || d*d < be {
						be, bn = d*d, nib
					}
				}
				r := ps3EncRecon(bn, int32(shift), ps3EncF0[filter], ps3EncF1[filter], o, oo)
				oo, o = o, r
				err += be
				nibs[i] = bn
			}
			if bestErr < 0 || err < bestErr {
				bestErr, bestOld, bestOlder = err, o, oo
				copy(best[:], blk[:])
				for i := 0; i < 28; i++ {
					u := uint8(nibs[i] & 0xF)
					if i&1 == 0 {
						best[2+i/2] |= u
					} else {
						best[2+i/2] |= u << 4
					}
				}
			}
		}
	}
	return best, bestOld, bestOlder
}

// ps3EncodeLoop encodes pcm as a seamlessly looping
// ADPCM stream: three encoding passes so the decoder
// state at the end matches the start, then the loop
// start flag on block 0 and loop end plus repeat on
// the last block.
func ps3EncodeLoop(pcm []int16) []byte {
	var out []byte
	var old, older int32
	for pass := 0; pass < 3; pass++ {
		out = nil
		for i := 0; i+28 <= len(pcm); i += 28 {
			blk, o, oo := ps3EncodeBlock(pcm[i:i+28], old, older)
			old, older = o, oo
			out = append(out, blk[:]...)
		}
	}
	out[1] |= 4           // loop start on first block
	out[len(out)-15] |= 3 // loop end + repeat on last block
	return out
}

// ps3SinePCM makes a sine: 2800 samples with 28
// periods is 441 Hz at the 44100 Hz sample rate.
func ps3SinePCM(n, periods int, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * math.Sin(2*math.Pi*float64(periods)*float64(i)/float64(n)))
	}
	return out
}

// ----------------------------------------------------------------
// PS-X EXE builder.
// ----------------------------------------------------------------

func ps3BuildEXE(text []uint32) []byte {
	out := make([]byte, 0x800+4*len(text))
	copy(out[0:8], "PS-X EXE")
	put := func(off int, v uint32) { binary.LittleEndian.PutUint32(out[off:], v) }
	put(0x10, 0x80010000) // pc0
	put(0x14, 0)          // gp0
	put(0x18, 0x80010000) // t_addr
	put(0x1C, uint32(4*len(text)))
	put(0x30, 0x800F0000) // s_addr, so SP = 0x80100000
	put(0x34, 0x10000)    // s_size
	for i, w := range text {
		binary.LittleEndian.PutUint32(out[0x800+4*i:], w)
	}
	return out
}

// ----------------------------------------------------------------
// The enginetest guest program. Result area base
// 0x80020000 ($s0): PASS magic at +0x00, stage marker
// at +0x100, CD checksum at +0x104, GTE results at
// +0x108 onward. CD buffer at 0x80030000.
// ----------------------------------------------------------------

func ps3EngineWords(tone []byte) []uint32 {
	a := newPS3Asm()
	a.lui(ps3S0, 0x8002) // result base 0x80020000

	// Stage A: critical section plus VBlank events.
	a.li(ps3A0, 1)
	a.syscall() // EnterCriticalSection
	a.li(ps3A0, 0xF0000001)
	a.li(ps3A1, 0x1000)
	a.li(ps3A2, 0x2000)
	a.li(ps3A3, 0)
	a.callBIOS(0xB0, 0x08) // OpenEvent(VBlank, IRQ spec, ready mode, no func)
	a.addu(ps3S1, ps3V0, ps3Zero)
	a.addu(ps3A0, ps3S1, ps3Zero)
	a.callBIOS(0xB0, 0x0C) // EnableEvent(handle)
	a.li(ps3A0, 2)
	a.syscall()                   // ExitCriticalSection
	a.storeWordAbs(0x1F801074, 1) // I_MASK = VBlank
	a.marker(1)
	a.addiu(ps3S2, ps3Zero, 0) // delivery count
	a.label("pollA")
	a.addu(ps3A0, ps3S1, ps3Zero)
	a.callBIOS(0xB0, 0x0B) // TestEvent(handle)
	a.beq(ps3V0, ps3Zero, "pollA")
	a.nop()
	a.addiu(ps3S2, ps3S2, 1)
	a.addiu(ps3T0, ps3Zero, 3)
	a.bne(ps3S2, ps3T0, "pollA")
	a.nop()
	a.marker(2)

	// Stage B: GPU display setup, texture upload,
	// textured quad.
	for _, w := range []uint32{0x00000000, 0x06C60260, 0x07041020, 0x08000001, 0x05000000} {
		a.storeWordAbs(0x1F801814, w) // GP1 direct
	}
	// The display enable GP1 word (0x03000000) is
	// deferred until after the quad is drawn, below:
	// gpu.plot suppresses drawing into the display
	// area while the display is enabled and the E1
	// draw-to-display bit is clear, so enabling first
	// would make the quad undrawable by design.
	for _, w := range []uint32{0xE1000000, 0xE3000000, 0xE403BD3F, 0xE5000000, 0xE6000000} {
		a.gpuCW(w)
	}
	a.gpuCW(0xA0000000) // CPU to VRAM transfer
	a.gpuCW(0x01000000) // dest (0,256): (256<<16)|0
	a.gpuCW(0x00100010) // size 16x16: (16<<16)|16
	a.liLabel(ps3A0, "texTable")
	a.li(ps3A1, 128)
	a.callBIOS(0xA0, 0x4A) // GPU_cwp(table, 128)
	for _, w := range []uint32{
		0x2C808080, // textured quad, neutral color
		0x00200020, // v0 (32,32)
		0x00000000, // uv0 (0,0)
		0x00200060, // v1 (96,32)
		0x01100010, // uv1: texpage 0x0110 in high half, u=16, v=0
		0x00600020, // v2 (32,96)
		0x00001000, // uv2: u=0, v=16
		0x00600060, // v3 (96,96)
		0x00001010, // uv3: u=16, v=16
	} {
		a.gpuCW(w)
	}
	a.storeWordAbs(0x1F801814, 0x03000000) // GP1 display enable, after the draw
	a.marker(3)

	// Stage C: SPU. The tone goes to SPU RAM byte
	// 0x1000 (TSA 0x200 in 8-byte units) through DMA
	// channel 4, then voice 0 plays it looped.
	a.storeHalfAbs(0x1F801DA6, 0x200)           // TSA
	a.storeWordAbs(0x1F8010F0, 0x80000)         // DPCR: channel 4 enable (bit 19)
	a.storeWordLabelAbs(0x1F8010C0, "toneData") // DMA4 MADR
	a.storeWordAbs(0x1F8010C4, 400)             // DMA4 BCR: 1600 bytes in words
	a.storeWordAbs(0x1F8010C8, 0x01000001)      // DMA4 CHCR: from RAM, sync 0, start
	a.storeHalfAbs(0x1F801C00, 0x3FFF)          // voice 0 VOLL
	a.storeHalfAbs(0x1F801C02, 0x3FFF)          // voice 0 VOLR
	a.storeHalfAbs(0x1F801C04, 0x1000)          // voice 0 pitch (44100 Hz)
	a.storeHalfAbs(0x1F801C06, 0x200)           // voice 0 SSA (byte 0x1000)
	a.storeHalfAbs(0x1F801C08, 0x000F)          // voice 0 ADSR1: fast attack, sustain 15
	a.storeHalfAbs(0x1F801C0A, 0x1FDF)          // voice 0 ADSR2: sustain never steps
	a.storeHalfAbs(0x1F801D80, 0x7FFF)          // main volume L
	a.storeHalfAbs(0x1F801D82, 0x7FFF)          // main volume R
	a.storeHalfAbs(0x1F801DB0, 0)               // CD volume L
	a.storeHalfAbs(0x1F801DB2, 0)               // CD volume R
	a.storeHalfAbs(0x1F801DAA, 0xC000)          // SPUCNT: on, unmuted
	a.storeHalfAbs(0x1F801D88, 1)               // KON voice 0
	a.marker(4)

	// Stage D: CD sector 0 into RAM, checksummed.
	a.lui(ps3S4, 0x1F80) // CD register base
	a.sb(ps3Zero, 0x1800, ps3S4)
	a.li(ps3T0, 0x1F)
	a.sb(ps3T0, 0x1803, ps3S4) // IRQ enable (index 0)
	a.sb(ps3Zero, 0x1802, ps3S4)
	a.li(ps3T0, 0x02)
	a.sb(ps3T0, 0x1802, ps3S4)
	a.sb(ps3Zero, 0x1802, ps3S4) // Setloc params 00:02:00 (LBA 0)
	a.li(ps3T0, 0x02)
	a.sb(ps3T0, 0x1801, ps3S4) // Setloc command
	a.lbu(ps3T7, 0x1801, ps3S4)
	a.li(ps3T0, 0x06)
	a.sb(ps3T0, 0x1801, ps3S4) // ReadN command
	a.li(ps3T2, 0)
	a.li(ps3T3, 20000000) // poll cap
	a.label("cdPoll")
	a.lbu(ps3T0, 0x1800, ps3S4)
	a.nop() // load delay before consuming $t0
	a.andi(ps3T0, ps3T0, 0x40)
	a.bne(ps3T0, ps3Zero, "cdReady")
	a.nop()
	a.addiu(ps3T2, ps3T2, 1)
	a.bne(ps3T2, ps3T3, "cdPoll")
	a.nop()
	a.beq(ps3Zero, ps3Zero, "cdTimeout")
	a.nop()
	a.label("cdTimeout")
	a.li(ps3T1, 0xEE)
	a.sw(ps3T1, 0x100, ps3S0)
	a.spin("cdSpin")
	a.label("cdReady")
	a.storeWordAbs(0x1F8010F0, 0x8000) // DPCR: channel 3 enable
	a.storeWordAbs(0x1F8010B0, 0x80030000)
	a.storeWordAbs(0x1F8010B4, 512)
	a.storeWordAbs(0x1F8010B8, 0x01000000) // CHCR: to RAM, sync 0, start
	a.li(ps3T0, 0x80030000)
	a.addiu(ps3T1, ps3Zero, 0)
	a.li(ps3T2, 512)
	a.addiu(ps3T3, ps3Zero, 0)
	a.label("cdSum")
	a.lw(ps3T4, 0, ps3T0)
	a.nop() // load delay before consuming $t4
	a.addu(ps3T3, ps3T3, ps3T4)
	a.addiu(ps3T0, ps3T0, 4)
	a.addiu(ps3T1, ps3T1, 1)
	a.bne(ps3T1, ps3T2, "cdSum")
	a.nop()
	a.sw(ps3T3, 0x104, ps3S0)
	a.marker(5)

	// Stage E: GTE RTPS under the identity rotation.
	for _, cr := range []struct {
		reg uint32
		val uint32
	}{
		{0, 0x00001000},  // R11=1.0 (low), R12=0 (high)
		{1, 0x00000000},  // R13=0, R21=0
		{2, 0x00001000},  // R22=1.0 (low), R23=0
		{3, 0x00000000},  // R31=0, R32=0
		{4, 0x00001000},  // R33=1.0
		{24, 0x00A00000}, // OFX = 160 in 16.16
		{25, 0x00780000}, // OFY = 120 in 16.16
		{26, 200},        // H
		{27, 0},          // DQA
		{28, 0},          // DQB
	} {
		a.li(ps3T0, cr.val)
		a.ctc2(ps3T0, cr.reg)
	}
	a.li(ps3T0, 0)
	a.mtc2(ps3T0, 0) // V0 X=0, Y=0
	a.li(ps3T0, 300)
	a.mtc2(ps3T0, 1) // V0 Z=300
	// COP2 command word: rs field 0x10 selects a GTE
	// command in r3000a execCOP2, low bits carry the
	// command with sf (bit 19) set and opcode 0x01
	// (RTPS).
	a.emit(0x4A080001)
	a.nop()
	a.nop()
	a.mfc2Store(15, 0x108) // SXYP
	a.mfc2Store(19, 0x10C) // SZ3
	a.cfc2Store(31, 0x110) // FLAG
	a.li(ps3T0, 0xFFCE0064)
	a.mtc2(ps3T0, 0) // V0 X=100, Y=-50
	a.li(ps3T0, 300)
	a.mtc2(ps3T0, 1) // V0 Z=300
	a.emit(0x4A080001)
	a.nop()
	a.nop()
	a.mfc2Store(15, 0x114) // SXYP, second point
	a.marker(6)

	// PASS magic, written last.
	a.li(ps3T1, 0x600DF00D)
	a.sw(ps3T1, 0, ps3S0)
	a.spin("spin")

	// Embedded data: the texture table, then the
	// ADPCM tone (100 blocks, 1600 bytes, 400 words).
	a.labelAt("texTable", len(a.words))
	text := append([]uint32(nil), a.words...)
	text = append(text, ps3TextureWords()...)
	a.labelAt("toneData", len(text))
	for i := 0; i+4 <= len(tone); i += 4 {
		text = append(text, binary.LittleEndian.Uint32(tone[i:]))
	}
	// Patch resolution needs the final label table
	// with the data labels in place, so stash the
	// appended words back and build for real.
	a.words = text
	return a.build(0x80010000)
}

// ----------------------------------------------------------------
// The enginetest gate.
// ----------------------------------------------------------------

func ps3SX(w uint32) int { return int(int16(w & 0xFFFF)) }
func ps3SY(w uint32) int { return int(int16(w >> 16)) }

func TestPS1Enginetest(t *testing.T) {
	tone := ps3EncodeLoop(ps3SinePCM(2800, 28, 12000))
	if len(tone) != 1600 {
		t.Fatalf("tone size actual %d bytes, want 1600 (100 ADPCM blocks)", len(tone))
	}
	exe := ps3BuildEXE(ps3EngineWords(tone))

	m := New()
	if err := m.LoadDisc(&seqDisc{sectors: 16}); err != nil {
		t.Fatalf("LoadDisc: %v", err)
	}
	if err := m.LoadEXE(exe); err != nil {
		t.Fatalf("LoadEXE: %v", err)
	}
	var audio []int16
	passed := false
	for chunk := 0; chunk < 40 && !passed; chunk++ {
		m.RunCycles(1000000)
		audio = append(audio, m.AudioBuffer()...)
		if m.Bus.Read32(0x80020000) == 0x600DF00D {
			passed = true
		}
	}
	if !passed {
		t.Fatalf("PASS magic not reached: stage marker actual %d, want 6; PASS word actual 0x%08X, want 0x600DF00D; unimplemented BIOS calls actual %d, want 0",
			m.Bus.Read32(0x80020100), m.Bus.Read32(0x80020000), m.UnimplementedBIOSCallCount())
	}
	// Keep the tone playing so the audio analysis
	// below has well over a second of output: the
	// looped voice keeps sounding while the guest
	// spins on its PASS loop.
	for i := 0; i < 30; i++ {
		m.RunCycles(1000000)
		audio = append(audio, m.AudioBuffer()...)
	}
	if got := m.Bus.Read32(0x80020100); got != 6 {
		t.Errorf("final stage marker actual %d, want 6", got)
	}
	if got := m.UnimplementedBIOSCallCount(); got != 0 {
		t.Errorf("unimplemented BIOS calls actual %d, want 0 (log %v)", got, m.UnimplementedBIOSCalls())
	}

	// GPU probes: one framebuffer pixel per texture
	// quadrant.
	if w, h := m.DisplayWidth(), m.DisplayHeight(); w != 320 || h != 240 {
		t.Errorf("display size actual %dx%d, want 320x240", w, h)
	}
	fb := m.Framebuffer()
	fw := m.DisplayWidth()
	for _, p := range []struct {
		x, y int
		want uint32
	}{
		{48, 48, 0xFFFF0000},
		{80, 48, 0xFF00FF00},
		{48, 80, 0xFF0000FF},
		{80, 80, 0xFFFFFFFF},
	} {
		if got := fb[p.y*fw+p.x]; got != p.want {
			t.Errorf("framebuffer pixel (%d,%d) actual 0x%08X, want 0x%08X", p.x, p.y, got, p.want)
		}
	}

	// CD checksum: the expected sum comes straight
	// from the seqDisc sector 0 pattern, byte i
	// equal to byte(i>>2), packed little endian.
	var wantSum uint32
	for i := 0; i < 2048; i += 4 {
		var w uint32
		for k := 0; k < 4; k++ {
			w |= uint32(byte((i+k)>>2)) << (8 * k)
		}
		wantSum += w
	}
	gotSum := m.Bus.Read32(0x80020104)
	if gotSum != wantSum {
		t.Errorf("CD sector checksum actual 0x%08X, want 0x%08X", gotSum, wantSum)
	}

	// GTE results.
	sxyp1 := m.Bus.Read32(0x80020108)
	if ps3SX(sxyp1) != 160 || ps3SY(sxyp1) != 120 {
		t.Errorf("RTPS centered point actual (%d,%d), want (160,120) exactly (SXYP word 0x%08X)",
			ps3SX(sxyp1), ps3SY(sxyp1), sxyp1)
	}
	if got := m.Bus.Read32(0x8002010C); got != 300 {
		t.Errorf("RTPS SZ3 actual %d, want 300", got)
	}
	if flag := m.Bus.Read32(0x80020110); flag&(1<<31) != 0 {
		t.Errorf("RTPS FLAG actual 0x%08X, want bit 31 clear", flag)
	}
	sxyp2 := m.Bus.Read32(0x80020114)
	if sx, sy := ps3SX(sxyp2), ps3SY(sxyp2); sx < 223 || sx > 229 || sy < 84 || sy > 90 {
		t.Errorf("RTPS second point actual (%d,%d), want SX in [223,229] and SY in [84,90] (SXYP word 0x%08X)", sx, sy, sxyp2)
	}

	// Audio: skip the first 0.25 s, count positive
	// zero crossings on the left channel over the
	// next 0.5 s.
	pairs := len(audio) / 2
	if pairs < 11025+22050 {
		t.Fatalf("audio length actual %d sample pairs, want at least %d for the analysis window", pairs, 11025+22050)
	}
	crossings := 0
	prev := int16(0)
	for i := 0; i < 22050; i++ {
		cur := audio[2*(11025+i)]
		if i > 0 && prev < 0 && cur >= 0 {
			crossings++
		}
		prev = cur
	}
	freq := float64(crossings) / 0.5
	t.Logf("enginetest: stage marker 6, CD checksum 0x%08X, SXYP1 (%d,%d), SXYP2 (%d,%d), audio %d crossings in 0.5 s = %.1f Hz",
		gotSum, ps3SX(sxyp1), ps3SY(sxyp1), ps3SX(sxyp2), ps3SY(sxyp2), crossings, freq)
	if freq < 415 || freq > 465 {
		t.Errorf("audio frequency actual %.1f Hz, want in [415,465]", freq)
	}

	// Determinism leg: a fixed cycle run, a mid
	// Serialize, then identical continuations on the
	// original machine and on a fresh machine that
	// only Deserializes the mid state. Pending audio
	// is drained and discarded on both sides of the
	// restore point, because the serialized SPU audio
	// buffer is capped and the original machine would
	// otherwise carry a longer pending tail.
	m2 := New()
	if err := m2.LoadDisc(&seqDisc{sectors: 16}); err != nil {
		t.Fatalf("determinism LoadDisc: %v", err)
	}
	if err := m2.LoadEXE(exe); err != nil {
		t.Fatalf("determinism LoadEXE: %v", err)
	}
	m2.RunCycles(12000000)
	mid := m2.Serialize()
	_ = m2.AudioBuffer() // discard pending, see above
	m2.RunCycles(2000000)
	audio2 := m2.AudioBuffer()
	wantState := m2.Serialize()
	wantFB := append([]uint32(nil), m2.Framebuffer()...)
	wantRes := make([]uint32, 128)
	for i := range wantRes {
		wantRes[i] = m2.Bus.Read32(0x80020000 + uint32(4*i))
	}
	if wantRes[0] != 0x600DF00D {
		t.Fatalf("determinism leg: m2 PASS word actual 0x%08X, want 0x600DF00D after 14M cycles (stage marker %d)",
			wantRes[0], m2.Bus.Read32(0x80020100))
	}

	m3 := New()
	if err := m3.LoadDisc(&seqDisc{sectors: 16}); err != nil {
		t.Fatalf("determinism LoadDisc m3: %v", err)
	}
	if err := m3.Deserialize(mid); err != nil {
		t.Fatalf("determinism Deserialize: %v", err)
	}
	_ = m3.AudioBuffer() // discard restored pending
	m3.RunCycles(2000000)
	audio3 := m3.AudioBuffer()
	if len(audio3) != len(audio2) {
		t.Fatalf("determinism audio length actual %d, want %d", len(audio3), len(audio2))
	}
	for i := range audio2 {
		if audio3[i] != audio2[i] {
			t.Fatalf("determinism audio sample %d actual %d, want %d", i, audio3[i], audio2[i])
		}
	}
	if got := m3.Serialize(); !bytes.Equal(got, wantState) {
		t.Errorf("determinism state after identical continuation diverged: actual %d bytes vs want %d bytes, byte equal false", len(got), len(wantState))
		for i := range wantState {
			if got[i] != wantState[i] {
				t.Logf("first divergent state byte at offset %d: actual 0x%02X, want 0x%02X", i, got[i], wantState[i])
				break
			}
		}
	}
	gotFB := m3.Framebuffer()
	if len(gotFB) != len(wantFB) {
		t.Fatalf("determinism framebuffer size actual %d, want %d", len(gotFB), len(wantFB))
	}
	for i := range wantFB {
		if gotFB[i] != wantFB[i] {
			t.Fatalf("determinism framebuffer pixel %d actual 0x%08X, want 0x%08X", i, gotFB[i], wantFB[i])
		}
	}
	for i := range wantRes {
		if got := m3.Bus.Read32(0x80020000 + uint32(4*i)); got != wantRes[i] {
			t.Fatalf("determinism result word %d actual 0x%08X, want 0x%08X", i, got, wantRes[i])
		}
	}
	t.Logf("determinism: state bytes equal %v, audio samples after restore %d, framebuffer pixels %d",
		bytes.Equal(m3.Serialize(), wantState), len(audio2), len(wantFB))
}

// ----------------------------------------------------------------
// BIOS HLE unit tests.
// ----------------------------------------------------------------

func TestBIOSUnimplementedLogged(t *testing.T) {
	a := newPS3Asm()
	a.li(ps3A0, 0)
	a.li(ps3A1, 0)
	a.li(ps3A2, 0)
	a.li(ps3A3, 0)
	a.callBIOS(0xA0, 0x7F) // unimplemented A function
	a.lui(ps3T0, 0x8002)
	a.sw(ps3V0, 0x210, ps3T0)
	a.spin("spin")
	m := New()
	loadProg(m, 0x80010000, a.build(0x80010000))
	setPC(m, 0x80010000)
	m.RunCycles(10000)
	if got := m.Bus.Read32(0x80020210); got != 0xFFFFFFFF {
		t.Errorf("unimplemented call return actual 0x%08X, want 0xFFFFFFFF", got)
	}
	if got := m.UnimplementedBIOSCallCount(); got < 1 {
		t.Fatalf("unimplemented call count actual %d, want at least 1", got)
	}
	calls := m.UnimplementedBIOSCalls()
	if len(calls) < 1 {
		t.Fatalf("unimplemented call log length actual %d, want at least 1", len(calls))
	}
	if calls[0].Table != 'A' || calls[0].Func != 0x7F {
		t.Errorf("unimplemented call log entry actual {%c, 0x%02X}, want {A, 0x7F}", calls[0].Table, calls[0].Func)
	}
}

func TestBIOSCallbackEvent(t *testing.T) {
	a := newPS3Asm()
	a.li(ps3A0, 0xF0000001)
	a.li(ps3A1, 0x1000)
	a.li(ps3A2, 0x1000) // callback mode
	a.liLabel(ps3A3, "callback")
	a.callBIOS(0xB0, 0x08) // OpenEvent
	a.addu(ps3S1, ps3V0, ps3Zero)
	a.addu(ps3A0, ps3S1, ps3Zero)
	a.callBIOS(0xB0, 0x0C)        // EnableEvent
	a.storeWordAbs(0x1F801074, 1) // I_MASK = VBlank
	a.label("spin")
	a.lui(ps3T2, 0x8002)
	a.lw(ps3T0, 0x204, ps3T2)
	a.nop() // load delay
	a.addiu(ps3T0, ps3T0, 1)
	a.sw(ps3T0, 0x204, ps3T2)
	a.beq(ps3Zero, ps3Zero, "spin")
	a.nop()
	for len(a.words) < 64 {
		a.nop()
	}
	a.label("callback") // at 0x80010100
	a.lui(ps3T0, 0x8002)
	a.lw(ps3T1, 0x200, ps3T0)
	a.nop() // load delay
	a.addiu(ps3T1, ps3T1, 1)
	a.sw(ps3T1, 0x200, ps3T0)
	a.jr(ps3RA)
	a.nop()
	m := New()
	loadProg(m, 0x80010000, a.build(0x80010000))
	setPC(m, 0x80010000)
	for i := 0; i < 3; i++ {
		m.RunFrame()
	}
	got := m.Bus.Read32(0x80020200)
	t.Logf("callback event: counter %d after 3 frames", got)
	if got < 2 {
		t.Errorf("callback counter actual %d, want at least 2 after 3 frames", got)
	}
	h1 := m.Bus.Read32(0x80020204)
	m.RunCycles(100000)
	h2 := m.Bus.Read32(0x80020204)
	if h2 == h1 {
		t.Errorf("main spin heartbeat actual %d after more cycles, want a value different from %d (spin still alive)", h2, h1)
	}
}

func TestBIOSSetRCntRoundTrip(t *testing.T) {
	a := newPS3Asm()
	a.li(ps3A0, 0)
	a.li(ps3A1, 1234)
	a.li(ps3A2, 0x0058)
	a.callBIOS(0xC0, 0x19) // SetRCnt(0, 1234, 0x0058)
	a.li(ps3A0, 0)
	a.callBIOS(0xC0, 0x1A) // GetRCnt(0)
	a.lui(ps3T0, 0x8002)
	a.sw(ps3V0, 0x220, ps3T0)
	a.li(ps3A0, 0)
	a.callBIOS(0xC0, 0x1B) // ResetRCnt(0)
	a.li(ps3A0, 0)
	a.callBIOS(0xC0, 0x1A) // GetRCnt(0)
	a.lui(ps3T0, 0x8002)
	a.sw(ps3V0, 0x224, ps3T0)
	a.spin("spin")
	m := New()
	loadProg(m, 0x80010000, a.build(0x80010000))
	setPC(m, 0x80010000)
	m.RunCycles(10000)
	if got := m.Timers.T[0].Target; got != 1234 {
		t.Errorf("timer 0 target actual %d, want 1234", got)
	}
	if got := m.Timers.T[0].Mode; got != 0x0058 {
		t.Errorf("timer 0 mode actual 0x%04X, want 0x0058", got)
	}
	first := m.Bus.Read32(0x80020220)
	second := m.Bus.Read32(0x80020224)
	t.Logf("SetRCnt round trip: first GetRCnt %d, second GetRCnt after reset %d", first, second)
	if first > 1234 {
		t.Errorf("first GetRCnt actual %d, want at most 1234", first)
	}
	if !(second < first || second < 100) {
		t.Errorf("second GetRCnt actual %d, want less than first (%d) or less than 100", second, first)
	}
}

func TestBIOSInitPAD2Fill(t *testing.T) {
	a := newPS3Asm()
	a.li(ps3A0, 0x80020300)
	a.li(ps3A1, 0x22)
	a.li(ps3A2, 0x80020340)
	a.li(ps3A3, 0x22)
	a.callBIOS(0xB0, 0x12) // InitPAD2
	a.callBIOS(0xB0, 0x13) // StartPAD2
	a.spin("spin")
	m := New()
	loadProg(m, 0x80010000, a.build(0x80010000))
	setPC(m, 0x80010000)
	m.SetButtons(ButtonStart | ButtonCross)
	m.RunFrame()
	m.RunFrame()
	for _, c := range []struct {
		addr uint32
		want uint8
	}{
		{0x80020300, 0x00}, // port 1 status okay
		{0x80020301, 0x41}, // digital pad ID
		{0x80020302, 0xF7}, // low buttons: Start is bit 3, byte = ^0x08
		{0x80020303, 0xBF}, // high buttons: Cross is bit 14, byte = ^0x40
		{0x80020340, 0xFF}, // port 2: no device
	} {
		if got := m.Bus.Read8(c.addr); got != c.want {
			t.Errorf("PAD buffer byte at 0x%08X actual 0x%02X, want 0x%02X", c.addr, got, c.want)
		}
	}
}
