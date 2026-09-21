package truenas

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nkcx/canarium/internal/engine"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func hostPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()

	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parsing %s: %v", rawURL, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parsing port: %v", err)
	}
	return u.Hostname(), port
}

// middleware is a stand-in for the TrueNAS JSON-RPC endpoint.
type middleware struct {
	authOK  bool
	methods []string
	apiKeys []string
}

func (m *middleware) handler(t *testing.T) http.HandlerFunc {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			var req map[string]any
			if err := conn.ReadJSON(&req); err != nil {
				return
			}

			method, _ := req["method"].(string)
			m.methods = append(m.methods, method)

			resp := map[string]any{"jsonrpc": "2.0", "id": req["id"]}

			switch method {
			case "auth.login_with_api_key":
				if params, ok := req["params"].([]any); ok && len(params) > 0 {
					if key, ok := params[0].(string); ok {
						m.apiKeys = append(m.apiKeys, key)
					}
				}
				resp["result"] = m.authOK
			case "system.shutdown":
				resp["result"] = true
			default:
				resp["error"] = map[string]any{"message": "unknown method " + method}
			}

			if err := conn.WriteJSON(resp); err != nil {
				return
			}
		}
	}
}

func TestShutdownAuthenticatesThenCallsSystemShutdown(t *testing.T) {
	mw := &middleware{authOK: true}
	srv := httptest.NewServer(mw.handler(t))
	defer srv.Close()

	host, port := hostPort(t, srv.URL)

	tr := New(discardLogger())
	result, err := tr.Execute(context.Background(), &engine.Client{
		Name:        "brick",
		Address:     host,
		Credentials: "1-abcdefghijklmnop",
		TransportConfig: map[string]any{
			"port": port,
			"tls":  false, // httptest.NewServer is plain HTTP
		},
	}, engine.ActionShutdown)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Success {
		t.Fatalf("Success = false: %s", result.Message)
	}

	if len(mw.methods) != 2 ||
		mw.methods[0] != "auth.login_with_api_key" ||
		mw.methods[1] != "system.shutdown" {
		t.Errorf("methods = %v, want auth then shutdown", mw.methods)
	}
	if len(mw.apiKeys) != 1 || mw.apiKeys[0] != "1-abcdefghijklmnop" {
		t.Errorf("api keys received = %v, want the configured one", mw.apiKeys)
	}
}

func TestShutdownFailsWhenAuthIsRejected(t *testing.T) {
	mw := &middleware{authOK: false}
	srv := httptest.NewServer(mw.handler(t))
	defer srv.Close()

	host, port := hostPort(t, srv.URL)

	tr := New(discardLogger())
	_, err := tr.Execute(context.Background(), &engine.Client{
		Name:            "brick",
		Address:         host,
		Credentials:     "wrong-key",
		TransportConfig: map[string]any{"port": port, "tls": false},
	}, engine.ActionShutdown)

	if err == nil {
		t.Fatal("a rejected authentication was reported as success")
	}
	if !strings.Contains(err.Error(), "authentication") {
		t.Errorf("error does not mention authentication: %v", err)
	}
}

// TestAuthErrorDoesNotLeakTheAPIKey: the error may quote the request.
func TestAuthErrorDoesNotLeakTheAPIKey(t *testing.T) {
	const secret = "1-super-secret-api-key-value"

	mw := &middleware{authOK: false}
	srv := httptest.NewServer(mw.handler(t))
	defer srv.Close()

	host, port := hostPort(t, srv.URL)

	tr := New(discardLogger())
	_, err := tr.Execute(context.Background(), &engine.Client{
		Name:            "brick",
		Address:         host,
		Credentials:     secret,
		TransportConfig: map[string]any{"port": port, "tls": false},
	}, engine.ActionShutdown)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the error leaks the API key: %v", err)
	}
}

// TestNoPlaintextFallback is the regression test for the TLS downgrade: the
// transport retried over ws:// whenever the wss:// dial failed, and TrueNAS
// ships a self-signed certificate, so the common path sent an API key in
// cleartext with nothing in the logs to say so.
func TestNoPlaintextFallback(t *testing.T) {
	mw := &middleware{authOK: true}
	// A plain HTTP server, while the client is told to use TLS.
	srv := httptest.NewServer(mw.handler(t))
	defer srv.Close()

	host, port := hostPort(t, srv.URL)

	tr := New(discardLogger())
	_, err := tr.Execute(context.Background(), &engine.Client{
		Name:        "brick",
		Address:     host,
		Credentials: "1-key",
		TransportConfig: map[string]any{
			"port": port,
			// tls defaults to true
		},
	}, engine.ActionShutdown)

	if err == nil {
		t.Fatal("a TLS dial against a plaintext server succeeded; " +
			"the transport fell back to ws:// and would have sent the API key in clear")
	}
	if len(mw.apiKeys) != 0 {
		t.Errorf("the API key reached the server over plaintext: %v", mw.apiKeys)
	}
}

func TestTLSVerificationIsOnByDefault(t *testing.T) {
	mw := &middleware{authOK: true}
	srv := httptest.NewTLSServer(mw.handler(t))
	defer srv.Close()

	host, port := hostPort(t, srv.URL)

	tr := New(discardLogger())
	_, err := tr.Execute(context.Background(), &engine.Client{
		Name:            "brick",
		Address:         host,
		Credentials:     "1-key",
		TransportConfig: map[string]any{"port": port},
	}, engine.ActionShutdown)

	if err == nil {
		t.Error("connected to a server with an untrusted certificate")
	}
	if len(mw.apiKeys) != 0 {
		t.Errorf("the API key was sent over an unverified connection: %v", mw.apiKeys)
	}
}

func TestInsecureSkipVerifyOptIn(t *testing.T) {
	mw := &middleware{authOK: true}
	srv := httptest.NewTLSServer(mw.handler(t))
	defer srv.Close()

	host, port := hostPort(t, srv.URL)

	tr := New(discardLogger())
	result, err := tr.Execute(context.Background(), &engine.Client{
		Name:        "brick",
		Address:     host,
		Credentials: "1-key",
		TransportConfig: map[string]any{
			"port":                     port,
			"tls_insecure_skip_verify": true,
		},
	}, engine.ActionShutdown)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Success {
		t.Errorf("Success = false: %s", result.Message)
	}
}

func TestExecuteHonoursContextDeadline(t *testing.T) {
	// A server that accepts the connection and then never answers.
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(30 * time.Second)
	}))
	defer srv.Close()

	host, port := hostPort(t, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	tr := New(discardLogger())

	start := time.Now()
	_, err := tr.Execute(ctx, &engine.Client{
		Name:            "brick",
		Address:         host,
		Credentials:     "1-key",
		TransportConfig: map[string]any{"port": port, "tls": false},
	}, engine.ActionShutdown)

	if err == nil {
		t.Fatal("expected a timeout")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Execute took %v; the context deadline was not honoured", elapsed)
	}
}

func TestUnsupportedActionIsRejected(t *testing.T) {
	tr := New(discardLogger())

	if _, err := tr.Execute(context.Background(),
		&engine.Client{Name: "brick"}, engine.ActionWake); err == nil {
		t.Error("the truenas transport accepted a wake action")
	}
}

func TestSessionObjectAuthResponseIsAccepted(t *testing.T) {
	// TrueNAS 25.04+ returns a session object rather than a bare true.
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			var req map[string]any
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			resp := map[string]any{"jsonrpc": "2.0", "id": req["id"]}
			if req["method"] == "auth.login_with_api_key" {
				resp["result"] = map[string]any{"session_id": "abc", "username": "root"}
			} else {
				resp["result"] = true
			}
			if err := conn.WriteJSON(resp); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	host, port := hostPort(t, srv.URL)

	tr := New(discardLogger())
	result, err := tr.Execute(context.Background(), &engine.Client{
		Name:            "brick",
		Address:         host,
		Credentials:     "1-key",
		TransportConfig: map[string]any{"port": port, "tls": false},
	}, engine.ActionShutdown)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Success {
		t.Errorf("a session-object auth response was treated as a failure: %s", result.Message)
	}
}

func TestProbeClassifiesReachability(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	host, port := hostPort(t, srv.URL)

	tr := New(discardLogger())
	state, err := tr.Probe(context.Background(), &engine.Client{
		Name:        "brick",
		Address:     host,
		ProbeConfig: engine.ProbeConfig{Port: port, Timeout: time.Second},
	})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if state != engine.StateUp {
		t.Errorf("state = %v, want up", state)
	}

	srv.Close()

	state, _ = tr.Probe(context.Background(), &engine.Client{
		Name:        "brick",
		Address:     host,
		ProbeConfig: engine.ProbeConfig{Port: port, Timeout: time.Second},
	})
	if state == engine.StateUp {
		t.Error("a closed port reported the host as up")
	}
}

var _ = json.Marshal
