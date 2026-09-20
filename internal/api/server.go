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
	"strings"
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

	// sessionReapInterval is how often expired sessions are pruned.
	sessionReapInterval = 1 * time.Hour
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

	// ctx is cancelled by Stop and bounds the server's background
	// goroutines.
	ctx    context.Context
	cancel context.CancelFunc
}

func NewServer(
	cfg *config.Config,
	store *facts.Store,
	executor *engine.Executor,
	db *state.DB,
	webFS embed.FS,
	logger *slog.Logger,
) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		cfg:       cfg,
		store:     store,
		executor:  executor,
		db:        db,
		logger:    logger,
		webFS:     webFS,
		mux:       http.NewServeMux(),
		wsClients: make(map[*wsClient]bool),
		ctx:       ctx,
		cancel:    cancel,
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
	s.mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
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

		// Bound how long a slow or malicious client can hold a connection.
		// WriteTimeout is deliberately absent: the WebSocket endpoint holds
		// its connection open indefinitely by design and sets its own
		// per-message deadlines.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go s.reapExpiredSessions()

	s.logger.Info("API server starting", "addr", addr)
	return s.server.ListenAndServe()
}

// Stop shuts the HTTP server down gracefully and stops background work.
//
// Calling Stop before Start is safe; an earlier version dereferenced a nil
// http.Server in that case, so a failure during startup turned into a panic
// during shutdown.
func (s *Server) Stop(ctx context.Context) error {
	s.cancel()

	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

// reapExpiredSessions periodically removes sessions past their expiry.
//
// Without this the table grows without bound: the previous kv-backed
// implementation never deleted anything, so every login ever performed left a
// row behind forever.
func (s *Server) reapExpiredSessions() {
	ticker := time.NewTicker(sessionReapInterval)
	defer ticker.Stop()

	prune := func() {
		n, err := s.db.DeleteExpiredSessions()
		if err != nil {
			s.logger.Error("pruning expired sessions", "error", err)
			return
		}
		if n > 0 {
			s.logger.Debug("pruned expired sessions", "count", n)
		}
	}

	prune()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
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

	http.SetCookie(w, s.sessionCookie(r, token, int(sessionTTL.Seconds())))

	if err := s.db.CreateSession(hashToken(token), sessionTTL); err != nil {
		s.logger.Error("persisting session", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleLogout invalidates the caller's session and clears the cookie.
//
// It is intentionally not wrapped in requireAuth: logging out with an
// already-invalid session should succeed quietly rather than return 401,
// and there is nothing to protect.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		if err := s.db.DeleteSession(hashToken(cookie.Value)); err != nil {
			s.logger.Error("deleting session", "error", err)
		}
	}

	// MaxAge < 0 instructs the browser to delete the cookie immediately.
	http.SetCookie(w, s.sessionCookie(r, "", -1))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// sessionCookie builds the session cookie with the security attributes
// appropriate to how the request arrived.
//
// Secure is set when the connection is TLS, or when a trusted reverse proxy
// reports that the original request was — Canarium terminates no TLS itself
// and is documented to run behind a proxy. It is deliberately not set
// unconditionally: on a plain-HTTP deployment a Secure cookie is silently
// discarded by the browser, which would make login appear to succeed and
// then fail on every subsequent request.
func (s *Server) sessionCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.requestIsSecure(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	}
}

func (s *Server) requestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !s.cfg.Canarium.Auth.TrustProxyHeaders {
		return false
	}
	// Only consulted when the operator has opted in, because these headers
	// are attacker-controlled when the daemon is reachable directly.
	if proto := r.Header.Get("X-Forwarded-Proto"); strings.EqualFold(proto, "https") {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Ssl"), "on")
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	existing, err := s.db.GetPasswordHash()
	if err != nil {
		s.logger.Error("reading password hash", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
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

// requireAuth wraps a handler so that it is reachable only by an
// authenticated caller.
//
// This fails closed. An earlier version admitted every request when no
// password had been configured, on the assumption that an operator would set
// one during first-run setup. Combined with a web UI that could not reach
// the setup endpoint, that left the entire API — including the endpoint that
// arms the executor — permanently open on a default install.
//
// A fresh install with no password therefore rejects every authenticated
// endpoint and reports setupRequired, which is what drives the UI to the
// first-run screen. /api/auth/status and /api/auth/setup stay open so that
// bootstrap is possible; setup itself refuses once a password exists.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		storedHash, err := s.db.GetPasswordHash()
		if err != nil {
			s.logger.Error("reading password hash", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}

		if storedHash == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error":          "setup required",
				"setup_required": true,
			})
			return
		}

		if s.authenticate(r) {
			next(w, r)
			return
		}

		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
}

// authenticate reports whether the request carries a valid API token or a
// valid, unexpired session cookie. It deliberately returns only a boolean:
// distinguishing "no credential", "unknown credential" and "expired
// credential" to the caller would let an unauthenticated client probe for
// valid tokens.
func (s *Server) authenticate(r *http.Request) bool {
	if token := bearerToken(r); token != "" {
		scope, err := s.db.ValidateAPIToken(hashToken(token))
		if err != nil {
			s.logger.Error("validating API token", "error", err)
			return false
		}
		if scope != "" {
			return true
		}
	}

	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}

	valid, err := s.db.SessionIsValid(hashToken(cookie.Value))
	if err != nil {
		s.logger.Error("validating session", "error", err)
		return false
	}
	return valid
}

// bearerToken extracts a token from the Authorization header, accepting both
// "Bearer <token>" and a bare token for backwards compatibility.
func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return ""
	}
	if after, ok := strings.CutPrefix(header, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	return header
}

// handleAuthStatus reports whether first-run setup is still required and
// whether the caller is already authenticated. It is unauthenticated by
// design: the UI must be able to decide between the setup screen, the login
// screen and the dashboard before it holds any credential.
//
// It reveals only whether a password exists, which is not sensitive and is
// already implied by the behaviour of every other endpoint.
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	storedHash, err := s.db.GetPasswordHash()
	if err != nil {
		s.logger.Error("reading password hash", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	setupRequired := storedHash == ""

	writeJSON(w, http.StatusOK, map[string]any{
		"setup_required":      setupRequired,
		"authenticated":       !setupRequired && s.authenticate(r),
		"min_password_length": MinPasswordLength,
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
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
