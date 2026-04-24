package proxy

import (
	"net/http"
	"testing"
)

func TestResolveCodexRequestIdentityStableFallback(t *testing.T) {
	identity := resolveCodexRequestIdentity(nil, "", nil, nil)
	stable := StableCodexClientProfile()
	if identity.UserAgent != stable.UserAgent {
		t.Fatalf("User-Agent = %q, want %q", identity.UserAgent, stable.UserAgent)
	}
	if identity.Version != stable.Version {
		t.Fatalf("Version = %q, want %q", identity.Version, stable.Version)
	}
}

func TestResolveCodexRequestIdentityUsesDownstreamCodexUA(t *testing.T) {
	headers := http.Header{}
	headers.Set("User-Agent", "codex_cli_rs/0.119.0 (Ubuntu 24.04; x86_64) kitty/0.40.0")

	identity := resolveCodexRequestIdentity(nil, "", headers, &DeviceProfileConfig{StabilizeDeviceProfile: false})
	if identity.UserAgent != headers.Get("User-Agent") {
		t.Fatalf("User-Agent = %q, want downstream %q", identity.UserAgent, headers.Get("User-Agent"))
	}
	if identity.Version != "0.119.0" {
		t.Fatalf("Version = %q, want %q", identity.Version, "0.119.0")
	}
}

func TestApplyCodexRequestHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	identity := codexRequestIdentity{
		UserAgent: "codex_cli_rs/0.120.0 (Mac OS 15.5.0; arm64) Apple_Terminal/464",
		Version:   "0.120.0",
	}
	applyCodexRequestHeaders(req, "token", "acc-id", "session-123", identity, true, nil)

	if got := req.Header.Get("Authorization"); got != "Bearer token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("User-Agent"); got != identity.UserAgent {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := req.Header.Get("Version"); got != "" {
		t.Fatalf("Version = %q, want empty by default", got)
	}
	if got := req.Header.Get("Session_id"); got != "session-123" {
		t.Fatalf("Session_id = %q", got)
	}
	if got := req.Header.Get("Conversation_id"); got != "session-123" {
		t.Fatalf("Conversation_id = %q", got)
	}
	if got := req.Header.Get("Chatgpt-Account-Id"); got != "acc-id" {
		t.Fatalf("Chatgpt-Account-Id = %q", got)
	}
	if got := req.Header.Get("Originator"); got != Originator {
		t.Fatalf("Originator = %q", got)
	}
	if got := req.Header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept = %q", got)
	}
}

func TestApplyCodexRequestHeaders_ForwardsExplicitVersionHeader(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	identity := codexRequestIdentity{
		UserAgent: "codex_cli_rs/0.120.0 (Mac OS 15.5.0; arm64) Apple_Terminal/464",
		Version:   "0.120.0",
	}
	downstreamHeaders := http.Header{}
	downstreamHeaders.Set("Version", "0.130.0")

	applyCodexRequestHeaders(req, "token", "acc-id", "session-123", identity, true, downstreamHeaders)

	if got := req.Header.Get("Version"); got != "0.130.0" {
		t.Fatalf("Version = %q, want downstream header", got)
	}
}
