package wol

import (
	"net"
	"testing"
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
