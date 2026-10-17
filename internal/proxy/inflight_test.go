package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/goodtek-xyz/openllms/internal/proxy"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func gaugeValues(t *testing.T, c prometheus.Collector) map[string]float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			key := mf.GetName()
			for _, l := range m.GetLabel() {
				key += "|" + l.GetName() + "=" + l.GetValue()
			}
			out[key] = gauge(m)
		}
	}
	return out
}

func gauge(m *dto.Metric) float64 { return m.GetGauge().GetValue() }

// A stream counts as in flight from the upstream request until its body is closed; the oldest age grows while it
// runs; both drop back to 0 afterwards. Labels are provider and vendor only.
func TestInflightMetricsFollowAStream(t *testing.T) {
	up := slowStream(t, 5, 40*time.Millisecond, nil)
	defer up.Close()
	acc, sec := streamAccount(t, up.URL) // vendor deepseek
	eng := &proxy.Engine{Secrets: sec, HTTP: proxy.NewUpstreamClient()}
	res, err := eng.CallAccountForTest(context.Background(), acc, []byte(`{"model":"x","stream":true,"messages":[]}`))
	if err != nil || res.Stream == nil {
		t.Fatalf("err=%v", err)
	}
	buf := make([]byte, 64)
	if _, err := res.Stream.Read(buf); err != nil {
		t.Fatal(err)
	}
	mid := gaugeValues(t, eng.MetricsCollector())
	if mid["llms_upstream_requests_in_flight|provider=deepseek|vendor=deepseek"] != 1 {
		t.Fatalf("mid-stream in flight: %v", mid)
	}
	if mid["llms_upstream_oldest_request_age_seconds|provider=deepseek|vendor=deepseek"] <= 0 {
		t.Fatalf("mid-stream oldest age: %v", mid)
	}
	_, _ = io.Copy(io.Discard, res.Stream)
	_ = res.Stream.Close()
	after := gaugeValues(t, eng.MetricsCollector())
	if after["llms_upstream_requests_in_flight|provider=deepseek|vendor=deepseek"] != 0 ||
		after["llms_upstream_oldest_request_age_seconds|provider=deepseek|vendor=deepseek"] != 0 {
		t.Fatalf("after the stream: %v", after)
	}
}

// A call that fails to connect is not left counted.
func TestInflightMetricsDropFailedCalls(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	acc, sec := streamAccount(t, url)
	acc.Vendor = "codex"
	acc.AuthType = "api_key"
	eng := &proxy.Engine{Secrets: sec, HTTP: proxy.NewUpstreamClient()}
	res, _ := eng.CallAccountForTest(context.Background(), acc, []byte(`{"model":"x","messages":[]}`))
	if res == nil || res.StatusCode != http.StatusBadGateway {
		t.Fatalf("want a 502 result for a refused connection, got %+v", res)
	}
	got := gaugeValues(t, eng.MetricsCollector())
	if got["llms_upstream_requests_in_flight|provider=openai|vendor=codex"] != 0 {
		t.Fatalf("failed call still counted: %v", got)
	}
}
