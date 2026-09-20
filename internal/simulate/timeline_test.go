package simulate

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDurationAcceptsStrings(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{`"30s"`, 30 * time.Second},
		{`"5m"`, 5 * time.Minute},
		{`"1h30m"`, 90 * time.Minute},
		{`"2d"`, 48 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			var d Duration
			if err := json.Unmarshal([]byte(tt.input), &d); err != nil {
				t.Fatalf("Unmarshal(%s): %v", tt.input, err)
			}
			if d.Duration() != tt.want {
				t.Errorf("= %v, want %v", d.Duration(), tt.want)
			}
		})
	}
}

// TestDurationNumberMeansSeconds is the regression test for the previous
// bare time.Duration field, which json decodes only from nanoseconds. A
// timeline written "at": 300 — the obvious way to say five minutes — was
// read as 300 nanoseconds, so every event fired on the first tick.
func TestDurationNumberMeansSeconds(t *testing.T) {
	var d Duration
	if err := json.Unmarshal([]byte(`300`), &d); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if d.Duration() != 5*time.Minute {
		t.Errorf("300 decoded as %v, want 5m (seconds, not nanoseconds)", d.Duration())
	}
}

func TestDurationRejectsGarbage(t *testing.T) {
	for _, input := range []string{`"not-a-duration"`, `true`, `{}`, `-5`} {
		var d Duration
		if err := json.Unmarshal([]byte(input), &d); err == nil {
			t.Errorf("Unmarshal(%s) succeeded, got %v", input, d.Duration())
		}
	}
}

func TestDurationRoundTrips(t *testing.T) {
	original := Duration(90 * time.Minute)

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded Duration
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal(%s): %v", encoded, err)
	}
	if decoded != original {
		t.Errorf("round trip = %v, want %v", decoded.Duration(), original.Duration())
	}
}

// TestUnqualifiedFactKeyIsRejected is the regression test for a crash: Run
// computed the instance name by slicing the key at its first dot and a key
// without one panicked with a slice-out-of-range.
func TestUnqualifiedFactKeyIsRejected(t *testing.T) {
	path := writeTimeline(t, `{
	  "duration": "1m",
	  "events": [{"at": "0s", "fact": "battery", "value": 50}]
	}`)

	_, err := LoadTimeline(path)
	if err == nil {
		t.Fatal("an unqualified fact key was accepted; Run would have panicked")
	}
	if !strings.Contains(err.Error(), "source") {
		t.Errorf("error does not explain the required format: %v", err)
	}
}

func TestTimelineValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"no duration", `{"events":[{"at":"0s","fact":"a.b","value":1}]}`, "duration"},
		{"no events", `{"duration":"1m","events":[]}`, "no events"},
		{"event after end", `{"duration":"1m","events":[{"at":"5m","fact":"a.b","value":1}]}`, "after the timeline ends"},
		{"negative at", `{"duration":"1m","events":[{"at":-5,"fact":"a.b","value":1}]}`, "negative"},
		{"no fact", `{"duration":"1m","events":[{"at":"0s","value":1}]}`, "fact"},
		{"no value", `{"duration":"1m","events":[{"at":"0s","fact":"a.b"}]}`, "value"},
		{"step longer than duration", `{"duration":"1m","step":"5m","events":[{"at":"0s","fact":"a.b","value":1}]}`, "longer than"},
		{"unknown field", `{"duration":"1m","bogus":1,"events":[{"at":"0s","fact":"a.b","value":1}]}`, "bogus"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadTimeline(writeTimeline(t, tt.body))
			if err == nil {
				t.Fatalf("accepted an invalid timeline")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestTimelineEventsAreSorted(t *testing.T) {
	path := writeTimeline(t, `{
	  "duration": "10m",
	  "events": [
	    {"at": "5m", "fact": "a.b", "value": 2},
	    {"at": "1m", "fact": "a.b", "value": 1},
	    {"at": "3m", "fact": "a.b", "value": 3}
	  ]
	}`)

	tl, err := LoadTimeline(path)
	if err != nil {
		t.Fatalf("LoadTimeline: %v", err)
	}

	for i := 1; i < len(tl.Events); i++ {
		if tl.Events[i-1].At > tl.Events[i].At {
			t.Fatalf("events are not sorted: %v", tl.Events)
		}
	}
}

func TestTimelineDefaultsStepToOneSecond(t *testing.T) {
	path := writeTimeline(t, `{
	  "duration": "10m",
	  "events": [{"at": "0s", "fact": "a.b", "value": 1}]
	}`)

	tl, err := LoadTimeline(path)
	if err != nil {
		t.Fatalf("LoadTimeline: %v", err)
	}
	if tl.Step.Duration() != time.Second {
		t.Errorf("default step = %v, want 1s", tl.Step.Duration())
	}
}

func TestSplitFactKey(t *testing.T) {
	tests := []struct {
		key    string
		source string
		name   string
		ok     bool
	}{
		{"ups.status", "ups", "status", true},
		{"rack_ups.battery.charge", "rack_ups", "battery.charge", true},
		{"battery", "", "", false},
		{".leading", "", "", false},
		{"trailing.", "", "", false},
		{"", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			source, name, ok := splitFactKey(tt.key)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && (source != tt.source || name != tt.name) {
				t.Errorf("= (%q, %q), want (%q, %q)", source, name, tt.source, tt.name)
			}
		})
	}
}

func TestFactTypeOf(t *testing.T) {
	tests := []struct {
		value any
		want  string
	}{
		{100.0, "number"},
		{42, "number"},
		{true, "bool"},
		{"OB", "string"},
		{[]any{"OB", "LB"}, "set"},
		{[]string{"OB"}, "set"},
	}

	for _, tt := range tests {
		if got := factTypeOf(tt.value); got != tt.want {
			t.Errorf("factTypeOf(%v) = %q, want %q", tt.value, got, tt.want)
		}
	}
}
