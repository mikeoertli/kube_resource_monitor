// Package notify turns threshold rules into alerts and delivers them.
package notify

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/mikeoertli/kube_resource_monitor/internal/model"
)

// Comparison is the direction of a threshold test.
type Comparison string

const (
	Above Comparison = ">"
	Below Comparison = "<"
)

// Rule is one threshold condition.
//
// Rules come in two flavors because both are genuinely useful: relative
// ("cpu>85%" catches a container about to be throttled regardless of its size)
// and absolute ("mem>2Gi" catches a workload growing past what you budgeted,
// even if nobody ever set a limit for it to be a percentage of).
type Rule struct {
	Raw    string
	Metric model.Metric
	Op     Comparison

	// Percent is set for relative rules, expressed as a fraction (0.85).
	Percent float64
	// Basis is the denominator a relative rule measures against.
	Basis model.Basis
	// Absolute is set for absolute rules, in milli-cores or bytes.
	Absolute int64
	// IsPercent distinguishes the two forms.
	IsPercent bool
}

var ruleRE = regexp.MustCompile(`^\s*(cpu|mem|memory|storage|disk)\s*(>=|<=|>|<)\s*([0-9.]+)\s*(%|[A-Za-z]*)\s*(?:(?:of|/)\s*(limit|request|capacity))?\s*$`)

// ParseRule reads a rule expression.
//
// Accepted forms:
//
//	cpu>85%              percent of the best available denominator
//	mem>90%of limit      percent of an explicit denominator
//	cpu>1500m            absolute milli-cores
//	mem>2Gi              absolute bytes
//	storage>80%          percent of provisioned volume capacity
func ParseRule(s string) (Rule, error) {
	m := ruleRE.FindStringSubmatch(s)
	if m == nil {
		return Rule{}, fmt.Errorf("cannot parse rule %q (examples: cpu>85%%, mem>90%%of limit, mem>2Gi)", s)
	}

	r := Rule{Raw: strings.TrimSpace(s)}
	switch m[1] {
	case "cpu":
		r.Metric = model.MetricCPU
	case "mem", "memory":
		r.Metric = model.MetricMemory
	case "storage", "disk":
		r.Metric = model.MetricStorage
	}

	// ">=" and ">" are treated identically: a threshold alert firing at exactly
	// the boundary is what people expect, and the distinction only ever causes
	// confusion in an alerting context.
	if strings.HasPrefix(m[2], "<") {
		r.Op = Below
	} else {
		r.Op = Above
	}

	value, err := strconv.ParseFloat(m[3], 64)
	if err != nil {
		return Rule{}, fmt.Errorf("rule %q: %w", s, err)
	}

	unit := m[4]
	if unit == "%" {
		r.IsPercent = true
		r.Percent = value / 100
		switch m[5] {
		case "limit":
			r.Basis = model.BasisLimit
		case "request":
			r.Basis = model.BasisRequest
		case "capacity":
			r.Basis = model.BasisCapacity
		default:
			r.Basis = "" // best available
		}
		if r.Metric == model.MetricStorage && r.Basis == "" {
			r.Basis = model.BasisCapacity
		}
		return r, nil
	}

	// Absolute: reuse Kubernetes quantity parsing so "1500m", "2Gi", and "0.5"
	// all mean what a kubectl user expects them to mean.
	q, err := resource.ParseQuantity(m[3] + unit)
	if err != nil {
		return Rule{}, fmt.Errorf("rule %q: %w", s, err)
	}
	if r.Metric == model.MetricCPU {
		r.Absolute = q.MilliValue()
	} else {
		r.Absolute = q.Value()
	}
	return r, nil
}

// ParseRules parses a list, reporting every failure at once rather than
// stopping at the first, so a user fixing a typo sees all of them.
func ParseRules(exprs []string) ([]Rule, error) {
	var rules []Rule
	var errs []string
	for _, e := range exprs {
		r, err := ParseRule(e)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		rules = append(rules, r)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return rules, nil
}

// Evaluate tests a usage against the rule.
//
// The second return value reports whether the rule was applicable at all: a
// percentage rule against a workload with no limit cannot be evaluated, and
// silently treating that as "not breached" would be indistinguishable from
// "healthy". Callers surface the difference.
func (r Rule) Evaluate(u model.Usage) (breached bool, applicable bool, observed float64) {
	if r.IsPercent {
		var f float64
		var ok bool
		if r.Basis != "" {
			f, ok = u.Fraction(r.Metric, r.Basis)
		} else {
			f, _, ok = u.BestFraction(r.Metric)
		}
		if !ok {
			return false, false, 0
		}
		if r.Op == Below {
			return f < r.Percent, true, f
		}
		return f >= r.Percent, true, f
	}

	v := u.Value(r.Metric)
	if r.Op == Below {
		return v < r.Absolute, true, float64(v)
	}
	return v >= r.Absolute, true, float64(v)
}

// Describe renders the rule for alert text.
func (r Rule) Describe() string {
	metric := string(r.Metric)
	if r.IsPercent {
		basis := string(r.Basis)
		if basis == "" {
			basis = "limit"
		}
		return fmt.Sprintf("%s %s %s of %s", metric, r.Op, model.FormatPercent(r.Percent, true), basis)
	}
	return fmt.Sprintf("%s %s %s", metric, r.Op, model.FormatMetric(r.Metric, r.Absolute))
}

// FormatObserved renders the measured value in the rule's own terms.
func (r Rule) FormatObserved(observed float64) string {
	if r.IsPercent {
		return model.FormatPercent(observed, true)
	}
	return model.FormatMetric(r.Metric, int64(observed))
}
