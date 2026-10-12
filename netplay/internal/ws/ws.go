// Package ws is a minimal server-side WebSocket (RFC 6455)
// implementation built only on the standard library. It covers what the
// netplay signaling relay needs: upgrade handshake, text/binary frames
// with fragmentation, ping/pong, close, and a hard message size cap.
// It is not a general-purpose WebSocket library.
package ws

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Frame opcodes.
const (
	OpContinuation = 0
	OpText         = 1
	OpBinary       = 2
	OpClose        = 8
	OpPing         = 9
	OpPong         = 10
)

// Close codes used by the relay.
const (
	CloseNormal    = 1000
	CloseGoingAway = 1001
	CloseTooBig    = 1009
	CloseInternal  = 1011
)

var (
	// ErrClosed is returned by ReadMessage after a close frame.
	ErrClosed = errors.New("ws: connection closed")
	// ErrTooBig is returned when a message exceeds the size cap.
	ErrTooBig = errors.New("ws: message too big")
)

const magicGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// DefaultMaxMessage caps a single (reassembled) message.
const DefaultMaxMessage = 256 << 10 // 256 KB

// Conn is one upgraded WebSocket connection. ReadMessage must be called
// from a single goroutine; WriteMessage is safe for concurrent use.
type Conn struct {
	nc         net.Conn
	br         *bufio.Reader
	wmu        sync.Mutex
	closeOnce  sync.Once
	maxMessage int

	// OnPong, if set, runs when a pong arrives. The server uses it to
	// extend the read deadline as proof of life.
	OnPong func()
}

// Upgrade hijacks w's connection and performs the server handshake.
// On failure it writes an HTTP error and returns a non-nil error.
func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !headerHasToken(r.Header, "Connection", "upgrade") ||
		!headerHasToken(r.Header, "Upgrade", "websocket") {
		http.Error(w, "not a websocket upgrade", http.StatusBadRequest)
		return nil, errors.New("ws: missing upgrade headers")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return nil, errors.New("ws: missing key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return nil, errors.New("ws: hijacking unsupported")
	}
	nc, brw, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("ws: hijack: %w", err)
	}

	sum := sha1.Sum([]byte(key + magicGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	_, err = fmt.Fprintf(brw,
		"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		accept)
	if err == nil {
		err = brw.Flush()
	}
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("ws: handshake write: %w", err)
	}
	return &Conn{nc: nc, br: brw.Reader, maxMessage: DefaultMaxMessage}, nil
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// SetReadDeadline passes through to the underlying connection.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.nc.SetReadDeadline(t) }

// RemoteAddr is the peer's network address.
func (c *Conn) RemoteAddr() net.Addr { return c.nc.RemoteAddr() }

// ReadMessage returns the next complete text or binary message.
// Ping frames are answered with pong automatically, pongs trigger
// OnPong, and a close frame ends the read with ErrClosed.
func (c *Conn) ReadMessage() (int, []byte, error) {
	var msgOp int
	var buf []byte
	for {
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case OpPing:
			_ = c.WriteMessage(OpPong, payload)
			continue
		case OpPong:
			if c.OnPong != nil {
				c.OnPong()
			}
			continue
		case OpClose:
			c.Close()
			return 0, nil, ErrClosed
		case OpText, OpBinary:
			if msgOp != 0 {
				c.Close()
				return 0, nil, errors.New("ws: new message before final frame")
			}
			msgOp = op
		case OpContinuation:
			if msgOp == 0 {
				c.Close()
				return 0, nil, errors.New("ws: unexpected continuation frame")
			}
		default:
			c.Close()
			return 0, nil, fmt.Errorf("ws: unknown opcode %d", op)
		}
		buf = append(buf, payload...)
		if len(buf) > c.maxMessage {
			_ = c.WriteClose(CloseTooBig, "message too big")
			c.Close()
			return 0, nil, ErrTooBig
		}
		if fin {
			return msgOp, buf, nil
		}
	}
}

func (c *Conn) readFrame() (bool, int, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		return false, 0, nil, err
	}
	fin := hdr[0]&0x80 != 0
	op := int(hdr[0] & 0x0f)
	masked := hdr[1]&0x80 != 0
	length := uint64(hdr[1] & 0x7f)

	switch length {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(b[:])
	}
	// A single frame larger than the message cap can never reassemble
	// into a legal message; fail fast instead of allocating for it.
	if length > uint64(c.maxMessage) {
		return false, 0, nil, ErrTooBig
	}

	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, op, payload, nil
}

// WriteMessage sends one unfragmented frame.
func (c *Conn) WriteMessage(op int, data []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()

	var hdr []byte
	switch n := len(data); {
	case n < 126:
		hdr = []byte{0x80 | byte(op), byte(n)}
	case n <= 0xffff:
		hdr = make([]byte, 4)
		hdr[0] = 0x80 | byte(op)
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:], uint16(n))
	default:
		hdr = make([]byte, 10)
		hdr[0] = 0x80 | byte(op)
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
	}
	if _, err := c.nc.Write(hdr); err != nil {
		return err
	}
	_, err := c.nc.Write(data)
	return err
}

// WriteClose sends a close frame with the given code (best effort).
func (c *Conn) WriteClose(code int, reason string) error {
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload, uint16(code))
	copy(payload[2:], reason)
	return c.WriteMessage(OpClose, payload)
}

// Close shuts the underlying connection. Safe to call more than once.
func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() { err = c.nc.Close() })
	return err
}
