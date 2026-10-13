package system

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-psp/ge"
)

// PSP-2 gate: a synthetic program runs VFPU math on the CPU and a
// display list through the GE, then a state saved mid-run restores
// into a byte-identical continuation (state bytes and framebuffer
// pixels). All fixtures are synthesized here; nothing is loaded from
// disc.

// vfpuProg is hand-encoded VFPU on top of the integer core:
//
//	vadd.q M1 row0 = M0 row0 + M0 row1        (1,2,3,4) + (10,20,30,40)
//	vdot.q S(2,0,0) = M1 row0 . M0 row0       11+44+99+176 = 330
//	vmul.q M1 row1 = M1 row0 * M0 row0        (11,44,99,176)
//	j self
var vfpuProg = []uint32{
	encGateVV(0x18, 0, 0x10, 0x00, 0x04, 3), // vadd.q (op 0x18, G 0)
	encGateVV(0x19, 1, 0x20, 0x10, 0x00, 3), // vdot.q (op 0x19, G 1)
	encGateVV(0x19, 0, 0x14, 0x10, 0x00, 3), // vmul.q (op 0x19, G 0)
	0x0A000003,                              // j 0x0800000C (self)
}

// encGateVV encodes a VV-format VFPU instruction: VD at bits 6:0, the
// size split across bits 7 and 15, VS at bits 14:8, VT at bits 22:16,
// group at bits 25:23.
func encGateVV(op, g, vd, vs, vt, size uint32) uint32 {
	return op<<26 | g<<23 | vt<<16 | ((size>>1)&1)<<15 | vs<<8 | (size&1)<<7 | vd
}

func geCmd(op, arg uint32) uint32 { return op<<24 | (arg & 0xFFFFFF) }

// gateDisplayList clears the frame to a color and draws one green
// sprite at (8,8)-(16,12). Addresses: list at 0x08001000, vertices at
// 0x08002000, framebuffer at VRAM offset 0 with width 512 in 8888.
var gateDisplayList = []uint32{
	geCmd(0x14, 0x080000),      // ORIGIN: originPtr = 0x08000000
	geCmd(0x9C, 0),             // FRAMEBUFPTR
	geCmd(0x9D, 512),           // FRAMEBUFWIDTH
	geCmd(0xE8, 3),             // PIXELFORMAT = 8888
	geCmd(0x15, 0),             // REGION1 (0,0)
	geCmd(0x16, 479|(271<<10)), // REGION2 (479,271)
	geCmd(0xC9, 0x102030),      // CLEARCOLOR r=0x30 g=0x20 b=0x10
	geCmd(0xCA, 0xFF),          // CLEARALPHA alpha=255
	geCmd(0xCC, 0x7),           // CLEAR enable|color|alpha
	geCmd(0x04, 3<<16|3),       // PRIM (clear)
	geCmd(0xCC, 0),             // CLEAR mode off, or the next PRIM clears too
	geCmd(0x12, 0x800113),      // VTYPE tc float, color 8888, pos s16, through
	geCmd(0x01, 0x2000),        // VADDR -> 0x08002000
	geCmd(0x04, 6<<16|2),       // PRIM sprite, 2 vertices
	geCmd(0x0F, 0),             // FINISH
	geCmd(0x0C, 0),             // END
}

// gateSpriteVerts: two through-mode vertices, stride 18 bytes
// (uv float pair, color word, position x/y/z as 12.4 fixed / raw z).
func gateSpriteVerts() []byte {
	out := make([]byte, 0, 36)
	put := func(v interface{}) {
		switch t := v.(type) {
		case float32:
			var b [4]byte
			binary.LittleEndian.PutUint32(b[:], math.Float32bits(t))
			out = append(out, b[:]...)
		case uint32:
			var b [4]byte
			binary.LittleEndian.PutUint32(b[:], t)
			out = append(out, b[:]...)
		case uint16:
			var b [2]byte
			binary.LittleEndian.PutUint16(b[:], t)
			out = append(out, b[:]...)
		}
	}
	// v0: uv (0,0), opaque green, pos (8,8), z 0
	put(float32(0))
	put(float32(0))
	put(uint32(0xFF00FF00))
	put(uint16(8 << 4))
	put(uint16(8 << 4))
	put(uint16(0))
	// v1: uv (1,1), opaque green, pos (16,12), z 0
	put(float32(1))
	put(float32(1))
	put(uint32(0xFF00FF00))
	put(uint16(16 << 4))
	put(uint16(12 << 4))
	put(uint16(0))
	return out
}

func loadGateMachine(t *testing.T) *Machine {
	t.Helper()
	m := New()
	buf := make([]byte, 4)
	for i, w := range vfpuProg {
		binary.LittleEndian.PutUint32(buf, w)
		m.LoadBytes(0x08000000+uint32(4*i), buf)
	}
	for i, w := range gateDisplayList {
		binary.LittleEndian.PutUint32(buf, w)
		m.LoadBytes(0x08001000+uint32(4*i), buf)
	}
	m.LoadBytes(0x08002000, gateSpriteVerts())
	m.Boot(0x08000000)
	// VFPU inputs: M0 row0 = (1,2,3,4), M0 row1 = (10,20,30,40). Set
	// after Boot because Boot resets the CPU and clears the VFPU file.
	for i, v := range []float32{1, 2, 3, 4, 10, 20, 30, 40} {
		m.CPU.SetVSingle(i, math.Float32bits(v))
	}
	return m
}

func TestPSP2VFPUProgramResults(t *testing.T) {
	m := loadGateMachine(t)
	m.RunCycles(100)
	// vadd.q result at M1 row0 (linear 16..19).
	for i, want := range []float32{11, 22, 33, 44} {
		if got := math.Float32frombits(m.CPU.VSingle(16 + i)); got != want {
			t.Errorf("vadd lane %d = %v, want %v", i, got, want)
		}
	}
	// vdot.q result at S(2,0,0) (linear 32).
	if got := math.Float32frombits(m.CPU.VSingle(32)); got != 330 {
		t.Errorf("vdot = %v, want 330", got)
	}
	// vmul.q result at M1 row1 (linear 20..23).
	for i, want := range []float32{11, 44, 99, 176} {
		if got := math.Float32frombits(m.CPU.VSingle(20 + i)); got != want {
			t.Errorf("vmul lane %d = %v, want %v", i, got, want)
		}
	}
	if got := m.CPU.VFPUFaultCount(); got != 0 {
		t.Errorf("VFPUFaultCount = %d, want 0 (VFPU executes now)", got)
	}
}

func TestPSP2DisplayListThroughSystem(t *testing.T) {
	m := loadGateMachine(t)
	if st := m.SubmitDisplayList(0x08001000); st != ge.StateCompleted {
		t.Fatalf("list state = %v, want StateCompleted", st)
	}
	fb := m.Framebuffer()
	const clearPx = 0xFF302010 // a=FF r=0x30 g=0x20 b=0x10
	const greenPx = 0xFF00FF00
	if got := fb[0]; got != clearPx {
		t.Errorf("pixel (0,0) = %#08x, want clear %#08x", got, clearPx)
	}
	if got := fb[10*Width+10]; got != greenPx {
		t.Errorf("pixel (10,10) = %#08x, want sprite green %#08x", got, greenPx)
	}
	if got := fb[11*Width+15]; got != greenPx {
		t.Errorf("pixel (15,11) = %#08x, want sprite green %#08x", got, greenPx)
	}
	if got := fb[10*Width+16]; got != clearPx {
		t.Errorf("pixel (16,10) = %#08x, want clear %#08x", got, clearPx)
	}
	if got := fb[12*Width+10]; got != clearPx {
		t.Errorf("pixel (10,12) = %#08x, want clear %#08x", got, clearPx)
	}
}

func TestPSP2SaveRestoreDeterministicContinuation(t *testing.T) {
	a := loadGateMachine(t)
	a.RunCycles(100)
	if st := a.SubmitDisplayList(0x08001000); st != ge.StateCompleted {
		t.Fatalf("list state = %v, want StateCompleted", st)
	}
	mid := a.Serialize()
	if len(mid) != a.StateSize() {
		t.Fatalf("serialized %d bytes, StateSize says %d", len(mid), a.StateSize())
	}
	if mid[0] != 2 {
		t.Fatalf("state version = %d, want 2", mid[0])
	}
	a.RunCycles(2000)
	finalA := a.Serialize()
	fbA := append([]uint32(nil), a.Framebuffer()...)

	b := New()
	if err := b.Deserialize(mid); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if b.DisplayListState() != ge.StateCompleted {
		t.Fatalf("restored list state = %v, want StateCompleted", b.DisplayListState())
	}
	if got := math.Float32frombits(b.CPU.VSingle(32)); got != 330 {
		t.Fatalf("restored vdot = %v, want 330", got)
	}
	b.RunCycles(2000)
	finalB := b.Serialize()
	fbB := append([]uint32(nil), b.Framebuffer()...)

	if !bytes.Equal(finalA, finalB) {
		t.Fatal("continuation after restore diverged from the uninterrupted run")
	}
	if len(fbA) != len(fbB) {
		t.Fatalf("framebuffer sizes differ: %d vs %d", len(fbA), len(fbB))
	}
	for i := range fbA {
		if fbA[i] != fbB[i] {
			t.Fatalf("framebuffer pixel %d diverged: %#08x vs %#08x", i, fbA[i], fbB[i])
		}
	}
}

func TestPSP2RejectsVersion1State(t *testing.T) {
	m := loadGateMachine(t)
	state := m.Serialize()
	state[0] = 1
	if err := New().Deserialize(state); err == nil {
		t.Fatal("version 1 state accepted by the version 2 loader")
	}
}
