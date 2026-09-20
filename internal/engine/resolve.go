package engine

import (
	"context"
	"net"
	"time"

	"github.com/nkcx/canarium/internal/config"
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
			resolved[c.Name] = entry
			continue
		}

		lookupCtx, cancel := context.WithTimeout(ctx, resolveTimeout)
		addrs, err := net.DefaultResolver.LookupHost(lookupCtx, c.Address)
		cancel()

		if err != nil || len(addrs) == 0 {
			e.logger.Warn("could not resolve client address at sequence start; "+
				"the name will be resolved again at use, which may fail if DNS "+
				"is itself affected by this outage",
				"client", c.Name, "address", c.Address, "error", err)
			resolved[c.Name] = entry
			continue
		}

		entry.IP = addrs[0]
		resolved[c.Name] = entry

		e.logger.Info("pinned client address for this sequence",
			"client", c.Name, "address", c.Address, "resolved", addrs[0])
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
