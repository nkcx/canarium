package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/state"
)

// discoveringStub is a transport that can report a hardware address, as the
// truenas and opnsense transports do.
type discoveringStub struct {
	*stubTransport
	mac string
	err error
}

func (d *discoveringStub) DiscoverMAC(context.Context, *engine.Client) (string, error) {
	return d.mac, d.err
}

// wolClient is a client woken by a magic packet, which is the only case
// that needs a hardware address.
func wolClient(mac string) config.ClientConfig {
	return config.ClientConfig{
		Name: "nas", Transport: "stub", Address: "10.0.10.20",
		Wake: &config.WakeConfig{Transport: "wol", MAC: mac},
	}
}

func runMACCheck(t *testing.T, c config.ClientConfig, transport engine.Transport, learned map[string]state.LearnedMAC) Check {
	t.Helper()

	d := New(&config.Config{Clients: []config.ClientConfig{c}},
		map[string]engine.Transport{"stub": transport}, nil, nil,
		Options{Timeout: time.Second})
	d.SetLearnedMACs(learned)

	var checks []Check
	d.checkWakeMAC(t.Context(), &c, func(ch Check) { checks = append(checks, ch) })

	if len(checks) != 1 {
		t.Fatalf("got %d checks, want exactly one", len(checks))
	}
	return checks[0]
}

func TestConfiguredMACPasses(t *testing.T) {
	got := runMACCheck(t, wolClient("aa:bb:cc:00:00:01"), probeCapable(engine.StateUp, nil), nil)

	if got.Status != StatusOK {
		t.Errorf("status = %s, want OK: %s", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "configuration file") {
		t.Errorf("detail = %q; it should say where the address came from", got.Detail)
	}
}

// TestUnusableConfiguredMACFails: a typo in the config is exactly the kind
// of thing doctor exists to catch on a Tuesday rather than during an
// outage, and it is invisible until a wake silently does nothing.
func TestUnusableConfiguredMACFails(t *testing.T) {
	for _, bad := range []string{"aa:bb:cc", "00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff", "nonsense"} {
		got := runMACCheck(t, wolClient(bad), probeCapable(engine.StateUp, nil), nil)
		if got.Status != StatusFail {
			t.Errorf("%q: status = %s, want FAIL", bad, got.Status)
		}
	}
}

// TestLearnedMACPasses: doctor usually runs while the daemon does, and the
// daemon's knowledge is the best answer available.
func TestLearnedMACPasses(t *testing.T) {
	learned := map[string]state.LearnedMAC{"nas": {
		Client: "nas", MAC: "aa:bb:cc:00:00:01", Source: "the truenas API",
		LearnedAt: time.Now().Add(-72 * time.Hour), ConfirmedAt: time.Now().Add(-90 * time.Minute),
	}}

	got := runMACCheck(t, wolClient(""), probeCapable(engine.StateUp, nil), learned)

	if got.Status != StatusOK {
		t.Errorf("status = %s, want OK: %s", got.Status, got.Detail)
	}
	for _, want := range []string{"aa:bb:cc:00:00:01", "truenas API", "hours ago"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail %q does not mention %q", got.Detail, want)
		}
	}
}

// TestStaleLearnedMACWarns: the daemon re-confirms every few hours while a
// client is up, so an address last confirmed weeks ago means the client has
// not been seen since — worth knowing before relying on it.
func TestStaleLearnedMACWarns(t *testing.T) {
	learned := map[string]state.LearnedMAC{"nas": {
		Client: "nas", MAC: "aa:bb:cc:00:00:01", Source: "the truenas API",
		ConfirmedAt: time.Now().Add(-30 * 24 * time.Hour),
	}}

	got := runMACCheck(t, wolClient(""), probeCapable(engine.StateUp, nil), learned)

	if got.Status != StatusWarn {
		t.Errorf("status = %s, want WARN for an address last confirmed a month ago", got.Status)
	}
	if !strings.Contains(got.Detail, "days ago") {
		t.Errorf("detail = %q; it should say how old the value is", got.Detail)
	}
}

// TestDiscoveryIsAttemptedWhenNothingIsKnown: doctor contacts things. If
// the daemon has never learned an address, doctor asks the device itself.
func TestDiscoveryIsAttemptedWhenNothingIsKnown(t *testing.T) {
	transport := &discoveringStub{
		stubTransport: probeCapable(engine.StateUp, nil), mac: "aa:bb:cc:00:00:02",
	}

	got := runMACCheck(t, wolClient(""), transport, nil)

	if got.Status != StatusOK {
		t.Errorf("status = %s, want OK: %s", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "aa:bb:cc:00:00:02") {
		t.Errorf("detail = %q, want the discovered address", got.Detail)
	}
}

func TestUnusableDiscoveredMACIsNotAccepted(t *testing.T) {
	transport := &discoveringStub{
		stubTransport: probeCapable(engine.StateUp, nil), mac: "00:00:00:00:00:00",
	}

	if got := runMACCheck(t, wolClient(""), transport, nil); got.Status != StatusFail {
		t.Errorf("status = %s, want FAIL: an all-zero address wakes nothing", got.Status)
	}
}

// TestNoMACAnywhereFails is the check's reason for existing: wake-on-LAN
// cannot run, and the operator finds out now rather than during a recovery.
func TestNoMACAnywhereFails(t *testing.T) {
	got := runMACCheck(t, wolClient(""), probeCapable(engine.StateUp, nil), nil)

	if got.Status != StatusFail {
		t.Fatalf("status = %s, want FAIL: %s", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "cannot report one") {
		t.Errorf("detail = %q; it should say the transport cannot discover one", got.Detail)
	}
	if !strings.Contains(got.Detail, "set mac:") {
		t.Errorf("detail = %q; a failure should say what to do", got.Detail)
	}
}

func TestDiscoveryFailureFallsThroughToFail(t *testing.T) {
	transport := &discoveringStub{
		stubTransport: probeCapable(engine.StateUp, nil), err: errors.New("API key rejected"),
	}

	if got := runMACCheck(t, wolClient(""), transport, nil); got.Status != StatusFail {
		t.Errorf("status = %s, want FAIL", got.Status)
	}
}

// TestClientsThatAreNotWokenByWOLAreNotChecked keeps the report free of
// lines about an address that would never be used.
func TestClientsThatAreNotWokenByWOLAreNotChecked(t *testing.T) {
	for _, c := range []config.ClientConfig{
		{Name: "fw", Transport: "stub", Address: "10.0.0.1"}, // no wake at all
		{Name: "vm", Transport: "stub", Address: "10.0.0.2",
			Wake: &config.WakeConfig{Transport: "ipmi"}},
	} {
		d := New(&config.Config{Clients: []config.ClientConfig{c}},
			map[string]engine.Transport{"stub": probeCapable(engine.StateUp, nil)},
			nil, nil, Options{Timeout: time.Second})

		var checks []Check
		d.checkWakeMAC(t.Context(), &c, func(ch Check) { checks = append(checks, ch) })

		if len(checks) != 0 {
			t.Errorf("%s: got %d checks, want none", c.Name, len(checks))
		}
	}
}

func TestWakeMACAppearsInAFullRun(t *testing.T) {
	cfg := &config.Config{Clients: []config.ClientConfig{wolClient("")}}
	d := New(cfg, map[string]engine.Transport{"stub": probeCapable(engine.StateUp, nil)},
		nil, nil, Options{Timeout: time.Second})

	report := d.Run(t.Context())

	found := false
	for _, c := range report.Checks {
		if c.Name == "wake MAC" {
			found = true
		}
	}
	if !found {
		t.Error("a full doctor run reported nothing about the wake MAC")
	}
	if !report.HasFailures() {
		t.Error("a client that cannot be woken did not fail preflight")
	}
}
