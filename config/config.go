package config

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// Config holds all configuration for the application
type Config struct {
	Ethereum   EthereumConfig   `mapstructure:"ethereum"`
	Monitoring MonitoringConfig `mapstructure:"monitoring"`
	Workers    WorkersConfig    `mapstructure:"workers"`
	Telegram   TelegramConfig   `mapstructure:"telegram"`
	Database   DatabaseConfig   `mapstructure:"database"`
	Addresses  []string         `mapstructure:"addresses"`
}

// EthereumConfig holds Ethereum-related configuration
type EthereumConfig struct {
	NodeURL    string `mapstructure:"node_url"`
	PrivateKey string `mapstructure:"private_key"`
}

// MonitoringConfig holds monitoring-related configuration
type MonitoringConfig struct {
	PrometheusPort        string `mapstructure:"prometheus_port"`         // Prometheus metrics port
	CheckInterval         int    `mapstructure:"check_interval"`          // in seconds
	BalanceThreshold      string `mapstructure:"balance_threshold"`       // in ETH
	RefillAmount          string `mapstructure:"refill_amount"`           // in ETH
	MaxRefillAmount       string `mapstructure:"max_refill_amount"`       // in ETH
	RefillThresholdCount  int    `mapstructure:"refill_threshold_count"`  // number of refills before increasing amount
	RefillThresholdWindow int    `mapstructure:"refill_threshold_window"` // in seconds
}

// WorkersConfig holds worker pool configuration
type WorkersConfig struct {
	PoolSize int `mapstructure:"pool_size"`
}

// TelegramConfig holds Telegram notification configuration
type TelegramConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	BotToken string `mapstructure:"bot_token"`
	ChatID   string `mapstructure:"chat_id"`
}

// DatabaseConfig holds database configuration
type DatabaseConfig struct {
	Path string `mapstructure:"path"`
}

// LoadConfig loads the configuration from file and environment variables
func LoadConfig() (*Config, error) {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("error reading config file: %w", err)
		}
		logrus.Warn("No config file found, using environment variables")
	}

	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("unable to decode config: %w", err)
	}
	if config.Monitoring.PrometheusPort == "" {
		config.Monitoring.PrometheusPort = "9090"
	}

	return &config, nil
}

// GetBalanceThresholdWei returns the balance threshold in wei
func (c *MonitoringConfig) GetBalanceThresholdWei() *big.Int {
	threshold, _ := new(big.Float).SetString(c.BalanceThreshold)
	thresholdWei := new(big.Float).Mul(threshold, big.NewFloat(params.Ether))
	thresholdWeiInt, _ := thresholdWei.Int(nil)
	return thresholdWeiInt
}

// GetRefillAmountWei returns the refill amount in wei
func (c *MonitoringConfig) GetRefillAmountWei() *big.Int {

	if c.RefillAmount == "" {
		logrus.Warn("Refill amount is empty in config, using default of 1.0 ETH")
		// Default to 1 ETH if not specified
		return big.NewInt(params.Ether)
	}

	refill, ok := new(big.Float).SetString(c.RefillAmount)
	if !ok {
		logrus.WithField("invalid_value", c.RefillAmount).Warn("Invalid refill amount in config, using default of 1.0 ETH")
		return big.NewInt(params.Ether)
	}

	refillWei := new(big.Float).Mul(refill, big.NewFloat(params.Ether))
	refillWeiInt, _ := refillWei.Int(nil)

	logrus.WithFields(logrus.Fields{
		"refill_eth": refill.String(),
		"refill_wei": refillWeiInt.String(),
	}).Info("Calculated refill amount")

	return refillWeiInt
}

// GetMaxRefillAmountWei returns the maximum refill amount in wei
func (c *MonitoringConfig) GetMaxRefillAmountWei() *big.Int {
	maxRefill, _ := new(big.Float).SetString(c.MaxRefillAmount)
	maxRefillWei := new(big.Float).Mul(maxRefill, big.NewFloat(params.Ether))
	maxRefillWeiInt, _ := maxRefillWei.Int(nil)
	return maxRefillWeiInt
}

// GetAddresses returns the list of addresses as common.Address
func (c *Config) GetAddresses() []common.Address {
	addresses := make([]common.Address, len(c.Addresses))
	for i, addr := range c.Addresses {
		addresses[i] = common.HexToAddress(addr)
	}
	return addresses
}
