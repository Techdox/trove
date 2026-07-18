// Command trove-server is the Trove server: it ingests agent reports, serves
// the read-only dashboard + APIs, and provides an agent-token CLI.
//
// Usage:
//
//	trove-server [serve]                 run the server (default)
//	trove-server agent create <name>     mint a bearer token for a new agent
//	trove-server agent list              list agents and last-seen
//	trove-server agent delete <name>     remove an agent and its data
//
// Config (serve): TROVE_ADDR (default :8080), TROVE_DB (default trove.db).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/techdox/trove/internal/alert"
	"github.com/techdox/trove/internal/server"
	"github.com/techdox/trove/internal/staleness"
	"github.com/techdox/trove/internal/store"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

const (
	readHeaderTimeout        = 10 * time.Second
	readTimeout              = 30 * time.Second
	writeTimeout             = 30 * time.Second
	idleTimeout              = 120 * time.Second
	supervisorInitialBackoff = time.Second
	supervisorMaxBackoff     = time.Minute
	supervisorStablePeriod   = time.Minute
)

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}

	var err error
	switch cmd {
	case "serve":
		err = runServe()
	case "agent":
		err = runAgent(args[1:])
	case "alert":
		err = runAlertCmd(args[1:])
	case "backup":
		err = runBackup(args[1:])
	case "healthcheck":
		err = runHealthcheck()
	case "version", "-v", "--version":
		fmt.Println("trove-server", version)
	case "help", "-h", "--help":
		usage(os.Stdout)
	default:
		usage(os.Stderr)
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `trove-server — read-only service catalog + health monitor

Commands:
  serve                     run the server (default)
  agent create <name>       mint a bearer token for a new agent
  agent list                list agents with last-seen status
  agent delete <name>       remove an agent and all its data
  alert test                send a test notification through configured channels
  backup [path]             write a consistent SQLite backup (default timestamped file)
  healthcheck               probe /healthz on the local server (exit 0/1)

Environment:
  TROVE_ADDR              listen address (default ":8080")
  TROVE_DB                sqlite path    (default "trove.db")
  TROVE_BOOTSTRAP_AGENT   dev-only: seed an agent with this name at startup
  TROVE_BOOTSTRAP_TOKEN   dev-only: token for the bootstrapped agent
  TROVE_OIDC_ISSUER       OIDC discovery URL (set with all required OIDC settings)
  TROVE_OIDC_CLIENT_ID    OAuth2 client ID
  TROVE_OIDC_CLIENT_SECRET  OAuth2 client secret
  TROVE_OIDC_REDIRECT_URL   OAuth2 callback URL
  TROVE_API_TOKEN         Bearer token for programmatic API access (with OIDC)
  TROVE_HEALTH_DETAILS_ENABLED  true to retain/display redacted platform health messages (default false)
  TROVE_HOST_RETENTION    Retain silent hosts before pruning (default "720h")
`)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func openStore() (*store.Store, error) {
	dbPath := envOr("TROVE_DB", "trove.db")
	return store.Open(dbPath)
}

type workerState uint8

const (
	workerStarting workerState = iota
	workerRunning
	workerBackoff
	workerStopped
)

// workerMonitor is shared by the process supervisors and the server health
// surfaces. Only enabled workers are registered.
type workerMonitor struct {
	mu     sync.RWMutex
	states map[string]workerState
}

func newWorkerMonitor(names ...string) *workerMonitor {
	states := make(map[string]workerState, len(names))
	for _, name := range names {
		states[name] = workerStarting
	}
	return &workerMonitor{states: states}
}

func (m *workerMonitor) set(name string, state workerState) {
	m.mu.Lock()
	m.states[name] = state
	m.mu.Unlock()
}

func (m *workerMonitor) health() error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var unavailable []string
	for name, state := range m.states {
		if state == workerStarting || state == workerBackoff {
			unavailable = append(unavailable, name)
		}
	}
	if len(unavailable) == 0 {
		return nil
	}
	sort.Strings(unavailable)
	return fmt.Errorf("background workers unavailable: %s", strings.Join(unavailable, ", "))
}

// runSupervised restarts an unexpectedly stopped or panicking background loop
// with bounded exponential backoff. During backoff the worker monitor makes the
// failure visible to /healthz and /metrics, allowing an orchestrator or operator
// to distinguish a fully healthy server from an HTTP-only shell.
func runSupervised(ctx context.Context, log *slog.Logger, name string, monitor *workerMonitor, fn func()) {
	runSupervisedWithBackoff(ctx, log, name, monitor, supervisorInitialBackoff, supervisorMaxBackoff, fn)
}

func runSupervisedWithBackoff(
	ctx context.Context,
	log *slog.Logger,
	name string,
	monitor *workerMonitor,
	initialBackoff time.Duration,
	maxBackoff time.Duration,
	fn func(),
) {
	backoff := initialBackoff
	for {
		monitor.set(name, workerRunning)
		startedAt := time.Now()
		panicked, panicValue := runWorker(fn)
		if ctx.Err() != nil {
			monitor.set(name, workerStopped)
			return
		}

		if time.Since(startedAt) >= supervisorStablePeriod {
			backoff = initialBackoff
		}
		monitor.set(name, workerBackoff)
		if panicked {
			log.Error("background loop panicked, restarting after backoff",
				"loop", name, "panic", panicValue, "backoff", backoff)
		} else {
			log.Error("background loop stopped unexpectedly, restarting after backoff",
				"loop", name, "backoff", backoff)
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			monitor.set(name, workerStopped)
			return
		case <-timer.C:
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

func runWorker(fn func()) (panicked bool, panicValue any) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			panicValue = r
		}
	}()
	fn()
	return false, nil
}

func runServe() error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	st, err := openStore()
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	if err := bootstrapAgent(context.Background(), st, logger); err != nil {
		return err
	}

	addr := envOr("TROVE_ADDR", ":8080")
	srv := server.New(st, logger)
	srv.ConfigureHealthDetails(server.LoadHealthDetailsEnabledFromEnv())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	freshnessCfg := server.LoadFreshnessConfigFromEnv()
	srv.ConfigureFreshness(freshnessCfg)
	srv.ConfigureRetention(server.LoadRetentionConfigFromEnv())
	alertCfg := alert.LoadConfigFromEnv()
	digestCfg := alert.LoadDigestConfigFromEnv(logger)

	// Configure OIDC if all required env vars are set. Partial configuration
	// fails startup; when all auth settings are absent, behavior is unchanged
	// (no auth — bind to a trusted network).
	if err := srv.ConfigureOIDC(server.LoadOIDCConfigFromEnv()); err != nil {
		return fmt.Errorf("configure oidc: %w", err)
	}

	workerNames := []string{"staleness", "maintenance"}
	if freshnessCfg.Enabled {
		workerNames = append(workerNames, "freshness")
	}
	if alertCfg.Enabled() {
		workerNames = append(workerNames, "alert")
	}
	if digestCfg.Enabled() {
		workerNames = append(workerNames, "digest")
	}
	workers := newWorkerMonitor(workerNames...)
	srv.ConfigureBackgroundHealth(workers.health)

	go runSupervised(ctx, logger, "staleness", workers, func() { srv.RunStalenessLoop(ctx) })
	go runSupervised(ctx, logger, "maintenance", workers, func() { srv.RunMaintenanceLoop(ctx) })
	if freshnessCfg.Enabled {
		go runSupervised(ctx, logger, "freshness", workers, func() { srv.RunFreshnessLoop(ctx) })
	} else {
		logger.Info("image freshness checking disabled")
	}
	if alertCfg.Enabled() {
		go runSupervised(ctx, logger, "alert", workers, func() { alert.NewEngine(st, logger, alertCfg).Run(ctx) })
	} else {
		logger.Info("alerting disabled (no channel configured)")
	}
	if digestCfg.Enabled() {
		go runSupervised(ctx, logger, "digest", workers, func() { alert.NewDigester(st, logger, digestCfg).Run(ctx) })
	} else {
		logger.Info("email digest disabled")
	}

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("trove-server listening", "addr", addr, "db", envOr("TROVE_DB", "trove.db"), "version", version)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

// bootstrapAgent seeds a dev agent from TROVE_BOOTSTRAP_AGENT /
// TROVE_BOOTSTRAP_TOKEN when both are set. It is a convenience for the
// docker-compose dev stack so `docker compose up` needs no manual token step;
// production should leave these unset and mint tokens with `agent create`.
func bootstrapAgent(ctx context.Context, st *store.Store, logger *slog.Logger) error {
	name := os.Getenv("TROVE_BOOTSTRAP_AGENT")
	token := os.Getenv("TROVE_BOOTSTRAP_TOKEN")
	if name == "" && token == "" {
		return nil
	}
	if name == "" || token == "" {
		return errors.New("TROVE_BOOTSTRAP_AGENT and TROVE_BOOTSTRAP_TOKEN must both be set")
	}
	created, err := st.EnsureAgentWithToken(ctx, name, token)
	if err != nil {
		return fmt.Errorf("bootstrap agent: %w", err)
	}
	if created {
		logger.Warn("bootstrapped dev agent from env (do not use in production)", "agent", name)
	}
	return nil
}

// runBackup writes a consistent SQLite backup using VACUUM INTO. It refuses to
// overwrite an existing file; backups are rollback points, not disposable temp
// files.
func runBackup(args []string) error {
	if len(args) > 1 {
		return errors.New("usage: trove-server backup [path]")
	}
	dst := ""
	if len(args) == 1 {
		dst = args[0]
	} else {
		dst = "trove-backup-" + time.Now().UTC().Format("20060102-150405") + ".db"
	}
	if dst == "" {
		return errors.New("backup path is required")
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("backup path %q already exists", dst)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat backup path: %w", err)
	}
	if dir := filepath.Dir(dst); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create backup directory: %w", err)
		}
	}

	st, err := openStore()
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := st.Backup(ctx, dst); err != nil {
		return err
	}
	fmt.Printf("backup written to %s\n", dst)
	return nil
}

// runAlertCmd handles `trove-server alert test`: it pushes a test
// notification through every configured instant channel and, if SMTP is set
// up, sends a sample digest. This is how operators verify their alerting env
// vars before trusting them.
func runAlertCmd(args []string) error {
	if len(args) == 0 || args[0] != "test" {
		return errors.New("usage: trove-server alert test")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	st, err := openStore()
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()
	ctx := context.Background()

	cfg := alert.LoadConfigFromEnv()
	if !cfg.Enabled() {
		fmt.Println("no instant channels configured (TROVE_ALERT_WEBHOOK_URL / _DISCORD_URL / _NTFY_URL)")
	} else {
		results := alert.NewEngine(st, logger, cfg).SendTest(ctx)
		for name, rerr := range results {
			if rerr != nil {
				fmt.Printf("  %-8s FAILED: %v\n", name, rerr)
			} else {
				fmt.Printf("  %-8s ok\n", name)
			}
		}
	}

	dcfg := alert.LoadDigestConfigFromEnv(logger)
	if !dcfg.Enabled() {
		fmt.Println("email digest not configured (TROVE_SMTP_* / TROVE_DIGEST)")
		return nil
	}
	fmt.Println("sending sample digest (covering the last 24h)…")
	if err := alert.NewDigester(st, logger, dcfg).SendNow(ctx, time.Now().Add(-24*time.Hour)); err != nil {
		fmt.Printf("  digest   FAILED: %v\n", err)
		return err
	}
	fmt.Println("  digest   ok")
	return nil
}

// runHealthcheck performs a one-shot GET of /healthz against the local server
// and exits non-zero on failure. It exists so distroless containers (no shell,
// no curl) can define a container healthcheck by exec-ing this binary.
func runHealthcheck() error {
	addr := envOr("TROVE_ADDR", ":8080")
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse TROVE_ADDR %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	url := fmt.Sprintf("http://%s/healthz", net.JoinHostPort(host, port))
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %d", resp.StatusCode)
	}
	return nil
}

func runAgent(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: trove-server agent <create|list|delete> ...")
	}
	st, err := openStore()
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	ctx := context.Background()
	switch args[0] {
	case "create":
		if len(args) < 2 {
			return errors.New("usage: trove-server agent create <name>")
		}
		return agentCreate(ctx, st, args[1])
	case "list":
		return agentList(ctx, st)
	case "delete", "rm":
		if len(args) < 2 {
			return errors.New("usage: trove-server agent delete <name>")
		}
		return agentDelete(ctx, st, args[1])
	default:
		return fmt.Errorf("unknown agent subcommand %q", args[0])
	}
}

func agentCreate(ctx context.Context, st *store.Store, name string) error {
	token, agent, err := st.CreateAgent(ctx, name)
	if err != nil {
		return err
	}
	fmt.Printf(`Created agent %q (id %d).

  Token (shown once — store it now, it is not recoverable):

      %s

  Configure the agent with:

      TROVE_TOKEN=%s

`, agent.Name, agent.ID, token, token)
	return nil
}

func agentList(ctx context.Context, st *store.Store) error {
	agents, err := st.ListAgents(ctx)
	if err != nil {
		return err
	}
	if len(agents) == 0 {
		fmt.Println("no agents registered")
		return nil
	}
	now := time.Now().UTC()
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPLATFORM\tVERSION\tSTATUS\tLAST SEEN")
	for _, a := range agents {
		var lastSeen *time.Time
		lastSeenStr := "never"
		if a.LastSeenAt.Valid {
			t := time.Unix(a.LastSeenAt.Int64, 0).UTC()
			lastSeen = &t
			lastSeenStr = t.Format(time.RFC3339)
		}
		status := staleness.Evaluate(lastSeen, a.IntervalSeconds, now)
		platform := a.Platform
		if platform == "" {
			platform = "-"
		}
		ver := a.Version
		if ver == "" {
			ver = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.Name, platform, ver, status, lastSeenStr)
	}
	return tw.Flush()
}

func agentDelete(ctx context.Context, st *store.Store, name string) error {
	if err := st.DeleteAgent(ctx, name); err != nil {
		return err
	}
	fmt.Printf("deleted agent %q and all its data\n", name)
	return nil
}
