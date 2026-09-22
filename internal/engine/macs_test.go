package engine

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/conditions"
	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
	"github.com/nkcx/canarium/internal/state"
)

var errTransportRefused = errors.New("API key rejected")

// newExecutorOver builds a second executor over an existing database, as a
// restart would: same stored state, nothing in memory.
func newExecutorOver(t *testing.T, db *state.DB, cfg *config.Config) *Executor {
	t.Helper()

	store := facts.NewStore()
	exec := NewExecutor(cfg, store, conditions.NewEvaluator(store), db,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	exec.SetTimings(testTimings())
	t.Cleanup(exec.Stop)
	return exec
}

// learnHarness wires a discovering transport to a client that is up and has
// no configured MAC, which is the case discovery exists for.
func learnHarness(t *testing.T, d *discoveringTransport) *harness {
	t.Helper()
	h := withDiscovery(t, discoveryConfig(""), d)
	h.exec.setClientState("nas", StateUp, nil)
	return h
}

func TestMACIsLearnedFromTheProbeLoop(t *testing.T) {
	// The point of doing this continuously: by the time wake-on-LAN needs
	// the address, the host is off and cannot be asked.
	d := &discoveringTransport{mac: "aa:bb:cc:00:00:01"}
	h := learnHarness(t, d)

	h.exec.learnMACs()

	learned, ok := h.exec.LearnedMAC("nas")
	if !ok || learned.MAC != "aa:bb:cc:00:00:01" {
		t.Fatalf("learned = %+v, want the discovered address", learned)
	}
	if learned.Source == "" {
		t.Error("no source recorded; an operator cannot tell where it came from")
	}
}

func TestLearnedMACIsUsedForWake(t *testing.T) {
	d := &discoveringTransport{mac: "aa:bb:cc:00:00:01"}
	h := learnHarness(t, d)
	h.exec.learnMACs()

	// No sequence: a client woken outside one still needs the address.
	if got := h.exec.macFor(nil, &h.cfg.Clients[0]); got != "aa:bb:cc:00:00:01" {
		t.Errorf("MAC for wake = %q, want the learned one", got)
	}
	if got := h.exec.buildClientFor(nil, &h.cfg.Clients[0]).MAC; got != "aa:bb:cc:00:00:01" {
		t.Errorf("MAC handed to the transport = %q", got)
	}
}

// TestLookupsAreRateLimited: discovery is an authenticated API call, and
// the probe loop runs every thirty seconds. Asking a NAS that often is
// abuse, not diligence.
func TestLookupsAreRateLimited(t *testing.T) {
	d := &discoveringTransport{mac: "aa:bb:cc:00:00:01"}
	h := learnHarness(t, d)

	for range 10 {
		h.exec.learnMACs()
	}

	if d.calls != 1 {
		t.Errorf("asked the device %d times across ten probe ticks, want 1", d.calls)
	}
}

func TestAnUnknownMACIsRetriedSoonerThanAKnownOneIsRefreshed(t *testing.T) {
	d := &discoveringTransport{err: errTransportRefused}
	h := learnHarness(t, d)

	h.exec.learnMACs()
	if d.calls != 1 {
		t.Fatalf("first attempt: %d calls", d.calls)
	}

	// Nothing learned, so the retry window is the short one.
	h.exec.mu.Lock()
	h.exec.macAttempts["nas"] = time.Now().Add(-macRetryInterval - time.Second)
	h.exec.mu.Unlock()

	h.exec.learnMACs()
	if d.calls != 2 {
		t.Errorf("an unknown MAC was not retried after the retry interval (%d calls)", d.calls)
	}

	// Once known, the same elapsed time is not enough to ask again.
	d.err = nil
	d.mac = "aa:bb:cc:00:00:01"
	h.exec.mu.Lock()
	h.exec.macAttempts["nas"] = time.Now().Add(-macRetryInterval - time.Second)
	h.exec.mu.Unlock()
	h.exec.learnMACs()
	before := d.calls

	h.exec.mu.Lock()
	h.exec.macAttempts["nas"] = time.Now().Add(-macRetryInterval - time.Second)
	h.exec.mu.Unlock()
	h.exec.learnMACs()

	if d.calls != before {
		t.Error("a known MAC was re-confirmed on the short retry interval")
	}
}

// TestBadDataIsIgnoredAndTheLastGoodValueKept is the rule that makes this
// safe to depend on: an address that cannot wake anything must never
// displace one that can.
func TestBadDataIsIgnoredAndTheLastGoodValueKept(t *testing.T) {
	d := &discoveringTransport{mac: "aa:bb:cc:00:00:01"}
	h := learnHarness(t, d)
	h.exec.learnMACs()

	for _, bad := range []string{
		"00:00:00:00:00:00", // an incomplete neighbour entry
		"ff:ff:ff:ff:ff:ff", // broadcast
		"01:00:5e:00:00:01", // multicast
		"not a mac",
		"",
	} {
		d.mac = bad
		h.exec.mu.Lock()
		delete(h.exec.macAttempts, "nas")
		h.exec.mu.Unlock()
		h.exec.learnMACs()

		learned, _ := h.exec.LearnedMAC("nas")
		if learned.MAC != "aa:bb:cc:00:00:01" {
			t.Fatalf("after %q the stored address became %q; the last good value must stand",
				bad, learned.MAC)
		}
	}
}

func TestAFailedLookupKeepsTheLastGoodValue(t *testing.T) {
	d := &discoveringTransport{mac: "aa:bb:cc:00:00:01"}
	h := learnHarness(t, d)
	h.exec.learnMACs()

	d.err = errTransportRefused
	h.exec.mu.Lock()
	delete(h.exec.macAttempts, "nas")
	h.exec.mu.Unlock()
	h.exec.learnMACs()

	if learned, _ := h.exec.LearnedMAC("nas"); learned.MAC != "aa:bb:cc:00:00:01" {
		t.Errorf("an unreachable device erased a known address: %q", learned.MAC)
	}
}

// TestAChangedMACIsAccepted: a replaced NIC is exactly the case continuous
// discovery exists to keep up with.
func TestAChangedMACIsAccepted(t *testing.T) {
	d := &discoveringTransport{mac: "aa:bb:cc:00:00:01"}
	h := learnHarness(t, d)
	h.exec.learnMACs()
	first, _ := h.exec.LearnedMAC("nas")

	d.mac = "aa:bb:cc:00:00:02"
	h.exec.mu.Lock()
	delete(h.exec.macAttempts, "nas")
	h.exec.mu.Unlock()
	h.exec.learnMACs()

	learned, _ := h.exec.LearnedMAC("nas")
	if learned.MAC != "aa:bb:cc:00:00:02" {
		t.Errorf("MAC = %q, want the new one", learned.MAC)
	}
	if !learned.LearnedAt.After(first.LearnedAt) {
		t.Error("a different address should restart learned_at; it is different hardware")
	}
}

func TestReconfirmingKeepsTheOriginalLearnedAt(t *testing.T) {
	d := &discoveringTransport{mac: "aa:bb:cc:00:00:01"}
	h := learnHarness(t, d)
	h.exec.learnMACs()
	first, _ := h.exec.LearnedMAC("nas")

	h.exec.mu.Lock()
	delete(h.exec.macAttempts, "nas")
	h.exec.mu.Unlock()
	h.exec.learnMACs()

	learned, _ := h.exec.LearnedMAC("nas")
	if !learned.LearnedAt.Equal(first.LearnedAt) {
		t.Error("re-confirming the same address restarted learned_at")
	}
	if !learned.ConfirmedAt.After(first.ConfirmedAt) {
		t.Error("re-confirming did not move confirmed_at")
	}
}

// TestLearnedMACSurvivesARestart is the case that decides whether this is
// worth anything: a restart is most likely right after the power event
// Canarium exists to handle, with the whole fleet down and nothing able to
// report its own address.
func TestLearnedMACSurvivesARestart(t *testing.T) {
	d := &discoveringTransport{mac: "aa:bb:cc:00:00:01"}
	h := learnHarness(t, d)
	h.exec.learnMACs()

	// A fresh executor over the same database, with the client down.
	revived := newExecutorOver(t, h.db, h.cfg)
	if err := revived.restoreLearnedMACs(); err != nil {
		t.Fatalf("restoreLearnedMACs: %v", err)
	}

	learned, ok := revived.LearnedMAC("nas")
	if !ok || learned.MAC != "aa:bb:cc:00:00:01" {
		t.Fatalf("learned = %+v after restart, want the stored address", learned)
	}
	if got := revived.macFor(nil, &h.cfg.Clients[0]); got != "aa:bb:cc:00:00:01" {
		t.Errorf("MAC available for wake after restart = %q", got)
	}
}

func TestConfiguredMACIsNeverLookedUp(t *testing.T) {
	d := &discoveringTransport{mac: "aa:bb:cc:00:00:99"}
	h := withDiscovery(t, discoveryConfig("11:22:33:44:55:66"), d)
	h.exec.setClientState("nas", StateUp, nil)

	h.exec.learnMACs()

	if d.calls != 0 {
		t.Error("a device was queried for a MAC the config already states")
	}
	if got := h.exec.macFor(nil, &h.cfg.Clients[0]); got != "11:22:33:44:55:66" {
		t.Errorf("MAC = %q, want the configured one", got)
	}
}

func TestClientsThatAreNotUpAreNotAsked(t *testing.T) {
	d := &discoveringTransport{mac: "aa:bb:cc:00:00:01"}
	h := withDiscovery(t, discoveryConfig(""), d)
	h.exec.setClientState("nas", StateDown, nil)

	h.exec.learnMACs()

	if d.calls != 0 {
		t.Error("a client that is down was asked for its MAC")
	}
}

// TestNoLookupsDuringASequence: the fleet is being shut down and its
// addresses were pinned at the start. An appliance handling a shutdown does
// not also need a status query.
func TestNoLookupsDuringASequence(t *testing.T) {
	d := &discoveringTransport{mac: "aa:bb:cc:00:00:01"}
	h := learnHarness(t, d)

	as := h.exec.newSequenceFor(&h.cfg.Plans[0])
	if !h.exec.setActiveSequence(as) {
		t.Fatal("could not claim the sequence slot")
	}
	defer h.exec.clearActiveSequence()

	h.exec.learnMACs()

	if d.calls != 0 {
		t.Error("a device was queried while a sequence was running")
	}
}

func TestStoredGarbageIsDiscardedOnRestore(t *testing.T) {
	h := newHarness(t, discoveryConfig(""))

	if err := h.db.SaveLearnedMAC(t.Context(), state.LearnedMAC{
		Client: "nas", MAC: "00:00:00:00:00:00", Source: "somewhere",
		LearnedAt: time.Now(), ConfirmedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveLearnedMAC: %v", err)
	}

	revived := newExecutorOver(t, h.db, h.cfg)
	if err := revived.restoreLearnedMACs(); err != nil {
		t.Fatalf("restoreLearnedMACs: %v", err)
	}

	if _, ok := revived.LearnedMAC("nas"); ok {
		t.Error("an unusable stored address was loaded and would be sent a magic packet")
	}
}
