package periph

// SPI is the ARM7 SPI bus behind SPICNT (0x040001C0) and
// SPIDATA (0x040001C2). Transfers are instant: the busy bit
// always reads 0, each SPIDATA write performs one byte exchange
// with the selected device and latches the response byte,
// which the next SPIDATA read returns. Devices: 0 power
// management (reads 0, writes ignored), 1 firmware flash (the
// generated block from firmware.go; read commands stream it,
// write commands are ignored), 2 touchscreen controller.
// Baud bits are ignored because exchanges are instant. The
// documented transfer-size bit is treated as byte mode only.
// The SPI IRQ (ARM7 bit 24) raises per completed byte when
// SPICNT bit 11 is set.
type SPI struct {
	cnt    uint16
	resp   byte
	fw     []byte // generated firmware image, owned by the Set
	fwAdr  uint32 // 24-bit flash read pointer
	fwFast bool   // fast-read command: a dummy byte precedes data
	fwPh   uint8  // 0 command, 1-3 address, 4 dummy (fast read), 5 data, 6 status
	tscPh  uint8  // 0 control, 1 first data byte, 2 second data byte
	tscADC uint16
}

func (sp *SPI) read(rel, off uint32, size int) uint32 {
	switch rel {
	case 0:
		return getU32(uint32(sp.cnt), off, size)
	case 2:
		return uint32(sp.resp)
	}
	return 0
}

func (sp *SPI) write(rel, off uint32, size int, val uint32, s *Set) {
	switch rel {
	case 0:
		old := sp.cnt
		sp.cnt = uint16(putU32(uint32(sp.cnt), off, size, val))
		if sp.cnt&0x8000 == 0 || (sp.cnt^old)&0x0300 != 0 {
			// Deselecting or switching devices restarts the
			// device protocols.
			sp.fwPh, sp.tscPh = 0, 0
		}
	case 2:
		if sp.cnt&0x8000 == 0 {
			return
		}
		sp.resp = sp.exchange(byte(val), s)
		if sp.cnt&0x0800 != 0 {
			s.Raise(false, IRQSPI)
		}
	}
}

func (sp *SPI) exchange(b byte, s *Set) byte {
	switch (sp.cnt >> 8) & 3 {
	case 1:
		return sp.fwExchange(b)
	case 2:
		return sp.tscExchange(b, s)
	}
	return 0 // power management and the unused device 3
}

// fwExchange runs the firmware flash read protocol: command
// 0x03 (read) or 0x0B (fast read) followed by a 24-bit
// address, then data bytes stream out, wrapping inside the
// flash image. Command 0x05 (read status) returns 0. All
// write-class commands are ignored: the flash is generated,
// never programmed.
func (sp *SPI) fwExchange(b byte) byte {
	switch sp.fwPh {
	case 0:
		switch b {
		case 0x03:
			sp.fwAdr, sp.fwFast, sp.fwPh = 0, false, 1
		case 0x0B:
			sp.fwAdr, sp.fwFast, sp.fwPh = 0, true, 1
		case 0x05:
			sp.fwPh = 6
		}
		return 0
	case 1, 2, 3:
		sp.fwAdr = (sp.fwAdr<<8 | uint32(b)) & 0xFFFFFF
		sp.fwPh++
		if sp.fwPh == 4 {
			if sp.fwFast {
				sp.fwPh = 4 // dummy phase next
			} else {
				sp.fwPh = 5
			}
		}
		return 0
	case 4: // fast-read dummy byte
		sp.fwPh = 5
		return 0
	case 5:
		v := sp.fw[sp.fwAdr%uint32(len(sp.fw))]
		sp.fwAdr = (sp.fwAdr + 1) & 0xFFFFFF
		return v
	case 6:
		return 0 // status: never busy, never write-enabled
	}
	return 0
}

// tscExchange runs the touchscreen controller protocol. The
// first byte after select is the control byte (bit 7 set,
// channel in bits 6-4); the next two byte exchanges return
// the 12-bit ADC value split as (adc>>5) then (adc&0x1F)<<3,
// so adc = ((b1<<8)|b2)>>3. Channel 1 is Y, channel 5 is X.
// With the pen up the ADC reads 0.
func (sp *SPI) tscExchange(b byte, s *Set) byte {
	switch sp.tscPh {
	case 0:
		x, y, down := s.touch()
		sp.tscADC = 0
		if down {
			switch (b >> 4) & 7 {
			case 1:
				sp.tscADC = adcFromCoord(uint32(y), 191)
			case 5:
				sp.tscADC = adcFromCoord(uint32(x), 255)
			}
		}
		sp.tscPh = 1
		return 0
	case 1:
		sp.tscPh = 2
		return byte(sp.tscADC >> 5)
	case 2:
		sp.tscPh = 0
		return byte(sp.tscADC&0x1F) << 3
	}
	return 0
}

// adcFromCoord converts a screen pixel coordinate to the
// 12-bit ADC scale of the generated calibration: screen edge
// low maps to 0x100 and the far edge to 0xF00, a documented
// linear map consistent with the firmware calibration block.
func adcFromCoord(coord, max uint32) uint16 {
	if coord > max {
		coord = max
	}
	return uint16(0x100 + (coord*0xE00)/max)
}
