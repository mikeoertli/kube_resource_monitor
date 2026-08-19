package metricsserver

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func deployment(ns string, replicas, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "metrics-server"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: ready},
	}
}

func TestDetectNotInstalled(t *testing.T) {
	s := Detect(context.Background(), fake.NewSimpleClientset(), false)
	if s.DeploymentFound || s.Healthy() {
		t.Fatalf("unexpected status: %+v", s)
	}
	if !strings.Contains(s.Hint, "not appear to be installed") {
		t.Errorf("hint = %q", s.Hint)
	}
}

// A deployment that exists but is not ready is a different problem from a
// missing install, and the guidance must not tell the user to reinstall.
func TestDetectDeployedButNotReady(t *testing.T) {
	kc := fake.NewSimpleClientset(deployment("kube-system", 1, 0))
	s := Detect(context.Background(), kc, false)

	if !s.DeploymentFound {
		t.Fatal("deployment should have been found")
	}
	if !strings.Contains(s.Hint, "0/1 replicas ready") {
		t.Errorf("hint should report readiness: %q", s.Hint)
	}
	if !strings.Contains(s.Hint, "kubelet serving certificate") {
		t.Errorf("hint should name the usual cause: %q", s.Hint)
	}

	instructions := InstallInstructions("kind-dev", s)
	if strings.Contains(instructions, "kubectl apply -f") {
		t.Error("must not advise reinstalling over an existing deployment")
	}
	if !strings.Contains(instructions, "logs deploy/metrics-server") {
		t.Errorf("should advise inspecting logs:\n%s", instructions)
	}
}

func TestDetectHealthy(t *testing.T) {
	kc := fake.NewSimpleClientset(deployment("kube-system", 1, 1))
	s := Detect(context.Background(), kc, true)
	if !s.Healthy() {
		t.Error("API available means healthy")
	}
}

func TestDetectFindsNonStandardNamespace(t *testing.T) {
	kc := fake.NewSimpleClientset(deployment("monitoring", 2, 2))
	s := Detect(context.Background(), kc, false)
	if s.Namespace != "monitoring" {
		t.Errorf("namespace = %q, want monitoring", s.Namespace)
	}
}

func TestInstallCommandIncludesContext(t *testing.T) {
	got := InstallCommand("kind-dev", false)
	if !strings.Contains(got, "--context kind-dev") {
		t.Errorf("got %q", got)
	}
	if !strings.Contains(got, ManifestURL) {
		t.Errorf("missing manifest URL: %q", got)
	}

	ha := InstallCommand("", true)
	if strings.Contains(ha, "--context") {
		t.Errorf("no context should mean no flag: %q", ha)
	}
	if !strings.Contains(ha, "high-availability.yaml") {
		t.Errorf("HA variant should use the HA manifest: %q", ha)
	}
}

// Context names can contain characters a shell would interpret; the printed
// command must be safe to paste.
func TestInstallCommandQuotesAwkwardContexts(t *testing.T) {
	got := InstallCommand("arn:aws:eks:us-east-1:1234:cluster/my cluster", false)
	if !strings.Contains(got, "'arn:aws:eks:us-east-1:1234:cluster/my cluster'") {
		t.Errorf("context with a space must be quoted: %q", got)
	}
}

func TestInstallInstructionsCoverTheCommonFailure(t *testing.T) {
	s := Detect(context.Background(), fake.NewSimpleClientset(), false)
	out := InstallInstructions("kind-dev", s)

	for _, want := range []string{"--kubelet-insecure-tls", "high-availability", "scrape interval"} {
		if !strings.Contains(out, want) {
			t.Errorf("instructions should mention %q:\n%s", want, out)
		}
	}
}
