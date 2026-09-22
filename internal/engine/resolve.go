package engine

import (
	"context"
	"net"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/netutil"
	"github.com/nkcx/canarium/internal/state"
)

// resolveTimeout bounds a single name lookup during sequence setup.
const resolveTimeout = 5 * time.Second

// resolveClientAddresses pins every client's address at the moment a
// sequence starts.
//
// The sequences table has carried a resolved_addrs column since the first
// schema and nothing ever wrote to it. The reason it exists: a power event
// frequently takes out the same infrastructure Canarium depends on. If the
// DNS server is itself on the UPS — or behind the switch that just lost PoE —
// then by the time the wake plan runs, the names in the config may no longer
// resolve, and Canarium cannot wake the fleet it just shut down.
//
// Resolving once at the start, while the network is still whole, removes
// that dependency for the rest of the sequence. Names that do not resolve
// are recorded as unresolved rather than failing the sequence: the host may
// be reachable by a literal address configured elsewhere, and refusing to
// shut anything down because of a DNS hiccup is the wrong trade.
func (e *Executor) resolveClientAddresses(ctx context.Context) map[string]state.ResolvedAddr {
	resolved := make(map[string]state.ResolvedAddr, len(e.cfg.Clients))

	for i := range e.cfg.Clients {
		c := &e.cfg.Clients[i]
		if c.Address == "" {
			continue
		}

		entry := state.ResolvedAddr{
			Hostname:   c.Address,
			MAC:        clientMAC(c),
			ResolvedAt: time.Now().Format(time.RFC3339Nano),
		}

		if ip := net.ParseIP(c.Address); ip != nil {
			// Already a literal; nothing to pin.
			entry.IP = c.Address
		} else {
			lookupCtx, cancel := context.WithTimeout(ctx, resolveTimeout)
			addrs, err := net.DefaultResolver.LookupHost(lookupCtx, c.Address)
			cancel()

			if err != nil || len(addrs) == 0 {
				e.logger.Warn("could not resolve client address at sequence start; "+
					"the name will be resolved again at use, which may fail if DNS "+
					"is itself affected by this outage",
					"client", c.Name, "address", c.Address, "error", err)
			} else {
				entry.IP = addrs[0]
				e.logger.Info("pinned client address for this sequence",
					"client", c.Name, "address", c.Address, "resolved", addrs[0])
			}
		}

		if entry.MAC == "" {
			entry.MAC = e.discoverMAC(ctx, c, entry.IP)
		}

		resolved[c.Name] = entry
	}

	return resolved
}

// clientMAC returns the MAC to use for a client, preferring its wake config.
func clientMAC(c *config.ClientConfig) string {
	if c.Wake != nil && c.Wake.MAC != "" {
		return c.Wake.MAC
	}
	return c.MAC
}

// macFor returns the MAC to use for a client during a sequence, preferring
// the one pinned when the sequence started.
//
// The pinned value is the only one that can include a discovered MAC: the
// config may not carry one, and by wake time the host is off and cannot be
// asked. Without this the snapshot recorded a MAC that nothing ever read,
// and wake-on-LAN failed with "no MAC address configured" for a client
// whose MAC had been discovered minutes earlier.
func (e *Executor) macFor(as *ActiveSequence, c *config.ClientConfig) string {
	if as != nil {
		if pinned, ok := as.ResolvedAddr(c.Name); ok && pinned.MAC != "" {
			return pinned.MAC
		}
	}
	return clientMAC(c)
}

// addressFor returns the address to use for a client during a sequence,
// preferring the address pinned when the sequence started.
func (e *Executor) addressFor(as *ActiveSequence, c *config.ClientConfig) string {
	if as == nil {
		return c.Address
	}

	pinned, ok := as.ResolvedAddr(c.Name)
	if !ok || pinned.IP == "" {
		return c.Address
	}
	return pinned.IP
}

// discoverMACTimeout bounds one device's answer. Discovery is a
// convenience; a slow appliance must not hold up a shutdown that is racing
// a battery.
const discoverMACTimeout = 10 * time.Second

// discoverMAC finds a client's hardware address when the config omits it.
//
// Two sources, in order of how much they can be trusted:
//
//  1. The device itself, through its transport, for the transports whose
//     APIs report interface details. The device knows its own hardware and
//     the answer holds across subnets.
//
//  2. The kernel's neighbour table, which is a cache of what recently
//     answered on the local segment -- useful, but only for a host on a
//     directly attached subnet, and not at all from inside a container on
//     a bridge network.
//
// This runs at sequence start, while the fleet is still up, which is the
// only moment the question can be asked at all: once a host is off, nothing
// can report its MAC, and WOL is precisely what needs it.
func (e *Executor) discoverMAC(ctx context.Context, c *config.ClientConfig, ip string) string {
	if d, ok := e.transports[c.Transport].(MACDiscoverer); ok {
		discoverCtx, cancel := context.WithTimeout(ctx, discoverMACTimeout)
		mac, err := d.DiscoverMAC(discoverCtx, e.buildClient(c))
		cancel()

		switch {
		case err != nil:
			e.logger.Info("could not ask the device for its MAC address",
				"client", c.Name, "transport", c.Transport, "error", err)
		case mac != "":
			e.logger.Info("discovered client MAC address from its own API",
				"client", c.Name, "transport", c.Transport, "mac", mac)
			return mac
		}
	}

	if ip == "" {
		return ""
	}
	if mac := netutil.NeighbourMAC(ip); mac != "" {
		e.logger.Info("discovered client MAC address from the neighbour table",
			"client", c.Name, "address", ip, "mac", mac)
		return mac
	}

	return ""
}
