package save

// The EEPROM serial protocol, following GBATEK. The
// chip sits on the data bus at window 0x0D000000 and
// moves one bit per bus access: a write access clocks
// in the bit in bit 0 of the value, a read access clocks
// out one bit, returned in bit 0 (all other bits read
// as zero here).
//
// A transfer starts with a start bit (1) and a request
// bit: 1 for read, 0 for write.
//
//	Read:  address bits, most significant first, then a
//	       stop bit (0) written by the host; the chip
//	       then outputs 4 dummy bits (0) and the 64 data
//	       bits of the addressed block, most significant
//	       first.
//	Write: address bits then 64 data bits, most
//	       significant first, then a stop bit (0). The
//	       chip commits the block when the stop bit
//	       arrives, then needs a programming interval
//	       during which reads return 0 (busy) before
//	       returning 1 (ready).
//
// The address is 6 bits on the 512 byte part and 14 bits
// on the 8 KB part; a block is 8 bytes either way. The
// cartridge signature does not name the size, so the
// device learns it from the first transfer, the way the
// wire allows: a read's address ends where the host
// switches from writing to reading, and a write commits
// when a read poll ends its stream or when the long
// form's 79th bit (its stop) arrives. One residue is
// stated plainly: if the very first transfer a game
// makes is a write to the 8 KB part whose data happens
// to put a 0 then a 1 across the small part's stop
// position, the stream is indistinguishable from a
// small part write followed by a new start bit, and is
// read the second way. Any earlier read, or a save
// image of known length, pins the size first and the
// ambiguity never arises. Until the size is pinned the
// device reports the 8 KB part.
//
// The write programming interval is also collapsed:
// writes complete at the stop bit, so the first ready
// poll already reads 1.

// EEPROMActive reports whether an access at addr hits
// the EEPROM rather than the ROM behind the window: the
// whole window for ROM images up to 16 MB, only offsets
// 0xFFFF00..0xFFFFFF of the window for larger images.
func (d *Device) EEPROMActive(addr uint32) bool {
	if !d.IsEEPROM() {
		return false
	}
	if d.romSize <= 16*1024*1024 {
		return true
	}
	return addr&0xFFFFFF >= 0xFFFF00
}

// EEPROMRead clocks out one bit.
func (d *Device) EEPROMRead() uint32 {
	switch d.eState {
	case eeReadAddr:
		// The host stopped writing: the address is
		// complete. The last captured bit may be the
		// stop bit; accept both shapes.
		bits := d.eBits
		addr := d.eAddr
		if bits == 7 || bits == 15 {
			bits--
			addr >>= 1
		}
		if bits != 6 && bits != 14 {
			d.eState = eeIdle
			return 1
		}
		d.learnEEPROMSize(bits)
		d.eAddr = addr & uint32(d.EEPROMSize()/8-1)
		base := int(d.eAddr) * 8
		var v uint64
		for i := 0; i < 8; i++ {
			v |= uint64(d.eeprom[base+i]) << (56 - 8*i)
		}
		d.eOut = v
		d.eOutPos = 0
		d.eState = eeReadOut
		return d.eepromOutBit()
	case eeReadOut:
		return d.eepromOutBit()
	case eeWritePend:
		// The host ended the stream with a poll: the
		// 71 bit capture was a complete small part write.
		d.commitWrite(6)
		d.eState = eeIdle
		return 1
	case eeWriteCap:
		// A read mid-capture aborts the transfer.
		d.eState = eeIdle
		return 1
	}
	// Idle, mid-handshake and busy all read back the
	// ready bit: 1. Writes complete instantly in this
	// model, so the busy interval never shows.
	return 1
}

// eepromOutBit serves one bit of a read: 4 dummy zero
// bits, then the 64 data bits, most significant first.
// After the last data bit the stream ends and later
// reads see the idle ready bit.
func (d *Device) eepromOutBit() uint32 {
	pos := d.eOutPos
	d.eOutPos++
	if pos < 4 {
		return 0
	}
	if pos >= 4+64 {
		d.eState = eeIdle
		return 1
	}
	return uint32(d.eOut >> (63 - (pos - 4)) & 1)
}

// EEPROMWrite clocks in one bit.
func (d *Device) EEPROMWrite(val uint32) {
	bit := val & 1
	switch d.eState {
	case eeIdle:
		if bit == 1 {
			d.eState = eeRequest
		}
	case eeRequest:
		if bit == 1 {
			d.eState = eeReadAddr
			d.eBits, d.eAddr = 0, 0
		} else {
			d.eState = eeWriteCap
			d.eCapN, d.eCapHi, d.eCapLo = 0, 0, 0
		}
	case eeReadAddr:
		d.eAddr = d.eAddr<<1 | bit
		d.eBits++
		if d.eBits > 15 {
			d.eState = eeIdle // runaway stream; drop it
		}
	case eeReadOut:
		// A write during readout starts a new transfer
		// if it is a start bit; anything else ends it.
		if bit == 1 {
			d.eState = eeRequest
		} else {
			d.eState = eeIdle
		}
	case eeWritePend:
		if bit == 0 {
			// A 0 cannot be a start bit: the capture is
			// the long part's stream after all. Fold the
			// bit in as data and keep capturing.
			d.capPush(bit)
			d.eState = eeWriteCap
			return
		}
		// A 1 here is read as a fresh start bit after a
		// completed small part write (the header comment
		// states the indistinguishable case plainly).
		d.commitWrite(6)
		d.eState = eeRequest
	case eeWriteCap:
		d.capPush(bit)
		switch {
		case d.eCapN == 71 && bit == 0:
			if d.eepromKnown && !d.eeprom512 {
				// Size pinned to the long part: bit 71 is
				// data, never a stop. Keep capturing.
			} else if d.eepromKnown {
				d.commitWrite(6)
				d.eState = eeIdle
			} else {
				d.eState = eeWritePend
			}
		case d.eCapN == 79:
			if bit == 0 {
				d.commitWrite(14)
			}
			d.eState = eeIdle
		case d.eCapN > 79:
			d.eState = eeIdle
		}
	}
}

// capPush appends one bit to the write capture.
func (d *Device) capPush(bit uint32) {
	d.eCapHi = d.eCapHi<<1 | d.eCapLo>>63
	d.eCapLo = d.eCapLo<<1 | uint64(bit)
	d.eCapN++
}

// commitWrite splits a captured write stream (address
// bits of the given width, then 64 data bits; the stop
// bit that ends the stream is captured last and belongs
// to neither) and stores the block. The block bytes land
// in stream order: the first data byte clocked in is the
// block's first byte.
func (d *Device) commitWrite(addrBits int) {
	addr := uint32(d.captureBits(0, addrBits))
	data := d.captureBits(addrBits, 64)
	d.learnEEPROMSize(addrBits)
	addr &= uint32(d.EEPROMSize()/8 - 1)
	base := int(addr) * 8
	for i := 0; i < 8; i++ {
		d.eeprom[base+i] = byte(data >> (56 - 8*i))
	}
}

// captureBits returns length stream bits starting at
// the given stream position (0 is the first bit clocked
// in) from the capture pair.
func (d *Device) captureBits(start, length int) uint64 {
	total := d.eCapN
	var out uint64
	for i := 0; i < length; i++ {
		// Stream bit (start+i), counted from the head,
		// sits at position total-1-(start+i) from bit 0
		// of the (hi, lo) pair.
		pos := total - 1 - (start + i)
		var b uint64
		if pos >= 64 {
			b = d.eCapHi >> uint(pos-64) & 1
		} else {
			b = d.eCapLo >> uint(pos) & 1
		}
		out = out<<1 | b
	}
	return out
}
