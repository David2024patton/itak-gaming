package video

// VRAM bank assignment. The engines never fetch from the flat
// LCDC store directly; they fetch through five assignment
// spaces (BG A, BG B, OBJ A, OBJ B, and the extended palette
// spaces) built from the VRAMCNT bytes A-I (IO9 0x240-0x24A)
// per the documented mapping tables. LCDC CPU access to the
// physical banks in the bus is unaffected. Texture image and
// texture palette slots are recorded nowhere in this slice:
// the 3D engine that consumes them is DS-3.
//
// Bank rules implemented (GBATEK):
//   - Banks A/B (128 KB): MST 1 = BG A at (OFS&3)*128 KB;
//     MST 2 = OBJ, OFS bit 0 selecting the OBJ A (0) or OBJ B
//     (1) space, OFS bit 1 a 128 KB step inside it; MST 3 =
//     texture slot (unused here).
//   - Banks C/D (128 KB): MST 1 = BG A as above; MST 2 = ARM7
//     WRAM image (decoded in the bus, not here); MST 3 =
//     texture slot (unused).
//   - Bank E (64 KB): MST 1 = BG A at 0; MST 2 = OBJ A at 0;
//     MST 3 = BG extended palette A, first 32 KB.
//   - Banks F/G (16 KB): the 3-bit mode/offset fields decode
//     as MST 1 = BG A, MST 2 = OBJ A at one of the documented
//     sub-offsets {0, 4 KB, 64 KB, 68 KB} from OFS; MST 3 =
//     BG extended palette A slot OFS (8 KB used); field value
//     5 = OBJ extended palette A (8 KB used). The published
//     tables carry overlapping encodings for the F/G palette
//     and texture slots; this slice resolves them in favor of
//     the decodes above and documents the choice.
//   - Bank H (32 KB): MST 1 = BG B at 0; MST 2 = BG extended
//     palette B (all 32 KB).
//   - Bank I (16 KB): MST 1 = BG B at 8 KB; MST 2 = OBJ B at
//     128 KB; MST 3 = OBJ extended palette B.
//
// Later banks in A-I order overwrite earlier chunks in the
// same space.

// maybeRebuildMaps rebuilds the fetch spaces when the
// VRAMCNT bytes changed. The nine bank bytes are gathered
// explicitly: WRAMCNT sits at 0x247, between VRAMCNT G
// (0x246) and H (0x248).
func (v *Video) maybeRebuildMaps() {
	var cur [9]byte
	copy(cur[:7], v.bus.IO9[0x240:0x247])
	cur[7] = v.bus.IO9[0x248]
	cur[8] = v.bus.IO9[0x249]
	if !v.mapsDirty && cur == v.cntCache {
		return
	}
	v.cntCache = cur
	v.mapsDirty = false
	v.bgA.clear()
	v.bgB.clear()
	v.objA.clear()
	v.objB.clear()
	v.extBG[0].clear()
	v.extBG[1].clear()
	v.extOBJ[0].clear()
	v.extOBJ[1].clear()
	for bank := 0; bank < 9; bank++ {
		cnt := cur[bank]
		if cnt&1 == 0 {
			continue
		}
		data := v.bus.VRAMBank(bank)
		mst := (cnt >> 1) & 3
		ofs := (cnt >> 3) & 3
		switch bank {
		case 0, 1: // A, B: 128 KB
			switch mst {
			case 1:
				v.bgA.put(uint32(ofs)*0x20000, data)
			case 2:
				if ofs&1 == 0 {
					v.objA.put(uint32(ofs>>1)*0x20000, data)
				} else {
					v.objB.put(uint32(ofs>>1)*0x20000, data)
				}
			}
		case 2, 3: // C, D: 128 KB
			if mst == 1 {
				v.bgA.put(uint32(ofs)*0x20000, data)
			}
		case 4: // E: 64 KB
			switch mst {
			case 1:
				v.bgA.put(0, data)
			case 2:
				v.objA.put(0, data)
			case 3:
				v.extBG[0].put(0, data[:0x8000])
			}
		case 5, 6: // F, G: 16 KB
			off14 := uint32(ofs&1)*0x4000 + uint32(ofs>>1)*0x10000
			switch (cnt >> 1) & 7 {
			case 1:
				v.bgA.put(off14, data)
			case 2:
				v.objA.put(off14, data)
			case 3:
				v.extBG[0].put(uint32(ofs)*0x2000, data[:0x2000])
			case 5:
				// The 3-bit field value 5 is the OBJ
				// extended palette A encoding given by the
				// reference tables (8 KB of the bank used).
				v.extOBJ[0].put(0, data[:0x2000])
			}
		case 7: // H: 32 KB
			switch mst {
			case 1:
				v.bgB.put(0, data)
			case 2:
				v.extBG[1].put(0, data)
			}
		case 8: // I: 16 KB
			switch mst {
			case 1:
				v.bgB.put(0x8000, data)
			case 2:
				v.objB.put(0x20000, data)
			case 3:
				v.extOBJ[1].put(0, data)
			}
		}
	}
}
