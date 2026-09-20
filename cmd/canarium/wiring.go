package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/facts"
	execmod "github.com/nkcx/canarium/modules/exec"
	gpiomod "github.com/nkcx/canarium/modules/gpio"
	nutmod "github.com/nkcx/canarium/modules/nut"
	"github.com/nkcx/canarium/modules/opnsense"
	"github.com/nkcx/canarium/modules/proxmox"
	restmod "github.com/nkcx/canarium/modules/rest"
	snmpmod "github.com/nkcx/canarium/modules/snmp"
	"github.com/nkcx/canarium/modules/ssh"
	"github.com/nkcx/canarium/modules/truenas"
	"github.com/nkcx/canarium/modules/wol"
	"gopkg.in/yaml.v3"
)

// Source type names accepted in the sources block.
const (
	sourceTypeNUT  = "nut"
	sourceTypeSNMP = "snmp"
	sourceTypeGPIO = "gpio"
)

// sourceUpdateBuffer is how many fact updates may queue between a source and
// the store before producers block.
const sourceUpdateBuffer = 128

// buildTransports constructs every transport the daemon supports.
func buildTransports(cfg *config.Config, logger *slog.Logger) map[string]engine.Transport {
	return map[string]engine.Transport{
		"ssh": ssh.New(ssh.Config{
			User:           cfg.Transports.SSH.User,
			Port:           cfg.Transports.SSH.Port,
			Command:        cfg.Transports.SSH.Command,
			KeyPath:        cfg.Transports.SSH.KeyPath,
			KeyPassphrase:  cfg.Transports.SSH.KeyPassphrase,
			KnownHosts:     cfg.Transports.SSH.KnownHosts,
			HostKeyPolicy:  cfg.Transports.SSH.HostKeyPolicy,
			ConnectTimeout: cfg.Transports.SSH.ConnectTimeout,
			CommandTimeout: cfg.Transports.SSH.CommandTimeout,
		}, logger),
		"wol": wol.New(wol.Config{
			RepeatCount: cfg.Transports.WOL.RepeatCount,
			RepeatDelay: cfg.Transports.WOL.RepeatDelay,
			Port:        cfg.Transports.WOL.Port,
		}, logger),
		"exec":     execmod.New(),
		"rest":     restmod.New(),
		"proxmox":  proxmox.New(logger),
		"truenas":  truenas.New(logger),
		"opnsense": opnsense.New(logger),
		"nut":      nutmod.NewTransport(logger),
		"snmp-poe": snmpmod.NewPoeTransport(logger),
	}
}

func registerTransports(executor *engine.Executor, cfg *config.Config, logger *slog.Logger) {
	for name, t := range buildTransports(cfg, logger) {
		executor.RegisterTransport(name, t)
	}
}

// transportNames returns the registered transport names, sorted.
func transportNames(cfg *config.Config, logger *slog.Logger) []string {
	transports := buildTransports(cfg, logger)
	names := make([]string, 0, len(transports))
	for name := range transports {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// sourceTypes returns the source types the daemon can construct.
func sourceTypes() []string {
	return []string{sourceTypeGPIO, sourceTypeNUT, sourceTypeSNMP}
}

// decodeSourceConfig converts a source's free-form config block into a typed
// struct.
//
// Round-tripping through YAML replaces about ninety lines of hand-written map
// plucking, which had to be extended by hand for every new field and silently
// dropped anything it did not know about — which is how the NUT username and
// password came to be documented in the example config but never read.
func decodeSourceConfig(raw map[string]any, out any) error {
	encoded, err := yaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("re-encoding source config: %w", err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(encoded))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decoding source config: %w", err)
	}
	return nil
}

// SourceManager owns the lifetime of every configured source.
//
// Sources were previously started with context.Background() and their Stop
// methods were never called, so they outlived the daemon's shutdown: polling
// goroutines and their fact-forwarding partners kept running, and the update
// channels were never closed.
type SourceManager struct {
	sources []engine.Source
	logger  *slog.Logger

	updates chan engine.FactUpdate
	wg      sync.WaitGroup
	cancel  context.CancelFunc
}

// NewSourceManager builds every source declared in the config and registers
// their fact declarations with the store.
//
// An unrecognised source type is an error. registerSources previously had no
// default case, so a config declaring a GPIO flood sensor started cleanly and
// simply never produced the fact its plans depended on.
func NewSourceManager(cfg *config.Config, store *facts.Store, logger *slog.Logger) (*SourceManager, error) {
	m := &SourceManager{
		logger:  logger,
		updates: make(chan engine.FactUpdate, sourceUpdateBuffer),
	}

	for _, src := range cfg.Sources {
		source, err := buildSource(src, store, logger)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", src.Name, err)
		}
		m.sources = append(m.sources, source)
	}

	return m, nil
}

func buildSource(src config.SourceConfig, store *facts.Store, logger *slog.Logger) (engine.Source, error) {
	switch src.Type {
	case sourceTypeNUT:
		var c nutmod.Config
		if err := decodeSourceConfig(src.Config, &c); err != nil {
			return nil, err
		}
		if len(c.Instances) == 0 {
			return nil, fmt.Errorf("no instances configured")
		}
		nutmod.RegisterFacts(store, c, logger)
		return nutmod.NewSource(c, logger), nil

	case sourceTypeSNMP:
		var c snmpmod.Config
		if err := decodeSourceConfig(src.Config, &c); err != nil {
			return nil, err
		}
		if len(c.Instances) == 0 {
			return nil, fmt.Errorf("no instances configured")
		}
		snmpmod.RegisterFacts(store, c, logger)
		return snmpmod.NewSource(c, logger), nil

	case sourceTypeGPIO:
		// The gpio module has existed since the first commit and was listed
		// in the README as a core module, but was never wired up here: a
		// `type: gpio` source was silently ignored.
		var c gpiomod.Config
		if err := decodeSourceConfig(src.Config, &c); err != nil {
			return nil, err
		}
		if len(c.Pins) == 0 {
			return nil, fmt.Errorf("no pins configured")
		}
		gpiomod.RegisterFacts(store, c, logger)
		return gpiomod.NewSource(c, logger), nil

	default:
		return nil, fmt.Errorf("unknown source type %q (available: %v)", src.Type, sourceTypes())
	}
}

// Start begins polling and forwards updates into the fact store.
func (m *SourceManager) Start(ctx context.Context, store *facts.Store) error {
	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	for _, source := range m.sources {
		if err := source.Start(ctx, m.updates); err != nil {
			cancel()
			return fmt.Errorf("starting source %s: %w", source.Name(), err)
		}
	}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case update := <-m.updates:
				store.Update(update.Key, update.Value, update.Timestamp)
			}
		}
	}()

	return nil
}

// Stop halts polling and waits for the forwarding goroutine to finish.
func (m *SourceManager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}

	for _, source := range m.sources {
		if err := source.Stop(); err != nil {
			m.logger.Error("stopping source", "source", source.Name(), "error", err)
		}
	}

	m.wg.Wait()
}

// buildRegistry describes what this daemon supports, for validation.
func buildRegistry(cfg *config.Config, store *facts.Store, logger *slog.Logger) *config.Registry {
	declared := store.AllDeclarations()
	factKeys := make([]string, 0, len(declared))
	for key := range declared {
		factKeys = append(factKeys, key)
	}
	sort.Strings(factKeys)

	// Conditions may reference facts Canarium derives itself, which have no
	// source declaring them.
	factKeys = append(factKeys, engine.DerivedFactKeys(cfg)...)
	sort.Strings(factKeys)

	return &config.Registry{
		Transports:  transportNames(cfg, logger),
		SourceTypes: sourceTypes(),
		Facts:       factKeys,
	}
}
