// Package metricsserver detects whether metrics-server is present and helps
// install it when it is not.
package metricsserver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ManifestURL is the upstream components manifest.
const ManifestURL = "https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml"

// HighAvailabilityManifestURL is the multi-replica variant, for clusters with
// three or more nodes.
const HighAvailabilityManifestURL = "https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/high-availability.yaml"

// Status describes what we found.
type Status struct {
	// APIAvailable is true when metrics.k8s.io/v1beta1 is served.
	APIAvailable bool
	// DeploymentFound is true when a metrics-server Deployment exists, which
	// distinguishes "not installed" from "installed but not yet serving".
	DeploymentFound bool
	Namespace       string
	Ready           int32
	Desired         int32
	// Hint explains the state in a sentence.
	Hint string
}

// Healthy reports whether metrics can actually be read right now.
func (s Status) Healthy() bool { return s.APIAvailable }

// candidateNamespaces are the places metrics-server conventionally lives.
var candidateNamespaces = []string{"kube-system", "metrics-server", "monitoring"}

// Detect works out what is going on with metrics-server.
//
// The distinction it draws is the point: "no metrics-server at all" is an
// install problem the user can fix in one command, while "deployment exists but
// zero replicas ready" is usually a kubelet TLS problem that installing again
// will not solve. Telling someone to reinstall when their pod is crash-looping
// on certificate verification wastes their afternoon.
func Detect(ctx context.Context, kc kubernetes.Interface, apiAvailable bool) Status {
	s := Status{APIAvailable: apiAvailable}

	var dep *appsv1.Deployment
	for _, ns := range candidateNamespaces {
		d, err := kc.AppsV1().Deployments(ns).Get(ctx, "metrics-server", metav1.GetOptions{})
		if err == nil {
			dep = d
			s.Namespace = ns
			break
		}
		if !errors.IsNotFound(err) && !errors.IsForbidden(err) {
			break
		}
	}

	if dep != nil {
		s.DeploymentFound = true
		s.Ready = dep.Status.ReadyReplicas
		s.Desired = 1
		if dep.Spec.Replicas != nil {
			s.Desired = *dep.Spec.Replicas
		}
	}

	switch {
	case s.APIAvailable:
		s.Hint = "metrics.k8s.io is available"
	case s.DeploymentFound && s.Ready == 0:
		s.Hint = fmt.Sprintf("metrics-server is deployed in %s but has 0/%d replicas ready. "+
			"The usual cause is the kubelet serving certificate not being verifiable; "+
			"check `kubectl -n %s logs deploy/metrics-server`.", s.Namespace, s.Desired, s.Namespace)
	case s.DeploymentFound:
		s.Hint = fmt.Sprintf("metrics-server is running in %s but metrics.k8s.io is not being served yet. "+
			"The APIService can take a minute to become available after startup; "+
			"check `kubectl get apiservice v1beta1.metrics.k8s.io`.", s.Namespace)
	default:
		s.Hint = "metrics-server does not appear to be installed"
	}
	return s
}

// InstallCommand returns the exact command to run.
func InstallCommand(kubeContext string, highAvailability bool) string {
	url := ManifestURL
	if highAvailability {
		url = HighAvailabilityManifestURL
	}
	ctxFlag := ""
	if kubeContext != "" {
		ctxFlag = " --context " + shellQuote(kubeContext)
	}
	return fmt.Sprintf("kubectl%s apply -f %s", ctxFlag, url)
}

// InstallInstructions is the full guidance block shown when metrics are
// unavailable, including the caveats that actually bite people.
func InstallInstructions(kubeContext string, s Status) string {
	var b strings.Builder

	b.WriteString(s.Hint)
	b.WriteString("\n\n")

	if s.DeploymentFound {
		b.WriteString("Since a deployment already exists, reinstalling is unlikely to help.\n")
		b.WriteString("Inspect it first:\n\n")
		fmt.Fprintf(&b, "  kubectl -n %s logs deploy/metrics-server --tail=50\n", s.Namespace)
		fmt.Fprintf(&b, "  kubectl -n %s describe deploy/metrics-server\n", s.Namespace)
		b.WriteString("  kubectl get apiservice v1beta1.metrics.k8s.io -o yaml\n")
		return b.String()
	}

	b.WriteString("Install it with:\n\n")
	fmt.Fprintf(&b, "  %s\n\n", InstallCommand(kubeContext, false))
	b.WriteString("For a cluster with three or more nodes, the high-availability variant is better:\n\n")
	fmt.Fprintf(&b, "  %s\n\n", InstallCommand(kubeContext, true))
	b.WriteString("Two things commonly go wrong afterwards:\n\n")
	b.WriteString("  1. On kind, k3s, minikube, and most self-managed clusters the kubelet serves a\n")
	b.WriteString("     self-signed certificate that metrics-server cannot verify, and the pod stays\n")
	b.WriteString("     NotReady. The usual fix is to add --kubelet-insecure-tls to its args:\n\n")
	b.WriteString("       kubectl -n kube-system patch deployment metrics-server --type=json \\\n")
	b.WriteString("         -p='[{\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/args/-\",\"value\":\"--kubelet-insecure-tls\"}]'\n\n")
	b.WriteString("     That skips verification of the kubelet's certificate, so prefer fixing the\n")
	b.WriteString("     certificates properly on anything you care about.\n\n")
	b.WriteString("  2. Metrics take one scrape interval (about 15s by default) to appear, and freshly\n")
	b.WriteString("     started pods show no data until then. That is expected, not a failure.\n")
	return b.String()
}

// Install applies the upstream manifest by shelling out to kubectl.
//
// This deliberately uses kubectl rather than applying the manifest through
// client-go. The manifest contains a dozen objects across several API groups
// including an APIService, and reimplementing server-side apply semantics for
// them would be a large amount of code that behaves subtly differently from
// what the user would get by hand. Shelling out means the result is exactly
// what the documented install produces.
func Install(ctx context.Context, kubeContext string, highAvailability, insecureTLS bool, out *os.File) error {
	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		return fmt.Errorf("kubectl not found on PATH; run this manually instead:\n\n  %s",
			InstallCommand(kubeContext, highAvailability))
	}

	url := ManifestURL
	if highAvailability {
		url = HighAvailabilityManifestURL
	}
	args := []string{}
	if kubeContext != "" {
		args = append(args, "--context", kubeContext)
	}
	args = append(args, "apply", "-f", url)

	fmt.Fprintf(out, "$ kubectl %s\n", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, kubectl, args...)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("applying the metrics-server manifest: %w", err)
	}

	if insecureTLS {
		patch := `[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]`
		pargs := []string{}
		if kubeContext != "" {
			pargs = append(pargs, "--context", kubeContext)
		}
		pargs = append(pargs, "-n", "kube-system", "patch", "deployment", "metrics-server",
			"--type=json", "-p="+patch)

		fmt.Fprintf(out, "\n$ kubectl %s\n", strings.Join(pargs, " "))
		pcmd := exec.CommandContext(ctx, kubectl, pargs...)
		pcmd.Stdout = out
		pcmd.Stderr = out
		if err := pcmd.Run(); err != nil {
			return fmt.Errorf("patching metrics-server for insecure kubelet TLS: %w", err)
		}
	}

	fmt.Fprintf(out, "\nApplied. Metrics usually start appearing within a minute.\n")
	return nil
}

// WaitReady polls until the metrics API starts serving, or the context expires.
func WaitReady(ctx context.Context, check func(context.Context) (bool, error), interval time.Duration) error {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		ok, err := check(ctx)
		if err == nil && ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for metrics.k8s.io to become available: %w", ctx.Err())
		case <-t.C:
		}
	}
}

// shellQuote makes a string safe to paste into a shell command.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '@') {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
