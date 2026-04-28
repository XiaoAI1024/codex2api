package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestNewSQLiteInitializesFreshDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")

	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	if got := db.Driver(); got != "sqlite" {
		t.Fatalf("Driver() = %q, want %q", got, "sqlite")
	}
}

func TestSQLiteUsageLogsHasAPIKeyColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")

	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	columns, err := db.sqliteTableColumns(context.Background(), "usage_logs")
	if err != nil {
		t.Fatalf("sqliteTableColumns 返回错误: %v", err)
	}

	for _, name := range []string{"api_key_id", "api_key_name", "api_key_masked", "image_count", "image_width", "image_height", "image_bytes", "image_format", "image_size"} {
		if _, ok := columns[name]; !ok {
			t.Fatalf("usage_logs 缺少列 %q", name)
		}
	}
}

func TestUsageLogsPersistImageMetadata(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")

	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.InsertUsageLog(ctx, &UsageLogInput{
		AccountID:        1,
		Endpoint:         "/v1/images/generations",
		InboundEndpoint:  "/v1/images/generations",
		UpstreamEndpoint: "/v1/responses",
		Model:            "gpt-image-2-4k",
		StatusCode:       200,
		DurationMs:       1200,
		ImageCount:       1,
		ImageWidth:       3840,
		ImageHeight:      2160,
		ImageBytes:       2457600,
		ImageFormat:      "png",
		ImageSize:        "3840x2160",
	}); err != nil {
		t.Fatalf("InsertUsageLog 返回错误: %v", err)
	}
	db.flushLogs()

	logs, err := db.ListRecentUsageLogs(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecentUsageLogs 返回错误: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("len(logs) = %d, want 1", len(logs))
	}
	got := logs[0]
	if got.ImageCount != 1 || got.ImageWidth != 3840 || got.ImageHeight != 2160 || got.ImageBytes != 2457600 || got.ImageFormat != "png" || got.ImageSize != "3840x2160" {
		t.Fatalf("image metadata = %#v", got)
	}
}

func TestSoftDeleteAccountPhysicallyDeletesAccountAndRelatedRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")

	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	id, err := db.InsertAccount(ctx, "delete-me", "rt-delete-me", "")
	if err != nil {
		t.Fatalf("InsertAccount 返回错误: %v", err)
	}
	if err := db.InsertUsageLog(ctx, &UsageLogInput{
		AccountID:  id,
		Endpoint:   "/v1/responses",
		Model:      "gpt-5.4",
		StatusCode: 200,
	}); err != nil {
		t.Fatalf("InsertUsageLog 返回错误: %v", err)
	}
	db.flushLogs()
	if err := db.InsertAccountEvent(ctx, id, "deleted", "manual"); err != nil {
		t.Fatalf("InsertAccountEvent 返回错误: %v", err)
	}
	if _, err := db.conn.ExecContext(ctx, `CREATE TABLE public_account_settlements (id INTEGER PRIMARY KEY AUTOINCREMENT, account_id INTEGER NOT NULL)`); err != nil {
		t.Fatalf("创建 public_account_settlements 返回错误: %v", err)
	}
	if _, err := db.conn.ExecContext(ctx, `INSERT INTO public_account_settlements (account_id) VALUES ($1)`, id); err != nil {
		t.Fatalf("插入 public_account_settlements 返回错误: %v", err)
	}

	if err := db.SoftDeleteAccount(ctx, id); err != nil {
		t.Fatalf("SoftDeleteAccount 返回错误: %v", err)
	}

	assertSQLiteCount(t, db, `SELECT COUNT(*) FROM accounts WHERE id = $1`, []interface{}{id}, 0)
	assertSQLiteCount(t, db, `SELECT COUNT(*) FROM usage_logs WHERE account_id = $1`, []interface{}{id}, 0)
	assertSQLiteCount(t, db, `SELECT COUNT(*) FROM account_events WHERE account_id = $1`, []interface{}{id}, 1)
	assertSQLiteCount(t, db, `SELECT COUNT(*) FROM public_account_settlements WHERE account_id = $1`, []interface{}{id}, 0)

	if err := db.InsertUsageLog(ctx, &UsageLogInput{
		AccountID:  id,
		Endpoint:   "/v1/responses",
		Model:      "gpt-5.4",
		StatusCode: 200,
	}); err != nil {
		t.Fatalf("deleted account InsertUsageLog 返回错误: %v", err)
	}
	db.flushLogs()
	assertSQLiteCount(t, db, `SELECT COUNT(*) FROM usage_logs WHERE account_id = $1`, []interface{}{id}, 0)
}

func TestSQLiteMigratesLegacyDeletedAccounts(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	ctx := context.Background()

	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	id, err := db.InsertAccount(ctx, "legacy-delete", "rt-legacy-delete", "")
	if err != nil {
		t.Fatalf("InsertAccount 返回错误: %v", err)
	}
	if _, err := db.conn.ExecContext(ctx, `
		UPDATE accounts
		SET status = 'error', error_message = 'deleted', updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`, id); err != nil {
		t.Fatalf("写入 legacy deleted 账号返回错误: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close 返回错误: %v", err)
	}

	db, err = New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	var status string
	var errorMessage string
	var deletedAt sql.NullString
	if err := db.conn.QueryRowContext(ctx, `SELECT status, error_message, deleted_at FROM accounts WHERE id = $1`, id).Scan(&status, &errorMessage, &deletedAt); err != nil {
		t.Fatalf("查询迁移后账号返回错误: %v", err)
	}
	if status != "deleted" {
		t.Fatalf("status = %q, want deleted", status)
	}
	if errorMessage != "" {
		t.Fatalf("error_message = %q, want empty", errorMessage)
	}
	if !deletedAt.Valid || deletedAt.String == "" {
		t.Fatal("deleted_at 未迁移")
	}
}

func TestSetErrorDeletedPhysicallyDeletesAccount(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")

	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	id, err := db.InsertAccount(ctx, "seterror-delete", "rt-seterror-delete", "")
	if err != nil {
		t.Fatalf("InsertAccount 返回错误: %v", err)
	}
	if err := db.SetError(ctx, id, "deleted"); err != nil {
		t.Fatalf("SetError 返回错误: %v", err)
	}

	assertSQLiteCount(t, db, `SELECT COUNT(*) FROM accounts WHERE id = $1`, []interface{}{id}, 0)
}

func TestBatchSoftDeleteAccountsPhysicallyDeletesAccounts(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")

	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	firstID, err := db.InsertAccount(ctx, "batch-delete-1", "rt-batch-delete-1", "")
	if err != nil {
		t.Fatalf("InsertAccount first 返回错误: %v", err)
	}
	secondID, err := db.InsertAccount(ctx, "batch-delete-2", "rt-batch-delete-2", "")
	if err != nil {
		t.Fatalf("InsertAccount second 返回错误: %v", err)
	}
	keepID, err := db.InsertAccount(ctx, "batch-keep", "rt-batch-keep", "")
	if err != nil {
		t.Fatalf("InsertAccount keep 返回错误: %v", err)
	}

	if err := db.BatchSoftDeleteAccounts(ctx, []int64{firstID, secondID}); err != nil {
		t.Fatalf("BatchSoftDeleteAccounts 返回错误: %v", err)
	}

	assertSQLiteCount(t, db, `SELECT COUNT(*) FROM accounts WHERE id IN ($1, $2)`, []interface{}{firstID, secondID}, 0)
	assertSQLiteCount(t, db, `SELECT COUNT(*) FROM accounts WHERE id = $1`, []interface{}{keepID}, 1)
}

func TestBatchSetErrorDeletedPhysicallyDeletesAccounts(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")

	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	firstID, err := db.InsertAccount(ctx, "batch-seterror-delete-1", "rt-batch-seterror-delete-1", "")
	if err != nil {
		t.Fatalf("InsertAccount first 返回错误: %v", err)
	}
	secondID, err := db.InsertAccount(ctx, "batch-seterror-delete-2", "rt-batch-seterror-delete-2", "")
	if err != nil {
		t.Fatalf("InsertAccount second 返回错误: %v", err)
	}

	if err := db.BatchSetError(ctx, []int64{firstID, secondID}, "deleted"); err != nil {
		t.Fatalf("BatchSetError 返回错误: %v", err)
	}

	assertSQLiteCount(t, db, `SELECT COUNT(*) FROM accounts WHERE id IN ($1, $2)`, []interface{}{firstID, secondID}, 0)
}

func TestListActiveIncludesErrorAccounts(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")

	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	id, err := db.InsertAccount(ctx, "error-account", "rt-error", "")
	if err != nil {
		t.Fatalf("InsertAccount 返回错误: %v", err)
	}
	if err := db.SetError(ctx, id, "batch test failed"); err != nil {
		t.Fatalf("SetError 返回错误: %v", err)
	}

	rows, err := db.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive 返回错误: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListActive 返回 %d 条，want 1", len(rows))
	}
	if rows[0].Status != "error" {
		t.Fatalf("status = %q, want error", rows[0].Status)
	}
	if rows[0].ErrorMessage != "batch test failed" {
		t.Fatalf("error_message = %q, want batch test failed", rows[0].ErrorMessage)
	}
}

func TestUsageLogsFilterByAPIKeyID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")

	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	now := time.Now().UTC()
	targetAPIKeyID := int64(7)

	logs := []*UsageLogInput{
		{
			AccountID:    1,
			Endpoint:     "/v1/chat/completions",
			Model:        "gpt-5.4",
			StatusCode:   200,
			DurationMs:   120,
			APIKeyID:     targetAPIKeyID,
			APIKeyName:   "Team A",
			APIKeyMasked: "sk-a****...****1111",
		},
		{
			AccountID:    1,
			Endpoint:     "/v1/responses",
			Model:        "gpt-5.4",
			StatusCode:   200,
			DurationMs:   220,
			APIKeyID:     targetAPIKeyID,
			APIKeyName:   "Team A",
			APIKeyMasked: "sk-a****...****1111",
		},
		{
			AccountID:    2,
			Endpoint:     "/v1/responses",
			Model:        "gpt-5.4-mini",
			StatusCode:   200,
			DurationMs:   320,
			APIKeyID:     8,
			APIKeyName:   "Team B",
			APIKeyMasked: "sk-b****...****2222",
		},
	}

	for _, usageLog := range logs {
		if err := db.InsertUsageLog(ctx, usageLog); err != nil {
			t.Fatalf("InsertUsageLog 返回错误: %v", err)
		}
	}
	db.flushLogs()

	recentLogs, err := db.ListRecentUsageLogs(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecentUsageLogs 返回错误: %v", err)
	}
	if len(recentLogs) != len(logs) {
		t.Fatalf("recentLogs 长度 = %d, want %d", len(recentLogs), len(logs))
	}

	foundSnapshot := false
	for _, usageLog := range recentLogs {
		if usageLog.APIKeyID == targetAPIKeyID {
			foundSnapshot = true
			if usageLog.APIKeyName != "Team A" {
				t.Fatalf("APIKeyName = %q, want %q", usageLog.APIKeyName, "Team A")
			}
			if usageLog.APIKeyMasked != "sk-a****...****1111" {
				t.Fatalf("APIKeyMasked = %q, want %q", usageLog.APIKeyMasked, "sk-a****...****1111")
			}
		}
	}
	if !foundSnapshot {
		t.Fatal("未找到带 API 密钥快照的最近日志")
	}

	page, err := db.ListUsageLogsByTimeRangePaged(ctx, UsageLogFilter{
		Start:    now.Add(-1 * time.Hour),
		End:      now.Add(1 * time.Hour),
		Page:     1,
		PageSize: 10,
		APIKeyID: &targetAPIKeyID,
	})
	if err != nil {
		t.Fatalf("ListUsageLogsByTimeRangePaged 返回错误: %v", err)
	}

	if page.Total != 2 {
		t.Fatalf("page.Total = %d, want %d", page.Total, 2)
	}
	if len(page.Logs) != 2 {
		t.Fatalf("len(page.Logs) = %d, want %d", len(page.Logs), 2)
	}
	for _, usageLog := range page.Logs {
		if usageLog.APIKeyID != targetAPIKeyID {
			t.Fatalf("APIKeyID = %d, want %d", usageLog.APIKeyID, targetAPIKeyID)
		}
		if usageLog.APIKeyName != "Team A" {
			t.Fatalf("APIKeyName = %q, want %q", usageLog.APIKeyName, "Team A")
		}
	}
}

func TestSQLiteUsageLogsTimeRangeUsesUTCStorage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")

	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	createdUTC := time.Date(2026, 4, 23, 20, 6, 0, 0, time.UTC)
	if _, err := db.conn.ExecContext(ctx, `
		INSERT INTO usage_logs (
			account_id, endpoint, inbound_endpoint, upstream_endpoint, model,
			status_code, total_tokens, input_tokens, output_tokens, created_at
		)
		VALUES (1, '/v1/images/generations', '/v1/images/generations', '/v1/responses', 'gpt-image-2',
			200, 1790, 34, 1756, $1)
	`, sqliteTimeParam(createdUTC)); err != nil {
		t.Fatalf("insert usage log 返回错误: %v", err)
	}

	shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)
	localCreated := createdUTC.In(shanghai)
	page, err := db.ListUsageLogsByTimeRangePaged(ctx, UsageLogFilter{
		Start:    localCreated.Add(-1 * time.Hour),
		End:      localCreated.Add(1 * time.Hour),
		Page:     1,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListUsageLogsByTimeRangePaged 返回错误: %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("page.Total = %d, want %d", page.Total, 1)
	}
	if len(page.Logs) != 1 {
		t.Fatalf("len(page.Logs) = %d, want %d", len(page.Logs), 1)
	}
	if got := page.Logs[0].InboundEndpoint; got != "/v1/images/generations" {
		t.Fatalf("InboundEndpoint = %q, want /v1/images/generations", got)
	}
	if got := page.Logs[0].Model; got != "gpt-image-2" {
		t.Fatalf("Model = %q, want gpt-image-2", got)
	}
}

func assertSQLiteCount(t *testing.T, db *DB, query string, args []interface{}, want int) {
	t.Helper()

	var got int
	if err := db.conn.QueryRowContext(context.Background(), query, args...).Scan(&got); err != nil {
		t.Fatalf("查询数量返回错误: %v", err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", query, got, want)
	}
}
