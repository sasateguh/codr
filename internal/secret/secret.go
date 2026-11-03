// Package secret seals small values (mirror tokens) with AES-256-GCM under the key in
// config.toml (auth.secret_key, PRD §9): a copy of the database alone never yields a token.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
)

// ErrNoKey means config has no secret_key (older installs: see doctor).
var ErrNoKey = errors.New("secret: no key configured")

// Key is a 32-byte AES key.
type Key []byte

// Parse decodes the hex key from config; "" yields a nil key (sealing refused).
func Parse(hexKey string) (Key, error) {
	if hexKey == "" {
		return nil, nil
	}
	b, err := hex.DecodeString(hexKey)
	if err != nil || len(b) != 32 {
		return nil, errors.New("secret: key must be 64 hex characters")
	}
	return Key(b), nil
}

// Generate makes a fresh key as hex for config.toml.
func Generate() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Seal encrypts plaintext; output is base64(nonce||ciphertext). Empty plaintext seals to "".
func (k Key) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if k == nil {
		return "", ErrNoKey
	}
	g, err := gcm(k)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(g.Seal(nonce, nonce, []byte(plain), nil)), nil
}

// Open decrypts what Seal produced.
func (k Key) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if k == nil {
		return "", ErrNoKey
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	g, err := gcm(k)
	if err != nil {
		return "", err
	}
	if len(raw) < g.NonceSize() {
		return "", errors.New("secret: sealed value too short")
	}
	out, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], nil)
	if err != nil {
		return "", errors.New("secret: cannot open (wrong key?)")
	}
	return string(out), nil
}

func gcm(k Key) (cipher.AEAD, error) {
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
