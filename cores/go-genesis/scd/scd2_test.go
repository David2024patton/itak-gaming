package scd

import (
	"bytes"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-genesis/chips/m68k"
)

// ---------------------------------------------------------------------------
// SCD-2 unit gates: PRG-RAM banking and write protection, bus
// request acknowledge, the CDD nibble interface routed through the
// sub CPU register page alongside the SCD-1 CDC latches, sub CPU
// interrupt delivery gating, unit level audio rendering, and the
// SCD-2 state extension round trip.
// ---------------------------------------------------------------------------

func TestPRGWindowBankAndProtect(t *testing.T) {
	u := NewUnit(nil)

	// Bank 0, no protection: the window writes physical PRG-RAM.
	u.MainWrite8(0x02, 0x00)
	u.MainPRGWrite8(0x420010, 0x11)
	if got := u.PRGRAM()[0x10]; got != 0x11 {
		t.Fatalf("PRGRAM()[0x10] = %#04x, want 0x11", got)
	}

	// WP field 1 protects the first 64 KB against main writes.
	// Reads are unaffected and sub CPU writes are never blocked.
	u.MainWrite8(0x02, 0x04)
	if got := u.PRGProtectBytes(); got != 0x10000 {
		t.Fatalf("PRGProtectBytes = %#x, want 0x10000", got)
	}
	u.MainPRGWrite8(0x420010, 0x22)
	if got := u.PRGRAM()[0x10]; got != 0x11 {
		t.Fatalf("protected main write landed: PRGRAM()[0x10] = %#04x, want 0x11", got)
	}
	u.MainPRGWrite8(0x430000, 0x33) // window offset 0x10000, past the protected block
	if got := u.PRGRAM()[0x10000]; got != 0x33 {
		t.Fatalf("PRGRAM()[0x10000] = %#04x, want 0x33 (outside the protected block)", got)
	}
	if got := u.MainPRGRead8(0x420010); got != 0x11 {
		t.Fatalf("protected region read = %#04x, want 0x11 (protection is write only)", got)
	}
	u.Write8(0x020010, 0x44) // sub CPU write into the protected block
	if got := u.PRGRAM()[0x10]; got != 0x44 {
		t.Fatalf("sub write into protected block = %#04x, want 0x44 (sub is never blocked)", got)
	}

	// Bank 3: the same window offset lands at physical 0x60010,
	// and the bank reads back in the memory mode register.
	u.MainWrite8(0x02, 0xC0)
	if got := u.PRGBank(); got != 3 {
		t.Fatalf("PRGBank = %d, want 3", got)
	}
	if got := u.MainRead8(0x02); got&0xC0 != 0xC0 {
		t.Fatalf("memory mode readback = %#04x, want bank bits 0xC0 set", got)
	}
	u.MainPRGWrite8(0x420010, 0x55)
	if got := u.PRGRAM()[0x60010]; got != 0x55 {
		t.Fatalf("PRGRAM()[0x60010] = %#04x, want 0x55 (bank 3 window write)", got)
	}
	if got := u.PRGRAM()[0x10]; got != 0x44 {
		t.Fatalf("PRGRAM()[0x10] = %#04x, want 0x44 undisturbed", got)
	}
}

// TestBankSwitchChangesSubExecution loads a tiny program through
// the main CPU bank window into bank 2 physical space and runs the
// sub CPU at its flat address there: the magic it publishes is the
// bank 2 program's, proving the window feeds the memory the sub
// CPU executes.
func TestBankSwitchChangesSubExecution(t *testing.T) {
	u := NewUnit(nil)

	// Program: MOVE.W #magic, $FF8020.l (comm status 0); BRA .
	program := func(magic uint16) []byte {
		return []byte{
			0x33, 0xFC, byte(magic >> 8), byte(magic),
			0x00, 0xFF, 0x80, 0x20,
			0x60, 0xFE,
		}
	}

	// Bank 0 program at window base with magic 0x0B00.
	u.MainWrite8(0x02, 0x00)
	for i, b := range program(0x0B00) {
		u.MainPRGWrite8(0x420000+uint32(i), b)
	}
	// Bank 2 program at the same window offset with magic 0x0B02.
	u.MainWrite8(0x02, 0x80)
	for i, b := range program(0x0B02) {
		u.MainPRGWrite8(0x420000+uint32(i), b)
	}

	run := func(pc uint32) uint16 {
		cpu := m68k.New(u)
		r := cpu.Registers()
		r.PC = pc
		r.SR = 0x2700
		r.SSP = 0x000A0000
		cpu.SetState(r)
		cpu.StepCycles(500)
		return u.CommStatus(0)
	}

	// Sub CPU flat addresses: physical 0 is sub 0x020000,
	// physical 0x40000 (bank 2) is sub 0x060000.
	if got := run(0x020000); got != 0x0B00 {
		t.Fatalf("bank 0 program published %#06x, want 0x0B00", got)
	}
	if got := run(0x060000); got != 0x0B02 {
		t.Fatalf("bank 2 program published %#06x, want 0x0B02", got)
	}
}

func TestBusRequestAckAndHalt(t *testing.T) {
	u := NewUnit(nil)
	if !u.SubHalted() {
		t.Fatal("sub CPU not halted at power-on (RES = 0)")
	}
	if got := u.MainRead8(0x00); got != 0x00 {
		t.Fatalf("main control readback = %#04x, want 0x00", got)
	}
	u.MainWrite8(0x00, 0x01) // RES = 1, BRQ = 0
	if u.SubHalted() {
		t.Fatal("sub CPU halted with RES = 1 and BRQ = 0")
	}
	if got := u.MainRead8(0x00); got != 0x01 {
		t.Fatalf("main control readback = %#04x, want 0x01", got)
	}
	u.MainWrite8(0x00, 0x03) // RES = 1, BRQ = 1: bus request holds it
	if !u.SubHalted() {
		t.Fatal("sub CPU not halted under bus request")
	}
	if got := u.MainRead8(0x00); got != 0x03 {
		t.Fatalf("main control readback = %#04x, want 0x03 (bit1 = bus acknowledge)", got)
	}
	u.MainWrite8(0x00, 0x02) // BRQ alone (RES = 0) also holds it
	if !u.SubHalted() {
		t.Fatal("sub CPU not halted with BRQ = 1, RES = 0")
	}
	if got := u.MainRead8(0x00); got != 0x02 {
		t.Fatalf("main control readback = %#04x, want 0x02", got)
	}
}

// writeCDDCmd writes one CDD command through the sub register
// page: code byte plus BCD MSF, with a correct checksum nibble in
// TC9 (whose write executes the command).
func writeCDDCmd(u *Unit, code uint8, m, s, f int) {
	nib := [10]uint8{
		code >> 4, code & 0x0F,
		uint8(m / 10), uint8(m % 10),
		uint8(s / 10), uint8(s % 10),
		uint8(f / 10), uint8(f % 10),
		0, 0,
	}
	sum := 0
	for i := 0; i < 9; i++ {
		sum += int(nib[i])
	}
	nib[9] = uint8((^sum) & 0x0F)
	for i := 0; i < 10; i++ {
		u.Write8(0xFF8042+uint32(i), nib[i])
	}
}

// readRS reads the 10 CDD status nibbles through the register page.
func readRS(u *Unit) [10]uint8 {
	var rs [10]uint8
	for i := range rs {
		rs[i] = u.Read8(0xFF8038 + uint32(i))
	}
	return rs
}

func TestCDDUnitRoutingAndInterrupts(t *testing.T) {
	ip := []byte{0xDE, 0xAD}
	disc := openISO(t, ip)
	u := NewUnit(disc)

	// A NOP command through the nibble registers: after one 75 Hz
	// tick the RS packet is the power-on status (stopped, 0x09)
	// with a valid checksum.
	writeCDDCmd(u, 0x00, 0, 0, 0)
	u.AdvanceCycles(200000)
	rs := readRS(u)
	if rs[0] != 0x09 {
		t.Fatalf("RS0 = %#04x, want 0x09 (stopped)", rs[0])
	}
	sum := 0
	for _, n := range rs {
		sum += int(n)
	}
	if sum%16 != 0x0F {
		t.Fatalf("RS packet checksum: nibble sum mod 16 = %#x, want 0xF", sum%16)
	}
	if !u.cddEngine().StatusValid() {
		t.Fatal("statusValid not set after packet delivery")
	}
	// HOCK hands the exchange back.
	u.Write8(0xFF8037, 0x04)
	if u.cddEngine().StatusValid() {
		t.Fatal("statusValid still set after HOCK write")
	}

	// The CDD interrupt is mask gated: with register 0x03 bit1
	// clear the completed command does not deliver, with it set
	// it delivers exactly once.
	writeCDDCmd(u, 0x00, 0, 0, 0)
	u.AdvanceCycles(200000)
	if u.TakeCDDInterrupt() {
		t.Fatal("CDD interrupt delivered with the mask bit clear")
	}
	u.Write8(0xFF8003, 0x02)
	if !u.TakeCDDInterrupt() {
		t.Fatal("CDD interrupt not delivered after unmasking")
	}
	if u.TakeCDDInterrupt() {
		t.Fatal("CDD interrupt delivered twice")
	}

	// The CDC latches still live behind the RS read view: write a
	// latch, confirm reads show nibbles (not latch bytes), then
	// run the transfer on the latch values.
	u.Write8(0xFF8003, 0x0C) // CDC IRQ mask on, destination PRG-RAM
	u.Write32(0xFF8038, 1)   // LBA latch = 1
	u.Write32(0xFF803E, 0x00030000)
	u.Write16(0xFF803C, 1)
	if got := u.Read8(0xFF8038); got > 0x0F {
		t.Fatalf("read of 0xFF8038 = %#04x, want a status nibble (<= 0x0F), not a latch byte", got)
	}
	u.Write8(0xFF8036, 0x01) // start
	st := pollCDC(t, u)
	if st&0x08 != 0 {
		t.Fatalf("CDC status %#04x has the error bit set", st)
	}
	if got := u.PRGRAM()[0x30000-0x20000]; got != 0xDE {
		t.Fatalf("PRGRAM IP byte = %#04x, want 0xDE (latch survived behind the RS view)", got)
	}
	// Transfer completion latched the CDC interrupt (mask was on).
	if !u.TakeCDCInterrupt() {
		t.Fatal("CDC interrupt not delivered after completion with mask on")
	}
	if u.TakeCDCInterrupt() {
		t.Fatal("CDC interrupt delivered twice")
	}

	// With the CDC mask clear, a completed transfer delivers
	// nothing.
	u2 := NewUnit(disc)
	u2.Write8(0xFF8003, 0x08) // destination PRG-RAM, mask off
	u2.Write32(0xFF8038, 1)
	u2.Write32(0xFF803E, 0x00030000)
	u2.Write16(0xFF803C, 1)
	u2.Write8(0xFF8036, 0x01)
	pollCDC(t, u2)
	if u2.TakeCDCInterrupt() {
		t.Fatal("CDC interrupt delivered with the mask bit clear")
	}
}

// drivePCMUnit programs PCM channel 0 of a unit through the sub
// register page with a 256 byte square period in wave RAM bank 0.
func drivePCMUnit(u *Unit) {
	for i := 0; i < 128; i++ {
		u.Write8(0xFF1000+uint32(i), 0xFE) // +126
	}
	for i := 128; i < 256; i++ {
		u.Write8(0xFF1000+uint32(i), 0x7E) // -126
	}
	for i := 256; i < 288; i++ {
		u.Write8(0xFF1000+uint32(i), 0xFF) // loop markers
	}
	u.Write8(0xFF0007, 0xC0) // CTRL: chip on, MOD = 1, channel 0
	u.Write8(0xFF0000, 0xFF) // ENV
	u.Write8(0xFF0001, 0xFF) // PAN
	u.Write8(0xFF0002, 0x00) // FD.L
	u.Write8(0xFF0003, 0x08) // FD.H: FD = 0x0800
	u.Write8(0xFF0006, 0x00) // ST: start at 0 (retrigger)
	u.Write8(0xFF0008, 0x01) // ON/OFF: channel 0 on
}

func TestUnitRenderAudioAndContinuation(t *testing.T) {
	u := NewUnit(nil)
	drivePCMUnit(u)
	a := u.RenderAudio(4410)
	nonzero := 0
	for _, s := range a {
		if s != 0 {
			nonzero++
		}
	}
	if nonzero < len(a)/2 {
		t.Fatalf("only %d of %d rendered samples nonzero, want the PCM square audible", nonzero, len(a))
	}

	// An identically driven unit renders identically.
	u2 := NewUnit(nil)
	drivePCMUnit(u2)
	b := u2.RenderAudio(4410)
	if !bytes.Equal(int16sLE(a), int16sLE(b)) {
		t.Fatal("two identically driven units rendered different audio")
	}

	// Save mid-playback, restore, and the continuation matches
	// the unrestored unit frame for frame.
	buf := make([]byte, u.SerializeSize())
	if err := u.Serialize(buf); err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	u3 := NewUnit(nil)
	if err := u3.Deserialize(buf); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	cont := u.RenderAudio(4410)
	contR := u3.RenderAudio(4410)
	if !bytes.Equal(int16sLE(cont), int16sLE(contR)) {
		t.Fatal("PCM continuation audio after restore differs")
	}
}

func int16sLE(in []int16) []byte {
	out := make([]byte, 2*len(in))
	for i, v := range in {
		out[2*i] = byte(v)
		out[2*i+1] = byte(v >> 8)
	}
	return out
}

func TestSerializeSCD2Extension(t *testing.T) {
	u := NewUnit(nil)
	// Bank 3 (bits 7:6) and WP field 5 (bits 4:2) in one main
	// write of the memory mode register: 0xC0 | 0x14 = 0xD4.
	u.MainWrite8(0x02, 0xD4)
	buf := make([]byte, u.SerializeSize())
	if err := u.Serialize(buf); err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	u2 := NewUnit(nil)
	if err := u2.Deserialize(buf); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if got := u2.PRGBank(); got != 3 {
		t.Fatalf("restored PRGBank = %d, want 3", got)
	}
	if got := u2.PRGProtectBytes(); got != 5*0x10000 {
		t.Fatalf("restored PRGProtectBytes = %#x, want %#x", got, 5*0x10000)
	}
	buf2 := make([]byte, u2.SerializeSize())
	if err := u2.Serialize(buf2); err != nil {
		t.Fatalf("re-Serialize: %v", err)
	}
	if !bytes.Equal(buf, buf2) {
		t.Fatal("SCD-2 extension did not round trip byte for byte")
	}
	// The unit serialized size is unchanged from SCD-1.
	if got, want := u.SerializeSize(), 0x80000+0x40000+0x10000+2048+256; got != want {
		t.Fatalf("SerializeSize = %d, want %d", got, want)
	}
}
