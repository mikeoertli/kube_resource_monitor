package render

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/mikeoertli/kube_resource_monitor/internal/model"
)

func TestClassifyBands(t *testing.T) {
	th := DefaultThresholds
	cases := []struct {
		f    float64
		ok   bool
		want Severity
	}{
		{0, false, SevUnknown},
		{0.01, true, SevIdle},
		{0.30, true, SevIdle},
		{0.55, true, SevNormal},
		{0.75, true, SevElevated},
		{0.88, true, SevHigh},
		{0.96, true, SevCritical},
		{1.00, true, SevOver},
		{1.40, true, SevOver},
	}
	for _, c := range cases {
		if got := th.Classify(c.f, c.ok); got != c.want {
			t.Errorf("Classify(%v, %v) = %v, want %v", c.f, c.ok, got, c.want)
		}
	}
}

// A pod with no limit must not be colored as if it were healthy; that is a
// different state from "using very little of a known limit".
func TestClassifyWithoutDenominatorIsUnknown(t *testing.T) {
	if got := DefaultThresholds.Classify(0.1, false); got != SevUnknown {
		t.Errorf("got %v, want SevUnknown", got)
	}
}

func TestRowSeverityUsesWorstMetric(t *testing.T) {
	u := model.Usage{
		Used:        model.Amounts{CPUMilli: 100, MemBytes: 970},
		Limits:      model.Amounts{CPUMilli: 1000, MemBytes: 1000},
		HasCPULimit: true, HasMemLimit: true,
	}
	// CPU is idle at 10%, memory is critical at 97%.
	if got := RowSeverity(DefaultThresholds, u); got != SevCritical {
		t.Errorf("RowSeverity = %v, want SevCritical", got)
	}
}

func TestBarWidthAndFill(t *testing.T) {
	full := Bar(1.0, true, 10, BarASCII)
	if full != strings.Repeat("#", 10) {
		t.Errorf("full bar = %q", full)
	}
	empty := Bar(0, true, 10, BarASCII)
	if empty != strings.Repeat("-", 10) {
		t.Errorf("empty bar = %q", empty)
	}
	half := Bar(0.5, true, 10, BarASCII)
	if strings.Count(half, "#") != 5 {
		t.Errorf("half bar = %q, want 5 filled", half)
	}
}

// Over-limit usage is real and must not draw past the column width; the
// numeric percentage carries the overflow instead.
func TestBarClampsAboveOneHundredPercent(t *testing.T) {
	b := Bar(1.4, true, 8, BarASCII)
	if len([]rune(b)) != 8 {
		t.Errorf("bar length = %d, want 8", len([]rune(b)))
	}
	if strings.Contains(b, "-") {
		t.Errorf("an over-limit bar should be completely full, got %q", b)
	}
}

func TestBarWithoutDenominatorIsDotted(t *testing.T) {
	b := Bar(0, false, 6, BarBlocks)
	if b != strings.Repeat("·", 6) {
		t.Errorf("got %q, want a dotted track", b)
	}
}

func TestBarBlocksHaveSubCharacterResolution(t *testing.T) {
	// 84% and 89% must be visually distinguishable at width 10, which is the
	// entire reason for using eighth-blocks.
	a := Bar(0.84, true, 10, BarBlocks)
	b := Bar(0.89, true, 10, BarBlocks)
	if a == b {
		t.Errorf("84%% and 89%% rendered identically: %q", a)
	}
}

func flat(rows []*model.Row) []model.FlatRow {
	return model.Flatten(rows, func(string) bool { return true })
}

func TestTableRendersAlignedColumns(t *testing.T) {
	rows := []*model.Row{
		{Kind: model.KindDeployment, Name: "web", Namespace: "prod", Ready: "2/2",
			Usage: model.Usage{
				Used:        model.Amounts{CPUMilli: 550, MemBytes: 500 << 20},
				Limits:      model.Amounts{CPUMilli: 1000, MemBytes: 1 << 30},
				HasCPULimit: true, HasMemLimit: true, UsedKnown: true,
			}},
		{Kind: model.KindStatefulSet, Name: "database-primary", Namespace: "prod", Ready: "1/1",
			Usage: model.Usage{
				Used:        model.Amounts{CPUMilli: 1500, MemBytes: 3 << 30},
				Limits:      model.Amounts{CPUMilli: 2000, MemBytes: 4 << 30},
				HasCPULimit: true, HasMemLimit: true, UsedKnown: true,
			}},
	}

	tbl := NewTable(NewPalette(false), DefaultOptions())
	header, lines := tbl.Lines(flat(rows))

	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2", len(lines))
	}
	// Every line must occupy the same number of terminal cells as the header,
	// measured in display width rather than bytes: block-drawing glyphs are
	// multi-byte but single-column.
	for i, l := range lines {
		if lipgloss.Width(l) != lipgloss.Width(header) {
			t.Errorf("line %d width %d != header width %d\nheader: %q\nline:   %q",
				i, lipgloss.Width(l), lipgloss.Width(header), header, l)
		}
	}
	if !strings.Contains(lines[0], "web") || !strings.Contains(lines[0], "55%") {
		t.Errorf("row 0 missing expected content: %q", lines[0])
	}
	if !strings.Contains(lines[1], "1.50") {
		t.Errorf("expected fractional cores for 1500m: %q", lines[1])
	}
}

func TestTableShowsDashForUndeclaredLimits(t *testing.T) {
	rows := []*model.Row{{
		Kind: model.KindPod, Name: "nolimits",
		Usage: model.Usage{Used: model.Amounts{CPUMilli: 250}, UsedKnown: true},
	}}
	opts := DefaultOptions()
	opts.ShowLimits = true
	opts.ShowRequests = true
	tbl := NewTable(NewPalette(false), opts)
	_, lines := tbl.Lines(flat(rows))

	if !strings.Contains(lines[0], "-") {
		t.Errorf("expected dashes for undeclared limits: %q", lines[0])
	}
	if strings.Contains(lines[0], "0m  ") && !strings.Contains(lines[0], "250m") {
		t.Errorf("undeclared limits must not render as 0: %q", lines[0])
	}
}

func TestTableMarksMissingMetrics(t *testing.T) {
	rows := []*model.Row{{
		Kind: model.KindPod, Name: "cold", MetricsMissing: true,
		Usage: model.Usage{Limits: model.Amounts{CPUMilli: 500}, HasCPULimit: true},
	}}
	tbl := NewTable(NewPalette(false), DefaultOptions())
	_, lines := tbl.Lines(flat(rows))
	if strings.Contains(lines[0], "0m") {
		t.Errorf("a pod with no sample must not report 0m: %q", lines[0])
	}
}

func TestTableIndentsChildren(t *testing.T) {
	rows := []*model.Row{{
		Kind: model.KindDeployment, Name: "web",
		Children: []*model.Row{{Kind: model.KindPod, Name: "web-1"}},
	}}
	tbl := NewTable(NewPalette(false), DefaultOptions())
	_, lines := tbl.Lines(flat(rows))
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[1], "└") {
		t.Errorf("child row should be visually nested: %q", lines[1])
	}
}

func TestColorEnabledHonorsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if ColorEnabled(false, false) {
		t.Error("NO_COLOR must disable color")
	}
	// An explicit --color flag still wins, since the user asked for it directly.
	if !ColorEnabled(true, false) {
		t.Error("explicit force should override NO_COLOR")
	}
	if ColorEnabled(true, true) {
		t.Error("explicit disable must win over force")
	}
}

func sampleExport() Export {
	rows := []*model.Row{{
		Kind: model.KindDeployment, Name: "web", Namespace: "prod",
		Usage: model.Usage{
			Used:        model.Amounts{CPUMilli: 550, MemBytes: 500 << 20},
			Requests:    model.Amounts{CPUMilli: 300, MemBytes: 256 << 20},
			Limits:      model.Amounts{CPUMilli: 1000, MemBytes: 1 << 30},
			HasCPULimit: true, HasMemLimit: true, HasCPURequest: true, HasMemRequest: true,
			UsedKnown: true,
		},
		Children: []*model.Row{{Kind: model.KindPod, Name: "web-1", Namespace: "prod",
			Usage: model.Usage{Used: model.Amounts{CPUMilli: 250}, UsedKnown: true}}},
	}}
	e := Export{Timestamp: time.Unix(1700000000, 0), GroupBy: "workload", Namespace: "prod"}
	for _, r := range rows {
		e.Rows = append(e.Rows, ToExportRow(r))
	}
	return e
}

func TestJSONExportOmitsUndeclaredFields(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, sampleExport()); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	rows := doc["rows"].([]any)
	web := rows[0].(map[string]any)
	if math.Abs(web["cpuPercentOfLimit"].(float64)-55) > 1e-6 {
		t.Errorf("cpuPercentOfLimit = %v, want 55", web["cpuPercentOfLimit"])
	}

	child := web["children"].([]any)[0].(map[string]any)
	// The child declares no limit, so the percentage key must be absent rather
	// than present-and-zero, which a consumer would read as "0% of limit".
	if _, present := child["cpuLimitMilli"]; present {
		t.Error("an undeclared limit must be omitted, not zero")
	}
	if _, present := child["cpuPercentOfLimit"]; present {
		t.Error("a percentage with no denominator must be omitted")
	}
}

func TestCSVIncludesChildRows(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteCSV(&buf, sampleExport()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("line count = %d, want header + 2 rows:\n%s", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "cpu_percent_of_limit") {
		t.Errorf("missing header column: %q", lines[0])
	}
	if !strings.Contains(buf.String(), "web-1") {
		t.Error("child rows should appear in CSV output")
	}
}

func TestPrometheusOutputIsWellFormed(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePrometheus(&buf, sampleExport()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "# TYPE krm_cpu_usage_millicores gauge") {
		t.Errorf("missing TYPE line:\n%s", out)
	}
	if !strings.Contains(out, `krm_cpu_usage_millicores{kind="Deployment",namespace="prod",name="web",node=""} 550`) {
		t.Errorf("missing or malformed sample:\n%s", out)
	}
	// HELP must appear exactly once per metric name.
	if got := strings.Count(out, "# HELP krm_cpu_usage_millicores"); got != 1 {
		t.Errorf("HELP repeated %d times", got)
	}
}

func TestParseFormat(t *testing.T) {
	for in, want := range map[string]Format{"": FormatTable, "json": FormatJSON, "prom": FormatPrometheus, "csv": FormatCSV} {
		got, err := ParseFormat(in)
		if err != nil || got != want {
			t.Errorf("ParseFormat(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseFormat("yaml"); err == nil {
		t.Error("expected an error for an unsupported format")
	}
}
