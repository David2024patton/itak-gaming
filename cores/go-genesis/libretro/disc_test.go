package main

import "testing"

// discImage2048 returns a one sector 2048 byte image carrying the
// boot signature at offset 0.
func discImage2048() []byte {
	data := make([]byte, 2048)
	copy(data, "SEGADISCSYSTEM")
	return data
}

// discImage2352 returns a one sector raw 2352 byte image: the sync
// pattern at offset 0, a four byte header, then the boot signature
// at offset 16.
func discImage2352() []byte {
	data := make([]byte, 2352)
	data[0] = 0x00
	for i := 1; i <= 10; i++ {
		data[i] = 0xFF
	}
	data[11] = 0x00
	copy(data[16:], "SEGADISCSYSTEM")
	return data
}

// cartridgeImage returns a small cartridge image carrying "SEGA" at
// offset 0x100, as a Genesis ROM header does.
func cartridgeImage() []byte {
	data := make([]byte, 1024)
	copy(data[0x100:], "SEGA")
	return data
}

func TestSniffDisc(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"2048 image", discImage2048(), true},
		{"2352 image", discImage2352(), true},
		{"cartridge header", cartridgeImage(), false},
		{"empty", nil, false},
		{"short", []byte("SEGA"), false},
		{"truncated signature", []byte("SEGADISC"), false},
		{"2352 signature without sync", func() []byte {
			data := make([]byte, 2352)
			copy(data[16:], "SEGADISCSYSTEM")
			return data
		}(), false},
	}
	for _, tc := range cases {
		if got := sniffDisc(tc.data); got != tc.want {
			t.Errorf("%s: sniffDisc = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRouteFor(t *testing.T) {
	cases := []struct {
		name string
		path string
		data []byte
		want int
	}{
		{"cue path wins over cartridge data", "game.cue", cartridgeImage(), routeDiscPath},
		{"uppercase cue path", "GAME.CUE", cartridgeImage(), routeDiscPath},
		{"iso path", "game.iso", cartridgeImage(), routeDiscPath},
		{"bin path with disc data", "game.bin", discImage2048(), routeDiscPath},
		{"no path with 2048 disc data", "", discImage2048(), routeDiscData},
		{"no path with 2352 disc data", "", discImage2352(), routeDiscData},
		{"md path with cartridge data", "game.md", cartridgeImage(), routeCartridge},
		{"no path with cartridge data", "", cartridgeImage(), routeCartridge},
		{"bin path with cartridge data", "game.bin", cartridgeImage(), routeCartridge},
	}
	for _, tc := range cases {
		if got := routeFor(tc.path, tc.data); got != tc.want {
			t.Errorf("%s: routeFor(%q) = %d, want %d", tc.name, tc.path, got, tc.want)
		}
	}
}
