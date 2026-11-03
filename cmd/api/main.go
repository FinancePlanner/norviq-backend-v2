// Command norviq-api is the Norviq API server.
//
//	norviq-api serve [--env production] [--port 8080]
//	norviq-api migrate [--yes] | migrate status
//	norviq-api routes
//
// The flags mirror the Vapor binary's so the Helm chart's command and the
// migrate job's `["migrate", "--yes"]` work unchanged.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/features"
	"norviq-api/internal/features/assistant"
	"norviq-api/internal/middleware"
	"norviq-api/internal/migrate"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/platform/metrics"
	"norviq-api/internal/platform/pprof"
	"norviq-api/internal/platform/realtime"
	"norviq-api/internal/server"
	"norviq-api/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
)

var gitSHA = "unknown"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "norviq-api:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	envName := fs.String("env", "", "environment (production, staging, development, testing)")
	port := fs.Int("port", 0, "listen port")
	_ = fs.String("hostname", "0.0.0.0", "accepted for Vapor compatibility; always binds all interfaces")
	yes := fs.Bool("yes", false, "accepted for Vapor compatibility; migrations never prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_ = yes

	cfg, err := config.Load[config.Base]()
	if err != nil {
		return err
	}
	if *envName != "" {
		cfg.Environment = config.Environment(*envName)
	}
	if *port != 0 {
		cfg.Port = *port
	}
	if cfg.GitSHA == "" {
		cfg.GitSHA = gitSHA
	}
	logger := newLogger(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "serve":
		return serve(ctx, cfg, logger)
	case "migrate":
		return runMigrate(ctx, cfg, logger, fs.Args())
	case "routes":
		return printRoutes(cfg, logger)
	}
	return fmt.Errorf("unknown command %q", cmd)
}

func newLogger(cfg config.Base) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

func openDB(ctx context.Context, cfg config.Base) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, cfg.Database.DSN())
	if err != nil {
		return nil, err
	}
	// Retry for up to 30 s: on the cluster a fresh pod can get "connection
	// refused" until kube-router has applied the NetworkPolicy for its IP,
	// and failing at once costs a container restart on every rollout.
	pingCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		err := pool.Ping(pingCtx)
		if err == nil {
			return pool, nil
		}
		select {
		case <-pingCtx.Done():
			pool.Close()
			return nil, fmt.Errorf("database: %w", err)
		case <-time.After(time.Second):
		}
	}
}

func openRedis(cfg config.Base, logger *slog.Logger) redis.UniversalClient {
	if cfg.RedisURL == "" {
		logger.Warn("Redis disabled: REDIS_URL not set. IdempotencyMiddleware disabled")
		return nil
	}
	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		logger.Warn("Redis disabled: could not parse REDIS_URL", "error", err)
		return nil
	}
	return redis.NewClient(opts)
}

func buildDeps(ctx context.Context, cfg config.Base, logger *slog.Logger, withDB bool) (*server.Deps, func(), error) {
	d := &server.Deps{Config: cfg, Logger: logger, Health: server.NewHealth()}
	cleanup := func() {}
	if withDB {
		pool, err := openDB(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		d.DB = pool
		d.Queries = store.New(pool)
		cleanup = pool.Close
	}
	d.Redis = openRedis(cfg, logger)
	d.Auth = &auth.Authenticator{Signer: auth.NewSigner(cfg.JWTSecret), Store: auth.PGStore{Q: d.Queries}}
	d.Admin = auth.Admin{
		IDs: cfg.AdminUserIDs(),
		UserExists: func(ctx context.Context, id uuid.UUID) (bool, error) {
			return d.Queries.UserExists(ctx, id)
		},
	}
	d.Limiter = &middleware.Limiter{
		Redis:      d.Redis,
		ClientIP:   middleware.NewClientIP(cfg.TrustedProxyCIDRs, cfg.EdgeClientIPKey),
		Production: cfg.IsProduction(),
		Logger:     logger,
	}
	server.AddCoreHealthChecks(d)
	return d, cleanup, nil
}

// Shutdown budget, measured from SIGTERM and kept inside the kubelet's
// default 30 second grace period: HTTP gets 15 seconds to finish requests,
// every drain (Telegram turns, jobs, detached work) shares what is left until
// 24 seconds, then the lock session and the pool get 2 and 3 seconds to close.
// Past each bound serve logs and moves on rather than being SIGKILLed with
// nothing logged.
const (
	httpShutdownBudget     = 15 * time.Second
	drainDeadline          = 24 * time.Second
	lockSessionCloseBudget = 2 * time.Second
	poolCloseBudget        = 3 * time.Second
)

func serve(ctx context.Context, cfg config.Base, logger *slog.Logger) error {
	d, cleanup, err := buildDeps(ctx, cfg, logger, true)
	if err != nil {
		return err
	}
	// pool.Close waits for every acquired connection; do not let one stuck
	// query hold the process past its grace period.
	defer closeWithin(poolCloseBudget, logger, "database pool", cleanup)

	// Every job lock lives on one extra connection (joblock.Session), so no
	// tick holds a pool connection while its body needs the pool.
	d.LockSession = joblock.NewSession(d.DB)
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), lockSessionCloseBudget)
		defer cancel()
		d.LockSession.Close(closeCtx)
	}()

	// lifetime bounds detached work (exports, group fan-out, live achievement
	// checks). It outlives ctx so requests drained by Shutdown can still
	// start that work, and ends once the HTTP server has stopped.
	lifetime, endLifetime := context.WithCancel(context.WithoutCancel(ctx))
	defer endLifetime()

	srv, err := server.New(d, features.All(lifetime))
	if err != nil {
		return err
	}
	// Pool stats are read when Prometheus scrapes, not per request.
	metrics.Shared.SetPoolStats(func() metrics.PoolStats {
		s := d.DB.Stat()
		return metrics.PoolStats{
			AcquireCount:         s.AcquireCount(),
			AcquireDuration:      s.AcquireDuration(),
			AcquiredConns:        s.AcquiredConns(),
			IdleConns:            s.IdleConns(),
			TotalConns:           s.TotalConns(),
			MaxConns:             s.MaxConns(),
			EmptyAcquireCount:    s.EmptyAcquireCount(),
			CanceledAcquireCount: s.CanceledAcquireCount(),
		}
	})
	jobs := features.StartJobs(ctx, d)
	pprof.Start(ctx, cfg.PprofAddr, logger)

	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           srv.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	// Open realtime streams end with the process (RealtimeBus shutdown):
	// Shutdown does not wait for hijacked or long-lived SSE responses.
	httpServer.RegisterOnShutdown(realtime.FinishAll)
	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "port", cfg.Port, "env", cfg.Environment)
		errCh <- httpServer.ListenAndServe()
	}()

	var shutdownStart time.Time
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		shutdownStart = time.Now()
	case <-ctx.Done():
		shutdownStart = time.Now()
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithDeadline(context.Background(), shutdownStart.Add(httpShutdownBudget))
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown", "error", err)
		}
	}
	// Jobs (ctx) and detached work (lifetime) are both canceled now, and
	// TelegramTurnDrain cancels the bot turns. They share one deadline to
	// return before the pool closes; the shutdown hooks also refuse new
	// detached work from handlers that outlived Shutdown.
	endLifetime()
	drainCtx, cancelDrain := context.WithDeadline(context.Background(), shutdownStart.Add(drainDeadline))
	defer cancelDrain()
	var drain sync.WaitGroup
	drain.Go(func() {
		if !assistant.DrainTelegramTurnsUntil(drainCtx) {
			logger.Warn("shutdown drain timed out; Telegram turns still running")
		}
	})
	drain.Go(func() { jobs.Wait(drainCtx, logger) })
	drain.Go(func() { d.Health.RunShutdownHooks(drainCtx, logger) })
	drain.Wait()
	return nil
}

// closeWithin runs closeFn and returns once it has, or after budget with a
// warning: the process is exiting, and the kernel closes what is left.
func closeWithin(budget time.Duration, logger *slog.Logger, what string, closeFn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		closeFn()
	}()
	select {
	case <-done:
	case <-time.After(budget):
		logger.Warn("shutdown: close timed out; exiting anyway", "resource", what)
	}
}

func runMigrate(ctx context.Context, cfg config.Base, logger *slog.Logger, args []string) error {
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	conn := stdlib.OpenDBFromPool(pool)
	defer conn.Close()
	if len(args) > 0 && args[0] == "status" {
		return migrate.Status(ctx, conn, logger)
	}
	return migrate.Up(ctx, conn, logger)
}

func printRoutes(cfg config.Base, _ *slog.Logger) error {
	// stdout is the route list: keep features' boot logs out of it.
	logger := slog.New(slog.DiscardHandler)
	d, _, err := buildDeps(context.Background(), cfg, logger, false)
	if err != nil {
		return err
	}
	srv, err := server.New(d, features.All(context.Background()))
	if err != nil {
		return err
	}
	var lines []string
	for _, r := range srv.Echo.Router().Routes() {
		lines = append(lines, r.Method+" "+r.Path)
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Println(l)
	}
	return nil
}
