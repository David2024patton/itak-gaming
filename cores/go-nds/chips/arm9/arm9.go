// Package arm9 exposes the DS module's ARM946E-S processor (the
// "ARM9"): the ARMv5TE core that runs the main game code on the
// Nintendo DS. Over the ARMv4T base it adds CLZ, the saturating
// Q arithmetic, the signed halfword multiplies, LDRD/STRD, BLX,
// BKPT, and the CP15 system control coprocessor with its ITCM
// and DTCM configuration registers. It is the ARMv5TE
// configuration of the module's shared ARM core (chips/armcore).
//
// In this slice CP15 cache and MPU registers are stored but not
// acted on: no cache is simulated and no memory protection is
// enforced. The TCM base/size registers are live, because the
// system bus decodes the TCM windows from them.
package arm9

import "github.com/davidpatton/itak-gaming/cores/go-nds/chips/armcore"

// Bus is the memory interface the CPU executes against.
type Bus = armcore.Bus

// CPU is the ARM946E-S state. The fields and methods are those
// of armcore.CPU; see that package for the state layout.
type CPU = armcore.CPU

// New returns an ARM946E-S in its reset state, bound to bus b,
// with the DS power-on CP15 TCM configuration installed.
func New(b Bus) *CPU {
	return armcore.NewARM9(b)
}

// StateSize is the serialized size of the CPU state in bytes.
const StateSize = armcore.StateSize
