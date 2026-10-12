package bus

import (
	"encoding/binary"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-genesis/cart"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/sound"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/vdp"
)

// testCart builds a synthetic 256KB cartridge image with a deterministic
// pattern plus two sentinel bytes inside the bank-3 window page.
func testCart() *cart.Cart {
	data := make([]byte, 0x40000)
	for i := range data {
		data[i] = uint8(i>>3) ^ uint8(i)
	}
	data[0x18000] = 0x5A
	data[0x18001] = 0xA5
	return &cart.Cart{Data: data}
}

func newTestBus(c *cart.Cart) *Bus {
	return New(c, vdp.New(), sound.New())
}

func TestROMReads(t *testing.T) {
	c := testCart()
	b := newTestBus(c)

	if got := b.Read8(0x1234); got != c.Data[0x1234] {
		t.Errorf("Read8 ROM = %02X, want %02X", got, c.Data[0x1234])
	}
	if got, want := b.Read16(0x0100), binary.BigEndian.Uint16(c.Data[0x100:0x102]); got != want {
		t.Errorf("Read16 ROM = %04X, want %04X", got, want)
	}
	if got, want := b.Read32(0x0200), binary.BigEndian.Uint32(c.Data[0x200:0x204]); got != want {
		t.Errorf("Read32 ROM = %08X, want %08X", got, want)
	}
}

func TestRAMMirror(t *testing.T) {
	b := newTestBus(testCart())

	b.Write8(0xE00010, 0xAB)
	if got := b.Read8(0xFF0010); got != 0xAB {
		t.Errorf("RAM mirror byte = %02X, want AB", got)
	}
	b.Write16(0xFF1000, 0xBEEF)
	if got := b.Read16(0xE01000); got != 0xBEEF {
		t.Errorf("RAM mirror word = %04X, want BEEF", got)
	}
	b.Write32(0xE02000, 0xDEADBEEF)
	if got := b.Read32(0xFF2000); got != 0xDEADBEEF {
		t.Errorf("RAM mirror long = %08X, want DEADBEEF", got)
	}
}

func TestPadProtocol(t *testing.T) {
	b := newTestBus(testCart())

	if got := b.Read8(0xA10000); got != 0x80 {
		t.Errorf("version register = %02X, want 80", got)
	}

	// Port 1: TH as output, TH driven high. A, Start and Up are pressed
	// but only Up belongs to the TH-high group.
	b.Write8(0xA10009, 0x40)
	b.Write8(0xA10003, 0x40)
	b.SetPad(0, PadA|PadStart|PadUp)
	if got := b.Read8(0xA10003); got != 0xFE {
		t.Errorf("TH high pad read = %02X, want FE", got)
	}
	if got := b.Read8(0xA10009); got != 0x40 {
		t.Errorf("control readback = %02X, want 40", got)
	}

	// TH driven low: Start and A appear, Down joins Up, Left/Right read 0.
	b.Write8(0xA10003, 0x00)
	if got := b.Read8(0xA10003); got != 0x82 {
		t.Errorf("TH low pad read = %02X, want 82", got)
	}

	// TH as input floats high regardless of the data latch; with no
	// buttons pressed every active-low bit reads 1.
	b.Write8(0xA10009, 0x00)
	b.SetPad(0, 0)
	if got := b.Read8(0xA10003); got != 0xFF {
		t.Errorf("TH input pad read = %02X, want FF", got)
	}

	// Expansion port floats.
	if got := b.Read8(0xA10007); got != 0xFF {
		t.Errorf("expansion port = %02X, want FF", got)
	}
}

func TestZ80WindowAndBank(t *testing.T) {
	c := testCart()
	b := newTestBus(c)

	// Z80 RAM through the 68k window and its mirror.
	b.Write(0x0010, 0x77)
	if got := b.Read8(0xA00010); got != 0x77 {
		t.Errorf("68k sees ZRAM = %02X, want 77", got)
	}
	if got := b.Read8(0xA08010); got != 0x77 {
		t.Errorf("68k sees ZRAM mirror = %02X, want 77", got)
	}
	if got := b.Fetch(0x0010); got != 0x77 {
		t.Errorf("Fetch = %02X, want 77", got)
	}
	// Window offsets past ZRAM are unmapped from both sides.
	if got := b.Read8(0xA02000); got != 0xFF {
		t.Errorf("window hole = %02X, want FF", got)
	}
	if got := b.Read(0x2000); got != 0xFF {
		t.Errorf("Z80 hole = %02X, want FF", got)
	}
	if got := b.Read(0x7F11); got != 0xFF {
		t.Errorf("PSG read = %02X, want FF", got)
	}

	// Shift bank 3 into the register (9 bits, LSB first) and read the
	// ROM page it selects through the 0x8000 window.
	for i := 0; i < 9; i++ {
		bit := uint8(0)
		if i < 2 {
			bit = 1
		}
		b.Write(0x6000, bit)
	}
	if b.bank != 3 {
		t.Fatalf("bank = %d, want 3", b.bank)
	}
	if got := b.Read(0x8000); got != 0x5A {
		t.Errorf("banked ROM[0] = %02X, want 5A", got)
	}
	if got := b.Read(0x8001); got != 0xA5 {
		t.Errorf("banked ROM[1] = %02X, want A5", got)
	}
}

func TestZ80Control(t *testing.T) {
	b := newTestBus(testCart())

	// Power-on state: reset asserted, so the Z80 is halted and no reset
	// edge is reported yet.
	if !b.Z80Halted() {
		t.Error("Z80 should be halted at power-on (reset asserted)")
	}
	if b.TakeZ80Reset() {
		t.Error("TakeZ80Reset fired while reset still asserted")
	}

	// Release reset: the one-shot fires exactly once.
	b.Write16(0xA11200, 0x0100)
	if b.Z80Halted() {
		t.Error("Z80 still halted after reset release")
	}
	if !b.TakeZ80Reset() {
		t.Error("TakeZ80Reset did not fire after release")
	}
	if b.TakeZ80Reset() {
		t.Error("TakeZ80Reset fired twice")
	}

	// Bus request by word write. The status reads back 0 once the bus is
	// granted to the 68000: the status bit means "the Z80 still owns the
	// bus", and games poll it waiting for it to clear.
	b.Write16(0xA11100, 0x0100)
	if !b.Z80Halted() {
		t.Error("Z80 not halted after bus request")
	}
	if got := b.Read16(0xA11100); got != 0x0000 {
		t.Errorf("bus request readback = %04X, want 0000 (granted)", got)
	}
	if got := b.Read8(0xA11100); got != 0x00 {
		t.Errorf("bus request byte readback = %02X, want 00 (granted)", got)
	}
	if got := b.Read8(0xA11101); got != 0x00 {
		t.Errorf("bus request low byte = %02X, want 00", got)
	}

	// Release by byte write, re-request by byte write, release by word.
	b.Write8(0xA11100, 0x00)
	if b.Z80Halted() {
		t.Error("Z80 halted after byte release of bus request")
	}
	b.Write8(0xA11100, 0x01)
	if !b.Z80Halted() {
		t.Error("Z80 not halted after byte bus request")
	}
	b.Write16(0xA11100, 0x0000)
	if b.Z80Halted() {
		t.Error("Z80 halted after word release of bus request")
	}
	if got := b.Read16(0xA11100); got != 0x0100 {
		t.Errorf("bus request readback = %04X, want 0100 (Z80 owns the bus again)", got)
	}
	if got := b.Read8(0xA11100); got != 0x01 {
		t.Errorf("bus request byte readback = %02X, want 01 (Z80 owns the bus again)", got)
	}

	// Re-asserting reset halts again without an edge report.
	b.Write16(0xA11200, 0x0000)
	if !b.Z80Halted() {
		t.Error("Z80 not halted after reset re-assert")
	}
	if b.TakeZ80Reset() {
		t.Error("TakeZ80Reset fired on assert")
	}
}

func TestSRAMOverlay(t *testing.T) {
	data := make([]byte, 0x400000)
	data[0x200001] = 0x11 // ROM byte behind the SRAM window
	c := &cart.Cart{Data: data, SRAM: true, SRAMStart: 0x200000, SRAMEnd: 0x20FFFF}
	b := newTestBus(c)

	// Switch off: the window reads ROM.
	if got := b.Read8(0x200001); got != 0x11 {
		t.Fatalf("pre-switch read = %02X, want ROM 11", got)
	}

	// Switch on via the byte lane. Even addresses float, odd store.
	b.Write8(0xA130F1, 0x01)
	if got := b.Read8(0x200000); got != 0xFF {
		t.Errorf("even SRAM read = %02X, want FF", got)
	}
	b.Write8(0x200000, 0x99)
	b.Write8(0x200001, 0x42)
	b.Write8(0x200003, 0x43)
	if got := b.Read8(0x200001); got != 0x42 {
		t.Errorf("SRAM[0] = %02X, want 42", got)
	}
	if got := b.Read8(0x200003); got != 0x43 {
		t.Errorf("SRAM[1] = %02X, want 43", got)
	}
	if got := b.Read8(0x200000); got != 0xFF {
		t.Errorf("even SRAM write leaked: read = %02X, want FF", got)
	}

	// Word writes use the low byte: 0x0000 switches out, 0x0001 back in,
	// and the contents survive the round trip.
	b.Write16(0xA130F0, 0x0000)
	if got := b.Read8(0x200001); got != 0x11 {
		t.Errorf("switched-out read = %02X, want ROM 11", got)
	}
	b.Write16(0xA130F0, 0x0001)
	if got := b.Read8(0x200001); got != 0x42 {
		t.Errorf("switched-in read = %02X, want SRAM 42", got)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	data := make([]byte, 0x400000)
	data[0x28000] = 0x3C // bank 5 window sentinel
	c := &cart.Cart{Data: data, SRAM: true, SRAMStart: 0x200000, SRAMEnd: 0x20FFFF}
	b := newTestBus(c)

	b.Write8(0xE00010, 0xDE)    // work RAM
	b.Write(0x0010, 0xAD)       // Z80 RAM
	b.Write8(0xA130F1, 0x01)    // SRAM on
	b.Write8(0x200001, 0x77)    // SRAM[0]
	b.Write8(0xA10009, 0x40)    // IO control
	b.Write8(0xA10003, 0x40)    // IO data
	b.Write16(0xA11100, 0x0100) // Z80 bus requested
	b.Write16(0xA11200, 0x0100) // reset released
	b.TakeZ80Reset()            // consume the edge
	for i, bit := range []uint8{1, 0, 1, 0, 0, 0, 0, 0, 0} {
		_ = i
		b.Write(0x6000, bit) // bank 5
	}
	if b.bank != 5 {
		t.Fatalf("bank = %d, want 5", b.bank)
	}

	if got, want := b.SerializeSize(), 0x10000+0x2000+4+0x10000+16; got != want {
		t.Fatalf("SerializeSize = %d, want %d", got, want)
	}
	buf := make([]byte, b.SerializeSize())
	if err := b.Serialize(buf); err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if err := b.Serialize(make([]byte, 8)); err == nil {
		t.Error("Serialize accepted a short buffer")
	}

	b2 := newTestBus(c)
	if err := b2.Deserialize(buf); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if err := b2.Deserialize(make([]byte, 8)); err == nil {
		t.Error("Deserialize accepted a short buffer")
	}
	if got := b2.Read8(0xE00010); got != 0xDE {
		t.Errorf("RAM round trip = %02X, want DE", got)
	}
	if got := b2.Read(0x0010); got != 0xAD {
		t.Errorf("ZRAM round trip = %02X, want AD", got)
	}
	if got := b2.Read8(0x200001); got != 0x77 {
		t.Errorf("SRAM round trip = %02X, want 77", got)
	}
	if got := b2.Read8(0xA10009); got != 0x40 {
		t.Errorf("ioCtrl round trip = %02X, want 40", got)
	}
	if !b2.Z80Halted() {
		t.Error("z80Req flag lost in round trip")
	}
	if got := b2.Read(0x8000); got != 0x3C {
		t.Errorf("bank round trip: window = %02X, want 3C", got)
	}
}
