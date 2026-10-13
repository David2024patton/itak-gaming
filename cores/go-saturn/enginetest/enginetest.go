// Package enginetest builds the synthetic Saturn disc the
// SAT-4 full stack gate boots: a 2048 byte sector ISO9660
// image whose initial program is hand assembled SH-2 (built
// with the tiny assembler in asm.go) plus a hand assembled
// 68EC000 sound driver, all synthetic data generated here.
// Nothing in this package is a Sega artifact: the System ID
// is written from the published layout, the IP is original
// code written for this gate, and PAYLOAD.BIN is a byte
// pattern defined below.
//
// The IP runs only through system BootDisc. Its jobs, in
// order, each recorded as a numbered milestone in Work
// RAM-H at MilestoneAddress:
//
//	1: the master released the slave with SMPC SSHON and
//	   the slave answered the 42 to 43 handshake
//	2: the CD block read PAYLOAD.BIN from the ISO root
//	   through the command and data port path and its
//	   checksum matched the builder's checksum
//	3: VDP2 NBG0 and a VDP1 polygon are programmed (the
//	   frame probes match the SAT-2 gate's display)
//	4: the 68EC000 sound driver was uploaded to sound RAM
//	   and SNDON released it; the driver keys a PCM slot
//	   looping the sine table at unity pitch
//	5: the pad was read once through SMPC INTBACK
//	6: a backup RAM pattern round tripped through the
//	   odd byte bus path
//	7: the IP reached its idle loop
//
// On a failure the IP stores a stage code at
// MilestoneAddress+0x20 and parks: 1 slave handshake
// timeout is not implemented (the spin is unbounded by
// design; the host gate fails on its frame budget), 2 is
// checksum mismatch, 4 file not found in the root
// directory, 5 backup read back mismatch, 6 a CD command
// answered with the reject status byte.
package enginetest

import (
	"encoding/binary"
	"math"
)

// MilestoneAddress is the Work RAM-H address of the
// milestone block the IP writes.
const MilestoneAddress = 0x06004000

// Milestone block field offsets (bytes from
// MilestoneAddress).
const (
	MileMagic    = 0x00 // 0x53415434, "SAT4"
	MileCount    = 0x04 // number of milestones recorded
	MileOrder    = 0x08 // milestone IDs in hit order, one byte each
	MileChecksum = 0x10 // guest computed PAYLOAD.BIN checksum
	MileSize     = 0x14 // guest read PAYLOAD.BIN size
	MilePad      = 0x18 // four INTBACK bytes: port status, ID, data 1, data 2
	MileBackup   = 0x1C // 0xA5 when the backup round trip matched
	MileError    = 0x20 // failure stage code, 0 while healthy
	MileMaster   = 0x30 // master handshake cell: the master writes 42
	MileSlave    = 0x34 // slave handshake cell: the slave answers 43
	MileAlive    = 0x38 // slave alive counter, incremented in its idle loop
)

// MileMagicValue is the magic the IP stores at
// MilestoneAddress on entry.
const MileMagicValue = 0x53415434

// PadMask is the button mask the host sets before running
// the machine: Up, A, R and Start. In the SMPC INTBACK
// data format (active low) the expected report bytes are
// port status 0xF1, peripheral ID 0x02, data byte 1 0xE3
// (Right, Left, Down, C and B read 1; Up, Start and A
// read 0) and data byte 2 0x7F (R reads 0, the rest 1).
const PadMask = 0x1811 // Start | R | A | Up in system button bits

// ExpectedPad is the four byte INTBACK report for PadMask.
var ExpectedPad = [4]byte{0xF1, 0x02, 0xE3, 0x7F}

// PayloadSize is the size of PAYLOAD.BIN in bytes.
const PayloadSize = 1000

// Payload returns the PAYLOAD.BIN contents: a fixed byte
// pattern, byte i = (31*i + 7) mod 256.
func Payload() []byte {
	out := make([]byte, PayloadSize)
	for i := range out {
		out[i] = byte(31*i + 7)
	}
	return out
}

// PayloadChecksum is the additive byte checksum the IP
// computes over the payload (a 32 bit wrapping sum).
func PayloadChecksum() uint32 {
	var sum uint32
	for _, b := range Payload() {
		sum += uint32(b)
	}
	return sum
}

// SineTable returns the 256 sample 16 bit sine table the
// 68EC000 driver loops on SCSP slot 0, the same table the
// SCSP package gates use: at unity pitch the loop period
// is exactly 256 samples, 172.27 Hz at 44100 Hz.
func SineTable() []uint16 {
	out := make([]uint16, 256)
	for i := range out {
		v := int16(30000 * math.Sin(2*math.Pi*float64(i)/256))
		out[i] = uint16(v)
	}
	return out
}

// putText writes s into a fixed width field, padding with
// spaces, matching the System ID text field convention.
func putText(sec []byte, off int, width int, s string) {
	for i := 0; i < width; i++ {
		sec[off+i] = ' '
	}
	copy(sec[off:off+width], s)
}

// dirRecord builds one ISO9660 directory record with both
// endian extent and size fields.
func dirRecord(extent int, size int, flags byte, name []byte) []byte {
	length := 33 + len(name)
	if length%2 == 1 {
		length++
	}
	rec := make([]byte, length)
	rec[0] = byte(length)
	binary.LittleEndian.PutUint32(rec[2:6], uint32(extent))
	binary.BigEndian.PutUint32(rec[6:10], uint32(extent))
	binary.LittleEndian.PutUint32(rec[10:14], uint32(size))
	binary.BigEndian.PutUint32(rec[14:18], uint32(size))
	rec[25] = flags
	rec[32] = byte(len(name))
	copy(rec[33:], name)
	return rec
}
