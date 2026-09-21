package snmp

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/facts"
	"github.com/nkcx/canarium/internal/netutil"
)

// pethPsePortAdminEnable OID from RFC 3621 POWER-ETHERNET-MIB.
// Indexed by pethPsePortGroupIndex and pethPsePortIndex.
// Values: 1 = true (enabled), 2 = false (disabled).
const poeAdminEnableOID = "1.3.6.1.2.1.105.1.1.1.3"

// Config holds SNMP source configuration parsed from the top-level sources
// section of the Canarium config file.
type Config struct {
	Instances []InstanceConfig `yaml:"instances"`
}

// InstanceConfig defines a single SNMP polling target with the OIDs to query
// and the connection parameters.
type InstanceConfig struct {
	Name         string    `yaml:"name"`
	Host         string    `yaml:"host"`
	Port         int       `yaml:"port"`
	PollInterval string    `yaml:"poll_interval"`
	Version      int       `yaml:"snmp_version"`
	Community    string    `yaml:"snmp_community"`
	User         string    `yaml:"snmp_user"`
	AuthPass     string    `yaml:"snmp_auth_pass"`
	PrivPass     string    `yaml:"snmp_priv_pass"`
	OIDs         []OIDSpec `yaml:"oids"`
}

// OIDSpec describes a single OID to poll and the fact it produces.
type OIDSpec struct {
	OID  string `yaml:"oid"`
	Name string `yaml:"name"`
	Type string `yaml:"type"`
}

// ---------------------------------------------------------------------------
// Source — generic SNMP GET polling for arbitrary OIDs
// ---------------------------------------------------------------------------

// Source polls configured SNMP agents and emits FactUpdates for each OID.
type Source struct {
	instances []InstanceConfig
	logger    *slog.Logger
}

// NewSource creates a Source from explicit InstanceConfig values.
func NewSource(cfg Config, logger *slog.Logger) *Source {
	return &Source{
		instances: cfg.Instances,
		logger:    logger,
	}
}

func (s *Source) Name() string { return "snmp" }

func (s *Source) Declarations() []engine.SourceDeclaration {
	var decls []engine.SourceDeclaration
	for _, inst := range s.instances {
		poll, _ := time.ParseDuration(inst.PollInterval)
		if poll == 0 {
			poll = 30 * time.Second
		}

		var factEntries []engine.FactDeclEntry
		for _, o := range inst.OIDs {
			factType := o.Type
			if factType == "" {
				factType = "number"
			}
			factEntries = append(factEntries, engine.FactDeclEntry{
				Name:        o.Name,
				Type:        factType,
				Description: fmt.Sprintf("SNMP OID %s", o.OID),
			})
		}

		decls = append(decls, engine.SourceDeclaration{
			InstanceName: inst.Name,
			PollInterval: poll,
			Facts:        factEntries,
		})
	}
	return decls
}

func (s *Source) Start(ctx context.Context, updates chan<- engine.FactUpdate) error {
	s.logger.Info("SNMP source started", "instances", len(s.instances))
	for _, inst := range s.instances {
		go s.pollInstance(ctx, inst, updates)
	}
	return nil
}

func (s *Source) Stop() error {
	return nil
}

func (s *Source) pollInstance(ctx context.Context, inst InstanceConfig, updates chan<- engine.FactUpdate) {
	poll, _ := time.ParseDuration(inst.PollInterval)
	if poll == 0 {
		poll = 30 * time.Second
	}

	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	// Immediate first poll.
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
	if len(inst.OIDs) == 0 {
		return
	}

	client, err := newGoSNMP(inst.Host, inst.Port, inst.Version, inst.Community, inst.User, inst.AuthPass, inst.PrivPass)
	if err != nil {
		s.logger.Error("SNMP client init failed", "instance", inst.Name, "error", err)
		return
	}

	if err := client.Connect(); err != nil {
		s.logger.Error("SNMP connect failed", "instance", inst.Name, "host", inst.Host, "error", err)
		return
	}
	defer client.Conn.Close()

	oids := make([]string, len(inst.OIDs))
	for i, o := range inst.OIDs {
		oids[i] = o.OID
	}

	result, err := client.Get(oids)
	if err != nil {
		s.logger.Error("SNMP GET failed", "instance", inst.Name, "error", err)
		return
	}

	now := time.Now()
	for i, variable := range result.Variables {
		if i >= len(inst.OIDs) {
			break
		}
		spec := inst.OIDs[i]

		value := decodeSnmpValue(variable)
		if value == nil {
			s.logger.Warn("SNMP OID returned no value", "oid", spec.OID, "type", variable.Type)
			continue
		}

		updates <- engine.FactUpdate{
			Key:       inst.Name + "." + spec.Name,
			Value:     value,
			Timestamp: now,
		}
	}
}

// RegisterFacts pre-registers fact declarations with the store so conditions
// can reference them before the first poll completes.
func RegisterFacts(store *facts.Store, cfg Config, logger *slog.Logger) {
	src := NewSource(cfg, logger)
	for _, decl := range src.Declarations() {
		var factDecls []facts.FactDeclaration
		for _, f := range decl.Facts {
			factDecls = append(factDecls, facts.FactDeclaration{
				Name:        f.Name,
				Type:        f.Type,
				Description: f.Description,
			})
		}
		store.RegisterSource(decl.InstanceName, decl.PollInterval, factDecls)
	}
}

// ---------------------------------------------------------------------------
// PoeTransport — SNMP SET for RFC 3621 PoE port control
// ---------------------------------------------------------------------------

// PoeTransport executes SNMP SET operations against pethPsePortAdminEnable
// to enable or disable PoE on managed switch ports.
type PoeTransport struct {
	logger *slog.Logger

	mu     sync.Mutex
	warned map[string]bool
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

func (t *PoeTransport) RemapAction(action engine.ActionType) engine.ActionType {
	switch action {
	case engine.ActionShutdown:
		return engine.ActionPoeOff
	case engine.ActionWake:
		return engine.ActionPoeOn
	default:
		return action
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

// switchAddress returns the management address of the switch whose PoE ports
// control this client.
//
// This is deliberately distinct from client.Address. Both used to come from
// the same field, which could not be right for both purposes at once: set it
// to the switch and Probe checks the switch — which is always up, so a
// de-powered camera reported as up forever. Set it to the camera and the
// SNMP SET goes to a device that does not manage the switch's ports.
//
// switch_address in transport_config is the switch; client.Address is the
// device being powered. Falling back to client.Address keeps existing
// configurations working, with a warning, since that is what the guide used
// to tell people to write.
func (t *PoeTransport) switchAddress(client *engine.Client) string {
	if addr := getConfigString(client.TransportConfig, "switch_address"); addr != "" {
		return addr
	}

	if client.Address != "" {
		t.warnOnce(client.Name, func() {
			t.logger.Warn("snmp-poe: no switch_address configured, falling back to the "+
				"client address. Set switch_address to the switch's management IP and "+
				"address to the device being powered, so its state can be verified.",
				"client", client.Name, "address", client.Address)
		})
	}
	return client.Address
}

// warnOnce emits a per-client warning a single time, so a misconfiguration
// does not fill the log on every poll.
func (t *PoeTransport) warnOnce(key string, warn func()) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.warned == nil {
		t.warned = make(map[string]bool)
	}
	if t.warned[key] {
		return
	}
	t.warned[key] = true
	warn()
}

// Probe reports whether a PoE-powered client is up.
//
// The original implementation dialled UDP against the switch's SNMP port and
// reported success as "up". net.Dial on UDP sends no packet — it only binds a
// local socket — so it succeeded for any resolvable address and reported
// every client as up, unconditionally and forever.
//
// Probing a client means probing the client, so this makes a TCP connection
// to the device itself. Set probe.port to a port the device listens on; 22 is
// the default because a PoE device that can be shut down gracefully almost
// certainly runs SSH.
//
// When switch_address is set, client.Address is unambiguously the device. If
// it is not, this probe is checking whatever client.Address points at, which
// may be the switch — hence the warning in switchAddress.
func (t *PoeTransport) Probe(ctx context.Context, client *engine.Client) (engine.ClientState, error) {
	if client.Address == "" {
		return engine.StateUnknown, fmt.Errorf(
			"snmp-poe: client %s has no address, so its power state cannot be verified", client.Name)
	}

	port := client.ProbeConfig.Port
	if port == 0 {
		port = 22
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

func (t *PoeTransport) setPoeState(ctx context.Context, client *engine.Client, enable bool) (*engine.ActionResult, error) {
	cfg := client.TransportConfig

	version := getConfigInt(cfg, "snmp_version")
	community := getConfigString(cfg, "snmp_community")
	user := getConfigString(cfg, "snmp_user")
	authPass := getConfigString(cfg, "snmp_auth_pass")
	privPass := getConfigString(cfg, "snmp_priv_pass")

	ports := getConfigPortList(cfg, "ports")
	if len(ports) == 0 {
		return nil, fmt.Errorf("snmp-poe: no ports configured in transport_config")
	}

	snmpPort := 161
	if p := getConfigInt(cfg, "snmp_port"); p != 0 {
		snmpPort = p
	}

	switchAddr := t.switchAddress(client)
	if switchAddr == "" {
		return nil, fmt.Errorf("snmp-poe: no switch_address configured for client %s", client.Name)
	}

	snmpClient, err := newGoSNMP(switchAddr, snmpPort, version, community, user, authPass, privPass)
	if err != nil {
		return nil, fmt.Errorf("snmp-poe: %w", err)
	}

	if err := snmpClient.Connect(); err != nil {
		return nil, fmt.Errorf("snmp-poe: connecting to %s: %w", switchAddr, err)
	}
	defer snmpClient.Conn.Close()

	// RFC 3621: pethPsePortAdminEnable value 1 = true (enable), 2 = false (disable).
	setValue := 2
	if enable {
		setValue = 1
	}

	var setErrors []string
	for _, p := range ports {
		oid := fmt.Sprintf("%s.%d.%d", poeAdminEnableOID, p.Group, p.Port)
		pdu := gosnmp.SnmpPDU{
			Name:  oid,
			Type:  gosnmp.Integer,
			Value: setValue,
		}

		result, err := snmpClient.Set([]gosnmp.SnmpPDU{pdu})
		if err != nil {
			setErrors = append(setErrors, fmt.Sprintf("port %d/%d: %v", p.Group, p.Port, err))
			t.logger.Error("SNMP SET failed",
				"client", client.Name,
				"oid", oid,
				"error", err,
			)
			continue
		}

		if result.Error != gosnmp.NoError {
			errMsg := fmt.Sprintf("port %d/%d: SNMP error %s (index %d)", p.Group, p.Port, result.Error, result.ErrorIndex)
			setErrors = append(setErrors, errMsg)
			t.logger.Error("SNMP SET returned error",
				"client", client.Name,
				"oid", oid,
				"snmp_error", result.Error.String(),
				"error_index", result.ErrorIndex,
			)
			continue
		}

		t.logger.Info("SNMP PoE SET succeeded",
			"client", client.Name,
			"address", client.Address,
			"group", p.Group,
			"port", p.Port,
			"enable", enable,
		)
	}

	if len(setErrors) > 0 {
		return &engine.ActionResult{
			Success: false,
			Message: fmt.Sprintf("PoE %s failed on %d of %d ports: %s",
				map[bool]string{true: "enable", false: "disable"}[enable],
				len(setErrors), len(ports),
				strings.Join(setErrors, "; ")),
		}, nil
	}

	return &engine.ActionResult{
		Success: true,
		Message: fmt.Sprintf("PoE %s on %d port(s) via SNMP SET",
			map[bool]string{true: "enabled", false: "disabled"}[enable], len(ports)),
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type portSpec struct {
	Group int
	Port  int
}

// newGoSNMP builds a gosnmp.GoSNMP client for the given connection params.
func newGoSNMP(host string, port, version int, community, user, authPass, privPass string) (*gosnmp.GoSNMP, error) {
	if host == "" {
		return nil, fmt.Errorf("SNMP host address is required")
	}
	if port == 0 {
		port = 161
	}

	client := &gosnmp.GoSNMP{
		Target:  host,
		Port:    uint16(port),
		Timeout: 10 * time.Second,
		Retries: 2,
	}

	switch version {
	case 3:
		client.Version = gosnmp.Version3
		client.SecurityModel = gosnmp.UserSecurityModel
		client.MsgFlags = gosnmp.AuthPriv

		secParams := &gosnmp.UsmSecurityParameters{
			UserName: user,
		}

		if authPass != "" {
			secParams.AuthenticationProtocol = gosnmp.SHA
			secParams.AuthenticationPassphrase = authPass
		}

		if privPass != "" {
			secParams.PrivacyProtocol = gosnmp.AES
			secParams.PrivacyPassphrase = privPass
		} else {
			// Auth without privacy.
			client.MsgFlags = gosnmp.AuthNoPriv
		}

		if authPass == "" && privPass == "" {
			client.MsgFlags = gosnmp.NoAuthNoPriv
		}

		client.SecurityParameters = secParams
	case 1:
		client.Version = gosnmp.Version1
		client.Community = community
	default:
		// Default to v2c.
		client.Version = gosnmp.Version2c
		client.Community = community
	}

	return client, nil
}

// decodeSnmpValue converts a gosnmp.SnmpPDU into a Go value suitable for
// fact storage.
func decodeSnmpValue(pdu gosnmp.SnmpPDU) any {
	switch pdu.Type {
	case gosnmp.Integer:
		return gosnmp.ToBigInt(pdu.Value).Int64()
	case gosnmp.OctetString:
		if b, ok := pdu.Value.([]byte); ok {
			return string(b)
		}
		return fmt.Sprint(pdu.Value)
	case gosnmp.Counter32, gosnmp.Gauge32, gosnmp.TimeTicks, gosnmp.Uinteger32:
		return gosnmp.ToBigInt(pdu.Value).Int64()
	case gosnmp.Counter64:
		return gosnmp.ToBigInt(pdu.Value).Int64()
	case gosnmp.ObjectIdentifier:
		return fmt.Sprint(pdu.Value)
	case gosnmp.IPAddress:
		return fmt.Sprint(pdu.Value)
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView:
		return nil
	default:
		return fmt.Sprint(pdu.Value)
	}
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

func getConfigInt(cfg map[string]any, key string) int {
	if cfg == nil {
		return 0
	}
	return toInt(cfg[key])
}

func getConfigPortList(cfg map[string]any, key string) []portSpec {
	if cfg == nil {
		return nil
	}
	v, ok := cfg[key]
	if !ok {
		return nil
	}

	list, ok := v.([]any)
	if !ok {
		return nil
	}

	var ports []portSpec
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		p := portSpec{
			Group: toInt(m["group"]),
			Port:  toInt(m["port"]),
		}
		if p.Group > 0 && p.Port > 0 {
			ports = append(ports, p)
		}
	}
	return ports
}

// toInt coerces a YAML/JSON-decoded value to an int. YAML yields int,
// JSON yields float64, and an environment-substituted value arrives as a
// string, so all three are accepted.
func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	default:
		return 0
	}
}
