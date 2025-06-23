package notifier

import (
	"fmt"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/sirupsen/logrus"
)

// AlertType represents the type of alert
type AlertType int

const (
	// AlertTypeBalanceQueryFailure represents a balance query failure
	AlertTypeBalanceQueryFailure AlertType = iota
	// AlertTypeTopUpFailed represents a top-up failure
	AlertTypeTopUpFailed
	// AlertTypeUnconfirmedTx represents an unconfirmed transaction
	AlertTypeUnconfirmedTx
	// AlertTypeRefillLoop represents a refill loop trigger
	AlertTypeRefillLoop
	// AlertTypeSuccessfulRefill represents a successful refill
	AlertTypeSuccessfulRefill
	// AlertTypeNodeConnectionIssue represents an Ethereum node connection issue
	AlertTypeNodeConnectionIssue
	// AlertTypeNodeConnectionRecovered represents recovery from an Ethereum node connection issue
	AlertTypeNodeConnectionRecovered
)

// Alert represents a notification alert
type Alert struct {
	Type      AlertType
	Address   string
	Message   string
	Timestamp time.Time
}

// TelegramNotifier handles sending notifications via Telegram
type TelegramNotifier struct {
	bot            *tgbotapi.BotAPI
	chatID         int64
	sentAlerts     map[string]time.Time
	alertRateLimit time.Duration
	mu             sync.Mutex
}

// NewTelegramNotifier creates a new Telegram notifier
func NewTelegramNotifier(botToken string, chatID string) (*TelegramNotifier, error) {
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create Telegram bot: %w", err)
	}

	var chatIDInt int64
	_, err = fmt.Sscanf(chatID, "%d", &chatIDInt)
	if err != nil {
		return nil, fmt.Errorf("invalid chat ID: %w", err)
	}

	return &TelegramNotifier{
		bot:            bot,
		chatID:         chatIDInt,
		sentAlerts:     make(map[string]time.Time),
		alertRateLimit: 5 * time.Minute,
	}, nil
}

// SendAlert sends an alert via Telegram
func (t *TelegramNotifier) SendAlert(alert Alert) error {
	// Don't send alerts for successful refills
	if alert.Type == AlertTypeSuccessfulRefill {
		logrus.WithFields(logrus.Fields{
			"address": alert.Address,
			"message": alert.Message,
		}).Info("Successful refill (not sending alert)")
		return nil
	}

	// Check if we've sent this alert recently
	alertKey := fmt.Sprintf("%d:%s", alert.Type, alert.Address)
	t.mu.Lock()
	lastSent, exists := t.sentAlerts[alertKey]
	if exists && time.Since(lastSent) < t.alertRateLimit {
		t.mu.Unlock()
		logrus.WithFields(logrus.Fields{
			"address": alert.Address,
			"type":    alert.Type,
		}).Info("Skipping alert due to rate limiting")
		return nil
	}
	t.sentAlerts[alertKey] = time.Now()
	t.mu.Unlock()

	// Format the message based on alert type
	var emoji string
	switch alert.Type {
	case AlertTypeBalanceQueryFailure:
		emoji = "⚠️"
	case AlertTypeTopUpFailed:
		emoji = "❌"
	case AlertTypeUnconfirmedTx:
		emoji = "⏱"
	case AlertTypeRefillLoop:
		emoji = "🔁"
	case AlertTypeNodeConnectionIssue:
		emoji = "🔌"
	case AlertTypeNodeConnectionRecovered:
		emoji = "🟢"
	default:
		emoji = "ℹ️"
	}

	message := fmt.Sprintf("%s *Alert*: %s\n\n*Address*: `%s`\n*Time*: %s",
		emoji, alert.Message, alert.Address, alert.Timestamp.Format(time.RFC3339))

	msg := tgbotapi.NewMessage(t.chatID, message)
	msg.ParseMode = tgbotapi.ModeMarkdown

	_, err := t.bot.Send(msg)
	if err != nil {
		return fmt.Errorf("failed to send Telegram message: %w", err)
	}

	logrus.WithFields(logrus.Fields{
		"address": alert.Address,
		"type":    alert.Type,
	}).Info("Alert sent successfully")

	return nil
}
