package rest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nkcx/canarium/internal/engine"
)

func TestExecutePostsToConfiguredURL(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotBody   string
		gotHeader string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotHeader = r.Header.Get("X-Api-Key")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	tr := New()
	result, err := tr.Execute(context.Background(), &engine.Client{
		Name:    "nas",
		Address: "10.0.10.20",
		TransportConfig: map[string]any{
			"shutdown_url":     srv.URL + "/shutdown",
			"shutdown_body":    `{"host":"{name}"}`,
			"shutdown_headers": map[string]any{"X-Api-Key": "abc123"},
		},
	}, engine.ActionShutdown)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !result.Success {
		t.Errorf("Success = false: %s", result.Message)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/shutdown" {
		t.Errorf("path = %s, want /shutdown", gotPath)
	}
	if !strings.Contains(gotBody, `"nas"`) {
		t.Errorf("body = %q, want {name} expanded", gotBody)
	}
	if gotHeader != "abc123" {
		t.Errorf("X-Api-Key = %q, want abc123", gotHeader)
	}
}

func TestExecuteReportsNonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "boom")
	}))
	defer srv.Close()

	tr := New()
	result, err := tr.Execute(context.Background(), &engine.Client{
		Name:            "nas",
		TransportConfig: map[string]any{"shutdown_url": srv.URL},
	}, engine.ActionShutdown)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Error("a 500 response was reported as a successful shutdown")
	}
}

func TestExecuteRequiresAURL(t *testing.T) {
	tr := New()

	if _, err := tr.Execute(context.Background(),
		&engine.Client{Name: "nas"}, engine.ActionShutdown); err == nil {
		t.Error("Execute succeeded with no shutdown_url configured")
	}
}

// TestCredentialsAreRedactedFromResults is the regression test for leaking a
// token into the intents table, the event stream and every webhook.
func TestCredentialsAreRedactedFromResults(t *testing.T) {
	const secret = "super-secret-token-value"

	tr := New()
	// Point at a port nothing is listening on, so http.Client returns an
	// error containing the full URL.
	result, err := tr.Execute(context.Background(), &engine.Client{
		Name:        "nas",
		Credentials: secret,
		TransportConfig: map[string]any{
			"shutdown_url": "http://127.0.0.1:1/shutdown?token={credentials}",
		},
	}, engine.ActionShutdown)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if strings.Contains(result.Message, secret) {
		t.Errorf("the result message leaks the credential:\n%s", result.Message)
	}
}

func TestProbeTreatsAnyResponseAsUp(t *testing.T) {
	for _, status := range []int{200, 401, 404, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer srv.Close()

			tr := New()
			state, err := tr.Probe(context.Background(), &engine.Client{
				Name:            "nas",
				TransportConfig: map[string]any{"probe_url": srv.URL},
			})
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			// Something answered, so the host is up. A 5xx means an
			// unhealthy application, not a powered-off machine.
			if state != engine.StateUp {
				t.Errorf("HTTP %d = %v, want up", status, state)
			}
		})
	}
}

// TestProbeRefusedIsDown: a refused connection is positive evidence.
func TestProbeRefusedIsDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now

	tr := New()
	state, err := tr.Probe(context.Background(), &engine.Client{
		Name:            "nas",
		TransportConfig: map[string]any{"probe_url": url},
	})

	if state == engine.StateUp {
		t.Error("a refused connection reported the host as up")
	}
	// Either down (refused) or unknown (classified indeterminate) is
	// acceptable; what must not happen is "up".
	_ = err
}

func TestExpandVars(t *testing.T) {
	client := &engine.Client{
		Name:        "nas",
		Address:     "10.0.10.20",
		MAC:         "aa:bb:cc:dd:ee:ff",
		Credentials: "tok",
	}

	got := expandVars("http://{address}/{name}/{mac}?t={credentials}", client)
	want := "http://10.0.10.20/nas/aa:bb:cc:dd:ee:ff?t=tok"
	if got != want {
		t.Errorf("expandVars = %q, want %q", got, want)
	}
}
