package wol

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/netutil"
)

type Transport struct {
	repeatCount int
	repeatDelay time.Duration
	port        int
	logger      *slog.Logger
}

// Config holds the wol transport's settings.
//
// RepeatDelay is a string rather than a time.Duration because yaml.v3 decodes
// a duration only from an integer nanosecond count, so "500ms" in a config
// file would have failed to parse. The field was unreachable in practice
// anyway: New was always called with a zero Config.
type Config struct {
	RepeatCount int    `yaml:"repeat_count"`
	RepeatDelay string `yaml:"repeat_delay"`
	Port        int    `yaml:"port"`
}

const (
	defaultRepeatCount = 3
	defaultRepeatDelay = 500 * time.Millisecond

	// defaultWOLPort is the conventional destination for magic packets.
	// Port 9 is discard; some hardware listens on 7 instead.
	defaultWOLPort = 9

	// globalBroadcast is the last-resort destination. Routers do not forward
	// it, so it only reaches the local segment.
	globalBroadcast = "255.255.255.255"
)

func New(cfg Config, logger *slog.Logger) *Transport {
	count := cfg.RepeatCount
	if count <= 0 {
		count = defaultRepeatCount
	}

	delay := defaultRepeatDelay
	if cfg.RepeatDelay != "" {
		if d, err := time.ParseDuration(cfg.RepeatDelay); err == nil {
			delay = d
		} else {
			logger.Error("invalid wol repeat_delay; using the default",
				"value", cfg.RepeatDelay, "default", defaultRepeatDelay, "error", err)
		}
	}

	port := cfg.Port
	if port <= 0 {
		port = defaultWOLPort
	}

	return &Transport{
		repeatCount: count,
		repeatDelay: delay,
		port:        port,
		logger:      logger,
	}
}

func (t *Transport) Name() string { return "wol" }

func (t *Transport) Capabilities() []engine.Capability {
	return []engine.Capability{
		{Action: engine.ActionWake, Idempotent: true, Timeout: 5 * time.Second},
	}
}

func (t *Transport) Execute(ctx context.Context, client *engine.Client, action engine.ActionType) (*engine.ActionResult, error) {
	if action != engine.ActionWake {
		return nil, fmt.Errorf("wol transport only supports wake action")
	}

	mac := client.MAC
	if client.WakeConfig != nil && client.WakeConfig.MAC != "" {
		mac = client.WakeConfig.MAC
	}
	if mac == "" {
		return nil, fmt.Errorf("no MAC address configured for client %s", client.Name)
	}

	broadcast := ""
	if client.WakeConfig != nil && client.WakeConfig.Broadcast != "" {
		broadcast = client.WakeConfig.Broadcast
	}
	if broadcast == "" && client.Address != "" {
		broadcast = inferBroadcast(client.Address, t.logger)
	}
	if broadcast == "" {
		broadcast = globalBroadcast
	}

	packet, err := buildMagicPacket(mac)
	if err != nil {
		return nil, fmt.Errorf("building magic packet: %w", err)
	}

	for i := 0; i < t.repeatCount; i++ {
		if err := sendPacket(broadcast, t.port, packet); err != nil {
			return &engine.ActionResult{Success: false, Message: err.Error()}, err
		}
		if i < t.repeatCount-1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(t.repeatDelay):
			}
		}
	}

	return &engine.ActionResult{
		Success: true,
		Message: fmt.Sprintf("sent %d magic packets to %s via %s:%d",
			t.repeatCount, mac, broadcast, t.port),
	}, nil
}

func (t *Transport) Probe(ctx context.Context, client *engine.Client) (engine.ClientState, error) {
	return engine.StateUnknown, fmt.Errorf("wol transport does not support probe")
}

// inferBroadcast computes the broadcast address for a client IP by checking
// the host's network interfaces for a matching subnet. Falls back to a /24
// assumption if no interface match is found.
func inferBroadcast(clientIP string, logger *slog.Logger) string {
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return ""
	}
	ip = ip.To4()
	if ip == nil {
		return ""
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		logger.Warn("could not list network interfaces for broadcast inference", "error", err)
		return broadcastFromCIDR(ip, net.CIDRMask(24, 32))
	}

	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ifIP := ipNet.IP.To4()
			if ifIP == nil {
				continue
			}
			if ipNet.Contains(ip) {
				bcast := broadcastFromCIDR(ip, ipNet.Mask)
				logger.Info("inferred broadcast address",
					"client_ip", clientIP,
					"interface", iface.Name,
					"subnet", ipNet,
					"broadcast", bcast,
				)
				return bcast
			}
		}
	}

	bcast := broadcastFromCIDR(ip, net.CIDRMask(24, 32))
	logger.Info("no matching interface found, assuming /24",
		"client_ip", clientIP,
		"broadcast", bcast,
	)
	return bcast
}

// broadcastFromCIDR computes the broadcast address for an IP within a subnet.
//
// The mask is normalised to four bytes first. net.IPNet.Mask for an IPv4
// address may legitimately be either 4 or 16 bytes depending on how the
// address was obtained, and indexing the first four bytes of a 16-byte
// IPv4-in-IPv6 mask reads the all-zero prefix — yielding 255.255.255.255 and
// sending the magic packet to the global broadcast address instead of the
// client's subnet, which routers drop.
func broadcastFromCIDR(ip net.IP, mask net.IPMask) string {
	ip = ip.To4()
	if ip == nil {
		return globalBroadcast
	}

	mask4 := normalizeMask(mask)
	if mask4 == nil {
		return globalBroadcast
	}

	bcast := make(net.IP, net.IPv4len)
	for i := 0; i < net.IPv4len; i++ {
		bcast[i] = ip[i] | ^mask4[i]
	}
	return bcast.String()
}

// normalizeMask reduces an IPv4 mask to its 4-byte form, returning nil if it
// is not an IPv4 mask.
func normalizeMask(mask net.IPMask) net.IPMask {
	switch len(mask) {
	case net.IPv4len:
		return mask
	case net.IPv6len:
		// An IPv4-mapped mask is 80 zero bits, 16 one bits, then the mask.
		ones, bits := mask.Size()
		if bits != 128 || ones < 96 {
			return nil
		}
		return net.CIDRMask(ones-96, 32)
	default:
		return nil
	}
}

func buildMagicPacket(macAddr string) ([]byte, error) {
	macAddr = strings.ReplaceAll(macAddr, ":", "")
	macAddr = strings.ReplaceAll(macAddr, "-", "")
	macAddr = strings.ToLower(macAddr)

	if len(macAddr) != 12 {
		return nil, fmt.Errorf("invalid MAC address length: %s", macAddr)
	}

	macBytes, err := hex.DecodeString(macAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid MAC address: %w", err)
	}

	packet := make([]byte, 102)
	for i := 0; i < 6; i++ {
		packet[i] = 0xFF
	}
	for i := 0; i < 16; i++ {
		copy(packet[6+i*6:], macBytes)
	}

	return packet, nil
}

func sendPacket(broadcast string, port int, packet []byte) error {
	addr, err := net.ResolveUDPAddr("udp4", netutil.HostPort(broadcast, port))
	if err != nil {
		return fmt.Errorf("resolving broadcast address: %w", err)
	}

	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return fmt.Errorf("dialing UDP: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return fmt.Errorf("setting write deadline: %w", err)
	}
	if _, err := conn.Write(packet); err != nil {
		return fmt.Errorf("sending magic packet: %w", err)
	}
	return nil
}
