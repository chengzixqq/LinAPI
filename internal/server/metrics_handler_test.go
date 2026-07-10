package server

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"linapi/internal/config"
)

type blockingCollector struct {
	desc    *prometheus.Desc
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *blockingCollector) Collect(ch chan<- prometheus.Metric) {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, 1)
}

func TestMetricsHandlerRejectsExcessConcurrentScrapes(t *testing.T) {
	collector := &blockingCollector{
		desc:    prometheus.NewDesc("linapi_test_blocking", "test", nil, nil),
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	h := newMetricsHandler(config.ServerConfig{
		MetricsMaxRequestsInFlight: 1,
		MetricsTimeoutSeconds:      5,
	}, registry)

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/metrics", nil))
	}()
	select {
	case <-collector.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("首个抓取未进入 collector")
	}

	second := httptest.NewRecorder()
	h.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("并发预算耗尽 status=%d, want 503", second.Code)
	}
	close(collector.release)
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("释放 collector 后首个抓取未退出")
	}
}

func TestMetricsHandlerTimeout(t *testing.T) {
	collector := &blockingCollector{
		desc:    prometheus.NewDesc("linapi_test_timeout", "test", nil, nil),
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	h := newMetricsHandler(config.ServerConfig{
		MetricsMaxRequestsInFlight: 1,
		MetricsTimeoutSeconds:      1,
	}, registry)

	recorder := httptest.NewRecorder()
	started := time.Now()
	h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	close(collector.release)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("超时抓取 status=%d, want 503", recorder.Code)
	}
	if elapsed := time.Since(started); elapsed < 800*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("超时预算未生效: elapsed=%s", elapsed)
	}
}
