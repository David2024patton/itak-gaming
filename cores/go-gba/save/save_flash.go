package save

import (
	"github.com/davidpatton/itak-gaming/cores/go-gba/cart"
)

// Flash window accesses and the command protocol,
// following GBATEK's description of the flash save
// chips.
//
// Command entry always starts with the unlock pair:
// byte 0xAA to window offset 0x5555, then 0x55 to offset
// 0x2AAA. The next byte, written to offset 0x5555, is the
// command:
//
//	0x90  enter ID mode: reads at offsets 0 and 1 return
//	      the manufacturer and device bytes (the pairs
//	      are named in flashID below)
//	0xF0  leave ID mode
//	0xA0  byte program: the very next window write stores
//	      one byte. Flash cells only clear bits, so the
//	      stored byte is the old byte AND the written one;
//	      only an erase sets bits again (to 1, byte 0xFF).
//	0x80  erase setup: a second unlock pair follows, then
//	      0x10 to offset 0x5555 erases the whole part, or
//	      0x30 to any address in a 4 KB sector erases that
//	      sector.
//	0xB0  bank switch (128 KB part only): the next write,
//	      to offset 0, carries the bank number in bit 0.
//
// Hardware programs and erases over milliseconds and
// games poll the byte being written (the documented
// data polling on bit 7). This core completes every
// operation at the write that triggers it, so a polling
// read always sees the final byte immediately; the
// simplification is stated here and in the README.

// FlashRead serves one read from the flash window. The
// window is byte wide: the byte is repeated into every
// lane of the result, exactly as the SRAM window behaves.
func (d *Device) FlashRead(addr uint32, size int) uint32 {
	off := addr & 0xFFFF
	var b byte
	if d.fIDMode && off <= 1 {
		b = d.flashID(off)
	} else {
		b = d.flash[d.fBank*Flash64Size+int(off)]
	}
	w := uint32(b) * 0x01010101
	switch size {
	case 1:
		return w & 0xFF
	case 2:
		return w & 0xFFFF
	}
	return w
}

// flashID returns the manufacturer (offset 0) or device
// (offset 1) byte for the emulated part: the Sanyo
// LE26FV10N1TS pair (0x62, 0x13) for the 128 KB part,
// the Macronix pair (0xC2, 0x1C) for the 64 KB parts.
func (d *Device) flashID(off uint32) byte {
	if d.kind == cart.SaveFlash1M {
		if off == 0 {
			return 0x62
		}
		return 0x13
	}
	if off == 0 {
		return 0xC2
	}
	return 0x1C
}

// FlashWrite serves one write to the flash window. Only
// the low byte of the value reaches the chip.
func (d *Device) FlashWrite(addr uint32, val uint32) {
	off := addr & 0xFFFF
	b := byte(val)

	// A bank switch write is the byte right after the
	// 0xB0 command, to offset 0.
	if d.fBankSw {
		d.fBankSw = false
		if off == 0 && d.kind == cart.SaveFlash1M {
			d.fBank = int(b & 1)
		}
		return
	}
	// A program write is the byte right after the 0xA0
	// command, to any address.
	if d.fProg {
		d.fProg = false
		cell := d.fBank*Flash64Size + int(off)
		d.flash[cell] &= b // cells only clear bits
		return
	}
	// ID mode ends on a bare 0xF0 write to 0x5555 as well
	// as on the unlocked command; several drivers exit
	// without the full unlock pair.
	if d.fIDMode && d.fStage == 0 && off == 0x5555 && b == 0xF0 {
		d.fIDMode = false
		return
	}

	switch d.fStage {
	case 0: // idle: watch for the unlock
		if off == 0x5555 && b == 0xAA {
			d.fStage = 1
		}
	case 1: // unlock half one seen
		if off == 0x2AAA && b == 0x55 {
			d.fStage = 2
		} else {
			d.fStage = 0
		}
	case 2: // command byte, to offset 0x5555
		d.fStage = 0
		if off != 0x5555 {
			return
		}
		switch b {
		case 0x90:
			d.fIDMode = true
		case 0xF0:
			d.fIDMode = false
		case 0xA0:
			d.fProg = true
		case 0x80:
			d.fStage = 3
		case 0xB0:
			if d.kind == cart.SaveFlash1M {
				d.fBankSw = true
			}
		}
	case 3: // erase setup: second unlock, half one
		if off == 0x5555 && b == 0xAA {
			d.fStage = 4
		} else {
			d.fStage = 0
		}
	case 4: // erase setup: second unlock, half two
		if off == 0x2AAA && b == 0x55 {
			d.fStage = 5
		} else {
			d.fStage = 0
		}
	case 5: // erase target
		d.fStage = 0
		switch {
		case b == 0x10 && off == 0x5555:
			for i := 0; i < d.flashSize(); i++ {
				d.flash[i] = 0xFF
			}
		case b == 0x30:
			base := d.fBank*Flash64Size + int(off&^0xFFF)
			for i := 0; i < 0x1000; i++ {
				d.flash[base+i] = 0xFF
			}
		}
	}
}
