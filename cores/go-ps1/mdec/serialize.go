package mdec

import "fmt"

// Serialization layout (all integers little endian).
// StateSize is fixed; queues are stored up to the
// capacities below, which are far above anything a
// single realistic decode burst needs. If a queue is
// longer than its capacity when Serialize is called,
// it is truncated to the capacity; that limitation is
// stated here rather than hidden.
//
//	0..255     header: 64 uint32 slots (see below)
//	256..319   luminance quant table (64 bytes)
//	320..383   color quant table (64 bytes)
//	384..511   scale table (64 int16)
//	512..767   current block coefficients (64 int32)
//	768..1023  Cr block (64 int32)
//	1024..1279 Cb block (64 int32)
//	1280..2303 Y blocks (4 x 64 int32)
//	2304..35071   BS bit queue (16384 uint16)
//	35072..166143 output queue (32768 uint32)
//	166144..198911 output macroblock infos (4096 x 2 uint32)
//
// Header slots:
//
//	0 magic 0x4D444543, 1 control enables, 2 status
//	copy nibble, 3 current block, 4 parameter field,
//	5 data in full flag, 6 active command kind,
//	7 command total words, 8 command received words,
//	9 parameter index, 10 quant color flag,
//	11 output depth, 12 signed output flag,
//	13 STP flag, 14 BS mode flag, 15 block state,
//	16 macroblock block index, 17 scan position,
//	18 quant scale, 19 DC Cr accumulator,
//	20 DC Cb accumulator, 21 DC Y accumulator,
//	22 BS stage, 23 BS ended flag, 24 BS quant,
//	25 BS version, 26 bit queue count,
//	27 bit queue head bit position, 28 output count,
//	29 output words read in current macroblock,
//	30 output macroblock info count, 31 active
//	decode format word (depth, signed, STP and BS
//	mode re-packed), 32..63 reserved zero.
const (
	serHdrSize   = 256
	serQYOff     = 256
	serQUVOff    = 320
	serScaleOff  = 384
	serCurOff    = 512
	serCrOff     = 768
	serCbOff     = 1024
	serYOff      = 1280
	serBitQOff   = 2304
	serBitQCap   = 16384 // halfwords
	serOutOff    = serBitQOff + serBitQCap*2
	serOutCap    = 32768 // words
	serMBOff     = serOutOff + serOutCap*4
	serMBCap     = 4096
	serTotalSize = serMBOff + serMBCap*8
)

// StateSize is the serialized MDEC state size.
const StateSize = serTotalSize

const serMagic = 0x4D444543

func put32(b []byte, off int, v uint32) {
	b[off] = byte(v)
	b[off+1] = byte(v >> 8)
	b[off+2] = byte(v >> 16)
	b[off+3] = byte(v >> 24)
}

func get32(b []byte, off int) uint32 {
	return uint32(b[off]) | uint32(b[off+1])<<8 | uint32(b[off+2])<<16 | uint32(b[off+3])<<24
}

func putI32(b []byte, off int, v int32) { put32(b, off, uint32(v)) }
func getI32(b []byte, off int) int32    { return int32(get32(b, off)) }

func boolU32(v bool) uint32 {
	if v {
		return 1
	}
	return 0
}

// Serialize captures the full internal state: tables,
// the run length block decoder position (including a
// partially decoded block), the BS bit reader
// position within a partially consumed stream, the
// current command intake counters, the DC
// accumulators, and the output queue contents and
// drain position, so a save and restore in the
// middle of a decode command continues identically.
func (d *MDEC) Serialize() []byte {
	out := make([]byte, StateSize)
	slot := func(i int, v uint32) { put32(out, i*4, v) }
	slot(0, serMagic)
	slot(1, boolU32(d.ctrlIn)<<30|boolU32(d.ctrlOut)<<29)
	slot(2, d.statCopy)
	slot(3, d.curBlock)
	slot(4, d.paramField)
	slot(5, boolU32(d.dataInFull))
	slot(6, uint32(d.active))
	slot(7, uint32(d.cmdTotal))
	slot(8, uint32(d.cmdRecv))
	slot(9, uint32(d.paramIdx))
	slot(10, boolU32(d.quantColor))
	slot(11, uint32(d.depth))
	slot(12, boolU32(d.signedOut))
	slot(13, boolU32(d.stp))
	slot(14, boolU32(d.bsMode))
	slot(15, uint32(d.blkState))
	slot(16, uint32(d.mbIdx))
	slot(17, uint32(d.scanK))
	slot(18, uint32(d.qScale))
	slot(19, uint32(d.dcCr))
	slot(20, uint32(d.dcCb))
	slot(21, uint32(d.dcY))
	slot(22, uint32(d.bsStage))
	slot(23, boolU32(d.bsEnded))
	slot(24, uint32(d.bsQuant))
	slot(25, uint32(d.bsVersion))
	copy(out[serQYOff:serQYOff+64], d.qY[:])
	copy(out[serQUVOff:serQUVOff+64], d.qUV[:])
	for i := 0; i < 64; i++ {
		out[serScaleOff+i*2] = byte(uint16(d.scale[i]))
		out[serScaleOff+i*2+1] = byte(uint16(d.scale[i]) >> 8)
		putI32(out, serCurOff+i*4, d.cur[i])
		putI32(out, serCrOff+i*4, d.crBlk[i])
		putI32(out, serCbOff+i*4, d.cbBlk[i])
		for q := 0; q < 4; q++ {
			putI32(out, serYOff+(q*64+i)*4, d.yBlk[q][i])
		}
	}
	// Bit queue, stored from the head.
	nb := 0
	for i := d.bitHead; i < len(d.bitQ) && nb < serBitQCap; i++ {
		v := d.bitQ[i]
		out[serBitQOff+nb*2] = byte(v)
		out[serBitQOff+nb*2+1] = byte(v >> 8)
		nb++
	}
	slot(26, uint32(nb))
	slot(27, uint32(d.bitPos))
	// Output queue, stored from the head.
	no := 0
	for i := d.outHead; i < len(d.out) && no < serOutCap; i++ {
		put32(out, serOutOff+no*4, d.out[i])
		no++
	}
	slot(28, uint32(no))
	slot(29, uint32(d.outIntoMB))
	nm := 0
	for _, mb := range d.outMBs {
		if nm >= serMBCap {
			break
		}
		put32(out, serMBOff+nm*8, uint32(mb.words))
		put32(out, serMBOff+nm*8+4, uint32(mb.depth))
		nm++
	}
	slot(30, uint32(nm))
	return out
}

// Deserialize restores a state produced by Serialize.
// It returns an error when the input has the wrong
// size or does not carry the MDEC state magic.
func (d *MDEC) Deserialize(in []byte) error {
	if len(in) != StateSize {
		return fmt.Errorf("mdec: state size %d, want %d", len(in), StateSize)
	}
	if get32(in, 0) != serMagic {
		return fmt.Errorf("mdec: bad state magic %08x", get32(in, 0))
	}
	slot := func(i int) uint32 { return get32(in, i*4) }
	ctrl := slot(1)
	d.ctrlIn = ctrl&(1<<30) != 0
	d.ctrlOut = ctrl&(1<<29) != 0
	d.statCopy = slot(2)
	d.curBlock = slot(3)
	d.paramField = slot(4)
	d.dataInFull = slot(5) != 0
	d.active = int(slot(6))
	d.cmdTotal = int(slot(7))
	d.cmdRecv = int(slot(8))
	d.paramIdx = int(slot(9))
	d.quantColor = slot(10) != 0
	d.depth = int(slot(11))
	d.signedOut = slot(12) != 0
	d.stp = slot(13) != 0
	d.bsMode = slot(14) != 0
	d.blkState = int(slot(15))
	d.mbIdx = int(slot(16))
	d.scanK = int(slot(17))
	d.qScale = int(slot(18))
	d.dcCr = int32(slot(19))
	d.dcCb = int32(slot(20))
	d.dcY = int32(slot(21))
	d.bsStage = int(slot(22))
	d.bsEnded = slot(23) != 0
	d.bsQuant = int(slot(24))
	d.bsVersion = int(slot(25))
	copy(d.qY[:], in[serQYOff:serQYOff+64])
	copy(d.qUV[:], in[serQUVOff:serQUVOff+64])
	for i := 0; i < 64; i++ {
		d.scale[i] = int16(uint16(in[serScaleOff+i*2]) | uint16(in[serScaleOff+i*2+1])<<8)
	}
	for i := 0; i < 64; i++ {
		d.cur[i] = getI32(in, serCurOff+i*4)
		d.crBlk[i] = getI32(in, serCrOff+i*4)
		d.cbBlk[i] = getI32(in, serCbOff+i*4)
		for q := 0; q < 4; q++ {
			d.yBlk[q][i] = getI32(in, serYOff+(q*64+i)*4)
		}
	}
	nb := int(slot(26))
	if nb > serBitQCap {
		nb = serBitQCap
	}
	d.bitQ = make([]uint16, nb)
	for i := 0; i < nb; i++ {
		d.bitQ[i] = uint16(in[serBitQOff+i*2]) | uint16(in[serBitQOff+i*2+1])<<8
	}
	d.bitHead = 0
	d.bitPos = int(slot(27))
	no := int(slot(28))
	if no > serOutCap {
		no = serOutCap
	}
	d.out = make([]uint32, no)
	for i := 0; i < no; i++ {
		d.out[i] = get32(in, serOutOff+i*4)
	}
	d.outHead = 0
	d.outIntoMB = int(slot(29))
	nm := int(slot(30))
	if nm > serMBCap {
		nm = serMBCap
	}
	d.outMBs = make([]mbInfo, nm)
	for i := 0; i < nm; i++ {
		d.outMBs[i] = mbInfo{
			words: int(get32(in, serMBOff+i*8)),
			depth: int(get32(in, serMBOff+i*8+4)),
		}
	}
	return nil
}
