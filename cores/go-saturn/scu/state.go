package scu

import (
	"encoding/binary"
	"fmt"
)

// Extra save state: everything the SCU keeps off the
// register page. The page itself is part of the main
// machine state. All integers are big endian.
//
// Layout:
//
//	0..1023      DSP program RAM (256 words)
//	1024..2047   DSP data RAM (4 banks of 64 words)
//	2048..2111   DSP register block:
//	               +0 RX, +4 RY, +8 P (64 bit),
//	               +16 A (64 bit), +24 RA0, +28 WA0,
//	               +32 TN0, +36 CT0..CT3 (one byte
//	               each), +40 PC, +41 TOP, +42 LOP
//	               (16 bit), +44 flags (bit 0 S, 1 Z,
//	               2 C, 3 V, 4 T0, 5 end), +45 running,
//	               +46 last EX, +47 repeat address,
//	               +48 repeat active, +49 LPS pending,
//	               +50 data port pointer, +52 program
//	               DMA pointer (32 bit)
//	2112..2127   DSP port sync block: +0 last program
//	               port word, +4 program port valid,
//	               +8 last data address port word,
//	               +12 last data data port word
//	2128..2143   DMA block: +0 latched start factors,
//	               +1 last enabled mask (bit per
//	               channel), +4 illegal DMA count
//	2144..2159   timer block: +0 line dots, +4 timer 1
//	               counter, +8 last ticked line
const ExtraSize = 2160

// Block offsets within the extra state.
const (
	extraProg    = 0
	extraData    = 1024
	extraDSPRegs = 2048
	extraPorts   = 2112
	extraDMA     = 2128
	extraTimers  = 2144
)

// SerializeExtra returns the off page SCU state as
// exactly ExtraSize bytes.
func (s *SCU) SerializeExtra() []byte {
	out := make([]byte, ExtraSize)
	d := &s.dsp
	for i, v := range d.prog {
		binary.BigEndian.PutUint32(out[extraProg+i*4:], v)
	}
	for b := 0; b < 4; b++ {
		for i, v := range d.data[b] {
			binary.BigEndian.PutUint32(out[extraData+(b*64+i)*4:], v)
		}
	}
	r := extraDSPRegs
	binary.BigEndian.PutUint32(out[r:], d.rx)
	binary.BigEndian.PutUint32(out[r+4:], d.ry)
	binary.BigEndian.PutUint64(out[r+8:], d.p)
	binary.BigEndian.PutUint64(out[r+16:], d.acc)
	binary.BigEndian.PutUint32(out[r+24:], d.ra0)
	binary.BigEndian.PutUint32(out[r+28:], d.wa0)
	binary.BigEndian.PutUint32(out[r+32:], d.tn0)
	for i, v := range d.ct {
		out[r+36+i] = v
	}
	out[r+40] = d.pc
	out[r+41] = d.top
	binary.BigEndian.PutUint16(out[r+42:], d.lop)
	var flags byte
	if d.flagS {
		flags |= 1 << 0
	}
	if d.flagZ {
		flags |= 1 << 1
	}
	if d.flagC {
		flags |= 1 << 2
	}
	if d.flagV {
		flags |= 1 << 3
	}
	if d.flagT0 {
		flags |= 1 << 4
	}
	if d.endFlag {
		flags |= 1 << 5
	}
	out[r+44] = flags
	if d.running {
		out[r+45] = 1
	}
	if d.lastEX {
		out[r+46] = 1
	}
	out[r+47] = d.repeatAddr
	if d.repeatActive {
		out[r+48] = 1
	}
	if d.lpsPending {
		out[r+49] = 1
	}
	out[r+50] = d.dataPtr
	binary.BigEndian.PutUint32(out[r+52:], d.dmaProgAddr)

	p := extraPorts
	binary.BigEndian.PutUint32(out[p:], s.lastProgPort)
	if s.progPortValid {
		out[p+4] = 1
	}
	binary.BigEndian.PutUint32(out[p+8:], s.lastDataAddr)
	binary.BigEndian.PutUint32(out[p+12:], s.lastDataData)

	m := extraDMA
	out[m] = s.latchedFactors
	var enMask byte
	for ch := 0; ch < 3; ch++ {
		if s.lastEnabled[ch] {
			enMask |= 1 << uint(ch)
		}
	}
	out[m+1] = enMask
	binary.BigEndian.PutUint32(out[m+4:], s.illegalDMA)

	t := extraTimers
	binary.BigEndian.PutUint32(out[t:], uint32(int32(s.lineDots)))
	binary.BigEndian.PutUint32(out[t+4:], uint32(int32(s.t1Counter)))
	binary.BigEndian.PutUint32(out[t+8:], uint32(int32(s.lastLine)))
	return out
}

// DeserializeExtra restores state produced by
// SerializeExtra. The input must be exactly ExtraSize
// bytes.
func (s *SCU) DeserializeExtra(data []byte) error {
	if len(data) != ExtraSize {
		return fmt.Errorf("scu: extra state size %d, want %d", len(data), ExtraSize)
	}
	d := &s.dsp
	for i := range d.prog {
		d.prog[i] = binary.BigEndian.Uint32(data[extraProg+i*4:])
	}
	for b := 0; b < 4; b++ {
		for i := range d.data[b] {
			d.data[b][i] = binary.BigEndian.Uint32(data[extraData+(b*64+i)*4:])
		}
	}
	r := extraDSPRegs
	d.rx = binary.BigEndian.Uint32(data[r:])
	d.ry = binary.BigEndian.Uint32(data[r+4:])
	d.p = binary.BigEndian.Uint64(data[r+8:])
	d.acc = binary.BigEndian.Uint64(data[r+16:])
	d.ra0 = binary.BigEndian.Uint32(data[r+24:])
	d.wa0 = binary.BigEndian.Uint32(data[r+28:])
	d.tn0 = binary.BigEndian.Uint32(data[r+32:])
	for i := range d.ct {
		d.ct[i] = data[r+36+i]
	}
	d.pc = data[r+40]
	d.top = data[r+41]
	d.lop = binary.BigEndian.Uint16(data[r+42:])
	flags := data[r+44]
	d.flagS = flags&(1<<0) != 0
	d.flagZ = flags&(1<<1) != 0
	d.flagC = flags&(1<<2) != 0
	d.flagV = flags&(1<<3) != 0
	d.flagT0 = flags&(1<<4) != 0
	d.endFlag = flags&(1<<5) != 0
	d.running = data[r+45] != 0
	d.lastEX = data[r+46] != 0
	d.repeatAddr = data[r+47]
	d.repeatActive = data[r+48] != 0
	d.lpsPending = data[r+49] != 0
	d.dataPtr = data[r+50]
	d.dmaProgAddr = binary.BigEndian.Uint32(data[r+52:])

	p := extraPorts
	s.lastProgPort = binary.BigEndian.Uint32(data[p:])
	s.progPortValid = data[p+4] != 0
	s.lastDataAddr = binary.BigEndian.Uint32(data[p+8:])
	s.lastDataData = binary.BigEndian.Uint32(data[p+12:])

	m := extraDMA
	s.latchedFactors = data[m]
	for ch := 0; ch < 3; ch++ {
		s.lastEnabled[ch] = data[m+1]&(1<<uint(ch)) != 0
	}
	s.illegalDMA = binary.BigEndian.Uint32(data[m+4:])

	t := extraTimers
	s.lineDots = int(int32(binary.BigEndian.Uint32(data[t:])))
	s.t1Counter = int(int32(binary.BigEndian.Uint32(data[t+4:])))
	s.lastLine = int(int32(binary.BigEndian.Uint32(data[t+8:])))
	return nil
}
