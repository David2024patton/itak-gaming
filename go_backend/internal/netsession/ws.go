// Package netsession implements the client side of the iTaK netplay
// protocol (see netplay/PROTOCOL.md): a minimal WebSocket client plus
// a deterministic lockstep session that keeps two emulator cores in
// sync by exchanging per-frame button states.
package netsession

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Minimal RFC 6455 WebSocket client: just enough for the lobby
// server's signaling socket. Text and binary frames, client masking,
// ping/pong, close. No extensions, no TLS (the LAN server is plain
// ws; production wss can ride a proxy).

type WSConn struct {
	conn   net.Conn
	rd     *bufio.Reader
	wmu    sync.Mutex
	closed bool
}

func DialWS(rawURL string, timeout time.Duration) (*WSConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "ws" {
		return nil, fmt.Errorf("unsupported scheme %q (ws only)", u.Scheme)
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.Dial("tcp", host)
	if err != nil {
		return nil, err
	}
	keyRaw := make([]byte, 16)
	if _, err := rand.Read(keyRaw); err != nil {
		conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyRaw)
	path := u.RequestURI()
	var b strings.Builder
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\n", path)
	fmt.Fprintf(&b, "Host: %s\r\n", u.Host)
	b.WriteString("Upgrade: websocket\r\n")
	b.WriteString("Connection: Upgrade\r\n")
	fmt.Fprintf(&b, "Sec-WebSocket-Key: %s\r\n", key)
	b.WriteString("Sec-WebSocket-Version: 13\r\n")
	b.WriteString("User-Agent: iTaK-Gaming/1.0\r\n\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		conn.Close()
		return nil, err
	}
	rd := bufio.NewReader(conn)
	status, err := rd.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !strings.Contains(status, "101") {
		conn.Close()
		return nil, fmt.Errorf("websocket upgrade refused: %s", strings.TrimSpace(status))
	}
	// Read headers; verify the accept key.
	want := wsAccept(key)
	sawAccept := false
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "sec-websocket-accept:") {
			got := strings.TrimSpace(line[len("sec-websocket-accept:"):])
			if got == want {
				sawAccept = true
			}
		}
	}
	if !sawAccept {
		conn.Close()
		return nil, errors.New("websocket accept key mismatch")
	}
	return &WSConn{conn: conn, rd: rd}, nil
}

func wsAccept(key string) string {
	h := sha1.New()
	io.WriteString(h, key+"258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// ReadMessage returns the next text or binary payload. Control frames
// are handled internally (ping -> pong). A close frame or any I/O
// error ends the connection and is reported as an error.
func (c *WSConn) ReadMessage() ([]byte, error) {
	for {
		payload, opcode, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case 0x1, 0x2: // text, binary
			return payload, nil
		case 0x0: // continuation: our peers never fragment
			continue
		case 0x9: // ping
			if err := c.writeFrame(0xA, payload); err != nil {
				return nil, err
			}
		case 0xA: // pong
			continue
		case 0x8: // close
			c.Close()
			return nil, io.EOF
		}
	}
}

func (c *WSConn) readFrame() ([]byte, byte, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c.rd, hdr); err != nil {
		return nil, 0, err
	}
	opcode := hdr[0] & 0x0F
	masked := hdr[1]&0x80 != 0
	length := uint64(hdr[1] & 0x7F)
	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(c.rd, ext); err != nil {
			return nil, 0, err
		}
		length = uint64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(c.rd, ext); err != nil {
			return nil, 0, err
		}
		length = binary.BigEndian.Uint64(ext)
	}
	var maskKey []byte
	if masked {
		maskKey = make([]byte, 4)
		if _, err := io.ReadFull(c.rd, maskKey); err != nil {
			return nil, 0, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.rd, payload); err != nil {
		return nil, 0, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return payload, opcode, nil
}

// WriteText sends one text frame (masked, as required of clients).
func (c *WSConn) WriteText(text string) error {
	return c.writeFrame(0x1, []byte(text))
}

func (c *WSConn) writeFrame(opcode byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return io.ErrClosedPipe
	}
	var hdr []byte
	hdr = append(hdr, 0x80|opcode)
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n < 65536:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		hdr = append(hdr, ext[:]...)
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	hdr = append(hdr, mask...)
	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := c.conn.Write(hdr); err != nil {
		return err
	}
	_, err := c.conn.Write(masked)
	return err
}

func (c *WSConn) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}

func (c *WSConn) Close() error {
	c.wmu.Lock()
	if c.closed {
		c.wmu.Unlock()
		return nil
	}
	c.closed = true
	c.wmu.Unlock()
	return c.conn.Close()
}
