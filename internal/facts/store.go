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
			continue
		}
		if now.Sub(updatedAt) > interval*2 {
			f.SetQuality(QualityStale)
		} else {
			f.SetQuality(QualityGood)
		}
	}
}

func (s *Store) FactValue(key string) any {
	val, q, _ := s.Get(key)
	if q == QualityUnknown {
		return nil
	}
	return val
}

func (s *Store) FactQuality(key string) string {
	_, q, _ := s.Get(key)
	return q.String()
}

func (s *Store) FactAge(key string) float64 {
	_, _, updatedAt := s.Get(key)
	if updatedAt.IsZero() {
		return -1
	}
	return time.Since(updatedAt).Seconds()
}

func sourceFromKey(key string) string {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) < 2 {
		return key
	}
	return parts[0]
}
