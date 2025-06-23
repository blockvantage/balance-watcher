package main

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/blockvantage/balance-watcher/config"
	"github.com/blockvantage/balance-watcher/database"
	"github.com/blockvantage/balance-watcher/metrics"
	"github.com/blockvantage/balance-watcher/notifier"
	"github.com/blockvantage/balance-watcher/worker"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/params"
	"github.com/sirupsen/logrus"
)

func main() {
	// Set up logging
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})
	logrus.SetOutput(os.Stdout)
	logrus.SetLevel(logrus.InfoLevel)

	// Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		logrus.WithError(err).Fatal("Failed to load configuration")
	}

	// Connect to Ethereum node
	client, err := ethclient.Dial(cfg.Ethereum.NodeURL)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to connect to Ethereum node")
	}
	logrus.Info("Connected to Ethereum node")

	// Verify the connection with a simple call
	if _, err := client.BlockNumber(context.Background()); err != nil {
		logrus.WithError(err).Fatal("Failed to get block number from Ethereum node")
	}

	// Initialize database
	db, err := database.New(cfg.Database.Path)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to initialize database")
	}
	defer db.Close()
	logrus.Info("Database initialized")

	// Initialize Telegram notifier
	var telegramNotifier *notifier.TelegramNotifier
	if cfg.Telegram.Enabled {
		var err error
		telegramNotifier, err = notifier.NewTelegramNotifier(cfg.Telegram.BotToken, cfg.Telegram.ChatID)
		if err != nil {
			logrus.WithError(err).Fatal("Failed to initialize Telegram notifier")
		}
		logrus.Info("Telegram notifier initialized")
	} else {
		logrus.Info("Telegram notifier disabled")
	}

	// Initialize Prometheus metrics
	prometheusMetrics := metrics.NewPrometheusMetrics()
	prometheusMetrics.StartServer(":" + cfg.Monitoring.PrometheusPort)
	logrus.Info("Prometheus metrics server started on :" + cfg.Monitoring.PrometheusPort)

	// Create worker pool
	workerPool := worker.NewWorkerPool(
		client,
		db,
		telegramNotifier,
		prometheusMetrics,
		cfg.Monitoring.GetBalanceThresholdWei(),
		cfg.Monitoring.GetRefillAmountWei(),
		cfg.Monitoring.GetMaxRefillAmountWei(),
		cfg.Monitoring.RefillThresholdCount,
		cfg.Monitoring.RefillThresholdWindow,
		cfg.Workers.PoolSize,
		cfg.Ethereum.PrivateKey,
	)

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start worker pool
	workerPool.Start(ctx, cfg.Workers.PoolSize)

	// Set up signal handling for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start monitoring loop
	go monitorAddresses(ctx, cfg, workerPool, prometheusMetrics)

	// Start Ethereum client health check
	go monitorEthereumClient(ctx, client, telegramNotifier, workerPool, cfg.Monitoring.CheckInterval)

	// Wait for termination signal
	sig := <-sigCh
	logrus.WithField("signal", sig.String()).Info("Received termination signal")

	// Initiate graceful shutdown
	cancel()
	workerPool.Stop()
	prometheusMetrics.StopServer()

	logrus.Info("Application shutdown complete")
}

// monitorAddresses periodically checks the balance of all addresses
func monitorAddresses(ctx context.Context, cfg *config.Config, workerPool *worker.WorkerPool, prometheusMetrics *metrics.PrometheusMetrics) {
	addresses := cfg.GetAddresses()
	ticker := time.NewTicker(time.Duration(cfg.Monitoring.CheckInterval) * time.Second)
	defer ticker.Stop()

	logrus.WithFields(logrus.Fields{
		"address_count":  len(addresses),
		"check_interval": cfg.Monitoring.CheckInterval,
	}).Info("Starting address monitoring")

	// Initial check for all addresses
	checkAllAddresses(addresses, workerPool, prometheusMetrics)

	// Periodic checks
	for {
		select {
		case <-ctx.Done():
			logrus.Info("Stopping address monitoring")
			return
		case <-ticker.C:
			checkAllAddresses(addresses, workerPool, prometheusMetrics)
		}
	}
}

// monitorEthereumClient periodically checks the health of the Ethereum client
func monitorEthereumClient(ctx context.Context, client *ethclient.Client, telegramNotifier *notifier.TelegramNotifier, workerPool *worker.WorkerPool, checkInterval int) {
	ticker := time.NewTicker(time.Duration(checkInterval) * time.Second)
	defer ticker.Stop()

	var lastErrorTime time.Time
	var consecutiveErrors int

	logrus.Info("Starting Ethereum client health monitoring")

	for {
		select {
		case <-ctx.Done():
			logrus.Info("Stopping Ethereum client monitoring")
			return
		case <-ticker.C:
			// Check if we can get the latest block number
			_, err := client.BlockNumber(ctx)
			if err != nil {
				consecutiveErrors++
				logrus.WithError(err).Error("Ethereum client health check failed")

				// Pause the worker pool after 3 consecutive errors
				if consecutiveErrors >= 3 && workerPool != nil && !workerPool.IsPaused() {
					logrus.Warn("Pausing worker pool due to Ethereum node connection issues")
					workerPool.Pause()
				}

				// Only send notification if it's been at least 5 minutes since the last one
				// or if this is the first error after successful connection
				if telegramNotifier != nil && (time.Since(lastErrorTime) > 5*time.Minute || consecutiveErrors == 1) {
					lastErrorTime = time.Now()
					alert := notifier.Alert{
						Type:      notifier.AlertTypeNodeConnectionIssue,
						Address:   "",
						Message:   fmt.Sprintf("Ethereum node connection issue: %v (consecutive errors: %d)", err, consecutiveErrors),
						Timestamp: time.Now(),
					}
					telegramNotifier.SendAlert(alert)
				}
			} else {
				// If we had errors before but now it's working, send recovery notification
				if consecutiveErrors > 0 {
					// Resume the worker pool if it was paused
					if workerPool != nil && workerPool.IsPaused() {
						logrus.Info("Resuming worker pool after Ethereum node connection recovery")
						workerPool.Resume()
					}

					if telegramNotifier != nil {
						alert := notifier.Alert{
							Type:      notifier.AlertTypeNodeConnectionRecovered,
							Address:   "",
							Message:   fmt.Sprintf("Ethereum node connection recovered after %d consecutive errors", consecutiveErrors),
							Timestamp: time.Now(),
						}
						telegramNotifier.SendAlert(alert)
					}
				}
				consecutiveErrors = 0
			}
		}
	}
}

// checkAllAddresses enqueues jobs for all addresses
func checkAllAddresses(addresses []common.Address, workerPool *worker.WorkerPool, prometheusMetrics *metrics.PrometheusMetrics) {
	for _, address := range addresses {
		workerPool.EnqueueJob(worker.Job{
			Address: address,
		})

		// Update metrics with current balance
		balance := workerPool.GetBalance(address)
		balanceEth := new(big.Float).Quo(new(big.Float).SetInt(balance), big.NewFloat(params.Ether))
		balanceFloat, _ := balanceEth.Float64()
		prometheusMetrics.UpdateAddressBalance(address, balanceFloat)
	}

	logrus.WithField("address_count", len(addresses)).Info("Enqueued balance check jobs for all addresses")
}
