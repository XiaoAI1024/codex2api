package wsrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

type capturedWebsocketFrame struct {
	messageType int
	data        []byte
	err         error
}

func TestSendRequestWritesRawResponseCreateJSONFrame(t *testing.T) {
	frameCh := make(chan capturedWebsocketFrame, 1)
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			frameCh <- capturedWebsocketFrame{err: err}
			return
		}
		defer conn.Close()

		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		messageType, data, err := conn.ReadMessage()
		frameCh <- capturedWebsocketFrame{
			messageType: messageType,
			data:        data,
			err:         err,
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	session := NewSession(123, nil)
	session.ID = "session-for-pending"
	wc := NewWsConnection(conn, session, wsURL)
	body := []byte(`{"type":"response.create","model":"gpt-5.4","stream":true}`)

	exec := NewExecutor()
	if err := exec.sendRequest(wc, body, "request-id-for-pending-only"); err != nil {
		t.Fatalf("sendRequest: %v", err)
	}

	var frame capturedWebsocketFrame
	select {
	case frame = <-frameCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for websocket frame")
	}

	if frame.err != nil {
		t.Fatalf("read websocket frame: %v", frame.err)
	}
	if frame.messageType != websocket.TextMessage {
		t.Fatalf("message type = %d, want TextMessage", frame.messageType)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(frame.data, &got); err != nil {
		t.Fatalf("frame is not JSON: %v; frame: %s", err, frame.data)
	}

	var gotType string
	if err := json.Unmarshal(got["type"], &gotType); err != nil {
		t.Fatalf("frame type is not a JSON string: %v; frame: %s", err, frame.data)
	}
	if gotType != "response.create" {
		t.Fatalf("top-level type = %q, want raw response.create body; frame: %s", gotType, frame.data)
	}
	if _, ok := got["content"]; ok {
		t.Fatalf("frame contains envelope content field; frame: %s", frame.data)
	}
	if _, ok := got["request_id"]; ok {
		t.Fatalf("frame contains request_id; request ID must stay pending-only; frame: %s", frame.data)
	}
	if !bytes.Equal(frame.data, body) {
		t.Fatalf("frame = %s, want raw body %s", frame.data, body)
	}
}

func TestWebsocketAcquireConnectionDoesNotCloseActiveSameAccountConnection(t *testing.T) {
	upgrader := websocket.Upgrader{}
	accepted := make(chan struct{}, 2)
	done := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		accepted <- struct{}{}
		<-done
	}))
	defer server.Close()
	defer close(done)

	manager := NewManager()
	defer manager.Stop()

	account := &auth.Account{
		DBID:        123,
		AccessToken: "access-token",
		AccountID:   "account-id",
	}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	wc1, err := manager.AcquireConnection(context.Background(), account, wsURL, nil, "")
	if err != nil {
		t.Fatalf("first AcquireConnection: %v", err)
	}
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first websocket")
	}

	wc2, err := manager.AcquireConnection(context.Background(), account, wsURL, nil, "")
	if err != nil {
		t.Fatalf("second AcquireConnection: %v", err)
	}
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second websocket")
	}

	if !wc1.IsConnected() {
		t.Fatal("first active connection was closed by acquiring a second same-account websocket")
	}
	if !wc2.IsConnected() {
		t.Fatal("second connection should be active")
	}
}

func TestWebsocketPongHandlerRefreshesReadDeadline(t *testing.T) {
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		time.Sleep(25 * time.Millisecond)
		_ = conn.WriteControl(websocket.PongMessage, []byte("keepalive"), time.Now().Add(time.Second))
		time.Sleep(100 * time.Millisecond)
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed"}`))
	}))
	defer server.Close()

	manager := NewManager()
	defer manager.Stop()

	account := &auth.Account{
		DBID:        123,
		AccessToken: "access-token",
		AccountID:   "account-id",
	}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	wc, err := manager.AcquireConnection(context.Background(), account, wsURL, nil, "")
	if err != nil {
		t.Fatalf("AcquireConnection: %v", err)
	}
	defer wc.Close()

	if err := wc.conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, data, err := wc.conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage should survive past original deadline after pong: %v", err)
	}
	if string(data) != `{"type":"response.completed"}` {
		t.Fatalf("message = %s, want response.completed", string(data))
	}
}

func TestPrepareWebsocketBodyPreservesPreviousResponseID(t *testing.T) {
	exec := &Executor{}
	body := []byte(`{"model":"gpt-5.4","previous_response_id":"resp-prev","input":[{"type":"message","role":"user","content":"again"}]}`)

	got := exec.prepareWebsocketBody(body, "session-id")

	if prev := gjson.GetBytes(got, "previous_response_id").String(); prev != "resp-prev" {
		t.Fatalf("previous_response_id = %q, want preserved; body=%s", prev, string(got))
	}
	if typ := gjson.GetBytes(got, "type").String(); typ != "response.create" {
		t.Fatalf("type = %q, want response.create; body=%s", typ, string(got))
	}
}

func TestExecuteRequestWebsocketBodyCloseClosesUpstreamImmediately(t *testing.T) {
	upgrader := websocket.Upgrader{}
	closed := make(chan error, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			closed <- err
			return
		}
		defer conn.Close()

		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, err := conn.ReadMessage(); err != nil {
			closed <- err
			return
		}
		_, _, err = conn.ReadMessage()
		closed <- err
	}))
	defer server.Close()

	manager := NewManager()
	defer manager.Stop()
	oldBaseURL := codexWebsocketBaseURL
	oldExecutor := globalExecutor
	oldOnce := executorOnce
	codexWebsocketBaseURL = server.URL
	executorOnce = sync.Once{}
	executorOnce.Do(func() {
		globalExecutor = NewExecutorWithManager(manager)
	})
	defer func() {
		codexWebsocketBaseURL = oldBaseURL
		globalExecutor = oldExecutor
		executorOnce = oldOnce
	}()

	account := &auth.Account{
		DBID:        123,
		AccessToken: "access-token",
		AccountID:   "account-id",
	}
	resp, err := ExecuteRequestWebsocket(
		context.Background(),
		account,
		[]byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"hello"}]}`),
		"session-id",
		"",
		"sk-test",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("ExecuteRequestWebsocket: %v", err)
	}

	if err := resp.Body.Close(); err != nil {
		t.Fatalf("Body.Close: %v", err)
	}

	select {
	case err := <-closed:
		if err == nil {
			t.Fatal("server saw nil error, want websocket close/read error")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Body.Close did not close upstream websocket promptly")
	}
}

func TestWebsocketReadStreamForwardsUpstreamErrorFrameAsTerminalPayload(t *testing.T) {
	upgrader := websocket.Upgrader{}
	done := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","status":429,"error":{"message":"rate limited","type":"rate_limit_error"}}`)); err != nil {
			return
		}
		<-done
	}))
	defer server.Close()
	defer close(done)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	session := NewSession(123, nil)
	session.SetConnected(true)
	pending := session.AddPendingRequest("session-id")
	resp := &WsResponse{
		conn:        NewWsConnection(conn, session, wsURL),
		pendingReq:  pending,
		sessionID:   "session-id",
		readErrChan: make(chan error, 1),
	}

	var frames [][]byte
	err = resp.ReadStream(func(data []byte) bool {
		frames = append(frames, bytes.Clone(data))
		return true
	})
	if err != nil {
		t.Fatalf("ReadStream returned error instead of forwarding error frame: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("forwarded frame count = %d, want 1", len(frames))
	}
	if got := gjson.GetBytes(frames[0], "type").String(); got != "error" {
		t.Fatalf("forwarded frame type = %q, want error; frame=%s", got, string(frames[0]))
	}
	if got := int(gjson.GetBytes(frames[0], "status").Int()); got != http.StatusTooManyRequests {
		t.Fatalf("forwarded status = %d, want 429; frame=%s", got, string(frames[0]))
	}
}
