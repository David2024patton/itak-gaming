package system

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-saturn/cd"
)

// masterProgram writes the value 42 into the cell at
// 0x06000100 and then spins in a BRA loop. The literal
// pool word at index 6 is the cell address.
var masterProgram = []uint16{
	0xD102,         // MOV.L @(2,PC),R1  -> R1 = 0x06000100
	0xE02A,         // MOV #42,R0
	0x2102,         // MOV.L R0,@R1
	0xAFFE,         // BRA self
	0x0009,         // delay slot: NOP (MOVT R0 encoding, T is 0)
	0x0000,         // padding, never executed
	0x0600, 0x0100, // literal: 0x06000100
}

// slaveProgram reads the cell at 0x06000100, adds 1, and
// writes the result into the cell at 0x06000104, then
// spins. Its BRA delay slot is a NOP; the literals sit
// after the NOP and a pad word.
var slaveProgram = []uint16{
	0xD103,         // MOV.L @(3,PC),R1  -> R1 = 0x06000100
	0x6012,         // MOV.L @R1,R0
	0x7001,         // ADD #1,R0
	0xD103,         // MOV.L @(3,PC),R1  -> R1 = 0x06000104
	0x2102,         // MOV.L R0,@R1
	0xAFFE,         // BRA self
	0x0009,         // delay slot: NOP
	0x0009,         // padding, never executed
	0x0600, 0x0100, // literal: 0x06000100
	0x0600, 0x0104, // literal: 0x06000104
}

func loadProgram(s *System, addr uint32, prog []uint16) {
	for i, w := range prog {
		s.Bus.Write16(addr+uint32(2*i), w)
	}
}

func TestDualCPUCommunication(t *testing.T) {
	s := New()
	loadProgram(s, 0x06000000, masterProgram)
	loadProgram(s, 0x06000200, slaveProgram)
	s.Master.PC = 0x06000000
	s.Slave.PC = 0x06000200

	s.RunFrame()
	if got := s.Bus.Read32(0x06000100); got != 42 {
		t.Fatalf("master cell = %#x, want 42", got)
	}
	if got := s.Bus.Read32(0x06000104); got != 0 {
		t.Fatalf("slave wrote before release: cell = %#x", got)
	}

	s.ReleaseSlave()
	s.RunFrame()
	if got := s.Bus.Read32(0x06000104); got != 43 {
		t.Fatalf("slave cell = %#x, want 43", got)
	}
}

func TestSaveStateDeterminism(t *testing.T) {
	s := New()
	loadProgram(s, 0x06000000, masterProgram)
	loadProgram(s, 0x06000200, slaveProgram)
	s.Master.PC = 0x06000000
	s.Slave.PC = 0x06000200
	s.ReleaseSlave()
	s.SetButtons(ButtonStart | ButtonA)
	s.RunFrame()

	snap := s.Serialize()
	if len(snap) != s.StateSize() {
		t.Fatalf("state size = %d, want %d", len(snap), s.StateSize())
	}
	s.RunFrame()
	want := s.Serialize()

	s2 := New()
	if err := s2.Deserialize(snap); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if !s2.SlaveReleased() {
		t.Fatal("slave release flag lost in restore")
	}
	if s2.Buttons() != ButtonStart|ButtonA {
		t.Fatalf("buttons = %#x after restore", s2.Buttons())
	}
	s2.RunFrame()
	got := s2.Serialize()
	if !bytes.Equal(got, want) {
		t.Fatal("continuation after restore differs from uninterrupted run")
	}
	if v := s2.Bus.Read32(0x06000104); v != 43 {
		t.Fatalf("slave cell after restore = %#x, want 43", v)
	}
}

func TestDeserializeRejectsBadStates(t *testing.T) {
	s := New()
	snap := s.Serialize()
	if err := s.Deserialize(snap[:len(snap)-1]); err == nil {
		t.Fatal("short state accepted")
	}
	bad := s.Serialize()
	bad[0] = 99
	if err := s.Deserialize(bad); err == nil {
		t.Fatal("bad version accepted")
	}
}

func TestLoadDiscImageHeader(t *testing.T) {
	sector := make([]byte, 2048)
	copy(sector[0:16], "SEGA SEGASATURN ")
	copy(sector[0x60:0x60+14], "SYNTHETIC TEST")
	binary.BigEndian.PutUint32(sector[0xF0:0xF4], 0x06001000)
	binary.BigEndian.PutUint32(sector[0xF8:0xFC], 0x06000800)
	img, err := cd.OpenBytes(sector, 2048)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	s := New()
	if err := s.LoadDiscImage(img); err != nil {
		t.Fatalf("LoadDiscImage: %v", err)
	}
	hdr := s.Header()
	if hdr == nil {
		t.Fatal("no header stored")
	}
	if hdr.MasterStack != 0x06001000 || hdr.SlaveStack != 0x06000800 {
		t.Fatalf("stacks = %#x / %#x", hdr.MasterStack, hdr.SlaveStack)
	}

	blank, err := cd.OpenBytes(make([]byte, 2048), 2048)
	if err != nil {
		t.Fatalf("OpenBytes blank: %v", err)
	}
	if err := s.LoadDiscImage(blank); err == nil {
		t.Fatal("blank image accepted as a Saturn disc")
	}
}

func TestFramebufferBlank(t *testing.T) {
	s := New()
	fb := s.Framebuffer()
	if len(fb) != Width*Height {
		t.Fatalf("framebuffer size = %d, want %d", len(fb), Width*Height)
	}
	for i, px := range fb {
		if px != 0 {
			t.Fatalf("framebuffer pixel %d = %#x, want blank", i, px)
		}
	}
}

// irqMasterProgram points VBR at 0x06001000, loads SR with
// 0 (interrupt mask open, block bit clear), then spins.
// Its literals sit after the code; the disp fields match
// the offsets in the listing.
var irqMasterProgram = []uint16{
	0xD102,         // MOV.L @(2,PC),R1 -> R1 = 0x06001000
	0x412E,         // LDC R1,VBR
	0xD102,         // MOV.L @(2,PC),R1 -> R1 = 0x00000000
	0x410E,         // LDC R1,SR
	0xAFFE,         // BRA self
	0x0009,         // delay slot NOP
	0x0600, 0x1000, // literal: 0x06001000 (VBR)
	0x0000, 0x0000, // literal: 0x00000000 (SR)
}

// irqHandler writes 42 into the cell at 0x06003000 and
// returns from the interrupt.
var irqHandler = []uint16{
	0xD102,         // MOV.L @(2,PC),R1 -> R1 = 0x06003000
	0xE02A,         // MOV #42,R0
	0x2102,         // MOV.L R0,@R1
	0x002B,         // RTE
	0x0009,         // delay slot NOP
	0x0009,         // padding
	0x0600, 0x3000, // literal: 0x06003000
}

func setupIRQ(s *System) {
	loadProgram(s, 0x06000000, irqMasterProgram)
	loadProgram(s, 0x06002000, irqHandler)
	// Vector entries for VBlank-IN (0x40), VBlank-OUT
	// (0x41), HBlank-IN (0x42), and the sprite draw end
	// (0x4D) all point at the one handler; whichever is
	// delivered first proves the path. In practice
	// VBlank-OUT wins the first frame because its IST bit
	// is raised at line 0 and never cleared by the
	// handler.
	s.Bus.Write32(0x06001100, 0x06002000)
	s.Bus.Write32(0x06001104, 0x06002000)
	s.Bus.Write32(0x06001108, 0x06002000)
	s.Bus.Write32(0x06001134, 0x06002000)
	s.Master.PC = 0x06000000
}

func TestVBlankInterruptDelivered(t *testing.T) {
	s := New()
	setupIRQ(s)
	s.RunFrame()
	if got := s.Bus.Read32(0x06003000); got != 42 {
		t.Fatalf("handler cell = %#x, want 42 (interrupt never reached the CPU)", got)
	}
	// The VBlank-IN status bit must be set in IST at
	// 0x05FE00A4 even though the handler never clears it.
	if got := s.Bus.Read32(0x05FE00A4); got&1 == 0 {
		t.Fatalf("IST = %#x, want VBlank-IN bit set", got)
	}
}

func TestVBlankInterruptMaskedByIMS(t *testing.T) {
	s := New()
	setupIRQ(s)
	// Mask every wired source: VBlank-IN, VBlank-OUT,
	// HBlank-IN, the sprite draw end (bit 12, raised by
	// the frame boundary auto draw), and, since SAT-3
	// wired the SCU timers into the frame, timer 0 (bit
	// 3: its power on compare of 0 fires at line 0).
	// Timer 1 stays silent here because its T1MD enable
	// bit is clear at power on.
	s.Bus.Write32(0x05FE00A0, 0x0000100F)
	s.RunFrame()
	if got := s.Bus.Read32(0x06003000); got != 0 {
		t.Fatalf("handler ran while masked: cell = %#x", got)
	}
	// Status still records the events; only delivery is
	// blocked.
	if got := s.Bus.Read32(0x05FE00A4); got&0x100F != 0x100F {
		t.Fatalf("IST = %#x, want bits 0, 1, 2, 3, and 12 set", got)
	}
}

// pokeProgram is a table driven device programmer. It
// walks (address, value, pad) entries of 8 bytes at the
// table address in its literal pool, storing each 16 bit
// value at its address through the bus, and stops at an
// entry whose address is zero. The slave has nothing to
// do in this gate beyond spinning once released.
var pokeProgram = []uint16{
	0xD107,         // MOV.L @(7,PC),R1 -> R1 = table address
	0x6216,         // loop: MOV.L @R1+,R2 (address)
	0x6315,         // MOV.W @R1+,R3 (value)
	0x6415,         // MOV.W @R1+,R4 (pad, keeps the 8 byte stride)
	0xE000,         // MOV #0,R0
	0x3200,         // CMP/EQ R0,R2
	0x8903,         // BT done (+3)
	0x2231,         // MOV.W R3,@R2
	0xAFF7,         // BRA loop (-9)
	0x0009,         // delay slot NOP
	0x0009,         // padding (unreachable)
	0xAFFE,         // done: BRA done
	0x0009,         // delay slot NOP
	0x0009,         // padding
	0x0009,         // padding
	0x0009,         // padding
	0x0600, 0x0100, // literal: 0x06000100 (table)
}

// poke builds one table of (address, value) pairs for
// pokeProgram, terminated by a zero address entry.
func poke(entries ...uint32) []byte {
	out := make([]byte, 0, len(entries)*4+8)
	for i := 0; i < len(entries); i += 2 {
		var b [8]byte
		binary.BigEndian.PutUint32(b[0:4], entries[i])
		binary.BigEndian.PutUint16(b[4:6], uint16(entries[i+1]))
		out = append(out, b[:]...)
	}
	return append(out, make([]byte, 8)...)
}

// vdpPokeTable programs the SAT-2 display through the
// bus: VDP2 shows NBG0 with one 16 color character (code
// 1 = green, code 2 = red, palette supplement 1), and
// VDP1 draws one white polygon at (100,50)-(119,69)
// whose sprite priority (7) beats NBG0 (2).
func vdpPokeTable() []byte {
	e := []uint32{
		// VDP2 registers.
		0x05F80000, 0x8000, // TVMD: DISP on, 320x224
		0x05F80020, 0x0001, // BGON: NBG0 on
		0x05F80028, 0x0000, // CHCTLA: NBG0 16 colors, cells, 1x1
		0x05F80030, 0x8020, // PNCN0: 1 word, SPLT = 1
		0x05F80078, 0x0001, // ZMXIN0: zoom increment 1
		0x05F8007C, 0x0001, // ZMYIN0: zoom increment 1
		0x05F800F8, 0x0002, // PRINA: NBG0 priority 2
		0x05F800F4, 0x0007, // PRISC: sprite S4 priority 7
		// CRAM: palette 16 code 1 green, code 2 red,
		// sprite color index 31 white.
		0x05F00202, 0x03E0,
		0x05F00204, 0x7C00,
		0x05F0003E, 0x7FFF,
		// VDP2 VRAM: pattern name (0,0) = character 1,
		// then the character pattern: first byte codes
		// 1,2 and the rest code 1.
		0x05E00000, 0x0001,
		0x05E00020, 0x1211,
	}
	for i := 1; i < 16; i++ {
		e = append(e, 0x05E00020+uint32(2*i), 0x1111)
	}
	e = append(e,
		// VDP1 command list: one polygon, then END.
		0x05C00000, 0x0004, // CMDCTRL: polygon
		0x05C00002, 0x0000, // CMDLINK
		0x05C00004, 0x0000, // CMDPMOD
		0x05C00006, 0x801F, // CMDCOLR: PR bit set, color 31
		0x05C00008, 0x0000, // CMDSRCA
		0x05C0000A, 0x0000, // CMDSIZE
		0x05C0000C, 100, // XA
		0x05C0000E, 50, // YA
		0x05C00010, 119, // XB
		0x05C00012, 50, // YB
		0x05C00014, 119, // XC
		0x05C00016, 69, // YC
		0x05C00018, 100, // XD
		0x05C0001A, 69, // YD
		0x05C0001C, 0x0000, // CMDGRDA
		0x05C00020, 0x8000, // END command CMDCTRL
		// VDP1 registers: 1 cycle frame change, auto
		// draw, erase with 0 over the full screen.
		0x05D00000, 0x0000, // TVMR
		0x05D00002, 0x0000, // FBCR
		0x05D00004, 0x0000, // PTMR
		0x05D00006, 0x0000, // EWDR
		0x05D00008, 0x0000, // EWLR: (0,0)
		0x05D0000A, 0x4EDF, // EWRR: x2 = 39, y2 = 223
	)
	return poke(e...)
}

func TestVDPDualCPUDisplayDeterminism(t *testing.T) {
	s := New()
	loadProgram(s, 0x06000000, pokeProgram)
	// The slave spin sits clear of the poke table, which
	// occupies 0x06000100 to 0x06000280.
	loadProgram(s, 0x06000400, []uint16{0xAFFE, 0x0009})
	table := vdpPokeTable()
	for i, b := range table {
		s.Bus.Write8(0x06000100+uint32(i), b)
	}
	s.Master.PC = 0x06000000
	s.Slave.PC = 0x06000400
	s.ReleaseSlave()

	s.RunFrame()
	s.RunFrame()
	snap := s.Serialize()
	if len(snap) != s.StateSize() {
		t.Fatalf("state size = %d, want %d", len(snap), s.StateSize())
	}
	s.RunFrame()
	s.RunFrame()
	wantState := s.Serialize()
	wantFB := append([]uint32(nil), s.Framebuffer()...)

	s2 := New()
	if err := s2.Deserialize(snap); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	s2.RunFrame()
	s2.RunFrame()
	if got := s2.Serialize(); !bytes.Equal(got, wantState) {
		t.Fatal("continuation state differs after restore")
	}
	gotFB := s2.Framebuffer()
	if len(gotFB) != len(wantFB) {
		t.Fatalf("framebuffer size = %d, want %d", len(gotFB), len(wantFB))
	}
	for i := range wantFB {
		if gotFB[i] != wantFB[i] {
			t.Fatalf("framebuffer pixel %d = %#x, want %#x", i, gotFB[i], wantFB[i])
		}
	}

	// Hand derived probes on the restored machine.
	px := func(x, y int) uint32 { return gotFB[y*Width+x] }
	if got := px(0, 0); got != 0xFF00FF00 {
		t.Fatalf("pixel (0,0) = %#x, want green 0xFF00FF00 (NBG0 code 1)", got)
	}
	if got := px(1, 0); got != 0xFFFF0000 {
		t.Fatalf("pixel (1,0) = %#x, want red 0xFFFF0000 (NBG0 code 2)", got)
	}
	if got := px(105, 55); got != 0xFFFFFFFF {
		t.Fatalf("pixel (105,55) = %#x, want white 0xFFFFFFFF (VDP1 polygon over NBG0)", got)
	}
	if got := px(99, 55); got != 0xFF000000 {
		t.Fatalf("pixel (99,55) = %#x, want black 0xFF000000 (NBG0 palette 16 code 0)", got)
	}
}
