package api

import (
	"embed"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/conditions"
	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/facts"
	"github.com/nkcx/canarium/internal/state"
)

// newServerWith builds a server over a caller-supplied config and fact
// store, for tests of what the read endpoints report.
func newServerWith(t *testing.T, cfg *config.Config, store *facts.Store) *Server {
	t.Helper()

	db, err := state.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("opening state database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if cfg.Canarium.Mode == "" {
		cfg.Canarium = config.DefaultCanariumConfig()
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	executor := engine.NewExecutor(cfg, store, conditions.NewEvaluator(store), db, logger)

	var emptyFS embed.FS
	s := NewServer(cfg, store, executor, db, emptyFS, logger)

	if rec := do(t, s, http.MethodPost, "/api/auth/setup", `{"password":"`+testPassword+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
	}
	return s
}

func getJSON(t *testing.T, s *Server, path string, out any) {
	t.Helper()
	rec := do(t, s, http.MethodGet, path, "", sessionCookie(t, s))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decoding %s: %v (%s)", path, err, rec.Body.String())
	}
}

// TestEmptyCollectionsAreArraysNotNull: with `clients: []` the clients
// endpoint returned null, so every consumer had to special-case the one
// configuration a new deployment is most likely to have.
func TestEmptyCollectionsAreArraysNotNull(t *testing.T) {
	s := newServerWith(t, &config.Config{}, facts.NewStore())

	for _, path := range []string{"/api/clients", "/api/plans"} {
		rec := do(t, s, http.MethodGet, path, "", sessionCookie(t, s))
		if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
			t.Errorf("GET %s with nothing configured = %s, want []", path, body)
		}
	}
}

// TestNeverReportedFactHasNoTimestamp: the zero time rendered in the UI as
// "17757122h ago" for every reading the UPS does not provide.
func TestNeverReportedFactHasNoTimestamp(t *testing.T) {
	store := facts.NewStore()
	store.RegisterSource("ups", time.Second, []facts.FactDeclaration{
		{Name: "charge", Type: "percent"},
		{Name: "temperature", Type: "number", Unit: "celsius"},
	})
	store.Update("ups.charge", 100.0, time.Now())

	s := newServerWith(t, &config.Config{}, store)

	var got map[string]map[string]any
	getJSON(t, s, "/api/facts", &got)

	if ts := got["ups.temperature"]["updated_at"]; ts != nil {
		t.Errorf("never-reported fact has updated_at %v, want null", ts)
	}
	if ts, _ := got["ups.charge"]["updated_at"].(string); ts == "" {
		t.Error("a reported fact lost its timestamp")
	}
}

func TestStatusReportsVersionAndConfigWarnings(t *testing.T) {
	s := newServerWith(t, &config.Config{}, facts.NewStore())
	s.SetVersion("0.1.1")
	s.SetConfigWarnings([]string{`plan "outage" stage "compute": tag "compute" matches no clients`})

	var got struct {
		Version        string   `json:"version"`
		ConfigWarnings []string `json:"config_warnings"`
	}
	getJSON(t, s, "/api/status", &got)

	if got.Version != "0.1.1" {
		t.Errorf("version = %q, want 0.1.1", got.Version)
	}
	if len(got.ConfigWarnings) != 1 || !strings.Contains(got.ConfigWarnings[0], "matches no clients") {
		t.Errorf("config_warnings = %v", got.ConfigWarnings)
	}
}

func TestStatusConfigWarningsAreAnArrayWhenEmpty(t *testing.T) {
	s := newServerWith(t, &config.Config{}, facts.NewStore())
	rec := do(t, s, http.MethodGet, "/api/status", "", sessionCookie(t, s))
	if !strings.Contains(rec.Body.String(), `"config_warnings":[]`) {
		t.Errorf("status body = %s, want config_warnings as []", rec.Body.String())
	}
}

func plansConfig() *config.Config {
	below := 50.0
	return &config.Config{
		Clients: []config.ClientConfig{
			{Name: "vanadium", Transport: "ssh", Tags: []string{"compute"}},
		},
		Plans: []config.PlanConfig{{
			Name:    "outage",
			Trigger: config.ConditionConfig{Condition: "state", Fact: "ups.status", Contains: "OB", For: "30s"},
			Shutdown: config.ShutdownConfig{
				Stages: []config.StageConfig{
					{
						Name:            "compute",
						When:            config.ConditionConfig{Condition: "numeric", Fact: "ups.charge", Below: &below},
						Clients:         []string{"tag:compute"},
						PointOfNoReturn: true,
					},
					{
						Name:    "storage",
						When:    config.ConditionConfig{Condition: "true"},
						Clients: []string{"tag:storage", "stell"},
					},
				},
				PostShutdown: &config.PostShutdownConfig{
					Action: "upscmd", Command: "shutdown.return", Delay: 120,
					UPS: "ups", Username: "admin", Password: "hunter2-secret",
				},
			},
		}},
	}
}

func plansStore() *facts.Store {
	store := facts.NewStore()
	store.RegisterSource("ups", time.Second, []facts.FactDeclaration{
		{Name: "status", Type: "set"},
		{Name: "charge", Type: "percent"},
	})
	store.Update("ups.status", []string{"OL"}, time.Now())
	store.Update("ups.charge", 100.0, time.Now())
	return store
}

// TestPlansDescribeWhatWouldHappen: the endpoint returned a plan's name and
// stage count and nothing else, so the Plans page had nothing to show and
// the guide's advice to check the trigger's evaluation state in the UI
// pointed at a feature that did not exist.
func TestPlansDescribeWhatWouldHappen(t *testing.T) {
	s := newServerWith(t, plansConfig(), plansStore())

	var plans []planInfo
	getJSON(t, s, "/api/plans", &plans)

	if len(plans) != 1 {
		t.Fatalf("got %d plans, want 1", len(plans))
	}
	p := plans[0]

	if p.Stages != 2 {
		t.Errorf("stages = %d, want 2 (kept for older consumers)", p.Stages)
	}

	// The trigger, evaluated against the current facts.
	if p.Trigger.Fact != "ups.status" || p.Trigger.Contains != "OB" {
		t.Errorf("trigger = %+v, want the configured condition", p.Trigger)
	}
	if p.Trigger.Result != "false" {
		t.Errorf("trigger result = %s, want false: the UPS is on line", p.Trigger.Result)
	}
	if p.Trigger.Dwell == nil || p.Trigger.Dwell.RequiredSeconds != 30 {
		t.Errorf("trigger dwell = %+v, want the 30s requirement", p.Trigger.Dwell)
	}

	// Stage clients resolved from tags.
	if got := p.Shutdown[0].Clients; len(got) != 1 || got[0] != "vanadium" {
		t.Errorf("compute stage clients = %v, want [vanadium]", got)
	}
	if !p.Shutdown[0].PointOfNoReturn {
		t.Error("the PONR flag was lost")
	}
}

// TestPlansFlagReferencesThatMatchNothing: a stage whose references resolve
// to no client will run and shut nothing down. That includes a misspelled
// bare client name, which ResolveClientRefs passes through unchecked.
func TestPlansFlagReferencesThatMatchNothing(t *testing.T) {
	s := newServerWith(t, plansConfig(), plansStore())

	var plans []planInfo
	getJSON(t, s, "/api/plans", &plans)

	storage := plans[0].Shutdown[1]
	want := map[string]bool{"tag:storage": true, "stell": true}
	if len(storage.Unmatched) != len(want) {
		t.Fatalf("unmatched = %v, want %v", storage.Unmatched, want)
	}
	for _, u := range storage.Unmatched {
		if !want[u] {
			t.Errorf("unexpected unmatched reference %q", u)
		}
	}
	if len(plans[0].Shutdown[0].Unmatched) != 0 {
		t.Errorf("compute stage reported unmatched %v; tag:compute matches vanadium",
			plans[0].Shutdown[0].Unmatched)
	}
}

func TestPlansFlagAMissingWakeGate(t *testing.T) {
	s := newServerWith(t, plansConfig(), plansStore())

	var plans []planInfo
	getJSON(t, s, "/api/plans", &plans)

	if !plans[0].Wake.GateMissing {
		t.Error("a plan with no wake gate was not flagged")
	}
}

// TestPlansNeverExposeNUTCredentials: post_shutdown carries the NUT
// username and password. The plans endpoint is readable with a read-scope
// token, which is exactly what gets handed to monitoring.
func TestPlansNeverExposeNUTCredentials(t *testing.T) {
	s := newServerWith(t, plansConfig(), plansStore())

	rec := do(t, s, http.MethodGet, "/api/plans", "", sessionCookie(t, s))
	body := rec.Body.String()

	for _, secret := range []string{"hunter2-secret", `"password"`, `"username"`} {
		if strings.Contains(body, secret) {
			t.Errorf("plans response contains %s", secret)
		}
	}
	if !strings.Contains(body, "shutdown.return") {
		t.Error("the post-shutdown action itself should still be shown")
	}
}

// TestPlansDoNotDisturbDwellTimers: reading the plans page must not change
// when a plan fires.
func TestPlansDoNotDisturbDwellTimers(t *testing.T) {
	store := plansStore()
	store.Update("ups.status", []string{"OB"}, time.Now())

	cfg := plansConfig()
	s := newServerWith(t, cfg, store)

	for range 5 {
		var plans []planInfo
		getJSON(t, s, "/api/plans", &plans)
	}

	// The policy loop never ran, so nothing may be timing the trigger.
	var plans []planInfo
	getJSON(t, s, "/api/plans", &plans)
	if d := plans[0].Trigger.Dwell; d == nil || d.Tracked {
		t.Errorf("trigger dwell = %+v; viewing the page started a timer", d)
	}
}

// TestClientsReportTheMACAndWhereItCameFrom: a discovered address is only
// trustworthy if an operator can see it and see how fresh it is. Without
// this the only evidence discovery had worked was a log line.
func TestClientsReportTheMACAndWhereItCameFrom(t *testing.T) {
	cfg := &config.Config{
		Clients: []config.ClientConfig{
			{Name: "fixed", Transport: "ssh", Address: "10.0.0.1", MAC: "11:22:33:44:55:66"},
			{Name: "learned", Transport: "ssh", Address: "10.0.0.2"},
			{Name: "unknown", Transport: "ssh", Address: "10.0.0.3"},
		},
	}
	s := newServerWith(t, cfg, facts.NewStore())

	confirmed := time.Now().Add(-90 * time.Second)
	if err := s.db.SaveLearnedMAC(t.Context(), state.LearnedMAC{
		Client: "learned", MAC: "aa:bb:cc:00:00:01", Source: "the truenas API",
		LearnedAt: confirmed.Add(-time.Hour), ConfirmedAt: confirmed,
	}); err != nil {
		t.Fatalf("SaveLearnedMAC: %v", err)
	}
	if err := s.executor.ReloadLearnedMACs(); err != nil {
		t.Fatalf("restoring: %v", err)
	}

	var clients []struct {
		Name           string     `json:"name"`
		MAC            string     `json:"mac"`
		MACSource      string     `json:"mac_source"`
		MACConfirmedAt *time.Time `json:"mac_confirmed_at"`
	}
	getJSON(t, s, "/api/clients", &clients)

	byName := map[string]int{}
	for i, c := range clients {
		byName[c.Name] = i
	}

	if c := clients[byName["fixed"]]; c.MAC != "11:22:33:44:55:66" || c.MACSource != "configured" {
		t.Errorf("configured client: mac=%q source=%q", c.MAC, c.MACSource)
	}

	c := clients[byName["learned"]]
	if c.MAC != "aa:bb:cc:00:00:01" {
		t.Errorf("learned client: mac=%q", c.MAC)
	}
	if c.MACSource != "the truenas API" {
		t.Errorf("learned client: source=%q, want where it came from", c.MACSource)
	}
	if c.MACConfirmedAt == nil {
		t.Error("learned client: no confirmation time, so staleness is invisible")
	}

	if c := clients[byName["unknown"]]; c.MAC != "" || c.MACSource != "" {
		t.Errorf("unknown client reported mac=%q source=%q", c.MAC, c.MACSource)
	}
}
