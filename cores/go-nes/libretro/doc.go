// Package libretro will hold the libretro C ABI shell for the NES
// core: the c-shared build that exports the retro_* entry points
// the iTaK engine loads, mirroring the go-gameboy core's shell.
// It arrives in slice NES-3, once the PPU (NES-2) and APU make the
// machine playable; NES-1 ships the CPU, the cartridge mappers,
// and the conformance rig only, so this package is a documented
// placeholder.
package libretro
