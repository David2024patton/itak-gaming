package periph

// StateSize is the serialized size of the whole peripheral
// set. The generated firmware image is not serialized: it is
// rebuilt identically by New.
const StateSize = 24 + 96 + 112 + 112 + 148 + 28 + 524 + 12 + 12 + 4

// Serialize writes the device state into dst (at least
// StateSize bytes) and returns the byte count.
func (s *Set) Serialize(dst []byte) int {
	w := &stateWriter{b: dst}
	for i := 0; i < 2; i++ {
		w.u32(s.irq[i].ime)
		w.u32(s.irq[i].ie)
		w.u32(s.irq[i].iflags)
	}
	for side := 0; side < 2; side++ {
		for t := 0; t < 4; t++ {
			tm := &s.tim[side][t]
			w.u16(tm.reload)
			w.u16(tm.ctrl)
			w.u16(tm.counter)
			w.u32(tm.sub)
			w.u16(0)
		}
	}
	for i := 0; i < 4; i++ {
		ch := &s.dm9[i]
		w.u32(ch.sad)
		w.u32(ch.dad)
		w.u32(ch.cnt)
		w.u32(ch.src)
		w.u32(ch.dst)
		w.u32(ch.remain)
		w.flag(ch.armed)
		w.bytes([]byte{0, 0, 0})
	}
	for i := 0; i < 4; i++ {
		ch := &s.dm7[i]
		w.u32(ch.sad)
		w.u32(ch.dad)
		w.u16(ch.cntL)
		w.u16(ch.cntH)
		w.u32(ch.src)
		w.u32(ch.dst)
		w.u32(ch.remain)
		w.flag(ch.armed)
		w.bytes([]byte{0, 0, 0})
	}
	for i := 0; i < 2; i++ {
		w.u8(s.ipc.syncOut[i])
	}
	for i := 0; i < 2; i++ {
		w.flag(s.ipc.syncIRQEn[i])
	}
	for i := 0; i < 2; i++ {
		w.flag(s.ipc.cntEn[i])
	}
	for i := 0; i < 2; i++ {
		w.flag(s.ipc.sendEmptyIE[i])
	}
	for i := 0; i < 2; i++ {
		w.flag(s.ipc.recvNotEmptyIE[i])
	}
	for i := 0; i < 2; i++ {
		w.flag(s.ipc.err[i])
	}
	for d := 0; d < 2; d++ {
		for k := 0; k < 16; k++ {
			w.u32(s.ipc.fifo[d][k])
		}
		w.u8(uint8(s.ipc.head[d]))
		w.u8(uint8(s.ipc.count[d]))
		w.bytes([]byte{0, 0})
	}
	w.bytes(s.crd.cmd[:])
	w.u32(s.crd.romctrl)
	w.u32(s.crd.srcOff)
	w.u32(s.crd.srcLen)
	w.u32(uint32(s.crd.pos))
	w.u8(s.crd.kind)
	w.flag(s.crd.active)
	w.bytes([]byte{0, 0})
	w.u16(s.aux.cnt)
	w.u8(s.aux.state)
	w.flag(s.aux.wel)
	w.u32(s.aux.addr)
	w.flag(s.aux.wr)
	w.u8(uint8(s.aux.hi))
	w.bytes([]byte{0, 0})
	w.bytes(s.aux.save[:])
	w.u16(s.spi.cnt)
	w.u8(s.spi.resp)
	w.u8(s.spi.fwPh)
	w.u32(s.spi.fwAdr)
	w.flag(s.spi.fwFast)
	w.u8(s.spi.tscPh)
	w.u16(s.spi.tscADC)
	w.u64(s.rtc.frames)
	w.u8(s.rtc.idx)
	w.bytes([]byte{0, 0, 0})
	w.flag(s.penDn)
	w.bytes([]byte{0, 0, 0})
	return w.n
}

// Deserialize restores the device state from src and returns
// the byte count consumed.
func (s *Set) Deserialize(src []byte) int {
	r := &stateReader{b: src}
	for i := 0; i < 2; i++ {
		s.irq[i].ime = r.u32()
		s.irq[i].ie = r.u32()
		s.irq[i].iflags = r.u32()
	}
	for side := 0; side < 2; side++ {
		for t := 0; t < 4; t++ {
			tm := &s.tim[side][t]
			tm.reload = r.u16()
			tm.ctrl = r.u16()
			tm.counter = r.u16()
			tm.sub = r.u32()
			r.u16()
		}
	}
	for i := 0; i < 4; i++ {
		ch := &s.dm9[i]
		ch.sad = r.u32()
		ch.dad = r.u32()
		ch.cnt = r.u32()
		ch.src = r.u32()
		ch.dst = r.u32()
		ch.remain = r.u32()
		ch.armed = r.flag()
		r.bytes(make([]byte, 3))
	}
	for i := 0; i < 4; i++ {
		ch := &s.dm7[i]
		ch.sad = r.u32()
		ch.dad = r.u32()
		ch.cntL = r.u16()
		ch.cntH = r.u16()
		ch.src = r.u32()
		ch.dst = r.u32()
		ch.remain = r.u32()
		ch.armed = r.flag()
		r.bytes(make([]byte, 3))
	}
	for i := 0; i < 2; i++ {
		s.ipc.syncOut[i] = r.u8()
	}
	for i := 0; i < 2; i++ {
		s.ipc.syncIRQEn[i] = r.flag()
	}
	for i := 0; i < 2; i++ {
		s.ipc.cntEn[i] = r.flag()
	}
	for i := 0; i < 2; i++ {
		s.ipc.sendEmptyIE[i] = r.flag()
	}
	for i := 0; i < 2; i++ {
		s.ipc.recvNotEmptyIE[i] = r.flag()
	}
	for i := 0; i < 2; i++ {
		s.ipc.err[i] = r.flag()
	}
	for d := 0; d < 2; d++ {
		for k := 0; k < 16; k++ {
			s.ipc.fifo[d][k] = r.u32()
		}
		s.ipc.head[d] = int(r.u8())
		s.ipc.count[d] = int(r.u8())
		r.bytes(make([]byte, 2))
	}
	r.bytes(s.crd.cmd[:])
	s.crd.romctrl = r.u32()
	s.crd.srcOff = r.u32()
	s.crd.srcLen = r.u32()
	s.crd.pos = int(r.u32())
	s.crd.kind = r.u8()
	s.crd.active = r.flag()
	r.bytes(make([]byte, 2))
	// The prepared block's bytes are re-derived from the ROM;
	// the machine contract requires the same ROM loaded.
	s.crd.rebuild(s.rom())
	s.aux.cnt = r.u16()
	s.aux.state = r.u8()
	s.aux.wel = r.flag()
	s.aux.addr = r.u32()
	s.aux.wr = r.flag()
	s.aux.hi = uint32(r.u8())
	r.bytes(make([]byte, 2))
	r.bytes(s.aux.save[:])
	s.spi.cnt = r.u16()
	s.spi.resp = r.u8()
	s.spi.fwPh = r.u8()
	s.spi.fwAdr = r.u32()
	s.spi.fwFast = r.flag()
	s.spi.tscPh = r.u8()
	s.spi.tscADC = r.u16()
	s.rtc.frames = r.u64()
	s.rtc.idx = r.u8()
	r.bytes(make([]byte, 3))
	s.penDn = r.flag()
	r.bytes(make([]byte, 3))
	return r.n
}
