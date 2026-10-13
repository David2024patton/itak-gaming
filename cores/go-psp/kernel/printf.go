package kernel

import (
	"strconv"
	"strings"
)

// printf implements the sceKernelPrintf format subset this kernel
// documents: %d %i %u %x %X %c %s %p and %%, with an optional 0
// flag, field width, and the l / ll length modifiers. Arguments
// follow the Allegrex o32 convention: $a1..$a3 hold the first
// three, the rest sit on the caller's stack at $sp+16, $sp+20, ...
type argReader struct {
	k    *Kernel
	th   *Thread
	regs [3]uint32 // $a1, $a2, $a3
	sp   uint32
	next int
}

func newArgReader(k *Kernel, th *Thread) *argReader {
	return &argReader{
		k:    k,
		th:   th,
		regs: [3]uint32{k.cpu.Reg(5), k.cpu.Reg(6), k.cpu.Reg(7)},
		sp:   k.cpu.Reg(29),
	}
}

func (a *argReader) word() uint32 {
	i := a.next
	a.next++
	if i < 3 {
		return a.regs[i]
	}
	return a.k.mem.Read32(a.sp + 16 + uint32(i-3)*4)
}

func (a *argReader) dword() uint64 {
	lo := uint64(a.word())
	hi := uint64(a.word())
	return lo | hi<<32
}

func (k *Kernel) kernelPrintf(th *Thread, formatAddr uint32) string {
	format := k.readCString(formatAddr, 4096)
	args := newArgReader(k, th)
	var out strings.Builder
	i := 0
	for i < len(format) {
		c := format[i]
		if c != '%' {
			out.WriteByte(c)
			i++
			continue
		}
		i++
		if i >= len(format) {
			out.WriteByte('%')
			break
		}
		zero := false
		if format[i] == '0' {
			zero = true
			i++
		}
		width := 0
		for i < len(format) && format[i] >= '0' && format[i] <= '9' {
			width = width*10 + int(format[i]-'0')
			i++
		}
		longs := 0
		for i < len(format) && format[i] == 'l' {
			longs++
			i++
		}
		if i >= len(format) {
			break
		}
		verb := format[i]
		i++
		pad := func(s string) string {
			if len(s) >= width {
				return s
			}
			fill := " "
			if zero {
				fill = "0"
			}
			return strings.Repeat(fill, width-len(s)) + s
		}
		switch verb {
		case '%':
			out.WriteByte('%')
		case 'd', 'i':
			if longs >= 2 {
				out.WriteString(pad(strconv.FormatInt(int64(args.dword()), 10)))
			} else {
				out.WriteString(pad(strconv.FormatInt(int64(int32(args.word())), 10)))
			}
		case 'u':
			if longs >= 2 {
				out.WriteString(pad(strconv.FormatUint(args.dword(), 10)))
			} else {
				out.WriteString(pad(strconv.FormatUint(uint64(args.word()), 10)))
			}
		case 'x', 'X':
			var s string
			if longs >= 2 {
				s = strconv.FormatUint(args.dword(), 16)
			} else {
				s = strconv.FormatUint(uint64(args.word()), 16)
			}
			if verb == 'X' {
				s = strings.ToUpper(s)
			}
			out.WriteString(pad(s))
		case 'p':
			out.WriteString("0x" + pad(strconv.FormatUint(uint64(args.word()), 16)))
		case 'c':
			out.WriteByte(byte(args.word()))
		case 's':
			out.WriteString(k.readCString(args.word(), 8192))
		default:
			// Unknown verbs are echoed verbatim.
			out.WriteByte('%')
			out.WriteByte(verb)
		}
	}
	return out.String()
}
