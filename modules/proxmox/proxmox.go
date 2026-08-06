package proxmox

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nkcx/canarium/internal/engine"
)

type Transport struct {
	httpClient *http.Client
}

func New() *Transport {
	return &Transport{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

func (t *Transport) Name() string { return "proxmox" }

func (t *Transport) Capabilities() []engine.Capability {
	return []engine.Capability{
		{Action: engine.ActionShutdown, Idempotent: true, Timeout: 30 * time.Second},
		{Action: engine.ActionProbe, Idempotent: true, Timeout: 10 * time.Second},
	}
}

func (t *Transport) Execute(ctx context.Context, client *engine.Client, action engine.ActionType) (*engine.ActionResult, error) {
	if action != engine.ActionShutdown {
		return nil, fmt.Errorf("proxmox transport does not support action %s", action)
	}

	node := client.Name
	if n, ok := client.TransportConfig["node"].(string); ok {
		node = n
	}

	port := 8006
	if p, ok := client.TransportConfig["port"].(int); ok {
		port = p
	}

	apiURL := fmt.Sprintf("https://%s:%d/api2/json/nodes/%s/status", client.Address, port, node)

	data := url.Values{}
	data.Set("command", "shutdown")

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if client.Credentials != "" {
		req.Header.Set("Authorization", "PVEAPIToken="+client.Credentials)
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return &engine.ActionResult{Success: false, Message: err.Error()}, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return &engine.ActionResult{Success: true, Message: "shutdown command sent"}, nil
	}

	return &engine.ActionResult{
		Success: false,
		Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body)),
	}, nil
}

func (t *Transport) Probe(ctx context.Context, client *engine.Client) (engine.ClientState, error) {
	port := client.ProbeConfig.Port
	if port == 0 {
		port = 8006
	}

	timeout := client.ProbeConfig.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	addr := fmt.Sprintf("%s:%d", client.Address, port)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return engine.StateDown, nil
	}
	conn.Close()
	return engine.StateUp, nil
}
