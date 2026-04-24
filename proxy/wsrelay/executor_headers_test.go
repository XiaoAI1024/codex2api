package wsrelay

import (
	"net/http"
	"testing"

	"github.com/codex2api/proxy"
)

func TestPrepareWebsocketHeaders_DoesNotSetVersionByDefault(t *testing.T) {
	exec := NewExecutor()

	headers := exec.prepareWebsocketHeaders(
		"token",
		nil,
		"acc-id",
		"",
		nil,
		http.Header{},
	)

	if got := headers.Get("Authorization"); got != "Bearer token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := headers.Get("Version"); got != "" {
		t.Fatalf("Version = %q, want empty by default", got)
	}
	if got := headers.Get("Chatgpt-Account-Id"); got != "acc-id" {
		t.Fatalf("Chatgpt-Account-Id = %q", got)
	}
	if got := headers.Get("Originator"); got != proxy.Originator {
		t.Fatalf("Originator = %q", got)
	}
}

func TestPrepareWebsocketHeaders_ForwardsExplicitVersionHeader(t *testing.T) {
	exec := NewExecutor()
	downstreamHeaders := http.Header{}
	downstreamHeaders.Set("Version", "0.130.0")

	headers := exec.prepareWebsocketHeaders(
		"token",
		nil,
		"acc-id",
		"",
		nil,
		downstreamHeaders,
	)

	if got := headers.Get("Version"); got != "0.130.0" {
		t.Fatalf("Version = %q, want downstream header", got)
	}
}
