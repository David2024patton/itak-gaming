package gte

import (
	"errors"
	"math"
	"testing"
)

// Command word builders. Per psx-spx "GTE Command
// Encoding": opcode in bits 0..5, lm in bit 10, sf in
// bit 19.
func cmdWord(opcode uint32, sf, lm bool) uint32 {
	w := opcode
	if lm {
		w |= 1 << 10
	}
	if sf {
		w |= 1 << 19
	}
	return w
}

func setV(g *GTE, n int, x, y, z int) {
	g.SetData(uint32(2*n), uint32(uint16(int16(y)))<<16|uint32(uint16(int16(x))))
	g.SetData(uint32(2*n+1), uint32(uint16(int16(z))))
}

func setIR(g *GTE, x, y, z int) {
	g.SetData(9, uint32(int32(int16(x))))
	g.SetData(10, uint32(int32(int16(y))))
	g.SetData(11, uint32(int32(int16(z))))
}

func irOf(g *GTE) (int64, int64, int64) {
	return int64(int16(uint16(g.Data(9)))),
		int64(int16(uint16(g.Data(10)))),
		int64(int16(uint16(g.Data(11))))
}

func macOf(g *GTE) (int64, int64, int64) {
	return int64(int32(g.Data(25))),
		int64(int32(g.Data(26))),
		int64(int32(g.Data(27)))
}

// setMatrix writes a 3x3 matrix into the packed
// control registers starting at base (0=RT, 8=LLM,
// 16=LCM), using the psx-spx "Matrix Registers"
// packing.
func setMatrix(g *GTE, base uint32, m [3][3]int) {
	pack := func(a, b int) uint32 {
		return uint32(uint16(int16(b)))<<16 | uint32(uint16(int16(a)))
	}
	g.SetControl(base+0, pack(m[0][0], m[0][1]))
	g.SetControl(base+1, pack(m[0][2], m[1][0]))
	g.SetControl(base+2, pack(m[1][1], m[1][2]))
	g.SetControl(base+3, pack(m[2][0], m[2][1]))
	g.SetControl(base+4, uint32(uint16(int16(m[2][2]))))
}

var identQ12 = [3][3]int{
	{0x1000, 0, 0},
	{0, 0x1000, 0},
	{0, 0, 0x1000},
}

func TestRegisterStorage(t *testing.T) {
	g := New()
	g.SetData(3, 0xDEADBEEF)
	g.SetData(63, 0x12345678) // wraps to register 31
	g.SetControl(31, 0x0BADF00D)
	if g.Data(3) != 0xDEADBEEF {
		t.Errorf("Data(3) = 0x%08X, want 0xDEADBEEF", g.Data(3))
	}
	if g.Data(31) != 0x12345678 {
		t.Errorf("Data(31) = 0x%08X, want 0x12345678", g.Data(31))
	}
	// FLAG read semantics (psx-spx, "GTE Saturation"):
	// bits 11..0 always read zero and bit 31 is the OR
	// of the error bits (30..23, 18..13). The stored
	// value 0x0BADF00D has error bits set, so the read
	// back is 0x0BADF000 with bit 31 forced on.
	if g.Control(31) != 0x8BADF000 {
		t.Errorf("Control(31) = 0x%08X, want 0x8BADF000", g.Control(31))
	}
	// A stored FLAG with no error bits reads back with
	// bit 31 clear.
	g.SetControl(31, 0x00001000)
	if g.Control(31) != 0x00001000 {
		t.Errorf("Control(31) = 0x%08X, want 0x00001000", g.Control(31))
	}
}

func TestCommandsExecuteNotRefused(t *testing.T) {
	g := New()
	g.SetData(0, 0x11111111)
	// Every documented opcode must execute (nil error).
	for _, c := range Commands {
		if err := g.Execute(cmdWord(c.Opcode, true, true)); err != nil {
			t.Errorf("Execute(%s) error = %v, want nil", c.Name, err)
		}
	}
	if g.Unsupported() != 0 {
		t.Errorf("Unsupported() = %d, want 0", g.Unsupported())
	}
	if err := g.Execute(0x3B); !errors.Is(err, ErrUnknownCommand) {
		t.Errorf("Execute(0x3B) error = %v, want ErrUnknownCommand", err)
	}
	if g.Unsupported() != 1 {
		t.Errorf("Unsupported() = %d, want 1 after unknown opcode", g.Unsupported())
	}
	name, ok := CommandName(0x01)
	if !ok || name != "RTPS" {
		t.Errorf("CommandName(0x01) = %q, %v; want RTPS, true", name, ok)
	}
	if _, ok := CommandName(0x3B); ok {
		t.Errorf("CommandName(0x3B) reported a name for an undocumented opcode")
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	g := New()
	g.SetData(7, 0xFEEDFACE)
	g.SetControl(2, 0x0D15EA5E)
	state := g.Serialize()
	if len(state) != StateSize {
		t.Fatalf("state size = %d, want %d", len(state), StateSize)
	}
	g2 := New()
	if err := g2.Deserialize(state); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if g2.Data(7) != 0xFEEDFACE || g2.Control(2) != 0x0D15EA5E {
		t.Errorf("round trip mismatch: data=0x%08X control=0x%08X", g2.Data(7), g2.Control(2))
	}
	if err := g2.Deserialize(state[:8]); err == nil {
		t.Errorf("short state accepted, want error")
	}
}

func TestCommandCycles(t *testing.T) {
	// Spot checks against the psx-spx "GTE Opcode
	// Summary" Clk column.
	want := map[uint32]int{
		0x01: 15, // RTPS
		0x30: 23, // RTPT
		0x16: 44, // NCDT
		0x06: 8,  // NCLIP
		0x0C: 6,  // OP
		0x28: 5,  // SQR
		0x2A: 17, // DPCT
		0x2D: 5,  // AVSZ3
		0x2E: 6,  // AVSZ4
		0x3F: 39, // NCCT
		0x13: 19, // NCDS
		0x3D: 5,  // GPF
		0x3E: 5,  // GPL
	}
	for op, cycles := range want {
		if got := CommandCycles(op); got != cycles {
			t.Errorf("CommandCycles(0x%02X) = %d, want %d", op, got, cycles)
		}
	}
	if got := CommandCycles(0x3B); got != 0 {
		t.Errorf("CommandCycles(0x3B) = %d, want 0", got)
	}
	g := New()
	if got := g.Command(cmdWord(0x01, true, false)); got != 15 {
		t.Errorf("Command(RTPS) = %d cycles, want 15", got)
	}
}

// rtpsRig builds a GTE with identity rotation, zero
// translation, the given H, and screen offsets
// OFX=160.0, OFY=120.0 in 16.16 fixed point.
func rtpsRig(h uint32) *GTE {
	g := New()
	setMatrix(g, 0, identQ12)
	g.SetControl(26, h)       // H
	g.SetControl(24, 160<<16) // OFX
	g.SetControl(25, 120<<16) // OFY
	return g
}

func TestRTPSIdentity(t *testing.T) {
	g := rtpsRig(300)
	setV(g, 0, 100, 50, 400)
	if err := g.Execute(0x0180001); err != nil { // documented RTPS word
		t.Fatalf("RTPS: %v", err)
	}
	ir1, ir2, ir3 := irOf(g)
	if ir1 != 100 || ir2 != 50 || ir3 != 400 {
		t.Errorf("IR = (%d,%d,%d), want (100,50,400)", ir1, ir2, ir3)
	}
	if sz := g.Data(19); sz != 400 {
		t.Errorf("SZ3 = %d, want 400", sz)
	}
	// Ideal division per the doc formula:
	// n = ((H*20000h/SZ3)+1)/2 = 49152, so
	// SX = (n*100 + OFX) / 10000h = 235 and
	// SY = (n*50 + OFY) / 10000h = 157.
	// The hardware UNR division is inexact (measured
	// n = 49149 for this input), so the gate tolerance
	// is 2 pixels; SZ is exact.
	sxy := g.Data(14)
	sx := int64(int16(uint16(sxy)))
	sy := int64(int16(uint16(sxy >> 16)))
	if math.Abs(float64(sx-235)) > 2 {
		t.Errorf("SX2 = %d, want 235 +/- 2 (UNR division tolerance)", sx)
	}
	if math.Abs(float64(sy-157)) > 2 {
		t.Errorf("SY2 = %d, want 157 +/- 2 (UNR division tolerance)", sy)
	}
	if flag := g.Control(31); flag != 0 {
		t.Errorf("FLAG = 0x%08X, want 0 (no saturation or overflow)", flag)
	}
}

func TestRTPSDivideOverflowAndSZSaturation(t *testing.T) {
	// Near point: SZ3 = 100 <= H/2 = 150, so the
	// division overflows: the quotient clamps to
	// 1FFFFh and FLAG bit 17 (and the summary bit 31)
	// is set (psx-spx, RTPS note and "GTE Division
	// Inaccuracy").
	g := rtpsRig(300)
	setV(g, 0, 500, 50, 100)
	if err := g.Execute(0x0180001); err != nil {
		t.Fatalf("RTPS: %v", err)
	}
	flag := g.Control(31)
	if flag&(1<<17) == 0 {
		t.Errorf("FLAG = 0x%08X, want divide overflow bit 17 set", flag)
	}
	if flag&(1<<31) == 0 {
		t.Errorf("FLAG = 0x%08X, want summary bit 31 set", flag)
	}
	if sz := g.Data(19); sz != 100 {
		t.Errorf("SZ3 = %d, want 100 (the Z itself does not saturate)", sz)
	}
	// With the clamped quotient, SX saturates to the
	// +3FFh screen limit.
	if sx := int16(uint16(g.Data(14))); sx != 0x3FF {
		t.Errorf("SX2 = %d, want 0x3FF (saturated)", sx)
	}
	// Far point with sf=0: MAC3 = TRZ*1000h leaves
	// SZ3 = MAC3 SAR 12 above FFFFh, so SZ3 saturates
	// to FFFFh with FLAG bit 18.
	g2 := rtpsRig(300)
	g2.SetControl(7, 0x100000) // TRZ
	setV(g2, 0, 0, 0, 0)
	if err := g2.Execute(cmdWord(0x01, false, false)); err != nil {
		t.Fatalf("RTPS sf=0: %v", err)
	}
	if sz := g2.Data(19); sz != 0xFFFF {
		t.Errorf("SZ3 = 0x%X, want 0xFFFF (saturated)", sz)
	}
	if flag := g2.Control(31); flag&(1<<18) == 0 {
		t.Errorf("FLAG = 0x%08X, want SZ saturation bit 18 set", flag)
	}
}

func TestNCLIPWinding(t *testing.T) {
	g := New()
	pack := func(x, y int) uint32 {
		return uint32(uint16(int16(y)))<<16 | uint32(uint16(int16(x)))
	}
	// (0,0), (10,0), (0,10): MAC0 = 10*10 = +100.
	g.SetData(12, pack(0, 0))
	g.SetData(13, pack(10, 0))
	g.SetData(14, pack(0, 10))
	if err := g.Execute(cmdWord(0x06, false, false)); err != nil {
		t.Fatalf("NCLIP: %v", err)
	}
	if mac0 := int32(g.Data(24)); mac0 != 100 {
		t.Errorf("MAC0 = %d, want +100", mac0)
	}
	// Reversed winding flips the sign exactly.
	g.SetData(13, pack(0, 10))
	g.SetData(14, pack(10, 0))
	if err := g.Execute(cmdWord(0x06, false, false)); err != nil {
		t.Fatalf("NCLIP: %v", err)
	}
	if mac0 := int32(g.Data(24)); mac0 != -100 {
		t.Errorf("MAC0 = %d, want -100", mac0)
	}
}

func TestAVSZ(t *testing.T) {
	g := New()
	g.SetData(16, 50)  // SZ0
	g.SetData(17, 100) // SZ1
	g.SetData(18, 200) // SZ2
	g.SetData(19, 300) // SZ3
	// ZSF3 = 1/3 in 1.3.12 fixed point = 0x555.
	g.SetControl(29, 0x555)
	if err := g.Execute(cmdWord(0x2D, true, true)); err != nil {
		t.Fatalf("AVSZ3: %v", err)
	}
	// MAC0 = ZSF3*(SZ1+SZ2+SZ3), OTZ = MAC0/1000h.
	wantMAC0 := int64(0x555) * 600
	if mac0 := int64(int32(g.Data(24))); mac0 != wantMAC0 {
		t.Errorf("MAC0 = %d, want %d", mac0, wantMAC0)
	}
	if otz := g.Data(7); otz != uint32(wantMAC0>>12) {
		t.Errorf("OTZ = %d, want %d", otz, wantMAC0>>12)
	}
	// ZSF4 = 1/4 = 0x400.
	g.SetControl(30, 0x400)
	if err := g.Execute(cmdWord(0x2E, true, true)); err != nil {
		t.Fatalf("AVSZ4: %v", err)
	}
	wantMAC0 = int64(0x400) * 650
	if mac0 := int64(int32(g.Data(24))); mac0 != wantMAC0 {
		t.Errorf("MAC0 = %d, want %d", mac0, wantMAC0)
	}
	if otz := g.Data(7); otz != uint32(wantMAC0>>12) {
		t.Errorf("OTZ = %d, want %d", otz, wantMAC0>>12)
	}
}

func mvmvaWord(mx, v, cv uint32, sf, lm bool) uint32 {
	w := uint32(0x12) | mx<<17 | v<<15 | cv<<13
	if lm {
		w |= 1 << 10
	}
	if sf {
		w |= 1 << 19
	}
	return w
}

func TestMVMVA(t *testing.T) {
	g := New()
	// Rotation matrix with a 0.5 cross term.
	setMatrix(g, 0, [3][3]int{
		{0x1000, 0x800, 0},
		{0, 0x1000, 0},
		{0, 0, 0x1000},
	})
	setV(g, 0, 100, 200, 50)
	if err := g.Execute(mvmvaWord(0, 0, 0, true, false)); err != nil {
		t.Fatalf("MVMVA: %v", err)
	}
	// Float64 reference at Q12 scale.
	fx := (1.0*100 + 0.5*200)
	if ir1, ir2, ir3 := irOf(g); math.Abs(float64(ir1)-fx) > 1 ||
		math.Abs(float64(ir2)-200) > 1 || math.Abs(float64(ir3)-50) > 1 {
		t.Errorf("IR = (%d,%d,%d), want near (%v,200,50)", ir1, ir2, ir3, fx)
	}
	if m1, m2, m3 := macOf(g); m1 != 200 || m2 != 200 || m3 != 50 {
		t.Errorf("MAC = (%d,%d,%d), want (200,200,50)", m1, m2, m3)
	}
	// Translation select cv=1 adds BK (each component
	// shifted by 12 before the SAR 12, so BK passes
	// through unchanged at sf=1).
	g.SetControl(13, 10)
	g.SetControl(14, 20)
	g.SetControl(15, 30)
	if err := g.Execute(mvmvaWord(0, 0, 1, true, false)); err != nil {
		t.Fatalf("MVMVA cv=BK: %v", err)
	}
	if ir1, ir2, ir3 := irOf(g); ir1 != 210 || ir2 != 220 || ir3 != 80 {
		t.Errorf("IR = (%d,%d,%d), want (210,220,80)", ir1, ir2, ir3)
	}
	// Light matrix path (mx=1), no translation.
	setMatrix(g, 8, [3][3]int{
		{0x2000, 0, 0},
		{0, 0x2000, 0},
		{0, 0, 0x2000},
	})
	if err := g.Execute(mvmvaWord(1, 0, 3, true, false)); err != nil {
		t.Fatalf("MVMVA mx=LLM: %v", err)
	}
	if ir1, ir2, ir3 := irOf(g); ir1 != 200 || ir2 != 400 || ir3 != 100 {
		t.Errorf("IR = (%d,%d,%d), want (200,400,100)", ir1, ir2, ir3)
	}
	// Bugged far-color path (cv=2): only the last two
	// product terms survive, MAC1 = (RT12*VY0 +
	// RT13*VZ0) SAR 12 = (0x800*200 + 0) SAR 12 = 100.
	g.SetControl(21, 999) // RFC, must not contribute
	if err := g.Execute(mvmvaWord(0, 0, 2, true, false)); err != nil {
		t.Fatalf("MVMVA cv=FC: %v", err)
	}
	if m1, _, _ := macOf(g); m1 != 100 {
		t.Errorf("MAC1 = %d, want 100 (bugged FC path drops term 1)", m1)
	}
}

// colorRig builds a GTE for the color commands: light
// matrix with only L33 set, light color matrix diag,
// zero background and far colors, RGBC set by the
// caller.
func colorRig() *GTE {
	g := New()
	setMatrix(g, 8, [3][3]int{
		{0, 0, 0},
		{0, 0, 0},
		{0, 0, 0x1000},
	})
	setMatrix(g, 16, identQ12)
	return g
}

func TestNCDSExact(t *testing.T) {
	g := colorRig()
	// Half-scale light color matrix.
	setMatrix(g, 16, [3][3]int{
		{0x800, 0, 0},
		{0, 0x800, 0},
		{0, 0, 0x800},
	})
	// RGBC: R=80h, G=40h, B=20h, CODE=5Ah.
	g.SetData(6, 0x5A<<24|0x20<<16|0x40<<8|0x80)
	setV(g, 0, 0, 0, 0x1000) // normal facing the light
	// Hand computation per the doc steps (sf=1):
	// step 1: IR = LLM*V0 SAR 12 = (0,0,1000h)
	// step 2: IR = (BK + LCM*IR) SAR 12 = (0,0,800h)
	// step 3: MAC = (R*IR1, G*IR2, B*IR3) SHL 4
	//             = (0, 0, 20h*800h) SHL 4 = (0,0,100000h)
	// step 4: IR0 = 0, so the depth cue changes
	//         nothing.
	// step 5: MAC = MAC SAR 12 = (0,0,100h)
	// FIFO byte B = 100h/16 = 10h, IR3 = 100h.
	if err := g.Execute(0x0E80413); err != nil { // documented NCDS word
		t.Fatalf("NCDS: %v", err)
	}
	if _, _, m3 := macOf(g); m3 != 0x100 {
		t.Errorf("MAC3 = 0x%X, want 0x100", m3)
	}
	if _, _, ir3 := irOf(g); ir3 != 0x100 {
		t.Errorf("IR3 = 0x%X, want 0x100", ir3)
	}
	if rgb2 := g.Data(22); rgb2 != 0x5A<<24|0x10<<16 {
		t.Errorf("RGB2 = 0x%08X, want 0x5A100000", rgb2)
	}
	// Full interpolation to the far color (IR0=1000h,
	// FC=0) pulls the result all the way to zero.
	g.SetData(8, 0x1000)
	if err := g.Execute(0x0E80413); err != nil {
		t.Fatalf("NCDS: %v", err)
	}
	if rgb2 := g.Data(22); rgb2 != 0x5A<<24 {
		t.Errorf("RGB2 = 0x%08X, want 0x5A000000 (full depth cue to FC=0)", rgb2)
	}
}

func TestColorCommands(t *testing.T) {
	const code = 0x63
	setRGBC := func(g *GTE, r, gg, b int) {
		g.SetData(6, code<<24|uint32(b)<<16|uint32(gg)<<8|uint32(r))
	}
	halfLCM := func(g *GTE) {
		setMatrix(g, 16, [3][3]int{
			{0x800, 0, 0},
			{0, 0x800, 0},
			{0, 0, 0x800},
		})
	}

	t.Run("NCS", func(t *testing.T) {
		g := colorRig()
		halfLCM(g)
		setRGBC(g, 0, 0, 0)
		setV(g, 0, 0, 0, 0x1000)
		if err := g.Execute(cmdWord(0x1E, true, true)); err != nil {
			t.Fatalf("NCS: %v", err)
		}
		// IR = (0,0,800h); FIFO bytes = MAC/16.
		if rgb2 := g.Data(22); rgb2 != code<<24|0x80<<16 {
			t.Errorf("RGB2 = 0x%08X, want code|0x800000", rgb2)
		}
		if _, _, ir3 := irOf(g); ir3 != 0x800 {
			t.Errorf("IR3 = 0x%X, want 0x800", ir3)
		}
	})

	t.Run("NCT", func(t *testing.T) {
		g := colorRig()
		halfLCM(g)
		setRGBC(g, 0, 0, 0)
		for i := 0; i < 3; i++ {
			setV(g, i, 0, 0, 0x1000)
		}
		if err := g.Execute(cmdWord(0x20, true, true)); err != nil {
			t.Fatalf("NCT: %v", err)
		}
		want := uint32(code<<24 | 0x80<<16)
		if g.Data(20) != want || g.Data(21) != want || g.Data(22) != want {
			t.Errorf("RGB FIFO = %08X %08X %08X, want all %08X",
				g.Data(20), g.Data(21), g.Data(22), want)
		}
	})

	t.Run("NCCS", func(t *testing.T) {
		g := colorRig()
		halfLCM(g)
		setRGBC(g, 0x80, 0x80, 0x80)
		setV(g, 0, 0, 0, 0x1000)
		if err := g.Execute(cmdWord(0x1B, true, true)); err != nil {
			t.Fatalf("NCCS: %v", err)
		}
		// MAC3 = (80h*800h) SHL 4 SAR 12 = 400h; byte 40h.
		if rgb2 := g.Data(22); rgb2 != code<<24|0x40<<16 {
			t.Errorf("RGB2 = 0x%08X, want code|0x400000", rgb2)
		}
		if _, _, ir3 := irOf(g); ir3 != 0x400 {
			t.Errorf("IR3 = 0x%X, want 0x400", ir3)
		}
	})

	t.Run("NCCT", func(t *testing.T) {
		g := colorRig()
		halfLCM(g)
		setRGBC(g, 0x80, 0x80, 0x80)
		for i := 0; i < 3; i++ {
			setV(g, i, 0, 0, 0x1000)
		}
		if err := g.Execute(cmdWord(0x3F, true, true)); err != nil {
			t.Fatalf("NCCT: %v", err)
		}
		want := uint32(code<<24 | 0x40<<16)
		if g.Data(20) != want || g.Data(21) != want || g.Data(22) != want {
			t.Errorf("RGB FIFO = %08X %08X %08X, want all %08X",
				g.Data(20), g.Data(21), g.Data(22), want)
		}
	})

	t.Run("CC", func(t *testing.T) {
		g := colorRig()
		setRGBC(g, 0xFF, 0x80, 0x40)
		setIR(g, 0x1000, 0x800, 0x400)
		if err := g.Execute(0x138041C); err != nil { // documented CC word
			t.Fatalf("CC: %v", err)
		}
		// Identity LCM: MAC = (FFh*1000h, 80h*800h,
		// 40h*400h) SHL 4 SAR 12 = (FF0h, 400h, 100h);
		// bytes (FFh, 40h, 10h).
		if rgb2 := g.Data(22); rgb2 != code<<24|0x10<<16|0x40<<8|0xFF {
			t.Errorf("RGB2 = 0x%08X, want 0x631040FF", rgb2)
		}
		if ir1, ir2, ir3 := irOf(g); ir1 != 0xFF0 || ir2 != 0x400 || ir3 != 0x100 {
			t.Errorf("IR = (%X,%X,%X), want (FF0,400,100)", ir1, ir2, ir3)
		}
	})

	t.Run("CDP", func(t *testing.T) {
		g := colorRig()
		setRGBC(g, 0xFF, 0x80, 0x40)
		setIR(g, 0x1000, 0x800, 0x400)
		g.SetData(8, 0x1000) // IR0: full depth cue to FC=0
		if err := g.Execute(cmdWord(0x14, true, true)); err != nil {
			t.Fatalf("CDP: %v", err)
		}
		if rgb2 := g.Data(22); rgb2 != code<<24 {
			t.Errorf("RGB2 = 0x%08X, want code only (cued to far color 0)", rgb2)
		}
		if ir1, ir2, ir3 := irOf(g); ir1 != 0 || ir2 != 0 || ir3 != 0 {
			t.Errorf("IR = (%d,%d,%d), want (0,0,0)", ir1, ir2, ir3)
		}
	})

	t.Run("DCPL", func(t *testing.T) {
		g := colorRig()
		setRGBC(g, 0x80, 0, 0)
		setIR(g, 0x1000, 0, 0)
		if err := g.Execute(cmdWord(0x29, true, true)); err != nil {
			t.Fatalf("DCPL: %v", err)
		}
		// MAC1 = (80h*1000h) SHL 4 SAR 12 = 800h (IR0=0
		// disables the cue); byte R = 80h.
		if rgb2 := g.Data(22); rgb2 != code<<24|0x80 {
			t.Errorf("RGB2 = 0x%08X, want code|0x80", rgb2)
		}
		if ir1, _, _ := irOf(g); ir1 != 0x800 {
			t.Errorf("IR1 = 0x%X, want 0x800", ir1)
		}
	})

	t.Run("DPCS", func(t *testing.T) {
		g := colorRig()
		setRGBC(g, 0x80, 0x40, 0x20)
		if err := g.Execute(cmdWord(0x10, true, false)); err != nil {
			t.Fatalf("DPCS: %v", err)
		}
		// Bytes round trip: MAC = byte SHL 16, SAR 12,
		// byte = MAC/16 gives the original bytes.
		if rgb2 := g.Data(22); rgb2 != code<<24|0x20<<16|0x40<<8|0x80 {
			t.Errorf("RGB2 = 0x%08X, want 0x63204080", rgb2)
		}
		if ir1, ir2, ir3 := irOf(g); ir1 != 0x800 || ir2 != 0x400 || ir3 != 0x200 {
			t.Errorf("IR = (%X,%X,%X), want (800,400,200)", ir1, ir2, ir3)
		}
	})

	t.Run("DPCT", func(t *testing.T) {
		g := colorRig()
		setRGBC(g, 0, 0, 0)                           // only CODE is read from RGBC
		g.SetData(20, 0x11<<24|0x20<<16|0x40<<8|0x80) // A
		g.SetData(21, 0x22<<24|0x04<<16|0x08<<8|0x10) // B
		g.SetData(22, 0x33<<24|0x03<<16|0x01<<8|0x02) // C
		if err := g.Execute(cmdWord(0x2A, true, false)); err != nil {
			t.Fatalf("DPCT: %v", err)
		}
		// With IR0=0 each entry round trips its bytes
		// and gains the RGBC code; the FIFO order is
		// preserved (oldest result in RGB0).
		if rgb0 := g.Data(20); rgb0 != code<<24|0x20<<16|0x40<<8|0x80 {
			t.Errorf("RGB0 = 0x%08X, want processed A", rgb0)
		}
		if rgb2 := g.Data(22); rgb2 != code<<24|0x03<<16|0x01<<8|0x02 {
			t.Errorf("RGB2 = 0x%08X, want processed C", rgb2)
		}
	})

	t.Run("INTPL", func(t *testing.T) {
		g := colorRig()
		setRGBC(g, 0, 0, 0)
		setIR(g, 0x1000, 0x800, 0x400)
		g.SetData(8, 0x800) // IR0 = 0.5 toward FC=0
		if err := g.Execute(cmdWord(0x11, true, true)); err != nil {
			t.Fatalf("INTPL: %v", err)
		}
		// Half-way interpolation halves every
		// component exactly.
		if ir1, ir2, ir3 := irOf(g); ir1 != 0x800 || ir2 != 0x400 || ir3 != 0x200 {
			t.Errorf("IR = (%X,%X,%X), want (800,400,200)", ir1, ir2, ir3)
		}
		if rgb2 := g.Data(22); rgb2 != code<<24|0x20<<16|0x40<<8|0x80 {
			t.Errorf("RGB2 = 0x%08X, want 0x63204080", rgb2)
		}
	})

	t.Run("OP", func(t *testing.T) {
		g := New()
		setMatrix(g, 0, [3][3]int{
			{0x1000, 0, 0},
			{0, 0x1000, 0},
			{0, 0, 0x1000},
		})
		setIR(g, 0x10, 0x20, 0x30)
		if err := g.Execute(cmdWord(0x0C, false, false)); err != nil {
			t.Fatalf("OP: %v", err)
		}
		// MAC = (30h*1000h - 20h*1000h,
		//        10h*1000h - 30h*1000h,
		//        20h*1000h - 10h*1000h)
		m1, m2, m3 := macOf(g)
		if m1 != 0x10000 || m2 != -0x20000 || m3 != 0x10000 {
			t.Errorf("MAC = (%X,%X,%X), want (10000,-20000,10000)", m1, m2, m3)
		}
		// All three IRs saturate (lm=0 range).
		if ir1, ir2, ir3 := irOf(g); ir1 != 0x7FFF || ir2 != -0x8000 || ir3 != 0x7FFF {
			t.Errorf("IR = (%X,%X,%X), want (7FFF,-8000,7FFF)", ir1, ir2, ir3)
		}
	})

	t.Run("SQR sf=1", func(t *testing.T) {
		g := New()
		setIR(g, 0x1000, 0x800, 0x400)
		if err := g.Execute(cmdWord(0x28, true, true)); err != nil {
			t.Fatalf("SQR: %v", err)
		}
		m1, m2, m3 := macOf(g)
		if m1 != 0x1000 || m2 != 0x400 || m3 != 0x100 {
			t.Errorf("MAC = (%X,%X,%X), want (1000,400,100)", m1, m2, m3)
		}
	})

	t.Run("SQR sf=0", func(t *testing.T) {
		g := New()
		setIR(g, 0x1000, 0x800, 0x400)
		if err := g.Execute(cmdWord(0x28, false, true)); err != nil {
			t.Fatalf("SQR: %v", err)
		}
		m1, m2, m3 := macOf(g)
		if m1 != 0x1000000 || m2 != 0x400000 || m3 != 0x100000 {
			t.Errorf("MAC = (%X,%X,%X), want (1000000,400000,100000)", m1, m2, m3)
		}
		// Unshifted squares exceed 7FFFh, so all IRs
		// saturate to the 7FFFh maximum.
		if ir1, ir2, ir3 := irOf(g); ir1 != 0x7FFF || ir2 != 0x7FFF || ir3 != 0x7FFF {
			t.Errorf("IR = (%X,%X,%X), want all 7FFF", ir1, ir2, ir3)
		}
	})

	t.Run("GPF", func(t *testing.T) {
		g := colorRig()
		setRGBC(g, 0, 0, 0)
		setIR(g, 0x1000, 0x800, 0x400)
		g.SetData(8, 0x800) // IR0
		if err := g.Execute(cmdWord(0x3D, true, true)); err != nil {
			t.Fatalf("GPF: %v", err)
		}
		// MAC = (IR*IR0) SAR 12 = half of IR.
		if ir1, ir2, ir3 := irOf(g); ir1 != 0x800 || ir2 != 0x400 || ir3 != 0x200 {
			t.Errorf("IR = (%X,%X,%X), want (800,400,200)", ir1, ir2, ir3)
		}
		if rgb2 := g.Data(22); rgb2 != code<<24|0x20<<16|0x40<<8|0x80 {
			t.Errorf("RGB2 = 0x%08X, want 0x63204080", rgb2)
		}
	})

	t.Run("GPL", func(t *testing.T) {
		g := colorRig()
		setRGBC(g, 0, 0, 0)
		g.SetData(25, 0x100) // MAC1
		g.SetData(26, 0x100) // MAC2
		g.SetData(27, 0x100) // MAC3
		setIR(g, 0x100, 0, 0)
		g.SetData(8, 0x1000) // IR0 = 1.0
		if err := g.Execute(cmdWord(0x3E, true, true)); err != nil {
			t.Fatalf("GPL: %v", err)
		}
		// MAC = ((IR*IR0) + (MAC SHL 12)) SAR 12:
		// MAC1 = (100000h + 100000h) SAR 12 = 200h,
		// MAC2 = MAC3 = 100h.
		if ir1, ir2, ir3 := irOf(g); ir1 != 0x200 || ir2 != 0x100 || ir3 != 0x100 {
			t.Errorf("IR = (%X,%X,%X), want (200,100,100)", ir1, ir2, ir3)
		}
	})
}

func TestOPOverflowSetsFlagSummary(t *testing.T) {
	g := New()
	setMatrix(g, 0, [3][3]int{
		{0x7FFF, 0, 0},
		{0, 0x7FFF, 0},
		{0, 0, 0x7FFF},
	})
	setIR(g, 0x7FFF, -0x8000, 0x7FFF)
	if err := g.Execute(cmdWord(0x0C, false, false)); err != nil {
		t.Fatalf("OP: %v", err)
	}
	// MAC1 = 7FFFh*7FFFh + 8000h*7FFFh = 7FFE8001h,
	// exact in the 44-bit accumulator.
	if m1, _, _ := macOf(g); m1 != 0x7FFE8001 {
		t.Errorf("MAC1 = 0x%X, want 0x7FFE8001", m1)
	}
	flag := g.Control(31)
	// Cross products of two signed 16-bit vectors are
	// bounded by 2^31, so the 44-bit MAC overflow bits
	// (30..25) cannot fire for OP; the overflow
	// surfaces through the IR saturation bits 24..22,
	// and bits 24/23 feed the summary bit 31.
	if flag&(1<<24) == 0 {
		t.Errorf("FLAG = 0x%08X, want IR1 saturation bit 24 set", flag)
	}
	if flag&(1<<31) == 0 {
		t.Errorf("FLAG = 0x%08X, want summary bit 31 set", flag)
	}
	if flag&(0x3F<<25) != 0 {
		t.Errorf("FLAG = 0x%08X, MAC overflow bits 30..25 unexpectedly set", flag)
	}
	// A genuine 44-bit MAC overflow: RTPS with sf=0, a
	// maximal translation and a maximal rotated Z
	// leave MAC3 = TRZ*1000h + RT33*VZ0 beyond 43
	// bits (TRZ*1000h alone is 2^43 - 2^12, just
	// inside the range; the product term pushes it
	// over).
	g2 := New()
	setMatrix(g2, 0, identQ12)
	g2.SetControl(7, 0x7FFFFFFF) // TRZ
	setV(g2, 0, 0, 0, 0x7FFF)
	if err := g2.Execute(cmdWord(0x01, false, false)); err != nil {
		t.Fatalf("RTPS: %v", err)
	}
	flag2 := g2.Control(31)
	if flag2&(1<<28) == 0 {
		t.Errorf("FLAG = 0x%08X, want MAC3 positive overflow bit 28 set", flag2)
	}
	if flag2&(1<<31) == 0 {
		t.Errorf("FLAG = 0x%08X, want summary bit 31 set", flag2)
	}
}

func TestSXYFIFOOrder(t *testing.T) {
	g := New()
	setMatrix(g, 0, identQ12)
	g.SetControl(26, 256) // H
	// OFX/OFY are zero. Three distinct vertices pushed
	// by one RTPT (V0, V1, V2 in order).
	setV(g, 0, 10, 20, 200)
	setV(g, 1, 30, 40, 200)
	setV(g, 2, 50, 60, 200)
	if err := g.Execute(0x0280030); err != nil { // documented RTPT word
		t.Fatalf("RTPT: %v", err)
	}
	sxOf := func(reg uint32) int {
		return int(int16(uint16(g.Data(reg))))
	}
	sx0, sx1, sx2 := sxOf(12), sxOf(13), sxOf(14)
	// Ideal SX = H*x/z = 256*x/200: 12, 38, 64, with
	// the UNR division tolerance of 2 pixels.
	for i, tc := range []struct{ got, want int }{{sx0, 12}, {sx1, 38}, {sx2, 64}} {
		if math.Abs(float64(tc.got-tc.want)) > 2 {
			t.Errorf("SXY%d SX = %d, want %d +/- 2", i, tc.got, tc.want)
		}
	}
	if !(sx0 < sx1 && sx1 < sx2) {
		t.Errorf("FIFO order wrong: SX = %d, %d, %d", sx0, sx1, sx2)
	}
	// SXYP reads back SXY2.
	if g.Data(15) != g.Data(14) {
		t.Errorf("SXYP = 0x%08X, want SXY2 = 0x%08X", g.Data(15), g.Data(14))
	}
	// Writing SXYP pushes the FIFO by hand: SXY0 takes
	// the old SXY1, SXY1 the old SXY2, SXY2 the value.
	prev1, prev2 := g.Data(13), g.Data(14)
	g.SetData(15, 0x00010002)
	if g.Data(12) != prev1 || g.Data(13) != prev2 {
		t.Errorf("SXYP push: SXY0=0x%08X SXY1=0x%08X, want old SXY1/SXY2",
			g.Data(12), g.Data(13))
	}
	if g.Data(14) != 0x00010002 {
		t.Errorf("SXY2 = 0x%08X, want 0x00010002 after SXYP write", g.Data(14))
	}
}

func TestLZCR(t *testing.T) {
	cases := []struct {
		in   uint32
		want uint32
	}{
		{0x0000FFFF, 16},
		{0xFFFF0000, 16},
		{0xFFFFFFFF, 32},
		{0x00000000, 32},
		{0x7FFFFFFF, 1},
	}
	g := New()
	for _, tc := range cases {
		g.SetData(30, tc.in)
		if got := g.Data(31); got != tc.want {
			t.Errorf("LZCR for LZCS=0x%08X = %d, want %d", tc.in, got, tc.want)
		}
		if got := g.Data(30); got != tc.in {
			t.Errorf("LZCS read = 0x%08X, want 0x%08X", got, tc.in)
		}
	}
}

func TestIRGBORGBConversion(t *testing.T) {
	g := New()
	// Writing IRGB expands the 5-bit fields into
	// IR1..IR3 multiplied by 80h.
	g.SetData(28, 0x7FFF) // R=G=B=1Fh
	for i := 0; i < 3; i++ {
		if v := g.Data(uint32(9 + i)); v != 0xF80 {
			t.Errorf("IR%d = 0x%X, want 0xF80", i+1, v)
		}
	}
	// Both IRGB and ORGB read back the collapsed
	// 5:5:5 value.
	if v := g.Data(28); v != 0x7FFF {
		t.Errorf("IRGB read = 0x%X, want 0x7FFF", v)
	}
	if v := g.Data(29); v != 0x7FFF {
		t.Errorf("ORGB read = 0x%X, want 0x7FFF", v)
	}
	// Direct IR writes are reflected: 1000h collapses
	// to 20h, saturated to 1Fh; a negative component
	// saturates to 0.
	setIR(g, 0x1000, -1, 0x80)
	if v := g.Data(29); v != 0x1F|(1<<10) {
		t.Errorf("ORGB read = 0x%X, want 0x%X", v, 0x1F|(1<<10))
	}
	// ORGB is read-only: writes are ignored.
	g.SetData(29, 0)
	if v := g.Data(29); v != 0x1F|(1<<10) {
		t.Errorf("ORGB after write = 0x%X, want unchanged 0x%X", v, 0x1F|(1<<10))
	}
}

func TestUNRTableGeneration(t *testing.T) {
	// Spot values from the table printed in psx-spx,
	// "GTE Division Inaccuracy".
	want := map[int]int64{
		0x00: 0xFF, 0x01: 0xFD, 0x02: 0xFB, 0x0F: 0xE3,
		0x40: 0x99, 0x80: 0x54, 0xFF: 0x00, 0x100: 0x00,
	}
	for i, v := range want {
		if unrTable[i] != v {
			t.Errorf("unrTable[0x%X] = 0x%X, want 0x%X", i, unrTable[i], v)
		}
	}
}

func TestUNRErrorBound(t *testing.T) {
	// The UNR division error bound, gated against
	// exact division over 20,000 deterministic
	// pseudo-random (H, SZ) pairs in the
	// non-overflow, non-saturating range: the exact
	// documented value is (H*20000h/SZ+1)/2, which is
	// H*10000h/SZ + 0.5. Pairs whose exact value
	// reaches the 1FFFFh saturation are skipped
	// (that path is pinned by the overflow test).
	e := &engine{g: New()}
	rng := uint32(0x12345678)
	next := func() uint32 {
		rng = rng*1664525 + 1013904223
		return rng >> 8
	}
	maxErr := 0.0
	tested := 0
	for i := 0; i < 20000; i++ {
		h := next()&0xFFFF + 1
		sz := next()&0xFFFF + 1
		if h >= sz*2 {
			continue // overflow branch
		}
		exact := float64(h)*65536.0/float64(sz) + 0.5
		if exact >= 0x1FFFF {
			continue // saturation path
		}
		e.g.control[26] = h
		e.flag = 0
		got := e.divide(sz)
		d := float64(got) - exact
		if d < 0 {
			d = -d
		}
		if d > maxErr {
			maxErr = d
		}
		tested++
	}
	if tested < 10000 {
		t.Fatalf("only %d pairs in range, want at least 10000", tested)
	}
	if maxErr > 3.0 {
		t.Errorf("UNR max error = %.3f over %d pairs, want <= 3.0", maxErr, tested)
	}
	t.Logf("UNR error bound: max %.3f over %d pairs", maxErr, tested)
}
