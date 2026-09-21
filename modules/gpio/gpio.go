package gpio

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/facts"
	"github.com/warthog618/go-gpiocdev"
)

type Source struct {
	logger *slog.Logger
	pins   []PinConfig

	mu    sync.Mutex
	lines []*gpiocdev.Line // opened lines, closed in Stop()
}

type Config struct {
	Pins []PinConfig `yaml:"pins"`
}

// instanceName is the fact-key prefix for every GPIO pin.
const instanceName = "gpio"

const defaultPollInterval = 5 * time.Second

type PinConfig struct {
	Name         string `yaml:"name"`
	Chip         string `yaml:"chip"`
	Line         int    `yaml:"line"`
	Type         string `yaml:"type"`
	ActiveLow    bool   `yaml:"active_low"`
	PollInterval string `yaml:"poll_interval"`
	Description  string `yaml:"description"`
}

// pollInterval returns the pin's configured poll interval, or the default.
func (p PinConfig) pollInterval() time.Duration {
	if p.PollInterval == "" {
		return defaultPollInterval
	}
	d, err := time.ParseDuration(p.PollInterval)
	if err != nil || d <= 0 {
		return defaultPollInterval
	}
	return d
}

func NewSource(cfg Config, logger *slog.Logger) *Source {
	return &Source{
		logger: logger,
		pins:   cfg.Pins,
	}
}

func (s *Source) Name() string { return "gpio" }

// Declarations returns one declaration covering every configured pin.
//
// All GPIO facts share the "gpio" instance name, and the fact store keys its
// staleness window by instance. Emitting one declaration per pin therefore
// had each pin overwrite the previous pin's poll interval, so every fact was
// judged fresh or stale against whichever pin happened to be configured
// last. The declaration now carries the shortest configured interval, which
// is the only value under which no pin is wrongly considered fresh.
func (s *Source) Declarations() []engine.SourceDeclaration {
	if len(s.pins) == 0 {
		return nil
	}

	entries := make([]engine.FactDeclEntry, 0, len(s.pins))
	shortest := time.Duration(0)

	for _, pin := range s.pins {
		poll := pin.pollInterval()
		if shortest == 0 || poll < shortest {
			shortest = poll
		}

		factType := "bool"
		if pin.Type == "temperature" || pin.Type == "analog" {
			factType = "number"
		}

		entries = append(entries, engine.FactDeclEntry{
			Name:        pin.Name,
			Type:        factType,
			Description: pin.Description,
		})
	}

	return []engine.SourceDeclaration{{
		InstanceName: instanceName,
		PollInterval: shortest,
		Facts:        entries,
	}}
}

// RegisterFacts pre-registers the pins' fact declarations with the store.
func RegisterFacts(store *facts.Store, cfg Config, logger *slog.Logger) {
	src := NewSource(cfg, logger)
	for _, decl := range src.Declarations() {
		factDecls := make([]facts.FactDeclaration, 0, len(decl.Facts))
		for _, f := range decl.Facts {
			factDecls = append(factDecls, facts.FactDeclaration{
				Name:        f.Name,
				Type:        f.Type,
				Description: f.Description,
				Unit:        f.Unit,
			})
		}
		store.RegisterSource(decl.InstanceName, decl.PollInterval, factDecls)
	}
}

// chipDevicePath returns the device path for a chip name.
func chipDevicePath(chip string) string {
	return "/dev/" + chip
}

// chipAvailable checks whether the GPIO chip device node exists.
func chipAvailable(chip string) bool {
	_, err := os.Stat(chipDevicePath(chip))
	return err == nil
}

func (s *Source) Start(ctx context.Context, updates chan<- engine.FactUpdate) error {
	s.logger.Info("GPIO source starting", "pins", len(s.pins))

	for _, pin := range s.pins {
		if !chipAvailable(pin.Chip) {
			s.logger.Warn("GPIO chip device not found, skipping pin",
				"chip", pin.Chip,
				"pin", pin.Name,
				"path", chipDevicePath(pin.Chip),
			)
			continue
		}

		// 1-Wire temperature sensors (e.g. DS18B20) are not accessed via the
		// GPIO chardev interface. They use the kernel 1-Wire driver exposed at
		// /sys/bus/w1/devices/<id>/temperature (or w1_slave). A "temperature"
		// pin type is declared in the schema for future use but currently only
		// digital reads are implemented here.
		if pin.Type == "temperature" {
			s.logger.Warn("temperature pin type not yet implemented via GPIO chardev; "+
				"1-Wire sensors should use /sys/bus/w1 interface, skipping pin",
				"pin", pin.Name,
			)
			continue
		}

		// Build line request options.
		opts := []gpiocdev.LineReqOption{
			gpiocdev.AsInput,
			gpiocdev.WithConsumer("canarium"),
		}
		if pin.ActiveLow {
			opts = append(opts, gpiocdev.AsActiveLow)
		}

		line, err := gpiocdev.RequestLine(pin.Chip, pin.Line, opts...)
		if err != nil {
			s.logger.Error("failed to request GPIO line, skipping pin",
				"chip", pin.Chip,
				"line", pin.Line,
				"pin", pin.Name,
				"error", err,
			)
			continue
		}

		s.mu.Lock()
		s.lines = append(s.lines, line)
		s.mu.Unlock()

		go s.pollLine(ctx, pin, line, updates)
	}

	s.logger.Info("GPIO source started")
	return nil
}

func (s *Source) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var firstErr error
	for _, line := range s.lines {
		if err := line.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.lines = nil

	if firstErr != nil {
		return fmt.Errorf("gpio: closing lines: %w", firstErr)
	}
	s.logger.Info("GPIO source stopped")
	return nil
}

func (s *Source) pollLine(ctx context.Context, pin PinConfig, line *gpiocdev.Line, updates chan<- engine.FactUpdate) {
	ticker := time.NewTicker(pin.pollInterval())
	defer ticker.Stop()

	// Read once immediately, as the NUT and SNMP sources do. Waiting for the
	// first tick left GPIO facts absent from the store for a whole poll
	// interval after startup — up to a minute for a flood or door sensor —
	// during which every condition reading them evaluated as unavailable.
	s.readAndEmit(ctx, pin, line, updates)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !s.readAndEmit(ctx, pin, line, updates) {
				return
			}
		}
	}
}

// readAndEmit takes one reading and publishes it, reporting whether polling
// should continue.
func (s *Source) readAndEmit(ctx context.Context, pin PinConfig, line *gpiocdev.Line, updates chan<- engine.FactUpdate) bool {
	value, err := line.Value()
	if err != nil {
		s.logger.Error("GPIO read failed", "pin", pin.Name, "error", err)
		return true
	}

	var fact any
	switch pin.Type {
	case "analog":
		// Raw chardev lines return 0 or 1; true analog would need an ADC.
		// Expose the integer for forward compatibility.
		fact = value
	default:
		fact = value == 1
	}

	select {
	case updates <- engine.FactUpdate{
		Key:       instanceName + "." + pin.Name,
		Value:     fact,
		Timestamp: time.Now(),
	}:
		return true
	case <-ctx.Done():
		return false
	}
}
