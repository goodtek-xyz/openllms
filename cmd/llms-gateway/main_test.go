package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goodtek-xyz/openllms/internal/config"
)

func TestWaitForStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	boom := errors.New("not yet")
	calls := 0
	err := waitFor(ctx, "test", func() error {
		calls++
		cancel()
		return boom
	})
	if !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("waitFor = %v after %d calls, want the op error after 1 call", err, calls)
	}
}

func getStatus(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return res.StatusCode, body
}

// While the database is unreachable the gateway already listens: liveness is up, start-up is reported as not
// finished, other paths are refused, and a shutdown signal ends start-up cleanly and closes the listener.
func TestRunServesAndShutsDownDuringStartup(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := config.Config{DatabaseURL: "postgres://llms:llms@127.0.0.1:1/llms?sslmode=disable&connect_timeout=1"}
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, ln, nil) }()

	if code, body := getStatus(t, base+"/health"); code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("/health while starting = %d %v, want 200 ok", code, body)
	}
	if code, body := getStatus(t, base+"/livez"); code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("/livez while starting = %d %v, want 200 ok", code, body)
	}
	if code, body := getStatus(t, base+"/readyz"); code != http.StatusServiceUnavailable || body["status"] != "starting" {
		t.Fatalf("/readyz while starting = %d %v, want 503 starting", code, body)
	}
	if code, body := getStatus(t, base+"/ready"); code != http.StatusServiceUnavailable || body["error"] != "starting" {
		t.Fatalf("/ready while starting = %d %v, want 503 starting", code, body)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run after shutdown signal = %v, want nil", err)
	}
	if _, err := http.Get(base + "/health"); err == nil {
		t.Fatal("listener still serving after shutdown")
	}
}

// A start-up error that retrying cannot fix (here: no secrets directory) ends run with that error, so the process
// exits non-zero, and the listener is closed.
func TestRunFailsOnFatalStartupError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DatabaseURL: "sqlite:" + filepath.Join(t.TempDir(), "llms.db")}
	if err := run(context.Background(), cfg, ln, nil); err == nil || !strings.Contains(err.Error(), "secrets") {
		t.Fatalf("run = %v, want a secrets error", err)
	}
	if _, err := http.Get("http://" + ln.Addr().String() + "/health"); err == nil {
		t.Fatal("listener still serving after a fatal start-up error")
	}
}

// With a reachable database start-up finishes: run signals it, /readyz answers 200 and the full router serves; a
// shutdown signal then shuts the server down gracefully and run returns nil with the listener closed.
func TestRunStartsAndShutsDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := config.Config{DatabaseURL: "sqlite:" + filepath.Join(dir, "llms.db"), SecretsDir: filepath.Join(dir, "secrets")}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, ln, func() { close(started) }) }()

	limit, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("run ended during start-up: %v", err)
	case <-limit.Done():
		t.Fatal("start-up did not finish within 10 s")
	}
	if code, body := getStatus(t, base+"/readyz"); code != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("/readyz after start-up = %d %v, want 200 ready", code, body)
	}
	if code, body := getStatus(t, base+"/ready"); code != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("/ready after start-up = %d %v, want 200 ready", code, body)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run after shutdown signal = %v, want nil", err)
	}
	if _, err := http.Get(base + "/health"); err == nil {
		t.Fatal("listener still serving after shutdown")
	}
}
