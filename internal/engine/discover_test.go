package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/state"
)

// discoveringTransport is a fake transport that can report a MAC, as the
// truenas and opnsense transports do.
type discoveringTransport struct {
	*fakeTransport
	mac   string
	err   error
	calls int
}

func (d *discoveringTransport) DiscoverMAC(_ context.Context, _ *Client) (string, error) {
	d.calls++
	return d.mac, d.err
}

func discoveryConfig(mac string) *config.Config {
	c := testClient("nas")
	c.Address = "10.0.10.20"
	c.MAC = mac
	return &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{c},
		Plans:    []config.PlanConfig{testPlan("outage", testStage("s", trueCondition(), "nas"))},
	}
}

func withDiscovery(t *testing.T, cfg *config.Config, d *discoveringTransport) *harness {
	t.Helper()
	h := newHarness(t, cfg)
	d.fakeTransport = h.transport
	h.exec.RegisterTransport("fake", d)
	return h
}

// TestMACIsDiscoveredWhenTheConfigOmitsIt is the point of the feature: a
// MAC is the one thing about a host that cannot be looked up once the host
// is off, and wake-on-LAN needs it. A device that manages its own network
// knows the answer, so it is asked while it is still up.
func TestMACIsDiscoveredWhenTheConfigOmitsIt(t *testing.T) {
	d := &discoveringTransport{mac: "aa:bb:cc:00:00:01"}
	h := withDiscovery(t, discoveryConfig(""), d)

	resolved := h.exec.resolveClientAddresses(t.Context())

	if got := resolved["nas"].MAC; got != "aa:bb:cc:00:00:01" {
		t.Errorf("MAC = %q, want the discovered one", got)
	}
	if d.calls != 1 {
		t.Errorf("DiscoverMAC called %d times, want 1", d.calls)
	}
}

// TestConfiguredMACWinsOverDiscovery: a hand-configured MAC is a
// deliberate statement and must not be second-guessed by an appliance --
// nor should discovery cost an API call when the answer is already known.
func TestConfiguredMACWinsOverDiscovery(t *testing.T) {
	d := &discoveringTransport{mac: "aa:bb:cc:00:00:99"}
	h := withDiscovery(t, discoveryConfig("11:22:33:44:55:66"), d)

	resolved := h.exec.resolveClientAddresses(t.Context())

	if got := resolved["nas"].MAC; got != "11:22:33:44:55:66" {
		t.Errorf("MAC = %q, want the configured one", got)
	}
	if d.calls != 0 {
		t.Error("the device was asked for a MAC that was already configured")
	}
}

// TestDiscoveryFailureIsNotFatal: this runs at sequence start, with a
// battery draining. An appliance that will not answer must cost nothing
// more than an unknown MAC.
func TestDiscoveryFailureIsNotFatal(t *testing.T) {
	d := &discoveringTransport{err: errors.New("API key rejected")}
	h := withDiscovery(t, discoveryConfig(""), d)

	resolved := h.exec.resolveClientAddresses(t.Context())

	entry, ok := resolved["nas"]
	if !ok {
		t.Fatal("the client was dropped from the resolved set")
	}
	if entry.MAC != "" {
		t.Errorf("MAC = %q after a failed discovery", entry.MAC)
	}
	if entry.IP != "10.0.10.20" {
		t.Errorf("IP = %q; address pinning must still have happened", entry.IP)
	}
}

// TestDiscoveryIsSkippedForTransportsThatCannotDoIt: most transports have
// no way to ask, and must not be broken by the attempt.
func TestDiscoveryIsSkippedForTransportsThatCannotDoIt(t *testing.T) {
	h := newHarness(t, discoveryConfig(""))

	resolved := h.exec.resolveClientAddresses(t.Context())

	if resolved["nas"].MAC != "" {
		t.Errorf("MAC = %q from a transport that cannot discover one", resolved["nas"].MAC)
	}
}

// TestWakeSnapshotCarriesTheDiscoveredMAC: discovery is worthless unless
// the value survives into the snapshot the wake plan reads, which is taken
// before anything is shut down precisely because the answer cannot be
// obtained afterwards.
func TestWakeSnapshotCarriesTheDiscoveredMAC(t *testing.T) {
	d := &discoveringTransport{mac: "aa:bb:cc:00:00:01"}
	h := withDiscovery(t, discoveryConfig(""), d)
	h.exec.SetMode(ModeArmed)

	h.runSequence("outage", 10*time.Second)

	seq, err := h.db.LastSequence(t.Context())
	if err != nil {
		t.Fatalf("LastSequence: %v", err)
	}
	if got := seq.ResolvedAddrs["nas"].MAC; got != "aa:bb:cc:00:00:01" {
		t.Errorf("recorded MAC = %q, want the discovered one", got)
	}
}

// TestWakeUsesTheDiscoveredMAC closes the loop. The snapshot recorded a
// discovered MAC, but buildClientFor overrode only the address, so the WOL
// transport still saw the empty one from the config and failed with "no MAC
// address configured" for a client whose MAC had been discovered minutes
// earlier.
func TestWakeUsesTheDiscoveredMAC(t *testing.T) {
	cfg := discoveryConfig("")
	h := newHarness(t, cfg)

	as := h.exec.newSequenceFor(&cfg.Plans[0])
	as.SetResolvedAddrs(map[string]state.ResolvedAddr{
		"nas": {IP: "10.0.10.20", MAC: "aa:bb:cc:00:00:01"},
	})

	client := h.exec.buildClientFor(as, &cfg.Clients[0])

	if client.MAC != "aa:bb:cc:00:00:01" {
		t.Errorf("MAC handed to the transport = %q, want the discovered one", client.MAC)
	}
	if client.Address != "10.0.10.20" {
		t.Errorf("Address = %q, want the pinned one", client.Address)
	}
}

// TestConfiguredMACSurvivesAnEmptySnapshot: a client resolved before
// discovery existed, or one whose snapshot carries no MAC, must still use
// the configured value.
func TestConfiguredMACSurvivesAnEmptySnapshot(t *testing.T) {
	cfg := discoveryConfig("11:22:33:44:55:66")
	h := newHarness(t, cfg)

	as := h.exec.newSequenceFor(&cfg.Plans[0])
	as.SetResolvedAddrs(map[string]state.ResolvedAddr{"nas": {IP: "10.0.10.20"}})

	if got := h.exec.buildClientFor(as, &cfg.Clients[0]).MAC; got != "11:22:33:44:55:66" {
		t.Errorf("MAC = %q, want the configured one", got)
	}
}
