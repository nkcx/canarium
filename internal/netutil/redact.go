package netutil

import (
	"net/url"
	"strings"
)

// redactedPlaceholder replaces any value considered sensitive.
const redactedPlaceholder = "REDACTED"

// sensitiveQueryKeys are query parameter names whose values are removed from
// logged and persisted URLs.
var sensitiveQueryKeys = []string{
	"apikey", "api_key", "access_token", "auth", "key", "password", "passwd",
	"pass", "secret", "sig", "signature", "token",
}

// RedactURL removes credentials from a URL so it is safe to log, persist or
// send to a webhook.
//
// Transports substitute {credentials} into URLs, and Go's http.Client puts
// the full URL into every error it returns. Those errors previously flowed
// straight into ActionResult.Message, which is written to the intents table,
// broadcast over the WebSocket and POSTed to notification webhooks — so a
// single unreachable host leaked its API token to every one of those places.
//
// A string that cannot be parsed as a URL is returned as the placeholder
// rather than passed through, because an unparseable value is exactly the
// case where a credential might be sitting in it unrecognised.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return redactedPlaceholder
	}

	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), redactedPlaceholder)
		}
	}

	if q := u.Query(); len(q) > 0 {
		changed := false
		for key := range q {
			if isSensitiveKey(key) {
				q.Set(key, redactedPlaceholder)
				changed = true
			}
		}
		if changed {
			u.RawQuery = q.Encode()
		}
	}

	return u.String()
}

func isSensitiveKey(key string) bool {
	normalised := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, candidate := range sensitiveQueryKeys {
		if normalised == candidate {
			return true
		}
	}
	return false
}

// RedactEndpoint reduces a URL to scheme and host, eliding the path.
//
// Use this for notification endpoints. RedactURL only removes userinfo and
// credential-shaped query parameters, but the dominant webhook providers put
// the secret in the *path*:
//
//	https://hooks.slack.com/services/T00000000/B00000000/XXXXXXXXXXXXXXXX
//	https://discord.com/api/webhooks/123456789/XXXXXXXXXXXXXXXX
//
// For those, the entire path is the credential, so identifying the endpoint
// by host alone is the only safe rendering that is still useful in a log.
func RedactEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return redactedPlaceholder
	}

	out := u.Scheme + "://" + u.Host
	if u.Path != "" && u.Path != "/" {
		out += "/" + redactedPlaceholder
	}
	return out
}

// RedactSecrets removes every occurrence of the given secrets from s.
//
// Used for error strings that may embed a credential somewhere structured
// parsing cannot reach. Empty and very short secrets are ignored: redacting a
// one- or two-character string would mangle unrelated text without
// protecting anything meaningful.
func RedactSecrets(s string, secrets ...string) string {
	const minSecretLength = 4

	for _, secret := range secrets {
		if len(secret) < minSecretLength {
			continue
		}
		s = strings.ReplaceAll(s, secret, redactedPlaceholder)
	}
	return s
}
