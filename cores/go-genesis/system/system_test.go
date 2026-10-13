package system

import (
	"encoding/binary"
	"testing"
)

// ymTimerROM builds a synthetic cartridge whose 68000 programs
// YM2612 timer A with a 432 master-clock period (NA = 1000), then
// polls the status port in a tight loop and writes a magic value
// to work RAM the moment it sees the timer A flag. reg27 is the
// value written to the timer mode register: 0x05 loads and enables
// timer A, 0x04 enables without loading (the timer never counts).
//
// The encodings are plain absolute-long moves: 0x13FC is
// move.b #imm, addr.l; 0x0839 is btst #imm, addr.l; 0x23FC is
// move.l #imm, addr.l. (In the mode-7 destination field the
// register bits select the sub-mode, and absolute long is 001.)
func ymTimerROM(reg27 byte) []byte {
	rom := make([]byte, 0x10000)
	binary.BigEndian.PutUint32(rom[0:], 0x00FFFE00) // initial SSP
	binary.BigEndian.PutUint32(rom[4:], 0x00000200) // reset PC
	copy(rom[0x100:], "SEGA GENESIS    ")

	var code []byte
	pair := func(reg, val byte) { // select register, write data port
		code = append(code, 0x13, 0xFC, 0x00, reg, 0x00, 0xA0, 0x40, 0x00)
		code = append(code, 0x13, 0xFC, 0x00, val, 0x00, 0xA0, 0x40, 0x01)
	}
	pair(0x24, 0xFA) // timer A period high bits
	pair(0x25, 0x00) // NA = 1000, period 18*(1024-1000) = 432 clocks
	pair(0x27, reg27)
	poll := len(code)
	code = append(code, 0x08, 0x39, 0x00, 0x00, 0x00, 0xA0, 0x40, 0x00) // btst #0, status
	code = append(code, 0x67, byte(poll-(len(code)+2)))                 // beq.s poll
	code = append(code, 0x23, 0xFC, 0x1B, 0xAD, 0xB0, 0x02,
		0x00, 0xFF, 0x00, 0x00) // move.l #0x1BADB002, $FF0000.l
	code = append(code, 0x60, 0xFE) // bra.s *
	copy(rom[0x200:], code)
	return rom
}

const ymTimerMagic = 0x1BADB002

// TestYMTimerFlagVisibleMidFrame proves the system steps the
// YM2612 timers while the frame runs: a 68000 polling the status
// port sees the timer A flag during the first frame and reacts.
// If the timers only moved inside the frame-end audio render, the
// poll loop would still be spinning when the frame ends and the
// magic would not be in RAM yet.
func TestYMTimerFlagVisibleMidFrame(t *testing.T) {
	sys := New()
	if err := sys.LoadGame(ymTimerROM(0x05)); err != nil {
		t.Fatalf("LoadGame: %v", err)
	}
	sys.RunFrame()
	if got := binary.BigEndian.Uint32(sys.SystemRAM()[0:4]); got != ymTimerMagic {
		t.Fatalf("RAM magic after one frame = %#08x, want %#08x: the polling 68000 never saw the timer A flag mid-frame", got, ymTimerMagic)
	}
}

// TestYMTimerFlagNeedsLoad is the control: with the load bit clear
// the timer never counts, the flag never rises, and the magic
// must stay out of RAM. It guards against the test above passing
// for a reason other than real timer expiry.
func TestYMTimerFlagNeedsLoad(t *testing.T) {
	sys := New()
	if err := sys.LoadGame(ymTimerROM(0x04)); err != nil {
		t.Fatalf("LoadGame: %v", err)
	}
	sys.RunFrame()
	if got := binary.BigEndian.Uint32(sys.SystemRAM()[0:4]); got == ymTimerMagic {
		t.Fatalf("RAM magic present with timer A never loaded: the flag rose without the timer counting")
	}
}
