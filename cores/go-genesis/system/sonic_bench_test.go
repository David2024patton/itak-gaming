package system

import (
	"encoding/binary"
	"hash/fnv"
	"os"
	"testing"
)

// sonicBenchROM is the local probe copy of Sonic 1 used by the
// bridge benchmark in go_backend. It is never committed; every
// entry point here skips when the file is absent.
const sonicBenchROM = "/tmp/sonic_probe.md"

func loadSonicBench(tb testing.TB) *System {
	tb.Helper()
	data, err := os.ReadFile(sonicBenchROM)
	if err != nil {
		tb.Skip("sonic probe ROM not present")
	}
	sys := New()
	if err := sys.LoadGame(data); err != nil {
		tb.Fatalf("LoadGame: %v", err)
	}
	return sys
}

// frameHash is a 64-bit FNV-1a over the framebuffer in little
// endian byte order. It is the pixel-stability gate for the
// speed work: optimizations must not change it.
func frameHash(sys *System) uint64 {
	h := fnv.New64a()
	var buf [2]byte
	for _, px := range sys.Framebuffer() {
		binary.LittleEndian.PutUint16(buf[:], px)
		h.Write(buf[:])
	}
	return h.Sum64()
}

// TestSonicFrame730Hash records the framebuffer hash after 730
// frames of Sonic 1 from power-on (the long-standing proof gate
// point) and logs it for comparison across optimizations.
func TestSonicFrame730Hash(t *testing.T) {
	sys := loadSonicBench(t)
	for i := 0; i < 730; i++ {
		sys.RunFrame()
		sys.AudioBuffer()
	}
	t.Logf("frame 730 hash: %016x size %dx%d", frameHash(sys), sys.Width(), sys.Height())
}

// BenchmarkSonicFrame mirrors the bridge benchmark: 300 warmup
// frames from power-on, then timed frames, draining audio each
// frame the way the native bridge does.
func BenchmarkSonicFrame(b *testing.B) {
	sys := loadSonicBench(b)
	for i := 0; i < 300; i++ {
		sys.RunFrame()
		sys.AudioBuffer()
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sys.RunFrame()
		sys.AudioBuffer()
	}
}
