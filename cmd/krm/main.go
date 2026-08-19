// Command krm monitors Kubernetes resource usage from the terminal.
package main

import (
	"os"

	"github.com/mikeoertli/kube_resource_monitor/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
