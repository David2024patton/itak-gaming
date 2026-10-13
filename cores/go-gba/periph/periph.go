// Package periph implements the GBA interrupt controller,
// the four hardware timers, the four DMA channels and the
// keypad registers for GBA-2, written from GBATEK semantics.
//
// All register offsets in this package are relative to the
// IO base 0x04000000. The system package owns routing: it
// forwards each IO access to the one component that owns
// the offset, with the access size (1, 2 or 4 bytes), and
// multi byte accesses compose little endian across the
// registers of a component. The CPU steps in whole
// instructions and reports cycle counts, so the timers are
// driven by Advance calls with elapsed cycles, and DMA and
// keypad events arrive as explicit trigger calls from the
// system at line and frame boundaries.
package periph

import "encoding/binary"

// Memory is the transfer endpoint DMA works through: the
// system bus, narrowed to plain reads and writes. Read
// returns the raw aligned unit (a 4 byte read returns the
// word at addr&^3, a 2 byte read the halfword at addr&^1
// zero extended); Write stores a unit the same way.
type Memory interface {
	Read(addr uint32, size int) uint32
	Write(addr uint32, size int, val uint32)
}

// validSize reports whether size is a routable IO access
// width (byte, halfword or word).
func validSize(size int) bool {
	return size == 1 || size == 2 || size == 4
}

// compose builds a little endian read of size bytes
// starting at off from per byte values.
func compose(off uint32, size int, byteAt func(addr uint32) byte) uint32 {
	var v uint32
	for i := 0; i < size; i++ {
		v |= uint32(byteAt(off+uint32(i))) << (8 * i)
	}
	return v
}

// decompose splits a little endian write of size bytes
// starting at off into per byte writes, lowest byte
// first, so side effects that depend on register order
// (a reload write landing before the enable write that
// consumes it, inside one word access) see the same
// sequence the hardware register file would.
func decompose(off uint32, size int, val uint32, writeByte func(addr uint32, b byte)) {
	for i := 0; i < size; i++ {
		writeByte(off+uint32(i), byte(val>>(8*i)))
	}
}

// put16 stores v little endian at dst[i:].
func put16(dst []byte, i int, v uint16) {
	binary.LittleEndian.PutUint16(dst[i:], v)
}

// put32 stores v little endian at dst[i:].
func put32(dst []byte, i int, v uint32) {
	binary.LittleEndian.PutUint32(dst[i:], v)
}

// get16 loads a little endian halfword from src[i:].
func get16(src []byte, i int) uint16 {
	return binary.LittleEndian.Uint16(src[i:])
}

// get32 loads a little endian word from src[i:].
func get32(src []byte, i int) uint32 {
	return binary.LittleEndian.Uint32(src[i:])
}
