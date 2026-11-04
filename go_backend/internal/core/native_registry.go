//go:build gocores

package core

import "path/filepath"

// The bundled Go core registry, compiled only under the
// gocores build tag (server-side reference and cross-check
// runs). The shipped app builds without the tag and gets
// the stub in native_registry_off.go instead.

// nativeCores maps a core file name (what the app passes to
// LoadCore) to its adapter constructor.
var nativeCores = map[string]func() NativeCore{
	"libgo_gameboy.so": func() NativeCore { return &gameboyCore{} },
	"libgo_nes.so":     func() NativeCore { return &nesCore{} },
	"libgo_snes.so":    func() NativeCore { return &snesCore{} },
	"libgo_genesis.so": func() NativeCore { return &genesisCore{} },
	"libgo_gba.so":     func() NativeCore { return &gbaCore{} },
	"libgo_ps1.so":     func() NativeCore { return &ps1Core{} },
}

// LoadNative returns a bridge over the bundled native core for
// the given core path, or false when the path names no bundled
// core (the caller then falls back to the .so bridge).
func LoadNative(corePath string) (*NativeBridge, bool) {
	base := filepath.Base(corePath)
	ctor, ok := nativeCores[base]
	if !ok {
		return nil, false
	}
	return &NativeBridge{core: ctor(), name: base}, true
}

