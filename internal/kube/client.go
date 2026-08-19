// Package kube handles everything between a kubeconfig on disk and a usable
// set of typed clients: context selection, namespace resolution, and the
// metrics API availability check.
package kube

import (
	"context"
	"fmt"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/mikeoertli/kube_resource_monitor/internal/buildinfo"
)

// MetricsGroupVersion is the API group the metrics-server serves.
const MetricsGroupVersion = "metrics.k8s.io/v1beta1"

// Options selects which cluster and namespace to talk to.
type Options struct {
	// Kubeconfig overrides the default kubeconfig discovery chain
	// ($KUBECONFIG, then ~/.kube/config). Empty means "use the default".
	Kubeconfig string
	// Context selects a kubeconfig context. Empty means current-context.
	Context string
	// Namespace overrides the context's namespace. Empty means "whatever the
	// selected context says", which is what a kubectl user expects.
	Namespace string
	// AllNamespaces makes namespace-scoped lists cluster-wide.
	AllNamespaces bool
	// Timeout bounds individual API requests.
	Timeout time.Duration
	// Impersonate, when set, issues requests as another user (--as).
	Impersonate string
	// QPS and Burst raise client-side rate limits. Watch mode on a large
	// cluster can otherwise trip the default 5 QPS limiter and stall.
	QPS   float32
	Burst int
}

// Client bundles the typed clients plus the resolved context/namespace, so the
// rest of the program never has to re-derive them.
type Client struct {
	Kube      kubernetes.Interface
	Metrics   metricsv.Interface
	Discovery discovery.DiscoveryInterface
	Config    *rest.Config

	// ContextName is the kubeconfig context actually in use.
	ContextName string
	// Namespace is the resolved namespace, empty when AllNamespaces is set.
	Namespace string
	// ClusterName is the cluster the context points at, for display.
	ClusterName string
}

// Connect builds clients from a kubeconfig.
//
// Resolution order deliberately mirrors kubectl: explicit flag beats the
// context's own namespace, which beats "default". Anyone with muscle memory for
// kubectl should not have to think about this.
func Connect(opts Options) (*Client, error) {
	loading := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.Kubeconfig != "" {
		loading.ExplicitPath = opts.Kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if opts.Context != "" {
		overrides.CurrentContext = opts.Context
	}
	if opts.Namespace != "" {
		overrides.Context.Namespace = opts.Namespace
	}

	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading, overrides)

	raw, err := cc.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("reading kubeconfig: %w", err)
	}
	ctxName := opts.Context
	if ctxName == "" {
		ctxName = raw.CurrentContext
	}
	if ctxName == "" {
		return nil, fmt.Errorf("no kubeconfig context selected and no current-context set; pass --context")
	}
	kubeCtx, ok := raw.Contexts[ctxName]
	if !ok {
		return nil, fmt.Errorf("context %q not found in kubeconfig (available: %s)", ctxName, joinContexts(raw))
	}

	ns, _, err := cc.Namespace()
	if err != nil {
		return nil, fmt.Errorf("resolving namespace: %w", err)
	}
	if opts.AllNamespaces {
		ns = metav1.NamespaceAll
	}

	restCfg, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("building client config for context %q: %w", ctxName, err)
	}
	if opts.Timeout > 0 {
		restCfg.Timeout = opts.Timeout
	}
	if opts.QPS > 0 {
		restCfg.QPS = opts.QPS
		restCfg.Burst = opts.Burst
	}
	if opts.Impersonate != "" {
		restCfg.Impersonate = rest.ImpersonationConfig{UserName: opts.Impersonate}
	}
	// A descriptive user agent makes this tool identifiable in apiserver audit
	// logs, which matters when someone is hunting a source of read load.
	restCfg.UserAgent = buildinfo.UserAgent()

	kc, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}
	mc, err := metricsv.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("creating metrics client: %w", err)
	}

	return &Client{
		Kube:        kc,
		Metrics:     mc,
		Discovery:   kc.Discovery(),
		Config:      restCfg,
		ContextName: ctxName,
		Namespace:   ns,
		ClusterName: kubeCtx.Cluster,
	}, nil
}

func joinContexts(raw clientcmdapi.Config) string {
	names := make([]string, 0, len(raw.Contexts))
	for n := range raw.Contexts {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "none"
	}
	out := names[0]
	for _, n := range names[1:] {
		out += ", " + n
	}
	return out
}

// ContextInfo describes one kubeconfig context for a picker UI.
type ContextInfo struct {
	Name      string
	Cluster   string
	Namespace string
	Current   bool
}

// ListContexts enumerates the kubeconfig's contexts without connecting to any
// cluster, so the TUI can offer a switcher that works offline.
func ListContexts(kubeconfig string) ([]ContextInfo, error) {
	loading := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loading.ExplicitPath = kubeconfig
	}
	raw, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading, &clientcmd.ConfigOverrides{}).RawConfig()
	if err != nil {
		return nil, fmt.Errorf("reading kubeconfig: %w", err)
	}
	out := make([]ContextInfo, 0, len(raw.Contexts))
	for name, c := range raw.Contexts {
		out = append(out, ContextInfo{
			Name:      name,
			Cluster:   c.Cluster,
			Namespace: c.Namespace,
			Current:   name == raw.CurrentContext,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListNamespaces returns namespace names, for the TUI's namespace picker.
//
// Plenty of users can read pods in their own namespace but cannot list
// namespaces cluster-wide, so a Forbidden here is reported as an empty list
// rather than a hard failure.
func (c *Client) ListNamespaces(ctx context.Context) ([]string, error) {
	list, err := c.Kube.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		if errors.IsForbidden(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(list.Items))
	for _, n := range list.Items {
		out = append(out, n.Name)
	}
	sort.Strings(out)
	return out, nil
}

// MetricsAvailable reports whether metrics.k8s.io is served by this cluster.
//
// This is a discovery lookup rather than a real query because it distinguishes
// the two failure modes that matter: the API group being entirely absent (no
// metrics-server installed, which we can offer to fix) versus present but
// erroring (installed and unhealthy, which needs a different conversation).
func (c *Client) MetricsAvailable(ctx context.Context) (bool, error) {
	groups, err := c.Discovery.ServerGroups()
	if err != nil {
		return false, fmt.Errorf("querying server API groups: %w", err)
	}
	for _, g := range groups.Groups {
		if g.Name != "metrics.k8s.io" {
			continue
		}
		for _, v := range g.Versions {
			if v.GroupVersion == MetricsGroupVersion {
				return true, nil
			}
		}
	}
	return false, nil
}

// Describe returns a short "context/namespace" label for status lines.
func (c *Client) Describe() string {
	ns := c.Namespace
	if ns == "" {
		ns = "all namespaces"
	}
	return fmt.Sprintf("%s · %s", c.ContextName, ns)
}
