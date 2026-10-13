package aica

import "math"

// Rate time tables from the AICA documentation. A value of -1 means
// infinite time (no movement). A value of 0 in the attack table means
// instant (the two fastest attack rates).
var attackTimesMs = [64]float64{
	-1, -1, 8100, 6900, 6000, 4800, 4000, 3400,
	3000, 2400, 2000, 1700, 1500, 1200, 1000, 860,
	760, 600, 500, 430, 380, 300, 250, 220,
	190, 150, 130, 110, 95, 76, 63, 55,
	47, 38, 31, 27, 24, 19, 15, 13,
	12, 9.4, 7.9, 6.8, 6.0, 4.7, 3.8, 3.4,
	3.0, 2.4, 2.0, 1.8, 1.6, 1.3, 1.1, 0.93,
	0.85, 0.65, 0.53, 0.44, 0.40, 0.35, 0, 0,
}

var decayTimesMs = [64]float64{
	-1, -1, 118200, 101300, 88600, 70900, 59100, 50700,
	44300, 35500, 29600, 25300, 22200, 17700, 14800, 12700,
	11100, 8900, 7400, 6300, 5500, 4400, 3700, 3200,
	2800, 2200, 1800, 1600, 1400, 1100, 920, 790,
	690, 550, 460, 390, 340, 270, 230, 200,
	170, 140, 110, 98, 85, 68, 57, 49,
	43, 34, 28, 25, 22, 18, 14, 12,
	11, 8.5, 7.1, 6.1, 5.4, 4.3, 3.6, 3.1,
}

// LFO frequencies in millihertz, indexed by LFOF.
var lfoMilliHz = [32]uint32{
	170, 190, 230, 270, 340, 390, 450, 550,
	680, 780, 920, 1100, 1390, 1600, 1870, 2270,
	2870, 3310, 3920, 4790, 6150, 7180, 8600, 10800,
	14400, 17200, 21500, 28700, 43100, 57400, 86100, 172300,
}

// Pitch LFO depth pairs in cents, indexed by PLFOS: negative side first.
var plfoCentsNeg = [8]int{0, 3, 7, 14, 27, 55, 112, 231}
var plfoCentsPos = [8]int{0, 2, 5, 12, 25, 52, 103, 202}

// Amplitude LFO depths in dB, indexed by ALFOS.
var alfoDepthDB = [8]float64{0, 0.4, 0.8, 1.5, 3, 6, 12, 24}

// ADPCM step adaptation factors, numerator over 256.
var adpcmFactors = [8]int32{230, 230, 230, 230, 307, 409, 512, 614}

// aegGainQ16 holds the documented amplitude envelope gain curve in
// Q16 fixed point: gain(V) = 10^((3*(V-1023)/32)/20).
var aegGainQ16 [1024]uint32

func init() {
	for v := 0; v < 1024; v += 1 {
		g := math.Pow(10, (3*float64(v-1023)/32)/20)
		aegGainQ16[v] = uint32(g*65536 + 0.5)
	}
}

func dbToGain(db float64) float64 {
	if db <= 0 {
		return 1
	}
	return math.Pow(10, -db/20)
}

// tlAttenuationDB sums the bit weights of a TL byte.
func tlAttenuationDB(tl uint16) float64 {
	weights := [8]float64{0.4, 0.8, 1.5, 3, 6, 12, 24, 48}
	att := 0.0
	for i := 0; i < 8; i += 1 {
		if tl&(1<<uint(i)) != 0 {
			att += weights[i]
		}
	}
	return att
}

// sendGain converts a 4 bit send level to a linear gain. Zero mutes.
func sendGain(v uint16) float64 {
	if v == 0 {
		return 0
	}
	if v >= 15 {
		return 1
	}
	return dbToGain(float64(15-v) * 3)
}

// sendGainQ15 is sendGain in Q15 fixed point, with 1.0 as 32768 so a
// unity send is exact in integer math.
func sendGainQ15(v uint16) int32 {
	if v == 0 {
		return 0
	}
	if v >= 15 {
		return 32768
	}
	return int32(sendGain(v)*32768 + 0.5)
}

// panGain returns the gain for one side. side 0 is left, 1 is right.
// A value below 0x10 pans left, otherwise it pans right.
func panGain(pan uint16, side int) float64 {
	weights := [4]float64{3, 6, 12, 24}
	pan &= 0x1F
	if pan < 0x10 {
		if side == 1 {
			return 1
		}
		nib := pan & 0xF
		if nib == 0xF {
			return 0
		}
		att := 0.0
		for i := 0; i < 4; i += 1 {
			if nib&(1<<uint(i)) != 0 {
				att += weights[i]
			}
		}
		return dbToGain(att)
	}
	if side == 0 {
		return 1
	}
	nib := pan & 0xF
	if nib == 0xF {
		return 0
	}
	att := 0.0
	for i := 0; i < 4; i += 1 {
		if nib&(1<<uint(i)) != 0 {
			att += weights[i]
		}
	}
	return dbToGain(att)
}

func clampInt32(v int32, lo int32, hi int32) int32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func sat24(v int32) int32 {
	return clampInt32(v, -8388608, 8388607)
}

func wrap24(v int32) int32 {
	v &= 0xFFFFFF
	if v&0x800000 != 0 {
		v -= 0x1000000
	}
	return v
}

func signExtend(v uint32, bits uint) int32 {
	shift := 32 - bits
	return int32(v<<shift) >> int(shift)
}
