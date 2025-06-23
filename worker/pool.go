package worker

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/blockvantage/balance-watcher/database"
	"github.com/blockvantage/balance-watcher/metrics"
	"github.com/blockvantage/balance-watcher/notifier"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/params"
	"github.com/sirupsen/logrus"
)

// Job represents a job to be processed by the worker pool
type Job struct {
	Address common.Address
}

// WorkerPool manages a pool of workers for processing jobs
type WorkerPool struct {
	client            *ethclient.Client
	jobQueue          chan Job
	db                *database.Database
	notifier          *notifier.TelegramNotifier
	metrics           *metrics.PrometheusMetrics
	balanceThreshold  *big.Int
	refillAmount      *big.Int
	maxRefillAmount   *big.Int
	refillThreshold   int
	refillWindow      int
	wg                sync.WaitGroup
	senderPrivateKey  string
	balancesByAddress map[common.Address]*big.Int
	mu                sync.RWMutex
	// Pause mechanism
	paused            bool
	pauseMu           sync.RWMutex
	// Nonce management
	currentNonce      uint64
	lastNonceUpdate   time.Time
	nonceMu           sync.Mutex
	senderAddress     common.Address
	// Pending top-ups tracking
	pendingTopUps     map[common.Address]time.Time
	pendingTopUpsMu   sync.RWMutex
}

// NewWorkerPool creates a new worker pool
func NewWorkerPool(
	client *ethclient.Client,
	db *database.Database,
	notifier *notifier.TelegramNotifier,
	metrics *metrics.PrometheusMetrics,
	balanceThreshold *big.Int,
	refillAmount *big.Int,
	maxRefillAmount *big.Int,
	refillThreshold int,
	refillWindow int,
	poolSize int,
	senderPrivateKey string,
) *WorkerPool {
	return &WorkerPool{
		client:            client,
		jobQueue:          make(chan Job, poolSize*2), // Buffer size is twice the number of workers
		db:                db,
		notifier:          notifier,
		metrics:           metrics,
		balanceThreshold:  balanceThreshold,
		refillAmount:      refillAmount,
		maxRefillAmount:   maxRefillAmount,
		refillThreshold:   refillThreshold,
		refillWindow:      refillWindow,
		senderPrivateKey:  senderPrivateKey,
		balancesByAddress: make(map[common.Address]*big.Int),
		pendingTopUps:     make(map[common.Address]time.Time),
	}
}

// Start starts the worker pool
func (wp *WorkerPool) Start(ctx context.Context, poolSize int) {
	// Initialize sender address for nonce tracking
	if wp.senderPrivateKey != "" {
		privateKey, err := crypto.HexToECDSA(wp.senderPrivateKey)
		if err == nil {
			publicKey := privateKey.Public()
			if publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey); ok {
				wp.senderAddress = crypto.PubkeyToAddress(*publicKeyECDSA)
				// Initialize nonce
				wp.updateNonceFromNetwork(ctx)
			}
		}
	}

	// Start nonce updater goroutine
	go wp.nonceUpdater(ctx)

	// Start pending top-ups cleaner goroutine
	go wp.pendingTopUpsCleaner(ctx)

	// Start workers
	for i := 0; i < poolSize; i++ {
		wp.wg.Add(1)
		go wp.worker(ctx, i)
	}

	logrus.WithField("pool_size", poolSize).Info("Worker pool started")
}

// Stop stops the worker pool
func (wp *WorkerPool) Stop() {
	close(wp.jobQueue)
	wp.wg.Wait()
	logrus.Info("Worker pool stopped")
}

// EnqueueJob adds a job to the queue
func (wp *WorkerPool) EnqueueJob(job Job) {
	// Check if the pool is paused before enqueueing
	wp.pauseMu.RLock()
	paused := wp.paused
	wp.pauseMu.RUnlock()
	
	if paused {
		logrus.WithField("address", job.Address.Hex()).Info("Job not enqueued because worker pool is paused")
		return
	}
	
	wp.jobQueue <- job
	// Update queue size metric
	if wp.metrics != nil {
		wp.metrics.UpdateQueueSize(len(wp.jobQueue))
	}
}

// GetBalance returns the cached balance for an address
func (wp *WorkerPool) GetBalance(address common.Address) *big.Int {
	wp.mu.RLock()
	defer wp.mu.RUnlock()
	if balance, ok := wp.balancesByAddress[address]; ok {
		return balance
	}
	return big.NewInt(0)
}

// Pause pauses the worker pool, preventing new jobs from being processed
func (wp *WorkerPool) Pause() {
	wp.pauseMu.Lock()
	defer wp.pauseMu.Unlock()
	
	if !wp.paused {
		wp.paused = true
		logrus.Info("Worker pool paused due to Ethereum node connection issues")
	}
}

// Resume resumes the worker pool, allowing jobs to be processed again
func (wp *WorkerPool) Resume() {
	wp.pauseMu.Lock()
	defer wp.pauseMu.Unlock()
	
	if wp.paused {
		wp.paused = false
		logrus.Info("Worker pool resumed after Ethereum node connection recovery")
	}
}

// IsPaused returns whether the worker pool is currently paused
func (wp *WorkerPool) IsPaused() bool {
	wp.pauseMu.RLock()
	defer wp.pauseMu.RUnlock()
	return wp.paused
}

// worker processes jobs from the queue
func (wp *WorkerPool) worker(ctx context.Context, id int) {
	defer wp.wg.Done()

	logger := logrus.WithField("worker_id", id)
	logger.Info("Worker started")

	for {
		select {
		case <-ctx.Done():
			logger.Info("Worker stopped due to context cancellation")
			return
		case job, ok := <-wp.jobQueue:
			if !ok {
				logger.Info("Worker stopped due to closed job queue")
				return
			}
			
			// Check if the pool is paused before processing
			wp.pauseMu.RLock()
			paused := wp.paused
			wp.pauseMu.RUnlock()
			
			if paused {
				logger.WithField("address", job.Address.Hex()).Info("Job skipped because worker pool is paused")
				// Re-enqueue the job for later processing
				go func() {
					time.Sleep(5 * time.Second)
					wp.EnqueueJob(job)
				}()
				continue
			}

			logger.WithField("address", job.Address.Hex()).Info("Processing job")
			wp.processJob(ctx, job, logger)
		}
	}
}

// processJob processes a single job
func (wp *WorkerPool) processJob(ctx context.Context, job Job, logger *logrus.Entry) {
	// Check balance
	balance, err := wp.client.BalanceAt(ctx, job.Address, nil)
	if err != nil {
		logger.WithError(err).Error("Failed to get balance")
		wp.db.LogError(job.Address, fmt.Sprintf("Failed to get balance: %v", err))
		// Update error metrics
		if wp.metrics != nil {
			wp.metrics.IncrementErrorCount(job.Address)
		}
		if wp.notifier != nil {
			wp.notifier.SendAlert(notifier.Alert{
				Type:      notifier.AlertTypeBalanceQueryFailure,
				Address:   job.Address.Hex(),
				Message:   fmt.Sprintf("Failed to get balance: %v", err),
				Timestamp: time.Now(),
			})
		}
		return
	}

	// Update cached balance
	wp.mu.Lock()
	wp.balancesByAddress[job.Address] = balance
	wp.mu.Unlock()

	// Convert balance to ETH for logging
	balanceEth := new(big.Float).Quo(new(big.Float).SetInt(balance), big.NewFloat(params.Ether))
	logger.WithFields(logrus.Fields{
		"address": job.Address.Hex(),
		"balance": balanceEth.String() + " ETH",
	}).Info("Balance checked")

	// Check if balance is below threshold
	if balance.Cmp(wp.balanceThreshold) < 0 {
		logger.WithFields(logrus.Fields{
			"address":   job.Address.Hex(),
			"balance":   balanceEth.String() + " ETH",
			"threshold": new(big.Float).Quo(new(big.Float).SetInt(wp.balanceThreshold), big.NewFloat(params.Ether)).String() + " ETH",
		}).Warn("Balance below threshold")

		// Check if there's already a pending top-up for this address
		wp.pendingTopUpsMu.RLock()
		pendingTime, isPending := wp.pendingTopUps[job.Address]
		wp.pendingTopUpsMu.RUnlock()

		// If there's a pending top-up that's less than 5 minutes old, skip this one
		if isPending && time.Since(pendingTime) < 5*time.Minute {
			logger.WithFields(logrus.Fields{
				"address":     job.Address.Hex(),
				"pendingSince": pendingTime.Format(time.RFC3339),
			}).Info("Skipping top-up because there's already a pending transaction for this address")
			return
		}

		// Check refill history to determine if we need to increase the refill amount
		refillCount, err := wp.db.GetRefillsInTimeWindow(job.Address, wp.refillWindow)
		if err != nil {
			logger.WithError(err).Error("Failed to get refill history")
		}

		// Calculate refill amount based on history
		refillAmount := wp.refillAmount
		if refillCount >= wp.refillThreshold {
			// Double the refill amount if we've refilled too many times recently
			refillAmount = new(big.Int).Mul(refillAmount, big.NewInt(2))
			if refillAmount.Cmp(wp.maxRefillAmount) > 0 {
				refillAmount = wp.maxRefillAmount
			}

			if wp.notifier != nil {
				wp.notifier.SendAlert(notifier.Alert{
					Type:      notifier.AlertTypeRefillLoop,
					Address:   job.Address.Hex(),
					Message:   fmt.Sprintf("Refill loop detected (%d refills in window). Increasing refill amount.", refillCount),
					Timestamp: time.Now(),
				})
			}
		}

		// Mark this address as having a pending top-up
		wp.pendingTopUpsMu.Lock()
		wp.pendingTopUps[job.Address] = time.Now()
		wp.pendingTopUpsMu.Unlock()

		// Convert refill amount to ETH for logging
		refillAmountEth := new(big.Float).Quo(new(big.Float).SetInt(refillAmount), big.NewFloat(params.Ether))
		logger.WithFields(logrus.Fields{
			"address":      job.Address.Hex(),
			"refillAmount": refillAmountEth.String() + " ETH",
		}).Info("Topping up address")

		// Top up the address
		txHash, err := wp.topUpAddress(ctx, job.Address, refillAmount)
		if err != nil {
			logger.WithError(err).Error("Failed to top up address")
			wp.db.LogRefill(job.Address, refillAmountEth.String(), "", false, err.Error())
			// Update error metrics
			if wp.metrics != nil {
				wp.metrics.IncrementErrorCount(job.Address)
			}
			if wp.notifier != nil {
				wp.notifier.SendAlert(notifier.Alert{
					Type:      notifier.AlertTypeTopUpFailed,
					Address:   job.Address.Hex(),
					Message:   fmt.Sprintf("Failed to top up address: %v", err),
					Timestamp: time.Now(),
				})
			}
			// Remove the pending flag if the transaction failed
			wp.pendingTopUpsMu.Lock()
			delete(wp.pendingTopUps, job.Address)
			wp.pendingTopUpsMu.Unlock()
			return
		}

		// Log successful refill
		wp.db.LogRefill(job.Address, refillAmountEth.String(), txHash, true, "")
		// Update refill metrics
		if wp.metrics != nil {
			wp.metrics.IncrementRefillCount(job.Address)
		}
		if wp.notifier != nil {
			wp.notifier.SendAlert(notifier.Alert{
				Type:      notifier.AlertTypeSuccessfulRefill,
				Address:   job.Address.Hex(),
				Message:   fmt.Sprintf("Successfully topped up with %s ETH (tx: %s)", refillAmountEth.String(), txHash),
				Timestamp: time.Now(),
			})
		}
		
		// Keep the pending flag for 5 minutes to prevent duplicate top-ups
		// It will be automatically cleaned up by the cleanup routine
	}
}

// updateNonceFromNetwork gets the current nonce from the network
func (wp *WorkerPool) updateNonceFromNetwork(ctx context.Context) error {
	if wp.senderAddress == (common.Address{}) {
		// Initialize sender address if not done yet
		privateKey, err := crypto.HexToECDSA(wp.senderPrivateKey)
		if err != nil {
			return fmt.Errorf("invalid private key: %w", err)
		}
		publicKey := privateKey.Public()
		publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("error casting public key to ECDSA")
		}
		wp.senderAddress = crypto.PubkeyToAddress(*publicKeyECDSA)
	}

	nonce, err := wp.client.PendingNonceAt(ctx, wp.senderAddress)
	if err != nil {
		return fmt.Errorf("failed to get nonce: %w", err)
	}

	wp.currentNonce = nonce
	wp.lastNonceUpdate = time.Now()
	logrus.WithFields(logrus.Fields{
		"sender": wp.senderAddress.Hex(),
		"nonce":  nonce,
	}).Debug("Updated nonce from network")

	return nil
}

// nonceUpdater periodically updates the nonce from the network
func (wp *WorkerPool) nonceUpdater(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			wp.nonceMu.Lock()
			err := wp.updateNonceFromNetwork(ctx)
			wp.nonceMu.Unlock()
			if err != nil {
				logrus.WithError(err).Warn("Failed to update nonce from network")
			}
		}
	}
}

// pendingTopUpsCleaner periodically cleans up old pending top-up entries
func (wp *WorkerPool) pendingTopUpsCleaner(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			wp.pendingTopUpsMu.Lock()
			
			// Remove entries older than 5 minutes
			cutoff := time.Now().Add(-5 * time.Minute)
			for addr, timestamp := range wp.pendingTopUps {
				if timestamp.Before(cutoff) {
					delete(wp.pendingTopUps, addr)
					logrus.WithField("address", addr.Hex()).Debug("Removed expired pending top-up entry")
				}
			}
			
			wp.pendingTopUpsMu.Unlock()
		}
	}
}

// topUpAddress sends ETH to the specified address
func (wp *WorkerPool) topUpAddress(ctx context.Context, address common.Address, amount *big.Int) (string, error) {
	privateKey, err := crypto.HexToECDSA(wp.senderPrivateKey)
	if err != nil {
		return "", fmt.Errorf("invalid private key: %w", err)
	}

	// We only need to validate the private key, but don't need the public key
	// since we're using the cached sender address
	_, ok := privateKey.Public().(*ecdsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("error with private key format")
	}
	
	// Use mutex to ensure only one goroutine gets a nonce at a time
	wp.nonceMu.Lock()
	defer wp.nonceMu.Unlock()
	
	// If we don't have a current nonce or it's been too long since our last update, get it from the network
	if wp.currentNonce == 0 || time.Since(wp.lastNonceUpdate) > 30*time.Second {
		if err := wp.updateNonceFromNetwork(ctx); err != nil {
			return "", fmt.Errorf("failed to update nonce: %w", err)
		}
	}
	
	// Use the current nonce and increment it for the next transaction
	nonce := wp.currentNonce
	wp.currentNonce++

	gasPrice, err := wp.client.SuggestGasPrice(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get gas price: %w", err)
	}

	gasLimit := uint64(21000) // Standard gas limit for ETH transfers
	tx := types.NewTransaction(nonce, address, amount, gasLimit, gasPrice, nil)

	chainID, err := wp.client.NetworkID(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get chain ID: %w", err)
	}

	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(chainID), privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign transaction: %w", err)
	}

	err = wp.client.SendTransaction(ctx, signedTx)
	if err != nil {
		// If transaction fails, reset the nonce on the next attempt
		// This will force a refresh from the network
		wp.lastNonceUpdate = time.Time{}
		return "", fmt.Errorf("failed to send transaction: %w", err)
	}

	txHash := signedTx.Hash().Hex()
	return txHash, nil
}
