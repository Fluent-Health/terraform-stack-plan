package ui

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SessionCookie is the browser session cookie name (also named as the
// security scheme in api/ui.openapi.yaml).
const SessionCookie = "tfsp_ui_session"

// sessionTTL bounds a login. Google identity is re-established by a silent
// redirect at expiry, so shortish is cheap.
const sessionTTL = 7 * 24 * time.Hour

// Session is the server-side state carried by the encrypted cookie — identity
// only, no tokens (the SPA never sees Google credentials, and the UI backend
// stores none).
type Session struct {
	Email   string    `json:"email"`
	Expires time.Time `json:"expires"`
}

// sessionCodec seals/opens Session values as AES-256-GCM tokens. The key is
// derived from the configured secret (SHA-256); rotating the secret
// invalidates every session. Stdlib crypto only, matching the repo's
// hand-rolled-JWT stance on auth dependencies.
type sessionCodec struct {
	aead cipher.AEAD
}

func newSessionCodec(secret string) (*sessionCodec, error) {
	if secret == "" {
		return nil, errors.New("session secret is empty")
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &sessionCodec{aead: aead}, nil
}

// seal encrypts s into a cookie-safe token: base64url(nonce || ciphertext).
func (c *sessionCodec) seal(s Session) (string, error) {
	plain, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, plain, nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// open decrypts and validates a token, rejecting expired sessions.
func (c *sessionCodec) open(token string) (Session, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Session{}, fmt.Errorf("session decode: %w", err)
	}
	if len(raw) < c.aead.NonceSize() {
		return Session{}, errors.New("session token too short")
	}
	nonce, ct := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	plain, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return Session{}, fmt.Errorf("session open: %w", err)
	}
	var s Session
	if err := json.Unmarshal(plain, &s); err != nil {
		return Session{}, err
	}
	if time.Now().After(s.Expires) {
		return Session{}, errors.New("session expired")
	}
	return s, nil
}
