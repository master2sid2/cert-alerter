// Package version exposes the build version (set with -ldflags).
package version

import "runtime/debug"

// Version is overridden at build time:
//
//	go build -ldflags "-X github.com/master2sid2/cert-alerter/internal/version.Version=1.2.3"
var Version = "dev"

// String returns the version, enriched with VCS data for dev builds.
func String() string {
	if Version != "dev" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Version
	}
	// "go install github.com/master2sid2/cert-alerter/cmd/cert-alerter@v1.2.3"
	// records the module version instead of VCS data.
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
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
		return Version
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "-dirty"
	}
	return Version + "+" + rev
}
