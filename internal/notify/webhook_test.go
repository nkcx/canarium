package notify

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/engine"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recorder captures webhook deliveries.
type recorder struct {
	mu       sync.Mutex
	bodies   []string
	headers  []http.Header
	received chan struct{}
}

func newRecorder(expect int) *recorder {
	return &recorder{received: make(chan struct{}, expect)}
}

func (r *recorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)

		r.mu.Lock()
		r.bodies = append(r.bodies, string(body))
		r.headers = append(r.headers, req.Header.Clone())
		r.mu.Unlock()

		select {
		case r.received <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}
}

func (r *recorder) wait(t *testing.T, n int) {
	t.Helper()

	for i := 0; i < n; i++ {
		select {
		case <-r.received:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for delivery %d of %d", i+1, n)
		}
	}
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func TestEventIsDelivered(t *testing.T) {
	rec := newRecorder(1)
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()

	n := NewWebhookNotifier([]config.WebhookConfig{{URL: srv.URL}}, discardLogger())
	defer n.Close()

	n.HandleEvent(engine.Event{Type: "trigger", Timestamp: time.Now(), Data: "outage"})
	rec.wait(t, 1)

	var payload map[string]any
	if err := json.Unmarshal([]byte(rec.bodies[0]), &payload); err != nil {
		t.Fatalf("decoding payload: %v", err)
	}
	if payload["event"] != "trigger" {
		t.Errorf("event = %v, want trigger", payload["event"])
	}
	if payload["data"] != "outage" {
		t.Errorf("data = %v, want outage", payload["data"])
	}
	if payload["timestamp"] == nil {
		t.Error("payload carries no timestamp")
	}
}

func TestCustomHeadersAreSent(t *testing.T) {
	rec := newRecorder(1)
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()

	n := NewWebhookNotifier([]config.WebhookConfig{{
		URL:     srv.URL,
		Headers: map[string]string{"X-Auth": "token-value"},
	}}, discardLogger())
	defer n.Close()

	n.HandleEvent(engine.Event{Type: "trigger", Timestamp: time.Now()})
	rec.wait(t, 1)

	if got := rec.headers[0].Get("X-Auth"); got != "token-value" {
		t.Errorf("X-Auth = %q, want token-value", got)
	}
	if got := rec.headers[0].Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// TestEventFilterIsApplied: a webhook declaring events should receive only
// those, so a chat integration is not flooded with client_state_changed.
func TestEventFilterIsApplied(t *testing.T) {
	rec := newRecorder(2)
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()

	n := NewWebhookNotifier([]config.WebhookConfig{{
		URL:    srv.URL,
		Events: []string{"trigger", "abort"},
	}}, discardLogger())

	n.HandleEvent(engine.Event{Type: "trigger", Timestamp: time.Now()})
	n.HandleEvent(engine.Event{Type: "client_state_changed", Timestamp: time.Now()})
	n.HandleEvent(engine.Event{Type: "abort", Timestamp: time.Now()})

	rec.wait(t, 2)
	n.Close()

	if got := rec.count(); got != 2 {
		t.Errorf("delivered %d events, want 2 (the filtered one leaked through)", got)
	}
}

func TestEmptyFilterDeliversEverything(t *testing.T) {
	rec := newRecorder(3)
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()

	n := NewWebhookNotifier([]config.WebhookConfig{{URL: srv.URL}}, discardLogger())

	for _, typ := range []string{"trigger", "stage_start", "sequence_completed"} {
		n.HandleEvent(engine.Event{Type: typ, Timestamp: time.Now()})
	}

	rec.wait(t, 3)
	n.Close()

	if got := rec.count(); got != 3 {
		t.Errorf("delivered %d events, want 3", got)
	}
}

// TestCloseDrainsQueuedDeliveries: the last events of a sequence are the
// interesting ones, so shutdown must not drop them.
func TestCloseDrainsQueuedDeliveries(t *testing.T) {
	rec := newRecorder(20)
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()

	n := NewWebhookNotifier([]config.WebhookConfig{{URL: srv.URL}}, discardLogger())

	for i := 0; i < 20; i++ {
		n.HandleEvent(engine.Event{Type: "client_state_changed", Timestamp: time.Now()})
	}

	n.Close()

	if got := rec.count(); got != 20 {
		t.Errorf("delivered %d of 20 queued events; Close dropped the rest", got)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	n := NewWebhookNotifier([]config.WebhookConfig{{URL: srv.URL}}, discardLogger())

	n.Close()
	n.Close() // must not panic on a second close of the queue
}

// TestUnreachableEndpointDoesNotBlock: a webhook that is down must not stall
// the executor that emitted the event.
func TestUnreachableEndpointDoesNotBlock(t *testing.T) {
	n := NewWebhookNotifier([]config.WebhookConfig{
		{URL: "http://127.0.0.1:1/hook"},
	}, discardLogger())

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			n.HandleEvent(engine.Event{Type: "trigger", Timestamp: time.Now()})
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("HandleEvent blocked on an unreachable endpoint")
	}
	n.Close()
}

// TestSlowEndpointDoesNotBlockTheEmitter covers the executor's own goroutine:
// emitting an event must never wait on HTTP.
func TestSlowEndpointDoesNotBlockTheEmitter(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()

	n := NewWebhookNotifier([]config.WebhookConfig{{URL: srv.URL}}, discardLogger())

	start := time.Now()
	for i := 0; i < webhookWorkers*2; i++ {
		n.HandleEvent(engine.Event{Type: "trigger", Timestamp: time.Now()})
	}
	elapsed := time.Since(start)

	// Unblock the handler before Close, which waits for in-flight sends.
	close(release)
	n.Close()

	if elapsed > 2*time.Second {
		t.Errorf("HandleEvent took %v against a stalled endpoint; it must not block", elapsed)
	}
}

// TestQueueOverflowDropsRatherThanBlocking bounds memory against an endpoint
// that never drains.
func TestQueueOverflowDropsRatherThanBlocking(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()

	n := NewWebhookNotifier([]config.WebhookConfig{{URL: srv.URL}}, discardLogger())

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < webhookQueueDepth*3; i++ {
			n.HandleEvent(engine.Event{Type: "trigger", Timestamp: time.Now()})
		}
	}()

	var overflowed bool
	select {
	case <-done:
		overflowed = true
	case <-time.After(10 * time.Second):
	}

	// Unblock the handler before Close, which waits for in-flight sends.
	close(release)
	n.Close()

	if !overflowed {
		t.Fatal("HandleEvent blocked once the queue filled; it must drop instead")
	}
}

// TestWebhookURLIsNotLogged covers the credential leak: Slack and Discord
// webhook URLs carry their token in the path.
func TestWebhookURLIsNotLogged(t *testing.T) {
	var logged strings.Builder
	logger := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))

	const secret = "T00000000/B00000000/abcdefghijklmnopqrstuvwx"

	n := NewWebhookNotifier([]config.WebhookConfig{
		{URL: "http://127.0.0.1:1/services/" + secret},
	}, logger)

	n.HandleEvent(engine.Event{Type: "trigger", Timestamp: time.Now()})
	n.Close()

	if strings.Contains(logged.String(), secret) {
		t.Errorf("the webhook token was written to the log:\n%s", logged.String())
	}
	if !strings.Contains(logged.String(), "REDACTED") {
		t.Errorf("no redacted form was logged at all:\n%s", logged.String())
	}
}

func TestNonSuccessStatusIsLoggedNotFatal(t *testing.T) {
	rec := newRecorder(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.handler()(w, r)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	n := NewWebhookNotifier([]config.WebhookConfig{{URL: srv.URL}}, discardLogger())
	defer n.Close()

	n.HandleEvent(engine.Event{Type: "trigger", Timestamp: time.Now()})
	rec.wait(t, 1)
	// Reaching here without a panic or hang is the assertion.
}
