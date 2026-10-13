package save

import "encoding/binary"

// Serialized layout (StateSize bytes total):
//
//	0          1 byte    the cartridge save kind
//	1          128 KB    the flash backing store
//	1+128 KB   8 KB      the EEPROM backing store
//	then       64 bytes  protocol state:
//	  0  flash stage counter
//	  1  flash ID mode flag
//	  2  flash program armed flag
//	  3  flash bank switch armed flag
//	  4  flash current bank
//	  5  EEPROM size flag (1 = the 512 byte part)
//	  6  EEPROM size known flag
//	  7  EEPROM stream phase
//	  8  EEPROM read address bits captured
//	  9  EEPROM read address, little endian 32
//	  13 EEPROM write capture count, little endian 32
//	  17 EEPROM write capture high bits, little endian 64
//	  25 EEPROM write capture low bits, little endian 64
//	  33 EEPROM readout block, little endian 64
//	  41 EEPROM readout position, little endian 32
//	  45..63 zero
//
// The backing stores are serialized at full size for
// every kind: unused regions hold their power-on fill
// (0xFF flash, zero EEPROM) and cost a fixed block in
// every state, which keeps the layout independent of
// the cartridge in the slot.

// protoOff is the offset of the protocol region.
const protoOff = 1 + Flash128Size + EEPROM8K

// Serialize writes the device block into dst and
// returns the number of bytes used, or 0 if dst is short.
func (d *Device) Serialize(dst []byte) int {
	if len(dst) < StateSize {
		return 0
	}
	dst[0] = byte(d.kind)
	copy(dst[1:1+Flash128Size], d.flash[:])
	copy(dst[1+Flash128Size:protoOff], d.eeprom[:])
	p := dst[protoOff : protoOff+64]
	for i := range p {
		p[i] = 0
	}
	p[0] = byte(d.fStage)
	p[1] = boolByte(d.fIDMode)
	p[2] = boolByte(d.fProg)
	p[3] = boolByte(d.fBankSw)
	p[4] = byte(d.fBank)
	p[5] = boolByte(d.eeprom512)
	p[6] = boolByte(d.eepromKnown)
	p[7] = byte(d.eState)
	p[8] = byte(d.eBits)
	binary.LittleEndian.PutUint32(p[9:13], d.eAddr)
	binary.LittleEndian.PutUint32(p[13:17], uint32(d.eCapN))
	binary.LittleEndian.PutUint64(p[17:25], d.eCapHi)
	binary.LittleEndian.PutUint64(p[25:33], d.eCapLo)
	binary.LittleEndian.PutUint64(p[33:41], d.eOut)
	binary.LittleEndian.PutUint32(p[41:45], uint32(d.eOutPos))
	return StateSize
}

// Deserialize restores a block written by Serialize. It
// reports false when the input is short or names a
// different save kind than this device serves.
func (d *Device) Deserialize(src []byte) bool {
	if len(src) < StateSize {
		return false
	}
	if src[0] != byte(d.kind) {
		return false
	}
	copy(d.flash[:], src[1:1+Flash128Size])
	copy(d.eeprom[:], src[1+Flash128Size:protoOff])
	p := src[protoOff : protoOff+64]
	d.fStage = int(p[0])
	d.fIDMode = p[1] != 0
	d.fProg = p[2] != 0
	d.fBankSw = p[3] != 0
	d.fBank = int(p[4])
	d.eeprom512 = p[5] != 0
	d.eepromKnown = p[6] != 0
	d.eState = int(p[7])
	d.eBits = int(p[8])
	d.eAddr = binary.LittleEndian.Uint32(p[9:13])
	d.eCapN = int(binary.LittleEndian.Uint32(p[13:17]))
	d.eCapHi = binary.LittleEndian.Uint64(p[17:25])
	d.eCapLo = binary.LittleEndian.Uint64(p[25:33])
	d.eOut = binary.LittleEndian.Uint64(p[33:41])
	d.eOutPos = int(binary.LittleEndian.Uint32(p[41:45]))
	return true
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}
