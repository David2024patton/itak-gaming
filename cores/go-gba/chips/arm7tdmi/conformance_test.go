package arm7tdmi

// The Tom Harte / SingleStepTests ARM7TDMI conformance gate.
//
// The corpus (github.com/SingleStepTests/ARM7TDMI, generated from
// NanoBoyAdvance) ships as binary .json.bin files: a header (magic
// 0xD33DBAE0, test count), then per test an initial state, a final
// state, a transaction list and the opcode with its base address.
// Each state is 40 u32 values: R0-R15, the FIQ bank R8-R14, the
// SVC/ABT/IRQ/UND banked R13-R14 pairs, CPSR, the five SPSRs (fiq,
// svc, abt, irq, und order), two pipeline words, and an access
// field this harness ignores.
//
// The harness installs the initial state, executes one Step
// against a transaction-backed bus, and compares:
//   - R0-R15, CPSR, all five SPSRs;
//   - the banked register storage, verbatim (the corpus dumps the
//     generator's raw bank storage, so the fields compare as
//     stored, not composed through the active mode);
//   - the pipeline as the corpus models it: its pipeline[0] is the
//     executing instruction and pipeline[1] the decode slot, which
//     map to this CPU's Executing and Pipe[0] after the step;
//   - every data read and write, in order: kind, size, address,
//     and the data returned or written.
//
// The corpus does not publish a trustworthy total cycle count
// (its per-transaction cycle field is documented upstream as
// experimental and is not asserted on here), so Step cycle counts
// follow the GBATEK model and are not corpus-compared.
//
// The corpus lives under testroms/tomharte-arm7tdmi in this
// module (gitignored, local evidence only). When it is absent the
// gate skips. Fetch it with:
//
//	git clone --depth 1 https://github.com/SingleStepTests/ARM7TDMI
//	# then copy ARM7TDMI/v1/*.json.bin into testroms/tomharte-arm7tdmi/
//
// The per-file subset size defaults to 2,000 of the 50,000 tests
// (seeded); set GBA_TH_SUBSET=50000 to run everything and
// GBA_TH_SEED to change the seed.

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
		// Section 1: initial state.
		secSize := int(int32(u32(off)))
		tt.initial = parseState(data, off)
		off += secSize
		// Section 2: final state.
		secSize = int(int32(u32(off)))
		tt.final = parseState(data, off)
		off += secSize
		// Section 3: transactions.
		secSize = int(int32(u32(off)))
		nTxn := int(int32(u32(off + 8)))
		for j := 0; j < nTxn; j++ {
			p := off + 12 + j*24
			tt.txns = append(tt.txns, thTxn{
				kind: u32(p), size: u32(p + 4), addr: u32(p + 8), data: u32(p + 12),
			})
		}
		off += secSize
		// Section 4: opcode and base address.
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

// corpusBus plays the transaction list back to the CPU.
type corpusBus struct {
	base   uint32
	opcode uint32
	data   []thTxn // kind 1 and 2, in order
	pos    int
	code   []thTxn // kind 0, in order
	codeIx map[[2]uint32]int
	fail   string
}

func newCorpusBus(tt *thTest) *corpusBus {
	b := &corpusBus{base: tt.base, opcode: tt.opcode, codeIx: map[[2]uint32]int{}}
	for _, x := range tt.txns {
		switch x.kind {
		case 1, 2:
			b.data = append(b.data, x)
		case 0:
			// The same address can be fetched at two sizes
			// (a sequential Thumb fetch and a refill word
			// fetch after a state switch); index by both.
			key := [2]uint32{x.addr, x.size}
			if _, dup := b.codeIx[key]; !dup {
				b.codeIx[key] = len(b.code)
			}
			b.code = append(b.code, x)
		}
	}
	return b
}

func (b *corpusBus) setFail(format string, args ...interface{}) {
	if b.fail == "" {
		b.fail = fmt.Sprintf(format, args...)
	}
}

func (b *corpusBus) Read(addr uint32, size int) uint32 {
	if b.pos >= len(b.data) {
		b.setFail("unexpected data read addr=%08x size=%d (no transactions left)", addr, size)
		return 0
	}
	x := b.data[b.pos]
	b.pos++
	if x.kind != 1 || x.addr != addr || x.size != uint32(size) {
		b.setFail("data read #%d: cpu read kind=%d addr=%08x size=%d, corpus has kind=%d addr=%08x size=%d",
			b.pos-1, 1, addr, size, x.kind, x.addr, x.size)
	}
	return x.data
}

func (b *corpusBus) Write(addr uint32, size int, val uint32) {
	if b.pos >= len(b.data) {
		b.setFail("unexpected data write addr=%08x size=%d val=%08x (no transactions left)", addr, size, val)
		return
	}
	x := b.data[b.pos]
	b.pos++
	if x.kind != 2 || x.addr != addr || x.size != uint32(size) || x.data != val {
		b.setFail("data write #%d: cpu wrote addr=%08x size=%d val=%08x, corpus has kind=%d addr=%08x size=%d data=%08x",
			b.pos-1, addr, size, val, x.kind, x.addr, x.size, x.data)
	}
}

func (b *corpusBus) Fetch(addr uint32, size int) uint32 {
	if addr == b.base {
		return b.opcode
	}
	if ix, ok := b.codeIx[[2]uint32{addr, uint32(size)}]; ok {
		return b.code[ix].data
	}
	// Fall back to any fetch recorded at this address.
	for _, x := range b.code {
		if x.addr == addr {
			return x.data
		}
	}
	if size == 2 {
		return addr & 0xFFFF
	}
	return addr
}

func loadState(c *CPU, s *thState, tt *thTest, bus *corpusBus) {
	// The corpus dumps raw bank storage with no swapping: its R
	// array is the user-bank view (R8-R14 as USR sees them) and
	// each mode's banked registers sit in their own arrays, even
	// for the active mode. This CPU keeps the active view in R
	// and swaps banks on mode changes, so loading composes the
	// active registers from the bank arrays of the initial mode,
	// while the bank fields themselves are loaded verbatim.
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
	for i := 0; i < 5; i++ {
		c.SPSR[i] = s.SPSR[i]
	}
	c.Executing = tt.opcode
	c.ExecutingValid = true
	c.Pipe[0] = s.Pipeline[1]
	// The fetch-stage slot holds the word the step's own code
	// transaction at the initial R15 returns; when the corpus has
	// no such transaction (control flow refills anyway), fall
	// back to the final pipeline value, which that slot feeds.
	c.Pipe[1] = tt.final.Pipeline[1]
	for _, x := range tt.txns {
		if x.kind == 0 && x.addr == s.R[15] {
			c.Pipe[1] = x.data
			break
		}
	}
	c.Halted = false
	c.flushed = false
}

// composedViews translates the CPU's swap-based state back into
// the corpus's raw bank storage layout, keyed by the final mode:
// the view array shows the user bank, and each bank array shows
// that mode's own registers (taken from the live R view when that
// mode is the active one).
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
	if bus.pos != len(bus.data) {
		diffs = append(diffs, fmt.Sprintf("bus: consumed %d of %d data transactions", bus.pos, len(bus.data)))
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
	if v := os.Getenv("GBA_TH_SUBSET"); v != "" {
		fmt.Sscanf(v, "%d", &subset)
	}
	seed := int64(0xA7D1)
	if v := os.Getenv("GBA_TH_SEED"); v != "" {
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
				loadState(cpu, &tt.initial, tt, bus)
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
