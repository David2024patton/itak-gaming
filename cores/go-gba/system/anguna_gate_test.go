package system

import (
	"crypto/sha256"
	"math"
	"os"
	"testing"
)

// The GBA-4 real-game gate: Anguna, a full open-source
// homebrew GBA game (homebrew/anguna.gba, local test
// material only, never shipped), booted and played on the
// core for 3,600 frames. The gate is presence-gated: it
// skips cleanly when the ROM is absent.
//
// The game flow below is what the GBA-4 probe runs
// (system probe logs, October 2026) measured on this
// core, and it matches the game's real behaviour:
//
//	frames 1..~450    intro fade and logo animation
//	frames ~500..900   title screen (static, about 67%
//	                   non-blank)
//	frame 900          Start (held 30 frames) opens the
//	                   menu (about 59% non-blank, the
//	                   display gains a second background)
//	frames 1250, 1600  A walks the menu (cursor steps)
//	frame 2400         A confirms; the game enters its
//	                   first animated scene (about 77%
//	                   non-blank, frame hash changes on
//	                   every sample from here on)
//
// Audio: the gate measures the output and finds digital
// silence for the whole run (RMS 0.0), and that is the
// game's own behaviour, traced exhaustively while
// bringing this gate up, not a core defect:
//
//   - A census of every IO write in the first 120 frames
//     shows the sound hardware written exactly three
//     times, all at boot: SOUNDCNT_H once, SOUNDCNT_X
//     twice (master enable plus the Direct Sound timer
//     select). No channel register is ever written.
//   - A 600 frame trace in gameplay shows the only IO
//     writes are the per-frame DMA3 OAM copy: zero sound
//     register writes, zero FIFO DMA arming.
//   - 1,800 frames of deep play (walking, sword swings,
//     interactions) produce zero sound register writes,
//     zero FIFO samples, and IE never gains a bit beyond
//     vblank, so no sound interrupt is ever unmasked.
//
// The APU itself is proven by the GBA-3 synthetic gates
// (exact tone frequencies, FIFO sine measurement), so a
// silent game stays silent here. The gate therefore pins
// the observed truth: the FIFO counters stay at zero and
// the output RMS stays at the floor. If a future change
// makes this title produce sound, or stops it being
// faithfully silent, the gate fails and a human looks.

const angunaROMPath = "../../../homebrew/anguna.gba"

// angunaPresses is the input script: frame number to the
// buttons held from that frame.
func angunaPresses() map[int]uint32 {
	press := map[int]uint32{}
	hold := func(at int, btn uint32, dur int) {
		for i := 0; i < dur; i++ {
			press[at+i] |= btn
		}
	}
	hold(900, ButtonStart, 30)
	hold(1250, ButtonA, 12)
	hold(1600, ButtonA, 12)
	hold(2400, ButtonA, 12)
	return press
}

func loadAnguna(t *testing.T) (*Machine, []byte) {
	t.Helper()
	data, err := os.ReadFile(angunaROMPath)
	if err != nil {
		t.Skipf("Anguna ROM not present (%v); gate skipped", err)
	}
	m := New()
	if err := m.LoadCart(data); err != nil {
		t.Fatalf("LoadCart(Anguna): %v", err)
	}
	return m, data
}

// fbBytes flattens the framebuffer to little-endian
// bytes for hashing (the conversion the original probe
// used, kept with the gate it evolved into).
func fbBytes(fb []uint32) []byte {
	out := make([]byte, len(fb)*4)
	for i, p := range fb {
		out[4*i] = byte(p)
		out[4*i+1] = byte(p >> 8)
		out[4*i+2] = byte(p >> 16)
		out[4*i+3] = byte(p >> 24)
	}
	return out
}

func frameHash(fb []uint32) [32]byte {
	return sha256.Sum256(fbBytes(fb))
}

func nonblankPct(fb []uint32) float64 {
	n := 0
	for _, p := range fb {
		if p != 0xFF000000 {
			n++
		}
	}
	return 100 * float64(n) / float64(len(fb))
}

func TestAngunaGate(t *testing.T) {
	m, data := loadAnguna(t)
	press := angunaPresses()

	// Anguna's ROM carries no save signature (devkitPro
	// build with an unfixed header), so detection reports
	// SaveNone and the fallback 32 KB SRAM window applies.
	if got := m.Cart().SaveKind.String(); got != "none" {
		t.Fatalf("Anguna save kind = %s, want none (no signature in the image)", got)
	}
	if got := len(m.SaveRAM()); got != 32768 {
		t.Fatalf("Anguna SaveRAM size = %d, want 32768 (fallback SRAM window)", got)
	}

	hashAt := map[int][32]byte{}
	pctAt := map[int]float64{}
	rmsMax := 0.0
	var stateAt2400 []byte
	sampleFrames := map[int]bool{600: true, 899: true, 1200: true, 1800: true, 2400: true, 3000: true, 3300: true, 3600: true}

	// Frames 1..2399: boot, title, Start into the menu.
	for f := 1; f <= 2399; f++ {
		m.SetButtons(press[f])
		m.RunFrame()
		ab := m.AudioBuffer()
		var ss float64
		for _, s := range ab {
			ss += float64(s) * float64(s)
		}
		if len(ab) > 0 {
			if r := math.Sqrt(ss / float64(len(ab))); r > rmsMax {
				rmsMax = r
			}
		}
		if sampleFrames[f] {
			hashAt[f] = frameHash(m.Framebuffer())
			pctAt[f] = nonblankPct(m.Framebuffer())
		}
	}
	stateAt2400 = m.Serialize()

	// The continuation, frames 2400..3600, recorded.
	contHash := map[int][32]byte{}
	contAudio := map[int][]int16{}
	for f := 2400; f <= 3600; f++ {
		m.SetButtons(press[f])
		m.RunFrame()
		ab := m.AudioBuffer()
		contAudio[f] = append([]int16{}, ab...)
		var ss float64
		for _, s := range ab {
			ss += float64(s) * float64(s)
		}
		if len(ab) > 0 {
			if r := math.Sqrt(ss / float64(len(ab))); r > rmsMax {
				rmsMax = r
			}
		}
		if sampleFrames[f] {
			hashAt[f] = frameHash(m.Framebuffer())
			pctAt[f] = nonblankPct(m.Framebuffer())
		}
		if f <= 2519 {
			contHash[f] = frameHash(m.Framebuffer())
		}
	}
	// Video assertions.
	for _, f := range []int{600, 1200, 1800, 2400, 3000, 3600} {
		if pctAt[f] < 25 {
			t.Fatalf("frame %d non-blank = %.1f%%, want a substantially drawn screen (>= 25%%)", f, pctAt[f])
		}
	}
	t.Logf("non-blank: f600 %.1f%% f1200 %.1f%% f1800 %.1f%% f2400 %.1f%% f3000 %.1f%% f3600 %.1f%%",
		pctAt[600], pctAt[1200], pctAt[1800], pctAt[2400], pctAt[3000], pctAt[3600])
	if hashAt[600] == hashAt[3600] {
		t.Fatal("frame at 3600 is identical to frame 600: the game did not progress")
	}
	// Start at frame 900 must leave the title screen: the
	// menu frame at 1200 differs materially from the
	// title frame at 899.
	if hashAt[899] == hashAt[1200] {
		t.Fatal("Start at frame 900 produced no frame change by 1200: title screen did not leave")
	}
	// The scene reached by 3000 is animated: consecutive
	// samples differ.
	if hashAt[3000] == hashAt[3300] {
		t.Fatal("frames 3000 and 3300 are identical: the in-game scene is not animating")
	}

	// Audio assertions: the traced profile documented at
	// the top of this file. RMS floor for this title is
	// the silence ceiling: the game requests no audio.
	if got := m.FIFOSamples(1) + m.FIFOSamples(2); got != 0 {
		t.Fatalf("FIFO samples delivered = %d, want 0 for Anguna's traced profile", got)
	}
	if rmsMax > 1.0 {
		t.Fatalf("audio RMS max = %.2f, want <= 1.0 (Anguna makes no sound; see the header)", rmsMax)
	}
	t.Logf("audio: RMS max %.2f over 3600 frames, FIFO samples 0 (game-side silence, traced)", rmsMax)

	// Save at 2400, restore into a fresh machine, and
	// require the next 120 frames to match byte for byte
	// (framebuffer hashes and audio samples).
	m2 := New()
	if err := m2.LoadCart(data); err != nil {
		t.Fatalf("LoadCart(Anguna, fresh): %v", err)
	}
	if err := m2.Deserialize(stateAt2400); err != nil {
		t.Fatalf("Deserialize at frame 2400: %v", err)
	}
	for f := 2400; f <= 2519; f++ {
		m2.SetButtons(press[f])
		m2.RunFrame()
		ab := m2.AudioBuffer()
		wantHash := contHash[f]
		if got := frameHash(m2.Framebuffer()); got != wantHash {
			t.Fatalf("restored frame %d hash = %x, want %x (continuation diverged)", f, got[:6], wantHash[:6])
		}
		want := contAudio[f]
		if len(ab) != len(want) {
			t.Fatalf("restored frame %d produced %d audio samples, want %d", f, len(ab), len(want))
		}
		for i := range ab {
			if ab[i] != want[i] {
				t.Fatalf("restored frame %d audio sample %d = %d, want %d", f, i, ab[i], want[i])
			}
		}
	}
	t.Logf("save/restore: 120 frames after the frame-2400 state are byte-identical (video and audio)")
}
