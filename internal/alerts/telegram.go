package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// TelegramNotifier sends an alert as a plain Telegram message via a bot —
// see https://core.telegram.org/bots/api#sendmessage. Set up once (create
// a bot with @BotFather, add it to a chat, find the chat's numeric ID),
// works from anywhere the bot's host has internet access, no port
// forwarding or paired device needed — see docs/alerts.md.
type TelegramNotifier struct {
	BotToken string
	ChatID   string
	// baseURL overrides the real Telegram API for tests; empty uses it.
	baseURL string
}

func (t *TelegramNotifier) apiBase() string {
	if t.baseURL != "" {
		return t.baseURL
	}
	return "https://api.telegram.org"
}

func (t *TelegramNotifier) Notify(ctx context.Context, alert Alert) error {
	body, err := json.Marshal(map[string]string{
		"chat_id": t.ChatID,
		"text":    formatPlainText(alert),
	})
	if err != nil {
		return fmt.Errorf("encode telegram message: %w", err)
	}
	url := fmt.Sprintf("%s/bot%s/sendMessage", t.apiBase(), t.BotToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("send telegram message: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram API returned HTTP %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

func formatPlainText(alert Alert) string {
	return fmt.Sprintf("[%s] %s\n\n%s", alert.Severity, alert.Title, alert.Body)
}
