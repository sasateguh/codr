package secret

import "testing"

func TestSealOpen(t *testing.T) {
	h, err := Generate()
	if err != nil || len(h) != 64 {
		t.Fatal(err, h)
	}
	k, err := Parse(h)
	if err != nil {
		t.Fatal(err)
	}
	s, err := k.Seal("ghp_token")
	if err != nil || s == "" || s == "ghp_token" {
		t.Fatalf("seal: %v %q", err, s)
	}
	if got, err := k.Open(s); err != nil || got != "ghp_token" {
		t.Fatalf("open: %v %q", err, got)
	}
	s2, _ := k.Seal("ghp_token")
	if s2 == s {
		t.Fatal("nonce reused")
	}
	other, _ := Parse(func() string { x, _ := Generate(); return x }())
	if _, err := other.Open(s); err == nil {
		t.Fatal("wrong key opened")
	}
	if _, err := Parse("abc"); err == nil {
		t.Fatal("short key accepted")
	}
	var none Key
	if _, err := none.Seal("x"); err != ErrNoKey {
		t.Fatal("sealing without key must fail")
	}
	if v, err := none.Seal(""); err != nil || v != "" {
		t.Fatal("empty plaintext needs no key")
	}
}
