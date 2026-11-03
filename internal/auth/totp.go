package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- RFC 6238 TOTP is defined on HMAC-SHA1 and every authenticator app speaks it
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TOTP (RFC 6238) for two-step sign-in (D80): 30-second steps, 6 digits, HMAC-SHA1 — the parameters every
// authenticator app accepts without asking. Secrets are 160 random bits, shown as unpadded base32.

const (
	totpPeriod = 30
	totpDigits = 6
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a fresh secret as base32.
func NewTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

// TOTPStep is the time step a moment falls in.
func TOTPStep(t time.Time) int64 { return t.Unix() / totpPeriod }

// TOTPCode is the code for one time step.
func TOTPCode(secret string, step int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step)) // #nosec G115 -- a Unix time step is positive
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	s := strconv.FormatUint(uint64(v%1000000), 10)
	return strings.Repeat("0", totpDigits-len(s)) + s, nil
}

// VerifyTOTP checks a code against the steps around now (±skew) and returns the step it matched. A step at or
// before lastStep is refused, so a code that already signed someone in cannot be replayed.
func VerifyTOTP(secret, code string, now time.Time, skew int, lastStep int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	cur := TOTPStep(now)
	for d := -skew; d <= skew; d++ {
		st := cur + int64(d)
		if st <= lastStep {
			continue
		}
		want, err := TOTPCode(secret, st)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return st, true
		}
	}
	return 0, false
}

// TOTPURI is the otpauth:// link an authenticator app reads from the QR code.
func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {strconv.Itoa(totpDigits)}, "period": {strconv.Itoa(totpPeriod)}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// NewRecoveryCode returns one recovery code: 10 base32 characters in two groups ("abcde-fghij"), 50 random bits.
func NewRecoveryCode() (string, error) {
	b := make([]byte, 7)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := strings.ToLower(b32.EncodeToString(b))[:10]
	return s[:5] + "-" + s[5:], nil
}

// NormalizeRecovery makes a typed recovery code comparable: lower case, no spaces or dashes.
func NormalizeRecovery(s string) string {
	return strings.NewReplacer("-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(s)))
}
