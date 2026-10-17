package proxy

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

// inflight counts upstream HTTP calls from just before the request is sent until the response body is closed — for a
// stream, until the stream ends. The values are computed when they are scraped, so no refresh interval is involved.
// The zero value is ready to use.
type inflight struct {
	mu    sync.Mutex
	seq   uint64
	calls map[uint64]inflightCall
	seen  map[inflightLabels]struct{} // label sets ever seen, reported as 0 when idle
}

type inflightLabels struct{ provider, vendor string }

type inflightCall struct {
	labels inflightLabels
	start  time.Time
}

// upstreamLabels keeps cardinality low: the account's vendor and the provider family it talks to.
func upstreamLabels(acc *store.Account) inflightLabels {
	v := strings.ToLower(strings.TrimSpace(acc.Vendor))
	if v == "" {
		v = "unknown"
	}
	p := v
	switch v {
	case "claude", "anthropic":
		p = "anthropic"
	case "codex", "openai":
		p = "openai"
	}
	return inflightLabels{provider: p, vendor: v}
}

func (t *inflight) begin(l inflightLabels, now time.Time) (done func()) {
	t.mu.Lock()
	if t.calls == nil {
		t.calls = map[uint64]inflightCall{}
		t.seen = map[inflightLabels]struct{}{}
	}
	t.seq++
	id := t.seq
	t.calls[id] = inflightCall{labels: l, start: now}
	t.seen[l] = struct{}{}
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			delete(t.calls, id)
			t.mu.Unlock()
		})
	}
}

type inflightStat struct {
	count  int
	oldest time.Duration
}

func (t *inflight) snapshot(now time.Time) map[inflightLabels]inflightStat {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[inflightLabels]inflightStat, len(t.seen))
	for l := range t.seen {
		out[l] = inflightStat{}
	}
	for _, c := range t.calls {
		s := out[c.labels]
		s.count++
		if age := now.Sub(c.start); age > s.oldest {
			s.oldest = age
		}
		out[c.labels] = s
	}
	return out
}

// doUpstreamHTTP sends req and counts it in flight until the response body is closed.
func (e *Engine) doUpstreamHTTP(client *http.Client, acc *store.Account, req *http.Request) (*http.Response, error) {
	done := e.inflight.begin(upstreamLabels(acc), time.Now())
	res, err := client.Do(req)
	if err != nil {
		done()
		return nil, err
	}
	res.Body = &doneOnClose{ReadCloser: res.Body, done: done}
	return res, nil
}

type doneOnClose struct {
	io.ReadCloser
	done func()
}

func (d *doneOnClose) Close() error {
	err := d.ReadCloser.Close()
	d.done()
	return err
}

var (
	descInFlight = prometheus.NewDesc("llms_upstream_requests_in_flight",
		"Upstream LLM HTTP calls in progress, from send until the response body (or stream) is closed.",
		[]string{"provider", "vendor"}, nil)
	descOldest = prometheus.NewDesc("llms_upstream_oldest_request_age_seconds",
		"Age of the oldest upstream LLM HTTP call in progress; 0 when none.",
		[]string{"provider", "vendor"}, nil)
)

// MetricsCollector exposes the in-flight upstream calls of this engine to a Prometheus registry.
func (e *Engine) MetricsCollector() prometheus.Collector { return inflightCollector{e: e} }

type inflightCollector struct{ e *Engine }

func (c inflightCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descInFlight
	ch <- descOldest
}

func (c inflightCollector) Collect(ch chan<- prometheus.Metric) {
	for l, s := range c.e.inflight.snapshot(time.Now()) {
		ch <- prometheus.MustNewConstMetric(descInFlight, prometheus.GaugeValue, float64(s.count), l.provider, l.vendor)
		ch <- prometheus.MustNewConstMetric(descOldest, prometheus.GaugeValue, s.oldest.Seconds(), l.provider, l.vendor)
	}
}
