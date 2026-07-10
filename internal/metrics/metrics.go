// Package metrics 集中定义并注册网关的 Prometheus 指标。
//
// 所有指标用包级单例注册到默认 Registry，通过 server 的 /metrics 端点暴露。
// 设计原则：标签基数可控——只用有限枚举（path/method/status/format/result/channel_id）
// 作标签，绝不把模型名、用户 ID 等高基数值放进标签，避免时间序列爆炸。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// HTTPRequestsTotal 统计对外 HTTP 请求数，按路由模板/方法/状态码分组。
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "linapi_http_requests_total",
		Help: "对外 HTTP 请求总数",
	}, []string{"path", "method", "status"})

	// HTTPRequestDuration 记录对外 HTTP 请求处理耗时（秒）。
	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "linapi_http_request_duration_seconds",
		Help:    "对外 HTTP 请求处理耗时（秒）",
		Buckets: prometheus.DefBuckets,
	}, []string{"path", "method"})

	// UpstreamRequestsTotal 统计向上游渠道发起的请求数，按渠道/格式/结果分组。
	// result 取 success | failure，用于观测各渠道成功率与故障转移。
	UpstreamRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "linapi_upstream_requests_total",
		Help: "向上游渠道发起的请求总数",
	}, []string{"channel_id", "format", "result"})

	// UpstreamRequestDuration 记录单次上游请求耗时（秒），按渠道/格式分组。
	UpstreamRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "linapi_upstream_request_duration_seconds",
		Help:    "单次上游渠道请求耗时（秒）",
		Buckets: prometheus.DefBuckets,
	}, []string{"channel_id", "format"})

	// CircuitBreakerState 反映每渠道熔断器状态：0=closed，1=half-open，2=open。
	CircuitBreakerState = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "linapi_circuit_breaker_state",
		Help: "渠道熔断器状态：0=closed 1=half-open 2=open",
	}, []string{"channel_id"})
)

// ObserveUpstream 记录一次上游请求的结果与耗时。
func ObserveUpstream(channelID, format string, success bool, seconds float64) {
	result := "failure"
	if success {
		result = "success"
	}
	UpstreamRequestsTotal.WithLabelValues(channelID, format, result).Inc()
	UpstreamRequestDuration.WithLabelValues(channelID, format).Observe(seconds)
}

// SetBreakerState 设置某渠道熔断器状态的 gauge 值。
func SetBreakerState(channelID string, state int) {
	CircuitBreakerState.WithLabelValues(channelID).Set(float64(state))
}
