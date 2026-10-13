package kernel

import (
	"errors"
	"sort"
)

// The user partition (partition id 2) is the documented 24 MB of
// main RAM at 0x08800000. Blocks are carved out of it with the
// sceKernelAllocPartitionMemory placement types. Block sizes are
// rounded up to a 256 byte granularity, which is the granularity
// this kernel accounts in; free space accounting is exact against
// that rule, so TotalFreeMemSize and MaxFreeMemSize are
// deterministic functions of the allocation history.
const (
	UserPartitionID   = 2
	UserPartitionBase = 0x08800000

	// moduleLoadReserve is the reserved head of the user
	// partition: a loaded module's block starts at the partition
	// base but its image sits this many bytes in (16 KB).
	moduleLoadReserve = 0x4000
	UserPartitionSize = 24 << 20
	blockGranularity  = 0x100
)

// Allocation types (PspSysMemBlockTypes in pspsdk pspsysmem.h).
const (
	allocLow         = 0 // PSP_SMEM_Low
	allocHigh        = 1 // PSP_SMEM_High
	allocAddr        = 2 // PSP_SMEM_Addr
	allocLowAligned  = 3 // PSP_SMEM_LowAligned
	allocHighAligned = 4 // PSP_SMEM_HighAligned
)

type memBlock struct {
	uid  uint32
	name string
	addr uint32
	size uint32 // rounded size actually reserved
}

func (b *memBlock) objKind() uint32 { return kindMemBlock }

// partition is a first fit block allocator over one address range.
// Allocated blocks are kept sorted by address; free ranges are the
// gaps between them.
type partition struct {
	base   uint32
	size   uint32
	blocks []*memBlock
}

func newPartition(base, size uint32) *partition {
	return &partition{base: base, size: size}
}

func roundBlockSize(size uint32) uint32 {
	if size == 0 {
		return 0
	}
	return (size + blockGranularity - 1) &^ (blockGranularity - 1)
}

type freeRange struct{ start, end uint32 }

func (p *partition) freeRanges() []freeRange {
	var out []freeRange
	cur := p.base
	for _, b := range p.blocks {
		if b.addr > cur {
			out = append(out, freeRange{cur, b.addr})
		}
		if end := b.addr + b.size; end > cur {
			cur = end
		}
	}
	if cur < p.base+p.size {
		out = append(out, freeRange{cur, p.base + p.size})
	}
	return out
}

func (p *partition) totalFree() uint32 {
	var total uint32
	for _, r := range p.freeRanges() {
		total += r.end - r.start
	}
	return total
}

func (p *partition) maxFree() uint32 {
	var max uint32
	for _, r := range p.freeRanges() {
		if s := r.end - r.start; s > max {
			max = s
		}
	}
	return max
}

func alignUp(v, a uint32) uint32 {
	if a <= 1 {
		return v
	}
	return (v + a - 1) &^ (a - 1)
}

func alignDown(v, a uint32) uint32 {
	if a <= 1 {
		return v
	}
	return v &^ (a - 1)
}

// alloc places a block of the (rounded) size per the placement
// type. For allocAddr, addr is the exact start. For the aligned
// types, addr is the alignment (a power of two). It returns the
// start address.
func (p *partition) alloc(typ int, size, addr uint32) (uint32, error) {
	size = roundBlockSize(size)
	if size == 0 || size > p.size {
		return 0, errors.New("partition: bad size")
	}
	ranges := p.freeRanges()
	place := func(start uint32) (uint32, error) {
		nb := &memBlock{addr: start, size: size}
		i := sort.Search(len(p.blocks), func(i int) bool { return p.blocks[i].addr > start })
		p.blocks = append(p.blocks, nil)
		copy(p.blocks[i+1:], p.blocks[i:])
		p.blocks[i] = nb
		return start, nil
	}
	switch typ {
	case allocLow, allocLowAligned:
		align := uint32(1)
		if typ == allocLowAligned {
			align = addr
			if align == 0 || align&(align-1) != 0 {
				return 0, errors.New("partition: bad alignment")
			}
		}
		for _, r := range ranges {
			start := alignUp(r.start, align)
			if start+size <= r.end {
				return place(start)
			}
		}
	case allocHigh, allocHighAligned:
		align := uint32(1)
		if typ == allocHighAligned {
			align = addr
			if align == 0 || align&(align-1) != 0 {
				return 0, errors.New("partition: bad alignment")
			}
		}
		for i := len(ranges) - 1; i >= 0; i-- {
			r := ranges[i]
			if r.end-r.start >= size {
				start := alignDown(r.end-size, align)
				if start >= r.start {
					return place(start)
				}
			}
		}
	case allocAddr:
		for _, r := range ranges {
			if addr >= r.start && addr+size <= r.end {
				return place(addr)
			}
		}
		return 0, errors.New("partition: address range not free")
	default:
		return 0, errors.New("partition: bad allocation type")
	}
	return 0, errors.New("partition: out of memory")
}

// allocInternal allocates a block and registers it as a kernel
// object with the given name, returning its UID and address.
func (p *partition) allocInternal(k *Kernel, name string, typ int, size, addr uint32) (uint32, uint32, error) {
	start, err := p.alloc(typ, size, addr)
	if err != nil {
		return 0, 0, err
	}
	uid := k.NewUID(kindMemBlock)
	// Find the block just placed and tag it.
	for _, b := range p.blocks {
		if b.addr == start && b.uid == 0 {
			b.uid = uid
			b.name = name
			break
		}
	}
	k.registerObject(uid, p.blockByAddr(start))
	return uid, start, nil
}

func (p *partition) blockByAddr(addr uint32) *memBlock {
	for _, b := range p.blocks {
		if b.addr == addr {
			return b
		}
	}
	return nil
}

func (p *partition) blockByUID(uid uint32) *memBlock {
	for _, b := range p.blocks {
		if b.uid == uid {
			return b
		}
	}
	return nil
}

// free releases the block with the given UID.
func (p *partition) free(uid uint32) bool {
	for i, b := range p.blocks {
		if b.uid == uid {
			p.blocks = append(p.blocks[:i], p.blocks[i+1:]...)
			return true
		}
	}
	return false
}
