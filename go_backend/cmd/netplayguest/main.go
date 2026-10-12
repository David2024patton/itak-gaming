// netplayguest is a headless netplay test client: it joins a lobby
// as the guest, runs the lockstep session against the real core and
// ROM, presses Start at a scripted frame, and reports the final
// state hash. It exists to prove box-to-box sync without needing a
// second TV: the TV hosts, this guest plays.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/davidpatton/itak-gaming/go_backend/internal/core"
	"github.com/davidpatton/itak-gaming/go_backend/internal/netsession"
)

type adapter struct{ b *core.Bridge }

func (a adapter) RunFrame() { a.b.RunFrame() }

func (a adapter) SetButtons(player int, mask uint16) {
	for i := 0; i < 16; i++ {
		a.b.SetButton(player, i, mask&(1<<uint(i)) != 0)
	}
}

func (a adapter) SaveState() []byte {
	data, err := a.b.SaveState()
	if err != nil {
		return nil
	}
	return data
}

func (a adapter) LoadState(d []byte) error { return a.b.LoadState(d) }

func httpJSON(method, url string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s: %s", resp.Status, data)
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func main() {
	server := flag.String("server", "http://127.0.0.1:8790", "lobby server base URL")
	corePath := flag.String("core", "", "libretro core .so path")
	romPath := flag.String("rom", "", "ROM path")
	lobbyID := flag.String("lobby", "", "lobby id (default: first open lobby)")
	asHost := flag.Bool("host", false, "create the lobby and play as host (player 1)")
	givenToken := flag.String("token", "", "skip lobby create/join; use this token with -lobby")
	frames := flag.Int("frames", 3600, "frames to simulate")
	startAt := flag.Int("start-at", 400, "frame to start pressing Start")
	pressFor := flag.Int("press-for", 12, "frames to hold Start")
	name := flag.String("name", "", "player name for the waiting room (default TestHost/TestGuest)")
	readyDelay := flag.Int("ready-delay", -1, "seconds before pressing Ready in the room; -1 readies instantly")
	pauseAt := flag.Int("pause-at", -1, "frame at which to signal a pause (as a player opening their menu would)")
	pauseSecs := flag.Int("pause-secs", 20, "how long the -pause-at pause lasts")
	flag.Parse()
	if *name == "" {
		if *asHost {
			*name = "TestHost"
		} else {
			*name = "TestGuest"
		}
	}
	if *corePath == "" || *romPath == "" {
		fmt.Println("need -core and -rom")
		os.Exit(2)
	}

	token := *givenToken
	if token != "" {
		// Token supplied: straight to the session.
	} else if *asHost {
		var created struct {
			ID        string `json:"id"`
			HostToken string `json:"hostToken"`
		}
		gameName := filepath.Base(*romPath)
		if err := httpJSON("POST", *server+"/lobbies",
			map[string]any{"name": "TestHost lobby", "game": gameName,
				"hostPlayer": *name, "password": ""}, &created); err != nil {
			fmt.Println("create:", err)
			os.Exit(1)
		}
		*lobbyID = created.ID
		token = created.HostToken
		fmt.Println("hosting lobby", created.ID)
	}
	// Find and join the lobby.
	var list struct {
		Lobbies []struct {
			ID   string `json:"id"`
			Game string `json:"game"`
		} `json:"lobbies"`
	}
	if err := httpJSON("GET", *server+"/lobbies", nil, &list); err != nil {
		fmt.Println("list lobbies:", err)
		os.Exit(1)
	}
	id := *lobbyID
	if !*asHost {
		// guest path continues below
	} else {
		goto joined
	}
	if id == "" {
		if len(list.Lobbies) == 0 {
			fmt.Println("no open lobbies")
			os.Exit(1)
		}
		id = list.Lobbies[0].ID
	}
	{
		var join struct {
			GuestToken string `json:"guestToken"`
		}
		if err := httpJSON("POST", *server+"/lobbies/"+id+"/join",
			map[string]any{"player": *name, "password": ""}, &join); err != nil {
			fmt.Println("join:", err)
			os.Exit(1)
		}
		token = join.GuestToken
		fmt.Println("joined lobby", id)
	}
joined:

	bridge, err := core.LoadCore(*corePath)
	if err != nil {
		fmt.Println("load core:", err)
		os.Exit(1)
	}
	if err := bridge.LoadGame(*romPath); err != nil {
		fmt.Println("load game:", err)
		os.Exit(1)
	}
	romBytes, err := os.ReadFile(*romPath)
	if err != nil {
		fmt.Println("read rom:", err)
		os.Exit(1)
	}
	sum := sha256.Sum256(romBytes)

	wsBase := "ws" + (*server)[4:]
	conn, err := netsession.DialWS(wsBase+"/ws?lobby="+id+"&token="+token, 10*time.Second)
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	sess := netsession.NewSession(conn, adapter{bridge}, netsession.Config{
		Game:       filepath.Base(*romPath),
		ROMHash:    hex.EncodeToString(sum[:]),
		IsHost:     *asHost,
		PlayerName: *name,
		ReadyNow:   *readyDelay < 0,
	})
	if *readyDelay >= 0 {
		go func() {
			time.Sleep(time.Duration(*readyDelay) * time.Second)
			sess.SetReady()
			fmt.Println("pressed Ready in the room")
		}()
	}
	t0 := time.Now()
	if err := sess.Start(); err != nil {
		fmt.Println("session start:", err)
		os.Exit(1)
	}
	fmt.Println("session started, stepping", *frames, "frames")
	const btnStart = uint16(1) << 3
	for f := 0; f < *frames; f++ {
		if f == *pauseAt {
			fmt.Printf("pausing for %ds at frame %d (menu open)\n",
				*pauseSecs, f)
			sess.SetPaused(true)
			time.Sleep(time.Duration(*pauseSecs) * time.Second)
			sess.SetPaused(false)
			fmt.Println("resumed from pause")
		}
		var mask uint16
		if f >= *startAt && f < *startAt+*pressFor {
			mask = btnStart
		}
		if *asHost {
			// Drive the menus: confirm file, pick 1 player, skip story.
			for _, at := range []int{1100, 1300, 1550, 1750} {
				if f >= at && f < at+*pressFor {
					mask = btnStart
				}
			}
		}
		if err := sess.Step(mask); err != nil {
			fmt.Printf("step %d: %v\n", f, err)
			os.Exit(1)
		}
		if (f+1)%600 == 0 {
			fmt.Printf("frame %d (%.1fs wall)\n", f+1, time.Since(t0).Seconds())
		}
	}
	state, err := bridge.SaveState()
	if err != nil {
		fmt.Println("save state:", err)
		os.Exit(1)
	}
	finalSum := sha256.Sum256(state)
	fmt.Printf("DONE frames=%d desyncs=%d state_sha256=%s\n",
		sess.Frame(), sess.Desyncs(), hex.EncodeToString(finalSum[:]))
	sess.End("test complete")
}
