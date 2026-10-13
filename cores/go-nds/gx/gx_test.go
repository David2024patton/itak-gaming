package gx_test

import (
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-nds/bus"
	"github.com/davidpatton/itak-gaming/cores/go-nds/gx"
)

// rig drives the engine through its IO interface over a bus.
type rig struct {
	b *bus.Bus
	g *gx.GX
}

func newRig() *rig {
	b := bus.New()
	return &rig{b: b, g: gx.New(b)}
}

// cmd sends one command through its direct port.
func (r *rig) cmd(id uint8, params ...uint32) {
	port := uint32(0x400) + uint32(id)*4
	if len(params) == 0 {
		r.g.WriteIO(port, 4, 0)
		return
	}
	for _, p := range params {
		r.g.WriteIO(port, 4, p)
	}
}

func (r *rig) rd(off uint32) uint32 {
	v, ok := r.g.ReadIO(off, 4)
	if !ok {
		panic("unclaimed read")
	}
	return v
}

// identity sets identity projection and position matrices
// and a full-screen viewport.
func (r *rig) identity() {
	r.cmd(0x10, 0) // MTX_MODE projection
	r.cmd(0x15)    // MTX_IDENTITY
	r.cmd(0x10, 1) // MTX_MODE position
	r.cmd(0x15)
	r.cmd(0x60, 0xBFFF0000) // VIEWPORT (0,0)-(255,191)
}

func TestClipMatrixReadbackExact(t *testing.T) {
	r := newRig()
	// Load a distinctive projection matrix, identity position.
	m := [16]uint32{0x1000, 0x0800, 0xFFFFF000, 3,
		0x2000, 0x1000, 0, 0xFFFFE000,
		1, 2, 3, 4,
		0x3000, 0xFFFFC000, 0x7FFFFFFF, 0x1000}
	r.cmd(0x10, 0)
	p := make([]uint32, 16)
	copy(p, m[:])
	r.cmd(0x16, p...)
	r.cmd(0x10, 1)
	r.cmd(0x15)
	for i := 0; i < 16; i++ {
		if got := r.rd(0x640 + uint32(i*4)); got != m[i] {
			t.Fatalf("CLIPMTX_RESULT[%d] = %08x want %08x", i, got, m[i])
		}
	}
}

func TestPosTestHandComputed(t *testing.T) {
	r := newRig()
	r.identity()
	// Translate the position matrix by (0.5, -0.25, 2.0).
	r.cmd(0x1C, 0x0800, 0xFFFFFC00, 0x2000)
	// POS_TEST (1.0, 0, 0): expect (1.5, -0.25, 2.0, 1.0).
	r.cmd(0x71, 0x1000, 0)
	want := [4]uint32{0x1800, 0xFFFFFC00, 0x2000, 0x1000}
	for i := 0; i < 4; i++ {
		if got := r.rd(0x620 + uint32(i*4)); got != want[i] {
			t.Fatalf("POS_RESULT[%d] = %08x want %08x", i, got, want[i])
		}
	}
}

func TestVecTestIdentity(t *testing.T) {
	r := newRig()
	r.identity()
	// Direction (0.5, -0.5, 0) in 9-frac packed 10-bit fields:
	// 256, -256 (0x300), 0.
	r.cmd(0x72, 256|0x300<<10)
	if got := r.rd(0x630); got != 256 {
		t.Fatalf("VEC_RESULT X = %d want 256", int32(got))
	}
	if got := r.rd(0x634); got != uint32(0xFFFFFF00) {
		t.Fatalf("VEC_RESULT Y = %08x want FFFFFF00 (-256)", got)
	}
	if got := r.rd(0x638); got != 0 {
		t.Fatalf("VEC_RESULT Z = %d want 0", got)
	}
}

func TestMatrixStackOverflow(t *testing.T) {
	r := newRig()
	r.cmd(0x10, 1) // position mode
	for i := 0; i < 31; i++ {
		r.cmd(0x11) // PUSH
	}
	st := r.rd(0x600)
	if st&(1<<15) != 0 {
		t.Fatal("stack error after 31 pushes")
	}
	if got := (st >> 8) & 31; got != 31 {
		t.Fatalf("stack pointer = %d want 31", got)
	}
	r.cmd(0x11) // 32nd push: entry 31, error flag
	st = r.rd(0x600)
	if st&(1<<15) == 0 {
		t.Fatal("stack error missing after 32nd push")
	}
	// Writing GXSTAT bit 15 clears the error.
	r.g.WriteIO(0x600, 4, 1<<15)
	if st := r.rd(0x600); st&(1<<15) != 0 {
		t.Fatal("stack error not cleared by GXSTAT write")
	}
	// POP 1 from SP 32 lands at 31 (also an error entry).
	r.cmd(0x12, 1)
	if st := r.rd(0x600); (st>>8)&31 != 31 {
		t.Fatalf("stack pointer after pop = %d want 31", (st>>8)&31)
	}
}

func TestPackedFIFOCommands(t *testing.T) {
	r := newRig()
	// One packed word: MTX_MODE (0x10) + MTX_IDENTITY (0x15),
	// then the MTX_MODE parameter word (mode 0).
	r.g.WriteIO(0x400, 4, 0x10|0x15<<8)
	r.g.WriteIO(0x400, 4, 0)
	if lvl := r.g.FIFOLevel(); lvl != 0 {
		t.Fatalf("FIFO level = %d want 0", lvl)
	}
	if r.g.FIFOBusy() {
		t.Fatal("geometry busy after drain")
	}
	if got := r.g.FIFOWords; got != 2 {
		t.Fatalf("FIFOWords = %d want 2", got)
	}
	// GXSTAT: empty flag set, less-than-half set.
	st := r.rd(0x600)
	if st&(1<<26) == 0 || st&(1<<25) == 0 {
		t.Fatalf("GXSTAT empty/half flags: %08x", st)
	}
	// IRQ modes: mode 2 (empty) asserts, mode 0 never does.
	r.g.WriteIO(0x600, 4, 2<<30)
	if !r.g.FIFOIRQCondition() {
		t.Fatal("FIFO IRQ condition false in empty mode")
	}
	r.g.WriteIO(0x600, 4, 0)
	if r.g.FIFOIRQCondition() {
		t.Fatal("FIFO IRQ condition true in never mode")
	}
}

func TestRAMCountAndSwapPause(t *testing.T) {
	r := newRig()
	r.identity()
	r.cmd(0x29, 31<<16|0xC0) // POLYGON_ATTR: alpha 31, both faces
	r.cmd(0x40, 0)           // BEGIN_VTXS triangles
	// One triangle via VTX_16.
	r.cmd(0x23, 0x0000F000, 0) // (0, -1)
	r.cmd(0x23, 0x00001000, 0) // (0, 1)
	r.cmd(0x23, 0x10001000, 0) // (1, 1)
	if got := r.rd(0x604); got != 1|3<<16 {
		t.Fatalf("RAM_COUNT = %08x want 1 poly, 3 verts", got)
	}
	r.cmd(0x50, 0) // SWAP_BUFFERS
	// Commands after the swap are paused until VBlank.
	r.cmd(0x40, 0)
	r.cmd(0x23, 0x0000F000, 0)
	if got := r.rd(0x604); got != 1|3<<16 {
		t.Fatalf("RAM_COUNT moved while paused: %08x", got)
	}
	r.g.OnVBlank()
	// The paused BEGIN+VTX now executed into the new build
	// buffer: 1 vertex, no polygon yet.
	if got := r.rd(0x604); got != 0|1<<16 {
		t.Fatalf("RAM_COUNT after vblank = %08x want 0 polys, 1 vert", got)
	}
}

func TestBoxTestResult(t *testing.T) {
	r := newRig()
	r.identity()
	// A box well inside the view volume: result bit set.
	r.cmd(0x70, 0x00000000, 0x00000000, 0x00080008) // origin 0, size (8,8,8) in 12-frac/16 units
	if st := r.rd(0x600); st&2 == 0 {
		t.Fatalf("BOX_TEST inside: GXSTAT = %08x, result bit clear", st)
	}
	// A box fully beyond the right plane (X = 2.0, past
	// +W): result bit clear.
	r.cmd(0x70, 0x00002000, 0x00000000, 0x00080008)
	if st := r.rd(0x600); st&2 != 0 {
		t.Fatalf("BOX_TEST outside: GXSTAT = %08x, result bit set", st)
	}
}

func TestGXStateRoundTrip(t *testing.T) {
	r := newRig()
	r.identity()
	r.cmd(0x29, 31<<16|0xC0)
	r.cmd(0x40, 0)
	r.cmd(0x23, 0x0000F000, 0)
	r.cmd(0x23, 0x00001000, 0)
	r.cmd(0x23, 0x10001000, 0)
	r.cmd(0x50, 0)
	r.g.OnVBlank()
	blob := make([]byte, gx.StateSize)
	if n := r.g.Serialize(blob); n != gx.StateSize {
		t.Fatalf("serialize n = %d want %d", n, gx.StateSize)
	}
	r2 := newRig()
	if n := r2.g.Deserialize(blob); n != gx.StateSize {
		t.Fatalf("deserialize n = %d want %d", n, gx.StateSize)
	}
	blob2 := make([]byte, gx.StateSize)
	r2.g.Serialize(blob2)
	for i := range blob {
		if blob[i] != blob2[i] {
			t.Fatalf("state bytes differ at %d", i)
		}
	}
}
