package opnsense

import (
	"encoding/json"
	"testing"
)

// opnsenseInterfaces is the shape
// /api/diagnostics/interface/getInterfaceConfig returns: an object keyed by
// device, each with macaddr and its addresses under ipv4.
const opnsenseInterfaces = `{
  "lo0":  {"device":"lo0","macaddr":"","is_physical":false,"status":"up","ipv4":[{"ipaddr":"127.0.0.1"}]},
  "igb0": {"device":"igb0","macaddr":"aa:bb:cc:11:00:01","is_physical":true,"status":"up","ipv4":[{"ipaddr":"10.0.0.1"}]},
  "igb1": {"device":"igb1","macaddr":"aa:bb:cc:11:00:02","is_physical":true,"status":"up","ipv4":[{"ipaddr":"10.0.10.1"}]},
  "igb2": {"device":"igb2","macaddr":"aa:bb:cc:11:00:03","is_physical":true,"status":"down","ipv4":[]}
}`

func parse(t *testing.T, raw string) map[string]interfaceConfig {
	t.Helper()
	var out map[string]interfaceConfig
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return out
}

// TestPickMatchesTheInterfaceHoldingTheAddress: a firewall has an interface
// per network, and a magic packet sent to the wrong one is dropped
// silently.
func TestPickMatchesTheInterfaceHoldingTheAddress(t *testing.T) {
	configs := parse(t, opnsenseInterfaces)

	if got := pickInterfaceMAC(configs, "10.0.0.1"); got != "aa:bb:cc:11:00:01" {
		t.Errorf("MAC = %q, want igb0's", got)
	}
	if got := pickInterfaceMAC(configs, "10.0.10.1"); got != "aa:bb:cc:11:00:02" {
		t.Errorf("MAC = %q, want igb1's", got)
	}
}

func TestPickRefusesToGuessBetweenInterfaces(t *testing.T) {
	// Two physical interfaces are up and neither holds the address, so
	// there is no answer to give. Waking the wrong NIC is indistinguishable
	// from waking nothing.
	if got := pickInterfaceMAC(parse(t, opnsenseInterfaces), "fw.example.com"); got != "" {
		t.Errorf("ambiguous lookup returned %q", got)
	}
}

func TestPickIgnoresInterfacesWithoutAMAC(t *testing.T) {
	if got := pickInterfaceMAC(parse(t, opnsenseInterfaces), "127.0.0.1"); got != "" {
		t.Errorf("loopback returned %q; it reports no hardware address", got)
	}
}

func TestPickUsesTheOnlyPhysicalInterfaceThatIsUp(t *testing.T) {
	const single = `{
	  "lo0":  {"device":"lo0","macaddr":"","is_physical":false,"status":"up","ipv4":[]},
	  "igb0": {"device":"igb0","macaddr":"aa:bb:cc:11:00:01","is_physical":true,"status":"up","ipv4":[{"ipaddr":"10.0.0.1"}]},
	  "igb1": {"device":"igb1","macaddr":"aa:bb:cc:11:00:09","is_physical":true,"status":"down","ipv4":[]}
	}`
	if got := pickInterfaceMAC(parse(t, single), "fw.example.com"); got != "aa:bb:cc:11:00:01" {
		t.Errorf("MAC = %q, want the one interface that is up", got)
	}
}

func TestPickHandlesAnEmptyResponse(t *testing.T) {
	if got := pickInterfaceMAC(map[string]interfaceConfig{}, "10.0.0.1"); got != "" {
		t.Errorf("empty response returned %q", got)
	}
}

func TestValidMACRejectsZeroes(t *testing.T) {
	if validMAC("00:00:00:00:00:00") {
		t.Error("an all-zero MAC was accepted; a magic packet sent there wakes nothing")
	}
	if validMAC("not a mac") {
		t.Error("garbage was accepted as a MAC")
	}
}
