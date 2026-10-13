package pvr

import "math"

// PVR fog unit for DC-3, per the PowerVR2 documentation in
// the Sega Dev.Box System Architecture doc (graphics
// section). Fog is selected per polygon by the ISP/TSP fog
// control field (ispFogCtrl, bits 21:20 of the ISP/TSP
// word of the parameter set that shades the pixel):
//
//	0  Look Up Table: fog color is FOG_COL_RAM, fog alpha
//	   comes from the fog table indexed by fragment depth.
//	1  Per Vertex: fog color is FOG_COL_VERT, fog alpha is
//	   the fragment interpolated offset color alpha. When
//	   the polygon Offset bit is clear this mode behaves
//	   as No Fog.
//	2  No Fog: the pixel is unchanged.
//	3  Look Up Table Mode 2: the documented substitution
//	   replaces the fog color with the polygon color and
//	   the fog alpha with the polygon alpha.
//
// The mix runs on the shaded source pixel after the
// texture instruction and after the source color clamp
// (the renderer clamps the source to 0 to 1 in shadePixel
// before this stage; the FOG_CLAMP_MIN and FOG_CLAMP_MAX
// registers are stored but no clamp stage reads them yet,
// see the DC-3 report). The mix is
//
//	out_c = src_c * (1 - a) + fog_c * a
//
// per RGB channel, in float32 like the rest of the
// renderer; the alpha channel is unchanged. Fog applies no
// integer rounding of its own: the only rounding is the
// framebuffer quantization in writeFrame (round to
// nearest).
//
// Mode 3 in this renderer: the mix target (fog color) is
// the polygon shaded color itself, which is exactly src at
// this stage, so the mix is src * (1 - a) + src * a = src
// for any factor a, an RGB identity by construction. On
// hardware the mode exists for passes that mix against
// the secondary accumulation buffer, but this renderer has
// a single color buffer (the modifier counter selects
// parameter sets, it holds no color), so there is nothing
// distinct to mix toward. applyFog therefore runs the
// table lookup for mode 3 and returns src unchanged, and
// the gate test pins a mode 3 pixel equal to a mode 2 (No
// Fog) pixel. The color clamp stage still runs before it,
// as for every mode.
//
// The background plane is not fogged: Render fills it
// directly and never routes it through shadePixel, the
// same treatment every other ISP driven shading feature
// gets in this renderer.

// fogDensity decodes FOG_DENSITY. Bits 15:8 are the
// mantissa byte, with bit 15 as the 1.0 bit, so the
// mantissa value is the byte divided by 128. Bits 7:0 are
// the exponent as a signed two complement value. The
// density is mantissa times two to the exponent.
func (p *PVR) fogDensity() float64 {
	reg := p.reg(RegFogDensity)
	mant := float64((reg>>8)&0xFF) / 128.0
	exp := int(int8(reg & 0xFF))
	return mant * math.Pow(2, float64(exp))
}

// fogTableAlpha returns the fog mix factor in the range 0
// to 1 for a fragment whose interpolated reciprocal w is
// invW. The eye space distance is d = 1 / invW, the scaled
// index is f = fogDensity * d clamped to the range 0 to
// 127.999999, and the alpha is the linear interpolation
// of the two neighboring fog table entries at floor(f).
// Table entry format choice: each 16 bit fog table entry
// carries its fog alpha in the LOW 8 bits (0 to 255); the
// high byte is stored but unused by this implementation.
func (p *PVR) fogTableAlpha(invW float32) float32 {
	f := p.fogDensity() * (1 / float64(invW))
	if math.IsNaN(f) || f < 0 {
		f = 0
	}
	if f > 127.999999 {
		f = 127.999999
	}
	i := int(math.Floor(f))
	frac := f - float64(i)
	j := i + 1
	if j > 127 {
		j = 127
	}
	lo := float64(p.fogTable[i] & 0xFF)
	hi := float64(p.fogTable[j] & 0xFF)
	return float32((lo*(1-frac) + hi*frac) / 255)
}

// fogColor unpacks a fog color register (FOG_COL_RAM or
// FOG_COL_VERT) to RGB floats. The register layout is
// bits 23:16 red, 15:8 green, 7:0 blue; there is no alpha
// byte, and the returned alpha is unused by the mix.
func fogColor(reg uint32) [4]float32 {
	return [4]float32{
		float32((reg>>16)&0xFF) / 255,
		float32((reg>>8)&0xFF) / 255,
		float32(reg&0xFF) / 255,
		1,
	}
}

// mixFog blends src toward fog by the factor a, RGB only.
func mixFog(src, fog [4]float32, a float32) [4]float32 {
	out := src
	for c := 0; c < 3; c++ {
		out[c] = src[c]*(1-a) + fog[c]*a
	}
	return out
}

// applyFog runs the fog mix for one shaded source pixel.
// mode is the polygon ISP fog control field, invW the
// fragment interpolated reciprocal w, offAlpha the
// fragment interpolated offset color alpha, and
// offsetEnabled the polygon Offset bit.
func (p *PVR) applyFog(src [4]float32, mode int, invW, offAlpha float32, offsetEnabled bool) [4]float32 {
	switch mode {
	case 1: // Per Vertex
		if !offsetEnabled {
			return src
		}
		return mixFog(src, fogColor(p.reg(RegFogColVert)), clamp01(offAlpha))
	case 2: // No Fog
		return src
	case 3: // Look Up Table Mode 2: identity, see header.
		_ = p.fogTableAlpha(invW)
		return src
	}
	// Mode 0: Look Up Table.
	return mixFog(src, fogColor(p.reg(RegFogColRAM)), p.fogTableAlpha(invW))
}
