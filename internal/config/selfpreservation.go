package config

import (
	"net"
	"os"
	"strings"
)

// LocalIdentity describes the host Canarium is running on, so validation can
// refuse a configuration that would have it shut itself down.
//
// SPEC §8.3: "Canarium must outlive everything it controls."
type LocalIdentity struct {
	// Hostnames the local host answers to, lowercased.
	Hostnames []string

	// Addresses configured on the local host's interfaces.
	Addresses []net.IP
}

// DetectLocalIdentity gathers the local hostname and interface addresses.
//
// Failures are not fatal: the checks that use this degrade to doing nothing,
// which is the same position as before it existed. Refusing to start because
// the hostname could not be read would be a worse outcome than not running
// one validation.
func DetectLocalIdentity() *LocalIdentity {
	id := &LocalIdentity{}

	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		id.Hostnames = append(id.Hostnames, strings.ToLower(hostname))
		// The short form, for a fully qualified hostname.
		if short, _, found := strings.Cut(hostname, "."); found && short != "" {
			id.Hostnames = append(id.Hostnames, strings.ToLower(short))
		}
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return id
	}

	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		// Loopback is excluded deliberately: a client configured as
		// 127.0.0.1 is almost always a deliberate local test target rather
		// than an operator pointing Canarium at itself.
		if ipNet.IP.IsLoopback() {
			continue
		}
		id.Addresses = append(id.Addresses, ipNet.IP)
	}

	return id
}

// Matches reports whether an address or hostname refers to the local host.
func (id *LocalIdentity) Matches(address string) bool {
	if id == nil {
		return false
	}

	address = strings.ToLower(strings.TrimSpace(address))
	if address == "" {
		return false
	}

	// Strip a port if one was included.
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}

	if ip := net.ParseIP(address); ip != nil {
		for _, local := range id.Addresses {
			if local.Equal(ip) {
				return true
			}
		}
		return false
	}

	for _, hostname := range id.Hostnames {
		if hostname == address {
			return true
		}
	}
	return false
}

// validateSelfPreservation refuses a configuration that would have Canarium
// shut down the host it is running on.
//
// SPEC §8.3 requires this, and nothing implemented it. An operator adding
// the Canarium host to a shutdown stage — easy to do when it runs on one of
// the machines it manages — produces a daemon that shuts itself down partway
// through a sequence, leaving the rest of the fleet running on a UPS with
// nothing watching it. That is the worst failure this software has.
func validateSelfPreservation(cfg *Config, result *ValidationResult, id *LocalIdentity) {
	if id == nil {
		return
	}

	if len(id.Hostnames) == 0 && len(id.Addresses) == 0 {
		result.AddWarning("could not determine this host's identity, so the check " +
			"that Canarium is not configured to shut itself down was skipped")
		return
	}

	// Which clients appear in a shutdown stage, and where.
	type placement struct{ plan, stage string }
	inShutdown := make(map[string]placement)

	for _, p := range cfg.Plans {
		for _, s := range p.Shutdown.Stages {
			for _, name := range ResolveClientRefs(s.Clients, cfg) {
				if _, seen := inShutdown[name]; !seen {
					inShutdown[name] = placement{plan: p.Name, stage: s.Name}
				}
			}
		}
	}

	for _, c := range cfg.Clients {
		if !id.Matches(c.Address) {
			continue
		}

		where, scheduled := inShutdown[c.Name]
		if !scheduled {
			result.AddWarning("client %q (%s) appears to be this host. It is not in any "+
				"shutdown stage, so nothing will act on it, but check that this is intended.",
				c.Name, c.Address)
			continue
		}

		result.AddError("client %q (%s) is the host Canarium is running on, and plan %q "+
			"stage %q would shut it down. Canarium would stop partway through the "+
			"sequence, leaving the rest of the fleet running with nothing watching "+
			"the power. Remove it from the stage, or run Canarium elsewhere.",
			c.Name, c.Address, where.plan, where.stage)
	}

	// A post-shutdown action cuts power to everything on that UPS, including
	// this host if it is fed by it. Canarium cannot know the wiring, so this
	// is a warning rather than an error.
	for _, p := range cfg.Plans {
		ps := p.Shutdown.PostShutdown
		if ps == nil {
			continue
		}
		result.AddInfo("plan %q: post_shutdown will command UPS %q to %s. If this host "+
			"is powered by that UPS, Canarium will lose power too — which is "+
			"correct once everything is down, but means it cannot wake anything "+
			"until mains returns and it boots again.",
			p.Name, ps.UPS, postShutdownDescription(ps))
	}
}

func postShutdownDescription(ps *PostShutdownConfig) string {
	if ps.Command != "" {
		return "run " + ps.Command
	}
	return "cut its outlets"
}
