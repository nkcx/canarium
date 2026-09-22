// Package nut reads UPS state from a Network UPS Tools server and issues
// instant commands to it.
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
	"github.com/nkcx/canarium/internal/netutil"
)

const (
	// defaultNUTPort is the IANA-registered port for the NUT network protocol.
	defaultNUTPort = 3493

	// defaultUPSName is NUT's conventional single-UPS name.
	defaultUPSName = "ups"

	// defaultPollInterval matches NUT's own default driver poll.
	defaultPollInterval = 15 * time.Second

	dialTimeout = 5 * time.Second
	ioTimeout   = 10 * time.Second
)

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

func (c InstanceConfig) host() string {
	if c.Host == "" {
		return "localhost"
	}
	return c.Host
}

func (c InstanceConfig) port() int {
	if c.Port == 0 {
		return defaultNUTPort
	}
	return c.Port
}

func (c InstanceConfig) ups() string {
	if c.UPS == "" {
		return defaultUPSName
	}
	return c.UPS
}

func (c InstanceConfig) pollInterval() time.Duration {
	if c.PollInterval == "" {
		return defaultPollInterval
	}
	d, err := time.ParseDuration(c.PollInterval)
	if err != nil || d <= 0 {
		return defaultPollInterval
	}
	return d
}

type Source struct {
	instances []InstanceConfig
	logger    *slog.Logger
}

func NewSource(cfg Config, logger *slog.Logger) *Source {
	return &Source{instances: cfg.Instances, logger: logger}
}

func (s *Source) Name() string { return "nut" }

// standardVar maps one NUT variable onto a Canarium fact.
type standardVar struct {
	nut  string // the variable as NUT names it
	fact string // the fact name under the instance
	typ  string
	unit string
	desc string
}

// statusFlags are the values NUT's ups.status can carry.
var statusFlags = []string{
	"OL", "OB", "LB", "HB", "RB", "CHRG", "DISCHRG", "BYPASS", "CAL",
	"OFF", "OVER", "TRIM", "BOOST", "FSD", "ALARM",
}

// standardVars is the set of NUT variables Canarium publishes as facts,
// from NUT's variable naming standard (docs/nut-names.txt).
//
// This used to be eight variables, listed twice -- once to declare them
// and once to read them -- and the two lists had to be kept in step by
// hand. Every poll already fetched the UPS's complete variable list with
// LIST VAR and then discarded everything else, so a UPS that reports its
// real power, nominal ratings, input frequency or transfer thresholds had
// those readings thrown away. An APC Back-UPS shows output watts on its
// front panel; Canarium could not.
//
// A UPS reports whichever subset its driver supports. Variables it never
// sends stay unknown, and the dashboard lists them as not provided by the
// hardware rather than as faults.
var standardVars = []standardVar{
	{nut: "ups.status", fact: "status", typ: "set", desc: "UPS status flags"},

	{nut: "battery.charge", fact: "battery.charge", typ: "percent", desc: "State of charge"},
	{nut: "battery.charge.low", fact: "battery.charge.low", typ: "percent", desc: "Charge at which the UPS reports low battery"},
	{nut: "battery.charge.warning", fact: "battery.charge.warning", typ: "percent", desc: "Charge at which the UPS warns"},
	{nut: "battery.runtime", fact: "battery.runtime", typ: "duration", unit: "seconds", desc: "Estimated runtime remaining"},
	{nut: "battery.runtime.low", fact: "battery.runtime.low", typ: "duration", unit: "seconds", desc: "Runtime at which the UPS reports low battery"},
	{nut: "battery.voltage", fact: "battery.voltage", typ: "number", unit: "volts", desc: "Battery voltage"},
	{nut: "battery.voltage.nominal", fact: "battery.voltage.nominal", typ: "number", unit: "volts", desc: "Nominal battery voltage"},
	{nut: "battery.current", fact: "battery.current", typ: "number", unit: "amps", desc: "Battery current"},
	{nut: "battery.temperature", fact: "battery.temperature", typ: "number", unit: "celsius", desc: "Battery temperature"},

	{nut: "input.voltage", fact: "input.voltage", typ: "number", unit: "volts", desc: "Input voltage"},
	{nut: "input.voltage.nominal", fact: "input.voltage.nominal", typ: "number", unit: "volts", desc: "Nominal input voltage"},
	{nut: "input.frequency", fact: "input.frequency", typ: "number", unit: "hertz", desc: "Input frequency"},
	{nut: "input.current", fact: "input.current", typ: "number", unit: "amps", desc: "Input current"},
	{nut: "input.transfer.low", fact: "input.transfer.low", typ: "number", unit: "volts", desc: "Input voltage below which the UPS goes to battery"},
	{nut: "input.transfer.high", fact: "input.transfer.high", typ: "number", unit: "volts", desc: "Input voltage above which the UPS goes to battery"},

	{nut: "output.voltage", fact: "output.voltage", typ: "number", unit: "volts", desc: "Output voltage"},
	{nut: "output.voltage.nominal", fact: "output.voltage.nominal", typ: "number", unit: "volts", desc: "Nominal output voltage"},
	{nut: "output.frequency", fact: "output.frequency", typ: "number", unit: "hertz", desc: "Output frequency"},
	{nut: "output.current", fact: "output.current", typ: "number", unit: "amps", desc: "Output current"},

	{nut: "ups.load", fact: "ups.load", typ: "percent", desc: "Load as a share of capacity"},
	{nut: "ups.realpower", fact: "ups.realpower", typ: "number", unit: "watts", desc: "Real power output"},
	{nut: "ups.realpower.nominal", fact: "ups.realpower.nominal", typ: "number", unit: "watts", desc: "Rated real power"},
	{nut: "ups.power", fact: "ups.power", typ: "number", unit: "VA", desc: "Apparent power output"},
	{nut: "ups.power.nominal", fact: "ups.power.nominal", typ: "number", unit: "VA", desc: "Rated apparent power"},
	{nut: "ups.efficiency", fact: "ups.efficiency", typ: "percent", desc: "Efficiency"},
	{nut: "ups.temperature", fact: "ups.temperature", typ: "number", unit: "celsius", desc: "UPS internal temperature"},

	{nut: "device.mfr", fact: "device.mfr", typ: "string", desc: "Manufacturer"},
	{nut: "device.model", fact: "device.model", typ: "string", desc: "Model"},
	{nut: "ups.test.result", fact: "ups.test.result", typ: "string", desc: "Result of the last self-test"},
}

func (s *Source) Declarations() []engine.SourceDeclaration {
	facts := make([]engine.FactDeclEntry, 0, len(standardVars))
	for _, v := range standardVars {
		entry := engine.FactDeclEntry{Name: v.fact, Type: v.typ, Unit: v.unit, Description: v.desc}
		if v.typ == "set" {
			entry.Values = statusFlags
		}
		facts = append(facts, entry)
	}

	decls := make([]engine.SourceDeclaration, 0, len(s.instances))
	for _, inst := range s.instances {
		decls = append(decls, engine.SourceDeclaration{
			InstanceName: inst.Name,
			PollInterval: inst.pollInterval(),
			Facts:        facts,
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

// Stop releases the source's resources.
//
// Each poll opens and closes its own connection, so there is nothing to tear
// down; the method exists to satisfy engine.Source.
func (s *Source) Stop() error { return nil }

func (s *Source) pollInstance(ctx context.Context, inst InstanceConfig, updates chan<- engine.FactUpdate) {
	ticker := time.NewTicker(inst.pollInterval())
	defer ticker.Stop()

	s.fetchAndUpdate(ctx, inst, updates)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.fetchAndUpdate(ctx, inst, updates)
		}
	}
}

func (s *Source) fetchAndUpdate(ctx context.Context, inst InstanceConfig, updates chan<- engine.FactUpdate) {
	vars, err := s.queryUPS(ctx, inst)
	if err != nil {
		s.logger.Error("NUT poll failed",
			"instance", inst.Name, "host", inst.host(), "ups", inst.ups(), "error", err)
		return
	}

	now := time.Now()

	emit := func(key string, value any) {
		select {
		case updates <- engine.FactUpdate{Key: inst.Name + "." + key, Value: value, Timestamp: now}:
		case <-ctx.Done():
		}
	}

	for _, sv := range standardVars {
		raw, ok := vars[sv.nut]
		if !ok {
			continue
		}

		switch sv.typ {
		case "set":
			emit(sv.fact, strings.Fields(raw))
		case "string":
			emit(sv.fact, strings.TrimSpace(raw))
		default:
			f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
			if err != nil {
				s.logger.Warn("NUT returned a non-numeric value for a numeric variable",
					"instance", inst.Name, "variable", sv.nut, "value", raw)
				continue
			}
			emit(sv.fact, f)
		}
	}
}

// queryUPS opens a connection, authenticates if credentials are configured,
// and lists the UPS's variables.
func (s *Source) queryUPS(ctx context.Context, inst InstanceConfig) (map[string]string, error) {
	conn, err := dial(ctx, inst.host(), inst.port())
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	session := newSession(conn)
	defer session.logout()

	if err := session.authenticate(inst.Username, inst.Password); err != nil {
		return nil, err
	}

	return session.listVars(inst.ups())
}

// RegisterFacts pre-registers fact declarations so conditions can reference
// them before the first poll completes.
func RegisterFacts(store *facts.Store, cfg Config, logger *slog.Logger) {
	src := NewSource(cfg, logger)
	for _, decl := range src.Declarations() {
		factDecls := make([]facts.FactDeclaration, 0, len(decl.Facts))
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

// ---------------------------------------------------------------------------
// Protocol
// ---------------------------------------------------------------------------

func dial(ctx context.Context, host string, port int) (net.Conn, error) {
	addr := netutil.HostPort(host, port)

	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connecting to NUT at %s: %w", addr, err)
	}

	deadline := time.Now().Add(ioTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return nil, fmt.Errorf("setting deadline: %w", err)
	}

	return conn, nil
}

// session wraps a NUT connection with the request/response handling the
// protocol needs.
type session struct {
	conn    net.Conn
	scanner *bufio.Scanner
}

func newSession(conn net.Conn) *session {
	return &session{conn: conn, scanner: bufio.NewScanner(conn)}
}

func (s *session) send(format string, args ...any) error {
	if _, err := fmt.Fprintf(s.conn, format+"\n", args...); err != nil {
		return fmt.Errorf("writing to NUT: %w", err)
	}
	return nil
}

func (s *session) readLine() (string, error) {
	if !s.scanner.Scan() {
		if err := s.scanner.Err(); err != nil {
			return "", fmt.Errorf("reading from NUT: %w", err)
		}
		return "", fmt.Errorf("NUT closed the connection unexpectedly")
	}
	return s.scanner.Text(), nil
}

// authenticate performs the USERNAME/PASSWORD exchange.
//
// This was previously absent entirely. InstanceConfig declared Username and
// Password, examples/basic.yaml documented them, and neither was ever read
// from the config or sent to the server. Any NUT server requiring
// authentication — which is every server that permits instant commands —
// rejected the connection, and the post_shutdown feature that tells the UPS
// to cut its outlets could never have worked.
func (s *session) authenticate(username, password string) error {
	if username == "" && password == "" {
		return nil
	}
	if username == "" || password == "" {
		return fmt.Errorf("NUT credentials incomplete: both username and password are required")
	}

	if err := s.send("USERNAME %s", username); err != nil {
		return err
	}
	line, err := s.readLine()
	if err != nil {
		return err
	}
	if err := checkOK(line, "USERNAME"); err != nil {
		return err
	}

	if err := s.send("PASSWORD %s", password); err != nil {
		return err
	}
	line, err = s.readLine()
	if err != nil {
		return err
	}
	return checkOK(line, "PASSWORD")
}

// listVars issues LIST VAR and collects the response.
func (s *session) listVars(ups string) (map[string]string, error) {
	if err := s.send("LIST VAR %s", ups); err != nil {
		return nil, err
	}

	vars := make(map[string]string)
	prefix := "VAR " + ups + " "
	sawBegin := false

	for {
		line, err := s.readLine()
		if err != nil {
			return nil, err
		}

		switch {
		case strings.HasPrefix(line, "BEGIN LIST VAR"):
			sawBegin = true

		case strings.HasPrefix(line, "END LIST VAR"):
			if !sawBegin {
				return nil, fmt.Errorf("NUT sent END LIST VAR without BEGIN")
			}
			return vars, nil

		case strings.HasPrefix(line, "ERR "):
			return nil, nutError(line)

		case strings.HasPrefix(line, prefix):
			name, value, ok := parseVarLine(strings.TrimPrefix(line, prefix))
			if ok {
				vars[name] = value
			}
		}
	}
}

// logout closes the session politely. Errors are ignored: the connection is
// being torn down regardless.
func (s *session) logout() {
	_ = s.send("LOGOUT")
}

// parseVarLine splits a `name "value"` pair from a LIST VAR response.
//
// NUT quotes values and escapes embedded quotes and backslashes with a
// backslash. The previous implementation used strings.Trim, which strips any
// number of quotes from both ends and leaves escapes in place, so a value
// containing a quote came back mangled.
func parseVarLine(rest string) (name, value string, ok bool) {
	name, quoted, found := strings.Cut(rest, " ")
	if !found || name == "" {
		return "", "", false
	}

	quoted = strings.TrimSpace(quoted)
	if !strings.HasPrefix(quoted, `"`) || !strings.HasSuffix(quoted, `"`) || len(quoted) < 2 {
		// Unquoted values are not standard but are harmless to accept.
		return name, quoted, true
	}

	inner := quoted[1 : len(quoted)-1]

	var sb strings.Builder
	sb.Grow(len(inner))
	escaped := false
	for _, r := range inner {
		if escaped {
			sb.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		sb.WriteRune(r)
	}

	return name, sb.String(), true
}

func checkOK(line, what string) error {
	if strings.HasPrefix(line, "OK") {
		return nil
	}
	if strings.HasPrefix(line, "ERR ") {
		return fmt.Errorf("NUT rejected %s: %w", what, nutError(line))
	}
	return fmt.Errorf("unexpected NUT response to %s: %q", what, line)
}

// nutError turns an ERR line into a Go error, expanding the codes an operator
// is most likely to hit into something actionable.
func nutError(line string) error {
	code := strings.TrimSpace(strings.TrimPrefix(line, "ERR "))
	if i := strings.IndexByte(code, ' '); i >= 0 {
		code = code[:i]
	}

	switch code {
	case "ACCESS-DENIED":
		return fmt.Errorf("access denied: check the username and password, and that " +
			"upsd.users grants this user the required actions")
	case "UNKNOWN-UPS":
		return fmt.Errorf("unknown UPS: the name does not match any entry in ups.conf")
	case "CMD-NOT-SUPPORTED":
		return fmt.Errorf("command not supported by this UPS driver")
	case "INSTCMD-FAILED":
		return fmt.Errorf("the UPS rejected the instant command")
	case "DRIVER-NOT-CONNECTED":
		return fmt.Errorf("the NUT driver is not connected to the UPS")
	case "DATA-STALE":
		return fmt.Errorf("NUT reports its data as stale; the driver has lost contact with the UPS")
	default:
		return fmt.Errorf("NUT error: %s", code)
	}
}

// ---------------------------------------------------------------------------
// Transport — instant commands
// ---------------------------------------------------------------------------

type NUTTransport struct {
	logger *slog.Logger
}

func NewTransport(logger *slog.Logger) *NUTTransport {
	return &NUTTransport{logger: logger}
}

func (t *NUTTransport) Name() string { return "nut" }

func (t *NUTTransport) Capabilities() []engine.Capability {
	return []engine.Capability{
		{Action: engine.ActionOutletOff, Idempotent: false, Timeout: ioTimeout},
		{Action: engine.ActionOutletOn, Idempotent: false, Timeout: ioTimeout},
	}
}

// Execute issues an instant command (INSTCMD) to the UPS.
//
// Instant commands always require authentication on any NUT server that has
// not been deliberately opened up, so credentials are effectively mandatory
// here.
func (t *NUTTransport) Execute(ctx context.Context, client *engine.Client, action engine.ActionType) (*engine.ActionResult, error) {
	switch action {
	case engine.ActionOutletOff, engine.ActionOutletOn:
	default:
		return nil, fmt.Errorf("nut transport does not support action %s", action)
	}

	cfg := client.TransportConfig

	command := configString(cfg, "command")
	if command == "" {
		return nil, fmt.Errorf("nut transport: no command configured")
	}

	// The host to connect to is distinct from the UPS to command. The
	// executor previously passed the UPS name as the address, so
	// post-shutdown tried to resolve "ups" as a hostname.
	host := client.Address
	if host == "" {
		host = "localhost"
	}

	port := configInt(cfg, "port")
	if port == 0 {
		port = defaultNUTPort
	}

	upsName := configString(cfg, "ups")
	if upsName == "" {
		upsName = client.Name
	}
	if upsName == "" {
		upsName = defaultUPSName
	}

	conn, err := dial(ctx, host, port)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	session := newSession(conn)
	defer session.logout()

	if err := session.authenticate(configString(cfg, "username"), configString(cfg, "password")); err != nil {
		return nil, fmt.Errorf("authenticating to NUT at %s: %w", netutil.HostPort(host, port), err)
	}

	if err := session.send("INSTCMD %s %s", upsName, command); err != nil {
		return nil, err
	}

	line, err := session.readLine()
	if err != nil {
		return nil, err
	}

	if err := checkOK(line, "INSTCMD "+command); err != nil {
		return &engine.ActionResult{Success: false, Message: err.Error()}, err
	}

	t.logger.Info("NUT instant command accepted",
		"ups", upsName, "host", host, "command", command)

	return &engine.ActionResult{
		Success: true,
		Message: fmt.Sprintf("NUT accepted %s for %s", command, upsName),
	}, nil
}

func (t *NUTTransport) Probe(ctx context.Context, client *engine.Client) (engine.ClientState, error) {
	return engine.StateUnknown, fmt.Errorf("nut transport does not support probe")
}

func configString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	s, _ := cfg[key].(string)
	return s
}

func configInt(cfg map[string]any, key string) int {
	if cfg == nil {
		return 0
	}
	switch v := cfg[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return 0
	}
}
