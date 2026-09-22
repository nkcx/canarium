package nut

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/nkcx/canarium/internal/engine"
)

// br1500gVars is what usbhid-ups reports for an APC Back-UPS Pro BR1500G,
// the UPS on the deployment that surfaced this: it has a real-power rating
// and a load percentage, and -- like most Back-UPS models -- no
// instantaneous real-power reading, no output voltage and no temperature.
var br1500gVars = map[string]string{
	"battery.charge":          "100",
	"battery.charge.low":      "10",
	"battery.charge.warning":  "50",
	"battery.runtime":         "1464",
	"battery.runtime.low":     "120",
	"battery.voltage":         "27.0",
	"battery.voltage.nominal": "24.0",
	"device.mfr":              "American Power Conversion",
	"device.model":            "Back-UPS RS 1500G",
	"device.serial":           "3B1234X56789",
	"input.transfer.high":     "144",
	"input.transfer.low":      "88",
	"input.voltage":           "120.0",
	"input.voltage.nominal":   "120",
	"ups.load":                "76",
	"ups.realpower.nominal":   "865",
	"ups.status":              "OL CHRG",
	"ups.test.result":         "No test initiated",
	"ups.beeper.status":       "enabled",
}

// fakeNUT serves just enough of the NUT protocol for a poll: LIST VAR and
// LOGOUT, without authentication.
func fakeNUT(t *testing.T, ups string, vars map[string]string) (host string, port int) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					switch strings.TrimSpace(line) {
					case "LIST VAR " + ups:
						var b strings.Builder
						b.WriteString("BEGIN LIST VAR " + ups + "\n")
						for k, v := range vars {
							b.WriteString("VAR " + ups + " " + k + ` "` + v + "\"\n")
						}
						b.WriteString("END LIST VAR " + ups + "\n")
						_, _ = io.WriteString(c, b.String())
					case "LOGOUT":
						_, _ = io.WriteString(c, "OK Goodbye\n")
						return
					default:
						_, _ = io.WriteString(c, "ERR UNKNOWN-COMMAND\n")
					}
				}
			}(conn)
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func pollOnce(t *testing.T, vars map[string]string) map[string]any {
	t.Helper()

	host, port := fakeNUT(t, "ups", vars)
	inst := InstanceConfig{Name: "rack_ups", Host: host, Port: port, UPS: "ups"}
	src := NewSource(Config{Instances: []InstanceConfig{inst}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	updates := make(chan engine.FactUpdate, 100)
	src.fetchAndUpdate(t.Context(), inst, updates)
	close(updates)

	got := make(map[string]any)
	for u := range updates {
		got[u.Key] = u.Value
	}
	return got
}

// TestPollPublishesEveryStandardVariable is the reason this table exists.
// Every poll fetched the complete variable list and then kept eight, so
// the BR1500G's real-power rating -- the basis of the output watts its
// front panel shows -- never reached Canarium.
func TestPollPublishesEveryStandardVariable(t *testing.T) {
	got := pollOnce(t, br1500gVars)

	want := map[string]any{
		"rack_ups.battery.charge":        100.0,
		"rack_ups.battery.runtime":       1464.0,
		"rack_ups.ups.load":              76.0,
		"rack_ups.ups.realpower.nominal": 865.0,
		"rack_ups.input.transfer.low":    88.0,
		"rack_ups.battery.charge.low":    10.0,
		"rack_ups.device.model":          "Back-UPS RS 1500G",
		"rack_ups.ups.test.result":       "No test initiated",
	}
	for key, w := range want {
		if got[key] != w {
			t.Errorf("%s = %#v, want %#v", key, got[key], w)
		}
	}

	status, ok := got["rack_ups.status"].([]string)
	if !ok || strings.Join(status, " ") != "OL CHRG" {
		t.Errorf("status = %#v, want [OL CHRG]", got["rack_ups.status"])
	}
}

// TestPollDoesNotPublishNonStandardVariables: the serial number is in the
// variable list, and the facts end up in the API, the audit journal and
// webhook payloads. Only the standard, operational set is published.
func TestPollDoesNotPublishNonStandardVariables(t *testing.T) {
	got := pollOnce(t, br1500gVars)

	for _, key := range []string{"rack_ups.device.serial", "rack_ups.ups.beeper.status"} {
		if _, ok := got[key]; ok {
			t.Errorf("%s was published; it is not in the standard set", key)
		}
	}
}

func TestPollSkipsAMalformedNumber(t *testing.T) {
	vars := map[string]string{"battery.charge": "full", "ups.load": "12"}
	got := pollOnce(t, vars)

	if _, ok := got["rack_ups.battery.charge"]; ok {
		t.Error("a non-numeric charge was published")
	}
	if got["rack_ups.ups.load"] != 12.0 {
		t.Errorf("one bad variable stopped the rest: ups.load = %#v", got["rack_ups.ups.load"])
	}
}

// TestDeclarationsMatchWhatPollPublishes: the two used to be separate
// hand-kept lists. Anything published must be declared, or the store has
// no type or unit for it and the UI cannot render it properly.
func TestDeclarationsMatchWhatPollPublishes(t *testing.T) {
	src := NewSource(Config{Instances: []InstanceConfig{{Name: "rack_ups"}}}, nil)
	declared := make(map[string]engine.FactDeclEntry)
	for _, d := range src.Declarations() {
		for _, f := range d.Facts {
			declared["rack_ups."+f.Name] = f
		}
	}

	for key := range pollOnce(t, br1500gVars) {
		if _, ok := declared[key]; !ok {
			t.Errorf("%s is published but not declared", key)
		}
	}

	if d := declared["rack_ups.ups.realpower.nominal"]; d.Unit != "watts" {
		t.Errorf("realpower.nominal unit = %q, want watts", d.Unit)
	}
	for _, flag := range []string{"OL", "OB", "LB", "CHRG", "FSD"} {
		found := false
		for _, v := range declared["rack_ups.status"].Values {
			if v == flag {
				found = true
			}
		}
		if !found {
			t.Errorf("status does not declare the %s flag", flag)
		}
	}
}
