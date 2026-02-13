package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Message struct {
	Title   string                 `json:"title"`
	Body    string                 `json:"body"`
	Context map[string]interface{} `json:"context,omitempty"`
}

type Notifier interface {
	Notify(ctx context.Context, message Message) error
}

type NoopNotifier struct{}

func (n NoopNotifier) Notify(_ context.Context, _ Message) error {
	return nil
}

type WebhookNotifier struct {
	url    string
	client *http.Client
}

func NewWebhookNotifier(webhookURL string) (*WebhookNotifier, error) {
	trimmed := strings.TrimSpace(webhookURL)
	if trimmed == "" {
		return nil, fmt.Errorf("webhook url is required")
	}
	return &WebhookNotifier{
		url: trimmed,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}, nil
}

func (w *WebhookNotifier) Notify(ctx context.Context, message Message) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal webhook message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("send webhook notification: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", res.StatusCode)
	}

	return nil
}

type MultiNotifier struct {
	notifiers []Notifier
}

func NewMultiNotifier(items ...Notifier) *MultiNotifier {
	filtered := make([]Notifier, 0, len(items))
	for _, item := range items {
		if item != nil {
			filtered = append(filtered, item)
		}
	}
	return &MultiNotifier{notifiers: filtered}
}

func (m *MultiNotifier) Notify(ctx context.Context, message Message) error {
	for _, notifier := range m.notifiers {
		if err := notifier.Notify(ctx, message); err != nil {
			return err
		}
	}
	return nil
}
