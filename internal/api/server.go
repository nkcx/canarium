package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
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
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Transport   string            `json:"transport"`
		Address     string            `json:"address"`
		Tags        []string          `json:"tags"`
		Feeds       []string          `json:"feeds"`
		FeedPolicy  string            `json:"feed_policy"`
		WakePolicy  string            `json:"wake_policy"`
		DependsOn   []string          `json:"depends_on"`
		State       string            `json:"state"`
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

	inputHash := hashPassword(req.Password)
	if subtle.ConstantTimeCompare([]byte(inputHash), []byte(storedHash)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid password"})
		return
	}

	token := generateSessionToken()
	http.SetCookie(w, &http.Cookie{
		Name:     "canarium_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400,
	})

	s.db.SetKV("session:"+hashToken(token), time.Now().Add(24*time.Hour).Format(time.RFC3339))

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

	if len(req.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password must be at least 8 characters"})
		return
	}

	hash := hashPassword(req.Password)
	if err := s.db.SetPasswordHash(hash); err != nil {
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

		cookie, err := r.Cookie("canarium_session")
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		sessionKey := "session:" + hashToken(cookie.Value)
		expiryStr, err := s.db.GetKV(sessionKey)
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

func hashPassword(password string) string {
	h := sha256.Sum256([]byte(password))
	return hex.EncodeToString(h[:])
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func generateSessionToken() string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(time.Now().UnixNano() >> (i % 8))
	}
	return hex.EncodeToString(b)
}
