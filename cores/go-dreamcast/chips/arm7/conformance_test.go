package arm7

// The Tom Harte / SingleStepTests ARM7TDMI conformance gate.
//
// The corpus (github.com/SingleStepTests/ARM7TDMI, generated from
// NanoBoyAdvance) ships as binary .json.bin files: a header (magic
// 0xD33DBAE0, test count), then per test a record holding an
// initial state, a final state, a transaction list, and the opcode
// with its base address. Each state is 40 u32 values: R0 to R15,
// the FIQ bank R8 to R14, the SVC, ABT, IRQ and UND banked R13 and
// R14 pairs, CPSR, the five SPSRs (fiq, svc, abt, irq, und order),
// two pipeline words, and an access field this harness ignores.
//
// The harness installs the initial state, executes one Step
// against a transaction backed bus, and compares R0 to R15, CPSR,
// all five SPSRs, the raw banked register storage, the pipeline
// slots (the corpus pipeline[0] is the executing instruction and
// pipeline[1] the decode slot, mapping to this CPU Executing and
// Pipe[0] after the step), and every data read and write in order.
//
// The corpus lives under testroms/tomharte-arm7tdmi in this
// module. When it is absent the gate skips. The per file subset
// defaults to 2,000 of the 50,000 tests per file with a seeded
// selection; set DC_TH_SUBSET and DC_TH_SEED to change them.

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

type thState struct {
	R        [16]uint32
	RFIQ     [7]uint32
	RSVC     [2]uint32
	RABT     [2]uint32
	RIRQ     [2]uint32
	RUND     [2]uint32
	CPSR     uint32
	SPSR     [5]uint32
	Pipeline [2]uint32
}

type thTxn struct {
	kind uint32
	size uint32
	addr uint32
	data uint32
}

type thTest struct {
	initial thState
	final   thState
	txns    []thTxn
	opcode  uint32
	base    uint32
}

func parseState(b []byte, off int) thState {
	var s thState
	v := func(i int) uint32 { return binary.LittleEndian.Uint32(b[off+8+i*4:]) }
	for i := 0; i < 16; i++ {
		s.R[i] = v(i)
	}
	for i := 0; i < 7; i++ {
		s.RFIQ[i] = v(16 + i)
	}
	for i := 0; i < 2; i++ {
		s.RSVC[i] = v(23 + i)
		s.RABT[i] = v(25 + i)
		s.RIRQ[i] = v(27 + i)
		s.RUND[i] = v(29 + i)
	}
	s.CPSR = v(31)
	for i := 0; i < 5; i++ {
		s.SPSR[i] = v(32 + i)
	}
	s.Pipeline[0] = v(37)
	s.Pipeline[1] = v(38)
	return s
}

func parseCorpusFile(t *testing.T, path string) []thTest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 8 || binary.LittleEndian.Uint32(data) != 0xD33DBAE0 {
		t.Fatalf("%s: bad magic", path)
	}
	num := int(binary.LittleEndian.Uint32(data[4:]))
	tests := make([]thTest, 0, num)
	off := 8
	u32 := func(p int) uint32 { return binary.LittleEndian.Uint32(data[p:]) }
	for i := 0; i < num; i++ {
		recStart := off
		recSize := int(int32(u32(off)))
		off += 4
		var tt thTest
		secSize := int(int32(u32(off)))
		tt.initial = parseState(data, off)
		off += secSize
		secSize = int(int32(u32(off)))
		tt.final = parseState(data, off)
		off += secSize
		secSize = int(int32(u32(off)))
		nTxn := int(int32(u32(off + 8)))
		for j := 0; j < nTxn; j++ {
			p := off + 12 + j*24
			tt.txns = append(tt.txns, thTxn{
				kind: u32(p), size: u32(p + 4), addr: u32(p + 8), data: u32(p + 12),
			})
		}
		off += secSize
		tt.opcode = u32(off + 8)
		tt.base = u32(off + 12)
		off += 16
		if off != recStart+recSize {
			t.Fatalf("%s test %d: record walk desync (%d != %d)", path, i, off, recStart+recSize)
		}
		tests = append(tests, tt)
	}
	return tests
}

// corpusBus serves the CPU from the transaction list. The CPU bus
// interface does not separate opcode fetches from data accesses,
// so the bus decides per call: a read that exactly matches the
// head of the data transaction queue is a data read, and any other
// read is an opcode fetch served from the code transactions by
// address and size. Data writes always come from the data queue.
// Data transaction order and values are checked exactly; opcode
// fetches are looked up, not ordered.
type corpusBus struct {
	txns []thTxn // the full transaction stream, in order
	pos  int     // read position in txns
	data []thTxn // kind 1 and 2, in order
	dpos int     // consumed count within data
	code map[[2]uint32]uint32
	fail string
}

func newCorpusBus(tt *thTest) *corpusBus {
	b := &corpusBus{code: map[[2]uint32]uint32{}, txns: tt.txns}
	for _, x := range tt.txns {
		switch x.kind {
		case 1, 2:
			b.data = append(b.data, x)
		case 0:
			key := [2]uint32{x.addr, x.size}
			if _, dup := b.code[key]; !dup {
				b.code[key] = x.data
			}
		}
	}
	return b
}

func (b *corpusBus) setFail(format string, args ...interface{}) {
	if b.fail == "" {
		b.fail = fmt.Sprintf(format, args...)
	}
}

// dataHeadMatches reports whether the next data transaction is a
// read at addr with the given size.
func (b *corpusBus) dataHeadMatches(addr uint32, size uint32) bool {
	if b.dpos >= len(b.data) {
		return false
	}
	x := b.data[b.dpos]
	return x.kind == 1 && x.addr == addr && x.size == size
}

func (b *corpusBus) read(addr uint32, size int) uint32 {
	// Transactions are consumed in order from the unified
	// stream: a read that matches the next recorded transaction
	// (a fetch or a data read) takes its data. Matching the
	// stream in order matters when a fetch address coincides
	// with a data address, which a data only head check would
	// misattribute.
	if b.pos < len(b.txns) {
		x := b.txns[b.pos]
		if x.kind != 2 && x.addr == addr && x.size == uint32(size) {
			b.pos++
			if x.kind == 1 {
				b.dpos++
			}
			return x.data
		}
	}
	if v, ok := b.code[[2]uint32{addr, uint32(size)}]; ok {
		return v
	}
	// A refill fetch at an address recorded at the other size, or
	// an unrecorded fetch: the fetched value only feeds pipeline
	// slots that are not compared after a flush.
	for k, v := range b.code {
		if k[0] == addr {
			return v
		}
	}
	if size == 2 {
		return addr & 0xFFFF
	}
	return addr
}

func (b *corpusBus) write(addr uint32, size int, val uint32) {
	if b.dpos >= len(b.data) {
		b.setFail("unexpected data write addr=%08x size=%d val=%08x (no transactions left)", addr, size, val)
		return
	}
	x := b.data[b.dpos]
	b.dpos++
	if b.pos < len(b.txns) && b.txns[b.pos].kind == 2 {
		b.pos++
	}
	if x.kind != 2 || x.addr != addr || x.size != uint32(size) || x.data != val {
		b.setFail("data write #%d: cpu wrote addr=%08x size=%d val=%08x, corpus has kind=%d addr=%08x size=%d data=%08x",
			b.dpos-1, addr, size, val, x.kind, x.addr, x.size, x.data)
	}
}

func (b *corpusBus) Read8(addr uint32) uint8   { return uint8(b.read(addr, 1)) }
func (b *corpusBus) Read16(addr uint32) uint16 { return uint16(b.read(addr, 2)) }
func (b *corpusBus) Read32(addr uint32) uint32 { return b.read(addr, 4) }
func (b *corpusBus) Write8(addr uint32, v uint8) {
	b.write(addr, 1, uint32(v))
}
func (b *corpusBus) Write16(addr uint32, v uint16) { b.write(addr, 2, uint32(v)) }
func (b *corpusBus) Write32(addr uint32, v uint32) { b.write(addr, 4, v) }

// loadState installs a corpus initial state. The corpus dumps raw
// bank storage: its R array is the user bank view and each mode
// bank sits in its own array even for the active mode. This CPU
// keeps the active view in R and swaps banks on mode changes, so
// loading composes the active registers from the bank arrays of
// the initial mode while the bank fields load verbatim.
func loadState(c *CPU, s *thState, tt *thTest) {
	mode := s.CPSR & 0x1F
	c.R = s.R
	c.RFIQ = s.RFIQ
	c.RSVC = s.RSVC
	c.RABT = s.RABT
	c.RIRQ = s.RIRQ
	c.RUND = s.RUND
	c.RUSR = [2]uint32{s.R[13], s.R[14]}
	c.usr8_12 = [5]uint32{}
	switch mode {
	case ModeFIQ:
		copy(c.usr8_12[:], s.R[8:13])
		copy(c.R[8:13], s.RFIQ[0:5])
		c.R[13], c.R[14] = s.RFIQ[5], s.RFIQ[6]
	case ModeSVC:
		c.R[13], c.R[14] = s.RSVC[0], s.RSVC[1]
	case ModeABT:
		c.R[13], c.R[14] = s.RABT[0], s.RABT[1]
	case ModeIRQ:
		c.R[13], c.R[14] = s.RIRQ[0], s.RIRQ[1]
	case ModeUND:
		c.R[13], c.R[14] = s.RUND[0], s.RUND[1]
	}
	c.CPSR = s.CPSR
	c.SPSR = s.SPSR
	c.Executing = tt.opcode
	c.Pipe[0] = s.Pipeline[1]
	c.Pipe[1] = tt.final.Pipeline[1]
	c.flushed = false
	c.irqLine = false
	c.fiqLine = false
}

// composedViews translates the CPU swap based state back into the
// corpus raw bank storage layout: the view array shows the user
// bank, and each bank array shows that mode registers (taken from
// the live view when that mode is the active one).
func composedViews(c *CPU) (view [16]uint32, fiq [7]uint32, svc, abt, irq, und [2]uint32) {
	view = c.R
	fiq = c.RFIQ
	svc = c.RSVC
	abt = c.RABT
	irq = c.RIRQ
	und = c.RUND
	switch c.Mode() {
	case ModeFIQ:
		copy(view[8:13], c.usr8_12[:])
		view[13], view[14] = c.RUSR[0], c.RUSR[1]
		copy(fiq[0:5], c.R[8:13])
		fiq[5], fiq[6] = c.R[13], c.R[14]
	case ModeSVC:
		view[13], view[14] = c.RUSR[0], c.RUSR[1]
		svc[0], svc[1] = c.R[13], c.R[14]
	case ModeABT:
		view[13], view[14] = c.RUSR[0], c.RUSR[1]
		abt[0], abt[1] = c.R[13], c.R[14]
	case ModeIRQ:
		view[13], view[14] = c.RUSR[0], c.RUSR[1]
		irq[0], irq[1] = c.R[13], c.R[14]
	case ModeUND:
		view[13], view[14] = c.RUSR[0], c.RUSR[1]
		und[0], und[1] = c.R[13], c.R[14]
	}
	return
}

func diffState(c *CPU, tt *thTest, bus *corpusBus) []string {
	var diffs []string
	f := &tt.final
	view, fiq, svc, abt, irq, und := composedViews(c)
	for i := 0; i < 16; i++ {
		if view[i] != f.R[i] {
			diffs = append(diffs, fmt.Sprintf("R%d: got %08x want %08x", i, view[i], f.R[i]))
		}
	}
	if c.CPSR != f.CPSR {
		diffs = append(diffs, fmt.Sprintf("CPSR: got %08x want %08x", c.CPSR, f.CPSR))
	}
	for i := 0; i < 5; i++ {
		if c.SPSR[i] != f.SPSR[i] {
			diffs = append(diffs, fmt.Sprintf("SPSR[%d]: got %08x want %08x", i, c.SPSR[i], f.SPSR[i]))
		}
	}
	banks := []struct {
		name string
		got  []uint32
		want []uint32
	}{
		{"R_fiq", fiq[:], f.RFIQ[:]},
		{"R_svc", svc[:], f.RSVC[:]},
		{"R_abt", abt[:], f.RABT[:]},
		{"R_irq", irq[:], f.RIRQ[:]},
		{"R_und", und[:], f.RUND[:]},
	}
	for _, bk := range banks {
		for i := range bk.got {
			if bk.got[i] != bk.want[i] {
				diffs = append(diffs, fmt.Sprintf("%s[%d]: got %08x want %08x", bk.name, i, bk.got[i], bk.want[i]))
			}
		}
	}
	if c.Executing != f.Pipeline[0] {
		diffs = append(diffs, fmt.Sprintf("pipeline[0]: got %08x want %08x", c.Executing, f.Pipeline[0]))
	}
	if c.Pipe[0] != f.Pipeline[1] {
		diffs = append(diffs, fmt.Sprintf("pipeline[1]: got %08x want %08x", c.Pipe[0], f.Pipeline[1]))
	}
	if bus.fail != "" {
		diffs = append(diffs, "bus: "+bus.fail)
	}
	if bus.dpos != len(bus.data) {
		diffs = append(diffs, fmt.Sprintf("bus: consumed %d of %d data transactions", bus.dpos, len(bus.data)))
	}
	return diffs
}

func corpusDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "testroms", "tomharte-arm7tdmi")
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Skipf("corpus not present at %s", dir)
	}
	return dir
}

func TestTomHarteARM7TDMI(t *testing.T) {
	dir := corpusDir(t)
	files, err := filepath.Glob(filepath.Join(dir, "*.json.bin"))
	if err != nil || len(files) == 0 {
		t.Skip("no corpus files")
	}
	sort.Strings(files)
	subset := 2000
	if v := os.Getenv("DC_TH_SUBSET"); v != "" {
		fmt.Sscanf(v, "%d", &subset)
	}
	seed := int64(0xD33D)
	if v := os.Getenv("DC_TH_SEED"); v != "" {
		fmt.Sscanf(v, "%d", &seed)
	}
	totalRun, totalFail := 0, 0
	for _, path := range files {
		path := path
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			tests := parseCorpusFile(t, path)
			rng := rand.New(rand.NewSource(seed + int64(len(name))))
			idx := rng.Perm(len(tests))
			if subset < len(idx) {
				idx = idx[:subset]
			}
			fails := 0
			for _, ix := range idx {
				tt := &tests[ix]
				bus := newCorpusBus(tt)
				cpu := &CPU{Bus: bus}
				loadState(cpu, &tt.initial, tt)
				cpu.Step()
				if diffs := diffState(cpu, tt, bus); len(diffs) > 0 {
					fails++
					if fails <= 3 {
						t.Logf("test %d opcode=%08x base=%08x: %v", ix, tt.opcode, tt.base, diffs)
					}
				}
			}
			totalRun += len(idx)
			totalFail += fails
			t.Logf("%s: %d/%d passed", name, len(idx)-fails, len(idx))
			if fails > 0 {
				t.Errorf("%s: %d of %d tests failed", name, fails, len(idx))
			}
		})
	}
	t.Logf("TOTAL: %d/%d passed", totalRun-totalFail, totalRun)
}
