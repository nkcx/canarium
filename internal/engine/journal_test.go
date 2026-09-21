package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
)

// journalConfig runs one stage against one client with no entry condition,
// so the sequence proceeds straight through to completion.
func journalConfig() *config.Config {
	plan := testPlan("outage", testStage("compute", trueCondition(), "nas"))
	return &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{testClient("nas")},
		Plans:    []config.PlanConfig{plan},
	}
}

// TestCompletedSequenceWritesAnAuditJournal is SPEC §8.6. The journal is
// the artefact an operator archives or hands to someone investigating an
// outage; the database it derives from is pruned on a retention schedule
// and is not portable.
func TestCompletedSequenceWritesAnAuditJournal(t *testing.T) {
	h := newHarness(t, journalConfig())
	h.exec.SetMode(ModeArmed)

	h.runSequence("outage", 10*time.Second)

	dir := h.db.JournalDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("the journal directory was never created: %v", err)
	}

	var journals []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			journals = append(journals, e.Name())
		}
	}
	if len(journals) != 1 {
		t.Fatalf("got %d journal files, want exactly one for the sequence", len(journals))
	}

	data, err := os.ReadFile(filepath.Join(dir, journals[0]))
	if err != nil {
		t.Fatalf("reading journal: %v", err)
	}

	var kinds []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("journal line is not valid JSON (%q): %v", line, err)
		}
		if rec.Timestamp == "" {
			t.Errorf("journal record %q carries no timestamp", rec.Type)
		}
		kinds = append(kinds, rec.Type)
	}

	if len(kinds) == 0 || kinds[0] != "sequence" {
		t.Fatalf("journal record types = %v, want the sequence header first", kinds)
	}
	if !slices.Contains(kinds, "stage") {
		t.Errorf("journal record types = %v, want at least one stage record", kinds)
	}
	if !slices.Contains(kinds, "intent") {
		t.Errorf("journal record types = %v, want the dispatched commands", kinds)
	}
}
