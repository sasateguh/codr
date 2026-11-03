package auth

import "testing"

var fast = Params{Time: 1, Memory: 8 * 1024, Threads: 1}

func TestHashVerify(t *testing.T) {
	h, err := Hash("correct horse battery", fast)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify("correct horse battery", h); err != nil {
		t.Fatal(err)
	}
	if err := Verify("wrong", h); err != ErrMismatch {
		t.Fatalf("want ErrMismatch, got %v", err)
	}
	h2, _ := Hash("correct horse battery", fast)
	if h == h2 {
		t.Fatal("salt must differ between hashes")
	}
	if NeedsRehash(h, fast) {
		t.Fatal("same params must not need rehash")
	}
	if !NeedsRehash(h, Params{Time: 2, Memory: 8 * 1024, Threads: 1}) {
		t.Fatal("higher time must need rehash")
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=8,t=1,p=1$x$y", "plain", "$argon2id$v=18$m=8,t=1,p=1$AAAA$AAAA"} {
		if err := Verify("x", bad); err == nil {
			t.Errorf("Verify(%q) must fail", bad)
		}
	}
}
