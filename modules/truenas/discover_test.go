package truenas

import (
	"encoding/json"
	"testing"
)

// truenasInterfaces is the shape interface.query returns: a list of
// interfaces, each with its hardware address under state.link_address and
// its addresses under state.aliases.
const truenasInterfaces = `[
  {"name":"lo","type":"PHYSICAL","state":{"link_address":"00:00:00:00:00:00","link_state":"LINK_STATE_UP","aliases":[{"type":"INET","address":"127.0.0.1"}]}},
  {"name":"eno1","type":"PHYSICAL","state":{"link_address":"aa:bb:cc:00:00:01","link_state":"LINK_STATE_UP","aliases":[{"type":"INET","address":"10.0.10.20"}]}},
  {"name":"eno2","type":"PHYSICAL","state":{"link_address":"aa:bb:cc:00:00:02","link_state":"LINK_STATE_UP","aliases":[{"type":"INET","address":"10.0.99.20"}]}}
]`

func parse(t *testing.T, raw string) []nasInterface {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	ifaces, err := decodeInterfaces(v)
	if err != nil {
		t.Fatalf("decodeInterfaces: %v", err)
	}
	return ifaces
}

// TestPickMatchesTheInterfaceHoldingTheAddress: a NAS has several
// interfaces and a magic packet sent to the wrong one is dropped without a
// word, so the choice has to be the interface the client is reached on.
func TestPickMatchesTheInterfaceHoldingTheAddress(t *testing.T) {
	ifaces := parse(t, truenasInterfaces)

	if got := pickInterfaceMAC(ifaces, "10.0.10.20"); got != "aa:bb:cc:00:00:01" {
		t.Errorf("MAC = %q, want eno1's", got)
	}
	if got := pickInterfaceMAC(ifaces, "10.0.99.20"); got != "aa:bb:cc:00:00:02" {
		t.Errorf("MAC = %q, want eno2's", got)
	}
}

func TestPickIgnoresLoopbackAndZeroMACs(t *testing.T) {
	if got := pickInterfaceMAC(parse(t, truenasInterfaces), "127.0.0.1"); got != "" {
		t.Errorf("loopback returned %q; its hardware address is all zeroes", got)
	}
}

// TestPickRefusesToGuessBetweenInterfaces: waking the wrong NIC looks
// exactly like waking nothing, so an ambiguous answer is no answer.
func TestPickRefusesToGuessBetweenInterfaces(t *testing.T) {
	if got := pickInterfaceMAC(parse(t, truenasInterfaces), "10.0.55.1"); got != "" {
		t.Errorf("unmatched address returned %q from two candidates", got)
	}
}

func TestPickUsesTheOnlyPhysicalInterface(t *testing.T) {
	const single = `[
	  {"name":"lo","type":"PHYSICAL","state":{"link_address":"00:00:00:00:00:00","link_state":"LINK_STATE_UP","aliases":[]}},
	  {"name":"eno1","type":"PHYSICAL","state":{"link_address":"aa:bb:cc:00:00:01","link_state":"LINK_STATE_UP","aliases":[{"type":"INET","address":"10.0.10.20"}]}}
	]`
	// The client is configured by a name that resolves elsewhere, so
	// nothing matches on address -- but there is only one real interface.
	if got := pickInterfaceMAC(parse(t, single), "nas.example.com"); got != "aa:bb:cc:00:00:01" {
		t.Errorf("MAC = %q, want the single physical interface's", got)
	}
}

func TestPickSkipsAnInterfaceWithNoLink(t *testing.T) {
	const downed = `[
	  {"name":"eno1","type":"PHYSICAL","state":{"link_address":"aa:bb:cc:00:00:01","link_state":"LINK_STATE_DOWN","aliases":[]}},
	  {"name":"eno2","type":"PHYSICAL","state":{"link_address":"aa:bb:cc:00:00:02","link_state":"LINK_STATE_UP","aliases":[]}}
	]`
	if got := pickInterfaceMAC(parse(t, downed), ""); got != "aa:bb:cc:00:00:02" {
		t.Errorf("MAC = %q, want the interface that has a link", got)
	}
}

// TestPickReadsAliasesFromEitherPlace: aliases have moved between the
// interface and its state across TrueNAS versions.
func TestPickReadsTopLevelAliases(t *testing.T) {
	const older = `[
	  {"name":"eno1","type":"PHYSICAL","aliases":[{"type":"INET","address":"10.0.10.20"}],
	   "state":{"link_address":"aa:bb:cc:00:00:01","link_state":"LINK_STATE_UP"}},
	  {"name":"eno2","type":"PHYSICAL","aliases":[{"type":"INET","address":"10.0.99.20"}],
	   "state":{"link_address":"aa:bb:cc:00:00:02","link_state":"LINK_STATE_UP"}}
	]`
	if got := pickInterfaceMAC(parse(t, older), "10.0.10.20"); got != "aa:bb:cc:00:00:01" {
		t.Errorf("MAC = %q, want eno1's from the top-level aliases", got)
	}
}

func TestDecodeInterfacesSurvivesAnUnexpectedShape(t *testing.T) {
	if _, err := decodeInterfaces(map[string]any{"unexpected": true}); err == nil {
		t.Error("a non-list result decoded without error")
	}
	if ifaces, err := decodeInterfaces([]any{}); err != nil || len(ifaces) != 0 {
		t.Errorf("empty list: %v, %v", ifaces, err)
	}
}

func TestNormaliseMAC(t *testing.T) {
	if got := normaliseMAC("AA:BB:CC:00:00:01"); got != "aa:bb:cc:00:00:01" {
		t.Errorf("normaliseMAC = %q", got)
	}
}
