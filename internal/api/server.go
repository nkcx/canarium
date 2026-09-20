package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/facts"
	"github.com/nkcx/canarium/internal/state"
)

const (
	// sessionTokenBytes is the size of a raw session token before hex
	// encoding. 256 bits, matching the SHA-256 digest it is stored under.
	sessionTokenBytes = 32

	// sessionTTL is how long a session cookie remains valid.
	sessionTTL = 24 * time.Hour

	// sessionCookieName is the cookie carrying the session token.
	sessionCookieName = "canarium_session"

	// sessionKeyPrefix namespaces session rows in the kv table.
	sessionKeyPrefix = "session:"
)

type Server struct {
	cfg      *config.Config
	store    *facts.Store
	executor *engine.Executor
	db       *state.DB
	logger   *slog.Logger
	webFS    embed.FS
	mux      *http.ServeMux
	server   *http.Server

	wsMu      sync.RWMutex
	wsClients map[*wsClient]bool
}

func NewServer(
	cfg *config.Config,
	store *facts.Store,
	executor *engine.Executor,
	db *state.DB,
	webFS embed.FS,
	logger *slog.Logger,
) *Server {
	s := &Server{
		cfg:       cfg,
		store:     store,
		executor:  executor,
		db:        db,
		logger:    logger,
		webFS:     webFS,
		mux:       http.NewServeMux(),
		wsClients: make(map[*wsClient]bool),
	}

	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/status", s.requireAuth(s.handleStatus))
	s.mux.HandleFunc("GET /api/facts", s.requireAuth(s.handleFacts))
	s.mux.HandleFunc("GET /api/clients", s.requireAuth(s.handleClients))
	s.mux.HandleFunc("GET /api/plans", s.requireAuth(s.handlePlans))
	s.mux.HandleFunc("GET /api/sequence", s.requireAuth(s.handleSequence))
	s.mux.HandleFunc("POST /api/mode", s.requireAuth(s.handleSetMode))
	s.mux.HandleFunc("POST /api/abort", s.requireAuth(s.handleAbort))
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/setup", s.handleSetup)
	s.mux.HandleFunc("GET /api/ws", s.requireAuth(s.handleWebSocket))

	webContent, err := fs.Sub(s.webFS, "web/dist")
	if err != nil {
		s.logger.Warn("web UI not found in embedded FS, serving API only")
		return
	}
	fileServer := http.FileServer(http.FS(webContent))
	s.mux.Handle("/", fileServer)
}

func (s *Server) Start(addr string) error {
	s.server = &http.Server{
		Addr:    addr,
		Handler: s.mux,
	}

	s.logger.Info("API server starting", "addr", addr)
	return s.server.ListenAndServe()
}

func (s *Server) Stop(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"mode":   s.executor.Mode().String(),
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	seq := s.executor.GetActiveSequence()
	var seqData any
	if seq != nil {
		seqData = map[string]any{
			"id":            seq.Sequence.ID,
			"plan":          seq.Sequence.PlanName,
			"state":         seq.Sequence.State,
			"current_stage": seq.Sequence.CurrentStage,
			"ponr_crossed":  seq.Sequence.PonrCrossed,
			"started_at":    seq.Sequence.StartedAt,
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"mode":     s.executor.Mode().String(),
		"sequence": seqData,
		"clients":  s.executor.GetAllClientStates(),
	})
}

func (s *Server) handleFacts(w http.ResponseWriter, r *http.Request) {
	allFacts := s.store.AllFacts()
	result := make(map[string]any)
	for key, f := range allFacts {
		val, quality, updated := f.Get()
		result[key] = map[string]any{
			"value":      val,
			"quality":    quality.String(),
			"updated_at": updated,
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleClients(w http.ResponseWriter, r *http.Request) {
	type clientInfo struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Transport   string   `json:"transport"`
		Address     string   `json:"address"`
		Tags        []string `json:"tags"`
		Feeds       []string `json:"feeds"`
		FeedPolicy  string   `json:"feed_policy"`
		WakePolicy  string   `json:"wake_policy"`
		DependsOn   []string `json:"depends_on"`
		State       string   `json:"state"`
	}

	var clients []clientInfo
	for _, c := range s.cfg.Clients {
		clients = append(clients, clientInfo{
			Name:        c.Name,
			Description: c.Description,
			Transport:   c.Transport,
			Address:     c.Address,
			Tags:        c.Tags,
			Feeds:       c.Feeds,
			FeedPolicy:  c.FeedPolicy,
			WakePolicy:  c.WakePolicy,
			DependsOn:   c.DependsOn,
			State:       s.executor.GetClientState(c.Name).String(),
		})
	}
	writeJSON(w, http.StatusOK, clients)
}

func (s *Server) handlePlans(w http.ResponseWriter, r *http.Request) {
	type planInfo struct {
		Name   string `json:"name"`
		Stages int    `json:"stages"`
	}

	var plans []planInfo
	for _, p := range s.cfg.Plans {
		plans = append(plans, planInfo{
			Name:   p.Name,
			Stages: len(p.Shutdown.Stages),
		})
	}
	writeJSON(w, http.StatusOK, plans)
}

func (s *Server) handleSequence(w http.ResponseWriter, r *http.Request) {
	seq := s.executor.GetActiveSequence()
	if seq == nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	writeJSON(w, http.StatusOK, seq.Sequence)
}

func (s *Server) handleSetMode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	mode := engine.ParseMode(req.Mode)
	s.executor.SetMode(mode)
	s.db.SetKV("mode", mode.String())

	writeJSON(w, http.StatusOK, map[string]string{"mode": mode.String()})
}

func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	if err := s.executor.AbortSequence(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "aborted"})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	storedHash, err := s.db.GetPasswordHash()
	if err != nil || storedHash == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no password set"})
		return
	}

	valid, needsUpgrade := verifyPassword(storedHash, req.Password)
	if !valid {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid password"})
		return
	}

	// Transparently migrate a legacy unsalted SHA-256 hash to bcrypt now that
	// we hold the plaintext. A failure here must not block the login.
	if needsUpgrade {
		if upgraded, err := hashPassword(req.Password); err != nil {
			s.logger.Error("re-hashing legacy password", "error", err)
		} else if err := s.db.SetPasswordHash(upgraded); err != nil {
			s.logger.Error("storing upgraded password hash", "error", err)
		} else {
			s.logger.Info("upgraded stored password hash from legacy SHA-256 to bcrypt")
		}
	}

	token, err := newSessionToken()
	if err != nil {
		s.logger.Error("generating session token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})

	if err := s.db.SetKV(sessionKey(token), time.Now().Add(sessionTTL).Format(time.RFC3339)); err != nil {
		s.logger.Error("persisting session", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	existing, _ := s.db.GetPasswordHash()
	if existing != "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "password already set"})
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	if len(req.Password) < MinPasswordLength {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("password must be at least %d characters", MinPasswordLength),
		})
		return
	}

	hash, err := hashPassword(req.Password)
	if err != nil {
		s.logger.Error("hashing password", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if err := s.db.SetPasswordHash(hash); err != nil {
		s.logger.Error("saving password hash", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save password"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		storedHash, _ := s.db.GetPasswordHash()
		if storedHash == "" {
			next(w, r)
			return
		}

		if token := r.Header.Get("Authorization"); token != "" {
			tokenHash := hashToken(token)
			scope, err := s.db.ValidateAPIToken(tokenHash)
			if err == nil && scope != "" {
				next(w, r)
				return
			}
		}

		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		expiryStr, err := s.db.GetKV(sessionKey(cookie.Value))
		if err != nil || expiryStr == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid session"})
			return
		}

		expiry, err := time.Parse(time.RFC3339, expiryStr)
		if err != nil || time.Now().After(expiry) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session expired"})
			return
		}

		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// sessionKey is the kv-table key under which a session's expiry is stored.
// The raw token is never persisted, only its digest, so a database leak does
// not yield usable session cookies.
func sessionKey(token string) string {
	return sessionKeyPrefix + hashToken(token)
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// newSessionToken returns a cryptographically random 256-bit session token,
// hex-encoded.
//
// This must never be derived from the clock. A token seeded from
// time.Now() carries only the entropy of "when did this login happen",
// which an attacker who can observe or provoke a login can search
// exhaustively, and it degenerates further on platforms with coarse clock
// resolution.
func newSessionToken() (string, error) {
	b := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating session token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
