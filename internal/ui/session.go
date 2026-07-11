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

// sealJSON encrypts any JSON-marshalable value into a URL/cookie-safe token:
// base64url(nonce || ciphertext). Used for the session cookie and for the
// approval intent riding the OAuth state parameter.
func (c *sessionCodec) sealJSON(v any) (string, error) {
	plain, err := json.Marshal(v)
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

// openJSON decrypts a token into out (authenticity from the AEAD seal;
// expiry is the payload's own concern).
func (c *sessionCodec) openJSON(token string, out any) error {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return fmt.Errorf("token decode: %w", err)
	}
	if len(raw) < c.aead.NonceSize() {
		return errors.New("token too short")
	}
	nonce, ct := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	plain, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return fmt.Errorf("token open: %w", err)
	}
	return json.Unmarshal(plain, out)
}

// seal encrypts a Session into a cookie value.
func (c *sessionCodec) seal(s Session) (string, error) {
	return c.sealJSON(s)
}

// open decrypts and validates a session token, rejecting expired sessions.
func (c *sessionCodec) open(token string) (Session, error) {
	var s Session
	if err := c.openJSON(token, &s); err != nil {
		return Session{}, fmt.Errorf("session: %w", err)
	}
	if time.Now().After(s.Expires) {
		return Session{}, errors.New("session expired")
	}
	return s, nil
}
