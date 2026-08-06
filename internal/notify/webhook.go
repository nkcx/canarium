package notify

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/engine"
)

type WebhookNotifier struct {
	webhooks   []config.WebhookConfig
	httpClient *http.Client
	logger     *slog.Logger
}

func NewWebhookNotifier(webhooks []config.WebhookConfig, logger *slog.Logger) *WebhookNotifier {
	return &WebhookNotifier{
		webhooks: webhooks,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger: logger,
	}
}

func (n *WebhookNotifier) HandleEvent(evt engine.Event) {
	for _, wh := range n.webhooks {
		if len(wh.Events) > 0 && !contains(wh.Events, evt.Type) {
			continue
		}
		go n.send(wh, evt)
	}
}

func (n *WebhookNotifier) send(wh config.WebhookConfig, evt engine.Event) {
	payload := map[string]any{
		"event":     evt.Type,
		"timestamp": evt.Timestamp.Format(time.RFC3339),
		"data":      evt.Data,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		n.logger.Error("marshaling webhook payload", "error", err)
		return
	}

	req, err := http.NewRequest("POST", wh.URL, bytes.NewReader(body))
	if err != nil {
		n.logger.Error("creating webhook request", "error", err, "url", wh.URL)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Canarium/1.0")

	for k, v := range wh.Headers {
		req.Header.Set(k, v)
	}

	resp, err := n.httpClient.Do(req)
	if err != nil {
		n.logger.Error("sending webhook", "error", err, "url", wh.URL)
		return
	}
	resp.Body.Close()

	if resp.StatusCode >= 300 {
		n.logger.Warn("webhook non-success", "url", wh.URL, "status", resp.StatusCode)
	}
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
