package gpio

import (
	"context"
	"log/slog"
	"time"

	"github.com/nkcx/canarium/internal/engine"
)

type Source struct {
	logger *slog.Logger
	pins   []PinConfig
}

type Config struct {
	Pins []PinConfig `yaml:"pins"`
}

type PinConfig struct {
	Name        string `yaml:"name"`
	Chip        string `yaml:"chip"`
	Line        int    `yaml:"line"`
	Type        string `yaml:"type"`
	ActiveLow   bool   `yaml:"active_low"`
	PollInterval string `yaml:"poll_interval"`
	Description string `yaml:"description"`
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

func (s *Source) Start(ctx context.Context, updates chan<- engine.FactUpdate) error {
	s.logger.Info("GPIO source started", "pins", len(s.pins))

	for _, pin := range s.pins {
		go s.pollPin(ctx, pin, updates)
	}

	return nil
}

func (s *Source) Stop() error {
	return nil
}

func (s *Source) pollPin(ctx context.Context, pin PinConfig, updates chan<- engine.FactUpdate) {
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
			value, err := readPin(pin)
			if err != nil {
				s.logger.Error("GPIO read failed", "pin", pin.Name, "error", err)
				continue
			}
			updates <- engine.FactUpdate{
				Key:       "gpio." + pin.Name,
				Value:     value,
				Timestamp: time.Now(),
			}
		}
	}
}

func readPin(pin PinConfig) (any, error) {
	// libgpiod integration placeholder
	// In production, this would use:
	//   chip, _ := gpiod.NewChip(pin.Chip)
	//   line, _ := chip.RequestLine(pin.Line, gpiod.AsInput)
	//   value, _ := line.Value()
	return false, nil
}
