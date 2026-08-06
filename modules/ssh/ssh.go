package ssh

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/nkcx/canarium/internal/engine"
	gossh "golang.org/x/crypto/ssh"
)

type Transport struct {
	keyPath string
	user    string
	command string
}

type Config struct {
	KeyPath string `yaml:"key_path"`
	User    string `yaml:"user"`
	Command string `yaml:"command"`
}

func New(cfg Config) *Transport {
	cmd := cfg.Command
	if cmd == "" {
		cmd = "shutdown -h now"
	}
	user := cfg.User
	if user == "" {
		user = "root"
	}
	return &Transport{
		keyPath: cfg.KeyPath,
		user:    user,
		command: cmd,
	}
}

func (t *Transport) Name() string { return "ssh" }

func (t *Transport) Capabilities() []engine.Capability {
	return []engine.Capability{
		{Action: engine.ActionShutdown, Idempotent: true, Timeout: 30 * time.Second},
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
	defer sshClient.Close()

	session, err := sshClient.NewSession()
	if err != nil {
		return nil, fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	cmd := t.command
	if cfg, ok := client.TransportConfig["command"]; ok {
		if s, ok := cfg.(string); ok {
			cmd = s
		}
	}

	err = session.Run(cmd)
	if err != nil {
		if exitErr, ok := err.(*gossh.ExitError); ok {
			if exitErr.ExitStatus() == 1 {
				return &engine.ActionResult{Success: true, Message: "shutdown initiated"}, nil
			}
		}
		return &engine.ActionResult{Success: false, Message: err.Error()}, nil
	}

	return &engine.ActionResult{Success: true, Message: "shutdown initiated"}, nil
}

func (t *Transport) Probe(ctx context.Context, client *engine.Client) (engine.ClientState, error) {
	port := client.ProbeConfig.Port
	if port == 0 {
		port = 22
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

func (t *Transport) connect(ctx context.Context, client *engine.Client) (*gossh.Client, error) {
	signer, err := loadKey(t.keyPath)
	if err != nil {
		return nil, err
	}

	user := t.user
	if u, ok := client.TransportConfig["user"]; ok {
		if s, ok := u.(string); ok {
			user = s
		}
	}

	config := &gossh.ClientConfig{
		User: user,
		Auth: []gossh.AuthMethod{
			gossh.PublicKeys(signer),
		},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	port := 22
	if client.ProbeConfig.Port != 0 {
		port = client.ProbeConfig.Port
	}
	addr := fmt.Sprintf("%s:%d", client.Address, port)

	return gossh.Dial("tcp", addr, config)
}

func loadKey(path string) (gossh.Signer, error) {
	if path == "" {
		home, _ := os.UserHomeDir()
		path = home + "/.ssh/id_ed25519"
	}

	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading SSH key %s: %w", path, err)
	}

	return gossh.ParsePrivateKey(key)
}
