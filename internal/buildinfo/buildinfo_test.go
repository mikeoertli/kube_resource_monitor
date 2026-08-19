package buildinfo

import (
	"runtime/debug"
	"strings"
	"testing"
)

func bi(mainVersion string, settings map[string]string) *debug.BuildInfo {
	out := &debug.BuildInfo{GoVersion: "go1.24.7"}
	out.Main.Version = mainVersion
	for k, v := range settings {
		out.Settings = append(out.Settings, debug.BuildSetting{Key: k, Value: v})
	}
	return out
}

// A stamped build is the only one we control, so it outranks everything the
// toolchain inferred.
func TestLDFlagsWinOverModuleVersion(t *testing.T) {
	got := resolve(
		Info{Version: "v1.2.3", Commit: "deadbeefcafe", Date: "2026-01-01T00:00:00Z"},
		bi("v9.9.9", map[string]string{"vcs.revision": "1111111111111111"}),
		true,
	)
	if got.Version != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3", got.Version)
	}
	if got.Source != SourceLDFlags {
		t.Errorf("source = %q, want ldflags", got.Source)
	}
	if got.Commit != "deadbee" {
		t.Errorf("commit = %q, want it truncated to 7", got.Commit)
	}
}

// `go install ...@v0.2.0` sets no ldflags; the module version is the answer.
func TestModuleVersionUsedWhenNotStamped(t *testing.T) {
	got := resolve(Info{}, bi("v0.2.0", nil), true)
	if got.Version != "v0.2.0" || got.Source != SourceModule {
		t.Errorf("got %q from %q, want v0.2.0 from module", got.Version, got.Source)
	}
}

// A pseudo-version identifies a commit no tag points at. Reporting
// "v0.0.0-20260819161746-7710d73326d1" as the version is technically true and
// practically useless, but the commit inside it is worth keeping — a module
// downloaded by commit carries no VCS metadata of its own.
func TestPseudoVersionBecomesDevAndYieldsCommit(t *testing.T) {
	got := resolve(Info{}, bi("v0.0.0-20260819161746-7710d73326d1", nil), true)
	if got.Version != "dev" {
		t.Errorf("version = %q, want dev", got.Version)
	}
	if got.Commit != "7710d73" {
		t.Errorf("commit = %q, want 7710d73 extracted from the pseudo-version", got.Commit)
	}
	if got.Source != SourceVCS {
		t.Errorf("source = %q, want vcs", got.Source)
	}
}

func TestPseudoVersionDetection(t *testing.T) {
	pseudo := []string{
		"v0.0.0-20260819161746-7710d73326d1",
		"v1.2.3-0.20260819161746-7710d73326d1",
		"v1.2.3-pre.0.20260819161746-abcdefabcdef",
	}
	for _, v := range pseudo {
		if !isPseudoVersion(v) {
			t.Errorf("%q should be recognized as a pseudo-version", v)
		}
	}
	for _, v := range []string{"v1.2.3", "v0.1.0-rc.1", "(devel)", "", "dev"} {
		if isPseudoVersion(v) {
			t.Errorf("%q should not be a pseudo-version", v)
		}
	}
}

// The Go toolchain already appends "+dirty" to the module version of a build
// from a modified tree. Without stripping it, Short() emitted "+dirty-dirty".
func TestDirtySuffixIsNotDoubled(t *testing.T) {
	got := resolve(Info{}, bi("v0.0.0-20260819161746-7710d73326d1+dirty", nil), true)
	if !got.Dirty {
		t.Error("a +dirty module version should set the Dirty flag")
	}
	short := got.Short()
	if strings.Count(short, "dirty") != 1 {
		t.Errorf("Short() = %q, want exactly one dirty marker", short)
	}
	if short != "dev-dirty" {
		t.Errorf("Short() = %q, want dev-dirty", short)
	}
}

func TestVCSSettingsFillCommitDateAndDirty(t *testing.T) {
	got := resolve(Info{}, bi("(devel)", map[string]string{
		"vcs.revision": "7710d73326d1aaaabbbbccccddddeeeeffff0000",
		"vcs.time":     "2026-08-19T16:17:46Z",
		"vcs.modified": "true",
	}), true)

	if got.Version != "dev" || got.Source != SourceVCS {
		t.Errorf("got %q/%q, want dev/vcs", got.Version, got.Source)
	}
	if got.Commit != "7710d73" {
		t.Errorf("commit = %q", got.Commit)
	}
	if got.Date != "2026-08-19T16:17:46Z" {
		t.Errorf("date = %q", got.Date)
	}
	if !got.Dirty {
		t.Error("vcs.modified=true should set Dirty")
	}
}

// A binary stripped of build info, or built by something other than the Go
// toolchain, still has to answer the question.
func TestNoBuildInfoStillReportsSomething(t *testing.T) {
	got := resolve(Info{}, nil, false)
	if got.Version != "dev" || got.Source != SourceUnknown {
		t.Errorf("got %q/%q, want dev/unknown", got.Version, got.Source)
	}
	if got.GoVersion == "" || got.Platform == "" {
		t.Errorf("go version and platform come from the runtime and should always be set: %+v", got)
	}
	if !strings.Contains(got.Platform, "/") {
		t.Errorf("platform = %q, want os/arch", got.Platform)
	}
}

func TestStringIncludesEverythingKnown(t *testing.T) {
	got := resolve(
		Info{Version: "v0.1.0", Commit: "abcdef1234", Date: "2026-08-19T00:00:00Z"},
		nil, false,
	).String()

	for _, want := range []string{"krm v0.1.0", "commit abcdef1", "built 2026-08-19", "go1."} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing %q", got, want)
		}
	}
}

func TestStringOmitsUnknownFields(t *testing.T) {
	got := resolve(Info{}, nil, false).String()
	if strings.Contains(got, "commit ") || strings.Contains(got, "built ") {
		t.Errorf("String() should not claim a commit or date it does not have: %q", got)
	}
	if !strings.HasPrefix(got, "krm dev") {
		t.Errorf("String() = %q", got)
	}
}

func TestUserAgentIdentifiesTheBuild(t *testing.T) {
	ua := UserAgent()
	if !strings.HasPrefix(ua, "kube-resource-monitor/") {
		t.Errorf("UserAgent() = %q", ua)
	}
	if strings.Contains(ua, " ") {
		t.Errorf("a user agent with a space in it will be mangled in audit logs: %q", ua)
	}
}

// `make build` passes `git describe --tags --dirty`, which already ends in
// "-dirty" on a modified tree. Combined with Short()'s own marker that produced
// "v0.1.0-dirty-dirty".
func TestLDFlagsDirtySuffixIsNotDoubled(t *testing.T) {
	got := resolve(Info{Version: "v0.1.0-dirty"}, nil, false)
	if !got.Dirty {
		t.Error("a -dirty suffix from git describe should set the Dirty flag")
	}
	if got.Version != "v0.1.0" {
		t.Errorf("version = %q, want the marker stripped", got.Version)
	}
	if short := got.Short(); short != "v0.1.0-dirty" {
		t.Errorf("Short() = %q, want exactly one marker", short)
	}
}

// The two dirty signals are independent; neither should be able to clear the
// other. A clean vcs.modified on a tree git describe called dirty must not win.
func TestDirtyFlagIsStickyAcrossSources(t *testing.T) {
	got := resolve(
		Info{Version: "v0.1.0-dirty"},
		bi("v0.1.0", map[string]string{"vcs.modified": "false"}),
		true,
	)
	if !got.Dirty {
		t.Error("vcs.modified=false must not clear a dirty marker from ldflags")
	}
	if strings.Count(got.Short(), "dirty") != 1 {
		t.Errorf("Short() = %q", got.Short())
	}
}
