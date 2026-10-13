package periph

// AuxSPI is the cartridge backup memory on the AUX SPI bus,
// modeled as a small serial EEPROM (the 512-byte class used by
// early DS games) behind AUXSPICNT (0x040001A0) and AUXSPIDATA
// (0x040001A2). The protocol follows the documented small
// EEPROM command set: WREN (0x06) sets the write-enable latch,
// WRDI (0x04) clears it, RDSR (0x05) streams the status byte
// (bit 0 write-in-progress, always 0 here since writes are
// instant; bit 1 write-enable latch), READ (0x03) and WRITE
// (0x02) take one address byte (address bit 8 rides in command
// bit 3) and then stream data bytes. A transaction ends when
// AUXSPICNT is written with the chip-select hold bit (bit 6)
// clear, or when a fresh command byte arrives in command
// state; ending a transaction after a write phase clears the
// latch. The backing buffer is part of the machine state and
// is exposed to the system as the cartridge save RAM.
type AuxSPI struct {
	cnt   uint16
	save  [512]byte
	state uint8 // 0 command, 1 address, 2 read data, 3 write data, 4 status
	addr  uint32
	hi    uint32 // address bit 8 from the command byte
	wr    bool   // current transaction writes
	wel   bool
}

func (a *AuxSPI) read(rel, off uint32, size int) uint32 {
	switch rel {
	case 0:
		return getU32(uint32(a.cnt), off, size)
	case 2:
		switch a.state {
		case 2: // read data streams on data reads
			return uint32(a.nextRead())
		case 4: // status streams after RDSR
			return uint32(a.status())
		}
		return 0
	}
	return 0
}

func (a *AuxSPI) status() byte {
	if a.wel {
		return 0x02
	}
	return 0
}

func (a *AuxSPI) nextRead() byte {
	b := a.save[a.addr&0x1FF]
	a.addr++
	return b
}

func (a *AuxSPI) write(rel, off uint32, size int, val uint32) {
	switch rel {
	case 0: // AUXSPICNT
		a.cnt = uint16(putU32(uint32(a.cnt), off, size, val))
		if a.cnt&(1<<6) == 0 {
			a.end()
		}
	case 2: // AUXSPIDATA: one byte per write
		a.byteIn(byte(val))
	}
}

// end closes the current transaction.
func (a *AuxSPI) end() {
	if a.state == 3 {
		a.wel = false
	}
	a.state = 0
}

// byteIn feeds one transmitted byte into the EEPROM protocol.
func (a *AuxSPI) byteIn(b byte) {
	switch a.state {
	case 0: // command byte
		switch {
		case b == 0x06: // WREN
			a.wel = true
		case b == 0x04: // WRDI
			a.wel = false
		case b == 0x05: // RDSR
			a.state = 4
		case b&0x07 == 0x03: // READ (bit 3 = address bit 8)
			a.hi = uint32(b>>3) & 1
			a.wr = false
			a.state = 1
		case b&0x07 == 0x02: // WRITE
			a.hi = uint32(b>>3) & 1
			a.wr = true
			a.state = 1
		}
	case 1: // address byte
		a.addr = a.hi<<8 | uint32(b)
		if a.wr {
			a.state = 3
		} else {
			a.state = 2
		}
	case 3: // write data
		if a.wel {
			a.save[a.addr&0x1FF] = b
		}
		a.addr++
	case 2, 4:
		// Transmitting during a read phase just clocks the
		// stream forward.
		if a.state == 2 {
			a.nextRead()
		}
	}
}
