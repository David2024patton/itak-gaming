// Package spu is the PlayStation SPU: 512 KB sound
// RAM with its transfer port (from PS1-2), plus the
// full chip per the psx-spx SPU chapter: 24 ADPCM
// voices with pitch modulation, gaussian
// interpolation, ADSR envelopes and volume sweeps,
// the noise generator, KON/KOFF/ENDX flags, the
// sample address interrupt, CD mixing, the Schroeder
// reverb network, and the capture buffers in the
// first 4 KB of sound RAM.
//
// Register interface: ReadReg/WriteReg cover absolute
// addresses 1F801C00h..1F801DFFh as halfwords; a
// 32-bit access is two halfword accesses, low first.
// The DAx block follows the psx-spx documented map
// (SPU Memory Access, SPU Interrupt, SPU Control and
// SPU Reverb Registers sections): 1F801DA0h unused,
// 1F801DA2h ESA (reverb work area start), 1F801DA4h
// IRQA (IRQ address), 1F801DA6h TSA (transfer start
// address), 1F801DA8h DATA (data port FIFO),
// 1F801DAAh ATTR/SPUCNT, 1F801DACh RAM transfer
// control, 1F801DAEh STATX. ESA, IRQA and TSA are
// all in 8-byte units. The transfer address unit
// changed in PS1-3 from the 4-byte units used in
// PS1-2 to the documented 8-byte units, so
// SetAddress(v) loads the internal byte address
// v<<3 (AddrReg still stores the raw written value).
//
// KON delay simplification: the document implies key
// on takes effect on a later SPU cycle but gives no
// exact count here; this core uses a fixed delay of
// konDelaySamples output samples between KON and the
// first envelope step.
//
// Capture simplification: the four capture buffers
// are written every output sample at the position
// given by the output sample counter modulo 512
// samples (voice 1 and voice 3 post-envelope mono,
// CD left and right pre-volume), exactly per the
// documented memory map. Capture IRQ and reverb IRQ
// sources are not modelled; only voice reads of the
// IRQ address raise the interrupt.
//
// Reverb simplification: the documented 39-tap
// resampling FIR on the reverb path is replaced by
// plain decimation (the reverb runs at half rate on
// every second output sample, as documented); the
// feedback equations themselves are the documented
// ones. Repeat address written-flag: a software
// write to the repeat address register sets a flag
// that stops a Loop Start header from overwriting
// it until the next KON; the document does not name
// this flag, this is the behaviour other references
// describe for the register.
//
// Serialized layout: sound RAM first, then the
// original 8-byte transfer tail (AddrReg and the
// internal byte address), then appended blocks:
// register shadow, global scalars, reverb registers,
// per-voice state, and the pending audio buffer. The
// RAM plus old tail prefix is unchanged from PS1-2.
package spu

// RAMSize is the sound RAM size in bytes.
const RAMSize = 512 * 1024

const numVoices = 24

// konDelaySamples is the fixed key on delay, in
// output samples (see package comment).
const konDelaySamples = 5

const (
	regBase = 0x1F801C00
	regTop  = 0x1F801DFF
)

// envelope phases
const (
	phaseOff = iota
	phaseAttack
	phaseDecay
	phaseSustain
	phaseRelease
)

// adpcmFilterF0 and adpcmFilterF1 are the published
// SPU/XA ADPCM predictor coefficients from the
// psx-spx CDROM XA chapter (the SPU chapter defers
// to it): f0 = 0,60,115,98,122 and f1 = 0,0,-52,-55,-60
// for filters 0..4.
var adpcmFilterF0 = [5]int32{0, 60, 115, 98, 122}
var adpcmFilterF1 = [5]int32{0, 0, -52, -55, -60}

// voice holds one channel of the SPU.
type voice struct {
	// registers
	volLRaw, volRRaw uint16
	pitch            uint16
	ssa              uint16
	adsrLo, adsrHi   uint16
	lsax             uint16

	// volume sweep state
	curVolL, curVolR     int32
	sweepCntL, sweepCntR int32
	sweepOnL, sweepOnR   bool

	// envelope state
	envLevel   int32
	envCounter int32
	phase      int
	konDelay   int
	active     bool

	// ADPCM runtime
	addr          uint32 // current block byte address
	pitchCounter  uint32
	decOld        int32
	decOlder      int32
	block         [28]int16
	blockFlags    uint8
	blockLoaded   bool
	pos           int
	hist          [4]int32 // oldest, older, old, new
	outx          int32    // latest post-envelope sample
	repeatWritten bool
}

// SPU is the whole sound chip.
type SPU struct {
	RAM     [RAMSize]byte
	AddrReg uint16 // last value written to TSA at 0x1F801DA6
	addr    uint32 // current byte address inside RAM

	// OnIRQ is called on the 0-to-1 transition of
	// the SPU interrupt condition.
	OnIRQ func()

	voices [numVoices]voice
	shadow [256]uint16 // register readback shadow, index = (addr-regBase)/2

	pmon uint32 // pitch modulation mask, bits per voice
	non  uint32 // noise mask
	eon  uint32 // reverb send mask
	endx uint32 // end reached mask

	spucnt  uint16
	ramCtrl uint16
	esa     uint16 // reverb work area start, in 8-byte units
	irqAddr uint16 // IRQ address, in 8-byte units

	cdVolL, cdVolR   int32
	extVolL, extVolR int32
	evolL, evolR     int32

	mainRawL, mainRawR uint16
	mainCurL, mainCurR int32
	mainSweepCntL      int32
	mainSweepCntR      int32
	mainSweepOnL       bool
	mainSweepOnR       bool

	rev              [32]uint16
	revBuf           uint32 // current reverb buffer byte address
	revOutL, revOutR int32
	revPhase         bool // false: run reverb on next sample

	noiseLevel int32
	noiseTimer int32

	irqFlag bool

	cycleAcc     int64 // CPU cycles not yet turned into samples
	totalSamples uint64
	audio        []int16
}

// New returns an SPU in reset state.
func New() *SPU { return &SPU{} }

// Reset clears sound RAM, all registers and runtime
// state. The OnIRQ callback is preserved.
func (s *SPU) Reset() {
	cb := s.OnIRQ
	*s = SPU{}
	s.OnIRQ = cb
}

// SetAddress writes the transfer address register
// (TSA, 1F801DA6h) and loads the internal byte
// address. The register value is in 8-byte units per
// the psx-spx SPU Memory Access section (PS1-2 used
// 4-byte units; changed in PS1-3).
func (s *SPU) SetAddress(v uint16) {
	s.AddrReg = v
	s.addr = (uint32(v) << 3) & (RAMSize - 1)
}

// Address returns the current byte address.
func (s *SPU) Address() uint32 { return s.addr }

// ReadData reads the halfword at the transfer
// address and advances it.
func (s *SPU) ReadData() uint16 {
	v := uint16(s.RAM[s.addr]) | uint16(s.RAM[s.addr+1])<<8
	s.addr = (s.addr + 2) & (RAMSize - 1)
	return v
}

// WriteData writes a halfword at the transfer
// address and advances it.
func (s *SPU) WriteData(v uint16) {
	s.RAM[s.addr] = byte(v)
	s.RAM[s.addr+1] = byte(v >> 8)
	s.addr = (s.addr + 2) & (RAMSize - 1)
}

// ReadWord performs a DMA word read (two halfword
// port accesses, low first).
func (s *SPU) ReadWord() uint32 {
	lo := s.ReadData()
	hi := s.ReadData()
	return uint32(lo) | uint32(hi)<<16
}

// WriteWord performs a DMA word write.
func (s *SPU) WriteWord(w uint32) {
	s.WriteData(uint16(w))
	s.WriteData(uint16(w >> 16))
}

// VoiceEnvelope returns the current envelope level of
// a voice, on the 0..7FFFh scale.
func (s *SPU) VoiceEnvelope(voice int) int {
	if voice < 0 || voice >= numVoices {
		return 0
	}
	l := s.voices[voice].envLevel
	if l < 0 {
		l = 0
	}
	if l > 0x7FFF {
		l = 0x7FFF
	}
	return int(l)
}

// VoicePlaying reports whether a voice is keyed on
// and has not finished its release (or an end+mute
// block).
func (s *SPU) VoicePlaying(voice int) bool {
	if voice < 0 || voice >= numVoices {
		return false
	}
	v := &s.voices[voice]
	return v.active && v.phase != phaseOff
}

func clamp16(v int32) int32 {
	if v > 0x7FFF {
		return 0x7FFF
	}
	if v < -0x8000 {
		return -0x8000
	}
	return v
}

// mul15 multiplies two signed values with the
// documented division by 8000h.
func mul15(a, b int32) int32 {
	return int32((int64(a) * int64(b)) >> 15)
}
