package rest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/netutil"
)

type Transport struct {
	httpClient *http.Client
}

func New() *Transport {
	return &Transport{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *Transport) Name() string { return "rest" }

func (t *Transport) Capabilities() []engine.Capability {
	return []engine.Capability{
		{Action: engine.ActionShutdown, Idempotent: false, Timeout: 30 * time.Second},
		{Action: engine.ActionWake, Idempotent: false, Timeout: 30 * time.Second},
		{Action: engine.ActionProbe, Idempotent: true, Timeout: 10 * time.Second},
	}
}

func (t *Transport) Execute(ctx context.Context, client *engine.Client, action engine.ActionType) (*engine.ActionResult, error) {
	prefix := action.String()
	url := getConfigString(client.TransportConfig, prefix+"_url")
	if url == "" {
		return nil, fmt.Errorf("rest transport: no %s_url configured", prefix)
	}

	method := strings.ToUpper(getConfigString(client.TransportConfig, prefix+"_method"))
	if method == "" {
		method = "POST"
	}

	url = expandVars(url, client)

	var body io.Reader
	if payload := getConfigString(client.TransportConfig, prefix+"_body"); payload != "" {
		body = strings.NewReader(expandVars(payload, client))
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	if headers, ok := client.TransportConfig[prefix+"_headers"].(map[string]any); ok {
		for k, v := range headers {
			req.Header.Set(k, fmt.Sprint(v))
		}
	}

	if req.Header.Get("Content-Type") == "" && body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		// Go embeds the full request URL in transport errors, and
		// expandVars may have substituted a credential into it. This message
		// is persisted to the intents table, broadcast over the WebSocket and
		// POSTed to notification webhooks.
		return &engine.ActionResult{
			Success: false,
			Message: sanitise(err.Error(), client),
		}, nil
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))

	return &engine.ActionResult{
		Success: resp.StatusCode >= 200 && resp.StatusCode < 300,
		Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode,
			sanitise(strings.TrimSpace(string(respBody)), client)),
	}, nil
}

// maxResponseBody caps how much of a response is captured into a result
// message, which is persisted and broadcast.
const maxResponseBody = 4096

// sanitise removes anything credential-shaped from a message that will be
// persisted, logged or sent to a webhook.
func sanitise(msg string, client *engine.Client) string {
	msg = netutil.RedactSecrets(msg, client.Credentials)

	// Transport errors quote the URL; redact any credential-bearing query
	// parameters that survived.
	for _, field := range urlFields {
		if raw := getConfigString(client.TransportConfig, field); raw != "" {
			expanded := expandVars(raw, client)
			msg = strings.ReplaceAll(msg, expanded, netutil.RedactURL(expanded))
		}
	}
	return msg
}

// urlFields are the transport_config keys that hold URLs.
var urlFields = []string{
	"shutdown_url", "wake_url", "probe_url",
	"poe_off_url", "poe_on_url", "outlet_off_url", "outlet_on_url",
}

func (t *Transport) Probe(ctx context.Context, client *engine.Client) (engine.ClientState, error) {
	url := getConfigString(client.TransportConfig, "probe_url")
	if url == "" {
		port := client.ProbeConfig.Port
		if port == 0 {
			port = 80
		}
		url = netutil.URL("http", client.Address, port, "/")
	}

	url = expandVars(url, client)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return engine.StateUnknown, fmt.Errorf("building probe request: %w", err)
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		// A refused connection says the host is not serving; a timeout or
		// DNS failure says nothing about the host at all.
		if netutil.ClassifyDialError(err) == netutil.Unreachable {
			return engine.StateDown, nil
		}
		return engine.StateUnknown, fmt.Errorf("probing %s: %s",
			netutil.RedactURL(url), sanitise(err.Error(), client))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	// Any HTTP response at all means something is serving. A 5xx means the
	// application is unhealthy, not that the host has powered off, so it is
	// deliberately not reported as down.
	return engine.StateUp, nil
}

func getConfigString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	v, ok := cfg[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func expandVars(s string, client *engine.Client) string {
	s = strings.ReplaceAll(s, "{address}", client.Address)
	s = strings.ReplaceAll(s, "{name}", client.Name)
	s = strings.ReplaceAll(s, "{mac}", client.MAC)
	if client.Credentials != "" {
		s = strings.ReplaceAll(s, "{credentials}", client.Credentials)
	}
	return s
}
