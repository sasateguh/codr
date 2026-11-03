package sshd

import (
	"testing"

	"codr.my/codr/internal/gitcmd"
)

func TestParseCommandAccepts(t *testing.T) {
	cases := map[string]Command{
		"git-upload-pack 'aiman/proj.git'":    {gitcmd.UploadPack, "aiman", "proj"},
		`git-receive-pack "/aiman/proj.git"`:  {gitcmd.ReceivePack, "aiman", "proj"},
		"git upload-pack aiman/proj":          {gitcmd.UploadPack, "aiman", "proj"},
		"git-receive-pack aiman/proj.v2.git":  {gitcmd.ReceivePack, "aiman", "proj.v2"},
		"  git-upload-pack   'aiman/proj'   ": {gitcmd.UploadPack, "aiman", "proj"},
	}
	for in, want := range cases {
		got, err := ParseCommand(in)
		if err != nil || got != want {
			t.Errorf("ParseCommand(%q) = %+v %v, want %+v", in, got, err, want)
		}
	}
}

func TestParseCommandRejects(t *testing.T) {
	bad := []string{
		"", "ls", "git", "git-upload-pack", "bash -c id",
		"git-upload-pack 'aiman/proj.git' extra",
		"git-upload-pack --help 'aiman/proj.git'",
		"git-upload-pack '-aiman/proj'",
		"git-upload-pack 'aiman/../proj'",
		"git-upload-pack '../../etc/passwd'",
		"git-upload-pack 'aiman/proj'; id",
		"git-upload-pack 'aiman/proj' | cat",
		"git-upload-pack $(id)",
		"git-upload-pack 'aiman/proj\n'",
		"git-upload-archive 'aiman/proj'",
		"git-shell -c x",
		"git-upload-pack 'Aiman/Proj'",
		"git-upload-pack 'admin/proj'",
		"git upload-pack",
		"git upload-pack a b",
		`git-upload-pack 'aiman/proj"`,
	}
	for _, in := range bad {
		if _, err := ParseCommand(in); err == nil {
			t.Errorf("ParseCommand(%q) must be rejected", in)
		}
	}
}

func TestValidGitProtocol(t *testing.T) {
	if !ValidGitProtocol("version=2") || !ValidGitProtocol("version=0") {
		t.Fatal("version=N must be accepted")
	}
	for _, v := range []string{"", "version=2;x", "version=2 x", "VERSION=2", "version=", "foo"} {
		if ValidGitProtocol(v) {
			t.Errorf("%q must be rejected", v)
		}
	}
}

func TestParseLFSAuth(t *testing.T) {
	for in, want := range map[string]LFSAuth{
		"git-lfs-authenticate 'aiman/proj.git' upload":  {"aiman", "proj", "upload"},
		"git-lfs-authenticate aiman/proj download":      {"aiman", "proj", "download"},
		`git-lfs-authenticate "/aiman/proj.git" upload`: {"aiman", "proj", "upload"},
	} {
		if got, err := ParseLFSAuth(in); err != nil || got != want {
			t.Errorf("%q = %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"git-lfs-authenticate aiman/proj", "git-lfs-authenticate aiman/proj delete", "git-lfs-authenticate aiman/proj upload; id",
		"git-lfs-authenticate aiman/proj upload extra", "git-lfs-authenticate -x aiman/proj upload", "git-lfs-authenticate ../etc upload", "git-lfs-transfer aiman/proj upload"} {
		if _, err := ParseLFSAuth(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
