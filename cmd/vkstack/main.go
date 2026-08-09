// Command vkstack answers compatibility questions across vCenter, ESX, vSphere
// Supervisor, VKS and VKr, using a local cache of the Broadcom Product
// Interoperability Matrix.
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/warroyo/vkstack/internal/cli"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.NewRoot(version).ExecuteContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		// Failures are reported the same way answers are: as data when a program is
		// reading, as a line of prose when a person is. The exit code is the part that
		// distinguishes a bad question from a well-formed one whose answer is "no".
		os.Exit(cli.ReportError(err, cli.Mode()))
	}
}
