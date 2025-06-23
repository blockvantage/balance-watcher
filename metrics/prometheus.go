package metrics

import (
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

// PrometheusMetrics holds all Prometheus metrics
type PrometheusMetrics struct {
	addressBalances    *prometheus.GaugeVec
	refillCounts       *prometheus.CounterVec
	errorCounts        *prometheus.CounterVec
	queueSize          prometheus.Gauge
	uptime             prometheus.Counter
	startTime          time.Time
	server             *http.Server
	balancesByAddress  map[common.Address]float64
	refillsByAddress   map[common.Address]int
	errorsByAddress    map[common.Address]int
}

// NewPrometheusMetrics creates a new Prometheus metrics exporter
func NewPrometheusMetrics() *PrometheusMetrics {
	return &PrometheusMetrics{
		addressBalances: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "balance_watcher_address_balance",
			Help: "The balance of each monitored address in ETH",
		}, []string{"address"}),
		refillCounts: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "balance_watcher_refill_count",
			Help: "The number of refills per address",
		}, []string{"address"}),
		errorCounts: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "balance_watcher_error_count",
			Help: "The number of errors per address",
		}, []string{"address"}),
		queueSize: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "balance_watcher_queue_size",
			Help: "The current size of the job queue",
		}),
		uptime: promauto.NewCounter(prometheus.CounterOpts{
			Name: "balance_watcher_uptime_seconds",
			Help: "The uptime of the service in seconds",
		}),
		startTime:         time.Now(),
		balancesByAddress: make(map[common.Address]float64),
		refillsByAddress:  make(map[common.Address]int),
		errorsByAddress:   make(map[common.Address]int),
	}
}

// StartServer starts the Prometheus metrics server
func (p *PrometheusMetrics) StartServer(addr string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	p.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		logrus.WithField("addr", addr).Info("Starting Prometheus metrics server")
		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logrus.WithError(err).Error("Failed to start Prometheus metrics server")
		}
	}()

	// Start updating uptime
	go p.updateUptime()
}

// StopServer stops the Prometheus metrics server
func (p *PrometheusMetrics) StopServer() error {
	logrus.Info("Stopping Prometheus metrics server")
	return p.server.Close()
}

// UpdateAddressBalance updates the balance for an address
func (p *PrometheusMetrics) UpdateAddressBalance(address common.Address, balanceEth float64) {
	p.balancesByAddress[address] = balanceEth
	p.addressBalances.WithLabelValues(address.Hex()).Set(balanceEth)
}

// IncrementRefillCount increments the refill count for an address
func (p *PrometheusMetrics) IncrementRefillCount(address common.Address) {
	p.refillsByAddress[address]++
	p.refillCounts.WithLabelValues(address.Hex()).Inc()
}

// IncrementErrorCount increments the error count for an address
func (p *PrometheusMetrics) IncrementErrorCount(address common.Address) {
	p.errorsByAddress[address]++
	p.errorCounts.WithLabelValues(address.Hex()).Inc()
}

// UpdateQueueSize updates the queue size metric
func (p *PrometheusMetrics) UpdateQueueSize(size int) {
	p.queueSize.Set(float64(size))
}

// updateUptime updates the uptime metric every second
func (p *PrometheusMetrics) updateUptime() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		p.uptime.Inc()
	}
}

// GetBalancesByAddress returns the balances by address
func (p *PrometheusMetrics) GetBalancesByAddress() map[common.Address]float64 {
	return p.balancesByAddress
}

// GetRefillsByAddress returns the refills by address
func (p *PrometheusMetrics) GetRefillsByAddress() map[common.Address]int {
	return p.refillsByAddress
}

// GetErrorsByAddress returns the errors by address
func (p *PrometheusMetrics) GetErrorsByAddress() map[common.Address]int {
	return p.errorsByAddress
}
