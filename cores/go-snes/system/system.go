// Package system is the SNES-1 test rig: the W65C816 CPU
// wired to the cartridge memory map, nothing else. It
// exists to prove the CPU and the map integrate (reset
// vector entry, code executing out of ROM banks, block
// moves and mode switches behaving end to end) before the
// real machine slices land: SNES-2 adds the PPU and the
// console timing, SNES-3 the SPC700 and DSP audio, SNES-4
// the libretro shell and the app wiring.
//
// The rig's shape prefigures the machine: LoadGame builds
// the console around a cartridge image, Reset runs the CPU
// reset sequence against the map, and RunCycles steps the
// CPU by bus cycles. There is no frame concept yet because
// there is no picture hardware yet.
package system

import (
	"github.com/davidpatton/itak-gaming/cores/go-snes/cart"
	"github.com/davidpatton/itak-gaming/cores/go-snes/chips/w65c816"
)

// System is the SNES-1 rig: one CPU on one cartridge map.
type System struct {
	CPU  *w65c816.CPU
	Cart *cart.Cart
	Map  *cart.Map

	loaded bool
}

// New builds a rig around a cartridge image and resets it.
func New(image []byte) (*System, error) {
	s := &System{}
	if err := s.LoadGame(image); err != nil {
		return nil, err
	}
	return s, nil
}

// LoadGame loads a cartridge, builds its map, attaches the
// CPU, and runs the reset sequence.
func (s *System) LoadGame(image []byte) error {
	ct, err := cart.Load(image)
	if err != nil {
		return err
	}
	s.Cart = ct
	s.Map = cart.NewMap(ct)
	s.CPU = w65c816.New()
	s.CPU.Attach(s.Map)
	s.loaded = true
	s.Reset()
	return nil
}

// Loaded reports whether a game is in the machine.
func (s *System) Loaded() bool { return s.loaded }

// Reset runs the CPU reset sequence against the map.
func (s *System) Reset() {
	if !s.loaded {
		return
	}
	s.CPU.Reset()
}

// RunCycles steps the CPU until at least n bus cycles have
// elapsed, returning the cycles actually run (instructions
// are atomic, so the total can overshoot).
func (s *System) RunCycles(n uint64) uint64 {
	var run uint64
	for run < n {
		run += uint64(s.CPU.Step())
	}
	return run
}

// Title returns the cartridge title, for logs and UIs.
func (s *System) Title() string {
	if s.Cart == nil {
		return ""
	}
	return s.Cart.Header.Title
}
