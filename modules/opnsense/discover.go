package opnsense

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/netutil"
)

// interfaceConfig is one entry of /api/diagnostics/interface/getInterfaceConfig,
// which returns an object keyed by device name.
//
// Only the fields needed here are declared; OPNsense returns considerably
// more (media, flags, capabilities, IPv6) and adds to it between releases,
// so decoding into a narrow struct keeps this from breaking on a field it
// does not care about.
type interfaceConfig struct {
	Device     string `json:"device"`
	MacAddr    string `json:"macaddr"`
	IsPhysical bool   `json:"is_physical"`
	Status     string `json:"status"`
	IPv4       []struct {
		IPAddr string `json:"ipaddr"`
	} `json:"ipv4"`
}

// DiscoverMAC asks OPNsense for the hardware address of the interface that
// holds the client's address.
//
// OPNsense is frequently the last thing shut down and the first thing that
// has to come back, and it is also the device most likely to be on a
// different VLAN from Canarium -- where the neighbour table can never help.
// Its diagnostics API reports each interface's MAC, so it can simply be
// asked while it is still up.
func (t *Transport) DiscoverMAC(ctx context.Context, client *engine.Client) (string, error) {
	configs, err := t.interfaceConfigs(ctx, client)
	if err != nil {
		return "", err
	}

	return pickInterfaceMAC(configs, client.Address), nil
}

func (t *Transport) interfaceConfigs(ctx context.Context, client *engine.Client) (map[string]interfaceConfig, error) {
	port := 443
	if p, ok := client.TransportConfig["port"].(int); ok {
		port = p
	}

	apiURL := netutil.URL("https", client.Address, port, "/api/diagnostics/interface/getInterfaceConfig")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if client.Credentials != "" {
		key, secret := parseCredentials(client.Credentials)
		req.SetBasicAuth(key, secret)
	}

	httpClient, err := t.httpClientFor(client)
	if err != nil {
		return nil, fmt.Errorf("opnsense TLS configuration: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Bounded: this is parsed into memory on a device with a 128 MB
	// ceiling, and the response grows with the interface count.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading interface list: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from the interface API", resp.StatusCode)
	}

	var configs map[string]interfaceConfig
	if err := json.Unmarshal(body, &configs); err != nil {
		return nil, fmt.Errorf("decoding interface list: %w", err)
	}
	return configs, nil
}

// pickInterfaceMAC chooses the interface whose address the client is
// reached on.
//
// Matching on the address matters: a firewall has an interface per network,
// and a magic packet sent to the wrong one is dropped silently. If nothing
// matches -- the client is configured by a hostname that resolves
// elsewhere, say -- and exactly one physical interface is up with a MAC,
// that one is unambiguous. Beyond that, guessing would be worse than
// admitting there is no answer.
func pickInterfaceMAC(configs map[string]interfaceConfig, address string) string {
	target := net.ParseIP(address)

	var candidates []interfaceConfig
	for _, cfg := range configs {
		if cfg.MacAddr == "" || !validMAC(cfg.MacAddr) {
			continue
		}

		if target != nil {
			for _, addr := range cfg.IPv4 {
				if ip := net.ParseIP(addr.IPAddr); ip != nil && ip.Equal(target) {
					return normaliseMAC(cfg.MacAddr)
				}
			}
		}

		if cfg.IsPhysical && cfg.Status == "up" {
			candidates = append(candidates, cfg)
		}
	}

	if len(candidates) == 1 {
		return normaliseMAC(candidates[0].MacAddr)
	}
	return ""
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
