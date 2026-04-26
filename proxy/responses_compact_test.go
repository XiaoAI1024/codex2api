package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestResponsesCompactRejectsStream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	h := &Handler{configKeys: map[string]bool{"sk-test": true}}
	h.RegisterRoutes(r)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-5.4","stream":true}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestPrepareCompactRequestBodyNormalizesForUpstream(t *testing.T) {
	body, reasoningEffort := prepareCompactRequestBody([]byte(`{"model":"gpt-5.4","input":"hello","stream":false,"instructions":null,"reasoning_effort":"high","context_management":[{"type":"compaction"}],"truncation":"disabled","user":"u1"}`), "gpt-5.4")

	if gjson.GetBytes(body, "stream").Exists() {
		t.Fatalf("stream should be removed from compact upstream body: %s", string(body))
	}
	store := gjson.GetBytes(body, "store")
	if !store.Exists() || store.Type != gjson.False {
		t.Fatalf("store = %s, want false field present; body=%s", store.Raw, string(body))
	}
	if got := gjson.GetBytes(body, "parallel_tool_calls").Bool(); !got {
		t.Fatalf("parallel_tool_calls = %v, want true; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, "include.0").String(); got != "reasoning.encrypted_content" {
		t.Fatalf("include.0 = %q, want reasoning.encrypted_content; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, "instructions").String(); got != "" {
		t.Fatalf("instructions = %q, want empty string", got)
	}
	if got := gjson.GetBytes(body, "input.0.role").String(); got != "user" {
		t.Fatalf("input.0.role = %q, want user; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, "reasoning.effort").String(); got != "high" {
		t.Fatalf("reasoning.effort = %q, want high; body=%s", got, string(body))
	}
	if gjson.GetBytes(body, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed from compact upstream body: %s", string(body))
	}
	if reasoningEffort != "high" {
		t.Fatalf("reasoningEffort = %q, want high", reasoningEffort)
	}
	for _, field := range []string{"context_management", "truncation", "user"} {
		if gjson.GetBytes(body, field).Exists() {
			t.Fatalf("%s should be removed from compact upstream body: %s", field, string(body))
		}
	}
}

func TestPrepareCompactRequestBodyNormalizesBuiltinToolsAndRoles(t *testing.T) {
	body, _ := prepareCompactRequestBody([]byte(`{
		"model":"gpt-5.4",
		"input":[{"role":"system","content":"be terse"}],
		"tools":[{"type":"web_search_preview"}],
		"tool_choice":{
			"type":"web_search_preview_2025_03_11",
			"tools":[{"type":"web_search_preview"}]
		}
	}`), "gpt-5.4")

	if got := gjson.GetBytes(body, "input.0.role").String(); got != "developer" {
		t.Fatalf("input.0.role = %q, want developer; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, "tools.0.type").String(); got != "web_search" {
		t.Fatalf("tools.0.type = %q, want web_search; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, "tool_choice.type").String(); got != "web_search" {
		t.Fatalf("tool_choice.type = %q, want web_search; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, "tool_choice.tools.0.type").String(); got != "web_search" {
		t.Fatalf("tool_choice.tools.0.type = %q, want web_search; body=%s", got, string(body))
	}
}

func TestExecuteRequestTracedToPathUsesCompactEndpoint(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	oldBaseURL := codexBaseURL
	codexBaseURL = server.URL
	defer func() { codexBaseURL = oldBaseURL }()

	account := &auth.Account{
		AccessToken: "access-token",
		AccountID:   "account-id",
	}

	resp, _, err := ExecuteRequestTracedToPath(context.Background(), account, []byte(`{"model":"gpt-5.4","input":"hello"}`), "", "", "", nil, nil, "/responses/compact", false)
	if err != nil {
		t.Fatalf("ExecuteRequestTracedToPath error: %v", err)
	}
	defer resp.Body.Close()

	if gotPath != "/responses/compact" {
		t.Fatalf("path = %q, want %q", gotPath, "/responses/compact")
	}
	if len(gotBody) == 0 {
		t.Fatal("expected request body to be forwarded")
	}
}
