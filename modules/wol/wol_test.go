package wol

import (
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
