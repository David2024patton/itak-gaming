package smpc

import "testing"

// newTestSMPC builds an SMPC over a fresh zeroed 128 byte page.
func newTestSMPC(hooks Hooks) (*SMPC, []byte) {
	page := make([]byte, 128)
	return New(page, hooks), page
}

// issueCmd writes cmd to COMREG and runs the two Polls of the
// command timing model: the executing Poll leaves SF reading 1,
// the next Poll clears it to 0.
func issueCmd(t *testing.T, s *SMPC, page []byte, cmd byte) {
	t.Helper()
	page[OffCOMREG] = cmd
	s.Poll()
	if got := page[OffSF]; got != 1 {
		t.Fatalf("cmd %02X: SF after executing Poll = %d, want 1", cmd, got)
	}
	s.Poll()
	if got := page[OffSF]; got != 0 {
		t.Fatalf("cmd %02X: SF after next Poll = %d, want 0", cmd, got)
	}
}

// repeatCmd reissues the command code already sitting in COMREG
// by setting SF again, the repeat path of the Poll model, and
// runs the same two Polls.
func repeatCmd(t *testing.T, s *SMPC, page []byte) {
	t.Helper()
	page[OffSF] = 1
	s.Poll()
	if got := page[OffSF]; got != 1 {
		t.Fatalf("repeat: SF after executing Poll = %d, want 1", got)
	}
	s.Poll()
	if got := page[OffSF]; got != 0 {
		t.Fatalf("repeat: SF after next Poll = %d, want 0", got)
	}
}

// setIREGs loads the seven input registers.
func setIREGs(page []byte, v [7]byte) {
	for i, b := range v {
		page[OffIREG0+2*i] = b
	}
}

// intbackStatus runs a status only INTBACK (IREG0 01H, PEN off,
// IREG2 F0H) and leaves the status report in the OREG block.
func intbackStatus(t *testing.T, s *SMPC, page []byte) {
	t.Helper()
	setIREGs(page, [7]byte{0x01, 0x00, 0xF0, 0, 0, 0, 0})
	issueCmd(t, s, page, CmdINTBACK)
}

func oreg(page []byte, n int) byte { return page[oregOff(n)] }

// setLeapTime loads SETTIME for 2024-02-29 and issues it. The
// date is a Thursday, weekday 4 in the manual numbering with
// Sunday as 0, so IREG2 packs weekday 4 and month 2 as 42H.
func setLeapTime(t *testing.T, s *SMPC, page []byte, hh, mm, ss byte) {
	t.Helper()
	setIREGs(page, [7]byte{0x20, 0x24, 0x42, 0x29, hh, mm, ss})
	issueCmd(t, s, page, CmdSETTIME)
}

type hookCounts struct {
	sshon, sshoff, nmi, sndon, sndoff, cdon, cdoff int
}

func (c *hookCounts) hooks() Hooks {
	return Hooks{
		OnSSHON:  func() { c.sshon++ },
		OnSSHOFF: func() { c.sshoff++ },
		OnNMIReq: func() { c.nmi++ },
		OnSNDON:  func() { c.sndon++ },
		OnSNDOFF: func() { c.sndoff++ },
		OnCDOn:   func() { c.cdon++ },
		OnCDOff:  func() { c.cdoff++ },
	}
}

func (c *hookCounts) total() int {
	return c.sshon + c.sshoff + c.nmi + c.sndon + c.sndoff + c.cdon + c.cdoff
}

// TestHookCommandsFireOnce covers task item (a): each hook
// command fires its hook exactly once per COMREG write, SF reads
// 1 after the executing Poll and 0 after the next.
func TestHookCommandsFireOnce(t *testing.T) {
	cases := []struct {
		name string
		cmd  byte
		pick func(*hookCounts) int
	}{
		{"SSHON", CmdSSHON, func(c *hookCounts) int { return c.sshon }},
		{"SSHOFF", CmdSSHOFF, func(c *hookCounts) int { return c.sshoff }},
		{"SNDON", CmdSNDON, func(c *hookCounts) int { return c.sndon }},
		{"SNDOFF", CmdSNDOFF, func(c *hookCounts) int { return c.sndoff }},
		{"CDON", CmdCDON, func(c *hookCounts) int { return c.cdon }},
		{"CDOFF", CmdCDOFF, func(c *hookCounts) int { return c.cdoff }},
		{"NMIREQ", CmdNMIREQ, func(c *hookCounts) int { return c.nmi }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counts := &hookCounts{}
			s, page := newTestSMPC(counts.hooks())
			page[OffCOMREG] = tc.cmd
			s.Poll()
			if got := page[OffSF]; got != 1 {
				t.Fatalf("SF after executing Poll = %d, want 1", got)
			}
			if got := tc.pick(counts); got != 1 {
				t.Fatalf("hook fired %d times, want 1", got)
			}
			if got := counts.total(); got != 1 {
				t.Fatalf("total hook firings = %d, want 1 (no other hook may fire)", got)
			}
			s.Poll()
			if got := page[OffSF]; got != 0 {
				t.Fatalf("SF after next Poll = %d, want 0", got)
			}
			s.Poll()
			if got := tc.pick(counts); got != 1 {
				t.Fatalf("hook refired on idle Polls: %d firings, want 1", got)
			}
		})
	}
}

// TestSetTimeThenIntbackStatus covers task item (b): SETTIME
// then an INTBACK status report returns the exact date bytes at
// the manual offsets. 2024-02-29 23:59:30, a Thursday.
func TestSetTimeThenIntbackStatus(t *testing.T) {
	s, page := newTestSMPC(Hooks{})
	setLeapTime(t, s, page, 0x23, 0x59, 0x30)
	intbackStatus(t, s, page)
	want := map[int]byte{
		0:  0x80,                               // STE set, RESD clear (reset enabled)
		1:  0x20,                               // year century digits, BCD
		2:  0x24,                               // year low digits, BCD
		3:  0x42,                               // weekday 4 (Thursday) high nibble, month 2 low nibble, plain hex
		4:  0x29,                               // day, BCD
		5:  0x23,                               // hour, BCD
		6:  0x59,                               // minute, BCD
		7:  0x30,                               // second, BCD
		8:  0x00,                               // cartridge code: none
		9:  0x04,                               // area code: North America
		10: 0x35,                               // system status 1: fixed bits plus SNDRES (sound held in reset)
		11: 0x40,                               // system status 2: CDRES (CD block held in reset)
		12: 0x00, 13: 0x00, 14: 0x00, 15: 0x00, // SMEM, never set
	}
	for n, w := range want {
		if got := oreg(page, n); got != w {
			t.Errorf("OREG%d = %02X, want %02X", n, got, w)
		}
	}
	if got := page[OffOREG31]; got != CmdINTBACK {
		t.Errorf("OREG31 = %02X, want %02X (INTBACK echo at end of sequence)", got, CmdINTBACK)
	}
	if got := page[OffSR]; got != 0x40 {
		t.Errorf("SR = %02X, want 40 (status phase, nothing pending)", got)
	}
}

// TestAdvanceRollsMinuteAndDay covers task item (c): advancing
// 61 seconds of machine cycles from 23:59:30 rolls the minute,
// the hour, the day, the weekday and the month, verified through
// a second INTBACK status report.
func TestAdvanceRollsMinuteAndDay(t *testing.T) {
	s, page := newTestSMPC(Hooks{})
	setLeapTime(t, s, page, 0x23, 0x59, 0x30)
	intbackStatus(t, s, page)
	s.Advance(ClockCyclesPerSecond * 61)
	// COMREG still holds INTBACK; reissue through the SF repeat
	// path for a fresh status report.
	repeatCmd(t, s, page)
	want := map[int]byte{
		1: 0x20, 2: 0x24, // still 2024
		3: 0x53, // weekday 5 (Friday), month 3
		4: 0x01, // day 1
		5: 0x00, // hour 0
		6: 0x00, // minute 0: the 61st second crossed 00:00
		7: 0x31, // second 31
	}
	for n, w := range want {
		if got := oreg(page, n); got != w {
			t.Errorf("after Advance: OREG%d = %02X, want %02X", n, got, w)
		}
	}
}

// TestPadReports covers task item (d): two pads report in the
// manual peripheral chunk layout across the continuation call,
// and OREG31 reaches the manual end value 10H. Pad data is
// active low: a pressed button reads 0.
func TestPadReports(t *testing.T) {
	s, page := newTestSMPC(Hooks{})
	s.SetPad(0, BtnStart|BtnA|BtnR)
	s.SetPad(1, BtnB|BtnL)
	// Peripheral only INTBACK: IREG0 00H (no status, both port
	// modes 0 in bits 7 to 4), IREG1 PEN set, IREG2 F0H.
	setIREGs(page, [7]byte{0x00, 0x08, 0xF0, 0, 0, 0, 0})
	issueCmd(t, s, page, CmdINTBACK)
	// First chunk is port 0.
	if got := oreg(page, 0); got != 0xF1 {
		t.Errorf("port 0 status byte = %02X, want F1 (multitap ID F, 1 connector)", got)
	}
	if got := oreg(page, 1); got != 0x02 {
		t.Errorf("port 0 peripheral ID = %02X, want 02 (type 0, data size 2)", got)
	}
	// Data byte 1: Start (bit 3) and A (bit 2) pressed read 0.
	if got := oreg(page, 2); got != 0xF3 {
		t.Errorf("port 0 data byte 1 = %02X, want F3", got)
	}
	// Data byte 2: R (bit 7) pressed reads 0.
	if got := oreg(page, 3); got != 0x7F {
		t.Errorf("port 0 data byte 2 = %02X, want 7F", got)
	}
	if got := page[OffOREG31]; got != 0xF0 {
		t.Errorf("OREG31 mid sequence = %02X, want F0 (chunks remain)", got)
	}
	if got := page[OffSR]; got != 0xE0 {
		t.Errorf("SR mid sequence = %02X, want E0 (peripheral phase, NPE and PDL set)", got)
	}
	// Continuation: IREG0 80H is the manual continue request.
	page[OffIREG0] = 0x80
	repeatCmd(t, s, page)
	// Second chunk is port 1.
	if got := oreg(page, 0); got != 0xF1 {
		t.Errorf("port 1 status byte = %02X, want F1", got)
	}
	if got := oreg(page, 1); got != 0x02 {
		t.Errorf("port 1 peripheral ID = %02X, want 02", got)
	}
	// Data byte 1: B (bit 0) pressed reads 0.
	if got := oreg(page, 2); got != 0xFE {
		t.Errorf("port 1 data byte 1 = %02X, want FE", got)
	}
	// Data byte 2: L (bit 3) pressed reads 0.
	if got := oreg(page, 3); got != 0xF7 {
		t.Errorf("port 1 data byte 2 = %02X, want F7", got)
	}
	if got := page[OffOREG31]; got != CmdINTBACK {
		t.Errorf("OREG31 at end = %02X, want %02X (manual end value: command echo)", got, CmdINTBACK)
	}
	if got := page[OffSR]; got != 0x80 {
		t.Errorf("SR at end = %02X, want 80 (peripheral phase, nothing pending)", got)
	}

	// A port in 0 byte mode (mode 3 in IREG0 bits 7 to 4) is
	// skipped: with port 1 in 0 byte mode the sequence ends with
	// the port 0 chunk and OREG31 reaches 10H at once.
	s2, page2 := newTestSMPC(Hooks{})
	s2.SetPad(0, BtnStart|BtnA|BtnR)
	s2.SetPad(1, BtnB|BtnL)
	setIREGs(page2, [7]byte{0xC0, 0x08, 0xF0, 0, 0, 0, 0})
	issueCmd(t, s2, page2, CmdINTBACK)
	if got := oreg(page2, 2); got != 0xF3 {
		t.Errorf("0 byte mode skip: first chunk data byte 1 = %02X, want F3 (port 0)", got)
	}
	if got := page2[OffOREG31]; got != CmdINTBACK {
		t.Errorf("0 byte mode skip: OREG31 = %02X, want %02X (sequence already ended)", got, CmdINTBACK)
	}
}

// TestSerializeRoundTrip covers task item (e): SETSMEM bytes, the
// RTC and the clock mode survive a Serialize/Deserialize round
// trip, verified through an INTBACK status report afterwards.
func TestSerializeRoundTrip(t *testing.T) {
	s, page := newTestSMPC(Hooks{})
	smem := [4]byte{0xA5, 0x3C, 0x7E, 0x19}
	setIREGs(page, [7]byte{smem[0], smem[1], smem[2], smem[3], 0, 0, 0})
	issueCmd(t, s, page, CmdSETSMEM)
	setLeapTime(t, s, page, 0x12, 0x34, 0x56)
	issueCmd(t, s, page, CmdCKCHG352)

	blob := s.Serialize()
	if len(blob) != SerializeSize {
		t.Fatalf("Serialize size = %d, want %d", len(blob), SerializeSize)
	}
	s2, page2 := newTestSMPC(Hooks{})
	if err := s2.Deserialize(blob); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if got := s2.ClockMode(); got != 352 {
		t.Errorf("restored ClockMode = %d, want 352", got)
	}
	intbackStatus(t, s2, page2)
	wantRTC := map[int]byte{
		1: 0x20, 2: 0x24, 3: 0x42, 4: 0x29, 5: 0x12, 6: 0x34, 7: 0x56,
	}
	for n, w := range wantRTC {
		if got := oreg(page2, n); got != w {
			t.Errorf("restored OREG%d = %02X, want %02X", n, got, w)
		}
	}
	for i, w := range smem {
		if got := oreg(page2, 12+i); got != w {
			t.Errorf("restored SMEM byte %d at OREG%d = %02X, want %02X", i, 12+i, got, w)
		}
	}
	if got := oreg(page2, 0); got != 0x80 {
		t.Errorf("restored OREG0 = %02X, want 80 (STE survived)", got)
	}
	if got := oreg(page2, 10); got != 0x75 {
		t.Errorf("restored OREG10 = %02X, want 75 (DOTSEL survived, SNDRES set)", got)
	}
}

// TestClockChange352 covers task item (f): CKCHG352 sets
// ClockMode 352, and the mode shows where the code models it:
// the DOTSEL bit of the INTBACK system status byte OREG10. SR
// itself carries no clock bit in this model; it keeps its
// documented phase layouts.
func TestClockChange352(t *testing.T) {
	s, page := newTestSMPC(Hooks{})
	if got := s.ClockMode(); got != 320 {
		t.Fatalf("power on ClockMode = %d, want 320", got)
	}
	issueCmd(t, s, page, CmdCKCHG352)
	if got := s.ClockMode(); got != 352 {
		t.Fatalf("ClockMode after CKCHG352 = %d, want 352", got)
	}
	if got := page[OffSR]; got != 0x80 {
		t.Errorf("SR after CKCHG352 = %02X, want 80 (peripheral layout, modes 0, no clock bit)", got)
	}
	intbackStatus(t, s, page)
	if got := oreg(page, 10); got != 0x75 {
		t.Errorf("OREG10 = %02X, want 75 (fixed bits, DOTSEL for 352, SNDRES)", got)
	}
	if got := page[OffSR]; got != 0x40 {
		t.Errorf("SR after status INTBACK = %02X, want 40 (status phase layout)", got)
	}
	issueCmd(t, s, page, CmdCKCHG320)
	if got := s.ClockMode(); got != 320 {
		t.Errorf("ClockMode after CKCHG320 = %d, want 320", got)
	}
}

// TestUnknownCommand covers task item (g): an unknown command
// byte increments UnknownCommands, echoes into OREG31 like any
// command, and fires no hook.
func TestUnknownCommand(t *testing.T) {
	counts := &hookCounts{}
	s, page := newTestSMPC(counts.hooks())
	issueCmd(t, s, page, 0x55)
	if got := s.UnknownCommands(); got != 1 {
		t.Errorf("UnknownCommands = %d, want 1", got)
	}
	if got := counts.total(); got != 0 {
		t.Errorf("hook firings for unknown command = %d, want 0", got)
	}
	if got := page[OffOREG31]; got != 0x55 {
		t.Errorf("OREG31 = %02X, want 55 (command echo)", got)
	}
}
