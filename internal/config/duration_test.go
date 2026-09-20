package config

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"", 0, false},
		{"30s", 30 * time.Second, false},
		{"5m", 5 * time.Minute, false},
		{"1h30m", 90 * time.Minute, false},
		{"  45s  ", 45 * time.Second, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"0.5d", 12 * time.Hour, false},
		{"1d", 24 * time.Hour, false},

		// Rejections.
		{"nonsense", 0, true},
		{"5x", 0, true},
		{"d", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseDuration(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseDuration(%q) = %v, want an error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDuration(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ParseDuration(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestParseDurationRejectsTrailingGarbage is the regression test for the
// fmt.Sscanf-based day parser. Sscanf stops at the first character it cannot
// consume and still reports success, so "7dogs" parsed as seven days and
// "1dd" as one day.
func TestParseDurationRejectsTrailingGarbage(t *testing.T) {
	for _, input := range []string{"7dogs", "1dd", "3d4", "--5d", "1 2d"} {
		t.Run(input, func(t *testing.T) {
			if got, err := ParseDuration(input); err == nil {
				t.Errorf("ParseDuration(%q) = %v, want an error", input, got)
			}
		})
	}
}

// TestDurationHonoursExplicitZero is the regression test for defaults
// overriding a deliberate zero. `stagger: 0s` means "wake everything at
// once"; the previous code parsed it, compared the result against zero, and
// silently substituted the 45-second default.
func TestDurationHonoursExplicitZero(t *testing.T) {
	got, err := Duration("0s", 45*time.Second)
	if err != nil {
		t.Fatalf("Duration: %v", err)
	}
	if got != 0 {
		t.Errorf("Duration(%q, 45s) = %v, want 0 — an explicit zero was overridden by the default", "0s", got)
	}
}

func TestDurationFallsBackWhenAbsent(t *testing.T) {
	for _, input := range []string{"", "   "} {
		got, err := Duration(input, 45*time.Second)
		if err != nil {
			t.Fatalf("Duration(%q): %v", input, err)
		}
		if got != 45*time.Second {
			t.Errorf("Duration(%q, 45s) = %v, want the default", input, got)
		}
	}
}

func TestDurationReturnsDefaultAndErrorOnGarbage(t *testing.T) {
	got, err := Duration("not-a-duration", 45*time.Second)
	if err == nil {
		t.Error("Duration accepted an unparseable value without an error")
	}
	if got != 45*time.Second {
		t.Errorf("Duration returned %v on error, want the default so the caller can proceed", got)
	}
}

func TestDurationPassesThroughValidValues(t *testing.T) {
	got, err := Duration("90s", time.Minute)
	if err != nil {
		t.Fatalf("Duration: %v", err)
	}
	if got != 90*time.Second {
		t.Errorf("Duration = %v, want 90s", got)
	}
}
