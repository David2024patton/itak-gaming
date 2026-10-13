package cd

// CD audio output for the SPU, per the psx-spx
// CD-ROM format and XA chapters.
//
// PullCDSample is called once per 44,100 Hz output
// sample.
//
// CD-DA: while playing, it returns the raw sector
// samples (2352-byte sectors, 588 stereo int16
// frames, little-endian) in order, advancing the
// play position by 1 frame at single speed or 2 at
// double speed (Setmode bit 7). Sectors on data
// tracks are treated as silence, as the doc notes
// for Play on data sectors. At a track boundary the
// Setmode autopause bit decides: autopause on
// pauses with INT4 at the end of the old track;
// off, playback continues into the next track, and
// at the end of the disc it stops (motor off) with
// INT4. While Forward/Backward scanning, and while
// muted, it returns silence (position is moved by
// Advance for scanning; by this pull for play).
//
// XA-ADPCM: a ReadN/ReadS stream with the Setmode
// XA bit on delivers Mode 2 Form 2 audio sectors
// to the decoder in deliverSector (via
// tryXAOrFilter) instead of the data FIFO. Decoded
// samples are queued and served sample-and-hold at
// the ratio of their rate to 44,100 Hz: each source
// sample is held until a fractional accumulator
// (rate added per output sample) crosses 44,100.
// That replaces the doc's 25-point zigzag
// resampler with the simpler hold the task
// specifies. At 37,800 Hz the hold pattern repeats
// one source sample in every 7 outputs (37,800 x 7
// = 44,100 x 6); at 18,900 Hz the ratio is 7:3.
//
// XA sector layout (raw sector): sync (12), header
// (4), subheader (4: file, channel, submode,
// codinginfo) plus its copy (4), then 0x900 bytes
// of ADPCM data in 18 portions of 128 bytes, each
// portion holding 16 header bytes and 28 data
// words. A 2336-byte raw sector is the same sector
// without sync and header: subheader at offset 0,
// ADPCM data at offset 8.

var syncPattern = []byte{0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00}

func hasSync(raw []byte) bool {
	if len(raw) < 16 {
		return false
	}
	for i, b := range syncPattern {
		if raw[i] != b {
			return false
		}
	}
	return true
}

// parseMode2 reports the subheader of a raw sector
// when it is a Mode 2 sector, and records the
// sector header/subheader in lastHdr for GetlocL.
func (c *Controller) parseMode2(raw []byte) (file, channel, submode, coding uint8, ok bool) {
	var subOff, hdrOff int
	switch {
	case hasSync(raw) && len(raw) >= 24:
		if raw[15] != 2 {
			return 0, 0, 0, 0, false
		}
		hdrOff, subOff = 12, 16
	case len(raw) == 2336:
		subOff = 0
		hdrOff = -1
	default:
		return 0, 0, 0, 0, false
	}
	sub := raw[subOff : subOff+4]
	if hdrOff >= 0 {
		c.lastHdr = [8]byte{raw[hdrOff], raw[hdrOff+1], raw[hdrOff+2], raw[hdrOff+3],
			sub[0], sub[1], sub[2], sub[3]}
	} else {
		m, s, f := LBAToMSF(c.nextLBA)
		c.lastHdr = [8]byte{toBCD(m), toBCD(s), toBCD(f), 2, sub[0], sub[1], sub[2], sub[3]}
	}
	return sub[0], sub[1], sub[2], sub[3], true
}

// tryXAOrFilter decides, for one streamed sector,
// whether it is consumed without data delivery:
// skipped by the Setfilter rule, or decoded as XA
// audio. It also keeps lastHdr current for sectors
// it parses.
func (c *Controller) tryXAOrFilter(lba int) bool {
	raw, err := c.disc.ReadSector(lba, true)
	if err != nil || len(raw) < 2048 {
		c.updateLastHdrSynth(lba)
		return false
	}
	file, channel, submode, coding, ok := c.parseMode2(raw)
	if !ok {
		c.updateLastHdrSynth(lba)
		return false
	}
	if c.mode&0x08 != 0 && (file != c.filterFile || channel != c.filterChannel) {
		// XA filter on (Setmode bit 3): non-matching
		// sectors are skipped entirely (the task's
		// stated simplification).
		return true
	}
	isAudio := submode&0x20 != 0 && submode&0x04 != 0 && submode&0x40 != 0
	if c.mode&0x40 != 0 && isAudio {
		if c.decodeXASector(raw, coding) {
			c.xaActive = true
			return true
		}
	}
	return false
}

// XA filter tables from the format chapter (only
// filters 0..3 exist for XA; entry 4 is the SPU's).
var (
	xaPosTable = [5]int{0, 60, 115, 98, 122}
	xaNegTable = [5]int{0, 0, -52, -55, -60}
)

// xaExpand shifts a signed sample by the (possibly
// negative) net shift without a negative shift
// count: shift is 12-shiftCode for 4-bit samples
// (expand by 12, then shift down by the code) and
// 8-shiftCode for 8-bit samples.
func xaExpand(t, shift int) int {
	if shift >= 0 {
		return t << uint(shift)
	}
	return t >> uint(-shift)
}

func (c *Controller) xaSample(t, shift, filter int, old, older *int) int16 {
	s := xaExpand(t, shift) + ((*old*xaPosTable[filter] + *older*xaNegTable[filter] + 32) >> 6)
	if s < -32768 {
		s = -32768
	}
	if s > 32767 {
		s = 32767
	}
	*older = *old
	*old = s
	return int16(s)
}

// decodeXASector decodes one XA audio sector's
// 0x900 ADPCM bytes into the sample queue. coding
// is the subheader codinginfo byte: bits 0-1
// mono/stereo, bit 2 sample rate (0=37.8 kHz,
// 1=18.9 kHz), bits 4-5 bits per sample
// (0=4-bit, 1=8-bit).
func (c *Controller) decodeXASector(raw []byte, coding uint8) bool {
	dataOff := 24
	if len(raw) == 2336 {
		dataOff = 8
	}
	if len(raw) < dataOff+0x900 {
		return false
	}
	data := raw[dataOff : dataOff+0x900]
	c.xaStereo = coding&0x03 == 1
	c.xaBits8 = (coding>>4)&0x03 == 1
	c.xaRate = 37800
	if coding&0x04 != 0 {
		c.xaRate = 18900
	}
	for portion := 0; portion < 18; portion++ {
		p := data[portion*128 : portion*128+128]
		if c.xaBits8 {
			c.decodeXAPortion8(p)
		} else {
			c.decodeXAPortion4(p)
		}
	}
	return true
}

// decodeXAPortion4 decodes one 128-byte portion in
// 4-bit format: 8 blocks of 28 samples, the blocks'
// samples packed as nibbles across the 28 data
// words (block b is nibble b of each word).
func (c *Controller) decodeXAPortion4(p []byte) {
	var blocks [8][28]int16
	for b := 0; b < 8; b++ {
		hdr := p[4+b]
		shiftCode := int(hdr & 0x0F)
		if shiftCode >= 13 {
			shiftCode = 9 // doc: reserved codes act as 9
		}
		shift := 12 - shiftCode
		filter := int(hdr>>4) & 0x03
		// Filter state is per logical channel: mono
		// runs all blocks through one state in time
		// order; stereo runs even blocks through the
		// left state and odd blocks through the
		// right state. Decoding block by block keeps
		// each channel's state in time order.
		for j := 0; j < 28; j++ {
			byt := p[16+j*4+b/2]
			nib := byt & 0x0F
			if b%2 == 1 {
				nib = byt >> 4
			}
			t := int(nib)
			if t >= 8 {
				t -= 16
			}
			switch {
			case !c.xaStereo:
				blocks[b][j] = c.xaSample(t, shift, filter, &c.xaOldM, &c.xaOlderM)
			case b%2 == 0:
				blocks[b][j] = c.xaSample(t, shift, filter, &c.xaOldL, &c.xaOlderL)
			default:
				blocks[b][j] = c.xaSample(t, shift, filter, &c.xaOldR, &c.xaOlderR)
			}
		}
	}
	c.emitXABlocks(blocks[:], 8)
}

// decodeXAPortion8 decodes one 128-byte portion in
// 8-bit format: 4 blocks of 28 samples, one byte
// per block in each data word.
func (c *Controller) decodeXAPortion8(p []byte) {
	var blocks [4][28]int16
	for b := 0; b < 4; b++ {
		hdr := p[4+b]
		shiftCode := int(hdr & 0x0F)
		if shiftCode >= 13 {
			shiftCode = 9
		}
		shift := 8 - shiftCode
		filter := int(hdr>>4) & 0x03
		for j := 0; j < 28; j++ {
			t := int(int8(p[16+j*4+b]))
			switch {
			case !c.xaStereo:
				blocks[b][j] = c.xaSample(t, shift, filter, &c.xaOldM, &c.xaOlderM)
			case b%2 == 0:
				blocks[b][j] = c.xaSample(t, shift, filter, &c.xaOldL, &c.xaOlderL)
			default:
				blocks[b][j] = c.xaSample(t, shift, filter, &c.xaOldR, &c.xaOlderR)
			}
		}
	}
	c.emitXABlocks(blocks[:], 4)
}

// emitXABlocks appends decoded blocks to the queue
// in time order: mono blocks are sequential;
// stereo blocks alternate left/right in pairs.
func (c *Controller) emitXABlocks(blocks [][28]int16, n int) {
	push := func(l, r int16) {
		if len(c.xaQueue) < xaQueueCap {
			c.xaQueue = append(c.xaQueue, [2]int16{l, r})
		}
	}
	if !c.xaStereo {
		for b := 0; b < n; b++ {
			for j := 0; j < 28; j++ {
				push(blocks[b][j], blocks[b][j])
			}
		}
		return
	}
	for pair := 0; pair+1 < n; pair += 2 {
		for j := 0; j < 28; j++ {
			push(blocks[pair][j], blocks[pair+1][j])
		}
	}
}

// pullXA serves the XA queue sample-and-hold, as
// described at the top of this file.
func (c *Controller) pullXA() (int16, int16) {
	if c.xaRate == 0 {
		c.xaRate = 37800
	}
	if c.xaNeedNext && len(c.xaQueue) > 0 {
		c.xaHoldL, c.xaHoldR = c.xaQueue[0][0], c.xaQueue[0][1]
		c.xaQueue = c.xaQueue[1:]
		c.xaHoldValid = true
		c.xaNeedNext = false
	}
	if !c.xaHoldValid {
		if len(c.xaQueue) == 0 {
			return 0, 0
		}
		c.xaHoldL, c.xaHoldR = c.xaQueue[0][0], c.xaQueue[0][1]
		c.xaQueue = c.xaQueue[1:]
		c.xaHoldValid = true
	}
	l, r := c.xaHoldL, c.xaHoldR
	c.xaAcc += c.xaRate
	if c.xaAcc >= 44100 {
		c.xaAcc -= 44100
		c.xaNeedNext = true
	}
	if c.muted {
		return 0, 0
	}
	return l, r
}

func (c *Controller) loadPlayBuf() {
	buf := make([]byte, 2352)
	if c.disc != nil {
		if tr, ok := c.trackAt(c.playLBA); ok && tr.Mode == TrackAudio {
			if raw, err := c.disc.ReadSector(c.playLBA, true); err == nil {
				copy(buf, raw)
			}
		}
		// Data track sectors play as silence, per the
		// doc's note on Play over data sectors.
	}
	c.playBuf = buf
	c.playBufLBA = c.playLBA
	c.playBufValid = true
}

// PullCDSample returns one 44,100 Hz stereo sample
// of CD audio: CD-DA while playing, XA-ADPCM while
// an XA stream is active, else silence.
func (c *Controller) PullCDSample() (int16, int16) {
	if c.playing && !c.seeking {
		if c.scanDir != 0 {
			return 0, 0 // scanning is muted; Advance moves the position
		}
		if c.playOff >= 588 {
			return 0, 0 // parked at a track end by autopause
		}
		if !c.playBufValid || c.playBufLBA != c.playLBA {
			c.loadPlayBuf()
		}
		off := c.playOff * 4
		l := int16(uint16(c.playBuf[off]) | uint16(c.playBuf[off+1])<<8)
		r := int16(uint16(c.playBuf[off+2]) | uint16(c.playBuf[off+3])<<8)
		steps := 1
		if c.mode&0x80 != 0 {
			steps = 2 // double speed: 2 source frames per output sample
		}
		for i := 0; i < steps; i++ {
			c.advancePlayFrame()
		}
		if c.muted {
			return 0, 0
		}
		return l, r
	}
	if c.xaActive {
		return c.pullXA()
	}
	return 0, 0
}

// advancePlayFrame moves the play position by one
// CD-DA frame, handling sector, track, and disc
// boundaries.
func (c *Controller) advancePlayFrame() {
	c.playOff++
	if c.playOff < 588 {
		return
	}
	c.playOff = 0
	c.playLBA++
	c.curLBA = c.playLBA
	c.playBufValid = false
	if c.mode&0x04 != 0 && c.playLBA%10 == 0 {
		c.reportFrame()
	}
	total := c.totalSectors()
	if c.playLBA >= total {
		// End of disc: INT4, playback stops, motor
		// off, per the doc.
		c.playing = false
		c.motorOn = false
		c.pushResp(c.statusByte())
		c.raiseIRQ(irqDataEnd)
		return
	}
	prevTr, pok := c.trackAt(c.playLBA - 1)
	nextTr, nok := c.trackAt(c.playLBA)
	if pok && nok && nextTr.Number != prevTr.Number && c.mode&0x02 != 0 {
		// Autopause: INT4 and pause at the END of
		// the old track (position stays there).
		c.playLBA--
		c.curLBA = c.playLBA
		c.playOff = 588
		c.playing = false
		c.pushResp(c.statusByte())
		c.raiseIRQ(irqDataEnd)
	}
}

// reportFrame emits one Play report INT1 (Setmode
// report bit): stat, track, index, then absolute
// or track-relative MSF depending on the sector,
// then peak lo/hi (modeled as 0).
func (c *Controller) reportFrame() {
	pos := c.positionBytes()
	resp := []byte{c.statusByte(), pos[0], pos[1], pos[5], pos[6], pos[7], 0, 0}
	if c.playLBA%20 >= 10 {
		resp[3] = pos[2]
		resp[4] = pos[3] | 0x80 // relative form sets the ss high bit, per the doc
		resp[5] = pos[4]
	}
	c.pushResp(resp...)
	c.raiseIRQ(irqDataReady)
}
