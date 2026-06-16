// Package notification handles webhook-based notifications (Telegram, Discord, Slack).
package notification

import "time"

// NotificationType enum for supported channels.
const (
	TypeTelegram = "telegram"
	TypeDiscord  = "discord"
	TypeSlack    = "slack"
)

// Notification represents a notification channel target (just a destination config).
type Notification struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	NotifType string    `json:"notif_type"` // telegram, discord, slack
	ConfigJSON string   `json:"config_json"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TelegramConfig holds Telegram bot config.
type TelegramConfig struct {
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
}

// DiscordConfig holds Discord webhook config.
type DiscordConfig struct {
	WebhookURL string `json:"webhook_url"`
}

// SlackConfig holds Slack webhook config.
type SlackConfig struct {
	WebhookURL string `json:"webhook_url"`
}

// NotificationRepository defines persistence contract for notifications.
type NotificationRepository interface {
	List() ([]Notification, error)
	GetByID(id string) (*Notification, error)
	Create(n *Notification) error
	Update(n *Notification) error
	Delete(id string) error
}
