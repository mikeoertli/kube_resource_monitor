package kube

// These are overridden at build time with -ldflags, e.g.
//
//	go build -ldflags "-X github.com/mikeoertli/kube_resource_monitor/internal/kube.version=v0.2.0"
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Version returns the build version string.
func Version() string { return version }

// BuildInfo returns version, commit, and build date for `krm version`.
func BuildInfo() (string, string, string) { return version, commit, date }
