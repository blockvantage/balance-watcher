# Ethereum Balance Watcher & Auto Top-Up Service

A service that monitors Ethereum addresses and automatically tops up their balances when they fall below a configured threshold.

## Features

- ETH balance monitoring with configurable thresholds
- Automatic top-up of addresses with low balances
- Adaptive top-up logic based on refill history
- Telegram alerts for critical events
- Prometheus metrics for observability
- SQLite database for historical tracking
- Configurable via environment variables or config file

## Getting Started

### Prerequisites

- Go 1.21 or higher
- Ethereum node access (Infura, Alchemy, or your own node)
- Telegram bot token (for alerts)

### Installation

1. Clone the repository
2. Configure the application (see Configuration section)
3. Build and run the application

```bash
go build -o balance-watcher
./balance-watcher
```

## Configuration

Copy the `config.yaml.sample` to `config.yaml` or create a `config.yaml` file in the project root or set environment variables:

```yaml
ethereum:
  node_url: "https://mainnet.infura.io/v3/YOUR_API_KEY"
  private_key: "YOUR_PRIVATE_KEY" # For the funding account, without the 0x prefix

monitoring:
  check_interval: 300 # seconds
  balance_threshold: "0.1" # ETH
  refill_amount: "0.5" # ETH
  max_refill_amount: "2.0" # ETH
  refill_threshold_count: 3 # Number of refills before increasing amount
  refill_threshold_window: 86400 # 24 hours in seconds

workers:
  pool_size: 10

telegram:
  enabled: false # Set to true to enable Telegram notifications
  bot_token: "YOUR_BOT_TOKEN"
  chat_id: "YOUR_CHAT_ID"

database:
  path: "./history.db"

addresses:
  - "0x123..."
  - "0x456..."
  # Add more addresses as needed
```

## Architecture

The application follows a simple worker pool pattern with the following components:

- Main Service: Coordinates all components
- Worker Pool: Handles balance checking and top-ups concurrently
- History Logger: Records all transactions and events in SQLite
- Notifier: Sends alerts via Telegram
- Metrics Exporter: Exposes Prometheus metrics

## License

MIT
