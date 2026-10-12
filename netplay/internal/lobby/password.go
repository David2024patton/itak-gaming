package lobby

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// Passwords are never stored. A lobby keeps a random 16-byte salt and
// sha256(salt || password); verification is constant-time. Bearer tokens
// get the same treatment: only their SHA-256 digest is stored, so a
// memory dump of the store does not hand out live tokens.

const (
	saltBytes  = 16
	tokenBytes = 32 // 256-bit tokens, hex encoded on the wire
	idBytes    = 5  // lobby id entropy before encoding
)

// idAlphabet avoids look-alike characters (0/O, 1/I/L) so codes can be
// read off a TV screen and typed on another one.
const idAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// newLobbyID returns a short human-friendly lobby code.
func newLobbyID() (string, error) {
	raw, err := randomBytes(idBytes)
	if err != nil {
		return "", err
	}
	out := make([]byte, len(raw))
	for i, b := range raw {
		out[i] = idAlphabet[int(b)%len(idAlphabet)]
	}
	return string(out), nil
}

// newToken returns a fresh bearer token (hex) for a lobby member.
func newToken() (string, error) {
	raw, err := randomBytes(tokenBytes)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// tokenDigest is the stored form of a bearer token.
func tokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func hashPassword(password string, salt []byte) []byte {
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(password))
	return h.Sum(nil)
}

func checkPassword(password string, salt, want []byte) bool {
	got := hashPassword(password, salt)
	return subtle.ConstantTimeCompare(got, want) == 1
}
