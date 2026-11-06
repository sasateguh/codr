package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codr.my/codr/internal/i18n"
)

// TestDoctorSocket: doctor dials the hook socket. A file with no listener behind it is a FAIL
// (5 Nov 2026: os.Stat said "serve is running" while every push was rejected).
func TestDoctorSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "codr-doc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p := filepath.Join(dir, "hook.sock")
	check := func() (string, *doctor) {
		var out bytes.Buffer
		d := &doctor{cli: &cli{out: &out, errOut: &out, lang: i18n.Normalize("en-US", "")}}
		d.socket(p)
		return out.String(), d
	}
	if out, d := check(); d.warn != 1 || d.fail != 0 || !strings.Contains(out, "is not running") {
		t.Fatalf("no socket: %+v %q", d, out)
	}
	ln, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if out, d := check(); d.ok != 1 || !strings.Contains(out, "serve is running") {
		t.Fatalf("live socket: %+v %q", d, out)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if out, d := check(); d.fail != 1 || !strings.Contains(out, "nothing answers") || !strings.Contains(out, "connection refused") {
		t.Fatalf("dead socket: %+v %q", d, out)
	}
}
