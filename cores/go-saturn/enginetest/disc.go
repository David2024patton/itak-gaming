package enginetest

import "encoding/binary"

// Disc layout: the IP occupies sectors 0 to 7 (its
// header size is 0x4000), the primary volume descriptor
// is sector 16, the volume descriptor terminator sector
// 17, the root directory sector 18, and PAYLOAD.BIN
// sector 19. The image is 24 sectors of zeros beyond
// that.

// IPSize is the header IP size of the enginetest disc
// and the length of the IP image at its start.
const IPSize = 0x4000

// BuildDisc returns the complete synthetic ISO9660 disc
// image for the enginetest gate.
func BuildDisc() []byte {
	ip := buildIP(PayloadChecksum(), IPSize)
	const sectors = 24
	img := make([]byte, sectors*2048)
	copy(img, ip)

	pvd := img[16*2048 : 17*2048]
	pvd[0] = 1
	copy(pvd[1:6], "CD001")
	pvd[6] = 1
	copy(pvd[156:190], dirRecord(18, 2048, 2, []byte{0}))

	term := img[17*2048 : 18*2048]
	term[0] = 255
	copy(term[1:6], "CD001")
	term[6] = 1

	root := img[18*2048 : 19*2048]
	off := 0
	for _, rec := range [][]byte{
		dirRecord(18, 2048, 2, []byte{0}),
		dirRecord(18, 2048, 2, []byte{1}),
		dirRecord(19, PayloadSize, 0, []byte("PAYLOAD.BIN;1")),
	} {
		copy(root[off:], rec)
		off += len(rec)
	}

	copy(img[19*2048:], Payload())
	return img
}

// vdpTable returns the (address, value) poke table the
// IP runs to program the display, byte identical in
// effect to the SAT-2 gate's table in the system tests:
// VDP2 NBG0 with a two color character (green then red
// on the first row) and a white VDP1 polygon at
// (100,50) to (119,69) whose priority beats the
// background. The table is terminated by a zero address.
func vdpTable() []byte {
	e := []uint32{
		0x05F80000, 0x8000, // TVMD: DISP on, 320x224
		0x05F80020, 0x0001, // BGON: NBG0 on
		0x05F80028, 0x0000, // CHCTLA: NBG0 16 colors, cells, 1x1
		0x05F80030, 0x8020, // PNCN0: 1 word, SPLT = 1
		0x05F80078, 0x0001, // ZMXIN0
		0x05F8007C, 0x0001, // ZMYIN0
		0x05F800F8, 0x0002, // PRINA: NBG0 priority 2
		0x05F800F4, 0x0007, // PRISC: sprite priority 7
		0x05F00202, 0x03E0, // CRAM palette 16 code 1: green
		0x05F00204, 0x7C00, // CRAM palette 16 code 2: red
		0x05F0003E, 0x7FFF, // CRAM sprite color 31: white
		0x05E00000, 0x0001, // pattern name (0,0): character 1
		0x05E00020, 0x1211, // character row 0: codes 1,2,1,1
	}
	for i := 1; i < 16; i++ {
		e = append(e, 0x05E00020+uint32(2*i), 0x1111)
	}
	e = append(e,
		0x05C00000, 0x0004, // CMDCTRL: polygon
		0x05C00002, 0x0000, // CMDLINK
		0x05C00004, 0x0000, // CMDPMOD
		0x05C00006, 0x801F, // CMDCOLR: color 31
		0x05C00008, 0x0000, // CMDSRCA
		0x05C0000A, 0x0000, // CMDSIZE
		0x05C0000C, 100, // XA
		0x05C0000E, 50, // YA
		0x05C00010, 119, // XB
		0x05C00012, 50, // YB
		0x05C00014, 119, // XC
		0x05C00016, 69, // YC
		0x05C00018, 100, // XD
		0x05C0001A, 69, // YD
		0x05C0001C, 0x0000, // CMDGRDA
		0x05C00020, 0x8000, // END
		0x05D00000, 0x0000, // TVMR
		0x05D00002, 0x0000, // FBCR
		0x05D00004, 0x0000, // PTMR
		0x05D00006, 0x0000, // EWDR
		0x05D00008, 0x0000, // EWLR
		0x05D0000A, 0x4EDF, // EWRR
	)
	out := make([]byte, 0, len(e)*4+8)
	for _, v := range e {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], v)
		out = append(out, b[:]...)
	}
	return append(out, 0, 0, 0, 0, 0, 0, 0, 0)
}

// sndDriverBlob returns the 512 byte sound RAM image the
// IP uploads before SNDON: the 68EC000 reset vectors
// (SSP 0x0007F000, PC 0x00000100), zero padding, and at
// 0x100 a hand assembled driver that programs SCSP slot
// 0 exactly as the SCSP package gates do (master volume
// 15, forward loop over the 256 sample table at sound
// RAM 0x2000, instant attack, unity pitch, direct send
// 7) and keys it on, then spins.
//
// Every driver instruction is MOVE.W #imm,(abs).L
// (0x33FC) except the final BRA.S self (0x60FE).
func sndDriverBlob() []byte {
	blob := make([]byte, 512)
	binary.BigEndian.PutUint32(blob[0:4], 0x0007F000)
	binary.BigEndian.PutUint32(blob[4:8], 0x00000100)
	code := []uint32{
		// addr, value pairs for MOVE.W #value, addr
		0x100400, 0x800F, // RegMaster: MEM4MB, MVOL 15
		0x100000, 0x4100, // slot 0 ctrl: KYONB, forward loop
		0x100002, 0x2000, // SA low: table at 0x2000
		0x100004, 0x0000, // LSA
		0x100006, 0x00FF, // LEA 255
		0x100008, 0x001F, // EG1: AR 31 instant
		0x10000A, 0x7800, // EG2: KRS 15, scaling off
		0x10000C, 0x0000, // Vol: TL 0
		0x100010, 0x0000, // Pitch: unity
		0x100016, 0xE000, // OutSel: DISDL 7, centre
		0x100000, 0xC100, // slot 0 ctrl: KYONEX on
	}
	off := 0x100
	put := func(w uint16) {
		binary.BigEndian.PutUint16(blob[off:off+2], w)
		off += 2
	}
	for i := 0; i < len(code); i += 2 {
		put(0x33FC)
		put(uint16(code[i+1]))
		put(uint16(code[i] >> 16))
		put(uint16(code[i]))
	}
	put(0x60FE) // BRA.S self
	return blob
}
