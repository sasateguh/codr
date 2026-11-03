// Package version holds the build identity of the binary.
// Values are set at link time by scripts/build-release.sh (and the deploy tooling):
//
//	go build -ldflags "-X codr.my/codr/internal/version.Tag=v0.1.0 -X ...Commit=abc123"
package version

import (
	"runtime"
	"runtime/debug"
)

// Tag is the release tag ("dev" when built without ldflags).
var Tag = "dev"

// Commit is the short git commit the binary was built from.
var Commit = "unknown"

// String renders the version line shown by `codr version` and /healthz. A binary built by `go install
// codr.my/codr/cmd/codr@vX` has no ldflags; it then reports the module version and VCS commit Go recorded.
func String() string {
	tag, commit := Tag, Commit
	if tag == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			if v := bi.Main.Version; v != "" && v != "(devel)" {
				tag = v
			}
			for _, st := range bi.Settings {
				if st.Key == "vcs.revision" && commit == "unknown" && len(st.Value) >= 7 {
					commit = st.Value[:7]
				}
			}
		}
	}
	return tag + " (" + commit + ", " + runtime.Version() + ")"
}
