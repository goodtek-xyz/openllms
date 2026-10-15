package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"canceled", context.Canceled, false},
		{"admin shutdown", &pgconn.PgError{Code: "57P01"}, true},
		{"cannot connect now", &pgconn.PgError{Code: "57P03"}, true},
		{"read-only after switchover", &pgconn.PgError{Code: "25006"}, true},
		{"connection failure", &pgconn.PgError{Code: "08006"}, true},
		{"unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"syntax error", &pgconn.PgError{Code: "42601"}, false},
		{"plain error", errors.New("boom"), false},
	}
	for _, c := range cases {
		if got := retryable(c.err); got != c.want {
			t.Errorf("%s: retryable = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestWithRetryRecoversAfterSwitchover(t *testing.T) {
	calls := 0
	v, err := withRetry(context.Background(), func() (int, error) {
		calls++
		if calls < 3 {
			return 0, &pgconn.PgError{Code: "57P01"}
		}
		return 42, nil
	})
	if err != nil || v != 42 || calls != 3 {
		t.Fatalf("got v=%d err=%v calls=%d, want 42 nil 3", v, err, calls)
	}
}

func TestWithRetryDoesNotRetryOtherErrors(t *testing.T) {
	calls := 0
	_, err := withRetry(context.Background(), func() (int, error) {
		calls++
		return 0, &pgconn.PgError{Code: "23505"}
	})
	if err == nil || calls != 1 {
		t.Fatalf("got err=%v calls=%d, want an error after 1 call", err, calls)
	}
}

func TestWithRetryStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := withRetry(ctx, func() (int, error) { return 0, &pgconn.PgError{Code: "57P03"} })
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("got err=%v after %v, want an error soon after the context ends", err, time.Since(start))
	}
}
