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

	conn, err := t.connect(client)
	if err != nil {
		return nil, fmt.Errorf("connecting to TrueNAS: %w", err)
	}
	defer conn.Close()

	if err := t.authenticate(conn, client); err != nil {
		return nil, fmt.Errorf("authentication failed: %w", err)
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

func (t *Transport) connect(client *engine.Client) (*websocket.Conn, error) {
	port := 443
	if p, ok := client.TransportConfig["port"].(int); ok {
		port = p
	}

	url := netutil.URL("wss", client.Address, port, "/api/current")

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		url = netutil.URL("ws", client.Address, port, "/api/current")
		conn, _, err = dialer.Dial(url, nil)
		if err != nil {
			return nil, err
		}
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

	return fmt.Errorf("authentication failed: %v", result)
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

	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

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
