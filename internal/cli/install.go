package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mikeoertli/kube_resource_monitor/internal/kube"
	"github.com/mikeoertli/kube_resource_monitor/internal/metricsserver"
)

func newInstallCommand(f *globalFlags) *cobra.Command {
	var (
		apply       bool
		ha          bool
		insecureTLS bool
		wait        time.Duration
	)

	cmd := &cobra.Command{
		Use:     "install-metrics-server",
		Aliases: []string{"install"},
		Short:   "Check for metrics-server and print or run the install",
		Long: `Report whether metrics-server is present and, if not, show exactly how to
install it.

By default this only prints -- nothing is changed in your cluster. Pass --apply
to actually run the install, which shells out to kubectl so the result is
identical to doing it by hand.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			client, err := kube.Connect(kube.Options{
				Kubeconfig:  f.kubeconfig,
				Context:     f.kubeContext,
				Namespace:   f.namespace,
				Timeout:     f.requestTimeout,
				Impersonate: f.impersonate,
			})
			if err != nil {
				return err
			}

			available, err := client.MetricsAvailable(ctx)
			if err != nil {
				return fmt.Errorf("could not reach the cluster: %w", err)
			}
			status := metricsserver.Detect(ctx, client.Kube, available)

			fmt.Fprintf(out, "context:  %s\n", client.ContextName)
			fmt.Fprintf(out, "cluster:  %s\n", client.ClusterName)
			fmt.Fprintf(out, "metrics.k8s.io: %s\n\n", yesNo(status.APIAvailable))

			if status.Healthy() {
				fmt.Fprintln(out, "metrics-server is working; nothing to do.")
				if status.DeploymentFound {
					fmt.Fprintf(out, "(deployment metrics-server in %s, %d/%d ready)\n",
						status.Namespace, status.Ready, status.Desired)
				}
				return nil
			}

			if !apply {
				fmt.Fprintln(out, metricsserver.InstallInstructions(client.ContextName, status))
				if !status.DeploymentFound {
					fmt.Fprintln(out, "\nRe-run with --apply to have krm run the install for you.")
				}
				return nil
			}

			if status.DeploymentFound {
				return fmt.Errorf("metrics-server is already deployed in %s but not serving; "+
					"reinstalling will not fix that. %s", status.Namespace, status.Hint)
			}

			if err := metricsserver.Install(ctx, client.ContextName, ha, insecureTLS, os.Stdout); err != nil {
				return err
			}

			if wait > 0 {
				fmt.Fprintf(out, "\nwaiting up to %s for metrics.k8s.io to start serving…\n", wait)
				wctx, cancel := context.WithTimeout(ctx, wait)
				defer cancel()
				err := metricsserver.WaitReady(wctx, client.MetricsAvailable, 3*time.Second)
				if err != nil {
					fmt.Fprintf(out, "\n%v\n\n", err)
					fmt.Fprintln(out, "This usually means the kubelet's serving certificate cannot be verified.")
					fmt.Fprintln(out, "Re-run with --insecure-kubelet-tls, or check:")
					fmt.Fprintln(out, "  kubectl -n kube-system logs deploy/metrics-server --tail=50")
					return &exitCodeError{code: 1, msg: "metrics-server did not become ready"}
				}
				fmt.Fprintln(out, "metrics.k8s.io is serving. Try `krm` now.")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&apply, "apply", false, "actually install metrics-server instead of just printing the command")
	cmd.Flags().BoolVar(&ha, "high-availability", false, "install the multi-replica manifest (needs three or more nodes)")
	cmd.Flags().BoolVar(&insecureTLS, "insecure-kubelet-tls", false,
		"add --kubelet-insecure-tls, which most local and self-managed clusters need but which skips kubelet certificate verification")
	cmd.Flags().DurationVar(&wait, "wait", 90*time.Second, "how long to wait for the metrics API after installing (0 to skip)")
	return cmd
}

func yesNo(b bool) string {
	if b {
		return "available"
	}
	return "not available"
}
