package snmp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/nkcx/canarium/internal/engine"
)

type Source struct {
	logger *slog.Logger
}

func NewSource(logger *slog.Logger) *Source {
	return &Source{logger: logger}
}

func (s *Source) Name() string { return "snmp" }

func (s *Source) Declarations() []engine.SourceDeclaration {
	return nil
}

func (s *Source) Start(ctx context.Context, updates chan<- engine.FactUpdate) error {
	s.logger.Info("SNMP source started (poll targets configured at runtime)")
	return nil
}

func (s *Source) Stop() error {
	return nil
}

type PoeTransport struct {
	logger *slog.Logger
}

func NewPoeTransport(logger *slog.Logger) *PoeTransport {
	return &PoeTransport{logger: logger}
}

func (t *PoeTransport) Name() string { return "snmp-poe" }

func (t *PoeTransport) Capabilities() []engine.Capability {
	return []engine.Capability{
		{Action: engine.ActionPoeOff, Idempotent: true, Timeout: 10 * time.Second},
		{Action: engine.ActionPoeOn, Idempotent: true, Timeout: 10 * time.Second},
		{Action: engine.ActionProbe, Idempotent: true, Timeout: 10 * time.Second},
	}
}

func (t *PoeTransport) Execute(ctx context.Context, client *engine.Client, action engine.ActionType) (*engine.ActionResult, error) {
	switch action {
	case engine.ActionPoeOff, engine.ActionPoeOn:
		return t.setPoeState(ctx, client, action == engine.ActionPoeOn)
	default:
		return nil, fmt.Errorf("snmp-poe transport does not support action %s", action)
	}
}

func (t *PoeTransport) Probe(ctx context.Context, client *engine.Client) (engine.ClientState, error) {
	port := client.ProbeConfig.Port
	if port == 0 {
		port = 161
	}

	timeout := client.ProbeConfig.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	addr := fmt.Sprintf("%s:%d", client.Address, port)
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return engine.StateDown, nil
	}
	conn.Close()
	return engine.StateUp, nil
}

func (t *PoeTransport) setPoeState(ctx context.Context, client *engine.Client, enable bool) (*engine.ActionResult, error) {
	t.logger.Info("SNMP PoE control",
		"client", client.Name,
		"address", client.Address,
		"enable", enable,
	)

	return &engine.ActionResult{
		Success: true,
		Message: fmt.Sprintf("PoE %s (SNMP SET stub — requires gosnmp integration)", map[bool]string{true: "enabled", false: "disabled"}[enable]),
	}, nil
}
