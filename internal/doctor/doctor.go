// Package doctor runs live preflight checks against a configuration.
//
// `canarium validate` is offline and deterministic: it checks a config
// against itself and against what this build supports. doctor is the
// complement — it contacts the things the config names and reports whether
// they are actually reachable and usable.
//
// The point is to fail at 3pm on a Tuesday rather than during the outage.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/facts"
	"github.com/nkcx/canarium/internal/netutil"
	"github.com/nkcx/canarium/internal/state"
)

// Status is the outcome of one check.
type Status int

const (
	// StatusOK means the check passed.
	StatusOK Status = iota

	// StatusWarn means something is questionable but not disqualifying.
	StatusWarn

	// StatusFail means this will not work when it matters.
	StatusFail

	// StatusSkip means the check did not apply.
	StatusSkip
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusWarn:
		return "WARN"
	case StatusFail:
		return "FAIL"
	default:
		return "SKIP"
	}
}

// Check is one preflight result.
type Check struct {
	// Subject is what was checked, e.g. `client "nas"`.
	Subject string

	// Name is which property of the subject was checked.
	Name string

	Status Status

	// Detail explains the result, and for a failure says what to do.
	Detail string

	// Duration is how long the check took, which surfaces a host that
	// answers but only just.
	Duration time.Duration
}

// Report is the full set of results.
type Report struct {
	Checks []Check
}

// Add appends a check.
func (r *Report) Add(c Check) { r.Checks = append(r.Checks, c) }

// Counts returns how many checks landed in each status.
func (r *Report) Counts() map[Status]int {
	counts := make(map[Status]int, 4)
	for _, c := range r.Checks {
		counts[c.Status]++
	}
	return counts
}

// HasFailures reports whether any check failed.
func (r *Report) HasFailures() bool {
	for _, c := range r.Checks {
		if c.Status == StatusFail {
			return true
		}
	}
	return false
}

// Options configures a doctor run.
type Options struct {
	// Timeout bounds each individual check.
	Timeout time.Duration

	// SourceSettle is how long to wait for a source to produce its first
	// facts before concluding it cannot.
	SourceSettle time.Duration

	// Concurrency limits how many clients are probed at once, so a large
	// fleet does not open hundreds of sockets simultaneously.
	Concurrency int
}

// DefaultOptions returns sensible defaults.
func DefaultOptions() Options {
	return Options{
		Timeout:      5 * time.Second,
		SourceSettle: 20 * time.Second,
		Concurrency:  8,
	}
}

// Doctor runs live checks.
type Doctor struct {
	cfg        *config.Config
	transports map[string]engine.Transport
	sources    []engine.Source
	store      *facts.Store
	opts       Options

	// learnedMACs is what the daemon has already discovered, read from the
	// state database. Nil when it could not be read, in which case doctor
	// falls back to looking addresses up itself.
	learnedMACs map[string]state.LearnedMAC
}

// New builds a Doctor.
func New(
	cfg *config.Config,
	transports map[string]engine.Transport,
	sources []engine.Source,
	store *facts.Store,
	opts Options,
) *Doctor {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultOptions().Timeout
	}
	if opts.SourceSettle <= 0 {
		opts.SourceSettle = DefaultOptions().SourceSettle
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = DefaultOptions().Concurrency
	}

	return &Doctor{
		cfg:        cfg,
		transports: transports,
		sources:    sources,
		store:      store,
		opts:       opts,
	}
}

// Run executes every check.
func (d *Doctor) Run(ctx context.Context) *Report {
	report := &Report{}

	d.checkDataDir(report)
	d.checkSSHKey(report)
	d.checkSources(ctx, report)
	d.checkClients(ctx, report)

	sort.SliceStable(report.Checks, func(i, j int) bool {
		return report.Checks[i].Subject < report.Checks[j].Subject
	})

	return report
}

// checkDataDir verifies the state directory is writable, which the daemon
// needs before it can record anything at all.
func (d *Doctor) checkDataDir(report *Report) {
	dir := d.cfg.Canarium.DataDir
	start := time.Now()

	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		report.Add(Check{
			Subject: "canarium", Name: "data_dir", Status: StatusWarn,
			Detail:   fmt.Sprintf("%s does not exist; it will be created at startup", dir),
			Duration: time.Since(start),
		})
		return
	}
	if err != nil {
		report.Add(Check{
			Subject: "canarium", Name: "data_dir", Status: StatusFail,
			Detail: fmt.Sprintf("cannot stat %s: %s", dir, err), Duration: time.Since(start),
		})
		return
	}
	if !info.IsDir() {
		report.Add(Check{
			Subject: "canarium", Name: "data_dir", Status: StatusFail,
			Detail: fmt.Sprintf("%s is not a directory", dir), Duration: time.Since(start),
		})
		return
	}

	probe, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		report.Add(Check{
			Subject: "canarium", Name: "data_dir", Status: StatusFail,
			Detail:   fmt.Sprintf("%s is not writable: %s", dir, err),
			Duration: time.Since(start),
		})
		return
	}
	name := probe.Name()
	probe.Close()
	os.Remove(name)

	report.Add(Check{
		Subject: "canarium", Name: "data_dir", Status: StatusOK,
		Detail: dir + " is writable", Duration: time.Since(start),
	})
}

// checkSSHKey verifies the configured SSH key exists and parses, which is
// otherwise discovered only when a shutdown is attempted.
func (d *Doctor) checkSSHKey(report *Report) {
	usesSSH := false
	for _, c := range d.cfg.Clients {
		if c.Transport == "ssh" {
			usesSSH = true
			break
		}
	}
	if !usesSSH {
		return
	}

	start := time.Now()
	path := d.cfg.Transports.SSH.KeyPath

	if path == "" {
		report.Add(Check{
			Subject: "transport ssh", Name: "key_path", Status: StatusFail,
			Detail:   "no key_path configured under transports.ssh, and clients use the ssh transport",
			Duration: time.Since(start),
		})
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		report.Add(Check{
			Subject: "transport ssh", Name: "key_path", Status: StatusFail,
			Detail: fmt.Sprintf("cannot read %s: %s", path, err), Duration: time.Since(start),
		})
		return
	}

	status, detail := StatusOK, path+" is readable"
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		status = StatusWarn
		detail = fmt.Sprintf("%s is mode %o; a private key should not be group- or world-readable", path, perm)
	}

	report.Add(Check{
		Subject: "transport ssh", Name: "key_path", Status: status,
		Detail: detail, Duration: time.Since(start),
	})
}

// checkSources starts every source briefly and reports which facts arrive.
//
// This is the check that matters most: a source that cannot authenticate
// produces no facts, every condition reading them stays unavailable, and the
// daemon sits there looking healthy while being blind.
func (d *Doctor) checkSources(ctx context.Context, report *Report) {
	if len(d.sources) == 0 {
		report.Add(Check{
			Subject: "sources", Name: "configured", Status: StatusWarn,
			Detail: "no sources are configured, so no plan can ever trigger",
		})
		return
	}

	for _, source := range d.sources {
		d.checkSource(ctx, report, source)
	}
}

func (d *Doctor) checkSource(ctx context.Context, report *Report, source engine.Source) {
	subject := "source " + source.Name()
	start := time.Now()

	runCtx, cancel := context.WithTimeout(ctx, d.opts.SourceSettle)
	defer cancel()

	updates := make(chan engine.FactUpdate, 64)
	if err := source.Start(runCtx, updates); err != nil {
		report.Add(Check{
			Subject: subject, Name: "start", Status: StatusFail,
			Detail: err.Error(), Duration: time.Since(start),
		})
		return
	}
	defer func() {
		if err := source.Stop(); err != nil {
			report.Add(Check{
				Subject: subject, Name: "stop", Status: StatusWarn,
				Detail: err.Error(),
			})
		}
	}()

	// Expect at least one fact per declared instance.
	expected := make(map[string]bool)
	for _, decl := range source.Declarations() {
		expected[decl.InstanceName] = true
	}

	seen := make(map[string]bool)
	deadline := time.After(d.opts.SourceSettle)

collect:
	for len(seen) < len(expected) {
		select {
		case update := <-updates:
			instance, _, _ := strings.Cut(update.Key, ".")
			seen[instance] = true
		case <-deadline:
			break collect
		case <-runCtx.Done():
			break collect
		}
	}

	for instance := range expected {
		if seen[instance] {
			report.Add(Check{
				Subject: subject + " / " + instance, Name: "facts", Status: StatusOK,
				Detail: "reporting", Duration: time.Since(start),
			})
			continue
		}
		report.Add(Check{
			Subject: subject + " / " + instance, Name: "facts", Status: StatusFail,
			Detail: fmt.Sprintf(
				"no facts received within %s — check the host, port and credentials; "+
					"the daemon would run blind with every condition reading this source unavailable",
				d.opts.SourceSettle),
			Duration: time.Since(start),
		})
	}
}

// checkClients resolves and probes every configured client.
func (d *Doctor) checkClients(ctx context.Context, report *Report) {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, d.opts.Concurrency)
	)

	add := func(c Check) {
		mu.Lock()
		report.Add(c)
		mu.Unlock()
	}

	for i := range d.cfg.Clients {
		client := &d.cfg.Clients[i]

		wg.Add(1)
		go func() {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			d.checkClient(ctx, client, add)
		}()
	}

	wg.Wait()
}

func (d *Doctor) checkClient(ctx context.Context, c *config.ClientConfig, add func(Check)) {
	subject := fmt.Sprintf("client %q", c.Name)

	transport, ok := d.transports[c.Transport]
	if !ok {
		add(Check{
			Subject: subject, Name: "transport", Status: StatusFail,
			Detail: fmt.Sprintf("unknown transport %q", c.Transport),
		})
		return
	}

	if c.Address == "" {
		add(Check{
			Subject: subject, Name: "address", Status: StatusWarn,
			Detail: "no address configured",
		})
	} else {
		d.checkResolves(ctx, subject, c.Address, add)
	}

	d.checkCredentials(subject, c, add)
	d.checkWakeMAC(ctx, c, add)

	// Probe only if the transport can.
	hasProbe := false
	for _, capability := range transport.Capabilities() {
		if capability.Action == engine.ActionProbe {
			hasProbe = true
			break
		}
	}
	if !hasProbe {
		add(Check{
			Subject: subject, Name: "probe", Status: StatusSkip,
			Detail: fmt.Sprintf("the %s transport cannot probe; shutdown cannot be verified", c.Transport),
		})
		return
	}

	start := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, d.opts.Timeout)
	defer cancel()

	timeout := d.opts.Timeout
	if c.Probe != nil {
		if parsed, err := config.Duration(c.Probe.Timeout, timeout); err == nil {
			timeout = parsed
		}
	}

	state, err := transport.Probe(probeCtx, &engine.Client{
		Name:            c.Name,
		Address:         c.Address,
		TransportConfig: c.Config,
		ProbeConfig:     engine.ProbeConfig{Timeout: timeout, Port: probePort(c)},
	})

	switch {
	case err != nil:
		add(Check{
			Subject: subject, Name: "probe", Status: StatusWarn,
			Detail:   fmt.Sprintf("inconclusive: %s (the host may simply be off)", err),
			Duration: time.Since(start),
		})
	case state == engine.StateUp:
		add(Check{
			Subject: subject, Name: "probe", Status: StatusOK,
			Detail: "reachable", Duration: time.Since(start),
		})
	default:
		add(Check{
			Subject: subject, Name: "probe", Status: StatusWarn,
			Detail:   "not reachable (expected if the host is currently off)",
			Duration: time.Since(start),
		})
	}
}

func probePort(c *config.ClientConfig) int {
	if c.Probe != nil {
		return c.Probe.Port
	}
	return 0
}

func (d *Doctor) checkResolves(ctx context.Context, subject, address string, add func(Check)) {
	start := time.Now()

	if ip := net.ParseIP(address); ip != nil {
		add(Check{
			Subject: subject, Name: "address", Status: StatusOK,
			Detail: address + " is a literal address", Duration: time.Since(start),
		})
		return
	}

	resolveCtx, cancel := context.WithTimeout(ctx, d.opts.Timeout)
	defer cancel()

	addrs, err := net.DefaultResolver.LookupHost(resolveCtx, address)
	if err != nil {
		add(Check{
			Subject: subject, Name: "address", Status: StatusFail,
			Detail: fmt.Sprintf("%s does not resolve: %s. During an outage DNS may "+
				"be down too; consider a literal address.", address, err),
			Duration: time.Since(start),
		})
		return
	}

	add(Check{
		Subject: subject, Name: "address", Status: StatusWarn,
		Detail: fmt.Sprintf("%s resolves to %s, but DNS may itself be unavailable "+
			"during an outage; a literal address is more robust",
			address, strings.Join(addrs, ", ")),
		Duration: time.Since(start),
	})
}

// checkCredentials reports clients whose transport needs a credential and
// does not have one.
func (d *Doctor) checkCredentials(subject string, c *config.ClientConfig, add func(Check)) {
	needsCredentials := map[string]bool{
		"proxmox":  true,
		"truenas":  true,
		"opnsense": true,
	}
	if !needsCredentials[c.Transport] {
		return
	}

	if strings.TrimSpace(c.Credentials) == "" {
		add(Check{
			Subject: subject, Name: "credentials", Status: StatusFail,
			Detail: fmt.Sprintf("the %s transport requires credentials and none are set", c.Transport),
		})
		return
	}

	status, detail := StatusOK, "configured"

	// TLS verification is on by default now; say so where it has been
	// turned off, since that is a standing risk rather than a one-off.
	if netutil.TLSOptionsFrom(c.Config).InsecureSkipVerify {
		status = StatusWarn
		detail = "configured, but TLS verification is disabled for this client; " +
			"credentials can be captured by an on-path attacker"
	}

	add(Check{Subject: subject, Name: "credentials", Status: status, Detail: detail})
}
