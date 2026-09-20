package wol

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func TestBuildMagicPacket(t *testing.T) {
	packet, err := buildMagicPacket("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(packet) != 102 {
		t.Errorf("packet length = %d, want 102", len(packet))
	}

	for i := 0; i < 6; i++ {
		if packet[i] != 0xFF {
			t.Errorf("sync byte %d = %02x, want FF", i, packet[i])
		}
	}

	expectedMAC := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	for i := 0; i < 16; i++ {
		offset := 6 + i*6
		for j := 0; j < 6; j++ {
			if packet[offset+j] != expectedMAC[j] {
				t.Errorf("MAC repeat %d byte %d = %02x, want %02x", i, j, packet[offset+j], expectedMAC[j])
			}
		}
	}
}

func TestBuildMagicPacketDashes(t *testing.T) {
	packet, err := buildMagicPacket("AA-BB-CC-DD-EE-FF")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(packet) != 102 {
		t.Errorf("packet length = %d, want 102", len(packet))
	}
}

func TestBuildMagicPacketNoSeparators(t *testing.T) {
	packet, err := buildMagicPacket("aabbccddeeff")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(packet) != 102 {
		t.Errorf("packet length = %d, want 102", len(packet))
	}
}

func TestBuildMagicPacketInvalid(t *testing.T) {
	_, err := buildMagicPacket("invalid")
	if err == nil {
		t.Error("expected error for invalid MAC")
	}

	_, err = buildMagicPacket("aa:bb:cc:dd:ee")
	if err == nil {
		t.Error("expected error for short MAC")
	}

	_, err = buildMagicPacket("gg:hh:ii:jj:kk:ll")
	if err == nil {
		t.Error("expected error for non-hex MAC")
	}
}

func TestBroadcastFromCIDR(t *testing.T) {
	tests := []struct {
		ip   string
		mask int
		want string
	}{
		{"10.0.10.11", 24, "10.0.10.255"},
		{"10.0.10.11", 16, "10.0.255.255"},
		{"192.168.1.100", 24, "192.168.1.255"},
		{"172.16.0.5", 12, "172.31.255.255"},
		{"10.0.10.11", 32, "10.0.10.11"},
		{"10.0.10.11", 8, "10.255.255.255"},
	}

	for _, tt := range tests {
		ip := net.ParseIP(tt.ip).To4()
		mask := net.CIDRMask(tt.mask, 32)
		got := broadcastFromCIDR(ip, mask)
		if got != tt.want {
			t.Errorf("broadcastFromCIDR(%s, /%d) = %s, want %s", tt.ip, tt.mask, got, tt.want)
		}
	}
}

func TestInferBroadcastInvalidIP(t *testing.T) {
	got := inferBroadcast("not-an-ip", nil)
	if got != "" {
		t.Errorf("inferBroadcast(invalid) = %s, want empty", got)
	}
}

func TestInferBroadcastIPv6(t *testing.T) {
	got := inferBroadcast("::1", nil)
	if got != "" {
		t.Errorf("inferBroadcast(ipv6) = %s, want empty", got)
	}
}

// TestBroadcastFromCIDRHandlesSixteenByteMask is the regression test for
// indexing the first four bytes of an IPv4-in-IPv6 mask.
//
// net.IPNet.Mask for an IPv4 address may be either 4 or 16 bytes. Reading
// mask[0:4] of the 16-byte form gets the all-zero prefix, so the computed
// broadcast was 255.255.255.255 — the global broadcast, which routers drop,
// rather than the client's subnet broadcast.
func TestBroadcastFromCIDRHandlesSixteenByteMask(t *testing.T) {
	ip := net.ParseIP("10.0.10.11")

	four := net.CIDRMask(24, 32)
	sixteen := net.CIDRMask(96+24, 128) // IPv4-mapped /24

	wantFour := broadcastFromCIDR(ip, four)
	if wantFour != "10.0.10.255" {
		t.Fatalf("4-byte mask gave %q, want 10.0.10.255", wantFour)
	}

	gotSixteen := broadcastFromCIDR(ip, sixteen)
	if gotSixteen != wantFour {
		t.Errorf("16-byte mask gave %q, want %q — the magic packet would go to "+
			"the global broadcast instead of the client's subnet",
			gotSixteen, wantFour)
	}
}

func TestBroadcastFromCIDRMaskSizes(t *testing.T) {
	ip := net.ParseIP("192.168.1.50")

	tests := []struct {
		name string
		mask net.IPMask
		want string
	}{
		{"/24", net.CIDRMask(24, 32), "192.168.1.255"},
		{"/16", net.CIDRMask(16, 32), "192.168.255.255"},
		{"/25 lower half", net.CIDRMask(25, 32), "192.168.1.127"},
		{"/30", net.CIDRMask(30, 32), "192.168.1.51"},
		{"/32", net.CIDRMask(32, 32), "192.168.1.50"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := broadcastFromCIDR(ip, tt.mask); got != tt.want {
				t.Errorf("broadcastFromCIDR(%v, %v) = %q, want %q", ip, tt.mask, got, tt.want)
			}
		})
	}
}

func TestBroadcastFromCIDRRejectsIPv6(t *testing.T) {
	ip := net.ParseIP("fd00::1")
	if got := broadcastFromCIDR(ip, net.CIDRMask(64, 128)); got != globalBroadcast {
		t.Errorf("broadcastFromCIDR for IPv6 = %q, want %q", got, globalBroadcast)
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	tr := New(Config{}, discardLogger())

	if tr.repeatCount != defaultRepeatCount {
		t.Errorf("repeatCount = %d, want %d", tr.repeatCount, defaultRepeatCount)
	}
	if tr.repeatDelay != defaultRepeatDelay {
		t.Errorf("repeatDelay = %v, want %v", tr.repeatDelay, defaultRepeatDelay)
	}
	if tr.port != defaultWOLPort {
		t.Errorf("port = %d, want %d", tr.port, defaultWOLPort)
	}
}

// TestNewHonoursConfig covers settings that were previously unreachable:
// New was always called with a zero Config, and RepeatDelay was typed as
// time.Duration, which yaml.v3 cannot decode from "500ms".
func TestNewHonoursConfig(t *testing.T) {
	tr := New(Config{RepeatCount: 5, RepeatDelay: "250ms", Port: 7}, discardLogger())

	if tr.repeatCount != 5 {
		t.Errorf("repeatCount = %d, want 5", tr.repeatCount)
	}
	if tr.repeatDelay != 250*time.Millisecond {
		t.Errorf("repeatDelay = %v, want 250ms", tr.repeatDelay)
	}
	if tr.port != 7 {
		t.Errorf("port = %d, want 7", tr.port)
	}
}

func TestNewFallsBackOnInvalidRepeatDelay(t *testing.T) {
	tr := New(Config{RepeatDelay: "not-a-duration"}, discardLogger())
	if tr.repeatDelay != defaultRepeatDelay {
		t.Errorf("repeatDelay = %v, want the default %v", tr.repeatDelay, defaultRepeatDelay)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
