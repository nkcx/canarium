package snmp

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/gosnmp/gosnmp"
	"github.com/nkcx/canarium/internal/engine"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestToInt(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  int
	}{
		{"int", 42, 42},
		{"float64 from json", float64(42), 42},
		{"string from env substitution", "42", 42},
		{"unparseable string", "not-a-number", 0},
		{"nil", nil, 0},
		{"bool", true, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toInt(tt.input); got != tt.want {
				t.Errorf("toInt(%v) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestGetConfigPortList(t *testing.T) {
	cfg := map[string]any{
		"ports": []any{
			map[string]any{"group": 1, "port": 5},
			map[string]any{"group": 1, "port": 6},
			map[string]any{"group": 0, "port": 7}, // group must be positive
			map[string]any{"port": 8},             // no group
			"not a map",
		},
	}

	ports := getConfigPortList(cfg, "ports")
	if len(ports) != 2 {
		t.Fatalf("parsed %d ports, want 2: %+v", len(ports), ports)
	}
	if ports[0].Group != 1 || ports[0].Port != 5 {
		t.Errorf("ports[0] = %+v, want {1 5}", ports[0])
	}
}

func TestGetConfigPortListOnMissingKey(t *testing.T) {
	if ports := getConfigPortList(nil, "ports"); ports != nil {
		t.Errorf("getConfigPortList(nil) = %+v, want nil", ports)
	}
	if ports := getConfigPortList(map[string]any{}, "ports"); ports != nil {
		t.Errorf("getConfigPortList({}) = %+v, want nil", ports)
	}
}

func TestNewGoSNMPVersions(t *testing.T) {
	tests := []struct {
		name    string
		version int
		want    gosnmp.SnmpVersion
	}{
		{"v1", 1, gosnmp.Version1},
		{"v2c", 2, gosnmp.Version2c},
		{"v3", 3, gosnmp.Version3},
		{"unset defaults to v2c", 0, gosnmp.Version2c},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := newGoSNMP("switch.lan", 161, tt.version, "public", "", "", "")
			if err != nil {
				t.Fatalf("newGoSNMP: %v", err)
			}
			if client.Version != tt.want {
				t.Errorf("Version = %v, want %v", client.Version, tt.want)
			}
		})
	}
}

func TestNewGoSNMPRequiresAHost(t *testing.T) {
	if _, err := newGoSNMP("", 161, 2, "public", "", "", ""); err == nil {
		t.Error("newGoSNMP accepted an empty host")
	}
}

func TestNewGoSNMPv3SecurityLevels(t *testing.T) {
	tests := []struct {
		name     string
		authPass string
		privPass string
		want     gosnmp.SnmpV3MsgFlags
	}{
		{"no credentials", "", "", gosnmp.NoAuthNoPriv},
		{"auth only", "authpass", "", gosnmp.AuthNoPriv},
		{"auth and priv", "authpass", "privpass", gosnmp.AuthPriv},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := newGoSNMP("switch.lan", 161, 3, "", "user", tt.authPass, tt.privPass)
			if err != nil {
				t.Fatalf("newGoSNMP: %v", err)
			}
			if client.MsgFlags != tt.want {
				t.Errorf("MsgFlags = %v, want %v", client.MsgFlags, tt.want)
			}
		})
	}
}

func TestRemapAction(t *testing.T) {
	tr := NewPoeTransport(discardLogger())

	tests := []struct {
		in, want engine.ActionType
	}{
		{engine.ActionShutdown, engine.ActionPoeOff},
		{engine.ActionWake, engine.ActionPoeOn},
		{engine.ActionProbe, engine.ActionProbe},
	}

	for _, tt := range tests {
		if got := tr.RemapAction(tt.in); got != tt.want {
			t.Errorf("RemapAction(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// TestProbeRequiresAnAddress is the regression test for probing the switch
// instead of the client: the old implementation dialled UDP to the switch's
// SNMP port and reported every client as up unconditionally.
func TestProbeRequiresAnAddress(t *testing.T) {
	tr := NewPoeTransport(discardLogger())

	state, err := tr.Probe(context.Background(), &engine.Client{Name: "ap"})
	if err == nil {
		t.Error("Probe succeeded with no client address; power state cannot be verified")
	}
	if state == engine.StateUp {
		t.Error("Probe reported a client with no address as up")
	}
}

func TestSetPoeStateRequiresPorts(t *testing.T) {
	tr := NewPoeTransport(discardLogger())

	_, err := tr.Execute(context.Background(), &engine.Client{
		Name:            "ap",
		Address:         "10.0.0.1",
		TransportConfig: map[string]any{},
	}, engine.ActionPoeOff)
	if err == nil {
		t.Error("Execute succeeded with no ports configured")
	}
}

func TestUnsupportedActionIsRejected(t *testing.T) {
	tr := NewPoeTransport(discardLogger())

	if _, err := tr.Execute(context.Background(),
		&engine.Client{Name: "ap"}, engine.ActionOutletOff); err == nil {
		t.Error("the snmp-poe transport accepted an outlet action")
	}
}

func TestDecodeSnmpValue(t *testing.T) {
	tests := []struct {
		name string
		pdu  gosnmp.SnmpPDU
		want any
	}{
		{"integer", gosnmp.SnmpPDU{Type: gosnmp.Integer, Value: 42}, int64(42)},
		{"octet string", gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: []byte("hello")}, "hello"},
		{"gauge", gosnmp.SnmpPDU{Type: gosnmp.Gauge32, Value: uint(7)}, int64(7)},
		{"no such object", gosnmp.SnmpPDU{Type: gosnmp.NoSuchObject}, nil},
		{"no such instance", gosnmp.SnmpPDU{Type: gosnmp.NoSuchInstance}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decodeSnmpValue(tt.pdu); got != tt.want {
				t.Errorf("decodeSnmpValue = %v (%T), want %v (%T)", got, got, tt.want, tt.want)
			}
		})
	}
}
