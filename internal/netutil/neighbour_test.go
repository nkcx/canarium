package netutil

import (
	"strings"
	"testing"
)

const sampleARP = `IP address       HW type     Flags       HW address            Mask     Device
10.0.10.20       0x1         0x2         aa:bb:cc:dd:ee:ff     *        eth0
10.0.10.21       0x1         0x0         00:00:00:00:00:00     *        eth0
10.0.10.22       0x1         0x2         00:00:00:00:00:00     *        eth0
192.168.1.1      0x1         0x2         AA:BB:CC:00:11:22     *        eth1
`

func TestParseNeighbourMAC(t *testing.T) {
	if got := parseNeighbourMAC(strings.NewReader(sampleARP), "10.0.10.20"); got != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("MAC = %q, want aa:bb:cc:dd:ee:ff", got)
	}

	// Returned normalised, so it does not matter that the kernel and the
	// config file disagree about case.
	if got := parseNeighbourMAC(strings.NewReader(sampleARP), "192.168.1.1"); got != "aa:bb:cc:00:11:22" {
		t.Errorf("MAC = %q, want it lowercased", got)
	}
}

func TestParseNeighbourMACSkipsUnusableEntries(t *testing.T) {
	// An incomplete entry is a lookup in progress, and its hardware
	// address is all zeroes. Sending a magic packet to that address wakes
	// nothing and reports success.
	if got := parseNeighbourMAC(strings.NewReader(sampleARP), "10.0.10.21"); got != "" {
		t.Errorf("incomplete entry returned %q, want empty", got)
	}
	if got := parseNeighbourMAC(strings.NewReader(sampleARP), "10.0.10.22"); got != "" {
		t.Errorf("zero MAC returned %q, want empty", got)
	}
}

func TestParseNeighbourMACMisses(t *testing.T) {
	for _, ip := range []string{"10.0.10.99", "", "not-an-ip", "example.com"} {
		if got := parseNeighbourMAC(strings.NewReader(sampleARP), ip); got != "" {
			t.Errorf("lookup of %q returned %q, want empty", ip, got)
		}
	}
}

func TestParseNeighbourMACSurvivesGarbage(t *testing.T) {
	junk := "not a table\n\n10.0.10.20\nx y z\n"
	if got := parseNeighbourMAC(strings.NewReader(junk), "10.0.10.20"); got != "" {
		t.Errorf("garbage returned %q", got)
	}
}

// TestNeighbourMACOnThisHost exercises the real file. It asserts only that
// the call is safe: the table's contents depend on the machine, and in a
// container on a bridge network it is usually empty.
func TestNeighbourMACOnThisHost(t *testing.T) {
	if got := NeighbourMAC("203.0.113.1"); got != "" {
		t.Errorf("a TEST-NET-3 address resolved to %q", got)
	}
}
