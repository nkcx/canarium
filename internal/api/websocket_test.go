package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nkcx/canarium/internal/engine"
)

// wsTestServer starts a real HTTP server with an authenticated session and
// returns a dialer helper for the WebSocket endpoint.
func wsTestServer(t *testing.T) (*Server, *httptest.Server, *http.Cookie) {
	t.Helper()

	s, _ := newTestServer(t)
	if rec := do(t, s, "POST", "/api/auth/setup", `{"password":"`+testPassword+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("setup: got %d", rec.Code)
	}
	cookie := sessionCookie(t, s)

	ts := httptest.NewServer(s.mux)
	t.Cleanup(ts.Close)

	return s, ts, cookie
}

func dialWS(t *testing.T, ts *httptest.Server, cookie *http.Cookie, origin string) *websocket.Conn {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws"

	header := http.Header{}
	header.Set("Cookie", cookie.Name+"="+cookie.Value)
	if origin != "" {
		header.Set("Origin", origin)
	}

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		status := "no response"
		if resp != nil {
			status = resp.Status
		}
		t.Fatalf("dialing %s: %v (%s)", wsURL, err, status)
	}
	t.Cleanup(func() { conn.Close() })

	return conn
}

// TestBroadcastDoesNotPanicOnDisconnect is the regression test for a
// remote-triggerable crash. readPump used to close the send channel while
// the client was still in the registry, so a broadcast arriving in that
// window panicked on send-to-closed-channel. BroadcastEvent runs on the
// executor's goroutine with no recover anywhere, so closing a browser tab at
// the wrong moment killed the daemon.
//
// This hammers connect/disconnect against a continuous broadcast storm. On
// the old code it panics within a few iterations.
func TestBroadcastDoesNotPanicOnDisconnect(t *testing.T) {
	s, ts, cookie := wsTestServer(t)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Broadcast continuously from several goroutines, as the executor would
	// during a staged shutdown.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					s.BroadcastEvent(engine.Event{
						Type:      "client_state_changed",
						Timestamp: time.Now(),
						Data:      map[string]string{"client": "nas", "state": "down"},
					})
				}
			}
		}()
	}

	// Connect and abruptly disconnect, repeatedly.
	for i := 0; i < 50; i++ {
		conn := dialWS(t, ts, cookie, ts.URL)
		conn.Close()
	}

	close(stop)
	wg.Wait()

	// Give the pumps a moment to unregister, then confirm the registry
	// drained rather than leaking client entries.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.wsMu.RLock()
		n := len(s.wsClients)
		s.wsMu.RUnlock()
		if n == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	s.wsMu.RLock()
	n := len(s.wsClients)
	s.wsMu.RUnlock()
	t.Errorf("%d websocket clients still registered after disconnect; the registry leaks", n)
}

func TestWebSocketReceivesBroadcastEvents(t *testing.T) {
	s, ts, cookie := wsTestServer(t)
	conn := dialWS(t, ts, cookie, ts.URL)

	// The client must be registered before a broadcast can reach it.
	waitForClients(t, s, 1)

	s.BroadcastEvent(engine.Event{
		Type:      "trigger",
		Timestamp: time.Now(),
		Data:      "outage",
	})

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}

	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("reading broadcast: %v", err)
	}
	if !strings.Contains(string(data), `"trigger"`) {
		t.Errorf("received %q, want an event of type trigger", data)
	}
}

func TestWebSocketRequiresAuthentication(t *testing.T) {
	s, _ := newTestServer(t)
	if rec := do(t, s, "POST", "/api/auth/setup", `{"password":"`+testPassword+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("setup: got %d", rec.Code)
	}

	ts := httptest.NewServer(s.mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		conn.Close()
		t.Fatal("unauthenticated websocket upgrade succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		got := "no response"
		if resp != nil {
			got = resp.Status
		}
		t.Errorf("got %s, want 401", got)
	}
}

// TestWebSocketRejectsCrossOriginUpgrade covers cross-site WebSocket
// hijacking: the upgrade carries the session cookie, so an arbitrary page
// must not be able to open one.
func TestWebSocketRejectsCrossOriginUpgrade(t *testing.T) {
	_, ts, cookie := wsTestServer(t)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws"
	header := http.Header{}
	header.Set("Cookie", cookie.Name+"="+cookie.Value)
	header.Set("Origin", "https://evil.example.com")

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		conn.Close()
		t.Fatal("cross-origin websocket upgrade was accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		got := "no response"
		if resp != nil {
			got = resp.Status
		}
		t.Errorf("got %s, want 403", got)
	}
}

func TestWebSocketAllowsConfiguredOrigin(t *testing.T) {
	s, ts, cookie := wsTestServer(t)
	s.cfg.Canarium.Auth.AllowedOrigins = []string{"https://ui.example.com"}

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws"
	header := http.Header{}
	header.Set("Cookie", cookie.Name+"="+cookie.Value)
	header.Set("Origin", "https://ui.example.com")

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("configured origin was rejected: %v", err)
	}
	conn.Close()
}

// TestSlowClientDoesNotBlockBroadcast verifies the executor goroutine is
// never held up by a client that has stopped reading.
func TestSlowClientDoesNotBlockBroadcast(t *testing.T) {
	s, ts, cookie := wsTestServer(t)
	conn := dialWS(t, ts, cookie, ts.URL)
	defer conn.Close()

	waitForClients(t, s, 1)

	// Never read from conn. Once the buffer and TCP window fill, sends must
	// start being dropped rather than blocking.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < wsSendBuffer*4; i++ {
			s.BroadcastEvent(engine.Event{Type: "noise", Timestamp: time.Now()})
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("BroadcastEvent blocked on a client that stopped reading; " +
			"a single stalled browser would freeze the executor")
	}
}

func TestCloseWebSocketsDisconnectsClients(t *testing.T) {
	s, ts, cookie := wsTestServer(t)
	conn := dialWS(t, ts, cookie, ts.URL)

	waitForClients(t, s, 1)
	s.closeWebSockets()

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Error("connection still readable after closeWebSockets")
	}
}

func waitForClients(t *testing.T, s *Server, want int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.wsMu.RLock()
		n := len(s.wsClients)
		s.wsMu.RUnlock()
		if n == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d websocket clients", want)
}
