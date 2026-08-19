package render

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/mikeoertli/kube_resource_monitor/internal/model"
)

// Format selects an output encoding.
type Format string

const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
	FormatCSV   Format = "csv"
	// FormatPrometheus emits the same numbers in the exposition format, so a
	// one-shot run can be scraped by a textfile collector or piped into
	// pushgateway without a second tool.
	FormatPrometheus Format = "prometheus"
)

// ParseFormat validates an --output value.
func ParseFormat(s string) (Format, error) {
	switch f := Format(s); f {
	case FormatTable, FormatJSON, FormatCSV, FormatPrometheus:
		return f, nil
	case "":
		return FormatTable, nil
	case "prom":
		return FormatPrometheus, nil
	case "text":
		return FormatTable, nil
	default:
		return "", fmt.Errorf("unknown output format %q (want table, json, csv, or prometheus)", s)
	}
}

// Export is the JSON shape. It is a stable, documented contract rather than a
// dump of internal structs, so scripts built on it survive refactors.
type Export struct {
	Timestamp time.Time     `json:"timestamp"`
	Context   string        `json:"context,omitempty"`
	Namespace string        `json:"namespace,omitempty"`
	GroupBy   string        `json:"groupBy"`
	Window    string        `json:"metricsWindow,omitempty"`
	Warnings  []string      `json:"warnings,omitempty"`
	Rows      []ExportRow   `json:"rows"`
	Totals    ExportSummary `json:"totals"`
}

// ExportRow is one row, flattened into explicit units.
type ExportRow struct {
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	Namespace string            `json:"namespace,omitempty"`
	Node      string            `json:"node,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Ready     string            `json:"ready,omitempty"`
	Phase     string            `json:"phase,omitempty"`
	Restarts  int32             `json:"restarts,omitempty"`

	CPUMilli        int64    `json:"cpuMilli"`
	CPURequestMilli *int64   `json:"cpuRequestMilli,omitempty"`
	CPULimitMilli   *int64   `json:"cpuLimitMilli,omitempty"`
	CPUPercent      *float64 `json:"cpuPercentOfLimit,omitempty"`

	MemBytes        int64    `json:"memBytes"`
	MemRequestBytes *int64   `json:"memRequestBytes,omitempty"`
	MemLimitBytes   *int64   `json:"memLimitBytes,omitempty"`
	MemPercent      *float64 `json:"memPercentOfLimit,omitempty"`

	StorageUsedBytes     *int64   `json:"storageUsedBytes,omitempty"`
	StorageCapacityBytes *int64   `json:"storageCapacityBytes,omitempty"`
	StoragePercent       *float64 `json:"storagePercentOfCapacity,omitempty"`

	MetricsMissing bool        `json:"metricsMissing,omitempty"`
	Children       []ExportRow `json:"children,omitempty"`
}

// ExportSummary is the totals block.
type ExportSummary struct {
	CPUMilli      int64 `json:"cpuMilli"`
	CPULimitMilli int64 `json:"cpuLimitMilli,omitempty"`
	MemBytes      int64 `json:"memBytes"`
	MemLimitBytes int64 `json:"memLimitBytes,omitempty"`
	RowCount      int   `json:"rowCount"`
}

func p64(v int64, ok bool) *int64 {
	if !ok {
		return nil
	}
	return &v
}

func pf(v float64, ok bool) *float64 {
	if !ok {
		return nil
	}
	return &v
}

// ToExportRow converts a row and its subtree.
func ToExportRow(r *model.Row) ExportRow {
	u := r.Usage
	cf, cok := u.CPUOfLimit()
	mf, mok := u.MemOfLimit()

	out := ExportRow{
		Kind:      string(r.Kind),
		Name:      r.Name,
		Namespace: r.Namespace,
		Node:      r.Node,
		Labels:    r.Labels,
		Ready:     r.Ready,
		Phase:     r.Phase,
		Restarts:  r.Restarts,

		CPUMilli:        u.Used.CPUMilli,
		CPURequestMilli: p64(u.Requests.CPUMilli, u.HasCPURequest),
		CPULimitMilli:   p64(u.Limits.CPUMilli, u.HasCPULimit),
		CPUPercent:      pf(cf*100, cok),

		MemBytes:        u.Used.MemBytes,
		MemRequestBytes: p64(u.Requests.MemBytes, u.HasMemRequest),
		MemLimitBytes:   p64(u.Limits.MemBytes, u.HasMemLimit),
		MemPercent:      pf(mf*100, mok),

		MetricsMissing: r.MetricsMissing,
	}
	if r.Kind == model.KindPVC || u.Capacity.StorageBytes > 0 {
		sf, sok := u.StorageOfCapacity()
		out.StorageUsedBytes = p64(u.Used.StorageBytes, !r.MetricsMissing)
		out.StorageCapacityBytes = p64(u.Capacity.StorageBytes, u.Capacity.StorageBytes > 0)
		out.StoragePercent = pf(sf*100, sok && !r.MetricsMissing)
	}
	for _, c := range r.Children {
		out.Children = append(out.Children, ToExportRow(c))
	}
	return out
}

// WriteJSON emits the export document.
func WriteJSON(w io.Writer, e Export) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(e)
}

// WriteCSV emits one flat record per row, including children, so spreadsheet
// users get the full breakdown rather than only the top level.
func WriteCSV(w io.Writer, e Export) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	header := []string{
		"timestamp", "kind", "namespace", "name", "node", "ready", "phase", "restarts",
		"cpu_milli", "cpu_request_milli", "cpu_limit_milli", "cpu_percent_of_limit",
		"mem_bytes", "mem_request_bytes", "mem_limit_bytes", "mem_percent_of_limit",
		"storage_used_bytes", "storage_capacity_bytes", "storage_percent",
		"metrics_missing",
	}
	if err := cw.Write(header); err != nil {
		return err
	}

	ts := e.Timestamp.UTC().Format(time.RFC3339)
	var walk func(rows []ExportRow) error
	walk = func(rows []ExportRow) error {
		for _, r := range rows {
			rec := []string{
				ts, r.Kind, r.Namespace, r.Name, r.Node, r.Ready, r.Phase, strconv.Itoa(int(r.Restarts)),
				strconv.FormatInt(r.CPUMilli, 10), optInt(r.CPURequestMilli), optInt(r.CPULimitMilli), optFloat(r.CPUPercent),
				strconv.FormatInt(r.MemBytes, 10), optInt(r.MemRequestBytes), optInt(r.MemLimitBytes), optFloat(r.MemPercent),
				optInt(r.StorageUsedBytes), optInt(r.StorageCapacityBytes), optFloat(r.StoragePercent),
				strconv.FormatBool(r.MetricsMissing),
			}
			if err := cw.Write(rec); err != nil {
				return err
			}
			if err := walk(r.Children); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(e.Rows)
}

// WritePrometheus emits the exposition format.
func WritePrometheus(w io.Writer, e Export) error {
	writeHelp := func(name, help, typ string) error {
		_, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
		return err
	}
	metrics := []struct {
		name, help string
		value      func(ExportRow) (float64, bool)
	}{
		{"krm_cpu_usage_millicores", "Observed CPU usage in millicores.", func(r ExportRow) (float64, bool) {
			return float64(r.CPUMilli), !r.MetricsMissing
		}},
		{"krm_cpu_limit_millicores", "Declared CPU limit in millicores.", func(r ExportRow) (float64, bool) {
			if r.CPULimitMilli == nil {
				return 0, false
			}
			return float64(*r.CPULimitMilli), true
		}},
		{"krm_memory_usage_bytes", "Observed memory usage in bytes.", func(r ExportRow) (float64, bool) {
			return float64(r.MemBytes), !r.MetricsMissing
		}},
		{"krm_memory_limit_bytes", "Declared memory limit in bytes.", func(r ExportRow) (float64, bool) {
			if r.MemLimitBytes == nil {
				return 0, false
			}
			return float64(*r.MemLimitBytes), true
		}},
		{"krm_storage_usage_bytes", "Observed volume usage in bytes.", func(r ExportRow) (float64, bool) {
			if r.StorageUsedBytes == nil {
				return 0, false
			}
			return float64(*r.StorageUsedBytes), true
		}},
	}

	for _, m := range metrics {
		emitted := false
		var walk func(rows []ExportRow) error
		walk = func(rows []ExportRow) error {
			for _, r := range rows {
				v, ok := m.value(r)
				if ok {
					if !emitted {
						if err := writeHelp(m.name, m.help, "gauge"); err != nil {
							return err
						}
						emitted = true
					}
					if _, err := fmt.Fprintf(w, "%s{kind=%q,namespace=%q,name=%q,node=%q} %g\n",
						m.name, r.Kind, r.Namespace, r.Name, r.Node, v); err != nil {
						return err
					}
				}
				if err := walk(r.Children); err != nil {
					return err
				}
			}
			return nil
		}
		if err := walk(e.Rows); err != nil {
			return err
		}
	}
	return nil
}

func optInt(p *int64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatInt(*p, 10)
}

func optFloat(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'f', 1, 64)
}
