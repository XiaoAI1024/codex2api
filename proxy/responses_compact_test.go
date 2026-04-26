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
	body, reasoningEffort := prepareCompactRequestBody([]byte(`{"model":"gpt-5.4","input":"hello","stream":false,"store":true,"parallel_tool_calls":false,"instructions":null,"reasoning_effort":"high","context_management":[{"type":"compaction"}],"truncation":"disabled","user":"u1"}`), "gpt-5.4")

	if gjson.GetBytes(body, "stream").Exists() {
		t.Fatalf("stream should be removed from compact upstream body: %s", string(body))
	}
	if gjson.GetBytes(body, "store").Exists() {
		t.Fatalf("store should be removed from compact upstream body: %s", string(body))
	}
	if gjson.GetBytes(body, "parallel_tool_calls").Exists() {
		t.Fatalf("parallel_tool_calls should be removed from compact upstream body: %s", string(body))
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

func TestResponsesCompactDoesNotAddImageGenerationTool(t *testing.T) {
	gin.SetMode(gin.TestMode)

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

	r := gin.New()
	store := auth.NewStore(nil, nil, nil)
	store.AddAccount(&auth.Account{DBID: 101, AccessToken: "access-token", AccountID: "account-id", PlanType: "plus"})
	h := NewHandler(store, nil, nil, nil)
	h.configKeys = map[string]bool{"sk-test": true}
	h.RegisterRoutes(r)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-5.4","input":"hello","store":true,"parallel_tool_calls":false,"tools":[{"type":"web_search"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotPath != "/responses/compact" {
		t.Fatalf("upstream path = %q, want /responses/compact", gotPath)
	}
	for _, tool := range gjson.GetBytes(gotBody, "tools").Array() {
		if tool.Get("type").String() == "image_generation" {
			t.Fatalf("compact body should not include image_generation tool: %s", string(gotBody))
		}
	}
	if gjson.GetBytes(gotBody, "store").Exists() {
		t.Fatalf("compact body should not forward store: %s", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "parallel_tool_calls").Exists() {
		t.Fatalf("compact body should not forward parallel_tool_calls: %s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "tools.0.type").String(); got != "web_search" {
		t.Fatalf("tools.0.type = %q, want web_search; body=%s", got, string(gotBody))
	}
}

func TestResponsesAddsImageGenerationTool(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n"))
	}))
	defer server.Close()

	oldBaseURL := codexBaseURL
	codexBaseURL = server.URL
	defer func() { codexBaseURL = oldBaseURL }()

	r := gin.New()
	store := auth.NewStore(nil, nil, nil)
	store.AddAccount(&auth.Account{DBID: 102, AccessToken: "access-token", AccountID: "account-id", PlanType: "plus"})
	h := NewHandler(store, nil, nil, nil)
	h.configKeys = map[string]bool{"sk-test": true}
	h.RegisterRoutes(r)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","input":"hello","tools":[{"type":"web_search"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotPath != "/responses" {
		t.Fatalf("upstream path = %q, want /responses", gotPath)
	}
	if got := gjson.GetBytes(gotBody, "tools.1.type").String(); got != "image_generation" {
		t.Fatalf("tools.1.type = %q, want image_generation; body=%s", got, string(gotBody))
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
