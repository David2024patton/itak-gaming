package cart

import "encoding/binary"

// Cartridge save devices for the go-n64 core, written
// from the published N64 documentation (n64brew Joybus
// Protocol for the EEPROM commands, and the published
// PI domain 2 / FlashRAM command documentation).
//
// N64 cartridges do not declare their save device in
// the ROM header, so this core has no database: the
// host selects the device with SaveStore.Configure
// (through the system API or the libretro core option).
// The default is SaveNone.

// SaveType identifies the cartridge save device.
type SaveType int

const (
	SaveNone SaveType = iota
	SaveEEPROM4K
	SaveEEPROM16K
	SaveSRAM
	SaveFlash
)

func (t SaveType) String() string {
	switch t {
	case SaveEEPROM4K:
		return "EEPROM 4K"
	case SaveEEPROM16K:
		return "EEPROM 16K"
	case SaveSRAM:
		return "SRAM"
	case SaveFlash:
		return "Flash"
	}
	return "None"
}

// EEPROM and SRAM/Flash sizes in bytes.
const (
	EEPROM4KSize  = 512
	EEPROM16KSize = 2048
	SRAMSize      = 32 << 10
	FlashSize     = 128 << 10
)

// SaveStore holds the cartridge save device contents.
// The EEPROM is reached through the PIF cartridge
// channel (Joybus commands 0x04/0x05); SRAM and Flash
// sit in the PI's second domain at physical 0x08000000.
type SaveStore struct {
	Type SaveType

	EEPROM []byte // 512 or 2048 bytes for the EEPROM types
	SRAM   []byte // 32768 bytes for SaveSRAM
	Flash  *FlashRAM
}

// NewSaveStore builds an empty store of the given type.
// Fresh EEPROM and SRAM contents read as 0xFF (the
// erased state of the real chips); Flash contents are
// built by NewFlashRAM.
func NewSaveStore(t SaveType) *SaveStore {
	s := &SaveStore{Type: t}
	switch t {
	case SaveEEPROM4K:
		s.EEPROM = filled(EEPROM4KSize, 0xFF)
	case SaveEEPROM16K:
		s.EEPROM = filled(EEPROM16KSize, 0xFF)
	case SaveSRAM:
		s.SRAM = filled(SRAMSize, 0xFF)
	case SaveFlash:
		s.Flash = NewFlashRAM()
	}
	return s
}

func filled(n int, v byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = v
	}
	return b
}

// Configure switches the store to a new device type,
// discarding the previous device's contents.
func (s *SaveStore) Configure(t SaveType) {
	ns := NewSaveStore(t)
	*s = *ns
}

// Data returns the active device's contents for the
// libretro SAVE_RAM interface and host persistence, or
// nil when no device is fitted.
func (s *SaveStore) Data() []byte {
	switch s.Type {
	case SaveEEPROM4K, SaveEEPROM16K:
		return s.EEPROM
	case SaveSRAM:
		return s.SRAM
	case SaveFlash:
		if s.Flash != nil {
			return s.Flash.Data[:]
		}
	}
	return nil
}

// SetData replaces the active device's contents from a
// host-provided save image (libretro SAVE_RAM writes by
// the frontend land here byte for byte; a short image
// leaves the tail untouched).
func (s *SaveStore) SetData(b []byte) {
	d := s.Data()
	copy(d, b)
}

// EEPROMInfoID returns the Joybus info identifier of
// the fitted EEPROM (0x0080 for 4 Kbit, 0x00C0 for
// 16 Kbit) and whether an EEPROM is fitted at all.
func (s *SaveStore) EEPROMInfoID() (uint16, bool) {
	switch s.Type {
	case SaveEEPROM4K:
		return 0x0080, true
	case SaveEEPROM16K:
		return 0x00C0, true
	}
	return 0, false
}

// EEPROMRead reads one 8-byte EEPROM block. The block
// number wraps like the real chips: on a 4 Kbit device
// the top two block bits are ignored.
func (s *SaveStore) EEPROMRead(block byte) ([8]byte, bool) {
	var out [8]byte
	if len(s.EEPROM) == 0 {
		return out, false
	}
	n := int(block)
	if len(s.EEPROM) == EEPROM4KSize {
		n &= 63
	}
	copy(out[:], s.EEPROM[n*8:])
	return out, true
}

// EEPROMWrite writes one 8-byte EEPROM block, with the
// same wrapping as EEPROMRead.
func (s *SaveStore) EEPROMWrite(block byte, data [8]byte) bool {
	if len(s.EEPROM) == 0 {
		return false
	}
	n := int(block)
	if len(s.EEPROM) == EEPROM4KSize {
		n &= 63
	}
	copy(s.EEPROM[n*8:], data[:])
	return true
}

// SaveStateSize is the fixed byte size of the save
// device block in a system save state: it always covers
// every device, whatever is fitted.
//
// Layout, all big-endian:
//
//	0: type byte
//	1..2049: EEPROM contents (2048, zero-padded)
//	2049..34817: SRAM contents (32768)
//	34817..165889: Flash data (131072)
//	then the Flash machine state: mode byte, status
//	uint64, erase page uint16, program page uint16,
//	write buffer (128 bytes)
const SaveStateSize = 1 + 2048 + 32768 + 131072 + 1 + 8 + 2 + 2 + 128

// Serialize writes the store into out (SaveStateSize
// bytes).
func (s *SaveStore) Serialize(out []byte) {
	for i := range out[:SaveStateSize] {
		out[i] = 0
	}
	out[0] = byte(s.Type)
	copy(out[1:], s.EEPROM)
	copy(out[1+2048:], s.SRAM)
	f := s.Flash
	if f == nil {
		f = NewFlashRAM()
	}
	copy(out[1+2048+32768:], f.Data[:])
	off := 1 + 2048 + 32768 + 131072
	out[off] = byte(f.Mode)
	binary.BigEndian.PutUint64(out[off+1:], f.Status)
	binary.BigEndian.PutUint16(out[off+9:], f.ErasePage)
	binary.BigEndian.PutUint16(out[off+11:], f.ProgPage)
	copy(out[off+13:], f.Buffer[:])
}

// Deserialize restores the store from a block written
// by Serialize, switching the fitted device type to the
// saved one.
func (s *SaveStore) Deserialize(in []byte) {
	t := SaveType(in[0])
	if t < SaveNone || t > SaveFlash {
		t = SaveNone
	}
	s.Configure(t)
	copy(s.EEPROM, in[1:])
	copy(s.SRAM, in[1+2048:])
	if s.Flash != nil {
		copy(s.Flash.Data[:], in[1+2048+32768:])
		off := 1 + 2048 + 32768 + 131072
		s.Flash.Mode = FlashMode(in[off])
		s.Flash.Status = binary.BigEndian.Uint64(in[off+1:])
		s.Flash.ErasePage = binary.BigEndian.Uint16(in[off+9:])
		s.Flash.ProgPage = binary.BigEndian.Uint16(in[off+11:])
		copy(s.Flash.Buffer[:], in[off+13:])
	}
}

// FlashMode is the FlashRAM command state machine mode.
type FlashMode byte

const (
	FlashModeRead    FlashMode = iota // reads return array data
	FlashModeStatus                   // reads return the status word
	FlashModeErase                    // erase armed (0x78 seen)
	FlashModeProgram                  // program armed (0xB4 seen): window writes fill the page buffer
)

// Flash command bytes (the top byte of a 32-bit write
// to the PI domain 2 window), per the published
// FlashRAM documentation.
const (
	flashCmdSetErasePage   = 0x4B
	flashCmdEraseMode      = 0x78
	flashCmdSetProgramPage = 0xA5
	flashCmdProgramMode    = 0xB4
	flashCmdExecute        = 0xD2
	flashCmdStatusMode     = 0xE1
	flashCmdReadMode       = 0xF0
)

// Flash status words (read as a 64-bit big-endian value
// in status mode), per the published documentation for
// the 1 Mbit device.
const (
	flashStatusIdle    = 0x1111800400C2001D
	flashStatusErase   = 0x1111800800C2001D
	flashStatusProgram = 0x1111800400C2001D
	flashStatusMode    = 0x1111800100C2001D
	flashStatusRead    = 0x11118004F000001D
)

// FlashPageSize is the FlashRAM program/erase page:
// 128 bytes, 1024 pages in the 128 KB array.
const FlashPageSize = 128

// FlashRAM models the 128 KB Flash save chip behind
// its command protocol: commands latch a page and a
// mode, program data collects in a 128-byte page
// buffer, and the execute command applies the erase or
// the program.
type FlashRAM struct {
	Data   [FlashSize]byte
	Mode   FlashMode
	Status uint64

	ErasePage uint16 // latched by command 0x4B (low 10 bits)
	ProgPage  uint16 // latched by command 0xA5 (low 10 bits)
	Buffer    [FlashPageSize]byte
}

// NewFlashRAM returns a Flash chip in its erased
// (all 0xFF) power-on state.
func NewFlashRAM() *FlashRAM {
	f := &FlashRAM{Status: flashStatusIdle}
	for i := range f.Data {
		f.Data[i] = 0xFF
	}
	return f
}

// LoadByte reads one byte of the window at off. In
// status mode the byte comes from the 64-bit status
// word, cycling every 8 bytes; otherwise it is array
// data (offsets wrap in the 128 KB window).
func (f *FlashRAM) LoadByte(off uint32) byte {
	if f.Mode == FlashModeStatus {
		shift := 56 - 8*(off&7)
		return byte(f.Status >> shift)
	}
	return f.Data[off&(FlashSize-1)]
}

// Read composes a big-endian value of size bytes.
func (f *FlashRAM) Read(off uint32, size int) uint64 {
	var v uint64
	for i := 0; i < size; i++ {
		v = v<<8 | uint64(f.LoadByte(off+uint32(i)))
	}
	return v
}

// Command applies a 32-bit command write: the top byte
// is the command, the low bits carry its argument.
// Returns true when the value was a command.
func (f *FlashRAM) Command(val uint32) bool {
	switch byte(val >> 24) {
	case flashCmdSetErasePage:
		f.ErasePage = uint16(val & 0x3FF)
	case flashCmdEraseMode:
		f.Mode = FlashModeErase
		f.Status = flashStatusErase
	case flashCmdSetProgramPage:
		f.ProgPage = uint16(val & 0x3FF)
		f.Status = flashStatusProgram
	case flashCmdProgramMode:
		f.Mode = FlashModeProgram
		for i := range f.Buffer {
			f.Buffer[i] = 0xFF
		}
	case flashCmdExecute:
		switch f.Mode {
		case FlashModeErase:
			base := int(f.ErasePage) * FlashPageSize
			for i := 0; i < FlashPageSize; i++ {
				f.Data[base+i] = 0xFF
			}
		case FlashModeProgram:
			base := int(f.ProgPage) * FlashPageSize
			copy(f.Data[base:base+FlashPageSize], f.Buffer[:])
		}
		f.Mode = FlashModeStatus
	case flashCmdStatusMode:
		f.Mode = FlashModeStatus
		f.Status = flashStatusMode
	case flashCmdReadMode:
		f.Mode = FlashModeRead
		f.Status = flashStatusRead
	default:
		return false
	}
	return true
}

// StoreByte stores one byte of a window write. In
// program mode the byte lands in the page buffer at
// its offset within the latched program page; other
// modes ignore data writes.
func (f *FlashRAM) StoreByte(off uint32, v byte) {
	if f.Mode != FlashModeProgram {
		return
	}
	pageBase := uint32(f.ProgPage) * FlashPageSize
	if off >= pageBase && off < pageBase+FlashPageSize {
		f.Buffer[off-pageBase] = v
	}
}

// Write handles a bus write of size bytes. A 32-bit
// write whose top byte is a command byte is a command;
// anything else is data for the page buffer.
func (f *FlashRAM) Write(off uint32, size int, val uint64) {
	if size == 4 && f.Command(uint32(val)) {
		return
	}
	for i := 0; i < size; i++ {
		f.StoreByte(off+uint32(i), byte(val>>(8*uint(size-1-i))))
	}
}

// DMARead copies length bytes from the window at off
// into dst (a PI DMA read from the device).
func (f *FlashRAM) DMARead(dst []byte, off uint32) {
	for i := range dst {
		dst[i] = f.LoadByte(off + uint32(i))
	}
}

// DMAWrite feeds length bytes from src into the window
// at off (a PI DMA write to the device): in program
// mode they land in the page buffer; commands never
// arrive by DMA, so no command decoding happens here.
func (f *FlashRAM) DMAWrite(src []byte, off uint32) {
	for i, v := range src {
		f.StoreByte(off+uint32(i), v)
	}
}
