package truenas

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nkcx/canarium/internal/engine"
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

func (t *Transport) Probe(ctx context.Context, client *engine.Client) (engine.ClientState, error) {
	port := client.ProbeConfig.Port
	if port == 0 {
		port = 443
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

func (t *Transport) connect(client *engine.Client) (*websocket.Conn, error) {
	port := 443
	if p, ok := client.TransportConfig["port"].(int); ok {
		port = p
	}

	url := fmt.Sprintf("wss://%s:%d/api/current", client.Address, port)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		url = fmt.Sprintf("ws://%s:%d/api/current", client.Address, port)
		conn, _, err = dialer.Dial(url, nil)
		if err != nil {
			return nil, err
		}
	}

	return conn, nil
}

func (t *Transport) authenticate(conn *websocket.Conn, client *engine.Client) error {
	result, err := t.callRPCOn(conn, "auth.login_with_api_key", []any{client.Credentials})
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
	return t.callRPCOn(conn, method, params)
}

func (t *Transport) callRPCOn(conn *websocket.Conn, method string, params any) (any, error) {
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

var _ = sync.Mutex{}
