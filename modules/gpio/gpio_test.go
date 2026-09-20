package gpio

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/facts"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPinPollIntervalDefaults(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"unset", "", defaultPollInterval},
		{"valid", "2s", 2 * time.Second},
		{"invalid", "not-a-duration", defaultPollInterval},
		{"zero", "0s", defaultPollInterval},
		{"negative", "-5s", defaultPollInterval},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := PinConfig{PollInterval: tt.value}
			if got := p.pollInterval(); got != tt.want {
				t.Errorf("pollInterval(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

// TestDeclarationsUseTheShortestInterval is the regression test for emitting
// one declaration per pin, all under the "gpio" instance name. The fact
// store keys staleness by instance, so each pin overwrote the previous
// pin's interval and every fact was judged against whichever was last.
func TestDeclarationsUseTheShortestInterval(t *testing.T) {
	src := NewSource(Config{Pins: []PinConfig{
		{Name: "flood", Chip: "gpiochip0", Line: 17, Type: "digital", PollInterval: "30s"},
		{Name: "door", Chip: "gpiochip0", Line: 18, Type: "digital", PollInterval: "2s"},
		{Name: "smoke", Chip: "gpiochip0", Line: 19, Type: "digital", PollInterval: "10s"},
	}}, discardLogger())

	decls := src.Declarations()
	if len(decls) != 1 {
		t.Fatalf("emitted %d declarations, want 1 covering every pin", len(decls))
	}
	if decls[0].InstanceName != instanceName {
		t.Errorf("InstanceName = %q, want %q", decls[0].InstanceName, instanceName)
	}
	if decls[0].PollInterval != 2*time.Second {
		t.Errorf("PollInterval = %v, want the shortest configured (2s)", decls[0].PollInterval)
	}
	if len(decls[0].Facts) != 3 {
		t.Errorf("declared %d facts, want 3", len(decls[0].Facts))
	}
}

func TestDeclarationsWithNoPins(t *testing.T) {
	src := NewSource(Config{}, discardLogger())
	if decls := src.Declarations(); len(decls) != 0 {
		t.Errorf("emitted %d declarations with no pins configured", len(decls))
	}
}

func TestFactTypes(t *testing.T) {
	src := NewSource(Config{Pins: []PinConfig{
		{Name: "flood", Type: "digital"},
		{Name: "temp", Type: "temperature"},
		{Name: "level", Type: "analog"},
		{Name: "unspecified"},
	}}, discardLogger())

	decls := src.Declarations()
	if len(decls) != 1 {
		t.Fatalf("emitted %d declarations, want 1", len(decls))
	}

	want := map[string]string{
		"flood":       "bool",
		"temp":        "number",
		"level":       "number",
		"unspecified": "bool",
	}
	for _, f := range decls[0].Facts {
		if got := want[f.Name]; got != f.Type {
			t.Errorf("fact %q type = %q, want %q", f.Name, f.Type, got)
		}
	}
}

func TestRegisterFactsPopulatesTheStore(t *testing.T) {
	store := facts.NewStore()

	RegisterFacts(store, Config{Pins: []PinConfig{
		{Name: "flood", Chip: "gpiochip0", Line: 17, Type: "digital", Description: "Floor sensor"},
	}}, discardLogger())

	decl := store.GetDeclaration("gpio.flood")
	if decl == nil {
		t.Fatal("gpio.flood was not registered with the store")
	}
	if decl.Type != "bool" {
		t.Errorf("type = %q, want bool", decl.Type)
	}
	if decl.Description != "Floor sensor" {
		t.Errorf("description = %q, want the configured one", decl.Description)
	}
}

// TestStartSkipsUnavailableChips: GPIO is optional hardware, so a config
// naming a chip that is not present must not prevent the daemon starting.
func TestStartSkipsUnavailableChips(t *testing.T) {
	src := NewSource(Config{Pins: []PinConfig{
		{Name: "flood", Chip: "gpiochip-does-not-exist", Line: 17, Type: "digital"},
	}}, discardLogger())

	updates := make(chan any, 1)
	_ = updates

	if err := src.Start(t.Context(), nil); err != nil {
		t.Errorf("Start returned an error for a missing chip: %v", err)
	}
	if err := src.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

func TestChipDevicePath(t *testing.T) {
	if got := chipDevicePath("gpiochip0"); got != "/dev/gpiochip0" {
		t.Errorf("chipDevicePath = %q, want /dev/gpiochip0", got)
	}
}

func TestSourceName(t *testing.T) {
	if got := NewSource(Config{}, discardLogger()).Name(); got != "gpio" {
		t.Errorf("Name() = %q, want gpio", got)
	}
}
