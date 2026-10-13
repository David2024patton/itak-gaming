// Package rsp implements the Nintendo 64 Reality Signal
// Processor: the scalar MIPS I core, the SP register set
// and DMA engine, and the vector unit (COP2). It is
// original code written from the published RSP
// documentation (the SGI/Nintendo RSP Programmer's Guide,
// used here as a factual reference for register layouts,
// instruction encodings, and operation semantics).
//
// The RSP owns its DMEM (4 KB) and IMEM (4 KB). The main
// CPU sees both through the SP memory window on the bus,
// and the SP registers through the SP register page. DMA
// between RDRAM and SP memory executes atomically when a
// length register is written, so the DMA busy and full
// flags always read clear; that simplification is stated
// in the README.
//
// The scalar core implements the documented MIPS I subset
// with no exceptions: overflow does not trap, illegal
// encodings execute as NOPs, and BREAK is the only stop
// condition besides the halt flag. Data accesses use the
// low 12 bits of the effective address into DMEM, exactly
// as the documented instruction pseudocode specifies.
package rsp

import "encoding/binary"

// CP0 register numbers on the RSP (also the CPU-visible
// SP register page order at 0x04040000).
const (
	CP0MemAddr   = 0 // SP_MEM_ADDR: bit 12 selects IMEM, bits 11..0 offset
	CP0DramAddr  = 1 // SP_DRAM_ADDR: 24-bit RDRAM address
	CP0RdLen     = 2 // SP_RD_LEN: write starts a DRAM to SP transfer
	CP0WrLen     = 3 // SP_WR_LEN: write starts an SP to DRAM transfer
	CP0Status    = 4 // SP_STATUS
	CP0DMAFull   = 5 // SP_DMA_FULL (read-only)
	CP0DMABusy   = 6 // SP_DMA_BUSY (read-only)
	CP0Semaphore = 7 // SP_SEMAPHORE
	// 8..15 are the DP registers, implemented in N64-3;
	// they read as 0 and ignore writes in this slice.
)

// SP_STATUS read bits.
const (
	StatusHalt        = 1 << 0
	StatusBroke       = 1 << 1
	StatusDMABusy     = 1 << 2
	StatusDMAFull     = 1 << 3
	StatusIOFull      = 1 << 4
	StatusSingleStep  = 1 << 5
	StatusIntrOnBreak = 1 << 6
	// Bits 7..14 are signals 0..7.
)

// RSP is the signal processor state.
type RSP struct {
	Reg [32]uint32
	HI  uint32
	LO  uint32
	PC  uint32 // IMEM address of the next instruction (12 bits)
	NPC uint32 // IMEM address after PC (12 bits)

	DMEM [4096]byte
	IMEM [4096]byte

	V   [32][8]uint16 // vector registers, element 0 first (big-endian order)
	ACC [8]int64      // 48-bit accumulator lanes, kept sign-extended
	VCC uint16        // compare code: low byte results, high byte clip codes
	VCO uint16        // low byte carry/borrow, high byte not-equal
	VCE uint8         // clip extend flags

	halted      bool
	broke       bool
	sstep       bool
	intrOnBreak bool
	signals     uint8
	intr        bool // MI SP interrupt line level driven by this chip

	memAddr   uint32 // SP_MEM_ADDR raw (bit 12 bank, 11..0 offset)
	dramAddr  uint32 // SP_DRAM_ADDR raw (24 bits)
	rdLen     uint32 // last value written to SP_RD_LEN
	wrLen     uint32 // last value written to SP_WR_LEN
	semaphore uint32 // 0 or 1

	divIn  int32 // latched reciprocal input (high half set by the H forms)
	divOut int32 // latched reciprocal result (read by the H forms)

	dram []byte // attached RDRAM, nil until AttachDRAM

	// OnInterrupt, when set, is called when the RSP
	// interrupt output changes level. The bus wires it to
	// the MI SP interrupt bit.
	OnInterrupt func(active bool)
}

// New returns an RSP in its power-on state: halted, PC 0,
// SP_STATUS reads 0x0001.
func New() *RSP {
	return &RSP{halted: true}
}

// AttachDRAM gives the DMA engine its RDRAM backing store.
func (r *RSP) AttachDRAM(dram []byte) { r.dram = dram }

// Halted reports whether the scalar core is stopped.
func (r *RSP) Halted() bool { return r.halted }

// Status returns the SP_STATUS register value.
func (r *RSP) Status() uint32 {
	var s uint32
	if r.halted {
		s |= StatusHalt
	}
	if r.broke {
		s |= StatusBroke
	}
	// DMA busy, DMA full, and IO full always read 0: DMA
	// completes atomically on the length-register write.
	if r.sstep {
		s |= StatusSingleStep
	}
	if r.intrOnBreak {
		s |= StatusIntrOnBreak
	}
	s |= uint32(r.signals) << 7
	return s
}

// setInterrupt drives the MI SP interrupt output.
func (r *RSP) setInterrupt(on bool) {
	if r.intr == on {
		return
	}
	r.intr = on
	if r.OnInterrupt != nil {
		r.OnInterrupt(on)
	}
}

// writeStatus applies the documented SP_STATUS write-bit
// semantics (used by both the CPU register page and the
// RSP's own MTC0): each function has a clear bit and a
// set bit; a write with neither changes nothing.
func (r *RSP) writeStatus(v uint32) {
	if v&0x001 != 0 { // clear halt
		r.halted = false
	}
	if v&0x002 != 0 { // set halt
		r.halted = true
	}
	if v&0x004 != 0 { // clear broke
		r.broke = false
	}
	if v&0x008 != 0 { // clear RSP interrupt
		r.setInterrupt(false)
	}
	if v&0x010 != 0 { // set RSP interrupt
		r.setInterrupt(true)
	}
	if v&0x020 != 0 { // clear single step
		r.sstep = false
	}
	if v&0x040 != 0 { // set single step
		r.sstep = true
	}
	if v&0x080 != 0 { // clear interrupt-on-break enable
		r.intrOnBreak = false
	}
	if v&0x100 != 0 { // set interrupt-on-break enable
		r.intrOnBreak = true
	}
	for i := 0; i < 8; i++ {
		if v&(1<<uint(9+2*i)) != 0 { // clear signal i
			r.signals &^= 1 << uint(i)
		}
		if v&(1<<uint(10+2*i)) != 0 { // set signal i
			r.signals |= 1 << uint(i)
		}
	}
}

// ReadCP0 reads an RSP coprocessor 0 register (the SP
// register set). Reading the semaphore performs the
// documented test-and-set.
func (r *RSP) ReadCP0(reg int) uint32 {
	switch reg {
	case CP0MemAddr:
		return r.memAddr
	case CP0DramAddr:
		return r.dramAddr
	case CP0RdLen:
		return r.rdLen
	case CP0WrLen:
		return r.wrLen
	case CP0Status:
		return r.Status()
	case CP0DMAFull, CP0DMABusy:
		return 0 // transfers complete atomically in this model
	case CP0Semaphore:
		v := r.semaphore
		r.semaphore = 1
		return v
	}
	return 0 // DP registers: N64-3
}

// WriteCP0 writes an RSP coprocessor 0 register. Writing
// a length register starts the DMA transfer immediately.
// Writing the semaphore clears it (the documented
// release operation writes 0).
func (r *RSP) WriteCP0(reg int, val uint32) {
	switch reg {
	case CP0MemAddr:
		r.memAddr = val & 0x1FFF
	case CP0DramAddr:
		r.dramAddr = val & 0xFFFFFF
	case CP0RdLen:
		r.rdLen = val
		r.dma(true, val)
	case CP0WrLen:
		r.wrLen = val
		r.dma(false, val)
	case CP0Status:
		r.writeStatus(val)
	case CP0DMAFull, CP0DMABusy:
		// Read-only status.
	case CP0Semaphore:
		r.semaphore = 0
	}
	// DP registers ignore writes in this slice.
}

// ReadRegister reads a CPU-visible SP register by byte
// offset from 0x04040000 (0x00..0x1C).
func (r *RSP) ReadRegister(off uint32) uint32 {
	return r.ReadCP0(int(off / 4))
}

// WriteRegister writes a CPU-visible SP register by byte
// offset from 0x04040000 (0x00..0x1C).
func (r *RSP) WriteRegister(off uint32, val uint32) {
	r.WriteCP0(int(off/4), val)
}

// ReadPC reads the SP_PC register for the CPU.
func (r *RSP) ReadPC() uint32 { return r.PC }

// WritePC writes the SP_PC register. Per the
// documentation the write takes effect only while the
// RSP is halted.
func (r *RSP) WritePC(val uint32) {
	if !r.halted {
		return
	}
	r.PC = val & 0xFFF
	r.NPC = (r.PC + 4) & 0xFFF
}

// decodeDMAFields splits a DMA length register value
// into the row size in bytes, the row count, and the
// per-row DRAM skip in bytes. The length field holds
// bytes-per-row minus 1 with the low 3 bits ignored (as
// if set), the count field holds rows minus 1.
func decodeDMAFields(raw uint32) (rowBytes, rows, skip int) {
	rowBytes = int(raw&0xFF8) + 8
	rows = int((raw>>12)&0xFF) + 1
	skip = int((raw >> 20) & 0xFFF)
	return
}

// dma performs one SP DMA transfer atomically. DRAM to SP
// when toSP is true (SP_RD_LEN write), SP to DRAM when
// false (SP_WR_LEN write). The SP address advances by the
// row length and wraps inside the selected 4 KB bank; the
// DRAM address advances by length plus skip per row and
// wraps in 24 bits. Both address registers are left
// holding their advanced values.
func (r *RSP) dma(toSP bool, lenRaw uint32) {
	rowBytes, rows, skip := decodeDMAFields(lenRaw)
	bank := r.memAddr & 0x1000
	off := r.memAddr & 0xFFF
	daddr := r.dramAddr
	for row := 0; row < rows; row++ {
		for i := 0; i < rowBytes; i++ {
			spIdx := bank | ((off + uint32(i)) & 0xFFF)
			dIdx := (daddr + uint32(i)) & 0xFFFFFF
			if toSP {
				r.setSPByte(spIdx, r.dramByte(dIdx))
			} else {
				r.setDRAMByte(dIdx, r.spByte(spIdx))
			}
		}
		off = (off + uint32(rowBytes)) & 0xFFF
		daddr = (daddr + uint32(rowBytes) + uint32(skip)) & 0xFFFFFF
	}
	r.memAddr = bank | off
	r.dramAddr = daddr
}

// spByte reads SP memory at a 13-bit index (bit 12
// selects IMEM over DMEM).
func (r *RSP) spByte(idx uint32) byte {
	if idx&0x1000 != 0 {
		return r.IMEM[idx&0xFFF]
	}
	return r.DMEM[idx&0xFFF]
}

// setSPByte writes SP memory at a 13-bit index.
func (r *RSP) setSPByte(idx uint32, v byte) {
	if idx&0x1000 != 0 {
		r.IMEM[idx&0xFFF] = v
	} else {
		r.DMEM[idx&0xFFF] = v
	}
}

// dramByte reads attached RDRAM; with no DRAM attached it
// reads 0.
func (r *RSP) dramByte(idx uint32) byte {
	if len(r.dram) == 0 {
		return 0
	}
	return r.dram[idx&(uint32(len(r.dram))-1)]
}

// setDRAMByte writes attached RDRAM.
func (r *RSP) setDRAMByte(idx uint32, v byte) {
	if len(r.dram) == 0 {
		return
	}
	r.dram[idx&(uint32(len(r.dram))-1)] = v
}

// dmemByte reads DMEM with 12-bit wrap; scalar and vector
// accesses are DMEM-only per the documented pseudocode.
func (r *RSP) dmemByte(addr uint32) byte { return r.DMEM[addr&0xFFF] }

// setDMEMByte writes DMEM with 12-bit wrap.
func (r *RSP) setDMEMByte(addr uint32, v byte) { r.DMEM[addr&0xFFF] = v }

// Run executes up to n instructions, stopping early at a
// halt. It returns the number of instructions executed.
func (r *RSP) Run(n int) int {
	done := 0
	for done < n && !r.halted {
		r.Step()
		done++
	}
	return done
}

// SerializeSize is the fixed byte size of a serialized
// RSP.
const SerializeSize = 32*4 + 4 + 4 + 4 + 4 + // GPRs, HI, LO, PC, NPC
	4096 + 4096 + // DMEM, IMEM
	32*8*2 + // vector registers
	8*8 + // accumulator
	2 + 2 + 1 + 8 + // VCC, VCO, VCE, state flags
	4 + 4 + 4 + 4 + 4 + // semaphore, mem addr, dram addr, rd len, wr len
	4 + 4 // reciprocal latch state

// Serialize writes the full RSP state into dst, which
// must be at least SerializeSize bytes, and returns the
// number of bytes written.
func (r *RSP) Serialize(dst []byte) int {
	w := &stateWriter{b: dst}
	for _, v := range r.Reg {
		w.u32(v)
	}
	w.u32(r.HI)
	w.u32(r.LO)
	w.u32(r.PC)
	w.u32(r.NPC)
	w.bytes(r.DMEM[:])
	w.bytes(r.IMEM[:])
	for i := 0; i < 32; i++ {
		for j := 0; j < 8; j++ {
			w.u16(r.V[i][j])
		}
	}
	for _, a := range r.ACC {
		w.u64(uint64(a))
	}
	w.u16(r.VCC)
	w.u16(r.VCO)
	w.u8(r.VCE)
	var flags uint64
	if r.halted {
		flags |= 1 << 0
	}
	if r.broke {
		flags |= 1 << 1
	}
	if r.sstep {
		flags |= 1 << 2
	}
	if r.intrOnBreak {
		flags |= 1 << 3
	}
	if r.intr {
		flags |= 1 << 4
	}
	flags |= uint64(r.signals) << 8
	w.u64(flags)
	w.u32(r.semaphore)
	w.u32(r.memAddr)
	w.u32(r.dramAddr)
	w.u32(r.rdLen)
	w.u32(r.wrLen)
	w.u32(uint32(r.divIn))
	w.u32(uint32(r.divOut))
	return w.n
}

// Deserialize restores a state written by Serialize.
func (r *RSP) Deserialize(in []byte) error {
	if len(in) < SerializeSize {
		return errShortState
	}
	rd := &stateReader{b: in}
	for i := range r.Reg {
		r.Reg[i] = rd.u32()
	}
	r.HI = rd.u32()
	r.LO = rd.u32()
	r.PC = rd.u32()
	r.NPC = rd.u32()
	copy(r.DMEM[:], rd.bytes(4096))
	copy(r.IMEM[:], rd.bytes(4096))
	for i := 0; i < 32; i++ {
		for j := 0; j < 8; j++ {
			r.V[i][j] = rd.u16()
		}
	}
	for i := range r.ACC {
		r.ACC[i] = acc48(int64(rd.u64()))
	}
	r.VCC = rd.u16()
	r.VCO = rd.u16()
	r.VCE = rd.u8()
	flags := rd.u64()
	r.halted = flags&(1<<0) != 0
	r.broke = flags&(1<<1) != 0
	r.sstep = flags&(1<<2) != 0
	r.intrOnBreak = flags&(1<<3) != 0
	r.intr = flags&(1<<4) != 0
	r.signals = uint8(flags >> 8)
	r.semaphore = rd.u32()
	r.memAddr = rd.u32()
	r.dramAddr = rd.u32()
	r.rdLen = rd.u32()
	r.wrLen = rd.u32()
	r.divIn = int32(rd.u32())
	r.divOut = int32(rd.u32())
	return nil
}

// acc48 masks and sign-extends a value to 48 bits, the
// accumulator storage invariant.
func acc48(v int64) int64 {
	v &= (1 << 48) - 1
	if v&(1<<47) != 0 {
		v |= ^int64(0) << 48
	}
	return v
}

var errShortState = errShort{}

type errShort struct{}

func (errShort) Error() string { return "rsp: state too short" }

// stateWriter appends fixed-width big-endian values.
type stateWriter struct {
	b []byte
	n int
}

func (w *stateWriter) u8(v uint8)   { w.b[w.n] = v; w.n++ }
func (w *stateWriter) u16(v uint16) { binary.BigEndian.PutUint16(w.b[w.n:], v); w.n += 2 }
func (w *stateWriter) u32(v uint32) { binary.BigEndian.PutUint32(w.b[w.n:], v); w.n += 4 }
func (w *stateWriter) u64(v uint64) { binary.BigEndian.PutUint64(w.b[w.n:], v); w.n += 8 }
func (w *stateWriter) bytes(p []byte) {
	copy(w.b[w.n:], p)
	w.n += len(p)
}

// stateReader reads fixed-width big-endian values.
type stateReader struct {
	b []byte
	n int
}

func (s *stateReader) u8() uint8   { v := s.b[s.n]; s.n++; return v }
func (s *stateReader) u16() uint16 { v := binary.BigEndian.Uint16(s.b[s.n:]); s.n += 2; return v }
func (s *stateReader) u32() uint32 { v := binary.BigEndian.Uint32(s.b[s.n:]); s.n += 4; return v }
func (s *stateReader) u64() uint64 { v := binary.BigEndian.Uint64(s.b[s.n:]); s.n += 8; return v }
func (s *stateReader) bytes(n int) []byte {
	v := s.b[s.n : s.n+n]
	s.n += n
	return v
}
