package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var envVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	expanded := envVarRe.ReplaceAllStringFunc(string(data), func(match string) string {
		varName := match[2 : len(match)-1]
		if val, ok := os.LookupEnv(varName); ok {
			return val
		}
		return match
	})

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	applyDefaults(&cfg)
	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Canarium.Mode == "" {
		cfg.Canarium.Mode = "disarmed"
	}
	if cfg.Canarium.Host == "" {
		cfg.Canarium.Host = "0.0.0.0:8420"
	}
	if cfg.Canarium.DataDir == "" {
		cfg.Canarium.DataDir = "/var/lib/canarium"
	}
	if cfg.Canarium.JournalRetain == "" {
		cfg.Canarium.JournalRetain = "30d"
	}

	for i := range cfg.Clients {
		if cfg.Clients[i].ShutdownBudget == "" {
			cfg.Clients[i].ShutdownBudget = "3m"
		}
		if cfg.Clients[i].FeedPolicy == "" {
			cfg.Clients[i].FeedPolicy = "any"
		}
		if cfg.Clients[i].WakePolicy == "" {
			cfg.Clients[i].WakePolicy = "power_state"
		}
		if cfg.Clients[i].GuardPeriod == "" {
			cfg.Clients[i].GuardPeriod = "60s"
		}
	}

	for i := range cfg.Plans {
		w := &cfg.Plans[i].Wake
		if w.Stagger == "" {
			w.Stagger = "45s"
		}
		if w.Order == "" {
			w.Order = "reverse"
		}
		if w.ProbeInterval == "" {
			w.ProbeInterval = "30s"
		}
		if w.BootDeadline == "" {
			w.BootDeadline = "5m"
		}
		if w.Retries == 0 {
			w.Retries = 3
		}

		for j := range cfg.Plans[i].Shutdown.Stages {
			s := &cfg.Plans[i].Shutdown.Stages[j]
			if s.WaitTimeout == "" {
				s.WaitTimeout = "1h"
			}
			if s.WaitPolicy == "" {
				s.WaitPolicy = "skip"
			}
		}
	}
}

func ParseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}

	s = strings.TrimSpace(s)

	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	if strings.HasSuffix(s, "d") {
		numStr := strings.TrimSuffix(s, "d")
		var days float64
		if _, err := fmt.Sscanf(numStr, "%f", &days); err == nil {
			return time.Duration(days * 24 * float64(time.Hour)), nil
		}
	}

	return 0, fmt.Errorf("invalid duration: %q", s)
}
