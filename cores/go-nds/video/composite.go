package video

// Per-line compositing: priority merge of the four BGs and the
// OBJ layer over the backdrop, window masking, and the DS
// color effects (alpha blending and brightness up/down via
// BLDCNT/BLDALPHA/BLDY), in rgb6 space. Master brightness is
// applied by the caller after compositing; display capture
// takes this pre-brightness line.

type cand struct {
	c      uint32
	on     bool
	layer  int // 0-3 BG, 4 OBJ, 5 backdrop
	prio   int
	semi   bool
	target int // BLDCNT bit index of this layer
}

// composite merges the rendered layers for one line.
func (lc *lineCtx) composite() [Width]uint32 {
	v := lc.v
	var out [Width]uint32
	bgPrio := [4]int{}
	for bg := 0; bg < 4; bg++ {
		bgPrio[bg] = int(v.rd16(lc.base+8+2*bg) & 3)
		lc.bgOn[bg] = false
		for x := 0; x < Width; x++ {
			if lc.bg[bg][x].ok {
				lc.bgOn[bg] = true
				break
			}
		}
	}
	winEn := lc.dispcnt & 0xE000
	bld := v.rd16(lc.base + 0x50)
	bldMode := (bld >> 6) & 3
	eva := int(v.rd16(lc.base+0x52) & 0x1F)
	evb := int(v.rd16(lc.base+0x52) >> 8 & 0x1F)
	bldy := int(v.rd16(lc.base+0x54) & 0x1F)
	if eva > 16 {
		eva = 16
	}
	if evb > 16 {
		evb = 16
	}
	if bldy > 16 {
		bldy = 16
	}
	backdrop := v.stdPal(lc.e, false, 0)
	for x := 0; x < Width; x++ {
		mask := uint32(0x3F)
		if winEn != 0 {
			mask = lc.windowMask(x)
		}
		// Collect present candidates in layer order and pick
		// the top two by (priority, layer order).
		var cs [6]cand
		n := 0
		for bg := 0; bg < 4; bg++ {
			if lc.bg[bg][x].ok && mask&(1<<uint(bg)) != 0 {
				cs[n] = cand{c: lc.bg[bg][x].c, on: true, layer: bg, prio: bgPrio[bg], target: bg}
				n++
			}
		}
		if lc.obj[x].ok && mask&0x10 != 0 {
			cs[n] = cand{c: lc.obj[x].c, on: true, layer: 4, prio: int(lc.obj[x].prio), semi: lc.obj[x].semi, target: 4}
			n++
		}
		cs[n] = cand{c: backdrop, on: true, layer: 5, prio: 4, target: 5}
		n++
		top, second := cs[0], cand{}
		for _, c := range cs[1:n] {
			if c.prio < top.prio || c.prio == top.prio && c.layer < top.layer {
				second, top = top, c
			} else if !second.on || c.prio < second.prio || c.prio == second.prio && c.layer < second.layer {
				second = c
			}
		}
		c := top.c
		if mask&0x20 != 0 {
			t1 := bld&(1<<uint(top.target)) != 0
			var t2 bool
			if second.on {
				t2 = bld&(1<<uint(8+second.target)) != 0
			}
			forceAlpha := top.semi && second.on && t2
			switch {
			case bldMode == 1 && t1 && t2 || forceAlpha:
				c = blend(c, second.c, eva, evb)
			case bldMode == 2 && t1:
				c = brighten(c, bldy, true)
			case bldMode == 3 && t1:
				c = brighten(c, bldy, false)
			}
		}
		out[x] = c
	}
	return out
}

// windowMask resolves the WININ/WINOUT layer mask for one
// pixel: WIN0 inside wins first, then WIN1, then the OBJ
// window, then outside.
func (lc *lineCtx) windowMask(x int) uint32 {
	v := lc.v
	inRect := func(n int) bool {
		wh := v.rd16(lc.base + 0x40 + 2*n)
		wv := v.rd16(lc.base + 0x44 + 2*n)
		x1, x2 := int(wh>>8), int(wh&0xFF)
		y1, y2 := int(wv>>8), int(wv&0xFF)
		inX := (x2 > x1 && x >= x1 && x < x2) || (x2 <= x1 && (x >= x1 || x < x2))
		inY := (y2 > y1 && lc.y >= y1 && lc.y < y2) || (y2 <= y1 && (lc.y >= y1 || lc.y < y2))
		return inX && inY
	}
	winIn := v.rd16(lc.base + 0x48)
	winOut := v.rd16(lc.base + 0x4A)
	if lc.dispcnt&0x2000 != 0 && inRect(0) {
		return uint32(winIn & 0x3F)
	}
	if lc.dispcnt&0x4000 != 0 && inRect(1) {
		return uint32(winIn >> 8 & 0x3F)
	}
	if lc.dispcnt&0x8000 != 0 && lc.objWin[x] {
		return uint32(winOut >> 8 & 0x3F)
	}
	return uint32(winOut & 0x3F)
}

func blend(a, b uint32, eva, evb int) uint32 {
	ch := func(x, y uint32) uint32 {
		v := (int(x)*eva + int(y)*evb) >> 4
		if v > 63 {
			v = 63
		}
		return uint32(v)
	}
	return rgb6(ch(a&63, b&63), ch(a>>6&63, b>>6&63), ch(a>>12&63, b>>12&63))
}

func brighten(c uint32, y int, up bool) uint32 {
	ch := func(v uint32) uint32 {
		if up {
			return v + (63-v)*uint32(y)>>4
		}
		return v - v*uint32(y)>>4
	}
	return rgb6(ch(c&63), ch(c>>6&63), ch(c>>12&63))
}
