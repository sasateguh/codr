package cloud

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
)

func seal(t *testing.T, key, plain []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := Seal(&b, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestSealRoundTrip: sizes around the chunk boundary come back byte for byte; damage, truncation, reordering
// and a wrong key are all refused.
func TestSealRoundTrip(t *testing.T) {
	key, _ := ParseRecoveryKey("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	for _, n := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 3*chunkSize + 17} {
		plain := make([]byte, n)
		_, _ = rand.Read(plain)
		ct := seal(t, key, plain)
		r, err := Open(bytes.NewReader(ct), key)
		if err != nil {
			t.Fatalf("%d: open: %v", n, err)
		}
		got, err := io.ReadAll(r)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("%d: round trip failed: %v", n, err)
		}
	}
	plain := bytes.Repeat([]byte("codr "), chunkSize) // 5 chunks
	ct := seal(t, key, plain)
	readAll := func(b []byte, k []byte) error {
		r, err := Open(bytes.NewReader(b), k)
		if err != nil {
			return err
		}
		_, err = io.ReadAll(r)
		return err
	}
	edited := append([]byte(nil), ct...)
	edited[len(magic)+prefixLen+100] ^= 1
	other, _ := ParseRecoveryKey("ff112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	chunk := chunkSize + 16
	swapped := append([]byte(nil), ct...)
	head := len(magic) + prefixLen
	copy(swapped[head:head+chunk], ct[head+chunk:head+2*chunk])
	copy(swapped[head+chunk:head+2*chunk], ct[head:head+chunk])
	for name, c := range map[string]struct {
		b []byte
		k []byte
	}{
		"edited": {edited, key}, "wrong key": {ct, other}, "truncated at a chunk boundary": {ct[:head+2*chunk], key},
		"truncated mid chunk": {ct[:len(ct)-10], key}, "reordered": {swapped, key}, "not an archive": {[]byte("hello world, not ours"), key},
	} {
		if err := readAll(c.b, c.k); !errors.Is(err, ErrOpen) {
			t.Errorf("%s: want ErrOpen, got %v", name, err)
		}
	}
	if _, err := ParseRecoveryKey("abcd"); err == nil {
		t.Fatal("short key accepted")
	}
	if KeyHint("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff") != "0011…eeff" {
		t.Fatal("hint")
	}
}
