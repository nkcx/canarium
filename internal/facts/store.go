package facts

import (
	"strings"
	"sync"
	"time"
)

type Store struct {
	mu    sync.RWMutex
	facts map[string]*Fact
	decls map[string]*FactDeclaration
	polls map[string]time.Duration // source -> poll interval
}

func NewStore() *Store {
	return &Store{
		facts: make(map[string]*Fact),
		decls: make(map[string]*FactDeclaration),
		polls: make(map[string]time.Duration),
	}
}

func (s *Store) RegisterSource(source string, pollInterval time.Duration, declarations []FactDeclaration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.polls[source] = pollInterval
	for _, d := range declarations {
		key := source + "." + d.Name
		d := d
		s.decls[key] = &d
		if _, ok := s.facts[key]; !ok {
			s.facts[key] = &Fact{
				Key:     key,
				Type:    ParseFactType(d.Type),
				Quality: QualityUnknown,
				Source:  source,
			}
		}
	}
}

func (s *Store) Update(key string, value any, t time.Time) {
	s.mu.RLock()
	f, ok := s.facts[key]
	s.mu.RUnlock()
	if !ok {
		return
	}
	f.Set(value, t)
}

func (s *Store) Get(key string) (any, Quality, time.Time) {
	s.mu.RLock()
	f, ok := s.facts[key]
	s.mu.RUnlock()
	if !ok {
		return nil, QualityUnknown, time.Time{}
	}
	return f.Get()
}

func (s *Store) GetFact(key string) *Fact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.facts[key]
}

func (s *Store) GetDeclaration(key string) *FactDeclaration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.decls[key]
}

func (s *Store) AllFacts() map[string]*Fact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]*Fact, len(s.facts))
	for k, v := range s.facts {
		result[k] = v
	}
	return result
}

func (s *Store) AllDeclarations() map[string]*FactDeclaration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]*FactDeclaration, len(s.decls))
	for k, v := range s.decls {
		result[k] = v
	}
	return result
}

// stalenessFactor is how many poll intervals a fact may go without an update
// before it is considered stale. Two allows a single missed poll (a dropped
// packet, a brief reconnect) without flapping quality, while still noticing a
// genuinely dead source within a couple of cycles.
const stalenessFactor = 2

// minPollInterval floors the staleness window. A source that declares a zero
// or missing poll interval would otherwise be judged stale the instant after
// any update, permanently disabling every condition that reads it.
const minPollInterval = time.Second

// RefreshQuality re-evaluates every fact's freshness against its source's
// poll interval.
//
// This must run regardless of operating mode. It previously ran only from the
// policy loop, which returns early when disarmed, so a disarmed daemon
// reported every fact as "good" indefinitely on the dashboard no matter how
// long the source had been dead.
func (s *Store) RefreshQuality(now time.Time) {
	s.mu.RLock()
	factsCopy := make(map[string]*Fact, len(s.facts))
	for k, v := range s.facts {
		factsCopy[k] = v
	}
	s.mu.RUnlock()

	for key, f := range factsCopy {
		source := sourceFromKey(key)
		s.mu.RLock()
		interval, ok := s.polls[source]
		s.mu.RUnlock()
		if !ok {
			continue
		}

		_, q, updatedAt := f.Get()
		if q == QualityUnknown {
			// Never received a value; there is no freshness to assess.
			continue
		}

		if interval < minPollInterval {
			interval = minPollInterval
		}

		if now.Sub(updatedAt) > interval*stalenessFactor {
			f.SetQuality(QualityStale)
		} else {
			f.SetQuality(QualityGood)
		}
	}
}

// FactValue returns a fact's value for use in a template expression, or nil
// if the fact is not currently trustworthy.
//
// This deliberately matches the rule the structured evaluator applies: only
// QualityGood counts. A template and an equivalent structured condition must
// not disagree about whether a stale fact is usable.
func (s *Store) FactValue(key string) any {
	val, q, _ := s.Get(key)
	if q != QualityGood {
		return nil
	}
	return val
}

func (s *Store) FactQuality(key string) string {
	_, q, _ := s.Get(key)
	return q.String()
}

// FactAgeAt returns a fact's age in seconds as of the given instant, or -1
// if it has never reported.
//
// The instant is supplied rather than read from the clock so that simulation
// works. The simulator advances synthetic timestamps through a loop that
// takes microseconds of real time, so an age measured with time.Since() was
// always near zero there — making `age()` in a template condition useless in
// exactly the tool meant to test it.
func (s *Store) FactAgeAt(key string, now time.Time) float64 {
	_, _, updatedAt := s.Get(key)
	if updatedAt.IsZero() {
		return -1
	}

	age := now.Sub(updatedAt).Seconds()
	if age < 0 {
		return 0
	}
	return age
}

// FactAge returns a fact's age in seconds as of now.
func (s *Store) FactAge(key string) float64 {
	return s.FactAgeAt(key, time.Now())
}

func sourceFromKey(key string) string {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) < 2 {
		return key
	}
	return parts[0]
}
