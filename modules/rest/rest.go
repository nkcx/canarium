package rest

import (
	"context"
	"fmt"
	"io"
	"net/http"
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
		return &engine.ActionResult{Success: false, Message: err.Error()}, nil
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return &engine.ActionResult{
			Success: true,
			Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody)),
		}, nil
	}

	return &engine.ActionResult{
		Success: false,
		Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody)),
	}, nil
}

func (t *Transport) Probe(ctx context.Context, client *engine.Client) (engine.ClientState, error) {
	url := getConfigString(client.TransportConfig, "probe_url")
	if url == "" {
		port := client.ProbeConfig.Port
		if port == 0 {
			port = 80
		}
		url = fmt.Sprintf("http://%s:%d/", client.Address, port)
	}

	url = expandVars(url, client)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return engine.StateDown, nil
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return engine.StateDown, nil
	}
	resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		return engine.StateUp, nil
	}
	return engine.StateDown, nil
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

