package auth

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B, SHA1 rows (8 digits there; the last 6 digits are the 6-digit code).
func TestTOTPRFCVectors(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	for unix, want8 := range map[int64]string{59: "94287082", 1111111109: "07081804", 1111111111: "14050471", 1234567890: "89005924", 2000000000: "69279037", 20000000000: "65353130"} {
		got, err := TOTPCode(secret, TOTPStep(time.Unix(unix, 0)))
		if err != nil || got != want8[2:] {
			t.Errorf("t=%d: %s want %s (%v)", unix, got, want8[2:], err)
		}
	}
}

func TestVerifyTOTP(t *testing.T) {
	sec, _ := NewTOTPSecret()
	now := time.Unix(1_800_000_000, 0)
	code, _ := TOTPCode(sec, TOTPStep(now))
	st, ok := VerifyTOTP(sec, code, now, 1, 0)
	if !ok || st != TOTPStep(now) {
		t.Fatal("current code")
	}
	if _, ok := VerifyTOTP(sec, code, now, 1, st); ok {
		t.Fatal("replayed code accepted")
	}
	prev, _ := TOTPCode(sec, TOTPStep(now)-1)
	if _, ok := VerifyTOTP(sec, prev, now, 1, 0); !ok {
		t.Fatal("one step of clock skew refused")
	}
	old, _ := TOTPCode(sec, TOTPStep(now)-3)
	if _, ok := VerifyTOTP(sec, old, now, 1, 0); ok {
		t.Fatal("a code three steps old accepted")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := VerifyTOTP(sec, bad, now, 1, 0); ok {
			t.Fatalf("%q accepted", bad)
		}
	}
	if u := TOTPURI("Codr", "aiman", sec); !strings.HasPrefix(u, "otpauth://totp/Codr:aiman?") || !strings.Contains(u, "secret="+sec) {
		t.Fatal(u)
	}
	r, _ := NewRecoveryCode()
	if len(r) != 11 || r[5] != '-' || NormalizeRecovery(" "+strings.ToUpper(r)+" ") != strings.ReplaceAll(r, "-", "") {
		t.Fatal(r)
	}
}
