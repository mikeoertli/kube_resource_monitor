package notify

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Notifier delivers alerts.
type Notifier interface {
	Notify(ctx context.Context, a Alert) error
	// Name identifies the transport in status output.
	Name() string
}

// Desktop posts native desktop notifications.
//
// Delivery mechanism is chosen once at construction rather than per alert,
// because probing for terminal-notifier on every notification would add a
// process spawn to something already on a timer.
type Desktop struct {
	// Sound plays the notification sound (macOS only).
	Sound string

	once    sync.Once
	deliver func(ctx context.Context, title, body string) error
	kind    string
}

// NewDesktop builds a desktop notifier for the current platform.
func NewDesktop() *Desktop { return &Desktop{Sound: "Submarine"} }

// Name implements Notifier.
func (d *Desktop) Name() string {
	d.once.Do(d.pick)
	return d.kind
}

func (d *Desktop) pick() {
	switch runtime.GOOS {
	case "darwin":
		// terminal-notifier is preferred when present: unlike osascript it can
		// set the notification's own icon and title bar, and it survives being
		// called from a background process without stealing focus.
		if path, err := exec.LookPath("terminal-notifier"); err == nil {
			d.kind = "terminal-notifier"
			d.deliver = func(ctx context.Context, title, body string) error {
				args := []string{"-title", "kube-resource-monitor", "-subtitle", title, "-message", body, "-group", "krm"}
				if d.Sound != "" {
					args = append(args, "-sound", d.Sound)
				}
				return exec.CommandContext(ctx, path, args...).Run()
			}
			return
		}
		d.kind = "osascript"
		d.deliver = func(ctx context.Context, title, body string) error {
			script := fmt.Sprintf(
				`display notification %s with title "kube-resource-monitor" subtitle %s`,
				appleScriptString(body), appleScriptString(title))
			if d.Sound != "" {
				script += fmt.Sprintf(" sound name %s", appleScriptString(d.Sound))
			}
			return exec.CommandContext(ctx, "osascript", "-e", script).Run()
		}
	case "linux":
		if path, err := exec.LookPath("notify-send"); err == nil {
			d.kind = "notify-send"
			d.deliver = func(ctx context.Context, title, body string) error {
				return exec.CommandContext(ctx, path, "-a", "kube-resource-monitor", title, body).Run()
			}
			return
		}
		d.kind = "unavailable"
	default:
		d.kind = "unavailable"
	}
}

// Notify implements Notifier.
func (d *Desktop) Notify(ctx context.Context, a Alert) error {
	d.once.Do(d.pick)
	if d.deliver == nil {
		return fmt.Errorf("no desktop notification mechanism available on this system (install terminal-notifier on macOS or notify-send on Linux)")
	}
	// A hung notification helper must not stall the refresh loop.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return d.deliver(ctx, a.Title(), a.Body())
}

// appleScriptString quotes a Go string for embedding in AppleScript.
//
// Pod names come from the cluster and can contain quotes or backslashes in
// pathological cases; interpolating them unescaped into a script passed to
// osascript would be both a correctness bug and a command injection risk.
func appleScriptString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString("\\n")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Writer prints alerts to a stream. Used for --notify-stdout, for logging
// alongside desktop delivery, and as the fallback when no desktop mechanism
// exists (a headless server should still be able to run notify mode).
type Writer struct{ W io.Writer }

// Name implements Notifier.
func (w Writer) Name() string { return "stdout" }

// Notify implements Notifier.
func (w Writer) Notify(_ context.Context, a Alert) error {
	state := "FIRING  "
	if !a.Firing {
		state = "RESOLVED"
	}
	_, err := fmt.Fprintf(w.W, "%s %s %s\n", time.Now().Format(time.RFC3339), state, a.Body())
	return err
}

// Multi fans an alert out to several notifiers, delivering to all of them even
// if one fails, so a broken desktop helper does not suppress the stdout log.
type Multi []Notifier

// Name implements Notifier.
func (m Multi) Name() string {
	names := make([]string, 0, len(m))
	for _, n := range m {
		names = append(names, n.Name())
	}
	return strings.Join(names, "+")
}

// Notify implements Notifier.
func (m Multi) Notify(ctx context.Context, a Alert) error {
	var errs []string
	for _, n := range m {
		if err := n.Notify(ctx, a); err != nil {
			errs = append(errs, n.Name()+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}
