package system

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// DC-3 whole machine gate: one SH-4 program, one ARM sound
// driver, one Maple poll, one fog scene, running together.
//
// The SH-4 program (house style: hand assembled words plus a
// data page at 0x0C000800) copies the ARM driver image from
// main RAM to AICA RAM, releases the ARM7 by clearing ARMRST,
// builds a two entry Maple DMA table (Get Condition on port 0,
// Device Request on port 1), programs the Holly masks for the
// Maple DMA done event (normal bank bit 12, level 2) and the
// AICA external event (bit 1, level 2), starts the Maple DMA,
// and spins. The interrupt handler at VBR+0x600 counts Maple
// completions in scratch word 0 (acking ISTNRM bit 12) and
// AICA timer interrupts in scratch word 1 (clearing the AICA
// pending bit through MCIRE).
//
// The ARM driver (hand assembled ARM words, a synthetic
// fixture, not firmware) programs slot 0 for a 64 sample
// 16-bit PCM loop at unity pitch, keys it on, and arms timer
// A (TIMA 250, prescaler 1) at level 2. The ARM takes timer
// A as an FIQ (SCIEB bit 6) and end-of-interrupts it in its
// handler, which counts entries in wave RAM at 0x2000. The
// SH-4 side watches timer B instead (MCIEB bits 6 and 7):
// timer B is not in SCIEB, so the ARM never acknowledges it
// and its pending bit survives until the SH-4 handler clears
// it through MCIRE, which is what the SH-4 count measures.
//
// The fog scene is the pvr package gate scene rebuilt through
// the bus: a black 32 by 32 quad at Z 0.5, FOG_DENSITY 0x8802
// (4.25), fog table entries 8 and 9 with low bytes 100 and
// 156, FOG_COL_RAM white. The center pixel must present as
// 0xFF848284, the same value the pvr gate pins.
//
// The gate then saves state mid run, runs one more frame on
// the original and on a restored machine, and requires byte
// identical continuation: state bytes, drained audio, and the
// presented framebuffer.

// armDriverWords is the ARM7 sound driver, loaded at wave RAM
// 0 (the ARM reset entry). Register bases: slot registers at
// 0x00800000, common registers at 0x00802800.
var armDriverWords = []uint32{
	0xEA000006, // 0x00: B start (0x20)
	0x00000000, // 0x04: undefined vector (unused)
	0x00000000, // 0x08: SWI vector (unused)
	0x00000000, // 0x0C: prefetch abort vector (unused)
	0x00000000, // 0x10: data abort vector (unused)
	0x00000000, // 0x14: reserved
	0xEAFFFFFE, // 0x18: IRQ vector: spin (IRQ stays masked)
	0xEA00002C, // 0x1C: FIQ vector: B fiq (0xD4)
	// start:
	0xE59FD0CC, // 0x20: LDR SP, [PC, #0xCC]   ; literal 0xF4 = 0x0007F000
	0xE59F00CC, // 0x24: LDR R0, [PC, #0xCC]   ; literal 0xF8 = 0x00800000
	0xE3A01C42, // 0x28: MOV R1, #0x4200       ; KYONB + LPCTL, PCM16
	0xE5801000, // 0x2C: STR R1, [R0]          ; slot 0 reg 0
	0xE3A01A01, // 0x30: MOV R1, #0x1000
	0xE5801004, // 0x34: STR R1, [R0, #4]      ; SA low = 0x1000
	0xE3A01000, // 0x38: MOV R1, #0
	0xE5801008, // 0x3C: STR R1, [R0, #8]      ; LSA = 0
	0xE3A0103F, // 0x40: MOV R1, #63
	0xE580100C, // 0x44: STR R1, [R0, #0xC]    ; LEA = 63
	0xE59F10AC, // 0x48: LDR R1, [PC, #0xAC]   ; literal 0xFC = 0x3C1F
	0xE5801014, // 0x4C: STR R1, [R0, #0x14]   ; KRS off, DL 0, RR 31
	0xE3A0101F, // 0x50: MOV R1, #0x1F
	0xE5801010, // 0x54: STR R1, [R0, #0x10]   ; AR 31, D1R 0, D2R 0
	0xE3A01000, // 0x58: MOV R1, #0
	0xE5801018, // 0x5C: STR R1, [R0, #0x18]   ; OCT 0, FNS 0
	0xE580101C, // 0x60: STR R1, [R0, #0x1C]   ; LFO off
	0xE5801020, // 0x64: STR R1, [R0, #0x20]   ; ISEL 0, IMXL 0
	0xE3A01C0F, // 0x68: MOV R1, #0xF00
	0xE5801024, // 0x6C: STR R1, [R0, #0x24]   ; DISDL 15, DIPAN 0
	0xE3A01004, // 0x70: MOV R1, #4
	0xE5801028, // 0x74: STR R1, [R0, #0x28]   ; TL 0, Q 4 (pass)
	0xE59F1080, // 0x78: LDR R1, [PC, #0x80]   ; literal 0x100 = 0x1FF8
	0xE580102C, // 0x7C: STR R1, [R0, #0x2C]   ; FLV0
	0xE5801030, // 0x80: STR R1, [R0, #0x30]   ; FLV1
	0xE5801034, // 0x84: STR R1, [R0, #0x34]   ; FLV2
	0xE5801038, // 0x88: STR R1, [R0, #0x38]   ; FLV3
	0xE580103C, // 0x8C: STR R1, [R0, #0x3C]   ; FLV4
	0xE59F206C, // 0x90: LDR R2, [PC, #0x6C]   ; literal 0x104 = 0x00802800
	0xE3A0100F, // 0x94: MOV R1, #0xF
	0xE5821000, // 0x98: STR R1, [R2]          ; MVOL 15
	0xE3A010FA, // 0x9C: MOV R1, #0xFA         ; TACTL 0, TIMA 250
	0xE5821090, // 0xA0: STR R1, [R2, #0x90]   ; timer A control
	0xE3A01040, // 0xA4: MOV R1, #0x40         ; source 6 (timer A)
	0xE582109C, // 0xA8: STR R1, [R2, #0x9C]   ; SCIEB (ARM side)
	0xE3A010C0, // 0xAC: MOV R1, #0xC0         ; sources 6 and 7
	0xE58210B4, // 0xB0: STR R1, [R2, #0xB4]   ; MCIEB (SH-4 side)
	0xE3A01040, // 0xB4: MOV R1, #0x40
	0xE58210AC, // 0xB8: STR R1, [R2, #0xAC]   ; SCILV1: level 2
	0xE10F1000, // 0xBC: MRS R1, CPSR
	0xE3C11040, // 0xC0: BIC R1, R1, #0x40     ; unmask FIQ
	0xE121F001, // 0xC4: MSR CPSR_c, R1
	0xE59F1038, // 0xC8: LDR R1, [PC, #0x38]   ; literal 0x108 = 0xC200
	0xE5801000, // 0xCC: STR R1, [R0]          ; KYONEX: key on
	0xEAFFFFFE, // 0xD0: B .                   ; idle loop
	// fiq:
	0xE59F8030, // 0xD4: LDR R8, [PC, #0x30]   ; literal 0x10C = 0x2000
	0xE5989000, // 0xD8: LDR R9, [R8]
	0xE2899001, // 0xDC: ADD R9, R9, #1
	0xE5889000, // 0xE0: STR R9, [R8]          ; timer IRQ counter++
	0xE59F8024, // 0xE4: LDR R8, [PC, #0x24]   ; literal 0x110 = 0x00802D04
	0xE3A09001, // 0xE8: MOV R9, #1
	0xE5889000, // 0xEC: STR R9, [R8]          ; end of interrupt
	0xE25EF004, // 0xF0: SUBS PC, LR, #4       ; return from FIQ
	// literal pool:
	0x0007F000, // 0xF4: stack top (wave RAM)
	0x00800000, // 0xF8: slot register base
	0x00003C1F, // 0xFC: reg 0x14 value
	0x00001FF8, // 0x100: FLV value
	0x00802800, // 0x104: common register base
	0x0000C200, // 0x108: KYONEX + KYONB + LPCTL
	0x00002000, // 0x10C: counter address (wave RAM)
	0x00802D04, // 0x110: M register (end of interrupt)
}

// dc3ProgramWords is the SH-4 side of the gate. R10 points at
// data page A (0x8C000800, entries 0 to 15) and R9 at data
// page B (0x8C000840, entries 16 to 27) for the whole run:
// the MOV.L @(disp,Rn) displacement field is four bits, so a
// single base register reaches only 16 words. The handler
// relies on both bases surviving.
var dc3ProgramWords = []uint16{
	0xDA01,         // 0x00: MOV.L @(1,PC),R10  ; literal at 0x08
	0xA003,         // 0x02: BRA 0x0C
	0x0009,         // 0x04: NOP (delay slot)
	0x0009,         // 0x06: pad
	0x0800, 0x8C00, // 0x08: .long 0x8C000800
	0x50A0,         // 0x0C: MOV.L @(0,R10),R0   ; SR value
	0x400E,         // 0x0E: LDC R0,SR
	0x50A1,         // 0x10: MOV.L @(1,R10),R0   ; VBR value
	0x402E,         // 0x12: LDC R0,VBR
	0x5FA2,         // 0x14: MOV.L @(2,R10),R15  ; stack top
	0xD916,         // 0x16: MOV.L @(22,PC),R9   ; literal at 0x70: page B
	0x5193,         // 0x18: MOV.L @(3,R9),R1    ; IML2NRM addr
	0x5294,         // 0x1A: MOV.L @(4,R9),R2    ; Maple done bit
	0x2122,         // 0x1C: MOV.L R2,@R1
	0x5195,         // 0x1E: MOV.L @(5,R9),R1    ; IML2EXT addr
	0x5296,         // 0x20: MOV.L @(6,R9),R2    ; AICA bit
	0x2122,         // 0x22: MOV.L R2,@R1
	0x51A3,         // 0x24: MOV.L @(3,R10),R1   ; copy source
	0x52A4,         // 0x26: MOV.L @(4,R10),R2   ; copy destination
	0x53A5,         // 0x28: MOV.L @(5,R10),R3   ; copy count
	0x6014,         // 0x2A: loop: MOV.B @R1+,R0
	0x2200,         // 0x2C: MOV.B R0,@R2 (address field is bits 11:8)
	0x7201,         // 0x2E: ADD #1,R2
	0x4310,         // 0x30: DT R3
	0x8BFA,         // 0x32: BF loop
	0x0009,         // 0x34: NOP (delay slot)
	0x51A6,         // 0x36: MOV.L @(6,R10),R1   ; ARMRST addr
	0xE000,         // 0x38: MOV #0,R0
	0x2102,         // 0x3A: MOV.L R0,@R1        ; release the ARM7
	0x57A7,         // 0x3C: MOV.L @(7,R10),R7   ; Maple table address
	0x50A8,         // 0x3E: MOV.L @(8,R10),R0
	0x1700,         // 0x40: MOV.L R0,@(0,R7)    ; entry 0 header
	0x50A9,         // 0x42: MOV.L @(9,R10),R0
	0x1701,         // 0x44: MOV.L R0,@(1,R7)    ; entry 0 receive buffer
	0x50AA,         // 0x46: MOV.L @(10,R10),R0
	0x1702,         // 0x48: MOV.L R0,@(2,R7)    ; Get Condition frame
	0x50AB,         // 0x4A: MOV.L @(11,R10),R0
	0x1703,         // 0x4C: MOV.L R0,@(3,R7)    ; function word
	0x50AC,         // 0x4E: MOV.L @(12,R10),R0
	0x1704,         // 0x50: MOV.L R0,@(4,R7)    ; entry 1 header (last)
	0x50AD,         // 0x52: MOV.L @(13,R10),R0
	0x1705,         // 0x54: MOV.L R0,@(5,R7)    ; entry 1 receive buffer
	0x50AE,         // 0x56: MOV.L @(14,R10),R0
	0x1706,         // 0x58: MOV.L R0,@(6,R7)    ; Device Request frame
	0x51AF,         // 0x5A: MOV.L @(15,R10),R1  ; SB_MDSTAR addr
	0x5290,         // 0x5C: MOV.L @(0,R9),R2    ; table physical address
	0x2122,         // 0x5E: MOV.L R2,@R1
	0x5191,         // 0x60: MOV.L @(1,R9),R1    ; SB_MDEN addr
	0xE201,         // 0x62: MOV #1,R2
	0x2122,         // 0x64: MOV.L R2,@R1        ; MDEN = 1
	0x5192,         // 0x66: MOV.L @(2,R9),R1    ; SB_MDST addr
	0x2122,         // 0x68: MOV.L R2,@R1        ; MDST = 1: transfer runs
	0xAFFE,         // 0x6A: spin: BRA spin
	0x0009,         // 0x6C: NOP (delay slot)
	0x0009,         // 0x6E: pad
	0x0840, 0x8C00, // 0x70: .long 0x8C000840
}

// dc3HandlerWords sits at image offset 0x1600 (VBR 0x8C001000
// plus 0x600). It counts the two interrupt sources the gate
// arms and acknowledges each at its own controller. Data page
// B entries are reached through R9.
var dc3HandlerWords = []uint16{
	0x5097, // +0x00: MOV.L @(7,R9),R0    ; ISTNRM addr
	0x539A, // +0x02: MOV.L @(10,R9),R3   ; scratch base
	0x6102, // +0x04: MOV.L @R0,R1
	0x5294, // +0x06: MOV.L @(4,R9),R2    ; Maple done bit
	0x2128, // +0x08: TST R2,R1
	0x8904, // +0x0A: BT +4 -> +0x16 (no Maple event)
	0x0009, // +0x0C: NOP (delay slot)
	0x5430, // +0x0E: MOV.L @(0,R3),R4    ; Maple IRQ count
	0x7401, // +0x10: ADD #1,R4
	0x1340, // +0x12: MOV.L R4,@(0,R3)
	0x2022, // +0x14: MOV.L R2,@R0        ; ack ISTNRM bit 12
	0x5098, // +0x16: MOV.L @(8,R9),R0    ; ISTEXT addr
	0x6102, // +0x18: MOV.L @R0,R1
	0xE202, // +0x1A: MOV #2,R2           ; AICA bit
	0x2128, // +0x1C: TST R2,R1
	0x8906, // +0x1E: BT +6 -> +0x2E (no AICA event)
	0x0009, // +0x20: NOP (delay slot)
	0x5431, // +0x22: MOV.L @(1,R3),R4    ; AICA IRQ count
	0x7401, // +0x24: ADD #1,R4
	0x1341, // +0x26: MOV.L R4,@(1,R3)
	0x5099, // +0x28: MOV.L @(9,R9),R0    ; MCIRE addr
	0x529B, // +0x2A: MOV.L @(11,R9),R2   ; timer B source bit
	0x2022, // +0x2C: MOV.L R2,@R0        ; clear AICA pending
	0x002B, // +0x2E: RTE
	0x0009, // +0x30: NOP (delay slot)
}

// dc3DataPage is written by the test at physical 0x0C000800.
var dc3DataPage = []uint32{
	0x40000000, // 0: SR, IMASK 0
	0x8C001000, // 1: VBR
	0x8C010000, // 2: stack top
	0x8C100000, // 3: driver image in main RAM
	0x00800000, // 4: AICA RAM base
	0x00000114, // 5: driver size in bytes
	0xA0702C00, // 6: ARMRST register
	0x8C200000, // 7: Maple table (virtual)
	0x00000001, // 8: entry 0 header: port 0, 2 frame words
	0x0C210000, // 9: entry 0 receive buffer
	0x09200001, // 10: Get Condition frame header, port 0
	0x01000000, // 11: controller function code
	0x80000000, // 12: entry 1 header: last, port 1, 1 frame word
	0x0C220000, // 13: entry 1 receive buffer
	0x01604000, // 14: Device Request frame header, port 1
	0xA05F6C04, // 15: SB_MDSTAR
	0x0C200000, // 16: table physical address
	0xA05F6C14, // 17: SB_MDEN
	0xA05F6C18, // 18: SB_MDST
	0xA05F6910, // 19: SB_IML2NRM
	0x00001000, // 20: Maple DMA done bit (ISTNRM bit 12)
	0xA05F6914, // 21: SB_IML2EXT
	0x00000002, // 22: AICA bit (ISTEXT bit 1)
	0xA05F6900, // 23: SB_ISTNRM
	0xA05F6904, // 24: SB_ISTEXT
	0xA07028BC, // 25: AICA MCIRE
	0x8C002000, // 26: scratch base
	0x00000080, // 27: timer B source bit (MCIRE value)
}

const (
	dc3ScratchPhys = 0x0C002000
	dc3Recv0Phys   = 0x0C210000
	dc3Recv1Phys   = 0x0C220000
)

// dc3PCM is the 64 sample loop the ARM driver plays.
func dc3PCM(i int) int16 { return int16((i - 32) * 256) }

// setupDC3Fog submits the fog gate scene through the bus.
func setupDC3Fog(t *testing.T, m *Machine) {
	t.Helper()
	pvrReg(m, 0x48, 0x3)       // FB_W_CTRL: enable, 565
	pvrReg(m, 0x60, 0)         // FB_W_SOF1 = VRAM 0
	pvrReg(m, 0x44, 0x800003)  // FB_R_CTRL: enable, 565, vclk_div
	pvrReg(m, 0x50, 0)         // FB_R_SOF1 = VRAM 0
	pvrReg(m, 0x5C, 15|31<<10) // FB_R_SIZE: 32 wide, 32 lines
	pvrReg(m, 0x68, 31<<16)    // FB_X_CLIP max 31
	pvrReg(m, 0x6C, 31<<16)    // FB_Y_CLIP max 31
	pvrReg(m, 0x98, 0xFF000000)
	pvrReg(m, 0x9C, 0)
	pvrReg(m, 0x128, 0x10000) // TA_ISP_BASE
	// Fog: entries 8 and 9 carry low bytes 100 and 156 (the
	// high bytes are junk on purpose), density 4.25, white.
	for i := 0; i < 128; i += 2 {
		var v uint32
		if i == 8 {
			v = 0xAB64 | 0xCD9C<<16
		}
		m.Bus.Write32(0x005F8000+0x200+uint32(i*2), v)
	}
	pvrReg(m, 0xB0, 0x00FFFFFF) // FOG_COL_RAM white
	pvrReg(m, 0xB8, 0x8802)     // FOG_DENSITY 4.25
	pvrReg(m, 0x140, 0)         // TA_LIST_INIT
	// Polygon header: opaque, ISP depth always with fog mode
	// 0 (table), TSP untextured one-zero blend, base black.
	taPacket(m,
		4<<29,      // polygon header, opaque list
		7,          // ISP/TSP: depth always, fog table mode
		0x6C080000, // TSP: src one, dst zero, 8 by 8 size codes
		0,          // TCW (untextured)
		0xFF000000, // base color black
		0, 0, 0,
	)
	f32 := math.Float32bits
	vtx := func(x, y float32, eos bool) {
		pcw := uint32(7 << 29)
		if eos {
			pcw |= 1 << 28
		}
		taPacket(m, pcw, f32(x), f32(y), f32(0.5), f32(0), f32(0), 0xFF000000, 0)
	}
	vtx(0, 0, false)
	vtx(32, 0, false)
	vtx(0, 32, false)
	vtx(32, 32, true)
	taPacket(m, 0)     // end of list
	pvrReg(m, 0x14, 0) // STARTRENDER
}

func loadDC3Machine(t *testing.T) *Machine {
	t.Helper()
	img := make([]byte, 0x1700)
	put := func(off int, words []uint16) {
		for i, w := range words {
			binary.LittleEndian.PutUint16(img[off+2*i:], w)
		}
	}
	put(0, dc3ProgramWords)
	put(0x1600, dc3HandlerWords)
	m := New()
	m.LoadProgram(0x0C000000, img)
	for i, v := range dc3DataPage {
		m.Bus.Write32(0x0C000800+uint32(4*i), v)
	}
	// The ARM driver image waits in main RAM for the SH-4
	// program to copy it into AICA RAM.
	for i, w := range armDriverWords {
		m.Bus.Write32(0x0C100000+uint32(4*i), w)
	}
	// The PCM loop data sits at wave RAM 0x1000.
	for i := 0; i < 64; i++ {
		m.Bus.Write16(0x00801000+uint32(2*i), uint16(dc3PCM(i)))
	}
	m.SetButtons(PadState{
		Mask:     BtnA | BtnStart,
		TriggerL: 200,
		JoyX:     30,
		JoyY:     200,
	})
	setupDC3Fog(t, m)
	return m
}

// audioBytes flattens drained stereo samples for comparison.
func audioBytes(a [][2]int16) []byte {
	b := make([]byte, 4*len(a))
	for i, s := range a {
		binary.LittleEndian.PutUint16(b[4*i:], uint16(s[0]))
		binary.LittleEndian.PutUint16(b[4*i+2:], uint16(s[1]))
	}
	return b
}

func TestDC3WholeMachineDeterminism(t *testing.T) {
	m := loadDC3Machine(t)
	m.RunFrame()

	// The fog scene rendered through the whole machine.
	if got := m.Framebuffer()[16*Width+16]; got != 0xFF848284 {
		t.Errorf("fog center pixel = %#08x, want 0xFF848284", got)
	}

	// The Maple poll landed: port 0 Get Condition and port 1
	// Device Info in the receive buffers.
	if got := m.Bus.Read32(dc3Recv0Phys + 0); got != 0x07002003 {
		t.Errorf("condition header = %#08x, want 0x07002003", got)
	}
	if got := m.Bus.Read32(dc3Recv0Phys + 4); got != 0x01000000 {
		t.Errorf("condition function = %#08x, want controller 0x01000000", got)
	}
	// A (wire bit 2) and Start (wire bit 3) pressed, active
	// low; right trigger 0, left trigger 200.
	if got := m.Bus.Read32(dc3Recv0Phys + 8); got != 0xFFF300C8 {
		t.Errorf("condition word 1 = %#08x, want 0xFFF300C8", got)
	}
	// Stick at (30, 200), second stick centered.
	if got := m.Bus.Read32(dc3Recv0Phys + 12); got != 0x8080C81E {
		t.Errorf("condition word 2 = %#08x, want 0x8080C81E", got)
	}
	// Port 1: recipient is the port 1 host address 0x40, the
	// sender is the port 1 main device 0x60.
	if got := m.Bus.Read32(dc3Recv1Phys + 0); got != 0x0540601C {
		t.Errorf("device info header = %#08x, want 0x0540601C", got)
	}
	if got := m.Bus.Read32(dc3Recv1Phys + 4); got != 0x01000000 {
		t.Errorf("device info function = %#08x, want controller 0x01000000", got)
	}

	// Both interrupt paths vectored the SH-4 handler, and the
	// ARM FIQ handler ran (its wave RAM counter advanced).
	if got := m.Bus.Read32(dc3ScratchPhys + 0); got != 1 {
		t.Errorf("Maple IRQ count = %d, want exactly 1", got)
	}
	if got := m.Bus.Read32(dc3ScratchPhys + 4); got < 1 {
		t.Errorf("AICA IRQ count = %d, want at least 1", got)
	}
	if got := m.Bus.Read32(0x00802000); got < 1 {
		t.Errorf("ARM timer counter = %d, want at least 1", got)
	}

	// The sound is the PCM loop itself: search the drained
	// stream for one full period and require two exact
	// periods in both channels.
	audio1 := m.AudioBuffer()
	if len(audio1) < 600 || len(audio1) > 900 {
		t.Fatalf("frame 1 audio = %d samples, want about 735", len(audio1))
	}
	found := -1
	for i := 0; i+128 <= len(audio1); i++ {
		if audio1[i][0] == dc3PCM(0) && audio1[i][1] == dc3PCM(0) {
			ok := true
			for j := 0; j < 128; j++ {
				if audio1[i+j][0] != dc3PCM(j%64) || audio1[i+j][1] != dc3PCM(j%64) {
					ok = false
					break
				}
			}
			if ok {
				found = i
				break
			}
		}
	}
	if found < 0 {
		t.Fatalf("PCM loop period not found verbatim in %d drained samples", len(audio1))
	}
	t.Logf("audio frame 1: %d samples, loop period found at index %d", len(audio1), found)

	// Mid run save, then byte identical continuation on the
	// original and on a restored machine.
	state := m.Serialize()
	t.Logf("state v3 size: %d bytes", len(state))

	m.RunFrame()
	wantAudio := m.AudioBuffer()
	wantFB := append([]uint32(nil), m.Framebuffer()...)
	wantState := m.Serialize()

	m2 := New()
	if !m2.Deserialize(state) {
		t.Fatal("Deserialize of the mid run state failed")
	}
	m2.RunFrame()
	if got := m2.AudioBuffer(); !bytes.Equal(audioBytes(got), audioBytes(wantAudio)) {
		t.Errorf("restored audio differs (%d vs %d samples)", len(got), len(wantAudio))
	}
	if got := m2.Framebuffer(); !bytes.Equal(u32s(got), u32s(wantFB)) {
		t.Error("restored framebuffer differs")
	}
	if got := m2.Serialize(); !bytes.Equal(got, wantState) {
		t.Errorf("restored final state differs (%d vs %d bytes)", len(got), len(wantState))
	}
}
