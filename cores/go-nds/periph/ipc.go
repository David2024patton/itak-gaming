package periph

// IPC implements IPCSYNC and the IPC FIFO pair. Each side has a
// 16-word send FIFO that the other side receives from. IRQ
// conditions follow the control register: send FIFO empty,
// receive FIFO not empty, plus the IPCSYNC nibble exchange.
// Overflow and underflow set the side's error flag.
type IPC struct {
	syncOut        [2]uint8
	syncIRQEn      [2]bool
	fifo           [2][16]uint32 // fifo[s] is written by side s
	head           [2]int
	count          [2]int
	cntEn          [2]bool
	sendEmptyIE    [2]bool
	recvNotEmptyIE [2]bool
	err            [2]bool
}

func sideNine(side int) bool { return side == 0 }

// read serves IPCSYNC (rel 0) and IPCFIFOCNT (rel 4).
func (p *IPC) read(side int, rel, off uint32, size int) uint32 {
	other := 1 - side
	var v uint32
	switch rel / 4 {
	case 0: // IPCSYNC
		v = uint32(p.syncOut[other]) | uint32(p.syncOut[side])<<8
		if p.syncIRQEn[side] {
			v |= 1 << 13
		}
	case 1: // IPCFIFOCNT
		if p.count[side] == 0 {
			v |= 1 << 0
		}
		if p.count[side] == 16 {
			v |= 1 << 1
		}
		if p.sendEmptyIE[side] {
			v |= 1 << 2
		}
		if p.count[other] == 0 {
			v |= 1 << 8
		}
		if p.count[other] == 16 {
			v |= 1 << 9
		}
		if p.recvNotEmptyIE[side] {
			v |= 1 << 10
		}
		if p.err[side] {
			v |= 1 << 14
		}
		if p.cntEn[side] {
			v |= 1 << 15
		}
	}
	return getU32(v, off, size)
}

// writeCnt serves writes to IPCSYNC and IPCFIFOCNT. Writing a
// new output nibble raises the other side's sync IRQ when that
// side enabled it (its IPCSYNC bit 13).
func (p *IPC) writeCnt(side int, rel, off uint32, size int, val uint32, s *Set) {
	other := 1 - side
	switch rel / 4 {
	case 0: // IPCSYNC
		if coversBit(off, size, 8) {
			merged := putU32(uint32(p.syncOut[side])<<8, off, size, val)
			newOut := uint8(merged >> 8 & 0xF)
			if newOut != p.syncOut[side] {
				p.syncOut[side] = newOut
				if p.syncIRQEn[other] {
					s.Raise(sideNine(other), IRQIPCSync)
				}
			}
		}
		if coversBit(off, size, 13) {
			p.syncIRQEn[side] = putU32(0, off, size, val)&(1<<13) != 0
		}
	case 1: // IPCFIFOCNT
		merged := putU32(0, off, size, val)
		// Only the writable bits the write actually covered
		// change; test the covered lanes via the raw value.
		p.applyCnt(side, off, size, merged)
		if coversBit(off, size, 3) && merged&(1<<3) != 0 {
			p.head[side], p.count[side] = 0, 0
			if p.sendEmptyIE[side] {
				s.Raise(sideNine(side), IRQIPCSend)
			}
		}
	}
}

// applyCnt updates the side's FIFO control enables from the
// lanes the CPU wrote.
func (p *IPC) applyCnt(side int, off uint32, size int, merged uint32) {
	if coversBit(off, size, 2) {
		p.sendEmptyIE[side] = merged&(1<<2) != 0
	}
	if coversBit(off, size, 10) {
		p.recvNotEmptyIE[side] = merged&(1<<10) != 0
	}
	if coversBit(off, size, 14) && merged&(1<<14) != 0 {
		p.err[side] = false // writing 1 clears the error flag
	}
	if coversBit(off, size, 15) {
		p.cntEn[side] = merged&(1<<15) != 0
	}
}

// coversBit reports whether a write of the given size at byte
// lane off touches register bit b.
func coversBit(off uint32, size int, b uint) bool {
	lo := uint((off & 3) * 8)
	hi := lo + uint(size*8)
	return lo <= b && b < hi
}

// send pushes one word into the side's send FIFO.
func (p *IPC) send(side int, v uint32, s *Set) {
	if !p.cntEn[side] {
		return
	}
	if p.count[side] == 16 {
		p.err[side] = true
		return
	}
	p.fifo[side][(p.head[side]+p.count[side])%16] = v
	p.count[side]++
	other := 1 - side
	if p.recvNotEmptyIE[other] {
		s.Raise(sideNine(other), IRQIPCRecv)
	}
}

// recv pops one word from the other side's send FIFO. Reading
// an empty FIFO sets the error flag and returns 0 (hardware
// returns the last word; 0 is our documented simplification).
// Draining the sender's FIFO raises the sender's send-empty
// IRQ when the sender enabled it.
func (p *IPC) recv(side int, s *Set) uint32 {
	other := 1 - side
	if p.count[other] == 0 {
		p.err[side] = true
		return 0
	}
	v := p.fifo[other][p.head[other]]
	p.head[other] = (p.head[other] + 1) % 16
	p.count[other]--
	if p.count[other] == 0 && p.sendEmptyIE[other] {
		s.Raise(sideNine(other), IRQIPCSend)
	}
	return v
}
