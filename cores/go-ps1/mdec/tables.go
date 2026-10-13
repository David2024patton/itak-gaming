package mdec

// Tables transcribed from the psx-spx document.
//
// zigzag is from section 6.3 (MDEC Decompression),
// the list headed "zigzag[0..63]". zagzig is its
// inverse, built with the rule the document gives:
// for i in 0..63, zagzig[zigzag[i]] = i.

var zigzag = [64]int{
	0, 1, 5, 6, 14, 15, 27, 28,
	2, 4, 7, 13, 16, 26, 29, 42,
	3, 8, 12, 17, 25, 30, 41, 43,
	9, 11, 18, 24, 31, 40, 44, 53,
	10, 19, 23, 32, 39, 45, 52, 54,
	20, 22, 33, 38, 46, 51, 55, 60,
	21, 34, 37, 47, 50, 56, 59, 61,
	35, 36, 48, 49, 57, 58, 62, 63,
}

var zagzig = buildZagzig()

func buildZagzig() [64]int {
	var z [64]int
	for i, v := range zigzag {
		z[v] = i
	}
	return z
}

// The standard scale table for MDEC(3) is not built
// into the hardware: software loads it with the set
// scale command. The usual values (printed in
// section 6.3 under set_scale_table) are transcribed
// in the tests, which load them through MDEC(3)
// exactly as software would.
//
// BS AC Huffman table, from section 13.21 (CDROM File
// Video BS Compression AC Values), the table headed
// "Huffman codes for AC values BS v1/v2/v3/iki". Each
// entry gives the code pattern and the 16 bit MDEC run
// length codes it maps to. In a pattern, 0 and 1 are
// fixed bits, the letter x bits form the index into
// the value list (first x is the most significant
// index bit), and the final letter s is the sign bit:
// when s is 1 the level in the low 10 bits of the MDEC
// code is negated, as the section states.
//
// Two codes are handled separately in bs.go: the end
// of block code 10 (MDEC code FE00h) and the escape
// code 000001, which is followed by a raw 16 bit MDEC
// code. The all zero 12 bit pattern is listed in the
// document as unused; it is included here marked as
// unused so the decoder can consume it deliberately
// instead of looping.

type acCode struct {
	pattern string
	values  []uint16
	unused  bool
}

var acTable = []acCode{
	{pattern: "11s", values: []uint16{0x0001}},
	{pattern: "011s", values: []uint16{0x0401}},
	{pattern: "010xs", values: []uint16{0x0002, 0x0801}},
	{pattern: "0011xs", values: []uint16{0x1001, 0x0C01}},
	{pattern: "00101s", values: []uint16{0x0003}},
	{pattern: "00100xxxs", values: []uint16{0x3401, 0x0006, 0x3001, 0x2C01, 0x0C02, 0x0403, 0x0005, 0x2801}},
	{pattern: "0001xxs", values: []uint16{0x1C01, 0x1801, 0x0402, 0x1401}},
	{pattern: "00001xxs", values: []uint16{0x0802, 0x2401, 0x0004, 0x2001}},
	{pattern: "0000001xxxs", values: []uint16{0x4001, 0x1402, 0x0007, 0x0803, 0x0404, 0x3C01, 0x3801, 0x1002}},
	{pattern: "00000001xxxxs", values: []uint16{0x000B, 0x2002, 0x1003, 0x000A, 0x0804, 0x1C02, 0x5401, 0x5001, 0x0009, 0x4C01, 0x4801, 0x0405, 0x0C03, 0x0008, 0x1802, 0x4401}},
	{pattern: "000000001xxxxs", values: []uint16{0x2802, 0x2402, 0x1403, 0x0C04, 0x0805, 0x0407, 0x0406, 0x000F, 0x000E, 0x000D, 0x000C, 0x6801, 0x6401, 0x6001, 0x5C01, 0x5801}},
	{pattern: "0000000001xxxxs", values: []uint16{0x001F, 0x001E, 0x001D, 0x001C, 0x001B, 0x001A, 0x0019, 0x0018, 0x0017, 0x0016, 0x0015, 0x0014, 0x0013, 0x0012, 0x0011, 0x0010}},
	{pattern: "00000000001xxxxs", values: []uint16{0x0028, 0x0027, 0x0026, 0x0025, 0x0024, 0x0023, 0x0022, 0x0021, 0x0020, 0x040E, 0x040D, 0x040C, 0x040B, 0x040A, 0x0409, 0x0408}},
	{pattern: "000000000001xxxxs", values: []uint16{0x0412, 0x0411, 0x0410, 0x040F, 0x1803, 0x4002, 0x3C02, 0x3802, 0x3402, 0x3002, 0x2C02, 0x7C01, 0x7801, 0x7401, 0x7001, 0x6C01}},
	{pattern: "000000000000", unused: true},
}

// BS v3 DC Huffman categories, from section 13.20
// (CDROM File Video BS Compression DC Values), the
// table headed "DC v3". Each row is one magnitude
// category: a fixed prefix for chroma (Cr and Cb) and
// a fixed prefix for luma (Y1..Y4), followed (except
// for the zero row) by a sign bit s and by index bits
// that select the magnitude inside the category
// range. The offset added to the previous DC of the
// same component is plus or minus (base + index)
// times 4, with s equal to 0 for negative and 1 for
// positive, as the section states. The number of
// index bits is the base 2 logarithm of the category
// range size, which matches the count of index bits
// printed in the document patterns.
//
// The end of frame code (ten 1 bits), and the two
// patterns the document lists as unused (111111110
// and 1111111110), are handled in bs.go before this
// table is consulted for the end of frame case; an
// unused pattern that is followed by enough bits is
// indistinguishable from the longest category with
// sign bit 0 and is decoded as that category, which
// is the documented ambiguity choice made here.

type dcCategory struct {
	base      int // first magnitude of the category (0, 1, 2, 4, ...)
	indexBits int // number of index bits after the sign bit
	chromaPre string
	lumaPre   string
}

var dcCategories = []dcCategory{
	{base: 0, indexBits: 0, chromaPre: "00", lumaPre: "100"},
	{base: 1, indexBits: 0, chromaPre: "01", lumaPre: "00"},
	{base: 2, indexBits: 1, chromaPre: "10", lumaPre: "01"},
	{base: 4, indexBits: 2, chromaPre: "110", lumaPre: "101"},
	{base: 8, indexBits: 3, chromaPre: "1110", lumaPre: "110"},
	{base: 16, indexBits: 4, chromaPre: "11110", lumaPre: "1110"},
	{base: 32, indexBits: 5, chromaPre: "111110", lumaPre: "11110"},
	{base: 64, indexBits: 6, chromaPre: "1111110", lumaPre: "111110"},
	{base: 128, indexBits: 7, chromaPre: "11111110", lumaPre: "1111110"},
}
