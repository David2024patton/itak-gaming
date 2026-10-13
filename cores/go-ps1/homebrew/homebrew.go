package homebrew

// Homebrew fixtures for the go-ps1 core: a small MIPS
// assembler, the PS1-3 enginetest guest program (a
// PS-X EXE that drives the BIOS event system, a GPU
// textured quad, SPU ADPCM playback, a CD sector read
// and GTE projection, then writes a PASS magic), and
// synthetic disc images carrying that EXE. The system
// package gates use the in-process builders; the
// libretro shell gates use the file forms. Everything
// here is synthetic: no Sony code, no game data.

import (
	"encoding/binary"
	"math"
)

// ----------------------------------------------------------------
// Register numbers used by the assembler.
// ----------------------------------------------------------------

const (
	Zero = 0
	V0   = 2
	A0   = 4
	A1   = 5
	A2   = 6
	A3   = 7
	T0   = 8
	T1   = 9
	T2   = 10
	T3   = 11
	T4   = 12
	T5   = 13
	T6   = 14
	T7   = 15
	S0   = 16
	S1   = 17
	S2   = 18
	S4   = 20
	RA   = 31
)

// ----------------------------------------------------------------
// A small MIPS assembler with label fixups.
// ----------------------------------------------------------------

const (
	patchBranch = iota
	patchJAL
	patchLoadHi
	patchLoadLo
)

type patch struct {
	idx   int
	kind  int
	label string
}

type Asm struct {
	words   []uint32
	labels  map[string]int
	patches []patch
}

func NewAsm() *Asm {
	return &Asm{labels: map[string]int{}}
}

func (a *Asm) Emit(w uint32) int {
	a.words = append(a.words, w)
	return len(a.words) - 1
}

func (a *Asm) Label(name string) { a.labels[name] = len(a.words) }

func (a *Asm) LabelAt(name string, idx int) { a.labels[name] = idx }

func (a *Asm) Nop() { a.Emit(0x00000000) }

func (a *Asm) Lui(rt, imm uint32) {
	a.Emit(0x3C000000 | rt<<16 | (imm & 0xFFFF))
}

func (a *Asm) Ori(rt, rs, imm uint32) {
	a.Emit(0x34000000 | rs<<21 | rt<<16 | (imm & 0xFFFF))
}

// li loads a 32-bit constant in one or two words.
func (a *Asm) Li(rt, v uint32) {
	switch {
	case v>>16 == 0:
		a.Ori(rt, Zero, v)
	case v&0xFFFF == 0:
		a.Lui(rt, v>>16)
	default:
		a.Lui(rt, v>>16)
		a.Ori(rt, rt, v)
	}
}

// liLabel loads the absolute address of a label.
func (a *Asm) LiLabel(rt uint32, name string) {
	idx := a.Emit(0x3C000000 | rt<<16)
	a.patches = append(a.patches, patch{idx: idx, kind: patchLoadHi, label: name})
	idx = a.Emit(0x34000000 | rt<<21 | rt<<16)
	a.patches = append(a.patches, patch{idx: idx, kind: patchLoadLo, label: name})
}

func (a *Asm) Addiu(rt, rs uint32, imm int) {
	a.Emit(0x24000000 | rs<<21 | rt<<16 | uint32(uint16(imm)))
}

func (a *Asm) Addu(rd, rs, rt uint32) {
	a.Emit(0x00000021 | rs<<21 | rt<<16 | rd<<11)
}

func (a *Asm) Andi(rt, rs, imm uint32) {
	a.Emit(0x30000000 | rs<<21 | rt<<16 | (imm & 0xFFFF))
}

func (a *Asm) Lw(rt uint32, off int, rs uint32) {
	a.Emit(0x8C000000 | rs<<21 | rt<<16 | uint32(uint16(off)))
}

func (a *Asm) Sw(rt uint32, off int, rs uint32) {
	a.Emit(0xAC000000 | rs<<21 | rt<<16 | uint32(uint16(off)))
}

func (a *Asm) Lbu(rt uint32, off int, rs uint32) {
	a.Emit(0x90000000 | rs<<21 | rt<<16 | uint32(uint16(off)))
}

func (a *Asm) Sb(rt uint32, off int, rs uint32) {
	a.Emit(0xA0000000 | rs<<21 | rt<<16 | uint32(uint16(off)))
}

func (a *Asm) Sh(rt uint32, off int, rs uint32) {
	a.Emit(0xA4000000 | rs<<21 | rt<<16 | uint32(uint16(off)))
}

func (a *Asm) Beq(rs, rt uint32, label string) {
	idx := a.Emit(0x10000000 | rs<<21 | rt<<16)
	a.patches = append(a.patches, patch{idx: idx, kind: patchBranch, label: label})
}

func (a *Asm) Bne(rs, rt uint32, label string) {
	idx := a.Emit(0x14000000 | rs<<21 | rt<<16)
	a.patches = append(a.patches, patch{idx: idx, kind: patchBranch, label: label})
}

func (a *Asm) JalAbs(addr uint32) {
	a.Emit(0x0C000000 | ((addr >> 2) & 0x3FFFFFF))
}

func (a *Asm) Jr(rs uint32) { a.Emit(0x00000008 | rs<<21) }

func (a *Asm) Syscall() { a.Emit(0x0000000C) }

// callBIOS emits a vector call: function number in
// $t1, jal to the vector, NOP in the delay slot (the
// HLE intercepts when the PC reaches the vector,
// after the delay slot has executed).
func (a *Asm) CallBIOS(vector uint32, fn int) {
	a.Addiu(T1, Zero, fn)
	a.JalAbs(vector)
	a.Nop()
}

// storeWordAbs stores a constant to an absolute
// address, using $t5 and $t6 as scratch.
func (a *Asm) StoreWordAbs(addr, val uint32) {
	a.Li(T5, addr)
	a.Li(T6, val)
	a.Sw(T6, 0, T5)
}

// storeWordLabelAbs stores a label address to an
// absolute address.
func (a *Asm) StoreWordLabelAbs(addr uint32, label string) {
	a.Li(T5, addr)
	a.LiLabel(T6, label)
	a.Sw(T6, 0, T5)
}

// storeHalfAbs stores a constant halfword to an
// absolute address.
func (a *Asm) StoreHalfAbs(addr, val uint32) {
	a.Li(T5, addr)
	a.Li(T6, val)
	a.Sh(T6, 0, T5)
}

// marker records stage completion: the stage number
// goes to the shared marker word at result+0x100.
// $s0 must hold the result base 0x80020000.
func (a *Asm) Marker(n int) {
	a.Li(T1, uint32(n))
	a.Sw(T1, 0x100, S0)
}

// gpuCW sends one GP0 word through BIOS GPU_cw.
func (a *Asm) GpuCW(w uint32) {
	a.Li(A0, w)
	a.CallBIOS(0xA0, 0x49)
}

func (a *Asm) Mtc2(rt, rd uint32) { a.Emit(0x48800000 | rt<<16 | rd<<11) }
func (a *Asm) Ctc2(rt, rd uint32) { a.Emit(0x48C00000 | rt<<16 | rd<<11) }

// mfc2Store reads a GTE data register into $t0 (with
// the load delay NOP) and stores it at off($s0).
func (a *Asm) Mfc2Store(rd uint32, off int) {
	a.Emit(0x48000000 | T0<<16 | rd<<11) // mfc2 t0, rd
	a.Nop()
	a.Sw(T0, off, S0)
}

// cfc2Store reads a GTE control register the same
// way as mfc2Store.
func (a *Asm) Cfc2Store(rd uint32, off int) {
	a.Emit(0x48400000 | T0<<16 | rd<<11) // cfc2 t0, rd
	a.Nop()
	a.Sw(T0, off, S0)
}

// build resolves all patches against base (the
// address word 0 lands at) and returns the words.
func (a *Asm) Build(base uint32) []uint32 {
	out := append([]uint32(nil), a.words...)
	for _, p := range a.patches {
		li, ok := a.labels[p.label]
		if !ok {
			panic("homebrew: unknown label " + p.label)
		}
		switch p.kind {
		case patchBranch:
			off := li - (p.idx + 1)
			if off < -32768 || off > 32767 {
				panic("homebrew: branch out of range to " + p.label)
			}
			out[p.idx] |= uint32(uint16(off))
		case patchJAL:
			addr := base + uint32(4*li)
			out[p.idx] |= (addr >> 2) & 0x3FFFFFF
		case patchLoadHi:
			addr := base + uint32(4*li)
			out[p.idx] |= (addr >> 16) & 0xFFFF
		case patchLoadLo:
			addr := base + uint32(4*li)
			out[p.idx] |= addr & 0xFFFF
		}
	}
	return out
}

// spin emits an endless self loop with its NOP
// delay slot.
func (a *Asm) Spin(name string) {
	a.Label(name)
	a.Beq(Zero, Zero, name)
	a.Nop()
}

// ----------------------------------------------------------------
// Fixtures: the 16x16 texture table and the ADPCM
// tone (encoder copied in shape from the spu package
// tests, which document the decoder math from the
// psx-spx XA chapter).
// ----------------------------------------------------------------

// textureWords builds the 128 data words of the
// 16x16 texture: two halfword pixels per word, low
// first, with one solid color per quadrant.
func textureWords() []uint32 {
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

func encRecon(nib, shift, f0, f1, old, older int32) int32 {
	s := (nib<<12)>>uint(shift) + (old*f0+older*f1+32)/64
	if s > 0x7FFF {
		s = 0x7FFF
	}
	if s < -0x8000 {
		s = -0x8000
	}
	return s
}

func encodeBlock(pcm []int16, old, older int32) ([16]byte, int32, int32) {
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
					r := encRecon(nib, int32(shift), ps3EncF0[filter], ps3EncF1[filter], o, oo)
					d := int64(r - target)
					if be < 0 || d*d < be {
						be, bn = d*d, nib
					}
				}
				r := encRecon(bn, int32(shift), ps3EncF0[filter], ps3EncF1[filter], o, oo)
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

// EncodeLoop encodes pcm as a seamlessly looping
// ADPCM stream: three encoding passes so the decoder
// state at the end matches the start, then the loop
// start flag on block 0 and loop end plus repeat on
// the last block.
func EncodeLoop(pcm []int16) []byte {
	var out []byte
	var old, older int32
	for pass := 0; pass < 3; pass++ {
		out = nil
		for i := 0; i+28 <= len(pcm); i += 28 {
			blk, o, oo := encodeBlock(pcm[i:i+28], old, older)
			old, older = o, oo
			out = append(out, blk[:]...)
		}
	}
	out[1] |= 4           // loop start on first block
	out[len(out)-15] |= 3 // loop end + repeat on last block
	return out
}

// SinePCM makes a sine: 2800 samples with 28
// periods is 441 Hz at the 44100 Hz sample rate.
func SinePCM(n, periods int, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * math.Sin(2*math.Pi*float64(periods)*float64(i)/float64(n)))
	}
	return out
}

// ----------------------------------------------------------------
// PS-X EXE builder.
// ----------------------------------------------------------------

func BuildEXE(text []uint32) []byte {
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

func EngineWords(tone []byte) []uint32 {
	a := NewAsm()
	a.Lui(S0, 0x8002) // result base 0x80020000

	// Stage A: critical section plus VBlank events.
	a.Li(A0, 1)
	a.Syscall() // EnterCriticalSection
	a.Li(A0, 0xF0000001)
	a.Li(A1, 0x1000)
	a.Li(A2, 0x2000)
	a.Li(A3, 0)
	a.CallBIOS(0xB0, 0x08) // OpenEvent(VBlank, IRQ spec, ready mode, no func)
	a.Addu(S1, V0, Zero)
	a.Addu(A0, S1, Zero)
	a.CallBIOS(0xB0, 0x0C) // EnableEvent(handle)
	a.Li(A0, 2)
	a.Syscall()                   // ExitCriticalSection
	a.StoreWordAbs(0x1F801074, 1) // I_MASK = VBlank
	a.Marker(1)
	a.Addiu(S2, Zero, 0) // delivery count
	a.Label("pollA")
	a.Addu(A0, S1, Zero)
	a.CallBIOS(0xB0, 0x0B) // TestEvent(handle)
	a.Beq(V0, Zero, "pollA")
	a.Nop()
	a.Addiu(S2, S2, 1)
	a.Addiu(T0, Zero, 3)
	a.Bne(S2, T0, "pollA")
	a.Nop()
	a.Marker(2)

	// Stage B: GPU display setup, texture upload,
	// textured quad.
	for _, w := range []uint32{0x00000000, 0x06C60260, 0x07041020, 0x08000001, 0x05000000} {
		a.StoreWordAbs(0x1F801814, w) // GP1 direct
	}
	// The display enable GP1 word (0x03000000) is
	// deferred until after the quad is drawn, below:
	// gpu.plot suppresses drawing into the display
	// area while the display is enabled and the E1
	// draw-to-display bit is clear, so enabling first
	// would make the quad undrawable by design.
	for _, w := range []uint32{0xE1000000, 0xE3000000, 0xE403BD3F, 0xE5000000, 0xE6000000} {
		a.GpuCW(w)
	}
	a.GpuCW(0xA0000000) // CPU to VRAM transfer
	a.GpuCW(0x01000000) // dest (0,256): (256<<16)|0
	a.GpuCW(0x00100010) // size 16x16: (16<<16)|16
	a.LiLabel(A0, "texTable")
	a.Li(A1, 128)
	a.CallBIOS(0xA0, 0x4A) // GPU_cwp(table, 128)
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
		a.GpuCW(w)
	}
	a.StoreWordAbs(0x1F801814, 0x03000000) // GP1 display enable, after the draw
	a.Marker(3)

	// Stage C: SPU. The tone goes to SPU RAM byte
	// 0x1000 (TSA 0x200 in 8-byte units) through DMA
	// channel 4, then voice 0 plays it looped.
	a.StoreHalfAbs(0x1F801DA6, 0x200)           // TSA
	a.StoreWordAbs(0x1F8010F0, 0x80000)         // DPCR: channel 4 enable (bit 19)
	a.StoreWordLabelAbs(0x1F8010C0, "toneData") // DMA4 MADR
	a.StoreWordAbs(0x1F8010C4, 400)             // DMA4 BCR: 1600 bytes in words
	a.StoreWordAbs(0x1F8010C8, 0x01000001)      // DMA4 CHCR: from RAM, sync 0, start
	a.StoreHalfAbs(0x1F801C00, 0x3FFF)          // voice 0 VOLL
	a.StoreHalfAbs(0x1F801C02, 0x3FFF)          // voice 0 VOLR
	a.StoreHalfAbs(0x1F801C04, 0x1000)          // voice 0 pitch (44100 Hz)
	a.StoreHalfAbs(0x1F801C06, 0x200)           // voice 0 SSA (byte 0x1000)
	a.StoreHalfAbs(0x1F801C08, 0x000F)          // voice 0 ADSR1: fast attack, sustain 15
	a.StoreHalfAbs(0x1F801C0A, 0x1FDF)          // voice 0 ADSR2: sustain never steps
	a.StoreHalfAbs(0x1F801D80, 0x7FFF)          // main volume L
	a.StoreHalfAbs(0x1F801D82, 0x7FFF)          // main volume R
	a.StoreHalfAbs(0x1F801DB0, 0)               // CD volume L
	a.StoreHalfAbs(0x1F801DB2, 0)               // CD volume R
	a.StoreHalfAbs(0x1F801DAA, 0xC000)          // SPUCNT: on, unmuted
	a.StoreHalfAbs(0x1F801D88, 1)               // KON voice 0
	a.Marker(4)

	// Stage D: CD sector 0 into RAM, checksummed.
	a.Lui(S4, 0x1F80) // CD register base
	a.Sb(Zero, 0x1800, S4)
	a.Li(T0, 0x1F)
	a.Sb(T0, 0x1803, S4) // IRQ enable (index 0)
	a.Sb(Zero, 0x1802, S4)
	a.Li(T0, 0x02)
	a.Sb(T0, 0x1802, S4)
	a.Sb(Zero, 0x1802, S4) // Setloc params 00:02:00 (LBA 0)
	a.Li(T0, 0x02)
	a.Sb(T0, 0x1801, S4) // Setloc command
	a.Lbu(T7, 0x1801, S4)
	a.Li(T0, 0x06)
	a.Sb(T0, 0x1801, S4) // ReadN command
	a.Li(T2, 0)
	a.Li(T3, 20000000) // poll cap
	a.Label("cdPoll")
	a.Lbu(T0, 0x1800, S4)
	a.Nop() // load delay before consuming $t0
	a.Andi(T0, T0, 0x40)
	a.Bne(T0, Zero, "cdReady")
	a.Nop()
	a.Addiu(T2, T2, 1)
	a.Bne(T2, T3, "cdPoll")
	a.Nop()
	a.Beq(Zero, Zero, "cdTimeout")
	a.Nop()
	a.Label("cdTimeout")
	a.Li(T1, 0xEE)
	a.Sw(T1, 0x100, S0)
	a.Spin("cdSpin")
	a.Label("cdReady")
	a.StoreWordAbs(0x1F8010F0, 0x8000) // DPCR: channel 3 enable
	a.StoreWordAbs(0x1F8010B0, 0x80030000)
	a.StoreWordAbs(0x1F8010B4, 512)
	a.StoreWordAbs(0x1F8010B8, 0x01000000) // CHCR: to RAM, sync 0, start
	a.Li(T0, 0x80030000)
	a.Addiu(T1, Zero, 0)
	a.Li(T2, 512)
	a.Addiu(T3, Zero, 0)
	a.Label("cdSum")
	a.Lw(T4, 0, T0)
	a.Nop() // load delay before consuming $t4
	a.Addu(T3, T3, T4)
	a.Addiu(T0, T0, 4)
	a.Addiu(T1, T1, 1)
	a.Bne(T1, T2, "cdSum")
	a.Nop()
	a.Sw(T3, 0x104, S0)
	a.Marker(5)

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
		a.Li(T0, cr.val)
		a.Ctc2(T0, cr.reg)
	}
	a.Li(T0, 0)
	a.Mtc2(T0, 0) // V0 X=0, Y=0
	a.Li(T0, 300)
	a.Mtc2(T0, 1) // V0 Z=300
	// COP2 command word: rs field 0x10 selects a GTE
	// command in r3000a execCOP2, low bits carry the
	// command with sf (bit 19) set and opcode 0x01
	// (RTPS).
	a.Emit(0x4A080001)
	a.Nop()
	a.Nop()
	a.Mfc2Store(15, 0x108) // SXYP
	a.Mfc2Store(19, 0x10C) // SZ3
	a.Cfc2Store(31, 0x110) // FLAG
	a.Li(T0, 0xFFCE0064)
	a.Mtc2(T0, 0) // V0 X=100, Y=-50
	a.Li(T0, 300)
	a.Mtc2(T0, 1) // V0 Z=300
	a.Emit(0x4A080001)
	a.Nop()
	a.Nop()
	a.Mfc2Store(15, 0x114) // SXYP, second point
	a.Marker(6)

	// PASS magic, written last.
	a.Li(T1, 0x600DF00D)
	a.Sw(T1, 0, S0)
	a.Spin("spin")

	// Embedded data: the texture table, then the
	// ADPCM tone (100 blocks, 1600 bytes, 400 words).
	a.LabelAt("texTable", len(a.words))
	text := append([]uint32(nil), a.words...)
	text = append(text, textureWords()...)
	a.LabelAt("toneData", len(text))
	for i := 0; i+4 <= len(tone); i += 4 {
		text = append(text, binary.LittleEndian.Uint32(tone[i:]))
	}
	// Patch resolution needs the final label table
	// with the data labels in place, so stash the
	// appended words back and build for real.
	a.words = text
	return a.Build(0x80010000)
}

// Len returns the number of words emitted so far.
// Tests pad programs to fixed offsets with it.
func (a *Asm) Len() int { return len(a.words) }

// EnginetestEXE returns the canonical enginetest
// guest: the engine program playing the 440 Hz
// ADPCM tone (2,800 samples, 28 periods, amplitude
// 12,000; the encoded loop is exactly 1,600 bytes,
// 100 ADPCM blocks), wrapped as a PS-X EXE.
func EnginetestEXE() []byte {
	tone := EncodeLoop(SinePCM(2800, 28, 12000))
	return BuildEXE(EngineWords(tone))
}
