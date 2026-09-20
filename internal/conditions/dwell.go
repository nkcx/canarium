package conditions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
)

// DwellTracker records progress toward a condition's `for:` requirement.
//
// Elapsed time is measured from the timestamp the caller supplies, never from
// a fresh clock reading. That matters twice over:
//
//   - In production the caller passes time.Now(), whose values carry Go's
//     monotonic clock reading, so Sub() is immune to wall-clock steps. The
//     previous implementation called time.Now().UnixNano() and subtracted,
//     which is pure wall clock: an NTP step forward could instantly satisfy a
//     five-minute dwell, and a step backward could block one indefinitely.
//     Canarium's documented target is a Raspberry Pi, which has no RTC and
//     therefore takes a large step on every boot.
//
//   - In simulation the caller passes synthetic timestamps derived by
//     Add()-ing to a base time, which also carry a monotonic reading. Dwell
//     therefore advances with simulated time. Under the old code every `for:`
//     condition was unreachable in simulation, because real elapsed time in
//     the simulator's tight loop is microseconds.
type DwellTracker struct {
	// Required is the configured dwell duration.
	Required time.Duration

	// StartedAt is when the condition most recently became true. Zero when
	// the condition is not currently true.
	StartedAt time.Time

	// Credited is dwell time carried over from before a restart. Time spent
	// while the daemon was down is never credited.
	Credited time.Duration

	// Satisfied is true once Required has been met.
	Satisfied bool

	// LastSeen is when this tracker was last evaluated, used to expire
	// trackers for conditions that no longer exist in the config.
	LastSeen time.Time

	// lastPersisted is when progress was last written through to the dwell
	// store, used to throttle writes.
	lastPersisted time.Time
}

// Elapsed returns total dwell credit as of now.
func (d *DwellTracker) Elapsed(now time.Time) time.Duration {
	if d.StartedAt.IsZero() {
		return d.Credited
	}
	since := now.Sub(d.StartedAt)
	if since < 0 {
		// Defensive: only reachable if the caller supplies timestamps
		// without a monotonic reading and the wall clock moved backwards.
		since = 0
	}
	return d.Credited + since
}

// DwellRecord is the persisted form of a tracker.
type DwellRecord struct {
	RequiredNS int64
	ElapsedNS  int64
	Satisfied  bool
	LastSeen   time.Time
}

// DwellStore persists dwell progress so timers survive a restart.
//
// Without it, a daemon that restarts thirty seconds into a five-minute wake
// gate silently starts the five minutes again.
type DwellStore interface {
	LoadDwellTrackers(ctx context.Context) (map[string]DwellRecord, error)
	SaveDwellTracker(ctx context.Context, key string, rec DwellRecord) error
	DeleteDwellTracker(ctx context.Context, key string) error
}

// SetDwellStore attaches a persistence backend and restores saved progress.
//
// Restored credit is carried forward, but the interval during which the
// daemon was not running is not: on resume a tracker holds exactly the credit
// it had earned before shutdown, and accrues again only once the condition is
// observed true. SPEC §5.3 calls for crediting only elapsed time the daemon
// can prove.
func (e *Evaluator) SetDwellStore(ctx context.Context, store DwellStore) error {
	records, err := store.LoadDwellTrackers(ctx)
	if err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.dwellStore = store
	for key, rec := range records {
		e.dwellState[key] = &DwellTracker{
			Required:  time.Duration(rec.RequiredNS),
			Credited:  time.Duration(rec.ElapsedNS),
			Satisfied: rec.Satisfied,
			LastSeen:  rec.LastSeen,
			// StartedAt is left zero: the condition must be observed true
			// again before the clock restarts.
		}
	}

	return nil
}

// applyDwell folds a condition's instantaneous result into its dwell timer.
//
// Returns True only once the condition has held true continuously for the
// configured duration. Any non-true result resets the timer: dwell means
// "continuously true for", not "true for a cumulative total of".
func (e *Evaluator) applyDwell(
	cond *config.ConditionConfig,
	current facts.Trilean,
	required time.Duration,
	now time.Time,
) facts.Trilean {
	key := conditionKey(cond)

	e.mu.Lock()
	defer e.mu.Unlock()

	tracker, ok := e.dwellState[key]
	if !ok {
		tracker = &DwellTracker{Required: required}
		e.dwellState[key] = tracker
	}
	tracker.Required = required
	tracker.LastSeen = now

	if current != facts.True {
		changed := !tracker.StartedAt.IsZero() || tracker.Credited != 0 || tracker.Satisfied
		tracker.StartedAt = time.Time{}
		tracker.Credited = 0
		tracker.Satisfied = false

		if changed {
			e.persistLocked(key, tracker)
		}

		// Unavailable must stay unavailable: "we don't know" is not the same
		// as "the dwell requirement is not met", and callers distinguish them.
		if current == facts.Unavailable {
			return facts.Unavailable
		}
		return facts.False
	}

	if tracker.StartedAt.IsZero() {
		tracker.StartedAt = now
	}

	elapsed := tracker.Elapsed(now)
	satisfied := elapsed >= required

	if satisfied != tracker.Satisfied {
		tracker.Satisfied = satisfied
		e.persistLocked(key, tracker)
	} else {
		e.persistThrottledLocked(key, tracker, now)
	}

	if satisfied {
		return facts.True
	}
	return facts.False
}

// persistLocked writes a tracker through to the store. Caller holds e.mu.
func (e *Evaluator) persistLocked(key string, tracker *DwellTracker) {
	if e.dwellStore == nil {
		return
	}

	rec := DwellRecord{
		RequiredNS: int64(tracker.Required),
		ElapsedNS:  int64(tracker.Elapsed(tracker.lastPersistBasis())),
		Satisfied:  tracker.Satisfied,
		LastSeen:   tracker.LastSeen,
	}
	if err := e.dwellStore.SaveDwellTracker(context.Background(), key, rec); err != nil && e.onPersistError != nil {
		e.onPersistError(key, err)
	}
	tracker.lastPersisted = tracker.LastSeen
}

// persistThrottledLocked writes progress periodically rather than on every
// tick. Caller holds e.mu.
//
// Dwell is re-evaluated every five seconds per condition. Writing each time
// would put a continuous write load on the SD card this is expected to run
// from, for a value whose only consumer is a restart.
func (e *Evaluator) persistThrottledLocked(key string, tracker *DwellTracker, now time.Time) {
	if e.dwellStore == nil || tracker.StartedAt.IsZero() {
		return
	}
	if !tracker.lastPersisted.IsZero() && now.Sub(tracker.lastPersisted) < dwellPersistInterval {
		return
	}
	e.persistLocked(key, tracker)
}

// dwellPersistInterval is how often in-progress dwell credit is written
// through to storage.
const dwellPersistInterval = 30 * time.Second

func (d *DwellTracker) lastPersistBasis() time.Time {
	if d.LastSeen.IsZero() {
		return d.StartedAt
	}
	return d.LastSeen
}

// ResetDwell clears a condition's dwell progress.
func (e *Evaluator) ResetDwell(cond *config.ConditionConfig) {
	key := conditionKey(cond)

	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.dwellState, key)
	if e.dwellStore != nil {
		if err := e.dwellStore.DeleteDwellTracker(context.Background(), key); err != nil && e.onPersistError != nil {
			e.onPersistError(key, err)
		}
	}
}

// DwellProgress reports a snapshot of every tracked dwell timer, for the
// status API and diagnostics.
func (e *Evaluator) DwellProgress(now time.Time) map[string]DwellSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()

	out := make(map[string]DwellSnapshot, len(e.dwellState))
	for key, t := range e.dwellState {
		out[key] = DwellSnapshot{
			Required:  t.Required,
			Elapsed:   t.Elapsed(now),
			Satisfied: t.Satisfied,
			Active:    !t.StartedAt.IsZero(),
		}
	}
	return out
}

// DwellSnapshot is an immutable view of one dwell timer.
type DwellSnapshot struct {
	Required  time.Duration `json:"required"`
	Elapsed   time.Duration `json:"elapsed"`
	Satisfied bool          `json:"satisfied"`
	Active    bool          `json:"active"`
}

// conditionKey derives a stable identity for a condition.
//
// The previous implementation concatenated a handful of fields and omitted
// is_not, in, equals and — critically — nested conditions. Two different
// compound conditions both written as `{condition: and, for: 5m, ...}`
// collapsed to the identical key "and|for:5m" and shared one dwell timer, so
// two plans' triggers silently interfered with each other.
//
// Hashing the complete serialised condition makes collisions
// cryptographically improbable and automatically covers fields added later.
// json.Marshal emits struct fields in declaration order, so the encoding is
// deterministic.
func conditionKey(cond *config.ConditionConfig) string {
	encoded, err := json.Marshal(cond)
	if err != nil {
		// ConditionConfig contains only JSON-encodable types; this cannot
		// fail in practice. Fall back to a value rendering rather than
		// panicking in the middle of policy evaluation.
		encoded = []byte(fmt.Sprintf("%#v", cond))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
