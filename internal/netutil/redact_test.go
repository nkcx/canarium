package netutil

import (
	"strings"
	"testing"
)

func TestRedactURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			"no credentials",
			"https://pve.lan:8006/api2/json/nodes/steel/status",
			"https://pve.lan:8006/api2/json/nodes/steel/status",
		},
		{
			"token query parameter",
			"https://host/api?token=supersecret",
			"https://host/api?token=REDACTED",
		},
		{
			"api_key parameter",
			"https://host/api?api_key=supersecret&node=steel",
			"https://host/api?api_key=REDACTED&node=steel",
		},
		{
			"hyphenated key normalised",
			"https://host/api?api-key=supersecret",
			"https://host/api?api-key=REDACTED",
		},
		{
			"mixed case key",
			"https://host/api?ApiKey=supersecret",
			"https://host/api?ApiKey=REDACTED",
		},
		{
			"userinfo password",
			"https://admin:hunter2@host/api",
			"https://admin:REDACTED@host/api",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactURL(tt.in)
			if got != tt.want {
				t.Errorf("RedactURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if strings.Contains(got, "supersecret") || strings.Contains(got, "hunter2") {
				t.Errorf("RedactURL leaked a credential: %q", got)
			}
		})
	}
}

func TestRedactURLOnUnparseableInput(t *testing.T) {
	// An unparseable value is exactly where a credential might sit
	// unrecognised, so it must not be passed through.
	got := RedactURL("://not a url at all\x7f")
	if got != redactedPlaceholder {
		t.Errorf("RedactURL on garbage = %q, want %q", got, redactedPlaceholder)
	}
}

// TestRedactEndpointHidesPathSecrets covers the dominant webhook providers,
// which put the token in the path where RedactURL cannot recognise it.
func TestRedactEndpointHidesPathSecrets(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		secret string
		want   string
	}{
		{
			"slack",
			"https://hooks.slack.com/services/T00000000/B00000000/abcdefghijklmnop",
			"abcdefghijklmnop",
			"https://hooks.slack.com/REDACTED",
		},
		{
			"discord",
			"https://discord.com/api/webhooks/123456789/zyxwvutsrqponml",
			"zyxwvutsrqponml",
			"https://discord.com/REDACTED",
		},
		{
			"bare host",
			"https://example.com",
			"",
			"https://example.com",
		},
		{
			"root path",
			"https://example.com/",
			"",
			"https://example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactEndpoint(tt.in)
			if got != tt.want {
				t.Errorf("RedactEndpoint(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if tt.secret != "" && strings.Contains(got, tt.secret) {
				t.Errorf("RedactEndpoint leaked the webhook token: %q", got)
			}
		})
	}
}

func TestRedactSecrets(t *testing.T) {
	msg := `Post "https://host/api": token hunter2000 rejected`

	got := RedactSecrets(msg, "hunter2000")
	if strings.Contains(got, "hunter2000") {
		t.Errorf("RedactSecrets left the secret in: %q", got)
	}
	if !strings.Contains(got, redactedPlaceholder) {
		t.Errorf("RedactSecrets did not substitute a placeholder: %q", got)
	}
}

// TestRedactSecretsIgnoresShortStrings: redacting a one- or two-character
// "secret" would mangle unrelated text without protecting anything.
func TestRedactSecretsIgnoresShortStrings(t *testing.T) {
	msg := "a connection error occurred"

	for _, secret := range []string{"", "a", "ab", "abc"} {
		if got := RedactSecrets(msg, secret); got != msg {
			t.Errorf("RedactSecrets(%q, %q) = %q, want the message unchanged",
				msg, secret, got)
		}
	}
}

func TestRedactSecretsHandlesMultiple(t *testing.T) {
	msg := "user=alice-token pass=bob-token done"

	got := RedactSecrets(msg, "alice-token", "bob-token")
	if strings.Contains(got, "alice-token") || strings.Contains(got, "bob-token") {
		t.Errorf("RedactSecrets left a secret in: %q", got)
	}
}
