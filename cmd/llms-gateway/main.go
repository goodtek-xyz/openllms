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
	"sync"
	"syscall"
	"time"

	"github.com/goodtek-xyz/openllms/internal/backoff"
	"github.com/goodtek-xyz/openllms/internal/config"
	"github.com/goodtek-xyz/openllms/internal/db"
	"github.com/goodtek-xyz/openllms/internal/health"
	"github.com/goodtek-xyz/openllms/internal/httpserver"
	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// waitFor retries a start-up dependency (the database may still be starting or switching its primary) with
// exponential backoff and full jitter, 1 s doubling to 30 s, until it succeeds or ctx ends (SIGTERM), instead of
// exiting and restarting until the database is ready.
func waitFor(ctx context.Context, what string, op func() error) error {
	b := backoff.Backoff{Base: time.Second, Cap: 30 * time.Second}
	for {
		err := op()
		if err == nil {
			return nil
		}
		pause := b.Next()
		slog.Warn("waiting for dependency", "what", what, "err", err, "retry_in", pause.String())
		if backoff.Sleep(ctx, pause) != nil {
			return err
		}
	}
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// SIGTERM/SIGINT ends start-up (a rollout or drain while waiting for the database) or the running gateway.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}

	if err := run(ctx, cfg, ln, nil); err != nil {
		slog.Error("llms-gateway", "err", err)
		os.Exit(1)
	}
}

// run serves on ln from the start: while migrations, the database connection and secrets are set up it answers
// liveness and reports "starting" (httpserver.Startup), then hands over to the full router and calls onStarted, if
// set. It returns nil once ctx ends (shutdown signal) and the server has shut down gracefully, or the error that
// stopped the gateway.
func run(ctx context.Context, cfg config.Config, ln net.Listener, onStarted func()) error {
	ctx, fail := context.WithCancelCause(ctx)
	defer fail(nil)

	gate := httpserver.NewStartup()
	httpSrv := &http.Server{
		Handler:           gate,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := httpSrv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			fail(fmt.Errorf("serve: %w", err))
		}
	}()
	slog.Info("llms-gateway listening", "addr", ln.Addr().String())

	// Deferred before the database is opened, so on an early return it runs after the database is closed; that is
	// fine because only the start-up handler is serving then. After start-up it is called explicitly, before the
	// database is closed, so in-flight requests finish first.
	shutdown := sync.OnceFunc(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	})
	defer shutdown()

	// startErr ends start-up: cleanly when ctx ended (shutdown signal), else with the step's error.
	startErr := func(step string, err error) error {
		if ctx.Err() != nil {
			return stopReason(ctx)
		}
		return fmt.Errorf("%s: %w", step, err)
	}

	if err := waitFor(ctx, "migrate", func() error { return db.Migrate(cfg.DatabaseURL) }); err != nil {
		return startErr("migrate", err)
	}

	var st *store.Store
	if db.IsSQLite(cfg.DatabaseURL) {
		sqldb, err := db.ConnectSQLite(ctx, cfg.DatabaseURL)
		if err != nil {
			return startErr("sqlite connect", err)
		}
		defer sqldb.Close()
		st = store.NewSQLiteWithCacheTTL(sqldb, cfg.AuthCacheTTL, cfg.RouteCacheTTL)
		slog.Info("database backend", "kind", "sqlite", "url", cfg.DatabaseURL)
	} else {
		var pool *pgxpool.Pool
		err := waitFor(ctx, "db connect", func() error {
			var cerr error
			pool, cerr = db.Connect(ctx, cfg.DatabaseURL)
			return cerr
		})
		if err != nil {
			return startErr("db connect", err)
		}
		defer pool.Close()
		st = store.NewWithCacheTTL(pool, cfg.AuthCacheTTL, cfg.RouteCacheTTL)
		slog.Info("database backend", "kind", "postgres")
	}

	secret, err := openSecrets(ctx, &cfg)
	if err != nil {
		return startErr("secrets", err)
	}

	srv := httpserver.New(st, cfg, secret)
	refreshCtx, refreshCancel := context.WithCancel(context.Background())
	defer refreshCancel()
	srv.StartQuotaRefresh(refreshCtx, 5*time.Minute)
	prober := &health.Prober{Store: st, Secrets: secret, HTTP: &http.Client{Timeout: 8 * time.Second}}
	if secret != nil {
		prober.Start(refreshCtx, 2*time.Minute)
	}

	gate.Started(srv.Router())
	slog.Info("llms-gateway started")
	if onStarted != nil {
		onStarted()
	}

	<-ctx.Done()
	shutdown()
	return stopReason(ctx)
}

// stopReason explains why the gateway stops once ctx is done: nil for a shutdown signal, else the serve error.
func stopReason(ctx context.Context) error {
	if err := context.Cause(ctx); !errors.Is(err, context.Canceled) {
		return err
	}
	slog.Info("shutting down")
	return nil
}
