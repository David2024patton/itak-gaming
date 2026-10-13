package gx

// The matrix unit: four current matrices (projection,
// position, directional/vector, texture), three stacks, and
// the MTX command group. Matrices are 4x4, row-major,
// elements signed with 12 fractional bits; vertices are row
// vectors, so the clip transform is v * (Position *
// Projection) per GBATEK's written order, and MTX_MULT
// composes C = M * C (the new matrix applies first, in the
// vertex's local space).

type mat [16]int32

func identityMat() mat {
	var m mat
	m[0], m[5], m[10], m[15] = 0x1000, 0x1000, 0x1000, 0x1000
	return m
}

// matMul returns a*b with 64-bit accumulation and a 12-bit
// fractional shift per output element, wrapping to 32 bits.
func matMul(a, b mat) mat {
	var out mat
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			var acc int64
			for k := 0; k < 4; k++ {
				acc += int64(a[i*4+k]) * int64(b[k*4+j])
			}
			out[i*4+j] = int32(acc >> 12)
		}
	}
	return out
}

// xform applies m to the row vector (x, y, z, w), all in the
// 12-fraction domain.
func xform(m mat, x, y, z, w int32) (int32, int32, int32, int32) {
	v := [4]int32{x, y, z, w}
	var out [4]int32
	for j := 0; j < 4; j++ {
		var acc int64
		for k := 0; k < 4; k++ {
			acc += int64(v[k]) * int64(m[k*4+j])
		}
		out[j] = int32(acc >> 12)
	}
	return out[0], out[1], out[2], out[3]
}

// xformDir applies the upper 3x3 of m to a direction vector
// in the 9-fraction domain (normals, light vectors, VEC_TEST).
func xformDir(m mat, x, y, z int32) (int32, int32, int32) {
	v := [3]int32{x, y, z}
	var out [3]int32
	for j := 0; j < 3; j++ {
		var acc int64
		for k := 0; k < 3; k++ {
			acc += int64(v[k]) * int64(m[k*4+j])
		}
		out[j] = int32(acc >> 12)
	}
	return out[0], out[1], out[2]
}

// clipMatrix is Position * Projection, recomputed on demand.
func (g *GX) clipMatrix() mat { return matMul(g.curPos, g.curProj) }

// targets returns pointers to the current matrices a load or
// multiply applies to, per the matrix mode. Mode 2 applies to
// both position and vector.
func (g *GX) targets() []*mat {
	switch g.mtxMode {
	case 0:
		return []*mat{&g.curProj}
	case 2:
		return []*mat{&g.curPos, &g.curVec}
	case 3:
		return []*mat{&g.curTex}
	}
	return []*mat{&g.curPos}
}

// execMatrix runs the MTX command group (0x10-0x1C).
func (g *GX) execMatrix(cmd uint8, p []uint32) {
	switch cmd {
	case 0x10: // MTX_MODE
		g.mtxMode = int(p[0] & 3)
	case 0x11: // MTX_PUSH
		g.stackPush()
	case 0x12: // MTX_POP
		g.stackPop(p[0])
	case 0x13: // MTX_STORE
		g.stackStore(p[0])
	case 0x14: // MTX_RESTORE
		g.stackRestore(p[0])
	case 0x15: // MTX_IDENTITY
		for _, t := range g.targets() {
			*t = identityMat()
		}
	case 0x16, 0x17: // MTX_LOAD_4x4 / 4x3
		m := loadMat(p, cmd == 0x16)
		for _, t := range g.targets() {
			*t = m
		}
	case 0x18, 0x19: // MTX_MULT_4x4 / 4x3
		m := loadMat(p, cmd == 0x18)
		for _, t := range g.targets() {
			*t = matMul(m, *t)
		}
	case 0x1A: // MTX_MULT_3x3
		m := embed3x3(p)
		for _, t := range g.targets() {
			*t = matMul(m, *t)
		}
	case 0x1B: // MTX_SCALE: position only in mode 2
		var s mat
		s[0], s[5], s[10], s[15] = int32(p[0]), int32(p[1]), int32(p[2]), 0x1000
		ts := g.targets()
		if g.mtxMode == 2 {
			ts = []*mat{&g.curPos}
		}
		for _, t := range ts {
			*t = matMul(s, *t)
		}
	case 0x1C: // MTX_TRANS
		var tr mat
		tr[0], tr[5], tr[10], tr[15] = 0x1000, 0x1000, 0x1000, 0x1000
		tr[12], tr[13], tr[14] = int32(p[0]), int32(p[1]), int32(p[2])
		for _, t := range g.targets() {
			*t = matMul(tr, *t)
		}
	}
}

// loadMat builds a 4x4 matrix from a 4x4 load (16 words) or a
// 4x3 load (12 words; the fourth column is 0,0,0,1).
func loadMat(p []uint32, full bool) mat {
	var m mat
	if full {
		for i := 0; i < 16; i++ {
			m[i] = int32(p[i])
		}
		return m
	}
	for i := 0; i < 12; i++ {
		m[i] = int32(p[i])
	}
	m[15] = 0x1000
	return m
}

// embed3x3 builds the 4x4 embedding of a 3x3 load: the 3x3
// block plus a zero fourth row/column and m[15] = 1.0, which
// leaves the current matrix's translation column unchanged
// under multiplication.
func embed3x3(p []uint32) mat {
	var m mat
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			m[r*4+c] = int32(p[r*3+c])
		}
	}
	m[15] = 0x1000
	return m
}

// stackPush implements MTX_PUSH per stack rules: projection
// and texture stacks hold one entry (the pointer is one bit
// and wraps, the entry mirroring, with the error flag set on
// access to entry 1); the position/vector stacks share the
// 6-bit pointer over 32 physical entries, entries addressed
// 31 and up setting the error flag (entry 31 physically
// exists and is written).
func (g *GX) stackPush() {
	switch g.mtxMode {
	case 0:
		if g.spProj == 1 {
			g.stackErr = true
		}
		g.projStk[0] = g.curProj
		g.spProj = (g.spProj + 1) & 1
	case 3:
		if g.spTex == 1 {
			g.stackErr = true
		}
		g.texStk[0] = g.curTex
		g.spTex = (g.spTex + 1) & 1
	default: // modes 1 and 2 act on both stacks
		if g.spPos >= 31 {
			g.stackErr = true
		}
		g.posStk[g.spPos&31] = g.curPos
		g.vecStk[g.spPos&31] = g.curVec
		g.spPos = (g.spPos + 1) & 63
	}
}

// stackPop implements MTX_POP: the position stack pointer
// moves by the signed 6-bit offset before the load; the
// projection and texture stacks always use offset +1.
func (g *GX) stackPop(param uint32) {
	switch g.mtxMode {
	case 0:
		g.spProj = (g.spProj - 1) & 1
		if g.spProj == 1 {
			g.stackErr = true
		}
		g.curProj = g.projStk[0]
	case 3:
		g.spTex = (g.spTex - 1) & 1
		if g.spTex == 1 {
			g.stackErr = true
		}
		g.curTex = g.texStk[0]
	default:
		n := int32(int8(param<<2)) >> 2 // sign-extend 6 bits
		g.spPos = int((int32(g.spPos) - n) & 63)
		if g.spPos >= 31 {
			g.stackErr = true
		}
		g.curPos = g.posStk[g.spPos&31]
		g.curVec = g.vecStk[g.spPos&31]
	}
}

// stackStore implements MTX_STORE. On the single-entry stacks
// the parameter is ignored.
func (g *GX) stackStore(param uint32) {
	switch g.mtxMode {
	case 0:
		g.projStk[0] = g.curProj
	case 3:
		g.texStk[0] = g.curTex
	default:
		n := int(param & 31)
		if n == 31 {
			g.stackErr = true
		}
		g.posStk[n] = g.curPos
		g.vecStk[n] = g.curVec
	}
}

// stackRestore implements MTX_RESTORE.
func (g *GX) stackRestore(param uint32) {
	switch g.mtxMode {
	case 0:
		g.curProj = g.projStk[0]
	case 3:
		g.curTex = g.texStk[0]
	default:
		n := int(param & 31)
		if n == 31 {
			g.stackErr = true
		}
		g.curPos = g.posStk[n]
		g.curVec = g.vecStk[n]
	}
}

// cmdPosTest transforms the given vertex by the clip matrix
// into POS_RESULT.
func (g *GX) cmdPosTest(p []uint32) {
	x := int32(int16(p[0]))
	y := int32(int16(p[0] >> 16))
	z := int32(int16(p[1]))
	cx, cy, cz, cw := xform(g.clipMatrix(), x, y, z, 0x1000)
	g.posResult = [4]int32{cx, cy, cz, cw}
}

// cmdVecTest transforms the packed 10-bit direction by the
// vector matrix into VEC_RESULT (9-fraction results).
func (g *GX) cmdVecTest(param uint32) {
	nx := sext10(param)
	ny := sext10(param >> 10)
	nz := sext10(param >> 20)
	rx, ry, rz := xformDir(g.curVec, nx, ny, nz)
	g.vecResult = [3]int32{rx, ry, rz}
}

// cmdBoxTest transforms the eight box corners by the clip
// matrix and reports whether any part of the box can be
// inside the view volume: the box is outside only when all
// corners fall outside one common clip plane.
func (g *GX) cmdBoxTest(p []uint32) {
	x := int32(int16(p[0]))
	y := int32(int16(p[0] >> 16))
	z := int32(int16(p[1]))
	sx := int32(int16(p[1] >> 16))
	sy := int32(int16(p[2]))
	sz := int32(int16(p[2] >> 16))
	clip := g.clipMatrix()
	var corners [8][4]int32
	for i := 0; i < 8; i++ {
		cx, cy := x, y
		if i&1 != 0 {
			cx += sx
		}
		if i&2 != 0 {
			cy += sy
		}
		cz := z
		if i&4 != 0 {
			cz += sz
		}
		a, b, c, w := xform(clip, cx, cy, cz, 0x1000)
		corners[i] = [4]int32{a, b, c, w}
	}
	outside := false
	for plane := 0; plane < 6; plane++ {
		all := true
		for _, c := range corners {
			if planeDist(c, plane) >= 0 {
				all = false
				break
			}
		}
		if all {
			outside = true
			break
		}
	}
	g.boxResult = !outside
}

// planeDist returns the signed distance of a clip-space
// vertex from clip plane p (0 left, 1 right, 2 bottom, 3 top,
// 4 near, 5 far); non-negative means inside.
func planeDist(c [4]int32, p int) int64 {
	x, y, z, w := int64(c[0]), int64(c[1]), int64(c[2]), int64(c[3])
	switch p {
	case 0:
		return x + w
	case 1:
		return w - x
	case 2:
		return y + w
	case 3:
		return w - y
	case 4:
		return z + w
	}
	return w - z
}

// sext10 sign-extends a 10-bit field.
func sext10(v uint32) int32 {
	v &= 0x3FF
	if v&0x200 != 0 {
		return int32(v) - 0x400
	}
	return int32(v)
}
