// Command vkstack answers compatibility questions across vCenter, ESX, vSphere
// Supervisor, VKS and VKr, using a local cache of the Broadcom Product
// Interoperability Matrix.
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/warroyo/vkstack/internal/cli"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

// resolveVersion reports which build this is, however it was installed.
//
// Released archives carry the version through ldflags, but `go install
// github.com/warroyo/vkstack/cmd/vkstack@v0.1.0` does not — it would report "dev" for a
// perfectly identifiable build. That matters beyond cosmetics: every JSON envelope stamps
// this into its `tool` field, and an agent is told to carry that value alongside any
// answer it passes on. So when ldflags are absent, ask the build itself.
func resolveVersion() string {
	if version != "dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return version
	}
	return info.Main.Version
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.NewRoot(resolveVersion()).ExecuteContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		// Failures are reported the same way answers are: as data when a program is
		// reading, as a line of prose when a person is. The exit code is the part that
		// distinguishes a bad question from a well-formed one whose answer is "no".
		os.Exit(cli.ReportError(err, cli.Mode()))
	}
}
