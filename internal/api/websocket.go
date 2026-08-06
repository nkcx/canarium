package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nkcx/canarium/internal/engine"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type wsClient struct {
	conn   *websocket.Conn
	send   chan []byte
	server *Server
	mu     sync.Mutex
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("websocket upgrade failed", "error", err)
		return
	}

	client := &wsClient{
		conn:   conn,
		send:   make(chan []byte, 256),
		server: s,
	}

	s.wsMu.Lock()
	s.wsClients[client] = true
	s.wsMu.Unlock()

	go client.writePump()
	go client.readPump()
}

func (c *wsClient) writePump() {
	defer func() {
		c.conn.Close()
		c.server.wsMu.Lock()
		delete(c.server.wsClients, c)
		c.server.wsMu.Unlock()
	}()

	for msg := range c.send {
		c.mu.Lock()
		c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		err := c.conn.WriteMessage(websocket.TextMessage, msg)
		c.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (c *wsClient) readPump() {
	defer func() {
		close(c.send)
		c.conn.Close()
	}()

	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
	}
}

func (s *Server) BroadcastEvent(evt engine.Event) {
	data, err := json.Marshal(evt)
	if err != nil {
		s.logger.Error("marshaling event for ws", "error", err)
		return
	}

	s.wsMu.RLock()
	defer s.wsMu.RUnlock()

	for client := range s.wsClients {
		select {
		case client.send <- data:
		default:
			s.logger.Warn("dropping ws message, client buffer full")
		}
	}
}

func (s *Server) EventListener() engine.EventListener {
	return func(evt engine.Event) {
		s.BroadcastEvent(evt)
	}
}

var _ slog.Handler = (*slog.TextHandler)(nil)
