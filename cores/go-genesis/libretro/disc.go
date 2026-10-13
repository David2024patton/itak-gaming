package main

import (
	"path/filepath"
	"strings"
)

// Disc routing for retro_load_game. A frontend can hand this core a
// cartridge image, a cue sheet path, an ISO path, or a raw disc image
// with no path at all. These helpers decide which loader the bytes and
// the path belong to; they are pure so they can be unit tested without
// the cgo shell.

// Load routes returned by routeFor.
const (
	routeCartridge = iota
	routeDiscPath
	routeDiscData
)

// discSignature is the boot signature at the start of every Sega CD
// disc's data area.
const discSignature = "SEGADISCSYSTEM"

// sniffDisc reports whether data looks like a Sega CD disc image. A
// 2048 byte sector image carries the signature at offset 0. A raw
// 2352 byte sector image carries the twelve byte sync pattern first
// (0x00, ten 0xFF bytes, 0x00), then a four byte header, so the
// signature lands at offset 16. A Genesis cartridge, which carries
// "SEGA" at offset 0x100 instead, returns false.
func sniffDisc(data []byte) bool {
	if len(data) >= len(discSignature) && string(data[:len(discSignature)]) == discSignature {
		return true
	}
	if len(data) < 16+len(discSignature) {
		return false
	}
	if data[0] != 0x00 || data[11] != 0x00 {
		return false
	}
	for i := 1; i <= 10; i++ {
		if data[i] != 0xFF {
			return false
		}
	}
	return string(data[16:16+len(discSignature)]) == discSignature
}

// routeFor picks the loader for one retro_load_game call. A .cue or
// .iso path always wins: the cue sheet is not the disc data, and an
// ISO is a disc by definition. Otherwise the data decides: a disc
// signature means a disc, loaded from its path when it has one and
// from the bytes themselves when it does not. Anything else is a
// cartridge.
func routeFor(path string, data []byte) int {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cue", ".iso":
		return routeDiscPath
	}
	if sniffDisc(data) {
		if path != "" {
			return routeDiscPath
		}
		return routeDiscData
	}
	return routeCartridge
}
