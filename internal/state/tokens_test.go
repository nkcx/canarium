package state

import (
	"errors"
	"testing"
)

func TestAPITokenRoundTrip(t *testing.T) {
	db := newTestDB(t)

	if err := db.SaveAPIToken(t.Context(), "digest-1", "monitoring", ScopeRead); err != nil {
		t.Fatalf("SaveAPIToken: %v", err)
	}

	scope, err := db.ValidateAPIToken(t.Context(), "digest-1")
	if err != nil {
		t.Fatalf("ValidateAPIToken: %v", err)
	}
	if scope != ScopeRead {
		t.Errorf("scope = %q, want %q", scope, ScopeRead)
	}
}

func TestUnknownTokenHasNoScope(t *testing.T) {
	db := newTestDB(t)

	scope, err := db.ValidateAPIToken(t.Context(), "never-issued")
	if err != nil {
		t.Fatalf("ValidateAPIToken: %v", err)
	}
	if scope != "" {
		t.Errorf("scope = %q for an unknown token, want empty", scope)
	}
}

func TestDuplicateTokenNameIsRejected(t *testing.T) {
	db := newTestDB(t)

	if err := db.SaveAPIToken(t.Context(), "digest-1", "monitoring", ScopeRead); err != nil {
		t.Fatalf("first SaveAPIToken: %v", err)
	}

	err := db.SaveAPIToken(t.Context(), "digest-2", "monitoring", ScopeAdmin)
	if !errors.Is(err, ErrTokenExists) {
		t.Errorf("second SaveAPIToken with the same name = %v, want ErrTokenExists", err)
	}
}

func TestListAPITokens(t *testing.T) {
	db := newTestDB(t)

	if err := db.SaveAPIToken(t.Context(), "d1", "monitoring", ScopeRead); err != nil {
		t.Fatalf("SaveAPIToken: %v", err)
	}
	if err := db.SaveAPIToken(t.Context(), "d2", "automation", ScopeAdmin); err != nil {
		t.Fatalf("SaveAPIToken: %v", err)
	}

	tokens, err := db.ListAPITokens(t.Context())
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("listed %d tokens, want 2", len(tokens))
	}

	for _, tok := range tokens {
		if tok.CreatedAt.IsZero() {
			t.Errorf("token %q has no creation time", tok.Name)
		}
		if tok.LastUsed != nil {
			t.Errorf("token %q reports a last-used time before ever being used", tok.Name)
		}
	}
}

// TestValidateRecordsLastUsed lets an operator tell which tokens are live
// before revoking one.
func TestValidateRecordsLastUsed(t *testing.T) {
	db := newTestDB(t)

	if err := db.SaveAPIToken(t.Context(), "d1", "monitoring", ScopeRead); err != nil {
		t.Fatalf("SaveAPIToken: %v", err)
	}
	if _, err := db.ValidateAPIToken(t.Context(), "d1"); err != nil {
		t.Fatalf("ValidateAPIToken: %v", err)
	}

	tokens, err := db.ListAPITokens(t.Context())
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("listed %d tokens, want 1", len(tokens))
	}
	if tokens[0].LastUsed == nil {
		t.Error("using a token did not record a last-used time")
	}
}

func TestDeleteAPIToken(t *testing.T) {
	db := newTestDB(t)

	if err := db.SaveAPIToken(t.Context(), "d1", "monitoring", ScopeRead); err != nil {
		t.Fatalf("SaveAPIToken: %v", err)
	}

	removed, err := db.DeleteAPIToken(t.Context(), "monitoring")
	if err != nil {
		t.Fatalf("DeleteAPIToken: %v", err)
	}
	if !removed {
		t.Error("DeleteAPIToken reported no rows removed")
	}

	scope, err := db.ValidateAPIToken(t.Context(), "d1")
	if err != nil {
		t.Fatalf("ValidateAPIToken: %v", err)
	}
	if scope != "" {
		t.Error("a revoked token still validates")
	}
}

func TestDeleteUnknownTokenReportsFalse(t *testing.T) {
	db := newTestDB(t)

	removed, err := db.DeleteAPIToken(t.Context(), "never-existed")
	if err != nil {
		t.Fatalf("DeleteAPIToken: %v", err)
	}
	if removed {
		t.Error("DeleteAPIToken reported removing a token that never existed")
	}
}
