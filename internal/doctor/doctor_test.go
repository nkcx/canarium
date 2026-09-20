package doctor

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/facts"
)

// stubTransport is a minimal engine.Transport for exercising checks.
type stubTransport struct {
	caps       []engine.Capability
	probeState engine.ClientState
	probeErr   error
}

func (s *stubTransport) Name() string                      { return "stub" }
func (s *stubTransport) Capabilities() []engine.Capability { return s.caps }

func (s *stubTransport) Execute(context.Context, *engine.Client, engine.ActionType) (*engine.ActionResult, error) {
	return &engine.ActionResult{Success: true}, nil
}

func (s *stubTransport) Probe(context.Context, *engine.Client) (engine.ClientState, error) {
	return s.probeState, s.probeErr
}

func probeCapable(state engine.ClientState, err error) *stubTransport {
	return &stubTransport{
		caps:       []engine.Capability{{Action: engine.ActionProbe}},
		probeState: state,
		probeErr:   err,
	}
}

// stubSource emits a fixed set of facts, or none.
type stubSource struct {
	instance string
	emit     bool
	startErr error
}

func (s *stubSource) Name() string { return "stub" }

func (s *stubSource) Declarations() []engine.SourceDeclaration {
	return []engine.SourceDeclaration{{
		InstanceName: s.instance,
		PollInterval: time.Second,
		Facts:        []engine.FactDeclEntry{{Name: "status", Type: "set"}},
	}}
}

func (s *stubSource) Start(ctx context.Context, updates chan<- engine.FactUpdate) error {
	if s.startErr != nil {
		return s.startErr
	}
	if !s.emit {
		return nil
	}

	go func() {
		select {
		case updates <- engine.FactUpdate{
			Key: s.instance + ".status", Value: []string{"OL"}, Timestamp: time.Now(),
		}:
		case <-ctx.Done():
		}
	}()
	return nil
}

func (s *stubSource) Stop() error { return nil }

func find(report *Report, subject, name string) (Check, bool) {
	for _, c := range report.Checks {
		if strings.Contains(c.Subject, subject) && c.Name == name {
			return c, true
		}
	}
	return Check{}, false
}

func baseConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{Canarium: config.DefaultCanariumConfig()}
	cfg.Canarium.DataDir = t.TempDir()
	return cfg
}

func fastOptions() Options {
	return Options{Timeout: time.Second, SourceSettle: 300 * time.Millisecond, Concurrency: 4}
}

func TestDataDirWritable(t *testing.T) {
	cfg := baseConfig(t)

	report := New(cfg, nil, nil, facts.NewStore(), fastOptions()).Run(context.Background())

	check, ok := find(report, "canarium", "data_dir")
	if !ok {
		t.Fatal("no data_dir check was run")
	}
	if check.Status != StatusOK {
		t.Errorf("data_dir check = %v (%s), want OK", check.Status, check.Detail)
	}
}

func TestDataDirMissingIsAWarning(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Canarium.DataDir = filepath.Join(t.TempDir(), "not-created-yet")

	report := New(cfg, nil, nil, facts.NewStore(), fastOptions()).Run(context.Background())

	check, _ := find(report, "canarium", "data_dir")
	if check.Status != StatusWarn {
		t.Errorf("missing data_dir = %v, want WARN (it is created at startup)", check.Status)
	}
}

func TestDataDirNotWritableIsAFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permissions are not enforced")
	}

	dir := t.TempDir()
	readonly := filepath.Join(dir, "readonly")
	if err := os.Mkdir(readonly, 0o500); err != nil {
		t.Fatalf("creating read-only dir: %v", err)
	}

	cfg := baseConfig(t)
	cfg.Canarium.DataDir = readonly

	report := New(cfg, nil, nil, facts.NewStore(), fastOptions()).Run(context.Background())

	check, _ := find(report, "canarium", "data_dir")
	if check.Status != StatusFail {
		t.Errorf("unwritable data_dir = %v, want FAIL", check.Status)
	}
}

// TestSourceProducingNoFactsIsAFailure is the check that matters most: a
// source that cannot authenticate produces nothing, every condition reading
// it stays unavailable, and the daemon looks healthy while running blind.
func TestSourceProducingNoFactsIsAFailure(t *testing.T) {
	cfg := baseConfig(t)
	sources := []engine.Source{&stubSource{instance: "ups", emit: false}}

	report := New(cfg, nil, sources, facts.NewStore(), fastOptions()).Run(context.Background())

	check, ok := find(report, "ups", "facts")
	if !ok {
		t.Fatal("no source facts check was run")
	}
	if check.Status != StatusFail {
		t.Errorf("silent source = %v (%s), want FAIL", check.Status, check.Detail)
	}
	if !strings.Contains(check.Detail, "blind") {
		t.Errorf("failure detail does not explain the consequence: %q", check.Detail)
	}
}

func TestSourceProducingFactsPasses(t *testing.T) {
	cfg := baseConfig(t)
	sources := []engine.Source{&stubSource{instance: "ups", emit: true}}

	report := New(cfg, nil, sources, facts.NewStore(), fastOptions()).Run(context.Background())

	check, _ := find(report, "ups", "facts")
	if check.Status != StatusOK {
		t.Errorf("reporting source = %v (%s), want OK", check.Status, check.Detail)
	}
}

func TestNoSourcesIsAWarning(t *testing.T) {
	cfg := baseConfig(t)

	report := New(cfg, nil, nil, facts.NewStore(), fastOptions()).Run(context.Background())

	check, ok := find(report, "sources", "configured")
	if !ok {
		t.Fatal("no check for the absence of sources")
	}
	if check.Status != StatusWarn {
		t.Errorf("no sources = %v, want WARN", check.Status)
	}
}

func TestMissingCredentialsIsAFailure(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Clients = []config.ClientConfig{{
		Name: "pve", Transport: "proxmox", Address: "127.0.0.1",
	}}

	transports := map[string]engine.Transport{"proxmox": probeCapable(engine.StateDown, nil)}
	report := New(cfg, transports, nil, facts.NewStore(), fastOptions()).Run(context.Background())

	check, ok := find(report, "pve", "credentials")
	if !ok {
		t.Fatal("no credentials check was run")
	}
	if check.Status != StatusFail {
		t.Errorf("missing credentials = %v, want FAIL", check.Status)
	}
}

func TestInsecureTLSIsAWarning(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Clients = []config.ClientConfig{{
		Name: "pve", Transport: "proxmox", Address: "127.0.0.1",
		Credentials: "token",
		Config:      map[string]any{"tls_insecure_skip_verify": true},
	}}

	transports := map[string]engine.Transport{"proxmox": probeCapable(engine.StateUp, nil)}
	report := New(cfg, transports, nil, facts.NewStore(), fastOptions()).Run(context.Background())

	check, _ := find(report, "pve", "credentials")
	if check.Status != StatusWarn {
		t.Errorf("insecure TLS = %v, want WARN", check.Status)
	}
	if !strings.Contains(check.Detail, "on-path") {
		t.Errorf("warning does not explain the risk: %q", check.Detail)
	}
}

func TestUnknownTransportIsAFailure(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Clients = []config.ClientConfig{{Name: "nas", Transport: "nonexistent"}}

	report := New(cfg, map[string]engine.Transport{}, nil, facts.NewStore(), fastOptions()).
		Run(context.Background())

	check, ok := find(report, "nas", "transport")
	if !ok {
		t.Fatal("no transport check was run")
	}
	if check.Status != StatusFail {
		t.Errorf("unknown transport = %v, want FAIL", check.Status)
	}
}

// TestNonProbeTransportIsSkippedWithAnExplanation: a transport that cannot
// probe means shutdown can never be verified, which the operator should know.
func TestNonProbeTransportIsSkipped(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Clients = []config.ClientConfig{{Name: "nas", Transport: "wol", Address: "10.0.0.1"}}

	transports := map[string]engine.Transport{
		"wol": &stubTransport{caps: []engine.Capability{{Action: engine.ActionWake}}},
	}
	report := New(cfg, transports, nil, facts.NewStore(), fastOptions()).Run(context.Background())

	check, ok := find(report, "nas", "probe")
	if !ok {
		t.Fatal("no probe check was recorded")
	}
	if check.Status != StatusSkip {
		t.Errorf("non-probing transport = %v, want SKIP", check.Status)
	}
	if !strings.Contains(check.Detail, "verified") {
		t.Errorf("skip detail does not explain the consequence: %q", check.Detail)
	}
}

func TestReachableClientPasses(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	defer ln.Close()

	cfg := baseConfig(t)
	cfg.Clients = []config.ClientConfig{{Name: "nas", Transport: "ssh", Address: "127.0.0.1"}}

	transports := map[string]engine.Transport{"ssh": probeCapable(engine.StateUp, nil)}
	report := New(cfg, transports, nil, facts.NewStore(), fastOptions()).Run(context.Background())

	check, _ := find(report, "nas", "probe")
	if check.Status != StatusOK {
		t.Errorf("reachable client = %v (%s), want OK", check.Status, check.Detail)
	}
}

func TestLiteralAddressPreferredOverDNS(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Clients = []config.ClientConfig{{Name: "nas", Transport: "ssh", Address: "10.0.0.1"}}

	transports := map[string]engine.Transport{"ssh": probeCapable(engine.StateDown, nil)}
	report := New(cfg, transports, nil, facts.NewStore(), fastOptions()).Run(context.Background())

	check, _ := find(report, "nas", "address")
	if check.Status != StatusOK {
		t.Errorf("literal address = %v, want OK", check.Status)
	}
}

func TestUnresolvableAddressIsAFailure(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Clients = []config.ClientConfig{{
		Name: "nas", Transport: "ssh", Address: "does-not-exist.invalid",
	}}

	transports := map[string]engine.Transport{"ssh": probeCapable(engine.StateDown, nil)}
	report := New(cfg, transports, nil, facts.NewStore(), fastOptions()).Run(context.Background())

	check, ok := find(report, "nas", "address")
	if !ok {
		t.Fatal("no address check was run")
	}
	if check.Status != StatusFail {
		t.Skipf("resolver returned an address for a .invalid name (status %v)", check.Status)
	}
}

func TestSSHKeyMissingIsAFailure(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Clients = []config.ClientConfig{{Name: "nas", Transport: "ssh", Address: "10.0.0.1"}}
	cfg.Transports.SSH.KeyPath = "/nonexistent/id_ed25519"

	transports := map[string]engine.Transport{"ssh": probeCapable(engine.StateDown, nil)}
	report := New(cfg, transports, nil, facts.NewStore(), fastOptions()).Run(context.Background())

	check, ok := find(report, "transport ssh", "key_path")
	if !ok {
		t.Fatal("no ssh key check was run")
	}
	if check.Status != StatusFail {
		t.Errorf("missing ssh key = %v, want FAIL", check.Status)
	}
}

func TestSSHKeyWithLoosePermissionsIsAWarning(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("key"), 0o644); err != nil {
		t.Fatalf("writing key: %v", err)
	}

	cfg := baseConfig(t)
	cfg.Clients = []config.ClientConfig{{Name: "nas", Transport: "ssh", Address: "10.0.0.1"}}
	cfg.Transports.SSH.KeyPath = keyPath

	transports := map[string]engine.Transport{"ssh": probeCapable(engine.StateDown, nil)}
	report := New(cfg, transports, nil, facts.NewStore(), fastOptions()).Run(context.Background())

	check, _ := find(report, "transport ssh", "key_path")
	if check.Status != StatusWarn {
		t.Errorf("world-readable key = %v, want WARN", check.Status)
	}
}

func TestSSHKeyCheckSkippedWithoutSSHClients(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Clients = []config.ClientConfig{{Name: "nas", Transport: "wol", Address: "10.0.0.1"}}

	transports := map[string]engine.Transport{
		"wol": &stubTransport{caps: []engine.Capability{{Action: engine.ActionWake}}},
	}
	report := New(cfg, transports, nil, facts.NewStore(), fastOptions()).Run(context.Background())

	if _, ok := find(report, "transport ssh", "key_path"); ok {
		t.Error("an ssh key check ran with no ssh clients configured")
	}
}

func TestReportCountsAndFailures(t *testing.T) {
	report := &Report{}
	report.Add(Check{Status: StatusOK})
	report.Add(Check{Status: StatusOK})
	report.Add(Check{Status: StatusWarn})
	report.Add(Check{Status: StatusSkip})

	if report.HasFailures() {
		t.Error("HasFailures reported true with no failures")
	}

	counts := report.Counts()
	if counts[StatusOK] != 2 || counts[StatusWarn] != 1 || counts[StatusSkip] != 1 {
		t.Errorf("unexpected counts: %+v", counts)
	}

	report.Add(Check{Status: StatusFail})
	if !report.HasFailures() {
		t.Error("HasFailures did not notice a failure")
	}
}

func TestStatusString(t *testing.T) {
	for _, tt := range []struct {
		s    Status
		want string
	}{
		{StatusOK, "OK"}, {StatusWarn, "WARN"}, {StatusFail, "FAIL"}, {StatusSkip, "SKIP"},
	} {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("Status(%d).String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}
