package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mikeoertli/kube_resource_monitor/internal/notify"
)

func newNotifyCommand(f *globalFlags) *cobra.Command {
	var (
		rules       []string
		repeat      time.Duration
		minDuration time.Duration
		hysteresis  float64
		noResolved  bool
		stdoutOnly  bool
		alsoStdout  bool
		once        bool
		exitOnAlert bool
	)

	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Watch for threshold breaches and send desktop notifications",
		Long: `Watch resource usage and notify when a threshold is crossed.

Rules are given with --on and may be repeated. Both relative and absolute forms
work:

  krm notify --on 'cpu>85%'
  krm notify --on 'mem>90% of request' --on 'cpu>1500m'
  krm notify -g pvc --on 'storage>80%'

A rule fires once when it is crossed and stays quiet until it recovers, so a
workload sitting at 90%% does not notify every refresh. Recovery requires
falling past a hysteresis margin below the threshold, which stops a value
hovering right at the line from flapping between firing and resolved.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(rules) == 0 {
				return fmt.Errorf("notify mode needs at least one --on rule (for example: --on 'cpu>85%%')")
			}
			parsed, err := notify.ParseRules(rules)
			if err != nil {
				return err
			}

			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			r, err := f.resolve(ctx)
			if err != nil {
				return err
			}

			cfg := notify.DefaultConfig()
			cfg.Rules = parsed
			cfg.Repeat = repeat
			cfg.MinDuration = minDuration
			cfg.NotifyResolved = !noResolved
			if hysteresis > 0 {
				cfg.Hysteresis = hysteresis
			}
			watcher := notify.NewWatcher(cfg)

			var notifier notify.Notifier
			switch {
			case stdoutOnly:
				notifier = notify.Writer{W: cmd.OutOrStdout()}
			case alsoStdout:
				notifier = notify.Multi{notify.NewDesktop(), notify.Writer{W: cmd.OutOrStdout()}}
			default:
				d := notify.NewDesktop()
				// A headless box has no notification daemon; falling back to
				// stdout keeps notify mode usable over SSH rather than failing
				// on every alert.
				if d.Name() == "unavailable" {
					fmt.Fprintln(cmd.ErrOrStderr(),
						"no desktop notification mechanism found; falling back to stdout "+
							"(install terminal-notifier on macOS or notify-send on Linux)")
					notifier = notify.Writer{W: cmd.OutOrStdout()}
				} else {
					notifier = d
				}
			}

			fmt.Fprintf(cmd.ErrOrStderr(), "watching %s every %s via %s; %d rule(s), delivering by %s\n",
				r.contextName, f.interval, r.source, len(parsed), notifier.Name())
			for _, p := range parsed {
				fmt.Fprintf(cmd.ErrOrStderr(), "  · %s\n", p.Describe())
			}

			breached := false
			evaluate := func() error {
				snap, err := r.collector.Collect(ctx, r.invOpts)
				if err != nil {
					return err
				}
				for _, a := range watcher.Evaluate(snap.Rows) {
					if a.Firing {
						breached = true
					}
					if err := notifier.Notify(ctx, a); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "notification failed: %v\n", err)
					}
				}
				return nil
			}

			if err := evaluate(); err != nil {
				return err
			}
			if once {
				if exitOnAlert && breached {
					// A distinct exit code makes this usable straight from a
					// shell script or a cron job without parsing output.
					return &exitCodeError{code: 2, msg: "threshold breached"}
				}
				return nil
			}

			t := time.NewTicker(f.interval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					fmt.Fprintln(cmd.ErrOrStderr(), "\nstopped")
					return nil
				case <-t.C:
					if err := evaluate(); err != nil {
						// A transient API error should not kill a long-running
						// watch; log it and try again on the next tick.
						fmt.Fprintf(cmd.ErrOrStderr(), "collection failed: %v\n", err)
					}
				}
			}
		},
	}

	cmd.Flags().StringArrayVar(&rules, "on", nil, "threshold rule, repeatable (cpu>85%, mem>90% of request, mem>2Gi, storage>80%)")
	cmd.Flags().DurationVar(&repeat, "repeat", 0, "re-notify a still-firing alert after this interval (0 = notify once)")
	cmd.Flags().DurationVar(&minDuration, "for", 0, "require a breach to persist this long before notifying")
	cmd.Flags().Float64Var(&hysteresis, "hysteresis", 0.10, "fraction below the threshold a value must fall before the alert resolves")
	cmd.Flags().BoolVar(&noResolved, "no-resolved", false, "do not notify when a breach clears")
	cmd.Flags().BoolVar(&stdoutOnly, "stdout", false, "print alerts to stdout instead of sending desktop notifications")
	cmd.Flags().BoolVar(&alsoStdout, "also-stdout", false, "print alerts to stdout in addition to desktop notifications")
	cmd.Flags().BoolVar(&once, "once", false, "evaluate once and exit instead of watching")
	cmd.Flags().BoolVar(&exitOnAlert, "exit-code", false, "with --once, exit 2 when any rule is breached")
	return cmd
}

// exitCodeError lets a command choose its own process exit status.
type exitCodeError struct {
	code int
	msg  string
}

func (e *exitCodeError) Error() string { return e.msg }

// Code returns the desired exit status.
func (e *exitCodeError) Code() int { return e.code }
