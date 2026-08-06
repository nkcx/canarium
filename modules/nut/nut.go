package nut

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/facts"
)

type Source struct {
	instances []InstanceConfig
	logger    *slog.Logger
	conns     map[string]net.Conn
}

type Config struct {
	Instances []InstanceConfig `yaml:"instances"`
}

type InstanceConfig struct {
	Name         string `yaml:"name"`
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	UPS          string `yaml:"ups"`
	PollInterval string `yaml:"poll_interval"`
	Username     string `yaml:"username,omitempty"`
	Password     string `yaml:"password,omitempty"`
}

func NewSource(cfg Config, logger *slog.Logger) *Source {
	return &Source{
		instances: cfg.Instances,
		logger:    logger,
		conns:     make(map[string]net.Conn),
	}
}

func (s *Source) Name() string { return "nut" }

func (s *Source) Declarations() []engine.SourceDeclaration {
	var decls []engine.SourceDeclaration
	for _, inst := range s.instances {
		poll, _ := time.ParseDuration(inst.PollInterval)
		if poll == 0 {
			poll = 15 * time.Second
		}
		decls = append(decls, engine.SourceDeclaration{
			InstanceName: inst.Name,
			PollInterval: poll,
			Facts: []engine.FactDeclEntry{
				{Name: "battery.charge", Type: "percent", Description: "State of charge"},
				{Name: "battery.runtime", Type: "duration", Description: "Estimated runtime remaining (seconds)"},
				{Name: "status", Type: "set", Values: []string{"OL", "OB", "LB", "RB", "CHRG", "DISCHRG", "ALARM", "OVER", "TRIM", "BOOST", "BYPASS", "OFF"}, Description: "UPS status flags"},
				{Name: "battery.voltage", Type: "number", Description: "Battery voltage"},
				{Name: "input.voltage", Type: "number", Description: "Input voltage"},
				{Name: "output.voltage", Type: "number", Description: "Output voltage"},
				{Name: "ups.load", Type: "percent", Description: "UPS load percentage"},
				{Name: "ups.temperature", Type: "number", Unit: "celsius", Description: "UPS internal temperature"},
			},
		})
	}
	return decls
}

func (s *Source) Start(ctx context.Context, updates chan<- engine.FactUpdate) error {
	for _, inst := range s.instances {
		go s.pollInstance(ctx, inst, updates)
	}
	return nil
}

func (s *Source) Stop() error {
	for _, conn := range s.conns {
		conn.Close()
	}
	return nil
}

func (s *Source) pollInstance(ctx context.Context, inst InstanceConfig, updates chan<- engine.FactUpdate) {
	poll, _ := time.ParseDuration(inst.PollInterval)
	if poll == 0 {
		poll = 15 * time.Second
	}

	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	s.fetchAndUpdate(inst, updates)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.fetchAndUpdate(inst, updates)
		}
	}
}

func (s *Source) fetchAndUpdate(inst InstanceConfig, updates chan<- engine.FactUpdate) {
	host := inst.Host
	if host == "" {
		host = "localhost"
	}
	port := inst.Port
	if port == 0 {
		port = 3493
	}
	ups := inst.UPS
	if ups == "" {
		ups = "ups"
	}

	vars, err := s.queryUPS(host, port, ups)
	if err != nil {
		s.logger.Error("NUT poll failed", "instance", inst.Name, "error", err)
		return
	}

	now := time.Now()
	prefix := inst.Name

	if v, ok := vars["battery.charge"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			updates <- engine.FactUpdate{Key: prefix + ".battery.charge", Value: f, Timestamp: now}
		}
	}

	if v, ok := vars["battery.runtime"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			updates <- engine.FactUpdate{Key: prefix + ".battery.runtime", Value: f, Timestamp: now}
		}
	}

	if v, ok := vars["ups.status"]; ok {
		flags := strings.Fields(v)
		updates <- engine.FactUpdate{Key: prefix + ".status", Value: flags, Timestamp: now}
	}

	numericVars := map[string]string{
		"battery.voltage": "battery.voltage",
		"input.voltage":   "input.voltage",
		"output.voltage":  "output.voltage",
		"ups.load":        "ups.load",
		"ups.temperature": "ups.temperature",
	}
	for nutVar, factName := range numericVars {
		if v, ok := vars[nutVar]; ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				updates <- engine.FactUpdate{Key: prefix + "." + factName, Value: f, Timestamp: now}
			}
		}
	}
}

func (s *Source) queryUPS(host string, port int, ups string) (map[string]string, error) {
	addr := fmt.Sprintf("%s:%d", host, port)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connecting to NUT: %w", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(10 * time.Second))

	fmt.Fprintf(conn, "LIST VAR %s\n", ups)

	scanner := bufio.NewScanner(conn)
	vars := make(map[string]string)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "BEGIN LIST VAR") {
			continue
		}
		if strings.HasPrefix(line, "END LIST VAR") {
			break
		}
		if strings.HasPrefix(line, "ERR") {
			return nil, fmt.Errorf("NUT error: %s", line)
		}
		if strings.HasPrefix(line, "VAR "+ups+" ") {
			rest := strings.TrimPrefix(line, "VAR "+ups+" ")
			parts := strings.SplitN(rest, " ", 2)
			if len(parts) == 2 {
				name := parts[0]
				value := strings.Trim(parts[1], "\"")
				vars[name] = value
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading NUT response: %w", err)
	}

	return vars, nil
}

func RegisterFacts(store *facts.Store, cfg Config) {
	src := NewSource(cfg, slog.Default())
	for _, decl := range src.Declarations() {
		var factDecls []facts.FactDeclaration
		for _, f := range decl.Facts {
			factDecls = append(factDecls, facts.FactDeclaration{
				Name:        f.Name,
				Type:        f.Type,
				Values:      f.Values,
				Description: f.Description,
				Unit:        f.Unit,
			})
		}
		store.RegisterSource(decl.InstanceName, decl.PollInterval, factDecls)
	}
}

type NUTTransport struct {
	logger *slog.Logger
}

func NewTransport(logger *slog.Logger) *NUTTransport {
	return &NUTTransport{logger: logger}
}

func (t *NUTTransport) Name() string { return "nut" }

func (t *NUTTransport) Capabilities() []engine.Capability {
	return []engine.Capability{
		{Action: engine.ActionOutletOff, Idempotent: false, Timeout: 10 * time.Second},
		{Action: engine.ActionOutletOn, Idempotent: false, Timeout: 10 * time.Second},
	}
}

func (t *NUTTransport) Execute(ctx context.Context, client *engine.Client, action engine.ActionType) (*engine.ActionResult, error) {
	command, ok := client.TransportConfig["command"].(string)
	if !ok {
		return nil, fmt.Errorf("no NUT command configured")
	}

	host := client.Address
	if host == "" {
		host = "localhost"
	}

	addr := fmt.Sprintf("%s:3493", host)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connecting to NUT: %w", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(10 * time.Second))

	upsName := client.Name
	fmt.Fprintf(conn, "INSTCMD %s %s\n", upsName, command)

	scanner := bufio.NewScanner(conn)
	if scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "OK") {
			return &engine.ActionResult{Success: true, Message: "NUT command executed"}, nil
		}
		return &engine.ActionResult{Success: false, Message: line}, fmt.Errorf("NUT command failed: %s", line)
	}

	return nil, fmt.Errorf("no response from NUT")
}

func (t *NUTTransport) Probe(ctx context.Context, client *engine.Client) (engine.ClientState, error) {
	return engine.StateUnknown, fmt.Errorf("NUT transport does not support probe")
}
