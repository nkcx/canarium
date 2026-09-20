package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkcx/canarium/internal/engine"
	gossh "golang.org/x/crypto/ssh"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testHostKey returns a throwaway ed25519 SSH public key.
func testHostKey(t *testing.T) gossh.PublicKey {
	t.Helper()

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	key, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("wrapping key: %v", err)
	}
	return key
}

func testAddr(t *testing.T) net.Addr {
	t.Helper()
	addr, err := net.ResolveTCPAddr("tcp", "10.0.10.11:22")
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	return addr
}

func newTransport(t *testing.T, policy string) (*Transport, string) {
	t.Helper()

	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	return New(Config{
		KnownHosts:    knownHosts,
		HostKeyPolicy: policy,
		KeyPath:       "/dev/null", // not used by the callback tests
	}, discardLogger()), knownHosts
}

func testClient() *engine.Client {
	return &engine.Client{Name: "nas", Address: "10.0.10.11"}
}

// TestAcceptNewLearnsThenPins covers the default policy. The previous
// implementation used InsecureIgnoreHostKey unconditionally, so an on-path
// attacker could impersonate a host: the shutdown command went to the
// attacker, the real host stayed up, and Canarium reported success.
func TestAcceptNewLearnsThenPins(t *testing.T) {
	tr, knownHosts := newTransport(t, HostKeyAcceptNew)

	callback, err := tr.hostKeyCallback(testClient())
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}

	key := testHostKey(t)
	addr := testAddr(t)

	// First contact: the key is learned.
	if err := callback("10.0.10.11:22", addr, key); err != nil {
		t.Fatalf("first contact rejected: %v", err)
	}

	contents, err := os.ReadFile(knownHosts)
	if err != nil {
		t.Fatalf("reading known_hosts: %v", err)
	}
	if len(contents) == 0 {
		t.Fatal("known_hosts is empty; the key was not recorded")
	}
	if !strings.Contains(string(contents), "10.0.10.11") {
		t.Errorf("known_hosts does not name the host: %q", contents)
	}

	// A fresh callback (as after a restart) must now accept the same key
	// from the pinned file.
	callback2, err := tr.hostKeyCallback(testClient())
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}
	if err := callback2("10.0.10.11:22", addr, key); err != nil {
		t.Errorf("a previously learned key was rejected: %v", err)
	}
}

// TestAcceptNewRejectsChangedKey: a known host presenting a different key is
// either a reinstall or an attack, and guessing which is not ours to do.
func TestAcceptNewRejectsChangedKey(t *testing.T) {
	tr, _ := newTransport(t, HostKeyAcceptNew)

	callback, err := tr.hostKeyCallback(testClient())
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}

	addr := testAddr(t)
	original := testHostKey(t)
	if err := callback("10.0.10.11:22", addr, original); err != nil {
		t.Fatalf("first contact rejected: %v", err)
	}

	impostor := testHostKey(t)

	callback2, err := tr.hostKeyCallback(testClient())
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}
	if err := callback2("10.0.10.11:22", addr, impostor); err == nil {
		t.Error("a changed host key was accepted; an on-path attacker could " +
			"impersonate the host and the real machine would never shut down")
	}
}

// TestStrictRejectsUnknownHost: strict never learns.
func TestStrictRejectsUnknownHost(t *testing.T) {
	tr, _ := newTransport(t, HostKeyStrict)

	callback, err := tr.hostKeyCallback(testClient())
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}

	if err := callback("10.0.10.11:22", testAddr(t), testHostKey(t)); err == nil {
		t.Error("strict policy accepted an unknown host key")
	}
}

func TestStrictAcceptsPinnedHost(t *testing.T) {
	tr, knownHosts := newTransport(t, HostKeyStrict)

	key := testHostKey(t)
	line := "10.0.10.11:22 " + key.Type() + " " +
		strings.TrimSpace(strings.SplitN(string(gossh.MarshalAuthorizedKey(key)), " ", 3)[1])
	if err := os.WriteFile(knownHosts, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("writing known_hosts: %v", err)
	}

	callback, err := tr.hostKeyCallback(testClient())
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}
	if err := callback("10.0.10.11:22", testAddr(t), key); err != nil {
		t.Errorf("strict policy rejected a pinned key: %v", err)
	}
}

func TestInsecurePolicyAcceptsAnything(t *testing.T) {
	tr, _ := newTransport(t, HostKeyInsecure)

	callback, err := tr.hostKeyCallback(testClient())
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}
	if err := callback("10.0.10.11:22", testAddr(t), testHostKey(t)); err != nil {
		t.Errorf("insecure policy rejected a key: %v", err)
	}
}

func TestPerClientPolicyOverride(t *testing.T) {
	tr, _ := newTransport(t, HostKeyStrict)

	client := testClient()
	client.TransportConfig = map[string]any{"host_key_policy": HostKeyInsecure}

	callback, err := tr.hostKeyCallback(client)
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}
	if err := callback("10.0.10.11:22", testAddr(t), testHostKey(t)); err != nil {
		t.Errorf("per-client override was ignored: %v", err)
	}
}

// TestProbePortIsSeparateFromSSHPort is the regression test for connect()
// reading client.ProbeConfig.Port. An operator setting probe.port to check a
// different service silently redirected SSH there too.
func TestProbePortIsSeparateFromSSHPort(t *testing.T) {
	tr := New(Config{Port: 22}, discardLogger())

	client := testClient()
	client.ProbeConfig.Port = 443

	if got := tr.sshPort(client); got != 22 {
		t.Errorf("sshPort = %d, want 22 — probe.port must not redirect SSH", got)
	}
	if got := tr.probePort(client); got != 443 {
		t.Errorf("probePort = %d, want 443", got)
	}
}

func TestSSHPortPrecedence(t *testing.T) {
	tr := New(Config{Port: 2200}, discardLogger())

	client := testClient()
	if got := tr.sshPort(client); got != 2200 {
		t.Errorf("sshPort = %d, want the transport default 2200", got)
	}

	client.TransportConfig = map[string]any{"port": 2222}
	if got := tr.sshPort(client); got != 2222 {
		t.Errorf("sshPort = %d, want the per-client override 2222", got)
	}
}

// TestClassifyExitStatusOne is the regression test for treating exit 1 as
// success. sudo returns 1 on an authentication failure, so a permission
// problem was reported as "shutdown initiated" and the executor moved on
// believing the host was going down.
func TestClassifyExitStatusOne(t *testing.T) {
	tr := New(Config{}, discardLogger())

	result, err := tr.classify(testClient(), "shutdown -h now", &gossh.ExitError{
		Waitmsg: gossh.Waitmsg{},
	})
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if result.Success {
		t.Error("a non-zero exit status was reported as a successful shutdown")
	}
}

func TestClassifyCleanExit(t *testing.T) {
	tr := New(Config{}, discardLogger())

	result, err := tr.classify(testClient(), "shutdown -h now", nil)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if !result.Success {
		t.Error("a clean exit was not reported as success")
	}
}

// TestClassifyConnectionDroppedIsSuccess: a host that goes away mid-command
// is a host that is shutting down, which is what we asked for.
func TestClassifyConnectionDroppedIsSuccess(t *testing.T) {
	tr := New(Config{}, discardLogger())

	result, err := tr.classify(testClient(), "shutdown -h now", &gossh.ExitMissingError{})
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if !result.Success {
		t.Error("a connection dropped mid-shutdown was reported as a failure")
	}
}

func TestLoadKeyRequiresAPath(t *testing.T) {
	tr := New(Config{}, discardLogger())

	_, err := tr.loadKey(testClient())
	if err == nil {
		t.Fatal("loadKey succeeded with no key_path configured")
	}
	if !strings.Contains(err.Error(), "key_path") {
		t.Errorf("error does not mention key_path: %v", err)
	}
}

func TestLoadKeyReportsMissingFile(t *testing.T) {
	tr := New(Config{KeyPath: "/nonexistent/id_ed25519"}, discardLogger())

	if _, err := tr.loadKey(testClient()); err == nil {
		t.Error("loadKey succeeded with a missing key file")
	}
}

func TestEnsureFileCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "known_hosts")

	if err := ensureFile(path); err != nil {
		t.Fatalf("ensureFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("known_hosts mode = %o, want 600", info.Mode().Perm())
	}

	// Must be idempotent.
	if err := ensureFile(path); err != nil {
		t.Errorf("second ensureFile: %v", err)
	}
}

func TestHostKeyCallbackRequiresKnownHostsPath(t *testing.T) {
	tr := New(Config{HostKeyPolicy: HostKeyStrict}, discardLogger())

	_, err := tr.hostKeyCallback(testClient())
	if err == nil {
		t.Fatal("hostKeyCallback succeeded with no known_hosts path")
	}
	if !errors.Is(err, err) || !strings.Contains(err.Error(), "known_hosts") {
		t.Errorf("error does not mention known_hosts: %v", err)
	}
}

func TestExecuteRejectsUnsupportedAction(t *testing.T) {
	tr := New(Config{}, discardLogger())

	if _, err := tr.Execute(t.Context(), testClient(), engine.ActionWake); err == nil {
		t.Error("ssh transport accepted a wake action")
	}
}
