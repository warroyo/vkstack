package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/warroyo/vkstack/internal/api"
	"github.com/warroyo/vkstack/internal/graph"
	"github.com/warroyo/vkstack/internal/store"
	"github.com/warroyo/vkstack/internal/web"
)

func newServeCmd() *cobra.Command {
	var (
		port            int
		bind            string
		open            bool
		readOnly        bool
		refreshInterval time.Duration
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the web UI, locally or hosted",
		Long: `Serve the web UI.

By default it binds 127.0.0.1 and refreshes only when asked, which is what you want on a
laptop: everything is embedded or cached locally, so it works offline and nothing leaves
the machine.

For a shared instance, run it read-only with a refresh interval:

  vkstack serve --bind 0.0.0.0 --read-only --refresh-interval 6h

--read-only rejects client-triggered refreshes, so visitors cannot make the server call
upstream. The scheduled refresh still writes to the cache — it is the server's job, not
the client's.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// One handle for the server's lifetime: opening (and migrating) the cache
			// per request is wasteful for anything hosted.
			db, err := openCache()
			if err != nil {
				return err
			}
			defer db.Close()

			load := newCachedLoader(db)

			refreshOnce := func(progress func(step, total int, message string)) error {
				if err := api.Refresh(cmd.Context(), api.New(g.apiBase, g.authKey), db, progress); err != nil {
					return err
				}
				load.invalidate()
				return nil
			}

			clientRefresh := refreshOnce
			if readOnly {
				clientRefresh = func(func(int, int, string)) error {
					return errors.New("this instance is read-only; refreshes are on a schedule")
				}
			}

			ledger := &web.Ledger{}
			handler, err := web.NewServer(web.Config{
				Load:            load.get,
				Refresh:         clientRefresh,
				ReadOnly:        readOnly,
				RefreshInterval: refreshInterval,
				Ledger:          ledger,
				Version:         cmd.Root().Version,
			})
			if err != nil {
				return err
			}

			addr := net.JoinHostPort(bind, fmt.Sprint(port))
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("listening on %s: %w", addr, err)
			}

			stderr := cmd.ErrOrStderr()
			cold := isCold(db)
			if cold && refreshInterval == 0 {
				fmt.Fprintln(stderr,
					"warning: no cached data yet — run `vkstack refresh` for anything beyond the model view")
			}

			if refreshInterval > 0 {
				startScheduledRefresh(cmd.Context(), stderr, refreshOnce, refreshInterval, cold, ledger)
			}
			if bind != "127.0.0.1" && bind != "localhost" && !readOnly {
				fmt.Fprintf(stderr,
					"warning: serving on %s without --read-only — anyone who can reach this can trigger upstream refreshes\n",
					bind)
			}

			srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
			go func() {
				<-cmd.Context().Done()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdownCtx)
			}()

			// A wildcard bind reports itself as "[::]", which is not a link anyone can
			// click. Show a usable local URL and note the wildcard separately.
			url := "http://" + ln.Addr().String()
			if bind == "0.0.0.0" || bind == "::" || bind == "" {
				_, boundPort, _ := net.SplitHostPort(ln.Addr().String())
				url = fmt.Sprintf("http://localhost:%s (listening on all interfaces)", boundPort)
			}
			mode := "on demand"
			if readOnly {
				mode = "read-only"
			}
			if refreshInterval > 0 {
				mode += fmt.Sprintf(", refreshing every %s", refreshInterval)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "vkstack is serving at %s (%s; ctrl-c to stop)\n", url, mode)
			if open {
				_, boundPort, _ := net.SplitHostPort(ln.Addr().String())
				openBrowser("http://localhost:" + boundPort)
			}
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", 8080, "port to listen on (0 picks a free one)")
	cmd.Flags().StringVar(&bind, "bind", "127.0.0.1", "address to bind (use 0.0.0.0 to host)")
	cmd.Flags().BoolVar(&open, "open", false, "open the UI in a browser")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "reject client-triggered refreshes")
	cmd.Flags().DurationVar(&refreshInterval, "refresh-interval", 0,
		"refresh from upstream on this interval, e.g. 6h (0 disables)")
	return cmd
}

// startScheduledRefresh runs refreshes in the background for a hosted instance.
//
// A failed refresh is logged and retried on the next tick rather than taking the server
// down: serving slightly stale data beats serving nothing because upstream had a blip.
// Every attempt is also recorded in the ledger, so the failure is visible to whoever is
// looking at the UI rather than only to whoever can read this log.
func startScheduledRefresh(
	ctx context.Context,
	logTo interface{ Write([]byte) (int, error) },
	refresh func(func(int, int, string)) error,
	interval time.Duration,
	cold bool,
	ledger *web.Ledger,
) {
	run := func(reason string) {
		start := time.Now()
		fmt.Fprintf(logTo, "refresh (%s) starting\n", reason)
		err := refresh(func(int, int, string) {})
		ledger.Record(start, time.Since(start), err)
		if err != nil {
			fmt.Fprintf(logTo, "refresh failed: %v (will retry in %s)\n", err, interval)
			return
		}
		fmt.Fprintf(logTo, "refresh done in %s\n", time.Since(start).Round(time.Second))
	}

	go func() {
		// A cold cache is refreshed immediately; a warm one waits for the first tick
		// so restarts do not hammer upstream.
		if cold {
			run("cold cache")
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run("scheduled")
			}
		}
	}()
}

func isCold(db *store.DB) bool {
	at, err := db.FetchedAt()
	return err != nil || at.IsZero()
}

// cachedLoader keeps the parsed graph in memory between requests.
//
// Loading reads the whole cache and reparses every version, which is fine once but wasteful
// per request on a hosted instance. The cached copy is dropped when the cache's fetched_at
// changes, so a refresh — from the schedule, or from a separate `vkstack refresh` against
// the same cache — is picked up without a restart.
type cachedLoader struct {
	db        *store.DB
	mu        sync.Mutex
	graph     *graph.Graph
	fetchedAt int64
}

func newCachedLoader(db *store.DB) *cachedLoader { return &cachedLoader{db: db} }

func (c *cachedLoader) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.graph = nil
}

func (c *cachedLoader) get() (*graph.Graph, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Reading just the timestamp is far cheaper than a full load, and it means a
	// refresh from a separate `vkstack refresh` against the same cache is picked up too.
	at, err := c.db.FetchedAt()
	if err != nil {
		return nil, err
	}
	if at.IsZero() {
		return nil, errors.New("no cached data yet — run `vkstack refresh`, or start with --refresh-interval")
	}
	if c.graph != nil && at.UnixMilli() == c.fetchedAt {
		return c.graph, nil
	}

	loaded, err := loadGraphFrom(c.db)
	if err != nil {
		return nil, err
	}
	c.graph, c.fetchedAt = loaded, loaded.FetchedAt
	return loaded, nil
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
