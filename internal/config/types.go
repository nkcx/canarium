package config

import "time"

type Config struct {
	Canarium CanariumConfig `yaml:"canarium"`
	Sources  []SourceConfig `yaml:"sources"`
	Clients  []ClientConfig `yaml:"clients"`
	Plans    []PlanConfig   `yaml:"plans"`
}

type CanariumConfig struct {
	Mode           string `yaml:"mode"`
	Host           string `yaml:"host"`
	DataDir        string `yaml:"data_dir"`
	JournalRetain  string `yaml:"journal_retain"`
	ConfigReadonly bool   `yaml:"config_readonly"`
	Auth           AuthConfig `yaml:"auth"`
	Notifications  NotificationConfig `yaml:"notifications"`
}

type AuthConfig struct {
	PasswordHash string `yaml:"password_hash,omitempty"`
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
	Name            string         `yaml:"name"`
	Description     string         `yaml:"description,omitempty"`
	Transport       string         `yaml:"transport"`
	Address         string         `yaml:"address,omitempty"`
	MAC             string         `yaml:"mac,omitempty"`
	Credentials     string         `yaml:"credentials,omitempty"`
	Tags            []string       `yaml:"tags,omitempty"`
	Feeds           []string       `yaml:"feeds,omitempty"`
	FeedPolicy      string         `yaml:"feed_policy,omitempty"`
	ShutdownBudget  string         `yaml:"shutdown_budget,omitempty"`
	WakePolicy      string         `yaml:"wake_policy,omitempty"`
	GuardPeriod     string         `yaml:"guard_period,omitempty"`
	CommsLossAssumes string        `yaml:"comms_loss_assumes,omitempty"`
	DependsOn       []string       `yaml:"depends_on,omitempty"`
	After           []string       `yaml:"after,omitempty"`
	Before          []string       `yaml:"before,omitempty"`
	Probe           *ProbeConfig   `yaml:"probe,omitempty"`
	Wake            *WakeConfig    `yaml:"wake,omitempty"`
	Config          map[string]any `yaml:"config,omitempty"`
}

type ProbeConfig struct {
	Method  string `yaml:"method"`
	Port    int    `yaml:"port,omitempty"`
	Timeout string `yaml:"timeout,omitempty"`
}

type WakeConfig struct {
	Transport string `yaml:"transport"`
	MAC       string `yaml:"mac,omitempty"`
	Broadcast string `yaml:"broadcast,omitempty"`
	Config    map[string]any `yaml:"config,omitempty"`
}

type PlanConfig struct {
	Name         string           `yaml:"name"`
	Trigger      ConditionConfig  `yaml:"trigger"`
	Abort        *ConditionConfig `yaml:"abort,omitempty"`
	Shutdown     ShutdownConfig   `yaml:"shutdown"`
	Wake         WakeConfig_      `yaml:"wake"`
}

type ShutdownConfig struct {
	PostShutdown *PostShutdownConfig `yaml:"post_shutdown,omitempty"`
	Stages       []StageConfig       `yaml:"stages"`
}

type PostShutdownConfig struct {
	Action  string `yaml:"action"`
	Command string `yaml:"command"`
	Delay   int    `yaml:"delay"`
	UPS     string `yaml:"ups,omitempty"`
}

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
