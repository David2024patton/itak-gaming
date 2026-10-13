// Package mdec implements the PlayStation MDEC macroblock
// decoder as documented in the psx-spx MDEC chapter
// (sections 6.1 to 6.4), plus the BS Huffman layer from
// sections 13.19 to 13.21 as an explicitly marked
// extension (see bs.go).
//
// Hardware input format (section 6.4): a block is a
// sequence of 16 bit run length codes. The first code
// carries a 6 bit quant scale in bits 15 to 10 and a
// signed 10 bit DC value in bits 9 to 0. Following codes
// carry a 6 bit run of zero coefficients and a signed
// 10 bit AC level. The code FE00h is the end of block
// code and is also ignored when it appears where a new
// block is expected (padding between blocks, section
// 6.4, dummy halfwords). Codes are packed into 32 bit
// words, low halfword first.
//
// Important source note: the MDEC hardware itself has
// no Huffman or VLC stage and no bit reader. The
// Huffman coding (BS v1, v2, v3) lives in the CDROM
// file format layer above the MDEC: software decodes
// it into exactly the run length codes above and then
// feeds those to this hardware. This package therefore
// defaults to the hardware run length format, and the
// BS layer is available only through the extension in
// bs.go so that the two documented layers are not
// silently conflated.
package mdec

// Command kinds stored in the active field.
const (
	cmdNone   = 0
	cmdDecode = 1
	cmdQuant  = 2
	cmdScale  = 3
)

// Block decode states.
const (
	blkNeedDC = 0
	blkInAC   = 1
)

// MDEC is the macroblock decoder state.
type MDEC struct {
	// Control register (0x1F801824 write) stored bits.
	ctrlIn  bool // bit 30: enable data in request
	ctrlOut bool // bit 29: enable data out request

	// Status register stored fields (0x1F801824 read).
	statCopy   uint32 // command bits 28 to 25, shown at status bits 26 to 23
	curBlock   uint32 // status bits 18 to 16
	paramField uint32 // status bits 15 to 0
	dataInFull bool   // status bit 30

	// Current command intake.
	active     int // cmdNone, cmdDecode, cmdQuant or cmdScale
	cmdTotal   int // parameter words expected for the active command
	cmdRecv    int // parameter words received so far
	paramIdx   int // byte index (quant) or halfword index (scale)
	quantColor bool

	// Decode command format, from the command word.
	depth     int  // 0 = 4 bit mono, 1 = 8 bit mono, 2 = 24 bit, 3 = 15 bit
	signedOut bool // command bit 26
	stp       bool // command bit 25
	bsMode    bool // extension only, command bit 24, see bs.go

	// Tables loaded by MDEC(2) and MDEC(3).
	qY    [64]uint8
	qUV   [64]uint8
	scale [64]int16

	// Run length block decoder state.
	blkState int // blkNeedDC or blkInAC
	mbIdx    int // block inside the macroblock: 0 Cr, 1 Cb, 2..5 Y1..Y4; mono always 0
	scanK    int // scan position of the last stored coefficient
	qScale   int // quant scale from the current block DC code
	cur      [64]int32

	// Decoded (post IDCT) blocks of the current macroblock.
	crBlk [64]int32
	cbBlk [64]int32
	yBlk  [4][64]int32

	// BS extension state (bs.go).
	dcCr, dcCb, dcY int32 // BS v3 differential DC accumulators
	bsStage         int   // 0 header word 0, 1 header word 1, 2 bitstream
	bsEnded         bool
	bsQuant         int
	bsVersion       int
	bitQ            []uint16 // pending bitstream halfwords
	bitHead         int      // index of the current halfword in bitQ
	bitPos          int      // bits already consumed in bitQ[bitHead], from bit 15 down

	// Output FIFO. ReadWord pops from outHead.
	out       []uint32
	outHead   int
	outMBs    []mbInfo // one entry per queued macroblock, in order
	outIntoMB int      // words already read from the first queued macroblock
}

// mbInfo describes one queued macroblock in the output FIFO.
type mbInfo struct {
	words int // total output words for this macroblock
	depth int // output depth of the command that produced it
}

// New returns an MDEC in reset state.
func New() *MDEC {
	d := &MDEC{}
	d.Reset()
	return d
}

// Reset restores the power on state: tables cleared, no
// command active, output empty, and the status register
// reads 80040000h (data out FIFO empty, current block
// field 4), matching the value the document gives for a
// control reset in section 6.1.
func (d *MDEC) Reset() {
	keep := MDEC{curBlock: 4}
	*d = keep
}

// Control returns the stored control register bits:
// bit 30 (data in request enable) and bit 29 (data out
// request enable). The reset bit 31 is momentary and
// reads back as zero.
func (d *MDEC) Control() uint32 {
	var v uint32
	if d.ctrlIn {
		v |= 1 << 30
	}
	if d.ctrlOut {
		v |= 1 << 29
	}
	return v
}

// WriteControl handles a write to 0x1F801824.
// Bit 31 aborts any active command, clears the output
// FIFO and the input state, and restores the status
// fields to the reset value from section 6.1. The
// quant and scale tables survive a control reset (the
// document describes an abort, not a table clear).
// Bits 30 and 29 store the DMA request enables.
func (d *MDEC) WriteControl(w uint32) {
	if w&(1<<31) != 0 {
		d.active = cmdNone
		d.cmdRecv = 0
		d.cmdTotal = 0
		d.blkState = blkNeedDC
		d.mbIdx = 0
		d.scanK = 0
		d.qScale = 0
		d.out = nil
		d.outHead = 0
		d.outMBs = nil
		d.outIntoMB = 0
		d.bitQ = nil
		d.bitHead = 0
		d.bitPos = 0
		d.bsStage = 0
		d.bsEnded = false
		d.dcCr, d.dcCb, d.dcY = 0, 0, 0
		d.statCopy = 0
		d.curBlock = 4
		d.paramField = 0
		d.dataInFull = false
	}
	d.ctrlIn = w&(1<<30) != 0
	d.ctrlOut = w&(1<<29) != 0
}

// Status returns the status register read from
// 0x1F801824, with the bit layout of section 6.1:
//
//	31    data out FIFO empty
//	30    data in FIFO full (set here when the last
//	      parameter word of a command was received)
//	29    command busy (receiving or processing)
//	28    data in request (DMA in enabled)
//	27    data out request (DMA out enabled and data
//	      is pending)
//	26-23 output depth, signed and bit15 flags copied
//	      from command bits 28 to 25
//	22-19 zero
//	18-16 current block (0..3 Y1..Y4, 4 Cr, 5 Cb;
//	      mono always 4)
//	15-0  parameter words remaining minus 1 while a
//	      command is being received, FFFFh when a
//	      command has completed its intake, the value
//	      0 after reset, and the raw low bits after a
//	      no function command (see WriteWord)
func (d *MDEC) Status() uint32 {
	var s uint32
	if d.outLen() == 0 {
		s |= 1 << 31
	}
	if d.dataInFull {
		s |= 1 << 30
	}
	if d.active != cmdNone {
		s |= 1 << 29
	}
	if d.ctrlIn {
		s |= 1 << 28
	}
	if d.ctrlOut && d.outLen() > 0 {
		s |= 1 << 27
	}
	s |= (d.statCopy & 0xF) << 23
	s |= (d.curBlock & 7) << 16
	s |= d.paramField & 0xFFFF
	return s
}

// WriteWord handles a write to 0x1F801820 (also the
// DMA channel 0 entry point). When idle the word is a
// command word (section 6.2); otherwise it is the next
// parameter word of the active command.
func (d *MDEC) WriteWord(w uint32) {
	if d.active == cmdNone {
		d.startCommand(w)
		return
	}
	d.cmdRecv++
	switch d.active {
	case cmdQuant:
		// 64 unsigned bytes per table, packed into
		// words low byte first (16 words per table).
		for i := 0; i < 4; i++ {
			b := uint8(w >> (8 * i))
			if d.paramIdx < 64 {
				d.qY[d.paramIdx] = b
			} else {
				d.qUV[d.paramIdx-64] = b
			}
			d.paramIdx++
		}
	case cmdScale:
		// 64 signed halfwords, low halfword first
		// (32 words).
		d.scale[d.paramIdx] = int16(uint16(w))
		d.scale[d.paramIdx+1] = int16(uint16(w >> 16))
		d.paramIdx += 2
	case cmdDecode:
		if d.bsMode {
			d.bsWriteWord(w)
		} else {
			d.feedHalfword(uint16(w))
			d.feedHalfword(uint16(w >> 16))
		}
	}
	d.updateParamField()
	if d.cmdRecv >= d.cmdTotal {
		d.finishIntake()
	}
}

// startCommand decodes a command word (section 6.2).
func (d *MDEC) startCommand(w uint32) {
	// All commands copy bits 28 to 25 into status
	// bits 26 to 23.
	d.statCopy = (w >> 25) & 0xF
	d.dataInFull = false
	switch cmd := w >> 29; cmd {
	case 1: // decode macroblocks
		d.depth = int((w >> 27) & 3)
		d.signedOut = (w>>26)&1 == 1
		d.stp = (w>>25)&1 == 1
		d.bsMode = (w>>24)&1 == 1
		d.resetDecodeStream()
		total := int(w & 0xFFFF)
		if total == 0 {
			d.paramField = 0xFFFF
			return
		}
		d.active = cmdDecode
		d.cmdTotal = total
		d.cmdRecv = 0
		d.paramField = uint32(total - 1)
	case 2: // set quant tables
		d.quantColor = w&1 == 1
		total := 16
		if d.quantColor {
			total = 32
		}
		d.active = cmdQuant
		d.cmdTotal = total
		d.cmdRecv = 0
		d.paramIdx = 0
		d.paramField = uint32(total - 1)
	case 3: // set scale table
		d.active = cmdScale
		d.cmdTotal = 32
		d.cmdRecv = 0
		d.paramIdx = 0
		d.paramField = 31
	default:
		// MDEC(0) has no function and MDEC(4..7) are
		// invalid and act identical to MDEC(0), per
		// section 6.2: no parameters are expected,
		// bits 25 to 28 were reflected above, and
		// bits 0 to 15 are reflected into the status
		// low field without the minus 1 effect used
		// by MDEC(1). Unknown commands therefore do
		// nothing and never become busy.
		d.active = cmdNone
		d.paramField = w & 0xFFFF
	}
}

// updateParamField maintains status bits 15 to 0 while
// a command is being received: parameter words
// remaining minus 1.
func (d *MDEC) updateParamField() {
	rem := d.cmdTotal - d.cmdRecv
	if rem <= 0 {
		d.paramField = 0xFFFF
	} else {
		d.paramField = uint32(rem - 1)
	}
}

// finishIntake runs when the last parameter word of a
// command arrived. A partially decoded block at the
// end of a decode command is discarded.
func (d *MDEC) finishIntake() {
	d.active = cmdNone
	d.paramField = 0xFFFF
	d.dataInFull = true
	d.blkState = blkNeedDC
	d.mbIdx = 0
	d.bitQ = nil
	d.bitHead = 0
	d.bitPos = 0
	d.bsEnded = false
	d.bsStage = 0
}

// resetDecodeStream clears the per stream decoder
// state at the start of a decode command.
func (d *MDEC) resetDecodeStream() {
	d.blkState = blkNeedDC
	d.mbIdx = 0
	d.scanK = 0
	d.qScale = 0
	d.curBlock = 4 // Cr first, and mono is always 4
	d.dcCr, d.dcCb, d.dcY = 0, 0, 0
	d.bsStage = 0
	d.bsEnded = false
	d.bsQuant = 0
	d.bsVersion = 2
	d.bitQ = nil
	d.bitHead = 0
	d.bitPos = 0
}

func (d *MDEC) mono() bool { return d.depth == 0 || d.depth == 1 }

// quantTable returns the quant table for the block
// currently being decoded: color blocks (Cr, Cb) use
// the color table, luma blocks use the luminance
// table (section 6.2, MDEC(2)).
func (d *MDEC) quantTable() *[64]uint8 {
	if !d.mono() && d.mbIdx < 2 {
		return &d.qUV
	}
	return &d.qY
}

// blockStatusCode maps the internal macroblock block
// index to the status current block field of
// section 6.1.
func blockStatusCode(mbIdx int, mono bool) uint32 {
	if mono {
		return 4
	}
	switch mbIdx {
	case 0:
		return 4 // Cr
	case 1:
		return 5 // Cb
	default:
		return uint32(mbIdx - 2) // Y1..Y4 are 0..3
	}
}

func sign10(v uint16) int32 {
	v &= 0x3FF
	if v&0x200 != 0 {
		return int32(v) - 0x400
	}
	return int32(v)
}

func clampI32(v, lo, hi int32) int32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// feedHalfword is the hardware run length entry point:
// one 16 bit code from the decode parameter stream.
func (d *MDEC) feedHalfword(h uint16) {
	if d.blkState == blkNeedDC {
		if h == 0xFE00 {
			// Padding at the start of a block is
			// ignored (section 6.4).
			return
		}
		d.startBlock(h)
		return
	}
	d.acStep(h)
}

// startBlock consumes the first code of a block: the
// DC code (section 6.3, rl_decode_block). qt index 0
// is used without the quant scale; the special scale
// zero mode uses the raw value times 2 and no zigzag,
// as in the published pseudocode.
func (d *MDEC) startBlock(h uint16) {
	d.qScale = int(h>>10) & 0x3F
	raw := sign10(h)
	var v int32
	if d.qScale == 0 {
		v = raw * 2
	} else {
		v = raw * int32(d.quantTable()[0])
	}
	v = clampI32(v, -0x400, 0x3FF)
	d.cur = [64]int32{}
	d.cur[0] = v
	d.scanK = 0
	d.blkState = blkInAC
	d.curBlock = blockStatusCode(d.mbIdx, d.mono())
}

// acStep consumes one AC code (or the end of block
// code) inside a block, following rl_decode_block in
// section 6.3: the run field advances the scan
// position, and a position past 63 ends the block
// (which is how FE00h, with run field 63, acts as the
// end of block code).
func (d *MDEC) acStep(h uint16) {
	run := int(h>>10) & 0x3F
	newK := d.scanK + run + 1
	if newK > 63 {
		d.finishBlock()
		return
	}
	d.scanK = newK
	raw := sign10(h)
	var v int32
	if d.qScale == 0 {
		// Special mode without quant table and
		// without zigzag reordering.
		v = clampI32(raw*2, -0x400, 0x3FF)
		d.cur[d.scanK] = v
		return
	}
	qt := d.quantTable()
	v = (raw*int32(qt[d.scanK])*int32(d.qScale) + 4) / 8
	v = clampI32(v, -0x400, 0x3FF)
	d.cur[zagzig[d.scanK]] = v
}

// finishBlock runs the IDCT on the current block,
// stores it in its macroblock slot, and assembles and
// enqueues a macroblock when its last block arrived.
// Block order in a colored macroblock is Cr, Cb, Y1,
// Y2, Y3, Y4 (section 6.3); monochrome macroblocks
// consist of a single Y block.
func (d *MDEC) finishBlock() {
	spatial := d.idct(d.cur)
	d.blkState = blkNeedDC
	d.scanK = 0
	if d.mono() {
		d.yBlk[0] = spatial
		d.assembleMono()
		d.curBlock = 4
		return
	}
	switch d.mbIdx {
	case 0:
		d.crBlk = spatial
	case 1:
		d.cbBlk = spatial
	default:
		d.yBlk[d.mbIdx-2] = spatial
	}
	if d.mbIdx == 5 {
		d.assembleColor()
		d.mbIdx = 0
	} else {
		d.mbIdx++
	}
	d.curBlock = blockStatusCode(d.mbIdx, false)
}

// idct is the low level IDCT of section 6.3
// (real_idct_core): two passes of dst = src times the
// scale table, with the source processed column by
// column (diagonally mirrored, as the document puts
// it). Each pass computes
//
//	dst[x+y*8] = (sum + 0FFFh) / 2000h
//	sum = Sigma src[y+z*8] * (scaletable[x+z*8] / 8)
//
// The document itself marks the rounding with
// "or so?" and notes the hardware result is not
// perfect; this implementation uses Go integer
// division (truncation toward zero) for both the
// scale division by 8 (which also implements the
// documented use of only the upper 13 bits of the
// scale values) and the final division by 2000h, and
// 64 bit accumulation. That choice is the documented
// ambiguity for this stage; the tests allow for it
// with a plus or minus 2 tolerance against a float
// reference where the exact integer path is not
// asserted.
func (d *MDEC) idct(blk [64]int32) [64]int32 {
	var buf [64]int32
	src := blk
	dst := &buf
	for pass := 0; pass < 2; pass++ {
		for x := 0; x < 8; x++ {
			for y := 0; y < 8; y++ {
				var sum int64
				for z := 0; z < 8; z++ {
					sum += int64(src[y+z*8]) * int64(int32(d.scale[x+z*8])/8)
				}
				dst[x+y*8] = int32((sum + 0xFFF) / 0x2000)
			}
		}
		tmp := src
		src = *dst
		*dst = tmp
	}
	return src
}

// clip9 interprets the low 9 bits of a sample as a
// signed 9 bit value and saturates it to the signed
// 8 bit range, as in the y_to_mono pseudocode of
// section 6.3. It is applied to luma and chroma
// samples before color conversion as well; the
// document notes a 9 bit limit probably exists there
// too.
func clip9(v int32) int32 {
	v = int32(int16((v&0x1FF)<<7) >> 7)
	return clampI32(v, -128, 127)
}

// outLen returns the number of queued output words.
func (d *MDEC) outLen() int { return len(d.out) - d.outHead }

// enqueue appends one finished macroblock of output
// words to the output FIFO.
func (d *MDEC) enqueue(words []uint32, depth int) {
	if d.outHead > 0 && d.outHead == len(d.out) {
		d.out = d.out[:0]
		d.outHead = 0
	}
	d.out = append(d.out, words...)
	d.outMBs = append(d.outMBs, mbInfo{words: len(words), depth: depth})
}

// wordsPerMB returns the documented output word count
// of one macroblock for an output depth: 24 bit color
// has 256 pixels at 3 bytes (192 words), 15 bit color
// has 256 pixels at 2 bytes (128 words), 8 bit mono
// has 64 pixels (16 words) and 4 bit mono has 64
// pixels at 4 bits (8 words).
func wordsPerMB(depth int) int {
	switch depth {
	case 2:
		return 192
	case 3:
		return 128
	case 1:
		return 16
	default:
		return 8
	}
}

// ReadWord pulls one decoded output word (the data
// port read and the DMA channel 1 entry point).
// Output is produced in 8x8 block order: for colored
// macroblocks the four quadrants are emitted in Y1,
// Y2, Y3, Y4 order, each row by row left to right and
// top to bottom, which is the 8x8 bitmap order the
// document describes for this port in section 6.1
// (DMA channel 1 reorders macroblocks in RAM).
// When no output is pending ReadWord returns 0; the
// document says the hardware returns garbage in that
// case, and returning 0 is the defined behavior of
// this implementation.
func (d *MDEC) ReadWord() uint32 {
	if d.outLen() == 0 {
		return 0
	}
	v := d.out[d.outHead]
	d.outHead++
	d.outIntoMB++
	if len(d.outMBs) > 0 {
		mb := d.outMBs[0]
		if d.outIntoMB >= mb.words {
			d.outMBs = d.outMBs[1:]
			d.outIntoMB = 0
		} else if mb.depth == 2 || mb.depth == 3 {
			// While output is pending the current
			// block field names the output block:
			// quadrant index 0..3 for Y1..Y4.
			perQuadrant := mb.words / 4
			d.curBlock = uint32(d.outIntoMB / perQuadrant)
		} else {
			d.curBlock = 4
		}
	}
	if d.outHead == len(d.out) {
		d.out = d.out[:0]
		d.outHead = 0
	}
	return v
}
