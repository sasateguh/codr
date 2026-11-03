package cloud

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPackUnpack: a snapshot folder survives the round trip; hostile archives are refused.
func TestPackUnpack(t *testing.T) {
	src := filepath.Join(t.TempDir(), "codr-20261016-000000")
	_ = os.MkdirAll(filepath.Join(src, "repos"), 0o700)
	_ = os.WriteFile(filepath.Join(src, "manifest.json"), []byte(`{"repos":[]}`), 0o600)
	_ = os.WriteFile(filepath.Join(src, "repos", "1.bundle"), bytes.Repeat([]byte{7}, 100000), 0o600)
	var b bytes.Buffer
	if err := Pack(&b, src); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	top, err := Unpack(bytes.NewReader(b.Bytes()), dest)
	if err != nil || filepath.Base(top) != "codr-20261016-000000" {
		t.Fatalf("%s %v", top, err)
	}
	if got, _ := os.ReadFile(filepath.Join(top, "repos", "1.bundle")); len(got) != 100000 {
		t.Fatal("bundle lost")
	}
	hostile := func(name string, typ byte) []byte {
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		tw := tar.NewWriter(gz)
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: typ, Mode: 0o600, Size: 1, Linkname: "/etc/passwd"})
		_, _ = tw.Write([]byte("x"))
		_ = tw.Close()
		_ = gz.Close()
		return b.Bytes()
	}
	for _, c := range []struct {
		name string
		typ  byte
	}{{"../escape", tar.TypeReg}, {"/abs/file", tar.TypeReg}, {"snap/../../x", tar.TypeReg}, {"snap/link", tar.TypeSymlink}} {
		if _, err := Unpack(bytes.NewReader(hostile(c.name, c.typ)), t.TempDir()); err == nil || !strings.Contains(err.Error(), "cloud:") {
			t.Errorf("%s accepted: %v", c.name, err)
		}
	}
}
