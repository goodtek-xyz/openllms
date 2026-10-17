package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metricsHandler serves the Prometheus text exposition of the gateway's own registry: upstream calls in flight plus
// the standard Go runtime and process collectors.
func metricsHandler(app prometheus.Collector) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(app, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg})
}
