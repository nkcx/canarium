package gpio

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/nkcx/canarium/internal/engine"
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

type PinConfig struct {
	Name         string `yaml:"name"`
	Chip         string `yaml:"chip"`
	Line         int    `yaml:"line"`
	Type         string `yaml:"type"`
	ActiveLow    bool   `yaml:"active_low"`
	PollInterval string `yaml:"poll_interval"`
	Description  string `yaml:"description"`
}

func NewSource(cfg Config, logger *slog.Logger) *Source {
	return &Source{
		logger: logger,
		pins:   cfg.Pins,
	}
}

func (s *Source) Name() string { return "gpio" }

func (s *Source) Declarations() []engine.SourceDeclaration {
	var decls []engine.SourceDeclaration
	for _, pin := range s.pins {
		poll, _ := time.ParseDuration(pin.PollInterval)
		if poll == 0 {
			poll = 5 * time.Second
		}

		factType := "bool"
		if pin.Type == "temperature" || pin.Type == "analog" {
			factType = "number"
		}

		decls = append(decls, engine.SourceDeclaration{
			InstanceName: "gpio",
			PollInterval: poll,
			Facts: []engine.FactDeclEntry{
				{
					Name:        pin.Name,
					Type:        factType,
					Description: pin.Description,
				},
			},
		})
	}
	return decls
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
	poll, _ := time.ParseDuration(pin.PollInterval)
	if poll == 0 {
		poll = 5 * time.Second
	}

	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			value, err := line.Value()
			if err != nil {
				s.logger.Error("GPIO read failed", "pin", pin.Name, "error", err)
				continue
			}

			var fact any
			switch pin.Type {
			case "digital":
				fact = value == 1
			case "analog":
				// Raw chardev lines return 0 or 1; true analog would need
				// an ADC. Expose the integer value for forward compatibility.
				fact = value
			default:
				fact = value == 1
			}

			updates <- engine.FactUpdate{
				Key:       "gpio." + pin.Name,
				Value:     fact,
				Timestamp: time.Now(),
			}
		}
	}
}
