package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/netutil"
)

const (
	// webhookTimeout bounds a single delivery attempt.
	webhookTimeout = 10 * time.Second

	// webhookQueueDepth is how many pending notifications are buffered
	// before new ones are dropped.
	webhookQueueDepth = 256

	// webhookWorkers is how many deliveries run concurrently.
	webhookWorkers = 4
)

type delivery struct {
	webhook config.WebhookConfig
	event   engine.Event
}

type WebhookNotifier struct {
	webhooks   []config.WebhookConfig
	httpClient *http.Client
	logger     *slog.Logger

	queue     chan delivery
	wg        sync.WaitGroup
	closeOnce sync.Once
}

func NewWebhookNotifier(webhooks []config.WebhookConfig, logger *slog.Logger) *WebhookNotifier {
	n := &WebhookNotifier{
		webhooks:   webhooks,
		httpClient: &http.Client{Timeout: webhookTimeout},
		logger:     logger,
		queue:      make(chan delivery, webhookQueueDepth),
	}

	n.wg.Add(webhookWorkers)
	for i := 0; i < webhookWorkers; i++ {
		go n.worker()
	}

	return n
}

// HandleEvent delivers an event to every matching webhook.
//
// Delivery happens on a bounded worker pool rather than one goroutine per
// event per webhook. A staged wake emits a burst of client_state_changed
// events, and the previous unbounded spawn could put hundreds of concurrent
// HTTP requests against an endpoint that is itself struggling.
func (n *WebhookNotifier) HandleEvent(evt engine.Event) {
	for _, wh := range n.webhooks {
		if len(wh.Events) > 0 && !slices.Contains(wh.Events, evt.Type) {
			continue
		}

		select {
		case n.queue <- delivery{webhook: wh, event: evt}:
		default:
			n.logger.Warn("webhook queue is full; dropping notification",
				"url", netutil.RedactEndpoint(wh.URL), "event", evt.Type)
		}
	}
}

// Close stops the delivery workers and waits for in-flight sends.
func (n *WebhookNotifier) Close() {
	n.closeOnce.Do(func() {
		close(n.queue)
		n.wg.Wait()
	})
}

func (n *WebhookNotifier) worker() {
	defer n.wg.Done()
	for d := range n.queue {
		ctx, cancel := context.WithTimeout(context.Background(), webhookTimeout)
		n.send(ctx, d.webhook, d.event)
		cancel()
	}
}

func (n *WebhookNotifier) send(ctx context.Context, wh config.WebhookConfig, evt engine.Event) {
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

	// Every log line uses the redacted form. Slack, Discord and Teams
	// webhook URLs carry their authentication token in the path, so logging
	// the raw URL on each failure published the credential to the journal —
	// and anyone holding it can post as your alerting integration.
	safeURL := netutil.RedactEndpoint(wh.URL)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.URL, bytes.NewReader(body))
	if err != nil {
		n.logger.Error("creating webhook request", "error", err, "url", safeURL)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Canarium/1.0")

	for k, v := range wh.Headers {
		req.Header.Set(k, v)
	}

	resp, err := n.httpClient.Do(req)
	if err != nil {
		n.logger.Error("sending webhook",
			"error", netutil.RedactSecrets(err.Error(), wh.URL), "url", safeURL)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 300 {
		n.logger.Warn("webhook returned a non-success status",
			"url", safeURL, "status", resp.StatusCode)
	}
}
