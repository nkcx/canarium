package netutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
)

// TLSOptions are the per-client TLS settings a transport reads from its
// transport_config block.
type TLSOptions struct {
	// InsecureSkipVerify disables certificate verification entirely.
	//
	// Canarium's HTTPS transports hold credentials that control entire
	// hypervisors and storage arrays. An on-path attacker facing an
	// unverified connection can present any certificate, capture the API
	// token, and then issue whatever commands they like. This exists
	// because self-signed certificates are near-universal on the appliances
	// Canarium talks to, but it must be a decision the operator makes
	// explicitly — it was previously hardcoded to true.
	InsecureSkipVerify bool

	// CACertFile is a PEM bundle to verify the peer against, instead of the
	// system roots. This is the right answer for a self-signed appliance:
	// export its certificate once and pin it, rather than trusting anything.
	CACertFile string

	// ServerName overrides the name checked against the certificate, for
	// appliances reached by IP whose certificate names a hostname.
	ServerName string
}

// TLSOptionsFrom extracts TLS settings from a transport_config map.
func TLSOptionsFrom(cfg map[string]any) TLSOptions {
	return TLSOptions{
		InsecureSkipVerify: configBool(cfg, "tls_insecure_skip_verify"),
		CACertFile:         configString(cfg, "tls_ca_cert"),
		ServerName:         configString(cfg, "tls_server_name"),
	}
}

// TLSConfig builds a *tls.Config from the options.
//
// The zero value verifies against the system roots, which is the secure
// default. Anything weaker requires an explicit opt-in, and logs a warning
// naming the client so it is visible in the journal rather than buried in a
// config file.
func (o TLSOptions) TLSConfig(logger *slog.Logger, clientName string) (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: o.ServerName,
	}

	if o.CACertFile != "" {
		pem, err := os.ReadFile(o.CACertFile)
		if err != nil {
			return nil, fmt.Errorf("reading tls_ca_cert %q: %w", o.CACertFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("tls_ca_cert %q contains no usable certificates", o.CACertFile)
		}
		cfg.RootCAs = pool
	}

	if o.InsecureSkipVerify {
		if o.CACertFile != "" {
			return nil, fmt.Errorf(
				"tls_insecure_skip_verify and tls_ca_cert are mutually exclusive: " +
					"pinning a CA and then not checking it achieves nothing")
		}
		cfg.InsecureSkipVerify = true
		if logger != nil {
			logger.Warn("TLS certificate verification is disabled for this client; "+
				"credentials sent to it can be captured by an on-path attacker. "+
				"Prefer tls_ca_cert to pin the appliance's certificate.",
				"client", clientName)
		}
	}

	return cfg, nil
}

func configString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	s, _ := cfg[key].(string)
	return s
}

func configBool(cfg map[string]any, key string) bool {
	if cfg == nil {
		return false
	}
	switch v := cfg[key].(type) {
	case bool:
		return v
	case string:
		// Environment substitution yields strings.
		return v == "true" || v == "yes" || v == "1"
	default:
		return false
	}
}
