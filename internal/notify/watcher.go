package notify

import (
	"fmt"
	"sort"
	"time"

	"github.com/mikeoertli/kube_resource_monitor/internal/model"
)

// Alert describes one rule breach on one row.
type Alert struct {
	Rule     Rule
	Row      *model.Row
	Observed float64
	// Firing is false for a resolution notice.
	Firing bool
	Since  time.Time
}

// Title is the notification headline.
func (a Alert) Title() string {
	state := "resolved"
	if a.Firing {
		state = "alert"
	}
	return fmt.Sprintf("krm %s: %s %s", state, a.Row.Kind.Short(), a.Row.Name)
}

// Body is the notification detail line.
func (a Alert) Body() string {
	ns := a.Row.Namespace
	if ns != "" {
		ns = ns + "/"
	}
	if a.Firing {
		return fmt.Sprintf("%s%s is at %s (rule: %s)",
			ns, a.Row.Name, a.Rule.FormatObserved(a.Observed), a.Rule.Describe())
	}
	return fmt.Sprintf("%s%s back to %s (rule: %s)",
		ns, a.Row.Name, a.Rule.FormatObserved(a.Observed), a.Rule.Describe())
}

// Config tunes alert behavior.
type Config struct {
	Rules []Rule
	// Hysteresis is how far below the threshold a value must fall before the
	// alert is considered resolved, as a fraction of the threshold.
	//
	// Without it, a workload oscillating around 85% produces an endless stream
	// of fire/resolve notifications. 10% of the threshold is enough to absorb
	// ordinary sampling noise while still resolving promptly on a real drop.
	Hysteresis float64
	// Repeat re-notifies a still-firing alert after this long. Zero means
	// notify once per breach and stay quiet until it resolves.
	Repeat time.Duration
	// NotifyResolved sends a notice when a breach clears.
	NotifyResolved bool
	// MinDuration requires a breach to persist this long before firing, which
	// suppresses alerts on momentary spikes like a JVM warming up.
	MinDuration time.Duration
}

// DefaultConfig is the out-of-the-box alerting behavior.
func DefaultConfig() Config {
	return Config{
		Hysteresis:     0.10,
		Repeat:         0,
		NotifyResolved: true,
		MinDuration:    0,
	}
}

type alertState struct {
	firstSeen  time.Time
	notifiedAt time.Time
	notified   bool
	lastSeen   time.Time
	observed   float64
}

// Watcher tracks which rules are currently breached and emits transitions.
//
// It is deliberately a pure state machine over successive snapshots: the same
// code drives the TUI's alert banner and the headless notify mode, and it can
// be tested by feeding it rows without any clock or delivery mechanism.
type Watcher struct {
	cfg   Config
	state map[string]*alertState
	now   func() time.Time
}

// NewWatcher builds a watcher.
func NewWatcher(cfg Config) *Watcher {
	if cfg.Hysteresis <= 0 {
		cfg.Hysteresis = DefaultConfig().Hysteresis
	}
	return &Watcher{cfg: cfg, state: map[string]*alertState{}, now: time.Now}
}

// SetClock injects a clock, for tests.
func (w *Watcher) SetClock(f func() time.Time) { w.now = f }

func key(r *model.Row, rule Rule) string {
	return string(r.Kind) + "|" + r.Namespace + "|" + r.Name + "|" + rule.Raw
}

// resolved reports whether a value has fallen far enough below the threshold to
// count as recovered.
func (w *Watcher) resolved(rule Rule, observed float64) bool {
	var threshold float64
	if rule.IsPercent {
		threshold = rule.Percent
	} else {
		threshold = float64(rule.Absolute)
	}
	margin := threshold * w.cfg.Hysteresis
	if rule.Op == Below {
		return observed > threshold+margin
	}
	return observed < threshold-margin
}

// Evaluate walks rows against the rules and returns the alerts to deliver.
//
// Rows are walked recursively so a rule can catch a hot container inside an
// otherwise unremarkable pod, which is exactly the case a workload-level
// average hides.
func (w *Watcher) Evaluate(rows []*model.Row) []Alert {
	now := w.now()
	seen := map[string]bool{}
	var out []Alert

	var walk func(rs []*model.Row)
	walk = func(rs []*model.Row) {
		for _, r := range rs {
			if r.MetricsMissing {
				walk(r.Children)
				continue
			}
			for _, rule := range w.cfg.Rules {
				breached, applicable, observed := rule.Evaluate(r.Usage)
				if !applicable {
					continue
				}
				k := key(r, rule)
				seen[k] = true
				st, exists := w.state[k]

				if breached {
					if !exists {
						st = &alertState{firstSeen: now}
						w.state[k] = st
					}
					st.lastSeen = now
					st.observed = observed

					if now.Sub(st.firstSeen) < w.cfg.MinDuration {
						continue
					}
					shouldNotify := !st.notified ||
						(w.cfg.Repeat > 0 && now.Sub(st.notifiedAt) >= w.cfg.Repeat)
					if shouldNotify {
						st.notified = true
						st.notifiedAt = now
						out = append(out, Alert{Rule: rule, Row: r, Observed: observed, Firing: true, Since: st.firstSeen})
					}
					continue
				}

				// Not breached: clear only once past the hysteresis margin, so
				// a value hovering at the threshold does not flap.
				if exists && st.notified && w.resolved(rule, observed) {
					if w.cfg.NotifyResolved {
						out = append(out, Alert{Rule: rule, Row: r, Observed: observed, Firing: false, Since: st.firstSeen})
					}
					delete(w.state, k)
				} else if exists && !st.notified && w.resolved(rule, observed) {
					// Never got past MinDuration; drop it silently.
					delete(w.state, k)
				} else if exists {
					st.lastSeen = now
					st.observed = observed
				}
			}
			walk(r.Children)
		}
	}
	walk(rows)

	// Forget rows that vanished. A deleted pod is not a resolved alert, and
	// notifying "resolved" for something that no longer exists is noise; but
	// keeping the state forever would leak memory in a long watch session.
	for k := range w.state {
		if !seen[k] {
			delete(w.state, k)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Firing != out[j].Firing {
			return out[i].Firing
		}
		return out[i].Row.Name < out[j].Row.Name
	})
	return out
}

// Firing returns the currently-breached alert keys, for the TUI banner.
func (w *Watcher) Firing() int {
	n := 0
	for _, st := range w.state {
		if st.notified {
			n++
		}
	}
	return n
}
