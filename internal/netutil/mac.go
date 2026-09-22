package netutil

import "net"

// NormaliseMAC parses a hardware address and reports whether it is usable
// as a wake-on-LAN target.
//
// Discovery reads these out of appliance APIs and kernel tables, so what
// comes back is not always an address a magic packet can be sent to. Each
// rejection here is an address that would be accepted, stored, and then
// silently fail to wake anything:
//
//   - Unparseable, or not 48 bits. EUI-64 and InfiniBand addresses appear
//     on interfaces that are not Ethernet.
//   - All zeroes. What a kernel reports for an interface whose lookup has
//     not completed, and what some APIs return for a loopback.
//   - Broadcast. Waking "everything" is not waking the client.
//   - Multicast, meaning the low bit of the first octet is set. No NIC has
//     one as its own address, so a value with that bit is a group address
//     that arrived where a hardware address was expected.
//
// The returned form is lowercase and colon-separated, so an address learned
// from a device compares equal to the same address typed into the config.
func NormaliseMAC(s string) (string, bool) {
	hw, err := net.ParseMAC(s)
	if err != nil || len(hw) != 6 {
		return "", false
	}

	if hw[0]&1 != 0 {
		// Covers broadcast too: ff:ff:ff:ff:ff:ff has the bit set.
		return "", false
	}

	zero := true
	for _, b := range hw {
		if b != 0 {
			zero = false
			break
		}
	}
	if zero {
		return "", false
	}

	return hw.String(), true
}
