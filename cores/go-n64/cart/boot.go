package cart

// CIC boot constants and the IPL3 checksum, from the
// published N64 documentation (n64brew PIF-NUS page,
// "IPL3 checksum algorithm" table, and the public
// n64crc description of the algorithm).
//
// The CIC chip in a cartridge reports two seed bytes to
// the PIF at power-on. In every known CIC variant the
// IPL2 seed and the IPL3 seed are the same byte. The
// IPL3 checksum then initializes its accumulators with
// seed*magic+1 (truncated to 32 bits), where the magic
// depends on the CIC family. No CIC or PIF code is
// reproduced here; only these published constants and
// the published checksum algorithm, applied to the
// ROM's own bytes.

// CICSeed returns the documented seed byte for a CIC
// family (6101/6102: 0x3F, 6103: 0x78, 6105: 0x91,
// 6106: 0x85).
func CICSeed(c CIC) (byte, bool) {
	switch c {
	case CIC6101, CIC6102:
		return 0x3F, true
	case CIC6103:
		return 0x78, true
	case CIC6105:
		return 0x91, true
	case CIC6106:
		return 0x85, true
	}
	return 0, false
}

// cicMagic returns the 32-bit IPL3 checksum magic for a
// CIC family: 0x5D588B65 for 6101/6102/6105, 0x6C078965
// for 6103/6106.
func cicMagic(c CIC) (uint32, bool) {
	switch c {
	case CIC6101, CIC6102, CIC6105:
		return 0x5D588B65, true
	case CIC6103, CIC6106:
		return 0x6C078965, true
	}
	return 0, false
}

// CICChecksumInit returns the initial accumulator value
// of the IPL3 checksum for a CIC family:
// seed*magic+1 truncated to 32 bits. The published
// values are 0xF8CA4DDC (6101/6102), 0xA3886759 (6103),
// 0xDF26F436 (6105), and 0x1FEA617A (6106).
func CICChecksumInit(c CIC) (uint32, bool) {
	seed, ok := CICSeed(c)
	if !ok {
		return 0, false
	}
	magic, ok := cicMagic(c)
	if !ok {
		return 0, false
	}
	return uint32(seed)*magic + 1, true
}

func rol32(v uint32, n uint32) uint32 {
	n &= 31
	return v<<n | v>>(32-n)
}

// IPL3Checksum computes the two documented checksum
// words over the ROM region 0x1000..0x101000 (the first
// megabyte after the header and boot area), using the
// CIC family's seed and magic. Bytes past the end of a
// short ROM read as zero. The results are the two words
// a ROM header stores at offsets 0x10 and 0x14. When the
// CIC family is unknown, ok is false.
func IPL3Checksum(rom []byte, c CIC) (crc1, crc2 uint32, ok bool) {
	init, ok := CICChecksumInit(c)
	if !ok {
		return 0, 0, false
	}
	word := func(off int) uint32 {
		var v uint32
		for i := 0; i < 4; i++ {
			v <<= 8
			if off+i < len(rom) {
				v |= uint32(rom[off+i])
			}
		}
		return v
	}
	t1, t2, t3, t4, t5, t6 := init, init, init, init, init, init
	for i := 0; i < 0x100000; i += 4 {
		d := word(0x1000 + i)
		if t6+d < t6 {
			t4++
		}
		t6 += d
		t3 ^= d
		r := rol32(d, d&0x1F)
		t5 += r
		if t2 > d {
			t2 ^= r
		} else {
			// The published algorithm uses the already
			// updated t6 (after d was added) here.
			t2 ^= t6 ^ d
		}
		if c == CIC6105 {
			// The 6105 variant mixes in a word read from
			// the boot area at 0x750 + (i & 0xFF).
			t1 += word(0x750+(i&0xFF)) ^ d
		} else {
			t1 += t5 ^ d
		}
	}
	return t6 ^ t4 ^ t3, t5 ^ t2 ^ t1, true
}
