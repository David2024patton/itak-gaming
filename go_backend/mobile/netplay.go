package mobile

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/davidpatton/itak-gaming/go_backend/internal/netsession"
)

// Netplay wiring: the lockstep session from internal/netsession,
// driven over the shared emulator bridge. Kotlin starts a session
// after loading the game, then calls NetplayStep once per frame
// instead of RunFrame while it is active.

// romHashHex is the SHA-256 of the loaded ROM, computed at LoadGame
// time; both peers must present identical hashes to start.
var romHashHex string
var coreLabel string

func noteLoadedGame(path string) {
	romHashHex = ""
	if data, err := os.ReadFile(path); err == nil {
		sum := sha256.Sum256(data)
		romHashHex = hex.EncodeToString(sum[:])
	}
}

func noteLoadedCore(path string) {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".so")
	base = strings.TrimPrefix(base, "lib")
	base = strings.TrimSuffix(base, "_libretro")
	coreLabel = base
}

// bridgeCore adapts the singleton bridge to netsession.Core.
type bridgeCore struct{}

func (bridgeCore) RunFrame() { emu.RunFrame() }

func (bridgeCore) SetButtons(player int, mask uint16) {
	for b := 0; b < 16; b++ {
		emu.SetButton(player, b, mask&(1<<uint(b)) != 0)
	}
}

func (bridgeCore) SaveState() []byte {
	data, err := emu.SaveState()
	if err != nil {
		return nil
	}
	return data
}

func (bridgeCore) LoadState(data []byte) error { return emu.LoadState(data) }

var npSession *netsession.Session
var npErr string

// NetplayStart connects to the lobby signaling socket and runs the
// session handshake (PROTOCOL.md 4.1). It blocks until both players
// are ready to step frames, so call it from a background thread.
// Returns "" on success or an error message.
func NetplayStart(serverBase, lobbyID, token, game string, isHost bool) string {
	if emu == nil {
		return "no core loaded"
	}
	if npSession != nil {
		return "a session is already running"
	}
	wsBase := strings.Replace(serverBase, "http://", "ws://", 1)
	wsBase = strings.Replace(wsBase, "https://", "wss://", 1)
	wsURL := wsBase + "/ws?lobby=" + url.QueryEscape(lobbyID) +
		"&token=" + url.QueryEscape(token)
	conn, err := netsession.DialWS(wsURL, 10*time.Second)
	if err != nil {
		return "cannot connect to the lobby server: " + err.Error()
	}
	sess := netsession.NewSession(conn, bridgeCore{}, netsession.Config{
		Game: game, ROMHash: romHashHex, CoreName: coreLabel, IsHost: isHost,
	})
	if err := sess.Start(); err != nil {
		_ = conn.Close()
		npErr = err.Error()
		return npErr
	}
	npErr = ""
	npSession = sess
	return ""
}

// NetplayStep simulates one frame with the local player's current
// button mask, blocking while the peer's input is in flight.
// Returns "" or the error that ended the session.
func NetplayStep(buttons int) string {
	if npSession == nil {
		return "no netplay session"
	}
	if err := npSession.Step(uint16(buttons)); err != nil {
		npErr = err.Error()
		return npErr
	}
	return ""
}

// NetplayActive reports whether a session is live.
func NetplayActive() bool { return npSession != nil }

// NetplayFrame returns the session's next frame number.
func NetplayFrame() int {
	if npSession == nil {
		return 0
	}
	return npSession.Frame()
}

// NetplayError returns the last session error, if any.
func NetplayError() string { return npErr }

// NetplayStop ends the session politely.
func NetplayStop() {
	if npSession != nil {
		npSession.End("quit")
		npSession = nil
	}
}
