package cd

// Test command (19h) sub-functions and the GetQ
// command (1Dh), per the psx-spx CD-ROM test
// commands and status commands chapters.
//
// This controller reports itself as the published
// version 95h,07h,24h,C1h (24 Jul 1995, vC1), so:
// sub-functions 06h..08h (early-only) are supported,
// 22h..25h (vC1 and up) are supported, and 51h
// (vC2 and up) answers the documented not-available
// error, INT5 with code 10h.

// cmdTest dispatches command 19h on its first
// parameter byte.
func (c *Controller) cmdTest(params []byte) {
	if len(params) < 1 {
		c.errorResp(0x20)
		return
	}
	sub, extra := params[0], params[1:]
	stat := c.statusByte()
	switch {
	case sub == 0x05:
		// Stop SCEx reading and get the counters.
		c.ackResp(c.scexTotal, c.scexSuccess)
	case sub <= 0x04 || (sub >= 0x10 && sub <= 0x1A):
		// Force motor on/off and lens mechanics:
		// all answer INT3(stat).
		switch sub {
		case 0x03:
			c.motorOn = false
		case 0x00, 0x01, 0x02, 0x04, 0x10, 0x17:
			c.motorOn = true
		}
		if sub == 0x04 {
			// Start SCEx reading, counters reset:
			// a licensed data disc model reads one
			// total, one success.
			c.scexTotal, c.scexSuccess = 0, 0
			for _, t := range c.tracks() {
				if t.Mode == TrackData {
					c.scexTotal, c.scexSuccess = 1, 1
				}
			}
		}
		c.ackResp(stat)
	case sub >= 0x06 && sub <= 0x08:
		// Early-PSX balance/gain adjust: returns the
		// old RAM value, modeled as 0.
		c.ackResp(0)
	case sub == 0x20:
		// CDROM BIOS date/version (BCD year, month,
		// day, version). The published vC1 value for
		// LATE-PU-8, 24 Jul 1995.
		c.ackResp(0x95, 0x07, 0x24, 0xC1)
	case sub == 0x21:
		var f uint8
		if c.curLBA == 0 {
			f |= 0x01 // head at POS0
		}
		if c.disc == nil {
			f |= 0x02 // door open
		}
		c.ackResp(f)
	case sub == 0x22:
		// Region ID string; this controller is the
		// North America (U/C) variant, matching the
		// SCEA string its GetID returns.
		c.ackResp([]byte("for U/C")...)
	case sub == 0x23:
		// Chip ID, servo amplifier. The doc lists
		// one string per board generation; this
		// vC1-era choice is CXD1817Q.
		c.ackResp([]byte("CXD1817Q")...)
	case sub == 0x24:
		// Chip ID, signal processor: CXD2510Q.
		c.ackResp([]byte("CXD2510Q")...)
	case sub == 0x25:
		// Chip ID, decoder/FIFO: CXD1815Q.
		c.ackResp([]byte("CXD1815Q")...)
	case sub == 0x50:
		// Send a CX command to the servo/signal
		// hardware; needs at least one byte.
		if len(extra) < 1 {
			c.errorResp(0x20)
			return
		}
		c.ackResp(stat)
	case sub == 0x60:
		// HC05 SUB-CPU RAM read, modeled as zeros.
		if len(extra) < 2 {
			c.errorResp(0x20)
			return
		}
		c.ackResp(0)
	case sub == 0x71:
		// Decoder read one register, modeled as 0.
		if len(extra) < 1 {
			c.errorResp(0x20)
			return
		}
		c.ackResp(0)
	case sub == 0x72:
		if len(extra) < 2 {
			c.errorResp(0x20)
			return
		}
		c.ackResp(stat)
	case sub == 0x73:
		if len(extra) < 2 {
			c.errorResp(0x20)
			return
		}
		n := int(extra[1])
		if n > 12 {
			n = 12
		}
		c.ackResp(make([]byte, n)...)
	case sub == 0x74:
		if len(extra) < 2 {
			c.errorResp(0x20)
			return
		}
		c.ackResp(stat)
	case sub == 0x75:
		// Decoder host transfer info: remain/addr,
		// modeled as zeros.
		c.ackResp(0, 0, 0, 0)
	case sub == 0x76:
		if len(extra) < 4 {
			c.errorResp(0x20)
			return
		}
		c.ackResp(stat)
	default:
		// Documented not-available answer. For the
		// ranges 06h..0Fh, 1Bh..1Fh and 26h..2Fh a
		// nonzero parameter count changes the code
		// from 10h to 20h; the 30h..4Fh prototype
		// range always answers 10h.
		code := uint8(0x10)
		inParamRange := (sub >= 0x06 && sub <= 0x0F) ||
			(sub >= 0x1B && sub <= 0x1F) ||
			(sub >= 0x26 && sub <= 0x2F)
		if inParamRange && len(extra) > 0 {
			code = 0x20
		}
		c.errorResp(code)
	}
}

// cmdGetQ implements GetQ. With no parameters it
// enqueues the subchannel Q block for the CURRENT
// position (the task's GetQ form). With the doc's
// adr,point parameters (ADR=1) it enqueues the
// lead-in TOC block for that point. Either way the
// first response is INT3(stat) and the second,
// delivered after one sector period, is INT2 with
// the 10 data bytes, the 16-bit CRC (big-endian,
// over the 10 data bytes), and the peak LSB byte
// the doc describes (modeled as 0 in lead-in and
// at this position model).
func (c *Controller) cmdGetQ(params []byte) {
	if len(params) == 1 {
		c.errorResp(0x20)
		return
	}
	var data []byte
	if len(params) >= 2 {
		if params[0] != 1 {
			c.errorResp(0x10)
			return
		}
		d, ok := c.tocSubQ(params[1])
		if !ok {
			c.errorResp(0x10)
			return
		}
		data = d
	} else {
		data = c.positionSubQ()
	}
	crc := subqCRC(data)
	resp := append([]byte(nil), data...)
	resp = append(resp, uint8(crc>>8), uint8(crc), 0)
	c.ackResp(c.statusByte())
	c.schedule(c.now+sectorPeriod1x, cdEvent{kind: evSecondResp, resp: resp, irq: irqComplete})
}

// positionSubQ builds the ADR=1 position block:
// control/ADR, track, index, track-relative MSF,
// reserved, absolute MSF (10 bytes, all BCD).
func (c *Controller) positionSubQ() []byte {
	pos := c.headPos()
	ctrl := uint8(0x01) // ADR=1, audio
	trackNum, index := 0xAA, 1
	rel := pos
	if tr, ok := c.trackAt(pos); ok {
		trackNum = tr.Number
		if tr.Mode == TrackData {
			ctrl |= 0x40 // data bit in the control field
		}
		if pos >= tr.DataLBA {
			rel = pos - tr.DataLBA
		} else {
			rel = pos - tr.StartLBA
			index = 0
		}
	}
	rm, rs, rf := SectorToMSF(rel)
	am, as, af := LBAToMSF(pos)
	return []byte{
		ctrl, toBCD(trackNum), toBCD(index),
		toBCD(rm), toBCD(rs), toBCD(rf),
		0,
		toBCD(am), toBCD(as), toBCD(af),
	}
}

// tocSubQ builds the ADR=1 lead-in block for a
// point: control/ADR, track 00h, point, lead-in
// MSF (modeled 00:00:00), reserved, then the
// point's absolute MSF (or the A0h/A1h payloads).
func (c *Controller) tocSubQ(point uint8) ([]byte, bool) {
	ctrl := uint8(0x01)
	if len(c.tracks()) > 0 && c.tracks()[0].Mode == TrackData {
		ctrl |= 0x40
	}
	data := []byte{ctrl, 0, point, 0, 0, 0, 0, 0, 0, 0}
	switch {
	case point == 0xA0:
		trs := c.tracks()
		if len(trs) == 0 {
			return nil, false
		}
		data[7] = toBCD(trs[0].Number)
		data[8] = 0x20 // disc type: CD-ROM XA, as in GetID
	case point == 0xA1:
		trs := c.tracks()
		if len(trs) == 0 {
			return nil, false
		}
		data[7] = toBCD(trs[len(trs)-1].Number)
	case point == 0xA2:
		m, s, f := LBAToMSF(c.totalSectors())
		data[7], data[8], data[9] = toBCD(m), toBCD(s), toBCD(f)
	default:
		if !bcdValid(point) {
			return nil, false
		}
		n := bcdVal(point)
		found := false
		for _, t := range c.tracks() {
			if t.Number == n {
				m, s, f := LBAToMSF(t.DataLBA)
				data[7], data[8], data[9] = toBCD(m), toBCD(s), toBCD(f)
				found = true
			}
		}
		if !found {
			return nil, false
		}
	}
	return data, true
}

// subqCRC computes the subchannel Q CRC per the
// psx-spx adjust_crc_16_ccitt routine: initial
// value zero, run over the data bytes, the result
// inverted and stored big-endian. That routine is
// the CRC-16-CCITT form (polynomial x^16+x^12+x^5+1)
// used for subchannel Q.
func subqCRC(data []byte) uint16 {
	var lsb, msb uint8
	for _, b := range data {
		x := b ^ msb
		x ^= x >> 4
		msb = lsb ^ (x >> 3) ^ (x << 4)
		lsb = x ^ (x << 5)
	}
	return uint16(msb^0xFF)<<8 | uint16(lsb^0xFF)
}
