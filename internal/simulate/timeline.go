package simulate

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/nkcx/canarium/internal/config"
)

// Timeline is a scripted sequence of fact changes to replay against a plan.
type Timeline struct {
	// Duration is how long to simulate. Accepts a duration string ("45m")
	// or a number of seconds.
	Duration Duration `json:"duration"`

	// Step is how much simulated time passes per tick. Defaults to one
	// second; a shorter step costs proportionally more iterations.
	Step Duration `json:"step,omitempty"`

	Events []TimelineEvent `json:"events"`
}

// TimelineEvent sets a fact to a value at a point in the timeline.
type TimelineEvent struct {
	// At is when the change occurs, relative to the start.
	At Duration `json:"at"`

	// Fact is the fully qualified fact key, e.g. "rack_ups.battery.charge".
	Fact string `json:"fact"`

	// Value is the new value: a number, a string, a bool, or a list of
	// strings for a set-typed fact such as ups.status.
	//
	// An explicit null models the source going silent: the fact stops being
	// refreshed and goes stale on its normal schedule, which is how a dead
	// NUT server or a cut network link behaves.
	Value any `json:"value"`

	// explicitNull distinguishes "value": null from an omitted value.
	explicitNull bool
}

// UnmarshalJSON records whether a null value was written explicitly.
func (e *TimelineEvent) UnmarshalJSON(data []byte) error {
	type raw TimelineEvent // avoid recursing into this method

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}

	var decoded raw
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	*e = TimelineEvent(decoded)
	if v, present := fields["value"]; present && string(v) == "null" {
		e.explicitNull = true
	}
	return nil
}

// Duration is a time.Duration that accepts human-readable JSON.
//
// The previous type was a bare time.Duration, which json decodes only from
// an integer count of *nanoseconds*. A timeline written with "at": 300 —
// the obvious way to say five minutes in — was read as 300 nanoseconds, so
// every event fired at once on the first tick and the simulation was
// meaningless. Nothing documented the nanosecond requirement.
type Duration time.Duration

// UnmarshalJSON accepts "45m", "1h30m", or a number of seconds.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		parsed, err := config.ParseDuration(asString)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", asString, err)
		}
		*d = Duration(parsed)
		return nil
	}

	var asNumber float64
	if err := json.Unmarshal(data, &asNumber); err != nil {
		return fmt.Errorf("duration must be a string like \"5m\" or a number of seconds")
	}
	if asNumber < 0 {
		return fmt.Errorf("duration cannot be negative")
	}

	*d = Duration(time.Duration(asNumber * float64(time.Second)))
	return nil
}

// MarshalJSON writes the duration in its human-readable form.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// LoadTimeline reads and validates a timeline file.
func LoadTimeline(path string) (*Timeline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading timeline: %w", err)
	}

	dec := json.NewDecoder(newTrimReader(data))
	dec.DisallowUnknownFields()

	var tl Timeline
	if err := dec.Decode(&tl); err != nil {
		return nil, fmt.Errorf("parsing timeline: %w", err)
	}

	if err := tl.validate(); err != nil {
		return nil, err
	}

	sort.SliceStable(tl.Events, func(i, j int) bool {
		return tl.Events[i].At < tl.Events[j].At
	})

	return &tl, nil
}

func (t *Timeline) validate() error {
	if t.Duration <= 0 {
		return fmt.Errorf("timeline duration must be positive (got %s)", t.Duration.Duration())
	}

	if t.Step < 0 {
		return fmt.Errorf("timeline step cannot be negative")
	}
	if t.Step == 0 {
		t.Step = Duration(time.Second)
	}
	if t.Step.Duration() > t.Duration.Duration() {
		return fmt.Errorf("timeline step (%s) is longer than its duration (%s)",
			t.Step.Duration(), t.Duration.Duration())
	}

	if len(t.Events) == 0 {
		return fmt.Errorf("timeline has no events, so nothing would ever change")
	}

	for i, e := range t.Events {
		if e.Fact == "" {
			return fmt.Errorf("event %d has no fact key", i)
		}
		// The fact store keys staleness by the segment before the first dot.
		// A key without one previously caused a slice out of range panic in
		// Run, crashing the CLI.
		if _, _, ok := splitFactKey(e.Fact); !ok {
			return fmt.Errorf(
				"event %d: fact key %q must be qualified as <source>.<name>, "+
					"for example \"rack_ups.battery.charge\"", i, e.Fact)
		}
		if e.At < 0 {
			return fmt.Errorf("event %d: 'at' cannot be negative", i)
		}
		if e.At.Duration() > t.Duration.Duration() {
			return fmt.Errorf("event %d fires at %s, after the timeline ends at %s",
				i, e.At.Duration(), t.Duration.Duration())
		}
		// A null value is meaningful: it models the source going silent, so
		// the fact stops being refreshed and goes stale. A *missing* value
		// is a mistake.
		if e.Value == nil && !e.explicitNull {
			return fmt.Errorf("event %d (%s) has no value "+
				"(use null to model the source going silent)", i, e.Fact)
		}
	}

	return nil
}

// splitFactKey divides a fact key into its source instance and fact name.
func splitFactKey(key string) (source, name string, ok bool) {
	for i := 0; i < len(key); i++ {
		if key[i] == '.' {
			if i == 0 || i == len(key)-1 {
				return "", "", false
			}
			return key[:i], key[i+1:], true
		}
	}
	return "", "", false
}
