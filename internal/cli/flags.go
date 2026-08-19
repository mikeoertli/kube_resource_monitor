// Package cli wires the command line to the collector, renderer, and UI.
package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mikeoertli/kube_resource_monitor/internal/inventory"
	"github.com/mikeoertli/kube_resource_monitor/internal/kube"
	"github.com/mikeoertli/kube_resource_monitor/internal/metrics"
	"github.com/mikeoertli/kube_resource_monitor/internal/metricsserver"
	"github.com/mikeoertli/kube_resource_monitor/internal/model"
	"github.com/mikeoertli/kube_resource_monitor/internal/render"
)

// globalFlags holds every flag shared across subcommands.
type globalFlags struct {
	kubeconfig     string
	kubeContext    string
	namespace      string
	allNamespaces  bool
	impersonate    string
	requestTimeout time.Duration

	selector      string
	fieldSelector string
	filter        string
	groupBy       string

	containers     bool
	requests       bool
	limits         bool
	bars           bool
	barStyle       string
	labels         bool
	age            bool
	restarts       bool
	includeMissing bool

	onlyProblems bool
	threshold    float64

	sortBy  string
	reverse bool

	output   string
	color    bool
	noColor  bool
	interval time.Duration

	// demo runs against synthetic data, so the UI can be exercised without a
	// cluster (and so a bug report can be reproduced without one).
	demo bool
}

func (f *globalFlags) register(cmd *cobra.Command) {
	p := cmd.PersistentFlags()

	p.StringVar(&f.kubeconfig, "kubeconfig", "", "path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)")
	p.StringVar(&f.kubeContext, "context", "", "kubeconfig context to use (default: current-context)")
	p.StringVarP(&f.namespace, "namespace", "n", "", "namespace to inspect (default: the context's namespace)")
	p.BoolVarP(&f.allNamespaces, "all-namespaces", "A", false, "inspect every namespace")
	p.StringVar(&f.impersonate, "as", "", "impersonate this user for API requests")
	p.DurationVar(&f.requestTimeout, "request-timeout", 30*time.Second, "timeout for individual API requests")

	p.StringVarP(&f.selector, "selector", "l", "", "label selector, e.g. app=web,tier!=cache")
	p.StringVar(&f.fieldSelector, "field-selector", "", "field selector passed to the pod list, e.g. spec.nodeName=node-1")
	p.StringVarP(&f.filter, "filter", "f", "", "filter by name; a regular expression when it parses as one, otherwise a case-insensitive substring")
	p.StringVarP(&f.groupBy, "group-by", "g", "workload", "workload, pod, container, node, namespace, pvc, deployment, statefulset, daemonset, job")

	p.BoolVarP(&f.containers, "containers", "c", false, "break pods down by container")
	p.BoolVar(&f.requests, "requests", false, "show the requests columns")
	p.BoolVar(&f.limits, "limits", false, "show the limits columns")
	p.BoolVar(&f.bars, "bars", true, "draw usage bars")
	p.StringVar(&f.barStyle, "bar-style", "blocks", "blocks, braille, or ascii")
	p.BoolVar(&f.labels, "show-labels", false, "show a labels column")
	p.BoolVar(&f.age, "show-age", false, "show an age column")
	p.BoolVar(&f.restarts, "show-restarts", false, "show a restarts column")
	p.BoolVar(&f.includeMissing, "include-missing", false, "include pods the metrics API has no sample for")

	p.BoolVar(&f.onlyProblems, "only-problems", false, "show only rows at or above --threshold")
	p.Float64Var(&f.threshold, "threshold", 0.85, "fraction of the limit that counts as a problem")

	p.StringVar(&f.sortBy, "sort-by", "cpu", "cpu, memory, storage, cpu%, mem%, name, restarts")
	p.BoolVar(&f.reverse, "reverse", false, "reverse the sort order")

	p.StringVarP(&f.output, "output", "o", "table", "table, json, csv, or prometheus")
	p.BoolVar(&f.color, "color", false, "force color output even when not writing to a terminal")
	p.BoolVar(&f.noColor, "no-color", false, "disable color output")
	p.DurationVarP(&f.interval, "interval", "i", 5*time.Second, "refresh interval for watch mode")

	p.BoolVar(&f.demo, "demo", false, "run against synthetic data instead of a cluster")
}

// resolved is everything the flags produced, after validation and connection.
type resolved struct {
	client    *kube.Client
	provider  metrics.Provider
	collector *inventory.Collector
	invOpts   inventory.Options
	rendOpts  render.Options
	palette   *render.Palette
	format    render.Format
	sortKey   model.SortKey
	// contextName and namespace are shown in status lines.
	contextName string
	namespace   string
	source      string
}

func parseSortKey(s string) (model.SortKey, error) {
	switch k := model.SortKey(s); k {
	case model.SortName, model.SortCPU, model.SortMemory, model.SortStorage,
		model.SortCPUPercent, model.SortMemPercent, model.SortRestarts:
		return k, nil
	case "mem":
		return model.SortMemory, nil
	case "cpu-percent", "cpupercent":
		return model.SortCPUPercent, nil
	case "mem-percent", "mempercent":
		return model.SortMemPercent, nil
	case "":
		return model.SortCPU, nil
	default:
		return "", fmt.Errorf("unknown --sort-by %q (want cpu, memory, storage, cpu%%, mem%%, name, or restarts)", s)
	}
}

func parseBarStyle(s string) (render.BarStyle, error) {
	switch b := render.BarStyle(s); b {
	case render.BarBlocks, render.BarASCII, render.BarBraille:
		return b, nil
	case "":
		return render.BarBlocks, nil
	default:
		return "", fmt.Errorf("unknown --bar-style %q (want blocks, braille, or ascii)", s)
	}
}

// resolve validates flags and connects.
//
// Validation happens before connecting so a typo in --group-by fails instantly
// rather than after a kubeconfig round trip.
func (f *globalFlags) resolve(ctx context.Context) (*resolved, error) {
	groupBy, err := inventory.ParseGroupBy(f.groupBy)
	if err != nil {
		return nil, err
	}
	format, err := render.ParseFormat(f.output)
	if err != nil {
		return nil, err
	}
	sortKey, err := parseSortKey(f.sortBy)
	if err != nil {
		return nil, err
	}
	barStyle, err := parseBarStyle(f.barStyle)
	if err != nil {
		return nil, err
	}
	if f.threshold <= 0 || f.threshold > 10 {
		return nil, fmt.Errorf("--threshold must be a fraction greater than 0 (got %v); use 0.85 for 85%%", f.threshold)
	}
	if f.interval < time.Second {
		return nil, fmt.Errorf("--interval must be at least 1s (got %v); metrics-server only scrapes every 15s by default, so anything faster just re-reads the same numbers", f.interval)
	}

	r := &resolved{
		format:  format,
		sortKey: sortKey,
		invOpts: inventory.Options{
			LabelSelector:     f.selector,
			FieldSelector:     f.fieldSelector,
			NamePattern:       f.filter,
			GroupBy:           groupBy,
			IncludeContainers: f.containers || groupBy == inventory.GroupContainer,
			IncludeMissing:    f.includeMissing,
			OnlyProblems:      f.onlyProblems,
			ProblemThreshold:  f.threshold,
		},
		rendOpts: render.Options{
			ShowRequests: f.requests,
			ShowLimits:   f.limits,
			ShowBars:     f.bars && format == render.FormatTable,
			ShowNode:     groupBy == inventory.GroupPod || groupBy == inventory.GroupContainer,
			ShowAge:      f.age,
			ShowRestarts: f.restarts,
			ShowReady:    true,
			ShowLabels:   f.labels,
			Storage:      groupBy == inventory.GroupPVC,
			BarWidth:     12,
			BarStyle:     barStyle,
			Thresholds:   render.DefaultThresholds,
		},
	}
	if r.rendOpts.Storage && (sortKey == model.SortCPU || sortKey == model.SortMemory) {
		r.sortKey = model.SortStorage
	}
	r.palette = render.NewPalette(render.ColorEnabled(f.color, f.noColor) && format == render.FormatTable)

	if f.demo {
		return f.resolveDemo(r)
	}

	client, err := kube.Connect(kube.Options{
		Kubeconfig:    f.kubeconfig,
		Context:       f.kubeContext,
		Namespace:     f.namespace,
		AllNamespaces: f.allNamespaces,
		Timeout:       f.requestTimeout,
		Impersonate:   f.impersonate,
		// Watch mode on a busy cluster issues several list calls per tick;
		// the client-go defaults of 5 QPS would throttle us into stale data.
		QPS:   50,
		Burst: 100,
	})
	if err != nil {
		return nil, err
	}

	available, err := client.MetricsAvailable(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not reach the cluster: %w", err)
	}
	if !available {
		status := metricsserver.Detect(ctx, client.Kube, false)
		return nil, &metricsUnavailableError{status: status, kubeContext: client.ContextName}
	}

	r.client = client
	r.provider = metrics.NewLive(client.Metrics, client.Kube)
	r.collector = inventory.New(client.Kube, r.provider)
	r.invOpts.Namespace = client.Namespace
	r.contextName = client.ContextName
	r.namespace = client.Namespace
	r.source = r.provider.Name()
	r.rendOpts.ShowNamespace = client.Namespace == ""
	return r, nil
}

// metricsUnavailableError carries the install guidance rather than just a
// message, so the top-level handler can print the full block without the error
// string itself becoming a wall of text in other contexts.
type metricsUnavailableError struct {
	status      metricsserver.Status
	kubeContext string
}

func (e *metricsUnavailableError) Error() string {
	return "the metrics API (metrics.k8s.io) is not available on this cluster"
}

// Advice returns the long-form guidance.
func (e *metricsUnavailableError) Advice() string {
	return metricsserver.InstallInstructions(e.kubeContext, e.status)
}

func (f *globalFlags) resolveDemo(r *resolved) (*resolved, error) {
	kc, provider := demoCluster()
	r.provider = provider
	r.collector = inventory.New(kc, provider)
	r.invOpts.Namespace = f.namespace
	if f.allNamespaces {
		r.invOpts.Namespace = ""
	}
	r.contextName = "demo"
	r.namespace = r.invOpts.Namespace
	r.source = "demo"
	r.rendOpts.ShowNamespace = r.invOpts.Namespace == ""
	return r, nil
}

// isTerminal reports whether stdout is attached to a terminal, which decides
// whether a bare `krm` opens the interactive view or prints once.
func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
