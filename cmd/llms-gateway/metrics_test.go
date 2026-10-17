package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

type oneGauge struct{ d *prometheus.Desc }

func (g oneGauge) Describe(ch chan<- *prometheus.Desc) { ch <- g.d }
func (g oneGauge) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(g.d, prometheus.GaugeValue, 1)
}

// /metrics answers in the Prometheus text exposition format with the app collector and the Go runtime metrics.
func TestMetricsHandlerTextExposition(t *testing.T) {
	h := metricsHandler(oneGauge{prometheus.NewDesc("llms_test_gauge", "test", nil, nil)})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	ct := rec.Header().Get("Content-Type")
	if rec.Code != 200 || !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Fatalf("status %d content-type %q", rec.Code, ct)
	}
	for _, want := range []string{"llms_test_gauge 1", "go_goroutines", "process_"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("missing %q in /metrics", want)
		}
	}
}
