package lfs

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"time"
)

// SSH users get LFS credentials from `ssh git@host git-lfs-authenticate owner/repo upload|download` (D82): a short
// token, signed with auth.secret_key, good for one repository and one direction until it expires. It is not stored
// anywhere and it is not an access token: it opens the LFS endpoints of that repository only, and the person's access
// is checked again on every use, so removing them from the repository ends it at once.

const authPrefix = "codrlfs."

// SignAuth makes a token for a person, a repository and a direction.
func SignAuth(key []byte, userID, repoID int64, write bool, exp time.Time) string {
	var p [25]byte
	binary.BigEndian.PutUint64(p[0:8], uint64(userID))       // #nosec G115 -- ids are positive
	binary.BigEndian.PutUint64(p[8:16], uint64(repoID))      // #nosec G115 -- ids are positive
	binary.BigEndian.PutUint64(p[16:24], uint64(exp.Unix())) // #nosec G115 -- a future time
	if write {
		p[24] = 1
	}
	m := hmac.New(sha256.New, append([]byte("codr-lfs-ssh:"), key...))
	m.Write(p[:])
	return authPrefix + base64.RawURLEncoding.EncodeToString(p[:]) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// IsAuth reports whether a bearer value is an SSH LFS token (and not an access token).
func IsAuth(tok string) bool { return strings.HasPrefix(tok, authPrefix) }

// VerifyAuth checks a token's signature and expiry.
func VerifyAuth(key []byte, tok string, now time.Time) (userID, repoID int64, write, ok bool) {
	body, ok := strings.CutPrefix(tok, authPrefix)
	if !ok || len(key) == 0 {
		return 0, 0, false, false
	}
	pb, sb, ok := strings.Cut(body, ".")
	if !ok {
		return 0, 0, false, false
	}
	p, err1 := base64.RawURLEncoding.Strict().DecodeString(pb) // strict: one spelling per token (unused bits must be zero)
	sig, err2 := base64.RawURLEncoding.Strict().DecodeString(sb)
	if err1 != nil || err2 != nil || len(p) != 25 {
		return 0, 0, false, false
	}
	m := hmac.New(sha256.New, append([]byte("codr-lfs-ssh:"), key...))
	m.Write(p)
	if !hmac.Equal(sig, m.Sum(nil)) {
		return 0, 0, false, false
	}
	exp := int64(binary.BigEndian.Uint64(p[16:24])) // #nosec G115 -- written by SignAuth
	if now.Unix() > exp {
		return 0, 0, false, false
	}
	return int64(binary.BigEndian.Uint64(p[0:8])), int64(binary.BigEndian.Uint64(p[8:16])), p[24] == 1, true // #nosec G115 -- written by SignAuth
}
