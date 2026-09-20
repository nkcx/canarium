package opnsense

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/netutil"
)

type Transport struct {
	logger *slog.Logger

	mu      sync.Mutex
	clients map[netutil.TLSOptions]*http.Client
}

func New(logger *slog.Logger) *Transport {
	return &Transport{
		logger:  logger,
		clients: make(map[netutil.TLSOptions]*http.Client),
	}
}

// httpClientFor returns an HTTP client configured for one Canarium client's
// TLS settings.
//
// Verification defaults to on. It was previously hardcoded off, so the
// OPNsense API key and secret — sent as HTTP basic auth — travelled over a
// connection an on-path attacker could impersonate and read.
func (t *Transport) httpClientFor(client *engine.Client) (*http.Client, error) {
	opts := netutil.TLSOptionsFrom(client.TransportConfig)

	t.mu.Lock()
	defer t.mu.Unlock()

	if c, ok := t.clients[opts]; ok {
		return c, nil
	}

	tlsCfg, err := opts.TLSConfig(t.logger, client.Name)
	if err != nil {
		return nil, err
	}

	c := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
	t.clients[opts] = c
	return c, nil
}

func (t *Transport) Name() string { return "opnsense" }

func (t *Transport) Capabilities() []engine.Capability {
	return []engine.Capability{
		{Action: engine.ActionShutdown, Idempotent: true, Timeout: 30 * time.Second},
		{Action: engine.ActionProbe, Idempotent: true, Timeout: 10 * time.Second},
	}
}

func (t *Transport) Execute(ctx context.Context, client *engine.Client, action engine.ActionType) (*engine.ActionResult, error) {
	if action != engine.ActionShutdown {
		return nil, fmt.Errorf("opnsense transport does not support action %s", action)
	}

	port := 443
	if p, ok := client.TransportConfig["port"].(int); ok {
		port = p
	}

	apiURL := netutil.URL("https", client.Address, port, "/api/core/system/halt")

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	if client.Credentials != "" {
		apiKey, apiSecret := parseCredentials(client.Credentials)
		req.SetBasicAuth(apiKey, apiSecret)
	}

	httpClient, err := t.httpClientFor(client)
	if err != nil {
		return nil, fmt.Errorf("opnsense TLS configuration: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return &engine.ActionResult{Success: false, Message: err.Error()}, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return &engine.ActionResult{Success: true, Message: "halt command sent"}, nil
	}

	return &engine.ActionResult{
		Success: false,
		Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body)),
	}, nil
}

// Probe reports whether the host is reachable on its probe port.
//
// A dial failure is not reported as "down" unless the network gave positive
// evidence — a refused connection, or a router reporting the host absent. A
// timeout or DNS failure returns an error, so the executor records
// down_unverified rather than treating a network partition as a completed
// shutdown.
func (t *Transport) Probe(ctx context.Context, client *engine.Client) (engine.ClientState, error) {
	port := client.ProbeConfig.Port
	if port == 0 {
		port = 443
	}

	addr := netutil.HostPort(client.Address, port)
	reach, err := netutil.ProbeTCP(ctx, addr, client.ProbeConfig.Timeout)

	switch reach {
	case netutil.Reachable:
		return engine.StateUp, nil
	case netutil.Unreachable:
		return engine.StateDown, nil
	default:
		return engine.StateUnknown, fmt.Errorf("probing %s: %w", addr, err)
	}
}

// parseCredentials splits an OPNsense "key:secret" credential pair.
func parseCredentials(creds string) (key, secret string) {
	key, secret, _ = strings.Cut(creds, ":")
	return key, secret
}
