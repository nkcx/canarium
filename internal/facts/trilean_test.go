package facts

import "testing"

func TestTrileanAnd(t *testing.T) {
	tests := []struct {
		a, b Trilean
		want Trilean
	}{
		{True, True, True},
		{True, False, False},
		{True, Unavailable, Unavailable},
		{False, True, False},
		{False, False, False},
		{False, Unavailable, False},
		{Unavailable, True, Unavailable},
		{Unavailable, False, False},
		{Unavailable, Unavailable, Unavailable},
	}

	for _, tt := range tests {
		got := And(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("And(%s, %s) = %s, want %s", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestTrileanOr(t *testing.T) {
	tests := []struct {
		a, b Trilean
		want Trilean
	}{
		{True, True, True},
		{True, False, True},
		{True, Unavailable, True},
		{False, True, True},
		{False, False, False},
		{False, Unavailable, Unavailable},
		{Unavailable, True, True},
		{Unavailable, False, Unavailable},
		{Unavailable, Unavailable, Unavailable},
	}

	for _, tt := range tests {
		got := Or(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("Or(%s, %s) = %s, want %s", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestTrileanNot(t *testing.T) {
	tests := []struct {
		a    Trilean
		want Trilean
	}{
		{True, False},
		{False, True},
		{Unavailable, Unavailable},
	}

	for _, tt := range tests {
		got := Not(tt.a)
		if got != tt.want {
			t.Errorf("Not(%s) = %s, want %s", tt.a, got, tt.want)
		}
	}
}
