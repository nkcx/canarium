package netutil

import (
	"bufio"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
)

// procNetARP is the kernel's IPv4 neighbour table.
const procNetARP = "/proc/net/arp"

// NeighbourMAC returns the hardware address the kernel has cached for an IP,
// or "" if it has none.
//
// The guide has told people since the first release that a client's `mac:`
// is "optional if Canarium can discover it via ARP", and the spec says MACs
// are discovered at config load. Neither was true: the MAC came from the
// configuration file or nowhere, so a client with `wake: {transport: wol}`
// and no `mac:` had no address to send the magic packet to.
//
// Two limits are worth stating plainly, because they decide whether this is
// useful to you at all:
//
//   - The neighbour table only holds hosts on a directly attached subnet.
//     A client on another VLAN is reached through a router, so the kernel
//     never learns its hardware address. Ask the device itself instead --
//     see engine.MACDiscoverer.
//
//   - Inside a container on a bridge network, the table belongs to the
//     container's namespace, and everything beyond the bridge is reached
//     through the Docker gateway. The shipped compose file is in exactly
//     that position, so this finds nothing there. Host or macvlan
//     networking makes it work.
//
// A MAC found this way is a cache entry, not a statement from the device,
// so it is the last resort rather than the first.
func NeighbourMAC(ip string) string {
	f, err := os.Open(procNetARP)
	if err != nil {
		return ""
	}
	defer f.Close()

	return parseNeighbourMAC(f, ip)
}

// parseNeighbourMAC scans /proc/net/arp for an IP's hardware address.
//
//	IP address       HW type     Flags       HW address            Mask     Device
//	10.0.10.20       0x1         0x2         aa:bb:cc:dd:ee:ff     *        eth0
func parseNeighbourMAC(r io.Reader, ip string) string {
	want := net.ParseIP(ip)
	if want == nil {
		return ""
	}

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}

		got := net.ParseIP(fields[0])
		if got == nil || !got.Equal(want) {
			continue
		}

		// Flags is a bitmask; 0x2 is ATF_COM, "the entry is complete". An
		// incomplete entry is a lookup in progress and its hardware
		// address is all zeroes.
		flags, err := strconv.ParseUint(strings.TrimPrefix(fields[2], "0x"), 16, 32)
		if err != nil || flags&0x2 == 0 {
			continue
		}

		mac, err := net.ParseMAC(fields[3])
		if err != nil || isZeroMAC(mac) {
			continue
		}
		return mac.String()
	}

	return ""
}

func isZeroMAC(mac net.HardwareAddr) bool {
	for _, b := range mac {
		if b != 0 {
			return false
		}
	}
	return true
}
