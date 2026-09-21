package api

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// defaultTrustedProxies are the networks assumed to be reverse proxies when
// trust_proxy_headers is set and no explicit list is given.
//
// Loopback covers the overwhelmingly common case — a proxy on the same host,
// which is what the shipped compose file produces. The private ranges cover a
// proxy container on a bridge network. A public address is never assumed
// trustworthy.
var defaultTrustedProxies = []string{
	"127.0.0.0/8",
	"::1/128",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"fc00::/7",
}

// proxyTruster decides whether a request arrived from a trusted proxy.
type proxyTruster struct {
	enabled  bool
	networks []*net.IPNet
}

// newProxyTruster compiles the configured CIDR list.
//
// An entry that does not parse is dropped with a warning rather than failing
// startup: refusing to boot over a malformed proxy entry would take the
// daemon offline, which is worse than falling back to not trusting the
// header.
func newProxyTruster(enabled bool, cidrs []string, logger *slog.Logger) *proxyTruster {
	t := &proxyTruster{enabled: enabled}
	if !enabled {
		return t
	}

	if len(cidrs) == 0 {
		cidrs = defaultTrustedProxies
		if logger != nil {
			logger.Info("trust_proxy_headers is set with no trusted_proxies list; "+
				"trusting loopback and private ranges",
				"networks", strings.Join(cidrs, ", "))
		}
	}

	for _, entry := range cidrs {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		// A bare address is accepted as a single-host network.
		if !strings.Contains(entry, "/") {
			if ip := net.ParseIP(entry); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				t.networks = append(t.networks, &net.IPNet{
					IP: ip, Mask: net.CIDRMask(bits, bits),
				})
				continue
			}
		}

		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			if logger != nil {
				logger.Error("ignoring an unparseable entry in trusted_proxies",
					"entry", entry, "error", err)
			}
			continue
		}
		t.networks = append(t.networks, network)
	}

	return t
}

// trusts reports whether a request came from a trusted proxy.
func (t *proxyTruster) trusts(r *http.Request) bool {
	if t == nil || !t.enabled || len(t.networks) == 0 || r == nil {
		return false
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return false
	}

	for _, network := range t.networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// trustedProxy reports whether this request's forwarding headers may be
// believed.
//
// Two conditions, not one: the operator must have opted in, and the request
// must have actually arrived from a proxy address. The opt-in alone was
// enough before, which meant anyone who could reach the port directly could
// forge a source address — evading their own rate limit and locking out
// whoever they claimed to be.
func (s *Server) trustedProxy(r *http.Request) bool {
	return s.proxies.trusts(r)
}
