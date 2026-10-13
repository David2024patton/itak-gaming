package homebrew

// Synthetic disc images carrying the enginetest EXE:
// a plain ISO9660 image and a cue/bin pair wrapping
// the same sectors. The image layout is fixed:
//
//	sectors 0 to 15   system area (zeros)
//	sector 16         primary volume descriptor
//	sector 20         root directory
//	sector 21         SYSTEM.CNF;1
//	sector 22 onward  ENGTEST.EXE;1
//
// SYSTEM.CNF names the EXE on its BOOT line, the way
// a real PlayStation disc does, so the system's
// BootDisc path (ISO9660 walk, SYSTEM.CNF parse, EXE
// load) is the exact code a commercial disc would
// take. The sector contents are otherwise inert: the
// guest's CD stage reads sector 0 and records a
// checksum of whatever is there, it does not compare
// it against a fixed value at runtime.

import (
	"encoding/binary"
	"fmt"
	"strings"
)

const (
	isoSectorSize  = 2048
	isoPVDSector   = 16
	isoRootSector  = 20
	isoCNFSector   = 21
	isoEXESector   = 22
	isoVolumeID    = "ITAK_ENGTEST"
	engtestCNFName = "SYSTEM.CNF;1"
	engtestEXEName = "ENGTEST.EXE;1"
)

// SystemCNF is the boot configuration file content:
// the BOOT line the disc boot parses, plus the
// customary TCB, EVENT and STACK lines.
func SystemCNF() string {
	return "BOOT = cdrom:\\ENGTEST.EXE;1\r\n" +
		"TCB = 4\r\n" +
		"EVENT = 16\r\n" +
		"STACK = 801FFF00\r\n"
}

// both16 writes a both-endian 16-bit value (little
// first, then big), the ISO9660 multi-byte format.
func both16(b []byte, v uint16) {
	binary.LittleEndian.PutUint16(b[0:], v)
	binary.BigEndian.PutUint16(b[2:], v)
}

// both32 writes a both-endian 32-bit value.
func both32(b []byte, v uint32) {
	binary.LittleEndian.PutUint32(b[0:], v)
	binary.BigEndian.PutUint32(b[4:], v)
}

// dirRecord builds one ISO9660 directory record.
// name is the raw stored name; a one-byte name is
// "\x00" for "." and "\x01" for "..".
func dirRecord(name string, extent, size int, isDir bool) []byte {
	nameLen := len(name)
	recLen := 33 + nameLen
	if recLen%2 != 0 {
		recLen++
	}
	b := make([]byte, recLen)
	b[0] = byte(recLen)
	both32(b[2:], uint32(extent))
	both32(b[10:], uint32(size))
	// Recording date: 2026-10-13, GMT. Only the year,
	// month and day are plausible; parsers ignore it.
	b[18] = 126 // years since 1900
	b[19] = 10
	b[20] = 13
	if isDir {
		b[25] = 2
	}
	both16(b[28:], 1) // volume sequence number
	b[32] = byte(nameLen)
	copy(b[33:], name)
	return b
}

// EnginetestISO builds the ISO9660 image carrying
// exe as its boot file. The image is a multiple of
// the 2048-byte sector size.
func EnginetestISO(exe []byte) []byte {
	exeSectors := (len(exe) + isoSectorSize - 1) / isoSectorSize
	total := isoEXESector + exeSectors
	img := make([]byte, total*isoSectorSize)

	// Primary volume descriptor at sector 16.
	pvd := img[isoPVDSector*isoSectorSize:]
	pvd[0] = 1
	copy(pvd[1:6], "CD001")
	pvd[6] = 1
	vid := isoVolumeID + strings.Repeat(" ", 32-len(isoVolumeID))
	copy(pvd[40:72], vid)
	both32(pvd[80:], uint32(total)) // volume space size
	both16(pvd[120:], 1)            // volume set size
	both16(pvd[124:], 1)            // volume sequence number
	both16(pvd[128:], isoSectorSize)
	rootRec := dirRecord("\x00", isoRootSector, isoSectorSize, true)
	copy(pvd[156:], rootRec)

	// Root directory at sector 20.
	root := img[isoRootSector*isoSectorSize:]
	off := 0
	put := func(r []byte) {
		copy(root[off:], r)
		off += len(r)
	}
	put(dirRecord("\x00", isoRootSector, isoSectorSize, true)) // .
	put(dirRecord("\x01", isoRootSector, isoSectorSize, true)) // ..
	put(dirRecord(engtestCNFName, isoCNFSector, len(SystemCNF()), false))
	put(dirRecord(engtestEXEName, isoEXESector, len(exe), false))

	// Files.
	copy(img[isoCNFSector*isoSectorSize:], SystemCNF())
	copy(img[isoEXESector*isoSectorSize:], exe)
	return img
}

// EnginetestDisc returns the cue sheet text and the
// bin contents for the enginetest disc: the ISO
// image as one MODE1/2048 track. The cue references
// "ENGTEST.BIN", so the two files belong side by
// side under that name.
func EnginetestDisc(exe []byte) (cue string, bin []byte) {
	bin = EnginetestISO(exe)
	cue = fmt.Sprintf("FILE \"ENGTEST.BIN\" BINARY\r\n  TRACK 01 MODE1/2048\r\n    INDEX 01 00:00:00\r\n")
	return cue, bin
}
