package proxy

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

func newGPT55HandlerTestStore(t *testing.T, total int) (*auth.Store, []int64) {
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
	ids := make([]int64, 0, total)
	for i := 0; i < total; i++ {
		id, err := db.InsertATAccount(ctx, "probe", "at-token", "")
		if err != nil {
			t.Fatalf("insert account %d: %v", i, err)
		}
		if err := db.UpdateCredentials(ctx, id, map[string]interface{}{
			"email":      "probe@example.com",
			"account_id": "acct_probe",
			"expires_at": time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339),
		}); err != nil {
			t.Fatalf("seed credentials %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	store := auth.NewStore(db, cache.NewMemory(1), nil)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	return store, ids
}

func newGPT55GinContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("POST", "http://127.0.0.1/v1/responses", nil)
	ctx.Set("x-model", "gpt-5.5")
	return ctx
}

func TestAcquireAccountForRequestPrefersKnownGPT55Support(t *testing.T) {
	store, ids := newGPT55HandlerTestStore(t, 2)
	supported := store.FindByID(ids[1])
	if supported == nil {
		t.Fatal("expected supported account")
	}
	supported.SupportsGPT55 = true
	supported.GPT55CheckedAt = time.Now().UTC()

	unsupported := store.FindByID(ids[0])
	if unsupported == nil {
		t.Fatal("expected unsupported account")
	}
	unsupported.SupportsGPT55 = false
	unsupported.GPT55CheckedAt = time.Now().UTC()

	var probeCalls int
	store.SetGPT55ProbeFunc(func(context.Context, *auth.Account) auth.GPT55ProbeResult {
		probeCalls++
		return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeFailed, LastError: "should not run"}
	})

	handler := &Handler{store: store}
	got := handler.acquireAccountForRequest(newGPT55GinContext(t), nil)
	if got == nil {
		t.Fatal("acquireAccountForRequest() returned nil")
	}
	if got.ID() != supported.ID() {
		t.Fatalf("picked account %d, want %d", got.ID(), supported.ID())
	}
	if probeCalls != 0 {
		t.Fatalf("probeCalls = %d, want 0", probeCalls)
	}
}

func TestAcquireAccountForRequestLazilyProbesUnknownGPT55Account(t *testing.T) {
	store, ids := newGPT55HandlerTestStore(t, 2)
	unsupported := store.FindByID(ids[0])
	if unsupported == nil {
		t.Fatal("expected unsupported account")
	}
	unsupported.SupportsGPT55 = false
	unsupported.GPT55CheckedAt = time.Now().UTC()

	unknown := store.FindByID(ids[1])
	if unknown == nil {
		t.Fatal("expected unknown account")
	}

	var probeCalls int
	store.SetGPT55ProbeFunc(func(_ context.Context, acc *auth.Account) auth.GPT55ProbeResult {
		probeCalls++
		if acc.ID() != unknown.ID() {
			t.Fatalf("probed account %d, want %d", acc.ID(), unknown.ID())
		}
		return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeSupported}
	})

	handler := &Handler{store: store}
	got := handler.acquireAccountForRequest(newGPT55GinContext(t), nil)
	if got == nil {
		t.Fatal("acquireAccountForRequest() returned nil")
	}
	if got.ID() != unknown.ID() {
		t.Fatalf("picked account %d, want %d", got.ID(), unknown.ID())
	}
	if probeCalls != 1 {
		t.Fatalf("probeCalls = %d, want 1", probeCalls)
	}
	if !unknown.SupportsGPT55 {
		t.Fatal("expected lazy probe to mark account as supports gpt-5.5")
	}
}
