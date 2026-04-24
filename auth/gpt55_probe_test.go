package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/codex2api/cache"
	"github.com/codex2api/database"
)

func newGPT55TestStore(t *testing.T) (*Store, *database.DB, *Account) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := database.New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("create sqlite db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	ctx := context.Background()
	id, err := db.InsertATAccount(ctx, "gpt55-test", "at-token", "")
	if err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if err := db.UpdateCredentials(ctx, id, map[string]interface{}{
		"email":      "gpt55@example.com",
		"account_id": "acct_gpt55",
		"expires_at": time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}

	store := NewStore(db, cache.NewMemory(1), nil)
	if err := store.loadFromDB(ctx); err != nil {
		t.Fatalf("loadFromDB: %v", err)
	}

	account := store.FindByID(id)
	if account == nil {
		t.Fatal("expected account in runtime store")
	}
	return store, db, account
}

func TestStoreLoadFromDBLoadsGPT55Capability(t *testing.T) {
	store, db, account := newGPT55TestStore(t)
	_ = store
	checkedAt := time.Now().UTC().Truncate(time.Second)

	if err := db.UpdateCredentials(context.Background(), account.ID(), map[string]interface{}{
		"supports_gpt_5_5":   true,
		"gpt_5_5_checked_at": checkedAt.Format(time.RFC3339),
		"gpt_5_5_last_error": "old error",
	}); err != nil {
		t.Fatalf("update credentials: %v", err)
	}

	reloaded := NewStore(db, cache.NewMemory(1), nil)
	if err := reloaded.loadFromDB(context.Background()); err != nil {
		t.Fatalf("reload from db: %v", err)
	}

	got := reloaded.FindByID(account.ID())
	if got == nil {
		t.Fatal("expected reloaded account")
	}
	if !got.SupportsGPT55 {
		t.Fatal("expected SupportsGPT55 to be true")
	}
	if !got.GPT55CheckedAt.Equal(checkedAt) {
		t.Fatalf("GPT55CheckedAt = %s, want %s", got.GPT55CheckedAt.Format(time.RFC3339), checkedAt.Format(time.RFC3339))
	}
	if got.GPT55LastError != "old error" {
		t.Fatalf("GPT55LastError = %q, want %q", got.GPT55LastError, "old error")
	}
}

func TestStoreProbeGPT55AccountPersistsProbeResults(t *testing.T) {
	tests := []struct {
		name             string
		result           GPT55ProbeResult
		wantSupported    bool
		wantChecked      bool
		wantLastError    string
		expectSupportKey bool
		expectCheckedKey bool
	}{
		{
			name:             "supported",
			result:           GPT55ProbeResult{Outcome: GPT55ProbeOutcomeSupported},
			wantSupported:    true,
			wantChecked:      true,
			expectSupportKey: true,
			expectCheckedKey: true,
		},
		{
			name:             "unsupported",
			result:           GPT55ProbeResult{Outcome: GPT55ProbeOutcomeUnsupported, LastError: "The model `gpt-5.5` does not exist or you do not have access to it."},
			wantSupported:    false,
			wantChecked:      true,
			wantLastError:    "The model `gpt-5.5` does not exist or you do not have access to it.",
			expectSupportKey: true,
			expectCheckedKey: true,
		},
		{
			name:             "failed",
			result:           GPT55ProbeResult{Outcome: GPT55ProbeOutcomeFailed, LastError: "upstream timeout"},
			wantSupported:    false,
			wantChecked:      false,
			wantLastError:    "upstream timeout",
			expectSupportKey: false,
			expectCheckedKey: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, db, account := newGPT55TestStore(t)
			store.SetGPT55ProbeFunc(func(context.Context, *Account) GPT55ProbeResult {
				return tc.result
			})

			got := store.ProbeGPT55Account(context.Background(), account, true)
			if got.Outcome != tc.result.Outcome {
				t.Fatalf("probe outcome = %q, want %q", got.Outcome, tc.result.Outcome)
			}

			if account.SupportsGPT55 != tc.wantSupported {
				t.Fatalf("SupportsGPT55 = %v, want %v", account.SupportsGPT55, tc.wantSupported)
			}
			if (account.GPT55CheckedAt.IsZero()) == tc.wantChecked {
				t.Fatalf("GPT55CheckedAt zero = %v, want checked=%v", account.GPT55CheckedAt.IsZero(), tc.wantChecked)
			}
			if account.GPT55LastError != tc.wantLastError {
				t.Fatalf("GPT55LastError = %q, want %q", account.GPT55LastError, tc.wantLastError)
			}

			row, err := db.GetAccountByID(context.Background(), account.ID())
			if err != nil {
				t.Fatalf("GetAccountByID: %v", err)
			}

			_, hasSupportKey := row.Credentials["supports_gpt_5_5"]
			if hasSupportKey != tc.expectSupportKey {
				t.Fatalf("supports_gpt_5_5 present = %v, want %v", hasSupportKey, tc.expectSupportKey)
			}

			_, hasCheckedKey := row.Credentials["gpt_5_5_checked_at"]
			if hasCheckedKey != tc.expectCheckedKey {
				t.Fatalf("gpt_5_5_checked_at present = %v, want %v", hasCheckedKey, tc.expectCheckedKey)
			}

			if gotLastError := row.GetCredential("gpt_5_5_last_error"); gotLastError != tc.wantLastError {
				t.Fatalf("db gpt_5_5_last_error = %q, want %q", gotLastError, tc.wantLastError)
			}
		})
	}
}
