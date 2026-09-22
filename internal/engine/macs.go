package engine

import (
	"context"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/netutil"
	"github.com/nkcx/canarium/internal/state"
)

// How often a client's hardware address is looked up.
//
// Discovery costs an authenticated API call to an appliance, so it cannot
// ride the probe tick. These intervals are the compromise between staying
// current and being a nuisance to the device:
//
//   - A known address is re-confirmed every few hours. A MAC changes when
//     somebody replaces a NIC, which is rare and not urgent.
//   - An unknown one is retried every few minutes, because until it is
//     known the client cannot be woken at all.
const (
	macRefreshInterval = 6 * time.Hour
	macRetryInterval   = 5 * time.Minute
)

// learnMACs looks up the hardware address of any client that needs one.
//
// Called from the probe loop, so discovery is continuous rather than a
// single attempt at sequence start. That matters because of when the value
// is used: wake-on-LAN needs a MAC for a host that is off, and a host that
// is off cannot be asked for it. The only way to have the answer then is to
// have kept asking while the host was up.
//
// Configuring `mac:` by hand remains the most reliable option and always
// wins. This exists so that nobody is *dependent* on having done so.
func (e *Executor) learnMACs() {
	// Not during a sequence. The fleet is being shut down, its addresses
	// were pinned when the sequence started, and an appliance handling a
	// shutdown request does not also need a status query.
	if e.ActiveSequence() != nil {
		return
	}

	for i := range e.cfg.Clients {
		c := &e.cfg.Clients[i]

		// A configured address is a deliberate statement, and asking the
		// device would only risk contradicting it.
		if clientMAC(c) != "" {
			continue
		}
		if !e.shouldLookUpMAC(c.Name) {
			continue
		}
		// Only a client that is up can answer, and a probe just told us.
		if e.GetClientState(c.Name) != StateUp {
			continue
		}

		e.noteMACAttempt(c.Name)

		mac, source := e.lookUpMAC(e.ctx, c, e.addressFor(nil, c))
		if mac == "" {
			continue
		}
		e.recordLearnedMAC(c.Name, mac, source)
	}
}

// shouldLookUpMAC rate-limits lookups per client.
func (e *Executor) shouldLookUpMAC(name string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	interval := macRetryInterval
	if _, known := e.learnedMACs[name]; known {
		interval = macRefreshInterval
	}

	last, tried := e.macAttempts[name]
	return !tried || time.Since(last) >= interval
}

func (e *Executor) noteMACAttempt(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.macAttempts == nil {
		e.macAttempts = make(map[string]time.Time)
	}
	e.macAttempts[name] = time.Now()
}

// lookUpMAC asks the device, then the kernel, for a client's hardware
// address. It returns the address and where it came from, or "" for both.
//
// Anything that is not a usable Ethernet address is discarded rather than
// stored: an all-zero address from an incomplete neighbour entry, or a
// multicast address where a hardware address was expected, would be
// recorded and would then fail to wake anything.
func (e *Executor) lookUpMAC(ctx context.Context, c *config.ClientConfig, ip string) (mac, source string) {
	if d, ok := e.transports[c.Transport].(MACDiscoverer); ok {
		lookupCtx, cancel := context.WithTimeout(ctx, discoverMACTimeout)
		found, err := d.DiscoverMAC(lookupCtx, e.buildClient(c))
		cancel()

		switch {
		case err != nil:
			e.logger.Debug("could not ask the device for its MAC address",
				"client", c.Name, "transport", c.Transport, "error", err)
		case found != "":
			if normalised, ok := netutil.NormaliseMAC(found); ok {
				return normalised, "the " + c.Transport + " API"
			}
			e.logger.Warn("device reported an unusable MAC address; ignoring it",
				"client", c.Name, "transport", c.Transport, "reported", found)
		}
	}

	if ip == "" {
		return "", ""
	}
	if found := netutil.NeighbourMAC(ip); found != "" {
		if normalised, ok := netutil.NormaliseMAC(found); ok {
			return normalised, "the neighbour table"
		}
	}

	return "", ""
}

// recordLearnedMAC stores a discovered address, in memory and on disk.
//
// An address that has not changed only moves its confirmation time. A
// different one is worth saying out loud: either a NIC was replaced, or
// something is answering for an address that is not the client any more,
// and both are things an operator would want to have been told.
func (e *Executor) recordLearnedMAC(client, mac, source string) {
	now := time.Now()

	e.mu.Lock()
	previous, existed := e.learnedMACs[client]
	entry := state.LearnedMAC{
		Client: client, MAC: mac, Source: source,
		LearnedAt: now, ConfirmedAt: now,
	}
	if existed && previous.MAC == mac {
		entry.LearnedAt = previous.LearnedAt
	}
	if e.learnedMACs == nil {
		e.learnedMACs = make(map[string]state.LearnedMAC)
	}
	e.learnedMACs[client] = entry
	e.mu.Unlock()

	switch {
	case !existed:
		e.logger.Info("learned client MAC address",
			"client", client, "mac", mac, "source", source)
	case previous.MAC != mac:
		e.logger.Warn("client MAC address changed",
			"client", client, "was", previous.MAC, "now", mac, "source", source)
	default:
		e.logger.Debug("re-confirmed client MAC address",
			"client", client, "mac", mac, "source", source)
	}

	if err := e.db.SaveLearnedMAC(e.ctx, entry); err != nil {
		// In memory it is still usable for this run; only a restart would
		// lose it, and refusing to use it now would help nobody.
		e.logger.Error("persisting learned MAC address",
			"client", client, "error", err)
	}
}

// LearnedMAC returns what has been discovered for a client, if anything.
func (e *Executor) LearnedMAC(client string) (state.LearnedMAC, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	m, ok := e.learnedMACs[client]
	return m, ok
}

// ReloadLearnedMACs reads discovered addresses from storage into memory.
//
// Called during startup, and exported because the store is a plain table
// that can be changed from outside the process -- by `canarium` on another
// host sharing the data directory, or by an operator correcting a row --
// and the running daemon should be able to pick that up without a restart.
func (e *Executor) ReloadLearnedMACs() error { return e.restoreLearnedMACs() }

// restoreLearnedMACs loads previously discovered addresses at startup.
//
// Without this the daemon would be at its most ignorant exactly when a
// restart is most likely: after the power event it exists to handle, with
// the whole fleet still down and nothing able to report its own address.
func (e *Executor) restoreLearnedMACs() error {
	stored, err := e.db.LearnedMACs(e.ctx)
	if err != nil {
		return err
	}

	configured := make(map[string]bool, len(e.cfg.Clients))
	for i := range e.cfg.Clients {
		configured[e.cfg.Clients[i].Name] = true
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.learnedMACs == nil {
		e.learnedMACs = make(map[string]state.LearnedMAC, len(stored))
	}
	for name, m := range stored {
		// Rows for clients no longer in the config are left on disk but
		// not loaded, as with client states: removing a client from the
		// config should remove it from the running system.
		if !configured[name] {
			continue
		}
		if _, ok := netutil.NormaliseMAC(m.MAC); !ok {
			e.logger.Warn("discarding an unusable stored MAC address",
				"client", name, "mac", m.MAC)
			continue
		}
		e.learnedMACs[name] = m
	}

	return nil
}

// MACFor returns the hardware address that would be used to wake a client
// outside a sequence, for display.
func (e *Executor) MACFor(c *config.ClientConfig) string {
	return e.macFor(nil, c)
}
