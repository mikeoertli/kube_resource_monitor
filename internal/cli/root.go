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

It reads the same metrics.k8s.io, so it needs metrics-server installed --
run "krm install-metrics-server" if it is not.
Unlike kubectl top it can roll usage up to the Deployment or StatefulSet that
owns a pod, break it down to individual containers, color everything by how
close it is to its limit, and stay open watching it change.

With no subcommand krm opens the interactive view when stdout is a terminal,
and prints a single table otherwise -- so "krm" is interactive and
"krm | grep web" or "krm -o json" are not.`

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
	var watch, once bool

	root := &cobra.Command{
		Use:           "krm",
		Short:         "Monitor Kubernetes resource usage in the terminal",
		Long:          longDescription,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			interactive := watch || (!once && isTerminal() && f.output == "table")
			if interactive {
				return runWatch(cmd, f)
			}
			return runOnce(cmd, f)
		},
	}
	f.register(root)
	root.Flags().BoolVarP(&watch, "watch", "w", false, "open the interactive view even when not writing to a terminal")
	root.Flags().BoolVar(&once, "once", false, "print a single table and exit, even on a terminal")

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
		Short: "Print resource usage once and exit",
		Long: `Print a single snapshot and exit.

This is the scriptable mode: combine it with -o json, -o csv, or -o prometheus
to feed the numbers somewhere else.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runOnce(cmd, f) },
	}
}

func newWatchCommand(f *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "watch",
		Short: "Open the interactive watch view",
		Long: `Open the interactive view and refresh until you quit.

Press ? inside for the full key list. Highlights: t cycles grouping, s cycles
sort, / filters, c toggles container breakdown, + and - change the refresh
interval, and p pauses.`,
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
	r, err := f.resolve(ctx)
	if err != nil {
		return err
	}
	// The interactive view always renders a table; -o json with --watch is a
	// contradiction worth calling out rather than silently ignoring.
	if r.format != render.FormatTable {
		return fmt.Errorf("--output %s cannot be combined with the interactive view; use `krm top -o %s` instead", r.format, r.format)
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
