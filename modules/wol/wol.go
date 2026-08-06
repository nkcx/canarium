package wol

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/nkcx/canarium/internal/engine"
)

type Transport struct {
	repeatCount int
	repeatDelay time.Duration
}

type Config struct {
	RepeatCount int           `yaml:"repeat_count"`
	RepeatDelay time.Duration `yaml:"repeat_delay"`
}

func New(cfg Config) *Transport {
	count := cfg.RepeatCount
	if count == 0 {
		count = 3
	}
	delay := cfg.RepeatDelay
	if delay == 0 {
		delay = 500 * time.Millisecond
	}
	return &Transport{
		repeatCount: count,
		repeatDelay: delay,
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

	broadcast := "255.255.255.255"
	if client.WakeConfig != nil && client.WakeConfig.Broadcast != "" {
		broadcast = client.WakeConfig.Broadcast
	}

	packet, err := buildMagicPacket(mac)
	if err != nil {
		return nil, fmt.Errorf("building magic packet: %w", err)
	}

	for i := 0; i < t.repeatCount; i++ {
		if err := sendPacket(broadcast, packet); err != nil {
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
		Message: fmt.Sprintf("sent %d magic packets to %s via %s", t.repeatCount, mac, broadcast),
	}, nil
}

func (t *Transport) Probe(ctx context.Context, client *engine.Client) (engine.ClientState, error) {
	return engine.StateUnknown, fmt.Errorf("wol transport does not support probe")
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

func sendPacket(broadcast string, packet []byte) error {
	addr, err := net.ResolveUDPAddr("udp4", broadcast+":9")
	if err != nil {
		return fmt.Errorf("resolving broadcast address: %w", err)
	}

	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return fmt.Errorf("dialing UDP: %w", err)
	}
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = conn.Write(packet)
	return err
}
