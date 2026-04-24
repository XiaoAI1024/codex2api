package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

func newProbeModelHandler(t *testing.T, total int) (*Handler, []*auth.Account) {
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
		id, err := db.InsertATAccount(ctx, "admin-probe", "at-token", "")
		if err != nil {
			t.Fatalf("insert account %d: %v", i, err)
		}
		if err := db.UpdateCredentials(ctx, id, map[string]interface{}{
			"email":      "admin-probe@example.com",
			"account_id": "acct_admin_probe",
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

	accounts := make([]*auth.Account, 0, len(ids))
	for _, id := range ids {
		acc := store.FindByID(id)
		if acc == nil {
			t.Fatalf("missing runtime account %d", id)
		}
		accounts = append(accounts, acc)
	}

	handler := NewHandler(store, db, cache.NewMemory(1), nil, "")
	return handler, accounts
}

func TestProbeModelSupportRejectsUnsupportedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, _ := newProbeModelHandler(t, 1)

	body := bytes.NewBufferString(`{"model":"gpt-4.1"}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/accounts/probe-model", body)
	ctx.Request.Header.Set("Content-Type", "application/json")

	handler.ProbeModelSupport(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	var payload map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := payload["error"]; got == "" {
		t.Fatal("expected error message")
	}
}

func TestProbeModelSupportReturnsSummary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, accounts := newProbeModelHandler(t, 3)
	handler.probeGPT55 = func(_ context.Context, acc *auth.Account, force bool) auth.GPT55ProbeResult {
		if !force {
			t.Fatal("expected force=true for manual probe endpoint")
		}
		switch acc.ID() {
		case accounts[0].ID():
			return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeSupported}
		case accounts[1].ID():
			return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeUnsupported, LastError: "no access"}
		default:
			return auth.GPT55ProbeResult{Outcome: auth.GPT55ProbeOutcomeFailed, LastError: "timeout"}
		}
	}

	body := bytes.NewBufferString(`{"model":"gpt-5.5","concurrency":2}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/accounts/probe-model", body)
	ctx.Request.Header.Set("Content-Type", "application/json")

	handler.ProbeModelSupport(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var payload struct {
		Model       string `json:"model"`
		Total       int    `json:"total"`
		Supported   int    `json:"supported"`
		Unsupported int    `json:"unsupported"`
		Failed      int    `json:"failed"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload.Model != "gpt-5.5" {
		t.Fatalf("model = %q, want %q", payload.Model, "gpt-5.5")
	}
	if payload.Total != 3 || payload.Supported != 1 || payload.Unsupported != 1 || payload.Failed != 1 {
		t.Fatalf("unexpected summary: %+v", payload)
	}
}
