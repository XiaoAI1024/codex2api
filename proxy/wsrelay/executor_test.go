package wsrelay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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
