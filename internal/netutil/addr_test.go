package netutil

import "testing"

func TestHostPort(t *testing.T) {
	tests := []struct {
		name string
		host string
		port int
		want string
	}{
		{"hostname", "nas.example.com", 443, "nas.example.com:443"},
		{"ipv4", "10.0.10.20", 3493, "10.0.10.20:3493"},
		{"ipv6", "fd00::1", 8006, "[fd00::1]:8006"},
		{"ipv6 loopback", "::1", 22, "[::1]:22"},
		{"empty host", "", 161, ":161"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HostPort(tt.host, tt.port); got != tt.want {
				t.Errorf("HostPort(%q, %d) = %q, want %q", tt.host, tt.port, got, tt.want)
			}
		})
	}
}

func TestURL(t *testing.T) {
	tests := []struct {
		name   string
		scheme string
		host   string
		port   int
		path   string
		want   string
	}{
		{"ipv4 https", "https", "10.0.1.1", 443, "/api/core/system/halt", "https://10.0.1.1:443/api/core/system/halt"},
		{"ipv6 https", "https", "fd00::1", 8006, "/api2/json", "https://[fd00::1]:8006/api2/json"},
		{"ipv6 wss", "wss", "fd00::5", 443, "/api/current", "wss://[fd00::5]:443/api/current"},
		{"path without slash", "http", "host", 80, "x", "http://host:80/x"},
		{"empty path", "http", "host", 80, "", "http://host:80"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := URL(tt.scheme, tt.host, tt.port, tt.path)
			if got != tt.want {
				t.Errorf("URL(%q, %q, %d, %q) = %q, want %q",
					tt.scheme, tt.host, tt.port, tt.path, got, tt.want)
			}
		})
	}
}
