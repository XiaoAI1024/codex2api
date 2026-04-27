package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func newRouteCoverageHandler() *Handler {
	store := auth.NewStore(nil, nil, nil)
	store.AddAccount(&auth.Account{
		DBID:        201,
		AccessToken: "access-token",
		AccountID:   "account-id",
		PlanType:    "plus",
		Status:      auth.StatusReady,
	})
	handler := NewHandler(store, nil, nil, nil)
	handler.configKeys = map[string]bool{"sk-test": true}
	return handler
}

func withRouteCoverageUpstream(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	oldBaseURL := codexBaseURL
	codexBaseURL = server.URL
	t.Cleanup(func() { codexBaseURL = oldBaseURL })
}

func TestChatCompletionsRouteForwardsDeveloperRole(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotPath string
	var gotBody []byte
	withRouteCoverageUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	})

	router := gin.New()
	newRouteCoverageHandler().RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5.4",
		"messages":[
			{"role":"developer","content":"developer rules"},
			{"role":"user","content":"hello"}
		]
	}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotPath != "/responses" {
		t.Fatalf("upstream path = %q, want /responses", gotPath)
	}
	if got := gjson.GetBytes(gotBody, "input.0.role").String(); got != "developer" {
		t.Fatalf("input.0.role = %q, want developer; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.1.role").String(); got != "user" {
		t.Fatalf("input.1.role = %q, want user; body=%s", got, string(gotBody))
	}
}

func TestRootResponsesRouteDoesNotAutoAddImageGenerationTool(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotPath string
	var gotBody []byte
	withRouteCoverageUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}` + "\n\n"))
	})

	router := gin.New()
	newRouteCoverageHandler().RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(`{"model":"gpt-5.4","input":"hello","tools":[{"type":"web_search"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotPath != "/responses" {
		t.Fatalf("upstream path = %q, want /responses", gotPath)
	}
	if gjson.GetBytes(gotBody, "tools.1").Exists() {
		t.Fatalf("image_generation should not be auto-added in explicit mode: %s", string(gotBody))
	}
}
