package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// HTTP метрики
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "path", "status"},
	)
	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "http_request_duration_seconds",
			Help: "Duration of HTTP requests in seconds",
		},
		[]string{"method", "path"},
	)
	HTTPRequestsInFlight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "Current number of HTTP requests in flight",
		},
	)

	// Binance API метрики
	BinanceAPIRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "binance_api_requests_total",
			Help: "Total number of Binance API requests",
		},
		[]string{"endpoint", "status"},
	)
	BinanceAPIRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "binance_api_request_duration_seconds",
			Help: "Duration of Binance API requests in seconds",
		},
		[]string{"endpoint"},
	)
	BinanceWebSocketConnections = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "binance_websocket_connections",
			Help: "Number of active Binance WebSocket connections",
		},
		[]string{"market"},
	)
	BinanceActiveSignals = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "binance_active_signals",
			Help: "Number of active signals per user",
		},
		[]string{"user_id"},
	)

	// Proxy метрики
	ProxyContainers = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "proxy_containers_total",
			Help: "Total number of running proxy containers",
		},
	)
)

func InitMetrics() {
	// Регистрация HTTP метрик
	prometheus.MustRegister(HTTPRequestsTotal)
	prometheus.MustRegister(HTTPRequestDuration)
	prometheus.MustRegister(HTTPRequestsInFlight)

	// Регистрация Binance метрик
	prometheus.MustRegister(BinanceAPIRequestsTotal)
	prometheus.MustRegister(BinanceAPIRequestDuration)
	prometheus.MustRegister(BinanceWebSocketConnections)
	prometheus.MustRegister(BinanceActiveSignals)

	// Регистрация Proxy метрик
	prometheus.MustRegister(ProxyContainers)

	// Стандартные метрики Go
	prometheus.MustRegister(prometheus.NewGoCollector())
	prometheus.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
}
