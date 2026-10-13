// Command ggdebug runs the Go Genesis machine directly (no libretro
// shell) and prints where the CPUs spend their time, what reached the
// VDP, and whether the framebuffer ever lights up. A debugging tool for
// core development; not part of the shipped core.
package main

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"os"
	"sort"

	"github.com/davidpatton/itak-gaming/cores/go-genesis/bus"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/cart"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/sound"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/system"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/vdp"

	m68k "github.com/davidpatton/itak-gaming/cores/go-genesis/chips/m68k"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/chips/z80"
)

const (
	masterFrame = 3420 * 262
	linesTotal  = 262
	z80PerLine  = 228
)

func m68kTarget(line int) uint64 { return uint64(line+1) * masterFrame / 7 / linesTotal }

// dumpPNG writes the visible framebuffer to a PNG file.
func dumpPNG(v *vdp.VDP, path string) {
	w, h := v.DisplayWidth(), v.DisplayHeight()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fb := v.Framebuffer()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			px := fb[y*320+x]
			r5 := (px >> 11) & 0x1F
			g6 := (px >> 5) & 0x3F
			b5 := px & 0x1F
			img.Set(x, y, color.RGBA{uint8(r5<<3 | r5>>2), uint8(g6<<2 | g6>>4), uint8(b5<<3 | b5>>2), 0xFF})
		}
	}
	out, err := os.Create(path)
	if err != nil {
		return
	}
	png.Encode(out, img)
	out.Close()
}

// savetest replays the engine test's exact script against the system
// package and checks save/restore determinism: the frame 2 frames after
// a restore must equal the frame at the save point, and a state that is
// serialized, restored and re-serialized must be byte-identical.
func savetest(romPath string) {
	data, err := os.ReadFile(romPath)
	if err != nil {
		panic(err)
	}
	sys := system.New()
	if err := sys.LoadGame(data); err != nil {
		panic(err)
	}
	frame := 0
	firstNonBlank := -1
	fbHash := func() uint64 {
		h := fnv.New64a()
		var b [2]byte
		for _, px := range sys.Framebuffer() {
			binary.LittleEndian.PutUint16(b[:], px)
			h.Write(b[:])
		}
		return h.Sum64()
	}
	audioNZ := 0
	run := func(n int, mask uint16) {
		for i := 0; i < n; i++ {
			sys.SetButtons(0, mask)
			sys.RunFrame()
			frame++
			for _, s := range sys.AudioBuffer() {
				if s != 0 && frame > 400 {
					audioNZ++
				}
			}
			if firstNonBlank < 0 && frame <= 200 {
				for _, px := range sys.Framebuffer() {
					if px != 0 {
						firstNonBlank = frame
						break
					}
				}
			}
		}
	}
	run(120, 0)
	run(480, 0)
	run(10, bus.PadStart)
	run(120, 0)
	fmt.Printf("first non-blank frame: %d\n", firstNonBlank)
	// Dump the save-point frame (in-level gameplay) as a PNG.
	{
		w, h := sys.Width(), sys.Height()
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		fb := sys.Framebuffer()
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				px := fb[y*320+x]
				r5 := (px >> 11) & 0x1F
				g6 := (px >> 5) & 0x3F
				b5 := px & 0x1F
				img.Set(x, y, color.RGBA{uint8(r5<<3 | r5>>2), uint8(g6<<2 | g6>>4), uint8(b5<<3 | b5>>2), 0xFF})
			}
		}
		out, _ := os.Create("/tmp/gengames/gameplay.png")
		png.Encode(out, img)
		out.Close()
		fmt.Println("gameplay frame dumped to /tmp/gengames/gameplay.png")
	}
	h1 := fbHash()
	state := make([]byte, sys.SerializeSize())
	if err := sys.Serialize(state); err != nil {
		panic(err)
	}
	fmt.Printf("save point: frame=%d fb=%016x state=%d bytes\n", frame, h1, len(state))
	psgW, ymW := sys.SoundWrites()
	fmt.Printf("audio: %d non-zero samples produced in frames 401-%d; chip writes so far: PSG=%d YM2612=%d\n",
		audioNZ, frame, psgW, ymW)

	run(2, bus.PadDown)
	h732 := fbHash()
	run(8, bus.PadDown)
	run(30, 0)
	fmt.Printf("moved on:   frame=%d fb=%016x (differs: %v)\n", frame, fbHash(), fbHash() != h1)

	if err := sys.Deserialize(state); err != nil {
		panic(err)
	}
	rt := make([]byte, sys.SerializeSize())
	if err := sys.Serialize(rt); err != nil {
		panic(err)
	}
	regions := []struct {
		name string
		lo   int
		hi   int
	}{
		{"m68k regs", 0, 80},
		{"z80 cpu", 80, 127},
		{"bus", 127, 139411},
		{"vdp", 139411, 205189},
		{"sound", 205189, 207125},
		{"trailer", 207125, len(state)},
	}
	ndiff := 0
	for i := range state {
		if state[i] != rt[i] {
			if ndiff < 5 {
				reg := "?"
				for _, r := range regions {
					if i >= r.lo && i < r.hi {
						reg = r.name
					}
				}
				fmt.Printf("state round-trip diff at byte %d (%s): %02x != %02x\n", i, reg, state[i], rt[i])
			}
			ndiff++
		}
	}
	fmt.Printf("state round-trip: %d bytes differ\n", ndiff)

	run(2, bus.PadDown)
	h3 := fbHash()
	fmt.Printf("restored+2: fb=%016x  harness-match(save point)=%v  deterministic-match(frame732)=%v\n",
		h3, h3 == h1, h3 == h732)
}

// playshot runs a game past its title screen, presses Start, plays a
// few seconds and dumps the frame: a visual proof of gameplay.
func playshot(romPath string) {
	data, err := os.ReadFile(romPath)
	if err != nil {
		panic(err)
	}
	sys := system.New()
	if err := sys.LoadGame(data); err != nil {
		panic(err)
	}
	frame := 0
	dump := func(tag string) {
		colors := map[uint16]bool{}
		for _, px := range sys.Framebuffer() {
			colors[px] = true
		}
		w, h := sys.Width(), sys.Height()
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		fb := sys.Framebuffer()
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				px := fb[y*320+x]
				r5 := (px >> 11) & 0x1F
				g6 := (px >> 5) & 0x3F
				b5 := px & 0x1F
				img.Set(x, y, color.RGBA{uint8(r5<<3 | r5>>2), uint8(g6<<2 | g6>>4), uint8(b5<<3 | b5>>2), 0xFF})
			}
		}
		out, _ := os.Create("/tmp/gengames/play_" + tag + ".png")
		png.Encode(out, img)
		out.Close()
		fmt.Printf("frame %d (%s): %d distinct colors\n", frame, tag, len(colors))
	}
	run := func(n int, mask uint16) {
		for i := 0; i < n; i++ {
			sys.SetButtons(0, mask)
			sys.RunFrame()
			sys.AudioBuffer()
			frame++
		}
	}
	run(700, 0)
	dump("700")
	run(120, 0)
	dump("820")
	run(10, bus.PadStart) // start the game
	dump("830")
	run(180, 0)
	dump("1010")
	run(150, 0)
	dump("1160")
	run(90, 0)
	dump("1250")
	run(120, 0)
	dump("1370")
}

func main() {
	if len(os.Args) >= 3 && os.Args[1] == "-savetest" {
		savetest(os.Args[2])
		return
	}
	if len(os.Args) >= 3 && os.Args[1] == "-playshot" {
		playshot(os.Args[2])
		return
	}
	if len(os.Args) < 2 {
		fmt.Println("usage: ggdebug <rom> [frames] | ggdebug -savetest <rom>")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	frames := 180
	if len(os.Args) > 2 {
		fmt.Sscanf(os.Args[2], "%d", &frames)
	}
	c, err := cart.Load(data)
	if err != nil {
		panic(err)
	}
	fmt.Printf("cart: %q serial=%s checksum ok=%v sram=%v region=%c\n",
		c.Title, c.Serial, c.ChecksumOK, c.SRAM, c.Region)

	v := vdp.New()
	snd := sound.New()
	b := bus.New(c, v, snd)
	cpu := m68k.New(b)
	zcpu := z80.New(b)
	cpu.Reset()
	zcpu.Reset()
	fmt.Printf("boot: SSP=%08X PC=%08X\n", cpu.Registers().A[7], cpu.Registers().PC)

	pcHist := map[uint32]int{}
	var trail []uint32
	run68k := func(target uint64) {
		for i := 0; i < 32; i++ {
			cur := cpu.Cycles()
			if cur >= target {
				return
			}
			cpu.StepCycles(int(target - cur))
			pc := cpu.Registers().PC
			pcHist[pc]++
			if pc != 0x71FC4 && pc != 0x71FC6 {
				trail = append(trail, pc)
				if len(trail) > 400 {
					trail = trail[len(trail)-400:]
				}
			}
			if cpu.Cycles() == cur {
				return
			}
		}
	}
	runZ80 := func() {
		if b.TakeZ80Reset() {
			zcpu.Reset()
		}
		if b.Z80Halted() {
			return
		}
		target := zcpu.Cycles() + z80PerLine
		for i := 0; i < 32; i++ {
			cur := zcpu.Cycles()
			if cur >= target {
				return
			}
			zcpu.StepCycles(int(target - cur))
			if zcpu.Cycles() == cur {
				return
			}
		}
	}

	startAt := -1
	if s := os.Getenv("START_AT"); s != "" {
		fmt.Sscanf(s, "%d", &startAt)
	}
	for f := 0; f < frames; f++ {
		if f == startAt {
			b.SetPad(0, bus.PadStart)
		}
		if f == startAt+10 {
			b.SetPad(0, 0)
		}
		h := v.DisplayHeight()
		base := uint64(f) * masterFrame / 7
		v.BeginFrame()
		for line := 0; line < linesTotal; line++ {
			if line == 0 {
				v.ExitVBlank()
				zcpu.INT(false, 0)
			}
			run68k(base + m68kTarget(line))
			runZ80()
			if line < h {
				if v.LineIRQ(line) {
					cpu.RequestInterrupt(4, nil)
				}
				v.RenderLine(line)
			}
			if line == h {
				if v.EnterVBlank() {
					cpu.RequestInterrupt(6, nil)
				}
				zcpu.INT(true, 0xFF)
			}
		}
		snd.Produce(736)

		if f%30 == 29 || f == frames-1 {
			vramNZ, cramNZ := 0, 0
			for _, x := range v.VRAM {
				if x != 0 {
					vramNZ++
				}
			}
			for _, x := range v.CRAM {
				if x != 0 {
					cramNZ++
				}
			}
			fb := v.Framebuffer()
			colors := map[uint16]bool{}
			for _, px := range fb {
				colors[px] = true
			}
			if f+1 == 500 {
				dumpPNG(v, "/tmp/gengames/frame500.png")
				fmt.Printf("frame 500 PC=%08X CRAM0=%04X\n", cpu.Registers().PC, uint16(v.CRAM[0])<<8|uint16(v.CRAM[1]))
			}
			if f+1 == 210 {
				vals := []int{}
				for px := range colors {
					vals = append(vals, int(px))
				}
				sort.Ints(vals)
				fmt.Printf("frame 210 distinct fb values: %04X\n", vals)
				fmt.Printf("frame 210 CRAM words 0-7: %04X %04X %04X %04X %04X %04X %04X %04X\n",
					uint16(v.CRAM[0])<<8|uint16(v.CRAM[1]), uint16(v.CRAM[2])<<8|uint16(v.CRAM[3]),
					uint16(v.CRAM[4])<<8|uint16(v.CRAM[5]), uint16(v.CRAM[6])<<8|uint16(v.CRAM[7]),
					uint16(v.CRAM[8])<<8|uint16(v.CRAM[9]), uint16(v.CRAM[10])<<8|uint16(v.CRAM[11]),
					uint16(v.CRAM[12])<<8|uint16(v.CRAM[13]), uint16(v.CRAM[14])<<8|uint16(v.CRAM[15]))
				dumpPNG(v, "/tmp/gengames/frame210.png")
			}
			r := cpu.Registers()
			fmt.Printf("frame %3d: PC=%08X SR=%04X D0=%08X D1=%08X A0=%08X cycles=%d VDP R1=%02X dispW=%d VRAM nz=%d CRAM nz=%d colors=%d Z80 halted=%v Z80 PC=%04X\n",
				f+1, r.PC, r.SR, r.D[0], r.D[1], r.A[0], cpu.Cycles(), v.Regs[1], v.DisplayWidth(), vramNZ, cramNZ, len(colors), b.Z80Halted(), zcpu.Registers().PC)
		}
	}

	type pcCount struct {
		pc uint32
		n  int
	}
	var top []pcCount
	for pc, n := range pcHist {
		top = append(top, pcCount{pc, n})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].n > top[j].n })
	fmt.Println("--- hottest PCs (where the 68000 spends its time):")
	for i := 0; i < len(top) && i < 8; i++ {
		txt, _ := m68k.Disassemble(top[i].pc, func(addr uint32) uint16 {
			return uint16(c.ReadROM(addr))<<8 | uint16(c.ReadROM(addr+1))
		})
		fmt.Printf("  %08X x%-6d %s\n", top[i].pc, top[i].n, txt)
	}
	fmt.Printf("VDP regs: % X\n", v.Regs)

	zramNZ := 0
	for _, x := range b.ZRAM {
		if x != 0 {
			zramNZ++
		}
	}
	ramNZ := 0
	for _, x := range b.RAM {
		if x != 0 {
			ramNZ++
		}
	}
	fmt.Printf("ZRAM nz=%d RAM nz=%d\n", zramNZ, ramNZ)
	fmt.Printf("CRAM words 0-7: %04X %04X %04X %04X %04X %04X %04X %04X\n",
		uint16(v.CRAM[0])<<8|uint16(v.CRAM[1]), uint16(v.CRAM[2])<<8|uint16(v.CRAM[3]),
		uint16(v.CRAM[4])<<8|uint16(v.CRAM[5]), uint16(v.CRAM[6])<<8|uint16(v.CRAM[7]),
		uint16(v.CRAM[8])<<8|uint16(v.CRAM[9]), uint16(v.CRAM[10])<<8|uint16(v.CRAM[11]),
		uint16(v.CRAM[12])<<8|uint16(v.CRAM[13]), uint16(v.CRAM[14])<<8|uint16(v.CRAM[15]))

	// Dump the final framebuffer as a PNG we can look at.
	dumpPNG(v, "/tmp/gengames/frame.png")
	fmt.Println("frame dumped to /tmp/gengames/frame.png")

	fmt.Println("--- last distinct PCs before the 0x71FC4 delay loop took over:")
	seen := map[uint32]bool{}
	shown := 0
	for i := len(trail) - 1; i >= 0 && shown < 24; i-- {
		if !seen[trail[i]] {
			seen[trail[i]] = true
			shown++
		}
	}
	// print in forward order, distinct only
	seen = map[uint32]bool{}
	for _, pc := range trail {
		if seen[pc] {
			continue
		}
		seen[pc] = true
		txt, _ := m68k.Disassemble(pc, func(addr uint32) uint16 {
			return uint16(c.ReadROM(addr))<<8 | uint16(c.ReadROM(addr+1))
		})
		fmt.Printf("  %08X  %s\n", pc, txt)
	}

	fmt.Println("--- disassembly 0x71F80-0x72040:")
	for pc := uint32(0x71F80); pc < 0x72040; {
		txt, next := m68k.Disassemble(pc, func(addr uint32) uint16 {
			return uint16(c.ReadROM(addr))<<8 | uint16(c.ReadROM(addr+1))
		})
		fmt.Printf("  %08X  %s\n", pc, txt)
		if next > pc {
			pc = next
		} else if next > 0 {
			pc += next
		} else {
			pc += 2
		}
	}
}
