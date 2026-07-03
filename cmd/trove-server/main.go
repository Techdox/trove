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
	"syscall"
	"text/tabwriter"
	"time"

	"trove/internal/server"
	"trove/internal/staleness"
	"trove/internal/store"
)

const version = "0.1.0"

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
  healthcheck               probe /healthz on the local server (exit 0/1)

Environment:
  TROVE_ADDR              listen address (default ":8080")
  TROVE_DB                sqlite path    (default "trove.db")
  TROVE_BOOTSTRAP_AGENT   dev-only: seed an agent with this name at startup
  TROVE_BOOTSTRAP_TOKEN   dev-only: token for the bootstrapped agent
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv.ConfigureFreshness(server.LoadFreshnessConfigFromEnv())

	go srv.RunStalenessLoop(ctx)
	go srv.RunFreshnessLoop(ctx)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
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
