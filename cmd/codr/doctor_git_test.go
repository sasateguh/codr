package main

import "testing"

func TestGitAtLeast(t *testing.T) {
	for out, want := range map[string]bool{
		"git version 2.43.0": true, "git version 2.40.0": true, "git version 3.0.1": true,
		"git version 2.39.5 (Apple Git-154)": false, "git version 2.38.1": false, "nonsense": false, "git version x.y": false,
	} {
		if got := gitAtLeast(out, 2, 40); got != want {
			t.Errorf("gitAtLeast(%q) = %v, want %v", out, got, want)
		}
	}
}
