// Package auth hashes and verifies web passwords with argon2id (D8: passwords are for
// the web only; git over SSH uses keys and git over HTTPS uses tokens).
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are the argon2id cost parameters, taken from config.toml [auth].
type Params struct {
	Time    uint32
	Memory  uint32 // KiB
	Threads uint8
}

const (
	saltLen = 16
	keyLen  = 32
)

// ErrMismatch is returned by Verify when the password is wrong.
var ErrMismatch = errors.New("password mismatch")

// Hash returns a PHC-formatted string: $argon2id$v=19$m=..,t=..,p=..$salt$hash.
// The parameters travel with the hash so cost can be raised later without a migration.
func Hash(password string, p Params) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, keyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads,
		enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// Verify checks password against a Hash output in constant time.
func Verify(password, encoded string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return errors.New("unsupported hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return errors.New("unsupported argon2 version")
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return errors.New("bad hash parameters")
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return err
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil {
		return err
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(want))) // #nosec G115 -- key length is 32
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

// NeedsRehash reports whether a stored hash uses weaker parameters than p.
func NeedsRehash(encoded string, p Params) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 {
		return true
	}
	var q Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &q.Memory, &q.Time, &q.Threads); err != nil {
		return true
	}
	return q.Memory < p.Memory || q.Time < p.Time
}
