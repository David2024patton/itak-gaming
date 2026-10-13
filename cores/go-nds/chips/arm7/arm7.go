// Package arm7 exposes the DS module's ARM7TDMI processor: the
// ARMv4T core (full ARM and Thumb states) that drives sound,
// touchscreen and wireless on the Nintendo DS. It is the
// ARMv4T configuration of the module's shared ARM core
// (chips/armcore); it has no coprocessor, so coprocessor
// encodings take the undefined-instruction exception.
package arm7

import "github.com/davidpatton/itak-gaming/cores/go-nds/chips/armcore"

// Bus is the memory interface the CPU executes against.
type Bus = armcore.Bus

// CPU is the ARM7TDMI state. The fields and methods are those of
// armcore.CPU; see that package for the state layout.
type CPU = armcore.CPU

// New returns an ARM7TDMI in its reset state, bound to bus b.
func New(b Bus) *CPU {
	return armcore.NewARM7(b)
}

// StateSize is the serialized size of the CPU state in bytes.
const StateSize = armcore.StateSize
