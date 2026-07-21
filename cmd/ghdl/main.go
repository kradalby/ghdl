// Command ghdl scrapes cumulative download/pull counts for GitHub release
// assets, Docker Hub repositories, and GHCR container packages, records them as
// a time series in SQLite, and serves that history as JSON for Grafana. It runs
// as its own tsnet node so it is reachable from anywhere on the tailnet
// regardless of which host runs it.
//
// Configuration is via flags or the matching GHDL_-prefixed environment
// variables (e.g. GHDL_HOSTNAME). Secrets are read only from the environment:
// GHDL_GITHUB_TOKEN (GitHub API) and TS_AUTHKEY (unattended tailnet enrolment).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"
	"golang.org/x/sync/errgroup"

	"github.com/kradalby/kra/web"

	"github.com/kradalby/ghdl/api"
	"github.com/kradalby/ghdl/collect"
	"github.com/kradalby/ghdl/db"
)

func main() { os.Exit(run()) }

func run() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	lvl := new(slog.LevelVar)
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
	slog.SetDefault(log)

	cmd := newCommand(log, lvl)

	err := cmd.ParseAndRun(ctx, os.Args[1:], ff.WithEnvVarPrefix("GHDL"))
	if err != nil {
		if errors.Is(err, ff.ErrHelp) {
			fmt.Fprintln(os.Stderr, ffhelp.Command(cmd))
			return 0
		}
		log.Error("ghdl failed", "err", err)
		return 1
	}
	return 0
}

type config struct {
	hostname       *string
	stateDir       *string
	dbPath         *string
	localAddr      *string
	interval       *time.Duration
	githubRepos    *string
	dockerhubRepos *string
	ghcrRepos      *string
	logLevel       *string
	dev            *bool
}

func newCommand(log *slog.Logger, lvl *slog.LevelVar) *ff.Command {
	fs := ff.NewFlagSet("ghdl")
	cfg := &config{
		hostname:       fs.StringLong("hostname", "ghdl", "tailnet hostname to serve as"),
		stateDir:       fs.StringLong("state-dir", "", "directory for tsnet state and the database (required)"),
		dbPath:         fs.StringLong("db-path", "", "SQLite database path (default <state-dir>/ghdl.db)"),
		localAddr:      fs.StringLong("local-addr", "127.0.0.1:9091", "loopback address for the local HTTP listener"),
		interval:       fs.DurationLong("interval", 24*time.Hour, "how often to scrape the sources"),
		githubRepos:    fs.StringLong("github-repos", "juanfont/headscale", "comma-separated owner/repo list for GitHub release downloads"),
		dockerhubRepos: fs.StringLong("dockerhub-repos", "headscale/headscale", "comma-separated namespace/repo list for Docker Hub pulls"),
		ghcrRepos:      fs.StringLong("ghcr-repos", "juanfont/headscale", "comma-separated owner/package list for GHCR pulls"),
		logLevel:       fs.StringLong("log-level", "info", "log level: debug, info, warn or error"),
		dev:            fs.BoolLong("dev", "serve plain local HTTP only, without joining the tailnet; for testing"),
	}
	return &ff.Command{
		Name:  "ghdl",
		Usage: "ghdl [FLAGS]",
		Flags: fs,
		Exec: func(ctx context.Context, _ []string) error {
			if err := lvl.UnmarshalText([]byte(*cfg.logLevel)); err != nil {
				return fmt.Errorf("invalid --log-level %q: %w", *cfg.logLevel, err)
			}
			return serve(ctx, log, cfg)
		},
	}
}

var errStateDirRequired = errors.New("--state-dir (or GHDL_STATE_DIR) is required")

func serve(ctx context.Context, log *slog.Logger, cfg *config) error {
	if *cfg.stateDir == "" {
		return errStateDirRequired
	}

	dbPath := *cfg.dbPath
	if dbPath == "" {
		dbPath = filepath.Join(*cfg.stateDir, "ghdl.db")
	}

	store, err := db.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	gh := collect.NewGitHub(os.Getenv("GHDL_GITHUB_TOKEN"), splitList(*cfg.githubRepos))
	collectors := []collect.Collector{
		gh,
		collect.NewDockerHub(splitList(*cfg.dockerhubRepos)),
		collect.NewGHCR(splitList(*cfg.ghcrRepos)),
	}

	srv, err := web.NewServer(web.ServerConfig{
		Hostname:        *cfg.hostname,
		LocalAddr:       *cfg.localAddr,
		AuthKey:         os.Getenv("TS_AUTHKEY"),
		EnableTailscale: !*cfg.dev,
	}, web.WithLogger(log),
		// tsnet defaults its state to $HOME/.config, which is /var/empty under
		// the hardened unit; keep it in the writable state directory instead.
		web.WithTailscaleStateDir(filepath.Join(*cfg.stateDir, "tsnet")))
	if err != nil {
		return fmt.Errorf("build server: %w", err)
	}

	apiSrv := api.New(store)
	srv.HandleFunc("/api/series", apiSrv.Series)
	srv.HandleFunc("/api/timeseries", apiSrv.TimeSeries)

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return srv.ListenAndServe(ctx) })
	g.Go(func() error { return collectLoop(ctx, log, store, collectors, gh, *cfg.interval) })

	return g.Wait()
}

// collectLoop scrapes immediately, then on every interval tick, until the
// context is cancelled (a clean SIGTERM shutdown).
func collectLoop(ctx context.Context, log *slog.Logger, store *db.DB, collectors []collect.Collector, gh *collect.GitHub, interval time.Duration) error {
	runAll(ctx, log, store, collectors, gh)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			runAll(ctx, log, store, collectors, gh)
		}
	}
}

// runAll runs every collector once, persisting with store-on-change semantics
// and updating the health metrics. One source failing does not stop the others.
func runAll(ctx context.Context, log *slog.Logger, store *db.DB, collectors []collect.Collector, gh *collect.GitHub) {
	ts := time.Now().Unix()

	for _, c := range collectors {
		obs, err := backoff.Retry(ctx, func() ([]db.Observation, error) {
			return c.Collect(ctx)
		}, backoff.WithBackOff(backoff.NewExponentialBackOff()), backoff.WithMaxTries(4))
		if err != nil {
			scrapeErrors.WithLabelValues(c.Source()).Inc()
			log.Error("collect failed", "source", c.Source(), "err", err)
			continue
		}

		n, err := store.Save(ctx, ts, obs)
		if err != nil {
			scrapeErrors.WithLabelValues(c.Source()).Inc()
			log.Error("save failed", "source", c.Source(), "err", err)
			continue
		}

		lastSuccess.WithLabelValues(c.Source()).Set(float64(ts))
		log.Info("collected", "source", c.Source(), "observations", len(obs), "written", n)
	}

	rateRemaining.Set(float64(gh.RateRemaining()))
	if cnt, err := store.CountSeries(ctx); err == nil {
		seriesTotal.Set(float64(cnt))
	}
}

// splitList splits a comma-separated flag value, trimming spaces and dropping
// empties.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
