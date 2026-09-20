package truenas

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/netutil"
)

type Transport struct {
	logger *slog.Logger
}

func New(logger *slog.Logger) *Transport {
	return &Transport{logger: logger}
}

func (t *Transport) Name() string { return "truenas" }

func (t *Transport) Capabilities() []engine.Capability {
	return []engine.Capability{
		{Action: engine.ActionShutdown, Idempotent: true, Timeout: 30 * time.Second},
		{Action: engine.ActionProbe, Idempotent: true, Timeout: 10 * time.Second},
	}
}

func (t *Transport) Execute(ctx context.Context, client *engine.Client, action engine.ActionType) (*engine.ActionResult, error) {
	if action != engine.ActionShutdown {
		return nil, fmt.Errorf("truenas transport does not support action %s", action)
	}

	conn, err := t.connect(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("connecting to TrueNAS: %w", err)
	}
	defer conn.Close()

	// Bound the whole exchange by the caller's deadline rather than the
	// per-call read deadline alone.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(deadline)
		_ = conn.SetWriteDeadline(deadline)
	}

	if err := t.authenticate(conn, client); err != nil {
		// The error may quote the request, which carries the API key.
		return nil, fmt.Errorf("authentication failed: %s",
			netutil.RedactSecrets(err.Error(), client.Credentials))
	}

	result, err := t.callRPC(conn, "system.shutdown", map[string]any{
		"delay": 0,
	})
	if err != nil {
		return &engine.ActionResult{Success: false, Message: err.Error()}, nil
	}

	return &engine.ActionResult{
		Success: true,
		Message: fmt.Sprintf("shutdown initiated: %v", result),
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

// connect opens a WebSocket to the TrueNAS middleware API.
//
// There is deliberately no fallback from wss:// to ws://. The previous
// implementation retried in plaintext whenever the TLS dial failed for any
// reason — and TrueNAS ships a self-signed certificate by default, so the
// common case was: verification fails, silently downgrade, and send
// auth.login_with_api_key over an unencrypted socket. Anyone on the path
// captured an API key with full control of the storage array, and nothing in
// the logs said the connection had been downgraded.
//
// Operators with a self-signed certificate set tls_ca_cert to pin it, or
// tls_insecure_skip_verify to accept the risk knowingly. Plaintext requires
// setting `tls: false`, which is honest about what it does.
func (t *Transport) connect(ctx context.Context, client *engine.Client) (*websocket.Conn, error) {
	port := 443
	if p, ok := client.TransportConfig["port"].(int); ok {
		port = p
	}

	useTLS := true
	if v, ok := client.TransportConfig["tls"].(bool); ok {
		useTLS = v
	}

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}

	scheme := "wss"
	if useTLS {
		opts := netutil.TLSOptionsFrom(client.TransportConfig)
		tlsCfg, err := opts.TLSConfig(t.logger, client.Name)
		if err != nil {
			return nil, fmt.Errorf("truenas TLS configuration: %w", err)
		}
		dialer.TLSClientConfig = tlsCfg
	} else {
		scheme = "ws"
		t.logger.Warn("connecting to TrueNAS without TLS; the API key is sent in cleartext",
			"client", client.Name)
	}

	url := netutil.URL(scheme, client.Address, port, "/api/current")

	conn, resp, err := dialer.DialContext(ctx, url, nil)
	if err != nil {
		status := ""
		if resp != nil {
			status = " (HTTP " + resp.Status + ")"
		}
		return nil, fmt.Errorf("dialing %s%s: %w", url, status, err)
	}

	return conn, nil
}

func (t *Transport) authenticate(conn *websocket.Conn, client *engine.Client) error {
	result, err := t.callRPC(conn, "auth.login_with_api_key", []any{client.Credentials})
	if err != nil {
		return err
	}

	if success, ok := result.(bool); ok && success {
		return nil
	}

	// TrueNAS 25.04+ returns a session object rather than a bare true.
	if _, ok := result.(map[string]any); ok {
		return nil
	}

	return fmt.Errorf("unexpected auth response: %v", result)
}

var rpcID atomic.Int64

func (t *Transport) callRPC(conn *websocket.Conn, method string, params any) (any, error) {
	id := rpcID.Add(1)

	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"id":      id,
		"params":  params,
	}

	if err := conn.WriteJSON(msg); err != nil {
		return nil, fmt.Errorf("writing RPC: %w", err)
	}

	// Best effort: a failure here surfaces as the read below timing out.
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	for {
		var resp map[string]any
		if err := conn.ReadJSON(&resp); err != nil {
			return nil, fmt.Errorf("reading RPC response: %w", err)
		}

		respID, ok := resp["id"]
		if !ok {
			continue
		}

		if fmt.Sprint(respID) == fmt.Sprint(id) {
			if errObj, ok := resp["error"]; ok {
				errJSON, _ := json.Marshal(errObj)
				return nil, fmt.Errorf("RPC error: %s", string(errJSON))
			}
			return resp["result"], nil
		}
	}
}
