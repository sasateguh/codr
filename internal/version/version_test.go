package version

import "testing"

func TestStringContainsTagAndCommit(t *testing.T) {
	Tag, Commit = "v9.9.9", "deadbee"
	got := String()
	for _, want := range []string{"v9.9.9", "deadbee"} {
		if !contains(got, want) {
			t.Fatalf("String() = %q, want it to contain %q", got, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
