package engine

import (
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
)

// derivedSource is the fact-key prefix for facts Canarium computes itself
// rather than reading from a device.
const derivedSource = "client"

// threatenedFact is the per-client derived fact name.
const threatenedFact = "threatened"

// threatFlags are UPS status flags that mean a feed is no longer carrying
// the load safely.
//
//	OB       on battery — mains is gone
//	LB       low battery
//	DISCHRG  discharging
//	ALARM    the UPS is reporting a fault
//	RB       replace battery: the UPS may not survive an outage at all
var threatFlags = []string{"OB", "LB", "DISCHRG", "ALARM", "RB"}

// registerDerivedFacts declares client.<name>.threatened for every client.
//
// These are declared like any other fact so conditions can reference them,
// `canarium validate` can check those references, and the dashboard can show
// them. SPEC §6.3 specifies this derivation; none of it existed.
func (e *Executor) registerDerivedFacts() {
	decls := make([]facts.FactDeclaration, 0, len(e.cfg.Clients))

	for _, c := range e.cfg.Clients {
		decls = append(decls, facts.FactDeclaration{
			Name: c.Name + "." + threatenedFact,
			Type: "bool",
			Description: "Whether " + c.Name + "'s power feeds are threatened, " +
				"per its feeds and feed_policy",
		})
	}

	// Derived facts are recomputed every policy tick, so their freshness
	// window follows that rather than any device's poll interval.
	e.store.RegisterSource(derivedSource, e.timings.Policy, decls)
}

// updateDerivedFacts recomputes every client's threatened fact.
//
// Runs on the same cadence as fact quality, so the value is current whether
// or not the executor is armed.
func (e *Executor) updateDerivedFacts(now time.Time) {
	for i := range e.cfg.Clients {
		c := &e.cfg.Clients[i]
		key := derivedSource + "." + c.Name + "." + threatenedFact
		e.store.Update(key, e.clientThreatened(c), now)
	}
}

// clientThreatened evaluates a client's feed policy.
//
// feed_policy "any" (the default, for a single-PSU machine) means the client
// is threatened as soon as one feed is. "all" means a dual-PSU machine on two
// UPSes is threatened only when both are failing — the whole point of
// redundant power is not shutting down when one side dies.
func (e *Executor) clientThreatened(c *config.ClientConfig) bool {
	feeds := e.clientFeeds(c)
	if len(feeds) == 0 {
		// Nothing to reason about. Not threatened is the safe answer: it
		// never initiates a shutdown on its own.
		return false
	}

	var result facts.Trilean
	if c.FeedPolicy == "all" {
		result = facts.True
		for _, feed := range feeds {
			result = facts.And(result, e.feedThreatened(feed))
			if result == facts.False {
				break
			}
		}
	} else {
		result = facts.False
		for _, feed := range feeds {
			result = facts.Or(result, e.feedThreatened(feed))
			if result == facts.True {
				break
			}
		}
	}

	switch result {
	case facts.True:
		return true
	case facts.False:
		return false
	default:
		// A feed whose status we cannot read.
		//
		// The default is to treat it as not threatened, so losing contact
		// with a sensor never initiates a shutdown — the safety property the
		// README leads with. comms_loss_assumes: threatened opts into the
		// opposite for operators who would rather shut down than risk
		// running on an unmonitored UPS.
		return c.CommsLossAssumes == config.CommsLossThreatened
	}
}

// clientFeeds returns the source instances powering a client.
//
// An empty feeds list means every declared UPS source, per SPEC §6.2: the
// single-UPS case should need no configuration at all.
func (e *Executor) clientFeeds(c *config.ClientConfig) []string {
	if len(c.Feeds) > 0 {
		return c.Feeds
	}
	return e.upsSources()
}

// upsSources returns every source instance that reports a UPS status fact.
//
// Derived from the store's declarations rather than a config list, so it
// reflects whatever sources are actually loaded.
func (e *Executor) upsSources() []string {
	e.upsSourcesOnce.Do(func() {
		seen := make(map[string]bool)
		for key := range e.store.AllDeclarations() {
			instance, rest, found := strings.Cut(key, ".")
			if !found || rest != "status" || instance == derivedSource {
				continue
			}
			seen[instance] = true
		}

		e.cachedUPSSources = make([]string, 0, len(seen))
		for instance := range seen {
			e.cachedUPSSources = append(e.cachedUPSSources, instance)
		}
		sort.Strings(e.cachedUPSSources)
	})

	return e.cachedUPSSources
}

// feedThreatened reports whether one source instance's UPS is failing.
//
// Unavailable is a distinct answer from false: it means the source has gone
// quiet, which the caller resolves according to comms_loss_assumes.
func (e *Executor) feedThreatened(feed string) facts.Trilean {
	value, quality, _ := e.store.Get(feed + ".status")
	if quality != facts.QualityGood || value == nil {
		return facts.Unavailable
	}

	switch flags := value.(type) {
	case []string:
		for _, flag := range flags {
			if slices.Contains(threatFlags, flag) {
				return facts.True
			}
		}
		return facts.False

	case []any:
		for _, item := range flags {
			if s, ok := item.(string); ok && slices.Contains(threatFlags, s) {
				return facts.True
			}
		}
		return facts.False

	case string:
		for _, flag := range strings.Fields(flags) {
			if slices.Contains(threatFlags, flag) {
				return facts.True
			}
		}
		return facts.False

	default:
		return facts.Unavailable
	}
}

// DerivedFactKeys returns the fact keys Canarium computes, for validation.
func DerivedFactKeys(cfg *config.Config) []string {
	keys := make([]string, 0, len(cfg.Clients))
	for _, c := range cfg.Clients {
		keys = append(keys, derivedSource+"."+c.Name+"."+threatenedFact)
	}
	sort.Strings(keys)
	return keys
}
