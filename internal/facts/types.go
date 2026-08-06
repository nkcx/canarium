package facts

import (
	"sync"
	"time"
)

type Quality int

const (
	QualityUnknown Quality = iota
	QualityStale
	QualityGood
)

func (q Quality) String() string {
	switch q {
	case QualityGood:
		return "good"
	case QualityStale:
		return "stale"
	default:
		return "unknown"
	}
}

type FactType int

const (
	TypeNumber FactType = iota
	TypePercent
	TypeDuration
	TypeEnum
	TypeSet
	TypeBool
	TypeString
)

func (t FactType) String() string {
	switch t {
	case TypeNumber:
		return "number"
	case TypePercent:
		return "percent"
	case TypeDuration:
		return "duration"
	case TypeEnum:
		return "enum"
	case TypeSet:
		return "set"
	case TypeBool:
		return "bool"
	case TypeString:
		return "string"
	default:
		return "unknown"
	}
}

func ParseFactType(s string) FactType {
	switch s {
	case "number":
		return TypeNumber
	case "percent":
		return TypePercent
	case "duration":
		return TypeDuration
	case "enum":
		return TypeEnum
	case "set":
		return TypeSet
	case "bool":
		return TypeBool
	case "string":
		return TypeString
	default:
		return TypeString
	}
}

type FactDeclaration struct {
	Name        string   `yaml:"name"`
	Type        string   `yaml:"type"`
	Range       []float64 `yaml:"range,omitempty"`
	Values      []string `yaml:"values,omitempty"`
	Description string   `yaml:"description,omitempty"`
	Unit        string   `yaml:"unit,omitempty"`
}

type Fact struct {
	mu        sync.RWMutex
	Key       string
	Value     any
	Type      FactType
	Quality   Quality
	UpdatedAt time.Time
	Source    string
}

func (f *Fact) Get() (any, Quality, time.Time) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.Value, f.Quality, f.UpdatedAt
}

func (f *Fact) Set(value any, t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Value = value
	f.UpdatedAt = t
	f.Quality = QualityGood
}

func (f *Fact) SetQuality(q Quality) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Quality = q
}

type Trilean int

const (
	Unavailable Trilean = iota
	False
	True
)

func (t Trilean) String() string {
	switch t {
	case True:
		return "true"
	case False:
		return "false"
	default:
		return "unavailable"
	}
}

func And(a, b Trilean) Trilean {
	if a == False || b == False {
		return False
	}
	if a == Unavailable || b == Unavailable {
		return Unavailable
	}
	return True
}

func Or(a, b Trilean) Trilean {
	if a == True || b == True {
		return True
	}
	if a == Unavailable || b == Unavailable {
		return Unavailable
	}
	return False
}

func Not(a Trilean) Trilean {
	switch a {
	case True:
		return False
	case False:
		return True
	default:
		return Unavailable
	}
}

func BoolToTrilean(b bool) Trilean {
	if b {
		return True
	}
	return False
}
