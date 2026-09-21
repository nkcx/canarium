package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func request(remoteAddr, xff string) *http.Request {
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.RemoteAddr = remoteAddr
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

// TestForwardedHeadersRequireATrustedSource is the regression test for
// trusting X-Forwarded-For on the operator's say-so alone. With
// trust_proxy_headers set, any client that could reach the port directly
// could forge a source address — evading its own rate limit and locking out
// whoever it chose to impersonate.
func TestForwardedHeadersRequireATrustedSource(t *testing.T) {
	truster := newProxyTruster(true, nil, discardLogger())

	// A direct connection from a public address is not a proxy.
	if truster.trusts(request("203.0.113.9:44444", "198.51.100.1")) {
		t.Error("a forwarded header was trusted from a non-proxy source; " +
			"an attacker could forge any source address")
	}

	// Loopback is, by default.
	if !truster.trusts(request("127.0.0.1:44444", "198.51.100.1")) {
		t.Error("a request from loopback was not trusted as a proxy")
	}
}

func TestClientIPUsesForwardedOnlyWhenTrusted(t *testing.T) {
	truster := newProxyTruster(true, nil, discardLogger())

	fromProxy := request("127.0.0.1:1234", "203.0.113.9")
	if got := clientIP(fromProxy, truster.trusts(fromProxy)); got != "203.0.113.9" {
		t.Errorf("clientIP behind a trusted proxy = %q, want the forwarded address", got)
	}

	direct := request("198.51.100.5:1234", "203.0.113.9")
	if got := clientIP(direct, truster.trusts(direct)); got != "198.51.100.5" {
		t.Errorf("clientIP for a direct connection = %q, want the real peer address", got)
	}
}

func TestDisabledProxyTrustIgnoresEverything(t *testing.T) {
	truster := newProxyTruster(false, []string{"127.0.0.0/8"}, discardLogger())

	if truster.trusts(request("127.0.0.1:1234", "203.0.113.9")) {
		t.Error("forwarding headers were trusted with trust_proxy_headers off")
	}
}

func TestExplicitTrustedProxyList(t *testing.T) {
	truster := newProxyTruster(true, []string{"10.42.0.0/16"}, discardLogger())

	if !truster.trusts(request("10.42.7.3:1234", "203.0.113.9")) {
		t.Error("a request from the configured proxy network was not trusted")
	}
	// Loopback is not in the explicit list, so it is not trusted.
	if truster.trusts(request("127.0.0.1:1234", "203.0.113.9")) {
		t.Error("loopback was trusted despite an explicit list that excludes it")
	}
}

func TestBareAddressInTrustedProxies(t *testing.T) {
	truster := newProxyTruster(true, []string{"10.42.7.3"}, discardLogger())

	if !truster.trusts(request("10.42.7.3:1234", "")) {
		t.Error("a bare address in trusted_proxies was not accepted")
	}
	if truster.trusts(request("10.42.7.4:1234", "")) {
		t.Error("an address outside the single-host entry was trusted")
	}
}

// TestUnparseableProxyEntryIsDropped: refusing to boot over a malformed
// config entry would take the daemon offline, which is worse than falling
// back to not trusting the header.
func TestUnparseableProxyEntryIsDropped(t *testing.T) {
	truster := newProxyTruster(true, []string{"not-a-cidr", "10.42.0.0/16"}, discardLogger())

	if !truster.trusts(request("10.42.7.3:1234", "")) {
		t.Error("a valid entry alongside a malformed one was not honoured")
	}
}

func TestIPv6TrustedProxy(t *testing.T) {
	truster := newProxyTruster(true, nil, discardLogger())

	if !truster.trusts(request("[::1]:1234", "203.0.113.9")) {
		t.Error("IPv6 loopback was not trusted as a proxy")
	}
}
