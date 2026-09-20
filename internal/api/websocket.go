package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nkcx/canarium/internal/engine"
)

const (
	// wsWriteTimeout bounds a single message write to a slow client.
	wsWriteTimeout = 10 * time.Second

	// wsPongTimeout is how long a connection may go without a pong before
	// it is considered dead.
	wsPongTimeout = 60 * time.Second

	// wsPingInterval must be comfortably shorter than wsPongTimeout so a
	// healthy connection is always refreshed before it expires.
	wsPingInterval = (wsPongTimeout * 9) / 10

	// wsSendBuffer is how many events may queue for one client before
	// messages are dropped. Sized for a burst of client state transitions
	// during a staged shutdown.
	wsSendBuffer = 256

	// wsMaxMessageSize caps inbound frames. Clients send nothing but
	// protocol-level pongs, so this is deliberately small.
	wsMaxMessageSize = 512
)

// wsClient is one connected browser.
//
// Lifecycle: handleWebSocket registers the client and starts both pumps.
// Either pump calling close() tears the connection down; readPump's exit
// unregisters it. The send channel is deliberately never closed — see
// BroadcastEvent.
type wsClient struct {
	conn   *websocket.Conn
	send   chan []byte
	server *Server

	closeOnce sync.Once
	done      chan struct{}
}

// close tears down the connection exactly once and signals both pumps.
func (c *wsClient) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		c.conn.Close()
	})
}

// checkOrigin enforces same-origin for WebSocket upgrades.
//
// The previous implementation returned true unconditionally, accepting an
// upgrade from any page on the internet. Because the connection carries the
// session cookie, that is cross-site WebSocket hijacking: any site the
// operator visited could open a socket to Canarium and read its live event
// stream. SameSite=Strict mitigates it in current browsers, but the check
// belongs here rather than relying on the cookie policy alone.
//
// A missing Origin header is allowed: non-browser clients (canarium's own
// CLI tooling, curl, monitoring) do not send one, and they are not subject
// to the ambient-authority problem this defends against.
func (s *Server) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}

	u, err := url.Parse(origin)
	if err != nil {
		s.logger.Warn("rejecting websocket upgrade: unparseable Origin",
			"origin", origin, "error", err)
		return false
	}

	if strings.EqualFold(u.Host, r.Host) {
		return true
	}

	for _, allowed := range s.cfg.Canarium.Auth.AllowedOrigins {
		if strings.EqualFold(allowed, origin) || strings.EqualFold(allowed, u.Host) {
			return true
		}
	}

	s.logger.Warn("rejecting cross-origin websocket upgrade",
		"origin", origin, "host", r.Host)
	return false
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		CheckOrigin:     s.checkOrigin,
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written an error response.
		s.logger.Error("websocket upgrade failed", "error", err)
		return
	}

	client := &wsClient{
		conn:   conn,
		send:   make(chan []byte, wsSendBuffer),
		server: s,
		done:   make(chan struct{}),
	}

	s.registerWSClient(client)

	go client.writePump()
	go client.readPump()
}

func (s *Server) registerWSClient(c *wsClient) {
	s.wsMu.Lock()
	s.wsClients[c] = struct{}{}
	n := len(s.wsClients)
	s.wsMu.Unlock()

	s.logger.Debug("websocket client connected", "clients", n)
}

func (s *Server) unregisterWSClient(c *wsClient) {
	s.wsMu.Lock()
	delete(s.wsClients, c)
	n := len(s.wsClients)
	s.wsMu.Unlock()

	s.logger.Debug("websocket client disconnected", "clients", n)
}

// writePump serialises all writes to the connection and keeps it alive.
//
// Gorilla permits only one concurrent writer, so every write — data frames
// and pings alike — happens here.
func (c *wsClient) writePump() {
	ticker := time.NewTicker(wsPingInterval)
	defer func() {
		ticker.Stop()
		c.close()
	}()

	for {
		select {
		case msg := <-c.send:
			if err := c.write(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			// Without this the peer never pongs, readPump's deadline
			// expires, and every connection was torn down and rebuilt on a
			// 60-second cycle.
			if err := c.write(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-c.server.ctx.Done():
			// Server shutting down: tell the peer why rather than dropping
			// the TCP connection and leaving it to guess.
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			_ = c.conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"))
			return

		case <-c.done:
			return
		}
	}
}

func (c *wsClient) write(messageType int, payload []byte) error {
	if err := c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout)); err != nil {
		return err
	}
	return c.conn.WriteMessage(messageType, payload)
}

// readPump drains inbound frames and owns unregistration.
//
// Canarium's protocol is one-way, so nothing here interprets messages; the
// read loop exists to process control frames (pong, close) and to notice
// when the peer goes away.
func (c *wsClient) readPump() {
	defer func() {
		c.close()
		c.server.unregisterWSClient(c)
	}()

	c.conn.SetReadLimit(wsMaxMessageSize)
	if err := c.conn.SetReadDeadline(time.Now().Add(wsPongTimeout)); err != nil {
		return
	}
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(wsPongTimeout))
	})

	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				c.server.logger.Debug("websocket read error", "error", err)
			}
			return
		}
	}
}

// BroadcastEvent fans an executor event out to every connected client.
//
// Delivery is best-effort: a client whose buffer is full is skipped rather
// than allowed to block the executor goroutine that produced the event.
//
// The send channel is never closed. An earlier version closed it from
// readPump while the client was still registered, so a broadcast landing in
// that window panicked on send-to-closed-channel — and since this runs on
// the executor's goroutine with no recover anywhere, closing a browser tab
// at the wrong moment killed the daemon mid-outage.
func (s *Server) BroadcastEvent(evt engine.Event) {
	data, err := json.Marshal(evt)
	if err != nil {
		s.logger.Error("marshaling event for websocket", "error", err)
		return
	}

	s.wsMu.RLock()
	clients := make([]*wsClient, 0, len(s.wsClients))
	for c := range s.wsClients {
		clients = append(clients, c)
	}
	s.wsMu.RUnlock()

	for _, c := range clients {
		select {
		case c.send <- data:
		case <-c.done:
			// Client is going away; nothing to do.
		default:
			s.logger.Warn("websocket client is not keeping up; dropping event",
				"event", evt.Type)
		}
	}
}

// closeWebSockets tears down every connection. Called during shutdown so
// clients are not left waiting on a socket that will never produce anything.
func (s *Server) closeWebSockets() {
	s.wsMu.RLock()
	clients := make([]*wsClient, 0, len(s.wsClients))
	for c := range s.wsClients {
		clients = append(clients, c)
	}
	s.wsMu.RUnlock()

	for _, c := range clients {
		c.close()
	}
}

func (s *Server) EventListener() engine.EventListener {
	return func(evt engine.Event) {
		s.BroadcastEvent(evt)
	}
}
