// Version reporting: `lw version` and `lw --version` share this string.
package main

import (
	"fmt"
	"runtime/debug"
)

// version is the build version, stamped at link time with -ldflags
// "-X main.version=...". `make build` and goreleaser inject `git describe`
// output (the release tag vX.Y.Z, or tag-N-g<hash> between tags); the nix
// flake injects the (dirty) short rev because its sandbox has no .git.
// A bare `go build ./cmd/lw` leaves the fallback, and init() then derives
// the version from the VCS metadata the toolchain embeds on its own
// (v2.3.1): the short revision, "-dirty" when the tree was modified.
// Only a build outside any git checkout stays 0.0.0-unknown.
var version = "0.0.0-unknown"

const unknownVersion = "0.0.0-unknown"

func init() {
	version = applyVCSStamp(version)
}

// applyVCSStamp leaves a link-time stamp alone and, for the bare-build
// fallback, substitutes the embedded VCS revision. Makefile/nix/goreleaser
// builds keep their richer describe/tag strings.
func applyVCSStamp(stamped string) string {
	if stamped != unknownVersion {
		return stamped
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return stamped
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return stamped
	}
	if len(rev) > 9 {
		rev = rev[:9]
	}
	if dirty {
		return rev + "-dirty"
	}
	return rev
}

// cmdVersion prints the build version. It takes no flags; extra args are
// ignored, exactly like `lw --version`.
func cmdVersion(args []string) error {
	fmt.Println("lw " + version)
	return nil
}
