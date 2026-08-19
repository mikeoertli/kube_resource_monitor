package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// run executes the root command with args and returns stdout.
//
// Every case goes through --demo, which swaps in a synthetic cluster but leaves
// the entire flag-parsing, collection, grouping, sorting, and rendering path
// intact. That makes these genuine end-to-end tests of the command surface
// without needing a cluster.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"--demo", "--no-color"}, args...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestTopRendersWorkloadTable(t *testing.T) {
	out, err := run(t, "top", "-n", "prod")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	for _, want := range []string{"NAME", "CPU", "MEM", "storefront", "checkout-api", "postgres", "TOTAL"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "deploy") || !strings.Contains(out, "sts") {
		t.Errorf("workload kinds should be resolved through the ReplicaSet:\n%s", out)
	}
}

func TestTopJSONIsValidAndComplete(t *testing.T) {
	out, err := run(t, "top", "-n", "prod", "-o", "json")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	var doc struct {
		GroupBy string `json:"groupBy"`
		Rows    []struct {
			Kind          string   `json:"kind"`
			Name          string   `json:"name"`
			CPUMilli      int64    `json:"cpuMilli"`
			CPULimitMilli *int64   `json:"cpuLimitMilli"`
			CPUPercent    *float64 `json:"cpuPercentOfLimit"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if doc.GroupBy != "workload" {
		t.Errorf("groupBy = %q", doc.GroupBy)
	}
	if len(doc.Rows) == 0 {
		t.Fatal("no rows")
	}
	for _, r := range doc.Rows {
		if r.CPUMilli <= 0 {
			t.Errorf("row %q has no CPU usage", r.Name)
		}
		// image-resizer deliberately declares no limits; its percentage must be
		// absent rather than zero.
		if r.Name == "image-resizer" && r.CPULimitMilli != nil {
			t.Errorf("image-resizer should have no CPU limit, got %v", *r.CPULimitMilli)
		}
	}
}

func TestTopCSVHasHeaderAndRows(t *testing.T) {
	out, err := run(t, "top", "-n", "prod", "-o", "csv")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected a header and rows:\n%s", out)
	}
	if !strings.HasPrefix(lines[0], "timestamp,kind,namespace,name") {
		t.Errorf("unexpected header: %q", lines[0])
	}
}

func TestGroupByFlagsAreHonored(t *testing.T) {
	cases := map[string][]string{
		"pod":         {"storefront-7c9d"},
		"node":        {"worker-1", "worker-2"},
		"namespace":   {"prod"},
		"pvc":         {"data-postgres-0", "SIZE"},
		"statefulset": {"postgres"},
	}
	for group, wants := range cases {
		out, err := run(t, "top", "-A", "-g", group)
		if err != nil {
			t.Fatalf("group %s: %v\n%s", group, err, out)
		}
		for _, w := range wants {
			if !strings.Contains(out, w) {
				t.Errorf("group %s output missing %q:\n%s", group, w, out)
			}
		}
	}
}

func TestGroupByRejectsUnknownValue(t *testing.T) {
	_, err := run(t, "top", "-g", "sandwiches")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "sandwiches") {
		t.Errorf("error should name the bad value: %v", err)
	}
}

func TestFilterNarrowsOutput(t *testing.T) {
	out, err := run(t, "top", "-n", "prod", "-f", "postgres")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "postgres") {
		t.Errorf("filter dropped its own match:\n%s", out)
	}
	if strings.Contains(out, "storefront") {
		t.Errorf("filter should have excluded storefront:\n%s", out)
	}
}

func TestSelectorNarrowsOutput(t *testing.T) {
	out, err := run(t, "top", "-n", "prod", "-g", "pod", "-l", "app=postgres")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if strings.Contains(out, "storefront") {
		t.Errorf("label selector should have excluded storefront:\n%s", out)
	}
}

func TestContainersFlagAddsBreakdown(t *testing.T) {
	out, err := run(t, "top", "-n", "prod", "-g", "pod", "-c", "-f", "storefront")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	for _, want := range []string{"web", "envoy", "ctr"} {
		if !strings.Contains(out, want) {
			t.Errorf("container breakdown missing %q:\n%s", want, out)
		}
	}
}

func TestRequestsAndLimitsColumns(t *testing.T) {
	out, err := run(t, "top", "-n", "prod", "--requests", "--limits")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	for _, want := range []string{"CPU REQ", "CPU LIM", "MEM REQ", "MEM LIM"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing column %q:\n%s", want, out)
		}
	}
}

func TestPrometheusOutput(t *testing.T) {
	out, err := run(t, "top", "-n", "prod", "-o", "prometheus")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "# TYPE krm_cpu_usage_millicores gauge") {
		t.Errorf("missing TYPE line:\n%s", out)
	}
	if !strings.Contains(out, `name="storefront"`) {
		t.Errorf("missing labeled sample:\n%s", out)
	}
}

func TestNotifyRequiresARule(t *testing.T) {
	_, err := run(t, "notify")
	if err == nil {
		t.Fatal("expected an error when no rule is given")
	}
	if !strings.Contains(err.Error(), "--on") {
		t.Errorf("error should point at --on: %v", err)
	}
}

func TestNotifyOnceFiresAndSetsExitCode(t *testing.T) {
	out, err := run(t, "notify", "-A", "--on", "cpu>50%", "--once", "--stdout", "--exit-code")
	if err == nil {
		t.Fatalf("expected a non-nil error carrying the exit code:\n%s", out)
	}
	var ec *exitCodeError
	if !asExitCode(err, &ec) {
		t.Fatalf("error should be an exitCodeError, got %T: %v", err, err)
	}
	if ec.Code() != 2 {
		t.Errorf("exit code = %d, want 2", ec.Code())
	}
	if !strings.Contains(out, "FIRING") {
		t.Errorf("stdout notifier should have printed the alert:\n%s", out)
	}
}

func TestNotifyRejectsBadRule(t *testing.T) {
	_, err := run(t, "notify", "--on", "gpu>50%", "--once", "--stdout")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "gpu>50%") {
		t.Errorf("error should quote the bad rule: %v", err)
	}
}

func TestInvalidIntervalIsRejectedWithAnExplanation(t *testing.T) {
	_, err := run(t, "top", "-i", "100ms")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "15s") {
		t.Errorf("the error should explain why sub-second polling is pointless: %v", err)
	}
}

func TestWatchRejectsMachineOutput(t *testing.T) {
	_, err := run(t, "watch", "-o", "json")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "krm top") {
		t.Errorf("the error should suggest the right command: %v", err)
	}
}

func TestVersionCommand(t *testing.T) {
	out, err := run(t, "version")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasPrefix(out, "krm ") {
		t.Errorf("unexpected version output: %q", out)
	}
}

func TestOnlyProblemsNarrowsToHotRows(t *testing.T) {
	all, err := run(t, "top", "-A")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	hot, err := run(t, "top", "-A", "--only-problems", "--threshold", "0.9")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(strings.Split(hot, "\n")) >= len(strings.Split(all, "\n")) {
		t.Errorf("--only-problems should show fewer rows\nall:\n%s\nhot:\n%s", all, hot)
	}
}

// asExitCode is errors.As specialized, kept local to avoid importing errors
// into a file that otherwise reads as a black-box test.
func asExitCode(err error, target **exitCodeError) bool {
	for err != nil {
		if ec, ok := err.(*exitCodeError); ok {
			*target = ec
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// Bare `krm` picks its mode from the environment. Under `go test` stdout is not
// a terminal, so it must take the one-shot path rather than trying to open a
// full-screen view into a pipe.
func TestBareCommandPrintsOneTableWhenNotATerminal(t *testing.T) {
	out, err := run(t, "-n", "prod")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "TOTAL") {
		t.Errorf("expected a one-shot table:\n%s", out)
	}
}

// `krm watch` is explicit, so it cannot fall back — it has to say why.
func TestWatchRefusesWhenStdoutIsNotATerminal(t *testing.T) {
	_, err := run(t, "watch")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "terminal") {
		t.Errorf("error should say a terminal is required: %v", err)
	}
	if !strings.Contains(err.Error(), "krm top") {
		t.Errorf("error should point at the alternative: %v", err)
	}
}

// A wrong --output is a mistake in what was typed; not-a-terminal is a property
// of how it was run. The typed mistake should be reported first.
func TestWatchReportsOutputMistakeBeforeTerminalCheck(t *testing.T) {
	_, err := run(t, "watch", "-o", "json")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "terminal") {
		t.Errorf("the --output problem should be reported first: %v", err)
	}
	if !strings.Contains(err.Error(), "krm top -o json") {
		t.Errorf("error should suggest the exact command: %v", err)
	}
}

// -w/--watch and --once were removed: two named subcommands plus an adaptive
// default is the whole surface, and duplicate entry points are what made the
// default confusing in the first place.
func TestRedundantModeFlagsAreGone(t *testing.T) {
	for _, flag := range []string{"--watch", "-w", "--once"} {
		_, err := run(t, flag)
		if err == nil {
			t.Errorf("%s should no longer be accepted on the root command", flag)
			continue
		}
		if !strings.Contains(err.Error(), "unknown") && !strings.Contains(err.Error(), "flag") {
			t.Errorf("%s: unexpected error %v", flag, err)
		}
	}
	// `krm notify --once` is a different flag on a different command and stays.
	if _, err := run(t, "notify", "--on", "cpu>99999m", "--once", "--stdout"); err != nil {
		t.Errorf("notify --once should still work: %v", err)
	}
}

// The mode table is the first thing a reader should hit, because "what does
// bare krm do" is the question the help exists to answer.
func TestHelpLeadsWithTheModeTable(t *testing.T) {
	out, err := run(t, "--help")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	idx := strings.Index(out, "MODES")
	if idx < 0 {
		t.Fatalf("help has no MODES block:\n%s", out)
	}
	if usage := strings.Index(out, "Usage:"); usage >= 0 && idx > usage {
		t.Error("the MODES block should appear before the Usage section")
	}
	for _, want := range []string{"stdout is a terminal", "piped or redirected", "krm top", "krm watch"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q", want)
		}
	}
	if !strings.Contains(out, "Examples:") {
		t.Errorf("help should carry worked examples:\n%s", out)
	}
}

func TestVersionCommandForms(t *testing.T) {
	full, err := run(t, "version")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasPrefix(full, "krm ") {
		t.Errorf("unexpected version output: %q", full)
	}
	// The Go version and platform come from the runtime, so they are always
	// knowable and always worth printing in a bug report.
	for _, want := range []string{"go1.", "/"} {
		if !strings.Contains(full, want) {
			t.Errorf("version output missing %q: %q", want, full)
		}
	}

	short, err := run(t, "version", "--short")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(short, " ") {
		t.Errorf("--short should print a bare version for scripts, got %q", short)
	}
	if strings.HasPrefix(short, "krm") {
		t.Errorf("--short should omit the program name, got %q", short)
	}
}

func TestVersionJSON(t *testing.T) {
	out, err := run(t, "version", "-o", "json")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	var doc struct {
		Version   string `json:"version"`
		GoVersion string `json:"goVersion"`
		Platform  string `json:"platform"`
		Source    string `json:"source"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if doc.Version == "" || doc.GoVersion == "" || doc.Platform == "" {
		t.Errorf("incomplete version document: %+v", doc)
	}
	// Without this field, "why does my build say dev?" has no answer short of
	// rebuilding.
	if doc.Source == "" {
		t.Error("version JSON should say where the version came from")
	}
}
