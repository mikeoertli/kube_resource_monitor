package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/mikeoertli/kube_resource_monitor/internal/model"
)

func TestParseRulePercent(t *testing.T) {
	r, err := ParseRule("cpu>85%")
	if err != nil {
		t.Fatal(err)
	}
	if r.Metric != model.MetricCPU || !r.IsPercent || r.Op != Above {
		t.Fatalf("unexpected rule: %+v", r)
	}
	if r.Percent != 0.85 {
		t.Errorf("percent = %v, want 0.85", r.Percent)
	}
	if r.Basis != "" {
		t.Errorf("basis = %q, want empty (best available)", r.Basis)
	}
}

func TestParseRuleExplicitBasis(t *testing.T) {
	r, err := ParseRule("mem>90% of request")
	if err != nil {
		t.Fatal(err)
	}
	if r.Metric != model.MetricMemory || r.Basis != model.BasisRequest {
		t.Fatalf("unexpected rule: %+v", r)
	}
}

func TestParseRuleAbsoluteQuantities(t *testing.T) {
	cpu, err := ParseRule("cpu>1500m")
	if err != nil {
		t.Fatal(err)
	}
	if cpu.IsPercent || cpu.Absolute != 1500 {
		t.Errorf("cpu rule = %+v, want absolute 1500 milli", cpu)
	}

	mem, err := ParseRule("mem>2Gi")
	if err != nil {
		t.Fatal(err)
	}
	if mem.IsPercent || mem.Absolute != 2<<30 {
		t.Errorf("mem rule = %+v, want absolute %d bytes", mem, int64(2<<30))
	}
}

func TestParseRuleStorageDefaultsToCapacity(t *testing.T) {
	r, err := ParseRule("storage>80%")
	if err != nil {
		t.Fatal(err)
	}
	if r.Basis != model.BasisCapacity {
		t.Errorf("basis = %q, want capacity (volumes have no limits)", r.Basis)
	}
}

func TestParseRuleRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "cpu", "cpu!85%", "gpu>50%", "cpu>%"} {
		if _, err := ParseRule(bad); err == nil {
			t.Errorf("ParseRule(%q) should have failed", bad)
		}
	}
}

func TestParseRulesReportsAllFailures(t *testing.T) {
	_, err := ParseRules([]string{"cpu>85%", "nonsense", "alsobad"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "nonsense") || !strings.Contains(err.Error(), "alsobad") {
		t.Errorf("both failures should be reported, got: %v", err)
	}
}

func limited(cpuUsed, cpuLimit int64) model.Usage {
	return model.Usage{
		Used:        model.Amounts{CPUMilli: cpuUsed},
		Limits:      model.Amounts{CPUMilli: cpuLimit},
		HasCPULimit: true, UsedKnown: true,
	}
}

// A percentage rule cannot be evaluated without a denominator, and that is a
// distinct outcome from "not breached".
func TestEvaluateReportsInapplicability(t *testing.T) {
	r, _ := ParseRule("cpu>85%")
	_, applicable, _ := r.Evaluate(model.Usage{Used: model.Amounts{CPUMilli: 9000}, UsedKnown: true})
	if applicable {
		t.Error("a percent rule against a workload with no limit is not applicable")
	}

	breached, applicable, observed := r.Evaluate(limited(900, 1000))
	if !applicable || !breached || observed != 0.9 {
		t.Errorf("got breached=%v applicable=%v observed=%v", breached, applicable, observed)
	}
}

func row(name string, u model.Usage) *model.Row {
	return &model.Row{Kind: model.KindPod, Name: name, Namespace: "prod", Usage: u}
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }

func newTestWatcher(cfg Config) (*Watcher, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	w := NewWatcher(cfg)
	w.SetClock(clk.now)
	return w, clk
}

func TestWatcherFiresOnceUntilResolved(t *testing.T) {
	rules, _ := ParseRules([]string{"cpu>85%"})
	cfg := DefaultConfig()
	cfg.Rules = rules
	w, clk := newTestWatcher(cfg)

	hot := []*model.Row{row("api", limited(900, 1000))}

	if got := w.Evaluate(hot); len(got) != 1 || !got[0].Firing {
		t.Fatalf("first evaluation should fire once, got %d alerts", len(got))
	}
	clk.add(time.Minute)
	if got := w.Evaluate(hot); len(got) != 0 {
		t.Errorf("a still-firing alert must not re-notify, got %d", len(got))
	}
}

// Without hysteresis, a value hovering at the threshold produces an endless
// fire/resolve stream. 88% is below the 85% threshold's... no: 84% is below the
// threshold but within the margin, so it must not resolve yet.
func TestWatcherHysteresisPreventsFlapping(t *testing.T) {
	rules, _ := ParseRules([]string{"cpu>85%"})
	cfg := DefaultConfig()
	cfg.Rules = rules
	cfg.Hysteresis = 0.10 // resolve only below 85% - 8.5% = 76.5%
	w, clk := newTestWatcher(cfg)

	w.Evaluate([]*model.Row{row("api", limited(900, 1000))}) // fires

	clk.add(time.Minute)
	if got := w.Evaluate([]*model.Row{row("api", limited(840, 1000))}); len(got) != 0 {
		t.Errorf("84%% is inside the hysteresis band and must not resolve, got %d alerts", len(got))
	}
	clk.add(time.Minute)
	got := w.Evaluate([]*model.Row{row("api", limited(500, 1000))})
	if len(got) != 1 || got[0].Firing {
		t.Fatalf("50%% is a real recovery and should resolve, got %+v", got)
	}
}

func TestWatcherRepeatInterval(t *testing.T) {
	rules, _ := ParseRules([]string{"cpu>85%"})
	cfg := DefaultConfig()
	cfg.Rules = rules
	cfg.Repeat = 5 * time.Minute
	w, clk := newTestWatcher(cfg)

	hot := []*model.Row{row("api", limited(900, 1000))}
	w.Evaluate(hot)

	clk.add(2 * time.Minute)
	if got := w.Evaluate(hot); len(got) != 0 {
		t.Errorf("re-notified too early, got %d", len(got))
	}
	clk.add(4 * time.Minute)
	if got := w.Evaluate(hot); len(got) != 1 {
		t.Errorf("should re-notify after the repeat interval, got %d", len(got))
	}
}

// A brief spike during startup should not page anyone.
func TestWatcherMinDurationSuppressesSpikes(t *testing.T) {
	rules, _ := ParseRules([]string{"cpu>85%"})
	cfg := DefaultConfig()
	cfg.Rules = rules
	cfg.MinDuration = 2 * time.Minute
	w, clk := newTestWatcher(cfg)

	hot := []*model.Row{row("api", limited(900, 1000))}
	if got := w.Evaluate(hot); len(got) != 0 {
		t.Errorf("should not fire before MinDuration, got %d", len(got))
	}
	clk.add(30 * time.Second)
	if got := w.Evaluate([]*model.Row{row("api", limited(100, 1000))}); len(got) != 0 {
		t.Errorf("a spike that recovers must produce no alert at all, got %+v", got)
	}
	clk.add(time.Minute)
	w.Evaluate(hot)
	clk.add(3 * time.Minute)
	if got := w.Evaluate(hot); len(got) != 1 {
		t.Errorf("a sustained breach should fire after MinDuration, got %d", len(got))
	}
}

func TestWatcherRecursesIntoChildren(t *testing.T) {
	rules, _ := ParseRules([]string{"mem>90%"})
	cfg := DefaultConfig()
	cfg.Rules = rules
	w, _ := newTestWatcher(cfg)

	// The pod as a whole looks fine; one container inside it does not.
	pod := &model.Row{Kind: model.KindPod, Name: "api-1", Namespace: "prod",
		Usage: model.Usage{
			Used: model.Amounts{MemBytes: 600}, Limits: model.Amounts{MemBytes: 2000},
			HasMemLimit: true, UsedKnown: true,
		},
		Children: []*model.Row{{Kind: model.KindContainer, Name: "cache", Namespace: "prod",
			Usage: model.Usage{
				Used: model.Amounts{MemBytes: 950}, Limits: model.Amounts{MemBytes: 1000},
				HasMemLimit: true, UsedKnown: true,
			}}},
	}

	got := w.Evaluate([]*model.Row{pod})
	if len(got) != 1 {
		t.Fatalf("expected the hot container to alert, got %d", len(got))
	}
	if got[0].Row.Name != "cache" {
		t.Errorf("alerted on %q, want cache", got[0].Row.Name)
	}
}

func TestWatcherIgnoresRowsWithoutMetrics(t *testing.T) {
	rules, _ := ParseRules([]string{"cpu>85%"})
	cfg := DefaultConfig()
	cfg.Rules = rules
	w, _ := newTestWatcher(cfg)

	r := row("cold", limited(0, 1000))
	r.MetricsMissing = true
	if got := w.Evaluate([]*model.Row{r}); len(got) != 0 {
		t.Errorf("a row with no sample must not alert, got %d", len(got))
	}
}

// A deleted pod is not a resolved alert; forgetting it must be silent, but the
// state must not leak.
func TestWatcherForgetsVanishedRowsSilently(t *testing.T) {
	rules, _ := ParseRules([]string{"cpu>85%"})
	cfg := DefaultConfig()
	cfg.Rules = rules
	w, clk := newTestWatcher(cfg)

	w.Evaluate([]*model.Row{row("api", limited(900, 1000))})
	if w.Firing() != 1 {
		t.Fatalf("Firing = %d, want 1", w.Firing())
	}
	clk.add(time.Minute)
	if got := w.Evaluate(nil); len(got) != 0 {
		t.Errorf("a vanished row should not emit a resolution, got %+v", got)
	}
	if w.Firing() != 0 {
		t.Errorf("state leaked: Firing = %d", w.Firing())
	}
}

func TestAlertBodyMentionsRuleAndValue(t *testing.T) {
	rules, _ := ParseRules([]string{"cpu>85%"})
	cfg := DefaultConfig()
	cfg.Rules = rules
	w, _ := newTestWatcher(cfg)

	got := w.Evaluate([]*model.Row{row("api", limited(920, 1000))})
	if len(got) != 1 {
		t.Fatal("expected one alert")
	}
	body := got[0].Body()
	if !strings.Contains(body, "92%") || !strings.Contains(body, "prod/api") {
		t.Errorf("alert body should name the workload and its value: %q", body)
	}
	if !strings.Contains(got[0].Title(), "alert") {
		t.Errorf("title should mark the state: %q", got[0].Title())
	}
}

// Pod names come from the cluster; interpolating one into an AppleScript
// unescaped would be a command injection.
func TestAppleScriptStringEscapes(t *testing.T) {
	got := appleScriptString(`evil" & do shell script "rm -rf /`)
	if strings.Contains(got, `evil" &`) {
		t.Errorf("quote was not escaped: %s", got)
	}
	if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
		t.Errorf("result should be a quoted literal: %s", got)
	}
	if got := appleScriptString(`back\slash`); !strings.Contains(got, `back\\slash`) {
		t.Errorf("backslash was not escaped: %s", got)
	}
}
