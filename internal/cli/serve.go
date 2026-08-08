package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/warroyo/interop-visualizer/internal/api"
	"github.com/warroyo/interop-visualizer/internal/graph"
	"github.com/warroyo/interop-visualizer/internal/web"
)

func newServeCmd() *cobra.Command {
	var port int
	var open bool

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the local web UI",
		Long: `Serve the local web UI on 127.0.0.1.

Everything — assets, diagrams, data — is embedded or cached locally, so the UI works
with no network connection and nothing leaves the machine.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Reload from disk per request rather than caching a graph, so a refresh
			// (from the CLI or the UI) is picked up without restarting the server.
			load := func() (*graph.Graph, error) { return loadGraph() }

			refresh := func(progress func(step, total int, message string)) error {
				db, err := openCache()
				if err != nil {
					return err
				}
				defer db.Close()
				return api.Refresh(cmd.Context(), api.New(g.apiBase, g.authKey), db, progress)
			}

			handler, err := web.NewServer(load, refresh)
			if err != nil {
				return err
			}

			addr := fmt.Sprintf("127.0.0.1:%d", port)
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("listening on %s: %w", addr, err)
			}
			url := "http://" + ln.Addr().String()

			// Warn rather than fail on a cold cache: the whiteboard view needs no data.
			if db, dbErr := openCache(); dbErr == nil {
				if at, _ := db.FetchedAt(); at.IsZero() {
					fmt.Fprintln(cmd.ErrOrStderr(),
						"warning: no cached data yet — run `interop refresh` for anything beyond the model view")
				}
				db.Close()
			}

			srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
			go func() {
				<-cmd.Context().Done()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdownCtx)
			}()

			fmt.Fprintf(cmd.OutOrStdout(), "interop is serving at %s (ctrl-c to stop)\n", url)
			if open {
				openBrowser(url)
			}
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", 8080, "port to listen on (0 picks a free one)")
	cmd.Flags().BoolVar(&open, "open", false, "open the UI in a browser")
	return cmd
}

// openBrowser is best-effort: failing to launch a browser must not fail the command.
func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	_ = exec.Command(cmd, append(args, url)...).Start()
}
