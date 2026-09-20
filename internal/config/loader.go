package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var envVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// LoadOptions controls how a configuration file is read.
type LoadOptions struct {
	// AllowMissingEnv substitutes a placeholder for unset ${VAR} references
	// instead of failing, and reports them in MissingEnv.
	//
	// The daemon must not do this: running with a placeholder where a
	// credential should be means every transport fails to authenticate
	// during an outage. `canarium validate` does, because checking a
	// config's structure in CI should not require production secrets.
	AllowMissingEnv bool
}

// LoadResult carries a parsed config plus anything noteworthy about how it
// was loaded.
type LoadResult struct {
	Config *Config

	// MissingEnv lists environment variables the file references that were
	// not set. Non-empty only when AllowMissingEnv is true.
	MissingEnv []string
}

// Load reads, expands and parses a configuration file, requiring every
// referenced environment variable to be set.
func Load(path string) (*Config, error) {
	res, err := LoadWith(path, LoadOptions{})
	if err != nil {
		return nil, err
	}
	return res.Config, nil
}

// LoadWith reads, expands and parses a configuration file.
func LoadWith(path string, opts LoadOptions) (*LoadResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	expanded, missing := expandEnv(string(data), opts.AllowMissingEnv)
	if len(missing) > 0 && !opts.AllowMissingEnv {
		// Previously an unresolved ${VAR} was left in place as a literal, so
		// a credential silently became the nine-character string
		// "${TOKEN}" and every request using it failed authentication with
		// no indication why. The compose file shipped without passing the
		// environment through at all, so this was the default experience.
		return nil, fmt.Errorf(
			"config references environment variables that are not set: %s\n"+
				"Set them in the environment, or via env_file in compose.yaml",
			strings.Join(missing, ", "))
	}

	cfg, err := parseStrict([]byte(expanded))
	if err != nil {
		return nil, err
	}

	applyDefaults(cfg)
	return &LoadResult{Config: cfg, MissingEnv: missing}, nil
}

// parseStrict decodes YAML, rejecting keys the schema does not define.
//
// Without KnownFields, a misspelled key is silently discarded: writing
// `point_of_no_retrun: true` produced a stage with no point of no return and
// no complaint, and `wake_policy: retain-state` (hyphen, not underscore)
// produced a client that was woken when it should not have been. For a tool
// whose config decides whether machines get shut down, a typo must be an
// error, not a default.
func parseStrict(data []byte) (*Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("config file is empty")
		}
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// A second document would be silently ignored otherwise.
	var extra Config
	if err := dec.Decode(&extra); err == nil {
		return nil, fmt.Errorf("config contains more than one YAML document; " +
			"Canarium reads a single document")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return &cfg, nil
}

// expandEnv substitutes ${VAR} references, reporting any that are unset.
//
// Substitution happens on the raw text before parsing, which is what makes
// it work inside any YAML value. The cost is that a value containing a
// newline or a quote can alter the document's structure, so values are
// checked for characters that would do so.
func expandEnv(raw string, placeholderForMissing bool) (expanded string, missing []string) {
	seen := make(map[string]bool)

	expanded = envVarRe.ReplaceAllStringFunc(raw, func(match string) string {
		name := match[2 : len(match)-1]

		val, ok := os.LookupEnv(name)
		if !ok {
			if !seen[name] {
				seen[name] = true
				missing = append(missing, name)
			}
			if placeholderForMissing {
				// Something structurally valid, so the rest of the file
				// still parses and can be checked.
				return missingEnvPlaceholder
			}
			return match
		}

		return yamlSafe(val)
	})

	return expanded, missing
}

// missingEnvPlaceholder stands in for an unset variable during validation.
const missingEnvPlaceholder = "unset-during-validation"

// yamlSafe renders a substituted value so it cannot change the document's
// structure.
//
// A secret containing a newline, a colon or a quote would otherwise be
// spliced into the YAML as syntax. Wrapping anything suspicious in single
// quotes — with embedded single quotes doubled, per the YAML spec — keeps it
// a scalar.
func yamlSafe(val string) string {
	if !strings.ContainsAny(val, "\n\r\t\"':#{}[],&*?|<>=!%@`") {
		return val
	}
	return "'" + strings.ReplaceAll(val, "'", "''") + "'"
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

	if cfg.Transports.SSH.KnownHosts == "" {
		// Keep learned host keys alongside the rest of the daemon's state,
		// so they survive restarts and container recreation.
		cfg.Transports.SSH.KnownHosts = filepath.Join(cfg.Canarium.DataDir, "known_hosts")
	}
	if cfg.Transports.SSH.HostKeyPolicy == "" {
		cfg.Transports.SSH.HostKeyPolicy = "accept-new"
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
