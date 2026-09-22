package netutil

import "testing"

func TestNormaliseMACAccepts(t *testing.T) {
	for in, want := range map[string]string{
		"aa:bb:cc:dd:ee:ff": "aa:bb:cc:dd:ee:ff",
		"AA:BB:CC:DD:EE:FF": "aa:bb:cc:dd:ee:ff",
		"aa-bb-cc-dd-ee-ff": "aa:bb:cc:dd:ee:ff",
		"02:00:00:00:00:01": "02:00:00:00:00:01", // locally administered
	} {
		got, ok := NormaliseMAC(in)
		if !ok || got != want {
			t.Errorf("NormaliseMAC(%q) = %q, %v; want %q, true", in, got, ok, want)
		}
	}
}

// TestNormaliseMACRejects covers the values that would be stored and then
// silently fail to wake anything.
func TestNormaliseMACRejects(t *testing.T) {
	for _, in := range []string{
		"",
		"not a mac",
		"aa:bb:cc:dd:ee",          // too short
		"00:00:00:00:00:00",       // incomplete neighbour entry
		"ff:ff:ff:ff:ff:ff",       // broadcast
		"01:00:5e:00:00:01",       // multicast
		"aa:bb:cc:dd:ee:ff:00:11", // EUI-64, not Ethernet
		"00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00", // InfiniBand
	} {
		if got, ok := NormaliseMAC(in); ok {
			t.Errorf("NormaliseMAC(%q) = %q, true; want rejected", in, got)
		}
	}
}
