package config

import "time"

type Config struct {
	Canarium   CanariumConfig   `yaml:"canarium"`
	Transports TransportsConfig `yaml:"transports,omitempty"`
	Sources    []SourceConfig   `yaml:"sources"`
	Clients    []ClientConfig   `yaml:"clients"`
	Plans      []PlanConfig     `yaml:"plans"`
}

// TransportsConfig holds per-transport defaults, which individual clients
// override through their own transport_config block.
//
// These structs existed in the transport packages from the beginning but were
// always constructed empty — ssh.New(ssh.Config{}) — so none of their
// settings could be reached from a config file.
type TransportsConfig struct {
	SSH SSHTransportConfig `yaml:"ssh,omitempty"`
	WOL WOLTransportConfig `yaml:"wol,omitempty"`
}

// SSHTransportConfig configures the ssh transport.
type SSHTransportConfig struct {
	// User to authenticate as. Defaults to root.
	User string `yaml:"user,omitempty"`

	// Port to connect to. Defaults to 22. Distinct from probe.port.
	Port int `yaml:"port,omitempty"`

	// Command run to shut a host down. Defaults to "shutdown -h now".
	Command string `yaml:"command,omitempty"`

	// KeyPath is the private key used for authentication. Required unless
	// every client sets its own.
	KeyPath       string `yaml:"key_path,omitempty"`
	KeyPassphrase string `yaml:"key_passphrase,omitempty"`

	// KnownHosts is the file host keys are verified against. Defaults to
	// <data_dir>/known_hosts.
	KnownHosts string `yaml:"known_hosts,omitempty"`

	// HostKeyPolicy is one of "strict", "accept-new" or "insecure".
	// Defaults to accept-new: learn a key on first contact, then pin it.
	HostKeyPolicy string `yaml:"host_key_policy,omitempty"`

	ConnectTimeout string `yaml:"connect_timeout,omitempty"`
	CommandTimeout string `yaml:"command_timeout,omitempty"`
}

// WOLTransportConfig configures Wake-on-LAN packet emission.
type WOLTransportConfig struct {
	// RepeatCount is how many magic packets are sent per wake. Repeats
	// cover UDP loss on a network that is itself coming back up.
	RepeatCount int `yaml:"repeat_count,omitempty"`

	// RepeatDelay is the gap between repeats.
	RepeatDelay string `yaml:"repeat_delay,omitempty"`

	// Port is the UDP destination port. 9 (discard) is conventional; some
	// hardware listens on 7.
	Port int `yaml:"port,omitempty"`
}

type CanariumConfig struct {
	// Mode is the operating mode at startup: disarmed, dry-run or armed.
	Mode string `yaml:"mode"`

	// Host is the address the API listens on.
	Host string `yaml:"host"`

	// DataDir holds the state database and learned SSH host keys.
	DataDir string `yaml:"data_dir"`

	// JournalRetain is how long finished sequences are kept. Zero disables
	// pruning.
	JournalRetain string `yaml:"journal_retain"`

	// ConfigReadonly makes the configuration file authoritative for
	// settings it declares, refusing runtime changes to them through the
	// API.
	//
	// Intended for GitOps-style deployments where the file in version
	// control is the source of truth and a mode change made through the web
	// UI would be silently reverted by the next deploy — the kind of drift
	// that is only discovered during an outage.
	ConfigReadonly bool `yaml:"config_readonly"`

	Auth          AuthConfig         `yaml:"auth"`
	Notifications NotificationConfig `yaml:"notifications"`
}

// AuthConfig controls how the API authenticates callers.
type AuthConfig struct {
	// PasswordHash pins the admin password from the configuration file
	// rather than the database.
	//
	// When set, it takes precedence over any stored hash and the first-run
	// setup endpoint is disabled — the password is whatever the file says.
	// Intended for immutable deployments where the container's database is
	// ephemeral and a first-run setup screen on every restart would be both
	// an annoyance and a window of exposure.
	//
	// Generate one with: canarium hash-password
	PasswordHash string `yaml:"password_hash,omitempty"`

	// TrustProxyHeaders makes the server believe X-Forwarded-Proto and
	// X-Forwarded-Ssl when deciding whether to mark the session cookie
	// Secure. Enable this only when Canarium is reachable exclusively
	// through a reverse proxy that sets them; if the daemon can be reached
	// directly, a client can set these headers itself.
	TrustProxyHeaders bool `yaml:"trust_proxy_headers,omitempty"`

	// TrustedProxies lists the networks whose forwarding headers are
	// believed, in CIDR notation. Only consulted when TrustProxyHeaders is
	// set; defaults to loopback and the private ranges.
	//
	// The addresses here are the *proxy's*, not your clients'. Setting this
	// too broadly lets anyone within the range forge a source address,
	// evading rate limiting and locking out whoever they impersonate.
	TrustedProxies []string `yaml:"trusted_proxies,omitempty"`

	// AllowedOrigins lists additional origins permitted to open a WebSocket
	// connection. Same-origin requests are always allowed; this is only
	// needed when the UI is served from a different host than the API, such
	// as during frontend development against a remote daemon.
	AllowedOrigins []string `yaml:"allowed_origins,omitempty"`
}

type NotificationConfig struct {
	Webhooks []WebhookConfig `yaml:"webhooks,omitempty"`
}

type WebhookConfig struct {
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers,omitempty"`
	Events  []string          `yaml:"events,omitempty"`
}

type SourceConfig struct {
	Name         string         `yaml:"name"`
	Type         string         `yaml:"type"`
	PollInterval string         `yaml:"poll_interval,omitempty"`
	Config       map[string]any `yaml:"config,omitempty"`
}

type ClientConfig struct {
	Name             string         `yaml:"name"`
	Description      string         `yaml:"description,omitempty"`
	Transport        string         `yaml:"transport"`
	Address          string         `yaml:"address,omitempty"`
	MAC              string         `yaml:"mac,omitempty"`
	Credentials      string         `yaml:"credentials,omitempty"`
	Tags             []string       `yaml:"tags,omitempty"`
	Feeds            []string       `yaml:"feeds,omitempty"`
	FeedPolicy       string         `yaml:"feed_policy,omitempty"`
	ShutdownBudget   string         `yaml:"shutdown_budget,omitempty"`
	WakePolicy       string         `yaml:"wake_policy,omitempty"`
	GuardPeriod      string         `yaml:"guard_period,omitempty"`
	CommsLossAssumes string         `yaml:"comms_loss_assumes,omitempty"`
	DependsOn        []string       `yaml:"depends_on,omitempty"`
	After            []string       `yaml:"after,omitempty"`
	Before           []string       `yaml:"before,omitempty"`
	Probe            *ProbeConfig   `yaml:"probe,omitempty"`
	Wake             *WakeConfig    `yaml:"wake,omitempty"`
	Config           map[string]any `yaml:"config,omitempty"`
}

type ProbeConfig struct {
	Method  string `yaml:"method"`
	Port    int    `yaml:"port,omitempty"`
	Timeout string `yaml:"timeout,omitempty"`
}

type WakeConfig struct {
	Transport string         `yaml:"transport"`
	MAC       string         `yaml:"mac,omitempty"`
	Broadcast string         `yaml:"broadcast,omitempty"`
	Config    map[string]any `yaml:"config,omitempty"`
}

type PlanConfig struct {
	Name     string           `yaml:"name"`
	Trigger  ConditionConfig  `yaml:"trigger"`
	Abort    *ConditionConfig `yaml:"abort,omitempty"`
	Shutdown ShutdownConfig   `yaml:"shutdown"`
	Wake     WakeConfig_      `yaml:"wake"`
}

type ShutdownConfig struct {
	PostShutdown *PostShutdownConfig `yaml:"post_shutdown,omitempty"`
	Stages       []StageConfig       `yaml:"stages"`
}

// PostShutdownConfig describes an action taken once every stage has
// completed, typically instructing the UPS to cut its outlets so it does not
// drain the battery powering nothing.
type PostShutdownConfig struct {
	Action  string `yaml:"action"`
	Command string `yaml:"command"`
	Delay   int    `yaml:"delay"`

	// UPS is the NUT UPS name to command, as it appears in ups.conf.
	UPS string `yaml:"ups,omitempty"`

	// Host is the NUT server to connect to. Defaults to localhost.
	//
	// This is distinct from UPS: the executor previously passed the UPS name
	// as the connection address, so post-shutdown tried to resolve "ups" as
	// a hostname and could never reach the server.
	Host string `yaml:"host,omitempty"`
	Port int    `yaml:"port,omitempty"`

	// Credentials for the NUT server. Instant commands such as
	// shutdown.return always require authentication.
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
}

// comms_loss_assumes values, controlling how a client is treated when the
// UPS feeding it stops reporting.
const (
	// CommsLossSafe treats a client whose feed has gone unknown as not
	// threatened, so losing contact with a sensor never initiates a
	// shutdown. This is the default and the behaviour the README promises.
	CommsLossSafe = "safe"

	// CommsLossThreatened treats a client whose feed has gone unknown as
	// threatened, for operators who would rather shut down than risk
	// running on an unmonitored UPS.
	CommsLossThreatened = "threatened"
)

// Wait policies control what happens when a stage's entry condition does not
// hold before wait_timeout expires.
const (
	// WaitPolicySkip abandons the stage and moves to the next one. Its
	// clients are never shut down.
	WaitPolicySkip = "skip"

	// WaitPolicyEscalate is skip, plus a notification.
	WaitPolicyEscalate = "escalate"

	// WaitPolicyHold waits indefinitely for the condition, or for an
	// operator to force the stage through.
	WaitPolicyHold = "hold"
)

// ValidWaitPolicies lists every accepted wait_policy value.
var ValidWaitPolicies = []string{WaitPolicySkip, WaitPolicyEscalate, WaitPolicyHold}

type StageConfig struct {
	Name            string          `yaml:"name"`
	When            ConditionConfig `yaml:"when"`
	Clients         []string        `yaml:"clients"`
	Budget          string          `yaml:"budget,omitempty"`
	WaitTimeout     string          `yaml:"wait_timeout,omitempty"`
	WaitPolicy      string          `yaml:"wait_policy,omitempty"`
	PointOfNoReturn bool            `yaml:"point_of_no_return,omitempty"`
}

type WakeConfig_ struct {
	Gate          ConditionConfig `yaml:"gate"`
	Stagger       string          `yaml:"stagger,omitempty"`
	Order         string          `yaml:"order,omitempty"`
	Stages        []StageConfig   `yaml:"stages,omitempty"`
	ProbeInterval string          `yaml:"probe_interval,omitempty"`
	BootDeadline  string          `yaml:"boot_deadline,omitempty"`
	Retries       int             `yaml:"retries,omitempty"`
}

type ConditionConfig struct {
	Condition  string            `yaml:"condition,omitempty"`
	Fact       string            `yaml:"fact,omitempty"`
	Above      *float64          `yaml:"above,omitempty"`
	Below      *float64          `yaml:"below,omitempty"`
	Equals     any               `yaml:"equals,omitempty"`
	Is         string            `yaml:"is,omitempty"`
	IsNot      string            `yaml:"is_not,omitempty"`
	In         []string          `yaml:"in,omitempty"`
	Contains   string            `yaml:"contains,omitempty"`
	For        string            `yaml:"for,omitempty"`
	Value      string            `yaml:"value,omitempty"`
	Conditions []ConditionConfig `yaml:"conditions,omitempty"`
}

func DefaultCanariumConfig() CanariumConfig {
	return CanariumConfig{
		Mode:          "disarmed",
		Host:          "0.0.0.0:8420",
		DataDir:       "/var/lib/canarium",
		JournalRetain: "30d",
	}
}

func DefaultShutdownBudget() time.Duration {
	return 3 * time.Minute
}

func DefaultGuardPeriod() time.Duration {
	return 60 * time.Second
}

func DefaultWaitTimeout() time.Duration {
	return 1 * time.Hour
}

func DefaultStagger() time.Duration {
	return 45 * time.Second
}

func DefaultProbeInterval() time.Duration {
	return 30 * time.Second
}

func DefaultBootDeadline() time.Duration {
	return 5 * time.Minute
}

func DefaultRetries() int {
	return 3
}

// DefaultJournalRetain is how long finished sequences are kept.
func DefaultJournalRetain() time.Duration {
	return 30 * 24 * time.Hour
}
