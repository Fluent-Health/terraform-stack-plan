// Command goseal seals values the way internal/ui/session.go does (AES-256-GCM under
// SHA-256 of the secret), so the replay can hand both implementations the same
// Go-sealed session cookies and approval states. The leading underscore keeps it out of
// the repository's `go ./...` builds.
//
//	go run main.go <secret> session <email> <RFC3339 expiry>
//	go run main.go <secret> json '<raw JSON>'
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	key := sha256.Sum256([]byte(os.Args[1]))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	var plain []byte
	switch os.Args[2] {
	case "session":
		exp, err := time.Parse(time.RFC3339, os.Args[4])
		if err != nil {
			panic(err)
		}
		plain, _ = json.Marshal(struct {
			Email   string    `json:"email"`
			Expires time.Time `json:"expires"`
		}{os.Args[3], exp})
	case "json":
		plain = []byte(os.Args[3])
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	fmt.Print(base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, nil)))
}
