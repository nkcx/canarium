package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strconv"
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

	// MaxPasswordLength bounds what will be hashed. bcrypt pre-hashing means
	// length is not a correctness problem, but an unauthenticated endpoint
	// should not accept megabytes to hash.
	MaxPasswordLength = 1024

	// sessionReapInterval is how often expired sessions are pruned.
	sessionReapInterval = 1 * time.Hour

	// maxRequestBody caps how much of a request body is read. Every API
	// request carries at most a small JSON object; without a cap an
	// unauthenticated client could stream an unbounded body at the decoder.
	maxRequestBody = 64 << 10
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
	wsClients map[*wsClient]struct{}

	// version is reported by the health endpoint.
	version string

	// loginLimiter throttles repeated failed logins per source address.
	loginLimiter *failureLimiter

	// setupLimiter throttles first-run setup attempts, which are
	// unauthenticated and expensive.
	setupLimiter *failureLimiter

	// proxies decides whether a request's forwarding headers may be
	// believed.
	proxies *proxyTruster

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
		wsClients: make(map[*wsClient]struct{}),
		loginLimiter: newFailureLimiter(
			maxLoginFailures, failureWindow, lockoutDuration),
		setupLimiter: newFailureLimiter(
			maxSetupAttempts, failureWindow, lockoutDuration),
		proxies: newProxyTruster(
			cfg.Canarium.Auth.TrustProxyHeaders,
			cfg.Canarium.Auth.TrustedProxies,
			logger),
		ctx:    ctx,
		cancel: cancel,
	}

	s.routes()
	return s
}

// SetVersion records the build version for the health endpoint.
func (s *Server) SetVersion(v string) { s.version = v }

func (s *Server) routes() {
	// Unauthenticated: the healthcheck and the endpoints needed to
	// bootstrap or establish a session.
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	s.mux.HandleFunc("POST /api/auth/setup", s.handleSetup)
	s.mux.HandleFunc("POST /api/auth/password", s.requireScope(state.ScopeAdmin, s.handleChangePassword))

	// Read scope: observing the system.
	s.mux.HandleFunc("GET /api/status", s.requireScope(state.ScopeRead, s.handleStatus))
	s.mux.HandleFunc("GET /api/facts", s.requireScope(state.ScopeRead, s.handleFacts))
	s.mux.HandleFunc("GET /api/clients", s.requireScope(state.ScopeRead, s.handleClients))
	s.mux.HandleFunc("GET /api/plans", s.requireScope(state.ScopeRead, s.handlePlans))
	s.mux.HandleFunc("GET /api/sequence", s.requireScope(state.ScopeRead, s.handleSequence))
	s.mux.HandleFunc("GET /api/ws", s.requireScope(state.ScopeRead, s.handleWebSocket))

	// Admin scope: anything that changes what the daemon will do.
	s.mux.HandleFunc("POST /api/mode", s.requireScope(state.ScopeAdmin, s.handleSetMode))
	s.mux.HandleFunc("POST /api/abort", s.requireScope(state.ScopeAdmin, s.handleAbort))
	s.mux.HandleFunc("POST /api/sequence/proceed", s.requireScope(state.ScopeAdmin, s.handleProceed))

	s.routeWebUI()
}

// routeWebUI serves the embedded single-page application.
func (s *Server) routeWebUI() {
	webContent, err := fs.Sub(s.webFS, "web/dist")
	if err != nil {
		s.logger.Warn("embedded web UI is unavailable; serving the API only", "error", err)
		s.mux.HandleFunc("/", s.handleMissingUI)
		return
	}

	// A binary built without running the frontend build embeds only the
	// placeholder that keeps the go:embed pattern satisfiable. Detect that
	// and say so, rather than returning a bare 404 that looks like a routing
	// bug.
	if _, err := fs.Stat(webContent, "index.html"); err != nil {
		s.logger.Warn("this binary was built without the web UI; " +
			"run `make build` to include it")
		s.mux.HandleFunc("/", s.handleMissingUI)
		return
	}

	fileServer := http.FileServer(http.FS(webContent))

	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Serve index.html for any path the bundle does not contain, so
		// deep links and browser reloads reach the SPA router instead of a
		// 404. Anything under /api is already claimed by a more specific
		// pattern, so it never reaches here.
		if _, err := fs.Stat(webContent, strings.TrimPrefix(path.Clean(r.URL.Path), "/")); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}

		// The UI is a build artefact keyed by content hash, but index.html
		// itself must not be cached or a deploy leaves stale asset
		// references behind.
		if r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".html") {
			w.Header().Set("Cache-Control", "no-cache")
		}

		fileServer.ServeHTTP(w, r)
	})
}

// handleMissingUI explains that this binary carries no web UI.
func (s *Server) handleMissingUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprint(w, "This Canarium binary was built without the web UI.\n\n"+
		"Build it with `make build`, which compiles the frontend into\n"+
		"web/dist before embedding it. The API is unaffected and is\n"+
		"available under /api.\n")
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
	s.closeWebSockets()

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
		n, err := s.db.DeleteExpiredSessions(s.ctx)
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
	s.writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"mode":    s.executor.Mode().String(),
		"version": s.version,
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	// Snapshot() reads the whole sequence under one lock acquisition, so the
	// response can never show a half-updated state. Reading the fields
	// individually raced with the executor goroutine writing them.
	var seqData any
	if seq := s.executor.ActiveSequence(); seq != nil {
		seqData = seq.Snapshot()
	}

	clientStates := s.executor.GetAllClientStates()
	clients := make(map[string]string, len(clientStates))
	for name, st := range clientStates {
		clients[name] = st.String()
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"mode":     s.executor.Mode().String(),
		"sequence": seqData,
		"clients":  clients,
	})
}

func (s *Server) handleFacts(w http.ResponseWriter, r *http.Request) {
	type factInfo struct {
		Value       any       `json:"value"`
		Quality     string    `json:"quality"`
		UpdatedAt   time.Time `json:"updated_at"`
		Type        string    `json:"type,omitempty"`
		Unit        string    `json:"unit,omitempty"`
		Description string    `json:"description,omitempty"`
	}

	declarations := s.store.AllDeclarations()

	result := make(map[string]factInfo)
	for key, f := range s.store.AllFacts() {
		value, quality, updated := f.Get()

		info := factInfo{Value: value, Quality: quality.String(), UpdatedAt: updated}

		// Type, unit and description come from the source's declaration.
		// Without them the UI cannot tell 3600 seconds of runtime from 3600
		// of anything else, and rendered every number to one decimal place.
		if decl, ok := declarations[key]; ok && decl != nil {
			info.Type = decl.Type
			info.Unit = decl.Unit
			info.Description = decl.Description
		}

		result[key] = info
	}

	s.writeJSON(w, http.StatusOK, result)
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
	s.writeJSON(w, http.StatusOK, clients)
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
	s.writeJSON(w, http.StatusOK, plans)
}

func (s *Server) handleSequence(w http.ResponseWriter, r *http.Request) {
	seq := s.executor.ActiveSequence()
	if seq == nil {
		s.writeJSON(w, http.StatusOK, nil)
		return
	}
	s.writeJSON(w, http.StatusOK, seq.Snapshot())
}

func (s *Server) handleSetMode(w http.ResponseWriter, r *http.Request) {
	// config_readonly makes the file authoritative. Accepting a mode change
	// that the next deploy would silently revert is exactly the drift this
	// setting exists to prevent.
	if s.cfg.Canarium.ConfigReadonly {
		s.writeJSON(w, http.StatusConflict, map[string]string{
			"error": "config_readonly is set; change canarium.mode in the " +
				"configuration file and restart",
		})
		return
	}

	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody)).Decode(&req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	// ParseMode falls back to disarmed for anything unrecognised, which
	// would quietly turn a typo into a disarmed system.
	mode, ok := engine.ParseModeStrict(req.Mode)
	if !ok {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid mode " + req.Mode + " (expected disarmed, dry-run or armed)",
		})
		return
	}

	previous := s.executor.Mode()
	s.executor.SetMode(mode)

	if err := s.db.SetKV(r.Context(), "mode", mode.String()); err != nil {
		s.logger.Error("persisting mode", "error", err)
	}

	s.logger.Warn("operating mode changed",
		"from", previous.String(), "to", mode.String(),
		"source", clientIP(r, s.trustedProxy(r)))

	s.writeJSON(w, http.StatusOK, map[string]string{"mode": mode.String()})
}

func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	// A body is optional; an empty one just means no reason was given.
	_ = json.NewDecoder(io.LimitReader(r.Body, maxRequestBody)).Decode(&req)

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "requested via API"
	}

	if err := s.executor.AbortSequence(reason); err != nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	s.logger.Info("abort requested via API",
		"source", clientIP(r, s.trustedProxy(r)), "reason", reason)

	s.writeJSON(w, http.StatusAccepted, map[string]string{"status": "abort requested"})
}

// handleProceed releases a stage that is holding on an entry condition which
// is not going to arrive. wait_policy: hold is documented as requiring
// manual intervention; this is that intervention.
func (s *Server) handleProceed(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, maxRequestBody)).Decode(&req)

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "requested via API"
	}

	if err := s.executor.ForceStage(reason); err != nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	s.logger.Warn("operator forced the current stage to proceed",
		"source", clientIP(r, s.trustedProxy(r)), "reason", reason)

	s.writeJSON(w, http.StatusAccepted, map[string]string{"status": "proceed requested"})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	source := clientIP(r, s.trustedProxy(r))

	if allowed, retryAfter := s.loginLimiter.Allow(source); !allowed {
		s.logger.Warn("login attempt refused; source is locked out",
			"source", source, "retry_after", retryAfter.Round(time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		s.writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"error": "too many failed attempts; try again later",
		})
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody)).Decode(&req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	storedHash, err := s.passwordHash(r.Context())
	if err != nil {
		s.logger.Error("reading password hash", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if storedHash == "" {
		s.writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error":          "setup required",
			"setup_required": true,
		})
		return
	}

	valid, needsUpgrade := verifyPassword(storedHash, req.Password)
	if !valid {
		s.loginLimiter.RecordFailure(source)
		s.logger.Warn("failed login attempt", "source", source)

		// Slow every rejection slightly. This costs a human who mistyped
		// their password nothing and meaningfully bounds an attacker who
		// spreads guesses across source addresses to evade the per-source
		// threshold.
		select {
		case <-time.After(loginFailureDelay):
		case <-r.Context().Done():
		}

		s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid password"})
		return
	}

	s.loginLimiter.Reset(source)

	// Transparently migrate a legacy unsalted SHA-256 hash to bcrypt now that
	// we hold the plaintext. A failure here must not block the login, and a
	// config-pinned hash is not ours to rewrite.
	if needsUpgrade && !s.passwordIsPinned() {
		if upgraded, err := hashPassword(req.Password); err != nil {
			s.logger.Error("re-hashing legacy password", "error", err)
		} else if err := s.db.SetPasswordHash(r.Context(), upgraded); err != nil {
			s.logger.Error("storing upgraded password hash", "error", err)
		} else {
			s.logger.Info("upgraded stored password hash from legacy SHA-256 to bcrypt")
		}
	}

	token, err := newSessionToken()
	if err != nil {
		s.logger.Error("generating session token", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	http.SetCookie(w, s.sessionCookie(r, token, int(sessionTTL.Seconds())))

	if err := s.db.CreateSession(r.Context(), hashToken(token), sessionTTL); err != nil {
		s.logger.Error("persisting session", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleLogout invalidates the caller's session and clears the cookie.
//
// It is intentionally not wrapped in requireAuth: logging out with an
// already-invalid session should succeed quietly rather than return 401,
// and there is nothing to protect.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		if err := s.db.DeleteSession(r.Context(), hashToken(cookie.Value)); err != nil {
			s.logger.Error("deleting session", "error", err)
		}
	}

	// MaxAge < 0 instructs the browser to delete the cookie immediately.
	http.SetCookie(w, s.sessionCookie(r, "", -1))
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
	if !s.trustedProxy(r) {
		return false
	}
	// Only consulted for requests that actually arrived from a trusted
	// proxy; these headers are attacker-controlled otherwise.
	if proto := r.Header.Get("X-Forwarded-Proto"); strings.EqualFold(proto, "https") {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Ssl"), "on")
}

// passwordHash returns the authoritative admin password hash.
//
// A hash pinned in the configuration file wins over the database, so an
// immutable deployment with an ephemeral database does not present a
// first-run setup screen — and a window in which anyone could claim it —
// on every restart.
func (s *Server) passwordHash(ctx context.Context) (string, error) {
	if pinned := strings.TrimSpace(s.cfg.Canarium.Auth.PasswordHash); pinned != "" {
		return pinned, nil
	}
	return s.db.GetPasswordHash(ctx)
}

func (s *Server) passwordIsPinned() bool {
	return strings.TrimSpace(s.cfg.Canarium.Auth.PasswordHash) != ""
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	// Setup is unauthenticated and hashes its input with bcrypt, which on
	// the single-board hardware this targets costs about a second of a core.
	// Without throttling, an anonymous client could saturate every core and
	// starve the probe and policy loops — a denial of service against the
	// thing that is supposed to be watching the power.
	source := clientIP(r, s.trustedProxy(r))
	if allowed, retryAfter := s.setupLimiter.Allow(source); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		s.writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"error": "too many setup attempts; try again later",
		})
		return
	}
	s.setupLimiter.RecordFailure(source)

	if s.passwordIsPinned() {
		s.writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "the admin password is set in the configuration file",
		})
		return
	}

	existing, err := s.db.GetPasswordHash(r.Context())
	if err != nil {
		s.logger.Error("reading password hash", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if existing != "" {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "password already set"})
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody)).Decode(&req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	if err := validatePasswordStrength(req.Password); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	hash, err := hashPassword(req.Password)
	if err != nil {
		s.logger.Error("hashing password", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	// Atomic: the check above and this write are separated by a bcrypt call
	// lasting a second or more on the hardware this targets, so two requests
	// arriving together could both pass the check. Only one insert can win.
	claimed, err := s.db.ClaimInitialPassword(r.Context(), hash)
	if err != nil {
		s.logger.Error("saving password hash", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save password"})
		return
	}
	if !claimed {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "password already set"})
		return
	}

	s.setupLimiter.Reset(source)
	s.logger.Info("admin password set", "source", source)

	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleChangePassword rotates the admin password.
//
// There was previously no way to change it at all: once set through
// first-run setup, an operator could not rotate a password they believed
// compromised without deleting the database. DeleteAllSessions existed for
// exactly this and had no caller.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if s.passwordIsPinned() {
		s.writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "the admin password is set in the configuration file",
		})
		return
	}

	var req struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody)).Decode(&req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	storedHash, err := s.passwordHash(r.Context())
	if err != nil {
		s.logger.Error("reading password hash", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	// Holding a session is not enough: an unattended browser must not be
	// able to lock the real operator out.
	if ok, _ := verifyPassword(storedHash, req.Current); !ok {
		s.logger.Warn("password change rejected: current password incorrect",
			"source", clientIP(r, s.trustedProxy(r)))
		s.writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "current password is incorrect",
		})
		return
	}

	if err := validatePasswordStrength(req.New); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.New == req.Current {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "the new password must differ from the current one",
		})
		return
	}

	hash, err := hashPassword(req.New)
	if err != nil {
		s.logger.Error("hashing password", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if err := s.db.SetPasswordHash(r.Context(), hash); err != nil {
		s.logger.Error("saving password hash", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save password"})
		return
	}

	// Every existing session was issued against the old password. If the
	// reason for rotating is a suspected compromise, leaving them valid
	// defeats the point.
	invalidated, err := s.db.DeleteAllSessions(r.Context())
	if err != nil {
		s.logger.Error("invalidating sessions after a password change", "error", err)
	}

	s.logger.Warn("admin password changed",
		"source", clientIP(r, s.trustedProxy(r)), "sessions_invalidated", invalidated)

	http.SetCookie(w, s.sessionCookie(r, "", -1))
	s.writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"detail": "all sessions have been signed out; log in again",
	})
}

// validatePasswordStrength applies the minimum requirements.
func validatePasswordStrength(password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	if len(password) > MaxPasswordLength {
		return fmt.Errorf("password must be at most %d characters", MaxPasswordLength)
	}
	if strings.TrimSpace(password) == "" {
		return fmt.Errorf("password cannot be only whitespace")
	}
	return nil
}

// requireScope wraps a handler so it is reachable only by a caller holding at
// least the given scope.
//
// This fails closed. An earlier version admitted every request when no
// password had been configured, on the assumption that an operator would set
// one during first-run setup. Combined with a web UI that could not reach the
// setup endpoint, that left the entire API — including the endpoint that arms
// the executor — permanently open on a default install.
//
// A fresh install with no password rejects every authenticated endpoint and
// reports setupRequired, which is what drives the UI to its first-run screen.
// /api/auth/status and /api/auth/setup stay open so bootstrap is possible;
// setup itself refuses once a password exists.
//
// Session cookies carry admin scope: the single local admin is the operator.
// API tokens carry whatever scope they were issued with, so a monitoring
// integration can be given a read-only token that cannot arm the executor or
// abort a sequence.
func (s *Server) requireScope(required string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Database work runs under the request context: a client that
		// disconnects mid-request stops the query behind it, and a slow
		// database cannot pin a handler goroutine after the caller has gone.
		ctx := r.Context()

		storedHash, err := s.passwordHash(ctx)
		if err != nil {
			s.logger.Error("reading password hash", "error", err)
			s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}

		if storedHash == "" {
			s.writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error":          "setup required",
				"setup_required": true,
			})
			return
		}

		scope, ok := s.authenticate(ctx, r)
		if !ok {
			s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		if !scopeAllows(scope, required) {
			s.logger.Warn("rejecting a request outside the credential's scope",
				"path", r.URL.Path, "have", scope, "need", required)
			s.writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "this credential has " + scope + " scope; " + required + " is required",
			})
			return
		}

		next(w, r)
	}
}

// scopeAllows reports whether a held scope satisfies a requirement.
func scopeAllows(held, required string) bool {
	if held == state.ScopeAdmin {
		return true
	}
	return held == required
}

// authenticate reports whether the request carries a valid API token or a
// valid, unexpired session cookie. It deliberately returns only a boolean:
// distinguishing "no credential", "unknown credential" and "expired
// credential" to the caller would let an unauthenticated client probe for
// valid tokens.
func (s *Server) authenticate(ctx context.Context, r *http.Request) (scope string, ok bool) {

	if token := bearerToken(r); token != "" {
		scope, err := s.db.ValidateAPIToken(ctx, hashToken(token))
		if err != nil {
			s.logger.Error("validating API token", "error", err)
			return "", false
		}
		if scope != "" {
			return scope, true
		}
	}

	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", false
	}

	valid, err := s.db.SessionIsValid(ctx, hashToken(cookie.Value))
	if err != nil {
		s.logger.Error("validating session", "error", err)
		return "", false
	}
	if !valid {
		return "", false
	}

	// A browser session belongs to the single local admin.
	return state.ScopeAdmin, true
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
	ctx := r.Context()

	storedHash, err := s.passwordHash(ctx)
	if err != nil {
		s.logger.Error("reading password hash", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	setupRequired := storedHash == ""

	var authenticated bool
	var scope string
	if !setupRequired {
		scope, authenticated = s.authenticate(ctx, r)
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"setup_required":      setupRequired,
		"authenticated":       authenticated,
		"scope":               scope,
		"min_password_length": MinPasswordLength,
		"password_pinned":     s.passwordIsPinned(),
		"config_readonly":     s.cfg.Canarium.ConfigReadonly,
	})
}

// writeJSON renders a response body.
//
// An encoding failure part way through leaves the client holding a truncated
// document with a success status already sent — the header is long gone by
// then, so there is nothing to do but record it. Silently discarding the
// error made that indistinguishable from a healthy response.
func (s *Server) writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		s.logger.Error("writing response body", "status", status, "error", err)
	}
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
