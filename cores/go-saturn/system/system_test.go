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
	if len(snap) != StateSize {
		t.Fatalf("state size = %d, want %d", len(snap), StateSize)
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
	if err := s.Deserialize(make([]byte, StateSize-1)); err == nil {
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
