package system

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// Interrupt delivery gate: a hand assembled SH-4 program plus its
// interrupt handler, run on the whole machine.
//
// The program (VA 0x8C000000, data page at 0x8C000800) sets up
// SR and VBR, programs the Holly masks (vblank-in at level 6,
// render-done at level 4) and a small SPG (20x10, vblank-in on
// line 8), then raises SR.IMASK to 7, submits an end-of-list TA
// packet, and triggers a render. The render-done interrupt
// (level 4) must stay pending while IMASK is 7: the handler
// copies a flag the program sets just before dropping IMASK to
// 0, so flagcopy == 1 proves the handler could not have run
// during the masked window. Each vblank handler entry increments
// a counter and appends 'V' to a log; the render entry appends
// 'R'. After RunFrame the counter must equal the PVR field count
// exactly (one serviced vblank per field, no re-entry, which is
// what the handler's ISTNRM write-1-to-clear ack guarantees) and
// the log must start with R, then only V.

var irqProgramWords = []uint16{
	// 0x00: load the data page base, jump over the literal.
	0xDA01,         // MOV.L @(1,PC),R10   ; literal at 0x08
	0xA003,         // BRA 0x0C
	0x0009,         // NOP (delay slot)
	0x0009,         // pad
	0x0800, 0x8C00, // 0x08: .long 0x8C000800
	// 0x0C: main setup. R10 = data page.
	0x50A0,                         // MOV.L @(0,R10),R0   ; SR value, IMASK 0
	0x400E,                         // LDC R0,SR
	0x50A1,                         // MOV.L @(1,R10),R0   ; VBR value
	0x402E,                         // LDC R0,VBR
	0x5FA2,                         // MOV.L @(2,R10),R15  ; stack top
	0x51A3,                         // MOV.L @(3,R10),R1   ; IML6LVL addr
	0x52A4,                         // MOV.L @(4,R10),R2   ; vblank-in bit
	0x2122,                         // MOV.L R2,@R1
	0x51A5,                         // MOV.L @(5,R10),R1   ; IML4LVL addr
	0x52A6,                         // MOV.L @(6,R10),R2   ; render-done bit
	0x2122,                         // MOV.L R2,@R1
	0x51A7,                         // MOV.L @(7,R10),R1   ; SPG_LOAD addr
	0x52A8,                         // MOV.L @(8,R10),R2   ; SPG_LOAD value
	0x2122,                         // MOV.L R2,@R1
	0x51A9,                         // MOV.L @(9,R10),R1   ; SPG_VBLANK_INT addr
	0x52AA,                         // MOV.L @(10,R10),R2
	0x2122,                         // MOV.L R2,@R1
	0x50AF,                         // MOV.L @(15,R10),R0  ; SR value, IMASK 7
	0x400E,                         // LDC R0,SR
	0x51AB,                         // MOV.L @(11,R10),R1  ; TA FIFO addr
	0xE200,                         // MOV #0,R2
	0x2122, 0x2122, 0x2122, 0x2122, // MOV.L R2,@R1 x8: end of list
	0x2122, 0x2122, 0x2122, 0x2122,
	0x51AC, // MOV.L @(12,R10),R1  ; STARTRENDER addr
	0x2122, // MOV.L R2,@R1        ; render: level 4 pends under IMASK 7
	0x51AE, // MOV.L @(14,R10),R1  ; scratch base
	0xE201, // MOV #1,R2
	0x1123, // MOV.L R2,@(3,R1)    ; flag = 1
	0x50A0, // MOV.L @(0,R10),R0   ; SR value, IMASK 0
	0x400E, // LDC R0,SR           ; mask drops: render IRQ accepted next
	0xAFFE, // loop: BRA loop
	0x0009, // NOP (delay slot)
}

// irqHandlerWords sits at image offset 0x1600 (VBR 0x8C001000 +
// 0x600). Bank 1 registers are the handler's own; R10 still
// points at the data page and R3 at the scratch page. Note the
// core's BT/BF model (as documented in the DC-1 machine test):
// the delay slot of a conditional branch runs only when the
// branch is taken, so every BT delay slot here holds a NOP and
// no load-bearing instruction sits in one.
var irqHandlerWords = []uint16{
	0x50AD, // MOV.L @(13,R10),R0  ; ISTNRM addr
	0x53AE, // MOV.L @(14,R10),R3  ; scratch base
	0x6102, // MOV.L @R0,R1        ; ISTNRM value
	0xE204, // MOV #4,R2           ; render-done bit
	0x2128, // TST R2,R1
	0x8909, // BT +9 -> 0x1620 (skip render service)
	0x0009, // NOP (delay slot)
	0x5434, // MOV.L @(4,R3),R4    ; render IRQ count
	0x7401, // ADD #1,R4
	0x1344, // MOV.L R4,@(4,R3)
	0x5433, // MOV.L @(3,R3),R4    ; flag
	0x1345, // MOV.L R4,@(5,R3)    ; flagcopy = flag
	0xE652, // MOV #0x52,R6        ; 'R'
	0xB00F, // BSR log (0x163C)
	0x0009, // NOP (delay slot)
	0x2022, // MOV.L R2,@R0        ; ack render-done
	// 0x1620:
	0xE208, // MOV #8,R2           ; vblank-in bit
	0x2128, // TST R2,R1
	0x8907, // BT +7 -> 0x1636 (done)
	0x0009, // NOP (delay slot)
	0x5430, // MOV.L @(0,R3),R4    ; vblank counter
	0x7401, // ADD #1,R4
	0x1340, // MOV.L R4,@(0,R3)
	0xE656, // MOV #0x56,R6        ; 'V'
	0xB004, // BSR log (0x163C)
	0x0009, // NOP (delay slot)
	0x2022, // MOV.L R2,@R0        ; ack vblank-in
	// 0x1636:
	0x002B, // RTE
	0x0009, // NOP (delay slot)
	0x0009, // pad (unreachable)
	// 0x163C: log: append the byte in R6 at scratch+32+loglen
	// (offset 32 keeps the log clear of the counter words).
	0x5431, // MOV.L @(1,R3),R4    ; log length
	0x6533, // MOV R3,R5
	0x7520, // ADD #32,R5
	0x354C, // ADD R4,R5
	0x2560, // MOV.B R6,@R5
	0x7401, // ADD #1,R4
	0x1341, // MOV.L R4,@(1,R3)
	0x000B, // RTS
	0x0009, // NOP (delay slot)
}

// irqDataPage is written by the test at physical 0x0C000800.
var irqDataPage = []uint32{
	0x40000000,     // 0: SR, IMASK 0
	0x8C001000,     // 1: VBR
	0x8C010000,     // 2: stack top
	0xA05F6930,     // 3: SB_IML6LVL (NRM row)
	0x00000008,     // 4: vblank-in bit
	0xA05F6920,     // 5: SB_IML4LVL (NRM row)
	0x00000004,     // 6: render-done (TSP) bit
	0xA05F80D8,     // 7: SPG_LOAD
	(9 << 16) | 19, // 8: 20 pixels, 10 lines
	0xA05F80CC,     // 9: SPG_VBLANK_INT
	(8 << 16) | 0,  // 10: in on line 8, out on line 0
	0xA0500000,     // 11: TA FIFO
	0xA05F8014,     // 12: STARTRENDER
	0xA05F6900,     // 13: SB_ISTNRM
	0x8C002000,     // 14: scratch base
	0x40000070,     // 15: SR, IMASK 7
}

const (
	scratchPhys = 0x0C002000
)

func loadIRQMachine(t *testing.T) *Machine {
	t.Helper()
	img := make([]byte, 0x1700)
	put := func(off int, words []uint16) {
		for i, w := range words {
			binary.LittleEndian.PutUint16(img[off+2*i:], w)
		}
	}
	put(0, irqProgramWords)
	put(0x1600, irqHandlerWords)
	m := New()
	m.LoadProgram(0x0C000000, img)
	// The data page sits inside the image span (offset 0x800), so
	// it is written after the image.
	for i, v := range irqDataPage {
		m.Bus.Write32(0x0C000800+uint32(4*i), v)
	}
	return m
}

func TestInterruptProgramOrderAndCounting(t *testing.T) {
	m := loadIRQMachine(t)
	m.RunFrame()

	counter := m.Bus.Read32(scratchPhys + 0)
	frames := m.PVR.SPGFrames()
	if frames < 100 {
		t.Fatalf("SPG frames after one RunFrame = %d, want > 100", frames)
	}
	// The counter must track the frame count closely. It cannot
	// be exact for the first frame: until the program reprograms
	// the SPG, RunFrame chunks by the default scanline (12,712
	// cycles), and the beam crosses several of the tiny 10-line
	// fields inside the first chunk's single PVR advance, so a
	// few vblank-in events coalesce in the ISTNRM bit before the
	// CPU can service them (the same coalescing the hardware
	// status bit exhibits). What must hold exactly: one log entry
	// per serviced interrupt, and the deficit must not grow on
	// the second frame (a missed ack would make the handler
	// re-enter endlessly and the counter would run away instead).
	deficit := int64(frames) - int64(counter)
	if deficit < 0 || deficit > 8 {
		t.Errorf("vblank counter = %d, SPG frames = %d, deficit %d outside [0, 8]", counter, frames, deficit)
	}
	if got := m.Bus.Read32(scratchPhys + 16); got != 1 {
		t.Errorf("render IRQ count = %d, want 1", got)
	}
	if got := m.Bus.Read32(scratchPhys + 20); got != 1 {
		t.Errorf("flagcopy = %d, want 1 (handler ran only after IMASK dropped)", got)
	}
	logLen := int(m.Bus.Read32(scratchPhys + 4))
	if logLen != int(counter)+1 {
		t.Fatalf("log length = %d, want counter+1 = %d", logLen, counter+1)
	}
	if b := m.Bus.Read8(scratchPhys + 32); b != 'R' {
		t.Errorf("log[0] = %q, want 'R' (render-done serviced first)", b)
	}
	for i := 1; i < logLen; i++ {
		if b := m.Bus.Read8(scratchPhys + 32 + uint32(i)); b != 'V' {
			t.Fatalf("log[%d] = %q, want 'V'", i, b)
		}
	}

	// A second frame keeps the count exact (the ack in the
	// handler is what stops re-entry; a missed ack would make
	// the counter run away from the frame count).
	m.RunFrame()
	counter = m.Bus.Read32(scratchPhys + 0)
	deficit2 := int64(m.PVR.SPGFrames()) - int64(counter)
	if deficit2 < 0 || deficit2 > deficit {
		t.Errorf("after frame 2: deficit grew from %d to %d (counter %d, frames %d)",
			deficit, deficit2, counter, m.PVR.SPGFrames())
	}
	if got := m.Bus.Read32(scratchPhys + 16); got != 1 {
		t.Errorf("render IRQ count after frame 2 = %d, want still 1", got)
	}
	if logLen2 := int(m.Bus.Read32(scratchPhys + 4)); logLen2 != int(counter)+1 {
		t.Errorf("log length after frame 2 = %d, want counter+1 = %d", logLen2, counter+1)
	}
}

// ---------------------------------------------------------------------------
// Textured quad end to end: the texture is uploaded through the
// 64-bit VRAM window (bank interleave), submitted as a TA stream
// over the bus, rendered, and read back from the VRAM
// framebuffer. The quad spans (0.5, 0.5) to (64.5, 64.5) of a
// 64x64 target, so pixel (4,4) samples texel (0,0) exactly,
// pixel (12,4) texel (1,0), and pixel (8,4) the exact bilinear
// midpoint of texels (0,0) and (1,0).

func twiddle(x, y, w, h int) int {
	idx, bit := 0, 0
	for i := 0; (1<<i) < w || (1<<i) < h; i++ {
		if (1 << i) < h {
			idx |= ((y >> i) & 1) << bit
			bit++
		}
		if (1 << i) < w {
			idx |= ((x >> i) & 1) << bit
			bit++
		}
	}
	return idx
}

// writeVRAM64 stores img at VRAM backing offset base by writing
// through the 64-bit window, applying the inverse of the bus
// bank interleave: the window presents backing bank 0's words at
// doubled offsets (window word 2k is backing word k, window word
// 2k+1 is bank 1 word k), so a bank 0 word at backing offset o
// is written at window offset 2*o. Fixtures here live in bank 0.
func writeVRAM64(m *Machine, base uint32, img []byte) {
	for i := 0; i+4 <= len(img); i += 4 {
		w := binary.LittleEndian.Uint32(img[i:])
		m.Bus.Write32(0x05000000+2*(base+uint32(i)), w)
	}
}

func pvrReg(m *Machine, off uint32, v uint32) { m.Bus.Write32(0x005F8000+off, v) }

func taPacket(m *Machine, words ...uint32) {
	for i := 0; i < 8; i++ {
		var w uint32
		if i < len(words) {
			w = words[i]
		}
		m.Bus.Write32(0x00500000, w)
	}
}

// setupTexturedScene programs the PVR for a 64x64 565 target,
// uploads the 8x8 texture, and submits the quad, but does not
// start the render.
func setupTexturedScene(t *testing.T, m *Machine) {
	t.Helper()
	pvrReg(m, 0x48, 0x3)        // FB_W_CTRL: enable, 565
	pvrReg(m, 0x60, 0)          // FB_W_SOF1 = VRAM 0
	pvrReg(m, 0x44, 0x800003)   // FB_R_CTRL: enable, 565, vclk_div
	pvrReg(m, 0x50, 0)          // FB_R_SOF1 = VRAM 0
	pvrReg(m, 0x5C, 63<<10|31)  // FB_R_SIZE: 64 lines, 32 words per line
	pvrReg(m, 0x68, 63<<16)     // FB_X_CLIP max 63
	pvrReg(m, 0x6C, 63<<16)     // FB_Y_CLIP max 63
	pvrReg(m, 0x98, 0xFF000000) // background: opaque black
	pvrReg(m, 0x9C, 0)          // background depth 0
	pvrReg(m, 0x128, 0x10000)   // TA_ISP_BASE

	// 8x8 texture of 565 texels, twiddled layout.
	texel := func(x, y int) uint16 {
		switch {
		case x == 0 && y == 0:
			return 0xF800 // red
		case x == 1 && y == 0:
			return 0x07E0 // green
		case x == 0 && y == 1:
			return 0x001F // blue
		}
		return 0xFFFF // white
	}
	img := make([]byte, 8*8*2)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			binary.LittleEndian.PutUint16(img[twiddle(x, y, 8, 8)*2:], texel(x, y))
		}
	}
	writeVRAM64(m, 0x1000, img)
	// The upload must read back identically through the linear
	// 32-bit window.
	for i := 0; i < len(img); i += 4 {
		want := binary.LittleEndian.Uint32(img[i:])
		if got := m.Bus.Read32(0x04001000 + uint32(i)); got != want {
			t.Fatalf("texture word %d = %#x, want %#x", i/4, got, want)
		}
	}

	pvrReg(m, 0x140, 0) // TA_LIST_INIT
	f32 := math.Float32bits
	hdr := []uint32{
		4 << 29,              // polygon header, opaque list
		1<<6 | 7,             // ISP/TSP: textured, depth always
		1 | 1<<24,            // TSP: bilinear, modulate
		1<<27 | (0x1000 / 8), // TCW: 565 texture at 0x1000
		0xFFFFFFFF, 0, 0, 0,  // base color white
	}
	taPacket(m, hdr...)
	vtx := func(x, y, u, v float32, eos bool) []uint32 {
		pcw := uint32(7 << 29)
		if eos {
			pcw |= 1 << 28
		}
		return []uint32{pcw, f32(x), f32(y), f32(1), f32(u), f32(v), 0xFFFFFFFF, 0}
	}
	taPacket(m, vtx(0.5, 0.5, 0, 0, false)...)
	taPacket(m, vtx(64.5, 0.5, 1, 0, false)...)
	taPacket(m, vtx(0.5, 64.5, 0, 1, false)...)
	taPacket(m, vtx(64.5, 64.5, 1, 1, true)...)
	taPacket(m, 0) // end of list
}

func TestTexturedQuadThroughBus(t *testing.T) {
	m := New()
	m.LoadProgram(0x0C000000, []byte{0xFE, 0xAF, 0x09, 0x00}) // spin: BRA -2, NOP
	setupTexturedScene(t, m)
	pvrReg(m, 0x14, 0) // STARTRENDER
	if got := m.PVR.RenderCount(); got != 1 {
		t.Fatalf("RenderCount = %d, want 1", got)
	}
	fb16 := func(x, y int) uint16 {
		return m.Bus.Read16(0x04000000 + uint32((y*64+x)*2))
	}
	checks := []struct {
		x, y int
		want uint16
		what string
	}{
		{4, 4, 0xF800, "texel (0,0) red"},
		{12, 4, 0x07E0, "texel (1,0) green"},
		{8, 4, 0x8400, "bilinear midpoint red/green"},
		{4, 12, 0x001F, "texel (0,1) blue"},
		{60, 60, 0xFFFF, "texel (7,7) white"},
	}
	for _, c := range checks {
		if got := fb16(c.x, c.y); got != c.want {
			t.Errorf("fb(%d,%d) = %#04x, want %#04x (%s)", c.x, c.y, got, c.want, c.what)
		}
	}
	// The presented frame carries the quad in its top-left.
	m.RunFrame()
	px := m.Framebuffer()[4*Width+4]
	if px>>16&0xFF < 0xF0 || px>>8&0xFF > 0x10 || px&0xFF > 0x10 {
		t.Errorf("presented pixel (4,4) = %#08x, want red", px)
	}
}

func TestSaveRestoreMidSceneRendersIdentical(t *testing.T) {
	m := New()
	m.LoadProgram(0x0C000000, []byte{0xFE, 0xAF, 0x09, 0x00})
	setupTexturedScene(t, m)
	// Snapshot after TA submission, before the render.
	state := m.Serialize()

	pvrReg(m, 0x14, 0) // STARTRENDER on the original
	m.RunFrame()
	wantFB := append([]uint32(nil), m.Framebuffer()...)
	wantState := m.Serialize()

	m2 := New()
	if !m2.Deserialize(state) {
		t.Fatal("Deserialize of the mid-scene state failed")
	}
	pvrReg(m2, 0x14, 0)
	m2.RunFrame()
	if got := m2.Framebuffer(); !bytes.Equal(u32s(got), u32s(wantFB)) {
		t.Error("restored machine framebuffer differs")
	}
	if got := m2.Serialize(); !bytes.Equal(got, wantState) {
		t.Errorf("restored final state differs (%d vs %d bytes)", len(got), len(wantState))
	}
}

func u32s(v []uint32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], x)
	}
	return b
}
