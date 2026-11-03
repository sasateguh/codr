package names

import (
	"strings"
	"testing"
)

func TestUser(t *testing.T) {
	ok := []string{"aiman", "a1", "ai.man", "ai-man", "ai_man", strings.Repeat("a", 39)}
	bad := []string{"", "a", "Aiman", "-aiman", "aiman-", "ai..man", "ai/man", "ai man", "admin", "api",
		strings.Repeat("a", 40), "..", "aiman\x00", "ай", "%2e%2e"}
	for _, n := range ok {
		if err := User(n); err != nil {
			t.Errorf("User(%q) = %v, want nil", n, err)
		}
	}
	for _, n := range bad {
		if err := User(n); err == nil {
			t.Errorf("User(%q) = nil, want error", n)
		}
	}
}

func TestRepo(t *testing.T) {
	ok := []string{"x", "proj", "proj.v2", "my-repo_1", strings.Repeat("a", 100)}
	bad := []string{"", ".", "..", ".hidden", "Proj", "a b", "a/b", "proj.git", "a..b", strings.Repeat("a", 101), "../x", "x\n"}
	for _, n := range ok {
		if err := Repo(n); err != nil {
			t.Errorf("Repo(%q) = %v, want nil", n, err)
		}
	}
	for _, n := range bad {
		if err := Repo(n); err == nil {
			t.Errorf("Repo(%q) = nil, want error", n)
		}
	}
}

func TestSplitRef(t *testing.T) {
	for _, in := range []string{"aiman/proj", "/aiman/proj.git", "aiman/proj.git"} {
		o, n, err := SplitRef(in)
		if err != nil || o != "aiman" || n != "proj" {
			t.Errorf("SplitRef(%q) = %q %q %v", in, o, n, err)
		}
	}
	for _, in := range []string{"aiman", "aiman/proj/x", "../proj", "aiman/../proj", "admin/proj", "a/b/../c", ""} {
		if _, _, err := SplitRef(in); err == nil {
			t.Errorf("SplitRef(%q) = nil, want error", in)
		}
	}
}
