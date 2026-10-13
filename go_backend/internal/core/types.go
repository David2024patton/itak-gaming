package core

// System represents a game console
type System string

const (
	SystemNES       System = "nes"
	SystemSNES      System = "snes"
	SystemGenesis   System = "genesis"
	SystemGameBoy   System = "gameboy"
	SystemGBC       System = "gbc"
	SystemGBA       System = "gba"
	SystemPS1       System = "ps1"
	SystemSegaCD    System = "segacd"
	SystemN64       System = "n64"
	SystemDreamcast System = "dreamcast"
	SystemPSP       System = "psp"
	SystemDS        System = "ds"
	SystemSaturn    System = "saturn"
	SystemWii       System = "wii"
)

// CoreInfo describes an emulator core
type CoreInfo struct {
	System     System
	Name       string   // e.g. "Snes9x"
	Library    string   // e.g. "libgo_snes.so"
	Extensions []string // e.g. [".sfc", ".smc"]
	Bitness    int      // 32 or 64 (minimum required)
}

// SupportedCores lists all cores by platform
var Cores32Bit = []CoreInfo{
	{SystemNES, "Go NES", "libgo_nes.so", []string{".nes"}, 32},
	{SystemSNES, "Go SNES", "libgo_snes.so", []string{".sfc", ".smc"}, 32},
	{SystemGenesis, "Go Genesis", "libgo_genesis.so", []string{".md", ".gen", ".bin"}, 32},
	{SystemGameBoy, "Go Game Boy", "libgo_gameboy.so", []string{".gb"}, 32},
	{SystemGBC, "Go Game Boy", "libgo_gameboy.so", []string{".gbc"}, 32},
	{SystemGBA, "mGBA", "libmgba.so", []string{".gba"}, 32},
	{SystemPS1, "PCSX ReARMed", "libpcsx_rearmed.so", []string{".cue", ".iso"}, 32},
	{SystemSegaCD, "Genesis Plus GX", "libgenesis_plus_gx.so", []string{".cue", ".iso"}, 32},
}

var Cores64BitOnly = []CoreInfo{
	{SystemN64, "Mupen64Plus", "libmupen64plus.so", []string{".n64", ".z64"}, 64},
	{SystemDreamcast, "Flycast", "libflycast.so", []string{".cdi", ".gdi"}, 64},
	{SystemPSP, "PPSSPP", "libppsspp.so", []string{".iso", ".cso"}, 64},
	{SystemDS, "melonDS", "libmelonds.so", []string{".nds"}, 64},
	{SystemSaturn, "Yabasanshiro", "libyabasanshiro.so", []string{".cue", ".iso"}, 64},
	{SystemWii, "Dolphin", "libdolphin.so", []string{".iso", ".wbfs"}, 64},
}

// AllCores64Bit returns all cores available on 64-bit
func AllCores64Bit() []CoreInfo {
	return append(append([]CoreInfo{}, Cores32Bit...), Cores64BitOnly...)
}

// CoreForSystem finds the core for a system
func CoreForSystem(system System, is64Bit bool) *CoreInfo {
	cores := Cores32Bit
	if is64Bit {
		cores = AllCores64Bit()
	}
	for _, c := range cores {
		if c.System == system {
			return &c
		}
	}
	return nil
}
