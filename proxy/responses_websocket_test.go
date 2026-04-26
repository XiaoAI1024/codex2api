package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

func TestNormalizeResponsesWebsocketRequestMessageAcceptsCreateAndPlainJSON(t *testing.T) {
	createBody, last, err := normalizeResponsesWebsocketRequestMessage(
		[]byte(`{"type":"response.create","model":"gpt-5.4","stream":false,"input":"hello"}`),
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("normalize create: %v", err)
	}
	if gjson.GetBytes(createBody, "type").Exists() {
		t.Fatalf("response.create type should be stripped before upstream HTTP normalization: %s", string(createBody))
	}
	if !gjson.GetBytes(createBody, "stream").Bool() {
		t.Fatalf("stream should be forced true: %s", string(createBody))
	}
	if !strings.Contains(string(last), "gpt-5.4") {
		t.Fatalf("last request should be updated: %s", string(last))
	}

	plainBody, _, err := normalizeResponsesWebsocketRequestMessage(
		[]byte(`{"model":"gpt-5.4","stream":false,"input":"hello"}`),
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("normalize plain JSON: %v", err)
	}
	if !gjson.GetBytes(plainBody, "stream").Bool() {
		t.Fatalf("plain JSON websocket body should be normalized to stream=true: %s", string(plainBody))
	}
}

func TestNormalizeResponsesWebsocketRequestMessageMergesAppendInput(t *testing.T) {
	lastRequest := []byte(`{"model":"gpt-5.4","stream":true,"input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[{"type":"message","id":"assistant-1"}]`)

	normalized, next, err := normalizeResponsesWebsocketRequestMessage(
		[]byte(`{"type":"response.append","input":[{"type":"message","id":"msg-2"}]}`),
		lastRequest,
		lastResponseOutput,
	)
	if err != nil {
		t.Fatalf("normalize append: %v", err)
	}

	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 3 {
		t.Fatalf("merged input len = %d, want 3; body=%s", len(input), string(normalized))
	}
	if input[0].Get("id").String() != "msg-1" ||
		input[1].Get("id").String() != "assistant-1" ||
		input[2].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected merged input order: %s", string(normalized))
	}
	if string(next) != string(normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestMessagePreservesExplicitPreviousResponseID(t *testing.T) {
	lastRequest := []byte(`{"model":"gpt-5.4","stream":true,"input":[{"type":"message","id":"msg-1"}],"instructions":"be terse"}`)
	lastResponseOutput := []byte(`[{"type":"message","id":"assistant-1"}]`)

	normalized, next, err := normalizeResponsesWebsocketRequestMessage(
		[]byte(`{"type":"response.create","previous_response_id":"resp-prev","input":[{"type":"message","id":"msg-2"}]}`),
		lastRequest,
		lastResponseOutput,
	)
	if err != nil {
		t.Fatalf("normalize create with previous_response_id: %v", err)
	}

	if prev := gjson.GetBytes(normalized, "previous_response_id").String(); prev != "resp-prev" {
		t.Fatalf("previous_response_id = %q, want preserved; body=%s", prev, string(normalized))
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 1 || input[0].Get("id").String() != "msg-2" {
		t.Fatalf("input should remain incremental when previous_response_id is present; body=%s", string(normalized))
	}
	if model := gjson.GetBytes(normalized, "model").String(); model != "gpt-5.4" {
		t.Fatalf("model = %q, want inherited gpt-5.4; body=%s", model, string(normalized))
	}
	if instructions := gjson.GetBytes(normalized, "instructions").String(); instructions != "be terse" {
		t.Fatalf("instructions = %q, want inherited instructions; body=%s", instructions, string(normalized))
	}
	if string(next) != string(normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestResponsesWebsocketForwardsSSEDataPayloads(t *testing.T) {
	gin.SetMode(gin.TestMode)

	oldWebsocketExecuteFunc := WebsocketExecuteFunc
	defer func() { WebsocketExecuteFunc = oldWebsocketExecuteFunc }()

	var gotBody []byte
	var gotHeaders http.Header
	WebsocketExecuteFunc = func(
		ctx context.Context,
		account *auth.Account,
		requestBody []byte,
		sessionID string,
		proxyOverride string,
		apiKey string,
		deviceCfg *DeviceProfileConfig,
		headers http.Header,
	) (*http.Response, error) {
		gotBody = append([]byte(nil), requestBody...)
		gotHeaders = headers.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[{\"type\":\"message\",\"id\":\"msg-1\"}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
			)),
		}, nil
	}

	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency: 1,
		MaxRetries:     0,
		TestModel:      "gpt-5.4",
	})
	store.AddAccount(&auth.Account{
		DBID:        1,
		AccessToken: "access-token",
		AccountID:   "account-id",
		ExpiresAt:   time.Now().Add(time.Hour),
		Status:      auth.StatusReady,
	})

	router := gin.New()
	handler := NewHandler(store, nil, nil, nil)
	router.GET("/v1/responses", handler.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer sk-test")
	headers.Set("Version", "0.130.0")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5.4","input":"hello"}`)); err != nil {
		t.Fatalf("write message: %v", err)
	}

	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read first payload: %v", err)
	}
	_, second, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read second payload: %v", err)
	}

	if got := gjson.GetBytes(first, "type").String(); got != "response.created" {
		t.Fatalf("first payload type = %q, want response.created; payload=%s", got, string(first))
	}
	if got := gjson.GetBytes(second, "type").String(); got != "response.completed" {
		t.Fatalf("second payload type = %q, want response.completed; payload=%s", got, string(second))
	}
	if gjson.GetBytes(gotBody, "type").Exists() {
		t.Fatalf("handler should strip downstream response.create type before ExecuteRequestTraced: %s", string(gotBody))
	}
	if !gjson.GetBytes(gotBody, "stream").Bool() {
		t.Fatalf("upstream body should be stream=true: %s", string(gotBody))
	}
	if got := gotHeaders.Get("Version"); got != "0.130.0" {
		t.Fatalf("Version header = %q, want explicit downstream value", got)
	}
}

func TestResponsesWebsocketTreatsUpstreamErrorFrameAsTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)

	oldWebsocketExecuteFunc := WebsocketExecuteFunc
	defer func() { WebsocketExecuteFunc = oldWebsocketExecuteFunc }()

	WebsocketExecuteFunc = func(
		ctx context.Context,
		account *auth.Account,
		requestBody []byte,
		sessionID string,
		proxyOverride string,
		apiKey string,
		deviceCfg *DeviceProfileConfig,
		headers http.Header,
	) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"error\",\"status\":429,\"error\":{\"message\":\"rate limited\",\"type\":\"rate_limit_error\"}}\n\n",
			)),
		}, nil
	}

	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency: 1,
		MaxRetries:     0,
		TestModel:      "gpt-5.4",
	})
	store.AddAccount(&auth.Account{
		DBID:        1,
		AccessToken: "access-token",
		AccountID:   "account-id",
		ExpiresAt:   time.Now().Add(time.Hour),
		Status:      auth.StatusReady,
	})

	router := gin.New()
	handler := NewHandler(store, nil, nil, nil)
	router.GET("/v1/responses", handler.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer sk-test")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5.4","input":"hello"}`)); err != nil {
		t.Fatalf("write message: %v", err)
	}

	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read error payload: %v", err)
	}
	if got := gjson.GetBytes(first, "type").String(); got != "error" {
		t.Fatalf("first payload type = %q, want error; payload=%s", got, string(first))
	}
	if got := int(gjson.GetBytes(first, "status").Int()); got != http.StatusTooManyRequests {
		t.Fatalf("first payload status = %d, want 429; payload=%s", got, string(first))
	}
	if got := gjson.GetBytes(first, "error.message").String(); got != "rate limited" {
		t.Fatalf("first payload error.message = %q, want rate limited; payload=%s", got, string(first))
	}

	if err := conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, second, err := conn.ReadMessage()
	if err == nil {
		t.Fatalf("unexpected second payload after terminal upstream error: %s", string(second))
	}
}
