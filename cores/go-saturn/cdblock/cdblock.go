// Package cdblock models the Sega Saturn CD block: the command
// processor behind the 64 byte register page owned by the bus, the
// selector (filter and buffer partition) engine, the 200 block
// sector buffer, and the play, seek and scan streaming of a disc
// image from the sibling cd package.
//
// Register page layout: the bus names the four command/response
// registers CDCR1 to CDCR4 at word offsets 0x18, 0x1C, 0x20 and
// 0x24, and the manual numbers them CR1 to CR4. Each is a 16 bit
// big endian register at an even byte offset, addressed exactly
// as the SH-2 addresses it, so CR1 is page bytes [0x18:0x1A],
// CR2 is [0x1C:0x1E], CR3 is [0x20:0x22] and CR4 is [0x24:0x26].
// The interrupt cause register HIRQ is the 16 bit big endian
// value at page bytes [0x08:0x0A] and its mask is at [0x0C:0x0E].
// The data
// transfer port is the 16 bit register at page bytes [0x28:0x2A]
// in this core's page model. On real hardware the data FIFO lives
// at its own CS2 offset; folding it into the page at 0x28 is this
// core's stated simplification, and the parent brief pins it.
//
// Command handshake: Poll is called once per scanline. A command
// executes only when the CMOK bit of HIRQ is 0 and the four CR
// words differ from the last response written by this package, so
// software acknowledges a response by clearing CMOK in the page
// (the page is plain storage, so writing a value without the bit
// clears it) and then writes the next command and its parameters.
// The command code is the top byte of CR1, parameters live in the
// rest of CR1 and in CR2 to CR4. After a command executes, CMOK is
// set along with the command specific HIRQ bits, and OnIRQ fires
// when a newly set bit is unmasked in the page mask word.
//
// Data transfers: commands that return bulk data (Get TOC, Get
// Subcode Q, Get Sector Data, Get Then Delete Sector Data) load a
// FIFO and set DRDY. The first FIFO word is placed at the data
// port by the executing Poll; each later Poll advances the port
// to the next word, so the host reads the port once per Poll.
// When the FIFO drains, DRDY clears and EHST sets. End Data
// Transfer finishes a transfer early with the same effects. That
// one word per Poll contract is the transfer timing model here.
//
// Command codes: the firmware numbering is implemented. The
// CDB106 firmware reference traced from the production ROM and
// the ST-38 CD Communication Interface function grouping agree
// on it: 0x50 is Get Buffer Size, Get Sector Number is 0x51,
// Calculate Actual Data Size is 0x52, Get Actual Data Size is
// 0x53, Get Sector Info is 0x54, Execute FAD Search is 0x55, Get
// FAD Search Result is 0x56, Set Sector Length is 0x60, Get
// Sector Data is 0x61, Delete Sector Data is 0x62, Get Then
// Delete Sector Data is 0x63 and Put Sector Data is 0x64. An
// earlier brief numbering one lower for this range was
// superseded. Copy Sector Data (0x65) and Move Sector Data
// (0x66) also exist in the firmware numbering; both are left
// unimplemented here and answer REJECT as unknown commands.
//
// Selector count: the brief said 8 filters and asked for the
// count to be verified. The manual counts 24 selectors, each a
// filter paired with a buffer partition, so this package
// implements 24 filters and 24 partitions sharing the 200 block
// pool. Filter connection targets use one byte: 0 to 23 is a
// buffer partition, 0xFE chains to the next filter, and 0xFF is
// disconnected (discard). That unified encoding is this core's
// adaptation of the manual's separate true (partition) and false
// (filter) connector encodings.
//
// Simplifications, stated plainly:
//
//   - Seek completes instantly: the state passes straight to
//     PAUSE at the target. Play end also lands in PAUSE.
//   - Play position values with bit 23 set are FADs with the flag
//     bit stripped; without the flag the value is a track number
//     read as a plain integer. An end position of 0 means the end
//     of the disc.
//   - The play mode byte repeat field is its low nibble: 0 plays
//     once, 1 to 14 is that many extra repeats, 0x0F is endless,
//     and a full mode byte field of 0x7F leaves the repeat
//     setting unchanged. Bit 7 (pickup hold) is ignored.
//   - Get Subcode Q returns its 10 bytes through the transfer
//     FIFO with a response word count of 5, matching the firmware
//     layout. The 2 CRC bytes are not part of the response. The
//     relative time in the pregap clamps at zero instead of
//     counting negative.
//   - Get TOC returns one 4 byte entry per track plus one for
//     the lead-out: control/ADR, then the 24 bit FAD big endian.
//     The firmware's fixed 102 entry table is not reproduced.
//   - Sector counts of 0 in Get, Delete and Get Then Delete mean
//     one sector in this core, and an overrunning count clamps to
//     the end of the partition instead of answering WAIT.
//   - Put Sector Data always answers REJECT and stores nothing.
//     The firmware implements Put with allocation semantics; that
//     behaviour is deferred in this core. Copy and Move Sector
//     Data answer REJECT as unknown commands.
//   - The CD device connection defaults to filter 0 at power on
//     and after Initialize, so Play buffers with no extra setup.
//     The firmware default is disconnected; Set CD Device
//     Connection with 0xFF disconnects here too.
//   - Get Hardware Info reports the traced CDB106 constants:
//     hardware version 2, MPEG version 0 (no card), and zero
//     drive version and revision fields.
//   - Mode 2 Form 2 sectors read at the 2048 sector length return
//     the first 2048 bytes of the form's user area.
//   - A new command issued while a transfer is active abandons
//     the transfer; a pending Get Then Delete deletion is then
//     not applied.
package cdblock

import (
	"encoding/binary"
	"fmt"

	"github.com/davidpatton/itak-gaming/cores/go-saturn/cd"
)

// Register page offsets. CR1 to CR4 are the bus registers CDCR1
// to CDCR4 at word offsets 0x18, 0x1C, 0x20 and 0x24.
const (
	offHIRQ     = 0x08
	offHIRQMask = 0x0C
	offCR1      = 0x18
	offDataPort = 0x28
)

// crOff returns the page offset of CR n (1 to 4).
func crOff(n int) int { return offCR1 + 4*(n-1) }

// HIRQ bit assignments, the production layout: the preliminary
// interface document put MPEG at bit 6, but the production
// firmware assigns bits 6 to 13 as below and this core follows
// the production table.
const (
	HIRQCMOK = 1 << 0  // command response set, can issue a command
	HIRQDRDY = 1 << 1  // data transfer preparation complete
	HIRQCSCT = 1 << 2  // one sector stored from the disc stream
	HIRQBFUL = 1 << 3  // buffer full
	HIRQPEND = 1 << 4  // play ended, position outside the play area
	HIRQDCHG = 1 << 5  // disc changed
	HIRQESEL = 1 << 6  // selector operation ended
	HIRKEHST = 1 << 7  // host input/output operation ended
	HIRQECPY = 1 << 8  // copy or move operation ended
	HIRQEFLS = 1 << 9  // file system operation ended
	HIRQSCDQ = 1 << 10 // subcode Q updated
	HIRQMPED = 1 << 11 // MPEG operation ended
	HIRQMPCM = 1 << 12 // MPEG undefined interval ended
	HIRQMPST = 1 << 13 // MPEG status notification pending
)

// CD status codes, the low nibble of the status byte.
const (
	StatusBUSY    = 0x00
	StatusPAUSE   = 0x01
	StatusSTANDBY = 0x02
	StatusPLAY    = 0x03
	StatusSEEK    = 0x04
	StatusSCAN    = 0x05
	StatusOPEN    = 0x06
	StatusNODISC  = 0x07
	StatusRETRY   = 0x08
	StatusERROR   = 0x09
	StatusFATAL   = 0x0A
)

// Status byte flag encodings above the status code.
const (
	statusFlagWAIT     = 0x80 // command received but not executed
	statusFlagTransfer = 0x40 // a data transfer is prepared
	statusReject       = 0xFF // whole status byte: command rejected
)

// Command codes implemented by this package, in the firmware
// numbering. See the package comment for the numbering note.
// Copy Sector Data (0x65) and Move Sector Data (0x66) exist in
// the firmware numbering but are not implemented.
const (
	CmdGetStatus        = 0x00
	CmdGetHardwareInfo  = 0x01
	CmdGetTOC           = 0x02
	CmdGetSessionInfo   = 0x03
	CmdInitialize       = 0x04
	CmdEndDataTransfer  = 0x06
	CmdPlayDisc         = 0x10
	CmdSeekDisc         = 0x11
	CmdScanDisc         = 0x12
	CmdGetSubcodeQ      = 0x20
	CmdSetCDConnection  = 0x30
	CmdGetCDConnection  = 0x31
	CmdSetFilterRange   = 0x40
	CmdGetFilterRange   = 0x41
	CmdSetFilterSubhead = 0x42
	CmdGetFilterSubhead = 0x43
	CmdSetFilterMode    = 0x44
	CmdGetFilterMode    = 0x45
	CmdSetFilterConn    = 0x46
	CmdGetFilterConn    = 0x47
	CmdGetBufferSize    = 0x50
	CmdGetSectorNumber  = 0x51
	CmdCalcActualSize   = 0x52
	CmdGetActualSize    = 0x53
	CmdGetSectorInfo    = 0x54
	CmdExecuteFADSearch = 0x55
	CmdGetFADSearch     = 0x56
	CmdSetSectorLength  = 0x60
	CmdGetSectorData    = 0x61
	CmdDeleteSectorData = 0x62
	CmdGetThenDelete    = 0x63
	CmdPutSectorData    = 0x64
)

// Selector and buffer geometry.
const (
	NumFilters    = 24
	NumPartitions = 24
	BufferBlocks  = 200
)

// Connection target encodings for filter outputs.
const (
	ConnNext    = 0xFE // chain to the next filter
	ConnDiscard = 0xFF // disconnected: the sector is discarded
)

// Filter mode bits.
const (
	FilterModeFN           = 1 << 0 // file number condition valid
	FilterModeCN           = 1 << 1 // channel number condition valid
	FilterModeSM           = 1 << 2 // submode condition valid
	FilterModeCI           = 1 << 3 // coding information condition valid
	FilterModeReverse      = 1 << 4 // reverse the match result
	FilterModeRangeOutside = 1 << 5 // FAD range test passes outside the range
	FilterModeRange        = 1 << 6 // FAD range condition valid
	FilterModeInit         = 1 << 7 // reset this filter's conditions
)

// Sector length types for Set Sector Length, with the byte count
// each type extracts from a raw sector.
var sectorLengthBytes = [4]int{2048, 2336, 2340, 2352}

// Machine rate and disc sector rate for Advance.
const (
	cyclesPerSecond = 28636400
	sectorsPerSec   = 75
	scanMultiplier  = 16
)

// filter holds one selector filter: its FAD range, subheader
// conditions, mode byte, and true and false output connections.
type filter struct {
	startFAD  int
	rangeCnt  int
	mode      byte
	fn        byte
	cn        byte
	smMask    byte
	smVal     byte
	ciMask    byte
	ciVal     byte
	trueConn  byte
	falseConn byte
}

// matches reports whether a buffered sector header passes this
// filter, following the mode bits: enabled conditions must all
// hold, the range outside bit inverts the range test, and the
// reverse bit inverts the combined result.
func (f *filter) matches(blk *block) bool {
	rangePass := true
	if f.mode&FilterModeRange != 0 && f.rangeCnt > 0 {
		rangePass = blk.fad >= f.startFAD && blk.fad < f.startFAD+f.rangeCnt
	}
	if f.mode&FilterModeRangeOutside != 0 {
		rangePass = !rangePass
	}
	subPass := true
	if f.mode&FilterModeFN != 0 && blk.fn != f.fn {
		subPass = false
	}
	if f.mode&FilterModeCN != 0 && blk.cn != f.cn {
		subPass = false
	}
	if f.mode&FilterModeSM != 0 && blk.sm&f.smMask != f.smVal&f.smMask {
		subPass = false
	}
	if f.mode&FilterModeCI != 0 && blk.ci&f.ciMask != f.ciVal&f.ciMask {
		subPass = false
	}
	m := rangePass && subPass
	if f.mode&FilterModeReverse != 0 {
		m = !m
	}
	return m
}

// block is one buffered sector: its descriptor (FAD and header
// fields) plus the raw payload. The payload is nil for blocks
// restored by Deserialize; their bytes are re-read from the image
// by FAD when a Get Sector Data asks for them.
type block struct {
	fad     int
	fn      byte
	cn      byte
	sm      byte
	ci      byte
	payload []byte
}

// transfer is one active data port FIFO.
type transfer struct {
	data          []byte
	pos           int // byte offset of the word now at the port
	deletePart    int
	deleteOff     int
	deleteCount   int
	pendingDelete bool
}

// Block is the CD block view over the bus register page.
type Block struct {
	page []byte
	img  *cd.Image

	// OnIRQ fires when a HIRQ bit newly sets and that bit is
	// unmasked in the page mask word. It may be nil.
	OnIRQ func()

	power bool
	sink  func(samples []int16)

	state       byte // one of the Status codes for pause, standby, play, scan
	fad         int  // current position as a frame address
	playStart   int
	playEnd     int
	repeatsLeft int
	infinite    bool
	scanReverse bool
	playAcc     int64 // cycle accumulator, scaled by sectorsPerSec
	scanAcc     int64

	devFilter int // filter the CD device feeds, negative when disconnected
	filters   [NumFilters]filter
	parts     [NumPartitions][]block
	getLen    int // sector length type for reads, 0 to 3
	putLen    int

	calcSize   int // last Calculate Actual Data Size result, in bytes
	searchPart int // partition of the last FAD search, negative when none
	searchPos  int
	searchFAD  int

	xfer        *transfer
	lastCmd     [4]uint16
	lastCmdSeen bool
}

// New wraps the 64 byte bus CD register page. Power defaults to
// on, the state to standby, and the selector to its default
// wiring: filter 0 passes everything to partition 0, every other
// filter is disconnected, and the CD device feeds filter 0.
func New(page []byte) *Block {
	b := &Block{page: page, power: true, devFilter: 0, searchPart: -1}
	b.state = StatusSTANDBY
	b.fad = 150
	b.resetSelector()
	b.lastCmdSeen = false
	return b
}

// resetSelector restores the default filter conditions and
// connections and clears every partition.
func (b *Block) resetSelector() {
	for i := range b.filters {
		b.filters[i] = filter{trueConn: ConnDiscard, falseConn: ConnDiscard}
	}
	b.filters[0].trueConn = 0
	for i := range b.parts {
		b.parts[i] = nil
	}
}

// SetImage attaches a disc image, or nil for no disc. It homes
// the position and returns the drive to standby, and raises
// DCHG when it replaces a different attached disc. It does not
// clear the selector or the buffer: a save state restore relies
// on Deserialize followed by SetImage keeping the restored
// block descriptors, whose payloads then re-read from this
// image by FAD. The Initialize command is what clears the
// buffer and the selector.
func (b *Block) SetImage(img *cd.Image) {
	changed := b.img != nil && img != b.img
	b.img = img
	b.xfer = nil
	b.state = StatusSTANDBY
	b.fad = 150
	if changed {
		b.setHIRQ(HIRQDCHG)
	}
}

// SetPower turns the block on or off, as the SMPC CDON and CDOFF
// commands do. While off, Poll and Advance do nothing.
func (b *Block) SetPower(on bool) { b.power = on }

// SetAudioSink installs the CD-DA output callback. It receives
// interleaved stereo samples at 44,100 Hz: 1176 values (588
// stereo pairs) per audio sector streamed.
func (b *Block) SetAudioSink(fn func(samples []int16)) { b.sink = fn }

// cr reads CR n (1 to 4) from the page.
func (b *Block) cr(n int) uint16 {
	return binary.BigEndian.Uint16(b.page[crOff(n) : crOff(n)+2])
}

// setCR writes CR n (1 to 4) into the page.
func (b *Block) setCR(n int, v uint16) {
	binary.BigEndian.PutUint16(b.page[crOff(n):crOff(n)+2], v)
}

// crTuple snapshots the four CR words.
func (b *Block) crTuple() [4]uint16 {
	return [4]uint16{b.cr(1), b.cr(2), b.cr(3), b.cr(4)}
}

// hirq reads the HIRQ word from the page.
func (b *Block) hirq() uint16 {
	return binary.BigEndian.Uint16(b.page[offHIRQ : offHIRQ+2])
}

// hirqMask reads the HIRQ mask word from the page.
func (b *Block) hirqMask() uint16 {
	return binary.BigEndian.Uint16(b.page[offHIRQMask : offHIRQMask+2])
}

// setHIRQ raises HIRQ bits, preserving any bits the host has not
// cleared in the page, and fires OnIRQ for newly set unmasked
// bits.
func (b *Block) setHIRQ(bits uint16) {
	old := b.hirq()
	nw := old | bits
	if nw == old {
		return
	}
	binary.BigEndian.PutUint16(b.page[offHIRQ:offHIRQ+2], nw)
	newly := nw &^ old
	if b.OnIRQ != nil && newly&b.hirqMask() != 0 {
		b.OnIRQ()
	}
}

// clearHIRQ drops HIRQ bits from the page word.
func (b *Block) clearHIRQ(bits uint16) {
	binary.BigEndian.PutUint16(b.page[offHIRQ:offHIRQ+2], b.hirq()&^bits)
}

// dataWord returns the FIFO word at byte offset pos of data, with
// a zero low byte for a trailing odd byte.
func dataWord(data []byte, pos int) uint16 {
	hi := uint16(data[pos])
	var lo uint16
	if pos+1 < len(data) {
		lo = uint16(data[pos+1])
	}
	return hi<<8 | lo
}

// loadPort places the current transfer word at the data port.
func (b *Block) loadPort() {
	if b.xfer == nil || b.xfer.pos >= len(b.xfer.data) {
		binary.BigEndian.PutUint16(b.page[offDataPort:offDataPort+2], 0)
		return
	}
	binary.BigEndian.PutUint16(b.page[offDataPort:offDataPort+2], dataWord(b.xfer.data, b.xfer.pos))
}

// startTransfer installs a FIFO and loads its first word at the
// data port. The executing Poll returns before the transfer
// advance step, so the first word waits for the host; each later
// Poll advances one word.
func (b *Block) startTransfer(data []byte, del *transfer) {
	t := &transfer{data: data}
	if del != nil {
		t.deletePart = del.deletePart
		t.deleteOff = del.deleteOff
		t.deleteCount = del.deleteCount
		t.pendingDelete = true
	}
	b.xfer = t
	b.loadPort()
}

// finishTransfer ends the active transfer: DRDY clears, EHST
// sets, a pending Get Then Delete deletion is applied, and the
// data port returns to zero.
func (b *Block) finishTransfer() {
	if b.xfer == nil {
		return
	}
	if b.xfer.pendingDelete {
		b.deleteRange(b.xfer.deletePart, b.xfer.deleteOff, b.xfer.deleteCount)
	}
	b.xfer = nil
	binary.BigEndian.PutUint16(b.page[offDataPort:offDataPort+2], 0)
	b.clearHIRQ(HIRQDRDY)
	b.setHIRQ(HIRKEHST)
}

// advanceTransfer moves the data port to the next FIFO word, or
// finishes the transfer when the last word has been read.
func (b *Block) advanceTransfer() {
	b.xfer.pos += 2
	if b.xfer.pos >= len(b.xfer.data) {
		b.finishTransfer()
		return
	}
	b.loadPort()
}

// Poll runs one scanline of housekeeping: the command handshake
// first, then data port advancement for an active transfer.
// While the power is off Poll does nothing at all.
func (b *Block) Poll() {
	if !b.power {
		return
	}
	tuple := b.crTuple()
	if b.hirq()&HIRQCMOK == 0 && (!b.lastCmdSeen || tuple != b.lastCmd) {
		b.lastCmdSeen = true
		params := tuple
		b.execute(byte(params[0]>>8), params)
		return
	}
	if b.xfer != nil {
		b.advanceTransfer()
	}
}

// respond writes a four word response into the CRs, records it
// as the handshake baseline, and raises the given HIRQ bits on
// top of CMOK. A transfer started by the command keeps the port
// word loaded by startTransfer.
func (b *Block) respond(resp [4]uint16, hirqBits uint16) {
	for i, v := range resp {
		b.setCR(i+1, v)
	}
	b.lastCmd = resp
	b.lastCmdSeen = true
	b.setHIRQ(HIRQCMOK | hirqBits)
}

// statusCodeNow is the status code Get Status would report: no
// disc outranks the drive state.
func (b *Block) statusCodeNow() byte {
	if b.img == nil {
		return StatusNODISC
	}
	return b.state
}

// statusResponse builds the standard CD status response: CR1
// carries the status byte and the flag and repeat low byte, CR2
// the control/ADR and track, CR3 the index and FAD high byte,
// and CR4 the FAD low word. The status byte is supplied by the
// caller so reject, wait and transfer variants share the layout.
func (b *Block) statusResponse(statusByte byte) [4]uint16 {
	var ctrlAdr, track, index byte
	fad := 0
	if b.img != nil {
		fad = b.fad
		if tr, inPregap, ok := b.trackAtFAD(fad); ok {
			ctrlAdr = ctrlADR(tr)
			track = byte(tr.Number)
			index = 1
			if inPregap {
				index = 0
			}
		}
	}
	rep := byte(b.repeatsLeft)
	if b.infinite {
		rep = 0x0F
	} else if rep > 0x0F {
		rep = 0x0F
	}
	return [4]uint16{
		uint16(statusByte)<<8 | uint16(rep),
		uint16(ctrlAdr)<<8 | uint16(toBCD(int(track))),
		uint16(index)<<8 | uint16(byte(fad>>16)),
		uint16(fad & 0xFFFF),
	}
}

// reject answers with the manual reject encoding: the whole
// status byte is 0xFF and the remaining words carry the standard
// status data.
func (b *Block) reject() {
	b.xfer = nil
	b.respond(b.statusResponse(statusReject), 0)
}

// wait answers with the WAIT flag over the current status code,
// the retryable failure encoding.
func (b *Block) wait() {
	b.respond(b.statusResponse(statusFlagWAIT|b.statusCodeNow()), 0)
}

// execute runs one command code with its parameter snapshot.
func (b *Block) execute(cmd byte, p [4]uint16) {
	// Any new command abandons an active transfer without
	// applying a pending deletion. End Data Transfer is the
	// exception: it is the command that finishes a transfer.
	if cmd != CmdEndDataTransfer && b.xfer != nil {
		b.xfer = nil
		binary.BigEndian.PutUint16(b.page[offDataPort:offDataPort+2], 0)
		b.clearHIRQ(HIRQDRDY)
	}
	switch cmd {
	case CmdGetStatus:
		b.respond(b.statusResponse(b.statusCodeNow()), 0)
	case CmdGetHardwareInfo:
		b.cmdHardwareInfo()
	case CmdGetTOC:
		b.cmdGetTOC()
	case CmdGetSessionInfo:
		b.cmdSessionInfo()
	case CmdInitialize:
		b.cmdInitialize()
	case CmdEndDataTransfer:
		b.cmdEndDataTransfer()
	case CmdPlayDisc:
		b.cmdPlay(p)
	case CmdSeekDisc:
		b.cmdSeek(p)
	case CmdScanDisc:
		b.cmdScan(p)
	case CmdGetSubcodeQ:
		b.cmdSubcodeQ(p)
	case CmdSetCDConnection:
		b.cmdSetCDConnection(p)
	case CmdGetCDConnection:
		b.cmdGetCDConnection()
	case CmdSetFilterRange:
		b.cmdSetFilterRange(p)
	case CmdGetFilterRange:
		b.cmdGetFilterRange(p)
	case CmdSetFilterSubhead:
		b.cmdSetFilterSubhead(p)
	case CmdGetFilterSubhead:
		b.cmdGetFilterSubhead(p)
	case CmdSetFilterMode:
		b.cmdSetFilterMode(p)
	case CmdGetFilterMode:
		b.cmdGetFilterMode(p)
	case CmdSetFilterConn:
		b.cmdSetFilterConn(p)
	case CmdGetFilterConn:
		b.cmdGetFilterConn(p)
	case CmdGetBufferSize:
		b.cmdGetBufferSize()
	case CmdGetSectorNumber:
		b.cmdGetSectorNumber(p)
	case CmdCalcActualSize:
		b.cmdCalcActualSize(p)
	case CmdGetActualSize:
		b.cmdGetActualSize()
	case CmdGetSectorInfo:
		b.cmdGetSectorInfo(p)
	case CmdExecuteFADSearch:
		b.cmdExecuteFADSearch(p)
	case CmdGetFADSearch:
		b.cmdGetFADSearch()
	case CmdSetSectorLength:
		b.cmdSetSectorLength(p)
	case CmdGetSectorData:
		b.cmdGetSectorData(p, false)
	case CmdDeleteSectorData:
		b.cmdDeleteSectorData(p)
	case CmdGetThenDelete:
		b.cmdGetSectorData(p, true)
	case CmdPutSectorData:
		// The firmware implements Put Sector Data with
		// allocation semantics; that is deferred in this
		// core, so Put is refused outright with the reject
		// encoding and nothing is stored.
		b.reject()
	default:
		b.reject()
	}
}

// cmdHardwareInfo answers with the traced CDB106 constants:
// hardware version 2 in the low byte of CR2, MPEG version 0 (no
// card) in the low byte of CR3, and zero drive version and
// revision in CR4. It answers with no disc inserted too.
func (b *Block) cmdHardwareInfo() {
	resp := b.statusResponse(b.statusCodeNow())
	resp[1] = 0x0002
	resp[2] = 0x0000
	resp[3] = 0x0000
	b.respond(resp, 0)
}

// cmdInitialize resets the selector, the buffer, the sector
// length and the drive position, and lands in standby. It always
// succeeds, with or without a disc.
func (b *Block) cmdInitialize() {
	b.resetSelector()
	b.devFilter = 0
	b.getLen = 0
	b.putLen = 0
	b.calcSize = 0
	b.searchPart = -1
	b.xfer = nil
	b.state = StatusSTANDBY
	b.fad = 150
	b.repeatsLeft = 0
	b.infinite = false
	b.respond(b.statusResponse(b.statusCodeNow()), HIRQESEL)
}

// cmdEndDataTransfer finishes an active transfer and reports
// the transferred word count across CR1 and CR2, or 0xFFFFFF
// when no transfer was in progress.
func (b *Block) cmdEndDataTransfer() {
	words := 0xFFFFFF
	if b.xfer != nil {
		// The port advances one word per Poll after a read, so
		// the byte offset counts the words the host has taken.
		words = b.xfer.pos / 2
		b.finishTransfer()
	}
	resp := [4]uint16{
		uint16(b.statusCodeNow())<<8 | uint16(byte(words>>16)),
		uint16(words & 0xFFFF),
		0, 0,
	}
	b.respond(resp, HIRKEHST)
}

// trackAtFAD finds the track owning a frame address. The second
// result reports whether the position is in that track's pregap.
func (b *Block) trackAtFAD(fad int) (cd.Track, bool, bool) {
	if b.img == nil {
		return cd.Track{}, false, false
	}
	lba := fad - 150
	for _, tr := range b.img.Tracks() {
		segStart := tr.Start - tr.Pregap
		segEnd := tr.Start + tr.Length
		if lba >= segStart && lba < segEnd {
			return tr, lba < tr.Start, true
		}
	}
	return cd.Track{}, false, false
}

// ctrlADR packs the control and ADR byte for a track: ADR 1 in
// the low nibble, control 0x4 for data tracks and 0x0 for audio
// in the high nibble.
func ctrlADR(tr cd.Track) byte {
	if tr.Mode == 0 {
		return 0x01
	}
	return 0x41
}

// lastFAD is the frame address of the final sector of the disc.
func (b *Block) lastFAD() int {
	return b.img.NumSectors() - 1 + 150
}

// cmdGetTOC loads the transfer FIFO with one 4 byte entry per
// track plus the lead-out: control/ADR, then the track start FAD
// as 24 big endian bits. CR2 of the response is the TOC size in
// words.
func (b *Block) cmdGetTOC() {
	if b.img == nil {
		b.reject()
		return
	}
	tracks := b.img.Tracks()
	data := make([]byte, 0, (len(tracks)+1)*4)
	for _, tr := range tracks {
		fad := tr.Start + 150
		data = append(data, ctrlADR(tr), byte(fad>>16), byte(fad>>8), byte(fad))
	}
	lead := b.img.NumSectors() + 150
	data = append(data, 0x41, byte(lead>>16), byte(lead>>8), byte(lead))
	b.startTransfer(data, nil)
	resp := b.statusResponse(b.statusCodeNow() | statusFlagTransfer)
	resp[1] = uint16(len(data) / 2)
	resp[2] = 0
	resp[3] = 0
	b.respond(resp, HIRQDRDY)
}

// cmdSessionInfo reports the single session of the images this
// core supports: CR3 carries the session count 1 in its high byte
// and the session start LBA (0) across its low byte and CR4.
func (b *Block) cmdSessionInfo() {
	if b.img == nil {
		b.reject()
		return
	}
	resp := b.statusResponse(b.statusCodeNow())
	resp[1] = 0
	resp[2] = 1 << 8
	resp[3] = 0
	b.respond(resp, 0)
}

// parsePosition decodes a 24 bit play or seek position: bit 23
// set means a FAD with the flag stripped, clear means a track
// number whose start is the position. The manual sentinels are
// handled by the callers before this is consulted.
func (b *Block) parsePosition(pos uint32) (int, bool) {
	if pos&0x800000 != 0 {
		return int(pos & 0x7FFFFF), true
	}
	for _, tr := range b.img.Tracks() {
		if tr.Number == int(pos) {
			return tr.Start + 150, true
		}
	}
	return 0, false
}

// cmdPlay starts playback of a disc range. The start position is
// the 24 bit value across the CR1 low byte and CR2, the play
// mode byte is the high byte of CR3, and the end position is the
// 24 bit value across the CR3 low byte and CR4.
func (b *Block) cmdPlay(p [4]uint16) {
	if b.img == nil {
		b.reject()
		return
	}
	start24 := uint32(p[0]&0xFF)<<16 | uint32(p[1])
	end24 := uint32(p[2]&0xFF)<<16 | uint32(p[3])
	mode := byte(p[2] >> 8)
	start, ok := b.parsePosition(start24)
	if !ok {
		b.reject()
		return
	}
	end := b.lastFAD()
	if end24 != 0 {
		e, ok := b.parsePosition(end24)
		if !ok {
			b.reject()
			return
		}
		if end24&0x800000 == 0 {
			// A track number as the end means the end of
			// that track, not its start.
			if tr, _, found := b.trackAtFAD(e); found {
				e = tr.Start + tr.Length - 1 + 150
			}
		}
		end = e
	}
	if end > b.lastFAD() {
		end = b.lastFAD()
	}
	if start > end {
		b.reject()
		return
	}
	if mode&0x7F != 0x7F {
		rep := mode & 0x0F
		if rep == 0x0F {
			b.infinite = true
			b.repeatsLeft = 0
		} else {
			b.infinite = false
			b.repeatsLeft = int(rep)
		}
	}
	b.playStart = start
	b.playEnd = end
	b.fad = start
	b.playAcc = 0
	b.state = StatusPLAY
	b.respond(b.statusResponse(b.statusCodeNow()), 0)
}

// cmdSeek moves the position and lands in PAUSE. A position of 0
// parks the drive in STANDBY at the disc start, and 0xFFFFFF
// pauses in place, per the firmware handlers.
func (b *Block) cmdSeek(p [4]uint16) {
	if b.img == nil {
		b.reject()
		return
	}
	pos24 := uint32(p[0]&0xFF)<<16 | uint32(p[1])
	switch {
	case pos24 == 0:
		b.state = StatusSTANDBY
		b.fad = 150
	case pos24 == 0xFFFFFF:
		b.state = StatusPAUSE
	default:
		fad, ok := b.parsePosition(pos24)
		if !ok {
			// An invalid track parks the drive, per the
			// firmware seek handler.
			b.state = StatusSTANDBY
			break
		}
		if fad > b.lastFAD() {
			fad = b.lastFAD()
		}
		b.fad = fad
		b.state = StatusPAUSE
	}
	b.respond(b.statusResponse(b.statusCodeNow()), 0)
}

// cmdScan starts a scan: the CR1 low byte selects the direction,
// 0 forward and any other value reverse.
func (b *Block) cmdScan(p [4]uint16) {
	if b.img == nil {
		b.reject()
		return
	}
	b.scanReverse = p[0]&0xFF != 0
	b.scanAcc = 0
	b.state = StatusSCAN
	b.respond(b.statusResponse(b.statusCodeNow()), 0)
}

// cmdSubcodeQ loads the transfer FIFO with the 10 byte subcode
// Q for the current position: control/ADR, track and index in
// BCD, the relative MSF in BCD, a zero byte, and the absolute
// MSF in BCD. The response carries the size in words (5) in CR2.
// The 2 CRC bytes of the on-disc Q channel are not included, so
// no CRC is computed. A CR1 low byte type other than 0 (Q) is
// rejected; the R to W subcode is not implemented.
func (b *Block) cmdSubcodeQ(p [4]uint16) {
	if b.img == nil || p[0]&0xFF != 0 {
		b.reject()
		return
	}
	tr, inPregap, ok := b.trackAtFAD(b.fad)
	if !ok {
		b.reject()
		return
	}
	index := 1
	rel := b.fad - (tr.Start + 150)
	if inPregap {
		index = 0
		rel = 0 // pregap negative time clamps at zero here
	}
	am, as, af := cd.LBAtoMSF(b.fad - 150)
	q := []byte{
		ctrlADR(tr),
		toBCD(tr.Number),
		toBCD(index),
		toBCD(rel / 4500),
		toBCD(rel / 75 % 60),
		toBCD(rel % 75),
		0,
		toBCD(am),
		toBCD(as),
		toBCD(af),
	}
	b.startTransfer(q, nil)
	resp := b.statusResponse(b.statusCodeNow() | statusFlagTransfer)
	resp[1] = 5
	resp[2] = 0
	resp[3] = 0
	b.respond(resp, HIRQDRDY)
}

// toBCD converts a value in the range 0 to 99 to binary coded
// decimal.
func toBCD(v int) byte {
	return byte((v/10)<<4 | (v % 10))
}

// filterIndex reads a filter number from the high byte of CR3
// and validates it.
func filterIndex(p [4]uint16) (int, bool) {
	n := int(p[2] >> 8)
	return n, n < NumFilters
}

// cmdSetCDConnection connects the CD device output to the filter
// named in the high byte of CR3, or disconnects it for 0xFF.
func (b *Block) cmdSetCDConnection(p [4]uint16) {
	n := int(p[2] >> 8)
	switch {
	case n == 0xFF:
		b.devFilter = -1
	case n < NumFilters:
		b.devFilter = n
	default:
		b.reject()
		return
	}
	b.respond(b.statusResponse(b.statusCodeNow()), HIRQESEL)
}

// cmdGetCDConnection reports the connected filter in the high
// byte of CR3, or 0xFF when disconnected.
func (b *Block) cmdGetCDConnection() {
	resp := b.statusResponse(b.statusCodeNow())
	resp[2] = uint16(byte(b.devFilter)) << 8
	b.respond(resp, 0)
}

// cmdSetFilterRange sets a filter's FAD range: the start FAD is
// the 24 bit value across the CR1 low byte and CR2, the filter
// number is the CR3 high byte, and the range sector count is the
// 24 bit value across the CR3 low byte and CR4. A field of
// 0xFFFFFF leaves that field unchanged.
func (b *Block) cmdSetFilterRange(p [4]uint16) {
	n, ok := filterIndex(p)
	if !ok {
		b.reject()
		return
	}
	f := &b.filters[n]
	start := uint32(p[0]&0xFF)<<16 | uint32(p[1])
	count := uint32(p[2]&0xFF)<<16 | uint32(p[3])
	if start != 0xFFFFFF {
		f.startFAD = int(start)
	}
	if count != 0xFFFFFF {
		f.rangeCnt = int(count)
	}
	b.respond(b.statusResponse(b.statusCodeNow()), HIRQESEL)
}

// cmdGetFilterRange answers in the Set Filter Range layout with
// the status byte in place of the command byte.
func (b *Block) cmdGetFilterRange(p [4]uint16) {
	n, ok := filterIndex(p)
	if !ok {
		b.reject()
		return
	}
	f := b.filters[n]
	resp := b.statusResponse(b.statusCodeNow())
	resp[0] = uint16(b.statusCodeNow())<<8 | uint16(byte(f.startFAD>>16))
	resp[1] = uint16(f.startFAD & 0xFFFF)
	resp[2] = uint16(n)<<8 | uint16(byte(f.rangeCnt>>16))
	resp[3] = uint16(f.rangeCnt & 0xFFFF)
	b.respond(resp, HIRQESEL)
}

// cmdSetFilterSubhead sets a filter's subheader conditions: the
// CR1 low byte is the channel number, CR2 packs the submode mask
// (high) and coding information mask (low), CR3 packs the filter
// number (high) and file number (low), and CR4 packs the submode
// value (high) and coding information value (low).
func (b *Block) cmdSetFilterSubhead(p [4]uint16) {
	n, ok := filterIndex(p)
	if !ok {
		b.reject()
		return
	}
	f := &b.filters[n]
	f.cn = byte(p[0])
	f.smMask = byte(p[1] >> 8)
	f.ciMask = byte(p[1])
	f.fn = byte(p[2])
	f.smVal = byte(p[3] >> 8)
	f.ciVal = byte(p[3])
	b.respond(b.statusResponse(b.statusCodeNow()), HIRQESEL)
}

// cmdGetFilterSubhead answers in the Set layout: CR1 carries the
// status and channel number, CR2 the masks, CR3 the file number
// in its low byte, and CR4 the values.
func (b *Block) cmdGetFilterSubhead(p [4]uint16) {
	n, ok := filterIndex(p)
	if !ok {
		b.reject()
		return
	}
	f := b.filters[n]
	resp := [4]uint16{
		uint16(b.statusCodeNow())<<8 | uint16(f.cn),
		uint16(f.smMask)<<8 | uint16(f.ciMask),
		uint16(f.fn),
		uint16(f.smVal)<<8 | uint16(f.ciVal),
	}
	b.respond(resp, HIRQESEL)
}

// cmdSetFilterMode sets a filter's mode byte from the CR1 low
// byte. The Init bit resets the filter's range, subheader
// conditions and mode to defaults and ignores the other bits;
// connections are not touched.
func (b *Block) cmdSetFilterMode(p [4]uint16) {
	n, ok := filterIndex(p)
	if !ok {
		b.reject()
		return
	}
	mode := byte(p[0])
	if mode&FilterModeInit != 0 {
		connT, connF := b.filters[n].trueConn, b.filters[n].falseConn
		b.filters[n] = filter{trueConn: connT, falseConn: connF}
	} else {
		b.filters[n].mode = mode
	}
	b.respond(b.statusResponse(b.statusCodeNow()), HIRQESEL)
}

// cmdGetFilterMode answers with the mode in the CR1 low byte.
func (b *Block) cmdGetFilterMode(p [4]uint16) {
	n, ok := filterIndex(p)
	if !ok {
		b.reject()
		return
	}
	resp := b.statusResponse(b.statusCodeNow())
	resp[0] = uint16(b.statusCodeNow())<<8 | uint16(b.filters[n].mode)
	b.respond(resp, HIRQESEL)
}

// cmdSetFilterConn sets a filter's output connections. The CR1
// low byte selects which connections change (bit 0 true, bit 1
// false), CR2 packs the true target (high) and false target
// (low), and CR3 carries the filter number in its high byte.
// Targets use the unified encoding: partition 0 to 23, ConnNext,
// or ConnDiscard.
func (b *Block) cmdSetFilterConn(p [4]uint16) {
	n, ok := filterIndex(p)
	if !ok {
		b.reject()
		return
	}
	which := byte(p[0])
	trueT, falseT := byte(p[1]>>8), byte(p[1])
	valid := func(t byte) bool { return t < NumPartitions || t == ConnNext || t == ConnDiscard }
	if which&1 != 0 && !valid(trueT) || which&2 != 0 && !valid(falseT) {
		b.reject()
		return
	}
	if which&1 != 0 {
		b.filters[n].trueConn = trueT
	}
	if which&2 != 0 {
		b.filters[n].falseConn = falseT
	}
	b.respond(b.statusResponse(b.statusCodeNow()), HIRQESEL)
}

// cmdGetFilterConn answers with both targets packed in CR2.
func (b *Block) cmdGetFilterConn(p [4]uint16) {
	n, ok := filterIndex(p)
	if !ok {
		b.reject()
		return
	}
	f := b.filters[n]
	resp := b.statusResponse(b.statusCodeNow())
	resp[1] = uint16(f.trueConn)<<8 | uint16(f.falseConn)
	resp[2] = 0
	resp[3] = 0
	b.respond(resp, HIRQESEL)
}

// partitionIndex reads a partition number from the high byte of
// CR3 and validates it.
func partitionIndex(p [4]uint16) (int, bool) {
	n := int(p[2] >> 8)
	return n, n < NumPartitions
}

// totalBlocks counts every buffered block across all partitions.
func (b *Block) totalBlocks() int {
	n := 0
	for i := range b.parts {
		n += len(b.parts[i])
	}
	return n
}

// sectorRange resolves a sector command's offset and count
// against a partition: offset 0xFFFF means the last sector,
// count 0 means one sector, and count 0xFFFF means to the end.
// An overrunning count clamps to the end of the partition.
func sectorRange(entries []block, off, count int) (int, int, bool) {
	if len(entries) == 0 {
		return 0, 0, false
	}
	if off == 0xFFFF {
		off = len(entries) - 1
	}
	if off < 0 || off >= len(entries) {
		return 0, 0, false
	}
	if count == 0 {
		count = 1
	}
	if count == 0xFFFF || off+count > len(entries) {
		count = len(entries) - off
	}
	return off, count, true
}

// cmdGetBufferSize reports the buffer totals in the traced
// firmware layout: CR1 carries the standard status word, CR2
// the free block count, CR3 the maximum selector count (24) in
// its high byte, and CR4 the total block count (200). It
// answers with the standard status responder and HIRQ ESEL,
// like the other buffer commands in this group.
func (b *Block) cmdGetBufferSize() {
	resp := b.statusResponse(b.statusCodeNow())
	resp[1] = uint16(BufferBlocks - b.totalBlocks())
	resp[2] = uint16(NumFilters) << 8
	resp[3] = uint16(BufferBlocks)
	b.respond(resp, HIRQESEL)
}

// cmdGetSectorNumber reports a partition's block count in CR4.
// The partition number is the high byte of CR3.
func (b *Block) cmdGetSectorNumber(p [4]uint16) {
	part, ok := partitionIndex(p)
	if !ok {
		b.reject()
		return
	}
	resp := b.statusResponse(b.statusCodeNow())
	resp[1] = 0
	resp[2] = 0
	resp[3] = uint16(len(b.parts[part]))
	b.respond(resp, 0)
}

// cmdCalcActualSize totals the byte size, at the current read
// sector length, of the range named by CR2 (sector offset), CR3
// (partition in the high byte) and CR4 (sector count). The
// result is read back with Get Actual Data Size.
func (b *Block) cmdCalcActualSize(p [4]uint16) {
	part, ok := partitionIndex(p)
	if !ok {
		b.reject()
		return
	}
	off, count, ok := sectorRange(b.parts[part], int(p[1]), int(p[3]))
	if !ok {
		b.wait()
		return
	}
	total := 0
	for _, e := range b.parts[part][off : off+count] {
		total += len(b.sectorBytes(&e))
	}
	b.calcSize = total
	b.respond(b.statusResponse(b.statusCodeNow()), HIRQESEL)
}

// cmdGetActualSize reports the calculated size in words as a 24
// bit value across the CR1 low byte and CR2.
func (b *Block) cmdGetActualSize() {
	words := b.calcSize / 2
	resp := b.statusResponse(b.statusCodeNow())
	resp[0] = uint16(b.statusCodeNow())<<8 | uint16(byte(words>>16))
	resp[1] = uint16(words & 0xFFFF)
	resp[2] = 0
	resp[3] = 0
	b.respond(resp, HIRQESEL)
}

// cmdGetSectorInfo reports one buffered block's descriptor: CR1
// carries the status and the FAD high byte, CR2 the FAD low word,
// CR3 the file and channel numbers, and CR4 the submode and
// coding information. Parameters are CR2 (sector position, with
// 0xFFFF the last sector) and CR3 (partition in the high byte).
func (b *Block) cmdGetSectorInfo(p [4]uint16) {
	part, ok := partitionIndex(p)
	if !ok {
		b.reject()
		return
	}
	entries := b.parts[part]
	pos := int(p[1])
	if pos == 0xFFFF && len(entries) > 0 {
		pos = len(entries) - 1
	}
	if pos < 0 || pos >= len(entries) {
		b.wait()
		return
	}
	e := entries[pos]
	resp := [4]uint16{
		uint16(b.statusCodeNow())<<8 | uint16(byte(e.fad>>16)),
		uint16(e.fad & 0xFFFF),
		uint16(e.fn)<<8 | uint16(e.cn),
		uint16(e.sm)<<8 | uint16(e.ci),
	}
	b.respond(resp, 0)
}

// cmdExecuteFADSearch searches a partition for a FAD: an exact
// match wins, otherwise the nearest rule picks the first block
// at or after the target, or the last block when every block is
// before it. Parameters are CR2 (start sector position), CR3
// (partition high byte, FAD high byte low) and CR4 (FAD low).
func (b *Block) cmdExecuteFADSearch(p [4]uint16) {
	part, ok := partitionIndex(p)
	if !ok {
		b.reject()
		return
	}
	entries := b.parts[part]
	if len(entries) == 0 {
		b.wait()
		return
	}
	target := int(p[2]&0xFF)<<16 | int(p[3])
	start := int(p[1])
	if start == 0xFFFF || start > len(entries) {
		start = 0
	}
	best := -1
	for i := start; i < len(entries); i++ {
		if entries[i].fad == target {
			best = i
			break
		}
		if entries[i].fad > target && best < 0 {
			best = i
		}
	}
	if best < 0 {
		best = len(entries) - 1
	}
	b.searchPart = part
	b.searchPos = best
	b.searchFAD = entries[best].fad
	b.respond(b.statusResponse(b.statusCodeNow()), HIRQESEL)
}

// cmdGetFADSearch reports the stored search result: CR2 is the
// sector position, CR3 packs the partition (high) and the found
// FAD high byte (low), and CR4 is the FAD low word.
func (b *Block) cmdGetFADSearch() {
	if b.searchPart < 0 {
		b.wait()
		return
	}
	resp := b.statusResponse(b.statusCodeNow())
	resp[1] = uint16(b.searchPos)
	resp[2] = uint16(b.searchPart)<<8 | uint16(byte(b.searchFAD>>16))
	resp[3] = uint16(b.searchFAD & 0xFFFF)
	b.respond(resp, 0)
}

// cmdSetSectorLength sets the read (get) sector length type
// from the CR1 low byte and the write (put) type from the CR2
// high byte; a field of 0xFF leaves that direction unchanged.
// Types are 0 for 2048 bytes, 1 for 2336, 2 for 2340 and 3 for
// the full 2352 byte sector.
func (b *Block) cmdSetSectorLength(p [4]uint16) {
	getT := byte(p[0])
	putT := byte(p[1] >> 8)
	if getT != 0xFF && getT > 3 || putT != 0xFF && putT > 3 {
		b.reject()
		return
	}
	if getT != 0xFF {
		b.getLen = int(getT)
	}
	if putT != 0xFF {
		b.putLen = int(putT)
	}
	b.respond(b.statusResponse(b.statusCodeNow()), HIRQESEL)
}

// sectorBytes extracts a block's bytes at the current read
// sector length. A payload absent block (restored from a save
// state) re-reads its raw sector from the image by FAD at this
// moment; with no image the extracted length reads as zeros.
func (b *Block) sectorBytes(e *block) []byte {
	raw := e.payload
	if raw == nil {
		if b.img == nil {
			return make([]byte, sectorLengthBytes[b.getLen])
		}
		r, err := b.img.ReadRawSector(e.fad - 150)
		if err != nil {
			return make([]byte, sectorLengthBytes[b.getLen])
		}
		raw = r
	}
	return extractSector(raw, b.getLen)
}

// extractSector pulls the byte range for a sector length type
// out of a raw sector. A 2048 byte payload has no header, so it
// is returned as is for type 0 and zero padded for the longer
// types.
func extractSector(raw []byte, lenType int) []byte {
	want := sectorLengthBytes[lenType]
	if len(raw) == 2048 {
		out := make([]byte, want)
		copy(out, raw)
		return out
	}
	if len(raw) < 2352 {
		out := make([]byte, want)
		copy(out, raw)
		return out
	}
	switch lenType {
	case 0:
		if raw[15] == 2 {
			// Mode 2, Form 1 and Form 2 alike: the user
			// area starts after the subheader. Form 2
			// reads return its first 2048 bytes here.
			return append([]byte(nil), raw[24:2072]...)
		}
		return append([]byte(nil), raw[16:2064]...)
	case 1:
		return append([]byte(nil), raw[16:2352]...)
	case 2:
		return append([]byte(nil), raw[12:2352]...)
	default:
		return append([]byte(nil), raw[:2352]...)
	}
}

// cmdGetSectorData loads the FIFO with the extracted bytes of
// the range named by CR2 (sector offset), CR3 (partition in the
// high byte) and CR4 (sector count), and sets DRDY. When del is
// set the range is deleted once the transfer finishes.
func (b *Block) cmdGetSectorData(p [4]uint16, del bool) {
	part, ok := partitionIndex(p)
	if !ok {
		b.reject()
		return
	}
	off, count, ok := sectorRange(b.parts[part], int(p[1]), int(p[3]))
	if !ok {
		b.wait()
		return
	}
	var data []byte
	for i := range b.parts[part][off : off+count] {
		e := &b.parts[part][off+i]
		data = append(data, b.sectorBytes(e)...)
	}
	var delInfo *transfer
	if del {
		delInfo = &transfer{deletePart: part, deleteOff: off, deleteCount: count}
	}
	b.startTransfer(data, delInfo)
	b.respond(b.statusResponse(b.statusCodeNow()|statusFlagTransfer), HIRQDRDY)
}

// deleteRange removes count blocks at off from a partition.
func (b *Block) deleteRange(part, off, count int) {
	entries := b.parts[part]
	if off < 0 || off >= len(entries) {
		return
	}
	if off+count > len(entries) {
		count = len(entries) - off
	}
	b.parts[part] = append(entries[:off], entries[off+count:]...)
}

// cmdDeleteSectorData deletes the range named like Get Sector
// Data and sets EHST.
func (b *Block) cmdDeleteSectorData(p [4]uint16) {
	part, ok := partitionIndex(p)
	if !ok {
		b.reject()
		return
	}
	off, count, ok := sectorRange(b.parts[part], int(p[1]), int(p[3]))
	if !ok {
		b.wait()
		return
	}
	b.deleteRange(part, off, count)
	b.respond(b.statusResponse(b.statusCodeNow()), HIRKEHST)
}

// Advance moves streaming by machine cycles at 28.6364 MHz. In
// PLAY one sector streams per 1/75 second of cycles; in SCAN the
// position moves at 16 times that rate and nothing is buffered
// or sent to the audio sink.
func (b *Block) Advance(cycles int) {
	if !b.power || cycles <= 0 || b.img == nil {
		return
	}
	switch b.state {
	case StatusPLAY:
		b.playAcc += int64(cycles) * sectorsPerSec
		for b.playAcc >= cyclesPerSecond && b.state == StatusPLAY {
			b.playAcc -= cyclesPerSecond
			b.streamPlaySector()
		}
	case StatusSCAN:
		b.scanAcc += int64(cycles) * sectorsPerSec * scanMultiplier
		for b.scanAcc >= cyclesPerSecond && b.state == StatusSCAN {
			b.scanAcc -= cyclesPerSecond
			b.stepScan()
		}
	}
}

// streamPlaySector processes the sector at the play position and
// advances: audio sectors go to the sink as interleaved stereo
// samples, data sectors go through the filter chain. At the end
// of the range a repeat restarts it, or playback finishes into
// PAUSE with PEND set.
func (b *Block) streamPlaySector() {
	if b.fad > b.playEnd {
		b.finishPlay()
		return
	}
	tr, _, ok := b.trackAtFAD(b.fad)
	if !ok {
		b.finishPlay()
		return
	}
	lba := b.fad - 150
	if tr.Mode == 0 {
		if raw, err := b.img.ReadRawSector(lba); err == nil && b.sink != nil {
			samples := make([]int16, len(raw)/2)
			for i := range samples {
				samples[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
			}
			b.sink(samples)
		}
	} else if raw, err := b.img.ReadRawSector(lba); err == nil {
		blk := block{fad: b.fad, payload: append([]byte(nil), raw...)}
		if len(raw) >= 24 && raw[15] == 2 {
			blk.fn = raw[16]
			blk.cn = raw[17]
			blk.sm = raw[18]
			blk.ci = raw[19]
		}
		b.routeSector(&blk)
	}
	b.setHIRQ(HIRQSCDQ)
	b.fad++
	if b.fad > b.playEnd {
		switch {
		case b.infinite:
			b.fad = b.playStart
		case b.repeatsLeft > 0:
			b.repeatsLeft--
			b.fad = b.playStart
		default:
			b.finishPlay()
		}
	}
}

// finishPlay ends playback: the state lands in PAUSE and PEND
// sets.
func (b *Block) finishPlay() {
	b.state = StatusPAUSE
	b.setHIRQ(HIRQPEND)
}

// routeSector walks the filter chain for one data sector,
// starting at the connected CD device filter. A partition target
// stores the sector when the pool has room; a full pool raises
// BFUL and drops the sector. Filter loops are cut after a full
// pass through every filter.
func (b *Block) routeSector(blk *block) {
	if b.devFilter < 0 {
		return
	}
	f := b.devFilter
	for steps := 0; steps < NumFilters+1; steps++ {
		flt := &b.filters[f]
		target := flt.falseConn
		if flt.matches(blk) {
			target = flt.trueConn
		}
		switch {
		case target == ConnDiscard:
			return
		case target == ConnNext:
			f++
			if f >= NumFilters {
				return
			}
		case target < NumPartitions:
			if b.totalBlocks() >= BufferBlocks {
				b.setHIRQ(HIRQBFUL)
				return
			}
			b.parts[target] = append(b.parts[target], *blk)
			b.setHIRQ(HIRQCSCT)
			return
		default:
			return
		}
	}
}

// stepScan moves the scan position one sector and stops into
// PAUSE with PEND at the disc edges.
func (b *Block) stepScan() {
	if b.scanReverse {
		b.fad--
		if b.fad < 150 {
			b.fad = 150
			b.state = StatusPAUSE
			b.setHIRQ(HIRQPEND)
			return
		}
	} else {
		b.fad++
		if b.fad > b.lastFAD() {
			b.fad = b.lastFAD()
			b.state = StatusPAUSE
			b.setHIRQ(HIRQPEND)
			return
		}
	}
	b.setHIRQ(HIRQSCDQ)
}

// Serialized layout (big endian):
//
//	byte 0: version (1)
//	byte 1: flags: bit 0 power, bit 1 endless repeat,
//	  bit 2 scan reverse
//	byte 2: drive state status code
//	byte 3: device filter plus 1 (0 means disconnected)
//	byte 4: read sector length type
//	byte 5: write sector length type
//	byte 6: last FAD search partition plus 1 (0 means none)
//	byte 7: zero
//	bytes 8 to 11: current FAD
//	bytes 12 to 15: play start FAD
//	bytes 16 to 19: play end FAD
//	bytes 20 to 23: repeats left
//	bytes 24 to 27: calculated actual size in bytes
//	bytes 28 to 31: last FAD search sector position
//	bytes 32 to 35: last FAD search FAD
//	bytes 36 to 43: play cycle accumulator
//	bytes 44 to 51: scan cycle accumulator
//	then 24 filter records of 16 bytes: start FAD (3 bytes),
//	range count (3 bytes), mode, file number, channel number,
//	submode mask, submode value, coding mask, coding value,
//	true connection, false connection, 1 zero byte
//	then the buffered block count as 2 bytes
//	then one 9 byte descriptor per block: partition, FAD
//	(4 bytes), file number, channel number, submode, coding
//	information. Payloads are not stored; see sectorBytes.
const stateVersion = 1

const stateHeader = 52
const filterRecord = 16
const blockRecord = 9

// stateFiltersEnd is the offset of the block count field.
const stateFiltersEnd = stateHeader + NumFilters*filterRecord

// Serialize writes the block state in the layout above. The
// register page itself belongs to the bus and is saved by the
// system state, not duplicated here.
func (b *Block) Serialize() []byte {
	total := b.totalBlocks()
	out := make([]byte, stateFiltersEnd+2+total*blockRecord)
	out[0] = stateVersion
	if b.power {
		out[1] |= 1 << 0
	}
	if b.infinite {
		out[1] |= 1 << 1
	}
	if b.scanReverse {
		out[1] |= 1 << 2
	}
	out[2] = b.state
	out[3] = byte(b.devFilter + 1)
	out[4] = byte(b.getLen)
	out[5] = byte(b.putLen)
	out[6] = byte(b.searchPart + 1)
	binary.BigEndian.PutUint32(out[8:12], uint32(b.fad))
	binary.BigEndian.PutUint32(out[12:16], uint32(b.playStart))
	binary.BigEndian.PutUint32(out[16:20], uint32(b.playEnd))
	binary.BigEndian.PutUint32(out[20:24], uint32(int32(b.repeatsLeft)))
	binary.BigEndian.PutUint32(out[24:28], uint32(b.calcSize))
	binary.BigEndian.PutUint32(out[28:32], uint32(b.searchPos))
	binary.BigEndian.PutUint32(out[32:36], uint32(b.searchFAD))
	binary.BigEndian.PutUint64(out[36:44], uint64(b.playAcc))
	binary.BigEndian.PutUint64(out[44:52], uint64(b.scanAcc))
	for i, f := range b.filters {
		off := stateHeader + i*filterRecord
		putUint24(out[off:off+3], f.startFAD)
		putUint24(out[off+3:off+6], f.rangeCnt)
		out[off+6] = f.mode
		out[off+7] = f.fn
		out[off+8] = f.cn
		out[off+9] = f.smMask
		out[off+10] = f.smVal
		out[off+11] = f.ciMask
		out[off+12] = f.ciVal
		out[off+13] = f.trueConn
		out[off+14] = f.falseConn
	}
	binary.BigEndian.PutUint16(out[stateFiltersEnd:stateFiltersEnd+2], uint16(total))
	off := stateFiltersEnd + 2
	for part := range b.parts {
		for _, e := range b.parts[part] {
			out[off] = byte(part)
			binary.BigEndian.PutUint32(out[off+1:off+5], uint32(e.fad))
			out[off+5] = e.fn
			out[off+6] = e.cn
			out[off+7] = e.sm
			out[off+8] = e.ci
			off += blockRecord
		}
	}
	return out
}

// Deserialize restores a state produced by Serialize. Restored
// blocks carry descriptors only, with no payloads; after a
// SetImage the sector bytes are re-read from the image by FAD.
// After a restore the handshake baseline resyncs to the page, so
// a state saved mid command does not re-execute it. Any active
// transfer is dropped.
func (b *Block) Deserialize(data []byte) error {
	if len(data) < stateFiltersEnd+2 {
		return fmt.Errorf("cdblock: state size %d, want at least %d", len(data), stateFiltersEnd+2)
	}
	if data[0] != stateVersion {
		return fmt.Errorf("cdblock: state version %d, want %d", data[0], stateVersion)
	}
	count := int(binary.BigEndian.Uint16(data[stateFiltersEnd : stateFiltersEnd+2]))
	want := stateFiltersEnd + 2 + count*blockRecord
	if len(data) != want {
		return fmt.Errorf("cdblock: state size %d, want %d for %d blocks", len(data), want, count)
	}
	b.power = data[1]&(1<<0) != 0
	b.infinite = data[1]&(1<<1) != 0
	b.scanReverse = data[1]&(1<<2) != 0
	b.state = data[2]
	b.devFilter = int(data[3]) - 1
	b.getLen = int(data[4])
	b.putLen = int(data[5])
	b.searchPart = int(data[6]) - 1
	b.fad = int(binary.BigEndian.Uint32(data[8:12]))
	b.playStart = int(binary.BigEndian.Uint32(data[12:16]))
	b.playEnd = int(binary.BigEndian.Uint32(data[16:20]))
	b.repeatsLeft = int(int32(binary.BigEndian.Uint32(data[20:24])))
	b.calcSize = int(binary.BigEndian.Uint32(data[24:28]))
	b.searchPos = int(binary.BigEndian.Uint32(data[28:32]))
	b.searchFAD = int(binary.BigEndian.Uint32(data[32:36]))
	b.playAcc = int64(binary.BigEndian.Uint64(data[36:44]))
	b.scanAcc = int64(binary.BigEndian.Uint64(data[44:52]))
	for i := range b.filters {
		off := stateHeader + i*filterRecord
		b.filters[i] = filter{
			startFAD:  uint24(data[off : off+3]),
			rangeCnt:  uint24(data[off+3 : off+6]),
			mode:      data[off+6],
			fn:        data[off+7],
			cn:        data[off+8],
			smMask:    data[off+9],
			smVal:     data[off+10],
			ciMask:    data[off+11],
			ciVal:     data[off+12],
			trueConn:  data[off+13],
			falseConn: data[off+14],
		}
	}
	for i := range b.parts {
		b.parts[i] = nil
	}
	off := stateFiltersEnd + 2
	for n := 0; n < count; n++ {
		rec := data[off : off+blockRecord]
		part := int(rec[0])
		if part >= NumPartitions {
			return fmt.Errorf("cdblock: block %d has partition %d, want under %d", n, part, NumPartitions)
		}
		b.parts[part] = append(b.parts[part], block{
			fad: int(binary.BigEndian.Uint32(rec[1:5])),
			fn:  rec[5],
			cn:  rec[6],
			sm:  rec[7],
			ci:  rec[8],
		})
		off += blockRecord
	}
	b.xfer = nil
	b.lastCmd = b.crTuple()
	b.lastCmdSeen = true
	return nil
}

// putUint24 writes the low 24 bits of v into 3 big endian bytes.
func putUint24(dst []byte, v int) {
	dst[0] = byte(v >> 16)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v)
}

// uint24 reads a 24 bit big endian value.
func uint24(src []byte) int {
	return int(src[0])<<16 | int(src[1])<<8 | int(src[2])
}
