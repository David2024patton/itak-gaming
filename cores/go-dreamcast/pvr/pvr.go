// Package pvr is the PowerVR2 (CLX2) tile renderer for DC-2:
// the CORE register file, the SPG display timing generator, the
// TA (tile accelerator) parameter input, and the ISP/TSP tile
// renderer, built from the published PowerVR2 and Holly
// documentation.
//
// The chip owns the register window at physical 0x005F8000 to
// 0x005F9FFF (CORE and TA registers, the fog table at +0x200, and
// palette RAM at +0x400), the TA FIFO port at 0x00500000, and the
// YUV FIFO port at 0x00600000. Texture and framebuffer memory is
// the bus VRAM, reached through the vram slice shared with the
// bus; rendering is synchronous: a write to STARTRENDER renders
// the current TA scene into the VRAM framebuffer immediately and
// raises the render-done ASIC events through the Holly
// controller.
//
// Documented simplifications for this slice (see the module
// README for the full list): trilinear filtering samples one mip
// level with bilinear filtering; fog registers are stored but the
// fog unit is deferred to DC-3; the region array is walked for
// per-tile list disables only, and the renderer consumes the TA
// primitive lists in per-pixel submission order, which is
// pixel-identical to the hardware tile order.
package pvr

import (
	"encoding/binary"

	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/holly"
)

// Register offsets from the PVR base 0x005F8000.
const (
	RegID            = 0x000
	RegRevision      = 0x004
	RegSoftreset     = 0x008
	RegStartRender   = 0x014
	RegTestSelect    = 0x018
	RegParamBase     = 0x020
	RegRegionBase    = 0x02C
	RegBorderCol     = 0x040 // VO_BORDER_COL
	RegFBRCTRL       = 0x044 // FB_R_CTRL
	RegFBWCTRL       = 0x048 // FB_W_CTRL
	RegFBWLineStride = 0x04C // FB_W_LINESTRIDE
	RegFBRSOF1       = 0x050 // FB_R_SOF1
	RegFBRSOF2       = 0x054 // FB_R_SOF2
	RegFBRSize       = 0x05C // FB_R_SIZE
	RegFBWSOF1       = 0x060 // FB_W_SOF1
	RegFBWSOF2       = 0x064 // FB_W_SOF2
	RegFBXClip       = 0x068 // FB_X_CLIP
	RegFBYClip       = 0x06C // FB_Y_CLIP

	RegISPBackgndT  = 0x094 // ISP_BACKGND_T (ISP/TSP word)
	RegISPBackgndS  = 0x098 // ISP_BACKGND_S (color or TCW)
	RegISPBackgndD  = 0x09C // ISP_BACKGND_D (depth, float 1/w)
	RegISPFeedCfg   = 0x0A0 // ISP_FEED_CFG (stored)
	RegFPUShadScale = 0x0A4 // FPU_SHAD_SCALE (float32 bits)
	RegSDRAMCfg     = 0x0A8 // SDRAM_CFG (stored)
	RegFPUCullMode  = 0x0AC // FPU_CULL_MODE (stored)

	RegFogColRAM   = 0x0B0 // FOG_COL_RAM (stored, fog deferred)
	RegFogColVert  = 0x0B4 // FOG_COL_VERT (stored)
	RegFogDensity  = 0x0B8 // FOG_DENSITY (stored)
	RegFogClampMax = 0x0BC // FOG_CLAMP_MAX (stored)
	RegFogClampMin = 0x0C0 // FOG_CLAMP_MIN (stored)

	RegSPGHBlankInt = 0x0C8 // SPG_HBLANK_INT
	RegSPGVBlankInt = 0x0CC // SPG_VBLANK_INT
	RegSPGControl   = 0x0D0 // SPG_CONTROL
	RegSPGHBlank    = 0x0D4 // SPG_HBLANK
	RegSPGLoad      = 0x0D8 // SPG_LOAD
	RegSPGVBlank    = 0x0DC // SPG_VBLANK (stored)
	RegSPGWidth     = 0x0E0 // SPG_WIDTH (stored)
	RegTextControl  = 0x0E4 // TEXT_CONTROL (bits 31:24 = ALPHA_REF)
	RegVOControl    = 0x0E8 // VO_CONTROL (stored)
	RegVOStartX     = 0x0EC // VO_STARTX (stored)
	RegVOStartY     = 0x0F0 // VO_STARTY (stored)
	RegScalerCtl    = 0x0F4 // SCALER_CTL
	RegPALRamCtrl   = 0x0F8 // PAL_RAM_CTRL
	RegFPUParamCfg  = 0x100 // FPU_PARAM_CFG (stored)
	RegHalfOffset   = 0x104 // HALF_OFFSET (stored)
	RegFPUPerpVal   = 0x108 // FPU_PERP_VAL (stored)
	RegSPGStatus    = 0x10C // SPG_STATUS (read-only, computed)

	RegTAITPCurrent   = 0x120 // TA_ITP_CURRENT (read-only)
	RegTAOLBase       = 0x124 // TA_OL_BASE
	RegTAISPBase      = 0x128 // TA_ISP_BASE
	RegTAOLLimit      = 0x12C // TA_OL_LIMIT (stored)
	RegTAISPLimit     = 0x130 // TA_ISP_LIMIT (stored)
	RegTAGlobTileClip = 0x134 // TA_GLOB_TILE_CLIP (stored)
	RegTAAllocCtrl    = 0x13C // TA_ALLOC_CTRL
	RegTAListInit     = 0x140 // TA_LIST_INIT
	RegTAListCont     = 0x144 // TA_LIST_CONT
	RegTAYUVTexBase   = 0x148 // TA_YUV_TEX_BASE
	RegTAYUVTexCtrl   = 0x14C // TA_YUV_TEX_CTRL
	RegTAYUVTexCnt    = 0x150 // TA_YUV_TEX_CNT (read-only)
	RegTANextOPB      = 0x164 // TA_NEXT_OPB (read-only)
	RegTANextOPBInit  = 0x168 // TA_NEXT_OPB_INIT
)

// FB_R_CTRL and FB_W_CTRL field bits used by this implementation.
const (
	fbEnable  = 1 << 0   // display (R) or write (W) enable
	fbDepth   = 0x7 << 1 // pixel format code in bits 3:1
	fbDither  = 1 << 8   // FB_W_CTRL: ordered dither enable
	fbVclkDiv = 1 << 23  // FB_R_CTRL: 1 = 13.5 MHz pixel clock, 0 = 27 MHz
)

// SPG_CONTROL field bits used by this implementation.
const (
	spgInterlace = 1 << 6 // interlaced scan: field flips every SPG frame
)

// Pixel format codes for the fbDepth fields.
const (
	Fmt0555 = 0 // ARGB0555, 16-bit
	Fmt0565 = 1 // RGB0565, 16-bit
	Fmt0888 = 2 // RGB888, 24-bit packed
	Fmt8888 = 3 // ARGB8888, 32-bit
)

// pvrBase is the physical base of the register window.
const pvrBase = 0x005F8000

// PVR is the PowerVR2 state. The zero value is not usable; call
// New.
type PVR struct {
	vram   []byte // shared with the bus, 8 MB
	events *holly.Controller

	regs     [128]uint32 // register file, offset/4, 0x000 to 0x1FF
	palette  [1024]uint32
	fogTable [128]uint16

	spg spgState
	ta  taState

	// renderCount counts completed renders, for tests.
	renderCount int
}

// New returns a powered-on PVR rendering into vram and raising
// ASIC events through events (which may be nil, in which case
// events are dropped).
func New(vram []byte, events *holly.Controller) *PVR {
	p := &PVR{vram: vram, events: events}
	p.Reset()
	return p
}

// Reset restores the power-on register defaults: NTSC SPG totals
// (858 pixels per line, 263 lines per field, 13.5 MHz pixel clock
// selected), vblank-in on the last line, and an empty TA scene.
func (p *PVR) Reset() {
	p.regs = [128]uint32{}
	p.palette = [1024]uint32{}
	p.fogTable = [128]uint16{}
	p.spg = spgState{}
	p.ta = taState{}
	p.renderCount = 0
	// SPG_LOAD: hcount (bits 9:0) = total pixels - 1 = 857,
	// vcount (bits 25:16) = total lines per field - 1 = 262.
	p.setReg(RegSPGLoad, 262<<16|857)
	// SPG_VBLANK_INT: vblank-in line (bits 25:16) = 262,
	// vblank-out line (bits 9:0) = 0.
	p.setReg(RegSPGVBlankInt, 262<<16|0)
	// SPG_HBLANK: hblank status window, start (bits 9:0) = 720,
	// end (bits 25:16) = 857.
	p.setReg(RegSPGHBlank, 857<<16|720)
	// FB_R_CTRL: vclk_div set (13.5 MHz), display disabled.
	p.setReg(RegFBRCTRL, fbVclkDiv)
	// FPU_SHAD_SCALE default 1.0 (float32 bits).
	p.setReg(RegFPUShadScale, 0x3F800000)
	p.ta.listInit()
}

// reg reads one register by offset.
func (p *PVR) reg(off uint32) uint32 { return p.regs[(off>>2)&0x7F] }

// setReg writes one register by offset.
func (p *PVR) setReg(off uint32, v uint32) { p.regs[(off>>2)&0x7F] = v }

// raise delivers ASIC event bits to the Holly controller.
func (p *PVR) raise(bank int, bits uint32) {
	if p.events != nil {
		p.events.Raise(bank, bits)
	}
}

// RenderCount returns how many renders have completed.
func (p *PVR) RenderCount() int { return p.renderCount }

// Read32 reads one word of the register window by full physical
// address (bus.Device). Palette RAM occupies offsets 0x400 to
// 0x13FF and the fog table offsets 0x200 to 0x2FF.
func (p *PVR) Read32(pa uint32) uint32 {
	off := (pa - pvrBase) & 0x1FFF
	switch {
	case off >= 0x400 && off < 0x1400:
		return p.palette[(off-0x400)>>2]
	case off >= 0x200 && off < 0x300:
		i := (off - 0x200) >> 1
		return uint32(p.fogTable[i]) | uint32(p.fogTable[i+1])<<16
	}
	switch off {
	case RegSPGStatus:
		return p.spgStatus()
	case RegTAITPCurrent:
		return p.reg(RegTAISPBase) + p.ta.ispBytes
	case RegTANextOPB:
		base := p.reg(RegTANextOPBInit)
		if base == 0 {
			base = p.reg(RegTAOLBase)
		}
		return base + p.ta.opbBytes
	case RegTAYUVTexCnt:
		return uint32(p.ta.yuv.mbDone)
	}
	return p.reg(off)
}

// Write32 writes one word of the register window by full physical
// address (bus.Device) and performs the register's action, if any.
func (p *PVR) Write32(pa uint32, v uint32) {
	off := (pa - pvrBase) & 0x1FFF
	switch {
	case off >= 0x400 && off < 0x1400:
		p.palette[(off-0x400)>>2] = v
		return
	case off >= 0x200 && off < 0x300:
		i := (off - 0x200) >> 1
		p.fogTable[i] = uint16(v)
		p.fogTable[i+1] = uint16(v >> 16)
		return
	}
	switch off {
	case RegSPGStatus, RegTAITPCurrent, RegTANextOPB, RegTAYUVTexCnt:
		return // read-only
	case RegSoftreset:
		if v&1 != 0 { // TA reset
			p.ta.listInit()
		}
		if v&2 != 0 { // core reset: register defaults, TA included
			p.Reset()
		}
		return
	case RegStartRender:
		p.Render()
		return
	case RegTAListInit:
		if v&(1<<31) != 0 {
			p.Render() // render the finished scene first
		}
		p.ta.listInit()
		return
	case RegTAListCont:
		return // appends to the open scene; nothing resets
	}
	p.setReg(off, v)
}

// Port is a bus.Device view of one of the PVR streaming ports.
// Reads return 0; each 32-bit write feeds one word to the port.
type Port struct {
	p   *PVR
	yuv bool
}

// Read32 on a streaming port returns 0.
func (pt Port) Read32(uint32) uint32 { return 0 }

// Write32 feeds one word to the port.
func (pt Port) Write32(_ uint32, v uint32) {
	if pt.yuv {
		pt.p.YUVWrite(v)
	} else {
		pt.p.TAWrite(v)
	}
}

// TAPort returns the TA FIFO port device (attach at 0x00500000).
func (p *PVR) TAPort() Port { return Port{p: p} }

// YUVPort returns the YUV FIFO port device (attach at 0x00600000).
func (p *PVR) YUVPort() Port { return Port{p: p, yuv: true} }

// TAWrite feeds one word of a TA parameter stream.
func (p *PVR) TAWrite(v uint32) { p.ta.writeWord(p, v) }

// YUVWrite feeds one word (four bytes, little-endian) of YUV
// converter input.
func (p *PVR) YUVWrite(v uint32) { p.ta.yuvWrite(p, v) }

// vram32 reads a 32-bit word from VRAM at a byte offset.
func (p *PVR) vram32(off uint32) uint32 {
	off &= 0x7FFFFF
	return binary.LittleEndian.Uint32(p.vram[off:])
}

// vramWrite32 writes a 32-bit word to VRAM at a byte offset.
func (p *PVR) vramWrite32(off uint32, v uint32) {
	off &= 0x7FFFFF
	binary.LittleEndian.PutUint32(p.vram[off:], v)
}

// ---------------------------------------------------------------------------
// Serialization
//
// Layout (variable length because the TA scene is serialized):
//
//	regs          512 bytes (128 words)
//	palette       4096 bytes
//	fog table     256 bytes
//	SPG state     28 bytes
//	TA state      fixed part, then the primitive lists
//
// The total length is p.SerializeSize() for the current state.

// SerializeSize returns the byte length of Serialize for the
// current state.
func (p *PVR) SerializeSize() int {
	n := 512 + 4096 + 256 + 28
	n += p.ta.serializeSize()
	return n
}

// Serialize writes the full PVR state.
func (p *PVR) Serialize() []byte {
	b := make([]byte, 0, p.SerializeSize())
	tmp := make([]byte, 4)
	put := func(v uint32) {
		binary.LittleEndian.PutUint32(tmp, v)
		b = append(b, tmp...)
	}
	for _, v := range p.regs {
		put(v)
	}
	for _, v := range p.palette {
		put(v)
	}
	for _, v := range p.fogTable {
		b = append(b, uint8(v), uint8(v>>8))
	}
	// SPG state: frac, hcount, vcount, field, frames (8), flags.
	put(p.spg.frac)
	put(p.spg.hcount)
	put(p.spg.vcount)
	put(p.spg.field)
	put(uint32(p.spg.frames))
	put(uint32(p.spg.frames >> 32))
	var fl uint32
	if p.spg.inVBlank {
		fl = 1
	}
	put(fl)
	b = p.ta.serialize(b)
	return b
}

// Deserialize restores a state written by Serialize. On a size
// mismatch the PVR is left untouched and false is returned.
func (p *PVR) Deserialize(b []byte) bool {
	if len(b) != p.expectedSize(b) {
		return false
	}
	r := &reader{b: b}
	for i := range p.regs {
		p.regs[i] = r.u32()
	}
	for i := range p.palette {
		p.palette[i] = r.u32()
	}
	for i := range p.fogTable {
		p.fogTable[i] = r.u16()
	}
	p.spg.frac = r.u32()
	p.spg.hcount = r.u32()
	p.spg.vcount = r.u32()
	p.spg.field = r.u32()
	lo := r.u32()
	hi := r.u32()
	p.spg.frames = uint64(lo) | uint64(hi)<<32
	p.spg.inVBlank = r.u32()&1 != 0
	if !p.ta.deserialize(r) {
		return false
	}
	return true
}

// SerializedSize returns the total length of the serialized PVR
// state that b begins with, as implied by the counts inside it,
// or -1 when b is too short or malformed to hold one.
func SerializedSize(b []byte) int {
	p := &PVR{}
	return p.expectedSize(b)
}

// expectedSize computes the total serialized length implied by
// the primitive counts stored inside b, or -1 when b is too short
// to hold them.
func (p *PVR) expectedSize(b []byte) int {
	// Fixed prefix up to the TA section.
	const prefix = 512 + 4096 + 256 + 28
	if len(b) < prefix+taFixedSize {
		return -1
	}
	return prefix + taSerializedSize(b[prefix:])
}

// reader is a little-endian cursor over a serialized state.
type reader struct {
	b   []byte
	off int
}

func (r *reader) u32() uint32 {
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

func (r *reader) u16() uint16 {
	v := binary.LittleEndian.Uint16(r.b[r.off:])
	r.off += 2
	return v
}

func (r *reader) u8() uint8 {
	v := r.b[r.off]
	r.off++
	return v
}

func (r *reader) bytes(n int) []byte {
	v := r.b[r.off : r.off+n]
	r.off += n
	return v
}
