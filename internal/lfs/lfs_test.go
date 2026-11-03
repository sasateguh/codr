package lfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func oidOf(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestPutVerifyLink(t *testing.T) {
	s := Store{Root: t.TempDir()}
	data := "isi fail besar"
	oid := oidOf(data)
	if err := s.Put(1, oid, int64(len(data)), 0, strings.NewReader("lain")); !errors.Is(err, ErrMismatch) {
		t.Fatalf("wrong content accepted: %v", err)
	}
	if err := s.Put(1, oid, 3, 0, strings.NewReader(data)); !errors.Is(err, ErrMismatch) {
		t.Fatalf("wrong size accepted: %v", err)
	}
	if err := s.Put(1, oid, -1, 4, strings.NewReader(data)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("limit: %v", err)
	}
	if _, ok := s.Stat(1, oid); ok {
		t.Fatal("a refused upload left an object")
	}
	if left, _ := os.ReadDir(s.Root + "/.tmp"); len(left) != 0 {
		t.Fatal("temporary files left behind")
	}
	if err := s.Put(1, oid, int64(len(data)), 0, strings.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	f, _ := s.Open(1, oid)
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != data {
		t.Fatal("content")
	}
	if _, err := s.Open(1, "../../etc/passwd"); err == nil {
		t.Fatal("path traversal")
	}
	n, err := s.LinkAll(1, s, 2)
	if err != nil || n != 1 {
		t.Fatalf("link: %d %v", n, err)
	}
	if c, by, _ := s.Usage(2); c != 1 || by != int64(len(data)) {
		t.Fatal("usage")
	}
	if bad, err := s.Verify(2); err != nil || len(bad) != 0 {
		t.Fatalf("verify: %v %v", bad, err)
	}
	_ = os.WriteFile(s.Path(2, oid), []byte("rosak"), 0o600) // hard link: corrupting one shows in both, which is why objects are never edited
	if bad, _ := s.Verify(1); len(bad) != 1 {
		t.Fatal("corruption not found")
	}
	ids, _ := s.Repos()
	if len(ids) != 2 {
		t.Fatalf("repos: %v", ids)
	}
}

func TestPointer(t *testing.T) {
	oid := oidOf("x")
	p, ok := ParsePointer([]byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "\nsize 12345\n"))
	if !ok || p.OID != oid || p.Size != 12345 {
		t.Fatalf("%+v %v", p, ok)
	}
	for _, bad := range []string{"hello", "version https://git-lfs.github.com/spec/v1\noid sha1:abc\nsize 1\n", "version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "\nsize -1\n"} {
		if _, ok := ParsePointer([]byte(bad)); ok {
			t.Fatalf("%q parsed", bad)
		}
	}
}

func TestSSHAuth(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Now()
	tok := SignAuth(key, 7, 42, true, now.Add(time.Hour))
	if !IsAuth(tok) {
		t.Fatal("prefix")
	}
	u, r, w, ok := VerifyAuth(key, tok, now)
	if !ok || u != 7 || r != 42 || !w {
		t.Fatal("verify")
	}
	if _, _, _, ok := VerifyAuth(key, tok, now.Add(2*time.Hour)); ok {
		t.Fatal("expired token accepted")
	}
	if _, _, _, ok := VerifyAuth([]byte("another key another key another k"), tok, now); ok {
		t.Fatal("wrong key accepted")
	}
	forged := SignAuth(key, 7, 42, false, now.Add(time.Hour))
	if _, _, w, _ := VerifyAuth(key, forged, now); w {
		t.Fatal("download token says write")
	}
	for k := 0; k < 64; k++ { // many tokens (the last base64 character's spare bits differ with the payload)
		tk := SignAuth(key, 7, 42, true, now.Add(time.Duration(k)*time.Minute+time.Hour))
		for i := len(authPrefix); i < len(tk); i++ {
			if tk[i] == '.' {
				continue
			}
			for _, c := range []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") {
				if c == tk[i] {
					continue
				}
				if _, _, _, ok := VerifyAuth(key, tk[:i]+string(c)+tk[i+1:], now); ok {
					t.Fatalf("token %d changed at %d to %c still verifies", k, i, c)
				}
			}
		}
	}
	for i := len(authPrefix); i < len(tok); i++ { // change every single character in turn: none may still verify
		if tok[i] == '.' {
			continue
		}
		c := byte('A')
		if tok[i] == 'A' {
			c = 'B'
		}
		if _, _, _, ok := VerifyAuth(key, tok[:i]+string(c)+tok[i+1:], now); ok {
			t.Fatalf("token changed at %d still verifies", i)
		}
	}
}
