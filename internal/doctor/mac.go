package doctor

import (
	"context"
	"fmt"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/netutil"
	"github.com/nkcx/canarium/internal/state"
)

// staleMACAge is how long a learned address may go unconfirmed before it is
// worth mentioning. The daemon re-confirms every few hours while a client is
// up, so a value older than this means the client has not been seen up for
// days -- which is worth knowing before an outage, not during one.
const staleMACAge = 7 * 24 * time.Hour

// SetLearnedMACs supplies what the daemon has already discovered.
//
// doctor usually runs as a separate process from the daemon, so it reads
// them out of the state database. Without them it would report a client as
// unwakeable whenever it happened to be off at the moment doctor ran, even
// though the daemon has known its address for weeks.
func (d *Doctor) SetLearnedMACs(macs map[string]state.LearnedMAC) {
	d.learnedMACs = macs
}

// checkWakeMAC answers the question doctor exists for, applied to
// wake-on-LAN: when the power comes back, can this client actually be
// woken?
//
// WOL needs a hardware address, and a host that is off cannot be asked for
// one. So the answer has to be known in advance, and the only bad time to
// find out that it is not is during the recovery.
func (d *Doctor) checkWakeMAC(ctx context.Context, c *config.ClientConfig, add func(Check)) {
	if !usesWOL(c) {
		return
	}

	subject := fmt.Sprintf("client %q", c.Name)
	const name = "wake MAC"

	if mac := configuredMAC(c); mac != "" {
		if _, ok := netutil.NormaliseMAC(mac); !ok {
			add(Check{
				Subject: subject, Name: name, Status: StatusFail,
				Detail: fmt.Sprintf("%q is not a usable hardware address; "+
					"wake-on-LAN will fail", mac),
			})
			return
		}
		add(Check{
			Subject: subject, Name: name, Status: StatusOK,
			Detail: fmt.Sprintf("%s, from the configuration file", mac),
		})
		return
	}

	// What the daemon already knows, which survives the client being off.
	if learned, ok := d.learnedMACs[c.Name]; ok {
		if _, valid := netutil.NormaliseMAC(learned.MAC); valid {
			age := time.Since(learned.ConfirmedAt)
			detail := fmt.Sprintf("%s, discovered from %s and last confirmed %s",
				learned.MAC, learned.Source, describeAge(age))

			status := StatusOK
			if age > staleMACAge {
				status = StatusWarn
				detail += "; the client has not been seen up since. Set mac: to be certain"
			}
			add(Check{Subject: subject, Name: name, Status: status, Detail: detail})
			return
		}
	}

	// Nothing on record: try to find it now, which is what the rest of
	// doctor does too.
	start := time.Now()
	if mac, source := d.discoverMACNow(ctx, c); mac != "" {
		add(Check{
			Subject: subject, Name: name, Status: StatusOK,
			Detail: fmt.Sprintf("%s, discovered now from %s; the daemon keeps this "+
				"up to date while the client is up", mac, source),
			Duration: time.Since(start),
		})
		return
	}

	_, discoverable := d.transports[c.Transport].(engine.MACDiscoverer)
	detail := "no mac: configured and none could be discovered. " +
		"Wake-on-LAN cannot run for this client"
	if !discoverable {
		detail = fmt.Sprintf("no mac: configured, and the %s transport cannot "+
			"report one. Wake-on-LAN cannot run for this client", c.Transport)
	}

	add(Check{
		Subject: subject, Name: name, Status: StatusFail,
		Detail:   detail + " — set mac: on it",
		Duration: time.Since(start),
	})
}

// discoverMACNow asks the device, then the kernel.
func (d *Doctor) discoverMACNow(ctx context.Context, c *config.ClientConfig) (mac, source string) {
	if discoverer, ok := d.transports[c.Transport].(engine.MACDiscoverer); ok {
		lookupCtx, cancel := context.WithTimeout(ctx, d.opts.Timeout)
		found, err := discoverer.DiscoverMAC(lookupCtx, &engine.Client{
			Name:            c.Name,
			Address:         c.Address,
			Credentials:     c.Credentials,
			TransportConfig: c.Config,
		})
		cancel()

		if err == nil {
			if normalised, valid := netutil.NormaliseMAC(found); valid {
				return normalised, "the " + c.Transport + " API"
			}
		}
	}

	if found := netutil.NeighbourMAC(c.Address); found != "" {
		if normalised, valid := netutil.NormaliseMAC(found); valid {
			return normalised, "the neighbour table"
		}
	}

	return "", ""
}

// usesWOL reports whether this client would be woken by a magic packet.
func usesWOL(c *config.ClientConfig) bool {
	if c.Wake != nil {
		return c.Wake.Transport == "wol"
	}
	// A client with no wake block is never woken, so it needs no address.
	return c.Transport == "wol"
}

func configuredMAC(c *config.ClientConfig) string {
	if c.Wake != nil && c.Wake.MAC != "" {
		return c.Wake.MAC
	}
	return c.MAC
}

func describeAge(age time.Duration) string {
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(age.Minutes()))
	case age < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(age.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(age.Hours()/24))
	}
}
