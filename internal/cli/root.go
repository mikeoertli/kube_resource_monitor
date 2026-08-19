package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/mikeoertli/kube_resource_monitor/internal/inventory"
	"github.com/mikeoertli/kube_resource_monitor/internal/kube"
	"github.com/mikeoertli/kube_resource_monitor/internal/model"
	"github.com/mikeoertli/kube_resource_monitor/internal/render"
	"github.com/mikeoertli/kube_resource_monitor/internal/tui"
)

const longDescription = `krm shows what your Kubernetes workloads are actually consuming,
with the requests and limits they were promised alongside.

MODES
  krm          live view, refreshing until you quit    (stdout is a terminal)
  krm          one table, then exit                    (piped or redirected)
  krm top      one table, then exit                    (always)
  krm watch    live view                               (always)
  krm notify   watch thresholds, send notifications

So "krm" on its own opens the interactive view, while "krm | grep web" and
"krm -o json" print plain output you can pipe. Reach for "krm top" when you
want a single snapshot without leaving your scrollback.

It reads the same metrics.k8s.io API that kubectl top uses, so it needs
metrics-server installed -- run "krm install-metrics-server" if it is not.
Unlike kubectl top it can roll usage up to the Deployment or StatefulSet that
owns a pod, break it down to individual containers, color everything by how
close it is to its limit, and stay open watching it change.`

const rootExamples = `  # live view of the current context and namespace
  krm

  # a single snapshot, even on a terminal
  krm top

  # every namespace, broken down by container
  krm -A -c

  # only what is close to its limit
  krm --only-problems

  # feed the numbers somewhere else
  krm top -o json | jq '.rows[]'

  # alert when anything passes 85% of its CPU limit
  krm notify --on 'cpu>85%'`

// Execute runs the root command.
func Execute() int {
	root := newRootCommand()
	if err := root.Execute(); err != nil {
		// Metrics being unavailable is the one failure worth a paragraph rather
		// than a line: it is the most common first-run problem and it has a
		// concrete fix the user can copy.
		var mu *metricsUnavailableError
		if errors.As(err, &mu) {
			fmt.Fprintf(os.Stderr, "\n%s\n\n%s\n", mu.Error(), mu.Advice())
			return 1
		}
		var ec *exitCodeError
		if errors.As(err, &ec) {
			if ec.msg != "" {
				fmt.Fprintf(os.Stderr, "%s\n", ec.msg)
			}
			return ec.Code()
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func newRootCommand() *cobra.Command {
	f := &globalFlags{}

	root := &cobra.Command{
		Use:     "krm",
		Short:   "Monitor Kubernetes resource usage — live view on a terminal, one table when piped",
		Long:    longDescription,
		Example: rootExamples,

		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Bare `krm` picks its mode from the environment, the way git
			// reaches for a pager only on a terminal. There is deliberately no
			// flag to override this: `krm top` and `krm watch` name the two
			// modes outright, and a third and fourth way to say the same thing
			// made the surface harder to hold in your head, not easier.
			if isTerminal() && f.output == "table" {
				return runWatch(cmd, f)
			}
			return runOnce(cmd, f)
		},
	}
	f.register(root)

	root.AddCommand(
		newTopCommand(f),
		newWatchCommand(f),
		newNotifyCommand(f),
		newInstallCommand(f),
		newContextsCommand(f),
		newVersionCommand(),
	)
	return root
}

func newTopCommand(f *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "top",
		Short: "Print one table and exit (no live view)",
		Long: `Print a single snapshot and exit.

Use this when you want numbers in your scrollback rather than a full-screen
view -- bare "krm" on a terminal opens the live view instead.

This is also the scriptable mode: combine it with -o json, -o csv, or
-o prometheus to feed the numbers somewhere else.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runOnce(cmd, f) },
	}
}

func newWatchCommand(f *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "watch",
		Short: "Open the live view (what bare `krm` does on a terminal)",
		Long: `Open the interactive view and refresh until you quit.

This is what bare "krm" already does when stdout is a terminal; naming it
explicitly is useful in aliases and documentation, where relying on terminal
detection would be obscure.

Press ? inside for the full key list. Highlights: t cycles grouping, s cycles
sort, / filters, c toggles container breakdown, + and - change the refresh
interval, p pauses, and Q quits.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runWatch(cmd, f) },
	}
}

func newContextsCommand(f *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "contexts",
		Short: "List the kubeconfig contexts krm can use",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctxs, err := kube.ListContexts(f.kubeconfig)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			for _, c := range ctxs {
				marker := " "
				if c.Current {
					marker = "*"
				}
				ns := c.Namespace
				if ns == "" {
					ns = "default"
				}
				fmt.Fprintf(w, "%s %-40s cluster=%-30s namespace=%s\n", marker, c.Name, c.Cluster, ns)
			}
			return nil
		},
	}
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			v, commit, date := kube.BuildInfo()
			fmt.Fprintf(cmd.OutOrStdout(), "krm %s (commit %s, built %s)\n", v, commit, date)
		},
	}
}

// runOnce collects a single snapshot and writes it in the requested format.
func runOnce(cmd *cobra.Command, f *globalFlags) error {
	ctx := cmd.Context()
	r, err := f.resolve(ctx)
	if err != nil {
		return err
	}

	snap, err := r.collector.Collect(ctx, r.invOpts)
	if err != nil {
		return err
	}
	model.Sort(snap.Rows, r.sortKey, !f.reverse)

	out := cmd.OutOrStdout()
	switch r.format {
	case render.FormatTable:
		return writeTable(out, r, snap, f)
	default:
		return writeMachine(out, r, snap)
	}
}

func writeTable(out io.Writer, r *resolved, snap *inventory.Snapshot, f *globalFlags) error {
	// One-shot output expands every subtree: there is no cursor to drill down
	// with, so collapsing subtrees would only make --containers useless.
	flat := model.Flatten(snap.Rows, func(string) bool { return true })
	tbl := render.NewTable(r.palette, r.rendOpts)

	for _, w := range snap.Warnings {
		fmt.Fprintln(out, r.palette.Warning.Render("! "+w))
	}
	if len(flat) == 0 {
		fmt.Fprintln(out, r.palette.Muted.Render(emptyHint(snap, f)))
		return nil
	}

	fmt.Fprint(out, tbl.Render(flat))
	fmt.Fprintln(out, tbl.TotalsLine(snap.Totals, len(flat)))
	if snap.MissingMetrics > 0 && !f.includeMissing {
		fmt.Fprintln(out, r.palette.Muted.Render(fmt.Sprintf(
			"  %d pod(s) had no metrics sample and were omitted; pass --include-missing to show them",
			snap.MissingMetrics)))
	}
	return nil
}

func emptyHint(snap *inventory.Snapshot, f *globalFlags) string {
	switch {
	case f.filter != "":
		return fmt.Sprintf("nothing matches --filter %q", f.filter)
	case f.selector != "":
		return fmt.Sprintf("nothing matches --selector %q", f.selector)
	case snap.MissingMetrics > 0:
		return fmt.Sprintf("%d pod(s) exist but none have a metrics sample yet; metrics-server needs one scrape interval (~15s) after a pod starts",
			snap.MissingMetrics)
	default:
		return "no workloads found"
	}
}

func writeMachine(out io.Writer, r *resolved, snap *inventory.Snapshot) error {
	e := render.Export{
		Timestamp: snap.Taken,
		Context:   r.contextName,
		Namespace: r.namespace,
		GroupBy:   string(snap.GroupBy),
		Warnings:  snap.Warnings,
		Totals: render.ExportSummary{
			CPUMilli:      snap.Totals.Used.CPUMilli,
			CPULimitMilli: snap.Totals.Limits.CPUMilli,
			MemBytes:      snap.Totals.Used.MemBytes,
			MemLimitBytes: snap.Totals.Limits.MemBytes,
			RowCount:      len(snap.Rows),
		},
	}
	if snap.Window > 0 {
		e.Window = snap.Window.String()
	}
	for _, row := range snap.Rows {
		e.Rows = append(e.Rows, render.ToExportRow(row))
	}

	switch r.format {
	case render.FormatJSON:
		return render.WriteJSON(out, e)
	case render.FormatCSV:
		return render.WriteCSV(out, e)
	case render.FormatPrometheus:
		return render.WritePrometheus(out, e)
	}
	return fmt.Errorf("unsupported format %q", r.format)
}

// runWatch opens the interactive view.
func runWatch(cmd *cobra.Command, f *globalFlags) error {
	ctx := cmd.Context()

	// Both guards run before connecting to the cluster, so a mistake in the
	// command line fails immediately rather than after a kubeconfig round trip.
	// Format is checked first because it names a concrete mistake in what the
	// user typed, while "not a terminal" is a property of how they ran it.

	// The interactive view always renders a table, so `krm watch -o json` is a
	// contradiction worth calling out rather than silently ignoring.
	if format, err := render.ParseFormat(f.output); err == nil && format != render.FormatTable {
		return fmt.Errorf("--output %s cannot be combined with the live view; use `krm top -o %s` instead", format, format)
	}
	// This only bites on an explicit `krm watch`, since bare `krm` already
	// routes to the one-shot path when stdout is not a terminal. Without it,
	// `krm watch | tee log` would write cursor-positioning escapes into the
	// file and appear to hang.
	if !isTerminal() {
		return fmt.Errorf("the live view needs a terminal and stdout is not one; use `krm top` to print a single table")
	}

	r, err := f.resolve(ctx)
	if err != nil {
		return err
	}

	return tui.Run(tui.Config{
		Collector:   r.collector,
		Options:     r.invOpts,
		Render:      r.rendOpts,
		Palette:     render.NewPalette(!f.noColor),
		Interval:    f.interval,
		ContextName: r.contextName,
		Namespace:   r.namespace,
		Source:      r.source,
		Sort:        r.sortKey,
		Descending:  !f.reverse,
	})
}
