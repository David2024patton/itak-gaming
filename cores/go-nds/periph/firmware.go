package periph

// FirmwareSize is the size of the generated firmware flash
// image the ARM7 SPI firmware device serves.
const FirmwareSize = 512 << 10

// SettingsOffset is the documented offset of the first user
// settings block in the firmware flash. A duplicate block sits
// 0x100 bytes later.
const SettingsOffset = 0x3FE00

// buildFirmware generates the minimal firmware image in code.
// It is NOT a dump of any real firmware: the image is filled
// with the erased value (0xFF) and only the two user settings
// blocks are populated, in the documented format, with a
// zeroed identity (empty nickname and message, default
// birthday and color) and a default touchscreen calibration
// matching the ADC map in spi.go. The settings block carries
// its CRC16 at offset 0x70 computed over bytes [0:0x70] with
// the reflected polynomial 0xA001 and initial value 0xFFFF,
// the CRC family of the cartridge header CRC in the cart
// package; that placement is this module's own internally
// consistent choice.
func buildFirmware() []byte {
	fw := make([]byte, FirmwareSize)
	for i := range fw {
		fw[i] = 0xFF
	}
	writeSettings(fw[SettingsOffset : SettingsOffset+0x100])
	writeSettings(fw[SettingsOffset+0x100 : SettingsOffset+0x200])
	return fw
}

func writeSettings(b []byte) {
	for i := range b {
		b[i] = 0
	}
	b[0] = 5 // settings version
	b[1] = 0 // favorite color
	b[2] = 1 // birthday month
	b[3] = 1 // birthday day
	// Nickname (UTF-16 at 0x04, length byte at 0x1A) and the
	// message area stay zeroed: empty.
	putU16(b, 0x58, 0x0200) // touch calibration: ADC X1
	putU16(b, 0x5A, 0x0200) // ADC Y1
	b[0x5C] = 0             // screen X1
	b[0x5D] = 0             // screen Y1
	putU16(b, 0x5E, 0x0E00) // ADC X2
	putU16(b, 0x60, 0x0E00) // ADC Y2
	b[0x62] = 254           // screen X2
	b[0x63] = 190           // screen Y2
	putU16(b, 0x70, crc16Settings(b[:0x70]))
}

func putU16(b []byte, off int, v uint16) {
	b[off] = byte(v)
	b[off+1] = byte(v >> 8)
}

func crc16Settings(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, c := range data {
		crc ^= uint16(c)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}
