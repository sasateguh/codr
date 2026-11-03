// Package version holds the build identity of the binary.
// Values are set at link time by scripts/build-release.sh (and the deploy tooling):
//
//	go build -ldflags "-X codr.my/codr/internal/version.Tag=v0.1.0 -X ...Commit=abc123"
package version

import "runtime"

// Tag is the release tag ("dev" when built without ldflags).
var Tag = "dev"

// Commit is the short git commit the binary was built from.
var Commit = "unknown"

// String renders the version line shown by `amcd version` and /healthz.
func String() string {
	return Tag + " (" + Commit + ", " + runtime.Version() + ")"
}
