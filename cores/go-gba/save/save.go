// Package save implements the GBA cartridge save devices
// with a bus protocol, as described in GBATEK: the flash
// parts (64 KB and 128 KB, command driven) and the serial
// EEPROMs (512 byte and 8 KB). A Device is created from
// the cartridge's detected save kind; the system installs
// it behind bus hooks for the windows the device owns:
//
//   - Flash occupies the byte wide window at 0x0E000000,
//     the same window SRAM uses, whenever the cartridge
//     carries a flash part.
//   - EEPROM occupies accesses whose top byte is 0x0D,
//     inside what is normally the ROM window. For ROM
//     images of 16 MB or less the whole window belongs to
//     the EEPROM; for larger images GBATEK restricts it
//     to window offsets 0xFFFF00..0xFFFFFF, and other
//     offsets still read the ROM.
//
// Battery SRAM has no protocol; it stays in the bus,
// where GBA-1 put it: a plain byte store behind the same
// 0x0E000000 window, masked to its 32 KB by the system
// when the cartridge kind calls for it.
package save

import (
	"github.com/davidpatton/itak-gaming/cores/go-gba/cart"
)

// Storage sizes in bytes.
const (
	Flash64Size  = 0x10000 // 64 KB flash part
	Flash128Size = 0x20000 // 128 KB flash part (two banks)
	EEPROM512    = 0x200   // 512 byte EEPROM
	EEPROM8K     = 0x2000  // 8 KB EEPROM
	SRAMSize     = 0x8000  // 32 KB battery SRAM
)

// StateSize is the serialized device block: one kind
// byte, the full flash backing, the full EEPROM backing,
// and a fixed region of protocol state.
const StateSize = 1 + Flash128Size + EEPROM8K + 64

// Device is one cartridge's save hardware. The zero
// value is not usable; construct with New.
type Device struct {
	kind    cart.SaveKind
	romSize int

	flash  [Flash128Size]byte // only the part's size is used
	eeprom [EEPROM8K]byte     // only the part's size is used

	// Flash command protocol state.
	fStage  int  // unlock and command stage counter
	fIDMode bool // reads return the ID pair
	fProg   bool // next window write programs one byte
	fBankSw bool // next window write to offset 0 sets the bank
	fBank   int  // current 64 KB bank (128 KB part only)

	// EEPROM size, learned from the first completed
	// transfer's address width (or from a loaded save
	// image's length). Until learned it reports 8 KB.
	eeprom512   bool
	eepromKnown bool

	// EEPROM serial protocol state.
	eState  int    // stream phase, see the ee* constants
	eBits   int    // address bits captured (read requests)
	eAddr   uint32 // read address being served
	eCapN   int    // bits captured (write requests)
	eCapHi  uint64 // captured bits above bit 63
	eCapLo  uint64 // captured bits, low 64
	eOut    uint64 // data block being clocked out
	eOutPos int    // bits clocked out, dummy bits included
}

// EEPROM stream phases.
const (
	eeIdle      = iota // waiting for a start bit
	eeRequest          // waiting for the read/write bit
	eeReadAddr         // capturing read address bits
	eeReadOut          // clocking out dummy then data bits
	eeWriteCap         // capturing address and data bits of a write
	eeWritePend        // 71 bits captured: small-part write or long stream, undecided
)

// New builds the device for a detected save kind. The
// ROM size decides the EEPROM window rule above.
func New(kind cart.SaveKind, romSize int) *Device {
	d := &Device{kind: kind, romSize: romSize}
	d.ResetContents()
	return d
}

// Kind reports the cartridge save kind this device serves.
func (d *Device) Kind() cart.SaveKind { return d.kind }

// ResetContents returns the storage to its power-on
// contents (flash erased to 0xFF, EEPROM zeroed) and the
// protocols to idle. Save data is meant to survive
// console resets, so the system does not call this on a
// machine reset; it exists for construction and tests.
func (d *Device) ResetContents() {
	for i := range d.flash {
		d.flash[i] = 0xFF
	}
	for i := range d.eeprom {
		d.eeprom[i] = 0
	}
	d.ResetProtocol()
}

// ResetProtocol returns both device protocols to idle
// without touching the stored contents.
func (d *Device) ResetProtocol() {
	d.fStage, d.fIDMode, d.fProg, d.fBankSw, d.fBank = 0, false, false, false, 0
	d.eState, d.eBits, d.eAddr = eeIdle, 0, 0
	d.eCapN, d.eCapHi, d.eCapLo = 0, 0, 0
	d.eOut, d.eOutPos = 0, 0
}

// IsFlash reports whether the flash window hook applies.
func (d *Device) IsFlash() bool {
	switch d.kind {
	case cart.SaveFlash, cart.SaveFlash512, cart.SaveFlash1M:
		return true
	}
	return false
}

// IsEEPROM reports whether the EEPROM window hook applies.
func (d *Device) IsEEPROM() bool { return d.kind == cart.SaveEEPROM }

// flashSize is the active flash capacity in bytes.
func (d *Device) flashSize() int {
	if d.kind == cart.SaveFlash1M {
		return Flash128Size
	}
	return Flash64Size
}

// EEPROMSize is the active EEPROM capacity in bytes: the
// 512 byte part once a 6 bit address transfer (or a 512
// byte save image) has identified it, otherwise 8 KB.
func (d *Device) EEPROMSize() int {
	if d.eeprom512 {
		return EEPROM512
	}
	return EEPROM8K
}

// learnEEPROMSize records the part size from an address
// width seen on the wire.
func (d *Device) learnEEPROMSize(addrBits int) {
	d.eepromKnown = true
	d.eeprom512 = addrBits == 6
}

// SaveRAM returns a copy of the device contents in the
// form a frontend persists: the flash bytes for a flash
// cartridge, the EEPROM bytes for an EEPROM cartridge.
// It returns nil for kinds this package does not own
// (the system serves SRAM from the bus store instead).
func (d *Device) SaveRAM() []byte {
	switch {
	case d.IsFlash():
		out := make([]byte, d.flashSize())
		copy(out, d.flash[:d.flashSize()])
		return out
	case d.IsEEPROM():
		out := make([]byte, d.EEPROMSize())
		copy(out, d.eeprom[:d.EEPROMSize()])
		return out
	}
	return nil
}

// SetSaveRAM loads device contents saved earlier. A
// length that matches the other size class of the same
// device family is accepted (a 64 KB image on a 128 KB
// part loads into bank 0 with bank 1 erased; a 512 byte
// EEPROM image marks the part as the small one). Other
// lengths are ignored.
func (d *Device) SetSaveRAM(b []byte) {
	switch {
	case d.IsFlash():
		if len(b) == 0 || len(b) > Flash128Size {
			return
		}
		d.ResetContents()
		copy(d.flash[:], b)
	case d.IsEEPROM():
		if len(b) != EEPROM512 && len(b) != EEPROM8K {
			return
		}
		d.ResetContents()
		copy(d.eeprom[:], b)
		d.eeprom512 = len(b) == EEPROM512
		d.eepromKnown = true
	}
}
