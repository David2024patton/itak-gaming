package system

import (
	"bytes"
	"hash/fnv"
	"testing"
)

// The SNES-3 machine gate: a synthetic LoROM whose main
// CPU program performs the real IPL handshake and block
// upload protocol through $2140-$2143 (the same protocol
// the apu package tests drive through its port API),
// loads the APU with a tone program, and the machine
// then runs it. A save state taken mid-frame must
// restore into a fresh machine and continue the audio
// bit-exactly.

// --- a tiny two-pass assembler, just enough for the
// uploader below: labels, rel8 branch fixups, and abs16
// fixups with an optional byte displacement (used for
// the payload table's pointers and indexed table loads).

type asmBuf struct {
	buf    []byte
	labels map[string]int
	fix    []asmFix
}

type asmFix struct {
	at    int
	label string
	disp  int
	rel   bool
}

func newAsm() *asmBuf { return &asmBuf{labels: map[string]int{}} }

func (a *asmBuf) emit(bs ...byte) *asmBuf { a.buf = append(a.buf, bs...); return a }

func (a *asmBuf) label(name string) *asmBuf { a.labels[name] = len(a.buf); return a }

// branch emits a rel8 branch opcode with its offset
// patched to label at build time.
func (a *asmBuf) branch(op byte, label string) *asmBuf {
	a.buf = append(a.buf, op, 0)
	a.fix = append(a.fix, asmFix{at: len(a.buf) - 1, label: label, rel: true})
	return a
}

// abs16 emits a 16-bit placeholder patched to
// base+label+disp at build time.
func (a *asmBuf) abs16(label string, disp int) *asmBuf {
	a.buf = append(a.buf, 0, 0)
	a.fix = append(a.fix, asmFix{at: len(a.buf) - 2, label: label, disp: disp})
	return a
}

func (a *asmBuf) build(base uint16) []byte {
	out := make([]byte, len(a.buf))
	copy(out, a.buf)
	for _, f := range a.fix {
		target := a.labels[f.label] + f.disp
		if f.rel {
			out[f.at] = uint8(int8(target - (f.at + 1)))
		} else {
			addr := int(base) + target
			out[f.at] = uint8(addr)
			out[f.at+1] = uint8(addr >> 8)
		}
	}
	return out
}

// apuToneProgram is the hand-assembled SPC700 tone
// program uploaded by the test (the same program the apu
// package gate uses): it posts magic values 0x11, 0x22,
// 0x33, 0x44 on the four output ports, programs the DSP
// (directory at $1000, master volume 0x7F, voice 0 pitch
// 0x385 = 440 Hz against the 16-sample BRR loop below,
// ADSR attack into full sustain), keys the voice on, and
// spins.
var apuToneProgram = []byte{
	0x8F, 0x11, 0xF4, 0x8F, 0x22, 0xF5, 0x8F, 0x33, 0xF6, 0x8F, 0x44, 0xF7,
	0x8F, 0x5D, 0xF2, 0x8F, 0x10, 0xF3, // DIR = $10 (table at $1000)
	0x8F, 0x0C, 0xF2, 0x8F, 0x7F, 0xF3, // MVOLL
	0x8F, 0x2C, 0xF2, 0x8F, 0x7F, 0xF3, // MVOLR
	0x8F, 0x6C, 0xF2, 0x8F, 0x20, 0xF3, // FLG: echo writes off
	0x8F, 0x00, 0xF2, 0x8F, 0x7F, 0xF3, // V0 VOLL
	0x8F, 0x01, 0xF2, 0x8F, 0x7F, 0xF3, // V0 VOLR
	0x8F, 0x02, 0xF2, 0x8F, 0x85, 0xF3, // V0 PITCHL
	0x8F, 0x03, 0xF2, 0x8F, 0x03, 0xF3, // V0 PITCHH
	0x8F, 0x04, 0xF2, 0x8F, 0x00, 0xF3, // V0 SRCN
	0x8F, 0x05, 0xF2, 0x8F, 0x8F, 0xF3, // V0 ADSR1
	0x8F, 0x06, 0xF2, 0x8F, 0xE0, 0xF3, // V0 ADSR2
	0x8F, 0x4C, 0xF2, 0x8F, 0x01, 0xF3, // KON voice 0
	0x2F, 0xFE, // BRA $
}

// apuToneDir is the sample directory block uploaded to
// $1000: entry 0 start $1100, loop $1100.
var apuToneDir = []byte{0x00, 0x11, 0x00, 0x11}

// apuToneBRR is one looping BRR block: 16 samples of a
// sine (range 4, filter 0, loop and end set).
var apuToneBRR = []byte{0x43, 0x03, 0x56, 0x76, 0x53, 0x0D, 0xBA, 0x9A, 0xBD}

// Direct page variables used by the uploader (WRAM).
const (
	dpKick = 0x00
	dpCnt  = 0x01
	dpLen  = 0x02
	dpSrc  = 0x10 // u16 ROM source pointer for (dp),Y
)

// apuROM builds the LoROM image: the uploader at $8000,
// a payload table, and the three payloads. When the
// upload finishes the program stores 0x42 at WRAM $0020
// and spins.
func apuROM() []byte {
	a := newAsm()
	// Native mode, 16-bit index, 8-bit accumulator.
	a.emit(0x18, 0xFB)       // CLC, XCE
	a.emit(0xC2, 0x10)       // REP #$10
	a.emit(0xE2, 0x20)       // SEP #$20
	a.emit(0xA2, 0xFF, 0x1F) // LDX #$1FFF
	a.emit(0x9A)             // TXS
	// Wait for the IPL signature AA BB on $2140/$2141.
	a.label("waitSig")
	a.emit(0xAD, 0x40, 0x21) // LDA $2140
	a.emit(0xC9, 0xAA)       // CMP #$AA
	a.branch(0xD0, "waitSig")
	a.emit(0xAD, 0x41, 0x21) // LDA $2141
	a.emit(0xC9, 0xBB)       // CMP #$BB
	a.branch(0xD0, "waitSig")
	// First kick is 0xCC; then one block per table entry.
	a.emit(0xA9, 0xCC, 0x85, dpKick) // LDA #$CC; STA kick
	a.emit(0xA2, 0x00, 0x00)         // LDX #$0000 (table offset)
	a.label("blockLoop")
	a.emit(0x20).abs16("sendBlock", 0) // JSR sendBlock
	for i := 0; i < 6; i++ {
		a.emit(0xE8) // INX; entries are 6 bytes
	}
	a.emit(0xE0, 0x12, 0x00) // CPX #18 (3 entries)
	a.branch(0xD0, "blockLoop")
	// Final command: entry $0200, command byte 0.
	a.emit(0xA9, 0x00, 0x8D, 0x42, 0x21) // $2142 = 0x00
	a.emit(0xA9, 0x02, 0x8D, 0x43, 0x21) // $2143 = 0x02
	a.emit(0xA9, 0x00, 0x8D, 0x41, 0x21) // $2141 = 0x00
	a.emit(0xA5, dpKick, 0x8D, 0x40, 0x21)
	a.label("waitFinal")
	a.emit(0xAD, 0x40, 0x21) // LDA $2140
	a.emit(0xC5, dpKick)     // CMP kick
	a.branch(0xD0, "waitFinal")
	a.emit(0xA9, 0x42, 0x8F, 0x20, 0x00, 0x7E) // done flag
	a.label("done")
	a.branch(0x80, "done") // BRA done

	// sendBlock: X is the table offset. Runs one IPL
	// block and leaves the next kick, (len+2)|1, in kick.
	a.label("sendBlock")
	a.emit(0xBD).abs16("tbl", 0) // LDA tbl+0,X
	a.emit(0x8D, 0x42, 0x21)     // STA $2142 (dest low)
	a.emit(0xBD).abs16("tbl", 1) // LDA tbl+1,X
	a.emit(0x8D, 0x43, 0x21)     // STA $2143 (dest high)
	a.emit(0xBD).abs16("tbl", 2) // LDA tbl+2,X
	a.emit(0x85, dpSrc)          // STA src low
	a.emit(0xBD).abs16("tbl", 3) // LDA tbl+3,X
	a.emit(0x85, dpSrc+1)        // STA src high
	a.emit(0xBD).abs16("tbl", 4) // LDA tbl+4,X
	a.emit(0x85, dpLen)          // STA len
	a.emit(0xA9, 0x01, 0x8D, 0x41, 0x21)
	a.emit(0xA5, dpKick, 0x8D, 0x40, 0x21)
	a.label("waitKick")
	a.emit(0xAD, 0x40, 0x21)
	a.emit(0xC5, dpKick)
	a.branch(0xD0, "waitKick")
	a.emit(0x64, dpCnt)      // STZ cnt
	a.emit(0xA0, 0x00, 0x00) // LDY #$0000
	a.label("byteLoop")
	a.emit(0xA5, dpCnt, 0xA8) // LDA cnt; TAY
	a.emit(0xB1, dpSrc)       // LDA (src),Y
	a.emit(0x8D, 0x41, 0x21)  // STA $2141 (data byte)
	a.emit(0xA5, dpCnt)       // LDA cnt
	a.emit(0x8D, 0x40, 0x21)  // STA $2140 (counter)
	a.label("waitEcho")
	a.emit(0xAD, 0x40, 0x21)
	a.emit(0xC5, dpCnt)
	a.branch(0xD0, "waitEcho")
	a.emit(0xE6, dpCnt) // INC cnt
	a.emit(0xA5, dpCnt) // LDA cnt
	a.emit(0xC5, dpLen) // CMP len
	a.branch(0xD0, "byteLoop")
	a.emit(0xA5, dpLen)  // LDA len
	a.emit(0x18)         // CLC
	a.emit(0x69, 0x02)   // ADC #$02
	a.emit(0x09, 0x01)   // ORA #$01
	a.emit(0x85, dpKick) // STA kick
	a.emit(0x60)         // RTS

	// Payload table: dest u16, src u16 (fixed up), len
	// u8, pad u8; three entries.
	a.label("tbl")
	a.emit(0x00, 0x10).abs16("dirData", 0).emit(uint8(len(apuToneDir)), 0)
	a.emit(0x00, 0x11).abs16("brrData", 0).emit(uint8(len(apuToneBRR)), 0)
	a.emit(0x00, 0x02).abs16("progData", 0).emit(uint8(len(apuToneProgram)), 0)

	a.label("dirData")
	a.emit(apuToneDir...)
	a.label("brrData")
	a.emit(apuToneBRR...)
	a.label("progData")
	a.emit(apuToneProgram...)

	img := make([]byte, 0x80000)
	copy(img, a.build(0x8000))
	h := img[0x7FC0 : 0x7FC0+0x40]
	copy(h, "SNES-3 APU IPL TEST  ")
	h[0x15] = 0x20 // LoROM
	h[0x16] = 0x02 // ROM + RAM + battery
	h[0x17] = 0x09 // 512 KB
	h[0x18] = 0x03 // 8 KB SRAM
	h[0x1C], h[0x1D] = 0x55, 0xAA
	h[0x1E], h[0x1F] = 0xAA, 0x55
	h[0x3C], h[0x3D] = 0x00, 0x80 // reset vector 0x8000
	return img
}

func fbHash(s *System) uint64 {
	h := fnv.New64a()
	var b [4]byte
	for _, px := range s.Framebuffer() {
		b[0], b[1], b[2], b[3] = uint8(px), uint8(px>>8), uint8(px>>16), uint8(px>>24)
		_, _ = h.Write(b[:])
	}
	return h.Sum64()
}

// TestAPUIplUploadAndAudioDeterminism boots the machine,
// lets the main CPU program drive the real IPL upload,
// and checks the tone runs; then a mid-frame save state
// restored into a fresh machine must continue the audio
// and the full machine state bit-exactly.
func TestAPUIplUploadAndAudioDeterminism(t *testing.T) {
	rom := apuROM()
	s1, err := New(rom)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Run until the uploader reports done (bounded).
	done := false
	for f := 0; f < 600 && !done; f++ {
		s1.RunFrame()
		done = s1.Map.WRAM()[0x20] == 0x42
	}
	if !done {
		regs := s1.CPU.Regs()
		t.Fatalf("IPL upload never finished: PC=%#06x PBR=%#x ports=%#x %#x %#x %#x",
			regs.PC, regs.PBR,
			s1.APU().ReadPort(0), s1.APU().ReadPort(1),
			s1.APU().ReadPort(2), s1.APU().ReadPort(3))
	}
	// The SPC program presented its magic on the ports.
	for i, want := range []uint8{0x11, 0x22, 0x33, 0x44} {
		if got := s1.APU().ReadPort(i); got != want {
			t.Fatalf("APU port %d = %#x, want %#x (tone program not running)", i, got, want)
		}
	}
	// Let the tone settle, drain, and confirm it is not
	// silent.
	for f := 0; f < 6; f++ {
		s1.RunFrame()
	}
	_ = s1.AudioBuffer()
	for f := 0; f < 6; f++ {
		s1.RunFrame()
	}
	settle := s1.AudioBuffer()
	peak := 0
	for _, v := range settle {
		if int(v) > peak {
			peak = int(v)
		}
		if -int(v) > peak {
			peak = -int(v)
		}
	}
	if len(settle) == 0 || peak == 0 {
		t.Fatalf("tone silent after upload: %d samples, peak %d", len(settle), peak)
	}
	t.Logf("tone running: %d samples over 6 frames, peak %d", len(settle), peak)

	// Save mid-frame: run a partial frame first so the
	// position, the APU cycle phase, and the DSP state
	// are all genuinely mid-stream.
	s1.RunCycles(1234)
	_ = s1.AudioBuffer() // pending audio is not saved
	state := s1.Serialize()
	if len(state) != s1.StateSize() {
		t.Fatalf("state len = %d, StateSize = %d", len(state), s1.StateSize())
	}
	if state[0] != 2 {
		t.Fatalf("state version = %d, want 2", state[0])
	}
	// Continue the original for 30 frames.
	for f := 0; f < 30; f++ {
		s1.RunFrame()
	}
	audioA := s1.AudioBuffer()
	stateA := s1.Serialize()
	fbA := fbHash(s1)

	// Restore into a fresh machine and continue it the
	// same distance.
	s2, err := New(rom)
	if err != nil {
		t.Fatalf("New (restore): %v", err)
	}
	if err := s2.Deserialize(state); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	for f := 0; f < 30; f++ {
		s2.RunFrame()
	}
	audioB := s2.AudioBuffer()
	stateB := s2.Serialize()
	fbB := fbHash(s2)

	if len(audioA) == 0 {
		t.Fatal("no audio in the continuation window")
	}
	if len(audioA) != len(audioB) {
		t.Fatalf("audio continuation lengths differ: %d vs %d values", len(audioA), len(audioB))
	}
	for i := range audioA {
		if audioA[i] != audioB[i] {
			t.Fatalf("audio value %d differs after restore: %d vs %d", i, audioA[i], audioB[i])
		}
	}
	if !bytes.Equal(stateA, stateB) {
		t.Fatal("machine states differ after restore and continuation")
	}
	if fbA != fbB {
		t.Fatalf("framebuffer hash differs after restore: %#x vs %#x", fbA, fbB)
	}
	t.Logf("continuation: %d audio values bit-identical, state %d bytes", len(audioA), len(stateA))

	// A version 1 state (no APU block) is rejected.
	bad := append([]byte{}, state...)
	bad[0] = 1
	if err := s2.Deserialize(bad); err == nil {
		t.Fatal("version 1 state accepted, want rejection")
	}
}
