package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/goodtek-xyz/openllms/internal/proxy"
	"github.com/goodtek-xyz/openllms/internal/secrets"
	"github.com/goodtek-xyz/openllms/internal/secrets/memory"
	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/goodtek-xyz/openllms/internal/vendor"
	"github.com/google/uuid"
)

// slowStream is an upstream that sends SSE events with a pause between them, for as long as total; it reports
// whether the client went away before the end.
func slowStream(t *testing.T, events int, gap time.Duration, gone chan<- struct{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for i := 0; i < events; i++ {
			_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"x"}}]}`+"\n\n")
			f.Flush()
			select {
			case <-r.Context().Done():
				if gone != nil {
					close(gone)
				}
				return
			case <-time.After(gap): // the scenario under test: a stream that keeps going
			}
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
}

func streamAccount(t *testing.T, base string) (*store.Account, *memory.Client) {
	t.Helper()
	sec := memory.New()
	path := vendor.InfisicalPath(uuid.New().String(), "deepseek", "default")
	_ = sec.Put(context.Background(), path, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-up"}))
	return &store.Account{ID: uuid.New(), InfisicalPath: path, BaseURL: base + "/v1", Vendor: "deepseek"}, sec
}

// A stream that lasts longer than a Client.Timeout is cut by that timeout (net/http counts the body in it); the
// upstream client has none, so the same stream arrives complete.
func TestUpstreamClientKeepsLongStreams(t *testing.T) {
	up := slowStream(t, 6, 60*time.Millisecond, nil) // about 360 ms in total
	defer up.Close()
	acc, sec := streamAccount(t, up.URL)
	body := []byte(`{"model":"x","stream":true,"messages":[]}`)

	read := func(c *http.Client) (string, error) {
		eng := &proxy.Engine{Secrets: sec, HTTP: c}
		res, err := eng.CallAccountForTest(context.Background(), acc, body)
		if err != nil {
			return "", err
		}
		if res.Stream == nil {
			return string(res.Body), nil
		}
		defer res.Stream.Close()
		b, err := io.ReadAll(res.Stream)
		return string(b), err
	}

	// Before: a fixed total timeout shorter than the stream (the old client had 120 s).
	if b, err := read(&http.Client{Timeout: 150 * time.Millisecond}); err == nil && strings.Contains(b, "[DONE]") {
		t.Fatal("a Client.Timeout shorter than the stream should cut it; the scenario does not reproduce the bug")
	}
	// After.
	c := proxy.NewUpstreamClient()
	if c.Timeout != 0 {
		t.Fatalf("upstream client Timeout = %v, want 0 (no total limit)", c.Timeout)
	}
	b, err := read(c)
	if err != nil || !strings.Contains(b, "[DONE]") || strings.Count(b, `"content":"x"`) != 6 {
		t.Fatalf("long stream through the upstream client: err=%v body=%q", err, b)
	}
}

// Without a total limit the client's request context is what ends a call: when the caller goes away mid-stream,
// the upstream connection is closed too.
func TestUpstreamCallEndsWithCallerContext(t *testing.T) {
	gone := make(chan struct{})
	up := slowStream(t, 1000, 20*time.Millisecond, gone)
	defer up.Close()
	acc, sec := streamAccount(t, up.URL)
	eng := &proxy.Engine{Secrets: sec, HTTP: proxy.NewUpstreamClient()}
	ctx, cancel := context.WithCancel(context.Background())
	res, err := eng.CallAccountForTest(ctx, acc, []byte(`{"model":"x","stream":true,"messages":[]}`))
	if err != nil || res.Stream == nil {
		t.Fatalf("err=%v stream=%v", err, res.Stream != nil)
	}
	buf := make([]byte, 64)
	if _, err := res.Stream.Read(buf); err != nil {
		t.Fatalf("first read: %v", err)
	}
	cancel() // the client disconnects
	_, _ = io.Copy(io.Discard, res.Stream)
	_ = res.Stream.Close()
	select {
	case <-gone:
	case <-time.After(5 * time.Second): // test guard only; the upstream sees the cancel within one event gap
		t.Fatal("upstream kept streaming after the caller's context ended")
	}
}
