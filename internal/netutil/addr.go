// Package netutil provides address-construction helpers shared by transports
// and sources.
//
// Transports build dial addresses and API URLs from operator-supplied host
// strings, which may be hostnames, IPv4 literals, or IPv6 literals. IPv6
// literals must be bracketed before a port is appended, so every address is
// built through these helpers rather than with fmt.Sprintf("%s:%d", ...).
package netutil

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// HostPort joins a host and port into a dial address, bracketing IPv6
// literals as required by net.Dial.
func HostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// URL builds an absolute URL from its parts, bracketing IPv6 literals in the
// authority section. path is used verbatim and should already be escaped; it
// is prefixed with "/" if it does not start with one.
func URL(scheme, host string, port int, path string) string {
	if path != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := url.URL{
		Scheme: scheme,
		Host:   HostPort(host, port),
		Path:   path,
	}
	return u.String()
}
