package inbox

import "time"

type Attachment struct {
	ID         string `json:"id"`
	MessageID  int64  `json:"message_id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	StorageKey string `json:"-"`
}

type Message struct {
	ID          int64        `json:"id"`
	BotID       string       `json:"bot_id"`
	PlatformID  string       `json:"platform_id"`
	ChatID      string       `json:"chat_id"`
	ChatType    string       `json:"chat_type"`
	SenderID    string       `json:"sender_id"`
	Type        string       `json:"type"`
	Content     string       `json:"content"`
	Quote       string       `json:"quote,omitempty"`
	ReceivedAt  string       `json:"received_at"`
	PlatformAt  string       `json:"platform_at,omitempty"`
	Status      string       `json:"status"`
	Error       string       `json:"error,omitempty"`
	Attachments []Attachment `json:"attachments"`
}

type Filter struct {
	BotID, Query, Type, SenderID, Status, From, To, Cursor string
	Limit                                                  int
	AllowedBots                                            []string
}

type Page struct {
	Messages   []Message `json:"messages"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

type Delivery struct {
	ID         string `json:"id"`
	MessageID  int64  `json:"message_id"`
	ConsumerID string `json:"consumer_id"`
	Status     string `json:"status"`
	Attempts   int    `json:"attempts"`
	Error      string `json:"error,omitempty"`
	NextAt     string `json:"next_at"`
	UpdatedAt  string `json:"updated_at"`
}

type ResultInput struct {
	Key     string `json:"idempotency_key"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
	Reply   bool   `json:"reply"`
}

type Result struct {
	ID         int64  `json:"id"`
	MessageID  int64  `json:"message_id"`
	ConsumerID string `json:"consumer_id"`
	ResultInput
	CreatedAt   string `json:"created_at"`
	ReplyStatus string `json:"reply_status,omitempty"`
	ReplyError  string `json:"reply_error,omitempty"`
}

type MediaSource struct {
	URL    string `json:"url,omitempty"`
	AESKey string `json:"aeskey,omitempty"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
}

type Incoming struct {
	Source    string        `json:"source,omitempty"`
	Message   Message       `json:"message"`
	Media     []MediaSource `json:"media"`
	RequestID string        `json:"request_id,omitempty"`
}

const timestampLayout = "2006-01-02T15:04:05.000000000Z"

func now() string { return time.Now().UTC().Format(timestampLayout) }

func contains(values []string, value string) bool {
	for _, s := range values {
		if s == value || s == "*" {
			return true
		}
	}
	return false
}

// Session 是机器人最近有消息往来、可主动发送的会话（来自企业微信 CLI 网关）。
type Session struct {
	ChatID      string `json:"chat_id"`
	ChatName    string `json:"chat_name"`
	ChatType    string `json:"chat_type"`
	LastMessage string `json:"last_msg_time"`
}
