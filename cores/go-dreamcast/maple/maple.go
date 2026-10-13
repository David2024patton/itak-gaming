// Package maple implements the Dreamcast Maple bus host: the DMA
// table walker and register block at physical 0x005F6C00, one
// controller per port, and one VMU storage device per port on the
// first sub device address, per the official Sega Dev.Box System
// Architecture document (worked examples in section 2.6.8 and the
// Maple register section 8.4.1.2) and the public KallistiOS maple
// headers.
//
// Each of the four ports always answers as a controller at its
// main device address and as a VMU at its first sub device
// address. The VMU holds 256 blocks of 512 bytes (128 KB total).
//
// Simplifications chosen for this implementation, stated plainly:
//
//   - Transfers complete synchronously inside the register write
//     or vblank call that starts them, so SB_MDST never reads
//     back busy.
//   - SB_MSYS powers on as 0x3A980000 (timeout count 0x3A98 in
//     bits 31:16, every other field zero) and stores later writes
//     verbatim. The rate and delay fields are stored only and
//     have no timing effect.
//   - SB_MDAPRO stores writes verbatim without checking the
//     documented security code 0x6155 in bits 31:16. It and
//     SB_MSHTCL are write only and read back zero.
//   - Header pattern values other than zero are treated as
//     pattern zero, the only implemented pattern.
//   - Counter choice: SB_MTXDAD holds the address of the entry
//     being processed while the walk runs and the address just
//     past the last entry when the walk ends. SB_MRXDAD and
//     SB_MRXDBD both hold the receive address of the last entry
//     processed.
//   - All Status replies carry the 28 word device info followed
//     by two condition words: the controller condition words for
//     a controller, two zero words for a VMU.
//   - Get Media Info reports its geometry in payload word 1 as
//     total blocks in the high half and block size in the low
//     half: (256 << 16) | 512. Payload word 2 is zero.
//   - VMU function data in the device info is word 1 =
//     0x00010100 (one in the high half, 256 blocks in the low
//     half), word 2 = 512 (bytes per block), word 3 = 0. The Get
//     Media Info reply is the authoritative geometry source.
//   - Reset and Kill write no reply at all and leave the receive
//     buffer untouched. They also leave the pad state alone.
package maple

import "encoding/binary"

// Memory is the main RAM access the DMA walker uses. Addresses
// are physical.
type Memory interface {
	Read32(pa uint32) uint32
	Write32(pa uint32, v uint32)
}

// EventSink is the Holly normal bank endpoint. Maple DMA done is
// bit 12 (1 << 12), raised once when a table walk completes. The
// bit stays set in Holly until the CPU clears it there.
type EventSink interface {
	RaiseNormal(bits uint32)
	ClearNormal(bits uint32)
}

// PadState is one controller report in the system button mask:
// A 1<<0, B 1<<1, X 1<<2, Y 1<<3, Start 1<<4, Up 1<<5, Down
// 1<<6, Left 1<<7, Right 1<<8, LT 1<<9, RT 1<<10. The analog
// triggers run 0 to 255 and stick value 128 is center.
type PadState struct {
	Buttons  uint16
	TriggerL uint8
	TriggerR uint8
	JoyX     uint8
	JoyY     uint8
}

// Register window base and offsets from it.
const (
	baseAddr = 0x005F6C00

	regMDSTAR = 0x04 // DMA table start address
	regMDTSEL = 0x10 // trigger select: 0 software, 1 vblank
	regMDEN   = 0x14 // DMA enable
	regMDST   = 0x18 // DMA start and busy
	regMSYS   = 0x80 // DMA system settings
	regMST    = 0x84 // line monitor status, read only
	regMSHTCL = 0x88 // hard trigger clear, write only
	regMDAPRO = 0x8C // DMA protect, write only
	regMMSEL  = 0xE8 // mode select
	regMTXDAD = 0xF4 // last transmit table address, read only
	regMRXDAD = 0xF8 // last receive address, read only
	regMRXDBD = 0xFC // last receive buffer address, read only
)

// Main RAM bounds for table and receive addresses.
const (
	ramBase    = 0x0C000000
	ramTop     = 0x0FFFFFFF
	tableTop   = 0x0FFFFFE0
	maxEntries = 4096
)

// Function codes carried in the first payload word of condition,
// media, and block frames.
const (
	funcController = 0x01000000
	funcStorage    = 0x02000000
)

// Command bytes (requests) and response bytes.
const (
	cmdDeviceRequest = 1
	cmdAllStatus     = 2
	cmdReset         = 3
	cmdKill          = 4
	cmdDeviceInfo    = 5
	cmdAllInfo       = 6
	cmdDeviceReply   = 7
	cmdDataTransfer  = 8
	cmdGetCondition  = 9
	cmdGetMediaInfo  = 10
	cmdBlockRead     = 11
	cmdBlockWrite    = 12
	cmdSetCondition  = 14

	respFileError   = 0xFB
	respDeviceError = 0xFC
	respUnknownCmd  = 0xFE
	respNoDevice    = 0xFFFFFFFF
)

// VMU geometry: 256 blocks of 512 bytes is 128 KB per port.
const (
	vmuBlocks    = 256
	vmuBlockSize = 512
	vmuSize      = vmuBlocks * vmuBlockSize
)

// Serialized layout sizes. The register block is ten words, each
// pad packs into eight bytes, then the four VMU images follow.
const (
	serRegsSize = 40
	serPadSize  = 8
	serPadsOff  = serRegsSize
	serVMUOff   = serPadsOff + 4*serPadSize
)

// SerializeSize is the exact byte size of a serialized Maple:
// the register block, four pad states, and four 128 KB VMU
// images. 40 + 32 + 524288 = 524360.
const SerializeSize = serVMUOff + 4*vmuSize

// Maple is the Maple bus host state.
type Maple struct {
	mem  Memory
	sink EventSink

	mdstar uint32
	mdtsel uint32 // bit 0 only
	mden   uint32 // bit 0 only
	msys   uint32
	mmsel  uint32 // bit 0 only
	mshtcl uint32 // stored, write only
	mdapro uint32 // stored, write only
	txdad  uint32
	rxdad  uint32
	rxdbd  uint32

	pads [4]PadState
	vmu  [4][]byte
}

// New returns a Maple host reading and writing main RAM through
// mem and reporting DMA completion to sink. Power on register
// values: SB_MSYS is 0x3A980000 and SB_MMSEL is 1, all other
// registers are zero. Every pad starts centered with no buttons
// pressed and every VMU starts zeroed.
func New(mem Memory, sink EventSink) *Maple {
	m := &Maple{
		mem:   mem,
		sink:  sink,
		msys:  0x3A980000,
		mmsel: 1,
	}
	for i := range m.pads {
		m.pads[i] = PadState{JoyX: 128, JoyY: 128}
	}
	for i := range m.vmu {
		m.vmu[i] = make([]byte, vmuSize)
	}
	return m
}

// Read32 reads one register by full physical address inside the
// window 0x005F6C00 to 0x005F6CFF. Unlisted offsets read zero.
func (m *Maple) Read32(pa uint32) uint32 {
	switch pa & 0xFF {
	case regMDSTAR:
		return m.mdstar
	case regMDTSEL:
		return m.mdtsel
	case regMDEN:
		return m.mden
	case regMDST:
		// Transfers complete synchronously, so busy never reads
		// back set.
		return 0
	case regMSYS:
		return m.msys
	case regMST:
		return 0xFF // line monitor idle
	case regMMSEL:
		return m.mmsel
	case regMTXDAD:
		return m.txdad
	case regMRXDAD:
		return m.rxdad
	case regMRXDBD:
		return m.rxdbd
	}
	// SB_MSHTCL and SB_MDAPRO are write only and read zero, as
	// does every unlisted offset.
	return 0
}

// Write32 writes one register by full physical address. Writing
// 1 to SB_MDST starts a transfer in software trigger mode when
// SB_MDEN is set; in vblank trigger mode SB_MDST writes are
// ignored and OnVblankOut starts the transfer instead.
func (m *Maple) Write32(pa uint32, v uint32) {
	switch pa & 0xFF {
	case regMDSTAR:
		m.mdstar = v
	case regMDTSEL:
		m.mdtsel = v & 1
	case regMDEN:
		m.mden = v & 1
		// Writing 0 aborts. Busy is never latched in this
		// synchronous model, so there is nothing else to clear.
	case regMDST:
		if v&1 != 0 && m.mdtsel == 0 && m.mden == 1 {
			m.doTransfer()
		}
	case regMSYS:
		m.msys = v
	case regMSHTCL:
		m.mshtcl = v
	case regMDAPRO:
		m.mdapro = v
	case regMMSEL:
		m.mmsel = v & 1
	}
	// SB_MST and the three counters are read only; writes to
	// them and to unlisted offsets do nothing.
}

// SetPad records the controller state for a port (0 to 3).
// Out of range ports are ignored.
func (m *Maple) SetPad(port int, p PadState) {
	if port < 0 || port > 3 {
		return
	}
	m.pads[port] = p
}

// OnVblankOut is called by the system when a vblank out event
// fires. In vblank trigger mode (SB_MDTSEL bit 0 set) with DMA
// enabled it starts a table walk; in software mode it does
// nothing.
func (m *Maple) OnVblankOut() {
	if m.mdtsel == 1 && m.mden == 1 {
		m.doTransfer()
	}
}

// VMUImage returns the live 128 KB storage image of a port, for
// tests and state code. Out of range ports return nil.
func (m *Maple) VMUImage(port int) []byte {
	if port < 0 || port > 3 {
		return nil
	}
	return m.vmu[port]
}

// doTransfer walks the DMA table at SB_MDSTAR. Each entry is a
// header word, a receive address word, then the frame words the
// header counts. The walk stops after the entry whose header has
// bit 31 set. On completion it raises the Holly normal bank
// Maple DMA done event, bit 12.
func (m *Maple) doTransfer() {
	if m.mem == nil {
		return
	}
	addr := m.mdstar
	if addr < ramBase || addr > tableTop {
		// No valid table start, so no walk and no completion
		// event.
		return
	}
	for n := 0; n < maxEntries; n++ {
		header := m.mem.Read32(addr)
		recv := m.mem.Read32(addr + 4)
		frameWords := int(header&0xFF) + 1
		last := header&(1<<31) != 0

		m.txdad = addr
		m.rxdad = recv
		m.rxdbd = recv

		frame := make([]uint32, frameWords)
		for i := range frame {
			frame[i] = m.mem.Read32(addr + 8 + uint32(i)*4)
		}
		m.processFrame(recv, frame)

		addr += uint32(2+frameWords) * 4
		m.txdad = addr
		if last {
			break
		}
		if addr < ramBase || addr > tableTop {
			break
		}
	}
	if m.sink != nil {
		m.sink.RaiseNormal(1 << 12)
	}
}

// validRecv reports whether a receive address sits in main RAM.
// Frames with an address outside main RAM write nothing but are
// still consumed.
func validRecv(recv uint32) bool {
	return recv >= ramBase && recv <= ramTop
}

// processFrame routes and answers one Maple frame. frame[0] is
// the frame header word: command in bits 31:24, recipient in
// bits 23:16, sender in bits 15:8, payload word count in bits
// 7:0. The payload words follow it.
func (m *Maple) processFrame(recv uint32, frame []uint32) {
	if len(frame) == 0 {
		return
	}
	head := frame[0]
	cmd := byte(head >> 24)
	recipient := byte(head >> 16)

	payload := frame[1:]
	if n := int(head & 0xFF); n < len(payload) {
		payload = payload[:n]
	}
	var fn uint32
	if len(payload) > 0 {
		fn = payload[0]
	}

	port := int(recipient>>6) & 3
	hostAddr := byte(port << 6)
	mainAddr := hostAddr | 0x20
	vmuAddr := hostAddr | 0x21

	isVMU := false
	switch recipient {
	case mainAddr:
		// The controller answers.
	case vmuAddr:
		isVMU = true
	default:
		// No device at this address: the documented reply is a
		// single 0xFFFFFFFF word in the receive buffer.
		if validRecv(recv) {
			m.mem.Write32(recv, respNoDevice)
		}
		return
	}
	devAddr := recipient

	switch cmd {
	case cmdDeviceRequest:
		m.writeResponse(recv, cmdDeviceInfo, hostAddr, devAddr, deviceInfoPayload(isVMU))
	case cmdAllStatus:
		info := deviceInfoPayload(isVMU)
		if isVMU {
			info = append(info, 0, 0)
		} else {
			w1, w2 := conditionWords(m.pads[port])
			info = append(info, w1, w2)
		}
		m.writeResponse(recv, cmdAllInfo, hostAddr, devAddr, info)
	case cmdReset, cmdKill:
		// No reply is written and the receive buffer is left
		// untouched.
		return
	case cmdGetCondition:
		if !isVMU && fn == funcController {
			w1, w2 := conditionWords(m.pads[port])
			m.writeResponse(recv, cmdDeviceReply, hostAddr, devAddr, []uint32{fn, w1, w2})
		} else {
			m.writeResponse(recv, respDeviceError, hostAddr, devAddr, []uint32{fn})
		}
	case cmdGetMediaInfo:
		if isVMU && fn == funcStorage {
			// Word 1: total blocks in the high half, block
			// size in the low half. Word 2 is zero.
			geo := uint32(vmuBlocks<<16) | uint32(vmuBlockSize)
			m.writeResponse(recv, cmdDataTransfer, hostAddr, devAddr, []uint32{fn, geo, 0})
		} else {
			m.writeResponse(recv, respDeviceError, hostAddr, devAddr, []uint32{fn})
		}
	case cmdBlockRead:
		m.blockRead(recv, hostAddr, devAddr, port, isVMU, payload, fn)
	case cmdBlockWrite:
		m.blockWrite(recv, hostAddr, devAddr, port, isVMU, payload, fn)
	case cmdSetCondition:
		if !isVMU && fn == funcController {
			// Accept on a controller is a no op that replies
			// with the function word only.
			m.writeResponse(recv, cmdDeviceReply, hostAddr, devAddr, []uint32{fn})
		} else {
			m.writeResponse(recv, respDeviceError, hostAddr, devAddr, []uint32{fn})
		}
	default:
		// Unknown command: response byte 0xFE, no payload.
		m.writeResponse(recv, respUnknownCmd, hostAddr, devAddr, nil)
	}
}

// writeResponse writes one response frame at recv: the header
// word (response byte, host address as recipient, device address
// as sender, payload word count) then the payload words. Nothing
// is written when recv is outside main RAM.
func (m *Maple) writeResponse(recv uint32, resp byte, hostAddr, devAddr byte, payload []uint32) {
	if !validRecv(recv) {
		return
	}
	head := uint32(resp)<<24 | uint32(hostAddr)<<16 | uint32(devAddr)<<8 | uint32(len(payload))
	m.mem.Write32(recv, head)
	for i, w := range payload {
		m.mem.Write32(recv+4+uint32(i)*4, w)
	}
}

// writeFileError writes the 0xFB file error response: payload
// word 0 is the function code, word 1 is the error code word,
// which is zero here.
func (m *Maple) writeFileError(recv uint32, hostAddr, devAddr byte, fn uint32) {
	m.writeResponse(recv, respFileError, hostAddr, devAddr, []uint32{fn, 0})
}

// blockAddr splits a block address word: bits 15:0 block number,
// bits 23:16 partition, bits 31:24 phase.
func blockAddr(w uint32) (block, partition, phase uint32) {
	return w & 0xFFFF, (w >> 16) & 0xFF, (w >> 24) & 0xFF
}

// blockRead answers command 11 for the VMU of a port.
func (m *Maple) blockRead(recv uint32, hostAddr, devAddr byte, port int, isVMU bool, payload []uint32, fn uint32) {
	if !isVMU || fn != funcStorage {
		m.writeResponse(recv, respDeviceError, hostAddr, devAddr, []uint32{fn})
		return
	}
	if len(payload) < 2 {
		m.writeFileError(recv, hostAddr, devAddr, fn)
		return
	}
	addrWord := payload[1]
	block, partition, phase := blockAddr(addrWord)
	if phase != 0 || partition != 0 || block >= vmuBlocks {
		m.writeFileError(recv, hostAddr, devAddr, fn)
		return
	}
	out := make([]uint32, 0, 130)
	out = append(out, fn, addrWord)
	img := m.vmu[port]
	off := int(block) * vmuBlockSize
	for i := 0; i < vmuBlockSize/4; i++ {
		out = append(out, binary.LittleEndian.Uint32(img[off+i*4:]))
	}
	m.writeResponse(recv, cmdDataTransfer, hostAddr, devAddr, out)
}

// blockWrite answers command 12 for the VMU of a port. A write
// whose data words are missing is reported as a file error.
func (m *Maple) blockWrite(recv uint32, hostAddr, devAddr byte, port int, isVMU bool, payload []uint32, fn uint32) {
	if !isVMU || fn != funcStorage {
		m.writeResponse(recv, respDeviceError, hostAddr, devAddr, []uint32{fn})
		return
	}
	if len(payload) < 2 {
		m.writeFileError(recv, hostAddr, devAddr, fn)
		return
	}
	addrWord := payload[1]
	block, partition, phase := blockAddr(addrWord)
	if phase != 0 || partition != 0 || block >= vmuBlocks {
		m.writeFileError(recv, hostAddr, devAddr, fn)
		return
	}
	if len(payload) < 2+vmuBlockSize/4 {
		m.writeFileError(recv, hostAddr, devAddr, fn)
		return
	}
	img := m.vmu[port]
	off := int(block) * vmuBlockSize
	for i := 0; i < vmuBlockSize/4; i++ {
		binary.LittleEndian.PutUint32(img[off+i*4:], payload[2+i])
	}
	m.writeResponse(recv, cmdDeviceReply, hostAddr, devAddr, []uint32{fn, addrWord})
}

// deviceInfoPayload builds the 28 word (112 byte) device info
// payload shared by Device Request and All Status replies. The
// first four words are the function code and function data. The
// remaining 96 bytes are: area code 0xFF, connector direction
// 0x00, a 30 byte space padded product name, 60 zero bytes of
// product license (left blank on purpose), standby power 43 and
// max power 50 as little endian u16 values, in that order.
func deviceInfoPayload(isVMU bool) []uint32 {
	b := make([]byte, 112)
	var name string
	if isVMU {
		binary.LittleEndian.PutUint32(b[0:], funcStorage)
		// Function data: word 1 holds one in the high half
		// and 256 blocks in the low half, word 2 is the
		// block size in bytes, word 3 is zero.
		binary.LittleEndian.PutUint32(b[4:], 0x00010100)
		binary.LittleEndian.PutUint32(b[8:], vmuBlockSize)
		name = "Dreamcast VMU"
	} else {
		binary.LittleEndian.PutUint32(b[0:], funcController)
		// The documented controller capability word: buttons
		// and axes bitmask. Words 2 and 3 are zero.
		binary.LittleEndian.PutUint32(b[4:], 0xFE060F00)
		name = "Dreamcast Controller"
	}
	b[16] = 0xFF // area code: all regions
	b[17] = 0x00 // connector direction
	for i := 0; i < 30; i++ {
		b[18+i] = ' '
	}
	copy(b[18:48], name)
	// Bytes 48 to 107 (product license) stay zero.
	binary.LittleEndian.PutUint16(b[108:], 43) // standby power
	binary.LittleEndian.PutUint16(b[110:], 50) // max power
	words := make([]uint32, 28)
	for i := range words {
		words[i] = binary.LittleEndian.Uint32(b[i*4:])
	}
	return words
}

// conditionWords converts a PadState into the two controller
// condition words. Word 1 packs the active low button bits in
// the high half, then the right trigger, then the left trigger.
// Word 2 packs the main stick in the low half and the second
// stick, always centered, in the high half.
//
// System mask bits map to wire bits as: A to 1<<2, B to 1<<1,
// X to 1<<10, Y to 1<<9, Start to 1<<3, Up to 1<<4, Down to
// 1<<5, Left to 1<<6, Right to 1<<7. The digital LT and RT
// system bits drive the matching analog trigger to 255 when the
// analog value is lower.
func conditionWords(p PadState) (uint32, uint32) {
	var pressed uint16
	if p.Buttons&(1<<0) != 0 {
		pressed |= 1 << 2 // A
	}
	if p.Buttons&(1<<1) != 0 {
		pressed |= 1 << 1 // B
	}
	if p.Buttons&(1<<2) != 0 {
		pressed |= 1 << 10 // X
	}
	if p.Buttons&(1<<3) != 0 {
		pressed |= 1 << 9 // Y
	}
	if p.Buttons&(1<<4) != 0 {
		pressed |= 1 << 3 // Start
	}
	if p.Buttons&(1<<5) != 0 {
		pressed |= 1 << 4 // Up
	}
	if p.Buttons&(1<<6) != 0 {
		pressed |= 1 << 5 // Down
	}
	if p.Buttons&(1<<7) != 0 {
		pressed |= 1 << 6 // Left
	}
	if p.Buttons&(1<<8) != 0 {
		pressed |= 1 << 7 // Right
	}
	ltrig := uint32(p.TriggerL)
	if p.Buttons&(1<<9) != 0 && ltrig < 255 {
		ltrig = 255
	}
	rtrig := uint32(p.TriggerR)
	if p.Buttons&(1<<10) != 0 && rtrig < 255 {
		rtrig = 255
	}
	activeLow := uint32(^pressed)
	w1 := activeLow<<16 | rtrig<<8 | ltrig
	w2 := uint32(p.JoyX) | uint32(p.JoyY)<<8 | 128<<16 | 128<<24
	return w1, w2
}

// Serialize writes the Maple state into dst and returns the
// number of bytes written, SerializeSize. It returns 0 and
// writes nothing when dst is too small. Layout:
//
//	words 0 to 9   MDSTAR, MDTSEL, MDEN, MSYS, MMSEL, MSHTCL,
//	                MDAPRO, MTXDAD, MRXDAD, MRXDBD
//	bytes 40 to 71  four pad states, eight bytes each: buttons
//	                as a little endian u16, then TriggerL,
//	                TriggerR, JoyX, JoyY, then two zero bytes
//	bytes 72 on     the four 128 KB VMU images, ports 0 to 3
func (m *Maple) Serialize(dst []byte) int {
	if len(dst) < SerializeSize {
		return 0
	}
	regs := []uint32{m.mdstar, m.mdtsel, m.mden, m.msys, m.mmsel,
		m.mshtcl, m.mdapro, m.txdad, m.rxdad, m.rxdbd}
	for i, v := range regs {
		binary.LittleEndian.PutUint32(dst[i*4:], v)
	}
	for i, p := range m.pads {
		off := serPadsOff + i*serPadSize
		binary.LittleEndian.PutUint16(dst[off:], p.Buttons)
		dst[off+2] = p.TriggerL
		dst[off+3] = p.TriggerR
		dst[off+4] = p.JoyX
		dst[off+5] = p.JoyY
		dst[off+6] = 0
		dst[off+7] = 0
	}
	for i := range m.vmu {
		copy(dst[serVMUOff+i*vmuSize:], m.vmu[i])
	}
	return SerializeSize
}

// Deserialize restores a state written by Serialize. It returns
// false and leaves the live state alone when src is shorter
// than SerializeSize.
func (m *Maple) Deserialize(src []byte) bool {
	if len(src) < SerializeSize {
		return false
	}
	m.mdstar = binary.LittleEndian.Uint32(src[0:])
	m.mdtsel = binary.LittleEndian.Uint32(src[4:])
	m.mden = binary.LittleEndian.Uint32(src[8:])
	m.msys = binary.LittleEndian.Uint32(src[12:])
	m.mmsel = binary.LittleEndian.Uint32(src[16:])
	m.mshtcl = binary.LittleEndian.Uint32(src[20:])
	m.mdapro = binary.LittleEndian.Uint32(src[24:])
	m.txdad = binary.LittleEndian.Uint32(src[28:])
	m.rxdad = binary.LittleEndian.Uint32(src[32:])
	m.rxdbd = binary.LittleEndian.Uint32(src[36:])
	for i := range m.pads {
		off := serPadsOff + i*serPadSize
		m.pads[i] = PadState{
			Buttons:  binary.LittleEndian.Uint16(src[off:]),
			TriggerL: src[off+2],
			TriggerR: src[off+3],
			JoyX:     src[off+4],
			JoyY:     src[off+5],
		}
	}
	for i := range m.vmu {
		copy(m.vmu[i], src[serVMUOff+i*vmuSize:serVMUOff+(i+1)*vmuSize])
	}
	return true
}
