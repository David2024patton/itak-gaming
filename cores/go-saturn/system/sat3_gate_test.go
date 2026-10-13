package system

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-saturn/scsp"
	"github.com/davidpatton/itak-gaming/cores/go-saturn/scu"
	"github.com/davidpatton/itak-gaming/cores/go-saturn/smpc"
)

// sat3MasterProgram writes the SMPC SSHON command byte
// (0x02) to COMREG at 0x0010001F and then spins. One
// command only: the polled SMPC samples COMREG once per
// scanline, and real software waits for the SF handshake
// between commands, so this gate issues SNDON and CDON
// through the page between frames instead.
var sat3MasterProgram = []uint16{
	0xD104,         // MOV.L @(4,PC),R1 -> R1 = 0x0010001F
	0xE002,         // MOV #2,R0 (SSHON)
	0x2100,         // MOV.B R0,@R1
	0xAFFE,         // BRA self
	0x0009,         // delay slot: NOP
	0x0009,         // padding
	0x0009,         // padding
	0x0009,         // padding
	0x0009,         // padding
	0x0009,         // padding
	0x0010, 0x001F, // literal: 0x0010001F
}

// setupSAT3Sound loads a 68EC000 spin program and a
// looped 256 sample sine slot into the shared sound RAM
// and SCSP register page, following the SCSP package's
// own key on recipe. The slot keys on at the first
// sample tick after the SMPC SNDON releases the chip.
func setupSAT3Sound(s *System) {
	ram := s.Bus.SoundRAM()
	binary.BigEndian.PutUint32(ram[0:4], 0x0007F000)     // SSP
	binary.BigEndian.PutUint32(ram[4:8], 0x00000100)     // PC
	binary.BigEndian.PutUint16(ram[0x100:0x102], 0x60FE) // BRA.W self
	const sa = 0x2000
	for i := 0; i < 256; i++ {
		v := int16(30000 * math.Sin(2*math.Pi*float64(i)/256))
		binary.BigEndian.PutUint16(ram[sa+i*2:sa+i*2+2], uint16(v))
	}
	regs := s.Bus.SCSPRegisters()
	put := func(off int, v uint16) {
		binary.BigEndian.PutUint16(regs[off:off+2], v)
	}
	put(scsp.RegMaster, 0x800F) // MEM4MB, MVOL 15
	put(scsp.SlotCtrl, 0x4100)  // KYONB, forward loop
	put(scsp.SlotSA, sa)
	put(scsp.SlotLSA, 0)
	put(scsp.SlotLEA, 255)
	put(scsp.SlotEG1, 31)     // instant attack
	put(scsp.SlotEG2, 0x7800) // KRS 15: scaling off
	put(scsp.SlotVol, 0)      // TL 0
	put(scsp.SlotPitch, 0)    // unity pitch
	put(scsp.SlotOutSel, 0xE000)
	put(scsp.SlotCtrl, 0xC100) // KYONEX + KYONB
}

// setupSAT3DMA programs SCU DMA channel 0 for a 16 byte
// Work RAM-H to Work RAM-L copy on the VBlank-IN start
// factor (the enable edge fires it at the first Poll).
func setupSAT3DMA(s *System) {
	for i, v := range []uint32{0x11223344, 0x55667788, 0x99AABBCC, 0xDDEEFF00} {
		s.Bus.Write32(0x06000400+uint32(4*i), v)
	}
	page := s.Bus.SCURegisters()
	put32 := func(off int, v uint32) {
		binary.BigEndian.PutUint32(page[off:off+4], v)
	}
	put32(0x00, 0x06000400) // D0R source
	put32(0x04, 0x00200000) // D0W destination
	put32(0x08, 16)         // D0C count
	put32(0x0C, 0x101)      // D0AD: read +4, write advance
	put32(0x14, 0)          // D0MD: direct, factor VBlank-IN
	put32(0x10, 1)          // D0EN: enable
}

// writeGateDisc builds a synthetic cue/bin image in a
// temp dir: track 1 is ten MODE1/2352 data sectors
// (sector 0 carries the Saturn IP signature), track 2
// is sixty audio sectors of a sine in little endian
// stereo, exactly the CD-DA layout the CD block streams.
func writeGateDisc(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var img []byte
	for i := 0; i < 10; i++ {
		sec := make([]byte, 2352)
		for j := 1; j <= 10; j++ {
			sec[j] = 0xFF
		}
		sec[12] = 0x00
		sec[13] = 0x02
		sec[14] = byte(i)
		sec[15] = 0x01
		user := sec[16 : 16+2048]
		for j := range user {
			user[j] = byte(j)
		}
		if i == 0 {
			copy(user[0:16], "SEGA SEGASATURN ")
		}
		img = append(img, sec...)
	}
	for i := 0; i < 60; i++ {
		sec := make([]byte, 2352)
		for n := 0; n < 588; n++ {
			v := int16(12000 * math.Sin(2*math.Pi*float64(i*588+n)/100))
			binary.LittleEndian.PutUint16(sec[n*4:n*4+2], uint16(v))
			binary.LittleEndian.PutUint16(sec[n*4+2:n*4+4], uint16(v))
		}
		img = append(img, sec...)
	}
	if err := os.WriteFile(filepath.Join(dir, "gate.bin"), img, 0o644); err != nil {
		t.Fatal(err)
	}
	cue := "FILE \"gate.bin\" BINARY\n" +
		"  TRACK 01 MODE1/2352\n" +
		"    INDEX 01 00:00:00\n" +
		"  TRACK 02 AUDIO\n" +
		"    INDEX 01 00:00:10\n"
	if err := os.WriteFile(filepath.Join(dir, "gate.cue"), []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "gate.cue")
}

// CD block page helpers. The register placement is the
// hardware one the cdblock package implements: HIRQ at
// page bytes [0x08:0x0A], its mask at [0x0C:0x0E], CR1
// at [0x18:0x1A] through CR4 at [0x24:0x26].
func cdPageWrite(s *System, off int, v uint16) {
	binary.BigEndian.PutUint16(s.Bus.CDRegisters()[off:off+2], v)
}

func cdPageRead(s *System, off int) uint16 {
	return binary.BigEndian.Uint16(s.Bus.CDRegisters()[off : off+2])
}

// cdStatusCode issues Get Status through the page and
// returns the status code (the low nibble of the status
// byte in the CR1 high byte).
func cdStatusCode(t *testing.T, s *System) int {
	t.Helper()
	cdPageWrite(s, 0x08, 0) // HIRQ clear: CMOK 0 admits a command
	cdPageWrite(s, 0x18, 0x0000)
	cdPageWrite(s, 0x1C, 0)
	cdPageWrite(s, 0x20, 0)
	cdPageWrite(s, 0x24, 0)
	s.RunFrame()
	return int(cdPageRead(s, 0x18)>>8) & 0x0F
}

func frameHash(fb []uint32) uint64 {
	h := uint64(1469598103934665603)
	for _, p := range fb {
		h ^= uint64(p)
		h *= 1099511628211
	}
	return h
}

func equalInt16(a, b []int16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSAT3FullStackDeterminism is the slice gate: the
// master boots the machine through the SMPC (SSHON from
// a CPU program, then SNDON and CDON through the page),
// the slave runs, the SCSP plays a looped slot under its
// 68EC000, the SCU DMA and DSP fire, the CD block plays
// the audio track through the SCSP CD input, and a mid
// run save restores to a byte identical continuation
// including the audio samples.
func TestSAT3FullStackDeterminism(t *testing.T) {
	s := New()
	if got := len(s.Serialize()); got != 4241176 {
		t.Fatalf("fresh v3 state size = %d, want 4241176", got)
	}
	if err := s.LoadDisc(writeGateDisc(t)); err != nil {
		t.Fatalf("LoadDisc: %v", err)
	}
	loadProgram(s, 0x06000000, sat3MasterProgram)
	loadProgram(s, 0x06000200, slaveProgram)
	s.Bus.Write32(0x06000100, 41)
	s.Master.PC = 0x06000000
	s.Slave.PC = 0x06000200
	setupSAT3Sound(s)
	setupSAT3DMA(s)
	s.SCU.DSPWriteProgram(0, 0xF8000000) // ENDI
	s.SCU.RunDSP(4)
	s.SetButtons(ButtonA | ButtonStart)

	// Frame 0: the master program's SSHON lands.
	s.RunFrame()
	if !s.SlaveReleased() {
		t.Fatal("SMPC SSHON from the master program did not release the slave")
	}
	if got := s.Bus.Read32(0x06000104); got != 42 {
		t.Fatalf("slave result cell = %d, want 42 (slave ran after SSHON)", got)
	}
	if s.SCU.Status()&(1<<scu.IntDMA0End) == 0 {
		t.Fatal("SCU DMA channel 0 end not raised")
	}
	if s.SCU.Status()&(1<<scu.IntDSPEnd) == 0 {
		t.Fatal("SCU DSP end not raised")
	}
	for i, want := range []uint32{0x11223344, 0x55667788, 0x99AABBCC, 0xDDEEFF00} {
		if got := s.Bus.Read32(0x00200000 + uint32(4*i)); got != want {
			t.Fatalf("DMA destination word %d = %#x, want %#x", i, got, want)
		}
	}

	// SNDON and CDON through the SMPC page, one command
	// per frame like the SF handshake requires.
	s.Bus.SMPCRegisters()[smpc.OffCOMREG] = smpc.CmdSNDON
	s.RunFrame()
	if !s.Sound.SoundOn() {
		t.Fatal("SMPC SNDON did not release the SCSP")
	}
	if pc := s.Sound.CPU().PC(); pc != 0x100 {
		t.Fatalf("68EC000 PC = %#x, want 0x100 (reset from sound RAM vectors, spinning)", pc)
	}
	s.Bus.SMPCRegisters()[smpc.OffCOMREG] = smpc.CmdCDON
	s.RunFrame()

	// Play the audio track by FAD through the CD block,
	// with the HIRQ mask open so completions raise the
	// SCU A-Bus input.
	trs := s.Disc().Tracks()
	if len(trs) != 2 {
		t.Fatalf("disc tracks = %d, want 2", len(trs))
	}
	start24 := uint32(0x800000 | (trs[1].Start + 150))
	cdPageWrite(s, 0x0C, 0xFFFF) // HIRQ mask: all sources
	cdPageWrite(s, 0x08, 0)      // HIRQ clear
	cdPageWrite(s, 0x18, 0x1000|uint16((start24>>16)&0xFF))
	cdPageWrite(s, 0x1C, uint16(start24&0xFFFF))
	cdPageWrite(s, 0x20, 0) // mode 0 (play once), end high 0
	cdPageWrite(s, 0x24, 0) // end 0: to disc end
	s.RunFrame()
	s.RunFrame()
	if s.SCU.Status()&(1<<scu.IntABusCD) == 0 {
		t.Fatal("CD block interrupt did not reach the SCU A-Bus input")
	}
	if code := cdStatusCode(t, s); code != 0x03 {
		t.Fatalf("CD status = %#x, want 0x03 (PLAY)", code)
	}

	// Pre run, drain (the pending output buffer is not
	// part of the state, by the documented contract),
	// then save mid play.
	for i := 0; i < 8; i++ {
		s.RunFrame()
	}
	_ = s.AudioBuffer()
	snap := s.Serialize()
	if len(snap) != s.StateSize() {
		t.Fatalf("snapshot size = %d, StateSize = %d", len(snap), s.StateSize())
	}
	for i := 0; i < 15; i++ {
		s.RunFrame()
	}
	audioA := s.AudioBuffer()
	snapA := s.Serialize()
	fbA := frameHash(s.Framebuffer())
	if len(audioA) == 0 {
		t.Fatal("no audio in the continuation window")
	}
	nonzero := false
	for _, v := range audioA {
		if v != 0 {
			nonzero = true
			break
		}
	}
	if !nonzero {
		t.Fatal("continuation audio is silent (slot and CD both missing)")
	}

	if err := s.Deserialize(snap); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	for i := 0; i < 15; i++ {
		s.RunFrame()
	}
	audioB := s.AudioBuffer()
	if !equalInt16(audioA, audioB) {
		t.Fatal("audio continuation differs after restore")
	}
	if !bytes.Equal(s.Serialize(), snapA) {
		t.Fatal("state continuation differs after restore")
	}
	if frameHash(s.Framebuffer()) != fbA {
		t.Fatal("framebuffer continuation differs after restore")
	}
	// The CD is still mid track on the restored timeline.
	if code := cdStatusCode(t, s); code != 0x03 {
		t.Fatalf("CD status after restore = %#x, want 0x03 (PLAY)", code)
	}
}
