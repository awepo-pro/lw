// Version reporting: `lw version` and `lw --version` share this string.
package main

import "fmt"

// version is the build version, stamped at link time with -ldflags
// "-X main.version=...". `make build` and goreleaser inject `git describe`
// output (the release tag vX.Y.Z, or tag-N-g<hash> between tags); the nix
// flake injects the (dirty) short rev because its sandbox has no .git. The
// fallback only fires for a bare `go build ./cmd/lw`.
var version = "0.0.0-unknown"

// cmdVersion prints the build version. It takes no flags; extra args are
// ignored, exactly like `lw --version`.
func cmdVersion(args []string) error {
	fmt.Println("lw " + version)
	return nil
}
