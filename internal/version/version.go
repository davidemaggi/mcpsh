// Package version holds build information, set by the linker at release time.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Set with -ldflags "-X github.com/davidemaggi/mcpsh/internal/version.Version=..." by GoReleaser.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// String returns a one-line description of the build.
func String() string {
	commit := Commit
	if commit == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				if s.Key == "vcs.revision" && len(s.Value) >= 12 {
					commit = s.Value[:12]
				}
			}
		}
	}
	s := "mcpsh " + Version
	if commit != "" {
		s += " (" + commit + ")"
	}
	if Date != "" {
		s += " built " + Date
	}
	return fmt.Sprintf("%s %s/%s", s, runtime.GOOS, runtime.GOARCH)
}
