package ppu

import (
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-nes/cart"
)

// fakeMem is a flat 8KB CHR space with fixed mirroring, enough
// to drive the PPU standalone.
type fakeMem struct {
	chr [0x2000]uint8
}

func (m *fakeMem) ReadCHR(addr uint16) uint8 { return m.chr[addr&0x1FFF] }
func (m *fakeMem) WriteCHR(addr uint16, v uint8) {
	m.chr[addr&0x1FFF] = v
}
func (m *fakeMem) MirrorMode() cart.Mirror { return cart.MirrorVertical }
func (m *fakeMem) ClockA12()               {}

// TestSerializeRoundtrip runs the PPU with rendering on for a
// few frames, serializes mid-frame, and requires the restored
// PPU to produce a byte-identical continuation.
func TestSerializeRoundtrip(t *testing.T) {
	mem := &fakeMem{}
	for i := range mem.chr {
		mem.chr[i] = uint8(i * 7)
	}
	p := New(mem)
	// Rendering on, background from $1000, NMI on.
	p.WriteReg(0, 0x88)
	p.WriteReg(1, 0x08)
	run := func(p *PPU, dots int) {
		for i := 0; i < dots; i++ {
			p.Tick(1)
		}
	}
	run(p, 89342*2+12345)
	state := p.Serialize()
	if len(state) != SerializeSize {
		t.Fatalf("Serialize len = %d, want %d", len(state), SerializeSize)
	}
	// The framebuffer is not part of the state (it is a pure
	// output), so run both PPUs to one full frame past a frame
	// boundary before comparing pixels.
	runToFrame := func(p *PPU) {
		f := p.Frames()
		for p.Frames() < f+2 {
			p.Tick(1)
		}
	}
	runToFrame(p)
	want := append([]uint32(nil), p.Framebuffer()...)
	wantFrames := p.Frames()

	q := New(mem)
	if err := q.Deserialize(state); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	runToFrame(q)
	if q.Frames() != wantFrames {
		t.Fatalf("frames diverged: got %d want %d", q.Frames(), wantFrames)
	}
	got := q.Framebuffer()
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("framebuffer pixel %d diverged: got %08X want %08X", i, got[i], want[i])
		}
	}
}

// TestBackdropAndPaletteMirror checks the palette behaviors
// games lean on: with rendering off the backdrop is palette
// entry 0 (unless v points into palette space, the $2006
// address color quirk), palette reads return their value
// immediately, and the sprite backdrop entries mirror the
// background ones ($3F10 reads $3F00).
func TestBackdropAndPaletteMirror(t *testing.T) {
	p := New(&fakeMem{})
	// Set backdrop (palette 0) to color index $16 through the
	// register path: $2006 to $3F00, one $2007 write.
	p.WriteReg(6, 0x3F)
	p.WriteReg(6, 0x00)
	p.WriteReg(7, 0x16)
	// Point v back at nametable space (the write incremented
	// it into palette space, where the display would show the
	// address-color quirk instead of the backdrop).
	p.WriteReg(6, 0x20)
	p.WriteReg(6, 0x00)
	// One full frame: with rendering off every visible pixel
	// is the backdrop color.
	for i := 0; i < 89342; i++ {
		p.Tick(1)
	}
	px := p.Framebuffer()[0]
	if want := p.colorRGB(0x16); px != want {
		t.Fatalf("backdrop pixel = %08X, want palette $16 = %08X", px, want)
	}
	// $3F10 reads back what $3F00 holds, immediately (palette
	// reads are not buffered).
	p.WriteReg(6, 0x3F)
	p.WriteReg(6, 0x10)
	if got := p.ReadReg(7); got != 0x16 {
		t.Fatalf("$3F10 read = %02X, want $16 (mirror of $3F00)", got)
	}
}
