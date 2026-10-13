package scd

import "encoding/binary"

// This file implements the Sega CD sub CPU BIOS call services
// (SCD-4 part 1): CDBIOS (CD drive and CDC functions), BURAM
// (backup RAM file system), and CDBOOT (disc boot checks). The
// implementations live here in Go. On the 68k side a game calls a
// jump table entry in the stub image (stub.go); the entry lands
// on a family trampoline that saves D0-D7/A0-A6 into the mailbox
// register image in PRG-RAM, stores a family byte, sets the
// request byte last, and spins until the Go service clears the
// request byte. The system calls BIOSService once per scanline.
//
// Mailbox layout (absolute sub CPU addresses in PRG-RAM):
//
//	0x25A00 request byte (0 idle, 1 pending)
//	0x25A01 family byte (0 cdbios, 1 buram, 2 cdboot)
//	0x25A04 register image: 15 longs (D0..D7, A0..A6)
//	0x25A40 result block: 4 longs (D0, D1, A0, A1)
//	0x25A50 result CCR word (low byte is the CCR the caller sees)
//
// The function number is D0.w of the register image. Arguments
// and caller memory (parameter tables, buffers) are read and
// written through the unit bus (Read8 and Write8). The default
// result copies the image registers unchanged with carry clear;
// the failure default is carry set with D0 = 0.
//
// CDBIOS function split: function numbers at or above 0x80 are
// synchronous and run in the service pass that sees the request.
// Numbers below 0x80 are asynchronous drive commands: the call
// returns at once with registers unchanged and carry clear, the
// (function, arguments) pair enters a one deep drive queue, and
// BIOSService executes the queued command once it has been
// queued for at least 223 service calls (one 75 Hz tick at one
// call per scanline). CDBCHK reports the queue as busy meanwhile.

// BRAMSize is the size in bytes of the backup RAM store.
const BRAMSize = 8192

// BIOSStateSize is the exact serialized size of the BIOS state
// block appended to a serialized Unit after the backup RAM.
const BIOSStateSize = 768

// biosState is the persistent BIOS service state. Its serialized
// layout inside the BIOSStateSize block (all multi-byte values
// big-endian) is:
//
//	0        flags: bit0 drive queue valid, bit1 drive
//	         initialized, bit2 read active, bit3 sector
//	         prepared, bit4 read target valid, bit5 a CDCREAD
//	         header has been recorded
//	1        queued function number
//	2..3     queued call age in BIOSService calls (saturated)
//	4..11    queued argument bytes (up to 8 bytes from (A0))
//	12..13   standby pause time (CDBPAUSE)
//	14..15   LED mode (LEDSET)
//	16..17   fader volume
//	18..19   fader master volume
//	20..21   fader ramp target volume (FDRCHG)
//	22..23   fader ramp speed (0 means no ramp)
//	24..25   fader ramp tick counter within the 223 call tick
//	26       CDC destination mode byte (CDCSETMODE)
//	27       TOC override entry count
//	28..31   read state: next expected LBA
//	32..35   read state: remaining sector count (-1 unbounded)
//	36..39   read target LBA (ROMSEEK, used by CDCSTART)
//	40..43   prepared sector LBA (CDCREAD)
//	44..47   last CDCREAD header long
//	48..443  TOC override: up to 99 entries of 4 bytes each
//	444..539 jump table overlay bytes for 0x5EE0..0x5F3F
//	540..767 reserved, zero
type biosState struct {
	queueValid      bool
	queueFn         uint8
	queueAge        int
	queueArgs       [8]byte
	driveInit       bool
	standbyPause    uint16
	ledMode         uint16
	faderVol        uint16
	faderMaster     uint16
	faderTarget     uint16
	faderSpeed      uint16
	faderTick       int
	cdcDestMode     uint8
	readActive      bool
	readPrepared    bool
	readTargetValid bool
	readNextLBA     uint32
	readRemaining   int32
	readTargetLBA   uint32
	preparedLBA     uint32
	lastHeader      uint32
	hasHeader       bool
	tocCount        uint8
	tocEntries      [99][4]byte
}

// initBIOSState puts the BIOS state at its power-on defaults:
// fader volume and master at unity (0x0400), the read count
// unbounded, and the jump table overlay copied from the stub
// image jump table region.
func (u *Unit) initBIOSState() {
	u.bios = biosState{
		faderVol:      0x0400,
		faderMaster:   0x0400,
		readRemaining: -1,
	}
	img := u.stub
	if len(img) < JumpTableBase+jumpTableSize {
		img = BootStubImage()
	}
	copy(u.jumpOverlay[:], img[JumpTableBase:JumpTableBase+jumpTableSize])
}

// serializeBIOSState writes the BIOS state block (BIOSStateSize
// bytes) into buf, which must be at least that long.
func (u *Unit) serializeBIOSState(buf []byte) {
	for i := 0; i < BIOSStateSize; i++ {
		buf[i] = 0
	}
	b := &u.bios
	var flags uint8
	if b.queueValid {
		flags |= 1 << 0
	}
	if b.driveInit {
		flags |= 1 << 1
	}
	if b.readActive {
		flags |= 1 << 2
	}
	if b.readPrepared {
		flags |= 1 << 3
	}
	if b.readTargetValid {
		flags |= 1 << 4
	}
	if b.hasHeader {
		flags |= 1 << 5
	}
	buf[0] = flags
	buf[1] = b.queueFn
	age := b.queueAge
	if age > 0xFFFF {
		age = 0xFFFF
	}
	if age < 0 {
		age = 0
	}
	binary.BigEndian.PutUint16(buf[2:4], uint16(age))
	copy(buf[4:12], b.queueArgs[:])
	binary.BigEndian.PutUint16(buf[12:14], b.standbyPause)
	binary.BigEndian.PutUint16(buf[14:16], b.ledMode)
	binary.BigEndian.PutUint16(buf[16:18], b.faderVol)
	binary.BigEndian.PutUint16(buf[18:20], b.faderMaster)
	binary.BigEndian.PutUint16(buf[20:22], b.faderTarget)
	binary.BigEndian.PutUint16(buf[22:24], b.faderSpeed)
	tick := b.faderTick
	if tick > 0xFFFF {
		tick = 0xFFFF
	}
	if tick < 0 {
		tick = 0
	}
	binary.BigEndian.PutUint16(buf[24:26], uint16(tick))
	buf[26] = b.cdcDestMode
	buf[27] = b.tocCount
	binary.BigEndian.PutUint32(buf[28:32], b.readNextLBA)
	binary.BigEndian.PutUint32(buf[32:36], uint32(b.readRemaining))
	binary.BigEndian.PutUint32(buf[36:40], b.readTargetLBA)
	binary.BigEndian.PutUint32(buf[40:44], b.preparedLBA)
	binary.BigEndian.PutUint32(buf[44:48], b.lastHeader)
	for i := 0; i < 99; i++ {
		copy(buf[48+i*4:52+i*4], b.tocEntries[i][:])
	}
	copy(buf[444:444+jumpTableSize], u.jumpOverlay[:])
}

// deserializeBIOSState restores the BIOS state block written by
// serializeBIOSState, including the jump table overlay.
func (u *Unit) deserializeBIOSState(buf []byte) {
	b := &u.bios
	*b = biosState{}
	flags := buf[0]
	b.queueValid = flags&(1<<0) != 0
	b.driveInit = flags&(1<<1) != 0
	b.readActive = flags&(1<<2) != 0
	b.readPrepared = flags&(1<<3) != 0
	b.readTargetValid = flags&(1<<4) != 0
	b.hasHeader = flags&(1<<5) != 0
	b.queueFn = buf[1]
	b.queueAge = int(binary.BigEndian.Uint16(buf[2:4]))
	copy(b.queueArgs[:], buf[4:12])
	b.standbyPause = binary.BigEndian.Uint16(buf[12:14])
	b.ledMode = binary.BigEndian.Uint16(buf[14:16])
	b.faderVol = binary.BigEndian.Uint16(buf[16:18])
	b.faderMaster = binary.BigEndian.Uint16(buf[18:20])
	b.faderTarget = binary.BigEndian.Uint16(buf[20:22])
	b.faderSpeed = binary.BigEndian.Uint16(buf[22:24])
	b.faderTick = int(binary.BigEndian.Uint16(buf[24:26]))
	b.cdcDestMode = buf[26]
	b.tocCount = buf[27]
	if b.tocCount > 99 {
		b.tocCount = 99
	}
	b.readNextLBA = binary.BigEndian.Uint32(buf[28:32])
	b.readRemaining = int32(binary.BigEndian.Uint32(buf[32:36]))
	b.readTargetLBA = binary.BigEndian.Uint32(buf[36:40])
	b.preparedLBA = binary.BigEndian.Uint32(buf[40:44])
	b.lastHeader = binary.BigEndian.Uint32(buf[44:48])
	for i := 0; i < 99; i++ {
		copy(b.tocEntries[i][:], buf[48+i*4:52+i*4])
	}
	copy(u.jumpOverlay[:], buf[444:444+jumpTableSize])
}

// biosResult is the result block of one mailbox call: the four
// longs the trampoline restores plus the carry flag for the CCR
// word. It starts as a copy of the register image values, which
// is the default result for every service.
type biosResult struct {
	d0, d1, a0, a1 uint32
	carry          bool
}

// fail applies the failure default: carry set and D0 = 0. The
// other result registers keep their image values.
func (r *biosResult) fail() {
	r.carry = true
	r.d0 = 0
}

// setD0Word replaces the low word of the D0 result.
func (r *biosResult) setD0Word(v uint16) {
	r.d0 = (r.d0 & 0xFFFF0000) | uint32(v)
}

// setD1Byte replaces the low byte of the D1 result.
func (r *biosResult) setD1Byte(v uint8) {
	r.d1 = (r.d1 & 0xFFFFFF00) | uint32(v)
}

// BIOSPending reports whether BIOSService has work: a mailbox
// request byte is set, or the asynchronous drive queue is
// non-empty.
func (u *Unit) BIOSPending() bool {
	if u.bios.queueValid {
		return true
	}
	return u.Read8(mbRequestAddr) != 0
}

// BIOSService runs one BIOS service pass. The system calls it
// once per scanline. Each pass first advances the fader ramp by
// one call (a 75 Hz ramp tick is 223 calls), then completes the
// queued asynchronous drive command once it has been queued for
// at least 223 calls, then services one pending mailbox
// request.
func (u *Unit) BIOSService() {
	b := &u.bios
	if b.faderSpeed != 0 && b.faderVol != b.faderTarget {
		b.faderTick++
		if b.faderTick >= 223 {
			b.faderTick = 0
			vol := int(b.faderVol)
			target := int(b.faderTarget)
			speed := int(b.faderSpeed)
			if vol < target {
				vol += speed
				if vol > target {
					vol = target
				}
			} else {
				vol -= speed
				if vol < target {
					vol = target
				}
			}
			b.faderVol = uint16(vol)
			u.applyFader()
		}
	}
	if b.queueValid {
		b.queueAge++
		if b.queueAge >= 223 {
			fn := b.queueFn
			args := b.queueArgs
			b.queueValid = false
			b.queueAge = 0
			u.executeQueued(fn, args)
		}
	}
	if u.Read8(mbRequestAddr) != 0 {
		u.serviceMailbox()
	}
}

// serviceMailbox services one pending mailbox request: it reads
// the family byte and the register image, dispatches to the
// family implementation, writes the result block and the CCR
// word, and clears the request byte last so the spinning
// trampoline only wakes once the results are in place.
func (u *Unit) serviceMailbox() {
	var img [15]uint32
	for i := range img {
		img[i] = u.Read32(mbImageAddr + uint32(i*4))
	}
	res := biosResult{d0: img[0], d1: img[1], a0: img[8], a1: img[9]}
	switch u.Read8(mbFamilyAddr) {
	case 0:
		u.cdbios(img, &res)
	case 1:
		u.buram(img, &res)
	case 2:
		u.cdboot(img, &res)
	default:
		res.fail()
	}
	u.Write32(mbResultAddr, res.d0)
	u.Write32(mbResultAddr+4, res.d1)
	u.Write32(mbResultAddr+8, res.a0)
	u.Write32(mbResultAddr+12, res.a1)
	var ccr uint16
	if res.carry {
		ccr = 0x0001 // CCR carry bit
	}
	u.Write16(mbCCRAddr, ccr)
	u.Write8(mbRequestAddr, 0)
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

// bcdByte encodes a value from 0 to 99 as a BCD byte.
func bcdByte(v int) byte {
	return byte((v/10)%10)<<4 | byte(v%10)
}

// bcdDecode decodes a BCD byte to its value.
func bcdDecode(b byte) int {
	return int(b>>4)*10 + int(b&0x0F)
}

// issueCDDMSF feeds one CDD command packet through the CDD
// command nibble interface: code byte in TC0 and TC1, the target
// MSF as BCD digit pairs in TC2 to TC7, TC8 = 0, and TC9 = the
// checksum nibble that makes the ten nibble sum congruent to
// 0xF modulo 16 (matching the check in cdd.go executeCommand).
func (u *Unit) issueCDDMSF(code uint8, m, s, f int) {
	nib := [10]uint8{
		code >> 4, code & 0x0F,
		uint8(m / 10 % 10), uint8(m % 10),
		uint8(s / 10 % 10), uint8(s % 10),
		uint8(f / 10 % 10), uint8(f % 10),
		0, 0,
	}
	sum := 0
	for i := 0; i < 9; i++ {
		sum += int(nib[i])
	}
	nib[9] = uint8((^sum) & 0xF)
	cdd := u.cddEngine()
	for i := 0; i < 10; i++ {
		cdd.WriteCommandNibble(i, nib[i])
	}
}

// issueCDD issues a CDD command whose target is the MSF of lba.
func (u *Unit) issueCDD(code uint8, lba int) {
	m, s, f := LBAToMSF(lba)
	u.issueCDDMSF(code, m, s, f)
}

// issueCDDSimple issues a CDD command with a zero target.
func (u *Unit) issueCDDSimple(code uint8) {
	u.issueCDDMSF(code, 0, 0, 0)
}

// findTrack returns the disc track with the given track number,
// or nil when there is no disc or no such track.
func (u *Unit) findTrack(number int) *Track {
	if u.disc == nil {
		return nil
	}
	for i := range u.disc.tracks {
		if u.disc.tracks[i].Number == number {
			return &u.disc.tracks[i]
		}
	}
	return nil
}

// currentTrack returns the track at the CDD play position. A
// position at the very end of the disc reports the last track.
func (u *Unit) biosCurrentTrack() *Track {
	if u.disc == nil {
		return nil
	}
	pos := u.cddEngine().PositionLBA()
	if tr := u.disc.trackForLBA(pos); tr != nil {
		return tr
	}
	tracks := u.disc.tracks
	if len(tracks) == 0 {
		return nil
	}
	if pos >= u.disc.NumSectors() {
		return &tracks[len(tracks)-1]
	}
	return nil
}

// sectorHeader packs the CDCREAD header long for a sector:
// minute, second and frame as BCD bytes plus a mode byte (1 for
// Mode 1 data, 2 for Mode 2 data, 0 for audio).
func (u *Unit) sectorHeader(lba int) uint32 {
	m, s, f := LBAToMSF(lba)
	var mode byte
	if u.disc != nil {
		if tr := u.disc.trackForLBA(lba); tr != nil {
			switch tr.Mode {
			case TrackMode1_2048, TrackMode1_2352:
				mode = 1
			case TrackMode2_2352:
				mode = 2
			}
		}
	}
	return uint32(bcdByte(m))<<24 | uint32(bcdByte(s))<<16 | uint32(bcdByte(f))<<8 | uint32(mode)
}

// applyFader pushes the BIOS fader state into the CDD fader. The
// effective fader value is volume times master over unity, in
// the same units FDRSET uses (1024 = 0x0400 = unity), applied
// through the CDD fader byte pair.
func (u *Unit) applyFader() {
	fd := int(u.bios.faderVol) * int(u.bios.faderMaster) / 1024
	word := uint16(fd) << 4
	cdd := u.cddEngine()
	cdd.WriteFaderByte(0, byte(word>>8))
	cdd.WriteFaderByte(1, byte(word))
}

// ---------------------------------------------------------------------------
// CDBIOS (family 0)
// ---------------------------------------------------------------------------

// cdbios dispatches one CDBIOS call. The function number is
// D0.w of the register image.
func (u *Unit) cdbios(img [15]uint32, res *biosResult) {
	fn := uint16(img[0])
	if fn < 0x80 {
		u.queueAsync(fn, img)
		return
	}
	switch fn {
	case 0x80: // CDBCHK: carry set while the drive queue is busy.
		res.carry = u.bios.queueValid
	case 0x81: // CDBSTAT
		u.cdbStat(res)
	case 0x82: // CDBTOCWRITE
		u.cdbTOCWrite(img)
	case 0x83: // CDBTOCREAD
		u.cdbTOCRead(img, res)
	case 0x84: // CDBPAUSE: record the standby pause time only.
		u.bios.standbyPause = uint16(img[1])
	case 0x85: // FDRSET
		u.fdrSet(img)
	case 0x86: // FDRCHG
		u.fdrChg(img)
	case 0x87, 0x88: // CDCSTART, CDCSTARTP
		// The manual text we have does not distinguish the P
		// variant, so both take the same path.
		u.cdcStartBIOS()
	case 0x89: // CDCSTOP
		u.issueCDDSimple(0x01)
		u.bios.readActive = false
		u.bios.readPrepared = false
		u.cdcReady = false
	case 0x8A: // CDCSTAT: carry clear when a sector is buffered.
		res.carry = !u.cdcReady
	case 0x8B: // CDCREAD
		b := &u.bios
		if b.readActive && u.cdcReady {
			b.readPrepared = true
			b.preparedLBA = b.readNextLBA
			hdr := u.sectorHeader(int(b.preparedLBA))
			b.lastHeader = hdr
			b.hasHeader = true
			res.d0 = hdr
		} else {
			res.fail()
		}
	case 0x8C: // CDCTRN
		u.cdcTransfer(img, res)
	case 0x8D: // CDCACK
		u.cdcAck(res)
	case 0x8E, 0x8F, 0x90:
		// SCDINIT, SCDSTART, SCDSTOP: success no-ops. Subcode
		// decoding is not modeled in this core.
	case 0x91: // SCDSTAT
		res.d0 = 0
		res.d1 = 0
	case 0x92, 0x93, 0x94:
		// SCDREAD, SCDPQ, SCDPQL: no subcode data is ever
		// prepared, so these always fail.
		res.fail()
	case 0x95: // LEDSET
		u.ledSet(img)
	case 0x96: // CDCSETMODE
		u.cdcSetMode(img)
	default:
		// WONDERREQ (0x97), WONDERCHK (0x98), and every other
		// unlisted synchronous function: failure stub.
		res.fail()
	}
}

// cdbStat implements CDBSTAT (0x81): it fills the 32 byte status
// table at PRG-RAM 0x25E80 through bus writes and returns its
// address in A0.
func (u *Unit) cdbStat(res *biosResult) {
	const statAddr = 0x00025E80
	var tbl [32]byte
	cdd := u.cddEngine()
	state := cdd.State()
	if u.disc == nil {
		tbl[0] |= 0x01 // no disc
	}
	if state == CDDTrayOpen {
		tbl[0] |= 0x02 // tray open
	}
	switch state {
	case CDDPlaying, CDDReading:
		tbl[1] = 1 // playing or reading
	case CDDPaused:
		tbl[1] = 2 // paused
	case CDDScanFF, CDDScanFR:
		tbl[1] = 4 // scanning
	}
	if state == CDDReading {
		tbl[2] = 1 // reading
	}
	// tbl[3] stays zero.
	cur := u.biosCurrentTrack()
	tbl[4] = cdd.DisplayStatus() // BP0
	// BP1 and BP2 stay zero.
	if cur != nil {
		tbl[7] = byte(cur.Number) // BP3: current track, hex
	}
	m, s, f := cdd.PositionMSF()
	tbl[8] = bcdByte(m) // BP4..BP6: absolute MSF as BCD
	tbl[9] = bcdByte(s)
	tbl[10] = bcdByte(f)
	// BP7 (tbl[11]) stays zero.
	rel := 0
	if cur != nil {
		rel = cdd.PositionLBA() - cur.StartLBA
		if rel < 0 {
			rel = 0
		}
	}
	rm, rs, rf := LBAToMSF(rel)
	tbl[12] = bcdByte(rm) // BP8..BP10: track relative MSF as BCD
	tbl[13] = bcdByte(rs)
	tbl[14] = bcdByte(rf)
	// BP11 (tbl[15]) stays zero.
	if u.disc != nil && len(u.disc.tracks) > 0 {
		tracks := u.disc.tracks
		tbl[16] = byte(tracks[0].Number)             // BP12: first track
		tbl[17] = byte(tracks[len(tracks)-1].Number) // BP13: last track
		lm, ls, lf := LBAToMSF(u.disc.NumSectors())
		tbl[20] = bcdByte(lm) // BP16..BP18: lead-out start MSF
		tbl[21] = bcdByte(ls)
		tbl[22] = bcdByte(lf)
	}
	// BP14 (tbl[18]) and BP19 (tbl[23]) stay zero.
	if cur != nil && cur.Mode != TrackAudio {
		tbl[19] = 0x04 // BP15 bit2: current track is a data track
	}
	binary.BigEndian.PutUint32(tbl[24:28], uint32(u.bios.faderMaster)<<16|uint32(u.bios.faderVol))
	binary.BigEndian.PutUint32(tbl[28:32], u.bios.lastHeader)
	for i := range tbl {
		u.Write8(statAddr+uint32(i), tbl[i])
	}
	res.a0 = statAddr
}

// cdbTOCWrite implements CDBTOCWRITE (0x82): it reads 4 byte
// entries {minute BCD, second BCD, frame BCD with bit7 as the
// data track flag, track number} from (A0) until a track number
// of 0xFF or 99 entries, and stores them as the override TOC
// that CDBTOCREAD consults first.
func (u *Unit) cdbTOCWrite(img [15]uint32) {
	a0 := img[8]
	count := 0
	for i := 0; i < 99; i++ {
		var e [4]byte
		for j := 0; j < 4; j++ {
			e[j] = u.Read8(a0 + uint32(i*4+j))
		}
		if e[3] == 0xFF {
			break
		}
		u.bios.tocEntries[count] = e
		count++
	}
	u.bios.tocCount = uint8(count)
}

// cdbTOCRead implements CDBTOCREAD (0x83). D1.w is the track
// number in hex. The result D0 long packs the start time as
// BCD minute, BCD second, BCD frame (bit7 of the frame byte set
// for data tracks), and the track number byte. D1.b reports the
// track type: 0x00 audio, 0xFF data. Track number 0 returns the
// lead-out start with a track byte of 0. An out of range number
// returns the beginning of disc time (00:02:00) with a track
// byte of 0, the manual documented error behavior.
func (u *Unit) cdbTOCRead(img [15]uint32, res *biosResult) {
	tno := int(uint16(img[1]))
	// The override TOC is consulted first.
	for i := 0; i < int(u.bios.tocCount); i++ {
		e := u.bios.tocEntries[i]
		if int(e[3]) == tno {
			res.d0 = uint32(e[0])<<24 | uint32(e[1])<<16 | uint32(e[2])<<8 | uint32(e[3])
			if e[2]&0x80 != 0 {
				res.setD1Byte(0xFF)
			} else {
				res.setD1Byte(0x00)
			}
			return
		}
	}
	if u.disc != nil && tno == 0 {
		// Lead-out: the start of the lead-out is the end of the
		// last track, at the total sector count.
		m, s, f := LBAToMSF(u.disc.NumSectors())
		res.d0 = uint32(bcdByte(m))<<24 | uint32(bcdByte(s))<<16 | uint32(bcdByte(f))<<8
		if tracks := u.disc.tracks; len(tracks) > 0 && tracks[len(tracks)-1].Mode != TrackAudio {
			res.setD1Byte(0xFF)
		} else {
			res.setD1Byte(0x00)
		}
		return
	}
	if tr := u.findTrack(tno); tr != nil {
		m, s, f := LBAToMSF(tr.StartLBA)
		frame := bcdByte(f)
		data := tr.Mode != TrackAudio
		if data {
			frame |= 0x80
		}
		res.d0 = uint32(bcdByte(m))<<24 | uint32(bcdByte(s))<<16 | uint32(frame)<<8 | uint32(byte(tno))
		if data {
			res.setD1Byte(0xFF)
		} else {
			res.setD1Byte(0x00)
		}
		return
	}
	// Out of range: beginning of disc time with track byte 0.
	frame := byte(0)
	if tr := u.findTrack(1); tr != nil && tr.Mode != TrackAudio {
		frame = 0x80
		res.setD1Byte(0xFF)
	} else {
		res.setD1Byte(0x00)
	}
	res.d0 = uint32(0x02)<<16 | uint32(frame)<<8
}

// fdrSet implements FDRSET (0x85). With D1.w bit15 set it sets
// the master volume from the low 11 bits, clamped to the 0x0400
// maximum; otherwise it sets the volume from the low 10 bits.
// Volumes run 0 to 0x0400. Setting the volume cancels any ramp.
func (u *Unit) fdrSet(img [15]uint32) {
	d1 := img[1]
	if d1&0x8000 != 0 {
		v := int(d1 & 0x07FF)
		if v > 0x0400 {
			v = 0x0400
		}
		u.bios.faderMaster = uint16(v)
	} else {
		u.bios.faderVol = uint16(d1 & 0x03FF)
		u.bios.faderSpeed = 0
		u.bios.faderTarget = u.bios.faderVol
		u.bios.faderTick = 0
	}
	u.applyFader()
}

// fdrChg implements FDRCHG (0x86). D1.l high word is the target
// volume, low word is the speed. A speed of 0x0400 or 0 sets the
// volume at once; any other speed ramps in BIOSService, moving
// the volume toward the target by the speed once per 223 call
// tick.
func (u *Unit) fdrChg(img [15]uint32) {
	d1 := img[1]
	target := uint16(d1 >> 16)
	if target > 0x0400 {
		target = 0x0400
	}
	speed := uint16(d1)
	if speed == 0 || speed == 0x0400 {
		u.bios.faderVol = target
		u.bios.faderTarget = target
		u.bios.faderSpeed = 0
		u.bios.faderTick = 0
		u.applyFader()
		return
	}
	u.bios.faderTarget = target
	u.bios.faderSpeed = speed
	u.bios.faderTick = 0
}

// cdcStartBIOS implements CDCSTART (0x87) and CDCSTARTP (0x88):
// with a BIOS read target recorded (ROMSEEK) it issues the CDD
// READ command there, otherwise it issues READ at the CDD
// current position as a fresh unbounded read.
func (u *Unit) cdcStartBIOS() {
	b := &u.bios
	if b.readTargetValid {
		lba := int(b.readTargetLBA)
		if u.disc == nil || u.disc.trackForLBA(lba) == nil {
			return
		}
		u.issueCDD(0x03, lba)
		b.readActive = true
		b.readNextLBA = uint32(lba)
		b.readPrepared = false
		u.cdcReady = false
		return
	}
	lba := u.cddEngine().PositionLBA()
	if u.disc == nil || u.disc.trackForLBA(lba) == nil {
		return
	}
	u.issueCDD(0x03, lba)
	b.readActive = true
	b.readNextLBA = uint32(lba)
	b.readRemaining = -1
	b.readPrepared = false
	u.cdcReady = false
}

// cdcTransfer implements CDCTRN (0x8C). It requires a prepared
// sector (CDCREAD). The 4 header bytes go to (A1) and 2336 data
// bytes go to (A0): the 2048 byte sector buffer followed by
// zeros, because the EDC and ECC region is not modeled. The
// result pointers advance past what was written.
func (u *Unit) cdcTransfer(img [15]uint32, res *biosResult) {
	b := &u.bios
	if !b.readPrepared {
		res.fail()
		return
	}
	a0, a1 := img[8], img[9]
	hdr := b.lastHeader
	for i := 0; i < 4; i++ {
		u.Write8(a1+uint32(i), byte(hdr>>uint(24-8*i)))
	}
	for i := 0; i < 2048; i++ {
		u.Write8(a0+uint32(i), u.sectorBuf[i])
	}
	for i := 2048; i < 2336; i++ {
		u.Write8(a0+uint32(i), 0)
	}
	res.a0 = a0 + 2336
	res.a1 = a1 + 4
}

// cdcAck implements CDCACK (0x8D): it clears the prepared state
// and the buffered sector flag, advances the read LBA, and
// clears the BIOS read active flag once a bounded read count is
// exhausted. The CDD transport is not stopped here: it keeps
// streaming its current track and settles at the track end on
// its own, which is the behavior the full stack fixture encodes
// (the drive is still in a read state right after the last ACK
// of a bounded read). CDCSTOP is the call that stops the drive.
func (u *Unit) cdcAck(res *biosResult) {
	b := &u.bios
	if !b.readPrepared {
		res.fail()
		return
	}
	b.readPrepared = false
	u.cdcReady = false
	b.readNextLBA++
	if b.readRemaining > 0 {
		b.readRemaining--
		if b.readRemaining == 0 {
			b.readActive = false
		}
	}
}

// ledSet implements LEDSET (0x95): it stores the D1.w mode in
// the BIOS state and writes a mode derived byte into the gate
// LED storage. The stored byte uses bit1 for red and bit2 for
// green, per the manual mode table: 0 ready (green), 1 disc in
// (green), 2 access (both), 3 standby (red), 4 error (both),
// 5 red, 6 green, 7 off. It is storage only today; the gate
// register read path keeps its existing behavior.
func (u *Unit) ledSet(img [15]uint32) {
	mode := uint16(img[1])
	u.bios.ledMode = mode
	table := [8]uint8{0x04, 0x04, 0x06, 0x02, 0x06, 0x02, 0x04, 0x00}
	u.led = table[mode&0x07]
}

// cdcSetMode implements CDCSETMODE (0x96): it stores the D1.w
// low byte as the CDC destination mode, and when it is a valid
// DDT value (0..3) it also writes register 0x03 bits 3-4. Note:
// the real load sample's value 3 means sub CPU read in the
// LC8951 model, while in this core the sub CPU read path is the
// buffered destination 0 used by CDCTRN.
func (u *Unit) cdcSetMode(img [15]uint32) {
	mode := uint8(img[1])
	u.bios.cdcDestMode = mode
	if mode <= 3 {
		u.reg03 = (u.reg03 &^ 0x18) | (mode << 3)
	}
}

// ---------------------------------------------------------------------------
// CDBIOS asynchronous drive commands (function numbers below 0x80)
// ---------------------------------------------------------------------------

// queueAsync handles an asynchronous CDBIOS call. The call
// returns immediately with the default result (registers
// unchanged, carry clear); an accepted command enters the one
// deep drive queue and CDBCHK reports busy until it executes.
// Argument bytes are captured from the (A0) table at queue time
// by argument class: class 0x0 takes none, class 0x1 takes one
// long (4 bytes), class 0x2 takes two longs (8 bytes). Function
// numbers 0x0D..0x0F, 0x1A..0x1F, 0x23..0x2B, and anything else
// unlisted hit the BIOS reject stub: they return like an async
// call but are never queued and take no action. If the queue is
// already occupied the new call is dropped (first in wins),
// since callers are expected to poll CDBCHK first.
func (u *Unit) queueAsync(fn uint16, img [15]uint32) {
	accepted := false
	switch {
	case fn <= 0x0C:
		accepted = true
	case fn >= 0x10 && fn <= 0x22 && !(fn >= 0x1A && fn <= 0x1F):
		accepted = true
	}
	if !accepted || u.bios.queueValid {
		return
	}
	b := &u.bios
	b.queueFn = uint8(fn)
	b.queueAge = 0
	for i := range b.queueArgs {
		b.queueArgs[i] = 0
	}
	n := 0
	switch fn >> 4 {
	case 1:
		n = 4
	case 2:
		n = 8
	}
	a0 := img[8]
	for i := 0; i < n; i++ {
		b.queueArgs[i] = u.Read8(a0 + uint32(i))
	}
	b.queueValid = true
}

// executeQueued runs one queued asynchronous drive command whose
// 223 call delay has elapsed.
func (u *Unit) executeQueued(fn uint8, args [8]byte) {
	switch fn {
	case 0x00: // DRVOPEN
		u.issueCDDSimple(0x0D)
	case 0x01: // behavior slot: stop
		u.issueCDDSimple(0x01)
	case 0x02: // behavior slot: resume from pause
		u.issueCDDSimple(0x07)
	case 0x04: // behavior slot: pause
		u.issueCDDSimple(0x06)
	case 0x06: // behavior slot: fast reverse
		u.issueCDDSimple(0x09)
	case 0x07: // behavior slot: fast forward
		u.issueCDDSimple(0x08)
	case 0x0A: // MSCSTOP
		u.issueCDDSimple(0x01)
	case 0x10: // DRVINIT
		u.bios.driveInit = true
		start := args[0]
		// args[1] is the ending track number; it is recorded by
		// real firmware but nothing in this core consumes it.
		if start&0x80 != 0 {
			u.playTrack(int(start & 0x7F))
		}
	case 0x11, 0x12, 0x13: // MSCPLAY, MSCPLAY1, MSCPLAYR
		// The single and repeat distinctions are not modeled at
		// the CDD level, so all three play the track from its
		// start.
		tno := int(binary.BigEndian.Uint16(args[0:2]))
		u.playTrack(tno)
	case 0x14: // MSCPLAYT: time long at (A0), BCD mm ss ff 00.
		m := bcdDecode(args[0])
		s := bcdDecode(args[1])
		f := bcdDecode(args[2])
		u.issueCDDMSF(0x03, m, s, f)
	case 0x15, 0x16: // MSCSEEK, MSCSEEK1: seek to a track start.
		tno := int(binary.BigEndian.Uint16(args[0:2]))
		if tr := u.findTrack(tno); tr != nil {
			u.issueCDD(0x04, tr.StartLBA)
		}
	case 0x17: // ROMREAD: unbounded read from a sector.
		sector := int(int32(binary.BigEndian.Uint32(args[0:4])))
		u.startBIOSRead(sector, -1)
	case 0x18: // ROMSEEK: seek and record the read target.
		sector := int(int32(binary.BigEndian.Uint32(args[0:4])))
		if u.disc != nil && u.disc.trackForLBA(sector) != nil {
			u.issueCDD(0x04, sector)
			u.bios.readTargetLBA = uint32(sector)
			u.bios.readTargetValid = true
			u.bios.readActive = false
		}
	case 0x19: // MSCSEEKT: seek to the time long position.
		m := bcdDecode(args[0])
		s := bcdDecode(args[1])
		f := bcdDecode(args[2])
		u.issueCDDMSF(0x04, m, s, f)
	case 0x20, 0x21: // ROMREADN (0x21 is the shared read tail)
		sector := int(int32(binary.BigEndian.Uint32(args[0:4])))
		count := int32(binary.BigEndian.Uint32(args[4:8]))
		u.startBIOSRead(sector, count)
	case 0x22: // ROMREADE: bounded read from start to end sector.
		start := int(int32(binary.BigEndian.Uint32(args[0:4])))
		end := int(int32(binary.BigEndian.Uint32(args[4:8])))
		if end >= start {
			u.startBIOSRead(start, int32(end-start+1))
		}
	default:
		// Remaining accepted behavior slots (0x03, 0x05, 0x08,
		// 0x09, 0x0B, 0x0C): success no-ops. Their manual names
		// are not confirmed in the material we have.
	}
}

// playTrack issues the CDD READ/PLAY command (0x03) at a track
// start. A data track is issued the same way: the CDD enters
// read mode for it, matching how the firmware treats it.
func (u *Unit) playTrack(tno int) {
	tr := u.findTrack(tno)
	if tr == nil {
		return
	}
	u.issueCDD(0x03, tr.StartLBA)
}

// startBIOSRead starts a BIOS sector read: it issues the CDD
// READ command at the sector and records the read state. A
// sector outside the disc starts nothing.
func (u *Unit) startBIOSRead(sector int, remaining int32) {
	if u.disc == nil || u.disc.trackForLBA(sector) == nil {
		return
	}
	u.issueCDD(0x03, sector)
	b := &u.bios
	b.readActive = true
	b.readNextLBA = uint32(sector)
	b.readRemaining = remaining
	b.readPrepared = false
	u.cdcReady = false
}

// ---------------------------------------------------------------------------
// BURAM (family 1): backup RAM file system
// ---------------------------------------------------------------------------
//
// The function numbers follow the manual listing order: BRMINIT,
// BRMSTAT, BRMSERCH, BRMREAD, BRMWRITE, BRMDEL, BRMFORMAT,
// BRMDIR, BRMVERIFY = 0..8. Note: the numeric equates in Sega's
// cabios.i were not obtainable, so the listing order is the best
// available source for the numbering.
//
// The store is 8192 logical bytes (de-interleaved; the physical
// odd byte mapping on the bus is a bus detail this internal store
// does not model). Blocks are 0x40 bytes: 128 blocks total. The
// volume footer is the last block:
//
//	+0..+10   eleven 0x5F bytes
//	+11..+14  zero
//	+15       0x40
//	+16..+23  free block count, big-endian word written 4 times
//	+24..+31  file count, big-endian word written 4 times
//	+32..+43  "SEGA_CD_ROM" plus a zero byte
//	+44       0x01
//	+45..+47  zero
//	+48..+63  "RAM_CARTRIDGE___"
//
// A volume is formatted iff the last 32 footer bytes match that
// template. The footer itself takes blocks, so an empty
// formatted volume has 125 free blocks.
//
// The directory layout is our own design (on real hardware it
// is BIOS internal and was not published): 16 byte entries are
// stored downward from the end of the data area, entry i at
// offset (8192-64) - 16*(i+1). An entry is name[11] padded with
// underscores, a flag byte (0 normal, 0xFF protected), a
// big-endian start block word, and a big-endian unit count word
// (units of 0x40 bytes for normal files, 0x20 for protected).
// Data blocks allocate upward from block 1 (block 0 is reserved)
// first-fit contiguous, never crossing into the directory bytes.
// Free blocks = 125 - data blocks used - directory blocks used,
// where directory blocks = ceil(16 * files / 64), 0 with no
// files. The footer counts are rewritten on every mutation.

const bramFooterOff = BRAMSize - 0x40

const bramDisplayString = " 8192 BYTES "

// bramEntry is one parsed directory entry.
type bramEntry struct {
	name  [11]byte
	flag  uint8
	start uint16
	units uint16
}

// bramUnitSize returns the unit size for a flag byte.
func bramUnitSize(flag uint8) int {
	if flag == 0 {
		return 0x40
	}
	return 0x20
}

// bytes returns the file data length in bytes.
func (e *bramEntry) bytes() int {
	return int(e.units) * bramUnitSize(e.flag)
}

// blocks returns the number of 0x40 blocks the file data spans.
func (e *bramEntry) blocks() int {
	return (e.bytes() + 0x3F) / 0x40
}

// bramFormatted reports whether the store holds a formatted
// volume: the last 32 footer bytes match the template.
func (u *Unit) bramFormatted() bool {
	want := buildBRAMFooter(0, 0)
	for i := 32; i < 64; i++ {
		if u.bram[bramFooterOff+i] != want[i] {
			return false
		}
	}
	return true
}

// buildBRAMFooter builds the 64 byte volume footer with the
// given free block and file counts.
func buildBRAMFooter(free, files uint16) [64]byte {
	var f [64]byte
	for i := 0; i < 11; i++ {
		f[i] = 0x5F
	}
	f[15] = 0x40
	for i := 0; i < 4; i++ {
		binary.BigEndian.PutUint16(f[16+i*2:18+i*2], free)
		binary.BigEndian.PutUint16(f[24+i*2:26+i*2], files)
	}
	copy(f[32:44], "SEGA_CD_ROM")
	// f[43] is the zero byte after the string.
	f[44] = 0x01
	copy(f[48:64], "RAM_CARTRIDGE___")
	return f
}

// bramEntries parses the directory. The entry count comes from
// the footer file count word, capped by the space available.
func (u *Unit) bramEntries() []bramEntry {
	if !u.bramFormatted() {
		return nil
	}
	count := int(binary.BigEndian.Uint16(u.bram[bramFooterOff+24 : bramFooterOff+26]))
	max := bramFooterOff / 16
	if count > max {
		count = max
	}
	entries := make([]bramEntry, 0, count)
	for i := 0; i < count; i++ {
		off := bramFooterOff - 16*(i+1)
		var e bramEntry
		copy(e.name[:], u.bram[off:off+11])
		e.flag = u.bram[off+11]
		e.start = binary.BigEndian.Uint16(u.bram[off+12 : off+14])
		e.units = binary.BigEndian.Uint16(u.bram[off+14 : off+16])
		entries = append(entries, e)
	}
	return entries
}

// bramFreeBlocks computes the free block count for a directory:
// 125 minus data blocks used minus directory blocks used.
func bramFreeBlocks(entries []bramEntry) int {
	used := 0
	for i := range entries {
		used += entries[i].blocks()
	}
	dirBlocks := 0
	if len(entries) > 0 {
		dirBlocks = (16*len(entries) + 0x3F) / 0x40
	}
	return 125 - used - dirBlocks
}

// bramRewriteDir replaces the directory with entries: it clears
// the byte range the old and new directories span, writes the
// entries downward from the end of the data area, and rewrites
// the footer free and file counts.
func (u *Unit) bramRewriteDir(entries []bramEntry) {
	oldCount := 0
	if u.bramFormatted() {
		oldCount = int(binary.BigEndian.Uint16(u.bram[bramFooterOff+24 : bramFooterOff+26]))
	}
	span := oldCount
	if len(entries) > span {
		span = len(entries)
	}
	lo := bramFooterOff - 16*span
	if lo < 0 {
		lo = 0
	}
	for i := lo; i < bramFooterOff; i++ {
		u.bram[i] = 0
	}
	for i, e := range entries {
		off := bramFooterOff - 16*(i+1)
		copy(u.bram[off:off+11], e.name[:])
		u.bram[off+11] = e.flag
		binary.BigEndian.PutUint16(u.bram[off+12:off+14], e.start)
		binary.BigEndian.PutUint16(u.bram[off+14:off+16], e.units)
	}
	free := uint16(bramFreeBlocks(entries))
	files := uint16(len(entries))
	for i := 0; i < 4; i++ {
		binary.BigEndian.PutUint16(u.bram[bramFooterOff+16+i*2:bramFooterOff+18+i*2], free)
		binary.BigEndian.PutUint16(u.bram[bramFooterOff+24+i*2:bramFooterOff+26+i*2], files)
	}
}

// bramFind returns the index of the entry with the given name,
// or -1.
func bramFind(entries []bramEntry, name [11]byte) int {
	for i := range entries {
		if entries[i].name == name {
			return i
		}
	}
	return -1
}

// bramReadName reads an 11 byte file name from the bus at addr.
func (u *Unit) bramReadName(addr uint32) [11]byte {
	var name [11]byte
	for i := 0; i < 11; i++ {
		name[i] = u.Read8(addr + uint32(i))
	}
	return name
}

// bramWriteDisplay writes the 12 byte size display string to
// the bus at addr.
func (u *Unit) bramWriteDisplay(addr uint32) {
	for i := 0; i < len(bramDisplayString); i++ {
		u.Write8(addr+uint32(i), bramDisplayString[i])
	}
}

// bramAlloc finds the first fit contiguous run of needBlocks
// data blocks at or above block 1 that overlaps no entry
// (skipping index exclude, or -1) and ends at or before
// dirLimit (a byte offset the run must not cross).
func bramAlloc(entries []bramEntry, exclude, needBlocks, dirLimit int) (int, bool) {
	for cand := 1; ; cand++ {
		startByte := cand * 0x40
		endByte := startByte + needBlocks*0x40
		if endByte > dirLimit {
			return 0, false
		}
		overlap := false
		for i := range entries {
			if i == exclude {
				continue
			}
			s := int(entries[i].start) * 0x40
			en := s + entries[i].blocks()*0x40
			if startByte < en && s < endByte {
				overlap = true
				break
			}
		}
		if !overlap {
			return cand, true
		}
	}
}

// bramNameMatch matches an 11 byte file name against an 11 byte
// template where '*' matches any sequence of characters. Other
// characters, including the underscore padding, match literally.
func bramNameMatch(pat, name [11]byte) bool {
	p, n := 0, 0
	starP, starN := -1, -1
	for n < 11 {
		if p < 11 && pat[p] == name[n] {
			p++
			n++
		} else if p < 11 && pat[p] == '*' {
			starP = p
			starN = n
			p++
		} else if starP >= 0 {
			p = starP + 1
			starN++
			n = starN
		} else {
			return false
		}
	}
	for p < 11 && pat[p] == '*' {
		p++
	}
	return p == 11
}

// buram dispatches one BURAM call. The function number is D0.w
// of the register image.
func (u *Unit) buram(img [15]uint32, res *biosResult) {
	fn := uint16(img[0])
	switch fn {
	case 0: // BRMINIT
		if u.bramFormatted() {
			u.bramWriteDisplay(img[9])
			// Result A1 stays the display buffer (the default).
		} else {
			res.carry = true
			res.d0 = (res.d0 & 0xFFFFFF00) | 1 // D0.b = 1: unformatted
		}
	case 1: // BRMSTAT
		// The manual lists no carry for BRMSTAT, so the carry
		// result stays at the default (clear) either way.
		if u.bramFormatted() {
			entries := u.bramEntries()
			u.bramWriteDisplay(img[9])
			res.setD0Word(uint16(bramFreeBlocks(entries)))
			res.d1 = (res.d1 & 0xFFFF0000) | uint32(uint16(len(entries)))
		} else {
			res.setD0Word(0xFFFF)
			res.d1 = (res.d1 & 0xFFFF0000) | 0xFFFF
		}
	case 2: // BRMSERCH
		u.bramSerch(img, res, nil)
	case 3: // BRMREAD
		u.bramRead(img, res)
	case 4: // BRMWRITE
		u.bramWrite(img, res)
	case 5: // BRMDEL
		name := u.bramReadName(img[8])
		entries := u.bramEntries()
		idx := bramFind(entries, name)
		if idx < 0 {
			res.fail()
			return
		}
		// The data bytes are not scrubbed, matching hardware.
		entries = append(entries[:idx], entries[idx+1:]...)
		u.bramRewriteDir(entries)
	case 6: // BRMFORMAT
		for i := range u.bram {
			u.bram[i] = 0
		}
		footer := buildBRAMFooter(125, 0)
		copy(u.bram[bramFooterOff:], footer[:])
	case 7: // BRMDIR
		u.bramDir(img, res)
	case 8: // BRMVERIFY
		u.bramVerify(img, res)
	default:
		res.fail()
	}
}

// bramSerch is the shared lookup behind BRMSERCH: on a hit it
// sets D0.w to the unit count, D1.b to the flag byte, and the A0
// result to the informational pointer 0x600000 + startBlock*0x40
// (backup RAM is not bus mapped in this core). On a miss it sets
// carry. BRMREAD reuses the same outs.
func (u *Unit) bramSerch(img [15]uint32, res *biosResult, idxOut *int) {
	name := u.bramReadName(img[8])
	entries := u.bramEntries()
	idx := bramFind(entries, name)
	if idx < 0 {
		res.fail()
		if idxOut != nil {
			*idxOut = -1
		}
		return
	}
	e := entries[idx]
	res.setD0Word(e.units)
	res.setD1Byte(e.flag)
	res.a0 = 0x600000 + uint32(e.start)*0x40
	if idxOut != nil {
		*idxOut = idx
	}
}

// bramRead implements BRMREAD (3): it copies the file bytes to
// the destination at (A1) through bus writes, with the same outs
// as BRMSERCH. The A1 result advances past the bytes read, the
// same result pointer convention CDCTRN uses.
func (u *Unit) bramRead(img [15]uint32, res *biosResult) {
	idx := -1
	u.bramSerch(img, res, &idx)
	if idx < 0 {
		return
	}
	e := u.bramEntries()[idx]
	dst := img[9]
	base := int(e.start) * 0x40
	for i := 0; i < e.bytes(); i++ {
		u.Write8(dst+uint32(i), u.bram[base+i])
	}
	res.a1 = dst + uint32(e.bytes())
}

// bramWriteParams resolves the flag byte and unit count for
// BRMWRITE and BRMVERIFY from the parameter sources callers
// use. Two conventions exist side by side: the table at (A0)
// laid out as {name[11], flag byte, block size word}, and the
// register convention the published call table documents (flag
// in D1.w, block count in D2.w, byte count in D3.l), which the
// staged parameter block form mirrors with a flag word at +12
// and a block count word at +14. A nonzero register value wins
// over the table word at +14, which wins over the table word at
// +12, so both conventions resolve to the same parameters.
func (u *Unit) bramWriteParams(img [15]uint32, a0 uint32) (flag uint8, sizeWord uint16) {
	flag = u.Read8(a0 + 11)
	if flag == 0 {
		flag = uint8(img[1])
	}
	sizeWord = uint16(img[2])
	if sizeWord == 0 {
		sizeWord = u.Read16(a0 + 14)
	}
	if sizeWord == 0 {
		sizeWord = u.Read16(a0 + 12)
	}
	return flag, sizeWord
}

// bramWrite implements BRMWRITE (4). The parameters come from
// the (A0) table and registers (see bramWriteParams); the data
// source is (A1). An existing file of the same name is replaced.
// The copy length is blockSize times the unit size for the flag.
// With no room the call fails with carry set and the store
// untouched.
func (u *Unit) bramWrite(img [15]uint32, res *biosResult) {
	if !u.bramFormatted() {
		res.fail()
		return
	}
	a0 := img[8]
	name := u.bramReadName(a0)
	flag, sizeWord := u.bramWriteParams(img, a0)
	entries := u.bramEntries()
	idx := bramFind(entries, name)
	newCount := len(entries)
	if idx < 0 {
		newCount++
	}
	dirLimit := bramFooterOff - 16*newCount
	needBytes := int(sizeWord) * bramUnitSize(flag)
	needBlocks := (needBytes + 0x3F) / 0x40
	start, ok := bramAlloc(entries, idx, needBlocks, dirLimit)
	if !ok {
		res.fail()
		return
	}
	e := bramEntry{name: name, flag: flag, start: uint16(start), units: sizeWord}
	if idx >= 0 {
		entries[idx] = e
	} else {
		entries = append(entries, e)
	}
	u.bramRewriteDir(entries)
	src := img[9]
	base := start * 0x40
	for i := 0; i < needBytes; i++ {
		u.bram[base+i] = u.Read8(src + uint32(i))
	}
	res.setD0Word(sizeWord)
	res.setD1Byte(flag)
	res.a0 = 0x600000 + uint32(start)*0x40
	// The A1 result advances past the bytes written, the same
	// result pointer convention CDCTRN uses. Callers that chain
	// a BRMSTAT (which writes its display string at A1) after a
	// write rely on the source buffer staying intact.
	res.a1 = src + uint32(needBytes)
}

// bramDir implements BRMDIR (7). D1.l high word is the number of
// matching files to skip, low word is the buffer capacity in
// files. Matching entries (16 bytes each, directory layout) are
// written to (A1). Carry is clear when every matching entry fit
// and set when more remain.
func (u *Unit) bramDir(img [15]uint32, res *biosResult) {
	if !u.bramFormatted() {
		res.fail()
		return
	}
	skip := int(img[1] >> 16)
	capacity := int(img[1] & 0xFFFF)
	tmpl := u.bramReadName(img[8])
	dst := img[9]
	written := 0
	seen := 0
	more := false
	for _, e := range u.bramEntries() {
		if !bramNameMatch(tmpl, e.name) {
			continue
		}
		if seen < skip {
			seen++
			continue
		}
		seen++
		if written >= capacity {
			more = true
			continue
		}
		off := dst + uint32(written*16)
		for i := 0; i < 11; i++ {
			u.Write8(off+uint32(i), e.name[i])
		}
		u.Write8(off+11, e.flag)
		u.Write16(off+12, e.start)
		u.Write16(off+14, e.units)
		written++
	}
	res.carry = more
}

// bramVerify implements BRMVERIFY (8): the table at (A0) has
// the BRMWRITE layout and the data at (A1) is compared against
// the stored file. A match clears carry. A missing file sets
// carry with D0.w = 0; a mismatch sets carry with D0.w = 0xFFFF.
func (u *Unit) bramVerify(img [15]uint32, res *biosResult) {
	a0 := img[8]
	name := u.bramReadName(a0)
	entries := u.bramEntries()
	idx := bramFind(entries, name)
	if idx < 0 {
		res.carry = true
		res.setD0Word(0)
		return
	}
	e := entries[idx]
	flag, sizeWord := u.bramWriteParams(img, a0)
	n := int(sizeWord) * bramUnitSize(flag)
	mismatch := flag != e.flag || n != e.bytes()
	if !mismatch {
		base := int(e.start) * 0x40
		src := img[9]
		for i := 0; i < n; i++ {
			if u.Read8(src+uint32(i)) != u.bram[base+i] {
				mismatch = true
				break
			}
		}
	}
	if mismatch {
		res.carry = true
		res.setD0Word(0xFFFF)
	}
}

// ---------------------------------------------------------------------------
// CDBOOT (family 2)
// ---------------------------------------------------------------------------
//
// The function numbers follow the manual listing order: CBTINIT,
// CBTINT, CBTOPENDISC, CBTOPENSTAT, CBTCHKDISC, CBTCHKSTAT =
// 0..5. The same cabios.i caveat as BURAM applies.

// cdboot dispatches one CDBOOT call. The function number is
// D0.w of the register image.
func (u *Unit) cdboot(img [15]uint32, res *biosResult) {
	fn := uint16(img[0])
	switch fn {
	case 0, 1: // CBTINIT, CBTINT: no-ops, carry clear.
	case 2: // CBTOPENDISC
		u.issueCDDSimple(0x0D)
	case 3: // CBTOPENSTAT: the open completed, carry clear.
	case 4: // CBTCHKDISC
		u.cbtChkDisc(img, res)
	case 5: // CBTCHKSTAT
		u.cbtChkStat(res)
	default:
		res.fail()
	}
}

// cbtChkDisc implements CBTCHKDISC (4): it copies sector 0 user
// data (2048 bytes) into the 0x800 byte scratch at (A0) and
// clears carry when the data starts with SEGADISCSYSTEM. With no
// disc, or an unreadable sector 0, carry is set.
func (u *Unit) cbtChkDisc(img [15]uint32, res *biosResult) {
	if u.disc == nil {
		res.carry = true
		return
	}
	data, err := u.disc.ReadData(0)
	if err != nil {
		res.carry = true
		return
	}
	a0 := img[8]
	for i := 0; i < 2048; i++ {
		u.Write8(a0+uint32(i), data[i])
	}
	res.carry = string(data[:14]) != "SEGADISCSYSTEM"
}

// cbtChkStat implements CBTCHKSTAT (5): carry clear with the
// D0.w disc type: -1 (0xFFFF) for no disc or an open tray, 1 for
// an audio only disc, 2 for a data disc without the boot
// signature, and 5 for a game boot disc (data track 1 sector 0
// carries SEGADISCSYSTEM).
func (u *Unit) cbtChkStat(res *biosResult) {
	if u.disc == nil || u.cddEngine().State() == CDDTrayOpen {
		res.setD0Word(0xFFFF)
		return
	}
	if u.disc.DataTracks() == 0 {
		res.setD0Word(1)
		return
	}
	data, err := u.disc.ReadData(0)
	if err == nil && string(data[:14]) == "SEGADISCSYSTEM" {
		res.setD0Word(5)
		return
	}
	res.setD0Word(2)
}
