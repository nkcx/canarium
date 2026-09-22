package truenas

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/netutil"
)

// nasInterface is one entry of TrueNAS's interface.query.
//
// Only the fields needed here are declared. TrueNAS returns a great deal
// more per interface, and the shape shifts between major versions, so a
// narrow struct keeps this from breaking on a field it does not read.
type nasInterface struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	State struct {
		LinkAddress string `json:"link_address"`
		LinkState   string `json:"link_state"`
		Aliases     []struct {
			Type    string `json:"type"`
			Address string `json:"address"`
		} `json:"aliases"`
	} `json:"state"`
	Aliases []struct {
		Type    string `json:"type"`
		Address string `json:"address"`
	} `json:"aliases"`
}

// DiscoverMAC asks TrueNAS for the hardware address of the interface that
// holds the client's address.
//
// A NAS is usually shut down early in a sequence and woken late, and by the
// time the wake plan runs it has been off for however long the outage
// lasted -- so its MAC has to be known before any of that starts. TrueNAS
// reports its interfaces over the same API the shutdown goes through, so it
// can be asked while it is still up.
func (t *Transport) DiscoverMAC(ctx context.Context, client *engine.Client) (string, error) {
	conn, err := t.connect(ctx, client)
	if err != nil {
		return "", fmt.Errorf("connecting to TrueNAS: %w", err)
	}
	defer conn.Close()

	// As in Execute: gorilla's reads have no context of their own, so the
	// connection is closed when the caller's context ends.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	if err := t.authenticate(ctx, conn, client); err != nil {
		// The error may quote the request, which carries the API key.
		return "", fmt.Errorf("authentication failed: %s",
			netutil.RedactSecrets(err.Error(), client.Credentials))
	}

	result, err := t.callRPC(ctx, conn, "interface.query", []any{})
	if err != nil {
		return "", fmt.Errorf("querying interfaces: %w", err)
	}

	interfaces, err := decodeInterfaces(result)
	if err != nil {
		return "", err
	}

	return pickInterfaceMAC(interfaces, client.Address), nil
}

// decodeInterfaces re-encodes the RPC result into the fields that matter.
// callRPC returns `any`, and round-tripping through JSON is simpler than
// walking maps by hand and gets the same type checking.
func decodeInterfaces(result any) ([]nasInterface, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("re-encoding interface list: %w", err)
	}

	var interfaces []nasInterface
	if err := json.Unmarshal(raw, &interfaces); err != nil {
		return nil, fmt.Errorf("decoding interface list: %w", err)
	}
	return interfaces, nil
}

// pickInterfaceMAC chooses the interface whose address the client is
// reached on.
//
// Matching on the address matters: a NAS often has several interfaces, and
// a magic packet sent to the wrong one is dropped without a word. Where
// nothing matches -- the client is configured by a name that resolves to an
// address the NAS reports differently, say -- a single physical interface
// with a link is unambiguous. Beyond that, guessing is worse than saying
// there is no answer.
func pickInterfaceMAC(interfaces []nasInterface, address string) string {
	target := net.ParseIP(address)

	var candidates []nasInterface
	for _, iface := range interfaces {
		mac := iface.State.LinkAddress
		if !validMAC(mac) {
			continue
		}

		if target != nil && interfaceHasAddress(iface, target) {
			return normaliseMAC(mac)
		}

		if iface.Type == "PHYSICAL" && iface.State.LinkState != "LINK_STATE_DOWN" {
			candidates = append(candidates, iface)
		}
	}

	if len(candidates) == 1 {
		return normaliseMAC(candidates[0].State.LinkAddress)
	}
	return ""
}

// interfaceHasAddress reports whether an interface holds an IP.
//
// Aliases have moved between the interface and its state across TrueNAS
// versions, so both are checked rather than betting on one.
func interfaceHasAddress(iface nasInterface, target net.IP) bool {
	for _, alias := range iface.State.Aliases {
		if ip := net.ParseIP(alias.Address); ip != nil && ip.Equal(target) {
			return true
		}
	}
	for _, alias := range iface.Aliases {
		if ip := net.ParseIP(alias.Address); ip != nil && ip.Equal(target) {
			return true
		}
	}
	return false
}

func validMAC(s string) bool {
	hw, err := net.ParseMAC(s)
	if err != nil {
		return false
	}
	for _, b := range hw {
		if b != 0 {
			return true
		}
	}
	return false
}

func normaliseMAC(s string) string {
	hw, err := net.ParseMAC(s)
	if err != nil {
		return s
	}
	return hw.String()
}
