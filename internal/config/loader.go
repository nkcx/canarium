package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
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

// Duration resolves a configured duration, returning def when the value is
// absent.
//
// An explicitly configured zero is honoured. `stagger: 0s` means "wake
// everything at once", not "use the 45-second default" — but code that
// parsed the string and then tested the result against zero could not tell
// an explicit "0s" from an unset field, so an operator asking for no stagger
// silently got 45 seconds between every host. The same pattern affected
// budgets, guard periods, wait timeouts and probe intervals.
//
// An unparseable value returns def along with the error, so callers can log
// the problem and still proceed with a sane value.
func Duration(value string, def time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return def, nil
	}

	d, err := ParseDuration(value)
	if err != nil {
		return def, err
	}
	return d, nil
}

// ParseDuration parses a duration string, additionally accepting a "d"
// (days) suffix that time.ParseDuration does not.
func ParseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}

	s = strings.TrimSpace(s)

	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	// time.ParseDuration has no unit larger than an hour, but journal
	// retention is naturally expressed in days.
	if numStr, ok := strings.CutSuffix(s, "d"); ok {
		// strconv, not fmt.Sscanf: Sscanf stops at the first character it
		// cannot consume and reports success, so "7dogs" parsed as 7 days.
		days, err := strconv.ParseFloat(numStr, 64)
		if err == nil {
			return time.Duration(days * 24 * float64(time.Hour)), nil
		}
	}

	return 0, fmt.Errorf("invalid duration: %q", s)
}
