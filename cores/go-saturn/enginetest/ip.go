package enginetest

import (
	"encoding/binary"
	"fmt"
)

// This file builds the initial program: hand assembled
// SH-2 (master and slave) plus the data it works on (the
// VDP poke table, the 68EC000 sound driver blob, the sine
// table, and the payload file name). The program is
// written against the documented device interfaces only:
// SMPC commands through COMREG with the SF handshake, CD
// block commands through CR1 to CR4 with the CMOK
// handshake and the one word per scanline data port
// (reads synchronized to the SCU HBlank-IN status bit),
// SCSP sound RAM uploads, SMPC INTBACK pad reports, and
// backup RAM through its odd byte window.
//
// Register plan for the master: R14 is the milestone
// block base, R13 the Work RAM-L sector buffer. The leaf
// subroutines (smpc_cmd, cd_cmd, copy_words) and the
// inlined fetch sequence use R0 to R12 as scratch and
// never touch R13 or R14.

// Device addresses used by the IP.
const (
	addrSMPCComReg = 0x0010001F
	addrSMPCSF     = 0x00100063
	addrSMPCOREGB  = 0x00100021 // OREG0; OREGs stride 2 from here
	addrBackupBase = 0x00180000
	addrSCUIMS     = 0x05FE00A0
	addrSCUIST     = 0x05FE00A4
	addrCDHIRQ     = 0x05800008
	addrCDCR1      = 0x05800018
	addrCDCR4      = 0x05800024
	addrCDDataPort = 0x05800028
	addrSoundRAM   = 0x05A00000
	addrSineRAM    = 0x05A02000
	addrSectorBuf  = 0x00200000
	addrStackM     = 0x060020E8 // Stack-M field of the loaded System ID
)

// ipDataAddrs carries the absolute RAM addresses of the
// data section, recomputed per assembly pass until the
// layout is stable.
type ipDataAddrs struct {
	table  uint32
	driver uint32
	sine   uint32
	name   uint32
}

// movC loads a small constant (0 to 255) into a register
// using the sign extended immediate plus an EXTU.B fixup
// where needed.
func movC(a *Asm, rn int, v int) {
	switch {
	case v >= -128 && v <= 127:
		a.MovImm(rn, v)
	case v >= 0 && v <= 255:
		a.MovImm(rn, v-256)
		a.ExtuB(rn, rn)
	default:
		panic("movC: constant out of range")
	}
}

// addC adds a positive constant to a register in
// immediate sized chunks.
func addC(a *Asm, rn int, v int) {
	for v > 127 {
		a.AddImm(rn, 127)
		v -= 127
	}
	if v > 0 {
		a.AddImm(rn, v)
	}
}

// mark emits the milestone recording sequence: the ID
// byte appends to the order log and the count increments.
func mark(a *Asm, id int) {
	a.LoadLDisp(0, 14, MileCount)
	a.MovReg(1, 14)
	a.AddImm(1, MileOrder)
	a.AddReg(1, 0)
	movC(a, 2, id)
	a.StoreB(1, 2)
	a.LoadLDisp(0, 14, MileCount)
	a.AddImm(0, 1)
	a.StoreLDisp(14, 0, MileCount)
}

var rejSeq int

// checkReject emits the response check after a cd_cmd:
// the status byte (the high byte of the CR1 response in
// R0) of 0xFF is the reject encoding and parks the IP at
// the stage 6 failure.
func checkReject(a *Asm) {
	rejSeq++
	skip := fmt.Sprintf("rejok%d", rejSeq)
	a.Shlr8(0)
	a.Shlr8(0)
	a.ExtuB(0, 0)
	a.MovImm(1, -1)
	a.ExtuB(1, 1)
	a.CmpEq(0, 1)
	a.Bf(skip)
	a.Nop()
	a.Bra("fail6")
	a.Nop()
	a.Label(skip)
}

// loadBE32 emits a big endian 32 bit load assembled from
// byte loads: the word at byte offset off from the base
// register lands in rdst. R0, R1 and R2 are scratch.
func loadBE32(a *Asm, rbase int, off int, rdst int) {
	a.MovReg(1, rbase)
	addC(a, 1, off)
	a.LoadBInc(0, 1)
	a.ExtuB(rdst, 0)
	for i := 0; i < 3; i++ {
		a.LoadBInc(0, 1)
		a.ExtuB(0, 0)
		a.Shll8(rdst)
		a.OrReg(rdst, 0)
	}
}

var fetchSeq int

// fetch emits the CD read sequence: with the FAD in R4,
// the sector count in R5 and the destination in R6, it
// plays the range into buffer partition 0, waits until
// the partition holds the sectors, pulls them through
// the data port with Get Then Delete, and waits for the
// transfer end flag. R0 to R12 are scratch.
func fetch(a *Asm) {
	fetchSeq++
	id := fetchSeq
	a.MovReg(10, 4) // FAD
	a.MovReg(11, 5) // count
	a.MovReg(12, 6) // dest
	// Play: start24 = 0x800000|FAD, end24 = 0x800000|FAD+count-1.
	a.LoadLit(0, 0x800000)
	a.AddReg(0, 10)
	a.MovReg(1, 0)
	a.Shlr8(1)
	a.Shlr8(1)
	movC(a, 2, 0x10)
	a.Shll8(2)
	a.AddReg(1, 2)
	a.MovReg(4, 1) // CR1
	a.MovReg(5, 0)
	a.ExtuW(5, 5) // CR2 = start24 low
	a.MovReg(0, 10)
	a.AddReg(0, 11)
	a.AddImm(0, -1)
	a.LoadLit(1, 0x800000)
	a.AddReg(0, 1)
	a.MovReg(6, 0)
	a.Shlr8(6)
	a.Shlr8(6) // CR3 = end24 high, play mode 0
	a.MovReg(7, 0)
	a.ExtuW(7, 7) // CR4 = end24 low
	a.Bsr("cd_cmd")
	a.Nop()
	checkReject(a)
	// Wait until partition 0 holds count sectors.
	ws := fmt.Sprintf("fws%d", id)
	a.Label(ws)
	movC(a, 4, 0x51)
	a.Shll8(4)
	a.MovImm(5, 0)
	a.MovImm(6, 0)
	a.MovImm(7, 0)
	a.Bsr("cd_cmd")
	a.Nop()
	a.CmpHS(1, 11) // R1 = buffered count from the CR4 response
	a.Bf(ws)
	a.Nop()
	// Get Then Delete the range into the destination.
	movC(a, 4, 0x63)
	a.Shll8(4)
	a.MovImm(5, 0)
	a.MovImm(6, 0)
	a.MovReg(7, 11)
	a.Bsr("cd_cmd")
	a.Nop()
	checkReject(a)
	// Read count*1024 words from the data port, one word
	// per scanline, synchronized on the HBlank-IN bit.
	a.MovReg(9, 11)
	a.Shll8(9)
	a.Shll2(9) // words = count * 1024
	a.MovReg(10, 12)
	a.LoadLit(3, addrCDDataPort)
	a.LoadLit(8, addrSCUIST)
	rd := fmt.Sprintf("frd%d", id)
	wt := fmt.Sprintf("fwt%d", id)
	dn := fmt.Sprintf("fdn%d", id)
	a.Label(rd)
	a.LoadW(0, 3)
	a.StoreW(10, 0)
	a.AddImm(10, 2)
	a.Dt(9)
	a.Bt(dn)
	a.Nop()
	a.MovImm(0, 0)
	a.StoreL(8, 0) // clear IST, HBlank-IN included
	a.Label(wt)
	a.LoadL(0, 8)
	a.TstImm(4) // HBlank-IN is IST bit 2
	a.Bt(wt)
	a.Nop()
	a.Bra(rd)
	a.Nop()
	a.Label(dn)
	// Wait for the transfer end flag (HIRQ EHST, bit 7)
	// so the pending delete lands before the next
	// command.
	a.LoadLit(8, addrCDHIRQ)
	eh := fmt.Sprintf("feh%d", id)
	a.Label(eh)
	a.LoadW(0, 8)
	a.TstImm(0x80)
	a.Bt(eh)
	a.Nop()
}

// buildIP assembles the IP code and data and returns the
// full IP image: System ID, code at offset 0x100, then
// the data section. ipSize is the header IP size; the
// image is exactly that long.
func buildIP(checksum uint32, ipSize int) []byte {
	codeLen := 0
	var words []uint16
	var data []byte
	for pass := 0; pass < 4; pass++ {
		dataOff := 0x100 + codeLen
		if dataOff%4 != 0 {
			dataOff += 4 - dataOff%4
		}
		base := uint32(0x06002000 + dataOff)
		table := vdpTable()
		driver := sndDriverBlob()
		sine := SineTable()
		name := []byte("PAYLOAD.BIN;1\x00")
		addrs := ipDataAddrs{
			table:  base,
			driver: base + uint32(len(table)),
			sine:   base + uint32(len(table)+len(driver)),
			name:   base + uint32(len(table)+len(driver)+2*len(sine)),
		}
		data = data[:0]
		data = append(data, table...)
		data = append(data, driver...)
		for _, w := range sine {
			var b [2]byte
			binary.BigEndian.PutUint16(b[:], w)
			data = append(data, b[:]...)
		}
		data = append(data, name...)
		words = assembleIP(addrs, checksum)
		if got := len(words) * 2; got == codeLen {
			break
		} else {
			codeLen = got
		}
		if pass == 3 {
			panic("enginetest: IP layout did not converge")
		}
	}

	img := make([]byte, ipSize)
	putText(img, 0x10, 16, "SEGA ENTERPRISES")
	putText(img, 0x20, 10, "MK-SAT4")
	putText(img, 0x2A, 6, "V1.000")
	putText(img, 0x30, 8, "20261013")
	putText(img, 0x38, 8, "CD-1/1")
	putText(img, 0x40, 10, "JTUE")
	putText(img, 0x50, 16, "JOYPAD")
	putText(img, 0x60, 112, "SAT-4 ENGINETEST")
	copy(img[0:16], "SEGA SEGASATURN ")
	binary.BigEndian.PutUint32(img[0xE0:0xE4], uint32(ipSize))
	binary.BigEndian.PutUint32(img[0xE8:0xEC], 0x06001000) // Stack-M
	binary.BigEndian.PutUint32(img[0xEC:0xF0], 0x06000800) // Stack-S
	binary.BigEndian.PutUint32(img[0xF0:0xF4], 0x06010000) // first read address
	for i, w := range words {
		binary.BigEndian.PutUint16(img[0x100+2*i:0x100+2*i+2], w)
	}
	dataOff := 0x100 + len(words)*2
	if dataOff%4 != 0 {
		dataOff += 4 - dataOff%4
	}
	copy(img[dataOff:], data)
	return img
}

// assembleIP emits the whole SH-2 program against the
// given data addresses.
func assembleIP(addrs ipDataAddrs, checksum uint32) []uint16 {
	rejSeq = 0
	fetchSeq = 0
	a := NewAsm(0x06002100)

	// Entry: the boot ROM enters both CPUs here with
	// their header stacks. SP equal to Stack-M means
	// this is the master; anything else is the slave.
	a.Label("entry")
	a.LoadLit(1, addrStackM)
	a.LoadL(1, 1)
	a.CmpEq(15, 1)
	a.Bt("master")
	a.Nop()
	a.Bra("slave")
	a.Nop()
	a.Pool()

	// ---- master ----
	a.Label("master")
	a.LoadLit(14, MilestoneAddress)
	a.LoadLit(13, addrSectorBuf)
	a.LoadLit(0, MileMagicValue)
	a.StoreL(14, 0)
	a.MovImm(0, 0)
	a.StoreLDisp(14, 0, MileCount)
	// Mask every SCU source and clear the status word:
	// this program polls status bits and takes no
	// interrupts.
	a.LoadLit(1, addrSCUIMS)
	a.MovImm(0, -1)
	a.StoreL(1, 0)
	a.LoadLit(1, addrSCUIST)
	a.MovImm(0, 0)
	a.StoreL(1, 0)
	// Slave handshake: write 42, release the slave, wait
	// for its 43.
	a.MovImm(0, 42)
	a.StoreLDisp(14, 0, MileMaster)
	movC(a, 0, 0x02) // SSHON
	a.Bsr("smpc_cmd")
	a.Nop()
	a.Label("wslave")
	a.LoadLDisp(0, 14, MileSlave)
	a.CmpEqImm(43)
	a.Bf("wslave")
	a.Nop()
	mark(a, 1)
	a.Pool()

	// CD on, then initialize the block.
	movC(a, 0, 0x08) // CDON
	a.Bsr("smpc_cmd")
	a.Nop()
	movC(a, 4, 0x04)
	a.Shll8(4)
	a.MovImm(5, 0)
	a.MovImm(6, 0)
	a.MovImm(7, 0)
	a.Bsr("cd_cmd")
	a.Nop()
	// Fetch the primary volume descriptor (LBA 16 is
	// FAD 166).
	movC(a, 4, 166)
	a.MovImm(5, 1)
	a.MovReg(6, 13)
	fetch(a)
	// Root directory extent from the root record at
	// PVD+156 (its big endian extent field is +6 into
	// the record).
	loadBE32(a, 13, 162, 7)
	movC(a, 0, 150)
	a.AddReg(7, 0)
	a.MovReg(4, 7)
	a.MovImm(5, 1)
	a.MovReg(6, 13)
	fetch(a)
	a.Pool()

	// Scan the root directory for PAYLOAD.BIN;1.
	a.MovReg(5, 13) // record pointer
	a.Label("scan")
	a.LoadB(0, 5)
	a.ExtuB(0, 0)
	a.TstReg(0, 0)
	a.Bf("scanlive")
	a.Nop()
	a.Bra("fail4") // zero length record: end of the used directory
	a.Nop()
	a.Label("scanlive")
	a.MovReg(1, 5)
	a.AddImm(1, 32)
	a.LoadB(0, 1)
	a.ExtuB(0, 0)
	movC(a, 2, 13)
	a.CmpEq(0, 2)
	a.Bt("scanname")
	a.Nop()
	a.Bra("nextrec")
	a.Nop()
	a.Label("scanname")
	a.MovReg(1, 5)
	a.AddImm(1, 33)
	a.LoadLit(2, addrs.name)
	movC(a, 3, 13)
	a.Label("cmpbyte")
	a.LoadB(0, 1)
	a.LoadB(4, 2)
	a.CmpEq(0, 4)
	a.Bt("byteok")
	a.Nop()
	a.Bra("nextrec")
	a.Nop()
	a.Label("byteok")
	a.AddImm(1, 1)
	a.AddImm(2, 1)
	a.Dt(3)
	a.Bf("cmpbyte")
	a.Nop()
	// Found: extent big endian at record+6, size at
	// record+14.
	loadBE32(a, 5, 6, 7)
	loadBE32(a, 5, 14, 8)
	a.Bra("haverec")
	a.Nop()
	a.Label("nextrec")
	a.LoadB(0, 5)
	a.ExtuB(0, 0)
	a.AddReg(5, 0)
	a.Bra("scan")
	a.Nop()
	a.Label("haverec")
	a.Pool()

	// Fetch the payload, then checksum its bytes. The
	// size is stashed in the milestone block first: the
	// fetch sequence uses every scratch register,
	// R8 included.
	a.StoreLDisp(14, 8, MileSize)
	movC(a, 0, 150)
	a.AddReg(7, 0)
	a.MovReg(4, 7)
	a.MovReg(5, 8)
	a.LoadLit(0, 2047)
	a.AddReg(5, 0)
	a.Shlr8(5)
	a.Shlr2(5)
	a.Shlr(5) // sector count = (size + 2047) / 2048
	a.MovReg(6, 13)
	fetch(a)
	a.MovReg(1, 13)
	a.LoadLDisp(2, 14, MileSize)
	a.MovImm(0, 0)
	a.Label("sumloop")
	a.LoadBInc(3, 1)
	a.ExtuB(3, 3)
	a.AddReg(0, 3)
	a.Dt(2)
	a.Bf("sumloop")
	a.Nop()
	a.StoreLDisp(14, 0, MileChecksum)
	a.LoadLit(1, checksum)
	a.LoadLDisp(0, 14, MileChecksum)
	a.CmpEq(0, 1)
	a.Bt("sumok")
	a.Nop()
	a.Bra("fail2")
	a.Nop()
	a.Label("sumok")
	mark(a, 2)
	a.Pool()

	// VDP: run the poke table (address, value) pairs,
	// terminated by a zero address.
	a.LoadLit(1, addrs.table)
	a.Label("vploop")
	a.LoadLInc(2, 1)
	a.LoadLInc(3, 1)
	a.TstReg(2, 2)
	a.Bt("vpdone")
	a.Nop()
	a.StoreW(2, 3)
	a.Bra("vploop")
	a.Nop()
	a.Label("vpdone")
	mark(a, 3)
	a.Pool()

	// Sound: upload the driver blob and the sine table,
	// then release the sound CPU.
	a.LoadLit(4, addrs.driver)
	a.LoadLit(5, addrSoundRAM)
	a.MovImm(6, 1)
	a.Shll8(6) // 256 words: the 512 byte blob
	a.Bsr("copy_words")
	a.Nop()
	a.LoadLit(4, addrs.sine)
	a.LoadLit(5, addrSineRAM)
	a.MovImm(6, 1)
	a.Shll8(6) // 256 words
	a.Bsr("copy_words")
	a.Nop()
	movC(a, 0, 0x06) // SNDON
	a.Bsr("smpc_cmd")
	a.Nop()
	mark(a, 4)
	a.Pool()

	// Pad: one INTBACK for port 1 peripheral data only
	// (IREG0 port 2 mode 3 skips the second port, IREG1
	// PEN set, IREG2 F0).
	a.LoadLit(1, 0x00100001)
	movC(a, 0, 0xC0)
	a.StoreB(1, 0)
	a.LoadLit(1, 0x00100003)
	movC(a, 0, 0x08)
	a.StoreB(1, 0)
	a.LoadLit(1, 0x00100005)
	movC(a, 0, 0xF0)
	a.StoreB(1, 0)
	movC(a, 0, 0x10) // INTBACK
	a.Bsr("smpc_cmd")
	a.Nop()
	a.LoadLit(1, addrSMPCOREGB)
	a.LoadLit(2, MilestoneAddress+MilePad)
	for i := 0; i < 4; i++ {
		a.LoadB(0, 1)
		a.StoreB(2, 0)
		if i < 3 {
			a.AddImm(1, 2)
			a.AddImm(2, 1)
		}
	}
	mark(a, 5)
	a.Pool()

	// Backup RAM: even offsets must read 0xFF; write a
	// 32 byte pattern through the odd byte path and read
	// it back byte for byte.
	a.LoadLit(1, addrBackupBase)
	a.LoadB(0, 1)
	a.CmpEqImm(-1)
	a.Bt("bkuok")
	a.Nop()
	a.Bra("fail5")
	a.Nop()
	a.Label("bkuok")
	a.LoadLit(1, addrBackupBase+1)
	movC(a, 2, 32)
	movC(a, 3, 0x41)
	a.Label("bkwr")
	a.StoreB(1, 3)
	a.AddImm(1, 2)
	a.AddImm(3, 1)
	a.Dt(2)
	a.Bf("bkwr")
	a.Nop()
	a.LoadLit(1, addrBackupBase+1)
	movC(a, 2, 32)
	movC(a, 3, 0x41)
	a.Label("bkrd")
	a.LoadB(0, 1)
	a.ExtuB(0, 0)
	a.CmpEq(0, 3)
	a.Bt("bkbyteok")
	a.Nop()
	a.Bra("fail5")
	a.Nop()
	a.Label("bkbyteok")
	a.AddImm(1, 2)
	a.AddImm(3, 1)
	a.Dt(2)
	a.Bf("bkrd")
	a.Nop()
	a.LoadLit(1, MilestoneAddress+MileBackup)
	movC(a, 0, 0xA5)
	a.StoreB(1, 0)
	mark(a, 6)
	mark(a, 7)
	a.Label("park")
	a.Bra("park")
	a.Nop()
	a.Pool()

	// Failure parks: store the stage code and park.
	a.Label("fail2")
	movC(a, 0, 2)
	a.Bra("failstore")
	a.Nop()
	a.Label("fail4")
	movC(a, 0, 4)
	a.Bra("failstore")
	a.Nop()
	a.Label("fail5")
	movC(a, 0, 5)
	a.Bra("failstore")
	a.Nop()
	a.Label("fail6")
	movC(a, 0, 6)
	a.Bra("failstore")
	a.Nop()
	a.Label("failstore")
	a.StoreLDisp(14, 0, MileError)
	a.Label("park2")
	a.Bra("park2")
	a.Nop()
	a.Pool()

	// ---- leaf subroutines ----

	// smpc_cmd: R0 is the command byte. Writes COMREG,
	// then waits out the SF handshake (SF sets as the
	// command executes and clears on the next poll).
	a.Label("smpc_cmd")
	a.LoadLit(1, addrSMPCComReg)
	a.StoreB(1, 0)
	a.LoadLit(1, addrSMPCSF)
	a.Label("scset")
	a.LoadB(0, 1)
	a.TstImm(1)
	a.Bt("scset")
	a.Nop()
	a.Label("scclr")
	a.LoadB(0, 1)
	a.TstImm(1)
	a.Bf("scclr")
	a.Nop()
	a.Rts()
	a.Nop()
	a.Pool()

	// cd_cmd: R4 to R7 are CR1 to CR4; returns the
	// response CR1 in R0 and CR4 in R1. The CRs are
	// written while CMOK is still set (so no poll can
	// execute a half written command), then HIRQ is
	// cleared to admit the command.
	a.Label("cd_cmd")
	a.LoadLit(8, addrCDHIRQ)
	a.Label("ccready")
	a.LoadW(0, 8)
	a.TstImm(1)
	a.Bt("ccready")
	a.Nop()
	a.LoadLit(9, addrCDCR1)
	a.StoreW(9, 4)
	a.AddImm(9, 4)
	a.StoreW(9, 5)
	a.AddImm(9, 4)
	a.StoreW(9, 6)
	a.AddImm(9, 4)
	a.StoreW(9, 7)
	a.MovImm(0, 0)
	a.StoreW(8, 0)
	a.Label("ccresp")
	a.LoadW(0, 8)
	a.TstImm(1)
	a.Bt("ccresp")
	a.Nop()
	a.LoadLit(9, addrCDCR1)
	a.LoadW(0, 9)
	a.LoadLit(9, addrCDCR4)
	a.LoadW(1, 9)
	a.Rts()
	a.Nop()
	a.Pool()

	// copy_words: R4 source, R5 destination, R6 word
	// count; copies upward.
	a.Label("copy_words")
	a.Label("cwloop")
	a.LoadWInc(0, 4)
	a.StoreW(5, 0)
	a.AddImm(5, 2)
	a.Dt(6)
	a.Bf("cwloop")
	a.Nop()
	a.Rts()
	a.Nop()
	a.Pool()

	// ---- slave ----
	// Waits for the master's 42, answers 43, then counts
	// forever in the alive cell.
	a.Label("slave")
	a.LoadLit(14, MilestoneAddress)
	a.Label("swait")
	a.LoadLDisp(0, 14, MileMaster)
	a.CmpEqImm(42)
	a.Bf("swait")
	a.Nop()
	movC(a, 0, 43)
	a.StoreLDisp(14, 0, MileSlave)
	a.Label("alive")
	a.LoadLDisp(0, 14, MileAlive)
	a.AddImm(0, 1)
	a.StoreLDisp(14, 0, MileAlive)
	a.Bra("alive")
	a.Nop()
	a.Pool()

	words, err := a.Build()
	if err != nil {
		panic("enginetest: assembling the IP: " + err.Error())
	}
	return words
}
