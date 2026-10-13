package cart

import "testing"

// buildImage makes an iNES image whose PRG bytes identify their
// 8 KB chunk (byte value = chunk index) and whose CHR bytes
// identify their 1 KB chunk, so bank checks read the pattern back.
func buildImage(mapper int, prgKB, chrKB int, flags byte) []byte {
	img := []byte{'N', 'E', 'S', 0x1A,
		byte(prgKB / 16), byte(chrKB / 8),
		byte(mapper<<4) | flags, byte(mapper & 0xF0),
		0, 0, 0, 0, 0, 0, 0, 0}
	for i := 0; i < prgKB*1024; i++ {
		img = append(img, byte(i/8192))
	}
	for i := 0; i < chrKB*1024; i++ {
		img = append(img, byte(i/1024))
	}
	return img
}

func mustCart(t *testing.T, img []byte) *Cart {
	t.Helper()
	c, err := New(img)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// writeBits feeds an MMC1 register through the serial port, one
// value bit per write, LSB first, with generous cycle spacing.
func writeBits(c *Cart, addr uint16, v uint8, cycle *int64) {
	for i := 0; i < 5; i++ {
		*cycle += 10
		c.SetCycle(*cycle)
		c.WritePRG(addr, (v>>uint(i))&1)
	}
}

func TestHeaderParse(t *testing.T) {
	c := mustCart(t, buildImage(4, 32, 8, 0x03)) // battery + vertical
	h := c.Header
	if h.Mapper != 4 || h.PRGSize != 32768 || h.CHRSize != 8192 {
		t.Fatalf("header = %+v", h)
	}
	if h.Mirror != MirrorVertical || !h.Battery {
		t.Fatalf("header mirror/battery = %v/%v", h.Mirror, h.Battery)
	}
	if h.PRGAMSize() != 8192 {
		t.Fatalf("battery RAM = %d, want 8192", h.PRGAMSize())
	}
}

func TestHeaderNES2(t *testing.T) {
	// A NES 2.0 header with a 12-bit mapper and an
	// exponent-encoded PRG size: E=10, M=1 gives 2^10 * 3 = 3072.
	img := []byte{'N', 'E', 'S', 0x1A, 41, 0, 0x40, 0x08, 0x31, 0x0F,
		0, 0, 0, 0, 0, 0}
	h, _, err := parseHeader(img)
	if err != nil {
		t.Fatal(err)
	}
	if !h.NES2 {
		t.Fatalf("NES2 flag not detected")
	}
	if h.Mapper != 0x104 || h.Submapper != 3 {
		t.Fatalf("mapper/submapper = %X/%X, want 104/3", h.Mapper, h.Submapper)
	}
	if h.PRGSize != 3072 {
		t.Fatalf("PRGSize = %d, want 3072 (exponent form)", h.PRGSize)
	}
}

func TestHeaderGarbage(t *testing.T) {
	if _, err := New([]byte{1, 2, 3}); err == nil {
		t.Fatalf("short image accepted")
	}
	bad := buildImage(0, 16, 8, 0)
	bad[0] = 'X'
	if _, err := New(bad); err == nil {
		t.Fatalf("bad magic accepted")
	}
	trunc := buildImage(0, 32, 8, 0)
	if _, err := New(trunc[:len(trunc)-100]); err == nil {
		t.Fatalf("truncated image accepted")
	}
	if _, err := New(buildImage(5, 16, 8, 0)); err == nil {
		t.Fatalf("unimplemented mapper accepted")
	}
}

func TestNROMMirror(t *testing.T) {
	c := mustCart(t, buildImage(0, 16, 8, 0))
	// A 16 KB PRG repeats at $C000.
	if c.ReadPRG(0x8000) != c.ReadPRG(0xC000) {
		t.Fatalf("16 KB mirror broken: %02X vs %02X", c.ReadPRG(0x8000), c.ReadPRG(0xC000))
	}
	if c.ReadPRG(0x8000) != 0 || c.ReadPRG(0xA000) != 1 {
		t.Fatalf("NROM pattern wrong")
	}
}

func TestUxROM(t *testing.T) {
	c := mustCart(t, buildImage(2, 128, 8, 0)) // 8 PRG chunks of 8 KB
	c.WritePRG(0x8000, 2)                      // bank 2 = chunks 4,5
	if got := c.ReadPRG(0x8000); got != 4 {
		t.Fatalf("$8000 chunk = %d, want 4", got)
	}
	if got := c.ReadPRG(0xC000); got != 14 { // last 16 KB bank = 8K chunks 14,15
		t.Fatalf("$C000 chunk = %d, want 14", got)
	}
}

func TestCNROM(t *testing.T) {
	c := mustCart(t, buildImage(3, 32, 32, 0))
	c.WritePRG(0x8000, 2)
	if got := c.ReadCHR(0); got != 16 { // CHR bank 2 starts at 1 KB chunk 16
		t.Fatalf("CHR chunk = %d, want 16", got)
	}
}

func TestAxROM(t *testing.T) {
	c := mustCart(t, buildImage(7, 128, 8, 0))
	c.WritePRG(0x8000, 3)
	if got := c.ReadPRG(0x8000); got != 12 { // 32 KB bank 3 = chunk 12
		t.Fatalf("AxROM chunk = %d, want 12", got)
	}
	if c.MirrorMode() != MirrorSingle0 {
		t.Fatalf("mirror = %v, want single low", c.MirrorMode())
	}
	c.WritePRG(0x8000, 0x10|3)
	if c.MirrorMode() != MirrorSingle1 {
		t.Fatalf("mirror = %v, want single high", c.MirrorMode())
	}
}

func TestLatchMappers(t *testing.T) {
	cd := mustCart(t, buildImage(11, 128, 64, 0))
	cd.WritePRG(0x8000, 0x21) // PRG bank 1, CHR bank 2
	if got := cd.ReadPRG(0x8000); got != 4 {
		t.Fatalf("Color Dreams PRG chunk = %d, want 4", got)
	}
	if got := cd.ReadCHR(0); got != 16 {
		t.Fatalf("Color Dreams CHR chunk = %d, want 16", got)
	}
	gx := mustCart(t, buildImage(66, 128, 32, 0))
	gx.WritePRG(0x8000, 0x21)
	if got := gx.ReadPRG(0x8000); got != 4 {
		t.Fatalf("GxROM PRG chunk = %d, want 4", got)
	}
	if got := gx.ReadCHR(0); got != 16 {
		t.Fatalf("GxROM CHR chunk = %d, want 16", got)
	}
}

func TestMMC1Banking(t *testing.T) {
	c := mustCart(t, buildImage(1, 64, 32, 0x02)) // battery: PRG RAM present
	var cycle int64
	// Reset the shift register, then write control $1E:
	// horizontal? No: bits 0-1 = 2 (vertical), PRG mode 3,
	// CHR mode 1 (two 4 KB banks).
	cycle += 10
	c.SetCycle(cycle)
	c.WritePRG(0x8000, 0x80)
	writeBits(c, 0x8000, 0x1E, &cycle)
	if c.MirrorMode() != MirrorVertical {
		t.Fatalf("MMC1 mirror = %v, want vertical", c.MirrorMode())
	}
	// PRG bank 2 in the low window; the high window is fixed to
	// the last bank (chunks 6,7).
	writeBits(c, 0xE000, 2, &cycle)
	if got := c.ReadPRG(0x8000); got != 4 {
		t.Fatalf("MMC1 low window chunk = %d, want 4", got)
	}
	if got := c.ReadPRG(0xC000); got != 6 {
		t.Fatalf("MMC1 high window chunk = %d, want 6", got)
	}
	// CHR bank 3 in the low 4 KB: the pattern marks 1 KB chunks,
	// so bank 3 reads back as chunk 12.
	writeBits(c, 0xA000, 3, &cycle)
	if got := c.ReadCHR(0); got != 12 {
		t.Fatalf("MMC1 CHR chunk = %d, want 12", got)
	}
}

func TestMMC1ResetWrite(t *testing.T) {
	c := mustCart(t, buildImage(1, 64, 32, 0))
	var cycle int64
	// Start a control write, abandon it halfway, reset, and
	// write a full value: only the post-reset bits may land.
	for _, bit := range []uint8{1, 1, 1} {
		cycle += 10
		c.SetCycle(cycle)
		c.WritePRG(0x8000, bit)
	}
	cycle += 10
	c.SetCycle(cycle)
	c.WritePRG(0x8000, 0x80) // reset
	if c.MirrorMode() == MirrorHorizontal && false {
		t.Fatal("unreachable")
	}
	writeBits(c, 0x8000, 0x1C, &cycle) // vertical? bits: mirroring 0 → single low
	if c.MirrorMode() != MirrorSingle0 {
		t.Fatalf("after reset write, mirror = %v, want single low", c.MirrorMode())
	}
}

func TestMMC1ConsecutiveWriteIgnored(t *testing.T) {
	c := mustCart(t, buildImage(1, 64, 32, 0))
	// Write control $12 (vertical, PRG mode 0, CHR 4 KB mode),
	// but deliver bit 1 one cycle after bit 0: that write must
	// be dropped, so the remaining bits only commit when the
	// sixth write arrives.
	bits := []struct {
		cycle int64
		bit   uint8
	}{
		{100, 0}, // bit 0 of $12
		{101, 1}, // bit 1, consecutive: ignored
		{110, 1}, // bit 1 again
		{120, 0}, // bit 2
		{130, 0}, // bit 3
		{140, 1}, // bit 4: commits $12
	}
	for _, w := range bits {
		c.SetCycle(w.cycle)
		c.WritePRG(0x8000, w.bit)
	}
	// CHR mode is 4 KB: CHR bank register 3 shows at $0000 as
	// 1 KB chunk 12. If the consecutive write had counted,
	// control would be $06 (8 KB CHR) and the same register
	// would show 8 KB bank 1, chunk 8.
	var cycle int64 = 1000
	writeBits(c, 0xA000, 3, &cycle)
	if got := c.ReadCHR(0); got != 12 {
		t.Fatalf("CHR chunk = %d, want 12 (consecutive write was not ignored?)", got)
	}
}

func TestMMC1RAMGate(t *testing.T) {
	c := mustCart(t, buildImage(1, 64, 32, 0x02))
	var cycle int64
	writeBits(c, 0xE000, 0x10, &cycle) // PRG register bit 4: RAM disabled
	c.WritePRG(0x6000, 0xAB)
	if got := c.ReadPRG(0x6000); got == 0xAB {
		t.Fatalf("write landed while RAM disabled")
	}
	writeBits(c, 0xE000, 0x00, &cycle)
	c.WritePRG(0x6000, 0xAB)
	if got := c.ReadPRG(0x6000); got != 0xAB {
		t.Fatalf("RAM read = %02X, want AB", got)
	}
}

func TestMMC3Banking(t *testing.T) {
	c := mustCart(t, buildImage(4, 64, 32, 0x02))
	// R6 = 5 at $8000 (mode 0), R7 = 2 at $A000.
	c.WritePRG(0x8000, 6)
	c.WritePRG(0x8001, 5)
	c.WritePRG(0x8000, 7)
	c.WritePRG(0x8001, 2)
	if got := c.ReadPRG(0x8000); got != 5 {
		t.Fatalf("$8000 bank = %d, want 5", got)
	}
	if got := c.ReadPRG(0xA000); got != 2 {
		t.Fatalf("$A000 bank = %d, want 2", got)
	}
	if got := c.ReadPRG(0xC000); got != 6 { // second to last
		t.Fatalf("$C000 bank = %d, want 6", got)
	}
	if got := c.ReadPRG(0xE000); got != 7 { // last
		t.Fatalf("$E000 bank = %d, want 7", got)
	}
	// PRG mode 1 moves R6 to $C000 and the fixed bank to $8000.
	c.WritePRG(0x8000, 0x40|6)
	if got := c.ReadPRG(0xC000); got != 5 {
		t.Fatalf("mode 1 $C000 bank = %d, want 5", got)
	}
	if got := c.ReadPRG(0x8000); got != 6 {
		t.Fatalf("mode 1 $8000 bank = %d, want 6", got)
	}
	c.WritePRG(0x8000, 6)
	// CHR: R0 = 4 is a 2 KB window at $0000-$07FF (its second
	// half reads as chunk 5), R1 = 6 covers $0800-$0FFF, and
	// R2 = 9 sits at $1000.
	c.WritePRG(0x8000, 0)
	c.WritePRG(0x8001, 4)
	c.WritePRG(0x8000, 1)
	c.WritePRG(0x8001, 6)
	c.WritePRG(0x8000, 2)
	c.WritePRG(0x8001, 9)
	if got := c.ReadCHR(0x0000); got != 4 {
		t.Fatalf("CHR $0000 bank = %d, want 4", got)
	}
	if got := c.ReadCHR(0x0400); got != 5 {
		t.Fatalf("CHR $0400 bank = %d, want 5 (R0 second half)", got)
	}
	if got := c.ReadCHR(0x0800); got != 6 {
		t.Fatalf("CHR $0800 bank = %d, want 6 (R1)", got)
	}
	if got := c.ReadCHR(0x1000); got != 9 {
		t.Fatalf("CHR $1000 bank = %d, want 9", got)
	}
	// CHR inversion swaps the window sets: R0 lands at $1000.
	c.WritePRG(0x8000, 0x80)
	if got := c.ReadCHR(0x1000); got != 4 {
		t.Fatalf("inverted CHR $1000 bank = %d, want 4", got)
	}
	if got := c.ReadCHR(0x0000); got != 9 {
		t.Fatalf("inverted CHR $0000 bank = %d, want 9 (R2)", got)
	}
	// Mirroring register: 0 vertical, 1 horizontal.
	if c.MirrorMode() != MirrorVertical {
		t.Fatalf("MMC3 mirror = %v, want vertical", c.MirrorMode())
	}
	c.WritePRG(0xA000, 1)
	if c.MirrorMode() != MirrorHorizontal {
		t.Fatalf("MMC3 mirror = %v, want horizontal", c.MirrorMode())
	}
}

func TestMMC3IRQCounter(t *testing.T) {
	c := mustCart(t, buildImage(4, 64, 32, 0))
	c.WritePRG(0xC000, 2) // latch = 2
	c.WritePRG(0xC001, 0) // reload on the next clock
	c.WritePRG(0xE001, 0) // enable
	c.ClockA12()          // reload: counter = 2
	if c.IRQPending() {
		t.Fatalf("IRQ fired on the reload clock")
	}
	c.ClockA12() // 2 to 1
	if c.IRQPending() {
		t.Fatalf("IRQ fired early")
	}
	c.ClockA12() // 1 to 0: fire
	if !c.IRQPending() {
		t.Fatalf("IRQ did not fire at zero")
	}
	c.WritePRG(0xE000, 0) // disable acknowledges
	if c.IRQPending() {
		t.Fatalf("IRQ still pending after disable")
	}
}

func TestMMC3RAMProtectRegisters(t *testing.T) {
	c := mustCart(t, buildImage(4, 64, 32, 0x02))
	c.WritePRG(0x6000, 0x5A)
	if got := c.ReadPRG(0x6000); got != 0x5A {
		t.Fatalf("MMC3 RAM read = %02X, want 5A", got)
	}
	c.WritePRG(0xA001, 0x00) // RAM enable bit clear
	c.WritePRG(0x6000, 0x11)
	c.WritePRG(0xA001, 0x80) // enabled again
	if got := c.ReadPRG(0x6000); got != 0x5A {
		t.Fatalf("MMC3 RAM = %02X after disable window, want 5A", got)
	}
}

func TestCartSerializeRoundTrip(t *testing.T) {
	c := mustCart(t, buildImage(4, 64, 32, 0x02))
	c.WritePRG(0x8000, 6)
	c.WritePRG(0x8001, 3)
	c.WritePRG(0x6000, 0x77)
	state := c.Serialize()
	c2 := mustCart(t, buildImage(4, 64, 32, 0x02))
	if err := c2.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	if got := c2.ReadPRG(0x8000); got != 3 {
		t.Fatalf("restored $8000 bank = %d, want 3", got)
	}
	if got := c2.ReadPRG(0x6000); got != 0x77 {
		t.Fatalf("restored RAM = %02X, want 77", got)
	}
	if err := c2.Deserialize([]byte{9, 9, 9}); err == nil {
		t.Fatalf("bad state accepted")
	}
}
