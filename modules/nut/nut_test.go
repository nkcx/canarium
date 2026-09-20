package nut

import (
	"strings"
	"testing"
	"time"
)

// TestParseVarLine covers NUT's quoting rules. The previous implementation
// used strings.Trim(value, `"`), which strips any number of quotes from both
// ends and leaves backslash escapes in place.
func TestParseVarLine(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantName  string
		wantValue string
		wantOK    bool
	}{
		{"simple", `battery.charge "100"`, "battery.charge", "100", true},
		{"spaces in value", `ups.status "OB LB"`, "ups.status", "OB LB", true},
		{"empty value", `ups.test.result ""`, "ups.test.result", "", true},
		{"escaped quote", `device.model "The \"Big\" One"`, "device.model", `The "Big" One`, true},
		{"escaped backslash", `device.path "C:\\ups"`, "device.path", `C:\ups`, true},
		{"unquoted", `battery.charge 100`, "battery.charge", "100", true},
		{"no value", `battery.charge`, "", "", false},
		{"empty", ``, "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, value, ok := parseVarLine(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("parseVarLine(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			if value != tt.wantValue {
				t.Errorf("value = %q, want %q", value, tt.wantValue)
			}
		})
	}
}

// TestParseVarLineDoesNotStripLegitimateQuotes is the specific regression:
// strings.Trim removes *every* leading and trailing quote.
func TestParseVarLineDoesNotStripLegitimateQuotes(t *testing.T) {
	_, value, ok := parseVarLine(`device.model "\"quoted\""`)
	if !ok {
		t.Fatal("parseVarLine failed")
	}
	if value != `"quoted"` {
		t.Errorf("value = %q, want %q — legitimate quotes were stripped", value, `"quoted"`)
	}
}

// TestNUTErrorsAreActionable: an operator reading the log should be able to
// tell an auth problem from a driver problem.
func TestNUTErrors(t *testing.T) {
	tests := []struct {
		line     string
		contains string
	}{
		{"ERR ACCESS-DENIED", "upsd.users"},
		{"ERR UNKNOWN-UPS", "ups.conf"},
		{"ERR CMD-NOT-SUPPORTED", "not supported"},
		{"ERR INSTCMD-FAILED", "rejected"},
		{"ERR DRIVER-NOT-CONNECTED", "not connected"},
		{"ERR DATA-STALE", "stale"},
		{"ERR SOMETHING-NEW", "SOMETHING-NEW"},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			err := nutError(tt.line)
			if err == nil {
				t.Fatal("nutError returned nil")
			}
			if !strings.Contains(err.Error(), tt.contains) {
				t.Errorf("error %q does not mention %q", err, tt.contains)
			}
		})
	}
}

func TestCheckOK(t *testing.T) {
	if err := checkOK("OK", "USERNAME"); err != nil {
		t.Errorf("checkOK(OK) = %v, want nil", err)
	}
	if err := checkOK("ERR ACCESS-DENIED", "PASSWORD"); err == nil {
		t.Error("checkOK accepted an ERR response")
	}
	if err := checkOK("something unexpected", "LIST"); err == nil {
		t.Error("checkOK accepted an unrecognised response")
	}
}

func TestInstanceConfigDefaults(t *testing.T) {
	var c InstanceConfig

	if got := c.host(); got != "localhost" {
		t.Errorf("host() = %q, want localhost", got)
	}
	if got := c.port(); got != defaultNUTPort {
		t.Errorf("port() = %d, want %d", got, defaultNUTPort)
	}
	if got := c.ups(); got != defaultUPSName {
		t.Errorf("ups() = %q, want %q", got, defaultUPSName)
	}
	if got := c.pollInterval(); got != defaultPollInterval {
		t.Errorf("pollInterval() = %v, want %v", got, defaultPollInterval)
	}
}

func TestInstanceConfigOverrides(t *testing.T) {
	c := InstanceConfig{Host: "nut.lan", Port: 3999, UPS: "rack", PollInterval: "5s"}

	if got := c.host(); got != "nut.lan" {
		t.Errorf("host() = %q", got)
	}
	if got := c.port(); got != 3999 {
		t.Errorf("port() = %d", got)
	}
	if got := c.ups(); got != "rack" {
		t.Errorf("ups() = %q", got)
	}
	if got := c.pollInterval(); got != 5*time.Second {
		t.Errorf("pollInterval() = %v", got)
	}
}

func TestInstanceConfigRejectsBadPollInterval(t *testing.T) {
	for _, value := range []string{"not-a-duration", "-5s", "0s"} {
		c := InstanceConfig{PollInterval: value}
		if got := c.pollInterval(); got != defaultPollInterval {
			t.Errorf("pollInterval(%q) = %v, want the default %v", value, got, defaultPollInterval)
		}
	}
}
