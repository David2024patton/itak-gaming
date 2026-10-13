// Package smpc models the Sega Saturn SMPC (System Manager and
// Peripheral Control): the command processor behind the 128 byte
// register page owned by the bus, a battery backed real time clock,
// four bytes of battery backed SMPC memory (SMEM), and the INTBACK
// status and peripheral reports for two standard digital pads.
//
// Register page layout: the page indices are the documented offsets
// within the SMPC window (bus base 0x00100000), so IREG0 is page
// offset 0x01, COMREG is 0x1F, OREG0 is 0x21, OREG31 is 0x5F, SR is
// 0x61 and SF is 0x63. Only odd offsets carry data on the bus; the
// even bytes of the page are unused storage here.
//
// Command timing model: Poll is called once per scanline. A command
// is pending when COMREG differs from the last polled value, or
// when software has set SF in the page again for a repeat of the
// same command code. A pending command executes instantly inside
// that Poll, SF reads 1 in the page for the rest of that Poll, and
// SF is cleared at the next Poll. That one Poll of SF latency is
// the whole timing model; the microsecond execution times in the
// manual are not modelled.
//
// INTBACK model: the manual splits INTBACK into a status report
// and peripheral data, gated independently. IREG0 low bits select
// the status report (00H means no status, 01H means status), IREG1
// bit 3 (PEN) enables peripheral data, IREG0 bits 7 to 4 select the
// per port data modes, and IREG2 is written F0H. Continuation on
// real hardware is requested by toggling IREG0 bit 7 and a break
// by setting IREG0 bit 6; in this Poll driven model a continuation
// is a fresh INTBACK command issued while a report sequence is
// still pending, and IREG0 bit 6 on such a call aborts the
// sequence. Reports are emitted one chunk per INTBACK call: the
// status chunk first when selected, then one chunk per port. SR
// carries the manual continuation bits (PDE in the status phase,
// NPE and PDL in the peripheral phase) and OREG31 carries the
// command echo 10H at the end of a sequence and F0H while chunks
// remain; the manual leaves OREG31 during peripheral data
// explicitly overwritable, and F0H is the remaining marker chosen
// for this core.
//
// Simplifications, stated plainly:
//
//   - PDR1 and PDR2 (page offsets 0x75 and 0x77) stay plain storage
//     bytes. Driving them needs the SH-2 direct port mode and the
//     INTBACK port mode setup, which are out of scope here; pads
//     are read through INTBACK reports and SetPad.
//   - SYSRES only records a reset request (see ResetRequested).
//     The system owns the actual machine reset.
//   - The reset button is never pressed in this core, so the SR
//     RESB bit and the status report reset data always read 0.
//   - The cartridge code always reads 0 (no cartridge). The area
//     code is fixed at 04H, North America, from the manual table.
//   - SETTIME takes the weekday from the date itself. The manual
//     leaves an inconsistent weekday undefined; deriving it keeps
//     the clock self consistent as Advance rolls the days.
//   - The power on clock is 1994-01-01 00:00:00, a Saturday. That
//     is one second past the manual cold reset value of Friday
//     1993-12-31 23:59:59, stated here so the default is pinned.
package smpc

import (
	"encoding/binary"
	"fmt"
)

// Register page offsets. The page is the slice returned by
// bus.SMPCRegisters; indices match the documented SMPC window
// offsets.
const (
	OffIREG0  = 0x01
	OffIREG1  = 0x03
	OffIREG2  = 0x05
	OffIREG3  = 0x07
	OffIREG4  = 0x09
	OffIREG5  = 0x0B
	OffIREG6  = 0x0D
	OffCOMREG = 0x1F
	OffOREG0  = 0x21
	OffOREG31 = 0x5F
	OffSR     = 0x61
	OffSF     = 0x63
	OffPDR1   = 0x75
	OffPDR2   = 0x77
)

// oregOff returns the page offset of OREG n.
func oregOff(n int) int { return OffOREG0 + 2*n }

// Command codes, from the SMPC command list in the manual.
const (
	CmdMSHON    = 0x00
	CmdSSHON    = 0x02
	CmdSSHOFF   = 0x03
	CmdSNDON    = 0x06
	CmdSNDOFF   = 0x07
	CmdCDON     = 0x08
	CmdCDOFF    = 0x09
	CmdSYSRES   = 0x0D
	CmdCKCHG352 = 0x0E
	CmdCKCHG320 = 0x0F
	CmdINTBACK  = 0x10
	CmdSETTIME  = 0x16
	CmdSETSMEM  = 0x17
	CmdNMIREQ   = 0x18
	CmdRESENAB  = 0x19
	CmdRESDISA  = 0x1A
)

// AreaNorthAmerica is the INTBACK area code this machine reports:
// 04H in the manual area code table (United States, Canada and
// the Latin America NTSC region).
const AreaNorthAmerica = 0x04

// Pad button bits, in the system PadState order used by SetPad.
// The values match the system package button constants.
const (
	BtnUp    = 1 << 0
	BtnDown  = 1 << 1
	BtnLeft  = 1 << 2
	BtnRight = 1 << 3
	BtnA     = 1 << 4
	BtnB     = 1 << 5
	BtnC     = 1 << 6
	BtnX     = 1 << 7
	BtnY     = 1 << 8
	BtnZ     = 1 << 9
	BtnL     = 1 << 10
	BtnR     = 1 << 11
	BtnStart = 1 << 12
)

// ClockCyclesPerSecond is the machine cycle rate the RTC advance
// counts against: 28.6364 MHz.
const ClockCyclesPerSecond = 28636400

// Hooks carries the machine level effects of the reset system
// commands out of this package. Any hook left nil is skipped, so a
// partially wired system still runs every other command.
type Hooks struct {
	OnSSHON  func() // release the slave SH-2 from reset
	OnSSHOFF func() // hold and reset the slave SH-2
	OnNMIReq func() // raise NMI to the master SH-2
	OnSNDON  func() // release the SCSP 68EC000 from reset
	OnSNDOFF func() // hold the SCSP 68EC000 in reset
	OnCDOn   func() // CD block power on
	OnCDOff  func() // CD block power off
}

// Report sequence stages for a pending INTBACK sequence.
const (
	stageIdle      = 0 // no sequence pending
	stagePort0     = 1 // port 0 chunk is next
	stagePort1     = 2 // port 1 chunk is next
	stagePort1Only = 3 // port 1 chunk is next, port 0 was skipped
)

// SMPC is the command processor view over the bus SMPC page.
type SMPC struct {
	page  []byte
	hooks Hooks

	lastPolled byte // COMREG value seen at the previous Poll
	sfBusy     bool // SF was set by the executing Poll, clear next Poll

	clock352      bool
	resetEnabled  bool
	resetReq      bool
	timeSet       bool // STE: SETTIME ran since power on
	sndOn         bool
	cdOn          bool
	unknown       int
	portModes     byte // SR low bits: P2MD and P1MD of the last INTBACK
	stage         int  // pending INTBACK report stage
	statusPhaseSR bool // SR shows the status phase layout

	days   int64 // RTC day count, days since 1970-01-01
	secDay int   // RTC seconds into the day, 0 to 86399
	cycRem int   // sub second cycle remainder for Advance
	smem   [4]byte
	pads   [2]uint16
}

// New wraps the 128 byte bus SMPC page. The clock starts at the
// power on value 1994-01-01 00:00:00, the clock mode at 320, and
// the reset button NMI path enabled, matching the boot ROM
// defaults the manual describes.
func New(page []byte, hooks Hooks) *SMPC {
	s := &SMPC{page: page, hooks: hooks, resetEnabled: true}
	s.days = daysFromCivil(1994, 1, 1)
	s.lastPolled = page[OffCOMREG]
	s.syncSR()
	return s
}

// Poll runs the per scanline command check described in the
// package comment.
func (s *SMPC) Poll() {
	if s.sfBusy {
		s.page[OffSF] = 0
		s.sfBusy = false
		s.lastPolled = s.page[OffCOMREG]
		s.syncSR()
		return
	}
	cur := s.page[OffCOMREG]
	repeat := s.page[OffSF]&1 != 0
	if cur == s.lastPolled && !repeat {
		s.syncSR()
		return
	}
	s.lastPolled = cur
	s.execute(cur)
	s.page[OffSF] = 1
	s.sfBusy = true
	s.syncSR()
}

// execute runs one command code instantly.
func (s *SMPC) execute(cmd byte) {
	// Every command echoes its code into OREG31 when processing
	// begins. INTBACK may overwrite OREG31 with its remaining
	// marker while a report sequence is pending.
	s.page[OffOREG31] = cmd
	switch cmd {
	case CmdMSHON:
		// Master SH-2 on: no state change in this core. The
		// master is the CPU issuing the command.
	case CmdSSHON:
		if s.hooks.OnSSHON != nil {
			s.hooks.OnSSHON()
		}
	case CmdSSHOFF:
		if s.hooks.OnSSHOFF != nil {
			s.hooks.OnSSHOFF()
		}
	case CmdSNDON:
		s.sndOn = true
		if s.hooks.OnSNDON != nil {
			s.hooks.OnSNDON()
		}
	case CmdSNDOFF:
		s.sndOn = false
		if s.hooks.OnSNDOFF != nil {
			s.hooks.OnSNDOFF()
		}
	case CmdCDON:
		s.cdOn = true
		if s.hooks.OnCDOn != nil {
			s.hooks.OnCDOn()
		}
	case CmdCDOFF:
		s.cdOn = false
		if s.hooks.OnCDOff != nil {
			s.hooks.OnCDOff()
		}
	case CmdSYSRES:
		s.resetReq = true
	case CmdCKCHG352:
		s.clock352 = true
	case CmdCKCHG320:
		s.clock352 = false
	case CmdINTBACK:
		s.intback()
	case CmdSETTIME:
		s.setTime()
	case CmdSETSMEM:
		s.smem[0] = s.page[OffIREG0]
		s.smem[1] = s.page[OffIREG0+2]
		s.smem[2] = s.page[OffIREG0+4]
		s.smem[3] = s.page[OffIREG0+6]
	case CmdNMIREQ:
		if s.hooks.OnNMIReq != nil {
			s.hooks.OnNMIReq()
		}
	case CmdRESENAB:
		s.resetEnabled = true
	case CmdRESDISA:
		s.resetEnabled = false
	default:
		s.unknown++
	}
}

// ResetRequested reports whether SYSRES has asked for a whole
// system reset. The flag latches; the system owns the reset and
// any clearing policy.
func (s *SMPC) ResetRequested() bool { return s.resetReq }

// ClockMode returns the current clock mode: 352 or 320.
func (s *SMPC) ClockMode() int {
	if s.clock352 {
		return 352
	}
	return 320
}

// UnknownCommands returns how many unrecognised command codes
// have been executed, for diagnostics.
func (s *SMPC) UnknownCommands() int { return s.unknown }

// syncSR rewrites the SR page byte from the current phase state.
// In the peripheral phase SR follows the manual peripheral
// control layout: bit 7 set, PDL, NPE, RESB (always 0 here) and
// the two port mode fields. In the status phase SR follows the
// manual status layout: bit 6 set, PDE for remaining peripheral
// data, and RESB. Outside an INTBACK sequence the peripheral
// layout reads with NPE clear.
func (s *SMPC) syncSR() {
	if s.statusPhaseSR {
		v := byte(0x40)
		if s.stage != stageIdle {
			v |= 0x20 // PDE: peripheral data remaining
		}
		s.page[OffSR] = v
		return
	}
	v := byte(0x80) | s.portModes
	if s.stage != stageIdle {
		v |= 0x20 // NPE: peripheral data remaining
	}
	if s.stage == stagePort1 || s.stage == stagePort1Only {
		v |= 0x40 // PDL: the peripheral data present is the first port group
	}
	s.page[OffSR] = v
}

// intback runs one INTBACK call: the next chunk of the pending
// report sequence, or the start of a new sequence.
func (s *SMPC) intback() {
	ireg0 := s.page[OffIREG0]
	ireg1 := s.page[OffIREG1]
	if s.stage != stageIdle {
		if ireg0&0x40 != 0 {
			// Break request: drop the rest of the sequence.
			s.stage = stageIdle
			s.statusPhaseSR = false
			s.page[OffOREG31] = CmdINTBACK
			return
		}
		s.emitPortChunk()
		return
	}
	// New sequence. Record the port modes for SR: IREG0 holds
	// P2MD in bits 7 to 6 and P1MD in bits 5 to 4; SR holds
	// P2MD in bits 3 to 2 and P1MD in bits 1 to 0.
	s.portModes = (ireg0 >> 4) & 0x0F
	wantStatus := ireg0&0x0F != 0
	pen := ireg1&0x08 != 0
	// Build the port queue, skipping ports in 0 byte mode.
	var ports []int
	if pen {
		if (ireg0>>4)&0x03 != 0x03 {
			ports = append(ports, 0)
		}
		if (ireg0>>6)&0x03 != 0x03 {
			ports = append(ports, 1)
		}
	}
	switch {
	case wantStatus:
		s.writeStatusReport()
		s.statusPhaseSR = true
		s.stage = stageForPorts(ports)
		if s.stage == stageIdle {
			s.page[OffOREG31] = CmdINTBACK
		} else {
			s.page[OffOREG31] = 0xF0
		}
	case len(ports) > 0:
		s.statusPhaseSR = false
		s.stage = stageForPorts(ports[1:])
		s.emitPortData(ports[0])
		if s.stage == stageIdle {
			s.page[OffOREG31] = CmdINTBACK
		} else {
			s.page[OffOREG31] = 0xF0
		}
	default:
		s.statusPhaseSR = false
		s.page[OffOREG31] = CmdINTBACK
	}
}

// stageForPorts maps the ports still to emit onto a stage value.
func stageForPorts(ports []int) int {
	if len(ports) == 0 {
		return stageIdle
	}
	if ports[0] == 0 {
		return stagePort0
	}
	return stagePort1Only
}

// emitPortChunk emits the chunk for the port named by the stage
// and advances the sequence.
func (s *SMPC) emitPortChunk() {
	s.statusPhaseSR = false
	switch s.stage {
	case stagePort0:
		s.emitPortData(0)
		s.stage = stagePort1
		s.page[OffOREG31] = 0xF0
	case stagePort1, stagePort1Only:
		s.emitPortData(1)
		s.stage = stageIdle
		s.page[OffOREG31] = CmdINTBACK
	}
}

// writeStatusReport writes the 16 byte status report into OREG0
// to OREG15. The layout follows the manual INTBACK result table:
// OREG0 status flags, OREG1 to OREG7 the RTC, OREG8 the
// cartridge code, OREG9 the area code, OREG10 and OREG11 the
// system status words, and OREG12 to OREG15 the SMEM bytes.
func (s *SMPC) writeStatusReport() {
	y, mo, d, wd, hh, mm, ss := s.rtcFields()
	var st byte
	if s.timeSet {
		st |= 0x80 // STE
	}
	if !s.resetEnabled {
		st |= 0x40 // RESD
	}
	s.page[oregOff(0)] = st
	s.page[oregOff(1)] = toBCD(y / 100)
	s.page[oregOff(2)] = toBCD(y % 100)
	// Weekday in the high nibble, month in the low nibble. Both
	// are plain hex values in the manual, not BCD: Sunday is 0
	// and December is 0x0C.
	s.page[oregOff(3)] = byte(wd<<4 | mo)
	s.page[oregOff(4)] = toBCD(d)
	s.page[oregOff(5)] = toBCD(hh)
	s.page[oregOff(6)] = toBCD(mm)
	s.page[oregOff(7)] = toBCD(ss)
	s.page[oregOff(8)] = 0 // cartridge code: no cartridge
	s.page[oregOff(9)] = AreaNorthAmerica
	// System status 1: fixed bits 5, 4 and 2 read 1, DOTSEL in
	// bit 6 follows the clock mode, and SNDRES in bit 0 reads 1
	// while the sound CPU is held in reset (sound off). The
	// MSHNMI and SYSRES signal bits read 0 in this core.
	var ss1 byte = 0x34
	if s.clock352 {
		ss1 |= 0x40
	}
	if !s.sndOn {
		ss1 |= 0x01
	}
	s.page[oregOff(10)] = ss1
	// System status 2: CDRES in bit 6 reads 1 while the CD block
	// is held in reset (CD off).
	var ss2 byte
	if !s.cdOn {
		ss2 |= 0x40
	}
	s.page[oregOff(11)] = ss2
	for i := 0; i < 4; i++ {
		s.page[oregOff(12+i)] = s.smem[i]
	}
}

// emitPortData writes one port report chunk at OREG0: the port
// status byte, the Saturn peripheral ID byte, and the pad data
// bytes. The port status for a directly connected pad is F1H:
// multitap ID F in the high nibble and one connector in the low
// nibble. The peripheral ID for the standard digital device is
// 02H: peripheral type 0 in the high nibble and a data size of 2
// in the low nibble. The pad data follows the manual Saturn
// digital device format and is active low: a pressed button
// reads 0. Data byte 1 is Right, Left, Down, Up, Start, A, C, B
// in bits 7 to 0. Data byte 2 is R, X, Y, Z, L in bits 7 to 3
// with the three extended bits reading 1 (unpressed).
func (s *SMPC) emitPortData(port int) {
	m := s.pads[port]
	s.page[oregOff(0)] = 0xF1
	s.page[oregOff(1)] = 0x02
	var d1 byte = 0xFF
	if m&BtnRight != 0 {
		d1 &^= 1 << 7
	}
	if m&BtnLeft != 0 {
		d1 &^= 1 << 6
	}
	if m&BtnDown != 0 {
		d1 &^= 1 << 5
	}
	if m&BtnUp != 0 {
		d1 &^= 1 << 4
	}
	if m&BtnStart != 0 {
		d1 &^= 1 << 3
	}
	if m&BtnA != 0 {
		d1 &^= 1 << 2
	}
	if m&BtnC != 0 {
		d1 &^= 1 << 1
	}
	if m&BtnB != 0 {
		d1 &^= 1 << 0
	}
	s.page[oregOff(2)] = d1
	var d2 byte = 0xFF
	if m&BtnR != 0 {
		d2 &^= 1 << 7
	}
	if m&BtnX != 0 {
		d2 &^= 1 << 6
	}
	if m&BtnY != 0 {
		d2 &^= 1 << 5
	}
	if m&BtnZ != 0 {
		d2 &^= 1 << 4
	}
	if m&BtnL != 0 {
		d2 &^= 1 << 3
	}
	s.page[oregOff(3)] = d2
}

// SetPad stores the button mask for a port: 0 or 1. Other port
// numbers are ignored. Button bits use the PadState order in the
// Btn constants.
func (s *SMPC) SetPad(port int, buttons uint16) {
	if port == 0 || port == 1 {
		s.pads[port] = buttons
	}
}

// setTime applies a SETTIME command. IREG0 and IREG1 are the year
// as two BCD bytes (century digits, then year digits). IREG2
// packs the weekday in its high nibble and the month in its low
// nibble, both plain hex. IREG3 to IREG6 are day, hour, minute
// and second in BCD. The supplied weekday is not trusted: the
// clock derives the weekday from the date. An out of range field
// makes the command a no-op, where the manual leaves the result
// undefined.
func (s *SMPC) setTime() {
	y := fromBCD(s.page[OffIREG0])*100 + fromBCD(s.page[OffIREG0+2])
	packed := s.page[OffIREG0+4]
	mo := int(packed & 0x0F)
	d := fromBCD(s.page[OffIREG0+6])
	hh := fromBCD(s.page[OffIREG0+8])
	mm := fromBCD(s.page[OffIREG0+10])
	ss := fromBCD(s.page[OffIREG0+12])
	if mo < 1 || mo > 12 || d < 1 || d > 31 || hh > 23 || mm > 59 || ss > 59 {
		return
	}
	s.days = daysFromCivil(y, mo, d)
	s.secDay = hh*3600 + mm*60 + ss
	s.cycRem = 0
	s.timeSet = true
}

// Advance moves the RTC forward by whole machine cycles. Cycles
// accumulate into a sub second remainder so repeated small calls
// still track real time. Negative counts are ignored.
func (s *SMPC) Advance(cycles int) {
	if cycles <= 0 {
		return
	}
	s.cycRem += cycles
	secs := s.cycRem / ClockCyclesPerSecond
	s.cycRem %= ClockCyclesPerSecond
	if secs == 0 {
		return
	}
	s.secDay += secs
	for s.secDay >= 86400 {
		s.secDay -= 86400
		s.days++
	}
}

// rtcFields derives the calendar fields from the day count and
// the seconds into the day. Weekday uses the manual numbering:
// Sunday 0 through Saturday 6. 1970-01-01 was a Thursday, which
// anchors the weekday arithmetic.
func (s *SMPC) rtcFields() (y, mo, d, wd, hh, mm, ss int) {
	y, mo, d = civilFromDays(s.days)
	wd = int(((s.days+4)%7 + 7) % 7)
	hh = s.secDay / 3600
	mm = s.secDay % 3600 / 60
	ss = s.secDay % 60
	return y, mo, d, wd, hh, mm, ss
}

// toBCD converts a value in the range 0 to 99 to BCD.
func toBCD(v int) byte {
	return byte((v/10)<<4 | (v % 10))
}

// fromBCD converts a BCD byte back to an integer.
func fromBCD(b byte) int {
	return int(b>>4)*10 + int(b&0x0F)
}

// daysFromCivil returns the day count since 1970-01-01 for a
// Gregorian date, using the standard civil calendar arithmetic.
func daysFromCivil(y, m, d int) int64 {
	if m <= 2 {
		y--
	}
	era := floorDiv(y, 400)
	yoe := y - era*400
	mp := m - 3
	if m <= 2 {
		mp = m + 9
	}
	doy := (153*mp+2)/5 + d - 1
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return int64(era)*146097 + int64(doe) - 719468
}

// civilFromDays is the inverse of daysFromCivil.
func civilFromDays(z int64) (y, m, d int) {
	z += 719468
	era := floorDiv64(z, 146097)
	doe := int(z - era*146097)
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365
	yy := yoe + int(era)*400
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	d = doy - (153*mp+2)/5 + 1
	if mp < 10 {
		m = mp + 3
	} else {
		m = mp - 9
	}
	y = yy
	if m <= 2 {
		y = yy + 1
	}
	return y, m, d
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func floorDiv64(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// Serialized layout, SerializeSize bytes, big endian:
//
//	byte 0: version (1)
//	byte 1: flags: bit 0 reset enabled, bit 1 time set (STE),
//	  bit 2 clock 352 mode, bit 3 reset requested,
//	  bit 4 sound on, bit 5 CD on, bit 6 status phase SR,
//	  bit 7 SF busy
//	bytes 2 to 5: RTC day count since 1970-01-01
//	bytes 6 to 9: RTC seconds into the day
//	bytes 10 to 13: sub second cycle remainder
//	bytes 14 to 17: unknown command count
//	bytes 18 to 19: port 0 pad mask
//	bytes 20 to 21: port 1 pad mask
//	bytes 22 to 25: SMEM bytes
//	byte 26: last executed command code
//	byte 27: report sequence stage
//	byte 28: last polled COMREG value
//	byte 29: SR port mode bits
//	bytes 30 to 31: zero
const stateVersion = 1

// SerializeSize is the exact size of a serialized SMPC.
const SerializeSize = 32

// Serialize writes the SMPC internal state in the layout above.
// The register page itself belongs to the bus and is saved by the
// system state, not duplicated here.
func (s *SMPC) Serialize() []byte {
	out := make([]byte, SerializeSize)
	out[0] = stateVersion
	if s.resetEnabled {
		out[1] |= 1 << 0
	}
	if s.timeSet {
		out[1] |= 1 << 1
	}
	if s.clock352 {
		out[1] |= 1 << 2
	}
	if s.resetReq {
		out[1] |= 1 << 3
	}
	if s.sndOn {
		out[1] |= 1 << 4
	}
	if s.cdOn {
		out[1] |= 1 << 5
	}
	if s.statusPhaseSR {
		out[1] |= 1 << 6
	}
	if s.sfBusy {
		out[1] |= 1 << 7
	}
	binary.BigEndian.PutUint32(out[2:6], uint32(int32(s.days)))
	binary.BigEndian.PutUint32(out[6:10], uint32(s.secDay))
	binary.BigEndian.PutUint32(out[10:14], uint32(s.cycRem))
	binary.BigEndian.PutUint32(out[14:18], uint32(s.unknown))
	binary.BigEndian.PutUint16(out[18:20], s.pads[0])
	binary.BigEndian.PutUint16(out[20:22], s.pads[1])
	copy(out[22:26], s.smem[:])
	out[26] = s.page[OffOREG31]
	if s.stage != stageIdle {
		out[26] = s.lastPolled
	}
	out[27] = byte(s.stage)
	out[28] = s.lastPolled
	out[29] = s.portModes
	return out
}

// Deserialize restores a state produced by Serialize. After a
// restore the poll tracker resyncs to the page COMREG value, so
// a state saved mid sequence does not re-execute its command.
func (s *SMPC) Deserialize(data []byte) error {
	if len(data) != SerializeSize {
		return fmt.Errorf("smpc: state size %d, want %d", len(data), SerializeSize)
	}
	if data[0] != stateVersion {
		return fmt.Errorf("smpc: state version %d, want %d", data[0], stateVersion)
	}
	s.resetEnabled = data[1]&(1<<0) != 0
	s.timeSet = data[1]&(1<<1) != 0
	s.clock352 = data[1]&(1<<2) != 0
	s.resetReq = data[1]&(1<<3) != 0
	s.sndOn = data[1]&(1<<4) != 0
	s.cdOn = data[1]&(1<<5) != 0
	s.statusPhaseSR = data[1]&(1<<6) != 0
	s.sfBusy = data[1]&(1<<7) != 0
	s.days = int64(int32(binary.BigEndian.Uint32(data[2:6])))
	s.secDay = int(binary.BigEndian.Uint32(data[6:10]))
	s.cycRem = int(binary.BigEndian.Uint32(data[10:14]))
	s.unknown = int(binary.BigEndian.Uint32(data[14:18]))
	s.pads[0] = binary.BigEndian.Uint16(data[18:20])
	s.pads[1] = binary.BigEndian.Uint16(data[20:22])
	copy(s.smem[:], data[22:26])
	s.stage = int(data[27])
	s.portModes = data[29]
	s.lastPolled = s.page[OffCOMREG]
	s.syncSR()
	return nil
}
