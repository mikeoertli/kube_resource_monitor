// Package buildinfo reports which build of krm is running.
//
// Version information can arrive by three different routes, and which one you
// get depends entirely on how the binary was produced:
//
//   - `make build` / `make install` stamp it in with -ldflags.
//   - `go install github.com/mikeoertli/kube_resource_monitor/cmd/krm@v0.2.0`
//     sets no ldflags at all, but the module version is recorded in the
//     binary's build info.
//   - `go build ./cmd/krm` in a git checkout sets neither, but the Go
//     toolchain records the VCS revision, commit time, and whether the tree
//     was dirty.
//
// Only the first is under our control, and it is the route most users will not
// take. Reading the other two is what stops `krm version` from reporting "dev"
// for everybody who installed the normal way.
package buildinfo

import (
	"fmt"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
)

// A Go pseudo-version ends in a UTC timestamp and a 12-character commit
// prefix. The separator before the timestamp is a hyphen when there is no base
// version (v0.0.0-20260819161746-7710d73326d1) but a dot when the pseudo-version
// builds on a tag (v1.2.3-0.20260819161746-7710d73326d1), so both are accepted.
var pseudoVersionRE = regexp.MustCompile(`[-.]([0-9]{14})-([0-9a-f]{12})$`)

// isPseudoVersion reports whether v is a synthesized version for a commit that
// no tag points at, rather than a real release.
func isPseudoVersion(v string) bool { return pseudoVersionRE.MatchString(v) }

// commitFromPseudoVersion pulls the commit prefix out of a pseudo-version.
func commitFromPseudoVersion(v string) string {
	if m := pseudoVersionRE.FindStringSubmatch(v); m != nil {
		return m[2]
	}
	return ""
}

// trimDirtySuffix strips a trailing dirty marker, reporting whether one was
// there.
//
// Two independent tools want to tell us the same thing in different dialects:
// `git describe --dirty` appends "-dirty" (which reaches us through -ldflags)
// and the Go toolchain appends "+dirty" to the module version. Either one left
// in place would be duplicated by Short(), which appends its own marker from
// the Dirty flag -- that is how "dev-dirty-dirty" happens.
func trimDirtySuffix(v string) (string, bool) {
	for _, suffix := range []string{"-dirty", "+dirty"} {
		if strings.HasSuffix(v, suffix) {
			return strings.TrimSuffix(v, suffix), true
		}
	}
	return v, false
}

// Set at build time with, for example:
//
//	go build -ldflags "-X github.com/mikeoertli/kube_resource_monitor/internal/buildinfo.version=v0.2.0"
var (
	version string
	commit  string
	date    string
)

// Source names where the version ultimately came from. It exists so that
// "why does this say dev?" is answerable without rebuilding.
type Source string

const (
	SourceLDFlags Source = "ldflags"
	SourceModule  Source = "module"
	SourceVCS     Source = "vcs"
	SourceUnknown Source = "unknown"
)

// Info describes the running build.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
	// Dirty reports that the working tree had uncommitted changes at build
	// time. A bug report from a dirty build is a bug report about code nobody
	// else has.
	Dirty  bool   `json:"dirty,omitempty"`
	Source Source `json:"source"`
}

// Get returns information about the running build.
func Get() Info {
	bi, ok := debug.ReadBuildInfo()
	return resolve(Info{Version: version, Commit: commit, Date: date}, bi, ok)
}

// resolve is the pure core of Get, split out so its precedence rules can be
// tested against synthetic build info instead of whatever produced the test
// binary.
func resolve(ld Info, bi *debug.BuildInfo, ok bool) Info {
	out := ld
	out.GoVersion = runtime.Version()
	out.Platform = runtime.GOOS + "/" + runtime.GOARCH

	if v, dirty := trimDirtySuffix(out.Version); dirty {
		out.Version, out.Dirty = v, true
	}
	if out.Version != "" {
		out.Source = SourceLDFlags
	}

	if ok && bi != nil {
		mv, mvDirty := trimDirtySuffix(bi.Main.Version)
		if mvDirty {
			out.Dirty = true
		}

		switch {
		// "(devel)" means a checkout with no version at all, and a
		// pseudo-version like v0.0.0-20260819161746-7710d73326d1 means a
		// commit that no tag points at. Neither is a release, and neither
		// belongs in a version line -- but the commit hash buried in a
		// pseudo-version is worth keeping, since a module downloaded by
		// commit carries no VCS metadata of its own.
		case out.Version != "" || mv == "" || mv == "(devel)":
			// Nothing usable, or ldflags already won.
		case isPseudoVersion(mv):
			if c := commitFromPseudoVersion(mv); c != "" && out.Commit == "" {
				out.Commit = c
			}
		default:
			out.Version = mv
			out.Source = SourceModule
		}
		if bi.GoVersion != "" {
			out.GoVersion = bi.GoVersion
		}

		var revision, vcsTime string
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				revision = s.Value
			case "vcs.time":
				vcsTime = s.Value
			case "vcs.modified":
				if s.Value == "true" {
					out.Dirty = true
				}
			case "GOOS":
				if s.Value != "" {
					out.Platform = s.Value + "/" + runtime.GOARCH
				}
			case "GOARCH":
				if s.Value != "" {
					out.Platform = strings.SplitN(out.Platform, "/", 2)[0] + "/" + s.Value
				}
			}
		}
		if out.Commit == "" && revision != "" {
			out.Commit = revision
		}
		if out.Date == "" && vcsTime != "" {
			out.Date = vcsTime
		}
		if out.Version == "" && (revision != "" || out.Commit != "") {
			out.Version = "dev"
			out.Source = SourceVCS
		}
	}

	if out.Version == "" {
		out.Version = "dev"
		out.Source = SourceUnknown
	}
	// Full hashes are noise in a version line; seven characters is what git
	// itself shows and is unambiguous in any repository this size.
	if len(out.Commit) > 7 {
		out.Commit = out.Commit[:7]
	}
	return out
}

// Short returns just the version, for scripts.
func (i Info) Short() string {
	if i.Dirty {
		return i.Version + "-dirty"
	}
	return i.Version
}

// String returns the one-line form shown by `krm version`.
func (i Info) String() string {
	var b strings.Builder
	b.WriteString("krm ")
	b.WriteString(i.Short())

	var parts []string
	if i.Commit != "" {
		parts = append(parts, "commit "+i.Commit)
	}
	if i.Date != "" {
		parts = append(parts, "built "+i.Date)
	}
	parts = append(parts, i.GoVersion, i.Platform)
	fmt.Fprintf(&b, " (%s)", strings.Join(parts, ", "))
	return b.String()
}

// UserAgent identifies this build to the Kubernetes API server, so a spike of
// read load in an audit log can be traced back to a specific version.
func UserAgent() string {
	i := Get()
	return "kube-resource-monitor/" + i.Short()
}
