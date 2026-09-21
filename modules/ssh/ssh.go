// Package ssh shuts hosts down by running a command over SSH.
package ssh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/netutil"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Host key verification policies.
const (
	// HostKeyStrict refuses to connect to a host whose key is not already
	// in known_hosts.
	HostKeyStrict = "strict"

	// HostKeyAcceptNew learns a host's key on first contact and pins it
	// thereafter, refusing if it later changes. This mirrors OpenSSH's
	// StrictHostKeyChecking=accept-new.
	HostKeyAcceptNew = "accept-new"

	// HostKeyInsecure accepts any key. Equivalent to the previous
	// unconditional InsecureIgnoreHostKey and offered only as an escape
	// hatch.
	HostKeyInsecure = "insecure"
)

const (
	defaultSSHPort    = 22
	defaultCommand    = "shutdown -h now"
	defaultUser       = "root"
	defaultConnectTTL = 10 * time.Second
	defaultCommandTTL = 30 * time.Second
)

// Config holds the ssh transport's defaults, from the transports.ssh section
// of the config file. Per-client transport_config overrides any of them.
//
// This struct previously existed but was always constructed empty —
// ssh.New(ssh.Config{}) — so key_path, user and command were unreachable and
// the key was always read from $HOME/.ssh/id_ed25519, a path that does not
// exist in the container image.
type Config struct {
	User           string `yaml:"user,omitempty"`
	Port           int    `yaml:"port,omitempty"`
	Command        string `yaml:"command,omitempty"`
	KeyPath        string `yaml:"key_path,omitempty"`
	KeyPassphrase  string `yaml:"key_passphrase,omitempty"`
	KnownHosts     string `yaml:"known_hosts,omitempty"`
	HostKeyPolicy  string `yaml:"host_key_policy,omitempty"`
	ConnectTimeout string `yaml:"connect_timeout,omitempty"`
	CommandTimeout string `yaml:"command_timeout,omitempty"`
}

type Transport struct {
	cfg    Config
	logger *slog.Logger

	// mu guards writes to the known_hosts file, which are appended from
	// multiple client goroutines during a stage.
	mu sync.Mutex
}

func New(cfg Config, logger *slog.Logger) *Transport {
	if cfg.Command == "" {
		cfg.Command = defaultCommand
	}
	if cfg.User == "" {
		cfg.User = defaultUser
	}
	if cfg.Port == 0 {
		cfg.Port = defaultSSHPort
	}
	if cfg.HostKeyPolicy == "" {
		cfg.HostKeyPolicy = HostKeyAcceptNew
	}
	return &Transport{cfg: cfg, logger: logger}
}

func (t *Transport) Name() string { return "ssh" }

func (t *Transport) Capabilities() []engine.Capability {
	return []engine.Capability{
		{Action: engine.ActionShutdown, Idempotent: true, Timeout: defaultCommandTTL},
		{Action: engine.ActionProbe, Idempotent: true, Timeout: 10 * time.Second},
	}
}

func (t *Transport) Execute(ctx context.Context, client *engine.Client, action engine.ActionType) (*engine.ActionResult, error) {
	if action != engine.ActionShutdown {
		return nil, fmt.Errorf("ssh transport does not support action %s", action)
	}

	sshClient, err := t.connect(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("ssh connect: %w", err)
	}
	defer func() { _ = sshClient.Close() }()

	command := t.cfg.Command
	if s := configString(client.TransportConfig, "command"); s != "" {
		command = s
	}

	return t.run(ctx, sshClient, client, command)
}

// run executes a command, bounded by the context.
//
// Session.Run has no timeout of its own: the dial timeout covers only the
// handshake. A host that accepts the connection and then stops responding —
// exactly what a machine mid-shutdown does — would block here forever,
// holding the stage's WaitGroup and stalling the entire sequence past every
// budget. The session is closed from another goroutine when the context
// expires, which unblocks Run.
func (t *Transport) run(ctx context.Context, sshClient *gossh.Client, client *engine.Client, command string) (*engine.ActionResult, error) {
	session, err := sshClient.NewSession()
	if err != nil {
		return nil, fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	timeout := t.commandTimeout(client)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- session.Run(command)
	}()

	select {
	case err := <-done:
		return t.classify(client, command, err)

	case <-runCtx.Done():
		// Closing the session unblocks the goroutine above.
		_ = session.Close()

		// A host that stops responding mid-command is usually a host that
		// is shutting down, which is what we asked for. Report it as such
		// rather than as a failure, and let the probe decide.
		t.logger.Info("ssh command did not return before the timeout; "+
			"the host may be shutting down",
			"client", client.Name, "timeout", timeout)
		return &engine.ActionResult{
			Success: true,
			Message: "command dispatched; connection closed before it returned",
		}, nil
	}
}

// classify turns a session result into an ActionResult.
//
// The previous implementation treated exit status 1 as success, on the
// theory that a host cutting the connection mid-shutdown looks like a
// failure. But exit 1 is also what `sudo` returns on an authentication
// failure and what most shells return for a command that refused to run, so
// a permission problem was reported as "shutdown initiated" and the
// executor moved on believing the host was on its way down.
//
// A clean exit is success. A connection dropped by the host while the
// command was running is success, because that is what a shutdown looks
// like. A non-zero exit status is a failure, reported with the status so the
// journal says what happened.
func (t *Transport) classify(client *engine.Client, command string, err error) (*engine.ActionResult, error) {
	if err == nil {
		return &engine.ActionResult{Success: true, Message: "shutdown command completed"}, nil
	}

	var exitMissing *gossh.ExitMissingError
	if errors.As(err, &exitMissing) {
		// The session ended without an exit status: the host went away
		// while running the command, which is the expected shape of a
		// successful shutdown.
		return &engine.ActionResult{
			Success: true,
			Message: "connection closed by host while the command ran (shutdown in progress)",
		}, nil
	}

	var exitErr *gossh.ExitError
	if errors.As(err, &exitErr) {
		t.logger.Error("shutdown command exited non-zero",
			"client", client.Name,
			"command", command,
			"exit_status", exitErr.ExitStatus())
		return &engine.ActionResult{
			Success: false,
			Message: fmt.Sprintf("command exited with status %d", exitErr.ExitStatus()),
		}, nil
	}

	return &engine.ActionResult{Success: false, Message: err.Error()}, nil
}

// Probe reports whether the host accepts TCP connections on its SSH port.
//
// A dial failure is reported as "down" only when the network gave positive
// evidence; see netutil.ProbeTCP.
func (t *Transport) Probe(ctx context.Context, client *engine.Client) (engine.ClientState, error) {
	addr := netutil.HostPort(client.Address, t.probePort(client))
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

// probePort is the port used for reachability checks.
//
// This is deliberately separate from sshPort. The two were previously the
// same value: connect() read client.ProbeConfig.Port, so an operator who set
// `probe: {port: 443}` to check a web service silently redirected SSH to
// port 443 as well.
func (t *Transport) probePort(client *engine.Client) int {
	if client.ProbeConfig.Port != 0 {
		return client.ProbeConfig.Port
	}
	return t.sshPort(client)
}

func (t *Transport) sshPort(client *engine.Client) int {
	if p := configInt(client.TransportConfig, "port"); p != 0 {
		return p
	}
	if t.cfg.Port != 0 {
		return t.cfg.Port
	}
	return defaultSSHPort
}

func (t *Transport) connect(ctx context.Context, client *engine.Client) (*gossh.Client, error) {
	signer, err := t.loadKey(client)
	if err != nil {
		return nil, err
	}

	user := t.cfg.User
	if s := configString(client.TransportConfig, "user"); s != "" {
		user = s
	}

	hostKeyCallback, err := t.hostKeyCallback(client)
	if err != nil {
		return nil, err
	}

	cfg := &gossh.ClientConfig{
		User:            user,
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: hostKeyCallback,
		Timeout:         t.connectTimeout(client),
	}

	addr := netutil.HostPort(client.Address, t.sshPort(client))

	// gossh.Dial is not context-aware; dial separately so cancellation and
	// the caller's deadline are honoured.
	dialer := net.Dialer{Timeout: cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", addr, err)
	}

	sshConn, chans, reqs, err := gossh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh handshake with %s: %w", addr, err)
	}

	return gossh.NewClient(sshConn, chans, reqs), nil
}

// hostKeyCallback builds the host key verification policy.
//
// The previous implementation used gossh.InsecureIgnoreHostKey()
// unconditionally. That let anyone able to intercept the connection
// impersonate the target: the shutdown command would be delivered to the
// attacker, the real host would stay up, and Canarium would report success.
func (t *Transport) hostKeyCallback(client *engine.Client) (gossh.HostKeyCallback, error) {
	policy := t.cfg.HostKeyPolicy
	if s := configString(client.TransportConfig, "host_key_policy"); s != "" {
		policy = s
	}

	if policy == HostKeyInsecure {
		t.logger.Warn("SSH host key verification is disabled for this client; "+
			"an on-path attacker can impersonate it and the real host will not be shut down",
			"client", client.Name)
		return gossh.InsecureIgnoreHostKey(), nil //nolint:gosec // explicit operator opt-out
	}

	path := t.knownHostsPath(client)
	if path == "" {
		return nil, errors.New("ssh: no known_hosts path configured; " +
			"set transports.ssh.known_hosts or host_key_policy: insecure")
	}

	// knownhosts.New requires the file to exist.
	if err := ensureFile(path); err != nil {
		return nil, fmt.Errorf("ssh: preparing known_hosts %q: %w", path, err)
	}

	verify, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("ssh: reading known_hosts %q: %w", path, err)
	}

	if policy == HostKeyStrict {
		return verify, nil
	}

	// accept-new: trust on first use, then pin.
	return func(hostname string, remote net.Addr, key gossh.PublicKey) error {
		err := verify(hostname, remote, key)
		if err == nil {
			return nil
		}

		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
			// Unknown host: learn it.
			//
			// Re-check under the lock before writing. A stage shuts its
			// clients down concurrently, and each goroutine holds its own
			// snapshot of known_hosts taken before any of them wrote — so
			// two clients reaching the same host, or the same client
			// probed and shut down at once, would each conclude the key was
			// unknown and append it.
			t.mu.Lock()
			defer t.mu.Unlock()

			if fresh, freshErr := knownhosts.New(path); freshErr == nil {
				if fresh(hostname, remote, key) == nil {
					// Another goroutine learned it while we waited.
					return nil
				}
			}

			t.logger.Warn("learning a new SSH host key on first contact; "+
				"verify the fingerprint out of band if this host matters",
				"client", client.Name,
				"host", hostname,
				"fingerprint", gossh.FingerprintSHA256(key))
			return t.appendKnownHostLocked(path, hostname, remote, key)
		}

		// Known host with a different key: refuse. This is either a
		// reinstall or an attack, and guessing which is not our call.
		t.logger.Error("SSH host key has changed; refusing to connect",
			"client", client.Name,
			"host", hostname,
			"fingerprint", gossh.FingerprintSHA256(key))
		return err
	}, nil
}

// appendKnownHostLocked records a host key. The caller must hold t.mu.
func (t *Transport) appendKnownHostLocked(path, hostname string, remote net.Addr, key gossh.PublicKey) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening known_hosts for append: %w", err)
	}
	defer f.Close()

	// Record both the hostname and the remote address, matching what
	// OpenSSH writes, so a later connection by either matches.
	addresses := []string{hostname}
	if remote != nil && remote.String() != hostname {
		addresses = append(addresses, knownhosts.Normalize(remote.String()))
	}

	line := knownhosts.Line(addresses, key)
	if _, err := f.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("appending to known_hosts: %w", err)
	}
	return nil
}

func (t *Transport) knownHostsPath(client *engine.Client) string {
	if s := configString(client.TransportConfig, "known_hosts"); s != "" {
		return s
	}
	return t.cfg.KnownHosts
}

func (t *Transport) loadKey(client *engine.Client) (gossh.Signer, error) {
	path := t.cfg.KeyPath
	if s := configString(client.TransportConfig, "key_path"); s != "" {
		path = s
	}
	if path == "" {
		return nil, errors.New("ssh: no key_path configured (set transports.ssh.key_path)")
	}

	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading SSH key %q: %w", path, err)
	}

	passphrase := t.cfg.KeyPassphrase
	if s := configString(client.TransportConfig, "key_passphrase"); s != "" {
		passphrase = s
	}

	if passphrase != "" {
		signer, err := gossh.ParsePrivateKeyWithPassphrase(key, []byte(passphrase))
		if err != nil {
			return nil, fmt.Errorf("decrypting SSH key %q: %w", path, err)
		}
		return signer, nil
	}

	signer, err := gossh.ParsePrivateKey(key)
	if err != nil {
		var passErr *gossh.PassphraseMissingError
		if errors.As(err, &passErr) {
			return nil, fmt.Errorf(
				"SSH key %q is passphrase-protected; set key_passphrase or use an unencrypted key", path)
		}
		return nil, fmt.Errorf("parsing SSH key %q: %w", path, err)
	}
	return signer, nil
}

func (t *Transport) connectTimeout(client *engine.Client) time.Duration {
	if s := configString(client.TransportConfig, "connect_timeout"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			return d
		}
	}
	if t.cfg.ConnectTimeout != "" {
		if d, err := time.ParseDuration(t.cfg.ConnectTimeout); err == nil {
			return d
		}
	}
	return defaultConnectTTL
}

func (t *Transport) commandTimeout(client *engine.Client) time.Duration {
	if s := configString(client.TransportConfig, "command_timeout"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			return d
		}
	}
	if t.cfg.CommandTimeout != "" {
		if d, err := time.ParseDuration(t.cfg.CommandTimeout); err == nil {
			return d
		}
	}
	return defaultCommandTTL
}

// ensureFile creates an empty file and its parent directory if absent.
func ensureFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

func configString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	s, _ := cfg[key].(string)
	return strings.TrimSpace(s)
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
