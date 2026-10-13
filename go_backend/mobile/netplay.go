package mobile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// npPending is the session while Start() is still blocked in the
// waiting room / handshake, so the UI can poll room state.
var npPending *netsession.Session
var npErr string

// npPlayerName is the local player's display name from the most
// recent NetplayStart, used to label our own chat messages.
var npPlayerName string

func npActive() *netsession.Session {
	if npSession != nil {
		return npSession
	}
	return npPending
}

// NetplayStart connects to the lobby signaling socket and runs the
// waiting room plus session handshake (PROTOCOL.md 4.0-4.1). It
// blocks until both players are ready to step frames, so call it
// from a background thread. Returns "" on success or an error.
func NetplayStart(serverBase, lobbyID, token, game string, isHost bool, playerName string) string {
	if emu == nil {
		return "no core loaded"
	}
	if npSession != nil || npPending != nil {
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
	npPlayerName = playerName
	sess := netsession.NewSession(conn, bridgeCore{}, netsession.Config{
		Game: game, ROMHash: romHashHex, CoreName: coreLabel,
		IsHost: isHost, PlayerName: playerName,
	})
	npPending = sess
	if err := sess.Start(); err != nil {
		_ = conn.Close()
		npPending = nil
		npErr = err.Error()
		return npErr
	}
	npPending = nil
	npErr = ""
	npSession = sess
	return ""
}

// NetplayPhase reports the waiting-room state for the UI:
// "idle", "waiting", "room", "countdown", or "playing".
func NetplayPhase() string {
	if npSession != nil {
		return "playing"
	}
	if npPending != nil {
		return npPending.RoomPhase()
	}
	return "idle"
}

// NetplayPeerName is the other player's display name.
func NetplayPeerName() string {
	if s := npActive(); s != nil {
		return s.PeerName()
	}
	return ""
}

// NetplayPeerReady reports whether the other player hit Ready.
func NetplayPeerReady() bool {
	if s := npActive(); s != nil {
		return s.PeerReady()
	}
	return false
}

// NetplayLocalReady reports whether this player hit Ready.
func NetplayLocalReady() bool {
	if s := npActive(); s != nil {
		return s.LocalReady()
	}
	return false
}

// NetplayCountdown is the seconds left on the shared countdown.
func NetplayCountdown() int {
	if s := npActive(); s != nil {
		return s.CountdownSeconds()
	}
	return 0
}

// NetplaySetReady marks this player ready in the waiting room.
func NetplaySetReady() {
	if s := npActive(); s != nil {
		s.SetReady()
	}
}

// NetplaySetPaused tells the peer this player has paused (menu
// open) or resumed. A paused session stalls without dying.
func NetplaySetPaused(paused bool) {
	if npSession != nil {
		npSession.SetPaused(paused)
	}
}

// NetplayPeerPaused reports whether the other player is paused.
func NetplayPeerPaused() bool {
	if npSession != nil {
		return npSession.PeerPaused()
	}
	return false
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

// NetplaySendChat sends a chat message to the other player
// (waiting room or in game). Returns "" on success or the error
// text. Chat is best effort and never affects the game itself.
func NetplaySendChat(text string) string {
	s := npActive()
	if s == nil {
		return "not in a session"
	}
	if err := s.SendChat(text); err != nil {
		return err.Error()
	}
	return ""
}

// chatMsgJSON and chatPollJSON are the wire shape NetplayChatJSON
// hands the UI: the messages newer than the caller's cursor plus
// the newest seq, which the UI passes back next time.
type chatMsgJSON struct {
	Seq  int    `json:"seq"`
	From string `json:"from"`
	Text string `json:"text"`
	Mine bool   `json:"mine"`
}

type chatPollJSON struct {
	Seq  int           `json:"seq"`
	Msgs []chatMsgJSON `json:"msgs"`
}

// NetplayChatJSON returns the chat messages with seq greater
// than afterSeq as JSON:
// {"seq":N,"msgs":[{"seq":..,"from":"..","text":"..","mine":bool}]}.
// With no session it returns the cursor unchanged and no messages.
func NetplayChatJSON(afterSeq int) string {
	out := chatPollJSON{Seq: afterSeq, Msgs: []chatMsgJSON{}}
	if s := npActive(); s != nil {
		msgs, latest := s.ChatSince(afterSeq)
		out.Seq = latest
		for _, m := range msgs {
			from := m.From
			if m.Mine && npPlayerName != "" {
				from = npPlayerName
			}
			out.Msgs = append(out.Msgs, chatMsgJSON{
				Seq: m.Seq, From: from, Text: m.Text, Mine: m.Mine,
			})
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return `{"seq":0,"msgs":[]}`
	}
	return string(b)
}

// NetplaySendSignal sends one voice chat signaling envelope to
// the other player (PROTOCOL.md 3.1): kind "offer" or "answer"
// with an SDP payload, or kind "ice" with a candidate payload.
// Returns "" on success or the error text. It works in the
// waiting room too, so voice can connect before the game
// starts. Signaling is best effort and never affects the game.
func NetplaySendSignal(kind, payload string) string {
	s := npActive()
	if s == nil {
		return "not in a session"
	}
	if err := s.SendSignal(kind, payload); err != nil {
		return err.Error()
	}
	return ""
}

// signalMsgJSON and signalPollJSON are the wire shape
// NetplaySignalJSON hands the UI: the signaling entries newer
// than the caller's cursor plus the newest seq, mirroring the
// chat poll shape. Empty payload fields are omitted.
type signalMsgJSON struct {
	Seq           int    `json:"seq"`
	Kind          string `json:"kind"`
	SDP           string `json:"sdp,omitempty"`
	Candidate     string `json:"candidate,omitempty"`
	SDPMid        string `json:"sdpMid,omitempty"`
	SDPMLineIndex int    `json:"sdpMLineIndex,omitempty"`
}

type signalPollJSON struct {
	Seq  int             `json:"seq"`
	Msgs []signalMsgJSON `json:"msgs"`
}

// NetplaySignalJSON returns the voice signaling entries with
// seq greater than afterSeq as JSON:
// {"seq":N,"msgs":[{"seq":..,"kind":"offer","sdp":".."}]}.
// With no session it returns the cursor unchanged and no
// messages.
func NetplaySignalJSON(afterSeq int) string {
	out := signalPollJSON{Seq: afterSeq, Msgs: []signalMsgJSON{}}
	if s := npActive(); s != nil {
		msgs, latest := s.SignalsSince(afterSeq)
		out.Seq = latest
		for _, m := range msgs {
			out.Msgs = append(out.Msgs, signalMsgJSON{
				Seq: m.Seq, Kind: m.Kind, SDP: m.SDP,
				Candidate: m.Candidate, SDPMid: m.SDPMid,
				SDPMLineIndex: m.SDPMLineIndex,
			})
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return `{"seq":0,"msgs":[]}`
	}
	return string(b)
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
	if npPending != nil {
		npPending.End("quit")
		npPending = nil
	}
	if npSession != nil {
		npSession.End("quit")
		npSession = nil
	}
}
