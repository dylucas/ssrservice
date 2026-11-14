// Package version holds build-time version information.
package version

import (
	"fmt"
	"runtime"
)

var (
	// Version is the semantic version, injected at build time.
	Version = "dev"
	// Commit is the git commit hash, injected at build time.
	Commit = ""
	// BuildTime is the build timestamp, injected at build time.
	BuildTime = ""
)

// Full returns a human readable version string.
func Full() string {
	if Commit == "" {
		return Version
	}
	if len(Commit) > 12 {
		Commit = Commit[:12]
	}
	return fmt.Sprintf("%s (%s)", Version, Commit)
}

// Info returns version information including the runtime.
func Info() string {
	return fmt.Sprintf("sspanel %s %s/%s %s", Full(), runtime.GOOS, runtime.GOARCH, BuildTime)
}
