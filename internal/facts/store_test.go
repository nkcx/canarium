package facts

import (
	"testing"
	"time"
)

func TestStoreRegisterAndUpdate(t *testing.T) {
	store := NewStore()
	store.RegisterSource("test", 10*time.Second, []FactDeclaration{
		{Name: "value", Type: "number"},
		{Name: "status", Type: "enum", Values: []string{"A", "B"}},
	})

	val, q, _ := store.Get("test.value")
	if q != QualityUnknown {
		t.Errorf("initial quality = %s, want unknown", q)
	}
	if val != nil {
		t.Errorf("initial value = %v, want nil", val)
	}

	now := time.Now()
	store.Update("test.value", 42.0, now)
	val, q, ts := store.Get("test.value")
	if q != QualityGood {
		t.Errorf("after update quality = %s, want good", q)
	}
	if val != 42.0 {
		t.Errorf("after update value = %v, want 42.0", val)
	}
	if !ts.Equal(now) {
		t.Errorf("after update timestamp mismatch")
	}
}

func TestStoreRefreshQuality(t *testing.T) {
	store := NewStore()
	store.RegisterSource("test", 5*time.Second, []FactDeclaration{
		{Name: "val", Type: "number"},
	})

	past := time.Now().Add(-20 * time.Second)
	store.Update("test.val", 1.0, past)

	store.RefreshQuality(time.Now())

	_, q, _ := store.Get("test.val")
	if q != QualityStale {
		t.Errorf("quality = %s, want stale (updated 20s ago with 5s poll)", q)
	}
}

func TestStoreFactValueNil(t *testing.T) {
	store := NewStore()
	store.RegisterSource("test", 10*time.Second, []FactDeclaration{
		{Name: "x", Type: "number"},
	})

	val := store.FactValue("test.x")
	if val != nil {
		t.Errorf("FactValue for unknown quality = %v, want nil", val)
	}
}

func TestStoreFactAge(t *testing.T) {
	store := NewStore()
	store.RegisterSource("test", 10*time.Second, []FactDeclaration{
		{Name: "x", Type: "number"},
	})

	age := store.FactAge("test.x")
	if age != -1 {
		t.Errorf("FactAge for never-updated = %f, want -1", age)
	}

	store.Update("test.x", 1.0, time.Now().Add(-5*time.Second))
	age = store.FactAge("test.x")
	if age < 4 || age > 6 {
		t.Errorf("FactAge = %f, want ~5", age)
	}
}

func TestStoreAllFacts(t *testing.T) {
	store := NewStore()
	store.RegisterSource("a", 10*time.Second, []FactDeclaration{
		{Name: "x", Type: "number"},
	})
	store.RegisterSource("b", 10*time.Second, []FactDeclaration{
		{Name: "y", Type: "string"},
	})

	all := store.AllFacts()
	if len(all) != 2 {
		t.Errorf("AllFacts count = %d, want 2", len(all))
	}
	if _, ok := all["a.x"]; !ok {
		t.Error("missing a.x")
	}
	if _, ok := all["b.y"]; !ok {
		t.Error("missing b.y")
	}
}

func TestStoreUpdateNonexistent(t *testing.T) {
	store := NewStore()
	store.Update("nonexistent.key", 1.0, time.Now())

	val, q, _ := store.Get("nonexistent.key")
	if val != nil || q != QualityUnknown {
		t.Error("updating nonexistent key should be a no-op")
	}
}
